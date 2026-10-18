package downloader

// Ejecucion de la descarga de un item concreto: resolver sus metadatos en
// Telegram, descargar el archivo y dejarlo en su ubicacion final.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tdDownloader "github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"

	"tgdown/pkg/config"
	"tgdown/pkg/i18n"
	"tgdown/pkg/logbus"
	"tgdown/pkg/storage"
	"tgdown/pkg/telegram"
)

func (e *Engine) resolveItemMetadata(itemID string) {
	e.mu.RLock()
	item, ok := e.downloads[itemID]
	if !ok {
		e.mu.RUnlock()
		return
	}
	// Copia bajo el mutex: fuera de él, el elemento del mapa lo escriben otras
	// goroutines (el progreso de la descarga, una pausa, el propio motor), así
	// que leer sus campos sin el candado es una carrera de datos.
	itemData := *item
	e.mu.RUnlock()

	// Si ya tiene nombre real y tamaño, no hacer nada.
	//
	// La comprobación va ANTES del semáforo. Antes iba después, así que un rango
	// de 500 mensajes dejaba 500 goroutines haciendo cola por un turno que la
	// mayoría ni necesitaba, cada una con un temporizador de un minuto vivo. De
	// `itemData` solo se usan ChatID y MessageID más abajo, y esos no cambian
	// nunca, así que leerlo aquí es igual de válido que leerlo después.
	if itemData.FileName != "" && !strings.HasPrefix(strings.ToLower(itemData.FileName), "mensaje_") && itemData.TotalBytes > 0 {
		return
	}

	// Limitar concurrencia de resolución de metadatos. El temporizador se para
	// al salir; con time.After quedaban vivos hasta cumplir el minuto.
	waitTimer := time.NewTimer(1 * time.Minute)
	defer waitTimer.Stop()
	select {
	case e.metadataSem <- struct{}{}:
		defer func() { <-e.metadataSem }()
	case <-waitTimer.C:
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := e.clientMgr.WaitReady(ctx); err != nil {
		return
	}

	msg, err := e.fetchMessage(ctx, itemData.ChatID, int(itemData.MessageID))
	if err != nil {
		// Aquí es donde se descubre, mientras la tarea todavía espera turno en
		// la cola, que ese ID del rango no existe. Se descarta ya, en vez de
		// dejarla en la lista ocupando sitio hasta que le toque descargar para
		// entonces darse cuenta de lo mismo.
		if errors.Is(err, errMissingMessage) {
			logbus.Warn(logbus.CatDownloads,
				i18n.T("downloads.msgMissing", itemData.MessageID),
				i18n.T("downloads.msgMissingDetail",
					e.chatLabel(itemData.ChatID)))
			e.discardDownload(itemID)
			if e.storage != nil {
				_ = e.storage.SetScannerItemDeleted(itemData.ChatID, itemData.MessageID, true)
			}
		}
		return
	}

	e.mu.Lock()
	e.messageCache[itemID] = msg
	it, exists := e.downloads[itemID]
	if !exists {
		e.mu.Unlock()
		return
	}

	media := ExtractMediaInfo(msg)
	if media == nil {
		e.mu.Unlock()
		return
	}

	// Un elemento de la bandeja de escucha ya trae el nombre que ve el usuario:
	// el del caption por defecto, o el que eligió con el selector de nombres.
	// Pisarlo aquí con el nombre del mensaje hacía que, al encolar varios de
	// golpe, todos salvo el primero acabaran con el caption: la resolución se
	// completaba mientras esperaban turno y executeDownload ya leía el nombre
	// sobrescrito. Solo se rellena el nombre cuando está vacío o es un
	// placeholder «mensaje_<id>», que es como llegan los rangos y las descargas
	// manuales por ID.
	if it.Source != "listener" || it.FileName == "" || strings.HasPrefix(strings.ToLower(it.FileName), "mensaje_") {
		it.FileName = media.FileName
	}
	it.Kind = string(media.Kind)
	it.TotalBytes = media.FileSize
	it.TotalStr = config.FormatBytes(float64(media.FileSize))
	cp := *it
	e.mu.Unlock()

	if e.storage != nil {
		if err := e.storage.SaveDownload(cp); err != nil {
			log.Printf("[DOWNLOADER] %s", i18n.T("downloader.saveStateError", err))
		}
	}
	e.notifyState(cp)
}

func (e *Engine) executeDownload(ctx context.Context, itemID string) error {
	e.mu.RLock()
	item, exists := e.downloads[itemID]
	var itemData storage.DownloadItem
	if exists {
		// Igual que en resolveItemMetadata: se trabaja sobre una copia y cada
		// escritura vuelve a tomar el mutex y a buscar el elemento en el mapa.
		// Guardarse el puntero y seguir leyéndolo sin candado era una carrera
		// con la goroutine que resuelve los metadatos y con la del progreso.
		itemData = *item
	}
	downloadFolder := e.config.DownloadFolder
	parallelChunks := e.config.ParallelChunks
	chunkWorkers := e.config.ChunkWorkers
	allowDuplicate := e.forceDuplicate[itemID]
	e.mu.RUnlock()
	if !exists {
		return errors.New("download not found")
	}

	if err := e.clientMgr.WaitReady(ctx); err != nil {
		log.Printf("[DOWNLOAD ERROR] %s", i18n.T("downloads.clientNotReadyErr", itemID, err))
		return fmt.Errorf("Telegram client not ready: %w", err)
	}

	rawClient := e.clientMgr.RawClient()
	if rawClient == nil {
		log.Printf("[DOWNLOAD ERROR] %s", i18n.T("downloads.clientNotReady", itemID))
		return errors.New("Telegram client not ready")
	}

	// Destino final: la carpeta de descargas y, si el archivo viene de un chat
	// vigilado y el reparto por chat está encendido, su subcarpeta. La ruta se
	// valida antes de pegarla: la genera el programa, pero sale de un nombre
	// que elige un tercero en Telegram.
	destination := downloadFolder
	if sub := SafeRelativePath(itemData.SubFolder); sub != "" {
		destination = filepath.Join(downloadFolder, sub)
	}

	_ = os.MkdirAll(destination, 0755)

	e.mu.Lock()
	cachedMsg := e.messageCache[itemID]
	e.mu.Unlock()

	var msg *tg.Message
	if cachedMsg != nil {
		msg = cachedMsg
		logbus.Debug(logbus.CatDownloads,
			i18n.T("downloads.reuseMsg", itemID), "")
	} else {
		log.Printf("[DOWNLOAD] %s", i18n.T("downloads.fetchingMsg", itemData.MessageID, itemData.ChatID))
		var err error
		msg, err = e.fetchMessage(ctx, itemData.ChatID, int(itemData.MessageID))
		if err != nil {
			log.Printf("[DOWNLOAD ERROR] %s", i18n.T("downloads.fetchMsgError", itemData.MessageID, err))
			return fmt.Errorf("error fetching message: %w", err)
		}
	}

	mediaInfo := ExtractMediaInfo(msg)
	if mediaInfo == nil {
		log.Printf("[DOWNLOAD ERROR] %s", i18n.T("downloads.noMedia", itemData.MessageID))
		return errors.New("message contains no downloadable media")
	}

	logbus.Debug(logbus.CatDownloads,
		i18n.T("downloads.mediaExtracted", mediaInfo.FileName, mediaInfo.Kind, mediaInfo.FileSize), "")

	e.mu.Lock()
	currentFileName := itemData.FileName
	// Si el nombre actual es un placeholder, lo actualizamos al nombre real extraído
	if strings.HasPrefix(strings.ToLower(currentFileName), "mensaje_") || currentFileName == "" {
		currentFileName = mediaInfo.FileName
		if actual, ok := e.downloads[itemID]; ok {
			actual.FileName = currentFileName
		}
	}
	e.mu.Unlock()

	finalPath, finalName, alreadyExists := e.reservations.ReservePath(
		destination, currentFileName, itemData.MessageID, mediaInfo.FileSize, allowDuplicate,
	)
	defer e.reservations.ReleasePath(finalPath)

	if alreadyExists {
		e.mu.Lock()
		actual, ok := e.downloads[itemID]
		if !ok {
			e.mu.Unlock()
			return errDownloadAlreadyExists
		}
		actual.FilePath = finalPath
		actual.FileName = finalName
		actual.Status = "duplicate"
		actual.Progress = 100.0
		actual.Speed = "0 B/s"
		cp := *actual
		e.mu.Unlock()
		if e.storage != nil {
			if err := e.storage.SaveDownload(cp); err != nil {
				log.Printf("[DOWNLOADER] %s", i18n.T("downloader.saveStateError", err))
			}
		}
		e.notifyState(cp)
		return errDownloadAlreadyExists
	}

	e.mu.Lock()
	actual, ok := e.downloads[itemID]
	if !ok {
		e.mu.Unlock()
		return errors.New("download not found")
	}
	// El tamaño que teníamos antes de pisarlo con el del mensaje: es lo que más
	// abajo permite darse cuenta de que el archivo cambió y de que los
	// fragmentos ya descargados no valen. Antes se comparaba item.TotalBytes
	// después de haberlo sobrescrito aquí mismo, así que la comprobación no
	// saltaba nunca.
	previousTotal := actual.TotalBytes

	actual.FilePath = finalPath
	actual.FileName = finalName
	actual.Kind = string(mediaInfo.Kind)
	if mediaInfo.FileSize > 0 {
		actual.TotalBytes = mediaInfo.FileSize
		actual.TotalStr = config.FormatBytes(float64(mediaInfo.FileSize))
	}
	cp := *actual
	e.mu.Unlock()

	if e.storage != nil {
		if err := e.storage.SaveDownload(cp); err != nil {
			log.Printf("[DOWNLOADER] %s", i18n.T("downloader.saveStateError", err))
		}
	}
	e.notifyState(cp)

	tempPath := finalPath + ".temp"
	tempFile, err := os.OpenFile(tempPath, os.O_CREATE|os.O_RDWR, 0666)
	if err != nil {
		return fmt.Errorf("error opening temporary file: %w", err)
	}
	defer tempFile.Close()

	resumeChunks := make(map[int64]struct{})
	if e.storage != nil {
		if savedChunks, chunksErr := e.storage.Chunks(itemID); chunksErr == nil {
			for index := range savedChunks {
				resumeChunks[int64(index)] = struct{}{}
			}
		}
	}
	if previousTotal > 0 && mediaInfo.FileSize > 0 && previousTotal != mediaInfo.FileSize {
		// El mensaje puede haber sido editado o sustituido desde la última
		// ejecución; los offsets anteriores ya no son confiables.
		resumeChunks = make(map[int64]struct{})
		if e.storage != nil {
			if err := e.storage.DeleteChunks(itemID); err != nil {
				log.Printf("[DOWNLOADER] %s", i18n.T("downloader.deleteChunksError", err))
			}
		}
	}
	if info, statErr := tempFile.Stat(); statErr != nil || mediaInfo.FileSize <= 0 || info.Size() != mediaInfo.FileSize {
		// Un temporal incompleto no puede reutilizarse de forma segura: sus
		// offsets persistidos podrían pertenecer a otra descarga.
		resumeChunks = make(map[int64]struct{})
		if e.storage != nil {
			if err := e.storage.DeleteChunks(itemID); err != nil {
				log.Printf("[DOWNLOADER] %s", i18n.T("downloader.deleteChunksError", err))
			}
		}
	}

	if mediaInfo.FileSize > 0 {
		if err := tempFile.Truncate(mediaInfo.FileSize); err != nil {
			return fmt.Errorf("error preparing temporary file: %w", err)
		}
	}

	// Reconstruir el progreso desde los offsets confirmados, nunca desde el
	// número de escrituras recibidas.
	var resumedBytes int64
	for index := range resumeChunks {
		offset := index * downloadPartSize
		if offset < mediaInfo.FileSize {
			resumedBytes += minInt64(downloadPartSize, mediaInfo.FileSize-offset)
		}
	}
	e.mu.Lock()
	if current, exists := e.downloads[itemID]; exists {
		current.CurrentBytes = resumedBytes
		current.CurrentStr = config.FormatBytes(float64(resumedBytes))
		if mediaInfo.FileSize > 0 {
			current.Progress = float64(resumedBytes) / float64(mediaInfo.FileSize) * 100
		}
	}
	seen := make(map[int64]struct{}, len(resumeChunks))
	for index := range resumeChunks {
		seen[index] = struct{}{}
	}
	e.seenChunks[itemID] = seen
	e.lastProgressBytes[itemID] = resumedBytes
	e.lastProgressTimes[itemID] = time.Now()
	e.mu.Unlock()

	threads := 1
	if parallelChunks && chunkWorkers > 1 {
		threads = chunkWorkers
	}
	// Más workers que bloques en vuelo no aporta: las peticiones de sobra se
	// quedan haciendo cola esperando ranura. Con el límite actual de bloques
	// (DownloadConnections * 8 = 64), permitimos hasta la mitad del límite global
	// por descarga para que con pocas descargas trabajen a pleno rendimiento.
	maxThreads := telegram.DownloadConnections * 4
	if threads > maxThreads {
		threads = maxThreads
	}

	// Los bloques salen por el grupo de conexiones de descarga; los metadatos
	// (el mensaje, el nombre, el tamaño) siguen yendo por la conexión principal
	// y así dejan de competir con ellos. El envoltorio es el que limita cuántas
	// peticiones van en vuelo y el que aguanta las pausas de Telegram, para los
	var downloadClient *tg.Client
	if mediaInfo.DCID > 0 {
		var dcErr error
		downloadClient, dcErr = e.clientMgr.DownloadClientForDC(ctx, mediaInfo.DCID)
		if dcErr != nil {
			logbus.Warn(logbus.CatDownloads,
				i18n.T("downloads.dcConnectWarn", mediaInfo.DCID, dcErr), "")
		} else {
			logbus.Debug(logbus.CatDownloads,
				i18n.T("downloads.usingDedicatedDC", mediaInfo.DCID), "")
		}
	}
	if downloadClient == nil {
		downloadClient = e.clientMgr.DownloadClient()
	}
	if downloadClient == nil {
		downloadClient = rawClient
	}
	client := wrapClient(downloadClient, e.clientMgr)

	logbus.Debug(logbus.CatDownloads,
		i18n.T("downloads.downloadingWorkers", finalName, threads, maxThreads),
		i18n.T("downloads.blocksDetail", downloadPartSize/1024, len(resumeChunks)))

	writer := &progressWriterAt{
		file:   tempFile,
		itemID: itemID,
		engine: e,
		total:  mediaInfo.FileSize,
	}

	if len(resumeChunks) > 0 {
		err = downloadMissingParts(ctx, client, mediaInfo.Location, writer, mediaInfo.FileSize, threads, resumeChunks)
	} else {
		dl := tdDownloader.NewDownloader().WithPartSize(int(downloadPartSize))
		builder := dl.Download(client, mediaInfo.Location).WithThreads(threads)
		_, err = builder.Parallel(ctx, writer)
	}

	// Asegurar que todos los fragmentos se persistan antes de finalizar
	e.persistSeenChunks(itemID)

	if err != nil {
		// Se deja constancia en el registro del motivo real por el que Telegram
		// cortó. Sin esto, cuando una descarga se ralentiza o muere no hay forma
		// de saber desde el panel si fue un límite de Telegram, la red o un
		// problema nuestro. Una pausa o cancelación no se avisa: no es un fallo.
		if !errors.Is(err, context.Canceled) {
			logbus.Warn(logbus.CatDownloads,
				i18n.T("downloads.telegramCut", finalName),
				i18n.T("downloads.telegramCutReason", err))
		}
		return err
	}

	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("error closing temporary file: %w", err)
	}

	// Renombrar con reintentos: en Windows el antivirus o un reproductor pueden
	// retener el archivo un instante. Nunca se borra el destino: os.Rename ya
	// sobrescribe cuando es posible, y si todo falla se conserva el archivo
	// existente en disco (y el .temp, para poder reintentar).
	var renameErr error
	for attempt := 0; attempt < 5; attempt++ {
		renameErr = os.Rename(tempPath, finalPath)
		if renameErr == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	if renameErr != nil {
		// Fallback por si el sistema mantiene bloqueado el temporal (copia manual)
		if copyErr := copyFile(tempPath, finalPath); copyErr != nil {
			return fmt.Errorf("error finalizing file: rename: %v; copy: %w", renameErr, copyErr)
		}
		// Si la copia funcionó, intentamos limpiar el temporal sin fallar si no se puede.
		_ = os.Remove(tempPath)
	}

	if e.storage != nil {
		if err := e.storage.DeleteChunks(itemID); err != nil {
			log.Printf("[DOWNLOAD] %s", i18n.T("downloads.cleanChunksWarn", itemID, err))
		}
	}
	return nil
}

func (e *Engine) fetchMessage(ctx context.Context, chatID int64, msgID int) (*tg.Message, error) {
	raw := e.clientMgr.RawClient()
	if raw == nil {
		return nil, errors.New("client not connected")
	}

	var messages []tg.MessageClass
	if chatID < 0 {
		isChannel := false
		channelID := -chatID
		s := strconv.FormatInt(chatID, 10)
		if strings.HasPrefix(s, "-100") && len(s) > 4 {
			isChannel = true
			if parsed, err := strconv.ParseInt(s[4:], 10, 64); err == nil {
				channelID = parsed
			}
		}

		if isChannel {
			accessHash, found := e.clientMgr.GetChannelAccessHash(channelID)
			if !found {
				log.Printf("[DOWNLOAD FETCH] %s", i18n.T("fetch.noAccessHashCache", channelID))
				_ = e.clientMgr.FetchDialogs(ctx)
				accessHash, found = e.clientMgr.GetChannelAccessHash(channelID)
			}
			if !found {
				log.Printf("[DOWNLOAD FETCH] %s", i18n.T("fetch.noAccessHashChannels", channelID))
				if chatsRes, err := raw.ChannelsGetChannels(ctx, []tg.InputChannelClass{
					&tg.InputChannel{ChannelID: channelID, AccessHash: 0},
				}); err == nil {
					if mc, ok := chatsRes.(*tg.MessagesChats); ok {
						for _, c := range mc.Chats {
							if ch, ok := c.(*tg.Channel); ok && ch.ID == channelID {
								accessHash = ch.AccessHash
								found = true
								e.clientMgr.SetChannelAccessHash(channelID, accessHash)
								// SEC-31: el access_hash es un secreto por-peer y estos logs se
								// exportan vía /api/logs/export, así que solo se registra el
								// resultado de la resolución, nunca el valor del hash.
								log.Printf("[DOWNLOAD FETCH] channel=%d resolved=true", channelID)
								break
							}
						}
					}
				}
			}
			// SEC-31: no se loguea el access_hash (secreto por-peer) porque los logs
			// son exportables vía /api/logs/export; se emite únicamente la señal
			// found=true/false junto a los identificadores, que no son secretos.
			log.Printf("[DOWNLOAD FETCH] channel=%d msgID=%d found=%v", channelID, msgID, found)

			req := &tg.ChannelsGetMessagesRequest{
				Channel: &tg.InputChannel{
					ChannelID:  channelID,
					AccessHash: accessHash,
				},
				ID: []tg.InputMessageClass{
					&tg.InputMessageID{ID: msgID},
				},
			}

			res, err := raw.ChannelsGetMessages(ctx, req)
			if err != nil {
				log.Printf("[DOWNLOAD FETCH ERROR] %s", i18n.T("fetch.channelMsgsError", channelID, msgID, err))
				return nil, fmt.Errorf("ChannelsGetMessages error: %w", err)
			}

			switch m := res.(type) {
			case *tg.MessagesMessages:
				messages = m.Messages
			case *tg.MessagesMessagesSlice:
				messages = m.Messages
			case *tg.MessagesChannelMessages:
				messages = m.Messages
			}
		} else {
			log.Printf("[DOWNLOAD FETCH] %s", i18n.T("fetch.basicChat", chatID, msgID))
			// Chat grupal básico
			res, err := raw.MessagesGetMessages(ctx, []tg.InputMessageClass{
				&tg.InputMessageID{ID: msgID},
			})
			if err != nil {
				log.Printf("[DOWNLOAD FETCH ERROR] %s", i18n.T("fetch.basicChatError", chatID, msgID, err))
				return nil, fmt.Errorf("MessagesGetMessages error: %w", err)
			}

			switch m := res.(type) {
			case *tg.MessagesMessages:
				messages = m.Messages
			case *tg.MessagesMessagesSlice:
				messages = m.Messages
			case *tg.MessagesChannelMessages:
				messages = m.Messages
			}
		}
	} else {
		log.Printf("[DOWNLOAD FETCH] %s", i18n.T("fetch.privateChat", chatID, msgID))
		// Usuario / Chat privado (chatID > 0)
		res, err := raw.MessagesGetMessages(ctx, []tg.InputMessageClass{
			&tg.InputMessageID{ID: msgID},
		})
		if err != nil {
			log.Printf("[DOWNLOAD FETCH ERROR] %s", i18n.T("fetch.privateChatError", chatID, msgID, err))
			return nil, fmt.Errorf("MessagesGetMessages error: %w", err)
		}

		switch m := res.(type) {
		case *tg.MessagesMessages:
			messages = m.Messages
		case *tg.MessagesMessagesSlice:
			messages = m.Messages
		case *tg.MessagesChannelMessages:
			messages = m.Messages
		}
	}

	if len(messages) == 0 {
		return nil, errMissingMessage
	}

	for _, m := range messages {
		if realMsg, ok := m.(*tg.Message); ok {
			return realMsg, nil
		}
	}

	// Telegram sí respondió, pero lo que devuelve no es un mensaje de usuario:
	// para un mensaje borrado manda un MessageEmpty, y para un aviso del chat un
	// MessageService. En los dos casos no hay nada que descargar y nunca lo
	// habrá, así que va por el mismo camino que un ID inexistente.
	return nil, fmt.Errorf("message was deleted on Telegram or contains no valid data: %w", errMissingMessage)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	// El error de Close no se descarta: es justo donde asoma un disco lleno o
	// una escritura que nunca llegó a tocar el archivo. Descartándolo, una copia
	// incompleta se daba por buena.
	return out.Close()
}
