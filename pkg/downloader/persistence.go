package downloader

// Persistencia en segundo plano del estado de las descargas: aqui vive el
// bucle que escribe en SQLite sin bloquear el hilo que actualiza el progreso.

import (
	"log"

	"tgdown/pkg/storage"
)

func (e *Engine) persistenceLoop() {
	for item := range e.persistCh {
		if e.storage != nil {
			if err := e.storage.SaveDownload(item); err != nil {
				log.Printf("[DOWNLOADER] error guardando estado de descarga en BD: %v", err)
			}
		}
		e.persistWG.Done()
	}
}

func (e *Engine) enqueuePersist(item storage.DownloadItem) {
	if e.storage == nil {
		return
	}
	e.persistWG.Add(1)
	select {
	case e.persistCh <- item:
	default:
		e.persistWG.Done()
		// El estado actual permanece en memoria y el siguiente tick lo
		// volverá a persistir; nunca frenamos una escritura de Telegram.
	}
}

func (e *Engine) persistSeenChunks(itemID string) {
	if e.storage == nil {
		return
	}
	e.mu.Lock()
	chunks := e.pendingChunks[itemID]
	e.pendingChunks[itemID] = nil
	e.mu.Unlock()

	if len(chunks) > 0 {
		if err := e.storage.AddChunks(itemID, chunks); err != nil {
			log.Printf("[DOWNLOADER] error guardando chunks en BD: %v", err)
		}
	}
}
