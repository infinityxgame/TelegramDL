package listener

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"

	"tgdown/pkg/config"
)

func newEngineWithChats(chats ...config.ListenerChat) *ListenerEngine {
	cfg := config.DefaultConfig()
	cfg.ListenerChats = chats
	cfg = config.NormalizeConfig(cfg)

	le := &ListenerEngine{
		config:         cfg,
		items:          make(map[string]*ListenerItem),
		chatMap:        make(map[int64][]config.ListenerChat),
		topicNameTries: make(map[string]bool),
	}
	le.updateChatMap(cfg)
	return le
}

func topicMessage(topicID int, viaReply bool) *tg.Message {
	msg := &tg.Message{ID: 1000}
	if topicID <= 0 {
		return msg
	}
	header := &tg.MessageReplyHeader{ForumTopic: true}
	if viaReply {
		// Respuesta dentro del tema: reply_to_top_id apunta al tema.
		header.SetReplyToTopID(topicID)
		header.SetReplyToMsgID(topicID + 5)
	} else {
		// Mensaje escrito directamente en el tema.
		header.SetReplyToMsgID(topicID)
	}
	msg.ReplyTo = header
	return msg
}

func TestMessageTopicIDReadsForumHeader(t *testing.T) {
	if got := messageTopicID(topicMessage(57, false)); got != 57 {
		t.Fatalf("mensaje escrito en el tema: got %d, want 57", got)
	}
	if got := messageTopicID(topicMessage(57, true)); got != 57 {
		t.Fatalf("respuesta dentro del tema: got %d, want 57", got)
	}
	if got := messageTopicID(topicMessage(0, false)); got != 0 {
		t.Fatalf("mensaje sin tema: got %d, want 0", got)
	}
}

func TestMessageTopicIDIgnoresNonForumReplies(t *testing.T) {
	// Una respuesta normal en un grupo sin temas NO debe interpretarse como
	// tema: si no, cualquier respuesta parecería pertenecer a un tema.
	header := &tg.MessageReplyHeader{}
	header.SetReplyToMsgID(42)
	msg := &tg.Message{ID: 1000, ReplyTo: header}

	if got := messageTopicID(msg); got != 0 {
		t.Fatalf("una respuesta normal no es un tema: got %d", got)
	}
}

func TestMatchChatOnlyAcceptsMonitoredTopic(t *testing.T) {
	le := newEngineWithChats(config.ListenerChat{
		ID:        -1002121902112,
		TopicID:   config.TopicPointer(57),
		TopicName: "Películas",
		Name:      "Mi Grupo",
	})

	if _, ok := le.matchChat(-1002121902112, 2121902112, 57); !ok {
		t.Fatal("monitored topic should be accepted")
	}
	if _, ok := le.matchChat(-1002121902112, 2121902112, 99); ok {
		t.Fatal("topic other than monitored should not be accepted")
	}
	if _, ok := le.matchChat(-1002121902112, 2121902112, 0); ok {
		t.Fatal("General topic should not match when another topic is monitored")
	}
}

func TestMatchChatEntireGroupAcceptsAnyTopic(t *testing.T) {
	le := newEngineWithChats(config.ListenerChat{ID: -1002121902112, Name: "Mi Grupo"})

	for _, topic := range []int64{0, 1, 57, 99} {
		if _, ok := le.matchChat(-1002121902112, 2121902112, topic); !ok {
			t.Fatalf("monitoring entire group should accept topic %d", topic)
		}
	}
}

func TestMatchChatPrefersSpecificTopicEntry(t *testing.T) {
	le := newEngineWithChats(
		config.ListenerChat{ID: -1001, Name: "Grupo", AutoDownload: false},
		config.ListenerChat{ID: -1001, TopicID: config.TopicPointer(57), TopicName: "Pelis", Name: "Grupo", AutoDownload: true},
	)

	chat, ok := le.matchChat(-1001, 0, 57)
	if !ok {
		t.Fatal("message for topic 57 should match")
	}
	if !chat.AutoDownload || chat.Topic() != 57 {
		t.Fatalf("specific topic entry should win: %+v", chat)
	}

	otro, ok := le.matchChat(-1001, 0, 12)
	if !ok {
		t.Fatal("other topics should match the group-wide entry")
	}
	if otro.HasTopic() {
		t.Fatalf("expected group-wide entry: %+v", otro)
	}
}

func TestMatchChatGeneralTopic(t *testing.T) {
	// Telegram no marca los mensajes del tema «General», llegan sin tema (0).
	le := newEngineWithChats(config.ListenerChat{
		ID:      -1001,
		TopicID: config.TopicPointer(config.GeneralTopicID),
		Name:    "Grupo",
	})

	if _, ok := le.matchChat(-1001, 0, 0); !ok {
		t.Fatal("message without topic should match General topic")
	}
	if _, ok := le.matchChat(-1001, 0, 57); ok {
		t.Fatal("message for another topic is not General")
	}
}

func TestMatchChatAlternativeIDs(t *testing.T) {
	// El chat puede estar configurado con el ID interno del canal en vez del
	// canónico con prefijo -100.
	le := newEngineWithChats(config.ListenerChat{ID: 2121902112, Name: "Grupo"})

	if _, ok := le.matchChat(-1002121902112, 2121902112, 0); !ok {
		t.Fatal("channel should be recognized even if noted with internal ID")
	}
}

func TestMatchChatUnmonitoredChat(t *testing.T) {
	le := newEngineWithChats(config.ListenerChat{ID: -1001, Name: "Grupo"})

	if _, ok := le.matchChat(-1009, 0, 0); ok {
		t.Fatal("unmonitored chat should not match")
	}
}

func TestHandleMessageListenSelf(t *testing.T) {
	ctx := context.Background()

	le := newEngineWithChats(
		config.ListenerChat{ID: -1001, Name: "Grupo 1", ListenSelf: false, AutoDownload: false, FPhotos: true},
		config.ListenerChat{ID: -1002, Name: "Grupo 2", ListenSelf: true, AutoDownload: false, FPhotos: true},
	)

	photoMedia := &tg.MessageMediaPhoto{
		Photo: &tg.Photo{ID: 123},
	}

	msg1 := &tg.Message{
		ID:     101,
		Out:    true,
		PeerID: &tg.PeerChannel{ChannelID: 1},
		Media:  photoMedia,
	}
	_ = le.HandleMessage(ctx, tg.Entities{}, msg1)
	if len(le.GetItems()) != 0 {
		t.Fatalf("expected 0 items for outgoing message on chat with ListenSelf=false, got %d", len(le.GetItems()))
	}

	msg2 := &tg.Message{
		ID:     102,
		Out:    true,
		PeerID: &tg.PeerChannel{ChannelID: 2},
		Media:  photoMedia,
	}
	_ = le.HandleMessage(ctx, tg.Entities{}, msg2)
	if len(le.GetItems()) != 1 {
		t.Fatalf("expected 1 item for outgoing message on chat with ListenSelf=true, got %d", len(le.GetItems()))
	}
}
