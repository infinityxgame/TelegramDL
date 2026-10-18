package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/net/netutil"

	"tgdown/pkg/config"
	"tgdown/pkg/downloader"
	"tgdown/pkg/i18n"
	"tgdown/pkg/listener"
	"tgdown/pkg/logbus"
	"tgdown/pkg/notifier"
	"tgdown/pkg/scanner"
	"tgdown/pkg/storage"
	"tgdown/pkg/telegram"
	"tgdown/pkg/updater"
)

type Server struct {
	clientMgr  *telegram.ClientManager
	storage    *storage.Storage
	downloader *downloader.Engine
	listener   *listener.ListenerEngine
	scanner    *scanner.ScannerService
	updater    *updater.AppUpdater
	config     config.Config
	assets     fs.FS

	mu           sync.RWMutex
	mux          *http.ServeMux
	wsClients    map[*wsClient]bool
	upgrader     websocket.Upgrader
	httpServer   *http.Server
	stopCh       chan struct{}
	stopOnce     sync.Once
	latestRel    *updater.ReleaseInfo
	cachedDisk   *downloader.DiskInfo
	exitCallback func()
	broadcastCh      chan struct{}
	onBroadcast      func(snap map[string]any)
	apiToken         string
	folderPicker     func() (string, error)
	onLanguageChange func()
	onConfigChange   func(config.Config)
	// tokenSendNextAt es el momento a partir del cual se vuelve a admitir un
	// envío del token a Telegram (ver tokenSendCooldown).
	tokenSendNextAt time.Time
	// tokenSendByIP limita además por origen. El límite global por sí solo
	// dejaba que cualquiera en la red llenase los mensajes guardados del
	// usuario a razón de uno por minuto, día y noche.
	tokenSendByIP map[string]time.Time

	// haySesion informa si hay una sesión de Telegram guardada en disco. Es
	// una costura para las pruebas: NewServer lo conecta a
	// ClientManager.HaySesionGuardada, y un servidor de test puede sustituirlo
	// para probar la puerta de setup sin una sesión real de Telegram.
	haySesion func() bool

	// Límites de frecuencia de la puerta de setup (SEC-09): 5 intentos por
	// minuto y origen y 30 por minuto en total, contados solo en las
	// mutaciones de /api/auth/ que responden sin token. Los mapas se purgan al
	// registrar cada intento para no crecer sin límite (mismo criterio que
	// tokenSendByIP).
	setupRateByOrigin map[string]rateWindow
	setupRateGlobal   rateWindow

	// setupStatusRate acota /api/auth/status cuando la puerta la deja pasar
	// sin token (solo sin sesión guardada): 30 por minuto y origen, en un mapa
	// aparte para que quien martillee status no se coma la cuota de mutaciones
	// que necesita el asistente de login (SEC-09).
	setupStatusRate map[string]rateWindow

	// ipcRateByOrigin limita /api/ipc/download sin token a 30 intentos por
	// minuto y origen (SEC-03). Quien trae token válido no consume cuota.
	ipcRateByOrigin map[string]rateWindow

	// authLogByOrigin suprime la inundación del registro de los 401 (SEC-32):
	// como mucho un aviso por origen y minuto (rateWindow con maxAttempts=1).
	// Las ventanas caducadas se purgan al registrar cada rechazo para que el
	// mapa no crezca sin límite aunque lleguen orígenes nuevos a millares.
	authLogByOrigin map[string]rateWindow

	// hostRejectLastAt es el instante del último aviso de Host rechazado por
	// hostValidationMiddleware (SEC-08). A propósito es un único timestamp
	// global y no un mapa indexado por el Host: el índice lo controlaría el
	// atacante (un dominio distinto por petición) y el mapa crecería sin
	// techo. Con un solo reloj, como mucho se escribe un aviso por minuto y
	// la memoria que ocupa la supresión no depende de nadie más.
	hostRejectLastAt time.Time

	// Apagado del equipo al terminar la cola (interruptor shutdown_when_done).
	// hadActiveDownloads recuerda que hubo descargas activas o en cola: solo
	// así una cola vacía significa «terminó» y no «todavía no ha empezado
	// nada». shutdownTimer aplica la espera de cortesía antes de apagar y
	// shutdownDeadline es el instante en el que saltará, para que el panel
	// pueda enseñar la cuenta atrás.
	hadActiveDownloads bool
	shutdownTimer      *time.Timer
	shutdownDeadline   time.Time
	botCancel          context.CancelFunc
}

type wsClient struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (c *wsClient) writeJSON(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	return c.conn.WriteJSON(v)
}

func (c *wsClient) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.conn.Close()
}

func NewServer(
	cm *telegram.ClientManager,
	st *storage.Storage,
	dl *downloader.Engine,
	le *listener.ListenerEngine,
	up *updater.AppUpdater,
	cfg config.Config,
	assets fs.FS,
	exitCb func(),
) *Server {
	s := &Server{
		clientMgr:  cm,
		storage:    st,
		downloader: dl,
		listener:   le,
		scanner:    scanner.NewScannerService(cm, st, dl),
		updater:    up,
		config:     cfg,
		assets:     assets,
		wsClients:  make(map[*wsClient]bool),
		upgrader: websocket.Upgrader{
			// Antes esto devolvía true siempre. Hoy el token ya frena a un
			// atacante, pero era la única barrera: cualquier página abierta en
			// el navegador del usuario podía intentar la conexión. Sin cabecera
			// Origin es un cliente nativo (la ventana de Wails), que sí se
			// admite.
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				return origin == "" || isOriginAllowed(origin, r)
			},
		},
		exitCallback:   exitCb,
		mux:            http.NewServeMux(),
		stopCh:         make(chan struct{}),
		broadcastCh:    make(chan struct{}, 1),
		tokenSendByIP:  make(map[string]time.Time),

		setupRateByOrigin: make(map[string]rateWindow),
		ipcRateByOrigin:   make(map[string]rateWindow),
		authLogByOrigin:   make(map[string]rateWindow),
		setupStatusRate:   make(map[string]rateWindow),
		// Costura para tests: aquí se conecta con la sesión real de Telegram.
		haySesion: func() bool { return cm != nil && cm.HaySesionGuardada() },
	}

	s.apiToken = s.loadOrCreateToken()

	s.registerRoutes(s.mux)

	// Escuchar cambios de estado en el motor de descargas para emitir a los WebSockets
	dl.OnStateChange(func(item storage.DownloadItem) {
		s.triggerBroadcast()
		s.watchShutdownWhenDone(item)
	})

	// Escuchar cambios de estado en el motor de escucha para emitir a los WebSockets
	le.OnStateChange(func(item listener.ListenerItem) {
		s.triggerBroadcast()
	})

	s.restartBotListener()

	return s
}

const (
	// publicAPIPath devuelve solo el color del panel, que la pantalla de acceso
	// remoto necesita para pintarse antes de que nadie se haya autenticado.
	publicAPIPath = "/api/theme"

	// tokenSendAPIPath envía el token de acceso a los Mensajes guardados del
	// dueño de la cuenta de Telegram. No puede exigir token porque el token es
	// justo lo que le falta a quien pulsa el botón. Quien llama no recibe el
	// token: va al chat privado del usuario, que es el único que puede leerlo.
	// El abuso se contiene con tokenSendCooldown.
	tokenSendAPIPath = "/api/auth/token/send"

	// hasCredentialsAPIPath indica si hay credenciales de Telegram configuradas.
	// Es público porque se necesita saber qué pantalla mostrar antes de tener
	// token. Solo devuelve un booleano sin revelar información sensible.
	hasCredentialsAPIPath = "/api/auth/has-credentials"

	// rateWindowDuration es la ventana de las limitaciones de frecuencia de la
	// puerta de setup y del endpoint IPC: un minuto redondo.
	rateWindowDuration = time.Minute

	// Intentos admitidos por ventana de frecuencia: la puerta de setup admite
	// 5 por origen y 30 en total; el IPC de descargas, 30 por origen (SEC-03
	// y SEC-09).
	setupMaxPorOrigen = 5
	setupMaxGlobal    = 30
	ipcMaxPorOrigen   = 30

	// setupStatusMax es la cuota de /api/auth/status en la puerta de setup.
	// Es más holgada que la de las mutaciones porque el panel la consulta de
	// forma event-driven, pero evita que un bombardeo de GETs sin sesión
	// dispare decenas de esperas de hasta 3 s en WaitReady (SEC-09).
	setupStatusMax = 30

	// tokenSendCooldown es lo que hay que esperar entre dos envíos del token.
	// Al ser un endpoint abierto, es lo que evita que alguien que alcance el
	// puerto llene de mensajes los guardados del usuario.
	tokenSendCooldown = time.Minute

	// tokenSendFailureCooldown es la espera cuando el envío falla. Es corta a
	// propósito: si la sesión de Telegram estaba caída, el usuario debe poder
	// reintentar en cuanto la arregle, sin dejar por ello la puerta abierta a
	// reintentos sin freno.
	tokenSendFailureCooldown = 10 * time.Second

	// tokenSendCooldownByIP es la espera que se aplica a cada origen por
	// separado, por encima del límite global.
	tokenSendCooldownByIP = 15 * time.Minute

	// ownRequestHeader la pone el panel en los endpoints que responden sin
	// token. No es un secreto: su única función es que el navegador tenga que
	// hacer el preflight de CORS, que es lo que corta una petición lanzada
	// desde otra página web.
	ownRequestHeader = "X-TGDL-Request"

	// shutdownDelay es la espera entre que la cola de descargas queda vacía y
	// el apagado del equipo, con el interruptor «Apagar al terminar» armado.
	// Da margen a encadenar más descargas o a desarmar el interruptor.
	shutdownDelay = 15 * time.Second

	// maxCuerpoAPI limita el cuerpo de cada petición de /api/ a 1 MiB. Ningún
	// endpoint legítimo necesita más (el mayor es el listado de ajustes o una
	// descarga por URL), y sin techo cualquier visitante podía obligar al
	// servidor a leer cuerpos gigantes hasta agotar memoria (SEC-02).
	maxCuerpoAPI = 1 << 20

	// maxConexionesSimultaneas acota las conexiones TCP que el servidor
	// admite a la vez. Por encima de este número, Accept espera a que se
	// libere una entrada en vez de crear goroutines y búferes sin límite: sin
	// el techo, abrir conexiones y no consumirlas bastaba para tumbar la app
	// completa, incluida la ventana de Wails, que comparte el servidor (SEC-02).
	maxConexionesSimultaneas = 64

	// maxClientesWS es el techo de paneles conectados por WebSocket a la vez.
	// Quien llega con el techo lleno recibe un 503 antes del handshake, en
	// vez de una conexión que se abriría para morir en el registro (SEC-16).
	maxClientesWS = 32

	// maxLecturaWS limita el tamaño de los mensajes que se leen del panel:
	// el cliente no manda nada con más de 4 KiB, así que un WebSocket
	// malintencionado no puede forzar búferes de tamaño arbitrario (SEC-16).
	maxLecturaWS = 4 << 10
)

// publicAPIPaths son los únicos endpoints de la API que responden sin token.
var publicAPIPaths = map[string]bool{
	publicAPIPath:         true,
	tokenSendAPIPath:      true,
	hasCredentialsAPIPath: true,
	"/api/ipc/download":   true,
}

// rutasSetup son los endpoints de configuración de Telegram que pueden
// responder sin token SOLO mientras no haya sesión guardada y cumpliendo
// las condiciones de setupSinToken.
var rutasSetup = map[string]bool{
	"/api/auth/status":      true,
	"/api/auth/credentials": true,
	"/api/auth/send-code":   true,
	"/api/auth/verify-code": true,
	"/api/auth/verify-2fa":  true,
	"/api/auth/logout":      true,
}

// isPublicAPIPath determina si un endpoint puede responder sin token: solo
// lookup exacto en publicAPIPaths.
//
// Antes todo el prefijo /api/auth/ entero era público por strings.HasPrefix
// (hallazgo SEC-01): logout destructivo en bucle, fuga de PII (teléfono y
// nombre) y sobrescritura de credenciales sin ningún token. Ahora la única vía
// sin token para configurar la cuenta es la puerta de setup de handlerDeAPI
// (ver rutasSetup y setupSinToken), que exige, entre otras cosas, no tener
// sesión de Telegram guardada.
func isPublicAPIPath(path string) bool {
	return publicAPIPaths[path]
}

// handlerDeAPI decide con qué cadena de middleware se sirve una petición de
// /api/. El orden de las ramas es el contrato:
//
//  1. públicos exactos (publicAPIPaths): responden sin token, igual que antes;
//  2. quien trae token válido: exactamente el mismo camino de siempre, para
//     que quien se autentica no vea ninguna regresión;
//  3. la puerta de setup: los endpoints de rutasSetup sin token, solo cuando
//     setupSinToken lo permite (sin sesión guardada y con la forma exigida);
//  4. cualquier otro caso: el 401 estándar de authMiddleware.
func (s *Server) handlerDeAPI(r *http.Request) http.Handler {
	if isPublicAPIPath(r.URL.Path) {
		return s.corsMiddleware(s.mux)
	}
	if s.checkAuth(r) {
		return s.corsMiddleware(s.authMiddleware(s.mux))
	}
	if rutasSetup[r.URL.Path] {
		return s.corsMiddleware(s.setupHandler(s.mux))
	}
	return s.corsMiddleware(s.authMiddleware(s.mux))
}

// setupHandler aplica la puerta de setup a una ruta de rutasSetup. Si
// setupSinToken devuelve codigo 0 es porque hay sesión guardada: sin responder
// aquí, el paso siguiente resuelve el 401 estándar con authMiddleware.
func (s *Server) setupHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, codigo, detalle, retryAfter := s.setupSinToken(r)
		if ok {
			next.ServeHTTP(w, r)
			return
		}
		if codigo == 0 {
			s.authMiddleware(next).ServeHTTP(w, r)
			return
		}
		// Se responde exactamente el código y el detalle que indicó la puerta.
		// El 429 replica el patrón de handleSendTokenToTelegram: cabecera
		// Retry-After y cuerpo con estado rate_limited.
		if codigo == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			s.jsonResponse(w, codigo, map[string]any{
				"status":      "rate_limited",
				"detail":      detalle,
				"retry_after": retryAfter,
			})
			return
		}
		s.errorResponse(w, codigo, detalle)
	})
}

// rateWindow cuenta los intentos de una ventana de un minuto. Al cumplirse
// rateWindowDuration la ventana se reabre desde cero.
type rateWindow struct {
	start    time.Time
	attempts int
}

// add anota un intento en la ventana. Devuelve false cuando la ventana ya agotó
// maxAttempts, con los segundos que quedan para que caduque (>= 1).
func (w *rateWindow) add(now time.Time, maxAttempts int) (bool, int) {
	if w.start.IsZero() || now.Sub(w.start) >= rateWindowDuration {
		w.start = now
		w.attempts = 0
	}
	if w.attempts >= maxAttempts {
		falta := rateWindowDuration - now.Sub(w.start)
		return false, int(falta.Seconds()) + 1
	}
	w.attempts++
	return true, 0
}

// consumeSetupQuota gasta un intento de la cuota de la puerta de setup para
// este origen (5/min por origen y 30/min en total). El intento solo llega al
// contador global si el del origen todavía tenía sitio: así un origen ya
// limitado no consume la cuota global de los demás.
//
// Debe llamarse sin s.mu tomado.
func (s *Server) consumeSetupQuota(origin string) (bool, int) {
	ahora := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.setupRateByOrigin == nil {
		s.setupRateByOrigin = make(map[string]rateWindow)
	}
	// Se purgan las ventanas caducadas para que el mapa no crezca sin límite
	// si alguien insiste desde muchas direcciones.
	for k, w := range s.setupRateByOrigin {
		if w.start.IsZero() || ahora.Sub(w.start) >= rateWindowDuration {
			delete(s.setupRateByOrigin, k)
		}
	}

	local := s.setupRateByOrigin[origin]
	okLocal, retryLocal := local.add(ahora, setupMaxPorOrigen)
	s.setupRateByOrigin[origin] = local
	if !okLocal {
		return false, retryLocal
	}

	okGlobal, retryGlobal := s.setupRateGlobal.add(ahora, setupMaxGlobal)
	if !okGlobal {
		return false, retryGlobal
	}
	return true, 0
}

// consumeStatusQuota gasta un intento de la cuota propia de /api/auth/status
// cuando la puerta la deja pasar sin token (solo sin sesión guardada):
// setupStatusMax por minuto y origen, sin cuota global para que el desgaste
// de status no recorte la de las mutaciones del asistente de login.
//
// Debe llamarse sin s.mu tomado.
func (s *Server) consumeStatusQuota(origin string) (bool, int) {
	ahora := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.setupStatusRate == nil {
		s.setupStatusRate = make(map[string]rateWindow)
	}
	// Se purgan las ventanas caducadas, igual que en las demás cuotas, para
	// que el mapa no crezca sin límite si alguien insiste desde muchas
	// direcciones.
	for k, w := range s.setupStatusRate {
		if w.start.IsZero() || ahora.Sub(w.start) >= rateWindowDuration {
			delete(s.setupStatusRate, k)
		}
	}

	w := s.setupStatusRate[origin]
	ok, retry := w.add(ahora, setupStatusMax)
	s.setupStatusRate[origin] = w
	return ok, retry
}

// consumeIPCQuota gasta un intento de la cuota de /api/ipc/download sin token
// (30 por minuto y origen). Igual que consumeSetupQuota, purga las ventanas
// caducadas antes de contar para que el mapa no crezca sin límite.
//
// Debe llamarse sin s.mu tomado.
func (s *Server) consumeIPCQuota(origin string) (bool, int) {
	ahora := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ipcRateByOrigin == nil {
		s.ipcRateByOrigin = make(map[string]rateWindow)
	}
	for k, w := range s.ipcRateByOrigin {
		if w.start.IsZero() || ahora.Sub(w.start) >= rateWindowDuration {
			delete(s.ipcRateByOrigin, k)
		}
	}

	w := s.ipcRateByOrigin[origin]
	ok, retry := w.add(ahora, ipcMaxPorOrigen)
	s.ipcRateByOrigin[origin] = w
	return ok, retry
}

// haySesionGuardada informa si hay una sesión de Telegram guardada en disco.
// Es la costura que permite probar la puerta de setup en tests sin una sesión
// real: NewServer conecta el campo con ClientManager.HaySesionGuardada, y si
// nadie lo asignó (construcción directa de &Server{}) se responde «sin
// sesión».
func (s *Server) haySesionGuardada() bool {
	if s.haySesion == nil {
		return false
	}
	return s.haySesion()
}

// setupSinToken decide si una petición sin token puede pasar por la puerta de
// configuración de /api/auth/. Solo se admite mientras NO exista sesión de
// Telegram guardada: es el único momento en que configurar la cuenta no
// expone ni destruye nada ajeno (cierre de SEC-01: logout destructivo, fuga de
// PII y sobrescritura de credenciales sin token).
//
// Con sesión guardada devuelve codigo 0 sin responder, para que el enrutado
// deje que authMiddleware emita el 401 estándar. Si no se permite el paso,
// codigo y detalle indican la respuesta exacta (429/405/403/415) y retryAfter
// los segundos de la cabecera Retry-After (0 fuera del 429).
func (s *Server) setupSinToken(r *http.Request) (ok bool, codigo int, detalle string, retryAfter int) {
	esMutacion := r.URL.Path != "/api/auth/status"
	origin := requestOrigin(r)

	// 1) Con sesión guardada, configuración solo con token: aquí ni se mira
	// el resto de condiciones, el 401 lo resuelve authMiddleware.
	if s.haySesionGuardada() {
		return false, 0, "", 0
	}

	// 2) Frecuencia: las mutaciones gastan su cuota (5 por origen y 30 en
	// total) y /api/auth/status la suya propia, más holgada (30 por origen y
	// en mapa aparte). Con sesión no se llega aquí (paso 1), pero sin ella y
	// con el cliente MTProto ya creado —la ventana entre guardar credenciales
	// y completar el login— cada GET de status espera hasta 3 s en WaitReady
	// (pkg/telegram): sin freno, esa espera se convierte en amplificación
	// (SEC-09).
	var permitido bool
	var retry int
	if esMutacion {
		permitido, retry = s.consumeSetupQuota(origin)
	} else {
		permitido, retry = s.consumeStatusQuota(origin)
	}
	if !permitido {
		logbus.Warn(logbus.CatServer,
			i18n.T("server.setupRateLimited"),
			i18n.T("server.requestOriginDetail", origin))
		return false, http.StatusTooManyRequests,
			i18n.T("server.rateLimitedDetail", retry), retry
	}

	if esMutacion {
		// 3) Forma de la petición: un método que no sea POST no puede venir
		// del panel (logout por <img>, SEC-01); sin la cabecera propia el
		// navegador no hace el preflight de CORS (SEC-04); y un Content-Type
		// que no sea JSON permite enviar el cuerpo en modo no-cors con
		// text/plain desde otra página web (SEC-04).
		if r.Method != http.MethodPost {
			return false, http.StatusMethodNotAllowed, i18n.T("server.requestMethodNotAllowed"), 0
		}
		if r.Header.Get(ownRequestHeader) == "" {
			return false, http.StatusForbidden, i18n.T("server.requestForbidden"), 0
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			return false, http.StatusUnsupportedMediaType, i18n.T("server.requestUnsupportedType"), 0
		}
	}

	// 4) Todo cuadra: se permite el paso y queda constancia en el registro,
	// para que cada acceso sin token sea visible en el panel de Logs.
	logbus.Warn(logbus.CatServer,
		i18n.T("server.setupWithoutToken"),
		i18n.T("server.requestOriginDetail", origin))
	return true, 0, "", 0
}

// limiteDeCuerpo acota el cuerpo de cada petición de /api/ a maxCuerpoAPI
// (SEC-02). Solo se envuelve la rama de la API: los estáticos y el IPC de
// Wails no aceptan cuerpos, y el handshake del WebSocket viaja sin cuerpo, así
// que no hay nada que limitar en ellos.
func (s *Server) limiteDeCuerpo(h http.Handler) http.Handler {
	return http.MaxBytesHandler(h, maxCuerpoAPI)
}

// contentSecurityPolicy es la política de seguridad del panel servido por
// este servidor (SEC-07), directiva a directiva con la razón de cada
// ampliación. La lista sale del grep sobre dashboard/src e index.html:
//
//   - default-src 'self': red de seguridad para todo recurso sin directiva
//     propia: solo puede salir del propio origen.
//   - script-src 'self': JavaScript solo del propio origen. Ni
//     'unsafe-inline' ni 'unsafe-eval' jamás: el grep no encontró eval() ni
//     new Function en el panel, y el único script inline que había (el IIFE
//     del tema de index.html) se extrajo a /theme-init.js para que 'self' lo
//     cubra.
//   - style-src 'self' 'unsafe-inline' https://fonts.googleapis.com:
//     'unsafe-inline' lo piden los atributos style que Vue inyecta (bindings
//     :style y style="..." en App.vue, DownloadsView, ListenerView y
//     SettingsView) y las llamadas a style.setProperty de theme-init.js sobre
//     <html>; sin él el panel perdería progresos y colores en pantalla.
//     fonts.googleapis.com lo pide el @import de src/style.css (línea 1).
//   - img-src 'self' data: el logo y el favicon son del propio origen y los
//     fondos decorativos de themes-dl.css y LogsView.vue son SVG en data:;
//     el panel no pide ninguna imagen externa (no consulta miniaturas de
//     Telegram por URL).
//   - font-src 'self' https://fonts.gstatic.com: /fonts/loader.otf (fuentes
//     propias declaradas en el @font-face de App.vue) y los woff2 que sirve
//     la hoja de Google Fonts.
//   - connect-src 'self': todos los fetch() del panel son /api/... del mismo
//     origen y el WebSocket en modo web es ws(s)://mismo-origen/api/ws. El
//     fallback ws://127.0.0.1:8000 solo se dispara en la ventana Wails (host
//     con «wails»), donde esta CSP no llega a aplicarse.
//   - frame-ancestors 'none': nadie puede enmarcar la pantalla de login
//     (cierra el clickjacking sobre los botones públicos).
//   - base-uri 'none': impide reescribir <base> para secuestrar las rutas
//     relativas de los src.
//   - object-src 'none': sin plugins, <object> ni <embed>.
//
// El panel no usa workers, ni <video>/<audio> por URL externa, ni iframes,
// así que no hace falta ampliar worker-src, media-src ni frame-src: caen en
// default-src 'self' (notification.wav es un asset del propio origen).
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; img-src 'self' data:; font-src 'self' https://fonts.gstatic.com; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'"

// securityHeadersMiddleware añade las cabeceras de protección (SEC-07) a
// todas las respuestas de este servidor. Sin ellas, la pantalla de login era
// enmarcable (clickjacking), cualquier XSS futuro robaba el token de
// localStorage y las respuestas de /api/auth/* (PII) se guardaban en la
// caché del navegador.
//
// Alcance: solo el servidor HTTP propio (modo web y acceso remoto). En modo
// escritorio los assets (index.html, JS y CSS) los sirve el asset server de
// Wails y estas cabeceras NO se aplican allí: la ventana nativa queda fuera
// de este middleware; solo pasan por él las rutas /api/ que Wails deriva a
// nuestro Handler.
//
// El middleware solo toca w.Header() y delega sin envolver el
// ResponseWriter: así el http.Hijacker que necesita el upgrade de /api/ws
// sigue siendo el mismo objeto de siempre, y gorilla escribe el 101 del
// handshake a mano después de secuestrar la conexión (las cabeceras del
// mapa se descartan al secuestrar), con lo que el WebSocket no se rompe ni
// se duplican cabeceras en la respuesta de cambio de protocolo.
//
// Cache-Control: no-store se limita a /api/: ahí viajan PII y token; los
// assets estáticos siguen cacheándose con normalidad.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// viteDevPort es el puerto del servidor de desarrollo de Vite
// (dashboard/vite.config.js). El proxy /api reescribe el Host a la dirección
// del backend (changeOrigin), pero el Origin de la página sigue siendo
// localhost:8080, así que ese puerto hay que admitirlo a la hora de mirar el
// Origin de una petición en desarrollo.
const viteDevPort = 8080

// hostLogMaxLen acorta el Host escrito en el registro: la cabecera Host es
// entrada del atacante y no tiene por qué caber ni ser legible tal cual.
const hostLogMaxLen = 100

// hostValidationMiddleware rechaza con 421 Misdirected Request cualquier
// petición cuya cabecera Host no apunte a este servidor (SEC-08, DNS
// rebinding). Se sitúa ANTES de todo handler, de modo que ninguna ruta —pública
// o protegida— llega a ejecutarse con un Host ajeno.
//
// La cadena final es securityHeadersMiddleware(hostValidationMiddleware(...))
// en Handler() y en WebHandler(): el middleware de cabeceras va por fuera
// para que la propia respuesta 421 salga ya con la CSP y el resto de
// cabeceras de SEC-07 (si validase el Host por fuera, el 421 sería la única
// respuesta del servidor sin ellas).
//
// El cuerpo se emite con s.errorResponse para que tenga exactamente la misma
// forma JSON ({"detail": ..., "error": ...}) que el resto de errores.
func (s *Server) hostValidationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hostAllowed(r.Host) {
			next.ServeHTTP(w, r)
			return
		}
		// Nada en silencio: el rechazo deja constancia en el registro, con
		// supresión de inundación (ver registerHostRejection).
		s.registerHostRejection(r.Host)
		s.errorResponse(w, http.StatusMisdirectedRequest, "Cabecera Host no permitida")
	})
}

// hostAllowed decide si la cabecera Host de una petición nombra a este
// servidor (SEC-08).
//
// El cliente manda el Host que corresponde a la URL que tiene delante, y de
// esa URL se encarga el DNS: si «evil.com» apunta a 127.0.0.1, una página
// servida desde ese dominio llega aquí con Host: evil.com:8000 y Origin:
// http://evil.com:8000, dos cabeceras que coinciden entre sí pero que no
// nombran a este servidor. Antes de este hallazgo no existía ninguna
// comprobación del Host en ningún punto del servidor, de modo que cualquier
// reflejo de «mismo origen» (isOriginAllowed) acababa validando un dominio
// ajeno: era el agujero clásico de DNS rebinding.
//
// Reglas aplicadas sobre el host ya sin puerto, en minúsculas y sin los
// corchetes [] de las IPv6:
//
//  1. Vacío → se admite. Toda petición HTTP/1.1 real que entra por net/http
//     lleva cabecera Host (de hecho net/http la exige), así que un valor
//     vacío solo puede venir de una llamada directa al handler, como las que
//     hacen los tests con httptest.NewRequest; no hay motivo para romperlas.
//  2. IP literal → se admite sea la que sea: loopback, IP de LAN o la
//     100.x.y.z de Tailscale. El rebinding siempre se monta sobre nombres de
//     dominio, nunca sobre una IP.
//  3. localhost, wails.localhost y *.wails.localhost → se admiten: la ventana
//     de escritorio sirve el panel bajo wails.localhost con puerto dinámico.
//  4. Nombre de una sola etiqueta (sin puntos) → se admite: los nombres
//     cortos de LAN y los de MagicDNS (p. ej. «mispeluquin») son habituales.
//     Riesgo residual aceptado y comentado: un nombre sin puntos solo puede
//     resolverse dentro de la propia red o del propio equipo, no desde
//     Internet, así que el rebinding externo sigue cortado.
//  5. Sufijos .ts.net (MagicDNS de Tailscale) y .local (mDNS de red local) →
//     se admiten: son infraestructuras gobernadas por el dueño de la red, y
//     REMOTE_ACCESS.md documenta Tailscale como el método de acceso remoto
//     recomendado para el panel (LAN y Tailscale incluidos).
//  6. El host concreto de la configuración de escucha (bind_host), cuando no
//     es wildcard (0.0.0.0, :: o vacío) → se admite porque nombra a este
//     mismo servidor. Se lee con config.GetServerHost(), la misma fuente en
//     vivo que usa Server.Start para abrir el listener (env > base de datos >
//     valor por defecto); no se añade ninguna dependencia nueva.
//  7. Cualquier otro nombre de dominio punteado (evil.com, panel.example.com
//     cuando no es el bind_host) → se rechaza: es justo el caso del DNS
//     rebinding y de un Host reescrito a mano.
func hostAllowed(host string) bool {
	// Regla 1: sin Host no hay nada que validar (ver comentario de la regla).
	if strings.TrimSpace(host) == "" {
		return true
	}
	normalized := hostWithoutPort(host)

	// Regla 2: cualquier IP literal es este mismo equipo o esta misma red.
	if net.ParseIP(normalized) != nil {
		return true
	}

	// Regla 3: los nombres de la ventana de escritorio de Wails.
	if normalized == "localhost" || normalized == "wails.localhost" ||
		strings.HasSuffix(normalized, ".wails.localhost") {
		return true
	}

	// Regla 4: nombres de una sola etiqueta (LAN / MagicDNS cortos).
	if !strings.Contains(normalized, ".") {
		return true
	}

	// Regla 5: infraestructuras gobernadas (Tailscale y mDNS).
	if strings.HasSuffix(normalized, ".ts.net") || strings.HasSuffix(normalized, ".local") {
		return true
	}

	// Regla 6: el host de escucha concreto configurado para este servidor.
	if bind := hostWithoutPort(config.GetServerHost()); !isWildcardBind(bind) &&
		strings.EqualFold(bind, normalized) {
		return true
	}

	// Regla 7: el resto de dominios punteados se rechazan.
	return false
}

// hostWithoutPort reduce un valor «host» o «host:puerto» a la sola parte del
// host, en minúsculas y sin los corchetes [] de las IPv6. Si no hay puerto
// (o el valor es una IPv6 entre corchetes sin puerto), SplitHostPort falla y
// se conserva el valor tal cual, que es lo que quieren las reglas 3 a 7.
func hostWithoutPort(host string) string {
	h := strings.TrimSpace(host)
	if withoutPort, _, err := net.SplitHostPort(h); err == nil {
		h = strings.TrimSpace(withoutPort)
	}
	return strings.ToLower(strings.Trim(h, "[]"))
}

// isWildcardBind indica si la dirección de escucha configurada no nombra una
// interfaz concreta (vacío, 0.0.0.0 o ::: todas las interfaces). En ese caso
// no hay host propio que añadir a la lista de permitidos, porque el valor no
// identifica a este servidor ante nadie.
func isWildcardBind(bind string) bool {
	switch bind {
	case "", "0.0.0.0", "::":
		return true
	}
	return false
}

// registerHostRejection anota en el registro un Host rechazado por
// hostValidationMiddleware (SEC-08): sin este aviso, un intento de DNS
// rebinding sería invisible para quien mire el panel de logs.
//
// La supresión de inundación deja como mucho un aviso por minuto usando un
// único timestamp del servidor (hostRejectLastAt). No se indexa por el Host
// atacante: así la memoria ocupada no depende de cuantos dominios distintos
// se prueben, ni dentro ni fuera de la ventana.
//
// Debe llamarse sin s.mu tomado.
func (s *Server) registerHostRejection(host string) {
	now := time.Now()

	s.mu.Lock()
	if !s.hostRejectLastAt.IsZero() && now.Sub(s.hostRejectLastAt) < rateWindowDuration {
		// Ya se dejó constancia de un rechazo en la ventana actual: se
		// silencia aquí para no inundar el registro con la misma línea.
		s.mu.Unlock()
		return
	}
	s.hostRejectLastAt = now
	s.mu.Unlock()

	// Mensaje en literal inglés al estilo de los avisos internos de app.go
	// («engine shutdown incomplete», «database close failed»): es un aviso
	// interno del backend, no una cadena de la interfaz. El Host va saneado
	// y acortado porque se inyecta desde la cabecera de la petición.
	logbus.Warn(logbus.CatServer,
		"Request rejected: Host header is not allowed for this server",
		"Host: "+sanitizeLogHost(host))
}

// sanitizeLogHost prepara un valor de cabecera Host para escribirlo en el
// registro: quita los caracteres de control (CR/LF primero, que son los que
// permitirían fabricar una línea falsa en el log) y lo acorta a
// hostLogMaxLen bytes sin cortar por medio de un rune UTF-8. Es defensa en
// profundidad: el logbus ya sanea, pero este valor llega de la cabecera y
// también puede llegar de un proxy o de una llamada de test.
func sanitizeLogHost(host string) string {
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, host)
	if len(clean) > hostLogMaxLen {
		// ToValidUTF8 descarta el último rune a medias si el corte lo parte.
		clean = strings.ToValidUTF8(clean[:hostLogMaxLen], "")
	}
	return clean
}

func (s *Server) Handler() http.Handler {
	return securityHeadersMiddleware(s.hostValidationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.limiteDeCuerpo(s.handlerDeAPI(r)).ServeHTTP(w, r)
			return
		}
		// Para cualquier ruta de frontend, devolver 404 para que Wails AssetServer
		// sirva index.html e inyecte los bindings e IPC nativos.
		http.NotFound(w, r)
	})))
}

func (s *Server) WebHandler() http.Handler {
	return securityHeadersMiddleware(s.hostValidationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.limiteDeCuerpo(s.handlerDeAPI(r)).ServeHTTP(w, r)
			return
		}
		// El panel estático (HTML/JS/CSS) se sirve siempre sin autenticación:
		// la pantalla de login remoto necesita poder cargar antes de tener token.
		s.mux.ServeHTTP(w, r)
	})))
}

// nuevoHTTPServer monta el http.Server con los límites de red (SEC-02). Se
// extrae a una función propia para que los tests puedan comprobar los campos
// sin abrir puertos.
//
// Por qué cada límite:
//
//   - ReadHeaderTimeout e IdleTimeout ya estaban: acotan la lectura de las
//     cabeceras y el tiempo ocioso entre peticiones de una conexión keep-alive.
//   - ReadTimeout es nuevo: sin él, un atacante podía abrir una conexión y
//     soltar el cuerpo gota a gota (slowloris) manteniendo ocupada una
//     goroutine para siempre.
//   - MaxHeaderBytes acaba con cabeceras gigantes que el servidor acabaría
//     alojando en memoria.
//   - NO se fija WriteTimeout: mataría los WebSocket, que son conexiones
//     largas por definición, y también las respuestas amplias (historial,
//     listado de descargas). Antes el comentario decía que solo se limitaba
//     ReadHeaderTimeout porque ReadTimeout también habría cortado el WS: ahora
//     ReadTimeout sí se aplica y el WebSocket sobrevive porque, tras el
//     upgrade, handleWebSocket limpia la fecha límite heredada con
//     conn.UnderlyingConn().SetDeadline(time.Time{}).
func nuevoHTTPServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler: h,
		// 30 s para leer cabeceras y cuerpo; la conexión ociosa tiene su
		// propio tope más amplio (IdleTimeout).
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		// 16 KiB de cabeceras como máximo; por encima se corta la petición.
		MaxHeaderBytes: 16 << 10,
	}
}

func (s *Server) Start(port int) error {
	host := config.GetServerHost()
	var addr string
	if host == "0.0.0.0" || host == "" {
		addr = fmt.Sprintf(":%d", port)
	} else {
		addr = fmt.Sprintf("%s:%d", host, port)
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		// Fallback a 127.0.0.1
		addr = fmt.Sprintf("127.0.0.1:%d", port)
		listener, err = net.Listen("tcp", addr)
		if err != nil {
			return err
		}
	}

	// Aviso de exposición (SEC-05): se comprueba DESPUÉS del fallback para
	// reflejar la dirección que realmente quedó escuchando. Si no es de
	// loopback, el panel es alcanzable desde fuera de este equipo y el
	// tráfico (token, credenciales, contraseña 2FA) viaja en claro.
	hostEfectivo := host
	if tcp, ok := listener.Addr().(*net.TCPAddr); ok && tcp.IP != nil {
		hostEfectivo = tcp.IP.String()
	}
	if !config.EsLoopback(hostEfectivo) {
		logbus.Warn(logbus.CatServer, i18n.T("server.exposedWithoutTLS", hostEfectivo, port), "")
	}

	// Techo de conexiones simultáneas (SEC-02): por encima de
	// maxConexionesSimultaneas, Accept espera a que se libere una entrada en
	// vez de seguir creando goroutines y búferes sin límite.
	listener = netutil.LimitListener(listener, maxConexionesSimultaneas)

	s.httpServer = nuevoHTTPServer(s.WebHandler())

	// Tarea de refresco y broadcast periódico debounced
	go s.periodicBroadcastLoop()
	go s.broadcastDebounceLoop()

	// Comprobación de actualización inmediata al iniciar
	go func() {
		time.Sleep(1 * time.Second)
		rel, _, err := s.updater.CheckForUpdate()
		if err == nil && rel != nil {
			s.mu.Lock()
			s.latestRel = rel
			s.mu.Unlock()
			s.triggerBroadcast()
		}
	}()

	go func() {
		_ = s.httpServer.Serve(listener)
	}()

	return nil
}

func (s *Server) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.mu.Lock()
	if s.botCancel != nil {
		s.botCancel()
		s.botCancel = nil
	}
	if s.shutdownTimer != nil {
		// Si la aplicación se cierra a mitad de la cuenta atrás, el apagado se
		// cancela: quien la cerró está delante del equipo.
		s.shutdownTimer.Stop()
		s.shutdownTimer = nil
		s.shutdownDeadline = time.Time{}
	}
	s.mu.Unlock()
	if s.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.httpServer.Shutdown(ctx)
	}
}

// isOriginAllowed decide si una petición que llega con cabecera Origin puede
// tocar la API. Son estos los casos que se admiten:
//
//  1. El panel se sirve desde la misma dirección y puerto que la API. Esto es
//     lo normal, y cubre el acceso remoto entero —el móvil entrando por
//     192.168.1.40:8000, un nombre de Tailscale, un túnel— sin tener que
//     enumerar direcciones de antemano.
//
//  2. El panel corre en este mismo equipo con otra dirección: la ventana de
//     Wails (wails.localhost), o el servidor de desarrollo de Vite, que usa
//     otro puerto.
//
// Antes esto era una lista fija de nombres (localhost, 127.0.0.1 y el host
// configurado) y después una comprobación genérica de «equipo local» que
// admitía CUALQUIER puerto de loopback: cualquier proceso local sirviendo
// una página en :9999 obtenía CORS de lectura sobre los endpoints públicos
// con PII. También reflejaba r.Host como si fuese «mismo origen» sin que
// nadie hubiese validado antes ese Host (DNS rebinding). Ambos agujeros
// quedan cerrados aquí y en hostValidationMiddleware.
//
// Las peticiones sin cabecera Origin (el CLI `tgdown <url>`, curl) no llegan
// a entrar en esta función: vacío significa «no es una petición de
// navegador» y lo único que se omite es el CORS de siempre.
func isOriginAllowed(origin string, r *http.Request) bool {
	if strings.TrimSpace(origin) == "" {
		return false
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}

	// Mismo origen: el panel y la API entraron por la misma puerta. Esto solo
	// es seguro porque el middleware de validación del Host corre ANTES en la
	// cadena (securityHeadersMiddleware(hostValidationMiddleware(...)), en
	// Handler() y WebHandler()) y ya ha descartado con 421 cualquier r.Host
	// que no nombre a este servidor: sin esa garantía, un Host reescrito por
	// DNS rebinding haría que «mismo origen» cubriese un dominio ajeno.
	if r != nil && r.Host != "" && strings.EqualFold(parsed.Host, r.Host) {
		return true
	}

	originHost := hostWithoutPort(parsed.Host)
	originPort := ""
	if _, port, splitErr := net.SplitHostPort(parsed.Host); splitErr == nil {
		originPort = port
	}

	// wails.localhost (con cualquier puerto): el asset server de Wails sirve
	// el panel con un puerto dinámico, así que su Host nunca coincidirá con
	// el del servidor; y la ventana de escritorio conecta el WebSocket
	// directamente a ws://127.0.0.1:8000 con Origin http://wails.localhost:
	// <puerto> (App.vue:862-865). Sin esta rama, la ventana se quedaría sin
	// estado en el panel de escritorio.
	if originHost == "wails.localhost" || strings.HasSuffix(originHost, ".wails.localhost") {
		return true
	}

	// Loopback con puerto: solo el del propio servidor (config en vivo) o el
	// 8080 del servidor de desarrollo de Vite (dashboard/vite.config.js:40).
	// El proxy de Vite reescribe el Host a 127.0.0.1:<puerto del backend>
	// (changeOrigin), pero el Origin de la página sigue siendo
	// localhost:8080, así que sin admitir ese puerto el panel se rompería en
	// desarrollo. Puerto ausente u otro distinto → no se admite: era el
	// agujero de «cualquier puerto de loopback».
	if originHost == "localhost" || originHost == "127.0.0.1" || originHost == "::1" {
		serverPort := strconv.Itoa(config.GetServerPort())
		return originPort != "" && (originPort == serverPort || originPort == strconv.Itoa(viteDevPort))
	}

	// El resto de orígenes (IP de LAN, Tailscale, nombres de dominio) solo
	// se admiten por la vía de mismo origen de arriba; aquí no se reflejan
	// jamás.
	return false
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if isOriginAllowed(origin, r) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, "+ownRequestHeader)
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// authMiddleware exige un token de acceso válido (ver GenerateToken) en toda
// la API. Sin esto, cualquier proceso o página web que alcance el puerto
// del servidor podía leer las credenciales de Telegram, navegar el
// filesystem completo o apagar la aplicación sin autenticarse.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.checkAuth(r) {
			// Cada rechazo deja constancia con el origen, con supresión de
			// inundación: sin esto, la fuerza bruta del token era invisible
			// en el panel de logs (SEC-32).
			s.registrarRechazoAuth(r)
			s.errorResponse(w, http.StatusUnauthorized, i18n.T("server.invalidToken"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// registrarRechazoAuth anota en el registro un 401 con el origen de la
// petición (SEC-32): la fuerza bruta del token no dejaba rastro alguno.
//
// La supresión de inundación admite como mucho un aviso por origen y minuto,
// reutilizando rateWindow con maxAttempts=1. Las ventanas caducadas se purgan
// en cada llamada, así que el mapa no crece sin límite aunque cada petición
// venga de un origen distinto.
//
// Debe llamarse sin s.mu tomado.
func (s *Server) registrarRechazoAuth(r *http.Request) {
	origin := requestOrigin(r)
	ahora := time.Now()

	s.mu.Lock()
	if s.authLogByOrigin == nil {
		s.authLogByOrigin = make(map[string]rateWindow)
	}
	// Se purgan las ventanas caducadas para que el mapa no crezca sin límite.
	for k, w := range s.authLogByOrigin {
		if w.start.IsZero() || ahora.Sub(w.start) >= rateWindowDuration {
			delete(s.authLogByOrigin, k)
		}
	}
	w := s.authLogByOrigin[origin]
	// maxAttempts=1: dentro de la ventana solo se registra la primera vez.
	permitido, _ := w.add(ahora, 1)
	s.authLogByOrigin[origin] = w
	s.mu.Unlock()

	if !permitido {
		// Este origen ya dejó su aviso en la ventana actual: se silencia aquí
		// para no inundar el registro con la misma línea una y otra vez.
		return
	}
	logbus.Warn(logbus.CatServer,
		i18n.T("server.authRejected"),
		i18n.T("server.requestOriginDetail", origin))
}

// wsSubprotocol es el subprotocolo que marca una conexión de TelegramDL. El
// cliente ofrece dos: este y, como segundo, el token.
const wsSubprotocol = "tgdl-v1"

// wsCloseTokenRotated es el código de cierre de aplicación (rango 4000-4999,
// reservado a la aplicación por RFC 6455) con el que se desconecta a los
// paneles conectados cuando rota el token de acceso (SEC-35): el navegador
// lo entrega como error de la aplicación, y eso distingue «vuelve a
// autenticarte» de una caída de red genérica.
const wsCloseTokenRotated = 4401

// wsCloseTokenRotatedMsg es el motivo corto en ASCII del frame de cierre
// anterior (máximo 123 bytes por RFC 6455). Solo describe el motivo: el valor
// del token jamás viaja en un frame de cierre (SEC-25).
const wsCloseTokenRotatedMsg = "token rotated"

// subprotocolToken saca el token de la cabecera Sec-WebSocket-Protocol.
func subprotocolToken(r *http.Request) string {
	for _, header := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, part := range strings.Split(header, ",") {
			part = strings.TrimSpace(part)
			if part != "" && part != wsSubprotocol {
				return part
			}
		}
	}
	return ""
}

func (s *Server) checkAuth(r *http.Request) bool {
	s.mu.RLock()
	token := s.apiToken
	s.mu.RUnlock()
	if token == "" {
		// No debería ocurrir (loadOrCreateToken siempre genera uno), pero si
		// pasara, negamos por defecto en vez de dejar la API abierta.
		return false
	}

	provided := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if provided == "" {
		// El navegador no deja poner cabeceras al abrir un WebSocket, pero sí
		// declarar subprotocolos, y eso viaja en una cabecera normal. Antes se
		// aceptaba el token como parámetro de la URL, y las URL completas
		// quedan registradas en cualquier proxy o túnel por el que pase la
		// conexión: justo lo que se usa para el acceso remoto.
		provided = subprotocolToken(r)
	}
	if provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}

// loadOrCreateToken recupera el token guardado en SQLite o genera uno nuevo
// la primera vez que arranca la app (no requiere ninguna acción del
// usuario).
func (s *Server) loadOrCreateToken() string {
	if s.storage != nil {
		if tok, err := s.storage.GetAPIToken(); err == nil && tok != "" {
			return tok
		}
	}

	tok, err := config.GenerateToken()
	if err != nil {
		// Extremadamente improbable (fallo de crypto/rand), pero preferimos
		// un token débil a dejar la API sin protección.
		tok = config.NewID()
	}
	if s.storage != nil {
		if err := s.storage.SaveAPIToken(tok); err != nil {
			log.Printf("[SERVER] %s", i18n.T("server.saveTokenError", err))
		}
	}
	return tok
}

// APIToken devuelve el token actual. Se expone al frontend de escritorio a
// través de un binding nativo de Wails (App.GetLocalToken), nunca por HTTP
// sin autenticar.
func (s *Server) APIToken() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.apiToken
}

// RegenerateToken invalida el token anterior y genera uno nuevo. Cualquier
// dispositivo remoto que estuviera usando el token viejo deberá
// reconfigurarse con el nuevo valor, y los paneles ya conectados por
// WebSocket se desconectan para volver a autenticarse (SEC-35).
func (s *Server) RegenerateToken() string {
	tok, err := config.GenerateToken()
	if err != nil {
		tok = config.NewID()
	}
	s.mu.Lock()
	s.apiToken = tok
	// SEC-35: el snapshot de los clientes WS conectados se toma DENTRO de la
	// misma adquisición que rota el token: es el único instante en que el
	// mapa y s.apiToken son coherentes entre sí.
	clients := make([]*wsClient, 0, len(s.wsClients))
	for c := range s.wsClients {
		clients = append(clients, c)
	}
	s.mu.Unlock()
	if s.storage != nil {
		if err := s.storage.SaveAPIToken(tok); err != nil {
			log.Printf("[SERVER] %s", i18n.T("server.saveTokenError", err))
		}
	}

	// SEC-35: el cierre de cada WebSocket se hace FUERA de s.mu. Nada de
	// syscalls de red (WriteControl ni Close) con el candado global tomado:
	// una escritura retenida en un socket dejaría parada toda la API (misma
	// razón por la que este método ya libera el candado antes de SaveAPIToken).
	// Tampoco hace falta c.mu: gorilla documenta que WriteControl y Close son
	// seguros concurrentes con las escrituras normales, así que no hay
	// inversión de candados con writeJSON.
	for _, c := range clients {
		// Cierre con código de aplicación 4401 (rango 4000-4999): el panel
		// distingue «vuelve a autenticarte con el token nuevo» de una caída
		// de red genérica. El error se descarta a propósito (best-effort): si
		// el cliente ya se cayó por su cuenta no queda nada que avisar, y su
		// bucle de lectura morirá igualmente.
		_ = c.conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(wsCloseTokenRotated, wsCloseTokenRotatedMsg),
			time.Now().Add(time.Second))
		c.close()
	}
	// El mapa NO se borra aquí: lo hace el defer de handleWebSocket cuando su
	// bucle de lectura muera por el cierre de arriba. Borrarlo también desde
	// esta goruta solo añadiría un segundo escritor sobre s.wsClients sin
	// aportar nada (y hay que dejar que ese defer siga siendo el único
	// dueño del retiro, para que nadie lo "optimice" y termine con un
	// cliente vivo sin registro).
	logbus.Info(logbus.CatServer,
		i18n.T("server.tokenRotated", len(clients)), "")
	return tok
}

func (s *Server) jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("[SERVER] %s", i18n.T("server.jsonError", err))
	}
}

func (s *Server) errorResponse(w http.ResponseWriter, status int, message string) {
	s.jsonResponse(w, status, map[string]string{"detail": message, "error": message})
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Servir panel estático en /dashboard/ y en / para el servidor web en puerto 8000
	var staticHandler http.Handler
	if s.assets != nil {
		if sub, err := fs.Sub(s.assets, "dashboard/dist"); err == nil {
			staticHandler = http.FileServer(http.FS(sub))
		} else {
			staticHandler = http.FileServer(http.FS(s.assets))
		}
	} else {
		distDir := filepath.Join(config.BaseDir, "dashboard", "dist")
		if fi, err := os.Stat(distDir); err == nil && fi.IsDir() {
			staticHandler = http.FileServer(http.Dir(distDir))
		}
	}

	if staticHandler != nil {
		mux.Handle("/dashboard/", http.StripPrefix("/dashboard/", staticHandler))
		mux.Handle("/", staticHandler)
	}

	// WebSocket
	mux.HandleFunc("/api/ws", s.handleWebSocket)

	// Auth
	mux.HandleFunc("/api/auth/status", s.handleAuthStatus)
	mux.HandleFunc("/api/auth/credentials", s.handleAuthCredentials)
	mux.HandleFunc("/api/auth/send-code", s.handleAuthSendCode)
	mux.HandleFunc("/api/auth/verify-code", s.handleAuthVerifyCode)
	mux.HandleFunc("/api/auth/verify-2fa", s.handleAuthVerify2FA)
	mux.HandleFunc("/api/auth/logout", s.handleAuthLogout)
	// Público: indica si hay credenciales de Telegram configuradas (sin token)
	mux.HandleFunc(hasCredentialsAPIPath, s.handleHasCredentials)

	// Token de acceso a la API (acceso remoto)
	mux.HandleFunc("/api/auth/token", s.handleGetToken)
	mux.HandleFunc("/api/auth/token/regenerate", s.handleRegenerateToken)
	// Sin token; ver tokenSendAPIPath.
	mux.HandleFunc(tokenSendAPIPath, s.handleSendTokenToTelegram)

	// Downloads (Soporta /api/downloads, /api/downloads/history, /api/downloads/open, /api/downloads/{id})
	mux.HandleFunc("/api/downloads", s.handleDownloadsRoute)
	mux.HandleFunc("/api/downloads/", s.handleDownloadsRoute)
	mux.HandleFunc("/api/downloads/cancel-all", s.handleCancelAllDownloads)
	mux.HandleFunc("/api/downloads/pause-all", s.handlePauseAllDownloads)
	mux.HandleFunc("/api/downloads/resume-all", s.handleResumeAllDownloads)
	mux.HandleFunc("/api/download", s.handleStartDownload)
	mux.HandleFunc("/api/cancel", s.handleCancelDownload)
	mux.HandleFunc("/api/pause", s.handlePauseDownload)
	mux.HandleFunc("/api/resume", s.handleResumeDownload)
	mux.HandleFunc("/api/retry", s.handleRetryDownload)
	mux.HandleFunc("/api/delete", s.handleDeleteDownload)
	mux.HandleFunc("/api/clear-history", s.handleClearHistory)
	mux.HandleFunc("/api/open", s.handleOpenDownload)

	// Settings
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/settings/speed", s.handleSpeedLimit)
	mux.HandleFunc("/api/settings/speed-limit", s.handleSpeedLimit)

	// Notifications
	mux.HandleFunc("/api/notifications/test", s.handleNotificationTest)
	mux.HandleFunc("/api/notifications/detect", s.handleNotificationDetect)

	// Listener
	mux.HandleFunc("/api/listener", s.handleListenerItems)
	mux.HandleFunc("/api/listener/settings", s.handleListenerSettings)
	mux.HandleFunc("/api/listener/items", s.handleListenerItems)
	mux.HandleFunc("/api/listener/download", s.handleListenerDownload)
	mux.HandleFunc("/api/listener/clear", s.handleListenerClear)
	mux.HandleFunc("/api/listener/delete", s.handleListenerDeleteItem)
	mux.HandleFunc("/api/listener/item/", s.handleListenerDeleteItemPath)
	mux.HandleFunc("/api/listener/resolve-chat", s.handleListenerResolveChat)
	mux.HandleFunc("/api/listener/chat/", s.handleListenerResolveChatPath)
	mux.HandleFunc("/api/listener/topics", s.handleListenerTopics)
	mux.HandleFunc("/api/listener/topics/", s.handleListenerTopics)
	mux.HandleFunc("/api/listener/update-filename", s.handleListenerUpdateFilename)
	mux.HandleFunc("/api/listener/reorder", s.handleListenerReorder)

	// Scanner / Histórico
	mux.HandleFunc("/api/scanner/search", s.handleScannerSearch)
	mux.HandleFunc("/api/scanner/download", s.handleScannerDownload)
	mux.HandleFunc("/api/scanner/resolve-target", s.handleScannerResolveTarget)
	mux.HandleFunc("/api/scanner/recents", s.handleScannerRecents)
	mux.HandleFunc("/api/scanner/recents/favorite", s.handleScannerRecentFavorite)
	mux.HandleFunc("/api/scanner/recents/delete", s.handleScannerRecentDelete)
	mux.HandleFunc("/api/scanner/recents/clear", s.handleScannerRecentClear)

	// Registro de actividad (vista de logs en vivo)
	mux.HandleFunc("/api/logs", s.handleLogs)
	mux.HandleFunc("/api/logs/clear", s.handleLogsClear)
	mux.HandleFunc("/api/logs/export", s.handleLogsExport)

	// Color del panel (sin token; ver publicAPIPath)
	mux.HandleFunc(publicAPIPath, s.handleTheme)

	// Filesystem & System
	mux.HandleFunc("/api/filesystem", s.handleFSBrowse)
	mux.HandleFunc("/api/fs/browse", s.handleFSBrowse)
	mux.HandleFunc("/api/fs/pick", s.handleFSPick)
	mux.HandleFunc("/api/system/disk", s.handleSystemDisk)
	mux.HandleFunc("/api/system/info", s.handleSystemInfo)
	mux.HandleFunc("/api/server/info", s.handleSystemInfo)

	// Updates (Soporta tanto singular /update/ como plural /updates/)
	mux.HandleFunc("/api/update/check", s.handleCheckUpdate)
	mux.HandleFunc("/api/updates/check", s.handleCheckUpdate)
	mux.HandleFunc("/api/update/progress", s.handleUpdateProgress)
	mux.HandleFunc("/api/updates/progress", s.handleUpdateProgress)
	mux.HandleFunc("/api/update/install", s.handleInstallUpdate)
	mux.HandleFunc("/api/updates/install", s.handleInstallUpdate)
	mux.HandleFunc("/api/update/postpone", s.handlePostponeUpdate)

	// App Exit (Soporta /api/app/exit y /api/exit)
	mux.HandleFunc("/api/app/exit", s.handleExit)
	mux.HandleFunc("/api/exit", s.handleExit)

	// IPC endpoint para recibir URLs desde una segunda instancia
	mux.HandleFunc("/api/ipc/download", s.handleIPCDownload)
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Techo de clientes (SEC-16): se comprueba ANTES del upgrade para que el
	// navegador vea un 503 de verdad. Respondido esto, se sale sin tocar el
	// handshake: si se comprobara después, la conexión ya habría quedado
	// abierta y el cliente solo vería un WebSocket que se cae en el acto.
	if s.websocketLimiteSuperado() {
		http.Error(w, i18n.T("server.wsTooManyClients"), http.StatusServiceUnavailable)
		return
	}

	// Si el cliente ofreció subprotocolos hay que devolverle uno, o el navegador
	// rechaza la conexión. Se responde siempre el nuestro, nunca el token.
	var headers http.Header
	if r.Header.Get("Sec-WebSocket-Protocol") != "" {
		headers = http.Header{"Sec-WebSocket-Protocol": []string{wsSubprotocol}}
	}

	conn, err := s.upgrader.Upgrade(w, r, headers)
	if err != nil {
		return
	}

	// Handshake hecho: solo entra lo razonable. El panel no manda mensajes de
	// más de 4 KiB, así que cualquier tamaño mayor es un ataque (SEC-16).
	conn.SetReadLimit(maxLecturaWS)

	// El servidor aplica ReadTimeout (30 s) a la conexión que acaba de ser
	// secuestrada para el WebSocket, y esa fecha límite sigue impuesta en el
	// socket subyacente: sin limpiarla, el panel moriría cada 30 s y entraría
	// en bucle de reconexión. Se borra para que la conexión larga solo la rija
	// su propia actividad.
	if err := conn.UnderlyingConn().SetDeadline(time.Time{}); err != nil {
		log.Printf("[SERVER] %s", i18n.T("server.wsDeadlineError", err))
	}

	client := &wsClient{conn: conn}

	s.mu.Lock()
	s.wsClients[client] = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.wsClients, client)
		s.mu.Unlock()
		client.close()
	}()

	// Enviar estado inicial inmediato
	snap := s.buildStateSnapshot()
	_ = client.writeJSON(snap)

	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

// websocketLimiteSuperado indica que ya hay maxClientesWS paneles conectados.
// Se lee bajo s.mu para que dos handshakes simultáneos no superen el techo
// entre los dos.
func (s *Server) websocketLimiteSuperado() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.wsClients) >= maxClientesWS
}

// periodicBroadcastLoop refresca el estado que ve el panel.
//
// Aquí ya no se comprueban actualizaciones: el propio panel pregunta por
// /api/update/check cada pocos minutos, así que tener además un reloj en el
// servidor significaba pedirle a GitHub dos veces lo mismo, y su API solo
// admite 60 peticiones por hora y por IP.
func (s *Server) periodicBroadcastLoop() {
	broadcastTicker := time.NewTicker(1 * time.Second)
	diskTicker := time.NewTicker(3 * time.Second)
	defer broadcastTicker.Stop()
	defer diskTicker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-diskTicker.C:
			s.mu.RLock()
			folder := s.config.DownloadFolder
			s.mu.RUnlock()

			if disk, err := downloader.GetDiskUsage(folder, s.downloader.GetDownloads()...); err == nil {
				s.mu.Lock()
				s.cachedDisk = disk
				s.mu.Unlock()
			}
		case <-broadcastTicker.C:
			s.triggerBroadcast()
		}
	}
}

func (s *Server) broadcastDebounceLoop() {
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	pending := false

	for {
		select {
		case <-s.stopCh:
			return
		case <-s.broadcastCh:
			pending = true
		case <-ticker.C:
			if pending {
				s.broadcastState()
				pending = false
			}
		}
	}
}

func (s *Server) triggerBroadcast() {
	select {
	case s.broadcastCh <- struct{}{}:
	default:
	}
}

func (s *Server) SetBroadcastCallback(cb func(map[string]any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onBroadcast = cb
}

func (s *Server) broadcastState() {
	s.mu.RLock()
	cb := s.onBroadcast
	if len(s.wsClients) == 0 && cb == nil {
		s.mu.RUnlock()
		return
	}
	clients := make([]*wsClient, 0, len(s.wsClients))
	for c := range s.wsClients {
		clients = append(clients, c)
	}
	s.mu.RUnlock()

	snap := s.buildStateSnapshot()

	if cb != nil {
		cb(snap)
	}

	var toRemove []*wsClient
	for _, c := range clients {
		if err := c.writeJSON(snap); err != nil {
			toRemove = append(toRemove, c)
		}
	}

	if len(toRemove) > 0 {
		s.mu.Lock()
		for _, c := range toRemove {
			delete(s.wsClients, c)
			c.close()
		}
		s.mu.Unlock()
	}
}

func (s *Server) BuildStateSnapshot() map[string]any {
	return s.buildStateSnapshot()
}

func (s *Server) buildStateSnapshot() map[string]any {
	downloads := s.downloader.GetDownloads()
	activeCount := 0
	queuedCount := 0

	for _, d := range downloads {
		switch d.Status {
		case "downloading":
			activeCount++
		case "queued":
			queuedCount++
		}
	}

	speedBytes := s.downloader.GetTotalSpeedBytes()

	s.mu.RLock()
	folder := s.config.DownloadFolder
	disk := s.cachedDisk
	cfg := s.config
	s.mu.RUnlock()

	if disk == nil {
		if d, err := downloader.GetDiskUsage(folder, downloads...); err == nil {
			s.mu.Lock()
			s.cachedDisk = d
			disk = d
			s.mu.Unlock()
		}
	}

	listenerItems := s.listener.GetItems()

	s.mu.RLock()
	latest := s.latestRel
	var shutdownPendingAt float64
	if s.shutdownTimer != nil && !s.shutdownDeadline.IsZero() {
		shutdownPendingAt = float64(s.shutdownDeadline.Unix())
	}
	s.mu.RUnlock()

	var updateInfo any = nil
	if latest != nil {
		updateInfo = map[string]any{
			"available": true,
			"version":   latest.TagName,
			"changelog": latest.Body,
		}
	}

	// Cifras de la sesión en curso: lo bajado desde que la aplicación está en
	// marcha. El historial restaurado no entra, así el panel no hereda las
	// descargas de sesiones anteriores tras un reinicio.
	sessionStats := s.downloader.GetSessionStats()

	// Este snapshot viaja por el WebSocket y por el binding de Wails a todos
	// los clientes conectados, así que el token del bot no puede salir en
	// claro (SEC-25): se enmascara en una copia local. s.config conserva el
	// token real, que es el que sigue usando el notificador para enviar.
	maskedCfg := cfg
	maskedCfg.NotificationBotToken = notifier.MaskToken(cfg.NotificationBotToken)

	snap := map[string]any{
		"type":         "state",
		"downloads":    downloads,
		"listener":     listenerItems,
		"settings":     maskedCfg,
		"active_count": activeCount,
		"queued_count": queuedCount,
		"speed_total":  config.FormatBytes(float64(speedBytes)) + "/s",
		"speed_bytes":  speedBytes,
		"disk":         disk,
		"logs_seq":     logbus.LastID(),
		"update":       updateInfo,
		"server_time":  float64(time.Now().Unix()),
		"session_stats": map[string]any{
			"completed": sessionStats.Completed,
			"failed":    sessionStats.Failed,
			"skipped":   sessionStats.Skipped,
		},
	}

	// Marca de tiempo (unix) en la que saltará el apagado programado del
	// equipo, o 0 si no hay ninguno. El panel la cruza con server_time para la
	// cuenta atrás, sin fiarse del reloj del dispositivo que lo mira.
	snap["shutdown_pending_at"] = shutdownPendingAt

	var floodWaitUntil float64
	if until := s.downloader.GetFloodWaitUntil(); !until.IsZero() {
		floodWaitUntil = float64(until.Unix())
	}
	snap["flood_wait_until"] = floodWaitUntil

	return snap
}

// Handlers de Auth
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	st := s.clientMgr.GetAuthStatus(r.Context())
	if !st.HasCredentials {
		// Recuperar desde la base de datos (con fallback a .env)
		apiID, apiHash, _ := s.storage.GetCredentials()
		if apiID != "" && apiHash != "" {
			_ = s.clientMgr.InitClient(apiID, apiHash)
			st = s.clientMgr.GetAuthStatus(r.Context())
		}
	}
	s.mu.RLock()
	if st.User != nil && s.config.ColorID != nil {
		st.User.ColorID = s.config.ColorID
	}
	configuredLang := s.config.Language
	s.mu.RUnlock()

	res := map[string]any{
		"configured":      st.Configured,
		"has_credentials": st.HasCredentials,
		"authorized":      st.Authorized,
		"authenticated":   st.Authenticated,
		"state":           st.State,
		"phone":           st.Phone,
		"user":            st.User,
		"language":        configuredLang,
	}
	s.jsonResponse(w, http.StatusOK, res)
}

// handleHasCredentials indica si hay credenciales de Telegram configuradas Y
// si hay una sesión activa. Es un endpoint público (sin token) porque se
// necesita para decidir qué pantalla mostrar antes de que el usuario tenga
// token de acceso. Solo devuelve un booleano sin revelar información sensible.
func (s *Server) handleHasCredentials(w http.ResponseWriter, r *http.Request) {
	apiID, apiHash, _ := s.storage.GetCredentials()
	hasCreds := apiID != "" && apiHash != ""

	// Verificar si hay una sesión activa de Telegram
	var hasActiveSession bool
	if hasCreds {
		st := s.clientMgr.GetAuthStatus(r.Context())
		hasActiveSession = st.Authenticated || st.State == "LOGGED_IN"
	}

	s.mu.RLock()
	configuredLang := s.config.Language
	s.mu.RUnlock()

	// Devolvemos si la app está completamente configurada (credenciales + sesión)
	s.jsonResponse(w, http.StatusOK, map[string]any{
		"has_credentials":    hasCreds,
		"has_active_session": hasActiveSession,
		"is_configured":      hasCreds && hasActiveSession,
		"language":           configuredLang,
	})
}

func (s *Server) handleAuthCredentials(w http.ResponseWriter, r *http.Request) {
	var body struct {
		APIID   string `json:"api_id"`
		APIHash string `json:"api_hash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.invalidData"))
		return
	}

	// La base de datos es la única fuente de verdad para las credenciales. Antes
	// se escribían además en un .env, y el fallo que se daba por bueno era el de
	// la base de datos: si esto falla no queda ningún otro sitio de donde
	// recuperarlas, así que ahora sí se corta aquí.
	if err := s.storage.SaveCredentials(body.APIID, body.APIHash); err != nil {
		log.Printf("[SERVER] %s", i18n.T("server.saveCredsError", err))
		s.errorResponse(w, http.StatusInternalServerError, i18n.T("server.saveCredsErrorMsg"))
		return
	}

	if err := s.clientMgr.InitClient(body.APIID, body.APIHash); err != nil {
		log.Printf("[SERVER] %s", i18n.T("server.tgInitError", err))
		s.errorResponse(w, http.StatusInternalServerError, i18n.T("server.tgInitErrorMsg"))
		return
	}

	// Esperar un momento para que el cliente MTProto tenga tiempo de iniciar
	// la conexión antes de consultar su estado. Esto evita race conditions
	// donde el cliente aún no está listo cuando consultamos GetAuthStatus.
	time.Sleep(500 * time.Millisecond)

	s.jsonResponse(w, http.StatusOK, s.clientMgr.GetAuthStatus(r.Context()))
}

func (s *Server) handleAuthSendCode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Phone       string `json:"phone"`
		PhoneNumber string `json:"phone_number"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.invalidData"))
		return
	}
	phone := strings.TrimSpace(body.Phone)
	if phone == "" {
		phone = strings.TrimSpace(body.PhoneNumber)
	}
	if phone == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.phoneRequired"))
		return
	}

	hash, err := s.clientMgr.SendCode(r.Context(), phone)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok", "phone_code_hash": hash})
}

func (s *Server) handleAuthVerifyCode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Phone         string `json:"phone"`
		PhoneNumber   string `json:"phone_number"`
		Code          string `json:"code"`
		PhoneCodeHash string `json:"phone_code_hash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.invalidData"))
		return
	}
	phone := strings.TrimSpace(body.Phone)
	if phone == "" {
		phone = strings.TrimSpace(body.PhoneNumber)
	}
	code := strings.TrimSpace(body.Code)
	if code == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.codeRequired"))
		return
	}

	status, err := s.clientMgr.VerifyCode(r.Context(), phone, code, body.PhoneCodeHash)
	if err != nil {
		logbus.Error(logbus.CatTelegram, i18n.T("telegram.codeRejected"), err.Error())
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if status == "ok" {
		logbus.Success(logbus.CatTelegram, i18n.T("telegram.sessionStarted"), "")
	}

	st := s.clientMgr.GetAuthStatus(r.Context())
	// Inyectar el estado específico de la verificación (como 2fa_required)
	// para que el frontend sepa si debe mostrar el paso de contraseña.
	response := struct {
		telegram.AuthStatus
		Status string `json:"status"`
	}{
		AuthStatus: st,
		Status:     status,
	}

	s.jsonResponse(w, http.StatusOK, response)
}

func (s *Server) handleAuthVerify2FA(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Password == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.passwordRequired"))
		return
	}

	if err := s.clientMgr.Verify2FA(r.Context(), body.Password); err != nil {
		logbus.Error(logbus.CatTelegram, i18n.T("telegram.tfaRejected"), err.Error())
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	logbus.Success(logbus.CatTelegram, i18n.T("telegram.sessionStarted2FA"), "")

	s.jsonResponse(w, http.StatusOK, s.clientMgr.GetAuthStatus(r.Context()))
}

// handleGetToken devuelve el token de acceso actual. Ya pasa por
// authMiddleware (requiere conocer el token para poder consultarlo), así
// que solo sirve para que un cliente ya autenticado confirme su valor
// (por ejemplo, para mostrarlo en Ajustes).
func (s *Server) handleGetToken(w http.ResponseWriter, r *http.Request) {
	s.jsonResponse(w, http.StatusOK, map[string]string{"token": s.APIToken()})
}

// requestOrigin describe de dónde vino una petición para dejarlo en el
// registro. Se usa la dirección real de la conexión, no las cabeceras de
// proxy: esas las escribe quien llama y se pueden falsificar, así que servirían
// justo para lo contrario de lo que se busca aquí.
func requestOrigin(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && strings.TrimSpace(host) != "" {
		return host
	}
	if addr := strings.TrimSpace(r.RemoteAddr); addr != "" {
		return addr
	}
	return "origen desconocido"
}

// recordTokenSendByIP anota cuándo puede volver a pedir el token este origen y
// aprovecha para limpiar las entradas ya caducadas, de modo que el mapa no
// crezca sin límite si alguien insiste desde muchas direcciones.
//
// Debe llamarse con s.mu tomado.
func (s *Server) recordTokenSendByIP(origin string, until time.Time) {
	if s.tokenSendByIP == nil {
		s.tokenSendByIP = make(map[string]time.Time)
	}
	now := time.Now()
	for k, v := range s.tokenSendByIP {
		if v.Before(now) {
			delete(s.tokenSendByIP, k)
		}
	}
	s.tokenSendByIP[origin] = until
}

// buildTokenMessage redacta la nota que se deja en los Mensajes guardados. El
// token va en su propia línea para que el formato de código lo abarque entero
// y baste un toque para copiarlo.
func buildTokenMessage(token string) string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = i18n.T("server.thisComputer")
	}

	return strings.Join([]string{
		i18n.T("server.tokenMsgTitle"),
		"",
		token,
		"",
		i18n.T("server.tokenMsgBody"),
		fmt.Sprintf(i18n.T("server.tokenMsgComputer"), host, config.AppVersion),
		fmt.Sprintf(i18n.T("server.tokenMsgSentAt"), time.Now().Format("02/01/2006 15:04")),
	}, "\n")
}

// handleSendTokenToTelegram manda el token de acceso a los Mensajes guardados
// del usuario, para no tener que ir al ordenador a copiarlo cuando se entra
// desde el móvil.
//
// Es un endpoint abierto por necesidad: quien lo pulsa es precisamente quien no
// tiene token. La respuesta nunca incluye el token; solo dice si se envió. Lo
// único que puede conseguir un extraño que alcance el puerto es provocar un
// mensaje en el chat privado del usuario, y para eso está el límite de
// frecuencia.
func (s *Server) handleSendTokenToTelegram(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.errorResponse(w, http.StatusMethodNotAllowed, i18n.T("server.methodNotAllowed"))
		return
	}

	// Este endpoint responde sin token, así que cualquier página web abierta en
	// el navegador del usuario podía dispararlo: la petición no llevaba cuerpo
	// ni Content-Type, y eso la convierte en una «simple request» que el
	// navegador manda sin consultar antes con CORS. Exigir una cabecera propia
	// obliga al navegador a hacer el preflight, que CORS rechaza.
	if r.Header.Get(ownRequestHeader) == "" {
		s.errorResponse(w, http.StatusForbidden, i18n.T("server.invalidRequest"))
		return
	}

	origin := requestOrigin(r)
	now := time.Now()

	s.mu.Lock()
	if wait := s.tokenSendByIP[origin].Sub(now); wait > 0 {
		s.mu.Unlock()
		seconds := int(wait.Seconds()) + 1
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		s.jsonResponse(w, http.StatusTooManyRequests, map[string]any{
			"status":      "rate_limited",
			"detail":      fmt.Sprintf(i18n.T("server.waitXSeconds"), seconds),
			"retry_after": seconds,
		})
		return
	}
	if wait := s.tokenSendNextAt.Sub(now); wait > 0 {
		s.mu.Unlock()

		seconds := int(wait.Seconds()) + 1
		logbus.Warn(logbus.CatServer,
			i18n.T("server.tokenSendLimited"),
			i18n.T("server.tokenSendLimitedDetail", origin, seconds))

		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		s.jsonResponse(w, http.StatusTooManyRequests, map[string]any{
			"status":      "rate_limited",
			"detail":      fmt.Sprintf(i18n.T("server.waitXSeconds"), seconds),
			"retry_after": seconds,
		})
		return
	}
	// El turno se consume antes de enviar: si Telegram tarda, un botón pulsado
	// con impaciencia no debe convertirse en cinco mensajes.
	s.tokenSendNextAt = now.Add(tokenSendCooldown)
	s.recordTokenSendByIP(origin, now.Add(tokenSendCooldownByIP))
	token := s.apiToken
	s.mu.Unlock()

	if token == "" {
		s.errorResponse(w, http.StatusServiceUnavailable, i18n.T("server.noTokenGenerated"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if err := s.clientMgr.SendToSavedMessages(ctx, buildTokenMessage(token), token); err != nil {
		s.mu.Lock()
		s.tokenSendNextAt = time.Now().Add(tokenSendFailureCooldown)
		// El límite por origen también se levanta: si el envío falló, quien lo
		// pidió debe poder reintentar en cuanto arregle la sesión, igual que con
		// el límite global.
		delete(s.tokenSendByIP, origin)
		s.mu.Unlock()

		logbus.Error(logbus.CatServer, i18n.T("server.tokenSendError"), err.Error())
		s.errorResponse(w, http.StatusBadGateway, fmt.Sprintf(i18n.T("server.tokenSendErrorPrefix"), err.Error()))
		return
	}

	logbus.Success(logbus.CatServer,
		i18n.T("server.tokenSent"),
		i18n.T("server.tokenSentDetail", origin))

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status":      "ok",
		"detail":      i18n.T("server.tokenSentDetailText"),
		"retry_after": int(tokenSendCooldown.Seconds()),
	})
}

// handleRegenerateToken rota el token de acceso. Requiere el token vigente
// (via authMiddleware); tras esto, cualquier otro dispositivo remoto deberá
// reconfigurarse con el nuevo valor devuelto aquí.
func (s *Server) handleRegenerateToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.errorResponse(w, http.StatusMethodNotAllowed, i18n.T("server.methodNotAllowed"))
		return
	}
	newToken := s.RegenerateToken()
	s.jsonResponse(w, http.StatusOK, map[string]string{"token": newToken})
}

// handleAuthLogout cierra la sesión de Telegram y, solo si se pide de forma
// explícita y autenticada, gira el token de acceso (SEC-27): tras «cerrar
// sesión» en Ajustes, cualquier cliente que aún conservara el token (otro
// dispositivo, una pestaña olvidada, un atacante que lo hubiera capturado)
// seguía con acceso completo al panel.
//
// El giro SOLO ocurre cuando se cumplen las tres condiciones siguientes:
//
// (a) el método es POST y el resto de guardias actuales dejan pasar la
// petición: la puerta de setup ya exige POST (responde 405 antes, ver
// setupSinToken) y el camino autenticado ya pasó por authMiddleware. No se
// añade ningún 405 nuevo: los códigos de respuesta actuales quedan intactos;
//
// (b) clientMgr.Logout ha tenido éxito: si la sesión de Telegram no se
// destruyó, no se gira el token, porque el cierre no se completó y el
// usuario legítimo se quedaría sin acceso con la sesión todavía viva;
//
// (c) el cuerpo trae revoke_token == true Y checkAuth(r) es verdadero: quien
// pide la revocación debe traer un token válido. Sin token (la puerta de
// setup también alcanza este handler) el flag se ignora: si no, un atacante
// sin token podría invalidar el token legítimo (DoS de rotación no
// autenticada).
//
// Por qué un flag opt-in y no girar en todo logout: /api/auth/logout también
// lo llama AuthWizard para cancelar o retroceder el flujo de login (p. ej.
// «Atrás» desde 2FA); si se rotase en todo logout, esas cancelaciones
// matarían el token del cliente web y lo expulsarían a mitad de la
// configuración.
func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	// SEC-27: flag revoke_token leído con tolerancia y SOLO en POST (condición
	// (a)): cuerpo ausente, vacío o malformado → false, sin error, porque las
	// llamadas antiguas —AuthWizard y demás— van sin cuerpo y deben seguir
	// comportándose igual que siempre. El techo del cuerpo ya lo pone
	// limiteDeCuerpo (maxCuerpoAPI) en la cadena de /api/, así que aquí no se
	// duplica el límite.
	revokeToken := false
	if r.Method == http.MethodPost && r.Body != nil {
		var body struct {
			RevokeToken bool `json:"revoke_token"`
		}
		// Si Decode falla (vacío → io.EOF o JSON malformado) el flag se queda
		// a false a propósito: el error se conoce y se ignora aquí mismo para
		// no romper el logout de los llamantes antiguos.
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			revokeToken = body.RevokeToken
		}
	}

	// SEC-24: el error ya no se descarta en silencio. Si Logout falla y aquí
	// se respondiera 200, el usuario vería «sesión cerrada» con la sesión de
	// Telegram todavía viva. Es también la condición (b) del giro: sin sesión
	// destruida no se rota el token.
	if err := s.clientMgr.Logout(r.Context()); err != nil {
		logbus.Error(logbus.CatTelegram, i18n.T("server.logoutFailed"), err.Error())
		s.jsonResponse(w, http.StatusBadGateway, map[string]string{
			"status": "error",
			"detail": i18n.T("server.logoutFailedDetail", err.Error()),
		})
		return
	}
	logbus.Warn(logbus.CatTelegram, i18n.T("telegram.loggedOut"), "")

	// Condiciones (b) cumplida (se llegó hasta aquí) y (c): giro solo con flag
	// explícito y petición autenticada. RegenerateToken (SEC-35) desconecta de
	// paso todos los WebSocket con 4401 y loguea server.tokenRotated; el token
	// nuevo NUNCA se incluye en la respuesta (SEC-25).
	if revokeToken && s.checkAuth(r) {
		s.RegenerateToken()
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Handlers de Descargas
func (s *Server) handleGetDownloads(w http.ResponseWriter, _ *http.Request) {
	s.jsonResponse(w, http.StatusOK, s.downloader.GetDownloads())
}

// ErrQueueFull marca el rechazo de EnqueueURL cuando la cola de descargas ya
// está en su techo (SEC-21). Los handlers HTTP lo detectan con errors.Is para
// responder 429 con Retry-After en vez del 400 genérico.
var ErrQueueFull = errors.New("download queue is full")

// retryAfterQueueFull son los segundos de la cabecera Retry-After cuando la
// cola está llena. Un minuto da tiempo a que la cola avance de verdad; un valor
// mucho menor invitaría a reintentos casi inmediatos que la volverían a llenar
// al instante.
const retryAfterQueueFull = 60

// respondQueueFull responde 429 a una cola llena con el mismo cuerpo que el
// 429 de cuota IPC: status "rate_limited", detalle traducido y retry_after.
func (s *Server) respondQueueFull(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(retryAfterQueueFull))
	s.jsonResponse(w, http.StatusTooManyRequests, map[string]any{
		"status":      "rate_limited",
		"detail":      i18n.T("server.rateLimitedDetail", retryAfterQueueFull),
		"retry_after": retryAfterQueueFull,
	})
}

// EnqueueURL procesa una URL de Telegram y añade los elementos correspondientes a la cola.
// Si la cola está en su techo, corta el rango y devuelve un error que envuelve
// ErrQueueFull: las tareas ya encoladas se quedan encoladas (SEC-21).
func (s *Server) EnqueueURL(ctx context.Context, rawURL string) error {
	clean := strings.TrimSpace(rawURL)
	if clean == "" {
		return errors.New("empty URL")
	}

	parsed, err := downloader.ParseURL(clean)
	if err != nil {
		return fmt.Errorf("error parsing URL: %w", err)
	}

	chatID := parsed.ChatID
	if chatID == 0 && parsed.ChatUsername != "" {
		if err := s.clientMgr.WaitReady(ctx); err != nil {
			return fmt.Errorf("Telegram client not ready: %w", err)
		}
		resolvedID, err := s.clientMgr.ResolveUsername(ctx, parsed.ChatUsername)
		if err != nil {
			return fmt.Errorf("channel or user could not be found: %w", err)
		}
		chatID = resolvedID
	}

	jobID := config.NewID()
	for msgID := parsed.StartMsgID; msgID <= parsed.EndMsgID; msgID++ {
		item := storage.DownloadItem{
			ID:        config.NewID(),
			JobID:     jobID,
			MessageID: int64(msgID),
			ChatID:    chatID,
			Status:    "queued",
			Source:    "manual",
			FileName:  fmt.Sprintf("mensaje_%d", msgID),
		}
		// "" = rechazado por techo de cola (SEC-21). Se corta el rango aquí:
		// el caller decide la respuesta HTTP. El broadcast va antes de salir
		// para que las tareas que sí entraron se vean en el panel sin esperar
		// al siguiente polling.
		if s.downloader.QueueItem(item) == "" {
			s.broadcastState()
			return fmt.Errorf("queue is full while enqueueing %s: %w", clean, ErrQueueFull)
		}
	}
	s.broadcastState()

	logbus.Info(logbus.CatDownloads, i18n.T("server.ipcDownloadReceived", clean), "")
	return nil
}

func (s *Server) handleStartDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.URL) == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.urlRequired"))
		return
	}

	// Separar por espacios, saltos de línea, comas o puntos y comas para procesar enlaces en lote en orden secuencial
	rawTokens := regexp.MustCompile(`[\s,;]+`).Split(strings.TrimSpace(body.URL), -1)
	var urls []string
	for _, tok := range rawTokens {
		tok = strings.TrimSpace(tok)
		if tok != "" {
			urls = append(urls, tok)
		}
	}

	if len(urls) == 0 {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.urlRequired"))
		return
	}

	var enqueuedCount int
	for _, rawURL := range urls {
		if err := s.EnqueueURL(r.Context(), rawURL); err != nil {
			// SEC-21: cola llena → 429 con Retry-After si no se pudo encolar ninguno;
			// si ya se encolaron algunos, se detiene y responde OK con los procesados.
			if errors.Is(err, ErrQueueFull) {
				if enqueuedCount > 0 {
					break
				}
				s.respondQueueFull(w)
				return
			}
			if enqueuedCount == 0 {
				s.errorResponse(w, http.StatusBadRequest, err.Error())
				return
			}
			break
		}
		enqueuedCount++
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": i18n.T("server.analyzingMsg"),
	})
}

// decodeOptionalBody decodifica el cuerpo JSON en los handlers que hasta ahora
// descartaban el error con `_ =`. SEC-38: el fallo deja de ser silencioso,
// pero un cuerpo vacío (io.EOF / io.ErrUnexpectedEOF) sigue sin registrarse:
// en todos estos endpoints el body es opcional o sus campos se validan después
// y se responde 422/404, así que llenar el log de EOFs por llamadas que van
// sin cuerpo no aportaría nada. Solo se loguea la clase de error, nunca el
// cuerpo, porque puede contener datos sensibles. Las respuestas no cambian:
// quien hace decode ciego ya valida o falla igual que antes.
func decodeOptionalBody(r *http.Request, dst any, handler string) {
	err := json.NewDecoder(r.Body).Decode(dst)
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return
	}
	log.Printf("[SERVER] %s: invalid request body: %v", handler, err)
}

func (s *Server) handleCancelDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID     string `json:"id"`
		ItemID string `json:"item_id"`
	}
	decodeOptionalBody(r, &body, "handleCancelDownload")
	id := strings.TrimSpace(body.ID)
	if id == "" {
		id = strings.TrimSpace(body.ItemID)
	}
	if id == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.idRequired"))
		return
	}

	_ = s.downloader.CancelDownload(id)
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handlePauseDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID     string `json:"id"`
		ItemID string `json:"item_id"`
	}
	decodeOptionalBody(r, &body, "handlePauseDownload")
	id := strings.TrimSpace(body.ID)
	if id == "" {
		id = strings.TrimSpace(body.ItemID)
	}
	if id == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.idRequired"))
		return
	}

	if err := s.downloader.PauseDownload(id); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleResumeDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID     string `json:"id"`
		ItemID string `json:"item_id"`
	}
	decodeOptionalBody(r, &body, "handleResumeDownload")
	id := strings.TrimSpace(body.ID)
	if id == "" {
		id = strings.TrimSpace(body.ItemID)
	}
	if id == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.idRequired"))
		return
	}

	if err := s.downloader.ResumeDownload(r.Context(), id); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleCancelAllDownloads(w http.ResponseWriter, r *http.Request) {
	s.downloader.CancelAll()
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handlePauseAllDownloads(w http.ResponseWriter, r *http.Request) {
	s.downloader.PauseAll()
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleResumeAllDownloads(w http.ResponseWriter, r *http.Request) {
	s.downloader.ResumeAll(r.Context())
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleRetryDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID     string `json:"id"`
		ItemID string `json:"item_id"`
	}
	decodeOptionalBody(r, &body, "handleRetryDownload")
	id := strings.TrimSpace(body.ID)
	if id == "" {
		id = strings.TrimSpace(body.ItemID)
	}
	if id == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.idRequired"))
		return
	}

	if err := s.downloader.ResumeDownload(r.Context(), id); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDownloadsRoute(w http.ResponseWriter, r *http.Request) {
	subpath := strings.TrimPrefix(r.URL.Path, "/api/downloads")
	subpath = strings.TrimPrefix(subpath, "/")

	if subpath == "" {
		if r.Method == http.MethodGet {
			s.handleGetDownloads(w, r)
			return
		}
		s.errorResponse(w, http.StatusMethodNotAllowed, i18n.T("server.methodNotAllowed"))
		return
	}

	if subpath == "history" {
		if r.Method == http.MethodDelete {
			s.handleClearHistory(w, r)
			return
		}
		s.errorResponse(w, http.StatusMethodNotAllowed, i18n.T("server.methodNotAllowed"))
		return
	}

	if subpath == "open" {
		if r.Method == http.MethodPost {
			s.handleOpenDownload(w, r)
			return
		}
		s.errorResponse(w, http.StatusMethodNotAllowed, i18n.T("server.methodNotAllowed"))
		return
	}

	// Subpath es el ID de la descarga (ej. DELETE /api/downloads/{id})
	if r.Method == http.MethodDelete {
		// Por defecto NO se borra el archivo: solo se quita la entrada del
		// historial. El cliente debe pedir el borrado físico explícitamente.
		delFile := false
		if q := r.URL.Query().Get("delete_file"); q != "" {
			if strings.ToLower(q) == "true" || q == "1" {
				delFile = true
			}
		}

		_ = s.downloader.DeleteDownload(subpath, delFile)
		s.listener.RemoveItem(subpath)
		s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	s.errorResponse(w, http.StatusNotFound, i18n.T("server.notFound"))
}

func (s *Server) handleDeleteDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID         string `json:"id"`
		ItemID     string `json:"item_id"`
		DeleteFile *bool  `json:"delete_file"`
	}
	decodeOptionalBody(r, &body, "handleDeleteDownload")
	id := strings.TrimSpace(body.ID)
	if id == "" {
		id = strings.TrimSpace(body.ItemID)
	}
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	if id == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.idRequired"))
		return
	}

	delFile := false
	if body.DeleteFile != nil {
		delFile = *body.DeleteFile
	} else if q := r.URL.Query().Get("delete_file"); q != "" {
		if strings.ToLower(q) == "true" || q == "1" {
			delFile = true
		}
	}

	_ = s.downloader.DeleteDownload(id, delFile)
	s.listener.RemoveItem(id)
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleClearHistory(w http.ResponseWriter, r *http.Request) {
	removed, _ := s.downloader.ClearHistory()
	logbus.Info(logbus.CatDownloads, i18n.T("downloads.historyCleared", removed), "")
	s.jsonResponse(w, http.StatusOK, map[string]any{"status": "ok", "removed": removed})
}

func (s *Server) handleOpenDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID     string `json:"id"`
		ItemID string `json:"item_id"`
	}
	// Con el body vacío o ilegible id queda en "", GetItem("") falla y el
	// handler responde 404: no hay ninguna salida silenciosa.
	decodeOptionalBody(r, &body, "handleOpenDownload")
	id := strings.TrimSpace(body.ID)
	if id == "" {
		id = strings.TrimSpace(body.ItemID)
	}
	item, ok := s.downloader.GetItem(id)
	if !ok || item.FilePath == "" {
		s.errorResponse(w, http.StatusNotFound, i18n.T("server.fileNotFound"))
		return
	}

	go openInSystem(item.FilePath)

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Settings
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.mu.RLock()
		cfg := s.config
		s.mu.RUnlock()
		// Enmascarar el token del bot por seguridad
		maskedCfg := cfg
		maskedCfg.NotificationBotToken = notifier.MaskToken(cfg.NotificationBotToken)
		s.jsonResponse(w, http.StatusOK, map[string]any{
			"status":   "ok",
			"settings": maskedCfg,
		})
		return
	}

	var raw map[string]any
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.invalidConfig"))
		return
	}

	s.mu.Lock()
	cfg := s.config
	previousCfg := s.config

	if v, ok := raw["max_concurrent_downloads"]; ok && v != nil {
		cfg.MaxConcurrentDownloads = int(config.ParseInt64(v))
	}
	if v, ok := raw["parallel_chunks"]; ok && v != nil {
		if b, ok := v.(bool); ok {
			cfg.ParallelChunks = b
		}
	}
	if v, ok := raw["chunk_workers"]; ok && v != nil {
		cfg.ChunkWorkers = int(config.ParseInt64(v))
	}
	if v, ok := raw["organize_by_chat"]; ok && v != nil {
		if b, ok := v.(bool); ok {
			cfg.OrganizeByChat = b
		}
	}
	if v, ok := raw["minimize_to_tray"]; ok && v != nil {
		if b, ok := v.(bool); ok {
			cfg.MinimizeToTray = b
		}
	}
	if v, ok := raw["sound_notification"]; ok && v != nil {
		if b, ok := v.(bool); ok {
			cfg.SoundNotification = b
		}
	}
	if v, ok := raw["shutdown_when_done"]; ok && v != nil {
		if b, ok := v.(bool); ok {
			cfg.ShutdownWhenDone = b
		}
	}
	if v, ok := raw["download_folder"]; ok && v != nil {
		if sVal, ok := v.(string); ok && strings.TrimSpace(sVal) != "" {
			// Se descarta en silencio una carpeta prohibida en vez de aplicarla:
			// quien llama puede ser un dispositivo remoto con el token, y dejar
			// archivos en la carpeta de Inicio o sobre la sesión de Telegram no
			// es una preferencia, es una vía de ataque.
			if err := config.ValidateDownloadFolder(sVal); err != nil {
				logbus.Warn(logbus.CatServer, i18n.T("server.folderRejected"), sVal+": "+err.Error())
			} else {
				cfg.DownloadFolder = sVal
			}
		}
	}
	if v, ok := raw["color_id"]; ok {
		if v == nil {
			cfg.ColorID = nil
		} else {
			c := int(config.ParseInt64(v))
			cfg.ColorID = &c
		}
	}
	if v, ok := raw["loader_color_id"]; ok {
		if v == nil {
			cfg.LoaderColorID = nil
		} else {
			c := int(config.ParseInt64(v))
			cfg.LoaderColorID = &c
		}
	}
	if v, ok := raw["language"]; ok && v != nil {
		if sVal, ok := v.(string); ok {
			cfg.Language = sVal
		}
	}
	if v, ok := raw["speed_limit"].(map[string]any); ok {
		if val, ok := v["value"]; ok && val != nil {
			if f, ok := val.(float64); ok {
				cfg.SpeedLimit.Value = f
			} else {
				cfg.SpeedLimit.Value = float64(config.ParseInt64(val))
			}
		}
		if unit, ok := v["unit"].(string); ok && strings.TrimSpace(unit) != "" {
			cfg.SpeedLimit.Unit = unit
		}
	}
	if v, ok := raw["listener_enabled"]; ok && v != nil {
		if b, ok := v.(bool); ok {
			cfg.ListenerEnabled = b
		}
	}
	if v, ok := raw["listener_chats"]; ok && v != nil {
		if chatBytes, err := json.Marshal(v); err == nil {
			var parsedChats []config.ListenerChat
			if json.Unmarshal(chatBytes, &parsedChats) == nil {
				// La carpeta asignada a cada chat no viaja en lo que manda el
				// panel, así que se conserva la que ya había: si no, guardar
				// desde Ajustes dejaría los archivos del mismo chat repartidos
				// entre la carpeta vieja y una nueva.
				cfg.ListenerChats = config.PreserveFolders(cfg.ListenerChats, parsedChats)
			}
		}
	}
	if v, ok := raw["notification_bot_enabled"]; ok && v != nil {
		if b, ok := v.(bool); ok {
			cfg.NotificationBotEnabled = b
		}
	}
	if v, ok := raw["notification_bot_token"]; ok && v != nil {
		if sVal, ok := v.(string); ok {
			if !strings.Contains(sVal, "*") {
				cfg.NotificationBotToken = sVal
			}
		}
	}
	if v, ok := raw["notification_chat_id"]; ok && v != nil {
		cfg.NotificationChatID = int64(config.ParseInt64(v))
	}
	if v, ok := raw["notification_topic_id"]; ok {
		if v == nil {
			cfg.NotificationTopicID = nil
		} else {
			topicID := int64(config.ParseInt64(v))
			cfg.NotificationTopicID = &topicID
		}
	}
	if v, ok := raw["bot_notifications"]; ok && v != nil {
		if b, ok := v.(bool); ok {
			cfg.BotNotifications = b
		}
	}

	cfg = config.NormalizeConfig(cfg)

	// Desarmar el interruptor con la cuenta atrás en marcha cancela el apagado
	// aquí mismo: si se dejara al temporizador, el panel seguiría mostrando la
	// cuenta atrás hasta que este venciera, aunque ya no fuera a apagar nada.
	shutdownCancelled := false
	if previousCfg.ShutdownWhenDone && !cfg.ShutdownWhenDone && s.shutdownTimer != nil {
		s.shutdownTimer.Stop()
		s.shutdownTimer = nil
		s.shutdownDeadline = time.Time{}
		shutdownCancelled = true
	}

	s.config = cfg
	s.mu.Unlock()

	// El idioma del registro se aplica antes de registrar el cambio, así la
	// línea "Ajustes guardados · Idioma: es → en" ya sale en el idioma recién
	// elegido, que es en el que el usuario está leyendo.
	if cfg.Language != previousCfg.Language {
		i18n.SetLanguage(cfg.Language)
		if s.onLanguageChange != nil {
			s.onLanguageChange()
		}
	}

	if shutdownCancelled {
		logbus.Info(logbus.CatSystem, i18n.T("system.shutdownCancelled"), i18n.T("system.shutdownCancelledSwitch"))
	}

	logConfigChanges(previousCfg, cfg)
	if err := s.storage.SaveConfig(cfg); err != nil {
		log.Printf("[SERVER] %s", i18n.T("common.saveConfigError", err))
	}
	s.downloader.UpdateConfig(cfg)
	s.listener.UpdateConfig(cfg)
	s.restartBotListener()
	s.broadcastState()
	if s.onConfigChange != nil {
		s.onConfigChange(cfg)
	}

	// Solo se enmascara la copia que se serializa en la respuesta: ni s.config
	// ni el cfg que se acaba de guardar en SQLite se tocan (SEC-25). Si el
	// panel recibe el token enmascarado y lo reenvía al guardar, el fallback
	// de notification_bot_token (strings.Contains(sVal, "*")) conserva el real.
	maskedCfg := cfg
	maskedCfg.NotificationBotToken = notifier.MaskToken(cfg.NotificationBotToken)
	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"settings": maskedCfg,
	})
}

// handleNotificationTest envía un mensaje de prueba mediante el bot de notificaciones.
func (s *Server) handleNotificationTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.errorResponse(w, http.StatusMethodNotAllowed, i18n.T("server.methodNotAllowed"))
		return
	}

	var body struct {
		Token   string `json:"token"`
		ChatID  int64  `json:"chat_id"`
		TopicID *int64 `json:"topic_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.invalidData"))
		return
	}

	token := body.Token
	if strings.Contains(token, "*") || strings.TrimSpace(token) == "" {
		s.mu.RLock()
		token = s.config.NotificationBotToken
		s.mu.RUnlock()
	}

	// Validar token
	if err := notifier.ValidateToken(token); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	// Validar chatID
	if err := notifier.ValidateChatID(body.ChatID); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	// Validar topicID si se especifica
	if err := notifier.ValidateTopicID(body.TopicID); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	// Crear notificador y enviar mensaje de prueba
	n := notifier.NewNotifier(token, body.ChatID, body.TopicID)
	testMessage := i18n.T("notifier.testMessage")
	n.SendMessage(context.Background(), testMessage)

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleNotificationDetect detecta el chatID usando getUpdates del Bot API.
func (s *Server) handleNotificationDetect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.errorResponse(w, http.StatusMethodNotAllowed, i18n.T("server.methodNotAllowed"))
		return
	}

	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.invalidData"))
		return
	}

	token := body.Token
	if strings.Contains(token, "*") || strings.TrimSpace(token) == "" {
		s.mu.RLock()
		token = s.config.NotificationBotToken
		s.mu.RUnlock()
	}

	// Validar token
	if err := notifier.ValidateToken(token); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	// Crear notificador temporal para detección
	n := notifier.NewNotifier(token, 0, nil)
	chatID, chatName, err := n.DetectChat(context.Background())
	if err != nil {
		// SEC-10: DetectChat ya entrega el error con el token redactado
		// (notifier.RedactError); se vuelve a aplicar aquí con el token en
		// mano como defensa en profundidad por si el error llega por otro
		// camino. Si el texto ya está limpio, RedactError devuelve el error
		// original sin tocarlo.
		s.errorResponse(w, http.StatusBadRequest, notifier.RedactError(err, token).Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"chat_id":   chatID,
		"chat_name": chatName,
	})
}

func (s *Server) handleSpeedLimit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SpeedLimit *config.SpeedLimit `json:"speed_limit"`
		Value      *float64           `json:"value"`
		Unit       *string            `json:"unit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.invalidData"))
		return
	}

	s.mu.Lock()
	previousCfg := s.config

	// Se parte del límite actual y solo se cambia lo que venga en la petición.
	// La condición anterior («unidad != "" o valor >= 0») era cierta siempre,
	// porque cualquier número no negativo la cumple: un cuerpo con solo la
	// unidad ponía el valor a cero y borraba el límite sin querer.
	speedLim := s.config.SpeedLimit
	if body.SpeedLimit != nil {
		speedLim = *body.SpeedLimit
	}
	if body.Value != nil {
		speedLim.Value = *body.Value
	}
	if body.Unit != nil {
		speedLim.Unit = *body.Unit
	}
	s.config.SpeedLimit = speedLim

	s.config = config.NormalizeConfig(s.config)
	cfg := s.config
	s.mu.Unlock()

	logConfigChanges(previousCfg, cfg)
	if err := s.storage.SaveConfig(cfg); err != nil {
		log.Printf("[SERVER] %s", i18n.T("common.saveConfigError", err))
	}
	s.downloader.UpdateConfig(cfg)
	s.broadcastState()

	// Igual que en handleSettings: el token real ya está en s.config y en
	// SQLite; solo se enmascara la copia que sale en la respuesta (SEC-25).
	maskedCfg := cfg
	maskedCfg.NotificationBotToken = notifier.MaskToken(cfg.NotificationBotToken)
	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"settings": maskedCfg,
	})
}

// Listener Handlers
func (s *Server) handleListenerSettings(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()

	if r.Method == http.MethodGet {
		s.jsonResponse(w, http.StatusOK, map[string]any{
			"status":   "ok",
			"enabled":  cfg.ListenerEnabled,
			"chats":    cfg.ListenerChats,
			"chat_ids": cfg.ListenerChatIDs,
		})
		return
	}

	var raw map[string]any
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.invalidData"))
		return
	}

	s.mu.Lock()
	cfg = s.config
	previousCfg := s.config

	// Enabled: puede venir como "enabled" o "listener_enabled"
	if v, ok := raw["enabled"]; ok && v != nil {
		if b, ok := v.(bool); ok {
			cfg.ListenerEnabled = b
		}
	} else if v, ok := raw["listener_enabled"]; ok && v != nil {
		if b, ok := v.(bool); ok {
			cfg.ListenerEnabled = b
		}
	}

	// Chats: puede venir como "chats", "listener_chats", "chat_ids", o "listener_chat_ids"
	var rawChats any
	if v, ok := raw["chats"]; ok && v != nil {
		rawChats = v
	} else if v, ok := raw["listener_chats"]; ok && v != nil {
		rawChats = v
	}

	if rawChats != nil {
		if chatBytes, err := json.Marshal(rawChats); err == nil {
			var parsedChats []config.ListenerChat
			if err := json.Unmarshal(chatBytes, &parsedChats); err == nil {
				// Igual que en /api/settings: la carpeta ya asignada se conserva
				// aunque quien llame no la mande (la importación de escucha
				// reconstruye cada chat campo a campo).
				cfg.ListenerChats = config.PreserveFolders(cfg.ListenerChats, parsedChats)
			}
		}
	} else {
		var rawIDs any
		if v, ok := raw["chat_ids"]; ok && v != nil {
			rawIDs = v
		} else if v, ok := raw["listener_chat_ids"]; ok && v != nil {
			rawIDs = v
		}
		if rawIDs != nil {
			if idBytes, err := json.Marshal(rawIDs); err == nil {
				var ids []int64
				if err := json.Unmarshal(idBytes, &ids); err == nil {
					// Esta rama solo recibe IDs de chat, sin temas. Un grupo ya
					// configurado conserva TODAS sus entradas (la del grupo entero y
					// las de cada tema): quedarse solo con la primera borraría en
					// silencio los temas vigilados.
					newChats := make([]config.ListenerChat, 0, len(ids))
					for _, id := range ids {
						exists := false
						for _, old := range cfg.ListenerChats {
							if old.ID == id {
								newChats = append(newChats, old)
								exists = true
							}
						}
						if !exists {
							newChats = append(newChats, config.ListenerChat{
								ID:           id,
								Name:         fmt.Sprintf("%d", id),
								AutoDownload: false,
								FPhotos:      true,
								FVideos:      true,
								FAudios:      true,
								FDocs:        true,
								FStickers:    true,
								NameMode:     "manual",
							})
						}
					}
					cfg.ListenerChats = newChats
				}
			}
		}
	}

	cfg = config.NormalizeConfig(cfg)
	s.config = cfg
	s.mu.Unlock()

	logListenerConfigChanges(previousCfg, cfg)
	logbus.Debug(logbus.CatListener,
		i18n.T("listener.configSavedDebug", cfg.ListenerEnabled, len(cfg.ListenerChats)), "")
	if err := s.storage.SaveConfig(cfg); err != nil {
		log.Printf("[SERVER] %s", i18n.T("common.saveConfigError", err))
	}
	s.downloader.UpdateConfig(cfg)
	s.listener.UpdateConfig(cfg)
	s.broadcastState()

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"enabled":  cfg.ListenerEnabled,
		"chats":    cfg.ListenerChats,
		"chat_ids": cfg.ListenerChatIDs,
	})
}

func (s *Server) handleListenerItems(w http.ResponseWriter, r *http.Request) {
	s.jsonResponse(w, http.StatusOK, s.listener.GetItems())
}

func (s *Server) handleListenerDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID     string `json:"id"`
		ItemID string `json:"item_id"`
	}
	decodeOptionalBody(r, &body, "handleListenerDownload")
	id := strings.TrimSpace(body.ID)
	if id == "" {
		id = strings.TrimSpace(body.ItemID)
	}
	if id == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.idRequired"))
		return
	}

	if err := s.listener.DownloadItem(id); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleListenerClear(w http.ResponseWriter, r *http.Request) {
	removed := len(s.listener.GetItems())
	s.listener.ClearItems()
	logbus.Warn(logbus.CatListener, i18n.T("listener.trayCleared", removed), "")
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleListenerReorder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.errorResponse(w, http.StatusMethodNotAllowed, i18n.T("server.methodNotAllowed"))
		return
	}
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.errorResponse(w, http.StatusBadRequest, i18n.T("server.invalidJson"))
		return
	}
	s.listener.ReorderItems(body.IDs)
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleListenerDeleteItem(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID     string `json:"id"`
		ItemID string `json:"item_id"`
	}
	// SEC-38: único handler de los ocho que NO falla con el body vacío: si id
	// queda en "" (ni body ni query), se responde 200 sin borrar nada. Se deja
	// constancia porque es un silencio heredado, pero no se cambia la
	// respuesta: el cliente de la bandeja del listener depende de ese 200.
	decodeOptionalBody(r, &body, "handleListenerDeleteItem")
	id := strings.TrimSpace(body.ID)
	if id == "" {
		id = strings.TrimSpace(body.ItemID)
	}
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	if id != "" {
		s.listener.RemoveItem(id)
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleListenerDeleteItemPath(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/listener/item/")
	if id != "" {
		s.listener.RemoveItem(id)
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleListenerUpdateFilename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.errorResponse(w, http.StatusMethodNotAllowed, i18n.T("server.methodNotAllowed"))
		return
	}

	var body struct {
		ID       string `json:"id"`
		FileName string `json:"file_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.errorResponse(w, http.StatusBadRequest, i18n.T("server.invalidJson"))
		return
	}

	id := strings.TrimSpace(body.ID)
	// La vacíez se comprueba sobre el valor crudo y ANTES de sanitizar:
	// downloader.SanitizeFileName devuelve "archivo" tanto para cadenas vacías
	// como para cadenas de solo caracteres inválidos, así que si se sanitizara
	// primero, rawFileName ya no sería nunca "" y se perdería el 400 actual
	// para un body sin nombre de archivo. Se conserva, por tanto, el orden
	// TrimSpace → comprobación de vacío → SanitizeFileName.
	rawFileName := strings.TrimSpace(body.FileName)
	if id == "" || rawFileName == "" {
		s.errorResponse(w, http.StatusBadRequest, i18n.T("server.idAndFileNameRequired"))
		return
	}

	// Defensa en profundidad (SEC-29): el nombre viene del cliente y se persiste
	// tal cual en SQLite, donde después se registra en log, de modo que unos
	// CR/LF en el valor abrirían una línea nueva falsa (inyección de log). El
	// sink de ruta ya aplica downloader.SanitizeFileName en
	// pkg/downloader/reserve.go al reservar el destino; aquí se aplica la misma
	// limpieza en la entrada para que el valor guardado tampoco lo contenga.
	// SanitizeFileName nunca devuelve cadena vacía (usa "archivo"), así que no
	// hace falta revalidar.
	newFileName := downloader.SanitizeFileName(rawFileName)

	// Actualizar el nombre del archivo en el listener
	if err := s.listener.UpdateItemFileName(id, newFileName); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

// resolveChatPayload arma la ficha de un chat para el panel. Si se pide un
// tema concreto, se añade su número y su título: así la lista de escucha puede
// enseñar "Tema · Grupo" desde el momento en que se añade.
func (s *Server) resolveChatPayload(ctx context.Context, chatID, topicID int64) map[string]any {
	info, _ := s.listener.ResolveChat(ctx, chatID)

	chat := map[string]any{
		"id":            chatID,
		"name":          info.Name,
		"type":          info.Type,
		"username":      info.Username,
		"is_forum":      info.IsForum,
		"auto_download": false,
		"f_photos":      true,
		"f_videos":      true,
		"f_audios":      true,
		"f_docs":        true,
		"f_stickers":    true,
	}

	if topicID > 0 {
		chat["topic_id"] = topicID
		if topicName, err := s.listener.ResolveTopicName(ctx, chatID, topicID); err == nil && strings.TrimSpace(topicName) != "" {
			chat["topic_name"] = topicName
		}
	}

	return chat
}

// topicIDFromQuery lee el tema pedido en la URL. Un valor ausente o inválido
// significa «todo el grupo».
func topicIDFromQuery(r *http.Request) int64 {
	raw := strings.TrimSpace(r.URL.Query().Get("topic_id"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("topic"))
	}
	if raw == "" {
		return 0
	}
	topicID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || topicID < 0 {
		return 0
	}
	return topicID
}

func (s *Server) handleListenerResolveChat(w http.ResponseWriter, r *http.Request) {
	chatIDStr := r.URL.Query().Get("chat_id")
	chatID, err := strconv.ParseInt(chatIDStr, 10, 64)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, i18n.T("server.invalidChatID"))
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status": "ok",
		"chat":   s.resolveChatPayload(r.Context(), chatID, topicIDFromQuery(r)),
	})
}

func (s *Server) handleListenerResolveChatPath(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 {
		s.errorResponse(w, http.StatusBadRequest, i18n.T("server.chatIdRequiredInPath"))
		return
	}

	chatIDStr := parts[3]
	chatID, err := strconv.ParseInt(chatIDStr, 10, 64)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, i18n.T("server.invalidChatID"))
		return
	}

	// El tema puede venir en la ruta (/api/listener/chat/-100.../57) o como
	// parámetro (?topic_id=57).
	topicID := topicIDFromQuery(r)
	if len(parts) >= 5 && strings.TrimSpace(parts[4]) != "" {
		if parsed, perr := strconv.ParseInt(parts[4], 10, 64); perr == nil && parsed > 0 {
			topicID = parsed
		}
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status": "ok",
		"chat":   s.resolveChatPayload(r.Context(), chatID, topicID),
	})
}

// handleListenerTopics devuelve los temas de un grupo para que el panel deje
// elegir cuál vigilar en lugar de escuchar el grupo entero.
func (s *Server) handleListenerTopics(w http.ResponseWriter, r *http.Request) {
	chatIDStr := strings.TrimSpace(r.URL.Query().Get("chat_id"))
	if chatIDStr == "" {
		// También se admite /api/listener/topics/<chat_id>
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) >= 4 {
			chatIDStr = parts[3]
		}
	}

	chatID, err := strconv.ParseInt(chatIDStr, 10, 64)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, i18n.T("server.invalidChatID"))
		return
	}

	topics, err := s.listener.ResolveTopics(r.Context(), chatID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"chat_id":  chatID,
		"is_forum": len(topics) > 0,
		"topics":   topics,
	})
}

// Filesystem & System
// handleTheme devuelve únicamente los índices de color del panel. Es el único
// endpoint que responde sin token (ver publicAPIPath): la pantalla de acceso
// remoto se pinta antes de que nadie se haya autenticado, y un número de color
// no dice nada de la cuenta ni de las descargas. Solo lectura, sin efectos.
func (s *Server) handleTheme(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.errorResponse(w, http.StatusMethodNotAllowed, i18n.T("server.methodNotAllowed"))
		return
	}

	s.mu.RLock()
	colorID := s.config.ColorID
	loaderColorID := s.config.LoaderColorID
	s.mu.RUnlock()

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status":          "ok",
		"color_id":        colorID,
		"loader_color_id": loaderColorID,
	})
}

// SetFolderPicker registra la función que abre el diálogo nativo de selección
// de carpetas. Solo la aplicación de escritorio puede aportarla: en modo
// servidor (sin ventana) queda a nil y el panel recurre a su propio explorador.
func (s *Server) SetFolderPicker(fn func() (string, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.folderPicker = fn
}


// SetLanguageChangeHandler registra la función que se ejecuta cuando el idioma cambia en ajustes.
func (s *Server) SetLanguageChangeHandler(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onLanguageChange = fn
}

// SetConfigChangeHandler registra la función que se ejecuta cuando la configuración cambia en ajustes.
func (s *Server) SetConfigChangeHandler(fn func(config.Config)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onConfigChange = fn
}

// GetConfig devuelve una copia por valor de la configuración viva del
// servidor. Server.config es la única fuente de verdad: la actualizan los
// handlers de guardado (handleSettings, handleSpeedLimit, handleListenerSettings)
// bajo candado, así que quien necesite leerla de fuera —por ejemplo el diálogo
// nativo de carpetas de la app— debe preguntar aquí en lugar de conservar su
// propia copia, que quedaría congelada en el valor inicial de la sesión.
func (s *Server) GetConfig() config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config
}

// isLocalRequest indica si la petición viene del propio equipo. Exige las dos
// cosas a la vez —que la dirección del cliente sea de loopback y que el panel
// se esté viendo en una dirección local— para que un túnel remoto montado en
// la misma máquina, que llegaría como 127.0.0.1, no cuente como local.
func isLocalRequest(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	if host != "127.0.0.1" && host != "localhost" && host != "::1" &&
		!strings.HasSuffix(host, "wails.localhost") {
		return false
	}

	remote := r.RemoteAddr
	if h, _, err := net.SplitHostPort(remote); err == nil {
		remote = h
	}
	ip := net.ParseIP(strings.Trim(remote, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return false
	}

	// Un túnel (Cloudflare Tunnel, ngrok, un proxy inverso) corre en esta misma
	// máquina, así que su conexión también sale de loopback y hasta aquí pasaba
	// por local. Todos ellos añaden alguna de estas cabeceras al reenviar, así
	// que su presencia significa que la petición viene de fuera.
	for _, cabecera := range []string{
		"X-Forwarded-For", "X-Real-Ip", "X-Forwarded-Host", "X-Forwarded-Proto",
		"Cf-Connecting-Ip", "Cf-Ray", "Forwarded",
	} {
		if r.Header.Get(cabecera) != "" {
			return false
		}
	}

	return true
}

// handleFSPick abre el diálogo nativo de carpetas del sistema y devuelve la
// ruta elegida. Solo responde a peticiones hechas desde el propio equipo: así
// el panel se ve igual tanto dentro de la app como en un navegador local, y
// desde un dispositivo remoto nunca se abre una ventana en el ordenador.
func (s *Server) handleFSPick(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.errorResponse(w, http.StatusMethodNotAllowed, i18n.T("server.methodNotAllowed"))
		return
	}
	if !isLocalRequest(r) {
		s.errorResponse(w, http.StatusForbidden, i18n.T("server.systemPickerLocalOnly"))
		return
	}

	s.mu.RLock()
	pick := s.folderPicker
	s.mu.RUnlock()

	if pick == nil {
		s.errorResponse(w, http.StatusNotImplemented, i18n.T("server.noAppWindowForPicker"))
		return
	}

	path, err := pick()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Una ruta vacía significa que se cerró el diálogo sin elegir nada.
	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status": "ok",
		"path":   path,
	})
}

func (s *Server) handleFSBrowse(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("path")
	if target == "" {
		roots := make([]string, 0)
		if runtime.GOOS == "windows" {
			for _, drive := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ" {
				d := fmt.Sprintf("%c:\\", drive)
				if _, err := os.Stat(d); err == nil {
					roots = append(roots, d)
				}
			}
		} else {
			roots = append(roots, "/")
		}
		s.jsonResponse(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"roots":   roots,
			"path":    nil,
			"parent":  nil,
			"entries": []any{},
		})
		return
	}

	cleanPath, err := filepath.Abs(target)
	if err != nil {
		cleanPath = target
	}

	entries, err := os.ReadDir(cleanPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, i18n.T("server.cannotReadDirectory"))
		return
	}

	type DirItem struct {
		Name  string `json:"name"`
		Path  string `json:"path"`
		IsDir bool   `json:"is_dir"`
	}

	items := make([]DirItem, 0)
	for _, e := range entries {
		if e.IsDir() {
			items = append(items, DirItem{
				Name:  e.Name(),
				Path:  filepath.Join(cleanPath, e.Name()),
				IsDir: true,
			})
		}
	}

	parent := filepath.Dir(cleanPath)
	var parentPath *string
	if parent != cleanPath {
		parentPath = &parent
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"roots":   []string{},
		"path":    cleanPath,
		"parent":  parentPath,
		"entries": items,
	})
}

func (s *Server) handleSystemDisk(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	folder := s.config.DownloadFolder
	s.mu.RUnlock()

	disk, err := downloader.GetDiskUsage(folder, s.downloader.GetDownloads()...)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, disk)
}

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	s.jsonResponse(w, http.StatusOK, map[string]any{
		"host":    config.GetServerHost(),
		"port":    config.GetServerPort(),
		"version": config.AppVersion,
	})
}

// Handlers del Escáner / Histórico
func (s *Server) handleScannerSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.errorResponse(w, http.StatusMethodNotAllowed, i18n.T("server.methodNotAllowed"))
		return
	}

	var req scanner.ScanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.invalidData"))
		return
	}

	res, err := s.scanner.Scan(r.Context(), req)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, res)
}

func (s *Server) handleScannerDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.errorResponse(w, http.StatusMethodNotAllowed, i18n.T("server.methodNotAllowed"))
		return
	}

	var body struct {
		Items []scanner.ScanItem `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Items) == 0 {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.atLeastOneItem"))
		return
	}

	queued := s.scanner.QueueItems(body.Items)
	s.broadcastState()

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status": "ok",
		"queued": queued,
	})
}

func (s *Server) handleScannerResolveTarget(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target string `json:"target"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	if strings.TrimSpace(body.Target) == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.targetRequired"))
		return
	}

	res, err := s.scanner.ResolveTarget(r.Context(), body.Target)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, res)
}

func (s *Server) handleScannerRecents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.errorResponse(w, http.StatusMethodNotAllowed, i18n.T("server.methodNotAllowed"))
		return
	}

	recents, err := s.storage.GetRecentChats()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, recents)
}

func (s *Server) handleScannerRecentFavorite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.errorResponse(w, http.StatusMethodNotAllowed, i18n.T("server.methodNotAllowed"))
		return
	}

	var body struct {
		ID         int64 `json:"id"`
		IsFavorite bool  `json:"is_favorite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == 0 {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.invalidData"))
		return
	}

	if err := s.storage.ToggleRecentChatFavorite(body.ID, body.IsFavorite); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleScannerRecentDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		s.errorResponse(w, http.StatusMethodNotAllowed, i18n.T("server.methodNotAllowed"))
		return
	}

	var id int64
	if r.Method == http.MethodDelete {
		if q := r.URL.Query().Get("id"); q != "" {
			id, _ = strconv.ParseInt(q, 10, 64)
		}
	}
	if id == 0 {
		var body struct {
			ID int64 `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		id = body.ID
	}

	if id == 0 {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.invalidData"))
		return
	}

	if err := s.storage.DeleteRecentChat(id); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleScannerRecentClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		s.errorResponse(w, http.StatusMethodNotAllowed, i18n.T("server.methodNotAllowed"))
		return
	}

	if err := s.storage.ClearRecentChats(); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}


// Updater
func (s *Server) handleCheckUpdate(w http.ResponseWriter, r *http.Request) {
	rel, asset, err := s.updater.CheckForUpdate()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}

	if rel != nil && asset != nil {
		s.mu.Lock()
		s.latestRel = rel
		s.mu.Unlock()
		s.jsonResponse(w, http.StatusOK, map[string]any{
			"update_available": true,
			"latest":           rel.TagName,
			"version":          rel.TagName,
			"current":          config.AppVersion,
			"size_bytes":       asset.Size,
			"changelog":        rel.Body,
		})
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"update_available": false,
		"latest":           nil,
		"current":          config.AppVersion,
		"size_bytes":       0,
	})
}

func (s *Server) handleUpdateProgress(w http.ResponseWriter, r *http.Request) {
	s.jsonResponse(w, http.StatusOK, s.updater.GetProgress())
}

func (s *Server) handleInstallUpdate(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	rel := s.latestRel
	s.mu.RUnlock()

	var err error
	if rel == nil {
		rel, _, err = s.updater.CheckForUpdate()
		if err != nil || rel == nil {
			s.errorResponse(w, http.StatusBadRequest, i18n.T("server.noUpdatesAvailable"))
			return
		}
	}

	if err := s.updater.InstallUpdate(rel); err != nil {
		logbus.Error(logbus.CatUpdater, i18n.T("updater.installStartError"), err.Error())
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	logbus.Info(logbus.CatUpdater, i18n.T("updater.installing", rel.TagName),
		i18n.T("updater.installingDetail", config.AppVersion))

	s.jsonResponse(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": i18n.T("server.startingInstall"),
	})
}

func (s *Server) handlePostponeUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Version == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.versionRequired"))
		return
	}

	s.updater.Postpone(body.Version)
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleExit(w http.ResponseWriter, r *http.Request) {
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
	go func() {
		time.Sleep(500 * time.Millisecond)
		if s.exitCallback != nil {
			s.exitCallback()
		} else {
			os.Exit(0)
		}
	}()
}

// handleIPCDownload recibe URLs de descarga desde una segunda instancia del
// programa (línea de comandos). No exige token porque el CLI no lo tiene, pero
// sí que la petición sea loopback con la cabecera propia (X-TGDL-Request) o
// que traiga un token válido: así se cierra el encolado ilimitado desde la
// LAN, un túnel o una página web (SEC-03).
func (s *Server) handleIPCDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.errorResponse(w, http.StatusMethodNotAllowed, i18n.T("server.methodNotAllowed"))
		return
	}

	// Sin token válido solo pasa el CLI del propio equipo, con la misma forma
	// que manda main.go. Con token, todo el resto del handler funciona como
	// siempre.
	if !s.checkAuth(r) {
		if !isLocalRequest(r) {
			logbus.Warn(logbus.CatServer,
				i18n.T("server.ipcRemoteRejected"),
				i18n.T("server.requestOriginDetail", requestOrigin(r)))
			s.errorResponse(w, http.StatusUnauthorized, i18n.T("server.ipcLocalOnly"))
			return
		}
		if r.Header.Get(ownRequestHeader) == "" {
			s.errorResponse(w, http.StatusForbidden, i18n.T("server.requestForbidden"))
			return
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			s.errorResponse(w, http.StatusUnsupportedMediaType, i18n.T("server.requestUnsupportedType"))
			return
		}
		// 30 intentos por minuto y origen: sin esto, repetir la petición
		// bastaría para llenar la cola sin límite (SEC-03).
		origin := requestOrigin(r)
		permitido, retry := s.consumeIPCQuota(origin)
		if !permitido {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			s.jsonResponse(w, http.StatusTooManyRequests, map[string]any{
				"status":      "rate_limited",
				"detail":      i18n.T("server.rateLimitedDetail", retry),
				"retry_after": retry,
			})
			return
		}
	}

	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.URL) == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, i18n.T("server.urlRequired"))
		return
	}

	if err := s.EnqueueURL(r.Context(), body.URL); err != nil {
		// SEC-21: la cola llena también se responde aquí con 429 + Retry-After;
		// el resto de errores sigue con el 400 de siempre.
		if errors.Is(err, ErrQueueFull) {
			s.respondQueueFull(w)
			return
		}
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": i18n.T("server.ipcDownloadAdded"),
	})
}

// ---------------------------------------------------------------------------
// Apagado del equipo al terminar la cola de descargas
//
// El interruptor es el ajuste config.ShutdownWhenDone. La mecánica:
// los eventos del motor con estado «queued» o «downloading» marcan que hubo
// actividad (y cancelan cualquier cuenta atrás en marcha), y los eventos de
// estado terminal disparan una evaluación: si la cola ya no tiene nada activo
// y antes sí lo tuvo, se programa el apagado con shutdownDelay de cortesía.
// ---------------------------------------------------------------------------

// watchShutdownWhenDone se llama con cada cambio de estado del motor. El
// camino barato (actividad) solo toca una marca; el de estado terminal es el
// único que mira la cola entera, así un tick de progreso nunca recorre la
// lista de descargas.
func (s *Server) watchShutdownWhenDone(item storage.DownloadItem) {
	switch item.Status {
	case "queued", "downloading":
		s.mu.Lock()
		s.hadActiveDownloads = true
		cancelled := s.shutdownTimer != nil
		if cancelled {
			s.shutdownTimer.Stop()
			s.shutdownTimer = nil
			s.shutdownDeadline = time.Time{}
		}
		s.mu.Unlock()
		if cancelled {
			logbus.Info(logbus.CatSystem, i18n.T("system.shutdownCancelled"), i18n.T("system.shutdownCancelledActivity"))
		}
	case "completed", "failed", "cancelled", "skipped":
		s.maybeScheduleShutdown()
	}
}

// maybeScheduleShutdown decide si toca programar el apagado o enviar notificación.
// Hace falta que el interruptor esté armado o que las notificaciones estén activadas,
// y que la cola se haya quedado vacía tras haber tenido actividad.
func (s *Server) maybeScheduleShutdown() {
	s.mu.RLock()
	armed := s.config.ShutdownWhenDone
	botNotifications := s.config.BotNotifications
	s.mu.RUnlock()
	if !armed && !botNotifications {
		return
	}

	// Las cifras del resumen son las de la sesión en curso (desde el arranque
	// de la aplicación): el historial restaurado de SQLite no cuenta, o un
	// reinicio heredaría las descargas de hace días y el aviso diría que se
	// bajaron archivos que no se han bajado ahora.
	stats := s.downloader.GetSessionStats()
	completed := stats.Completed
	failed := stats.Failed

	active := false
	for _, d := range s.downloader.GetDownloads() {
		if d.Status == "queued" || d.Status == "downloading" {
			active = true
			break
		}
	}

	s.mu.Lock()
	if active {
		s.hadActiveDownloads = true
		s.mu.Unlock()
		return
	}
	if !s.hadActiveDownloads || s.shutdownTimer != nil {
		s.mu.Unlock()
		return
	}
	s.hadActiveDownloads = false

	// Enviar notificación de resumen si está configurado
	if botNotifications && (completed > 0 || failed > 0) {
		failedItems := s.sessionFailedItems(stats)
		s.mu.Unlock()
		message := i18n.T("notifier.queueComplete", completed, failed)
		if failed > 0 && len(failedItems) > 0 {
			failedText := strings.Join(failedItems, "\n")
			message += i18n.T("notifier.queueCompleteError", failedText)
		}
		s.downloader.SendNotification(message)
		s.mu.Lock()
	}

	if armed {
		s.shutdownDeadline = time.Now().Add(shutdownDelay)
		s.shutdownTimer = time.AfterFunc(shutdownDelay, s.executeScheduledShutdown)
		s.mu.Unlock()

		logbus.Warn(logbus.CatSystem,
			i18n.T("system.shutdownScheduled", int(shutdownDelay.Seconds())),
			i18n.T("system.shutdownScheduledHint"))
		s.triggerBroadcast()
	} else {
		s.mu.Unlock()
	}
}

// sessionFailedItems construye el detalle (nombre y enlace) de las descargas
// fallidas en esta sesión para el resumen del bot. Solo entran las de ahora:
// las fallidas que trae el historial ya se avisaron en su día.
func (s *Server) sessionFailedItems(stats downloader.SessionStats) []string {
	if len(stats.FailedIDs) == 0 {
		return nil
	}
	byID := make(map[string]storage.DownloadItem)
	for _, d := range s.downloader.GetDownloads() {
		byID[d.ID] = d
	}

	items := make([]string, 0, len(stats.FailedIDs))
	for _, id := range stats.FailedIDs {
		d, ok := byID[id]
		if !ok {
			continue
		}
		link := ""
		if strings.HasPrefix(d.Source, "http://") || strings.HasPrefix(d.Source, "https://") {
			link = d.Source
		} else if d.MessageID > 0 && d.ChatID != 0 {
			chatIDStr := strconv.FormatInt(d.ChatID, 10)
			if strings.HasPrefix(chatIDStr, "-100") {
				chatIDStr = strings.TrimPrefix(chatIDStr, "-100")
			} else if strings.HasPrefix(chatIDStr, "-") {
				chatIDStr = strings.TrimPrefix(chatIDStr, "-")
			}
			link = fmt.Sprintf("https://t.me/c/%s/%d", chatIDStr, d.MessageID)
		}

		fileName := html.EscapeString(d.FileName)
		if link != "" {
			escapedLink := html.EscapeString(link)
			if fileName != "" && fileName != link {
				items = append(items, fmt.Sprintf("• <b>%s</b>\n  <code>%s</code>", fileName, escapedLink))
			} else {
				items = append(items, fmt.Sprintf("• <code>%s</code>", escapedLink))
			}
		} else if fileName != "" {
			items = append(items, fmt.Sprintf("• <b>%s</b>", fileName))
		}
	}
	return items
}

// executeScheduledShutdown es la cuenta atrás vencida. El interruptor y la cola
// se vuelven a comprobar aquí porque pudieron cambiar durante la espera: si el
// usuario desarmó el interruptor o encoló algo en el último momento, no se
// apaga nada.
func (s *Server) executeScheduledShutdown() {
	s.mu.Lock()
	s.shutdownTimer = nil
	s.shutdownDeadline = time.Time{}
	armed := s.config.ShutdownWhenDone
	s.mu.Unlock()
	s.triggerBroadcast()

	if !armed {
		return
	}
	for _, d := range s.downloader.GetDownloads() {
		if d.Status == "queued" || d.Status == "downloading" {
			return
		}
	}

	logbus.Warn(logbus.CatSystem, i18n.T("system.shuttingDown"), i18n.T("system.shuttingDownDetail"))
	if err := shutdownSystem(); err != nil {
		logbus.Error(logbus.CatSystem, i18n.T("system.shutdownError"), err.Error())
	}
}

// ---------------------------------------------------------------------------
// Listener interactivo para bot de Telegram (comandos y recepción de enlaces)
// ---------------------------------------------------------------------------

var telegramURLRegex = regexp.MustCompile(`(?i)(?:https?://)?(?:[a-zA-Z0-9-]+\.)*(?:t|telegram)\.me/[^\s]+`)

func extractTelegramLinks(text string) []string {
	matches := telegramURLRegex.FindAllString(text, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var result []string
	for _, m := range matches {
		m = strings.TrimRight(m, ".,;:!?)>]}")
		if !seen[m] {
			seen[m] = true
			result = append(result, m)
		}
	}
	return result
}

func (s *Server) restartBotListener() {
	s.mu.Lock()
	if s.botCancel != nil {
		s.botCancel()
		s.botCancel = nil
	}

	cfg := s.config
	if !cfg.NotificationBotEnabled || cfg.NotificationBotToken == "" || cfg.NotificationChatID == 0 {
		s.mu.Unlock()
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.botCancel = cancel
	s.mu.Unlock()

	go s.runBotListener(ctx, cfg.NotificationBotToken, cfg.NotificationChatID, cfg.NotificationTopicID)
}

func (s *Server) runBotListener(ctx context.Context, token string, authorizedChatID int64, topicID *int64) {
	client := &http.Client{Timeout: 35 * time.Second}
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates", token)

	offset := 0

	// Obtener el último update_id al arrancar para no procesar mensajes antiguos acumulados
	initReqURL := fmt.Sprintf("%s?offset=-1&limit=1", apiURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, initReqURL, nil)
	if err == nil {
		resp, err := client.Do(req)
		if err == nil {
			var initRes struct {
				OK     bool `json:"ok"`
				Result []struct {
					UpdateID int `json:"update_id"`
				} `json:"result"`
			}
			if json.NewDecoder(resp.Body).Decode(&initRes) == nil && initRes.OK && len(initRes.Result) > 0 {
				offset = initRes.Result[0].UpdateID + 1
			}
			resp.Body.Close()
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		pollURL := fmt.Sprintf("%s?offset=%d&timeout=20", apiURL, offset)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}

		resp, err := client.Do(req)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}

		var res struct {
			OK     bool `json:"ok"`
			Result []struct {
				UpdateID int `json:"update_id"`
				Message  *struct {
					MessageID       int64  `json:"message_id"`
					MessageThreadID *int64 `json:"message_thread_id,omitempty"`
					From            struct {
						ID int64 `json:"id"`
					} `json:"from"`
					Chat struct {
						ID int64 `json:"id"`
					} `json:"chat"`
					Text string `json:"text,omitempty"`
				} `json:"message,omitempty"`
			} `json:"result"`
		}

		decodeErr := json.NewDecoder(resp.Body).Decode(&res)
		resp.Body.Close()

		if decodeErr != nil || !res.OK {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}

		for _, up := range res.Result {
			if up.UpdateID >= offset {
				offset = up.UpdateID + 1
			}

			if up.Message == nil || up.Message.Chat.ID != authorizedChatID {
				continue
			}

			text := strings.TrimSpace(up.Message.Text)
			if text == "" {
				continue
			}

			var replyThreadID *int64
			if up.Message.MessageThreadID != nil && *up.Message.MessageThreadID > 0 {
				replyThreadID = up.Message.MessageThreadID
			} else {
				replyThreadID = topicID
			}

			n := notifier.NewNotifier(token, authorizedChatID, replyThreadID)

			if text == "/start" || text == "/help" {
				replyText := i18n.T("notifier.botStart")
				n.SendMessage(ctx, replyText)
				continue
			}

			candidates := extractTelegramLinks(text)
			if len(candidates) == 0 {
				replyText := i18n.T("notifier.botInvalidLink")
				n.SendMessage(ctx, replyText)
				continue
			}

			var added []string
			var failedErrs []string

			for _, rawURL := range candidates {
				err := s.EnqueueURL(ctx, rawURL)
				if err == nil {
					added = append(added, rawURL)
					logbus.Info(logbus.CatDownloads, i18n.T("server.ipcDownloadReceived", rawURL), "Bot Telegram")
				} else {
					failedErrs = append(failedErrs, fmt.Sprintf("• <code>%s</code>: <i>%s</i>", html.EscapeString(rawURL), html.EscapeString(err.Error())))
				}
			}

			if len(added) > 0 {
				addedLinks := make([]string, len(added))
				for i, l := range added {
					addedLinks[i] = "• <code>" + html.EscapeString(l) + "</code>"
				}
				replyText := i18n.T("notifier.botAddedQueue", strings.Join(addedLinks, "\n"))
				if len(failedErrs) > 0 {
					replyText += i18n.T("notifier.botErrorsHeader", strings.Join(failedErrs, "\n"))
				}
				n.SendMessage(ctx, replyText)
			} else if len(failedErrs) > 0 {
				replyText := i18n.T("notifier.botProcessError", strings.Join(failedErrs, "\n"))
				n.SendMessage(ctx, replyText)
			}
		}
	}
}
