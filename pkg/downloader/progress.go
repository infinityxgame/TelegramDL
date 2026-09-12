package downloader

// Calculo de progreso y limitacion de velocidad (throttle) de las
// descargas en curso.

import (
	"io"
	"math"
	"os"
	"time"

	"tgdown/pkg/config"
)

func (e *Engine) throttle(bytesCount int64) {
	e.mu.RLock()
	sp := e.config.SpeedLimit
	e.mu.RUnlock()

	if sp.Value <= 0 {
		return
	}

	mult := config.SpeedMultipliers[sp.Unit]
	limitBps := sp.Value * mult

	e.throttleMu.Lock()
	if e.throttleTime.IsZero() {
		e.throttleTime = time.Now()
		e.bytesSince = 0
	}
	e.bytesSince += bytesCount
	elapsed := time.Since(e.throttleTime).Seconds()
	expectedTime := float64(e.bytesSince) / limitBps
	sleepSec := expectedTime - elapsed
	e.throttleMu.Unlock()

	if sleepSec > 0.005 {
		time.Sleep(time.Duration(sleepSec * float64(time.Second)))
	}
}

type progressWriterAt struct {
	file   *os.File
	itemID string
	engine *Engine
	total  int64
}

func (pw *progressWriterAt) WriteAt(p []byte, off int64) (int, error) {
	n, err := pw.file.WriteAt(p, off)
	if n == len(p) && err == nil {
		pw.engine.onProgress(pw.itemID, int64(n), pw.total, off)
		pw.engine.throttle(int64(n))
	} else if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}

func (e *Engine) onProgress(itemID string, bytesWritten int64, totalBytes int64, offset int64) {
	e.mu.Lock()
	item, ok := e.downloads[itemID]
	if !ok || item.Status != "downloading" {
		e.mu.Unlock()
		return
	}

	now := time.Now()
	// Sumar siempre los bytes escritos al progreso actual para evitar que se detenga
	item.CurrentBytes += bytesWritten

	if totalBytes > 0 {
		item.TotalBytes = totalBytes
		progressVal := (float64(item.CurrentBytes) / float64(totalBytes)) * 100.0
		if progressVal >= 100.0 {
			item.Progress = 100.0
		} else {
			item.Progress = math.Min(99.9, progressVal)
		}
		item.TotalStr = config.FormatBytes(float64(totalBytes))
	}
	item.CurrentStr = config.FormatBytes(float64(item.CurrentBytes))
	item.UpdatedAt = float64(now.Unix())

	// Deduplicación solo para la base de datos (para no saturar con miles de inserts)
	chunkIndex := offset / downloadPartSize
	chunks := e.seenChunks[itemID]
	if chunks == nil {
		chunks = make(map[int64]struct{})
		e.seenChunks[itemID] = chunks
	}
	if _, seen := chunks[chunkIndex]; !seen {
		chunks[chunkIndex] = struct{}{}
		e.pendingChunks[itemID] = append(e.pendingChunks[itemID], chunkIndex)
	}

	lastTime := e.lastProgressTimes[itemID]
	lastBytes := e.lastProgressBytes[itemID]
	if !lastTime.IsZero() && bytesWritten > 0 {
		elapsed := now.Sub(lastTime).Seconds()
		bytesDelta := item.CurrentBytes - lastBytes
		if elapsed > 0 && bytesDelta >= 0 {
			instantSpeed := float64(bytesDelta) / elapsed
			// Suavizado exponencial basado en tiempo: las ráfagas de varios
			// workers no generan picos artificiales de velocidad.
			const smoothingWindow = 2.0
			alpha := 1 - math.Exp(-elapsed/smoothingWindow)
			speedVal := e.itemSpeeds[itemID] + alpha*(instantSpeed-e.itemSpeeds[itemID])
			item.Speed = config.FormatBytes(speedVal) + "/s"
			e.itemSpeeds[itemID] = speedVal
		}
	}
	if bytesWritten > 0 {
		e.lastProgressTimes[itemID] = now
		e.lastProgressBytes[itemID] = item.CurrentBytes
	}

	lastBroadcast, hasBroadcast := e.lastBroadcastTimes[itemID]
	shouldBroadcast := !hasBroadcast || now.Sub(lastBroadcast) >= 200*time.Millisecond
	if shouldBroadcast {
		e.lastBroadcastTimes[itemID] = now
	}

	lastSave, hasSave := e.lastSaveTimes[itemID]
	shouldSave := !hasSave || now.Sub(lastSave) >= 1*time.Second
	if shouldSave {
		e.lastSaveTimes[itemID] = now
	}

	cp := *item
	e.mu.Unlock()

	if shouldSave && e.storage != nil {
		e.enqueuePersist(cp)
		e.persistSeenChunks(itemID)
	}

	if shouldBroadcast {
		e.notifyState(cp)
	}
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
