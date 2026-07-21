// Package configstore provides a persistent configuration store for Bifrost.
package configstore

import (
	"context"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/objectstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"gorm.io/gorm"
)

// VirtualKeyQueryParams holds pagination, filtering, and search parameters for virtual key queries.
type VirtualKeyQueryParams struct {
	Limit                              int
	Offset                             int
	Search                             string
	CustomerID                         string
	TeamID                             string
	SortBy                             string // name, budget_spent, created_at, status (default: created_at)
	Order                              string // asc, desc (default: asc)
	Export                             bool   // When true, skip default pagination limits (caller controls limit)
	ExcludeAccessProfileManagedVirtual bool   // When true, exclude VKs managed through enterprise access profiles
	ExcludeAssignedVirtualKeys         bool   // When true, exclude VKs that already have any user assignment
	ForUserAssignment                  bool   // When true, exclude VKs assigned to any entity (team, customer, access profile, or user) — intended for user-assignment pickers
}

// ModelConfigsQueryParams holds pagination, filtering, and search parameters for model configs queries.
type ModelConfigsQueryParams struct {
	Limit    int
	Offset   int
	Search   string
	Scope    string // optional; filters to an exact scope value (e.g. "global", "virtual_key")
	Provider string // optional; filters to an exact provider value (e.g. "openai")
}

// SkillListQueryParams holds pagination, filtering, and search parameters for skill repository queries.
type SkillListQueryParams struct {
	Limit  int
	Offset int
	Search string
	SortBy string // name, updated_at, created_at (default: created_at)
	Order  string // asc, desc (default: desc)
}

type SkillVersionListQueryParams struct {
	Limit  int
	Offset int
	SortBy string // version, created_at (default: created_at)
	Order  string // asc, desc (default: desc)
	Search string // substring match on the version string (optional)
}

// RoutingRulesQueryParams holds pagination, filtering, and search parameters for routing rules queries.
type RoutingRulesQueryParams struct {
	Limit  int
	Offset int
	Search string
}

// WebhookEndpointsQueryParams holds pagination, filtering, and search
// parameters for webhook endpoint queries.
type WebhookEndpointsQueryParams struct {
	Limit    int
	Offset   int
	Search   string   // matches name or url (case-insensitive)
	Events   []string // endpoints subscribed to any of these events, OR semantics
	Disabled *bool    // nil = no filter; true/false = filter on disabled
}

// MCPClientsQueryParams holds pagination, filtering, and search parameters for MCP client queries.
type MCPClientsQueryParams struct {
	Limit            int
	Offset           int
	Search           string   // matches name (case-insensitive)
	ClientID         string   // exact client_id match
	ConnectionTypes  []string // exact connection_type filter(s), OR semantics (http | sse | stdio)
	AuthTypes        []string // exact auth_type filter(s), OR semantics (none | headers | oauth | per_user_oauth | per_user_headers)
	IsCodeModeClient *bool    // nil = no filter; true/false = filter on is_code_mode_client
	Disabled         *bool    // nil = no filter; true/false = filter on disabled

	// Runtime connection-state filter. State is not persisted, so the caller
	// resolves the set of currently-connected client_ids from the engine and
	// passes it here. StateInclude nil = no filter; true = client_id IN set
	// (connected); false = client_id NOT IN set (disconnected).
	StateClientIDs []string
	StateInclude   *bool

	// Virtual-key access filter (OR semantics within the group). When both are
	// set, a client matches if it is open to all VKs OR explicitly assigned to
	// one of VirtualKeyIDs.
	OnlyAllVirtualKeys bool     // include clients with allow_on_all_virtual_keys=true
	VirtualKeyIDs      []string // include clients explicitly assigned to any of these VK IDs
}

// MCPLibraryQueryParams holds pagination, filtering, search, and sort
// parameters for MCP library catalog queries. All fields are optional — an
// empty struct returns the first default-sized page ordered by name.
type MCPLibraryQueryParams struct {
	Limit           int
	Offset          int
	Search          string   // matches name/description/publisher (case-insensitive)
	Categories      []string // exact category filter(s), OR semantics
	ConnectionTypes []string // exact connection_type filter(s) (http | stdio | sse)
	AuthTypes       []string // exact auth_type filter(s)
	Tags            []string // match rows carrying any of these tags
	SortBy          string   // name, category, publisher, created_at, updated_at (default: name)
	Order           string   // asc, desc (default: asc)
}

// MCPLibraryFilterData holds the distinct facet values surfaced by the filter
// sidebar on the MCP library page. Populated via GetMCPLibraryFilterData.
type MCPLibraryFilterData struct {
	Categories      []string `json:"categories"`
	ConnectionTypes []string `json:"connection_types"`
	AuthTypes       []string `json:"auth_types"`
	Tags            []string `json:"tags"`
}

// TeamsQueryParams holds pagination, filtering, and search parameters for team queries.
type TeamsQueryParams struct {
	Limit      int
	Offset     int
	Search     string
	CustomerID string
}

// CustomersQueryParams holds pagination, filtering, and search parameters for customer queries.
type CustomersQueryParams struct {
	Limit  int
	Offset int
	Search string
}

// MCPSessionsFilterParams is the filter set shared across the four
// MCP-sessions list methods (oauth tokens, pending oauth sessions,
// per-user header credentials, pending per-user header flows).
//
// Pagination is intentionally omitted: the four sources are merged and
// de-duped in the handler before the page slice, so per-table LIMIT/OFFSET
// would not compose into a correct global page. These methods are filter
// pushdown only; the handler paginates the merged result.
//
// Search is a case-insensitive substring matched against the MCP client's
// name/client_id, the row's identity columns (user_id, session_id), and
// the virtual key's id/name (joined). Empty filter slices match all
// values for that field.
type MCPSessionsFilterParams struct {
	Search       string
	Statuses     []string
	AuthModes    []string // matched against auth_mode (tokens, credentials) or flow_mode (sessions, flows)
	MCPClientIDs []string
	// Identity exact-matches a single resolved identity value against any of
	// the row's identity columns (user_id, virtual_key_id, session_id). Unlike
	// Search it is not a substring match — it pins the list to exactly one
	// user, virtual key, or session. Typically paired with AuthModes to scope
	// to that identity's rows for a known mode.
	Identity string
	// MatchedUserIDs is an optional set of user_ids that should be treated
	// as a positive search hit alongside Search. Callers that maintain a
	// user directory (display names, emails) resolve the search string
	// against that directory and pass the resulting user_ids in here so
	// rows owned by those users surface even though the search columns on
	// these tables only carry the opaque user_id. When non-empty the
	// filter ORs `{table}.user_id IN (matched)` into the search WHERE.
	// Only consulted when Search is non-empty.
	MatchedUserIDs []string
}

// OAuth2SessionsQueryParams holds the filters + pagination for the OAuth2
// grants list (Connected Clients UI). Search is a case-insensitive substring
// matched against the client name/id, the bound identity (bf_sub), and the
// joined virtual key name. Modes filters on bf_mode (user/vk/session); an
// empty slice matches all. Limit/Offset paginate the filtered result in SQL.
type OAuth2SessionsQueryParams struct {
	Search string
	Modes  []string
	Limit  int
	Offset int
}

// PricingOverrideFilters holds the filters for pricing overrides.
type PricingOverrideFilters struct {
	ScopeKind     *string
	VirtualKeyID  *string
	ProviderID    *string
	ProviderKeyID *string
}

// PricingOverridesQueryParams holds pagination, filtering, and search parameters for pricing override queries.
type PricingOverridesQueryParams struct {
	Limit         int
	Offset        int
	Search        string
	ScopeKind     *string
	VirtualKeyID  *string
	ProviderID    *string
	ProviderKeyID *string
}

// ConfigStore is the interface for the config store.
type ConfigStore interface {
	// Health check
	Ping(ctx context.Context) error

	// Encryption
	EncryptPlaintextRows(ctx context.Context) error

	// Client config CRUD
	UpdateClientConfig(ctx context.Context, config *ClientConfig) error
	GetClientConfig(ctx context.Context) (*ClientConfig, error)
	// Client config metadata (UI/admin preferences blob — bypasses config.json sync)
	GetClientMetadata(ctx context.Context) (map[string]any, error)
	UpdateClientMetadata(ctx context.Context, patch map[string]any) error

	// Framework config CRUD
	UpdateFrameworkConfig(ctx context.Context, config *tables.TableFrameworkConfig) error
	GetFrameworkConfig(ctx context.Context) (*tables.TableFrameworkConfig, error)

	// Feature flag overrides: list + upsert. Flags themselves are
	// code-declared (via featureflags.Register); only the toggle state
	// lives here. There is intentionally no Delete: removing a flag means
	// removing its Register() call in code.
	ListFeatureFlags(ctx context.Context) ([]tables.TableFeatureFlag, error)
	UpsertFeatureFlag(ctx context.Context, id string, enabled bool, updatedAt int64) error

	// Provider config CRUD
	UpdateProvidersConfig(ctx context.Context, providers map[schemas.ModelProvider]ProviderConfig, tx ...*gorm.DB) error
	AddProvider(ctx context.Context, provider schemas.ModelProvider, config ProviderConfig, tx ...*gorm.DB) error
	UpdateProvider(ctx context.Context, provider schemas.ModelProvider, config ProviderConfig, tx ...*gorm.DB) error
	DeleteProvider(ctx context.Context, provider schemas.ModelProvider, tx ...*gorm.DB) error
	GetProvidersConfig(ctx context.Context) (map[schemas.ModelProvider]ProviderConfig, error)
	GetProviderConfig(ctx context.Context, provider schemas.ModelProvider) (*ProviderConfig, error)
	GetProviderKeys(ctx context.Context, provider schemas.ModelProvider) ([]schemas.Key, error)
	GetProviderKey(ctx context.Context, provider schemas.ModelProvider, keyID string) (*schemas.Key, error)
	CreateProviderKey(ctx context.Context, provider schemas.ModelProvider, key schemas.Key, tx ...*gorm.DB) error
	UpdateProviderKey(ctx context.Context, provider schemas.ModelProvider, keyID string, key schemas.Key, tx ...*gorm.DB) error
	DeleteProviderKey(ctx context.Context, provider schemas.ModelProvider, keyID string, tx ...*gorm.DB) error
	GetProviders(ctx context.Context) ([]tables.TableProvider, error)
	GetProvider(ctx context.Context, provider schemas.ModelProvider) (*tables.TableProvider, error)
	UpdateStatus(ctx context.Context, provider schemas.ModelProvider, keyID string, status, errorMsg string) error

	// MCP config CRUD
	GetMCPConfig(ctx context.Context) (*schemas.MCPConfig, error)
	GetMCPClientByID(ctx context.Context, id string) (*tables.TableMCPClient, error)
	GetMCPClientConfigByID(ctx context.Context, id string) (*schemas.MCPClientConfig, error)
	GetMCPClientByName(ctx context.Context, name string) (*tables.TableMCPClient, error)
	GetMCPClientByOauthConfigID(ctx context.Context, oauthConfigID string) (*tables.TableMCPClient, error)
	UpdateMCPClientOAuthConfigID(ctx context.Context, clientID string, oauthConfigID *string) error
	ClearMCPClientPendingOAuthConfig(ctx context.Context, clientID string) error
	GetMCPClientsPaginated(ctx context.Context, params MCPClientsQueryParams) ([]tables.TableMCPClient, int64, error)
	CreateMCPClientConfig(ctx context.Context, clientConfig *schemas.MCPClientConfig) error
	UpdateMCPClientConfig(ctx context.Context, id string, clientConfig *tables.TableMCPClient) error
	DeleteMCPClientConfig(ctx context.Context, id string) error

	// MCP library catalog (synced + org-custom)
	GetMCPLibraryPaginated(ctx context.Context, params MCPLibraryQueryParams) ([]tables.TableMCPLibrary, int64, error)
	GetMCPLibraryFilterData(ctx context.Context) (*MCPLibraryFilterData, error)
	UpsertMCPLibraryEntry(ctx context.Context, entry *tables.TableMCPLibrary, tx ...*gorm.DB) error
	// CreateCustomMCPLibraryEntry inserts an org-internal ("custom") library row.
	// Returns ErrAlreadyExists when the slug collides with an existing entry.
	CreateCustomMCPLibraryEntry(ctx context.Context, entry *tables.TableMCPLibrary) error
	// SoftDeleteMCPLibraryEntry tombstones a library row by ID (sets deleted_at)
	// so it is hidden from listings and never resurrected by the remote sync.
	SoftDeleteMCPLibraryEntry(ctx context.Context, id uint) error
	// DeleteMCPLibraryEntry removes a library row by ID, hard-deleting "custom"
	// rows (freeing their slug for re-add) and tombstoning "remote" rows.
	DeleteMCPLibraryEntry(ctx context.Context, id uint) error
	// GetProtectedMCPLibrarySlugs returns the slugs the remote sync must not
	// overwrite or recreate: custom rows and soft-deleted (tombstoned) rows.
	GetProtectedMCPLibrarySlugs(ctx context.Context) ([]string, error)

	// Vector store config CRUD
	UpdateVectorStoreConfig(ctx context.Context, config *vectorstore.Config) error
	GetVectorStoreConfig(ctx context.Context) (*vectorstore.Config, error)

	// Logs store config CRUD
	UpdateLogsStoreConfig(ctx context.Context, config *logstore.Config) error
	GetLogsStoreConfig(ctx context.Context) (*logstore.Config, error)

	// Config CRUD
	GetConfig(ctx context.Context, key string) (*tables.TableGovernanceConfig, error)
	UpdateConfig(ctx context.Context, config *tables.TableGovernanceConfig, tx ...*gorm.DB) error
	// GetComplexityAnalyzerConfig retrieves the persisted analyzer config, if configured.
	GetComplexityAnalyzerConfig(ctx context.Context) (*ComplexityAnalyzerConfig, error)
	// UpdateComplexityAnalyzerConfig persists the normalized analyzer config.
	UpdateComplexityAnalyzerConfig(ctx context.Context, config *ComplexityAnalyzerConfig, tx ...*gorm.DB) error

	// Plugins CRUD
	GetPlugins(ctx context.Context) ([]*tables.TablePlugin, error)
	GetPlugin(ctx context.Context, name string) (*tables.TablePlugin, error)
	CreatePlugin(ctx context.Context, plugin *tables.TablePlugin, tx ...*gorm.DB) error
	UpsertPlugin(ctx context.Context, plugin *tables.TablePlugin, tx ...*gorm.DB) error
	UpdatePlugin(ctx context.Context, plugin *tables.TablePlugin, tx ...*gorm.DB) error
	DeletePlugin(ctx context.Context, name string, tx ...*gorm.DB) error

	// Governance config CRUD
	GetVirtualKeys(ctx context.Context) ([]tables.TableVirtualKey, error)
	GetVirtualKeysPaginated(ctx context.Context, params VirtualKeyQueryParams) ([]tables.TableVirtualKey, int64, error)
	GetRedactedVirtualKeys(ctx context.Context, ids []string) ([]tables.TableVirtualKey, error) // leave ids empty to get all
	GetVirtualKey(ctx context.Context, id string) (*tables.TableVirtualKey, error)
	GetVirtualKeyByValue(ctx context.Context, value string) (*tables.TableVirtualKey, error)
	GetVirtualKeyQuotaByValue(ctx context.Context, value string) (*tables.TableVirtualKey, error)
	CreateVirtualKey(ctx context.Context, virtualKey *tables.TableVirtualKey, tx ...*gorm.DB) error
	UpdateVirtualKey(ctx context.Context, virtualKey *tables.TableVirtualKey, tx ...*gorm.DB) error
	DeleteVirtualKey(ctx context.Context, id string, tx ...*gorm.DB) error

	// Virtual key provider config CRUD
	GetVirtualKeyProviderConfigs(ctx context.Context, virtualKeyID string) ([]tables.TableVirtualKeyProviderConfig, error)
	CreateVirtualKeyProviderConfig(ctx context.Context, virtualKeyProviderConfig *tables.TableVirtualKeyProviderConfig, tx ...*gorm.DB) error
	UpdateVirtualKeyProviderConfig(ctx context.Context, virtualKeyProviderConfig *tables.TableVirtualKeyProviderConfig, tx ...*gorm.DB) error
	DeleteVirtualKeyProviderConfig(ctx context.Context, id uint, tx ...*gorm.DB) error

	// Virtual key MCP config CRUD
	GetVirtualKeyMCPConfigs(ctx context.Context, virtualKeyID string) ([]tables.TableVirtualKeyMCPConfig, error)
	GetVirtualKeyMCPConfigsByMCPClientID(ctx context.Context, mcpClientID uint) ([]tables.TableVirtualKeyMCPConfig, error)
	GetVirtualKeyMCPConfigsByMCPClientIDs(ctx context.Context, mcpClientIDs []uint) ([]tables.TableVirtualKeyMCPConfig, error)
	GetVirtualKeyMCPConfigsByMCPClientStringIDs(ctx context.Context, clientIDs []string) ([]tables.TableVirtualKeyMCPConfig, error)
	CreateVirtualKeyMCPConfig(ctx context.Context, virtualKeyMCPConfig *tables.TableVirtualKeyMCPConfig, tx ...*gorm.DB) error
	UpdateVirtualKeyMCPConfig(ctx context.Context, virtualKeyMCPConfig *tables.TableVirtualKeyMCPConfig, tx ...*gorm.DB) error
	DeleteVirtualKeyMCPConfig(ctx context.Context, id uint, tx ...*gorm.DB) error

	// Team CRUD
	GetTeams(ctx context.Context, customerID string) ([]tables.TableTeam, error)
	GetTeamsPaginated(ctx context.Context, params TeamsQueryParams) ([]tables.TableTeam, int64, error)
	GetTeam(ctx context.Context, id string) (*tables.TableTeam, error)
	GetTeamByName(ctx context.Context, name string, customerID string) (*tables.TableTeam, error)
	GetTeamBySourceID(ctx context.Context, sourceID string) (*tables.TableTeam, error)
	CreateTeam(ctx context.Context, team *tables.TableTeam, tx ...*gorm.DB) error
	UpdateTeam(ctx context.Context, team *tables.TableTeam, tx ...*gorm.DB) error
	DeleteTeam(ctx context.Context, id string, tx ...*gorm.DB) error

	// Customer CRUD
	GetCustomers(ctx context.Context) ([]tables.TableCustomer, error)
	GetCustomersPaginated(ctx context.Context, params CustomersQueryParams) ([]tables.TableCustomer, int64, error)
	GetCustomer(ctx context.Context, id string) (*tables.TableCustomer, error)
	CreateCustomer(ctx context.Context, customer *tables.TableCustomer, tx ...*gorm.DB) error
	UpdateCustomer(ctx context.Context, customer *tables.TableCustomer, tx ...*gorm.DB) error
	DeleteCustomer(ctx context.Context, id string, tx ...*gorm.DB) error

	// Rate limit CRUD
	GetRateLimits(ctx context.Context) ([]tables.TableRateLimit, error)
	GetRateLimit(ctx context.Context, id string, tx ...*gorm.DB) (*tables.TableRateLimit, error)
	CreateRateLimit(ctx context.Context, rateLimit *tables.TableRateLimit, tx ...*gorm.DB) error
	UpdateRateLimit(ctx context.Context, rateLimit *tables.TableRateLimit, tx ...*gorm.DB) error
	UpdateRateLimits(ctx context.Context, rateLimits []*tables.TableRateLimit, tx ...*gorm.DB) error
	DeleteRateLimit(ctx context.Context, id string, tx ...*gorm.DB) error

	// Budget CRUD
	GetBudgets(ctx context.Context) ([]tables.TableBudget, error)
	GetBudget(ctx context.Context, id string, tx ...*gorm.DB) (*tables.TableBudget, error)
	CreateBudget(ctx context.Context, budget *tables.TableBudget, tx ...*gorm.DB) error
	UpdateBudget(ctx context.Context, budget *tables.TableBudget, tx ...*gorm.DB) error
	UpdateBudgets(ctx context.Context, budgets []*tables.TableBudget, tx ...*gorm.DB) error
	DeleteBudget(ctx context.Context, id string, tx ...*gorm.DB) error
	UpdateBudgetUsage(ctx context.Context, id string, currentUsage float64, tx ...*gorm.DB) error
	UpdateRateLimitUsage(ctx context.Context, id string, tokenCurrentUsage int64, requestCurrentUsage int64, tx ...*gorm.DB) error

	// Routing Rules CRUD
	GetRoutingRules(ctx context.Context) ([]tables.TableRoutingRule, error)
	GetRoutingRulesByScope(ctx context.Context, scope string, scopeID string) ([]tables.TableRoutingRule, error)
	GetRoutingRule(ctx context.Context, id string) (*tables.TableRoutingRule, error)
	GetRedactedRoutingRules(ctx context.Context, ids []string) ([]tables.TableRoutingRule, error) // leave ids empty to get all
	GetRoutingRulesPaginated(ctx context.Context, params RoutingRulesQueryParams) ([]tables.TableRoutingRule, int64, error)
	CreateRoutingRule(ctx context.Context, rule *tables.TableRoutingRule, tx ...*gorm.DB) error
	UpdateRoutingRule(ctx context.Context, rule *tables.TableRoutingRule, tx ...*gorm.DB) error
	DeleteRoutingRule(ctx context.Context, id string, tx ...*gorm.DB) error

	// Model config CRUD
	GetModelConfigs(ctx context.Context) ([]tables.TableModelConfig, error)
	GetModelConfigsByScopeAndScopeIDs(ctx context.Context, scope string, scopeIDs []string) ([]tables.TableModelConfig, error)
	GetProviderGovernanceModelConfigs(ctx context.Context) ([]tables.TableModelConfig, error)
	GetModelConfigsPaginated(ctx context.Context, params ModelConfigsQueryParams) ([]tables.TableModelConfig, int64, error)
	GetModelConfig(ctx context.Context, scope string, scopeID *string, modelName string, provider *string) (*tables.TableModelConfig, error)
	GetModelConfigByID(ctx context.Context, id string) (*tables.TableModelConfig, error)
	CreateModelConfig(ctx context.Context, modelConfig *tables.TableModelConfig, tx ...*gorm.DB) error
	UpdateModelConfig(ctx context.Context, modelConfig *tables.TableModelConfig, tx ...*gorm.DB) error
	UpdateModelConfigs(ctx context.Context, modelConfigs []*tables.TableModelConfig, tx ...*gorm.DB) error
	DeleteModelConfig(ctx context.Context, id string, tx ...*gorm.DB) error
	// DeleteModelConfigsForScope deletes all model configs (and their owned budgets/rate-limits) for a scope owner. Must run inside the owner-delete transaction.
	DeleteModelConfigsForScope(ctx context.Context, tx *gorm.DB, scope, scopeID string) error

	// Governance config CRUD
	GetGovernanceConfig(ctx context.Context) (*GovernanceConfig, error)

	// Auth config CRUD
	GetAuthConfig(ctx context.Context) (*AuthConfig, error)
	UpdateAuthConfig(ctx context.Context, config *AuthConfig) error

	// Proxy config CRUD
	GetProxyConfig(ctx context.Context) (*tables.GlobalProxyConfig, error)
	UpdateProxyConfig(ctx context.Context, config *tables.GlobalProxyConfig) error

	// Restart required config CRUD
	GetRestartRequiredConfig(ctx context.Context) (*tables.RestartRequiredConfig, error)
	SetRestartRequiredConfig(ctx context.Context, config *tables.RestartRequiredConfig) error
	ClearRestartRequiredConfig(ctx context.Context) error

	// Session CRUD
	GetSession(ctx context.Context, token string) (*tables.SessionsTable, error)
	CreateSession(ctx context.Context, session *tables.SessionsTable) error
	DeleteSession(ctx context.Context, token string) error
	FlushSessions(ctx context.Context) error

	// Temp token CRUD
	CreateTempToken(ctx context.Context, token *tables.TempToken, tx ...*gorm.DB) error
	GetTempTokenByHash(ctx context.Context, tokenHash string) (*tables.TempToken, error)
	// DeleteTempTokensByResourceID removes every row matching (scope, resource_id).
	// Used by lifecycle owners (e.g. OAuth provider on flow termination) to burn
	// the link as soon as the work it authorized is finished.
	DeleteTempTokensByResourceID(ctx context.Context, scope, resourceID string, tx ...*gorm.DB) (int64, error)
	DeleteExpiredTempTokens(ctx context.Context, before time.Time) (int64, error)

	// Model pricing CRUD
	GetModelPrices(ctx context.Context) ([]tables.TableModelPricing, error)
	UpsertModelPrices(ctx context.Context, pricing *tables.TableModelPricing, tx ...*gorm.DB) error
	UpsertModelPricesBatch(ctx context.Context, pricing []tables.TableModelPricing, tx ...*gorm.DB) error
	DeleteModelPrices(ctx context.Context, tx ...*gorm.DB) error

	// UpsertModelPricingAttributes writes only the additional_attributes column
	// on the pricing rows keyed by (model, provider). Returns the number of
	// rows updated; 0 means no such pricing row exists.
	UpsertModelPricingAttributes(ctx context.Context, model, provider string, attrs map[string]string, tx ...*gorm.DB) (int64, error)

	// Governance pricing overrides CRUD
	GetPricingOverrides(ctx context.Context, filters PricingOverrideFilters) ([]tables.TablePricingOverride, error)
	GetPricingOverridesPaginated(ctx context.Context, params PricingOverridesQueryParams) ([]tables.TablePricingOverride, int64, error)
	GetPricingOverrideByID(ctx context.Context, id string) (*tables.TablePricingOverride, error)
	CreatePricingOverride(ctx context.Context, override *tables.TablePricingOverride, tx ...*gorm.DB) error
	UpdatePricingOverride(ctx context.Context, override *tables.TablePricingOverride, tx ...*gorm.DB) error
	DeletePricingOverride(ctx context.Context, id string, tx ...*gorm.DB) error

	// Model parameters
	GetModelParameters(ctx context.Context) ([]tables.TableModelParameters, error)
	GetModelParametersByModel(ctx context.Context, model string) (*tables.TableModelParameters, error)
	UpsertModelParameters(ctx context.Context, params *tables.TableModelParameters, tx ...*gorm.DB) error
	UpsertModelParametersBatch(ctx context.Context, params []tables.TableModelParameters, tx ...*gorm.DB) error

	// Key management
	GetKeysByIDs(ctx context.Context, ids []string) ([]tables.TableKey, error)
	GetKeysByProvider(ctx context.Context, provider string) ([]tables.TableKey, error)
	GetAllRedactedKeys(ctx context.Context, ids []string) ([]schemas.Key, error) // leave ids empty to get all

	// Generic transaction manager
	ExecuteTransaction(ctx context.Context, fn func(tx *gorm.DB) error) error

	// TryAcquireLock attempts to insert a lock row. Returns true if the lock was acquired.
	// If the lock already exists and is not expired, returns false.
	TryAcquireLock(ctx context.Context, lock *tables.TableDistributedLock) (bool, error)

	// GetLock retrieves a lock by its key. Returns nil if the lock doesn't exist.
	GetLock(ctx context.Context, lockKey string) (*tables.TableDistributedLock, error)

	// UpdateLockExpiry updates the expiration time for an existing lock.
	// Only succeeds if the holder ID matches the current lock holder.
	UpdateLockExpiry(ctx context.Context, lockKey, holderID string, expiresAt time.Time) error

	// ReleaseLock deletes a lock if the holder ID matches.
	// Returns true if the lock was released, false if it wasn't held by the given holder.
	ReleaseLock(ctx context.Context, lockKey, holderID string) (bool, error)

	// CleanupExpiredLockByKey atomically deletes a specific lock only if it has expired.
	// Returns true if an expired lock was deleted, false if the lock doesn't exist or hasn't expired.
	CleanupExpiredLockByKey(ctx context.Context, lockKey string) (bool, error)

	// CleanupExpiredLocks removes all locks that have expired.
	// Returns the number of locks cleaned up.
	CleanupExpiredLocks(ctx context.Context) (int64, error)

	// OAuth config CRUD
	GetOauthConfigByID(ctx context.Context, id string) (*tables.TableOauthConfig, error)
	GetOauthConfigsByIDs(ctx context.Context, ids []string) (map[string]*tables.TableOauthConfig, error)
	GetOauthConfigByState(ctx context.Context, state string) (*tables.TableOauthConfig, error)
	GetOauthConfigByTokenID(ctx context.Context, tokenID string) (*tables.TableOauthConfig, error)
	CreateOauthConfig(ctx context.Context, config *tables.TableOauthConfig) error
	UpdateOauthConfig(ctx context.Context, config *tables.TableOauthConfig) error

	// OAuth token CRUD
	GetOauthTokenByID(ctx context.Context, id string) (*tables.TableOauthToken, error)
	GetExpiringOauthTokens(ctx context.Context, before time.Time) ([]*tables.TableOauthToken, error)
	CreateOauthToken(ctx context.Context, token *tables.TableOauthToken) error
	UpdateOauthToken(ctx context.Context, token *tables.TableOauthToken) error
	DeleteOauthToken(ctx context.Context, id string) error

	// Per-user OAuth session CRUD
	GetOauthUserSessionByID(ctx context.Context, id string) (*tables.TableOauthUserSession, error)
	ClaimOauthUserSessionByState(ctx context.Context, state string) (*tables.TableOauthUserSession, error)
	// GetOauthUserSessionByModeIdentityAndMCPClient returns the canonical flow
	// row for an (identity, mcp_client) binding. Used at flow-init time as the
	// single source of truth: reauth updates this row in place rather than
	// inserting a new one. Returns (nil, nil) when no row exists.
	GetOauthUserSessionByModeIdentityAndMCPClient(ctx context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) (*tables.TableOauthUserSession, error)
	CreateOauthUserSession(ctx context.Context, session *tables.TableOauthUserSession) error
	UpdateOauthUserSession(ctx context.Context, session *tables.TableOauthUserSession) error

	// Per-user OAuth token CRUD
	// GetOauthUserTokenByMode looks up the active token row keyed by a single
	// identity dimension. Filters status='active'. identity is the user ID for
	// AuthModeUser, the VK row ID for AuthModeVK, and the session ID for
	// AuthModeSession.
	GetOauthUserTokenByMode(ctx context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) (*tables.TableOauthUserToken, error)
	CreateOauthUserToken(ctx context.Context, token *tables.TableOauthUserToken) error
	UpdateOauthUserToken(ctx context.Context, token *tables.TableOauthUserToken) error
	DeleteOauthUserToken(ctx context.Context, id string) error
	// DeleteOauthUserSession hard-deletes a single flow row by primary key.
	// Used by CompleteUserOAuthFlow on terminal transitions so completed,
	// failed, and expired-at-completion flows don't accumulate. The UI
	// treats 404 on flow-detail as "expired or completed".
	DeleteOauthUserSession(ctx context.Context, id string) error
	// DeleteOauthUserSessionsByModeIdentityAndMCPClient hard-deletes any flow
	// rows matching the given identity column + MCP client. Used by revoke
	// across all auth modes so subsequent OAuth init starts from a clean slate.
	DeleteOauthUserSessionsByModeIdentityAndMCPClient(ctx context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) error
	// MarkOauthUserTokenNeedsReauthByID flips status to 'needs_reauth'
	// on a single token row. Called by the refresh-failure path when
	// the upstream credential is permanently rejected: the row stays
	// (preserves audit + binding for re-auth), but is filtered from
	// active lookups so the next inference triggers a fresh OAuth
	// flow that upserts the row back to 'active'.
	MarkOauthUserTokenNeedsReauthByID(ctx context.Context, tokenID string) error
	// GetOauthUserTokenByID looks up a single token row by primary key.
	// Returns nil, nil when not found.
	GetOauthUserTokenByID(ctx context.Context, id string) (*tables.TableOauthUserToken, error)
	// ListOauthUserTokens returns token rows matching the supplied filters,
	// regardless of status. The sessions UI renders all three states
	// (active / orphaned / needs_reauth) with distinct affordances, so
	// hiding any of them by default would only break the user's ability
	// to act on rows that need their attention; status filtering is the
	// caller's responsibility via params.Statuses. Runtime token lookups
	// apply their own status='active' filter and don't go through this
	// method.
	ListOauthUserTokens(ctx context.Context, params MCPSessionsFilterParams) ([]tables.TableOauthUserToken, error)
	// ListPendingOauthUserSessions returns pending OAuth flow rows matching
	// the supplied filters. Companion to ListOauthUserTokens for the admin
	// view. Always restricted to status='pending' AND expires_at > now;
	// params.Statuses further narrows within that set.
	ListPendingOauthUserSessions(ctx context.Context, params MCPSessionsFilterParams) ([]tables.TableOauthUserSession, error)
	// DeleteExpiredOauthUserSessions hard-deletes pending OAuth flow rows
	// whose ExpiresAt has passed. Returns the number of rows removed.
	DeleteExpiredOauthUserSessions(ctx context.Context) (int64, error)
	// DeleteOrphanedOauthUserTokens hard-deletes token rows where status='orphaned'
	// and updated_at is older than olderThan. Returns the number of rows removed.
	DeleteOrphanedOauthUserTokens(ctx context.Context, olderThan time.Duration) (int64, error)

	// Per-user MCP header credential CRUD. Storage analog of per-user OAuth
	// tokens for MCPAuthTypePerUserHeaders clients. The row holds an encrypted
	// JSON blob of header_name → value pairs keyed by (auth_mode, identity,
	// mcp_client_id).
	GetMCPPerUserHeaderCredentialByMode(ctx context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) (*tables.TableMCPPerUserHeaderCredential, error)
	GetMCPPerUserHeaderCredentialByID(ctx context.Context, id string) (*tables.TableMCPPerUserHeaderCredential, error)
	UpsertMCPPerUserHeaderCredential(ctx context.Context, cred *tables.TableMCPPerUserHeaderCredential) error
	DeleteMCPPerUserHeaderCredential(ctx context.Context, id string) error
	// ListMCPPerUserHeaderCredentials returns credential rows matching the
	// supplied filters, regardless of status. Mirrors ListOauthUserTokens —
	// the sessions UI surfaces non-active states (needs_update / orphaned)
	// with distinct affordances, so status filtering is the caller's
	// responsibility via params.Statuses.
	ListMCPPerUserHeaderCredentials(ctx context.Context, params MCPSessionsFilterParams) ([]tables.TableMCPPerUserHeaderCredential, error)
	// MarkMCPPerUserHeaderCredentialsNeedsUpdate flips status to 'needs_update'
	// for every row tied to mcpClientID. Called when the admin changes
	// PerUserHeaderKeys on the MCP client config: existing user submissions
	// stay (so the UI can prefill known values) but are excluded from runtime
	// lookups until the user re-submits.
	MarkMCPPerUserHeaderCredentialsNeedsUpdate(ctx context.Context, mcpClientID string) error
	// DeleteOrphanedMCPPerUserHeaderCredentials hard-deletes rows where
	// status='orphaned' and updated_at is older than olderThan.
	DeleteOrphanedMCPPerUserHeaderCredentials(ctx context.Context, olderThan time.Duration) (int64, error)

	// Per-user-headers submission flow CRUD. Mirrors the OAuth user-session
	// surface — the resolver creates a pending flow row when the inline-401
	// fires, the submit endpoint deletes the row on success, and the sweep
	// worker reaps expired pending rows.
	CreateMCPPerUserHeaderFlow(ctx context.Context, flow *tables.TableMCPPerUserHeaderFlow) error
	GetMCPPerUserHeaderFlowByID(ctx context.Context, id string) (*tables.TableMCPPerUserHeaderFlow, error)
	// GetMCPPerUserHeaderFlowByModeIdentityAndMCPClient returns the canonical
	// pending flow row for the (mode, identity, mcp_client) triple, if any.
	// Companion to GetOauthUserSessionByModeIdentityAndMCPClient — used by
	// InitiateUserSubmissionFlow to keep at most one pending row per binding
	// (mirrors OAuth's single-row-per-binding invariant).
	GetMCPPerUserHeaderFlowByModeIdentityAndMCPClient(ctx context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) (*tables.TableMCPPerUserHeaderFlow, error)
	// UpdateMCPPerUserHeaderFlow updates a flow row in place. Used on the
	// reauth/re-init path to rotate ExpiresAt without spawning a new row.
	UpdateMCPPerUserHeaderFlow(ctx context.Context, flow *tables.TableMCPPerUserHeaderFlow) error
	// DeleteMCPPerUserHeaderFlowsByModeIdentityAndMCPClient hard-deletes any
	// pending flow rows for a binding. Called from revoke so a credential
	// delete also clears any in-flight resubmission flow for the same
	// (mode, identity, mcp_client). Mirrors
	// DeleteOauthUserSessionsByModeIdentityAndMCPClient.
	DeleteMCPPerUserHeaderFlowsByModeIdentityAndMCPClient(ctx context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) error
	DeleteMCPPerUserHeaderFlow(ctx context.Context, id string) error
	// ListPendingMCPPerUserHeaderFlows returns non-expired pending header
	// submission flow rows matching the supplied filters. Mirrors
	// ListPendingOauthUserSessions on the OAuth side. Always restricted to
	// status='pending' AND expires_at > now; params.Statuses further
	// narrows within that set. The implementation reads via ScopedDB(ctx),
	// so a query-scope stashed on ctx (e.g. by enterprise DAC) narrows the
	// result; with no scope, every matching pending row is returned.
	ListPendingMCPPerUserHeaderFlows(ctx context.Context, params MCPSessionsFilterParams) ([]tables.TableMCPPerUserHeaderFlow, error)
	// DeleteExpiredMCPPerUserHeaderFlows hard-deletes pending flow rows whose
	// ExpiresAt has passed. Returns the number of rows removed.
	DeleteExpiredMCPPerUserHeaderFlows(ctx context.Context) (int64, error)

	// Per-user credential reconciliation.
	//
	// Called whenever a VK ↔ MCP grant might have changed (direct
	// dashboard edit, AP propagation, SCIM auto-assign). Orphans
	// vk-keyed credentials whose MCP is no longer in the VK's effective
	// allowlist (explicit per-VK row ∪ MCPs with
	// AllowOnAllVirtualKeys=true) and reactivates orphaned rows when the
	// grant returns. Pending flow rows for lost grants are hard-deleted.
	//
	// Session-keyed rows are never touched — they carry no notion of
	// "lost access".
	//
	// Handlers should invoke both surfaces (OAuth + headers) after every
	// grant-change so both stay consistent.
	ReconcileOauthAfterVKChange(ctx context.Context, vkID string) error
	ReconcileMCPHeadersAfterVKChange(ctx context.Context, vkID string) error
	// MCP-side variants: called when the change originates on the MCP
	// client (vk_configs edit OR AllowOnAllVirtualKeys toggle). Each
	// re-evaluates every VK that holds a credential for the changed MCP.
	ReconcileOauthAfterMCPChange(ctx context.Context, mcpClientID string) error
	ReconcileMCPHeadersAfterMCPChange(ctx context.Context, mcpClientID string) error

	// Not found retry wrapper
	RetryOnNotFound(ctx context.Context, fn func(ctx context.Context) (any, error), maxRetries int, retryDelay time.Duration) (any, error)

	// Prompt Repository - Folders
	GetFolders(ctx context.Context) ([]tables.TableFolder, error)
	GetFolderByID(ctx context.Context, id string) (*tables.TableFolder, error)
	CreateFolder(ctx context.Context, folder *tables.TableFolder) error
	UpdateFolder(ctx context.Context, folder *tables.TableFolder) error
	DeleteFolder(ctx context.Context, id string) error

	// Prompt Repository - Prompts
	GetPrompts(ctx context.Context, folderID *string) ([]tables.TablePrompt, error)
	GetPromptByID(ctx context.Context, id string) (*tables.TablePrompt, error)
	CreatePrompt(ctx context.Context, prompt *tables.TablePrompt, tx ...*gorm.DB) error
	UpdatePrompt(ctx context.Context, prompt *tables.TablePrompt) error
	DeletePrompt(ctx context.Context, id string) error

	// Prompt Repository - Versions
	GetAllPromptVersions(ctx context.Context) ([]tables.TablePromptVersion, error)
	GetPromptVersions(ctx context.Context, promptID string) ([]tables.TablePromptVersion, error)
	GetPromptVersionByID(ctx context.Context, id uint) (*tables.TablePromptVersion, error)
	GetLatestPromptVersion(ctx context.Context, promptID string) (*tables.TablePromptVersion, error)
	CreatePromptVersion(ctx context.Context, version *tables.TablePromptVersion) error
	DeletePromptVersion(ctx context.Context, id uint) error

	// Skills Repository
	CreateSkill(ctx context.Context, skill *tables.TableSkill, version string, objectStore objectstore.ObjectStore) error
	GetSkill(ctx context.Context, id string) (*tables.TableSkill, error)
	GetSkillLean(ctx context.Context, id string) (*tables.TableSkill, error)
	GetSkillByName(ctx context.Context, name string) (*tables.TableSkill, error)
	GetSkillVersion(ctx context.Context, skillID, version string) (*tables.TableSkillVersion, error)
	ListSkillVersions(ctx context.Context, skillID string, params SkillVersionListQueryParams) ([]tables.TableSkillVersion, int64, error)
	UpdateSkill(ctx context.Context, skill *tables.TableSkill, version string, serve bool, objectStore objectstore.ObjectStore) error
	DeleteSkill(ctx context.Context, id string, objectStore objectstore.ObjectStore) error
	ListSkills(ctx context.Context, params SkillListQueryParams) ([]tables.TableSkill, int64, error)
	ShiftSkillVersion(ctx context.Context, skillID string, targetVersion string, objectStore objectstore.ObjectStore) error
	GetAllSkillsVersion(ctx context.Context) (string, error)
	BumpAllSkillsVersion(ctx context.Context, bump string) (string, error)
	CreateSkillFileBlob(ctx context.Context, blob *tables.TableSkillFileBlob) error
	CleanupOrphanSkillFileBlobs(ctx context.Context, force bool) (int64, error)
	UpdateSkillConfigHash(ctx context.Context, skillID string, configHash string) error

	// Prompt Repository - Sessions
	GetPromptSessions(ctx context.Context, promptID string) ([]tables.TablePromptSession, error)
	GetPromptSessionByID(ctx context.Context, id uint) (*tables.TablePromptSession, error)
	CreatePromptSession(ctx context.Context, session *tables.TablePromptSession) error
	UpdatePromptSession(ctx context.Context, session *tables.TablePromptSession) error
	RenamePromptSession(ctx context.Context, id uint, name string) error
	DeletePromptSession(ctx context.Context, id uint) error

	// Sidekiq - generic durable background jobs
	CreateSidekiqJob(ctx context.Context, job *tables.TableSidekiqJob) error
	GetSidekiqJob(ctx context.Context, id string) (*tables.TableSidekiqJob, error)
	ClaimSidekiqJob(ctx context.Context, id, runnerID string, staleBefore time.Time) (bool, error)
	HeartbeatSidekiqJob(ctx context.Context, id, runnerID string) (bool, error)
	UpdateSidekiqJobProgress(ctx context.Context, id, runnerID, metadata string) error
	CompleteSidekiqJob(ctx context.Context, id, runnerID, metadata string) error
	FailSidekiqJob(ctx context.Context, id, runnerID, metadata, lastErr string) error
	ListClaimableSidekiqJobs(ctx context.Context, staleBefore time.Time) ([]tables.TableSidekiqJob, error)
	GetInFlightSidekiqJobByKind(ctx context.Context, kind string) (*tables.TableSidekiqJob, error)
	MarkStaleSidekiqJobsFailed(ctx context.Context, staleBefore time.Time) (int64, error)

	// Webhook Endpoints
	GetWebhookEndpoints(ctx context.Context) ([]tables.TableWebhookEndpoint, error)
	GetWebhookEndpointsPaginated(ctx context.Context, params WebhookEndpointsQueryParams) ([]tables.TableWebhookEndpoint, int64, error)
	GetWebhookEndpointByID(ctx context.Context, id string) (*tables.TableWebhookEndpoint, error)
	GetWebhookEndpointByName(ctx context.Context, name string) (*tables.TableWebhookEndpoint, error)
	CreateWebhookEndpoint(ctx context.Context, endpoint *tables.TableWebhookEndpoint) error
	UpdateWebhookEndpoint(ctx context.Context, endpoint *tables.TableWebhookEndpoint) error
	DeleteWebhookEndpoint(ctx context.Context, id string) error
	RotateWebhookEndpointSecret(ctx context.Context, id string) (*tables.TableWebhookEndpoint, error)
	RecordWebhookEndpointSuccess(ctx context.Context, id string) error
	RecordWebhookEndpointFailure(ctx context.Context, id string) (int, error)

	// Webhook Jobs - in-flight webhook delivery work queue
	CreateWebhookJob(ctx context.Context, job *tables.TableWebhookJob) error
	ListDueWebhookJobs(ctx context.Context, limit int) ([]tables.TableWebhookJob, error)
	ClaimWebhookJob(ctx context.Context, id, runnerID string, leaseUntil time.Time) (bool, error)
	RescheduleWebhookJob(ctx context.Context, id, runnerID string, leaseUntil, nextAttemptAt time.Time) error
	DeleteWebhookJob(ctx context.Context, id, runnerID string, leaseUntil time.Time) error

	// DB returns the underlying database connection.
	DB() *gorm.DB

	// ScopedDB returns the underlying DB bound to ctx with any
	// QueryScope on ctx pre-applied. Use this in read paths that
	// should respect caller-driven row visibility; use DB().WithContext(ctx)
	// for writes and internal lookups that must bypass scoping.
	ScopedDB(ctx context.Context) *gorm.DB

	// RunMigration opens a throwaway *gorm.DB against the same
	// backing database, invokes fn with it, and closes the connection. Use
	// this for DDL (typically downstream-consumer migrations) that must not
	// leave cached prepared-statement plans on the runtime pool.
	//
	// After fn returns successfully, callers should invoke
	// RefreshConnectionPool if the migration altered tables the runtime pool
	// has already queried — otherwise SQLSTATE 0A000 can surface on reads
	// whose cached plans predate the DDL.
	//
	// For SQLite backends, this is a pass-through that runs fn on the
	// existing connection (no server-side plan cache, single-writer lock).
	RunMigration(ctx context.Context, fn func(context.Context, *gorm.DB) error) error

	// RefreshConnectionPool tears down the runtime pool and opens a fresh
	// one against the same configuration. In-flight queries on the old
	// pool complete before it closes; subsequent DB() calls return the new
	// pool, whose connections carry no cached plans. SQLite is a no-op.
	RefreshConnectionPool(ctx context.Context) error

	// GetOAuth2SigningKey returns the signing key, creating and persisting one
	// on first call. Always returns a usable key — never nil on a nil error.
	GetOAuth2SigningKey(ctx context.Context) (*tables.OAuth2SigningKey, error)

	// OAuth2 clients (DCR)
	CreateOAuth2Client(ctx context.Context, client *tables.TableOAuth2Client) error
	GetOAuth2ClientByClientID(ctx context.Context, clientID string) (*tables.TableOAuth2Client, error)

	// OAuth2 authorize requests
	CreateOAuth2AuthorizeRequest(ctx context.Context, req *tables.TableOAuth2AuthorizeRequest) error
	GetOAuth2AuthorizeRequestByID(ctx context.Context, id string) (*tables.TableOAuth2AuthorizeRequest, error)
	GetOAuth2AuthorizeRequestByCodeHash(ctx context.Context, codeHash string) (*tables.TableOAuth2AuthorizeRequest, error)
	// ConsentOAuth2AuthorizeRequest atomically transitions a still-pending request
	// to consented (recording the code hash and resolved identity) — returns
	// ErrNotFound when no longer pending, so concurrent double-consent can't
	// overwrite an already-minted code.
	ConsentOAuth2AuthorizeRequest(ctx context.Context, req *tables.TableOAuth2AuthorizeRequest) error
	SweepExpiredOAuth2AuthorizeRequests(ctx context.Context) error

	// OAuth2 refresh tokens
	GetOAuth2RefreshTokenByHash(ctx context.Context, hash string) (*tables.TableOAuth2RefreshToken, error)
	// GetOAuth2RefreshTokenByHashAny returns the row including revoked tokens,
	// used to detect token reuse attacks and trigger family revocation.
	GetOAuth2RefreshTokenByHashAny(ctx context.Context, hash string) (*tables.TableOAuth2RefreshToken, error)
	// ConsumeOAuth2AuthorizeRequest atomically marks the authorize request as
	// code_issued and creates the refresh token — if either fails the client can retry.
	ConsumeOAuth2AuthorizeRequest(ctx context.Context, requestID string, rt *tables.TableOAuth2RefreshToken) error
	// RotateOAuth2RefreshToken atomically revokes the old token and creates the
	// new one — if either fails the old token stays active and the client can retry.
	RotateOAuth2RefreshToken(ctx context.Context, oldID string, newRT *tables.TableOAuth2RefreshToken) error
	// RevokeOAuth2RefreshTokensByFamilyID revokes all active tokens in a family
	// when a stolen-token reuse is detected (RFC 9700 §2.2.2).
	RevokeOAuth2RefreshTokensByFamilyID(ctx context.Context, familyID string) error
	// RevokeOAuth2RefreshTokensByMode revokes all active tokens for a given mode.
	RevokeOAuth2RefreshTokensByMode(ctx context.Context, bfMode string) error
	// SweepOAuth2RefreshTokens deletes revoked tokens older than the given duration.
	SweepOAuth2RefreshTokens(ctx context.Context, revokedOlderThan time.Duration) (int64, error)
	// SweepOrphanedOAuth2Clients deletes registered clients that back no refresh
	// token and were registered before the grace cutoff. Run after the refresh
	// token sweep so clients are not collected while their tokens are still
	// retained for reuse detection.
	SweepOrphanedOAuth2Clients(ctx context.Context, registeredOlderThan time.Duration) (int64, error)
	// ListOAuth2Sessions returns a page of active downstream grants for the
	// Connected Clients UI, plus the total count matching the filters (before
	// the limit/offset are applied). Filtering and pagination are pushed to SQL.
	ListOAuth2Sessions(ctx context.Context, params OAuth2SessionsQueryParams) ([]OAuth2SessionRow, int64, error)
	// GetOAuth2SessionByID returns a single active grant row for permission checks.
	GetOAuth2SessionByID(ctx context.Context, id string) (*tables.TableOAuth2RefreshToken, error)
	// RevokeOAuth2Session revokes a specific downstream grant by refresh token ID.
	RevokeOAuth2Session(ctx context.Context, id string) error

	// Cleanup
	Close(ctx context.Context) error
}

// NewConfigStore creates a new config store based on the configuration
func NewConfigStore(ctx context.Context, config *Config, logger schemas.Logger) (ConfigStore, error) {
	if config == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}
	if !config.Enabled {
		return nil, nil
	}
	logger.Info("connecting to %s database", config.Type)
	switch config.Type {
	case ConfigStoreTypeSQLite:
		if sqliteConfig, ok := config.Config.(*SQLiteConfig); ok {
			return newSqliteConfigStore(ctx, sqliteConfig, logger)
		}
		return nil, fmt.Errorf("invalid sqlite config: %T", config.Config)
	case ConfigStoreTypePostgres:
		if postgresConfig, ok := config.Config.(*PostgresConfig); ok {
			return newPostgresConfigStore(ctx, postgresConfig, logger)
		}
		return nil, fmt.Errorf("invalid postgres config: %T", config.Config)
	}
	return nil, fmt.Errorf("unsupported config store type: %s", config.Type)
}
