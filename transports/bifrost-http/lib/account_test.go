package lib

import (
	"testing"

	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

func enabledGlobalProxy() *configstoreTables.GlobalProxyConfig {
	return &configstoreTables.GlobalProxyConfig{
		Enabled:            true,
		Type:               network.GlobalProxyTypeHTTP,
		URL:                "http://10.0.0.9:3128",
		Username:           "svc",
		Password:           "env.NOT_A_REFERENCE",
		NoProxy:            ".vpce.amazonaws.com",
		EnableForInference: true,
	}
}

// TestGetConfigForProvider_InheritsGlobalProxy pins that the global proxy's
// "Inference" toggle reaches providers. It used to be stored and never read, so
// turning it on changed nothing for provider traffic.
func TestGetConfigForProvider_InheritsGlobalProxy(t *testing.T) {
	ownProxy := &schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: schemas.NewSecretVar("http://own-proxy:8080")}
	store := &Config{
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.Vertex: {},
			schemas.Azure:  {ProxyConfig: &schemas.ProxyConfig{Type: schemas.NoProxy}},
			schemas.OpenAI: {ProxyConfig: ownProxy},
		},
		ProxyConfig: enabledGlobalProxy(),
	}
	account := NewBaseAccount(store)

	for _, provider := range []schemas.ModelProvider{schemas.Vertex, schemas.Azure} {
		config, err := account.GetConfigForProvider(provider)
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		got := config.ProxyConfig
		if got == nil || got.Type != schemas.HTTPProxy || got.URL.GetValue() != "http://10.0.0.9:3128" {
			t.Fatalf("%s: proxy = %+v, want the inherited global proxy", provider, got)
		}
		if got.NoProxy != ".vpce.amazonaws.com" {
			t.Errorf("%s: no_proxy = %q, want the global list", provider, got.NoProxy)
		}
		if got.Password.GetValue() != "env.NOT_A_REFERENCE" {
			t.Errorf("%s: password = %q, want the literal value (not resolved as an env reference)", provider, got.Password.GetValue())
		}
	}

	config, err := account.GetConfigForProvider(schemas.OpenAI)
	if err != nil {
		t.Fatalf("openai: %v", err)
	}
	if config.ProxyConfig != ownProxy {
		t.Errorf("a provider's own proxy must win over the global proxy, got %+v", config.ProxyConfig)
	}
}

func TestInheritGlobalProxy(t *testing.T) {
	disabled := enabledGlobalProxy()
	disabled.Enabled = false
	notForInference := enabledGlobalProxy()
	notForInference.EnableForInference = false
	noURL := enabledGlobalProxy()
	noURL.URL = " "
	tcp := enabledGlobalProxy()
	tcp.Type = network.GlobalProxyTypeTCP
	socks := enabledGlobalProxy()
	socks.Type = network.GlobalProxyTypeSOCKS5
	socks.URL = "socks5://10.0.0.9:1080"
	skipTLS := enabledGlobalProxy()
	skipTLS.SkipTLSVerify = true

	tests := []struct {
		name     string
		own      *schemas.ProxyConfig
		global   *configstoreTables.GlobalProxyConfig
		wantOK   bool
		wantType schemas.ProxyType
		wantSkip bool
	}{
		{name: "no global proxy", global: nil},
		{name: "global disabled", global: disabled},
		{name: "not enabled for inference", global: notForInference},
		{name: "global without URL", global: noURL},
		{name: "tcp has no provider equivalent", global: tcp},
		{name: "own proxy wins", own: &schemas.ProxyConfig{Type: schemas.EnvProxy}, global: enabledGlobalProxy()},
		{name: "nil own inherits", global: enabledGlobalProxy(), wantOK: true, wantType: schemas.HTTPProxy},
		{name: "none inherits (UI default)", own: &schemas.ProxyConfig{Type: schemas.NoProxy}, global: enabledGlobalProxy(), wantOK: true, wantType: schemas.HTTPProxy},
		{name: "empty type inherits", own: &schemas.ProxyConfig{}, global: enabledGlobalProxy(), wantOK: true, wantType: schemas.HTTPProxy},
		{name: "socks5 maps across", global: socks, wantOK: true, wantType: schemas.Socks5Proxy},
		{name: "skip_tls_verify carries over", global: skipTLS, wantOK: true, wantType: schemas.HTTPProxy, wantSkip: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := inheritGlobalProxy(tt.own, tt.global)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got.proxy.Type != tt.wantType {
				t.Errorf("type = %q, want %q", got.proxy.Type, tt.wantType)
			}
			if got.skipTLSVerify != tt.wantSkip {
				t.Errorf("skipTLSVerify = %v, want %v", got.skipTLSVerify, tt.wantSkip)
			}
		})
	}
}

// TestGetConfigForProvider_InheritedSkipTLSVerifyDoesNotLeak pins that carrying the
// global skip_tls_verify onto a provider changes only the config handed to core, never
// the provider's stored network config.
func TestGetConfigForProvider_InheritedSkipTLSVerifyDoesNotLeak(t *testing.T) {
	global := enabledGlobalProxy()
	global.SkipTLSVerify = true
	stored := schemas.DefaultNetworkConfig
	store := &Config{
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.Vertex: {NetworkConfig: &stored},
		},
		ProxyConfig: global,
	}

	config, err := NewBaseAccount(store).GetConfigForProvider(schemas.Vertex)
	if err != nil {
		t.Fatalf("GetConfigForProvider: %v", err)
	}
	if !config.NetworkConfig.InsecureSkipVerify {
		t.Error("inherited skip_tls_verify must reach the provider's effective network config")
	}
	if store.Providers[schemas.Vertex].NetworkConfig.InsecureSkipVerify {
		t.Error("the stored network config must stay untouched")
	}
}

func TestSetGlobalProxyConfig_StoresWithoutClient(t *testing.T) {
	store := &Config{Providers: map[schemas.ModelProvider]configstore.ProviderConfig{schemas.Vertex: {}}}
	global := enabledGlobalProxy()
	if err := store.SetGlobalProxyConfig(global); err != nil {
		t.Fatalf("SetGlobalProxyConfig: %v", err)
	}
	if store.GetGlobalProxyConfig() != global {
		t.Error("the global proxy must be stored")
	}
}
