package listener

import (
	"testing"

	"tgdown/pkg/config"
)

func TestListenerFilterLogic(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ListenerEnabled = true
	cfg.ListenerChats = []config.ListenerChat{
		{
			ID:           -100999888,
			Name:         "Media Channel",
			AutoDownload: false,
			FPhotos:      true,
			FVideos:      false,
		},
	}

	le := NewListenerEngine(nil, nil, nil, cfg)
	if len(le.chatMap) != 1 {
		t.Fatalf("expected 1 chat in map, got %d", len(le.chatMap))
	}
	c, ok := le.chatMap[-100999888]
	if !ok || !c.FPhotos || c.FVideos {
		t.Errorf("chat config mismatch: %+v", c)
	}
}
