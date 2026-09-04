package storage

import (
	"os"
	"path/filepath"
	"testing"

	"tgdown/pkg/config"
)

func TestStorageLifecycle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "tgdown_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	st, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer st.Close()

	// Config test
	defCfg := config.DefaultConfig()
	defCfg.MaxConcurrentDownloads = 5
	defCfg.ListenerEnabled = true
	defCfg.ListenerChats = []config.ListenerChat{
		{
			ID:           -1001234567,
			Name:         "Test Channel",
			AutoDownload: true,
			FPhotos:      true,
			FVideos:      false,
		},
	}

	if err := st.SaveConfig(defCfg); err != nil {
		t.Fatalf("SaveConfig error: %v", err)
	}

	loaded, err := st.LoadConfig(config.DefaultConfig(), "")
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if loaded.MaxConcurrentDownloads != 5 {
		t.Errorf("expected max concurrent 5, got %d", loaded.MaxConcurrentDownloads)
	}
	if len(loaded.ListenerChats) != 1 || loaded.ListenerChats[0].ID != -1001234567 {
		t.Errorf("listener chats mismatch: %+v", loaded.ListenerChats)
	}

	// Downloads test
	item := DownloadItem{
		ID:         "test_item_1",
		JobID:      "job_1",
		FileName:   "video.mp4",
		Status:     "downloading",
		Progress:   45.5,
		TotalStr:   "100 MB",
		CurrentStr: "45.5 MB",
		Speed:      "2 MB/s",
		Kind:       "video",
		TotalBytes: 104857600,
	}
	if err := st.SaveDownload(item); err != nil {
		t.Fatalf("SaveDownload error: %v", err)
	}

	downloads, err := st.LoadDownloads("")
	if err != nil {
		t.Fatalf("LoadDownloads error: %v", err)
	}
	if d, ok := downloads["test_item_1"]; !ok || d.FileName != "video.mp4" {
		t.Errorf("download item not found or mismatch: %+v", d)
	}

	// Chunks test
	_ = st.AddChunk("test_item_1", 0)
	_ = st.AddChunk("test_item_1", 1)
	_ = st.AddChunk("test_item_1", 2)
	chunks, err := st.Chunks("test_item_1")
	if err != nil {
		t.Fatalf("Chunks error: %v", err)
	}
	if len(chunks) != 3 || !chunks[1] {
		t.Errorf("expected 3 chunks, got %+v", chunks)
	}

	_ = st.DeleteChunks("test_item_1")
	chunks, _ = st.Chunks("test_item_1")
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks after delete, got %d", len(chunks))
	}
}
