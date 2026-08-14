// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains the routing rule CRUD endpoints and the complexity analyzer
// configuration endpoints that back the complexity_tier routing variable.
package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/rules"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// ErrRoutingPluginUnavailable marks a RoutingManager call that failed because the routing
// plugin could not be reached at all, rather than because the caller's payload was bad.
// Implementations must wrap it so handlers can tell the two apart: the payload-rejection
// case is the client's to fix and answers 400, while an unreachable plugin is a server
// misconfiguration that would otherwise be reported as an invalid configuration.
var ErrRoutingPluginUnavailable = errors.New("routing plugin unavailable")

// RoutingManager applies routing-rule and complexity-analyzer edits to the running plugin.
// Writes land in the config store first; these calls refresh the in-memory state that
// request evaluation reads, so an edit takes effect without a restart.
type RoutingManager interface {
	ReloadRoutingRule(ctx context.Context, id string) error
	RemoveRoutingRule(ctx context.Context, id string) error
	ReloadComplexityAnalyzerConfig(ctx context.Context, config *complexity.AnalyzerConfig) error
	// ValidateComplexityAnalyzerConfig checks runtime dependencies that static
	// JSON validation cannot prove, such as an external VectorStore being
	// configured for semantic.vector_store "vector_store" mode.
	ValidateComplexityAnalyzerConfig(ctx context.Context, config *complexity.AnalyzerConfig) error
	// GetComplexitySemanticStatus returns the non-persisted readiness of
	// semantic complexity routing so configuration clients can distinguish
	// saved from ready.
	GetComplexitySemanticStatus(ctx context.Context) (complexity.SemanticStatusInfo, error)
	// GetComplexityLLMStatus returns the llm fallback classifier's readiness.
	GetComplexityLLMStatus(ctx context.Context) (complexity.LLMStatusInfo, error)
}

// complexityStatusResponse is the analyzer status payload. SemanticStatusInfo is
// embedded rather than nested so every field clients already read stays at the
// top level; session_store and llm are additive and omitted when absent.
type complexityStatusResponse struct {
	complexity.SemanticStatusInfo
	LLM *complexity.LLMStatusInfo `json:"llm,omitempty"`
	// LLMDefaultPrompt is the shipped classification guidance, served so the
	// UI can seed its prompt editor and offer a reset without holding a copy
	// that drifts from the gateway's. It is the editable half only; the fixed
	// tier-name reinforcement is appended server-side and never exposed.
	LLMDefaultPrompt string `json:"llm_default_prompt,omitempty"`
}

// RoutingHandler manages HTTP requests for routing rules and complexity analyzer config.
type RoutingHandler struct {
	configStore    configstore.ConfigStore
	routingManager RoutingManager
}

// NewRoutingHandler creates a routing handler. Both the manager and the config store are
// required: rules are persisted in the store and applied through the manager.
func NewRoutingHandler(manager RoutingManager, configStore configstore.ConfigStore) (*RoutingHandler, error) {
	if manager == nil {
		return nil, fmt.Errorf("routing manager is required")
	}
	if configStore == nil {
		return nil, fmt.Errorf("config store is required")
	}
	return &RoutingHandler{
		routingManager: manager,
		configStore:    configStore,
	}, nil
}

// RegisterRoutes registers the routing rule and complexity analyzer routes.
//
// Every endpoint is served on two paths: the canonical /api/routing/* path, and the
// /api/governance/* path it shipped under while routing rules lived inside the governance
// plugin. The second registration exists purely for backwards compatibility with clients
// and scripts written against the original paths, so those keep working unchanged; new
// callers should use /api/routing.
func (h *RoutingHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	// Both paths are registered with the same wrapped handler value, so the backwards
	// compatible path can never drift from the canonical one in behavior or middleware.
	register := func(method string, canonical string, legacy string, handler fasthttp.RequestHandler) {
		wrapped := lib.ChainMiddlewares(handler, middlewares...)
		r.Handle(method, canonical, wrapped)
		r.Handle(method, legacy, wrapped)
	}

	register(fasthttp.MethodGet, "/api/routing/rules", "/api/governance/routing-rules", h.getRoutingRules)
	register(fasthttp.MethodPost, "/api/routing/rules", "/api/governance/routing-rules", h.createRoutingRule)
	register(fasthttp.MethodGet, "/api/routing/rules/{rule_id}", "/api/governance/routing-rules/{rule_id}", h.getRoutingRule)
	register(fasthttp.MethodPut, "/api/routing/rules/{rule_id}", "/api/governance/routing-rules/{rule_id}", h.updateRoutingRule)
	register(fasthttp.MethodDelete, "/api/routing/rules/{rule_id}", "/api/governance/routing-rules/{rule_id}", h.deleteRoutingRule)

	register(fasthttp.MethodGet, "/api/routing/complexity-analyzer-config", "/api/governance/complexity-analyzer-config", h.getComplexityAnalyzerConfig)
	register(fasthttp.MethodPut, "/api/routing/complexity-analyzer-config", "/api/governance/complexity-analyzer-config", h.updateComplexityAnalyzerConfig)
	register(fasthttp.MethodPost, "/api/routing/complexity-analyzer-config/reset", "/api/governance/complexity-analyzer-config/reset", h.resetComplexityAnalyzerConfig)
	// Status never shipped under /api/governance, so it has no legacy alias.
	r.Handle(fasthttp.MethodGet, "/api/routing/complexity-analyzer-status", lib.ChainMiddlewares(h.getComplexitySemanticStatus, middlewares...))
}

// RoutingTarget represents a single weighted routing target within a rule.
// All fields except Weight are optional; nil means "use the incoming request's value".
// Weights across all targets in a rule must sum to 1 (e.g. 0.7 + 0.3 = 1.0).
type RoutingTarget struct {
	Provider *string `json:"provider,omitempty"` // nil = use incoming provider
	Model    *string `json:"model,omitempty"`    // nil = use incoming model
	KeyID    *string `json:"key_id,omitempty"`   // nil = no key pin
	Weight   float64 `json:"weight"`             // must be > 0; all weights must sum to 1
}

// CreateRoutingRuleRequest represents the request body for creating a routing rule
type CreateRoutingRuleRequest struct {
	Name          string          `json:"name" validate:"required"`
	Description   string          `json:"description,omitempty"`
	Enabled       *bool           `json:"enabled,omitempty"`    // nil = use DB default (true)
	ChainRule     *bool           `json:"chain_rule,omitempty"` // nil = use DB default (false)
	CelExpression string          `json:"cel_expression"`
	Targets       []RoutingTarget `json:"targets"` // Required; weights must sum to 1
	Fallbacks     []string        `json:"fallbacks,omitempty"`
	Scope         string          `json:"scope,omitempty"` // Defaults to "global" if not provided
	ScopeID       *string         `json:"scope_id,omitempty"`
	Query         map[string]any  `json:"query,omitempty"`
	Priority      int             `json:"priority,omitempty"` // Defaults to 0 if not provided
}

// UpdateRoutingRuleRequest represents the request body for updating a routing rule
type UpdateRoutingRuleRequest struct {
	Name          *string         `json:"name,omitempty"`
	Description   *string         `json:"description,omitempty"`
	Enabled       *bool           `json:"enabled,omitempty"`
	ChainRule     *bool           `json:"chain_rule,omitempty"`
	CelExpression *string         `json:"cel_expression,omitempty"`
	Targets       []RoutingTarget `json:"targets,omitempty"` // If provided, replaces all existing targets; weights must sum to 1
	Fallbacks     []string        `json:"fallbacks,omitempty"`
	Query         map[string]any  `json:"query,omitempty"`
	Priority      *int            `json:"priority,omitempty"`
	Scope         *string         `json:"scope,omitempty"`
	ScopeID       *string         `json:"scope_id,omitempty"`
}

// validRoutingScopes contains the allowed scope values for routing rules
var validRoutingScopes = map[string]bool{
	"global":      true,
	"team":        true,
	"customer":    true,
	"virtual_key": true,
	"user":        true,
}

// errRoutingScopeIDNotFound marks a validateRoutingScopeID failure as a genuine
// "entity doesn't exist" rejection, as opposed to a store error (DB down, timeout,
// context cancellation). Callers use errors.Is to tell the two apart: the former
// is a 400 (bad request data), the latter a 500 (server couldn't verify).
var errRoutingScopeIDNotFound = errors.New("routing rule scope_id not found")

// validateRoutingScopeID checks that scopeID resolves to an existing entity of the
// given scope type. A rule whose scope_id doesn't resolve silently matches zero
// requests (the routing engine caches rules keyed by the real entity ID), so this
// must be rejected at write time rather than left to fail invisibly at eval time.
func (h *RoutingHandler) validateRoutingScopeID(ctx context.Context, scope string, scopeID string) error {
	switch scope {
	case "virtual_key":
		if _, err := h.configStore.GetVirtualKey(ctx, scopeID); err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				return fmt.Errorf("virtual key '%s' not found: %w", scopeID, errRoutingScopeIDNotFound)
			}
			return fmt.Errorf("failed to verify virtual key: %w", err)
		}
	case "team":
		if _, err := h.configStore.GetTeam(ctx, scopeID); err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				return fmt.Errorf("team '%s' not found: %w", scopeID, errRoutingScopeIDNotFound)
			}
			return fmt.Errorf("failed to verify team: %w", err)
		}
	case "customer":
		if _, err := h.configStore.GetCustomer(ctx, scopeID); err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				return fmt.Errorf("customer '%s' not found: %w", scopeID, errRoutingScopeIDNotFound)
			}
			return fmt.Errorf("failed to verify customer: %w", err)
		}
	case "user":
		// User ids live outside the config store (they arrive on requests via
		// the resolved identity context), so existence cannot be verified
		// here; the id is matched at eval time against the calling user.
	}
	return nil
}

// sendRoutingScopeIDValidationError maps a validateRoutingScopeID error to the
// right HTTP status: 400 when scope_id genuinely doesn't resolve, 500 when the
// store itself failed and existence couldn't be determined.
func sendRoutingScopeIDValidationError(ctx *fasthttp.RequestCtx, err error) {
	if errors.Is(err, errRoutingScopeIDNotFound) {
		SendError(ctx, 400, err.Error())
		return
	}
	logger.Error("failed to validate routing rule scope_id: %v", err)
	SendError(ctx, 500, "Failed to verify scope_id")
}

// validateRoutingScope validates that the scope value is one of the allowed values
func validateRoutingScope(scope string) error {
	if scope == "" {
		return nil // Empty scope will default to "global" later
	}
	if !validRoutingScopes[scope] {
		return fmt.Errorf("invalid scope %q: must be one of: global, team, customer, virtual_key, user", scope)
	}
	return nil
}

// validateRoutingTargets checks that all weights are positive, that no two
// targets share the same (provider, model, key_id) identity, and that all
// weights sum to 1.
func validateRoutingTargets(targets []RoutingTarget) error {
	seen := make(map[string]struct{}, len(targets))
	total := 0.0
	for _, t := range targets {
		if t.Weight < 0 {
			return fmt.Errorf("each target weight must be positive")
		}
		if t.KeyID != nil && *t.KeyID != "" && (t.Provider == nil || *t.Provider == "") {
			return fmt.Errorf("key_id requires provider to be set")
		}

		// Canonicalise identity: lowercase provider/model, treat nil == "".
		provider := ""
		if t.Provider != nil {
			provider = strings.ToLower(*t.Provider)
		}
		model := ""
		if t.Model != nil {
			model = strings.ToLower(*t.Model)
		}
		keyID := ""
		if t.KeyID != nil {
			keyID = *t.KeyID
		}
		key := provider + "|" + model + "|" + keyID
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate target entry: provider=%q model=%q key_id=%q", provider, model, keyID)
		}
		seen[key] = struct{}{}

		total += t.Weight
	}
	if math.Abs(total-1.0) > 0.001 {
		return fmt.Errorf("target weights must sum to 1, got %.4f", total)
	}
	return nil
}

// validateRoutingFallbacks ensures each fallback parses to a non-empty known provider via
// schemas.ParseModelString (e.g. "openai/gpt-4o", or "azure/" to use the incoming model).
func validateRoutingFallbacks(fallbacks []string) error {
	for i, fb := range fallbacks {
		if strings.TrimSpace(fb) == "" {
			return fmt.Errorf("fallbacks[%d] must not be empty", i)
		}
		provider, _ := schemas.ParseModelString(fb, "")
		if provider == "" {
			return fmt.Errorf("fallbacks[%d] %q is invalid: must use a known provider prefix (e.g. \"openai/gpt-4o\" or \"azure/\" for the incoming model)", i, fb)
		}
	}
	return nil
}

func (h *RoutingHandler) getComplexityAnalyzerConfig(ctx *fasthttp.RequestCtx) {
	if h.configStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not available")
		return
	}

	cfg, err := h.configStore.GetComplexityAnalyzerConfig(ctx)
	if err != nil {
		if !errors.Is(err, configstore.ErrConfigUnreadable) {
			// The store itself is unreachable. Serving defaults here would
			// report a broken installation as a working one.
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get complexity analyzer config: %v", err))
			return
		}
		// A stored config this version cannot read is already being ignored by
		// the analyzer, which fell back to defaults at boot for the same reason
		// and only logged a warning. Failing the page instead of showing that
		// hides it: routing has already changed behaviour, and the operator
		// cannot see or fix the config through a screen that will not load.
		logger.Warn("serving default complexity analyzer config: %v", err)
		cfg = nil
	}
	if cfg == nil {
		defaults := complexity.DefaultAnalyzerConfig()
		SendJSON(ctx, defaults)
		return
	}
	SendJSON(ctx, cfg)
}

func (h *RoutingHandler) updateComplexityAnalyzerConfig(ctx *fasthttp.RequestCtx) {
	if h.configStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not available")
		return
	}

	var payload complexity.AnalyzerConfig
	decoder := json.NewDecoder(bytes.NewReader(ctx.PostBody()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request format: multiple JSON values")
		return
	}

	normalized, err := complexity.ValidateAndNormalize(&payload)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	if err := h.routingManager.ValidateComplexityAnalyzerConfig(ctx, normalized); err != nil {
		// An unreachable routing plugin says nothing about the submitted config, so it
		// must not be reported as a rejected payload the operator could fix by editing.
		if errors.Is(err, ErrRoutingPluginUnavailable) {
			SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
			return
		}
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	if err := h.configStore.UpdateComplexityAnalyzerConfig(ctx, normalized); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to update complexity analyzer config: %v", err))
		return
	}
	if err := h.reloadComplexityAnalyzerConfig(ctx, normalized); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to reload complexity analyzer config in memory: %v, please restart bifrost to sync with the database", err))
		return
	}

	SendJSON(ctx, normalized)
}

func (h *RoutingHandler) resetComplexityAnalyzerConfig(ctx *fasthttp.RequestCtx) {
	if h.configStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not available")
		return
	}

	// Reset owns the two sections it is named for — the tier boundaries and the
	// phrase lists — and nothing else. The rest of the record is deployment
	// state configured elsewhere: the embedding provider, model, and storage
	// selection, without which classification stops entirely. Replacing the
	// whole record would turn "restore the default phrases" into "turn the
	// classifier off", which is neither what the endpoint says nor what the
	// operator confirmed.
	//
	// The section hashes ride along for the same reason they are carried over on
	// an ordinary update: they record which config.json sections have already
	// been applied. Clearing them would let config.json reapply its phrases on
	// the next boot and silently undo this reset.
	//
	// The store performs the swap: reading the record here and writing it back
	// would leave a window in which a concurrent save of the preserved sections
	// is read before the save and overwritten after it.
	defaults := complexity.DefaultAnalyzerConfig()
	restored, err := h.configStore.ResetComplexityAnalyzerConfig(ctx, &defaults)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to reset complexity analyzer config: %v", err))
		return
	}
	if err := h.reloadComplexityAnalyzerConfig(ctx, restored); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to reload complexity analyzer config in memory: %v, please restart bifrost to sync with the database", err))
		return
	}

	SendJSON(ctx, restored)
}

func (h *RoutingHandler) reloadComplexityAnalyzerConfig(ctx context.Context, config *complexity.AnalyzerConfig) error {
	return h.routingManager.ReloadComplexityAnalyzerConfig(ctx, config)
}

// getComplexitySemanticStatus returns the non-persisted readiness of semantic
// complexity routing so configuration clients can distinguish saved from ready.
func (h *RoutingHandler) getComplexitySemanticStatus(ctx *fasthttp.RequestCtx) {
	status, err := h.routingManager.GetComplexitySemanticStatus(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, fmt.Sprintf("failed to get semantic complexity status: %v", err))
		return
	}
	response := complexityStatusResponse{SemanticStatusInfo: status}
	// The llm classifier state rides the same endpoint and must not be able to
	// fail the whole response.
	if llmStatus, llmErr := h.routingManager.GetComplexityLLMStatus(ctx); llmErr != nil {
		logger.Warn("failed to get llm complexity status: %v", llmErr)
	} else {
		response.LLM = &llmStatus
		response.LLMDefaultPrompt = complexity.DefaultLLMClassifierGuidance()
	}
	SendJSON(ctx, response)
}

// getRoutingRules retrieves all routing rules with optional filtering from database
func (h *RoutingHandler) getRoutingRules(ctx *fasthttp.RequestCtx) {
	// Get query parameters for filtering
	scope := string(ctx.QueryArgs().Peek("scope"))
	scopeID := string(ctx.QueryArgs().Peek("scope_id"))

	// If scope/scopeID filters are specified, use the existing non-paginated path
	if scope != "" || scopeID != "" {
		rules, err := h.configStore.GetRoutingRulesByScope(ctx, scope, scopeID)
		if err != nil {
			SendError(ctx, 500, "Failed to get routing rules")
			return
		}
		response := make([]configstoreTables.TableRoutingRule, 0, len(rules))
		for _, rule := range rules {
			response = append(response, rule)
		}
		SendJSON(ctx, map[string]interface{}{
			"rules":       response,
			"count":       len(response),
			"total_count": len(response),
			"limit":       len(response),
			"offset":      0,
		})
		return
	}

	// Check for pagination parameters
	limitStr := string(ctx.QueryArgs().Peek("limit"))
	offsetStr := string(ctx.QueryArgs().Peek("offset"))
	search := string(ctx.QueryArgs().Peek("search"))

	if limitStr != "" || offsetStr != "" || search != "" {
		// Paginated path
		params := configstore.RoutingRulesQueryParams{
			Search: search,
		}
		if limitStr != "" {
			n, err := strconv.Atoi(limitStr)
			if err != nil {
				SendError(ctx, 400, "Invalid limit parameter: must be a number")
				return
			}
			if n < 0 {
				SendError(ctx, 400, "Invalid limit parameter: must be non-negative")
				return
			}
			params.Limit = n
		}
		if offsetStr != "" {
			n, err := strconv.Atoi(offsetStr)
			if err != nil {
				SendError(ctx, 400, "Invalid offset parameter: must be a number")
				return
			}
			if n < 0 {
				SendError(ctx, 400, "Invalid offset parameter: must be non-negative")
				return
			}
			params.Offset = n
		}

		params.Limit, params.Offset = ClampPaginationParams(params.Limit, params.Offset)
		rules, totalCount, err := h.configStore.GetRoutingRulesPaginated(ctx, params)
		if err != nil {
			logger.Error("failed to retrieve routing rules: %v", err)
			SendError(ctx, 500, "Failed to retrieve routing rules")
			return
		}
		SendJSON(ctx, map[string]interface{}{
			"rules":       rules,
			"count":       len(rules),
			"total_count": totalCount,
			"limit":       params.Limit,
			"offset":      params.Offset,
		})
		return
	}

	// Non-paginated path: return all routing rules
	rules, err := h.configStore.GetRoutingRules(ctx)
	if err != nil {
		logger.Error("failed to retrieve routing rules: %v", err)
		SendError(ctx, 500, "Failed to retrieve routing rules")
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"rules":       rules,
		"count":       len(rules),
		"total_count": len(rules),
		"limit":       len(rules),
		"offset":      0,
	})
}

// getRoutingRule retrieves a single routing rule by ID from database
func (h *RoutingHandler) getRoutingRule(ctx *fasthttp.RequestCtx) {
	ruleID := ctx.UserValue("rule_id").(string)

	rule, err := h.configStore.GetRoutingRule(ctx, ruleID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Routing rule not found")
			return
		}
		logger.Error("failed to get routing rule: %v", err)
		SendError(ctx, 500, "Failed to retrieve routing rule")
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"rule": rule,
	})
}

// createRoutingRule creates a new routing rule
func (h *RoutingHandler) createRoutingRule(ctx *fasthttp.RequestCtx) {
	// Parse request body
	var req CreateRoutingRuleRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON")
		return
	}

	// Validate required fields
	if req.Name == "" {
		SendError(ctx, 400, "name field is required")
		return
	}

	// Validate targets
	if len(req.Targets) == 0 {
		SendError(ctx, 400, "at least one target is required")
		return
	}
	if err := validateRoutingTargets(req.Targets); err != nil {
		SendError(ctx, 400, err.Error())
		return
	}
	if err := validateRoutingFallbacks(req.Fallbacks); err != nil {
		SendError(ctx, 400, err.Error())
		return
	}
	// Reject malformed CEL at write time instead of it silently failing at first evaluation.
	if err := rules.ValidateCELExpression(req.CelExpression); err != nil {
		SendError(ctx, 400, fmt.Sprintf("invalid CEL expression: %s", err.Error()))
		return
	}

	// Set defaults and normalize scope/scope_id
	scope := req.Scope
	if scope == "" {
		scope = "global"
	}

	// Validate scope value before normalization
	if err := validateRoutingScope(scope); err != nil {
		SendError(ctx, 400, err.Error())
		return
	}

	// Validate: scope_id required for non-global scopes; must be nil/empty for global
	if scope == "global" {
		req.ScopeID = nil // normalize: global rules must not have scope_id
	} else if req.ScopeID == nil || *req.ScopeID == "" {
		SendError(ctx, 400, "scope_id field is required when scope is not global")
		return
	} else if err := h.validateRoutingScopeID(ctx, scope, *req.ScopeID); err != nil {
		sendRoutingScopeIDValidationError(ctx, err)
		return
	}

	// Build targets
	ruleID := uuid.NewString()
	targets := make([]configstoreTables.TableRoutingTarget, 0, len(req.Targets))
	for _, t := range req.Targets {
		targets = append(targets, configstoreTables.TableRoutingTarget{
			Provider: t.Provider,
			Model:    t.Model,
			KeyID:    t.KeyID,
			Weight:   t.Weight,
		})
	}

	// Create routing rule
	// Handle Enabled/ChainRule: nil means use DB default (true/false), otherwise use provided value
	enabled := req.Enabled
	if enabled == nil {
		enabled = bifrost.Ptr(true)
	}
	chainRule := false // DB default
	if req.ChainRule != nil {
		chainRule = *req.ChainRule
	}
	rule := &configstoreTables.TableRoutingRule{
		ID:              ruleID,
		Name:            req.Name,
		Description:     req.Description,
		Enabled:         enabled,
		ChainRule:       chainRule,
		CelExpression:   req.CelExpression,
		Targets:         targets,
		Scope:           scope,
		ScopeID:         req.ScopeID,
		Priority:        req.Priority,
		ParsedFallbacks: req.Fallbacks,
		ParsedQuery:     req.Query,
	}

	// Create in database
	if err := h.configStore.CreateRoutingRule(ctx, rule); err != nil {
		SendError(ctx, 500, fmt.Sprintf("Failed to create routing rule: %v", err))
		return
	}

	// Update in-memory store via manager callback
	if err := h.routingManager.ReloadRoutingRule(ctx, rule.ID); err != nil {
		SendError(ctx, 500, fmt.Sprintf("Failed to reload routing rule in memory: %v, please restart bifrost to sync with the database", err))
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"message": "Routing rule created successfully",
		"rule":    rule,
	})
}

// updateRoutingRule updates an existing routing rule
func (h *RoutingHandler) updateRoutingRule(ctx *fasthttp.RequestCtx) {
	ruleID := ctx.UserValue("rule_id").(string)

	// Parse request body
	var req UpdateRoutingRuleRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON")
		return
	}

	rule, err := h.configStore.GetRoutingRule(ctx, ruleID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Routing rule not found")
			return
		}
		logger.Error("failed to get routing rule: %v", err)
		SendError(ctx, 500, "Failed to retrieve routing rule")
		return
	}

	// Update fields if provided
	if req.Name != nil && *req.Name != "" {
		rule.Name = *req.Name
	}
	if req.Description != nil {
		rule.Description = *req.Description
	}
	if req.Enabled != nil {
		rule.Enabled = req.Enabled
	}
	if req.ChainRule != nil {
		rule.ChainRule = *req.ChainRule
	}
	if req.CelExpression != nil {
		// Validate only when the field is supplied, so unrelated updates (e.g. toggling
		// enabled) never start failing on a pre-existing malformed expression.
		if err := rules.ValidateCELExpression(*req.CelExpression); err != nil {
			SendError(ctx, 400, fmt.Sprintf("invalid CEL expression: %s", err.Error()))
			return
		}
		rule.CelExpression = *req.CelExpression
	}
	if req.Targets != nil {
		if len(req.Targets) == 0 {
			SendError(ctx, 400, "at least one routing target is required")
			return
		}
		if err := validateRoutingTargets(req.Targets); err != nil {
			SendError(ctx, 400, err.Error())
			return
		}
		newTargets := make([]configstoreTables.TableRoutingTarget, 0, len(req.Targets))
		for _, t := range req.Targets {
			newTargets = append(newTargets, configstoreTables.TableRoutingTarget{
				Provider: t.Provider,
				Model:    t.Model,
				KeyID:    t.KeyID,
				Weight:   t.Weight,
			})
		}
		rule.Targets = newTargets
	}
	if req.Priority != nil {
		rule.Priority = *req.Priority
	}
	if req.Query != nil {
		rule.ParsedQuery = req.Query
	}
	if req.Fallbacks != nil {
		if err := validateRoutingFallbacks(req.Fallbacks); err != nil {
			SendError(ctx, 400, err.Error())
			return
		}
		rule.ParsedFallbacks = req.Fallbacks
	}
	if req.Scope != nil && *req.Scope != "" {
		// Validate scope value before updating
		if err := validateRoutingScope(*req.Scope); err != nil {
			SendError(ctx, 400, err.Error())
			return
		}
		rule.Scope = *req.Scope
	}
	if req.ScopeID != nil {
		rule.ScopeID = req.ScopeID
	}

	// If scope is global, ensure scope_id is nil
	if rule.Scope == "global" {
		rule.ScopeID = nil
	} else if rule.ScopeID == nil || *rule.ScopeID == "" {
		SendError(ctx, 400, "scope_id field is required when scope is not global")
		return
	} else if req.Scope != nil || req.ScopeID != nil {
		// Only re-validate when scope or scope_id actually changed in this request;
		// avoids re-checking on every unrelated update (e.g. toggling enabled).
		if err := h.validateRoutingScopeID(ctx, rule.Scope, *rule.ScopeID); err != nil {
			sendRoutingScopeIDValidationError(ctx, err)
			return
		}
	}

	// Update in database
	if err := h.configStore.UpdateRoutingRule(ctx, rule); err != nil {
		SendError(ctx, 500, fmt.Sprintf("Failed to update routing rule in database: %v", err))
		return
	}

	// Update in-memory store via manager callback
	if err := h.routingManager.ReloadRoutingRule(ctx, rule.ID); err != nil {
		SendError(ctx, 500, fmt.Sprintf("Failed to reload routing rule in memory: %v, please restart bifrost to sync with the database", err))
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"message": "Routing rule updated successfully",
		"rule":    rule,
	})
}

// deleteRoutingRule deletes a routing rule
func (h *RoutingHandler) deleteRoutingRule(ctx *fasthttp.RequestCtx) {
	ruleID := ctx.UserValue("rule_id").(string)

	// Delete from database
	if err := h.configStore.DeleteRoutingRule(ctx, ruleID); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Routing rule not found")
			return
		}
		SendError(ctx, 500, fmt.Sprintf("Failed to delete routing rule from database: %v", err))
		return
	}

	// Remove from in-memory store via manager callback (non-fatal: DB already updated)
	if err := h.routingManager.RemoveRoutingRule(ctx, ruleID); err != nil {
		logger.Error("failed to remove routing rule from memory: %v", err)
	}

	SendJSON(ctx, map[string]interface{}{
		"message": "Routing rule deleted successfully",
	})
}
