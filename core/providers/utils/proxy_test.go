package utils

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func TestConfigureProxy_HTTPProxy_WithLiteralURL_ConfiguresDialer(t *testing.T) {
	client := &fasthttp.Client{}
	logger := testLogger{}
	cfg := &schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  schemas.NewEnvVar("http://127.0.0.1:1"),
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
		URL:  schemas.NewEnvVar("env.BIFROST_TEST_PROXY_URL"),
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
		URL:  schemas.NewEnvVar("env.BIFROST_TEST_PROXY_URL_EMPTY"),
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
