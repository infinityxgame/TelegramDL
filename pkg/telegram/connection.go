package telegram

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/beevik/ntp"
	gotdlog "github.com/gotd/log"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"golang.org/x/net/proxy"

	"tgdown/pkg/config"
	"tgdown/pkg/i18n"
)

const (
	transportDirect = "direct"
	transportWS     = "websocket"
)

var errInitialConnectTimeout = errors.New("initial Telegram connection timed out")

type sessionEvidenceStore interface {
	MarkTelegramAuthPending() error
	MarkTelegramAuthorized(sessionID string, user UserInfo) error
	GetTelegramAuthEvidence() (sessionID string, user *UserInfo, pending bool, err error)
	ClearTelegramAuthEvidence() error
}

type authClient interface {
	Status(ctx context.Context) (*auth.Status, error)
	SendCode(ctx context.Context, phone string, opts auth.SendCodeOptions) (tg.AuthSentCodeClass, error)
	SignIn(ctx context.Context, phone, code, phoneCodeHash string) (tg.AuthAuthorizationClass, error)
	Password(ctx context.Context, password string) (tg.AuthAuthorizationClass, error)
}

type realAuthClient struct {
	auth *auth.Client
}

func (r *realAuthClient) Status(ctx context.Context) (*auth.Status, error) {
	return r.auth.Status(ctx)
}

func (r *realAuthClient) SendCode(ctx context.Context, phone string, opts auth.SendCodeOptions) (tg.AuthSentCodeClass, error) {
	return r.auth.SendCode(ctx, phone, opts)
}

func (r *realAuthClient) SignIn(ctx context.Context, phone, code, phoneCodeHash string) (tg.AuthAuthorizationClass, error) {
	return r.auth.SignIn(ctx, phone, code, phoneCodeHash)
}

func (r *realAuthClient) Password(ctx context.Context, password string) (tg.AuthAuthorizationClass, error) {
	return r.auth.Password(ctx, password)
}

type managedClient interface {
	Run(context.Context, func(context.Context) error) error
	API() *tg.Client
	Auth() *auth.Client
	Self(context.Context) (*tg.User, error)
	telegramClient() *telegram.Client
}

type realManagedClient struct {
	client *telegram.Client
}

func (r *realManagedClient) telegramClient() *telegram.Client { return r.client }

func (r *realManagedClient) Run(ctx context.Context, f func(context.Context) error) error {
	return r.client.Run(ctx, f)
}

func (r *realManagedClient) API() *tg.Client {
	return r.client.API()
}

func (r *realManagedClient) Auth() *auth.Client {
	return r.client.Auth()
}

func (r *realManagedClient) Self(ctx context.Context) (*tg.User, error) {
	return r.client.Self(ctx)
}

type synchronizedClock struct {
	offset int64
	ready  chan struct{}
}

func newSynchronizedClock() *synchronizedClock {
	sc := &synchronizedClock{ready: make(chan struct{})}
	go sc.syncNTP()
	return sc
}

func (c *synchronizedClock) syncNTP() {
	// MTProto no debe crear su primer message ID antes de conocer el desfase
	// del reloj. Cerramos ready tanto si NTP funciona como si agota los
	// servidores, para no bloquear indefinidamente el arranque.
	defer close(c.ready)
	servers := []string{"pool.ntp.org", "time.google.com", "time.cloudflare.com"}
	for _, server := range servers {
		response, err := ntp.QueryWithOptions(server, ntp.QueryOptions{Timeout: 2 * time.Second})
		if err == nil && response != nil {
			if err := response.Validate(); err == nil {
				atomic.StoreInt64(&c.offset, response.ClockOffset.Nanoseconds())
				log.Printf("[TG CLIENT] NTP clock synchronized with %s (offset: %v)", server, response.ClockOffset)
				return
			}
		}
	}
	log.Printf("[TG CLIENT] Could not query NTP servers. Ensure system date, time, and timezone are correctly configured.")
}

func (c *synchronizedClock) Now() time.Time {
	offset := atomic.LoadInt64(&c.offset)
	now := clock.System.Now()
	if offset != 0 {
		return now.Add(time.Duration(offset))
	}
	return now
}

func (c *synchronizedClock) Timer(d time.Duration) clock.Timer {
	return clock.System.Timer(d)
}

func (c *synchronizedClock) Ticker(d time.Duration) clock.Ticker {
	return clock.System.Ticker(d)
}

func (cm *ClientManager) SetSessionStore(store sessionEvidenceStore) {
	cm.mu.Lock()
	cm.sessionStore = store
	cm.mu.Unlock()
}

type telegramNetworkLogger struct {
	mu       sync.Mutex
	lastSeen map[string]time.Time
}

func (l *telegramNetworkLogger) Enabled(_ context.Context, level gotdlog.Level) bool {
	return level >= gotdlog.LevelInfo
}

func (l *telegramNetworkLogger) Log(_ context.Context, level gotdlog.Level, msg string, attrs ...gotdlog.Attr) {
	interesting := level >= gotdlog.LevelWarn
	for _, allowed := range []string{"Restarting connection", "Permanent connection error", "Local clock needs synchronization"} {
		if msg == allowed {
			interesting = true
		}
	}
	if !interesting {
		return
	}

	l.mu.Lock()
	if l.lastSeen == nil {
		l.lastSeen = make(map[string]time.Time)
	}
	if last, ok := l.lastSeen[msg]; ok && time.Since(last) < 5*time.Second {
		l.mu.Unlock()
		return
	}
	l.lastSeen[msg] = time.Now()
	l.mu.Unlock()

	var detail string
	for _, attr := range attrs {
		if attr.Key == "error" || attr.Key == "err" || attr.Key == "time_difference" {
			if attr.Value.Kind() == gotdlog.KindError {
				detail = attr.Value.Error().Error()
			} else {
				detail = attr.Value.String()
			}
		}
	}
	if detail != "" {
		log.Printf("[TG CLIENT] %s: %s", msg, detail)
		return
	}
	log.Printf("[TG CLIENT] %s", msg)
}

func (cm *ClientManager) stateChangedLocked() {
	if cm.stateChanged == nil {
		cm.stateChanged = make(chan struct{})
		return
	}
	close(cm.stateChanged)
	cm.stateChanged = make(chan struct{})
}

func (cm *ClientManager) markConnecting(gen uint64, transport string) {
	cm.mu.Lock()
	if cm.generation == gen {
		cm.connected = false
		cm.transport = transport
		cm.stateChangedLocked()
	}
	cm.mu.Unlock()
}

func (cm *ClientManager) markReady(gen uint64) {
	cm.mu.Lock()
	if cm.generation == gen && cm.client != nil {
		cm.connected = true
		cm.lastConnErr = nil
		cm.stateChangedLocked()
	}
	cm.mu.Unlock()
}

func (cm *ClientManager) publishClient(gen uint64, transport string, client managedClient) bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.generation != gen {
		return false
	}
	cm.mClient = client
	if client != nil {
		if realC := client.telegramClient(); realC != nil {
			cm.client = realC
			cm.rawClient = realC.API()
		}
	}
	cm.transport = transport
	cm.connected = false
	cm.stateChangedLocked()
	return true
}

// WaitReady sigue al cliente vigente, incluso si el transporte cambia mientras
// la petición está esperando. Un canal cerrado nunca equivale a una sesión sana.
func (cm *ClientManager) WaitReady(ctx context.Context) error {
	timer := time.NewTimer(40 * time.Second)
	defer timer.Stop()
	for {
		cm.mu.RLock()
		client := cm.client
		ready := cm.connected
		changed := cm.stateChanged
		apiID := cm.apiID
		cm.mu.RUnlock()
		if apiID == 0 || client == nil {
			return errors.New(i18n.T("telegram.notConfigured"))
		}
		if ready {
			return nil
		}
		select {
		case <-changed:
			continue
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errors.New(i18n.T("telegram.timeoutConnecting"))
		}
	}
}

func (cm *ClientManager) stopRunningLocked() {
	// A QR token belongs to the current MTProto client. Cancel its polling
	// before replacing the client so it cannot authenticate on a stale key.
	cm.CancelQRLogin()
	cm.mu.Lock()
	cm.generation++
	cancel := cm.cancelRun
	cm.cancelRun = nil
	cm.client = nil
	cm.rawClient = nil
	cm.connected = false
	cm.stateChangedLocked()
	cm.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	cm.runWg.Wait()
	cm.closeDownloadPool()

	cm.mu.Lock()
	cm.dcPools = make(map[int]telegram.CloseInvoker)
	cm.dcClients = make(map[int]*tg.Client)
	cm.primaryDC = 0
	cm.mu.Unlock()
}

func (cm *ClientManager) stopRunning() {
	cm.lifecycleMu.Lock()
	defer cm.lifecycleMu.Unlock()
	cm.stopRunningLocked()
}

func (cm *ClientManager) Stop() {
	cm.stopRunning()
}

func (cm *ClientManager) SetProxyConfig(cfg config.Config) {
	cm.lifecycleMu.Lock()
	defer cm.lifecycleMu.Unlock()

	cm.mu.Lock()
	changed := !cm.cfgProxyInitialized ||
		cm.cfgProxyEnabled != cfg.ProxyEnabled ||
		cm.cfgProxyType != cfg.ProxyType ||
		cm.cfgProxyHost != cfg.ProxyHost ||
		cm.cfgProxyPort != cfg.ProxyPort ||
		cm.cfgProxyUsername != cfg.ProxyUsername ||
		cm.cfgProxyPassword != cfg.ProxyPassword ||
		cm.cfgProxySecret != cfg.ProxySecret
	cm.cfgProxyInitialized = true
	cm.cfgProxyEnabled = cfg.ProxyEnabled
	cm.cfgProxyType = cfg.ProxyType
	cm.cfgProxyHost = cfg.ProxyHost
	cm.cfgProxyPort = cfg.ProxyPort
	cm.cfgProxyUsername = cfg.ProxyUsername
	cm.cfgProxyPassword = cfg.ProxyPassword
	cm.cfgProxySecret = cfg.ProxySecret
	cm.proxyEnabled = cfg.ProxyEnabled
	cm.proxyType = cfg.ProxyType
	cm.proxyHost = cfg.ProxyHost
	cm.proxyPort = cfg.ProxyPort
	cm.proxyUsername = cfg.ProxyUsername
	cm.proxyPassword = cfg.ProxyPassword
	cm.proxySecret = cfg.ProxySecret
	apiID := cm.apiID
	apiHash := cm.apiHash
	deferRestart := cm.authStage != ""
	if changed && deferRestart {
		cm.proxyRestart = true
	}
	cm.mu.Unlock()

	if changed && deferRestart {
		log.Printf("[TG CLIENT] Proxy change saved; it will be applied after the current authentication step")
		return
	}
	if changed && apiID != 0 && apiHash != "" {
		if err := cm.initClientLocked(strconv.Itoa(apiID), apiHash); err != nil {
			log.Printf("[TG CLIENT ERROR] Could not apply proxy configuration: %v", err)
		}
	}
}

func (cm *ClientManager) SetOnProxyFailed(fn func(err error)) {
	cm.mu.Lock()
	cm.onProxyFailed = fn
	cm.mu.Unlock()
}

func (cm *ClientManager) notifyProxyFailure(gen uint64, err error) {
	cm.mu.Lock()
	if cm.generation != gen || cm.proxyFailureNotified == gen {
		cm.mu.Unlock()
		return
	}
	cm.proxyFailureNotified = gen
	cm.proxyEnabled = false
	cb := cm.onProxyFailed
	cm.mu.Unlock()
	if cb != nil {
		// La reconfiguración nunca debe esperar a esta misma goroutine de Run.
		go cb(err)
	}
}

func (cm *ClientManager) InitClient(apiIDStr, apiHash string) error {
	return cm.InitClientExplicit(apiIDStr, apiHash, false)
}

func (cm *ClientManager) InitClientExplicit(apiIDStr, apiHash string, isNewSetup bool) error {
	apiIDStr = strings.TrimSpace(apiIDStr)
	apiHash = strings.TrimSpace(apiHash)
	if apiIDStr == "" || apiHash == "" {
		cm.lifecycleMu.Lock()
		defer cm.lifecycleMu.Unlock()
		cm.stopRunningLocked()
		cm.mu.Lock()
		cm.apiID = 0
		cm.apiHash = ""
		cm.mu.Unlock()
		if cm.sessionStore != nil && isNewSetup {
			_ = cm.sessionStore.MarkTelegramAuthPending()
		}
		return nil
	}
	id, err := strconv.Atoi(apiIDStr)
	if err != nil || id <= 0 {
		return fmt.Errorf("%s: %w", i18n.T("telegram.invalidAPIID"), err)
	}
	if len(apiHash) != 32 {
		return errors.New(i18n.T("telegram.invalidAPIHash"))
	}
	for _, r := range apiHash {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return errors.New(i18n.T("telegram.invalidAPIHash"))
		}
	}

	cm.lifecycleMu.Lock()
	defer cm.lifecycleMu.Unlock()
	if cm.sessionStore != nil && isNewSetup {
		if err := cm.sessionStore.MarkTelegramAuthPending(); err != nil {
			return fmt.Errorf("could not save authentication setup state: %w", err)
		}
		cm.mu.Lock()
		cm.setupPending = true
		cm.mu.Unlock()
	}
	return cm.initClientLocked(apiIDStr, apiHash)
}

func (cm *ClientManager) initClientLocked(apiIDStr, apiHash string) error {
	id, err := strconv.Atoi(apiIDStr)
	if err != nil || id <= 0 {
		return fmt.Errorf("%s: %w", i18n.T("telegram.invalidAPIID"), err)
	}
	cm.stopRunningLocked()
	cm.mu.Lock()
	cm.apiID = id
	cm.apiHash = apiHash
	cm.lastConnErr = nil
	gen := cm.generation
	ctx, cancel := context.WithCancel(context.Background())
	cm.cancelRun = cancel
	cm.stateChangedLocked()
	cm.mu.Unlock()

	if cm.clock == nil {
		cm.clock = newSynchronizedClock()
	}
	cm.runWg.Add(1)
	go func() {
		defer cm.runWg.Done()
		cm.supervise(gen, ctx)
	}()
	return nil
}

func (cm *ClientManager) baseOptions(gen uint64) telegram.Options {
	return telegram.Options{
		SessionStorage: &session.FileStorage{Path: cm.sessionPath},
		Device: telegram.DeviceConfig{
			DeviceModel:   "TGDown Desktop",
			SystemVersion: systemVersionLabel(),
			AppVersion:    config.AppVersion,
			LangCode:      "es",
		},
		Clock:  cm.clock,
		Logger: &telegramNetworkLogger{},
		OnDead: func(err error) {
			cm.mu.Lock()
			if cm.generation == gen && !errors.Is(err, context.Canceled) {
				cm.lastConnErr = err
			}
			cm.mu.Unlock()
		},
		OnSelfError: func(context.Context, error) error {
			// No autorizado es el estado normal antes de introducir teléfono/código.
			return nil
		},
	}
}

func (cm *ClientManager) configureTransport(opts *telegram.Options, gen uint64, transport string) error {
	if transport == transportWS {
		opts.Resolver = dcs.Websocket(dcs.WebsocketOptions{})
		log.Printf("[TG CLIENT] %s", i18n.T("telegram.websocketEnabled"))
		return nil
	}

	cm.mu.RLock()
	enabled := cm.proxyEnabled
	proxyType := strings.ToLower(strings.TrimSpace(cm.proxyType))
	host := strings.TrimSpace(cm.proxyHost)
	port := cm.proxyPort
	username := cm.proxyUsername
	password := cm.proxyPassword
	secret := cm.proxySecret
	cm.mu.RUnlock()
	if enabled && host != "" && port > 0 {
		addr := net.JoinHostPort(host, strconv.Itoa(port))
		onFail := func(err error) { cm.notifyProxyFailure(gen, err) }
		switch proxyType {
		case "socks5":
			var credentials *proxy.Auth
			if username != "" || password != "" {
				credentials = &proxy.Auth{User: username, Password: password}
			}
			dialer, err := proxy.SOCKS5("tcp", addr, credentials, &net.Dialer{Timeout: 12 * time.Second})
			if err != nil {
				return fmt.Errorf("could not configure SOCKS5 proxy: %w", err)
			}
			var dial dcs.DialFunc
			if contextDialer, ok := dialer.(proxy.ContextDialer); ok {
				dial = contextDialer.DialContext
			} else {
				dial = func(_ context.Context, network, address string) (net.Conn, error) {
					return dialer.Dial(network, address)
				}
			}
			opts.Resolver = dcs.Plain(dcs.PlainOptions{Dial: wrapProxyDialer(dial, "SOCKS5", addr, onFail), PreferIPv6: false, Obfuscated: true})
		case "http":
			dial := buildHTTPProxyDialer(addr, username, password)
			opts.Resolver = dcs.Plain(dcs.PlainOptions{Dial: wrapProxyDialer(dial, "HTTP", addr, onFail), PreferIPv6: false, Obfuscated: true})
		case "mtproto":
			secretBytes, err := ParseMTProtoSecret(secret)
			if err != nil {
				return fmt.Errorf("invalid MTProto secret: %w", err)
			}
			dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", addr)
			}
			resolver, err := dcs.MTProxy(addr, secretBytes, dcs.MTProxyOptions{Dial: wrapProxyDialer(dial, "MTProto", addr, onFail)})
			if err != nil {
				return fmt.Errorf("could not configure MTProto proxy: %w", err)
			}
			opts.Resolver = resolver
		default:
			return fmt.Errorf("unsupported proxy type: %s", proxyType)
		}
		log.Printf("[TG CLIENT] Proxy %s enabled", proxyType)
		return nil
	}

	opts.Resolver = dcs.Plain(dcs.PlainOptions{PreferIPv6: !shouldDisableIPv6(), Obfuscated: true})
	return nil
}

func (cm *ClientManager) openDownloadPoolFor(client managedClient) error {
	if client == nil {
		return errors.New("client is nil")
	}
	pool, err := client.telegramClient().Pool(int64(DownloadConnections))
	if err != nil {
		return err
	}
	cm.mu.Lock()
	cm.downloadPool = pool
	cm.downloadClient = tg.NewClient(pool)
	cm.downloadPoolNotice = false
	cm.mu.Unlock()
	return nil
}

func (cm *ClientManager) serveConnected(gen uint64, runCtx context.Context, client managedClient, gaps *updates.Manager) error {
	if err := cm.openDownloadPoolFor(client); err != nil {
		log.Printf("[TG CLIENT] Download pool deferred: %v", err)
	}
	defer cm.closeDownloadPool()
	log.Printf("[TG CLIENT] %s", i18n.T("telegram.connected"))
	lastWaiting := time.Time{}
	for {
		self, err := client.Self(runCtx)
		if err == nil && self != nil {
			cm.rememberAuthorized(self)
			log.Printf("[TG CLIENT] %s", i18n.T("telegram.sessionActive", self.FirstName, self.LastName, self.Username, self.ID))
			return gaps.Run(runCtx, client.API(), self.ID, updates.AuthOptions{IsBot: false})
		}
		if err != nil && !auth.IsUnauthorized(err) && !errors.Is(err, context.Canceled) {
			log.Printf("[TG CLIENT] Session check failed: %v", err)
		}
		if time.Since(lastWaiting) > 30*time.Second {
			log.Printf("[TG CLIENT] %s", i18n.T("telegram.waitingAuth"))
			lastWaiting = time.Now()
		}
		select {
		case <-runCtx.Done():
			return runCtx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (cm *ClientManager) supervise(gen uint64, ctx context.Context) {
	// Esperar a que termine el intento NTP ANTES de construir el primer
	// cliente MTProto. Si el reloj local está desfasado, crear el cliente
	// antes de aplicar el offset puede dejar su generador de message IDs
	// inicializado con una hora incorrecta incluso después de sincronizar.
	cm.mu.RLock()
	syncClock, hasSyncClock := cm.clock.(*synchronizedClock)
	cm.mu.RUnlock()
	if hasSyncClock {
		select {
		case <-syncClock.ready:
		case <-ctx.Done():
			return
		}
	}

	transport := transportDirect
	if shouldEnableWebSocket() {
		transport = transportWS
	}
	for {
		if ctx.Err() != nil {
			return
		}
		opts := cm.baseOptions(gen)
		gaps := updates.New(updates.Config{Handler: cm.dispatcher})
		opts.UpdateHandler = gaps
		readySignal := make(chan struct{})
		var readyOnce sync.Once
		markReady := func() {
			readyOnce.Do(func() { close(readySignal) })
			cm.markReady(gen)
		}
		opts.OnConnectionState = func(state telegram.ConnectionState) {
			switch state {
			case telegram.ConnectionStateReady:
				markReady()
			case telegram.ConnectionStateConnecting, telegram.ConnectionStateDisconnected:
				cm.markConnecting(gen, transport)
			}
		}
		if err := cm.configureTransport(&opts, gen, transport); err != nil {
			log.Printf("[TG CLIENT ERROR] %v", err)
			if !waitForRetry(ctx, time.Second) {
				return
			}
			continue
		}
		cm.mu.RLock()
		newClientFn := cm.newClient
		cm.mu.RUnlock()
		if newClientFn == nil {
			newClientFn = func(o telegram.Options) managedClient {
				return &realManagedClient{client: telegram.NewClient(cm.apiID, cm.apiHash, o)}
			}
		}
		client := newClientFn(opts)
		if client == nil {
			log.Printf("[TG CLIENT ERROR] Could not create Telegram client")
			if !waitForRetry(ctx, time.Second) {
				return
			}
			continue
		}
		if !cm.publishClient(gen, transport, client) {
			return
		}
		log.Printf("[TG CLIENT] Iniciando intento MTProto (generation=%d, transport=%s)", gen, transport)

		attemptCtx, cancelAttempt := context.WithCancelCause(ctx)
		initialTimeout := 12 * time.Second
		if transport == transportWS {
			initialTimeout = 20 * time.Second
		}
		timer := time.AfterFunc(initialTimeout, func() {
			select {
			case <-readySignal:
			default:
				cancelAttempt(errInitialConnectTimeout)
			}
		})
		err := client.Run(attemptCtx, func(runCtx context.Context) error {
			markReady()
			go func() {
				dialogCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				if err := cm.FetchDialogs(dialogCtx); err != nil && !errors.Is(err, context.Canceled) {
					log.Printf("[TG CLIENT] Could not refresh dialogs: %v", err)
				}
			}()
			return cm.serveConnected(gen, runCtx, client, gaps)
		})
		timer.Stop()
		cancelAttempt(nil)
		wasReady := false
		select {
		case <-readySignal:
			wasReady = true
		default:
		}
		if ctx.Err() != nil {
			return
		}
		cm.mu.RLock()
		generationCurrent := cm.generation == gen && cm.client == client.telegramClient()
		cm.mu.RUnlock()
		if !generationCurrent {
			return
		}
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(context.Cause(attemptCtx), errInitialConnectTimeout) {
			log.Printf("[TG CLIENT ERROR] %s", i18n.T("telegram.runError", err))
		}
		log.Printf("[TG CLIENT] Intento MTProto finalizado (generation=%d, transport=%s, ready=%t, err=%v, context_cause=%v)",
			gen, transport, wasReady, err, context.Cause(attemptCtx))

		if transport == transportDirect && !wasReady {
			cm.mu.RLock()
			proxyEnabled := cm.proxyEnabled
			cm.mu.RUnlock()
			if proxyEnabled {
				cm.notifyProxyFailure(gen, errInitialConnectTimeout)
			}
			log.Printf("[TG CLIENT] Direct connection unavailable; trying WebSocket")
			transport = transportWS
			continue
		}
		if !waitForRetry(ctx, 2*time.Second) {
			return
		}
	}
}

func waitForRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
