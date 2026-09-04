package listener

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
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

type ListenerEngine struct {
	clientMgr *telegram.ClientManager
	storage   *storage.Storage
	engine    *downloader.Engine

	mu         sync.RWMutex
	config     config.Config
	items      map[string]*ListenerItem
	chatMap    map[int64]config.ListenerChat
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

	// Conectar callback en ClientManager si está disponible
	if cm != nil {
		cm.SetMessageCallback(le.HandleChannelMessage)
	}

	return le
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

func (le *ListenerEngine) GetItems() []ListenerItem {
	le.mu.RLock()
	defer le.mu.RUnlock()

	res := make([]ListenerItem, 0, len(le.items))
	for _, item := range le.items {
		res = append(res, *item)
	}
	return res
}

func (le *ListenerEngine) HandleChannelMessage(ctx context.Context, entities tg.Entities, update *tg.UpdateNewChannelMessage) error {
	le.mu.RLock()
	enabled := le.config.ListenerEnabled
	le.mu.RUnlock()

	if !enabled || update == nil {
		return nil
	}

	msg, ok := update.Message.(*tg.Message)
	if !ok || msg.Out {
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
	chatCfg, watched := le.chatMap[peerID]
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

	itemID := uuid.New().String()
	now := float64(time.Now().Unix())

	if chatCfg.AutoDownload {
		// Descarga automática inmediata
		dlItem := storage.DownloadItem{
			ID:         itemID,
			MessageID:  int64(msg.ID),
			ChatID:     peerID,
			FileName:   mediaInfo.FileName,
			Status:     "queued",
			Kind:       string(mediaInfo.Kind),
			TotalStr:   config.FormatBytes(float64(mediaInfo.FileSize)),
			TotalBytes: mediaInfo.FileSize,
			Source:     "listener",
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		le.engine.QueueItem(dlItem)
	} else {
		// Modo manual: guardar como disponible
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
	delete(le.items, itemID)
	le.mu.Unlock()

	dlItem := storage.DownloadItem{
		ID:        item.ID,
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

	le.engine.QueueItem(dlItem)
	return nil
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

		res, err := raw.ChannelsGetChannels(ctx, []tg.InputChannelClass{
			&tg.InputChannel{ChannelID: channelID, AccessHash: 0},
		})
		if err == nil {
			chats := res.GetChats()
			if len(chats) > 0 {
				if ch, ok := chats[0].(*tg.Channel); ok {
					return ch.Title, nil
				}
			}
		}
	}

	return strconv.FormatInt(chatID, 10), nil
}
