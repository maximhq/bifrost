package mcptools

import (
	"context"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"gorm.io/gorm"
)

// LogReader is the slice of the deployment's telemetry these tools are allowed
// to read.
//
// It exists for two reasons, and the second is the one that forced it.
//
// The plain reason: this is the whole read surface. All queries, no writes and
// nothing that returns key material. Anything a tool can reach is on this list,
// so reviewing what the server can see means reading one interface rather than
// auditing every executor.
//
// The structural reason: plugins/logging depends on framework, so framework
// cannot depend back on it without a module cycle. Declaring the methods here
// and letting logging.LogManager satisfy them structurally is what lets the
// tools live in framework at all.
type LogReader interface {
	Search(ctx context.Context, filters *logstore.SearchFilters, pagination *logstore.PaginationOptions) (*logstore.SearchResult, error)
	GetLog(ctx context.Context, id string) (*logstore.Log, error)
	GetLogsByIDs(ctx context.Context, ids []string) ([]logstore.Log, error)
	GetStats(ctx context.Context, filters *logstore.SearchFilters) (*logstore.SearchStats, error)

	GetHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.HistogramResult, error)
	GetCostHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.CostHistogramResult, error)
	GetTokenHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.TokenHistogramResult, error)
	GetLatencyHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.LatencyHistogramResult, error)
	GetThroughputHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ThroughputHistogramResult, error)

	GetModelRankings(ctx context.Context, filters *logstore.SearchFilters) (*logstore.ModelRankingResult, error)
	GetDimensionRankings(ctx context.Context, filters *logstore.SearchFilters, dimension logstore.RankingDimension) (*logstore.DimensionRankingResult, error)

	GetProviderCostHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderCostHistogramResult, error)
	GetProviderLatencyHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderLatencyHistogramResult, error)
	GetProviderThroughputHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderThroughputHistogramResult, error)
	GetProviderTokenHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderTokenHistogramResult, error)

	GetAvailableModels(ctx context.Context, limit int, query string) ([]string, error)
	GetAvailableApps(ctx context.Context, limit int, query string) ([]string, error)
	GetAvailableStopReasons(ctx context.Context, limit int, query string) ([]string, error)
	GetAvailableVirtualKeys(ctx context.Context, limit int, query string) ([]KeyPair, error)
	GetAvailableTeams(ctx context.Context, limit int, query string) ([]KeyPair, error)
	GetAvailableCustomers(ctx context.Context, limit int, query string) ([]KeyPair, error)
	GetAvailableBusinessUnits(ctx context.Context, limit int, query string) ([]KeyPair, error)
	GetAvailableRoutingRules(ctx context.Context, limit int, query string) ([]KeyPair, error)
	GetAvailableSelectedKeys(ctx context.Context, limit int, query string) ([]KeyPair, error)
	GetAvailableAliases(ctx context.Context, limit int, query string) ([]string, error)
	GetAvailableRoutingEngines(ctx context.Context, limit int, query string) ([]string, error)
	GetAvailableToolCallNames(ctx context.Context, limit int, query string) ([]string, error)
	GetAvailableMetadataKeys(ctx context.Context, limit int, query string) (map[string][]string, error)

	GetSessionLogs(ctx context.Context, sessionID string, pagination *logstore.PaginationOptions) (*logstore.SessionDetailResult, error)
	GetSessionSummary(ctx context.Context, sessionID string) (*logstore.SessionSummaryResult, error)
	GetDroppedRequests(ctx context.Context) int64

	GetMCPToolLog(ctx context.Context, id string) (*logstore.MCPToolLog, error)
	SearchMCPToolLogs(ctx context.Context, filters *logstore.MCPToolLogSearchFilters, pagination *logstore.PaginationOptions) (*logstore.MCPToolLogSearchResult, error)
	GetMCPToolLogStats(ctx context.Context, filters *logstore.MCPToolLogSearchFilters) (*logstore.MCPToolLogStats, error)
	GetMCPHistogram(ctx context.Context, filters logstore.MCPToolLogSearchFilters, bucketSizeSeconds int64) (*logstore.MCPHistogramResult, error)
	GetMCPCostHistogram(ctx context.Context, filters logstore.MCPToolLogSearchFilters, bucketSizeSeconds int64) (*logstore.MCPCostHistogramResult, error)
	GetMCPTopTools(ctx context.Context, filters logstore.MCPToolLogSearchFilters, limit int) (*logstore.MCPTopToolsResult, error)
}

// KeyPair is an id paired with the name it is known by.
type KeyPair struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// GovernanceReader is the slice of the config store these tools may reach.
// configstore.ConfigStore satisfies it structurally. Only some methods apply
// the request's row filter (virtual keys, teams, customers read through
// ScopedDB; budgets, rate limits, model configs and writes do not), so the
// tools enforce the caller's tenant themselves - see tenant.go.
//
// Writes persist through this interface; GovernanceReloader / MCPRuntime are
// what make a new row live in the process that inference actually consults.
type GovernanceReader interface {
	GetVirtualKey(ctx context.Context, id string) (*tables.TableVirtualKey, error)
	GetVirtualKeysPaginated(ctx context.Context, params configstore.VirtualKeyQueryParams) ([]tables.TableVirtualKey, int64, error)
	CreateVirtualKey(ctx context.Context, virtualKey *tables.TableVirtualKey, tx ...*gorm.DB) error
	UpdateVirtualKey(ctx context.Context, virtualKey *tables.TableVirtualKey, tx ...*gorm.DB) error

	GetTeam(ctx context.Context, id string, tx ...*gorm.DB) (*tables.TableTeam, error)
	GetTeamsPaginated(ctx context.Context, params configstore.TeamsQueryParams) ([]tables.TableTeam, int64, error)
	CreateTeam(ctx context.Context, team *tables.TableTeam, tx ...*gorm.DB) error
	UpdateTeam(ctx context.Context, team *tables.TableTeam, tx ...*gorm.DB) error

	GetCustomer(ctx context.Context, id string, tx ...*gorm.DB) (*tables.TableCustomer, error)
	GetCustomersPaginated(ctx context.Context, params configstore.CustomersQueryParams) ([]tables.TableCustomer, int64, error)
	CreateCustomer(ctx context.Context, customer *tables.TableCustomer, tx ...*gorm.DB) error
	UpdateCustomer(ctx context.Context, customer *tables.TableCustomer, tx ...*gorm.DB) error

	GetBudget(ctx context.Context, id string, tx ...*gorm.DB) (*tables.TableBudget, error)
	GetBudgets(ctx context.Context) ([]tables.TableBudget, error)
	CreateBudget(ctx context.Context, budget *tables.TableBudget, tx ...*gorm.DB) error
	UpdateBudget(ctx context.Context, budget *tables.TableBudget, tx ...*gorm.DB) error

	GetProviders(ctx context.Context) ([]tables.TableProvider, error)
	GetProviderKeys(ctx context.Context, provider schemas.ModelProvider) ([]schemas.Key, error)
	GetProviderConfig(ctx context.Context, provider schemas.ModelProvider) (*configstore.ProviderConfig, error)
	AddProvider(ctx context.Context, provider schemas.ModelProvider, config configstore.ProviderConfig, tx ...*gorm.DB) error
	UpdateProvider(ctx context.Context, provider schemas.ModelProvider, config configstore.ProviderConfig, tx ...*gorm.DB) error
	CreateProviderKey(ctx context.Context, provider schemas.ModelProvider, key schemas.Key, tx ...*gorm.DB) error
	UpdateProviderKey(ctx context.Context, provider schemas.ModelProvider, keyID string, key schemas.Key, tx ...*gorm.DB) error

	GetMCPClientByID(ctx context.Context, id string) (*tables.TableMCPClient, error)
	GetMCPClientsPaginated(ctx context.Context, params configstore.MCPClientsQueryParams) ([]tables.TableMCPClient, int64, error)
	CreateMCPClientConfig(ctx context.Context, clientConfig *schemas.MCPClientConfig) error
	UpdateMCPClientConfig(ctx context.Context, id string, clientConfig *tables.TableMCPClient) error

	GetVirtualMCPByID(ctx context.Context, id uint) (*tables.TableVirtualMCP, error)
	GetVirtualMCPsPaginated(ctx context.Context, params configstore.VirtualMCPsQueryParams) ([]tables.TableVirtualMCP, int64, error)
	CreateVirtualMCP(ctx context.Context, def *tables.TableVirtualMCP) error
	UpdateVirtualMCP(ctx context.Context, def *tables.TableVirtualMCP) error
	AttachVirtualMCPToVirtualKey(ctx context.Context, vmcpID uint, virtualKeyID string) error
	DetachVirtualMCPFromVirtualKey(ctx context.Context, vmcpID uint, virtualKeyID string) error
	GetVirtualKeyIDsForVirtualMCP(ctx context.Context, vmcpID uint) ([]string, error)

	GetRoutingRule(ctx context.Context, id string) (*tables.TableRoutingRule, error)
	GetRoutingRulesPaginated(ctx context.Context, params configstore.RoutingRulesQueryParams) ([]tables.TableRoutingRule, int64, error)
	CreateRoutingRule(ctx context.Context, rule *tables.TableRoutingRule, tx ...*gorm.DB) error
	UpdateRoutingRule(ctx context.Context, rule *tables.TableRoutingRule, tx ...*gorm.DB) error

	GetRateLimit(ctx context.Context, id string, tx ...*gorm.DB) (*tables.TableRateLimit, error)
	GetRateLimits(ctx context.Context) ([]tables.TableRateLimit, error)
	CreateRateLimit(ctx context.Context, rateLimit *tables.TableRateLimit, tx ...*gorm.DB) error
	UpdateRateLimit(ctx context.Context, rateLimit *tables.TableRateLimit, tx ...*gorm.DB) error

	GetModelConfigByID(ctx context.Context, id string) (*tables.TableModelConfig, error)
	GetModelConfigsPaginated(ctx context.Context, params configstore.ModelConfigsQueryParams) ([]tables.TableModelConfig, int64, error)
	CreateModelConfig(ctx context.Context, modelConfig *tables.TableModelConfig, tx ...*gorm.DB) error
	UpdateModelConfig(ctx context.Context, modelConfig *tables.TableModelConfig, tx ...*gorm.DB) error

	GetPlugin(ctx context.Context, name string) (*tables.TablePlugin, error)
	GetPlugins(ctx context.Context) ([]*tables.TablePlugin, error)
	CreatePlugin(ctx context.Context, plugin *tables.TablePlugin, tx ...*gorm.DB) error
	UpdatePlugin(ctx context.Context, plugin *tables.TablePlugin, tx ...*gorm.DB) error

	GetPricingOverrideByID(ctx context.Context, id string) (*tables.TablePricingOverride, error)
	GetPricingOverridesPaginated(ctx context.Context, params configstore.PricingOverridesQueryParams) ([]tables.TablePricingOverride, int64, error)
	CreatePricingOverride(ctx context.Context, override *tables.TablePricingOverride, tx ...*gorm.DB) error
	UpdatePricingOverride(ctx context.Context, override *tables.TablePricingOverride, tx ...*gorm.DB) error

	GetWebhookEndpointByID(ctx context.Context, id string) (*tables.TableWebhookEndpoint, error)
	GetWebhookEndpointsPaginated(ctx context.Context, params configstore.WebhookEndpointsQueryParams) ([]tables.TableWebhookEndpoint, int64, error)
	CreateWebhookEndpoint(ctx context.Context, endpoint *tables.TableWebhookEndpoint) error
	UpdateWebhookEndpoint(ctx context.Context, endpoint *tables.TableWebhookEndpoint) error

	ListFeatureFlags(ctx context.Context) ([]tables.TableFeatureFlag, error)
	UpsertFeatureFlag(ctx context.Context, id string, enabled bool, updatedAt int64) error

	ListOauthUserTokens(ctx context.Context, params configstore.MCPSessionsFilterParams) ([]tables.TableMCPOauthToken, error)
	ListMCPPerUserHeaderCredentials(ctx context.Context, params configstore.MCPSessionsFilterParams) ([]tables.TableMCPPerUserHeaderCredential, error)

	GetClientConfig(ctx context.Context) (*configstore.ClientConfig, error)

	// ExecuteTransaction runs fn in one database transaction; writes that
	// create a row and link it to another pass the tx through both so a
	// failure part-way leaves nothing behind.
	ExecuteTransaction(ctx context.Context, fn func(tx *gorm.DB) error) error
}

// GovernanceReloader refreshes the in-memory governance cache after a write.
type GovernanceReloader interface {
	ReloadVirtualKey(ctx context.Context, id string) (*tables.TableVirtualKey, error)
	ReloadTeam(ctx context.Context, id string) (*tables.TableTeam, error)
	ReloadCustomer(ctx context.Context, id string) (*tables.TableCustomer, error)
	ReloadVirtualMCP(ctx context.Context, id uint) (*tables.TableVirtualMCP, error)
	// The key-to-virtual-MCP assignment is its own in-memory map; attach and
	// detach update it directly.
	AttachVirtualMCPToVirtualKeyInMemory(ctx context.Context, vkID string, id uint) error
	DetachVirtualMCPFromVirtualKeyInMemory(ctx context.Context, vkID string, id uint) error
	ReloadRoutingRule(ctx context.Context, id string) error
	ReloadModelConfig(ctx context.Context, id string) (*tables.TableModelConfig, error)
	ReloadRateLimit(ctx context.Context, id string) error
	UpsertPricingOverride(ctx context.Context, override *tables.TablePricingOverride) error
	ReloadWebhookEndpoint(ctx context.Context, id string) error
}

// MCPRuntime connects, drops or redials an upstream MCP client in this
// process. *BifrostHTTPServer satisfies it, not the core client: the server
// also keeps the config's in-memory client list in step (which
// MCPClientEditor reads, so a client added here can be edited next) and
// resyncs the /mcp surface. Every method takes the tool call's context, which
// the add - including the add a redial falls back to for a client that never
// connected - dials under. Nil means the row is stored but not dialed.
type MCPRuntime interface {
	AddMCPClient(ctx context.Context, config *schemas.MCPClientConfig) error
	RemoveMCPClient(ctx context.Context, id string) error
	ReconnectMCPClient(ctx context.Context, id string) error
}

// MCPClientEditor applies an edited MCP client to the live process: the
// runtime connection and the gateway's view of it. *BifrostHTTPServer
// satisfies it. Nil means an edit is stored but not applied until restart.
type MCPClientEditor interface {
	// GetMCPClientConfig returns a copy of the live config, safe to edit.
	GetMCPClientConfig(id string) (*schemas.MCPClientConfig, error)
	UpdateMCPClient(ctx context.Context, id string, updatedConfig *schemas.MCPClientConfig) error
}

// WarpConfigSaver saves Warp's configuration through Warp's own validation -
// base URL shape, the vector store an enable needs, namespace provisioning and
// the embedding-space rules. *warp.Service satisfies it; framework/warp imports
// this package, so the JSON body is the boundary rather than warp's types.
// body carries a full configuration with the same field presence the HTTP
// handler's request body has.
type WarpConfigSaver interface {
	SaveConfigJSON(ctx context.Context, body []byte) error
}

// CELValidator checks a routing rule's CEL expression compiles. The routing
// plugin owns the CEL environment and framework cannot import it, so the
// transport supplies it.
type CELValidator interface {
	ValidateCELExpression(expression string) error
}

// ProviderKeyChecks gives provider-key writes the HTTP handler's checks - no
// keys on a keyless provider, the value, model lists and aliases, and the
// URL or config a provider's keys require - and its model-catalog refresh
// after the write. *BifrostHTTPServer satisfies it.
type ProviderKeyChecks interface {
	ValidateProviderKey(provider schemas.ModelProvider, key schemas.Key, creating bool) error
	OnKeyAdded(ctx context.Context, provider schemas.ModelProvider, key schemas.Key) error
	OnKeyUpdated(ctx context.Context, provider schemas.ModelProvider, key schemas.Key) error
}

// ProviderRuntime adds providers and keys to the live process.
// *lib.Config satisfies it. Nil means the row is stored but not loaded.
type ProviderRuntime interface {
	AddProvider(ctx context.Context, provider schemas.ModelProvider, config configstore.ProviderConfig) error
	UpdateProviderConfig(ctx context.Context, provider schemas.ModelProvider, config configstore.ProviderConfig) error
	AddProviderKey(ctx context.Context, provider schemas.ModelProvider, key schemas.Key) error
	UpdateProviderKey(ctx context.Context, provider schemas.ModelProvider, keyID string, key schemas.Key) error
}

// PluginRuntime reloads a plugin into this process after a persist.
// *BifrostHTTPServer satisfies it. Nil means the row is stored but not loaded.
type PluginRuntime interface {
	ReloadPlugin(ctx context.Context, name string, path *string, pluginConfig any, placement *schemas.PluginPlacement, order *int) error
	// DisablePlugin stops a loaded plugin whose stored row is disabled. A
	// plugin that is not loaded is not an error.
	DisablePlugin(ctx context.Context, name string) error
	// NormalizePluginConfig decodes config through the plugin's typed config;
	// nil means the plugin has none to apply.
	NormalizePluginConfig(name string, config map[string]any) (map[string]any, error)
}

// ModelsRuntime re-runs list-models discovery for a provider or one of its keys.
// *BifrostHTTPServer satisfies it.
type ModelsRuntime interface {
	RefreshLiveModelsForAllKeys(ctx context.Context, provider schemas.ModelProvider) error
	RefreshLiveModelsForKey(ctx context.Context, provider schemas.ModelProvider, keyID string) error
}

// Pinger is a store that can answer whether it is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// MaxSemanticQueryChars bounds the natural-language query, in characters.
const MaxSemanticQueryChars = 2000

// SemanticSearcher is semantic_search_logs' dependency.
type SemanticSearcher interface {
	Search(ctx context.Context, query string, filters *logstore.SearchFilters, requestedLimit int) (SemanticSearchResult, error)
}

// SemanticSearchRow is one hit: the projected log row and its vector score.
type SemanticSearchRow struct {
	Score float64 `json:"score"`
	LogRow
}

// SemanticSearchResult is what a SemanticSearcher returns, in score order.
type SemanticSearchResult struct {
	Rows      []SemanticSearchRow `json:"rows"`
	Returned  int                 `json:"returned"`
	Threshold float64             `json:"threshold"`
}
