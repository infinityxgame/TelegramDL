package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"tgdown/pkg/config"
	"tgdown/pkg/downloader"
	"tgdown/pkg/listener"
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
	wsClients    map[*websocket.Conn]bool
	upgrader     websocket.Upgrader
	httpServer   *http.Server
	latestRel    *updater.ReleaseInfo
	cachedDisk   *downloader.DiskInfo
	exitCallback func()
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
		wsClients:  make(map[*websocket.Conn]bool),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		exitCallback: exitCb,
		mux:          http.NewServeMux(),
	}

	s.registerRoutes(s.mux)

	// Escuchar cambios de estado en el motor de descargas para emitir a los WebSockets
	dl.OnStateChange(func(item storage.DownloadItem) {
		s.broadcastState()
	})

	return s
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.corsMiddleware(s.mux).ServeHTTP(w, r)
			return
		}
		// Para cualquier ruta de frontend, devolver 404 para que Wails AssetServer
		// sirva index.html e inyecte los bindings e IPC nativos.
		http.NotFound(w, r)
	})
}

func (s *Server) WebHandler() http.Handler {
	return s.corsMiddleware(s.mux)
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
	}

	// Tarea de refresco y broadcast periódico
	go s.periodicBroadcastLoop()

	go func() {
		_ = s.httpServer.Serve(listener)
	}()

	return nil
}

func (s *Server) Stop() {
	if s.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.httpServer.Shutdown(ctx)
	}
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
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

	// Downloads (Soporta /api/downloads, /api/downloads/history, /api/downloads/open, /api/downloads/{id})
	mux.HandleFunc("/api/downloads", s.handleDownloadsRoute)
	mux.HandleFunc("/api/downloads/", s.handleDownloadsRoute)
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
	mux.HandleFunc("/api/listener/resolve-chat", s.handleListenerResolveChat)
	mux.HandleFunc("/api/listener/chat/", s.handleListenerResolveChatPath)

	// Filesystem & System
	mux.HandleFunc("/api/filesystem", s.handleFSBrowse)
	mux.HandleFunc("/api/fs/browse", s.handleFSBrowse)
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

	// App Exit (Soporta /api/app/exit y /api/exit)
	mux.HandleFunc("/api/app/exit", s.handleExit)
	mux.HandleFunc("/api/exit", s.handleExit)
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	s.mu.Lock()
	s.wsClients[conn] = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.wsClients, conn)
		s.mu.Unlock()
		_ = conn.Close()
	}()

	// Enviar estado inicial inmediato
	snap := s.buildStateSnapshot()
	_ = conn.WriteJSON(snap)

	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

func (s *Server) periodicBroadcastLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		s.broadcastState()
	}
}

func (s *Server) broadcastState() {
	snap := s.buildStateSnapshot()

	s.mu.RLock()
	clients := make([]*websocket.Conn, 0, len(s.wsClients))
	for c := range s.wsClients {
		clients = append(clients, c)
	}
	s.mu.RUnlock()

	for _, c := range clients {
		_ = c.WriteJSON(snap)
	}
}

func (s *Server) buildStateSnapshot() map[string]any {
	downloads := s.downloader.GetDownloads()
	activeCount := 0
	queuedCount := 0
	var speedBytes int64 = 0

	for _, d := range downloads {
		if d.Status == "downloading" {
			activeCount++
		} else if d.Status == "queued" {
			queuedCount++
		}
	}

	s.mu.RLock()
	folder := s.config.DownloadFolder
	s.mu.RUnlock()

	disk, _ := downloader.GetDiskUsage(folder, downloads...)
	if disk != nil {
		s.mu.Lock()
		s.cachedDisk = disk
		s.mu.Unlock()
	} else {
		s.mu.RLock()
		disk = s.cachedDisk
		s.mu.RUnlock()
	}

	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()

	listenerItems := s.listener.GetItems()

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
	s.jsonResponse(w, http.StatusOK, st)
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

	// Guardar en la base de datos SQLite
	_ = s.storage.SaveCredentials(body.APIID, body.APIHash)

	// Guardar también en archivo .env
	if err := config.SaveEnvCredentials(body.APIID, body.APIHash); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Error guardando credenciales")
		return
	}

	_ = s.clientMgr.InitClient(body.APIID, body.APIHash)
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleAuthSendCode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Phone string `json:"phone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Phone == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, "Teléfono requerido")
		return
	}

	hash, err := s.clientMgr.SendCode(r.Context(), body.Phone)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok", "phone_code_hash": hash})
}

func (s *Server) handleAuthVerifyCode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Phone         string `json:"phone"`
		Code          string `json:"code"`
		PhoneCodeHash string `json:"phone_code_hash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Code == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, "Código requerido")
		return
	}

	status, err := s.clientMgr.VerifyCode(r.Context(), body.Phone, body.Code, body.PhoneCodeHash)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": status})
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
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	_ = s.clientMgr.Logout(r.Context())
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Handlers de Descargas
func (s *Server) handleGetDownloads(w http.ResponseWriter, r *http.Request) {
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

	jobID := uuid.New().String()
	go func() {
		// Crear items para el rango de mensajes
		for msgID := parsed.StartMsgID; msgID <= parsed.EndMsgID; msgID++ {
			item := storage.DownloadItem{
				ID:        uuid.New().String(),
				JobID:     jobID,
				MessageID: int64(msgID),
				ChatID:    chatID,
				Status:    "queued",
				Source:    "manual",
				FileName:  fmt.Sprintf("mensaje_%d", msgID),
			}
			s.downloader.QueueItem(item)
		}
	}()

	s.jsonResponse(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"job_id":  jobID,
		"message": "Analizando mensajes e iniciando descarga...",
	})
}

func (s *Server) handleCancelDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.ID == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, "ID requerido")
		return
	}

	_ = s.downloader.CancelDownload(body.ID)
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handlePauseDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.ID == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, "ID requerido")
		return
	}

	if err := s.downloader.PauseDownload(body.ID); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleResumeDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.ID == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, "ID requerido")
		return
	}

	if err := s.downloader.ResumeDownload(r.Context(), body.ID); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleRetryDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.ID == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, "ID requerido")
		return
	}

	if err := s.downloader.ResumeDownload(r.Context(), body.ID); err != nil {
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
		if r.Method == http.MethodDelete || r.Method == http.MethodPost {
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
		delFile := true
		if q := r.URL.Query().Get("delete_file"); q != "" {
			if strings.ToLower(q) == "false" || q == "0" {
				delFile = false
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
		DeleteFile *bool  `json:"delete_file"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.ID == "" {
		body.ID = r.URL.Query().Get("id")
	}
	if body.ID == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, "ID requerido")
		return
	}

	delFile := true
	if body.DeleteFile != nil {
		delFile = *body.DeleteFile
	} else if q := r.URL.Query().Get("delete_file"); q != "" {
		if strings.ToLower(q) == "false" || q == "0" {
			delFile = false
		}
	}

	_ = s.downloader.DeleteDownload(body.ID, delFile)
	s.listener.RemoveItem(body.ID)
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleClearHistory(w http.ResponseWriter, r *http.Request) {
	removed, _ := s.downloader.ClearHistory()
	s.jsonResponse(w, http.StatusOK, map[string]any{"status": "ok", "removed": removed})
}

func (s *Server) handleOpenDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	item, ok := s.downloader.GetItem(body.ID)
	if !ok || item.FilePath == "" {
		s.errorResponse(w, http.StatusNotFound, "Archivo no encontrado")
		return
	}

	go func() {
		target := item.FilePath
		if runtime.GOOS == "windows" {
			if _, err := os.Stat(target); err == nil {
				_ = exec.Command("explorer.exe", "/select,", target).Start()
			} else {
				_ = exec.Command("explorer.exe", filepath.Dir(target)).Start()
			}
		} else if runtime.GOOS == "darwin" {
			_ = exec.Command("open", "-R", target).Start()
		} else {
			_ = exec.Command("xdg-open", filepath.Dir(target)).Start()
		}
	}()

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

	var newCfg config.Config
	if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
		s.errorResponse(w, http.StatusUnprocessableEntity, "Configuración inválida")
		return
	}

	newCfg = config.NormalizeConfig(newCfg)
	s.mu.Lock()
	s.config = newCfg
	s.mu.Unlock()

	_ = s.storage.SaveConfig(newCfg)
	s.downloader.UpdateConfig(newCfg)
	s.listener.UpdateConfig(newCfg)

	s.jsonResponse(w, http.StatusOK, newCfg)
}

func (s *Server) handleSpeedLimit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SpeedLimit config.SpeedLimit `json:"speed_limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.errorResponse(w, http.StatusUnprocessableEntity, "Datos inválidos")
		return
	}

	s.mu.Lock()
	s.config.SpeedLimit = body.SpeedLimit
	cfg := s.config
	s.mu.Unlock()

	_ = s.storage.SaveConfig(cfg)
	s.downloader.UpdateConfig(cfg)

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
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

	var body struct {
		ListenerEnabled bool                  `json:"listener_enabled"`
		ListenerChats   []config.ListenerChat `json:"listener_chats"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.errorResponse(w, http.StatusUnprocessableEntity, "Datos inválidos")
		return
	}

	s.mu.Lock()
	s.config.ListenerEnabled = body.ListenerEnabled
	s.config.ListenerChats = body.ListenerChats
	savedCfg := s.config
	s.mu.Unlock()

	_ = s.storage.SaveConfig(savedCfg)
	s.listener.UpdateConfig(savedCfg)

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleListenerItems(w http.ResponseWriter, r *http.Request) {
	s.jsonResponse(w, http.StatusOK, s.listener.GetItems())
}

func (s *Server) handleListenerDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.ID == "" {
		s.errorResponse(w, http.StatusUnprocessableEntity, "ID requerido")
		return
	}

	if err := s.listener.DownloadItem(body.ID); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleListenerResolveChat(w http.ResponseWriter, r *http.Request) {
	chatIDStr := r.URL.Query().Get("chat_id")
	chatID, err := strconv.ParseInt(chatIDStr, 10, 64)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "chat_id inválido")
		return
	}

	name, _ := s.listener.ResolveChat(r.Context(), chatID)
	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status": "ok",
		"chat": map[string]any{
			"id":            chatID,
			"name":          name,
			"auto_download": false,
			"f_photos":      true,
			"f_videos":      true,
			"f_audios":      true,
			"f_docs":        true,
			"f_stickers":    true,
		},
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

	name, _ := s.listener.ResolveChat(r.Context(), chatID)
	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status": "ok",
		"chat": map[string]any{
			"id":            chatID,
			"name":          name,
			"auto_download": false,
			"f_photos":      true,
			"f_videos":      true,
			"f_audios":      true,
			"f_docs":        true,
			"f_stickers":    true,
		},
	})
}

// Filesystem & System
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
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": "Iniciando descarga e instalación",
	})
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
