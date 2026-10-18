package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"tgdown/pkg/config"
	"tgdown/pkg/i18n"
)

type DownloadItem struct {
	ID               string  `json:"id"`
	JobID            string  `json:"job_id"`
	MessageID        int64   `json:"message_id"`
	ChatID           int64   `json:"chat_id"`
	FileName         string  `json:"file_name"`
	CaptionFileName  string  `json:"caption_file_name,omitempty"`
	OriginalFileName string  `json:"original_file_name,omitempty"`
	Status           string  `json:"status"`
	Progress         float64 `json:"progress"`
	TotalStr         string  `json:"total_str"`
	CurrentStr       string  `json:"current_str"`
	Speed            string  `json:"speed"`
	Kind             string  `json:"kind"`
	FilePath         string  `json:"file_path"`
	// SubFolder es la subcarpeta, relativa a la carpeta de descargas, donde va
	// este archivo. La pone la escucha con el nombre del chat; vacía significa
	// que el archivo cae en la raíz, como las descargas por enlace.
	SubFolder    string  `json:"sub_folder,omitempty"`
	Source       string  `json:"source"`
	Error        string  `json:"error"`
	UpdatedAt    float64 `json:"updated_at"`
	CreatedAt    float64 `json:"created_at"`
	TotalBytes   int64   `json:"total_bytes"`
	CurrentBytes int64   `json:"current_bytes"`

	// ETA es lo que falta para terminar, ya formateado ("2 min 30 s"). Solo
	// vive en memoria: no tiene columna en SQLite y no se guarda a propósito,
	// porque una estimación de hace tres días no le sirve a nadie. Las
	// consultas de esta tabla nombran sus columnas una a una, así que añadir
	// este campo no afecta a lo que se guarda ni a lo que se lee.
	ETA string `json:"eta"`
}

type Storage struct {
	dbPath string
	db     *sql.DB
	mu     sync.RWMutex
	// vacuumMu serializa los VACUUM entre limpiezas simultáneas. Es un mutex
	// por valor, así que no necesita inicialización.
	vacuumMu sync.Mutex
}

func NewStorage(dbPath string) (*Storage, error) {
	// Los PRAGMA por conexión van en el DSN, no en un db.Exec suelto:
	// database/sql mantiene un pool y un "PRAGMA foreign_keys = ON" ejecutado
	// con Exec solo afecta a la conexión que atendió esa llamada. Cualquier
	// consulta posterior puede caer en otra conexión con las claves foráneas
	// desactivadas, así que el ON DELETE CASCADE de download_chunks se aplicaba
	// o no según qué conexión tocara.
	dsn := dbPath + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("storage.openErr"), err)
	}

	s := &Storage{
		dbPath: dbPath,
		db:     db,
	}

	if err := s.initSchema(); err != nil {
		db.Close()
		return nil, err
	}

	return s, nil
}

func (s *Storage) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

func (s *Storage) initSchema() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// journal_mode queda grabado en el propio archivo, así que basta con
	// fijarlo una vez. foreign_keys y busy_timeout se aplican por conexión y
	// ya vienen en el DSN de NewStorage.
	//
	// SEC-38: estos dos PRAGMA ya no se descartan en silencio. Si journal_mode
	// no llega a WAL, cada escritura queda sincronizada con el diario completo
	// (rendimiento) y una caída puede dejar el archivo incoherente; si falla
	// synchronous, el riesgo es de corrupción ante cortes de alimentación.
	// Ninguno de los dos es una operación por arranque esperada que falle, así
	// que cualquier error se registra con el patrón [STORAGE] del paquete.
	if _, err := s.db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		log.Printf("[STORAGE] failed to enable WAL journal mode: %v", err)
	}
	if _, err := s.db.Exec("PRAGMA synchronous=NORMAL;"); err != nil {
		log.Printf("[STORAGE] failed to set synchronous mode: %v", err)
	}

	schema := `
	CREATE TABLE IF NOT EXISTS app_config (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);

	` + listenerChatsSchema + `

	CREATE TABLE IF NOT EXISTS downloads (
		id TEXT PRIMARY KEY,
		job_id TEXT,
		message_id INTEGER,
		chat_id INTEGER,
		file_name TEXT NOT NULL,
		status TEXT NOT NULL,
		progress REAL NOT NULL DEFAULT 0,
		total_str TEXT NOT NULL DEFAULT '0 B',
		current_str TEXT NOT NULL DEFAULT '0 B',
		speed TEXT NOT NULL DEFAULT '0 B/s',
		kind TEXT,
		file_path TEXT,
		sub_folder TEXT,
		source TEXT,
		error TEXT,
		updated_at REAL NOT NULL,
		created_at REAL NOT NULL,
		total_bytes INTEGER NOT NULL DEFAULT 0,
		current_bytes INTEGER NOT NULL DEFAULT 0,
		caption_file_name TEXT,
		original_file_name TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_downloads_updated ON downloads(updated_at DESC);

	CREATE TABLE IF NOT EXISTS download_chunks (
		download_id TEXT NOT NULL,
		chunk_index INTEGER NOT NULL,
		PRIMARY KEY(download_id, chunk_index),
		FOREIGN KEY(download_id) REFERENCES downloads(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS scanner_recent_chats (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		target TEXT NOT NULL,
		chat_id INTEGER NOT NULL DEFAULT 0,
		chat_name TEXT NOT NULL,
		username TEXT NOT NULL DEFAULT '',
		is_favorite INTEGER NOT NULL DEFAULT 0,
		updated_at REAL NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_scanner_recent_updated ON scanner_recent_chats(updated_at DESC);

	CREATE TABLE IF NOT EXISTS scanner_cache_items (
		id TEXT PRIMARY KEY,
		chat_id INTEGER NOT NULL,
		chat_name TEXT NOT NULL,
		message_id INTEGER NOT NULL,
		file_name TEXT NOT NULL,
		caption_file_name TEXT NOT NULL DEFAULT '',
		original_file_name TEXT NOT NULL DEFAULT '',
		kind TEXT NOT NULL,
		file_size INTEGER NOT NULL,
		formatted_size TEXT NOT NULL,
		date INTEGER NOT NULL,
		date_str TEXT NOT NULL,
		is_deleted INTEGER NOT NULL DEFAULT 0
	);

	CREATE INDEX IF NOT EXISTS idx_scanner_items_chat_date ON scanner_cache_items(chat_id, date DESC);

	CREATE TABLE IF NOT EXISTS scanner_cache_ranges (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		chat_id INTEGER NOT NULL,
		start_ts INTEGER NOT NULL,
		end_ts INTEGER NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_scanner_ranges_chat ON scanner_cache_ranges(chat_id);
	`

	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("error al inicializar esquema: %w", err)
	}

	// Migraciones defensivas (idénticas a Python)
	s.execMigration("ALTER TABLE downloads ADD COLUMN total_bytes INTEGER NOT NULL DEFAULT 0")
	s.execMigration("ALTER TABLE downloads ADD COLUMN current_bytes INTEGER NOT NULL DEFAULT 0")
	s.execMigration("ALTER TABLE downloads ADD COLUMN error TEXT")
	s.execMigration("ALTER TABLE downloads ADD COLUMN sub_folder TEXT")
	// Nombres alternativos para selección manual
	s.execMigration("ALTER TABLE downloads ADD COLUMN caption_file_name TEXT")
	s.execMigration("ALTER TABLE downloads ADD COLUMN original_file_name TEXT")
	for _, col := range []string{"f_photos", "f_videos", "f_audios", "f_docs", "f_stickers"} {
		s.execMigration(fmt.Sprintf("ALTER TABLE listener_chats ADD COLUMN %s INTEGER NOT NULL DEFAULT 1", col))
	}
	// Carpeta asignada a cada chat vigilado. Las bases de datos que vienen de
	// una versión anterior no tienen estas columnas.
	for _, col := range []string{"folder", "topic_folder"} {
		s.execMigration(fmt.Sprintf("ALTER TABLE listener_chats ADD COLUMN %s TEXT NOT NULL DEFAULT ''", col))
	}
	// Selección de nombres para archivos de la escucha: "manual", "original" o "caption"
	s.execMigration("ALTER TABLE listener_chats ADD COLUMN name_mode TEXT NOT NULL DEFAULT 'manual'")
	// Migración: si existe la columna antigua manual_name_selection, convertirla a name_mode
	cols, err := s.tableColumns("listener_chats")
	if err == nil {
		if cols["manual_name_selection"] {
			// Eliminar la columna antigua (SQLite no soporta DROP COLUMN en todas las versiones, así que la dejamos)
		}
	}

	if err := s.migrateListenerTopics(); err != nil {
		return err
	}

	return nil
}

// execMigration aplica una migración defensiva ALTER TABLE. Estas consultas se
// ejecutan en cada arranque, así que "duplicate column name" es el resultado
// normal cuando la columna ya existe (BD recién creada o migración ya hecha):
// ese error se ignora sin registrar para no inundar el log con una línea por
// columna en cada arranque. Cualquier otro fallo (tabla inexistente, BD
// bloqueada, corrupción...) sí es inesperado y se deja constancia con el
// patrón [STORAGE] del paquete. El fichero no tenía ningún patrón previo de
// "columna ya existe", así que se establece aquí, en un único punto.
func (s *Storage) execMigration(query string) {
	_, err := s.db.Exec(query)
	if err == nil {
		return
	}
	if strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return
	}
	log.Printf("[STORAGE] migration failed (%s): %v", query, err)
}

// listenerChatsSchema es la definición actual de la tabla de chats vigilados.
// La clave primaria es el par (chat_id, topic_id) porque un mismo grupo puede
// vigilarse varias veces, una por cada tema. topic_id = 0 significa «todo el
// grupo».
const listenerChatsSchema = `
	CREATE TABLE IF NOT EXISTS listener_chats (
		chat_id INTEGER NOT NULL,
		topic_id INTEGER NOT NULL DEFAULT 0,
		topic_name TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL,
		folder TEXT NOT NULL DEFAULT '',
		topic_folder TEXT NOT NULL DEFAULT '',
		auto_download INTEGER NOT NULL DEFAULT 0,
		f_photos INTEGER NOT NULL DEFAULT 1,
		f_videos INTEGER NOT NULL DEFAULT 1,
		f_audios INTEGER NOT NULL DEFAULT 1,
		f_docs INTEGER NOT NULL DEFAULT 1,
		f_stickers INTEGER NOT NULL DEFAULT 1,
		name_mode TEXT NOT NULL DEFAULT 'manual',
		PRIMARY KEY(chat_id, topic_id)
	);
`

// tableColumns devuelve el conjunto de columnas de una tabla, o un mapa vacío
// si la tabla no existe.
func (s *Storage) tableColumns(table string) (map[string]bool, error) {
	cols := make(map[string]bool)
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return cols, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err == nil {
			cols[name] = true
		}
	}
	return cols, rows.Err()
}

// migrateListenerTopics lleva la tabla de chats vigilados del esquema antiguo
// (una fila por grupo, chat_id como clave primaria) al nuevo, que admite una
// fila por tema. No se puede cambiar una clave primaria con ALTER TABLE, así
// que hay que reconstruir la tabla y volcar lo que hubiera dentro.
func (s *Storage) migrateListenerTopics() error {
	cols, err := s.tableColumns("listener_chats")
	if err != nil {
		return fmt.Errorf("error al inspeccionar listener_chats: %w", err)
	}
	// Sin columnas la tabla no existe (la acaba de crear el esquema de arriba con
	// el formato nuevo) y con topic_id la migración ya se hizo en otro arranque.
	if len(cols) == 0 || cols["topic_id"] {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	steps := []string{
		"ALTER TABLE listener_chats RENAME TO listener_chats_legacy",
		listenerChatsSchema,
		`INSERT OR IGNORE INTO listener_chats(
			chat_id, topic_id, topic_name, name, auto_download,
			f_photos, f_videos, f_audios, f_docs, f_stickers, name_mode
		)
		SELECT chat_id, 0, '', name, auto_download,
			f_photos, f_videos, f_audios, f_docs, f_stickers, 'manual'
		FROM listener_chats_legacy`,
		"DROP TABLE listener_chats_legacy",
	}
	for _, step := range steps {
		if _, err := tx.Exec(step); err != nil {
			return fmt.Errorf(i18n.T("storage.migrateTopicsErr"), err)
		}
	}

	return tx.Commit()
}

func (s *Storage) setConfigKey(key, value string) error {
	_, err := s.db.Exec(`
		INSERT INTO app_config(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value
	`, key, value)
	return err
}

func (s *Storage) GetCredentials() (string, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var apiID, apiHash string
	// SEC-38: el error ya no se descarta a ciegas. sql.ErrNoRows es el caso
	// esperado: el usuario nunca guardó credenciales y la app debe arrancar
	// igual, así que no se registra (inundaría el log en cada arranque limpio).
	// El patrón ya existía en el fichero con GetAPIToken. Cualquier otro error
	// (BD bloqueada, corrupta...) sí es inesperado y se registra.
	if err := s.db.QueryRow("SELECT value FROM app_config WHERE key = 'api_id' OR key = 'tgdl_api_id' ORDER BY key ASC LIMIT 1").Scan(&apiID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		log.Printf("[STORAGE] failed to read api_id credential: %v", err)
	}
	if err := s.db.QueryRow("SELECT value FROM app_config WHERE key = 'api_hash' OR key = 'tgdl_api_hash' ORDER BY key ASC LIMIT 1").Scan(&apiHash); err != nil && !errors.Is(err, sql.ErrNoRows) {
		log.Printf("[STORAGE] failed to read api_hash credential: %v", err)
	}

	if apiID == "" || apiHash == "" {
		// Ya no hay respaldo en ningún .env: lo que hubiera en el archivo se
		// vuelca a app_config una sola vez, al arrancar, desde
		// MigrateLegacyEnv. Lo que queda aquí es el rescate del api_id para
		// quien venga de la versión en Python.
		if apiID == "" {
			sessionFiles := []string{
				filepath.Join(config.DataDir, "downloader_session.session"),
				filepath.Join(config.BaseDir, "downloader_session.session"),
			}
			for _, sf := range sessionFiles {
				if _, err := os.Stat(sf); err == nil {
					db, err := sql.Open("sqlite", sf)
					if err == nil {
						var id int
						if err := db.QueryRow("SELECT api_id FROM sessions LIMIT 1").Scan(&id); err == nil && id != 0 {
							apiID = strconv.Itoa(id)
						}
						db.Close()
					}
					if apiID != "" {
						break
					}
				}
			}
		}

		if apiID != "" && apiHash != "" {
			go func(id, hash string) {
				_ = s.SaveCredentials(id, hash)
			}(apiID, apiHash)
		}
	}

	return apiID, apiHash, nil
}

func (s *Storage) SaveCredentials(apiID, apiHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.setConfigKey("api_id", apiID); err != nil {
		return err
	}
	return s.setConfigKey("api_hash", apiHash)
}

// getConfigKey lee una clave de app_config y devuelve cadena vacía si no está.
// No toma el mutex: eso corresponde a quien lo llama.
func (s *Storage) getConfigKey(key string) string {
	var value string
	if err := s.db.QueryRow("SELECT value FROM app_config WHERE key = ?", key).Scan(&value); err != nil {
		return ""
	}
	return value
}

// ServerBinding devuelve la dirección y el puerto en los que debe escuchar el
// panel. Lo que no esté guardado sale vacío o en cero, y decide quien llama.
func (s *Storage) ServerBinding() (string, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	host := strings.TrimSpace(s.getConfigKey("bind_host"))

	port, err := strconv.Atoi(strings.TrimSpace(s.getConfigKey("server_port")))
	if err != nil || port <= 0 || port > 65535 {
		port = 0
	}

	return host, port
}

// SaveServerBinding persiste la dirección de escucha. Los valores vacíos o
// fuera de rango se ignoran en vez de sobrescribir lo que ya hubiera.
func (s *Storage) SaveServerBinding(host string, port int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if h := strings.TrimSpace(host); h != "" {
		if err := s.setConfigKey("bind_host", h); err != nil {
			return err
		}
	}
	if port > 0 && port <= 65535 {
		return s.setConfigKey("server_port", strconv.Itoa(port))
	}
	return nil
}

// MigrateLegacyEnv vuelca a la base de datos lo que quedara en el .env de una
// versión anterior y aparta el archivo. Solo rellena lo que falte: si una clave
// ya está en app_config, manda la base de datos.
//
// Se ejecuta una vez al arrancar; a partir de ahí la aplicación no vuelve a
// mirar ningún .env.
func (s *Storage) MigrateLegacyEnv() {
	values := config.LegacyEnvValues()

	first := func(names ...string) string {
		for _, n := range names {
			if v := strings.TrimSpace(values[n]); v != "" {
				return v
			}
		}
		return ""
	}

	s.mu.Lock()

	var migrated []string
	save := func(key, value string) {
		if value == "" || s.getConfigKey(key) != "" {
			return
		}
		if err := s.setConfigKey(key, value); err != nil {
			log.Printf("[STORAGE] %s", i18n.T("storage.envMigrateError", key, err))
			return
		}
		migrated = append(migrated, key)
	}

	save("api_id", first("TGDL_API_ID", "API_ID"))
	save("api_hash", first("TGDL_API_HASH", "API_HASH"))
	save("bind_host", first("TGDL_BIND_HOST", "BIND_HOST"))
	save("server_port", first("TGDL_PORT", "PORT"))

	// Si nunca hubo una elección de dirección, se deja escrita la de por
	// defecto. Antes esto lo hacía el .env recién creado; ahora el valor queda
	// visible en app_config para quien quiera cambiarlo.
	save("bind_host", config.DefaultBindHost)

	s.mu.Unlock()

	if len(migrated) > 0 {
		log.Printf("[STORAGE] %s", i18n.T("storage.envMigrated", strings.Join(migrated, ", ")))
	}
	config.ArchiveLegacyEnv()
}

// GetAPIToken devuelve el token de acceso a la API HTTP local, si existe.
func (s *Storage) GetAPIToken() (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var token string
	err := s.db.QueryRow("SELECT value FROM app_config WHERE key = 'api_token'").Scan(&token)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return token, nil
}

// SaveAPIToken persiste el token de acceso a la API HTTP local.
func (s *Storage) SaveAPIToken(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setConfigKey("api_token", token)
}

func findExistingFile(paths ...string) string {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

func (s *Storage) LoadConfig(defaults config.Config, legacyPath string) (config.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := defaults

	rows, err := s.db.Query("SELECT key, value FROM app_config")
	if err != nil {
		return cfg, err
	}
	defer rows.Close()

	kv := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err == nil {
			kv[k] = v
		}
	}
	if err := rows.Err(); err != nil {
		return cfg, err
	}

	// Si está vacío e invocan con legacy JSON o existe en DataDir/BaseDir
	if len(kv) == 0 {
		targetLegacy := findExistingFile(legacyPath, filepath.Join(config.DataDir, "config.json"), filepath.Join(config.BaseDir, "config.json"))
		if targetLegacy != "" {
			if data, err := os.ReadFile(targetLegacy); err == nil {
				var raw map[string]any
				if json.Unmarshal(data, &raw) == nil {
					for _, k := range []string{"max_concurrent_downloads", "parallel_chunks", "chunk_workers", "download_folder", "listener_enabled", "color_id"} {
						if val, ok := raw[k]; ok && val != nil {
							_ = s.setConfigKey(k, fmt.Sprintf("%v", val))
						}
					}
					if sp, ok := raw["speed_limit"].(map[string]any); ok {
						_ = s.setConfigKey("speed_value", fmt.Sprintf("%v", sp["value"]))
						_ = s.setConfigKey("speed_unit", fmt.Sprintf("%v", sp["unit"]))
					}
					// Releer
					if r2, err := s.db.Query("SELECT key, value FROM app_config"); err == nil {
						for r2.Next() {
							var k, v string
							if err := r2.Scan(&k, &v); err == nil {
								kv[k] = v
							}
						}
						if err := r2.Err(); err != nil {
							// Log error but continue with existing kv
						}
						r2.Close()
					}
				}
			}
		}
	}

	if val, ok := kv["max_concurrent_downloads"]; ok {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.MaxConcurrentDownloads = n
		}
	}
	if val, ok := kv["chunk_workers"]; ok {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.ChunkWorkers = n
		}
	}
	if val, ok := kv["parallel_chunks"]; ok {
		cfg.ParallelChunks = val == "1" || val == "true"
	}
	if val, ok := kv["listener_enabled"]; ok {
		cfg.ListenerEnabled = val == "1" || val == "true"
	}
	if val, ok := kv["organize_by_chat"]; ok {
		cfg.OrganizeByChat = val == "1" || val == "true"
	}
	if val, ok := kv["minimize_to_tray"]; ok {
		cfg.MinimizeToTray = val == "1" || val == "true"
	}
	if val, ok := kv["sound_notification"]; ok {
		cfg.SoundNotification = val == "1" || val == "true"
	}
	// shutdown_when_done no se carga a propósito: el apagado automático es un
	// ajuste de sesión, siempre arranca desactivado (ver config.Config).
	if val, ok := kv["download_folder"]; ok && val != "" {
		cfg.DownloadFolder = val
	}
	if val, ok := kv["color_id"]; ok && val != "" && val != "None" && val != "null" {
		if c, err := strconv.Atoi(val); err == nil {
			cfg.ColorID = &c
		}
	}
	if val, ok := kv["loader_color_id"]; ok && val != "" && val != "None" && val != "null" {
		if c, err := strconv.Atoi(val); err == nil {
			cfg.LoaderColorID = &c
		}
	}
	if val, ok := kv["speed_value"]; ok {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			cfg.SpeedLimit.Value = f
		}
	}
	if val, ok := kv["speed_unit"]; ok && val != "" {
		cfg.SpeedLimit.Unit = val
	}
	if val, ok := kv["language"]; ok && val != "" {
		cfg.Language = val
	}
	if val, ok := kv["notification_bot_enabled"]; ok {
		cfg.NotificationBotEnabled = val == "1" || val == "true"
	}
	if val, ok := kv["notification_bot_token"]; ok {
		cfg.NotificationBotToken = val
	}
	if val, ok := kv["notification_chat_id"]; ok {
		if n, err := strconv.ParseInt(val, 10, 64); err == nil {
			cfg.NotificationChatID = n
		}
	}
	if val, ok := kv["notification_topic_id"]; ok && val != "" && val != "None" && val != "null" {
		if n, err := strconv.ParseInt(val, 10, 64); err == nil {
			cfg.NotificationTopicID = &n
		}
	}
	if val, ok := kv["bot_notifications"]; ok {
		cfg.BotNotifications = val == "1" || val == "true"
	} else if val, ok := kv["notify_on_complete"]; ok {
		// Instalaciones anteriores a la unificación guardaban dos interruptores
		// (notify_on_complete / notify_on_error). Al no existir todavía la clave
		// nueva, se hereda el estado del primero para no perder las
		// notificaciones que el usuario ya tenía configuradas.
		cfg.BotNotifications = val == "1" || val == "true"
	}

	// Cargar listener_chats
	chatRows, err := s.db.Query("SELECT chat_id, topic_id, topic_name, name, folder, topic_folder, auto_download, f_photos, f_videos, f_audios, f_docs, f_stickers, name_mode FROM listener_chats ORDER BY chat_id, topic_id")
	if err == nil {
		defer chatRows.Close()
		chats := make([]config.ListenerChat, 0)
		for chatRows.Next() {
			var c config.ListenerChat
			var topicID int64
			var topicName, folder, topicFolder, nameMode string
			var auto, photos, videos, audios, docs, stickers int
			if err := chatRows.Scan(&c.ID, &topicID, &topicName, &c.Name, &folder, &topicFolder, &auto, &photos, &videos, &audios, &docs, &stickers, &nameMode); err == nil {
				c.TopicID = config.TopicPointer(topicID)
				if c.HasTopic() {
					c.TopicName = topicName
					c.TopicFolder = topicFolder
				}
				c.Folder = folder
				c.AutoDownload = auto != 0
				c.NameMode = nameMode
				if c.NameMode == "" {
					c.NameMode = "manual"
				}
				c.FPhotos = photos != 0
				c.FVideos = videos != 0
				c.FAudios = audios != 0
				c.FDocs = docs != 0
				c.FStickers = stickers != 0
				chats = append(chats, c)
			}
		}
		if err := chatRows.Err(); err != nil {
			// Log error but continue with whatever chats we loaded
		}
		cfg.ListenerChats = chats
	}

	// Si no hay chats en SQLite, migrar desde config.json legacy (de BaseDir o DataDir)
	if len(cfg.ListenerChats) == 0 {
		targetLegacy := findExistingFile(legacyPath, filepath.Join(config.BaseDir, "config.json"), filepath.Join(config.DataDir, "config.json"))
		if targetLegacy != "" {
			if data, err := os.ReadFile(targetLegacy); err == nil {
				var raw struct {
					ListenerChats   []config.ListenerChat `json:"listener_chats"`
					ListenerChatIDs []int64               `json:"listener_chat_ids"`
				}
				if json.Unmarshal(data, &raw) == nil {
					imported := raw.ListenerChats
					if len(imported) == 0 && len(raw.ListenerChatIDs) > 0 {
						for _, id := range raw.ListenerChatIDs {
							imported = append(imported, config.ListenerChat{
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
					for _, c := range imported {
						if c.Name == "" {
							c.Name = fmt.Sprintf("%d", c.ID)
						}
						c.FPhotos = true
						c.FVideos = true
						c.FAudios = true
						c.FDocs = true
						c.FStickers = true
						nameMode := c.NameMode
						if nameMode == "" {
							nameMode = "manual"
						}
						_, _ = s.db.Exec(`
							INSERT OR REPLACE INTO listener_chats(chat_id, topic_id, topic_name, name, auto_download, f_photos, f_videos, f_audios, f_docs, f_stickers, name_mode)
							VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
						`, c.ID, c.Topic(), c.TopicName, c.Name, c.AutoDownload, c.FPhotos, c.FVideos, c.FAudios, c.FDocs, c.FStickers, nameMode)
						cfg.ListenerChats = append(cfg.ListenerChats, c)
					}
				}
			}
		}
	}

	return config.NormalizeConfig(cfg), nil
}

func (s *Storage) SaveConfig(cfg config.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	pairs := map[string]string{
		"max_concurrent_downloads": strconv.Itoa(cfg.MaxConcurrentDownloads),
		"parallel_chunks":          strconv.FormatBool(cfg.ParallelChunks),
		"chunk_workers":            strconv.Itoa(cfg.ChunkWorkers),
		"download_folder":          cfg.DownloadFolder,
		"listener_enabled":         strconv.FormatBool(cfg.ListenerEnabled),
		"organize_by_chat":         strconv.FormatBool(cfg.OrganizeByChat),
		"minimize_to_tray":         strconv.FormatBool(cfg.MinimizeToTray),
		"sound_notification":       strconv.FormatBool(cfg.SoundNotification),
		"speed_value":              fmt.Sprintf("%f", cfg.SpeedLimit.Value),
		"speed_unit":               cfg.SpeedLimit.Unit,
		"language":                 cfg.Language,
		"notification_bot_enabled": strconv.FormatBool(cfg.NotificationBotEnabled),
		"notification_bot_token":   cfg.NotificationBotToken,
		"notification_chat_id":     strconv.FormatInt(cfg.NotificationChatID, 10),
		"bot_notifications":        strconv.FormatBool(cfg.BotNotifications),
	}

	if cfg.NotificationTopicID != nil {
		pairs["notification_topic_id"] = strconv.FormatInt(*cfg.NotificationTopicID, 10)
	} else {
		pairs["notification_topic_id"] = "None"
	}

	// shutdown_when_done queda fuera a propósito: no debe sobrevivir a un
	// reinicio, o un apagado armado ayer se ejecutaría mañana sin que el
	// usuario lo haya pedido en esta sesión.

	if cfg.ColorID != nil {
		pairs["color_id"] = strconv.Itoa(*cfg.ColorID)
	} else {
		pairs["color_id"] = "None"
	}

	if cfg.LoaderColorID != nil {
		pairs["loader_color_id"] = strconv.Itoa(*cfg.LoaderColorID)
	} else {
		pairs["loader_color_id"] = "None"
	}

	for k, v := range pairs {
		_, err := tx.Exec(`
			INSERT INTO app_config(key, value) VALUES(?, ?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value
		`, k, v)
		if err != nil {
			return err
		}
	}

	_, _ = tx.Exec("DELETE FROM listener_chats")
	for _, c := range cfg.ListenerChats {
		auto := 0
		if c.AutoDownload {
			auto = 1
		}
		fPhotos, fVideos, fAudios, fDocs, fStickers := 1, 1, 1, 1, 1
		if !c.FPhotos {
			fPhotos = 0
		}
		if !c.FVideos {
			fVideos = 0
		}
		if !c.FAudios {
			fAudios = 0
		}
		if !c.FDocs {
			fDocs = 0
		}
		if !c.FStickers {
			fStickers = 0
		}

		nameMode := c.NameMode
		if nameMode == "" {
			nameMode = "manual"
		}

		name := c.Name
		if name == "" {
			name = strconv.FormatInt(c.ID, 10)
		}

		_, err := tx.Exec(`
			INSERT OR REPLACE INTO listener_chats(chat_id, topic_id, topic_name, name, folder, topic_folder, auto_download, f_photos, f_videos, f_audios, f_docs, f_stickers, name_mode)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, c.ID, c.Topic(), c.TopicName, name, c.Folder, c.TopicFolder, auto, fPhotos, fVideos, fAudios, fDocs, fStickers, nameMode)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *Storage) LoadDownloads(legacyPath string) (map[string]DownloadItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	items := make(map[string]DownloadItem)

	// Importación legacy si no hay datos
	var count int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM downloads").Scan(&count)
	if count == 0 {
		targetLegacy := findExistingFile(legacyPath, filepath.Join(config.DataDir, "downloads.json"), filepath.Join(config.BaseDir, "downloads.json"))
		if targetLegacy != "" {
			if data, err := os.ReadFile(targetLegacy); err == nil {
				var raw map[string]map[string]any
				if json.Unmarshal(data, &raw) == nil {
					now := float64(time.Now().Unix())
					for id, item := range raw {
						fileName, _ := item["file_name"].(string)
						status, _ := item["status"].(string)
						if status == "" {
							status = "failed"
						}
						totalStr, _ := item["total_str"].(string)
						currentStr, _ := item["current_str"].(string)
						speed, _ := item["speed"].(string)
						kind, _ := item["kind"].(string)
						filePath, _ := item["file_path"].(string)
						source, _ := item["source"].(string)
						jobID, _ := item["job_id"].(string)

						_, _ = s.db.Exec(`
						INSERT INTO downloads(
							id, job_id, message_id, chat_id, file_name,
							status, progress, total_str, current_str, speed,
							kind, file_path, source, updated_at, created_at
						) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
					`, id, jobID, config.ParseInt64(item["message_id"]),
							config.ParseInt64(item["chat_id"]), fileName,
							status, 0.0, totalStr, currentStr, speed,
							kind, filePath, source, now, now)
					}
				}
			}
		}
	}

	rows, err := s.db.Query("SELECT id, job_id, message_id, chat_id, file_name, status, progress, total_str, current_str, speed, kind, file_path, sub_folder, source, error, updated_at, created_at, total_bytes, current_bytes, caption_file_name, original_file_name FROM downloads ORDER BY updated_at DESC")
	if err != nil {
		return items, err
	}
	defer rows.Close()

	for rows.Next() {
		var item DownloadItem
		var jobID, kind, filePath, subFolder, source, errStr, captionFileName, originalFileName sql.NullString
		var msgID, chatID, totalB, currB sql.NullInt64

		err := rows.Scan(
			&item.ID, &jobID, &msgID, &chatID, &item.FileName,
			&item.Status, &item.Progress, &item.TotalStr, &item.CurrentStr,
			&item.Speed, &kind, &filePath, &subFolder, &source, &errStr, &item.UpdatedAt,
			&item.CreatedAt, &totalB, &currB, &captionFileName, &originalFileName,
		)
		if err == nil {
			if jobID.Valid {
				item.JobID = jobID.String
			}
			if kind.Valid {
				item.Kind = kind.String
			}
			if filePath.Valid {
				item.FilePath = filePath.String
			}
			if subFolder.Valid {
				item.SubFolder = subFolder.String
			}
			if source.Valid {
				item.Source = source.String
			}
			if errStr.Valid {
				item.Error = errStr.String
			}
			if captionFileName.Valid {
				item.CaptionFileName = captionFileName.String
			}
			if originalFileName.Valid {
				item.OriginalFileName = originalFileName.String
			}
			if msgID.Valid {
				item.MessageID = msgID.Int64
			}
			if chatID.Valid {
				item.ChatID = chatID.Int64
			}
			if totalB.Valid {
				item.TotalBytes = totalB.Int64
			}
			if currB.Valid {
				item.CurrentBytes = currB.Int64
			}
			items[item.ID] = item
		}
	}
	if err := rows.Err(); err != nil {
		return items, err
	}

	return items, nil
}

func (s *Storage) SaveDownload(item DownloadItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := float64(time.Now().Unix())
	if item.UpdatedAt == 0 {
		item.UpdatedAt = now
	}
	if item.CreatedAt == 0 {
		item.CreatedAt = now
	}

	_, err := s.db.Exec(`
		INSERT INTO downloads(
			id, job_id, message_id, chat_id, file_name,
			status, progress, total_str, current_str, speed,
			kind, file_path, sub_folder, source, error, updated_at, created_at, total_bytes, current_bytes,
			caption_file_name, original_file_name
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			file_name=excluded.file_name,
			status=excluded.status,
			progress=excluded.progress,
			total_str=excluded.total_str,
			current_str=excluded.current_str,
			speed=excluded.speed,
			updated_at=excluded.updated_at,
			file_path=excluded.file_path,
			sub_folder=excluded.sub_folder,
			kind=excluded.kind,
			error=excluded.error,
			total_bytes=excluded.total_bytes,
			current_bytes=excluded.current_bytes,
			caption_file_name=excluded.caption_file_name,
			original_file_name=excluded.original_file_name
	`,
		item.ID, item.JobID, item.MessageID, item.ChatID, item.FileName,
		item.Status, item.Progress, item.TotalStr, item.CurrentStr, item.Speed,
		item.Kind, item.FilePath, item.SubFolder, item.Source, item.Error, item.UpdatedAt, item.CreatedAt,
		item.TotalBytes, item.CurrentBytes, item.CaptionFileName, item.OriginalFileName,
	)

	return err
}

func (s *Storage) DeleteDownload(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("DELETE FROM downloads WHERE id=?", id)
	return err
}

func (s *Storage) UpdateDownloadFileName(id string, newFileName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("UPDATE downloads SET file_name=? WHERE id=?", newFileName, id)
	return err
}

func (s *Storage) UpdateDownloadCreatedAt(id string, createdAt float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("UPDATE downloads SET created_at=? WHERE id=?", createdAt, id)
	return err
}

func (s *Storage) Chunks(downloadID string) (map[int64]bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	res := make(map[int64]bool)
	rows, err := s.db.Query("SELECT chunk_index FROM download_chunks WHERE download_id=?", downloadID)
	if err != nil {
		return res, err
	}
	defer rows.Close()

	for rows.Next() {
		var idx int64
		if err := rows.Scan(&idx); err == nil {
			res[idx] = true
		}
	}
	if err := rows.Err(); err != nil {
		return res, err
	}
	return res, nil
}

func (s *Storage) AddChunk(downloadID string, chunkIndex int64) error {
	return s.AddChunks(downloadID, []int64{chunkIndex})
}

func (s *Storage) AddChunks(downloadID string, indices []int64) error {
	if len(indices) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare("INSERT OR IGNORE INTO download_chunks(download_id, chunk_index) VALUES(?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, idx := range indices {
		// No se ignora el error: con foreign_keys activo, un chunk cuyo
		// download_id no exista se rechaza, y tragarse ese error dejaba la
		// tabla vacía sin avisar a nadie.
		if _, err := stmt.Exec(downloadID, idx); err != nil {
			return fmt.Errorf(i18n.T("storage.saveChunkErr"), idx, downloadID, err)
		}
	}

	return tx.Commit()
}

func (s *Storage) DeleteChunks(downloadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("DELETE FROM download_chunks WHERE download_id=?", downloadID)
	return err
}

// ClearFinishedDownloads borra del historial las descargas terminadas y los
// chunks huérfanos, y después devuelve el espacio al disco con un VACUUM.
// Los DELETE van con s.mu tomado porque son operaciones rápidas; el VACUUM se
// ejecuta fuera del candado porque necesita escritura exclusiva y en una base
// grande tarda segundos: con s.mu tomado durante ese rato quedaban congeladas
// el progreso, los guardados de descargas y el arranque de nuevas.
func (s *Storage) ClearFinishedDownloads() (int64, error) {
	rows, err := s.deleteFinishedAndOrphanChunks()
	if err != nil {
		return 0, err
	}

	s.vacuumMu.Lock()
	defer s.vacuumMu.Unlock()

	// El fallo del VACUUM no tumba la operación: los DELETE de arriba ya se
	// aplicaron y las filas se borraron, así que se sigue devolviendo
	// (rows, nil). Tampoco se traga el error: log.Printf está enganchado a
	// logbus (logbus.Attach), por lo que la línea acaba en la vista de logs.
	// Va sin clave i18n porque los diccionarios no forman parte de este
	// cambio; el texto va en inglés por ser un mensaje de log interno.
	if _, err := s.db.Exec("VACUUM"); err != nil {
		log.Printf("[STORAGE] vacuum failed: %v", err)
	}

	return rows, nil
}

// deleteFinishedAndOrphanChunks ejecuta los dos DELETE de la limpieza con s.mu
// tomado y devuelve las filas borradas de downloads. El VACUUM no entra aquí:
// se queda en ClearFinishedDownloads, fuera del candado.
func (s *Storage) deleteFinishedAndOrphanChunks() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Borrar registros de descargas terminadas del historial
	res, err := s.db.Exec("DELETE FROM downloads WHERE status IN ('completed', 'skipped', 'failed', 'cancelled', 'duplicate')")
	if err != nil {
		return 0, fmt.Errorf("error al borrar descargas terminadas: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("error al contar las descargas borradas: %w", err)
	}

	// 2. Borrar CUALQUIER chunk que no tenga una descarga asociada (limpieza de huérfanos)
	// Esto soluciona casos donde las descargas se borraron pero los chunks quedaron atrás.
	if _, err := s.db.Exec("DELETE FROM download_chunks WHERE download_id NOT IN (SELECT id FROM downloads)"); err != nil {
		return 0, fmt.Errorf("error al borrar chunks huérfanos: %w", err)
	}

	return rows, nil
}

type RecentChat struct {
	ID         int64   `json:"id"`
	Target     string  `json:"target"`
	ChatID     int64   `json:"chat_id"`
	ChatName   string  `json:"chat_name"`
	Username   string  `json:"username"`
	IsFavorite bool    `json:"is_favorite"`
	UpdatedAt  float64 `json:"updated_at"`
}

// SaveRecentChat guarda o actualiza un chat consultado en el escáner histórico.
func (s *Storage) SaveRecentChat(target string, chatID int64, chatName string, username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	target = strings.TrimSpace(target)
	chatName = strings.TrimSpace(chatName)
	username = strings.TrimSpace(username)
	if target == "" && chatName == "" {
		return nil
	}

	now := float64(time.Now().UnixNano()) / 1e9

	var existingID int64
	var err error

	if chatID != 0 {
		err = s.db.QueryRow("SELECT id FROM scanner_recent_chats WHERE chat_id = ?", chatID).Scan(&existingID)
	} else {
		err = s.db.QueryRow("SELECT id FROM scanner_recent_chats WHERE LOWER(target) = LOWER(?)", target).Scan(&existingID)
	}

	if err == nil && existingID > 0 {
		_, updateErr := s.db.Exec(`
			UPDATE scanner_recent_chats
			SET target = ?, chat_name = ?, username = ?, updated_at = ?
			WHERE id = ?
		`, target, chatName, username, now, existingID)
		if updateErr != nil {
			return fmt.Errorf("error actualizando chat reciente: %w", updateErr)
		}
	} else {
		_, insertErr := s.db.Exec(`
			INSERT INTO scanner_recent_chats (target, chat_id, chat_name, username, is_favorite, updated_at)
			VALUES (?, ?, ?, ?, 0, ?)
		`, target, chatID, chatName, username, now)
		if insertErr != nil {
			return fmt.Errorf("error guardando chat reciente: %w", insertErr)
		}
	}

	_, _ = s.db.Exec(`
		DELETE FROM scanner_recent_chats
		WHERE is_favorite = 0 AND id NOT IN (
			SELECT id FROM scanner_recent_chats
			WHERE is_favorite = 0
			ORDER BY updated_at DESC
			LIMIT 50
		)
	`)

	return nil
}

// GetRecentChats devuelve la lista de chats recientes ordenados por favoritos y fecha.
func (s *Storage) GetRecentChats() ([]RecentChat, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, target, chat_id, chat_name, username, is_favorite, updated_at
		FROM scanner_recent_chats
		ORDER BY is_favorite DESC, updated_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("error consultando chats recientes: %w", err)
	}
	defer rows.Close()

	recents := make([]RecentChat, 0)
	for rows.Next() {
		var item RecentChat
		var isFav int
		if err := rows.Scan(&item.ID, &item.Target, &item.ChatID, &item.ChatName, &item.Username, &isFav, &item.UpdatedAt); err != nil {
			continue
		}
		item.IsFavorite = (isFav != 0)
		recents = append(recents, item)
	}

	return recents, nil
}

// ToggleRecentChatFavorite marca o desmarca un chat reciente como favorito.
func (s *Storage) ToggleRecentChatFavorite(id int64, isFavorite bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	favVal := 0
	if isFavorite {
		favVal = 1
	}

	_, err := s.db.Exec("UPDATE scanner_recent_chats SET is_favorite = ? WHERE id = ?", favVal, id)
	if err != nil {
		return fmt.Errorf("error cambiando favorito en chat reciente: %w", err)
	}
	return nil
}

// DeleteRecentChat elimina un chat reciente y todos sus datos escaneados en SQLite.
func (s *Storage) DeleteRecentChat(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var chatID int64
	_ = s.db.QueryRow("SELECT chat_id FROM scanner_recent_chats WHERE id = ?", id).Scan(&chatID)

	_, err := s.db.Exec("DELETE FROM scanner_recent_chats WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("error eliminando chat reciente: %w", err)
	}

	if chatID != 0 {
		_, _ = s.db.Exec("DELETE FROM scanner_cache_items WHERE chat_id = ?", chatID)
		_, _ = s.db.Exec("DELETE FROM scanner_cache_ranges WHERE chat_id = ?", chatID)
	}

	return nil
}

// ClearRecentChats elimina todos los chats recientes no favoritos y sus datos escaneados en SQLite.
func (s *Storage) ClearRecentChats() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query("SELECT chat_id FROM scanner_recent_chats WHERE is_favorite = 0")
	if err == nil {
		var chatIDs []int64
		for rows.Next() {
			var cid int64
			if err := rows.Scan(&cid); err == nil && cid != 0 {
				chatIDs = append(chatIDs, cid)
			}
		}
		rows.Close()

		for _, cid := range chatIDs {
			_, _ = s.db.Exec("DELETE FROM scanner_cache_items WHERE chat_id = ?", cid)
			_, _ = s.db.Exec("DELETE FROM scanner_cache_ranges WHERE chat_id = ?", cid)
		}
	}

	_, err = s.db.Exec("DELETE FROM scanner_recent_chats WHERE is_favorite = 0")
	if err != nil {
		return fmt.Errorf("error limpiando historial de chats recientes: %w", err)
	}

	return nil
}

type ScanItem struct {
	ID               string `json:"id"`
	ChatID           int64  `json:"chat_id"`
	ChatName         string `json:"chat_name"`
	MessageID        int64  `json:"message_id"`
	FileName         string `json:"file_name"`
	CaptionFileName  string `json:"caption_file_name,omitempty"`
	OriginalFileName string `json:"original_file_name,omitempty"`
	Kind             string `json:"kind"`
	FileSize         int64  `json:"file_size"`
	FormattedSize    string `json:"formatted_size"`
	Date             int64  `json:"date"`
	DateStr          string `json:"date_str"`
	IsDeleted        bool   `json:"is_deleted"`
}

type DateRange struct {
	StartTS int64 `json:"start_ts"`
	EndTS   int64 `json:"end_ts"`
}

// SaveScannerCacheItems guarda o actualiza elementos detectados durante un escaneo en SQLite.
func (s *Storage) SaveScannerCacheItems(items []ScanItem) error {
	if len(items) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO scanner_cache_items (
			id, chat_id, chat_name, message_id, file_name, caption_file_name,
			original_file_name, kind, file_size, formatted_size, date, date_str, is_deleted
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, COALESCE((SELECT is_deleted FROM scanner_cache_items WHERE id = ?), 0))
		ON CONFLICT(id) DO UPDATE SET
			file_name = excluded.file_name,
			caption_file_name = excluded.caption_file_name,
			original_file_name = excluded.original_file_name,
			formatted_size = excluded.formatted_size
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, item := range items {
		deletedVal := 0
		if item.IsDeleted {
			deletedVal = 1
		}
		_, err := stmt.Exec(
			item.ID,
			item.ChatID,
			item.ChatName,
			item.MessageID,
			item.FileName,
			item.CaptionFileName,
			item.OriginalFileName,
			item.Kind,
			item.FileSize,
			item.FormattedSize,
			item.Date,
			item.DateStr,
			item.ID,
		)
		if err != nil {
			log.Printf("[STORAGE] error guardando elemento de escáner %s: %v", item.ID, err)
		}
		if deletedVal == 1 {
			_, _ = tx.Exec("UPDATE scanner_cache_items SET is_deleted = 1 WHERE id = ?", item.ID)
		}
	}

	return tx.Commit()
}

// SaveScannerCacheRanges guarda los rangos de fechas ya escaneados para un chat en SQLite.
func (s *Storage) SaveScannerCacheRanges(chatID int64, ranges []DateRange) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec("DELETE FROM scanner_cache_ranges WHERE chat_id = ?", chatID)
	if err != nil {
		return err
	}

	stmt, err := tx.Prepare("INSERT INTO scanner_cache_ranges (chat_id, start_ts, end_ts) VALUES (?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range ranges {
		if _, err := stmt.Exec(chatID, r.StartTS, r.EndTS); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetScannerCache recupera los elementos y rangos cubiertos guardados en SQLite para un chat.
func (s *Storage) GetScannerCache(chatID int64) ([]ScanItem, []DateRange, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]ScanItem, 0)
	rows, err := s.db.Query(`
		SELECT id, chat_id, chat_name, message_id, file_name, caption_file_name,
		       original_file_name, kind, file_size, formatted_size, date, date_str, is_deleted
		FROM scanner_cache_items
		WHERE chat_id = ?
		ORDER BY date DESC
	`, chatID)
	if err == nil {
		for rows.Next() {
			var it ScanItem
			var isDel int
			if err := rows.Scan(
				&it.ID, &it.ChatID, &it.ChatName, &it.MessageID, &it.FileName,
				&it.CaptionFileName, &it.OriginalFileName, &it.Kind, &it.FileSize,
				&it.FormattedSize, &it.Date, &it.DateStr, &isDel,
			); err == nil {
				it.IsDeleted = (isDel != 0)
				items = append(items, it)
			}
		}
		rows.Close()
	}

	ranges := make([]DateRange, 0)
	rRows, err := s.db.Query("SELECT start_ts, end_ts FROM scanner_cache_ranges WHERE chat_id = ?", chatID)
	if err == nil {
		for rRows.Next() {
			var dr DateRange
			if err := rRows.Scan(&dr.StartTS, &dr.EndTS); err == nil {
				ranges = append(ranges, dr)
			}
		}
		rRows.Close()
	}

	return items, ranges, nil
}

// SetScannerItemDeleted marca o desmarca un elemento escaneado como eliminado/inexistente en Telegram.
func (s *Storage) SetScannerItemDeleted(chatID int64, messageID int64, isDeleted bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delVal := 0
	if isDeleted {
		delVal = 1
	}

	_, err := s.db.Exec("UPDATE scanner_cache_items SET is_deleted = ? WHERE chat_id = ? AND message_id = ?", delVal, chatID, messageID)
	return err
}

// DeleteScannerCacheByChat elimina todos los datos escaneados de un chat en SQLite.
func (s *Storage) DeleteScannerCacheByChat(chatID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, _ = s.db.Exec("DELETE FROM scanner_cache_items WHERE chat_id = ?", chatID)
	_, _ = s.db.Exec("DELETE FROM scanner_cache_ranges WHERE chat_id = ?", chatID)
	return nil
}

