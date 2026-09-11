package downloader

import (
	"testing"
	"time"

	"tgdown/pkg/config"
	"tgdown/pkg/storage"
	"tgdown/pkg/telegram"
)

func waitForCondition(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condición no alcanzada en 8s: %s", what)
}

func (e *Engine) statusOf(id string) string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if it, ok := e.downloads[id]; ok {
		return it.Status
	}
	return ""
}

func (e *Engine) inFlight(id string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.jobsInFlight[id]
}

// Reproduce el fallo reportado: al reducir el límite de descargas simultáneas,
// la tarea detenida en exceso debe volver a la cola CON un job vivo, y
// retomarse cuando la descarga en curso termina o se cancela. Antes del fix,
// el relanzamiento se hacía desde dentro del propio job mientras aún figuraba
// en jobsInFlight, se descartaba en silencio y la tarea quedaba huérfana.
func TestConcurrencyLimitRequeuesCancelledJob(t *testing.T) {
	cm := telegram.NewClientManager()
	// Cliente con forma válida que nunca conecta: los jobs se quedan
	// bloqueados en WaitReady hasta ser cancelados, simulando descargas.
	if err := cm.InitClient("1234567", "0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("InitClient: %v", err)
	}
	t.Cleanup(cm.Stop)

	cfg := config.DefaultConfig()
	cfg.DownloadFolder = t.TempDir()
	cfg.MaxConcurrentDownloads = 2
	eng := NewEngine(cm, nil, cfg)

	eng.QueueItem(storage.DownloadItem{ID: "a", ChatID: 10, MessageID: 1, FileName: "a.bin"})
	eng.QueueItem(storage.DownloadItem{ID: "b", ChatID: 10, MessageID: 2, FileName: "b.bin"})

	waitForCondition(t, "ambas descargas en curso", func() bool {
		return eng.statusOf("a") == "downloading" && eng.statusOf("b") == "downloading"
	})

	// Reducir el límite a 1: la tarea más reciente se detiene y debe quedar
	// en cola con un job vivo esperando hueco.
	cfg2 := cfg
	cfg2.MaxConcurrentDownloads = 1
	eng.UpdateConfig(cfg2)

	waitForCondition(t, "una sigue downloading y la otra está en cola con job vivo", func() bool {
		as, bs := eng.statusOf("a"), eng.statusOf("b")
		queuedWithJob := (as == "queued" && eng.inFlight("a")) || (bs == "queued" && eng.inFlight("b"))
		stillDownloading := as == "downloading" || bs == "downloading"
		return queuedWithJob && stillDownloading
	})

	// Al terminar (cancelar) la descarga activa, la encolada debe tomar el hueco.
	activeID, queuedID := "a", "b"
	if eng.statusOf("b") == "downloading" {
		activeID, queuedID = "b", "a"
	}
	if err := eng.CancelDownload(activeID); err != nil {
		t.Fatal(err)
	}

	waitForCondition(t, "la tarea encolada retoma la descarga", func() bool {
		return eng.statusOf(queuedID) == "downloading"
	})

	// Subir el límite no debe dejar nada huérfano: todo lo que siga en cola
	// tiene un job esperando.
	eng.CancelAll()
	waitForCondition(t, "ningún job huérfano tras cancelar todo", func() bool {
		eng.mu.RLock()
		defer eng.mu.RUnlock()
		for id, item := range eng.downloads {
			if item.Status == "queued" && !eng.jobsInFlight[id] {
				return false
			}
		}
		return true
	})
}
