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
	channelRegex  = regexp.MustCompile(`^https://t\.me/c/(\d+)/(\d+)(?:-(\d+))?(?:[?#].*)?$`)
	usernameRegex = regexp.MustCompile(`^https://t\.me/([A-Za-z0-9_]{1,64})/(\d+)(?:-(\d+))?(?:[?#].*)?$`)
)

const MaxMessagesPerJob = 500

func ParseURL(url string) (*ParsedURL, error) {
	clean := strings.TrimSpace(url)
	if clean == "" {
		return nil, errors.New("URL vacía")
	}

	if match := channelRegex.FindStringSubmatch(clean); match != nil {
		startID, _ := strconv.Atoi(match[2])
		endID := startID
		if len(match) > 3 && match[3] != "" {
			endID, _ = strconv.Atoi(match[3])
		}
		if endID < startID {
			return nil, errors.New("el mensaje final no puede ser menor que el inicial")
		}
		if endID-startID+1 > MaxMessagesPerJob {
			return nil, fmt.Errorf("el rango máximo es de %d mensajes", MaxMessagesPerJob)
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

	if match := usernameRegex.FindStringSubmatch(clean); match != nil {
		username := match[1]
		startID, _ := strconv.Atoi(match[2])
		endID := startID
		if len(match) > 3 && match[3] != "" {
			endID, _ = strconv.Atoi(match[3])
		}
		if endID < startID {
			return nil, errors.New("el mensaje final no puede ser menor que el inicial")
		}
		if endID-startID+1 > MaxMessagesPerJob {
			return nil, fmt.Errorf("el rango máximo es de %d mensajes", MaxMessagesPerJob)
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
