package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"tgdown/pkg/config"
)

type DownloadItem struct {
	ID           string  `json:"id"`
	JobID        string  `json:"job_id"`
	MessageID    int64   `json:"message_id"`
	ChatID       int64   `json:"chat_id"`
	FileName     string  `json:"file_name"`
	Status       string  `json:"status"`
	Progress     float64 `json:"progress"`
	TotalStr     string  `json:"total_str"`
	CurrentStr   string  `json:"current_str"`
	Speed        string  `json:"speed"`
	Kind         string  `json:"kind"`
	FilePath     string  `json:"file_path"`
	Source       string  `json:"source"`
	UpdatedAt    float64 `json:"updated_at"`
	CreatedAt    float64 `json:"created_at"`
	TotalBytes   int64   `json:"total_bytes"`
	CurrentBytes int64   `json:"current_bytes"`
}

type Storage struct {
	dbPath string
	db     *sql.DB
	mu     sync.RWMutex
}

func NewStorage(dbPath string) (*Storage, error) {
	db, err := sql.Open("sqlite", dbPath)
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

	_, _ = s.db.Exec("PRAGMA journal_mode=WAL;")
	_, _ = s.db.Exec("PRAGMA synchronous=NORMAL;")

	schema := `
	CREATE TABLE IF NOT EXISTS app_config (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS listener_chats (
		chat_id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		auto_download INTEGER NOT NULL DEFAULT 0,
		f_photos INTEGER NOT NULL DEFAULT 1,
		f_videos INTEGER NOT NULL DEFAULT 1,
		f_audios INTEGER NOT NULL DEFAULT 1,
		f_docs INTEGER NOT NULL DEFAULT 1,
		f_stickers INTEGER NOT NULL DEFAULT 1
	);

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
		source TEXT,
		updated_at REAL NOT NULL,
		created_at REAL NOT NULL,
		total_bytes INTEGER NOT NULL DEFAULT 0,
		current_bytes INTEGER NOT NULL DEFAULT 0
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
	for _, col := range []string{"f_photos", "f_videos", "f_audios", "f_docs", "f_stickers"} {
		_, _ = s.db.Exec(fmt.Sprintf("ALTER TABLE listener_chats ADD COLUMN %s INTEGER NOT NULL DEFAULT 1", col))
	}

	return nil
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
		// Fallback a variables de entorno / .env
		envID, envHash := config.LoadEnvCredentials()
		if envID != "" {
			apiID = envID
		}
		if envHash != "" {
			apiHash = envHash
		}

		// Si aún falta apiID, buscar en downloader_session.session de Pyrogram
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
	if val, ok := kv["download_folder"]; ok && val != "" {
		cfg.DownloadFolder = val
	}
	if val, ok := kv["color_id"]; ok && val != "" && val != "None" && val != "null" {
		if c, err := strconv.Atoi(val); err == nil {
			cfg.ColorID = &c
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
	chatRows, err := s.db.Query("SELECT chat_id, name, auto_download, f_photos, f_videos, f_audios, f_docs, f_stickers FROM listener_chats ORDER BY chat_id")
	if err == nil {
		defer chatRows.Close()
		chats := make([]config.ListenerChat, 0)
		for chatRows.Next() {
			var c config.ListenerChat
			var auto, photos, videos, audios, docs, stickers int
			if err := chatRows.Scan(&c.ID, &c.Name, &auto, &photos, &videos, &audios, &docs, &stickers); err == nil {
				c.AutoDownload = auto != 0
				c.FPhotos = photos != 0
				c.FVideos = videos != 0
				c.FAudios = audios != 0
				c.FDocs = docs != 0
				c.FStickers = stickers != 0
				chats = append(chats, c)
			}
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
							INSERT OR REPLACE INTO listener_chats(chat_id, name, auto_download, f_photos, f_videos, f_audios, f_docs, f_stickers)
							VALUES(?, ?, ?, ?, ?, ?, ?, ?)
						`, c.ID, c.Name, c.AutoDownload, c.FPhotos, c.FVideos, c.FAudios, c.FDocs, c.FStickers)
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
		"parallel_chunks":         strconv.FormatBool(cfg.ParallelChunks),
		"chunk_workers":           strconv.Itoa(cfg.ChunkWorkers),
		"download_folder":         cfg.DownloadFolder,
		"listener_enabled":        strconv.FormatBool(cfg.ListenerEnabled),
		"speed_value":             fmt.Sprintf("%f", cfg.SpeedLimit.Value),
		"speed_unit":              cfg.SpeedLimit.Unit,
	}

	if cfg.ColorID != nil {
		pairs["color_id"] = strconv.Itoa(*cfg.ColorID)
	} else {
		pairs["color_id"] = "None"
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

		name := c.Name
		if name == "" {
			name = strconv.FormatInt(c.ID, 10)
		}

		_, err := tx.Exec(`
			INSERT INTO listener_chats(chat_id, name, auto_download, f_photos, f_videos, f_audios, f_docs, f_stickers)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		`, c.ID, name, auto, fPhotos, fVideos, fAudios, fDocs, fStickers)
		if err != nil {
			return err
		}
	}

	err = tx.Commit()
	if err == nil {
		if data, jerr := json.MarshalIndent(cfg, "", "  "); jerr == nil {
			_ = os.WriteFile(filepath.Join(config.BaseDir, "config.json"), data, 0644)
			_ = os.WriteFile(filepath.Join(config.DataDir, "config.json"), data, 0644)
		}
	}
	return err
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

	rows, err := s.db.Query("SELECT id, job_id, message_id, chat_id, file_name, status, progress, total_str, current_str, speed, kind, file_path, source, updated_at, created_at, total_bytes, current_bytes FROM downloads ORDER BY updated_at DESC")
	if err != nil {
		return items, err
	}
	defer rows.Close()

	for rows.Next() {
		var item DownloadItem
		var jobID, kind, filePath, source sql.NullString
		var msgID, chatID, totalB, currB sql.NullInt64

		err := rows.Scan(
			&item.ID, &jobID, &msgID, &chatID, &item.FileName,
			&item.Status, &item.Progress, &item.TotalStr, &item.CurrentStr,
			&item.Speed, &kind, &filePath, &source, &item.UpdatedAt,
			&item.CreatedAt, &totalB, &currB,
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
			if source.Valid {
				item.Source = source.String
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
			kind, file_path, source, updated_at, created_at, total_bytes, current_bytes
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status=excluded.status,
			progress=excluded.progress,
			total_str=excluded.total_str,
			current_str=excluded.current_str,
			speed=excluded.speed,
			updated_at=excluded.updated_at,
			file_path=excluded.file_path,
			kind=excluded.kind,
			total_bytes=excluded.total_bytes,
			current_bytes=excluded.current_bytes
	`,
		item.ID, item.JobID, item.MessageID, item.ChatID, item.FileName,
		item.Status, item.Progress, item.TotalStr, item.CurrentStr, item.Speed,
		item.Kind, item.FilePath, item.Source, item.UpdatedAt, item.CreatedAt,
		item.TotalBytes, item.CurrentBytes,
	)

	return err
}

func (s *Storage) DeleteDownload(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("DELETE FROM downloads WHERE id=?", id)
	return err
}

func (s *Storage) Chunks(downloadID string) (map[int]bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	res := make(map[int]bool)
	rows, err := s.db.Query("SELECT chunk_index FROM download_chunks WHERE download_id=?", downloadID)
	if err != nil {
		return res, err
	}
	defer rows.Close()

	for rows.Next() {
		var idx int
		if err := rows.Scan(&idx); err == nil {
			res[idx] = true
		}
	}
	return res, nil
}

func (s *Storage) AddChunk(downloadID string, chunkIndex int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO download_chunks(download_id, chunk_index)
		VALUES(?, ?)
	`, downloadID, chunkIndex)
	return err
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

	res, err := s.db.Exec("DELETE FROM downloads WHERE status IN ('completed', 'skipped', 'failed', 'cancelled')")
	if err != nil {
		return 0, err
	}
	rows, _ := res.RowsAffected()
	return rows, nil
}
