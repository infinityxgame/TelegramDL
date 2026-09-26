package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"tgdown/pkg/config"
	"tgdown/pkg/downloader"
	"tgdown/pkg/listener"
	"tgdown/pkg/logbus"
	"tgdown/pkg/storage"
	"tgdown/pkg/telegram"
	"tgdown/pkg/updater"
)

type Server struct {
	clientMgr  *telegram.ClientManager
	storage    *storage.Storage
	downloader *downloader.Engine
	listener   *listener.ListenerEngine
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
	broadcastCh  chan struct{}
	onBroadcast  func(snap map[string]any)
	apiToken     string
	folderPicker func() (string, error)
	// tokenSendNextAt es el momento a partir del cual se vuelve a admitir un
	// envío del token a Telegram (ver tokenSendCooldown).
	tokenSendNextAt time.Time
	// tokenSendPorIP limita además por origen. El límite global por sí solo
	// dejaba que cualquiera en la red llenase los mensajes guardados del
	// usuario a razón de uno por minuto, día y noche.
	tokenSendPorIP map[string]time.Time
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
				return origin == "" || origenPermitido(origin, r)
			},
		},
		exitCallback:   exitCb,
		mux:            http.NewServeMux(),
		stopCh:         make(chan struct{}),
		broadcastCh:    make(chan struct{}, 1),
		tokenSendPorIP: make(map[string]time.Time),
	}

	s.apiToken = s.loadOrCreateToken()

	s.registerRoutes(s.mux)

	// Escuchar cambios de estado en el motor de descargas para emitir a los WebSockets
	dl.OnStateChange(func(item storage.DownloadItem) {
		s.triggerBroadcast()
	})

	// Escuchar cambios de estado en el motor de escucha para emitir a los WebSockets
	le.OnStateChange(func(item listener.ListenerItem) {
		s.triggerBroadcast()
	})

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

	// authAPIPrefix agrupa los endpoints de configuración de Telegram que
	// necesitan ser públicos para permitir la configuración inicial sin token.
	// Estos endpoints no revelan información sensible, solo permiten configurar
	// la cuenta de Telegram. El abuso se limita por las protecciones de Telegram
	// (rate limiting, código de verificación, 2FA).
	authAPIPrefix = "/api/auth/"

	// tokenSendCooldown es lo que hay que esperar entre dos envíos del token.
	// Al ser un endpoint abierto, es lo que evita que alguien que alcance el
	// puerto llene de mensajes los guardados del usuario.
	tokenSendCooldown = time.Minute

	// tokenSendFailureCooldown es la espera cuando el envío falla. Es corta a
	// propósito: si la sesión de Telegram estaba caída, el usuario debe poder
	// reintentar en cuanto la arregle, sin dejar por ello la puerta abierta a
	// reintentos sin freno.
	tokenSendFailureCooldown = 10 * time.Second

	// tokenSendCooldownPorIP es la espera que se aplica a cada origen por
	// separado, por encima del límite global.
	tokenSendCooldownPorIP = 15 * time.Minute

	// cabeceraPeticionPropia la pone el panel en los endpoints que responden sin
	// token. No es un secreto: su única función es que el navegador tenga que
	// hacer el preflight de CORS, que es lo que corta una petición lanzada
	// desde otra página web.
	cabeceraPeticionPropia = "X-TGDL-Request"
)

// publicAPIPaths son los únicos endpoints de la API que responden sin token.
var publicAPIPaths = map[string]bool{
	publicAPIPath:         true,
	tokenSendAPIPath:      true,
	hasCredentialsAPIPath: true,
}

// isPublicAPIPath determina si un endpoint puede responder sin token.
// Los endpoints de configuración de Telegram (/api/auth/credentials, /api/auth/send-code,
// /api/auth/verify-code, /api/auth/verify-2fa, /api/auth/status, /api/auth/logout)
// son públicos para permitir la configuración inicial sin token.
// Los endpoints de token (/api/auth/token, /api/auth/token/regenerate) requieren autenticación.
func isPublicAPIPath(path string) bool {
	if publicAPIPaths[path] {
		return true
	}
	// Permitir endpoints de configuración de autenticación (pero no los de token)
	if strings.HasPrefix(path, authAPIPrefix) {
		// Proteger endpoints de token
		if path == "/api/auth/token" || path == "/api/auth/token/regenerate" {
			return false
		}
		return true
	}
	return false
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if isPublicAPIPath(r.URL.Path) {
				s.corsMiddleware(s.mux).ServeHTTP(w, r)
				return
			}
			s.corsMiddleware(s.authMiddleware(s.mux)).ServeHTTP(w, r)
			return
		}
		// Para cualquier ruta de frontend, devolver 404 para que Wails AssetServer
		// sirva index.html e inyecte los bindings e IPC nativos.
		http.NotFound(w, r)
	})
}

func (s *Server) WebHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if isPublicAPIPath(r.URL.Path) {
				s.corsMiddleware(s.mux).ServeHTTP(w, r)
				return
			}
			s.corsMiddleware(s.authMiddleware(s.mux)).ServeHTTP(w, r)
			return
		}
		// El panel estático (HTML/JS/CSS) se sirve siempre sin autenticación:
		// la pantalla de login remoto necesita poder cargar antes de tener token.
		s.mux.ServeHTTP(w, r)
	})
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

	s.httpServer = &http.Server{
		Handler: s.WebHandler(),
		// Solo se limita la lectura de cabeceras y el tiempo ocioso. Ni
		// ReadTimeout ni WriteTimeout: cortarían los WebSocket, que son
		// conexiones largas por definición.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

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
	if s.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.httpServer.Shutdown(ctx)
	}
}

// origenPermitido decide si una petición que llega con cabecera Origin puede
// tocar la API. Son dos casos:
//
//  1. El panel se sirve desde la misma dirección y puerto que la API. Esto es
//     lo normal, y cubre el acceso remoto entero —el móvil entrando por
//     192.168.1.40:8000, un nombre de Tailscale, un túnel— sin tener que
//     enumerar direcciones de antemano.
//
//  2. El panel corre en este mismo equipo con otra dirección: la ventana de
//     Wails (wails.localhost), un navegador local, o el servidor de desarrollo
//     de Vite, que usa otro puerto.
//
// Antes esto era una lista fija de nombres (localhost, 127.0.0.1 y el host
// configurado). Como la dirección de escucha por defecto es 0.0.0.0, desde
// cualquier IP real quedaban fuera los dos: el navegador manda
// «Origin: http://192.168.1.40:8000», no coincidía con nada, y el WebSocket se
// rechazaba con un 403. El panel cargaba pero no recibía estado nunca y se
// quedaba reintentando la conexión cada dos segundos.
func origenPermitido(origin string, r *http.Request) bool {
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

	// Mismo origen: el panel y la API entraron por la misma puerta.
	if r != nil && r.Host != "" && strings.EqualFold(parsed.Host, r.Host) {
		return true
	}

	return esEquipoLocal(parsed.Host)
}

// esEquipoLocal indica si el host de una URL apunta a esta misma máquina.
func esEquipoLocal(hostPuerto string) bool {
	host := hostPuerto
	if h, _, err := net.SplitHostPort(hostPuerto); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))

	if host == "localhost" || host == "wails.localhost" || strings.HasSuffix(host, ".wails.localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origenPermitido(origin, r) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, "+cabeceraPeticionPropia)
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
			s.errorResponse(w, http.StatusUnauthorized, "Token de acceso inválido o ausente")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// wsSubprotocol es el subprotocolo que marca una conexión de TelegramDL. El
// cliente ofrece dos: este y, como segundo, el token.
const wsSubprotocol = "tgdl-v1"

// tokenDelSubprotocolo saca el token de la cabecera Sec-WebSocket-Protocol.
func tokenDelSubprotocolo(r *http.Request) string {
	for _, cabecera := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, parte := range strings.Split(cabecera, ",") {
			parte = strings.TrimSpace(parte)
			if parte != "" && parte != wsSubprotocol {
				return parte
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
		provided = tokenDelSubprotocolo(r)
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
			log.Printf("[SERVER] error guardando token de API en BD: %v", err)
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
// reconfigurarse con el nuevo valor.
func (s *Server) RegenerateToken() string {
	tok, err := config.GenerateToken()
	if err != nil {
		tok = config.NewID()
	}
	s.mu.Lock()
	s.apiToken = tok
	s.mu.Unlock()
	if s.storage != nil {
		if err := s.storage.SaveAPIToken(tok); err != nil {
			log.Printf("[SERVER] error guardando token de API en BD: %v", err)
		}
	}
	return tok
}

func (s *Server) jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("[SERVER] error codificando respuesta JSON: %v", err)
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
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Si el cliente ofreció subprotocolos hay que devolverle uno, o el navegador
	// rechaza la conexión. Se responde siempre el nuestro, nunca el token.
	var cabeceras http.Header
	if r.Header.Get("Sec-WebSocket-Protocol") != "" {
		cabeceras = http.Header{"Sec-WebSocket-Protocol": []string{wsSubprotocol}}
	}

	conn, err := s.upgrader.Upgrade(w, r, cabeceras)
	if err != nil {
		return
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
	s.mu.RUnlock()

	var updateInfo any = nil
	if latest != nil {
		updateInfo = map[string]any{
			"available": true,
			"version":   latest.TagName,
			"changelog": latest.Body,
		}
	}

	return map[string]any{
		"type":         "state",
		"downloads":    downloads,
		"listener":     listenerItems,
		"settings":     cfg,
		"active_count": activeCount,
		"queued_count": queuedCount,
		"speed_total":  config.FormatBytes(float64(speedBytes)) + "/s",
		"speed_bytes":  speedBytes,
		"disk":         disk,
		"logs_seq":     logbus.LastID(),
		"update":       updateInfo,
		"server_time":  float64(time.Now().Unix()),
	}
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
	s.mu.RUnlock()

	s.jsonResponse(w, http.StatusOK, st)
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

	// Devolvemos si la app está completamente configurada (credenciales + sesión)
	s.jsonResponse(w, http.StatusOK, map[string]bool{
		"has_credentials":    hasCreds,
		"has_active_session": hasActiveSession,
		"is_configured":      hasCreds && hasActiveSession,
	})
}

func (s *Server) handleAuthCredentials(w http.ResponseWriter, r *http.Request) {
	var body struct {
		APIID   string `json:"api_id"`
		APIHash string `json:"api_hash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.errorResponse(w, http.StatusUnprocessableEntity, "Datos inválidos")
		return
	}

	// La base de datos es la única fuente de verdad para las credenciales. Antes
	// se escribían además en un .env, y el fallo que se daba por bueno era el de
	// la base de datos: si esto falla no queda ningún otro sitio de donde
	// recuperarlas, así que ahora sí se corta aquí.
	if err := s.storage.SaveCredentials(body.APIID, body.APIHash); err != nil {
		log.Printf("[SERVER] error guardando credenciales en BD: %v", err)
		s.errorResponse(w, http.StatusInternalServerError, "Error guardando credenciales")
		return
	}

	if err := s.clientMgr.InitClient(body.APIID, body.APIHash); err != nil {
		log.Printf("[SERVER] error inicializando cliente Telegram: %v", err)
		s.errorResponse(w, http.StatusInternalServerError, "Error inicializando cliente Telegram")
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
		s.errorResponse(w, http.StatusUnprocessableEntity, "Datos inválidos")
		return
	}
	phone := strings.TrimSpace(body.Phone)
	if phone == "" {
		phone = strings.TrimSpace(body.PhoneNumber)
	}
	if phone == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, "Teléfono requerido")
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
		s.errorResponse(w, http.StatusUnprocessableEntity, "Datos inválidos")
		return
	}
	phone := strings.TrimSpace(body.Phone)
	if phone == "" {
		phone = strings.TrimSpace(body.PhoneNumber)
	}
	code := strings.TrimSpace(body.Code)
	if code == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, "Código requerido")
		return
	}

	status, err := s.clientMgr.VerifyCode(r.Context(), phone, code, body.PhoneCodeHash)
	if err != nil {
		logbus.Error(logbus.CatTelegram, "Código de verificación rechazado", err.Error())
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if status == "ok" {
		logbus.Success(logbus.CatTelegram, "Sesión de Telegram iniciada", "")
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
		s.errorResponse(w, http.StatusUnprocessableEntity, "Contraseña requerida")
		return
	}

	if err := s.clientMgr.Verify2FA(r.Context(), body.Password); err != nil {
		logbus.Error(logbus.CatTelegram, "Contraseña de dos pasos rechazada", err.Error())
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	logbus.Success(logbus.CatTelegram, "Sesión de Telegram iniciada (verificación en dos pasos)", "")

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

// registrarEnvioPorIP anota cuándo puede volver a pedir el token este origen y
// aprovecha para limpiar las entradas ya caducadas, de modo que el mapa no
// crezca sin límite si alguien insiste desde muchas direcciones.
//
// Debe llamarse con s.mu tomado.
func (s *Server) registrarEnvioPorIP(origin string, hasta time.Time) {
	if s.tokenSendPorIP == nil {
		s.tokenSendPorIP = make(map[string]time.Time)
	}
	ahora := time.Now()
	for k, v := range s.tokenSendPorIP {
		if v.Before(ahora) {
			delete(s.tokenSendPorIP, k)
		}
	}
	s.tokenSendPorIP[origin] = hasta
}

// buildTokenMessage redacta la nota que se deja en los Mensajes guardados. El
// token va en su propia línea para que el formato de código lo abarque entero
// y baste un toque para copiarlo.
func buildTokenMessage(token string) string {
	equipo, err := os.Hostname()
	if err != nil || strings.TrimSpace(equipo) == "" {
		equipo = "este equipo"
	}

	return strings.Join([]string{
		"🔐 TelegramDL · token de acceso remoto",
		"",
		token,
		"",
		"Toca el token para copiarlo y pégalo en la pantalla de acceso remoto.",
		fmt.Sprintf("Equipo: %s · v%s", equipo, config.AppVersion),
		fmt.Sprintf("Enviado: %s", time.Now().Format("02/01/2006 15:04")),
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
		s.errorResponse(w, http.StatusMethodNotAllowed, "Método no permitido")
		return
	}

	// Este endpoint responde sin token, así que cualquier página web abierta en
	// el navegador del usuario podía dispararlo: la petición no llevaba cuerpo
	// ni Content-Type, y eso la convierte en una «simple request» que el
	// navegador manda sin consultar antes con CORS. Exigir una cabecera propia
	// obliga al navegador a hacer el preflight, que CORS rechaza.
	if r.Header.Get(cabeceraPeticionPropia) == "" {
		s.errorResponse(w, http.StatusForbidden, "Petición no válida")
		return
	}

	origin := requestOrigin(r)
	now := time.Now()

	s.mu.Lock()
	if wait := s.tokenSendPorIP[origin].Sub(now); wait > 0 {
		s.mu.Unlock()
		seconds := int(wait.Seconds()) + 1
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		s.jsonResponse(w, http.StatusTooManyRequests, map[string]any{
			"status":      "rate_limited",
			"detail":      fmt.Sprintf("Espera %d segundos antes de volver a pedirlo.", seconds),
			"retry_after": seconds,
		})
		return
	}
	if wait := s.tokenSendNextAt.Sub(now); wait > 0 {
		s.mu.Unlock()

		seconds := int(wait.Seconds()) + 1
		logbus.Warn(logbus.CatServer,
			"Petición de envío del token a Telegram rechazada por exceso de intentos",
			fmt.Sprintf("Origen: %s · Faltan %d s", origin, seconds))

		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		s.jsonResponse(w, http.StatusTooManyRequests, map[string]any{
			"status":      "rate_limited",
			"detail":      fmt.Sprintf("Espera %d segundos antes de volver a pedirlo.", seconds),
			"retry_after": seconds,
		})
		return
	}
	// El turno se consume antes de enviar: si Telegram tarda, un botón pulsado
	// con impaciencia no debe convertirse en cinco mensajes.
	s.tokenSendNextAt = now.Add(tokenSendCooldown)
	s.registrarEnvioPorIP(origin, now.Add(tokenSendCooldownPorIP))
	token := s.apiToken
	s.mu.Unlock()

	if token == "" {
		s.errorResponse(w, http.StatusServiceUnavailable, "Todavía no hay ningún token de acceso generado")
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
		delete(s.tokenSendPorIP, origin)
		s.mu.Unlock()

		logbus.Error(logbus.CatServer, "No se pudo enviar el token a Telegram", err.Error())
		s.errorResponse(w, http.StatusBadGateway, fmt.Sprintf("No se pudo enviar el token a Telegram: %s", err.Error()))
		return
	}

	logbus.Success(logbus.CatServer,
		"Token de acceso enviado a los Mensajes guardados de Telegram",
		fmt.Sprintf("Pedido desde %s", origin))

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status":      "ok",
		"detail":      "Token enviado a tus Mensajes guardados de Telegram.",
		"retry_after": int(tokenSendCooldown.Seconds()),
	})
}

// handleRegenerateToken rota el token de acceso. Requiere el token vigente
// (via authMiddleware); tras esto, cualquier otro dispositivo remoto deberá
// reconfigurarse con el nuevo valor devuelto aquí.
func (s *Server) handleRegenerateToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.errorResponse(w, http.StatusMethodNotAllowed, "Método no permitido")
		return
	}
	newToken := s.RegenerateToken()
	s.jsonResponse(w, http.StatusOK, map[string]string{"token": newToken})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	_ = s.clientMgr.Logout(r.Context())
	logbus.Warn(logbus.CatTelegram, "Sesión de Telegram cerrada", "")
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Handlers de Descargas
func (s *Server) handleGetDownloads(w http.ResponseWriter, _ *http.Request) {
	s.jsonResponse(w, http.StatusOK, s.downloader.GetDownloads())
}

func (s *Server) handleStartDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.URL) == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, "URL requerida")
		return
	}

	parsed, err := downloader.ParseURL(body.URL)
	if err != nil {
		s.errorResponse(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	chatID := parsed.ChatID
	if chatID == 0 && parsed.ChatUsername != "" {
		resolvedID, err := s.clientMgr.ResolveUsername(r.Context(), parsed.ChatUsername)
		if err != nil {
			s.errorResponse(w, http.StatusBadRequest, fmt.Sprintf("No se pudo encontrar el canal o usuario: %s", err.Error()))
			return
		}
		chatID = resolvedID
	}

	jobID := config.NewID()
	// Crear items para el rango de mensajes
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
		s.downloader.QueueItem(item)
	}
	s.broadcastState()

	s.jsonResponse(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"job_id":  jobID,
		"message": "Analizando mensajes e iniciando descarga...",
	})
}

func (s *Server) handleCancelDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID     string `json:"id"`
		ItemID string `json:"item_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	id := strings.TrimSpace(body.ID)
	if id == "" {
		id = strings.TrimSpace(body.ItemID)
	}
	if id == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, "ID requerido")
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
	_ = json.NewDecoder(r.Body).Decode(&body)
	id := strings.TrimSpace(body.ID)
	if id == "" {
		id = strings.TrimSpace(body.ItemID)
	}
	if id == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, "ID requerido")
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
	_ = json.NewDecoder(r.Body).Decode(&body)
	id := strings.TrimSpace(body.ID)
	if id == "" {
		id = strings.TrimSpace(body.ItemID)
	}
	if id == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, "ID requerido")
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
	_ = json.NewDecoder(r.Body).Decode(&body)
	id := strings.TrimSpace(body.ID)
	if id == "" {
		id = strings.TrimSpace(body.ItemID)
	}
	if id == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, "ID requerido")
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
		s.errorResponse(w, http.StatusMethodNotAllowed, "Método no permitido")
		return
	}

	if subpath == "history" {
		if r.Method == http.MethodDelete {
			s.handleClearHistory(w, r)
			return
		}
		s.errorResponse(w, http.StatusMethodNotAllowed, "Método no permitido")
		return
	}

	if subpath == "open" {
		if r.Method == http.MethodPost {
			s.handleOpenDownload(w, r)
			return
		}
		s.errorResponse(w, http.StatusMethodNotAllowed, "Método no permitido")
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

	s.errorResponse(w, http.StatusNotFound, "Ruta no encontrada")
}

func (s *Server) handleDeleteDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID         string `json:"id"`
		ItemID     string `json:"item_id"`
		DeleteFile *bool  `json:"delete_file"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	id := strings.TrimSpace(body.ID)
	if id == "" {
		id = strings.TrimSpace(body.ItemID)
	}
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	if id == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, "ID requerido")
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
	logbus.Info(logbus.CatDownloads, fmt.Sprintf("Historial de descargas limpiado (%d entradas)", removed), "")
	s.jsonResponse(w, http.StatusOK, map[string]any{"status": "ok", "removed": removed})
}

func (s *Server) handleOpenDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID     string `json:"id"`
		ItemID string `json:"item_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	id := strings.TrimSpace(body.ID)
	if id == "" {
		id = strings.TrimSpace(body.ItemID)
	}
	item, ok := s.downloader.GetItem(id)
	if !ok || item.FilePath == "" {
		s.errorResponse(w, http.StatusNotFound, "Archivo no encontrado")
		return
	}

	go abrirEnElSistema(item.FilePath)

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Settings
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.mu.RLock()
		cfg := s.config
		s.mu.RUnlock()
		s.jsonResponse(w, http.StatusOK, map[string]any{
			"status":   "ok",
			"settings": cfg,
		})
		return
	}

	var raw map[string]any
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		s.errorResponse(w, http.StatusUnprocessableEntity, "Configuración inválida")
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
	if v, ok := raw["download_folder"]; ok && v != nil {
		if sVal, ok := v.(string); ok && strings.TrimSpace(sVal) != "" {
			// Se descarta en silencio una carpeta prohibida en vez de aplicarla:
			// quien llama puede ser un dispositivo remoto con el token, y dejar
			// archivos en la carpeta de Inicio o sobre la sesión de Telegram no
			// es una preferencia, es una vía de ataque.
			if err := config.ValidateDownloadFolder(sVal); err != nil {
				logbus.Warn(logbus.CatServer, "Carpeta de descargas rechazada", sVal+": "+err.Error())
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
				cfg.ListenerChats = config.PreservarCarpetas(cfg.ListenerChats, parsedChats)
			}
		}
	}

	cfg = config.NormalizeConfig(cfg)
	s.config = cfg
	s.mu.Unlock()

	logConfigChanges(previousCfg, cfg)
	if err := s.storage.SaveConfig(cfg); err != nil {
		log.Printf("[SERVER] error guardando configuración en BD: %v", err)
	}
	s.downloader.UpdateConfig(cfg)
	s.listener.UpdateConfig(cfg)
	s.broadcastState()

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"settings": cfg,
	})
}

func (s *Server) handleSpeedLimit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SpeedLimit *config.SpeedLimit `json:"speed_limit"`
		Value      *float64           `json:"value"`
		Unit       *string            `json:"unit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.errorResponse(w, http.StatusUnprocessableEntity, "Datos inválidos")
		return
	}

	s.mu.Lock()
	previousCfg := s.config

	// Se parte del límite actual y solo se cambia lo que venga en la petición.
	// La condición anterior («unidad != "" o valor >= 0») era cierta siempre,
	// porque cualquier número no negativo la cumple: un cuerpo con solo la
	// unidad ponía el valor a cero y borraba el límite sin querer.
	limite := s.config.SpeedLimit
	if body.SpeedLimit != nil {
		limite = *body.SpeedLimit
	}
	if body.Value != nil {
		limite.Value = *body.Value
	}
	if body.Unit != nil {
		limite.Unit = *body.Unit
	}
	s.config.SpeedLimit = limite

	s.config = config.NormalizeConfig(s.config)
	cfg := s.config
	s.mu.Unlock()

	logConfigChanges(previousCfg, cfg)
	if err := s.storage.SaveConfig(cfg); err != nil {
		log.Printf("[SERVER] error guardando configuración en BD: %v", err)
	}
	s.downloader.UpdateConfig(cfg)
	s.broadcastState()

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"settings": cfg,
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
		s.errorResponse(w, http.StatusUnprocessableEntity, "Datos inválidos")
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
				cfg.ListenerChats = config.PreservarCarpetas(cfg.ListenerChats, parsedChats)
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
		fmt.Sprintf("Configuración de escucha guardada: activa=%v, %d chats", cfg.ListenerEnabled, len(cfg.ListenerChats)), "")
	if err := s.storage.SaveConfig(cfg); err != nil {
		log.Printf("[SERVER] error guardando configuración en BD: %v", err)
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
	_ = json.NewDecoder(r.Body).Decode(&body)
	id := strings.TrimSpace(body.ID)
	if id == "" {
		id = strings.TrimSpace(body.ItemID)
	}
	if id == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, "ID requerido")
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
	logbus.Warn(logbus.CatListener, fmt.Sprintf("Bandeja de escucha vaciada (%d elementos)", removed), "")
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleListenerDeleteItem(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID     string `json:"id"`
		ItemID string `json:"item_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
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
		s.errorResponse(w, http.StatusMethodNotAllowed, "Método no permitido")
		return
	}

	var body struct {
		ID       string `json:"id"`
		FileName string `json:"file_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "JSON inválido")
		return
	}

	id := strings.TrimSpace(body.ID)
	newFileName := strings.TrimSpace(body.FileName)
	if id == "" || newFileName == "" {
		s.errorResponse(w, http.StatusBadRequest, "ID y nombre de archivo requeridos")
		return
	}

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
		s.errorResponse(w, http.StatusBadRequest, "chat_id inválido")
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
		s.errorResponse(w, http.StatusBadRequest, "chat_id requerido en la ruta")
		return
	}

	chatIDStr := parts[3]
	chatID, err := strconv.ParseInt(chatIDStr, 10, 64)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "chat_id inválido")
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
		s.errorResponse(w, http.StatusBadRequest, "chat_id inválido")
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
		s.errorResponse(w, http.StatusMethodNotAllowed, "Método no permitido")
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
		s.errorResponse(w, http.StatusMethodNotAllowed, "Método no permitido")
		return
	}
	if !isLocalRequest(r) {
		s.errorResponse(w, http.StatusForbidden, "El selector del sistema solo está disponible en el propio equipo")
		return
	}

	s.mu.RLock()
	pick := s.folderPicker
	s.mu.RUnlock()

	if pick == nil {
		s.errorResponse(w, http.StatusNotImplemented, "No hay ventana de la aplicación para abrir el selector")
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
		s.errorResponse(w, http.StatusBadRequest, "No se puede leer el directorio")
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
			s.errorResponse(w, http.StatusBadRequest, "No hay actualizaciones disponibles")
			return
		}
	}

	if err := s.updater.InstallUpdate(rel); err != nil {
		logbus.Error(logbus.CatUpdater, "No se pudo iniciar la actualización", err.Error())
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	logbus.Info(logbus.CatUpdater, fmt.Sprintf("Instalando actualización %s", rel.TagName),
		fmt.Sprintf("Versión actual: v%s", config.AppVersion))

	s.jsonResponse(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": "Iniciando descarga e instalación",
	})
}

func (s *Server) handlePostponeUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Version == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, "Versión requerida")
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
