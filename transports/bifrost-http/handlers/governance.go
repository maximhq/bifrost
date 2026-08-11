// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains all governance management functionality including CRUD operations for VKs, Rules, and configs.
package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/plugins/governance/complexity"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// dbForUpdate adds a PostgreSQL row-level update lock to the query.
func dbForUpdate(db *gorm.DB) *gorm.DB {
	if db.Dialector.Name() != "postgres" {
		return db
	}
	return db.Clauses(clause.Locking{Strength: "UPDATE"})
}

// GovernanceManager is the interface for the governance manager
type GovernanceManager interface {
	GetGovernanceData(ctx context.Context) *governance.GovernanceData
	ReloadVirtualKey(ctx context.Context, id string) (*configstoreTables.TableVirtualKey, error)
	// ResetBudgetUsageInMemory clears usage for the given budgets in the store that
	// enforces spend, leaving each reset boundary untouched. Enterprise also
	// propagates the reset to cluster peers, which is what owner addresses.
	ResetBudgetUsageInMemory(ctx context.Context, owner BudgetUsageResetOwner, budgetIDs []string) error
	// AdoptCalendarAlignmentInMemory re-anchors the given budgets and rate limit
	// onto the calendar boundaries they have just been told to follow, preserving
	// their usage. Called only on the transition from rolling to calendar-aligned;
	// without it that switch clears the usage of every window that opened before
	// the current boundary.
	AdoptCalendarAlignmentInMemory(ctx context.Context, owner BudgetUsageResetOwner, budgetIDs []string, rateLimitIDs []string) error
	RemoveVirtualKey(ctx context.Context, id string) error
	ReloadTeam(ctx context.Context, id string) (*configstoreTables.TableTeam, error)
	RemoveTeam(ctx context.Context, id string) error
	ReloadCustomer(ctx context.Context, id string) (*configstoreTables.TableCustomer, error)
	RemoveCustomer(ctx context.Context, id string) error
	ReloadModelConfig(ctx context.Context, id string) (*configstoreTables.TableModelConfig, error)
	RemoveModelConfig(ctx context.Context, id string) error
	ReloadProvider(ctx context.Context, provider schemas.ModelProvider) (*configstoreTables.TableProvider, error)
	RemoveProvider(ctx context.Context, provider schemas.ModelProvider) error
	ReloadRoutingRule(ctx context.Context, id string) error
	RemoveRoutingRule(ctx context.Context, id string) error
	UpsertPricingOverride(ctx context.Context, override *configstoreTables.TablePricingOverride) error
	DeletePricingOverride(ctx context.Context, id string) error
}

// BudgetUsageResetOwner identifies the entity whose budgets had their usage reset.
//
// Enterprise needs this to address the cluster broadcast: a peer has to reload the
// right entity and then apply the reset, because a plain reload deliberately
// preserves that peer's cached usage. Kind deliberately uses the same strings as
// the enterprise cluster entity types ("virtual_key", "team", "customer",
// "model_config", "provider") so the mapping is a direct conversion rather than a
// lookup table that can drift.
type BudgetUsageResetOwner struct {
	Kind string
	ID   string
}

// Budget owner kinds, matching the enterprise cluster entity type values.
const (
	BudgetOwnerVirtualKey  = "virtual_key"
	BudgetOwnerTeam        = "team"
	BudgetOwnerCustomer    = "customer"
	BudgetOwnerModelConfig = "model_config"
)

type complexityAnalyzerConfigReloader interface {
	// HTTP server bridge signature: BifrostHTTPServer implements this and adapts
	// to the governance plugin's in-memory ReloadComplexityAnalyzerConfig(config).
	ReloadComplexityAnalyzerConfig(ctx context.Context, config *complexity.AnalyzerConfig) error
}

// GovernanceHandler manages HTTP requests for governance operations
// ScopeNameResolver returns the human-readable name for a non-global model
// config scope target (e.g. a virtual key's Name given its ID). The second
// return value is false when no name could be resolved; the UI then falls
// back to rendering the raw scope_id. Implementations must be safe to call
// concurrently.
type ScopeNameResolver func(ctx context.Context, scopeID string) (string, bool)

// scopeNameResolvers is the package-level registry consulted by
// resolveModelConfigScopeName. OSS seeds it (virtual_key) the first time a
// GovernanceHandler is constructed; downstream builds extend it via
// RegisterScopeNameResolver at startup. Guarded by scopeNameResolversMu.
var (
	scopeNameResolversMu sync.RWMutex
	scopeNameResolvers   = map[string]ScopeNameResolver{}
)

// RegisterScopeNameResolver wires a resolver for a model_config scope value.
// Intended to be called once at process startup, before serving requests
// (e.g. an enterprise build registering a "user" resolver). Overwrites any
// previously registered resolver for the same scope. Safe to call
// concurrently.
func RegisterScopeNameResolver(scope string, fn ScopeNameResolver) {
	if scope == "" || fn == nil {
		return
	}
	scopeNameResolversMu.Lock()
	scopeNameResolvers[scope] = fn
	scopeNameResolversMu.Unlock()
}

func lookupScopeNameResolver(scope string) (ScopeNameResolver, bool) {
	scopeNameResolversMu.RLock()
	defer scopeNameResolversMu.RUnlock()
	fn, ok := scopeNameResolvers[scope]
	return fn, ok
}

// ExternalQuotaBudgetResolver returns budgets that govern a VK but whose usage is
// tracked OUTSIDE the VK's own budget rows
type ExternalQuotaBudgetResolver func(ctx context.Context, vk *configstoreTables.TableVirtualKey) (*ExternalQuotaBudgetResult, error)

// ExternalQuotaBudgetResult is what an external resolver returns for a VK whose
// authoritative usage lives outside its own budget rows.
type ExternalQuotaBudgetResult struct {
	// Budgets replaces the VK's own budget rows in the quota response.
	Budgets []configstoreTables.TableBudget
	// RateLimit, when non-nil, replaces the VK's own rate-limit row (enterprise:
	// the access-profile rate limit that carries the real usage for an AP-managed VK).
	RateLimit *configstoreTables.TableRateLimit
	// Managed reports that the VK is access-profile-managed. Set independently of
	// Budgets/RateLimit so the flag reaches the response even when the profile has
	// neither — it drives the managed-key UI (lock + notice).
	Managed bool
	// UsageUserID, when non-empty, scopes the per_model_usage query to this user id
	// instead of the VK id.
	UsageUserID string
}

type GovernanceHandler struct {
	configStore       configstore.ConfigStore
	governanceManager GovernanceManager
	// logManager sources actual per-model usage (from request logs) for the quota
	// endpoint's model_usage breakdown. Optional: nil when the logging plugin is
	// not enabled, in which case the breakdown is simply omitted.
	logManager logging.LogManager
	// externalQuotaBudgetResolver, when non-nil, supplies budgets that govern a VK
	// but whose usage lives outside the VK's own budget rows (enterprise
	// access-profile-managed VKs). Injected at construction; nil on OSS builds.
	externalQuotaBudgetResolver ExternalQuotaBudgetResolver
}

// GovernanceRouteRegistrar registers one replaceable governance route family.
type GovernanceRouteRegistrar func(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware)

// GovernanceTeamRouteOverrides replaces selected Team handlers and registers edition-specific extensions.
type GovernanceTeamRouteOverrides struct {
	List       fasthttp.RequestHandler
	Get        fasthttp.RequestHandler
	Create     fasthttp.RequestHandler
	Update     fasthttp.RequestHandler
	Delete     fasthttp.RequestHandler
	Extensions GovernanceRouteRegistrar
}

// GovernanceRouteOverrides allows downstream editions to own selected governance route families.
type GovernanceRouteOverrides struct {
	Teams *GovernanceTeamRouteOverrides
}

// NewGovernanceHandler creates a new governance handler instance.
// logManager is optional (may be nil); when supplied it powers the quota
// endpoint's per-budget actual per-model usage breakdown.
// externalQuotaBudgetResolver is optional (may be nil); when supplied the quota
// endpoint uses it to resolve budgets/usage for VKs whose authoritative usage
// is tracked outside their own budget rows.
// Side effect: ensures the default virtual_key scope-name resolver is
// registered against the supplied configStore, so resolveModelConfigScopeName
// can render VK names for OSS-only builds without further wiring.
func NewGovernanceHandler(manager GovernanceManager, configStore configstore.ConfigStore, logManager logging.LogManager, externalQuotaBudgetResolver ExternalQuotaBudgetResolver) (*GovernanceHandler, error) {
	if manager == nil {
		return nil, fmt.Errorf("governance manager is required")
	}
	if configStore == nil {
		return nil, fmt.Errorf("config store is required")
	}
	RegisterScopeNameResolver(configstoreTables.ModelConfigScopeVirtualKey, func(ctx context.Context, scopeID string) (string, bool) {
		vk, err := configStore.GetVirtualKey(ctx, scopeID)
		if err != nil || vk == nil {
			return "", false
		}
		return vk.Name, true
	})
	return &GovernanceHandler{
		governanceManager:           manager,
		configStore:                 configStore,
		logManager:                  logManager,
		externalQuotaBudgetResolver: externalQuotaBudgetResolver,
	}, nil
}

// CreateVirtualKeyRequest represents the request body for creating a virtual key
type CreateVirtualKeyRequest struct {
	Name            string `json:"name" validate:"required"`
	Description     string `json:"description,omitempty"`
	ProviderConfigs []struct {
		Provider          string                  `json:"provider" validate:"required"`
		Weight            *float64                `json:"weight,omitempty"`
		AllowedModels     schemas.WhiteList       `json:"allowed_models,omitempty"`     // ["*"] allows all models; empty denies all
		BlacklistedModels schemas.BlackList       `json:"blacklisted_models,omitempty"` // ["*"] blocks all models; empty blocks none
		Budgets           []CreateBudgetRequest   `json:"budgets,omitempty"`            // Multi-budget for provider config
		RateLimit         *CreateRateLimitRequest `json:"rate_limit,omitempty"`         // Provider-level rate limit
		KeyIDs            schemas.WhiteList       `json:"key_ids,omitempty"`            // List of DBKey UUIDs to associate with this provider config
	} `json:"provider_configs,omitempty"` // Empty means no providers allowed (deny-by-default)
	MCPConfigs []struct {
		MCPClientName  string            `json:"mcp_client_name" validate:"required"`
		ToolsToExecute schemas.WhiteList `json:"tools_to_execute,omitempty"`
	} `json:"mcp_configs,omitempty"` // Empty means no MCP clients allowed (deny-by-default)
	TeamID          *string                 `json:"team_id,omitempty"`     // Mutually exclusive with CustomerID
	CustomerID      *string                 `json:"customer_id,omitempty"` // Mutually exclusive with TeamID
	Budgets         []CreateBudgetRequest   `json:"budgets,omitempty"`     // Multi-budget: each must have a unique reset_duration
	RateLimit       *CreateRateLimitRequest `json:"rate_limit,omitempty"`
	IsActive        *bool                   `json:"is_active,omitempty"`
	CalendarAligned bool                    `json:"calendar_aligned,omitempty"` // When true, all budgets reset at clean calendar boundaries
	ExpiresAt       *time.Time              `json:"expires_at,omitempty"`       // Optional expiry; nil means never expires
}

// UpdateVirtualKeyRequest represents the request body for updating a virtual key
type UpdateVirtualKeyRequest struct {
	Name            *string `json:"name,omitempty"`
	Description     *string `json:"description,omitempty"`
	ProviderConfigs []struct {
		ID                *uint                   `json:"id,omitempty"` // null for new entries
		Provider          string                  `json:"provider" validate:"required"`
		Weight            *float64                `json:"weight,omitempty"`
		AllowedModels     schemas.WhiteList       `json:"allowed_models,omitempty"`     // ["*"] allows all models; empty denies all
		BlacklistedModels schemas.BlackList       `json:"blacklisted_models,omitempty"` // ["*"] blocks all models; empty blocks none
		Budgets           []CreateBudgetRequest   `json:"budgets,omitempty"`            // Multi-budget for provider config
		RateLimit         *UpdateRateLimitRequest `json:"rate_limit,omitempty"`         // Provider-level rate limit
		KeyIDs            schemas.WhiteList       `json:"key_ids,omitempty"`            // List of DBKey UUIDs to associate with this provider config
	} `json:"provider_configs,omitempty"`
	MCPConfigs []struct {
		ID             *uint             `json:"id,omitempty"` // null for new entries
		MCPClientName  string            `json:"mcp_client_name" validate:"required"`
		ToolsToExecute schemas.WhiteList `json:"tools_to_execute,omitempty"`
	} `json:"mcp_configs,omitempty"`
	TeamID           schemas.OptionalJSON[string] `json:"team_id,omitempty"`
	CustomerID       schemas.OptionalJSON[string] `json:"customer_id,omitempty"`
	Budgets          []CreateBudgetRequest        `json:"budgets,omitempty"` // Multi-budget: replaces all VK-level budgets
	RateLimit        *UpdateRateLimitRequest      `json:"rate_limit,omitempty"`
	IsActive         *bool                        `json:"is_active,omitempty"`
	CalendarAligned  *bool                        `json:"calendar_aligned,omitempty"` // When true, all budgets reset at clean calendar boundaries
	ResetBudgetUsage *bool                        `json:"reset_budget_usage,omitempty"`
	ExpiresAt        *string                      `json:"expires_at,omitempty"` // RFC3339 timestamp sets a new expiry, "" clears it, omitted leaves it unchanged
}

var errVirtualKeyDualAssociation = errors.New("VirtualKey cannot be attached to both Team and Customer")

// optionalJSONStringHasValue reports whether a presence-aware string contains a non-empty value.
func optionalJSONStringHasValue(value schemas.OptionalJSON[string]) bool {
	return value.Set && !value.Null && value.Value != ""
}

// applyVirtualKeyOwnershipUpdate applies presence-aware team/customer ownership changes.
func applyVirtualKeyOwnershipUpdate(vk *configstoreTables.TableVirtualKey, req *UpdateVirtualKeyRequest) error {
	if optionalJSONStringHasValue(req.TeamID) && optionalJSONStringHasValue(req.CustomerID) {
		return errVirtualKeyDualAssociation
	}
	if optionalJSONStringHasValue(req.TeamID) {
		vk.TeamID = new(req.TeamID.Value)
		vk.CustomerID = nil
		return nil
	}
	if optionalJSONStringHasValue(req.CustomerID) {
		vk.CustomerID = new(req.CustomerID.Value)
		vk.TeamID = nil
		return nil
	}
	if req.TeamID.Set || req.CustomerID.Set {
		vk.TeamID = nil
		vk.CustomerID = nil
	}
	return nil
}

type BulkRotateVirtualKeysRequest struct {
	IDs []string `json:"ids"`
}

// CreateBudgetRequest represents the request body for creating a budget
type CreateBudgetRequest struct {
	ID            string  `json:"id,omitempty"`
	MaxLimit      float64 `json:"max_limit" validate:"required"`      // Maximum budget in dollars
	ResetDuration string  `json:"reset_duration" validate:"required"` // e.g., "30s", "5m", "1h", "1d", "1w", "1M", "1Q"
	// ResetConfig carries window settings the duration cannot express, currently
	// the fiscal quarter start. Only valid on a quarterly reset duration.
	ResetConfig *configstoreTables.BudgetResetConfig `json:"reset_config,omitempty"`
}

// UpdateBudgetRequest represents the request body for updating a budget
type UpdateBudgetRequest struct {
	MaxLimit      *float64                             `json:"max_limit,omitempty"`
	ResetDuration *string                              `json:"reset_duration,omitempty"`
	ResetConfig   *configstoreTables.BudgetResetConfig `json:"reset_config,omitempty"`
}

// BudgetOverrideRequest replaces the active override on one budget.
type BudgetOverrideRequest struct {
	Amount float64                              `json:"amount"`
	Mode   configstoreTables.BudgetOverrideMode `json:"mode"`
	Cycles int                                  `json:"cycles,omitempty"`
}

// BudgetOverrideResponse returns the persisted budget and its additive effective limit.
type BudgetOverrideResponse struct {
	Budget            *configstoreTables.TableBudget `json:"budget"`
	EffectiveMaxLimit float64                        `json:"effective_max_limit"`
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

// CreateRateLimitRequest represents the request body for creating a rate limit using flexible approach
type CreateRateLimitRequest struct {
	TokenMaxLimit        *int64  `json:"token_max_limit,omitempty"`        // Maximum tokens allowed
	TokenResetDuration   *string `json:"token_reset_duration,omitempty"`   // e.g., "30s", "5m", "1h", "1d", "1w", "1M"
	RequestMaxLimit      *int64  `json:"request_max_limit,omitempty"`      // Maximum requests allowed
	RequestResetDuration *string `json:"request_reset_duration,omitempty"` // e.g., "30s", "5m", "1h", "1d", "1w", "1M"
}

// UpdateRateLimitRequest represents the request body for updating a rate limit using flexible approach
type UpdateRateLimitRequest struct {
	TokenMaxLimit        *int64  `json:"token_max_limit,omitempty"`        // Maximum tokens allowed
	TokenResetDuration   *string `json:"token_reset_duration,omitempty"`   // e.g., "30s", "5m", "1h", "1d", "1w", "1M"
	RequestMaxLimit      *int64  `json:"request_max_limit,omitempty"`      // Maximum requests allowed
	RequestResetDuration *string `json:"request_reset_duration,omitempty"` // e.g., "30s", "5m", "1h", "1d", "1w", "1M"
}

func isBudgetRemovalRequest(req *UpdateBudgetRequest) bool {
	return req != nil && req.MaxLimit == nil && req.ResetDuration == nil
}

// budgetLastReset returns the appropriate LastReset for a new budget.
// When calendarAligned is true it snaps to the start of the current calendar period
// (e.g. midnight on the 1st of the month for "1M"), otherwise it returns time.Now().
//
// Takes the budget rather than a bare duration string so a quarterly window snaps
// to the budget's own fiscal quarter. Passing only the duration would silently
// produce January quarters for a budget configured to start in any other month.
func budgetLastReset(calendarAligned bool, budget *configstoreTables.TableBudget) time.Time {
	if calendarAligned && budget != nil {
		return configstoreTables.GetCalendarPeriodStart(budget.ResetDuration, time.Now(), budget.QuarterStartMonth())
	}
	return time.Now()
}

// newBudgetFromRequest builds a fresh budget row from a create request, snapping
// LastReset to the owner's current calendar period when the owner is
// calendar-aligned. The caller sets the owner FK on the returned value.
//
// Every reconciler shares this one constructor rather than repeating the struct
// literal. Five near-identical literals is how a newly added field gets carried
// at four sites and quietly dropped at the fifth, and for a budget that failure
// is invisible: the row saves, the API returns it, and only the reset boundary
// is wrong.
func newBudgetFromRequest(req CreateBudgetRequest, calendarAligned bool) configstoreTables.TableBudget {
	budget := configstoreTables.TableBudget{
		ID:            uuid.NewString(),
		MaxLimit:      req.MaxLimit,
		ResetDuration: req.ResetDuration,
		ResetConfig:   req.ResetConfig,
		CurrentUsage:  0,
	}
	budget.LastReset = budgetLastReset(calendarAligned, &budget)
	return budget
}

// applyResetConfigToExistingBudget copies a request's quarter definition onto an
// existing budget. The quarter start is configuration, so the request wins,
// exactly as it does for max_limit and reset_duration.
//
// It deliberately does NOT move LastReset onto the new fiscal boundary, for two
// reasons. UpdateBudget cannot persist such a move: it carries CurrentUsage and
// LastReset forward from the stored row on every config write, so that a
// config.json force-sync cannot replay stale zeroes over live accounting. Any
// boundary written here would be silently discarded, which is worse than not
// writing it.
//
// And it is unnecessary, because the reset path converges on its own. A budget
// is due when WindowStart(now) is after LastReset, and WindowStart already reads
// the new quarter start the moment this config lands. Moving a January-start
// budget to a February start on 9 August leaves LastReset on 1 July while
// WindowStart becomes 1 August, so the next reset tick - at most ten seconds
// later - resets it and stamps the new boundary through the runtime-owned path
// that is allowed to move it. Usage is zeroed by that reset, which is the honest
// outcome: under the new calendar the quarter genuinely began on 1 August.
func applyResetConfigToExistingBudget(budget *configstoreTables.TableBudget, req CreateBudgetRequest) {
	budget.ResetConfig = req.ResetConfig
}

func resetBudgetUsageIfRequested(budget *configstoreTables.TableBudget, reset bool, calendarAligned bool) {
	if !reset {
		return
	}
	budget.CurrentUsage = 0
	budget.LastReset = budgetLastReset(calendarAligned, budget)
}

func compareBudgetRequestDurations(left, right CreateBudgetRequest) bool {
	leftDuration, leftErr := configstoreTables.ParseDuration(left.ResetDuration)
	rightDuration, rightErr := configstoreTables.ParseDuration(right.ResetDuration)
	if leftErr == nil && rightErr == nil && leftDuration != rightDuration {
		return leftDuration < rightDuration
	}
	return left.ResetDuration < right.ResetDuration
}

func inheritUsageFromClosestShorterBudget(budget *configstoreTables.TableBudget, existing []configstoreTables.TableBudget, reset bool) {
	if reset {
		return
	}
	targetDuration, err := configstoreTables.ParseDuration(budget.ResetDuration)
	if err != nil {
		return
	}

	var closest *configstoreTables.TableBudget
	var closestDuration time.Duration
	for i := range existing {
		candidate := &existing[i]
		candidateDuration, err := configstoreTables.ParseDuration(candidate.ResetDuration)
		if err != nil || candidateDuration >= targetDuration {
			continue
		}
		if closest == nil || candidateDuration > closestDuration {
			closest = candidate
			closestDuration = candidateDuration
		}
	}
	if closest == nil {
		return
	}
	budget.CurrentUsage = closest.CurrentUsage
}

// buildBudgetLookup builds ID- and duration-keyed maps of existing budgets for
// the reconciliation pass. Rows whose ID is explicitly claimed by an
// ID-specified entry in requests are omitted from byDuration so a
// duration-only entry that sorts earlier cannot steal the row reserved for an
// ID-based rename (e.g. payload [{new 1d}, {ID:X→1w}] against existing
// {ID:X, "1d"}).
func buildBudgetLookup(existing []configstoreTables.TableBudget, requests []CreateBudgetRequest) (map[string]configstoreTables.TableBudget, map[string]configstoreTables.TableBudget) {
	claimedIDs := make(map[string]struct{}, len(requests))
	for _, r := range requests {
		if r.ID != "" {
			claimedIDs[r.ID] = struct{}{}
		}
	}
	byID := make(map[string]configstoreTables.TableBudget, len(existing))
	byDuration := make(map[string]configstoreTables.TableBudget, len(existing))
	for _, budget := range existing {
		if budget.ID != "" {
			byID[budget.ID] = budget
		}
		if _, claimed := claimedIDs[budget.ID]; claimed {
			continue
		}
		byDuration[budget.ResetDuration] = budget
	}
	return byID, byDuration
}

func findExistingBudget(request CreateBudgetRequest, byID map[string]configstoreTables.TableBudget, byDuration map[string]configstoreTables.TableBudget) (configstoreTables.TableBudget, bool, error) {
	if request.ID != "" {
		existing, found := byID[request.ID]
		if !found {
			return configstoreTables.TableBudget{}, false, &badRequestError{err: fmt.Errorf("budget %s does not belong to this entity", request.ID)}
		}
		// Consume the matched row from both maps so a later iteration cannot
		// reuse it (e.g. renaming an existing budget by ID while the same
		// payload adds a new budget with the old duration).
		delete(byID, existing.ID)
		delete(byDuration, existing.ResetDuration)
		return existing, true, nil
	}
	existing, found := byDuration[request.ResetDuration]
	if found {
		delete(byID, existing.ID)
		delete(byDuration, existing.ResetDuration)
	}
	return existing, found, nil
}

// coerceLegacyBudget converts a single UpdateBudgetRequest into a *[]CreateBudgetRequest
// so it can be handled uniformly via reconcileModelConfigBudgets. Returns nil when the
// request carries no actionable change (e.g. only one field set but no existing budget to
// merge with, leaving the budget list unchanged).
func coerceLegacyBudget(req *UpdateBudgetRequest, existing *configstoreTables.TableBudget) *[]CreateBudgetRequest {
	if isBudgetRemovalRequest(req) {
		empty := []CreateBudgetRequest{}
		return &empty
	}
	b := CreateBudgetRequest{}
	if existing != nil {
		b.ID = existing.ID
		b.MaxLimit = existing.MaxLimit
		b.ResetDuration = existing.ResetDuration
	}
	if req.MaxLimit != nil {
		b.MaxLimit = *req.MaxLimit
	}
	if req.ResetDuration != nil {
		b.ResetDuration = *req.ResetDuration
	}
	if b.MaxLimit == 0 || b.ResetDuration == "" {
		return nil
	}
	result := []CreateBudgetRequest{b}
	return &result
}

func isRateLimitRemovalRequest(req *UpdateRateLimitRequest) bool {
	return req != nil && req.TokenMaxLimit == nil && req.RequestMaxLimit == nil &&
		req.TokenResetDuration == nil && req.RequestResetDuration == nil
}

// budgetUsageReset carries an operator's explicit "reset usage" choice through
// budget reconciliation and collects the budgets it was applied to.
//
// Two things make this necessary rather than a plain bool. UpdateBudget cannot
// clear usage - it copies CurrentUsage and LastReset back from the stored row on
// every config write, deliberately, so a config.json sync cannot replay stale
// values over live accounting - so the reset has to go through UpdateBudgetUsage,
// the store method that owns that column. And the in-memory governance store is
// what actually enforces spend, so the caller needs the list of budgets to clear
// there once the transaction has committed.
//
// A nil pointer means the caller is not resetting anything.
type budgetUsageReset struct {
	requested bool
	budgetIDs []string
}

// apply zeroes one budget's persisted usage and records it for the in-memory
// pass. No-op when the operator did not ask for a reset.
func (r *budgetUsageReset) apply(ctx context.Context, store configstore.ConfigStore, budgetID string, tx *gorm.DB) error {
	if r == nil || !r.requested {
		return nil
	}
	if err := store.UpdateBudgetUsage(ctx, budgetID, 0, tx); err != nil {
		return fmt.Errorf("failed to reset usage for budget %s: %w", budgetID, err)
	}
	r.budgetIDs = append(r.budgetIDs, budgetID)
	return nil
}

// adoptCalendarAlignment re-anchors an owner's open windows after calendar
// alignment was switched on, and is a no-op on every other write.
//
// The switch is otherwise destructive. The reset sweep treats a window as due
// whenever its boundary is later than its last reset, and a freshly aligned window
// is measured against the most recent calendar boundary, so anything that opened
// before that boundary is due immediately and its usage is cleared on the next
// tick. For a monthly budget that is most of the month.
//
// Adoption moves each window's boundary forward onto the grid instead, which the
// forward-only guard permits, so the window reads as current and its usage
// survives until the next real boundary. Every window is judged on its own
// duration, so a monthly budget can adopt while an hourly counter beside it stays
// rolling untouched.
//
// Runs after the in-memory reload, for the same reason the usage reset does: the
// reload carries cached values forward and would otherwise overwrite the change.
func (h *GovernanceHandler) adoptCalendarAlignment(ctx context.Context, switchedOn bool, owner BudgetUsageResetOwner, budgets []configstoreTables.TableBudget, rateLimitID *string) error {
	if !switchedOn {
		return nil
	}
	budgetIDs := make([]string, 0, len(budgets))
	for _, budget := range budgets {
		budgetIDs = append(budgetIDs, budget.ID)
	}
	var rateLimitIDs []string
	if rateLimitID != nil && *rateLimitID != "" {
		rateLimitIDs = append(rateLimitIDs, *rateLimitID)
	}
	return h.governanceManager.AdoptCalendarAlignmentInMemory(ctx, owner, budgetIDs, rateLimitIDs)
}

// reconcileModelConfigBudgets upserts the desired set of budgets owned by a model config
// (via TableBudget.ModelConfigID), preserving usage on matched rows and deleting removed
// ones. It mutates mc.Budgets to the reconciled set. The model config row must already
// exist (callers create it first). Mirrors the VK/team multi-budget reconciliation.
func (h *GovernanceHandler) reconcileModelConfigBudgets(ctx context.Context, tx *gorm.DB, mc *configstoreTables.TableModelConfig, requests []CreateBudgetRequest, usageReset *budgetUsageReset) error {
	seenDurations := make(map[string]bool, len(requests))
	for _, b := range requests {
		if b.MaxLimit < 0 {
			return &badRequestError{err: fmt.Errorf("budget max_limit cannot be negative: %.2f", b.MaxLimit)}
		}
		if d, err := configstoreTables.ParseDuration(b.ResetDuration); err != nil || d <= 0 {
			return &badRequestError{err: fmt.Errorf("invalid reset duration (must be a positive duration): %s", b.ResetDuration)}
		}
		if seenDurations[b.ResetDuration] {
			return &badRequestError{err: fmt.Errorf("duplicate reset_duration in budgets: %s", b.ResetDuration)}
		}
		seenDurations[b.ResetDuration] = true
	}

	existingByID, existingByDuration := buildBudgetLookup(mc.Budgets, requests)
	var reconciled []configstoreTables.TableBudget
	matchedIDs := make(map[string]bool)
	for _, b := range requests {
		existing, found, err := findExistingBudget(b, existingByID, existingByDuration)
		if err != nil {
			return err
		}
		if found {
			existing.MaxLimit = b.MaxLimit
			existing.ResetDuration = b.ResetDuration
			applyResetConfigToExistingBudget(&existing, b)
			if err := validateBudget(&existing); err != nil {
				return err
			}
			if err := h.configStore.UpdateBudget(ctx, &existing, tx); err != nil {
				return err
			}
			// Usage is runtime-owned, so UpdateBudget above cannot clear it. Route
			// the operator's explicit choice through the method that owns the column.
			if err := usageReset.apply(ctx, h.configStore, existing.ID, tx); err != nil {
				return err
			}
			if usageReset != nil && usageReset.requested {
				existing.CurrentUsage = 0
			}
			reconciled = append(reconciled, existing)
			matchedIDs[existing.ID] = true
		} else {
			budget := newBudgetFromRequest(b, mc.CalendarAligned)
			budget.ModelConfigID = &mc.ID
			inheritUsageFromClosestShorterBudget(&budget, mc.Budgets, false)
			if err := validateBudget(&budget); err != nil {
				return err
			}
			if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
				return err
			}
			reconciled = append(reconciled, budget)
		}
	}
	// Delete budgets no longer present.
	for _, existing := range mc.Budgets {
		if !matchedIDs[existing.ID] {
			if err := h.configStore.DeleteBudget(ctx, existing.ID, tx); err != nil {
				return fmt.Errorf("failed to delete removed model config budget: %w", err)
			}
		}
	}
	mc.Budgets = reconciled
	return nil
}

// reconcileCustomerBudgets upserts the desired set of budgets owned by a customer
// (via TableBudget.CustomerID), preserving usage on matched rows and deleting removed ones.
// It mutates customer.Budgets to the reconciled set. Mirrors reconcileModelConfigBudgets.
func (h *GovernanceHandler) reconcileCustomerBudgets(ctx context.Context, tx *gorm.DB, customer *configstoreTables.TableCustomer, requests []CreateBudgetRequest, usageReset *budgetUsageReset) error {
	seenDurations := make(map[string]bool, len(requests))
	for _, b := range requests {
		if b.MaxLimit < 0 {
			return &badRequestError{err: fmt.Errorf("budget max_limit cannot be negative: %.2f", b.MaxLimit)}
		}
		if d, err := configstoreTables.ParseDuration(b.ResetDuration); err != nil || d <= 0 {
			return &badRequestError{err: fmt.Errorf("invalid reset duration (must be a positive duration): %s", b.ResetDuration)}
		}
		if seenDurations[b.ResetDuration] {
			return &badRequestError{err: fmt.Errorf("duplicate reset_duration in budgets: %s", b.ResetDuration)}
		}
		seenDurations[b.ResetDuration] = true
	}

	existingByID, existingByDuration := buildBudgetLookup(customer.Budgets, requests)
	var reconciled []configstoreTables.TableBudget
	matchedIDs := make(map[string]bool)
	for _, b := range requests {
		existing, found, err := findExistingBudget(b, existingByID, existingByDuration)
		if err != nil {
			return err
		}
		if found {
			existing.MaxLimit = b.MaxLimit
			existing.ResetDuration = b.ResetDuration
			applyResetConfigToExistingBudget(&existing, b)
			if err := validateBudget(&existing); err != nil {
				return err
			}
			if err := h.configStore.UpdateBudget(ctx, &existing, tx); err != nil {
				return err
			}
			if err := usageReset.apply(ctx, h.configStore, existing.ID, tx); err != nil {
				return err
			}
			if usageReset != nil && usageReset.requested {
				existing.CurrentUsage = 0
			}
			reconciled = append(reconciled, existing)
			matchedIDs[existing.ID] = true
		} else {
			cid := customer.ID
			budget := newBudgetFromRequest(b, customer.CalendarAligned)
			budget.CustomerID = &cid
			inheritUsageFromClosestShorterBudget(&budget, customer.Budgets, false)
			if err := validateBudget(&budget); err != nil {
				return err
			}
			if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
				return err
			}
			reconciled = append(reconciled, budget)
		}
	}
	for _, existing := range customer.Budgets {
		if !matchedIDs[existing.ID] {
			if err := h.configStore.DeleteBudget(ctx, existing.ID, tx); err != nil {
				return fmt.Errorf("failed to delete removed customer budget: %w", err)
			}
		}
	}
	customer.Budgets = reconciled
	return nil
}

// vkModelConfigDesired is the desired governance state for one VK-scoped model config tier
// (provider=nil for the VK top-level, or a specific provider). The *Provided flags distinguish
// "leave unchanged" (false, used by partial VK updates) from "set to the given value" (true).
// The rateLimit carries only the limit/duration fields (no ID/usage).
type vkModelConfigDesired struct {
	provider          *string
	budgetsProvided   bool
	budgets           []CreateBudgetRequest
	rateLimitProvided bool
	rateLimitRemove   bool
	rateLimit         *configstoreTables.TableRateLimit
}

// syncVKGovernanceToModelConfigs folds a virtual key's governance (top-level + per-provider
// budgets/rate-limits) into VK-scoped model configs, the single source of truth. It reconciles
// the top-level and per-provider configs and removes configs for providers no longer configured.
// Must run inside the VK create/update transaction. reconcileProviders controls per-provider
// handling: true treats perProvider as the full desired set (reconciling and removing absent
// providers); false leaves all per-provider configs untouched (for a partial VK update that
// omits provider_configs).
func (h *GovernanceHandler) syncVKGovernanceToModelConfigs(ctx context.Context, tx *gorm.DB, vk *configstoreTables.TableVirtualKey, top vkModelConfigDesired, perProvider []vkModelConfigDesired, reconcileProviders bool, usageReset *budgetUsageReset) error {
	if err := h.reconcileVKModelConfig(ctx, tx, vk, top, usageReset); err != nil {
		return err
	}
	if !reconcileProviders {
		return nil
	}
	keep := make(map[string]bool, len(perProvider))
	for _, pg := range perProvider {
		if pg.provider == nil {
			continue
		}
		keep[*pg.provider] = true
		if err := h.reconcileVKModelConfig(ctx, tx, vk, pg, usageReset); err != nil {
			return err
		}
	}
	// Delete VK-scoped provider model configs whose provider is no longer configured.
	var existing []configstoreTables.TableModelConfig
	if err := tx.Preload("Budgets").
		Where("scope = ? AND scope_id = ? AND model_name = ? AND provider IS NOT NULL",
			configstoreTables.ModelConfigScopeVirtualKey, vk.ID, configstoreTables.ModelConfigAllModels).
		Find(&existing).Error; err != nil {
		return err
	}
	for i := range existing {
		mc := &existing[i]
		if mc.Provider != nil && !keep[*mc.Provider] {
			if err := h.deleteVKModelConfig(ctx, tx, mc); err != nil {
				return err
			}
		}
	}
	return nil
}

// reconcileVKModelConfig reconciles a single VK-scoped model config to the desired state.
func (h *GovernanceHandler) reconcileVKModelConfig(ctx context.Context, tx *gorm.DB, vk *configstoreTables.TableVirtualKey, d vkModelConfigDesired, usageReset *budgetUsageReset) error {
	q := tx.Preload("Budgets").Where("scope = ? AND scope_id = ? AND model_name = ?",
		configstoreTables.ModelConfigScopeVirtualKey, vk.ID, configstoreTables.ModelConfigAllModels)
	if d.provider == nil {
		q = q.Where("provider IS NULL")
	} else {
		q = q.Where("provider = ?", *d.provider)
	}
	var existingList []configstoreTables.TableModelConfig
	if err := q.Limit(1).Find(&existingList).Error; err != nil {
		return err
	}
	isNew := len(existingList) == 0

	var mc configstoreTables.TableModelConfig
	if isNew {
		mc = configstoreTables.TableModelConfig{
			ID:              uuid.NewString(),
			ModelName:       configstoreTables.ModelConfigAllModels,
			Provider:        d.provider,
			Scope:           configstoreTables.ModelConfigScopeVirtualKey,
			ScopeID:         &vk.ID,
			CalendarAligned: vk.CalendarAligned,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		}
	} else {
		mc = existingList[0]
		mc.CalendarAligned = vk.CalendarAligned // keep in sync with the owning VK
	}

	// Rate limit (mc references it via RateLimitID, so resolve before persisting the mc).
	var rateLimitIDToDelete string
	if d.rateLimitProvided {
		switch {
		case d.rateLimitRemove:
			if mc.RateLimitID != nil {
				rateLimitIDToDelete = *mc.RateLimitID
				mc.RateLimitID = nil
				mc.RateLimit = nil
			}
		case mc.RateLimitID != nil:
			rl := configstoreTables.TableRateLimit{}
			if err := tx.First(&rl, "id = ?", *mc.RateLimitID).Error; err != nil {
				return err
			}
			rl.TokenMaxLimit = d.rateLimit.TokenMaxLimit
			rl.TokenResetDuration = d.rateLimit.TokenResetDuration
			rl.RequestMaxLimit = d.rateLimit.RequestMaxLimit
			rl.RequestResetDuration = d.rateLimit.RequestResetDuration
			if err := validateRateLimit(&rl); err != nil {
				return err
			}
			if err := h.configStore.UpdateRateLimit(ctx, &rl, tx); err != nil {
				return err
			}
			mc.RateLimit = &rl
		default:
			rl := configstoreTables.TableRateLimit{
				ID:                   uuid.NewString(),
				TokenMaxLimit:        d.rateLimit.TokenMaxLimit,
				TokenResetDuration:   d.rateLimit.TokenResetDuration,
				RequestMaxLimit:      d.rateLimit.RequestMaxLimit,
				RequestResetDuration: d.rateLimit.RequestResetDuration,
				TokenLastReset:       time.Now(),
				RequestLastReset:     time.Now(),
			}
			if err := validateRateLimit(&rl); err != nil {
				return err
			}
			if err := h.configStore.CreateRateLimit(ctx, &rl, tx); err != nil {
				return err
			}
			mc.RateLimitID = &rl.ID
			mc.RateLimit = &rl
		}
	}

	// Resulting budget count: the desired set if provided, else the existing set.
	finalBudgetCount := len(mc.Budgets)
	if d.budgetsProvided {
		finalBudgetCount = len(d.budgets)
	}
	hasGovernance := mc.RateLimitID != nil || finalBudgetCount > 0

	if !hasGovernance {
		// No governance left → drop the model config (and its budgets) if it existed.
		if !isNew {
			for i := range mc.Budgets {
				if err := h.configStore.DeleteBudget(ctx, mc.Budgets[i].ID, tx); err != nil {
					return err
				}
			}
			if err := tx.Delete(&configstoreTables.TableModelConfig{}, "id = ?", mc.ID).Error; err != nil {
				return err
			}
		}
		if rateLimitIDToDelete != "" {
			if err := tx.Delete(&configstoreTables.TableRateLimit{}, "id = ?", rateLimitIDToDelete).Error; err != nil {
				return err
			}
		}
		return nil
	}

	// Persist the mc (create or update) before touching budgets, which FK to it.
	if isNew {
		if err := h.configStore.CreateModelConfig(ctx, &mc, tx); err != nil {
			return err
		}
	} else {
		mc.UpdatedAt = time.Now()
		if err := h.configStore.UpdateModelConfig(ctx, &mc, tx); err != nil {
			return err
		}
	}

	if d.budgetsProvided {
		if err := h.reconcileModelConfigBudgets(ctx, tx, &mc, d.budgets, usageReset); err != nil {
			return err
		}
	}

	if rateLimitIDToDelete != "" {
		if err := tx.Delete(&configstoreTables.TableRateLimit{}, "id = ?", rateLimitIDToDelete).Error; err != nil {
			return err
		}
	}
	return nil
}

// deleteVKModelConfig removes a VK-scoped model config and its owned
// budgets/rate-limit (used when a provider config is removed from the VK).
func (h *GovernanceHandler) deleteVKModelConfig(ctx context.Context, tx *gorm.DB, mc *configstoreTables.TableModelConfig) error {
	for i := range mc.Budgets {
		if err := h.configStore.DeleteBudget(ctx, mc.Budgets[i].ID, tx); err != nil {
			return err
		}
	}
	rlID := mc.RateLimitID
	if err := tx.Delete(&configstoreTables.TableModelConfig{}, "id = ?", mc.ID).Error; err != nil {
		return err
	}
	if rlID != nil {
		if err := tx.Delete(&configstoreTables.TableRateLimit{}, "id = ?", *rlID).Error; err != nil {
			return err
		}
	}
	return nil
}

// rateLimitFromRequestFields builds a transient TableRateLimit (limit/duration fields only)
// for the VK governance sync, from the shared rate-limit request field shape.
func rateLimitFromRequestFields(tokenMax *int64, tokenDur *string, reqMax *int64, reqDur *string) *configstoreTables.TableRateLimit {
	return &configstoreTables.TableRateLimit{
		TokenMaxLimit:        tokenMax,
		TokenResetDuration:   tokenDur,
		RequestMaxLimit:      reqMax,
		RequestResetDuration: reqDur,
	}
}

// vkModelConfigIndexKey builds a lookup key for a VK-scoped model config by scope target + provider.
func vkModelConfigIndexKey(scopeID string, provider *string) string {
	if provider == nil {
		return scopeID + "|"
	}
	return scopeID + "|" + *provider
}

// applyVKGovernanceFromModelConfigs repopulates a VK's (and each provider config's) budgets and
// rate-limit from the VK-scoped model configs that own them — for serialization only (so the VK
// sheet still renders the governance it edits). byKey is keyed by vkModelConfigIndexKey.
// The reverse of syncVKGovernanceToModelConfigs.
func applyVKGovernanceFromModelConfigs(vk *configstoreTables.TableVirtualKey, byKey map[string]*configstoreTables.TableModelConfig) {
	if mc := byKey[vkModelConfigIndexKey(vk.ID, nil)]; mc != nil {
		vk.Budgets = mc.Budgets
		vk.RateLimit = mc.RateLimit
		vk.RateLimitID = mc.RateLimitID
	}
	for i := range vk.ProviderConfigs {
		pc := &vk.ProviderConfigs[i]
		if mc := byKey[vkModelConfigIndexKey(vk.ID, &pc.Provider)]; mc != nil {
			pc.Budgets = mc.Budgets
			pc.RateLimit = mc.RateLimit
			pc.RateLimitID = mc.RateLimitID
		}
	}
}

// hydrateVKGovernance reverse-maps a single VK's governance from its VK-scoped model configs.
func (h *GovernanceHandler) hydrateVKGovernance(ctx context.Context, vk *configstoreTables.TableVirtualKey) {
	if vk == nil {
		return
	}
	byKey := make(map[string]*configstoreTables.TableModelConfig)
	add := func(provider *string) {
		mc, err := h.configStore.GetModelConfig(ctx, configstoreTables.ModelConfigScopeVirtualKey, &vk.ID, configstoreTables.ModelConfigAllModels, provider)
		if err != nil {
			if !errors.Is(err, configstore.ErrNotFound) {
				logger.Error("failed to get model config for VK governance hydration: %v", err)
			}
			return
		}
		if mc != nil {
			byKey[vkModelConfigIndexKey(vk.ID, provider)] = mc
		}
	}
	add(nil)
	for i := range vk.ProviderConfigs {
		prov := vk.ProviderConfigs[i].Provider
		add(&prov)
	}
	applyVKGovernanceFromModelConfigs(vk, byKey)
}

// buildVKModelConfigIndex builds a lookup map of VK-scoped model configs keyed by
// vkModelConfigIndexKey, from a slice of model-config pointers.
func buildVKModelConfigIndex(mcs []*configstoreTables.TableModelConfig) map[string]*configstoreTables.TableModelConfig {
	byKey := make(map[string]*configstoreTables.TableModelConfig)
	for _, mc := range mcs {
		if mc != nil && mc.Scope == configstoreTables.ModelConfigScopeVirtualKey && mc.ModelName == configstoreTables.ModelConfigAllModels && mc.ScopeID != nil {
			byKey[vkModelConfigIndexKey(*mc.ScopeID, mc.Provider)] = mc
		}
	}
	return byKey
}

// hydrateVKListGovernance reverse-maps governance for a list of VKs using a single bulk load
// of all VK-scoped model configs (avoids per-VK/per-provider queries).
func (h *GovernanceHandler) hydrateVKListGovernance(ctx context.Context, vks []configstoreTables.TableVirtualKey) {
	if len(vks) == 0 {
		return
	}
	allMCs, err := h.configStore.GetModelConfigs(ctx)
	if err != nil {
		logger.Error("failed to load model configs for VK governance hydration: %v", err)
		return
	}
	byKey := make(map[string]*configstoreTables.TableModelConfig)
	for i := range allMCs {
		mc := &allMCs[i]
		if mc.Scope == configstoreTables.ModelConfigScopeVirtualKey && mc.ModelName == configstoreTables.ModelConfigAllModels && mc.ScopeID != nil {
			byKey[vkModelConfigIndexKey(*mc.ScopeID, mc.Provider)] = mc
		}
	}
	for i := range vks {
		applyVKGovernanceFromModelConfigs(&vks[i], byKey)
	}
}

// applyExternalBudgets swaps a VK's own budget and rate-limit rows for the ones
// that actually govern its spend when an external resolver supplies them
// (enterprise: the access-profile budgets and rate limit that carry the real usage
// for an AP-managed VK, in place of the VK's own untracked mirror rows). This is the
// same resolution the self-service quota endpoint performs, applied to the admin
// read paths so the VK detail/list responses show the effective limits without the
// caller needing a separate, differently-gated access-profile lookup. No-op in OSS
// (resolver nil) and for non-AP-managed VKs (resolver returns nil). A resolver error
// degrades gracefully to the VK's own rows rather than failing the whole read.
func (h *GovernanceHandler) applyExternalBudgets(ctx context.Context, vk *configstoreTables.TableVirtualKey) {
	if h.externalQuotaBudgetResolver == nil || vk == nil {
		return
	}
	ext, err := h.externalQuotaBudgetResolver(ctx, vk)
	if err != nil {
		logger.Error("failed to resolve external budgets for VK %s: %v", vk.ID, err)
		return
	}
	if ext == nil {
		return
	}
	// The VK is access-profile-managed: its own budget/rate-limit rows are untracked
	// mirrors (reset to current_usage=0 at adoption and never charged), so surface the
	// AP's governance verbatim. An empty budget slice or nil rate limit means the AP has
	// none — we must NOT fall back to the misleading VK rows. This mirrors the UI's
	// managing-profile branch and getVirtualKeyQuota, so all read paths agree.
	vk.IsAccessProfileManaged = ext.Managed
	vk.Budgets = ext.Budgets
	vk.RateLimit = ext.RateLimit
}

func collectProviderConfigDeleteIDs(
	config configstoreTables.TableVirtualKeyProviderConfig,
	budgetIDs []string,
	rateLimitIDs []string,
) ([]string, []string) {
	for _, b := range config.Budgets {
		budgetIDs = append(budgetIDs, b.ID)
	}
	if config.RateLimitID != nil {
		rateLimitIDs = append(rateLimitIDs, *config.RateLimitID)
	}
	return budgetIDs, rateLimitIDs
}

// CreateTeamRequest represents the request body for creating a team
type CreateTeamRequest struct {
	Name            string                  `json:"name" validate:"required"`
	CustomerID      *string                 `json:"customer_id,omitempty"`      // Team can belong to a customer
	Budgets         []CreateBudgetRequest   `json:"budgets,omitempty"`          // Multi-budget: each must have a unique reset_duration
	RateLimit       *CreateRateLimitRequest `json:"rate_limit,omitempty"`       // Team can have its own rate limit
	CalendarAligned bool                    `json:"calendar_aligned,omitempty"` // Team-wide: snap all team budgets and rate-limit resets to calendar boundaries
}

// UpdateTeamRequest represents the request body for updating a team
type UpdateTeamRequest struct {
	Name            *string                 `json:"name,omitempty"`
	CustomerID      *string                 `json:"customer_id,omitempty"`
	Budgets         []CreateBudgetRequest   `json:"budgets,omitempty"` // Multi-budget: replaces all team budgets
	RateLimit       *UpdateRateLimitRequest `json:"rate_limit,omitempty"`
	CalendarAligned *bool                   `json:"calendar_aligned,omitempty"` // Team-wide setting; nil means "leave unchanged"
	// ResetBudgetUsage zeroes current usage on the reconciled budgets when true.
	// The reset boundary is left alone; only accumulated spend is cleared.
	ResetBudgetUsage *bool `json:"reset_budget_usage,omitempty"`
}

// CreateCustomerRequest represents the request body for creating a customer
type CreateCustomerRequest struct {
	Name            string                  `json:"name" validate:"required"`
	Budgets         []CreateBudgetRequest   `json:"budgets,omitempty"` // Multi-budget: each must have a unique reset_duration
	Budget          *CreateBudgetRequest    `json:"budget,omitempty"`  // Deprecated: use budgets
	RateLimit       *CreateRateLimitRequest `json:"rate_limit,omitempty"`
	CalendarAligned bool                    `json:"calendar_aligned,omitempty"`
}

// UpdateCustomerRequest represents the request body for updating a customer
type UpdateCustomerRequest struct {
	Name            *string                 `json:"name,omitempty"`
	Budgets         *[]CreateBudgetRequest  `json:"budgets,omitempty"` // nil=no change, []=remove all
	Budget          *UpdateBudgetRequest    `json:"budget,omitempty"`  // Deprecated: use budgets
	RateLimit       *UpdateRateLimitRequest `json:"rate_limit,omitempty"`
	CalendarAligned *bool                   `json:"calendar_aligned,omitempty"`
	// ResetBudgetUsage zeroes current usage on the reconciled budgets when true.
	ResetBudgetUsage *bool `json:"reset_budget_usage,omitempty"`
}

// CreateModelConfigRequest represents the request body for creating a model config
type CreateModelConfigRequest struct {
	ModelName string                  `json:"model_name" validate:"required"`
	Provider  *string                 `json:"provider,omitempty"` // Optional provider, nil means all providers
	Scope     string                  `json:"scope,omitempty"`    // Defaults to "global" if not provided
	ScopeID   *string                 `json:"scope_id,omitempty"` // Required for non-global scopes (e.g. the virtual key ID)
	Budgets   []CreateBudgetRequest   `json:"budgets,omitempty"`  // A model config may carry multiple budgets (distinct reset windows)
	RateLimit *CreateRateLimitRequest `json:"rate_limit,omitempty"`
}

// UpdateModelConfigRequest represents the request body for updating a model config.
// Scope and scope_id are part of a config's identity and are intentionally not
// editable here (mirroring model_name/provider) — change them by recreating the config.
type UpdateModelConfigRequest struct {
	ModelName *string                 `json:"model_name,omitempty"`
	Provider  *string                 `json:"provider,omitempty"` // Optional provider, nil means no change
	Budgets   []CreateBudgetRequest   `json:"budgets,omitempty"`  // Full desired set of budgets (reconciled against existing)
	RateLimit *UpdateRateLimitRequest `json:"rate_limit,omitempty"`
	// ResetBudgetUsage zeroes current usage on the reconciled budgets when true.
	ResetBudgetUsage *bool `json:"reset_budget_usage,omitempty"`
}

// UpdateProviderGovernanceRequest represents the request body for updating provider governance
type UpdateProviderGovernanceRequest struct {
	Budget          *UpdateBudgetRequest    `json:"budget,omitempty"`  // deprecated; use budgets
	Budgets         *[]CreateBudgetRequest  `json:"budgets,omitempty"` // nil=no change, []=remove all
	RateLimit       *UpdateRateLimitRequest `json:"rate_limit,omitempty"`
	CalendarAligned *bool                   `json:"calendar_aligned,omitempty"`
	// ResetBudgetUsage zeroes current usage on the reconciled budgets when true.
	ResetBudgetUsage *bool `json:"reset_budget_usage,omitempty"`
}

// RegisterRoutes registers all governance routes with the default OSS family handlers.
func (h *GovernanceHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	h.RegisterRoutesWithOverrides(r, GovernanceRouteOverrides{}, middlewares...)
}

// RegisterRoutesWithOverrides registers governance routes while allowing downstream editions to replace route families.
func (h *GovernanceHandler) RegisterRoutesWithOverrides(r *router.Router, overrides GovernanceRouteOverrides, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/governance/complexity-analyzer-config", lib.ChainMiddlewares(h.getComplexityAnalyzerConfig, middlewares...))
	r.PUT("/api/governance/complexity-analyzer-config", lib.ChainMiddlewares(h.updateComplexityAnalyzerConfig, middlewares...))
	r.POST("/api/governance/complexity-analyzer-config/reset", lib.ChainMiddlewares(h.resetComplexityAnalyzerConfig, middlewares...))

	// Virtual Key CRUD operations
	r.GET("/api/governance/virtual-keys", lib.ChainMiddlewares(h.getVirtualKeys, middlewares...))
	r.POST("/api/governance/virtual-keys", lib.ChainMiddlewares(h.createVirtualKey, middlewares...))
	r.POST("/api/governance/virtual-keys/rotate", lib.ChainMiddlewares(h.rotateVirtualKeys, middlewares...))
	r.GET("/api/governance/virtual-keys/{vk_id}", lib.ChainMiddlewares(h.getVirtualKey, middlewares...))
	r.PUT("/api/governance/virtual-keys/{vk_id}", lib.ChainMiddlewares(h.updateVirtualKey, middlewares...))
	r.POST("/api/governance/virtual-keys/{vk_id}/rotate", lib.ChainMiddlewares(h.rotateVirtualKey, middlewares...))
	r.PUT("/api/governance/virtual-keys/{vk_id}/budgets/{budget_id}/override", lib.ChainMiddlewares(h.updateVirtualKeyBudgetOverride, middlewares...))
	r.DELETE("/api/governance/virtual-keys/{vk_id}/budgets/{budget_id}/override", lib.ChainMiddlewares(h.deleteVirtualKeyBudgetOverride, middlewares...))
	r.DELETE("/api/governance/virtual-keys/{vk_id}", lib.ChainMiddlewares(h.deleteVirtualKey, middlewares...))

	// Team CRUD operations. Enterprise replaces this family with a superset
	// registrar so the router sees exactly one handler per method/path.
	h.registerTeamRoutes(r, overrides.Teams, middlewares...)

	// Customer CRUD operations
	r.GET("/api/governance/customers", lib.ChainMiddlewares(h.getCustomers, middlewares...))
	r.POST("/api/governance/customers", lib.ChainMiddlewares(h.createCustomer, middlewares...))
	r.GET("/api/governance/customers/{customer_id}", lib.ChainMiddlewares(h.getCustomer, middlewares...))
	r.PUT("/api/governance/customers/{customer_id}", lib.ChainMiddlewares(h.updateCustomer, middlewares...))
	r.DELETE("/api/governance/customers/{customer_id}", lib.ChainMiddlewares(h.deleteCustomer, middlewares...))

	// Budget and Rate Limit GET operations
	r.GET("/api/governance/budgets", lib.ChainMiddlewares(h.getBudgets, middlewares...))
	r.GET("/api/governance/rate-limits", lib.ChainMiddlewares(h.getRateLimits, middlewares...))

	// Routing Rules CRUD operations
	r.GET("/api/governance/routing-rules", lib.ChainMiddlewares(h.getRoutingRules, middlewares...))
	r.POST("/api/governance/routing-rules", lib.ChainMiddlewares(h.createRoutingRule, middlewares...))
	r.GET("/api/governance/routing-rules/{rule_id}", lib.ChainMiddlewares(h.getRoutingRule, middlewares...))
	r.PUT("/api/governance/routing-rules/{rule_id}", lib.ChainMiddlewares(h.updateRoutingRule, middlewares...))
	r.DELETE("/api/governance/routing-rules/{rule_id}", lib.ChainMiddlewares(h.deleteRoutingRule, middlewares...))

	// Model Config CRUD operations
	r.GET("/api/governance/model-configs", lib.ChainMiddlewares(h.getModelConfigs, middlewares...))
	r.POST("/api/governance/model-configs", lib.ChainMiddlewares(h.createModelConfig, middlewares...))
	r.GET("/api/governance/model-configs/{mc_id}", lib.ChainMiddlewares(h.getModelConfig, middlewares...))
	r.PUT("/api/governance/model-configs/{mc_id}", lib.ChainMiddlewares(h.updateModelConfig, middlewares...))
	r.DELETE("/api/governance/model-configs/{mc_id}", lib.ChainMiddlewares(h.deleteModelConfig, middlewares...))

	// Provider Governance operations
	r.GET("/api/governance/providers", lib.ChainMiddlewares(h.getProviderGovernance, middlewares...))
	r.PUT("/api/governance/providers/{provider_name}", lib.ChainMiddlewares(h.updateProviderGovernance, middlewares...))
	r.DELETE("/api/governance/providers/{provider_name}", lib.ChainMiddlewares(h.deleteProviderGovernance, middlewares...))

	// Pricing override operations
	r.GET("/api/governance/pricing-overrides", lib.ChainMiddlewares(h.getPricingOverrides, middlewares...))
	r.POST("/api/governance/pricing-overrides", lib.ChainMiddlewares(h.createPricingOverride, middlewares...))
	r.PUT("/api/governance/pricing-overrides/{id}", lib.ChainMiddlewares(h.updatePricingOverride, middlewares...))
	r.DELETE("/api/governance/pricing-overrides/{id}", lib.ChainMiddlewares(h.deletePricingOverride, middlewares...))

	// Self-service endpoint — no admin auth, VK in header is the credential.
	// Registered without admin middlewares; only common middlewares (telemetry) are applied.
	r.GET("/api/governance/virtual-keys/quota", h.getVirtualKeyQuota)
}

// registerTeamRoutes registers Team CRUD with optional edition-specific handlers.
func (h *GovernanceHandler) registerTeamRoutes(r *router.Router, overrides *GovernanceTeamRouteOverrides, middlewares ...schemas.BifrostHTTPMiddleware) {
	listHandler := h.getTeams
	getHandler := h.getTeam
	createHandler := h.createTeam
	updateHandler := h.updateTeam
	deleteHandler := h.deleteTeam
	if overrides != nil {
		if overrides.List != nil {
			listHandler = overrides.List
		}
		if overrides.Get != nil {
			getHandler = overrides.Get
		}
		if overrides.Create != nil {
			createHandler = overrides.Create
		}
		if overrides.Update != nil {
			updateHandler = overrides.Update
		}
		if overrides.Delete != nil {
			deleteHandler = overrides.Delete
		}
	}

	r.GET("/api/governance/teams", lib.ChainMiddlewares(listHandler, middlewares...))
	r.POST("/api/governance/teams", lib.ChainMiddlewares(createHandler, middlewares...))
	r.GET("/api/governance/teams/{team_id}", lib.ChainMiddlewares(getHandler, middlewares...))
	r.PUT("/api/governance/teams/{team_id}", lib.ChainMiddlewares(updateHandler, middlewares...))
	r.DELETE("/api/governance/teams/{team_id}", lib.ChainMiddlewares(deleteHandler, middlewares...))
	if overrides != nil && overrides.Extensions != nil {
		overrides.Extensions(r, middlewares...)
	}
}

func (h *GovernanceHandler) getComplexityAnalyzerConfig(ctx *fasthttp.RequestCtx) {
	if h.configStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not available")
		return
	}

	cfg, err := h.configStore.GetComplexityAnalyzerConfig(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get complexity analyzer config: %v", err))
		return
	}
	if cfg == nil {
		defaults := complexity.DefaultAnalyzerConfig()
		SendJSON(ctx, defaults)
		return
	}
	SendJSON(ctx, cfg)
}

func (h *GovernanceHandler) updateComplexityAnalyzerConfig(ctx *fasthttp.RequestCtx) {
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

func (h *GovernanceHandler) resetComplexityAnalyzerConfig(ctx *fasthttp.RequestCtx) {
	if h.configStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not available")
		return
	}

	defaults := complexity.DefaultAnalyzerConfig()
	if err := h.configStore.UpdateComplexityAnalyzerConfig(ctx, &defaults); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to reset complexity analyzer config: %v", err))
		return
	}
	if err := h.reloadComplexityAnalyzerConfig(ctx, &defaults); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to reload complexity analyzer config in memory: %v, please restart bifrost to sync with the database", err))
		return
	}

	SendJSON(ctx, defaults)
}

func (h *GovernanceHandler) reloadComplexityAnalyzerConfig(ctx context.Context, config *complexity.AnalyzerConfig) error {
	reloader, ok := h.governanceManager.(complexityAnalyzerConfigReloader)
	if !ok {
		return fmt.Errorf("governance manager does not support complexity analyzer config reload")
	}
	return reloader.ReloadComplexityAnalyzerConfig(ctx, config)
}

// Virtual Key CRUD Operations

// getVirtualKeys handles GET /api/governance/virtual-keys - Get all virtual keys with relationships
func (h *GovernanceHandler) getVirtualKeys(ctx *fasthttp.RequestCtx) {
	// Check if "from_memory" query parameter is set to true
	fromMemory := string(ctx.QueryArgs().Peek("from_memory")) == "true"
	if fromMemory {
		// The in-memory cache holds no VK↔user assignments (GovernanceData.Users carries
		// only budget/rate-limit ids), so a user_id filter cannot be applied here. Reject
		// the combination rather than silently dropping the filter: the database path
		// fails closed on user_id, and returning every cached key instead would be the
		// exact inverse of that contract. The other filters keep their long-standing
		// ignored-under-from_memory behaviour.
		if len(ctx.QueryArgs().Peek("user_id")) > 0 {
			SendError(ctx, 400, "user_id filter is not supported with from_memory=true; omit from_memory to filter virtual keys by user")
			return
		}
		data := h.governanceManager.GetGovernanceData(ctx)
		if data == nil {
			SendError(ctx, 500, "Governance data is not available")
			return
		}
		// Convert map to slice to match the non-memory response format (array)
		virtualKeys := make([]*configstoreTables.TableVirtualKey, 0, len(data.VirtualKeys))
		for _, vk := range data.VirtualKeys {
			virtualKeys = append(virtualKeys, vk)
		}
		sort.Slice(virtualKeys, func(i, j int) bool {
			return virtualKeys[i].CreatedAt.Before(virtualKeys[j].CreatedAt)
		})
		byKey := buildVKModelConfigIndex(data.ModelConfigs)
		hydratedVKs := make([]*configstoreTables.TableVirtualKey, len(virtualKeys))
		for i, vk := range virtualKeys {
			clone := *vk
			pcs := make([]configstoreTables.TableVirtualKeyProviderConfig, len(vk.ProviderConfigs))
			copy(pcs, vk.ProviderConfigs)
			clone.ProviderConfigs = pcs
			applyVKGovernanceFromModelConfigs(&clone, byKey)
			h.applyExternalBudgets(ctx, &clone)
			hydratedVKs[i] = &clone
		}
		SendJSON(ctx, map[string]interface{}{
			"virtual_keys": hydratedVKs,
			"count":        len(hydratedVKs),
			"total_count":  len(hydratedVKs),
			"limit":        len(hydratedVKs),
			"offset":       0,
		})
		return
	}
	// Check for pagination/filter parameters
	limitStr := string(ctx.QueryArgs().Peek("limit"))
	offsetStr := string(ctx.QueryArgs().Peek("offset"))
	search := string(ctx.QueryArgs().Peek("search"))
	customerID := string(ctx.QueryArgs().Peek("customer_id"))
	teamID := string(ctx.QueryArgs().Peek("team_id"))
	userID := string(ctx.QueryArgs().Peek("user_id"))
	sortBy := string(ctx.QueryArgs().Peek("sort_by"))
	order := string(ctx.QueryArgs().Peek("order"))
	isExport := string(ctx.QueryArgs().Peek("export")) == "true"
	excludeAccessProfileManagedVirtual := string(ctx.QueryArgs().Peek("exclude_access_profile_managed_virtual")) == "true"
	excludeAssignedVirtualKeys := string(ctx.QueryArgs().Peek("exclude_assigned_virtual_keys")) == "true"
	forUserAssignment := string(ctx.QueryArgs().Peek("for_user_assignment")) == "true"

	if limitStr != "" || offsetStr != "" || search != "" || customerID != "" || teamID != "" || userID != "" || sortBy != "" || isExport || excludeAccessProfileManagedVirtual || excludeAssignedVirtualKeys || forUserAssignment {
		// Paginated/filtered path
		params := configstore.VirtualKeyQueryParams{
			Search:                             search,
			CustomerID:                         customerID,
			TeamID:                             teamID,
			UserID:                             userID,
			SortBy:                             sortBy,
			Order:                              order,
			Export:                             isExport,
			ExcludeAccessProfileManagedVirtual: excludeAccessProfileManagedVirtual,
			ExcludeAssignedVirtualKeys:         excludeAssignedVirtualKeys,
			ForUserAssignment:                  forUserAssignment,
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

		if !params.Export {
			params.Limit, params.Offset = ClampPaginationParams(params.Limit, params.Offset)
		} else if params.Offset < 0 {
			params.Offset = 0
		}
		virtualKeys, totalCount, err := h.configStore.GetVirtualKeysPaginated(ctx, params)
		if err != nil {
			logger.Error("failed to retrieve virtual keys: %v", err)
			SendError(ctx, 500, "Failed to retrieve virtual keys")
			return
		}
		// Reverse-map governance from VK-scoped model configs for display.
		h.hydrateVKListGovernance(ctx, virtualKeys)
		for i := range virtualKeys {
			h.applyExternalBudgets(ctx, &virtualKeys[i])
		}
		SendJSON(ctx, map[string]interface{}{
			"virtual_keys": virtualKeys,
			"count":        len(virtualKeys),
			"total_count":  totalCount,
			"limit":        params.Limit,
			"offset":       params.Offset,
		})
		return
	}

	// Non-paginated path: return all virtual keys
	virtualKeys, err := h.configStore.GetVirtualKeys(ctx)
	if err != nil {
		logger.Error("failed to retrieve virtual keys: %v", err)
		SendError(ctx, 500, "Failed to retrieve virtual keys")
		return
	}
	h.hydrateVKListGovernance(ctx, virtualKeys)
	for i := range virtualKeys {
		h.applyExternalBudgets(ctx, &virtualKeys[i])
	}
	SendJSON(ctx, map[string]interface{}{
		"virtual_keys": virtualKeys,
		"count":        len(virtualKeys),
		"total_count":  len(virtualKeys),
		"limit":        len(virtualKeys),
		"offset":       0,
	})
}

// createVirtualKey handles POST /api/governance/virtual-keys - Create a new virtual key
func (h *GovernanceHandler) createVirtualKey(ctx *fasthttp.RequestCtx) {
	var req CreateVirtualKeyRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON")
		return
	}
	// Validate required fields
	if req.Name == "" {
		SendError(ctx, 400, "Virtual key name is required")
		return
	}
	// Validate mutually exclusive TeamID and CustomerID
	if req.TeamID != nil && req.CustomerID != nil {
		SendError(ctx, 400, "VirtualKey cannot be attached to both Team and Customer")
		return
	}
	// Validate budgets if provided
	if len(req.Budgets) > 0 {
		seenDurations := make(map[string]bool)
		for _, b := range req.Budgets {
			if b.MaxLimit < 0 {
				SendError(ctx, 400, fmt.Sprintf("Budget max_limit cannot be negative: %.2f", b.MaxLimit))
				return
			}
			if d, err := configstoreTables.ParseDuration(b.ResetDuration); err != nil || d <= 0 {
				SendError(ctx, 400, fmt.Sprintf("Invalid reset duration (must be a positive duration): %s", b.ResetDuration))
				return
			}
			if seenDurations[b.ResetDuration] {
				SendError(ctx, 400, fmt.Sprintf("Duplicate reset_duration in budgets: %s", b.ResetDuration))
				return
			}
			seenDurations[b.ResetDuration] = true
		}
	}
	// Validate expires_at: must be in the future if provided
	if req.ExpiresAt != nil {
		now := time.Now().UTC()
		if !req.ExpiresAt.After(now) {
			SendError(ctx, 400, "expires_at must be a future timestamp")
			return
		}
	}
	// Set defaults: nil means "use DB default (true)"
	isActive := req.IsActive
	if isActive == nil {
		isActive = new(true)
	}
	// Fetch providers from DB to ensure up-to-date data in cluster mode.
	providerSet := map[schemas.ModelProvider]struct{}{}
	if req.ProviderConfigs != nil {
		var err error
		providerSet, err = h.getConfiguredProviderSet(ctx)
		if err != nil {
			SendError(ctx, 500, fmt.Sprintf("Failed to load providers: %v", err))
			return
		}
	}
	var vk configstoreTables.TableVirtualKey
	if err := h.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		vk = configstoreTables.TableVirtualKey{
			ID:              uuid.NewString(),
			Name:            req.Name,
			Value:           *schemas.NewSecretVar(governance.GenerateVirtualKey()),
			Description:     req.Description,
			TeamID:          req.TeamID,
			CustomerID:      req.CustomerID,
			IsActive:        isActive,
			CalendarAligned: req.CalendarAligned,
			ExpiresAt:       req.ExpiresAt,
		}
		if err := h.configStore.CreateVirtualKey(ctx, &vk, tx); err != nil {
			return err
		}
		// VK top-level and per-provider budgets/rate-limits are stored in VK-scoped model configs,
		// the single source of truth, written via syncVKGovernanceToModelConfigs below.
		// The per-provider desired state is accumulated while creating the provider configs.
		var vkGovProviders []vkModelConfigDesired
		if req.ProviderConfigs != nil {
			for _, pc := range req.ProviderConfigs {
				providerName := schemas.ModelProvider(strings.TrimSpace(pc.Provider))
				if providerName == "" {
					return &badRequestError{err: fmt.Errorf("provider name is required")}
				}
				if _, ok := providerSet[providerName]; !ok {
					return &badRequestError{err: fmt.Errorf("invalid provider name: %s", pc.Provider)}
				}
				if err := pc.AllowedModels.Validate(); err != nil {
					return &badRequestError{err: fmt.Errorf("invalid allowed_models for provider %s: %w", pc.Provider, err)}
				}
				if err := pc.BlacklistedModels.Validate(); err != nil {
					return &badRequestError{err: fmt.Errorf("invalid blacklisted_models for provider %s: %w", pc.Provider, err)}
				}
				if err := pc.KeyIDs.Validate(); err != nil {
					return &badRequestError{err: fmt.Errorf("invalid key_ids for provider %s: %w", pc.Provider, err)}
				}

				// Get keys for this provider config if specified
				var keys []configstoreTables.TableKey
				allowAllKeys := false
				if pc.KeyIDs.IsUnrestricted() {
					allowAllKeys = true
				} else if !pc.KeyIDs.IsEmpty() {
					var err error
					keys, err = h.configStore.GetKeysByIDs(ctx, pc.KeyIDs)
					if err != nil {
						return fmt.Errorf("failed to get keys by IDs for provider %s: %w", pc.Provider, err)
					}
					if len(keys) != len(pc.KeyIDs) {
						return fmt.Errorf("some keys not found for provider %s: expected %d, found %d", pc.Provider, len(pc.KeyIDs), len(keys))
					}
				}

				providerConfig := &configstoreTables.TableVirtualKeyProviderConfig{
					VirtualKeyID:      vk.ID,
					Provider:          string(providerName),
					Weight:            pc.Weight,
					AllowedModels:     pc.AllowedModels,
					BlacklistedModels: pc.BlacklistedModels,
					AllowAllKeys:      allowAllKeys,
					Keys:              keys,
				}

				if err := h.configStore.CreateVirtualKeyProviderConfig(ctx, providerConfig, tx); err != nil {
					return err
				}
				// Provider-config budgets/rate-limit are stored in the VK-scoped model config
				// for this provider (written by syncVKGovernanceToModelConfigs).
				providerNameStr := string(providerName)
				var pcRateLimit *configstoreTables.TableRateLimit
				if pc.RateLimit != nil {
					pcRateLimit = rateLimitFromRequestFields(pc.RateLimit.TokenMaxLimit, pc.RateLimit.TokenResetDuration, pc.RateLimit.RequestMaxLimit, pc.RateLimit.RequestResetDuration)
				}
				vkGovProviders = append(vkGovProviders, vkModelConfigDesired{
					provider:          &providerNameStr,
					budgetsProvided:   true,
					budgets:           pc.Budgets,
					rateLimitProvided: pc.RateLimit != nil,
					rateLimit:         pcRateLimit,
				})
			}
		}
		// Fold VK top-level + per-provider governance into VK-scoped model configs.
		var topRateLimit *configstoreTables.TableRateLimit
		if req.RateLimit != nil {
			topRateLimit = rateLimitFromRequestFields(req.RateLimit.TokenMaxLimit, req.RateLimit.TokenResetDuration, req.RateLimit.RequestMaxLimit, req.RateLimit.RequestResetDuration)
		}
		if err := h.syncVKGovernanceToModelConfigs(ctx, tx, &vk, vkModelConfigDesired{
			budgetsProvided:   true,
			budgets:           req.Budgets,
			rateLimitProvided: req.RateLimit != nil,
			rateLimit:         topRateLimit,
		}, vkGovProviders, true, nil); err != nil {
			return err
		}
		if req.MCPConfigs != nil {
			// Check for duplicate MCPClientName values before processing
			seenMCPClientNames := make(map[string]bool)
			for _, mc := range req.MCPConfigs {
				if seenMCPClientNames[mc.MCPClientName] {
					return &badRequestError{err: fmt.Errorf("duplicate mcp_client_name: %s", mc.MCPClientName)}
				}
				seenMCPClientNames[mc.MCPClientName] = true
			}

			for _, mc := range req.MCPConfigs {
				if err := mc.ToolsToExecute.Validate(); err != nil {
					return &badRequestError{err: fmt.Errorf("invalid tools_to_execute for mcp client %s: %w", mc.MCPClientName, err)}
				}
				mcpClient, err := h.configStore.GetMCPClientByName(ctx, mc.MCPClientName)
				if err != nil {
					return fmt.Errorf("failed to get MCP client: %w", err)
				}
				if err := h.configStore.CreateVirtualKeyMCPConfig(ctx, &configstoreTables.TableVirtualKeyMCPConfig{
					VirtualKeyID:   vk.ID,
					MCPClientID:    mcpClient.ID,
					ToolsToExecute: mc.ToolsToExecute,
				}, tx); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		var badReqErr *badRequestError
		if errors.As(err, &badReqErr) {
			SendError(ctx, 400, err.Error())
			return
		}
		if errors.Is(err, configstore.ErrAlreadyExists) {
			SendError(ctx, 409, "A virtual key with this name already exists")
			return
		}
		SendError(ctx, 500, err.Error())
		return
	}
	preloadedVk, err := h.governanceManager.ReloadVirtualKey(ctx, vk.ID)
	if err != nil {
		logger.Error("failed to reload virtual key: %v", err)
		preloadedVk = &vk
	}
	// Reverse-map governance from the model configs just written, for display.
	h.hydrateVKGovernance(ctx, preloadedVk)

	SendJSON(ctx, map[string]any{
		"message":     "Virtual key created successfully",
		"virtual_key": preloadedVk,
	})
}

// getVirtualKey handles GET /api/governance/virtual-keys/{vk_id} - Get a specific virtual key
func (h *GovernanceHandler) getVirtualKey(ctx *fasthttp.RequestCtx) {
	vkID := ctx.UserValue("vk_id").(string)
	// Check if "from_memory" query parameter is set to true
	fromMemory := string(ctx.QueryArgs().Peek("from_memory")) == "true"
	if fromMemory {
		data := h.governanceManager.GetGovernanceData(ctx)
		if data == nil {
			SendError(ctx, 500, "Governance data is not available")
			return
		}
		byKey := buildVKModelConfigIndex(data.ModelConfigs)
		for _, vk := range data.VirtualKeys {
			if vk.ID == vkID {
				clone := *vk
				pcs := make([]configstoreTables.TableVirtualKeyProviderConfig, len(vk.ProviderConfigs))
				copy(pcs, vk.ProviderConfigs)
				clone.ProviderConfigs = pcs
				applyVKGovernanceFromModelConfigs(&clone, byKey)
				h.applyExternalBudgets(ctx, &clone)
				SendJSON(ctx, map[string]interface{}{
					"virtual_key": &clone,
				})
				return
			}
		}
		SendError(ctx, 404, "Virtual key not found")
		return
	}
	vk, err := h.configStore.GetVirtualKey(ctx, vkID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Virtual key not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve virtual key")
		return
	}
	// Reverse-map governance from VK-scoped model configs for display.
	h.hydrateVKGovernance(ctx, vk)
	// Surface the effective (e.g. access-profile) budgets in place of the VK's own
	// untracked rows so the admin detail panel matches the self-service quota view.
	h.applyExternalBudgets(ctx, vk)

	SendJSON(ctx, map[string]interface{}{
		"virtual_key": vk,
	})
}

// loadVirtualKeyBudget resolves only budgets owned by the virtual key's scoped model configs.
func (h *GovernanceHandler) loadVirtualKeyBudget(ctx context.Context, vkID, budgetID string) (*configstoreTables.TableBudget, error) {
	if _, err := h.configStore.GetVirtualKey(ctx, vkID); err != nil {
		return nil, err
	}
	modelConfigs, err := h.configStore.GetModelConfigsByScopeAndScopeIDs(
		ctx,
		configstoreTables.ModelConfigScopeVirtualKey,
		[]string{vkID},
	)
	if err != nil {
		return nil, err
	}
	for i := range modelConfigs {
		for j := range modelConfigs[i].Budgets {
			if modelConfigs[i].Budgets[j].ID == budgetID {
				return &modelConfigs[i].Budgets[j], nil
			}
		}
	}
	return nil, configstore.ErrNotFound
}

// updateVirtualKeyBudgetOverride handles PUT for a standalone virtual-key budget override.
func (h *GovernanceHandler) updateVirtualKeyBudgetOverride(ctx *fasthttp.RequestCtx) {
	h.mutateVirtualKeyBudgetOverride(ctx, false)
}

// deleteVirtualKeyBudgetOverride handles DELETE for a standalone virtual-key budget override.
func (h *GovernanceHandler) deleteVirtualKeyBudgetOverride(ctx *fasthttp.RequestCtx) {
	h.mutateVirtualKeyBudgetOverride(ctx, true)
}

// mutateVirtualKeyBudgetOverride replaces or clears an override without changing base budget configuration or usage.
func (h *GovernanceHandler) mutateVirtualKeyBudgetOverride(ctx *fasthttp.RequestCtx, clear bool) {
	vkID := ctx.UserValue("vk_id").(string)
	budgetID := ctx.UserValue("budget_id").(string)
	budget, err := h.loadVirtualKeyBudget(ctx, vkID, budgetID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "virtual key or budget not found")
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to retrieve virtual key budget")
		return
	}

	if clear {
		budget.ClearOverride()
	} else {
		var req BudgetOverrideRequest
		if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
			return
		}
		if err := budget.SetOverride(req.Amount, req.Mode, req.Cycles); err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, err.Error())
			return
		}
	}

	// IsCalendarAligned was stamped onto the budget by the virtual key's AfterFind
	// during loadVirtualKeyBudget, and the store needs it to anchor a finite grant
	// on the same lattice the budget actually resets on.
	budget, err = h.configStore.UpdateBudgetOverride(
		ctx,
		budget.ID,
		budget.OverrideAmount,
		budget.OverrideMode,
		budget.OverrideCyclesRemaining,
		budget.IsCalendarAligned,
	)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "budget not found")
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to update budget override")
		return
	}
	if _, err := h.governanceManager.ReloadVirtualKey(ctx, vkID); err != nil {
		logger.Error("failed to reload virtual key after budget override: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "budget override saved but virtual key reload failed")
		return
	}

	SendJSON(ctx, BudgetOverrideResponse{
		Budget:            budget,
		EffectiveMaxLimit: budget.EffectiveMaxLimit(),
	})
}

// updateVirtualKey handles PUT /api/governance/virtual-keys/{vk_id} - Update a virtual key
func (h *GovernanceHandler) updateVirtualKey(ctx *fasthttp.RequestCtx) {
	vkID := ctx.UserValue("vk_id").(string)
	var req UpdateVirtualKeyRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON")
		return
	}
	// Validate mutually exclusive TeamID and CustomerID
	if optionalJSONStringHasValue(req.TeamID) && optionalJSONStringHasValue(req.CustomerID) {
		SendError(ctx, 400, "VirtualKey cannot be attached to both Team and Customer")
		return
	}
	// The operator's explicit "reset usage" choice, surfaced by the UI's
	// Preserve / Reset dialog. Carried into budget reconciliation and collected
	// there, so the in-memory store can be cleared once the transaction commits.
	usageReset := &budgetUsageReset{requested: req.ResetBudgetUsage != nil && *req.ResetBudgetUsage}
	// See adoptCalendarAlignment: captured before the update so open windows are
	// re-anchored rather than reset when alignment is switched on.
	alignmentSwitchedOn := false
	// Parse expires_at when provided: a timestamp must be in the future, "" clears the expiry.
	var newExpiresAt *time.Time
	if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			SendError(ctx, 400, "expires_at must be an RFC3339 timestamp")
			return
		}
		if !parsed.After(time.Now().UTC()) {
			SendError(ctx, 400, "expires_at must be a future timestamp")
			return
		}
		newExpiresAt = &parsed
	}
	vk, err := h.configStore.GetVirtualKey(ctx, vkID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Virtual key not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve virtual key")
		return
	}
	providerSet := map[schemas.ModelProvider]struct{}{}
	if len(req.ProviderConfigs) > 0 {
		providerSet, err = h.getConfiguredProviderSet(ctx)
		if err != nil {
			SendError(ctx, 500, fmt.Sprintf("Failed to load providers: %v", err))
			return
		}
	}
	if err := h.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		var rateLimitIDToDelete string
		var providerBudgetIDsToDelete []string
		var providerRateLimitIDsToDelete []string
		var lockedVK configstoreTables.TableVirtualKey
		if err := dbForUpdate(tx.WithContext(ctx)).
			Preload("Budgets").
			Preload("RateLimit").
			Preload("ProviderConfigs").
			First(&lockedVK, "id = ?", vkID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return configstore.ErrNotFound
			}
			return err
		}
		vk = &lockedVK
		sort.Slice(vk.Budgets, func(i, j int) bool {
			if vk.Budgets[i].ResetDuration == vk.Budgets[j].ResetDuration {
				return vk.Budgets[i].ID < vk.Budgets[j].ID
			}
			return vk.Budgets[i].ResetDuration < vk.Budgets[j].ResetDuration
		})

		// Update fields if provided
		if req.Name != nil {
			vk.Name = *req.Name
		}
		if req.Description != nil {
			vk.Description = *req.Description
		}
		if err := applyVirtualKeyOwnershipUpdate(vk, &req); err != nil {
			if errors.Is(err, errVirtualKeyDualAssociation) {
				return &badRequestError{err: err}
			}
			return err
		}
		if req.IsActive != nil {
			vk.IsActive = req.IsActive
		}
		if req.ExpiresAt != nil {
			vk.ExpiresAt = newExpiresAt
		}
		if req.CalendarAligned != nil {
			alignmentSwitchedOn = !vk.CalendarAligned && *req.CalendarAligned
			vk.CalendarAligned = *req.CalendarAligned
		}
		// VK top-level and per-provider budgets/rate-limits are stored in VK-scoped model
		// configs (the single source of truth), written by syncVKGovernanceToModelConfigs
		// below. Per-provider desired state is accumulated while reconciling provider config rows.
		var vkGovProviders []vkModelConfigDesired

		if err := h.configStore.UpdateVirtualKey(ctx, vk, tx); err != nil {
			return err
		}
		if req.ProviderConfigs != nil {
			// Get existing provider configs for comparison
			var existingConfigs []configstoreTables.TableVirtualKeyProviderConfig
			if err := tx.Where("virtual_key_id = ?", vk.ID).
				Preload("Budgets").
				Find(&existingConfigs).Error; err != nil {
				return err
			}
			sort.Slice(existingConfigs, func(i, j int) bool { return existingConfigs[i].ID < existingConfigs[j].ID })
			sort.Slice(req.ProviderConfigs, func(i, j int) bool {
				if req.ProviderConfigs[i].ID == nil && req.ProviderConfigs[j].ID != nil {
					return false
				}
				if req.ProviderConfigs[i].ID != nil && req.ProviderConfigs[j].ID == nil {
					return true
				}
				if req.ProviderConfigs[i].ID != nil && req.ProviderConfigs[j].ID != nil && *req.ProviderConfigs[i].ID != *req.ProviderConfigs[j].ID {
					return *req.ProviderConfigs[i].ID < *req.ProviderConfigs[j].ID
				}
				return req.ProviderConfigs[i].Provider < req.ProviderConfigs[j].Provider
			})
			// Create maps for easier lookup
			existingConfigsMap := make(map[uint]configstoreTables.TableVirtualKeyProviderConfig)
			for _, config := range existingConfigs {
				existingConfigsMap[config.ID] = config
			}
			requestConfigsMap := make(map[uint]bool)
			// Process new configs: create new ones and update existing ones
			for _, pc := range req.ProviderConfigs {
				providerName := schemas.ModelProvider(strings.TrimSpace(pc.Provider))
				if providerName == "" {
					return &badRequestError{err: fmt.Errorf("provider name is required")}
				}
				if _, ok := providerSet[providerName]; !ok {
					return &badRequestError{err: fmt.Errorf("invalid provider name: %s", pc.Provider)}
				}
				if pc.ID == nil {
					if err := pc.AllowedModels.Validate(); err != nil {
						return &badRequestError{err: fmt.Errorf("invalid allowed_models for provider %s: %w", pc.Provider, err)}
					}
					if err := pc.BlacklistedModels.Validate(); err != nil {
						return &badRequestError{err: fmt.Errorf("invalid blacklisted_models for provider %s: %w", pc.Provider, err)}
					}
					if err := pc.KeyIDs.Validate(); err != nil {
						return &badRequestError{err: fmt.Errorf("invalid key_ids for provider %s: %w", pc.Provider, err)}
					}

					// Get keys for this provider config if specified
					var keys []configstoreTables.TableKey
					allowAllKeys := false
					if pc.KeyIDs.IsUnrestricted() {
						allowAllKeys = true
					} else if !pc.KeyIDs.IsEmpty() {
						var err error
						keys, err = h.configStore.GetKeysByIDs(ctx, pc.KeyIDs)
						if err != nil {
							return fmt.Errorf("failed to get keys by IDs for provider %s: %w", pc.Provider, err)
						}
						if len(keys) != len(pc.KeyIDs) {
							return fmt.Errorf("some keys not found for provider %s: expected %d, found %d", pc.Provider, len(pc.KeyIDs), len(keys))
						}
					}

					// Create new provider config
					providerConfig := &configstoreTables.TableVirtualKeyProviderConfig{
						VirtualKeyID:      vk.ID,
						Provider:          string(providerName),
						Weight:            pc.Weight,
						AllowedModels:     pc.AllowedModels,
						BlacklistedModels: pc.BlacklistedModels,
						AllowAllKeys:      allowAllKeys,
						Keys:              keys,
					}
					if err := h.configStore.CreateVirtualKeyProviderConfig(ctx, providerConfig, tx); err != nil {
						return err
					}
					// Provider-config governance is stored in the VK-scoped model config for this provider.
					pName := string(providerName)
					var pcRL *configstoreTables.TableRateLimit
					if pc.RateLimit != nil {
						pcRL = rateLimitFromRequestFields(pc.RateLimit.TokenMaxLimit, pc.RateLimit.TokenResetDuration, pc.RateLimit.RequestMaxLimit, pc.RateLimit.RequestResetDuration)
					}
					vkGovProviders = append(vkGovProviders, vkModelConfigDesired{
						provider:          &pName,
						budgetsProvided:   true,
						budgets:           pc.Budgets,
						rateLimitProvided: pc.RateLimit != nil,
						rateLimit:         pcRL,
					})
				} else {
					// Update existing provider config
					existing, ok := existingConfigsMap[*pc.ID]
					if !ok {
						return fmt.Errorf("provider config %d does not belong to this virtual key", *pc.ID)
					}
					requestConfigsMap[*pc.ID] = true
					if err := pc.AllowedModels.Validate(); err != nil {
						return &badRequestError{err: fmt.Errorf("invalid allowed_models for provider %s: %w", pc.Provider, err)}
					}
					if err := pc.BlacklistedModels.Validate(); err != nil {
						return &badRequestError{err: fmt.Errorf("invalid blacklisted_models for provider %s: %w", pc.Provider, err)}
					}
					if err := pc.KeyIDs.Validate(); err != nil {
						return &badRequestError{err: fmt.Errorf("invalid key_ids for provider %s: %w", pc.Provider, err)}
					}
					existing.Provider = string(providerName)
					existing.Weight = pc.Weight
					existing.AllowedModels = pc.AllowedModels
					existing.BlacklistedModels = pc.BlacklistedModels

					// Get keys for this provider config if specified
					var keys []configstoreTables.TableKey
					allowAllKeys := false
					if pc.KeyIDs.IsUnrestricted() {
						allowAllKeys = true
					} else if !pc.KeyIDs.IsEmpty() {
						var err error
						keys, err = h.configStore.GetKeysByIDs(ctx, pc.KeyIDs)
						if err != nil {
							return fmt.Errorf("failed to get keys by IDs for provider %s: %w", pc.Provider, err)
						}
						if len(keys) != len(pc.KeyIDs) {
							return fmt.Errorf("some keys not found for provider %s: expected %d, found %d", pc.Provider, len(pc.KeyIDs), len(keys))
						}
					}
					existing.AllowAllKeys = allowAllKeys
					existing.Keys = keys

					// Provider-config governance is stored in the VK-scoped model config for this
					// provider (written by syncVKGovernanceToModelConfigs). pc.Budgets == nil
					// leaves existing budgets unchanged; an explicit set reconciles them.
					pName := string(providerName)
					rlRemove := false
					var pcRL *configstoreTables.TableRateLimit
					if pc.RateLimit != nil {
						if isRateLimitRemovalRequest(pc.RateLimit) {
							rlRemove = true
						} else {
							pcRL = rateLimitFromRequestFields(pc.RateLimit.TokenMaxLimit, pc.RateLimit.TokenResetDuration, pc.RateLimit.RequestMaxLimit, pc.RateLimit.RequestResetDuration)
						}
					}
					vkGovProviders = append(vkGovProviders, vkModelConfigDesired{
						provider:          &pName,
						budgetsProvided:   pc.Budgets != nil,
						budgets:           pc.Budgets,
						rateLimitProvided: pc.RateLimit != nil,
						rateLimitRemove:   rlRemove,
						rateLimit:         pcRL,
					})
					if err := h.configStore.UpdateVirtualKeyProviderConfig(ctx, &existing, tx); err != nil {
						return err
					}
				}
			}
			// Delete provider configs that are not in the request
			configIDs := make([]uint, 0, len(existingConfigsMap))
			for id := range existingConfigsMap {
				configIDs = append(configIDs, id)
			}
			sort.Slice(configIDs, func(i, j int) bool { return configIDs[i] < configIDs[j] })
			for _, id := range configIDs {
				if !requestConfigsMap[id] {
					providerBudgetIDsToDelete, providerRateLimitIDsToDelete = collectProviderConfigDeleteIDs(
						existingConfigsMap[id],
						providerBudgetIDsToDelete,
						providerRateLimitIDsToDelete,
					)
					if err := h.configStore.DeleteVirtualKeyProviderConfig(ctx, id, tx); err != nil {
						return err
					}
				}
			}
		}
		// Fold VK governance into VK-scoped model configs. The top-level is always reconciled
		// (provided-aware); per-provider configs are reconciled only when the request supplied
		// provider_configs (else they're left untouched).
		top := vkModelConfigDesired{
			budgetsProvided:   req.Budgets != nil,
			budgets:           req.Budgets,
			rateLimitProvided: req.RateLimit != nil,
		}
		if req.RateLimit != nil {
			if isRateLimitRemovalRequest(req.RateLimit) {
				top.rateLimitRemove = true
			} else {
				top.rateLimit = rateLimitFromRequestFields(req.RateLimit.TokenMaxLimit, req.RateLimit.TokenResetDuration, req.RateLimit.RequestMaxLimit, req.RateLimit.RequestResetDuration)
			}
		}
		if err := h.syncVKGovernanceToModelConfigs(ctx, tx, vk, top, vkGovProviders, req.ProviderConfigs != nil, usageReset); err != nil {
			return err
		}
		if req.MCPConfigs != nil {
			// Check for duplicate MCPClientName values among all configs before processing
			seenMCPClientNames := make(map[string]bool)
			for _, mc := range req.MCPConfigs {
				if seenMCPClientNames[mc.MCPClientName] {
					return &badRequestError{err: fmt.Errorf("duplicate mcp_client_name: %s", mc.MCPClientName)}
				}
				seenMCPClientNames[mc.MCPClientName] = true
			}
			// Get existing MCP configs for comparison
			var existingMCPConfigs []configstoreTables.TableVirtualKeyMCPConfig
			if err := tx.Where("virtual_key_id = ?", vk.ID).Find(&existingMCPConfigs).Error; err != nil {
				return err
			}
			sort.Slice(existingMCPConfigs, func(i, j int) bool { return existingMCPConfigs[i].ID < existingMCPConfigs[j].ID })
			sort.Slice(req.MCPConfigs, func(i, j int) bool {
				if req.MCPConfigs[i].ID == nil && req.MCPConfigs[j].ID != nil {
					return false
				}
				if req.MCPConfigs[i].ID != nil && req.MCPConfigs[j].ID == nil {
					return true
				}
				if req.MCPConfigs[i].ID != nil && req.MCPConfigs[j].ID != nil && *req.MCPConfigs[i].ID != *req.MCPConfigs[j].ID {
					return *req.MCPConfigs[i].ID < *req.MCPConfigs[j].ID
				}
				return req.MCPConfigs[i].MCPClientName < req.MCPConfigs[j].MCPClientName
			})
			// Create maps for easier lookup
			existingMCPConfigsMap := make(map[uint]configstoreTables.TableVirtualKeyMCPConfig)
			for _, config := range existingMCPConfigs {
				existingMCPConfigsMap[config.ID] = config
			}
			requestMCPConfigsMap := make(map[uint]bool)
			// Process new configs: create new ones and update existing ones
			for _, mc := range req.MCPConfigs {
				if err := mc.ToolsToExecute.Validate(); err != nil {
					return &badRequestError{err: fmt.Errorf("invalid tools_to_execute for mcp client %s: %w", mc.MCPClientName, err)}
				}
				if mc.ID == nil {
					mcpClient, err := h.configStore.GetMCPClientByName(ctx, mc.MCPClientName)
					if err != nil {
						return fmt.Errorf("failed to get MCP client: %w", err)
					}
					// Create new MCP config
					if err := h.configStore.CreateVirtualKeyMCPConfig(ctx, &configstoreTables.TableVirtualKeyMCPConfig{
						VirtualKeyID:   vk.ID,
						MCPClientID:    mcpClient.ID,
						ToolsToExecute: mc.ToolsToExecute,
					}, tx); err != nil {
						return err
					}
				} else {
					// Update existing MCP config
					existing, ok := existingMCPConfigsMap[*mc.ID]
					if !ok {
						return fmt.Errorf("MCP config %d does not belong to this virtual key", *mc.ID)
					}
					requestMCPConfigsMap[*mc.ID] = true
					existing.ToolsToExecute = mc.ToolsToExecute
					if err := h.configStore.UpdateVirtualKeyMCPConfig(ctx, &existing, tx); err != nil {
						return err
					}
				}
			}
			// Delete MCP configs that are not in the request
			mcpConfigIDs := make([]uint, 0, len(existingMCPConfigsMap))
			for id := range existingMCPConfigsMap {
				mcpConfigIDs = append(mcpConfigIDs, id)
			}
			sort.Slice(mcpConfigIDs, func(i, j int) bool { return mcpConfigIDs[i] < mcpConfigIDs[j] })
			for _, id := range mcpConfigIDs {
				if !requestMCPConfigsMap[id] {
					if err := h.configStore.DeleteVirtualKeyMCPConfig(ctx, id, tx); err != nil {
						return err
					}
				}
			}
		}

		if rateLimitIDToDelete != "" {
			if err := h.configStore.DeleteRateLimit(ctx, rateLimitIDToDelete, tx); err != nil {
				return err
			}
		}
		sort.Strings(providerBudgetIDsToDelete)
		for _, id := range providerBudgetIDsToDelete {
			if err := h.configStore.DeleteBudget(ctx, id, tx); err != nil && !errors.Is(err, configstore.ErrNotFound) {
				return err
			}
		}
		sort.Strings(providerRateLimitIDsToDelete)
		for _, id := range providerRateLimitIDsToDelete {
			if err := h.configStore.DeleteRateLimit(ctx, id, tx); err != nil && !errors.Is(err, configstore.ErrNotFound) {
				return err
			}
		}

		return nil
	}); err != nil {
		var badReqErr *badRequestError
		if errors.As(err, &badReqErr) {
			SendError(ctx, 400, fmt.Sprintf("Failed to update virtual key: %v", err))
			return
		}
		if errors.Is(err, configstore.ErrAlreadyExists) {
			SendError(ctx, 409, "A virtual key with this name already exists")
			return
		}
		SendError(ctx, 500, fmt.Sprintf("Failed to update virtual key: %v", err))
		return
	}
	// Load relationships for response
	preloadedVk, err := h.configStore.GetVirtualKey(ctx, vk.ID)
	if err != nil {
		logger.Error("failed to load relationships for updated VK: %v", err)
		preloadedVk = vk
	}
	// Reverse-map governance from VK-scoped model configs for display.
	h.hydrateVKGovernance(ctx, preloadedVk)

	if _, err := h.governanceManager.ReloadVirtualKey(ctx, vk.ID); err != nil {
		// Should never happen but just in case
		logger.Error("failed to reload virtual key after update: %v", err)
		SendError(ctx, 500, "Virtual key updated in database but failed to reload in-memory state")
		return
	}
	// Clear usage in the store that actually enforces spend. This must run after
	// the reload, not before: ReloadVirtualKey rebuilds the virtual key in memory
	// and deliberately carries the cached CurrentUsage forward, which would undo
	// the reset. Enterprise additionally propagates this to cluster peers.
	if len(usageReset.budgetIDs) > 0 {
		if err := h.governanceManager.ResetBudgetUsageInMemory(ctx, BudgetUsageResetOwner{Kind: BudgetOwnerVirtualKey, ID: vk.ID}, usageReset.budgetIDs); err != nil {
			logger.Error("failed to reset in-memory budget usage after update: %v", err)
			SendError(ctx, 500, "Virtual key updated but budget usage reset did not take effect")
			return
		}
	}
	if alignmentSwitchedOn {
		// The VK's own windows, plus every model config that inherits the flag from
		// it: provider governance carries no alignment field of its own, so a VK
		// switch is the only event that aligns those budgets and their rate limits.
		// Reload is allowed to return no entity; fall back to the row already held.
		adoptBudgets, adoptRateLimitID := vk.Budgets, vk.RateLimitID
		if preloadedVk != nil {
			adoptBudgets, adoptRateLimitID = preloadedVk.Budgets, preloadedVk.RateLimitID
		}
		if err := h.adoptCalendarAlignment(ctx, true,
			BudgetUsageResetOwner{Kind: BudgetOwnerVirtualKey, ID: vk.ID}, adoptBudgets, adoptRateLimitID); err != nil {
			logger.Error("failed to adopt calendar alignment for virtual key: %v", err)
			SendError(ctx, 500, "Virtual key updated but calendar alignment did not take effect")
			return
		}
		modelConfigs, err := h.configStore.GetModelConfigsByScopeAndScopeIDs(ctx, configstoreTables.ModelConfigScopeVirtualKey, []string{vk.ID})
		if err != nil {
			logger.Error("failed to load model configs for calendar alignment adoption: %v", err)
			SendError(ctx, 500, "Virtual key updated but calendar alignment did not take effect")
			return
		}
		for i := range modelConfigs {
			if err := h.adoptCalendarAlignment(ctx, true,
				BudgetUsageResetOwner{Kind: BudgetOwnerModelConfig, ID: modelConfigs[i].ID}, modelConfigs[i].Budgets, modelConfigs[i].RateLimitID); err != nil {
				logger.Error("failed to adopt calendar alignment for model config %s: %v", modelConfigs[i].ID, err)
				SendError(ctx, 500, "Virtual key updated but calendar alignment did not take effect")
				return
			}
		}
	}

	// Per-user credential reconciliation when the VK's MCP allowlist
	// changed. Mirrors the AP-propagation path: enterprise orphans /
	// reactivates credentials keyed to this VK (vk-keyed creds) and to the
	// VK's owner (user-keyed creds) against the new effective allowlist
	// (explicit rows ∪ MCPs with AllowOnAllVirtualKeys=true). OSS no-ops.
	// Must run before ReloadVirtualKey: the reload also evicts this VK's
	// cached OAuth tokens and header credentials, and an eviction that lands
	// before these writes could be refilled from the pre-reconcile rows and
	// then never dropped.
	if req.MCPConfigs != nil && h.configStore != nil {
		if err := h.configStore.ReconcileOauthAfterVKChange(ctx, vk.ID); err != nil {
			logger.Error("reconcile OAuth credentials after VK %s update failed: %v", vk.ID, err)
		}
		if err := h.configStore.ReconcileMCPHeadersAfterVKChange(ctx, vk.ID); err != nil {
			logger.Error("reconcile per-user-headers credentials after VK %s update failed: %v", vk.ID, err)
		}
	}

	if _, err := h.governanceManager.ReloadVirtualKey(ctx, vk.ID); err != nil {
		// Should never happen but just in case
		logger.Error("failed to reload virtual key after update: %v", err)
		SendError(ctx, 500, "Virtual key updated in database but failed to reload in-memory state")
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"message":     "Virtual key updated successfully",
		"virtual_key": preloadedVk,
	})
}

func (h *GovernanceHandler) rotateVirtualKeyByID(ctx context.Context, vkID string) (*configstoreTables.TableVirtualKey, error) {
	vk, err := h.configStore.GetVirtualKey(ctx, vkID)
	if err != nil {
		return nil, err
	}
	oldValue := vk.Value.GetValue()
	vk.Value = *schemas.NewSecretVar(governance.GenerateVirtualKey())
	if vk.Value.GetValue() == oldValue {
		return nil, fmt.Errorf("generated virtual key matched existing value")
	}
	if err := h.configStore.UpdateVirtualKey(ctx, vk); err != nil {
		return nil, err
	}
	preloadedVk, err := h.governanceManager.ReloadVirtualKey(ctx, vk.ID)
	if err != nil {
		return nil, fmt.Errorf("virtual key rotated in database but failed to reload in-memory state: %w", err)
	}
	h.hydrateVKGovernance(ctx, preloadedVk)
	return preloadedVk, nil
}

// rotateVirtualKey handles POST /api/governance/virtual-keys/{vk_id}/rotate - Rotate only the virtual key value
func (h *GovernanceHandler) rotateVirtualKey(ctx *fasthttp.RequestCtx) {
	vkID := ctx.UserValue("vk_id").(string)
	preloadedVk, err := h.rotateVirtualKeyByID(ctx, vkID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Virtual key not found")
			return
		}
		logger.Error("failed to rotate virtual key: %v", err)
		SendError(ctx, 500, fmt.Sprintf("Failed to rotate virtual key: %v", err))
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"message":     "Virtual key rotated successfully",
		"virtual_key": preloadedVk,
	})
}

// rotateVirtualKeys handles POST /api/governance/virtual-keys/rotate - Rotate multiple virtual key values
func (h *GovernanceHandler) rotateVirtualKeys(ctx *fasthttp.RequestCtx) {
	var req BulkRotateVirtualKeysRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON")
		return
	}
	if len(req.IDs) == 0 {
		SendError(ctx, 400, "At least one virtual key ID is required")
		return
	}

	seen := make(map[string]struct{}, len(req.IDs))
	ids := make([]string, 0, len(req.IDs))
	for _, id := range req.IDs {
		id = strings.TrimSpace(id)
		if id == "" {
			SendError(ctx, 400, "Virtual key ID cannot be empty")
			return
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	rotated := make([]*configstoreTables.TableVirtualKey, 0, len(ids))
	failures := make(map[string]string)
	for _, id := range ids {
		vk, err := h.rotateVirtualKeyByID(ctx, id)
		if err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				failures[id] = "virtual key not found"
			} else {
				failures[id] = err.Error()
			}
			logger.Error("failed to rotate virtual key %s: %v", id, err)
			continue
		}
		rotated = append(rotated, vk)
	}

	response := map[string]interface{}{
		"message":      "Virtual keys rotated successfully",
		"virtual_keys": rotated,
	}
	if len(failures) > 0 {
		response["errors"] = failures
	}
	if len(rotated) == 0 {
		response["message"] = "Failed to rotate virtual keys"
		SendJSONWithStatus(ctx, response, 500)
		return
	}
	SendJSON(ctx, response)
}

// deleteVirtualKey handles DELETE /api/governance/virtual-keys/{vk_id} - Delete a virtual key
func (h *GovernanceHandler) deleteVirtualKey(ctx *fasthttp.RequestCtx) {
	vkID := ctx.UserValue("vk_id").(string)
	// Fetch the virtual key from the database to get the budget and rate limit
	vk, err := h.configStore.GetVirtualKey(ctx, vkID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Virtual key not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve virtual key")
		return
	}
	// Deleting key from database
	if err := h.configStore.DeleteVirtualKey(ctx, vkID); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Virtual key not found")
			return
		}
		logger.Error("failed to delete virtual key: %v", err)
		SendError(ctx, 500, "Failed to delete virtual key")
		return
	}
	// Removing key from in-memory store. RemoveVirtualKey also evicts the
	// VK's cached OAuth access tokens and header credentials internally,
	// covering the rows the database delete above cascaded over.
	err = h.governanceManager.RemoveVirtualKey(ctx, vk.ID)
	if err != nil {
		// But we ignore this error because its not
		logger.Error("failed to remove virtual key: %v", err)
	}
	SendJSON(ctx, map[string]interface{}{
		"message": "Virtual key deleted successfully",
	})
}

// Team CRUD Operations

// getTeams handles GET /api/governance/teams - Get all teams
func (h *GovernanceHandler) getTeams(ctx *fasthttp.RequestCtx) {
	customerID := string(ctx.QueryArgs().Peek("customer_id"))

	// Check for pagination parameters
	limitStr := string(ctx.QueryArgs().Peek("limit"))
	offsetStr := string(ctx.QueryArgs().Peek("offset"))
	search := string(ctx.QueryArgs().Peek("search"))

	if limitStr != "" || offsetStr != "" || search != "" {
		limit, _ := strconv.Atoi(limitStr)
		offset, _ := strconv.Atoi(offsetStr)
		limit, offset = ClampPaginationParams(limit, offset)
		teams, totalCount, err := h.configStore.GetTeamsPaginated(ctx, configstore.TeamsQueryParams{
			Limit:      limit,
			Offset:     offset,
			Search:     search,
			CustomerID: customerID,
		})
		if err != nil {
			logger.Error("failed to retrieve teams: %v", err)
			SendError(ctx, 500, fmt.Sprintf("Failed to retrieve teams: %v", err))
			return
		}
		SendJSON(ctx, map[string]interface{}{
			"teams":       teams,
			"count":       len(teams),
			"total_count": totalCount,
			"limit":       limit,
			"offset":      offset,
		})
		return
	}

	// Non-paginated path: return all teams
	teams, err := h.configStore.GetTeams(ctx, customerID)
	if err != nil {
		logger.Error("failed to retrieve teams: %v", err)
		SendError(ctx, 500, fmt.Sprintf("Failed to retrieve teams: %v", err))
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"teams":       teams,
		"count":       len(teams),
		"total_count": len(teams),
		"limit":       len(teams),
		"offset":      0,
	})
}

// createTeam handles POST /api/governance/teams - Create a new team
func (h *GovernanceHandler) createTeam(ctx *fasthttp.RequestCtx) {
	var req CreateTeamRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON")
		return
	}
	// Validate required fields
	if req.Name == "" {
		SendError(ctx, 400, "Team name is required")
		return
	}
	// Validate rate limit if provided
	if req.RateLimit != nil {
		rateLimit := configstoreTables.TableRateLimit{
			TokenMaxLimit:        req.RateLimit.TokenMaxLimit,
			TokenResetDuration:   req.RateLimit.TokenResetDuration,
			RequestMaxLimit:      req.RateLimit.RequestMaxLimit,
			RequestResetDuration: req.RateLimit.RequestResetDuration,
		}
		if err := validateRateLimit(&rateLimit); err != nil {
			SendError(ctx, 400, fmt.Sprintf("Invalid rate limit: %s", err.Error()))
			return
		}
	}
	// Creating team in database
	var team configstoreTables.TableTeam
	if err := h.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		team = configstoreTables.TableTeam{
			ID:              uuid.NewString(),
			Name:            req.Name,
			CustomerID:      req.CustomerID,
			CalendarAligned: req.CalendarAligned,
		}
		if req.RateLimit != nil {
			rateLimit := configstoreTables.TableRateLimit{
				ID:                   uuid.NewString(),
				TokenMaxLimit:        req.RateLimit.TokenMaxLimit,
				TokenResetDuration:   req.RateLimit.TokenResetDuration,
				RequestMaxLimit:      req.RateLimit.RequestMaxLimit,
				RequestResetDuration: req.RateLimit.RequestResetDuration,
				TokenLastReset:       time.Now(),
				RequestLastReset:     time.Now(),
			}
			if err := h.configStore.CreateRateLimit(ctx, &rateLimit, tx); err != nil {
				return err
			}
			team.RateLimitID = &rateLimit.ID
		}
		// Team row must exist before child budgets (FK on governance_budgets.team_id)
		if err := h.configStore.CreateTeam(ctx, &team, tx); err != nil {
			return err
		}
		// Create owned multi-budgets; enforce unique reset_duration per team
		seenDurations := make(map[string]bool)
		for _, b := range req.Budgets {
			if b.MaxLimit < 0 {
				return &badRequestError{err: fmt.Errorf("budget max_limit cannot be negative: %.2f", b.MaxLimit)}
			}
			if d, err := configstoreTables.ParseDuration(b.ResetDuration); err != nil || d <= 0 {
				return &badRequestError{err: fmt.Errorf("invalid reset duration (must be a positive duration): %s", b.ResetDuration)}
			}
			if seenDurations[b.ResetDuration] {
				return &badRequestError{err: fmt.Errorf("duplicate reset_duration in budgets: %s", b.ResetDuration)}
			}
			seenDurations[b.ResetDuration] = true
			budget := newBudgetFromRequest(b, team.CalendarAligned)
			budget.TeamID = &team.ID
			if err := validateBudget(&budget); err != nil {
				return err
			}
			if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
				return err
			}
			team.Budgets = append(team.Budgets, budget)
		}
		return nil
	}); err != nil {
		var badReqErr *badRequestError
		if errors.As(err, &badReqErr) {
			SendError(ctx, 400, err.Error())
			return
		}
		if errors.Is(err, configstore.ErrAlreadyExists) {
			SendError(ctx, 409, "A team with this name already exists")
			return
		}
		logger.Error("failed to create team: %v", err)
		SendError(ctx, 500, "failed to create team")
		return
	}
	// Reloading team from in-memory store
	preloadedTeam, err := h.governanceManager.ReloadTeam(ctx, team.ID)
	if err != nil {
		logger.Error("failed to reload team: %v", err)
		preloadedTeam = &team
	}
	SendJSON(ctx, map[string]interface{}{
		"message": "Team created successfully",
		"team":    preloadedTeam,
	})
}

// getTeam handles GET /api/governance/teams/{team_id} - Get a specific team
func (h *GovernanceHandler) getTeam(ctx *fasthttp.RequestCtx) {
	// The router matches on the raw (percent-encoded) path, so SCIM/IdP-synced team
	// IDs containing spaces or other URL-sensitive characters arrive still encoded.
	teamID, err := url.PathUnescape(ctx.UserValue("team_id").(string))
	if err != nil {
		SendError(ctx, 400, "Invalid team ID encoding")
		return
	}
	team, err := h.configStore.GetTeam(ctx, teamID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Team not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve team")
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"team": team,
	})
}

// updateTeam handles PUT /api/governance/teams/{team_id} - Update a team
func (h *GovernanceHandler) updateTeam(ctx *fasthttp.RequestCtx) {
	// The router matches on the raw (percent-encoded) path, so SCIM/IdP-synced team
	// IDs containing spaces or other URL-sensitive characters arrive still encoded.
	teamID, err := url.PathUnescape(ctx.UserValue("team_id").(string))
	if err != nil {
		SendError(ctx, 400, "Invalid team ID encoding")
		return
	}

	var req UpdateTeamRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON")
		return
	}
	// The operator's explicit "reset usage" choice. Carried into budget
	// reconciliation and collected there, so the in-memory store can be cleared
	// once the transaction commits.
	usageReset := &budgetUsageReset{requested: req.ResetBudgetUsage != nil && *req.ResetBudgetUsage}
	// Whether this request switches alignment on. Captured before the update so the
	// open windows can be adopted onto the calendar grid afterwards instead of
	// being reset out from under the operator; see adoptCalendarAlignment.
	alignmentSwitchedOn := false
	// Fetching team from database
	team, err := h.configStore.GetTeam(ctx, teamID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Team not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve team")
		return
	}
	// Updating team in database
	if err := h.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		// Track rate-limit ID to delete after updating the team (to avoid FK constraint)
		var rateLimitIDToDelete string

		// Update fields if provided
		if req.Name != nil {
			team.Name = *req.Name
		}
		if req.CustomerID != nil {
			if *req.CustomerID == "" {
				team.CustomerID = nil
			} else {
				team.CustomerID = req.CustomerID
			}
		}
		// Resolve team-level calendar alignment for this update:
		//   - explicit team-level field wins (req.CalendarAligned != nil)
		//   - else leave existing team.CalendarAligned untouched
		if req.CalendarAligned != nil {
			alignmentSwitchedOn = !team.CalendarAligned && *req.CalendarAligned
			team.CalendarAligned = *req.CalendarAligned
		}
		// Snap-to-calendar-period happens after budget/rate-limit reconciliation
		// below, so combined `calendar_aligned + budgets/rate_limit` updates see
		// the final persisted state.

		// Multi-budget reconciliation: match by reset_duration, preserve usage on update,
		// create new budgets for new durations, delete unmatched existing budgets.
		// Mirrors VK multi-budget handling above.
		if req.Budgets != nil {
			// Validate incoming budgets
			seenDurations := make(map[string]bool)
			for _, b := range req.Budgets {
				if b.MaxLimit < 0 {
					return &badRequestError{err: fmt.Errorf("budget max_limit cannot be negative: %.2f", b.MaxLimit)}
				}
				if d, err := configstoreTables.ParseDuration(b.ResetDuration); err != nil || d <= 0 {
					return &badRequestError{err: fmt.Errorf("invalid reset duration (must be a positive duration): %s", b.ResetDuration)}
				}
				if seenDurations[b.ResetDuration] {
					return &badRequestError{err: fmt.Errorf("duplicate reset_duration in budgets: %s", b.ResetDuration)}
				}
				seenDurations[b.ResetDuration] = true
			}

			existingByDuration := make(map[string]configstoreTables.TableBudget)
			for _, existing := range team.Budgets {
				existingByDuration[existing.ResetDuration] = existing
			}

			var reconciledBudgets []configstoreTables.TableBudget
			matchedIDs := make(map[string]bool)
			for _, b := range req.Budgets {
				if existing, found := existingByDuration[b.ResetDuration]; found {
					existing.MaxLimit = b.MaxLimit
					applyResetConfigToExistingBudget(&existing, b)
					// LastReset is preserved on update: the reset boundary only ever
					// moves forward, and never as a side effect of a config write.
					// CurrentUsage is preserved too unless the operator explicitly
					// asked for a reset, which UpdateBudget cannot carry and so goes
					// through the store method that owns the column.
					if err := validateBudget(&existing); err != nil {
						return err
					}
					if err := h.configStore.UpdateBudget(ctx, &existing, tx); err != nil {
						return err
					}
					if err := usageReset.apply(ctx, h.configStore, existing.ID, tx); err != nil {
						return err
					}
					if usageReset.requested {
						existing.CurrentUsage = 0
					}
					reconciledBudgets = append(reconciledBudgets, existing)
					matchedIDs[existing.ID] = true
				} else {
					budget := newBudgetFromRequest(b, team.CalendarAligned)
					budget.TeamID = &team.ID
					if err := validateBudget(&budget); err != nil {
						return err
					}
					if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
						return err
					}
					reconciledBudgets = append(reconciledBudgets, budget)
				}
			}
			// Delete budgets that are no longer present
			for _, existing := range team.Budgets {
				if !matchedIDs[existing.ID] {
					if err := h.configStore.DeleteBudget(ctx, existing.ID, tx); err != nil {
						return fmt.Errorf("failed to delete removed team budget: %w", err)
					}
				}
			}
			team.Budgets = reconciledBudgets
		}
		// Handle rate limit updates
		if req.RateLimit != nil {
			// Check if rate limit values are empty - means remove rate limit (reset durations don't matter)
			rateLimitIsEmpty := req.RateLimit.TokenMaxLimit == nil && req.RateLimit.RequestMaxLimit == nil
			if rateLimitIsEmpty {
				// Mark rate limit for deletion after FK is removed
				if team.RateLimitID != nil {
					rateLimitIDToDelete = *team.RateLimitID
					team.RateLimitID = nil
					team.RateLimit = nil
				}
			} else if team.RateLimitID != nil {
				// Update existing rate limit
				rateLimit := configstoreTables.TableRateLimit{}
				if err := tx.First(&rateLimit, "id = ?", *team.RateLimitID).Error; err != nil {
					return err
				}
				rateLimit.TokenMaxLimit = req.RateLimit.TokenMaxLimit
				rateLimit.TokenResetDuration = req.RateLimit.TokenResetDuration
				rateLimit.RequestMaxLimit = req.RateLimit.RequestMaxLimit
				rateLimit.RequestResetDuration = req.RateLimit.RequestResetDuration
				if err := validateRateLimit(&rateLimit); err != nil {
					return err
				}
				if err := h.configStore.UpdateRateLimit(ctx, &rateLimit, tx); err != nil {
					return err
				}
				team.RateLimit = &rateLimit
			} else {
				// Create new rate limit
				rateLimit := configstoreTables.TableRateLimit{
					ID:                   uuid.NewString(),
					TokenMaxLimit:        req.RateLimit.TokenMaxLimit,
					TokenResetDuration:   req.RateLimit.TokenResetDuration,
					RequestMaxLimit:      req.RateLimit.RequestMaxLimit,
					RequestResetDuration: req.RateLimit.RequestResetDuration,
					TokenLastReset:       time.Now(),
					RequestLastReset:     time.Now(),
				}
				if err := validateRateLimit(&rateLimit); err != nil {
					return err
				}
				if err := h.configStore.CreateRateLimit(ctx, &rateLimit, tx); err != nil {
					return err
				}
				team.RateLimitID = &rateLimit.ID
				team.RateLimit = &rateLimit
			}
		}

		if err := h.configStore.UpdateTeam(ctx, team, tx); err != nil {
			return err
		}

		// Now that FK references are removed, delete the orphaned rate limit.
		// Budgets are reconciled above (deletion of unmatched rows happens inside
		// the reconciliation loop), so nothing to clean up here.
		if rateLimitIDToDelete != "" {
			if err := tx.Delete(&configstoreTables.TableRateLimit{}, "id = ?", rateLimitIDToDelete).Error; err != nil {
				return err
			}
		}

		return nil
	}); err != nil {
		var badReqErr *badRequestError
		if errors.As(err, &badReqErr) {
			SendError(ctx, 400, err.Error())
			return
		}
		logger.Error("failed to update team: %v", err)
		SendError(ctx, 500, "Failed to update team")
		return
	}
	// Reloading team from in-memory store.
	//
	// A failure here cannot be swallowed on this path: the usage reset below is
	// ordered after the reload and depends on it having happened, so continuing
	// would clear usage against stale in-memory config while the database already
	// holds the new one, and still answer 200 with the pre-update team.
	preloadedTeam, err := h.governanceManager.ReloadTeam(ctx, team.ID)
	if err != nil {
		logger.Error("failed to reload team: %v", err)
		SendError(ctx, 500, "Team updated in database but failed to reload in-memory state")
		return
	}
	// Clear usage in the store that enforces spend. This runs after the reload,
	// which deliberately carries each budget's cached usage forward and would
	// otherwise undo the reset. Enterprise propagates it to cluster peers.
	if len(usageReset.budgetIDs) > 0 {
		if err := h.governanceManager.ResetBudgetUsageInMemory(ctx, BudgetUsageResetOwner{Kind: BudgetOwnerTeam, ID: team.ID}, usageReset.budgetIDs); err != nil {
			logger.Error("failed to reset in-memory budget usage for Team: %v", err)
			SendError(ctx, 500, "Team updated but budget usage reset did not take effect")
			return
		}
	}
	// Reload is allowed to return no entity; fall back to the row already held.
	adoptBudgets, adoptRateLimitID := team.Budgets, team.RateLimitID
	if preloadedTeam != nil {
		adoptBudgets, adoptRateLimitID = preloadedTeam.Budgets, preloadedTeam.RateLimitID
	}
	if err := h.adoptCalendarAlignment(ctx, alignmentSwitchedOn,
		BudgetUsageResetOwner{Kind: BudgetOwnerTeam, ID: team.ID}, adoptBudgets, adoptRateLimitID); err != nil {
		logger.Error("failed to adopt calendar alignment for Team: %v", err)
		SendError(ctx, 500, "Team updated but calendar alignment did not take effect")
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"message": "Team updated successfully",
		"team":    preloadedTeam,
	})
}

// deleteTeam handles DELETE /api/governance/teams/{team_id} - Delete a team
func (h *GovernanceHandler) deleteTeam(ctx *fasthttp.RequestCtx) {
	// The router matches on the raw (percent-encoded) path, so SCIM/IdP-synced team
	// IDs containing spaces or other URL-sensitive characters arrive still encoded.
	teamID, err := url.PathUnescape(ctx.UserValue("team_id").(string))
	if err != nil {
		SendError(ctx, 400, "Invalid team ID encoding")
		return
	}
	team, err := h.configStore.GetTeam(ctx, teamID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Team not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve team")
		return
	}
	// Removing team from in-memory store
	err = h.governanceManager.RemoveTeam(ctx, team.ID)
	if err != nil {
		// But we ignore this error because its not
		logger.Error("failed to remove team: %v", err)
	}
	if err := h.configStore.DeleteTeam(ctx, teamID); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Team not found")
			return
		}
		SendError(ctx, 500, "Failed to delete team")
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"message": "Team deleted successfully",
	})
}

// Customer CRUD Operations

// getCustomers handles GET /api/governance/customers - Get all customers
func (h *GovernanceHandler) getCustomers(ctx *fasthttp.RequestCtx) {
	limitStr := string(ctx.QueryArgs().Peek("limit"))
	offsetStr := string(ctx.QueryArgs().Peek("offset"))
	search := string(ctx.QueryArgs().Peek("search"))

	if limitStr != "" || offsetStr != "" || search != "" {
		limit, _ := strconv.Atoi(limitStr)
		offset, _ := strconv.Atoi(offsetStr)
		limit, offset = ClampPaginationParams(limit, offset)
		customers, totalCount, err := h.configStore.GetCustomersPaginated(ctx, configstore.CustomersQueryParams{
			Limit:  limit,
			Offset: offset,
			Search: search,
		})
		if err != nil {
			logger.Error("failed to retrieve customers: %v", err)
			SendError(ctx, 500, "failed to retrieve customers")
			return
		}
		SendJSON(ctx, map[string]interface{}{
			"customers":   customers,
			"count":       len(customers),
			"total_count": totalCount,
			"limit":       limit,
			"offset":      offset,
		})
		return
	}

	customers, err := h.configStore.GetCustomers(ctx)
	if err != nil {
		logger.Error("failed to retrieve customers: %v", err)
		SendError(ctx, 500, "failed to retrieve customers")
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"customers":   customers,
		"count":       len(customers),
		"total_count": len(customers),
		"limit":       len(customers),
		"offset":      0,
	})
}

// createCustomer handles POST /api/governance/customers - Create a new customer
func (h *GovernanceHandler) createCustomer(ctx *fasthttp.RequestCtx) {
	var req CreateCustomerRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON")
		return
	}
	if len(req.Budgets) > 0 && req.Budget != nil {
		SendError(ctx, 400, "only one of 'budget' or 'budgets' may be set")
		return
	}
	// Validate required fields
	if req.Name == "" {
		SendError(ctx, 400, "Customer name is required")
		return
	}
	// Validate rate limit if provided
	if req.RateLimit != nil {
		rateLimit := configstoreTables.TableRateLimit{
			TokenMaxLimit:        req.RateLimit.TokenMaxLimit,
			TokenResetDuration:   req.RateLimit.TokenResetDuration,
			RequestMaxLimit:      req.RateLimit.RequestMaxLimit,
			RequestResetDuration: req.RateLimit.RequestResetDuration,
		}
		if err := validateRateLimit(&rateLimit); err != nil {
			SendError(ctx, 400, fmt.Sprintf("Invalid rate limit: %s", err.Error()))
			return
		}
	}
	// Coerce legacy singular budget into the multi-budget slice.
	budgetRequests := req.Budgets
	if len(budgetRequests) == 0 && req.Budget != nil {
		budgetRequests = []CreateBudgetRequest{{
			MaxLimit:      req.Budget.MaxLimit,
			ResetDuration: req.Budget.ResetDuration,
		}}
	}
	var customer configstoreTables.TableCustomer
	if err := h.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		customer = configstoreTables.TableCustomer{
			ID:              uuid.NewString(),
			Name:            req.Name,
			CalendarAligned: req.CalendarAligned,
		}
		if err := h.configStore.CreateCustomer(ctx, &customer, tx); err != nil {
			return err
		}
		if len(budgetRequests) > 0 {
			if err := h.reconcileCustomerBudgets(ctx, tx, &customer, budgetRequests, nil); err != nil {
				return err
			}
		}
		if req.RateLimit != nil {
			rateLimit := configstoreTables.TableRateLimit{
				ID:                   uuid.NewString(),
				TokenMaxLimit:        req.RateLimit.TokenMaxLimit,
				TokenResetDuration:   req.RateLimit.TokenResetDuration,
				RequestMaxLimit:      req.RateLimit.RequestMaxLimit,
				RequestResetDuration: req.RateLimit.RequestResetDuration,
				TokenLastReset:       time.Now(),
				RequestLastReset:     time.Now(),
			}
			if err := h.configStore.CreateRateLimit(ctx, &rateLimit, tx); err != nil {
				return err
			}
			customer.RateLimitID = &rateLimit.ID
			if err := h.configStore.UpdateCustomer(ctx, &customer, tx); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		var badReqErr *badRequestError
		if errors.As(err, &badReqErr) {
			SendError(ctx, 400, err.Error())
			return
		}
		if errors.Is(err, configstore.ErrAlreadyExists) {
			SendError(ctx, 409, "A customer with this name already exists")
			return
		}
		SendError(ctx, 500, "failed to create customer")
		return
	}
	preloadedCustomer, err := h.governanceManager.ReloadCustomer(ctx, customer.ID)
	if err != nil {
		logger.Error("failed to reload customer: %v", err)
		preloadedCustomer = &customer
	}
	SendJSON(ctx, map[string]interface{}{
		"message":  "Customer created successfully",
		"customer": preloadedCustomer,
	})
}

// getCustomer handles GET /api/governance/customers/{customer_id} - Get a specific customer
func (h *GovernanceHandler) getCustomer(ctx *fasthttp.RequestCtx) {
	customerID := ctx.UserValue("customer_id").(string)
	customer, err := h.configStore.GetCustomer(ctx, customerID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Customer not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve customer")
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"customer": customer,
	})
}

// updateCustomer handles PUT /api/governance/customers/{customer_id} - Update a customer
func (h *GovernanceHandler) updateCustomer(ctx *fasthttp.RequestCtx) {
	customerID := ctx.UserValue("customer_id").(string)
	var req UpdateCustomerRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON")
		return
	}
	// The operator's explicit "reset usage" choice. Carried into budget
	// reconciliation and collected there, so the in-memory store can be cleared
	// once the transaction commits.
	usageReset := &budgetUsageReset{requested: req.ResetBudgetUsage != nil && *req.ResetBudgetUsage}
	// See adoptCalendarAlignment: captured before the update so open windows are
	// re-anchored rather than reset when alignment is switched on.
	alignmentSwitchedOn := false
	if req.Budgets != nil && req.Budget != nil {
		SendError(ctx, 400, "only one of 'budget' or 'budgets' may be set")
		return
	}
	// Fetching customer from database
	customer, err := h.configStore.GetCustomer(ctx, customerID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Customer not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve customer")
		return
	}
	// Updating customer in database
	if err := h.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		var rateLimitIDToDelete string

		// Update fields if provided
		if req.Name != nil {
			customer.Name = *req.Name
		}
		if req.CalendarAligned != nil {
			alignmentSwitchedOn = !customer.CalendarAligned && *req.CalendarAligned
			customer.CalendarAligned = *req.CalendarAligned
		}
		// Handle budget updates: prefer Budgets slice; coerce legacy Budget if needed.
		effectiveBudgets := req.Budgets
		if effectiveBudgets == nil && req.Budget != nil {
			if len(customer.Budgets) > 1 {
				return &badRequestError{err: fmt.Errorf("deprecated 'budget' field cannot be used when multiple budgets already exist; use 'budgets'")}
			}
			var existingBudget *configstoreTables.TableBudget
			if len(customer.Budgets) == 1 {
				existingBudget = &customer.Budgets[0]
			}
			effectiveBudgets = coerceLegacyBudget(req.Budget, existingBudget)
			if effectiveBudgets == nil && !isBudgetRemovalRequest(req.Budget) {
				return &badRequestError{err: fmt.Errorf("both max_limit and reset_duration are required when creating a new budget")}
			}
		}
		if effectiveBudgets != nil {
			if err := h.reconcileCustomerBudgets(ctx, tx, customer, *effectiveBudgets, usageReset); err != nil {
				return err
			}
		}
		// Handle rate limit updates
		if req.RateLimit != nil {
			// Check if rate limit values are empty - means remove rate limit (reset durations don't matter)
			rateLimitIsEmpty := req.RateLimit.TokenMaxLimit == nil && req.RateLimit.RequestMaxLimit == nil
			if rateLimitIsEmpty {
				// Mark rate limit for deletion after FK is removed
				if customer.RateLimitID != nil {
					rateLimitIDToDelete = *customer.RateLimitID
					customer.RateLimitID = nil
					customer.RateLimit = nil
				}
			} else if customer.RateLimitID != nil {
				// Update existing rate limit
				rateLimit := configstoreTables.TableRateLimit{}
				if err := tx.First(&rateLimit, "id = ?", *customer.RateLimitID).Error; err != nil {
					return err
				}
				rateLimit.TokenMaxLimit = req.RateLimit.TokenMaxLimit
				rateLimit.TokenResetDuration = req.RateLimit.TokenResetDuration
				rateLimit.RequestMaxLimit = req.RateLimit.RequestMaxLimit
				rateLimit.RequestResetDuration = req.RateLimit.RequestResetDuration
				if err := validateRateLimit(&rateLimit); err != nil {
					return err
				}
				if err := h.configStore.UpdateRateLimit(ctx, &rateLimit, tx); err != nil {
					return err
				}
				customer.RateLimit = &rateLimit
			} else {
				// Create new rate limit
				rateLimit := configstoreTables.TableRateLimit{
					ID:                   uuid.NewString(),
					TokenMaxLimit:        req.RateLimit.TokenMaxLimit,
					TokenResetDuration:   req.RateLimit.TokenResetDuration,
					RequestMaxLimit:      req.RateLimit.RequestMaxLimit,
					RequestResetDuration: req.RateLimit.RequestResetDuration,
					TokenLastReset:       time.Now(),
					RequestLastReset:     time.Now(),
				}
				if err := validateRateLimit(&rateLimit); err != nil {
					return err
				}
				if err := h.configStore.CreateRateLimit(ctx, &rateLimit, tx); err != nil {
					return err
				}
				customer.RateLimitID = &rateLimit.ID
				customer.RateLimit = &rateLimit
			}
		}

		if err := h.configStore.UpdateCustomer(ctx, customer, tx); err != nil {
			return err
		}

		if rateLimitIDToDelete != "" {
			if err := tx.Delete(&configstoreTables.TableRateLimit{}, "id = ?", rateLimitIDToDelete).Error; err != nil {
				return err
			}
		}

		return nil
	}); err != nil {
		var badReqErr *badRequestError
		if errors.As(err, &badReqErr) {
			SendError(ctx, 400, err.Error())
			return
		}
		SendError(ctx, 500, "Failed to update customer")
		return
	}

	// Not swallowed on this path: the usage reset below is ordered after the reload
	// and depends on it, so continuing would clear usage against stale in-memory
	// config and still answer 200 with the pre-update customer.
	preloadedCustomer, err := h.governanceManager.ReloadCustomer(ctx, customer.ID)
	if err != nil {
		logger.Error("failed to reload customer: %v", err)
		SendError(ctx, 500, "Customer updated in database but failed to reload in-memory state")
		return
	}
	// Clear usage in the store that enforces spend. This runs after the reload,
	// which deliberately carries each budget's cached usage forward and would
	// otherwise undo the reset. Enterprise propagates it to cluster peers.
	if len(usageReset.budgetIDs) > 0 {
		if err := h.governanceManager.ResetBudgetUsageInMemory(ctx, BudgetUsageResetOwner{Kind: BudgetOwnerCustomer, ID: customer.ID}, usageReset.budgetIDs); err != nil {
			logger.Error("failed to reset in-memory budget usage for Customer: %v", err)
			SendError(ctx, 500, "Customer updated but budget usage reset did not take effect")
			return
		}
	}
	// Read the windows off the reload when it produced one, else off the row this
	// handler already holds: ReloadCustomer is allowed to return no entity.
	adoptBudgets, adoptRateLimitID := customer.Budgets, customer.RateLimitID
	if preloadedCustomer != nil {
		adoptBudgets, adoptRateLimitID = preloadedCustomer.Budgets, preloadedCustomer.RateLimitID
	}
	if err := h.adoptCalendarAlignment(ctx, alignmentSwitchedOn,
		BudgetUsageResetOwner{Kind: BudgetOwnerCustomer, ID: customer.ID}, adoptBudgets, adoptRateLimitID); err != nil {
		logger.Error("failed to adopt calendar alignment for Customer: %v", err)
		SendError(ctx, 500, "Customer updated but calendar alignment did not take effect")
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"message":  "Customer updated successfully",
		"customer": preloadedCustomer,
	})
}

// deleteCustomer handles DELETE /api/governance/customers/{customer_id} - Delete a customer
func (h *GovernanceHandler) deleteCustomer(ctx *fasthttp.RequestCtx) {
	customerID := ctx.UserValue("customer_id").(string)

	customer, err := h.configStore.GetCustomer(ctx, customerID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Customer not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve customer")
		return
	}
	err = h.governanceManager.RemoveCustomer(ctx, customer.ID)
	if err != nil {
		// But we ignore this error because its not
		logger.Error("failed to remove customer: %v", err)
	}
	if err := h.configStore.DeleteCustomer(ctx, customerID); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Customer not found")
			return
		}
		SendError(ctx, 500, "Failed to delete customer")
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"message": "Customer deleted successfully",
	})
}

// Budget and Rate Limit GET operations

// getBudgets handles GET /api/governance/budgets - Get all budgets
func (h *GovernanceHandler) getBudgets(ctx *fasthttp.RequestCtx) {
	budgets, err := h.configStore.GetBudgets(ctx)
	if err != nil {
		logger.Error("failed to retrieve budgets: %v", err)
		SendError(ctx, 500, "failed to retrieve budgets")
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"budgets": budgets,
		"count":   len(budgets),
	})
}

// getRateLimits handles GET /api/governance/rate-limits - Get all rate limits
func (h *GovernanceHandler) getRateLimits(ctx *fasthttp.RequestCtx) {
	rateLimits, err := h.configStore.GetRateLimits(ctx)
	if err != nil {
		logger.Error("failed to retrieve rate limits: %v", err)
		SendError(ctx, 500, "failed to retrieve rate limits")
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"rate_limits": rateLimits,
		"count":       len(rateLimits),
	})
}

// validateRateLimit validates the rate limit
func validateRateLimit(rateLimit *configstoreTables.TableRateLimit) error {
	if rateLimit.TokenMaxLimit != nil && (*rateLimit.TokenMaxLimit < 0 || *rateLimit.TokenMaxLimit == 0) {
		return fmt.Errorf("rate limit token max limit cannot be negative or zero: %d", *rateLimit.TokenMaxLimit)
	}
	// Only require token reset duration if token limit is set
	if rateLimit.TokenMaxLimit != nil {
		if rateLimit.TokenResetDuration == nil {
			return fmt.Errorf("rate limit token reset duration is required")
		}
		if d, err := configstoreTables.ParseDuration(*rateLimit.TokenResetDuration); err != nil || d <= 0 {
			return fmt.Errorf("invalid rate limit token reset duration (must be a positive duration): %s", *rateLimit.TokenResetDuration)
		}
	}
	if rateLimit.RequestMaxLimit != nil && (*rateLimit.RequestMaxLimit < 0 || *rateLimit.RequestMaxLimit == 0) {
		return fmt.Errorf("rate limit request max limit cannot be negative or zero: %d", *rateLimit.RequestMaxLimit)
	}
	// Only require request reset duration if request limit is set
	if rateLimit.RequestMaxLimit != nil {
		if rateLimit.RequestResetDuration == nil {
			return fmt.Errorf("rate limit request reset duration is required")
		}
		if d, err := configstoreTables.ParseDuration(*rateLimit.RequestResetDuration); err != nil || d <= 0 {
			return fmt.Errorf("invalid rate limit request reset duration (must be a positive duration): %s", *rateLimit.RequestResetDuration)
		}
	}
	return nil
}

func (h *GovernanceHandler) getConfiguredProviderSet(ctx context.Context) (map[schemas.ModelProvider]struct{}, error) {
	providers, err := h.configStore.GetProviders(ctx)
	if err != nil {
		return nil, err
	}
	providerSet := make(map[schemas.ModelProvider]struct{}, len(providers))
	for _, provider := range providers {
		providerName := schemas.ModelProvider(strings.TrimSpace(provider.Name))
		if providerName == "" {
			continue
		}
		providerSet[providerName] = struct{}{}
	}
	return providerSet, nil
}

// validateBudget validates the budget
func validateBudget(budget *configstoreTables.TableBudget) error {
	if budget.MaxLimit < 0 || budget.MaxLimit == 0 {
		return fmt.Errorf("budget max limit cannot be negative or zero: %.2f", budget.MaxLimit)
	}
	if budget.ResetDuration == "" {
		return fmt.Errorf("budget reset duration is required")
	}
	if d, err := configstoreTables.ParseDuration(budget.ResetDuration); err != nil || d <= 0 {
		return fmt.Errorf("invalid budget reset duration (must be a positive duration): %s", budget.ResetDuration)
	}
	if budget.ResetConfig != nil {
		if !configstoreTables.IsQuarterlyDuration(budget.ResetDuration) {
			return fmt.Errorf("reset_config is only valid on a quarterly reset duration, got: %s", budget.ResetDuration)
		}
		if month := budget.ResetConfig.QuarterStartMonth; month != 0 && (month < 1 || month > 12) {
			return fmt.Errorf("quarter_start_month must be between 1 and 12, got: %d", month)
		}
	}
	return nil
}

// Model Config CRUD Operations

// getModelConfigs handles GET /api/governance/model-configs - Get all model configs
func (h *GovernanceHandler) getModelConfigs(ctx *fasthttp.RequestCtx) {
	fromMemory := string(ctx.QueryArgs().Peek("from_memory")) == "true"
	if fromMemory {
		data := h.governanceManager.GetGovernanceData(ctx)
		if data == nil {
			SendError(ctx, 500, "Governance data is not available")
			return
		}
		search := string(ctx.QueryArgs().Peek("search"))
		scopeFilter := string(ctx.QueryArgs().Peek("scope"))
		scopeIDFilter := string(ctx.QueryArgs().Peek("scope_id"))
		providerFilter := string(ctx.QueryArgs().Peek("provider"))
		// Deep-copy into a value slice: top-level struct copy + nested pointer/slice fields
		// so we never alias or mutate live governance state during serialization.
		all := make([]configstoreTables.TableModelConfig, 0, len(data.ModelConfigs))
		for _, mc := range data.ModelConfigs {
			if mc == nil {
				continue
			}
			if search != "" && !strings.Contains(strings.ToLower(mc.ModelName), strings.ToLower(search)) {
				continue
			}
			if scopeFilter != "" && mc.Scope != scopeFilter {
				continue
			}
			if scopeIDFilter != "" {
				if mc.ScopeID == nil || *mc.ScopeID != scopeIDFilter {
					continue
				}
			}
			if providerFilter != "" {
				if mc.Provider == nil || *mc.Provider != providerFilter {
					continue
				}
			}
			clone := *mc
			if len(mc.Budgets) > 0 {
				bs := make([]configstoreTables.TableBudget, len(mc.Budgets))
				copy(bs, mc.Budgets)
				clone.Budgets = bs
			}
			if mc.Budget != nil {
				b := *mc.Budget
				clone.Budget = &b
			}
			if mc.RateLimit != nil {
				rl := *mc.RateLimit
				clone.RateLimit = &rl
			}
			all = append(all, clone)
		}
		totalCount := len(all)
		// Apply pagination if requested, otherwise return all (consistent with DB path).
		limitStr := string(ctx.QueryArgs().Peek("limit"))
		offsetStr := string(ctx.QueryArgs().Peek("offset"))
		offset := 0
		limit := totalCount
		if offsetStr != "" {
			if n, err := strconv.Atoi(offsetStr); err == nil && n >= 0 {
				offset = n
			}
		}
		if limitStr != "" {
			if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
				limit = n
			}
		}
		if offset > totalCount {
			offset = totalCount
		}
		end := offset + limit
		if end > totalCount {
			end = totalCount
		}
		page := all[offset:end]
		h.enrichModelConfigScopeNames(ctx, page)
		SendJSON(ctx, map[string]any{
			"model_configs": page,
			"count":         len(page),
			"total_count":   totalCount,
			"limit":         limit,
			"offset":        offset,
		})
		return
	}

	// Check for pagination parameters
	limitStr := string(ctx.QueryArgs().Peek("limit"))
	offsetStr := string(ctx.QueryArgs().Peek("offset"))
	search := string(ctx.QueryArgs().Peek("search"))
	scope := string(ctx.QueryArgs().Peek("scope"))
	scopeID := string(ctx.QueryArgs().Peek("scope_id"))
	provider := string(ctx.QueryArgs().Peek("provider"))

	if limitStr != "" || offsetStr != "" || search != "" || scope != "" || scopeID != "" || provider != "" {
		// Paginated path
		params := configstore.ModelConfigsQueryParams{
			Search:   search,
			Scope:    scope,
			ScopeID:  scopeID,
			Provider: provider,
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
		modelConfigs, totalCount, err := h.configStore.GetModelConfigsPaginated(ctx, params)
		if err != nil {
			logger.Error("failed to retrieve model configs: %v", err)
			SendError(ctx, 500, "Failed to retrieve model configs")
			return
		}
		h.enrichModelConfigScopeNames(ctx, modelConfigs)
		SendJSON(ctx, map[string]any{
			"model_configs": modelConfigs,
			"count":         len(modelConfigs),
			"total_count":   totalCount,
			"limit":         params.Limit,
			"offset":        params.Offset,
		})
		return
	}

	// Non-paginated path: return all model configs
	modelConfigs, err := h.configStore.GetModelConfigs(ctx)
	if err != nil {
		logger.Error("failed to retrieve model configs: %v", err)
		SendError(ctx, 500, "Failed to retrieve model configs")
		return
	}
	h.enrichModelConfigScopeNames(ctx, modelConfigs)
	SendJSON(ctx, map[string]any{
		"model_configs": modelConfigs,
		"count":         len(modelConfigs),
		"total_count":   len(modelConfigs),
		"limit":         len(modelConfigs),
		"offset":        0,
	})
}

// getModelConfig handles GET /api/governance/model-configs/{mc_id} - Get a specific model config
func (h *GovernanceHandler) getModelConfig(ctx *fasthttp.RequestCtx) {
	mcID := ctx.UserValue("mc_id").(string)
	mc, err := h.configStore.GetModelConfigByID(ctx, mcID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Model config not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve model config")
		return
	}
	h.resolveModelConfigScopeName(ctx, mc, map[string]string{})
	SendJSON(ctx, map[string]interface{}{
		"model_config": mc,
	})
}

// resolveModelConfigScopeName populates the transient ScopeName for a single non-global
// model config by dispatching to the resolver registered for mc.Scope. Unknown scopes
// (no resolver registered) and resolution failures are non-fatal — ScopeName stays empty
// and the UI falls back to rendering the scope_id. The cache lets callers dedupe lookups
// across many configs; it is keyed by (scope, scope_id) so distinct scopes never collide.
func (h *GovernanceHandler) resolveModelConfigScopeName(ctx context.Context, mc *configstoreTables.TableModelConfig, cache map[string]string) {
	if mc == nil || mc.Scope == "" || mc.ScopeID == nil {
		return
	}
	resolver, ok := lookupScopeNameResolver(mc.Scope)
	if !ok {
		return
	}
	id := *mc.ScopeID
	key := mc.Scope + "|" + id
	name, cached := cache[key]
	if !cached {
		if resolved, found := resolver(ctx, id); found {
			name = resolved
		}
		cache[key] = name
	}
	mc.ScopeName = name
}

// enrichModelConfigScopeNames populates ScopeName for each non-global config in the slice.
func (h *GovernanceHandler) enrichModelConfigScopeNames(ctx context.Context, configs []configstoreTables.TableModelConfig) {
	cache := map[string]string{}
	for i := range configs {
		h.resolveModelConfigScopeName(ctx, &configs[i], cache)
	}
}

// createModelConfig handles POST /api/governance/model-configs - Create a new model config
func (h *GovernanceHandler) createModelConfig(ctx *fasthttp.RequestCtx) {
	var req CreateModelConfigRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON")
		return
	}
	// Validate required fields
	if req.ModelName == "" {
		SendError(ctx, 400, "Model name is required")
		return
	}
	// Default and validate scope. Global is the implicit default (preserves
	// pre-scope behavior). Non-global scopes require a scope_id naming the target.
	// scopeCalendarAligned is inherited from the owning VK for virtual_key scope.
	scopeCalendarAligned := false
	if req.Scope == "" {
		req.Scope = configstoreTables.ModelConfigScopeGlobal
	}
	if !configstoreTables.IsValidModelConfigScope(req.Scope) {
		SendError(ctx, 400, fmt.Sprintf("Invalid scope %q", req.Scope))
		return
	}
	if req.Scope == configstoreTables.ModelConfigScopeGlobal {
		req.ScopeID = nil // normalize: global configs must not carry a scope_id
	} else {
		if req.ScopeID == nil || *req.ScopeID == "" {
			SendError(ctx, 400, "scope_id is required when scope is not global")
			return
		}
		// For the virtual_key scope, the scope_id must reference an existing VK.
		if req.Scope == configstoreTables.ModelConfigScopeVirtualKey {
			vk, vkErr := h.configStore.GetVirtualKey(ctx, *req.ScopeID)
			if vkErr != nil {
				if errors.Is(vkErr, configstore.ErrNotFound) {
					SendError(ctx, 400, fmt.Sprintf("Virtual key '%s' not found", *req.ScopeID))
				} else {
					logger.Error("failed to verify virtual key for model config scope: %v", vkErr)
					SendError(ctx, 500, "Failed to verify virtual key")
				}
				return
			}
			// Inherit calendar alignment from the owning VK so budgets reset consistently.
			scopeCalendarAligned = vk.CalendarAligned
		}
	}
	// Check if a model config with the same identity (scope, scope_id, model_name, provider) already exists
	existing, err := h.configStore.GetModelConfig(ctx, req.Scope, req.ScopeID, req.ModelName, req.Provider)
	if err != nil && err != configstore.ErrNotFound {
		logger.Error("failed to check existing model config: %v", err)
		SendError(ctx, 500, fmt.Sprintf("Failed to check existing model config: %v", err))
		return
	}
	if existing != nil {
		scopeDesc := "global"
		if req.Scope != configstoreTables.ModelConfigScopeGlobal {
			scopeDesc = fmt.Sprintf("%s '%s'", req.Scope, *req.ScopeID)
		}
		if req.Provider != nil {
			SendError(ctx, 409, fmt.Sprintf("Model config for model '%s' with provider '%s' (%s) already exists", req.ModelName, *req.Provider, scopeDesc))
		} else {
			SendError(ctx, 409, fmt.Sprintf("Model config for model '%s' (%s) already exists", req.ModelName, scopeDesc))
		}
		return
	}
	// Validate budgets if provided
	seenDurations := make(map[string]bool, len(req.Budgets))
	for i := range req.Budgets {
		if req.Budgets[i].MaxLimit < 0 {
			SendError(ctx, 400, fmt.Sprintf("Budget max_limit cannot be negative: %.2f", req.Budgets[i].MaxLimit))
			return
		}
		if d, err := configstoreTables.ParseDuration(req.Budgets[i].ResetDuration); err != nil || d <= 0 {
			SendError(ctx, 400, fmt.Sprintf("Invalid reset duration (must be a positive duration): %s", req.Budgets[i].ResetDuration))
			return
		}
		if seenDurations[req.Budgets[i].ResetDuration] {
			SendError(ctx, 400, fmt.Sprintf("Duplicate reset_duration in budgets: %s", req.Budgets[i].ResetDuration))
			return
		}
		seenDurations[req.Budgets[i].ResetDuration] = true
	}
	var mc configstoreTables.TableModelConfig
	if err := h.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		mc = configstoreTables.TableModelConfig{
			ID:              uuid.NewString(),
			ModelName:       req.ModelName,
			Provider:        req.Provider,
			Scope:           req.Scope,
			ScopeID:         req.ScopeID,
			CalendarAligned: scopeCalendarAligned,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		}
		// Create rate limit if provided (mc references it via RateLimitID, so create first).
		if req.RateLimit != nil {
			rateLimit := configstoreTables.TableRateLimit{
				ID:                   uuid.NewString(),
				TokenMaxLimit:        req.RateLimit.TokenMaxLimit,
				TokenResetDuration:   req.RateLimit.TokenResetDuration,
				RequestMaxLimit:      req.RateLimit.RequestMaxLimit,
				RequestResetDuration: req.RateLimit.RequestResetDuration,
				TokenLastReset:       time.Now(),
				RequestLastReset:     time.Now(),
			}
			if err := validateRateLimit(&rateLimit); err != nil {
				return err
			}
			if err := h.configStore.CreateRateLimit(ctx, &rateLimit, tx); err != nil {
				return err
			}
			mc.RateLimitID = &rateLimit.ID
			mc.RateLimit = &rateLimit
		}
		// Create the model config row first so its budgets can reference it via ModelConfigID.
		if err := h.configStore.CreateModelConfig(ctx, &mc, tx); err != nil {
			return err
		}
		// Create owned budgets (a model config may carry multiple).
		for _, b := range req.Budgets {
			budget := newBudgetFromRequest(b, mc.CalendarAligned)
			budget.ModelConfigID = &mc.ID
			if err := validateBudget(&budget); err != nil {
				return err
			}
			if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
				return err
			}
			mc.Budgets = append(mc.Budgets, budget)
		}
		return nil
	}); err != nil {
		logger.Error("failed to create model config: %v", err)
		SendError(ctx, 500, fmt.Sprintf("Failed to create model config: %v", err))
		return
	}
	// Reload model config in memory
	preloadedMC, err := h.governanceManager.ReloadModelConfig(ctx, mc.ID)
	if err != nil {
		logger.Error("failed to reload model config in memory: %v", err)
		preloadedMC = &mc
	}
	h.resolveModelConfigScopeName(ctx, preloadedMC, map[string]string{})
	SendJSON(ctx, map[string]interface{}{
		"message":      "Model config created successfully",
		"model_config": preloadedMC,
	})
}

// updateModelConfig handles PUT /api/governance/model-configs/{mc_id} - Update a model config
func (h *GovernanceHandler) updateModelConfig(ctx *fasthttp.RequestCtx) {
	mcID := ctx.UserValue("mc_id").(string)
	var req UpdateModelConfigRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON")
		return
	}
	// The operator's explicit "reset usage" choice. Carried into budget
	// reconciliation and collected there, so the in-memory store can be cleared
	// once the transaction commits.
	usageReset := &budgetUsageReset{requested: req.ResetBudgetUsage != nil && *req.ResetBudgetUsage}
	mc, err := h.configStore.GetModelConfigByID(ctx, mcID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Model config not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve model config")
		return
	}
	if err := h.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		// Track rate-limit ID to delete after updating the model config (to avoid FK constraint).
		var rateLimitIDToDelete string

		// Update fields if provided
		if req.ModelName != nil {
			mc.ModelName = *req.ModelName
		}
		// Update provider if provided in request
		if req.Provider != nil {
			mc.Provider = req.Provider
		}
		// Handle budget updates: req.Budgets is the full desired set. A non-nil empty
		// slice removes all budgets; omitting the field leaves them unchanged. Budgets
		// are owned via ModelConfigID, so no model-config FK juggling is needed.
		if req.Budgets != nil {
			if err := h.reconcileModelConfigBudgets(ctx, tx, mc, req.Budgets, usageReset); err != nil {
				return err
			}
		}
		// Handle rate limit updates
		if req.RateLimit != nil {
			// Check if rate limit values are empty - means remove rate limit (reset durations don't matter)
			rateLimitIsEmpty := req.RateLimit.TokenMaxLimit == nil && req.RateLimit.RequestMaxLimit == nil
			if rateLimitIsEmpty {
				// Mark rate limit for deletion after FK is removed
				if mc.RateLimitID != nil {
					rateLimitIDToDelete = *mc.RateLimitID
					mc.RateLimitID = nil
					mc.RateLimit = nil
				}
			} else if mc.RateLimitID != nil {
				// Update existing rate limit - set ALL fields from request (nil means clear)
				rateLimit := configstoreTables.TableRateLimit{}
				if err := tx.First(&rateLimit, "id = ?", *mc.RateLimitID).Error; err != nil {
					return err
				}
				// Set all fields from request - nil values will clear the field
				rateLimit.TokenMaxLimit = req.RateLimit.TokenMaxLimit
				rateLimit.TokenResetDuration = req.RateLimit.TokenResetDuration
				rateLimit.RequestMaxLimit = req.RateLimit.RequestMaxLimit
				rateLimit.RequestResetDuration = req.RateLimit.RequestResetDuration
				if err := validateRateLimit(&rateLimit); err != nil {
					return err
				}
				if err := h.configStore.UpdateRateLimit(ctx, &rateLimit, tx); err != nil {
					return err
				}
				mc.RateLimit = &rateLimit
			} else {
				// Create new rate limit
				rateLimit := configstoreTables.TableRateLimit{
					ID:                   uuid.NewString(),
					TokenMaxLimit:        req.RateLimit.TokenMaxLimit,
					TokenResetDuration:   req.RateLimit.TokenResetDuration,
					RequestMaxLimit:      req.RateLimit.RequestMaxLimit,
					RequestResetDuration: req.RateLimit.RequestResetDuration,
					TokenLastReset:       time.Now(),
					RequestLastReset:     time.Now(),
				}
				if err := validateRateLimit(&rateLimit); err != nil {
					return err
				}
				if err := h.configStore.CreateRateLimit(ctx, &rateLimit, tx); err != nil {
					return err
				}
				mc.RateLimitID = &rateLimit.ID
				mc.RateLimit = &rateLimit
			}
		}
		mc.UpdatedAt = time.Now()
		if err := h.configStore.UpdateModelConfig(ctx, mc, tx); err != nil {
			return err
		}

		// Now that the FK reference is removed, delete the orphaned rate limit.
		if rateLimitIDToDelete != "" {
			if err := tx.Delete(&configstoreTables.TableRateLimit{}, "id = ?", rateLimitIDToDelete).Error; err != nil {
				return err
			}
		}

		return nil
	}); err != nil {
		logger.Error("failed to update model config: %v", err)
		SendError(ctx, 500, fmt.Sprintf("Failed to update model config: %v", err))
		return
	}
	// Reload model config in memory (also reloads from DB to get full relationships).
	//
	// Not swallowed on this path: the usage reset below is ordered after the reload
	// and depends on it, so continuing would clear usage against stale in-memory
	// config and still answer 200 with the pre-update model config.
	updatedMC, err := h.governanceManager.ReloadModelConfig(ctx, mc.ID)
	if err != nil {
		logger.Error("failed to reload model config in memory: %v", err)
		SendError(ctx, 500, "Model config updated in database but failed to reload in-memory state")
		return
	}
	// Clear usage in the store that enforces spend. This runs after the reload,
	// which deliberately carries each budget's cached usage forward and would
	// otherwise undo the reset. Enterprise propagates it to cluster peers.
	if len(usageReset.budgetIDs) > 0 {
		if err := h.governanceManager.ResetBudgetUsageInMemory(ctx, BudgetUsageResetOwner{Kind: BudgetOwnerModelConfig, ID: mc.ID}, usageReset.budgetIDs); err != nil {
			logger.Error("failed to reset in-memory budget usage for Model config: %v", err)
			SendError(ctx, 500, "Model config updated but budget usage reset did not take effect")
			return
		}
	}
	h.resolveModelConfigScopeName(ctx, updatedMC, map[string]string{})
	SendJSON(ctx, map[string]interface{}{
		"message":      "Model config updated successfully",
		"model_config": updatedMC,
	})
}

// deleteModelConfig handles DELETE /api/governance/model-configs/{mc_id} - Delete a model config
func (h *GovernanceHandler) deleteModelConfig(ctx *fasthttp.RequestCtx) {
	mcID := ctx.UserValue("mc_id").(string)
	// Check if model config exists
	_, err := h.configStore.GetModelConfigByID(ctx, mcID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Model config not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve model config")
		return
	}
	// Delete the model config
	if err := h.configStore.DeleteModelConfig(ctx, mcID); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Model config not found")
			return
		}
		logger.Error("failed to delete model config: %v", err)
		SendError(ctx, 500, "Failed to delete model config")
		return
	}
	// Remove model config from in-memory store
	if err := h.governanceManager.RemoveModelConfig(ctx, mcID); err != nil {
		logger.Error("failed to remove model config from memory: %v", err)
		// Continue anyway, the config is deleted from DB
	}
	SendJSON(ctx, map[string]interface{}{
		"message": "Model config deleted successfully",
	})
}

// Provider Governance Operations

// ProviderGovernanceResponse represents a provider with its governance settings
type ProviderGovernanceResponse struct {
	Provider        string                            `json:"provider"`
	Budget          *configstoreTables.TableBudget    `json:"budget,omitempty"` // deprecated: use budgets
	Budgets         []configstoreTables.TableBudget   `json:"budgets,omitempty"`
	RateLimit       *configstoreTables.TableRateLimit `json:"rate_limit,omitempty"`
	CalendarAligned bool                              `json:"calendar_aligned"`
}

// modelConfigToProviderGovernance converts a model config to a ProviderGovernanceResponse.
// Returns false if the config does not represent provider-level governance
// (i.e. not scope=global, model_name="*", with a provider set).
func modelConfigToProviderGovernance(mc *configstoreTables.TableModelConfig) (ProviderGovernanceResponse, bool) {
	if mc == nil || mc.Scope != configstoreTables.ModelConfigScopeGlobal ||
		mc.ModelName != configstoreTables.ModelConfigAllModels || mc.Provider == nil {
		return ProviderGovernanceResponse{}, false
	}
	var budget *configstoreTables.TableBudget
	if len(mc.Budgets) > 0 {
		budget = &mc.Budgets[0]
	}
	budgets := make([]configstoreTables.TableBudget, len(mc.Budgets))
	copy(budgets, mc.Budgets)
	return ProviderGovernanceResponse{
		Provider:        *mc.Provider,
		Budget:          budget,
		Budgets:         budgets,
		RateLimit:       mc.RateLimit,
		CalendarAligned: mc.CalendarAligned,
	}, true
}

// getProviderGovernance handles GET /api/governance/providers - returns provider-level governance,
// now backed by all-models model configs scoped per provider.
func (h *GovernanceHandler) getProviderGovernance(ctx *fasthttp.RequestCtx) {
	fromMemory := string(ctx.QueryArgs().Peek("from_memory")) == "true"
	var result []ProviderGovernanceResponse
	if fromMemory {
		data := h.governanceManager.GetGovernanceData(ctx)
		if data == nil {
			SendError(ctx, 500, "Governance data is not available")
			return
		}
		for _, mc := range data.ModelConfigs {
			if r, ok := modelConfigToProviderGovernance(mc); ok {
				result = append(result, r)
			}
		}
	} else {
		configs, err := h.configStore.GetProviderGovernanceModelConfigs(ctx)
		if err != nil {
			logger.Error("failed to retrieve model configs: %v", err)
			SendError(ctx, 500, "Failed to retrieve providers")
			return
		}
		for i := range configs {
			if r, ok := modelConfigToProviderGovernance(&configs[i]); ok {
				result = append(result, r)
			}
		}
	}
	SendJSON(ctx, map[string]interface{}{
		"providers": result,
		"count":     len(result),
	})
}

// updateProviderGovernance handles PUT /api/governance/providers/{provider_name} - Update provider governance
func (h *GovernanceHandler) updateProviderGovernance(ctx *fasthttp.RequestCtx) {
	providerName, err := url.PathUnescape(ctx.UserValue("provider_name").(string))
	if err != nil {
		SendError(ctx, 400, "Invalid provider name encoding")
		return
	}
	var req UpdateProviderGovernanceRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON")
		return
	}
	// The operator's explicit "reset usage" choice. Carried into budget
	// reconciliation and collected there, so the in-memory store can be cleared
	// once the transaction commits.
	usageReset := &budgetUsageReset{requested: req.ResetBudgetUsage != nil && *req.ResetBudgetUsage}
	// See adoptCalendarAlignment: captured before the update so open windows are
	// re-anchored rather than reset when alignment is switched on.
	alignmentSwitchedOn := false
	if req.Budget != nil && req.Budgets != nil {
		SendError(ctx, 400, "only one of 'budget' or 'budgets' may be set")
		return
	}
	// Validate the provider exists.
	providers, err := h.configStore.GetProviders(ctx)
	if err != nil {
		SendError(ctx, 500, "Failed to retrieve providers")
		return
	}
	providerExists := false
	for i := range providers {
		if providers[i].Name == providerName {
			providerExists = true
			break
		}
	}
	if !providerExists {
		SendError(ctx, 404, "Provider not found")
		return
	}

	existing, err := h.configStore.GetModelConfig(ctx, configstoreTables.ModelConfigScopeGlobal, nil, configstoreTables.ModelConfigAllModels, &providerName)
	if err != nil && err != configstore.ErrNotFound {
		logger.Error("failed to load provider governance: %v", err)
		SendError(ctx, 500, fmt.Sprintf("Failed to load provider governance: %v", err))
		return
	}
	isNew := existing == nil
	mc := configstoreTables.TableModelConfig{
		ID:        uuid.NewString(),
		ModelName: configstoreTables.ModelConfigAllModels,
		Provider:  &providerName,
		Scope:     configstoreTables.ModelConfigScopeGlobal,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if existing != nil {
		mc = *existing
	}

	// Existing single owned budget, if any (provider governance is single-budget by API).
	var existingBudget *configstoreTables.TableBudget
	if len(mc.Budgets) > 0 {
		existingBudget = &mc.Budgets[0]
	}

	deleted := false
	if err := h.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		var rateLimitIDToDelete string

		// Apply CalendarAligned if provided.
		if req.CalendarAligned != nil {
			alignmentSwitchedOn = !mc.CalendarAligned && *req.CalendarAligned
			mc.CalendarAligned = *req.CalendarAligned
		}

		// Rate limit lifecycle (mc references it via RateLimitID, so resolve it before
		// persisting the model config below).
		if req.RateLimit != nil {
			if isRateLimitRemovalRequest(req.RateLimit) {
				if mc.RateLimitID != nil {
					rateLimitIDToDelete = *mc.RateLimitID
					mc.RateLimitID = nil
					mc.RateLimit = nil
				}
			} else if mc.RateLimitID != nil {
				rateLimit := configstoreTables.TableRateLimit{}
				if err := tx.First(&rateLimit, "id = ?", *mc.RateLimitID).Error; err != nil {
					return err
				}
				rateLimit.TokenMaxLimit = req.RateLimit.TokenMaxLimit
				rateLimit.TokenResetDuration = req.RateLimit.TokenResetDuration
				rateLimit.RequestMaxLimit = req.RateLimit.RequestMaxLimit
				rateLimit.RequestResetDuration = req.RateLimit.RequestResetDuration
				if err := validateRateLimit(&rateLimit); err != nil {
					return err
				}
				if err := h.configStore.UpdateRateLimit(ctx, &rateLimit, tx); err != nil {
					return err
				}
				mc.RateLimit = &rateLimit
			} else {
				rateLimit := configstoreTables.TableRateLimit{
					ID:                   uuid.NewString(),
					TokenMaxLimit:        req.RateLimit.TokenMaxLimit,
					TokenResetDuration:   req.RateLimit.TokenResetDuration,
					RequestMaxLimit:      req.RateLimit.RequestMaxLimit,
					RequestResetDuration: req.RateLimit.RequestResetDuration,
					TokenLastReset:       time.Now(),
					RequestLastReset:     time.Now(),
				}
				if err := validateRateLimit(&rateLimit); err != nil {
					return err
				}
				if err := h.configStore.CreateRateLimit(ctx, &rateLimit, tx); err != nil {
					return err
				}
				mc.RateLimitID = &rateLimit.ID
				mc.RateLimit = &rateLimit
			}
		}

		// Determine effective budgets: budgets field takes priority; budget field is coerced
		// into a single-element slice for backward compatibility.
		effectiveBudgets := req.Budgets
		if effectiveBudgets == nil && req.Budget != nil {
			if len(mc.Budgets) > 1 {
				return &badRequestError{err: fmt.Errorf("deprecated 'budget' field cannot be used when multiple budgets already exist; use 'budgets'")}
			}
			effectiveBudgets = coerceLegacyBudget(req.Budget, existingBudget)
			if effectiveBudgets == nil && !isBudgetRemovalRequest(req.Budget) {
				return &badRequestError{err: fmt.Errorf("both max_limit and reset_duration are required when creating a new budget")}
			}
		}

		willHaveBudget := len(mc.Budgets) > 0
		if effectiveBudgets != nil {
			willHaveBudget = len(*effectiveBudgets) > 0
		}

		hasGovernance := mc.RateLimitID != nil || willHaveBudget
		switch {
		case !hasGovernance && isNew:
			// Nothing to persist (removal request on a provider with no governance).
			return nil
		case !hasGovernance && !isNew:
			// All governance removed → delete the model config and its owned budgets.
			for _, b := range mc.Budgets {
				if err := tx.Delete(&configstoreTables.TableBudget{}, "id = ?", b.ID).Error; err != nil {
					return err
				}
			}
			if err := tx.Delete(&configstoreTables.TableModelConfig{}, "id = ?", mc.ID).Error; err != nil {
				return err
			}
			deleted = true
		case isNew:
			// Create the model config first so its budgets can reference it.
			if err := h.configStore.CreateModelConfig(ctx, &mc, tx); err != nil {
				return err
			}
		default:
			if err := h.configStore.UpdateModelConfig(ctx, &mc, tx); err != nil {
				return err
			}
		}

		// Budget reconciliation (mc row exists at this point for create cases).
		if !deleted && effectiveBudgets != nil {
			if err := h.reconcileModelConfigBudgets(ctx, tx, &mc, *effectiveBudgets, usageReset); err != nil {
				return err
			}
		}

		// Delete orphaned rate-limit row if it was unlinked.
		if rateLimitIDToDelete != "" {
			if err := tx.Delete(&configstoreTables.TableRateLimit{}, "id = ?", rateLimitIDToDelete).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		var badReqErr *badRequestError
		if errors.As(err, &badReqErr) {
			SendError(ctx, 400, err.Error())
			return
		}
		logger.Error("failed to update provider governance: %v", err)
		SendError(ctx, 500, fmt.Sprintf("Failed to update provider governance: %v", err))
		return
	}

	// Sync the in-memory governance store with the change.
	resp := ProviderGovernanceResponse{Provider: providerName}
	if deleted {
		if err := h.governanceManager.RemoveModelConfig(ctx, mc.ID); err != nil {
			logger.Error("failed to remove provider governance from memory: %v", err)
		}
	} else if len(mc.Budgets) > 0 || mc.RateLimitID != nil {
		if reloaded, err := h.governanceManager.ReloadModelConfig(ctx, mc.ID); err != nil {
			logger.Error("failed to reload provider governance in memory: %v", err)
			if r, ok := modelConfigToProviderGovernance(&mc); ok {
				resp = r
			}
		} else if r, ok := modelConfigToProviderGovernance(reloaded); ok {
			resp = r
		}
	}
	// Clear usage in the store that enforces spend. This runs after the reload,
	// which deliberately carries each budget's cached usage forward and would
	// otherwise undo the reset. Skipped when the governance was deleted outright,
	// since there is nothing left to reset.
	if !deleted && len(usageReset.budgetIDs) > 0 {
		// Provider governance stores its budgets on a VK-agnostic model config, which
		// is also what the reload above refreshes, so the reset is addressed to that
		// model config rather than to the provider itself.
		if err := h.governanceManager.ResetBudgetUsageInMemory(ctx, BudgetUsageResetOwner{Kind: BudgetOwnerModelConfig, ID: mc.ID}, usageReset.budgetIDs); err != nil {
			logger.Error("failed to reset in-memory budget usage for provider governance: %v", err)
			SendError(ctx, 500, "Provider governance updated but budget usage reset did not take effect")
			return
		}
	}
	if !deleted {
		if err := h.adoptCalendarAlignment(ctx, alignmentSwitchedOn,
			BudgetUsageResetOwner{Kind: BudgetOwnerModelConfig, ID: mc.ID}, mc.Budgets, mc.RateLimitID); err != nil {
			logger.Error("failed to adopt calendar alignment for provider governance: %v", err)
			SendError(ctx, 500, "Provider governance updated but calendar alignment did not take effect")
			return
		}
	}
	SendJSON(ctx, map[string]interface{}{
		"message":  "Provider governance updated successfully",
		"provider": resp,
	})
}

// deleteProviderGovernance handles DELETE /api/governance/providers/{provider_name} - removes
// provider-level governance by deleting the all-models model config for that provider.
func (h *GovernanceHandler) deleteProviderGovernance(ctx *fasthttp.RequestCtx) {
	providerName, err := url.PathUnescape(ctx.UserValue("provider_name").(string))
	if err != nil {
		SendError(ctx, 400, "Invalid provider name encoding")
		return
	}
	mc, err := h.configStore.GetModelConfig(ctx, configstoreTables.ModelConfigScopeGlobal, nil, configstoreTables.ModelConfigAllModels, &providerName)
	if err != nil {
		if err == configstore.ErrNotFound {
			// No provider-level governance to remove — treat as success (idempotent).
			SendJSON(ctx, map[string]interface{}{"message": "Provider governance deleted successfully"})
			return
		}
		logger.Error("failed to load provider governance: %v", err)
		SendError(ctx, 500, "Failed to delete provider governance")
		return
	}
	// DeleteModelConfig cascades to the owned budget/rate-limit rows.
	if err := h.configStore.DeleteModelConfig(ctx, mc.ID); err != nil {
		logger.Error("failed to delete provider governance: %v", err)
		SendError(ctx, 500, "Failed to delete provider governance")
		return
	}
	if err := h.governanceManager.RemoveModelConfig(ctx, mc.ID); err != nil {
		logger.Error("failed to remove provider governance from memory: %v", err)
	}
	SendJSON(ctx, map[string]interface{}{
		"message": "Provider governance deleted successfully",
	})
}

// Routing Rules CRUD Operations

// getRoutingRules retrieves all routing rules with optional filtering from database
func (h *GovernanceHandler) getRoutingRules(ctx *fasthttp.RequestCtx) {
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
func (h *GovernanceHandler) getRoutingRule(ctx *fasthttp.RequestCtx) {
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
func (h *GovernanceHandler) createRoutingRule(ctx *fasthttp.RequestCtx) {
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
	if err := governance.ValidateRoutingCELExpression(req.CelExpression); err != nil {
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
	if err := h.governanceManager.ReloadRoutingRule(ctx, rule.ID); err != nil {
		SendError(ctx, 500, fmt.Sprintf("Failed to reload routing rule in memory: %v, please restart bifrost to sync with the database", err))
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"message": "Routing rule created successfully",
		"rule":    rule,
	})
}

// updateRoutingRule updates an existing routing rule
func (h *GovernanceHandler) updateRoutingRule(ctx *fasthttp.RequestCtx) {
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
		if err := governance.ValidateRoutingCELExpression(*req.CelExpression); err != nil {
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
	if err := h.governanceManager.ReloadRoutingRule(ctx, rule.ID); err != nil {
		SendError(ctx, 500, fmt.Sprintf("Failed to reload routing rule in memory: %v, please restart bifrost to sync with the database", err))
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"message": "Routing rule updated successfully",
		"rule":    rule,
	})
}

// deleteRoutingRule deletes a routing rule
func (h *GovernanceHandler) deleteRoutingRule(ctx *fasthttp.RequestCtx) {
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
	if err := h.governanceManager.RemoveRoutingRule(ctx, ruleID); err != nil {
		logger.Error("failed to remove routing rule from memory: %v", err)
	}

	SendJSON(ctx, map[string]interface{}{
		"message": "Routing rule deleted successfully",
	})
}

// ---------------------------------------------------------------------------
// Pricing Override Operations
// ---------------------------------------------------------------------------

// CreatePricingOverrideRequest is the request payload for creating a governance
// pricing override.
type CreatePricingOverrideRequest struct {
	Name          string                      `json:"name"`
	ScopeKind     modelcatalog.ScopeKind      `json:"scope_kind"`
	UserID        *string                     `json:"user_id,omitempty"`
	VirtualKeyID  *string                     `json:"virtual_key_id,omitempty"`
	ProviderID    *string                     `json:"provider_id,omitempty"`
	ProviderKeyID *string                     `json:"provider_key_id,omitempty"`
	MatchType     modelcatalog.MatchType      `json:"match_type"`
	Pattern       string                      `json:"pattern"`
	RequestTypes  []schemas.RequestType       `json:"request_types,omitempty"`
	Patch         modelcatalog.PricingOptions `json:"patch,omitempty"`
}

// nullableString tracks whether a JSON string field was explicitly present in
// the request body (even as null), so the merge logic can distinguish "omitted"
// (leave existing value) from "set to null" (clear the value).
type nullableString struct {
	Value *string
	Set   bool
}

func (n *nullableString) UnmarshalJSON(b []byte) error {
	n.Set = true
	if string(b) == "null" {
		n.Value = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	n.Value = &s
	return nil
}

// UpdatePricingOverrideRequest is the request payload for updating a governance
// pricing override. All fields except Patch are optional — omitted fields are
// merged from the existing record. Patch is always replaced in full.
type UpdatePricingOverrideRequest struct {
	Name          *string                      `json:"name,omitempty"`
	ScopeKind     *modelcatalog.ScopeKind      `json:"scope_kind,omitempty"`
	UserID        nullableString               `json:"user_id"`
	VirtualKeyID  nullableString               `json:"virtual_key_id"`
	ProviderID    nullableString               `json:"provider_id"`
	ProviderKeyID nullableString               `json:"provider_key_id"`
	MatchType     *modelcatalog.MatchType      `json:"match_type,omitempty"`
	Pattern       *string                      `json:"pattern,omitempty"`
	RequestTypes  []schemas.RequestType        `json:"request_types,omitempty"`
	Patch         *modelcatalog.PricingOptions `json:"patch,omitempty"`
}

func (h *GovernanceHandler) getPricingOverrides(ctx *fasthttp.RequestCtx) {
	// Parse filter parameters
	var scopeKind, userID, virtualKeyID, providerID, providerKeyID *string
	if v := strings.TrimSpace(string(ctx.QueryArgs().Peek("scope_kind"))); v != "" {
		scopeKind = &v
	}
	if v := strings.TrimSpace(string(ctx.QueryArgs().Peek("user_id"))); v != "" {
		userID = &v
	}
	if v := strings.TrimSpace(string(ctx.QueryArgs().Peek("virtual_key_id"))); v != "" {
		virtualKeyID = &v
	}
	if v := strings.TrimSpace(string(ctx.QueryArgs().Peek("provider_id"))); v != "" {
		providerID = &v
	}
	if v := strings.TrimSpace(string(ctx.QueryArgs().Peek("provider_key_id"))); v != "" {
		providerKeyID = &v
	}

	// Check for pagination parameters
	limitStr := string(ctx.QueryArgs().Peek("limit"))
	offsetStr := string(ctx.QueryArgs().Peek("offset"))
	search := string(ctx.QueryArgs().Peek("search"))

	if limitStr != "" || offsetStr != "" || search != "" {
		params := configstore.PricingOverridesQueryParams{
			Search:        search,
			ScopeKind:     scopeKind,
			UserID:        userID,
			VirtualKeyID:  virtualKeyID,
			ProviderID:    providerID,
			ProviderKeyID: providerKeyID,
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
		overrides, totalCount, err := h.configStore.GetPricingOverridesPaginated(ctx, params)
		if err != nil {
			logger.Error("failed to retrieve pricing overrides: %v", err)
			SendError(ctx, fasthttp.StatusInternalServerError, "Failed to retrieve pricing overrides")
			return
		}
		SendJSON(ctx, map[string]interface{}{
			"pricing_overrides": overrides,
			"count":             len(overrides),
			"total_count":       totalCount,
			"limit":             params.Limit,
			"offset":            params.Offset,
		})
		return
	}

	// Non-paginated path: return all matching overrides (backward compatible)
	filters := configstore.PricingOverrideFilters{
		ScopeKind:     scopeKind,
		UserID:        userID,
		VirtualKeyID:  virtualKeyID,
		ProviderID:    providerID,
		ProviderKeyID: providerKeyID,
	}
	overrides, err := h.configStore.GetPricingOverrides(ctx, filters)
	if err != nil {
		logger.Error("failed to retrieve pricing overrides: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to retrieve pricing overrides")
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"pricing_overrides": overrides,
		"count":             len(overrides),
		"total_count":       len(overrides),
		"limit":             len(overrides),
		"offset":            0,
	})
}

func (h *GovernanceHandler) createPricingOverride(ctx *fasthttp.RequestCtx) {
	var req CreatePricingOverrideRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid JSON")
		return
	}

	name, err := normalizeAndValidatePricingOverrideName(req.Name)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	shape := modelcatalog.PricingOverride{
		ScopeKind:     req.ScopeKind,
		UserID:        req.UserID,
		VirtualKeyID:  req.VirtualKeyID,
		ProviderID:    req.ProviderID,
		ProviderKeyID: req.ProviderKeyID,
		MatchType:     req.MatchType,
		Pattern:       req.Pattern,
		RequestTypes:  req.RequestTypes,
	}
	if err := shape.IsValid(); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	patchJSON, err := sonic.Marshal(req.Patch)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid patch")
		return
	}

	now := time.Now()
	override := configstoreTables.TablePricingOverride{
		ID:               uuid.NewString(),
		Name:             name,
		ScopeKind:        string(req.ScopeKind),
		UserID:           normalizeOptionalString(req.UserID),
		VirtualKeyID:     normalizeOptionalString(req.VirtualKeyID),
		ProviderID:       normalizeOptionalString(req.ProviderID),
		ProviderKeyID:    normalizeOptionalString(req.ProviderKeyID),
		MatchType:        string(req.MatchType),
		Pattern:          strings.TrimSpace(req.Pattern),
		RequestTypes:     req.RequestTypes,
		PricingPatchJSON: string(patchJSON),
		ConfigHash:       "",
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if err := h.configStore.CreatePricingOverride(ctx, &override); err != nil {
		logger.Error("failed to create pricing override: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to create pricing override")
		return
	}

	if err := h.governanceManager.UpsertPricingOverride(ctx, &override); err != nil {
		logger.Error("failed to upsert pricing override: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to upsert pricing override")
		return
	}
	SendJSONWithStatus(ctx, map[string]interface{}{
		"message":          "Pricing override created successfully",
		"pricing_override": override,
	}, fasthttp.StatusCreated)
}

func (h *GovernanceHandler) updatePricingOverride(ctx *fasthttp.RequestCtx) {
	id := ctx.UserValue("id").(string)

	var req UpdatePricingOverrideRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid JSON")
		return
	}

	existing, err := h.configStore.GetPricingOverrideByID(ctx, id)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "Pricing override not found")
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to retrieve pricing override: %v", err))
		return
	}

	// Merge request fields onto the existing record; omitted fields keep their current values.
	merged := modelcatalog.PricingOverride{
		ScopeKind:     modelcatalog.ScopeKind(existing.ScopeKind),
		UserID:        existing.UserID,
		VirtualKeyID:  existing.VirtualKeyID,
		ProviderID:    existing.ProviderID,
		ProviderKeyID: existing.ProviderKeyID,
		MatchType:     modelcatalog.MatchType(existing.MatchType),
		Pattern:       existing.Pattern,
		RequestTypes:  existing.RequestTypes,
	}
	if req.ScopeKind != nil {
		merged.ScopeKind = *req.ScopeKind
		// Changing scope_kind resets all scope IDs; only what the request
		// explicitly provides will be kept.
		merged.UserID = nil
		merged.VirtualKeyID = nil
		merged.ProviderID = nil
		merged.ProviderKeyID = nil
	}
	if req.UserID.Set {
		merged.UserID = req.UserID.Value
	}
	if req.VirtualKeyID.Set {
		merged.VirtualKeyID = req.VirtualKeyID.Value
	}
	if req.ProviderID.Set {
		merged.ProviderID = req.ProviderID.Value
	}
	if req.ProviderKeyID.Set {
		merged.ProviderKeyID = req.ProviderKeyID.Value
	}
	if req.MatchType != nil {
		merged.MatchType = *req.MatchType
	}
	if req.Pattern != nil {
		merged.Pattern = *req.Pattern
	}
	if req.RequestTypes != nil {
		merged.RequestTypes = req.RequestTypes
	}

	if err := merged.IsValid(); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	// Resolve name: use provided value or fall back to existing.
	nameStr := existing.Name
	if req.Name != nil {
		nameStr, err = normalizeAndValidatePricingOverrideName(*req.Name)
		if err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, err.Error())
			return
		}
	}

	// Patch JSON: always replace in full with whatever is provided (or keep existing if omitted).
	pricingPatchJSON := existing.PricingPatchJSON
	if req.Patch != nil {
		b, err := sonic.Marshal(req.Patch)
		if err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, "Invalid patch")
			return
		}
		pricingPatchJSON = string(b)
	}

	override := configstoreTables.TablePricingOverride{
		ID:               id,
		Name:             nameStr,
		ScopeKind:        string(merged.ScopeKind),
		UserID:           normalizeOptionalString(merged.UserID),
		VirtualKeyID:     normalizeOptionalString(merged.VirtualKeyID),
		ProviderID:       normalizeOptionalString(merged.ProviderID),
		ProviderKeyID:    normalizeOptionalString(merged.ProviderKeyID),
		MatchType:        string(merged.MatchType),
		Pattern:          strings.TrimSpace(merged.Pattern),
		RequestTypes:     merged.RequestTypes,
		PricingPatchJSON: pricingPatchJSON,
		ConfigHash:       existing.ConfigHash,
		CreatedAt:        existing.CreatedAt,
		UpdatedAt:        time.Now(),
	}

	if err := h.configStore.UpdatePricingOverride(ctx, &override); err != nil {
		logger.Error("failed to update pricing override: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to update pricing override")
		return
	}

	if err := h.governanceManager.UpsertPricingOverride(ctx, &override); err != nil {
		logger.Error("failed to upsert pricing override: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to upsert pricing override")
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"message":          "Pricing override updated successfully",
		"pricing_override": override,
	})
}

func (h *GovernanceHandler) deletePricingOverride(ctx *fasthttp.RequestCtx) {
	id := ctx.UserValue("id").(string)
	if err := h.configStore.DeletePricingOverride(ctx, id); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "Pricing override not found")
			return
		}
		logger.Error("failed to delete pricing override: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to delete pricing override")
		return
	}

	if err := h.governanceManager.DeletePricingOverride(ctx, id); err != nil {
		logger.Warn("failed to delete pricing override from memory: %v", err)
	}
	SendJSON(ctx, map[string]interface{}{
		"message": "Pricing override deleted successfully",
	})
}

func normalizeAndValidatePricingOverrideName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", errors.New("name is required")
	}
	return trimmed, nil
}

func normalizeOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
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
func (h *GovernanceHandler) validateRoutingScopeID(ctx context.Context, scope string, scopeID string) error {
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

// quotaModelUsage is one entry in the quota endpoint's per-model breakdown: the budgets
// and rate limit (with their current usage) for a specific model governed under this VK.
// Mirrors how provider_configs surface per-provider governance.
type quotaModelUsage struct {
	ModelName string                            `json:"model_name"`
	Provider  *string                           `json:"provider,omitempty"` // nil means all providers
	Budgets   []configstoreTables.TableBudget   `json:"budgets,omitempty"`
	RateLimit *configstoreTables.TableRateLimit `json:"rate_limit,omitempty"`
}

// collectVKModelUsage loads the VK-scoped model configs for vk in a single query, then
// (1) reverse-maps the wildcard ("*") configs onto the VK and its provider configs — the
// same hydration hydrateVKGovernance performs — and (2) returns a per-model usage list
// built from the specific-model configs. Surfacing only VK-scoped governance keeps this
// self-service endpoint reporting the key's own usage (global/shared per-model limits are
// intentionally not exposed here). Returns an error on load failure so the endpoint fails
// closed (500) rather than silently returning empty governance — an empty result here is
// indistinguishable from a key that legitimately has no model configs.
func (h *GovernanceHandler) collectVKModelUsage(ctx context.Context, vk *configstoreTables.TableVirtualKey) ([]quotaModelUsage, error) {
	mcs, err := h.configStore.GetModelConfigsByScopeAndScopeIDs(ctx, configstoreTables.ModelConfigScopeVirtualKey, []string{vk.ID})
	if err != nil {
		logger.Error("failed to load model configs for VK quota: %v", err)
		return nil, err
	}

	ptrs := make([]*configstoreTables.TableModelConfig, len(mcs))
	for i := range mcs {
		ptrs[i] = &mcs[i]
	}
	applyVKGovernanceFromModelConfigs(vk, buildVKModelConfigIndex(ptrs))

	models := make([]quotaModelUsage, 0)
	for i := range mcs {
		mc := &mcs[i]
		if mc.ModelName == configstoreTables.ModelConfigAllModels {
			continue // wildcard configs are the VK-/provider-level governance handled above
		}
		models = append(models, quotaModelUsage{
			ModelName: mc.ModelName,
			Provider:  mc.Provider,
			Budgets:   mc.Budgets,
			RateLimit: mc.RateLimit,
		})
	}
	return models, nil
}

// quotaModelSpend is one model's actual usage drawn from request logs (independent of
// whether any governance config exists for that model) within a budget's current cycle.
type quotaModelSpend struct {
	Model         string  `json:"model"`
	Provider      string  `json:"provider,omitempty"`
	TotalRequests int64   `json:"total_requests"`
	TotalTokens   int64   `json:"total_tokens"`
	TotalCost     float64 `json:"total_cost"`
}

// quotaBudget is a VK budget plus the actual per-model spend (from request logs) accumulated
// in its current cycle [last_reset, now]. The TableBudget is embedded so the budget's own
// fields (id, max_limit, reset_duration, last_reset, current_usage, …) render flat alongside
// the breakdown — no field is duplicated. The per-model totals reconcile with current_usage
// (both measured since last_reset). models is empty when logging is disabled.
type quotaBudget struct {
	configstoreTables.TableBudget
	Models []quotaModelSpend `json:"per_model_usage"`
}

// buildBudgetsWithUsage wraps each budget with its per-model actual usage, queried from
// request logs over that budget's current cycle
func (h *GovernanceHandler) buildBudgetsWithUsage(ctx context.Context, vkID, usageUserID string, budgets []configstoreTables.TableBudget, now time.Time) ([]quotaBudget, error) {
	out := make([]quotaBudget, 0, len(budgets))
	for i := range budgets {
		b := &budgets[i]
		entry := quotaBudget{TableBudget: *b, Models: []quotaModelSpend{}}
		if h.logManager != nil {
			start := b.LastReset
			if b.CreatedAt.After(start) {
				start = b.CreatedAt
			}
			filters := &logstore.SearchFilters{
				StartTime: &start,
				EndTime:   &now,
			}
			if usageUserID != "" {
				filters.UserIDs = []string{usageUserID}
			} else {
				filters.VirtualKeyIDs = []string{vkID}
			}
			ranking, err := h.logManager.GetModelRankings(ctx, filters)
			if err != nil {
				logger.Error("failed to load per-model usage for VK quota (budget %s): %v", b.ID, err)
				return nil, err
			}
			if ranking != nil {
				for _, r := range ranking.Rankings {
					entry.Models = append(entry.Models, quotaModelSpend{
						Model:         r.Model,
						Provider:      r.Provider,
						TotalRequests: r.TotalRequests,
						TotalTokens:   r.TotalTokens,
						TotalCost:     r.TotalCost,
					})
				}
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

// getVirtualKeyQuota handles GET /api/governance/virtual-keys/quota
// This is a self-service endpoint — no admin auth required. The VK value in the header is the credential.
func (h *GovernanceHandler) getVirtualKeyQuota(ctx *fasthttp.RequestCtx) {
	// Extract virtual key using the same logic as the inference path (lib/ctx.go):
	// x-bf-vk accepts any value; other headers require the sk-bf- prefix.
	var vkValue string
	if v := string(ctx.Request.Header.Peek("x-bf-vk")); v != "" {
		vkValue = v
	} else if v := governance.ParseVirtualKeyFromFastHTTPRequest(ctx); v != nil {
		vkValue = *v
	}
	if vkValue == "" {
		SendError(ctx, 401, "Missing virtual key. Provide it via x-bf-vk header, Authorization Bearer, x-api-key, x-goog-api-key, or api-key header.")
		return
	}

	vk, err := h.configStore.GetVirtualKeyQuotaByValue(ctx, vkValue)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 401, "Virtual key not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve virtual key")
		return
	}

	// collectVKModelUsage hydrates the wildcard VK/provider governance (in place) and
	// returns the configured per-model limits — both from a single VK-scoped model-config load.
	// Fail closed: a load error must not degrade to empty governance (it would leave vk.Budgets
	// un-hydrated and report "budgets": [], silently hiding configured limits).
	models, err := h.collectVKModelUsage(ctx, vk)
	if err != nil {
		SendError(ctx, 500, "Failed to load model configurations")
		return
	}

	budgetRows := vk.Budgets
	rateLimit := vk.RateLimit
	usageUserID := ""
	if resolve := h.externalQuotaBudgetResolver; resolve != nil {
		ext, err := resolve(ctx, vk)
		if err != nil {
			SendError(ctx, 500, "Failed to load access-profile usage")
			return
		}
		if ext != nil {
			// AP-managed VK: surface the AP's governance verbatim in place of the VK's own
			// untracked mirror rows. Empty budgets / nil rate limit mean the AP has none.
			budgetRows = ext.Budgets
			rateLimit = ext.RateLimit
			usageUserID = ext.UsageUserID
		}
	}

	// Each budget carries its actual per-model spend (from request logs) for the current
	// cycle. Must run after collectVKModelUsage, which hydrates vk.Budgets.
	budgets, err := h.buildBudgetsWithUsage(ctx, vk.ID, usageUserID, budgetRows, time.Now())
	if err != nil {
		SendError(ctx, 500, "Failed to load per-model usage")
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"virtual_key_name": vk.Name,
		"is_active":        vk.IsActiveValue(),
		"budgets":          budgets,
		"rate_limit":       rateLimit,
		"provider_configs": vk.ProviderConfigs,
		"model_configs":    models,
	})
}
