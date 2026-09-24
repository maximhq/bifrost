package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

type inferenceSetupAccount struct {
	wsSpanTestAccount
	url string
}

func (a inferenceSetupAccount) GetConfigForProvider(p schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	c, err := a.wsSpanTestAccount.GetConfigForProvider(p)
	if err == nil {
		c.NetworkConfig.BaseURL = a.url
		c.NetworkConfig.AllowPrivateNetwork = true
	}
	return c, err
}

func TestUpdateConfig_InferenceAuthBlocksUpstream(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
	h := &ConfigHandler{store: cfg, configManager: setupConfigManager{store: store, validToken: true}}
	setup := putConfigCtx(`{"client_config":{"log_retention_days":7},"auth_config":{"is_enabled":true,"admin_username":"admin","admin_password":"StrongPassword1!"}}`)
	h.updateConfig(setup)
	require.Equal(t, 200, setup.Response.StatusCode(), string(setup.Response.Body()))

	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/chat/completions":
			fmt.Fprint(w, `{"id":"chat-test","object":"chat.completion","model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
		case "/files":
			fmt.Fprint(w, `{"object":"list","data":[]}`)
		default:
			fmt.Fprint(w, `{"id":"resp_test","object":"response","status":"completed","model":"gpt-4o-mini","output":[]}`)
		}
	}))
	defer upstream.Close()
	log := bifrost.NewDefaultLogger(schemas.LogLevelError)
	plugin, err := governance.Init(t.Context(), &governance.Config{IsVkMandatory: &cfg.ClientConfig.EnforceAuthOnInference}, log, nil,
		&configstore.GovernanceConfig{VirtualKeys: []configtables.TableVirtualKey{{
			ID: "setup-vk", Name: "setup-vk", Value: *schemas.NewSecretVar("sk-bf-setup"), IsActive: new(true),
			ProviderConfigs: []configtables.TableVirtualKeyProviderConfig{{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}, AllowAllKeys: true}},
		}}}, nil, nil, nil)
	require.NoError(t, err)
	client, err := bifrost.Init(t.Context(), schemas.BifrostConfig{
		Account: inferenceSetupAccount{url: upstream.URL}, Logger: log, LLMPlugins: []schemas.LLMPlugin{plugin},
	})
	require.NoError(t, err)
	defer client.Shutdown()
	for _, operation := range []string{"chat", "files", "responses"} {
		for _, credential := range []string{"", "sk-bf-setup"} {
			t.Run(operation+"/"+credential, func(t *testing.T) {
				ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
				if credential != "" {
					ctx.SetValue(schemas.BifrostContextKeyVirtualKey, credential)
				}
				lib.SettleIdentity(ctx)
				before := calls.Load()
				var failure *schemas.BifrostError
				switch operation {
				case "chat":
					_, failure = client.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o-mini", Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("hi")}}}})
				case "files":
					_, failure = client.FileListRequest(ctx, &schemas.BifrostFileListRequest{Provider: schemas.OpenAI})
				case "responses":
					_, failure = client.ResponsesRetrieveRequest(ctx, &schemas.BifrostResponsesRetrieveRequest{Provider: schemas.OpenAI, ResponseID: "resp_test"})
				}
				if credential == "" {
					require.NotNil(t, failure)
					require.NotNil(t, failure.StatusCode)
					assert.Equal(t, 401, *failure.StatusCode)
					assert.Equal(t, "virtual_key_required", *failure.Type)
					assert.Equal(t, before, calls.Load(), "anonymous request must not reach upstream")
				} else {
					require.Nil(t, failure, "%+v", failure)
					assert.Equal(t, before+1, calls.Load())
				}
			})
		}
	}
}

type setupConfigManager struct {
	stubConfigManager
	store      configstore.ConfigStore
	validToken bool
}

type inferenceSetupFailureStore struct {
	configstore.ConfigStore
	fail string
}

func (s inferenceSetupFailureStore) GetAuthConfig(ctx context.Context) (*configstore.AuthConfig, error) {
	if s.fail == "read" {
		return nil, errors.New("auth lookup failed")
	}
	return s.ConfigStore.GetAuthConfig(ctx)
}
func (s inferenceSetupFailureStore) UpdateClientConfig(ctx context.Context, c *configstore.ClientConfig) error {
	if s.fail == "write" {
		return errors.New("client persistence failed")
	}
	return s.ConfigStore.UpdateClientConfig(ctx, c)
}

func TestUpdateConfig_InferenceAuthStoreFailures(t *testing.T) {
	SetLogger(&mockLogger{})
	for _, failure := range []string{"read", "write"} {
		t.Run(failure, func(t *testing.T) {
			store := newRealOAuth2Store(t)
			cfg := newTestOAuth2Config(inferenceSetupFailureStore{store, failure}, configtables.MCPServerAuthModeHeaders, false)
			require.NoError(t, store.UpdateClientConfig(bgCtx(), cfg.ClientConfig))
			h := &ConfigHandler{store: cfg, configManager: setupConfigManager{store: store, validToken: true}}
			ctx := putConfigCtx(`{"client_config":{"log_retention_days":7},"auth_config":{"is_enabled":true,"admin_username":"admin","admin_password":"StrongPassword1!"}}`)
			h.updateConfig(ctx)
			require.Equal(t, 500, ctx.Response.StatusCode())
			persisted, err := store.GetClientConfig(bgCtx())
			require.NoError(t, err)
			assert.False(t, persisted.EnforceAuthOnInference)
			assert.False(t, cfg.ClientConfig.EnforceAuthOnInference)
			auth, err := store.GetAuthConfig(bgCtx())
			require.NoError(t, err)
			assert.Nil(t, auth)
		})
	}
}

func (m setupConfigManager) ValidateSetupToken(string) bool { return m.validToken }
func (m setupConfigManager) UpdateAuthConfig(ctx context.Context, auth *configstore.AuthConfig) error {
	return m.store.UpdateAuthConfig(ctx, auth)
}

func TestUpdateConfig_InferenceAuthSetup(t *testing.T) {
	SetLogger(&mockLogger{})
	for _, tt := range []struct {
		name, field, password               string
		existing, initial, validToken, want bool
		status                              int
	}{
		{"first omitted", "", "StrongPassword1!", false, false, true, true, 200},
		{"first opt out", `,"enforce_auth_on_inference":false`, "StrongPassword1!", false, false, true, false, 200},
		{"first explicit on", `,"enforce_auth_on_inference":true`, "StrongPassword1!", false, false, true, true, 200},
		{"existing on omitted", "", "StrongPassword1!", true, true, true, true, 200},
		{"existing off omitted", "", "StrongPassword1!", true, false, true, false, 200},
		{"invalid setup token", "", "StrongPassword1!", false, false, false, false, 403},
		{"invalid setup password", "", "weak", false, false, true, false, 400},
		{"unhashable setup password", "", strings.Repeat("StrongPassword1!", 10), false, false, true, false, 400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newRealOAuth2Store(t)
			cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
			cfg.ClientConfig.EnforceAuthOnInference = tt.initial
			require.NoError(t, store.UpdateClientConfig(bgCtx(), cfg.ClientConfig))
			if tt.existing {
				require.NoError(t, store.UpdateAuthConfig(bgCtx(), &configstore.AuthConfig{
					AdminUserName: schemas.NewSecretVar("admin"), AdminPassword: schemas.NewSecretVar("stored"), IsEnabled: true,
				}))
			}
			h := &ConfigHandler{store: cfg, configManager: setupConfigManager{store: store, validToken: tt.validToken}}
			ctx := putConfigCtx(fmt.Sprintf(`{"client_config":{"log_retention_days":7%s},"auth_config":{"is_enabled":true,"admin_username":"admin","admin_password":%q}}`, tt.field, tt.password))
			h.updateConfig(ctx)
			require.Equal(t, tt.status, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			persisted, err := store.GetClientConfig(bgCtx())
			require.NoError(t, err)
			assert.Equal(t, tt.want, persisted.EnforceAuthOnInference)
			assert.Equal(t, tt.want, cfg.ClientConfig.EnforceAuthOnInference)
			if tt.status == 200 {
				// An unrelated partial save must preserve the just-selected setting.
				save := putConfigCtx(`{"client_config":{"log_retention_days":8}}`)
				h.updateConfig(save)
				require.Equal(t, 200, save.Response.StatusCode(), string(save.Response.Body()))
				persisted, err = store.GetClientConfig(bgCtx())
				require.NoError(t, err)
				assert.Equal(t, tt.want, persisted.EnforceAuthOnInference)
				status := putConfigCtx("")
				(&SessionHandler{configStore: store}).isAuthEnabled(status)
				var reported struct {
					Inference *bool `json:"inference_auth_enforced"`
				}
				require.NoError(t, json.Unmarshal(status.Response.Body(), &reported))
				require.NotNil(t, reported.Inference)
				assert.Equal(t, tt.want, *reported.Inference)
			}
			if tt.status != 200 {
				auth, err := store.GetAuthConfig(bgCtx())
				require.NoError(t, err)
				assert.Nil(t, auth)
			}
		})
	}
}

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
