package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"tgdown/pkg/config"
	"tgdown/pkg/downloader"
	"tgdown/pkg/i18n"
	"tgdown/pkg/storage"
	"tgdown/pkg/telegram"
)

// RunCLIMode ejecuta el modo CLI sin servidor HTTP
func RunCLIMode(url string) int {
	// 1. Inicializar rutas y configuración
	config.InitPaths()
	i18n.SetLanguage(i18n.DetectSystemLanguage())

	fmt.Println(i18n.T("cli.modeHeader"))
	fmt.Println("======================")

	// 2. Abrir base de datos (la única: tgdown.sqlite3). Si está dañada, se
	// aparta renombrada, se crea una nueva y se rescata todo lo legible, así
	// que el fallback a un segundo archivo de la versión anterior se retiró.
	dbPath := filepath.Join(config.DataDir, "tgdown.sqlite3")
	st, err := storage.OpenResilient(dbPath)
	if err != nil {
		fmt.Fprint(os.Stderr, i18n.T("cli.errDatabase", err))
		return 1
	}

	// SEC-30: la base de datos (y sus ficheros -wal/-shm) se acaban de crear
	// con los permisos por defecto del sistema; se cierran a 0600 tras un
	// NewStorage exitoso. dbPath ya apunta a la BD realmente usada
	config.SecureDataDir(dbPath, dbPath+"-wal", dbPath+"-shm")

	defer st.Close()

	// 3. Migrar .env legacy si existe
	st.MigrateLegacyEnv()
	bindHost, bindPort := st.ServerBinding()
	config.SetServerBinding(bindHost, bindPort)

	// 4. Cargar configuración desde SQLite
	cfg, err := st.LoadConfig(config.DefaultConfig(), filepath.Join(config.BaseDir, "config.json"))
	if err != nil {
		cfg = config.DefaultConfig()
	}
	cfg = config.NormalizeConfig(cfg)

	if cfg.Language != "" {
		i18n.SetLanguage(cfg.Language)
	}

	// 5. Verificar credenciales de Telegram
	apiID, apiHash, _ := st.GetCredentials()
	if apiID == "" || apiHash == "" {
		fmt.Fprint(os.Stderr, i18n.T("cli.errNoCredentials"))
		fmt.Fprint(os.Stderr, i18n.T("cli.errNoCredentialsHint"))
		return 1
	}

	// 6. Inicializar cliente Telegram
	fmt.Println(i18n.T("cli.connecting"))
	cm := telegram.NewClientManager()
	if err := cm.InitClient(apiID, apiHash); err != nil {
		fmt.Fprint(os.Stderr, i18n.T("cli.errStartTelegram", err))
		return 1
	}
	defer cm.Stop()

	// Esperar a que el cliente esté listo
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := cm.WaitReady(ctx); err != nil {
		fmt.Fprint(os.Stderr, i18n.T("cli.errWaitTelegram", err))
		return 1
	}
	fmt.Println(i18n.T("cli.connected"))

	// 7. Inicializar motor de descargas (SIN servidor)
	eng := downloader.NewEngine(cm, st, cfg)

	// 8. Registrar callback de progreso para CLI
	reporter := NewCLIProgressReporter()
	eng.OnStateChange(reporter.OnStateChange)

	// 9. Procesar la URL
	fmt.Print(i18n.T("cli.processingURL", url))
	parsed, err := downloader.ParseURL(url)
	if err != nil {
		fmt.Fprint(os.Stderr, i18n.T("cli.errParseURL", err))
		return 1
	}

	// 10. Resolver username si es necesario
	chatID := parsed.ChatID
	if chatID == 0 && parsed.ChatUsername != "" {
		fmt.Print(i18n.T("cli.resolvingUser", parsed.ChatUsername))
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		resolvedID, err := cm.ResolveUsername(ctx, parsed.ChatUsername)
		if err != nil {
			fmt.Fprint(os.Stderr, i18n.T("cli.errUserNotFound", err))
			return 1
		}
		chatID = resolvedID
		fmt.Print(i18n.T("cli.resolvedUser", chatID))
	}

	// 11. Crear items para el rango de mensajes
	jobID := config.NewID()
	msgCount := parsed.EndMsgID - parsed.StartMsgID + 1
	fmt.Print(i18n.T("cli.creatingTasks", msgCount))

	itemIDs := make([]string, 0, msgCount)
	for msgID := parsed.StartMsgID; msgID <= parsed.EndMsgID; msgID++ {
		item := storage.DownloadItem{
			ID:        config.NewID(),
			JobID:     jobID,
			MessageID: int64(msgID),
			ChatID:    chatID,
			Status:    "queued",
			Source:    "manual",
			FileName:  fmt.Sprintf("mensaje_%d", msgID),
		}
		eng.QueueItem(item)
		itemIDs = append(itemIDs, item.ID)
	}

	fmt.Print(i18n.T("cli.queuedWaiting"))

	// 12. Esperar a que termine
	return waitForDownloads(eng, itemIDs)
}

// waitForDownloads espera a que todas las descargas terminen
func waitForDownloads(eng *downloader.Engine, ids []string) int {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	checkInterval := time.NewTicker(100 * time.Millisecond)
	defer checkInterval.Stop()

	for {
		select {
		case <-checkInterval.C:
			// Chequeo rápido para detectar cuando todo termina
		case <-ticker.C:
			// Reporte periódico
		}

		allDone := true
		anyFailed := false
		completedCount := 0
		failedCount := 0
		skippedCount := 0

		for _, id := range ids {
			item, ok := eng.GetItem(id)
			if !ok {
				continue
			}

			if item.Status == "downloading" || item.Status == "queued" || item.Status == "pending" {
				allDone = false
			}
			if item.Status == "failed" {
				anyFailed = true
				failedCount++
			}
			if item.Status == "completed" {
				completedCount++
			}
			if item.Status == "skipped" || item.Status == "duplicate" {
				skippedCount++
			}
		}

		if allDone {
			fmt.Print("\n\n")
			fmt.Println("======================")
			fmt.Println(i18n.T("cli.summaryHeader"))
			fmt.Print(i18n.T("cli.summaryCompleted", completedCount))
			fmt.Print(i18n.T("cli.summarySkipped", skippedCount))
			if failedCount > 0 {
				fmt.Print(i18n.T("cli.summaryFailed", failedCount))
			}
			fmt.Println("======================")

			if anyFailed {
				fmt.Println(i18n.T("cli.someFailed"))
				return 1
			}
			fmt.Println(i18n.T("cli.allCompleted"))
			return 0
		}
	}
}
