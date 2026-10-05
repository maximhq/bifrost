package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
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

// TestUpdateConfig_FirstAdminAcceptsSetupTokenHeader pins that a first-admin PUT the OSS
// setup-lock gate already authenticated (X-Bifrost-Setup-Token) does not have to repeat the
// token in auth_config.setup_token, while an unauthenticated one without it is still refused.
func TestUpdateConfig_FirstAdminAcceptsSetupTokenHeader(t *testing.T) {
	SetLogger(&mockLogger{})
	for _, tt := range []struct {
		name        string
		setupAuthed bool
		status      int
	}{
		{name: "header authenticated", setupAuthed: true, status: 200},
		{name: "no token anywhere", setupAuthed: false, status: 403},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newRealOAuth2Store(t)
			cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
			require.NoError(t, store.UpdateClientConfig(bgCtx(), cfg.ClientConfig))
			h := &ConfigHandler{store: cfg, configManager: setupConfigManager{store: store, validToken: false}}
			ctx := putConfigCtx(`{"client_config":{"log_retention_days":7},"auth_config":{"is_enabled":true,"admin_username":"admin","admin_password":"StrongPassword1!"}}`)
			if tt.setupAuthed {
				ctx.SetUserValue(schemas.BifrostContextKeySetupTokenAuthenticated, true)
			}
			h.updateConfig(ctx)
			require.Equal(t, tt.status, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			auth, err := store.GetAuthConfig(bgCtx())
			require.NoError(t, err)
			if tt.status == 200 {
				require.NotNil(t, auth)
				assert.True(t, auth.IsEnabled)
			} else {
				assert.Nil(t, auth)
			}
		})
	}
}

// TestIsAuthEnabled_ReportsSetupLockState pins the fields the dashboard uses to choose the
// setup screen: setup_required follows the gate's lock state, setup_token_configured whether
// a token exists, and both stay false when the gate is not installed (enterprise).
func TestIsAuthEnabled_ReportsSetupLockState(t *testing.T) {
	SetLogger(&mockLogger{})
	token := "s3cret"
	enabled := &configstore.AuthConfig{AdminUserName: schemas.NewSecretVar("admin"), AdminPassword: schemas.NewSecretVar("stored"), IsEnabled: true}
	for _, tt := range []struct {
		name                     string
		gate                     bool
		token                    string
		admin                    *configstore.AuthConfig
		wantRequired, wantConfig bool
	}{
		{name: "no gate (enterprise)", gate: false, token: token},
		{name: "locked with token", gate: true, token: token, wantRequired: true, wantConfig: true},
		{name: "locked without token", gate: true, wantRequired: true},
		{name: "auth enabled", gate: true, token: token, admin: enabled, wantConfig: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newRealOAuth2Store(t)
			if tt.admin != nil {
				require.NoError(t, store.UpdateAuthConfig(bgCtx(), tt.admin))
			}
			h := &SessionHandler{configStore: store}
			if tt.gate {
				am := &AuthMiddleware{}
				if tt.token != "" {
					am.setupToken.Store(&tt.token)
				}
				am.UpdateAuthConfig(tt.admin)
				h.SetSetupLock(am)
			}
			ctx := putConfigCtx("")
			h.isAuthEnabled(ctx)
			var reported struct {
				SetupRequired        *bool `json:"setup_required"`
				SetupTokenConfigured *bool `json:"setup_token_configured"`
			}
			require.NoError(t, json.Unmarshal(ctx.Response.Body(), &reported), string(ctx.Response.Body()))
			require.NotNil(t, reported.SetupRequired)
			require.NotNil(t, reported.SetupTokenConfigured)
			assert.Equal(t, tt.wantRequired, *reported.SetupRequired)
			assert.Equal(t, tt.wantConfig, *reported.SetupTokenConfigured)
		})
	}
}

// TestStartSetupSession_IssuesHttpOnlyCookie pins POST /api/session/setup: it trades a valid
// setup token for an HttpOnly, SameSite=Strict cookie the gate accepts, refuses a wrong token,
// and refuses outright when the setup lock is not active. Logout expires the cookie.
func TestStartSetupSession_IssuesHttpOnlyCookie(t *testing.T) {
	SetLogger(&mockLogger{})
	token := "s3cret"
	newLocked := func() (*SessionHandler, *AuthMiddleware) {
		am := &AuthMiddleware{}
		am.setupToken.Store(&token)
		h := &SessionHandler{configStore: newRealOAuth2Store(t)}
		h.SetSetupLock(am)
		return h, am
	}
	post := func(h *SessionHandler, header string) *fasthttp.RequestCtx {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.SetMethod("POST")
		ctx.Request.SetRequestURI("/api/session/setup")
		if header != "" {
			ctx.Request.Header.Set(SetupTokenHeader, header)
		}
		h.startSetupSession(ctx)
		return ctx
	}

	t.Run("valid token", func(t *testing.T) {
		h, am := newLocked()
		ctx := post(h, token)
		require.Equal(t, 200, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		cookie := fasthttp.AcquireCookie()
		defer fasthttp.ReleaseCookie(cookie)
		cookie.SetKey(SetupSessionCookie)
		require.True(t, ctx.Response.Header.Cookie(cookie), "Set-Cookie %s missing", SetupSessionCookie)
		assert.True(t, cookie.HTTPOnly(), "setup session cookie must be HttpOnly")
		assert.Equal(t, fasthttp.CookieSameSiteStrictMode, cookie.SameSite())
		assert.Equal(t, "/", string(cookie.Path()))
		assert.NotContains(t, string(cookie.Value()), token, "cookie must not carry the token")
		assert.True(t, am.validSetupSession(string(cookie.Value()), time.Now()), "issued cookie must pass the gate")
	})
	t.Run("forwarded https sets Secure", func(t *testing.T) {
		h, _ := newLocked()
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.SetMethod("POST")
		ctx.Request.Header.Set(SetupTokenHeader, token)
		ctx.Request.Header.Set("X-Forwarded-Proto", "https")
		h.startSetupSession(ctx)
		require.Equal(t, 200, ctx.Response.StatusCode())
		cookie := fasthttp.AcquireCookie()
		defer fasthttp.ReleaseCookie(cookie)
		cookie.SetKey(SetupSessionCookie)
		require.True(t, ctx.Response.Header.Cookie(cookie))
		assert.True(t, cookie.Secure())
	})
	t.Run("wrong or missing token", func(t *testing.T) {
		h, _ := newLocked()
		for _, header := range []string{"nope", ""} {
			ctx := post(h, header)
			assert.Equal(t, 403, ctx.Response.StatusCode(), "header %q", header)
			assert.Empty(t, ctx.Response.Header.PeekCookie(SetupSessionCookie))
		}
	})
	t.Run("lock not active", func(t *testing.T) {
		enterprise := &SessionHandler{configStore: newRealOAuth2Store(t)}
		assert.Equal(t, 409, post(enterprise, token).Response.StatusCode(), "no gate installed")
		h, am := newLocked()
		am.UpdateAuthConfig(&configstore.AuthConfig{AdminUserName: schemas.NewSecretVar("admin"), AdminPassword: schemas.NewSecretVar("x"), IsEnabled: true})
		assert.Equal(t, 409, post(h, token).Response.StatusCode(), "dashboard auth enabled")
	})
	t.Run("logout expires the cookie", func(t *testing.T) {
		h, _ := newLocked()
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.SetMethod("POST")
		h.logout(ctx)
		cookie := fasthttp.AcquireCookie()
		defer fasthttp.ReleaseCookie(cookie)
		cookie.SetKey(SetupSessionCookie)
		require.True(t, ctx.Response.Header.Cookie(cookie), "logout must clear %s", SetupSessionCookie)
		assert.Empty(t, string(cookie.Value()))
		assert.True(t, cookie.Expire().Before(time.Now()))
	})
}

// TestUpdateConfig_SetupTokenFirstAdminCanLogIn pins the full first-admin path through the
// OSS setup lock: an admin created by a setup-token-authenticated PUT (the security view's
// payload shape) can then log in with exactly the credentials it was created with.
func TestUpdateConfig_SetupTokenFirstAdminCanLogIn(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
	require.NoError(t, store.UpdateClientConfig(bgCtx(), cfg.ClientConfig))
	h := &ConfigHandler{store: cfg, configManager: setupConfigManager{store: store, validToken: false}}
	ctx := putConfigCtx(`{"client_config":{"log_retention_days":7},"auth_config":{"is_enabled":true,"admin_username":{"value":"admin","ref":""},"admin_password":{"value":"StrongPassword1!","ref":""}}}`)
	ctx.SetUserValue(schemas.BifrostContextKeySetupTokenAuthenticated, true)
	h.updateConfig(ctx)
	require.Equal(t, 200, ctx.Response.StatusCode(), string(ctx.Response.Body()))

	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{name: "same credentials", body: `{"username":"admin","password":"StrongPassword1!"}`, status: 200},
		{name: "wrong password", body: `{"username":"admin","password":"StrongPassword1"}`, status: 401},
	} {
		login := putConfigCtx(tc.body)
		(&SessionHandler{configStore: store}).login(login)
		assert.Equal(t, tc.status, login.Response.StatusCode(), "%s: %s", tc.name, login.Response.Body())
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
