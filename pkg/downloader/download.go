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
	"strings"
	"time"

	tdDownloader "github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"

	"tgdown/pkg/config"
	"tgdown/pkg/storage"
)

func (e *Engine) resolveItemMetadata(itemID string) {
	// Limitar concurrencia de resolución de metadatos
	select {
	case e.metadataSem <- struct{}{}:
		defer func() { <-e.metadataSem }()
	case <-time.After(1 * time.Minute):
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	e.mu.RLock()
	item, ok := e.downloads[itemID]
	if !ok {
		e.mu.RUnlock()
		return
	}
	// Si ya tiene nombre real y tamaño, no hacer nada
	if item.FileName != "" && !strings.HasPrefix(strings.ToLower(item.FileName), "mensaje_") && item.TotalBytes > 0 {
		e.mu.RUnlock()
		return
	}
	e.mu.RUnlock()

	if err := e.clientMgr.WaitReady(ctx); err != nil {
		return
	}

	msg, err := e.fetchMessage(ctx, item.ChatID, int(item.MessageID))
	if err != nil {
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

	it.FileName = media.FileName
	it.Kind = string(media.Kind)
	it.TotalBytes = media.FileSize
	it.TotalStr = config.FormatBytes(float64(media.FileSize))
	cp := *it
	e.mu.Unlock()

	if e.storage != nil {
		if err := e.storage.SaveDownload(cp); err != nil {
			log.Printf("[DOWNLOADER] error guardando estado de descarga en BD: %v", err)
		}
	}
	e.notifyState(cp)
}

func (e *Engine) executeDownload(ctx context.Context, itemID string) error {
	e.mu.RLock()
	item := e.downloads[itemID]
	downloadFolder := e.config.DownloadFolder
	parallelChunks := e.config.ParallelChunks
	chunkWorkers := e.config.ChunkWorkers
	allowDuplicate := e.forceDuplicate[itemID]
	e.mu.RUnlock()
	if item == nil {
		return errors.New("descarga no encontrada")
	}

	if err := e.clientMgr.WaitReady(ctx); err != nil {
		log.Printf("[DOWNLOAD ERROR] Cliente de Telegram no listo para item %s: %v", itemID, err)
		return fmt.Errorf("cliente de Telegram no listo: %w", err)
	}

	rawClient := e.clientMgr.RawClient()
	if rawClient == nil {
		log.Printf("[DOWNLOAD ERROR] Cliente de Telegram no listo para item %s", itemID)
		return errors.New("cliente de Telegram no listo")
	}

	_ = os.MkdirAll(downloadFolder, 0755)

	e.mu.Lock()
	cachedMsg := e.messageCache[itemID]
	e.mu.Unlock()

	var msg *tg.Message
	if cachedMsg != nil {
		msg = cachedMsg
		log.Printf("[DOWNLOAD] Usando mensaje cacheado para item %s", itemID)
	} else {
		log.Printf("[DOWNLOAD] Obteniendo mensaje %d del chat %d en Telegram...", item.MessageID, item.ChatID)
		var err error
		msg, err = e.fetchMessage(ctx, item.ChatID, int(item.MessageID))
		if err != nil {
			log.Printf("[DOWNLOAD ERROR] Error al obtener mensaje %d: %v", item.MessageID, err)
			return fmt.Errorf("error al obtener mensaje: %w", err)
		}
	}

	mediaInfo := ExtractMediaInfo(msg)
	if mediaInfo == nil {
		log.Printf("[DOWNLOAD ERROR] El mensaje %d no contiene multimedia descargable", item.MessageID)
		return errors.New("el mensaje no contiene multimedia descargable")
	}

	log.Printf("[DOWNLOAD] Multimedia extraída: %s (%s, %d bytes)", mediaInfo.FileName, mediaInfo.Kind, mediaInfo.FileSize)

	e.mu.Lock()
	currentFileName := item.FileName
	// Si el nombre actual es un placeholder, lo actualizamos al nombre real extraído
	if strings.HasPrefix(strings.ToLower(currentFileName), "mensaje_") || currentFileName == "" {
		item.FileName = mediaInfo.FileName
		currentFileName = mediaInfo.FileName
	}
	e.mu.Unlock()

	finalPath, finalName, alreadyExists := e.reservations.ReservePath(
		downloadFolder, currentFileName, item.MessageID, mediaInfo.FileSize, allowDuplicate,
	)
	defer e.reservations.ReleasePath(finalPath)

	if alreadyExists {
		e.mu.Lock()
		item.FilePath = finalPath
		item.FileName = finalName
		item.Status = "duplicate"
		item.Progress = 100.0
		item.Speed = "0 B/s"
		cp := *item
		e.mu.Unlock()
		if e.storage != nil {
			if err := e.storage.SaveDownload(cp); err != nil {
				log.Printf("[DOWNLOADER] error guardando estado de descarga en BD: %v", err)
			}
		}
		e.notifyState(cp)
		return errDownloadAlreadyExists
	}

	e.mu.Lock()
	item.FilePath = finalPath
	item.FileName = finalName
	item.Kind = string(mediaInfo.Kind)
	if mediaInfo.FileSize > 0 {
		item.TotalBytes = mediaInfo.FileSize
		item.TotalStr = config.FormatBytes(float64(mediaInfo.FileSize))
	}
	cp := *item
	e.mu.Unlock()

	if e.storage != nil {
		if err := e.storage.SaveDownload(cp); err != nil {
			log.Printf("[DOWNLOADER] error guardando estado de descarga en BD: %v", err)
		}
	}
	e.notifyState(cp)

	tempPath := finalPath + ".temp"
	tempFile, err := os.OpenFile(tempPath, os.O_CREATE|os.O_RDWR, 0666)
	if err != nil {
		return fmt.Errorf("error al abrir archivo temporal: %w", err)
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
	if item.TotalBytes > 0 && mediaInfo.FileSize > 0 && item.TotalBytes != mediaInfo.FileSize {
		// El mensaje puede haber sido editado o sustituido desde la última
		// ejecución; los offsets anteriores ya no son confiables.
		resumeChunks = make(map[int64]struct{})
		if e.storage != nil {
			if err := e.storage.DeleteChunks(itemID); err != nil {
				log.Printf("[DOWNLOADER] error eliminando chunks de BD: %v", err)
			}
		}
	}
	if info, statErr := tempFile.Stat(); statErr != nil || mediaInfo.FileSize <= 0 || info.Size() != mediaInfo.FileSize {
		// Un temporal incompleto no puede reutilizarse de forma segura: sus
		// offsets persistidos podrían pertenecer a otra descarga.
		resumeChunks = make(map[int64]struct{})
		if e.storage != nil {
			if err := e.storage.DeleteChunks(itemID); err != nil {
				log.Printf("[DOWNLOADER] error eliminando chunks de BD: %v", err)
			}
		}
	}

	if mediaInfo.FileSize > 0 {
		if err := tempFile.Truncate(mediaInfo.FileSize); err != nil {
			return fmt.Errorf("error al preparar archivo temporal: %w", err)
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

	writer := &progressWriterAt{
		file:   tempFile,
		itemID: itemID,
		engine: e,
		total:  mediaInfo.FileSize,
	}

	if len(resumeChunks) > 0 {
		err = downloadMissingParts(ctx, rawClient, mediaInfo.Location, writer, mediaInfo.FileSize, threads, resumeChunks)
	} else {
		dl := tdDownloader.NewDownloader().WithPartSize(int(downloadPartSize))
		builder := dl.Download(rawClient, mediaInfo.Location).WithThreads(threads)
		_, err = builder.Parallel(ctx, writer)
	}

	// Asegurar que todos los fragmentos se persistan antes de finalizar
	e.persistSeenChunks(itemID)

	if err != nil {
		return err
	}

	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("error al cerrar archivo temporal: %w", err)
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
			return fmt.Errorf("error al finalizar archivo: rename: %v; copia: %w", renameErr, copyErr)
		}
		// Si la copia funcionó, intentamos limpiar el temporal sin fallar si no se puede.
		_ = os.Remove(tempPath)
	}

	if e.storage != nil {
		if err := e.storage.DeleteChunks(itemID); err != nil {
			log.Printf("[DOWNLOAD] Advertencia: no se pudieron limpiar los fragmentos de %s: %v", itemID, err)
		}
	}
	return nil
}

func (e *Engine) fetchMessage(ctx context.Context, chatID int64, msgID int) (*tg.Message, error) {
	raw := e.clientMgr.RawClient()
	if raw == nil {
		return nil, errors.New("cliente no conectado")
	}

	var messages []tg.MessageClass
	if chatID < 0 {
		isChannel := false
		channelID := -chatID
		s := fmt.Sprintf("%d", chatID)
		if strings.HasPrefix(s, "-100") && len(s) > 4 {
			isChannel = true
			if parsed, err := strconvParse(s[4:]); err == nil {
				channelID = parsed
			}
		}

		if isChannel {
			accessHash, found := e.clientMgr.GetChannelAccessHash(channelID)
			if !found {
				log.Printf("[DOWNLOAD FETCH] Canal %d sin accessHash en caché. Consultando dialogs...", channelID)
				_ = e.clientMgr.FetchDialogs(ctx)
				accessHash, found = e.clientMgr.GetChannelAccessHash(channelID)
			}
			if !found {
				log.Printf("[DOWNLOAD FETCH] Canal %d sigue sin accessHash. Consultando ChannelsGetChannels...", channelID)
				if chatsRes, err := raw.ChannelsGetChannels(ctx, []tg.InputChannelClass{
					&tg.InputChannel{ChannelID: channelID, AccessHash: 0},
				}); err == nil {
					if mc, ok := chatsRes.(*tg.MessagesChats); ok {
						for _, c := range mc.Chats {
							if ch, ok := c.(*tg.Channel); ok && ch.ID == channelID {
								accessHash = ch.AccessHash
								found = true
								e.clientMgr.SetChannelAccessHash(channelID, accessHash)
								log.Printf("[DOWNLOAD FETCH] Canal %d resuelto exitosamente: AccessHash=%d", channelID, accessHash)
								break
							}
						}
					}
				}
			}
			log.Printf("[DOWNLOAD FETCH] Consultando ChannelsGetMessages (Canal: %d, AccessHash: %d, MsgID: %d, Found: %v)...", channelID, accessHash, msgID, found)

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
				log.Printf("[DOWNLOAD FETCH ERROR] ChannelsGetMessages falló para canal %d mensaje %d: %v", channelID, msgID, err)
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
			log.Printf("[DOWNLOAD FETCH] Consultando chat básico %d para mensaje %d...", chatID, msgID)
			// Chat grupal básico
			res, err := raw.MessagesGetMessages(ctx, []tg.InputMessageClass{
				&tg.InputMessageID{ID: msgID},
			})
			if err != nil {
				log.Printf("[DOWNLOAD FETCH ERROR] MessagesGetMessages falló para chat %d mensaje %d: %v", chatID, msgID, err)
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
		log.Printf("[DOWNLOAD FETCH] Consultando chat privado/usuario %d para mensaje %d...", chatID, msgID)
		// Usuario / Chat privado (chatID > 0)
		res, err := raw.MessagesGetMessages(ctx, []tg.InputMessageClass{
			&tg.InputMessageID{ID: msgID},
		})
		if err != nil {
			log.Printf("[DOWNLOAD FETCH ERROR] MessagesGetMessages falló para usuario %d mensaje %d: %v", chatID, msgID, err)
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
		return nil, errors.New("mensaje no encontrado en Telegram")
	}

	for _, m := range messages {
		if realMsg, ok := m.(*tg.Message); ok {
			return realMsg, nil
		}
	}

	return nil, errors.New("el mensaje no contiene datos válidos o fue eliminado en Telegram")
}

func strconvParse(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
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
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
