package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	tdDownloader "github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"

	"tgdown/pkg/config"
	"tgdown/pkg/storage"
	"tgdown/pkg/telegram"
)

type DownloadStateListener func(item storage.DownloadItem)

type Engine struct {
	clientMgr    *telegram.ClientManager
	storage      *storage.Storage
	config       config.Config
	reservations *PathReservations

	mu                 sync.RWMutex
	downloads          map[string]*storage.DownloadItem
	cancelFuncs        map[string]context.CancelFunc
	pauseStates        map[string]bool
	activeSem          chan struct{}
	stateListeners     []DownloadStateListener
	startTimes         map[string]time.Time
	lastBroadcastTimes map[string]time.Time
	lastSaveTimes      map[string]time.Time
	itemSpeeds         map[string]float64
	seenChunks         map[string]map[int64]struct{}
	lastProgressBytes  map[string]int64
	lastProgressTimes  map[string]time.Time
	persistCh          chan storage.DownloadItem

	// Throttling
	throttleMu   sync.Mutex
	bytesSince   int64
	throttleTime time.Time
}

func NewEngine(cm *telegram.ClientManager, st *storage.Storage, cfg config.Config) *Engine {
	cfg = config.NormalizeConfig(cfg)
	eng := &Engine{
		clientMgr:          cm,
		storage:            st,
		config:             cfg,
		reservations:       NewPathReservations(),
		downloads:          make(map[string]*storage.DownloadItem),
		cancelFuncs:        make(map[string]context.CancelFunc),
		pauseStates:        make(map[string]bool),
		activeSem:          make(chan struct{}, cfg.MaxConcurrentDownloads),
		startTimes:         make(map[string]time.Time),
		lastBroadcastTimes: make(map[string]time.Time),
		lastSaveTimes:      make(map[string]time.Time),
		itemSpeeds:         make(map[string]float64),
		seenChunks:         make(map[string]map[int64]struct{}),
		lastProgressBytes:  make(map[string]int64),
		lastProgressTimes:  make(map[string]time.Time),
		persistCh:          make(chan storage.DownloadItem, 256),
	}
	if st != nil {
		go eng.persistenceLoop()
	}

	// Cargar descargas previas desde SQLite
	if saved, err := st.LoadDownloads(""); err == nil {
		for id, item := range saved {
			// Si quedó en downloading cuando se cerró la app, pasa a queued o paused
			if item.Status == "downloading" {
				item.Status = "queued"
			}
			copyItem := item
			eng.downloads[id] = &copyItem
			if copyItem.Status == "queued" {
				go eng.startDownloadJob(id)
			}
		}
	}

	return eng
}

func (e *Engine) persistenceLoop() {
	for item := range e.persistCh {
		if e.storage != nil {
			_ = e.storage.SaveDownload(item)
		}
	}
}

func (e *Engine) enqueuePersist(item storage.DownloadItem) {
	if e.storage == nil {
		return
	}
	select {
	case e.persistCh <- item:
	default:
		// El estado actual permanece en memoria y el siguiente tick lo
		// volverá a persistir; nunca frenamos una escritura de Telegram.
	}
}

func (e *Engine) persistSeenChunks(itemID string) {
	if e.storage == nil {
		return
	}
	e.mu.RLock()
	chunks := make([]int64, 0, len(e.seenChunks[itemID]))
	for index := range e.seenChunks[itemID] {
		chunks = append(chunks, index)
	}
	e.mu.RUnlock()
	for _, index := range chunks {
		_ = e.storage.AddChunk(itemID, int(index))
	}
}

func (e *Engine) OnStateChange(listener DownloadStateListener) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stateListeners = append(e.stateListeners, listener)
}

func (e *Engine) notifyState(item storage.DownloadItem) {
	e.mu.RLock()
	listeners := append([]DownloadStateListener(nil), e.stateListeners...)
	e.mu.RUnlock()

	for _, l := range listeners {
		// La persistencia y la UI no deben detener el camino de descarga.
		go func(listener DownloadStateListener) {
			defer func() { _ = recover() }()
			listener(item)
		}(l)
	}
}

func (e *Engine) UpdateConfig(cfg config.Config) {
	e.mu.Lock()
	defer e.mu.Unlock()

	oldMax := e.config.MaxConcurrentDownloads
	e.config = config.NormalizeConfig(cfg)

	if oldMax != e.config.MaxConcurrentDownloads {
		e.activeSem = make(chan struct{}, e.config.MaxConcurrentDownloads)
	}
}

func (e *Engine) GetDownloads() []storage.DownloadItem {
	e.mu.RLock()
	defer e.mu.RUnlock()

	res := make([]storage.DownloadItem, 0, len(e.downloads))
	for _, item := range e.downloads {
		res = append(res, *item)
	}

	sort.Slice(res, func(i, j int) bool {
		sI := statusPriority(res[i].Status)
		sJ := statusPriority(res[j].Status)
		if sI != sJ {
			return sI > sJ
		}
		if res[i].Progress != res[j].Progress {
			return res[i].Progress > res[j].Progress
		}
		return res[i].CreatedAt > res[j].CreatedAt
	})

	return res
}

func statusPriority(status string) int {
	switch status {
	case "downloading":
		return 4
	case "paused":
		return 3
	case "queued", "pending":
		return 2
	default:
		return 1
	}
}

func (e *Engine) GetTotalSpeedBytes() int64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var total float64
	for id, speed := range e.itemSpeeds {
		if item, ok := e.downloads[id]; ok && item.Status == "downloading" {
			total += speed
		}
	}
	return int64(total)
}

func (e *Engine) GetItem(id string) (*storage.DownloadItem, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	item, ok := e.downloads[id]
	if !ok {
		return nil, false
	}
	cp := *item
	return &cp, true
}

func (e *Engine) ClearHistory() (int64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	for id, item := range e.downloads {
		if item.Status == "completed" || item.Status == "failed" || item.Status == "cancelled" || item.Status == "skipped" {
			delete(e.downloads, id)
		}
	}

	if e.storage == nil {
		return 0, nil
	}
	return e.storage.ClearFinishedDownloads()
}

func (e *Engine) DeleteDownload(id string, deleteFile bool) error {
	e.mu.Lock()
	item, ok := e.downloads[id]
	if !ok {
		e.mu.Unlock()
		if e.storage != nil {
			_ = e.storage.DeleteDownload(id)
			_ = e.storage.DeleteChunks(id)
		}
		return nil
	}

	if cancel, exists := e.cancelFuncs[id]; exists {
		cancel()
		delete(e.cancelFuncs, id)
	}

	filePath := item.FilePath
	delete(e.downloads, id)
	e.mu.Unlock()

	if e.storage != nil {
		_ = e.storage.DeleteDownload(id)
		_ = e.storage.DeleteChunks(id)
	}

	if deleteFile && filePath != "" {
		_ = os.Remove(filePath)
		_ = os.Remove(filePath + ".temp")
	}

	return nil
}

func (e *Engine) CancelDownload(id string) error {
	e.mu.Lock()
	item, ok := e.downloads[id]
	if !ok {
		e.mu.Unlock()
		return errors.New("descarga no encontrada")
	}

	if cancel, exists := e.cancelFuncs[id]; exists {
		cancel()
		delete(e.cancelFuncs, id)
	}

	item.Status = "cancelled"
	item.Speed = "0 B/s"
	item.UpdatedAt = float64(time.Now().Unix())
	cp := *item
	e.mu.Unlock()
	e.persistSeenChunks(id)
	if e.storage != nil {
		_ = e.storage.SaveDownload(cp)
	}
	e.notifyState(cp)
	return nil
}

func (e *Engine) PauseDownload(id string) error {
	e.mu.Lock()
	item, ok := e.downloads[id]
	if !ok {
		e.mu.Unlock()
		return errors.New("descarga no encontrada")
	}

	if item.Status != "downloading" && item.Status != "queued" {
		e.mu.Unlock()
		return errors.New("solo se pueden pausar descargas activas o en cola")
	}

	e.pauseStates[id] = true
	if cancel, exists := e.cancelFuncs[id]; exists {
		cancel()
		delete(e.cancelFuncs, id)
	}

	item.Status = "paused"
	item.Speed = "0 B/s"
	item.UpdatedAt = float64(time.Now().Unix())
	cp := *item
	e.mu.Unlock()
	e.persistSeenChunks(id)
	if e.storage != nil {
		_ = e.storage.SaveDownload(cp)
	}
	e.notifyState(cp)

	return nil
}

func (e *Engine) ResumeDownload(ctx context.Context, id string) error {
	e.mu.Lock()
	item, ok := e.downloads[id]
	if !ok {
		e.mu.Unlock()
		return errors.New("descarga no encontrada")
	}

	if item.Status != "paused" && item.Status != "failed" && item.Status != "cancelled" {
		e.mu.Unlock()
		return errors.New("la descarga no está pausada ni cancelada")
	}

	delete(e.pauseStates, id)
	item.Status = "queued"
	item.Speed = "0 B/s"
	item.UpdatedAt = float64(time.Now().Unix())
	cp := *item
	e.mu.Unlock()
	if e.storage != nil {
		_ = e.storage.SaveDownload(cp)
	}
	e.notifyState(cp)

	go e.startDownloadJob(id)
	return nil
}

func (e *Engine) QueueItem(item storage.DownloadItem) string {
	e.mu.Lock()
	if item.ID == "" {
		item.ID = uuid.New().String()
	}
	item.Status = "queued"
	item.CreatedAt = float64(time.Now().Unix())
	item.UpdatedAt = item.CreatedAt

	e.downloads[item.ID] = &item
	cp := item
	e.mu.Unlock()
	if e.storage != nil {
		_ = e.storage.SaveDownload(item)
	}
	e.notifyState(cp)

	go e.startDownloadJob(item.ID)
	return item.ID
}

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
	if n > 0 {
		pw.engine.onProgress(pw.itemID, int64(n), pw.total, off)
		pw.engine.throttle(int64(n))
	}
	return n, err
}

func (e *Engine) onProgress(itemID string, bytesWritten int64, totalBytes int64, offset int64) {
	chunkAdded := false
	e.mu.Lock()
	item, ok := e.downloads[itemID]
	if !ok || item.Status != "downloading" {
		e.mu.Unlock()
		return
	}

	now := time.Now()
	chunkIndex := offset / downloadPartSize
	chunks := e.seenChunks[itemID]
	if chunks == nil {
		chunks = make(map[int64]struct{})
		e.seenChunks[itemID] = chunks
	}
	newBytes := bytesWritten
	if _, seen := chunks[chunkIndex]; seen {
		newBytes = 0
	} else {
		chunks[chunkIndex] = struct{}{}
		chunkAdded = true
	}
	item.CurrentBytes += newBytes
	if totalBytes > 0 {
		item.TotalBytes = totalBytes
		// El 100% se reserva para cuando Parallel() termina sin errores y el
		// archivo temporal ya fue finalizado correctamente.
		item.Progress = math.Min(99.9, float64(item.CurrentBytes)/float64(totalBytes)*100.0)
		item.TotalStr = config.FormatBytes(float64(totalBytes))
	}
	item.CurrentStr = config.FormatBytes(float64(item.CurrentBytes))
	item.UpdatedAt = float64(now.Unix())

	lastTime := e.lastProgressTimes[itemID]
	lastBytes := e.lastProgressBytes[itemID]
	if !lastTime.IsZero() && newBytes > 0 {
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
	if newBytes > 0 {
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
	if chunkAdded && e.storage != nil {
		go func() { _ = e.storage.AddChunk(itemID, int(chunkIndex)) }()
	}

	if shouldSave && e.storage != nil {
		e.enqueuePersist(cp)
	}

	if shouldBroadcast {
		e.notifyState(cp)
	}
}

func (e *Engine) startDownloadJob(itemID string) {
	// Adquirir slot de concurrencia
	log.Printf("[DOWNLOAD] Solicitando slot de concurrencia para item %s...", itemID)
	e.activeSem <- struct{}{}
	defer func() { <-e.activeSem }()

	e.mu.Lock()
	item, ok := e.downloads[itemID]
	if !ok || item.Status == "cancelled" || item.Status == "paused" {
		e.mu.Unlock()
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	e.cancelFuncs[itemID] = cancel
	e.startTimes[itemID] = time.Now()
	e.lastBroadcastTimes[itemID] = time.Now()
	e.seenChunks[itemID] = make(map[int64]struct{})
	delete(e.lastProgressBytes, itemID)
	delete(e.lastProgressTimes, itemID)
	item.Status = "downloading"
	item.Speed = "0 B/s"
	e.itemSpeeds[itemID] = 0
	log.Printf("[DOWNLOAD] Iniciando descarga activa para item %s (ChatID: %d, MsgID: %d)", itemID, item.ChatID, item.MessageID)
	cp := *item
	e.mu.Unlock()
	e.notifyState(cp)

	defer func() {
		e.mu.Lock()
		delete(e.cancelFuncs, itemID)
		delete(e.startTimes, itemID)
		delete(e.lastBroadcastTimes, itemID)
		delete(e.lastSaveTimes, itemID)
		delete(e.itemSpeeds, itemID)
		delete(e.seenChunks, itemID)
		delete(e.lastProgressBytes, itemID)
		delete(e.lastProgressTimes, itemID)
		e.mu.Unlock()
	}()

	err := e.executeDownload(ctx, itemID)
	log.Printf("[DOWNLOAD] Tarea %s finalizó con resultado: err=%v", itemID, err)
	e.mu.Lock()

	curItem, ok := e.downloads[itemID]
	if !ok {
		e.mu.Unlock()
		return
	}

	if errors.Is(err, context.Canceled) {
		if e.pauseStates[itemID] {
			curItem.Status = "paused"
		} else {
			curItem.Status = "cancelled"
		}
	} else if err != nil {
		curItem.Status = "failed"
		curItem.Speed = "0 B/s"
	} else {
		curItem.Status = "completed"
		curItem.Progress = 100.0
		curItem.Speed = "0 B/s"
	}

	curItem.UpdatedAt = float64(time.Now().Unix())
	cp = *curItem
	e.mu.Unlock()
	if e.storage != nil {
		_ = e.storage.SaveDownload(cp)
	}
	e.notifyState(cp)
}

func (e *Engine) executeDownload(ctx context.Context, itemID string) error {
	e.mu.RLock()
	item := e.downloads[itemID]
	downloadFolder := e.config.DownloadFolder
	parallelChunks := e.config.ParallelChunks
	chunkWorkers := e.config.ChunkWorkers
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

	log.Printf("[DOWNLOAD] Obteniendo mensaje %d del chat %d en Telegram...", item.MessageID, item.ChatID)
	// Resolver mensaje y extraer Multimedia
	msg, err := e.fetchMessage(ctx, item.ChatID, int(item.MessageID))
	if err != nil {
		log.Printf("[DOWNLOAD ERROR] Error al obtener mensaje %d: %v", item.MessageID, err)
		return fmt.Errorf("error al obtener mensaje: %w", err)
	}

	mediaInfo := ExtractMediaInfo(msg)
	if mediaInfo == nil {
		log.Printf("[DOWNLOAD ERROR] El mensaje %d no contiene multimedia descargable", item.MessageID)
		return errors.New("el mensaje no contiene multimedia descargable")
	}

	log.Printf("[DOWNLOAD] Multimedia extraída: %s (%s, %d bytes)", mediaInfo.FileName, mediaInfo.Kind, mediaInfo.FileSize)

	finalPath, finalName, alreadyExists := e.reservations.ReservePath(
		downloadFolder, mediaInfo.FileName, item.MessageID, mediaInfo.FileSize,
	)
	defer e.reservations.ReleasePath(finalPath)

	if alreadyExists {
		e.mu.Lock()
		item.FilePath = finalPath
		item.FileName = finalName
		item.Status = "skipped"
		item.Progress = 100.0
		e.mu.Unlock()
		return nil
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
		_ = e.storage.SaveDownload(cp)
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
			_ = e.storage.DeleteChunks(itemID)
		}
	}
	if info, statErr := tempFile.Stat(); statErr != nil || mediaInfo.FileSize <= 0 || info.Size() != mediaInfo.FileSize {
		// Un temporal incompleto no puede reutilizarse de forma segura: sus
		// offsets persistidos podrían pertenecer a otra descarga.
		resumeChunks = make(map[int64]struct{})
		if e.storage != nil {
			_ = e.storage.DeleteChunks(itemID)
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
	if err != nil {
		return err
	}

	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("error al cerrar archivo temporal: %w", err)
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		// Fallback por si el sistema mantiene abierto el temporal.
		if copyErr := copyFile(tempPath, finalPath); copyErr != nil {
			return fmt.Errorf("error al finalizar archivo: rename: %v; copia: %w", err, copyErr)
		}
		if removeErr := os.Remove(tempPath); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("archivo descargado pero no se pudo limpiar el temporal: %w", removeErr)
		}
	}

	if e.storage != nil {
		if err := e.storage.DeleteChunks(itemID); err != nil {
			return fmt.Errorf("error al limpiar fragmentos: %w", err)
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

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
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
