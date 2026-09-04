package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"tgdown/pkg/config"
	"tgdown/pkg/downloader"
	"tgdown/pkg/listener"
	"tgdown/pkg/storage"
	"tgdown/pkg/telegram"
	"tgdown/pkg/updater"
)

func TestServerEndpoints(t *testing.T) {
	tmpDir := t.TempDir()
	st, err := storage.NewStorage(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer st.Close()

	cfg := config.DefaultConfig()
	cfg.DownloadFolder = tmpDir

	cm := telegram.NewClientManager()
	eng := downloader.NewEngine(cm, st, cfg)
	le := listener.NewListenerEngine(cm, st, eng, cfg)
	up := updater.NewAppUpdater()

	srv := NewServer(cm, st, eng, le, up, cfg, nil, func() {})
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	// Probar /api/auth/status
	req := httptest.NewRequest("GET", "/api/auth/status", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	// Probar /api/downloads
	req = httptest.NewRequest("GET", "/api/downloads", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	// Probar /api/settings
	req = httptest.NewRequest("GET", "/api/settings", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	// Probar /api/system/disk
	req = httptest.NewRequest("GET", "/api/system/disk", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	// Probar redirección de / hacia /dashboard/
	req = httptest.NewRequest("GET", "/", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("expected status 302, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/dashboard/" {
		t.Errorf("expected redirect to /dashboard/, got %s", loc)
	}

	// Probar redirección de /favicon.ico hacia /dashboard/favicon.ico
	req = httptest.NewRequest("GET", "/favicon.ico", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("expected status 302, got %d", w.Code)
	}

	_ = os.RemoveAll(tmpDir)
}
