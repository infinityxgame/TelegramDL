package downloader

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gotd/td/tg"
)

const downloadPartSize int64 = 512 * 1024

// downloadMissingParts descarga únicamente las partes que no aparecen en
// completed. UploadGetFile acepta offsets arbitrarios, por lo que no vuelve a
// transferir las partes que ya están en el temporal.
func downloadMissingParts(
	ctx context.Context,
	client interface {
		UploadGetFile(context.Context, *tg.UploadGetFileRequest) (tg.UploadFileClass, error)
	},
	location tg.InputFileLocationClass,
	output *progressWriterAt,
	totalBytes int64,
	workers int,
	completed map[int64]struct{},
) error {
	if totalBytes <= 0 {
		return fmt.Errorf("tamaño de archivo inválido para reanudación")
	}
	if workers < 1 {
		workers = 1
	}

	jobs := make(chan int64)
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	setErr := func(err error) {
		if err == nil {
			return
		}
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
		errMu.Unlock()
	}

	worker := func() {
		defer wg.Done()
		for offset := range jobs {
			limit := int(downloadPartSize)
			if remaining := totalBytes - offset; remaining < int64(limit) {
				limit = int(remaining)
			}

			var data []byte
			var err error
			for attempt := 0; attempt < 3; attempt++ {
				if workCtx.Err() != nil {
					return
				}
				response, requestErr := client.UploadGetFile(workCtx, &tg.UploadGetFileRequest{
					Location: location,
					Offset:   offset,
					Limit:    limit,
				})
				if requestErr == nil {
					file, ok := response.(*tg.UploadFile)
					if !ok {
						requestErr = fmt.Errorf("respuesta inesperada de Telegram: %T", response)
					} else {
						data = file.Bytes
						if len(data) == 0 || len(data) > limit {
							requestErr = fmt.Errorf("Telegram devolvió %d bytes para un bloque de %d", len(data), limit)
						}
					}
				}
				if requestErr == nil {
					break
				}
				err = requestErr
				if attempt < 2 {
					select {
					case <-workCtx.Done():
						return
					case <-time.After(time.Duration(attempt+1) * 300 * time.Millisecond):
					}
				}
			}
			if err != nil {
				setErr(fmt.Errorf("error descargando offset %d: %w", offset, err))
				return
			}
			if _, err = output.WriteAt(data, offset); err != nil {
				setErr(fmt.Errorf("error escribiendo offset %d: %w", offset, err))
				return
			}
		}
	}

	if workers > 16 {
		workers = 16
	}
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go worker()
	}

	sentJobs := true
	for offset := int64(0); offset < totalBytes; offset += downloadPartSize {
		if _, alreadyDone := completed[offset/downloadPartSize]; alreadyDone {
			continue
		}
		select {
		case jobs <- offset:
		case <-workCtx.Done():
			sentJobs = false
		}
		if !sentJobs {
			break
		}
	}
	close(jobs)
	wg.Wait()

	errMu.Lock()
	defer errMu.Unlock()
	if firstErr != nil {
		return firstErr
	}
	return workCtx.Err()
}
