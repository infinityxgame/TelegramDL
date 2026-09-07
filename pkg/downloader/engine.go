package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
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

var errDownloadAlreadyExists = errors.New("el archivo ya existe")

type Engine struct {
	clientMgr    *telegram.ClientManager
	storage      *storage.Storage
	config       config.Config
	reservations *PathReservations

	mu                 sync.RWMutex
	downloads          map[string]*storage.DownloadItem
	cancelFuncs        map[string]context.CancelFunc
	pauseStates        map[string]bool
	activeCond         *sync.Cond
	runningJobs        int
	metadataSem        chan struct{}
	stateListeners     []DownloadStateListener
	startTimes         map[string]time.Time
	lastBroadcastTimes map[string]time.Time
	lastSaveTimes      map[string]time.Time
	itemSpeeds         map[string]float64
	seenChunks         map[string]map[int64]struct{}
	lastProgressBytes  map[string]int64
	lastProgressTimes  map[string]time.Time
	persistCh          chan storage.DownloadItem
	pendingChunks      map[string][]int64
	forceDuplicate     map[string]bool
	jobsWG             sync.WaitGroup
	persistWG          sync.WaitGroup
	stopping           bool
	cancelledForLimit  map[string]bool
	jobsInFlight       map[string]bool
	messageCache       map[string]*tg.Message
	chatMsgMap         map[string]string
	queuedIDs          map[string]bool

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
		startTimes:         make(map[string]time.Time),
		lastBroadcastTimes: make(map[string]time.Time),
		lastSaveTimes:      make(map[string]time.Time),
		itemSpeeds:         make(map[string]float64),
		seenChunks:         make(map[string]map[int64]struct{}),
		lastProgressBytes:  make(map[string]int64),
		lastProgressTimes:  make(map[string]time.Time),
		persistCh:          make(chan storage.DownloadItem, 256),
		pendingChunks:      make(map[string][]int64),
		forceDuplicate:     make(map[string]bool),
		cancelledForLimit:  make(map[string]bool),
		jobsInFlight:       make(map[string]bool),
		messageCache:       make(map[string]*tg.Message),
		chatMsgMap:         make(map[string]string),
		queuedIDs:          make(map[string]bool),
		metadataSem:        make(chan struct{}, 5),
	}
	eng.activeCond = sync.NewCond(&eng.mu)
	if st != nil {
		go eng.persistenceLoop()
	}

	// Cargar descargas previas desde SQLite
	if st != nil {
		if saved, err := st.LoadDownloads(""); err == nil {
			for id, item := range saved {
				// Si quedó en downloading cuando se cerró la app, pasa a queued o paused
				if item.Status == "downloading" {
					item.Status = "queued"
				}
				// Las descargas antiguas podían conservar "mensaje_ID" en la
				// columna file_name aunque file_path ya tuviera el nombre real.
				// Recuperarlo evita que el historial vuelva a mostrar el placeholder.
				if item.FilePath != "" && (item.FileName == "" || strings.HasPrefix(strings.ToLower(item.FileName), "mensaje_")) {
					if recoveredName := filepath.Base(item.FilePath); recoveredName != "." && recoveredName != string(filepath.Separator) {
						item.FileName = recoveredName
						_ = st.SaveDownload(item)
					}
				}
				copyItem := item
				eng.downloads[id] = &copyItem
				key := fmt.Sprintf("%d:%d", item.ChatID, item.MessageID)
				eng.chatMsgMap[key] = id
				if copyItem.Status == "queued" {
					eng.queuedIDs[id] = true
					eng.launchDownloadJob(id)
				}
			}
		}
	}

	return eng
}

func (e *Engine) launchDownloadJob(itemID string) {
	e.mu.Lock()
	if e.jobsInFlight[itemID] {
		e.mu.Unlock()
		return
	}
	e.jobsInFlight[itemID] = true
	e.mu.Unlock()

	e.jobsWG.Add(1)
	go func() {
		defer e.jobsWG.Done()
		defer func() {
			e.mu.Lock()
			delete(e.jobsInFlight, itemID)
			e.mu.Unlock()
		}()
		e.startDownloadJob(itemID)
	}()
}

// Shutdown conserva las tareas activas como queued y espera a que terminen
// los trabajos y las escrituras pendientes antes de cerrar SQLite.
func (e *Engine) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	e.stopping = true
	e.activeCond.Broadcast()
	for id, cancel := range e.cancelFuncs {
		if item, ok := e.downloads[id]; ok && item.Status == "downloading" {
			item.Status = "queued"
			e.queuedIDs[id] = true
			item.Speed = "0 B/s"
			item.UpdatedAt = float64(time.Now().Unix())
		}
		cancel()
	}
	e.mu.Unlock()

	done := make(chan struct{})
	go func() {
		e.jobsWG.Wait()
		e.persistWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}

	e.mu.RLock()
	items := make([]storage.DownloadItem, 0, len(e.downloads))
	for _, item := range e.downloads {
		if item.Status == "queued" || item.Status == "downloading" {
			items = append(items, *item)
		}
	}
	e.mu.RUnlock()
	for _, item := range items {
		if e.storage != nil {
			if err := e.storage.SaveDownload(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Engine) persistenceLoop() {
	for item := range e.persistCh {
		if e.storage != nil {
			_ = e.storage.SaveDownload(item)
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
		_ = e.storage.AddChunks(itemID, chunks)
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
		func(listener DownloadStateListener) {
			defer func() { _ = recover() }()
			listener(item)
		}(l)
	}
}

func (e *Engine) UpdateConfig(cfg config.Config) {
	e.mu.Lock()
	oldMax := e.config.MaxConcurrentDownloads
	e.config = config.NormalizeConfig(cfg)
	newMax := e.config.MaxConcurrentDownloads
	e.mu.Unlock()

	if oldMax != newMax {
		e.activeCond.Broadcast()
		if oldMax > newMax {
			e.enforceConcurrencyLimit()
		}
	}
}

func (e *Engine) enforceConcurrencyLimit() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.runningJobs <= e.config.MaxConcurrentDownloads {
		return
	}

	diff := e.runningJobs - e.config.MaxConcurrentDownloads
	log.Printf("[ENGINE] Reduciendo concurrencia: deteniendo %d tareas en exceso", diff)

	type jobInfo struct {
		id        string
		startTime time.Time
	}
	running := make([]jobInfo, 0, len(e.cancelFuncs))
	for id := range e.cancelFuncs {
		running = append(running, jobInfo{id: id, startTime: e.startTimes[id]})
	}

	// Ordenar por tiempo de inicio descendente (las más nuevas primero)
	sort.Slice(running, func(i, j int) bool {
		return running[i].startTime.After(running[j].startTime)
	})

	for i := 0; i < diff && i < len(running); i++ {
		id := running[i].id
		if cancel, ok := e.cancelFuncs[id]; ok {
			log.Printf("[ENGINE] Pausando tarea en exceso %s para respetar nuevo límite", id)
			e.cancelledForLimit[id] = true
			cancel()
		}
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
		// Para activos/en cola (prioridad >= 2), orden de creación ascendente
		if sI >= 2 {
			// Para rangos (mismo JobID), priorizar MessageID para asegurar el orden
			if res[i].JobID != "" && res[i].JobID == res[j].JobID {
				if res[i].MessageID != res[j].MessageID {
					return res[i].MessageID < res[j].MessageID
				}
			}
			if res[i].CreatedAt != res[j].CreatedAt {
				return res[i].CreatedAt < res[j].CreatedAt
			}
			return res[i].MessageID < res[j].MessageID
		} else {
			// Para historial, lo más reciente primero
			if res[i].UpdatedAt != res[j].UpdatedAt {
				return res[i].UpdatedAt > res[j].UpdatedAt
			}
		}
		return res[i].ID < res[j].ID
	})

	return res
}

func statusPriority(status string) int {
	switch status {
	case "downloading", "paused", "queued", "pending":
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

func (e *Engine) discardDownload(id string) {
	e.mu.Lock()
	if item, ok := e.downloads[id]; ok {
		key := fmt.Sprintf("%d:%d", item.ChatID, item.MessageID)
		delete(e.chatMsgMap, key)
	}
	delete(e.downloads, id)
	delete(e.forceDuplicate, id)
	delete(e.queuedIDs, id)
	delete(e.messageCache, id)
	e.mu.Unlock()

	if e.storage != nil {
		_ = e.storage.DeleteDownload(id)
		_ = e.storage.DeleteChunks(id)
	}
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
	delete(e.messageCache, id)

	item.Status = "cancelled"
	delete(e.queuedIDs, id)
	item.Speed = "0 B/s"
	item.UpdatedAt = float64(time.Now().Unix())
	cp := *item
	e.mu.Unlock()
	e.activeCond.Broadcast() // Despertar tareas en espera para que vean el cambio
	e.persistSeenChunks(id)
	if e.storage != nil {
		_ = e.storage.SaveDownload(cp)
	}
	e.notifyState(cp)
	return nil
}

func (e *Engine) CancelAll() {
	e.mu.Lock()
	ids := make([]string, 0, len(e.downloads))
	for id := range e.downloads {
		ids = append(ids, id)
	}
	e.mu.Unlock()

	for _, id := range ids {
		_ = e.CancelDownload(id)
	}
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
	delete(e.messageCache, id)

	item.Status = "paused"
	delete(e.queuedIDs, id)
	item.Speed = "0 B/s"
	item.UpdatedAt = float64(time.Now().Unix())
	cp := *item
	e.mu.Unlock()
	e.activeCond.Broadcast() // Despertar tareas en espera para que vean el cambio
	e.persistSeenChunks(id)
	if e.storage != nil {
		_ = e.storage.SaveDownload(cp)
	}
	e.notifyState(cp)

	return nil
}

func (e *Engine) PauseAll() {
	e.mu.Lock()
	ids := make([]string, 0, len(e.downloads))
	for id, item := range e.downloads {
		if item.Status == "downloading" || item.Status == "queued" {
			ids = append(ids, id)
		}
	}
	e.mu.Unlock()

	for _, id := range ids {
		_ = e.PauseDownload(id)
	}
}

func (e *Engine) ResumeDownload(ctx context.Context, id string) error {
	e.mu.Lock()
	item, ok := e.downloads[id]
	if !ok {
		e.mu.Unlock()
		return errors.New("descarga no encontrada")
	}

	// Permitimos reanudar casi cualquier estado no activo, incluyendo 'completed' para forzar re-descarga
	allowed := map[string]bool{
		"paused":    true,
		"failed":    true,
		"cancelled": true,
		"duplicate": true,
		"queued":    true,
		"completed": true,
	}
	if !allowed[item.Status] {
		e.mu.Unlock()
		return errors.New("la descarga no se puede reanudar en su estado actual")
	}

	delete(e.pauseStates, id)
	if item.Status == "duplicate" || item.Status == "completed" {
		e.forceDuplicate[id] = true
	} else {
		delete(e.forceDuplicate, id)
	}
	item.Status = "queued"
	e.queuedIDs[id] = true
	item.Speed = "0 B/s"
	item.UpdatedAt = float64(time.Now().Unix())
	cp := *item
	e.mu.Unlock()
	if e.storage != nil {
		_ = e.storage.SaveDownload(cp)
	}
	e.notifyState(cp)

	// Siempre intentamos lanzar el job, launchDownloadJob ya evita duplicados internos
	e.launchDownloadJob(id)
	return nil
}

func (e *Engine) ResumeAll(ctx context.Context) {
	e.mu.Lock()
	items := make([]*storage.DownloadItem, 0, len(e.downloads))
	for _, item := range e.downloads {
		// No reanudamos automáticamente lo que fue cancelado
		if item.Status == "paused" || item.Status == "failed" {
			items = append(items, item)
		}
	}

	// Ordenar por CreatedAt y MessageID para respetar el orden original al reanudar
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].CreatedAt != items[j].CreatedAt {
			return items[i].CreatedAt < items[j].CreatedAt
		}
		if items[i].MessageID != items[j].MessageID {
			return items[i].MessageID < items[j].MessageID
		}
		return items[i].ID < items[j].ID
	})
	e.mu.Unlock()

	for _, item := range items {
		_ = e.ResumeDownload(ctx, item.ID)
	}
}

func (e *Engine) QueueItem(item storage.DownloadItem) string {
	e.mu.Lock()
	if item.ID == "" {
		item.ID = uuid.New().String()
	}

	// Evitar duplicados por ID único
	if existing, ok := e.downloads[item.ID]; ok {
		e.mu.Unlock()
		return existing.ID
	}

	// Evitar duplicados por ChatID y MessageID usando el mapa optimizado
	key := fmt.Sprintf("%d:%d", item.ChatID, item.MessageID)
	if existingID, ok := e.chatMsgMap[key]; ok {
		if existing, exists := e.downloads[existingID]; exists {
			// Si el item ya existe y no está fallido/cancelado, no duplicar
			if existing.Status != "failed" && existing.Status != "cancelled" {
				e.mu.Unlock()
				return existing.ID
			}
		}
	}

	item.Status = "queued"
	e.queuedIDs[item.ID] = true
	delete(e.forceDuplicate, item.ID)
	// Usar mayor precisión para evitar colisiones en CreatedAt durante bucles rápidos (rangos)
	item.CreatedAt = float64(time.Now().UnixNano()) / 1e9
	item.UpdatedAt = item.CreatedAt

	e.downloads[item.ID] = &item
	e.chatMsgMap[key] = item.ID
	cp := item
	e.mu.Unlock()
	if e.storage != nil {
		_ = e.storage.SaveDownload(item)
	}
	e.notifyState(cp)

	e.launchDownloadJob(item.ID)
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

func (e *Engine) startDownloadJob(itemID string) {
	// Pre-resolver metadatos (nombre, tamaño) antes de esperar el slot de descarga
	go e.resolveItemMetadata(itemID)

	// Adquirir slot de concurrencia respetando el orden de la cola
	log.Printf("[DOWNLOAD] Solicitando slot de concurrencia para item %s...", itemID)
	e.mu.Lock()
	for (e.runningJobs >= e.config.MaxConcurrentDownloads || !e.isNextInQueue(itemID)) && !e.stopping {
		e.activeCond.Wait()
		// Si mientras esperaba fue cancelada, pausada o ya no existe, salir
		if it, exists := e.downloads[itemID]; !exists || (it.Status != "queued" && it.Status != "downloading") || e.pauseStates[itemID] {
			e.mu.Unlock()
			e.activeCond.Broadcast()
			return
		}
	}

	item, ok := e.downloads[itemID]
	if !ok || item.Status == "cancelled" || item.Status == "paused" || e.stopping {
		e.mu.Unlock()
		e.activeCond.Broadcast() // Despertar al siguiente si este decide no iniciar
		return
	}

	e.runningJobs++
	item.Status = "downloading" // Cambiar a downloading inmediatamente bajo lock
	delete(e.queuedIDs, itemID)

	ctx, cancel := context.WithCancel(context.Background())
	e.cancelFuncs[itemID] = cancel
	e.startTimes[itemID] = time.Now()
	e.lastBroadcastTimes[itemID] = time.Now()
	e.seenChunks[itemID] = make(map[int64]struct{})
	delete(e.lastProgressBytes, itemID)
	delete(e.lastProgressTimes, itemID)
	item.Speed = "0 B/s"
	e.itemSpeeds[itemID] = 0
	log.Printf("[DOWNLOAD] Iniciando descarga activa para item %s (ChatID: %d, MsgID: %d)", itemID, item.ChatID, item.MessageID)
	cp := *item
	e.mu.Unlock()
	e.notifyState(cp)

	defer func() {
		e.mu.Lock()
		e.runningJobs--
		e.activeCond.Broadcast() // Broadcast es más seguro que Signal para asegurar que la cola siga

		delete(e.cancelFuncs, itemID)
		delete(e.startTimes, itemID)
		delete(e.lastBroadcastTimes, itemID)
		delete(e.lastSaveTimes, itemID)
		delete(e.itemSpeeds, itemID)
		delete(e.seenChunks, itemID)
		delete(e.lastProgressBytes, itemID)
		delete(e.lastProgressTimes, itemID)
		delete(e.forceDuplicate, itemID)
		delete(e.messageCache, itemID) // Limpiar cache de mensaje

		// Si el job termina y el estado sigue siendo 'downloading', significa
		// que hubo un error no controlado o pánico. Lo marcamos como fallido.
		if it, ok := e.downloads[itemID]; ok && it.Status == "downloading" {
			it.Status = "failed"
			it.Error = "descarga interrumpida inesperadamente"
		}

		isLimitCancel := e.cancelledForLimit[itemID]
		delete(e.cancelledForLimit, itemID)
		e.mu.Unlock()

		if isLimitCancel {
			e.launchDownloadJob(itemID)
		}
	}()

	err := e.executeDownloadWithRetry(ctx, itemID)
	log.Printf("[DOWNLOAD] Tarea %s finalizó con resultado: err=%v", itemID, err)
	if err != nil && strings.Contains(err.Error(), "mensaje no encontrado en Telegram") {
		log.Printf("[DOWNLOAD] Omitiendo item %s porque el mensaje %d no existe", itemID, item.MessageID)
		e.discardDownload(itemID)
		return
	}
	e.mu.Lock()

	curItem, ok := e.downloads[itemID]
	if !ok {
		e.mu.Unlock()
		return
	}

	if errors.Is(err, context.Canceled) {
		// Si el estado ya es 'queued', significa que fue reanudado mientras se cerraba
		if curItem.Status == "queued" && !e.pauseStates[itemID] {
			e.mu.Unlock()
			e.launchDownloadJob(itemID)
			return
		}

		if e.stopping || e.cancelledForLimit[itemID] {
			curItem.Status = "queued"
			e.queuedIDs[itemID] = true
			delete(e.pauseStates, itemID)
		} else if e.pauseStates[itemID] {
			curItem.Status = "paused"
		} else {
			curItem.Status = "cancelled"
		}
		curItem.Error = "" // Limpiar error si fue cancelado/pausado
	} else if err != nil {
		if errors.Is(err, errDownloadAlreadyExists) {
			curItem.Status = "duplicate"
			curItem.Error = ""
			curItem.Progress = 100.0
			curItem.Speed = "0 B/s"
			curItem.UpdatedAt = float64(time.Now().Unix())
			cp = *curItem
			e.mu.Unlock()
			if e.storage != nil {
				_ = e.storage.SaveDownload(cp)
			}
			e.notifyState(cp)
			return
		}
		curItem.Status = "failed"
		curItem.Error = err.Error()
		curItem.Speed = "0 B/s"
	} else {
		curItem.Status = "completed"
		curItem.Error = ""
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
		_ = e.storage.SaveDownload(cp)
	}
	e.notifyState(cp)
}

func (e *Engine) executeDownloadWithRetry(ctx context.Context, itemID string) error {
	const maxAttempts = 6
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
			_ = e.storage.SaveDownload(cp)
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

func (e *Engine) isNextInQueue(itemID string) bool {
	item, ok := e.downloads[itemID]
	if !ok || (item.Status != "queued" && item.Status != "downloading") || e.pauseStates[itemID] {
		return false
	}

	// Si hay hueco para nosotros según el límite, verificamos si hay alguien
	// antes que nosotros que también esté en "queued" y NO esté pausado.
	if e.runningJobs < e.config.MaxConcurrentDownloads {
		// Necesitamos saber cuántos huecos libres quedan
		slotsAvailable := e.config.MaxConcurrentDownloads - e.runningJobs

		// Buscamos cuántos elementos "queued" deberían ir antes que nosotros
		betterCandidates := 0
		for id := range e.queuedIDs {
			if id == itemID {
				continue
			}
			other, ok := e.downloads[id]
			if !ok || e.pauseStates[id] {
				continue
			}

			if e.shouldGoBefore(other, item) {
				betterCandidates++
				if betterCandidates >= slotsAvailable {
					return false
				}
			}
		}

		return true
	}

	return false
}

func (e *Engine) shouldGoBefore(a, b *storage.DownloadItem) bool {
	// 1. Mismo JobID (rango): priorizar MessageID
	if a.JobID != "" && a.JobID == b.JobID {
		if a.MessageID != b.MessageID {
			return a.MessageID < b.MessageID
		}
	}
	// 2. Tiempo de creación
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt < b.CreatedAt
	}
	// 3. Tie-breaker final: MessageID
	return a.MessageID < b.MessageID
}

