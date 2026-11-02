package lib

import (
	"reflect"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/network"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/valyala/fasthttp"
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
	withCA := enabledGlobalProxy()
	withCA.CACertPEM = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"

	tests := []struct {
		name     string
		own      *schemas.ProxyConfig
		global   *configstoreTables.GlobalProxyConfig
		wantOK   bool
		wantType schemas.ProxyType
		wantSkip bool
		wantCA   string
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
		{name: "ca_cert_pem carries over", global: withCA, wantOK: true, wantType: schemas.HTTPProxy, wantCA: withCA.CACertPEM},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := inheritGlobalProxy(tt.own, tt.global)
			if err != nil {
				t.Fatalf("inheritGlobalProxy: %v", err)
			}
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got.proxy.Type != tt.wantType {
				t.Errorf("type = %q, want %q", got.proxy.Type, tt.wantType)
			}
			// The proxy stacks read it from the proxy config for the TLS hop to an
			// https:// proxy, which NetworkConfig never reaches.
			if got.proxy.SkipTLSVerify != tt.wantSkip {
				t.Errorf("proxy.SkipTLSVerify = %v, want %v", got.proxy.SkipTLSVerify, tt.wantSkip)
			}
			// The provider stacks trust the global proxy's CA for the https:// proxy hop
			// and for TLS through a TLS-inspecting proxy.
			if gotCA := got.proxy.CACertPEM.GetValue(); gotCA != tt.wantCA {
				t.Errorf("proxy.CACertPEM = %q, want %q", gotCA, tt.wantCA)
			}
		})
	}
}

// TestInheritGlobalProxy_BareAddressKeepsTheProxy: the settings page accepts a bare
// host:port and takes its scheme from the type. Providers parse the URL to add credentials,
// and a bare address does not parse, so the inherited URL must carry the scheme or the
// provider client is left without a proxy and inference goes direct.
func TestInheritGlobalProxy_BareAddressKeepsTheProxy(t *testing.T) {
	cases := []struct {
		proxyType network.GlobalProxyType
		url       string
		want      string
	}{
		{network.GlobalProxyTypeSOCKS5, "10.0.0.9:1080", "socks5://10.0.0.9:1080"},
		{network.GlobalProxyTypeHTTP, "10.0.0.9:3128", "http://10.0.0.9:3128"},
		{network.GlobalProxyTypeSOCKS5, "proxy.corp:1080", "socks5://proxy.corp:1080"},
		// Stored before the scheme had to match the type (config.json did not check it). The
		// dialer follows the URL's scheme, so this proxy worked and must keep working.
		{network.GlobalProxyTypeHTTP, "socks5://10.0.0.9:1080", "socks5://10.0.0.9:1080"},
	}
	for _, tc := range cases {
		t.Run(string(tc.proxyType)+" "+tc.url, func(t *testing.T) {
			global := enabledGlobalProxy()
			global.Type = tc.proxyType
			global.URL = tc.url
			got, ok, err := inheritGlobalProxy(nil, global)
			if err != nil {
				t.Fatalf("inheritGlobalProxy: %v", err)
			}
			if !ok {
				t.Fatal("nothing inherited")
			}
			if got.proxy.URL.GetValue() != tc.want {
				t.Errorf("url = %q, want %q", got.proxy.URL.GetValue(), tc.want)
			}
			client := providerUtils.ConfigureProxy(&fasthttp.Client{}, got.proxy, nil)
			if client.Dial == nil {
				t.Errorf("the provider client got no proxy dialer for %q with credentials: inference would go direct", tc.url)
			}
		})
	}

	invalid := enabledGlobalProxy()
	invalid.Type = network.GlobalProxyTypeSOCKS5
	invalid.URL = "socks5://10.0.0.9:bad"
	if _, ok, err := inheritGlobalProxy(nil, invalid); ok || err == nil {
		t.Errorf("an unparseable global proxy url (which the API and config.json both refuse) must be an error, not inherited or dropped: ok=%v err=%v", ok, err)
	}
}

// TestGetConfigForProvider_InheritedSkipTLSVerifyDoesNotLeak pins where an inherited
// global skip_tls_verify goes: onto the inherited proxy config, which scopes it to TLS
// through the proxy, and never onto the provider's network config, where it would also
// switch off certificate checks for no_proxy hosts reached directly. The stored network
// config stays untouched either way.
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
	if config.ProxyConfig == nil || !config.ProxyConfig.SkipTLSVerify {
		t.Error("inherited skip_tls_verify must reach the provider through its proxy config")
	}
	if config.NetworkConfig.InsecureSkipVerify {
		t.Error("inherited skip_tls_verify must not become network_config.insecure_skip_verify: that would also skip verification for direct no_proxy hosts")
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

// TestGetConfigForProvider_ProxySourceMatrix pins which proxy config a provider hands to
// core for every combination of global proxy state, the provider's own proxy_config and
// the proxy env vars. The env vars never change the resolved config: they are read later
// by the provider's HTTP stacks (see TestProxyRoutingMatrix in core/providers/utils),
// so each case runs with and without them and must resolve the same way. Together the
// two matrices cover global x provider x env x stack x target end to end.
func TestGetConfigForProvider_ProxySourceMatrix(t *testing.T) {
	globals := []struct {
		name     string
		config   func() *configstoreTables.GlobalProxyConfig
		inherits bool
		wantType schemas.ProxyType
	}{
		{name: "absent", config: func() *configstoreTables.GlobalProxyConfig { return nil }},
		{name: "disabled", config: func() *configstoreTables.GlobalProxyConfig {
			g := enabledGlobalProxy()
			g.Enabled = false
			return g
		}},
		{name: "enabled-not-for-inference", config: func() *configstoreTables.GlobalProxyConfig {
			g := enabledGlobalProxy()
			g.EnableForInference = false
			return g
		}},
		{name: "enabled-no-url", config: func() *configstoreTables.GlobalProxyConfig {
			g := enabledGlobalProxy()
			g.URL = ""
			return g
		}},
		{name: "enabled-http", config: enabledGlobalProxy, inherits: true, wantType: schemas.HTTPProxy},
		{name: "enabled-socks5", config: func() *configstoreTables.GlobalProxyConfig {
			g := enabledGlobalProxy()
			g.Type = network.GlobalProxyTypeSOCKS5
			g.URL = "socks5://10.0.0.9:1080"
			return g
		}, inherits: true, wantType: schemas.Socks5Proxy},
		{name: "enabled-tcp", config: func() *configstoreTables.GlobalProxyConfig {
			g := enabledGlobalProxy()
			g.Type = network.GlobalProxyTypeTCP
			return g
		}},
	}
	owns := []struct {
		name   string
		config *schemas.ProxyConfig
		hasOwn bool
	}{
		{name: "unset", config: nil},
		{name: "none", config: &schemas.ProxyConfig{Type: schemas.NoProxy}},
		{name: "empty-type", config: &schemas.ProxyConfig{}},
		{name: "http", config: &schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: schemas.NewSecretVar("http://own:8080")}, hasOwn: true},
		{name: "socks5", config: &schemas.ProxyConfig{Type: schemas.Socks5Proxy, URL: schemas.NewSecretVar("socks5://own:1080")}, hasOwn: true},
		{name: "environment", config: &schemas.ProxyConfig{Type: schemas.EnvProxy}, hasOwn: true},
	}
	envs := map[string]map[string]string{
		"no-env":   {},
		"env-vars": {"HTTPS_PROXY": "http://10.0.0.2:3128", "HTTP_PROXY": "http://10.0.0.3:3128", "NO_PROXY": "other.example"},
	}

	for _, global := range globals {
		for _, own := range owns {
			for envName, vars := range envs {
				t.Run(global.name+"/"+own.name+"/"+envName, func(t *testing.T) {
					for _, name := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy"} {
						t.Setenv(name, "")
					}
					for name, value := range vars {
						t.Setenv(name, value)
					}
					store := &Config{
						Providers:   map[schemas.ModelProvider]configstore.ProviderConfig{schemas.Vertex: {ProxyConfig: own.config}},
						ProxyConfig: global.config(),
					}
					config, err := NewBaseAccount(store).GetConfigForProvider(schemas.Vertex)
					if err != nil {
						t.Fatalf("GetConfigForProvider: %v", err)
					}
					got := config.ProxyConfig

					switch {
					case own.hasOwn:
						if got != own.config {
							t.Errorf("proxy = %+v, want the provider's own proxy", got)
						}
					case global.inherits:
						if got == nil || got.Type != global.wantType || got.URL.GetValue() != store.ProxyConfig.URL {
							t.Fatalf("proxy = %+v, want the inherited global %s proxy", got, global.wantType)
						}
						if got.NoProxy != store.ProxyConfig.NoProxy {
							t.Errorf("no_proxy = %q, want the global list %q", got.NoProxy, store.ProxyConfig.NoProxy)
						}
					default:
						if got != own.config {
							t.Errorf("proxy = %+v, want the provider's config unchanged (%+v)", got, own.config)
						}
					}
				})
			}
		}
	}
}

// TestGetConfigForProvider_CarriesInjectedTools pins the field-by-field bridge from the
// stored provider config into core: a field missing here saves fine but never reaches
// the request path.
func TestGetConfigForProvider_CarriesInjectedTools(t *testing.T) {
	injected := []schemas.InjectedTool{
		{Name: "web_search", MCPClientName: "tavily", ToolName: "search"},
		{Name: "read_page", MCPClientName: "fetcher", ToolName: "fetch"},
	}
	store := &Config{
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI: {InjectedTools: injected},
		},
	}

	config, err := NewBaseAccount(store).GetConfigForProvider(schemas.OpenAI)
	if err != nil {
		t.Fatalf("GetConfigForProvider: %v", err)
	}
	if !reflect.DeepEqual(config.InjectedTools, injected) {
		t.Errorf("injected_tools must reach core; got %+v", config.InjectedTools)
	}
}

// TestGetConfigForProvider_CarriesPassthrough pins the bridge for the passthrough policy:
// without it the policy saves fine and core forwards every route.
func TestGetConfigForProvider_CarriesPassthrough(t *testing.T) {
	policy := &schemas.PassthroughPolicy{AllowedRoutes: []schemas.PassthroughRouteRule{{Methods: []string{"POST"}, Path: "/v1/chat/completions"}}}
	store := &Config{
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI:    {Passthrough: policy},
			schemas.Anthropic: {},
		},
	}

	config, err := NewBaseAccount(store).GetConfigForProvider(schemas.OpenAI)
	if err != nil {
		t.Fatalf("GetConfigForProvider: %v", err)
	}
	if config.Passthrough != policy {
		t.Errorf("passthrough must reach core; got %+v", config.Passthrough)
	}
	config, err = NewBaseAccount(store).GetConfigForProvider(schemas.Anthropic)
	if err != nil {
		t.Fatalf("GetConfigForProvider: %v", err)
	}
	if config.Passthrough != nil {
		t.Errorf("a provider without a policy must reach core without one; got %+v", config.Passthrough)
	}
}

// TestGetConfigForProvider_UnreadableGlobalProxyFailsClosed pins that a global proxy enabled
// for inference whose URL cannot be read stops the provider from being configured. Dropping
// it would send inference direct, around the proxy the admin turned on; a stored URL can
// predate the validator. The error names the URL with its password masked.
func TestGetConfigForProvider_UnreadableGlobalProxyFailsClosed(t *testing.T) {
	for _, raw := range []string{"http://proxy.example:bad", "http://svc:s3cret@proxy.example:bad", "http://:3128", "socks5://proxy.example:0"} {
		t.Run(raw, func(t *testing.T) {
			global := enabledGlobalProxy()
			global.URL = raw
			if strings.HasPrefix(raw, "socks5://") {
				global.Type = network.GlobalProxyTypeSOCKS5
			}
			store := &Config{
				Providers:   map[schemas.ModelProvider]configstore.ProviderConfig{schemas.Vertex: {}},
				ProxyConfig: global,
			}
			config, err := NewBaseAccount(store).GetConfigForProvider(schemas.Vertex)
			if err == nil {
				t.Fatalf("an unreadable global proxy was dropped, so inference would go direct (proxy_config %+v)", config.ProxyConfig)
			}
			if strings.Contains(err.Error(), "s3cret") {
				t.Errorf("the error carries the proxy password: %v", err)
			}
		})
	}
}
