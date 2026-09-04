package telegram

import (
	"context"
	"testing"
	"time"
)

func TestClientManagerInitialization(t *testing.T) {
	cm := NewClientManager()
	if cm == nil {
		t.Fatal("expected non-nil ClientManager")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	status := cm.GetAuthStatus(ctx)
	if status.Configured {
		t.Errorf("expected Configured=false for empty credentials, got true")
	}

	// Probar InitClient con credenciales vacías
	err := cm.InitClient("", "")
	if err != nil {
		t.Fatalf("InitClient with empty credentials failed: %v", err)
	}

	status = cm.GetAuthStatus(ctx)
	if status.Configured {
		t.Errorf("expected Configured=false after empty InitClient, got true")
	}
}
