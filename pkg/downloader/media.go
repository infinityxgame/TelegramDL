package downloader

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gotd/td/tg"
)

type MediaKind string

const (
	KindPhoto   MediaKind = "photo"
	KindVideo   MediaKind = "video"
	KindSong    MediaKind = "song"
	KindSticker MediaKind = "sticker"
	KindFile    MediaKind = "file"
)

type MediaInfo struct {
	Location tg.InputFileLocationClass
	FileName string
	Kind     MediaKind
	FileSize int64
}

var invalidPathChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

func SanitizeFileName(name string) string {
	clean := invalidPathChars.ReplaceAllString(name, "_")
	clean = strings.TrimSpace(clean)
	clean = strings.Trim(clean, ". ")
	if clean == "" {
		return "archivo"
	}
	if len(clean) > 200 {
		ext := filepath.Ext(clean)
		base := clean[:200-len(ext)]
		clean = base + ext
	}
	return clean
}

func ExtractMediaInfo(msg *tg.Message) *MediaInfo {
	if msg == nil || msg.Media == nil {
		return nil
	}

	caption := msg.Message
	firstCaptionLine := ""
	if caption != "" {
		lines := strings.Split(caption, "\n")
		firstCaptionLine = strings.TrimSpace(lines[0])
		if len(firstCaptionLine) > 50 {
			firstCaptionLine = firstCaptionLine[:50]
		}
	}

	switch m := msg.Media.(type) {
	case *tg.MessageMediaDocument:
		doc, ok := m.Document.(*tg.Document)
		if !ok || doc == nil {
			return nil
		}

		kind := KindFile
		fileName := ""
		for _, attr := range doc.Attributes {
			switch a := attr.(type) {
			case *tg.DocumentAttributeFilename:
				fileName = a.FileName
			case *tg.DocumentAttributeVideo:
				kind = KindVideo
			case *tg.DocumentAttributeAudio:
				kind = KindSong
			case *tg.DocumentAttributeSticker:
				kind = KindSticker
			case *tg.DocumentAttributeAnimated:
				kind = KindVideo
			}
		}

		if fileName == "" {
			if kind == KindVideo {
				fileName = fmt.Sprintf("video_%d.mp4", msg.ID)
			} else if kind == KindSong {
				fileName = fmt.Sprintf("audio_%d.mp3", msg.ID)
			} else if kind == KindSticker {
				fileName = fmt.Sprintf("sticker_%d.webp", msg.ID)
			} else {
				fileName = fmt.Sprintf("file_%d", msg.ID)
			}
		}

		// Si el nombre es genérico pero hay caption, usar caption
		ext := filepath.Ext(fileName)
		lowerName := strings.ToLower(fileName)
		genericPrefixes := []string{"video_", "doc_", "music_", "audio_", "sticker_", "file_"}
		isGeneric := false
		for _, p := range genericPrefixes {
			if strings.HasPrefix(lowerName, p) {
				isGeneric = true
				break
			}
		}

		if isGeneric && firstCaptionLine != "" {
			fileName = SanitizeFileName(firstCaptionLine) + ext
		}

		// Refinar por mime-type si sigue como KindFile
		mime := strings.ToLower(doc.MimeType)
		lowerExt := strings.ToLower(ext)
		if kind == KindFile {
			if strings.HasPrefix(mime, "image/") || lowerExt == ".jpg" || lowerExt == ".png" || lowerExt == ".webp" || lowerExt == ".gif" {
				kind = KindPhoto
			} else if strings.HasPrefix(mime, "video/") || lowerExt == ".mp4" || lowerExt == ".mkv" || lowerExt == ".webm" || lowerExt == ".avi" || lowerExt == ".mov" {
				kind = KindVideo
			} else if strings.HasPrefix(mime, "audio/") || lowerExt == ".mp3" || lowerExt == ".m4a" || lowerExt == ".flac" || lowerExt == ".wav" || lowerExt == ".ogg" {
				kind = KindSong
			}
		}

		loc := doc.AsInputDocumentFileLocation("")
		return &MediaInfo{
			Location: loc,
			FileName: SanitizeFileName(fileName),
			Kind:     kind,
			FileSize: doc.Size,
		}

	case *tg.MessageMediaPhoto:
		photo, ok := m.Photo.(*tg.Photo)
		if !ok || photo == nil {
			return nil
		}

		fileName := fmt.Sprintf("photo_%d.jpg", msg.ID)
		if firstCaptionLine != "" {
			fileName = fmt.Sprintf("%s.jpg", SanitizeFileName(firstCaptionLine))
		}

		// Encontrar la versión más grande de la foto
		var largest tg.PhotoSizeClass
		for _, size := range photo.Sizes {
			switch s := size.(type) {
			case *tg.PhotoSize:
				largest = s
			case *tg.PhotoSizeProgressive:
				largest = s
			}
		}

		var thumbType string
		if largest != nil {
			thumbType = largest.GetType()
		} else {
			thumbType = "y"
		}

		loc := &tg.InputPhotoFileLocation{
			ID:            photo.ID,
			AccessHash:    photo.AccessHash,
			FileReference: photo.FileReference,
			ThumbSize:     thumbType,
		}

		return &MediaInfo{
			Location: loc,
			FileName: SanitizeFileName(fileName),
			Kind:     KindPhoto,
			FileSize: 0, // Las fotos en Telegram no siempre reportan tamaño estricto previo
		}
	}

	return nil
}
