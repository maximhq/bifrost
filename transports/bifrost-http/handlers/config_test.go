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

	"github.com/maximhq/bifrost/core/schemas"
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

// TestUpdateProxyConfig_InterceptionGuardWhenAuthBypassed pins that the fail-open bypass
// cannot point the global proxy at a caller-chosen host or turn off its TLS verification:
// either lets a third party read provider credentials for every proxied request. Saving
// other fields against the stored proxy URL must still go through.
func TestUpdateProxyConfig_InterceptionGuardWhenAuthBypassed(t *testing.T) {
	SetLogger(&mockLogger{})
	const storedURL = "http://10.0.0.5:3128"
	cases := []struct {
		name    string
		body    string
		want403 bool
	}{
		{name: "same url, timeout edit", body: `{"enabled":true,"type":"http","url":"` + storedURL + `","timeout":30,"enable_for_inference":true}`, want403: false},
		{name: "url changed", body: `{"enabled":true,"type":"http","url":"http://evil.example.com:8080","timeout":10,"enable_for_inference":true}`, want403: true},
		{name: "skip_tls_verify turned on", body: `{"enabled":true,"type":"http","url":"` + storedURL + `","timeout":10,"skip_tls_verify":true,"enable_for_inference":true}`, want403: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newRealOAuth2Store(t)
			require.NoError(t, store.UpdateProxyConfig(context.Background(), &configtables.GlobalProxyConfig{
				Enabled: true, Type: "http", URL: storedURL, Timeout: 10, EnableForInference: true,
			}))
			cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
			h := &ConfigHandler{store: cfg, configManager: stubConfigManager{}}

			ctx := newTestRequestCtx(tc.body)
			ctx.Request.Header.SetMethod(fasthttp.MethodPut)
			ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)

			h.updateProxyConfig(ctx)

			got403 := ctx.Response.StatusCode() == fasthttp.StatusForbidden
			require.Equal(t, tc.want403, got403, "status %d; body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
			stored, err := store.GetProxyConfig(context.Background())
			require.NoError(t, err)
			if tc.want403 {
				assert.Equal(t, storedURL, stored.URL)
				assert.False(t, stored.SkipTLSVerify)
				assert.Equal(t, 10, stored.Timeout)
			} else {
				require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), "body=%s", ctx.Response.Body())
				assert.Equal(t, 30, stored.Timeout)
			}
		})
	}
}
