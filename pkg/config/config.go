package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/joho/godotenv"
)

const (
	AppVersion = "2.1.8"
	GithubRepo = "infinityxgame/tgdown"
)

var SpeedMultipliers = map[string]float64{
	"B":  1.0,
	"KB": 1024.0,
	"MB": 1024.0 * 1024.0,
	"GB": 1024.0 * 1024.0 * 1024.0,
}

type SpeedLimit struct {
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

type ListenerChat struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	AutoDownload bool   `json:"auto_download"`
	FPhotos      bool   `json:"f_photos"`
	FVideos      bool   `json:"f_videos"`
	FAudios      bool   `json:"f_audios"`
	FDocs        bool   `json:"f_docs"`
	FStickers    bool   `json:"f_stickers"`
}

type Config struct {
	MaxConcurrentDownloads int            `json:"max_concurrent_downloads"`
	ParallelChunks         bool           `json:"parallel_chunks"`
	ChunkWorkers           int            `json:"chunk_workers"`
	DownloadFolder         string         `json:"download_folder"`
	ColorID                *int           `json:"color_id"`
	SpeedLimit             SpeedLimit     `json:"speed_limit"`
	ListenerEnabled        bool           `json:"listener_enabled"`
	ListenerChats          []ListenerChat `json:"listener_chats"`
	ListenerChatIDs        []int64        `json:"listener_chat_ids"`
}

var (
	DataDir     string
	UserEnvPath string
	BaseDir     string
	initDirOnce sync.Once
)

func InitPaths() {
	initDirOnce.Do(func() {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		DataDir = filepath.Join(home, ".tgdown")
		_ = os.MkdirAll(DataDir, 0755)

		UserEnvPath = filepath.Join(DataDir, ".env")

		// Base directory
		execPath, err := os.Executable()
		if err == nil {
			BaseDir = filepath.Dir(execPath)
		} else {
			BaseDir = "."
		}

		// Migración de .env legacy si existe
		legacyEnv := filepath.Join(BaseDir, ".env")
		if _, err := os.Stat(UserEnvPath); os.IsNotExist(err) {
			if _, lerr := os.Stat(legacyEnv); lerr == nil {
				if content, rerr := os.ReadFile(legacyEnv); rerr == nil {
					_ = os.WriteFile(UserEnvPath, content, 0600)
				}
			} else if _, cerr := os.Stat(".env"); cerr == nil {
				if content, rerr := os.ReadFile(".env"); rerr == nil {
					_ = os.WriteFile(UserEnvPath, content, 0600)
				}
			}
		}

		// Cargar variables de entorno prioritariamente desde UserEnvPath (.tgdown/.env)
		if _, err := os.Stat(UserEnvPath); err == nil {
			_ = godotenv.Overload(UserEnvPath)
		} else if _, err := os.Stat(legacyEnv); err == nil {
			_ = godotenv.Overload(legacyEnv)
		} else if _, err := os.Stat(".env"); err == nil {
			_ = godotenv.Overload(".env")
		}
	})
}

func DefaultConfig() Config {
	InitPaths()
	return Config{
		MaxConcurrentDownloads: 3,
		ParallelChunks:         true,
		ChunkWorkers:           4,
		DownloadFolder:         filepath.Join(BaseDir, "descargas"),
		ColorID:                nil,
		SpeedLimit: SpeedLimit{
			Value: 0,
			Unit:  "MB",
		},
		ListenerEnabled: true,
		ListenerChats:   []ListenerChat{},
		ListenerChatIDs: []int64{},
	}
}

func LoadEnvCredentials() (string, string) {
	InitPaths()
	// Cargar primero desde UserEnvPath
	_ = godotenv.Load(UserEnvPath)

	apiID := os.Getenv("TGDL_API_ID")
	if apiID == "" {
		apiID = os.Getenv("API_ID")
	}
	apiHash := os.Getenv("TGDL_API_HASH")
	if apiHash == "" {
		apiHash = os.Getenv("API_HASH")
	}

	if apiID == "" || apiHash == "" {
		// Intentar leer desde BaseDir/.env
		_ = godotenv.Load(filepath.Join(BaseDir, ".env"))
		if apiID == "" {
			apiID = os.Getenv("TGDL_API_ID")
			if apiID == "" {
				apiID = os.Getenv("API_ID")
			}
		}
		if apiHash == "" {
			apiHash = os.Getenv("TGDL_API_HASH")
			if apiHash == "" {
				apiHash = os.Getenv("API_HASH")
			}
		}
	}

	return strings.TrimSpace(apiID), strings.TrimSpace(apiHash)
}

func SaveEnvCredentials(apiID, apiHash string) error {
	InitPaths()
	apiID = strings.TrimSpace(apiID)
	apiHash = strings.TrimSpace(apiHash)

	content := fmt.Sprintf("API_ID=%s\nAPI_HASH=%s\nTGDL_API_ID=%s\nTGDL_API_HASH=%s\n", apiID, apiHash, apiID, apiHash)
	err := os.WriteFile(UserEnvPath, []byte(content), 0600)
	if err != nil {
		return err
	}

	_ = os.Setenv("API_ID", apiID)
	_ = os.Setenv("API_HASH", apiHash)
	_ = os.Setenv("TGDL_API_ID", apiID)
	_ = os.Setenv("TGDL_API_HASH", apiHash)
	return nil
}

func GetServerPort() int {
	InitPaths()
	portStr := os.Getenv("TGDL_PORT")
	if portStr == "" {
		portStr = os.Getenv("PORT")
	}
	if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
		return p
	}
	return 8000
}

func GetServerHost() string {
	InitPaths()
	host := os.Getenv("TGDL_BIND_HOST")
	if host == "" {
		host = os.Getenv("BIND_HOST")
	}
	if host == "" {
		return "127.0.0.1"
	}
	return host
}

func NormalizeConfig(raw Config) Config {
	if raw.MaxConcurrentDownloads < 1 {
		raw.MaxConcurrentDownloads = 1
	} else if raw.MaxConcurrentDownloads > 20 {
		raw.MaxConcurrentDownloads = 20
	}

	if raw.ChunkWorkers < 1 {
		raw.ChunkWorkers = 1
	} else if raw.ChunkWorkers > 16 {
		raw.ChunkWorkers = 16
	}

	if strings.TrimSpace(raw.DownloadFolder) == "" {
		raw.DownloadFolder = filepath.Join(BaseDir, "descargas")
	}

	raw.SpeedLimit.Unit = strings.ToUpper(strings.TrimSpace(raw.SpeedLimit.Unit))
	if _, ok := SpeedMultipliers[raw.SpeedLimit.Unit]; !ok {
		raw.SpeedLimit.Unit = "MB"
	}
	if raw.SpeedLimit.Value < 0 {
		raw.SpeedLimit.Value = 0
	}

	chatIDs := make([]int64, 0, len(raw.ListenerChats))
	for i := range raw.ListenerChats {
		chatIDs = append(chatIDs, raw.ListenerChats[i].ID)
	}
	raw.ListenerChatIDs = chatIDs

	return raw
}

func FormatBytes(size float64) string {
	if size <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for size >= 1024 && i < len(units)-1 {
		size /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f B", size)
	}
	return fmt.Sprintf("%.2f %s", size, units[i])
}

func ParseInt64(val any) int64 {
	switch v := val.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case string:
		res, _ := strconv.ParseInt(v, 10, 64)
		return res
	case json.Number:
		res, _ := v.Int64()
		return res
	default:
		return 0
	}
}
