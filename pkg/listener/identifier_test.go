package listener

import (
	"context"
	"testing"
)

func TestResolveChatIdentifierNumericAndLinks(t *testing.T) {
	le := &ListenerEngine{}
	ctx := context.Background()

	// 1. Numeric ID
	chatID, topicID, err := le.ResolveChatIdentifier(ctx, "-1001234567890")
	if err != nil || chatID != -1001234567890 || topicID != 0 {
		t.Fatalf("expected -1001234567890, 0, nil; got %d, %d, %v", chatID, topicID, err)
	}

	// 2. Positive numeric ID
	chatID, topicID, err = le.ResolveChatIdentifier(ctx, "93372553")
	if err != nil || chatID != 93372553 || topicID != 0 {
		t.Fatalf("expected 93372553, 0, nil; got %d, %d, %v", chatID, topicID, err)
	}

	// 3. Private group link with topic
	chatID, topicID, err = le.ResolveChatIdentifier(ctx, "https://t.me/c/2121902112/57/31449")
	if err != nil || chatID != -1002121902112 || topicID != 57 {
		t.Fatalf("expected -1002121902112, 57, nil; got %d, %d, %v", chatID, topicID, err)
	}

	// 4. Private group link without topic
	chatID, topicID, err = le.ResolveChatIdentifier(ctx, "t.me/c/2121902112")
	if err != nil || chatID != -1002121902112 || topicID != 0 {
		t.Fatalf("expected -1002121902112, 0, nil; got %d, %d, %v", chatID, topicID, err)
	}

	// 5. Empty input
	_, _, err = le.ResolveChatIdentifier(ctx, "   ")
	if err == nil {
		t.Fatal("expected error for empty input")
	}
}
