package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/fasthttp/router"
	"github.com/google/uuid"
	providerCodex "github.com/maximhq/bifrost/core/providers/codex"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

const (
	codexAuthSessionPending   = "pending"
	codexAuthSessionSucceeded = "authorized"
	codexAuthSessionFailed    = "failed"
	codexAuthSessionExpired   = "expired"
	codexAuthSessionCancelled = "cancelled"
	defaultCodexSessionTTL    = 15 * time.Minute
)

type CodexAuthHandler struct {
	store      *lib.Config
	httpClient *http.Client
}

func NewCodexAuthHandler(store *lib.Config) *CodexAuthHandler {
	return &CodexAuthHandler{
		store: store,
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

func (h *CodexAuthHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.POST("/api/providers/codex/keys/{keyId}/auth/device/start", lib.ChainMiddlewares(h.startDeviceAuth, middlewares...))
	r.GET("/api/providers/codex/auth/sessions/{id}", lib.ChainMiddlewares(h.getAuthSessionStatus, middlewares...))
	r.DELETE("/api/providers/codex/auth/sessions/{id}", lib.ChainMiddlewares(h.cancelAuthSession, middlewares...))
}

func (h *CodexAuthHandler) startDeviceAuth(ctx *fasthttp.RequestCtx) {
	keyID := ctx.UserValue("keyId").(string)
	if _, _, err := h.getEditableCodexKey(keyID); err != nil {
		h.sendAuthError(ctx, err)
		return
	}
	requestCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	deviceAuth, err := providerCodex.StartDeviceAuthorization(requestCtx, h.httpClient, h.userAgent())
	if err != nil {
		SendError(ctx, fasthttp.StatusBadGateway, fmt.Sprintf("Failed to start device authorization: %v", err))
		return
	}
	intervalSeconds := 5
	if parsed, parseErr := time.ParseDuration(deviceAuth.Interval + "s"); parseErr == nil {
		intervalSeconds = max(1, int(parsed.Seconds()))
	}
	verificationURI := providerCodex.DeviceVerificationURL
	deviceAuthID := deviceAuth.DeviceAuthID
	userCode := deviceAuth.UserCode
	nextPollAt := providerCodex.NextPollTime(intervalSeconds)
	session := &configstoreTables.TableCodexAuthSession{
		ID:              uuid.NewString(),
		Provider:        string(schemas.Codex),
		KeyID:           keyID,
		FlowType:        string(schemas.CodexAuthMethodDevice),
		Status:          codexAuthSessionPending,
		DeviceAuthID:    &deviceAuthID,
		UserCode:        &userCode,
		VerificationURI: &verificationURI,
		IntervalSeconds: &intervalSeconds,
		NextPollAt:      &nextPollAt,
		ExpiresAt:       time.Now().Add(defaultCodexSessionTTL),
	}
	if err := h.store.ConfigStore.CreateCodexAuthSession(ctx, session); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to create auth session: %v", err))
		return
	}
	SendJSON(ctx, h.sessionResponse(session))
}

func (h *CodexAuthHandler) getAuthSessionStatus(ctx *fasthttp.RequestCtx) {
	sessionID := ctx.UserValue("id").(string)
	session, err := h.store.ConfigStore.GetCodexAuthSessionByID(ctx, sessionID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get auth session: %v", err))
		return
	}
	if session == nil {
		SendError(ctx, fasthttp.StatusNotFound, "Auth session not found")
		return
	}
	if err := h.refreshSessionState(ctx, session); err != nil {
		SendError(ctx, fasthttp.StatusBadGateway, fmt.Sprintf("Failed to refresh auth session: %v", err))
		return
	}
	SendJSON(ctx, h.sessionResponse(session))
}

func (h *CodexAuthHandler) cancelAuthSession(ctx *fasthttp.RequestCtx) {
	sessionID := ctx.UserValue("id").(string)
	session, err := h.store.ConfigStore.GetCodexAuthSessionByID(ctx, sessionID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get auth session: %v", err))
		return
	}
	if session == nil {
		SendError(ctx, fasthttp.StatusNotFound, "Auth session not found")
		return
	}
	session.Status = codexAuthSessionCancelled
	now := time.Now()
	session.CompletedAt = &now
	if err := h.store.ConfigStore.UpdateCodexAuthSession(ctx, session); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to cancel auth session: %v", err))
		return
	}
	SendJSON(ctx, h.sessionResponse(session))
}

func (h *CodexAuthHandler) refreshSessionState(ctx context.Context, session *configstoreTables.TableCodexAuthSession) error {
	if session.Status != codexAuthSessionPending {
		return nil
	}
	if time.Now().After(session.ExpiresAt) {
		session.Status = codexAuthSessionExpired
		return h.store.ConfigStore.UpdateCodexAuthSession(ctx, session)
	}
	if session.FlowType != string(schemas.CodexAuthMethodDevice) {
		return nil
	}
	if session.NextPollAt != nil && time.Now().Before(*session.NextPollAt) {
		return nil
	}
	if session.DeviceAuthID == nil || session.UserCode == nil {
		session.Status = codexAuthSessionFailed
		message := "Missing device authorization state"
		session.LastError = &message
		return h.store.ConfigStore.UpdateCodexAuthSession(ctx, session)
	}
	pollCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	deviceToken, statusCode, err := providerCodex.PollDeviceAuthorization(pollCtx, h.httpClient, *session.DeviceAuthID, *session.UserCode, h.userAgent())
	if err != nil {
		return err
	}
	if statusCode == http.StatusOK && deviceToken != nil {
		tokens, err := providerCodex.ExchangeDeviceAuthorizationCode(pollCtx, h.httpClient, deviceToken.AuthorizationCode, deviceToken.CodeVerifier)
		if err != nil {
			session.Status = codexAuthSessionFailed
			message := err.Error()
			session.LastError = &message
			return h.store.ConfigStore.UpdateCodexAuthSession(ctx, session)
		}
		if err := h.persistTokensToKey(ctx, session.KeyID, tokens, schemas.CodexAuthMethodDevice); err != nil {
			session.Status = codexAuthSessionFailed
			message := err.Error()
			session.LastError = &message
			return h.store.ConfigStore.UpdateCodexAuthSession(ctx, session)
		}
		h.completeSession(session)
		return nil
	}
	if statusCode == fasthttp.StatusForbidden || statusCode == fasthttp.StatusNotFound {
		nextPollAt := providerCodex.NextPollTime(valueOrDefault(session.IntervalSeconds, 5))
		session.NextPollAt = &nextPollAt
		return h.store.ConfigStore.UpdateCodexAuthSession(ctx, session)
	}
	session.Status = codexAuthSessionFailed
	message := fmt.Sprintf("Device authorization failed with status %d", statusCode)
	session.LastError = &message
	return h.store.ConfigStore.UpdateCodexAuthSession(ctx, session)
}

func (h *CodexAuthHandler) persistTokensToKey(ctx context.Context, keyID string, tokens *providerCodex.TokenResponse, authMethod schemas.CodexAuthMethod) error {
	providerConfig, _, err := h.getEditableCodexKey(keyID)
	if err != nil {
		return err
	}
	updatedKeys := append([]schemas.Key(nil), providerConfig.Keys...)
	for idx := range updatedKeys {
		if updatedKeys[idx].ID != keyID {
			continue
		}
		var existingAccountID *schemas.EnvVar
		if updatedKeys[idx].CodexKeyConfig != nil {
			existingAccountID = updatedKeys[idx].CodexKeyConfig.AccountID
		}
		refreshToken := schemas.NewEnvVar(tokens.RefreshToken)
		accessToken := schemas.NewEnvVar(tokens.AccessToken)
		accountIDValue := providerCodex.ExtractAccountID(tokens)
		accessTokenExpiresAt := providerCodex.ExpiresAtFromNow(tokens.ExpiresIn)
		updatedKeys[idx].CodexKeyConfig = &schemas.CodexKeyConfig{
			RefreshToken:         *refreshToken,
			AccessToken:          accessToken,
			AccessTokenExpiresAt: &accessTokenExpiresAt,
			AuthMethod:           authMethod,
		}
		if accountIDValue != "" {
			updatedKeys[idx].CodexKeyConfig.AccountID = schemas.NewEnvVar(accountIDValue)
		} else if existingAccountID != nil {
			updatedKeys[idx].CodexKeyConfig.AccountID = existingAccountID
		}
		providerConfig.Keys = updatedKeys
		if providerConfig.CodexConfig == nil {
			providerConfig.CodexConfig = &schemas.CodexConfig{PricingMode: schemas.CodexPricingModeIncludedZero}
		}
		return h.store.UpdateProviderConfig(ctx, schemas.Codex, *providerConfig)
	}
	return fmt.Errorf("Codex key %s not found", keyID)
}

func (h *CodexAuthHandler) getEditableCodexKey(keyID string) (*configstore.ProviderConfig, *schemas.Key, error) {
	if h.store == nil || h.store.ConfigStore == nil {
		return nil, nil, fmt.Errorf("database-backed config store is required for Codex authentication")
	}
	providerConfig, err := h.store.GetProviderConfigRaw(schemas.Codex)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load Codex provider config: %w", err)
	}
	if providerConfig == nil {
		return nil, nil, fmt.Errorf("Codex provider is not configured")
	}
	for idx := range providerConfig.Keys {
		if providerConfig.Keys[idx].ID != keyID {
			continue
		}
		// ConfigHash is used for general reconciliation and is not a reliable indicator
		// that a key is currently managed by config.json. Allow Codex reauth for saved keys.
		return providerConfig, &providerConfig.Keys[idx], nil
	}
	return nil, nil, fmt.Errorf("Codex key %s not found", keyID)
}

func (h *CodexAuthHandler) completeSession(session *configstoreTables.TableCodexAuthSession) {
	now := time.Now()
	session.Status = codexAuthSessionSucceeded
	session.CompletedAt = &now
	session.LastError = nil
	_ = h.store.ConfigStore.UpdateCodexAuthSession(context.Background(), session)
}

func (h *CodexAuthHandler) failSession(session *configstoreTables.TableCodexAuthSession, message string) {
	now := time.Now()
	session.Status = codexAuthSessionFailed
	session.CompletedAt = &now
	session.LastError = &message
	_ = h.store.ConfigStore.UpdateCodexAuthSession(context.Background(), session)
}

func (h *CodexAuthHandler) sessionResponse(session *configstoreTables.TableCodexAuthSession) map[string]any {
	response := map[string]any{
		"id":         session.ID,
		"flow_type":  session.FlowType,
		"status":     session.Status,
		"expires_at": session.ExpiresAt,
	}
	if session.VerificationURI != nil {
		response["verification_uri"] = *session.VerificationURI
	}
	if session.UserCode != nil {
		response["user_code"] = *session.UserCode
	}
	if session.IntervalSeconds != nil {
		response["interval_seconds"] = *session.IntervalSeconds
	}
	if session.NextPollAt != nil {
		response["next_poll_at"] = *session.NextPollAt
	}
	if session.LastError != nil {
		response["last_error"] = *session.LastError
	}
	if session.CompletedAt != nil {
		response["completed_at"] = *session.CompletedAt
	}
	return response
}

func (h *CodexAuthHandler) userAgent() string {
	return "bifrost-codex-auth"
}

func (h *CodexAuthHandler) sendAuthError(ctx *fasthttp.RequestCtx, err error) {
	status := fasthttp.StatusBadRequest
	if strings.Contains(err.Error(), "database-backed config store") {
		status = fasthttp.StatusServiceUnavailable
	}
	SendError(ctx, status, err.Error())
}

func valueOrDefault(value *int, fallback int) int {
	if value == nil || *value <= 0 {
		return fallback
	}
	return *value
}
