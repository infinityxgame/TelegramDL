package downloader

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"tgdown/pkg/storage"
)

type resumeTestClient struct {
	mu      sync.Mutex
	data    []byte
	offsets []int64
}

func (c *resumeTestClient) UploadGetFile(_ context.Context, req *tg.UploadGetFileRequest) (tg.UploadFileClass, error) {
	c.mu.Lock()
	c.offsets = append(c.offsets, req.Offset)
	c.mu.Unlock()
	end := req.Offset + int64(req.Limit)
	if end > int64(len(c.data)) {
		end = int64(len(c.data))
	}
	return &tg.UploadFile{Bytes: c.data[req.Offset:end]}, nil
}

func TestDownloadMissingPartsSkipsCompletedOffsets(t *testing.T) {
	part := int(downloadPartSize)
	client := &resumeTestClient{data: make([]byte, part*3+10)}
	file, err := os.CreateTemp(t.TempDir(), "resume-*.tmp")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := file.Truncate(int64(len(client.data))); err != nil {
		t.Fatal(err)
	}

	engine := &Engine{
		downloads: map[string]*storage.DownloadItem{
			"resume-test": {ID: "resume-test", Status: "downloading"},
		},
		seenChunks:         make(map[string]map[int64]struct{}),
		itemSpeeds:         make(map[string]float64),
		lastProgressBytes:  make(map[string]int64),
		lastProgressTimes:  make(map[string]time.Time),
		startTimes:         make(map[string]time.Time),
		lastBroadcastTimes: make(map[string]time.Time),
		lastSaveTimes:      make(map[string]time.Time),
	}
	writer := &progressWriterAt{file: file, itemID: "resume-test", engine: engine, total: int64(len(client.data))}
	completed := map[int64]struct{}{0: {}}

	if err := downloadMissingParts(context.Background(), client, nil, writer, int64(len(client.data)), 3, completed); err != nil {
		t.Fatal(err)
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.offsets) != 3 {
		t.Fatalf("expected three missing requests, got %v", client.offsets)
	}
	for _, offset := range client.offsets {
		if offset == 0 {
			t.Fatalf("completed offset was requested again: %v", client.offsets)
		}
	}
}
