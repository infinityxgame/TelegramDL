package downloader

// Reintentos de descarga: clasifica que errores vale la pena reintentar y
// aplica backoff entre intentos.

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"
)

func (e *Engine) executeDownloadWithRetry(ctx context.Context, itemID string) error {
	const maxAttempts = 2
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err = e.executeDownload(ctx, itemID)
		if err == nil || !isRetryableDownloadError(err) || attempt == maxAttempts {
			return err
		}

		backoff := time.Duration(1<<(attempt-1)) * time.Second
		log.Printf("[DOWNLOAD] Reintentando item %s en %s (%d/%d): %v", itemID, backoff, attempt, maxAttempts-1, err)
		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
	return err
}

func isRetryableDownloadError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, errDownloadAlreadyExists) {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, permanent := range []string{
		"mensaje no encontrado",
		"no contiene multimedia",
		"espacio",
		"archivo ya existe",
		"error al abrir archivo",
		"error al preparar archivo",
		"error al finalizar archivo",
	} {
		if strings.Contains(message, permanent) {
			return false
		}
	}
	return true
}
