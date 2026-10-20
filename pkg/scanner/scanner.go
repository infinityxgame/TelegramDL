package scanner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/tg"

	"tgdown/pkg/config"
	"tgdown/pkg/downloader"
	"tgdown/pkg/i18n"
	"tgdown/pkg/storage"
	"tgdown/pkg/telegram"
)

type ScanRequest struct {
	Target    string `json:"target"`
	StartDate string `json:"start_date"` // YYYY-MM-DD
	EndDate   string `json:"end_date"`   // YYYY-MM-DD
	FPhotos   bool   `json:"f_photos"`
	FVideos   bool   `json:"f_videos"`
	FAudios   bool   `json:"f_audios"`
	FDocs     bool   `json:"f_docs"`
	FStickers bool   `json:"f_stickers"`
	MinSize   int64  `json:"min_size"` // Bytes (0 = sin límite)
	MaxSize   int64  `json:"max_size"` // Bytes (0 = sin límite)
	MaxScan   int    `json:"max_scan"` // Máximo de mensajes a inspeccionar
}

type ScanItem = storage.ScanItem
type DateRange = storage.DateRange

type ScanResponse struct {
	Status       string     `json:"status"`
	ScannedCount int        `json:"scanned_count"`
	MatchCount   int        `json:"match_count"`
	Items        []ScanItem `json:"items"`
	ReachedLimit bool       `json:"reached_limit"`
	Message      string     `json:"message,omitempty"`
}

type ResolvedTargetInfo struct {
	ChatID   int64  `json:"chat_id"`
	ChatName string `json:"chat_name"`
	Type     string `json:"type"`
	Username string `json:"username,omitempty"`
}

type ChatCache struct {
	Items        map[int64]ScanItem
	Ranges       []DateRange
	ScannedTotal int
}

type ScannerService struct {
	clientMgr *telegram.ClientManager
	storage   *storage.Storage
	engine    *downloader.Engine
	cacheMu   sync.RWMutex
	chatCache map[int64]*ChatCache
}

func NewScannerService(cm *telegram.ClientManager, st *storage.Storage, eng *downloader.Engine) *ScannerService {
	return &ScannerService{
		clientMgr: cm,
		storage:   st,
		engine:    eng,
		chatCache: make(map[int64]*ChatCache),
	}
}

// GetUnscannedRanges calcula los subrangos de [reqStart, reqEnd] que aún no han sido escaneados.
func GetUnscannedRanges(covered []DateRange, reqStart, reqEnd int64) []DateRange {
	if reqEnd == 0 {
		reqEnd = time.Now().Unix() + 86400
	}
	missing := []DateRange{{StartTS: reqStart, EndTS: reqEnd}}

	for _, cov := range covered {
		var nextMissing []DateRange
		for _, m := range missing {
			if cov.EndTS < m.StartTS || cov.StartTS > m.EndTS {
				nextMissing = append(nextMissing, m)
				continue
			}
			if m.StartTS < cov.StartTS {
				nextMissing = append(nextMissing, DateRange{
					StartTS: m.StartTS,
					EndTS:   cov.StartTS - 1,
				})
			}
			if m.EndTS > cov.EndTS {
				nextMissing = append(nextMissing, DateRange{
					StartTS: cov.EndTS + 1,
					EndTS:   m.EndTS,
				})
			}
		}
		missing = nextMissing
	}
	return missing
}

// MergeRanges fusiona un nuevo rango escaneado con la lista de rangos existentes.
func MergeRanges(covered []DateRange, newRange DateRange) []DateRange {
	if newRange.StartTS > newRange.EndTS && newRange.EndTS > 0 {
		return covered
	}
	all := append(covered, newRange)
	sort.Slice(all, func(i, j int) bool {
		return all[i].StartTS < all[j].StartTS
	})

	var merged []DateRange
	for _, r := range all {
		if len(merged) == 0 {
			merged = append(merged, r)
			continue
		}
		last := &merged[len(merged)-1]
		if r.StartTS <= last.EndTS+1 {
			if r.EndTS > last.EndTS {
				last.EndTS = r.EndTS
			}
		} else {
			merged = append(merged, r)
		}
	}
	return merged
}

// ResolveTarget identifica un chat, bot o canal ingresado por el usuario.
func (s *ScannerService) ResolveTarget(ctx context.Context, target string) (ResolvedTargetInfo, error) {
	if s.clientMgr == nil {
		return ResolvedTargetInfo{}, errors.New(i18n.T("scanner.tgClientUnavailable"))
	}
	if err := s.clientMgr.WaitReady(ctx); err != nil {
		return ResolvedTargetInfo{}, fmt.Errorf("%s: %w", i18n.T("scanner.tgClientNotReady"), err)
	}

	clean := strings.TrimSpace(target)
	if clean == "" {
		return ResolvedTargetInfo{}, errors.New(i18n.T("scanner.emptyTarget"))
	}

	var chatID int64
	var username string

	if strings.HasPrefix(clean, "http://") || strings.HasPrefix(clean, "https://") || strings.HasPrefix(clean, "t.me/") {
		parsed, err := downloader.ParseURL(clean)
		if err == nil {
			chatID = parsed.ChatID
			username = parsed.ChatUsername
		}
	}

	if chatID == 0 {
		if id, err := strconv.ParseInt(clean, 10, 64); err == nil {
			chatID = id
		} else {
			username = strings.TrimPrefix(clean, "@")
		}
	}

	if chatID == 0 && username != "" {
		resolvedID, err := s.clientMgr.ResolveUsername(ctx, username)
		if err != nil {
			return ResolvedTargetInfo{}, fmt.Errorf("no se pudo resolver @%s: %w", username, err)
		}
		chatID = resolvedID
	}

	if chatID == 0 {
		return ResolvedTargetInfo{}, errors.New("no se pudo identificar el chat")
	}

	chatName := s.clientMgr.GetChatName(chatID)
	if username == "" {
		username = s.clientMgr.GetChatUsername(chatID)
	}

	raw := s.clientMgr.RawClient()
	if username == "" && raw != nil {
		sID := strconv.FormatInt(chatID, 10)
		if strings.HasPrefix(sID, "-100") && len(sID) > 4 {
			if channelID, err := strconv.ParseInt(sID[4:], 10, 64); err == nil {
				accessHash, found := s.clientMgr.GetChannelAccessHash(channelID)
				if !found {
					_ = s.clientMgr.FetchDialogs(ctx)
					accessHash, _ = s.clientMgr.GetChannelAccessHash(channelID)
				}
				if accessHash != 0 {
					res, err := raw.ChannelsGetChannels(ctx, []tg.InputChannelClass{&tg.InputChannel{
						ChannelID:  channelID,
						AccessHash: accessHash,
					}})
					if err == nil {
						for _, c := range res.GetChats() {
							if ch, ok := c.(*tg.Channel); ok {
								if ch.Title != "" {
									chatName = ch.Title
									s.clientMgr.RememberChatName(chatID, ch.Title)
								}
								if ch.Username != "" {
									username = ch.Username
									s.clientMgr.RememberChatUsername(chatID, ch.Username)
								}
							}
						}
					}
				}
			}
		} else if chatID > 0 {
			accessHash, found := s.clientMgr.GetUserAccessHash(chatID)
			if !found {
				_ = s.clientMgr.FetchDialogs(ctx)
				accessHash, _ = s.clientMgr.GetUserAccessHash(chatID)
			}
			if accessHash != 0 {
				users, err := raw.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUser{
					UserID:     chatID,
					AccessHash: accessHash,
				}})
				if err == nil {
					for _, u := range users {
						if usr, ok := u.(*tg.User); ok {
							if usr.Username != "" {
								username = usr.Username
								s.clientMgr.RememberChatUsername(chatID, usr.Username)
							}
						}
					}
				}
			}
		}
	}

	if chatName == "" {
		chatName = strconv.FormatInt(chatID, 10)
	}

	if s.storage != nil {
		if err := s.storage.SaveRecentChat(clean, chatID, chatName, username); err != nil {
			log.Printf("[SCANNER] error saving recent chat: %v", err)
		}
	}

	return ResolvedTargetInfo{
		ChatID:   chatID,
		ChatName: chatName,
		Type:     s.determineType(chatID),
		Username: username,
	}, nil
}

func (s *ScannerService) determineType(chatID int64) string {
	if chatID > 0 {
		return "user_or_bot"
	}
	sID := strconv.FormatInt(chatID, 10)
	if strings.HasPrefix(sID, "-100") {
		return "channel"
	}
	return "group"
}

// resolveInputPeer construye el InputPeerClass de MTProto recuperando access hash si es necesario.
func (s *ScannerService) resolveInputPeer(ctx context.Context, chatID int64) (tg.InputPeerClass, string, error) {
	raw := s.clientMgr.RawClient()
	if raw == nil {
		return nil, "", errors.New(i18n.T("scanner.clientNotConnected"))
	}

	chatName := s.clientMgr.GetChatName(chatID)

	// Usuario / Bot (ID positivo)
	if chatID > 0 {
		accessHash, found := s.clientMgr.GetUserAccessHash(chatID)
		if !found {
			_ = s.clientMgr.FetchDialogs(ctx)
			accessHash, found = s.clientMgr.GetUserAccessHash(chatID)
		}
		if found {
			if chatName == "" {
				chatName = fmt.Sprintf(i18n.T("scanner.userPrefix"), chatID)
			}
			return &tg.InputPeerUser{UserID: chatID, AccessHash: accessHash}, chatName, nil
		}

		// Probar si era ID de canal positivo
		accessHashCh, foundCh := s.clientMgr.GetChannelAccessHash(chatID)
		if !foundCh {
			_ = s.clientMgr.FetchDialogs(ctx)
			accessHashCh, foundCh = s.clientMgr.GetChannelAccessHash(chatID)
		}
		if foundCh {
			if chatName == "" {
				chatName = fmt.Sprintf(i18n.T("scanner.channelPrefix"), chatID)
			}
			return &tg.InputPeerChannel{ChannelID: chatID, AccessHash: accessHashCh}, chatName, nil
		}

		return nil, "", fmt.Errorf(i18n.T("scanner.noAccessInfo"), chatID)
	}

	// Canal / Supergrupo / Grupo
	sID := strconv.FormatInt(chatID, 10)
	if strings.HasPrefix(sID, "-100") && len(sID) > 4 {
		channelID, err := strconv.ParseInt(sID[4:], 10, 64)
		if err != nil {
			return nil, "", fmt.Errorf(i18n.T("scanner.invalidChannelID"), sID)
		}
		accessHash, found := s.clientMgr.GetChannelAccessHash(channelID)
		if !found {
			_ = s.clientMgr.FetchDialogs(ctx)
			accessHash, found = s.clientMgr.GetChannelAccessHash(channelID)
		}
		if !found {
			return nil, "", fmt.Errorf(i18n.T("scanner.noChannelAccess"), channelID)
		}
		if chatName == "" {
			chatName = fmt.Sprintf(i18n.T("scanner.channelPrefix"), channelID)
		}
		return &tg.InputPeerChannel{ChannelID: channelID, AccessHash: accessHash}, chatName, nil
	}

	// Grupo básico
	groupChatID := -chatID
	if chatName == "" {
		chatName = fmt.Sprintf(i18n.T("scanner.groupPrefix"), groupChatID)
	}
	return &tg.InputPeerChat{ChatID: groupChatID}, chatName, nil
}

// Scan busca en el historial de Telegram filtrando por fechas, tipo de medio y tamaño.
// Utiliza una caché en memoria por chat para evitar re-escanear rangos ya consultados.
func (s *ScannerService) Scan(ctx context.Context, req ScanRequest) (*ScanResponse, error) {
	if s.clientMgr == nil {
		return nil, errors.New(i18n.T("scanner.tgClientUnavailable"))
	}
	if err := s.clientMgr.WaitReady(ctx); err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("scanner.tgClientNotReady"), err)
	}

	raw := s.clientMgr.RawClient()
	if raw == nil {
		return nil, errors.New(i18n.T("scanner.clientNotConnected"))
	}

	// Resolver target a ChatID si hace falta
	targetInfo, err := s.ResolveTarget(ctx, req.Target)
	if err != nil {
		return nil, err
	}
	chatID := targetInfo.ChatID

	peer, chatName, err := s.resolveInputPeer(ctx, chatID)
	if err != nil {
		return nil, err
	}

	if s.storage != nil && chatName != "" {
		if err := s.storage.SaveRecentChat(req.Target, chatID, chatName, targetInfo.Username); err != nil {
			log.Printf("[SCANNER] error updating recent chat: %v", err)
		}
	}

	// Parsear fechas
	var startTS int64
	var endTS int64

	if req.StartDate != "" {
		if t, err := time.ParseInLocation("2006-01-02", req.StartDate, time.Local); err == nil {
			startTS = t.Unix()
		}
	}
	if req.EndDate != "" {
		if t, err := time.ParseInLocation("2006-01-02", req.EndDate, time.Local); err == nil {
			endTS = t.Add(23*time.Hour + 59*time.Minute + 59*time.Second).Unix()
		}
	}
	if endTS == 0 {
		endTS = time.Now().Unix() + 86400
	}

	// Obtener los rangos de fechas ya escaneados guardados en la base de datos
	var coveredRanges []DateRange
	var allCachedItems []ScanItem

	if s.storage != nil {
		var errCache error
		allCachedItems, coveredRanges, errCache = s.storage.GetScannerCache(chatID)
		if errCache != nil {
			log.Printf("[SCANNER] error leyendo caché de escáner desde BD: %v", errCache)
		}
	}

	existingMsgIDs := make(map[int64]bool, len(allCachedItems))
	for _, it := range allCachedItems {
		existingMsgIDs[it.MessageID] = true
	}

	// Determinar los subrangos de fechas que aún no se han escaneado para este chat
	missingRanges := GetUnscannedRanges(coveredRanges, startTS, endTS)

	maxScan := req.MaxScan
	if maxScan <= 0 || maxScan > 5000 {
		maxScan = 1000
	}

	batchLimit := 100
	if maxScan < batchLimit {
		batchLimit = maxScan
	}

	var floodMsg string
	var newlyDiscovered []ScanItem
	var newRangesToSave []DateRange

	// Comprobar mensajes de hoy en tiempo real si el rango incluye la fecha actual
	nowTS := time.Now().Unix()
	todayStartTS := time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 0, 0, 0, 0, time.Now().Location()).Unix()

	if endTS >= todayStartTS && len(existingMsgIDs) > 0 {
		offsetID := 0
		checkCount := 0
		hitExisting := false

		for checkCount < 300 && !hitExisting {
			if checkCount > 0 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(200 * time.Millisecond):
				}
			}

			reqBatch := &tg.MessagesGetHistoryRequest{
				Peer:     peer,
				OffsetID: offsetID,
				Limit:    batchLimit,
			}

			res, err := raw.MessagesGetHistory(ctx, reqBatch)
			if err != nil {
				break
			}

			var msgClasses []tg.MessageClass
			switch resMsg := res.(type) {
			case *tg.MessagesMessages:
				msgClasses = resMsg.Messages
			case *tg.MessagesMessagesSlice:
				msgClasses = resMsg.Messages
			case *tg.MessagesChannelMessages:
				msgClasses = resMsg.Messages
			}

			if len(msgClasses) == 0 {
				break
			}

			for _, mc := range msgClasses {
				msg, ok := mc.(*tg.Message)
				if !ok {
					continue
				}

				checkCount++
				offsetID = msg.ID

				if existingMsgIDs[int64(msg.ID)] {
					hitExisting = true
					break
				}

				mediaInfo := downloader.ExtractMediaInfo(msg)
				if mediaInfo != nil {
					tMsg := time.Unix(int64(msg.Date), 0)
					dateStr := tMsg.Format("2006-01-02 15:04:05")

					item := ScanItem{
						ID:               fmt.Sprintf("scanner:%d:%d", chatID, msg.ID),
						ChatID:           chatID,
						ChatName:         chatName,
						MessageID:        int64(msg.ID),
						FileName:         mediaInfo.FileName,
						CaptionFileName:  mediaInfo.CaptionFileName,
						OriginalFileName: mediaInfo.OriginalFileName,
						Kind:             string(mediaInfo.Kind),
						FileSize:         mediaInfo.FileSize,
						FormattedSize:    config.FormatBytes(float64(mediaInfo.FileSize)),
						Date:             int64(msg.Date),
						DateStr:          dateStr,
						IsDeleted:        false,
					}

					newlyDiscovered = append(newlyDiscovered, item)
					existingMsgIDs[int64(msg.ID)] = true
				}
			}
		}

		if hitExisting || checkCount > 0 {
			newRangesToSave = append(newRangesToSave, DateRange{StartTS: todayStartTS, EndTS: nowTS + 86400})
		}
	}

	// Escanear únicamente los rangos de fechas faltantes en Telegram
	for _, mRange := range missingRanges {
		offsetID := 0
		scannedInRun := 0
		reachedLimit := false
		actualStart := mRange.EndTS // Seguimiento de la fecha más antigua alcanzada en este escaneo

		for scannedInRun < maxScan {
			// Pausa preventiva entre peticiones para evitar FLOOD_WAIT
			if scannedInRun > 0 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(350 * time.Millisecond):
				}
			}

			reqBatch := &tg.MessagesGetHistoryRequest{
				Peer:     peer,
				OffsetID: offsetID,
				Limit:    batchLimit,
			}

			res, err := raw.MessagesGetHistory(ctx, reqBatch)
			if err != nil {
				if floodDur, ok := parseFloodWait(err); ok {
					downloader.SetFloodWait(floodDur)
					waitSeconds := int(floodDur.Seconds())
					if waitSeconds > 10 {
						floodMsg = i18n.T("scanner.floodWaitNotice", waitSeconds)
						break
					}
					time.Sleep(floodDur)
					continue
				}
				return nil, fmt.Errorf(i18n.T("scanner.historyFetchErr"), err)
			}

			var msgClasses []tg.MessageClass
			switch resMsg := res.(type) {
			case *tg.MessagesMessages:
				msgClasses = resMsg.Messages
			case *tg.MessagesMessagesSlice:
				msgClasses = resMsg.Messages
			case *tg.MessagesChannelMessages:
				msgClasses = resMsg.Messages
			}

			if len(msgClasses) == 0 {
				break
			}

			for _, mc := range msgClasses {
				msg, ok := mc.(*tg.Message)
				if !ok {
					continue
				}

				scannedInRun++
				offsetID = msg.ID
				msgDate := int64(msg.Date)

				if msgDate < actualStart {
					actualStart = msgDate
				}

				// Si el mensaje es más reciente que el límite superior del rango faltante, omitir e ir más atrás
				if mRange.EndTS > 0 && msgDate > mRange.EndTS {
					continue
				}

				// Si el mensaje es más antiguo que el límite inferior del rango faltante, detener este sub-escaneo
				if mRange.StartTS > 0 && msgDate < mRange.StartTS {
					reachedLimit = true
					break
				}

				// Extraer información multimedia
				mediaInfo := downloader.ExtractMediaInfo(msg)
				if mediaInfo == nil {
					continue
				}

				tMsg := time.Unix(msgDate, 0)
				dateStr := tMsg.Format("2006-01-02 15:04:05")

				item := ScanItem{
					ID:               fmt.Sprintf("scanner:%d:%d", chatID, msg.ID),
					ChatID:           chatID,
					ChatName:         chatName,
					MessageID:        int64(msg.ID),
					FileName:         mediaInfo.FileName,
					CaptionFileName:  mediaInfo.CaptionFileName,
					OriginalFileName: mediaInfo.OriginalFileName,
					Kind:             string(mediaInfo.Kind),
					FileSize:         mediaInfo.FileSize,
					FormattedSize:    config.FormatBytes(float64(mediaInfo.FileSize)),
					Date:             msgDate,
					DateStr:          dateStr,
					IsDeleted:        false,
				}

				newlyDiscovered = append(newlyDiscovered, item)
			}

			if reachedLimit {
				break
			}
		}

		// Registrar en la caché del servidor el rango efectivamente escaneado
		if actualStart <= mRange.EndTS {
			newRangesToSave = append(newRangesToSave, DateRange{StartTS: actualStart, EndTS: mRange.EndTS})
		}
	}

	// Persistir elementos descubiertos y rangos escaneados en SQLite
	if len(newlyDiscovered) > 0 && s.storage != nil {
		if err := s.storage.SaveScannerCacheItems(newlyDiscovered); err != nil {
			log.Printf("[SCANNER] error guardando elementos escaneados en BD: %v", err)
		}
	}

	if len(newRangesToSave) > 0 && s.storage != nil {
		finalRanges := coveredRanges
		for _, nr := range newRangesToSave {
			finalRanges = MergeRanges(finalRanges, nr)
		}
		if err := s.storage.SaveScannerCacheRanges(chatID, finalRanges); err != nil {
			log.Printf("[SCANNER] error guardando rangos escaneados en BD: %v", err)
		}
	}

	// Consultar de la base de datos todos los elementos del chat para filtrar por fecha, tipo y tamaño
	if s.storage != nil {
		allCachedItems, _, _ = s.storage.GetScannerCache(chatID)
	}

	var resultItems []ScanItem
	for _, item := range allCachedItems {
		// Filtrar por el rango de fechas actual
		if startTS > 0 && item.Date < startTS {
			continue
		}
		if endTS > 0 && item.Date > endTS {
			continue
		}

		// Filtros por tipo de archivo
		allowed := false
		switch item.Kind {
		case string(downloader.KindPhoto):
			allowed = req.FPhotos
		case string(downloader.KindVideo):
			allowed = req.FVideos
		case string(downloader.KindSong):
			allowed = req.FAudios
		case string(downloader.KindSticker):
			allowed = req.FStickers
		case string(downloader.KindFile):
			allowed = req.FDocs
		}
		if !allowed {
			continue
		}

		// Filtros por tamaño
		if req.MinSize > 0 && item.FileSize < req.MinSize {
			continue
		}
		if req.MaxSize > 0 && item.FileSize > req.MaxSize {
			continue
		}

		resultItems = append(resultItems, item)
	}

	// Ordenar los resultados del rango actual por fecha descendente
	sort.Slice(resultItems, func(i, j int) bool {
		if resultItems[i].Date == resultItems[j].Date {
			return resultItems[i].MessageID > resultItems[j].MessageID
		}
		return resultItems[i].Date > resultItems[j].Date
	})

	return &ScanResponse{
		Status:       "ok",
		ScannedCount: len(allCachedItems),
		MatchCount:   len(resultItems),
		Items:        resultItems,
		ReachedLimit: true,
		Message:      floodMsg,
	}, nil
}

// QueueItems añade los elementos escaneados a la cola de descargas.
func (s *ScannerService) QueueItems(items []ScanItem) int {
	queued := 0
	now := float64(time.Now().UnixNano()) / 1e9

	// Ordenar ítems de menor a mayor MessageID / Date para que las descargas en lote
	// comiencen desde el mensaje más antiguo al más reciente (ID 100, 101, ...),
	// coincidiendo con el orden en la lista de descargas activas.
	sortedItems := make([]ScanItem, len(items))
	copy(sortedItems, items)
	sort.SliceStable(sortedItems, func(i, j int) bool {
		if sortedItems[i].MessageID != sortedItems[j].MessageID {
			return sortedItems[i].MessageID < sortedItems[j].MessageID
		}
		if sortedItems[i].Date != sortedItems[j].Date {
			return sortedItems[i].Date < sortedItems[j].Date
		}
		return sortedItems[i].ID < sortedItems[j].ID
	})

	for i, item := range sortedItems {
		if item.IsDeleted {
			log.Printf("[SCANNER] omitiendo elemento borrado en Telegram: %s", item.ID)
			continue
		}

		// Asignar CreatedAt con un minúsculo incremento para garantizar el orden cronológico
		itemCreatedAt := now + float64(i)*0.0001

		dlItem := storage.DownloadItem{
			ID:               item.ID,
			JobID:            fmt.Sprintf("scanner:%d", item.ChatID),
			MessageID:        item.MessageID,
			ChatID:           item.ChatID,
			FileName:         item.FileName,
			CaptionFileName:  item.CaptionFileName,
			OriginalFileName: item.OriginalFileName,
			Status:           "queued",
			Kind:             item.Kind,
			TotalStr:         item.FormattedSize,
			TotalBytes:       item.FileSize,
			Source:           "scanner",
			CreatedAt:        itemCreatedAt,
			UpdatedAt:        now,
		}

		if s.storage != nil {
			if err := s.storage.SaveDownload(dlItem); err != nil {
				log.Printf("[SCANNER] %s", i18n.T("scanner.saveDownloadErr", err))
			}
		}
		if s.engine != nil {
			s.engine.QueueItem(dlItem)
			queued++
		}
	}
	return queued
}

func parseFloodWait(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	text := err.Error()
	const prefix = "FLOOD_WAIT_"
	pos := strings.Index(text, prefix)
	if pos < 0 {
		return 0, false
	}
	rest := text[pos+len(prefix):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	seconds, convErr := strconv.Atoi(rest[:end])
	if convErr != nil || seconds <= 0 {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}
