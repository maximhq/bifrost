package handlers

import (
	"context"
	"errors"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// ErrOdinUnavailable is returned when Odin has no usable configuration. It is
// the analogue of ErrNotificationsUnavailable, and like that one it is a
// supported deployment state rather than a fault.
var ErrOdinUnavailable = errors.New("odin is not configured")

// OdinService owns Odin's configuration API. The chat endpoint and its agent
// loop are registered separately, and only when a log store exists for Odin to
// read — see RegisterRoutes.
type OdinService struct {
	store configstore.OdinStore
	// logManager is nil on deployments with no logging plugin. Odin's tools have
	// nothing to read there, so the chat route is not registered at all rather
	// than registered and always failing.
	logManager logging.LogManager
	// client owns Odin's dedicated Bifrost instance. Tests replace chatOverride
	// instead, so the agent loop can be driven by a scripted model.
	client *odinClient
	// chatOverride, when set, replaces the real inference path. Test seam only.
	chatOverride odinChatFunc
}

// NewOdinService builds the Odin service. A nil logManager is a supported
// deployment (logging disabled): Odin then serves only its configuration routes,
// because its tools would have nothing to read.
func NewOdinService(store configstore.ConfigStore, logManager logging.LogManager, logger schemas.Logger) *OdinService {
	odinStore, _ := store.(configstore.OdinStore)
	service := &OdinService{store: odinStore, logManager: logManager}
	if logManager != nil {
		service.client = newOdinClient(logger)
	}
	return service
}

// chatFuncFor resolves the inference function for a request. Config is read per
// request rather than captured at construction, so a settings change takes
// effect without a restart.
func (s *OdinService) chatFuncFor(ctx context.Context, config *schemas.OdinConfig) odinChatFunc {
	if s.chatOverride != nil {
		return s.chatOverride
	}
	if s.client == nil {
		return nil
	}
	return s.client.chat(ctx, config)
}

// Shutdown releases Odin's model client.
func (s *OdinService) Shutdown() {
	if s.client != nil {
		s.client.Shutdown()
	}
}

// RegisterRoutes wires the configuration API. Every route goes through the
// standard middleware chain: Odin reads the deployment's own telemetry, so it
// must never be reachable on an unauthenticated route the way the OAuth2
// issuance handler deliberately is.
func (s *OdinService) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/odin/config", lib.ChainMiddlewares(s.getConfig, middlewares...))
	r.PUT("/api/odin/config", lib.ChainMiddlewares(s.putConfig, middlewares...))

	// The chat route exists only where Odin has data to read and a way to reach a
	// model. A route that is registered but always 503s is worse than absent: it
	// tells the dashboard the feature is present, and the failure only shows up
	// after a user has typed a question.
	if s.logManager != nil && (s.client != nil || s.chatOverride != nil) {
		r.POST("/api/odin/chat", lib.ChainMiddlewares(s.chat, middlewares...))
	}
}

// odinConfigResponse is what the settings page renders. It can be returned
// whole, with no redaction step, because the config stores a key reference
// rather than a key.
type odinConfigResponse struct {
	Configured            bool                  `json:"configured"`
	Enabled               bool                  `json:"enabled"`
	Provider              schemas.ModelProvider `json:"provider"`
	Model                 string                `json:"model"`
	BaseURL               string                `json:"base_url,omitempty"`
	APIKeyID              string                `json:"api_key_id,omitempty"`
	MaxIterations         int                   `json:"max_iterations"`
	RequestTimeoutSeconds int                   `json:"request_timeout_seconds"`
	SystemPromptSuffix    string                `json:"system_prompt_suffix,omitempty"`
}

// odinConfigRequest is the PUT body.
type odinConfigRequest struct {
	Enabled  bool                  `json:"enabled"`
	Provider schemas.ModelProvider `json:"provider"`
	Model    string                `json:"model"`
	BaseURL  string                `json:"base_url,omitempty"`
	// APIKeyID names one of the provider's configured keys, or is empty for a
	// provider that needs none. It round-trips like any other field - no
	// omitted-means-unchanged special case, because there is no secret to lose.
	APIKeyID              string `json:"api_key_id,omitempty"`
	MaxIterations         int    `json:"max_iterations,omitempty"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds,omitempty"`
	SystemPromptSuffix    string `json:"system_prompt_suffix,omitempty"`
}

// getConfig serves the settings page. It is safe for any authenticated caller
// because the stored credential is never part of the response.
func (s *OdinService) getConfig(ctx *fasthttp.RequestCtx) {
	if s.store == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, ErrOdinUnavailable.Error())
		return
	}
	row, err := s.store.GetOdinConfig(ctx)
	if err != nil {
		logger.Warn("failed to read odin configuration: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to read odin configuration")
		return
	}
	// An unconfigured deployment gets a 200 with configured:false rather than a
	// 404. The settings page needs to render its empty form, and a 404 would
	// make "Odin was never set up" indistinguishable from "this build has no
	// Odin route" on the client.
	if row == nil {
		SendJSON(ctx, odinConfigResponse{
			MaxIterations:         schemas.OdinDefaultMaxIterations,
			RequestTimeoutSeconds: schemas.OdinDefaultRequestTimeoutSeconds,
		})
		return
	}
	SendJSON(ctx, odinConfigResponseFromRow(row))
}

// odinConfigResponseFromRow renders a stored row for the API, replacing the
// credential with a presence flag and resolving defaults so the form never has
// to show a zero where a default applies.
func odinConfigResponseFromRow(row *tables.TableOdinConfig) odinConfigResponse {
	config := odinConfigFromRow(row)
	return odinConfigResponse{
		Configured:            config.IsConfigured(),
		Enabled:               row.Enabled,
		Provider:              schemas.ModelProvider(row.Provider),
		Model:                 row.Model,
		BaseURL:               row.BaseURL,
		APIKeyID:              row.APIKeyID,
		MaxIterations:         config.EffectiveMaxIterations(),
		RequestTimeoutSeconds: config.EffectiveRequestTimeoutSeconds(),
		SystemPromptSuffix:    derefString(row.SystemPromptSuffix),
	}
}

// odinConfigFromRow lifts a stored row into the shared schema type.
func odinConfigFromRow(row *tables.TableOdinConfig) *schemas.OdinConfig {
	if row == nil {
		return nil
	}
	config := &schemas.OdinConfig{
		Enabled:               row.Enabled,
		APIKeyID:              row.APIKeyID,
		Provider:              schemas.ModelProvider(row.Provider),
		Model:                 row.Model,
		BaseURL:               row.BaseURL,
		MaxIterations:         row.MaxIterations,
		RequestTimeoutSeconds: row.RequestTimeoutSeconds,
		SystemPromptSuffix:    derefString(row.SystemPromptSuffix),
		UpdatedAt:             row.UpdatedAt,
	}
	return config
}

// derefString reads a *string, treating nil as empty.
func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// putConfig is admin-only, on the same reasoning notifications.go applies to
// publishing: RBAC in this transport is enterprise-only and path-based, so the
// OSS floor is enforced in the handler. A single PUT plants a credential that
// the server will then use to make outbound calls, which is not something an
// ordinary dashboard user should be able to do.
func (s *OdinService) putConfig(ctx *fasthttp.RequestCtx) {
	if localAdmin, _ := ctx.UserValue(schemas.IsLocalAdminContextKey).(bool); !localAdmin {
		SendError(ctx, fasthttp.StatusForbidden, "Only administrators can configure Odin")
		return
	}
	if s.store == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, ErrOdinUnavailable.Error())
		return
	}

	var input odinConfigRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &input); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}
	if err := validateOdinConfigInput(&input); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	row := &tables.TableOdinConfig{
		Enabled:               input.Enabled,
		Provider:              string(input.Provider),
		Model:                 input.Model,
		BaseURL:               input.BaseURL,
		APIKeyID:              strings.TrimSpace(input.APIKeyID),
		MaxIterations:         input.MaxIterations,
		RequestTimeoutSeconds: input.RequestTimeoutSeconds,
	}
	if input.SystemPromptSuffix != "" {
		row.SystemPromptSuffix = &input.SystemPromptSuffix
	}
	if err := s.store.UpsertOdinConfig(ctx, row); err != nil {
		// Log the cause. The client gets a generic message because a raw driver
		// error can name columns and constraints, but swallowing it entirely
		// leaves an operator staring at a 500 with nothing to act on - which is
		// exactly what a schema drift between the table and this struct produces.
		logger.Warn("failed to save odin configuration: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to save odin configuration")
		return
	}
	SendJSON(ctx, odinConfigResponseFromRow(row))
}

// validateOdinConfigInput normalizes and checks a write.
//
// Completeness is only required when enabled is true: an operator saving a
// half-filled form with the toggle off is drafting, and rejecting that would
// make the form impossible to fill in across more than one sitting.
func validateOdinConfigInput(input *odinConfigRequest) error {
	input.Model = strings.TrimSpace(input.Model)
	input.BaseURL = strings.TrimSpace(input.BaseURL)
	input.Provider = schemas.ModelProvider(strings.TrimSpace(string(input.Provider)))

	// Only a config that claims to be usable has to be complete. An operator
	// saving a half-filled form with the toggle off is drafting, not
	// misconfiguring, and rejecting that would make the form impossible to fill
	// in over more than one sitting.
	if input.Enabled {
		if input.Provider == "" {
			return errors.New("provider is required when odin is enabled")
		}
		if input.Model == "" {
			return errors.New("model is required when odin is enabled")
		}
	}
	if input.MaxIterations < 0 || input.MaxIterations > schemas.OdinMaxIterationsCeiling {
		return errors.New("max_iterations must be between 0 and 20")
	}
	if input.RequestTimeoutSeconds < 0 {
		return errors.New("request_timeout_seconds must not be negative")
	}
	return nil
}

// GetConfig exposes the resolved Odin configuration to in-process callers (the
// chat handler and its client builder), returning ErrOdinUnavailable when Odin
// cannot answer. It resolves secret references, so callers get a usable key.
func (s *OdinService) GetConfig(ctx context.Context) (*schemas.OdinConfig, error) {
	if s.store == nil {
		return nil, ErrOdinUnavailable
	}
	row, err := s.store.GetOdinConfig(ctx)
	if err != nil {
		return nil, err
	}
	config := odinConfigFromRow(row)
	if !config.IsConfigured() {
		return nil, ErrOdinUnavailable
	}
	return config, nil
}
