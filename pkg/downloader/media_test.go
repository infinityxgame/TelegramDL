package downloader

import (
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

func TestSanitizeFileNameReplacesInvalidChars(t *testing.T) {
	got := SanitizeFileName(`a/b\c:d"e<f>g|h?i*j`)
	if strings.ContainsAny(got, `/\:"<>|?*`) {
		t.Fatalf("quedaron caracteres inválidos: %q", got)
	}
}

func TestSanitizeFileNameEmptyFallsBackToDefault(t *testing.T) {
	if got := SanitizeFileName(""); got != "archivo" {
		t.Fatalf("se esperaba 'archivo', got %q", got)
	}
	if got := SanitizeFileName("   "); got != "archivo" {
		t.Fatalf("un nombre de solo espacios debería caer al valor por defecto, got %q", got)
	}
}

func TestSanitizeFileNameTrimsDotsAndSpaces(t *testing.T) {
	got := SanitizeFileName("  video.mp4  ")
	if got != "video.mp4" {
		t.Fatalf("se esperaba 'video.mp4' sin espacios, got %q", got)
	}
}

func TestSanitizeFileNameTruncatesLongNamesKeepingExtension(t *testing.T) {
	longStem := strings.Repeat("a", 250)
	got := SanitizeFileName(longStem + ".mp4")
	if len(got) > 200 {
		t.Fatalf("el nombre no se truncó a 200 caracteres: len=%d", len(got))
	}
	if !strings.HasSuffix(got, ".mp4") {
		t.Fatalf("se perdió la extensión al truncar: %q", got)
	}
}

func TestExtractMediaInfoDocumentWithFilename(t *testing.T) {
	msg := &tg.Message{
		ID: 42,
		Media: &tg.MessageMediaDocument{
			Document: &tg.Document{
				MimeType: "application/pdf",
				Size:     12345,
				Attributes: []tg.DocumentAttributeClass{
					&tg.DocumentAttributeFilename{FileName: "reporte.pdf"},
				},
			},
		},
	}

	info := ExtractMediaInfo(msg)
	if info == nil {
		t.Fatal("se esperaba MediaInfo, se obtuvo nil")
	}
	if info.FileName != "reporte.pdf" {
		t.Fatalf("nombre de archivo incorrecto: %q", info.FileName)
	}
	if info.Kind != KindFile {
		t.Fatalf("kind incorrecto: %q", info.Kind)
	}
	if info.FileSize != 12345 {
		t.Fatalf("tamaño incorrecto: %d", info.FileSize)
	}
}

func TestExtractMediaInfoVideoWithoutFilenameGeneratesOne(t *testing.T) {
	msg := &tg.Message{
		ID: 99,
		Media: &tg.MessageMediaDocument{
			Document: &tg.Document{
				MimeType: "video/mp4",
				Size:     999,
				Attributes: []tg.DocumentAttributeClass{
					&tg.DocumentAttributeVideo{},
				},
			},
		},
	}

	info := ExtractMediaInfo(msg)
	if info == nil {
		t.Fatal("se esperaba MediaInfo, se obtuvo nil")
	}
	if info.Kind != KindVideo {
		t.Fatalf("kind incorrecto: %q", info.Kind)
	}
	if info.FileName != "video_99.mp4" {
		t.Fatalf("nombre generado incorrecto: %q", info.FileName)
	}
}

func TestExtractMediaInfoAudioWithTitleAndPerformer(t *testing.T) {
	msg := &tg.Message{
		ID: 7,
		Media: &tg.MessageMediaDocument{
			Document: &tg.Document{
				MimeType: "audio/mpeg",
				Size:     321,
				Attributes: []tg.DocumentAttributeClass{
					&tg.DocumentAttributeAudio{Title: "Cancion", Performer: "Artista"},
				},
			},
		},
	}

	info := ExtractMediaInfo(msg)
	if info == nil {
		t.Fatal("se esperaba MediaInfo, se obtuvo nil")
	}
	if info.Kind != KindSong {
		t.Fatalf("kind incorrecto: %q", info.Kind)
	}
	if info.FileName != "Artista - Cancion.mp3" {
		t.Fatalf("nombre generado incorrecto: %q", info.FileName)
	}
}

func TestExtractMediaInfoVoiceMessage(t *testing.T) {
	msg := &tg.Message{
		ID: 3,
		Media: &tg.MessageMediaDocument{
			Document: &tg.Document{
				MimeType: "audio/ogg",
				Size:     50,
				Attributes: []tg.DocumentAttributeClass{
					&tg.DocumentAttributeAudio{Voice: true},
				},
			},
		},
	}

	info := ExtractMediaInfo(msg)
	if info == nil {
		t.Fatal("se esperaba MediaInfo, se obtuvo nil")
	}
	if info.FileName != "voice_3.ogg" {
		t.Fatalf("nombre generado incorrecto: %q", info.FileName)
	}
}

func TestExtractMediaInfoNoMediaReturnsNil(t *testing.T) {
	if info := ExtractMediaInfo(&tg.Message{ID: 1}); info != nil {
		t.Fatalf("un mensaje sin Media debería devolver nil, se obtuvo %+v", info)
	}
	if info := ExtractMediaInfo(nil); info != nil {
		t.Fatal("un mensaje nil debería devolver nil")
	}
}

func TestExtensionFromMimeKnownAndUnknown(t *testing.T) {
	if ext := extensionFromMime("application/zip"); ext != ".zip" {
		t.Fatalf("extensión incorrecta para zip: %q", ext)
	}
	if ext := extensionFromMime("application/x-made-up"); ext != "" {
		t.Fatalf("un mime desconocido debería devolver cadena vacía, got %q", ext)
	}
}
