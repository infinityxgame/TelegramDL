package logbus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEmitNeutralizesLineBreaks comprueba (SEC-28) que un message/detail
// forjado con CR/LF no puede introducir líneas físicas falsas en la salida del
// registro (falsificación de nivel/categoría, p. ej. "\n[ERROR] [FAKE] ...").
func TestEmitNeutralizesLineBreaks(t *testing.T) {
	var out strings.Builder
	b := NewBus(16)
	b.mirror = &out

	e := b.Emit(LevelInfo, CatSystem,
		"arranque ok\r\n[ERROR] [FAKE] inyectado",
		"detalle\r\n[WARN] [FAKE] otro")

	if n := strings.Count(out.String(), "\n"); n != 1 {
		t.Fatalf("la salida debe tener exactamente 1 salto de línea (el terminador), tiene %d: %q", n, out.String())
	}
	if strings.ContainsRune(out.String(), '\r') {
		t.Errorf("la salida contiene CR sin normalizar: %q", out.String())
	}
	if strings.ContainsAny(e.Message, "\r\n") || strings.ContainsAny(e.Detail, "\r\n") {
		t.Errorf("la entrada en memoria aún contiene saltos: message=%q detail=%q", e.Message, e.Detail)
	}
	// El texto no se trunca: solo cambia el salto por su marca literal.
	if !strings.Contains(e.Message, `\n[ERROR] [FAKE] inyectado`) {
		t.Errorf("el texto debe conservarse íntegro con el salto neutralizado: %q", e.Message)
	}
	if !strings.Contains(e.Detail, `\n[WARN] [FAKE] otro`) {
		t.Errorf("el detalle debe conservarse íntegro con el salto neutralizado: %q", e.Detail)
	}
}

// TestSanitizeLogField comprueba que CRLF, LF y CR sueltos se neutralizan sin
// truncar el texto.
func TestSanitizeLogField(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a\r\nb", `a\nb`},
		{"a\nb", `a\nb`},
		{"a\rb", `a\nb`},
		{"sin saltos", "sin saltos"},
	}
	for _, c := range cases {
		if got := sanitizeLogField(c.in); got != c.want {
			t.Errorf("sanitizeLogField(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestDailyLogFile comprueba que los logs se guardan en un archivo con formato log_DD-MM-YY.log.
func TestDailyLogFile(t *testing.T) {
	tempDir := t.TempDir()
	b := NewBus(16)

	if err := b.OpenFile(tempDir); err != nil {
		t.Fatalf("OpenFile falló: %v", err)
	}

	b.Emit(LevelInfo, CatSystem, "prueba daily log", "detalle de prueba")
	b.CloseFile()

	today := time.Now().Format("02-01-06")
	expectedName := "log_" + today + ".log"
	expectedPath := filepath.Join(tempDir, "logs", expectedName)

	data, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("no se pudo leer el archivo de log esperado %s: %v", expectedPath, err)
	}

	if !strings.Contains(string(data), "prueba daily log") {
		t.Errorf("el contenido del log no contiene el mensaje esperado: %s", string(data))
	}
}

// TestDailyLogFileRotation comprueba que los logs cambian de archivo automáticamente al cambiar de día.
func TestDailyLogFileRotation(t *testing.T) {
	tempDir := t.TempDir()
	b := NewBus(16)

	if err := b.OpenFile(tempDir); err != nil {
		t.Fatalf("OpenFile falló: %v", err)
	}

	day1 := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

	b.writeFileForTime(day1, "entrada dia 19")
	b.writeFileForTime(day2, "entrada dia 20")
	b.CloseFile()

	path1 := filepath.Join(tempDir, "logs", "log_19-10-26.log")
	path2 := filepath.Join(tempDir, "logs", "log_20-10-26.log")

	data1, err1 := os.ReadFile(path1)
	if err1 != nil {
		t.Fatalf("error leyendo log dia 1: %v", err1)
	}
	if !strings.Contains(string(data1), "entrada dia 19") {
		t.Errorf("el log del día 19 no contiene 'entrada dia 19': %s", string(data1))
	}

	data2, err2 := os.ReadFile(path2)
	if err2 != nil {
		t.Fatalf("error leyendo log dia 2: %v", err2)
	}
	if !strings.Contains(string(data2), "entrada dia 20") {
		t.Errorf("el log del día 20 no contiene 'entrada dia 20': %s", string(data2))
	}
}
