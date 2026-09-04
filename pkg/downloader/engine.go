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
		l(item)
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
		_ = e.storage.DeleteDownload(id)
		_ = e.storage.DeleteChunks(id)
		return nil
	}

	if cancel, exists := e.cancelFuncs[id]; exists {
		cancel()
		delete(e.cancelFuncs, id)
	}

	filePath := item.FilePath
	delete(e.downloads, id)
	e.mu.Unlock()

	_ = e.storage.DeleteDownload(id)
	_ = e.storage.DeleteChunks(id)

	if deleteFile && filePath != "" {
		_ = os.Remove(filePath)
		_ = os.Remove(filePath + ".temp")
	}

	return nil
}

func (e *Engine) CancelDownload(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	item, ok := e.downloads[id]
	if !ok {
		return errors.New("descarga no encontrada")
	}

	if cancel, exists := e.cancelFuncs[id]; exists {
		cancel()
		delete(e.cancelFuncs, id)
	}

	item.Status = "cancelled"
	item.Speed = "0 B/s"
	item.UpdatedAt = float64(time.Now().Unix())
	_ = e.storage.SaveDownload(*item)
	e.notifyState(*item)
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
	_ = e.storage.SaveDownload(*item)
	e.notifyState(*item)
	e.mu.Unlock()

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
	_ = e.storage.SaveDownload(*item)
	e.notifyState(*item)
	e.mu.Unlock()

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
	_ = e.storage.SaveDownload(item)
	e.notifyState(item)
	e.mu.Unlock()

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
		chunkIndex := int(off / (512 * 1024))
		_ = pw.engine.storage.AddChunk(pw.itemID, chunkIndex)
		pw.engine.onProgress(pw.itemID, int64(n), pw.total)
		pw.engine.throttle(int64(n))
	}
	return n, err
}

func (e *Engine) onProgress(itemID string, bytesJustDownloaded int64, totalBytes int64) {
	e.mu.Lock()
	item, ok := e.downloads[itemID]
	if !ok || item.Status != "downloading" {
		e.mu.Unlock()
		return
	}

	now := time.Now()
	item.CurrentBytes += bytesJustDownloaded
	if totalBytes > 0 {
		item.TotalBytes = totalBytes
		item.Progress = math.Min(100.0, float64(item.CurrentBytes)/float64(totalBytes)*100.0)
		item.TotalStr = config.FormatBytes(float64(totalBytes))
	}
	item.CurrentStr = config.FormatBytes(float64(item.CurrentBytes))
	item.UpdatedAt = float64(now.Unix())

	startTime, hasStart := e.startTimes[itemID]
	if !hasStart {
		startTime = now
		e.startTimes[itemID] = startTime
	}
	elapsed := now.Sub(startTime).Seconds()
	if elapsed > 0.05 {
		speedVal := float64(item.CurrentBytes) / elapsed
		item.Speed = config.FormatBytes(speedVal) + "/s"
		e.itemSpeeds[itemID] = speedVal
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
		_ = e.storage.SaveDownload(cp)
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
	item.Status = "downloading"
	item.Speed = "0 B/s"
	e.itemSpeeds[itemID] = 0
	log.Printf("[DOWNLOAD] Iniciando descarga activa para item %s (ChatID: %d, MsgID: %d)", itemID, item.ChatID, item.MessageID)
	e.notifyState(*item)
	e.mu.Unlock()

	defer func() {
		e.mu.Lock()
		delete(e.cancelFuncs, itemID)
		delete(e.startTimes, itemID)
		delete(e.lastBroadcastTimes, itemID)
		delete(e.lastSaveTimes, itemID)
		delete(e.itemSpeeds, itemID)
		e.mu.Unlock()
	}()

	err := e.executeDownload(ctx, itemID)
	log.Printf("[DOWNLOAD] Tarea %s finalizó con resultado: err=%v", itemID, err)
	e.mu.Lock()
	defer e.mu.Unlock()

	curItem, ok := e.downloads[itemID]
	if !ok {
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
	if e.storage != nil {
		_ = e.storage.SaveDownload(*curItem)
	}
	e.notifyState(*curItem)
}

func (e *Engine) executeDownload(ctx context.Context, itemID string) error {
	e.mu.RLock()
	item := e.downloads[itemID]
	rawClient := e.clientMgr.RawClient()
	downloadFolder := e.config.DownloadFolder
	parallelChunks := e.config.ParallelChunks
	chunkWorkers := e.config.ChunkWorkers
	e.mu.RUnlock()

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

	if mediaInfo.FileSize > 0 {
		_ = tempFile.Truncate(mediaInfo.FileSize)
	}

	threads := 1
	if parallelChunks && chunkWorkers > 1 {
		threads = chunkWorkers
	}

	dl := tdDownloader.NewDownloader().WithPartSize(512 * 1024)
	builder := dl.Download(rawClient, mediaInfo.Location).WithThreads(threads)

	writer := &progressWriterAt{
		file:   tempFile,
		itemID: itemID,
		engine: e,
		total:  mediaInfo.FileSize,
	}

	_, err = builder.Parallel(ctx, writer)
	if err != nil {
		return err
	}

	_ = tempFile.Close()
	if err := os.Rename(tempPath, finalPath); err != nil {
		// Fallback por si hay bloqueo
		_ = copyFile(tempPath, finalPath)
		_ = os.Remove(tempPath)
	}

	_ = e.storage.DeleteChunks(itemID)
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

	msg, ok := messages[0].(*tg.Message)
	if !ok {
		return nil, errors.New("el contenido no es un mensaje regular")
	}

	return msg, nil
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
