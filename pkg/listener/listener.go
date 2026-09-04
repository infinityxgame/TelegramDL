package listener

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/tg"

	"tgdown/pkg/config"
	"tgdown/pkg/downloader"
	"tgdown/pkg/storage"
	"tgdown/pkg/telegram"
)

type ListenerItem struct {
	ID        string  `json:"id"`
	MessageID int64   `json:"message_id"`
	ChatID    int64   `json:"chat_id"`
	ChatName  string  `json:"chat_name"`
	FileName  string  `json:"file_name"`
	Kind      string  `json:"kind"`
	TotalStr  string  `json:"total_str"`
	Status    string  `json:"status"` // "available"
	UpdatedAt float64 `json:"updated_at"`
}

type ListenerStateListener func(item ListenerItem)

type ListenerEngine struct {
	clientMgr *telegram.ClientManager
	storage   *storage.Storage
	engine    *downloader.Engine

	mu             sync.RWMutex
	config         config.Config
	items          map[string]*ListenerItem
	chatMap        map[int64]config.ListenerChat
	stateListeners []ListenerStateListener
}

func NewListenerEngine(cm *telegram.ClientManager, st *storage.Storage, eng *downloader.Engine, cfg config.Config) *ListenerEngine {
	cfg = config.NormalizeConfig(cfg)
	le := &ListenerEngine{
		clientMgr: cm,
		storage:   st,
		engine:    eng,
		config:    cfg,
		items:     make(map[string]*ListenerItem),
		chatMap:   make(map[int64]config.ListenerChat),
	}

	le.updateChatMap(cfg)

	// Cargar items de escucha existentes desde SQLite (source='listener')
	if st != nil {
		if saved, err := st.LoadDownloads(""); err == nil {
			for id, d := range saved {
				if d.Source == "listener" && (d.Status == "available" || d.Status == "cancelled") {
					chatName := strconv.FormatInt(d.ChatID, 10)
					if cfgChat, ok := le.chatMap[d.ChatID]; ok && cfgChat.Name != "" {
						chatName = cfgChat.Name
					}
					le.items[id] = &ListenerItem{
						ID:        d.ID,
						MessageID: d.MessageID,
						ChatID:    d.ChatID,
						ChatName:  chatName,
						FileName:  d.FileName,
						Kind:      d.Kind,
						TotalStr:  d.TotalStr,
						Status:    d.Status,
						UpdatedAt: d.UpdatedAt,
					}
				}
			}
		}
	}

	// Conectar callback genérico en ClientManager
	if cm != nil {
		cm.SetMessageCallback(le.HandleMessage)
	}

	return le
}

func (le *ListenerEngine) OnStateChange(listener ListenerStateListener) {
	le.mu.Lock()
	defer le.mu.Unlock()
	le.stateListeners = append(le.stateListeners, listener)
}

func (le *ListenerEngine) notifyState(item ListenerItem) {
	le.mu.RLock()
	listeners := append([]ListenerStateListener(nil), le.stateListeners...)
	le.mu.RUnlock()

	for _, l := range listeners {
		l(item)
	}
}

func (le *ListenerEngine) updateChatMap(cfg config.Config) {
	le.chatMap = make(map[int64]config.ListenerChat)
	for _, c := range cfg.ListenerChats {
		le.chatMap[c.ID] = c
	}
}

func (le *ListenerEngine) UpdateConfig(cfg config.Config) {
	le.mu.Lock()
	defer le.mu.Unlock()
	le.config = config.NormalizeConfig(cfg)
	le.updateChatMap(le.config)
	log.Printf("[LISTENER CONFIG] Actualizada. Escucha activa: %v, Chats vigilados (%d): %+v", le.config.ListenerEnabled, len(le.chatMap), le.chatMap)
}

func (le *ListenerEngine) matchChatID(peerID int64) (config.ListenerChat, bool) {
	if cfg, ok := le.chatMap[peerID]; ok {
		return cfg, true
	}
	s := fmt.Sprintf("%d", peerID)
	if strings.HasPrefix(s, "-100") && len(s) > 4 {
		if rawID, err := strconv.ParseInt(s[4:], 10, 64); err == nil {
			if cfg, ok := le.chatMap[rawID]; ok {
				return cfg, true
			}
			if cfg, ok := le.chatMap[-rawID]; ok {
				return cfg, true
			}
		}
	} else if peerID > 0 {
		if fullID, err := strconv.ParseInt(fmt.Sprintf("-100%d", peerID), 10, 64); err == nil {
			if cfg, ok := le.chatMap[fullID]; ok {
				return cfg, true
			}
		}
		if cfg, ok := le.chatMap[-peerID]; ok {
			return cfg, true
		}
	} else if peerID < 0 {
		if cfg, ok := le.chatMap[-peerID]; ok {
			return cfg, true
		}
	}
	return config.ListenerChat{}, false
}

func (le *ListenerEngine) GetItems() []ListenerItem {
	le.mu.RLock()
	defer le.mu.RUnlock()

	res := make([]ListenerItem, 0, len(le.items))
	for _, item := range le.items {
		res = append(res, *item)
	}
	return res
}

func (le *ListenerEngine) HandleMessage(ctx context.Context, entities tg.Entities, msg *tg.Message) error {
	le.mu.RLock()
	enabled := le.config.ListenerEnabled
	le.mu.RUnlock()

	if msg == nil {
		return nil
	}

	// Obtener ID del chat y del remitente
	var peerID int64
	switch p := msg.PeerID.(type) {
	case *tg.PeerChannel:
		peerID, _ = strconv.ParseInt(fmt.Sprintf("-100%d", p.ChannelID), 10, 64)
	case *tg.PeerChat:
		peerID = -p.ChatID
	case *tg.PeerUser:
		peerID = p.UserID
	}

	var fromID int64
	if msg.FromID != nil {
		switch f := msg.FromID.(type) {
		case *tg.PeerUser:
			fromID = f.UserID
		case *tg.PeerChannel:
			fromID, _ = strconv.ParseInt(fmt.Sprintf("-100%d", f.ChannelID), 10, 64)
		case *tg.PeerChat:
			fromID = -f.ChatID
		}
	}

	if !enabled || msg.Out {
		return nil
	}

	le.mu.RLock()
	chatCfg, watched := le.matchChatID(peerID)
	if !watched && fromID != 0 {
		chatCfg, watched = le.matchChatID(fromID)
		if watched {
			peerID = fromID
		}
	}
	le.mu.RUnlock()

	if !watched {
		return nil
	}

	mediaInfo := downloader.ExtractMediaInfo(msg)
	if mediaInfo == nil {
		return nil
	}

	// Filtros por tipo de medio
	switch mediaInfo.Kind {
	case downloader.KindPhoto:
		if !chatCfg.FPhotos {
			return nil
		}
	case downloader.KindVideo:
		if !chatCfg.FVideos {
			return nil
		}
	case downloader.KindSong:
		if !chatCfg.FAudios {
			return nil
		}
	case downloader.KindSticker:
		if !chatCfg.FStickers {
			return nil
		}
	case downloader.KindFile:
		if !chatCfg.FDocs {
			return nil
		}
	}

	chatName := chatCfg.Name
	if chatName == "" {
		chatName = strconv.FormatInt(peerID, 10)
	}

	itemID := fmt.Sprintf("listener:%d:%d", peerID, msg.ID)
	now := float64(time.Now().Unix())

	dlItem := storage.DownloadItem{
		ID:         itemID,
		JobID:      fmt.Sprintf("listener:%d", peerID),
		MessageID:  int64(msg.ID),
		ChatID:     peerID,
		FileName:   mediaInfo.FileName,
		Status:     "available",
		Kind:       string(mediaInfo.Kind),
		TotalStr:   config.FormatBytes(float64(mediaInfo.FileSize)),
		TotalBytes: mediaInfo.FileSize,
		Source:     "listener",
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	if chatCfg.AutoDownload {
		dlItem.Status = "queued"
		if le.storage != nil {
			_ = le.storage.SaveDownload(dlItem)
		}
		if le.engine != nil {
			le.engine.QueueItem(dlItem)
		}
	} else {
		if le.storage != nil {
			_ = le.storage.SaveDownload(dlItem)
		}
		item := &ListenerItem{
			ID:        itemID,
			MessageID: int64(msg.ID),
			ChatID:    peerID,
			ChatName:  chatName,
			FileName:  mediaInfo.FileName,
			Kind:      string(mediaInfo.Kind),
			TotalStr:  config.FormatBytes(float64(mediaInfo.FileSize)),
			Status:    "available",
			UpdatedAt: now,
		}

		le.mu.Lock()
		le.items[itemID] = item
		le.mu.Unlock()
		le.notifyState(*item)
	}

	return nil
}

func (le *ListenerEngine) DownloadItem(itemID string) error {
	le.mu.Lock()
	item, ok := le.items[itemID]
	if ok {
		item.Status = "queued"
		cp := *item
		le.mu.Unlock()
		le.notifyState(cp)

		dlItem := storage.DownloadItem{
			ID:        item.ID,
			JobID:     fmt.Sprintf("listener:%d", item.ChatID),
			MessageID: item.MessageID,
			ChatID:    item.ChatID,
			FileName:  item.FileName,
			Status:    "queued",
			Kind:      item.Kind,
			TotalStr:  item.TotalStr,
			Source:    "listener",
			CreatedAt: float64(time.Now().Unix()),
			UpdatedAt: float64(time.Now().Unix()),
		}

		if le.storage != nil {
			_ = le.storage.SaveDownload(dlItem)
		}
		if le.engine != nil {
			le.engine.QueueItem(dlItem)
		}
		return nil
	}
	le.mu.Unlock()

	// Si no está en items pero está en SQLite
	if le.storage != nil {
		if saved, err := le.storage.LoadDownloads(""); err == nil {
			if dl, exists := saved[itemID]; exists {
				dl.Status = "queued"
				_ = le.storage.SaveDownload(dl)
				if le.engine != nil {
					le.engine.QueueItem(dl)
				}
				return nil
			}
		}
	}

	return errors.New("multimedia no encontrado en escucha")
}

func (le *ListenerEngine) RemoveItem(itemID string) {
	le.mu.Lock()
	item, ok := le.items[itemID]
	if ok {
		delete(le.items, itemID)
	}
	le.mu.Unlock()

	if le.storage != nil {
		_ = le.storage.DeleteDownload(itemID)
		_ = le.storage.DeleteChunks(itemID)
	}
	if le.engine != nil {
		_ = le.engine.DeleteDownload(itemID, false)
	}

	if ok && item != nil {
		le.notifyState(*item)
	}
}

func (le *ListenerEngine) ClearItems() {
	le.mu.Lock()
	ids := make([]string, 0, len(le.items))
	for id := range le.items {
		ids = append(ids, id)
	}
	le.items = make(map[string]*ListenerItem)
	le.mu.Unlock()

	for _, id := range ids {
		if le.storage != nil {
			_ = le.storage.DeleteDownload(id)
			_ = le.storage.DeleteChunks(id)
		}
		if le.engine != nil {
			_ = le.engine.DeleteDownload(id, false)
		}
	}
	le.notifyState(ListenerItem{})
}

type ResolvedChatInfo struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Username string `json:"username,omitempty"`
}

func (le *ListenerEngine) ResolveChat(ctx context.Context, chatID int64) (ResolvedChatInfo, error) {
	raw := le.clientMgr.RawClient()
	if raw == nil {
		return ResolvedChatInfo{ID: chatID, Name: strconv.FormatInt(chatID, 10), Type: "chat"}, errors.New("cliente no conectado")
	}

	info := ResolvedChatInfo{
		ID:   chatID,
		Name: strconv.FormatInt(chatID, 10),
		Type: "chat",
	}

	// 1. Si es canal o supergrupo (-100...) o grupo básico
	if chatID < 0 {
		channelID := -chatID
		s := fmt.Sprintf("%d", chatID)
		isMegagroupOrChannel := strings.HasPrefix(s, "-100") && len(s) > 4
		if isMegagroupOrChannel {
			if parsed, err := strconv.ParseInt(s[4:], 10, 64); err == nil {
				channelID = parsed
			}
		}

		if isMegagroupOrChannel {
			accessHash, found := le.clientMgr.GetChannelAccessHash(channelID)
			if !found {
				_ = le.clientMgr.FetchDialogs(ctx)
				accessHash, _ = le.clientMgr.GetChannelAccessHash(channelID)
			}

			res, err := raw.ChannelsGetChannels(ctx, []tg.InputChannelClass{
				&tg.InputChannel{ChannelID: channelID, AccessHash: accessHash},
			})
			if err == nil {
				chats := res.GetChats()
				if len(chats) > 0 {
					if ch, ok := chats[0].(*tg.Channel); ok {
						info.Name = ch.Title
						info.Username = ch.Username
						if ch.Megagroup {
							info.Type = "supergroup"
						} else {
							info.Type = "channel"
						}
						return info, nil
					}
				}
			}
		} else {
			// Grupo básico
			res, err := raw.MessagesGetChats(ctx, []int64{-chatID})
			if err == nil {
				chats := res.GetChats()
				if len(chats) > 0 {
					if ch, ok := chats[0].(*tg.Chat); ok {
						info.Name = ch.Title
						info.Type = "group"
						return info, nil
					}
				}
			}
		}
	} else {
		// 2. ID positivo: Usuario o Bot (o ID de canal sin -100)
		accessHash, found := le.clientMgr.GetUserAccessHash(chatID)
		if !found {
			_ = le.clientMgr.FetchDialogs(ctx)
			accessHash, _ = le.clientMgr.GetUserAccessHash(chatID)
		}

		res, err := raw.UsersGetUsers(ctx, []tg.InputUserClass{
			&tg.InputUser{UserID: chatID, AccessHash: accessHash},
		})
		if err == nil && len(res) > 0 {
			if u, ok := res[0].(*tg.User); ok {
				name := strings.TrimSpace(u.FirstName + " " + u.LastName)
				if name == "" {
					name = u.Username
				}
				if name != "" {
					info.Name = name
				}
				info.Username = u.Username
				if u.Bot {
					info.Type = "bot"
				} else {
					info.Type = "user"
				}
				return info, nil
			}
		}

		// Probar si era un canal pasado sin -100
		accessHash, found = le.clientMgr.GetChannelAccessHash(chatID)
		if found {
			resCh, errCh := raw.ChannelsGetChannels(ctx, []tg.InputChannelClass{
				&tg.InputChannel{ChannelID: chatID, AccessHash: accessHash},
			})
			if errCh == nil && len(resCh.GetChats()) > 0 {
				if ch, ok := resCh.GetChats()[0].(*tg.Channel); ok {
					info.Name = ch.Title
					info.Username = ch.Username
					if ch.Megagroup {
						info.Type = "supergroup"
					} else {
						info.Type = "channel"
					}
					return info, nil
				}
			}
		}
	}

	return info, nil
}
