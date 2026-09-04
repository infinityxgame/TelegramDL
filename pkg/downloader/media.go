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
		if doc, ok := m.Document.(*tg.Document); ok && doc != nil {
			return extractDocumentInfo(msg, doc, firstCaptionLine)
		}
	case *tg.MessageMediaPhoto:
		if photo, ok := m.Photo.(*tg.Photo); ok && photo != nil {
			return extractPhotoInfo(msg, photo, firstCaptionLine)
		}
	case *tg.MessageMediaWebPage:
		if wp, ok := m.Webpage.(*tg.WebPage); ok {
			if doc, ok := wp.Document.(*tg.Document); ok && doc != nil {
				return extractDocumentInfo(msg, doc, firstCaptionLine)
			}
			if photo, ok := wp.Photo.(*tg.Photo); ok && photo != nil {
				return extractPhotoInfo(msg, photo, firstCaptionLine)
			}
		}
	}

	return nil
}

func extractDocumentInfo(msg *tg.Message, doc *tg.Document, firstCaptionLine string) *MediaInfo {
	kind := KindFile
	fileName := ""
	isVoice := false
	audioTitle := ""
	audioPerformer := ""

	for _, attr := range doc.Attributes {
		switch a := attr.(type) {
		case *tg.DocumentAttributeFilename:
			fileName = a.FileName
		case *tg.DocumentAttributeVideo:
			kind = KindVideo
			if a.RoundMessage && fileName == "" {
				fileName = fmt.Sprintf("videonote_%d.mp4", msg.ID)
			}
		case *tg.DocumentAttributeAudio:
			kind = KindSong
			isVoice = a.Voice
			audioTitle = strings.TrimSpace(a.Title)
			audioPerformer = strings.TrimSpace(a.Performer)
		case *tg.DocumentAttributeSticker:
			kind = KindSticker
		case *tg.DocumentAttributeAnimated:
			kind = KindVideo
		}
	}

	mime := strings.ToLower(doc.MimeType)
	if fileName == "" {
		if kind == KindVideo {
			fileName = fmt.Sprintf("video_%d.mp4", msg.ID)
		} else if kind == KindSong {
			if isVoice {
				fileName = fmt.Sprintf("voice_%d.ogg", msg.ID)
			} else if audioPerformer != "" && audioTitle != "" {
				fileName = fmt.Sprintf("%s - %s.mp3", audioPerformer, audioTitle)
			} else if audioTitle != "" {
				fileName = fmt.Sprintf("%s.mp3", audioTitle)
			} else {
				fileName = fmt.Sprintf("audio_%d.mp3", msg.ID)
			}
		} else if kind == KindSticker {
			if mime == "application/x-tgsticker" {
				fileName = fmt.Sprintf("sticker_%d.tgs", msg.ID)
			} else if mime == "video/webm" {
				fileName = fmt.Sprintf("sticker_%d.webm", msg.ID)
			} else {
				fileName = fmt.Sprintf("sticker_%d.webp", msg.ID)
			}
		} else {
			extByMime := extensionFromMime(mime)
			if extByMime != "" {
				fileName = fmt.Sprintf("file_%d%s", msg.ID, extByMime)
			} else {
				fileName = fmt.Sprintf("file_%d", msg.ID)
			}
		}
	}

	ext := filepath.Ext(fileName)
	lowerName := strings.ToLower(fileName)
	genericPrefixes := []string{"video_", "doc_", "music_", "audio_", "voice_", "sticker_", "file_", "videonote_"}
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

	lowerExt := strings.ToLower(ext)
	if kind == KindFile {
		if strings.HasPrefix(mime, "image/") || lowerExt == ".jpg" || lowerExt == ".jpeg" || lowerExt == ".png" || lowerExt == ".webp" || lowerExt == ".gif" || lowerExt == ".heic" {
			kind = KindPhoto
		} else if strings.HasPrefix(mime, "video/") || lowerExt == ".mp4" || lowerExt == ".mkv" || lowerExt == ".webm" || lowerExt == ".avi" || lowerExt == ".mov" || lowerExt == ".flv" || lowerExt == ".wmv" || lowerExt == ".ts" || lowerExt == ".m4v" {
			kind = KindVideo
		} else if strings.HasPrefix(mime, "audio/") || lowerExt == ".mp3" || lowerExt == ".m4a" || lowerExt == ".flac" || lowerExt == ".wav" || lowerExt == ".ogg" || lowerExt == ".opus" || lowerExt == ".aac" || lowerExt == ".wma" {
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
}

func extractPhotoInfo(msg *tg.Message, photo *tg.Photo, firstCaptionLine string) *MediaInfo {
	fileName := fmt.Sprintf("photo_%d.jpg", msg.ID)
	if firstCaptionLine != "" {
		fileName = fmt.Sprintf("%s.jpg", SanitizeFileName(firstCaptionLine))
	}

	var largest tg.PhotoSizeClass
	var fileSize int64
	for _, size := range photo.Sizes {
		switch s := size.(type) {
		case *tg.PhotoSize:
			largest = s
			if int64(s.Size) > fileSize {
				fileSize = int64(s.Size)
			}
		case *tg.PhotoSizeProgressive:
			largest = s
			if len(s.Sizes) > 0 {
				last := int64(s.Sizes[len(s.Sizes)-1])
				if last > fileSize {
					fileSize = last
				}
			}
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
		FileSize: fileSize,
	}
}

func extensionFromMime(mime string) string {
	switch mime {
	case "application/pdf":
		return ".pdf"
	case "application/zip":
		return ".zip"
	case "application/x-rar-compressed", "application/vnd.rar", "application/x-rar":
		return ".rar"
	case "application/x-7z-compressed":
		return ".7z"
	case "application/x-tar":
		return ".tar"
	case "application/gzip", "application/x-gzip":
		return ".gz"
	case "application/vnd.android.package-archive":
		return ".apk"
	case "text/plain":
		return ".txt"
	case "application/json":
		return ".json"
	case "application/msword":
		return ".doc"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return ".docx"
	case "application/vnd.ms-excel":
		return ".xls"
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return ".xlsx"
	case "application/x-bittorrent":
		return ".torrent"
	default:
		return ""
	}
}
