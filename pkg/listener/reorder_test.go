package listener

import (
	"testing"
	"time"

	"tgdown/pkg/config"
	"tgdown/pkg/downloader"
	"tgdown/pkg/storage"
	"tgdown/pkg/telegram"
)

func TestReorderItems(t *testing.T) {
	le := newEngineWithChats()
	now := float64(time.Now().Unix())

	le.items["item1"] = &ListenerItem{ID: "item1", MessageID: 10, FileName: "1.jpg", CreatedAt: now - 10}
	le.items["item2"] = &ListenerItem{ID: "item2", MessageID: 11, FileName: "2.jpg", CreatedAt: now - 5}
	le.items["item3"] = &ListenerItem{ID: "item3", MessageID: 12, FileName: "3.jpg", CreatedAt: now}

	// Reordenar a: item3 (msg 12), item1 (msg 10), item2 (msg 11)
	le.ReorderItems([]string{"item3", "item1", "item2"})

	items := le.GetItems()
	if len(items) != 3 {
		t.Fatalf("se esperaban 3 items, got %d", len(items))
	}
	if items[0].ID != "item3" || items[1].ID != "item1" || items[2].ID != "item2" {
		t.Fatalf("orden incorrecto tras ReorderItems: got [%s, %s, %s]", items[0].ID, items[1].ID, items[2].ID)
	}
}

func TestReorderDownloadOrderPreservedInEngine(t *testing.T) {
	cm := telegram.NewClientManager()
	if err := cm.InitClient("1234567", "0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("InitClient: %v", err)
	}
	t.Cleanup(cm.Stop)

	cfg := config.DefaultConfig()
	eng := downloader.NewEngine(cm, nil, cfg)

	le := newEngineWithChats()

	now := float64(time.Now().Unix())
	le.items["item1"] = &ListenerItem{ID: "item1", MessageID: 10, ChatID: 100, FileName: "1.jpg", CreatedAt: now}
	le.items["item2"] = &ListenerItem{ID: "item2", MessageID: 11, ChatID: 100, FileName: "2.jpg", CreatedAt: now + 1}
	le.items["item3"] = &ListenerItem{ID: "item3", MessageID: 12, ChatID: 100, FileName: "3.jpg", CreatedAt: now + 2}

	// Reordenar a: item3 (msg 12), item1 (msg 10), item2 (msg 11)
	le.ReorderItems([]string{"item3", "item1", "item2"})

	items := le.GetItems()
	if items[0].CreatedAt >= items[1].CreatedAt || items[1].CreatedAt >= items[2].CreatedAt {
		t.Fatalf("CreatedAt no es incremental tras ReorderItems: [%f, %f, %f]", items[0].CreatedAt, items[1].CreatedAt, items[2].CreatedAt)
	}

	eng.QueueItem(storage.DownloadItem{
		ID:        items[0].ID,
		JobID:     "listener:100",
		MessageID: items[0].MessageID,
		ChatID:    items[0].ChatID,
		CreatedAt: items[0].CreatedAt,
	})
	eng.QueueItem(storage.DownloadItem{
		ID:        items[1].ID,
		JobID:     "listener:100",
		MessageID: items[1].MessageID,
		ChatID:    items[1].ChatID,
		CreatedAt: items[1].CreatedAt,
	})
	eng.QueueItem(storage.DownloadItem{
		ID:        items[2].ID,
		JobID:     "listener:100",
		MessageID: items[2].MessageID,
		ChatID:    items[2].ChatID,
		CreatedAt: items[2].CreatedAt,
	})

	dls := eng.GetDownloads()
	if len(dls) != 3 {
		t.Fatalf("se esperaban 3 descargas, got %d", len(dls))
	}

	if dls[0].ID != "item3" || dls[1].ID != "item1" || dls[2].ID != "item2" {
		t.Fatalf("el motor de descargas no respetó el orden reordenado: got [%s, %s, %s]", dls[0].ID, dls[1].ID, dls[2].ID)
	}
}
