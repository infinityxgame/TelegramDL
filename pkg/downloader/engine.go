package downloader

// Engine coordina el ciclo de vida completo de las descargas: alta, cancelacion,
// pausa/reanudacion, borrado e historial. La logica de cola/planificacion, el
// calculo de progreso/velocidad, la persistencia en segundo plano y la
// ejecucion de la descarga en si viven en los demas archivos de este paquete
// (queue.go, progress.go, persistence.go, retry.go, download.go).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
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

var errDownloadAlreadyExists = errors.New("file already exists")

// errMissingMessage marca los mensajes que Telegram no va a entregar nunca:
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
var errMissingMessage = errors.New("message not found on Telegram")

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
	persistClosed      bool
	pendingChunks      map[string][]int64
	forceDuplicate     map[string]bool
	jobsWG             sync.WaitGroup
	persistWG          sync.WaitGroup
	loopsWG            sync.WaitGroup
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
		// loopsWG lleva la cuenta de los dos bucles de persistencia para que
		// Shutdown pueda esperar a que terminen tras cerrar sus canales
		// (SEC-36): sin esta espera, las dos goroutines quedaban en `range`
		// para siempre y sus escrituras podían solaparse con el cierre de
		// SQLite que hace el llamador.
		eng.loopsWG.Add(2)
		go func() {
			defer eng.loopsWG.Done()
			eng.persistenceLoop()
		}()
		go func() {
			defer eng.loopsWG.Done()
			eng.chunkFlushLoop()
		}()
	}

	RegisterFloodNotifier(func() {
		eng.notifyState(storage.DownloadItem{})
	})

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
//
// Además (SEC-36) cierra persistCh y chunkFlushCh y espera a que sus dos
// bucles terminen antes de devolver: si no se cierran, persistenceLoop y
// chunkFlushLoop se quedan en `range` para siempre (dos goroutines que jamás
// salen) y un volcado tardío podría ejecutarse contra SQLite ya cerrado por el
// llamador.
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

	// Esperar a los trabajos: son los que encolan progreso, y con ellos vivos
	// no se puede cerrar nada (harían send a un canal cerrado).
	jobsDone := make(chan struct{})
	go func() {
		e.jobsWG.Wait()
		close(jobsDone)
	}()
	select {
	case <-jobsDone:
	case <-ctx.Done():
		// Camino de timeout: quedan trabajos vivos que pueden seguir
		// llamando a enqueuePersist/requestChunkFlush, así que NO se cierran
		// los canales (sería un "send on closed channel" que tumbaría el
		// proceso). Los bucles se quedan abiertos hasta que el proceso termine
		// y aquí queda constancia de que el cierre no llegó a completarse.
		log.Printf("[ENGINE] shutdown timed out waiting for jobs: %v", ctx.Err())
		return ctx.Err()
	}

	// El cierre va bajo el MISMO candado que protegen los envíos de
	// enqueuePersist/requestChunkFlush: quien llegue primero, o envía con el
	// canal aún abierto, o se encuentra persistClosed y no envía. Así ningún
	// send puede llegar a un canal ya cerrado, ni siquiera desde el servidor
	// HTTP, que sigue atendiendo peticiones hasta server.Stop (posterior a
	// este Shutdown). Con el flag levantado en esta misma adquisición del
	// candado, además, todos los persistWG.Add posibles han ocurrido ANTES de
	// la espera siguiente: no puede haber un Add concurrente con el Wait. El
	// flag también hace que un segundo Shutdown no vuelva a cerrar los mismos
	// canales.
	e.mu.Lock()
	if !e.persistClosed {
		e.persistClosed = true
		close(e.persistCh)
		close(e.chunkFlushCh)
	}
	e.mu.Unlock()

	// Al cerrar, los ítems ya encolados se drenan antes de que el `range`
	// termine: esperar a que los dos bucles salgan (y con ellos persistWG,
	// cuyo contador solo puede bajar ya que no entran Adds nuevos) garantiza
	// que ninguna escritura siga en vuelo cuando el llamador cierre SQLite.
	loopsDone := make(chan struct{})
	go func() {
		e.loopsWG.Wait()
		e.persistWG.Wait()
		close(loopsDone)
	}()
	select {
	case <-loopsDone:
	case <-ctx.Done():
		// Los canales ya están cerrados y los bucles siguen drenando: no hay
		// nada más que hacer sin tirar escrituras a medias, así que se avisa
		// y se devuelve el error para que el llamador lo registre.
		log.Printf("[ENGINE] shutdown timed out draining persistence loops: %v", ctx.Err())
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
			// SEC-38: el pánico se registra ANTES de suprimirlo. Sin esto, un
			// listener roto dejaba de recibir estados sin dejar rastro alguno
			// (regla 5: los errores nunca en silencio). log.Printf está
			// enganchado al bus con logbus.Attach, así que la traza llega a la
			// vista de logs; Emit sanea los saltos de línea del stack, así que
			// no puede falsificar nivel ni categoría.
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[ENGINE] panic in state listener: %v\n%s", r, debug.Stack())
				}
			}()
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

// GetFloodWaitUntil devuelve el instante hasta el cual Telegram mantendrá la pausa por FLOOD_WAIT.
func (e *Engine) GetFloodWaitUntil() time.Time {
	return GetFloodWaitUntil()
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
func (e *Engine) forgetStateOf(id string) {
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

// maxHistoryItems es el techo del historial: cuántas descargas pueden convivir
// en e.downloads sin importar su estado. Al encolar por encima del techo se
// purgan los ítems terminales más antiguos; los activos (queued, downloading,
// paused y cualquier otro no terminal) jamás se tocan (SEC-21).
const maxHistoryItems = 1000

// makeHistoryRoom garantiza sitio para un ítem más en e.downloads sin superar
// maxHistoryItems, purgando los terminales más antiguos si el historial ya está
// lleno. Se llama SIN e.mu tomado: la purga delega en (*Engine).DeleteDownload,
// que adquiere ese mismo candado, y con él tomado se produciría un deadlock.
//
// La purga es solo de metadatos (mapas en memoria y fila en SQLite): pasa
// deleteFile=false para no borrar el archivo del usuario en disco.
func (e *Engine) makeHistoryRoom() {
	e.mu.RLock()
	// +1 porque tras purgar se va a insertar un ítem más.
	excess := len(e.downloads) + 1 - maxHistoryItems
	if excess <= 0 {
		e.mu.RUnlock()
		return
	}
	ids := e.collectOldestTerminalLocked(excess)
	e.mu.RUnlock()

	for _, id := range ids {
		// Entre soltar el candado y aquí la tarea podría haberse reanudado
		// (ResumeDownload): solo se purga lo que siga en estado terminal, para
		// no borrar nunca nada con un job activo.
		e.mu.RLock()
		item, ok := e.downloads[id]
		stillTerminal := ok && isTerminalStatus(item.Status)
		e.mu.RUnlock()
		if !stillTerminal {
			continue
		}
		// DeleteDownload hace todo el trabajo pesado ya probado: mapas,
		// chatMsgMap, olvidarEstadoDe y la fila de SQLite, sin tocar ficheros.
		// Hoy devuelve siempre nil (sus errores internos ya se registran), pero
		// se comprueba el retorno por si deja de hacerlo: nada en silencio.
		if err := e.DeleteDownload(id, false); err != nil {
			log.Printf("[DOWNLOADER] %s", i18n.T("downloader.deleteDownloadError", err))
		}
	}
}

// collectOldestTerminalLocked elige los IDs de hasta `need` ítems terminales
// más antiguos (CreatedAt ascendente) sin borrar nada. Debe llamarse con e.mu
// tomado (basta lectura): el borrado real se hace fuera del candado.
func (e *Engine) collectOldestTerminalLocked(need int) []string {
	if need <= 0 {
		return nil
	}

	type candidate struct {
		id        string
		createdAt float64
	}
	terminal := make([]candidate, 0, len(e.downloads))
	for id, item := range e.downloads {
		if isTerminalStatus(item.Status) {
			terminal = append(terminal, candidate{id: id, createdAt: item.CreatedAt})
		}
	}
	sort.Slice(terminal, func(i, j int) bool {
		if terminal[i].createdAt != terminal[j].createdAt {
			return terminal[i].createdAt < terminal[j].createdAt
		}
		// Desempate por ID: dos ítems con el mismo timestamp tendrían, si no,
		// un orden indeterminado entre una llamada y la siguiente.
		return terminal[i].id < terminal[j].id
	})

	if need > len(terminal) {
		need = len(terminal)
	}
	ids := make([]string, 0, need)
	for i := 0; i < need; i++ {
		ids = append(ids, terminal[i].id)
	}
	return ids
}

func (e *Engine) ClearHistory() (int64, error) {
	e.mu.Lock()
	tempPaths := make([]string, 0, len(e.downloads))
	for id, item := range e.downloads {
		if item.Status == "completed" || item.Status == "failed" || item.Status == "cancelled" || item.Status == "skipped" || item.Status == "duplicate" {
			// Los archivos temporales se borran después, fuera del candado.
			if item.FilePath != "" {
				tempPaths = append(tempPaths, item.FilePath+".temp")
			}

			key := fmt.Sprintf("%d:%d", item.ChatID, item.MessageID)
			delete(e.chatMsgMap, key)
			delete(e.downloads, id)
			e.forgetStateOf(id)
		}
	}
	e.mu.Unlock()

	// Eliminar archivos temporales de descargas no terminadas o canceladas
	for _, path := range tempPaths {
		_ = os.Remove(path)
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
	e.forgetStateOf(id)
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

// sameFilePath compara rutas de archivo teniendo en cuenta que en Windows y macOS
// el sistema de archivos no distingue mayúsculas de minúsculas por defecto.
func sameFilePath(a, b string) bool {
	ca, cb := filepath.Clean(a), filepath.Clean(b)
	if ca == cb {
		return true
	}
	return (runtime.GOOS == "windows" || runtime.GOOS == "darwin") && strings.EqualFold(ca, cb)
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
	e.forgetStateOf(id)
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
		return errors.New("download not found")
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
		return errors.New("download not found")
	}

	if item.Status != "downloading" && item.Status != "queued" {
		e.mu.Unlock()
		return errors.New("can only pause active or queued downloads")
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
		return errors.New("download not found")
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
		return errors.New("download cannot be resumed in its current state")
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

type ExportedDownload struct {
	storage.DownloadItem
	Chunks []int64 `json:"chunks"`
}

type DownloadsExportPayload struct {
	Format     string             `json:"format"`
	Version    int                `json:"version"`
	ExportedAt string             `json:"exported_at"`
	Downloads  []ExportedDownload `json:"downloads"`
}

// ExportDownloads exporta el estado actual de las descargas e historial con sus chunks a JSON
// y guarda una copia en la carpeta de descargas configurada.
func (e *Engine) ExportDownloads() (*DownloadsExportPayload, string, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.storage == nil {
		return nil, "", errors.New("storage not initialized")
	}

	saved, err := e.storage.LoadDownloads("")
	if err != nil {
		return nil, "", fmt.Errorf("error loading downloads: %w", err)
	}

	exportedItems := make([]ExportedDownload, 0, len(saved))
	for id, item := range saved {
		// Sobrescribir con el estado mas reciente en memoria si la descarga esta activa
		if memItem, ok := e.downloads[id]; ok {
			item = *memItem
		}

		chunksMap, err := e.storage.Chunks(id)
		chunksList := make([]int64, 0, len(chunksMap))
		if err == nil {
			for cIdx := range chunksMap {
				chunksList = append(chunksList, cIdx)
			}
			sort.Slice(chunksList, func(i, j int) bool { return chunksList[i] < chunksList[j] })
		}

		exportedItems = append(exportedItems, ExportedDownload{
			DownloadItem: item,
			Chunks:       chunksList,
		})
	}

	payload := &DownloadsExportPayload{
		Format:     "tgdown-downloads",
		Version:    1,
		ExportedAt: time.Now().Format(time.RFC3339),
		Downloads:  exportedItems,
	}

	downloadFolder := e.config.DownloadFolder
	if downloadFolder == "" {
		downloadFolder = config.GetDefaultDownloadFolder()
	}
	if err := os.MkdirAll(downloadFolder, 0755); err != nil {
		log.Printf("[ENGINE] error creating download directory for export: %v", err)
	}
	exportFilePath := filepath.Join(downloadFolder, "tgdown_downloads_export.json")

	data, err := json.MarshalIndent(payload, "", "  ")
	if err == nil {
		if errWrite := os.WriteFile(exportFilePath, data, 0644); errWrite != nil {
			log.Printf("[ENGINE] error writing export file to download dir: %v", errWrite)
		} else {
			log.Printf("[ENGINE] downloads state exported successfully to %s", exportFilePath)
		}
	}

	return payload, exportFilePath, nil
}

// ImportDownloads importa el estado de descargas y sus chunks, recalculando
// la ruta del archivo para que apunte a la carpeta de descargas de la maquina actual.
func (e *Engine) ImportDownloads(payload DownloadsExportPayload) (int, int, error) {
	if len(payload.Downloads) == 0 {
		return 0, 0, errors.New("no downloads in payload")
	}

	if e.storage == nil {
		return 0, 0, errors.New("storage not initialized")
	}

	downloadFolder := e.config.DownloadFolder
	if downloadFolder == "" {
		downloadFolder = config.GetDefaultDownloadFolder()
	}

	importedCount := 0
	importedChunks := 0

	for _, exp := range payload.Downloads {
		item := exp.DownloadItem
		if strings.TrimSpace(item.ID) == "" {
			continue
		}

		// Recomputar la ruta del archivo con la carpeta de descargas de la maquina actual
		subFolder := strings.TrimSpace(item.SubFolder)
		var destDir string
		if subFolder != "" {
			destDir = filepath.Join(downloadFolder, subFolder)
		} else {
			destDir = downloadFolder
		}
		_ = os.MkdirAll(destDir, 0755)

		item.FilePath = filepath.Join(destDir, item.FileName)

		// Si el estado guardado era 'downloading' o vacio, se pasa a 'paused'
		if item.Status == "downloading" || item.Status == "" {
			item.Status = "paused"
		}

		// Persistir descarga en la base de datos de la maquina destino
		if err := e.storage.SaveDownload(item); err != nil {
			log.Printf("[ENGINE] error saving imported download %s: %v", item.ID, err)
			continue
		}

		// Limpiar chunks previos e importar los chunks nuevos
		_ = e.storage.DeleteChunks(item.ID)
		if len(exp.Chunks) > 0 {
			if err := e.storage.AddChunks(item.ID, exp.Chunks); err != nil {
				log.Printf("[ENGINE] error saving chunks for imported download %s: %v", item.ID, err)
			} else {
				importedChunks += len(exp.Chunks)
			}
		}

		// Actualizar estado en memoria del motor
		e.mu.Lock()
		copyItem := item
		e.downloads[item.ID] = &copyItem
		if item.ChatID != 0 && item.MessageID != 0 {
			key := fmt.Sprintf("%d:%d", item.ChatID, item.MessageID)
			e.chatMsgMap[key] = item.ID
		}
		e.mu.Unlock()

		// Notificar a la interfaz
		e.notifyState(item)
		importedCount++
	}

	return importedCount, importedChunks, nil
}

