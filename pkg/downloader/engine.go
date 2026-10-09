package downloader

// Engine coordina el ciclo de vida completo de las descargas: alta, cancelacion,
// pausa/reanudacion, borrado e historial. La logica de cola/planificacion, el
// calculo de progreso/velocidad, la persistencia en segundo plano y la
// ejecucion de la descarga en si viven en los demas archivos de este paquete
// (queue.go, progress.go, persistence.go, retry.go, download.go).

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/tg"

	"tgdown/pkg/config"
	"tgdown/pkg/i18n"
	"tgdown/pkg/notifier"
	"tgdown/pkg/storage"
	"tgdown/pkg/telegram"
)

type DownloadStateListener func(item storage.DownloadItem)

var errDownloadAlreadyExists = errors.New("el archivo ya existe")

// errMensajeInexistente marca los mensajes que Telegram no va a entregar nunca:
// borrados, IDs que jamás existieron, o entradas que no son un mensaje de
// usuario (avisos de servicio del chat, por ejemplo).
//
// Es lo normal al pedir un rango: en 100-103 el 102 puede estar borrado. Eso no
// es un fallo de descarga y no tiene sentido dejarlo en el historial como
// fallido, porque no hay nada que reintentar. La tarea se descarta y ya.
//
// Antes esto se detectaba comparando el texto del error, y solo acertaba con
// uno de los dos caminos por los que fetchMessage da un mensaje por perdido: el
// otro (el que se da precisamente cuando el mensaje está borrado) se escapaba y
// acababa en "fallido". Con un error centinela y errors.Is ya no depende de
// cómo esté redactado el mensaje.
var errMensajeInexistente = errors.New("mensaje no encontrado en Telegram")

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
	chunkFlushCh       chan string
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

	// Contadores de la sesión en curso: solo cuentan lo que ocurre desde que
	// se arranca la aplicación, así que el «COMPLETADAS en esta sesión» del
	// panel y el resumen del bot son reales. El historial restaurado de
	// SQLite no entra aquí y cada arranque vuelve a empezar de cero.
	sessionCompleted int
	sessionFailed    int
	sessionSkipped   int
	// IDs de las descargas fallidas en esta sesión, para que el resumen del
	// bot detalle solo las de ahora y no las heredadas del historial.
	sessionFailedIDs []string

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
		chunkFlushCh:       make(chan string, 64),
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
		go eng.chunkFlushLoop()
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

// sendNotification envía una notificación mediante el bot de Telegram si está configurado.
func (e *Engine) sendNotification(text string) {
	e.mu.RLock()
	cfg := e.config
	e.mu.RUnlock()

	if !cfg.NotificationBotEnabled || cfg.NotificationBotToken == "" || cfg.NotificationChatID == 0 {
		return
	}

	n := notifier.NewNotifier(cfg.NotificationBotToken, cfg.NotificationChatID, cfg.NotificationTopicID)
	n.SendMessage(context.Background(), text)
}

// SendNotification es el método público para enviar notificaciones desde fuera del Engine.
func (e *Engine) SendNotification(text string) {
	e.sendNotification(text)
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
			if res[i].CreatedAt != res[j].CreatedAt {
				return res[i].CreatedAt < res[j].CreatedAt
			}
			// Para rangos (mismo JobID con la misma fecha), priorizar MessageID
			if res[i].JobID != "" && res[i].JobID == res[j].JobID {
				if res[i].MessageID != res[j].MessageID {
					return res[i].MessageID < res[j].MessageID
				}
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

// SessionStats es la foto de lo que ha pasado en esta sesión, entendida como
// el tiempo que la aplicación lleva arrancada. Vive solo en memoria: el
// historial guardado en SQLite no cuenta, así que cada arranque empieza de
// cero y las cifras que se muestran («COMPLETADAS en esta sesión», el resumen
// del bot) son las de verdad.
type SessionStats struct {
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	// Skipped son las omisiones por archivo ya existente (estado "duplicate").
	Skipped int `json:"skipped"`
	// FailedIDs identifica las fallidas de esta sesión para que el resumen del
	// bot pueda detallarlas; no se expone en el snapshot, se consume interno.
	FailedIDs []string `json:"-"`
}

func (e *Engine) GetSessionStats() SessionStats {
	e.mu.RLock()
	defer e.mu.RUnlock()
	failedIDs := make([]string, len(e.sessionFailedIDs))
	copy(failedIDs, e.sessionFailedIDs)
	return SessionStats{
		Completed: e.sessionCompleted,
		Failed:    e.sessionFailed,
		Skipped:   e.sessionSkipped,
		FailedIDs: failedIDs,
	}
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

// olvidarEstadoDe borra el estado por tarea que el motor guarda en mapas
// aparte. Debe llamarse con e.mu tomado.
//
// No toca cancelFuncs ni jobsInFlight: esos dos los administra el job que esté
// corriendo y borrarlos desde fuera lo dejaría huérfano o permitiría arrancar
// otro en paralelo.
//
// Antes cada sitio que quitaba una descarga borraba solo algunos de estos
// mapas, así que las entradas de los demás se quedaban para siempre.
func (e *Engine) olvidarEstadoDe(id string) {
	delete(e.pauseStates, id)
	delete(e.startTimes, id)
	delete(e.lastBroadcastTimes, id)
	delete(e.lastSaveTimes, id)
	delete(e.itemSpeeds, id)
	delete(e.seenChunks, id)
	delete(e.lastProgressBytes, id)
	delete(e.lastProgressTimes, id)
	delete(e.pendingChunks, id)
	delete(e.forceDuplicate, id)
	delete(e.cancelledForLimit, id)
	delete(e.messageCache, id)
	delete(e.queuedIDs, id)
}

func (e *Engine) ClearHistory() (int64, error) {
	e.mu.Lock()
	temporales := make([]string, 0, len(e.downloads))
	for id, item := range e.downloads {
		if item.Status == "completed" || item.Status == "failed" || item.Status == "cancelled" || item.Status == "skipped" || item.Status == "duplicate" {
			// Los archivos temporales se borran después, fuera del candado.
			if item.FilePath != "" {
				temporales = append(temporales, item.FilePath+".temp")
			}

			key := fmt.Sprintf("%d:%d", item.ChatID, item.MessageID)
			delete(e.chatMsgMap, key)
			delete(e.downloads, id)
			e.olvidarEstadoDe(id)
		}
	}
	e.mu.Unlock()

	// Eliminar archivos temporales de descargas no terminadas o canceladas
	for _, ruta := range temporales {
		_ = os.Remove(ruta)
	}

	if e.storage == nil {
		return 0, nil
	}
	// Fuera del mutex a propósito: ClearFinishedDownloads termina con un VACUUM,
	// que en una base de datos grande tarda segundos. Con el candado del motor
	// tomado, durante todo ese rato se congelaban las descargas en curso, el
	// progreso y el panel entero.
	return e.storage.ClearFinishedDownloads()
}

func (e *Engine) DeleteDownload(id string, deleteFile bool) error {
	e.mu.Lock()
	item, ok := e.downloads[id]
	if !ok {
		e.mu.Unlock()
		if e.storage != nil {
			if err := e.storage.DeleteDownload(id); err != nil {
				log.Printf("[DOWNLOADER] %s", i18n.T("downloader.deleteDownloadError", err))
			}
			if err := e.storage.DeleteChunks(id); err != nil {
				log.Printf("[DOWNLOADER] %s", i18n.T("downloader.deleteChunksError", err))
			}
		}
		return nil
	}

	if cancel, exists := e.cancelFuncs[id]; exists {
		cancel()
		delete(e.cancelFuncs, id)
	}

	filePath := item.FilePath
	// Un duplicado nunca creó el archivo: su file_path apunta al original.
	// Tampoco se borra si otra entrada del historial referencia la misma ruta.
	fileOwned := item.Status != "duplicate" && filePath != ""
	if fileOwned && deleteFile {
		for otherID, other := range e.downloads {
			if otherID != id && other.FilePath != "" && sameFilePath(other.FilePath, filePath) {
				fileOwned = false
				break
			}
		}
	}

	key := fmt.Sprintf("%d:%d", item.ChatID, item.MessageID)
	delete(e.chatMsgMap, key)
	delete(e.downloads, id)
	e.olvidarEstadoDe(id)
	e.mu.Unlock()

	if e.storage != nil {
		if err := e.storage.DeleteDownload(id); err != nil {
			log.Printf("[DOWNLOADER] %s", i18n.T("downloader.deleteDownloadError", err))
		}
		if err := e.storage.DeleteChunks(id); err != nil {
			log.Printf("[DOWNLOADER] %s", i18n.T("downloader.deleteChunksError", err))
		}
	}

	if deleteFile && filePath != "" {
		if fileOwned {
			_ = os.Remove(filePath)
			_ = os.Remove(filePath + ".temp")
		} else {
			log.Printf("[ENGINE] %s", i18n.T("downloader.entryKeptFile", id, filePath))
		}
	}

	return nil
}

// sameFilePath compara rutas de archivo teniendo en cuenta que en Windows el
// sistema de archivos no distingue mayúsculas de minúsculas.
func sameFilePath(a, b string) bool {
	ca, cb := filepath.Clean(a), filepath.Clean(b)
	if ca == cb {
		return true
	}
	return runtime.GOOS == "windows" && strings.EqualFold(ca, cb)
}

// chatLabel devuelve el nombre legible de un chat para los mensajes del
// registro. Si Telegram todavía no ha entregado el título, cae al ID numérico.
func (e *Engine) chatLabel(chatID int64) string {
	if e.clientMgr != nil {
		if name := strings.TrimSpace(e.clientMgr.GetChatName(chatID)); name != "" {
			return name
		}
	}
	return strconv.FormatInt(chatID, 10)
}

func (e *Engine) discardDownload(id string) {
	e.mu.Lock()
	if item, ok := e.downloads[id]; ok {
		key := fmt.Sprintf("%d:%d", item.ChatID, item.MessageID)
		delete(e.chatMsgMap, key)
	}
	delete(e.downloads, id)
	e.olvidarEstadoDe(id)
	e.mu.Unlock()
	// Despertar a la cola: si el job de esta tarea estaba esperando turno, tiene
	// que ver que ya no existe y salir en vez de seguir dormido hasta que otra
	// descarga termine. Con un rango entero de IDs borrados no habría ningún
	// otro aviso que lo despertase.
	e.activeCond.Broadcast()

	if e.storage != nil {
		if err := e.storage.DeleteDownload(id); err != nil {
			log.Printf("[DOWNLOADER] %s", i18n.T("downloader.deleteDownloadError", err))
		}
		if err := e.storage.DeleteChunks(id); err != nil {
			log.Printf("[DOWNLOADER] %s", i18n.T("downloader.deleteChunksError", err))
		}
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
		if err := e.storage.SaveDownload(cp); err != nil {
			log.Printf("[DOWNLOADER] %s", i18n.T("downloader.saveStateError", err))
		}
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
		if err := e.storage.SaveDownload(cp); err != nil {
			log.Printf("[DOWNLOADER] %s", i18n.T("downloader.saveStateError", err))
		}
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
		if err := e.storage.SaveDownload(cp); err != nil {
			log.Printf("[DOWNLOADER] %s", i18n.T("downloader.saveStateError", err))
		}
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
