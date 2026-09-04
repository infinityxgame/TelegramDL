package downloader

import (
	"testing"
)

func TestParseURL(t *testing.T) {
	// Canal privado con rango
	p1, err := ParseURL("https://t.me/c/1234567890/100-110")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !p1.IsChannelID || p1.ChatID != -1001234567890 || p1.StartMsgID != 100 || p1.EndMsgID != 110 {
		t.Errorf("mismatch in parsed private channel: %+v", p1)
	}

	// Canal privado mensaje único
	p2, err := ParseURL("https://t.me/c/1234567890/55")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p2.StartMsgID != 55 || p2.EndMsgID != 55 {
		t.Errorf("mismatch single msg: %+v", p2)
	}

	// Canal público por username
	p3, err := ParseURL("https://t.me/some_channel/42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p3.IsChannelID || p3.ChatUsername != "some_channel" || p3.StartMsgID != 42 {
		t.Errorf("mismatch username: %+v", p3)
	}

	// URL inválida
	_, err = ParseURL("https://google.com")
	if err == nil {
		t.Error("expected error for invalid URL, got nil")
	}

	// Rango invertido
	_, err = ParseURL("https://t.me/c/123/50-40")
	if err == nil {
		t.Error("expected error for inverted range, got nil")
	}
}

func TestSanitizeFileName(t *testing.T) {
	dirty := `My:Cool*Video?.mp4`
	clean := SanitizeFileName(dirty)
	expected := "My_Cool_Video_.mp4"
	if clean != expected {
		t.Errorf("expected %s, got %s", expected, clean)
	}
}

func TestReservations(t *testing.T) {
	res := NewPathReservations()
	tmpDir := t.TempDir()

	p1, n1, exists1 := res.ReservePath(tmpDir, "video.mp4", 1, 100)
	if exists1 || n1 != "video.mp4" {
		t.Errorf("unexpected: p1=%s, n1=%s, exists=%v", p1, n1, exists1)
	}

	// Misma reserva concurrente sin haber cerrado/liberado
	p2, n2, exists2 := res.ReservePath(tmpDir, "video.mp4", 2, 100)
	if exists2 || n2 != "video_1.mp4" {
		t.Errorf("unexpected collision result: p2=%s, n2=%s, exists=%v", p2, n2, exists2)
	}

	res.ReleasePath(p1)
	res.ReleasePath(p2)
}
