package telegram

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image/png"
	"log"
	"strings"
	"time"

	tdtelegram "github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"rsc.io/qr"
)

// QRLoginStatus es el estado público del flujo QR. El token nunca se devuelve:
// solo se expone la imagen que debe escanearse y su caducidad.
type QRLoginStatus struct {
	Status    string `json:"status"`
	Image     string `json:"image,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
	Error     string `json:"error,omitempty"`
}

// StartQRLogin inicia un flujo QR en segundo plano. QR.Auth se encarga de
// renovar el token cuando caduca y de completar la migración entre DC.
func (cm *ClientManager) StartQRLogin(ctx context.Context) (QRLoginStatus, error) {
	if err := cm.WaitReady(ctx); err != nil {
		return QRLoginStatus{}, fmt.Errorf("telegram connection is not ready: %w", err)
	}

	cm.qrLoginMu.Lock()
	defer cm.qrLoginMu.Unlock()

	if cm.qrLoginCancel != nil {
		switch cm.qrLoginStatus.Status {
		case "WAITING_SCAN", "WAITING_2FA":
			return cm.qrLoginStatus, nil
		}
	}

	cm.mu.RLock()
	client := cm.client
	apiID := cm.apiID
	apiHash := cm.apiHash
	connected := cm.connected
	cm.mu.RUnlock()
	if client == nil || !connected || apiID <= 0 || apiHash == "" {
		return QRLoginStatus{}, errors.New("telegram client is not ready for QR login")
	}
	if _, err := client.Self(ctx); err == nil {
		return QRLoginStatus{}, errors.New("telegram session is already authorized")
	}

	// Descarta cualquier aviso de un intento anterior antes de arrancar uno nuevo.
	for {
		select {
		case <-cm.qrLoginSignal:
		default:
			goto drained
		}
	}

drained:
	qrCtx, cancel := context.WithCancel(context.Background())
	cm.qrLoginCancel = cancel
	cm.qrLoginStatus = QRLoginStatus{Status: "STARTING"}

	log.Printf("[TG AUTH] QR login starting; current client=%p", client)
	go cm.runQRLogin(qrCtx, client)
	return cm.qrLoginStatus, nil
}

func (cm *ClientManager) runQRLogin(ctx context.Context, client *tdtelegram.Client) {
	log.Printf("[TG AUTH] QR auth flow entered; client=%p", client)
	show := func(_ context.Context, token qrlogin.Token) error {
		serverExpiry := token.Expires()
		correctedNow := cm.qrClockNow()
		log.Printf("[TG AUTH] QR token received; expires_at=%s; corrected_now=%s; remaining=%s", serverExpiry.Format(time.RFC3339), correctedNow.Format(time.RFC3339), serverExpiry.Sub(correctedNow))
		img, err := token.Image(qr.M)
		if err != nil {
			return fmt.Errorf("encode Telegram QR: %w", err)
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return fmt.Errorf("encode Telegram QR PNG: %w", err)
		}
		dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())

		cm.qrLoginMu.Lock()
		if ctx.Err() == nil {
			cm.qrLoginStatus = QRLoginStatus{
				Status:    "WAITING_SCAN",
				Image:     dataURL,
				ExpiresAt: token.Expires().Unix(),
			}
		}
		cm.qrLoginMu.Unlock()
		log.Printf("[TG AUTH] QR image generated and published; expires_at=%d", token.Expires().Unix())
		return nil
	}

	log.Printf("[TG AUTH] Calling gotd QR().Auth()")
	_, err := client.QR().Auth(ctx, qrlogin.LoggedIn(cm.qrLoginSignal), show)
	log.Printf("[TG AUTH] gotd QR().Auth() returned; err=%v; context_err=%v", err, ctx.Err())
	if err != nil {
		if ctx.Err() != nil {
			log.Printf("[TG AUTH] QR auth flow stopped because its context was cancelled: %v", ctx.Err())
			return
		}
		if strings.Contains(strings.ToUpper(err.Error()), "SESSION_PASSWORD_NEEDED") {
			cm.mu.Lock()
			cm.authStage = "WAITING_2FA"
			cm.mu.Unlock()
			cm.qrLoginMu.Lock()
			cm.qrLoginStatus = QRLoginStatus{Status: "WAITING_2FA"}
			cm.qrLoginMu.Unlock()
			log.Printf("[TG AUTH] QR accepted; cloud password is required")
			return
		}
		cm.qrLoginMu.Lock()
		cm.qrLoginStatus = QRLoginStatus{Status: "ERROR", Error: err.Error()}
		cm.qrLoginCancel = nil
		cm.qrLoginMu.Unlock()
		log.Printf("[TG AUTH] QR login failed: %v", err)
		return
	}

	user, selfErr := client.Self(ctx)
	if selfErr != nil {
		cm.qrLoginMu.Lock()
		cm.qrLoginStatus = QRLoginStatus{Status: "ERROR", Error: selfErr.Error()}
		cm.qrLoginCancel = nil
		cm.qrLoginMu.Unlock()
		log.Printf("[TG AUTH] QR login completed but user lookup failed: %v", selfErr)
		return
	}
	cm.rememberAuthorized(user)
	cm.mu.Lock()
	cm.authStage = ""
	cm.mu.Unlock()
	cm.qrLoginMu.Lock()
	cm.qrLoginStatus = QRLoginStatus{Status: "LOGGED_IN"}
	cm.qrLoginCancel = nil
	cm.qrLoginMu.Unlock()
	log.Printf("[TG AUTH] QR login completed successfully")
}

// GetQRLoginStatus devuelve una copia del estado actual del flujo QR.
func (cm *ClientManager) GetQRLoginStatus() QRLoginStatus {
	cm.qrLoginMu.Lock()
	defer cm.qrLoginMu.Unlock()
	return cm.qrLoginStatus
}

// CancelQRLogin cancela el flujo QR actual sin cerrar el cliente MTProto.
func (cm *ClientManager) CancelQRLogin() {
	cm.qrLoginMu.Lock()
	previousStatus := cm.qrLoginStatus.Status
	cancel := cm.qrLoginCancel
	cm.qrLoginCancel = nil
	if cm.qrLoginStatus.Status != "LOGGED_IN" {
		cm.qrLoginStatus = QRLoginStatus{Status: "CANCELLED"}
	}
	cm.qrLoginMu.Unlock()
	if cancel != nil {
		log.Printf("[TG AUTH] Cancelling QR login; previous_status=%s", previousStatus)
		cancel()
	} else if previousStatus != "" && previousStatus != "LOGGED_IN" {
		log.Printf("[TG AUTH] QR status reset without active cancel function; previous_status=%s", previousStatus)
	}
}

// MarkQRLogin2FAComplete actualiza la interfaz después de que Verify2FA
// complete el paso de contraseña iniciado por un QR.
func (cm *ClientManager) MarkQRLogin2FAComplete() {
	cm.qrLoginMu.Lock()
	if cm.qrLoginStatus.Status == "WAITING_2FA" {
		cm.qrLoginStatus = QRLoginStatus{Status: "LOGGED_IN"}
		cm.qrLoginCancel = nil
	}
	cm.qrLoginMu.Unlock()
}

// qrClockNow devuelve la hora corregida por NTP que también utiliza gotd para
// generar los message IDs de MTProto. Los expires_at de Telegram son timestamps
// del servidor; compararlos con el reloj local sin corregir puede expirar un QR
// prematuramente cuando el reloj de Windows está adelantado.
func (cm *ClientManager) qrClockNow() time.Time {
	if cm.clock != nil {
		return cm.clock.Now()
	}
	return time.Now()
}

// Mantiene acotado el estado QR si se deja abierta la pantalla de acceso.
func (cm *ClientManager) ExpireQRLoginState() {
	now := cm.qrClockNow().Unix()
	cm.qrLoginMu.Lock()
	expiresAt := cm.qrLoginStatus.ExpiresAt
	if cm.qrLoginStatus.Status == "WAITING_SCAN" && expiresAt > 0 && now > expiresAt+300 {
		log.Printf("[TG AUTH] QR state expired; corrected_now=%d; token_expires_at=%d; grace_seconds=300", now, expiresAt)
		cm.qrLoginStatus = QRLoginStatus{Status: "EXPIRED"}
	}
	cm.qrLoginMu.Unlock()
}

