package downloader

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type ParsedURL struct {
	ChatUsername string
	ChatID       int64
	IsChannelID  bool
	StartMsgID   int
	EndMsgID     int
}

var (
	channelRegex  = regexp.MustCompile(`^https://t\.me/c/(\d+)/(\d+)(?:-(\d+))?(?:/)?(?:[?#].*)?$`)
	botRegex      = regexp.MustCompile(`^https://t\.me/b/([A-Za-z0-9_]{1,64})/(\d+)(?:-(\d+))?(?:/)?(?:[?#].*)?$`)
	usernameRegex = regexp.MustCompile(`^https://t\.me/([A-Za-z0-9_]{1,64})/(\d+)(?:-(\d+))?(?:/)?(?:[?#].*)?$`)
)

const MaxMessagesPerJob = 500

// parseMsgRange interpreta los campos de inicio/fin de mensaje extraídos por
// las expresiones regulares de arriba y valida que formen un rango razonable.
// Centraliza una lógica que antes estaba duplicada en las tres ramas de
// ParseURL (canal, bot y username).
func parseMsgRange(startStr, endStr string) (startID, endID int, err error) {
	startID, _ = strconv.Atoi(startStr)
	endID = startID
	if endStr != "" {
		endID, _ = strconv.Atoi(endStr)
	}
	if endID < startID {
		return 0, 0, errors.New("el mensaje final no puede ser menor que el inicial")
	}
	if endID-startID+1 > MaxMessagesPerJob {
		return 0, 0, fmt.Errorf("el rango máximo es de %d mensajes", MaxMessagesPerJob)
	}
	return startID, endID, nil
}

func submatchOrEmpty(match []string, idx int) string {
	if len(match) > idx {
		return match[idx]
	}
	return ""
}

func ParseURL(url string) (*ParsedURL, error) {
	clean := strings.TrimSpace(url)
	if clean == "" {
		return nil, errors.New("URL vacía")
	}

	clean = strings.Replace(clean, "http://telegram.me/", "https://t.me/", 1)
	clean = strings.Replace(clean, "https://telegram.me/", "https://t.me/", 1)
	clean = strings.Replace(clean, "telegram.me/", "https://t.me/", 1)

	if strings.HasPrefix(clean, "http://") {
		clean = "https://" + strings.TrimPrefix(clean, "http://")
	} else if !strings.HasPrefix(clean, "https://") && !strings.HasPrefix(clean, "tg://") {
		clean = "https://" + clean
	}

	if match := channelRegex.FindStringSubmatch(clean); match != nil {
		startID, endID, err := parseMsgRange(match[2], submatchOrEmpty(match, 3))
		if err != nil {
			return nil, err
		}

		// En MTProto de Telegram, los IDs de canales privados llevan prefijo -100
		tgChatID, _ := strconv.ParseInt(fmt.Sprintf("-100%s", match[1]), 10, 64)

		return &ParsedURL{
			ChatID:      tgChatID,
			IsChannelID: true,
			StartMsgID:  startID,
			EndMsgID:    endID,
		}, nil
	}

	if match := botRegex.FindStringSubmatch(clean); match != nil {
		username := match[1]
		startID, endID, err := parseMsgRange(match[2], submatchOrEmpty(match, 3))
		if err != nil {
			return nil, err
		}

		return &ParsedURL{
			ChatUsername: username,
			IsChannelID:  false,
			StartMsgID:   startID,
			EndMsgID:     endID,
		}, nil
	}

	if match := usernameRegex.FindStringSubmatch(clean); match != nil {
		username := match[1]
		startID, endID, err := parseMsgRange(match[2], submatchOrEmpty(match, 3))
		if err != nil {
			return nil, err
		}

		return &ParsedURL{
			ChatUsername: username,
			IsChannelID:  false,
			StartMsgID:   startID,
			EndMsgID:     endID,
		}, nil
	}

	return nil, errors.New("URL no válida. Formatos soportados: https://t.me/c/... o https://t.me/...")
}
