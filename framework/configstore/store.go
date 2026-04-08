// Package configstore provides a persistent configuration store for Bifrost.
package configstore

import (
	"context"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/migrator"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"gorm.io/gorm"
)

// VirtualKeyQueryParams holds pagination, filtering, and search parameters for virtual key queries.
type VirtualKeyQueryParams struct {
	Limit      int
	Offset     int
	Search     string
	CustomerID string
	TeamID     string
	SortBy     string // name, budget_spent, created_at, status (default: created_at)
	Order      string // asc, desc (default: asc)
	Export     bool   // When true, skip default pagination limits (caller controls limit)
}

// ModelConfigsQueryParams holds pagination, filtering, and search parameters for model configs queries.
type ModelConfigsQueryParams struct {
	Limit  int
	Offset int
	Search string
}

// RoutingRulesQueryParams holds pagination, filtering, and search parameters for routing rules queries.
type RoutingRulesQueryParams struct {
	Limit  int
	Offset int
	Search string
}

// MCPClientsQueryParams holds pagination, filtering, and search parameters for MCP client queries.
type MCPClientsQueryParams struct {
	Limit  int
	Offset int
	Search string
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

// ConfigStore is the interface for the config store.
type ConfigStore interface {
	// Health check
	Ping(ctx context.Context) error

	// Encryption
	EncryptPlaintextRows(ctx context.Context) error

	// Client config CRUD
	UpdateClientConfig(ctx context.Context, config *ClientConfig) error
	GetClientConfig(ctx context.Context) (*ClientConfig, error)

	// Framework config CRUD
	UpdateFrameworkConfig(ctx context.Context, config *tables.TableFrameworkConfig) error
	GetFrameworkConfig(ctx context.Context) (*tables.TableFrameworkConfig, error)

	// Provider config CRUD
	UpdateProvidersConfig(ctx context.Context, providers map[schemas.ModelProvider]ProviderConfig, tx ...*gorm.DB) error
	AddProvider(ctx context.Context, provider schemas.ModelProvider, config ProviderConfig, tx ...*gorm.DB) error
	UpdateProvider(ctx context.Context, provider schemas.ModelProvider, config ProviderConfig, tx ...*gorm.DB) error
	DeleteProvider(ctx context.Context, provider schemas.ModelProvider, tx ...*gorm.DB) error
	GetProvidersConfig(ctx context.Context) (map[schemas.ModelProvider]ProviderConfig, error)
	GetProviderConfig(ctx context.Context, provider schemas.ModelProvider) (*ProviderConfig, error)
	GetProviders(ctx context.Context) ([]tables.TableProvider, error)
	GetProvider(ctx context.Context, provider schemas.ModelProvider) (*tables.TableProvider, error)
	UpdateStatus(ctx context.Context, provider schemas.ModelProvider, keyID string, status, errorMsg string) error

	// MCP config CRUD
	GetMCPConfig(ctx context.Context) (*schemas.MCPConfig, error)
	GetMCPClientByID(ctx context.Context, id string) (*tables.TableMCPClient, error)
	GetMCPClientByName(ctx context.Context, name string) (*tables.TableMCPClient, error)
	GetMCPClientsPaginated(ctx context.Context, params MCPClientsQueryParams) ([]tables.TableMCPClient, int64, error)
	CreateMCPClientConfig(ctx context.Context, clientConfig *schemas.MCPClientConfig) error
	UpdateMCPClientConfig(ctx context.Context, id string, clientConfig *tables.TableMCPClient) error
	DeleteMCPClientConfig(ctx context.Context, id string) error

	// Vector store config CRUD
	UpdateVectorStoreConfig(ctx context.Context, config *vectorstore.Config) error
	GetVectorStoreConfig(ctx context.Context) (*vectorstore.Config, error)

	// Logs store config CRUD
	UpdateLogsStoreConfig(ctx context.Context, config *logstore.Config) error
	GetLogsStoreConfig(ctx context.Context) (*logstore.Config, error)

	// Config CRUD
	GetConfig(ctx context.Context, key string) (*tables.TableGovernanceConfig, error)
	UpdateConfig(ctx context.Context, config *tables.TableGovernanceConfig, tx ...*gorm.DB) error

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
	CreateVirtualKey(ctx context.Context, virtualKey *tables.TableVirtualKey, tx ...*gorm.DB) error
	UpdateVirtualKey(ctx context.Context, virtualKey *tables.TableVirtualKey, tx ...*gorm.DB) error
	DeleteVirtualKey(ctx context.Context, id string) error

	// Virtual key provider config CRUD
	GetVirtualKeyProviderConfigs(ctx context.Context, virtualKeyID string) ([]tables.TableVirtualKeyProviderConfig, error)
	CreateVirtualKeyProviderConfig(ctx context.Context, virtualKeyProviderConfig *tables.TableVirtualKeyProviderConfig, tx ...*gorm.DB) error
	UpdateVirtualKeyProviderConfig(ctx context.Context, virtualKeyProviderConfig *tables.TableVirtualKeyProviderConfig, tx ...*gorm.DB) error
	DeleteVirtualKeyProviderConfig(ctx context.Context, id uint, tx ...*gorm.DB) error

	// Virtual key MCP config CRUD
	GetVirtualKeyMCPConfigs(ctx context.Context, virtualKeyID string) ([]tables.TableVirtualKeyMCPConfig, error)
	CreateVirtualKeyMCPConfig(ctx context.Context, virtualKeyMCPConfig *tables.TableVirtualKeyMCPConfig, tx ...*gorm.DB) error
	UpdateVirtualKeyMCPConfig(ctx context.Context, virtualKeyMCPConfig *tables.TableVirtualKeyMCPConfig, tx ...*gorm.DB) error
	DeleteVirtualKeyMCPConfig(ctx context.Context, id uint, tx ...*gorm.DB) error

	// Team CRUD
	GetTeams(ctx context.Context, customerID string) ([]tables.TableTeam, error)
	GetTeamsPaginated(ctx context.Context, params TeamsQueryParams) ([]tables.TableTeam, int64, error)
	GetTeam(ctx context.Context, id string) (*tables.TableTeam, error)
	CreateTeam(ctx context.Context, team *tables.TableTeam, tx ...*gorm.DB) error
	UpdateTeam(ctx context.Context, team *tables.TableTeam, tx ...*gorm.DB) error
	DeleteTeam(ctx context.Context, id string) error

	// Customer CRUD
	GetCustomers(ctx context.Context) ([]tables.TableCustomer, error)
	GetCustomersPaginated(ctx context.Context, params CustomersQueryParams) ([]tables.TableCustomer, int64, error)
	GetCustomer(ctx context.Context, id string) (*tables.TableCustomer, error)
	CreateCustomer(ctx context.Context, customer *tables.TableCustomer, tx ...*gorm.DB) error
	UpdateCustomer(ctx context.Context, customer *tables.TableCustomer, tx ...*gorm.DB) error
	DeleteCustomer(ctx context.Context, id string) error

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
	UpdateBudgetUsage(ctx context.Context, id string, currentUsage float64) error
	UpdateRateLimitUsage(ctx context.Context, id string, tokenCurrentUsage int64, requestCurrentUsage int64) error

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
	GetModelConfigsPaginated(ctx context.Context, params ModelConfigsQueryParams) ([]tables.TableModelConfig, int64, error)
	GetModelConfig(ctx context.Context, modelName string, provider *string) (*tables.TableModelConfig, error)
	GetModelConfigByID(ctx context.Context, id string) (*tables.TableModelConfig, error)
	CreateModelConfig(ctx context.Context, modelConfig *tables.TableModelConfig, tx ...*gorm.DB) error
	UpdateModelConfig(ctx context.Context, modelConfig *tables.TableModelConfig, tx ...*gorm.DB) error
	UpdateModelConfigs(ctx context.Context, modelConfigs []*tables.TableModelConfig, tx ...*gorm.DB) error
	DeleteModelConfig(ctx context.Context, id string) error

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

	// Model pricing CRUD
	GetModelPrices(ctx context.Context) ([]tables.TableModelPricing, error)
	UpsertModelPrices(ctx context.Context, pricing *tables.TableModelPricing, tx ...*gorm.DB) error
	DeleteModelPrices(ctx context.Context, tx ...*gorm.DB) error

	// Model parameters
	GetModelParameters(ctx context.Context, model string) (*tables.TableModelParameters, error)
	UpsertModelParameters(ctx context.Context, params *tables.TableModelParameters, tx ...*gorm.DB) error

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
	CreatePrompt(ctx context.Context, prompt *tables.TablePrompt) error
	UpdatePrompt(ctx context.Context, prompt *tables.TablePrompt) error
	DeletePrompt(ctx context.Context, id string) error

	// Prompt Repository - Versions
	GetPromptVersions(ctx context.Context, promptID string) ([]tables.TablePromptVersion, error)
	GetPromptVersionByID(ctx context.Context, id uint) (*tables.TablePromptVersion, error)
	GetLatestPromptVersion(ctx context.Context, promptID string) (*tables.TablePromptVersion, error)
	CreatePromptVersion(ctx context.Context, version *tables.TablePromptVersion) error
	DeletePromptVersion(ctx context.Context, id uint) error

	// Prompt Repository - Sessions
	GetPromptSessions(ctx context.Context, promptID string) ([]tables.TablePromptSession, error)
	GetPromptSessionByID(ctx context.Context, id uint) (*tables.TablePromptSession, error)
	CreatePromptSession(ctx context.Context, session *tables.TablePromptSession) error
	UpdatePromptSession(ctx context.Context, session *tables.TablePromptSession) error
	RenamePromptSession(ctx context.Context, id uint, name string) error
	DeletePromptSession(ctx context.Context, id uint) error

	// DB returns the underlying database connection.
	DB() *gorm.DB

	// Migration manager
	RunMigration(ctx context.Context, migration *migrator.Migration) error

	// Cleanup
	Close(ctx context.Context) error

	// ─── RBAC ─────────────────────────────────────────────────────────────────
	CreateRole(ctx context.Context, role *tables.TableRole) error
	GetRole(ctx context.Context, id string) (*tables.TableRole, error)
	ListRoles(ctx context.Context) ([]tables.TableRole, error)
	UpdateRole(ctx context.Context, id string, updates map[string]any) error
	DeleteRole(ctx context.Context, id string) error
	UpsertPermission(ctx context.Context, perm *tables.TablePermission) error
	ListPermissions(ctx context.Context) ([]tables.TablePermission, error)
	AssignPermissionToRole(ctx context.Context, roleID, permissionID string) error
	RevokePermissionFromRole(ctx context.Context, roleID, permissionID string) error
	GetRolePermissions(ctx context.Context, roleID string) ([]tables.TablePermission, error)
	AssignRoleToUser(ctx context.Context, userID, roleID, grantedBy string) error
	RevokeRoleFromUser(ctx context.Context, userID, roleID string) error
	GetUserRoles(ctx context.Context, userID string) ([]tables.TableRole, error)
	GetUserPermissions(ctx context.Context, userID string) ([]tables.TablePermission, error)

	// ─── SSO/SCIM ─────────────────────────────────────────────────────────────
	CreateSSOProvider(ctx context.Context, provider *tables.TableSSOProvider) error
	GetSSOProvider(ctx context.Context, id string) (*tables.TableSSOProvider, error)
	ListSSOProviders(ctx context.Context) ([]tables.TableSSOProvider, error)
	UpdateSSOProvider(ctx context.Context, id string, updates map[string]any) error
	DeleteSSOProvider(ctx context.Context, id string) error
	UpsertExternalUser(ctx context.Context, user *tables.TableExternalUser) (*tables.TableExternalUser, error)
	GetExternalUser(ctx context.Context, id string) (*tables.TableExternalUser, error)
	FindExternalUserByEmail(ctx context.Context, email string) (*tables.TableExternalUser, error)
	ListExternalUsers(ctx context.Context, providerID string) ([]tables.TableExternalUser, error)
	DeactivateExternalUser(ctx context.Context, id string) error
	CreateSSOSession(ctx context.Context, sess *tables.TableSSOSession) error
	DeleteSSOSession(ctx context.Context, id string) error
	CleanExpiredSSOSessions(ctx context.Context) (int64, error)

	// ─── User Groups ──────────────────────────────────────────────────────────
	CreateUserGroup(ctx context.Context, group *tables.TableUserGroup) error
	GetUserGroup(ctx context.Context, id string) (*tables.TableUserGroup, error)
	ListUserGroups(ctx context.Context) ([]tables.TableUserGroup, error)
	UpdateUserGroup(ctx context.Context, id string, updates map[string]any) error
	DeleteUserGroup(ctx context.Context, id string) error
	UpsertUserGroup(ctx context.Context, group *tables.TableUserGroup) (*tables.TableUserGroup, error)
	FindUserGroupByExternalID(ctx context.Context, externalID string) (*tables.TableUserGroup, error)
	AddUserToGroup(ctx context.Context, groupID, userID, addedBy string) error
	RemoveUserFromGroup(ctx context.Context, groupID, userID string) error
	GetUserGroups(ctx context.Context, userID string) ([]tables.TableUserGroup, error)
	GetUserGroupMembers(ctx context.Context, groupID string) ([]tables.TableUserGroupMember, error)
	AssignVirtualKeyToGroup(ctx context.Context, groupID, vkID string, budgetOverride *float64) error
	UnassignVirtualKeyFromGroup(ctx context.Context, groupID, vkID string) error
	GetUserGroupVirtualKeys(ctx context.Context, groupID string) ([]tables.TableUserGroupVirtualKey, error)
	AssignMCPGroupToUserGroup(ctx context.Context, groupID, mcpGroupID string) error
	UnassignMCPGroupFromUserGroup(ctx context.Context, groupID, mcpGroupID string) error
	GetUserGroupMCPGroups(ctx context.Context, groupID string) ([]tables.TableUserGroupMCPGroup, error)

	// ─── Audit Logs ───────────────────────────────────────────────────────────
	AppendAuditLog(ctx context.Context, entry *tables.TableAuditLog) error
	QueryAuditLogs(ctx context.Context, opts AuditLogQueryOpts) ([]tables.TableAuditLog, int64, error)
	VerifyAuditChain(ctx context.Context, fromSeq int64) (int64, error)

	// ─── Guardrails ───────────────────────────────────────────────────────────
	CreateGuardrailPolicy(ctx context.Context, p *tables.TableGuardrailPolicy) error
	GetGuardrailPolicy(ctx context.Context, id string) (*tables.TableGuardrailPolicy, error)
	ListGuardrailPolicies(ctx context.Context) ([]tables.TableGuardrailPolicy, error)
	UpdateGuardrailPolicy(ctx context.Context, id string, updates map[string]any) error
	DeleteGuardrailPolicy(ctx context.Context, id string) error
	CreateGuardrailRule(ctx context.Context, r *tables.TableGuardrailRule) error
	ListGuardrailRules(ctx context.Context, policyID string) ([]tables.TableGuardrailRule, error)
	DeleteGuardrailRule(ctx context.Context, id string) error
	AppendGuardrailViolation(ctx context.Context, v *tables.TableGuardrailViolation) error
	QueryGuardrailViolations(ctx context.Context, opts GuardrailViolationQueryOpts) ([]tables.TableGuardrailViolation, int64, error)

	// ─── PII Redactor ─────────────────────────────────────────────────────────
	CreatePIIPolicy(ctx context.Context, p *tables.TablePIIPolicy) error
	GetPIIPolicy(ctx context.Context, id string) (*tables.TablePIIPolicy, error)
	ListPIIPolicies(ctx context.Context) ([]tables.TablePIIPolicy, error)
	UpdatePIIPolicy(ctx context.Context, id string, updates map[string]any) error
	DeletePIIPolicy(ctx context.Context, id string) error
	CreatePIIDetectorRule(ctx context.Context, r *tables.TablePIIDetectorRule) error
	ListPIIDetectorRules(ctx context.Context, policyID string) ([]tables.TablePIIDetectorRule, error)
	DeletePIIDetectorRule(ctx context.Context, id string) error
	UpsertPIIToken(ctx context.Context, t *tables.TablePIITokenStore) error
	GetPIIToken(ctx context.Context, token string) (*tables.TablePIITokenStore, error)
	DeleteExpiredPIITokens(ctx context.Context) (int64, error)

	// ─── Adaptive Routing ─────────────────────────────────────────────────────
	CreateRoutingPolicy(ctx context.Context, p *tables.TableRoutingPolicy) error
	GetRoutingPolicy(ctx context.Context, id string) (*tables.TableRoutingPolicy, error)
	ListRoutingPolicies(ctx context.Context) ([]tables.TableRoutingPolicy, error)
	UpdateRoutingPolicy(ctx context.Context, id string, updates map[string]any) error
	DeleteRoutingPolicy(ctx context.Context, id string) error
	UpsertProviderMetrics(ctx context.Context, m *tables.TableProviderMetrics) error
	GetProviderMetrics(ctx context.Context, provider, model string, windowMinutes int, since time.Time) ([]tables.TableProviderMetrics, error)
	UpsertModelQualityScore(ctx context.Context, score *tables.TableModelQualityScore) error
	ListModelQualityScores(ctx context.Context) ([]tables.TableModelQualityScore, error)

	// ─── Alerting ─────────────────────────────────────────────────────────────
	CreateAlertRule(ctx context.Context, r *tables.TableAlertRule) error
	GetAlertRule(ctx context.Context, id string) (*tables.TableAlertRule, error)
	ListAlertRules(ctx context.Context) ([]tables.TableAlertRule, error)
	UpdateAlertRule(ctx context.Context, id string, updates map[string]any) error
	DeleteAlertRule(ctx context.Context, id string) error
	CreateAlertChannel(ctx context.Context, c *tables.TableAlertChannel) error
	GetAlertChannel(ctx context.Context, id string) (*tables.TableAlertChannel, error)
	ListAlertChannels(ctx context.Context) ([]tables.TableAlertChannel, error)
	UpdateAlertChannel(ctx context.Context, id string, updates map[string]any) error
	DeleteAlertChannel(ctx context.Context, id string) error
	UpsertAlertState(ctx context.Context, state *tables.TableAlertState) error
	GetAlertState(ctx context.Context, ruleID string) (*tables.TableAlertState, error)
	ListAlertStates(ctx context.Context) ([]tables.TableAlertState, error)
	AppendAlertHistory(ctx context.Context, h *tables.TableAlertHistory) error
	QueryAlertHistory(ctx context.Context, opts AlertHistoryQueryOpts) ([]tables.TableAlertHistory, int64, error)

	// ─── MCP Tool Groups ──────────────────────────────────────────────────────
	CreateMCPToolGroup(ctx context.Context, g *tables.TableMCPToolGroup) error
	GetMCPToolGroup(ctx context.Context, id string) (*tables.TableMCPToolGroup, error)
	ListMCPToolGroups(ctx context.Context) ([]tables.TableMCPToolGroup, error)
	UpdateMCPToolGroup(ctx context.Context, id string, updates map[string]any) error
	DeleteMCPToolGroup(ctx context.Context, id string) error
	AddMCPToolGroupMember(ctx context.Context, groupID, clientID, toolName string) error
	RemoveMCPToolGroupMember(ctx context.Context, memberID string) error
	GetMCPToolGroupMembers(ctx context.Context, groupID string) ([]tables.TableMCPToolGroupMember, error)
	AssignVirtualKeyMCPGroup(ctx context.Context, vkID, groupID string) error
	UnassignVirtualKeyMCPGroup(ctx context.Context, vkID, groupID string) error
	GetVirtualKeyMCPToolGroups(ctx context.Context, vkID string) ([]tables.TableMCPToolGroup, error)

	// ─── Data Connectors ──────────────────────────────────────────────────────
	CreateConnector(ctx context.Context, c *tables.TableConnector) error
	GetConnector(ctx context.Context, id string) (*tables.TableConnector, error)
	ListConnectors(ctx context.Context, connType string) ([]tables.TableConnector, error)
	UpdateConnector(ctx context.Context, id string, updates map[string]any) error
	DeleteConnector(ctx context.Context, id string) error
	MarkConnectorTested(ctx context.Context, id string, ok bool) error
}

// NewConfigStore creates a new config store based on the configuration
func NewConfigStore(ctx context.Context, config *Config, logger schemas.Logger) (ConfigStore, error) {
	if config == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}
	if !config.Enabled {
		return nil, nil
	}
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
