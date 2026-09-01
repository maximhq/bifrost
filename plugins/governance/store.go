// Package governance provides the in-memory cache store for fast governance data access
package governance

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"gorm.io/gorm"
)

type (
	EntityWiseBudgets    map[string][]*configstoreTables.TableBudget
	EntityWiseRateLimits map[string][]*configstoreTables.TableRateLimit
)

// LocalGovernanceStore provides in-memory cache for governance data with fast, non-blocking access
type LocalGovernanceStore struct {
	// inMemoryStore supplies the MCP clients open to every virtual key when building the
	// grant a key carries. Optional: without it, no client is treated as open.
	inMemoryStore InMemoryStore
	// Core data maps using sync.Map for lock-free reads
	// virtualKeys is keyed by VK value; during the rotation grace period a VK is
	// registered under two keys (current + previous value) pointing at the same
	// object. GetVirtualKey validates which key matched and lazily removes
	// expired/stale value keys (a plain Delete on the value key only - the ID
	// index must survive such cleanup).
	virtualKeys sync.Map // string -> *VirtualKey (VK value -> VirtualKey with preloaded relationships)
	// virtualKeysByID is a secondary index over virtualKeys keyed by VK row ID,
	// giving O(1) by-ID lookups (e.g. the /mcp JWT auth path) without an O(n)
	// scan or a database read. Maintained in lock-step with virtualKeys via
	// storeVirtualKey / deleteVirtualKeyByValue — never write it directly.
	virtualKeysByID sync.Map // string -> *VirtualKey (VK row ID -> VirtualKey)
	teams           sync.Map // string -> *Team (Team ID -> Team)
	customers       sync.Map // string -> *Customer (Customer ID -> Customer)
	budgets         sync.Map // string -> *Budget (Budget ID -> Budget)
	rateLimits      sync.Map // string -> *RateLimit (RateLimit ID -> RateLimit)
	modelConfigs    sync.Map // string -> *ModelConfig (key: "modelName" or "modelName:provider" -> ModelConfig)
	providers       sync.Map // string -> *Provider (Provider name -> Provider with preloaded relationships)

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

// BusinessUnitGovernance holds in-memory budget and rate limit data for a business unit
type BusinessUnitGovernance struct {
	BudgetID    *string
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
	GetGovernanceData(ctx context.Context) *GovernanceData
	GetGovernanceUsageData(ctx context.Context) *GovernanceData
	GetVirtualKey(ctx context.Context, vkValue string) (*configstoreTables.TableVirtualKey, bool)
	GetVirtualKeyByID(ctx context.Context, vkID string) (*configstoreTables.TableVirtualKey, bool)
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
	ResolvePermits(ctx *schemas.BifrostContext) (bases []schemas.Permit, scoping schemas.Permit, mode grant.CompositionMode)
	// ProviderAndModelLimits reports the deployment's own limits on a provider plus whichever model
	// configs cover the pair, for the deployment and for the permit's holder. Resolved per attempt
	// because a request can fail over to another provider or have its model rewritten, so they are
	// not facts about the permit.
	ProviderAndModelLimits(ctx context.Context, permit schemas.Permit, provider schemas.ModelProvider, model string) (budgets []schemas.Limit, rateLimits []schemas.Limit)
	// HolderLimits reports what funds a permit's holder whichever provider serves the request: for a
	// virtual key, its own limits plus its team's and customer's. Not on the permit because these
	// cannot tell one provider from another, so load balancing has no use for them and asking once
	// per candidate answers the same question N times.
	//
	// This is the seam a deployment reimplements to fund requests from something other than a key.
	// Nothing downstream asks what kind of holder answered.
	HolderLimits(ctx context.Context, permit schemas.Permit) (budgets []schemas.Limit, rateLimits []schemas.Limit)
	// ProviderLimits reports what funds a permit's use of one provider: the limits configured against
	// that provider on the holder, and no other. These are the limits that can tell one provider
	// from another, which is what load balancing asks about, and they are gathered again with the
	// rest once the attempt's provider is settled. A permit describes what may be reached; what that
	// costs is the store's to know, keyed by the permit's identity.
	ProviderLimits(ctx context.Context, permit schemas.Permit, provider schemas.ModelProvider) (budgets []schemas.Limit, rateLimits []schemas.Limit)
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
	ResetExpiredRateLimitsInMemory(ctx context.Context, refreshReferences bool, rateLimitIDs ...string) []*configstoreTables.TableRateLimit
	ResetExpiredBudgetsInMemory(ctx context.Context, refreshReferences bool, budgetIDs ...string) []*configstoreTables.TableBudget
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
	ResetExpiredRateLimits(ctx context.Context, resetRateLimits []*configstoreTables.TableRateLimit) error
	ResetExpiredBudgets(ctx context.Context, resetBudgets []*configstoreTables.TableBudget) error
	// Provider and model-level usage updates (combined)
	// Dump operations
	DumpRateLimits(ctx context.Context, tokenBaselines map[string]int64, requestBaselines map[string]int64) error
	DumpBudgets(ctx context.Context, baselines map[string]float64) error
	// In-memory CRUD operations
	CreateVirtualKeyInMemory(ctx context.Context, vk *configstoreTables.TableVirtualKey)
	UpdateVirtualKeyInMemory(ctx context.Context, vk *configstoreTables.TableVirtualKey, budgetBaselines map[string]float64, rateLimitTokensBaselines map[string]int64, rateLimitRequestsBaselines map[string]int64)
	DeleteVirtualKeyInMemory(ctx context.Context, vkID string)
	CreateTeamInMemory(ctx context.Context, team *configstoreTables.TableTeam)
	UpdateTeamInMemory(ctx context.Context, team *configstoreTables.TableTeam, budgetBaselines map[string]float64)
	DeleteTeamInMemory(ctx context.Context, teamID string)
	// Customer information
	CreateCustomerInMemory(ctx context.Context, customer *configstoreTables.TableCustomer)
	UpdateCustomerInMemory(ctx context.Context, customer *configstoreTables.TableCustomer, budgetBaselines map[string]float64)
	DeleteCustomerInMemory(ctx context.Context, customerID string)
	// User governance in-memory operations (enterprise-only, but interface defined here for compatibility)
	GetUserGovernance(ctx context.Context, userID string) (*UserGovernance, bool)
	CreateUserGovernanceInMemory(ctx context.Context, userID string, budget *configstoreTables.TableBudget, rateLimit *configstoreTables.TableRateLimit)
	UpdateUserGovernanceInMemory(ctx context.Context, userID string, budget *configstoreTables.TableBudget, rateLimit *configstoreTables.TableRateLimit)
	DeleteUserGovernanceInMemory(ctx context.Context, userID string)
	CreateUserNameInMemory(ctx context.Context, userID string, userName string)
	// Model config in-memory operations
	UpdateModelConfigInMemory(ctx context.Context, mc *configstoreTables.TableModelConfig) *configstoreTables.TableModelConfig
	DeleteModelConfigInMemory(ctx context.Context, mcID string)
	ScopedModelConfigIDs(scope, scopeID string) []string
	// Provider in-memory operations
	UpdateProviderInMemory(ctx context.Context, provider *configstoreTables.TableProvider) *configstoreTables.TableProvider
	DeleteProviderInMemory(ctx context.Context, providerName string)
	// Budget and rate limit status queries for routing with baseline support
	// GetBudgetAndRateLimitStatus reports how close the request is to what it answers to for a
	// provider and model pair, read from the access on its grant. Routing asks this before an attempt's
	// limits are settled, one pair at a time, so it gathers rather than reads the settled set.
	GetBudgetAndRateLimitStatus(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, budgetBaselines map[string]float64, tokenBaselines map[string]int64, requestBaselines map[string]int64) *BudgetAndRateLimitStatus
	// CollectModelScopedGovernanceIDs returns the budget and rate-limit IDs owned by
	// model configs matching (provider, model) across the global, user and virtual-key
	// scopes. It takes the virtual key's ID rather than its value, so a caller settling
	// long after the request — batch accounting, which has only a stored row — can
	// still reach the same configs.
	CollectModelScopedGovernanceIDs(ctx context.Context, virtualKeyID string, userID string, provider schemas.ModelProvider, model string) (budgetIDs []string, rateLimitIDs []string)
}

// NewLocalGovernanceStore creates a new in-memory governance store.
//
// The modelCatalog parameter is optional (can be nil) and enables cross-provider model matching
// for governance lookups (e.g., "openai/gpt-4o" matching config for "gpt-4o"). inMemoryStore is
// the deployment's view of its configured MCP clients, read when building the grant a key carries
// so the clients allowed by default are appended; without it, no client is treated as open.
func NewLocalGovernanceStore(ctx context.Context, logger schemas.Logger, configStore configstore.ConfigStore, governanceConfig *configstore.GovernanceConfig, modelCatalog *modelcatalog.ModelCatalog, inMemoryStore InMemoryStore) (*LocalGovernanceStore, error) {
	store := &LocalGovernanceStore{
		inMemoryStore:                  inMemoryStore,
		configStore:                    configStore,
		logger:                         logger,
		modelCatalog:                   modelCatalog,
		LastDBUsagesBudgets:            make(map[string]float64),
		LastDBUsagesRequestsRateLimits: make(map[string]int64),
		LastDBUsagesTokensRateLimits:   make(map[string]int64),
	}

	if configStore != nil {
		// Load initial data from database
		if err := store.loadFromDatabase(ctx); err != nil {
			return nil, fmt.Errorf("failed to load initial data: %w", err)
		}
	} else {
		if err := store.loadFromConfigMemory(ctx, governanceConfig); err != nil {
			return nil, fmt.Errorf("failed to load governance data from config memory: %w", err)
		}
	}

	store.logger.Info("governance store initialized successfully")
	return store, nil
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
		merged.RefreshOverrideCyclesRemaining()
		if gs.budgets.CompareAndSwap(budgetID, raw, &merged) {
			return
		}
	}
}

// DeleteBudget deletes a budget from the local store.
func (gs *LocalGovernanceStore) DeleteBudget(ctx context.Context, budgetID string) {
	gs.budgets.Delete(budgetID)
	// Clean up LastDB baselines so the gossip delta doesn't carry stale entries.
	gs.LastDBUsagesBudgetsMu.Lock()
	delete(gs.LastDBUsagesBudgets, budgetID)
	// A dump holding this budget in an earlier snapshot must not resurrect its row.
	gs.markBudgetResetLocked(budgetID)
	gs.LastDBUsagesBudgetsMu.Unlock()
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
		if gs.rateLimits.CompareAndSwap(rateLimitID, raw, &merged) {
			return
		}
	}
}

// DeleteRateLimit deletes a rate limit from the local store.
func (gs *LocalGovernanceStore) DeleteRateLimit(ctx context.Context, rateLimitID string) {
	gs.rateLimits.Delete(rateLimitID)
	// Clean up LastDB baselines so the gossip delta doesn't carry stale entries.
	gs.LastDBUsagesRateLimitsTokensMu.Lock()
	delete(gs.LastDBUsagesTokensRateLimits, rateLimitID)
	gs.LastDBUsagesRateLimitsTokensMu.Unlock()
	gs.LastDBUsagesRateLimitsRequestsMu.Lock()
	delete(gs.LastDBUsagesRequestsRateLimits, rateLimitID)
	gs.LastDBUsagesRateLimitsRequestsMu.Unlock()
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
			gs.ResetExpiredBudgetsInMemory(ctx, false, budgetID)
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
			clone.RefreshOverrideCyclesRemaining()
			gs.LastDBUsagesBudgetsMu.Lock()
			gs.LastDBUsagesBudgets[budgetID] = 0
			gs.markBudgetResetLocked(budgetID)
			gs.LastDBUsagesBudgetsMu.Unlock()
		}
		clone.CurrentUsage += cost
		if gs.budgets.CompareAndSwap(budgetID, raw, &clone) {
			return nil
		}
	}
}

// BumpRateLimitUsage atomically increments the token and/or request counters on
// the rate limit identified by rateLimitID and, as a side effect, zeros the
// relevant counter / advances its LastReset when the rolling
// TokenResetDuration / RequestResetDuration has elapsed. Same CAS-retry
// contract as BumpBudgetUsage — no increment is ever dropped under
// concurrent callers. No-op when the rate limit is absent.
func (gs *LocalGovernanceStore) BumpRateLimitUsage(ctx context.Context, rateLimitID string, tokensUsed int64, shouldUpdateTokens, shouldUpdateRequests bool) error {
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
			gs.ResetExpiredRateLimitsInMemory(ctx, false, rateLimitID)
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
			if tokenNewLastReset != nil {
				clone.TokenCurrentUsage = 0
				clone.TokenLastReset = *tokenNewLastReset
				gs.LastDBUsagesRateLimitsTokensMu.Lock()
				gs.LastDBUsagesTokensRateLimits[rateLimitID] = 0
				gs.LastDBUsagesRateLimitsTokensMu.Unlock()
			}
			if requestNewLastReset != nil {
				clone.RequestCurrentUsage = 0
				clone.RequestLastReset = *requestNewLastReset
				gs.LastDBUsagesRateLimitsRequestsMu.Lock()
				gs.LastDBUsagesRequestsRateLimits[rateLimitID] = 0
				gs.LastDBUsagesRateLimitsRequestsMu.Unlock()
			}
		}
		if shouldUpdateTokens {
			clone.TokenCurrentUsage += tokensUsed
		}
		if shouldUpdateRequests {
			clone.RequestCurrentUsage++
		}
		if gs.rateLimits.CompareAndSwap(rateLimitID, raw, &clone) {
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

// ResetBudgetAt atomically zeros the budget's CurrentUsage and advances its
// LastReset to newLastReset, provided the currently-stored budget has an
// older LastReset. Returns the reset budget and true when the CAS succeeds;
// (nil, false) if the budget is absent or another writer has already advanced
// LastReset to at least newLastReset. Callers (e.g. ResetExpiredBudgetsInMemory)
// use the false return to skip the DB-persistence and reference-refresh work
// that would otherwise be redundant.
func (gs *LocalGovernanceStore) ResetBudgetAt(ctx context.Context, budgetID string, newLastReset time.Time) (*configstoreTables.TableBudget, bool) {
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
		clone.RefreshOverrideCyclesRemaining()
		if gs.budgets.CompareAndSwap(budgetID, raw, &clone) {
			return &clone, true
		}
	}
}

// ResetRateLimitAt atomically resets one or both rate-limit counters on the
// rate limit identified by rateLimitID. A non-nil tokenNewLastReset resets the
// token counter and advances TokenLastReset; similarly for
// requestNewLastReset. Each reset is conditional on the corresponding
// LastReset currently being strictly older than the supplied target, so
// concurrent resetters collapse into a single successful write. Returns the
// updated snapshot and true when at least one counter was reset; (nil, false)
// otherwise.
func (gs *LocalGovernanceStore) ResetRateLimitAt(ctx context.Context, rateLimitID string, tokenNewLastReset, requestNewLastReset *time.Time) (*configstoreTables.TableRateLimit, bool) {
	if tokenNewLastReset == nil && requestNewLastReset == nil {
		return nil, false
	}
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
		didReset := false
		if tokenNewLastReset != nil && old.TokenLastReset.Before(*tokenNewLastReset) {
			clone.TokenCurrentUsage = 0
			clone.TokenLastReset = *tokenNewLastReset
			didReset = true
		}
		if requestNewLastReset != nil && old.RequestLastReset.Before(*requestNewLastReset) {
			clone.RequestCurrentUsage = 0
			clone.RequestLastReset = *requestNewLastReset
			didReset = true
		}
		if !didReset {
			return nil, false
		}
		if gs.rateLimits.CompareAndSwap(rateLimitID, raw, &clone) {
			return &clone, true
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
// Callers applying remote state must funnel through here rather than mutating the
// pointer returned by GetGovernanceData, which is a live entry in the budgets
// sync.Map: writing through it races BumpBudgetUsage's clone-and-CAS and can drop
// a concurrent usage increment.
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

// GetGovernanceData returns a snapshot of the current governance data.
func (gs *LocalGovernanceStore) GetGovernanceData(ctx context.Context) *GovernanceData {
	refreshVKAssociations := func(vk *configstoreTables.TableVirtualKey) {
		if vk == nil {
			return
		}
		// Cross-reference live budget/rate limit from standalone maps
		// (usage updates clone into budgets/rateLimits maps, so embedded pointers go stale)
		// Hydrate multi-budgets from live sync.Map
		if len(vk.Budgets) > 0 {
			liveBudgets := make([]configstoreTables.TableBudget, 0, len(vk.Budgets))
			for _, b := range vk.Budgets {
				if lb, exists := gs.budgets.Load(b.ID); exists && lb != nil {
					if budget, ok := lb.(*configstoreTables.TableBudget); ok {
						liveBudgets = append(liveBudgets, *budget)
					}
				}
			}
			vk.Budgets = liveBudgets
		}
		if vk.RateLimitID != nil {
			if liveRL, exists := gs.rateLimits.Load(*vk.RateLimitID); exists && liveRL != nil {
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
						if lb, exists := gs.budgets.Load(b.ID); exists && lb != nil {
							if budget, ok := lb.(*configstoreTables.TableBudget); ok {
								liveBudgets = append(liveBudgets, *budget)
							}
						}
					}
					configs[i].Budgets = liveBudgets
				}
				if configs[i].RateLimitID != nil {
					if liveRL, exists := gs.rateLimits.Load(*configs[i].RateLimitID); exists && liveRL != nil {
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
		// Allocate a fresh slice — shallow-copying `team` (via `clone := *team` at
		// the caller) reuses the backing array, so in-place writes would mutate
		// the live gs.teams entry under concurrent reads. Mirrors the VK pattern
		// above. Budgets missing from gs.budgets are dropped rather than kept stale.
		if len(team.Budgets) > 0 {
			liveBudgets := make([]configstoreTables.TableBudget, 0, len(team.Budgets))
			for _, b := range team.Budgets {
				if lb, exists := gs.budgets.Load(b.ID); exists && lb != nil {
					if budget, ok := lb.(*configstoreTables.TableBudget); ok {
						liveBudgets = append(liveBudgets, *budget)
					}
				}
			}
			team.Budgets = liveBudgets
		}
		if team.RateLimitID != nil {
			if liveRL, exists := gs.rateLimits.Load(*team.RateLimitID); exists && liveRL != nil {
				if rl, ok := liveRL.(*configstoreTables.TableRateLimit); ok {
					team.RateLimit = rl
				}
			}
		}
	}
	virtualKeys := make(map[string]*configstoreTables.TableVirtualKey)
	gs.virtualKeys.Range(func(key, value interface{}) bool {
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
	gs.teams.Range(func(key, value interface{}) bool {
		team, ok := value.(*configstoreTables.TableTeam)
		if !ok || team == nil {
			return true // continue
		}
		clone := *team
		refreshTeamAssociations(&clone)
		// Reset to 0 — will be recomputed from live VKs below to stay accurate
		// after creates/updates/deletes that don't trigger a full ReloadTeam.
		clone.VirtualKeyCount = 0
		teams[key.(string)] = &clone
		return true // continue iteration
	})
	customers := make(map[string]*configstoreTables.TableCustomer)
	gs.customers.Range(func(key, value interface{}) bool {
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
			if liveBudget, exists := gs.budgets.Load(b.ID); exists && liveBudget != nil {
				if lb, ok := liveBudget.(*configstoreTables.TableBudget); ok {
					refreshedBudgets = append(refreshedBudgets, *lb)
					continue
				}
			}
			refreshedBudgets = append(refreshedBudgets, b)
		}
		clone.Budgets = refreshedBudgets
		if clone.RateLimitID != nil {
			if liveRL, exists := gs.rateLimits.Load(*clone.RateLimitID); exists && liveRL != nil {
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
	gs.budgets.Range(func(key, value interface{}) bool {
		budget, ok := value.(*configstoreTables.TableBudget)
		if !ok || budget == nil {
			return true // continue
		}
		budgets[key.(string)] = budget
		return true // continue iteration
	})
	rateLimits := make(map[string]*configstoreTables.TableRateLimit)
	gs.rateLimits.Range(func(key, value interface{}) bool {
		rateLimit, ok := value.(*configstoreTables.TableRateLimit)
		if !ok || rateLimit == nil {
			return true // continue
		}
		rateLimits[key.(string)] = rateLimit
		return true // continue iteration
	})
	var modelConfigsList []*configstoreTables.TableModelConfig
	gs.modelConfigs.Range(func(key, value any) bool {
		mc, ok := value.(*configstoreTables.TableModelConfig)
		if !ok || mc == nil {
			return true // continue
		}
		// Cross-reference live budgets/rate limit from standalone maps.
		clone := *mc
		if len(clone.Budgets) > 0 {
			liveBudgets := make([]configstoreTables.TableBudget, 0, len(clone.Budgets))
			for _, b := range clone.Budgets {
				if lb, exists := gs.budgets.Load(b.ID); exists && lb != nil {
					if budget, ok := lb.(*configstoreTables.TableBudget); ok {
						liveBudgets = append(liveBudgets, *budget)
					}
				}
			}
			clone.Budgets = liveBudgets
		}
		if clone.RateLimitID != nil {
			if liveRL, exists := gs.rateLimits.Load(*clone.RateLimitID); exists && liveRL != nil {
				if rl, ok := liveRL.(*configstoreTables.TableRateLimit); ok {
					clone.RateLimit = rl
				}
			}
		}
		modelConfigsList = append(modelConfigsList, &clone)
		return true // continue iteration
	})
	var providersList []*configstoreTables.TableProvider
	gs.providers.Range(func(key, value interface{}) bool {
		p, ok := value.(*configstoreTables.TableProvider)
		if !ok || p == nil {
			return true // continue
		}
		// Cross-reference live budget/rate limit from standalone maps
		clone := *p
		if clone.BudgetID != nil {
			if liveBudget, exists := gs.budgets.Load(*clone.BudgetID); exists && liveBudget != nil {
				if b, ok := liveBudget.(*configstoreTables.TableBudget); ok {
					clone.Budget = b
				}
			}
		}
		if clone.RateLimitID != nil {
			if liveRL, exists := gs.rateLimits.Load(*clone.RateLimitID); exists && liveRL != nil {
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
	}
}

// GetVirtualKey retrieves a virtual key by its value (lock-free) with all relationships preloaded.
// A VK can be reachable under two keys: its current value and, during the
// rotation grace period, its previous value. The matched key decides validity.
func (gs *LocalGovernanceStore) GetVirtualKey(ctx context.Context, vkValue string) (*configstoreTables.TableVirtualKey, bool) {
	value, exists := gs.virtualKeys.Load(vkValue)
	if !exists || value == nil {
		return nil, false
	}
	vk, ok := value.(*configstoreTables.TableVirtualKey)
	if !ok || vk == nil {
		return nil, false
	}
	switch vkValue {
	case vk.Value.GetValue():
		return vk, true
	case vk.PreviousValue.GetValue():
		if vk.HasActivePreviousValue(time.Now()) {
			gs.logger.Debug("virtual key %s authenticated via rotation grace-period value", vk.ID)
			return vk, true
		}
		// Grace window over: lazily drop only this value-keyed entry. Never use
		// deleteVirtualKeyByValue here - it would also remove the ID index entry
		// that the live current-value entry still needs. Compare against the
		// entry we just read so a key that claimed this value in the meantime
		// keeps its registration.
		gs.virtualKeys.CompareAndDelete(vkValue, value)
		return nil, false
	default:
		// Stale entry left behind by an earlier value change; same lazy cleanup.
		gs.virtualKeys.CompareAndDelete(vkValue, value)
		return nil, false
	}
}

// GetVirtualKeyByID retrieves a virtual key by its row ID (lock-free) with all
// relationships preloaded, via the ID-keyed secondary index. Mirrors
// GetVirtualKey (which is keyed by value); used by by-ID hot paths such as /mcp
// JWT auth to avoid a per-request database read.
func (gs *LocalGovernanceStore) GetVirtualKeyByID(ctx context.Context, vkID string) (*configstoreTables.TableVirtualKey, bool) {
	value, exists := gs.virtualKeysByID.Load(vkID)
	if !exists || value == nil {
		return nil, false
	}
	vk, ok := value.(*configstoreTables.TableVirtualKey)
	if !ok || vk == nil {
		return nil, false
	}
	return vk, true
}

// ResolvePermits reports the permits a request carries. This store knows only about virtual keys,
// so the presented key's permit is the whole answer and nothing scopes it.
func (gs *LocalGovernanceStore) ResolvePermits(ctx *schemas.BifrostContext) ([]schemas.Permit, schemas.Permit, grant.CompositionMode) {
	virtualKeyValue := PresentedVirtualKey(ctx)
	if virtualKeyValue == "" {
		// A key is the only thing this store resolves permits from, so no key means nothing granted
		// this request anything. It carries no access, which is what a request with no credential has
		// always been: unrestricted, and still bound by the deployment's own limits, which are gathered
		// for it whether or not it carries a permit.
		return nil, nil, ""
	}
	virtualKey, ok := gs.GetVirtualKey(ctx, virtualKeyValue)
	if !ok || virtualKey == nil {
		// A credential was presented and resolves to nothing. That is refused, not widened: the
		// funnel's identity step tells the two apart by what was presented.
		return nil, nil, ""
	}
	StampVirtualKeyScope(ctx, virtualKey)
	return []schemas.Permit{gs.permitForVirtualKey(ctx, virtualKey)}, nil, ""
}

// PresentedVirtualKey is the virtual key the request presented, read off the identity the
// transport settled on its grant. A grant nothing settled an identity on is read the way it always
// was, from the key stamped on the context directly.
//
// Exported because a store that resolves permits from something other than a key still resolves
// keys, and which key a request presented is the same question either way.
func PresentedVirtualKey(ctx *schemas.BifrostContext) string {
	if identity := ctx.Grant().Identity(); identity != nil {
		if credential := identity.Credential(); credential.Kind == string(grant.CredentialVirtualKey) {
			return credential.Value
		}
		// An identity was settled and it is not a key: whatever the context also carries under the
		// key's own name did not authenticate this request.
		return ""
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
func StampVirtualKeyScope(ctx *schemas.BifrostContext, virtualKey *configstoreTables.TableVirtualKey) {
	if ctx == nil || virtualKey == nil {
		return
	}
	recordVirtualKeyIdentity(ctx, virtualKey)
	ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, virtualKey.ID)
	ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyName, virtualKey.Name)
	if virtualKey.Team != nil {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceTeamID, virtualKey.Team.ID)
		ctx.SetValue(schemas.BifrostContextKeyGovernanceTeamName, virtualKey.Team.Name)
		if virtualKey.Team.Customer != nil {
			ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerID, virtualKey.Team.Customer.ID)
			ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerName, virtualKey.Team.Customer.Name)
		}
	}
	// A customer the key names directly wins over one reached through its team: it is the more specific
	// statement of who pays, and it is stamped second so it overwrites.
	if virtualKey.Customer != nil {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerID, virtualKey.Customer.ID)
		ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerName, virtualKey.Customer.Name)
	}
}

// recordVirtualKeyIdentity completes the request's identity with what the presented key resolved to:
// the key itself, and the team and customer it sits under, the attributed one first. What the
// transport settled, the credential and any user it named, is kept. A grant nothing settled an
// identity on has nothing to complete.
func recordVirtualKeyIdentity(ctx *schemas.BifrostContext, virtualKey *configstoreTables.TableVirtualKey) {
	current := ctx.Grant().Identity()
	if current == nil {
		return
	}
	var teams, customers []schemas.EntityRef
	if virtualKey.Team != nil {
		teams = append(teams, schemas.EntityRef{ID: virtualKey.Team.ID, Name: virtualKey.Team.Name})
	}
	// The customer the key names directly is the one the request is attributed to, and comes first;
	// one reached through the team is still one the request happens under.
	if virtualKey.Customer != nil {
		customers = append(customers, schemas.EntityRef{ID: virtualKey.Customer.ID, Name: virtualKey.Customer.Name})
	}
	if virtualKey.Team != nil && virtualKey.Team.Customer != nil &&
		(virtualKey.Customer == nil || virtualKey.Customer.ID != virtualKey.Team.Customer.ID) {
		customers = append(customers, schemas.EntityRef{ID: virtualKey.Team.Customer.ID, Name: virtualKey.Team.Customer.Name})
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
// own limits on that provider, and what the grant's configs for it are funded by. A limit that covers
// every provider answers the same for all of them, so it cannot make one candidate better than
// another: it can only exclude every candidate at once, which is a refusal, and the funnel states it
// with a reason rather than load balancing quietly running out of options.
//
// So load balancing routes around a provider that is out of room, and being out of money overall is
// left to the funnel, which is the only place that can say so.
//
// The model is what says which permits pay for the candidate: every permit that permits the model on
// the candidate's provider is funded on it, and a candidate is affordable only when all of them have
// room. A limit scoped to the model itself applies to every candidate serving it and is not asked
// about here, since it cannot discriminate.
func (gs *LocalGovernanceStore) CheckProviderCandidateExclusion(ctx *schemas.BifrostContext, access schemas.Access, candidate schemas.ProviderCandidate, model string) (Decision, error) {
	if spendingChecksSkipped(ctx) {
		return DecisionAllow, nil
	}

	provider := schemas.ModelProvider(candidate.Provider)
	budgets, rateLimits := gs.GlobalProviderLimits(ctx, provider)
	if access != nil {
		for _, permit := range access.PermitsForModel(candidate.Provider, model) {
			permitBudgets, permitRateLimits := gs.ProviderLimits(ctx, permit, provider)
			budgets = append(budgets, permitBudgets...)
			rateLimits = append(rateLimits, permitRateLimits...)
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
// and ProviderLimits, which read it by the permit's identity when an attempt's provider is known.
func (gs *LocalGovernanceStore) permitForVirtualKey(ctx context.Context, vk *configstoreTables.TableVirtualKey) *grant.Permit {
	if vk == nil {
		return nil
	}

	var allowedByDefaultClients map[string]string
	if gs.inMemoryStore != nil {
		allowedByDefaultClients = gs.inMemoryStore.GetMCPClientsAllowedByDefault()
	}

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

	// The key's own MCP configs come first, so they own their clients.
	mcpPermits := make([]schemas.MCPPermit, 0, len(vk.MCPConfigs)+len(allowedByDefaultClients))
	configured := make(map[string]struct{}, len(vk.MCPConfigs))
	for _, mcpConfig := range vk.MCPConfigs {
		configured[mcpConfig.MCPClient.ClientID] = struct{}{}
		mcpPermits = append(mcpPermits, schemas.MCPPermit{
			Client:     mcpConfig.MCPClient.ClientID,
			ClientName: mcpConfig.MCPClient.Name,
			Tools:      mcpConfig.ToolsToExecute,
		})
	}

	mcpPermits = AppendMCPPermitsAllowedByDefault(mcpPermits, configured, allowedByDefaultClients)

	return grant.NewPermit(grant.PermitVirtualKey, vk.ID, vk.Name, vk.IsActiveValue(), vk.IsExpiredAt(time.Now().UTC()), providerPermits, mcpPermits)
}

// AppendMCPPermitsAllowedByDefault adds a permit for every client allowed by default that the holder
// has not configured itself: all of the client's tools, under the client's name. A client in
// configured is left alone whatever it was configured with, since an explicit assignment decides for
// its holder even when it grants nothing.
//
// One function because every kind of holder appends the same way: a holder that has built its own
// list hands over the clients it named and gets the list back with the defaults added. Appended in
// id order, so the permit, and everything derived from it, is stable regardless of map iteration
// order.
func AppendMCPPermitsAllowedByDefault(mcpPermits []schemas.MCPPermit, configured map[string]struct{}, allowedByDefault map[string]string) []schemas.MCPPermit {
	if len(allowedByDefault) == 0 {
		return mcpPermits
	}
	clientIDs := make([]string, 0, len(allowedByDefault))
	for clientID := range allowedByDefault {
		if _, ok := configured[clientID]; ok {
			continue
		}
		clientIDs = append(clientIDs, clientID)
	}
	sort.Strings(clientIDs)
	for _, clientID := range clientIDs {
		mcpPermits = append(mcpPermits, schemas.MCPPermit{
			Client:     clientID,
			ClientName: allowedByDefault[clientID],
			Tools:      []string{grant.Wildcard},
		})
	}
	return mcpPermits
}

// virtualKeyOf is the key row behind a permit, when the permit is a key's. Nothing else this store
// funds, so any other permit answers with nothing.
func (gs *LocalGovernanceStore) virtualKeyOf(ctx context.Context, permit schemas.Permit) *configstoreTables.TableVirtualKey {
	if permit == nil || permit.Type() != string(grant.PermitVirtualKey) || permit.ID() == "" {
		return nil
	}
	vk, ok := gs.GetVirtualKeyByID(ctx, permit.ID())
	if !ok || vk == nil {
		return nil
	}
	return vk
}

// ProviderLimits reports what funds a key's use of one provider: the limits of every provider config
// the key holds for it. Every config, not the first: two configs for one provider are funded
// separately, and a request served by that provider draws on both. A config keeps its own row
// identity as holder, because nothing makes a provider unique within a key.
func (gs *LocalGovernanceStore) ProviderLimits(ctx context.Context, permit schemas.Permit, provider schemas.ModelProvider) (budgets []schemas.Limit, rateLimits []schemas.Limit) {
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
		rateLimits = append(rateLimits, grant.LimitsHeldBy(grant.LimitHolderVirtualKeyProviderConfig, holderID, vk.Name, config.Provider, "", rateLimitIDs(config.RateLimitID)...)...)
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
// times. ProviderLimits answers what discriminates, and this answers the rest.
//
// This is the seam a deployment reimplements to fund requests from something other than a key.
// Nothing downstream asks what kind of holder came back, so answering with a project's limits, or a
// user's, or both according to how that deployment is configured, needs no other path to change.
func (gs *LocalGovernanceStore) HolderLimits(ctx context.Context, permit schemas.Permit) (budgets []schemas.Limit, rateLimits []schemas.Limit) {
	vk := gs.virtualKeyOf(ctx, permit)
	if vk == nil {
		return nil, nil
	}

	budgets = grant.LimitsHeldBy(grant.LimitHolderVirtualKey, vk.ID, vk.Name, "", "", budgetIDs(vk.Budgets)...)
	rateLimits = grant.LimitsHeldBy(grant.LimitHolderVirtualKey, vk.ID, vk.Name, "", "", rateLimitIDs(vk.RateLimitID)...)

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

// GetTeamCustomerID returns a team's scalar customer id (TableTeam.CustomerID), or "" if the team is unknown or has no scalar customer. The enterprise layer uses it to exclude that customer from its M2M team→customer propagation so the OSS VK→team→customer hierarchy doesn't double-charge it.
func (gs *LocalGovernanceStore) GetTeamCustomerID(ctx context.Context, teamID string) string {
	if teamID == "" {
		return ""
	}
	teamValue, exists := gs.teams.Load(teamID)
	if !exists || teamValue == nil {
		return ""
	}
	team, ok := teamValue.(*configstoreTables.TableTeam)
	if !ok || team == nil || team.CustomerID == nil {
		return ""
	}
	return *team.CustomerID
}

// CollectTeamBudgets returns the live budget objects configured for a team,
// resolved by ID from the hot budgets map (so usage counters and recent edits
// are reflected). Mirrors the read pattern in CheckTeamBudget. Returns nil when
// the team is unknown or has no budgets. Exported so the enterprise layer can
// fold team budgets into a user→team→business-unit hierarchy collector the same
// way collectBudgetsFromHierarchy folds them into the VK hierarchy.
func (gs *LocalGovernanceStore) CollectTeamBudgets(ctx context.Context, teamID string) []*configstoreTables.TableBudget {
	if teamID == "" {
		return nil
	}
	teamValue, exists := gs.teams.Load(teamID)
	if !exists || teamValue == nil {
		return nil
	}
	team, ok := teamValue.(*configstoreTables.TableTeam)
	if !ok || team == nil || len(team.Budgets) == 0 {
		return nil
	}
	list := make([]*configstoreTables.TableBudget, 0, len(team.Budgets))
	for _, b := range team.Budgets {
		if hot := gs.LoadBudget(ctx, b.ID); hot != nil {
			list = append(list, hot)
		}
	}
	if len(list) == 0 {
		return nil
	}
	return list
}

// CollectTeamRateLimits returns the live rate-limit object configured for a team
// (at most one), resolved by ID from the hot rate-limits map. Mirrors the read
// pattern in CheckTeamRateLimit. Returns nil when the team is unknown or has no
// rate limit. Exported for the enterprise user-hierarchy collector.
func (gs *LocalGovernanceStore) CollectTeamRateLimits(ctx context.Context, teamID string) []*configstoreTables.TableRateLimit {
	if teamID == "" {
		return nil
	}
	teamValue, exists := gs.teams.Load(teamID)
	if !exists || teamValue == nil {
		return nil
	}
	team, ok := teamValue.(*configstoreTables.TableTeam)
	if !ok || team == nil || team.RateLimitID == nil {
		return nil
	}
	rl := gs.LoadRateLimit(ctx, *team.RateLimitID)
	if rl == nil {
		return nil
	}
	return []*configstoreTables.TableRateLimit{rl}
}

// CollectCustomerBudgets returns the customer's live budgets resolved from the hot budgets map, or nil if the customer is unknown or has none.
func (gs *LocalGovernanceStore) CollectCustomerBudgets(ctx context.Context, customerID string) []*configstoreTables.TableBudget {
	if customerID == "" {
		return nil
	}
	customerValue, exists := gs.customers.Load(customerID)
	if !exists || customerValue == nil {
		return nil
	}
	customer, ok := customerValue.(*configstoreTables.TableCustomer)
	if !ok || customer == nil || len(customer.Budgets) == 0 {
		return nil
	}
	list := make([]*configstoreTables.TableBudget, 0, len(customer.Budgets))
	for i := range customer.Budgets {
		if hot := gs.LoadBudget(ctx, customer.Budgets[i].ID); hot != nil {
			list = append(list, hot)
		}
	}
	return list
}

// CollectCustomerRateLimits returns the customer's live rate-limit (at most one) resolved from the hot rate-limits map, or nil if the customer is unknown or has none.
func (gs *LocalGovernanceStore) CollectCustomerRateLimits(ctx context.Context, customerID string) []*configstoreTables.TableRateLimit {
	if customerID == "" {
		return nil
	}
	customerValue, exists := gs.customers.Load(customerID)
	if !exists || customerValue == nil {
		return nil
	}
	customer, ok := customerValue.(*configstoreTables.TableCustomer)
	if !ok || customer == nil || customer.RateLimitID == nil {
		return nil
	}
	rl := gs.LoadRateLimit(ctx, *customer.RateLimitID)
	if rl == nil {
		return nil
	}
	return []*configstoreTables.TableRateLimit{rl}
}

// organizationLimitsForVirtualKey collects the limits a key answers to because of where it sits in
// the organization: its team's, and its customer's.
//
// The chain matches how enforcement derives it. A key attached straight to a customer answers to
// that customer; otherwise a customer is reached through the key's team, so a team's requests count
// against the customer containing it. Team and customer limits are provider-agnostic (they govern
// everything under them, whichever provider serves it), so they carry no provider of their own.
//
// The identities come from the key's preloaded relations, but the limits come from the store's own
// team and customer records: the key is loaded with enough of its team to know which team it is,
// not with that team's budgets.
func (gs *LocalGovernanceStore) organizationLimitsForVirtualKey(ctx context.Context, vk *configstoreTables.TableVirtualKey) (budgets []schemas.Limit, rateLimits []schemas.Limit) {
	if vk.TeamID != nil && *vk.TeamID != "" {
		if teamValue, ok := gs.teams.Load(*vk.TeamID); ok && teamValue != nil {
			if team, ok := teamValue.(*configstoreTables.TableTeam); ok && team != nil {
				budgets = append(budgets, grant.LimitsHeldBy(grant.LimitHolderTeam, team.ID, team.Name, "", "", budgetIDs(team.Budgets)...)...)
				rateLimits = append(rateLimits, grant.LimitsHeldBy(grant.LimitHolderTeam, team.ID, team.Name, "", "", rateLimitIDs(team.RateLimitID)...)...)
			}
		}
	}

	customerID := ""
	customerFromTeam := false
	switch {
	case vk.CustomerID != nil && *vk.CustomerID != "":
		customerID = *vk.CustomerID
	case vk.Team != nil && vk.Team.CustomerID != nil && *vk.Team.CustomerID != "":
		customerID = *vk.Team.CustomerID
		customerFromTeam = true
	}

	// A request can be scoped to one customer of the key's team, and then only that customer pays.
	// The customer a key reaches through its team is dropped when the request is scoped to a
	// different one: the scoped customer is enforced by whoever scoped it, and charging the team's
	// as well would bill a customer the request was explicitly directed away from. A customer the
	// key names directly is not reached through a team, so scoping cannot redirect it.
	if scopedCustomerID, _ := ctx.Value(schemas.BifrostContextKeyGovernanceScopedCustomerID).(string); customerFromTeam &&
		scopedCustomerID != "" && scopedCustomerID != customerID {
		customerID = ""
	}

	if customerID != "" {
		if customerValue, ok := gs.customers.Load(customerID); ok && customerValue != nil {
			if customer, ok := customerValue.(*configstoreTables.TableCustomer); ok && customer != nil {
				budgets = append(budgets, grant.LimitsHeldBy(grant.LimitHolderCustomer, customer.ID, customer.Name, "", "", budgetIDs(customer.Budgets)...)...)
				rateLimits = append(rateLimits, grant.LimitsHeldBy(grant.LimitHolderCustomer, customer.ID, customer.Name, "", "", rateLimitIDs(customer.RateLimitID)...)...)
			}
		}
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

// rateLimitIDs names a holder's single optional rate limit as the list a grant records. A key, a
// provider config, a team and a customer each carry at most one, but how many limits a holder
// answers to is the holder's business: a caller written against exactly one would have to be
// rewritten for the first holder with two.
func rateLimitIDs(rateLimitID *string) []string {
	if rateLimitID == nil {
		return nil
	}
	return []string{*rateLimitID}
}

// storeVirtualKey writes vk into both the value-keyed primary map and the
// ID-keyed secondary index, keeping the two in lock-step. Every writer to
// virtualKeys must go through here so the ID index never diverges.
func (gs *LocalGovernanceStore) storeVirtualKey(value string, vk *configstoreTables.TableVirtualKey) {
	if value == "" {
		if vk != nil {
			// Fail closed: an unresolvable VK must be unavailable on BOTH
			// lookup paths. Callers that delete value keys directly rely on
			// this to also retire the ID index entry, otherwise
			// GetVirtualKeyByID would keep serving the stale object.
			if vk.ID != "" {
				gs.virtualKeysByID.Delete(vk.ID)
			}
			gs.logger.Warn("skipping virtual key %s with unresolvable value (env/vault ref could not be resolved)", vk.ID)
		}
		return
	}
	gs.virtualKeys.Store(value, vk)
	if vk != nil && vk.ID != "" {
		gs.virtualKeysByID.Store(vk.ID, vk)
	}
	// Register an active grace-period previous value as a second key onto the
	// same object, so a rotated-out value keeps authenticating until expiry.
	if vk != nil && vk.HasActivePreviousValue(time.Now()) {
		if prev := vk.PreviousValue.GetValue(); prev != "" && prev != value {
			// Never displace the key that owns this value as its CURRENT
			// credential. The previous-value hash index is deliberately
			// non-unique, so a retired value can equal another key's live
			// value; handing the alias the entry would authenticate that
			// key's holder as this VK. Current ownership wins, matching the
			// database lookup order (current hash first, grace hash second).
			// LoadOrStore claims the slot atomically when it is free.
			if existing, loaded := gs.virtualKeys.LoadOrStore(prev, vk); loaded {
				owner, ok := existing.(*configstoreTables.TableVirtualKey)
				// Only a DIFFERENT key that still holds this value as its
				// current credential keeps the entry. Our own pre-rotation
				// entry looks like that too, so it must be replaced by the
				// rotated object, otherwise the retired value would keep
				// resolving as a current value and never expire.
				if !ok || owner == nil || owner.ID == vk.ID || owner.Value.GetValue() != prev {
					gs.virtualKeys.Store(prev, vk)
				}
			}
		}
	}
}

// deleteVirtualKeyByValue removes the VK stored under value from both the
// primary map and the ID-keyed secondary index.
func (gs *LocalGovernanceStore) deleteVirtualKeyByValue(value string) {
	existing, ok := gs.virtualKeys.Load(value)
	if !ok {
		return
	}
	if vk, isVK := existing.(*configstoreTables.TableVirtualKey); isVK && vk != nil && vk.ID != "" {
		gs.virtualKeysByID.Delete(vk.ID)
	}
	// Compare-and-delete so a concurrent writer that has already replaced this
	// entry keeps its own registration.
	gs.virtualKeys.CompareAndDelete(value, existing)
}

// deleteVirtualKeyAlias drops a value-keyed entry only while it still belongs to
// vkID. The previous-value hash index is deliberately non-unique, so a value one
// key is retiring may already be another key's current credential; deleting it
// blindly would deauthenticate that live key. CompareAndDelete then makes the
// removal atomic against a writer claiming the value in between.
func (gs *LocalGovernanceStore) deleteVirtualKeyAlias(value string, vkID string) {
	if value == "" {
		return
	}
	existing, ok := gs.virtualKeys.Load(value)
	if !ok {
		return
	}
	if owner, isVK := existing.(*configstoreTables.TableVirtualKey); isVK && owner != nil && owner.ID != vkID {
		return
	}
	gs.virtualKeys.CompareAndDelete(value, existing)
}

// CheckRateLimit checks rate limits for tokens and requests across categories
func (gs *LocalGovernanceStore) CheckRateLimit(ctx context.Context, entityWiseRateLimits EntityWiseRateLimits, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error) {
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
			if !requestLimitExpired && rateLimit.RequestMaxLimit != nil && rateLimit.RequestCurrentUsage+requestsBaseline >= *rateLimit.RequestMaxLimit {
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

// findScopedModelOnlyConfig looks up a model-only config (no provider) within a specific
// scope, preserving cross-provider model-name normalization. scope=="global" reproduces the
// historical global lookup exactly. Returns the matching config and the display name.
func (gs *LocalGovernanceStore) findScopedModelOnlyConfig(ctx context.Context, scope, scopeID, model string) (*configstoreTables.TableModelConfig, string) {
	tryKey := func(modelKey string) (*configstoreTables.TableModelConfig, string) {
		key := modelConfigStoreKey(scope, scopeID, modelKey, nil)
		if value, exists := gs.modelConfigs.Load(key); exists && value != nil {
			if mc, ok := value.(*configstoreTables.TableModelConfig); ok && mc != nil {
				return mc, modelKey
			}
		}
		return nil, ""
	}
	// If modelCatalog is available, try normalized base model name first (cross-provider matching)
	if gs.modelCatalog != nil {
		baseName := gs.modelCatalog.GetBaseModelName(model)
		if baseName != model {
			if mc, name := tryKey(baseName); mc != nil {
				return mc, name
			}
		}
	}
	// Always try direct lookup by original model name as fallback
	return tryKey(model)
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

// collectModelConfigsFor returns every model config that applies to a request for
// (model, provider) within a single scope, across four tiers (most → least specific),
// deduped by config ID:
//  1. (model, provider)  exact model on this provider
//  2. (model, nil)       exact model on all providers (base-name normalized)
//  3. ("*", provider)    all models on this provider  (provider-level governance)
//  4. ("*", nil)         all models on all providers
//
// This is the single source of truth for "which model configs apply"; every budget /
// rate-limit check and usage-tracking site iterates it so the wildcard tiers are matched
// consistently everywhere.
func (gs *LocalGovernanceStore) collectModelConfigsFor(ctx context.Context, scope, scopeID, model string, provider *string) []*configstoreTables.TableModelConfig {
	var out []*configstoreTables.TableModelConfig
	seen := make(map[string]bool)
	add := func(mc *configstoreTables.TableModelConfig) {
		if mc == nil || seen[mc.ID] {
			return
		}
		seen[mc.ID] = true
		out = append(out, mc)
	}
	loadKey := func(modelKey string, prov *string) *configstoreTables.TableModelConfig {
		if value, exists := gs.modelConfigs.Load(modelConfigStoreKey(scope, scopeID, modelKey, prov)); exists && value != nil {
			if mc, ok := value.(*configstoreTables.TableModelConfig); ok {
				return mc
			}
		}
		return nil
	}
	if provider != nil {
		add(loadKey(model, provider)) // tier 1: exact model + provider
	}
	if mc, _ := gs.findScopedModelOnlyConfig(ctx, scope, scopeID, model); mc != nil {
		add(mc) // tier 2: exact model, all providers (normalized)
	}
	if provider != nil {
		add(loadKey(modelConfigWildcard, provider)) // tier 3: all models on this provider
	}
	add(loadKey(modelConfigWildcard, nil)) // tier 4: all models, all providers
	return out
}

// modelConfigEntityKey builds a stable, unique entity description for a model config
func modelConfigEntityKey(mc *configstoreTables.TableModelConfig) string {
	name := mc.ModelName
	if name == modelConfigWildcard {
		name = "AllModels"
	}
	key := "Model:" + name
	if mc.Provider != nil {
		key += ":Provider:" + *mc.Provider
	}
	if mc.Scope != "" && mc.Scope != configstoreTables.ModelConfigScopeGlobal {
		scopeID := ""
		if mc.ScopeID != nil {
			scopeID = *mc.ScopeID
		}
		key += ":" + mc.Scope + ":" + scopeID
	}
	return key
}

// GetTeamName returns a team's display name from the in-memory store, or "" if
// the team is unknown. The enterprise layer uses it as the fallback for log
// stamping when its edge-driven name caches miss (e.g. a team with no user
// members and no business unit).
func (gs *LocalGovernanceStore) GetTeamName(ctx context.Context, teamID string) string {
	if teamID == "" {
		return ""
	}
	teamValue, exists := gs.teams.Load(teamID)
	if !exists || teamValue == nil {
		return ""
	}
	team, ok := teamValue.(*configstoreTables.TableTeam)
	if !ok || team == nil {
		return ""
	}
	return team.Name
}

// GetCustomerName returns a customer's display name from the in-memory store,
// or "" if the customer is unknown. Same fallback role as GetTeamName.
func (gs *LocalGovernanceStore) GetCustomerName(ctx context.Context, customerID string) string {
	if customerID == "" {
		return ""
	}
	customerValue, exists := gs.customers.Load(customerID)
	if !exists || customerValue == nil {
		return ""
	}
	customer, ok := customerValue.(*configstoreTables.TableCustomer)
	if !ok || customer == nil {
		return ""
	}
	return customer.Name
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
	gs.LastDBUsagesBudgetsMu.Lock()
	gs.LastDBUsagesBudgets[resetBudget.ID] = 0
	gs.markBudgetResetLocked(resetBudget.ID)
	gs.LastDBUsagesBudgetsMu.Unlock()
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
	reset, ok := gs.RebaseBudget(ctx, budgetID, 0, nil)
	if !ok {
		return nil, false
	}
	gs.LastDBUsagesBudgetsMu.Lock()
	gs.LastDBUsagesBudgets[budgetID] = 0
	gs.markBudgetResetLocked(budgetID)
	gs.LastDBUsagesBudgetsMu.Unlock()
	gs.logger.Debug(fmt.Sprintf("Reset budget %s usage on operator request (boundary unchanged at %s)", budgetID, reset.LastReset))
	return reset, true
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
// refreshReferences controls whether embedded owner references (VK, team, customer) are updated
// after reset. Background/ticker callers pass true; request-time callers pass false to avoid
// an O(N) scan of all owners on the hot path.
func (gs *LocalGovernanceStore) ResetExpiredBudgetsInMemory(ctx context.Context, refreshReferences bool, budgetIDs ...string) []*configstoreTables.TableBudget {
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
	if refreshReferences {
		gs.updateBudgetReferences(ctx, resetBudgets)
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
func (gs *LocalGovernanceStore) resetExpiredRateLimitFromSnapshot(ctx context.Context, rateLimit *configstoreTables.TableRateLimit, now time.Time) *configstoreTables.TableRateLimit {
	tokenNewLastReset, requestNewLastReset := gs.rateLimitResetTargets(rateLimit, now)
	if tokenNewLastReset == nil && requestNewLastReset == nil {
		return nil
	}
	resetRateLimit, ok := gs.ResetRateLimitAt(ctx, rateLimit.ID, tokenNewLastReset, requestNewLastReset)
	if !ok {
		return nil
	}
	if tokenNewLastReset != nil {
		gs.LastDBUsagesRateLimitsTokensMu.Lock()
		gs.LastDBUsagesTokensRateLimits[resetRateLimit.ID] = 0
		gs.LastDBUsagesRateLimitsTokensMu.Unlock()
	}
	if requestNewLastReset != nil {
		gs.LastDBUsagesRateLimitsRequestsMu.Lock()
		gs.LastDBUsagesRequestsRateLimits[resetRateLimit.ID] = 0
		gs.LastDBUsagesRateLimitsRequestsMu.Unlock()
	}
	return resetRateLimit
}

// ResetExpiredRateLimitsInMemory performs reset of expired rate limits for both provider-level and VK-level.
// With no rateLimitIDs it scans every rate limit; with IDs it only checks those rate limits.
// refreshReferences controls whether embedded owner references (VK, team, customer) are updated
// after reset. Background/ticker callers pass true; request-time callers pass false to avoid
// an O(N) scan of all owners on the hot path.
func (gs *LocalGovernanceStore) ResetExpiredRateLimitsInMemory(ctx context.Context, refreshReferences bool, rateLimitIDs ...string) []*configstoreTables.TableRateLimit {
	now := time.Now()
	var resetRateLimits []*configstoreTables.TableRateLimit
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
	if refreshReferences {
		gs.updateRateLimitReferences(ctx, resetRateLimits)
	}
	if len(resetRateLimits) > 0 {
		if onRateLimitsReset := gs.getRateLimitsResetHook(); onRateLimitsReset != nil {
			onRateLimitsReset(resetRateLimits)
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
func (gs *LocalGovernanceStore) ResetExpiredRateLimits(ctx context.Context, resetRateLimits []*configstoreTables.TableRateLimit) error {
	if gs.configStore == nil {
		return nil
	}
	// Token and request counters expire independently, so the batches are built
	// per dimension. A rate limit with no duration configured for a dimension
	// contributes nothing to that dimension's batch.
	var tokenRows, requestRows []rateLimitBoundaryRow
	for _, rateLimit := range resetRateLimits {
		if rateLimit == nil {
			continue
		}
		if rateLimit.TokenResetDuration != nil {
			tokenRows = append(tokenRows, rateLimitBoundaryRow{ID: rateLimit.ID, Boundary: rateLimit.TokenLastReset})
		}
		if rateLimit.RequestResetDuration != nil {
			requestRows = append(requestRows, rateLimitBoundaryRow{ID: rateLimit.ID, Boundary: rateLimit.RequestLastReset})
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
	ID                  string
	TokenCurrentUsage   int64
	TokenLastReset      time.Time
	RequestCurrentUsage int64
	RequestLastReset    time.Time
}

// budgetDumpRow is one budget's persisted usage and override state for a dump write.
type budgetDumpRow struct {
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
			// Direct UPDATE only touches usage fields, so a concurrent config
			// change to max_limit or reset_duration is never clobbered.
			if err := tx.WithContext(ctx).
				Session(&gorm.Session{SkipHooks: true}).
				Model(&configstoreTables.TableRateLimit{}).
				Where("id = ?", row.ID).
				Updates(map[string]interface{}{
					"token_current_usage":   row.TokenCurrentUsage,
					"token_last_reset":      row.TokenLastReset,
					"request_current_usage": row.RequestCurrentUsage,
					"request_last_reset":    row.RequestLastReset,
				}).Error; err != nil {
				return fmt.Errorf("failed to dump rate limit %s: %w", row.ID, err)
			}
		}
		return nil
	}

	var sb strings.Builder
	args := make([]any, 0, len(batch)*5)
	sb.WriteString("UPDATE " + configstoreTables.TableRateLimit{}.TableName() + " AS t SET " +
		"token_current_usage = v.tcu, token_last_reset = v.tlr, " +
		"request_current_usage = v.rcu, request_last_reset = v.rlr FROM (VALUES ")
	for i, row := range batch {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(?::varchar,?::bigint,?::timestamptz,?::bigint,?::timestamptz)")
		args = append(args, row.ID, row.TokenCurrentUsage, row.TokenLastReset, row.RequestCurrentUsage, row.RequestLastReset)
	}
	sb.WriteString(") AS v(id, tcu, tlr, rcu, rlr) WHERE t.id = v.id")
	if err := tx.WithContext(ctx).Exec(sb.String(), args...).Error; err != nil {
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
				Where("id = ? AND last_reset < ?", row.ID, row.LastReset).
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
				Where("id = ? AND last_reset "+usageGuard+" ?", row.ID, row.LastReset).
				Updates(map[string]interface{}{
					"current_usage": row.CurrentUsage,
					"last_reset":    row.LastReset,
				}).Error; err != nil {
				return fmt.Errorf("failed to update budget %s: %w", row.ID, err)
			}
		}
		return nil
	}

	table := configstoreTables.TableBudget{}.TableName()

	var overrideSQL strings.Builder
	overrideArgs := make([]any, 0, len(batch)*7)
	overrideSQL.WriteString("UPDATE " + table + " AS t SET " +
		"override_amount = v.oa, override_mode = v.om, override_cycles_remaining = v.ocr, " +
		"override_cycles_total = v.oct, override_anchor_reset = v.oar FROM (VALUES ")
	for i, row := range batch {
		if i > 0 {
			overrideSQL.WriteString(",")
		}
		overrideSQL.WriteString("(?::varchar,?::double precision,?::varchar,?::integer,?::integer,?::timestamptz,?::timestamptz)")
		overrideArgs = append(overrideArgs, row.ID, row.OverrideAmount, string(row.OverrideMode),
			row.OverrideCyclesRemaining, row.OverrideCyclesTotal, row.OverrideAnchorReset, row.LastReset)
	}
	overrideSQL.WriteString(") AS v(id, oa, om, ocr, oct, oar, lr) WHERE t.id = v.id AND t.last_reset < v.lr")
	if err := tx.WithContext(ctx).Exec(overrideSQL.String(), overrideArgs...).Error; err != nil {
		return fmt.Errorf("failed to update budget override lifecycle for %d budgets: %w", len(batch), err)
	}

	var usageSQL strings.Builder
	usageArgs := make([]any, 0, len(batch)*3)
	usageSQL.WriteString("UPDATE " + table + " AS t SET current_usage = v.cu, last_reset = v.lr FROM (VALUES ")
	for i, row := range batch {
		if i > 0 {
			usageSQL.WriteString(",")
		}
		usageSQL.WriteString("(?::varchar,?::double precision,?::timestamptz)")
		usageArgs = append(usageArgs, row.ID, row.CurrentUsage, row.LastReset)
	}
	usageSQL.WriteString(") AS v(id, cu, lr) WHERE t.id = v.id AND t.last_reset " + usageGuard + " v.lr")
	if err := tx.WithContext(ctx).Exec(usageSQL.String(), usageArgs...).Error; err != nil {
		return fmt.Errorf("failed to update %d budgets: %w", len(batch), err)
	}
	return nil
}

// rateLimitBoundaryRow is one rate limit's new boundary for a single counter
// dimension. Token and request counters expire independently, so a reset batch
// is built per dimension rather than per row.
type rateLimitBoundaryRow struct {
	ID       string
	Boundary time.Time
}

// resetRateLimitDimensionBatch zeroes one counter dimension for a batch of rate
// limits and advances its boundary column.
//
// The guard is the boundary column rather than a test of whether the in-memory
// counter is still zero: under sustained traffic a request bumps the counter
// back above zero between the in-memory reset and this write, which used to drop
// the whole field pair including the boundary. A dimension that was not reset in
// this sweep is a no-op because its in-memory boundary already equals the
// persisted one.
func (gs *LocalGovernanceStore) resetRateLimitDimensionBatch(ctx context.Context, tx *gorm.DB, usageColumn, boundaryColumn string, batch []rateLimitBoundaryRow) error {
	if len(batch) == 0 {
		return nil
	}
	if !supportsBatchedDump(tx) {
		for _, row := range batch {
			if err := tx.WithContext(ctx).
				Session(&gorm.Session{SkipHooks: true}).
				Model(&configstoreTables.TableRateLimit{}).
				Where("id = ? AND "+boundaryColumn+" < ?", row.ID, row.Boundary).
				Updates(map[string]interface{}{
					usageColumn:    0,
					boundaryColumn: row.Boundary,
				}).Error; err != nil {
				return fmt.Errorf("failed to reset rate limit %s %s: %w", row.ID, boundaryColumn, err)
			}
		}
		return nil
	}

	var sb strings.Builder
	args := make([]any, 0, len(batch)*2)
	sb.WriteString("UPDATE " + configstoreTables.TableRateLimit{}.TableName() + " AS t SET " +
		usageColumn + " = 0, " + boundaryColumn + " = v.b FROM (VALUES ")
	for i, row := range batch {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(?::varchar,?::timestamptz)")
		args = append(args, row.ID, row.Boundary)
	}
	sb.WriteString(") AS v(id, b) WHERE t.id = v.id AND t." + boundaryColumn + " < v.b")
	if err := tx.WithContext(ctx).Exec(sb.String(), args...).Error; err != nil {
		return fmt.Errorf("failed to reset %s for %d rate limits: %w", boundaryColumn, len(batch), err)
	}
	return nil
}

// DumpRateLimits dumps all rate limits to the database
func (gs *LocalGovernanceStore) DumpRateLimits(ctx context.Context, tokenBaselines map[string]int64, requestBaselines map[string]int64) error {
	if gs.configStore == nil {
		return nil
	}
	// This is to prevent nil pointer dereference
	if tokenBaselines == nil {
		tokenBaselines = map[string]int64{}
	}
	if requestBaselines == nil {
		requestBaselines = map[string]int64{}
	}
	// Range over ALL rate limits in memory (mirrors DumpBudgets pattern).
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
			ID:                  rateLimit.ID,
			TokenCurrentUsage:   rateLimit.TokenCurrentUsage,
			TokenLastReset:      rateLimit.TokenLastReset,
			RequestCurrentUsage: rateLimit.RequestCurrentUsage,
			RequestLastReset:    rateLimit.RequestLastReset,
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

// DumpBudgets dumps all budgets to the database.
//
// Split into a snapshot and a write so the gap between them is expressible in a
// test: that gap is where an operator reset can land, and the reset generations
// taken here are what let the write recognise a row cleared underneath it.
func (gs *LocalGovernanceStore) DumpBudgets(ctx context.Context, baselines map[string]float64) error {
	if gs.configStore == nil {
		return nil
	}
	rows, gens := gs.snapshotBudgetRows(baselines)
	return gs.writeBudgetRows(ctx, rows, gens)
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
	budgets := make(map[string]*configstoreTables.TableBudget)
	gs.budgets.Range(func(key, value interface{}) bool {
		// Type-safe conversion
		keyStr, keyOk := key.(string)
		budget, budgetOk := value.(*configstoreTables.TableBudget)

		if keyOk && budgetOk && budget != nil {
			budgets[keyStr] = budget // Store budget by ID
		}
		return true // continue iteration
	})
	rows := make([]budgetDumpRow, 0, len(budgets))
	for _, budget := range budgets {
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
			OverrideAmount:          budget.OverrideAmount,
			OverrideMode:            budget.OverrideMode,
			OverrideCyclesRemaining: budget.OverrideCyclesRemaining,
			OverrideCyclesTotal:     budget.OverrideCyclesTotal,
			OverrideAnchorReset:     budget.OverrideAnchorReset,
		})
	}
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

// loadFromDatabase loads all governance data from the database into memory
func (gs *LocalGovernanceStore) loadFromDatabase(ctx context.Context) error {
	loadStart := time.Now()
	defer func() {
		gs.logger.Info("[startup-timing] loadFromDatabase total took %v", time.Since(loadStart))
	}()
	// Load customers with their budgets
	customers, err := gs.configStore.GetCustomers(ctx)
	if err != nil {
		return fmt.Errorf("failed to load customers: %w", err)
	}

	// Load teams with their budgets
	teams, err := gs.configStore.GetTeams(ctx, "")
	if err != nil {
		return fmt.Errorf("failed to load teams: %w", err)
	}

	// Load virtual keys with all relationships
	vkLoadStart := time.Now()
	virtualKeys, err := gs.configStore.GetVirtualKeys(ctx)
	if err != nil {
		return fmt.Errorf("failed to load virtual keys: %w", err)
	}
	gs.logger.Info("[startup-timing] loadFromDatabase GetVirtualKeys loaded %d keys in %v", len(virtualKeys), time.Since(vkLoadStart))

	// Load budgets
	budgets, err := gs.configStore.GetBudgets(ctx)
	if err != nil {
		return fmt.Errorf("failed to load budgets: %w", err)
	}

	// Load rate limits
	rateLimits, err := gs.configStore.GetRateLimits(ctx)
	if err != nil {
		return fmt.Errorf("failed to load rate limits: %w", err)
	}

	// Load model configs
	modelConfigs, err := gs.configStore.GetModelConfigs(ctx)
	if err != nil {
		return fmt.Errorf("failed to load model configs: %w", err)
	}

	// Load providers with governance relationships (similar to GetModelConfigs)
	providers, err := gs.configStore.GetProviders(ctx)
	if err != nil {
		return fmt.Errorf("failed to load providers: %w", err)
	}

	// Rebuild in-memory structures (lock-free)
	rebuildStart := time.Now()
	gs.rebuildInMemoryStructures(ctx, customers, teams, virtualKeys, budgets, rateLimits, modelConfigs, providers)
	gs.logger.Info("[startup-timing] loadFromDatabase rebuildInMemoryStructures took %v", time.Since(rebuildStart))

	return nil
}

// loadFromConfigMemory loads all governance data from the config's memory into store's memory
func (gs *LocalGovernanceStore) loadFromConfigMemory(ctx context.Context, config *configstore.GovernanceConfig) error {
	if config == nil {
		return fmt.Errorf("governance config is nil")
	}

	// Load customers with their budgets
	customers := config.Customers

	// Load teams with their budgets
	teams := config.Teams

	// Load budgets
	budgets := config.Budgets

	// Load virtual keys with all relationships
	virtualKeys := config.VirtualKeys

	// Load rate limits
	rateLimits := config.RateLimits

	// Load model configs
	modelConfigs := config.ModelConfigs

	// Load providers
	providers := config.Providers

	// Populate model configs with their relationships (Budgets and RateLimit)
	for i := range modelConfigs {
		mc := &modelConfigs[i]

		// Populate multi-budgets owned via TableBudget.ModelConfigID (the active path).
		if len(mc.Budgets) == 0 {
			for j := range budgets {
				if budgets[j].ModelConfigID != nil && *budgets[j].ModelConfigID == mc.ID {
					mc.Budgets = append(mc.Budgets, budgets[j])
				}
			}
		}

		// Legacy single-budget linking (inert; kept for backward-compatible config.json).
		if mc.BudgetID != nil {
			for j := range budgets {
				if budgets[j].ID == *mc.BudgetID {
					mc.Budget = &budgets[j]
					break
				}
			}
		}

		// Populate rate limit
		if mc.RateLimitID != nil {
			for j := range rateLimits {
				if rateLimits[j].ID == *mc.RateLimitID {
					mc.RateLimit = &rateLimits[j]
					break
				}
			}
		}

		modelConfigs[i] = *mc
	}

	// Populate providers with their relationships (Budget and RateLimit)
	for i := range providers {
		provider := &providers[i]

		// Populate budget
		if provider.BudgetID != nil {
			for j := range budgets {
				if budgets[j].ID == *provider.BudgetID {
					provider.Budget = &budgets[j]
					break
				}
			}
		}

		// Populate rate limit
		if provider.RateLimitID != nil {
			for j := range rateLimits {
				if rateLimits[j].ID == *provider.RateLimitID {
					provider.RateLimit = &rateLimits[j]
					break
				}
			}
		}

		providers[i] = *provider
	}

	// Populate virtual keys with their relationships
	for i := range virtualKeys {
		vk := &virtualKeys[i]

		for i := range teams {
			if vk.TeamID != nil && teams[i].ID == *vk.TeamID {
				vk.Team = &teams[i]
			}
		}

		for i := range customers {
			if vk.CustomerID != nil && customers[i].ID == *vk.CustomerID {
				vk.Customer = &customers[i]
			}
		}

		for i := range rateLimits {
			if vk.RateLimitID != nil && rateLimits[i].ID == *vk.RateLimitID {
				vk.RateLimit = &rateLimits[i]
			}
		}

		// Populate provider config relationships with rate limits
		if vk.ProviderConfigs != nil {
			for j := range vk.ProviderConfigs {
				pc := &vk.ProviderConfigs[j]

				// Populate rate limit
				if pc.RateLimitID != nil {
					for k := range rateLimits {
						if rateLimits[k].ID == *pc.RateLimitID {
							pc.RateLimit = &rateLimits[k]
							break
						}
					}
				}
			}
		}

		virtualKeys[i] = *vk
	}

	// Rebuild in-memory structures (lock-free)
	gs.rebuildInMemoryStructures(ctx, customers, teams, virtualKeys, budgets, rateLimits, modelConfigs, providers)

	return nil
}

// rebuildInMemoryStructures rebuilds all in-memory data structures (lock-free)
func (gs *LocalGovernanceStore) rebuildInMemoryStructures(ctx context.Context, customers []configstoreTables.TableCustomer, teams []configstoreTables.TableTeam, virtualKeys []configstoreTables.TableVirtualKey, budgets []configstoreTables.TableBudget, rateLimits []configstoreTables.TableRateLimit, modelConfigs []configstoreTables.TableModelConfig, providers []configstoreTables.TableProvider) {
	// Clear existing data by creating new sync.Maps
	gs.virtualKeys = sync.Map{}
	gs.virtualKeysByID = sync.Map{}
	gs.teams = sync.Map{}
	gs.customers = sync.Map{}
	gs.budgets = sync.Map{}
	gs.rateLimits = sync.Map{}
	gs.modelConfigs = sync.Map{}
	gs.providers = sync.Map{}

	// Build customers map
	for i := range customers {
		customer := &customers[i]
		gs.customers.Store(customer.ID, customer)
	}

	// Build teams map
	for i := range teams {
		team := &teams[i]
		gs.teams.Store(team.ID, team)
	}

	// Build budgets map
	for i := range budgets {
		budget := &budgets[i]
		gs.storeBudget(budget.ID, budget)
	}

	// Build rate limits map
	for i := range rateLimits {
		rateLimit := &rateLimits[i]
		gs.rateLimits.Store(rateLimit.ID, rateLimit)
	}

	// Build virtual keys map and track active VKs
	for i := range virtualKeys {
		vk := &virtualKeys[i]
		gs.storeVirtualKey(vk.Value.GetValue(), vk)
	}

	// Build model configs map.
	// Key format (global scope): "modelName" for all-provider configs, "modelName:provider"
	// for provider-specific configs. Non-global scopes (e.g. virtual_key) prefix the key with
	// "<scope>:<scopeID>:" via modelConfigStoreKey so they never collide with global configs.
	// Model names are normalized using GetBaseModelName to prevent duplicate config leakage
	// (e.g., "openai/gpt-4o" and "gpt-4o" both store under base "gpt-4o").
	for i := range modelConfigs {
		mc := &modelConfigs[i]
		// Stamp calendar alignment onto owned budgets and rate limit so the reset path
		// reads the right window (the flat budgets/rate-limits list lacks owner context).
		// Mirrors how VK/team budgets are stamped from their owner.
		for j := range mc.Budgets {
			mc.Budgets[j].IsCalendarAligned = mc.CalendarAligned
			gs.storeBudget(mc.Budgets[j].ID, &mc.Budgets[j])
		}
		if mc.RateLimit != nil {
			mc.RateLimit.IsCalendarAligned = mc.CalendarAligned
			gs.rateLimits.Store(mc.RateLimit.ID, mc.RateLimit)
		}
		scopeID := ""
		if mc.ScopeID != nil {
			scopeID = *mc.ScopeID
		}
		if mc.Provider != nil {
			// Provider-specific: store under (scope-prefixed) "modelName:provider" key
			key := modelConfigStoreKey(mc.Scope, scopeID, mc.ModelName, mc.Provider)
			gs.modelConfigs.Store(key, mc)
		} else {
			// All-provider config - store under normalized (scope-prefixed) model name.
			// The "*" (all-models) sentinel is never normalized.
			modelKey := mc.ModelName
			if gs.modelCatalog != nil && mc.ModelName != modelConfigWildcard {
				modelKey = gs.modelCatalog.GetBaseModelName(mc.ModelName)
			}
			key := modelConfigStoreKey(mc.Scope, scopeID, modelKey, nil)
			gs.modelConfigs.Store(key, mc)
		}
	}

	// Stamp customer-owned budget and rate-limit entries in the flat caches so
	// calendar-aligned resets survive restarts and reloads. Mirrors the model-config
	// stamping above.
	for i := range customers {
		customer := &customers[i]
		for j := range customer.Budgets {
			customer.Budgets[j].IsCalendarAligned = customer.CalendarAligned
			gs.storeBudget(customer.Budgets[j].ID, &customer.Budgets[j])
		}
		if customer.RateLimitID != nil {
			if raw, ok := gs.rateLimits.Load(*customer.RateLimitID); ok {
				if rl, ok := raw.(*configstoreTables.TableRateLimit); ok && rl != nil {
					rl.IsCalendarAligned = customer.CalendarAligned
				}
			}
		}
	}

	// Build providers map
	// Key format: provider name (e.g., "openai", "anthropic")
	for i := range providers {
		provider := &providers[i]
		gs.providers.Store(provider.Name, provider)
	}

	// Load last DB usages from database entities (assign and populate inside mutexes to avoid race with ResetExpired*InMemory)
	gs.LastDBUsagesBudgetsMu.Lock()
	gs.LastDBUsagesBudgets = make(map[string]float64)
	for i := range budgets {
		budget := &budgets[i]
		gs.LastDBUsagesBudgets[budget.ID] = budget.CurrentUsage
	}
	gs.LastDBUsagesBudgetsMu.Unlock()

	gs.LastDBUsagesRateLimitsRequestsMu.Lock()
	gs.LastDBUsagesRateLimitsTokensMu.Lock()
	gs.LastDBUsagesRequestsRateLimits = make(map[string]int64)
	gs.LastDBUsagesTokensRateLimits = make(map[string]int64)
	for i := range rateLimits {
		rateLimit := &rateLimits[i]
		gs.LastDBUsagesRequestsRateLimits[rateLimit.ID] = rateLimit.RequestCurrentUsage
		gs.LastDBUsagesTokensRateLimits[rateLimit.ID] = rateLimit.TokenCurrentUsage
	}
	gs.LastDBUsagesRateLimitsTokensMu.Unlock()
	gs.LastDBUsagesRateLimitsRequestsMu.Unlock()
}

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
// (provider, model) pair, across the global, user and virtual-key scopes.
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
	addModelConfigIDs := func(mc *configstoreTables.TableModelConfig) {
		for i := range mc.Budgets {
			if id := mc.Budgets[i].ID; !seenBudgets[id] {
				budgetIDs = append(budgetIDs, id)
				seenBudgets[id] = true
			}
		}
		if mc.RateLimitID != nil && !seenRateLimits[*mc.RateLimitID] {
			rateLimitIDs = append(rateLimitIDs, *mc.RateLimitID)
			seenRateLimits[*mc.RateLimitID] = true
		}
	}

	scopes := []struct{ name, id string }{
		{configstoreTables.ModelConfigScopeGlobal, ""},
		{configstoreTables.ModelConfigScopeVirtualKey, virtualKeyID},
	}
	for _, scope := range scopes {
		if scope.name != configstoreTables.ModelConfigScopeGlobal && scope.id == "" {
			continue
		}
		for _, mc := range gs.collectModelConfigsFor(ctx, scope.name, scope.id, model, providerStr) {
			addModelConfigIDs(mc)
		}
	}
	// Downstream-registered scopes (e.g. enterprise's per-access-profile per-model
	// budgets, keyed by a user-access-profile association ID rather than the raw
	// user/VK ID) — see RegisterExtraScopedIDsResolver.
	for _, pair := range collectExtraScopedIDs(ctx, virtualKeyID, userID) {
		for _, mc := range gs.collectModelConfigsFor(ctx, pair.Scope, pair.ScopeID, model, providerStr) {
			addModelConfigIDs(mc)
		}
	}
	return budgetIDs, rateLimitIDs
}

// ScopedID names a (scope, scope_id) pair for a model-config lookup.
type ScopedID struct {
	Scope   string
	ScopeID string
}

// ExtraScopedIDsResolver returns additional (scope, scope_id) pairs to check for
// model-config-based budgets/rate-limits on a request, given its virtual key and
// user IDs. Lets downstream consumers (e.g. enterprise access profiles) graft
// extra scopes onto CollectModelScopedGovernanceIDs's built-in global/user/
// virtual_key set without this package needing to know their scope semantics.
// Must be fast and non-blocking (in-memory only) — called on every request.
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

// CreateVirtualKeyInMemory adds a new virtual key to the in-memory store (lock-free)
func (gs *LocalGovernanceStore) CreateVirtualKeyInMemory(ctx context.Context, vk *configstoreTables.TableVirtualKey) {
	if vk == nil {
		return // Nothing to create
	}

	clone := *vk

	// Clone provider configs
	if vk.ProviderConfigs != nil {
		clone.ProviderConfigs = make([]configstoreTables.TableVirtualKeyProviderConfig, len(vk.ProviderConfigs))
		copy(clone.ProviderConfigs, vk.ProviderConfigs)
	}

	// Store budgets
	for i := range clone.Budgets {
		clone.Budgets[i].IsCalendarAligned = clone.CalendarAligned
		gs.storeBudget(clone.Budgets[i].ID, &clone.Budgets[i])
	}

	// Create associated rate limit if exists
	if clone.RateLimit != nil {
		clone.RateLimit.IsCalendarAligned = clone.CalendarAligned
		gs.rateLimits.Store(clone.RateLimit.ID, clone.RateLimit)
	}

	// Create provider config budgets and rate limits if they exist
	if clone.ProviderConfigs != nil {
		for i := range clone.ProviderConfigs {
			pc := &clone.ProviderConfigs[i]
			for j := range pc.Budgets {
				pc.Budgets[j].IsCalendarAligned = clone.CalendarAligned
				gs.storeBudget(pc.Budgets[j].ID, &pc.Budgets[j])
			}
			if pc.RateLimit != nil {
				pc.RateLimit.IsCalendarAligned = clone.CalendarAligned
				gs.rateLimits.Store(pc.RateLimit.ID, pc.RateLimit)
			}
		}
	}

	gs.storeVirtualKey(clone.Value.GetValue(), &clone)
}

// UpdateVirtualKeyInMemory updates an existing virtual key in the in-memory store (lock-free)
func (gs *LocalGovernanceStore) UpdateVirtualKeyInMemory(ctx context.Context, vk *configstoreTables.TableVirtualKey, budgetBaselines map[string]float64, rateLimitTokensBaselines map[string]int64, rateLimitRequestsBaselines map[string]int64) {
	if vk == nil {
		return // Nothing to update
	}

	// Do not update the current usage of the rate limit, as it will be updated by the usage tracker.
	// But update if max limit or reset duration changes.
	existingVKKey := vk.Value.GetValue()
	existingVKValue, exists := gs.virtualKeys.Load(vk.Value.GetValue())
	if exists && existingVKValue != nil {
		if existingVK, ok := existingVKValue.(*configstoreTables.TableVirtualKey); !ok || existingVK == nil || existingVK.ID != vk.ID {
			exists = false
			existingVKValue = nil
		}
	}
	if !exists || existingVKValue == nil {
		gs.virtualKeys.Range(func(key, value interface{}) bool {
			existingVK, ok := value.(*configstoreTables.TableVirtualKey)
			if !ok || existingVK == nil || existingVK.ID != vk.ID {
				return true
			}
			existingVKKey, _ = key.(string)
			existingVKValue = value
			exists = true
			return false
		})
	}
	if exists && existingVKValue != nil {
		existingVK, ok := existingVKValue.(*configstoreTables.TableVirtualKey)
		if !ok || existingVK == nil {
			return // Nothing to update
		}

		// Create clone to avoid modifying the original
		clone := *vk

		// Collect all incoming budget IDs across VK + provider configs to avoid
		// deleting a budget that was moved between VK-level and PC-level in one update.
		allNewBudgetIDs := make(map[string]bool)
		for i := range clone.Budgets {
			allNewBudgetIDs[clone.Budgets[i].ID] = true
		}
		for i := range clone.ProviderConfigs {
			for j := range clone.ProviderConfigs[i].Budgets {
				allNewBudgetIDs[clone.ProviderConfigs[i].Budgets[j].ID] = true
			}
		}

		// Update multi-budgets for VK
		for i := range clone.Budgets {
			// Preserve existing usage from memory
			if existingBudgetValue, exists := gs.budgets.Load(clone.Budgets[i].ID); exists && existingBudgetValue != nil {
				if existingBudget, ok := existingBudgetValue.(*configstoreTables.TableBudget); ok && existingBudget != nil {
					clone.Budgets[i].CurrentUsage = existingBudget.CurrentUsage
					clone.Budgets[i].LastReset = existingBudget.LastReset
				}
			}
			clone.Budgets[i].IsCalendarAligned = clone.CalendarAligned
			gs.storeBudget(clone.Budgets[i].ID, &clone.Budgets[i])
		}
		// Delete removed multi-budgets
		for _, oldBudget := range existingVK.Budgets {
			if !allNewBudgetIDs[oldBudget.ID] {
				gs.DeleteBudget(ctx, oldBudget.ID)
			}
		}

		if clone.RateLimit != nil {
			// Preserve existing usage from memory when updating rate limit config
			// The usage tracker maintains current usage in memory, and we only want to update
			// the configuration fields (max_limit, reset_duration) from the database
			if existingRateLimitValue, exists := gs.rateLimits.Load(clone.RateLimit.ID); exists && existingRateLimitValue != nil {
				if existingRateLimit, ok := existingRateLimitValue.(*configstoreTables.TableRateLimit); ok && existingRateLimit != nil {
					// Preserve current usage and last reset times from existing in-memory rate limit
					clone.RateLimit.TokenCurrentUsage = existingRateLimit.TokenCurrentUsage
					clone.RateLimit.RequestCurrentUsage = existingRateLimit.RequestCurrentUsage
					clone.RateLimit.TokenLastReset = existingRateLimit.TokenLastReset
					clone.RateLimit.RequestLastReset = existingRateLimit.RequestLastReset
				}
			}
			clone.RateLimit.IsCalendarAligned = clone.CalendarAligned
			// Update the rate limit in the main rateLimits sync.Map
			gs.rateLimits.Store(clone.RateLimit.ID, clone.RateLimit)
			// Clean up old rate limit if ID changed (e.g., after AP propagation
			// creates a fresh UUID). Without this the orphaned entry leaks memory
			// and its stale usage pollutes gossip baselines.
			if existingVK.RateLimit != nil && existingVK.RateLimit.ID != clone.RateLimit.ID {
				gs.DeleteRateLimit(ctx, existingVK.RateLimit.ID)
			}
		} else if existingVK.RateLimit != nil {
			// Rate limit was removed from the virtual key, delete it from memory
			gs.DeleteRateLimit(ctx, existingVK.RateLimit.ID)
		}
		if clone.ProviderConfigs != nil {
			// Create a map of existing provider configs by ID for fast lookup
			existingProviderConfigs := make(map[uint]configstoreTables.TableVirtualKeyProviderConfig)
			if existingVK.ProviderConfigs != nil {
				for _, existingPC := range existingVK.ProviderConfigs {
					existingProviderConfigs[existingPC.ID] = existingPC
				}
			}

			// Collect all new rate limit IDs from new provider configs
			allNewRateLimitIDs := make(map[string]bool)
			if clone.RateLimit != nil {
				allNewRateLimitIDs[clone.RateLimit.ID] = true
			}
			for _, pc := range clone.ProviderConfigs {
				if pc.RateLimit != nil {
					allNewRateLimitIDs[pc.RateLimit.ID] = true
				}
			}

			// Process each new/updated provider config
			for i, pc := range clone.ProviderConfigs {
				if pc.RateLimit != nil {
					// Preserve existing usage from memory when updating provider config rate limit
					if existingRateLimitValue, exists := gs.rateLimits.Load(pc.RateLimit.ID); exists && existingRateLimitValue != nil {
						if existingRateLimit, ok := existingRateLimitValue.(*configstoreTables.TableRateLimit); ok && existingRateLimit != nil {
							// Preserve current usage and last reset times from existing in-memory rate limit
							clone.ProviderConfigs[i].RateLimit.TokenCurrentUsage = existingRateLimit.TokenCurrentUsage
							clone.ProviderConfigs[i].RateLimit.RequestCurrentUsage = existingRateLimit.RequestCurrentUsage
							clone.ProviderConfigs[i].RateLimit.TokenLastReset = existingRateLimit.TokenLastReset
							clone.ProviderConfigs[i].RateLimit.RequestLastReset = existingRateLimit.RequestLastReset
						}
					}
					clone.ProviderConfigs[i].RateLimit.IsCalendarAligned = clone.CalendarAligned
					gs.rateLimits.Store(clone.ProviderConfigs[i].RateLimit.ID, clone.ProviderConfigs[i].RateLimit)
				} else {
					// Rate limit was removed from provider config, delete it from memory if it existed
					if existingPC, exists := existingProviderConfigs[pc.ID]; exists && existingPC.RateLimit != nil {
						gs.DeleteRateLimit(ctx, existingPC.RateLimit.ID)
						clone.ProviderConfigs[i].RateLimit = nil
					}
				}
				// Update multi-budgets for provider config
				for j := range clone.ProviderConfigs[i].Budgets {
					b := &clone.ProviderConfigs[i].Budgets[j]
					if existingBudgetValue, exists := gs.budgets.Load(b.ID); exists && existingBudgetValue != nil {
						if existingBudget, ok := existingBudgetValue.(*configstoreTables.TableBudget); ok && existingBudget != nil {
							b.CurrentUsage = existingBudget.CurrentUsage
							b.LastReset = existingBudget.LastReset
						}
					}
					b.IsCalendarAligned = clone.CalendarAligned
					gs.storeBudget(b.ID, b)
				}
				// Delete removed multi-budgets for this provider config
				if existingPC, exists := existingProviderConfigs[pc.ID]; exists {
					for _, oldBudget := range existingPC.Budgets {
						if !allNewBudgetIDs[oldBudget.ID] {
							gs.DeleteBudget(ctx, oldBudget.ID)
						}
					}
				}
			}
			// Clean up orphaned rate limits and budgets from old provider configs
			// whose IDs changed (e.g., AP propagation replaces all configs with
			// new DB row IDs). Without this, stale entries leak memory and
			// pollute gossip baselines.
			for _, oldPC := range existingVK.ProviderConfigs {
				if oldPC.RateLimit != nil && !allNewRateLimitIDs[oldPC.RateLimit.ID] {
					gs.DeleteRateLimit(ctx, oldPC.RateLimit.ID)
				}
				for _, oldBudget := range oldPC.Budgets {
					if !allNewBudgetIDs[oldBudget.ID] {
						gs.DeleteBudget(ctx, oldBudget.ID)
					}
				}
			}
		}
		// Reconcile value keys: keep the new current value plus an active
		// grace-period previous value; drop every other key that pointed at this
		// VK (the old current value on a plain value change, and a stale previous
		// value after repeated rotation). Direct deletes keep the ID index intact;
		// storeVirtualKey refreshes it below.
		newValue := clone.Value.GetValue()
		keep := make(map[string]bool)
		if newValue != "" {
			// An unresolvable current value keeps keep empty so every alias —
			// including an active grace-period previous value — is dropped:
			// fail closed on all lookup paths, matching storeVirtualKey.
			keep[newValue] = true
			if clone.HasActivePreviousValue(time.Now()) {
				keep[clone.PreviousValue.GetValue()] = true
			}
		}
		for _, oldKey := range []string{existingVKKey, existingVK.Value.GetValue(), existingVK.PreviousValue.GetValue()} {
			if oldKey != "" && !keep[oldKey] {
				gs.deleteVirtualKeyAlias(oldKey, clone.ID)
			}
		}
		gs.storeVirtualKey(newValue, &clone)
	} else {
		gs.CreateVirtualKeyInMemory(ctx, vk)
	}
}

// DeleteVirtualKeyInMemory removes a virtual key from the in-memory store
func (gs *LocalGovernanceStore) DeleteVirtualKeyInMemory(ctx context.Context, vkID string) {
	if vkID == "" {
		return // Nothing to delete
	}

	// Find and delete the VK by ID (lock-free)
	gs.virtualKeys.Range(func(key, value interface{}) bool {
		// Type-safe conversion
		vk, ok := value.(*configstoreTables.TableVirtualKey)
		if !ok || vk == nil {
			return true // continue iteration
		}

		if vk.ID == vkID {
			// Delete budgets
			for _, b := range vk.Budgets {
				gs.DeleteBudget(ctx, b.ID)
			}

			// Delete associated rate limit if exists
			if vk.RateLimitID != nil {
				gs.DeleteRateLimit(ctx, *vk.RateLimitID)
			}

			// Delete provider config budgets and rate limits
			if vk.ProviderConfigs != nil {
				for _, pc := range vk.ProviderConfigs {
					for _, b := range pc.Budgets {
						gs.DeleteBudget(ctx, b.ID)
					}
					if pc.RateLimitID != nil {
						gs.DeleteRateLimit(ctx, *pc.RateLimitID)
					}
				}
			}

			gs.deleteVirtualKeyByValue(key.(string))
			// The VK can be registered under a second key (current or
			// grace-period previous value); remove both so neither
			// authenticates after deletion.
			if prev := vk.PreviousValue.GetValue(); prev != "" && prev != key.(string) {
				gs.deleteVirtualKeyAlias(prev, vkID)
			}
			if cur := vk.Value.GetValue(); cur != "" && cur != key.(string) {
				gs.deleteVirtualKeyAlias(cur, vkID)
			}
			return false // stop iteration
		}
		return true // continue iteration
	})

	// Evict any model configs scoped to this virtual key (and their budgets/rate-limits).
	// Mirrors the DB-side cleanup in DeleteVirtualKey and keeps the in-memory store
	// consistent even when the VK entry was already removed.
	gs.DeleteModelConfigsForScopeInMemory(ctx, configstoreTables.ModelConfigScopeVirtualKey, vkID)
}

// DeleteModelConfigsForScopeInMemory evicts every cached model config targeting the
// given scope owner (e.g. scope=virtual_key, scopeID=<vk id>) along with the budgets
// and rate-limits those configs own. It is the in-memory mirror of
// RDBConfigStore.DeleteModelConfigsForScope; every owner-eviction path routes through
// here so the cleanup lives in one place. Exported so out-of-package owner-eviction
// paths (e.g. the enterprise user-deletion flow) reuse it. Owned budgets are released
// from both the active Budgets slice and the legacy single BudgetID column.
func (gs *LocalGovernanceStore) DeleteModelConfigsForScopeInMemory(ctx context.Context, scope, scopeID string) {
	gs.modelConfigs.Range(func(key, value any) bool {
		mc, ok := value.(*configstoreTables.TableModelConfig)
		if !ok || mc == nil {
			return true
		}
		if mc.Scope == scope && mc.ScopeID != nil && *mc.ScopeID == scopeID {
			for i := range mc.Budgets {
				gs.DeleteBudget(ctx, mc.Budgets[i].ID)
			}
			if mc.BudgetID != nil {
				gs.DeleteBudget(ctx, *mc.BudgetID)
			}
			if mc.RateLimitID != nil {
				gs.DeleteRateLimit(ctx, *mc.RateLimitID)
			}
			gs.modelConfigs.Delete(key)
		}
		return true
	})
}

// CreateTeamInMemory adds a new team to the in-memory store (lock-free)
func (gs *LocalGovernanceStore) CreateTeamInMemory(ctx context.Context, team *configstoreTables.TableTeam) {
	if team == nil {
		return // Nothing to create
	}

	clone := *team

	// Create associated budgets if they exist
	for i := range clone.Budgets {
		clone.Budgets[i].IsCalendarAligned = clone.CalendarAligned
		b := clone.Budgets[i]
		gs.storeBudget(b.ID, &b)
	}

	// Create associated rate limit if exists
	if clone.RateLimit != nil {
		clone.RateLimit.IsCalendarAligned = clone.CalendarAligned
		gs.rateLimits.Store(clone.RateLimit.ID, clone.RateLimit)
	}

	gs.teams.Store(clone.ID, &clone)
}

// UpdateTeamInMemory updates an existing team in the in-memory store (lock-free)
func (gs *LocalGovernanceStore) UpdateTeamInMemory(ctx context.Context, team *configstoreTables.TableTeam, budgetBaselines map[string]float64) {
	if team == nil {
		return // Nothing to update
	}

	// Check if there's an existing team to get current budget state
	if existingTeamValue, exists := gs.teams.Load(team.ID); exists && existingTeamValue != nil {
		existingTeam, ok := existingTeamValue.(*configstoreTables.TableTeam)
		if !ok || existingTeam == nil {
			return // Nothing to update
		}

		// Create clone to avoid modifying the original
		clone := *team

		// Reconcile multi-budget slice by ID: preserve live usage on matches,
		// evict budgets that disappeared from the team (owned-FK semantics —
		// a team's budgets are team-scoped, so dropping the association means
		// the budget no longer exists for anyone).
		existingBudgetIDs := map[string]struct{}{}
		for _, b := range existingTeam.Budgets {
			existingBudgetIDs[b.ID] = struct{}{}
		}
		nextBudgetIDs := map[string]struct{}{}
		for i := range clone.Budgets {
			b := &clone.Budgets[i]
			nextBudgetIDs[b.ID] = struct{}{}
			if live, exists := gs.budgets.Load(b.ID); exists && live != nil {
				if lb, ok := live.(*configstoreTables.TableBudget); ok && lb != nil {
					b.CurrentUsage = lb.CurrentUsage
					b.LastReset = lb.LastReset
				}
			}
			b.IsCalendarAligned = clone.CalendarAligned
			gs.storeBudget(b.ID, b)
		}
		for id := range existingBudgetIDs {
			if _, stillThere := nextBudgetIDs[id]; !stillThere {
				gs.DeleteBudget(ctx, id)
			}
		}

		// Handle rate limit updates with consistent logic
		if clone.RateLimit != nil {
			// Preserve existing usage from memory when updating team rate limit config
			if existingRateLimitValue, exists := gs.rateLimits.Load(clone.RateLimit.ID); exists && existingRateLimitValue != nil {
				if existingRateLimit, ok := existingRateLimitValue.(*configstoreTables.TableRateLimit); ok && existingRateLimit != nil {
					// Preserve current usage and last reset time from existing in-memory rate limit
					clone.RateLimit.TokenCurrentUsage = existingRateLimit.TokenCurrentUsage
					clone.RateLimit.TokenLastReset = existingRateLimit.TokenLastReset
					clone.RateLimit.RequestCurrentUsage = existingRateLimit.RequestCurrentUsage
					clone.RateLimit.RequestLastReset = existingRateLimit.RequestLastReset
				}
			}
			clone.RateLimit.IsCalendarAligned = clone.CalendarAligned
			gs.rateLimits.Store(clone.RateLimit.ID, clone.RateLimit)
			// Clean up old rate limit if ID changed (e.g., UUID rotation on propagation)
			if existingTeam.RateLimit != nil && existingTeam.RateLimit.ID != clone.RateLimit.ID {
				gs.DeleteRateLimit(ctx, existingTeam.RateLimit.ID)
			}
		} else if existingTeam.RateLimit != nil {
			// Rate limit was removed from the team, delete it from memory
			gs.DeleteRateLimit(ctx, existingTeam.RateLimit.ID)
		}

		gs.teams.Store(team.ID, &clone)
	} else {
		gs.CreateTeamInMemory(ctx, team)
	}
}

// DeleteTeamInMemory removes a team from the in-memory store (lock-free)
func (gs *LocalGovernanceStore) DeleteTeamInMemory(ctx context.Context, teamID string) {
	if teamID == "" {
		return // Nothing to delete
	}

	// Get team to check for associated budgets and rate limit
	if teamValue, exists := gs.teams.Load(teamID); exists && teamValue != nil {
		if team, ok := teamValue.(*configstoreTables.TableTeam); ok && team != nil {
			// Delete all associated budgets
			for _, b := range team.Budgets {
				gs.DeleteBudget(ctx, b.ID)
			}
			// Delete associated rate limit if exists
			if team.RateLimitID != nil {
				gs.DeleteRateLimit(ctx, *team.RateLimitID)
			}
		}
	}

	// Set team_id to null for all virtual keys associated with the team
	// Iterate through all VKs since team.VirtualKeys may not be populated
	gs.virtualKeys.Range(func(key, value interface{}) bool {
		vk, ok := value.(*configstoreTables.TableVirtualKey)
		if !ok || vk == nil {
			return true // continue
		}
		if vk.TeamID != nil && *vk.TeamID == teamID {
			clone := *vk
			clone.TeamID = nil
			clone.Team = nil
			gs.storeVirtualKey(key.(string), &clone)
		}
		return true // continue iteration
	})

	gs.teams.Delete(teamID)
}

// CreateCustomerInMemory adds a new customer to the in-memory store (lock-free)
func (gs *LocalGovernanceStore) CreateCustomerInMemory(ctx context.Context, customer *configstoreTables.TableCustomer) {
	if customer == nil {
		return // Nothing to create
	}
	clone := *customer
	for i := range clone.Budgets {
		clone.Budgets[i].IsCalendarAligned = clone.CalendarAligned
		gs.storeBudget(clone.Budgets[i].ID, &clone.Budgets[i])
	}
	if clone.RateLimit != nil {
		clone.RateLimit.IsCalendarAligned = clone.CalendarAligned
		gs.rateLimits.Store(clone.RateLimit.ID, clone.RateLimit)
	}
	gs.customers.Store(clone.ID, &clone)
}

// UpdateCustomerInMemory updates an existing customer in the in-memory store (lock-free)
func (gs *LocalGovernanceStore) UpdateCustomerInMemory(ctx context.Context, customer *configstoreTables.TableCustomer, budgetBaselines map[string]float64) {
	if customer == nil {
		return // Nothing to update
	}
	// Check if there's an existing customer to get current budget state
	if existingCustomerValue, exists := gs.customers.Load(customer.ID); exists && existingCustomerValue != nil {
		existingCustomer, ok := existingCustomerValue.(*configstoreTables.TableCustomer)
		if !ok || existingCustomer == nil {
			return // Nothing to update
		}
		// Create clone to avoid modifying the original
		clone := *customer

		// Reconcile budgets: upsert new set, delete any that were removed.
		newBudgetIDs := make(map[string]bool, len(clone.Budgets))
		for i := range clone.Budgets {
			b := &clone.Budgets[i]
			b.IsCalendarAligned = clone.CalendarAligned
			if existingBudgetValue, exists := gs.budgets.Load(b.ID); exists && existingBudgetValue != nil {
				if existingBudget, ok := existingBudgetValue.(*configstoreTables.TableBudget); ok && existingBudget != nil {
					b.CurrentUsage = existingBudget.CurrentUsage
					b.LastReset = existingBudget.LastReset
				}
			}
			gs.storeBudget(b.ID, b)
			newBudgetIDs[b.ID] = true
		}
		for _, existing := range existingCustomer.Budgets {
			if !newBudgetIDs[existing.ID] {
				gs.DeleteBudget(ctx, existing.ID)
			}
		}

		// Handle rate limit updates with consistent logic
		if clone.RateLimit != nil {
			clone.RateLimit.IsCalendarAligned = clone.CalendarAligned
			// Preserve existing usage from memory when updating customer rate limit config
			if existingRateLimitValue, exists := gs.rateLimits.Load(clone.RateLimit.ID); exists && existingRateLimitValue != nil {
				if existingRateLimit, ok := existingRateLimitValue.(*configstoreTables.TableRateLimit); ok && existingRateLimit != nil {
					// Preserve current usage and last reset time from existing in-memory rate limit
					clone.RateLimit.TokenCurrentUsage = existingRateLimit.TokenCurrentUsage
					clone.RateLimit.TokenLastReset = existingRateLimit.TokenLastReset
					clone.RateLimit.RequestCurrentUsage = existingRateLimit.RequestCurrentUsage
					clone.RateLimit.RequestLastReset = existingRateLimit.RequestLastReset
				}
			}
			gs.rateLimits.Store(clone.RateLimit.ID, clone.RateLimit)
			// Clean up old rate limit if ID changed (e.g., UUID rotation on propagation)
			if existingCustomer.RateLimit != nil && existingCustomer.RateLimit.ID != clone.RateLimit.ID {
				gs.DeleteRateLimit(ctx, existingCustomer.RateLimit.ID)
			}
		} else if existingCustomer.RateLimit != nil {
			// Rate limit was removed from the customer, delete it from memory
			gs.DeleteRateLimit(ctx, existingCustomer.RateLimit.ID)
		}

		gs.customers.Store(customer.ID, &clone)
	} else {
		gs.CreateCustomerInMemory(ctx, customer)
	}
}

// DeleteCustomerInMemory removes a customer from the in-memory store (lock-free)
func (gs *LocalGovernanceStore) DeleteCustomerInMemory(ctx context.Context, customerID string) {
	if customerID == "" {
		return // Nothing to delete
	}
	// Get customer to check for associated budget and rate limit
	if customerValue, exists := gs.customers.Load(customerID); exists && customerValue != nil {
		if customer, ok := customerValue.(*configstoreTables.TableCustomer); ok && customer != nil {
			for _, b := range customer.Budgets {
				gs.DeleteBudget(ctx, b.ID)
			}
			// Delete associated rate limit if exists
			if customer.RateLimitID != nil {
				gs.DeleteRateLimit(ctx, *customer.RateLimitID)
			}
		}
	}
	// Set customer_id to null for all virtual keys associated with the customer
	// Iterate through all VKs since customer.VirtualKeys may not be populated
	gs.virtualKeys.Range(func(key, value interface{}) bool {
		vk, ok := value.(*configstoreTables.TableVirtualKey)
		if !ok || vk == nil {
			return true // continue
		}
		if vk.CustomerID != nil && *vk.CustomerID == customerID {
			clone := *vk
			clone.CustomerID = nil
			clone.Customer = nil
			gs.storeVirtualKey(key.(string), &clone)
		}
		return true // continue iteration
	})
	// Set customer_id to null for all teams associated with the customer
	// Iterate through all teams since customer.Teams may not be populated
	gs.teams.Range(func(key, value interface{}) bool {
		team, ok := value.(*configstoreTables.TableTeam)
		if !ok || team == nil {
			return true // continue
		}
		if team.CustomerID != nil && *team.CustomerID == customerID {
			clone := *team
			clone.CustomerID = nil
			clone.Customer = nil
			gs.teams.Store(key, &clone)
		}
		return true // continue iteration
	})
	gs.customers.Delete(customerID)
}

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

// UpdateModelConfigInMemory adds or updates a model config in the in-memory store (lock-free)
// Preserves existing usage values when updating budgets and rate limits
// Returns the updated model config with potentially modified usage values
func (gs *LocalGovernanceStore) UpdateModelConfigInMemory(ctx context.Context, mc *configstoreTables.TableModelConfig) *configstoreTables.TableModelConfig {
	if mc == nil {
		return nil // Nothing to update
	}

	// Clone to avoid modifying the original
	clone := *mc

	// Store associated budgets, preserving existing in-memory usage per budget ID and
	// stamping calendar alignment from the model config (consumed by the reset path).
	for i := range clone.Budgets {
		b := &clone.Budgets[i]
		b.IsCalendarAligned = clone.CalendarAligned
		if existingBudgetValue, exists := gs.budgets.Load(b.ID); exists && existingBudgetValue != nil {
			if eb, ok := existingBudgetValue.(*configstoreTables.TableBudget); ok && eb != nil {
				b.CurrentUsage = eb.CurrentUsage
				b.LastReset = eb.LastReset
			}
		}
		gs.storeBudget(b.ID, b)
	}

	// Store associated rate limit if exists, preserving existing in-memory usage and
	// stamping calendar alignment from the owning model config.
	if clone.RateLimit != nil {
		clone.RateLimit.IsCalendarAligned = clone.CalendarAligned
		if existingRateLimitValue, exists := gs.rateLimits.Load(clone.RateLimit.ID); exists && existingRateLimitValue != nil {
			if erl, ok := existingRateLimitValue.(*configstoreTables.TableRateLimit); ok && erl != nil {
				clone.RateLimit.TokenCurrentUsage = erl.TokenCurrentUsage
				clone.RateLimit.RequestCurrentUsage = erl.RequestCurrentUsage
			}
		}
		gs.rateLimits.Store(clone.RateLimit.ID, clone.RateLimit)
	}

	// Determine the (scope-aware) key. Global scope keeps the historical key format;
	// non-global scopes are namespaced by modelConfigStoreKey. Scope/scope_id are part of
	// a config's identity and do not change on update, so this matches the stored key.
	scopeID := ""
	if clone.ScopeID != nil {
		scopeID = *clone.ScopeID
	}
	if clone.Provider != nil {
		key := modelConfigStoreKey(clone.Scope, scopeID, clone.ModelName, clone.Provider)
		gs.modelConfigs.Store(key, &clone)
	} else {
		modelKey := clone.ModelName
		if gs.modelCatalog != nil && clone.ModelName != modelConfigWildcard {
			modelKey = gs.modelCatalog.GetBaseModelName(clone.ModelName)
		}
		key := modelConfigStoreKey(clone.Scope, scopeID, modelKey, nil)
		gs.modelConfigs.Store(key, &clone)
	}

	return &clone
}

// DeleteModelConfigInMemory removes a model config from the in-memory store (lock-free)
func (gs *LocalGovernanceStore) DeleteModelConfigInMemory(ctx context.Context, mcID string) {
	if mcID == "" {
		return // Nothing to delete
	}

	// Find and delete the model config by ID
	gs.modelConfigs.Range(func(key, value interface{}) bool {
		mc, ok := value.(*configstoreTables.TableModelConfig)
		if !ok || mc == nil {
			return true // continue iteration
		}

		if mc.ID == mcID {
			// Delete associated budgets if any
			for i := range mc.Budgets {
				gs.DeleteBudget(ctx, mc.Budgets[i].ID)
			}

			// Delete associated rate limit if exists
			if mc.RateLimitID != nil {
				gs.DeleteRateLimit(ctx, *mc.RateLimitID)
			}

			gs.modelConfigs.Delete(key)
			return false // stop iteration
		}
		return true // continue iteration
	})
}

// ScopedModelConfigIDs returns the IDs of all in-memory model configs for the
// given (scope, scopeID). Callers use this to diff against the DB result and
// evict stale entries via DeleteModelConfigInMemory.
func (gs *LocalGovernanceStore) ScopedModelConfigIDs(scope, scopeID string) []string {
	var ids []string
	gs.modelConfigs.Range(func(key, value interface{}) bool {
		mc, ok := value.(*configstoreTables.TableModelConfig)
		if !ok || mc == nil {
			return true
		}
		mcScopeID := ""
		if mc.ScopeID != nil {
			mcScopeID = *mc.ScopeID
		}
		if mc.Scope == scope && mcScopeID == scopeID {
			ids = append(ids, mc.ID)
		}
		return true
	})
	return ids
}

// UpdateProviderInMemory adds or updates a provider in the in-memory store (lock-free)
// Preserves existing usage values when updating budgets and rate limits
// Returns the updated provider with potentially modified usage values
func (gs *LocalGovernanceStore) UpdateProviderInMemory(ctx context.Context, provider *configstoreTables.TableProvider) *configstoreTables.TableProvider {
	if provider == nil {
		return nil // Nothing to update
	}

	// Clone to avoid modifying the original
	clone := *provider

	// Store associated budget if exists, preserving existing in-memory usage
	if clone.Budget != nil {
		if existingBudgetValue, exists := gs.budgets.Load(clone.Budget.ID); exists && existingBudgetValue != nil {
			if eb, ok := existingBudgetValue.(*configstoreTables.TableBudget); ok && eb != nil {
				clone.Budget.CurrentUsage = eb.CurrentUsage
			}
		}
		gs.storeBudget(clone.Budget.ID, clone.Budget)
	}

	// Store associated rate limit if exists, preserving existing in-memory usage
	if clone.RateLimit != nil {
		if existingRateLimitValue, exists := gs.rateLimits.Load(clone.RateLimit.ID); exists && existingRateLimitValue != nil {
			if erl, ok := existingRateLimitValue.(*configstoreTables.TableRateLimit); ok && erl != nil {
				clone.RateLimit.TokenCurrentUsage = erl.TokenCurrentUsage
				clone.RateLimit.RequestCurrentUsage = erl.RequestCurrentUsage
			}
		}
		gs.rateLimits.Store(clone.RateLimit.ID, clone.RateLimit)
	}

	// Store under provider name
	gs.providers.Store(clone.Name, &clone)

	return &clone
}

// DeleteProviderInMemory removes a provider from the in-memory store (lock-free)
func (gs *LocalGovernanceStore) DeleteProviderInMemory(ctx context.Context, providerName string) {
	if providerName == "" {
		return // Nothing to delete
	}
	// Get provider to check for associated budget/rate limit
	if providerValue, exists := gs.providers.Load(providerName); exists && providerValue != nil {
		if provider, ok := providerValue.(*configstoreTables.TableProvider); ok && provider != nil {
			// Delete associated budget if exists
			if provider.BudgetID != nil {
				gs.DeleteBudget(ctx, *provider.BudgetID)
			}

			// Delete associated rate limit if exists
			if provider.RateLimitID != nil {
				gs.DeleteRateLimit(ctx, *provider.RateLimitID)
			}
		}
	}
	gs.providers.Delete(providerName)
}

// Helper functions

// updateBudgetReferences updates all VKs, teams, customers, and provider configs
// that reference any of the reset budgets. It makes ONE pass over each owner map
// regardless of how many budgets reset, and clones an owner only after a match,
// so a background sweep costs O(owners + resets) instead of O(owners x resets)
// with a clone per owner per reset. Essential at large key counts; the contract
// is pinned by TestBackgroundResetReferenceRefreshScales.
func (gs *LocalGovernanceStore) updateBudgetReferences(ctx context.Context, resetBudgets []*configstoreTables.TableBudget) {
	if len(resetBudgets) == 0 {
		return
	}
	resets := make(map[string]*configstoreTables.TableBudget, len(resetBudgets))
	for _, b := range resetBudgets {
		if b != nil {
			resets[b.ID] = b
		}
	}
	// Update VKs that reference these budgets
	gs.virtualKeys.Range(func(key, value interface{}) bool {
		vk, ok := value.(*configstoreTables.TableVirtualKey)
		if !ok || vk == nil {
			return true // continue
		}
		vkMatch := false
		for i := range vk.Budgets {
			if resets[vk.Budgets[i].ID] != nil {
				vkMatch = true
				break
			}
		}
		pcMatch := false
		for i := range vk.ProviderConfigs {
			for j := range vk.ProviderConfigs[i].Budgets {
				if resets[vk.ProviderConfigs[i].Budgets[j].ID] != nil {
					pcMatch = true
					break
				}
			}
			if pcMatch {
				break
			}
		}
		if !vkMatch && !pcMatch {
			return true // continue
		}
		clone := *vk
		if vkMatch {
			clone.Budgets = append([]configstoreTables.TableBudget(nil), vk.Budgets...)
			for i := range clone.Budgets {
				if b := resets[clone.Budgets[i].ID]; b != nil {
					clone.Budgets[i] = *b
				}
			}
		}
		if pcMatch {
			clone.ProviderConfigs = append([]configstoreTables.TableVirtualKeyProviderConfig(nil), vk.ProviderConfigs...)
			for i := range clone.ProviderConfigs {
				matched := false
				for j := range clone.ProviderConfigs[i].Budgets {
					if resets[clone.ProviderConfigs[i].Budgets[j].ID] != nil {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
				budgets := append([]configstoreTables.TableBudget(nil), clone.ProviderConfigs[i].Budgets...)
				for j := range budgets {
					if b := resets[budgets[j].ID]; b != nil {
						budgets[j] = *b
					}
				}
				clone.ProviderConfigs[i].Budgets = budgets
			}
		}
		gs.storeVirtualKey(key.(string), &clone)
		return true // continue
	})
	// Update teams that reference these budgets
	gs.teams.Range(func(key, value interface{}) bool {
		team, ok := value.(*configstoreTables.TableTeam)
		if !ok || team == nil {
			return true // continue
		}
		matched := false
		for i := range team.Budgets {
			if resets[team.Budgets[i].ID] != nil {
				matched = true
				break
			}
		}
		if !matched {
			return true // continue
		}
		clone := *team
		clone.Budgets = append([]configstoreTables.TableBudget(nil), team.Budgets...)
		for i := range clone.Budgets {
			if b := resets[clone.Budgets[i].ID]; b != nil {
				clone.Budgets[i] = *b
			}
		}
		gs.teams.Store(key, &clone)
		return true // continue
	})
	// Update customers that own these budgets
	gs.customers.Range(func(key, value interface{}) bool {
		customer, ok := value.(*configstoreTables.TableCustomer)
		if !ok || customer == nil {
			return true // continue
		}
		matched := false
		for i := range customer.Budgets {
			if resets[customer.Budgets[i].ID] != nil {
				matched = true
				break
			}
		}
		if !matched {
			return true // continue
		}
		clone := *customer
		clone.Budgets = append([]configstoreTables.TableBudget(nil), customer.Budgets...)
		for i := range clone.Budgets {
			if b := resets[clone.Budgets[i].ID]; b != nil {
				clone.Budgets[i] = *b
			}
		}
		gs.customers.Store(key, &clone)
		return true // continue
	})
}

// updateRateLimitReferences updates all VKs, teams, customers and provider configs
// that reference any of the reset rate limits. It makes ONE pass over each owner
// map regardless of how many rate limits reset, and clones an owner only after a
// match, so a background sweep costs O(owners + resets) instead of
// O(owners x resets) with a clone per owner per reset. Essential at large key
// counts; the contract is pinned by TestBackgroundResetReferenceRefreshScales.
func (gs *LocalGovernanceStore) updateRateLimitReferences(ctx context.Context, resetRateLimits []*configstoreTables.TableRateLimit) {
	if len(resetRateLimits) == 0 {
		return
	}
	resets := make(map[string]*configstoreTables.TableRateLimit, len(resetRateLimits))
	for _, rl := range resetRateLimits {
		if rl != nil {
			resets[rl.ID] = rl
		}
	}
	// Update VKs that reference these rate limits
	gs.virtualKeys.Range(func(key, value interface{}) bool {
		vk, ok := value.(*configstoreTables.TableVirtualKey)
		if !ok || vk == nil {
			return true // continue
		}
		vkMatch := vk.RateLimitID != nil && resets[*vk.RateLimitID] != nil
		pcMatch := false
		for i := range vk.ProviderConfigs {
			if id := vk.ProviderConfigs[i].RateLimitID; id != nil && resets[*id] != nil {
				pcMatch = true
				break
			}
		}
		if !vkMatch && !pcMatch {
			return true // continue
		}
		clone := *vk
		if vkMatch {
			clone.RateLimit = resets[*vk.RateLimitID]
		}
		if pcMatch {
			clone.ProviderConfigs = append([]configstoreTables.TableVirtualKeyProviderConfig(nil), vk.ProviderConfigs...)
			for i := range clone.ProviderConfigs {
				if id := clone.ProviderConfigs[i].RateLimitID; id != nil {
					if rl := resets[*id]; rl != nil {
						clone.ProviderConfigs[i].RateLimit = rl
					}
				}
			}
		}
		gs.storeVirtualKey(key.(string), &clone)
		return true // continue
	})
	// Update teams that reference these rate limits
	gs.teams.Range(func(key, value interface{}) bool {
		team, ok := value.(*configstoreTables.TableTeam)
		if !ok || team == nil {
			return true // continue
		}
		if team.RateLimitID != nil {
			if rl := resets[*team.RateLimitID]; rl != nil {
				clone := *team
				clone.RateLimit = rl
				gs.teams.Store(key, &clone)
			}
		}
		return true // continue
	})
	// Update customers that reference these rate limits
	gs.customers.Range(func(key, value interface{}) bool {
		customer, ok := value.(*configstoreTables.TableCustomer)
		if !ok || customer == nil {
			return true // continue
		}
		if customer.RateLimitID != nil {
			if rl := resets[*customer.RateLimitID]; rl != nil {
				clone := *customer
				clone.RateLimit = rl
				gs.customers.Store(key, &clone)
			}
		}
		return true // continue
	})
}

// GetBudgetAndRateLimitStatus implements GovernanceStore. It gathers what the request would answer to
// for the pair, from the store and the access on its grant, and reports the fullest budget and rate
// limit among them. A request with no access still answers to the deployment's own limits.
func (gs *LocalGovernanceStore) GetBudgetAndRateLimitStatus(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, budgetBaselines map[string]float64, tokenBaselines map[string]int64, requestBaselines map[string]int64) *BudgetAndRateLimitStatus {
	budgets, rateLimits := gatherLimits(ctx, gs, ctx.Grant().Access(), provider, model)
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
// Exported so a store that answers ResolvePermits from something other than a key can ask the same
// narrow question over its own baseline-aware checks, which this package's cannot answer for it. It
// exists so load balancing can ask that narrower question without naming a model. Asking
// ProviderAndModelLimits with an empty one would answer the same, but it would say the model is
// unknown where in fact it is known and deliberately not consulted, and the obvious repair to that
// is to pass the model, which quietly puts model limits back into load balancing.
func (gs *LocalGovernanceStore) GlobalProviderLimits(ctx context.Context, provider schemas.ModelProvider) (budgets []schemas.Limit, rateLimits []schemas.Limit) {
	providerName := string(provider)
	if providerName == "" {
		return nil, nil
	}
	value, exists := gs.providers.Load(providerName)
	if !exists || value == nil {
		return nil, nil
	}
	providerTable, ok := value.(*configstoreTables.TableProvider)
	if !ok || providerTable == nil {
		return nil, nil
	}
	return grant.LimitsHeldBy(grant.LimitHolderProvider, providerName, providerName, providerName, "", idOrEmpty(providerTable.BudgetID)...),
		grant.LimitsHeldBy(grant.LimitHolderProvider, providerName, providerName, providerName, "", idOrEmpty(providerTable.RateLimitID)...)
}

// ProviderAndModelLimits returns the limits that apply only once a request's provider and model are known:
// the deployment's own provider limits, its global model-config limits, and the grant holder's
// per-model ones.
//
// These are resolved per attempt rather than carried on the grant. A grant is built before a
// provider is chosen, and an attempt can fail over to a different provider or have its model
// rewritten, so which of them apply is not a fact about the holder; it is a fact about the attempt.
// Resolving them here costs four keyed lookups per scope instead of a scan over every model config
// the deployment has.
//
// The caller gathers these into the attempt's settled limits rather than storing them anywhere
// longer-lived, so one attempt's limits are never charged to the next.
func (gs *LocalGovernanceStore) ProviderAndModelLimits(ctx context.Context, permit schemas.Permit, provider schemas.ModelProvider, model string) (budgets []schemas.Limit, rateLimits []schemas.Limit) {
	providerName := string(provider)
	budgets, rateLimits = gs.GlobalProviderLimits(ctx, provider)

	// Model configs, most specific tier first, in every scope that applies: the deployment's own,
	// then the grant holder's. collectModelConfigsFor walks the four tiers: exact pair, exact
	// model on any provider, any model on this provider, any model anywhere.
	//
	// A request with no model still walks the scopes: only the wildcard tiers can match then,
	// and an all-models budget covers a request whatever it runs. Batch creation is the case
	// that carries no top-level model, and skipping the walk there left the unscoped budget a
	// key was created with unchecked and never bumped.
	var providerArg *string
	if providerName != "" {
		providerArg = &providerName
	}
	for _, scope := range modelConfigScopesFor(ctx, permit) {
		for _, mc := range gs.collectModelConfigsFor(ctx, scope.name, scope.id, model, providerArg) {
			if mc == nil {
				continue
			}
			budgets = append(budgets, grant.LimitsHeldBy(scope.kind, mc.ID, modelConfigDisplayName(mc), providerName, model, budgetIDsOf(mc.Budgets)...)...)
			rateLimits = append(rateLimits, grant.LimitsHeldBy(scope.kind, mc.ID, modelConfigDisplayName(mc), providerName, model, idOrEmpty(mc.RateLimitID)...)...)
		}
	}
	return budgets, rateLimits
}

// limitScope is one model-config scope to resolve limits from, with the holder kind its limits are
// attributed to.
type limitScope struct {
	name string
	id   string
	kind grant.LimitHolderKind
}

// modelConfigScopesFor lists the model-config scopes an attempt is subject to: the deployment's
// global one, the user's when the request was made as one, and the permit holder's own when the
// permit came from a holder that has one.
//
// The holder's scope is keyed by the permit's identity rather than by a virtual key, so a permit
// from any other kind of holder is looked up the same way without this needing to know what it is.
func modelConfigScopesFor(ctx context.Context, permit schemas.Permit) []limitScope {
	scopes := []limitScope{{
		name: configstoreTables.ModelConfigScopeGlobal,
		kind: grant.LimitHolderModelConfig,
	}}
	// The user the request was made as, when there is one. A user is not the permit holder, since a
	// request can be made as a user through a key, so downstream-registered scopes keyed off it
	// (e.g. enterprise's per-access-profile per-model budgets — see RegisterExtraScopedIDsResolver)
	// are scopes of their own rather than derived from the permit.
	userID, _ := ctx.Value(schemas.BifrostContextKeyUserID).(string)
	vkID := ""
	if permit != nil && permit.Type() == string(grant.PermitVirtualKey) {
		vkID = permit.ID()
	}
	for _, extra := range collectExtraScopedIDs(ctx, vkID, userID) {
		scopes = append(scopes, limitScope{
			name: extra.Scope,
			id:   extra.ScopeID,
			kind: grant.LimitHolderUserModelConfig,
		})
	}
	if permit == nil || permit.ID() == "" {
		return scopes
	}
	return append(scopes, limitScope{
		name: modelConfigScopeFor(permit.Type()),
		id:   permit.ID(),
		kind: scopedModelConfigKind(permit.Type()),
	})
}

// modelConfigScopeFor is the scope a permit holder's own model configs are stored under.
//
// A permit type and a model-config scope are different vocabularies: the scope is a persisted
// column, the type names what resolved the permit, and for virtual keys the two spell it
// differently. The translation is explicit because casting one to the other silently finds
// nothing: the lookup is keyed by scope name, so a near-miss reads as "this holder configured no
// model limits" rather than as an error.
func modelConfigScopeFor(permitType string) string {
	switch permitType {
	case string(grant.PermitVirtualKey):
		return configstoreTables.ModelConfigScopeVirtualKey
	default:
		return permitType
	}
}

// scopedModelConfigKind is the kind a permit holder's own per-model limits are attributed to, so a
// refusal can say whose model limit ran out.
func scopedModelConfigKind(permitType string) grant.LimitHolderKind {
	switch permitType {
	case string(grant.PermitVirtualKey):
		return grant.LimitHolderVirtualKeyModelConfig
	case string(grant.PermitProject):
		return grant.LimitHolderProjectModelConfig
	default:
		return grant.LimitHolderUserModelConfig
	}
}

// modelConfigDisplayName is what a refusal calls a model config's limit.
func modelConfigDisplayName(mc *configstoreTables.TableModelConfig) string {
	if mc.Provider != nil && *mc.Provider != "" {
		return fmt.Sprintf("%s on %s", mc.ModelName, *mc.Provider)
	}
	return mc.ModelName
}

// budgetIDsOf names a set of budget rows.
func budgetIDsOf(rows []configstoreTables.TableBudget) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

// idOrEmpty reads an optional identifier as the list LimitsHeldBy takes.
func idOrEmpty(id *string) []string {
	if id == nil {
		return nil
	}
	return []string{*id}
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
