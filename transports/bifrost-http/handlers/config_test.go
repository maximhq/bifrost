package handlers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"

	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

func TestGetPasswordPolicyFailures(t *testing.T) {
	tests := []struct {
		name     string
		password string
		want     []string
	}{
		{
			name:     "valid password",
			password: "StrongPass1!",
			want:     []string{},
		},
		{
			name:     "missing all requirements",
			password: "",
			want: []string{
				"at least 12 characters",
				"one uppercase letter",
				"one lowercase letter",
				"one number",
				"one special character",
			},
		},
		{
			name:     "missing character classes",
			password: "weakpassword",
			want: []string{
				"one uppercase letter",
				"one number",
				"one special character",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getPasswordPolicyFailures(tt.password)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("getPasswordPolicyFailures() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestUpdateConfig_EmptyDatasheetURLsResetToDefaults pins the regression where
// PUT /api/config rejected an empty pricing_url with "URL cannot be empty".
// The custom pricing page sends "" when the user clears the field; an empty
// pricing_url or model_parameters_url means "use the built-in datasheet URL".
func TestUpdateConfig_EmptyDatasheetURLsResetToDefaults(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
	h := &ConfigHandler{store: cfg, configManager: stubConfigManager{}}

	// A file:// URL passes the accessibility check without network access.
	custom := filepath.Join(t.TempDir(), "datasheet.json")
	require.NoError(t, os.WriteFile(custom, []byte("{}"), 0o600))
	customURL := "file://" + custom

	save := func(t *testing.T, pricingURL, modelParamsURL string) {
		t.Helper()
		ctx := putConfigCtx(`{"client_config":{"log_retention_days":7},"framework_config":{"pricing_url":"` + pricingURL + `","model_parameters_url":"` + modelParamsURL + `"}}`)
		h.updateConfig(ctx)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}

	save(t, customURL, customURL)
	persisted, err := store.GetFrameworkConfig(bgCtx())
	require.NoError(t, err)
	require.NotNil(t, persisted)
	assert.Equal(t, customURL, *persisted.PricingURL)
	assert.Equal(t, customURL, *persisted.ModelParametersURL)

	save(t, "", "")
	persisted, err = store.GetFrameworkConfig(bgCtx())
	require.NoError(t, err)
	require.NotNil(t, persisted)
	assert.Equal(t, modelcatalog.DefaultPricingURL, *persisted.PricingURL, "empty pricing_url must reset to the default")
	assert.Equal(t, modelcatalog.DefaultModelParametersURL, *persisted.ModelParametersURL, "empty model_parameters_url must reset to the default")
	assert.Equal(t, modelcatalog.DefaultPricingURL, *cfg.FrameworkConfig.Pricing.PricingURL)
	assert.Equal(t, modelcatalog.DefaultModelParametersURL, *cfg.FrameworkConfig.Pricing.ModelParametersURL)
}

// failingFrameworkConfigStore makes the framework config write fail while every
// other store call goes through to the real store.
type failingFrameworkConfigStore struct {
	configstore.ConfigStore
}

func (failingFrameworkConfigStore) UpdateFrameworkConfig(context.Context, *configtables.TableFrameworkConfig) error {
	return errors.New("simulated store failure")
}

// TestUpdateConfig_FrameworkConfigStoreFailureLeavesRuntimeUnchanged pins the
// ordering in updateConfig: the framework config is persisted before it is
// published to runtime, so a failed store write does not leave the in-memory
// config pointing at URLs the database never saved.
func TestUpdateConfig_FrameworkConfigStoreFailureLeavesRuntimeUnchanged(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	cfg := newTestOAuth2Config(failingFrameworkConfigStore{store}, configtables.MCPServerAuthModeHeaders, false)
	before := cfg.FrameworkConfig
	h := &ConfigHandler{store: cfg, configManager: stubConfigManager{}}

	custom := filepath.Join(t.TempDir(), "datasheet.json")
	require.NoError(t, os.WriteFile(custom, []byte("{}"), 0o600))

	ctx := putConfigCtx(`{"client_config":{"log_retention_days":7},"framework_config":{"pricing_url":"file://` + custom + `"}}`)
	h.updateConfig(ctx)
	require.Equal(t, fasthttp.StatusInternalServerError, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	assert.Same(t, before, cfg.FrameworkConfig, "runtime framework config must not change when the store write fails")
}

// proxyReloadRecorder records the global proxy config updateProxyConfig hands to the
// runtime.
type proxyReloadRecorder struct {
	stubConfigManager
	reloaded *configtables.GlobalProxyConfig
}

func (r *proxyReloadRecorder) ReloadProxyConfig(_ context.Context, cfg *configtables.GlobalProxyConfig) error {
	r.reloaded = cfg
	return nil
}

// TestUpdateProxyConfig_ProxyTypes pins which global proxy types PUT /api/proxy-config
// accepts. socks5 is accepted, stored and applied: the HTTP client factory and the
// provider stacks dial SOCKS5 proxies (with RFC 1929 credentials), so rejecting it as
// "not yet supported" only hid a working feature. tcp has no dialer and stays refused.
func TestUpdateProxyConfig_ProxyTypes(t *testing.T) {
	SetLogger(&mockLogger{})
	for _, tc := range []struct {
		name     string
		body     string
		wantCode int
		wantType string
		wantURL  string
	}{
		{"http", `{"enabled":true,"type":"http","url":"http://proxy.example:3128","enable_for_inference":true}`, fasthttp.StatusOK, "http", "http://proxy.example:3128"},
		{"socks5", `{"enabled":true,"type":"socks5","url":"socks5://proxy.example:1080","username":"svc","password":"s3cret","enable_for_inference":true,"enable_for_api":true}`, fasthttp.StatusOK, "socks5", "socks5://proxy.example:1080"},
		{"socks5 bare host:port", `{"enabled":true,"type":"socks5","url":"proxy.example:1080"}`, fasthttp.StatusOK, "socks5", "proxy.example:1080"},
		{"socks5 upper-case scheme and spaces, as the form accepts", `{"enabled":true,"type":"socks5","url":"  SOCKS5://proxy.example:1080  "}`, fasthttp.StatusOK, "socks5", "SOCKS5://proxy.example:1080"},
		{"socks5 without url", `{"enabled":true,"type":"socks5","url":""}`, fasthttp.StatusBadRequest, "", ""},
		{"socks5 with an http url", `{"enabled":true,"type":"socks5","url":"http://proxy.example:1080"}`, fasthttp.StatusBadRequest, "", ""},
		// A URL the dialer cannot parse is refused: the runtime treats an unparsable
		// proxy URL as no proxy, so saving one would send traffic direct.
		{"socks5 with a non-numeric port", `{"enabled":true,"type":"socks5","url":"socks5://proxy.example:bad"}`, fasthttp.StatusBadRequest, "", ""},
		{"http with a non-numeric port", `{"enabled":true,"type":"http","url":"http://proxy.example:bad"}`, fasthttp.StatusBadRequest, "", ""},
		{"http without a host", `{"enabled":true,"type":"http","url":"http://:3128"}`, fasthttp.StatusBadRequest, "", ""},
		{"tcp", `{"enabled":true,"type":"tcp","url":"tcp://proxy.example:9000"}`, fasthttp.StatusBadRequest, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newRealOAuth2Store(t)
			cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
			manager := &proxyReloadRecorder{}
			h := &ConfigHandler{store: cfg, configManager: manager}

			ctx := putConfigCtx(tc.body)
			h.updateProxyConfig(ctx)
			require.Equal(t, tc.wantCode, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			if tc.wantCode != fasthttp.StatusOK {
				assert.Nil(t, manager.reloaded, "a refused proxy config must not reach the runtime")
				return
			}
			stored, err := store.GetProxyConfig(bgCtx())
			require.NoError(t, err)
			require.NotNil(t, manager.reloaded)
			assert.Equal(t, tc.wantType, string(stored.Type), "the proxy type must be persisted")
			assert.Equal(t, tc.wantType, string(manager.reloaded.Type), "the proxy type must reach the runtime")
			assert.Equal(t, tc.wantURL, stored.URL, "the proxy URL must be persisted trimmed")
		})
	}
}
