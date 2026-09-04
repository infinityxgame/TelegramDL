package listener

import (
	"context"
	"errors"
	"fmt"
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
	ID        string `json:"id"`
	MessageID int64  `json:"message_id"`
	ChatID    int64  `json:"chat_id"`
	ChatName  string `json:"chat_name"`
	FileName  string `json:"file_name"`
	Kind      string `json:"kind"`
	TotalStr  string `json:"total_str"`
	Status    string `json:"status"` // "available"
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

	if !enabled || msg == nil || msg.Out {
		return nil
	}

	// Obtener ID del chat
	var peerID int64
	switch p := msg.PeerID.(type) {
	case *tg.PeerChannel:
		peerID, _ = strconv.ParseInt(fmt.Sprintf("-100%d", p.ChannelID), 10, 64)
	case *tg.PeerChat:
		peerID = -p.ChatID
	case *tg.PeerUser:
		peerID = p.UserID
	}

	le.mu.RLock()
	chatCfg, watched := le.matchChatID(peerID)
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
	if !ok {
		le.mu.Unlock()
		return errors.New("multimedia no encontrado en escucha")
	}
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

func (le *ListenerEngine) RemoveItem(itemID string) {
	le.mu.Lock()
	item, ok := le.items[itemID]
	if ok {
		delete(le.items, itemID)
	}
	le.mu.Unlock()
	if ok && item != nil {
		le.notifyState(*item)
	}
}

func (le *ListenerEngine) ResolveChat(ctx context.Context, chatID int64) (string, error) {
	raw := le.clientMgr.RawClient()
	if raw == nil {
		return "", errors.New("cliente no conectado")
	}

	// Si es canal o supergrupo
	if chatID < 0 {
		channelID := -chatID
		s := fmt.Sprintf("%d", chatID)
		if len(s) > 4 && s[:4] == "-100" {
			parsed, err := strconv.ParseInt(s[4:], 10, 64)
			if err == nil {
				channelID = parsed
			}
		}

		accessHash, _ := le.clientMgr.GetChannelAccessHash(channelID)
		res, err := raw.ChannelsGetChannels(ctx, []tg.InputChannelClass{
			&tg.InputChannel{ChannelID: channelID, AccessHash: accessHash},
		})
		if err == nil {
			chats := res.GetChats()
			if len(chats) > 0 {
				if ch, ok := chats[0].(*tg.Channel); ok {
					return ch.Title, nil
				}
			}
		}
	} else {
		// Usuario
		accessHash, _ := le.clientMgr.GetUserAccessHash(chatID)
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
					return name, nil
				}
			}
		}
	}

	return strconv.FormatInt(chatID, 10), nil
}
