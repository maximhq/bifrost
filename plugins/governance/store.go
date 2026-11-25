// Package governance provides the in-memory cache store for fast governance data access
package governance

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"gorm.io/gorm"
)

type (
	EntityWiseBudgets    map[string][]*configstoreTables.TableBudget
	EntityWiseRateLimits map[string][]*configstoreTables.TableRateLimit
)

// LocalGovernanceStore answers governance questions from the live store's collections (keys,
// teams, customers, model configs, providers, virtual MCPs, and budget and rate limit settings)
// and keeps budget and rate limit usage in its own maps, seeded from those collections.
type LocalGovernanceStore struct {
	// live holds the configuration records this store reads.
	live *livestore.Store
	// modelConfigIndex indexes model configs by the keys lookups ask for, which depend on the model
	// catalog: computed key -> ID of the config indexed there last. modelConfigKeyOf remembers each
	// config's key, so removing a config finds its entry even if the catalog has since changed.
	modelConfigIndex *livestore.Table[string, string]
	modelConfigKeyOf *livestore.Table[string, string]
	// unsubscribe cancels the subscriptions that keep the usage maps and the model config index
	// following the collections.
	unsubscribe []func()
	closeOnce   sync.Once
	// limitHooks is what SetLimitHooks set, nil until then.
	limitHooks atomic.Pointer[LimitHooks]
	// logTimestampPrecision is the precision the log store keeps request timestamps at; zero keeps
	// them exact. Init sets it from Config before any usage is charged.
	logTimestampPrecision time.Duration

	// budgets and rateLimits hold every limit's settings with its live usage, keyed by limit ID.
	// The limits the collections hold take their settings from them; other limits are installed
	// by their holders through UpsertBudgetConfig and SeedBudgetConfig. Usage is this store's.
	budgets    sync.Map // string -> *TableBudget
	rateLimits sync.Map // string -> *TableRateLimit

	// Last DB usages for budgets and rate limits
	LastDBUsagesBudgetsMu            sync.RWMutex       // Last DB usages for budgets
	LastDBUsagesRateLimitsRequestsMu sync.RWMutex       // Mutex for last DB usages for rate limits requests
	LastDBUsagesRateLimitsTokensMu   sync.RWMutex       // Mutex for last DB usages for rate limits tokens
	LastDBUsagesBudgets              map[string]float64 // Map for last DB usages for budgets
	LastDBUsagesRequestsRateLimits   map[string]int64   // Map for last DB usages for rate limits requests
	LastDBUsagesTokensRateLimits     map[string]int64   // Map for last DB usages for rate limits tokens

	// budgetResetGens counts how many times each budget's in-memory usage has been
	// zeroed. A dump snapshots budgets by pointer and writes them several
	// transactions later; a reset installs a fresh pointer and deliberately leaves
	// LastReset alone, so neither the snapshot nor the last_reset guard can tell
	// that the usage about to be persisted has since been cleared. LastReset cannot
	// carry this signal: an operator reset leaves it equal, and equal has to keep
	// meaning "persist this row" or steady-state usage would stop saving.
	//
	// Counts resets only, never usage writes, so a budget under traffic still dumps
	// every cycle. Guarded by LastDBUsagesBudgetsMu, which every reset already holds
	// to clear the matching baseline, so this adds no new lock.
	budgetResetGens map[string]uint64

	// rateLimitDeletions invalidates captures that might include a deleted rate limit.
	rateLimitDeletions atomic.Uint64

	// Config store for refresh operations
	configStore configstore.ConfigStore

	// Model catalog for cross-provider model matching (optional)
	modelCatalog *modelcatalog.ModelCatalog

	// Reset hooks allow wrappers to observe request-time local resets.
	resetHooksMu      sync.RWMutex
	onBudgetsReset    func([]*configstoreTables.TableBudget)
	onRateLimitsReset func([]*configstoreTables.TableRateLimit)

	// Logger
	logger schemas.Logger
}

type GovernanceData struct {
	VirtualKeys  map[string]*configstoreTables.TableVirtualKey `json:"virtual_keys"`
	Teams        map[string]*configstoreTables.TableTeam       `json:"teams"`
	Customers    map[string]*configstoreTables.TableCustomer   `json:"customers"`
	Users        map[string]*UserGovernance                    `json:"users"` // User-level governance (enterprise-only)
	Budgets      map[string]*configstoreTables.TableBudget     `json:"budgets"`
	RateLimits   map[string]*configstoreTables.TableRateLimit  `json:"rate_limits"`
	ModelConfigs []*configstoreTables.TableModelConfig         `json:"model_configs"`
	Providers    []*configstoreTables.TableProvider            `json:"providers"`
}

// BusinessUnitGovernance names the budgets and rate limit a business unit holds itself, as the
// in-memory store publishes them. BudgetIDs carries one id per reset window, like a team's.
type BusinessUnitGovernance struct {
	BudgetIDs   []string
	RateLimitID *string
}

// UserGovernance holds governance data for a user (enterprise-only)
type UserGovernance struct {
	BudgetID    *string `json:"budget_id,omitempty"`
	RateLimitID *string `json:"rate_limit_id,omitempty"`
}

// BudgetAndRateLimitStatus represents the current budget and rate limit usage state
// Exhaustion is determined by percent_used >= 100
type BudgetAndRateLimitStatus struct {
	BudgetPercentUsed           float64 `json:"budget_percent_used"`             // 0-100, >100 means exhausted
	RateLimitTokenPercentUsed   float64 `json:"rate_limit_token_percent_used"`   // 0-100, >100 means exhausted
	RateLimitRequestPercentUsed float64 `json:"rate_limit_request_percent_used"` // 0-100, >100 means exhausted
}

// GovernanceStore defines the interface for governance data access and policy evaluation.
//
// Error semantics contract:
//   - CheckRateLimit and CheckBudget return a non-nil error to indicate a governance/policy
//     violation (not an infrastructure/operational failure).
//   - Callers must treat any non-nil error from these methods as an explicit denial/violation
//     decision rather than a retryable infrastructure error.
//   - This contract ensures consistent behavior across implementations (e.g., in-memory,
//     DB-backed) and prevents retry loops on policy violations.
type GovernanceStore interface {
	// Close stops the store following the live store and releases what it registered there. The
	// plugin calls it once on Cleanup, so a reload leaves nothing of the old store behind; calling it
	// again does nothing.
	Close()
	// ModelConfigIndexKey is the model spelling a config for (model, provider) is indexed under:
	// the catalog base name for a provider-less config, the canonical spelling otherwise.
	ModelConfigIndexKey(model string, provider *string) string
	GetGovernanceData(ctx context.Context) *GovernanceData
	GetGovernanceUsageData(ctx context.Context) *GovernanceData
	GetVirtualKey(ctx context.Context, vkValue string) (*registry.VirtualKey, bool)
	GetVirtualKeyByID(ctx context.Context, vkID string) (*registry.VirtualKey, bool)
	// ResolvePermits reports the permits a request carries: the ones its caller holds, the permit
	// scoping the request, and the mode composing them.
	//
	// Everything is resolved from the request itself, starting with the identity the transport
	// settled on its grant, so a caller needs nothing but the context to ask.
	//
	// A store that knows only about virtual keys answers with the presented key's permit and nothing
	// scoping it. A store that resolves other holders answers for those too, including a caller whose
	// permits do not come from a key at all, and it may answer with several: a caller holding more
	// than one is read as holding all of them at once.
	//
	// Answering with nothing means one of two things the funnel tells apart by what was presented: a
	// request that presented nothing carries no access and is governed as unrestricted, and one that
	// presented something this store resolves permits from, which resolved to nothing, is refused.
	//
	// A store that resolves a project the request named answers the same way about that: the scoping
	// slot is filled when the request may be scoped by the project it named, and left empty when it
	// may not. Which of those happened is not this store's to report: the funnel refuses a request
	// that named a project and carries no scoping permit, exactly as it refuses one that presented a
	// credential nothing granted.
	//
	// levelMode says how the caller's levels combine (see grant.NewAccess), separately from mode.
	ResolvePermits(ctx *schemas.BifrostContext) (bases []schemas.Permit, levelMode grant.CompositionMode, scoping schemas.Permit, mode grant.CompositionMode)
	// GatherLimits assembles every limit an attempt answers to, once its provider and model are
	// known: the deployment's own for the pair, what funds each holder the request carries, and what
	// funds their use of the provider. Which holders pay is the store's to say: a store that composes
	// several possible payers settles here on who pays, and everything downstream (the checks, the
	// co-payers named on the log row, the charge) reads that one settled answer off the grant rather
	// than assembling its own.
	//
	// An error is a request whose payers cannot be settled at all, which the funnel refuses as such;
	// it is not a limit being exhausted, which the checks report.
	GatherLimits(ctx *schemas.BifrostContext, access schemas.Access, provider schemas.ModelProvider, model string) (budgets []schemas.Limit, rateLimits []schemas.Limit, err error)
	// The gather primitives below are one vocabulary: who holds the money (the deployment, or a
	// permit's holder) crossed with what scopes it (a provider, or a model on a provider), plus
	// HolderLimits for what funds the holder whichever provider serves. GatherLimits composes them;
	// nothing else decides who is charged. All are resolved per attempt because a request can fail
	// over to another provider or have its model rewritten, so none of them are facts about the
	// permit.

	// GlobalProviderLimits reports the deployment's own limits on a provider, spent by every
	// request that provider serves whatever granted it.
	GlobalProviderLimits(ctx context.Context, provider schemas.ModelProvider) (budgets []schemas.Limit, rateLimits []schemas.Limit)
	// GlobalModelLimits reports the deployment's own model-config limits covering the pair,
	// wildcard tiers included.
	GlobalModelLimits(ctx context.Context, provider schemas.ModelProvider, model string) (budgets []schemas.Limit, rateLimits []schemas.Limit)
	// HolderLimits reports what funds a permit's holder whichever provider serves the request: for a
	// virtual key, its own limits plus its team's and customer's. Not on the permit because these
	// cannot tell one provider from another, so load balancing has no use for them and asking once
	// per candidate answers the same question N times.
	//
	// This is the seam a deployment reimplements to fund requests from something other than a key.
	// Nothing downstream asks what kind of holder answered.
	HolderLimits(ctx context.Context, permit schemas.Permit) (budgets []schemas.Limit, rateLimits []schemas.Limit)
	// PermitProviderLimits reports what funds a permit's use of one provider: the limits configured
	// against that provider on the holder, and no other. These are the limits that can tell one
	// provider from another, which is what load balancing asks about, and they are gathered again
	// with the rest once the attempt's provider is settled. A permit describes what may be reached;
	// what that costs is the store's to know, keyed by the permit's identity.
	PermitProviderLimits(ctx context.Context, permit schemas.Permit, provider schemas.ModelProvider) (budgets []schemas.Limit, rateLimits []schemas.Limit)
	// PermitModelLimits reports the permit holder's own model-config limits covering the pair,
	// keyed by the permit's identity the way PermitProviderLimits is. The deployment's are not
	// folded in, so a caller composing several holders can ask about each without gathering the
	// same deployment rows once per holder.
	PermitModelLimits(ctx context.Context, permit schemas.Permit, provider schemas.ModelProvider, model string) (budgets []schemas.Limit, rateLimits []schemas.Limit)
	// CheckBudgets reports whether any of the given budgets is exhausted, and CheckRateLimits the
	// same for rate limits. What to check is the caller's to assemble; these only evaluate, so
	// neither knows what kind of holder is paying.
	CheckBudgets(ctx context.Context, limits []schemas.Limit, baselines map[string]float64) (Decision, error)
	CheckRateLimits(ctx context.Context, limits []schemas.Limit, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error)
	// ChargeBudgets bills a cost to the given budgets, and ChargeRateLimits counts a request against the
	// given rate limits. What to charge is the caller's to have settled: these are handed the same list
	// the checks were, so a request cannot be checked against one set and billed to another.
	ChargeBudgets(ctx context.Context, limits []schemas.Limit, cost float64) error
	ChargeRateLimits(ctx context.Context, limits []schemas.Limit, tokensUsed int64, shouldUpdateTokens bool, shouldUpdateRequests bool) error
	// CheckProviderCandidateExclusion reports whether a load-balancing candidate can serve this
	// request right now, answering with a Decision like the checks above. What to tell the caller
	// about a refusal is the caller's to word: it is the one writing a routing log. Only spending
	// decides here: what a candidate may reach was already settled by the access it came from.
	//
	// Only the limits that can tell one candidate from another are asked about: the deployment's own
	// on that provider, and what every permit that pays for the pair is funded by on it. A limit
	// covering every provider answers the same for all of them, so it can only exclude every
	// candidate at once, which is a refusal, and one the funnel states with a reason.
	//
	// The store answers because the store owns what pays for a candidate. A store that knows only
	// about virtual keys looks at what the presented key funds; a store that funds candidates from
	// something else answers for that too, and load balancing does not need to know the difference.
	CheckProviderCandidateExclusion(ctx *schemas.BifrostContext, access schemas.Access, candidate schemas.ProviderCandidate, model string) (Decision, error)
	// Budget crud.
	// UpsertBudgetConfig preserves in-memory CurrentUsage/LastReset on replacement —
	// use it for every config publish (fresh load or admin edit) so a concurrent
	// BumpBudgetUsage increment is never clobbered.
	LoadBudget(ctx context.Context, budgetID string) *configstoreTables.TableBudget
	BumpBudgetUsage(ctx context.Context, budgetID string, cost float64) error
	UpsertBudgetConfig(ctx context.Context, budgetID string, config *configstoreTables.TableBudget)
	DeleteBudget(ctx context.Context, budgetID string)
	// Rate limit crud. UpsertRateLimitConfig carries in-memory counter state
	// (token + request CurrentUsage/LastReset) forward across replacements —
	// same rationale as UpsertBudgetConfig.
	LoadRateLimit(ctx context.Context, rateLimitID string) *configstoreTables.TableRateLimit
	BumpRateLimitUsage(ctx context.Context, rateLimitID string, tokensUsed int64, shouldUpdateTokens, shouldUpdateRequests bool) error
	UpsertRateLimitConfig(ctx context.Context, rateLimitID string, config *configstoreTables.TableRateLimit)
	DeleteRateLimit(ctx context.Context, rateLimitID string)
	// In-memory usage updates (for VK-level)
	// In-memory usage updates for scoped model configs (mirror the global model updates).
	// scope/scopeID identify the owning entity; an empty scope or scopeID is a no-op.
	// In-memory reset checks (return items that need DB sync)
	ResetExpiredRateLimitsInMemory(ctx context.Context, rateLimitIDs ...string) []*RateLimitReset
	ResetExpiredBudgetsInMemory(ctx context.Context, budgetIDs ...string) []*configstoreTables.TableBudget
	// ResetBudgetUsageInMemory clears one budget's usage on operator request,
	// leaving its reset boundary untouched.
	ResetBudgetUsageInMemory(ctx context.Context, budgetID string) (*configstoreTables.TableBudget, bool)
	// AdoptCalendarAlignmentInMemory re-anchors a budget onto the calendar boundary
	// it has just been told to follow, preserving its usage. Reports whether the
	// boundary moved.
	AdoptCalendarAlignmentInMemory(ctx context.Context, budgetID string, now time.Time) bool
	// AdoptRateLimitCalendarAlignmentInMemory is the rate-limit counterpart,
	// applying the rule to the token and request counters independently.
	AdoptRateLimitCalendarAlignmentInMemory(ctx context.Context, rateLimitID string, now time.Time) bool
	// DB sync for expired items
	ResetExpiredRateLimits(ctx context.Context, resetRateLimits []*RateLimitReset) error
	ResetExpiredBudgets(ctx context.Context, resetBudgets []*configstoreTables.TableBudget) error
	// Provider and model-level usage updates (combined)
	// Dump operations capture counters and return a writer to invoke after releasing accounting locks.
	GetRateLimitDumper(ctx context.Context, tokenBaselines map[string]int64, requestBaselines map[string]int64) func(context.Context) error
	GetBudgetDumper(ctx context.Context, baselines map[string]float64) func(context.Context) error
	// User governance in-memory operations (enterprise-only, but interface defined here for compatibility)
	GetUserGovernance(ctx context.Context, userID string) (*UserGovernance, bool)
	CreateUserGovernanceInMemory(ctx context.Context, userID string, budget *configstoreTables.TableBudget, rateLimit *configstoreTables.TableRateLimit)
	UpdateUserGovernanceInMemory(ctx context.Context, userID string, budget *configstoreTables.TableBudget, rateLimit *configstoreTables.TableRateLimit)
	DeleteUserGovernanceInMemory(ctx context.Context, userID string)
	CreateUserNameInMemory(ctx context.Context, userID string, userName string)
	// Budget and rate limit status queries for routing with baseline support
	// GetBudgetAndRateLimitStatus reports how close the request is to what it answers to for a
	// provider and model pair, read from the access on its grant. Routing asks this before an attempt's
	// limits are settled, one pair at a time, so it gathers rather than reads the settled set.
	GetBudgetAndRateLimitStatus(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, budgetBaselines map[string]float64, tokenBaselines map[string]int64, requestBaselines map[string]int64) *BudgetAndRateLimitStatus
	// CollectModelScopedGovernanceIDs returns the budget and rate-limit IDs owned by
	// model configs matching (provider, model) across the global and virtual-key scopes,
	// plus whatever scopes a registered resolver derives from the IDs (see
	// RegisterExtraScopedIDsResolver). It takes the virtual key's ID rather than its
	// value, so a caller settling long after the request — batch accounting, which has
	// only a stored row — can still reach the same configs.
	CollectModelScopedGovernanceIDs(ctx context.Context, virtualKeyID string, userID string, provider schemas.ModelProvider, model string) (budgetIDs []string, rateLimitIDs []string)
}

// modelConfigKeysTable and modelConfigKeyOfTable name the tables a store indexes model configs in.
// A process runs one governance store, since the plugins API does not create, change or delete
// the governance plugin, so the names are fixed.
const (
	modelConfigKeysTable  = "governance_model_config_keys"
	modelConfigKeyOfTable = "governance_model_config_keys_of"
)

// NewLocalGovernanceStore creates a governance store over live, whose collections must already be
// loaded. It seeds budget and rate limit usage from the limits live holds, and follows every later
// change to them, to model configs, and through them to the keys lookups use. Close stops that.
//
// The modelCatalog parameter is optional (can be nil) and enables cross-provider model matching
// for governance lookups (e.g., "openai/gpt-4o" matching config for "gpt-4o"). configStore, when
// set, is where usage is persisted.
func NewLocalGovernanceStore(ctx context.Context, logger schemas.Logger, configStore configstore.ConfigStore, live *livestore.Store, modelCatalog *modelcatalog.ModelCatalog) (*LocalGovernanceStore, error) {
	if live == nil {
		return nil, fmt.Errorf("governance store: %w", livestore.ErrNoLiveStore)
	}
	keys, err := live.NewTable[string, string](ctx, modelConfigKeysTable)
	if err != nil {
		return nil, fmt.Errorf("failed to create the model config index: %w", err)
	}
	keyOf, err := live.NewTable[string, string](ctx, modelConfigKeyOfTable)
	if err != nil {
		_ = live.DropTable(ctx, keys.Name())
		return nil, fmt.Errorf("failed to create the model config index: %w", err)
	}
	store := &LocalGovernanceStore{
		live:                           live,
		modelConfigIndex:               keys,
		modelConfigKeyOf:               keyOf,
		configStore:                    configStore,
		logger:                         logger,
		modelCatalog:                   modelCatalog,
		LastDBUsagesBudgets:            make(map[string]float64),
		LastDBUsagesRequestsRateLimits: make(map[string]int64),
		LastDBUsagesTokensRateLimits:   make(map[string]int64),
	}
	store.follow(ctx)
	store.logger.Info("governance store initialized successfully")
	return store, nil
}

// follow subscribes to the limit and model config collections and then installs what they hold,
// so no change falls between the two. Installing a limit it already holds keeps its usage. Each
// change set of limits is applied under the limit hooks' lock, and its removals reported to them.
//
// The live store stamps each limit's record with its holder's calendar flag as it reads the row,
// so a record whose holder is unchanged, with the flag off before and on now, is the holder
// switching alignment on: that install adopts the calendar grid in the same swap (see
// upsertBudgetConfig and budgetSwitchedToCalendar). A limit first seen, one a holder takes over,
// or one whose holder was already aligned adopts nothing and keeps a reset that is really due.
func (gs *LocalGovernanceStore) follow(ctx context.Context) {
	gs.unsubscribe = append(gs.unsubscribe,
		gs.live.Collections.Budgets.Subscribe(func(ctx context.Context, changes []livestore.Change[registry.Budget]) {
			unlock := gs.lockLimits()
			defer unlock()
			var removed []string
			for _, c := range changes {
				if c.New == nil {
					gs.DeleteBudget(ctx, c.ID)
					removed = append(removed, c.ID)
					continue
				}
				row := c.New.TableBudget
				gs.seedBudgetConfig(ctx, c.ID, &row, budgetSwitchedToCalendar(c.Old, c.New))
			}
			gs.limitsRemoved(ctx, removed, nil)
		}),
		gs.live.Collections.RateLimits.Subscribe(func(ctx context.Context, changes []livestore.Change[registry.RateLimit]) {
			unlock := gs.lockLimits()
			defer unlock()
			var removed []string
			for _, c := range changes {
				if c.New == nil {
					gs.DeleteRateLimit(ctx, c.ID)
					removed = append(removed, c.ID)
					continue
				}
				row := c.New.TableRateLimit
				gs.seedRateLimitConfig(ctx, c.ID, &row, rateLimitSwitchedToCalendar(c.Old, c.New))
			}
			gs.limitsRemoved(ctx, nil, removed)
		}),
		gs.live.Collections.ModelConfigs.Subscribe(func(_ context.Context, changes []livestore.Change[registry.ModelConfig]) {
			for _, c := range changes {
				// An update is indexed in place: indexModelConfig moves the config off a key it
				// left, and a key it keeps is never empty for a concurrent lookup.
				switch {
				case c.New != nil:
					gs.indexModelConfig(c.New)
				case c.Old != nil:
					gs.unindexModelConfig(c.Old)
				}
			}
		}),
	)
	gs.live.Collections.Budgets.Range(func(b *registry.Budget) bool {
		row := b.TableBudget
		gs.SeedBudgetConfig(ctx, row.ID, &row)
		return true
	})
	gs.live.Collections.RateLimits.Range(func(r *registry.RateLimit) bool {
		row := r.TableRateLimit
		gs.SeedRateLimitConfig(ctx, row.ID, &row)
		return true
	})
	// In ID order, as the configs always loaded: of two configs under one key, the later answers.
	var configs []*registry.ModelConfig
	gs.live.Collections.ModelConfigs.Range(func(m *registry.ModelConfig) bool {
		configs = append(configs, m)
		return true
	})
	sort.Slice(configs, func(i, j int) bool { return configs[i].ID < configs[j].ID })
	for _, m := range configs {
		gs.indexModelConfig(m)
	}
}

// budgetSwitchedToCalendar reports whether a budget record's change is its holder switching
// calendar alignment on: a holder held it before and the same holder holds it now, with the flag
// off before and on now. A holder taking over a budget it did not hold, as a write handing an owner
// an existing budget does, is not a switch: like a holder first seen, it adopts nothing and keeps a
// reset that is really due.
func budgetSwitchedToCalendar(old, cur *registry.Budget) bool {
	if old == nil || cur == nil || old.IsCalendarAligned || !cur.IsCalendarAligned {
		return false
	}
	held := old.VirtualKeyID != nil || old.ProviderConfigID != nil || old.TeamID != nil ||
		old.CustomerID != nil || old.ModelConfigID != nil || old.Provider != ""
	return held && samePtr(old.VirtualKeyID, cur.VirtualKeyID) && samePtr(old.ProviderConfigID, cur.ProviderConfigID) &&
		samePtr(old.TeamID, cur.TeamID) && samePtr(old.CustomerID, cur.CustomerID) &&
		samePtr(old.ModelConfigID, cur.ModelConfigID) && old.KeyID == cur.KeyID && old.Provider == cur.Provider
}

// rateLimitSwitchedToCalendar is budgetSwitchedToCalendar for rate limits.
func rateLimitSwitchedToCalendar(old, cur *registry.RateLimit) bool {
	if old == nil || cur == nil || old.IsCalendarAligned || !cur.IsCalendarAligned {
		return false
	}
	held := old.KeyID != "" || old.TeamID != "" || old.CustomerID != "" || old.ModelConfigID != "" || old.Provider != ""
	return held && old.KeyID == cur.KeyID && old.TeamID == cur.TeamID && old.CustomerID == cur.CustomerID &&
		old.ModelConfigID == cur.ModelConfigID && old.Provider == cur.Provider
}

// samePtr reports whether a and b are both nil or point to equal values.
func samePtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Close stops following the live store and drops the model config index, so a plugin reload
// leaves nothing of the old store behind. Calling it again does nothing.
func (gs *LocalGovernanceStore) Close() {
	gs.closeOnce.Do(func() {
		for _, cancel := range gs.unsubscribe {
			cancel()
		}
		_ = gs.live.DropTable(context.Background(), gs.modelConfigIndex.Name())
		_ = gs.live.DropTable(context.Background(), gs.modelConfigKeyOf.Name())
	})
}

// LimitHooks let whoever owns a store keep its own bookkeeping atomic with the budget and rate-limit
// changes the store applies from the live store.
type LimitHooks struct {
	// Lock is held while the store installs or removes the budgets, or the rate limits, of one
	// change set from the live store. It must not be held by whoever refreshes the live store.
	Lock sync.Locker
	// Removed is called with Lock held, after the store dropped them, with the IDs of the budgets
	// and rate limits one change set removed.
	Removed func(ctx context.Context, budgetIDs, rateLimitIDs []string)
}

// SetLimitHooks sets the hooks the store runs around the limit changes it applies from the live
// store. Set them before the live store is refreshed.
func (gs *LocalGovernanceStore) SetLimitHooks(hooks LimitHooks) {
	gs.limitHooks.Store(&hooks)
}

// lockLimits takes the limit hooks' lock, when there is one, and returns what releases it.
func (gs *LocalGovernanceStore) lockLimits() func() {
	hooks := gs.limitHooks.Load()
	if hooks == nil || hooks.Lock == nil {
		return func() {}
	}
	hooks.Lock.Lock()
	return hooks.Lock.Unlock
}

// limitsRemoved tells the limit hooks which budgets and rate limits one change set removed.
func (gs *LocalGovernanceStore) limitsRemoved(ctx context.Context, budgetIDs, rateLimitIDs []string) {
	hooks := gs.limitHooks.Load()
	if hooks == nil || hooks.Removed == nil || len(budgetIDs)+len(rateLimitIDs) == 0 {
		return
	}
	hooks.Removed(ctx, budgetIDs, rateLimitIDs)
}

// SetResetHooks configures callbacks that run after successful local resets.
func (gs *LocalGovernanceStore) SetResetHooks(onBudgetsReset func([]*configstoreTables.TableBudget), onRateLimitsReset func([]*configstoreTables.TableRateLimit)) {
	gs.resetHooksMu.Lock()
	defer gs.resetHooksMu.Unlock()
	gs.onBudgetsReset = onBudgetsReset
	gs.onRateLimitsReset = onRateLimitsReset
}

func (gs *LocalGovernanceStore) getBudgetsResetHook() func([]*configstoreTables.TableBudget) {
	gs.resetHooksMu.RLock()
	defer gs.resetHooksMu.RUnlock()
	return gs.onBudgetsReset
}

func (gs *LocalGovernanceStore) getRateLimitsResetHook() func([]*configstoreTables.TableRateLimit) {
	gs.resetHooksMu.RLock()
	defer gs.resetHooksMu.RUnlock()
	return gs.onRateLimitsReset
}

// LoadBudget loads a budget by its ID from the local store.
func (gs *LocalGovernanceStore) LoadBudget(ctx context.Context, budgetID string) *configstoreTables.TableBudget {
	if budget, ok := gs.budgets.Load(budgetID); ok {
		if b, ok := budget.(*configstoreTables.TableBudget); ok {
			return b
		}
	}
	return nil
}

// storeBudget publishes a budget into the shared budgets map after re-deriving its
// finite-override lifecycle from the immutable grant.
//
// Every path that installs a budget funnels through here, which is what makes the
// derived override count safe. It is a pure function of (OverrideAnchorReset,
// OverrideCyclesTotal, LastReset), so deriving it at the moment of storage means no
// caller can install a stale count by accident: a config reload replaying a persisted
// row, an admin edit, access-profile propagation and the initial load from the database
// all converge on the same answer. It also means a restarting node recomputes the count
// rather than trusting whatever was last persisted.
//
// Callers stay responsible for preserving CurrentUsage and LastReset, since LastReset
// is what decides which window the derivation is relative to.
func (gs *LocalGovernanceStore) storeBudget(budgetID string, budget *configstoreTables.TableBudget) {
	if budget == nil {
		return
	}
	budget.RefreshOverrideCyclesRemaining()
	gs.budgets.Store(budgetID, budget)
}

// UpsertBudgetConfig publishes a budget config under budgetID, preserving the
// in-memory CurrentUsage and LastReset from any prior snapshot so a concurrent
// BumpBudgetUsage or ResetBudgetAt is never clobbered by a config replacement.
// First-writes (no prior entry) are handled via sync.Map.LoadOrStore so
// simultaneous first-writers collapse to a single insertion and the late
// arrival re-enters the CAS loop against the winner's snapshot.
//
// This method replaces the former blind StoreBudget: every caller installing
// a budget — whether fresh load or config replacement — should funnel through
// here so counters are never clobbered by an admin edit racing with a usage
// increment.
//
// The override grant (OverrideAmount / OverrideMode / OverrideCyclesTotal /
// OverrideAnchorReset) is taken from the incoming config, which is safe because a
// grant is immutable for its life: a reload carrying a stale row replays the same
// values, and RefreshOverrideCyclesRemaining then re-derives the spent count from
// the preserved LastReset. That is what stops a reload from handing back a cycle
// the reset path already spent. Previously the mutable remaining count came
// straight from the incoming config while LastReset stayed advanced, so the
// window's cycle could never be spent again and the override outlived its grant
// by a different amount on every node.
func (gs *LocalGovernanceStore) UpsertBudgetConfig(ctx context.Context, budgetID string, config *configstoreTables.TableBudget) {
	gs.upsertBudgetConfig(budgetID, config, false)
}

// upsertBudgetConfig is UpsertBudgetConfig, optionally adopting the calendar grid.
// adoptCalendar is set only when the owner has just switched alignment on, which
// follow reads from the budget's previous and new records. The flag and the moved
// boundary must be published by one swap: with the flag alone the preserved boundary
// reads as overdue, and a sweep or charge would clear the usage. Adoption reads the
// incoming config, so a duration changed by the same edit is the one adopted.
func (gs *LocalGovernanceStore) upsertBudgetConfig(budgetID string, config *configstoreTables.TableBudget, adoptCalendar bool) {
	if config == nil {
		return
	}
	for {
		raw, exists := gs.budgets.Load(budgetID)
		if !exists {
			// Publish a copy, never the caller's struct. Storing config directly would
			// mutate it in place via the refresh and then alias it as the live map
			// entry, so any later caller-side write would race BumpBudgetUsage's
			// clone-and-CAS. Callers legitimately pass a pointer into a slice they keep
			// using (BulkLoadUserAccessProfiles passes &p.Budgets[j]), so the copy has
			// to happen here rather than relying on every caller to make one.
			fresh := *config
			fresh.RefreshOverrideCyclesRemaining()
			if _, loaded := gs.budgets.LoadOrStore(budgetID, &fresh); !loaded {
				return
			}
			continue
		}
		old, ok := raw.(*configstoreTables.TableBudget)
		if !ok || old == nil {
			// Same reasoning as the not-exists branch above: storeBudget refreshes and
			// stores what it is given, so hand it a copy.
			replacement := *config
			gs.storeBudget(budgetID, &replacement)
			return
		}
		merged := *config
		merged.CurrentUsage = old.CurrentUsage
		merged.LastReset = old.LastReset
		merged.UsageResetEpoch = old.UsageResetEpoch
		if adoptCalendar {
			merged.AdoptCalendarAlignment(time.Now())
		}
		merged.RefreshOverrideCyclesRemaining()
		if gs.budgets.CompareAndSwap(budgetID, raw, &merged) {
			return
		}
	}
}

// DeleteBudget deletes a budget from the local store.
func (gs *LocalGovernanceStore) DeleteBudget(ctx context.Context, budgetID string) {
	gs.LastDBUsagesBudgetsMu.Lock()
	gs.budgets.Delete(budgetID)
	// Clean up LastDB baselines in the same critical section as the counter.
	delete(gs.LastDBUsagesBudgets, budgetID)
	// A dump holding this budget in an earlier snapshot must not resurrect its row.
	gs.markBudgetResetLocked(budgetID)
	gs.LastDBUsagesBudgetsMu.Unlock()
}

// SeedBudgetConfig publishes config and anchors its DB usage only when the budget
// is first seen. The baseline lock keeps resets and accounting captures from
// observing a published counter before its baseline. Existing counters and their
// baselines are preserved, including charges racing the config CAS.
func (gs *LocalGovernanceStore) SeedBudgetConfig(ctx context.Context, budgetID string, config *configstoreTables.TableBudget) {
	gs.seedBudgetConfig(ctx, budgetID, config, false)
}

// seedBudgetConfig is SeedBudgetConfig, adopting the calendar grid when adoptCalendar is set;
// see upsertBudgetConfig.
func (gs *LocalGovernanceStore) seedBudgetConfig(ctx context.Context, budgetID string, config *configstoreTables.TableBudget, adoptCalendar bool) {
	if config == nil {
		return
	}
	gs.LastDBUsagesBudgetsMu.Lock()
	defer gs.LastDBUsagesBudgetsMu.Unlock()
	known := gs.LoadBudget(ctx, budgetID) != nil
	gs.upsertBudgetConfig(budgetID, config, adoptCalendar)
	if !known {
		gs.LastDBUsagesBudgets[budgetID] = config.CurrentUsage
	}
}

// SetBudgetDBBaseline records the DB-authoritative usage for a budget so that
// gossip delta calculations (CurrentUsage - LastDBUsage) start from the correct
// base. Must be called whenever a budget with non-zero usage is loaded into
// memory outside of the initial loadFromDatabase path (e.g., access-profile
// propagation that preserves usage under a new ID).
func (gs *LocalGovernanceStore) SetBudgetDBBaseline(budgetID string, currentUsage float64) {
	gs.LastDBUsagesBudgetsMu.Lock()
	gs.LastDBUsagesBudgets[budgetID] = currentUsage
	gs.LastDBUsagesBudgetsMu.Unlock()
}

// markBudgetResetLocked records that a budget's in-memory usage was cleared, so a
// dump that snapshotted the pre-reset value can recognise its row as stale.
//
// The caller must already hold LastDBUsagesBudgetsMu for writing. Every reset site
// does, because clearing the gossip baseline and invalidating in-flight dumps are
// the same event and must not be observable half-done.
func (gs *LocalGovernanceStore) markBudgetResetLocked(budgetID string) {
	if gs.budgetResetGens == nil {
		gs.budgetResetGens = make(map[string]uint64)
	}
	gs.budgetResetGens[budgetID]++
}

// budgetResetGensSnapshot copies the current reset generations. A dump takes this
// alongside its budget snapshot and re-compares before writing.
func (gs *LocalGovernanceStore) budgetResetGensSnapshot() map[string]uint64 {
	gs.LastDBUsagesBudgetsMu.RLock()
	defer gs.LastDBUsagesBudgetsMu.RUnlock()
	gens := make(map[string]uint64, len(gs.budgetResetGens))
	for budgetID, gen := range gs.budgetResetGens {
		gens[budgetID] = gen
	}
	return gens
}

// keepUnresetRows returns the rows of a dump batch whose budget has not been reset
// since snapshot was taken. A row that has been reset carries the usage the reset
// cleared, so writing it would put that number straight back.
//
// Dropping is safe and self-correcting: the post-reset usage is already in memory,
// so the next dump cycle persists it a few seconds later. Re-checked per batch
// rather than once per dump because batches are separated by database round trips,
// which is time enough for a reset to land between them.
func (gs *LocalGovernanceStore) keepUnresetRows(batch []budgetDumpRow, snapshot map[string]uint64) []budgetDumpRow {
	gs.LastDBUsagesBudgetsMu.RLock()
	defer gs.LastDBUsagesBudgetsMu.RUnlock()
	stale := 0
	for _, row := range batch {
		if gs.budgetResetGens[row.ID] != snapshot[row.ID] {
			stale++
		}
	}
	if stale == 0 {
		return batch
	}
	kept := make([]budgetDumpRow, 0, len(batch)-stale)
	for _, row := range batch {
		if gs.budgetResetGens[row.ID] == snapshot[row.ID] {
			kept = append(kept, row)
		}
	}
	return kept
}

// LoadRateLimit loads a rate limit by its ID from the local store.
func (gs *LocalGovernanceStore) LoadRateLimit(ctx context.Context, rateLimitID string) *configstoreTables.TableRateLimit {
	if rateLimit, ok := gs.rateLimits.Load(rateLimitID); ok {
		if rl, ok := rateLimit.(*configstoreTables.TableRateLimit); ok {
			return rl
		}
	}
	return nil
}

// UpsertRateLimitConfig publishes a rate-limit config under rateLimitID,
// preserving in-memory token and request counter state (TokenCurrentUsage /
// TokenLastReset / RequestCurrentUsage / RequestLastReset) from any prior
// snapshot. Same CAS-retry contract as UpsertBudgetConfig.
func (gs *LocalGovernanceStore) UpsertRateLimitConfig(ctx context.Context, rateLimitID string, config *configstoreTables.TableRateLimit) {
	gs.upsertRateLimitConfig(rateLimitID, config, false)
}

// upsertRateLimitConfig is the rate-limit counterpart of upsertBudgetConfig. Token
// and request windows adopt independently.
func (gs *LocalGovernanceStore) upsertRateLimitConfig(rateLimitID string, config *configstoreTables.TableRateLimit, adoptCalendar bool) {
	if config == nil {
		return
	}
	for {
		raw, exists := gs.rateLimits.Load(rateLimitID)
		if !exists {
			if _, loaded := gs.rateLimits.LoadOrStore(rateLimitID, config); !loaded {
				return
			}
			continue
		}
		old, ok := raw.(*configstoreTables.TableRateLimit)
		if !ok || old == nil {
			gs.rateLimits.Store(rateLimitID, config)
			return
		}
		merged := *config
		merged.TokenCurrentUsage = old.TokenCurrentUsage
		merged.TokenLastReset = old.TokenLastReset
		merged.RequestCurrentUsage = old.RequestCurrentUsage
		merged.RequestLastReset = old.RequestLastReset
		merged.TokenUsageResetEpoch = old.TokenUsageResetEpoch
		merged.RequestUsageResetEpoch = old.RequestUsageResetEpoch
		if adoptCalendar {
			merged.AdoptCalendarAlignment(time.Now())
		}
		if gs.rateLimits.CompareAndSwap(rateLimitID, raw, &merged) {
			return
		}
	}
}

// DeleteRateLimit deletes a rate limit from the local store.
func (gs *LocalGovernanceStore) DeleteRateLimit(ctx context.Context, rateLimitID string) {
	gs.LastDBUsagesRateLimitsRequestsMu.Lock()
	gs.LastDBUsagesRateLimitsTokensMu.Lock()
	if _, deleted := gs.rateLimits.LoadAndDelete(rateLimitID); deleted {
		gs.rateLimitDeletions.Add(1)
	}
	// Hold both locks so seeding cannot publish a replacement during cleanup.
	delete(gs.LastDBUsagesTokensRateLimits, rateLimitID)
	delete(gs.LastDBUsagesRequestsRateLimits, rateLimitID)
	gs.LastDBUsagesRateLimitsTokensMu.Unlock()
	gs.LastDBUsagesRateLimitsRequestsMu.Unlock()
}

// SeedRateLimitConfig publishes config and anchors both DB usage dimensions
// only when the rate limit is first seen. Request then token locking matches
// resets and accounting captures; existing counters and baselines stay intact.
func (gs *LocalGovernanceStore) SeedRateLimitConfig(ctx context.Context, rateLimitID string, config *configstoreTables.TableRateLimit) {
	gs.seedRateLimitConfig(ctx, rateLimitID, config, false)
}

// seedRateLimitConfig is SeedRateLimitConfig, adopting the calendar grid when adoptCalendar is
// set; see upsertRateLimitConfig.
func (gs *LocalGovernanceStore) seedRateLimitConfig(ctx context.Context, rateLimitID string, config *configstoreTables.TableRateLimit, adoptCalendar bool) {
	if config == nil {
		return
	}
	gs.LastDBUsagesRateLimitsRequestsMu.Lock()
	defer gs.LastDBUsagesRateLimitsRequestsMu.Unlock()
	gs.LastDBUsagesRateLimitsTokensMu.Lock()
	defer gs.LastDBUsagesRateLimitsTokensMu.Unlock()
	known := gs.LoadRateLimit(ctx, rateLimitID) != nil
	gs.upsertRateLimitConfig(rateLimitID, config, adoptCalendar)
	if !known {
		gs.LastDBUsagesTokensRateLimits[rateLimitID] = config.TokenCurrentUsage
		gs.LastDBUsagesRequestsRateLimits[rateLimitID] = config.RequestCurrentUsage
	}
}

// SetRateLimitDBBaseline records the DB-authoritative usage for a rate limit so
// that gossip delta calculations (TokenCurrentUsage - LastDBTokenUsage) start
// from the correct base. Must be called whenever a rate limit with non-zero
// usage is loaded into memory outside of the initial loadFromDatabase path
// (e.g., access-profile propagation that preserves usage under a new ID).
func (gs *LocalGovernanceStore) SetRateLimitDBBaseline(rateLimitID string, tokenUsage int64, requestUsage int64) {
	gs.LastDBUsagesRateLimitsTokensMu.Lock()
	gs.LastDBUsagesTokensRateLimits[rateLimitID] = tokenUsage
	gs.LastDBUsagesRateLimitsTokensMu.Unlock()
	gs.LastDBUsagesRateLimitsRequestsMu.Lock()
	gs.LastDBUsagesRequestsRateLimits[rateLimitID] = requestUsage
	gs.LastDBUsagesRateLimitsRequestsMu.Unlock()
}

// maxRequestTimeResetAttempts bounds how many times a Bump* usage loop defers
// to the shared reset path before falling back to an inline reset. A converging
// target needs exactly one attempt; the bound exists so a non-converging target
// (issue #4851 class) can never spin a request-path goroutine at 100% CPU.
const maxRequestTimeResetAttempts = 3

// BumpBudgetUsage atomically increments CurrentUsage on the budget identified
// by budgetID and, as a side effect, zeros CurrentUsage / advances LastReset
// when the rolling ResetDuration has elapsed. Uses sync.Map.CompareAndSwap so
// concurrent callers on the same budget never drop increments — a lost CAS
// retries against the winner's snapshot. No-op when the budget is absent.
//
// This is the serialisation point for every usage increment: callers MUST
// funnel through this method (directly or via one of the higher-level
// ChargeBudgets) rather than doing a plain
// Load → clone → mutate → Store, which races.
func (gs *LocalGovernanceStore) BumpBudgetUsage(ctx context.Context, budgetID string, cost float64) error {
	requestStartedAt := gs.usageLogTimestamp(ctx)
	resetAttempts := 0
	for {
		raw, exists := gs.budgets.Load(budgetID)
		if !exists || raw == nil {
			return nil
		}
		old, ok := raw.(*configstoreTables.TableBudget)
		if !ok || old == nil {
			return nil
		}
		target := gs.budgetResetTarget(old, time.Now())
		if target != nil && resetAttempts < maxRequestTimeResetAttempts {
			resetAttempts++
			gs.ResetExpiredBudgetsInMemory(ctx, budgetID)
			continue
		}
		clone := *old
		if target != nil {
			// The reset target is still due after maxRequestTimeResetAttempts
			// shared-path resets, so it is not converging (issue #4851 class:
			// a target function bug or bad duration slipped past validation).
			// Reset inline in the clone so this loop always terminates,
			// zeroing the LastDB baseline exactly like
			// resetExpiredBudgetFromSnapshot so DB delta folding stays
			// consistent. Only the reset hook (DB persistence of LastReset)
			// is skipped on the request path.
			//
			// budgetResetTarget returns a window boundary, so applying it makes
			// the next evaluation return nil and this branch is unreachable for
			// any valid duration. It survives purely as a termination guard.
			//
			// Skipping the hook is safe for the override lifecycle: the remaining
			// count is derived from the grant and LastReset, so nothing needs
			// persisting here beyond LastReset itself, which the next dump writes.
			gs.logger.Error("budget %s reset target not converging after %d resets; applying inline reset to avoid request-path spin", budgetID, resetAttempts)
			clone.CurrentUsage = 0
			clone.LastReset = *target
			clone.UsageResetEpoch = max(old.UsageResetEpoch, target.UnixNano())
			clone.RefreshOverrideCyclesRemaining()
			gs.LastDBUsagesBudgetsMu.Lock()
		}
		if countsAfterUsageReset(requestStartedAt, clone.UsageResetEpoch) {
			clone.CurrentUsage += cost
		} else if target == nil {
			return nil
		}
		swapped := gs.budgets.CompareAndSwap(budgetID, raw, &clone)
		if target != nil {
			if swapped {
				gs.LastDBUsagesBudgets[budgetID] = 0
				gs.markBudgetResetLocked(budgetID)
			}
			gs.LastDBUsagesBudgetsMu.Unlock()
		}
		if swapped {
			return nil
		}
	}
}

// usageLogTimestamp applies the log backend's timestamp precision before comparing
// a request's attribution time against the exact reset epoch.
func (gs *LocalGovernanceStore) usageLogTimestamp(ctx context.Context) time.Time {
	startedAt, _ := ctx.Value(usageRequestStartKey).(time.Time)
	return startedAt.Truncate(gs.logTimestampPrecision)
}

// SetLogTimestampPrecision sets the precision the log store keeps request timestamps at, so a charge
// is compared with a usage reset at the precision the logs record it; zero keeps them exact. Call it
// before the plugin charges anything, as Init and InitFromStore do.
func (gs *LocalGovernanceStore) SetLogTimestampPrecision(precision time.Duration) {
	gs.logTimestampPrecision = precision
}

// countsAfterUsageReset matches recovery's inclusive cutoff for an actual clear.
// A missing start time or unknown reset cutoff leaves the charge eligible.
func countsAfterUsageReset(startedAt time.Time, resetEpoch int64) bool {
	return resetEpoch == 0 || startedAt.IsZero() || startedAt.UnixNano() >= resetEpoch
}

// BumpRateLimitUsage atomically increments the token and/or request counters on
// the rate limit identified by rateLimitID and, as a side effect, zeros the
// relevant counter / advances its LastReset when the rolling
// TokenResetDuration / RequestResetDuration has elapsed. Same CAS-retry
// contract as BumpBudgetUsage. Eligibility is checked independently for each
// dimension against the reset evidence in the same snapshot as the write.
func (gs *LocalGovernanceStore) BumpRateLimitUsage(ctx context.Context, rateLimitID string, tokensUsed int64, shouldUpdateTokens, shouldUpdateRequests bool) error {
	requestStartedAt := gs.usageLogTimestamp(ctx)
	resetAttempts := 0
	for {
		raw, exists := gs.rateLimits.Load(rateLimitID)
		if !exists || raw == nil {
			return nil
		}
		old, ok := raw.(*configstoreTables.TableRateLimit)
		if !ok || old == nil {
			return nil
		}
		tokenNewLastReset, requestNewLastReset := gs.rateLimitResetTargets(old, time.Now())
		if (tokenNewLastReset != nil || requestNewLastReset != nil) && resetAttempts < maxRequestTimeResetAttempts {
			resetAttempts++
			gs.ResetExpiredRateLimitsInMemory(ctx, rateLimitID)
			continue
		}
		clone := *old
		if tokenNewLastReset != nil || requestNewLastReset != nil {
			// The reset target is still due after maxRequestTimeResetAttempts
			// shared-path resets, so it is not converging (issue #4851 class:
			// a target function bug or bad duration slipped past validation).
			// Reset inline in the clone so this loop always terminates,
			// zeroing the LastDB baselines exactly like
			// resetExpiredRateLimitFromSnapshot so DB delta folding stays
			// consistent. Only the reset hook (DB persistence of LastReset)
			// is skipped on the request path.
			gs.logger.Error("rate limit %s reset target not converging after %d resets; applying inline reset to avoid request-path spin", rateLimitID, resetAttempts)
			gs.LastDBUsagesRateLimitsRequestsMu.Lock()
			gs.LastDBUsagesRateLimitsTokensMu.Lock()
			if tokenNewLastReset != nil {
				clone.TokenCurrentUsage = 0
				clone.TokenLastReset = *tokenNewLastReset
				clone.TokenUsageResetEpoch = max(old.TokenUsageResetEpoch, tokenNewLastReset.UnixNano())
			}
			if requestNewLastReset != nil {
				clone.RequestCurrentUsage = 0
				clone.RequestLastReset = *requestNewLastReset
				clone.RequestUsageResetEpoch = max(old.RequestUsageResetEpoch, requestNewLastReset.UnixNano())
			}
		}
		countTokens := shouldUpdateTokens && countsAfterUsageReset(requestStartedAt, clone.TokenUsageResetEpoch)
		countRequests := shouldUpdateRequests && countsAfterUsageReset(requestStartedAt, clone.RequestUsageResetEpoch)
		if !countTokens && !countRequests && tokenNewLastReset == nil && requestNewLastReset == nil {
			return nil
		}
		if countTokens {
			clone.TokenCurrentUsage += tokensUsed
		}
		if countRequests {
			clone.RequestCurrentUsage++
		}
		swapped := gs.rateLimits.CompareAndSwap(rateLimitID, raw, &clone)
		if tokenNewLastReset != nil || requestNewLastReset != nil {
			if swapped {
				if tokenNewLastReset != nil {
					gs.LastDBUsagesTokensRateLimits[rateLimitID] = 0
				}
				if requestNewLastReset != nil {
					gs.LastDBUsagesRequestsRateLimits[rateLimitID] = 0
				}
			}
			gs.LastDBUsagesRateLimitsTokensMu.Unlock()
			gs.LastDBUsagesRateLimitsRequestsMu.Unlock()
		}
		if swapped {
			return nil
		}
	}
}

// BumpBudgetUsageBy atomically adds an arbitrary delta without applying a
// window reset. The CAS retry preserves concurrent request increments.
func (gs *LocalGovernanceStore) BumpBudgetUsageBy(_ context.Context, budgetID string, delta float64) error {
	if delta == 0 {
		return nil
	}
	for {
		raw, exists := gs.budgets.Load(budgetID)
		if !exists || raw == nil {
			return nil
		}
		old, ok := raw.(*configstoreTables.TableBudget)
		if !ok || old == nil {
			return nil
		}
		clone := *old
		clone.CurrentUsage = max(clone.CurrentUsage+delta, 0)
		if gs.budgets.CompareAndSwap(budgetID, raw, &clone) {
			return nil
		}
	}
}

// BumpRateLimitUsageBy atomically adds arbitrary token and request deltas to the
// rate limit identified by rateLimitID. Unlike BumpRateLimitUsage (which adds a
// token count and a single request), this adds caller-supplied counts on both
// dimensions — used to fold accumulated usage carried from another rate limit.
// Same CAS-retry contract: no increment is dropped under concurrent callers.
// No window-reset side effect, since carried deltas are not request traffic.
// No-op when the rate limit is absent or both deltas are zero.
func (gs *LocalGovernanceStore) BumpRateLimitUsageBy(ctx context.Context, rateLimitID string, tokenDelta, requestDelta int64) error {
	if tokenDelta == 0 && requestDelta == 0 {
		return nil
	}
	for {
		raw, exists := gs.rateLimits.Load(rateLimitID)
		if !exists || raw == nil {
			return nil
		}
		old, ok := raw.(*configstoreTables.TableRateLimit)
		if !ok || old == nil {
			return nil
		}
		clone := *old
		clone.TokenCurrentUsage += tokenDelta
		clone.RequestCurrentUsage += requestDelta
		if gs.rateLimits.CompareAndSwap(rateLimitID, raw, &clone) {
			return nil
		}
	}
}

// baselineClearsUsage reports whether a baseline from a later window replaces local
// usage instead of rebasing it. A known epoch no newer than the local one means the
// sender moved its boundary without resetting, as calendar adoption does, so local
// charges it has not folded still count. A zero epoch is unknown and keeps the replacement.
func baselineClearsUsage(localReset, boundary time.Time, localEpoch, incomingEpoch int64) bool {
	return localReset.Before(boundary) && (incomingEpoch == 0 || incomingEpoch > localEpoch)
}

// ApplyBudgetBaselineWithResetEpoch installs a matching baseline and monotonically
// received actual-reset evidence in the same snapshot under the baseline lock.
func (gs *LocalGovernanceStore) ApplyBudgetBaselineWithResetEpoch(ctx context.Context, budgetID string, baseline float64, boundary time.Time, usageResetEpoch int64) bool {
	gs.LastDBUsagesBudgetsMu.Lock()
	defer gs.LastDBUsagesBudgetsMu.Unlock()
	for {
		raw, _ := gs.budgets.Load(budgetID)
		old, ok := raw.(*configstoreTables.TableBudget)
		if !ok || old == nil || old.LastReset.After(boundary) {
			return false
		}
		clone := *old
		clone.UsageResetEpoch = max(old.UsageResetEpoch, usageResetEpoch)
		if baselineClearsUsage(old.LastReset, boundary, old.UsageResetEpoch, usageResetEpoch) {
			clone.CurrentUsage = baseline
		} else {
			clone.CurrentUsage = max(0, old.CurrentUsage+baseline-gs.LastDBUsagesBudgets[budgetID])
		}
		if old.LastReset.Before(boundary) {
			clone.LastReset = boundary
			clone.RefreshOverrideCyclesRemaining()
		}
		if gs.budgets.CompareAndSwap(budgetID, raw, &clone) {
			gs.LastDBUsagesBudgets[budgetID] = baseline
			return true
		}
	}
}

// ApplyRateLimitBaselinesWithResetEpochs installs each matching baseline and its
// monotonically received reset evidence without changing the other dimension.
func (gs *LocalGovernanceStore) ApplyRateLimitBaselinesWithResetEpochs(ctx context.Context, rateLimitID string, tokens, requests int64, tokenBoundary, requestBoundary time.Time, tokenUsageResetEpoch, requestUsageResetEpoch int64) (bool, bool) {
	gs.LastDBUsagesRateLimitsRequestsMu.Lock()
	defer gs.LastDBUsagesRateLimitsRequestsMu.Unlock()
	gs.LastDBUsagesRateLimitsTokensMu.Lock()
	defer gs.LastDBUsagesRateLimitsTokensMu.Unlock()
	for {
		raw, _ := gs.rateLimits.Load(rateLimitID)
		old, ok := raw.(*configstoreTables.TableRateLimit)
		if !ok || old == nil {
			return false, false
		}
		acceptTokens, acceptRequests := !old.TokenLastReset.After(tokenBoundary), !old.RequestLastReset.After(requestBoundary)
		if !acceptTokens && !acceptRequests {
			return false, false
		}
		clone := *old
		if acceptTokens {
			clone.TokenUsageResetEpoch = max(old.TokenUsageResetEpoch, tokenUsageResetEpoch)
			clone.TokenLastReset = tokenBoundary
			if baselineClearsUsage(old.TokenLastReset, tokenBoundary, old.TokenUsageResetEpoch, tokenUsageResetEpoch) {
				clone.TokenCurrentUsage = tokens
			} else {
				clone.TokenCurrentUsage = max(0, old.TokenCurrentUsage+tokens-gs.LastDBUsagesTokensRateLimits[rateLimitID])
			}
		}
		if acceptRequests {
			clone.RequestUsageResetEpoch = max(old.RequestUsageResetEpoch, requestUsageResetEpoch)
			clone.RequestLastReset = requestBoundary
			if baselineClearsUsage(old.RequestLastReset, requestBoundary, old.RequestUsageResetEpoch, requestUsageResetEpoch) {
				clone.RequestCurrentUsage = requests
			} else {
				clone.RequestCurrentUsage = max(0, old.RequestCurrentUsage+requests-gs.LastDBUsagesRequestsRateLimits[rateLimitID])
			}
		}
		if gs.rateLimits.CompareAndSwap(rateLimitID, raw, &clone) {
			if acceptTokens {
				gs.LastDBUsagesTokensRateLimits[rateLimitID] = tokens
			}
			if acceptRequests {
				gs.LastDBUsagesRequestsRateLimits[rateLimitID] = requests
			}
			return acceptTokens, acceptRequests
		}
	}
}

// ResetBudgetAt atomically zeros the budget's CurrentUsage and advances its
// LastReset to newLastReset, provided the currently-stored budget has an
// older LastReset. Returns the reset budget and true when the CAS succeeds;
// (nil, false) if the budget is absent or another writer has already advanced
// LastReset to at least newLastReset. Callers (e.g. ResetExpiredBudgetsInMemory)
// use the false return to skip the DB-persistence and reference-refresh work
// that would otherwise be redundant. The accepted reset also clears its DB
// baseline under the same lock used by ApplyBudgetBaseline.
func (gs *LocalGovernanceStore) ResetBudgetAt(ctx context.Context, budgetID string, newLastReset time.Time) (*configstoreTables.TableBudget, bool) {
	gs.LastDBUsagesBudgetsMu.Lock()
	defer gs.LastDBUsagesBudgetsMu.Unlock()
	for {
		raw, exists := gs.budgets.Load(budgetID)
		if !exists || raw == nil {
			return nil, false
		}
		old, ok := raw.(*configstoreTables.TableBudget)
		if !ok || old == nil {
			return nil, false
		}
		if !old.LastReset.Before(newLastReset) {
			// Someone else already advanced LastReset past ours, or the reset
			// window hasn't actually opened relative to the stored snapshot.
			return nil, false
		}
		clone := *old
		clone.CurrentUsage = 0
		clone.LastReset = newLastReset
		clone.UsageResetEpoch = max(old.UsageResetEpoch, newLastReset.UnixNano())
		clone.RefreshOverrideCyclesRemaining()
		if gs.budgets.CompareAndSwap(budgetID, raw, &clone) {
			gs.LastDBUsagesBudgets[budgetID] = 0
			gs.markBudgetResetLocked(budgetID)
			return &clone, true
		}
	}
}

// RateLimitReset carries the snapshot and the dimensions reset by the same swap.
type RateLimitReset struct {
	*configstoreTables.TableRateLimit
	tokenReset   bool
	requestReset bool
}

// ResetRateLimitAt atomically resets one or both rate-limit counters on the
// rate limit identified by rateLimitID. A non-nil tokenNewLastReset resets the
// token counter and advances TokenLastReset; similarly for
// requestNewLastReset. Each reset is conditional on the corresponding
// LastReset currently being strictly older than the supplied target, so
// concurrent resetters targeting the same boundary collapse into a single
// successful write. Returns the snapshot and accepted dimensions with true
// after a reset, or (nil, false) otherwise. Accepted counters clear their DB
// baselines under lock so a remote rebase cannot observe a half-applied reset.
func (gs *LocalGovernanceStore) ResetRateLimitAt(ctx context.Context, rateLimitID string, tokenNewLastReset, requestNewLastReset *time.Time) (*RateLimitReset, bool) {
	if tokenNewLastReset == nil && requestNewLastReset == nil {
		return nil, false
	}
	gs.LastDBUsagesRateLimitsRequestsMu.Lock()
	defer gs.LastDBUsagesRateLimitsRequestsMu.Unlock()
	gs.LastDBUsagesRateLimitsTokensMu.Lock()
	defer gs.LastDBUsagesRateLimitsTokensMu.Unlock()
	for {
		raw, exists := gs.rateLimits.Load(rateLimitID)
		if !exists || raw == nil {
			return nil, false
		}
		old, ok := raw.(*configstoreTables.TableRateLimit)
		if !ok || old == nil {
			return nil, false
		}
		clone := *old
		tokenReset := tokenNewLastReset != nil && old.TokenLastReset.Before(*tokenNewLastReset)
		requestReset := requestNewLastReset != nil && old.RequestLastReset.Before(*requestNewLastReset)
		if tokenReset {
			clone.TokenCurrentUsage = 0
			clone.TokenLastReset = *tokenNewLastReset
			clone.TokenUsageResetEpoch = max(old.TokenUsageResetEpoch, tokenNewLastReset.UnixNano())
		}
		if requestReset {
			clone.RequestCurrentUsage = 0
			clone.RequestLastReset = *requestNewLastReset
			clone.RequestUsageResetEpoch = max(old.RequestUsageResetEpoch, requestNewLastReset.UnixNano())
		}
		if !tokenReset && !requestReset {
			return nil, false
		}
		if gs.rateLimits.CompareAndSwap(rateLimitID, raw, &clone) {
			if tokenReset {
				gs.LastDBUsagesTokensRateLimits[rateLimitID] = 0
			}
			if requestReset {
				gs.LastDBUsagesRequestsRateLimits[rateLimitID] = 0
			}
			return &RateLimitReset{TableRateLimit: &clone, tokenReset: tokenReset, requestReset: requestReset}, true
		}
	}
}

// RebaseBudget atomically installs a cluster-authoritative usage value and, when
// newLastReset is non-nil, raises LastReset toward it. Returns the rebased
// snapshot and true when the swap landed; (nil, false) when the budget is absent.
//
// LastReset is never moved backward. A leader state sync that arrives after this
// node already crossed a boundary must not reopen the window it closed: with
// boundaries deterministic, the leader is publishing a value this node already
// holds, so a backward move could only ever un-spend an override cycle. The
// derived override count is refreshed inside the same clone, so remaining and
// LastReset can never be observed out of step.
//
// Callers must not mutate the live pointer returned by GetGovernanceData.
// Use ApplyBudgetBaseline when installing a baseline while preserving local
// charges; this method installs an already-computed absolute usage value.
func (gs *LocalGovernanceStore) RebaseBudget(ctx context.Context, budgetID string, newUsage float64, newLastReset *time.Time) (*configstoreTables.TableBudget, bool) {
	for {
		raw, exists := gs.budgets.Load(budgetID)
		if !exists || raw == nil {
			return nil, false
		}
		old, ok := raw.(*configstoreTables.TableBudget)
		if !ok || old == nil {
			return nil, false
		}
		clone := *old
		clone.CurrentUsage = newUsage
		if newLastReset != nil && clone.LastReset.Before(*newLastReset) {
			clone.LastReset = *newLastReset
		}
		clone.RefreshOverrideCyclesRemaining()
		if gs.budgets.CompareAndSwap(budgetID, raw, &clone) {
			return &clone, true
		}
	}
}

// RebaseRateLimit atomically installs cluster-authoritative token and request
// usage values and raises each dimension's LastReset toward the supplied target
// when it is non-nil and strictly newer. Each dimension is rebased independently,
// and neither boundary is ever moved backward. Same contract and rationale as
// RebaseBudget. Returns the rebased snapshot and true when the swap landed.
func (gs *LocalGovernanceStore) RebaseRateLimit(ctx context.Context, rateLimitID string, newTokens, newRequests int64, tokenLastReset, requestLastReset *time.Time) (*configstoreTables.TableRateLimit, bool) {
	for {
		raw, exists := gs.rateLimits.Load(rateLimitID)
		if !exists || raw == nil {
			return nil, false
		}
		old, ok := raw.(*configstoreTables.TableRateLimit)
		if !ok || old == nil {
			return nil, false
		}
		clone := *old
		clone.TokenCurrentUsage = newTokens
		clone.RequestCurrentUsage = newRequests
		if tokenLastReset != nil && clone.TokenLastReset.Before(*tokenLastReset) {
			clone.TokenLastReset = *tokenLastReset
		}
		if requestLastReset != nil && clone.RequestLastReset.Before(*requestLastReset) {
			clone.RequestLastReset = *requestLastReset
		}
		if gs.rateLimits.CompareAndSwap(rateLimitID, raw, &clone) {
			return &clone, true
		}
	}
}

// GetGovernanceUsageData returns a snapshot containing only budgets and rate limits.
// Use this when callers do not require the complete governance state.
func (gs *LocalGovernanceStore) GetGovernanceUsageData(_ context.Context) *GovernanceData {
	budgets := make(map[string]*configstoreTables.TableBudget)
	gs.budgets.Range(func(key, value interface{}) bool {
		budget, ok := value.(*configstoreTables.TableBudget)
		if ok && budget != nil {
			budgets[key.(string)] = budget
		}
		return true
	})

	rateLimits := make(map[string]*configstoreTables.TableRateLimit)
	gs.rateLimits.Range(func(key, value interface{}) bool {
		rateLimit, ok := value.(*configstoreTables.TableRateLimit)
		if ok && rateLimit != nil {
			rateLimits[key.(string)] = rateLimit
		}
		return true
	})

	return &GovernanceData{
		Budgets:    budgets,
		RateLimits: rateLimits,
	}
}

// GetGovernanceData returns a snapshot of what this store holds, as table structs: each key,
// team, customer, model config and provider with its live budgets and rate limit, keys nested
// under their team and customer, and teams under their customer. Only the fields the records
// carry are set; the full rows are the config store's. Keys are keyed by value, and a key whose
// value could not be resolved is left out. Lists are in ID order.
func (gs *LocalGovernanceStore) GetGovernanceData(ctx context.Context) *GovernanceData {
	virtualKeys := make(map[string]*configstoreTables.TableVirtualKey)
	gs.live.Collections.VirtualKeys.Range(func(k *registry.VirtualKey) bool {
		if k.Value != "" {
			virtualKeys[k.Value] = gs.virtualKeyView(ctx, k)
		}
		return true
	})
	teams := make(map[string]*configstoreTables.TableTeam)
	gs.live.Collections.Teams.Range(func(t *registry.Team) bool {
		teams[t.ID] = &configstoreTables.TableTeam{
			ID: t.ID, Name: t.Name, CustomerID: optionalID(t.CustomerID), RateLimitID: optionalID(t.RateLimitID),
			CalendarAligned: t.CalendarAligned, Budgets: gs.liveBudgets(ctx, t.BudgetIDs), RateLimit: gs.LoadRateLimit(ctx, t.RateLimitID),
		}
		return true
	})
	customers := make(map[string]*configstoreTables.TableCustomer)
	gs.live.Collections.Customers.Range(func(c *registry.Customer) bool {
		customers[c.ID] = &configstoreTables.TableCustomer{
			ID: c.ID, Name: c.Name, RateLimitID: optionalID(c.RateLimitID), CalendarAligned: c.CalendarAligned,
			Budgets: gs.liveBudgets(ctx, c.BudgetIDs), RateLimit: gs.LoadRateLimit(ctx, c.RateLimitID),
			Teams: make([]configstoreTables.TableTeam, 0), VirtualKeys: make([]configstoreTables.TableVirtualKey, 0),
		}
		return true
	})
	for _, vk := range virtualKeys {
		if vk.TeamID != nil {
			if team, exists := teams[*vk.TeamID]; exists {
				vk.Team = team
				team.VirtualKeyCount++
			}
		}
		if vk.CustomerID != nil {
			if customer, exists := customers[*vk.CustomerID]; exists {
				vk.Customer = customer
				nested := *vk
				nested.Customer = nil
				customer.VirtualKeys = append(customer.VirtualKeys, nested)
			}
		}
	}
	for _, team := range teams {
		if team.CustomerID != nil {
			if customer, exists := customers[*team.CustomerID]; exists {
				team.Customer = customer
				nested := *team
				nested.Customer = nil
				customer.Teams = append(customer.Teams, nested)
			}
		}
	}
	for _, customer := range customers {
		sort.Slice(customer.Teams, func(i, j int) bool { return customer.Teams[i].ID < customer.Teams[j].ID })
		sort.Slice(customer.VirtualKeys, func(i, j int) bool { return customer.VirtualKeys[i].ID < customer.VirtualKeys[j].ID })
	}
	usage := gs.GetGovernanceUsageData(ctx)
	var modelConfigs []*configstoreTables.TableModelConfig
	gs.live.Collections.ModelConfigs.Range(func(m *registry.ModelConfig) bool {
		modelConfigs = append(modelConfigs, gs.modelConfigView(ctx, m))
		return true
	})
	var providers []*configstoreTables.TableProvider
	gs.live.Collections.Providers.Range(func(p *registry.Provider) bool {
		providers = append(providers, &configstoreTables.TableProvider{
			Name: string(p.Name), BudgetID: optionalID(p.BudgetID), RateLimitID: optionalID(p.RateLimitID),
			Budget: gs.LoadBudget(ctx, p.BudgetID), RateLimit: gs.LoadRateLimit(ctx, p.RateLimitID),
		})
		return true
	})
	sort.Slice(modelConfigs, func(i, j int) bool { return modelConfigs[i].ID < modelConfigs[j].ID })
	sort.Slice(providers, func(i, j int) bool { return providers[i].Name < providers[j].Name })
	return &GovernanceData{
		VirtualKeys:  virtualKeys,
		Teams:        teams,
		Customers:    customers,
		Budgets:      usage.Budgets,
		RateLimits:   usage.RateLimits,
		ModelConfigs: modelConfigs,
		Providers:    providers,
	}
}

// GovernanceDataFromRows assembles the snapshot GetGovernanceData used to build from in-memory
// copies of the configuration rows, from the rows themselves: every key (listed under its value, and
// under its previous value while a rotation's grace period lasts), team, customer, model config and
// provider as the config store loads them, with their budgets and rate limits replaced by the live
// ones in liveBudgets and liveRateLimits. Admin reads that serve the store's view of the configuration use
// it, so their responses carry every column of the rows.
func GovernanceDataFromRows(ctx context.Context, configStore configstore.ConfigStore, liveBudgets map[string]*configstoreTables.TableBudget, liveRateLimits map[string]*configstoreTables.TableRateLimit) (*GovernanceData, error) {
	src, err := loadSnapshotSources(ctx, configStore, liveBudgets, liveRateLimits)
	if err != nil {
		return nil, err
	}
	refreshVKAssociations := func(vk *configstoreTables.TableVirtualKey) {
		if vk == nil {
			return
		}
		// Replace the row's budgets and rate limit with the live ones, which carry the usage
		// counted since the row was written.
		if len(vk.Budgets) > 0 {
			liveBudgets := make([]configstoreTables.TableBudget, 0, len(vk.Budgets))
			for _, b := range vk.Budgets {
				if lb, exists := src.budgets.Load(b.ID); exists && lb != nil {
					if budget, ok := lb.(*configstoreTables.TableBudget); ok {
						liveBudgets = append(liveBudgets, *budget)
					}
				}
			}
			vk.Budgets = liveBudgets
		}
		if vk.RateLimitID != nil {
			if liveRL, exists := src.rateLimits.Load(*vk.RateLimitID); exists && liveRL != nil {
				if rl, ok := liveRL.(*configstoreTables.TableRateLimit); ok {
					vk.RateLimit = rl
				}
			}
		}
		if len(vk.ProviderConfigs) > 0 {
			configs := make([]configstoreTables.TableVirtualKeyProviderConfig, len(vk.ProviderConfigs))
			copy(configs, vk.ProviderConfigs)
			for i := range configs {
				// Hydrate provider config multi-budgets
				if len(configs[i].Budgets) > 0 {
					liveBudgets := make([]configstoreTables.TableBudget, 0, len(configs[i].Budgets))
					for _, b := range configs[i].Budgets {
						if lb, exists := src.budgets.Load(b.ID); exists && lb != nil {
							if budget, ok := lb.(*configstoreTables.TableBudget); ok {
								liveBudgets = append(liveBudgets, *budget)
							}
						}
					}
					configs[i].Budgets = liveBudgets
				}
				if configs[i].RateLimitID != nil {
					if liveRL, exists := src.rateLimits.Load(*configs[i].RateLimitID); exists && liveRL != nil {
						if rl, ok := liveRL.(*configstoreTables.TableRateLimit); ok {
							configs[i].RateLimit = rl
						}
					}
				}
			}
			vk.ProviderConfigs = configs
		}
	}

	refreshTeamAssociations := func(team *configstoreTables.TableTeam) {
		if team == nil {
			return
		}
		// Allocate a fresh slice: shallow-copying `team` (via `clone := *team` at the caller)
		// reuses the backing array, so in-place writes would change the source row. Mirrors the
		// VK pattern above. Budgets missing from the live budgets are dropped rather than kept
		// stale.
		if len(team.Budgets) > 0 {
			liveBudgets := make([]configstoreTables.TableBudget, 0, len(team.Budgets))
			for _, b := range team.Budgets {
				if lb, exists := src.budgets.Load(b.ID); exists && lb != nil {
					if budget, ok := lb.(*configstoreTables.TableBudget); ok {
						liveBudgets = append(liveBudgets, *budget)
					}
				}
			}
			team.Budgets = liveBudgets
		}
		if team.RateLimitID != nil {
			if liveRL, exists := src.rateLimits.Load(*team.RateLimitID); exists && liveRL != nil {
				if rl, ok := liveRL.(*configstoreTables.TableRateLimit); ok {
					team.RateLimit = rl
				}
			}
		}
	}
	virtualKeys := make(map[string]*configstoreTables.TableVirtualKey)
	src.virtualKeys.Range(func(key, value interface{}) bool {
		vk, ok := value.(*configstoreTables.TableVirtualKey)
		if !ok || vk == nil {
			return true // continue
		}
		clone := *vk
		refreshVKAssociations(&clone)
		virtualKeys[key.(string)] = &clone
		return true // continue iteration
	})
	teams := make(map[string]*configstoreTables.TableTeam)
	src.teams.Range(func(key, value interface{}) bool {
		team, ok := value.(*configstoreTables.TableTeam)
		if !ok || team == nil {
			return true // continue
		}
		clone := *team
		refreshTeamAssociations(&clone)
		// Reset to 0: recomputed from the keys below, counting each listing of a key.
		clone.VirtualKeyCount = 0
		teams[key.(string)] = &clone
		return true // continue iteration
	})
	customers := make(map[string]*configstoreTables.TableCustomer)
	src.customers.Range(func(key, value interface{}) bool {
		customer, ok := value.(*configstoreTables.TableCustomer)
		if !ok || customer == nil {
			return true // continue
		}
		clone := *customer
		clone.Teams = make([]configstoreTables.TableTeam, 0)
		clone.VirtualKeys = make([]configstoreTables.TableVirtualKey, 0)
		// Refresh each owned budget from the live budget map.
		refreshedBudgets := make([]configstoreTables.TableBudget, 0, len(clone.Budgets))
		for _, b := range clone.Budgets {
			if liveBudget, exists := src.budgets.Load(b.ID); exists && liveBudget != nil {
				if lb, ok := liveBudget.(*configstoreTables.TableBudget); ok {
					refreshedBudgets = append(refreshedBudgets, *lb)
					continue
				}
			}
			refreshedBudgets = append(refreshedBudgets, b)
		}
		clone.Budgets = refreshedBudgets
		if clone.RateLimitID != nil {
			if liveRL, exists := src.rateLimits.Load(*clone.RateLimitID); exists && liveRL != nil {
				if rl, ok := liveRL.(*configstoreTables.TableRateLimit); ok {
					clone.RateLimit = rl
				}
			}
		}
		customers[key.(string)] = &clone
		return true // continue iteration
	})
	// virtualKeys level data
	for _, vk := range virtualKeys {
		if vk == nil {
			continue
		}
		if vk.TeamID != nil {
			if team, exists := teams[*vk.TeamID]; exists && team != nil {
				vk.Team = team
				team.VirtualKeyCount++
			}
		}
		if vk.CustomerID != nil {
			if customer, exists := customers[*vk.CustomerID]; exists && customer != nil {
				vk.Customer = customer

				nestedVK := *vk
				nestedVK.Customer = nil
				customer.VirtualKeys = append(customer.VirtualKeys, nestedVK)
			}
		}
	}
	// Team level data
	for _, team := range teams {
		if team == nil {
			continue
		}
		if team.CustomerID != nil {
			if customer, exists := customers[*team.CustomerID]; exists && customer != nil {
				team.Customer = customer

				nestedTeam := *team
				nestedTeam.Customer = nil
				customer.Teams = append(customer.Teams, nestedTeam)
			}
		}
	}
	// Customer level data
	for _, customer := range customers {
		if customer == nil {
			continue
		}
		sort.Slice(customer.Teams, func(i, j int) bool {
			if customer.Teams[i].CreatedAt.Equal(customer.Teams[j].CreatedAt) {
				return customer.Teams[i].ID < customer.Teams[j].ID
			}
			return customer.Teams[i].CreatedAt.Before(customer.Teams[j].CreatedAt)
		})
		sort.Slice(customer.VirtualKeys, func(i, j int) bool {
			if customer.VirtualKeys[i].CreatedAt.Equal(customer.VirtualKeys[j].CreatedAt) {
				return customer.VirtualKeys[i].ID < customer.VirtualKeys[j].ID
			}
			return customer.VirtualKeys[i].CreatedAt.Before(customer.VirtualKeys[j].CreatedAt)
		})
	}
	budgets := make(map[string]*configstoreTables.TableBudget)
	src.budgets.Range(func(key, value interface{}) bool {
		budget, ok := value.(*configstoreTables.TableBudget)
		if !ok || budget == nil {
			return true // continue
		}
		budgets[key.(string)] = budget
		return true // continue iteration
	})
	rateLimits := make(map[string]*configstoreTables.TableRateLimit)
	src.rateLimits.Range(func(key, value interface{}) bool {
		rateLimit, ok := value.(*configstoreTables.TableRateLimit)
		if !ok || rateLimit == nil {
			return true // continue
		}
		rateLimits[key.(string)] = rateLimit
		return true // continue iteration
	})
	var modelConfigsList []*configstoreTables.TableModelConfig
	src.modelConfigs.Range(func(key, value any) bool {
		mc, ok := value.(*configstoreTables.TableModelConfig)
		if !ok || mc == nil {
			return true // continue
		}
		// Cross-reference live budgets/rate limit from standalone maps.
		clone := *mc
		if len(clone.Budgets) > 0 {
			liveBudgets := make([]configstoreTables.TableBudget, 0, len(clone.Budgets))
			for _, b := range clone.Budgets {
				if lb, exists := src.budgets.Load(b.ID); exists && lb != nil {
					if budget, ok := lb.(*configstoreTables.TableBudget); ok {
						liveBudgets = append(liveBudgets, *budget)
					}
				}
			}
			clone.Budgets = liveBudgets
		}
		if clone.RateLimitID != nil {
			if liveRL, exists := src.rateLimits.Load(*clone.RateLimitID); exists && liveRL != nil {
				if rl, ok := liveRL.(*configstoreTables.TableRateLimit); ok {
					clone.RateLimit = rl
				}
			}
		}
		modelConfigsList = append(modelConfigsList, &clone)
		return true // continue iteration
	})
	var providersList []*configstoreTables.TableProvider
	src.providers.Range(func(key, value interface{}) bool {
		p, ok := value.(*configstoreTables.TableProvider)
		if !ok || p == nil {
			return true // continue
		}
		// Cross-reference live budget/rate limit from standalone maps
		clone := *p
		if clone.BudgetID != nil {
			if liveBudget, exists := src.budgets.Load(*clone.BudgetID); exists && liveBudget != nil {
				if b, ok := liveBudget.(*configstoreTables.TableBudget); ok {
					clone.Budget = b
				}
			}
		}
		if clone.RateLimitID != nil {
			if liveRL, exists := src.rateLimits.Load(*clone.RateLimitID); exists && liveRL != nil {
				if rl, ok := liveRL.(*configstoreTables.TableRateLimit); ok {
					clone.RateLimit = rl
				}
			}
		}
		providersList = append(providersList, &clone)
		return true // continue iteration
	})
	// Sort slice fields by CreatedAt so responses are sent in consistent order
	sort.Slice(modelConfigsList, func(i, j int) bool {
		return modelConfigsList[i].CreatedAt.Before(modelConfigsList[j].CreatedAt)
	})
	sort.Slice(providersList, func(i, j int) bool {
		return providersList[i].CreatedAt.Before(providersList[j].CreatedAt)
	})
	return &GovernanceData{
		VirtualKeys:  virtualKeys,
		Teams:        teams,
		Customers:    customers,
		Budgets:      budgets,
		RateLimits:   rateLimits,
		ModelConfigs: modelConfigsList,
		Providers:    providersList,
	}, nil
}

// snapshotSource is a read-only set of rows with the Load and Range methods of the maps the in-memory
// snapshot read from, so GovernanceDataFromRows assembles exactly as that snapshot did.
type snapshotSource map[any]any

// Load returns the row under key.
func (s snapshotSource) Load(key any) (any, bool) {
	v, ok := s[key]
	return v, ok
}

// Range calls fn for every row until fn returns false.
func (s snapshotSource) Range(fn func(key, value any) bool) {
	for k, v := range s {
		if !fn(k, v) {
			return
		}
	}
}

// snapshotSources holds the rows GovernanceDataFromRows assembles from, under the names the
// in-memory snapshot read them by.
type snapshotSources struct {
	virtualKeys, teams, customers, budgets, rateLimits, modelConfigs, providers snapshotSource
}

// loadSnapshotSources reads the configuration rows from configStore with the loaders the store
// once loaded itself from, and takes budgets and rate limits from the live maps.
func loadSnapshotSources(ctx context.Context, configStore configstore.ConfigStore, budgets map[string]*configstoreTables.TableBudget, rateLimits map[string]*configstoreTables.TableRateLimit) (*snapshotSources, error) {
	src := &snapshotSources{
		virtualKeys: snapshotSource{}, teams: snapshotSource{}, customers: snapshotSource{}, budgets: snapshotSource{},
		rateLimits: snapshotSource{}, modelConfigs: snapshotSource{}, providers: snapshotSource{},
	}
	keys, err := configStore.GetVirtualKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load virtual keys: %w", err)
	}
	now := time.Now()
	for i := range keys {
		vk := &keys[i]
		value := vk.Value.GetValue()
		if value == "" {
			continue
		}
		src.virtualKeys[value] = vk
		if vk.HasActivePreviousValue(now) {
			if prev := vk.PreviousValue.GetValue(); prev != "" && prev != value {
				if _, taken := src.virtualKeys[prev]; !taken {
					src.virtualKeys[prev] = vk
				}
			}
		}
	}
	teams, err := configStore.GetTeams(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("failed to load teams: %w", err)
	}
	for i := range teams {
		src.teams[teams[i].ID] = &teams[i]
	}
	customers, err := configStore.GetCustomers(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load customers: %w", err)
	}
	for i := range customers {
		src.customers[customers[i].ID] = &customers[i]
	}
	modelConfigs, err := configStore.GetModelConfigs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load model configs: %w", err)
	}
	for i := range modelConfigs {
		src.modelConfigs[modelConfigs[i].ID] = &modelConfigs[i]
	}
	providers, err := configStore.GetProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load providers: %w", err)
	}
	for i := range providers {
		src.providers[providers[i].Name] = &providers[i]
	}
	for id, b := range budgets {
		src.budgets[id] = b
	}
	for id, rl := range rateLimits {
		src.rateLimits[id] = rl
	}
	return src, nil
}

// virtualKeyView is a key's record as a table struct, with its live budgets and rate limit and
// its provider configs' live ones.
func (gs *LocalGovernanceStore) virtualKeyView(ctx context.Context, k *registry.VirtualKey) *configstoreTables.TableVirtualKey {
	active := k.Active
	vk := &configstoreTables.TableVirtualKey{
		ID: k.ID, Name: k.Name, Value: *schemas.NewSecretVar(k.Value), IsActive: &active,
		CalendarAligned: k.CalendarAligned, AllowAllProviders: k.AllowAllProviders,
		DisableContentLogging: k.DisableContentLogging, Metadata: k.Metadata,
		TeamID: optionalID(k.TeamID), CustomerID: optionalID(k.CustomerID), BusinessUnitID: optionalID(k.BusinessUnitID),
		CreatedByUserID: optionalID(k.CreatedByUserID), RateLimitID: optionalID(k.RateLimitID),
		MCPConfigs: k.MCPConfigs, AgentGrants: k.AgentGrants, RelayGrants: k.RelayGrants,
		Budgets: gs.liveBudgets(ctx, k.BudgetIDs), RateLimit: gs.LoadRateLimit(ctx, k.RateLimitID),
	}
	if k.ExpiresAt != 0 {
		expires := time.Unix(0, k.ExpiresAt).UTC()
		vk.ExpiresAt = &expires
	}
	vk.ProviderConfigs = make([]configstoreTables.TableVirtualKeyProviderConfig, len(k.ProviderConfigs))
	for i, pc := range k.ProviderConfigs {
		pc.Budgets = gs.liveBudgets(ctx, budgetIDs(pc.Budgets))
		if pc.RateLimitID != nil {
			pc.RateLimit = gs.LoadRateLimit(ctx, *pc.RateLimitID)
		}
		vk.ProviderConfigs[i] = pc
	}
	return vk
}

// modelConfigView is a model config's record as a table struct, with its live budgets and rate
// limit.
func (gs *LocalGovernanceStore) modelConfigView(ctx context.Context, m *registry.ModelConfig) *configstoreTables.TableModelConfig {
	return &configstoreTables.TableModelConfig{
		ID: m.ID, ModelName: m.ModelName, Provider: m.Provider, Scope: m.Scope, ScopeID: optionalID(m.ScopeID),
		RateLimitID: optionalID(m.RateLimitID), CalendarAligned: m.CalendarAligned, BudgetID: optionalID(m.BudgetID),
		Budgets: gs.liveBudgets(ctx, m.BudgetIDs), RateLimit: gs.LoadRateLimit(ctx, m.RateLimitID),
	}
}

// liveBudgets copies the live budgets with these IDs, leaving out any this store does not hold.
func (gs *LocalGovernanceStore) liveBudgets(ctx context.Context, ids []string) []configstoreTables.TableBudget {
	held := gs.loadBudgets(ctx, ids)
	out := make([]configstoreTables.TableBudget, 0, len(held))
	for _, b := range held {
		out = append(out, *b)
	}
	return out
}

// optionalID is id as the optional reference a table struct holds, nil for "".
func optionalID(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}

// GetVirtualKey returns the key presented with this value: the key whose current value it is,
// or else a rotating key still accepting it as its previous value.
func (gs *LocalGovernanceStore) GetVirtualKey(ctx context.Context, vkValue string) (*registry.VirtualKey, bool) {
	if vkValue == "" {
		return nil, false
	}
	if vk, ok := gs.live.Collections.VirtualKeys.GetBy(registry.VirtualKeyByValue, vkValue); ok {
		return vk, true
	}
	now := time.Now()
	for _, vk := range gs.live.Collections.VirtualKeys.List(registry.VirtualKeyByPreviousValue, vkValue) {
		if vk.HasActivePreviousValue(now) {
			gs.logger.Debug("virtual key %s authenticated via rotation grace-period value", vk.ID)
			return vk, true
		}
	}
	return nil, false
}

// GetVirtualKeyByID returns the key with this row ID, for paths that know the key by ID, such as
// /mcp JWT auth. A key whose value could not be resolved is treated as absent, as it is by value.
func (gs *LocalGovernanceStore) GetVirtualKeyByID(ctx context.Context, vkID string) (*registry.VirtualKey, bool) {
	vk, ok := gs.live.Collections.VirtualKeys.Get(vkID)
	if !ok || vk.Value == "" {
		return nil, false
	}
	return vk, true
}

// ResolvePermits reports the permits a request carries. This store knows only about virtual keys,
// so the presented key's permit is the whole answer and nothing scopes it.
func (gs *LocalGovernanceStore) ResolvePermits(ctx *schemas.BifrostContext) ([]schemas.Permit, grant.CompositionMode, schemas.Permit, grant.CompositionMode) {
	virtualKeyValue := PresentedVirtualKey(ctx)
	if virtualKeyValue == "" {
		// A key is the only thing this store resolves permits from, so no key means nothing granted
		// this request anything. It carries no access, which is what a request with no credential has
		// always been: unrestricted, and still bound by the deployment's own limits, which are gathered
		// for it whether or not it carries a permit.
		return nil, "", nil, ""
	}
	virtualKey, ok := gs.GetVirtualKey(ctx, virtualKeyValue)
	if !ok || virtualKey == nil {
		// A credential was presented and resolves to nothing. That is refused, not widened: the
		// funnel's identity step tells the two apart by what was presented.
		return nil, "", nil, ""
	}
	gs.StampVirtualKeyScope(ctx, virtualKey)
	if virtualKey.TeamID != "" {
		gs.StampTeamContentLogging(ctx, virtualKey.TeamID)
	}
	return []schemas.Permit{gs.permitForVirtualKey(ctx, virtualKey)}, "", nil, ""
}

// StampTeamContentLogging stamps the team's own content-logging decision for the request. The team
// is read from the live store's team collection, so a team edit reaches requests as soon as its
// record is refreshed. A team that inherits stamps nothing.
//
// Exported because a store that resolves callers other than keys (a user in several teams) stamps
// every team it resolves the same way.
func (gs *LocalGovernanceStore) StampTeamContentLogging(ctx *schemas.BifrostContext, teamID string) {
	if ctx == nil || teamID == "" {
		return
	}
	team, ok := gs.live.Collections.Teams.Get(teamID)
	if !ok || team == nil || team.DisableContentLogging == nil {
		return
	}
	schemas.StampContentLoggingDecision(ctx, schemas.BifrostContextKeyTeamDisableContentLogging, *team.DisableContentLogging)
}

// PresentedVirtualKey is the virtual key the request presented, read off the identity the
// transport settled on its grant. A grant nothing settled an identity on is read the way it always
// was, from the key stamped on the context directly.
//
// Exported because a store that resolves permits from something other than a key still resolves
// keys, and which key a request presented is the same question either way.
func PresentedVirtualKey(ctx *schemas.BifrostContext) string {
	// A context built without a grant (an MCP connect, an internal probe) settled nothing, so it falls
	// through to the key it carries under the key's own name.
	if g := ctx.Grant(); g != nil {
		if identity := g.Identity(); identity != nil {
			if credential := identity.Credential(); credential.Kind == string(grant.CredentialVirtualKey) {
				return credential.Value
			}
			// An identity was settled and it is not a key: whatever the context also carries under the
			// key's own name did not authenticate this request.
			return ""
		}
	}
	virtualKeyValue, _ := ctx.Value(schemas.BifrostContextKeyVirtualKey).(string)
	return virtualKeyValue
}

// StampVirtualKeyScope records which key a request was made with, and the team and customer it sits
// under, so everything downstream reads the identity that resolving the access answered for rather
// than looking it up again. It is recorded twice: on the request's identity, which is what carries
// who the request is from here on, and under the context keys everything that predates the identity
// still reads.
//
// Exported because a store that resolves permits from something other than a key still resolves
// keys, and the scope a key puts on a request is the same either way. Two copies of this would be
// two records of who the caller is, free to disagree about a key that names a customer directly and
// also reaches one through its team, the case the ordering below decides.
func (gs *LocalGovernanceStore) StampVirtualKeyScope(ctx *schemas.BifrostContext, virtualKey *registry.VirtualKey) {
	if ctx == nil || virtualKey == nil {
		return
	}
	org := keyOrganizationOf(gs.live, virtualKey)
	recordVirtualKeyIdentity(ctx, virtualKey, org)
	ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, virtualKey.ID)
	ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyName, virtualKey.Name)
	stampVirtualKeyContentLogging(ctx, virtualKey)
	stampVirtualKeyMetadata(ctx, virtualKey)
	org.stamp(ctx)
}

// keyOrganization is where a key sits: its team, that team's customer, and the customer it names
// directly. Each is nil when the key names none or the record is not held.
type keyOrganization struct {
	team                   *registry.Team
	teamCustomer, customer *registry.Customer
}

// keyOrganizationOf looks up the team and customers a key sits under in live.
func keyOrganizationOf(live *livestore.Store, vk *registry.VirtualKey) keyOrganization {
	var org keyOrganization
	cols := live.Collections
	if vk.TeamID != "" {
		if team, ok := cols.Teams.Get(vk.TeamID); ok {
			org.team = team
			if team.CustomerID != "" {
				org.teamCustomer, _ = cols.Customers.Get(team.CustomerID)
			}
		}
	}
	if vk.CustomerID != "" {
		org.customer, _ = cols.Customers.Get(vk.CustomerID)
	}
	return org
}

// stamp puts the team and customer on the context keys that predate the request's identity.
func (org keyOrganization) stamp(ctx *schemas.BifrostContext) {
	if org.team != nil {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceTeamID, org.team.ID)
		ctx.SetValue(schemas.BifrostContextKeyGovernanceTeamName, org.team.Name)
		if org.teamCustomer != nil {
			ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerID, org.teamCustomer.ID)
			ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerName, org.teamCustomer.Name)
		}
	}
	// A customer the key names directly wins over one reached through its team: it is the more specific
	// statement of who pays, and it is stamped second so it overwrites.
	if org.customer != nil {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerID, org.customer.ID)
		ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerName, org.customer.Name)
	}
}

// stampVirtualKeyContentLogging publishes the key's own content-logging decision for the logging
// plugin, which runs before governance in the plugin chain and so cannot look the key up itself.
// Only a decision is stamped: a key that inherits leaves the context alone, so the plugin falls
// through to the client setting instead of reading a false it would have to treat as a choice.
//
// The stamp goes through schemas.StampContentLoggingDecision, which also keeps the request's root
// span marked when the layers resolve to off: observability connectors are handed the finished
// trace without the request context, so the span is the only place they can read the decision from.
//
// A project the request is scoped to can override the key's decision once it is admitted, on the
// context and on the span alike.
func stampVirtualKeyContentLogging(ctx *schemas.BifrostContext, virtualKey *registry.VirtualKey) {
	if ctx == nil || virtualKey == nil || virtualKey.DisableContentLogging == nil {
		return
	}
	schemas.StampContentLoggingDecision(ctx, schemas.BifrostContextKeyGovernanceDisableContentLogging, *virtualKey.DisableContentLogging)
}

// VirtualKeyMetadataContextKey carries the resolved virtual key's own metadata (map[string]string,
// a copy the request owns): operator-set attribution such as cost center or owner. The logging
// plugin merges it into the log row's metadata after request-supplied entries, so a caller cannot
// override a key the virtual key sets. Absent when the key has no metadata. Set by this plugin -
// DO NOT SET THIS MANUALLY.
//
// The logging plugin (which cannot import this package) and the HTTP transport's realtime handlers
// declare the same key locally, so the plugins keep building against published core; the string
// must stay identical in all three places.
const VirtualKeyMetadataContextKey schemas.BifrostContextKey = "bifrost-governance-virtual-key-metadata"

// stampVirtualKeyMetadata publishes the key's metadata for the logging plugin, which snapshots it
// onto the log row so spend can be filtered by it. The context gets its own copy: the key is shared
// by every in-flight request and replaced wholesale on reload, and nothing a request does to its
// copy may reach the store. A key without metadata leaves the context alone.
func stampVirtualKeyMetadata(ctx *schemas.BifrostContext, virtualKey *registry.VirtualKey) {
	if ctx == nil || virtualKey == nil || len(virtualKey.Metadata) == 0 {
		return
	}
	ctx.SetValue(VirtualKeyMetadataContextKey, maps.Clone(virtualKey.Metadata))
	stampVirtualKeyDimensions(ctx, virtualKey.Metadata)
}

// stampVirtualKeyDimensions adds the key's metadata to the request's dimensions, where metrics and
// connectors read x-bf-dim-* labels: the context, the trace and the root span, the same three places
// the transport puts header dimensions. The key's value wins over a header of the same name. "path"
// and "method" are skipped, as for headers, since they name the root span's http attributes.
func stampVirtualKeyDimensions(ctx *schemas.BifrostContext, metadata map[string]string) {
	// May be the stored key's shared map: everything below copies it, never keeps it.
	added := metadata
	_, hasPath := metadata["path"]
	_, hasMethod := metadata["method"]
	if hasPath || hasMethod {
		added = maps.Clone(metadata)
		delete(added, "path")
		delete(added, "method")
		if len(added) == 0 {
			return
		}
	}
	existing, _ := ctx.Value(schemas.BifrostContextKeyDimensions).(map[string]string)
	dims := make(map[string]string, len(existing)+len(added))
	maps.Copy(dims, existing)
	maps.Copy(dims, added)
	// A new map: other readers of the request may hold the previous one.
	ctx.SetValue(schemas.BifrostContextKeyDimensions, dims)

	tracer, _ := ctx.Value(schemas.BifrostContextKeyTracer).(schemas.Tracer)
	traceID, _ := ctx.Value(schemas.BifrostContextKeyTraceID).(string)
	if tracer == nil || traceID == "" {
		return
	}
	tracer.MergeTraceDimensions(traceID, added)
	root := tracer.SpanFromHandle(tracer.GetSpanHandleByID(traceID, nil))
	for key, value := range added {
		// Never replace an attribute Bifrost set on the root span (the content-logging flag, http.*,
		// identity); only a header dimension's value, which the key's value overrides.
		if current, set := root.GetAttribute(key); set {
			if header, isHeader := existing[key]; !isHeader || current != header {
				continue
			}
		}
		root.SetAttribute(key, value)
	}
}

// recordVirtualKeyIdentity completes the request's identity with what the presented key resolved to:
// the key itself, and the team and customer it sits under, the attributed one first. What the
// transport settled, the credential and any user it named, is kept. A grant nothing settled an
// identity on has nothing to complete.
func recordVirtualKeyIdentity(ctx *schemas.BifrostContext, virtualKey *registry.VirtualKey, org keyOrganization) {
	current := ctx.Grant().Identity()
	if current == nil {
		return
	}
	var teams, customers []schemas.EntityRef
	if org.team != nil {
		teams = append(teams, schemas.EntityRef{ID: org.team.ID, Name: org.team.Name})
	}
	// The customer the key names directly is the one the request is attributed to, and comes first;
	// one reached through the team is still one the request happens under.
	if org.customer != nil {
		customers = append(customers, schemas.EntityRef{ID: org.customer.ID, Name: org.customer.Name})
	}
	if org.teamCustomer != nil && (org.customer == nil || org.customer.ID != org.teamCustomer.ID) {
		customers = append(customers, schemas.EntityRef{ID: org.teamCustomer.ID, Name: org.teamCustomer.Name})
	}
	ctx.Grant().SetIdentity(grant.NewIdentity(
		current.Credential(),
		current.User(),
		&schemas.EntityRef{ID: virtualKey.ID, Name: virtualKey.Name},
		teams,
		customers,
		current.BusinessUnits(),
		current.Project(),
	))
}

// CheckProviderCandidateExclusion reports whether a load-balancing candidate can serve this request
// right now: DecisionAllow when nothing stops it, and otherwise whichever refusal the checks below
// produced, unchanged. The caller decides what to say about it, because the caller is the one writing
// a routing log and this store has no business inventing the words for one.
//
// Only spending decides here: what a candidate may reach was already settled by the grant it came from.
//
// It asks about the limits that can tell one candidate from another, and only those: the deployment's
// own limits on that provider, its model configs scoped to the provider, and what the grant's configs
// for it are funded by, the holder's provider-scoped model configs included. A limit that covers
// every provider answers the same for all of them, so it cannot make one candidate better than
// another: it can only exclude every candidate at once, which is a refusal, and the funnel states it
// with a reason rather than load balancing quietly running out of options. That is why the
// provider-less model-config tiers stay out (see ProviderScopedModelLimits).
//
// So load balancing routes around a provider that is out of room, and being out of money overall is
// left to the funnel, which is the only place that can say so.
//
// The model says two things here: which permits pay for the candidate (every permit that permits the
// model on the candidate's provider is funded on it, and a candidate is affordable only when all of
// them have room), and which per-model configs cover the pair on this provider and no other.
func (gs *LocalGovernanceStore) CheckProviderCandidateExclusion(ctx *schemas.BifrostContext, access schemas.Access, candidate schemas.ProviderCandidate, model string) (Decision, error) {
	if spendingChecksSkipped(ctx) {
		return DecisionAllow, nil
	}

	provider := schemas.ModelProvider(candidate.Provider)
	budgets, rateLimits := gs.GlobalProviderLimits(ctx, provider)
	modelBudgets, modelRateLimits := gs.ProviderScopedModelLimits(ctx, nil, provider, model)
	budgets = append(budgets, modelBudgets...)
	rateLimits = append(rateLimits, modelRateLimits...)
	if access != nil {
		for _, permit := range access.PermitsForModel(candidate.Provider, model) {
			permitBudgets, permitRateLimits := gs.PermitProviderLimits(ctx, permit, provider)
			budgets = append(budgets, permitBudgets...)
			rateLimits = append(rateLimits, permitRateLimits...)
			permitModelBudgets, permitModelRateLimits := gs.ProviderScopedModelLimits(ctx, permit, provider, model)
			budgets = append(budgets, permitModelBudgets...)
			rateLimits = append(rateLimits, permitModelRateLimits...)
		}
	}

	if decision, err := gs.CheckRateLimits(ctx, rateLimits, nil, nil); err != nil || isRateLimitViolation(decision) {
		return decision, err
	}
	if decision, err := gs.CheckBudgets(ctx, budgets, nil); err != nil || isBudgetViolation(decision) {
		return decision, err
	}
	return DecisionAllow, nil
}

// permitForVirtualKey builds the permit a virtual key confers: one provider permit per configured
// provider, and one MCP permit per MCP client the key may execute tools of: its own MCP configs,
// plus the clients allowed by default.
//
// allowedByDefaultClients maps client id to client name, as the in-memory store reports it. A client
// the key configures explicitly is never widened by that map: the explicit config decides, including
// when it grants no tool at all.
//
// The result is a snapshot for one attempt. Provider permits carry a copy of the config they came
// from, so a key reloaded mid-attempt cannot change what that attempt has already resolved. A later
// attempt builds its own and picks the reload up. What funds the key is not here: see HolderLimits
// and PermitProviderLimits, which read it by the permit's identity when an attempt's provider is known.
func (gs *LocalGovernanceStore) permitForVirtualKey(ctx context.Context, vk *registry.VirtualKey) *grant.Permit {
	if vk == nil {
		return nil
	}

	allowedByDefaultClients := MCPClientsAllowedByDefault(gs.live)

	providerPermits := make([]schemas.ProviderPermit, 0, len(vk.ProviderConfigs))
	for i := range vk.ProviderConfigs {
		config := vk.ProviderConfigs[i]
		providerPermits = append(providerPermits, schemas.ProviderPermit{
			Provider:          config.Provider,
			AllowedModels:     config.AllowedModels,
			BlacklistedModels: config.BlacklistedModels,
			KeyIDs:            config.KeyIDs(),
			Weight:            config.Weight,
		})
	}
	if vk.AllowAllProviders {
		providerPermits = AppendAllProviderPermits(providerPermits, ConfiguredProviderNames(gs.live))
	}

	// A key's own MCP configs and its Virtual MCPs build one accumulator, so a Virtual MCP grants
	// exactly like a config: owns the clients it names, unions per client, blocks allowed-by-default.
	vmcpIDs := vk.VirtualMCPIDs
	acc := NewMCPToolAccumulator(len(vk.MCPConfigs) + len(vmcpIDs))
	for i := range vk.MCPConfigs {
		acc.addMCPConfig(&vk.MCPConfigs[i])
	}
	for _, id := range vmcpIDs {
		def := gs.virtualMCPByID(id)
		if def == nil || !def.Enabled {
			continue
		}
		acc.AddVirtualMCP(def)
		// A key's direct assignment makes the vMCP addressable at /mcp/<slug>.
		RecordGrantedVirtualMCP(ctx, def)
	}
	mcpPermits := MCPPermitsFromAccumulator(acc, "virtual key", vk.Name, MCPClientNames(gs.live), gs.logger)
	mcpPermits = AppendMCPPermitsAllowedByDefault(mcpPermits, acc.ConfiguredClients(), allowedByDefaultClients)

	agentPermits := make([]string, 0, len(vk.AgentGrants))
	for _, agentGrant := range vk.AgentGrants {
		agentPermits = append(agentPermits, agentGrant.AgentName)
	}

	endpointPermits := make([]string, 0, len(vk.RelayGrants))
	for _, endpointGrant := range vk.RelayGrants {
		endpointPermits = append(endpointPermits, endpointGrant.RelayName)
	}
	return grant.NewPermit(grant.PermitVirtualKey, vk.ID, vk.Name, vk.Active, vk.IsExpiredAt(time.Now()), providerPermits, mcpPermits,
		grant.WithAgentPermits(agentPermits), grant.WithAllowAllProviders(vk.AllowAllProviders),
		grant.WithRelayPermits(endpointPermits))
}

// virtualMCPByID returns the virtual MCP definition with this row ID, nil when there is none.
func (gs *LocalGovernanceStore) virtualMCPByID(id uint) *configstoreTables.TableVirtualMCP {
	if v, ok := gs.live.Collections.VirtualMCPs.Get(registry.VirtualMCPKey(id)); ok {
		return &v.TableVirtualMCP
	}
	return nil
}

// GetVirtualMCPFromCache returns the live cached Virtual MCP definition for id, or nil. Enterprise
// access-profile / project resolution uses this so it serves the current definition (Enabled and
// tool specs) rather than a snapshot captured at propagate time, which goes stale on a vMCP edit.
func (gs *LocalGovernanceStore) GetVirtualMCPFromCache(id uint) *configstoreTables.TableVirtualMCP {
	return gs.virtualMCPByID(id)
}

// grantedVirtualMCPsKey keys the per-request set of addressable Virtual MCPs.
type grantedVirtualMCPsKey struct{}

// grantedVirtualMCPs is the per-request set of vMCPs reachable at /mcp/<slug>, keyed by slug. Only the
// addressable paths record here (direct key assignment; enterprise access profiles); legacy
// team/customer/provider scopes don't, so those grant on /mcp but not /mcp/<slug>.
type grantedVirtualMCPs struct {
	bySlug map[string]configstoreTables.TableVirtualMCP
}

// grantedVirtualMCPsMu guards the per-request set's get-or-create and its bySlug map. Resolution is
// single-goroutine per request today, so it is uncontended; it stops a concurrent-map panic if a
// record path is ever parallelized.
var grantedVirtualMCPsMu sync.Mutex

// RecordGrantedVirtualMCP records an enabled vMCP as reachable at /mcp/<slug> for this request. No-op
// off the gateway path or for a disabled/slug-less vMCP. Exported so enterprise records profile vMCPs.
func RecordGrantedVirtualMCP(ctx context.Context, vmcp *configstoreTables.TableVirtualMCP) {
	if vmcp == nil || !vmcp.Enabled || vmcp.EndpointSlug == "" {
		return
	}
	bctx, ok := ctx.(*schemas.BifrostContext)
	if !ok {
		return
	}
	if gateway, _ := bctx.Value(schemas.BifrostContextKeyIsMCPGateway).(bool); !gateway {
		return
	}
	grantedVirtualMCPsMu.Lock()
	defer grantedVirtualMCPsMu.Unlock()
	set, _ := bctx.Value(grantedVirtualMCPsKey{}).(*grantedVirtualMCPs)
	if set == nil {
		set = &grantedVirtualMCPs{bySlug: make(map[string]configstoreTables.TableVirtualMCP)}
		bctx.SetValue(grantedVirtualMCPsKey{}, set)
	}
	set.bySlug[vmcp.EndpointSlug] = *vmcp
}

// grantedVirtualMCP returns the Virtual MCP recorded under slug for the request, if any.
func grantedVirtualMCP(ctx context.Context, slug string) (*configstoreTables.TableVirtualMCP, bool) {
	bctx, ok := ctx.(*schemas.BifrostContext)
	if !ok {
		return nil, false
	}
	grantedVirtualMCPsMu.Lock()
	defer grantedVirtualMCPsMu.Unlock()
	set, _ := bctx.Value(grantedVirtualMCPsKey{}).(*grantedVirtualMCPs)
	if set == nil {
		return nil, false
	}
	v, ok := set.bySlug[slug]
	if !ok {
		return nil, false
	}
	return &v, true
}

// VirtualMCPToolAccess resolves the tool include-list a /mcp/<slug> request may see. The slug must
// have been recorded addressable during resolution (assigned=false → the caller answers 403); its
// tools are then narrowed to what the request's access grants, so an excluded tool can't leak. A nil
// access restricts nothing, so the vMCP's own tools are served as-is.
func (gs *LocalGovernanceStore) VirtualMCPToolAccess(ctx *schemas.BifrostContext, slug string, access schemas.Access) (served []string, assigned bool) {
	target, ok := grantedVirtualMCP(ctx, slug)
	if !ok {
		return nil, false
	}

	clientNames := MCPClientNames(gs.live)
	requested := make([]string, 0, len(target.ParsedTools))
	for _, spec := range target.ParsedTools {
		name := clientNames[spec.MCPClientID]
		if name == "" {
			// Client no longer configured (or unnamed): nothing to serve for it.
			continue
		}
		// WhiteList semantics: ["*"] serves the whole client (name-*), [] serves nothing, a named list
		// serves those tools. grant.Wildcard is "*", so the loop below produces name-* for the all case.
		for _, tool := range spec.ToolNames {
			if tool == "" {
				continue
			}
			requested = append(requested, name+"-"+tool)
		}
	}
	if access == nil {
		return requested, true
	}
	return access.NarrowMCPToolIncludeList(requested), true
}

// MCPClientToolAccess resolves what a /mcp/<slug> request may see for one MCP client: the client's tools
// narrowed to the caller's access (empty → not granted → the caller answers 403). Nil access serves the
// whole client. No per-request recording (unlike a Virtual MCP): the access already names the clients.
func (gs *LocalGovernanceStore) MCPClientToolAccess(ctx *schemas.BifrostContext, slug string, access schemas.Access) (served []string, ok bool) {
	client, found := gs.live.Collections.MCPClients.GetBy(registry.MCPClientBySlug, slug)
	if !found || client.Config.Disabled || client.Config.Name == "" {
		return nil, false
	}
	clientName := client.Config.Name
	// Narrow the whole client (name-*) to what access grants; empty means not granted.
	whole := []string{clientName + "-" + grant.Wildcard}
	if access == nil {
		return whole, true
	}
	served = access.NarrowMCPToolIncludeList(whole)
	return served, len(served) > 0
}

// virtualKeyOf is the key row behind a permit, when the permit is a key's. Nothing else this store
// funds, so any other permit answers with nothing.
func (gs *LocalGovernanceStore) virtualKeyOf(ctx context.Context, permit schemas.Permit) *registry.VirtualKey {
	if permit == nil || permit.Type() != string(grant.PermitVirtualKey) || permit.ID() == "" {
		return nil
	}
	vk, ok := gs.GetVirtualKeyByID(ctx, permit.ID())
	if !ok || vk == nil {
		return nil
	}
	return vk
}

// PermitProviderLimits reports what funds a key's use of one provider: the limits of every provider config
// the key holds for it. Every config, not the first: two configs for one provider are funded
// separately, and a request served by that provider draws on both. A config keeps its own row
// identity as holder, because nothing makes a provider unique within a key.
func (gs *LocalGovernanceStore) PermitProviderLimits(ctx context.Context, permit schemas.Permit, provider schemas.ModelProvider) (budgets []schemas.Limit, rateLimits []schemas.Limit) {
	vk := gs.virtualKeyOf(ctx, permit)
	if vk == nil || provider == "" {
		return nil, nil
	}
	for i := range vk.ProviderConfigs {
		config := vk.ProviderConfigs[i]
		if config.Provider != string(provider) {
			continue
		}
		holderID := strconv.FormatUint(uint64(config.ID), 10)
		budgets = append(budgets, grant.LimitsHeldBy(grant.LimitHolderVirtualKeyProviderConfig, holderID, vk.Name, config.Provider, "", budgetIDs(config.Budgets)...)...)
		rateLimits = append(rateLimits, grant.LimitsHeldBy(grant.LimitHolderVirtualKeyProviderConfig, holderID, vk.Name, config.Provider, "", idList(derefString(config.RateLimitID))...)...)
	}
	return budgets, rateLimits
}

// HolderLimits reports what funds a permit's holder whichever provider serves the request: for a
// virtual key, its own limits plus those of the team and customer it sits under.
//
// Separate from the permit, and resolved when a request's provider and model are settled rather than
// when its permit is built, because these limits cannot tell one provider from another. Load
// balancing asks which provider still has room; a limit that answers the same for every provider
// cannot help it choose, and asking it once per candidate is how the same question gets answered N
// times. PermitProviderLimits answers what discriminates, and this answers the rest.
//
// This is the seam a deployment reimplements to fund requests from something other than a key.
// Nothing downstream asks what kind of holder came back, so answering with a project's limits, or a
// user's, or both according to how that deployment is configured, needs no other path to change.
func (gs *LocalGovernanceStore) HolderLimits(ctx context.Context, permit schemas.Permit) (budgets []schemas.Limit, rateLimits []schemas.Limit) {
	vk := gs.virtualKeyOf(ctx, permit)
	if vk == nil {
		return nil, nil
	}

	budgets = grant.LimitsHeldBy(grant.LimitHolderVirtualKey, vk.ID, vk.Name, "", "", vk.BudgetIDs...)
	rateLimits = grant.LimitsHeldBy(grant.LimitHolderVirtualKey, vk.ID, vk.Name, "", "", idList(vk.RateLimitID)...)

	orgBudgets, orgRateLimits := gs.organizationLimitsForVirtualKey(ctx, vk)
	return append(budgets, orgBudgets...), append(rateLimits, orgRateLimits...)
}

// The team and customer rows a holder sits under, read straight from the in-memory maps.
//
// These answer "what does this team fund" without deciding who is asking or how the hierarchy above
// them is shaped. This store walks a key's team and customer itself, just below; a store that
// composes a different hierarchy (more levels, several customers per team, its own filtering) needs
// the same rows to walk and cannot reach this state any other way, so the lookups are exported and
// the walk is not.

// GetTeamCustomerID returns a team's customer id, or "" if the team is unknown or has no customer.
// A store composing a different team-to-customer hierarchy uses it to leave out the customer the
// key-team-customer chain already charges, so that customer is not charged twice.
func (gs *LocalGovernanceStore) GetTeamCustomerID(ctx context.Context, teamID string) string {
	if team, ok := gs.live.Collections.Teams.Get(teamID); ok {
		return team.CustomerID
	}
	return ""
}

// CollectTeamBudgets returns the live budgets a team holds, or nil when the team is unknown or
// holds none. Exported so a store composing its own hierarchy can fold team budgets into it the way
// a key's hierarchy folds them in.
func (gs *LocalGovernanceStore) CollectTeamBudgets(ctx context.Context, teamID string) []*configstoreTables.TableBudget {
	team, ok := gs.live.Collections.Teams.Get(teamID)
	if !ok {
		return nil
	}
	return gs.loadBudgets(ctx, team.BudgetIDs)
}

// CollectTeamRateLimits returns the live rate limit a team holds (at most one), or nil when the team
// is unknown or holds none. Exported for the same reason as CollectTeamBudgets.
func (gs *LocalGovernanceStore) CollectTeamRateLimits(ctx context.Context, teamID string) []*configstoreTables.TableRateLimit {
	team, ok := gs.live.Collections.Teams.Get(teamID)
	if !ok {
		return nil
	}
	return gs.loadRateLimits(ctx, team.RateLimitID)
}

// CollectCustomerBudgets returns the live budgets a customer holds, or nil when the customer is
// unknown or holds none.
func (gs *LocalGovernanceStore) CollectCustomerBudgets(ctx context.Context, customerID string) []*configstoreTables.TableBudget {
	customer, ok := gs.live.Collections.Customers.Get(customerID)
	if !ok {
		return nil
	}
	return gs.loadBudgets(ctx, customer.BudgetIDs)
}

// CollectCustomerRateLimits returns the live rate limit a customer holds (at most one), or nil when
// the customer is unknown or holds none.
func (gs *LocalGovernanceStore) CollectCustomerRateLimits(ctx context.Context, customerID string) []*configstoreTables.TableRateLimit {
	customer, ok := gs.live.Collections.Customers.Get(customerID)
	if !ok {
		return nil
	}
	return gs.loadRateLimits(ctx, customer.RateLimitID)
}

// loadBudgets returns the live budgets with these IDs, leaving out any this store does not hold,
// or nil when none is held.
func (gs *LocalGovernanceStore) loadBudgets(ctx context.Context, ids []string) []*configstoreTables.TableBudget {
	var list []*configstoreTables.TableBudget
	for _, id := range ids {
		if hot := gs.LoadBudget(ctx, id); hot != nil {
			list = append(list, hot)
		}
	}
	return list
}

// loadRateLimits returns the live rate limit with this ID as a list, or nil for "" or one this
// store does not hold.
func (gs *LocalGovernanceStore) loadRateLimits(ctx context.Context, id string) []*configstoreTables.TableRateLimit {
	if id == "" {
		return nil
	}
	if rl := gs.LoadRateLimit(ctx, id); rl != nil {
		return []*configstoreTables.TableRateLimit{rl}
	}
	return nil
}

// organizationLimitsForVirtualKey collects the limits a key answers to because of where it sits in
// the organization: its team's, and its customer's.
//
// The chain matches how enforcement derives it. A key attached straight to a customer answers to
// that customer; otherwise a customer is reached through the key's team, so a team's requests count
// against the customer containing it. Team and customer limits are provider-agnostic (they govern
// everything under them, whichever provider serves it), so they carry no provider of their own.
//
// The key names its team and customer by ID; their limits come from the team and customer records.
func (gs *LocalGovernanceStore) organizationLimitsForVirtualKey(ctx context.Context, vk *registry.VirtualKey) (budgets []schemas.Limit, rateLimits []schemas.Limit) {
	org := keyOrganizationOf(gs.live, vk)
	if org.team != nil {
		budgets = append(budgets, grant.LimitsHeldBy(grant.LimitHolderTeam, org.team.ID, org.team.Name, "", "", org.team.BudgetIDs...)...)
		rateLimits = append(rateLimits, grant.LimitsHeldBy(grant.LimitHolderTeam, org.team.ID, org.team.Name, "", "", idList(org.team.RateLimitID)...)...)
	}

	customer := org.customer
	if customer == nil && org.teamCustomer != nil {
		customer = org.teamCustomer
		// A request can be scoped to one customer of the key's team, and then only that customer pays.
		// The customer a key reaches through its team is dropped when the request is scoped to a
		// different one: the scoped customer is enforced by whoever scoped it, and charging the team's
		// as well would bill a customer the request was explicitly directed away from. A customer the
		// key names directly is not reached through a team, so scoping cannot redirect it.
		if scopedCustomerID, _ := ctx.Value(schemas.BifrostContextKeyGovernanceScopedCustomerID).(string); scopedCustomerID != "" && scopedCustomerID != customer.ID {
			customer = nil
		}
	}
	if customer != nil {
		budgets = append(budgets, grant.LimitsHeldBy(grant.LimitHolderCustomer, customer.ID, customer.Name, "", "", customer.BudgetIDs...)...)
		rateLimits = append(rateLimits, grant.LimitsHeldBy(grant.LimitHolderCustomer, customer.ID, customer.Name, "", "", idList(customer.RateLimitID)...)...)
	}
	return budgets, rateLimits
}

// budgetIDs names a set of budget rows. Knowing what a budget row looks like is this package's
// business, not the grant model's, so the rows stop here and only identities travel on.
func budgetIDs(rows []configstoreTables.TableBudget) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

// idList names a holder's single optional limit as the list a grant records. A key, a
// provider config, a team and a customer each carry at most one, but how many limits a holder
// answers to is the holder's business: a caller written against exactly one would have to be
// rewritten for the first holder with two.
func idList(id string) []string {
	if id == "" {
		return nil
	}
	return []string{id}
}

// CheckRateLimit checks rate limits for tokens and requests across categories
func (gs *LocalGovernanceStore) CheckRateLimit(ctx context.Context, entityWiseRateLimits EntityWiseRateLimits, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error) {
	// A live session's continuation was already counted as one request when the session was admitted.
	sessionContinuation := isLiveSessionContinuation(ctx)
	for entity, rateLimits := range entityWiseRateLimits {
		for _, rateLimit := range rateLimits {
			var violations []string
			// Check if rate limit needs reset (in-memory check)
			// Track which limits are expired so we can skip only those specific checks
			tokenLimitExpired := false
			if rateLimit.TokenResetDuration != nil {
				if duration, err := configstoreTables.ParseDuration(*rateLimit.TokenResetDuration); err == nil {
					if time.Since(rateLimit.TokenLastReset) >= duration {
						// Token rate limit expired but hasn't been reset yet - skip token check only
						tokenLimitExpired = true
					}
				}
			}
			requestLimitExpired := false
			if rateLimit.RequestResetDuration != nil {
				if duration, err := configstoreTables.ParseDuration(*rateLimit.RequestResetDuration); err == nil {
					if time.Since(rateLimit.RequestLastReset) >= duration {
						// Request rate limit expired but hasn't been reset yet - skip request check only
						requestLimitExpired = true
					}
				}
			}

			tokensBaseline, exists := tokensBaselines[rateLimit.ID]
			if !exists {
				tokensBaseline = 0
			}
			requestsBaseline, exists := requestsBaselines[rateLimit.ID]
			if !exists {
				requestsBaseline = 0
			}

			// Token limits - check if total usage (local + remote baseline) exceeds limit
			// Skip this check if token limit has expired
			if !tokenLimitExpired && rateLimit.TokenMaxLimit != nil && rateLimit.TokenCurrentUsage+tokensBaseline >= *rateLimit.TokenMaxLimit {
				duration := "unknown"
				if rateLimit.TokenResetDuration != nil {
					duration = *rateLimit.TokenResetDuration
				}
				violations = append(violations, fmt.Sprintf("token limit exceeded (%d/%d, resets every %s)",
					rateLimit.TokenCurrentUsage+tokensBaseline, *rateLimit.TokenMaxLimit, duration))
			}

			// Request limits - check if total usage (local + remote baseline) exceeds limit
			// Skip this check if request limit has expired
			if !sessionContinuation && !requestLimitExpired && rateLimit.RequestMaxLimit != nil && rateLimit.RequestCurrentUsage+requestsBaseline >= *rateLimit.RequestMaxLimit {
				duration := "unknown"
				if rateLimit.RequestResetDuration != nil {
					duration = *rateLimit.RequestResetDuration
				}
				violations = append(violations, fmt.Sprintf("request limit exceeded (%d/%d, resets every %s)",
					rateLimit.RequestCurrentUsage+requestsBaseline, *rateLimit.RequestMaxLimit, duration))
			}

			if len(violations) > 0 {
				// Determine specific violation type
				decision := DecisionRateLimited // Default to general rate limited decision
				if len(violations) == 1 {
					if strings.Contains(violations[0], "token") {
						decision = DecisionTokenLimited // More specific violation type
					} else if strings.Contains(violations[0], "request") {
						decision = DecisionRequestLimited // More specific violation type
					}
				}
				return decision, fmt.Errorf("rate limit violated for %s: %s", entity, violations)
			}
		}
	}
	return DecisionAllow, nil
}

// Generic check budget method
// The idea is to keep this as a common method for checking all budgets. The entire business logic resides in here
func (gs *LocalGovernanceStore) CheckBudget(ctx context.Context, entityWiseBudgets EntityWiseBudgets, baselines map[string]float64) (Decision, error) {
	// Check each budget in hierarchy order using in-memory data
	now := time.Now()
	for entity, budgets := range entityWiseBudgets {
		for _, budget := range budgets {
			// Whether the window has closed must be decided by exactly the same
			// predicate the reset path uses. Open-coding it here previously
			// disagreed with budgetResetTarget in two ways: it ignored
			// IsCalendarAligned, so a calendar-aligned budget was judged on a
			// rolling clock for enforcement while resetting on the calendar; and
			// it had no non-positive-duration guard, so because time.Since is
			// always at least zero such a budget skipped enforcement forever,
			// which is unlimited spend.
			//
			// An expired-but-unswept budget is skipped rather than denied: the
			// reset lands on the next sweep or in the post-request usage bump,
			// and skipping errs permissive for at most one ticker interval.
			if gs.budgetResetTarget(budget, now) != nil {
				gs.logger.Debug("LocalStore CheckBudget: Budget %s (%s) expired, skipping check", budget.ID, entity)
				continue
			}
			baseline, exists := baselines[budget.ID]
			if !exists {
				baseline = 0
			}
			effectiveMaxLimit := budget.EffectiveMaxLimit()
			gs.logger.Debug("LocalStore CheckBudget: Checking %s budget %s: local=%.4f, remote=%.4f, total=%.4f, limit=%.4f",
				entity, budget.ID, budget.CurrentUsage, baseline, budget.CurrentUsage+baseline, effectiveMaxLimit)
			// Check if current usage (local + remote baseline) exceeds budget limit
			if budget.CurrentUsage+baseline >= effectiveMaxLimit {
				gs.logger.Debug("LocalStore CheckBudget: Budget %s EXCEEDED", budget.ID)
				return DecisionBudgetExceeded, fmt.Errorf("%s budget exceeded: %.4f >= %.4f dollars",
					entity, budget.CurrentUsage+baseline, effectiveMaxLimit)
			}
		}
	}
	return DecisionAllow, nil
}

const modelConfigWildcard = configstoreTables.ModelConfigAllModels

// modelConfigStoreKey builds the in-memory cache key for a model config.
func modelConfigStoreKey(scope, scopeID, modelKey string, provider *string) string {
	base := modelKey
	if provider != nil {
		base = fmt.Sprintf("%s:%s", modelKey, *provider)
	}
	if scope == "" || scope == configstoreTables.ModelConfigScopeGlobal {
		return base
	}
	return fmt.Sprintf("%s:%s:%s", scope, scopeID, base)
}

// modelConfigKeys returns the two in-memory lookup keys a model name resolves to: its canonical
// spelling and its base-model alias. Storing a model config and looking one up both go through
// this function, so the two sides can never disagree on what a model is called and a limit set
// on a model cannot be side-stepped by respelling it.
//
// The canonical spelling is derived in this order:
//  1. surrounding whitespace is trimmed;
//  2. the "*" all-models sentinel is returned unchanged as both keys;
//  3. a leading provider prefix is removed when it names the given provider ("openai/gpt-4o" on
//     openai is "gpt-4o"). With no provider, any known-provider prefix is removed, which is how
//     the catalog already resolves cross-provider names. A prefix naming a different provider is
//     kept: nested slugs such as groq's "openai/gpt-oss-120b" are the model's real name;
//  4. the result is lower-cased.
//
// The base key is the lower-cased catalog base model of the prefix-stripped name
// ("gpt-4o-2024-08-06" is a "gpt-4o"). Without a catalog, or when the name has no recognised
// alias, it equals the canonical key.
func (gs *LocalGovernanceStore) modelConfigKeys(model string, provider *string) (canonical, base string) {
	canonical = CanonicalModelConfigName(model, provider)
	if canonical == "" || canonical == modelConfigWildcard {
		return canonical, canonical
	}
	base = canonical
	if gs.modelCatalog != nil {
		if baseName := gs.modelCatalog.GetBaseModelName(canonical); baseName != "" {
			base = strings.ToLower(baseName)
		}
	}
	return canonical, base
}

// CanonicalModelConfigName is the catalog-independent spelling a model config is keyed by:
// trimmed, lower-cased, with a provider prefix stripped when it names the config's own provider
// (any prefix when the config is provider-less). Two configs whose names canonicalise to the same
// value in the same scope and provider would index to one key and shadow each other, so the
// management API refuses that at create and update time.
func CanonicalModelConfigName(model string, provider *string) string {
	// Lower-case before parsing so a prefix spelled "OpenAI/" is recognised as the openai
	// provider; the result is lower-cased anyway.
	name := strings.ToLower(strings.TrimSpace(model))
	if name == "" || name == modelConfigWildcard {
		return name
	}
	if provider != nil {
		if prefix, rest := schemas.ParseModelString(name, ""); prefix != "" && strings.EqualFold(string(prefix), strings.TrimSpace(*provider)) {
			name = rest
		}
	} else {
		_, name = schemas.ParseModelString(name, "")
	}
	return name
}

// ModelConfigIndexKey returns the model spelling the model config index keys a config for
// (model, provider) under, so the management API can refuse a config that would share a key
// with an existing one: the catalog base name for a provider-less config (every alias of a
// base model collapses onto it), the canonical spelling for a provider-scoped one.
func (gs *LocalGovernanceStore) ModelConfigIndexKey(model string, provider *string) string {
	canonical, base := gs.modelConfigKeys(model, provider)
	if provider != nil {
		return canonical
	}
	return base
}

// modelConfigIndexKey is the key a model config is indexed under: its scope, then the model name
// lookups ask for (the catalog base name for a provider-less config, the canonical spelling
// otherwise), then its provider.
func (gs *LocalGovernanceStore) modelConfigIndexKey(mc *registry.ModelConfig) string {
	canonical, base := gs.modelConfigKeys(mc.ModelName, mc.Provider)
	modelKey := base
	if mc.Provider != nil {
		modelKey = canonical
	}
	return modelConfigStoreKey(mc.Scope, mc.ScopeID, modelKey, mc.Provider)
}

// indexModelConfig puts a model config under its key, moving it off the key it had before. Two configs
// whose names fold to one key shadow each other: the one indexed last answers, with a warning, as a
// later write always has.
func (gs *LocalGovernanceStore) indexModelConfig(mc *registry.ModelConfig) {
	key := gs.modelConfigIndexKey(mc)
	if previous, ok := gs.modelConfigKeyOf.Get(mc.ID); ok && previous != key {
		if owner, _ := gs.modelConfigIndex.Get(previous); owner == mc.ID {
			gs.modelConfigIndex.Delete(previous)
		}
	}
	if owner, ok := gs.modelConfigIndex.Get(key); ok && owner != mc.ID {
		shadowedName := ""
		if shadowed, ok := gs.live.Collections.ModelConfigs.Get(owner); ok {
			shadowedName = shadowed.ModelName
		}
		gs.logger.Warn("model config %s (%s) shadows model config %s (%s) under the same key %q", mc.ID, mc.ModelName, owner, shadowedName, key)
	}
	gs.modelConfigIndex.Put(key, mc.ID)
	gs.modelConfigKeyOf.Put(mc.ID, key)
}

// unindexModelConfig removes a model config from the key it was indexed under. A config it shadowed
// does not come back: as before, the key is empty until a config is indexed there again.
func (gs *LocalGovernanceStore) unindexModelConfig(mc *registry.ModelConfig) {
	key, ok := gs.modelConfigKeyOf.Get(mc.ID)
	if !ok {
		key = gs.modelConfigIndexKey(mc)
	}
	if owner, _ := gs.modelConfigIndex.Get(key); owner == mc.ID {
		gs.modelConfigIndex.Delete(key)
	}
	gs.modelConfigKeyOf.Delete(mc.ID)
}

// modelConfigAt returns the model config answering for key, nil when none does.
func (gs *LocalGovernanceStore) modelConfigAt(key string) *registry.ModelConfig {
	id, ok := gs.modelConfigIndex.Get(key)
	if !ok {
		return nil
	}
	mc, _ := gs.live.Collections.ModelConfigs.Get(id)
	return mc
}

// extractModelAndProvider extracts the model name and optional provider from a request.
func extractModelAndProvider(request *EvaluationRequest) (string, *string) {
	if request == nil {
		return "", nil
	}
	var provider *string
	if request.Provider != "" {
		p := string(request.Provider)
		provider = &p
	}
	return request.Model, provider
}

type limitsContextKey struct{}

// literalLimitsContextKey marks a context whose model runs at the provider exactly as written, so
// no key alias is applied to it and no alias target's limits may be added.
var literalLimitsContextKey = limitsContextKey{}

type chargedKeyContextKey struct{}

// chargedKeyContext marks a limit walk made at charge time for the key that ran: alias targets are
// then the ones that key maps the name to, not every eligible key's. See limitsForKeyThatRan.
var chargedKeyContext = chargedKeyContextKey{}

type aliasLimitTargetsContextKey struct{}

// aliasLimitTargetsContext holds a settledAliasTargets on the request once GatherLimits has added
// alias targets' limits to it.
var aliasLimitTargetsContext = aliasLimitTargetsContextKey{}

// settledAliasTargets records that the limits settled for (provider, model) include the limits of
// targets, the models the eligible keys map the name to. A handful of model names, not stream data.
type settledAliasTargets struct {
	provider schemas.ModelProvider
	model    string
	targets  []string
}

// runsAsWritten reports whether the model on ctx reaches the provider exactly as the client named
// it: the caller marked it literal, or the credential skips key selection, so no key alias is
// ever applied.
func runsAsWritten(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	if literal, _ := ctx.Value(literalLimitsContextKey).(bool); literal {
		return true
	}
	if _, direct := ctx.Value(schemas.BifrostContextKeyDirectKey).(schemas.Key); direct {
		return true
	}
	skip, _ := ctx.Value(schemas.BifrostContextKeySkipKeySelection).(bool)
	return skip
}

// aliasTargets returns the models the provider's eligible keys map the requested name to, or nil
// when the lookup has no provider, the store cannot see provider config, or no key carries an
// alias. It is resolved once per attempt and handed to the model-config walks, so the provider
// lookup (a shared read lock in production) and the alias scan are not repeated per scope.
func (gs *LocalGovernanceStore) aliasTargets(ctx context.Context, provider *string, model string) []string {
	if provider == nil {
		return nil
	}
	// The context reads come after HasKeyAliases and MayBeKeyAlias: each takes the context's lock,
	// and a provider without aliases, or a name that is no alias, must not pay for them.
	list, ok := gs.live.Views.ProviderKeyLists.Get(*provider)
	if !ok {
		return nil
	}
	all := *list // shared, read only
	if !modelcatalog.HasKeyAliases(all) || !modelcatalog.MayBeKeyAlias(schemas.ModelProvider(*provider), model, all) || runsAsWritten(ctx) {
		return nil
	}
	keys, ok := pinnedKeys(ctx, all)
	if !ok {
		return nil
	}
	// At charge time only the key that ran counts: a model another key would have run never ran.
	if keyID, charging := ctx.Value(chargedKeyContext).(string); charging {
		if keys, ok = keyByID(keys, keyID); !ok {
			return nil
		}
	}
	var targets []string
	collect := func(target string) {
		if !slices.Contains(targets, target) {
			targets = append(targets, target)
		}
	}
	modelcatalog.KeyAliasTargets(model, keys, allKeyIDs, collect)
	// Key selection picks only among the keys the request's access allows. Asked only once the name
	// is an alias at all, so a provider whose aliases do not cover the name pays nothing for it.
	if len(targets) > 0 {
		if keyIDs, restricted := accessKeysForModel(ctx, *provider, model); restricted {
			targets = targets[:0]
			modelcatalog.KeyAliasTargets(model, keys, keyIDs, collect)
		}
	}
	return targets
}

// allKeyIDs is the key restriction that restricts nothing.
var allKeyIDs = schemas.WhiteList{"*"}

// accessKeysForModel is the key restriction the access on ctx's grant places on (provider, model),
// as Evaluate publishes it to key selection. No grant, no access, or no restriction answers false.
func accessKeysForModel(ctx context.Context, provider, model string) (schemas.WhiteList, bool) {
	bctx, ok := ctx.(*schemas.BifrostContext)
	if !ok || bctx == nil {
		return nil, false
	}
	g := bctx.Grant()
	if g == nil {
		return nil, false
	}
	access := g.Access()
	if access == nil {
		return nil, false
	}
	// KeysForModel re-runs the model checks; an access none of whose permits restricts the
	// provider's keys answers without them.
	if ga, ok := access.(*grant.Access); ok && !ga.RestrictsKeys(provider) {
		return nil, false
	}
	keyIDs, restricted := access.KeysForModel(provider, model)
	return schemas.WhiteList(keyIDs), restricted
}

// keyByID is the one-key subslice of keys whose ID is id, and false when no key has it.
func keyByID(keys []schemas.Key, id string) ([]schemas.Key, bool) {
	for i := range keys {
		if keys[i].ID == id {
			return keys[i : i+1], true
		}
	}
	return nil, false
}

// pinnedKeys narrows keys to the one a pinned request is served from, in core's order: a routing-rule
// pin, then the caller's key id, then the caller's key name. Unpinned, keys is returned as is. A pin
// that names no key returns false: core refuses that request, so no alias of it applies. The result
// is a subslice, so nothing is allocated.
func pinnedKeys(ctx context.Context, keys []schemas.Key) ([]schemas.Key, bool) {
	if ctx == nil {
		return keys, true
	}
	byID := func(id string) ([]schemas.Key, bool) {
		for i := range keys {
			if keys[i].ID == id {
				return keys[i : i+1], true
			}
		}
		return nil, false
	}
	if pin, _ := ctx.Value(schemas.BifrostContextKeyRoutingPinnedAPIKeyID).(string); strings.TrimSpace(pin) != "" {
		return byID(strings.TrimSpace(pin))
	}
	if pin, _ := ctx.Value(schemas.BifrostContextKeyAPIKeyID).(string); strings.TrimSpace(pin) != "" {
		return byID(strings.TrimSpace(pin))
	}
	if pin, _ := ctx.Value(schemas.BifrostContextKeyAPIKeyName).(string); strings.TrimSpace(pin) != "" {
		name := strings.TrimSpace(pin)
		for i := range keys {
			if keys[i].Name == name {
				return keys[i : i+1], true
			}
		}
		return nil, false
	}
	return keys, true
}

// collectModelConfigsFor returns every model config that applies to a request for
// (model, provider) within a single scope, across six tiers (most → least specific),
// deduped by config ID. The model is canonicalised by modelConfigKeys, the same way it
// was when the configs were indexed, so every spelling of a model reaches its limits:
//  1. (canonical model, provider)  exact model on this provider
//  2. (base model, provider)       the model's base alias on this provider
//  3. (canonical model, nil)       exact model on all providers
//  4. (base model, nil)            the model's base alias on all providers
//  5. ("*", provider)              all models on this provider  (provider-level governance)
//  6. ("*", nil)                   all models on all providers
//
// When targets is non-nil (the models a key alias resolves to), tiers 1-4 are repeated for each
// target after the six tiers above, deduped by config ID, so a limit on the aliased model also
// applies to a request that names the alias.
//
// This is the single source of truth for "which model configs apply"; every budget /
// rate-limit check and usage-tracking site iterates it so the wildcard tiers are matched
// consistently everywhere.
func (gs *LocalGovernanceStore) collectModelConfigsFor(ctx context.Context, scope, scopeID, model string, provider *string, targets []string) []*registry.ModelConfig {
	var out []*registry.ModelConfig
	seen := make(map[string]bool)
	add := func(mc *registry.ModelConfig) {
		if mc == nil || seen[mc.ID] {
			return
		}
		seen[mc.ID] = true
		out = append(out, mc)
	}
	loadKey := func(modelKey string, prov *string) *registry.ModelConfig {
		return gs.modelConfigAt(modelConfigStoreKey(scope, scopeID, modelKey, prov))
	}
	if provider != nil {
		canonical, base := gs.modelConfigKeys(model, provider)
		add(loadKey(canonical, provider)) // tier 1: exact model + provider
		add(loadKey(base, provider))      // tier 2: base model + provider
	}
	canonical, base := gs.modelConfigKeys(model, nil)
	add(loadKey(canonical, nil)) // tier 3: exact model, all providers
	add(loadKey(base, nil))      // tier 4: base model, all providers
	if provider != nil {
		add(loadKey(modelConfigWildcard, provider)) // tier 5: all models on this provider
	}
	add(loadKey(modelConfigWildcard, nil)) // tier 6: all models, all providers
	// A key alias is resolved only at key selection, after this lookup, so a limit set on the model
	// an alias stands for would never meet a request that names the alias. targets are the models the
	// eligible keys map the name to (see aliasTargets), resolved once per attempt by the caller; each
	// is looked up the same way the name itself was, and the dedupe above keeps a limit reached twice
	// as one.
	for _, target := range targets {
		if provider != nil {
			targetCanonical, targetBase := gs.modelConfigKeys(target, provider)
			add(loadKey(targetCanonical, provider))
			add(loadKey(targetBase, provider))
		}
		targetCanonical, targetBase := gs.modelConfigKeys(target, nil)
		add(loadKey(targetCanonical, nil))
		add(loadKey(targetBase, nil))
	}
	return out
}

// modelConfigEntityKey builds a stable, unique entity description for a model config
func modelConfigEntityKey(mc *registry.ModelConfig) string {
	name := mc.ModelName
	if name == modelConfigWildcard {
		name = "AllModels"
	}
	key := "Model:" + name
	if mc.Provider != nil {
		key += ":Provider:" + *mc.Provider
	}
	if mc.Scope != "" && mc.Scope != configstoreTables.ModelConfigScopeGlobal {
		key += ":" + mc.Scope + ":" + mc.ScopeID
	}
	return key
}

// GetTeamName returns a team's display name from the in-memory store, or "" if
// the team is unknown. The enterprise layer uses it as the fallback for log
// stamping when its edge-driven name caches miss (e.g. a team with no user
// members and no business unit).
func (gs *LocalGovernanceStore) GetTeamName(ctx context.Context, teamID string) string {
	if team, ok := gs.live.Collections.Teams.Get(teamID); ok {
		return team.Name
	}
	return ""
}

// GetCustomerName returns a customer's display name from the in-memory store,
// or "" if the customer is unknown. Same fallback role as GetTeamName.
func (gs *LocalGovernanceStore) GetCustomerName(ctx context.Context, customerID string) string {
	if customer, ok := gs.live.Collections.Customers.Get(customerID); ok {
		return customer.Name
	}
	return ""
}

// budgetResetTarget returns the LastReset value to write when budget is expired,
// or nil when the current window is still open.
//
// The returned value is always a window boundary from TableBudget.WindowStart,
// never the caller's wall clock. That distinction is the whole point: a boundary
// is a pure function of the persisted row, so every node in a cluster writes the
// identical value and the LastReset guards in ResetBudgetAt and in the reset SQL
// collapse duplicate work instead of each node advancing to its own timestamp.
// Stamping now() instead makes LastReset a function of ticker phase, which then
// becomes the origin of that node's next window, so the phase error compounds
// every cycle. LastReset is only ever read here to decide whether to write; it
// never influences what gets written.
//
// A gap of many windows collapses to a single reset at the current boundary
// rather than replaying one reset per elapsed window, and applying the returned
// target makes an immediate re-evaluation return nil, so a target can never be
// perpetually due (issue #4851 class).
func (gs *LocalGovernanceStore) budgetResetTarget(budget *configstoreTables.TableBudget, now time.Time) *time.Time {
	if budget == nil || budget.ResetDuration == "" {
		return nil
	}
	// Sub-day durations have no calendar boundary; see rateLimitResetTarget.
	// WindowStart applies the same guard, so validation here exists purely to
	// surface a misconfigured duration in the logs.
	if !budget.IsCalendarAligned || !configstoreTables.IsCalendarAlignableDuration(budget.ResetDuration) {
		duration, err := configstoreTables.ParseDuration(budget.ResetDuration)
		if err != nil {
			gs.logger.Error("invalid budget reset duration %s: %v", budget.ResetDuration, err)
			return nil
		}
		if duration <= 0 {
			gs.logger.Error("non-positive budget reset duration %s: budget will not auto-reset", budget.ResetDuration)
			return nil
		}
	}
	target := budget.WindowStart(now)
	if target.After(budget.LastReset) {
		return &target
	}
	return nil
}

// resetExpiredBudgetFromSnapshot applies the local side effects for an expired budget snapshot.
// Owner reference refresh is NOT done here; ResetExpiredBudgetsInMemory batches it
// once per sweep so a sweep costs O(owners + resets) instead of O(owners x resets).
func (gs *LocalGovernanceStore) resetExpiredBudgetFromSnapshot(ctx context.Context, budget *configstoreTables.TableBudget, now time.Time) *configstoreTables.TableBudget {
	newLastReset := gs.budgetResetTarget(budget, now)
	if newLastReset == nil {
		return nil
	}
	resetBudget, ok := gs.ResetBudgetAt(ctx, budget.ID, *newLastReset)
	if !ok {
		return nil
	}
	oldUsage := budget.CurrentUsage
	gs.logger.Debug(fmt.Sprintf("Reset budget %s (was %.2f, reset to 0)", resetBudget.ID, oldUsage))
	return resetBudget
}

// ResetBudgetUsageInMemory clears one budget's usage on operator request, without
// moving its reset boundary.
//
// This is the deliberate counterpart to resetExpiredBudgetFromSnapshot above. That
// one is driven by a window closing, so it advances LastReset; this one is driven
// by a person choosing "reset usage", where the window has not closed and the
// boundary must stay exactly where it is. Every persistence path guards LastReset
// forward-only, so a boundary move here would be refused anyway.
//
// Clearing the LastDBUsages baseline is not optional. The dump path writes the
// difference between in-memory usage and that baseline, and in a cluster each
// node contributes CurrentUsage - LastDBUsage to the shared total. Leaving the
// old baseline behind would re-add the spend that was just cleared, or drive the
// contribution negative.
//
// Returns the reset snapshot and true, or (nil, false) when the budget is unknown.
func (gs *LocalGovernanceStore) ResetBudgetUsageInMemory(ctx context.Context, budgetID string) (*configstoreTables.TableBudget, bool) {
	// Serialize the counter/baseline clear with accounting and dump capture.
	// This does not start a new window or advance its durable reset cutoff.
	gs.LastDBUsagesBudgetsMu.Lock()
	defer gs.LastDBUsagesBudgetsMu.Unlock()
	for {
		raw, exists := gs.budgets.Load(budgetID)
		if !exists || raw == nil {
			return nil, false
		}
		old, ok := raw.(*configstoreTables.TableBudget)
		if !ok || old == nil {
			return nil, false
		}
		clone := *old
		clone.CurrentUsage = 0
		if gs.budgets.CompareAndSwap(budgetID, raw, &clone) {
			gs.LastDBUsagesBudgets[budgetID] = 0
			gs.markBudgetResetLocked(budgetID)
			gs.logger.Debug(fmt.Sprintf("Reset budget %s usage on operator request (boundary unchanged at %s)", budgetID, clone.LastReset))
			return &clone, true
		}
	}
}

// AdoptCalendarAlignmentInMemory re-anchors a budget onto the calendar grid when
// its owner has just switched alignment on, and reports whether it moved.
//
// Called on the false-to-true transition only. Without it the switch is
// destructive: budgetResetTarget resets whenever WindowStart(now) is after
// LastReset, so a window that opened before the current boundary is instantly due
// and the next sweep clears its usage. See TableBudget.AdoptCalendarAlignment.
//
// The usage carried into the swap is the one observed inside the CAS loop, not a
// value read beforehand: a request bumping this budget between a read and the
// write would otherwise have its spend dropped. No reset generation is bumped
// here - nothing was reset, so an in-flight dump's rows are still accurate.
func (gs *LocalGovernanceStore) AdoptCalendarAlignmentInMemory(ctx context.Context, budgetID string, now time.Time) bool {
	for {
		raw, exists := gs.budgets.Load(budgetID)
		if !exists || raw == nil {
			return false
		}
		old, ok := raw.(*configstoreTables.TableBudget)
		if !ok || old == nil {
			return false
		}
		clone := *old
		// The owner's flag has already been applied upstream, but the in-memory
		// copy predates it, so stamp it here or adoption declines its own work.
		clone.IsCalendarAligned = true
		if !clone.AdoptCalendarAlignment(now) {
			return false
		}
		clone.RefreshOverrideCyclesRemaining()
		if gs.budgets.CompareAndSwap(budgetID, raw, &clone) {
			gs.logger.Debug(fmt.Sprintf("Adopted budget %s onto its calendar boundary %s (usage %.2f preserved)",
				budgetID, clone.LastReset, clone.CurrentUsage))
			return true
		}
	}
}

// AdoptRateLimitCalendarAlignmentInMemory is the rate-limit counterpart, applying
// the same switch-over rule to the token and request counters independently.
func (gs *LocalGovernanceStore) AdoptRateLimitCalendarAlignmentInMemory(ctx context.Context, rateLimitID string, now time.Time) bool {
	for {
		raw, exists := gs.rateLimits.Load(rateLimitID)
		if !exists || raw == nil {
			return false
		}
		old, ok := raw.(*configstoreTables.TableRateLimit)
		if !ok || old == nil {
			return false
		}
		clone := *old
		clone.IsCalendarAligned = true
		if !clone.AdoptCalendarAlignment(now) {
			return false
		}
		if gs.rateLimits.CompareAndSwap(rateLimitID, raw, &clone) {
			gs.logger.Debug(fmt.Sprintf("Adopted rate limit %s onto its calendar boundaries (token %s, request %s)",
				rateLimitID, clone.TokenLastReset, clone.RequestLastReset))
			return true
		}
	}
}

// ResetExpiredBudgetsInMemory checks and resets budgets that have exceeded their reset duration.
// With no budgetIDs it scans every budget; with IDs it only checks those budgets.
func (gs *LocalGovernanceStore) ResetExpiredBudgetsInMemory(ctx context.Context, budgetIDs ...string) []*configstoreTables.TableBudget {
	now := time.Now()
	var resetBudgets []*configstoreTables.TableBudget
	resetOne := func(value any) {
		budget, ok := value.(*configstoreTables.TableBudget)
		if !ok || budget == nil {
			return
		}
		if resetBudget := gs.resetExpiredBudgetFromSnapshot(ctx, budget, now); resetBudget != nil {
			resetBudgets = append(resetBudgets, resetBudget)
		}
	}
	if len(budgetIDs) == 0 {
		gs.budgets.Range(func(key, value any) bool {
			resetOne(value)
			return true
		})
	} else {
		for _, budgetID := range budgetIDs {
			if value, ok := gs.budgets.Load(budgetID); ok {
				resetOne(value)
			}
		}
	}
	if len(resetBudgets) > 0 {
		if onBudgetsReset := gs.getBudgetsResetHook(); onBudgetsReset != nil {
			onBudgetsReset(resetBudgets)
		}
	}
	return resetBudgets
}

// rateLimitResetTarget returns the LastReset value to write when a rate-limit counter is expired.
// Calendar alignment only applies to durations with a calendar boundary (d/w/M/Q/Y);
// sub-day durations fall back to rolling-window semantics even when the owner is
// calendar-aligned, mirroring the handler-side snap logic. Without this guard
// GetCalendarPeriodStart returns now for sub-day durations, making the reset
// target perpetually due and spinning BumpRateLimitUsage forever (issue #4851).
//
// Rate limits carry no quarter definition - quarters are configured per budget -
// so the quarter start is not applicable here and plain calendar quarters apply.
// The returned value is a window boundary anchored on the rate limit's
// CreatedAt, never the caller's wall clock, so every cluster node computes the
// same instant from the same persisted row. See budgetResetTarget for why that
// matters; the two must stay in step because a rate limit and a budget on the
// same owner are expected to roll over together.
func (gs *LocalGovernanceStore) rateLimitResetTarget(resetDuration *string, calendarAligned bool, anchor, lastReset time.Time, now time.Time) *time.Time {
	if resetDuration == nil {
		return nil
	}
	if calendarAligned && configstoreTables.IsCalendarAlignableDuration(*resetDuration) {
		period := configstoreTables.GetCalendarPeriodStart(*resetDuration, now, configstoreTables.QuarterStartNotApplicable)
		if period.After(lastReset) {
			return &period
		}
		return nil
	}
	duration, err := configstoreTables.ParseDuration(*resetDuration)
	if err != nil {
		gs.logger.Error("invalid rate limit reset duration %s: %v", *resetDuration, err)
		return nil
	}
	// A non-positive duration would be perpetually due, spinning BumpRateLimitUsage
	// forever (issue #4851 class); treat it as invalid, same as unparseable.
	if duration <= 0 {
		gs.logger.Error("non-positive rate limit reset duration %s: counter will not auto-reset", *resetDuration)
		return nil
	}
	if anchor.IsZero() {
		anchor = lastReset
	}
	target := configstoreTables.RollingWindowStart(anchor, duration, now)
	if target.After(lastReset) {
		return &target
	}
	return nil
}

// rateLimitResetTargets returns reset targets for the token and request counters.
// Both dimensions share the rate limit's CreatedAt as their lattice anchor, so
// they stay phase-locked to each other as well as across nodes.
func (gs *LocalGovernanceStore) rateLimitResetTargets(rateLimit *configstoreTables.TableRateLimit, now time.Time) (*time.Time, *time.Time) {
	if rateLimit == nil {
		return nil, nil
	}
	calendarAligned := rateLimit.IsCalendarAligned
	anchor := rateLimit.CreatedAt
	return gs.rateLimitResetTarget(rateLimit.TokenResetDuration, calendarAligned, anchor, rateLimit.TokenLastReset, now),
		gs.rateLimitResetTarget(rateLimit.RequestResetDuration, calendarAligned, anchor, rateLimit.RequestLastReset, now)
}

// resetExpiredRateLimitFromSnapshot applies the local side effects for an expired rate-limit snapshot.
// Owner reference refresh is NOT done here; ResetExpiredRateLimitsInMemory batches it
// once per sweep so a sweep costs O(owners + resets) instead of O(owners x resets).
func (gs *LocalGovernanceStore) resetExpiredRateLimitFromSnapshot(ctx context.Context, rateLimit *configstoreTables.TableRateLimit, now time.Time) *RateLimitReset {
	tokenNewLastReset, requestNewLastReset := gs.rateLimitResetTargets(rateLimit, now)
	if tokenNewLastReset == nil && requestNewLastReset == nil {
		return nil
	}
	resetRateLimit, ok := gs.ResetRateLimitAt(ctx, rateLimit.ID, tokenNewLastReset, requestNewLastReset)
	if !ok {
		return nil
	}
	return resetRateLimit
}

// ResetExpiredRateLimitsInMemory performs reset of expired rate limits for both provider-level and VK-level.
// With no rateLimitIDs it scans every rate limit; with IDs it only checks those rate limits.
func (gs *LocalGovernanceStore) ResetExpiredRateLimitsInMemory(ctx context.Context, rateLimitIDs ...string) []*RateLimitReset {
	now := time.Now()
	var resetRateLimits []*RateLimitReset
	resetOne := func(value any) {
		rateLimit, ok := value.(*configstoreTables.TableRateLimit)
		if !ok || rateLimit == nil {
			return
		}
		if resetRateLimit := gs.resetExpiredRateLimitFromSnapshot(ctx, rateLimit, now); resetRateLimit != nil {
			resetRateLimits = append(resetRateLimits, resetRateLimit)
		}
	}
	if len(rateLimitIDs) == 0 {
		gs.rateLimits.Range(func(key, value any) bool {
			resetOne(value)
			return true
		})
	} else {
		for _, rateLimitID := range rateLimitIDs {
			if value, ok := gs.rateLimits.Load(rateLimitID); ok {
				resetOne(value)
			}
		}
	}
	if len(resetRateLimits) > 0 {
		if onRateLimitsReset := gs.getRateLimitsResetHook(); onRateLimitsReset != nil {
			snapshots := make([]*configstoreTables.TableRateLimit, 0, len(resetRateLimits))
			for _, reset := range resetRateLimits {
				snapshots = append(snapshots, reset.TableRateLimit)
			}
			onRateLimitsReset(snapshots)
		}
	}
	return resetRateLimits
}

// ResetExpiredBudgets checks and resets budgets that have exceeded their reset duration in database
func (gs *LocalGovernanceStore) ResetExpiredBudgets(ctx context.Context, resetBudgets []*configstoreTables.TableBudget) error {
	// Persist to database if any resets occurred using direct UPDATE to avoid
	// overwriting config fields. Both statements share the strict "<" guard so a
	// snapshot that lost the override race cannot advance last_reset on its own.
	if gs.configStore == nil {
		return nil
	}
	rows := make([]budgetDumpRow, 0, len(resetBudgets))
	for _, budget := range resetBudgets {
		if budget == nil {
			continue
		}
		rows = append(rows, budgetDumpRow{
			ID:                      budget.ID,
			CurrentUsage:            budget.CurrentUsage,
			LastReset:               budget.LastReset,
			UsageResetEpoch:         budget.UsageResetEpoch,
			OverrideAmount:          budget.OverrideAmount,
			OverrideMode:            budget.OverrideMode,
			OverrideCyclesRemaining: budget.OverrideCyclesRemaining,
			OverrideCyclesTotal:     budget.OverrideCyclesTotal,
			OverrideAnchorReset:     budget.OverrideAnchorReset,
		})
	}
	// Stable ID order keeps concurrent writers taking row locks in the same
	// sequence, which is what keeps them deadlock-free rather than merely lucky.
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })

	// Chunked so row locks are released between batches instead of being held
	// for every reset row at once, and so each batch costs one round trip
	// rather than two per row. A batch that fails leaves earlier batches
	// committed, which is safe: every write is guarded and monotonic, so the
	// next sweep re-applies whatever did not land.
	for start := 0; start < len(rows); start += dumpBatchSize {
		end := min(start+dumpBatchSize, len(rows))
		if err := gs.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
			return gs.writeBudgetBatch(ctx, tx, rows[start:end], "<")
		}); err != nil {
			return fmt.Errorf("failed to persist budget resets to database: %w", err)
		}
	}

	return nil
}

// ResetExpiredRateLimits performs background reset of expired rate limits for both provider-level and VK-level in database
func (gs *LocalGovernanceStore) ResetExpiredRateLimits(ctx context.Context, resetRateLimits []*RateLimitReset) error {
	if gs.configStore == nil {
		return nil
	}
	// Calendar adoption can advance a boundary without clearing usage.
	// Only persist dimensions the in-memory reset actually cleared.
	var tokenRows, requestRows []rateLimitBoundaryRow
	for _, rateLimit := range resetRateLimits {
		if rateLimit == nil {
			continue
		}
		if rateLimit.tokenReset {
			tokenRows = append(tokenRows, rateLimitBoundaryRow{ID: rateLimit.ID, Boundary: rateLimit.TokenLastReset, UsageResetEpoch: rateLimit.TokenUsageResetEpoch})
		}
		if rateLimit.requestReset {
			requestRows = append(requestRows, rateLimitBoundaryRow{ID: rateLimit.ID, Boundary: rateLimit.RequestLastReset, UsageResetEpoch: rateLimit.RequestUsageResetEpoch})
		}
	}
	// Stable ID order within each dimension, and a fixed dimension order, so
	// concurrent writers acquire row locks in the same sequence.
	sort.Slice(tokenRows, func(i, j int) bool { return tokenRows[i].ID < tokenRows[j].ID })
	sort.Slice(requestRows, func(i, j int) bool { return requestRows[i].ID < requestRows[j].ID })

	dimensions := []struct {
		usageColumn    string
		boundaryColumn string
		rows           []rateLimitBoundaryRow
	}{
		{"token_current_usage", "token_last_reset", tokenRows},
		{"request_current_usage", "request_last_reset", requestRows},
	}
	for _, dimension := range dimensions {
		for start := 0; start < len(dimension.rows); start += dumpBatchSize {
			end := min(start+dumpBatchSize, len(dimension.rows))
			batch := dimension.rows[start:end]
			if err := gs.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
				return gs.resetRateLimitDimensionBatch(ctx, tx, dimension.usageColumn, dimension.boundaryColumn, batch)
			}); err != nil {
				return fmt.Errorf("failed to persist rate limit resets to database: %w", err)
			}
		}
	}
	return nil
}

// dumpBatchSize bounds both how many rows one batched UPDATE folds together and
// how many rows a single dump transaction covers.
//
// The dump used to issue one statement per row inside one transaction spanning
// the whole table. That cost a network round trip per row, which is invisible
// against a local database and ruinous when the leader and the database sit in
// different regions, and it held every row lock until the sweep committed, so an
// interactive virtual key save could block for the length of the entire sweep.
// Batching collapses the round trips; chunking bounds how long any row stays
// locked.
const dumpBatchSize = 500

// rateLimitDumpRow is one rate limit's persisted counter state for a dump write.
type rateLimitDumpRow struct {
	TokenUsageResetEpoch   int64
	RequestUsageResetEpoch int64
	ID                     string
	TokenCurrentUsage      int64
	TokenLastReset         time.Time
	RequestCurrentUsage    int64
	RequestLastReset       time.Time
}

// budgetDumpRow is one budget's persisted usage and override state for a dump write.
type budgetDumpRow struct {
	UsageResetEpoch         int64
	ID                      string
	CurrentUsage            float64
	LastReset               time.Time
	OverrideAmount          float64
	OverrideMode            configstoreTables.BudgetOverrideMode
	OverrideCyclesRemaining int
	OverrideCyclesTotal     int
	OverrideAnchorReset     *time.Time
}

// isDumpDeadlock reports whether a dump error is a database deadlock. In a
// multi-node setup that simply means another node wrote the same rows first;
// this node's usage is still held in memory and in the gossip baselines, so the
// next cycle persists it.
func isDumpDeadlock(err error) bool {
	errStr := err.Error()
	return strings.Contains(errStr, "deadlock") ||
		strings.Contains(errStr, "40P01") ||
		strings.Contains(errStr, "1213")
}

// supportsBatchedDump reports whether the dialect supports the
// UPDATE ... FROM (VALUES ...) form the batched writes use. Only PostgreSQL
// does here; every other dialect takes the per-row fallback.
func supportsBatchedDump(tx *gorm.DB) bool {
	return tx.Dialector.Name() == "postgres"
}

// dumpRateLimitBatch persists one batch of rate-limit counters, as a single
// multi-row statement on PostgreSQL and as per-row updates elsewhere.
func (gs *LocalGovernanceStore) dumpRateLimitBatch(ctx context.Context, tx *gorm.DB, batch []rateLimitDumpRow) error {
	if !supportsBatchedDump(tx) {
		for _, row := range batch {
			updates := make(map[string]interface{}, 6)
			for _, dimension := range []struct {
				prefix   string
				usage    int64
				boundary time.Time
				epoch    int64
			}{
				{"token", row.TokenCurrentUsage, row.TokenLastReset, row.TokenUsageResetEpoch},
				{"request", row.RequestCurrentUsage, row.RequestLastReset, row.RequestUsageResetEpoch},
			} {
				guard := dimension.prefix + "_last_reset <= ? AND " + dimension.prefix + "_usage_reset_epoch <= ?"
				for column, value := range map[string]interface{}{
					dimension.prefix + "_current_usage":     dimension.usage,
					dimension.prefix + "_last_reset":        dimension.boundary,
					dimension.prefix + "_usage_reset_epoch": dimension.epoch,
				} {
					updates[column] = gorm.Expr("CASE WHEN "+guard+" THEN ? ELSE "+column+" END", dimension.boundary, dimension.epoch, value)
				}
			}
			if err := tx.WithContext(ctx).Session(&gorm.Session{SkipHooks: true}).Model(&configstoreTables.TableRateLimit{}).
				Where("id = ?", row.ID).Updates(updates).Error; err != nil {
				return fmt.Errorf("failed to dump rate limit %s: %w", row.ID, err)
			}
		}
		return nil
	}
	var statement strings.Builder
	args := make([]any, 0, len(batch)*7)
	statement.WriteString("UPDATE " + configstoreTables.TableRateLimit{}.TableName() + " AS t SET ")
	for index, dimension := range []struct{ prefix, usage, boundary, epoch string }{
		{"token", "tcu", "tlr", "tue"}, {"request", "rcu", "rlr", "rue"},
	} {
		if index > 0 {
			statement.WriteString(", ")
		}
		guard := "t." + dimension.prefix + "_last_reset <= v." + dimension.boundary + " AND t." + dimension.prefix + "_usage_reset_epoch <= v." + dimension.epoch
		for columnIndex, column := range []struct{ suffix, value string }{
			{"current_usage", dimension.usage}, {"last_reset", dimension.boundary}, {"usage_reset_epoch", dimension.epoch},
		} {
			if columnIndex > 0 {
				statement.WriteString(", ")
			}
			name := dimension.prefix + "_" + column.suffix
			statement.WriteString(name + " = CASE WHEN " + guard + " THEN v." + column.value + " ELSE t." + name + " END")
		}
	}
	statement.WriteString(" FROM (VALUES ")
	for index, row := range batch {
		if index > 0 {
			statement.WriteString(",")
		}
		statement.WriteString("(?::varchar,?::bigint,?::timestamptz,?::bigint,?::timestamptz,?::bigint,?::bigint)")
		args = append(args, row.ID, row.TokenCurrentUsage, row.TokenLastReset, row.RequestCurrentUsage, row.RequestLastReset, row.TokenUsageResetEpoch, row.RequestUsageResetEpoch)
	}
	statement.WriteString(") AS v(id, tcu, tlr, rcu, rlr, tue, rue) WHERE t.id = v.id" +
		" AND ((t.token_last_reset <= v.tlr AND t.token_usage_reset_epoch <= v.tue AND (t.token_current_usage, t.token_last_reset, t.token_usage_reset_epoch) IS DISTINCT FROM (v.tcu, v.tlr, v.tue))" +
		" OR (t.request_last_reset <= v.rlr AND t.request_usage_reset_epoch <= v.rue AND (t.request_current_usage, t.request_last_reset, t.request_usage_reset_epoch) IS DISTINCT FROM (v.rcu, v.rlr, v.rue)))")
	if err := tx.WithContext(ctx).Exec(statement.String(), args...).Error; err != nil {
		return fmt.Errorf("failed to dump %d rate limits: %w", len(batch), err)
	}
	return nil
}

// writeBudgetBatch persists one batch of budget override state and usage counters.
//
// The override write runs first and is guarded on a strictly older last_reset,
// exactly as the per-row path was: it must only fire when this node performed a
// reset the database has not seen. The usage write advances last_reset and so
// would close that guard if it ran first.
//
// usageGuard is the comparison the usage write comes under and differs by
// caller. The dump passes "<=" because in steady state the persisted boundary
// equals the in-memory one, and "<" would match zero rows so usage would never
// persist at all. The reset path passes "<" so that its two statements share one
// guard: a snapshot that lost the override race must not advance last_reset on
// its own, which is exactly the split that lets a window's override cycle go
// unspent.
func (gs *LocalGovernanceStore) writeBudgetBatch(ctx context.Context, tx *gorm.DB, batch []budgetDumpRow, usageGuard string) error {
	if !supportsBatchedDump(tx) {
		for _, row := range batch {
			if err := tx.WithContext(ctx).
				Session(&gorm.Session{SkipHooks: true}).
				Model(&configstoreTables.TableBudget{}).
				Where("id = ? AND last_reset < ? AND usage_reset_epoch <= ?", row.ID, row.LastReset, row.UsageResetEpoch).
				Updates(map[string]interface{}{
					"override_amount":           row.OverrideAmount,
					"override_mode":             row.OverrideMode,
					"override_cycles_remaining": row.OverrideCyclesRemaining,
					"override_cycles_total":     row.OverrideCyclesTotal,
					"override_anchor_reset":     row.OverrideAnchorReset,
				}).Error; err != nil {
				return fmt.Errorf("failed to update budget override lifecycle %s: %w", row.ID, err)
			}
			if err := tx.WithContext(ctx).
				Session(&gorm.Session{SkipHooks: true}).
				Model(&configstoreTables.TableBudget{}).
				Where("id = ? AND last_reset "+usageGuard+" ? AND usage_reset_epoch <= ?", row.ID, row.LastReset, row.UsageResetEpoch).
				Updates(map[string]interface{}{
					"current_usage":     row.CurrentUsage,
					"last_reset":        row.LastReset,
					"usage_reset_epoch": row.UsageResetEpoch,
				}).Error; err != nil {
				return fmt.Errorf("failed to update budget %s: %w", row.ID, err)
			}
		}
		return nil
	}

	table := configstoreTables.TableBudget{}.TableName()

	var overrideSQL strings.Builder
	overrideArgs := make([]any, 0, len(batch)*8)
	overrideSQL.WriteString("UPDATE " + table + " AS t SET " +
		"override_amount = v.oa, override_mode = v.om, override_cycles_remaining = v.ocr, " +
		"override_cycles_total = v.oct, override_anchor_reset = v.oar FROM (VALUES ")
	for i, row := range batch {
		if i > 0 {
			overrideSQL.WriteString(",")
		}
		overrideSQL.WriteString("(?::varchar,?::double precision,?::varchar,?::integer,?::integer,?::timestamptz,?::timestamptz,?::bigint)")
		overrideArgs = append(overrideArgs, row.ID, row.OverrideAmount, string(row.OverrideMode),
			row.OverrideCyclesRemaining, row.OverrideCyclesTotal, row.OverrideAnchorReset, row.LastReset, row.UsageResetEpoch)
	}
	// IS DISTINCT FROM keeps an unchanged row from getting a new row version.
	overrideSQL.WriteString(") AS v(id, oa, om, ocr, oct, oar, lr, ure) WHERE t.id = v.id AND t.last_reset < v.lr AND t.usage_reset_epoch <= v.ure" +
		" AND (t.override_amount, t.override_mode, t.override_cycles_remaining, t.override_cycles_total, t.override_anchor_reset)" +
		" IS DISTINCT FROM (v.oa, v.om, v.ocr, v.oct, v.oar)")
	if err := tx.WithContext(ctx).Exec(overrideSQL.String(), overrideArgs...).Error; err != nil {
		return fmt.Errorf("failed to update budget override lifecycle for %d budgets: %w", len(batch), err)
	}

	var usageSQL strings.Builder
	usageArgs := make([]any, 0, len(batch)*4)
	usageSQL.WriteString("UPDATE " + table + " AS t SET current_usage = v.cu, last_reset = v.lr, usage_reset_epoch = v.ure FROM (VALUES ")
	for i, row := range batch {
		if i > 0 {
			usageSQL.WriteString(",")
		}
		usageSQL.WriteString("(?::varchar,?::double precision,?::timestamptz,?::bigint)")
		usageArgs = append(usageArgs, row.ID, row.CurrentUsage, row.LastReset, row.UsageResetEpoch)
	}
	usageSQL.WriteString(") AS v(id, cu, lr, ure) WHERE t.id = v.id AND t.last_reset " + usageGuard + " v.lr AND t.usage_reset_epoch <= v.ure" +
		" AND (t.current_usage, t.last_reset, t.usage_reset_epoch) IS DISTINCT FROM (v.cu, v.lr, v.ure)")
	if err := tx.WithContext(ctx).Exec(usageSQL.String(), usageArgs...).Error; err != nil {
		return fmt.Errorf("failed to update %d budgets: %w", len(batch), err)
	}
	return nil
}

// rateLimitBoundaryRow is one rate limit's new boundary for a single counter
// dimension. Token and request counters expire independently, so a reset batch
// is built per dimension rather than per row.
type rateLimitBoundaryRow struct {
	UsageResetEpoch int64
	ID              string
	Boundary        time.Time
}

// resetRateLimitDimensionBatch zeroes one counter dimension for a batch of rate
// limits and advances its boundary column.
//
// A strict boundary guard protects usage already dumped for this window or a
// newer window. The epoch guard protects newer reset evidence. Current usage
// cannot identify a reset because traffic may already have bumped the counter.
func (gs *LocalGovernanceStore) resetRateLimitDimensionBatch(ctx context.Context, tx *gorm.DB, usageColumn, boundaryColumn string, batch []rateLimitBoundaryRow) error {
	epochColumn := strings.TrimSuffix(usageColumn, "current_usage") + "usage_reset_epoch"
	if len(batch) == 0 {
		return nil
	}
	if !supportsBatchedDump(tx) {
		for _, row := range batch {
			if err := tx.WithContext(ctx).
				Session(&gorm.Session{SkipHooks: true}).
				Model(&configstoreTables.TableRateLimit{}).
				Where("id = ? AND "+boundaryColumn+" < ? AND "+epochColumn+" <= ?", row.ID, row.Boundary, row.UsageResetEpoch).
				Updates(map[string]interface{}{
					usageColumn:    0,
					boundaryColumn: row.Boundary,
					epochColumn:    row.UsageResetEpoch,
				}).Error; err != nil {
				return fmt.Errorf("failed to reset rate limit %s %s: %w", row.ID, boundaryColumn, err)
			}
		}
		return nil
	}

	var sb strings.Builder
	args := make([]any, 0, len(batch)*3)
	sb.WriteString("UPDATE " + configstoreTables.TableRateLimit{}.TableName() + " AS t SET " +
		usageColumn + " = 0, " + boundaryColumn + " = v.b, " + epochColumn + " = v.e FROM (VALUES ")
	for i, row := range batch {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(?::varchar,?::timestamptz,?::bigint)")
		args = append(args, row.ID, row.Boundary, row.UsageResetEpoch)
	}
	sb.WriteString(") AS v(id, b, e) WHERE t.id = v.id AND t." + boundaryColumn + " < v.b AND t." + epochColumn + " <= v.e")
	if err := tx.WithContext(ctx).Exec(sb.String(), args...).Error; err != nil {
		return fmt.Errorf("failed to reset %s for %d rate limits: %w", boundaryColumn, len(batch), err)
	}
	return nil
}

// GetRateLimitDumper captures rate-limit counters plus the supplied baselines and returns
// their database writer. Callers can release accounting locks before invoking
// the writer; capture itself does not perform database I/O.
func (gs *LocalGovernanceStore) GetRateLimitDumper(ctx context.Context, tokenBaselines map[string]int64, requestBaselines map[string]int64) func(context.Context) error {
	if gs.configStore == nil {
		return func(context.Context) error { return nil }
	}
	// This is to prevent nil pointer dereference
	if tokenBaselines == nil {
		tokenBaselines = map[string]int64{}
	}
	if requestBaselines == nil {
		requestBaselines = map[string]int64{}
	}
	// Range over ALL rate limits in memory (mirrors GetBudgetDumper pattern).
	// This covers rate limits from every source: virtual keys, model configs,
	// providers, teams, customers, AND access profiles — whose IDs were
	// previously missing, causing AP rate-limit usage to never reach the DB.
	var rateLimitUpdates []rateLimitDumpRow
	gs.rateLimits.Range(func(key, value interface{}) bool {
		rateLimit, ok := value.(*configstoreTables.TableRateLimit)
		if !ok || rateLimit == nil {
			return true
		}
		update := rateLimitDumpRow{
			ID:                     rateLimit.ID,
			TokenCurrentUsage:      rateLimit.TokenCurrentUsage,
			TokenLastReset:         rateLimit.TokenLastReset,
			TokenUsageResetEpoch:   rateLimit.TokenUsageResetEpoch,
			RequestUsageResetEpoch: rateLimit.RequestUsageResetEpoch,
			RequestCurrentUsage:    rateLimit.RequestCurrentUsage,
			RequestLastReset:       rateLimit.RequestLastReset,
		}
		if tokenBaseline, exists := tokenBaselines[rateLimit.ID]; exists {
			update.TokenCurrentUsage += tokenBaseline
		}
		if requestBaseline, exists := requestBaselines[rateLimit.ID]; exists {
			update.RequestCurrentUsage += requestBaseline
		}
		rateLimitUpdates = append(rateLimitUpdates, update)
		return true
	})
	sort.Slice(rateLimitUpdates, func(i, j int) bool {
		return rateLimitUpdates[i].ID < rateLimitUpdates[j].ID
	})
	return func(ctx context.Context) error {
		return gs.writeRateLimitRows(ctx, rateLimitUpdates)
	}
}

// writeRateLimitRows persists the captured rate-limit counters.
func (gs *LocalGovernanceStore) writeRateLimitRows(ctx context.Context, rateLimitUpdates []rateLimitDumpRow) error {
	// Save all updated rate limits to database using direct UPDATE to avoid overwriting config fields.
	// Written in batches so row locks are released between chunks rather than
	// held for the whole sweep, and so each chunk costs one round trip instead
	// of one per row.
	for start := 0; start < len(rateLimitUpdates); start += dumpBatchSize {
		end := min(start+dumpBatchSize, len(rateLimitUpdates))
		batch := rateLimitUpdates[start:end]
		if err := gs.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
			return gs.dumpRateLimitBatch(ctx, tx, batch)
		}); err != nil {
			if isDumpDeadlock(err) {
				// Another node wrote these rows first. Our usage is still in
				// memory and in the gossip baselines, so skip this chunk and let
				// the next cycle persist it.
				gs.logger.Debug("Rate limit dump chunk encountered deadlock (another node is updating) - will retry next cycle")
				continue
			}
			return fmt.Errorf("failed to dump rate limits to database: %w", err)
		}
	}
	return nil
}

// dumpBudgetGeneration includes operator resets, which preserve LastReset.
type dumpBudgetGeneration struct {
	reset    uint64
	boundary time.Time
	epoch    int64
}

// dumpRateGeneration tracks independently advancing token and request windows.
type dumpRateGeneration struct {
	tokens       time.Time
	requests     time.Time
	tokenEpoch   int64
	requestEpoch int64
}

// CaptureDumpResetGeneration returns a check for resets or calendar adoption
// since capture. Call it before reading remote baselines and check it after
// capturing local dump rows. Each baseline lock protects a single resource read;
// no lock spans a map scan, sort, or database operation. Ordinary charges do not
// invalidate the capture. Reset and adoption boundaries advance monotonically.
func (gs *LocalGovernanceStore) CaptureDumpResetGeneration() func() bool {
	budgets := make(map[string]dumpBudgetGeneration)
	gs.budgets.Range(func(key, _ any) bool {
		id := key.(string)
		if generation, ok := gs.dumpBudgetGeneration(id); ok {
			budgets[id] = generation
		}
		return true
	})
	rateLimitDeletions := gs.rateLimitDeletions.Load()
	rates := make(map[string]dumpRateGeneration)
	gs.rateLimits.Range(func(key, _ any) bool {
		id := key.(string)
		if generation, ok := gs.dumpRateGeneration(id); ok {
			rates[id] = generation
		}
		return true
	})
	return func() bool {
		for id, before := range budgets {
			if after, ok := gs.dumpBudgetGeneration(id); !ok || before != after {
				return false
			}
		}
		for id, before := range rates {
			if after, ok := gs.dumpRateGeneration(id); !ok || before != after {
				return false
			}
		}
		return rateLimitDeletions == gs.rateLimitDeletions.Load()
	}
}

// dumpBudgetGeneration reads the counter generation and boundary as one reset view.
func (gs *LocalGovernanceStore) dumpBudgetGeneration(id string) (dumpBudgetGeneration, bool) {
	gs.LastDBUsagesBudgetsMu.RLock()
	defer gs.LastDBUsagesBudgetsMu.RUnlock()
	budget := gs.LoadBudget(nil, id)
	if budget == nil {
		return dumpBudgetGeneration{}, false
	}
	return dumpBudgetGeneration{reset: gs.budgetResetGens[id], boundary: budget.LastReset, epoch: budget.UsageResetEpoch}, true
}

// dumpRateGeneration reads both windows under their reset locks, in writer order.
func (gs *LocalGovernanceStore) dumpRateGeneration(id string) (dumpRateGeneration, bool) {
	gs.LastDBUsagesRateLimitsRequestsMu.RLock()
	defer gs.LastDBUsagesRateLimitsRequestsMu.RUnlock()
	gs.LastDBUsagesRateLimitsTokensMu.RLock()
	defer gs.LastDBUsagesRateLimitsTokensMu.RUnlock()
	rate := gs.LoadRateLimit(nil, id)
	if rate == nil {
		return dumpRateGeneration{}, false
	}
	return dumpRateGeneration{
		tokens:       rate.TokenLastReset,
		requests:     rate.RequestLastReset,
		tokenEpoch:   rate.TokenUsageResetEpoch,
		requestEpoch: rate.RequestUsageResetEpoch,
	}, true
}

// GetBudgetDumper captures usage, baselines, and reset generations, then returns
// their database writer. Callers can release accounting locks before invoking
// the writer; capture itself does not perform database I/O.
func (gs *LocalGovernanceStore) GetBudgetDumper(ctx context.Context, baselines map[string]float64) func(context.Context) error {
	if gs.configStore == nil {
		return func(context.Context) error { return nil }
	}
	rows, gens := gs.snapshotBudgetRows(baselines)
	return func(ctx context.Context) error { return gs.writeBudgetRows(ctx, rows, gens) }
}

// snapshotBudgetRows captures every budget's current usage as dump rows, together
// with the reset generation each row was read at.
func (gs *LocalGovernanceStore) snapshotBudgetRows(baselines map[string]float64) ([]budgetDumpRow, map[string]uint64) {
	// This is to prevent nil pointer dereference
	if baselines == nil {
		baselines = map[string]float64{}
	}
	// Read the generations first. Taking them before the budgets means a reset that
	// races this snapshot is either fully visible in both, or shows up as a bumped
	// generation later; it can never look unchanged while the usage read was stale.
	gens := gs.budgetResetGensSnapshot()
	rows := make([]budgetDumpRow, 0)
	gs.budgets.Range(func(key, value interface{}) bool {
		_, keyOK := key.(string)
		budget, ok := value.(*configstoreTables.TableBudget)
		if !keyOK || !ok || budget == nil {
			return true
		}
		// Fold this node's view of remote usage in before writing so every node
		// persists the same cluster-wide total.
		newUsage := budget.CurrentUsage
		if baseline, exists := baselines[budget.ID]; exists {
			newUsage += baseline
		}
		rows = append(rows, budgetDumpRow{
			ID:                      budget.ID,
			CurrentUsage:            newUsage,
			LastReset:               budget.LastReset,
			UsageResetEpoch:         budget.UsageResetEpoch,
			OverrideAmount:          budget.OverrideAmount,
			OverrideMode:            budget.OverrideMode,
			OverrideCyclesRemaining: budget.OverrideCyclesRemaining,
			OverrideCyclesTotal:     budget.OverrideCyclesTotal,
			OverrideAnchorReset:     budget.OverrideAnchorReset,
		})
		return true
	})
	// Stable ID order keeps concurrent dumpers taking row locks in the same
	// sequence, which is what keeps them deadlock-free rather than merely lucky.
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, gens
}

// writeBudgetRows persists a snapshot taken by snapshotBudgetRows.
func (gs *LocalGovernanceStore) writeBudgetRows(ctx context.Context, rows []budgetDumpRow, gens map[string]uint64) error {
	if gs.configStore == nil {
		return nil
	}
	// Written in batches so row locks are released between chunks rather than
	// held for the whole sweep, and so each chunk costs one round trip instead
	// of two per row.
	for start := 0; start < len(rows); start += dumpBatchSize {
		end := min(start+dumpBatchSize, len(rows))
		batch := gs.keepUnresetRows(rows[start:end], gens)
		if len(batch) == 0 {
			continue
		}
		if err := gs.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
			return gs.writeBudgetBatch(ctx, tx, batch, "<=")
		}); err != nil {
			if isDumpDeadlock(err) {
				// Another node wrote these rows first. Our usage is still in
				// memory and in the gossip baselines, so skip this chunk and let
				// the next cycle persist it.
				gs.logger.Debug("Budget dump chunk encountered deadlock (another node is updating) - will retry next cycle")
				continue
			}
			return fmt.Errorf("failed to dump budgets to database: %w", err)
		}
	}
	return nil
}

// DATABASE METHODS

// limitIDsOf names the limits of several lists as one deduplicated list of identifiers, in the order
// first seen. A limit reached twice is one limit, and charging it twice for one request would bill
// the same budget twice.
func limitIDsOf(lists ...[]schemas.Limit) []string {
	ids := []string{}
	seen := map[string]bool{}
	for _, list := range lists {
		for _, limit := range list {
			if limit.ID == "" || seen[limit.ID] {
				continue
			}
			seen[limit.ID] = true
			ids = append(ids, limit.ID)
		}
	}
	return ids
}

// CollectModelScopedGovernanceIDs returns only the model-config-owned IDs for a
// (provider, model) pair, across the global and virtual-key scopes plus whatever
// scopes a registered resolver derives from the IDs (see RegisterExtraScopedIDsResolver).
//
// It exists for delayed settlement. A request's limits are settled while it is in
// flight, and a batch create carries no top-level model (each input-file row names
// its own), so collectModelConfigsFor matches only the all-models wildcards and
// never the exact-model configs. Those are precisely where
// a model-level budget lives (an access profile's per-model limits materialise as
// user-scoped configs naming one model and provider), so batch spend never reached
// them. Settlement does know the models, and calls this per model to charge what
// the create-time collection could not see.
//
// Callers must subtract the IDs already charged for the request: the wildcard tiers
// returned here overlap what was settled at create time by design, since both walk
// the same four tiers.
func (gs *LocalGovernanceStore) CollectModelScopedGovernanceIDs(ctx context.Context, virtualKeyID string, userID string, provider schemas.ModelProvider, model string) (budgetIDs []string, rateLimitIDs []string) {
	seenBudgets := map[string]bool{}
	seenRateLimits := map[string]bool{}

	var providerStr *string
	if provider != "" {
		p := string(provider)
		providerStr = &p
	}
	addModelConfigIDs := func(mc *registry.ModelConfig) {
		for _, id := range mc.BudgetIDs {
			if !seenBudgets[id] {
				budgetIDs = append(budgetIDs, id)
				seenBudgets[id] = true
			}
		}
		if mc.RateLimitID != "" && !seenRateLimits[mc.RateLimitID] {
			rateLimitIDs = append(rateLimitIDs, mc.RateLimitID)
			seenRateLimits[mc.RateLimitID] = true
		}
	}

	targets := gs.aliasTargets(ctx, providerStr, model)
	scopes := []struct{ name, id string }{
		{configstoreTables.ModelConfigScopeGlobal, ""},
		{configstoreTables.ModelConfigScopeVirtualKey, virtualKeyID},
	}
	for _, scope := range scopes {
		if scope.name != configstoreTables.ModelConfigScopeGlobal && scope.id == "" {
			continue
		}
		for _, mc := range gs.collectModelConfigsFor(ctx, scope.name, scope.id, model, providerStr, targets) {
			addModelConfigIDs(mc)
		}
	}
	// Downstream-registered scopes (e.g. enterprise's per-access-profile per-model
	// budgets, keyed by a user-access-profile association ID rather than the raw
	// user/VK ID) — see RegisterExtraScopedIDsResolver.
	for _, pair := range collectExtraScopedIDs(ctx, virtualKeyID, userID) {
		for _, mc := range gs.collectModelConfigsFor(ctx, pair.Scope, pair.ScopeID, model, providerStr, targets) {
			addModelConfigIDs(mc)
		}
	}
	return budgetIDs, rateLimitIDs
}

// ScopedID names a (scope, scope_id) pair for a model-config lookup, and the holder kind its
// per-model limits are attributed to when the scope is enforced by name (see ScopedModelLimits).
// Left empty by a caller that only wants CollectModelScopedGovernanceIDs's budget/rate-limit IDs,
// since that path never attributes a limit to a holder.
type ScopedID struct {
	Scope   string
	ScopeID string
	Kind    grant.LimitHolderKind
}

// ExtraScopedIDsResolver returns additional (scope, scope_id) pairs to check for
// model-config-based budgets/rate-limits on a request, given its virtual key and
// user IDs. Lets downstream consumers (e.g. enterprise access profiles) graft
// extra scopes onto CollectModelScopedGovernanceIDs's built-in global/
// virtual_key set without this package needing to know their scope semantics.
// Must be fast and non-blocking (in-memory only) — called on every request.
//
// modelConfigScopesFor does not consult these resolvers: a caller wanting one of these scopes
// enforced passes it to ScopedModelLimits / ProviderScopedModelLimitsInScope, where the ScopedID's
// Kind is what a refusal names as the holder of the limit that ran out — see ScopedID.
type ExtraScopedIDsResolver func(ctx context.Context, virtualKeyID, userID string) []ScopedID

var (
	extraScopedIDsResolversMu sync.RWMutex
	extraScopedIDsResolvers   []ExtraScopedIDsResolver
)

// RegisterExtraScopedIDsResolver adds fn to the resolvers consulted by
// CollectModelScopedGovernanceIDs. Intended to be called once at process
// startup; safe to call concurrently.
func RegisterExtraScopedIDsResolver(fn ExtraScopedIDsResolver) {
	if fn == nil {
		return
	}
	extraScopedIDsResolversMu.Lock()
	extraScopedIDsResolvers = append(extraScopedIDsResolvers, fn)
	extraScopedIDsResolversMu.Unlock()
}

// collectExtraScopedIDs asks every registered resolver for the extra model-config scopes of a key
// and user, in registration order.
func collectExtraScopedIDs(ctx context.Context, virtualKeyID, userID string) []ScopedID {
	extraScopedIDsResolversMu.RLock()
	resolvers := extraScopedIDsResolvers
	extraScopedIDsResolversMu.RUnlock()
	if len(resolvers) == 0 {
		return nil
	}
	var out []ScopedID
	for _, fn := range resolvers {
		out = append(out, fn(ctx, virtualKeyID, userID)...)
	}
	return out
}

// PUBLIC API METHODS

// GetUserGovernance retrieves user governance data by user ID (enterprise-only, lock-free)
func (gs *LocalGovernanceStore) GetUserGovernance(ctx context.Context, userID string) (*UserGovernance, bool) {
	// User governance is part of enterprise
	return nil, false
}

// CreateUserGovernanceInMemory adds user governance data to the in-memory store (enterprise-only)
func (gs *LocalGovernanceStore) CreateUserGovernanceInMemory(ctx context.Context, userID string, budget *configstoreTables.TableBudget, rateLimit *configstoreTables.TableRateLimit) {
	// NoOp
	// Available in enterprise
}

// CreateUserNameInMemory records a user's display name; this store keeps no users, so it does nothing.
func (gs *LocalGovernanceStore) CreateUserNameInMemory(ctx context.Context, userID string, userName string) {
	// NoOp
	// Available in enterprise
}

// UpdateUserGovernanceInMemory updates user governance data in the in-memory store (enterprise-only)
func (gs *LocalGovernanceStore) UpdateUserGovernanceInMemory(ctx context.Context, userID string, budget *configstoreTables.TableBudget, rateLimit *configstoreTables.TableRateLimit) {
	// NoOp
	// Available in enterprise
}

// DeleteUserGovernanceInMemory removes user governance data from the in-memory store (enterprise-only)
func (gs *LocalGovernanceStore) DeleteUserGovernanceInMemory(ctx context.Context, userID string) {
	// NoOp
	// Available in enterprise
}

// ScopedModelConfigs returns the model configs of one scope and scope ID, in ID order. The records
// are shared and must be treated as read-only.
func (gs *LocalGovernanceStore) ScopedModelConfigs(scope, scopeID string) []*registry.ModelConfig {
	return gs.live.Collections.ModelConfigs.List(registry.ModelConfigByScope, registry.ModelConfigScopeKey(scope, scopeID))
}

// Helper functions

// GetBudgetAndRateLimitStatus implements GovernanceStore. It gathers what the request would answer to
// for the pair, from the store and the access on its grant, and reports the fullest budget and rate
// limit among them. A request with no access still answers to the deployment's own limits.
// GatherLimits assembles every limit an attempt answers to: the deployment's own for the pair; for
// every permit the request carries, what funds the holder and the holder's own model limits; and
// for every permit that pays for the pair, what funds its use of the provider.
//
// A holder's own limits bind whatever the request does, whichever permit admits the pair: a key
// whose budget is spent is spent, however the request reached the provider. Only what is funded
// per provider follows the pair, and which permits pay for it is the access's to say (see
// PermitsForModel).
//
// The deployment's own come first, because that is the order refusals report in: what the
// deployment imposes before what the holder is funded by. Every carried permit is gathered: this
// store resolves one permit per credential, so there is nothing to pick between, and one limit
// reached twice is still one limit once the list is settled.
func (gs *LocalGovernanceStore) GatherLimits(ctx *schemas.BifrostContext, access schemas.Access, provider schemas.ModelProvider, model string) (budgets []schemas.Limit, rateLimits []schemas.Limit, err error) {
	// A request naming no provider and no model (a relay call, an MCP tool execution) still walks
	// the model-config scopes: only the all-models, all-providers tier can match then, and that
	// tier is where a key's own budget and rate limit live since the model-config migration.
	// Provider- and model-specific tiers cannot match an empty pair, so they never reach it.
	budgets, rateLimits = gs.GlobalProviderLimits(ctx, provider)
	targets := gs.aliasTargets(ctx, providerPtrFor(provider), model)
	// Recorded for limitsForKeyThatRan, but not by its own charge-time walk: the record is what the
	// check answered to.
	if len(targets) > 0 && ctx != nil && ctx.Value(chargedKeyContext) == nil {
		ctx.SetValue(aliasLimitTargetsContext, settledAliasTargets{provider: provider, model: model, targets: targets})
	}
	globalModelBudgets, globalModelRateLimits := gs.globalModelLimits(ctx, provider, model, targets)
	budgets = append(budgets, globalModelBudgets...)
	rateLimits = append(rateLimits, globalModelRateLimits...)
	if access == nil {
		return budgets, rateLimits, nil
	}
	carried := access.Bases()
	if scoping := access.Scoping(); scoping != nil {
		carried = append(carried, scoping)
	}
	for _, permit := range carried {
		heldBudgets, heldRateLimits := gs.HolderLimits(ctx, permit)
		budgets = append(budgets, heldBudgets...)
		rateLimits = append(rateLimits, heldRateLimits...)

		// The holder's own model limits, gathered per permit because they are keyed by the
		// permit's identity.
		modelBudgets, modelRateLimits := gs.permitModelLimits(ctx, permit, provider, model, targets)
		budgets = append(budgets, modelBudgets...)
		rateLimits = append(rateLimits, modelRateLimits...)
	}
	for _, permit := range access.PermitsForModel(string(provider), model) {
		providerBudgets, providerRateLimits := gs.PermitProviderLimits(ctx, permit, provider)
		budgets = append(budgets, providerBudgets...)
		rateLimits = append(rateLimits, providerRateLimits...)
	}
	return budgets, rateLimits, nil
}

func (gs *LocalGovernanceStore) GetBudgetAndRateLimitStatus(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, budgetBaselines map[string]float64, tokenBaselines map[string]int64, requestBaselines map[string]int64) *BudgetAndRateLimitStatus {
	budgets, rateLimits, _ := gs.GatherLimits(ctx, ctx.Grant().Access(), provider, model)
	status := &BudgetAndRateLimitStatus{}

	for _, limit := range budgets {
		budget := gs.LoadBudget(ctx, limit.ID)
		if budget == nil {
			continue
		}
		maxLimit := budget.EffectiveMaxLimit()
		if maxLimit <= 0 {
			continue
		}
		if percent := (budget.CurrentUsage + budgetBaselines[budget.ID]) / maxLimit * 100; percent > status.BudgetPercentUsed {
			status.BudgetPercentUsed = percent
		}
	}

	for _, limit := range rateLimits {
		rateLimit := gs.LoadRateLimit(ctx, limit.ID)
		if rateLimit == nil {
			continue
		}
		if rateLimit.TokenMaxLimit != nil && *rateLimit.TokenMaxLimit > 0 {
			used := float64(rateLimit.TokenCurrentUsage+tokenBaselines[rateLimit.ID]) / float64(*rateLimit.TokenMaxLimit) * 100
			if used > status.RateLimitTokenPercentUsed {
				status.RateLimitTokenPercentUsed = used
			}
		}
		if rateLimit.RequestMaxLimit != nil && *rateLimit.RequestMaxLimit > 0 {
			used := float64(rateLimit.RequestCurrentUsage+requestBaselines[rateLimit.ID]) / float64(*rateLimit.RequestMaxLimit) * 100
			if used > status.RateLimitRequestPercentUsed {
				status.RateLimitRequestPercentUsed = used
			}
		}
	}

	return status
}

// GlobalProviderLimits reports the deployment's own limits on a provider: unscoped to any holder,
// the way ModelConfigScopeGlobal names the model configs nobody owns. Every request to the provider
// counts against them, whatever granted it, and they are the only limits besides a permit's own
// provider configs that can tell one provider apart from another, which is what makes them the pair
// load balancing asks about.
//
// Exported alongside the other gather primitives so a store that answers ResolvePermits from
// something other than a key can ask the same narrow question over its own baseline-aware checks,
// which this package's cannot answer for it. GlobalModelLimits answers with every model-config
// tier, including the ones that cover every provider, and those cannot tell one candidate from
// another; the model-config limits that can are ProviderScopedModelLimits's to report.
func (gs *LocalGovernanceStore) GlobalProviderLimits(ctx context.Context, provider schemas.ModelProvider) (budgets []schemas.Limit, rateLimits []schemas.Limit) {
	providerName := string(provider)
	p, ok := gs.live.Collections.Providers.Get(providerName)
	if !ok {
		return nil, nil
	}
	return grant.LimitsHeldBy(grant.LimitHolderProvider, providerName, providerName, providerName, "", idList(p.BudgetID)...),
		grant.LimitsHeldBy(grant.LimitHolderProvider, providerName, providerName, providerName, "", idList(p.RateLimitID)...)
}

// ProviderScopedModelLimits reports the model-config limits that can tell one
// load-balancing candidate from another for this model: the configs scoped to the candidate's
// provider, exact pair and all-models-on-the-provider, in the scope the permit selects (the
// deployment's own for nil, the holder's otherwise).
//
// The provider-less tiers are deliberately absent, and this method rather than GlobalModelLimits
// or PermitModelLimits is what routing asks so they stay absent: a limit covering every provider
// answers the same for every candidate, so it can only exclude all of them at once, which is a
// refusal for the funnel to state with a reason rather than load balancing quietly running out of
// options.
func (gs *LocalGovernanceStore) ProviderScopedModelLimits(ctx context.Context, permit schemas.Permit, provider schemas.ModelProvider, model string) (budgets []schemas.Limit, rateLimits []schemas.Limit) {
	return gs.providerScopedModelLimitsInScopes(ctx, modelConfigScopesFor(permit), provider, model, gs.aliasTargets(ctx, providerPtrFor(provider), model))
}

// ProviderScopedModelLimitsInScope is ProviderScopedModelLimits over one named scope, attributed to
// the given holder kind: the routing-side counterpart of ScopedModelLimits, and there for the same
// reason. A store layered on this one may impose per-model limits on the caller themselves, under
// a scope no permit stands for, and a provider-scoped one of those prices the caller out of that
// provider exactly as a permit's would. Load balancing has to ask about it the same way, or it
// routes to a provider the funnel then refuses.
func (gs *LocalGovernanceStore) ProviderScopedModelLimitsInScope(ctx context.Context, scope, scopeID string, kind grant.LimitHolderKind, provider schemas.ModelProvider, model string) (budgets []schemas.Limit, rateLimits []schemas.Limit) {
	if scope == "" || scopeID == "" {
		return nil, nil
	}
	return gs.providerScopedModelLimitsInScopes(ctx, []limitScope{{name: scope, id: scopeID, kind: kind}}, provider, model, gs.aliasTargets(ctx, providerPtrFor(provider), model))
}

// providerScopedModelLimitsInScopes is modelLimitsInScopes narrowed to the configs that name the
// provider. The walk is collectModelConfigsFor's, filtered rather than reimplemented, so which
// configs cover a pair has exactly one answer and routing's subset of it cannot drift from the
// funnel's.
func (gs *LocalGovernanceStore) providerScopedModelLimitsInScopes(ctx context.Context, scopes []limitScope, provider schemas.ModelProvider, model string, targets []string) (budgets []schemas.Limit, rateLimits []schemas.Limit) {
	providerName := string(provider)
	for _, scope := range scopes {
		for _, mc := range gs.collectModelConfigsFor(ctx, scope.name, scope.id, model, &providerName, targets) {
			if mc == nil || mc.Provider == nil {
				continue
			}
			budgets = append(budgets, grant.LimitsHeldBy(scope.kind, mc.ID, modelConfigDisplayName(mc), providerName, model, mc.BudgetIDs...)...)
			rateLimits = append(rateLimits, grant.LimitsHeldBy(scope.kind, mc.ID, modelConfigDisplayName(mc), providerName, model, idList(mc.RateLimitID)...)...)
		}
	}
	return budgets, rateLimits
}

// modelLimitsInScopes names the model-config limits covering the pair in the given scopes.
// collectModelConfigsFor walks the four tiers, most specific first: exact pair, exact model on any
// provider, any model on this provider, any model anywhere.
//
// A request with no model still walks the scopes: only the wildcard tiers can match then, and an
// all-models budget covers a request whatever it runs. Batch creation is the case that carries no
// top-level model, and skipping the walk there left the unscoped budget a key was created with
// unchecked and never bumped.
func (gs *LocalGovernanceStore) modelLimitsInScopes(ctx context.Context, scopes []limitScope, provider schemas.ModelProvider, model string, targets []string) (budgets []schemas.Limit, rateLimits []schemas.Limit) {
	providerName := string(provider)
	var providerArg *string
	if providerName != "" {
		providerArg = &providerName
	}
	for _, scope := range scopes {
		for _, mc := range gs.collectModelConfigsFor(ctx, scope.name, scope.id, model, providerArg, targets) {
			if mc == nil {
				continue
			}
			budgets = append(budgets, grant.LimitsHeldBy(scope.kind, mc.ID, modelConfigDisplayName(mc), providerName, model, mc.BudgetIDs...)...)
			rateLimits = append(rateLimits, grant.LimitsHeldBy(scope.kind, mc.ID, modelConfigDisplayName(mc), providerName, model, idList(mc.RateLimitID)...)...)
		}
	}
	return budgets, rateLimits
}

// GlobalModelLimits reports the deployment's own model-config limits covering the pair.
//
// Resolved per attempt rather than carried on the grant. A grant is built before a provider is
// chosen, and an attempt can fail over to a different provider or have its model rewritten, so
// which configs apply is a fact about the attempt. Resolving them here costs four keyed lookups
// per scope instead of a scan over every model config the deployment has. The caller gathers these
// into the attempt's settled limits rather than storing them anywhere longer-lived, so one
// attempt's limits are never charged to the next.
func (gs *LocalGovernanceStore) GlobalModelLimits(ctx context.Context, provider schemas.ModelProvider, model string) ([]schemas.Limit, []schemas.Limit) {
	return gs.globalModelLimits(ctx, provider, model, gs.aliasTargets(ctx, providerPtrFor(provider), model))
}

// globalModelLimits is GlobalModelLimits with the alias targets already resolved, so a caller
// gathering several scopes for one attempt resolves them once.
func (gs *LocalGovernanceStore) globalModelLimits(ctx context.Context, provider schemas.ModelProvider, model string, targets []string) ([]schemas.Limit, []schemas.Limit) {
	return gs.modelLimitsInScopes(ctx, modelConfigScopesFor(nil), provider, model, targets)
}

// providerPtrFor is the provider argument of a model-config lookup: nil when no provider is named.
func providerPtrFor(provider schemas.ModelProvider) *string {
	if provider == "" {
		return nil
	}
	name := string(provider)
	return &name
}

// PermitModelLimits reports the permit holder's own model-config limits covering the pair, in the
// scope the permit's identity selects and no other: the deployment's rows are GlobalModelLimits's
// to report, so a caller composing several holders never gathers them once per holder. Per attempt
// for the reasons on GlobalModelLimits.
func (gs *LocalGovernanceStore) PermitModelLimits(ctx context.Context, permit schemas.Permit, provider schemas.ModelProvider, model string) ([]schemas.Limit, []schemas.Limit) {
	if permit == nil || permit.ID() == "" {
		return nil, nil
	}
	return gs.permitModelLimits(ctx, permit, provider, model, gs.aliasTargets(ctx, providerPtrFor(provider), model))
}

// permitModelLimits is PermitModelLimits with the alias targets already resolved.
func (gs *LocalGovernanceStore) permitModelLimits(ctx context.Context, permit schemas.Permit, provider schemas.ModelProvider, model string, targets []string) ([]schemas.Limit, []schemas.Limit) {
	if permit == nil || permit.ID() == "" {
		// A permit selects the scope, so no permit selects nothing: falling through would answer
		// with the deployment's rows under a method that promised one holder's.
		return nil, nil
	}
	return gs.modelLimitsInScopes(ctx, modelConfigScopesFor(permit), provider, model, targets)
}

// ScopedModelLimits reports the model-config limits covering the pair in one named scope, attributed
// to the given holder kind.
//
// The scope is named directly rather than derived from a permit, because a scope can belong to an
// identity no permit stands for: a store layered on this one may impose per-model limits on the
// caller themselves, whose rows are keyed by a scope this package has no permit type for. What
// covers a pair stays modelLimitsInScopes's single answer.
func (gs *LocalGovernanceStore) ScopedModelLimits(ctx context.Context, scope, scopeID string, kind grant.LimitHolderKind, provider schemas.ModelProvider, model string) ([]schemas.Limit, []schemas.Limit) {
	if scope == "" || scopeID == "" {
		return nil, nil
	}
	return gs.modelLimitsInScopes(ctx, []limitScope{{name: scope, id: scopeID, kind: kind}}, provider, model, gs.aliasTargets(ctx, providerPtrFor(provider), model))
}

// limitScope is one model-config scope to resolve limits from, with the holder kind its limits are
// attributed to.
type limitScope struct {
	name string
	id   string
	kind grant.LimitHolderKind
}

// modelConfigScopesFor lists the model-config scopes one permit selects: the deployment's global
// scope for a nil permit, and the permit holder's own scope otherwise.
//
// The holder's scope is keyed by the permit's identity rather than by a virtual key, so a permit
// from any other kind of holder is looked up the same way without this needing to know what it is.
// One permit, one holder's scope: whoever gathers an attempt's limits asks once per permit it means
// to charge, which is what lets it charge one holder among several without collecting the others.
func modelConfigScopesFor(permit schemas.Permit) []limitScope {
	if permit == nil || permit.ID() == "" {
		return []limitScope{{
			name: configstoreTables.ModelConfigScopeGlobal,
			kind: grant.LimitHolderModelConfig,
		}}
	}
	return []limitScope{{
		name: modelConfigScopeFor(permit.Type()),
		id:   permit.ID(),
		kind: scopedModelConfigKind(permit.Type()),
	}}
}

// modelConfigScopeFor is the scope a permit holder's own model configs are stored under.
//
// A permit type and a model-config scope are different vocabularies: the scope is a persisted
// column, the type names what resolved the permit, and the two spell several of them differently.
// The translation is explicit rather than a cast, because casting one to the other silently finds
// nothing: the lookup is keyed by scope name, so a near-miss reads as "this holder configured no
// model limits" rather than as an error.
//
// A type with no case falls back to its own name, which is a scope nothing is stored under - the
// same "no per-model limits" answer, rather than another holder's rows.
func modelConfigScopeFor(permitType string) string {
	switch grant.PermitType(permitType) {
	case grant.PermitVirtualKey:
		return configstoreTables.ModelConfigScopeVirtualKey
	case grant.PermitProject:
		return configstoreTables.ModelConfigScopeProject
	case grant.PermitAccessProfile:
		return configstoreTables.ModelConfigScopeAccessProfile
	case grant.PermitTeamAccessProfile, grant.PermitBusinessUnitAccessProfile, grant.PermitCustomerAccessProfile:
		// One scope for all three: an attachment's rows are keyed by the attachment's own id, and the
		// kind below is what tells a refusal whose profile it was.
		return configstoreTables.ModelConfigScopeEntityAccessProfile
	default:
		return permitType
	}
}

// scopedModelConfigKind is the kind a permit holder's own per-model limits are attributed to, so a
// refusal can say whose model limit ran out. A type with no case is attributed to the generic
// model-config holder rather than to a particular one: naming another holder's kind would put one
// holder's name on a refusal that came from somewhere else.
func scopedModelConfigKind(permitType string) grant.LimitHolderKind {
	switch grant.PermitType(permitType) {
	case grant.PermitVirtualKey:
		return grant.LimitHolderVirtualKeyModelConfig
	case grant.PermitProject:
		return grant.LimitHolderProjectModelConfig
	case grant.PermitAccessProfile:
		return grant.LimitHolderUserAccessProfileModelConfig
	case grant.PermitTeamAccessProfile:
		return grant.LimitHolderTeamAccessProfileModelConfig
	case grant.PermitBusinessUnitAccessProfile:
		return grant.LimitHolderBusinessUnitAccessProfileModelConfig
	case grant.PermitCustomerAccessProfile:
		return grant.LimitHolderCustomerAccessProfileModelConfig
	default:
		return grant.LimitHolderModelConfig
	}
}

// modelConfigDisplayName is what a refusal calls a model config's limit.
func modelConfigDisplayName(mc *registry.ModelConfig) string {
	if mc.Provider != nil && *mc.Provider != "" {
		return fmt.Sprintf("%s on %s", mc.ModelName, *mc.Provider)
	}
	return mc.ModelName
}

// CheckBudgets reports whether any of the given budgets is exhausted.
//
// It is the only budget check: what to check is decided by whoever assembled the limits (an attempt's
// resolved set, filtered to the holder kinds in force), and this evaluates whatever it is handed. That
// is what lets a caller stop knowing whether a virtual key, a team, an access profile or a model
// config is paying.
func (gs *LocalGovernanceStore) CheckBudgets(ctx context.Context, limits []schemas.Limit, baselines map[string]float64) (Decision, error) {
	if len(limits) == 0 {
		return DecisionAllow, nil
	}
	entityWise := make(EntityWiseBudgets, len(limits))
	for _, limit := range limits {
		budget := gs.LoadBudget(ctx, limit.ID)
		if budget == nil {
			continue
		}
		label := limitLabel(limit)
		entityWise[label] = append(entityWise[label], budget)
	}
	if len(entityWise) == 0 {
		return DecisionAllow, nil
	}
	return gs.CheckBudget(ctx, entityWise, baselines)
}

// CheckRateLimits is CheckBudgets for rate limits.
func (gs *LocalGovernanceStore) CheckRateLimits(ctx context.Context, limits []schemas.Limit, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error) {
	if len(limits) == 0 {
		return DecisionAllow, nil
	}
	entityWise := make(EntityWiseRateLimits, len(limits))
	for _, limit := range limits {
		rateLimit := gs.LoadRateLimit(ctx, limit.ID)
		if rateLimit == nil {
			continue
		}
		label := limitLabel(limit)
		entityWise[label] = append(entityWise[label], rateLimit)
	}
	if len(entityWise) == 0 {
		return DecisionAllow, nil
	}
	return gs.CheckRateLimit(ctx, entityWise, tokensBaselines, requestsBaselines)
}

// ChargeBudgets bills a cost to every budget it is handed. The list is the one the check read, so a
// request is billed to exactly what it was checked against.
func (gs *LocalGovernanceStore) ChargeBudgets(ctx context.Context, limits []schemas.Limit, cost float64) error {
	for _, budgetID := range limitIDsOf(limits) {
		if err := gs.BumpBudgetUsage(ctx, budgetID, cost); err != nil {
			return err
		}
	}
	return nil
}

// ChargeRateLimits counts a request against every rate limit it is handed, for the same reason as
// ChargeBudgets.
func (gs *LocalGovernanceStore) ChargeRateLimits(ctx context.Context, limits []schemas.Limit, tokensUsed int64, shouldUpdateTokens, shouldUpdateRequests bool) error {
	for _, rateLimitID := range limitIDsOf(limits) {
		if err := gs.BumpRateLimitUsage(ctx, rateLimitID, tokensUsed, shouldUpdateTokens, shouldUpdateRequests); err != nil {
			return err
		}
	}
	return nil
}

// limitLabel names a limit's holder for the log lines and refusals the check emits. It is what the
// per-holder checks used to hardcode, now read off the limit itself.
func limitLabel(limit schemas.Limit) string {
	if limit.HolderName != "" {
		return fmt.Sprintf("%s %s", limit.HolderKind, limit.HolderName)
	}
	return string(limit.HolderKind)
}
