package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"tgdown/pkg/i18n"
)

// Notifier maneja el envío de notificaciones mediante un bot de Telegram.
type Notifier struct {
	token   string
	chatID  int64
	topicID *int64 // Opcional: para grupos con temas
	client  *http.Client
}

// NewNotifier crea una nueva instancia de Notifier.
func NewNotifier(token string, chatID int64, topicID *int64) *Notifier {
	return &Notifier{
		token:   token,
		chatID:  chatID,
		topicID: topicID,
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// SendMessageEnvia un mensaje de texto al chat configurado.
// Fire-and-forget: no bloquea al llamante; los errores se registran en log.
func (n *Notifier) SendMessage(ctx context.Context, text string) {
	go func() {
		if err := n.sendSync(ctx, text); err != nil {
			log.Printf("[NOTIFIER] %s", i18n.T("notifier.sendError", err))
		}
	}()
}

// sendSync realiza el envío síncrono del mensaje.
func (n *Notifier) sendSync(ctx context.Context, text string) error {
	if n.token == "" || n.chatID == 0 {
		return fmt.Errorf("notifier not configured: missing token or chat ID")
	}

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", n.token)

	payload := map[string]any{
		"chat_id":    n.chatID,
		"text":       text,
		"parse_mode": "HTML",
	}

	// Si hay topicID especificado, agregarlo al payload
	if n.topicID != nil && *n.topicID > 0 {
		payload["message_thread_id"] = *n.topicID
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		// SEC-10: el error de url.Parse arrastra la URL completa (con el
		// token); el texto final es el que acaba en los logs.
		return RedactError(fmt.Errorf("failed to create request: %w", err), n.token)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		// SEC-10: *url.Error incluye la URL con el token y este error se
		// registra en logbus vía SendMessage; el token solo puede entrar en
		// un mensaje por la URL (apiURL), así que este y el return anterior
		// son los dos únicos puntos donde hace falta redactar.
		return RedactError(fmt.Errorf("HTTP request failed: %w", err), n.token)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Telegram API returned status %d", resp.StatusCode)
	}

	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	if !result.OK {
		return fmt.Errorf("Telegram API error: %s", result.Error)
	}

	return nil
}

// SendMessageWithKeyboardSync envía un mensaje con un teclado en línea (inline_keyboard).
func (n *Notifier) SendMessageWithKeyboardSync(ctx context.Context, text string, replyMarkup map[string]any) error {
	if n.token == "" || n.chatID == 0 {
		return fmt.Errorf("notifier not configured: missing token or chat ID")
	}

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", n.token)

	payload := map[string]any{
		"chat_id":      n.chatID,
		"text":         text,
		"parse_mode":   "HTML",
		"reply_markup": replyMarkup,
	}

	if n.topicID != nil && *n.topicID > 0 {
		payload["message_thread_id"] = *n.topicID
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return RedactError(fmt.Errorf("failed to create request: %w", err), n.token)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return RedactError(fmt.Errorf("HTTP request failed: %w", err), n.token)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Telegram API returned status %d", resp.StatusCode)
	}

	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	if !result.OK {
		return fmt.Errorf("Telegram API error: %s", result.Error)
	}

	return nil
}

// AnswerCallbackQuerySync responde a un evento callback query de un botón.
func (n *Notifier) AnswerCallbackQuerySync(ctx context.Context, callbackQueryID string) error {
	if n.token == "" {
		return fmt.Errorf("notifier not configured: missing token")
	}

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/answerCallbackQuery", n.token)

	payload := map[string]any{
		"callback_query_id": callbackQueryID,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return RedactError(fmt.Errorf("failed to create request: %w", err), n.token)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return RedactError(fmt.Errorf("HTTP request failed: %w", err), n.token)
	}
	defer resp.Body.Close()

	return nil
}

// EditMessageTextSync edita el texto de un mensaje enviado anteriormente por el bot.
func (n *Notifier) EditMessageTextSync(ctx context.Context, messageID int64, text string) error {
	if n.token == "" || n.chatID == 0 {
		return fmt.Errorf("notifier not configured: missing token or chat ID")
	}

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/editMessageText", n.token)

	payload := map[string]any{
		"chat_id":    n.chatID,
		"message_id": messageID,
		"text":       text,
		"parse_mode": "HTML",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return RedactError(fmt.Errorf("failed to create request: %w", err), n.token)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return RedactError(fmt.Errorf("HTTP request failed: %w", err), n.token)
	}
	defer resp.Body.Close()

	return nil
}

// DetectChatIntententa detectar el chatID usando getUpdates del Bot API.
// Devuelve el chatID detectado y el nombre del chat (si está disponible).
func (n *Notifier) DetectChat(ctx context.Context) (int64, string, error) {
	if n.token == "" {
		return 0, "", fmt.Errorf("token not configured")
	}

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates", n.token)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		// SEC-10: url.Parse incluye la URL completa (con el token) en el error.
		return 0, "", RedactError(fmt.Errorf("failed to create request: %w", err), n.token)
	}

	resp, err := n.client.Do(req)
	if err != nil {
		// SEC-10: *url.Error arrastra la URL con el token y este error se
		// devuelve entero al cliente en handleNotificationDetect.
		return 0, "", RedactError(fmt.Errorf("HTTP request failed: %w", err), n.token)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("Telegram API returned status %d", resp.StatusCode)
	}

	var result struct {
		OK     bool `json:"ok"`
		Result []struct {
			UpdateID int `json:"update_id"`
			Message  *struct {
				Chat struct {
					ID        int64  `json:"id"`
					Type      string `json:"type"`
					Username  string `json:"username,omitempty"`
					Title     string `json:"title,omitempty"`
					FirstName string `json:"first_name,omitempty"`
				} `json:"chat"`
				Text string `json:"text,omitempty"`
			} `json:"message,omitempty"`
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, "", fmt.Errorf("failed to decode response: %w", err)
	}

	if !result.OK {
		return 0, "", fmt.Errorf("Telegram API error")
	}

	if len(result.Result) == 0 {
		return 0, "", fmt.Errorf("no updates found; user must start the conversation first")
	}

	// Tomar el último mensaje
	lastUpdate := result.Result[len(result.Result)-1]
	if lastUpdate.Message == nil {
		return 0, "", fmt.Errorf("last update has no message")
	}

	chat := lastUpdate.Message.Chat
	chatName := chat.Title
	if chatName == "" {
		if chat.FirstName != "" {
			chatName = chat.FirstName
			if chat.Username != "" {
				chatName += " (@" + chat.Username + ")"
			}
		} else if chat.Username != "" {
			chatName = "@" + chat.Username
		} else {
			chatName = fmt.Sprintf("Chat %d", chat.ID)
		}
	}

	return chat.ID, chatName, nil
}

// redactedError (SEC-10) conserva el error original en la cadena de Unwrap
// para que errors.Is/As sigan funcionando, pero expone un mensaje ya
// redactado. El *url.Error de net/http incluye la URL completa con el token
// y ese texto renderizado es el que se filtra a logbus, al fichero de logs y
// a las respuestas HTTP, así que no puede viajar tal cual.
type redactedError struct {
	err     error
	message string
}

func (e *redactedError) Error() string { return e.message }
func (e *redactedError) Unwrap() error { return e.err }

// RedactError sustituye el token por «***» en el mensaje que se renderiza del
// error. No se reutiliza MaskToken porque este conserva fragmentos del token
// (primeros caracteres del id y últimas cifras del hash) pensando en mostrarlo
// en pantalla; aquí hace falta suprimirlo entero. Si el token no aparece en
// el texto se devuelve el error original sin envolver, para no añadir ruido a
// la cadena de unwrapping.
func RedactError(err error, token string) error {
	if err == nil || token == "" {
		return err
	}
	message := err.Error()
	redacted := strings.ReplaceAll(message, token, "***")
	if redacted == message {
		return err
	}
	return &redactedError{err: err, message: redacted}
}

// MaskToken devuelve una versión enmascarada del token para visualización segura.
func MaskToken(token string) string {
	if token == "" {
		return ""
	}
	parts := strings.Split(token, ":")
	if len(parts) != 2 {
		return "***"
	}
	// Mostrar solo los primeros 3 caracteres del ID y los últimos 4 caracteres del hash
	id := parts[0]
	hash := parts[1]
	if len(id) > 3 {
		id = id[:3] + strings.Repeat("*", len(id)-3)
	}
	if len(hash) > 4 {
		hash = strings.Repeat("*", len(hash)-4) + hash[len(hash)-4:]
	}
	return id + ":" + hash
}

// ValidateToken verifica que el token tenga el formato correcto (id:hash).
func ValidateToken(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("token cannot be empty")
	}

	parts := strings.Split(token, ":")
	if len(parts) != 2 {
		return fmt.Errorf("invalid token format: must be in format id:hash")
	}

	id := parts[0]
	hash := parts[1]

	if id == "" || hash == "" {
		return fmt.Errorf("invalid token: id and hash cannot be empty")
	}

	// Validar que sean caracteres válidos (alphanumeric y underscore)
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_') {
			return fmt.Errorf("invalid characters in token id")
		}
	}
	for _, c := range hash {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c == '-') {
			return fmt.Errorf("invalid characters in token hash")
		}
	}

	return nil
}

// ValidateChatID verifica que el chatID sea válido.
func ValidateChatID(chatID int64) error {
	if chatID == 0 {
		return fmt.Errorf("chat ID cannot be 0")
	}
	return nil
}

// ValidateTopicID verifica que el topicID sea válido (debe ser positivo si se especifica).
func ValidateTopicID(topicID *int64) error {
	if topicID != nil && *topicID <= 0 {
		return fmt.Errorf("topic ID must be positive if specified")
	}
	return nil
}
