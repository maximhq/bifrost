package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
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
	"github.com/maximhq/bifrost/framework/encrypt"
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
	// The datasheet server below is loopback-bound; the accessibility check refuses loopback
	// by default, so route its dial through a plain dialer for this test only.
	prevDial := checkURLAccessibilityDialContext
	checkURLAccessibilityDialContext = (&net.Dialer{}).DialContext
	t.Cleanup(func() { checkURLAccessibilityDialContext = prevDial })
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
	h := &ConfigHandler{store: cfg, configManager: stubConfigManager{}}

	// A loopback datasheet server passes the accessibility check; file:// URLs
	// are refused over the API (TestUpdateConfig_RejectsFileURLsOverAPI).
	customURL := newDatasheetServer(t).URL

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
	// The datasheet server below is loopback-bound; the accessibility check refuses loopback
	// by default, so route its dial through a plain dialer for this test only.
	prevDial := checkURLAccessibilityDialContext
	checkURLAccessibilityDialContext = (&net.Dialer{}).DialContext
	t.Cleanup(func() { checkURLAccessibilityDialContext = prevDial })
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	cfg := newTestOAuth2Config(failingFrameworkConfigStore{store}, configtables.MCPServerAuthModeHeaders, false)
	before := cfg.FrameworkConfig
	h := &ConfigHandler{store: cfg, configManager: stubConfigManager{}}

	ctx := putConfigCtx(`{"client_config":{"log_retention_days":7},"framework_config":{"pricing_url":"` + newDatasheetServer(t).URL + `"}}`)
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

// A per-server cap larger than the total it must fit inside can never be satisfied, so it is
// rejected rather than stored and silently clamped later.
func TestValidateMCPInstructionCaps(t *testing.T) {
	assert.NoError(t, validateMCPInstructionCaps(0, 0), "0/0 means use the built-in defaults")
	assert.NoError(t, validateMCPInstructionCaps(4096, 16384))
	assert.NoError(t, validateMCPInstructionCaps(0, 2048), "a total alone is fine")
	assert.NoError(t, validateMCPInstructionCaps(2048, 0), "a per-client cap alone is fine")

	assert.Error(t, validateMCPInstructionCaps(-1, 0))
	assert.Error(t, validateMCPInstructionCaps(0, -1))
	assert.Error(t, validateMCPInstructionCaps(1<<21, 0), "a cap in the megabytes is a typo")
	assert.Error(t, validateMCPInstructionCaps(0, 1<<21))
	err := validateMCPInstructionCaps(20000, 16384)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must not exceed")
}

// newDatasheetServer serves an empty JSON datasheet on loopback.
func newDatasheetServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestCheckURLAccessibility_RejectsFileURLs: file:// datasheet and catalog
// URLs are an operator feature of config.json (air-gapped deployments) and are
// refused when they arrive over the HTTP API, whatever the path points at.
func TestCheckURLAccessibility_RejectsFileURLs(t *testing.T) {
	for _, raw := range []string{"file:///etc/hostname", "file://./pricing.json", "file:pricing.json", "FILE:///etc/hostname"} {
		err := checkURLAccessibility(raw)
		require.Error(t, err, raw)
		assert.Contains(t, err.Error(), "config.json", raw)
	}
}

// TestCheckURLAccessibility_RefusesRedirectToLinkLocal: the entry host is a
// reachable loopback server whose 302 points at the cloud metadata address.
// The hop must be refused, and the caller-facing error must not name the
// target or carry the transport detail.
func TestCheckURLAccessibility_RefusesRedirectToLinkLocal(t *testing.T) {
	SetLogger(&mockLogger{})
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer redirect.Close()

	err := checkURLAccessibility(redirect.URL)
	require.Error(t, err)
	assert.Equal(t, "url is not reachable", err.Error())
}

// TestCheckURLAccessibility_DoesNotReflectTransportErrors: a non-HTTP service
// greets with a banner that net/http folds into its parse error. That detail
// is for the debug log, never for the response body.
func TestCheckURLAccessibility_DoesNotReflectTransportErrors(t *testing.T) {
	SetLogger(&mockLogger{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte("SSH-2.0-OpenSSH_9.6 Ubuntu-3ubuntu0.1\r\n"))
			conn.Close()
		}
	}()

	err = checkURLAccessibility("http://" + ln.Addr().String() + "/")
	require.Error(t, err)
	assert.Equal(t, "url is not reachable", err.Error())
	assert.NotContains(t, strings.ToLower(err.Error()), "openssh")

	// A refused port reads the same as a banner: no open/closed oracle.
	ln.Close()
	err = checkURLAccessibility("http://" + ln.Addr().String() + "/")
	require.Error(t, err)
	assert.Equal(t, "url is not reachable", err.Error())
}

// TestUpdateConfig_RejectsOAuthModeWithoutIssuerURL pins the HTTP side of the issuer_url
// rule the OpenAPI description states: enabling issuance over PUT /api/config without a
// pinned issuer is refused with 400 and names the field. The startup side (a config.json
// doing the same fails validation at boot) is pinned by
// TestLoadConfig_OAuthDiscoveryWithoutIssuerURLFailsBoot in lib.
func TestUpdateConfig_RejectsOAuthModeWithoutIssuerURL(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
	// The shared fixture pins an issuer so the issuance tests clear this rule; this test is
	// about the rule itself, so start from a headers-mode config with no issuer stored.
	cfg.ClientConfig.OAuth2ServerConfig = nil
	h := &ConfigHandler{store: cfg, configManager: stubConfigManager{}}

	for _, mode := range []string{"both", "oauth"} {
		t.Run(mode, func(t *testing.T) {
			ctx := putConfigCtx(`{"client_config":{"log_retention_days":7,"mcp_server_auth_mode":"` + mode + `"}}`)
			h.updateConfig(ctx)
			require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			assert.Contains(t, string(ctx.Response.Body()), "issuer_url")
		})
	}
}

// TestUpdateConfig_RejectsFileURLsOverAPI pins the API-side rule for all three
// catalog URLs: PUT /api/config answers 400 and persists nothing.
func TestUpdateConfig_RejectsFileURLsOverAPI(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
	h := &ConfigHandler{store: cfg, configManager: stubConfigManager{}}

	for _, field := range []string{"pricing_url", "model_parameters_url", "mcp_library_url"} {
		t.Run(field, func(t *testing.T) {
			ctx := putConfigCtx(`{"client_config":{"log_retention_days":7},"framework_config":{"` + field + `":"file:///etc/hostname"}}`)
			h.updateConfig(ctx)
			require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			assert.Contains(t, string(ctx.Response.Body()), "config.json")
		})
	}
	persisted, err := store.GetFrameworkConfig(bgCtx())
	require.NoError(t, err)
	if persisted != nil {
		for _, got := range []*string{persisted.PricingURL, persisted.ModelParametersURL, persisted.MCPLibraryURL} {
			if got != nil {
				assert.NotContains(t, *got, "file://")
			}
		}
	}
}

// TestUpdateProxyConfig_RejectsLinkLocalURL pins that the global proxy URL is held to the
// same destination rule as a provider base URL. Every proxied request carries its provider
// credentials to this host, so an address that can never be a legitimate egress proxy
// (link-local such as the cloud metadata endpoint, or unspecified) is refused even for a
// genuinely authenticated admin. Private and loopback hosts stay allowed: a self-hosted
// proxy on the local network is the normal setup.
func TestUpdateProxyConfig_RejectsLinkLocalURL(t *testing.T) {
	SetLogger(&mockLogger{})
	cases := []struct {
		name       string
		url        string
		wantStatus int
	}{
		{name: "link-local metadata endpoint", url: "http://169.254.169.254:80", wantStatus: fasthttp.StatusBadRequest},
		{name: "unspecified address", url: "http://0.0.0.0:3128", wantStatus: fasthttp.StatusBadRequest},
		{name: "private network proxy", url: "http://10.0.0.5:3128", wantStatus: fasthttp.StatusOK},
		{name: "loopback proxy", url: "http://127.0.0.1:3128", wantStatus: fasthttp.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newRealOAuth2Store(t)
			cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
			h := &ConfigHandler{store: cfg, configManager: stubConfigManager{}}

			ctx := newTestRequestCtx(`{"enabled":true,"type":"http","url":"` + tc.url + `","timeout":0}`)
			ctx.Request.Header.SetMethod(fasthttp.MethodPut)

			h.updateProxyConfig(ctx)

			require.Equal(t, tc.wantStatus, ctx.Response.StatusCode(), "body=%s", ctx.Response.Body())
			stored, err := store.GetProxyConfig(context.Background())
			if tc.wantStatus == fasthttp.StatusOK {
				require.NoError(t, err)
				assert.Equal(t, tc.url, stored.URL)
				return
			}
			if err != nil {
				require.ErrorIs(t, err, configstore.ErrNotFound)
				return
			}
			if stored != nil {
				assert.Empty(t, stored.URL, "a rejected proxy URL must not be persisted")
			}
		})
	}
}

// authConfigTestManager persists auth config to the real store and matches setup tokens
// against one fixed value, so updateConfig's credential gates can be exercised end to end.
type authConfigTestManager struct {
	stubConfigManager
	store      configstore.ConfigStore
	setupToken string
}

func (m authConfigTestManager) UpdateAuthConfig(ctx context.Context, c *configstore.AuthConfig) error {
	return m.store.UpdateAuthConfig(ctx, c)
}

func (m authConfigTestManager) ValidateSetupToken(token string) bool {
	return m.setupToken != "" && token == m.setupToken
}

func (m authConfigTestManager) ValidateConfiguredSetupToken(token string) bool {
	return m.setupToken != "" && token == m.setupToken
}

// TestUpdateConfig_StoredCredentialsRequireProofWhenAuthBypassed pins that an admin account
// which exists but has dashboard auth switched off cannot be taken over through the open
// management API: a caller admitted without a credential check may switch auth back on, or
// replace the stored credentials, only by presenting the current admin password
// (current_password) or the operator-configured setup token. A genuinely authenticated
// session needs no extra proof, and a disabled-state save that resubmits nothing keeps the
// stored credentials as before.
func TestUpdateConfig_StoredCredentialsRequireProofWhenAuthBypassed(t *testing.T) {
	SetLogger(&mockLogger{})
	const (
		storedUser = "admin"
		storedPass = "Current-Pass-1234!"
		newPass    = "Replacement-Pass-5678!"
		setupToken = "operator-setup-token"
	)
	body := func(enabled, extra string) string {
		return `{"client_config":{"log_retention_days":7},"auth_config":{"is_enabled":` + enabled +
			`,"admin_username":"` + storedUser + `","admin_password":"` + newPass + `"` + extra + `}}`
	}
	cases := []struct {
		name        string
		body        string
		bypassed    bool
		wantStatus  int
		wantEnabled bool
		wantPass    string
	}{
		{name: "anonymous re-enable with new password", body: body("true", ""), bypassed: true, wantStatus: fasthttp.StatusForbidden, wantPass: storedPass},
		{name: "anonymous re-enable with wrong current_password", body: body("true", `,"current_password":"not-it"`), bypassed: true, wantStatus: fasthttp.StatusForbidden, wantPass: storedPass},
		{name: "anonymous re-enable with wrong setup token", body: body("true", `,"setup_token":"wrong"`), bypassed: true, wantStatus: fasthttp.StatusForbidden, wantPass: storedPass},
		{name: "anonymous re-enable with correct current_password", body: body("true", `,"current_password":"`+storedPass+`"`), bypassed: true, wantStatus: fasthttp.StatusOK, wantEnabled: true, wantPass: newPass},
		{name: "anonymous re-enable with valid setup token", body: body("true", `,"setup_token":"`+setupToken+`"`), bypassed: true, wantStatus: fasthttp.StatusOK, wantEnabled: true, wantPass: newPass},
		{name: "authenticated re-enable needs no extra proof", body: body("true", ""), bypassed: false, wantStatus: fasthttp.StatusOK, wantEnabled: true, wantPass: newPass},
		{name: "anonymous credential change while auth stays disabled", body: body("false", ""), bypassed: true, wantStatus: fasthttp.StatusForbidden, wantPass: storedPass},
		{name: "anonymous credential change while disabled with current_password", body: body("false", `,"current_password":"`+storedPass+`"`), bypassed: true, wantStatus: fasthttp.StatusOK, wantPass: newPass},
		{name: "anonymous disabled save without credentials keeps the stored ones", body: `{"client_config":{"log_retention_days":7},"auth_config":{"is_enabled":false}}`, bypassed: true, wantStatus: fasthttp.StatusOK, wantPass: storedPass},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newRealOAuth2Store(t)
			hash, err := encrypt.Hash(storedPass)
			require.NoError(t, err)
			require.NoError(t, store.UpdateAuthConfig(context.Background(), &configstore.AuthConfig{
				AdminUserName: schemas.NewSecretVar(storedUser),
				AdminPassword: schemas.NewSecretVar(hash),
				IsEnabled:     false,
			}))
			cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
			h := &ConfigHandler{store: cfg, configManager: authConfigTestManager{store: store, setupToken: setupToken}}

			ctx := putConfigCtx(tc.body)
			if tc.bypassed {
				ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)
			}
			h.updateConfig(ctx)

			require.Equal(t, tc.wantStatus, ctx.Response.StatusCode(), "body=%s", ctx.Response.Body())
			stored, err := store.GetAuthConfig(context.Background())
			require.NoError(t, err)
			require.NotNil(t, stored)
			assert.Equal(t, tc.wantEnabled, stored.IsEnabled)
			assert.Equal(t, storedUser, stored.AdminUserName.GetValue())
			ok, _ := encrypt.CompareHash(stored.AdminPassword.GetValue(), tc.wantPass)
			assert.True(t, ok, "stored password must verify against %q", tc.wantPass)
		})
	}
}

// TestUpdateConfig_FirstAdminStillRequiresSetupToken pins that the first-admin gate is
// unchanged by the stored-credential proof: with no admin account, only the setup token
// creates one. A current_password cannot stand in for it - there is nothing to compare it to.
func TestUpdateConfig_FirstAdminStillRequiresSetupToken(t *testing.T) {
	SetLogger(&mockLogger{})
	const setupToken = "operator-setup-token"
	body := func(extra string) string {
		return `{"client_config":{"log_retention_days":7},"auth_config":{"is_enabled":true,"admin_username":"admin","admin_password":"First-Admin-Pass-1!"` + extra + `}}`
	}
	cases := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "no setup token", body: body(""), wantStatus: fasthttp.StatusForbidden},
		{name: "wrong setup token", body: body(`,"setup_token":"wrong"`), wantStatus: fasthttp.StatusForbidden},
		{name: "current_password instead of setup token", body: body(`,"current_password":"First-Admin-Pass-1!"`), wantStatus: fasthttp.StatusForbidden},
		{name: "valid setup token", body: body(`,"setup_token":"` + setupToken + `"`), wantStatus: fasthttp.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newRealOAuth2Store(t)
			cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
			h := &ConfigHandler{store: cfg, configManager: authConfigTestManager{store: store, setupToken: setupToken}}

			ctx := putConfigCtx(tc.body)
			ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)
			h.updateConfig(ctx)

			require.Equal(t, tc.wantStatus, ctx.Response.StatusCode(), "body=%s", ctx.Response.Body())
			stored, err := store.GetAuthConfig(context.Background())
			if tc.wantStatus == fasthttp.StatusOK {
				require.NoError(t, err)
				require.NotNil(t, stored)
				assert.True(t, stored.IsEnabled)
				return
			}
			if err != nil {
				require.ErrorIs(t, err, configstore.ErrNotFound)
				return
			}
			assert.Nil(t, stored, "no admin account may be created without the setup token")
		})
	}
}
