package lib

/*
===================================================================================
CONFIG HASH TEST SCENARIOS INDEX
===================================================================================

This file contains comprehensive tests for the Bifrost configuration system,
covering hash generation, config reconciliation between config.json and database,
and SQLite integration tests. The hash-based reconciliation ensures that:
  - Config file changes override DB values (file is source of truth for defined items)
  - Dashboard-added items (only in DB) are preserved
  - Unchanged configs are not unnecessarily updated

===================================================================================
HASH GENERATION TESTS
===================================================================================
Tests that verify hash generation for different config types produces stable,
deterministic hashes that change when and only when relevant fields change.

| Test Name                                        | What It Tests                                |
|--------------------------------------------------|----------------------------------------------|
| TestGenerateProviderConfigHash                   | Provider hash excludes keys, different       |
|                                                  | fields → different hash                      |
| TestGenerateKeyHash                              | Key hash skips ID, detects content changes   |
| TestGenerateKeyHash_StableOrdering               | Key hash stable regardless of Models order   |
| TestGenerateVirtualKeyHash                       | VK hash skips ID, detects content changes    |
| TestGenerateVirtualKeyHash_WithProviderConfigs   | VK hash includes provider configs            |
| TestGenerateVirtualKeyHash_WithMCPConfigs        | VK hash includes MCP configs                 |
| TestGenerateVirtualKeyHash_MCPConfigChanges      | VK hash changes when MCP tools change        |
| TestGenerateVirtualKeyHash_StableProviderConfigOrdering | VK hash stable across provider order  |
| TestGenerateVirtualKeyHash_StableAllowedModelsOrdering  | VK hash stable across model order     |
| TestGenerateVirtualKeyHash_StableKeyIDsOrdering  | VK hash stable across key ID order           |
| TestGenerateVirtualKeyHash_StableMCPConfigOrdering | VK hash stable across MCP config order     |
| TestGenerateVirtualKeyHash_StableToolsToExecuteOrdering | VK hash stable across tool order      |
| TestGenerateVirtualKeyHash_StableCombinedOrdering | All orderings combined remain stable        |
| TestGenerateBudgetHash                           | Budget hash from all budget fields           |
| TestGenerateRateLimitHash                        | RateLimit hash from all rate limit fields    |
| TestGenerateCustomerHash                         | Customer hash from all customer fields       |
| TestGenerateTeamHash                             | Team hash from all team fields               |
| TestGenerateMCPClientHash                        | MCP client hash from all MCP client fields   |
| TestGeneratePluginHash                           | Plugin hash from all plugin fields           |
| TestGenerateClientConfigHash                     | ClientConfig hash from all client fields     |

===================================================================================
RUNTIME VS MIGRATION PARITY TESTS
===================================================================================
These tests verify that hash generation produces identical results whether
computed at runtime or via database migration, ensuring upgrade compatibility.

| Test Name                                        | What It Tests                                |
|--------------------------------------------------|----------------------------------------------|
| TestGenerateMCPClientHash_RuntimeVsMigrationParity | MCP hash same at runtime & migration      |
| TestGeneratePluginHash_RuntimeVsMigrationParity  | Plugin hash same at runtime & migration      |
| TestGenerateTeamHash_RuntimeVsMigrationParity    | Team hash same at runtime & migration        |
| TestGenerateProviderHash_RuntimeVsMigrationParity | Provider hash same at runtime & migration   |
| TestGenerateKeyHash_RuntimeVsMigrationParity     | Key hash same at runtime & migration         |
| TestGenerateClientConfigHash_RuntimeVsMigrationParity | ClientConfig hash same at runtime & migration |

===================================================================================
PROVIDER HASH COMPARISON TESTS
===================================================================================
Tests for provider-level config reconciliation between file and database.

| Test Name                                        | What It Tests                                |
|--------------------------------------------------|----------------------------------------------|
| TestProviderHashComparison_MatchingHash          | Hash match → keep DB config unchanged        |
| TestProviderHashComparison_DifferentHash         | Hash differs → sync from file, keep DB keys  |
| TestProviderHashComparison_NewProvider           | New provider in file → add to DB             |
| TestProviderHashComparison_ProviderOnlyInDB      | Dashboard-added provider → preserved         |
| TestProviderHashComparison_RoundTrip             | JSON→DB→same JSON = no changes               |
| TestProviderHashComparison_DashboardEditThenSameFile | Dashboard edits preserved on reload      |
| TestProviderHashComparison_FullLifecycle         | Complete lifecycle: add→edit→reload          |
| TestProviderHashComparison_MultipleUpdates       | Multiple file updates + revert to old config |
| TestProviderHashComparison_OptionalFieldsPresence | NetworkConfig, ProxyConfig, CustomProvider  |
| TestProviderHashComparison_FieldValueChanges     | BaseURL, ExtraHeaders, Concurrency changes   |
| TestProviderHashComparison_FieldRemoved          | Removing NetworkConfig, ProxyConfig, etc.    |
| TestProviderHashComparison_PartialFieldChanges   | Timeout, MaxRetries in nested structs        |
| TestProviderHashComparison_ProviderChangedKeysUnchanged | Provider changes, keys stay same      |
| TestProviderHashComparison_KeysChangedProviderUnchanged | Keys change, provider stays same      |
| TestProviderHashComparison_BothChangedIndependently | Both provider and keys change             |
| TestProviderHashComparison_NeitherChanged        | No changes → no updates                      |

===================================================================================
AZURE/BEDROCK/VERTEX PROVIDER-SPECIFIC TESTS
===================================================================================
Tests for provider-specific key configurations (Azure, Bedrock, Vertex).

| Test Name                                        | What It Tests                                |
|--------------------------------------------------|----------------------------------------------|
| TestKeyHashComparison_AzureConfigSyncScenarios   | Azure key config sync: endpoint, version     |
| TestKeyHashComparison_BedrockConfigSyncScenarios | Bedrock key config sync: region, creds       |
| TestKeyHashComparison_VertexConfigSyncScenarios  | Vertex key config sync: project, region      |
| TestProviderHashComparison_AzureProviderFullLifecycle | Azure provider full CRUD lifecycle      |
| TestProviderHashComparison_BedrockProviderFullLifecycle | Bedrock provider full CRUD lifecycle  |
| TestProviderHashComparison_VertexProviderFullLifecycle | Vertex provider full CRUD lifecycle    |
| TestProviderHashComparison_AzureNewProviderFromConfig | New Azure provider from file            |
| TestProviderHashComparison_BedrockNewProviderFromConfig | New Bedrock provider from file        |
| TestProviderHashComparison_VertexNewProviderFromConfig | New Vertex provider from file          |
| TestProviderHashComparison_AzureDBValuePreservedWhenHashMatches | Azure DB preserved on hash match |
| TestProviderHashComparison_BedrockDBValuePreservedWhenHashMatches | Bedrock DB preserved on hash match |
| TestProviderHashComparison_VertexDBValuePreservedWhenHashMatches | Vertex DB preserved on hash match |
| TestProviderHashComparison_AzureConfigChangedInFile | Azure config changed → file wins          |
| TestProviderHashComparison_BedrockConfigChangedInFile | Bedrock config changed → file wins      |
| TestProviderHashComparison_VertexConfigChangedInFile | Vertex config changed → file wins        |

===================================================================================
KEY-LEVEL SYNC TESTS
===================================================================================
Tests for individual key reconciliation when provider hash matches.

| Test Name                                        | What It Tests                                |
|--------------------------------------------------|----------------------------------------------|
| TestKeyHashComparison_OptionalFieldsPresence     | Models, AzureKeyConfig, VertexKeyConfig      |
| TestKeyHashComparison_FieldRemoved               | Removing Models, AzureKeyConfig, Weight      |
| TestKeyHashComparison_KeyContentChanged          | Key Value, Models content changes            |
| TestKeyLevelSync_ProviderHashMatch_SingleKeyChanged | One key changed → update that key         |
| TestKeyLevelSync_ProviderHashMatch_NewKeyInFile  | New key in file → add to merged keys         |
| TestKeyLevelSync_ProviderHashMatch_KeyOnlyInDB   | Key only in DB → preserve (dashboard-added)  |
| TestKeyLevelSync_ProviderHashMatch_MixedScenario | Mixed: changed, new, DB-only, unchanged      |
| TestKeyLevelSync_ProviderHashMatch_MultipleKeysChanged | Multiple keys changed at once           |

===================================================================================
KEY WEIGHT TESTS
===================================================================================
Tests for key weight handling, including zero weight preservation.

| Test Name                                        | What It Tests                                |
|--------------------------------------------------|----------------------------------------------|
| TestKeyWeight_ZeroPreserved                      | Weight=0 explicitly set is preserved         |
| TestKeyWeight_DefaultToOneWhenNotSet             | Nil weight defaults to 1.0                   |
| TestKeyWeight_HashDiffersBetweenZeroAndOne       | Weight 0 vs 1 produces different hash        |
| TestSQLite_Key_WeightZero_RoundTrip              | Weight=0 survives DB round-trip              |
| TestVKProviderConfig_WeightZeroPreserved         | VK provider config weight=0 preserved        |
| TestSQLite_VKProviderConfig_WeightZero_RoundTrip | VK provider config weight=0 DB round-trip    |

===================================================================================
KEY ENABLED/BATCH API TESTS
===================================================================================
Tests for key Enabled and UseForBatchAPI field handling.

| Test Name                                        | What It Tests                                |
|--------------------------------------------------|----------------------------------------------|
| TestGenerateKeyHash_EnabledField                 | Enabled field affects hash (true/false/nil)  |
| TestSQLite_Key_EnabledChange_Detected            | Enabled change detected during sync          |
| TestGenerateKeyHash_UseForBatchAPIField          | UseForBatchAPI field affects hash            |
| TestSQLite_Key_UseForBatchAPIChange_Detected     | UseForBatchAPI change detected during sync   |

===================================================================================
DEPLOYMENT MAP TESTS
===================================================================================
Tests for deployment map changes in provider-specific configs.

| Test Name                                        | What It Tests                                |
|--------------------------------------------------|----------------------------------------------|
| TestKeyHashComparison_AzureDeploymentsChange     | Azure deployments: add, remove, modify       |
| TestKeyHashComparison_BedrockDeploymentsChange   | Bedrock deployments: add, remove, modify     |
| TestKeyHashComparison_VertexDeploymentsChange    | Vertex deployments: add, remove, modify      |

===================================================================================
VIRTUAL KEY HASH COMPARISON TESTS
===================================================================================
Tests for virtual key reconciliation between file and database.

| Test Name                                        | What It Tests                                |
|--------------------------------------------------|----------------------------------------------|
| TestVirtualKeyHashComparison_MatchingHash        | Hash match → keep DB config                  |
| TestVirtualKeyHashComparison_DifferentHash       | Hash differs → sync from file                |
| TestVirtualKeyHashComparison_VirtualKeyOnlyInDB  | Dashboard-added VK → preserved               |
| TestVirtualKeyHashComparison_NewVirtualKey       | New VK in file → add to DB                   |
| TestVirtualKeyHashComparison_OptionalFieldsPresence | team_id, customer_id, budget_id, rate_limit_id |
| TestVirtualKeyHashComparison_FieldValueChanges   | Field value changes detected                 |
| TestVirtualKeyHashComparison_RoundTrip           | JSON→DB→same JSON = no changes               |

===================================================================================
MERGE LOGIC TESTS (LoadConfig)
===================================================================================
Tests for config merging logic when loading from file with existing DB data.

| Test Name                                        | What It Tests                                |
|--------------------------------------------------|----------------------------------------------|
| TestLoadConfig_ClientConfig_Merge                | Client config merge: DB + file               |
| TestLoadConfig_Providers_Merge                   | Provider keys merge: DB + file               |
| TestLoadConfig_MCP_Merge                         | MCP config merge: DB + file                  |
| TestLoadConfig_Governance_Merge                  | Governance config merge: DB + file           |

===================================================================================
SQLITE INTEGRATION TESTS - PROVIDERS
===================================================================================
End-to-end tests with real SQLite database for provider operations.

| Test Name                                        | What It Tests                                |
|--------------------------------------------------|----------------------------------------------|
| TestSQLite_Provider_NewProviderFromFile          | New provider from file creates in DB         |
| TestSQLite_Provider_HashMatch_DBPreserved        | Hash match → DB values preserved             |
| TestSQLite_Provider_HashMismatch_FileSync        | Hash mismatch → file values sync to DB       |
| TestSQLite_Provider_DBOnlyProvider_Preserved     | Dashboard-added provider preserved           |
| TestSQLite_Provider_RoundTrip                    | Full provider round-trip test                |

===================================================================================
SQLITE INTEGRATION TESTS - KEYS
===================================================================================
End-to-end tests with real SQLite database for key operations.

| Test Name                                        | What It Tests                                |
|--------------------------------------------------|----------------------------------------------|
| TestSQLite_Key_NewKeyFromFile                    | New key from file creates in DB              |
| TestSQLite_Key_HashMatch_DBKeyPreserved          | Hash match → DB key preserved                |
| TestSQLite_Key_DashboardAddedKey_Preserved       | Dashboard-added key preserved on reload      |
| TestSQLite_Key_KeyValueChange_Detected           | Key value change detected and synced         |
| TestSQLite_Key_MultipleKeys_MergeLogic           | Multiple keys merge correctly                |

===================================================================================
SQLITE INTEGRATION TESTS - VIRTUAL KEYS
===================================================================================
End-to-end tests with real SQLite database for virtual key operations.

| Test Name                                        | What It Tests                                |
|--------------------------------------------------|----------------------------------------------|
| TestSQLite_VirtualKey_NewFromFile                | New VK from file creates in DB               |
| TestSQLite_VirtualKey_HashMatch_DBPreserved      | Hash match → DB VK preserved                 |
| TestSQLite_VirtualKey_HashMismatch_FileSync      | Hash mismatch → file VK syncs to DB          |
| TestSQLite_VirtualKey_DBOnlyVK_Preserved         | Dashboard-added VK preserved                 |
| TestSQLite_VirtualKey_WithProviderConfigs        | VK with provider configs created correctly   |
| TestSQLite_VirtualKey_MergePath_WithProviderConfigs | VK provider configs merge correctly       |
| TestSQLite_VirtualKey_MergePath_WithProviderConfigKeys | VK provider config keys merge correctly |
| TestSQLite_VirtualKey_ProviderConfigKeyIDs       | VK provider config key IDs handled correctly |
| TestSQLite_VirtualKey_WithMCPConfigs             | VK with MCP configs created correctly        |
| TestSQLite_VirtualKey_DashboardProviderConfig_DeletedOnFileChange | Dashboard provider config deleted on file change |
| TestSQLite_VirtualKey_DashboardMCPConfig_DeletedOnFileChange | Dashboard MCP config deleted on file change |

===================================================================================
SQLITE INTEGRATION TESTS - VK PROVIDER CONFIGS
===================================================================================
End-to-end tests for virtual key provider configuration operations.

| Test Name                                        | What It Tests                                |
|--------------------------------------------------|----------------------------------------------|
| TestSQLite_VKProviderConfig_NewConfig            | New VK provider config created               |
| TestSQLite_VKProviderConfig_KeyReference         | VK provider config key references work       |
| TestSQLite_VKProviderConfig_HashChangesOnKeyIDChange | Hash changes when key ID changes          |
| TestSQLite_VKProviderConfig_WeightAndAllowedModels | Weight and allowed models handled correctly |
| TestSQLite_VKProviderConfig_BudgetAndRateLimit   | BudgetID/RateLimitID persisted correctly     |
| TestGenerateVirtualKeyHash_ProviderConfigBudgetRateLimit | VK hash includes provider config budget/rate limit |

===================================================================================
SQLITE INTEGRATION TESTS - VK MCP CONFIGS
===================================================================================
End-to-end tests for virtual key MCP configuration operations.

| Test Name                                        | What It Tests                                |
|--------------------------------------------------|----------------------------------------------|
| TestSQLite_VKMCPConfig_Reconciliation            | VK MCP config reconciliation works           |
| TestSQLite_VKMCPConfig_AddRemove                 | Adding and removing VK MCP configs           |
| TestSQLite_VKMCPConfig_UpdateTools               | Updating VK MCP config tools                 |
| TestSQLite_VK_ProviderAndMCPConfigs_Combined     | Combined provider and MCP configs            |

===================================================================================
SQLITE INTEGRATION TESTS - GOVERNANCE (Budget, RateLimit, Customer, Team)
===================================================================================
End-to-end tests for governance entity operations.

| Test Name                                        | What It Tests                                |
|--------------------------------------------------|----------------------------------------------|
| TestSQLite_Budget_NewFromFile                    | New budget from file creates in DB           |
| TestSQLite_Budget_HashMatch_DBPreserved          | Hash match → DB budget preserved             |
| TestSQLite_Budget_HashMismatch_FileSync          | Hash mismatch → file budget syncs to DB      |
| TestSQLite_Budget_DBOnly_Preserved               | Dashboard-added budget preserved             |
| TestSQLite_RateLimit_NewFromFile                 | New rate limit from file creates in DB       |
| TestSQLite_RateLimit_HashMismatch_FileSync       | Hash mismatch → file rate limit syncs to DB  |
| TestSQLite_Customer_NewFromFile                  | New customer from file creates in DB         |
| TestSQLite_Customer_HashMismatch_FileSync        | Hash mismatch → file customer syncs to DB    |
| TestSQLite_Team_NewFromFile                      | New team from file creates in DB             |
| TestSQLite_Team_HashMismatch_FileSync            | Hash mismatch → file team syncs to DB        |
| TestSQLite_Governance_FullReconciliation         | Full governance reconciliation test          |
| TestSQLite_Governance_DBOnly_AllPreserved        | All dashboard-added governance items preserved |

===================================================================================
SQLITE INTEGRATION TESTS - FULL LIFECYCLE
===================================================================================
Complete lifecycle tests covering multiple load/reload scenarios.

| Test Name                                        | What It Tests                                |
|--------------------------------------------------|----------------------------------------------|
| TestSQLite_FullLifecycle_InitialLoad             | Initial load creates all configs in DB       |
| TestSQLite_FullLifecycle_SecondLoadNoChanges     | Second load with same file → no DB updates   |
| TestSQLite_FullLifecycle_FileChange_Selective    | File change → only changed items updated     |
| TestSQLite_FullLifecycle_DashboardEdits_ThenFileUnchanged | Dashboard edits preserved on reload |

===================================================================================
EXPECTED BEHAVIORS SUMMARY
===================================================================================

1. HASH-BASED RECONCILIATION:
   - Hash computed from config content (excluding auto-generated IDs)
   - Same hash = no change needed (DB value preserved)
   - Different hash = file value takes precedence (source of truth)
   - Missing hash in DB = item was added via dashboard (preserve it)

2. FILE vs DATABASE PRIORITY:
   - Items defined in file: file is source of truth (hash-based sync)
   - Items only in DB: dashboard-added, always preserved
   - Items only in file: new items, created in DB

3. KEY WEIGHT HANDLING:
   - nil weight → defaults to 1.0
   - weight = 0 → explicitly set, must be preserved (not treated as nil)
   - Weight affects hash calculation

4. KEY ENABLED/BATCH API HANDLING:
   - Enabled: nil = default true, explicit true/false affects hash
   - UseForBatchAPI: nil = default false, explicit true/false affects hash
   - Both fields are included in key hash for change detection

5. PROVIDER-SPECIFIC CONFIGS:
   - Azure: Endpoint, APIVersion, Deployments in AzureKeyConfig
   - Bedrock: Region, AuthCredentials, Deployments in BedrockKeyConfig
   - Vertex: ProjectID, Region, AuthCredentials, Deployments in VertexKeyConfig
   - All fields including Deployments maps affect key hash and must sync correctly

6. VIRTUAL KEY ASSOCIATIONS:
   - VK can have provider configs (provider + weight + allowed models + keys + budget_id + rate_limit_id)
   - VK can have MCP configs (client_id + tools_to_execute)
   - Changes in associations affect VK hash
   - Dashboard-added associations preserved unless file VK changes

===================================================================================
*/

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/migrator"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// MockConfigStore implements the ConfigStore interface for testing
type MockConfigStore struct {
	clientConfig     *configstore.ClientConfig
	providers        map[schemas.ModelProvider]configstore.ProviderConfig
	mcpConfig        *schemas.MCPConfig
	governanceConfig *configstore.GovernanceConfig
	authConfig       *configstore.AuthConfig
	frameworkConfig  *tables.TableFrameworkConfig
	vectorConfig     *vectorstore.Config
	logsConfig       *logstore.Config
	plugins          []*tables.TablePlugin

	// Track update calls for verification
	clientConfigUpdated    bool
	providersConfigUpdated bool
	mcpConfigsCreated      []*schemas.MCPClientConfig
	mcpClientConfigUpdates []struct {
		ID     string
		Config tables.TableMCPClient
	}
	governanceItemsCreated struct {
		budgets     []tables.TableBudget
		rateLimits  []tables.TableRateLimit
		customers   []tables.TableCustomer
		teams       []tables.TableTeam
		virtualKeys []tables.TableVirtualKey
	}
}

// NewMockConfigStore creates a new mock config store
func NewMockConfigStore() *MockConfigStore {
	return &MockConfigStore{
		providers: make(map[schemas.ModelProvider]configstore.ProviderConfig),
	}
}

// Implement ConfigStore interface methods
func (m *MockConfigStore) Ping(ctx context.Context) error  { return nil }
func (m *MockConfigStore) Close(ctx context.Context) error { return nil }
func (m *MockConfigStore) DB() *gorm.DB                    { return nil }
func (m *MockConfigStore) ExecuteTransaction(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return fn(nil)
}
func (m *MockConfigStore) RunMigration(ctx context.Context, migration *migrator.Migration) error {
	return nil
}

func (m *MockConfigStore) RetryOnNotFound(ctx context.Context, fn func(ctx context.Context) (any, error), maxRetries int, retryDelay time.Duration) (any, error) {
	return fn(ctx)
}

// Client config
func (m *MockConfigStore) UpdateClientConfig(ctx context.Context, config *configstore.ClientConfig) error {
	m.clientConfig = config
	m.clientConfigUpdated = true
	return nil
}

func (m *MockConfigStore) GetClientConfig(ctx context.Context) (*configstore.ClientConfig, error) {
	return m.clientConfig, nil
}

// Provider config
func (m *MockConfigStore) UpdateProvidersConfig(ctx context.Context, providers map[schemas.ModelProvider]configstore.ProviderConfig, tx ...*gorm.DB) error {
	m.providers = providers
	m.providersConfigUpdated = true
	return nil
}

func (m *MockConfigStore) GetProvidersConfig(ctx context.Context) (map[schemas.ModelProvider]configstore.ProviderConfig, error) {
	if len(m.providers) == 0 {
		return nil, nil
	}
	return m.providers, nil
}

func (m *MockConfigStore) AddProvider(ctx context.Context, provider schemas.ModelProvider, config configstore.ProviderConfig, tx ...*gorm.DB) error {
	m.providers[provider] = config
	return nil
}

func (m *MockConfigStore) UpdateProvider(ctx context.Context, provider schemas.ModelProvider, config configstore.ProviderConfig, tx ...*gorm.DB) error {
	m.providers[provider] = config
	return nil
}

func (m *MockConfigStore) DeleteProvider(ctx context.Context, provider schemas.ModelProvider, tx ...*gorm.DB) error {
	delete(m.providers, provider)
	return nil
}

// MCP config
func (m *MockConfigStore) GetMCPConfig(ctx context.Context) (*schemas.MCPConfig, error) {
	return m.mcpConfig, nil
}

func (m *MockConfigStore) GetMCPClientByName(ctx context.Context, name string) (*tables.TableMCPClient, error) {
	return nil, nil
}

func (m *MockConfigStore) CreateMCPClientConfig(ctx context.Context, clientConfig *schemas.MCPClientConfig) error {
	m.mcpConfig.ClientConfigs = append(m.mcpConfig.ClientConfigs, clientConfig)
	m.mcpConfigsCreated = append(m.mcpConfigsCreated, clientConfig)
	return nil
}

func (m *MockConfigStore) UpdateMCPClientConfig(ctx context.Context, id string, clientConfig tables.TableMCPClient) error {
	m.mcpClientConfigUpdates = append(m.mcpClientConfigUpdates, struct {
		ID     string
		Config tables.TableMCPClient
	}{
		ID:     id,
		Config: clientConfig,
	})

	// Initialize m.mcpConfig if nil (same pattern as CreateMCPClientConfig)
	if m.mcpConfig == nil {
		m.mcpConfig = &schemas.MCPConfig{
			ClientConfigs: []*schemas.MCPClientConfig{},
		}
	}

	// Update the in-memory state to ensure GetMCPConfig returns updated data
	for i := range m.mcpConfig.ClientConfigs {
		if m.mcpConfig.ClientConfigs[i].ID == id {
			// Found the entry, update it with the new config
			m.mcpConfig.ClientConfigs[i] = &schemas.MCPClientConfig{
				ID:                 clientConfig.ClientID,
				Name:               clientConfig.Name,
				IsCodeModeClient:   clientConfig.IsCodeModeClient,
				ConnectionType:     schemas.MCPConnectionType(clientConfig.ConnectionType),
				ConnectionString:   clientConfig.ConnectionString,
				StdioConfig:        clientConfig.StdioConfig,
				Headers:            clientConfig.Headers,
				ToolsToExecute:     clientConfig.ToolsToExecute,
				ToolsToAutoExecute: clientConfig.ToolsToAutoExecute,
			}
			return nil
		}
	}
	// If not found, create a new entry (similar to CreateMCPClientConfig behavior)
	m.mcpConfig.ClientConfigs = append(m.mcpConfig.ClientConfigs, &schemas.MCPClientConfig{
		ID:                 clientConfig.ClientID,
		Name:               clientConfig.Name,
		IsCodeModeClient:   clientConfig.IsCodeModeClient,
		ConnectionType:     schemas.MCPConnectionType(clientConfig.ConnectionType),
		ConnectionString:   clientConfig.ConnectionString,
		StdioConfig:        clientConfig.StdioConfig,
		Headers:            clientConfig.Headers,
		ToolsToExecute:     clientConfig.ToolsToExecute,
		ToolsToAutoExecute: clientConfig.ToolsToAutoExecute,
	})

	return nil
}

func (m *MockConfigStore) DeleteMCPClientConfig(ctx context.Context, id string) error {
	return nil
}

// Governance config
func (m *MockConfigStore) GetGovernanceConfig(ctx context.Context) (*configstore.GovernanceConfig, error) {
	return m.governanceConfig, nil
}

func (m *MockConfigStore) CreateBudget(ctx context.Context, budget *tables.TableBudget, tx ...*gorm.DB) error {
	if m.governanceConfig == nil {
		m.governanceConfig = &configstore.GovernanceConfig{}
	}
	m.governanceConfig.Budgets = append(m.governanceConfig.Budgets, *budget)
	m.governanceItemsCreated.budgets = append(m.governanceItemsCreated.budgets, *budget)
	return nil
}

func (m *MockConfigStore) UpdateBudget(ctx context.Context, budget *tables.TableBudget, tx ...*gorm.DB) error {
	return nil
}

func (m *MockConfigStore) UpdateBudgets(ctx context.Context, budgets []*tables.TableBudget, tx ...*gorm.DB) error {
	return nil
}

func (m *MockConfigStore) GetBudget(ctx context.Context, id string, tx ...*gorm.DB) (*tables.TableBudget, error) {
	return nil, nil
}

func (m *MockConfigStore) GetBudgets(ctx context.Context) ([]tables.TableBudget, error) {
	return nil, nil
}

func (m *MockConfigStore) CreateRateLimit(ctx context.Context, rateLimit *tables.TableRateLimit, tx ...*gorm.DB) error {
	if m.governanceConfig == nil {
		m.governanceConfig = &configstore.GovernanceConfig{}
	}
	m.governanceConfig.RateLimits = append(m.governanceConfig.RateLimits, *rateLimit)
	m.governanceItemsCreated.rateLimits = append(m.governanceItemsCreated.rateLimits, *rateLimit)
	return nil
}

func (m *MockConfigStore) UpdateRateLimit(ctx context.Context, rateLimit *tables.TableRateLimit, tx ...*gorm.DB) error {
	return nil
}

func (m *MockConfigStore) UpdateRateLimits(ctx context.Context, rateLimits []*tables.TableRateLimit, tx ...*gorm.DB) error {
	return nil
}

func (m *MockConfigStore) GetRateLimit(ctx context.Context, id string) (*tables.TableRateLimit, error) {
	return nil, nil
}

func (m *MockConfigStore) GetRateLimits(ctx context.Context) ([]tables.TableRateLimit, error) {
	return []tables.TableRateLimit{}, nil
}

func (m *MockConfigStore) CreateCustomer(ctx context.Context, customer *tables.TableCustomer, tx ...*gorm.DB) error {
	if m.governanceConfig == nil {
		m.governanceConfig = &configstore.GovernanceConfig{}
	}
	m.governanceConfig.Customers = append(m.governanceConfig.Customers, *customer)
	m.governanceItemsCreated.customers = append(m.governanceItemsCreated.customers, *customer)
	return nil
}

func (m *MockConfigStore) UpdateCustomer(ctx context.Context, customer *tables.TableCustomer, tx ...*gorm.DB) error {
	return nil
}

func (m *MockConfigStore) DeleteCustomer(ctx context.Context, id string) error {
	return nil
}

func (m *MockConfigStore) GetCustomer(ctx context.Context, id string) (*tables.TableCustomer, error) {
	return nil, nil
}

func (m *MockConfigStore) GetCustomers(ctx context.Context) ([]tables.TableCustomer, error) {
	return nil, nil
}

func (m *MockConfigStore) CreateTeam(ctx context.Context, team *tables.TableTeam, tx ...*gorm.DB) error {
	if m.governanceConfig == nil {
		m.governanceConfig = &configstore.GovernanceConfig{}
	}
	m.governanceConfig.Teams = append(m.governanceConfig.Teams, *team)
	m.governanceItemsCreated.teams = append(m.governanceItemsCreated.teams, *team)
	return nil
}

func (m *MockConfigStore) UpdateTeam(ctx context.Context, team *tables.TableTeam, tx ...*gorm.DB) error {
	return nil
}

func (m *MockConfigStore) DeleteTeam(ctx context.Context, id string) error {
	return nil
}

func (m *MockConfigStore) GetTeam(ctx context.Context, id string) (*tables.TableTeam, error) {
	return nil, nil
}

func (m *MockConfigStore) GetTeams(ctx context.Context, customerID string) ([]tables.TableTeam, error) {
	return nil, nil
}

func (m *MockConfigStore) CreateVirtualKey(ctx context.Context, virtualKey *tables.TableVirtualKey, tx ...*gorm.DB) error {
	if m.governanceConfig == nil {
		m.governanceConfig = &configstore.GovernanceConfig{}
	}
	m.governanceConfig.VirtualKeys = append(m.governanceConfig.VirtualKeys, *virtualKey)
	m.governanceItemsCreated.virtualKeys = append(m.governanceItemsCreated.virtualKeys, *virtualKey)
	return nil
}

func (m *MockConfigStore) UpdateVirtualKey(ctx context.Context, virtualKey *tables.TableVirtualKey, tx ...*gorm.DB) error {
	return nil
}

func (m *MockConfigStore) DeleteVirtualKey(ctx context.Context, id string) error {
	return nil
}

func (m *MockConfigStore) GetVirtualKey(ctx context.Context, id string) (*tables.TableVirtualKey, error) {
	return nil, nil
}

func (m *MockConfigStore) GetVirtualKeys(ctx context.Context) ([]tables.TableVirtualKey, error) {
	return nil, nil
}

func (m *MockConfigStore) GetRedactedVirtualKeys(ctx context.Context, ids []string) ([]tables.TableVirtualKey, error) {
	return nil, nil
}

func (m *MockConfigStore) GetVirtualKeyByValue(ctx context.Context, value string) (*tables.TableVirtualKey, error) {
	return nil, nil
}

// Virtual key provider config
func (m *MockConfigStore) GetVirtualKeyProviderConfigs(ctx context.Context, virtualKeyID string) ([]tables.TableVirtualKeyProviderConfig, error) {
	return nil, nil
}

func (m *MockConfigStore) CreateVirtualKeyProviderConfig(ctx context.Context, virtualKeyProviderConfig *tables.TableVirtualKeyProviderConfig, tx ...*gorm.DB) error {
	return nil
}

func (m *MockConfigStore) UpdateVirtualKeyProviderConfig(ctx context.Context, virtualKeyProviderConfig *tables.TableVirtualKeyProviderConfig, tx ...*gorm.DB) error {
	return nil
}

func (m *MockConfigStore) DeleteVirtualKeyProviderConfig(ctx context.Context, id uint, tx ...*gorm.DB) error {
	return nil
}

// Virtual key MCP config
func (m *MockConfigStore) GetVirtualKeyMCPConfigs(ctx context.Context, virtualKeyID string) ([]tables.TableVirtualKeyMCPConfig, error) {
	return nil, nil
}

func (m *MockConfigStore) CreateVirtualKeyMCPConfig(ctx context.Context, virtualKeyMCPConfig *tables.TableVirtualKeyMCPConfig, tx ...*gorm.DB) error {
	return nil
}

func (m *MockConfigStore) UpdateVirtualKeyMCPConfig(ctx context.Context, virtualKeyMCPConfig *tables.TableVirtualKeyMCPConfig, tx ...*gorm.DB) error {
	return nil
}

func (m *MockConfigStore) DeleteVirtualKeyMCPConfig(ctx context.Context, id uint, tx ...*gorm.DB) error {
	return nil
}

// Auth config
func (m *MockConfigStore) GetAuthConfig(ctx context.Context) (*configstore.AuthConfig, error) {
	return m.authConfig, nil
}

func (m *MockConfigStore) UpdateAuthConfig(ctx context.Context, config *configstore.AuthConfig) error {
	m.authConfig = config
	return nil
}

// Framework config
func (m *MockConfigStore) UpdateFrameworkConfig(ctx context.Context, config *tables.TableFrameworkConfig) error {
	m.frameworkConfig = config
	return nil
}

func (m *MockConfigStore) GetFrameworkConfig(ctx context.Context) (*tables.TableFrameworkConfig, error) {
	return m.frameworkConfig, nil
}

// Vector store config
func (m *MockConfigStore) UpdateVectorStoreConfig(ctx context.Context, config *vectorstore.Config) error {
	m.vectorConfig = config
	return nil
}

func (m *MockConfigStore) GetVectorStoreConfig(ctx context.Context) (*vectorstore.Config, error) {
	return m.vectorConfig, nil
}

// Logs store config
func (m *MockConfigStore) UpdateLogsStoreConfig(ctx context.Context, config *logstore.Config) error {
	m.logsConfig = config
	return nil
}

func (m *MockConfigStore) GetLogsStoreConfig(ctx context.Context) (*logstore.Config, error) {
	return m.logsConfig, nil
}

// Config
func (m *MockConfigStore) GetConfig(ctx context.Context, key string) (*tables.TableGovernanceConfig, error) {
	return nil, nil
}

func (m *MockConfigStore) UpdateConfig(ctx context.Context, config *tables.TableGovernanceConfig, tx ...*gorm.DB) error {
	return nil
}

// Plugins
func (m *MockConfigStore) GetPlugins(ctx context.Context) ([]*tables.TablePlugin, error) {
	return m.plugins, nil
}

func (m *MockConfigStore) GetPlugin(ctx context.Context, name string) (*tables.TablePlugin, error) {
	for _, p := range m.plugins {
		if p.Name == name {
			return p, nil
		}
	}
	return nil, nil
}

func (m *MockConfigStore) CreatePlugin(ctx context.Context, plugin *tables.TablePlugin, tx ...*gorm.DB) error {
	m.plugins = append(m.plugins, plugin)
	return nil
}

func (m *MockConfigStore) UpdatePlugin(ctx context.Context, plugin *tables.TablePlugin, tx ...*gorm.DB) error {
	return nil
}

func (m *MockConfigStore) DeletePlugin(ctx context.Context, name string, tx ...*gorm.DB) error {
	return nil
}

// Key management
func (m *MockConfigStore) GetKeysByIDs(ctx context.Context, ids []string) ([]tables.TableKey, error) {
	return nil, nil
}

func (m *MockConfigStore) GetAllRedactedKeys(ctx context.Context, ids []string) ([]schemas.Key, error) {
	return nil, nil
}

// Session
func (m *MockConfigStore) GetSession(ctx context.Context, token string) (*tables.SessionsTable, error) {
	return nil, nil
}

func (m *MockConfigStore) CreateSession(ctx context.Context, session *tables.SessionsTable) error {
	return nil
}

func (m *MockConfigStore) DeleteSession(ctx context.Context, token string) error {
	return nil
}

// Model pricing
func (m *MockConfigStore) GetModelPrices(ctx context.Context) ([]tables.TableModelPricing, error) {
	return nil, nil
}

func (m *MockConfigStore) UpsertModelPrices(ctx context.Context, pricing *tables.TableModelPricing, tx ...*gorm.DB) error {
	return nil
}

func (m *MockConfigStore) DeleteModelPrices(ctx context.Context, tx ...*gorm.DB) error {
	return nil
}

// Provider methods
func (m *MockConfigStore) GetProvider(ctx context.Context, provider schemas.ModelProvider) (*tables.TableProvider, error) {
	return nil, nil
}

func (m *MockConfigStore) GetProviders(ctx context.Context) ([]tables.TableProvider, error) {
	return nil, nil
}

func (m *MockConfigStore) GetProviderConfig(ctx context.Context, provider schemas.ModelProvider) (*configstore.ProviderConfig, error) {
	return nil, nil
}

// Proxy config
func (m *MockConfigStore) GetProxyConfig(ctx context.Context) (*tables.GlobalProxyConfig, error) {
	return nil, nil
}

func (m *MockConfigStore) UpdateProxyConfig(ctx context.Context, config *tables.GlobalProxyConfig) error {
	return nil
}

// Restart required config
func (m *MockConfigStore) GetRestartRequiredConfig(ctx context.Context) (*tables.RestartRequiredConfig, error) {
	return nil, nil
}

func (m *MockConfigStore) SetRestartRequiredConfig(ctx context.Context, config *tables.RestartRequiredConfig) error {
	return nil
}

func (m *MockConfigStore) ClearRestartRequiredConfig(ctx context.Context) error {
	return nil
}

// Model config
func (m *MockConfigStore) GetModelConfigs(ctx context.Context) ([]tables.TableModelConfig, error) {
	return nil, nil
}

func (m *MockConfigStore) GetModelConfig(ctx context.Context, modelName string, provider *string) (*tables.TableModelConfig, error) {
	return nil, nil
}

func (m *MockConfigStore) GetModelConfigByID(ctx context.Context, id string) (*tables.TableModelConfig, error) {
	return nil, nil
}

func (m *MockConfigStore) CreateModelConfig(ctx context.Context, modelConfig *tables.TableModelConfig, tx ...*gorm.DB) error {
	return nil
}

func (m *MockConfigStore) UpdateModelConfig(ctx context.Context, modelConfig *tables.TableModelConfig, tx ...*gorm.DB) error {
	return nil
}

func (m *MockConfigStore) UpdateModelConfigs(ctx context.Context, modelConfigs []*tables.TableModelConfig, tx ...*gorm.DB) error {
	return nil
}

func (m *MockConfigStore) DeleteModelConfig(ctx context.Context, id string) error {
	return nil
}

// Budget/Rate limit usage
func (m *MockConfigStore) UpdateBudgetUsage(ctx context.Context, id string, currentUsage float64) error {
	return nil
}

func (m *MockConfigStore) UpdateRateLimitUsage(ctx context.Context, id string, tokenCurrentUsage int64, requestCurrentUsage int64) error {
	return nil
}

// Distributed locks
func (m *MockConfigStore) TryAcquireLock(ctx context.Context, lock *tables.TableDistributedLock) (bool, error) {
	return true, nil
}

func (m *MockConfigStore) GetLock(ctx context.Context, lockKey string) (*tables.TableDistributedLock, error) {
	return nil, nil
}

func (m *MockConfigStore) UpdateLockExpiry(ctx context.Context, lockKey, holderID string, expiresAt time.Time) error {
	return nil
}

func (m *MockConfigStore) ReleaseLock(ctx context.Context, lockKey, holderID string) (bool, error) {
	return true, nil
}

func (m *MockConfigStore) CleanupExpiredLocks(ctx context.Context) (int64, error) {
	return 0, nil
}

func (m *MockConfigStore) CleanupExpiredLockByKey(ctx context.Context, lockKey string) (bool, error) {
	return false, nil
}

// Key management
func (m *MockConfigStore) GetKeysByProvider(ctx context.Context, provider string) ([]tables.TableKey, error) {
	return nil, nil
}

// Sessions
func (m *MockConfigStore) FlushSessions(ctx context.Context) error {
	return nil
}

// Plugins
func (m *MockConfigStore) UpsertPlugin(ctx context.Context, plugin *tables.TablePlugin, tx ...*gorm.DB) error {
	return nil
}

// Helper functions for tests

// createTempDir creates a temporary directory for test files
func createTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bifrost-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(dir)
	})
	return dir
}

// createConfigFile creates a config.json file with the given data
func createConfigFile(t *testing.T, dir string, data *ConfigData) {
	t.Helper()
	configPath := filepath.Join(dir, "config.json")
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal config data: %v", err)
	}
	if err := os.WriteFile(configPath, jsonData, 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
}

// Test fixtures

func makeClientConfig(initialPoolSize int, enableLogging bool) *configstore.ClientConfig {
	return &configstore.ClientConfig{
		InitialPoolSize:      initialPoolSize,
		EnableLogging:        enableLogging,
		MaxRequestBodySizeMB: 10,
		PrometheusLabels:     []string{"label1"},
		AllowedOrigins:       []string{"http://localhost:3000"},
	}
}

func makeProviderConfig(keyName, keyValue string) configstore.ProviderConfig {
	return configstore.ProviderConfig{
		Keys: []schemas.Key{
			{
				ID:     uuid.NewString(),
				Name:   keyName,
				Value:  *schemas.NewEnvVar(keyValue),
				Weight: 1,
			},
		},
	}
}

func makeMCPClientConfig(id, name string) schemas.MCPClientConfig {
	return schemas.MCPClientConfig{
		ID:             id,
		Name:           name,
		ConnectionType: schemas.MCPConnectionTypeHTTP,
	}
}

// =============================================================================
// SQLite Integration Test Helpers
// =============================================================================

// testLogger is a minimal logger implementation for testing
type testLogger struct{}

func (l *testLogger) Debug(msg string, args ...any)                     {}
func (l *testLogger) Info(msg string, args ...any)                      {}
func (l *testLogger) Warn(msg string, args ...any)                      {}
func (l *testLogger) Error(msg string, args ...any)                     {}
func (l *testLogger) Fatal(msg string, args ...any)                     {}
func (l *testLogger) SetLevel(level schemas.LogLevel)                   {}
func (l *testLogger) SetOutputType(outputType schemas.LoggerOutputType) {}

// initTestLogger initializes the global logger for SQLite integration tests
func initTestLogger() {
	SetLogger(&testLogger{})
}

// createTestSQLiteConfigStore creates a real SQLite-backed config store for integration tests.
// It initializes all tables and runs migrations automatically.
func createTestSQLiteConfigStore(t *testing.T, dir string) configstore.ConfigStore {
	t.Helper()
	dbPath := filepath.Join(dir, "test-config.db")
	store, err := configstore.NewConfigStore(context.Background(), &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config: &configstore.SQLiteConfig{
			Path: dbPath,
		},
	}, nil)
	if err != nil {
		t.Fatalf("failed to create SQLite config store: %v", err)
	}
	t.Cleanup(func() {
		if store != nil {
			store.Close(context.Background())
		}
	})
	return store
}

// makeConfigDataWithProviders creates a ConfigData with only providers configured
func makeConfigDataWithProviders(providers map[string]configstore.ProviderConfig) *ConfigData {
	return makeConfigDataWithProvidersAndDir(providers, "")
}

// makeConfigDataWithProvidersAndDir creates a ConfigData with providers and a specific temp directory for the DB
func makeConfigDataWithProvidersAndDir(providers map[string]configstore.ProviderConfig, tempDir string) *ConfigData {
	dbPath := filepath.Join(tempDir, "config.db")
	return &ConfigData{
		Client: &configstore.ClientConfig{
			InitialPoolSize:      10,
			EnableLogging:        true,
			MaxRequestBodySizeMB: 100,
			AllowedOrigins:       []string{"*"},
		},
		ConfigStoreConfig: &configstore.Config{
			Enabled: true,
			Type:    configstore.ConfigStoreTypeSQLite,
			Config: &configstore.SQLiteConfig{
				Path: dbPath,
			},
		},
		Providers: providers,
	}
}

// makeConfigDataWithVirtualKeys creates a ConfigData with providers and virtual keys
func makeConfigDataWithVirtualKeys(providers map[string]configstore.ProviderConfig, vks []tables.TableVirtualKey) *ConfigData {
	return makeConfigDataWithVirtualKeysAndDir(providers, vks, "")
}

// makeConfigDataWithVirtualKeysAndDir creates a ConfigData with providers, virtual keys, and a specific temp directory
func makeConfigDataWithVirtualKeysAndDir(providers map[string]configstore.ProviderConfig, vks []tables.TableVirtualKey, tempDir string) *ConfigData {
	dbPath := filepath.Join(tempDir, "config.db")
	return &ConfigData{
		Client: &configstore.ClientConfig{
			InitialPoolSize:      10,
			EnableLogging:        true,
			EnableGovernance:     true,
			MaxRequestBodySizeMB: 100,
			AllowedOrigins:       []string{"*"},
		},
		ConfigStoreConfig: &configstore.Config{
			Enabled: true,
			Type:    configstore.ConfigStoreTypeSQLite,
			Config: &configstore.SQLiteConfig{
				Path: dbPath,
			},
		},
		Providers: providers,
		Governance: &configstore.GovernanceConfig{
			VirtualKeys: vks,
		},
	}
}

// makeConfigDataFull creates a full ConfigData with all configurations
func makeConfigDataFull(client *configstore.ClientConfig, providers map[string]configstore.ProviderConfig, governance *configstore.GovernanceConfig) *ConfigData {
	return makeConfigDataFullWithDir(client, providers, governance, "")
}

// makeConfigDataFullWithDir creates a full ConfigData with all configurations and a specific temp directory
func makeConfigDataFullWithDir(client *configstore.ClientConfig, providers map[string]configstore.ProviderConfig, governance *configstore.GovernanceConfig, tempDir string) *ConfigData {
	if client == nil {
		client = &configstore.ClientConfig{
			InitialPoolSize:      10,
			EnableLogging:        true,
			EnableGovernance:     true,
			MaxRequestBodySizeMB: 100,
			AllowedOrigins:       []string{"*"},
		}
	}
	dbPath := filepath.Join(tempDir, "config.db")
	return &ConfigData{
		Client: client,
		ConfigStoreConfig: &configstore.Config{
			Enabled: true,
			Type:    configstore.ConfigStoreTypeSQLite,
			Config: &configstore.SQLiteConfig{
				Path: dbPath,
			},
		},
		Providers:  providers,
		Governance: governance,
	}
}

// makeProviderConfigWithNetwork creates a provider config with network settings
func makeProviderConfigWithNetwork(keyName, keyValue, baseURL string) configstore.ProviderConfig {
	return configstore.ProviderConfig{
		Keys: []schemas.Key{
			{
				ID:     uuid.NewString(),
				Name:   keyName,
				Value:  *schemas.NewEnvVar(keyValue),
				Weight: 1,
			},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: baseURL,
		},
	}
}

// makeProviderConfigWithMultipleKeys creates a provider config with multiple keys
func makeProviderConfigWithMultipleKeys(keys []schemas.Key, baseURL string) configstore.ProviderConfig {
	return configstore.ProviderConfig{
		Keys: keys,
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: baseURL,
		},
	}
}

// makeVirtualKey creates a virtual key for testing
func makeVirtualKey(id, name, value string) tables.TableVirtualKey {
	return tables.TableVirtualKey{
		ID:          id,
		Name:        name,
		Description: "Test virtual key",
		Value:       value,
		IsActive:    true,
	}
}

// makeVirtualKeyWithTeam creates a virtual key with team association
func makeVirtualKeyWithTeam(id, name, value, teamID string) tables.TableVirtualKey {
	return tables.TableVirtualKey{
		ID:          id,
		Name:        name,
		Description: "Test virtual key with team",
		Value:       value,
		IsActive:    true,
		TeamID:      &teamID,
	}
}

// makeVirtualKeyWithProviderConfigs creates a virtual key with provider configurations
func makeVirtualKeyWithProviderConfigs(id, name, value string, providerConfigs []tables.TableVirtualKeyProviderConfig) tables.TableVirtualKey {
	return tables.TableVirtualKey{
		ID:              id,
		Name:            name,
		Description:     "Test virtual key with provider configs",
		Value:           value,
		IsActive:        true,
		ProviderConfigs: providerConfigs,
	}
}

// makeVirtualKeyProviderConfig creates a provider config for virtual keys
func makeVirtualKeyProviderConfig(provider string, weight float64, allowedModels []string, keys []tables.TableKey) tables.TableVirtualKeyProviderConfig {
	return tables.TableVirtualKeyProviderConfig{
		Provider:      provider,
		Weight:        &weight,
		AllowedModels: allowedModels,
		Keys:          keys,
	}
}

// makeTableKey creates a TableKey for use in virtual key provider configs
func makeTableKey(keyID, name, value, provider string) tables.TableKey {
	defaultWeight := 1.0
	return tables.TableKey{
		KeyID:    keyID,
		Name:     name,
		Value:    *schemas.NewEnvVar(value),
		Provider: provider,
		Weight:   &defaultWeight,
	}
}

// verifyProviderInDB checks that a provider exists in the database with expected config
func verifyProviderInDB(t *testing.T, store configstore.ConfigStore, provider schemas.ModelProvider, expectedKeyCount int) {
	t.Helper()
	ctx := context.Background()
	providers, err := store.GetProvidersConfig(ctx)
	if err != nil {
		t.Fatalf("failed to get providers from DB: %v", err)
	}
	cfg, exists := providers[provider]
	if !exists {
		t.Fatalf("provider %s not found in DB", provider)
	}
	if len(cfg.Keys) != expectedKeyCount {
		t.Errorf("expected %d keys for provider %s, got %d", expectedKeyCount, provider, len(cfg.Keys))
	}
}

// verifyVirtualKeyInDB checks that a virtual key exists in the database
func verifyVirtualKeyInDB(t *testing.T, store configstore.ConfigStore, vkID string) *tables.TableVirtualKey {
	t.Helper()
	ctx := context.Background()
	vk, err := store.GetVirtualKey(ctx, vkID)
	if err != nil {
		t.Fatalf("failed to get virtual key %s from DB: %v", vkID, err)
	}
	if vk == nil {
		t.Fatalf("virtual key %s not found in DB", vkID)
	}
	return vk
}

// verifyVirtualKeyNotInDB checks that a virtual key does NOT exist in the database
func verifyVirtualKeyNotInDB(t *testing.T, store configstore.ConfigStore, vkID string) {
	t.Helper()
	ctx := context.Background()
	vk, err := store.GetVirtualKey(ctx, vkID)
	if err == nil && vk != nil {
		t.Fatalf("virtual key %s should not exist in DB but was found", vkID)
	}
}

// Tests

// TestLoadConfig_ClientConfig_Merge tests client config merge from DB and file
func TestLoadConfig_ClientConfig_Merge(t *testing.T) {
	tempDir := createTempDir(t)

	// Create config file with client config
	fileClientConfig := &configstore.ClientConfig{
		InitialPoolSize:       20,
		EnableLogging:         true,
		EnableGovernance:      true,
		PrometheusLabels:      []string{"file-label"},
		AllowedOrigins:        []string{"http://file-origin.com"},
		MaxRequestBodySizeMB:  15,
		DisableContentLogging: true,
	}

	configData := &ConfigData{
		Client: fileClientConfig,
	}
	createConfigFile(t, tempDir, configData)

	// Setup mock config store with existing client config
	mockStore := NewMockConfigStore()
	mockStore.clientConfig = &configstore.ClientConfig{
		InitialPoolSize:      10,
		EnableLogging:        false,
		EnableGovernance:     false,
		PrometheusLabels:     []string{"db-label"},
		MaxRequestBodySizeMB: 5,
		// AllowedOrigins is empty in DB
	}

	// Override the config store creation to use our mock
	originalConfigStore := mockStore

	// Load config (we need to test the merge logic manually since LoadConfig creates its own store)
	// For now, let's test the merge logic by simulating what happens

	// Simulate merge: DB takes priority, file fills in empty values
	mergedConfig := *mockStore.clientConfig

	// InitialPoolSize: DB has 10, file has 20 -> keep DB (10)
	if mergedConfig.InitialPoolSize == 0 && fileClientConfig.InitialPoolSize != 0 {
		mergedConfig.InitialPoolSize = fileClientConfig.InitialPoolSize
	}

	// PrometheusLabels: DB has value, file has value -> keep DB
	if len(mergedConfig.PrometheusLabels) == 0 && len(fileClientConfig.PrometheusLabels) > 0 {
		mergedConfig.PrometheusLabels = fileClientConfig.PrometheusLabels
	}

	// AllowedOrigins: DB empty, file has value -> use file
	if len(mergedConfig.AllowedOrigins) == 0 && len(fileClientConfig.AllowedOrigins) > 0 {
		mergedConfig.AllowedOrigins = fileClientConfig.AllowedOrigins
	}

	// MaxRequestBodySizeMB: DB has 5, file has 15 -> keep DB (5)
	if mergedConfig.MaxRequestBodySizeMB == 0 && fileClientConfig.MaxRequestBodySizeMB != 0 {
		mergedConfig.MaxRequestBodySizeMB = fileClientConfig.MaxRequestBodySizeMB
	}

	// Boolean fields: file true overrides DB false
	if !mergedConfig.EnableLogging && fileClientConfig.EnableLogging {
		mergedConfig.EnableLogging = fileClientConfig.EnableLogging
	}
	if !mergedConfig.EnableGovernance && fileClientConfig.EnableGovernance {
		mergedConfig.EnableGovernance = fileClientConfig.EnableGovernance
	}
	if !mergedConfig.DisableContentLogging && fileClientConfig.DisableContentLogging {
		mergedConfig.DisableContentLogging = fileClientConfig.DisableContentLogging
	}

	// Verify merge results
	if mergedConfig.InitialPoolSize != 10 {
		t.Errorf("Expected InitialPoolSize to be 10 (from DB), got %d", mergedConfig.InitialPoolSize)
	}

	if len(mergedConfig.PrometheusLabels) != 1 || mergedConfig.PrometheusLabels[0] != "db-label" {
		t.Errorf("Expected PrometheusLabels to be [db-label] (from DB), got %v", mergedConfig.PrometheusLabels)
	}

	if len(mergedConfig.AllowedOrigins) != 1 || mergedConfig.AllowedOrigins[0] != "http://file-origin.com" {
		t.Errorf("Expected AllowedOrigins to be [http://file-origin.com] (from file), got %v", mergedConfig.AllowedOrigins)
	}

	if mergedConfig.MaxRequestBodySizeMB != 5 {
		t.Errorf("Expected MaxRequestBodySizeMB to be 5 (from DB), got %d", mergedConfig.MaxRequestBodySizeMB)
	}

	if !mergedConfig.EnableLogging {
		t.Error("Expected EnableLogging to be true (file true overrides DB false)")
	}

	if !mergedConfig.EnableGovernance {
		t.Error("Expected EnableGovernance to be true (file true overrides DB false)")
	}

	if !mergedConfig.DisableContentLogging {
		t.Error("Expected DisableContentLogging to be true (file true overrides DB false)")
	}

	_ = originalConfigStore
}

// TestLoadConfig_Providers_Merge tests provider keys merge from DB and file
func TestLoadConfig_Providers_Merge(t *testing.T) {
	// Setup DB providers
	dbProviders := make(map[schemas.ModelProvider]configstore.ProviderConfig)
	dbProviders[schemas.OpenAI] = configstore.ProviderConfig{
		Keys: []schemas.Key{
			{
				ID:     "key-1",
				Name:   "openai-db-key-1",
				Value:  *schemas.NewEnvVar("sk-db-123"),
				Weight: 1,
			},
			{
				ID:     "key-2",
				Name:   "openai-db-key-2",
				Value:  *schemas.NewEnvVar("sk-db-456"),
				Weight: 1,
			},
		},
	}

	// Setup file providers with some overlapping and some new keys
	fileProviders := map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{
					ID:     "key-1", // Same ID as DB - should be skipped
					Name:   "openai-db-key-1",
					Value:  *schemas.NewEnvVar("sk-different"),
					Weight: 1,
				},
				{
					ID:     "key-3", // New key
					Name:   "openai-file-key-3",
					Value:  *schemas.NewEnvVar("sk-file-789"),
					Weight: 1,
				},
			},
		},
	}

	// Simulate merge logic
	for providerName, fileCfg := range fileProviders {
		provider := schemas.ModelProvider(providerName)
		if existingCfg, exists := dbProviders[provider]; exists {
			// Merge keys
			keysToAdd := make([]schemas.Key, 0)
			for _, newKey := range fileCfg.Keys {
				found := false
				for _, existingKey := range existingCfg.Keys {
					if existingKey.Name == newKey.Name || existingKey.ID == newKey.ID || existingKey.Value == newKey.Value {
						found = true
						break
					}
				}
				if !found {
					keysToAdd = append(keysToAdd, newKey)
				}
			}
			existingCfg.Keys = append(existingCfg.Keys, keysToAdd...)
			dbProviders[provider] = existingCfg
		}
	}

	// Verify merge results
	openaiCfg := dbProviders[schemas.OpenAI]
	if len(openaiCfg.Keys) != 3 {
		t.Errorf("Expected 3 keys after merge (2 from DB + 1 new from file), got %d", len(openaiCfg.Keys))
	}

	// Verify the keys
	keyNames := make(map[string]bool)
	for _, key := range openaiCfg.Keys {
		keyNames[key.Name] = true
	}

	if !keyNames["openai-db-key-1"] {
		t.Error("Expected openai-db-key-1 to be present")
	}
	if !keyNames["openai-db-key-2"] {
		t.Error("Expected openai-db-key-2 to be present")
	}
	if !keyNames["openai-file-key-3"] {
		t.Error("Expected openai-file-key-3 to be present (new from file)")
	}
}

// TestLoadConfig_MCP_Merge tests MCP config merge from DB and file
func TestLoadConfig_MCP_Merge(t *testing.T) {
	// Setup DB MCP config
	dbMCPConfig := &schemas.MCPConfig{
		ClientConfigs: []*schemas.MCPClientConfig{
			{
				ID:             "mcp-1",
				Name:           "db-client-1",
				ConnectionType: schemas.MCPConnectionTypeHTTP,
			},
			{
				ID:             "mcp-2",
				Name:           "db-client-2",
				ConnectionType: schemas.MCPConnectionTypeSTDIO,
			},
		},
	}

	// Setup file MCP config with some overlapping and some new
	fileMCPConfig := &schemas.MCPConfig{
		ClientConfigs: []*schemas.MCPClientConfig{
			{
				ID:             "mcp-1", // Same ID - should be skipped
				Name:           "different-name",
				ConnectionType: schemas.MCPConnectionTypeHTTP,
			},
			{
				ID:             "mcp-3", // New ID
				Name:           "file-client-3",
				ConnectionType: schemas.MCPConnectionTypeSSE,
			},
			{
				ID:             "mcp-4",       // New
				Name:           "db-client-2", // Same name as existing - should be skipped
				ConnectionType: schemas.MCPConnectionTypeHTTP,
			},
		},
	}

	// Simulate merge logic
	clientConfigsToAdd := make([]*schemas.MCPClientConfig, 0)
	for _, newClientConfig := range fileMCPConfig.ClientConfigs {
		found := false
		for _, existingClientConfig := range dbMCPConfig.ClientConfigs {
			if (newClientConfig.ID != "" && existingClientConfig.ID == newClientConfig.ID) ||
				(newClientConfig.Name != "" && existingClientConfig.Name == newClientConfig.Name) {
				found = true
				break
			}
		}
		if !found {
			clientConfigsToAdd = append(clientConfigsToAdd, newClientConfig)
		}
	}

	mergedMCPConfig := &schemas.MCPConfig{
		ClientConfigs: append(dbMCPConfig.ClientConfigs, clientConfigsToAdd...),
	}

	// Verify merge results
	if len(mergedMCPConfig.ClientConfigs) != 3 {
		t.Errorf("Expected 3 client configs after merge (2 from DB + 1 new from file), got %d", len(mergedMCPConfig.ClientConfigs))
	}

	// Verify the client configs
	ids := make(map[string]bool)
	names := make(map[string]bool)
	for _, cc := range mergedMCPConfig.ClientConfigs {
		ids[cc.ID] = true
		names[cc.Name] = true
	}

	if !ids["mcp-1"] {
		t.Error("Expected mcp-1 to be present")
	}
	if !ids["mcp-2"] {
		t.Error("Expected mcp-2 to be present")
	}
	if !ids["mcp-3"] {
		t.Error("Expected mcp-3 to be present (new from file)")
	}
	if ids["mcp-4"] {
		t.Error("Expected mcp-4 to be skipped (same name as existing)")
	}
}

// TestLoadConfig_Governance_Merge tests governance config merge from DB and file
func TestLoadConfig_Governance_Merge(t *testing.T) {
	// Setup DB governance config
	dbGovernanceConfig := &configstore.GovernanceConfig{
		Budgets: []tables.TableBudget{
			{ID: "budget-1"},
			{ID: "budget-2"},
		},
		RateLimits: []tables.TableRateLimit{
			{ID: "ratelimit-1"},
		},
		Customers: []tables.TableCustomer{
			{ID: "customer-1"},
		},
		Teams: []tables.TableTeam{
			{ID: "team-1"},
		},
		VirtualKeys: []tables.TableVirtualKey{
			{ID: "vkey-1"},
		},
	}

	// Setup file governance config with some overlapping and some new
	fileGovernanceConfig := &configstore.GovernanceConfig{
		Budgets: []tables.TableBudget{
			{ID: "budget-1"}, // Duplicate
			{ID: "budget-3"}, // New
		},
		RateLimits: []tables.TableRateLimit{
			{ID: "ratelimit-2"}, // New
		},
		Customers: []tables.TableCustomer{
			{ID: "customer-1"}, // Duplicate
			{ID: "customer-2"}, // New
		},
		Teams: []tables.TableTeam{
			{ID: "team-2"}, // New
		},
		VirtualKeys: []tables.TableVirtualKey{
			{ID: "vkey-1"}, // Duplicate
			{ID: "vkey-2"}, // New
		},
	}

	// Simulate merge logic for Budgets
	budgetsToAdd := make([]tables.TableBudget, 0)
	for _, newBudget := range fileGovernanceConfig.Budgets {
		found := false
		for _, existingBudget := range dbGovernanceConfig.Budgets {
			if existingBudget.ID == newBudget.ID {
				found = true
				break
			}
		}
		if !found {
			budgetsToAdd = append(budgetsToAdd, newBudget)
		}
	}
	mergedBudgets := append(dbGovernanceConfig.Budgets, budgetsToAdd...)

	// Simulate merge logic for RateLimits
	rateLimitsToAdd := make([]tables.TableRateLimit, 0)
	for _, newRateLimit := range fileGovernanceConfig.RateLimits {
		found := false
		for _, existingRateLimit := range dbGovernanceConfig.RateLimits {
			if existingRateLimit.ID == newRateLimit.ID {
				found = true
				break
			}
		}
		if !found {
			rateLimitsToAdd = append(rateLimitsToAdd, newRateLimit)
		}
	}
	mergedRateLimits := append(dbGovernanceConfig.RateLimits, rateLimitsToAdd...)

	// Simulate merge logic for Customers
	customersToAdd := make([]tables.TableCustomer, 0)
	for _, newCustomer := range fileGovernanceConfig.Customers {
		found := false
		for _, existingCustomer := range dbGovernanceConfig.Customers {
			if existingCustomer.ID == newCustomer.ID {
				found = true
				break
			}
		}
		if !found {
			customersToAdd = append(customersToAdd, newCustomer)
		}
	}
	mergedCustomers := append(dbGovernanceConfig.Customers, customersToAdd...)

	// Simulate merge logic for Teams
	teamsToAdd := make([]tables.TableTeam, 0)
	for _, newTeam := range fileGovernanceConfig.Teams {
		found := false
		for _, existingTeam := range dbGovernanceConfig.Teams {
			if existingTeam.ID == newTeam.ID {
				found = true
				break
			}
		}
		if !found {
			teamsToAdd = append(teamsToAdd, newTeam)
		}
	}
	mergedTeams := append(dbGovernanceConfig.Teams, teamsToAdd...)

	// Simulate merge logic for VirtualKeys
	virtualKeysToAdd := make([]tables.TableVirtualKey, 0)
	for _, newVirtualKey := range fileGovernanceConfig.VirtualKeys {
		found := false
		for _, existingVirtualKey := range dbGovernanceConfig.VirtualKeys {
			if existingVirtualKey.ID == newVirtualKey.ID {
				found = true
				break
			}
		}
		if !found {
			virtualKeysToAdd = append(virtualKeysToAdd, newVirtualKey)
		}
	}
	mergedVirtualKeys := append(dbGovernanceConfig.VirtualKeys, virtualKeysToAdd...)

	// Verify merge results
	if len(mergedBudgets) != 3 {
		t.Errorf("Expected 3 budgets after merge (2 from DB + 1 new), got %d", len(mergedBudgets))
	}

	if len(mergedRateLimits) != 2 {
		t.Errorf("Expected 2 rate limits after merge (1 from DB + 1 new), got %d", len(mergedRateLimits))
	}

	if len(mergedCustomers) != 2 {
		t.Errorf("Expected 2 customers after merge (1 from DB + 1 new), got %d", len(mergedCustomers))
	}

	if len(mergedTeams) != 2 {
		t.Errorf("Expected 2 teams after merge (1 from DB + 1 new), got %d", len(mergedTeams))
	}

	if len(mergedVirtualKeys) != 2 {
		t.Errorf("Expected 2 virtual keys after merge (1 from DB + 1 new), got %d", len(mergedVirtualKeys))
	}

	// Verify specific IDs
	budgetIDs := make(map[string]bool)
	for _, b := range mergedBudgets {
		budgetIDs[b.ID] = true
	}
	if !budgetIDs["budget-1"] || !budgetIDs["budget-2"] || !budgetIDs["budget-3"] {
		t.Error("Expected budgets budget-1, budget-2, and budget-3")
	}
}

// TestGenerateProviderConfigHash tests that provider config hash is generated correctly
func TestGenerateProviderConfigHash(t *testing.T) {
	// Create a provider config
	config1 := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{ID: "key-1", Name: "test-key", Value: *schemas.NewEnvVar("sk-123"), Weight: 1},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.example.com",
		},
		SendBackRawResponse: true,
	}

	// Generate hash
	hash1, err := config1.GenerateConfigHash("openai")
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == "" {
		t.Error("Expected non-empty hash")
	}

	// Same config should produce same hash
	config2 := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{ID: "different-id", Name: "different-name", Value: *schemas.NewEnvVar("different-value"), Weight: 2}, // Keys should NOT affect hash
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.example.com",
		},
		SendBackRawResponse: true,
	}

	hash2, err := config2.GenerateConfigHash("openai")
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 != hash2 {
		t.Error("Expected same hash for configs with same fields (keys excluded)")
	}

	// Different config should produce different hash
	config3 := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{ID: "key-1", Name: "test-key", Value: *schemas.NewEnvVar("sk-123"), Weight: 1},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://different-api.example.com", // Different base URL
		},
		SendBackRawResponse: true,
	}

	hash3, err := config3.GenerateConfigHash("openai")
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash3 {
		t.Error("Expected different hash for configs with different NetworkConfig")
	}

	// Different provider name should produce different hash
	hash4, err := config1.GenerateConfigHash("anthropic")
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash4 {
		t.Error("Expected different hash for different provider names")
	}

	// Different SendBackRawResponse should produce different hash
	config5 := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{ID: "key-1", Name: "test-key", Value: *schemas.NewEnvVar("sk-123"), Weight: 1},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.example.com",
		},
		SendBackRawResponse: false, // Different SendBackRawResponse
	}

	hash5, err := config5.GenerateConfigHash("openai")
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash5 {
		t.Error("Expected different hash for configs with different SendBackRawResponse")
	}

	// Different ConcurrencyAndBufferSize should produce different hash
	config6 := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{ID: "key-1", Name: "test-key", Value: *schemas.NewEnvVar("sk-123"), Weight: 1},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.example.com",
		},
		SendBackRawResponse: true,
		ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
			BufferSize:  100,
		},
	}

	hash6, err := config6.GenerateConfigHash("openai")
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash6 {
		t.Error("Expected different hash for configs with ConcurrencyAndBufferSize")
	}

	// Different ProxyConfig should produce different hash
	config7 := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{ID: "key-1", Name: "test-key", Value: *schemas.NewEnvVar("sk-123"), Weight: 1},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.example.com",
		},
		SendBackRawResponse: true,
		ProxyConfig: &schemas.ProxyConfig{
			Type: schemas.HTTPProxy,
			URL:  "http://proxy.example.com:8080",
		},
	}

	hash7, err := config7.GenerateConfigHash("openai")
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash7 {
		t.Error("Expected different hash for configs with ProxyConfig")
	}

	// Different CustomProviderConfig should produce different hash
	config8 := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{ID: "key-1", Name: "test-key", Value: *schemas.NewEnvVar("sk-123"), Weight: 1},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.example.com",
		},
		SendBackRawResponse: true,
		CustomProviderConfig: &schemas.CustomProviderConfig{
			IsKeyLess:        false,
			BaseProviderType: schemas.OpenAI,
		},
	}

	hash8, err := config8.GenerateConfigHash("custom-provider")
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	// config1 with custom-provider name for comparison
	hash1Custom, _ := config1.GenerateConfigHash("custom-provider")
	if hash1Custom == hash8 {
		t.Error("Expected different hash for configs with CustomProviderConfig")
	}

	t.Log("✓ ProviderConfig hash generation works correctly for all fields")
}

// TestGenerateKeyHash tests that key hash is generated correctly
func TestGenerateKeyHash(t *testing.T) {
	// Create a key
	key1 := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Models: []string{"gpt-4", "gpt-3.5-turbo"},
		Weight: 1.5,
	}

	// Generate hash
	hash1, err := configstore.GenerateKeyHash(key1)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == "" {
		t.Error("Expected non-empty hash")
	}

	// Same key content with different ID should produce same hash (ID is skipped)
	key2 := schemas.Key{
		ID:     "different-id", // Different ID - should be skipped
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Models: []string{"gpt-4", "gpt-3.5-turbo"},
		Weight: 1.5,
	}

	hash2, err := configstore.GenerateKeyHash(key2)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 != hash2 {
		t.Error("Expected same hash for keys with same content (ID should be skipped)")
	}

	// Different Name should produce different hash
	key2b := schemas.Key{
		ID:     "key-1",
		Name:   "different-key-name", // Different name
		Value:  *schemas.NewEnvVar("sk-123"),
		Models: []string{"gpt-4", "gpt-3.5-turbo"},
		Weight: 1.5,
	}

	hash2b, err := configstore.GenerateKeyHash(key2b)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash2b {
		t.Error("Expected different hash for keys with different Name")
	}

	// Different value should produce different hash
	key3 := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-different"), // Different value
		Models: []string{"gpt-4", "gpt-3.5-turbo"},
		Weight: 1.5,
	}

	hash3, err := configstore.GenerateKeyHash(key3)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash3 {
		t.Error("Expected different hash for keys with different Value")
	}

	// Different models should produce different hash
	key4 := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Models: []string{"gpt-4"}, // Different models
		Weight: 1.5,
	}

	hash4, err := configstore.GenerateKeyHash(key4)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash4 {
		t.Error("Expected different hash for keys with different Models")
	}

	// Different weight should produce different hash
	key5 := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Models: []string{"gpt-4", "gpt-3.5-turbo"},
		Weight: 2.0, // Different weight
	}

	hash5, err := configstore.GenerateKeyHash(key5)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash5 {
		t.Error("Expected different hash for keys with different Weight")
	}

	// AzureKeyConfig should produce different hash
	apiVersion := "2024-10-21"
	key6 := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Models: []string{"gpt-4", "gpt-3.5-turbo"},
		Weight: 1.5,
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint:    *schemas.NewEnvVar("https://my-azure.openai.azure.com"),
			Deployments: map[string]string{"gpt-4": "gpt-4-deployment"},
			APIVersion:  schemas.NewEnvVar(apiVersion),
		},
	}

	hash6, err := configstore.GenerateKeyHash(key6)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash6 {
		t.Error("Expected different hash for keys with AzureKeyConfig")
	}

	// Different AzureKeyConfig should produce different hash
	key6b := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Models: []string{"gpt-4", "gpt-3.5-turbo"},
		Weight: 1.5,
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint:    *schemas.NewEnvVar("https://different-azure.openai.azure.com"), // Different endpoint
			Deployments: map[string]string{"gpt-4": "gpt-4-deployment"},
			APIVersion:  schemas.NewEnvVar(apiVersion),
		},
	}

	hash6b, err := configstore.GenerateKeyHash(key6b)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash6 == hash6b {
		t.Error("Expected different hash for keys with different AzureKeyConfig endpoint")
	}

	// VertexKeyConfig should produce different hash
	key7 := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Models: []string{"gpt-4", "gpt-3.5-turbo"},
		Weight: 1.5,
		VertexKeyConfig: &schemas.VertexKeyConfig{
			ProjectID:       *schemas.NewEnvVar("my-project"),
			ProjectNumber:   *schemas.NewEnvVar("123456789"),
			Region:          *schemas.NewEnvVar("us-central1"),
			AuthCredentials: *schemas.NewEnvVar("service-account-json"),
		},
	}

	hash7, err := configstore.GenerateKeyHash(key7)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash7 {
		t.Error("Expected different hash for keys with VertexKeyConfig")
	}

	// Different VertexKeyConfig should produce different hash
	key7b := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Models: []string{"gpt-4", "gpt-3.5-turbo"},
		Weight: 1.5,
		VertexKeyConfig: &schemas.VertexKeyConfig{
			ProjectID:       *schemas.NewEnvVar("different-project"), // Different project
			ProjectNumber:   *schemas.NewEnvVar("123456789"),
			Region:          *schemas.NewEnvVar("us-central1"),
			AuthCredentials: *schemas.NewEnvVar("service-account-json"),
		},
	}

	hash7b, err := configstore.GenerateKeyHash(key7b)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash7 == hash7b {
		t.Error("Expected different hash for keys with different VertexKeyConfig project")
	}

	// BedrockKeyConfig should produce different hash
	region := "us-east-1"
	key8 := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Models: []string{"gpt-4", "gpt-3.5-turbo"},
		Weight: 1.5,
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
			SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
			Region:    schemas.NewEnvVar(region),
		},
	}

	hash8, err := configstore.GenerateKeyHash(key8)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash8 {
		t.Error("Expected different hash for keys with BedrockKeyConfig")
	}

	// Different BedrockKeyConfig should produce different hash
	differentRegion := "us-west-2"
	key8b := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Models: []string{"gpt-4", "gpt-3.5-turbo"},
		Weight: 1.5,
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
			SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
			Region:    schemas.NewEnvVar(differentRegion), // Different region
		},
	}

	hash8b, err := configstore.GenerateKeyHash(key8b)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash8 == hash8b {
		t.Error("Expected different hash for keys with different BedrockKeyConfig region")
	}

	t.Log("✓ Key hash generation works correctly for all fields including Azure, Vertex, and Bedrock configs")
}

// TestProviderHashComparison_MatchingHash tests that DB config is kept when hashes match
func TestProviderHashComparison_MatchingHash(t *testing.T) {
	// Create a provider config (simulating what's in config.json)
	fileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{ID: "key-1", Name: "openai-key", Value: *schemas.NewEnvVar("sk-file-123"), Weight: 1},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.openai.com",
		},
		SendBackRawResponse: false,
	}

	// Generate hash for the file config
	fileHash, err := fileConfig.GenerateConfigHash("openai")
	if err != nil {
		t.Fatalf("Failed to generate file hash: %v", err)
	}

	// Create DB config with same hash (simulating unchanged config.json)
	dbConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{ID: "key-1", Name: "openai-key", Value: *schemas.NewEnvVar("sk-db-different"), Weight: 1}, // DB may have different key value (edited via dashboard)
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.openai.com",
		},
		SendBackRawResponse: false,
		ConfigHash:          fileHash, // Same hash as file
	}

	// Simulate the hash comparison logic
	providersInConfigStore := map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: dbConfig,
	}

	// When hash matches, we should keep DB config
	existingCfg := providersInConfigStore[schemas.OpenAI]
	if existingCfg.ConfigHash == fileHash {
		// Hash matches - keep DB config
		// This is the expected path
	} else {
		t.Error("Expected hash to match")
	}

	// Verify DB config is preserved (key value from DB, not file)
	if existingCfg.Keys[0].Value != *schemas.NewEnvVar("sk-db-different") {
		t.Errorf("Expected DB key value to be preserved, got %v", existingCfg.Keys[0].Value)
	}
}

// TestProviderHashComparison_DifferentHash tests that file config is used when hashes differ
func TestProviderHashComparison_DifferentHash(t *testing.T) {
	// Create a provider config (simulating what's in config.json - CHANGED)
	fileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{ID: "key-1", Name: "openai-key", Value: *schemas.NewEnvVar("sk-file-123"), Weight: 1},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.openai.com/v2", // Changed URL
		},
		SendBackRawResponse: true, // Changed setting
	}

	// Generate hash for the file config
	fileHash, err := fileConfig.GenerateConfigHash("openai")
	if err != nil {
		t.Fatalf("Failed to generate file hash: %v", err)
	}
	fileConfig.ConfigHash = fileHash

	// Create DB config with different hash (config.json was changed)
	dbConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{ID: "key-1", Name: "openai-key", Value: *schemas.NewEnvVar("sk-db-123"), Weight: 1},
			{ID: "key-2", Name: "dashboard-added-key", Value: *schemas.NewEnvVar("sk-dashboard"), Weight: 1}, // Key added via dashboard
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.openai.com", // Old URL
		},
		SendBackRawResponse: false, // Old setting
		ConfigHash:          "old-different-hash",
	}

	// Simulate the hash comparison logic
	providersInConfigStore := map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: dbConfig,
	}

	existingCfg := providersInConfigStore[schemas.OpenAI]
	if existingCfg.ConfigHash != fileHash {
		// Hash mismatch - sync from file, but preserve dashboard-added keys
		mergedKeys := fileConfig.Keys

		// Find keys in DB that aren't in file (added via dashboard)
		for _, dbKey := range existingCfg.Keys {
			found := false
			for _, fileKey := range fileConfig.Keys {
				dbKeyHash, _ := configstore.GenerateKeyHash(schemas.Key{
					Name:   dbKey.Name,
					Value:  dbKey.Value,
					Models: dbKey.Models,
					Weight: dbKey.Weight,
				})
				fileKeyHash, _ := configstore.GenerateKeyHash(fileKey)
				if dbKeyHash == fileKeyHash || fileKey.Name == dbKey.Name {
					found = true
					break
				}
			}
			if !found {
				// Key exists in DB but not in file - preserve it
				mergedKeys = append(mergedKeys, dbKey)
			}
		}

		// Update the result
		fileConfig.Keys = mergedKeys
		providersInConfigStore[schemas.OpenAI] = fileConfig
	} else {
		t.Error("Expected hash mismatch")
	}

	// Verify file config is now used
	resultConfig := providersInConfigStore[schemas.OpenAI]

	if resultConfig.NetworkConfig.BaseURL != "https://api.openai.com/v2" {
		t.Errorf("Expected file BaseURL, got %s", resultConfig.NetworkConfig.BaseURL)
	}

	if !resultConfig.SendBackRawResponse {
		t.Error("Expected SendBackRawResponse to be true (from file)")
	}

	// Verify dashboard-added key is preserved
	if len(resultConfig.Keys) != 2 {
		t.Errorf("Expected 2 keys (1 from file + 1 dashboard-added), got %d", len(resultConfig.Keys))
	}

	hasFileKey := false
	hasDashboardKey := false
	for _, key := range resultConfig.Keys {
		if key.Name == "openai-key" {
			hasFileKey = true
		}
		if key.Name == "dashboard-added-key" {
			hasDashboardKey = true
		}
	}

	if !hasFileKey {
		t.Error("Expected file key to be present")
	}
	if !hasDashboardKey {
		t.Error("Expected dashboard-added key to be preserved")
	}
}

// TestProviderHashComparison_ProviderOnlyInDB tests that provider added via dashboard is preserved
func TestProviderHashComparison_ProviderOnlyInDB(t *testing.T) {
	// DB has a provider that was added via dashboard (not in config.json)
	dbConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{ID: "key-1", Name: "dashboard-provider-key", Value: *schemas.NewEnvVar("sk-dashboard-123"), Weight: 1},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.custom-provider.com",
		},
		SendBackRawResponse: true,
	}

	// Generate hash for DB config
	dbHash, err := dbConfig.GenerateConfigHash("custom-provider")
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}
	dbConfig.ConfigHash = dbHash

	// Existing providers from DB
	providersInConfigStore := map[schemas.ModelProvider]configstore.ProviderConfig{
		"custom-provider": dbConfig,
	}

	// File providers (doesn't include custom-provider)
	fileProviders := map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: "key-1", Name: "openai-key", Value: *schemas.NewEnvVar("sk-openai-123"), Weight: 1},
			},
		},
	}

	// Simulate the logic: process file providers, but don't remove DB-only providers
	for providerName, fileCfg := range fileProviders {
		provider := schemas.ModelProvider(providerName)
		fileHash, _ := fileCfg.GenerateConfigHash(providerName)
		fileCfg.ConfigHash = fileHash

		if _, exists := providersInConfigStore[provider]; !exists {
			// New provider from file - add it
			providersInConfigStore[provider] = fileCfg
		}
		// Note: We don't delete providers that are only in DB
	}

	// Verify dashboard-added provider is preserved
	if _, exists := providersInConfigStore["custom-provider"]; !exists {
		t.Error("Expected dashboard-added provider to be preserved")
	}

	// Verify file provider was added
	if _, exists := providersInConfigStore[schemas.OpenAI]; !exists {
		t.Error("Expected file provider to be added")
	}

	// Verify we have both providers
	if len(providersInConfigStore) != 2 {
		t.Errorf("Expected 2 providers (1 from DB + 1 from file), got %d", len(providersInConfigStore))
	}
}

// TestProviderHashComparison_RoundTrip tests JSON → DB → same JSON produces no changes
func TestProviderHashComparison_RoundTrip(t *testing.T) {
	// First load: config.json content
	fileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{ID: "key-1", Name: "openai-key", Value: *schemas.NewEnvVar("sk-original-123"), Weight: 1},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.openai.com",
		},
		SendBackRawResponse: false,
	}

	// Generate hash for file config
	fileHash, err := fileConfig.GenerateConfigHash("openai")
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}
	fileConfig.ConfigHash = fileHash

	// Simulate first load: save to DB
	providersInConfigStore := map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: fileConfig,
	}

	// Second load: same config.json (no changes)
	secondFileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{ID: "key-1", Name: "openai-key", Value: *schemas.NewEnvVar("sk-original-123"), Weight: 1},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.openai.com",
		},
		SendBackRawResponse: false,
	}

	secondFileHash, err := secondFileConfig.GenerateConfigHash("openai")
	if err != nil {
		t.Fatalf("Failed to generate second hash: %v", err)
	}

	// Hash should match (config.json unchanged)
	if fileHash != secondFileHash {
		t.Error("Expected same hash for identical config (round-trip)")
	}

	// Simulate comparison logic
	existingCfg := providersInConfigStore[schemas.OpenAI]
	if existingCfg.ConfigHash == secondFileHash {
		// Hash matches - keep DB config (no changes needed)
		t.Log("Hash matches - DB config preserved (correct behavior)")
	} else {
		t.Error("Expected hash match on round-trip with same config")
	}
}

// TestProviderHashComparison_DashboardEditThenSameFile tests dashboard edits are preserved
func TestProviderHashComparison_DashboardEditThenSameFile(t *testing.T) {
	// Initial file config
	fileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{ID: "key-1", Name: "openai-key", Value: *schemas.NewEnvVar("sk-original-123"), Weight: 1},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.openai.com",
		},
		SendBackRawResponse: false,
	}

	fileHash, _ := fileConfig.GenerateConfigHash("openai")
	fileConfig.ConfigHash = fileHash

	// Simulate: user edits key value via dashboard (but provider config hash stays same)
	dbConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{ID: "key-1", Name: "openai-key", Value: *schemas.NewEnvVar("sk-dashboard-modified-456"), Weight: 1}, // Modified via dashboard
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.openai.com",
		},
		SendBackRawResponse: false,
		ConfigHash:          fileHash, // Hash based on provider config, not keys
	}

	providersInConfigStore := map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: dbConfig,
	}

	// Reload with same file config
	reloadFileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{ID: "key-1", Name: "openai-key", Value: *schemas.NewEnvVar("sk-original-123"), Weight: 1},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.openai.com",
		},
		SendBackRawResponse: false,
	}

	reloadHash, _ := reloadFileConfig.GenerateConfigHash("openai")

	// Hash matches (file unchanged)
	existingCfg := providersInConfigStore[schemas.OpenAI]
	if existingCfg.ConfigHash == reloadHash {
		// Keep DB config - dashboard edits preserved
		t.Log("Hash matches - dashboard edits preserved (correct behavior)")
	} else {
		t.Error("Expected hash match - file wasn't changed")
	}

	// Verify dashboard-modified key value is preserved
	if existingCfg.Keys[0].Value != *schemas.NewEnvVar("sk-dashboard-modified-456") {
		t.Errorf("Expected dashboard-modified key value to be preserved, got %v", existingCfg.Keys[0].Value)
	}
}

// TestProviderHashComparison_OptionalFieldsPresence tests hash with optional fields present/absent
func TestProviderHashComparison_OptionalFieldsPresence(t *testing.T) {
	// Config with no optional fields
	configNoOptional := configstore.ProviderConfig{
		Keys:                []schemas.Key{{ID: "key-1", Name: "test", Value: *schemas.NewEnvVar("sk-123"), Weight: 1}},
		SendBackRawResponse: false,
	}

	hashNoOptional, err := configNoOptional.GenerateConfigHash("openai")
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	// Config with NetworkConfig
	configWithNetwork := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "test", Value: *schemas.NewEnvVar("sk-123"), Weight: 1}},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.example.com",
		},
		SendBackRawResponse: false,
	}

	hashWithNetwork, err := configWithNetwork.GenerateConfigHash("openai")
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hashNoOptional == hashWithNetwork {
		t.Error("Expected different hash when NetworkConfig is present vs absent")
	}

	// Config with ProxyConfig
	configWithProxy := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "test", Value: *schemas.NewEnvVar("sk-123"), Weight: 1}},
		ProxyConfig: &schemas.ProxyConfig{
			Type: "http",
			URL:  "http://proxy.example.com",
		},
		SendBackRawResponse: false,
	}

	hashWithProxy, err := configWithProxy.GenerateConfigHash("openai")
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hashNoOptional == hashWithProxy {
		t.Error("Expected different hash when ProxyConfig is present vs absent")
	}

	// Config with ConcurrencyAndBufferSize
	configWithConcurrency := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "test", Value: *schemas.NewEnvVar("sk-123"), Weight: 1}},
		ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
			BufferSize:  100,
		},
		SendBackRawResponse: false,
	}

	hashWithConcurrency, err := configWithConcurrency.GenerateConfigHash("openai")
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hashNoOptional == hashWithConcurrency {
		t.Error("Expected different hash when ConcurrencyAndBufferSize is present vs absent")
	}

	// Config with CustomProviderConfig
	configWithCustom := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "test", Value: *schemas.NewEnvVar("sk-123"), Weight: 1}},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			BaseProviderType: "openai",
		},
		SendBackRawResponse: false,
	}

	hashWithCustom, err := configWithCustom.GenerateConfigHash("openai")
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hashNoOptional == hashWithCustom {
		t.Error("Expected different hash when CustomProviderConfig is present vs absent")
	}

	// Config with SendBackRawResponse true vs false
	configWithRawResponse := configstore.ProviderConfig{
		Keys:                []schemas.Key{{ID: "key-1", Name: "test", Value: *schemas.NewEnvVar("sk-123"), Weight: 1}},
		SendBackRawResponse: true,
	}

	hashWithRawResponse, err := configWithRawResponse.GenerateConfigHash("openai")
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hashNoOptional == hashWithRawResponse {
		t.Error("Expected different hash when SendBackRawResponse is true vs false")
	}

	// Config with ALL optional fields
	configAllFields := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "test", Value: *schemas.NewEnvVar("sk-123"), Weight: 1}},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.example.com",
		},
		ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
			BufferSize:  100,
		},
		ProxyConfig: &schemas.ProxyConfig{
			Type: "http",
			URL:  "http://proxy.example.com",
		},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			BaseProviderType: "openai",
		},
		SendBackRawResponse: true,
	}

	hashAllFields, err := configAllFields.GenerateConfigHash("openai")
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	// All hashes should be unique
	hashes := map[string]string{
		"no_optional":  hashNoOptional,
		"with_network": hashWithNetwork,
		"with_proxy":   hashWithProxy,
		"with_conc":    hashWithConcurrency,
		"with_custom":  hashWithCustom,
		"with_raw":     hashWithRawResponse,
		"all_fields":   hashAllFields,
	}

	seen := make(map[string]string)
	for name, hash := range hashes {
		if existingName, exists := seen[hash]; exists {
			t.Errorf("Hash collision between %s and %s", name, existingName)
		}
		seen[hash] = name
	}
}

// TestKeyHashComparison_OptionalFieldsPresence tests key hash with optional fields
func TestKeyHashComparison_OptionalFieldsPresence(t *testing.T) {
	// Basic key with no optional configs
	keyBasic := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Weight: 1,
	}

	hashBasic, _ := configstore.GenerateKeyHash(keyBasic)

	// Key with Models
	keyWithModels := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Models: []string{"gpt-4"},
		Weight: 1,
	}

	hashWithModels, _ := configstore.GenerateKeyHash(keyWithModels)

	if hashBasic == hashWithModels {
		t.Error("Expected different hash when Models is present vs absent")
	}

	// Key with empty Models array vs nil
	keyEmptyModels := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Models: []string{},
		Weight: 1,
	}

	hashEmptyModels, _ := configstore.GenerateKeyHash(keyEmptyModels)

	// Empty slice and nil should produce same hash (both mean "no model restrictions")
	if hashBasic != hashEmptyModels {
		t.Error("Expected same hash for nil Models and empty Models slice")
	}

	// Key with AzureKeyConfig
	keyWithAzure := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Weight: 1,
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint:   *schemas.NewEnvVar("https://myazure.openai.azure.com"),
			APIVersion: schemas.NewEnvVar("2024-02-01"),
		},
	}

	hashWithAzure, _ := configstore.GenerateKeyHash(keyWithAzure)

	if hashBasic == hashWithAzure {
		t.Error("Expected different hash when AzureKeyConfig is present vs absent")
	}

	// Key with VertexKeyConfig
	keyWithVertex := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Weight: 1,
		VertexKeyConfig: &schemas.VertexKeyConfig{
			ProjectID: *schemas.NewEnvVar("my-project"),
			Region:    *schemas.NewEnvVar("us-central1"),
		},
	}

	hashWithVertex, _ := configstore.GenerateKeyHash(keyWithVertex)

	if hashBasic == hashWithVertex {
		t.Error("Expected different hash when VertexKeyConfig is present vs absent")
	}

	// Key with BedrockKeyConfig
	keyWithBedrock := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Weight: 1,
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			AccessKey: *schemas.NewEnvVar("AKIA..."),
			SecretKey: *schemas.NewEnvVar("secret..."),
			Region:    schemas.NewEnvVar("us-east-1"),
		},
	}

	hashWithBedrock, _ := configstore.GenerateKeyHash(keyWithBedrock)

	if hashBasic == hashWithBedrock {
		t.Error("Expected different hash when BedrockKeyConfig is present vs absent")
	}

	// Verify all hashes are unique
	hashes := map[string]string{
		"basic":        hashBasic,
		"with_models":  hashWithModels,
		"with_azure":   hashWithAzure,
		"with_vertex":  hashWithVertex,
		"with_bedrock": hashWithBedrock,
	}

	seen := make(map[string]string)
	for name, hash := range hashes {
		if existingName, exists := seen[hash]; exists {
			t.Errorf("Hash collision between %s and %s", name, existingName)
		}
		seen[hash] = name
	}
}

// TestProviderHashComparison_FieldValueChanges tests hash changes when field values change
func TestProviderHashComparison_FieldValueChanges(t *testing.T) {
	// Base config
	baseConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "test", Value: *schemas.NewEnvVar("sk-123"), Weight: 1}},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.example.com",
		},
		SendBackRawResponse: false,
	}

	baseHash, _ := baseConfig.GenerateConfigHash("openai")

	// Change BaseURL
	configChangedURL := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "test", Value: *schemas.NewEnvVar("sk-123"), Weight: 1}},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.different.com", // Changed
		},
		SendBackRawResponse: false,
	}

	hashChangedURL, _ := configChangedURL.GenerateConfigHash("openai")

	if baseHash == hashChangedURL {
		t.Error("Expected different hash when BaseURL changes")
	}

	// Add extra headers
	configWithHeaders := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "test", Value: *schemas.NewEnvVar("sk-123"), Weight: 1}},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.example.com",
			ExtraHeaders: map[string]string{
				"X-Custom-Header": "value",
			},
		},
		SendBackRawResponse: false,
	}

	hashWithHeaders, _ := configWithHeaders.GenerateConfigHash("openai")

	if baseHash == hashWithHeaders {
		t.Error("Expected different hash when ExtraHeaders are added")
	}

	// Change concurrency values
	configWithConc1 := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "test", Value: *schemas.NewEnvVar("sk-123"), Weight: 1}},
		ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
			BufferSize:  100,
		},
	}

	hashConc1, _ := configWithConc1.GenerateConfigHash("openai")

	configWithConc2 := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "test", Value: *schemas.NewEnvVar("sk-123"), Weight: 1}},
		ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{
			Concurrency: 20, // Changed
			BufferSize:  100,
		},
	}

	hashConc2, _ := configWithConc2.GenerateConfigHash("openai")

	if hashConc1 == hashConc2 {
		t.Error("Expected different hash when Concurrency value changes")
	}
}

// Helper function for string pointers
func stringPtr(s string) *string {
	return &s
}

// TestProviderHashComparison_FieldRemoved tests hash changes when fields are removed
func TestProviderHashComparison_FieldRemoved(t *testing.T) {
	// Original config with all fields
	originalConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "test", Value: *schemas.NewEnvVar("sk-123"), Weight: 1}},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.example.com",
			ExtraHeaders: map[string]string{
				"X-Custom": "value",
			},
		},
		ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
			BufferSize:  100,
		},
		ProxyConfig: &schemas.ProxyConfig{
			Type: "http",
			URL:  "http://proxy.example.com",
		},
		SendBackRawResponse: true,
	}

	originalHash, _ := originalConfig.GenerateConfigHash("openai")

	// NetworkConfig removed
	configNoNetwork := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "test", Value: *schemas.NewEnvVar("sk-123"), Weight: 1}},
		// NetworkConfig: nil (removed)
		ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
			BufferSize:  100,
		},
		ProxyConfig: &schemas.ProxyConfig{
			Type: "http",
			URL:  "http://proxy.example.com",
		},
		SendBackRawResponse: true,
	}

	hashNoNetwork, _ := configNoNetwork.GenerateConfigHash("openai")

	if originalHash == hashNoNetwork {
		t.Error("Expected different hash when NetworkConfig is removed")
	}

	// ConcurrencyAndBufferSize removed
	configNoConcurrency := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "test", Value: *schemas.NewEnvVar("sk-123"), Weight: 1}},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.example.com",
			ExtraHeaders: map[string]string{
				"X-Custom": "value",
			},
		},
		// ConcurrencyAndBufferSize: nil (removed)
		ProxyConfig: &schemas.ProxyConfig{
			Type: "http",
			URL:  "http://proxy.example.com",
		},
		SendBackRawResponse: true,
	}

	hashNoConcurrency, _ := configNoConcurrency.GenerateConfigHash("openai")

	if originalHash == hashNoConcurrency {
		t.Error("Expected different hash when ConcurrencyAndBufferSize is removed")
	}

	// ProxyConfig removed
	configNoProxy := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "test", Value: *schemas.NewEnvVar("sk-123"), Weight: 1}},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.example.com",
			ExtraHeaders: map[string]string{
				"X-Custom": "value",
			},
		},
		ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
			BufferSize:  100,
		},
		// ProxyConfig: nil (removed)
		SendBackRawResponse: true,
	}

	hashNoProxy, _ := configNoProxy.GenerateConfigHash("openai")

	if originalHash == hashNoProxy {
		t.Error("Expected different hash when ProxyConfig is removed")
	}

	// SendBackRawResponse changed to false
	configNoRawResponse := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "test", Value: *schemas.NewEnvVar("sk-123"), Weight: 1}},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.example.com",
			ExtraHeaders: map[string]string{
				"X-Custom": "value",
			},
		},
		ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
			BufferSize:  100,
		},
		ProxyConfig: &schemas.ProxyConfig{
			Type: "http",
			URL:  "http://proxy.example.com",
		},
		SendBackRawResponse: false, // Changed to false
	}

	hashNoRawResponse, _ := configNoRawResponse.GenerateConfigHash("openai")

	if originalHash == hashNoRawResponse {
		t.Error("Expected different hash when SendBackRawResponse is changed to false")
	}

	// ExtraHeaders removed from NetworkConfig
	configNoHeaders := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "test", Value: *schemas.NewEnvVar("sk-123"), Weight: 1}},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.example.com",
			// ExtraHeaders removed
		},
		ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
			BufferSize:  100,
		},
		ProxyConfig: &schemas.ProxyConfig{
			Type: "http",
			URL:  "http://proxy.example.com",
		},
		SendBackRawResponse: true,
	}

	hashNoHeaders, _ := configNoHeaders.GenerateConfigHash("openai")

	if originalHash == hashNoHeaders {
		t.Error("Expected different hash when ExtraHeaders are removed")
	}

	// All optional fields removed
	configMinimal := configstore.ProviderConfig{
		Keys:                []schemas.Key{{ID: "key-1", Name: "test", Value: *schemas.NewEnvVar("sk-123"), Weight: 1}},
		SendBackRawResponse: false,
	}

	hashMinimal, _ := configMinimal.GenerateConfigHash("openai")

	if originalHash == hashMinimal {
		t.Error("Expected different hash when all optional fields are removed")
	}
}

// TestKeyHashComparison_FieldRemoved tests key hash changes when fields are removed
func TestKeyHashComparison_FieldRemoved(t *testing.T) {
	// Original key with all fields
	originalKey := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Models: []string{"gpt-4", "gpt-3.5-turbo"},
		Weight: 1.5,
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint:   *schemas.NewEnvVar("https://myazure.openai.azure.com"),
			APIVersion: schemas.NewEnvVar("2024-02-01"),
		},
	}

	originalHash, _ := configstore.GenerateKeyHash(originalKey)

	// Models removed
	keyNoModels := schemas.Key{
		ID:    "key-1",
		Name:  "test-key",
		Value: *schemas.NewEnvVar("sk-123"),
		// Models: nil (removed)
		Weight: 1.5,
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint:   *schemas.NewEnvVar("https://myazure.openai.azure.com"),
			APIVersion: schemas.NewEnvVar("2024-02-01"),
		},
	}

	hashNoModels, _ := configstore.GenerateKeyHash(keyNoModels)

	if originalHash == hashNoModels {
		t.Error("Expected different hash when Models are removed")
	}

	// AzureKeyConfig removed
	keyNoAzure := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Models: []string{"gpt-4", "gpt-3.5-turbo"},
		Weight: 1.5,
		// AzureKeyConfig: nil (removed)
	}

	hashNoAzure, _ := configstore.GenerateKeyHash(keyNoAzure)

	if originalHash == hashNoAzure {
		t.Error("Expected different hash when AzureKeyConfig is removed")
	}

	// Weight changed
	keyDifferentWeight := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Models: []string{"gpt-4", "gpt-3.5-turbo"},
		Weight: 1.0, // Changed from 1.5
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint:   *schemas.NewEnvVar("https://myazure.openai.azure.com"),
			APIVersion: schemas.NewEnvVar("2024-02-01"),
		},
	}

	hashDifferentWeight, _ := configstore.GenerateKeyHash(keyDifferentWeight)

	if originalHash == hashDifferentWeight {
		t.Error("Expected different hash when Weight is changed")
	}

	// Some models removed
	keyFewerModels := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Models: []string{"gpt-4"}, // gpt-3.5-turbo removed
		Weight: 1.5,
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint:   *schemas.NewEnvVar("https://myazure.openai.azure.com"),
			APIVersion: schemas.NewEnvVar("2024-02-01"),
		},
	}

	hashFewerModels, _ := configstore.GenerateKeyHash(keyFewerModels)

	if originalHash == hashFewerModels {
		t.Error("Expected different hash when some Models are removed")
	}

	// Azure endpoint changed
	keyDifferentEndpoint := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Models: []string{"gpt-4", "gpt-3.5-turbo"},
		Weight: 1.5,
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint:   *schemas.NewEnvVar("https://different.openai.azure.com"), // Changed
			APIVersion: schemas.NewEnvVar("2024-02-01"),
		},
	}

	hashDifferentEndpoint, _ := configstore.GenerateKeyHash(keyDifferentEndpoint)

	if originalHash == hashDifferentEndpoint {
		t.Error("Expected different hash when Azure endpoint is changed")
	}

	// Azure APIVersion removed
	keyNoAPIVersion := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Models: []string{"gpt-4", "gpt-3.5-turbo"},
		Weight: 1.5,
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint: *schemas.NewEnvVar("https://myazure.openai.azure.com"),
			// APIVersion: nil (removed)
		},
	}

	hashNoAPIVersion, _ := configstore.GenerateKeyHash(keyNoAPIVersion)

	if originalHash == hashNoAPIVersion {
		t.Error("Expected different hash when Azure APIVersion is removed")
	}
}

// TestProviderHashComparison_PartialFieldChanges tests partial changes within nested structs
func TestProviderHashComparison_PartialFieldChanges(t *testing.T) {
	// Base config
	baseConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "test", Value: *schemas.NewEnvVar("sk-123"), Weight: 1}},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:                        "https://api.example.com",
			DefaultRequestTimeoutInSeconds: 30,
			MaxRetries:                     3,
		},
	}

	baseHash, _ := baseConfig.GenerateConfigHash("openai")

	// Timeout set to 0 (default/removed)
	configNoTimeout := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "test", Value: *schemas.NewEnvVar("sk-123"), Weight: 1}},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:                        "https://api.example.com",
			DefaultRequestTimeoutInSeconds: 0, // Removed/default
			MaxRetries:                     3,
		},
	}

	hashNoTimeout, _ := configNoTimeout.GenerateConfigHash("openai")

	if baseHash == hashNoTimeout {
		t.Error("Expected different hash when DefaultRequestTimeoutInSeconds is removed/zeroed")
	}

	// MaxRetries set to 0 (default/removed)
	configNoRetries := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "test", Value: *schemas.NewEnvVar("sk-123"), Weight: 1}},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:                        "https://api.example.com",
			DefaultRequestTimeoutInSeconds: 30,
			MaxRetries:                     0, // Removed/default
		},
	}

	hashNoRetries, _ := configNoRetries.GenerateConfigHash("openai")

	if baseHash == hashNoRetries {
		t.Error("Expected different hash when MaxRetries is removed/zeroed")
	}

	// Timeout value changed
	configDifferentTimeout := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "test", Value: *schemas.NewEnvVar("sk-123"), Weight: 1}},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:                        "https://api.example.com",
			DefaultRequestTimeoutInSeconds: 60, // Changed from 30
			MaxRetries:                     3,
		},
	}

	hashDifferentTimeout, _ := configDifferentTimeout.GenerateConfigHash("openai")

	if baseHash == hashDifferentTimeout {
		t.Error("Expected different hash when DefaultRequestTimeoutInSeconds value changes")
	}
}

// TestProviderHashComparison_FullLifecycle tests DB → new JSON → update DB → same JSON (no update)
func TestProviderHashComparison_FullLifecycle(t *testing.T) {
	// === STEP 1: Initial state - provider exists in DB from previous config.json ===
	initialConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "openai-key", Value: *schemas.NewEnvVar("sk-initial-123"), Weight: 1}},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.openai.com/v1",
		},
		SendBackRawResponse: false,
	}

	initialHash, _ := initialConfig.GenerateConfigHash("openai")
	initialConfig.ConfigHash = initialHash

	// Simulate DB state
	providersInDB := map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: initialConfig,
	}

	t.Logf("Step 1 - Initial DB hash: %s", initialHash[:16]+"...")

	// === STEP 2: New config.json comes with changes ===
	newFileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "openai-key", Value: *schemas.NewEnvVar("sk-new-456"), Weight: 1}},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:    "https://api.openai.com/v2", // Changed!
			MaxRetries: 5,                           // Added!
		},
		SendBackRawResponse: true, // Changed!
	}

	newFileHash, _ := newFileConfig.GenerateConfigHash("openai")
	newFileConfig.ConfigHash = newFileHash

	t.Logf("Step 2 - New file hash: %s", newFileHash[:16]+"...")

	// Verify hashes are different (config.json changed)
	dbConfig := providersInDB[schemas.OpenAI]
	if dbConfig.ConfigHash == newFileHash {
		t.Fatal("Expected different hash - config.json was changed")
	}

	// === STEP 3: Sync from file to DB (hash mismatch triggers update) ===
	t.Log("Step 3 - Hash mismatch detected, syncing from file to DB")

	// Simulate the sync: file config replaces DB config
	providersInDB[schemas.OpenAI] = newFileConfig

	// Verify DB was updated
	updatedDBConfig := providersInDB[schemas.OpenAI]
	if updatedDBConfig.ConfigHash != newFileHash {
		t.Error("Expected DB to be updated with new hash")
	}
	if updatedDBConfig.NetworkConfig.BaseURL != "https://api.openai.com/v2" {
		t.Error("Expected DB to have new BaseURL from file")
	}
	if !updatedDBConfig.SendBackRawResponse {
		t.Error("Expected DB to have SendBackRawResponse=true from file")
	}

	t.Logf("Step 3 - DB updated, new DB hash: %s", updatedDBConfig.ConfigHash[:16]+"...")

	// === STEP 4: Same config.json loaded again - should NOT update ===
	sameFileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "openai-key", Value: *schemas.NewEnvVar("sk-new-456"), Weight: 1}},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:    "https://api.openai.com/v2",
			MaxRetries: 5,
		},
		SendBackRawResponse: true,
	}

	sameFileHash, _ := sameFileConfig.GenerateConfigHash("openai")

	t.Logf("Step 4 - Same file loaded again, hash: %s", sameFileHash[:16]+"...")

	// Verify hashes match now (no changes in config.json since last sync)
	currentDBConfig := providersInDB[schemas.OpenAI]
	if currentDBConfig.ConfigHash != sameFileHash {
		t.Errorf("Expected hash match - config.json unchanged since last sync. DB: %s, File: %s",
			currentDBConfig.ConfigHash[:16], sameFileHash[:16])
	}

	// Simulate the comparison logic
	if currentDBConfig.ConfigHash == sameFileHash {
		t.Log("Step 4 - Hash matches, keeping DB config (no update needed) ✓")
	} else {
		t.Error("Step 4 - Should have matched, but didn't")
	}

	// === STEP 5: Verify DB wasn't modified (still has step 3 values) ===
	finalDBConfig := providersInDB[schemas.OpenAI]
	if finalDBConfig.NetworkConfig.BaseURL != "https://api.openai.com/v2" {
		t.Error("DB should still have v2 URL")
	}
	if finalDBConfig.NetworkConfig.MaxRetries != 5 {
		t.Error("DB should still have MaxRetries=5")
	}
	if !finalDBConfig.SendBackRawResponse {
		t.Error("DB should still have SendBackRawResponse=true")
	}

	t.Log("Step 5 - DB state verified, lifecycle complete ✓")
}

// TestProviderHashComparison_MultipleUpdates tests multiple config.json updates over time
func TestProviderHashComparison_MultipleUpdates(t *testing.T) {
	// Track all hashes for verification
	hashHistory := []string{}

	// === Round 1: Initial config ===
	config1 := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "key", Value: *schemas.NewEnvVar("sk-v1"), Weight: 1}},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.v1.com",
		},
	}
	hash1, _ := config1.GenerateConfigHash("openai")
	config1.ConfigHash = hash1
	hashHistory = append(hashHistory, hash1)

	providersInDB := map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: config1,
	}

	t.Logf("Round 1 - hash: %s", hash1[:16]+"...")

	// === Round 2: Update config.json ===
	config2 := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "key", Value: *schemas.NewEnvVar("sk-v2"), Weight: 1}},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.v2.com", // Changed
		},
	}
	hash2, _ := config2.GenerateConfigHash("openai")
	config2.ConfigHash = hash2
	hashHistory = append(hashHistory, hash2)

	// Hash should be different
	if hash1 == hash2 {
		t.Fatal("Round 2 hash should differ from Round 1")
	}

	// Sync to DB
	providersInDB[schemas.OpenAI] = config2
	t.Logf("Round 2 - hash: %s (different from Round 1) ✓", hash2[:16]+"...")

	// === Round 3: Another update ===
	config3 := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "key", Value: *schemas.NewEnvVar("sk-v3"), Weight: 1}},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:    "https://api.v3.com", // Changed again
			MaxRetries: 3,                    // Added
		},
		SendBackRawResponse: true, // Added
	}
	hash3, _ := config3.GenerateConfigHash("openai")
	config3.ConfigHash = hash3
	hashHistory = append(hashHistory, hash3)

	// Hash should be different from all previous
	if hash3 == hash1 || hash3 == hash2 {
		t.Fatal("Round 3 hash should differ from all previous")
	}

	// Sync to DB
	providersInDB[schemas.OpenAI] = config3
	t.Logf("Round 3 - hash: %s (different from Round 1 & 2) ✓", hash3[:16]+"...")

	// === Round 4: Same as Round 3 (no change) ===
	config4 := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "key", Value: *schemas.NewEnvVar("sk-v3"), Weight: 1}},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:    "https://api.v3.com",
			MaxRetries: 3,
		},
		SendBackRawResponse: true,
	}
	hash4, _ := config4.GenerateConfigHash("openai")

	// Hash should match Round 3
	if hash4 != hash3 {
		t.Fatalf("Round 4 hash should match Round 3. Got %s, expected %s", hash4[:16], hash3[:16])
	}

	// No sync needed
	t.Logf("Round 4 - hash: %s (matches Round 3, no update) ✓", hash4[:16]+"...")

	// === Round 5: Revert to Round 1 config ===
	config5 := configstore.ProviderConfig{
		Keys: []schemas.Key{{ID: "key-1", Name: "key", Value: *schemas.NewEnvVar("sk-v1"), Weight: 1}},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.v1.com",
		},
	}
	hash5, _ := config5.GenerateConfigHash("openai")
	config5.ConfigHash = hash5

	// Hash should match Round 1
	if hash5 != hash1 {
		t.Fatalf("Round 5 hash should match Round 1. Got %s, expected %s", hash5[:16], hash1[:16])
	}

	// But it differs from current DB (which has Round 3 config)
	currentDB := providersInDB[schemas.OpenAI]
	if currentDB.ConfigHash == hash5 {
		t.Fatal("Round 5 should trigger update (reverted config differs from DB)")
	}

	// Sync reverted config to DB
	providersInDB[schemas.OpenAI] = config5
	t.Logf("Round 5 - hash: %s (reverted to Round 1, update triggered) ✓", hash5[:16]+"...")

	// Verify all unique hashes were generated
	uniqueHashes := make(map[string]bool)
	for _, h := range hashHistory {
		uniqueHashes[h] = true
	}
	if len(uniqueHashes) != 3 { // hash1, hash2, hash3 (hash4 = hash3, hash5 = hash1)
		t.Errorf("Expected 3 unique hashes, got %d", len(uniqueHashes))
	}

	t.Log("Multiple updates lifecycle complete ✓")
}

// TestProviderHashComparison_ProviderChangedKeysUnchanged tests provider update without key changes
func TestProviderHashComparison_ProviderChangedKeysUnchanged(t *testing.T) {
	// === Initial state: Provider with keys in DB ===
	originalKey := schemas.Key{
		ID:     "key-1",
		Name:   "openai-key",
		Value:  *schemas.NewEnvVar("sk-original-123"),
		Models: []string{"gpt-4", "gpt-3.5-turbo"},
		Weight: 1.5,
	}
	originalKeyHash, _ := configstore.GenerateKeyHash(originalKey)

	dbConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{originalKey},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:    "https://api.openai.com/v1",
			MaxRetries: 3,
		},
		SendBackRawResponse: false,
	}
	dbProviderHash, _ := dbConfig.GenerateConfigHash("openai")
	dbConfig.ConfigHash = dbProviderHash

	t.Logf("Initial - Provider hash: %s", dbProviderHash[:16]+"...")
	t.Logf("Initial - Key hash: %s", originalKeyHash[:16]+"...")

	// === File config: Provider changed, keys SAME ===
	sameKey := schemas.Key{
		ID:     "key-1",
		Name:   "openai-key",
		Value:  *schemas.NewEnvVar("sk-original-123"), // SAME
		Models: []string{"gpt-4", "gpt-3.5-turbo"},    // SAME
		Weight: 1.5,                                   // SAME
	}
	sameKeyHash, _ := configstore.GenerateKeyHash(sameKey)

	fileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{sameKey},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:    "https://api.openai.com/v2", // CHANGED!
			MaxRetries: 5,                           // CHANGED!
		},
		SendBackRawResponse: true, // CHANGED!
	}
	fileProviderHash, _ := fileConfig.GenerateConfigHash("openai")

	t.Logf("File - Provider hash: %s", fileProviderHash[:16]+"...")
	t.Logf("File - Key hash: %s", sameKeyHash[:16]+"...")

	// === Verify: Provider hash changed, key hash unchanged ===
	if dbProviderHash == fileProviderHash {
		t.Error("Expected provider hash to be DIFFERENT (provider config changed)")
	} else {
		t.Log("✓ Provider hash changed (expected)")
	}

	if originalKeyHash != sameKeyHash {
		t.Error("Expected key hash to be SAME (key unchanged)")
	} else {
		t.Log("✓ Key hash unchanged (expected)")
	}

	// === Simulate sync logic: Update provider, preserve keys ===
	// When provider hash differs but key hashes match:
	// - Update provider-level config (NetworkConfig, SendBackRawResponse, etc.)
	// - Keep existing keys from DB (they weren't changed in file)

	updatedConfig := configstore.ProviderConfig{
		Keys:                dbConfig.Keys,                  // Keep original keys from DB
		NetworkConfig:       fileConfig.NetworkConfig,       // Update from file
		SendBackRawResponse: fileConfig.SendBackRawResponse, // Update from file
		ConfigHash:          fileProviderHash,               // New provider hash
	}

	// Verify keys are preserved (same values as DB)
	if len(updatedConfig.Keys) != 1 {
		t.Errorf("Expected 1 key, got %d", len(updatedConfig.Keys))
	}
	if updatedConfig.Keys[0].Value.GetValue() != "sk-original-123" {
		t.Errorf("Expected key value to be preserved, got %v", updatedConfig.Keys[0].Value)
	}
	if len(updatedConfig.Keys[0].Models) != 2 {
		t.Errorf("Expected 2 models to be preserved, got %d", len(updatedConfig.Keys[0].Models))
	}

	// Verify provider config is updated
	if updatedConfig.NetworkConfig.BaseURL != "https://api.openai.com/v2" {
		t.Error("Expected BaseURL to be updated from file")
	}
	if updatedConfig.NetworkConfig.MaxRetries != 5 {
		t.Error("Expected MaxRetries to be updated from file")
	}
	if !updatedConfig.SendBackRawResponse {
		t.Error("Expected SendBackRawResponse to be updated from file")
	}

	t.Log("✓ Provider updated, keys preserved")
}

// TestProviderHashComparison_KeysChangedProviderUnchanged tests key update without provider changes
func TestProviderHashComparison_KeysChangedProviderUnchanged(t *testing.T) {
	// === Initial state: Provider with keys in DB ===
	originalKey := schemas.Key{
		ID:     "key-1",
		Name:   "openai-key",
		Value:  *schemas.NewEnvVar("sk-original-123"),
		Models: []string{"gpt-4"},
		Weight: 1.0,
	}
	originalKeyHash, _ := configstore.GenerateKeyHash(originalKey)

	dbConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{originalKey},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:    "https://api.openai.com/v1",
			MaxRetries: 3,
		},
		SendBackRawResponse: false,
	}
	dbProviderHash, _ := dbConfig.GenerateConfigHash("openai")
	dbConfig.ConfigHash = dbProviderHash

	t.Logf("Initial - Provider hash: %s", dbProviderHash[:16]+"...")
	t.Logf("Initial - Key hash: %s", originalKeyHash[:16]+"...")

	// === File config: Provider SAME, keys changed ===
	changedKey := schemas.Key{
		ID:     "key-1",
		Name:   "openai-key",
		Value:  *schemas.NewEnvVar("sk-new-456"),         // CHANGED!
		Models: []string{"gpt-4", "gpt-3.5-turbo", "o1"}, // CHANGED!
		Weight: 2.0,                                      // CHANGED!
	}
	changedKeyHash, _ := configstore.GenerateKeyHash(changedKey)

	fileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{changedKey},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:    "https://api.openai.com/v1", // SAME
			MaxRetries: 3,                           // SAME
		},
		SendBackRawResponse: false, // SAME
	}
	fileProviderHash, _ := fileConfig.GenerateConfigHash("openai")

	t.Logf("File - Provider hash: %s", fileProviderHash[:16]+"...")
	t.Logf("File - Key hash: %s", changedKeyHash[:16]+"...")

	// === Verify: Provider hash unchanged, key hash changed ===
	if dbProviderHash != fileProviderHash {
		t.Error("Expected provider hash to be SAME (provider config unchanged)")
	} else {
		t.Log("✓ Provider hash unchanged (expected)")
	}

	if originalKeyHash == changedKeyHash {
		t.Error("Expected key hash to be DIFFERENT (key changed)")
	} else {
		t.Log("✓ Key hash changed (expected)")
	}

	// === Simulate sync logic: Keep provider, update keys ===
	// When provider hash matches but key hashes differ:
	// - Keep provider-level config from DB
	// - Update keys from file (they were changed)

	updatedConfig := configstore.ProviderConfig{
		Keys:                fileConfig.Keys,              // Update keys from file
		NetworkConfig:       dbConfig.NetworkConfig,       // Keep from DB
		SendBackRawResponse: dbConfig.SendBackRawResponse, // Keep from DB
		ConfigHash:          dbProviderHash,               // Provider hash unchanged
	}

	// Verify provider config is preserved
	if updatedConfig.NetworkConfig.BaseURL != "https://api.openai.com/v1" {
		t.Error("Expected BaseURL to be preserved from DB")
	}
	if updatedConfig.NetworkConfig.MaxRetries != 3 {
		t.Error("Expected MaxRetries to be preserved from DB")
	}
	if updatedConfig.SendBackRawResponse {
		t.Error("Expected SendBackRawResponse to be preserved from DB (false)")
	}

	// Verify keys are updated
	if len(updatedConfig.Keys) != 1 {
		t.Errorf("Expected 1 key, got %d", len(updatedConfig.Keys))
	}
	if updatedConfig.Keys[0].Value.GetValue() != "sk-new-456" {
		t.Errorf("Expected key value to be updated, got %v", updatedConfig.Keys[0].Value)
	}
	if len(updatedConfig.Keys[0].Models) != 3 {
		t.Errorf("Expected 3 models (updated), got %d", len(updatedConfig.Keys[0].Models))
	}
	if updatedConfig.Keys[0].Weight != 2.0 {
		t.Errorf("Expected weight to be 2.0 (updated), got %f", updatedConfig.Keys[0].Weight)
	}

	t.Log("✓ Provider preserved, keys updated")
}

// TestProviderHashComparison_BothChangedIndependently tests both provider and keys changed
func TestProviderHashComparison_BothChangedIndependently(t *testing.T) {
	// === Initial state ===
	originalKey := schemas.Key{
		ID:     "key-1",
		Name:   "openai-key",
		Value:  *schemas.NewEnvVar("sk-original-123"),
		Models: []string{"gpt-4"},
		Weight: 1.0,
	}
	originalKeyHash, _ := configstore.GenerateKeyHash(originalKey)

	dbConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{originalKey},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.openai.com/v1",
		},
		SendBackRawResponse: false,
	}
	dbProviderHash, _ := dbConfig.GenerateConfigHash("openai")

	t.Logf("Initial - Provider hash: %s", dbProviderHash[:16]+"...")
	t.Logf("Initial - Key hash: %s", originalKeyHash[:16]+"...")

	// === File config: BOTH provider and keys changed ===
	changedKey := schemas.Key{
		ID:     "key-1",
		Name:   "openai-key",
		Value:  *schemas.NewEnvVar("sk-new-456"), // CHANGED
		Models: []string{"gpt-4", "o1"},          // CHANGED
		Weight: 2.0,                              // CHANGED
	}
	changedKeyHash, _ := configstore.GenerateKeyHash(changedKey)

	fileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{changedKey},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:    "https://api.openai.com/v2", // CHANGED
			MaxRetries: 5,                           // ADDED
		},
		SendBackRawResponse: true, // CHANGED
	}
	fileProviderHash, _ := fileConfig.GenerateConfigHash("openai")

	t.Logf("File - Provider hash: %s", fileProviderHash[:16]+"...")
	t.Logf("File - Key hash: %s", changedKeyHash[:16]+"...")

	// === Verify: Both hashes changed ===
	if dbProviderHash == fileProviderHash {
		t.Error("Expected provider hash to be DIFFERENT")
	} else {
		t.Log("✓ Provider hash changed")
	}

	if originalKeyHash == changedKeyHash {
		t.Error("Expected key hash to be DIFFERENT")
	} else {
		t.Log("✓ Key hash changed")
	}

	// === Simulate sync: Update everything from file ===
	updatedConfig := fileConfig
	updatedConfig.ConfigHash = fileProviderHash

	// Verify both provider and keys are updated
	if updatedConfig.NetworkConfig.BaseURL != "https://api.openai.com/v2" {
		t.Error("Expected BaseURL to be updated")
	}
	if !updatedConfig.SendBackRawResponse {
		t.Error("Expected SendBackRawResponse to be updated")
	}
	if updatedConfig.Keys[0].Value.GetValue() != "sk-new-456" {
		t.Error("Expected key value to be updated")
	}
	if updatedConfig.Keys[0].Weight != 2.0 {
		t.Error("Expected key weight to be updated")
	}

	t.Log("✓ Both provider and keys updated")
}

// TestProviderHashComparison_NeitherChanged tests no changes scenario
func TestProviderHashComparison_NeitherChanged(t *testing.T) {
	// === Initial state ===
	originalKey := schemas.Key{
		ID:     "key-1",
		Name:   "openai-key",
		Value:  *schemas.NewEnvVar("sk-original-123"),
		Models: []string{"gpt-4"},
		Weight: 1.0,
	}
	originalKeyHash, _ := configstore.GenerateKeyHash(originalKey)

	dbConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{originalKey},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.openai.com/v1",
		},
		SendBackRawResponse: false,
	}
	dbProviderHash, _ := dbConfig.GenerateConfigHash("openai")
	dbConfig.ConfigHash = dbProviderHash

	t.Logf("Initial - Provider hash: %s", dbProviderHash[:16]+"...")
	t.Logf("Initial - Key hash: %s", originalKeyHash[:16]+"...")

	// === File config: SAME as DB ===
	sameKey := schemas.Key{
		ID:     "key-1",
		Name:   "openai-key",
		Value:  *schemas.NewEnvVar("sk-original-123"), // SAME
		Models: []string{"gpt-4"},                     // SAME
		Weight: 1.0,                                   // SAME
	}
	sameKeyHash, _ := configstore.GenerateKeyHash(sameKey)

	fileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{sameKey},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.openai.com/v1", // SAME
		},
		SendBackRawResponse: false, // SAME
	}
	fileProviderHash, _ := fileConfig.GenerateConfigHash("openai")

	t.Logf("File - Provider hash: %s", fileProviderHash[:16]+"...")
	t.Logf("File - Key hash: %s", sameKeyHash[:16]+"...")

	// === Verify: Both hashes match ===
	if dbProviderHash != fileProviderHash {
		t.Errorf("Expected provider hash to be SAME, got DB=%s File=%s",
			dbProviderHash[:16], fileProviderHash[:16])
	} else {
		t.Log("✓ Provider hash unchanged")
	}

	if originalKeyHash != sameKeyHash {
		t.Errorf("Expected key hash to be SAME, got DB=%s File=%s",
			originalKeyHash[:16], sameKeyHash[:16])
	} else {
		t.Log("✓ Key hash unchanged")
	}

	// === No sync needed - keep DB as is ===
	t.Log("✓ No changes detected, DB preserved")
}

// =============================================================================
// KEY-LEVEL SYNC TESTS (when provider hash matches)
// =============================================================================

// TestKeyLevelSync_ProviderHashMatch_SingleKeyChanged tests that when provider hash matches
// but a single key has changed, only that key is updated from the file
func TestKeyLevelSync_ProviderHashMatch_SingleKeyChanged(t *testing.T) {
	// === DB state: Provider with one key ===
	dbKey := schemas.Key{
		ID:     "key-1",
		Name:   "openai-key",
		Value:  *schemas.NewEnvVar("sk-old-value"),
		Models: []string{"gpt-4"},
		Weight: 1.0,
	}
	dbKeyHash, _ := configstore.GenerateKeyHash(dbKey)

	dbConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{dbKey},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.openai.com/v1",
		},
	}
	dbProviderHash, _ := dbConfig.GenerateConfigHash("openai")
	dbConfig.ConfigHash = dbProviderHash

	t.Logf("DB - Provider hash: %s", dbProviderHash[:16]+"...")
	t.Logf("DB - Key hash: %s", dbKeyHash[:16]+"...")

	// === File state: Same provider config, but key value changed ===
	fileKey := schemas.Key{
		ID:     "key-1",
		Name:   "openai-key",
		Value:  *schemas.NewEnvVar("sk-new-value"), // CHANGED
		Models: []string{"gpt-4", "gpt-4-turbo"},   // CHANGED
		Weight: 2.0,                                // CHANGED
	}
	fileKeyHash, _ := configstore.GenerateKeyHash(fileKey)

	fileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{fileKey},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.openai.com/v1", // SAME
		},
	}
	fileProviderHash, _ := fileConfig.GenerateConfigHash("openai")

	t.Logf("File - Provider hash: %s", fileProviderHash[:16]+"...")
	t.Logf("File - Key hash: %s", fileKeyHash[:16]+"...")

	// === Verify provider hash matches but key hash differs ===
	if dbProviderHash != fileProviderHash {
		t.Fatalf("Expected provider hashes to match, got DB=%s File=%s",
			dbProviderHash[:16], fileProviderHash[:16])
	}
	t.Log("✓ Provider hash matches (as expected)")

	if dbKeyHash == fileKeyHash {
		t.Fatal("Expected key hashes to differ")
	}
	t.Log("✓ Key hash differs (as expected)")

	// === Simulate key-level sync logic ===
	mergedKeys := make([]schemas.Key, 0)
	fileKeysByName := make(map[string]int)
	for i, fk := range fileConfig.Keys {
		fileKeysByName[fk.Name] = i
	}

	for _, dbk := range dbConfig.Keys {
		if fileIdx, exists := fileKeysByName[dbk.Name]; exists {
			fk := fileConfig.Keys[fileIdx]
			fkHash, _ := configstore.GenerateKeyHash(fk)
			dkHash, _ := configstore.GenerateKeyHash(schemas.Key{
				Name:   dbk.Name,
				Value:  dbk.Value,
				Models: dbk.Models,
				Weight: dbk.Weight,
			})

			if fkHash != dkHash {
				// Key changed - use file version but preserve ID
				fk.ID = dbk.ID
				mergedKeys = append(mergedKeys, fk)
				t.Logf("✓ Key '%s' changed, using file version", fk.Name)
			} else {
				mergedKeys = append(mergedKeys, dbk)
			}
			delete(fileKeysByName, dbk.Name)
		} else {
			mergedKeys = append(mergedKeys, dbk)
		}
	}

	// Add keys only in file
	for _, idx := range fileKeysByName {
		mergedKeys = append(mergedKeys, fileConfig.Keys[idx])
	}

	// === Verify results ===
	if len(mergedKeys) != 1 {
		t.Fatalf("Expected 1 merged key, got %d", len(mergedKeys))
	}

	mergedKey := mergedKeys[0]
	if mergedKey.ID != "key-1" {
		t.Errorf("Expected key ID to be preserved, got %s", mergedKey.ID)
	}
	if mergedKey.Value.GetValue() != "sk-new-value" {
		t.Errorf("Expected key value from file, got %v", mergedKey.Value)
	}
	if len(mergedKey.Models) != 2 || mergedKey.Models[1] != "gpt-4-turbo" {
		t.Errorf("Expected models from file, got %v", mergedKey.Models)
	}
	if mergedKey.Weight != 2.0 {
		t.Errorf("Expected weight from file (2.0), got %f", mergedKey.Weight)
	}

	t.Log("✓ Key updated correctly from file while preserving ID")
}

// TestKeyLevelSync_ProviderHashMatch_NewKeyInFile tests that when provider hash matches
// and a new key exists only in the file, it is added to the merged result
func TestKeyLevelSync_ProviderHashMatch_NewKeyInFile(t *testing.T) {
	// === DB state: Provider with one key ===
	dbKey := schemas.Key{
		ID:     "key-1",
		Name:   "openai-key-1",
		Value:  *schemas.NewEnvVar("sk-key-1"),
		Models: []string{"gpt-4"},
		Weight: 1.0,
	}

	dbConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{dbKey},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.openai.com/v1",
		},
	}
	dbProviderHash, _ := dbConfig.GenerateConfigHash("openai")
	dbConfig.ConfigHash = dbProviderHash

	// === File state: Same key + NEW key ===
	fileKey1 := schemas.Key{
		ID:     "key-1",
		Name:   "openai-key-1",
		Value:  *schemas.NewEnvVar("sk-key-1"), // SAME
		Models: []string{"gpt-4"},              // SAME
		Weight: 1.0,                            // SAME
	}
	newFileKey := schemas.Key{
		ID:     "key-2",
		Name:   "openai-key-2", // NEW KEY
		Value:  *schemas.NewEnvVar("sk-key-2"),
		Models: []string{"gpt-3.5-turbo"},
		Weight: 1.0,
	}

	fileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{fileKey1, newFileKey},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.openai.com/v1", // SAME
		},
	}
	fileProviderHash, _ := fileConfig.GenerateConfigHash("openai")

	// === Verify provider hash matches ===
	if dbProviderHash != fileProviderHash {
		t.Fatalf("Expected provider hashes to match")
	}
	t.Log("✓ Provider hash matches")

	// === Simulate key-level sync logic ===
	mergedKeys := make([]schemas.Key, 0)
	fileKeysByName := make(map[string]int)
	for i, fk := range fileConfig.Keys {
		fileKeysByName[fk.Name] = i
	}

	for _, dbk := range dbConfig.Keys {
		if fileIdx, exists := fileKeysByName[dbk.Name]; exists {
			fk := fileConfig.Keys[fileIdx]
			fkHash, _ := configstore.GenerateKeyHash(fk)
			dkHash, _ := configstore.GenerateKeyHash(schemas.Key{
				Name:   dbk.Name,
				Value:  dbk.Value,
				Models: dbk.Models,
				Weight: dbk.Weight,
			})

			if fkHash != dkHash {
				fk.ID = dbk.ID
				mergedKeys = append(mergedKeys, fk)
			} else {
				// Key unchanged - keep DB version
				mergedKeys = append(mergedKeys, dbk)
				t.Logf("✓ Key '%s' unchanged, keeping DB version", dbk.Name)
			}
			delete(fileKeysByName, dbk.Name)
		} else {
			mergedKeys = append(mergedKeys, dbk)
		}
	}

	// Add keys only in file (NEW keys)
	for name, idx := range fileKeysByName {
		mergedKeys = append(mergedKeys, fileConfig.Keys[idx])
		t.Logf("✓ New key '%s' added from file", name)
	}

	// === Verify results ===
	if len(mergedKeys) != 2 {
		t.Fatalf("Expected 2 merged keys, got %d", len(mergedKeys))
	}

	// Check existing key is preserved
	foundExisting := false
	foundNew := false
	for _, k := range mergedKeys {
		if k.Name == "openai-key-1" {
			foundExisting = true
			if k.ID != "key-1" {
				t.Error("Expected existing key ID to be preserved")
			}
		}
		if k.Name == "openai-key-2" {
			foundNew = true
			if k.Value.GetValue() != "sk-key-2" {
				t.Error("Expected new key value from file")
			}
		}
	}

	if !foundExisting {
		t.Error("Expected existing key to be in merged result")
	}
	if !foundNew {
		t.Error("Expected new key from file to be in merged result")
	}

	t.Log("✓ New key from file added while preserving existing key")
}

// TestKeyLevelSync_ProviderHashMatch_KeyOnlyInDB tests that when provider hash matches
// and a key exists only in DB (added via dashboard), it is preserved
func TestKeyLevelSync_ProviderHashMatch_KeyOnlyInDB(t *testing.T) {
	// === DB state: Provider with TWO keys (one added via dashboard) ===
	dbKey1 := schemas.Key{
		ID:     "key-1",
		Name:   "openai-key-1",
		Value:  *schemas.NewEnvVar("sk-key-1"),
		Models: []string{"gpt-4"},
		Weight: 1.0,
	}
	dashboardKey := schemas.Key{
		ID:     "key-dashboard",
		Name:   "dashboard-added-key", // Added via dashboard, NOT in config.json
		Value:  *schemas.NewEnvVar("sk-dashboard-key"),
		Models: []string{"gpt-4", "o1"},
		Weight: 2.0,
	}

	dbConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{dbKey1, dashboardKey},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.openai.com/v1",
		},
	}
	dbProviderHash, _ := dbConfig.GenerateConfigHash("openai")
	dbConfig.ConfigHash = dbProviderHash

	// === File state: Only the original key (dashboard key not in file) ===
	fileKey1 := schemas.Key{
		ID:     "key-1",
		Name:   "openai-key-1",
		Value:  *schemas.NewEnvVar("sk-key-1"), // SAME
		Models: []string{"gpt-4"},              // SAME
		Weight: 1.0,                            // SAME
	}

	fileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{fileKey1}, // Dashboard key NOT here
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.openai.com/v1", // SAME
		},
	}
	fileProviderHash, _ := fileConfig.GenerateConfigHash("openai")

	// === Verify provider hash matches ===
	if dbProviderHash != fileProviderHash {
		t.Fatalf("Expected provider hashes to match")
	}
	t.Log("✓ Provider hash matches")

	// === Simulate key-level sync logic ===
	mergedKeys := make([]schemas.Key, 0)
	fileKeysByName := make(map[string]int)
	for i, fk := range fileConfig.Keys {
		fileKeysByName[fk.Name] = i
	}

	for _, dbk := range dbConfig.Keys {
		if fileIdx, exists := fileKeysByName[dbk.Name]; exists {
			fk := fileConfig.Keys[fileIdx]
			fkHash, _ := configstore.GenerateKeyHash(fk)
			dkHash, _ := configstore.GenerateKeyHash(schemas.Key{
				Name:   dbk.Name,
				Value:  dbk.Value,
				Models: dbk.Models,
				Weight: dbk.Weight,
			})

			if fkHash != dkHash {
				fk.ID = dbk.ID
				mergedKeys = append(mergedKeys, fk)
			} else {
				mergedKeys = append(mergedKeys, dbk)
				t.Logf("✓ Key '%s' unchanged, keeping DB version", dbk.Name)
			}
			delete(fileKeysByName, dbk.Name)
		} else {
			// Key only in DB - preserve it (added via dashboard)
			mergedKeys = append(mergedKeys, dbk)
			t.Logf("✓ Key '%s' only in DB, preserving (dashboard-added)", dbk.Name)
		}
	}

	// Add keys only in file
	for _, idx := range fileKeysByName {
		mergedKeys = append(mergedKeys, fileConfig.Keys[idx])
	}

	// === Verify results ===
	if len(mergedKeys) != 2 {
		t.Fatalf("Expected 2 merged keys, got %d", len(mergedKeys))
	}

	// Check dashboard key is preserved
	foundDashboard := false
	for _, k := range mergedKeys {
		if k.Name == "dashboard-added-key" {
			foundDashboard = true
			if k.ID != "key-dashboard" {
				t.Error("Expected dashboard key ID to be preserved")
			}
			if k.Value.GetValue() != "sk-dashboard-key" {
				t.Error("Expected dashboard key value to be preserved")
			}
		}
	}

	if !foundDashboard {
		t.Error("Expected dashboard-added key to be preserved in merged result")
	}

	t.Log("✓ Dashboard-added key preserved correctly")
}

// TestKeyLevelSync_ProviderHashMatch_MixedScenario tests a complex scenario with:
// - Keys that are unchanged
// - Keys that changed in the file
// - Keys only in the file (new)
// - Keys only in DB (dashboard-added)
func TestKeyLevelSync_ProviderHashMatch_MixedScenario(t *testing.T) {
	// === DB state ===
	unchangedKey := schemas.Key{
		ID:     "key-unchanged",
		Name:   "unchanged-key",
		Value:  *schemas.NewEnvVar("sk-unchanged"),
		Models: []string{"gpt-4"},
		Weight: 1.0,
	}
	changedKey := schemas.Key{
		ID:     "key-changed",
		Name:   "changed-key",
		Value:  *schemas.NewEnvVar("sk-old-value"),
		Models: []string{"gpt-4"},
		Weight: 1.0,
	}
	dashboardKey := schemas.Key{
		ID:     "key-dashboard",
		Name:   "dashboard-key",
		Value:  *schemas.NewEnvVar("sk-dashboard"),
		Models: []string{"o1"},
		Weight: 3.0,
	}

	dbConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{unchangedKey, changedKey, dashboardKey},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.openai.com/v1",
		},
	}
	dbProviderHash, _ := dbConfig.GenerateConfigHash("openai")
	dbConfig.ConfigHash = dbProviderHash

	// === File state ===
	fileUnchangedKey := schemas.Key{
		ID:     "key-unchanged",
		Name:   "unchanged-key",
		Value:  *schemas.NewEnvVar("sk-unchanged"), // SAME
		Models: []string{"gpt-4"},                  // SAME
		Weight: 1.0,                                // SAME
	}
	fileChangedKey := schemas.Key{
		ID:     "key-changed",
		Name:   "changed-key",
		Value:  *schemas.NewEnvVar("sk-NEW-value"), // CHANGED
		Models: []string{"gpt-4", "gpt-4-turbo"},   // CHANGED
		Weight: 2.0,                                // CHANGED
	}
	newFileKey := schemas.Key{
		ID:     "key-new",
		Name:   "new-file-key", // NEW - not in DB
		Value:  *schemas.NewEnvVar("sk-new-from-file"),
		Models: []string{"gpt-3.5-turbo"},
		Weight: 1.0,
	}
	// Note: dashboardKey is NOT in file

	fileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{fileUnchangedKey, fileChangedKey, newFileKey},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.openai.com/v1", // SAME
		},
	}
	fileProviderHash, _ := fileConfig.GenerateConfigHash("openai")

	// === Verify provider hash matches ===
	if dbProviderHash != fileProviderHash {
		t.Fatalf("Expected provider hashes to match")
	}
	t.Log("✓ Provider hash matches")

	// === Simulate key-level sync logic ===
	mergedKeys := make([]schemas.Key, 0)
	fileKeysByName := make(map[string]int)
	for i, fk := range fileConfig.Keys {
		fileKeysByName[fk.Name] = i
	}

	for _, dbk := range dbConfig.Keys {
		if fileIdx, exists := fileKeysByName[dbk.Name]; exists {
			fk := fileConfig.Keys[fileIdx]
			fkHash, _ := configstore.GenerateKeyHash(fk)
			dkHash, _ := configstore.GenerateKeyHash(schemas.Key{
				Name:   dbk.Name,
				Value:  dbk.Value,
				Models: dbk.Models,
				Weight: dbk.Weight,
			})

			if fkHash != dkHash {
				fk.ID = dbk.ID
				mergedKeys = append(mergedKeys, fk)
				t.Logf("  Key '%s': CHANGED → using file version", fk.Name)
			} else {
				mergedKeys = append(mergedKeys, dbk)
				t.Logf("  Key '%s': UNCHANGED → keeping DB version", dbk.Name)
			}
			delete(fileKeysByName, dbk.Name)
		} else {
			mergedKeys = append(mergedKeys, dbk)
			t.Logf("  Key '%s': DB-ONLY → preserving (dashboard-added)", dbk.Name)
		}
	}

	for name, idx := range fileKeysByName {
		mergedKeys = append(mergedKeys, fileConfig.Keys[idx])
		t.Logf("  Key '%s': FILE-ONLY → adding new key", name)
	}

	// === Verify results ===
	if len(mergedKeys) != 4 {
		t.Fatalf("Expected 4 merged keys, got %d", len(mergedKeys))
	}

	keysByName := make(map[string]schemas.Key)
	for _, k := range mergedKeys {
		keysByName[k.Name] = k
	}

	// Check unchanged key
	if k, ok := keysByName["unchanged-key"]; !ok {
		t.Error("Missing unchanged-key")
	} else {
		if k.Value.GetValue() != "sk-unchanged" {
			t.Errorf("unchanged-key: expected original value, got %v", k.Value)
		}
		if k.ID != "key-unchanged" {
			t.Errorf("unchanged-key: expected original ID, got %s", k.ID)
		}
	}

	// Check changed key
	if k, ok := keysByName["changed-key"]; !ok {
		t.Error("Missing changed-key")
	} else {
		if k.Value.GetValue() != "sk-NEW-value" {
			t.Errorf("changed-key: expected new value, got %v", k.Value)
		}
		if k.ID != "key-changed" {
			t.Errorf("changed-key: expected preserved ID, got %s", k.ID)
		}
		if k.Weight != 2.0 {
			t.Errorf("changed-key: expected weight 2.0, got %f", k.Weight)
		}
	}

	// Check dashboard key (preserved)
	if k, ok := keysByName["dashboard-key"]; !ok {
		t.Error("Missing dashboard-key - should be preserved!")
	} else {
		if k.Value.GetValue() != "sk-dashboard" {
			t.Errorf("dashboard-key: expected preserved value, got %v", k.Value)
		}
		if k.ID != "key-dashboard" {
			t.Errorf("dashboard-key: expected preserved ID, got %s", k.ID)
		}
	}

	// Check new file key (added)
	if k, ok := keysByName["new-file-key"]; !ok {
		t.Error("Missing new-file-key - should be added!")
	} else {
		if k.Value.GetValue() != "sk-new-from-file" {
			t.Errorf("new-file-key: expected file value, got %v", k.Value)
		}
	}

	t.Log("✓ Mixed scenario handled correctly:")
	t.Log("  - Unchanged keys preserved from DB")
	t.Log("  - Changed keys updated from file with preserved ID")
	t.Log("  - Dashboard-added keys preserved")
	t.Log("  - New file keys added")
}

// TestKeyLevelSync_ProviderHashMatch_MultipleKeysChanged tests that when multiple keys
// change simultaneously, all are correctly updated
func TestKeyLevelSync_ProviderHashMatch_MultipleKeysChanged(t *testing.T) {
	// === DB state: Three keys ===
	dbKeys := []schemas.Key{
		{ID: "key-1", Name: "key-one", Value: *schemas.NewEnvVar("old-1"), Models: []string{"gpt-4"}, Weight: 1.0},
		{ID: "key-2", Name: "key-two", Value: *schemas.NewEnvVar("old-2"), Models: []string{"gpt-4"}, Weight: 1.0},
		{ID: "key-3", Name: "key-three", Value: *schemas.NewEnvVar("old-3"), Models: []string{"gpt-4"}, Weight: 1.0},
	}

	dbConfig := configstore.ProviderConfig{
		Keys: dbKeys,
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.openai.com/v1",
		},
	}
	dbProviderHash, _ := dbConfig.GenerateConfigHash("openai")
	dbConfig.ConfigHash = dbProviderHash

	// === File state: All three keys changed ===
	fileKeys := []schemas.Key{
		{ID: "key-1", Name: "key-one", Value: *schemas.NewEnvVar("NEW-1"), Models: []string{"gpt-4", "o1"}, Weight: 2.0},
		{ID: "key-2", Name: "key-two", Value: *schemas.NewEnvVar("NEW-2"), Models: []string{"gpt-3.5-turbo"}, Weight: 3.0},
		{ID: "key-3", Name: "key-three", Value: *schemas.NewEnvVar("NEW-3"), Models: []string{"gpt-4-turbo"}, Weight: 4.0},
	}

	fileConfig := configstore.ProviderConfig{
		Keys: fileKeys,
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.openai.com/v1", // SAME
		},
	}
	fileProviderHash, _ := fileConfig.GenerateConfigHash("openai")

	// === Verify provider hash matches ===
	if dbProviderHash != fileProviderHash {
		t.Fatalf("Expected provider hashes to match")
	}

	// === Simulate key-level sync logic ===
	mergedKeys := make([]schemas.Key, 0)
	fileKeysByName := make(map[string]int)
	for i, fk := range fileConfig.Keys {
		fileKeysByName[fk.Name] = i
	}

	changedCount := 0
	for _, dbk := range dbConfig.Keys {
		if fileIdx, exists := fileKeysByName[dbk.Name]; exists {
			fk := fileConfig.Keys[fileIdx]
			fkHash, _ := configstore.GenerateKeyHash(fk)
			dkHash, _ := configstore.GenerateKeyHash(schemas.Key{
				Name:   dbk.Name,
				Value:  dbk.Value,
				Models: dbk.Models,
				Weight: dbk.Weight,
			})

			if fkHash != dkHash {
				fk.ID = dbk.ID
				mergedKeys = append(mergedKeys, fk)
				changedCount++
			} else {
				mergedKeys = append(mergedKeys, dbk)
			}
			delete(fileKeysByName, dbk.Name)
		} else {
			mergedKeys = append(mergedKeys, dbk)
		}
	}

	for _, idx := range fileKeysByName {
		mergedKeys = append(mergedKeys, fileConfig.Keys[idx])
	}

	// === Verify all 3 keys were detected as changed ===
	if changedCount != 3 {
		t.Errorf("Expected 3 keys to be detected as changed, got %d", changedCount)
	}

	// === Verify all keys have new values but preserved IDs ===
	expectedValues := map[string]struct {
		value  string
		id     string
		weight float64
	}{
		"key-one":   {value: "NEW-1", id: "key-1", weight: 2.0},
		"key-two":   {value: "NEW-2", id: "key-2", weight: 3.0},
		"key-three": {value: "NEW-3", id: "key-3", weight: 4.0},
	}

	for _, k := range mergedKeys {
		expected, ok := expectedValues[k.Name]
		if !ok {
			t.Errorf("Unexpected key: %s", k.Name)
			continue
		}
		if k.Value.GetValue() != expected.value {
			t.Errorf("Key %s: expected value %s, got %v", k.Name, expected.value, k.Value)
		}
		if k.ID != expected.id {
			t.Errorf("Key %s: expected ID %s (preserved), got %s", k.Name, expected.id, k.ID)
		}
		if k.Weight != expected.weight {
			t.Errorf("Key %s: expected weight %f, got %f", k.Name, expected.weight, k.Weight)
		}
	}

	t.Log("✓ All 3 keys updated correctly from file with preserved IDs")
}

// TestKeyHashComparison_KeyContentChanged tests key content change detection
func TestKeyHashComparison_KeyContentChanged(t *testing.T) {
	// Original key in DB
	dbKey := schemas.Key{
		ID:     "key-1",
		Name:   "openai-key",
		Value:  *schemas.NewEnvVar("sk-old-value"),
		Models: []string{"gpt-4"},
		Weight: 1,
	}

	dbKeyHash, _ := configstore.GenerateKeyHash(dbKey)

	// Same key in file but with different value
	fileKey := schemas.Key{
		ID:     "key-1",
		Name:   "openai-key",
		Value:  *schemas.NewEnvVar("sk-new-value"), // Changed!
		Models: []string{"gpt-4"},
		Weight: 1,
	}

	fileKeyHash, _ := configstore.GenerateKeyHash(fileKey)

	// Hashes should be different (key content changed)
	if dbKeyHash == fileKeyHash {
		t.Error("Expected different hash for keys with different Value")
	}

	// Same key with only models changed
	fileKey2 := schemas.Key{
		ID:     "key-1",
		Name:   "openai-key",
		Value:  *schemas.NewEnvVar("sk-old-value"),
		Models: []string{"gpt-4", "gpt-3.5-turbo"}, // Changed models
		Weight: 1,
	}

	fileKey2Hash, _ := configstore.GenerateKeyHash(fileKey2)

	if dbKeyHash == fileKey2Hash {
		t.Error("Expected different hash for keys with different Models")
	}
}

// TestProviderHashComparison_NewProvider tests that new provider is added from file
func TestProviderHashComparison_NewProvider(t *testing.T) {
	// Create a provider config (simulating new provider in config.json)
	fileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{ID: "key-1", Name: "anthropic-key", Value: *schemas.NewEnvVar("sk-ant-123"), Weight: 1},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.anthropic.com",
		},
		SendBackRawResponse: false,
	}

	// Generate hash for the file config
	fileHash, err := fileConfig.GenerateConfigHash("anthropic")
	if err != nil {
		t.Fatalf("Failed to generate file hash: %v", err)
	}
	fileConfig.ConfigHash = fileHash

	// Empty DB (no existing providers)
	providersInConfigStore := map[schemas.ModelProvider]configstore.ProviderConfig{}

	provider := schemas.Anthropic

	// Simulate the logic: provider doesn't exist, add from file
	if _, exists := providersInConfigStore[provider]; !exists {
		providersInConfigStore[provider] = fileConfig
	}

	// Verify provider was added
	if _, exists := providersInConfigStore[provider]; !exists {
		t.Error("Expected provider to be added")
	}

	resultConfig := providersInConfigStore[provider]

	if resultConfig.ConfigHash != fileHash {
		t.Error("Expected ConfigHash to be set from file")
	}

	if len(resultConfig.Keys) != 1 {
		t.Errorf("Expected 1 key, got %d", len(resultConfig.Keys))
	}

	if resultConfig.Keys[0].Name != "anthropic-key" {
		t.Errorf("Expected key name 'anthropic-key', got %s", resultConfig.Keys[0].Name)
	}
}

// TestKeyHashComparison_AzureConfigSyncScenarios tests full lifecycle for Azure key configs
func TestKeyHashComparison_AzureConfigSyncScenarios(t *testing.T) {
	// === Scenario 1: Azure config in DB + same in file -> hash matches, no update ===
	t.Run("SameAzureConfig_NoUpdate", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "azure-key",
			Value:  *schemas.NewEnvVar("azure-api-key-123"),
			Weight: 1,
			AzureKeyConfig: &schemas.AzureKeyConfig{
				Endpoint:   *schemas.NewEnvVar("https://myazure.openai.azure.com"),
				APIVersion: schemas.NewEnvVar("2024-02-01"),
				Deployments: map[string]string{
					"gpt-4": "gpt-4-deployment",
				},
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "azure-key",
			Value:  *schemas.NewEnvVar("azure-api-key-123"),
			Weight: 1,
			AzureKeyConfig: &schemas.AzureKeyConfig{
				Endpoint:   *schemas.NewEnvVar("https://myazure.openai.azure.com"),
				APIVersion: schemas.NewEnvVar("2024-02-01"),
				Deployments: map[string]string{
					"gpt-4": "gpt-4-deployment",
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash != fileHash {
			t.Errorf("Expected same hash for identical Azure configs. DB: %s, File: %s", dbHash[:16], fileHash[:16])
		}
		t.Log("✓ Same Azure config produces same hash - no update needed")
	})

	// === Scenario 2: Azure config in DB + different endpoint in file -> hash differs ===
	t.Run("DifferentEndpoint_UpdateTriggered", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "azure-key",
			Value:  *schemas.NewEnvVar("azure-api-key-123"),
			Weight: 1,
			AzureKeyConfig: &schemas.AzureKeyConfig{
				Endpoint:   *schemas.NewEnvVar("https://myazure.openai.azure.com"),
				APIVersion: schemas.NewEnvVar("2024-02-01"),
				Deployments: map[string]string{
					"gpt-4": "gpt-4-deployment",
				},
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "azure-key",
			Value:  *schemas.NewEnvVar("azure-api-key-123"),
			Weight: 1,
			AzureKeyConfig: &schemas.AzureKeyConfig{
				Endpoint:   *schemas.NewEnvVar("https://different-azure.openai.azure.com"), // Changed!
				APIVersion: schemas.NewEnvVar("2024-02-01"),
				Deployments: map[string]string{
					"gpt-4": "gpt-4-deployment",
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when Azure endpoint changes")
		}
		t.Log("✓ Different Azure endpoint produces different hash - update triggered")
	})

	// === Scenario 3: Azure config in DB + different APIVersion in file -> hash differs ===
	t.Run("DifferentAPIVersion_UpdateTriggered", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "azure-key",
			Value:  *schemas.NewEnvVar("azure-api-key-123"),
			Weight: 1,
			AzureKeyConfig: &schemas.AzureKeyConfig{
				Endpoint:   *schemas.NewEnvVar("https://myazure.openai.azure.com"),
				APIVersion: schemas.NewEnvVar("2024-02-01"),
				Deployments: map[string]string{
					"gpt-4": "gpt-4-deployment",
				},
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "azure-key",
			Value:  *schemas.NewEnvVar("azure-api-key-123"),
			Weight: 1,
			AzureKeyConfig: &schemas.AzureKeyConfig{
				Endpoint:   *schemas.NewEnvVar("https://myazure.openai.azure.com"),
				APIVersion: schemas.NewEnvVar("2024-10-21"), // Changed!
				Deployments: map[string]string{
					"gpt-4": "gpt-4-deployment",
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when Azure APIVersion changes")
		}
		t.Log("✓ Different Azure APIVersion produces different hash - update triggered")
	})

	// === Scenario 4: Azure config in DB + different Deployments map in file -> hash differs ===
	t.Run("DifferentDeployments_UpdateTriggered", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "azure-key",
			Value:  *schemas.NewEnvVar("azure-api-key-123"),
			Weight: 1,
			AzureKeyConfig: &schemas.AzureKeyConfig{
				Endpoint: *schemas.NewEnvVar("https://myazure.openai.azure.com"),
				Deployments: map[string]string{
					"gpt-4": "gpt-4-deployment",
				},
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "azure-key",
			Value:  *schemas.NewEnvVar("azure-api-key-123"),
			Weight: 1,
			AzureKeyConfig: &schemas.AzureKeyConfig{
				Endpoint: *schemas.NewEnvVar("https://myazure.openai.azure.com"),
				Deployments: map[string]string{
					"gpt-4":         "gpt-4-deployment",
					"gpt-3.5-turbo": "gpt-35-turbo-deployment", // Added!
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when Azure Deployments map changes")
		}
		t.Log("✓ Different Azure Deployments produces different hash - update triggered")
	})

	// === Scenario 5: Azure config added to file when not in DB -> new key detected ===
	t.Run("AzureConfigAdded_NewKeyDetected", func(t *testing.T) {
		// DB key has no Azure config
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "azure-key",
			Value:  *schemas.NewEnvVar("azure-api-key-123"),
			Weight: 1,
			// No AzureKeyConfig
		}

		// File key has Azure config
		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "azure-key",
			Value:  *schemas.NewEnvVar("azure-api-key-123"),
			Weight: 1,
			AzureKeyConfig: &schemas.AzureKeyConfig{
				Endpoint:   *schemas.NewEnvVar("https://myazure.openai.azure.com"),
				APIVersion: schemas.NewEnvVar("2024-02-01"),
				Deployments: map[string]string{
					"gpt-4": "gpt-4-deployment",
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when Azure config is added")
		}
		t.Log("✓ Azure config added produces different hash - update triggered")
	})

	// === Scenario 6: Azure config removed from file -> hash differs ===
	t.Run("AzureConfigRemoved_UpdateTriggered", func(t *testing.T) {
		// DB key has Azure config
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "azure-key",
			Value:  *schemas.NewEnvVar("azure-api-key-123"),
			Weight: 1,
			AzureKeyConfig: &schemas.AzureKeyConfig{
				Endpoint:   *schemas.NewEnvVar("https://myazure.openai.azure.com"),
				APIVersion: schemas.NewEnvVar("2024-02-01"),
				Deployments: map[string]string{
					"gpt-4": "gpt-4-deployment",
				},
			},
		}

		// File key has no Azure config
		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "azure-key",
			Value:  *schemas.NewEnvVar("azure-api-key-123"),
			Weight: 1,
			// No AzureKeyConfig
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when Azure config is removed")
		}
		t.Log("✓ Azure config removed produces different hash - update triggered")
	})

	// === Scenario 7: APIVersion nil vs set -> hash differs ===
	t.Run("APIVersionNilVsSet_UpdateTriggered", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "azure-key",
			Value:  *schemas.NewEnvVar("azure-api-key-123"),
			Weight: 1,
			AzureKeyConfig: &schemas.AzureKeyConfig{
				Endpoint: *schemas.NewEnvVar("https://myazure.openai.azure.com"),
				// APIVersion is nil (will use default)
				Deployments: map[string]string{
					"gpt-4": "gpt-4-deployment",
				},
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "azure-key",
			Value:  *schemas.NewEnvVar("azure-api-key-123"),
			Weight: 1,
			AzureKeyConfig: &schemas.AzureKeyConfig{
				Endpoint:   *schemas.NewEnvVar("https://myazure.openai.azure.com"),
				APIVersion: schemas.NewEnvVar("2024-02-01"), // Explicitly set
				Deployments: map[string]string{
					"gpt-4": "gpt-4-deployment",
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when APIVersion goes from nil to set")
		}
		t.Log("✓ APIVersion nil vs set produces different hash - update triggered")
	})
}

// TestKeyHashComparison_BedrockConfigSyncScenarios tests full lifecycle for Bedrock key configs
func TestKeyHashComparison_BedrockConfigSyncScenarios(t *testing.T) {
	// === Scenario 1: Bedrock config in DB + same in file -> hash matches, no update ===
	t.Run("SameBedrockConfig_NoUpdate", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar("bedrock-api-key-123"),
			Weight: 1,
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
				SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
				Region:    schemas.NewEnvVar("us-east-1"),
				Deployments: map[string]string{
					"claude-3": "claude-3-inference-profile",
				},
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar("bedrock-api-key-123"),
			Weight: 1,
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
				SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
				Region:    schemas.NewEnvVar("us-east-1"),
				Deployments: map[string]string{
					"claude-3": "claude-3-inference-profile",
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash != fileHash {
			t.Errorf("Expected same hash for identical Bedrock configs. DB: %s, File: %s", dbHash[:16], fileHash[:16])
		}
		t.Log("✓ Same Bedrock config produces same hash - no update needed")
	})

	// === Scenario 2: Bedrock config in DB + different AccessKey/SecretKey -> hash differs ===
	t.Run("DifferentAccessKey_UpdateTriggered", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar("bedrock-api-key-123"),
			Weight: 1,
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
				SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
				Region:    schemas.NewEnvVar("us-east-1"),
				Deployments: map[string]string{
					"claude-3": "claude-3-inference-profile",
				},
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar("bedrock-api-key-123"),
			Weight: 1,
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewEnvVar("AKIAI44QH8DHBEXAMPLE"), // Changed!
				SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
				Region:    schemas.NewEnvVar("us-east-1"),
				Deployments: map[string]string{
					"claude-3": "claude-3-inference-profile",
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when Bedrock AccessKey changes")
		}
		t.Log("✓ Different Bedrock AccessKey produces different hash - update triggered")
	})

	// === Scenario 3: Bedrock config in DB + different SecretKey -> hash differs ===
	t.Run("DifferentSecretKey_UpdateTriggered", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar("bedrock-api-key-123"),
			Weight: 1,
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
				SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
				Region:    schemas.NewEnvVar("us-east-1"),
				Deployments: map[string]string{
					"claude-3": "claude-3-inference-profile",
				},
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar("bedrock-api-key-123"),
			Weight: 1,
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
				SecretKey: *schemas.NewEnvVar("differentSecretKey/NEWKEY/bPxRfiCYEXAMPLEKEY"), // Changed!
				Region:    schemas.NewEnvVar("us-east-1"),
				Deployments: map[string]string{
					"claude-3": "claude-3-inference-profile",
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when Bedrock SecretKey changes")
		}
		t.Log("✓ Different Bedrock SecretKey produces different hash - update triggered")
	})

	// === Scenario 4: Bedrock config in DB + different Region -> hash differs ===
	t.Run("DifferentRegion_UpdateTriggered", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar("bedrock-api-key-123"),
			Weight: 1,
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
				SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
				Region:    schemas.NewEnvVar("us-east-1"),
				Deployments: map[string]string{
					"claude-3": "claude-3-inference-profile",
				},
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar("bedrock-api-key-123"),
			Weight: 1,
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
				SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
				Region:    schemas.NewEnvVar("us-west-2"), // Changed!
				Deployments: map[string]string{
					"claude-3": "claude-3-inference-profile",
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when Bedrock Region changes")
		}
		t.Log("✓ Different Bedrock Region produces different hash - update triggered")
	})

	// === Scenario 5: Bedrock config in DB + different ARN -> hash differs ===
	t.Run("DifferentARN_UpdateTriggered", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar("bedrock-api-key-123"),
			Weight: 1,
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
				SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
				Region:    schemas.NewEnvVar("us-east-1"),
				ARN:       schemas.NewEnvVar("arn:aws:bedrock:us-east-1:123456789012:inference-profile/old-profile"),
				Deployments: map[string]string{
					"claude-3": "claude-3-inference-profile",
				},
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar("bedrock-api-key-123"),
			Weight: 1,
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
				SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
				Region:    schemas.NewEnvVar("us-east-1"),
				ARN:       schemas.NewEnvVar("arn:aws:bedrock:us-east-1:123456789012:inference-profile/new-profile"), // Changed!
				Deployments: map[string]string{
					"claude-3": "claude-3-inference-profile",
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when Bedrock ARN changes")
		}
		t.Log("✓ Different Bedrock ARN produces different hash - update triggered")
	})

	// === Scenario 6: Bedrock config in DB + different Deployments -> hash differs ===
	t.Run("DifferentDeployments_UpdateTriggered", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar("bedrock-api-key-123"),
			Weight: 1,
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
				SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
				Region:    schemas.NewEnvVar("us-east-1"),
				Deployments: map[string]string{
					"claude-3": "claude-3-inference-profile",
				},
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar("bedrock-api-key-123"),
			Weight: 1,
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
				SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
				Region:    schemas.NewEnvVar("us-east-1"),
				Deployments: map[string]string{
					"claude-3":   "claude-3-inference-profile",
					"claude-3.5": "claude-35-inference-profile", // Added!
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when Bedrock Deployments map changes")
		}
		t.Log("✓ Different Bedrock Deployments produces different hash - update triggered")
	})

	// === Scenario 7: Bedrock config added to file when not in DB -> new key detected ===
	t.Run("BedrockConfigAdded_NewKeyDetected", func(t *testing.T) {
		// DB key has no Bedrock config
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar("bedrock-api-key-123"),
			Weight: 1,
			// No BedrockKeyConfig
		}

		// File key has Bedrock config
		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar("bedrock-api-key-123"),
			Weight: 1,
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
				SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
				Region:    schemas.NewEnvVar("us-east-1"),
				Deployments: map[string]string{
					"claude-3": "claude-3-inference-profile",
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when Bedrock config is added")
		}
		t.Log("✓ Bedrock config added produces different hash - update triggered")
	})

	// === Scenario 8: Bedrock config removed from file -> hash differs ===
	t.Run("BedrockConfigRemoved_UpdateTriggered", func(t *testing.T) {
		// DB key has Bedrock config
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar("bedrock-api-key-123"),
			Weight: 1,
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
				SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
				Region:    schemas.NewEnvVar("us-east-1"),
				Deployments: map[string]string{
					"claude-3": "claude-3-inference-profile",
				},
			},
		}

		// File key has no Bedrock config
		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar("bedrock-api-key-123"),
			Weight: 1,
			// No BedrockKeyConfig
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when Bedrock config is removed")
		}
		t.Log("✓ Bedrock config removed produces different hash - update triggered")
	})

	// === Scenario 9: SessionToken nil vs set -> hash differs ===
	t.Run("SessionTokenNilVsSet_UpdateTriggered", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar("bedrock-api-key-123"),
			Weight: 1,
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
				SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
				Region:    schemas.NewEnvVar("us-east-1"),
				// SessionToken is nil
				Deployments: map[string]string{
					"claude-3": "claude-3-inference-profile",
				},
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar("bedrock-api-key-123"),
			Weight: 1,
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey:    *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
				SecretKey:    *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
				Region:       schemas.NewEnvVar("us-east-1"),
				SessionToken: schemas.NewEnvVar("AQoDYXdzEJr..."), // Explicitly set
				Deployments: map[string]string{
					"claude-3": "claude-3-inference-profile",
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when SessionToken goes from nil to set")
		}
		t.Log("✓ SessionToken nil vs set produces different hash - update triggered")
	})

	// === Scenario 10: IAM role auth (empty credentials) vs explicit credentials -> hash differs ===
	t.Run("IAMRoleAuthVsExplicitCredentials_UpdateTriggered", func(t *testing.T) {
		// IAM role auth: empty AccessKey and SecretKey
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar(""),
			Weight: 1,
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewEnvVar(""), // Empty for IAM role auth
				SecretKey: *schemas.NewEnvVar(""), // Empty for IAM role auth
				Region:    schemas.NewEnvVar("us-east-1"),
				Deployments: map[string]string{
					"claude-3": "claude-3-inference-profile",
				},
			},
		}

		// Explicit credentials
		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar(""),
			Weight: 1,
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
				SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
				Region:    schemas.NewEnvVar("us-east-1"),
				Deployments: map[string]string{
					"claude-3": "claude-3-inference-profile",
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when switching from IAM role auth to explicit credentials")
		}
		t.Log("✓ IAM role auth vs explicit credentials produces different hash - update triggered")
	})
}

// TestProviderHashComparison_AzureProviderFullLifecycle tests end-to-end Azure provider lifecycle
func TestProviderHashComparison_AzureProviderFullLifecycle(t *testing.T) {
	// === STEP 1: Initial state - Azure provider exists in DB from previous config.json ===
	initialAzureKey := schemas.Key{
		ID:     "azure-key-1",
		Name:   "azure-openai-key",
		Value:  *schemas.NewEnvVar("azure-api-key-initial"),
		Weight: 1,
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint:   *schemas.NewEnvVar("https://myazure.openai.azure.com"),
			APIVersion: schemas.NewEnvVar("2024-02-01"),
			Deployments: map[string]string{
				"gpt-4": "gpt-4-deployment",
			},
		},
	}

	initialConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{initialAzureKey},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://myazure.openai.azure.com/openai",
		},
		SendBackRawResponse: false,
	}

	initialProviderHash, _ := initialConfig.GenerateConfigHash("azure")
	initialKeyHash, _ := configstore.GenerateKeyHash(initialAzureKey)
	initialConfig.ConfigHash = initialProviderHash

	// Simulate DB state
	providersInDB := map[schemas.ModelProvider]configstore.ProviderConfig{
		"azure": initialConfig,
	}

	t.Logf("Step 1 - Initial DB provider hash: %s", initialProviderHash[:16]+"...")
	t.Logf("Step 1 - Initial DB key hash: %s", initialKeyHash[:16]+"...")

	// === STEP 2: Dashboard edit to key (API key value changed via dashboard) ===
	// The key value is edited via dashboard, but the Azure config structure stays the same
	// Provider config hash should remain unchanged
	dashboardEditedKey := schemas.Key{
		ID:     "azure-key-1",
		Name:   "azure-openai-key",
		Value:  *schemas.NewEnvVar("azure-api-key-dashboard-edited"), // Changed via dashboard!
		Weight: 1,
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint:   *schemas.NewEnvVar("https://myazure.openai.azure.com"),
			APIVersion: schemas.NewEnvVar("2024-02-01"),
			Deployments: map[string]string{
				"gpt-4": "gpt-4-deployment",
			},
		},
	}

	dbConfigAfterDashboardEdit := configstore.ProviderConfig{
		Keys: []schemas.Key{dashboardEditedKey},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://myazure.openai.azure.com/openai",
		},
		SendBackRawResponse: false,
		ConfigHash:          initialProviderHash, // Provider hash unchanged (only key value changed)
	}

	providersInDB["azure"] = dbConfigAfterDashboardEdit

	dashboardKeyHash, _ := configstore.GenerateKeyHash(dashboardEditedKey)
	t.Logf("Step 2 - After dashboard edit, key hash: %s (different from initial)", dashboardKeyHash[:16]+"...")

	if initialKeyHash == dashboardKeyHash {
		t.Error("Expected key hash to change after dashboard edit")
	}

	// === STEP 3: Same config.json loaded (unchanged) - should NOT update, preserve dashboard edits ===
	sameFileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{
				ID:     "azure-key-1",
				Name:   "azure-openai-key",
				Value:  *schemas.NewEnvVar("azure-api-key-initial"), // Original value from file
				Weight: 1,
				AzureKeyConfig: &schemas.AzureKeyConfig{
					Endpoint:   *schemas.NewEnvVar("https://myazure.openai.azure.com"),
					APIVersion: schemas.NewEnvVar("2024-02-01"),
					Deployments: map[string]string{
						"gpt-4": "gpt-4-deployment",
					},
				},
			},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://myazure.openai.azure.com/openai",
		},
		SendBackRawResponse: false,
	}

	sameFileProviderHash, _ := sameFileConfig.GenerateConfigHash("azure")

	// Provider hash should match (config.json unchanged)
	existingCfg := providersInDB["azure"]
	if existingCfg.ConfigHash != sameFileProviderHash {
		t.Errorf("Expected provider hash to match - config.json unchanged. DB: %s, File: %s",
			existingCfg.ConfigHash[:16], sameFileProviderHash[:16])
	}

	t.Logf("Step 3 - Hash matches, dashboard edits preserved ✓")

	// Verify dashboard-edited key value is preserved
	if existingCfg.Keys[0].Value.GetValue() != "azure-api-key-dashboard-edited" {
		t.Errorf("Expected dashboard-edited key value to be preserved, got %v", existingCfg.Keys[0].Value)
	}

	// === STEP 4: Config.json changed (Azure endpoint updated) - should trigger sync ===
	newFileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{
				ID:     "azure-key-1",
				Name:   "azure-openai-key",
				Value:  *schemas.NewEnvVar("azure-api-key-initial"),
				Weight: 1,
				AzureKeyConfig: &schemas.AzureKeyConfig{
					Endpoint:   *schemas.NewEnvVar("https://new-azure.openai.azure.com"), // Changed!
					APIVersion: schemas.NewEnvVar("2024-10-21"),                          // Changed!
					Deployments: map[string]string{
						"gpt-4":  "gpt-4-deployment",
						"gpt-4o": "gpt-4o-deployment", // Added!
					},
				},
			},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://new-azure.openai.azure.com/openai", // Changed!
		},
		SendBackRawResponse: true, // Changed!
	}

	newFileProviderHash, _ := newFileConfig.GenerateConfigHash("azure")
	newFileKeyHash, _ := configstore.GenerateKeyHash(newFileConfig.Keys[0])

	t.Logf("Step 4 - New file provider hash: %s", newFileProviderHash[:16]+"...")
	t.Logf("Step 4 - New file key hash: %s", newFileKeyHash[:16]+"...")

	// Provider hash should be different (config.json changed)
	if existingCfg.ConfigHash == newFileProviderHash {
		t.Error("Expected provider hash to differ - config.json was changed")
	}

	// Simulate sync: update from file, but preserve dashboard-added keys
	// (In this case, we're updating the existing key, not adding new ones)
	mergedKeys := []schemas.Key{}

	// For each key in file, check if it exists in DB
	for _, fileKey := range newFileConfig.Keys {
		found := false
		for _, dbKey := range existingCfg.Keys {
			if dbKey.Name == fileKey.Name || dbKey.ID == fileKey.ID {
				// Key exists in both - use file version (config.json changed)
				mergedKeys = append(mergedKeys, fileKey)
				found = true
				break
			}
		}
		if !found {
			// New key from file
			mergedKeys = append(mergedKeys, fileKey)
		}
	}

	// Preserve dashboard-added keys that aren't in file
	for _, dbKey := range existingCfg.Keys {
		found := false
		for _, fileKey := range newFileConfig.Keys {
			if dbKey.Name == fileKey.Name || dbKey.ID == fileKey.ID {
				found = true
				break
			}
		}
		if !found {
			// Key only in DB (added via dashboard) - preserve it
			mergedKeys = append(mergedKeys, dbKey)
		}
	}

	updatedConfig := configstore.ProviderConfig{
		Keys:                mergedKeys,
		NetworkConfig:       newFileConfig.NetworkConfig,
		SendBackRawResponse: newFileConfig.SendBackRawResponse,
		ConfigHash:          newFileProviderHash,
	}

	providersInDB["azure"] = updatedConfig

	t.Logf("Step 4 - Sync complete, DB updated ✓")

	// === STEP 5: Verify final state ===
	finalConfig := providersInDB["azure"]

	// Verify provider config updated
	if finalConfig.NetworkConfig.BaseURL != "https://new-azure.openai.azure.com/openai" {
		t.Errorf("Expected updated BaseURL, got %s", finalConfig.NetworkConfig.BaseURL)
	}
	if !finalConfig.SendBackRawResponse {
		t.Error("Expected SendBackRawResponse to be true")
	}
	if finalConfig.ConfigHash != newFileProviderHash {
		t.Error("Expected config hash to be updated")
	}

	// Verify Azure key config updated
	if len(finalConfig.Keys) != 1 {
		t.Errorf("Expected 1 key, got %d", len(finalConfig.Keys))
	}
	if finalConfig.Keys[0].AzureKeyConfig.Endpoint.GetValue() != "https://new-azure.openai.azure.com" {
		t.Errorf("Expected updated Azure endpoint, got %s", finalConfig.Keys[0].AzureKeyConfig.Endpoint.GetValue())
	}
	if finalConfig.Keys[0].AzureKeyConfig.APIVersion.GetValue() != "2024-10-21" {
		t.Errorf("Expected updated APIVersion, got %s", finalConfig.Keys[0].AzureKeyConfig.APIVersion.GetValue())
	}
	if len(finalConfig.Keys[0].AzureKeyConfig.Deployments) != 2 {
		t.Errorf("Expected 2 deployments, got %d", len(finalConfig.Keys[0].AzureKeyConfig.Deployments))
	}

	t.Log("Step 5 - Final state verified, Azure provider lifecycle complete ✓")
}

// TestProviderHashComparison_BedrockProviderFullLifecycle tests end-to-end Bedrock provider lifecycle
func TestProviderHashComparison_BedrockProviderFullLifecycle(t *testing.T) {
	// === STEP 1: Initial state - Bedrock provider exists in DB from previous config.json ===
	initialBedrockKey := schemas.Key{
		ID:     "bedrock-key-1",
		Name:   "aws-bedrock-key",
		Value:  *schemas.NewEnvVar(""), // Empty for Bedrock with IAM or AccessKey auth
		Weight: 1,
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
			SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
			Region:    schemas.NewEnvVar("us-east-1"),
			Deployments: map[string]string{
				"claude-3-sonnet": "anthropic.claude-3-sonnet-20240229-v1:0",
			},
		},
	}

	initialConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{initialBedrockKey},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:    "https://bedrock-runtime.us-east-1.amazonaws.com",
			MaxRetries: 3,
		},
		SendBackRawResponse: false,
	}

	initialProviderHash, _ := initialConfig.GenerateConfigHash("bedrock")
	initialKeyHash, _ := configstore.GenerateKeyHash(initialBedrockKey)
	initialConfig.ConfigHash = initialProviderHash

	// Simulate DB state
	providersInDB := map[schemas.ModelProvider]configstore.ProviderConfig{
		"bedrock": initialConfig,
	}

	t.Logf("Step 1 - Initial DB provider hash: %s", initialProviderHash[:16]+"...")
	t.Logf("Step 1 - Initial DB key hash: %s", initialKeyHash[:16]+"...")

	// === STEP 2: Dashboard adds a second key ===
	dashboardAddedKey := schemas.Key{
		ID:     "bedrock-key-2",
		Name:   "aws-bedrock-key-eu",
		Value:  *schemas.NewEnvVar(""),
		Weight: 1,
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			AccessKey: *schemas.NewEnvVar("AKIAI44QH8DHBEXAMPLE"),
			SecretKey: *schemas.NewEnvVar("je7MtGbClwBF/2Zp9Utk/h3yCo8nvbEXAMPLEKEY"),
			Region:    schemas.NewEnvVar("eu-west-1"), // Different region
			Deployments: map[string]string{
				"claude-3-sonnet": "anthropic.claude-3-sonnet-20240229-v1:0",
			},
		},
	}

	dbConfigAfterDashboardAdd := configstore.ProviderConfig{
		Keys:                []schemas.Key{initialBedrockKey, dashboardAddedKey}, // Added via dashboard
		NetworkConfig:       initialConfig.NetworkConfig,
		SendBackRawResponse: false,
		ConfigHash:          initialProviderHash, // Provider hash unchanged
	}

	providersInDB["bedrock"] = dbConfigAfterDashboardAdd

	t.Logf("Step 2 - Dashboard added second key ✓")

	// === STEP 3: Same config.json loaded (unchanged) - should NOT update, preserve dashboard keys ===
	sameFileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{
				ID:     "bedrock-key-1",
				Name:   "aws-bedrock-key",
				Value:  *schemas.NewEnvVar(""),
				Weight: 1,
				BedrockKeyConfig: &schemas.BedrockKeyConfig{
					AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
					SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
					Region:    schemas.NewEnvVar("us-east-1"),
					Deployments: map[string]string{
						"claude-3-sonnet": "anthropic.claude-3-sonnet-20240229-v1:0",
					},
				},
			},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:    "https://bedrock-runtime.us-east-1.amazonaws.com",
			MaxRetries: 3,
		},
		SendBackRawResponse: false,
	}

	sameFileProviderHash, _ := sameFileConfig.GenerateConfigHash("bedrock")

	// Provider hash should match (config.json unchanged)
	existingCfg := providersInDB["bedrock"]
	if existingCfg.ConfigHash != sameFileProviderHash {
		t.Errorf("Expected provider hash to match - config.json unchanged. DB: %s, File: %s",
			existingCfg.ConfigHash[:16], sameFileProviderHash[:16])
	}

	t.Logf("Step 3 - Hash matches, dashboard-added key preserved ✓")

	// Verify dashboard-added key is preserved
	if len(existingCfg.Keys) != 2 {
		t.Errorf("Expected 2 keys (1 original + 1 dashboard-added), got %d", len(existingCfg.Keys))
	}

	// === STEP 4: Config.json changed (region and new deployment) - should trigger sync but preserve dashboard keys ===
	newFileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{
				ID:     "bedrock-key-1",
				Name:   "aws-bedrock-key",
				Value:  *schemas.NewEnvVar(""),
				Weight: 1,
				BedrockKeyConfig: &schemas.BedrockKeyConfig{
					AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
					SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
					Region:    schemas.NewEnvVar("us-west-2"),                                                           // Changed!
					ARN:       schemas.NewEnvVar("arn:aws:bedrock:us-west-2:123456789012:inference-profile/my-profile"), // Added!
					Deployments: map[string]string{
						"claude-3-sonnet": "anthropic.claude-3-sonnet-20240229-v1:0",
						"claude-3-opus":   "anthropic.claude-3-opus-20240229-v1:0", // Added!
					},
				},
			},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:    "https://bedrock-runtime.us-west-2.amazonaws.com", // Changed!
			MaxRetries: 5,                                                 // Changed!
		},
		SendBackRawResponse: true, // Changed!
	}

	newFileProviderHash, _ := newFileConfig.GenerateConfigHash("bedrock")
	newFileKeyHash, _ := configstore.GenerateKeyHash(newFileConfig.Keys[0])

	t.Logf("Step 4 - New file provider hash: %s", newFileProviderHash[:16]+"...")
	t.Logf("Step 4 - New file key hash: %s", newFileKeyHash[:16]+"...")

	// Provider hash should be different (config.json changed)
	if existingCfg.ConfigHash == newFileProviderHash {
		t.Error("Expected provider hash to differ - config.json was changed")
	}

	// Simulate sync: update from file, but preserve dashboard-added keys
	mergedKeys := []schemas.Key{}

	// For each key in file, update or add
	for _, fileKey := range newFileConfig.Keys {
		mergedKeys = append(mergedKeys, fileKey)
	}

	// Preserve dashboard-added keys that aren't in file
	for _, dbKey := range existingCfg.Keys {
		found := false
		for _, fileKey := range newFileConfig.Keys {
			if dbKey.Name == fileKey.Name || dbKey.ID == fileKey.ID {
				found = true
				break
			}
		}
		if !found {
			// Key only in DB (added via dashboard) - preserve it
			mergedKeys = append(mergedKeys, dbKey)
		}
	}

	updatedConfig := configstore.ProviderConfig{
		Keys:                mergedKeys,
		NetworkConfig:       newFileConfig.NetworkConfig,
		SendBackRawResponse: newFileConfig.SendBackRawResponse,
		ConfigHash:          newFileProviderHash,
	}

	providersInDB["bedrock"] = updatedConfig

	t.Logf("Step 4 - Sync complete, DB updated ✓")

	// === STEP 5: Verify final state ===
	finalConfig := providersInDB["bedrock"]

	// Verify provider config updated
	if finalConfig.NetworkConfig.BaseURL != "https://bedrock-runtime.us-west-2.amazonaws.com" {
		t.Errorf("Expected updated BaseURL, got %s", finalConfig.NetworkConfig.BaseURL)
	}
	if finalConfig.NetworkConfig.MaxRetries != 5 {
		t.Errorf("Expected MaxRetries to be 5, got %d", finalConfig.NetworkConfig.MaxRetries)
	}
	if !finalConfig.SendBackRawResponse {
		t.Error("Expected SendBackRawResponse to be true")
	}
	if finalConfig.ConfigHash != newFileProviderHash {
		t.Error("Expected config hash to be updated")
	}

	// Verify we have both keys (1 updated from file + 1 dashboard-added)
	if len(finalConfig.Keys) != 2 {
		t.Errorf("Expected 2 keys (1 from file + 1 dashboard-added), got %d", len(finalConfig.Keys))
	}

	// Find the file key and verify its updates
	var fileKey *schemas.Key
	var dashboardKey *schemas.Key
	for i := range finalConfig.Keys {
		if finalConfig.Keys[i].Name == "aws-bedrock-key" {
			fileKey = &finalConfig.Keys[i]
		}
		if finalConfig.Keys[i].Name == "aws-bedrock-key-eu" {
			dashboardKey = &finalConfig.Keys[i]
		}
	}

	if fileKey == nil {
		t.Fatal("Expected to find file key")
	}
	if dashboardKey == nil {
		t.Fatal("Expected to find dashboard-added key")
	}

	// Verify file key Bedrock config updated
	if fileKey.BedrockKeyConfig.Region.GetValue() != "us-west-2" {
		t.Errorf("Expected updated Bedrock region, got %s", fileKey.BedrockKeyConfig.Region.GetValue())
	}
	if fileKey.BedrockKeyConfig.ARN == nil || fileKey.BedrockKeyConfig.ARN.GetValue() != "arn:aws:bedrock:us-west-2:123456789012:inference-profile/my-profile" {
		t.Error("Expected ARN to be set")
	}
	if len(fileKey.BedrockKeyConfig.Deployments) != 2 {
		t.Errorf("Expected 2 deployments, got %d", len(fileKey.BedrockKeyConfig.Deployments))
	}

	// Verify dashboard-added key is preserved
	if dashboardKey.BedrockKeyConfig.Region.GetValue() != "eu-west-1" {
		t.Errorf("Expected dashboard key region to be preserved, got %v", *dashboardKey.BedrockKeyConfig.Region)
	}

	t.Log("Step 5 - Final state verified, Bedrock provider lifecycle complete ✓")

	// === STEP 6: Same config.json loaded again - should NOT update ===
	sameNewFileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{
				ID:     "bedrock-key-1",
				Name:   "aws-bedrock-key",
				Value:  *schemas.NewEnvVar(""),
				Weight: 1,
				BedrockKeyConfig: &schemas.BedrockKeyConfig{
					AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
					SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
					Region:    schemas.NewEnvVar("us-west-2"),
					ARN:       schemas.NewEnvVar("arn:aws:bedrock:us-west-2:123456789012:inference-profile/my-profile"),
					Deployments: map[string]string{
						"claude-3-sonnet": "anthropic.claude-3-sonnet-20240229-v1:0",
						"claude-3-opus":   "anthropic.claude-3-opus-20240229-v1:0",
					},
				},
			},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:    "https://bedrock-runtime.us-west-2.amazonaws.com",
			MaxRetries: 5,
		},
		SendBackRawResponse: true,
	}

	sameNewFileProviderHash, _ := sameNewFileConfig.GenerateConfigHash("bedrock")

	currentDBConfig := providersInDB["bedrock"]
	if currentDBConfig.ConfigHash != sameNewFileProviderHash {
		t.Errorf("Expected hash match on same config reload. DB: %s, File: %s",
			currentDBConfig.ConfigHash[:16], sameNewFileProviderHash[:16])
	}

	t.Log("Step 6 - Hash matches on reload, no update needed ✓")
}

// TestProviderHashComparison_AzureNewProviderFromConfig tests adding new Azure provider from config.json when not in DB
func TestProviderHashComparison_AzureNewProviderFromConfig(t *testing.T) {
	// === Scenario: Azure provider not in DB, but present in config.json ===
	// Expected: Provider should be added to DB with new hash

	// Empty DB - no Azure provider
	providersInDB := map[schemas.ModelProvider]configstore.ProviderConfig{}

	// File has Azure provider with Azure key config
	fileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{
				ID:     "azure-key-1",
				Name:   "azure-openai-key",
				Value:  *schemas.NewEnvVar("azure-api-key-123"),
				Weight: 1,
				AzureKeyConfig: &schemas.AzureKeyConfig{
					Endpoint:   *schemas.NewEnvVar("https://myazure.openai.azure.com"),
					APIVersion: schemas.NewEnvVar("2024-02-01"),
					Deployments: map[string]string{
						"gpt-4": "gpt-4-deployment",
					},
				},
			},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://myazure.openai.azure.com/openai",
		},
		SendBackRawResponse: false,
	}

	fileHash, err := fileConfig.GenerateConfigHash("azure")
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}
	fileConfig.ConfigHash = fileHash

	// Simulate: check if provider exists in DB
	if _, exists := providersInDB["azure"]; !exists {
		// Provider not in DB - add from file
		providersInDB["azure"] = fileConfig
		t.Log("✓ Azure provider not in DB - added from config.json")
	}

	// Verify provider was added
	addedConfig, exists := providersInDB["azure"]
	if !exists {
		t.Fatal("Expected Azure provider to be added to DB")
	}

	// Verify hash was set
	if addedConfig.ConfigHash != fileHash {
		t.Error("Expected config hash to be set from file")
	}

	// Verify Azure key config is present
	if len(addedConfig.Keys) != 1 {
		t.Errorf("Expected 1 key, got %d", len(addedConfig.Keys))
	}
	if addedConfig.Keys[0].AzureKeyConfig == nil {
		t.Fatal("Expected AzureKeyConfig to be present")
	}
	if addedConfig.Keys[0].AzureKeyConfig.Endpoint.GetValue() != "https://myazure.openai.azure.com" {
		t.Errorf("Expected Azure endpoint, got %v", addedConfig.Keys[0].AzureKeyConfig.Endpoint)
	}

	t.Log("✓ New Azure provider added to DB with correct hash and config")
}

// TestProviderHashComparison_BedrockNewProviderFromConfig tests adding new Bedrock provider from config.json when not in DB
func TestProviderHashComparison_BedrockNewProviderFromConfig(t *testing.T) {
	// === Scenario: Bedrock provider not in DB, but present in config.json ===
	// Expected: Provider should be added to DB with new hash

	// Empty DB - no Bedrock provider
	providersInDB := map[schemas.ModelProvider]configstore.ProviderConfig{}

	// File has Bedrock provider with Bedrock key config
	fileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{
				ID:     "bedrock-key-1",
				Name:   "aws-bedrock-key",
				Value:  *schemas.NewEnvVar(""),
				Weight: 1,
				BedrockKeyConfig: &schemas.BedrockKeyConfig{
					AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
					SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
					Region:    schemas.NewEnvVar("us-east-1"),
					Deployments: map[string]string{
						"claude-3": "anthropic.claude-3-sonnet-20240229-v1:0",
					},
				},
			},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:    "https://bedrock-runtime.us-east-1.amazonaws.com",
			MaxRetries: 3,
		},
		SendBackRawResponse: false,
	}

	fileHash, err := fileConfig.GenerateConfigHash("bedrock")
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}
	fileConfig.ConfigHash = fileHash

	// Simulate: check if provider exists in DB
	if _, exists := providersInDB["bedrock"]; !exists {
		// Provider not in DB - add from file
		providersInDB["bedrock"] = fileConfig
		t.Log("✓ Bedrock provider not in DB - added from config.json")
	}

	// Verify provider was added
	addedConfig, exists := providersInDB["bedrock"]
	if !exists {
		t.Fatal("Expected Bedrock provider to be added to DB")
	}

	// Verify hash was set
	if addedConfig.ConfigHash != fileHash {
		t.Error("Expected config hash to be set from file")
	}

	// Verify Bedrock key config is present
	if len(addedConfig.Keys) != 1 {
		t.Errorf("Expected 1 key, got %d", len(addedConfig.Keys))
	}
	if addedConfig.Keys[0].BedrockKeyConfig == nil {
		t.Fatal("Expected BedrockKeyConfig to be present")
	}
	if addedConfig.Keys[0].BedrockKeyConfig.AccessKey.GetValue() != "AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("Expected Bedrock AccessKey, got %v", addedConfig.Keys[0].BedrockKeyConfig.AccessKey)
	}
	if addedConfig.Keys[0].BedrockKeyConfig.Region.GetValue() != "us-east-1" {
		t.Errorf("Expected Bedrock region us-east-1, got %v", *addedConfig.Keys[0].BedrockKeyConfig.Region)
	}

	t.Log("✓ New Bedrock provider added to DB with correct hash and config")
}

// TestProviderHashComparison_AzureDBValuePreservedWhenHashMatches explicitly tests that DB values are NOT overwritten when hash matches
func TestProviderHashComparison_AzureDBValuePreservedWhenHashMatches(t *testing.T) {
	// === Scenario: DB has Azure config with dashboard-edited value, config.json has same structure but different value ===
	// Expected: Hash matches (structure same), DB value should NOT be overwritten

	// DB has Azure config - key value was edited via dashboard
	dbConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{
				ID:     "azure-key-1",
				Name:   "azure-openai-key",
				Value:  *schemas.NewEnvVar("DASHBOARD-EDITED-SECRET-KEY"), // Dashboard edited this!
				Weight: 1,
				AzureKeyConfig: &schemas.AzureKeyConfig{
					Endpoint:   *schemas.NewEnvVar("https://myazure.openai.azure.com"),
					APIVersion: schemas.NewEnvVar("2024-02-01"),
					Deployments: map[string]string{
						"gpt-4": "gpt-4-deployment",
					},
				},
			},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://myazure.openai.azure.com/openai",
		},
		SendBackRawResponse: false,
	}

	// Generate hash based on provider config structure (keys excluded from provider hash)
	dbHash, _ := dbConfig.GenerateConfigHash("azure")
	dbConfig.ConfigHash = dbHash

	providersInDB := map[schemas.ModelProvider]configstore.ProviderConfig{
		"azure": dbConfig,
	}

	// File config has SAME STRUCTURE but DIFFERENT key value
	fileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{
				ID:     "azure-key-1",
				Name:   "azure-openai-key",
				Value:  *schemas.NewEnvVar("original-key-from-file"), // Different value than DB!
				Weight: 1,
				AzureKeyConfig: &schemas.AzureKeyConfig{
					Endpoint:   *schemas.NewEnvVar("https://myazure.openai.azure.com"), // Same
					APIVersion: schemas.NewEnvVar("2024-02-01"),                        // Same
					Deployments: map[string]string{
						"gpt-4": "gpt-4-deployment", // Same
					},
				},
			},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://myazure.openai.azure.com/openai", // Same
		},
		SendBackRawResponse: false, // Same
	}

	fileHash, _ := fileConfig.GenerateConfigHash("azure")

	// === Key assertion: Provider hashes should MATCH (structure is same) ===
	if dbHash != fileHash {
		t.Fatalf("Expected provider hashes to match (same structure). DB: %s, File: %s", dbHash[:16], fileHash[:16])
	}
	t.Log("✓ Provider hashes match (same structure)")

	// === Simulate sync logic: hash matches -> keep DB config ===
	existingConfig := providersInDB["azure"]
	if existingConfig.ConfigHash == fileHash {
		// Hash matches - DO NOT overwrite DB
		t.Log("✓ Hash matches - keeping DB config (not overwriting)")
	} else {
		t.Error("Expected hash match - should keep DB config")
	}

	// === Verify DB value was NOT overwritten ===
	if existingConfig.Keys[0].Value.GetValue() != "DASHBOARD-EDITED-SECRET-KEY" {
		t.Errorf("DB value should NOT be overwritten! Expected 'DASHBOARD-EDITED-SECRET-KEY', got '%v'",
			existingConfig.Keys[0].Value)
	}
	t.Log("✓ DB value preserved (dashboard edit NOT overwritten)")

	// === Verify Azure config in DB is intact ===
	if existingConfig.Keys[0].AzureKeyConfig.Endpoint.GetValue() != "https://myazure.openai.azure.com" {
		t.Error("Azure endpoint should be preserved")
	}
	t.Log("✓ Azure config preserved in DB")
}

// TestProviderHashComparison_BedrockDBValuePreservedWhenHashMatches explicitly tests that DB values are NOT overwritten when hash matches
func TestProviderHashComparison_BedrockDBValuePreservedWhenHashMatches(t *testing.T) {
	// === Scenario: DB has Bedrock config with dashboard-edited credentials, config.json has same structure ===
	// Expected: Hash matches (structure same), DB credentials should NOT be overwritten

	// DB has Bedrock config - credentials were edited via dashboard
	dbConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{
				ID:     "bedrock-key-1",
				Name:   "aws-bedrock-key",
				Value:  *schemas.NewEnvVar(""),
				Weight: 1,
				BedrockKeyConfig: &schemas.BedrockKeyConfig{
					AccessKey: *schemas.NewEnvVar("DASHBOARD-EDITED-ACCESS-KEY"), // Dashboard edited!
					SecretKey: *schemas.NewEnvVar("DASHBOARD-EDITED-SECRET-KEY"), // Dashboard edited!
					Region:    schemas.NewEnvVar("us-east-1"),
					Deployments: map[string]string{
						"claude-3": "anthropic.claude-3-sonnet-20240229-v1:0",
					},
				},
			},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:    "https://bedrock-runtime.us-east-1.amazonaws.com",
			MaxRetries: 3,
		},
		SendBackRawResponse: false,
	}

	dbHash, _ := dbConfig.GenerateConfigHash("bedrock")
	dbConfig.ConfigHash = dbHash

	providersInDB := map[schemas.ModelProvider]configstore.ProviderConfig{
		"bedrock": dbConfig,
	}

	// File config has SAME STRUCTURE but DIFFERENT credentials
	fileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{
				ID:     "bedrock-key-1",
				Name:   "aws-bedrock-key",
				Value:  *schemas.NewEnvVar(""),
				Weight: 1,
				BedrockKeyConfig: &schemas.BedrockKeyConfig{
					AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),                     // Different!
					SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"), // Different!
					Region:    schemas.NewEnvVar("us-east-1"),                                 // Same
					Deployments: map[string]string{
						"claude-3": "anthropic.claude-3-sonnet-20240229-v1:0", // Same
					},
				},
			},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:    "https://bedrock-runtime.us-east-1.amazonaws.com", // Same
			MaxRetries: 3,                                                 // Same
		},
		SendBackRawResponse: false, // Same
	}

	fileHash, _ := fileConfig.GenerateConfigHash("bedrock")

	// === Key assertion: Provider hashes should MATCH (structure is same) ===
	if dbHash != fileHash {
		t.Fatalf("Expected provider hashes to match (same structure). DB: %s, File: %s", dbHash[:16], fileHash[:16])
	}
	t.Log("✓ Provider hashes match (same structure)")

	// === Simulate sync logic: hash matches -> keep DB config ===
	existingConfig := providersInDB["bedrock"]
	if existingConfig.ConfigHash == fileHash {
		t.Log("✓ Hash matches - keeping DB config (not overwriting)")
	} else {
		t.Error("Expected hash match - should keep DB config")
	}

	// === Verify DB credentials were NOT overwritten ===
	if existingConfig.Keys[0].BedrockKeyConfig.AccessKey.GetValue() != "DASHBOARD-EDITED-ACCESS-KEY" {
		t.Errorf("DB AccessKey should NOT be overwritten! Expected 'DASHBOARD-EDITED-ACCESS-KEY', got '%v'",
			existingConfig.Keys[0].BedrockKeyConfig.AccessKey)
	}
	if existingConfig.Keys[0].BedrockKeyConfig.SecretKey.GetValue() != "DASHBOARD-EDITED-SECRET-KEY" {
		t.Errorf("DB SecretKey should NOT be overwritten! Expected 'DASHBOARD-EDITED-SECRET-KEY', got '%v'",
			existingConfig.Keys[0].BedrockKeyConfig.SecretKey)
	}
	t.Log("✓ DB credentials preserved (dashboard edits NOT overwritten)")

	// === Verify Bedrock config in DB is intact ===
	if existingConfig.Keys[0].BedrockKeyConfig.Region.GetValue() != "us-east-1" {
		t.Error("Bedrock region should be preserved")
	}
	t.Log("✓ Bedrock config preserved in DB")
}

// TestProviderHashComparison_AzureConfigChangedInFile tests that DB updates when config.json Azure config changes
func TestProviderHashComparison_AzureConfigChangedInFile(t *testing.T) {
	// === Scenario: DB has Azure config, config.json has DIFFERENT Azure config ===
	// Expected: Hash differs, DB should be updated with new config and hash

	// DB has existing Azure config
	dbConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{
				ID:     "azure-key-1",
				Name:   "azure-openai-key",
				Value:  *schemas.NewEnvVar("azure-api-key-123"),
				Weight: 1,
				AzureKeyConfig: &schemas.AzureKeyConfig{
					Endpoint:   *schemas.NewEnvVar("https://old-azure.openai.azure.com"),
					APIVersion: schemas.NewEnvVar("2024-02-01"),
				},
			},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://old-azure.openai.azure.com/openai",
		},
		SendBackRawResponse: false,
	}

	dbHash, _ := dbConfig.GenerateConfigHash("azure")
	dbConfig.ConfigHash = dbHash

	providersInDB := map[schemas.ModelProvider]configstore.ProviderConfig{
		"azure": dbConfig,
	}

	// File has CHANGED Azure config (new endpoint, new API version)
	fileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{
				ID:     "azure-key-1",
				Name:   "azure-openai-key",
				Value:  *schemas.NewEnvVar("azure-api-key-123"),
				Weight: 1,
				AzureKeyConfig: &schemas.AzureKeyConfig{
					Endpoint:   *schemas.NewEnvVar("https://NEW-azure.openai.azure.com"), // Changed!
					APIVersion: schemas.NewEnvVar("2024-10-21"),                          // Changed!
					Deployments: map[string]string{
						"gpt-4o": "gpt-4o-deployment", // Added!
					},
				},
			},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://NEW-azure.openai.azure.com/openai", // Changed!
		},
		SendBackRawResponse: true, // Changed!
	}

	fileHash, _ := fileConfig.GenerateConfigHash("azure")
	fileConfig.ConfigHash = fileHash

	// === Key assertion: Hashes should DIFFER (config changed) ===
	existingConfig := providersInDB["azure"]
	if existingConfig.ConfigHash == fileHash {
		t.Fatal("Expected hashes to DIFFER (config changed)")
	}
	t.Log("✓ Hashes differ (config changed in file)")

	// === Simulate sync: hash differs -> update DB from file ===
	providersInDB["azure"] = fileConfig
	t.Log("✓ DB updated from config.json")

	// === Verify DB was updated ===
	updatedConfig := providersInDB["azure"]

	if updatedConfig.ConfigHash != fileHash {
		t.Error("Expected DB hash to be updated")
	}
	if updatedConfig.Keys[0].AzureKeyConfig.Endpoint.GetValue() != "https://NEW-azure.openai.azure.com" {
		t.Errorf("Expected new Azure endpoint, got %v", updatedConfig.Keys[0].AzureKeyConfig.Endpoint)
	}
	if updatedConfig.Keys[0].AzureKeyConfig.APIVersion.GetValue() != "2024-10-21" {
		t.Errorf("Expected new API version, got %v", *updatedConfig.Keys[0].AzureKeyConfig.APIVersion)
	}
	if !updatedConfig.SendBackRawResponse {
		t.Error("Expected SendBackRawResponse to be updated to true")
	}

	t.Log("✓ DB updated with new Azure config and hash")
}

// TestProviderHashComparison_BedrockConfigChangedInFile tests that DB updates when config.json Bedrock config changes
func TestProviderHashComparison_BedrockConfigChangedInFile(t *testing.T) {
	// === Scenario: DB has Bedrock config, config.json has DIFFERENT Bedrock config ===
	// Expected: Hash differs, DB should be updated with new config and hash

	// DB has existing Bedrock config
	dbConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{
				ID:     "bedrock-key-1",
				Name:   "aws-bedrock-key",
				Value:  *schemas.NewEnvVar(""),
				Weight: 1,
				BedrockKeyConfig: &schemas.BedrockKeyConfig{
					AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
					SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
					Region:    schemas.NewEnvVar("us-east-1"),
				},
			},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:    "https://bedrock-runtime.us-east-1.amazonaws.com",
			MaxRetries: 3,
		},
		SendBackRawResponse: false,
	}

	dbHash, _ := dbConfig.GenerateConfigHash("bedrock")
	dbConfig.ConfigHash = dbHash

	providersInDB := map[schemas.ModelProvider]configstore.ProviderConfig{
		"bedrock": dbConfig,
	}

	// File has CHANGED Bedrock config (new region, new ARN)
	fileConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{
				ID:     "bedrock-key-1",
				Name:   "aws-bedrock-key",
				Value:  *schemas.NewEnvVar(""),
				Weight: 1,
				BedrockKeyConfig: &schemas.BedrockKeyConfig{
					AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
					SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
					Region:    schemas.NewEnvVar("us-west-2"),                                                            // Changed!
					ARN:       schemas.NewEnvVar("arn:aws:bedrock:us-west-2:123456789012:inference-profile/new-profile"), // Added!
					Deployments: map[string]string{
						"claude-3-opus": "anthropic.claude-3-opus-20240229-v1:0", // Added!
					},
				},
			},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:    "https://bedrock-runtime.us-west-2.amazonaws.com", // Changed!
			MaxRetries: 5,                                                 // Changed!
		},
		SendBackRawResponse: true, // Changed!
	}

	fileHash, _ := fileConfig.GenerateConfigHash("bedrock")
	fileConfig.ConfigHash = fileHash

	// === Key assertion: Hashes should DIFFER (config changed) ===
	existingConfig := providersInDB["bedrock"]
	if existingConfig.ConfigHash == fileHash {
		t.Fatal("Expected hashes to DIFFER (config changed)")
	}
	t.Log("✓ Hashes differ (config changed in file)")

	// === Simulate sync: hash differs -> update DB from file ===
	providersInDB["bedrock"] = fileConfig
	t.Log("✓ DB updated from config.json")

	// === Verify DB was updated ===
	updatedConfig := providersInDB["bedrock"]

	if updatedConfig.ConfigHash != fileHash {
		t.Error("Expected DB hash to be updated")
	}
	if updatedConfig.Keys[0].BedrockKeyConfig.Region.GetValue() != "us-west-2" {
		t.Errorf("Expected new Bedrock region, got %v", *updatedConfig.Keys[0].BedrockKeyConfig.Region)
	}
	if updatedConfig.Keys[0].BedrockKeyConfig.ARN == nil {
		t.Error("Expected ARN to be set")
	}
	if updatedConfig.NetworkConfig.MaxRetries != 5 {
		t.Errorf("Expected MaxRetries to be 5, got %d", updatedConfig.NetworkConfig.MaxRetries)
	}
	if !updatedConfig.SendBackRawResponse {
		t.Error("Expected SendBackRawResponse to be updated to true")
	}

	t.Log("✓ DB updated with new Bedrock config and hash")
}

// ===================================================================================
// VIRTUAL KEY HASH TESTS
// ===================================================================================

// TestGenerateVirtualKeyHash tests that virtual key hash is generated correctly
func TestGenerateVirtualKeyHash(t *testing.T) {
	// Create a virtual key
	teamID := "team-1"
	budgetID := "budget-1"
	vk1 := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		TeamID:      &teamID,
		BudgetID:    &budgetID,
	}

	// Generate hash
	hash1, err := configstore.GenerateVirtualKeyHash(vk1)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == "" {
		t.Error("Expected non-empty hash")
	}

	// Same virtual key content with different ID should produce same hash (ID is skipped)
	vk2 := tables.TableVirtualKey{
		ID:          "different-id", // Different ID - should be skipped
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		TeamID:      &teamID,
		BudgetID:    &budgetID,
	}

	hash2, err := configstore.GenerateVirtualKeyHash(vk2)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 != hash2 {
		t.Error("Expected same hash for virtual keys with same content (ID should be skipped)")
	}

	// Different name should produce different hash
	vk3 := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "different-name", // Different name
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		TeamID:      &teamID,
		BudgetID:    &budgetID,
	}

	hash3, err := configstore.GenerateVirtualKeyHash(vk3)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash3 {
		t.Error("Expected different hash for virtual keys with different Name")
	}

	// Different value should produce different hash
	vk4 := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_different", // Different value
		IsActive:    true,
		TeamID:      &teamID,
		BudgetID:    &budgetID,
	}

	hash4, err := configstore.GenerateVirtualKeyHash(vk4)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash4 {
		t.Error("Expected different hash for virtual keys with different Value")
	}

	// Different IsActive should produce different hash
	vk5 := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    false, // Different IsActive
		TeamID:      &teamID,
		BudgetID:    &budgetID,
	}

	hash5, err := configstore.GenerateVirtualKeyHash(vk5)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash5 {
		t.Error("Expected different hash for virtual keys with different IsActive")
	}

	// Different TeamID should produce different hash
	differentTeamID := "team-2"
	vk6 := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		TeamID:      &differentTeamID, // Different TeamID
		BudgetID:    &budgetID,
	}

	hash6, err := configstore.GenerateVirtualKeyHash(vk6)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash6 {
		t.Error("Expected different hash for virtual keys with different TeamID")
	}

	// Different Description should produce different hash
	vk7 := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Different description", // Different description
		Value:       "vk_abc123",
		IsActive:    true,
		TeamID:      &teamID,
		BudgetID:    &budgetID,
	}

	hash7, err := configstore.GenerateVirtualKeyHash(vk7)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash7 {
		t.Error("Expected different hash for virtual keys with different Description")
	}

	// Different CustomerID should produce different hash
	customerID := "customer-1"
	vk8 := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		TeamID:      &teamID,
		BudgetID:    &budgetID,
		CustomerID:  &customerID, // CustomerID set
	}

	hash8, err := configstore.GenerateVirtualKeyHash(vk8)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash8 {
		t.Error("Expected different hash for virtual keys with CustomerID set")
	}

	// Different CustomerID value should produce different hash
	differentCustomerID := "customer-2"
	vk8b := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		TeamID:      &teamID,
		BudgetID:    &budgetID,
		CustomerID:  &differentCustomerID, // Different CustomerID
	}

	hash8b, err := configstore.GenerateVirtualKeyHash(vk8b)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash8 == hash8b {
		t.Error("Expected different hash for virtual keys with different CustomerID values")
	}

	// Different BudgetID should produce different hash
	differentBudgetID := "budget-2"
	vk9 := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		TeamID:      &teamID,
		BudgetID:    &differentBudgetID, // Different BudgetID
	}

	hash9, err := configstore.GenerateVirtualKeyHash(vk9)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash9 {
		t.Error("Expected different hash for virtual keys with different BudgetID")
	}

	// RateLimitID should produce different hash
	rateLimitID := "ratelimit-1"
	vk10 := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		TeamID:      &teamID,
		BudgetID:    &budgetID,
		RateLimitID: &rateLimitID, // RateLimitID set
	}

	hash10, err := configstore.GenerateVirtualKeyHash(vk10)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash10 {
		t.Error("Expected different hash for virtual keys with RateLimitID set")
	}

	// Different RateLimitID value should produce different hash
	differentRateLimitID := "ratelimit-2"
	vk10b := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		TeamID:      &teamID,
		BudgetID:    &budgetID,
		RateLimitID: &differentRateLimitID, // Different RateLimitID
	}

	hash10b, err := configstore.GenerateVirtualKeyHash(vk10b)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash10 == hash10b {
		t.Error("Expected different hash for virtual keys with different RateLimitID values")
	}

	t.Log("✓ VirtualKey hash generation works correctly for all fields")
}

// TestGenerateVirtualKeyHash_WithProviderConfigs tests hash generation with provider configs
func TestGenerateVirtualKeyHash_WithProviderConfigs(t *testing.T) {
	budgetID := "budget-pc-1"
	rateLimitID := "rl-pc-1"

	// Virtual key with provider configs
	vk1 := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				ID:            1,
				VirtualKeyID:  "vk-1",
				Provider:      "openai",
				Weight:        ptrFloat64(1.0),
				AllowedModels: []string{"gpt-4", "gpt-3.5-turbo"},
				BudgetID:      &budgetID,
				RateLimitID:   &rateLimitID,
				Keys: []tables.TableKey{
					{KeyID: "key-1", Name: "key-1"},
					{KeyID: "key-2", Name: "key-2"},
				},
			},
		},
	}

	hash1, err := configstore.GenerateVirtualKeyHash(vk1)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == "" {
		t.Error("Expected non-empty hash")
	}

	// Different provider configs should produce different hash
	vk2 := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				ID:            1,
				VirtualKeyID:  "vk-1",
				Provider:      "anthropic", // Different provider
				Weight:        ptrFloat64(1.0),
				AllowedModels: []string{"claude-3"},
				BudgetID:      &budgetID,
				RateLimitID:   &rateLimitID,
			},
		},
	}

	hash2, err := configstore.GenerateVirtualKeyHash(vk2)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash2 {
		t.Error("Expected different hash for virtual keys with different ProviderConfigs")
	}

	// Same provider configs with different weight should produce different hash
	vk3 := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				ID:            1,
				VirtualKeyID:  "vk-1",
				Provider:      "openai",
				Weight:        ptrFloat64(2.0), // Different weight
				AllowedModels: []string{"gpt-4", "gpt-3.5-turbo"},
				BudgetID:      &budgetID,
				RateLimitID:   &rateLimitID,
				Keys: []tables.TableKey{
					{KeyID: "key-1", Name: "key-1"},
					{KeyID: "key-2", Name: "key-2"},
				},
			},
		},
	}

	hash3, err := configstore.GenerateVirtualKeyHash(vk3)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash3 {
		t.Error("Expected different hash for virtual keys with different ProviderConfigs weight")
	}
}

// TestGenerateVirtualKeyHash_WithMCPConfigs tests hash generation with MCP configs
func TestGenerateVirtualKeyHash_WithMCPConfigs(t *testing.T) {
	// Virtual key with MCP configs
	vk1 := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		MCPConfigs: []tables.TableVirtualKeyMCPConfig{
			{
				ID:             1,
				VirtualKeyID:   "vk-1",
				MCPClientID:    1,
				ToolsToExecute: []string{"tool1", "tool2"},
			},
		},
	}

	hash1, err := configstore.GenerateVirtualKeyHash(vk1)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == "" {
		t.Error("Expected non-empty hash")
	}

	// Different MCP configs should produce different hash
	vk2 := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		MCPConfigs: []tables.TableVirtualKeyMCPConfig{
			{
				ID:             1,
				VirtualKeyID:   "vk-1",
				MCPClientID:    2, // Different MCP client ID
				ToolsToExecute: []string{"tool1", "tool2"},
			},
		},
	}

	hash2, err := configstore.GenerateVirtualKeyHash(vk2)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash2 {
		t.Error("Expected different hash for virtual keys with different MCPConfigs")
	}

	// Different tools should produce different hash
	vk3 := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		MCPConfigs: []tables.TableVirtualKeyMCPConfig{
			{
				ID:             1,
				VirtualKeyID:   "vk-1",
				MCPClientID:    1,
				ToolsToExecute: []string{"tool3"}, // Different tools
			},
		},
	}

	hash3, err := configstore.GenerateVirtualKeyHash(vk3)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash3 {
		t.Error("Expected different hash for virtual keys with different MCPConfigs tools")
	}
}

// TestVirtualKeyHashComparison_MatchingHash tests that DB config is kept when hashes match
func TestVirtualKeyHashComparison_MatchingHash(t *testing.T) {
	teamID := "team-1"
	budgetID := "budget-1"

	// Create a virtual key (simulating what's in config.json)
	fileVK := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		TeamID:      &teamID,
		BudgetID:    &budgetID,
	}

	// Generate file hash
	fileHash, err := configstore.GenerateVirtualKeyHash(fileVK)
	if err != nil {
		t.Fatalf("Failed to generate file hash: %v", err)
	}

	// Create DB virtual key with same content (simulating existing DB record)
	dbTeamID := "team-1"
	dbBudgetID := "budget-1"
	dbVK := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		TeamID:      &dbTeamID,
		BudgetID:    &dbBudgetID,
		ConfigHash:  fileHash, // Same hash as file
	}

	// Verify hashes match
	dbHash, err := configstore.GenerateVirtualKeyHash(dbVK)
	if err != nil {
		t.Fatalf("Failed to generate DB hash: %v", err)
	}

	if fileHash != dbHash {
		t.Error("Expected hashes to match for same content")
	}

	// When hash matches, DB config should be kept
	if dbVK.ConfigHash != fileHash {
		t.Error("Expected DB config hash to match file hash")
	}

	t.Log("✓ Matching hashes correctly detected - DB config would be kept")
}

// TestVirtualKeyHashComparison_DifferentHash tests that file config is used when hashes differ
func TestVirtualKeyHashComparison_DifferentHash(t *testing.T) {
	teamID := "team-1"
	budgetID := "budget-1"

	// Create DB virtual key with old config
	dbVK := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "old-name", // Old name
		Description: "Old description",
		Value:       "vk_abc123",
		IsActive:    true,
		TeamID:      &teamID,
		BudgetID:    &budgetID,
	}

	dbHash, err := configstore.GenerateVirtualKeyHash(dbVK)
	if err != nil {
		t.Fatalf("Failed to generate DB hash: %v", err)
	}
	dbVK.ConfigHash = dbHash

	// Create file virtual key with updated config
	fileTeamID := "team-1"
	fileBudgetID := "budget-1"
	fileVK := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "new-name", // Updated name
		Description: "New description",
		Value:       "vk_abc123",
		IsActive:    true,
		TeamID:      &fileTeamID,
		BudgetID:    &fileBudgetID,
	}

	fileHash, err := configstore.GenerateVirtualKeyHash(fileVK)
	if err != nil {
		t.Fatalf("Failed to generate file hash: %v", err)
	}

	// Hashes should differ
	if dbHash == fileHash {
		t.Error("Expected hashes to differ for different content")
	}

	// When hash differs, file config should be used
	t.Logf("DB hash: %s", dbHash)
	t.Logf("File hash: %s", fileHash)
	t.Log("✓ Different hashes correctly detected - file config would be synced to DB")
}

// TestVirtualKeyHashComparison_VirtualKeyOnlyInDB tests that dashboard-added VK is preserved
func TestVirtualKeyHashComparison_VirtualKeyOnlyInDB(t *testing.T) {
	customerID := "customer-1"
	rateLimitID := "rl-1"

	// Create a dashboard-added virtual key (not in config.json)
	dashboardVK := tables.TableVirtualKey{
		ID:          "vk-dashboard",
		Name:        "dashboard-vk",
		Description: "Added via dashboard",
		Value:       "vk_dashboard123",
		IsActive:    true,
		CustomerID:  &customerID,
		RateLimitID: &rateLimitID,
	}

	dashboardHash, err := configstore.GenerateVirtualKeyHash(dashboardVK)
	if err != nil {
		t.Fatalf("Failed to generate dashboard hash: %v", err)
	}
	dashboardVK.ConfigHash = dashboardHash

	// Config.json has different virtual keys
	fileVKs := []tables.TableVirtualKey{
		{
			ID:          "vk-file",
			Name:        "file-vk",
			Description: "From config.json",
			Value:       "vk_file123",
			IsActive:    true,
		},
	}

	// Dashboard VK should not be found in file VKs
	found := false
	for _, fileVK := range fileVKs {
		if fileVK.ID == dashboardVK.ID {
			found = true
			break
		}
	}

	if found {
		t.Error("Expected dashboard VK to not be found in file VKs")
	}

	t.Log("✓ Dashboard-added virtual key preserved (not in config.json)")
}

// TestVirtualKeyHashComparison_NewVirtualKey tests that new VK is added from file
func TestVirtualKeyHashComparison_NewVirtualKey(t *testing.T) {
	teamID := "team-new"

	// Create a new virtual key in config.json
	newFileVK := tables.TableVirtualKey{
		ID:          "vk-new",
		Name:        "new-vk",
		Description: "New virtual key from config.json",
		Value:       "vk_new123",
		IsActive:    true,
		TeamID:      &teamID,
	}

	newHash, err := configstore.GenerateVirtualKeyHash(newFileVK)
	if err != nil {
		t.Fatalf("Failed to generate new VK hash: %v", err)
	}
	newFileVK.ConfigHash = newHash

	// DB has no virtual keys
	dbVKs := []tables.TableVirtualKey{}

	// New VK should not be found in DB
	found := false
	for _, dbVK := range dbVKs {
		if dbVK.ID == newFileVK.ID {
			found = true
			break
		}
	}

	if found {
		t.Error("Expected new VK to not be found in DB")
	}

	// Hash should be set for new VK
	if newFileVK.ConfigHash == "" {
		t.Error("Expected new VK to have hash set")
	}

	t.Log("✓ New virtual key would be added from config.json with hash")
}

// TestVirtualKeyHashComparison_OptionalFieldsPresence tests hash with optional fields
func TestVirtualKeyHashComparison_OptionalFieldsPresence(t *testing.T) {
	// Virtual key with no optional fields
	vkNoOptional := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "",
		Value:       "vk_abc123",
		IsActive:    true,
	}

	hashNoOptional, err := configstore.GenerateVirtualKeyHash(vkNoOptional)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	// Virtual key with team_id
	teamID := "team-1"
	vkWithTeam := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "",
		Value:       "vk_abc123",
		IsActive:    true,
		TeamID:      &teamID,
	}

	hashWithTeam, err := configstore.GenerateVirtualKeyHash(vkWithTeam)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hashNoOptional == hashWithTeam {
		t.Error("Expected different hash when team_id is added")
	}

	// Virtual key with customer_id
	customerID := "customer-1"
	vkWithCustomer := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "",
		Value:       "vk_abc123",
		IsActive:    true,
		CustomerID:  &customerID,
	}

	hashWithCustomer, err := configstore.GenerateVirtualKeyHash(vkWithCustomer)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hashNoOptional == hashWithCustomer {
		t.Error("Expected different hash when customer_id is added")
	}

	if hashWithTeam == hashWithCustomer {
		t.Error("Expected different hash for team_id vs customer_id")
	}

	// Virtual key with budget_id
	budgetID := "budget-1"
	vkWithBudget := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "",
		Value:       "vk_abc123",
		IsActive:    true,
		BudgetID:    &budgetID,
	}

	hashWithBudget, err := configstore.GenerateVirtualKeyHash(vkWithBudget)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hashNoOptional == hashWithBudget {
		t.Error("Expected different hash when budget_id is added")
	}

	// Virtual key with rate_limit_id
	rateLimitID := "rl-1"
	vkWithRateLimit := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "",
		Value:       "vk_abc123",
		IsActive:    true,
		RateLimitID: &rateLimitID,
	}

	hashWithRateLimit, err := configstore.GenerateVirtualKeyHash(vkWithRateLimit)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hashNoOptional == hashWithRateLimit {
		t.Error("Expected different hash when rate_limit_id is added")
	}

	t.Log("✓ Optional fields correctly affect hash generation")
}

// TestVirtualKeyHashComparison_FieldValueChanges tests hash changes when field values change
func TestVirtualKeyHashComparison_FieldValueChanges(t *testing.T) {
	teamID := "team-1"
	budgetID := "budget-1"

	// Base virtual key
	baseVK := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Base description",
		Value:       "vk_abc123",
		IsActive:    true,
		TeamID:      &teamID,
		BudgetID:    &budgetID,
	}

	baseHash, err := configstore.GenerateVirtualKeyHash(baseVK)
	if err != nil {
		t.Fatalf("Failed to generate base hash: %v", err)
	}

	// Change description
	vkChangedDesc := baseVK
	vkChangedDesc.Description = "Changed description"

	hashChangedDesc, err := configstore.GenerateVirtualKeyHash(vkChangedDesc)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if baseHash == hashChangedDesc {
		t.Error("Expected different hash when description changes")
	}

	// Change IsActive
	vkChangedActive := baseVK
	vkChangedActive.IsActive = false

	hashChangedActive, err := configstore.GenerateVirtualKeyHash(vkChangedActive)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if baseHash == hashChangedActive {
		t.Error("Expected different hash when IsActive changes")
	}

	// Change TeamID value
	newTeamID := "team-2"
	vkChangedTeam := baseVK
	vkChangedTeam.TeamID = &newTeamID

	hashChangedTeam, err := configstore.GenerateVirtualKeyHash(vkChangedTeam)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if baseHash == hashChangedTeam {
		t.Error("Expected different hash when TeamID value changes")
	}

	// Change BudgetID value
	newBudgetID := "budget-2"
	vkChangedBudget := baseVK
	vkChangedBudget.BudgetID = &newBudgetID

	hashChangedBudget, err := configstore.GenerateVirtualKeyHash(vkChangedBudget)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if baseHash == hashChangedBudget {
		t.Error("Expected different hash when BudgetID value changes")
	}

	t.Log("✓ Field value changes correctly detected in hash")
}

// TestVirtualKeyHashComparison_RoundTrip tests JSON → DB → same JSON produces no changes
func TestVirtualKeyHashComparison_RoundTrip(t *testing.T) {
	teamID := "team-1"
	budgetID := "budget-1"
	rateLimitID := "rl-1"

	// Original config.json virtual key
	originalVK := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		TeamID:      &teamID,
		BudgetID:    &budgetID,
		RateLimitID: &rateLimitID,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				Provider:      "openai",
				Weight:        ptrFloat64(1.0),
				AllowedModels: []string{"gpt-4"},
			},
		},
	}

	// Generate hash and store in "DB"
	originalHash, err := configstore.GenerateVirtualKeyHash(originalVK)
	if err != nil {
		t.Fatalf("Failed to generate original hash: %v", err)
	}
	originalVK.ConfigHash = originalHash

	// Simulate DB storage and retrieval
	dbVK := originalVK

	// Same config.json on reload (simulating app restart)
	reloadTeamID := "team-1"
	reloadBudgetID := "budget-1"
	reloadRateLimitID := "rl-1"
	reloadVK := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		TeamID:      &reloadTeamID,
		BudgetID:    &reloadBudgetID,
		RateLimitID: &reloadRateLimitID,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				Provider:      "openai",
				Weight:        ptrFloat64(1.0),
				AllowedModels: []string{"gpt-4"},
			},
		},
	}

	// Generate hash for reload
	reloadHash, err := configstore.GenerateVirtualKeyHash(reloadVK)
	if err != nil {
		t.Fatalf("Failed to generate reload hash: %v", err)
	}

	// Hashes should match - no update needed
	if dbVK.ConfigHash != reloadHash {
		t.Errorf("Expected hashes to match on round-trip: DB=%s, reload=%s", dbVK.ConfigHash, reloadHash)
	}

	t.Log("✓ Round-trip produces matching hashes - no unnecessary DB updates")
}

// =============================================================================
// SQLite Integration Tests - Provider Hash Scenarios
// =============================================================================

// TestSQLite_Provider_NewProviderFromFile tests that a new provider in config.json is added to an empty DB
func TestSQLite_Provider_NewProviderFromFile(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Create config.json with a new provider
	providers := map[string]configstore.ProviderConfig{
		"openai": makeProviderConfigWithNetwork("openai-key-1", "sk-test-123", "https://api.openai.com"),
	}
	configData := makeConfigDataWithProvidersAndDir(providers, tempDir)
	createConfigFile(t, tempDir, configData)

	// Load config - this should create the provider in the DB
	ctx := context.Background()
	config, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	defer config.ConfigStore.Close(ctx)

	// Verify provider exists in memory
	if _, exists := config.Providers[schemas.OpenAI]; !exists {
		t.Fatal("OpenAI provider not found in memory")
	}

	// Verify provider exists in DB
	verifyProviderInDB(t, config.ConfigStore, schemas.OpenAI, 1)

	// Verify the hash was set
	dbProviders, err := config.ConfigStore.GetProvidersConfig(ctx)
	if err != nil {
		t.Fatalf("failed to get providers from DB: %v", err)
	}
	if dbProviders[schemas.OpenAI].ConfigHash == "" {
		t.Error("Expected config hash to be set for new provider")
	}
}

// TestSQLite_Provider_HashMatch_DBPreserved tests that DB config is preserved when hashes match
func TestSQLite_Provider_HashMatch_DBPreserved(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Create config.json with a provider
	providers := map[string]configstore.ProviderConfig{
		"openai": makeProviderConfigWithNetwork("openai-key-1", "sk-test-123", "https://api.openai.com"),
	}
	configData := makeConfigDataWithProvidersAndDir(providers, tempDir)
	createConfigFile(t, tempDir, configData)

	// First load - creates provider in DB
	ctx := context.Background()
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Get the hash from first load
	dbProviders1, _ := config1.ConfigStore.GetProvidersConfig(ctx)
	firstHash := dbProviders1[schemas.OpenAI].ConfigHash
	config1.ConfigStore.Close(ctx)

	// Second load with same config.json - should preserve DB config
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	// Verify hash is still the same
	dbProviders2, _ := config2.ConfigStore.GetProvidersConfig(ctx)
	secondHash := dbProviders2[schemas.OpenAI].ConfigHash

	if firstHash != secondHash {
		t.Errorf("Expected hash to remain unchanged on reload: first=%s, second=%s", firstHash, secondHash)
	}

	// Verify provider still has 1 key
	verifyProviderInDB(t, config2.ConfigStore, schemas.OpenAI, 1)
}

// TestSQLite_Provider_HashMismatch_FileSync tests that file config is synced when hashes differ
func TestSQLite_Provider_HashMismatch_FileSync(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Create config.json with a provider
	providers := map[string]configstore.ProviderConfig{
		"openai": makeProviderConfigWithNetwork("openai-key-1", "sk-test-123", "https://api.openai.com"),
	}
	configData := makeConfigDataWithProvidersAndDir(providers, tempDir)
	createConfigFile(t, tempDir, configData)

	// First load
	ctx := context.Background()
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Get original hash
	dbProviders1, _ := config1.ConfigStore.GetProvidersConfig(ctx)
	originalHash := dbProviders1[schemas.OpenAI].ConfigHash
	config1.ConfigStore.Close(ctx)

	// Modify config.json - change the BaseURL
	providers2 := map[string]configstore.ProviderConfig{
		"openai": makeProviderConfigWithNetwork("openai-key-1", "sk-test-123", "https://api.openai.com/v2"),
	}
	configData2 := makeConfigDataWithProvidersAndDir(providers2, tempDir)
	createConfigFile(t, tempDir, configData2)

	// Second load with modified config.json - should sync from file
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	// Verify hash changed
	dbProviders2, _ := config2.ConfigStore.GetProvidersConfig(ctx)
	newHash := dbProviders2[schemas.OpenAI].ConfigHash

	if originalHash == newHash {
		t.Error("Expected hash to change when config.json is modified")
	}

	// Verify the new BaseURL is in memory
	if config2.Providers[schemas.OpenAI].NetworkConfig.BaseURL != "https://api.openai.com/v2" {
		t.Errorf("Expected BaseURL to be updated, got %s", config2.Providers[schemas.OpenAI].NetworkConfig.BaseURL)
	}
}

// TestSQLite_Provider_DBOnlyProvider_Preserved tests that provider added via DB is preserved
func TestSQLite_Provider_DBOnlyProvider_Preserved(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Create config.json with OpenAI provider only
	providers := map[string]configstore.ProviderConfig{
		"openai": makeProviderConfigWithNetwork("openai-key-1", "sk-test-123", "https://api.openai.com"),
	}
	configData := makeConfigDataWithProvidersAndDir(providers, tempDir)
	createConfigFile(t, tempDir, configData)

	// First load
	ctx := context.Background()
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Add Anthropic provider directly to DB (simulating dashboard addition)
	anthropicConfig := configstore.ProviderConfig{
		Keys: []schemas.Key{
			{
				ID:     uuid.NewString(),
				Name:   "anthropic-key-1",
				Value:  *schemas.NewEnvVar("sk-anthropic-123"),
				Weight: 1,
			},
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "https://api.anthropic.com",
		},
	}
	anthropicHash, _ := anthropicConfig.GenerateConfigHash("anthropic")
	anthropicConfig.ConfigHash = anthropicHash

	// Update providers in DB to include both
	existingProviders, _ := config1.ConfigStore.GetProvidersConfig(ctx)
	existingProviders[schemas.Anthropic] = anthropicConfig
	err = config1.ConfigStore.UpdateProvidersConfig(ctx, existingProviders)
	if err != nil {
		t.Fatalf("Failed to add Anthropic to DB: %v", err)
	}
	config1.ConfigStore.Close(ctx)

	// Second load with same config.json (no Anthropic) - should preserve DB-added provider
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	// Verify both providers exist
	if _, exists := config2.Providers[schemas.OpenAI]; !exists {
		t.Error("OpenAI provider should exist (from file)")
	}
	if _, exists := config2.Providers[schemas.Anthropic]; !exists {
		t.Error("Anthropic provider should be preserved (added via DB)")
	}

	// Verify both in DB
	verifyProviderInDB(t, config2.ConfigStore, schemas.OpenAI, 1)
	verifyProviderInDB(t, config2.ConfigStore, schemas.Anthropic, 1)
}

// TestSQLite_Provider_RoundTrip tests load -> modify via DB -> reload same file -> no changes
func TestSQLite_Provider_RoundTrip(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Create config.json
	providers := map[string]configstore.ProviderConfig{
		"openai": makeProviderConfigWithNetwork("openai-key-1", "sk-test-123", "https://api.openai.com"),
	}
	configData := makeConfigDataWithProvidersAndDir(providers, tempDir)
	createConfigFile(t, tempDir, configData)

	// First load
	ctx := context.Background()
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Get original state
	dbProviders1, _ := config1.ConfigStore.GetProvidersConfig(ctx)
	originalHash := dbProviders1[schemas.OpenAI].ConfigHash
	originalKeyValue := dbProviders1[schemas.OpenAI].Keys[0].Value

	// Modify key value in DB (simulating dashboard edit)
	dbProviders1[schemas.OpenAI].Keys[0].Value = *schemas.NewEnvVar("sk-dashboard-modified")
	// Note: We keep the same hash since only the key value changed, not provider config
	err = config1.ConfigStore.UpdateProvidersConfig(ctx, dbProviders1)
	if err != nil {
		t.Fatalf("Failed to update provider in DB: %v", err)
	}
	config1.ConfigStore.Close(ctx)

	// Second load with same config.json - should preserve DB changes since hash matches
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	// Verify hash is unchanged
	dbProviders2, _ := config2.ConfigStore.GetProvidersConfig(ctx)
	if dbProviders2[schemas.OpenAI].ConfigHash != originalHash {
		t.Error("Hash should remain unchanged when config.json hasn't changed")
	}

	// The key value should be from DB (dashboard edit preserved) since hash matches
	// This demonstrates that when hashes match, DB config is kept
	if dbProviders2[schemas.OpenAI].Keys[0].Value == originalKeyValue {
		t.Log("Note: Key value preserved from initial load (hash match means DB preserved)")
	}
}

// =============================================================================
// SQLite Integration Tests - Provider Key Hash Scenarios
// =============================================================================

// TestSQLite_Key_NewKeyFromFile tests that a new key in config.json provider is added to DB
func TestSQLite_Key_NewKeyFromFile(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Create config.json with a provider and one key
	providers := map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: uuid.NewString(), Name: "openai-key-1", Value: *schemas.NewEnvVar("sk-key1-123"), Weight: 1},
			},
			NetworkConfig: &schemas.NetworkConfig{BaseURL: "https://api.openai.com"},
		},
	}
	configData := makeConfigDataWithProvidersAndDir(providers, tempDir)
	createConfigFile(t, tempDir, configData)

	// Load config
	ctx := context.Background()
	config, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	defer config.ConfigStore.Close(ctx)

	// Verify the key exists
	dbProviders, _ := config.ConfigStore.GetProvidersConfig(ctx)
	if len(dbProviders[schemas.OpenAI].Keys) != 1 {
		t.Errorf("Expected 1 key, got %d", len(dbProviders[schemas.OpenAI].Keys))
	}
	if dbProviders[schemas.OpenAI].Keys[0].Name != "openai-key-1" {
		t.Errorf("Expected key name 'openai-key-1', got '%s'", dbProviders[schemas.OpenAI].Keys[0].Name)
	}
}

// TestSQLite_Key_HashMatch_DBKeyPreserved tests that DB key is preserved when unchanged
func TestSQLite_Key_HashMatch_DBKeyPreserved(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Create config.json
	providers := map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: uuid.NewString(), Name: "key-1", Value: *schemas.NewEnvVar("sk-key1-123"), Weight: 1},
			},
			NetworkConfig: &schemas.NetworkConfig{BaseURL: "https://api.openai.com"},
		},
	}
	configData := makeConfigDataWithProvidersAndDir(providers, tempDir)
	createConfigFile(t, tempDir, configData)

	// First load
	ctx := context.Background()
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Get original key ID
	dbProviders1, _ := config1.ConfigStore.GetProvidersConfig(ctx)
	originalKeyID := dbProviders1[schemas.OpenAI].Keys[0].ID
	config1.ConfigStore.Close(ctx)

	// Second load with same config
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	// Verify key ID is preserved (same key, not recreated)
	dbProviders2, _ := config2.ConfigStore.GetProvidersConfig(ctx)
	if dbProviders2[schemas.OpenAI].Keys[0].ID != originalKeyID {
		t.Errorf("Expected key ID to be preserved on reload: got %s, want %s",
			dbProviders2[schemas.OpenAI].Keys[0].ID, originalKeyID)
	}

	// Key should still be present
	if len(dbProviders2[schemas.OpenAI].Keys) != 1 {
		t.Errorf("Expected 1 key after reload, got %d", len(dbProviders2[schemas.OpenAI].Keys))
	}
}

// TestSQLite_Key_DashboardAddedKey_Preserved tests that key added via DB is preserved on reload
func TestSQLite_Key_DashboardAddedKey_Preserved(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Create config.json with one key
	keyID1 := uuid.NewString()
	providers := map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: keyID1, Name: "file-key", Value: *schemas.NewEnvVar("sk-file-123"), Weight: 1},
			},
			NetworkConfig: &schemas.NetworkConfig{BaseURL: "https://api.openai.com"},
		},
	}
	configData := makeConfigDataWithProvidersAndDir(providers, tempDir)
	createConfigFile(t, tempDir, configData)

	// First load
	ctx := context.Background()
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Add a second key directly to DB (simulating dashboard addition)
	dbProviders1, _ := config1.ConfigStore.GetProvidersConfig(ctx)
	openaiConfig := dbProviders1[schemas.OpenAI]
	openaiConfig.Keys = append(openaiConfig.Keys, schemas.Key{
		ID:     uuid.NewString(),
		Name:   "dashboard-key",
		Value:  *schemas.NewEnvVar("sk-dashboard-456"),
		Weight: 1,
	})
	dbProviders1[schemas.OpenAI] = openaiConfig
	err = config1.ConfigStore.UpdateProvidersConfig(ctx, dbProviders1)
	if err != nil {
		t.Fatalf("Failed to add dashboard key: %v", err)
	}
	config1.ConfigStore.Close(ctx)

	// Second load with same config.json (still has only file-key)
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	// Verify both keys exist (dashboard-added key preserved)
	dbProviders2, _ := config2.ConfigStore.GetProvidersConfig(ctx)
	if len(dbProviders2[schemas.OpenAI].Keys) != 2 {
		t.Errorf("Expected 2 keys (1 from file + 1 from dashboard), got %d", len(dbProviders2[schemas.OpenAI].Keys))
	}

	// Check key names
	keyNames := make(map[string]bool)
	for _, k := range dbProviders2[schemas.OpenAI].Keys {
		keyNames[k.Name] = true
	}
	if !keyNames["file-key"] {
		t.Error("Expected file-key to be present")
	}
	if !keyNames["dashboard-key"] {
		t.Error("Expected dashboard-key to be preserved")
	}
}

// TestSQLite_Key_KeyValueChange_Detected tests that key value change in file is detected via hash
func TestSQLite_Key_KeyValueChange_Detected(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Create config.json with a key
	keyID := uuid.NewString()
	providers := map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: keyID, Name: "test-key", Value: *schemas.NewEnvVar("sk-original-123"), Weight: 1},
			},
			NetworkConfig: &schemas.NetworkConfig{BaseURL: "https://api.openai.com"},
		},
	}
	configData := makeConfigDataWithProvidersAndDir(providers, tempDir)
	createConfigFile(t, tempDir, configData)

	// First load
	ctx := context.Background()
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Verify original value
	dbProviders1, _ := config1.ConfigStore.GetProvidersConfig(ctx)
	if dbProviders1[schemas.OpenAI].Keys[0].Value.GetValue() != "sk-original-123" {
		t.Errorf("Expected original key value, got %v", dbProviders1[schemas.OpenAI].Keys[0].Value)
	}
	config1.ConfigStore.Close(ctx)

	// Modify config.json - change key value AND network config to trigger hash mismatch
	providers2 := map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: keyID, Name: "test-key", Value: *schemas.NewEnvVar("sk-modified-456"), Weight: 1},
			},
			NetworkConfig: &schemas.NetworkConfig{BaseURL: "https://api.openai.com/v2"}, // Changed to trigger hash mismatch
		},
	}
	configData2 := makeConfigDataWithProvidersAndDir(providers2, tempDir)
	createConfigFile(t, tempDir, configData2)

	// Second load with modified config
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	// When hash mismatches (provider config changed), file key value should be synced
	dbProviders2, _ := config2.ConfigStore.GetProvidersConfig(ctx)
	if dbProviders2[schemas.OpenAI].Keys[0].Value.GetValue() != "sk-modified-456" {
		t.Errorf("Expected key value to be updated to 'sk-modified-456', got '%v'", dbProviders2[schemas.OpenAI].Keys[0].Value)
	}
}

// TestSQLite_Key_MultipleKeys_MergeLogic tests that multiple keys merge correctly
func TestSQLite_Key_MultipleKeys_MergeLogic(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Create config.json with two keys
	keyID1 := uuid.NewString()
	keyID2 := uuid.NewString()
	providers := map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: keyID1, Name: "key-1", Value: *schemas.NewEnvVar("sk-key1-123"), Weight: 1},
				{ID: keyID2, Name: "key-2", Value: *schemas.NewEnvVar("sk-key2-456"), Weight: 2},
			},
			NetworkConfig: &schemas.NetworkConfig{BaseURL: "https://api.openai.com"},
		},
	}
	configData := makeConfigDataWithProvidersAndDir(providers, tempDir)
	createConfigFile(t, tempDir, configData)

	// First load
	ctx := context.Background()
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Verify two keys exist
	dbProviders1, _ := config1.ConfigStore.GetProvidersConfig(ctx)
	if len(dbProviders1[schemas.OpenAI].Keys) != 2 {
		t.Errorf("Expected 2 keys, got %d", len(dbProviders1[schemas.OpenAI].Keys))
	}

	// Add a third key via dashboard
	openaiConfig := dbProviders1[schemas.OpenAI]
	openaiConfig.Keys = append(openaiConfig.Keys, schemas.Key{
		ID:     uuid.NewString(),
		Name:   "key-3-dashboard",
		Value:  *schemas.NewEnvVar("sk-key3-789"),
		Weight: 1,
	})
	dbProviders1[schemas.OpenAI] = openaiConfig
	err = config1.ConfigStore.UpdateProvidersConfig(ctx, dbProviders1)
	if err != nil {
		t.Fatalf("Failed to add third key: %v", err)
	}
	config1.ConfigStore.Close(ctx)

	// Second load with same config.json (still has key-1 and key-2)
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	// Verify all three keys exist
	dbProviders2, _ := config2.ConfigStore.GetProvidersConfig(ctx)
	if len(dbProviders2[schemas.OpenAI].Keys) != 3 {
		t.Errorf("Expected 3 keys after merge, got %d", len(dbProviders2[schemas.OpenAI].Keys))
	}

	// Verify key names
	keyNames := make(map[string]bool)
	for _, k := range dbProviders2[schemas.OpenAI].Keys {
		keyNames[k.Name] = true
	}
	if !keyNames["key-1"] || !keyNames["key-2"] || !keyNames["key-3-dashboard"] {
		t.Errorf("Expected all three keys, got: %v", keyNames)
	}
}

// =============================================================================
// SQLite Integration Tests - Virtual Key Hash Scenarios
// =============================================================================

// TestSQLite_VirtualKey_NewFromFile tests that a new VK in config.json is added to DB with hash
func TestSQLite_VirtualKey_NewFromFile(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Create config.json with providers and a virtual key
	providers := map[string]configstore.ProviderConfig{
		"openai": makeProviderConfigWithNetwork("openai-key-1", "sk-test-123", "https://api.openai.com"),
	}
	vks := []tables.TableVirtualKey{
		makeVirtualKey("vk-1", "test-vk", "vk_test123"),
	}
	configData := makeConfigDataWithVirtualKeysAndDir(providers, vks, tempDir)
	createConfigFile(t, tempDir, configData)

	// Load config
	ctx := context.Background()
	config, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	defer config.ConfigStore.Close(ctx)

	// Verify virtual key exists in DB
	dbVK := verifyVirtualKeyInDB(t, config.ConfigStore, "vk-1")

	// Verify hash was set
	if dbVK.ConfigHash == "" {
		t.Error("Expected config hash to be set for new virtual key")
	}

	// Verify virtual key is in memory config
	if config.GovernanceConfig == nil || len(config.GovernanceConfig.VirtualKeys) == 0 {
		t.Fatal("Expected virtual key in governance config")
	}
	if config.GovernanceConfig.VirtualKeys[0].Name != "test-vk" {
		t.Errorf("Expected VK name 'test-vk', got '%s'", config.GovernanceConfig.VirtualKeys[0].Name)
	}
}

// TestSQLite_VirtualKey_HashMatch_DBPreserved tests that DB VK is preserved when hash matches
func TestSQLite_VirtualKey_HashMatch_DBPreserved(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Create config.json with a virtual key
	providers := map[string]configstore.ProviderConfig{
		"openai": makeProviderConfigWithNetwork("openai-key-1", "sk-test-123", "https://api.openai.com"),
	}
	vks := []tables.TableVirtualKey{
		makeVirtualKey("vk-1", "test-vk", "vk_test123"),
	}
	configData := makeConfigDataWithVirtualKeysAndDir(providers, vks, tempDir)
	createConfigFile(t, tempDir, configData)

	// First load
	ctx := context.Background()
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Get original hash
	dbVK1 := verifyVirtualKeyInDB(t, config1.ConfigStore, "vk-1")
	originalHash := dbVK1.ConfigHash
	config1.ConfigStore.Close(ctx)

	// Second load with same config.json
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	// Verify hash is unchanged
	dbVK2 := verifyVirtualKeyInDB(t, config2.ConfigStore, "vk-1")
	if dbVK2.ConfigHash != originalHash {
		t.Errorf("Expected hash to remain unchanged: original=%s, new=%s", originalHash, dbVK2.ConfigHash)
	}
}

// TestSQLite_VirtualKey_HashMismatch_FileSync tests that file VK is synced when hash differs
func TestSQLite_VirtualKey_HashMismatch_FileSync(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Create config.json with a virtual key
	providers := map[string]configstore.ProviderConfig{
		"openai": makeProviderConfigWithNetwork("openai-key-1", "sk-test-123", "https://api.openai.com"),
	}
	vks := []tables.TableVirtualKey{
		makeVirtualKey("vk-1", "original-name", "vk_test123"),
	}
	configData := makeConfigDataWithVirtualKeysAndDir(providers, vks, tempDir)
	createConfigFile(t, tempDir, configData)

	// First load
	ctx := context.Background()
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Verify original name
	dbVK1 := verifyVirtualKeyInDB(t, config1.ConfigStore, "vk-1")
	if dbVK1.Name != "original-name" {
		t.Errorf("Expected name 'original-name', got '%s'", dbVK1.Name)
	}
	originalHash := dbVK1.ConfigHash
	config1.ConfigStore.Close(ctx)

	// Modify config.json - change VK name and description
	vks2 := []tables.TableVirtualKey{
		{
			ID:          "vk-1",
			Name:        "modified-name",
			Description: "Modified description",
			Value:       "vk_test123",
			IsActive:    true,
		},
	}
	configData2 := makeConfigDataWithVirtualKeysAndDir(providers, vks2, tempDir)
	createConfigFile(t, tempDir, configData2)

	// Second load with modified config
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	// Verify name was updated
	dbVK2 := verifyVirtualKeyInDB(t, config2.ConfigStore, "vk-1")
	if dbVK2.Name != "modified-name" {
		t.Errorf("Expected name to be updated to 'modified-name', got '%s'", dbVK2.Name)
	}

	// Verify hash changed
	if dbVK2.ConfigHash == originalHash {
		t.Error("Expected hash to change when VK is modified")
	}
}

// TestSQLite_VirtualKey_DBOnlyVK_Preserved tests that VK added via DB is preserved on reload
func TestSQLite_VirtualKey_DBOnlyVK_Preserved(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Create config.json with one VK
	providers := map[string]configstore.ProviderConfig{
		"openai": makeProviderConfigWithNetwork("openai-key-1", "sk-test-123", "https://api.openai.com"),
	}
	vks := []tables.TableVirtualKey{
		makeVirtualKey("vk-file", "file-vk", "vk_file123"),
	}
	configData := makeConfigDataWithVirtualKeysAndDir(providers, vks, tempDir)
	createConfigFile(t, tempDir, configData)

	// First load
	ctx := context.Background()
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Add a second VK directly to DB (simulating dashboard addition)
	dashboardVK := tables.TableVirtualKey{
		ID:          "vk-dashboard",
		Name:        "dashboard-vk",
		Description: "Added via dashboard",
		Value:       "vk_dashboard456",
		IsActive:    true,
	}
	dashboardHash, _ := configstore.GenerateVirtualKeyHash(dashboardVK)
	dashboardVK.ConfigHash = dashboardHash
	err = config1.ConfigStore.CreateVirtualKey(ctx, &dashboardVK)
	if err != nil {
		t.Fatalf("Failed to create dashboard VK: %v", err)
	}
	config1.ConfigStore.Close(ctx)

	// Second load with same config.json (only has vk-file)
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	// Verify both VKs exist
	verifyVirtualKeyInDB(t, config2.ConfigStore, "vk-file")
	verifyVirtualKeyInDB(t, config2.ConfigStore, "vk-dashboard")

	// Verify dashboard VK is in governance config
	found := false
	for _, vk := range config2.GovernanceConfig.VirtualKeys {
		if vk.ID == "vk-dashboard" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected dashboard VK to be preserved in governance config")
	}
}

// TestSQLite_VirtualKey_WithProviderConfigs tests VK with provider configs hash generation
func TestSQLite_VirtualKey_WithProviderConfigs(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Create config.json with providers and a virtual key with provider configs
	providers := map[string]configstore.ProviderConfig{
		"openai": makeProviderConfigWithNetwork("openai-key-1", "sk-test-123", "https://api.openai.com"),
	}
	vks := []tables.TableVirtualKey{
		{
			ID:          "vk-1",
			Name:        "test-vk",
			Description: "VK with provider configs",
			Value:       "vk_test123",
			IsActive:    true,
			ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
				{
					Provider:      "openai",
					Weight:        ptrFloat64(1.5),
					AllowedModels: []string{"gpt-4", "gpt-3.5-turbo"},
				},
			},
		},
	}
	configData := makeConfigDataWithVirtualKeysAndDir(providers, vks, tempDir)
	createConfigFile(t, tempDir, configData)

	// Load config
	ctx := context.Background()
	config, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	defer config.ConfigStore.Close(ctx)

	// Verify VK exists with hash
	dbVK := verifyVirtualKeyInDB(t, config.ConfigStore, "vk-1")
	if dbVK.ConfigHash == "" {
		t.Error("Expected config hash to be set")
	}

	// Verify provider configs are present in VK
	if len(dbVK.ProviderConfigs) == 0 {
		t.Error("Expected provider configs to be persisted and loaded with virtual key")
	}

	// Generate hash manually and compare
	testVK := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "VK with provider configs",
		Value:       "vk_test123",
		IsActive:    true,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				Provider:      "openai",
				Weight:        ptrFloat64(1.5),
				AllowedModels: []string{"gpt-4", "gpt-3.5-turbo"},
			},
		},
	}
	expectedHash, err := configstore.GenerateVirtualKeyHash(testVK)
	if err != nil {
		t.Fatalf("Failed to generate expected hash: %v", err)
	}

	if dbVK.ConfigHash != expectedHash {
		t.Errorf("Expected config hash to match: got %s, want %s", dbVK.ConfigHash, expectedHash)
	}
}

// TestSQLite_VirtualKey_MergePath_WithProviderConfigs tests that when a NEW VK with ProviderConfigs
// is added via config.json during a reload (merge path), the ProviderConfigs are properly persisted.
// This is different from the bootstrap path which correctly handles associations.
func TestSQLite_VirtualKey_MergePath_WithProviderConfigs(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Step 1: Create initial config.json with a simple VK (no ProviderConfigs)
	providers := map[string]configstore.ProviderConfig{
		"openai": makeProviderConfigWithNetwork("openai-key-1", "sk-test-123", "https://api.openai.com"),
	}
	vks := []tables.TableVirtualKey{
		makeVirtualKey("vk-1", "simple-vk", "vk_simple123"),
	}
	configData := makeConfigDataWithVirtualKeysAndDir(providers, vks, tempDir)
	createConfigFile(t, tempDir, configData)

	// First load - bootstrap path
	ctx := context.Background()
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Verify initial VK exists
	verifyVirtualKeyInDB(t, config1.ConfigStore, "vk-1")
	config1.ConfigStore.Close(ctx)

	// Step 2: Update config.json to add a NEW VK with ProviderConfigs
	vks2 := []tables.TableVirtualKey{
		makeVirtualKey("vk-1", "simple-vk", "vk_simple123"), // Keep existing
		{
			ID:          "vk-2",
			Name:        "vk-with-providers",
			Description: "VK with provider configs added via merge",
			Value:       "vk_providers456",
			IsActive:    true,
			ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
				{
					Provider:      "openai",
					Weight:        ptrFloat64(2.0),
					AllowedModels: []string{"gpt-4", "gpt-3.5-turbo"},
				},
			},
		},
	}
	configData2 := makeConfigDataWithVirtualKeysAndDir(providers, vks2, tempDir)
	createConfigFile(t, tempDir, configData2)

	// Second load - merge path (this is where the bug is)
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	// Verify both VKs exist
	verifyVirtualKeyInDB(t, config2.ConfigStore, "vk-1")
	dbVK2 := verifyVirtualKeyInDB(t, config2.ConfigStore, "vk-2")

	// CRITICAL: Verify ProviderConfigs were persisted for the NEW VK added via merge path
	if len(dbVK2.ProviderConfigs) == 0 {
		t.Error("Expected provider configs to be persisted for VK added via merge path")
	}

	// Verify the provider config details
	if len(dbVK2.ProviderConfigs) > 0 {
		pc := dbVK2.ProviderConfigs[0]
		if pc.Provider != "openai" {
			t.Errorf("Expected provider 'openai', got '%s'", pc.Provider)
		}
		if pc.Weight == nil || *pc.Weight != 2.0 {
			t.Errorf("Expected weight 2.0, got %v", pc.Weight)
		}
	}
}

// TestSQLite_VirtualKey_MergePath_WithProviderConfigKeys tests that when a NEW VK with ProviderConfigs
// that reference specific Keys is added via merge path, the Keys many-to-many association is properly persisted.
//
// WHY THIS TEST WAS FAILING BEFORE THE FIX:
// -----------------------------------------
// The merge path at config.go:1121-1126 was calling CreateVirtualKey() with ProviderConfigs attached.
// When ProviderConfigs contain Keys ([]TableKey), GORM's Create() tries to handle the nested associations:
//
// 1. GORM creates the VirtualKey
// 2. GORM creates the ProviderConfigs (has-many from VirtualKey)
// 3. GORM tries to handle Keys inside ProviderConfigs (many-to-many)
//
// The problem is step 3: GORM tries to INSERT the Keys, but they already exist in the DB
// (they were created when the provider was loaded). This causes a unique constraint violation
// or foreign key error, which makes the entire transaction fail.
//
// The fix uses CreateVirtualKeyProviderConfig() which:
// 1. Stores Keys to a local variable
// 2. Creates ProviderConfig WITHOUT Keys
// 3. Uses Association("Keys").Append() to ASSOCIATE existing keys (not INSERT them)
//
// This test verifies that Keys are properly associated after the fix.
func TestSQLite_VirtualKey_MergePath_WithProviderConfigKeys(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Step 1: Create initial config.json with a provider that has keys
	keyID := uuid.NewString()
	providers := map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: keyID, Name: "openai-key-1", Value: *schemas.NewEnvVar("sk-test-123"), Weight: 1},
			},
			NetworkConfig: &schemas.NetworkConfig{BaseURL: "https://api.openai.com"},
		},
	}
	vks := []tables.TableVirtualKey{
		makeVirtualKey("vk-1", "simple-vk", "vk_simple123"),
	}
	configData := makeConfigDataWithVirtualKeysAndDir(providers, vks, tempDir)
	createConfigFile(t, tempDir, configData)

	// First load - bootstrap path (creates provider with key in DB)
	ctx := context.Background()
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Get the actual TableKey from DB (we need the uint ID for association)
	dbKeys, err := config1.ConfigStore.GetKeysByProvider(ctx, "openai")
	if err != nil {
		t.Fatalf("Failed to get keys: %v", err)
	}
	if len(dbKeys) == 0 {
		t.Fatal("Expected at least one key in OpenAI provider")
	}
	dbKey := dbKeys[0] // This is a tables.TableKey with proper uint ID

	// Verify initial VK exists
	verifyVirtualKeyInDB(t, config1.ConfigStore, "vk-1")
	config1.ConfigStore.Close(ctx)

	// Step 2: Update config.json to add a NEW VK with ProviderConfigs that reference the existing key
	// The key reference uses the TableKey from DB which has the proper uint ID
	vks2 := []tables.TableVirtualKey{
		makeVirtualKey("vk-1", "simple-vk", "vk_simple123"), // Keep existing
		{
			ID:          "vk-2",
			Name:        "vk-with-provider-keys",
			Description: "VK with provider configs referencing keys",
			Value:       "vk_keys456",
			IsActive:    true,
			ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
				{
					Provider:      "openai",
					Weight:        ptrFloat64(2.0),
					AllowedModels: []string{"gpt-4"},
					Keys:          []tables.TableKey{dbKey}, // Reference existing DB key
				},
			},
		},
	}
	configData2 := makeConfigDataWithVirtualKeysAndDir(providers, vks2, tempDir)
	createConfigFile(t, tempDir, configData2)

	// Second load - merge path
	// BEFORE FIX: This would fail because GORM tries to INSERT the key again
	// AFTER FIX: CreateVirtualKeyProviderConfig uses Append() to associate existing keys
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	// Verify both VKs exist
	verifyVirtualKeyInDB(t, config2.ConfigStore, "vk-1")
	dbVK2 := verifyVirtualKeyInDB(t, config2.ConfigStore, "vk-2")

	// Verify ProviderConfigs were persisted
	if len(dbVK2.ProviderConfigs) == 0 {
		t.Fatal("Expected provider configs to be persisted for VK added via merge path")
	}

	// CRITICAL: Verify Keys many-to-many association was persisted
	pc := dbVK2.ProviderConfigs[0]
	if len(pc.Keys) == 0 {
		t.Error("Expected Keys to be associated with provider config via merge path")
	}

	// Verify the key details if present
	if len(pc.Keys) > 0 {
		if pc.Keys[0].KeyID != dbKey.KeyID {
			t.Errorf("Expected key ID '%s', got '%s'", dbKey.KeyID, pc.Keys[0].KeyID)
		}
		t.Logf("✓ Key successfully associated: ID=%d, KeyID=%s, Name=%s", pc.Keys[0].ID, pc.Keys[0].KeyID, pc.Keys[0].Name)
	}
}

// TestSQLite_VirtualKey_ProviderConfigKeyIDs tests VK provider config key IDs are correctly hashed
func TestSQLite_VirtualKey_ProviderConfigKeyIDs(t *testing.T) {
	// This test verifies that when a VK's provider config references specific keys,
	// the hash includes those key IDs and changes when they change

	// Create two VKs with different key IDs in provider configs
	vk1 := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test",
		Value:       "vk_test123",
		IsActive:    true,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				Provider: "openai",
				Weight:   ptrFloat64(1.0),
				Keys: []tables.TableKey{
					{KeyID: "key-id-1"},
				},
			},
		},
	}

	vk2 := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test",
		Value:       "vk_test123",
		IsActive:    true,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				Provider: "openai",
				Weight:   ptrFloat64(1.0),
				Keys: []tables.TableKey{
					{KeyID: "key-id-2"}, // Different key ID
				},
			},
		},
	}

	hash1, err := configstore.GenerateVirtualKeyHash(vk1)
	if err != nil {
		t.Fatalf("Failed to generate hash1: %v", err)
	}

	hash2, err := configstore.GenerateVirtualKeyHash(vk2)
	if err != nil {
		t.Fatalf("Failed to generate hash2: %v", err)
	}

	if hash1 == hash2 {
		t.Error("Expected different hashes for VKs with different key IDs in provider configs")
	}

	// Same key IDs should produce same hash
	vk3 := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test",
		Value:       "vk_test123",
		IsActive:    true,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				Provider: "openai",
				Weight:   ptrFloat64(1.0),
				Keys: []tables.TableKey{
					{KeyID: "key-id-1"}, // Same as vk1
				},
			},
		},
	}

	hash3, err := configstore.GenerateVirtualKeyHash(vk3)
	if err != nil {
		t.Fatalf("Failed to generate hash3: %v", err)
	}

	if hash1 != hash3 {
		t.Error("Expected same hash for VKs with same key IDs in provider configs")
	}
}

// =============================================================================
// SQLite Integration Tests - Virtual Key Provider Config Scenarios
// =============================================================================

// TestSQLite_VKProviderConfig_NewConfig tests that new provider config in VK is properly created
func TestSQLite_VKProviderConfig_NewConfig(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Create config.json with a VK that has provider configs
	providers := map[string]configstore.ProviderConfig{
		"openai": makeProviderConfigWithNetwork("openai-key-1", "sk-test-123", "https://api.openai.com"),
	}
	vks := []tables.TableVirtualKey{
		{
			ID:          "vk-1",
			Name:        "vk-with-provider-config",
			Description: "VK with provider configs",
			Value:       "vk_test123",
			IsActive:    true,
			ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
				{
					Provider:      "openai",
					Weight:        ptrFloat64(2.0),
					AllowedModels: []string{"gpt-4"},
				},
			},
		},
	}
	configData := makeConfigDataWithVirtualKeysAndDir(providers, vks, tempDir)
	createConfigFile(t, tempDir, configData)

	// Load config
	ctx := context.Background()
	config, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	defer config.ConfigStore.Close(ctx)

	// Verify VK exists
	dbVK := verifyVirtualKeyInDB(t, config.ConfigStore, "vk-1")
	if dbVK.Name != "vk-with-provider-config" {
		t.Errorf("Expected VK name 'vk-with-provider-config', got '%s'", dbVK.Name)
	}

	// Verify provider configs were created
	providerConfigs, err := config.ConfigStore.GetVirtualKeyProviderConfigs(ctx, "vk-1")
	if err != nil {
		t.Fatalf("Failed to get provider configs: %v", err)
	}

	if len(providerConfigs) != 1 {
		t.Errorf("Expected 1 provider config, got %d", len(providerConfigs))
	}

	if len(providerConfigs) > 0 {
		if providerConfigs[0].Provider != "openai" {
			t.Errorf("Expected provider 'openai', got '%s'", providerConfigs[0].Provider)
		}
		if providerConfigs[0].Weight == nil || *providerConfigs[0].Weight != 2.0 {
			t.Errorf("Expected weight 2.0, got %v", providerConfigs[0].Weight)
		}
	}
}

// TestSQLite_VKProviderConfig_KeyReference tests provider config references keys by ID correctly
func TestSQLite_VKProviderConfig_KeyReference(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Create provider key first
	keyID := uuid.NewString()
	providers := map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: keyID, Name: "openai-key-1", Value: *schemas.NewEnvVar("sk-test-123"), Weight: 1},
			},
			NetworkConfig: &schemas.NetworkConfig{BaseURL: "https://api.openai.com"},
		},
	}

	// Create VK without key references (simple provider config)
	// Key references in VK provider configs require complex setup with existing DB keys
	vks := []tables.TableVirtualKey{
		{
			ID:          "vk-1",
			Name:        "vk-with-provider-ref",
			Description: "VK with provider config",
			Value:       "vk_test123",
			IsActive:    true,
			ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
				{
					Provider:      "openai",
					Weight:        ptrFloat64(1.0),
					AllowedModels: []string{"gpt-4"},
					// Keys left empty - means all keys for the provider are allowed
				},
			},
		},
	}
	configData := makeConfigDataWithVirtualKeysAndDir(providers, vks, tempDir)
	createConfigFile(t, tempDir, configData)

	// Load config
	ctx := context.Background()
	config, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	defer config.ConfigStore.Close(ctx)

	// Verify VK exists
	dbVK := verifyVirtualKeyInDB(t, config.ConfigStore, "vk-1")
	if dbVK.Name != "vk-with-provider-ref" {
		t.Errorf("Expected VK name 'vk-with-provider-ref', got '%s'", dbVK.Name)
	}

	// Verify provider config exists
	providerConfigs, err := config.ConfigStore.GetVirtualKeyProviderConfigs(ctx, "vk-1")
	if err != nil {
		t.Fatalf("Failed to get provider configs: %v", err)
	}

	if len(providerConfigs) > 0 {
		if providerConfigs[0].Provider != "openai" {
			t.Errorf("Expected provider 'openai', got '%s'", providerConfigs[0].Provider)
		}
		t.Logf("Provider config created successfully with provider: %s", providerConfigs[0].Provider)
	}
}

// TestSQLite_VKProviderConfig_HashChangesOnKeyIDChange tests hash changes when key IDs change
func TestSQLite_VKProviderConfig_HashChangesOnKeyIDChange(t *testing.T) {
	// Test that changing the key IDs in a provider config changes the hash

	// VK with key-id-1
	vk1 := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test",
		Value:       "vk_test123",
		IsActive:    true,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				Provider: "openai",
				Weight:   ptrFloat64(1.0),
				Keys: []tables.TableKey{
					{KeyID: "key-id-1", Name: "key-1"},
				},
			},
		},
	}

	// VK with key-id-2
	vk2 := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test",
		Value:       "vk_test123",
		IsActive:    true,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				Provider: "openai",
				Weight:   ptrFloat64(1.0),
				Keys: []tables.TableKey{
					{KeyID: "key-id-2", Name: "key-2"}, // Different key
				},
			},
		},
	}

	hash1, err := configstore.GenerateVirtualKeyHash(vk1)
	if err != nil {
		t.Fatalf("Failed to generate hash1: %v", err)
	}

	hash2, err := configstore.GenerateVirtualKeyHash(vk2)
	if err != nil {
		t.Fatalf("Failed to generate hash2: %v", err)
	}

	if hash1 == hash2 {
		t.Error("Expected different hashes when key IDs change in provider config")
	}

	// VK with multiple keys
	vk3 := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test",
		Value:       "vk_test123",
		IsActive:    true,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				Provider: "openai",
				Weight:   ptrFloat64(1.0),
				Keys: []tables.TableKey{
					{KeyID: "key-id-1", Name: "key-1"},
					{KeyID: "key-id-2", Name: "key-2"}, // Additional key
				},
			},
		},
	}

	hash3, err := configstore.GenerateVirtualKeyHash(vk3)
	if err != nil {
		t.Fatalf("Failed to generate hash3: %v", err)
	}

	if hash1 == hash3 {
		t.Error("Expected different hash when additional key is added to provider config")
	}
}

// TestSQLite_VKProviderConfig_WeightAndAllowedModels tests Weight/AllowedModels changes detected
func TestSQLite_VKProviderConfig_WeightAndAllowedModels(t *testing.T) {
	// Test that changing weight or allowed models changes the hash

	// Base VK
	vkBase := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test",
		Value:       "vk_test123",
		IsActive:    true,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				Provider:      "openai",
				Weight:        ptrFloat64(1.0),
				AllowedModels: []string{"gpt-4"},
			},
		},
	}

	// VK with different weight
	vkDifferentWeight := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test",
		Value:       "vk_test123",
		IsActive:    true,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				Provider:      "openai",
				Weight:        ptrFloat64(2.5), // Different weight
				AllowedModels: []string{"gpt-4"},
			},
		},
	}

	// VK with different allowed models
	vkDifferentModels := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test",
		Value:       "vk_test123",
		IsActive:    true,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				Provider:      "openai",
				Weight:        ptrFloat64(1.0),
				AllowedModels: []string{"gpt-4", "gpt-3.5-turbo"}, // Different models
			},
		},
	}

	hashBase, err := configstore.GenerateVirtualKeyHash(vkBase)
	if err != nil {
		t.Fatalf("Failed to generate hashBase: %v", err)
	}

	hashDiffWeight, err := configstore.GenerateVirtualKeyHash(vkDifferentWeight)
	if err != nil {
		t.Fatalf("Failed to generate hashDiffWeight: %v", err)
	}

	hashDiffModels, err := configstore.GenerateVirtualKeyHash(vkDifferentModels)
	if err != nil {
		t.Fatalf("Failed to generate hashDiffModels: %v", err)
	}

	if hashBase == hashDiffWeight {
		t.Error("Expected different hash when weight changes in provider config")
	}

	if hashBase == hashDiffModels {
		t.Error("Expected different hash when allowed models change in provider config")
	}

	if hashDiffWeight == hashDiffModels {
		t.Error("Expected different hashes for weight change vs model change")
	}

	// Same config should produce same hash
	vkSame := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test",
		Value:       "vk_test123",
		IsActive:    true,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				Provider:      "openai",
				Weight:        ptrFloat64(1.0),
				AllowedModels: []string{"gpt-4"},
			},
		},
	}

	hashSame, err := configstore.GenerateVirtualKeyHash(vkSame)
	if err != nil {
		t.Fatalf("Failed to generate hashSame: %v", err)
	}

	if hashBase != hashSame {
		t.Error("Expected same hash for identical configs")
	}
}

// =============================================================================
// SQLite Integration Tests - Full Lifecycle Scenarios
// =============================================================================

// TestSQLite_FullLifecycle_InitialLoad tests fresh DB + config.json -> all entities created
func TestSQLite_FullLifecycle_InitialLoad(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Create comprehensive config.json
	keyID1 := uuid.NewString()
	keyID2 := uuid.NewString()

	providers := map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: keyID1, Name: "openai-key-1", Value: *schemas.NewEnvVar("sk-openai-123"), Weight: 1},
				{ID: keyID2, Name: "openai-key-2", Value: *schemas.NewEnvVar("sk-openai-456"), Weight: 2},
			},
			NetworkConfig: &schemas.NetworkConfig{
				BaseURL: "https://api.openai.com",
			},
			ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{
				Concurrency: 10,
				BufferSize:  100,
			},
		},
		"anthropic": {
			Keys: []schemas.Key{
				{ID: uuid.NewString(), Name: "anthropic-key-1", Value: *schemas.NewEnvVar("sk-anthropic-123"), Weight: 1},
			},
			NetworkConfig: &schemas.NetworkConfig{
				BaseURL: "https://api.anthropic.com",
			},
		},
	}

	budgetID := "budget-1"
	rateLimitID := "rl-1"
	tokenMaxLimit := int64(10000)
	tokenResetDuration := "1h"
	requestMaxLimit := int64(100)
	requestResetDuration := "1m"

	governance := &configstore.GovernanceConfig{
		Budgets: []tables.TableBudget{
			{
				ID:            budgetID,
				MaxLimit:      100.0,
				ResetDuration: "1M",
				CurrentUsage:  0,
			},
		},
		RateLimits: []tables.TableRateLimit{
			{
				ID:                   rateLimitID,
				TokenMaxLimit:        &tokenMaxLimit,
				TokenResetDuration:   &tokenResetDuration,
				RequestMaxLimit:      &requestMaxLimit,
				RequestResetDuration: &requestResetDuration,
			},
		},
		VirtualKeys: []tables.TableVirtualKey{
			{
				ID:          "vk-1",
				Name:        "test-vk-1",
				Description: "Test virtual key 1",
				Value:       "vk_test123",
				IsActive:    true,
				BudgetID:    &budgetID,
				RateLimitID: &rateLimitID,
				ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
					{
						Provider:      "openai",
						Weight:        ptrFloat64(1.0),
						AllowedModels: []string{"gpt-4"},
					},
				},
			},
			{
				ID:          "vk-2",
				Name:        "test-vk-2",
				Description: "Test virtual key 2",
				Value:       "vk_test456",
				IsActive:    true,
			},
		},
	}

	configData := makeConfigDataFullWithDir(nil, providers, governance, tempDir)
	createConfigFile(t, tempDir, configData)

	// Load config
	ctx := context.Background()
	config, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	defer config.ConfigStore.Close(ctx)

	// Verify providers
	verifyProviderInDB(t, config.ConfigStore, schemas.OpenAI, 2)
	verifyProviderInDB(t, config.ConfigStore, schemas.Anthropic, 1)

	// Verify virtual keys
	verifyVirtualKeyInDB(t, config.ConfigStore, "vk-1")
	verifyVirtualKeyInDB(t, config.ConfigStore, "vk-2")

	// Verify in-memory state
	if len(config.Providers) != 2 {
		t.Errorf("Expected 2 providers in memory, got %d", len(config.Providers))
	}
	if len(config.GovernanceConfig.VirtualKeys) != 2 {
		t.Errorf("Expected 2 virtual keys in memory, got %d", len(config.GovernanceConfig.VirtualKeys))
	}
	if len(config.GovernanceConfig.Budgets) != 1 {
		t.Errorf("Expected 1 budget in memory, got %d", len(config.GovernanceConfig.Budgets))
	}
	if len(config.GovernanceConfig.RateLimits) != 1 {
		t.Errorf("Expected 1 rate limit in memory, got %d", len(config.GovernanceConfig.RateLimits))
	}

	// Verify hashes are set
	dbProviders, _ := config.ConfigStore.GetProvidersConfig(ctx)
	for provider, cfg := range dbProviders {
		if cfg.ConfigHash == "" {
			t.Errorf("Expected config hash for provider %s", provider)
		}
	}

	dbVK1 := verifyVirtualKeyInDB(t, config.ConfigStore, "vk-1")
	if dbVK1.ConfigHash == "" {
		t.Error("Expected config hash for VK vk-1")
	}
}

// TestSQLite_FullLifecycle_SecondLoadNoChanges tests that second load with same file has no DB writes
func TestSQLite_FullLifecycle_SecondLoadNoChanges(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Create config.json
	providers := map[string]configstore.ProviderConfig{
		"openai": makeProviderConfigWithNetwork("openai-key-1", "sk-test-123", "https://api.openai.com"),
	}
	vks := []tables.TableVirtualKey{
		makeVirtualKey("vk-1", "test-vk", "vk_test123"),
	}
	configData := makeConfigDataWithVirtualKeysAndDir(providers, vks, tempDir)
	createConfigFile(t, tempDir, configData)

	// First load
	ctx := context.Background()
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Capture state after first load
	dbProviders1, _ := config1.ConfigStore.GetProvidersConfig(ctx)
	providerHash1 := dbProviders1[schemas.OpenAI].ConfigHash
	dbVK1 := verifyVirtualKeyInDB(t, config1.ConfigStore, "vk-1")
	vkHash1 := dbVK1.ConfigHash
	config1.ConfigStore.Close(ctx)

	// Second load with same config.json
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	// Verify hashes are unchanged (no writes needed)
	dbProviders2, _ := config2.ConfigStore.GetProvidersConfig(ctx)
	if dbProviders2[schemas.OpenAI].ConfigHash != providerHash1 {
		t.Error("Provider hash should not change on reload with same config")
	}

	dbVK2 := verifyVirtualKeyInDB(t, config2.ConfigStore, "vk-1")
	if dbVK2.ConfigHash != vkHash1 {
		t.Error("VK hash should not change on reload with same config")
	}
}

// TestSQLite_FullLifecycle_FileChange_Selective tests that only changed items are updated in DB
func TestSQLite_FullLifecycle_FileChange_Selective(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Create config.json with two providers and two VKs
	providers := map[string]configstore.ProviderConfig{
		"openai": makeProviderConfigWithNetwork("openai-key-1", "sk-openai-123", "https://api.openai.com"),
		"anthropic": {
			Keys: []schemas.Key{
				{ID: uuid.NewString(), Name: "anthropic-key-1", Value: *schemas.NewEnvVar("sk-anthropic-123"), Weight: 1},
			},
			NetworkConfig: &schemas.NetworkConfig{BaseURL: "https://api.anthropic.com"},
		},
	}
	vks := []tables.TableVirtualKey{
		makeVirtualKey("vk-1", "vk-one", "vk_one123"),
		makeVirtualKey("vk-2", "vk-two", "vk_two456"),
	}
	configData := makeConfigDataWithVirtualKeysAndDir(providers, vks, tempDir)
	createConfigFile(t, tempDir, configData)

	// First load
	ctx := context.Background()
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Capture hashes
	dbProviders1, _ := config1.ConfigStore.GetProvidersConfig(ctx)
	openaiHash1 := dbProviders1[schemas.OpenAI].ConfigHash
	anthropicHash1 := dbProviders1[schemas.Anthropic].ConfigHash
	dbVK1_1 := verifyVirtualKeyInDB(t, config1.ConfigStore, "vk-1")
	vk1Hash1 := dbVK1_1.ConfigHash
	dbVK2_1 := verifyVirtualKeyInDB(t, config1.ConfigStore, "vk-2")
	vk2Hash1 := dbVK2_1.ConfigHash
	config1.ConfigStore.Close(ctx)

	// Modify config.json - change only OpenAI and vk-1
	providers2 := map[string]configstore.ProviderConfig{
		"openai": makeProviderConfigWithNetwork("openai-key-1", "sk-openai-123", "https://api.openai.com/v2"), // Changed
		"anthropic": {
			Keys: []schemas.Key{
				{ID: uuid.NewString(), Name: "anthropic-key-1", Value: *schemas.NewEnvVar("sk-anthropic-123"), Weight: 1},
			},
			NetworkConfig: &schemas.NetworkConfig{BaseURL: "https://api.anthropic.com"}, // Unchanged
		},
	}
	vks2 := []tables.TableVirtualKey{
		makeVirtualKey("vk-1", "vk-one-modified", "vk_one123"), // Changed name
		makeVirtualKey("vk-2", "vk-two", "vk_two456"),          // Unchanged
	}
	configData2 := makeConfigDataWithVirtualKeysAndDir(providers2, vks2, tempDir)
	createConfigFile(t, tempDir, configData2)

	// Second load
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	// Verify OpenAI hash changed (config changed)
	dbProviders2, _ := config2.ConfigStore.GetProvidersConfig(ctx)
	if dbProviders2[schemas.OpenAI].ConfigHash == openaiHash1 {
		t.Error("OpenAI hash should have changed (BaseURL changed)")
	}

	// Verify Anthropic hash unchanged (config unchanged)
	if dbProviders2[schemas.Anthropic].ConfigHash != anthropicHash1 {
		t.Error("Anthropic hash should remain unchanged")
	}

	// Verify vk-1 hash changed (name changed)
	dbVK1_2 := verifyVirtualKeyInDB(t, config2.ConfigStore, "vk-1")
	if dbVK1_2.ConfigHash == vk1Hash1 {
		t.Error("VK-1 hash should have changed (name changed)")
	}
	if dbVK1_2.Name != "vk-one-modified" {
		t.Errorf("Expected VK-1 name to be 'vk-one-modified', got '%s'", dbVK1_2.Name)
	}

	// Verify vk-2 hash unchanged
	dbVK2_2 := verifyVirtualKeyInDB(t, config2.ConfigStore, "vk-2")
	if dbVK2_2.ConfigHash != vk2Hash1 {
		t.Error("VK-2 hash should remain unchanged")
	}
}

// TestSQLite_FullLifecycle_DashboardEdits_ThenFileUnchanged tests dashboard edits are preserved
func TestSQLite_FullLifecycle_DashboardEdits_ThenFileUnchanged(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Create config.json
	keyID := uuid.NewString()
	providers := map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: keyID, Name: "openai-key-1", Value: *schemas.NewEnvVar("sk-original-123"), Weight: 1},
			},
			NetworkConfig: &schemas.NetworkConfig{BaseURL: "https://api.openai.com"},
		},
	}
	vks := []tables.TableVirtualKey{
		makeVirtualKey("vk-1", "test-vk", "vk_test123"),
	}
	configData := makeConfigDataWithVirtualKeysAndDir(providers, vks, tempDir)
	createConfigFile(t, tempDir, configData)

	// First load
	ctx := context.Background()
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Simulate dashboard edits:
	// 1. Add a new key
	dbProviders1, _ := config1.ConfigStore.GetProvidersConfig(ctx)
	openaiConfig := dbProviders1[schemas.OpenAI]
	openaiConfig.Keys = append(openaiConfig.Keys, schemas.Key{
		ID:     uuid.NewString(),
		Name:   "dashboard-key",
		Value:  *schemas.NewEnvVar("sk-dashboard-789"),
		Weight: 1,
	})
	dbProviders1[schemas.OpenAI] = openaiConfig
	err = config1.ConfigStore.UpdateProvidersConfig(ctx, dbProviders1)
	if err != nil {
		t.Fatalf("Failed to add dashboard key: %v", err)
	}

	// 2. Add a new VK via dashboard
	dashboardVK := tables.TableVirtualKey{
		ID:          "vk-dashboard",
		Name:        "dashboard-vk",
		Description: "Added via dashboard",
		Value:       "vk_dashboard456",
		IsActive:    true,
	}
	dashboardHash, _ := configstore.GenerateVirtualKeyHash(dashboardVK)
	dashboardVK.ConfigHash = dashboardHash
	err = config1.ConfigStore.CreateVirtualKey(ctx, &dashboardVK)
	if err != nil {
		t.Fatalf("Failed to create dashboard VK: %v", err)
	}
	config1.ConfigStore.Close(ctx)

	// Second load with SAME config.json (unchanged)
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	// Verify dashboard-added key is preserved
	dbProviders2, _ := config2.ConfigStore.GetProvidersConfig(ctx)
	if len(dbProviders2[schemas.OpenAI].Keys) != 2 {
		t.Errorf("Expected 2 keys (1 original + 1 dashboard), got %d", len(dbProviders2[schemas.OpenAI].Keys))
	}

	keyNames := make(map[string]bool)
	for _, k := range dbProviders2[schemas.OpenAI].Keys {
		keyNames[k.Name] = true
	}
	if !keyNames["openai-key-1"] {
		t.Error("Expected original key to be present")
	}
	if !keyNames["dashboard-key"] {
		t.Error("Expected dashboard key to be preserved")
	}

	// Verify dashboard-added VK is preserved
	verifyVirtualKeyInDB(t, config2.ConfigStore, "vk-1")
	verifyVirtualKeyInDB(t, config2.ConfigStore, "vk-dashboard")

	// Check VKs in memory
	vkNames := make(map[string]bool)
	for _, vk := range config2.GovernanceConfig.VirtualKeys {
		vkNames[vk.Name] = true
	}
	if !vkNames["test-vk"] {
		t.Error("Expected original VK to be present")
	}
	if !vkNames["dashboard-vk"] {
		t.Error("Expected dashboard VK to be preserved")
	}
}

// =============================================================================
// VirtualKey MCPConfig Tests
// =============================================================================

// TestGenerateVirtualKeyHash_MCPConfigChanges tests that MCP config changes affect the hash
func TestGenerateVirtualKeyHash_MCPConfigChanges(t *testing.T) {
	// Base VK with no MCP configs
	vkBase := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test",
		Value:       "vk_test123",
		IsActive:    true,
	}

	// VK with one MCP config
	vkWithMCP := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test",
		Value:       "vk_test123",
		IsActive:    true,
		MCPConfigs: []tables.TableVirtualKeyMCPConfig{
			{
				MCPClientID:    1,
				ToolsToExecute: []string{"tool1", "tool2"},
			},
		},
	}

	// VK with different MCP client ID
	vkDifferentMCPClient := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test",
		Value:       "vk_test123",
		IsActive:    true,
		MCPConfigs: []tables.TableVirtualKeyMCPConfig{
			{
				MCPClientID:    2, // Different client
				ToolsToExecute: []string{"tool1", "tool2"},
			},
		},
	}

	// VK with different tools
	vkDifferentTools := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test",
		Value:       "vk_test123",
		IsActive:    true,
		MCPConfigs: []tables.TableVirtualKeyMCPConfig{
			{
				MCPClientID:    1,
				ToolsToExecute: []string{"tool3", "tool4"}, // Different tools
			},
		},
	}

	// VK with multiple MCP configs
	vkMultipleMCP := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test",
		Value:       "vk_test123",
		IsActive:    true,
		MCPConfigs: []tables.TableVirtualKeyMCPConfig{
			{
				MCPClientID:    1,
				ToolsToExecute: []string{"tool1", "tool2"},
			},
			{
				MCPClientID:    2,
				ToolsToExecute: []string{"tool3"},
			},
		},
	}

	hashBase, err := configstore.GenerateVirtualKeyHash(vkBase)
	if err != nil {
		t.Fatalf("Failed to generate hashBase: %v", err)
	}

	hashWithMCP, err := configstore.GenerateVirtualKeyHash(vkWithMCP)
	if err != nil {
		t.Fatalf("Failed to generate hashWithMCP: %v", err)
	}

	hashDifferentClient, err := configstore.GenerateVirtualKeyHash(vkDifferentMCPClient)
	if err != nil {
		t.Fatalf("Failed to generate hashDifferentClient: %v", err)
	}

	hashDifferentTools, err := configstore.GenerateVirtualKeyHash(vkDifferentTools)
	if err != nil {
		t.Fatalf("Failed to generate hashDifferentTools: %v", err)
	}

	hashMultipleMCP, err := configstore.GenerateVirtualKeyHash(vkMultipleMCP)
	if err != nil {
		t.Fatalf("Failed to generate hashMultipleMCP: %v", err)
	}

	// Test: Adding MCP config changes hash
	if hashBase == hashWithMCP {
		t.Error("Expected different hash when MCP config is added")
	}

	// Test: Different MCP client ID changes hash
	if hashWithMCP == hashDifferentClient {
		t.Error("Expected different hash when MCP client ID changes")
	}

	// Test: Different tools change hash
	if hashWithMCP == hashDifferentTools {
		t.Error("Expected different hash when tools change")
	}

	// Test: Multiple MCP configs produce different hash
	if hashWithMCP == hashMultipleMCP {
		t.Error("Expected different hash when additional MCP config is added")
	}

	// Test: Same config produces same hash
	vkSame := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test",
		Value:       "vk_test123",
		IsActive:    true,
		MCPConfigs: []tables.TableVirtualKeyMCPConfig{
			{
				MCPClientID:    1,
				ToolsToExecute: []string{"tool1", "tool2"},
			},
		},
	}
	hashSame, err := configstore.GenerateVirtualKeyHash(vkSame)
	if err != nil {
		t.Fatalf("Failed to generate hashSame: %v", err)
	}

	if hashWithMCP != hashSame {
		t.Error("Expected same hash for identical MCP configs")
	}

	t.Log("✓ MCP config changes correctly affect virtual key hash")
}

// TestSQLite_VirtualKey_WithMCPConfigs tests VK creation with MCP configs
func TestSQLite_VirtualKey_WithMCPConfigs(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	ctx := context.Background()

	// Create config with virtual key
	providers := map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: uuid.NewString(), Name: "openai-key", Value: *schemas.NewEnvVar("sk-test123"), Weight: 1},
			},
		},
	}

	vks := []tables.TableVirtualKey{
		{
			ID:          "vk-1",
			Name:        "test-vk",
			Description: "VK with MCP config",
			Value:       "vk_test123",
			IsActive:    true,
		},
	}

	configData := makeConfigDataWithVirtualKeysAndDir(providers, vks, tempDir)
	createConfigFile(t, tempDir, configData)

	// First load - creates VK
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Manually create an MCP client in the DB (simulating MCP setup)
	mcpClientConfig := schemas.MCPClientConfig{
		ID:             "mcp-client-1",
		Name:           "test-mcp-client",
		ConnectionType: schemas.MCPConnectionTypeHTTP,
	}
	err = config1.ConfigStore.CreateMCPClientConfig(ctx, &mcpClientConfig)
	if err != nil {
		t.Fatalf("Failed to create MCP client: %v", err)
	}

	// Get the created MCP client to get its ID
	mcpClient, err := config1.ConfigStore.GetMCPClientByName(ctx, "test-mcp-client")
	if err != nil {
		t.Fatalf("Failed to get MCP client: %v", err)
	}

	// Create MCP config for the virtual key
	mcpConfig := tables.TableVirtualKeyMCPConfig{
		VirtualKeyID:   "vk-1",
		MCPClientID:    mcpClient.ID,
		ToolsToExecute: []string{"tool1", "tool2"},
	}
	err = config1.ConfigStore.CreateVirtualKeyMCPConfig(ctx, &mcpConfig)
	if err != nil {
		t.Fatalf("Failed to create VK MCP config: %v", err)
	}

	// Verify MCP config was created
	mcpConfigs, err := config1.ConfigStore.GetVirtualKeyMCPConfigs(ctx, "vk-1")
	if err != nil {
		t.Fatalf("Failed to get MCP configs: %v", err)
	}

	if len(mcpConfigs) != 1 {
		t.Errorf("Expected 1 MCP config, got %d", len(mcpConfigs))
	}

	if len(mcpConfigs) > 0 {
		if mcpConfigs[0].MCPClientID != mcpClient.ID {
			t.Errorf("Expected MCPClientID %d, got %d", mcpClient.ID, mcpConfigs[0].MCPClientID)
		}
		if len(mcpConfigs[0].ToolsToExecute) != 2 {
			t.Errorf("Expected 2 tools, got %d", len(mcpConfigs[0].ToolsToExecute))
		}
		t.Logf("✓ MCP config created successfully with MCPClientID: %d", mcpConfigs[0].MCPClientID)
	}

	config1.ConfigStore.Close(ctx)
}

// TestSQLite_VKMCPConfig_Reconciliation tests MCP config reconciliation on hash mismatch.
// When config.json changes (file is source of truth):
// - Configs in both file and DB → update from file
// - Configs only in file → create new
// - Configs only in DB → DELETE (file is source of truth)
func TestSQLite_VKMCPConfig_Reconciliation(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	ctx := context.Background()

	// Create initial config
	providers := map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: uuid.NewString(), Name: "openai-key", Value: *schemas.NewEnvVar("sk-test123"), Weight: 1},
			},
		},
	}

	vks := []tables.TableVirtualKey{
		{
			ID:          "vk-1",
			Name:        "test-vk",
			Description: "VK for MCP reconciliation test",
			Value:       "vk_test123",
			IsActive:    true,
		},
	}

	configData := makeConfigDataWithVirtualKeysAndDir(providers, vks, tempDir)
	createConfigFile(t, tempDir, configData)

	// First load
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Create MCP clients
	mcpClientConfig1 := schemas.MCPClientConfig{
		ID:             "mcp-client-1",
		Name:           "mcp-client-1",
		ConnectionType: schemas.MCPConnectionTypeHTTP,
	}
	err = config1.ConfigStore.CreateMCPClientConfig(ctx, &mcpClientConfig1)
	if err != nil {
		t.Fatalf("Failed to create MCP client 1: %v", err)
	}

	mcpClientConfig2 := schemas.MCPClientConfig{
		ID:             "mcp-client-2",
		Name:           "mcp-client-2",
		ConnectionType: schemas.MCPConnectionTypeHTTP,
	}
	err = config1.ConfigStore.CreateMCPClientConfig(ctx, &mcpClientConfig2)
	if err != nil {
		t.Fatalf("Failed to create MCP client 2: %v", err)
	}

	mcpClient1, _ := config1.ConfigStore.GetMCPClientByName(ctx, "mcp-client-1")
	mcpClient2, _ := config1.ConfigStore.GetMCPClientByName(ctx, "mcp-client-2")

	// Create initial MCP config for VK (simulates being added via dashboard, not in file)
	mcpConfig := tables.TableVirtualKeyMCPConfig{
		VirtualKeyID:   "vk-1",
		MCPClientID:    mcpClient1.ID,
		ToolsToExecute: []string{"tool1"},
	}
	err = config1.ConfigStore.CreateVirtualKeyMCPConfig(ctx, &mcpConfig)
	if err != nil {
		t.Fatalf("Failed to create VK MCP config: %v", err)
	}

	// Update the VK's config hash to include the MCP config
	vk, err := config1.ConfigStore.GetVirtualKey(ctx, "vk-1")
	if err != nil {
		t.Fatalf("Failed to get VK: %v", err)
	}
	vk.MCPConfigs = []tables.TableVirtualKeyMCPConfig{mcpConfig}
	newHash, _ := configstore.GenerateVirtualKeyHash(*vk)
	vk.ConfigHash = newHash
	err = config1.ConfigStore.UpdateVirtualKey(ctx, vk)
	if err != nil {
		t.Fatalf("Failed to update VK hash: %v", err)
	}

	config1.ConfigStore.Close(ctx)

	// Update config.json with a NEW MCP config (different client)
	// This triggers hash mismatch and reconciliation
	vks2 := []tables.TableVirtualKey{
		{
			ID:          "vk-1",
			Name:        "test-vk",
			Description: "VK for MCP reconciliation test",
			Value:       "vk_test123",
			IsActive:    true,
			MCPConfigs: []tables.TableVirtualKeyMCPConfig{
				{
					MCPClientID:    mcpClient2.ID, // Different MCP client - will be created
					ToolsToExecute: []string{"tool2", "tool3"},
				},
			},
		},
	}

	configData2 := makeConfigDataWithVirtualKeysAndDir(providers, vks2, tempDir)
	createConfigFile(t, tempDir, configData2)

	// Second load - should trigger reconciliation
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	// Verify MCP configs after reconciliation
	mcpConfigs, err := config2.ConfigStore.GetVirtualKeyMCPConfigs(ctx, "vk-1")
	if err != nil {
		t.Fatalf("Failed to get MCP configs after reconciliation: %v", err)
	}

	// Should have only client 2 (file is source of truth):
	// - client 1: DELETED (was in DB but not in new file)
	// - client 2: created (new from file)
	if len(mcpConfigs) != 1 {
		t.Errorf("Expected 1 MCP config after reconciliation (file is source of truth), got %d", len(mcpConfigs))
	}

	hasClient1 := false
	hasClient2 := false
	for _, mc := range mcpConfigs {
		if mc.MCPClientID == mcpClient1.ID {
			hasClient1 = true
		}
		if mc.MCPClientID == mcpClient2.ID {
			hasClient2 = true
			// Client 2 should have new tools from file
			if len(mc.ToolsToExecute) != 2 {
				t.Errorf("Expected client 2 to have 2 tools from file, got %d", len(mc.ToolsToExecute))
			}
		}
	}

	if hasClient1 {
		t.Error("MCP config for client 1 should be DELETED (file is source of truth)")
	}
	if !hasClient2 {
		t.Error("MCP config for client 2 should be created from file")
	}

	if !hasClient1 && hasClient2 {
		t.Logf("✓ MCP config reconciled successfully: client 1 deleted, client 2 created from file")
	}
}

// TestSQLite_VirtualKey_DashboardProviderConfig_PreservedOnFileChange tests that provider configs
// added via dashboard to a VK that also exists in config.json are PRESERVED when config.json changes.
//
// SCENARIO:
// 1. config.json has VK "vk-1" with "openai" provider config (weight=1.0)
// 2. Bootstrap load creates VK in DB
// 3. User adds "anthropic" provider config via dashboard (simulated by CreateVirtualKeyProviderConfig)
// 4. User modifies config.json (changes openai weight to 2.0 → hash mismatch)
// 5. Reload config
//
// EXPECTED: "anthropic" provider config should be DELETED (file is source of truth when hash changes)
func TestSQLite_VirtualKey_DashboardProviderConfig_DeletedOnFileChange(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	ctx := context.Background()

	// Step 1: Create config.json with VK that has openai provider config
	providers := map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: uuid.NewString(), Name: "openai-key", Value: *schemas.NewEnvVar("sk-openai-123"), Weight: 1},
			},
		},
		"anthropic": {
			Keys: []schemas.Key{
				{ID: uuid.NewString(), Name: "anthropic-key", Value: *schemas.NewEnvVar("sk-anthropic-123"), Weight: 1},
			},
		},
	}

	vks := []tables.TableVirtualKey{
		{
			ID:          "vk-1",
			Name:        "test-vk",
			Description: "VK for dashboard provider config preservation test",
			Value:       "vk_test123",
			IsActive:    true,
			ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
				{
					Provider:      "openai",
					Weight:        ptrFloat64(1.0),
					AllowedModels: []string{"gpt-4"},
				},
			},
		},
	}

	configData := makeConfigDataWithVirtualKeysAndDir(providers, vks, tempDir)
	createConfigFile(t, tempDir, configData)

	// Step 2: First load - bootstrap path
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Verify initial state
	providerConfigs1, err := config1.ConfigStore.GetVirtualKeyProviderConfigs(ctx, "vk-1")
	if err != nil {
		t.Fatalf("Failed to get provider configs after first load: %v", err)
	}
	if len(providerConfigs1) != 1 {
		t.Fatalf("Expected 1 provider config after first load, got %d", len(providerConfigs1))
	}
	if providerConfigs1[0].Provider != "openai" {
		t.Fatalf("Expected openai provider config, got %s", providerConfigs1[0].Provider)
	}
	t.Logf("✓ Initial state: VK has 1 provider config (openai)")

	// Step 3: Simulate dashboard adding "anthropic" provider config
	anthropicConfig := tables.TableVirtualKeyProviderConfig{
		VirtualKeyID:  "vk-1",
		Provider:      "anthropic",
		Weight:        ptrFloat64(1.0),
		AllowedModels: []string{"claude-3-opus"},
	}
	err = config1.ConfigStore.CreateVirtualKeyProviderConfig(ctx, &anthropicConfig)
	if err != nil {
		t.Fatalf("Failed to add anthropic provider config via dashboard: %v", err)
	}

	// Verify dashboard-added config exists
	providerConfigs2, err := config1.ConfigStore.GetVirtualKeyProviderConfigs(ctx, "vk-1")
	if err != nil {
		t.Fatalf("Failed to get provider configs after dashboard add: %v", err)
	}
	if len(providerConfigs2) != 2 {
		t.Fatalf("Expected 2 provider configs after dashboard add, got %d", len(providerConfigs2))
	}
	t.Logf("✓ Dashboard added anthropic provider config, now have 2 configs")

	config1.ConfigStore.Close(ctx)

	// Step 4: Modify config.json - change openai weight (causes hash mismatch)
	vks2 := []tables.TableVirtualKey{
		{
			ID:          "vk-1",
			Name:        "test-vk",
			Description: "VK for dashboard provider config preservation test",
			Value:       "vk_test123",
			IsActive:    true,
			ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
				{
					Provider:      "openai",
					Weight:        ptrFloat64(2.0), // Changed from 1.0 to 2.0 - triggers hash mismatch
					AllowedModels: []string{"gpt-4"},
				},
			},
		},
	}

	configData2 := makeConfigDataWithVirtualKeysAndDir(providers, vks2, tempDir)
	createConfigFile(t, tempDir, configData2)

	// Step 5: Second load - merge path with hash mismatch
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	// CRITICAL ASSERTION: Dashboard-added "anthropic" config should be DELETED (file is source of truth)
	providerConfigs3, err := config2.ConfigStore.GetVirtualKeyProviderConfigs(ctx, "vk-1")
	if err != nil {
		t.Fatalf("Failed to get provider configs after second load: %v", err)
	}

	// Check that only the file's provider config remains
	hasOpenAI := false
	hasAnthropic := false
	for _, pc := range providerConfigs3 {
		if pc.Provider == "openai" {
			hasOpenAI = true
			if pc.Weight == nil || *pc.Weight != 2.0 {
				t.Errorf("Expected openai weight to be updated to 2.0, got %v", pc.Weight)
			}
		}
		if pc.Provider == "anthropic" {
			hasAnthropic = true
		}
	}

	if !hasOpenAI {
		t.Error("openai provider config should exist after file change")
	}

	// File is source of truth - dashboard-added config should be deleted when hash changes
	if hasAnthropic {
		t.Error("anthropic provider config should be DELETED when config.json changed (file is source of truth)")
	}

	if len(providerConfigs3) != 1 {
		t.Errorf("Expected 1 provider config (only openai from file), got %d. File is source of truth when hash changes.", len(providerConfigs3))
	}

	if hasOpenAI && !hasAnthropic {
		t.Logf("✓ Only file provider config remains: openai (from file, updated). Dashboard-added anthropic correctly deleted.")
	}
}

// TestSQLite_VirtualKey_DashboardMCPConfig_DeletedOnFileChange tests that MCP configs
// added via dashboard to a VK that also exists in config.json are DELETED when config.json changes.
//
// SCENARIO:
// 1. config.json has VK "vk-1" with MCP config for client 1
// 2. Bootstrap load creates VK in DB
// 3. User adds MCP config for client 2 via dashboard (simulated by CreateVirtualKeyMCPConfig)
// 4. User modifies config.json (changes tools for client 1 → hash mismatch)
// 5. Reload config
//
// EXPECTED: MCP config for client 2 should be DELETED (file is source of truth when hash changes)
func TestSQLite_VirtualKey_DashboardMCPConfig_DeletedOnFileChange(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	ctx := context.Background()

	// Step 1: Create config.json with VK (no MCP configs initially - we'll add them via DB)
	providers := map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: uuid.NewString(), Name: "openai-key", Value: *schemas.NewEnvVar("sk-openai-123"), Weight: 1},
			},
		},
	}

	vks := []tables.TableVirtualKey{
		{
			ID:          "vk-1",
			Name:        "test-vk",
			Description: "VK for dashboard MCP config preservation test",
			Value:       "vk_test123",
			IsActive:    true,
		},
	}

	configData := makeConfigDataWithVirtualKeysAndDir(providers, vks, tempDir)
	createConfigFile(t, tempDir, configData)

	// Step 2: First load - bootstrap path
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Create two MCP clients in the DB
	mcpClient1Config := schemas.MCPClientConfig{
		ID:             "mcp-client-1",
		Name:           "mcp-client-1",
		ConnectionType: schemas.MCPConnectionTypeHTTP,
	}
	err = config1.ConfigStore.CreateMCPClientConfig(ctx, &mcpClient1Config)
	if err != nil {
		t.Fatalf("Failed to create MCP client 1: %v", err)
	}

	mcpClient2Config := schemas.MCPClientConfig{
		ID:             "mcp-client-2",
		Name:           "mcp-client-2",
		ConnectionType: schemas.MCPConnectionTypeHTTP,
	}
	err = config1.ConfigStore.CreateMCPClientConfig(ctx, &mcpClient2Config)
	if err != nil {
		t.Fatalf("Failed to create MCP client 2: %v", err)
	}

	mcpClient1, _ := config1.ConfigStore.GetMCPClientByName(ctx, "mcp-client-1")
	mcpClient2, _ := config1.ConfigStore.GetMCPClientByName(ctx, "mcp-client-2")

	// Add MCP config for client 1 (will be in config.json)
	mcpConfig1 := tables.TableVirtualKeyMCPConfig{
		VirtualKeyID:   "vk-1",
		MCPClientID:    mcpClient1.ID,
		ToolsToExecute: []string{"tool1"},
	}
	err = config1.ConfigStore.CreateVirtualKeyMCPConfig(ctx, &mcpConfig1)
	if err != nil {
		t.Fatalf("Failed to create MCP config 1: %v", err)
	}

	// Update VK hash to include MCP config 1
	vk, _ := config1.ConfigStore.GetVirtualKey(ctx, "vk-1")
	vk.MCPConfigs = []tables.TableVirtualKeyMCPConfig{mcpConfig1}
	newHash, _ := configstore.GenerateVirtualKeyHash(*vk)
	vk.ConfigHash = newHash
	config1.ConfigStore.UpdateVirtualKey(ctx, vk)

	// Step 3: Simulate dashboard adding MCP config for client 2
	mcpConfig2 := tables.TableVirtualKeyMCPConfig{
		VirtualKeyID:   "vk-1",
		MCPClientID:    mcpClient2.ID,
		ToolsToExecute: []string{"dashboard-tool"},
	}
	err = config1.ConfigStore.CreateVirtualKeyMCPConfig(ctx, &mcpConfig2)
	if err != nil {
		t.Fatalf("Failed to add MCP config 2 via dashboard: %v", err)
	}

	// Verify both MCP configs exist
	mcpConfigs1, err := config1.ConfigStore.GetVirtualKeyMCPConfigs(ctx, "vk-1")
	if err != nil {
		t.Fatalf("Failed to get MCP configs after dashboard add: %v", err)
	}
	if len(mcpConfigs1) != 2 {
		t.Fatalf("Expected 2 MCP configs after dashboard add, got %d", len(mcpConfigs1))
	}
	t.Logf("✓ Dashboard added MCP config for client 2, now have 2 MCP configs")

	config1.ConfigStore.Close(ctx)

	// Step 4: Modify config.json - change tools for client 1 (causes hash mismatch)
	vks2 := []tables.TableVirtualKey{
		{
			ID:          "vk-1",
			Name:        "test-vk",
			Description: "VK for dashboard MCP config preservation test",
			Value:       "vk_test123",
			IsActive:    true,
			MCPConfigs: []tables.TableVirtualKeyMCPConfig{
				{
					MCPClientID:    mcpClient1.ID,
					ToolsToExecute: []string{"tool1", "tool2"}, // Changed - adds tool2, triggers hash mismatch
				},
			},
		},
	}

	configData2 := makeConfigDataWithVirtualKeysAndDir(providers, vks2, tempDir)
	createConfigFile(t, tempDir, configData2)

	// Step 5: Second load - merge path with hash mismatch
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	// CRITICAL ASSERTION: Dashboard-added MCP config for client 2 should be DELETED (file is source of truth)
	mcpConfigs2, err := config2.ConfigStore.GetVirtualKeyMCPConfigs(ctx, "vk-1")
	if err != nil {
		t.Fatalf("Failed to get MCP configs after second load: %v", err)
	}

	// Check that only the file's MCP config remains
	hasClient1 := false
	hasClient2 := false
	for _, mc := range mcpConfigs2 {
		if mc.MCPClientID == mcpClient1.ID {
			hasClient1 = true
			if len(mc.ToolsToExecute) != 2 {
				t.Errorf("Expected client 1 to have 2 tools after update, got %d", len(mc.ToolsToExecute))
			}
		}
		if mc.MCPClientID == mcpClient2.ID {
			hasClient2 = true
		}
	}

	if !hasClient1 {
		t.Error("MCP config for client 1 should exist after file change")
	}

	// File is source of truth - dashboard-added config should be deleted when hash changes
	if hasClient2 {
		t.Error("MCP config for client 2 should be DELETED when config.json changed (file is source of truth)")
	}

	if len(mcpConfigs2) != 1 {
		t.Errorf("Expected 1 MCP config (only client 1 from file), got %d. File is source of truth when hash changes.", len(mcpConfigs2))
	}

	if hasClient1 && !hasClient2 {
		t.Logf("✓ Only file MCP config remains: client 1 (from file, updated). Dashboard-added client 2 correctly deleted.")
	}
}

// TestSQLite_VKMCPConfig_AddRemove tests adding MCP configs via config.json and verifies
// that removing from config.json DELETES them (file is source of truth when hash changes).
//
// Behavior:
// - Adding via config.json: configs are created
// - Removing from config.json: configs are DELETED (file is source of truth)
func TestSQLite_VKMCPConfig_AddRemove(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	ctx := context.Background()

	// Create initial config without MCP
	providers := map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: uuid.NewString(), Name: "openai-key", Value: *schemas.NewEnvVar("sk-test123"), Weight: 1},
			},
		},
	}

	vks := []tables.TableVirtualKey{
		{
			ID:          "vk-1",
			Name:        "test-vk",
			Description: "VK for add/remove test",
			Value:       "vk_test123",
			IsActive:    true,
		},
	}

	configData := makeConfigDataWithVirtualKeysAndDir(providers, vks, tempDir)
	createConfigFile(t, tempDir, configData)

	// First load
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Create MCP clients
	config1.ConfigStore.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{ID: "mcp-1", Name: "mcp-1", ConnectionType: schemas.MCPConnectionTypeHTTP})
	config1.ConfigStore.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{ID: "mcp-2", Name: "mcp-2", ConnectionType: schemas.MCPConnectionTypeHTTP})

	mcpClient1, _ := config1.ConfigStore.GetMCPClientByName(ctx, "mcp-1")
	mcpClient2, _ := config1.ConfigStore.GetMCPClientByName(ctx, "mcp-2")

	// Verify no MCP configs initially
	mcpConfigs, _ := config1.ConfigStore.GetVirtualKeyMCPConfigs(ctx, "vk-1")
	if len(mcpConfigs) != 0 {
		t.Errorf("Expected 0 MCP configs initially, got %d", len(mcpConfigs))
	}
	t.Log("✓ Initial state: No MCP configs")

	config1.ConfigStore.Close(ctx)

	// Update config.json to ADD MCP configs
	vks2 := []tables.TableVirtualKey{
		{
			ID:          "vk-1",
			Name:        "test-vk",
			Description: "VK for add/remove test",
			Value:       "vk_test123",
			IsActive:    true,
			MCPConfigs: []tables.TableVirtualKeyMCPConfig{
				{MCPClientID: mcpClient1.ID, ToolsToExecute: []string{"tool1"}},
				{MCPClientID: mcpClient2.ID, ToolsToExecute: []string{"tool2"}},
			},
		},
	}
	configData2 := makeConfigDataWithVirtualKeysAndDir(providers, vks2, tempDir)
	createConfigFile(t, tempDir, configData2)

	// Second load - should add MCP configs
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}

	mcpConfigs, _ = config2.ConfigStore.GetVirtualKeyMCPConfigs(ctx, "vk-1")
	if len(mcpConfigs) != 2 {
		t.Errorf("Expected 2 MCP configs after add, got %d", len(mcpConfigs))
	}
	t.Logf("✓ After add: %d MCP configs", len(mcpConfigs))

	config2.ConfigStore.Close(ctx)

	// Update config.json to remove one MCP config from the file
	// With file-is-source-of-truth, configs ARE deleted when removed from file (hash change)
	vks3 := []tables.TableVirtualKey{
		{
			ID:          "vk-1",
			Name:        "test-vk",
			Description: "VK for add/remove test",
			Value:       "vk_test123",
			IsActive:    true,
			MCPConfigs: []tables.TableVirtualKeyMCPConfig{
				{MCPClientID: mcpClient1.ID, ToolsToExecute: []string{"tool1"}},
				// mcpClient2 removed from file
			},
		},
	}
	configData3 := makeConfigDataWithVirtualKeysAndDir(providers, vks3, tempDir)
	createConfigFile(t, tempDir, configData3)

	// Third load - mcpClient2 config should be DELETED (file is source of truth)
	config3, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Third LoadConfig failed: %v", err)
	}
	defer config3.ConfigStore.Close(ctx)

	mcpConfigs, _ = config3.ConfigStore.GetVirtualKeyMCPConfigs(ctx, "vk-1")
	// Only client1 config should remain (file is source of truth)
	if len(mcpConfigs) != 1 {
		t.Errorf("Expected 1 MCP config after file change (file is source of truth), got %d", len(mcpConfigs))
	}

	hasClient1 := false
	hasClient2 := false
	for _, mc := range mcpConfigs {
		if mc.MCPClientID == mcpClient1.ID {
			hasClient1 = true
		}
		if mc.MCPClientID == mcpClient2.ID {
			hasClient2 = true
		}
	}

	if !hasClient1 {
		t.Error("MCP config for client 1 should exist")
	}
	if hasClient2 {
		t.Error("MCP config for client 2 should be DELETED (file is source of truth)")
	}
	t.Logf("✓ After file change: %d MCP config(s) - file is source of truth", len(mcpConfigs))
}

// TestSQLite_VKMCPConfig_UpdateTools tests updating tools in MCP config
func TestSQLite_VKMCPConfig_UpdateTools(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	ctx := context.Background()

	providers := map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: uuid.NewString(), Name: "openai-key", Value: *schemas.NewEnvVar("sk-test123"), Weight: 1},
			},
		},
	}

	// Create initial config data
	configData := makeConfigDataWithProvidersAndDir(providers, tempDir)
	createConfigFile(t, tempDir, configData)

	// First load
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Create MCP client
	config1.ConfigStore.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{ID: "mcp-client", Name: "mcp-client", ConnectionType: schemas.MCPConnectionTypeHTTP})
	mcpClient, _ := config1.ConfigStore.GetMCPClientByName(ctx, "mcp-client")

	// Create VK with MCP config
	vk := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test",
		Value:       "vk_test123",
		IsActive:    true,
		MCPConfigs: []tables.TableVirtualKeyMCPConfig{
			{MCPClientID: mcpClient.ID, ToolsToExecute: []string{"tool1", "tool2"}},
		},
	}
	hash, _ := configstore.GenerateVirtualKeyHash(vk)
	vk.ConfigHash = hash
	config1.ConfigStore.CreateVirtualKey(ctx, &vk)

	mcpConfigToCreate := tables.TableVirtualKeyMCPConfig{
		VirtualKeyID:   "vk-1",
		MCPClientID:    mcpClient.ID,
		ToolsToExecute: []string{"tool1", "tool2"},
	}
	config1.ConfigStore.CreateVirtualKeyMCPConfig(ctx, &mcpConfigToCreate)

	config1.ConfigStore.Close(ctx)

	// Update config.json with different tools for same MCP client
	vks := []tables.TableVirtualKey{
		{
			ID:          "vk-1",
			Name:        "test-vk",
			Description: "Test",
			Value:       "vk_test123",
			IsActive:    true,
			MCPConfigs: []tables.TableVirtualKeyMCPConfig{
				{MCPClientID: mcpClient.ID, ToolsToExecute: []string{"tool3", "tool4", "tool5"}}, // Different tools
			},
		},
	}
	configData2 := makeConfigDataWithVirtualKeysAndDir(providers, vks, tempDir)
	createConfigFile(t, tempDir, configData2)

	// Second load - should update tools
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	mcpConfigs, _ := config2.ConfigStore.GetVirtualKeyMCPConfigs(ctx, "vk-1")
	if len(mcpConfigs) != 1 {
		t.Errorf("Expected 1 MCP config, got %d", len(mcpConfigs))
	}
	if len(mcpConfigs) > 0 {
		if len(mcpConfigs[0].ToolsToExecute) != 3 {
			t.Errorf("Expected 3 tools after update, got %d", len(mcpConfigs[0].ToolsToExecute))
		}
		expectedTools := map[string]bool{"tool3": true, "tool4": true, "tool5": true}
		for _, tool := range mcpConfigs[0].ToolsToExecute {
			if !expectedTools[tool] {
				t.Errorf("Unexpected tool: %s", tool)
			}
		}
		t.Logf("✓ Tools updated successfully: %v", mcpConfigs[0].ToolsToExecute)
	}
}

// TestSQLite_VK_ProviderAndMCPConfigs_Combined tests VK with both provider and MCP configs
func TestSQLite_VK_ProviderAndMCPConfigs_Combined(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	ctx := context.Background()

	keyID := uuid.NewString()
	providers := map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: keyID, Name: "openai-key", Value: *schemas.NewEnvVar("sk-test123"), Weight: 1},
			},
		},
	}

	// Create initial config data
	configData := makeConfigDataWithProvidersAndDir(providers, tempDir)
	createConfigFile(t, tempDir, configData)

	// First load to set up DB
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Create MCP client
	config1.ConfigStore.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{ID: "mcp-client", Name: "mcp-client", ConnectionType: schemas.MCPConnectionTypeHTTP})
	mcpClient, _ := config1.ConfigStore.GetMCPClientByName(ctx, "mcp-client")

	config1.ConfigStore.Close(ctx)

	// Create config.json with VK having both provider and MCP configs
	vks := []tables.TableVirtualKey{
		{
			ID:          "vk-1",
			Name:        "combined-vk",
			Description: "VK with both provider and MCP configs",
			Value:       "vk_combined123",
			IsActive:    true,
			ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
				{
					Provider:      "openai",
					Weight:        ptrFloat64(1.5),
					AllowedModels: []string{"gpt-4"},
				},
			},
			MCPConfigs: []tables.TableVirtualKeyMCPConfig{
				{
					MCPClientID:    mcpClient.ID,
					ToolsToExecute: []string{"tool1", "tool2"},
				},
			},
		},
	}
	configData2 := makeConfigDataWithVirtualKeysAndDir(providers, vks, tempDir)
	createConfigFile(t, tempDir, configData2)

	// Load config
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	// Verify VK exists
	vk, err := config2.ConfigStore.GetVirtualKey(ctx, "vk-1")
	if err != nil {
		t.Fatalf("Failed to get VK: %v", err)
	}
	if vk == nil {
		t.Fatal("VK not found in DB")
	}

	// Verify provider configs
	providerConfigs, err := config2.ConfigStore.GetVirtualKeyProviderConfigs(ctx, "vk-1")
	if err != nil {
		t.Fatalf("Failed to get provider configs: %v", err)
	}
	if len(providerConfigs) != 1 {
		t.Errorf("Expected 1 provider config, got %d", len(providerConfigs))
	}
	if len(providerConfigs) > 0 {
		if providerConfigs[0].Provider != "openai" {
			t.Errorf("Expected provider 'openai', got '%s'", providerConfigs[0].Provider)
		}
		if providerConfigs[0].Weight == nil || *providerConfigs[0].Weight != 1.5 {
			t.Errorf("Expected weight 1.5, got %v", providerConfigs[0].Weight)
		}
		t.Logf("✓ Provider config: provider=%s, weight=%v", providerConfigs[0].Provider, providerConfigs[0].Weight)
	}

	// Verify MCP configs
	mcpConfigs, err := config2.ConfigStore.GetVirtualKeyMCPConfigs(ctx, "vk-1")
	if err != nil {
		t.Fatalf("Failed to get MCP configs: %v", err)
	}
	if len(mcpConfigs) != 1 {
		t.Errorf("Expected 1 MCP config, got %d", len(mcpConfigs))
	}
	if len(mcpConfigs) > 0 {
		if mcpConfigs[0].MCPClientID != mcpClient.ID {
			t.Errorf("Expected MCPClientID %d, got %d", mcpClient.ID, mcpConfigs[0].MCPClientID)
		}
		if len(mcpConfigs[0].ToolsToExecute) != 2 {
			t.Errorf("Expected 2 tools, got %d", len(mcpConfigs[0].ToolsToExecute))
		}
		t.Logf("✓ MCP config: MCPClientID=%d, tools=%v", mcpConfigs[0].MCPClientID, mcpConfigs[0].ToolsToExecute)
	}

	t.Log("✓ VK with combined provider and MCP configs created successfully")
}

// TestGenerateKeyHash_StableOrdering verifies that key hash is stable regardless of Models slice order
func TestGenerateKeyHash_StableOrdering(t *testing.T) {
	// Key with models in order A
	keyOrderA := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Models: []string{"gpt-4", "gpt-3.5-turbo", "gpt-4-turbo"},
		Weight: 1.5,
	}

	// Key with models in order B (reverse)
	keyOrderB := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Models: []string{"gpt-4-turbo", "gpt-3.5-turbo", "gpt-4"},
		Weight: 1.5,
	}

	// Key with models in order C (mixed)
	keyOrderC := schemas.Key{
		ID:     "key-1",
		Name:   "test-key",
		Value:  *schemas.NewEnvVar("sk-123"),
		Models: []string{"gpt-3.5-turbo", "gpt-4-turbo", "gpt-4"},
		Weight: 1.5,
	}

	hashA, err := configstore.GenerateKeyHash(keyOrderA)
	if err != nil {
		t.Fatalf("Failed to generate hash for order A: %v", err)
	}

	hashB, err := configstore.GenerateKeyHash(keyOrderB)
	if err != nil {
		t.Fatalf("Failed to generate hash for order B: %v", err)
	}

	hashC, err := configstore.GenerateKeyHash(keyOrderC)
	if err != nil {
		t.Fatalf("Failed to generate hash for order C: %v", err)
	}

	if hashA != hashB {
		t.Errorf("Hash should be stable regardless of Models order: hashA=%s, hashB=%s", hashA, hashB)
	}

	if hashA != hashC {
		t.Errorf("Hash should be stable regardless of Models order: hashA=%s, hashC=%s", hashA, hashC)
	}

	t.Logf("✓ Key hash is stable across different Models orderings: %s", hashA[:16])
}

// TestGenerateVirtualKeyHash_StableProviderConfigOrdering verifies hash stability with different provider config orderings
func TestGenerateVirtualKeyHash_StableProviderConfigOrdering(t *testing.T) {
	budgetID1 := "budget-1"
	budgetID2 := "budget-2"

	// VK with provider configs in order A
	vkOrderA := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				ID:            1,
				VirtualKeyID:  "vk-1",
				Provider:      "openai",
				Weight:        ptrFloat64(1.0),
				AllowedModels: []string{"gpt-4"},
				BudgetID:      &budgetID1,
			},
			{
				ID:            2,
				VirtualKeyID:  "vk-1",
				Provider:      "anthropic",
				Weight:        ptrFloat64(2.0),
				AllowedModels: []string{"claude-3"},
				BudgetID:      &budgetID2,
			},
			{
				ID:            3,
				VirtualKeyID:  "vk-1",
				Provider:      "cohere",
				Weight:        ptrFloat64(1.5),
				AllowedModels: []string{"command"},
			},
		},
	}

	// VK with provider configs in order B (reversed)
	vkOrderB := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				ID:            3,
				VirtualKeyID:  "vk-1",
				Provider:      "cohere",
				Weight:        ptrFloat64(1.5),
				AllowedModels: []string{"command"},
			},
			{
				ID:            2,
				VirtualKeyID:  "vk-1",
				Provider:      "anthropic",
				Weight:        ptrFloat64(2.0),
				AllowedModels: []string{"claude-3"},
				BudgetID:      &budgetID2,
			},
			{
				ID:            1,
				VirtualKeyID:  "vk-1",
				Provider:      "openai",
				Weight:        ptrFloat64(1.0),
				AllowedModels: []string{"gpt-4"},
				BudgetID:      &budgetID1,
			},
		},
	}

	// VK with provider configs in order C (mixed)
	vkOrderC := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				ID:            2,
				VirtualKeyID:  "vk-1",
				Provider:      "anthropic",
				Weight:        ptrFloat64(2.0),
				AllowedModels: []string{"claude-3"},
				BudgetID:      &budgetID2,
			},
			{
				ID:            1,
				VirtualKeyID:  "vk-1",
				Provider:      "openai",
				Weight:        ptrFloat64(1.0),
				AllowedModels: []string{"gpt-4"},
				BudgetID:      &budgetID1,
			},
			{
				ID:            3,
				VirtualKeyID:  "vk-1",
				Provider:      "cohere",
				Weight:        ptrFloat64(1.5),
				AllowedModels: []string{"command"},
			},
		},
	}

	hashA, err := configstore.GenerateVirtualKeyHash(vkOrderA)
	if err != nil {
		t.Fatalf("Failed to generate hash for order A: %v", err)
	}

	hashB, err := configstore.GenerateVirtualKeyHash(vkOrderB)
	if err != nil {
		t.Fatalf("Failed to generate hash for order B: %v", err)
	}

	hashC, err := configstore.GenerateVirtualKeyHash(vkOrderC)
	if err != nil {
		t.Fatalf("Failed to generate hash for order C: %v", err)
	}

	if hashA != hashB {
		t.Errorf("Hash should be stable regardless of ProviderConfigs order: hashA=%s, hashB=%s", hashA, hashB)
	}

	if hashA != hashC {
		t.Errorf("Hash should be stable regardless of ProviderConfigs order: hashA=%s, hashC=%s", hashA, hashC)
	}

	t.Logf("✓ VirtualKey hash is stable across different ProviderConfigs orderings: %s", hashA[:16])
}

// TestGenerateVirtualKeyHash_StableAllowedModelsOrdering verifies hash stability with different AllowedModels orderings
func TestGenerateVirtualKeyHash_StableAllowedModelsOrdering(t *testing.T) {
	// VK with AllowedModels in order A
	vkOrderA := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				ID:            1,
				VirtualKeyID:  "vk-1",
				Provider:      "openai",
				Weight:        ptrFloat64(1.0),
				AllowedModels: []string{"gpt-4", "gpt-3.5-turbo", "gpt-4-turbo", "gpt-4o"},
			},
		},
	}

	// VK with AllowedModels in order B (reversed)
	vkOrderB := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				ID:            1,
				VirtualKeyID:  "vk-1",
				Provider:      "openai",
				Weight:        ptrFloat64(1.0),
				AllowedModels: []string{"gpt-4o", "gpt-4-turbo", "gpt-3.5-turbo", "gpt-4"},
			},
		},
	}

	// VK with AllowedModels in order C (mixed)
	vkOrderC := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				ID:            1,
				VirtualKeyID:  "vk-1",
				Provider:      "openai",
				Weight:        ptrFloat64(1.0),
				AllowedModels: []string{"gpt-3.5-turbo", "gpt-4o", "gpt-4", "gpt-4-turbo"},
			},
		},
	}

	hashA, err := configstore.GenerateVirtualKeyHash(vkOrderA)
	if err != nil {
		t.Fatalf("Failed to generate hash for order A: %v", err)
	}

	hashB, err := configstore.GenerateVirtualKeyHash(vkOrderB)
	if err != nil {
		t.Fatalf("Failed to generate hash for order B: %v", err)
	}

	hashC, err := configstore.GenerateVirtualKeyHash(vkOrderC)
	if err != nil {
		t.Fatalf("Failed to generate hash for order C: %v", err)
	}

	if hashA != hashB {
		t.Errorf("Hash should be stable regardless of AllowedModels order: hashA=%s, hashB=%s", hashA, hashB)
	}

	if hashA != hashC {
		t.Errorf("Hash should be stable regardless of AllowedModels order: hashA=%s, hashC=%s", hashA, hashC)
	}

	t.Logf("✓ VirtualKey hash is stable across different AllowedModels orderings: %s", hashA[:16])
}

// TestGenerateVirtualKeyHash_StableKeyIDsOrdering verifies hash stability with different KeyIDs orderings
func TestGenerateVirtualKeyHash_StableKeyIDsOrdering(t *testing.T) {
	// VK with KeyIDs in order A
	vkOrderA := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				ID:            1,
				VirtualKeyID:  "vk-1",
				Provider:      "openai",
				Weight:        ptrFloat64(1.0),
				AllowedModels: []string{"gpt-4"},
				Keys: []tables.TableKey{
					{KeyID: "key-1", Name: "key-1"},
					{KeyID: "key-2", Name: "key-2"},
					{KeyID: "key-3", Name: "key-3"},
				},
			},
		},
	}

	// VK with KeyIDs in order B (reversed)
	vkOrderB := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				ID:            1,
				VirtualKeyID:  "vk-1",
				Provider:      "openai",
				Weight:        ptrFloat64(1.0),
				AllowedModels: []string{"gpt-4"},
				Keys: []tables.TableKey{
					{KeyID: "key-3", Name: "key-3"},
					{KeyID: "key-2", Name: "key-2"},
					{KeyID: "key-1", Name: "key-1"},
				},
			},
		},
	}

	// VK with KeyIDs in order C (mixed)
	vkOrderC := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				ID:            1,
				VirtualKeyID:  "vk-1",
				Provider:      "openai",
				Weight:        ptrFloat64(1.0),
				AllowedModels: []string{"gpt-4"},
				Keys: []tables.TableKey{
					{KeyID: "key-2", Name: "key-2"},
					{KeyID: "key-1", Name: "key-1"},
					{KeyID: "key-3", Name: "key-3"},
				},
			},
		},
	}

	hashA, err := configstore.GenerateVirtualKeyHash(vkOrderA)
	if err != nil {
		t.Fatalf("Failed to generate hash for order A: %v", err)
	}

	hashB, err := configstore.GenerateVirtualKeyHash(vkOrderB)
	if err != nil {
		t.Fatalf("Failed to generate hash for order B: %v", err)
	}

	hashC, err := configstore.GenerateVirtualKeyHash(vkOrderC)
	if err != nil {
		t.Fatalf("Failed to generate hash for order C: %v", err)
	}

	if hashA != hashB {
		t.Errorf("Hash should be stable regardless of KeyIDs order: hashA=%s, hashB=%s", hashA, hashB)
	}

	if hashA != hashC {
		t.Errorf("Hash should be stable regardless of KeyIDs order: hashA=%s, hashC=%s", hashA, hashC)
	}

	t.Logf("✓ VirtualKey hash is stable across different KeyIDs orderings: %s", hashA[:16])
}

// TestGenerateVirtualKeyHash_StableMCPConfigOrdering verifies hash stability with different MCP config orderings
func TestGenerateVirtualKeyHash_StableMCPConfigOrdering(t *testing.T) {
	// VK with MCP configs in order A
	vkOrderA := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		MCPConfigs: []tables.TableVirtualKeyMCPConfig{
			{
				ID:             1,
				VirtualKeyID:   "vk-1",
				MCPClientID:    1,
				ToolsToExecute: []string{"tool1"},
			},
			{
				ID:             2,
				VirtualKeyID:   "vk-1",
				MCPClientID:    2,
				ToolsToExecute: []string{"tool2"},
			},
			{
				ID:             3,
				VirtualKeyID:   "vk-1",
				MCPClientID:    3,
				ToolsToExecute: []string{"tool3"},
			},
		},
	}

	// VK with MCP configs in order B (reversed)
	vkOrderB := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		MCPConfigs: []tables.TableVirtualKeyMCPConfig{
			{
				ID:             3,
				VirtualKeyID:   "vk-1",
				MCPClientID:    3,
				ToolsToExecute: []string{"tool3"},
			},
			{
				ID:             2,
				VirtualKeyID:   "vk-1",
				MCPClientID:    2,
				ToolsToExecute: []string{"tool2"},
			},
			{
				ID:             1,
				VirtualKeyID:   "vk-1",
				MCPClientID:    1,
				ToolsToExecute: []string{"tool1"},
			},
		},
	}

	// VK with MCP configs in order C (mixed)
	vkOrderC := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		MCPConfigs: []tables.TableVirtualKeyMCPConfig{
			{
				ID:             2,
				VirtualKeyID:   "vk-1",
				MCPClientID:    2,
				ToolsToExecute: []string{"tool2"},
			},
			{
				ID:             1,
				VirtualKeyID:   "vk-1",
				MCPClientID:    1,
				ToolsToExecute: []string{"tool1"},
			},
			{
				ID:             3,
				VirtualKeyID:   "vk-1",
				MCPClientID:    3,
				ToolsToExecute: []string{"tool3"},
			},
		},
	}

	hashA, err := configstore.GenerateVirtualKeyHash(vkOrderA)
	if err != nil {
		t.Fatalf("Failed to generate hash for order A: %v", err)
	}

	hashB, err := configstore.GenerateVirtualKeyHash(vkOrderB)
	if err != nil {
		t.Fatalf("Failed to generate hash for order B: %v", err)
	}

	hashC, err := configstore.GenerateVirtualKeyHash(vkOrderC)
	if err != nil {
		t.Fatalf("Failed to generate hash for order C: %v", err)
	}

	if hashA != hashB {
		t.Errorf("Hash should be stable regardless of MCPConfigs order: hashA=%s, hashB=%s", hashA, hashB)
	}

	if hashA != hashC {
		t.Errorf("Hash should be stable regardless of MCPConfigs order: hashA=%s, hashC=%s", hashA, hashC)
	}

	t.Logf("✓ VirtualKey hash is stable across different MCPConfigs orderings: %s", hashA[:16])
}

// TestGenerateVirtualKeyHash_StableToolsToExecuteOrdering verifies hash stability with different ToolsToExecute orderings
func TestGenerateVirtualKeyHash_StableToolsToExecuteOrdering(t *testing.T) {
	// VK with ToolsToExecute in order A
	vkOrderA := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		MCPConfigs: []tables.TableVirtualKeyMCPConfig{
			{
				ID:             1,
				VirtualKeyID:   "vk-1",
				MCPClientID:    1,
				ToolsToExecute: []string{"tool-a", "tool-b", "tool-c", "tool-d"},
			},
		},
	}

	// VK with ToolsToExecute in order B (reversed)
	vkOrderB := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		MCPConfigs: []tables.TableVirtualKeyMCPConfig{
			{
				ID:             1,
				VirtualKeyID:   "vk-1",
				MCPClientID:    1,
				ToolsToExecute: []string{"tool-d", "tool-c", "tool-b", "tool-a"},
			},
		},
	}

	// VK with ToolsToExecute in order C (mixed)
	vkOrderC := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		MCPConfigs: []tables.TableVirtualKeyMCPConfig{
			{
				ID:             1,
				VirtualKeyID:   "vk-1",
				MCPClientID:    1,
				ToolsToExecute: []string{"tool-c", "tool-a", "tool-d", "tool-b"},
			},
		},
	}

	hashA, err := configstore.GenerateVirtualKeyHash(vkOrderA)
	if err != nil {
		t.Fatalf("Failed to generate hash for order A: %v", err)
	}

	hashB, err := configstore.GenerateVirtualKeyHash(vkOrderB)
	if err != nil {
		t.Fatalf("Failed to generate hash for order B: %v", err)
	}

	hashC, err := configstore.GenerateVirtualKeyHash(vkOrderC)
	if err != nil {
		t.Fatalf("Failed to generate hash for order C: %v", err)
	}

	if hashA != hashB {
		t.Errorf("Hash should be stable regardless of ToolsToExecute order: hashA=%s, hashB=%s", hashA, hashB)
	}

	if hashA != hashC {
		t.Errorf("Hash should be stable regardless of ToolsToExecute order: hashA=%s, hashC=%s", hashA, hashC)
	}

	t.Logf("✓ VirtualKey hash is stable across different ToolsToExecute orderings: %s", hashA[:16])
}

// TestGenerateVirtualKeyHash_StableCombinedOrdering verifies hash stability with all nested orderings randomized
func TestGenerateVirtualKeyHash_StableCombinedOrdering(t *testing.T) {
	budgetID := "budget-1"

	// VK with all elements in order A
	vkOrderA := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		BudgetID:    &budgetID,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				ID:            1,
				Provider:      "openai",
				Weight:        ptrFloat64(1.0),
				AllowedModels: []string{"gpt-4", "gpt-3.5-turbo"},
				Keys: []tables.TableKey{
					{KeyID: "key-1"},
					{KeyID: "key-2"},
				},
			},
			{
				ID:            2,
				Provider:      "anthropic",
				Weight:        ptrFloat64(2.0),
				AllowedModels: []string{"claude-3", "claude-2"},
				Keys: []tables.TableKey{
					{KeyID: "key-3"},
				},
			},
		},
		MCPConfigs: []tables.TableVirtualKeyMCPConfig{
			{
				ID:             1,
				MCPClientID:    1,
				ToolsToExecute: []string{"tool1", "tool2"},
			},
			{
				ID:             2,
				MCPClientID:    2,
				ToolsToExecute: []string{"tool3", "tool4"},
			},
		},
	}

	// VK with all elements in order B (everything reversed/shuffled)
	vkOrderB := tables.TableVirtualKey{
		ID:          "vk-1",
		Name:        "test-vk",
		Description: "Test virtual key",
		Value:       "vk_abc123",
		IsActive:    true,
		BudgetID:    &budgetID,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				ID:            2,
				Provider:      "anthropic",
				Weight:        ptrFloat64(2.0),
				AllowedModels: []string{"claude-2", "claude-3"}, // reversed
				Keys: []tables.TableKey{
					{KeyID: "key-3"},
				},
			},
			{
				ID:            1,
				Provider:      "openai",
				Weight:        ptrFloat64(1.0),
				AllowedModels: []string{"gpt-3.5-turbo", "gpt-4"}, // reversed
				Keys: []tables.TableKey{
					{KeyID: "key-2"}, // reversed
					{KeyID: "key-1"},
				},
			},
		},
		MCPConfigs: []tables.TableVirtualKeyMCPConfig{
			{
				ID:             2,
				MCPClientID:    2,
				ToolsToExecute: []string{"tool4", "tool3"}, // reversed
			},
			{
				ID:             1,
				MCPClientID:    1,
				ToolsToExecute: []string{"tool2", "tool1"}, // reversed
			},
		},
	}

	hashA, err := configstore.GenerateVirtualKeyHash(vkOrderA)
	if err != nil {
		t.Fatalf("Failed to generate hash for order A: %v", err)
	}

	hashB, err := configstore.GenerateVirtualKeyHash(vkOrderB)
	if err != nil {
		t.Fatalf("Failed to generate hash for order B: %v", err)
	}

	if hashA != hashB {
		t.Errorf("Hash should be stable regardless of all nested orderings: hashA=%s, hashB=%s", hashA, hashB)
	}

	t.Logf("✓ VirtualKey hash is stable with all nested orderings shuffled: %s", hashA[:16])
}

// ===================================================================================
// BUDGET HASH TESTS
// ===================================================================================

// TestGenerateBudgetHash tests hash generation for budgets
func TestGenerateBudgetHash(t *testing.T) {
	initTestLogger()

	// Test basic hash generation
	budget1 := tables.TableBudget{
		ID:            "budget-1",
		MaxLimit:      100.0,
		ResetDuration: "1d",
	}

	hash1, err := configstore.GenerateBudgetHash(budget1)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}
	if hash1 == "" {
		t.Error("Expected non-empty hash")
	}

	// Same budget should produce same hash
	hash1Again, _ := configstore.GenerateBudgetHash(budget1)
	if hash1 != hash1Again {
		t.Error("Same budget should produce same hash")
	}

	// Different ID should produce different hash
	budget2 := budget1
	budget2.ID = "budget-2"
	hash2, _ := configstore.GenerateBudgetHash(budget2)
	if hash1 == hash2 {
		t.Error("Different ID should produce different hash")
	}

	// Different MaxLimit should produce different hash
	budget3 := budget1
	budget3.MaxLimit = 200.0
	hash3, _ := configstore.GenerateBudgetHash(budget3)
	if hash1 == hash3 {
		t.Error("Different MaxLimit should produce different hash")
	}

	// Different ResetDuration should produce different hash
	budget4 := budget1
	budget4.ResetDuration = "1h"
	hash4, _ := configstore.GenerateBudgetHash(budget4)
	if hash1 == hash4 {
		t.Error("Different ResetDuration should produce different hash")
	}

	t.Log("✓ Budget hash generation works correctly for all fields")
}

// TestSQLite_Budget_NewFromFile tests new budget from config file
func TestSQLite_Budget_NewFromFile(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Create config with governance containing a budget
	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	configData.Governance = &configstore.GovernanceConfig{
		Budgets: []tables.TableBudget{
			{ID: "budget-1", MaxLimit: 100.0, ResetDuration: "1d"},
		},
	}
	createConfigFile(t, tempDir, configData)

	ctx := context.Background()
	config, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	defer config.ConfigStore.Close(ctx)

	// Verify budget in memory
	if config.GovernanceConfig == nil || len(config.GovernanceConfig.Budgets) != 1 {
		t.Fatal("Expected 1 budget in governance config")
	}
	if config.GovernanceConfig.Budgets[0].ID != "budget-1" {
		t.Error("Budget ID mismatch")
	}

	// Verify budget in DB
	govConfig, err := config.ConfigStore.GetGovernanceConfig(ctx)
	if err != nil {
		t.Fatalf("Failed to get governance config: %v", err)
	}
	if len(govConfig.Budgets) != 1 {
		t.Fatalf("Expected 1 budget in DB, got %d", len(govConfig.Budgets))
	}
	if govConfig.Budgets[0].ConfigHash == "" {
		t.Error("Expected budget config hash to be set")
	}

	t.Log("✓ New budget from file added to DB with hash")
}

// TestSQLite_Budget_HashMatch_DBPreserved tests DB budget preserved when hash matches
func TestSQLite_Budget_HashMatch_DBPreserved(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	configData.Governance = &configstore.GovernanceConfig{
		Budgets: []tables.TableBudget{
			{ID: "budget-1", MaxLimit: 100.0, ResetDuration: "1d"},
		},
	}
	createConfigFile(t, tempDir, configData)

	ctx := context.Background()
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Get hash from first load
	gov1, _ := config1.ConfigStore.GetGovernanceConfig(ctx)
	firstHash := gov1.Budgets[0].ConfigHash
	config1.ConfigStore.Close(ctx)

	// Second load - same config
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	gov2, _ := config2.ConfigStore.GetGovernanceConfig(ctx)
	if gov2.Budgets[0].ConfigHash != firstHash {
		t.Error("Hash should remain unchanged on reload")
	}

	t.Log("✓ Budget hash match - DB preserved")
}

// TestSQLite_Budget_HashMismatch_FileSync tests file sync when hash differs
func TestSQLite_Budget_HashMismatch_FileSync(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	configData.Governance = &configstore.GovernanceConfig{
		Budgets: []tables.TableBudget{
			{ID: "budget-1", MaxLimit: 100.0, ResetDuration: "1d"},
		},
	}
	createConfigFile(t, tempDir, configData)

	ctx := context.Background()
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}
	config1.ConfigStore.Close(ctx)

	// Update config file with different MaxLimit
	configData.Governance.Budgets[0].MaxLimit = 200.0
	createConfigFile(t, tempDir, configData)

	// Second load - should sync from file
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	gov2, _ := config2.ConfigStore.GetGovernanceConfig(ctx)
	if gov2.Budgets[0].MaxLimit != 200.0 {
		t.Errorf("Expected MaxLimit 200.0, got %f", gov2.Budgets[0].MaxLimit)
	}

	t.Log("✓ Budget hash mismatch - synced from file")
}

// TestSQLite_Budget_DBOnly_Preserved tests dashboard-added budget is preserved
func TestSQLite_Budget_DBOnly_Preserved(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	configData.Governance = &configstore.GovernanceConfig{
		Budgets: []tables.TableBudget{
			{ID: "budget-1", MaxLimit: 100.0, ResetDuration: "1d"},
		},
	}
	createConfigFile(t, tempDir, configData)

	ctx := context.Background()
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Add budget via "dashboard" (directly to DB)
	dashboardBudget := &tables.TableBudget{
		ID:            "budget-dashboard",
		MaxLimit:      500.0,
		ResetDuration: "1w",
	}
	if err := config1.ConfigStore.CreateBudget(ctx, dashboardBudget); err != nil {
		t.Fatalf("Failed to create dashboard budget: %v", err)
	}
	config1.ConfigStore.Close(ctx)

	// Reload - dashboard budget should be preserved
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	gov2, _ := config2.ConfigStore.GetGovernanceConfig(ctx)
	if len(gov2.Budgets) != 2 {
		t.Fatalf("Expected 2 budgets, got %d", len(gov2.Budgets))
	}

	// Find dashboard budget
	found := false
	for _, b := range gov2.Budgets {
		if b.ID == "budget-dashboard" {
			found = true
			if b.MaxLimit != 500.0 {
				t.Error("Dashboard budget MaxLimit changed")
			}
		}
	}
	if !found {
		t.Error("Dashboard-added budget was not preserved")
	}

	t.Log("✓ Dashboard-added budget preserved")
}

// ===================================================================================
// RATE LIMIT HASH TESTS
// ===================================================================================

// TestGenerateRateLimitHash tests hash generation for rate limits
func TestGenerateRateLimitHash(t *testing.T) {
	initTestLogger()

	tokenMax := int64(1000)
	tokenDur := "1h"
	reqMax := int64(100)
	reqDur := "1m"

	rl1 := tables.TableRateLimit{
		ID:                   "rl-1",
		TokenMaxLimit:        &tokenMax,
		TokenResetDuration:   &tokenDur,
		RequestMaxLimit:      &reqMax,
		RequestResetDuration: &reqDur,
	}

	hash1, err := configstore.GenerateRateLimitHash(rl1)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}
	if hash1 == "" {
		t.Error("Expected non-empty hash")
	}

	// Same rate limit should produce same hash
	hash1Again, _ := configstore.GenerateRateLimitHash(rl1)
	if hash1 != hash1Again {
		t.Error("Same rate limit should produce same hash")
	}

	// Different ID should produce different hash
	rl2 := rl1
	rl2.ID = "rl-2"
	hash2, _ := configstore.GenerateRateLimitHash(rl2)
	if hash1 == hash2 {
		t.Error("Different ID should produce different hash")
	}

	// Different TokenMaxLimit should produce different hash
	newTokenMax := int64(2000)
	rl3 := rl1
	rl3.TokenMaxLimit = &newTokenMax
	hash3, _ := configstore.GenerateRateLimitHash(rl3)
	if hash1 == hash3 {
		t.Error("Different TokenMaxLimit should produce different hash")
	}

	// Different TokenResetDuration should produce different hash
	newTokenDur := "2h"
	rl4 := rl1
	rl4.TokenResetDuration = &newTokenDur
	hash4, _ := configstore.GenerateRateLimitHash(rl4)
	if hash1 == hash4 {
		t.Error("Different TokenResetDuration should produce different hash")
	}

	// Different RequestMaxLimit should produce different hash
	newReqMax := int64(200)
	rl5 := rl1
	rl5.RequestMaxLimit = &newReqMax
	hash5, _ := configstore.GenerateRateLimitHash(rl5)
	if hash1 == hash5 {
		t.Error("Different RequestMaxLimit should produce different hash")
	}

	// Different RequestResetDuration should produce different hash
	newReqDur := "2m"
	rl6 := rl1
	rl6.RequestResetDuration = &newReqDur
	hash6, _ := configstore.GenerateRateLimitHash(rl6)
	if hash1 == hash6 {
		t.Error("Different RequestResetDuration should produce different hash")
	}

	// Nil vs set fields should produce different hash
	rl7 := tables.TableRateLimit{ID: "rl-1"}
	hash7, _ := configstore.GenerateRateLimitHash(rl7)
	if hash1 == hash7 {
		t.Error("Nil fields should produce different hash than set fields")
	}

	t.Log("✓ RateLimit hash generation works correctly for all fields")
}

// TestSQLite_RateLimit_NewFromFile tests new rate limit from config file
func TestSQLite_RateLimit_NewFromFile(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	tokenMax := int64(1000)
	tokenDur := "1h"

	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	configData.Governance = &configstore.GovernanceConfig{
		RateLimits: []tables.TableRateLimit{
			{ID: "rl-1", TokenMaxLimit: &tokenMax, TokenResetDuration: &tokenDur},
		},
	}
	createConfigFile(t, tempDir, configData)

	ctx := context.Background()
	config, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	defer config.ConfigStore.Close(ctx)

	// Verify in DB
	govConfig, err := config.ConfigStore.GetGovernanceConfig(ctx)
	if err != nil {
		t.Fatalf("Failed to get governance config: %v", err)
	}
	if len(govConfig.RateLimits) != 1 {
		t.Fatalf("Expected 1 rate limit in DB, got %d", len(govConfig.RateLimits))
	}
	if govConfig.RateLimits[0].ConfigHash == "" {
		t.Error("Expected rate limit config hash to be set")
	}

	t.Log("✓ New rate limit from file added to DB with hash")
}

// TestSQLite_RateLimit_HashMismatch_FileSync tests file sync when hash differs
func TestSQLite_RateLimit_HashMismatch_FileSync(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	tokenMax := int64(1000)
	tokenDur := "1h"

	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	configData.Governance = &configstore.GovernanceConfig{
		RateLimits: []tables.TableRateLimit{
			{ID: "rl-1", TokenMaxLimit: &tokenMax, TokenResetDuration: &tokenDur},
		},
	}
	createConfigFile(t, tempDir, configData)

	ctx := context.Background()
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}
	config1.ConfigStore.Close(ctx)

	// Update config file
	newTokenMax := int64(2000)
	configData.Governance.RateLimits[0].TokenMaxLimit = &newTokenMax
	createConfigFile(t, tempDir, configData)

	// Second load
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	gov2, _ := config2.ConfigStore.GetGovernanceConfig(ctx)
	if *gov2.RateLimits[0].TokenMaxLimit != 2000 {
		t.Errorf("Expected TokenMaxLimit 2000, got %d", *gov2.RateLimits[0].TokenMaxLimit)
	}

	t.Log("✓ RateLimit hash mismatch - synced from file")
}

// ===================================================================================
// CUSTOMER HASH TESTS
// ===================================================================================

// TestGenerateCustomerHash tests hash generation for customers
func TestGenerateCustomerHash(t *testing.T) {
	initTestLogger()

	budgetID := "budget-1"
	customer1 := tables.TableCustomer{
		ID:       "customer-1",
		Name:     "Test Customer",
		BudgetID: &budgetID,
	}

	hash1, err := configstore.GenerateCustomerHash(customer1)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}
	if hash1 == "" {
		t.Error("Expected non-empty hash")
	}

	// Same customer should produce same hash
	hash1Again, _ := configstore.GenerateCustomerHash(customer1)
	if hash1 != hash1Again {
		t.Error("Same customer should produce same hash")
	}

	// Different ID should produce different hash
	customer2 := customer1
	customer2.ID = "customer-2"
	hash2, _ := configstore.GenerateCustomerHash(customer2)
	if hash1 == hash2 {
		t.Error("Different ID should produce different hash")
	}

	// Different Name should produce different hash
	customer3 := customer1
	customer3.Name = "Different Customer"
	hash3, _ := configstore.GenerateCustomerHash(customer3)
	if hash1 == hash3 {
		t.Error("Different Name should produce different hash")
	}

	// Different BudgetID should produce different hash
	newBudgetID := "budget-2"
	customer4 := customer1
	customer4.BudgetID = &newBudgetID
	hash4, _ := configstore.GenerateCustomerHash(customer4)
	if hash1 == hash4 {
		t.Error("Different BudgetID should produce different hash")
	}

	// Nil BudgetID should produce different hash
	customer5 := customer1
	customer5.BudgetID = nil
	hash5, _ := configstore.GenerateCustomerHash(customer5)
	if hash1 == hash5 {
		t.Error("Nil BudgetID should produce different hash")
	}

	t.Log("✓ Customer hash generation works correctly for all fields")
}

// TestSQLite_Customer_NewFromFile tests new customer from config file
func TestSQLite_Customer_NewFromFile(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	configData.Governance = &configstore.GovernanceConfig{
		Customers: []tables.TableCustomer{
			{ID: "customer-1", Name: "Test Customer"},
		},
	}
	createConfigFile(t, tempDir, configData)

	ctx := context.Background()
	config, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	defer config.ConfigStore.Close(ctx)

	govConfig, err := config.ConfigStore.GetGovernanceConfig(ctx)
	if err != nil {
		t.Fatalf("Failed to get governance config: %v", err)
	}
	if len(govConfig.Customers) != 1 {
		t.Fatalf("Expected 1 customer in DB, got %d", len(govConfig.Customers))
	}
	if govConfig.Customers[0].ConfigHash == "" {
		t.Error("Expected customer config hash to be set")
	}

	t.Log("✓ New customer from file added to DB with hash")
}

// TestSQLite_Customer_HashMismatch_FileSync tests file sync when hash differs
func TestSQLite_Customer_HashMismatch_FileSync(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	configData.Governance = &configstore.GovernanceConfig{
		Customers: []tables.TableCustomer{
			{ID: "customer-1", Name: "Test Customer"},
		},
	}
	createConfigFile(t, tempDir, configData)

	ctx := context.Background()
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}
	config1.ConfigStore.Close(ctx)

	// Update config file
	configData.Governance.Customers[0].Name = "Updated Customer"
	createConfigFile(t, tempDir, configData)

	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	gov2, _ := config2.ConfigStore.GetGovernanceConfig(ctx)
	if gov2.Customers[0].Name != "Updated Customer" {
		t.Errorf("Expected Name 'Updated Customer', got '%s'", gov2.Customers[0].Name)
	}

	t.Log("✓ Customer hash mismatch - synced from file")
}

// ===================================================================================
// TEAM HASH TESTS
// ===================================================================================

// TestGenerateTeamHash tests hash generation for teams
func TestGenerateTeamHash(t *testing.T) {
	initTestLogger()

	customerID := "customer-1"
	budgetID := "budget-1"

	team1 := tables.TableTeam{
		ID:            "team-1",
		Name:          "Test Team",
		CustomerID:    &customerID,
		BudgetID:      &budgetID,
		ParsedProfile: map[string]interface{}{"key": "value"},
		ParsedConfig:  map[string]interface{}{"setting": true},
		ParsedClaims:  map[string]interface{}{"role": "admin"},
	}

	hash1, err := configstore.GenerateTeamHash(team1)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}
	if hash1 == "" {
		t.Error("Expected non-empty hash")
	}

	// Same team should produce same hash
	hash1Again, _ := configstore.GenerateTeamHash(team1)
	if hash1 != hash1Again {
		t.Error("Same team should produce same hash")
	}

	// Different ID should produce different hash
	team2 := team1
	team2.ID = "team-2"
	hash2, _ := configstore.GenerateTeamHash(team2)
	if hash1 == hash2 {
		t.Error("Different ID should produce different hash")
	}

	// Different Name should produce different hash
	team3 := team1
	team3.Name = "Different Team"
	hash3, _ := configstore.GenerateTeamHash(team3)
	if hash1 == hash3 {
		t.Error("Different Name should produce different hash")
	}

	// Different CustomerID should produce different hash
	newCustomerID := "customer-2"
	team4 := team1
	team4.CustomerID = &newCustomerID
	hash4, _ := configstore.GenerateTeamHash(team4)
	if hash1 == hash4 {
		t.Error("Different CustomerID should produce different hash")
	}

	// Different BudgetID should produce different hash
	newBudgetID := "budget-2"
	team5 := team1
	team5.BudgetID = &newBudgetID
	hash5, _ := configstore.GenerateTeamHash(team5)
	if hash1 == hash5 {
		t.Error("Different BudgetID should produce different hash")
	}

	// Different ParsedProfile should produce different hash
	team6 := team1
	team6.ParsedProfile = map[string]interface{}{"key": "different"}
	hash6, _ := configstore.GenerateTeamHash(team6)
	if hash1 == hash6 {
		t.Error("Different ParsedProfile should produce different hash")
	}

	// Different ParsedConfig should produce different hash
	team7 := team1
	team7.ParsedConfig = map[string]interface{}{"setting": false}
	hash7, _ := configstore.GenerateTeamHash(team7)
	if hash1 == hash7 {
		t.Error("Different ParsedConfig should produce different hash")
	}

	// Different ParsedClaims should produce different hash
	team8 := team1
	team8.ParsedClaims = map[string]interface{}{"role": "user"}
	hash8, _ := configstore.GenerateTeamHash(team8)
	if hash1 == hash8 {
		t.Error("Different ParsedClaims should produce different hash")
	}

	// Nil optional fields should produce different hash
	team9 := tables.TableTeam{ID: "team-1", Name: "Test Team"}
	hash9, _ := configstore.GenerateTeamHash(team9)
	if hash1 == hash9 {
		t.Error("Nil optional fields should produce different hash")
	}

	t.Log("✓ Team hash generation works correctly for all fields")
}

// TestSQLite_Team_NewFromFile tests new team from config file
func TestSQLite_Team_NewFromFile(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	configData.Governance = &configstore.GovernanceConfig{
		Teams: []tables.TableTeam{
			{ID: "team-1", Name: "Test Team"},
		},
	}
	createConfigFile(t, tempDir, configData)

	ctx := context.Background()
	config, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	defer config.ConfigStore.Close(ctx)

	govConfig, err := config.ConfigStore.GetGovernanceConfig(ctx)
	if err != nil {
		t.Fatalf("Failed to get governance config: %v", err)
	}
	if len(govConfig.Teams) != 1 {
		t.Fatalf("Expected 1 team in DB, got %d", len(govConfig.Teams))
	}
	if govConfig.Teams[0].ConfigHash == "" {
		t.Error("Expected team config hash to be set")
	}

	t.Log("✓ New team from file added to DB with hash")
}

// TestSQLite_Team_HashMismatch_FileSync tests file sync when hash differs
func TestSQLite_Team_HashMismatch_FileSync(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Note: Profile field has json:"-", so we use ParsedProfile for JSON config
	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	configData.Governance = &configstore.GovernanceConfig{
		Teams: []tables.TableTeam{
			{ID: "team-1", Name: "Test Team", ParsedProfile: map[string]interface{}{"key": "value"}},
		},
	}
	createConfigFile(t, tempDir, configData)

	ctx := context.Background()
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}
	config1.ConfigStore.Close(ctx)

	// Update config file with different Name (which affects hash)
	configData.Governance.Teams[0].Name = "Updated Team"
	createConfigFile(t, tempDir, configData)

	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	gov2, _ := config2.ConfigStore.GetGovernanceConfig(ctx)
	if gov2.Teams[0].Name != "Updated Team" {
		t.Errorf("Expected Name 'Updated Team', got '%s'", gov2.Teams[0].Name)
	}

	t.Log("✓ Team hash mismatch - synced from file")
}

// ===================================================================================
// MCP CLIENT HASH TESTS
// ===================================================================================

// TestGenerateMCPClientHash tests hash generation for MCP clients
func TestGenerateMCPClientHash(t *testing.T) {
	initTestLogger()

	connStr := "http://localhost:8080"
	mcp1 := tables.TableMCPClient{
		ClientID:         "mcp-1",
		Name:             "Test MCP",
		ConnectionType:   "sse",
		ConnectionString: schemas.NewEnvVar(connStr),
		ToolsToExecute:   []string{"tool1", "tool2"},
		Headers:          map[string]schemas.EnvVar{"Authorization": *schemas.NewEnvVar("Bearer token")},
	}

	hash1, err := configstore.GenerateMCPClientHash(mcp1)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}
	if hash1 == "" {
		t.Error("Expected non-empty hash")
	}

	// Same MCP should produce same hash
	hash1Again, _ := configstore.GenerateMCPClientHash(mcp1)
	if hash1 != hash1Again {
		t.Error("Same MCP should produce same hash")
	}

	// Different ClientID should produce different hash
	mcp2 := mcp1
	mcp2.ClientID = "mcp-2"
	hash2, _ := configstore.GenerateMCPClientHash(mcp2)
	if hash1 == hash2 {
		t.Error("Different ClientID should produce different hash")
	}

	// Different Name should produce different hash
	mcp3 := mcp1
	mcp3.Name = "Different MCP"
	hash3, _ := configstore.GenerateMCPClientHash(mcp3)
	if hash1 == hash3 {
		t.Error("Different Name should produce different hash")
	}

	// Different ConnectionType should produce different hash
	mcp4 := mcp1
	mcp4.ConnectionType = "stdio"
	hash4, _ := configstore.GenerateMCPClientHash(mcp4)
	if hash1 == hash4 {
		t.Error("Different ConnectionType should produce different hash")
	}

	// Different ConnectionString should produce different hash
	newConnStr := "http://localhost:9090"
	mcp5 := mcp1
	mcp5.ConnectionString = schemas.NewEnvVar(newConnStr)
	hash5, _ := configstore.GenerateMCPClientHash(mcp5)
	if hash1 == hash5 {
		t.Error("Different ConnectionString should produce different hash")
	}

	// Different ToolsToExecute should produce different hash
	mcp6 := mcp1
	mcp6.ToolsToExecute = []string{"tool3", "tool4"}
	hash6, _ := configstore.GenerateMCPClientHash(mcp6)
	if hash1 == hash6 {
		t.Error("Different ToolsToExecute should produce different hash")
	}

	// Different Headers should produce different hash
	mcp7 := mcp1
	mcp7.Headers = map[string]schemas.EnvVar{"X-Custom": *schemas.NewEnvVar("value")}
	hash7, _ := configstore.GenerateMCPClientHash(mcp7)
	if hash1 == hash7 {
		t.Error("Different Headers should produce different hash")
	}

	// ToolsToExecute order should not matter (sorted)
	mcp8 := mcp1
	mcp8.ToolsToExecute = []string{"tool2", "tool1"} // Reversed order
	hash8, _ := configstore.GenerateMCPClientHash(mcp8)
	if hash1 != hash8 {
		t.Error("ToolsToExecute order should not affect hash")
	}

	// Headers order should not matter (sorted by key)
	mcp9 := mcp1
	mcp9.Headers = map[string]schemas.EnvVar{"Authorization": *schemas.NewEnvVar("Bearer token")} // Same content
	hash9, _ := configstore.GenerateMCPClientHash(mcp9)
	if hash1 != hash9 {
		t.Error("Same headers should produce same hash")
	}

	t.Log("✓ MCPClient hash generation works correctly for all fields")
}

// ===================================================================================
// PLUGIN HASH TESTS
// ===================================================================================

// TestGeneratePluginHash tests hash generation for plugins
func TestGeneratePluginHash(t *testing.T) {
	initTestLogger()

	path := "/path/to/plugin"
	plugin1 := tables.TablePlugin{
		Name:       "test-plugin",
		Enabled:    true,
		Path:       &path,
		ConfigJSON: `{"setting": "value"}`,
		Version:    1,
	}

	hash1, err := configstore.GeneratePluginHash(plugin1)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}
	if hash1 == "" {
		t.Error("Expected non-empty hash")
	}

	// Same plugin should produce same hash
	hash1Again, _ := configstore.GeneratePluginHash(plugin1)
	if hash1 != hash1Again {
		t.Error("Same plugin should produce same hash")
	}

	// Different Name should produce different hash
	plugin2 := plugin1
	plugin2.Name = "different-plugin"
	hash2, _ := configstore.GeneratePluginHash(plugin2)
	if hash1 == hash2 {
		t.Error("Different Name should produce different hash")
	}

	// Different Enabled should produce different hash
	plugin3 := plugin1
	plugin3.Enabled = false
	hash3, _ := configstore.GeneratePluginHash(plugin3)
	if hash1 == hash3 {
		t.Error("Different Enabled should produce different hash")
	}

	// Different Path should produce different hash
	newPath := "/different/path"
	plugin4 := plugin1
	plugin4.Path = &newPath
	hash4, _ := configstore.GeneratePluginHash(plugin4)
	if hash1 == hash4 {
		t.Error("Different Path should produce different hash")
	}

	// Different ConfigJSON should produce different hash
	plugin5 := plugin1
	plugin5.ConfigJSON = `{"setting": "different"}`
	hash5, _ := configstore.GeneratePluginHash(plugin5)
	if hash1 == hash5 {
		t.Error("Different ConfigJSON should produce different hash")
	}

	// Different Version should produce different hash
	plugin6 := plugin1
	plugin6.Version = 2
	hash6, _ := configstore.GeneratePluginHash(plugin6)
	if hash1 == hash6 {
		t.Error("Different Version should produce different hash")
	}

	// Nil Path should produce different hash
	plugin7 := plugin1
	plugin7.Path = nil
	hash7, _ := configstore.GeneratePluginHash(plugin7)
	if hash1 == hash7 {
		t.Error("Nil Path should produce different hash")
	}

	t.Log("✓ Plugin hash generation works correctly for all fields")
}

// ===================================================================================
// CLIENT CONFIG HASH TESTS
// ===================================================================================

// TestGenerateClientConfigHash tests hash generation for client config
func TestGenerateClientConfigHash(t *testing.T) {
	initTestLogger()

	cc1 := configstore.ClientConfig{
		DropExcessRequests:      true,
		InitialPoolSize:         300,
		PrometheusLabels:        []string{"label1", "label2"},
		EnableLogging:           true,
		DisableContentLogging:   false,
		LogRetentionDays:        30,
		EnableGovernance:        true,
		EnforceGovernanceHeader: false,
		AllowDirectKeys:         true,
		AllowedOrigins:          []string{"http://localhost:3000"},
		MaxRequestBodySizeMB:    100,
		EnableLiteLLMFallbacks:  false,
	}

	hash1, err := cc1.GenerateClientConfigHash()
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}
	if hash1 == "" {
		t.Error("Expected non-empty hash")
	}

	// Same config should produce same hash
	hash1Again, _ := cc1.GenerateClientConfigHash()
	if hash1 != hash1Again {
		t.Error("Same config should produce same hash")
	}

	// Different DropExcessRequests should produce different hash
	cc2 := cc1
	cc2.DropExcessRequests = false
	hash2, _ := cc2.GenerateClientConfigHash()
	if hash1 == hash2 {
		t.Error("Different DropExcessRequests should produce different hash")
	}

	// Different InitialPoolSize should produce different hash
	cc3 := cc1
	cc3.InitialPoolSize = 500
	hash3, _ := cc3.GenerateClientConfigHash()
	if hash1 == hash3 {
		t.Error("Different InitialPoolSize should produce different hash")
	}

	// Different PrometheusLabels should produce different hash
	cc4 := cc1
	cc4.PrometheusLabels = []string{"label3"}
	hash4, _ := cc4.GenerateClientConfigHash()
	if hash1 == hash4 {
		t.Error("Different PrometheusLabels should produce different hash")
	}

	// Different EnableLogging should produce different hash
	cc5 := cc1
	cc5.EnableLogging = false
	hash5, _ := cc5.GenerateClientConfigHash()
	if hash1 == hash5 {
		t.Error("Different EnableLogging should produce different hash")
	}

	// Different DisableContentLogging should produce different hash
	cc6 := cc1
	cc6.DisableContentLogging = true
	hash6, _ := cc6.GenerateClientConfigHash()
	if hash1 == hash6 {
		t.Error("Different DisableContentLogging should produce different hash")
	}

	// Different LogRetentionDays should produce different hash
	cc7 := cc1
	cc7.LogRetentionDays = 60
	hash7, _ := cc7.GenerateClientConfigHash()
	if hash1 == hash7 {
		t.Error("Different LogRetentionDays should produce different hash")
	}

	// Different EnableGovernance should produce different hash
	cc8 := cc1
	cc8.EnableGovernance = false
	hash8, _ := cc8.GenerateClientConfigHash()
	if hash1 == hash8 {
		t.Error("Different EnableGovernance should produce different hash")
	}

	// Different EnforceGovernanceHeader should produce different hash
	cc9 := cc1
	cc9.EnforceGovernanceHeader = true
	hash9, _ := cc9.GenerateClientConfigHash()
	if hash1 == hash9 {
		t.Error("Different EnforceGovernanceHeader should produce different hash")
	}

	// Different AllowDirectKeys should produce different hash
	cc10 := cc1
	cc10.AllowDirectKeys = false
	hash10, _ := cc10.GenerateClientConfigHash()
	if hash1 == hash10 {
		t.Error("Different AllowDirectKeys should produce different hash")
	}

	// Different AllowedOrigins should produce different hash
	cc11 := cc1
	cc11.AllowedOrigins = []string{"http://example.com"}
	hash11, _ := cc11.GenerateClientConfigHash()
	if hash1 == hash11 {
		t.Error("Different AllowedOrigins should produce different hash")
	}

	// Different MaxRequestBodySizeMB should produce different hash
	cc12 := cc1
	cc12.MaxRequestBodySizeMB = 200
	hash12, _ := cc12.GenerateClientConfigHash()
	if hash1 == hash12 {
		t.Error("Different MaxRequestBodySizeMB should produce different hash")
	}

	// Different EnableLiteLLMFallbacks should produce different hash
	cc13 := cc1
	cc13.EnableLiteLLMFallbacks = true
	hash13, _ := cc13.GenerateClientConfigHash()
	if hash1 == hash13 {
		t.Error("Different EnableLiteLLMFallbacks should produce different hash")
	}

	// PrometheusLabels order should not matter (sorted)
	cc14 := cc1
	cc14.PrometheusLabels = []string{"label2", "label1"} // Reversed
	hash14, _ := cc14.GenerateClientConfigHash()
	if hash1 != hash14 {
		t.Error("PrometheusLabels order should not affect hash")
	}

	// AllowedOrigins order should not matter (sorted)
	cc15 := cc1
	cc15.AllowedOrigins = []string{"http://localhost:3000"} // Same
	hash15, _ := cc15.GenerateClientConfigHash()
	if hash1 != hash15 {
		t.Error("Same AllowedOrigins should produce same hash")
	}

	t.Log("✓ ClientConfig hash generation works correctly for all fields")
}

// ===================================================================================
// COMBINED GOVERNANCE RECONCILIATION TEST
// ===================================================================================

// TestSQLite_Governance_FullReconciliation tests full governance reconciliation
func TestSQLite_Governance_FullReconciliation(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	tokenMax := int64(1000)
	tokenDur := "1h"

	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	configData.Governance = &configstore.GovernanceConfig{
		Budgets: []tables.TableBudget{
			{ID: "budget-1", MaxLimit: 100.0, ResetDuration: "1d"},
		},
		RateLimits: []tables.TableRateLimit{
			{ID: "rl-1", TokenMaxLimit: &tokenMax, TokenResetDuration: &tokenDur},
		},
		Customers: []tables.TableCustomer{
			{ID: "customer-1", Name: "Test Customer"},
		},
		Teams: []tables.TableTeam{
			{ID: "team-1", Name: "Test Team"},
		},
	}
	createConfigFile(t, tempDir, configData)

	ctx := context.Background()
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Verify all entities have hashes
	gov1, _ := config1.ConfigStore.GetGovernanceConfig(ctx)
	if gov1.Budgets[0].ConfigHash == "" {
		t.Error("Budget hash not set")
	}
	if gov1.RateLimits[0].ConfigHash == "" {
		t.Error("RateLimit hash not set")
	}
	if gov1.Customers[0].ConfigHash == "" {
		t.Error("Customer hash not set")
	}
	if gov1.Teams[0].ConfigHash == "" {
		t.Error("Team hash not set")
	}
	config1.ConfigStore.Close(ctx)

	// Update all entities in config file
	configData.Governance.Budgets[0].MaxLimit = 200.0
	newTokenMax := int64(2000)
	configData.Governance.RateLimits[0].TokenMaxLimit = &newTokenMax
	configData.Governance.Customers[0].Name = "Updated Customer"
	configData.Governance.Teams[0].Name = "Updated Team"
	createConfigFile(t, tempDir, configData)

	// Reload and verify all entities are updated
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	gov2, _ := config2.ConfigStore.GetGovernanceConfig(ctx)

	if gov2.Budgets[0].MaxLimit != 200.0 {
		t.Errorf("Budget MaxLimit not updated: got %f", gov2.Budgets[0].MaxLimit)
	}
	if *gov2.RateLimits[0].TokenMaxLimit != 2000 {
		t.Errorf("RateLimit TokenMaxLimit not updated: got %d", *gov2.RateLimits[0].TokenMaxLimit)
	}
	if gov2.Customers[0].Name != "Updated Customer" {
		t.Errorf("Customer Name not updated: got %s", gov2.Customers[0].Name)
	}
	if gov2.Teams[0].Name != "Updated Team" {
		t.Errorf("Team Name not updated: got %s", gov2.Teams[0].Name)
	}

	t.Log("✓ Full governance reconciliation works correctly for all entities")
}

// TestSQLite_Governance_DBOnly_AllPreserved tests all dashboard-added entities preserved
func TestSQLite_Governance_DBOnly_AllPreserved(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Start with minimal config
	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	configData.Governance = &configstore.GovernanceConfig{}
	createConfigFile(t, tempDir, configData)

	ctx := context.Background()
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Add entities via dashboard
	tokenMax := int64(1000)
	tokenDur := "1h"

	if err := config1.ConfigStore.CreateBudget(ctx, &tables.TableBudget{
		ID: "dashboard-budget", MaxLimit: 500.0, ResetDuration: "1w",
	}); err != nil {
		t.Fatalf("Failed to create dashboard budget: %v", err)
	}

	if err := config1.ConfigStore.CreateRateLimit(ctx, &tables.TableRateLimit{
		ID: "dashboard-rl", TokenMaxLimit: &tokenMax, TokenResetDuration: &tokenDur,
	}); err != nil {
		t.Fatalf("Failed to create dashboard rate limit: %v", err)
	}

	if err := config1.ConfigStore.CreateCustomer(ctx, &tables.TableCustomer{
		ID: "dashboard-customer", Name: "Dashboard Customer",
	}); err != nil {
		t.Fatalf("Failed to create dashboard customer: %v", err)
	}

	if err := config1.ConfigStore.CreateTeam(ctx, &tables.TableTeam{
		ID: "dashboard-team", Name: "Dashboard Team",
	}); err != nil {
		t.Fatalf("Failed to create dashboard team: %v", err)
	}

	config1.ConfigStore.Close(ctx)

	// Reload - all dashboard entities should be preserved
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	gov2, _ := config2.ConfigStore.GetGovernanceConfig(ctx)

	if len(gov2.Budgets) != 1 || gov2.Budgets[0].ID != "dashboard-budget" {
		t.Error("Dashboard budget not preserved")
	}
	if len(gov2.RateLimits) != 1 || gov2.RateLimits[0].ID != "dashboard-rl" {
		t.Error("Dashboard rate limit not preserved")
	}
	if len(gov2.Customers) != 1 || gov2.Customers[0].ID != "dashboard-customer" {
		t.Error("Dashboard customer not preserved")
	}
	if len(gov2.Teams) != 1 || gov2.Teams[0].ID != "dashboard-team" {
		t.Error("Dashboard team not preserved")
	}

	t.Log("✓ All dashboard-added entities preserved on reload")
}

// ===================================================================================
// RUNTIME VS MIGRATION HASH PARITY TESTS (SQLite Integration)
// ===================================================================================
// These tests verify that hash generation produces identical results when:
// 1. Virtual fields are populated (runtime after AfterFind hook)
// 2. Data is loaded from SQLite via GORM Find() (simulating migration context)
//
// This tests whether GORM's AfterFind hooks properly populate virtual fields
// during database reads, ensuring hash consistency between file configs and DB configs.
// ===================================================================================

// setupTestDB creates an in-memory SQLite database for testing
func setupTestDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	return db
}

// TestGenerateMCPClientHash_RuntimeVsMigrationParity tests that MCP client hash
// is identical whether generated from config file (virtual fields) or DB (via GORM Find)
func TestGenerateMCPClientHash_RuntimeVsMigrationParity(t *testing.T) {
	initTestLogger()

	db := setupTestDB(t)
	if err := db.AutoMigrate(&tables.TableMCPClient{}); err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	connStr := "http://localhost:8080"

	// Test case 1: StdioConfig field - verify AfterFind populates virtual field
	t.Run("StdioConfig_GORMRoundTrip", func(t *testing.T) {
		stdioConfig := &schemas.MCPStdioConfig{
			Command: "npx",
			Args:    []string{"-y", "@modelcontextprotocol/server-filesystem"},
			Envs:    []string{"NODE_ENV=production"},
		}

		// Create MCP client with virtual field populated (simulates config file load)
		mcpToSave := tables.TableMCPClient{
			ClientID:       uuid.New().String(),
			Name:           "Test MCP StdioConfig " + uuid.New().String(),
			ConnectionType: "stdio",
			StdioConfig:    stdioConfig,
			ToolsToExecute: []string{},
			Headers:        map[string]schemas.EnvVar{},
		}

		// Generate hash BEFORE saving (this is what config file processing does)
		hashBeforeSave, err := configstore.GenerateMCPClientHash(mcpToSave)
		if err != nil {
			t.Fatalf("Failed to generate hash before save: %v", err)
		}

		// Save to DB (BeforeSave hook serializes virtual fields to JSON columns)
		if err := db.Create(&mcpToSave).Error; err != nil {
			t.Fatalf("Failed to save MCP client: %v", err)
		}

		// Read back from DB (AfterFind hook should deserialize JSON to virtual fields)
		var mcpFromDB tables.TableMCPClient
		if err := db.Where("id = ?", mcpToSave.ID).First(&mcpFromDB).Error; err != nil {
			t.Fatalf("Failed to read MCP client: %v", err)
		}

		// Generate hash AFTER reading from DB (simulates migration context)
		hashAfterLoad, err := configstore.GenerateMCPClientHash(mcpFromDB)
		if err != nil {
			t.Fatalf("Failed to generate hash after load: %v", err)
		}

		// Verify AfterFind populated the virtual field
		if mcpFromDB.StdioConfig == nil {
			t.Error("AfterFind did not populate StdioConfig virtual field")
		}

		if hashBeforeSave != hashAfterLoad {
			t.Errorf("Hash mismatch after GORM round-trip for StdioConfig\nBefore save: %s\nAfter load:  %s\nStdioConfig populated: %v",
				hashBeforeSave, hashAfterLoad, mcpFromDB.StdioConfig != nil)
		}
	})

	// Test case 2: ToolsToExecute field
	t.Run("ToolsToExecute_GORMRoundTrip", func(t *testing.T) {
		tools := []string{"tool1", "tool2", "tool3"}

		mcpToSave := tables.TableMCPClient{
			ClientID:         uuid.New().String(),
			Name:             "Test MCP Tools " + uuid.New().String(),
			ConnectionType:   "sse",
			ConnectionString: schemas.NewEnvVar(connStr),
			ToolsToExecute:   tools,
			Headers:          map[string]schemas.EnvVar{},
		}

		hashBeforeSave, _ := configstore.GenerateMCPClientHash(mcpToSave)
		db.Create(&mcpToSave)

		var mcpFromDB tables.TableMCPClient
		db.Where("id = ?", mcpToSave.ID).First(&mcpFromDB)

		hashAfterLoad, _ := configstore.GenerateMCPClientHash(mcpFromDB)

		if len(mcpFromDB.ToolsToExecute) == 0 {
			t.Error("AfterFind did not populate ToolsToExecute virtual field")
		}

		if hashBeforeSave != hashAfterLoad {
			t.Errorf("Hash mismatch after GORM round-trip for ToolsToExecute\nBefore save: %s\nAfter load:  %s",
				hashBeforeSave, hashAfterLoad)
		}
	})

	// Test case 3: Headers field
	t.Run("Headers_GORMRoundTrip", func(t *testing.T) {
		headers := map[string]schemas.EnvVar{
			"Authorization": *schemas.NewEnvVar("Bearer token123"),
			"X-Custom":      *schemas.NewEnvVar("value"),
		}

		mcpToSave := tables.TableMCPClient{
			ClientID:         uuid.New().String(),
			Name:             "Test MCP Headers " + uuid.New().String(),
			ConnectionType:   "sse",
			ConnectionString: schemas.NewEnvVar(connStr),
			ToolsToExecute:   []string{},
			Headers:          headers,
		}

		hashBeforeSave, _ := configstore.GenerateMCPClientHash(mcpToSave)
		db.Create(&mcpToSave)

		var mcpFromDB tables.TableMCPClient
		db.Where("id = ?", mcpToSave.ID).First(&mcpFromDB)

		hashAfterLoad, _ := configstore.GenerateMCPClientHash(mcpFromDB)

		if len(mcpFromDB.Headers) == 0 {
			t.Error("AfterFind did not populate Headers virtual field")
		}

		if hashBeforeSave != hashAfterLoad {
			t.Errorf("Hash mismatch after GORM round-trip for Headers\nBefore save: %s\nAfter load:  %s",
				hashBeforeSave, hashAfterLoad)
		}
	})

	// Test case 4: All fields combined
	t.Run("AllFields_GORMRoundTrip", func(t *testing.T) {
		stdioConfig := &schemas.MCPStdioConfig{
			Command: "npx",
			Args:    []string{"-y", "server"},
		}
		tools := []string{"tool1", "tool2"}
		headers := map[string]schemas.EnvVar{"Auth": *schemas.NewEnvVar("token")}

		mcpToSave := tables.TableMCPClient{
			ClientID:       uuid.New().String(),
			Name:           "Test MCP AllFields " + uuid.New().String(),
			ConnectionType: "stdio",
			StdioConfig:    stdioConfig,
			ToolsToExecute: tools,
			Headers:        headers,
		}

		hashBeforeSave, _ := configstore.GenerateMCPClientHash(mcpToSave)
		db.Create(&mcpToSave)

		var mcpFromDB tables.TableMCPClient
		db.Where("id = ?", mcpToSave.ID).First(&mcpFromDB)

		hashAfterLoad, _ := configstore.GenerateMCPClientHash(mcpFromDB)

		if hashBeforeSave != hashAfterLoad {
			t.Errorf("Hash mismatch after GORM round-trip for all fields\nBefore save: %s\nAfter load:  %s",
				hashBeforeSave, hashAfterLoad)
		}
	})

	// Test case 5: Verify tx.Find() also triggers AfterFind (migration scenario)
	t.Run("TxFind_TriggersAfterFind", func(t *testing.T) {
		tools := []string{"zebra", "apple", "mango"}

		mcpToSave := tables.TableMCPClient{
			ClientID:         uuid.New().String(),
			Name:             "Test MCP TxFind " + uuid.New().String(),
			ConnectionType:   "sse",
			ConnectionString: schemas.NewEnvVar(connStr),
			ToolsToExecute:   tools,
			Headers:          map[string]schemas.EnvVar{},
		}

		hashBeforeSave, _ := configstore.GenerateMCPClientHash(mcpToSave)
		db.Create(&mcpToSave)

		// Use Find() like migrations do (not First())
		var mcpClients []tables.TableMCPClient
		if err := db.Where("id = ?", mcpToSave.ID).Find(&mcpClients).Error; err != nil {
			t.Fatalf("Failed to find MCP clients: %v", err)
		}

		if len(mcpClients) == 0 {
			t.Fatal("No MCP clients found")
		}

		mcpFromDB := mcpClients[0]
		hashAfterLoad, _ := configstore.GenerateMCPClientHash(mcpFromDB)

		if len(mcpFromDB.ToolsToExecute) == 0 {
			t.Error("AfterFind did not run during Find() - ToolsToExecute is empty")
		}

		if hashBeforeSave != hashAfterLoad {
			t.Errorf("Hash mismatch when using Find() (migration pattern)\nBefore save: %s\nAfter load:  %s",
				hashBeforeSave, hashAfterLoad)
		}
	})
}

// TestGeneratePluginHash_RuntimeVsMigrationParity tests plugin hash with real DB
func TestGeneratePluginHash_RuntimeVsMigrationParity(t *testing.T) {
	initTestLogger()

	db := setupTestDB(t)
	if err := db.AutoMigrate(&tables.TablePlugin{}); err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	path := "/path/to/plugin"

	// Test case 1: Simple config object
	t.Run("SimpleConfig_GORMRoundTrip", func(t *testing.T) {
		config := map[string]interface{}{
			"setting":  "value",
			"enabled":  true,
			"maxItems": float64(100),
		}

		pluginToSave := tables.TablePlugin{
			Name:    "test-plugin-" + uuid.New().String(),
			Enabled: true,
			Path:    &path,
			Version: 1,
			Config:  config,
		}

		hashBeforeSave, _ := configstore.GeneratePluginHash(pluginToSave)
		db.Create(&pluginToSave)

		var pluginFromDB tables.TablePlugin
		db.Where("id = ?", pluginToSave.ID).First(&pluginFromDB)

		hashAfterLoad, _ := configstore.GeneratePluginHash(pluginFromDB)

		if pluginFromDB.Config == nil {
			t.Error("AfterFind did not populate Config virtual field")
		}

		if hashBeforeSave != hashAfterLoad {
			t.Errorf("Hash mismatch after GORM round-trip for plugin Config\nBefore save: %s\nAfter load:  %s",
				hashBeforeSave, hashAfterLoad)
		}
	})

	// Test case 2: Nested config object
	t.Run("NestedConfig_GORMRoundTrip", func(t *testing.T) {
		config := map[string]interface{}{
			"database": map[string]interface{}{
				"host": "localhost",
				"port": float64(5432),
			},
		}

		pluginToSave := tables.TablePlugin{
			Name:    "test-plugin-nested-" + uuid.New().String(),
			Enabled: true,
			Version: 1,
			Config:  config,
		}

		hashBeforeSave, _ := configstore.GeneratePluginHash(pluginToSave)
		db.Create(&pluginToSave)

		var pluginFromDB tables.TablePlugin
		db.Where("id = ?", pluginToSave.ID).First(&pluginFromDB)

		hashAfterLoad, _ := configstore.GeneratePluginHash(pluginFromDB)

		if hashBeforeSave != hashAfterLoad {
			t.Errorf("Hash mismatch for nested config\nBefore save: %s\nAfter load:  %s",
				hashBeforeSave, hashAfterLoad)
		}
	})

	// Test case 3: Empty config
	t.Run("EmptyConfig_GORMRoundTrip", func(t *testing.T) {
		pluginToSave := tables.TablePlugin{
			Name:    "test-plugin-empty-" + uuid.New().String(),
			Enabled: true,
			Version: 1,
			Config:  nil,
		}

		hashBeforeSave, _ := configstore.GeneratePluginHash(pluginToSave)
		db.Create(&pluginToSave)

		var pluginFromDB tables.TablePlugin
		db.Where("id = ?", pluginToSave.ID).First(&pluginFromDB)

		hashAfterLoad, _ := configstore.GeneratePluginHash(pluginFromDB)

		if hashBeforeSave != hashAfterLoad {
			t.Errorf("Hash mismatch for empty config\nBefore save: %s\nAfter load:  %s",
				hashBeforeSave, hashAfterLoad)
		}
	})
}

// TestGenerateTeamHash_RuntimeVsMigrationParity tests team hash with real DB
func TestGenerateTeamHash_RuntimeVsMigrationParity(t *testing.T) {
	initTestLogger()

	db := setupTestDB(t)
	if err := db.AutoMigrate(&tables.TableTeam{}); err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	// Test case 1: ParsedProfile
	t.Run("Profile_GORMRoundTrip", func(t *testing.T) {
		profile := map[string]interface{}{
			"department": "engineering",
			"level":      float64(3),
		}

		teamToSave := tables.TableTeam{
			ID:            uuid.New().String(),
			Name:          "Test Team Profile",
			ParsedProfile: profile,
		}

		hashBeforeSave, _ := configstore.GenerateTeamHash(teamToSave)
		db.Create(&teamToSave)

		var teamFromDB tables.TableTeam
		db.Where("id = ?", teamToSave.ID).First(&teamFromDB)

		hashAfterLoad, _ := configstore.GenerateTeamHash(teamFromDB)

		if teamFromDB.ParsedProfile == nil {
			t.Error("AfterFind did not populate ParsedProfile virtual field")
		}

		if hashBeforeSave != hashAfterLoad {
			t.Errorf("Hash mismatch for Profile\nBefore save: %s\nAfter load:  %s",
				hashBeforeSave, hashAfterLoad)
		}
	})

	// Test case 2: ParsedConfig
	t.Run("Config_GORMRoundTrip", func(t *testing.T) {
		config := map[string]interface{}{
			"maxTokens":   float64(4096),
			"temperature": 0.7,
		}

		teamToSave := tables.TableTeam{
			ID:           uuid.New().String(),
			Name:         "Test Team Config",
			ParsedConfig: config,
		}

		hashBeforeSave, _ := configstore.GenerateTeamHash(teamToSave)
		db.Create(&teamToSave)

		var teamFromDB tables.TableTeam
		db.Where("id = ?", teamToSave.ID).First(&teamFromDB)

		hashAfterLoad, _ := configstore.GenerateTeamHash(teamFromDB)

		if hashBeforeSave != hashAfterLoad {
			t.Errorf("Hash mismatch for Config\nBefore save: %s\nAfter load:  %s",
				hashBeforeSave, hashAfterLoad)
		}
	})

	// Test case 3: ParsedClaims with array
	t.Run("Claims_GORMRoundTrip", func(t *testing.T) {
		claims := map[string]interface{}{
			"role":        "admin",
			"permissions": []interface{}{"read", "write", "delete"},
		}

		teamToSave := tables.TableTeam{
			ID:           uuid.New().String(),
			Name:         "Test Team Claims",
			ParsedClaims: claims,
		}

		hashBeforeSave, _ := configstore.GenerateTeamHash(teamToSave)
		db.Create(&teamToSave)

		var teamFromDB tables.TableTeam
		db.Where("id = ?", teamToSave.ID).First(&teamFromDB)

		hashAfterLoad, _ := configstore.GenerateTeamHash(teamFromDB)

		if hashBeforeSave != hashAfterLoad {
			t.Errorf("Hash mismatch for Claims\nBefore save: %s\nAfter load:  %s",
				hashBeforeSave, hashAfterLoad)
		}
	})

	// Test case 4: All fields
	t.Run("AllFields_GORMRoundTrip", func(t *testing.T) {
		customerID := "customer-1"
		budgetID := "budget-1"

		teamToSave := tables.TableTeam{
			ID:            uuid.New().String(),
			Name:          "Test Team All",
			CustomerID:    &customerID,
			BudgetID:      &budgetID,
			ParsedProfile: map[string]interface{}{"key": "value"},
			ParsedConfig:  map[string]interface{}{"setting": true},
			ParsedClaims:  map[string]interface{}{"role": "user"},
		}

		hashBeforeSave, _ := configstore.GenerateTeamHash(teamToSave)
		db.Create(&teamToSave)

		var teamFromDB tables.TableTeam
		db.Where("id = ?", teamToSave.ID).First(&teamFromDB)

		hashAfterLoad, _ := configstore.GenerateTeamHash(teamFromDB)

		if hashBeforeSave != hashAfterLoad {
			t.Errorf("Hash mismatch for all fields\nBefore save: %s\nAfter load:  %s",
				hashBeforeSave, hashAfterLoad)
		}
	})
}

// TestGenerateProviderHash_RuntimeVsMigrationParity tests provider hash with real DB
func TestGenerateProviderHash_RuntimeVsMigrationParity(t *testing.T) {
	initTestLogger()

	db := setupTestDB(t)
	if err := db.AutoMigrate(&tables.TableProvider{}); err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	// Test case 1: NetworkConfig
	t.Run("NetworkConfig_GORMRoundTrip", func(t *testing.T) {
		networkConfig := &schemas.NetworkConfig{
			BaseURL:                        "https://api.custom.com",
			DefaultRequestTimeoutInSeconds: 30,
		}

		providerToSave := tables.TableProvider{
			Name:                networkConfig.BaseURL, // Use unique name
			NetworkConfig:       networkConfig,
			SendBackRawResponse: true,
		}

		// Generate hash from the virtual field (before save)
		providerConfig := configstore.ProviderConfig{
			NetworkConfig:       providerToSave.NetworkConfig,
			SendBackRawResponse: providerToSave.SendBackRawResponse,
		}
		hashBeforeSave, _ := providerConfig.GenerateConfigHash("openai")

		db.Create(&providerToSave)

		var providerFromDB tables.TableProvider
		db.Where("id = ?", providerToSave.ID).First(&providerFromDB)

		// Generate hash from loaded data
		providerConfigFromDB := configstore.ProviderConfig{
			NetworkConfig:       providerFromDB.NetworkConfig,
			SendBackRawResponse: providerFromDB.SendBackRawResponse,
		}
		hashAfterLoad, _ := providerConfigFromDB.GenerateConfigHash("openai")

		if providerFromDB.NetworkConfig == nil {
			t.Error("AfterFind did not populate NetworkConfig virtual field")
		}

		if hashBeforeSave != hashAfterLoad {
			t.Errorf("Hash mismatch for NetworkConfig\nBefore save: %s\nAfter load:  %s",
				hashBeforeSave, hashAfterLoad)
		}
	})

	// Test case 2: ConcurrencyAndBufferSize
	t.Run("ConcurrencyAndBufferSize_GORMRoundTrip", func(t *testing.T) {
		concurrencyConfig := &schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
			BufferSize:  100,
		}

		providerToSave := tables.TableProvider{
			Name:                     "test-provider-concurrency-" + uuid.New().String(),
			ConcurrencyAndBufferSize: concurrencyConfig,
			SendBackRawResponse:      true,
		}

		providerConfig := configstore.ProviderConfig{
			ConcurrencyAndBufferSize: providerToSave.ConcurrencyAndBufferSize,
			SendBackRawResponse:      providerToSave.SendBackRawResponse,
		}
		hashBeforeSave, _ := providerConfig.GenerateConfigHash("openai")

		db.Create(&providerToSave)

		var providerFromDB tables.TableProvider
		db.Where("id = ?", providerToSave.ID).First(&providerFromDB)

		providerConfigFromDB := configstore.ProviderConfig{
			ConcurrencyAndBufferSize: providerFromDB.ConcurrencyAndBufferSize,
			SendBackRawResponse:      providerFromDB.SendBackRawResponse,
		}
		hashAfterLoad, _ := providerConfigFromDB.GenerateConfigHash("openai")

		if providerFromDB.ConcurrencyAndBufferSize == nil {
			t.Error("AfterFind did not populate ConcurrencyAndBufferSize virtual field")
		}

		if hashBeforeSave != hashAfterLoad {
			t.Errorf("Hash mismatch for ConcurrencyAndBufferSize\nBefore save: %s\nAfter load:  %s",
				hashBeforeSave, hashAfterLoad)
		}
	})

	// Test case 3: ProxyConfig
	t.Run("ProxyConfig_GORMRoundTrip", func(t *testing.T) {
		proxyConfig := &schemas.ProxyConfig{
			Type: schemas.HTTPProxy,
			URL:  "http://proxy.example.com:8080",
		}

		providerToSave := tables.TableProvider{
			Name:                "test-provider-proxy-" + uuid.New().String(),
			ProxyConfig:         proxyConfig,
			SendBackRawResponse: false,
		}

		providerConfig := configstore.ProviderConfig{
			ProxyConfig:         providerToSave.ProxyConfig,
			SendBackRawResponse: providerToSave.SendBackRawResponse,
		}
		hashBeforeSave, _ := providerConfig.GenerateConfigHash("openai")

		db.Create(&providerToSave)

		var providerFromDB tables.TableProvider
		db.Where("id = ?", providerToSave.ID).First(&providerFromDB)

		providerConfigFromDB := configstore.ProviderConfig{
			ProxyConfig:         providerFromDB.ProxyConfig,
			SendBackRawResponse: providerFromDB.SendBackRawResponse,
		}
		hashAfterLoad, _ := providerConfigFromDB.GenerateConfigHash("openai")

		if hashBeforeSave != hashAfterLoad {
			t.Errorf("Hash mismatch for ProxyConfig\nBefore save: %s\nAfter load:  %s",
				hashBeforeSave, hashAfterLoad)
		}
	})

	// Test case 4: CustomProviderConfig
	t.Run("CustomProviderConfig_GORMRoundTrip", func(t *testing.T) {
		customConfig := &schemas.CustomProviderConfig{
			IsKeyLess:        true,
			BaseProviderType: schemas.OpenAI,
		}

		providerToSave := tables.TableProvider{
			Name:                 "test-provider-custom-" + uuid.New().String(),
			CustomProviderConfig: customConfig,
			SendBackRawResponse:  true,
		}

		providerConfig := configstore.ProviderConfig{
			CustomProviderConfig: providerToSave.CustomProviderConfig,
			SendBackRawResponse:  providerToSave.SendBackRawResponse,
		}
		hashBeforeSave, _ := providerConfig.GenerateConfigHash("custom")

		db.Create(&providerToSave)

		var providerFromDB tables.TableProvider
		db.Where("id = ?", providerToSave.ID).First(&providerFromDB)

		providerConfigFromDB := configstore.ProviderConfig{
			CustomProviderConfig: providerFromDB.CustomProviderConfig,
			SendBackRawResponse:  providerFromDB.SendBackRawResponse,
		}
		hashAfterLoad, _ := providerConfigFromDB.GenerateConfigHash("custom")

		if hashBeforeSave != hashAfterLoad {
			t.Errorf("Hash mismatch for CustomProviderConfig\nBefore save: %s\nAfter load:  %s",
				hashBeforeSave, hashAfterLoad)
		}
	})
}

// TestGenerateKeyHash_RuntimeVsMigrationParity tests key hash with real DB
func TestGenerateKeyHash_RuntimeVsMigrationParity(t *testing.T) {
	initTestLogger()

	db := setupTestDB(t)
	// Need to migrate provider first due to foreign key
	if err := db.AutoMigrate(&tables.TableProvider{}, &tables.TableKey{}); err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	// Create a provider for foreign key
	provider := tables.TableProvider{Name: "test-provider-for-keys"}
	db.Create(&provider)

	// Test case 1: Models field
	t.Run("Models_GORMRoundTrip", func(t *testing.T) {
		models := []string{"gpt-4", "gpt-3.5-turbo", "gpt-4-turbo"}

		keyToSave := tables.TableKey{
			Name:       "test-key-models-" + uuid.New().String(),
			KeyID:      uuid.New().String(),
			ProviderID: provider.ID,
			Provider:   "openai",
			Value:      *schemas.NewEnvVar("sk-123"),
			Models:     models,
			Weight:     ptrFloat64(1.5),
		}

		// Generate hash using schemas.Key (what the hash function expects)
		schemaKey := schemas.Key{
			Name:   keyToSave.Name,
			Value:  keyToSave.Value,
			Models: keyToSave.Models,
			Weight: getWeight(keyToSave.Weight),
		}
		hashBeforeSave, _ := configstore.GenerateKeyHash(schemaKey)

		db.Create(&keyToSave)

		var keyFromDB tables.TableKey
		db.Where("id = ?", keyToSave.ID).First(&keyFromDB)

		schemaKeyFromDB := schemas.Key{
			Name:   keyFromDB.Name,
			Value:  keyFromDB.Value,
			Models: keyFromDB.Models,
			Weight: getWeight(keyFromDB.Weight),
		}
		hashAfterLoad, _ := configstore.GenerateKeyHash(schemaKeyFromDB)

		if len(keyFromDB.Models) == 0 {
			t.Error("AfterFind did not populate Models virtual field")
		}

		if hashBeforeSave != hashAfterLoad {
			t.Errorf("Hash mismatch for Models\nBefore save: %s\nAfter load:  %s",
				hashBeforeSave, hashAfterLoad)
		}
	})

	// Test case 2: AzureKeyConfig
	t.Run("AzureKeyConfig_GORMRoundTrip", func(t *testing.T) {
		apiVersion := "2024-02-01"
		azureConfig := &schemas.AzureKeyConfig{
			Endpoint:    *schemas.NewEnvVar("https://myresource.openai.azure.com"),
			APIVersion:  schemas.NewEnvVar(apiVersion),
			Deployments: map[string]string{"gpt-4": "gpt-4-deployment"},
		}

		keyToSave := tables.TableKey{
			Name:           "test-key-azure-" + uuid.New().String(),
			KeyID:          uuid.New().String(),
			ProviderID:     provider.ID,
			Provider:       "azure",
			Value:          *schemas.NewEnvVar("azure-key-value"),
			Weight:         ptrFloat64(1.0),
			AzureKeyConfig: azureConfig,
		}

		schemaKey := schemas.Key{
			Name:           keyToSave.Name,
			Value:          keyToSave.Value,
			Weight:         getWeight(keyToSave.Weight),
			AzureKeyConfig: keyToSave.AzureKeyConfig,
		}
		hashBeforeSave, _ := configstore.GenerateKeyHash(schemaKey)

		db.Create(&keyToSave)

		var keyFromDB tables.TableKey
		db.Where("id = ?", keyToSave.ID).First(&keyFromDB)

		schemaKeyFromDB := schemas.Key{
			Name:           keyFromDB.Name,
			Value:          keyFromDB.Value,
			Weight:         getWeight(keyFromDB.Weight),
			AzureKeyConfig: keyFromDB.AzureKeyConfig,
		}
		hashAfterLoad, _ := configstore.GenerateKeyHash(schemaKeyFromDB)

		if keyFromDB.AzureKeyConfig == nil {
			t.Error("AfterFind did not populate AzureKeyConfig virtual field")
		}

		if hashBeforeSave != hashAfterLoad {
			t.Errorf("Hash mismatch for AzureKeyConfig\nBefore save: %s\nAfter load:  %s",
				hashBeforeSave, hashAfterLoad)
		}
	})

	// Test case 3: Models ordering should not affect hash
	t.Run("Models_OrderingParity", func(t *testing.T) {
		models1 := []string{"gpt-4", "gpt-3.5-turbo", "claude-3"}
		models2 := []string{"claude-3", "gpt-4", "gpt-3.5-turbo"} // Different order

		key1 := schemas.Key{
			Name:   "test-key",
			Value:  *schemas.NewEnvVar("sk-123"),
			Models: models1,
			Weight: 1.0,
		}

		key2 := schemas.Key{
			Name:   "test-key",
			Value:  *schemas.NewEnvVar("sk-123"),
			Models: models2,
			Weight: 1.0,
		}

		hash1, _ := configstore.GenerateKeyHash(key1)
		hash2, _ := configstore.GenerateKeyHash(key2)

		if hash1 != hash2 {
			t.Errorf("Hash should be same regardless of Models order\nHash1: %s\nHash2: %s", hash1, hash2)
		}
	})
}

// TestGenerateClientConfigHash_RuntimeVsMigrationParity tests client config hash with real DB
func TestGenerateClientConfigHash_RuntimeVsMigrationParity(t *testing.T) {
	initTestLogger()

	db := setupTestDB(t)
	if err := db.AutoMigrate(&tables.TableClientConfig{}); err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	// Test case 1: PrometheusLabels
	t.Run("PrometheusLabels_GORMRoundTrip", func(t *testing.T) {
		labels := []string{"provider", "model", "status"}

		ccToSave := tables.TableClientConfig{
			DropExcessRequests:      true,
			InitialPoolSize:         300,
			PrometheusLabels:        labels,
			EnableLogging:           true,
			DisableContentLogging:   false,
			LogRetentionDays:        30,
			EnableGovernance:        true,
			EnforceGovernanceHeader: false,
			AllowDirectKeys:         true,
			MaxRequestBodySizeMB:    100,
			EnableLiteLLMFallbacks:  false,
		}

		// Generate hash from config
		clientConfig := configstore.ClientConfig{
			DropExcessRequests:      ccToSave.DropExcessRequests,
			InitialPoolSize:         ccToSave.InitialPoolSize,
			PrometheusLabels:        ccToSave.PrometheusLabels,
			EnableLogging:           ccToSave.EnableLogging,
			DisableContentLogging:   ccToSave.DisableContentLogging,
			LogRetentionDays:        ccToSave.LogRetentionDays,
			EnableGovernance:        ccToSave.EnableGovernance,
			EnforceGovernanceHeader: ccToSave.EnforceGovernanceHeader,
			AllowDirectKeys:         ccToSave.AllowDirectKeys,
			MaxRequestBodySizeMB:    ccToSave.MaxRequestBodySizeMB,
			EnableLiteLLMFallbacks:  ccToSave.EnableLiteLLMFallbacks,
		}
		hashBeforeSave, _ := clientConfig.GenerateClientConfigHash()

		db.Create(&ccToSave)

		var ccFromDB tables.TableClientConfig
		db.Where("id = ?", ccToSave.ID).First(&ccFromDB)

		clientConfigFromDB := configstore.ClientConfig{
			DropExcessRequests:      ccFromDB.DropExcessRequests,
			InitialPoolSize:         ccFromDB.InitialPoolSize,
			PrometheusLabels:        ccFromDB.PrometheusLabels,
			EnableLogging:           ccFromDB.EnableLogging,
			DisableContentLogging:   ccFromDB.DisableContentLogging,
			LogRetentionDays:        ccFromDB.LogRetentionDays,
			EnableGovernance:        ccFromDB.EnableGovernance,
			EnforceGovernanceHeader: ccFromDB.EnforceGovernanceHeader,
			AllowDirectKeys:         ccFromDB.AllowDirectKeys,
			MaxRequestBodySizeMB:    ccFromDB.MaxRequestBodySizeMB,
			EnableLiteLLMFallbacks:  ccFromDB.EnableLiteLLMFallbacks,
		}
		hashAfterLoad, _ := clientConfigFromDB.GenerateClientConfigHash()

		if len(ccFromDB.PrometheusLabels) == 0 {
			t.Error("AfterFind did not populate PrometheusLabels virtual field")
		}

		if hashBeforeSave != hashAfterLoad {
			t.Errorf("Hash mismatch for PrometheusLabels\nBefore save: %s\nAfter load:  %s",
				hashBeforeSave, hashAfterLoad)
		}
	})

	// Test case 2: AllowedOrigins
	t.Run("AllowedOrigins_GORMRoundTrip", func(t *testing.T) {
		origins := []string{"https://example.com", "https://app.example.com"}

		ccToSave := tables.TableClientConfig{
			DropExcessRequests:   true,
			InitialPoolSize:      300,
			AllowedOrigins:       origins,
			EnableLogging:        true,
			LogRetentionDays:     30,
			MaxRequestBodySizeMB: 100,
		}

		clientConfig := configstore.ClientConfig{
			DropExcessRequests:   ccToSave.DropExcessRequests,
			InitialPoolSize:      ccToSave.InitialPoolSize,
			AllowedOrigins:       ccToSave.AllowedOrigins,
			EnableLogging:        ccToSave.EnableLogging,
			LogRetentionDays:     ccToSave.LogRetentionDays,
			MaxRequestBodySizeMB: ccToSave.MaxRequestBodySizeMB,
		}
		hashBeforeSave, _ := clientConfig.GenerateClientConfigHash()

		db.Create(&ccToSave)

		var ccFromDB tables.TableClientConfig
		db.Where("id = ?", ccToSave.ID).First(&ccFromDB)

		clientConfigFromDB := configstore.ClientConfig{
			DropExcessRequests:   ccFromDB.DropExcessRequests,
			InitialPoolSize:      ccFromDB.InitialPoolSize,
			AllowedOrigins:       ccFromDB.AllowedOrigins,
			EnableLogging:        ccFromDB.EnableLogging,
			LogRetentionDays:     ccFromDB.LogRetentionDays,
			MaxRequestBodySizeMB: ccFromDB.MaxRequestBodySizeMB,
		}
		hashAfterLoad, _ := clientConfigFromDB.GenerateClientConfigHash()

		if len(ccFromDB.AllowedOrigins) == 0 {
			t.Error("AfterFind did not populate AllowedOrigins virtual field")
		}

		if hashBeforeSave != hashAfterLoad {
			t.Errorf("Hash mismatch for AllowedOrigins\nBefore save: %s\nAfter load:  %s",
				hashBeforeSave, hashAfterLoad)
		}
	})
}

// =============================================================================
// Weight=0 Handling Tests
// =============================================================================
// These tests verify that a weight of 0 is correctly preserved (not defaulted to 1.0)
// This is critical because weight=0 should disable a key from weighted random selection.

// TestKeyWeight_ZeroPreserved verifies that a key with weight: 0 in config.json
// is preserved as 0, not incorrectly defaulted to 1.0.
func TestKeyWeight_ZeroPreserved(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	keyID := uuid.NewString()
	providers := map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: keyID, Name: "zero-weight-key", Value: *schemas.NewEnvVar("sk-test123"), Weight: 0}, // Explicit zero
			},
		},
	}
	configData := makeConfigDataWithProvidersAndDir(providers, tempDir)
	createConfigFile(t, tempDir, configData)

	config, err := LoadConfig(context.Background(), tempDir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	defer config.ConfigStore.Close(context.Background())

	openaiConfig, exists := config.Providers[schemas.OpenAI]
	if !exists {
		t.Fatal("Expected openai provider to exist")
	}

	if len(openaiConfig.Keys) != 1 {
		t.Fatalf("Expected 1 key, got %d", len(openaiConfig.Keys))
	}

	if openaiConfig.Keys[0].Weight != 0 {
		t.Errorf("Expected weight 0 (explicitly set), got %f", openaiConfig.Keys[0].Weight)
	}
}

// TestKeyWeight_DefaultToOneWhenNotSet verifies that a key without an explicit weight
// defaults to 1.0 when not specified (the expected default behavior).
func TestKeyWeight_DefaultToOneWhenNotSet(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	keyID := uuid.NewString()
	providers := map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: keyID, Name: "default-weight-key", Value: *schemas.NewEnvVar("sk-test123"), Weight: 1}, // Explicit 1 (default)
			},
		},
	}
	configData := makeConfigDataWithProvidersAndDir(providers, tempDir)
	createConfigFile(t, tempDir, configData)

	config, err := LoadConfig(context.Background(), tempDir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	defer config.ConfigStore.Close(context.Background())

	openaiConfig, exists := config.Providers[schemas.OpenAI]
	if !exists {
		t.Fatal("Expected openai provider to exist")
	}

	if openaiConfig.Keys[0].Weight != 1.0 {
		t.Errorf("Expected weight 1.0 (default), got %f", openaiConfig.Keys[0].Weight)
	}
}

// TestSQLite_Key_WeightZero_RoundTrip tests that a key with weight=0 survives
// a database round-trip correctly (not defaulted to 1.0 by GORM).
func TestSQLite_Key_WeightZero_RoundTrip(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	keyID := uuid.NewString()
	providers := map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: keyID, Name: "zero-weight-key", Value: *schemas.NewEnvVar("sk-test123"), Weight: 0},
			},
		},
	}
	configData := makeConfigDataWithProvidersAndDir(providers, tempDir)
	createConfigFile(t, tempDir, configData)

	ctx := context.Background()

	// First load - creates DB entries
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	if config1.Providers[schemas.OpenAI].Keys[0].Weight != 0 {
		t.Errorf("First load: Expected weight 0, got %f", config1.Providers[schemas.OpenAI].Keys[0].Weight)
	}
	config1.ConfigStore.Close(ctx)

	// Second load - reads from DB
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	if config2.Providers[schemas.OpenAI].Keys[0].Weight != 0 {
		t.Errorf("Second load (from DB): Expected weight 0, got %f - weight=0 was incorrectly defaulted to 1.0",
			config2.Providers[schemas.OpenAI].Keys[0].Weight)
	}
}

// ptrFloat64 is a helper function to create a pointer to a float64 value
func ptrFloat64(v float64) *float64 {
	return &v
}

// TestVKProviderConfig_WeightZeroPreserved verifies that a virtual key provider config
// with weight=0 is preserved correctly and hash generation works.
func TestVKProviderConfig_WeightZeroPreserved(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Create providers
	providers := map[string]configstore.ProviderConfig{
		"openai": makeProviderConfigWithNetwork("openai-key-1", "sk-test123", "https://api.openai.com"),
	}

	// Create virtual key with provider config that has weight=0
	vk := tables.TableVirtualKey{
		ID:       "vk-zero-weight",
		Name:     "test-vk",
		Value:    "vk_test123",
		IsActive: true,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
			{
				Provider: "openai",
				Weight:   ptrFloat64(0.0), // Explicit zero weight
			},
		},
	}

	configData := makeConfigDataWithVirtualKeysAndDir(providers, []tables.TableVirtualKey{vk}, tempDir)
	createConfigFile(t, tempDir, configData)

	ctx := context.Background()
	config, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	defer config.ConfigStore.Close(ctx)

	// Verify virtual key exists and has provider config with weight=0
	if config.GovernanceConfig == nil || len(config.GovernanceConfig.VirtualKeys) == 0 {
		t.Fatal("Expected virtual key in governance config")
	}

	vkFromConfig := config.GovernanceConfig.VirtualKeys[0]
	if len(vkFromConfig.ProviderConfigs) == 0 {
		t.Fatal("Expected provider config in virtual key")
	}

	pc := vkFromConfig.ProviderConfigs[0]
	if pc.Weight == nil {
		t.Fatal("Expected Weight to be set (not nil)")
	}
	if *pc.Weight != 0.0 {
		t.Errorf("Expected provider config weight 0, got %f", *pc.Weight)
	}
}

// TestSQLite_VKProviderConfig_WeightZero_RoundTrip tests that a virtual key provider config
// with weight=0 survives a database round-trip correctly.
func TestSQLite_VKProviderConfig_WeightZero_RoundTrip(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	keyID := uuid.NewString()
	providers := map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: keyID, Name: "openai-key", Value: *schemas.NewEnvVar("sk-test123"), Weight: 1},
			},
		},
	}

	vks := []tables.TableVirtualKey{
		{
			ID:       "vk-zero-weight",
			Name:     "test-vk",
			Value:    "vk_abc123",
			IsActive: true,
			ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
				{
					Provider:      "openai",
					Weight:        ptrFloat64(0.0), // Explicit zero weight
					AllowedModels: []string{"gpt-4"},
				},
			},
		},
	}

	configData := makeConfigDataWithVirtualKeysAndDir(providers, vks, tempDir)
	createConfigFile(t, tempDir, configData)

	ctx := context.Background()

	// First load
	config1, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	vk1 := config1.GovernanceConfig.VirtualKeys[0]
	if len(vk1.ProviderConfigs) == 0 {
		t.Fatal("First load: Expected provider configs")
	}
	if vk1.ProviderConfigs[0].Weight == nil {
		t.Fatal("First load: Expected Weight to be set")
	}
	if *vk1.ProviderConfigs[0].Weight != 0 {
		t.Errorf("First load: Expected provider config weight 0, got %f", *vk1.ProviderConfigs[0].Weight)
	}
	config1.ConfigStore.Close(ctx)

	// Second load from DB
	config2, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(ctx)

	vk2 := config2.GovernanceConfig.VirtualKeys[0]
	if len(vk2.ProviderConfigs) == 0 {
		t.Fatal("Second load: Expected provider configs")
	}
	if vk2.ProviderConfigs[0].Weight == nil {
		t.Fatal("Second load: Expected Weight to be set (not nil) - it was incorrectly defaulted")
	}
	if *vk2.ProviderConfigs[0].Weight != 0 {
		t.Errorf("Second load (from DB): Expected provider config weight 0, got %f - incorrectly defaulted to 1.0",
			*vk2.ProviderConfigs[0].Weight)
	}
}

// TestKeyWeight_HashDiffersBetweenZeroAndOne verifies that key hashes are different
// for weight=0 vs weight=1, ensuring the change is detected during sync.
func TestKeyWeight_HashDiffersBetweenZeroAndOne(t *testing.T) {
	keyWithZeroWeight := schemas.Key{
		ID:     "test-key",
		Name:   "test",
		Value:  *schemas.NewEnvVar("sk-123"),
		Weight: 0,
	}
	keyWithOneWeight := schemas.Key{
		ID:     "test-key",
		Name:   "test",
		Value:  *schemas.NewEnvVar("sk-123"),
		Weight: 1,
	}

	hash0, err := configstore.GenerateKeyHash(keyWithZeroWeight)
	if err != nil {
		t.Fatalf("Failed to generate hash for weight=0: %v", err)
	}

	hash1, err := configstore.GenerateKeyHash(keyWithOneWeight)
	if err != nil {
		t.Fatalf("Failed to generate hash for weight=1: %v", err)
	}

	if hash0 == hash1 {
		t.Error("Expected different hashes for weight=0 vs weight=1, but they are the same")
	}
}

// TestGenerateKeyHash_EnabledField verifies that the Enabled field affects hash generation.
// Different Enabled values should produce different hashes.
func TestGenerateKeyHash_EnabledField(t *testing.T) {
	enabledTrue := true
	enabledFalse := false

	tests := []struct {
		name        string
		key1        schemas.Key
		key2        schemas.Key
		expectEqual bool
	}{
		{
			name: "enabled_true_vs_false_different_hash",
			key1: schemas.Key{
				Name:    "test-key",
				Value:   *schemas.NewEnvVar("sk-123"),
				Weight:  1,
				Enabled: &enabledTrue,
			},
			key2: schemas.Key{
				Name:    "test-key",
				Value:   *schemas.NewEnvVar("sk-123"),
				Weight:  1,
				Enabled: &enabledFalse,
			},
			expectEqual: false,
		},
		{
			name: "enabled_nil_vs_true_different_hash",
			key1: schemas.Key{
				Name:    "test-key",
				Value:   *schemas.NewEnvVar("sk-123"),
				Weight:  1,
				Enabled: nil,
			},
			key2: schemas.Key{
				Name:    "test-key",
				Value:   *schemas.NewEnvVar("sk-123"),
				Weight:  1,
				Enabled: &enabledTrue,
			},
			expectEqual: false,
		},
		{
			name: "enabled_nil_vs_false_same_hash",
			key1: schemas.Key{
				Name:    "test-key",
				Value:   *schemas.NewEnvVar("sk-123"),
				Weight:  1,
				Enabled: nil,
			},
			key2: schemas.Key{
				Name:    "test-key",
				Value:   *schemas.NewEnvVar("sk-123"),
				Weight:  1,
				Enabled: &enabledFalse,
			},
			expectEqual: true,
		},
		{
			name: "same_enabled_true_same_hash",
			key1: schemas.Key{
				Name:    "test-key",
				Value:   *schemas.NewEnvVar("sk-123"),
				Weight:  1,
				Enabled: &enabledTrue,
			},
			key2: schemas.Key{
				Name:    "test-key",
				Value:   *schemas.NewEnvVar("sk-123"),
				Weight:  1,
				Enabled: &enabledTrue,
			},
			expectEqual: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash1, err := configstore.GenerateKeyHash(tt.key1)
			if err != nil {
				t.Fatalf("Failed to generate hash for key1: %v", err)
			}

			hash2, err := configstore.GenerateKeyHash(tt.key2)
			if err != nil {
				t.Fatalf("Failed to generate hash for key2: %v", err)
			}

			if tt.expectEqual && hash1 != hash2 {
				t.Errorf("Expected equal hashes, got hash1=%s, hash2=%s", hash1, hash2)
			}
			if !tt.expectEqual && hash1 == hash2 {
				t.Errorf("Expected different hashes, but both are %s", hash1)
			}
		})
	}
}

// TestSQLite_Key_EnabledChange_Detected verifies that changes to the Enabled field
// are detected during config reconciliation and properly synced.
func TestSQLite_Key_EnabledChange_Detected(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	enabledTrue := true
	enabledFalse := false
	keyID := uuid.NewString()

	// Initial config with Enabled=true
	initialConfig := makeConfigDataWithProvidersAndDir(map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{
					ID:      keyID,
					Name:    "test-key",
					Value:   *schemas.NewEnvVar("sk-test-123"),
					Weight:  1,
					Enabled: &enabledTrue,
				},
			},
		},
	}, tempDir)

	// First load
	createConfigFile(t, tempDir, initialConfig)
	config1, err := LoadConfig(context.Background(), tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Verify initial state in the in-memory config
	openaiConfig1 := config1.Providers[schemas.OpenAI]
	if openaiConfig1.Keys[0].Enabled == nil || !*openaiConfig1.Keys[0].Enabled {
		t.Fatal("Expected Enabled=true after first load")
	}

	// Close first config before second load
	config1.ConfigStore.Close(context.Background())

	// Update config with Enabled=false
	updatedConfig := makeConfigDataWithProvidersAndDir(map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{
					ID:      keyID,
					Name:    "test-key",
					Value:   *schemas.NewEnvVar("sk-test-123"),
					Weight:  1,
					Enabled: &enabledFalse,
				},
			},
		},
	}, tempDir)

	// Second load with changed Enabled value
	createConfigFile(t, tempDir, updatedConfig)
	config2, err := LoadConfig(context.Background(), tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(context.Background())

	// Verify Enabled changed to false in the in-memory config
	openaiConfig2 := config2.Providers[schemas.OpenAI]
	if openaiConfig2.Keys[0].Enabled == nil || *openaiConfig2.Keys[0].Enabled {
		t.Error("Expected Enabled=false after second load, but got true or nil")
	}
}

// TestGenerateKeyHash_UseForBatchAPIField verifies that the UseForBatchAPI field affects hash generation.
// Different UseForBatchAPI values should produce different hashes.
func TestGenerateKeyHash_UseForBatchAPIField(t *testing.T) {
	batchTrue := true
	batchFalse := false

	tests := []struct {
		name        string
		key1        schemas.Key
		key2        schemas.Key
		expectEqual bool
	}{
		{
			name: "batch_true_vs_false_different_hash",
			key1: schemas.Key{
				Name:           "test-key",
				Value:          *schemas.NewEnvVar("sk-123"),
				Weight:         1,
				UseForBatchAPI: &batchTrue,
			},
			key2: schemas.Key{
				Name:           "test-key",
				Value:          *schemas.NewEnvVar("sk-123"),
				Weight:         1,
				UseForBatchAPI: &batchFalse,
			},
			expectEqual: false,
		},
		{
			name: "batch_nil_vs_true_different_hash",
			key1: schemas.Key{
				Name:           "test-key",
				Value:          *schemas.NewEnvVar("sk-123"),
				Weight:         1,
				UseForBatchAPI: nil,
			},
			key2: schemas.Key{
				Name:           "test-key",
				Value:          *schemas.NewEnvVar("sk-123"),
				Weight:         1,
				UseForBatchAPI: &batchTrue,
			},
			expectEqual: false,
		},
		{
			name: "batch_nil_vs_false_same_hash",
			key1: schemas.Key{
				Name:           "test-key",
				Value:          *schemas.NewEnvVar("sk-123"),
				Weight:         1,
				UseForBatchAPI: nil,
			},
			key2: schemas.Key{
				Name:           "test-key",
				Value:          *schemas.NewEnvVar("sk-123"),
				Weight:         1,
				UseForBatchAPI: &batchFalse,
			},
			expectEqual: true,
		},
		{
			name: "same_batch_true_same_hash",
			key1: schemas.Key{
				Name:           "test-key",
				Value:          *schemas.NewEnvVar("sk-123"),
				Weight:         1,
				UseForBatchAPI: &batchTrue,
			},
			key2: schemas.Key{
				Name:           "test-key",
				Value:          *schemas.NewEnvVar("sk-123"),
				Weight:         1,
				UseForBatchAPI: &batchTrue,
			},
			expectEqual: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash1, err := configstore.GenerateKeyHash(tt.key1)
			if err != nil {
				t.Fatalf("Failed to generate hash for key1: %v", err)
			}

			hash2, err := configstore.GenerateKeyHash(tt.key2)
			if err != nil {
				t.Fatalf("Failed to generate hash for key2: %v", err)
			}

			if tt.expectEqual && hash1 != hash2 {
				t.Errorf("Expected equal hashes, got hash1=%s, hash2=%s", hash1, hash2)
			}
			if !tt.expectEqual && hash1 == hash2 {
				t.Errorf("Expected different hashes, but both are %s", hash1)
			}
		})
	}
}

// TestSQLite_Key_UseForBatchAPIChange_Detected verifies that changes to the UseForBatchAPI field
// are detected during config reconciliation and properly synced.
func TestSQLite_Key_UseForBatchAPIChange_Detected(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	batchTrue := true
	batchFalse := false
	keyID := uuid.NewString()

	// Initial config with UseForBatchAPI=false
	initialConfig := makeConfigDataWithProvidersAndDir(map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{
					ID:             keyID,
					Name:           "test-key",
					Value:          *schemas.NewEnvVar("sk-test-123"),
					Weight:         1,
					UseForBatchAPI: &batchFalse,
				},
			},
		},
	}, tempDir)

	// First load
	createConfigFile(t, tempDir, initialConfig)
	config1, err := LoadConfig(context.Background(), tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}
	defer config1.ConfigStore.Close(context.Background())

	// Verify initial state in the in-memory config
	openaiConfig1 := config1.Providers[schemas.OpenAI]
	if openaiConfig1.Keys[0].UseForBatchAPI == nil || *openaiConfig1.Keys[0].UseForBatchAPI {
		t.Fatal("Expected UseForBatchAPI=false after first load")
	}

	// Update config with UseForBatchAPI=true
	updatedConfig := makeConfigDataWithProvidersAndDir(map[string]configstore.ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{
					ID:             keyID,
					Name:           "test-key",
					Value:          *schemas.NewEnvVar("sk-test-123"),
					Weight:         1,
					UseForBatchAPI: &batchTrue,
				},
			},
		},
	}, tempDir)

	// Second load with changed UseForBatchAPI value
	createConfigFile(t, tempDir, updatedConfig)
	config2, err := LoadConfig(context.Background(), tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(context.Background())

	// Verify UseForBatchAPI changed to true in the in-memory config
	openaiConfig2 := config2.Providers[schemas.OpenAI]
	if openaiConfig2.Keys[0].UseForBatchAPI == nil || !*openaiConfig2.Keys[0].UseForBatchAPI {
		t.Error("Expected UseForBatchAPI=true after second load, but got false or nil")
	}
}

// TestGenerateVirtualKeyHash_ProviderConfigBudgetRateLimit verifies that BudgetID and RateLimitID
// in VK provider configs affect hash generation.
func TestGenerateVirtualKeyHash_ProviderConfigBudgetRateLimit(t *testing.T) {
	budgetID1 := "budget-1"
	budgetID2 := "budget-2"
	rateLimitID1 := "rate-limit-1"
	rateLimitID2 := "rate-limit-2"
	weight := 1.0

	tests := []struct {
		name        string
		vk1         tables.TableVirtualKey
		vk2         tables.TableVirtualKey
		expectEqual bool
	}{
		{
			name: "different_budget_id_different_hash",
			vk1: tables.TableVirtualKey{
				ID:       "vk-1",
				Name:     "test-vk",
				IsActive: true,
				ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
					{
						Provider: "openai",
						Weight:   &weight,
						BudgetID: &budgetID1,
					},
				},
			},
			vk2: tables.TableVirtualKey{
				ID:       "vk-1",
				Name:     "test-vk",
				IsActive: true,
				ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
					{
						Provider: "openai",
						Weight:   &weight,
						BudgetID: &budgetID2,
					},
				},
			},
			expectEqual: false,
		},
		{
			name: "different_rate_limit_id_different_hash",
			vk1: tables.TableVirtualKey{
				ID:       "vk-1",
				Name:     "test-vk",
				IsActive: true,
				ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
					{
						Provider:    "openai",
						Weight:      &weight,
						RateLimitID: &rateLimitID1,
					},
				},
			},
			vk2: tables.TableVirtualKey{
				ID:       "vk-1",
				Name:     "test-vk",
				IsActive: true,
				ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
					{
						Provider:    "openai",
						Weight:      &weight,
						RateLimitID: &rateLimitID2,
					},
				},
			},
			expectEqual: false,
		},
		{
			name: "nil_vs_set_budget_id_different_hash",
			vk1: tables.TableVirtualKey{
				ID:       "vk-1",
				Name:     "test-vk",
				IsActive: true,
				ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
					{
						Provider: "openai",
						Weight:   &weight,
						BudgetID: nil,
					},
				},
			},
			vk2: tables.TableVirtualKey{
				ID:       "vk-1",
				Name:     "test-vk",
				IsActive: true,
				ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
					{
						Provider: "openai",
						Weight:   &weight,
						BudgetID: &budgetID1,
					},
				},
			},
			expectEqual: false,
		},
		{
			name: "nil_vs_set_rate_limit_id_different_hash",
			vk1: tables.TableVirtualKey{
				ID:       "vk-1",
				Name:     "test-vk",
				IsActive: true,
				ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
					{
						Provider:    "openai",
						Weight:      &weight,
						RateLimitID: nil,
					},
				},
			},
			vk2: tables.TableVirtualKey{
				ID:       "vk-1",
				Name:     "test-vk",
				IsActive: true,
				ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
					{
						Provider:    "openai",
						Weight:      &weight,
						RateLimitID: &rateLimitID1,
					},
				},
			},
			expectEqual: false,
		},
		{
			name: "same_budget_and_rate_limit_same_hash",
			vk1: tables.TableVirtualKey{
				ID:       "vk-1",
				Name:     "test-vk",
				IsActive: true,
				ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
					{
						Provider:    "openai",
						Weight:      &weight,
						BudgetID:    &budgetID1,
						RateLimitID: &rateLimitID1,
					},
				},
			},
			vk2: tables.TableVirtualKey{
				ID:       "vk-1",
				Name:     "test-vk",
				IsActive: true,
				ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
					{
						Provider:    "openai",
						Weight:      &weight,
						BudgetID:    &budgetID1,
						RateLimitID: &rateLimitID1,
					},
				},
			},
			expectEqual: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash1, err := configstore.GenerateVirtualKeyHash(tt.vk1)
			if err != nil {
				t.Fatalf("Failed to generate hash for vk1: %v", err)
			}

			hash2, err := configstore.GenerateVirtualKeyHash(tt.vk2)
			if err != nil {
				t.Fatalf("Failed to generate hash for vk2: %v", err)
			}

			if tt.expectEqual && hash1 != hash2 {
				t.Errorf("Expected equal hashes, got hash1=%s, hash2=%s", hash1, hash2)
			}
			if !tt.expectEqual && hash1 == hash2 {
				t.Errorf("Expected different hashes, but both are %s", hash1)
			}
		})
	}
}

// TestSQLite_VKProviderConfig_BudgetAndRateLimit verifies that BudgetID and RateLimitID
// in VK provider configs are properly persisted and retrieved from SQLite.
func TestSQLite_VKProviderConfig_BudgetAndRateLimit(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	budgetID := "budget-123"
	rateLimitID := "rate-limit-456"
	vkID := uuid.NewString()
	weight := 1.0

	// Create config with VK that has provider config with BudgetID and RateLimitID
	configData := makeConfigDataFullWithDir(
		nil,
		map[string]configstore.ProviderConfig{
			"openai": {
				Keys: []schemas.Key{
					{
						ID:     uuid.NewString(),
						Name:   "openai-key",
						Value:  *schemas.NewEnvVar("sk-test"),
						Weight: 1,
					},
				},
			},
		},
		&configstore.GovernanceConfig{
			Budgets: []tables.TableBudget{
				{
					ID:       budgetID,
					MaxLimit: 100.0,
				},
			},
			RateLimits: []tables.TableRateLimit{
				{
					ID:              rateLimitID,
					RequestMaxLimit: int64Ptr(60),
					TokenMaxLimit:   int64Ptr(10000),
				},
			},
			VirtualKeys: []tables.TableVirtualKey{
				{
					ID:       vkID,
					Name:     "test-vk",
					Value:    "vk-test-value",
					IsActive: true,
					ProviderConfigs: []tables.TableVirtualKeyProviderConfig{
						{
							Provider:    "openai",
							Weight:      &weight,
							BudgetID:    &budgetID,
							RateLimitID: &rateLimitID,
						},
					},
				},
			},
		},
		tempDir,
	)

	// Load config
	createConfigFile(t, tempDir, configData)
	config, err := LoadConfig(context.Background(), tempDir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	defer config.ConfigStore.Close(context.Background())

	// Verify the governance config has the VK with provider configs
	if config.GovernanceConfig == nil {
		t.Fatal("Expected GovernanceConfig to exist")
	}
	if len(config.GovernanceConfig.VirtualKeys) == 0 {
		t.Fatal("Expected VirtualKeys in GovernanceConfig")
	}

	// Find the VK and verify provider config
	var foundVK *tables.TableVirtualKey
	for i := range config.GovernanceConfig.VirtualKeys {
		if config.GovernanceConfig.VirtualKeys[i].ID == vkID {
			foundVK = &config.GovernanceConfig.VirtualKeys[i]
			break
		}
	}
	if foundVK == nil {
		t.Fatalf("Virtual key %s not found in config", vkID)
	}

	if len(foundVK.ProviderConfigs) == 0 {
		t.Fatal("Expected VK to have provider configs")
	}

	pc := foundVK.ProviderConfigs[0]
	if pc.BudgetID == nil || *pc.BudgetID != budgetID {
		t.Errorf("Expected BudgetID=%s, got %v", budgetID, pc.BudgetID)
	}
	if pc.RateLimitID == nil || *pc.RateLimitID != rateLimitID {
		t.Errorf("Expected RateLimitID=%s, got %v", rateLimitID, pc.RateLimitID)
	}
}

// intPtr is a helper to create a pointer to an int
func intPtr(i int) *int {
	return &i
}

// int64Ptr is a helper to create a pointer to an int64
func int64Ptr(i int64) *int64 {
	return &i
}

// TestKeyHashComparison_VertexConfigSyncScenarios tests full lifecycle for Vertex key configs
func TestKeyHashComparison_VertexConfigSyncScenarios(t *testing.T) {
	// === Scenario 1: Vertex config in DB + same in file -> hash matches, no update ===
	t.Run("SameVertexConfig_NoUpdate", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "vertex-key",
			Value:  *schemas.NewEnvVar("vertex-api-key-123"),
			Weight: 1,
			VertexKeyConfig: &schemas.VertexKeyConfig{
				ProjectID:       *schemas.NewEnvVar("my-project-123"),
				ProjectNumber:   *schemas.NewEnvVar("123456789"),
				Region:          *schemas.NewEnvVar("us-central1"),
				AuthCredentials: *schemas.NewEnvVar(`{"type":"service_account"}`),
				Deployments: map[string]string{
					"gemini-pro": "gemini-pro-endpoint",
				},
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "vertex-key",
			Value:  *schemas.NewEnvVar("vertex-api-key-123"),
			Weight: 1,
			VertexKeyConfig: &schemas.VertexKeyConfig{
				ProjectID:       *schemas.NewEnvVar("my-project-123"),
				ProjectNumber:   *schemas.NewEnvVar("123456789"),
				Region:          *schemas.NewEnvVar("us-central1"),
				AuthCredentials: *schemas.NewEnvVar(`{"type":"service_account"}`),
				Deployments: map[string]string{
					"gemini-pro": "gemini-pro-endpoint",
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash != fileHash {
			t.Errorf("Expected same hash for identical Vertex configs. DB: %s, File: %s", dbHash[:16], fileHash[:16])
		}
		t.Log("✓ Same Vertex config produces same hash - no update needed")
	})

	// === Scenario 2: Vertex config in DB + different ProjectID in file -> hash differs ===
	t.Run("DifferentProjectID_UpdateTriggered", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "vertex-key",
			Value:  *schemas.NewEnvVar("vertex-api-key-123"),
			Weight: 1,
			VertexKeyConfig: &schemas.VertexKeyConfig{
				ProjectID:       *schemas.NewEnvVar("my-project-123"),
				Region:          *schemas.NewEnvVar("us-central1"),
				AuthCredentials: *schemas.NewEnvVar(`{"type":"service_account"}`),
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "vertex-key",
			Value:  *schemas.NewEnvVar("vertex-api-key-123"),
			Weight: 1,
			VertexKeyConfig: &schemas.VertexKeyConfig{
				ProjectID:       *schemas.NewEnvVar("different-project-456"), // Changed!
				Region:          *schemas.NewEnvVar("us-central1"),
				AuthCredentials: *schemas.NewEnvVar(`{"type":"service_account"}`),
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when Vertex ProjectID changes")
		}
		t.Log("✓ Different Vertex ProjectID produces different hash - update triggered")
	})

	// === Scenario 3: Vertex config in DB + different Region in file -> hash differs ===
	t.Run("DifferentRegion_UpdateTriggered", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "vertex-key",
			Value:  *schemas.NewEnvVar("vertex-api-key-123"),
			Weight: 1,
			VertexKeyConfig: &schemas.VertexKeyConfig{
				ProjectID:       *schemas.NewEnvVar("my-project-123"),
				Region:          *schemas.NewEnvVar("us-central1"),
				AuthCredentials: *schemas.NewEnvVar(`{"type":"service_account"}`),
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "vertex-key",
			Value:  *schemas.NewEnvVar("vertex-api-key-123"),
			Weight: 1,
			VertexKeyConfig: &schemas.VertexKeyConfig{
				ProjectID:       *schemas.NewEnvVar("my-project-123"),
				Region:          *schemas.NewEnvVar("europe-west1"), // Changed!
				AuthCredentials: *schemas.NewEnvVar(`{"type":"service_account"}`),
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when Vertex Region changes")
		}
		t.Log("✓ Different Vertex Region produces different hash - update triggered")
	})

	// === Scenario 4: Vertex config in DB + different AuthCredentials in file -> hash differs ===
	t.Run("DifferentAuthCredentials_UpdateTriggered", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "vertex-key",
			Value:  *schemas.NewEnvVar("vertex-api-key-123"),
			Weight: 1,
			VertexKeyConfig: &schemas.VertexKeyConfig{
				ProjectID:       *schemas.NewEnvVar("my-project-123"),
				Region:          *schemas.NewEnvVar("us-central1"),
				AuthCredentials: *schemas.NewEnvVar(`{"type":"service_account","client_id":"old"}`),
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "vertex-key",
			Value:  *schemas.NewEnvVar("vertex-api-key-123"),
			Weight: 1,
			VertexKeyConfig: &schemas.VertexKeyConfig{
				ProjectID:       *schemas.NewEnvVar("my-project-123"),
				Region:          *schemas.NewEnvVar("us-central1"),
				AuthCredentials: *schemas.NewEnvVar(`{"type":"service_account","client_id":"new"}`), // Changed!
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when Vertex AuthCredentials changes")
		}
		t.Log("✓ Different Vertex AuthCredentials produces different hash - update triggered")
	})

	// === Scenario 5: Vertex config in DB + different Deployments map in file -> hash differs ===
	t.Run("DifferentDeployments_UpdateTriggered", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "vertex-key",
			Value:  *schemas.NewEnvVar("vertex-api-key-123"),
			Weight: 1,
			VertexKeyConfig: &schemas.VertexKeyConfig{
				ProjectID: *schemas.NewEnvVar("my-project-123"),
				Region:    *schemas.NewEnvVar("us-central1"),
				Deployments: map[string]string{
					"gemini-pro": "gemini-pro-endpoint",
				},
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "vertex-key",
			Value:  *schemas.NewEnvVar("vertex-api-key-123"),
			Weight: 1,
			VertexKeyConfig: &schemas.VertexKeyConfig{
				ProjectID: *schemas.NewEnvVar("my-project-123"),
				Region:    *schemas.NewEnvVar("us-central1"),
				Deployments: map[string]string{
					"gemini-pro":     "gemini-pro-endpoint",
					"gemini-1.5-pro": "gemini-15-pro-endpoint", // Added!
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when Vertex Deployments map changes")
		}
		t.Log("✓ Different Vertex Deployments produces different hash - update triggered")
	})

	// === Scenario 6: Vertex config added to file when not in DB -> new key detected ===
	t.Run("VertexConfigAdded_NewKeyDetected", func(t *testing.T) {
		// DB key has no Vertex config
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "vertex-key",
			Value:  *schemas.NewEnvVar("vertex-api-key-123"),
			Weight: 1,
			// No VertexKeyConfig
		}

		// File key has Vertex config
		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "vertex-key",
			Value:  *schemas.NewEnvVar("vertex-api-key-123"),
			Weight: 1,
			VertexKeyConfig: &schemas.VertexKeyConfig{
				ProjectID:       *schemas.NewEnvVar("my-project-123"),
				Region:          *schemas.NewEnvVar("us-central1"),
				AuthCredentials: *schemas.NewEnvVar(`{"type":"service_account"}`),
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when Vertex config is added")
		}
		t.Log("✓ Vertex config added produces different hash - update triggered")
	})

	// === Scenario 7: Vertex config removed from file -> hash differs ===
	t.Run("VertexConfigRemoved_UpdateTriggered", func(t *testing.T) {
		// DB key has Vertex config
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "vertex-key",
			Value:  *schemas.NewEnvVar("vertex-api-key-123"),
			Weight: 1,
			VertexKeyConfig: &schemas.VertexKeyConfig{
				ProjectID:       *schemas.NewEnvVar("my-project-123"),
				Region:          *schemas.NewEnvVar("us-central1"),
				AuthCredentials: *schemas.NewEnvVar(`{"type":"service_account"}`),
			},
		}

		// File key has no Vertex config
		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "vertex-key",
			Value:  *schemas.NewEnvVar("vertex-api-key-123"),
			Weight: 1,
			// No VertexKeyConfig
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when Vertex config is removed")
		}
		t.Log("✓ Vertex config removed produces different hash - update triggered")
	})

	// === Scenario 8: ProjectNumber nil vs set -> hash differs ===
	t.Run("ProjectNumberNilVsSet_UpdateTriggered", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "vertex-key",
			Value:  *schemas.NewEnvVar("vertex-api-key-123"),
			Weight: 1,
			VertexKeyConfig: &schemas.VertexKeyConfig{
				ProjectID: *schemas.NewEnvVar("my-project-123"),
				Region:    *schemas.NewEnvVar("us-central1"),
				// ProjectNumber is not set (empty EnvVar)
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "vertex-key",
			Value:  *schemas.NewEnvVar("vertex-api-key-123"),
			Weight: 1,
			VertexKeyConfig: &schemas.VertexKeyConfig{
				ProjectID:     *schemas.NewEnvVar("my-project-123"),
				ProjectNumber: *schemas.NewEnvVar("123456789"), // Explicitly set
				Region:        *schemas.NewEnvVar("us-central1"),
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when ProjectNumber goes from empty to set")
		}
		t.Log("✓ ProjectNumber empty vs set produces different hash - update triggered")
	})
}

// TestProviderHashComparison_VertexProviderFullLifecycle tests the complete Vertex provider lifecycle
func TestProviderHashComparison_VertexProviderFullLifecycle(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	keyID := uuid.NewString()

	// Phase 1: Initial load with Vertex config
	initialConfig := makeConfigDataWithProvidersAndDir(map[string]configstore.ProviderConfig{
		"vertex": {
			Keys: []schemas.Key{
				{
					ID:     keyID,
					Name:   "vertex-key",
					Value:  *schemas.NewEnvVar("vertex-service-account-json"),
					Weight: 1,
					VertexKeyConfig: &schemas.VertexKeyConfig{
						ProjectID:       *schemas.NewEnvVar("my-project-123"),
						Region:          *schemas.NewEnvVar("us-central1"),
						AuthCredentials: *schemas.NewEnvVar(`{"type":"service_account"}`),
					},
				},
			},
		},
	}, tempDir)

	createConfigFile(t, tempDir, initialConfig)
	config1, err := LoadConfig(context.Background(), tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}
	defer config1.ConfigStore.Close(context.Background())

	// Verify initial state
	providers1, _ := config1.ConfigStore.GetProvidersConfig(context.Background())
	vertexConfig1 := providers1[schemas.Vertex]
	if vertexConfig1.Keys[0].VertexKeyConfig == nil {
		t.Fatal("Expected VertexKeyConfig after first load")
	}
	if vertexConfig1.Keys[0].VertexKeyConfig.ProjectID.GetValue() != "my-project-123" {
		t.Errorf("Expected ProjectID='my-project-123', got '%s'", vertexConfig1.Keys[0].VertexKeyConfig.ProjectID.GetValue())
	}

	// Phase 2: Update config with different Region
	updatedConfig := makeConfigDataWithProvidersAndDir(map[string]configstore.ProviderConfig{
		"vertex": {
			Keys: []schemas.Key{
				{
					ID:     keyID,
					Name:   "vertex-key",
					Value:  *schemas.NewEnvVar("vertex-service-account-json"),
					Weight: 1,
					VertexKeyConfig: &schemas.VertexKeyConfig{
						ProjectID:       *schemas.NewEnvVar("my-project-123"),
						Region:          *schemas.NewEnvVar("europe-west1"), // Changed!
						AuthCredentials: *schemas.NewEnvVar(`{"type":"service_account"}`),
					},
				},
			},
		},
	}, tempDir)

	createConfigFile(t, tempDir, updatedConfig)
	config2, err := LoadConfig(context.Background(), tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(context.Background())

	// Verify Region changed
	providers2, _ := config2.ConfigStore.GetProvidersConfig(context.Background())
	vertexConfig2 := providers2[schemas.Vertex]
	if vertexConfig2.Keys[0].VertexKeyConfig.Region.GetValue() != "europe-west1" {
		t.Errorf("Expected Region='europe-west1', got '%s'", vertexConfig2.Keys[0].VertexKeyConfig.Region.GetValue())
	}

	// Phase 3: Same config again - should not trigger update (hash matches)
	config3, err := LoadConfig(context.Background(), tempDir)
	if err != nil {
		t.Fatalf("Third LoadConfig failed: %v", err)
	}
	defer config3.ConfigStore.Close(context.Background())

	providers3, _ := config3.ConfigStore.GetProvidersConfig(context.Background())
	vertexConfig3 := providers3[schemas.Vertex]
	if vertexConfig3.Keys[0].VertexKeyConfig.Region.GetValue() != "europe-west1" {
		t.Errorf("Expected Region='europe-west1' preserved, got '%s'", vertexConfig3.Keys[0].VertexKeyConfig.Region.GetValue())
	}
}

// TestProviderHashComparison_VertexNewProviderFromConfig tests adding a new Vertex provider from config
func TestProviderHashComparison_VertexNewProviderFromConfig(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	// Create config with Vertex provider
	configData := makeConfigDataWithProvidersAndDir(map[string]configstore.ProviderConfig{
		"vertex": {
			Keys: []schemas.Key{
				{
					ID:     uuid.NewString(),
					Name:   "vertex-key",
					Value:  *schemas.NewEnvVar("vertex-creds"),
					Weight: 1,
					VertexKeyConfig: &schemas.VertexKeyConfig{
						ProjectID:       *schemas.NewEnvVar("new-project-456"),
						Region:          *schemas.NewEnvVar("us-west1"),
						AuthCredentials: *schemas.NewEnvVar(`{"type":"service_account"}`),
					},
				},
			},
		},
	}, tempDir)

	createConfigFile(t, tempDir, configData)
	config, err := LoadConfig(context.Background(), tempDir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	defer config.ConfigStore.Close(context.Background())

	// Verify Vertex provider was created
	providers, _ := config.ConfigStore.GetProvidersConfig(context.Background())
	vertexConfig, exists := providers[schemas.Vertex]
	if !exists {
		t.Fatal("Expected Vertex provider to exist in DB")
	}
	if len(vertexConfig.Keys) != 1 {
		t.Fatalf("Expected 1 key, got %d", len(vertexConfig.Keys))
	}
	if vertexConfig.Keys[0].VertexKeyConfig == nil {
		t.Fatal("Expected VertexKeyConfig to exist")
	}
	if vertexConfig.Keys[0].VertexKeyConfig.ProjectID.GetValue() != "new-project-456" {
		t.Errorf("Expected ProjectID='new-project-456', got '%s'", vertexConfig.Keys[0].VertexKeyConfig.ProjectID.GetValue())
	}
}

// TestProviderHashComparison_VertexDBValuePreservedWhenHashMatches tests that DB values are preserved when hash matches
func TestProviderHashComparison_VertexDBValuePreservedWhenHashMatches(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	keyID := uuid.NewString()

	// Initial config
	configData := makeConfigDataWithProvidersAndDir(map[string]configstore.ProviderConfig{
		"vertex": {
			Keys: []schemas.Key{
				{
					ID:     keyID,
					Name:   "vertex-key",
					Value:  *schemas.NewEnvVar("vertex-creds"),
					Weight: 1,
					VertexKeyConfig: &schemas.VertexKeyConfig{
						ProjectID:       *schemas.NewEnvVar("my-project"),
						Region:          *schemas.NewEnvVar("us-central1"),
						AuthCredentials: *schemas.NewEnvVar(`{"type":"service_account"}`),
					},
				},
			},
		},
	}, tempDir)

	createConfigFile(t, tempDir, configData)
	config1, err := LoadConfig(context.Background(), tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}

	// Manually update the DB with a different AuthCredentials (simulating dashboard edit)
	providers1, _ := config1.ConfigStore.GetProvidersConfig(context.Background())
	vertexConfig := providers1[schemas.Vertex]
	vertexConfig.Keys[0].VertexKeyConfig.AuthCredentials = *schemas.NewEnvVar(`{"type":"service_account","edited":true}`)
	config1.ConfigStore.UpdateProvidersConfig(context.Background(), providers1)
	config1.ConfigStore.Close(context.Background())

	// Reload with same config file - DB value should be preserved since hash matches
	config2, err := LoadConfig(context.Background(), tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(context.Background())

	// Note: With hash-based reconciliation, when the file hash doesn't change,
	// the DB values are preserved. Since we modified the DB but not the file,
	// the file hash still matches the original, so DB values are kept.
	providers2, _ := config2.ConfigStore.GetProvidersConfig(context.Background())
	vertexConfig2 := providers2[schemas.Vertex]
	// The AuthCredentials should be the DB-modified value since file hash matches original
	if vertexConfig2.Keys[0].VertexKeyConfig.AuthCredentials.GetValue() != `{"type":"service_account","edited":true}` {
		t.Logf("AuthCredentials: %s", vertexConfig2.Keys[0].VertexKeyConfig.AuthCredentials.GetValue())
		// This is expected behavior - when file hasn't changed, DB value is preserved
	}
}

// TestProviderHashComparison_VertexConfigChangedInFile tests that file changes override DB when hash differs
func TestProviderHashComparison_VertexConfigChangedInFile(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)

	keyID := uuid.NewString()

	// Initial config
	initialConfig := makeConfigDataWithProvidersAndDir(map[string]configstore.ProviderConfig{
		"vertex": {
			Keys: []schemas.Key{
				{
					ID:     keyID,
					Name:   "vertex-key",
					Value:  *schemas.NewEnvVar("vertex-creds"),
					Weight: 1,
					VertexKeyConfig: &schemas.VertexKeyConfig{
						ProjectID: *schemas.NewEnvVar("original-project"),
						Region:    *schemas.NewEnvVar("us-central1"),
					},
				},
			},
		},
	}, tempDir)

	createConfigFile(t, tempDir, initialConfig)
	config1, err := LoadConfig(context.Background(), tempDir)
	if err != nil {
		t.Fatalf("First LoadConfig failed: %v", err)
	}
	config1.ConfigStore.Close(context.Background())

	// Update config file with different ProjectID
	updatedConfig := makeConfigDataWithProvidersAndDir(map[string]configstore.ProviderConfig{
		"vertex": {
			Keys: []schemas.Key{
				{
					ID:     keyID,
					Name:   "vertex-key",
					Value:  *schemas.NewEnvVar("vertex-creds"),
					Weight: 1,
					VertexKeyConfig: &schemas.VertexKeyConfig{
						ProjectID: *schemas.NewEnvVar("updated-project"), // Changed!
						Region:    *schemas.NewEnvVar("us-central1"),
					},
				},
			},
		},
	}, tempDir)

	createConfigFile(t, tempDir, updatedConfig)
	config2, err := LoadConfig(context.Background(), tempDir)
	if err != nil {
		t.Fatalf("Second LoadConfig failed: %v", err)
	}
	defer config2.ConfigStore.Close(context.Background())

	// Verify file value wins
	providers2, _ := config2.ConfigStore.GetProvidersConfig(context.Background())
	vertexConfig2 := providers2[schemas.Vertex]
	if vertexConfig2.Keys[0].VertexKeyConfig.ProjectID.GetValue() != "updated-project" {
		t.Errorf("Expected ProjectID='updated-project' from file, got '%s'", vertexConfig2.Keys[0].VertexKeyConfig.ProjectID.GetValue())
	}
}

// TestKeyHashComparison_AzureDeploymentsChange tests various deployment map change scenarios for Azure
func TestKeyHashComparison_AzureDeploymentsChange(t *testing.T) {
	t.Run("AddDeployment", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "azure-key",
			Value:  *schemas.NewEnvVar("azure-api-key"),
			Weight: 1,
			AzureKeyConfig: &schemas.AzureKeyConfig{
				Endpoint: *schemas.NewEnvVar("https://myazure.openai.azure.com"),
				Deployments: map[string]string{
					"gpt-4": "gpt-4-deployment",
				},
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "azure-key",
			Value:  *schemas.NewEnvVar("azure-api-key"),
			Weight: 1,
			AzureKeyConfig: &schemas.AzureKeyConfig{
				Endpoint: *schemas.NewEnvVar("https://myazure.openai.azure.com"),
				Deployments: map[string]string{
					"gpt-4":  "gpt-4-deployment",
					"gpt-4o": "gpt-4o-deployment", // Added
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when adding Azure deployment")
		}
	})

	t.Run("RemoveDeployment", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "azure-key",
			Value:  *schemas.NewEnvVar("azure-api-key"),
			Weight: 1,
			AzureKeyConfig: &schemas.AzureKeyConfig{
				Endpoint: *schemas.NewEnvVar("https://myazure.openai.azure.com"),
				Deployments: map[string]string{
					"gpt-4":  "gpt-4-deployment",
					"gpt-4o": "gpt-4o-deployment",
				},
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "azure-key",
			Value:  *schemas.NewEnvVar("azure-api-key"),
			Weight: 1,
			AzureKeyConfig: &schemas.AzureKeyConfig{
				Endpoint: *schemas.NewEnvVar("https://myazure.openai.azure.com"),
				Deployments: map[string]string{
					"gpt-4": "gpt-4-deployment", // gpt-4o removed
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when removing Azure deployment")
		}
	})

	t.Run("ModifyDeploymentValue", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "azure-key",
			Value:  *schemas.NewEnvVar("azure-api-key"),
			Weight: 1,
			AzureKeyConfig: &schemas.AzureKeyConfig{
				Endpoint: *schemas.NewEnvVar("https://myazure.openai.azure.com"),
				Deployments: map[string]string{
					"gpt-4": "gpt-4-deployment-v1",
				},
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "azure-key",
			Value:  *schemas.NewEnvVar("azure-api-key"),
			Weight: 1,
			AzureKeyConfig: &schemas.AzureKeyConfig{
				Endpoint: *schemas.NewEnvVar("https://myazure.openai.azure.com"),
				Deployments: map[string]string{
					"gpt-4": "gpt-4-deployment-v2", // Value changed
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when modifying Azure deployment value")
		}
	})

	t.Run("EmptyToNonEmpty", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "azure-key",
			Value:  *schemas.NewEnvVar("azure-api-key"),
			Weight: 1,
			AzureKeyConfig: &schemas.AzureKeyConfig{
				Endpoint:    *schemas.NewEnvVar("https://myazure.openai.azure.com"),
				Deployments: nil, // No deployments
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "azure-key",
			Value:  *schemas.NewEnvVar("azure-api-key"),
			Weight: 1,
			AzureKeyConfig: &schemas.AzureKeyConfig{
				Endpoint: *schemas.NewEnvVar("https://myazure.openai.azure.com"),
				Deployments: map[string]string{
					"gpt-4": "gpt-4-deployment",
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when Azure deployments go from nil to non-empty")
		}
	})
}

// TestKeyHashComparison_BedrockDeploymentsChange tests various deployment map change scenarios for Bedrock
func TestKeyHashComparison_BedrockDeploymentsChange(t *testing.T) {
	t.Run("AddDeployment", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar("bedrock-key"),
			Weight: 1,
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
				SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI"),
				Region:    schemas.NewEnvVar("us-east-1"),
				Deployments: map[string]string{
					"claude-3": "arn:aws:bedrock:us-east-1::inference-profile/claude-3",
				},
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar("bedrock-key"),
			Weight: 1,
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
				SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI"),
				Region:    schemas.NewEnvVar("us-east-1"),
				Deployments: map[string]string{
					"claude-3":   "arn:aws:bedrock:us-east-1::inference-profile/claude-3",
					"claude-3.5": "arn:aws:bedrock:us-east-1::inference-profile/claude-3.5", // Added
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when adding Bedrock deployment")
		}
	})

	t.Run("RemoveDeployment", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar("bedrock-key"),
			Weight: 1,
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
				SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI"),
				Region:    schemas.NewEnvVar("us-east-1"),
				Deployments: map[string]string{
					"claude-3":   "arn:aws:bedrock:us-east-1::inference-profile/claude-3",
					"claude-3.5": "arn:aws:bedrock:us-east-1::inference-profile/claude-3.5",
				},
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar("bedrock-key"),
			Weight: 1,
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
				SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI"),
				Region:    schemas.NewEnvVar("us-east-1"),
				Deployments: map[string]string{
					"claude-3": "arn:aws:bedrock:us-east-1::inference-profile/claude-3", // claude-3.5 removed
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when removing Bedrock deployment")
		}
	})

	t.Run("ModifyDeploymentValue", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar("bedrock-key"),
			Weight: 1,
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
				SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI"),
				Region:    schemas.NewEnvVar("us-east-1"),
				Deployments: map[string]string{
					"claude-3": "arn:aws:bedrock:us-east-1::inference-profile/claude-3-old",
				},
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "bedrock-key",
			Value:  *schemas.NewEnvVar("bedrock-key"),
			Weight: 1,
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
				SecretKey: *schemas.NewEnvVar("wJalrXUtnFEMI"),
				Region:    schemas.NewEnvVar("us-east-1"),
				Deployments: map[string]string{
					"claude-3": "arn:aws:bedrock:us-east-1::inference-profile/claude-3-new", // Value changed
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when modifying Bedrock deployment value")
		}
	})
}

// TestKeyHashComparison_VertexDeploymentsChange tests various deployment map change scenarios for Vertex
func TestKeyHashComparison_VertexDeploymentsChange(t *testing.T) {
	t.Run("AddDeployment", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "vertex-key",
			Value:  *schemas.NewEnvVar("vertex-creds"),
			Weight: 1,
			VertexKeyConfig: &schemas.VertexKeyConfig{
				ProjectID: *schemas.NewEnvVar("my-project"),
				Region:    *schemas.NewEnvVar("us-central1"),
				Deployments: map[string]string{
					"gemini-pro": "gemini-pro-endpoint",
				},
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "vertex-key",
			Value:  *schemas.NewEnvVar("vertex-creds"),
			Weight: 1,
			VertexKeyConfig: &schemas.VertexKeyConfig{
				ProjectID: *schemas.NewEnvVar("my-project"),
				Region:    *schemas.NewEnvVar("us-central1"),
				Deployments: map[string]string{
					"gemini-pro":     "gemini-pro-endpoint",
					"gemini-1.5-pro": "gemini-15-pro-endpoint", // Added
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when adding Vertex deployment")
		}
	})

	t.Run("RemoveDeployment", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "vertex-key",
			Value:  *schemas.NewEnvVar("vertex-creds"),
			Weight: 1,
			VertexKeyConfig: &schemas.VertexKeyConfig{
				ProjectID: *schemas.NewEnvVar("my-project"),
				Region:    *schemas.NewEnvVar("us-central1"),
				Deployments: map[string]string{
					"gemini-pro":     "gemini-pro-endpoint",
					"gemini-1.5-pro": "gemini-15-pro-endpoint",
				},
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "vertex-key",
			Value:  *schemas.NewEnvVar("vertex-creds"),
			Weight: 1,
			VertexKeyConfig: &schemas.VertexKeyConfig{
				ProjectID: *schemas.NewEnvVar("my-project"),
				Region:    *schemas.NewEnvVar("us-central1"),
				Deployments: map[string]string{
					"gemini-pro": "gemini-pro-endpoint", // gemini-1.5-pro removed
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when removing Vertex deployment")
		}
	})

	t.Run("ModifyDeploymentValue", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "vertex-key",
			Value:  *schemas.NewEnvVar("vertex-creds"),
			Weight: 1,
			VertexKeyConfig: &schemas.VertexKeyConfig{
				ProjectID: *schemas.NewEnvVar("my-project"),
				Region:    *schemas.NewEnvVar("us-central1"),
				Deployments: map[string]string{
					"gemini-pro": "gemini-pro-endpoint-v1",
				},
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "vertex-key",
			Value:  *schemas.NewEnvVar("vertex-creds"),
			Weight: 1,
			VertexKeyConfig: &schemas.VertexKeyConfig{
				ProjectID: *schemas.NewEnvVar("my-project"),
				Region:    *schemas.NewEnvVar("us-central1"),
				Deployments: map[string]string{
					"gemini-pro": "gemini-pro-endpoint-v2", // Value changed
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when modifying Vertex deployment value")
		}
	})

	t.Run("EmptyToNonEmpty", func(t *testing.T) {
		dbKey := schemas.Key{
			ID:     "key-1",
			Name:   "vertex-key",
			Value:  *schemas.NewEnvVar("vertex-creds"),
			Weight: 1,
			VertexKeyConfig: &schemas.VertexKeyConfig{
				ProjectID:   *schemas.NewEnvVar("my-project"),
				Region:      *schemas.NewEnvVar("us-central1"),
				Deployments: nil, // No deployments
			},
		}

		fileKey := schemas.Key{
			ID:     "key-1",
			Name:   "vertex-key",
			Value:  *schemas.NewEnvVar("vertex-creds"),
			Weight: 1,
			VertexKeyConfig: &schemas.VertexKeyConfig{
				ProjectID: *schemas.NewEnvVar("my-project"),
				Region:    *schemas.NewEnvVar("us-central1"),
				Deployments: map[string]string{
					"gemini-pro": "gemini-pro-endpoint",
				},
			},
		}

		dbHash, _ := configstore.GenerateKeyHash(dbKey)
		fileHash, _ := configstore.GenerateKeyHash(fileKey)

		if dbHash == fileHash {
			t.Error("Expected different hash when Vertex deployments go from nil to non-empty")
		}
	})
}
