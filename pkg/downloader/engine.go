package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
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

	mu             sync.RWMutex
	downloads      map[string]*storage.DownloadItem
	cancelFuncs    map[string]context.CancelFunc
	pauseStates    map[string]bool
	activeSem      chan struct{}
	stateListeners []DownloadStateListener

	// Throttling
	throttleMu   sync.Mutex
	bytesSince   int64
	throttleTime time.Time
}

func NewEngine(cm *telegram.ClientManager, st *storage.Storage, cfg config.Config) *Engine {
	cfg = config.NormalizeConfig(cfg)
	eng := &Engine{
		clientMgr:    cm,
		storage:      st,
		config:       cfg,
		reservations: NewPathReservations(),
		downloads:    make(map[string]*storage.DownloadItem),
		cancelFuncs:  make(map[string]context.CancelFunc),
		pauseStates:  make(map[string]bool),
		activeSem:    make(chan struct{}, cfg.MaxConcurrentDownloads),
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
	return res
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

func (e *Engine) ClearHistory() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for id, item := range e.downloads {
		if item.Status == "completed" || item.Status == "failed" || item.Status == "cancelled" || item.Status == "skipped" {
			delete(e.downloads, id)
		}
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

	item.CurrentBytes += bytesJustDownloaded
	if totalBytes > 0 {
		item.TotalBytes = totalBytes
		item.Progress = math.Min(100.0, float64(item.CurrentBytes)/float64(totalBytes)*100.0)
		item.TotalStr = config.FormatBytes(float64(totalBytes))
	}
	item.CurrentStr = config.FormatBytes(float64(item.CurrentBytes))
	item.UpdatedAt = float64(time.Now().Unix())

	cp := *item
	e.mu.Unlock()

	_ = e.storage.SaveDownload(cp)
	e.notifyState(cp)
}

func (e *Engine) startDownloadJob(itemID string) {
	// Adquirir slot de concurrencia
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
	item.Status = "downloading"
	e.notifyState(*item)
	e.mu.Unlock()

	defer func() {
		e.mu.Lock()
		delete(e.cancelFuncs, itemID)
		e.mu.Unlock()
	}()

	err := e.executeDownload(ctx, itemID)
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
	_ = e.storage.SaveDownload(*curItem)
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
		return errors.New("cliente de Telegram no listo")
	}

	_ = os.MkdirAll(downloadFolder, 0755)

	// Resolver mensaje y extraer Multimedia
	msg, err := e.fetchMessage(ctx, item.ChatID, int(item.MessageID))
	if err != nil {
		return fmt.Errorf("error al obtener mensaje: %w", err)
	}

	mediaInfo := ExtractMediaInfo(msg)
	if mediaInfo == nil {
		return errors.New("el mensaje no contiene multimedia descargable")
	}

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
	e.mu.Unlock()

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
				_ = e.clientMgr.FetchDialogs(ctx)
				accessHash, _ = e.clientMgr.GetChannelAccessHash(channelID)
			}

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
			// Chat grupal básico
			res, err := raw.MessagesGetMessages(ctx, []tg.InputMessageClass{
				&tg.InputMessageID{ID: msgID},
			})
			if err != nil {
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
		// Usuario / Chat privado (chatID > 0)
		res, err := raw.MessagesGetMessages(ctx, []tg.InputMessageClass{
			&tg.InputMessageID{ID: msgID},
		})
		if err != nil {
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
