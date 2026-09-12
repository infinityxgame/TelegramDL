package downloader

import "testing"

func TestParseURLChannelSingleMessage(t *testing.T) {
	res, err := ParseURL("https://t.me/c/2121902112/31449")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if !res.IsChannelID {
		t.Fatal("se esperaba IsChannelID = true")
	}
	if res.ChatID != -1002121902112 {
		t.Fatalf("ChatID incorrecto: got %d, want -1002121902112", res.ChatID)
	}
	if res.StartMsgID != 31449 || res.EndMsgID != 31449 {
		t.Fatalf("rango incorrecto: start=%d end=%d", res.StartMsgID, res.EndMsgID)
	}
}

func TestParseURLChannelRange(t *testing.T) {
	res, err := ParseURL("https://t.me/c/2121902112/31449-31455")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if res.StartMsgID != 31449 || res.EndMsgID != 31455 {
		t.Fatalf("rango incorrecto: start=%d end=%d", res.StartMsgID, res.EndMsgID)
	}
}

func TestParseURLRangeInvertedFails(t *testing.T) {
	_, err := ParseURL("https://t.me/c/2121902112/31455-31449")
	if err == nil {
		t.Fatal("se esperaba error cuando el mensaje final es menor que el inicial")
	}
}

func TestParseURLRangeTooLargeFails(t *testing.T) {
	url := "https://t.me/c/2121902112/1-600"
	_, err := ParseURL(url)
	if err == nil {
		t.Fatal("se esperaba error cuando el rango supera MaxMessagesPerJob")
	}
}

func TestParseURLRangeAtLimitSucceeds(t *testing.T) {
	// 500 mensajes exactos (1..500) debe ser válido, el límite es inclusivo.
	_, err := ParseURL("https://t.me/c/2121902112/1-500")
	if err != nil {
		t.Fatalf("un rango de exactamente %d mensajes no debería fallar: %v", MaxMessagesPerJob, err)
	}
}

func TestParseURLBotFormat(t *testing.T) {
	res, err := ParseURL("https://t.me/b/mybot/50")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if res.ChatUsername != "mybot" {
		t.Fatalf("username incorrecto: %q", res.ChatUsername)
	}
	if res.IsChannelID {
		t.Fatal("el formato /b/ no debería marcarse como IsChannelID")
	}
	if res.StartMsgID != 50 || res.EndMsgID != 50 {
		t.Fatalf("rango incorrecto: start=%d end=%d", res.StartMsgID, res.EndMsgID)
	}
}

func TestParseURLUsernameFormat(t *testing.T) {
	res, err := ParseURL("https://t.me/canal/100")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if res.ChatUsername != "canal" {
		t.Fatalf("username incorrecto: %q", res.ChatUsername)
	}
	if res.StartMsgID != 100 {
		t.Fatalf("StartMsgID incorrecto: %d", res.StartMsgID)
	}
}

func TestParseURLNormalizesTelegramMeDomain(t *testing.T) {
	res, err := ParseURL("telegram.me/canal/7")
	if err != nil {
		t.Fatalf("telegram.me debería normalizarse a t.me: %v", err)
	}
	if res.ChatUsername != "canal" || res.StartMsgID != 7 {
		t.Fatalf("resultado inesperado: %+v", res)
	}
}

func TestParseURLAddsSchemeWhenMissing(t *testing.T) {
	res, err := ParseURL("t.me/canal/9")
	if err != nil {
		t.Fatalf("una URL sin esquema debería aceptarse anteponiendo https://: %v", err)
	}
	if res.ChatUsername != "canal" || res.StartMsgID != 9 {
		t.Fatalf("resultado inesperado: %+v", res)
	}
}

func TestParseURLEmptyFails(t *testing.T) {
	if _, err := ParseURL(""); err == nil {
		t.Fatal("una URL vacía debe devolver error")
	}
	if _, err := ParseURL("   "); err == nil {
		t.Fatal("una URL con solo espacios debe devolver error")
	}
}

func TestParseURLGarbageFails(t *testing.T) {
	if _, err := ParseURL("https://example.com/not-telegram"); err == nil {
		t.Fatal("una URL que no es de Telegram debe devolver error")
	}
}
