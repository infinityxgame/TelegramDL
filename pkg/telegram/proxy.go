package telegram

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gotd/td/telegram/dcs"
	"golang.org/x/net/proxy"

	"tgdown/pkg/logbus"
)

// buildHTTPProxyDialer construye una función DialFunc que establece un túnel HTTP CONNECT
// a través de un proxy HTTP con autenticación opcional.
func buildHTTPProxyDialer(proxyAddr, username, password string) dcs.DialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", proxyAddr)
		if err != nil {
			return nil, fmt.Errorf("http proxy dial error: %w", err)
		}

		req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", addr, addr)
		if username != "" || password != "" {
			auth := username + ":" + password
			basicAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(auth))
			req += fmt.Sprintf("Proxy-Authorization: %s\r\n", basicAuth)
		}
		req += "\r\n"

		if deadline, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(deadline)
		}

		if _, err := conn.Write([]byte(req)); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("http proxy write error: %w", err)
		}

		br := bufio.NewReader(conn)
		res, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("http proxy response error: %w", err)
		}
		_ = res.Body.Close()

		_ = conn.SetDeadline(time.Time{})

		if res.StatusCode != http.StatusOK {
			_ = conn.Close()
			return nil, fmt.Errorf("http proxy status %s", res.Status)
		}

		return conn, nil
	}
}

// wrapProxyDialer envuelve una función DialFunc de proxy para contar los fallos de marcado consecutivos.
// Si el proxy falla 2 veces consecutivas al intentar conectar con Telegram, dispara inmediatamente
// la función de desactivado automático (onFail) para conmutar a conexión directa.
func wrapProxyDialer(dialFunc dcs.DialFunc, proxyType, proxyAddr string, onFail func(err error)) dcs.DialFunc {
	var failCount int32
	var triggered int32

	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dialFunc(ctx, network, addr)
		if err == nil {
			atomic.StoreInt32(&failCount, 0)
			return conn, nil
		}

		fails := atomic.AddInt32(&failCount, 1)
		msg := fmt.Sprintf("El proxy %s (%s) falló al conectar (%d/2): %v", proxyType, proxyAddr, fails, err)
		log.Printf("[TG CLIENT ERROR] %s", msg)
		logbus.Error(logbus.CatTelegram, "Fallo de conexión en proxy", msg)

		if fails >= 2 && atomic.CompareAndSwapInt32(&triggered, 0, 1) {
			if onFail != nil {
				go onFail(err)
			}
		}

		return nil, err
	}
}

// TestProxy comprueba la disponibilidad y mide la latencia (en ms) de un proxy SOCKS5, HTTP o MTProto.
func TestProxy(pType, host string, port int, username, password, secret string) (bool, int64, error) {
	if strings.TrimSpace(host) == "" || port <= 0 {
		return false, 0, errors.New("host and port are required")
	}

	proxyAddr := net.JoinHostPort(strings.TrimSpace(host), strconv.Itoa(port))
	testTargets := []string{"149.154.167.50:443", "149.154.175.50:443", "91.108.56.130:443"}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	switch strings.ToLower(strings.TrimSpace(pType)) {
	case "socks5":
		var auth *proxy.Auth
		if username != "" || password != "" {
			auth = &proxy.Auth{User: username, Password: password}
		}
		dialer, err := proxy.SOCKS5("tcp", proxyAddr, auth, &net.Dialer{Timeout: 5 * time.Second})
		if err != nil {
			return false, 0, fmt.Errorf("socks5 setup error: %w", err)
		}
		cd, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return false, 0, errors.New("socks5 dialer does not support context")
		}

		var lastErr error
		for _, target := range testTargets {
			conn, err := cd.DialContext(ctx, "tcp", target)
			if err == nil {
				_ = conn.Close()
				return true, time.Since(start).Milliseconds(), nil
			}
			lastErr = err
		}
		return false, 0, fmt.Errorf("proxy reachable but telegram connection failed: %w", lastErr)

	case "http":
		dialFunc := buildHTTPProxyDialer(proxyAddr, username, password)
		var lastErr error
		for _, target := range testTargets {
			conn, err := dialFunc(ctx, "tcp", target)
			if err == nil {
				_ = conn.Close()
				return true, time.Since(start).Milliseconds(), nil
			}
			lastErr = err
		}
		return false, 0, fmt.Errorf("http proxy connection failed: %w", lastErr)

	case "mtproto":
		cleanSecret := strings.TrimSpace(secret)
		if len(cleanSecret) > 2 && (strings.HasPrefix(cleanSecret, "ee") || strings.HasPrefix(cleanSecret, "dd")) {
			cleanSecret = cleanSecret[2:]
		}
		if cleanSecret != "" {
			if _, err := hex.DecodeString(cleanSecret); err != nil {
				return false, 0, fmt.Errorf("invalid mtproto secret hex: %w", err)
			}
		}

		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", proxyAddr)
		if err != nil {
			return false, 0, fmt.Errorf("cannot connect to mtproto proxy server: %w", err)
		}
		_ = conn.Close()
		return true, time.Since(start).Milliseconds(), nil

	default:
		return false, 0, fmt.Errorf("unsupported proxy type: %s", pType)
	}
}
