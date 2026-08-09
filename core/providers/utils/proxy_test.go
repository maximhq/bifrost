package utils

import (
	"strings"
	"testing"

	ws "github.com/fasthttp/websocket"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func TestConfigureProxy_HTTPProxy_WithLiteralURL_ConfiguresDialer(t *testing.T) {
	client := &fasthttp.Client{}
	logger := testLogger{}
	cfg := &schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  schemas.NewSecretVar("http://127.0.0.1:1"),
	}

	ConfigureProxy(client, cfg, logger)

	if client.Dial == nil {
		t.Fatal("expected dialer to be configured for literal HTTP proxy URL")
	}
	_, err := client.Dial("example.com:80")
	if err == nil {
		t.Fatal("expected dial via test proxy to fail")
	}
	if !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("expected dial error to include proxy address, got: %v", err)
	}
}

func TestConfigureProxy_HTTPProxy_WithEnvURL_ConfiguresDialer(t *testing.T) {
	t.Setenv("BIFROST_TEST_PROXY_URL", "http://127.0.0.1:1")

	client := &fasthttp.Client{}
	logger := testLogger{}
	cfg := &schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  schemas.NewSecretVar("env.BIFROST_TEST_PROXY_URL"),
	}

	ConfigureProxy(client, cfg, logger)

	if client.Dial == nil {
		t.Fatal("expected dialer to be configured for env-backed HTTP proxy URL")
	}
	_, err := client.Dial("example.com:80")
	if err == nil {
		t.Fatal("expected dial via test proxy to fail")
	}
	if !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("expected dial error to include proxy address from env value, got: %v", err)
	}
}

func TestConfigureProxy_HTTPProxy_WithEmptyEnvValue_FailsFast(t *testing.T) {
	t.Setenv("BIFROST_TEST_PROXY_URL_EMPTY", "")

	client := &fasthttp.Client{}
	logger := testLogger{}
	cfg := &schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  schemas.NewSecretVar("env.BIFROST_TEST_PROXY_URL_EMPTY"),
	}

	ConfigureProxy(client, cfg, logger)

	if client.Dial == nil {
		t.Fatal("expected fail-fast dialer when env-backed proxy URL resolves empty")
	}
	_, err := client.Dial("example.com:80")
	if err == nil {
		t.Fatal("expected dial to fail with explicit configuration error")
	}
	if !strings.Contains(err.Error(), "proxy.url") || !strings.Contains(err.Error(), "env.BIFROST_TEST_PROXY_URL_EMPTY") {
		t.Fatalf("expected explicit proxy env configuration error, got: %v", err)
	}
}

func TestConfigureProxy_HTTPProxy_WithUnsetLiteralURL_KeepsDefaultBehavior(t *testing.T) {
	client := &fasthttp.Client{}
	logger := testLogger{}
	cfg := &schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  nil,
	}

	ConfigureProxy(client, cfg, logger)

	if client.Dial != nil {
		t.Fatal("expected dialer to remain unset when literal proxy URL is not provided")
	}
}

func TestConfigureWebSocketProxy_HTTPProxy_WithLiteralURL_ConfiguresDialer(t *testing.T) {
	dialer := &ws.Dialer{}
	cfg := &schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  schemas.NewSecretVar("http://127.0.0.1:1"),
	}

	_, err := ConfigureWebSocketProxy(dialer, cfg)
	if err != nil {
		t.Fatalf("expected no error configuring WebSocket proxy, got: %v", err)
	}
	if dialer.Proxy == nil {
		t.Fatal("expected dialer.Proxy to be configured for literal HTTP proxy URL")
	}
	proxyURL, err := dialer.Proxy(nil)
	if err != nil {
		t.Fatalf("expected proxy func to resolve without error, got: %v", err)
	}
	if proxyURL == nil || proxyURL.Host != "127.0.0.1:1" {
		t.Fatalf("expected resolved proxy URL host 127.0.0.1:1, got: %v", proxyURL)
	}
}

func TestConfigureWebSocketProxy_HTTPProxy_WithEnvURL_ConfiguresDialer(t *testing.T) {
	t.Setenv("BIFROST_TEST_WS_PROXY_URL", "http://127.0.0.1:1")

	dialer := &ws.Dialer{}
	cfg := &schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  schemas.NewSecretVar("env.BIFROST_TEST_WS_PROXY_URL"),
	}

	_, err := ConfigureWebSocketProxy(dialer, cfg)
	if err != nil {
		t.Fatalf("expected no error configuring WebSocket proxy, got: %v", err)
	}
	if dialer.Proxy == nil {
		t.Fatal("expected dialer.Proxy to be configured for env-backed HTTP proxy URL")
	}
}

func TestConfigureWebSocketProxy_HTTPProxy_WithEmptyEnvValue_FailsFast(t *testing.T) {
	t.Setenv("BIFROST_TEST_WS_PROXY_URL_EMPTY", "")

	dialer := &ws.Dialer{}
	cfg := &schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  schemas.NewSecretVar("env.BIFROST_TEST_WS_PROXY_URL_EMPTY"),
	}

	_, err := ConfigureWebSocketProxy(dialer, cfg)
	if err == nil {
		t.Fatal("expected fail-fast error when env-backed proxy URL resolves empty")
	}
	if !strings.Contains(err.Error(), "proxy.url") || !strings.Contains(err.Error(), "env.BIFROST_TEST_WS_PROXY_URL_EMPTY") {
		t.Fatalf("expected explicit proxy env configuration error, got: %v", err)
	}
}

func TestConfigureWebSocketProxy_HTTPProxy_WithUnsetLiteralURL_KeepsDefaultBehavior(t *testing.T) {
	dialer := &ws.Dialer{}
	cfg := &schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  nil,
	}

	_, err := ConfigureWebSocketProxy(dialer, cfg)
	if err != nil {
		t.Fatalf("expected no error when proxy URL is unset, got: %v", err)
	}
	if dialer.Proxy != nil {
		t.Fatal("expected dialer.Proxy to remain unset when literal proxy URL is not provided")
	}
}

func TestConfigureWebSocketProxy_Socks5Proxy_ConfiguresDialer(t *testing.T) {
	dialer := &ws.Dialer{}
	cfg := &schemas.ProxyConfig{
		Type: schemas.Socks5Proxy,
		URL:  schemas.NewSecretVar("socks5://127.0.0.1:1080"),
	}

	_, err := ConfigureWebSocketProxy(dialer, cfg)
	if err != nil {
		t.Fatalf("expected no error configuring WebSocket proxy, got: %v", err)
	}
	if dialer.Proxy == nil {
		t.Fatal("expected dialer.Proxy to be configured for SOCKS5 proxy URL")
	}
	proxyURL, err := dialer.Proxy(nil)
	if err != nil {
		t.Fatalf("expected proxy func to resolve without error, got: %v", err)
	}
	if proxyURL == nil || proxyURL.Scheme != "socks5" || proxyURL.Host != "127.0.0.1:1080" {
		t.Fatalf("expected resolved socks5://127.0.0.1:1080 proxy URL, got: %v", proxyURL)
	}
}

func TestConfigureWebSocketProxy_NoProxy_LeavesDialerUnset(t *testing.T) {
	dialer := &ws.Dialer{}
	cfg := &schemas.ProxyConfig{Type: schemas.NoProxy}

	_, err := ConfigureWebSocketProxy(dialer, cfg)
	if err != nil {
		t.Fatalf("expected no error for NoProxy type, got: %v", err)
	}
	if dialer.Proxy != nil {
		t.Fatal("expected dialer.Proxy to remain unset for NoProxy type")
	}
}

func TestConfigureWebSocketProxy_NilConfig_LeavesDialerUnset(t *testing.T) {
	dialer := &ws.Dialer{}

	_, err := ConfigureWebSocketProxy(dialer, nil)
	if err != nil {
		t.Fatalf("expected no error for nil proxy config, got: %v", err)
	}
	if dialer.Proxy != nil {
		t.Fatal("expected dialer.Proxy to remain unset for nil proxy config")
	}
}
