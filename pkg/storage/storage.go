package storage

import (
	"database/sql"
	"encoding/json"
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
		return nil, fmt.Errorf("error al abrir sqlite: %w", err)
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
	_, _ = s.db.Exec("PRAGMA journal_mode=WAL;")
	_, _ = s.db.Exec("PRAGMA synchronous=NORMAL;")

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
	`

	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("error al inicializar esquema: %w", err)
	}

	// Migraciones defensivas (idénticas a Python)
	_, _ = s.db.Exec("ALTER TABLE downloads ADD COLUMN total_bytes INTEGER NOT NULL DEFAULT 0")
	_, _ = s.db.Exec("ALTER TABLE downloads ADD COLUMN current_bytes INTEGER NOT NULL DEFAULT 0")
	_, _ = s.db.Exec("ALTER TABLE downloads ADD COLUMN error TEXT")
	_, _ = s.db.Exec("ALTER TABLE downloads ADD COLUMN sub_folder TEXT")
	// Nombres alternativos para selección manual
	_, _ = s.db.Exec("ALTER TABLE downloads ADD COLUMN caption_file_name TEXT")
	_, _ = s.db.Exec("ALTER TABLE downloads ADD COLUMN original_file_name TEXT")
	for _, col := range []string{"f_photos", "f_videos", "f_audios", "f_docs", "f_stickers"} {
		_, _ = s.db.Exec(fmt.Sprintf("ALTER TABLE listener_chats ADD COLUMN %s INTEGER NOT NULL DEFAULT 1", col))
	}
	// Carpeta asignada a cada chat vigilado. Las bases de datos que vienen de
	// una versión anterior no tienen estas columnas.
	for _, col := range []string{"folder", "topic_folder"} {
		_, _ = s.db.Exec(fmt.Sprintf("ALTER TABLE listener_chats ADD COLUMN %s TEXT NOT NULL DEFAULT ''", col))
	}
	// Selección manual de nombres para archivos de la escucha
	_, _ = s.db.Exec("ALTER TABLE listener_chats ADD COLUMN manual_name_selection INTEGER NOT NULL DEFAULT 0")

	if err := s.migrateListenerTopics(); err != nil {
		return err
	}

	return nil
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
		manual_name_selection INTEGER NOT NULL DEFAULT 0,
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
			f_photos, f_videos, f_audios, f_docs, f_stickers, manual_name_selection
		)
		SELECT chat_id, 0, '', name, auto_download,
			f_photos, f_videos, f_audios, f_docs, f_stickers, 0
		FROM listener_chats_legacy`,
		"DROP TABLE listener_chats_legacy",
	}
	for _, step := range steps {
		if _, err := tx.Exec(step); err != nil {
			return fmt.Errorf("error al migrar listener_chats a temas: %w", err)
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
	_ = s.db.QueryRow("SELECT value FROM app_config WHERE key = 'api_id' OR key = 'tgdl_api_id' ORDER BY key ASC LIMIT 1").Scan(&apiID)
	_ = s.db.QueryRow("SELECT value FROM app_config WHERE key = 'api_hash' OR key = 'tgdl_api_hash' ORDER BY key ASC LIMIT 1").Scan(&apiHash)

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

	primero := func(names ...string) string {
		for _, n := range names {
			if v := strings.TrimSpace(values[n]); v != "" {
				return v
			}
		}
		return ""
	}

	s.mu.Lock()

	var migradas []string
	guardar := func(key, value string) {
		if value == "" || s.getConfigKey(key) != "" {
			return
		}
		if err := s.setConfigKey(key, value); err != nil {
			log.Printf("[STORAGE] No se pudo migrar %s desde el .env: %v", key, err)
			return
		}
		migradas = append(migradas, key)
	}

	guardar("api_id", primero("TGDL_API_ID", "API_ID"))
	guardar("api_hash", primero("TGDL_API_HASH", "API_HASH"))
	guardar("bind_host", primero("TGDL_BIND_HOST", "BIND_HOST"))
	guardar("server_port", primero("TGDL_PORT", "PORT"))

	// Si nunca hubo una elección de dirección, se deja escrita la de por
	// defecto. Antes esto lo hacía el .env recién creado; ahora el valor queda
	// visible en app_config para quien quiera cambiarlo.
	guardar("bind_host", config.DefaultBindHost)

	s.mu.Unlock()

	if len(migradas) > 0 {
		log.Printf("[STORAGE] Configuración migrada del .env a la base de datos: %s", strings.Join(migradas, ", "))
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

	// Cargar listener_chats
	chatRows, err := s.db.Query("SELECT chat_id, topic_id, topic_name, name, folder, topic_folder, auto_download, f_photos, f_videos, f_audios, f_docs, f_stickers, manual_name_selection FROM listener_chats ORDER BY chat_id, topic_id")
	if err == nil {
		defer chatRows.Close()
		chats := make([]config.ListenerChat, 0)
		for chatRows.Next() {
			var c config.ListenerChat
			var topicID int64
			var topicName, folder, topicFolder string
			var auto, photos, videos, audios, docs, stickers, manualNameSelection int
			if err := chatRows.Scan(&c.ID, &topicID, &topicName, &c.Name, &folder, &topicFolder, &auto, &photos, &videos, &audios, &docs, &stickers, &manualNameSelection); err == nil {
				c.TopicID = config.TopicPointer(topicID)
				if c.HasTopic() {
					c.TopicName = topicName
					c.TopicFolder = topicFolder
				}
				c.Folder = folder
				c.AutoDownload = auto != 0
				c.ManualNameSelection = manualNameSelection != 0
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
						_, _ = s.db.Exec(`
							INSERT OR REPLACE INTO listener_chats(chat_id, topic_id, topic_name, name, auto_download, f_photos, f_videos, f_audios, f_docs, f_stickers, manual_name_selection)
							VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
						`, c.ID, c.Topic(), c.TopicName, c.Name, c.AutoDownload, c.FPhotos, c.FVideos, c.FAudios, c.FDocs, c.FStickers, c.ManualNameSelection)
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
		"speed_value":              fmt.Sprintf("%f", cfg.SpeedLimit.Value),
		"speed_unit":               cfg.SpeedLimit.Unit,
	}

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
		fPhotos, fVideos, fAudios, fDocs, fStickers, fManualNameSelection := 1, 1, 1, 1, 1, 0
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
		if c.ManualNameSelection {
			fManualNameSelection = 1
		}
		if !c.ManualNameSelection {
			fManualNameSelection = 0
		}

		name := c.Name
		if name == "" {
			name = strconv.FormatInt(c.ID, 10)
		}

		_, err := tx.Exec(`
			INSERT OR REPLACE INTO listener_chats(chat_id, topic_id, topic_name, name, folder, topic_folder, auto_download, f_photos, f_videos, f_audios, f_docs, f_stickers, manual_name_selection)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, c.ID, c.Topic(), c.TopicName, name, c.Folder, c.TopicFolder, auto, fPhotos, fVideos, fAudios, fDocs, fStickers, fManualNameSelection)
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
			return fmt.Errorf("error al guardar el chunk %d de %s: %w", idx, downloadID, err)
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

func (s *Storage) ClearFinishedDownloads() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Borrar registros de descargas terminadas del historial
	res, err := s.db.Exec("DELETE FROM downloads WHERE status IN ('completed', 'skipped', 'failed', 'cancelled', 'duplicate')")
	if err != nil {
		return 0, err
	}
	rows, _ := res.RowsAffected()

	// 2. Borrar CUALQUIER chunk que no tenga una descarga asociada (limpieza de huérfanos)
	// Esto soluciona casos donde las descargas se borraron pero los chunks quedaron atrás.
	_, _ = s.db.Exec("DELETE FROM download_chunks WHERE download_id NOT IN (SELECT id FROM downloads)")

	// 3. Ejecutar VACUUM para liberar el espacio en el archivo de base de datos físicamente
	_, _ = s.db.Exec("VACUUM")

	return rows, nil
}
