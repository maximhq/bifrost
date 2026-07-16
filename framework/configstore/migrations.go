package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/maximhq/bifrost/framework/migrator"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// addColumnIfNotExists is a package-local alias for migrator.AddColumnIfNotExists,
// the idempotent column-add helper shared with logstore. Declared at package
// scope (where `migrator` resolves to the package, not the `migrator :=
// tx.Migrator()` locals inside migration closures) so every call site can keep
// calling addColumnIfNotExists(tx, ...) directly.
var addColumnIfNotExists = migrator.AddColumnIfNotExists

// dropColumnIfExists is the drop counterpart to addColumnIfNotExists: a
// package-local alias for migrator.DropColumnIfExists, the idempotent
// column-drop helper shared with logstore. Declared at package scope for the
// same reason as above so call sites can use dropColumnIfExists(tx, ...).
var dropColumnIfExists = migrator.DropColumnIfExists

const (
	// migrationAdvisoryLockKey is used for PostgreSQL advisory locks
	// to serialize migrations across cluster nodes
	migrationAdvisoryLockKey = 1000001

	// advisoryLockRetryInterval is how long to wait between lock acquisition attempts.
	advisoryLockRetryInterval = 5 * time.Second

	// advisoryLockTimeout is the maximum time to wait for the advisory lock
	// before giving up with actionable operator guidance.
	advisoryLockTimeout = 1 * time.Minute
)

// migrationLock holds a dedicated connection for the advisory lock.
// This ensures the lock is held on the same connection throughout migrations,
// preventing race conditions caused by GORM's connection pooling.
type migrationLock struct {
	conn *sql.Conn
}

// acquireMigrationLock gets a dedicated connection and acquires an advisory lock
// using pg_try_advisory_lock with retry + timeout. This prevents pods from
// blocking indefinitely if a previous pod crashed without releasing the lock
// (e.g., behind a connection proxy or with slow TCP keepalive detection).
// For non-PostgreSQL databases, returns a no-op lock.
func acquireMigrationLock(ctx context.Context, db *gorm.DB, logger schemas.Logger) (*migrationLock, error) {
	if db.Dialector.Name() != "postgres" {
		return &migrationLock{}, nil
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get sql.DB: %w", err)
	}

	// Get a dedicated connection (not returned to pool until Close())
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get dedicated connection: %w", err)
	}

	logger.Info("[configstore] attempting to get migration lock %d", migrationAdvisoryLockKey)
	// Try to acquire advisory lock with retry + timeout instead of blocking forever.
	// pg_try_advisory_lock returns true if acquired, false if held by another session.
	deadline := time.Now().Add(advisoryLockTimeout)
	maxAttempts := int(advisoryLockTimeout / advisoryLockRetryInterval)
	attempt := 0

	for {
		attempt++
		// Derive a per-attempt context with the remaining lock budget as timeout,
		// so a stalled DB round-trip can't block beyond the overall deadline.
		attemptTimeout := time.Until(deadline)
		if attemptTimeout <= 0 {
			attemptTimeout = advisoryLockRetryInterval
		}
		attemptCtx, attemptCancel := context.WithTimeout(ctx, attemptTimeout)
		var acquired bool
		err = conn.QueryRowContext(attemptCtx, "SELECT pg_try_advisory_lock($1)", migrationAdvisoryLockKey).Scan(&acquired)
		attemptCancel()
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("failed to attempt migration advisory lock: %w", err)
		}

		if acquired {
			if attempt > 1 {
				logger.Info("[configstore] migration lock acquired after %d attempts", attempt)
			}
			return &migrationLock{conn: conn}, nil
		}

		// Lock not acquired -- check if we've exceeded the timeout
		if time.Now().After(deadline) {
			conn.Close()
			return nil, fmt.Errorf(
				"failed to acquire configstore migration lock (key=%d) after %d attempts over %s\n\n"+
					"This usually means another Bifrost pod (or a previous crashed pod's lingering\n"+
					"database session) is still holding the lock. To diagnose and resolve:\n\n"+
					"1. Find who holds the lock:\n"+
					"   SELECT pid, usename, application_name, client_addr, backend_start, state, query\n"+
					"   FROM pg_stat_activity\n"+
					"   WHERE pid IN (SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND objid = %d AND granted = true);\n\n"+
					"2. If the session belongs to a dead/crashed pod, terminate it:\n"+
					"   SELECT pg_terminate_backend(<pid_from_step_1>);\n\n"+
					"3. List all sessions waiting for this lock:\n"+
					"   SELECT pid, usename, client_addr, state, wait_event\n"+
					"   FROM pg_stat_activity\n"+
					"   WHERE pid IN (SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND objid = %d AND granted = false);\n\n"+
					"After terminating the stale session, restart this pod and it should proceed normally.",
				migrationAdvisoryLockKey, attempt, advisoryLockTimeout,
				migrationAdvisoryLockKey, migrationAdvisoryLockKey,
			)
		}

		logger.Info("[configstore] waiting for migration lock (attempt %d/%d) — another node is running migrations, retrying in %s...",
			attempt, maxAttempts, advisoryLockRetryInterval)

		// Wait before retrying, but respect context cancellation
		select {
		case <-ctx.Done():
			conn.Close()
			return nil, fmt.Errorf("context cancelled while waiting for migration lock: %w", ctx.Err())
		case <-time.After(advisoryLockRetryInterval):
		}
	}
}

// release unlocks and closes the dedicated connection
func (l *migrationLock) release(ctx context.Context) {
	if l.conn == nil {
		return
	}
	// Release lock on the SAME connection that acquired it
	_, _ = l.conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", migrationAdvisoryLockKey)
	l.conn.Close()
}

// RunSingleMigration applies a single gormigrate migration on the given
// *gorm.DB. Mirrors (*RDBConfigStore).RunMigration but takes the *gorm.DB
// directly, so downstream consumers (bifrost-enterprise, plugins) can run
// their migrations inside a MigrateOnFreshConnection callback without having
// to reach the throwaway pool through the ConfigStore abstraction.
func RunSingleMigration(ctx context.Context, options *migrator.Options, db *gorm.DB, logger schemas.Logger, migration *migrator.Migration) error {
	if db == nil {
		return fmt.Errorf("db cannot be nil")
	}
	if migration == nil {
		return fmt.Errorf("migration cannot be nil")
	}
	migrationOpts := migrator.DefaultOptions
	if options != nil {
		migrationOpts = options
	}
	m := migrator.New(db.WithContext(ctx), migrationOpts, []*migrator.Migration{migration})
	return m.Migrate()
}

// legacyBudgetVirtualKey holds the legacy budget virtual key model.
type legacyBudgetVirtualKey struct {
	tables.TableVirtualKey
	BudgetID *string `gorm:"column:budget_id;type:varchar(255);index"`
}

// TableName returns the governance_virtual_keys table name for legacyBudgetVirtualKey.
func (legacyBudgetVirtualKey) TableName() string { return "governance_virtual_keys" }

// legacyBudgetVirtualKeyProviderConfig holds the legacy budget virtual key provider config model.
type legacyBudgetVirtualKeyProviderConfig struct {
	tables.TableVirtualKeyProviderConfig
	BudgetID *string `gorm:"column:budget_id;type:varchar(255);index"`
}

// TableName returns the governance_virtual_key_provider_configs table name for legacyBudgetVirtualKeyProviderConfig.
func (legacyBudgetVirtualKeyProviderConfig) TableName() string {
	return "governance_virtual_key_provider_configs"
}

// legacyBudgetTeam holds the legacy budget team model.
type legacyBudgetTeam struct {
	tables.TableTeam
	BudgetID *string `gorm:"column:budget_id;type:varchar(255);index"`
}

// TableName returns the governance_teams table name for legacyBudgetTeam.
func (legacyBudgetTeam) TableName() string { return "governance_teams" }

// sqliteColumnInfo holds the information about a SQLite column.
type sqliteColumnInfo struct {
	Name string `gorm:"column:name"`
}

// legacyBudgetColumnModel returns the legacy budget column model for a given table name.
func legacyBudgetColumnModel(tableName string) (any, error) {
	switch tableName {
	case "governance_virtual_keys":
		return &legacyBudgetVirtualKey{}, nil
	case "governance_virtual_key_provider_configs":
		return &legacyBudgetVirtualKeyProviderConfig{}, nil
	case "governance_teams":
		return &legacyBudgetTeam{}, nil
	default:
		return nil, fmt.Errorf("unsupported legacy budget column drop table: %s", tableName)
	}
}

// currentBudgetOwnerModel returns the current budget owner model for a given table name.
func currentBudgetOwnerModel(tableName string) (any, error) {
	switch tableName {
	case "governance_virtual_keys":
		return &tables.TableVirtualKey{}, nil
	case "governance_virtual_key_provider_configs":
		return &tables.TableVirtualKeyProviderConfig{}, nil
	case "governance_teams":
		return &tables.TableTeam{}, nil
	default:
		return nil, fmt.Errorf("unsupported legacy budget column drop table: %s", tableName)
	}
}

// migrationStep records the migration IDs written by one migration function.
// Most functions write one ID, but a few grouped migrations write multiple IDs.
type migrationStep struct {
	IDs []string
	run func(context.Context, *gorm.DB, schemas.Logger) error
}

// migrationStepIDs flattens migration step IDs in execution order.
func migrationStepIDs(steps []migrationStep) []string {
	ids := make([]string, 0, len(steps))
	for _, step := range steps {
		ids = append(ids, step.IDs...)
	}
	return ids
}

// pendingMigrationStepIDs returns the migration IDs from steps that are not
// recorded in the migration table yet.
func pendingMigrationStepIDs(ctx context.Context, db *gorm.DB, steps []migrationStep) ([]string, error) {
	return migrator.PendingIDs(ctx, db, migrator.DefaultOptions, migrationStepIDs(steps))
}

// runMigrationSteps runs migration steps in their declared order.
func runMigrationSteps(ctx context.Context, db *gorm.DB, logger schemas.Logger, steps []migrationStep) error {
	for _, step := range steps {
		if err := step.run(ctx, db, logger); err != nil {
			return err
		}
	}
	return nil
}

// configstoreMigrationSteps is the ordered source of truth for configstore
// migration execution and preflight checks.
var configstoreMigrationSteps = []migrationStep{
	{IDs: []string{"init"}, run: migrationInit},
	{IDs: []string{"many2manyjoin"}, run: migrationMany2ManyJoinTable},
	{IDs: []string{"addcustomproviderconfigjsoncolumn"}, run: migrationAddCustomProviderConfigJSONColumn},
	{IDs: []string{"addvirtualkeyproviderconfig"}, run: migrationAddVirtualKeyProviderConfigTable},
	{IDs: []string{"add_allowed_origins_json_column"}, run: migrationAddAllowedOriginsJSONColumn},
	{IDs: []string{"add_allow_direct_keys_column"}, run: migrationAddAllowDirectKeysColumn},
	{IDs: []string{"add_enable_litellm_fallbacks_column"}, run: migrationAddEnableLiteLLMFallbacksColumn},
	{IDs: []string{"add_profile_config_claims_columns_to_team_table"}, run: migrationTeamsTableUpdates},
	{IDs: []string{"add_team_source_id_column"}, run: migrationAddTeamSourceIDColumn},
	{IDs: []string{"add_key_name_column"}, run: migrationAddKeyNameColumn},
	{IDs: []string{"add_framework_configs_table"}, run: migrationAddFrameworkConfigsTable},
	{IDs: []string{"cleanup_mcp_client_tools_config"}, run: migrationCleanupMCPClientToolsConfig},
	{IDs: []string{"add_vk_mcp_configs_table"}, run: migrationAddVirtualKeyMCPConfigsTable},
	{IDs: []string{"update_plugins_table_for_custom_plugins"}, run: migrationAddPluginPathColumn},
	{IDs: []string{"add_provider_config_budget_rate_limit"}, run: migrationAddProviderConfigBudgetRateLimit},
	{IDs: []string{"add_sessions_table"}, run: migrationAddSessionsTable},
	{IDs: []string{"add_headers_json_column_into_mcp_client"}, run: migrationAddHeadersJSONColumnIntoMCPClient},
	{IDs: []string{"add_disable_content_logging_column"}, run: migrationAddDisableContentLoggingColumn},
	{IDs: []string{"add_mcp_client_id_column"}, run: migrationAddMCPClientIDColumn},
	{IDs: []string{"add_vertex_project_number_column"}, run: migrationAddVertexProjectNumberColumn},
	{IDs: []string{"add_vertex_deployments_json_column"}, run: migrationAddVertexDeploymentsJSONColumn},
	{IDs: []string{"add_and_fill_provider_column_in_key_table"}, run: migrationMissingProviderColumnInKeyTable},
	{IDs: []string{"add_tools_to_auto_execute_json_column"}, run: migrationAddToolsToAutoExecuteJSONColumn},
	{IDs: []string{"add_is_code_mode_client_column"}, run: migrationAddIsCodeModeClientColumn},
	{IDs: []string{"add_log_retention_days_column"}, run: migrationAddLogRetentionDaysColumn},
	{IDs: []string{"add_enabled_column_to_key_table"}, run: migrationAddEnabledColumnToKeyTable},
	{IDs: []string{"update_model_pricing_table_to_add_cache_and_batch_pricing"}, run: migrationAddBatchAndCachePricingColumns},
	{IDs: []string{"add_mcp_agent_depth_and_mcp_tool_execution_timeout_columns"}, run: migrationAddMCPAgentDepthAndMCPToolExecutionTimeoutColumns},
	{IDs: []string{"add_mcp_code_mode_binding_level_column"}, run: migrationAddMCPCodeModeBindingLevelColumn},
	{IDs: []string{"normalize_mcp_client_names"}, run: migrationNormalizeMCPClientNames},
	{IDs: []string{"move_keys_to_provider_config"}, run: migrationMoveKeysToProviderConfig},
	{IDs: []string{"add_plugin_version_column"}, run: migrationAddPluginVersionColumn},
	{IDs: []string{"add_send_back_raw_request_columns"}, run: migrationAddSendBackRawRequestColumns},
	{IDs: []string{"add_config_hash_column"}, run: migrationAddConfigHashColumn},
	{IDs: []string{"add_virtual_key_config_hash_column"}, run: migrationAddVirtualKeyConfigHashColumn},
	{IDs: []string{"add_additional_config_hash_columns"}, run: migrationAddAdditionalConfigHashColumns},
	{IDs: []string{"add_200k_token_pricing_columns"}, run: migrationAdd200kTokenPricingColumns},
	{IDs: []string{"add_image_pricing_columns"}, run: migrationAddImagePricingColumns},
	{IDs: []string{"add_use_for_batch_api_column"}, run: migrationAddUseForBatchAPIColumnAndS3BucketsConfig},
	{IDs: []string{"add_header_filter_config_json_column"}, run: migrationAddHeaderFilterConfigJSONColumn},
	{IDs: []string{"add_azure_client_id_and_client_secret_and_tenant_id_columns"}, run: migrationAddAzureClientIDAndClientSecretAndTenantIDColumns},
	{IDs: []string{"add_distributed_locks_table"}, run: migrationAddDistributedLocksTable},
	{IDs: []string{"add_model_config_table"}, run: migrationAddModelConfigTable},
	{IDs: []string{"add_provider_governance_columns"}, run: migrationAddProviderGovernanceColumns},
	{IDs: []string{"add_allowed_headers_json_column"}, run: migrationAddAllowedHeadersJSONColumn},
	{IDs: []string{"add_disable_db_pings_in_health_column"}, run: migrationAddDisableDBPingsInHealthColumn},
	{IDs: []string{"add_is_ping_available_column"}, run: migrationAddIsPingAvailableColumnToMCPClientTable},
	{IDs: []string{"add_tool_pricing_json_column"}, run: migrationAddToolPricingJSONColumn},
	{IDs: []string{"remove_server_prefix_from_mcp_tools"}, run: migrationRemoveServerPrefixFromMCPTools},
	{IDs: []string{"add_oauth_tables"}, run: migrationAddOAuthTables},
	{IDs: []string{"add_tool_sync_interval_columns"}, run: migrationAddToolSyncIntervalColumns},
	{IDs: []string{"add_mcp_client_config_to_oauth_config"}, run: migrationAddMCPClientConfigToOAuthConfig},
	{IDs: []string{"add_routing_rules_table"}, run: migrationAddRoutingRulesTable},
	{IDs: []string{"add_base_model_pricing_column"}, run: migrationAddBaseModelPricingColumn},
	{IDs: []string{"add_azure_scopes_column"}, run: migrationAddAzureScopesColumn},
	{IDs: []string{"add_replicate_deployments_json_column"}, run: migrationAddReplicateDeploymentsJSONColumn},
	{IDs: []string{"add_key_status_columns"}, run: migrationAddKeyStatusColumns},
	{IDs: []string{"add_provider_status_columns"}, run: migrationAddProviderStatusColumns},
	{IDs: []string{"add_rate_limit_to_teams_and_customers"}, run: migrationAddRateLimitToTeamsAndCustomers},
	{IDs: []string{"add_async_job_result_ttl_column"}, run: migrationAddAsyncJobResultTTLColumn},
	{IDs: []string{"add_required_headers_json_column"}, run: migrationAddRequiredHeadersJSONColumn},
	{IDs: []string{"add_logging_headers_json_column"}, run: migrationAddLoggingHeadersJSONColumn},
	{IDs: []string{"add_hide_deleted_virtual_keys_in_filters_column"}, run: migrationAddHideDeletedVirtualKeysInFiltersColumn},
	{IDs: []string{"add_enforce_scim_auth_column"}, run: migrationAddEnforceSCIMAuthColumn},
	{IDs: []string{"add_enforce_auth_on_inference_column"}, run: migrationAddEnforceAuthOnInferenceColumn},
	{IDs: []string{"reconcile_pricing_overrides_table"}, run: migrationReconcilePricingOverridesTable},
	{IDs: []string{"add_encryption_columns"}, run: migrationAddEncryptionColumns},
	{IDs: []string{"add_output_cost_per_video_per_second_and_output_cost_per_second_columns"}, run: migrationAddOutputCostPerVideoPerSecond},
	{IDs: []string{"drop_enable_governance_column"}, run: migrationDropEnableGovernanceColumn},
	{IDs: []string{"add_vllm_key_config_columns"}, run: migrationAddVLLMKeyConfigColumns},
	{IDs: []string{"widen_encrypted_varchar_columns"}, run: migrationWidenEncryptedVarcharColumns},
	{IDs: []string{"add_bedrock_assume_role_columns"}, run: migrationAddBedrockAssumeRoleColumns},
	{IDs: []string{"add_store_raw_request_response_column"}, run: migrationAddStoreRawRequestResponseColumn},
	{IDs: []string{"add_pricing_refactor_columns"}, run: migrationAddPricingRefactorColumns},
	{IDs: []string{"rename_truncated_pricing_column"}, run: migrationRenameTruncatedPricingColumn},
	{IDs: []string{"add_image_quality_pricing_columns"}, run: migrationAddImageQualityPricingColumns},
	{IDs: []string{"add_routing_targets_table"}, run: migrationAddRoutingTargetsTable},
	{IDs: []string{"add_prompt_repo_tables", "add_prompt_id_to_prompt_message_tables", "add_model_parameters_table"}, run: migrationAddPromptRepoTables},
	{IDs: []string{"add_plugin_order_columns"}, run: migrationAddPluginOrderColumns},
	{IDs: []string{"add_allow_all_keys_to_provider_config"}, run: migrationAddAllowAllKeysToProviderConfig},
	{IDs: []string{"add_vk_provider_config_blacklisted_models_column"}, run: migrationAddVirtualKeyBlacklistedModelsColumn},
	{IDs: []string{"backfill_empty_virtual_key_configs"}, run: migrationBackfillEmptyVirtualKeyConfigs},
	{IDs: []string{"add_mcp_disable_auto_tool_inject_column"}, run: migrationAddMCPDisableAutoToolInjectColumn},
	{IDs: []string{"add_mcp_enable_temp_token_auth_column"}, run: migrationAddMCPEnableTempTokenAuthColumn},
	{IDs: []string{"backfill_allowed_models_wildcard"}, run: migrationBackfillAllowedModelsWildcard},
	{IDs: []string{"add_mcp_client_allowed_extra_headers_json_column"}, run: migrationAddMCPClientAllowedExtraHeadersJSONColumn},
	{IDs: []string{"make_base_pricing_columns_nullable"}, run: migrationMakeBasePricingColumnsNullable},
	{IDs: []string{"add_allow_on_all_virtual_keys_column"}, run: migrationAddAllowOnAllVirtualKeysColumn},
	{IDs: []string{"add_open_ai_config_json_column"}, run: migrationAddOpenAIConfigJSONColumn},
	{IDs: []string{"add_key_blacklisted_models_json_column"}, run: migrationAddKeyBlacklistedModelsJSONColumn},
	{IDs: []string{"add_chain_rule_column_to_routing_rules"}, run: migrationAddChainRuleColumnToRoutingRules},
	{IDs: []string{"drop_deployment_columns_and_add_aliases"}, run: migrationDropDeploymentColumnsAndAddAliases},
	{IDs: []string{"add_replicate_key_config_column"}, run: migrationAddReplicateKeyConfigColumn},
	{IDs: []string{"add_budget_calendar_aligned_column"}, run: migrationAddBudgetCalendarAlignedColumn},
	{IDs: []string{"add_routing_chain_max_depth_column"}, run: migrationAddRoutingChainMaxDepthColumn},
	{IDs: []string{"add_prompt_variables_columns"}, run: migrationAddPromptVariablesColumns},
	{IDs: []string{"add_model_capability_columns"}, run: migrationAddModelCapabilityColumns},
	{IDs: []string{"add_ollama_sgl_config_columns"}, run: migrationAddOllamaSGLConfigColumns},
	{IDs: []string{"add_multi_budget_tables"}, run: migrationAddMultiBudgetTables},
	{IDs: []string{"add_per_user_oauth_tables"}, run: migrationAddPerUserOAuthTables},
	{IDs: []string{"add_mcp_client_discovered_tools_columns"}, run: migrationAddMCPClientDiscoveredToolsColumns},
	{IDs: []string{"add_whitelisted_routes_json_column"}, run: migrationAddWhitelistedRoutesJSONColumn},
	{IDs: []string{"replace_enable_litellm_with_compat_columns"}, run: migrationReplaceEnableLiteLLMWithCompatColumns},
	{IDs: []string{"add_model_pricing_unique_index"}, run: migrationAddModelPricingUniqueIndex},
	{IDs: []string{"default_compat_should_convert_params_false"}, run: migrationDefaultCompatShouldConvertParamsFalse},
	{IDs: []string{"add_priority_tier_pricing_columns"}, run: migrationAddPriorityTierPricingColumns},
	{IDs: []string{"add_flex_tier_pricing_columns"}, run: migrationAddFlexTierPricingColumns},
	{IDs: []string{"normalize_otel_trace_type"}, run: migrationNormalizeOtelTraceType},
	{IDs: []string{"migrate_calendar_aligned"}, run: migrateCalendarAlignedToBudgetsAndRateLimitsTable},
	{IDs: []string{"add_team_budgets_to_budgets_table"}, run: migrationAddTeamBudgetsToBudgetsTable},
	{IDs: []string{"add_ocr_pricing_columns"}, run: migrationAddOCRPricingColumns},
	{IDs: []string{"convert_mcp_client_tool_sync_interval_minutes_to_seconds"}, run: migrationConvertMCPClientToolSyncIntervalMinutesToSeconds},
	{IDs: []string{"add_mcp_external_base_url_column"}, run: migrationAddMCPExternalBaseURLColumn},
	{IDs: []string{"split_mcp_external_base_url_into_server_client"}, run: migrationSplitMCPExternalBaseURL},
	{IDs: []string{"make_oauth_token_expiry_nullable"}, run: migrationMakeOAuthTokenExpiryNullable},
	{IDs: []string{"add_allow_per_request_content_storage_override_column"}, run: migrationAddAllowPerRequestContentStorageOverrideColumn},
	{IDs: []string{"add_allow_per_request_raw_override_column"}, run: migrationAddAllowPerRequestRawOverrideColumn},
	{IDs: []string{"add_mcp_client_disabled_column"}, run: migrationAddMCPClientDisabledColumn},
	{IDs: []string{"gov_unique_team_names"}, run: migrationUniqueTeamNames},
	{IDs: []string{"drop_allow_direct_keys_column"}, run: migrationDropAllowDirectKeysColumn},
	{IDs: []string{"drop_allow_direct_keys_column_ddl"}, run: migrationDropAllowDirectKeysColumnDDL},
	{IDs: []string{"add_oauth_auth_mode_columns"}, run: migrationAddOAuthAuthModeColumns},
	{IDs: []string{"replace_oauth_session_token_with_session_id"}, run: migrationReplaceOauthSessionTokenWithSessionID},
	{IDs: []string{"drop_legacy_oauth_server_tables"}, run: migrationDropLegacyOAuthServerTables},
	{IDs: []string{"drop_non_vk_oauth_user_rows"}, run: migrationDropNonVKOauthUserRows},
	{IDs: []string{"drop_mcp_external_server_url_column"}, run: migrationDropMCPExternalServerURL},
	{IDs: []string{"add_team_calendar_aligned_column"}, run: migrationAddTeamCalendarAlignedColumn},
	{IDs: []string{"drop_legacy_calendar_aligned_columns"}, run: migrationDropLegacyCalendarAlignedColumns},
	{IDs: []string{"add_vk_access_profile_id_column"}, run: migrationAddVKAccessProfileIDColumn},
	{IDs: []string{"drop_vk_access_profile_id_column"}, run: migrationDropVKAccessProfileIDColumn},
	{IDs: []string{"add_feature_flags_table"}, run: migrationAddFeatureFlagsTable},
	{IDs: []string{"add_framework_config_hash_column"}, run: migrationAddFrameworkConfigHashColumn},
	{IDs: []string{"add_model_parameters_url_column"}, run: migrationAddModelParametersURLColumn},
	{IDs: []string{"add_client_config_metadata_json_column"}, run: migrationAddClientConfigMetadataColumn},
	{IDs: []string{"add_temp_tokens_table"}, run: migrationAddTempTokensTable},
	{IDs: []string{"backfill_vk_provider_config_blacklisted_models"}, run: migrationBackfillVirtualKeyBlacklistedModels},
	{IDs: []string{"add_created_by_user_id_column_for_virtual_keys"}, run: migrationAddCreatedByUserIDColumnForVirtualKeys},
	{IDs: []string{"re_add_allow_direct_keys_column"}, run: migrationReAddAllowDirectKeysColumn},
	{IDs: []string{"refresh_config_hash_after_mcp_external_server_url_removal"}, run: migrationRefreshConfigHashAfterMCPExternalServerURLRemoval},
	{IDs: []string{"drop_azure_api_version_column"}, run: migrationDropAzureAPIVersionColumn},
	{IDs: []string{"add_mcp_per_user_header_credentials_table"}, run: migrationAddPerUserHeadersTables},
	{IDs: []string{"add_mcp_per_user_header_flows_table"}, run: migrationAddPerUserHeadersFlowsTable},
	{IDs: []string{"add_mcp_client_tls_config_json_column"}, run: migrationAddMCPClientTLSConfigColumn},
	{IDs: []string{"add_additional_attributes_to_pricing"}, run: migrationAddAdditionalAttributesToPricing},
	{IDs: []string{"add_model_config_scope_columns"}, run: migrationAddModelConfigScopeColumns},
	{IDs: []string{"migrate_provider_governance_to_model_configs"}, run: migrationMigrateProviderGovernanceToModelConfigs},
	{IDs: []string{"add_budget_model_config_id_column"}, run: migrationAddBudgetModelConfigIDColumn},
	{IDs: []string{"add_model_config_calendar_aligned_column"}, run: migrationAddModelConfigCalendarAlignedColumn},
	{IDs: []string{"migrate_virtual_key_governance_to_model_configs"}, run: migrationMigrateVirtualKeyGovernanceToModelConfigs},
	{IDs: []string{"add_customer_calendar_aligned_column"}, run: migrationAddCustomerCalendarAlignedColumn},
	{IDs: []string{"add_customer_budgets_to_budgets_table"}, run: migrationAddCustomerBudgetsToBudgetsTable},
	{IDs: []string{"add_model_config_budgets_fk_constraint"}, run: migrationAddModelConfigBudgetsFKConstraint},
	{IDs: []string{"add_mcp_library_table"}, run: migrationAddMCPLibraryTable},
	{IDs: []string{"add_mcp_library_config_columns"}, run: migrationAddMCPLibraryConfigColumns},
	{IDs: []string{"add_mcp_library_source_columns"}, run: migrationAddMCPLibrarySourceColumns},
	{IDs: []string{"add_fast_mode_pricing_columns"}, run: migrationAddFastModePricingColumns},
	{IDs: []string{"add_customer_name_unique_constraint_dedup", "add_customer_name_unique_constraint_index"}, run: migrationAddCustomerNameUniqueConstraint},
	{IDs: []string{"null_legacy_customer_budget_id_refs"}, run: migrationNullLegacyCustomerBudgetID},
	{IDs: []string{"add_skills_repo_tables"}, run: migrationAddSkillsRepoTables},
	{IDs: []string{"add_oauth2_server_tables"}, run: migrationAddOAuth2ServerTables},
	{IDs: []string{"add_oauth2_issuance_tables"}, run: migrationAddOAuth2IssuanceTables},
	{IDs: []string{"add_dump_errors_in_console_logs_column"}, run: migrationAddDumpErrorsInConsoleLogsColumn},
	{IDs: []string{"add_bedrock_mantle_key_columns"}, run: migrationAddBedrockMantleKeyColumns},
	{IDs: []string{"add_model_pricing_is_deprecated_column"}, run: migrationAddModelPricingIsDeprecatedColumn},
	{IDs: []string{"add_mcp_client_tool_execution_timeout_column"}, run: migrationAddMCPClientToolExecutionTimeoutColumn},
	{IDs: []string{"add_virtual_key_expires_at_column"}, run: migrationAddVirtualKeyExpiresAtColumn},
	{IDs: []string{"add_fast_mode_cache_pricing_columns"}, run: migrationAddFastModeCachePricingColumns},
	{IDs: []string{"add_inference_geo_multiplier_column"}, run: migrationAddInferenceGeoMultiplierColumn},
	{IDs: []string{"add_flex_and_cache_creation_272k_pricing_columns"}, run: migrationAddFlexAndCacheCreation272kPricingColumns},
	{IDs: []string{"add_vertex_force_single_region_column"}, run: migrationAddVertexForceSingleRegionColumn},
	{IDs: []string{"add_sidekiq_table"}, run: migrationAddSidekiqTable},
	{IDs: []string{"add_sidekiq_kind_status_created_index"}, run: migrationAddSidekiqKindStatusCreatedIndex},
	{IDs: []string{"add_fast_mode_cache_pricing_columns"}, run: migrationAddFastModeCachePricingColumns},
	{IDs: []string{"add_inference_geo_multiplier_column"}, run: migrationAddInferenceGeoMultiplierColumn},
	{IDs: []string{"repair_bare_wildcard_allowed_models"}, run: migrationRepairBareWildcardAllowedModels},
	{IDs: []string{"add_bedrock_project_id_columns"}, run: migrationAddBedrockProjectIDColumns},
	{IDs: []string{"add_dual_credential_conflict_behavior_column"}, run: migrationAddDualCredentialConflictBehaviorColumn},
	{IDs: []string{"add_webhook_endpoints_table"}, run: migrationAddWebhookEndpointsTable},
	{IDs: []string{"add_webhook_jobs_table"}, run: migrationAddWebhookJobsTable},
	{IDs: []string{"add_webhook_config_client_column"}, run: migrationAddWebhookConfigClientColumn},
	{IDs: []string{"add_oauth_config_resource_column"}, run: migrationAddOauthConfigResourceColumn},
	{IDs: []string{"add_use_anthropic_endpoints_column"}, run: migrationAddUseAnthropicEndpointsColumn},
}

// quoteSQLiteIdentifier quotes a SQLite identifier, escaping any double quotes.
func quoteSQLiteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// sqliteTableColumns returns the column names of a SQLite table.
func sqliteTableColumns(tx *gorm.DB, tableName string) ([]string, error) {
	var columns []sqliteColumnInfo
	query := fmt.Sprintf("PRAGMA table_info(%s)", quoteSQLiteIdentifier(tableName))
	if err := tx.Raw(query).Scan(&columns).Error; err != nil {
		return nil, err
	}

	result := make([]string, 0, len(columns))
	for _, column := range columns {
		result = append(result, column.Name)
	}
	return result, nil
}

// sqliteTableHasColumn checks if a SQLite table has a column with the given name.
func sqliteTableHasColumn(tx *gorm.DB, tableName, columnName string) (bool, error) {
	columns, err := sqliteTableColumns(tx, tableName)
	if err != nil {
		return false, err
	}
	if slices.Contains(columns, columnName) {
		return true, nil
	}
	return false, nil
}

// hasColumn checks if a table has a column with the given name.
// Returns the introspection error rather than collapsing it to false so callers
// can distinguish "column missing" from "could not determine".
func hasColumn(tx *gorm.DB, table, column string) (bool, error) {
	var count int64
	var q string
	switch tx.Dialector.Name() {
	case "sqlite":
		q = `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`
	default:
		q = `SELECT COUNT(*) FROM information_schema.columns WHERE table_name = ? AND column_name = ? AND table_schema = current_schema()`
	}
	if err := tx.Raw(q, table, column).Scan(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// sqliteDropLegacyBudgetColumn removes the legacy budget_id column from a
// SQLite table by dumping data, recreating the table from the current GORM
// model, and copying data back.
//
// Strategy: dump-data → drop-original → create-clean → restore-data.
// We never RENAME the original table because SQLite propagates ALTER TABLE
// RENAME into FK references in OTHER tables, corrupting them.
func sqliteDropLegacyBudgetColumn(tx *gorm.DB, tableName string) error {
	model, err := currentBudgetOwnerModel(tableName)
	if err != nil {
		return err
	}

	columns, err := sqliteTableColumns(tx, tableName)
	if err != nil {
		return fmt.Errorf("failed to inspect SQLite columns for %s: %w", tableName, err)
	}

	preservedColumns := make([]string, 0, len(columns))
	for _, column := range columns {
		if column != "budget_id" {
			preservedColumns = append(preservedColumns, column)
		}
	}
	if len(preservedColumns) == len(columns) {
		return nil // budget_id column not present, nothing to do
	}

	// Build the column list for data transfer.
	quotedColumns := make([]string, 0, len(preservedColumns))
	for _, column := range preservedColumns {
		quotedColumns = append(quotedColumns, quoteSQLiteIdentifier(column))
	}
	columnList := strings.Join(quotedColumns, ", ")

	// Dump existing data into a temporary table (data-only, no constraints).
	dumpTable := tableName + "__dump"
	if err := tx.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", quoteSQLiteIdentifier(dumpTable))).Error; err != nil {
		return fmt.Errorf("failed to drop stale dump table %s: %w", dumpTable, err)
	}
	dumpSQL := fmt.Sprintf("CREATE TABLE %s AS SELECT %s FROM %s",
		quoteSQLiteIdentifier(dumpTable), columnList, quoteSQLiteIdentifier(tableName))
	if err := tx.Exec(dumpSQL).Error; err != nil {
		return fmt.Errorf("failed to dump %s data: %w", tableName, err)
	}

	// Drop the original table. Safe because PRAGMA foreign_keys is OFF.
	// This also removes all indexes and FK definitions cleanly.
	if err := tx.Exec(fmt.Sprintf("DROP TABLE %s", quoteSQLiteIdentifier(tableName))).Error; err != nil {
		return fmt.Errorf("failed to drop original SQLite table %s: %w", tableName, err)
	}

	// Recreate the table from the current GORM model (no budget_id column,
	// proper indexes and constraints). The original table name is now free.
	if err := tx.Migrator().CreateTable(model); err != nil {
		return fmt.Errorf("failed to recreate SQLite table %s: %w", tableName, err)
	}

	// Restore data from the dump.
	restoreSQL := fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM %s",
		quoteSQLiteIdentifier(tableName), columnList, columnList, quoteSQLiteIdentifier(dumpTable))
	if err := tx.Exec(restoreSQL).Error; err != nil {
		return fmt.Errorf("failed to restore data into %s: %w", tableName, err)
	}

	// Clean up the dump table.
	if err := tx.Exec(fmt.Sprintf("DROP TABLE %s", quoteSQLiteIdentifier(dumpTable))).Error; err != nil {
		return fmt.Errorf("failed to drop dump table %s: %w", dumpTable, err)
	}
	return nil
}

// dropColumnSQL returns a dialect-correct `ALTER TABLE ... DROP COLUMN` statement.
// Postgres supports (and we use) the `IF EXISTS` guard so the drop is a no-op when the
// column is already gone; SQLite's ALTER TABLE grammar has no `IF EXISTS` clause and
// errors with `near "EXISTS": syntax error`, so we emit a plain DROP COLUMN there.
// Callers must still guard the SQLite path with a HasColumn check, since plain DROP
// COLUMN errors on a missing column.
func dropColumnSQL(tx *gorm.DB, table, column string) string {
	if tx.Dialector.Name() == "postgres" {
		return fmt.Sprintf("ALTER TABLE %s DROP COLUMN IF EXISTS %s", table, column)
	}
	return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", table, column)
}

func dropLegacyBudgetColumn(tx *gorm.DB, tableName string) error {
	mg := tx.Migrator()
	if !mg.HasColumn(tableName, "budget_id") {
		return nil
	}

	if tx.Dialector.Name() == "sqlite" {
		if err := sqliteDropLegacyBudgetColumn(tx, tableName); err != nil {
			return err
		}
	} else {
		model, err := legacyBudgetColumnModel(tableName)
		if err != nil {
			return err
		}
		if err := dropColumnIfExists(tx, nil, model, "budget_id"); err != nil {
			return fmt.Errorf("failed to drop legacy %s.budget_id column: %w", tableName, err)
		}
	}

	var stillExists bool
	var err error
	if tx.Dialector.Name() == "sqlite" {
		stillExists, err = sqliteTableHasColumn(tx, tableName, "budget_id")
		if err != nil {
			return fmt.Errorf("failed to verify legacy %s.budget_id column drop: %w", tableName, err)
		}
	} else {
		stillExists = mg.HasColumn(tableName, "budget_id")
	}
	if stillExists {
		return fmt.Errorf("legacy %s.budget_id column still exists after migration", tableName)
	}
	return nil
}

// areThereAnyPendingMigrations returns true if there are any pending migrations to be applied.
func areThereAnyPendingMigrations(ctx context.Context, db *gorm.DB, logger schemas.Logger) bool {
	pending, err := pendingMigrationStepIDs(ctx, db, configstoreMigrationSteps)
	if err != nil {
		logger.Warn("[configstore] migration preflight failed; acquiring migration lock and running migrations: %v", err)
	}
	// Fail open: on a preflight error we proceed to acquire the lock and run
	// migrations rather than silently skipping them (matches logstore and the
	// warn log above). Only a clean "nothing pending" result skips the run.
	return err != nil || len(pending) > 0
}

// Migrate performs the necessary database migrations.
func triggerMigrations(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	if !areThereAnyPendingMigrations(ctx, db, logger) {
		logger.Info("[configstore] no pending migrations; skipping migration run")
		return nil
	}
	// Acquire advisory lock to serialize migrations across cluster nodes.
	// This prevents race conditions when multiple nodes start simultaneously
	// and try to create the same tables in parallel.
	lock, err := acquireMigrationLock(ctx, db, logger)
	if err != nil {
		return err
	}
	defer lock.release(ctx)
	// Checking again if there are any pending migrations after acquiring the lock.
	if !areThereAnyPendingMigrations(ctx, db, logger) {
		logger.Info("[configstore] no pending migrations after lock acquisition; skipping migration run")
		return nil
	}
	if db.Dialector.Name() == "postgres" {
		pending, err := pendingMigrationStepIDs(ctx, db, configstoreMigrationSteps)
		if err == nil && len(pending) == 0 {
			logger.Info("[configstore] migrations completed by another node; skipping migration run")
			return nil
		}
		if err != nil {
			logger.Warn("[configstore] migration preflight after lock failed; running migrations: %v", err)
		}
	}
	return runMigrationSteps(ctx, db, logger, configstoreMigrationSteps)
}

// migrationAddClientConfigMetadataColumn adds the metadata_json column to
// config_client. The column stores a JSON blob of UI/admin preferences (e.g.
// onboarding_dismissed) and is deliberately not part of the ClientConfig API
// struct, so config.json sync cannot overwrite it.
func migrationAddClientConfigMetadataColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_client_config_metadata_json_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "metadata_json"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "metadata_json"); err != nil {
				return err
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

// migrationAddFeatureFlagsTable creates the feature_flags table holding
// user-toggled overrides for the in-memory featureflags registry.
func migrationAddFeatureFlagsTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_feature_flags_table"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasTable(&tables.TableFeatureFlag{}) {
				logger.Info("[configstore] %s: creating table TableFeatureFlag", migrationName)
				if err := migrator.CreateTable(&tables.TableFeatureFlag{}); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

func migrationAddStoreRawRequestResponseColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_store_raw_request_response_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableProvider{}, "store_raw_request_response"); err != nil {
				return err
			}
			// Backfill config_hash for existing providers so they don't appear
			// dirty after upgrade. StoreRawRequestResponse is now part of the
			// hash input; rows written before this migration have stale hashes.
			var providers []tables.TableProvider
			if err := tx.
				Select(
					"id",
					"name",
					"network_config_json",
					"concurrency_buffer_json",
					"proxy_config_json",
					"custom_provider_config_json",
					"send_back_raw_request",
					"send_back_raw_response",
					"store_raw_request_response",
					"encryption_status",
				).
				Find(&providers).Error; err != nil {
				return fmt.Errorf("failed to fetch providers for hash backfill: %w", err)
			}
			logger.Info("[configstore] %s: processing %d providers", migrationName, len(providers))
			for _, provider := range providers {
				providerConfig := ProviderConfig{
					NetworkConfig:            provider.NetworkConfig,
					ConcurrencyAndBufferSize: provider.ConcurrencyAndBufferSize,
					ProxyConfig:              provider.ProxyConfig,
					SendBackRawRequest:       provider.SendBackRawRequest,
					SendBackRawResponse:      provider.SendBackRawResponse,
					StoreRawRequestResponse:  provider.StoreRawRequestResponse,
					CustomProviderConfig:     provider.CustomProviderConfig,
				}
				// Here the default value of store_raw_request_response should be based on the default value of SendBackRawRequest and SendBackRawResponse
				if provider.SendBackRawRequest || provider.SendBackRawResponse {
					providerConfig.StoreRawRequestResponse = true
				}
				hash, err := providerConfig.GenerateConfigHash(provider.Name)
				if err != nil {
					return fmt.Errorf("failed to generate hash for provider %s: %w", provider.Name, err)
				}
				if err := tx.Model(&provider).Updates(map[string]interface{}{
					"config_hash":                hash,
					"store_raw_request_response": providerConfig.StoreRawRequestResponse,
				}).Error; err != nil {
					return fmt.Errorf("failed to update hash for provider %s: %w", provider.Name, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableProvider{}, "store_raw_request_response"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running add store raw request response column migration: %s", err.Error())
	}
	return nil
}

// migrationInit is the first migration
func migrationInit(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "init"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasTable(&tables.TableConfigHash{}) {
				logger.Info("[configstore] %s: creating table TableConfigHash", migrationName)
				if err := migrator.CreateTable(&tables.TableConfigHash{}); err != nil {
					return err
				}
			}
			// TableBudget and TableRateLimit must be created before TableProvider
			// because TableProvider has FK references to them
			if !migrator.HasTable(&tables.TableBudget{}) {
				logger.Info("[configstore] %s: creating table TableBudget", migrationName)
				if err := migrator.CreateTable(&tables.TableBudget{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableRateLimit{}) {
				logger.Info("[configstore] %s: creating table TableRateLimit", migrationName)
				if err := migrator.CreateTable(&tables.TableRateLimit{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableProvider{}) {
				logger.Info("[configstore] %s: creating table TableProvider", migrationName)
				if err := migrator.CreateTable(&tables.TableProvider{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableKey{}) {
				logger.Info("[configstore] %s: creating table TableKey", migrationName)
				if err := migrator.CreateTable(&tables.TableKey{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableModel{}) {
				logger.Info("[configstore] %s: creating table TableModel", migrationName)
				if err := migrator.CreateTable(&tables.TableModel{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableOauthConfig{}) {
				logger.Info("[configstore] %s: creating table TableOauthConfig", migrationName)
				if err := migrator.CreateTable(&tables.TableOauthConfig{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableOauthToken{}) {
				logger.Info("[configstore] %s: creating table TableOauthToken", migrationName)
				if err := migrator.CreateTable(&tables.TableOauthToken{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableMCPClient{}) {
				logger.Info("[configstore] %s: creating table TableMCPClient", migrationName)
				if err := migrator.CreateTable(&tables.TableMCPClient{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableClientConfig{}) {
				logger.Info("[configstore] %s: creating table TableClientConfig", migrationName)
				if err := migrator.CreateTable(&tables.TableClientConfig{}); err != nil {
					return err
				}
			} else if !migrator.HasColumn(&tables.TableClientConfig{}, "max_request_body_size_mb") {
				if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "max_request_body_size_mb"); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableEnvKey{}) {
				logger.Info("[configstore] %s: creating table TableEnvKey", migrationName)
				if err := migrator.CreateTable(&tables.TableEnvKey{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableVectorStoreConfig{}) {
				logger.Info("[configstore] %s: creating table TableVectorStoreConfig", migrationName)
				if err := migrator.CreateTable(&tables.TableVectorStoreConfig{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableLogStoreConfig{}) {
				logger.Info("[configstore] %s: creating table TableLogStoreConfig", migrationName)
				if err := migrator.CreateTable(&tables.TableLogStoreConfig{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableCustomer{}) {
				logger.Info("[configstore] %s: creating table TableCustomer", migrationName)
				if err := migrator.CreateTable(&tables.TableCustomer{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableTeam{}) {
				logger.Info("[configstore] %s: creating table TableTeam", migrationName)
				if err := migrator.CreateTable(&tables.TableTeam{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableVirtualKey{}) {
				logger.Info("[configstore] %s: creating table TableVirtualKey", migrationName)
				if err := migrator.CreateTable(&tables.TableVirtualKey{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableGovernanceConfig{}) {
				logger.Info("[configstore] %s: creating table TableGovernanceConfig", migrationName)
				if err := migrator.CreateTable(&tables.TableGovernanceConfig{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableModelPricing{}) {
				logger.Info("[configstore] %s: creating table TableModelPricing", migrationName)
				if err := migrator.CreateTable(&tables.TableModelPricing{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TablePricingOverride{}) {
				logger.Info("[configstore] %s: creating table TablePricingOverride", migrationName)
				if err := migrator.CreateTable(&tables.TablePricingOverride{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TablePlugin{}) {
				logger.Info("[configstore] %s: creating table TablePlugin", migrationName)
				if err := migrator.CreateTable(&tables.TablePlugin{}); err != nil {
					return err
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			// Drop children first, then parents (adjust if your actual FKs differ)
			logger.Info("[configstore] %s: dropping table TableVirtualKey", migrationName)
			if err := migrator.DropTable(&tables.TableVirtualKey{}); err != nil {
				return err
			}
			logger.Info("[configstore] %s: dropping table TableKey", migrationName)
			if err := migrator.DropTable(&tables.TableKey{}); err != nil {
				return err
			}
			logger.Info("[configstore] %s: dropping table TableTeam", migrationName)
			if err := migrator.DropTable(&tables.TableTeam{}); err != nil {
				return err
			}
			logger.Info("[configstore] %s: dropping table TableProvider", migrationName)
			if err := migrator.DropTable(&tables.TableProvider{}); err != nil {
				return err
			}
			logger.Info("[configstore] %s: dropping table TableCustomer", migrationName)
			if err := migrator.DropTable(&tables.TableCustomer{}); err != nil {
				return err
			}
			logger.Info("[configstore] %s: dropping table TableBudget", migrationName)
			if err := migrator.DropTable(&tables.TableBudget{}); err != nil {
				return err
			}
			logger.Info("[configstore] %s: dropping table TableRateLimit", migrationName)
			if err := migrator.DropTable(&tables.TableRateLimit{}); err != nil {
				return err
			}
			logger.Info("[configstore] %s: dropping table TableModel", migrationName)
			if err := migrator.DropTable(&tables.TableModel{}); err != nil {
				return err
			}
			logger.Info("[configstore] %s: dropping table TableMCPClient", migrationName)
			if err := migrator.DropTable(&tables.TableMCPClient{}); err != nil {
				return err
			}
			logger.Info("[configstore] %s: dropping table TableClientConfig", migrationName)
			if err := migrator.DropTable(&tables.TableClientConfig{}); err != nil {
				return err
			}
			logger.Info("[configstore] %s: dropping table TableEnvKey", migrationName)
			if err := migrator.DropTable(&tables.TableEnvKey{}); err != nil {
				return err
			}
			logger.Info("[configstore] %s: dropping table TableVectorStoreConfig", migrationName)
			if err := migrator.DropTable(&tables.TableVectorStoreConfig{}); err != nil {
				return err
			}
			logger.Info("[configstore] %s: dropping table TableLogStoreConfig", migrationName)
			if err := migrator.DropTable(&tables.TableLogStoreConfig{}); err != nil {
				return err
			}
			logger.Info("[configstore] %s: dropping table TableGovernanceConfig", migrationName)
			if err := migrator.DropTable(&tables.TableGovernanceConfig{}); err != nil {
				return err
			}
			logger.Info("[configstore] %s: dropping table TableModelPricing", migrationName)
			if err := migrator.DropTable(&tables.TableModelPricing{}); err != nil {
				return err
			}
			logger.Info("[configstore] %s: dropping table TablePricingOverride", migrationName)
			if err := migrator.DropTable(&tables.TablePricingOverride{}); err != nil {
				return err
			}
			logger.Info("[configstore] %s: dropping table TablePlugin", migrationName)
			if err := migrator.DropTable(&tables.TablePlugin{}); err != nil {
				return err
			}
			logger.Info("[configstore] %s: dropping table TableConfigHash", migrationName)
			if err := migrator.DropTable(&tables.TableConfigHash{}); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

// createMany2ManyJoinTable creates a many-to-many join table for the given tables.
func migrationMany2ManyJoinTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "many2manyjoin"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			// create the many-to-many join table for virtual keys and keys
			if !migrator.HasTable("governance_virtual_key_keys") {
				createJoinTableSQL := `
					CREATE TABLE IF NOT EXISTS governance_virtual_key_keys (
						table_virtual_key_id VARCHAR(255) NOT NULL,
						table_key_id INTEGER NOT NULL,
						PRIMARY KEY (table_virtual_key_id, table_key_id),
						FOREIGN KEY (table_virtual_key_id) REFERENCES governance_virtual_keys(id) ON DELETE CASCADE,
						FOREIGN KEY (table_key_id) REFERENCES config_keys(id) ON DELETE CASCADE
					)
				`
				logger.Info("[configstore] adding join table for governance_virtual_key_keys: %s", migrationName)
				if err := tx.Exec(createJoinTableSQL).Error; err != nil {
					return fmt.Errorf("failed to create governance_virtual_key_keys table: %w", err)
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			logger.Info("[configstore] dropping join table for governance_virtual_key_keys: %s", migrationName)
			if err := tx.Exec("DROP TABLE IF EXISTS governance_virtual_key_keys").Error; err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

// migrationAddCustomProviderConfigJSONColumn adds the custom_provider_config_json column to the provider table
func migrationAddCustomProviderConfigJSONColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "addcustomproviderconfigjsoncolumn"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := addColumnIfNotExists(tx, logger, &tables.TableProvider{}, "custom_provider_config_json"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

// migrationAddVirtualKeyProviderConfigTable adds the virtual_key_provider_config table
func migrationAddVirtualKeyProviderConfigTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "addvirtualkeyproviderconfig"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			if !migrator.HasTable(&tables.TableVirtualKeyProviderConfig{}) {
				logger.Info("[configstore] %s: creating table TableVirtualKeyProviderConfig", migrationName)
				if err := migrator.CreateTable(&tables.TableVirtualKeyProviderConfig{}); err != nil {
					return err
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			logger.Info("[configstore] %s: dropping table TableVirtualKeyProviderConfig", migrationName)
			if err := migrator.DropTable(&tables.TableVirtualKeyProviderConfig{}); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

// migrationAddAllowedOriginsJSONColumn adds the allowed_origins_json column to the client config table
// migrationAddBedrockMantleKeyColumns adds the bedrock_mantle_* SigV4 credential columns to the
// config_keys table for the standalone bedrock_mantle provider.
func migrationAddBedrockMantleKeyColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_bedrock_mantle_key_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	cols := []string{
		"bedrock_mantle_access_key",
		"bedrock_mantle_secret_key",
		"bedrock_mantle_session_token",
		"bedrock_mantle_region",
		"bedrock_mantle_role_arn",
		"bedrock_mantle_external_id",
		"bedrock_mantle_role_session_name",
	}
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			for _, col := range cols {
				if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, col); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			for _, col := range cols {
				if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, col); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

// migrationAddBedrockProjectIDColumns adds the bedrock_project_id and bedrock_mantle_project_id
// columns to the config_keys table. These scope Bedrock Mantle inference / model listing to a
// specific Bedrock project via the OpenAI-Project / anthropic-workspace-id header.
func migrationAddBedrockProjectIDColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_bedrock_project_id_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	cols := []string{
		"bedrock_project_id",
		"bedrock_mantle_project_id",
	}
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			for _, col := range cols {
				if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, col); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			for _, col := range cols {
				if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, col); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

func migrationAddAllowedOriginsJSONColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_allowed_origins_json_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "allowed_origins_json"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

// migrationAddAllowDirectKeysColumn adds the allow_direct_keys column to the client config table.
// Use raw SQL since the struct field was removed in v1.5 when the feature was retired.
// This column is subsequently dropped by migrationDropAllowDirectKeysColumn.
func migrationAddAllowDirectKeysColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_allow_direct_keys_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&tables.TableClientConfig{}, "allow_direct_keys") {
				if err := tx.Exec("ALTER TABLE config_client ADD COLUMN allow_direct_keys BOOLEAN DEFAULT FALSE").Error; err != nil {
					return err
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

// migrationDropAllowDirectKeysColumn recomputes config_hash after the
// AllowDirectKeys field was removed from GenerateClientConfigHash in v1.5.
// Without this, every existing config_hash would mismatch on first startup
// and trigger a spurious config-reload cycle.
//
// The actual DROP COLUMN is handled by migrationDropAllowDirectKeysColumnDDL
// so that the DDL (AccessExclusiveLock) never shares a transaction with the
// SELECT + UPDATE on the same table — that combination was observed to lock
// config_client indefinitely on contended Postgres instances.
func migrationDropAllowDirectKeysColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "drop_allow_direct_keys_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			var clientConfigs []tables.TableClientConfig
			if err := tx.Find(&clientConfigs).Error; err != nil {
				return fmt.Errorf("failed to fetch client configs for hash recompute: %w", err)
			}
			logger.Info("[configstore] %s: processing %d clientConfigs", migrationName, len(clientConfigs))
			for _, cc := range clientConfigs {
				if cc.ConfigHash == "" {
					continue
				}
				clientConfig := ClientConfig{
					DropExcessRequests:                    cc.DropExcessRequests,
					InitialPoolSize:                       cc.InitialPoolSize,
					PrometheusLabels:                      cc.PrometheusLabels,
					EnableLogging:                         cc.EnableLogging,
					DisableContentLogging:                 cc.DisableContentLogging,
					AllowPerRequestContentStorageOverride: cc.AllowPerRequestContentStorageOverride,
					AllowPerRequestRawOverride:            cc.AllowPerRequestRawOverride,
					DisableDBPingsInHealth:                cc.DisableDBPingsInHealth,
					LogRetentionDays:                      cc.LogRetentionDays,
					EnforceAuthOnInference:                cc.EnforceAuthOnInference,
					AllowedOrigins:                        cc.AllowedOrigins,
					AllowedHeaders:                        cc.AllowedHeaders,
					MaxRequestBodySizeMB:                  cc.MaxRequestBodySizeMB,
					MCPAgentDepth:                         cc.MCPAgentDepth,
					MCPToolExecutionTimeout:               cc.MCPToolExecutionTimeout,
					MCPCodeModeBindingLevel:               cc.MCPCodeModeBindingLevel,
					MCPToolSyncInterval:                   cc.MCPToolSyncInterval,
					MCPDisableAutoToolInject:              cc.MCPDisableAutoToolInject,
					MCPEnableTempTokenAuth:                cc.MCPEnableTempTokenAuth,
					HeaderFilterConfig:                    cc.HeaderFilterConfig,
					AsyncJobResultTTL:                     cc.AsyncJobResultTTL,
					RequiredHeaders:                       cc.RequiredHeaders,
					LoggingHeaders:                        cc.LoggingHeaders,
					WhitelistedRoutes:                     cc.WhitelistedRoutes,
					HideDeletedVirtualKeysInFilters:       cc.HideDeletedVirtualKeysInFilters,
					RoutingChainMaxDepth:                  cc.RoutingChainMaxDepth,
					Compat: CompatConfig{
						ConvertTextToChat:      cc.CompatConvertTextToChat,
						ConvertChatToResponses: cc.CompatConvertChatToResponses,
						ShouldDropParams:       cc.CompatShouldDropParams,
						ShouldConvertParams:    cc.CompatShouldConvertParams,
					},
				}
				newHash, err := clientConfig.GenerateClientConfigHash()
				if err != nil {
					return fmt.Errorf("failed to generate hash for client config %d: %w", cc.ID, err)
				}
				if err := tx.Model(&cc).Update("config_hash", newHash).Error; err != nil {
					return fmt.Errorf("failed to update hash for client config %d: %w", cc.ID, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running drop_allow_direct_keys_column migration: %s", err.Error())
	}
	return nil
}

// migrationDropAllowDirectKeysColumnDDL drops the now-unused allow_direct_keys
// column in its own migration. Splitting the DDL from the hash-recompute DML
// ensures the AccessExclusiveLock from DROP COLUMN is held only for the brief
// catalog update and never contends with reads/writes on the same table.
func migrationDropAllowDirectKeysColumnDDL(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "drop_allow_direct_keys_column_ddl"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "allow_direct_keys"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if !tx.Migrator().HasColumn(&tables.TableClientConfig{}, "allow_direct_keys") {
				if err := tx.Exec("ALTER TABLE config_client ADD COLUMN allow_direct_keys BOOLEAN DEFAULT FALSE").Error; err != nil {
					return err
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running drop_allow_direct_keys_column_ddl migration: %s", err.Error())
	}
	return nil
}

// migrationAddEnableLiteLLMFallbacksColumn adds the enable_litellm_fallbacks column to the client config table
func migrationAddEnableLiteLLMFallbacksColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_enable_litellm_fallbacks_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			// Use raw SQL since the struct field was removed in a later migration.
			// This column is subsequently dropped by migrationReplaceEnableLiteLLMWithCompatColumns.
			if !tx.Migrator().HasColumn(&tables.TableClientConfig{}, "enable_litellm_fallbacks") {
				if err := tx.Exec("ALTER TABLE config_client ADD COLUMN enable_litellm_fallbacks BOOLEAN DEFAULT FALSE").Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := tx.Exec("ALTER TABLE config_client DROP COLUMN IF EXISTS enable_litellm_fallbacks").Error; err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

// migrationTeamsTableUpdates adds profile, config, and claims columns to the team table
func migrationTeamsTableUpdates(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_profile_config_claims_columns_to_team_table"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableTeam{}, "profile"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TableTeam{}, "config"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TableTeam{}, "claims"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

// migrationAddFrameworkConfigsTable adds the framework_configs table
func migrationAddFrameworkConfigsTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_framework_configs_table"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasTable(&tables.TableFrameworkConfig{}) {
				logger.Info("[configstore] %s: creating table TableFrameworkConfig", migrationName)
				if err := migrator.CreateTable(&tables.TableFrameworkConfig{}); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

// migrationAddTeamSourceIDColumn adds optional source_id to governance_teams, with a unique index
func migrationAddTeamSourceIDColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_team_source_id_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	const idxName = "idx_governance_teams_source_id"
	return RunSingleMigration(ctx, nil, db, logger, &migrator.Migration{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			if err := addColumnIfNotExists(tx, logger, &tables.TableTeam{}, "source_id"); err != nil {
				return fmt.Errorf("add source_id column to governance_teams: %w", err)
			}
			if !mg.HasIndex(&tables.TableTeam{}, idxName) {
				logger.Info("[configstore] %s: creating index SourceID on TableTeam", migrationName)
				if err := mg.CreateIndex(&tables.TableTeam{}, "SourceID"); err != nil {
					return fmt.Errorf("create unique index on governance_teams.source_id: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			if mg.HasIndex(&tables.TableTeam{}, idxName) {
				logger.Info("[configstore] %s: dropping index %s on TableTeam", migrationName, idxName)
				if err := mg.DropIndex(&tables.TableTeam{}, idxName); err != nil {
					return fmt.Errorf("drop unique index on governance_teams.source_id: %w", err)
				}
			}
			if err := dropColumnIfExists(tx, logger, &tables.TableTeam{}, "source_id"); err != nil {
				return fmt.Errorf("drop source_id column from governance_teams: %w", err)
			}
			return nil
		},
	})
}

// migrationAddKeyNameColumn adds the name column to the key table and populates unique names
func migrationAddKeyNameColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_key_name_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&tables.TableKey{}, "name") {
				// Step 1: Add the column as nullable first
				if err := tx.Exec("ALTER TABLE config_keys ADD COLUMN name VARCHAR(255)").Error; err != nil {
					return fmt.Errorf("failed to add name column: %w", err)
				}

				// Step 2: Populate unique names for all existing keys
				var keys []tables.TableKey
				if err := tx.Find(&keys).Error; err != nil {
					return fmt.Errorf("failed to fetch keys: %w", err)
				}

				logger.Info("[configstore] %s: processing %d keys", migrationName, len(keys))
				for _, key := range keys {
					// Create unique name: provider_name-key-{first8chars_of_key_id}-{key_index}
					keyIDShort := key.KeyID
					if len(keyIDShort) > 8 {
						keyIDShort = keyIDShort[:8]
					}
					keyName := keyIDShort + "-" + strconv.Itoa(int(key.ID))
					uniqueName := fmt.Sprintf("%s-key-%s", key.Provider, keyName)

					// Update the key with the unique name
					if err := tx.Model(&key).Update("name", uniqueName).Error; err != nil {
						return fmt.Errorf("failed to update key %s with name %s: %w", key.KeyID, uniqueName, err)
					}
				}

				// Step 3: Add unique index (SQLite compatible)
				if err := tx.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_key_name ON config_keys (name)").Error; err != nil {
					return fmt.Errorf("failed to create unique index on name: %w", err)
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			// Drop the unique index first to avoid orphaned index artifacts
			if err := tx.Exec("DROP INDEX IF EXISTS idx_key_name").Error; err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, "name"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

// migrationCleanupMCPClientToolsConfig removes ToolsToSkipJSON column and converts empty ToolsToExecuteJSON to wildcard
func migrationCleanupMCPClientToolsConfig(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "cleanup_mcp_client_tools_config"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			// Step 1: Remove ToolsToSkipJSON column if it exists (cleanup from old versions)
			if err := dropColumnIfExists(tx, logger, &tables.TableMCPClient{}, "tools_to_skip_json"); err != nil {
				return fmt.Errorf("failed to drop tools_to_skip_json column: %w", err)
			}

			// Alternative column name variations that might exist
			if err := dropColumnIfExists(tx, logger, &tables.TableMCPClient{}, "ToolsToSkipJSON"); err != nil {
				return fmt.Errorf("failed to drop ToolsToSkipJSON column: %w", err)
			}

			// Step 2: Update empty ToolsToExecuteJSON arrays to wildcard ["*"]
			// Convert "[]" (empty array) to "[\"*\"]" (wildcard array) for backward compatibility
			updateSQL := `
				UPDATE config_mcp_clients
				SET tools_to_execute_json = '["*"]'
				WHERE tools_to_execute_json = '[]' OR tools_to_execute_json = '' OR tools_to_execute_json IS NULL
			`
			if err := tx.Exec(updateSQL).Error; err != nil {
				return fmt.Errorf("failed to update empty ToolsToExecuteJSON to wildcard: %w", err)
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			// For rollback, we could add the column back, but since we're moving away from this
			// functionality, we'll just revert the wildcard changes back to empty arrays
			tx = tx.WithContext(ctx)

			revertSQL := `
				UPDATE config_mcp_clients
				SET tools_to_execute_json = '[]'
				WHERE tools_to_execute_json = '["*"]'
			`
			if err := tx.Exec(revertSQL).Error; err != nil {
				return fmt.Errorf("failed to revert wildcard ToolsToExecuteJSON to empty arrays: %w", err)
			}

			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running MCP client tools cleanup migration: %s", err.Error())
	}
	return nil
}

// migrationAddVirtualKeyMCPConfigsTable adds the virtual_key_mcp_configs table
func migrationAddVirtualKeyMCPConfigsTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_vk_mcp_configs_table"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasTable(&tables.TableVirtualKeyMCPConfig{}) {
				logger.Info("[configstore] %s: creating table TableVirtualKeyMCPConfig", migrationName)
				if err := migrator.CreateTable(&tables.TableVirtualKeyMCPConfig{}); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			logger.Info("[configstore] %s: dropping table TableVirtualKeyMCPConfig", migrationName)
			if err := migrator.DropTable(&tables.TableVirtualKeyMCPConfig{}); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

// migrationAddProviderConfigBudgetRateLimit adds budget_id and rate_limit_id columns with proper foreign key constraints
func migrationAddProviderConfigBudgetRateLimit(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_provider_config_budget_rate_limit"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			// Add budget_id and rate_limit_id columns if they don't exist
			// Note: budget_id is added via raw SQL because the field was later removed from the struct
			// (migrated to governance_budgets.provider_config_id in add_multi_budget_tables)
			if migrator.HasTable(&tables.TableVirtualKeyProviderConfig{}) {
				if err := tx.Exec("ALTER TABLE governance_virtual_key_provider_configs ADD COLUMN IF NOT EXISTS budget_id VARCHAR(255)").Error; err != nil {
					// Ignore error for databases that don't support IF NOT EXISTS (e.g., SQLite)
					// The column may already exist from a previous run
				}

				// Add RateLimitID column if it doesn't exist
				if err := addColumnIfNotExists(tx, logger, &tables.TableVirtualKeyProviderConfig{}, "rate_limit_id"); err != nil {
					return fmt.Errorf("failed to add rate_limit_id column: %w", err)
				}

				// Create foreign key indexes for better performance
				if err := tx.Exec("CREATE INDEX IF NOT EXISTS idx_provider_config_budget ON governance_virtual_key_provider_configs (budget_id)").Error; err != nil {
					// Ignore - index may already exist or column may not exist yet
				}

				if !migrator.HasIndex(&tables.TableVirtualKeyProviderConfig{}, "idx_provider_config_rate_limit") {
					if err := tx.Exec("CREATE INDEX IF NOT EXISTS idx_provider_config_rate_limit ON governance_virtual_key_provider_configs (rate_limit_id)").Error; err != nil {
						return fmt.Errorf("failed to create rate_limit_id index: %w", err)
					}
				}

				// Create FK constraint for RateLimit (Budget FK is no longer needed - budgets use direct FK on budget table)
				if !migrator.HasConstraint(&tables.TableVirtualKeyProviderConfig{}, "RateLimit") {
					if err := migrator.CreateConstraint(&tables.TableVirtualKeyProviderConfig{}, "RateLimit"); err != nil {
						return fmt.Errorf("failed to create RateLimit FK constraint: %w", err)
					}
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			// Drop indexes
			_ = tx.Exec("DROP INDEX IF EXISTS idx_provider_config_budget")
			_ = tx.Exec("DROP INDEX IF EXISTS idx_provider_config_rate_limit")

			// Drop FK constraints
			if migrator.HasConstraint(&tables.TableVirtualKeyProviderConfig{}, "RateLimit") {
				if err := migrator.DropConstraint(&tables.TableVirtualKeyProviderConfig{}, "RateLimit"); err != nil {
					return fmt.Errorf("failed to drop RateLimit FK constraint: %w", err)
				}
			}

			// Drop columns via raw SQL (budget_id no longer on struct)
			_ = tx.Exec("ALTER TABLE governance_virtual_key_provider_configs DROP COLUMN IF EXISTS budget_id")
			if err := dropColumnIfExists(tx, logger, &tables.TableVirtualKeyProviderConfig{}, "rate_limit_id"); err != nil {
				return fmt.Errorf("failed to drop rate_limit_id column: %w", err)
			}

			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running provider config budget/rate limit migration: %s", err.Error())
	}
	return nil
}

// migrationAddPluginPathColumn adds the path column to the plugin table
func migrationAddPluginPathColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "update_plugins_table_for_custom_plugins"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TablePlugin{}, "path"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TablePlugin{}, "is_custom"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TablePlugin{}, "path"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &tables.TablePlugin{}, "is_custom"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running plugin path migration: %s", err.Error())
	}
	return nil
}

// migrationAddSessionsTable adds the sessions table
func migrationAddSessionsTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_sessions_table"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasTable(&tables.SessionsTable{}) {
				logger.Info("[configstore] %s: creating table SessionsTable", migrationName)
				if err := migrator.CreateTable(&tables.SessionsTable{}); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			logger.Info("[configstore] %s: dropping table SessionsTable", migrationName)
			if err := migrator.DropTable(&tables.SessionsTable{}); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

// migrationAddHeadersJSONColumnIntoMCPClient adds the headers_json column to the mcp_client table
func migrationAddHeadersJSONColumnIntoMCPClient(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_headers_json_column_into_mcp_client"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableMCPClient{}, "headers_json"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableMCPClient{}, "headers_json"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

// migrationAddDisableContentLoggingColumn adds the disable_content_logging column to the client config table
func migrationAddDisableContentLoggingColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_disable_content_logging_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "disable_content_logging"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "disable_content_logging"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

// migrationAddMCPClientIDColumn adds the client_id column to the mcp_clients table and populates unique client IDs
func migrationAddMCPClientIDColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_mcp_client_id_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			if !migrator.HasColumn(&tables.TableMCPClient{}, "client_id") {
				// Add the column as nullable first
				if err := tx.Exec("ALTER TABLE config_mcp_clients ADD COLUMN client_id VARCHAR(255)").Error; err != nil {
					return fmt.Errorf("failed to add client_id column: %w", err)
				}

				// Populate unique client_ids (UUIDs) for all existing MCP clients
				var mcpClients []tables.TableMCPClient
				if err := tx.Find(&mcpClients).Error; err != nil {
					return fmt.Errorf("failed to fetch MCP clients: %w", err)
				}

				logger.Info("[configstore] %s: processing %d mcpClients", migrationName, len(mcpClients))
				for _, client := range mcpClients {
					// Generate a UUID for the client_id
					clientID := uuid.New().String()

					// Update the client with the generated client_id
					if err := tx.Model(&client).Update("client_id", clientID).Error; err != nil {
						return fmt.Errorf("failed to update MCP client %d with client_id %s: %w", client.ID, clientID, err)
					}
				}

				// Create unique index on client_id
				if err := tx.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_mcp_client_id ON config_mcp_clients (client_id)").Error; err != nil {
					return fmt.Errorf("failed to create unique index on client_id: %w", err)
				}
				// Enforce NOT NULL in Postgres to guarantee ID presence on new rows
				if tx.Dialector.Name() == "postgres" {
					if err := tx.Exec("ALTER TABLE config_mcp_clients ALTER COLUMN client_id SET NOT NULL").Error; err != nil {
						return fmt.Errorf("failed to set client_id NOT NULL: %w", err)
					}
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			// Drop the unique index first to avoid orphaned index artifacts
			if err := tx.Exec("DROP INDEX IF EXISTS idx_mcp_client_id").Error; err != nil {
				return fmt.Errorf("failed to drop client_id index: %w", err)
			}

			if err := dropColumnIfExists(tx, logger, &tables.TableMCPClient{}, "client_id"); err != nil {
				return fmt.Errorf("failed to drop client_id column: %w", err)
			}

			return nil
		},
	}})

	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running MCP client_id migration: %s", err.Error())
	}
	return nil
}

// migrationAddVertexProjectNumberColumn adds the vertex_project_number column to the key table
func migrationAddVertexProjectNumberColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_vertex_project_number_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, "vertex_project_number"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, "vertex_project_number"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running vertex project number migration: %s", err.Error())
	}
	return nil
}

// migrationAddVertexDeploymentsJSONColumn adds the vertex_deployments_json column to the key table.
// This column is later dropped by migrationDropDeploymentColumnsAndAddAliases after data is migrated.
func migrationAddVertexDeploymentsJSONColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_vertex_deployments_json_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if !tx.Migrator().HasColumn(&tables.TableKey{}, "vertex_deployments_json") {
				if err := tx.Exec("ALTER TABLE config_keys ADD COLUMN vertex_deployments_json TEXT").Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if tx.Migrator().HasColumn(&tables.TableKey{}, "vertex_deployments_json") {
				if err := tx.Exec("ALTER TABLE config_keys DROP COLUMN vertex_deployments_json").Error; err != nil {
					return err
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running vertex deployments JSON migration: %s", err.Error())
	}
	return nil
}

func migrationMissingProviderColumnInKeyTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_and_fill_provider_column_in_key_table"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	options := &migrator.Options{
		TableName:                 migrator.DefaultOptions.TableName,
		IDColumnName:              migrator.DefaultOptions.IDColumnName,
		IDColumnSize:              migrator.DefaultOptions.IDColumnSize,
		UseTransaction:            true,
		ValidateUnknownMigrations: migrator.DefaultOptions.ValidateUnknownMigrations,
	}
	m := migrator.New(db, options, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			// Step 1: Add the provider column if it doesn't exist
			if migrator.HasColumn(&tables.TableKey{}, "provider") {
				return nil
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, "provider"); err != nil {
				return fmt.Errorf("failed to add provider column: %w", err)
			}

			// Step 2: Find all keys where provider is empty/null but provider_id is set
			var keys []tables.TableKey
			if err := tx.Where("provider IS NULL OR provider = ''").Find(&keys).Error; err != nil {
				return fmt.Errorf("failed to fetch keys with missing provider: %w", err)
			}

			// Step 3: Update each key with the provider name from the provider table
			logger.Info("[configstore] %s: processing %d keys", migrationName, len(keys))
			for _, key := range keys {
				var provider tables.TableProvider
				if err := tx.First(&provider, key.ProviderID).Error; err != nil {
					// Skip keys with invalid provider_id
					if err == gorm.ErrRecordNotFound {
						continue
					}
					return fmt.Errorf("failed to fetch provider %d for key %s: %w", key.ProviderID, key.KeyID, err)
				}

				// Update the key with the provider name
				if err := tx.Model(&key).Update("provider", provider.Name).Error; err != nil {
					return fmt.Errorf("failed to update key %s with provider %s: %w", key.KeyID, provider.Name, err)
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, "provider"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running add and fill provider column migration: %s", err.Error())
	}
	return nil
}

// migrationAddToolsToAutoExecuteJSONColumn adds the tools_to_auto_execute_json column to the mcp_client table
func migrationAddToolsToAutoExecuteJSONColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_tools_to_auto_execute_json_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&tables.TableMCPClient{}, "tools_to_auto_execute_json") {
				if err := addColumnIfNotExists(tx, logger, &tables.TableMCPClient{}, "tools_to_auto_execute_json"); err != nil {
					return err
				}
				// Initialize existing rows with empty array
				if err := tx.Exec("UPDATE config_mcp_clients SET tools_to_auto_execute_json = '[]' WHERE tools_to_auto_execute_json IS NULL OR tools_to_auto_execute_json = ''").Error; err != nil {
					return fmt.Errorf("failed to initialize tools_to_auto_execute_json: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableMCPClient{}, "tools_to_auto_execute_json"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

// migrationAddIsCodeModeClientColumn adds the is_code_mode_client column to the config_mcp_clients table
func migrationAddIsCodeModeClientColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_is_code_mode_client_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&tables.TableMCPClient{}, "is_code_mode_client") {
				if err := addColumnIfNotExists(tx, logger, &tables.TableMCPClient{}, "is_code_mode_client"); err != nil {
					return err
				}
				// Initialize existing rows with false (default value)
				if err := tx.Exec("UPDATE config_mcp_clients SET is_code_mode_client = false WHERE is_code_mode_client IS NULL").Error; err != nil {
					return fmt.Errorf("failed to initialize is_code_mode_client: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableMCPClient{}, "is_code_mode_client"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

// migrationAddLogRetentionDaysColumn adds the log_retention_days column to the client config table
func migrationAddLogRetentionDaysColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_log_retention_days_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "log_retention_days"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "log_retention_days"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

// migrationAddEnabledColumnToKeyTable adds the enabled column to the config_keys table
func migrationAddEnabledColumnToKeyTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_enabled_column_to_key_table"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, "enabled"); err != nil {
				return fmt.Errorf("failed to add enabled column: %w", err)
			}
			// Set default = true for existing rows
			if err := tx.Exec("UPDATE config_keys SET enabled = TRUE WHERE enabled IS NULL").Error; err != nil {
				return fmt.Errorf("failed to backfill enabled column: %w", err)
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, "enabled"); err != nil {
				return fmt.Errorf("failed to drop enabled column: %w", err)
			}

			return nil
		},
	}})

	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running enabled column migration: %s", err.Error())
	}
	return nil
}

// migrationAddBatchAndCachePricingColumns adds the cache_read_input_token_cost, cache_creation_input_token_cost, input_cost_per_token_batches, and output_cost_per_token_batches columns to the model_pricing table
func migrationAddBatchAndCachePricingColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "update_model_pricing_table_to_add_cache_and_batch_pricing"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableModelPricing{}, "cache_read_input_token_cost"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TableModelPricing{}, "cache_creation_input_token_cost"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TableModelPricing{}, "input_cost_per_token_batches"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TableModelPricing{}, "output_cost_per_token_batches"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableModelPricing{}, "cache_read_input_token_cost"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &tables.TableModelPricing{}, "cache_creation_input_token_cost"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &tables.TableModelPricing{}, "input_cost_per_token_batches"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &tables.TableModelPricing{}, "output_cost_per_token_batches"); err != nil {
				return err
			}
			return nil
		},
	}})
	return m.Migrate()
}

func migrationAddMCPAgentDepthAndMCPToolExecutionTimeoutColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_mcp_agent_depth_and_mcp_tool_execution_timeout_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "mcp_agent_depth"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "mcp_tool_execution_timeout"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "mcp_agent_depth"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "mcp_tool_execution_timeout"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

// migrationAddMCPCodeModeBindingLevelColumn adds the mcp_code_mode_binding_level column to the client config table.
// This column stores the code mode binding level preference (server or tool).
func migrationAddMCPCodeModeBindingLevelColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_mcp_code_mode_binding_level_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "mcp_code_mode_binding_level"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "mcp_code_mode_binding_level"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

// normalizeMCPClientName normalizes an MCP client name by:
// 1. Replacing hyphens and spaces with underscores
// 2. Removing leading digits
// 3. Using a default name if the result is empty
func normalizeMCPClientName(name string) string {
	// Replace hyphens and spaces with underscores
	normalized := strings.ReplaceAll(name, "-", "_")
	normalized = strings.ReplaceAll(normalized, " ", "_")

	// Remove leading digits
	normalized = strings.TrimLeftFunc(normalized, func(r rune) bool {
		return unicode.IsDigit(r)
	})

	// If name becomes empty after normalization, use a default name
	if normalized == "" {
		normalized = "mcp_client"
	}

	return normalized
}

// migrationNormalizeMCPClientNames normalizes MCP client names by:
// 1. Replacing hyphens and spaces with underscores
// 2. Removing leading digits
// 3. Adding number suffix if name already exists
func migrationNormalizeMCPClientNames(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "normalize_mcp_client_names"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			// Fetch all MCP clients
			var mcpClients []tables.TableMCPClient
			if err := tx.Find(&mcpClients).Error; err != nil {
				return fmt.Errorf("failed to fetch MCP clients: %w", err)
			}

			// Track assigned names in memory to avoid transaction visibility issues
			// and ensure we see all updates made during this migration
			assignedNames := make(map[string]bool)

			// Helper function to find a unique name
			findUniqueName := func(baseName string, originalName string, excludeID uint, tx *gorm.DB, assignedNames map[string]bool) (string, error) {
				// First check if base name is already assigned in this migration
				if !assignedNames[baseName] {
					// Also check database for existing names (excluding current client)
					var existing tables.TableMCPClient
					err := tx.Where("name = ? AND id != ?", baseName, excludeID).First(&existing).Error
					if err == gorm.ErrRecordNotFound {
						// Name is available
						assignedNames[baseName] = true
						// Log normalization even when no collision
						if originalName != baseName {
							logger.Info("MCP Client Name Normalized: '%s' -> '%s'", originalName, baseName)
						}
						return baseName, nil
					} else if err != nil {
						return "", fmt.Errorf("failed to check name availability: %w", err)
					}
				}

				// Name exists (either assigned in this migration or in database), try with number suffix starting from 2
				// (base name is conceptually "1", so collisions start from "2")
				suffix := 2
				const maxSuffix = 1000 // Safety limit to prevent infinite loops
				for {
					if suffix > maxSuffix {
						return "", fmt.Errorf("could not find unique name after %d attempts for base name: %s", maxSuffix, baseName)
					}
					candidateName := baseName + strconv.Itoa(suffix)

					// Check both in-memory map and database
					if !assignedNames[candidateName] {
						var existing tables.TableMCPClient
						err := tx.Where("name = ? AND id != ?", candidateName, excludeID).First(&existing).Error
						if err == gorm.ErrRecordNotFound {
							// Found available name - log the transformation
							assignedNames[candidateName] = true
							logger.Info("MCP Client Name Normalized: '%s' -> '%s'", originalName, candidateName)
							return candidateName, nil
						} else if err != nil {
							return "", fmt.Errorf("failed to check name availability: %w", err)
						}
					}
					suffix++
				}
			}

			// Process each client
			logger.Info("[configstore] %s: processing %d mcpClients", migrationName, len(mcpClients))
			for _, client := range mcpClients {
				originalName := client.Name
				needsUpdate := false

				// Check if name needs normalization
				if strings.Contains(originalName, "-") || strings.Contains(originalName, " ") {
					needsUpdate = true
				} else if len(originalName) > 0 && unicode.IsDigit(rune(originalName[0])) {
					needsUpdate = true
				}

				if needsUpdate {
					// Normalize the name
					normalizedName := normalizeMCPClientName(originalName)

					// Find a unique name (pass assignedNames map to track names in this migration)
					uniqueName, err := findUniqueName(normalizedName, originalName, client.ID, tx, assignedNames)
					if err != nil {
						return fmt.Errorf("failed to find unique name for client %d (original: %s): %w", client.ID, originalName, err)
					}

					// Update the client name
					if err := tx.Model(&client).Update("name", uniqueName).Error; err != nil {
						return fmt.Errorf("failed to update MCP client %d name from %s to %s: %w", client.ID, originalName, uniqueName, err)
					}
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			// Rollback is not possible as we don't store the original names
			// This migration is one-way
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running MCP client name normalization migration: %s", err.Error())
	}
	return nil
}

// migrationMoveKeysToProviderConfig migrates keys from virtual key level to provider config level
func migrationMoveKeysToProviderConfig(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "move_keys_to_provider_config"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			gormMigrator := tx.Migrator()

			// Step 1: Create the new join table for provider config -> keys relationship
			// Setup the join table so GORM knows about the custom structure
			if err := tx.SetupJoinTable(&tables.TableVirtualKeyProviderConfig{}, "Keys", &tables.TableVirtualKeyProviderConfigKey{}); err != nil {
				return fmt.Errorf("failed to setup join table for provider config keys: %w", err)
			}

			// Create the join table if it doesn't exist
			if !gormMigrator.HasTable(&tables.TableVirtualKeyProviderConfigKey{}) {
				logger.Info("[configstore] %s: creating table TableVirtualKeyProviderConfigKey", migrationName)
				if err := gormMigrator.CreateTable(&tables.TableVirtualKeyProviderConfigKey{}); err != nil {
					return fmt.Errorf("failed to create join table for provider config keys: %w", err)
				}
			}

			// Step 2: Migrate existing key associations from virtual key to provider config level
			// Check if old join table exists
			hasOldTable := gormMigrator.HasTable("governance_virtual_key_keys")

			if hasOldTable {
				// Get all existing associations from old table using GORM's Table method
				type OldAssociation struct {
					VirtualKeyID string `gorm:"column:table_virtual_key_id"`
					KeyID        uint   `gorm:"column:table_key_id"`
				}
				var oldAssociations []OldAssociation
				if err := tx.Table("governance_virtual_key_keys").Find(&oldAssociations).Error; err == nil {
					// Process each association
					logger.Info("[configstore] %s: processing %d oldAssociations", migrationName, len(oldAssociations))
					for _, assoc := range oldAssociations {
						// Get only the key ID and provider - using a minimal struct to avoid
						// querying columns that may not exist yet (added by later migrations)
						type KeyMinimal struct {
							ID       uint
							Provider string
						}
						var keyData KeyMinimal
						if err := tx.Table("config_keys").Select("id, provider").Where("id = ?", assoc.KeyID).First(&keyData).Error; err != nil {
							// Key might have been deleted, skip
							continue
						}

						// Find existing provider config for this virtual key and provider
						var providerConfig tables.TableVirtualKeyProviderConfig
						result := tx.Where("virtual_key_id = ? AND provider = ?", assoc.VirtualKeyID, keyData.Provider).First(&providerConfig)

						if result.Error != nil {
							if result.Error == gorm.ErrRecordNotFound {
								// Create a new provider config for this provider
								providerConfig = tables.TableVirtualKeyProviderConfig{
									VirtualKeyID:  assoc.VirtualKeyID,
									Provider:      keyData.Provider,
									Weight:        bifrost.Ptr(1.0),
									AllowedModels: []string{},
								}
								if err := tx.Create(&providerConfig).Error; err != nil {
									return fmt.Errorf("failed to create provider config for migration: %w", err)
								}
							} else {
								return fmt.Errorf("failed to query provider config: %w", result.Error)
							}
						}

						// Insert directly into the join table using clause.OnConflict for
						// database-agnostic duplicate handling (works for SQLite and PostgreSQL)
						joinEntry := tables.TableVirtualKeyProviderConfigKey{
							TableVirtualKeyProviderConfigID: providerConfig.ID,
							TableKeyID:                      keyData.ID,
						}
						if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&joinEntry).Error; err != nil {
							return fmt.Errorf("failed to associate key %d with provider config %d: %w", keyData.ID, providerConfig.ID, err)
						}
					}
				}

				// Step 3: Drop the old join table
				logger.Info("[configstore] %s: dropping table governance_virtual_key_keys", migrationName)
				if err := gormMigrator.DropTable("governance_virtual_key_keys"); err != nil {
					return fmt.Errorf("failed to drop old governance_virtual_key_keys table: %w", err)
				}
			}

			// Note: Empty keys in provider config means all keys are allowed at runtime
			// We don't pre-populate keys here - this is handled at runtime

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			gormMigrator := tx.Migrator()

			// Recreate the old join table structure
			type OldJoinTable struct {
				VirtualKeyID string `gorm:"column:table_virtual_key_id;primaryKey"`
				KeyID        uint   `gorm:"column:table_key_id;primaryKey"`
			}
			logger.Info("[configstore] %s: creating table OldJoinTable", migrationName)
			if err := gormMigrator.CreateTable(&OldJoinTable{}); err != nil {
				// Table might already exist, ignore error
				_ = err
			}
			// Rename to correct table name if needed
			if gormMigrator.HasTable(&OldJoinTable{}) && !gormMigrator.HasTable("governance_virtual_key_keys") {
				if err := gormMigrator.RenameTable(&OldJoinTable{}, "governance_virtual_key_keys"); err != nil {
					return fmt.Errorf("failed to rename old join table: %w", err)
				}
			}

			// Note: We cannot fully rollback the data migration as it would require
			// reconstructing which keys belonged to which virtual keys

			// Drop the new join table
			logger.Info("[configstore] %s: dropping table governance_virtual_key_provider_config_keys", migrationName)
			if err := gormMigrator.DropTable("governance_virtual_key_provider_config_keys"); err != nil {
				return fmt.Errorf("failed to drop governance_virtual_key_provider_config_keys table: %w", err)
			}

			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running move keys to provider config migration: %s", err.Error())
	}
	return nil
}

// migrationAddPluginVersionColumn adds the version column to the plugin table
func migrationAddPluginVersionColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_plugin_version_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TablePlugin{}, "version"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TablePlugin{}, "version"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running add plugin version column migration: %s", err.Error())
	}
	return nil
}

func migrationAddSendBackRawRequestColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_send_back_raw_request_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableProvider{}, "send_back_raw_request"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableProvider{}, "send_back_raw_request"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running add send back raw request columns migration: %s", err.Error())
	}
	return nil
}

// migrationAddConfigHashColumn adds the config_hash column to the provider and key tables
func migrationAddConfigHashColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_config_hash_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			// Add config_hash to providers table
			if !migrator.HasColumn(&tables.TableProvider{}, "config_hash") {
				if err := addColumnIfNotExists(tx, logger, &tables.TableProvider{}, "config_hash"); err != nil {
					return err
				}
				// Pre-populate hashes for existing providers
				var providers []tables.TableProvider
				if err := tx.Find(&providers).Error; err != nil {
					return fmt.Errorf("failed to fetch providers for hash migration: %w", err)
				}
				logger.Info("[configstore] %s: processing %d providers", migrationName, len(providers))
				for _, provider := range providers {
					if provider.ConfigHash == "" {
						// Convert to ProviderConfig and generate hash
						providerConfig := ProviderConfig{
							NetworkConfig:            provider.NetworkConfig,
							ConcurrencyAndBufferSize: provider.ConcurrencyAndBufferSize,
							ProxyConfig:              provider.ProxyConfig,
							SendBackRawRequest:       provider.SendBackRawRequest,
							SendBackRawResponse:      provider.SendBackRawResponse,
							CustomProviderConfig:     provider.CustomProviderConfig,
						}
						hash, err := providerConfig.GenerateConfigHash(provider.Name)
						if err != nil {
							return fmt.Errorf("failed to generate hash for provider %s: %w", provider.Name, err)
						}
						if err := tx.Model(&provider).Update("config_hash", hash).Error; err != nil {
							return fmt.Errorf("failed to update hash for provider %s: %w", provider.Name, err)
						}
					}
				}
			}
			// Add config_hash to keys table
			if !migrator.HasColumn(&tables.TableKey{}, "config_hash") {
				if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, "config_hash"); err != nil {
					return err
				}
				// Pre-populate hashes for existing keys
				var keys []tables.TableKey
				if err := tx.Find(&keys).Error; err != nil {
					return fmt.Errorf("failed to fetch keys for hash migration: %w", err)
				}
				logger.Info("[configstore] %s: processing %d keys", migrationName, len(keys))
				for _, key := range keys {
					if key.ConfigHash == "" {
						// Convert to schemas.Key and generate hash
						schemaKey := schemas.Key{
							Name:             key.Name,
							Value:            key.Value,
							Models:           key.Models,
							Weight:           getWeight(key.Weight),
							AzureKeyConfig:   key.AzureKeyConfig,
							VertexKeyConfig:  key.VertexKeyConfig,
							BedrockKeyConfig: key.BedrockKeyConfig,
							Aliases:          key.Aliases,
						}
						hash, err := GenerateKeyHash(schemaKey)
						if err != nil {
							return fmt.Errorf("failed to generate hash for key %s: %w", key.Name, err)
						}
						if err := tx.Model(&key).Update("config_hash", hash).Error; err != nil {
							return fmt.Errorf("failed to update hash for key %s: %w", key.Name, err)
						}
					}
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableProvider{}, "config_hash"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, "config_hash"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running add config hash column migration: %s", err.Error())
	}
	return nil
}

// migrationAddVirtualKeyConfigHashColumn adds the config_hash column to the virtual keys table
func migrationAddVirtualKeyConfigHashColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_virtual_key_config_hash_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			// Add config_hash to virtual keys table
			if !migrator.HasColumn(&tables.TableVirtualKey{}, "config_hash") {
				if err := addColumnIfNotExists(tx, logger, &tables.TableVirtualKey{}, "config_hash"); err != nil {
					return err
				}
				// Pre-populate hashes for existing virtual keys
				var virtualKeys []tables.TableVirtualKey
				if err := tx.Preload("ProviderConfigs").Preload("ProviderConfigs.Keys").Preload("MCPConfigs").Find(&virtualKeys).Error; err != nil {
					return fmt.Errorf("failed to fetch virtual keys for hash migration: %w", err)
				}
				logger.Info("[configstore] %s: processing %d virtualKeys", migrationName, len(virtualKeys))
				for _, vk := range virtualKeys {
					if vk.ConfigHash == "" {
						hash, err := GenerateVirtualKeyHash(vk)
						if err != nil {
							return fmt.Errorf("failed to generate hash for virtual key %s: %w", vk.ID, err)
						}
						if err := tx.Model(&vk).Update("config_hash", hash).Error; err != nil {
							return fmt.Errorf("failed to update hash for virtual key %s: %w", vk.ID, err)
						}
					}
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableVirtualKey{}, "config_hash"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running add virtual key config hash column migration: %s", err.Error())
	}
	return nil
}

// migrationAddAdditionalConfigHashColumns adds config_hash columns to client config, budget, rate limit,
// customer, team, MCP client, and plugin tables for reconciliation support
func migrationAddAdditionalConfigHashColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_additional_config_hash_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			// Add config_hash to client config table
			if !migrator.HasColumn(&tables.TableClientConfig{}, "config_hash") {
				if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "config_hash"); err != nil {
					return err
				}
				// Pre-populate hashes for existing client configs
				var clientConfigs []tables.TableClientConfig
				if err := tx.Find(&clientConfigs).Error; err != nil {
					return fmt.Errorf("failed to fetch client configs for hash migration: %w", err)
				}
				logger.Info("[configstore] %s: processing %d clientConfigs", migrationName, len(clientConfigs))
				for _, cc := range clientConfigs {
					if cc.ConfigHash == "" {
						clientConfig := ClientConfig{
							DropExcessRequests:      cc.DropExcessRequests,
							InitialPoolSize:         cc.InitialPoolSize,
							PrometheusLabels:        cc.PrometheusLabels,
							EnableLogging:           cc.EnableLogging,
							DisableContentLogging:   cc.DisableContentLogging,
							LogRetentionDays:        cc.LogRetentionDays,
							EnforceGovernanceHeader: cc.EnforceGovernanceHeader,
							AllowedOrigins:          cc.AllowedOrigins,
							MaxRequestBodySizeMB:    cc.MaxRequestBodySizeMB,
						}
						hash, err := clientConfig.GenerateClientConfigHash()
						if err != nil {
							return fmt.Errorf("failed to generate hash for client config %d: %w", cc.ID, err)
						}
						if err := tx.Model(&cc).Update("config_hash", hash).Error; err != nil {
							return fmt.Errorf("failed to update hash for client config %d: %w", cc.ID, err)
						}
					}
				}
			}

			// Add config_hash to budgets table
			if !migrator.HasColumn(&tables.TableBudget{}, "config_hash") {
				if err := addColumnIfNotExists(tx, logger, &tables.TableBudget{}, "config_hash"); err != nil {
					return err
				}
				// Pre-populate hashes for existing budgets
				var budgets []tables.TableBudget
				if err := tx.Find(&budgets).Error; err != nil {
					return fmt.Errorf("failed to fetch budgets for hash migration: %w", err)
				}
				logger.Info("[configstore] %s: processing %d budgets", migrationName, len(budgets))
				for _, budget := range budgets {
					if budget.ConfigHash == "" {
						hash, err := GenerateBudgetHash(budget)
						if err != nil {
							return fmt.Errorf("failed to generate hash for budget %s: %w", budget.ID, err)
						}
						if err := tx.Model(&budget).Update("config_hash", hash).Error; err != nil {
							return fmt.Errorf("failed to update hash for budget %s: %w", budget.ID, err)
						}
					}
				}
			}

			// Add config_hash to rate limits table
			if !migrator.HasColumn(&tables.TableRateLimit{}, "config_hash") {
				if err := addColumnIfNotExists(tx, logger, &tables.TableRateLimit{}, "config_hash"); err != nil {
					return err
				}
				// Pre-populate hashes for existing rate limits
				var rateLimits []tables.TableRateLimit
				if err := tx.Find(&rateLimits).Error; err != nil {
					return fmt.Errorf("failed to fetch rate limits for hash migration: %w", err)
				}
				logger.Info("[configstore] %s: processing %d rateLimits", migrationName, len(rateLimits))
				for _, rl := range rateLimits {
					if rl.ConfigHash == "" {
						hash, err := GenerateRateLimitHash(rl)
						if err != nil {
							return fmt.Errorf("failed to generate hash for rate limit %s: %w", rl.ID, err)
						}
						if err := tx.Model(&rl).Update("config_hash", hash).Error; err != nil {
							return fmt.Errorf("failed to update hash for rate limit %s: %w", rl.ID, err)
						}
					}
				}
			}

			// Add config_hash to customers table
			if !migrator.HasColumn(&tables.TableCustomer{}, "config_hash") {
				if err := addColumnIfNotExists(tx, logger, &tables.TableCustomer{}, "config_hash"); err != nil {
					return err
				}
				// Pre-populate hashes for existing customers
				var customers []tables.TableCustomer
				if err := tx.Find(&customers).Error; err != nil {
					return fmt.Errorf("failed to fetch customers for hash migration: %w", err)
				}
				logger.Info("[configstore] %s: processing %d customers", migrationName, len(customers))
				for _, customer := range customers {
					if customer.ConfigHash == "" {
						hash, err := GenerateCustomerHash(customer)
						if err != nil {
							return fmt.Errorf("failed to generate hash for customer %s: %w", customer.ID, err)
						}
						if err := tx.Model(&customer).Update("config_hash", hash).Error; err != nil {
							return fmt.Errorf("failed to update hash for customer %s: %w", customer.ID, err)
						}
					}
				}
			}

			// Add config_hash to teams table
			if !migrator.HasColumn(&tables.TableTeam{}, "config_hash") {
				if err := addColumnIfNotExists(tx, logger, &tables.TableTeam{}, "config_hash"); err != nil {
					return err
				}
				// Pre-populate hashes for existing teams
				var teams []tables.TableTeam
				if err := tx.Find(&teams).Error; err != nil {
					return fmt.Errorf("failed to fetch teams for hash migration: %w", err)
				}
				logger.Info("[configstore] %s: processing %d teams", migrationName, len(teams))
				for _, team := range teams {
					if team.ConfigHash == "" {
						hash, err := GenerateTeamHash(team)
						if err != nil {
							return fmt.Errorf("failed to generate hash for team %s: %w", team.ID, err)
						}
						if err := tx.Model(&team).Update("config_hash", hash).Error; err != nil {
							return fmt.Errorf("failed to update hash for team %s: %w", team.ID, err)
						}
					}
				}
			}

			// Add config_hash to MCP clients table
			if !migrator.HasColumn(&tables.TableMCPClient{}, "config_hash") {
				if err := addColumnIfNotExists(tx, logger, &tables.TableMCPClient{}, "config_hash"); err != nil {
					return err
				}
				// Pre-populate hashes for existing MCP clients
				var mcpClients []tables.TableMCPClient
				if err := tx.Find(&mcpClients).Error; err != nil {
					return fmt.Errorf("failed to fetch MCP clients for hash migration: %w", err)
				}
				logger.Info("[configstore] %s: processing %d mcpClients", migrationName, len(mcpClients))
				for _, mcp := range mcpClients {
					if mcp.ConfigHash == "" {
						hash, err := GenerateMCPClientHash(mcp)
						if err != nil {
							return fmt.Errorf("failed to generate hash for MCP client %s: %w", mcp.Name, err)
						}
						if err := tx.Model(&mcp).Update("config_hash", hash).Error; err != nil {
							return fmt.Errorf("failed to update hash for MCP client %s: %w", mcp.Name, err)
						}
					}
				}
			}

			// Add config_hash to plugins table
			if !migrator.HasColumn(&tables.TablePlugin{}, "config_hash") {
				if err := addColumnIfNotExists(tx, logger, &tables.TablePlugin{}, "config_hash"); err != nil {
					return err
				}
				// Pre-populate hashes for existing plugins
				var plugins []tables.TablePlugin
				if err := tx.Find(&plugins).Error; err != nil {
					return fmt.Errorf("failed to fetch plugins for hash migration: %w", err)
				}
				logger.Info("[configstore] %s: processing %d plugins", migrationName, len(plugins))
				for _, plugin := range plugins {
					if plugin.ConfigHash == "" {
						hash, err := GeneratePluginHash(plugin)
						if err != nil {
							return fmt.Errorf("failed to generate hash for plugin %s: %w", plugin.Name, err)
						}
						if err := tx.Model(&plugin).Update("config_hash", hash).Error; err != nil {
							return fmt.Errorf("failed to update hash for plugin %s: %w", plugin.Name, err)
						}
					}
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "config_hash"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &tables.TableBudget{}, "config_hash"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &tables.TableRateLimit{}, "config_hash"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &tables.TableCustomer{}, "config_hash"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &tables.TableTeam{}, "config_hash"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &tables.TableMCPClient{}, "config_hash"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &tables.TablePlugin{}, "config_hash"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running add additional config hash columns migration: %s", err.Error())
	}
	return nil
}

// migrationAdd200kTokenPricingColumns adds pricing columns for 200k token tier models
func migrationAdd200kTokenPricingColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_200k_token_pricing_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			columns := []string{
				"input_cost_per_token_above_200k_tokens",
				"output_cost_per_token_above_200k_tokens",
				"cache_creation_input_token_cost_above_200k_tokens",
				"cache_read_input_token_cost_above_200k_tokens",
			}

			for _, field := range columns {
				if err := addColumnIfNotExists(tx, logger, &tables.TableModelPricing{}, field); err != nil {
					return fmt.Errorf("failed to add column %s: %w", field, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			columns := []string{
				"input_cost_per_token_above_200k_tokens",
				"output_cost_per_token_above_200k_tokens",
				"cache_creation_input_token_cost_above_200k_tokens",
				"cache_read_input_token_cost_above_200k_tokens",
			}

			for _, field := range columns {
				if err := dropColumnIfExists(tx, logger, &tables.TableModelPricing{}, field); err != nil {
					return fmt.Errorf("failed to drop column %s: %w", field, err)
				}
			}
			return nil
		},
	}})
	return m.Migrate()
}

// migrationAddImagePricingColumns adds the image generation pricing columns to the model_pricing table
func migrationAddImagePricingColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_image_pricing_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			columns := []string{
				"input_cost_per_image_token",
				"output_cost_per_image_token",
				"input_cost_per_image",
				"output_cost_per_image",
				"cache_read_input_image_token_cost",
			}

			for _, field := range columns {
				if err := addColumnIfNotExists(tx, logger, &tables.TableModelPricing{}, field); err != nil {
					return fmt.Errorf("failed to add column %s: %w", field, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			columns := []string{
				"input_cost_per_image_token",
				"output_cost_per_image_token",
				"input_cost_per_image",
				"output_cost_per_image",
				"cache_read_input_image_token_cost",
			}

			for _, field := range columns {
				if err := dropColumnIfExists(tx, logger, &tables.TableModelPricing{}, field); err != nil {
					return fmt.Errorf("failed to drop column %s: %w", field, err)
				}
			}
			return nil
		},
	}})
	return m.Migrate()
}

// migrationAddUseForBatchAPIColumnAndS3BucketsConfig adds the use_for_batch_api and bedrock_batch_s3_config_json columns to the config_keys table
// Existing keys are backfilled with use_for_batch_api = TRUE to preserve current behavior
func migrationAddUseForBatchAPIColumnAndS3BucketsConfig(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_use_for_batch_api_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			// Add use_for_batch_api column
			if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, "use_for_batch_api"); err != nil {
				return fmt.Errorf("failed to add use_for_batch_api column: %w", err)
			}

			// Add bedrock_batch_s3_config_json column
			if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, "bedrock_batch_s3_config_json"); err != nil {
				return fmt.Errorf("failed to add bedrock_batch_s3_config_json column: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, "use_for_batch_api"); err != nil {
				return fmt.Errorf("failed to drop use_for_batch_api column: %w", err)
			}

			if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, "bedrock_batch_s3_config_json"); err != nil {
				return fmt.Errorf("failed to drop bedrock_batch_s3_config_json column: %w", err)
			}

			return nil
		},
	}})

	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running use_for_batch_api migration: %s", err.Error())
	}
	return nil
}

// migrationAddUseAnthropicEndpointsColumn adds the use_anthropic_endpoints column to the config_keys table.
func migrationAddUseAnthropicEndpointsColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_use_anthropic_endpoints_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, "use_anthropic_endpoints"); err != nil {
				return fmt.Errorf("failed to add use_anthropic_endpoints column: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, "use_anthropic_endpoints"); err != nil {
				return fmt.Errorf("failed to drop use_anthropic_endpoints column: %w", err)
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_use_anthropic_endpoints_column migration: %s", err.Error())
	}
	return nil
}

// migrationAddHeaderFilterConfigJSONColumn adds the header_filter_config_json column to the config_client table
func migrationAddHeaderFilterConfigJSONColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_header_filter_config_json_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "header_filter_config_json"); err != nil {
				return fmt.Errorf("failed to add header_filter_config_json column: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "header_filter_config_json"); err != nil {
				return fmt.Errorf("failed to drop header_filter_config_json column: %w", err)
			}
			return nil
		},
	}})

	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running header_filter_config_json migration: %s", err.Error())
	}
	return nil
}

// migrationAddAzureClientIDAndClientSecretAndTenantIDColumns adds the azure_client_id, azure_client_secret, and azure_tenant_id columns to the key table
func migrationAddAzureClientIDAndClientSecretAndTenantIDColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_azure_client_id_and_client_secret_and_tenant_id_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, "azure_client_id"); err != nil {
				return fmt.Errorf("failed to add azure_client_id column: %w", err)
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, "azure_client_secret"); err != nil {
				return fmt.Errorf("failed to add azure_client_secret column: %w", err)
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, "azure_tenant_id"); err != nil {
				return fmt.Errorf("failed to add azure_tenant_id column: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, "azure_client_id"); err != nil {
				return fmt.Errorf("failed to drop azure_client_id column: %w", err)
			}
			if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, "azure_client_secret"); err != nil {
				return fmt.Errorf("failed to drop azure_client_secret column: %w", err)
			}
			if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, "azure_tenant_id"); err != nil {
				return fmt.Errorf("failed to drop azure_tenant_id column: %w", err)
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running azure_client_id_and_client_secret_and_tenant_id migration: %s", err.Error())
	}
	return nil
}

func migrationAddToolPricingJSONColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_tool_pricing_json_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableMCPClient{}, "tool_pricing_json"); err != nil {
				return fmt.Errorf("failed to add tool_pricing_json column: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableMCPClient{}, "tool_pricing_json"); err != nil {
				return fmt.Errorf("failed to drop tool_pricing_json column: %w", err)
			}
			return nil
		},
	}})
	return m.Migrate()
}

// migrationRemoveServerPrefixFromMCPTools removes the server name prefix from tool names
// in tools_to_execute_json, tools_to_auto_execute_json, and tool_pricing_json columns
// in both config_mcp_clients and governance_virtual_key_mcp_configs tables.
//
// This migration converts:
//   - tools_to_execute_json: ["calculator_add", "calculator_subtract"] → ["add", "subtract"]
//   - tools_to_auto_execute_json: ["calculator_multiply"] → ["multiply"]
//   - tool_pricing_json: {"calculator_add": 0.001, "calculator_subtract": 0.001} → {"add": 0.001, "subtract": 0.001}
func migrationRemoveServerPrefixFromMCPTools(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "remove_server_prefix_from_mcp_tools"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	// Helper function to check if a tool name has a prefix matching the client name
	// Handles both exact matches and legacy normalized forms
	hasClientPrefix := func(toolName, clientName string) (bool, string) {
		prefix := clientName + "_"
		if strings.HasPrefix(toolName, prefix) {
			return true, strings.TrimPrefix(toolName, prefix)
		}
		// Legacy prefix: normalize the substring before first underscore
		if idx := strings.IndexByte(toolName, '_'); idx > 0 {
			toolPrefix := toolName[:idx]
			unprefixed := toolName[idx+1:]
			if normalizeMCPClientName(toolPrefix) == clientName {
				return true, unprefixed
			}
		}
		return false, ""
	}

	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			// ============================================================
			// Step 1: Migrate config_mcp_clients table
			// ============================================================

			// Fetch all MCP clients
			var mcpClients []tables.TableMCPClient
			if err := tx.Find(&mcpClients).Error; err != nil {
				return fmt.Errorf("failed to fetch MCP clients: %w", err)
			}

			// Process each MCP client
			logger.Info("[configstore] %s: processing %d mcpClients", migrationName, len(mcpClients))
			for i := range mcpClients {
				client := &mcpClients[i]
				clientName := client.Name
				needsUpdate := false

				// Process tools_to_execute_json
				var toolsToExecute []string
				if client.ToolsToExecuteJSON != "" && client.ToolsToExecuteJSON != "null" {
					if err := json.Unmarshal([]byte(client.ToolsToExecuteJSON), &toolsToExecute); err != nil {
						return fmt.Errorf("failed to unmarshal tools_to_execute_json for client %s: %w", clientName, err)
					}

					// Strip prefix from each tool
					updatedTools := make([]string, 0, len(toolsToExecute))
					seenTools := make(map[string]bool)
					for _, tool := range toolsToExecute {
						// Check if tool has client prefix (handles both current and legacy normalized forms)
						if hasPrefix, unprefixedTool := hasClientPrefix(tool, clientName); hasPrefix {
							// Check for collision: if unprefixed tool already exists in the list
							if seenTools[unprefixedTool] {
								logger.Info("Collision detected when stripping prefix from tool '%s' for client '%s': unprefixed name '%s' already exists. Keeping unprefixed value.", tool, clientName, unprefixedTool)
								needsUpdate = true
								continue
							}
							seenTools[unprefixedTool] = true
							updatedTools = append(updatedTools, unprefixedTool)
							needsUpdate = true
						} else {
							// Tool already unprefixed or is wildcard "*"
							if seenTools[tool] {
								logger.Info("Duplicate tool name '%s' found for client '%s'. Keeping first occurrence.", tool, clientName)
								continue
							}
							seenTools[tool] = true
							updatedTools = append(updatedTools, tool)
						}
					}

					// Update the JSON
					if needsUpdate {
						updatedJSON, err := json.Marshal(updatedTools)
						if err != nil {
							return fmt.Errorf("failed to marshal updated tools_to_execute for client %s: %w", clientName, err)
						}
						client.ToolsToExecuteJSON = string(updatedJSON)
					}
				}

				// Process tools_to_auto_execute_json
				var toolsToAutoExecute []string
				if client.ToolsToAutoExecuteJSON != "" && client.ToolsToAutoExecuteJSON != "null" {
					if err := json.Unmarshal([]byte(client.ToolsToAutoExecuteJSON), &toolsToAutoExecute); err != nil {
						return fmt.Errorf("failed to unmarshal tools_to_auto_execute_json for client %s: %w", clientName, err)
					}

					// Strip prefix from each tool
					updatedAutoTools := make([]string, 0, len(toolsToAutoExecute))
					seenAutoTools := make(map[string]bool)
					for _, tool := range toolsToAutoExecute {
						// Check if tool has client prefix (handles both current and legacy normalized forms)
						if hasPrefix, unprefixedTool := hasClientPrefix(tool, clientName); hasPrefix {
							// Check for collision: if unprefixed tool already exists in the list
							if seenAutoTools[unprefixedTool] {
								logger.Info("Collision detected when stripping prefix from auto-execute tool '%s' for client '%s': unprefixed name '%s' already exists. Keeping unprefixed value.", tool, clientName, unprefixedTool)
								needsUpdate = true
								continue
							}
							seenAutoTools[unprefixedTool] = true
							updatedAutoTools = append(updatedAutoTools, unprefixedTool)
							needsUpdate = true
						} else {
							// Tool already unprefixed or is wildcard "*"
							if seenAutoTools[tool] {
								logger.Info("Duplicate auto-execute tool name '%s' found for client '%s'. Keeping first occurrence.", tool, clientName)
								continue
							}
							seenAutoTools[tool] = true
							updatedAutoTools = append(updatedAutoTools, tool)
						}
					}

					// Update the JSON
					if needsUpdate {
						updatedJSON, err := json.Marshal(updatedAutoTools)
						if err != nil {
							return fmt.Errorf("failed to marshal updated tools_to_auto_execute for client %s: %w", clientName, err)
						}
						client.ToolsToAutoExecuteJSON = string(updatedJSON)
					}
				}

				// Process tool_pricing_json
				var toolPricing map[string]float64
				if client.ToolPricingJSON != "" && client.ToolPricingJSON != "null" {
					if err := json.Unmarshal([]byte(client.ToolPricingJSON), &toolPricing); err != nil {
						return fmt.Errorf("failed to unmarshal tool_pricing_json for client %s: %w", clientName, err)
					}

					// Strip prefix from each tool name key
					updatedPricing := make(map[string]float64)
					for toolName, price := range toolPricing {
						// Check if tool has client prefix (handles both current and legacy normalized forms)
						if hasPrefix, unprefixedTool := hasClientPrefix(toolName, clientName); hasPrefix {
							// Check for collision: if unprefixed key already exists
							if existingPrice, exists := updatedPricing[unprefixedTool]; exists {
								logger.Info("Collision detected when stripping prefix from pricing key '%s' for client '%s': unprefixed key '%s' already exists with price %.6f. Keeping existing unprefixed value (%.6f), discarding prefixed value (%.6f).", toolName, clientName, unprefixedTool, existingPrice, existingPrice, price)
								needsUpdate = true
								continue
							}
							updatedPricing[unprefixedTool] = price
							needsUpdate = true
						} else {
							// Check for collision: if unprefixed key already exists (from a previously processed prefixed entry)
							if existingPrice, exists := updatedPricing[toolName]; exists {
								logger.Info("Collision detected for pricing key '%s' for client '%s': key already exists with price %.6f. Keeping first value (%.6f), discarding duplicate (%.6f).", toolName, clientName, existingPrice, existingPrice, price)
								continue
							}
							updatedPricing[toolName] = price
						}
					}

					// Update the JSON
					if needsUpdate {
						updatedJSON, err := json.Marshal(updatedPricing)
						if err != nil {
							return fmt.Errorf("failed to marshal updated tool_pricing for client %s: %w", clientName, err)
						}
						client.ToolPricingJSON = string(updatedJSON)
					}
				}

				// Save the updated client if any changes were made
				if needsUpdate {
					// Use Model + Updates to ensure changes are persisted
					result := tx.Model(&tables.TableMCPClient{}).Where("id = ?", client.ID).Updates(map[string]interface{}{
						"tools_to_execute_json":      client.ToolsToExecuteJSON,
						"tools_to_auto_execute_json": client.ToolsToAutoExecuteJSON,
						"tool_pricing_json":          client.ToolPricingJSON,
					})

					if result.Error != nil {
						return fmt.Errorf("failed to save updated MCP client %s: %w", clientName, result.Error)
					}
				}
			}

			// ============================================================
			// Step 2: Migrate governance_virtual_key_mcp_configs table
			// ============================================================

			// Fetch all virtual key MCP configs with their associated MCP client
			var vkMCPConfigs []tables.TableVirtualKeyMCPConfig
			if err := tx.Preload("MCPClient").Find(&vkMCPConfigs).Error; err != nil {
				return fmt.Errorf("failed to fetch virtual key MCP configs: %w", err)
			}

			// Process each VK MCP config
			logger.Info("[configstore] %s: processing %d vkMCPConfigs", migrationName, len(vkMCPConfigs))
			for i := range vkMCPConfigs {
				vkConfig := &vkMCPConfigs[i]
				if vkConfig.MCPClient.Name == "" {
					// Skip if MCP client is not loaded
					continue
				}

				clientName := vkConfig.MCPClient.Name
				needsUpdate := false

				// Process tools_to_execute (this is a JSON array stored in GORM's serializer format)
				if len(vkConfig.ToolsToExecute) > 0 {
					updatedTools := make([]string, 0, len(vkConfig.ToolsToExecute))
					seen := make(map[string]bool, len(vkConfig.ToolsToExecute))

					for _, tool := range vkConfig.ToolsToExecute {
						var finalTool string
						// Check if tool has client prefix (handles both current and legacy normalized forms)
						if hasPrefix, unprefixedTool := hasClientPrefix(tool, clientName); hasPrefix {
							finalTool = unprefixedTool
						} else {
							finalTool = tool
						}

						// Skip if we've already added this tool (collision detection)
						if !seen[finalTool] {
							seen[finalTool] = true
							updatedTools = append(updatedTools, finalTool)
						}
					}

					// Only update if the final list differs from the original
					needsUpdate = len(updatedTools) != len(vkConfig.ToolsToExecute)
					if !needsUpdate {
						// Check if any tools actually changed
						for j, tool := range vkConfig.ToolsToExecute {
							if tool != updatedTools[j] {
								needsUpdate = true
								break
							}
						}
					}

					if needsUpdate {
						vkConfig.ToolsToExecute = updatedTools
					}
				}

				// Save the updated VK config if any changes were made
				if needsUpdate {
					if err := tx.Save(vkConfig).Error; err != nil {
						return fmt.Errorf("failed to save updated VK MCP config ID %d: %w", vkConfig.ID, err)
					}
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			// Rollback is complex because we need to re-add the prefix
			// This requires knowing the client name for each tool
			tx = tx.WithContext(ctx)

			// ============================================================
			// Step 1: Rollback config_mcp_clients table
			// ============================================================

			var mcpClients []tables.TableMCPClient
			if err := tx.Find(&mcpClients).Error; err != nil {
				return fmt.Errorf("failed to fetch MCP clients for rollback: %w", err)
			}

			logger.Info("[configstore] %s: processing %d mcpClients", migrationName, len(mcpClients))
			for _, client := range mcpClients {
				clientName := client.Name
				needsUpdate := false

				// Rollback tools_to_execute_json
				var toolsToExecute []string
				if client.ToolsToExecuteJSON != "" && client.ToolsToExecuteJSON != "null" {
					if err := json.Unmarshal([]byte(client.ToolsToExecuteJSON), &toolsToExecute); err != nil {
						return fmt.Errorf("failed to unmarshal tools_to_execute_json for rollback: %w", err)
					}

					prefixedTools := make([]string, 0, len(toolsToExecute))
					for _, tool := range toolsToExecute {
						// Skip wildcard
						if tool == "*" {
							prefixedTools = append(prefixedTools, tool)
							continue
						}
						// Add prefix if not already present
						prefix := clientName + "_"
						if !strings.HasPrefix(tool, prefix) {
							prefixedTools = append(prefixedTools, prefix+tool)
							needsUpdate = true
						} else {
							prefixedTools = append(prefixedTools, tool)
						}
					}

					if needsUpdate {
						updatedJSON, err := json.Marshal(prefixedTools)
						if err != nil {
							return fmt.Errorf("failed to marshal rollback tools_to_execute: %w", err)
						}
						client.ToolsToExecuteJSON = string(updatedJSON)
					}
				}

				// Rollback tools_to_auto_execute_json
				var toolsToAutoExecute []string
				if client.ToolsToAutoExecuteJSON != "" && client.ToolsToAutoExecuteJSON != "null" {
					if err := json.Unmarshal([]byte(client.ToolsToAutoExecuteJSON), &toolsToAutoExecute); err != nil {
						return fmt.Errorf("failed to unmarshal tools_to_auto_execute_json for rollback: %w", err)
					}

					prefixedAutoTools := make([]string, 0, len(toolsToAutoExecute))
					for _, tool := range toolsToAutoExecute {
						if tool == "*" {
							prefixedAutoTools = append(prefixedAutoTools, tool)
							continue
						}
						prefix := clientName + "_"
						if !strings.HasPrefix(tool, prefix) {
							prefixedAutoTools = append(prefixedAutoTools, prefix+tool)
							needsUpdate = true
						} else {
							prefixedAutoTools = append(prefixedAutoTools, tool)
						}
					}

					if needsUpdate {
						updatedJSON, err := json.Marshal(prefixedAutoTools)
						if err != nil {
							return fmt.Errorf("failed to marshal rollback tools_to_auto_execute: %w", err)
						}
						client.ToolsToAutoExecuteJSON = string(updatedJSON)
					}
				}

				// Rollback tool_pricing_json
				var toolPricing map[string]float64
				if client.ToolPricingJSON != "" && client.ToolPricingJSON != "null" {
					if err := json.Unmarshal([]byte(client.ToolPricingJSON), &toolPricing); err != nil {
						return fmt.Errorf("failed to unmarshal tool_pricing_json for rollback: %w", err)
					}

					prefixedPricing := make(map[string]float64)
					for toolName, price := range toolPricing {
						prefix := clientName + "_"
						if !strings.HasPrefix(toolName, prefix) {
							prefixedPricing[prefix+toolName] = price
							needsUpdate = true
						} else {
							prefixedPricing[toolName] = price
						}
					}

					if needsUpdate {
						updatedJSON, err := json.Marshal(prefixedPricing)
						if err != nil {
							return fmt.Errorf("failed to marshal rollback tool_pricing: %w", err)
						}
						client.ToolPricingJSON = string(updatedJSON)
					}
				}

				if needsUpdate {
					if err := tx.Save(&client).Error; err != nil {
						return fmt.Errorf("failed to save rollback MCP client: %w", err)
					}
				}
			}

			// ============================================================
			// Step 2: Rollback governance_virtual_key_mcp_configs table
			// ============================================================

			var vkMCPConfigs []tables.TableVirtualKeyMCPConfig
			if err := tx.Preload("MCPClient").Find(&vkMCPConfigs).Error; err != nil {
				return fmt.Errorf("failed to fetch virtual key MCP configs for rollback: %w", err)
			}

			logger.Info("[configstore] %s: processing %d vkMCPConfigs", migrationName, len(vkMCPConfigs))
			for _, vkConfig := range vkMCPConfigs {
				if vkConfig.MCPClient.Name == "" {
					continue
				}

				clientName := vkConfig.MCPClient.Name
				needsUpdate := false

				if len(vkConfig.ToolsToExecute) > 0 {
					prefixedTools := make([]string, 0, len(vkConfig.ToolsToExecute))
					for _, tool := range vkConfig.ToolsToExecute {
						if tool == "*" {
							prefixedTools = append(prefixedTools, tool)
							continue
						}
						prefix := clientName + "_"
						if !strings.HasPrefix(tool, prefix) {
							prefixedTools = append(prefixedTools, prefix+tool)
							needsUpdate = true
						} else {
							prefixedTools = append(prefixedTools, tool)
						}
					}

					if needsUpdate {
						vkConfig.ToolsToExecute = prefixedTools
					}
				}

				if needsUpdate {
					if err := tx.Save(&vkConfig).Error; err != nil {
						return fmt.Errorf("failed to save rollback VK MCP config: %w", err)
					}
				}
			}

			return nil
		},
	}})

	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running migration to remove server prefix from MCP tools: %s", err.Error())
	}
	return nil
}

// migrationAddDistributedLocksTable adds the distributed_locks table for distributed locking
func migrationAddDistributedLocksTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_distributed_locks_table"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			// Use raw SQL with IF NOT EXISTS for atomic, race-condition-safe table creation
			createTableSQL := `
				CREATE TABLE IF NOT EXISTS distributed_locks (
					lock_key VARCHAR(255) PRIMARY KEY,
					holder_id VARCHAR(255) NOT NULL,
					expires_at TIMESTAMP NOT NULL,
					created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
				)
			`
			if err := tx.Exec(createTableSQL).Error; err != nil {
				return fmt.Errorf("failed to create distributed_locks table: %w", err)
			}
			// Create index on expires_at for efficient cleanup queries
			createIndexSQL := `CREATE INDEX IF NOT EXISTS idx_distributed_locks_expires_at ON distributed_locks (expires_at)`
			if err := tx.Exec(createIndexSQL).Error; err != nil {
				return fmt.Errorf("failed to create expires_at index: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := tx.Exec("DROP TABLE IF EXISTS distributed_locks").Error; err != nil {
				return fmt.Errorf("failed to drop distributed_locks table: %w", err)
			}
			return nil
		},
	}})

	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running distributed_locks table migration: %s", err.Error())
	}
	return nil
}

// migrationAddModelConfigTable adds the governance_model_configs table
func migrationAddModelConfigTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_model_config_table"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasTable(&tables.TableModelConfig{}) {
				logger.Info("[configstore] %s: creating table TableModelConfig", migrationName)
				if err := migrator.CreateTable(&tables.TableModelConfig{}); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			logger.Info("[configstore] %s: dropping table TableModelConfig", migrationName)
			if err := migrator.DropTable(&tables.TableModelConfig{}); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running add model config table migration: %s", err.Error())
	}
	return nil
}

// migrationAddProviderGovernanceColumns adds budget_id and rate_limit_id columns to config_providers table
func migrationAddProviderGovernanceColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_provider_governance_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			provider := &tables.TableProvider{}

			// Add budget_id column if it doesn't exist
			if err := addColumnIfNotExists(tx, logger, provider, "budget_id"); err != nil {
				return fmt.Errorf("failed to add budget_id column: %w", err)
			}
			// Create index for budget_id (outside HasColumn to handle reruns where column exists but index doesn't)
			if !migrator.HasIndex(provider, "idx_provider_budget") {
				if err := tx.Exec("CREATE INDEX IF NOT EXISTS idx_provider_budget ON config_providers (budget_id)").Error; err != nil {
					return fmt.Errorf("failed to create budget_id index: %w", err)
				}
			}

			// Add rate_limit_id column if it doesn't exist
			if err := addColumnIfNotExists(tx, logger, provider, "rate_limit_id"); err != nil {
				return fmt.Errorf("failed to add rate_limit_id column: %w", err)
			}
			// Create index for rate_limit_id (outside HasColumn to handle reruns where column exists but index doesn't)
			if !migrator.HasIndex(provider, "idx_provider_rate_limit") {
				if err := tx.Exec("CREATE INDEX IF NOT EXISTS idx_provider_rate_limit ON config_providers (rate_limit_id)").Error; err != nil {
					return fmt.Errorf("failed to create rate_limit_id index: %w", err)
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			provider := &tables.TableProvider{}

			// Drop indexes first
			if migrator.HasIndex(provider, "idx_provider_rate_limit") {
				if err := tx.Exec("DROP INDEX IF EXISTS idx_provider_rate_limit").Error; err != nil {
					return fmt.Errorf("failed to drop rate_limit_id index: %w", err)
				}
			}

			if migrator.HasIndex(provider, "idx_provider_budget") {
				if err := tx.Exec("DROP INDEX IF EXISTS idx_provider_budget").Error; err != nil {
					return fmt.Errorf("failed to drop budget_id index: %w", err)
				}
			}

			// Drop rate_limit_id column if it exists
			if err := dropColumnIfExists(tx, logger, provider, "rate_limit_id"); err != nil {
				return fmt.Errorf("failed to drop rate_limit_id column: %w", err)
			}

			// Drop budget_id column if it exists
			if err := dropColumnIfExists(tx, logger, provider, "budget_id"); err != nil {
				return fmt.Errorf("failed to drop budget_id column: %w", err)
			}

			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running add provider governance columns migration: %s", err.Error())
	}
	return nil
}

// migrationAddModelConfigScopeColumns adds the scope and scope_id columns to
// governance_model_configs and swaps the unique index from (model_name, provider)
// to (scope, scope_id, model_name, provider). Existing rows are backfilled to the
// "global" scope, preserving pre-scope behavior. The new index is created before
// the old one is dropped so uniqueness is never unenforced during the migration.
func migrationAddModelConfigScopeColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_model_config_scope_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			modelConfig := &tables.TableModelConfig{}

			// Add scope column (NOT NULL DEFAULT 'global' backfills existing rows).
			if err := addColumnIfNotExists(tx, logger, modelConfig, "scope"); err != nil {
				return fmt.Errorf("failed to add scope column: %w", err)
			}
			// Add scope_id column (nullable).
			if err := addColumnIfNotExists(tx, logger, modelConfig, "scope_id"); err != nil {
				return fmt.Errorf("failed to add scope_id column: %w", err)
			}
			// Belt-and-suspenders backfill in case the column default did not populate
			// existing rows on this dialect.
			if err := tx.Exec("UPDATE governance_model_configs SET scope = ? WHERE scope IS NULL OR scope = ''", tables.ModelConfigScopeGlobal).Error; err != nil {
				return fmt.Errorf("failed to backfill scope: %w", err)
			}

			// Create the new composite unique index BEFORE dropping the old one. The
			// composite index is strictly more selective, so already-unique rows stay
			// unique under it; this ordering avoids any window where uniqueness is
			// unenforced. CreateIndex reads the struct tags so it is dialect-safe.
			if !migrator.HasIndex(modelConfig, "idx_model_scope_provider") {
				logger.Info("[configstore] %s: creating index idx_model_scope_provider on TableModelConfig", migrationName)
				if err := migrator.CreateIndex(modelConfig, "idx_model_scope_provider"); err != nil {
					return fmt.Errorf("failed to create idx_model_scope_provider: %w", err)
				}
			}
			// Drop the now-superseded (model_name, provider) unique index.
			if migrator.HasIndex(modelConfig, "idx_model_provider") {
				logger.Info("[configstore] %s: dropping index idx_model_provider on TableModelConfig", migrationName)
				if err := migrator.DropIndex(modelConfig, "idx_model_provider"); err != nil {
					return fmt.Errorf("failed to drop idx_model_provider: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return fmt.Errorf("add_model_config_scope_columns is non-rollbackable: scope-aware rows and the previous uniqueness invariant cannot be restored safely")
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running add model config scope columns migration: %s", err.Error())
	}
	return nil
}

// migrationMigrateProviderGovernanceToModelConfigs folds provider-level governance
// (config_providers.budget_id / rate_limit_id) into governance_model_configs as
// (scope='global', provider=<name>, model_name='*') "all models on this provider" rows,
// reusing the same budget/rate-limit rows. It then NULLs the provider FKs so the old
// provider-governance enforcement path goes inert (single source of truth = model_configs).
func migrationMigrateProviderGovernanceToModelConfigs(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "migrate_provider_governance_to_model_configs"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			// Guard: only run once the model-config table + scope columns exist.
			if !tx.Migrator().HasTable(&tables.TableModelConfig{}) || !tx.Migrator().HasColumn(&tables.TableModelConfig{}, "scope") {
				return nil
			}

			var providers []tables.TableProvider
			if err := tx.Where("budget_id IS NOT NULL OR rate_limit_id IS NOT NULL").Find(&providers).Error; err != nil {
				return fmt.Errorf("failed to load providers with governance: %w", err)
			}

			now := time.Now()
			logger.Info("[configstore] %s: processing %d providers", migrationName, len(providers))
			for i := range providers {
				p := &providers[i]

				// Load any existing global all-models row for this provider. This keeps the
				// migration idempotent (a re-run skips re-insert) AND lossless: if a wildcard
				// row already exists - hand-created via the UI, or left by a partial prior run -
				// we merge the provider's governance into it instead of skipping, so clearing
				// the provider FKs below can never silently drop budget/rate-limit ownership.
				// Select only the columns that exist at this point in the migration sequence
				// (later columns like calendar_aligned do not yet exist).
				var existing tables.TableModelConfig
				err := tx.
					Where("scope = ? AND scope_id IS NULL AND model_name = ? AND provider = ?",
						tables.ModelConfigScopeGlobal, tables.ModelConfigAllModels, p.Name).
					Select("id", "budget_id", "rate_limit_id").
					First(&existing).Error
				switch {
				case err == gorm.ErrRecordNotFound:
					providerName := p.Name
					// Insert via an explicit column map rather than the live TableModelConfig
					// struct: GORM derives the INSERT column list from today's struct, so a
					// column added by a *later* migration (e.g. calendar_aligned, added by
					// add_model_config_calendar_aligned_column further down the sequence) would
					// otherwise appear in this INSERT before its column exists and fail boot.
					// The map pins this historical migration to the columns present at this point.
					if err := tx.Table((tables.TableModelConfig{}).TableName()).Create(map[string]any{
						"id":            uuid.NewString(),
						"model_name":    tables.ModelConfigAllModels,
						"provider":      providerName,
						"scope":         tables.ModelConfigScopeGlobal,
						"budget_id":     p.BudgetID,
						"rate_limit_id": p.RateLimitID,
						"created_at":    now,
						"updated_at":    now,
					}).Error; err != nil {
						return fmt.Errorf("failed to create wildcard model config for provider %q: %w", p.Name, err)
					}
				case err != nil:
					return fmt.Errorf("failed to check existing wildcard config for provider %q: %w", p.Name, err)
				default:
					// A wildcard row already exists: backfill only the governance slots it lacks,
					// so we never overwrite values already present. This is commutative and safe
					// to repeat (a clean re-run finds nothing to fill and writes nothing).
					updates := map[string]any{}
					if existing.BudgetID == nil && p.BudgetID != nil {
						updates["budget_id"] = p.BudgetID
					}
					if existing.RateLimitID == nil && p.RateLimitID != nil {
						updates["rate_limit_id"] = p.RateLimitID
					}
					if len(updates) > 0 {
						updates["updated_at"] = now
						if err := tx.Table((tables.TableModelConfig{}).TableName()).
							Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
							return fmt.Errorf("failed to merge provider governance into wildcard model config for provider %q: %w", p.Name, err)
						}
					}
				}

				// Detach governance from the provider (FK rows are reused by the model config above).
				if err := tx.Model(&tables.TableProvider{}).Where("name = ?", p.Name).
					Updates(map[string]any{"budget_id": nil, "rate_limit_id": nil}).Error; err != nil {
					return fmt.Errorf("failed to clear governance FKs for provider %q: %w", p.Name, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			// Nothing to reverse if the model-config table/scope columns are gone.
			if !tx.Migrator().HasTable(&tables.TableModelConfig{}) || !tx.Migrator().HasColumn(&tables.TableModelConfig{}, "scope") {
				return nil
			}

			// Reverse provider-level wildcards:
			// (scope='global', scope_id IS NULL, model_name='*', provider IS NOT NULL).
			var wildcards []tables.TableModelConfig
			if err := tx.Where(
				"scope = ? AND scope_id IS NULL AND model_name = ? AND provider IS NOT NULL",
				tables.ModelConfigScopeGlobal, tables.ModelConfigAllModels,
			).Find(&wildcards).Error; err != nil {
				return fmt.Errorf("failed to load provider wildcard configs: %w", err)
			}

			logger.Info("[configstore] %s: processing %d wildcards", migrationName, len(wildcards))
			for i := range wildcards {
				mc := &wildcards[i]
				// Re-attach the budget/rate-limit FK rows to the provider row.
				if err := tx.Model(&tables.TableProvider{}).Where("name = ?", *mc.Provider).
					Updates(map[string]any{"budget_id": mc.BudgetID, "rate_limit_id": mc.RateLimitID}).Error; err != nil {
					return fmt.Errorf("failed to restore governance FKs for provider %q: %w", *mc.Provider, err)
				}
				// Drop the wildcard model config; its FK rows now live on the provider again.
				if err := tx.Delete(&tables.TableModelConfig{}, "id = ?", mc.ID).Error; err != nil {
					return fmt.Errorf("failed to delete wildcard config for provider %q: %w", *mc.Provider, err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running migrate provider governance to model configs migration: %s", err.Error())
	}
	return nil
}

// migrationAddBudgetModelConfigIDColumn adds governance_budgets.model_config_id and
// backfills it from the legacy single governance_model_configs.budget_id, inverting
// budget ownership so a model config can own multiple budgets via the FK.
func migrationAddBudgetModelConfigIDColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_budget_model_config_id_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mig := tx.Migrator()

			if err := addColumnIfNotExists(tx, logger, &tables.TableBudget{}, "model_config_id"); err != nil {
				return fmt.Errorf("failed to add model_config_id column: %w", err)
			}

			// Backfill from the legacy single budget_id. Idempotent via the IS NULL guard.
			if !mig.HasColumn(&tables.TableModelConfig{}, "budget_id") {
				return nil
			}
			var mcs []tables.TableModelConfig
			if err := tx.Where("budget_id IS NOT NULL").Find(&mcs).Error; err != nil {
				return fmt.Errorf("failed to load model configs with budgets: %w", err)
			}
			logger.Info("[configstore] %s: processing %d mcs", migrationName, len(mcs))
			for i := range mcs {
				mc := &mcs[i]
				if mc.BudgetID == nil {
					continue
				}
				if err := tx.Exec(
					"UPDATE governance_budgets SET model_config_id = ? WHERE id = ? AND model_config_id IS NULL",
					mc.ID, *mc.BudgetID,
				).Error; err != nil {
					return fmt.Errorf("failed to backfill model_config_id for budget %q: %w", *mc.BudgetID, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return fmt.Errorf("add_budget_model_config_id_column is non-rollbackable: dropping model_config_id would permanently lose multi-budget ownership data that cannot be recovered from the legacy single budget_id column")
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running add budget model_config_id column migration: %s", err.Error())
	}
	return nil
}

// ensureVKModelConfig returns the ID of the VK-scoped model config for the given
// (vkID, provider) pair, creating it if absent.
func ensureVKModelConfig(tx *gorm.DB, vkID string, provider *string, calendarAligned bool, now time.Time) (string, error) {
	q := tx.Model(&tables.TableModelConfig{}).
		Where("scope = ? AND scope_id = ? AND model_name = ?",
			tables.ModelConfigScopeVirtualKey, vkID, tables.ModelConfigAllModels)
	if provider == nil {
		q = q.Where("provider IS NULL")
	} else {
		q = q.Where("provider = ?", *provider)
	}
	var existing []tables.TableModelConfig
	if err := q.Limit(1).Find(&existing).Error; err != nil {
		return "", fmt.Errorf("failed to look up VK model config: %w", err)
	}
	if len(existing) > 0 {
		return existing[0].ID, nil
	}
	mc := tables.TableModelConfig{
		ID:              uuid.NewString(),
		ModelName:       tables.ModelConfigAllModels,
		Provider:        provider,
		Scope:           tables.ModelConfigScopeVirtualKey,
		ScopeID:         &vkID,
		CalendarAligned: calendarAligned,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := tx.Create(&mc).Error; err != nil {
		return "", fmt.Errorf("failed to create VK model config: %w", err)
	}
	return mc.ID, nil
}

// migrationMigrateVirtualKeyGovernanceToModelConfigs folds VK-level governance into
// model_configs as VK-scoped all-models wildcard rows:
//   - VK top-level budgets/rate-limit -> (scope=virtual_key, scope_id=vk, model_name='*', provider=NULL)
//   - per-provider-config budgets/rate-limit -> (..., provider=<that provider>)
func migrationMigrateVirtualKeyGovernanceToModelConfigs(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "migrate_virtual_key_governance_to_model_configs"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			// Required tables/columns must exist (scope columns + the new owner FK).
			if !tx.Migrator().HasTable(&tables.TableModelConfig{}) ||
				!tx.Migrator().HasColumn(&tables.TableModelConfig{}, "scope") ||
				!tx.Migrator().HasColumn(&tables.TableBudget{}, "model_config_id") {
				return nil
			}

			var vks []tables.TableVirtualKey
			if err := tx.Preload("Budgets").Preload("ProviderConfigs").Preload("ProviderConfigs.Budgets").
				Find(&vks).Error; err != nil {
				return fmt.Errorf("failed to load virtual keys: %w", err)
			}

			now := time.Now()
			logger.Info("[configstore] %s: processing %d vks", migrationName, len(vks))
			for i := range vks {
				vk := &vks[i]

				// VK top-level governance -> all-providers wildcard.
				if len(vk.Budgets) > 0 || vk.RateLimitID != nil {
					mcID, err := ensureVKModelConfig(tx, vk.ID, nil, vk.CalendarAligned, now)
					if err != nil {
						return err
					}
					logger.Info("[configstore] %s: processing %d vk.Budgets", migrationName, len(vk.Budgets))
					for _, b := range vk.Budgets {
						if err := tx.Exec(
							"UPDATE governance_budgets SET model_config_id = ?, virtual_key_id = NULL WHERE id = ? AND model_config_id IS NULL",
							mcID, b.ID,
						).Error; err != nil {
							return fmt.Errorf("failed to reparent VK budget %q: %w", b.ID, err)
						}
					}
					if vk.RateLimitID != nil {
						if err := tx.Exec("UPDATE governance_model_configs SET rate_limit_id = ? WHERE id = ?", *vk.RateLimitID, mcID).Error; err != nil {
							return fmt.Errorf("failed to move VK rate limit to model config: %w", err)
						}
						if err := tx.Exec("UPDATE governance_virtual_keys SET rate_limit_id = NULL WHERE id = ?", vk.ID).Error; err != nil {
							return fmt.Errorf("failed to clear VK rate limit FK: %w", err)
						}
					}
				}

				// Per-provider-config governance -> provider-specific wildcard.
				logger.Info("[configstore] %s: processing %d vk.ProviderConfigs", migrationName, len(vk.ProviderConfigs))
				for j := range vk.ProviderConfigs {
					pc := &vk.ProviderConfigs[j]
					if len(pc.Budgets) == 0 && pc.RateLimitID == nil {
						continue
					}
					provider := pc.Provider
					mcID, err := ensureVKModelConfig(tx, vk.ID, &provider, vk.CalendarAligned, now)
					if err != nil {
						return err
					}
					logger.Info("[configstore] %s: processing %d pc.Budgets", migrationName, len(pc.Budgets))
					for _, b := range pc.Budgets {
						if err := tx.Exec(
							"UPDATE governance_budgets SET model_config_id = ?, provider_config_id = NULL WHERE id = ? AND model_config_id IS NULL",
							mcID, b.ID,
						).Error; err != nil {
							return fmt.Errorf("failed to reparent provider-config budget %q: %w", b.ID, err)
						}
					}
					if pc.RateLimitID != nil {
						if err := tx.Exec("UPDATE governance_model_configs SET rate_limit_id = ? WHERE id = ?", *pc.RateLimitID, mcID).Error; err != nil {
							return fmt.Errorf("failed to move provider-config rate limit to model config: %w", err)
						}
						if err := tx.Exec("UPDATE governance_virtual_key_provider_configs SET rate_limit_id = NULL WHERE id = ?", pc.ID).Error; err != nil {
							return fmt.Errorf("failed to clear provider-config rate limit FK: %w", err)
						}
					}
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if !tx.Migrator().HasTable(&tables.TableModelConfig{}) ||
				!tx.Migrator().HasColumn(&tables.TableModelConfig{}, "scope") {
				return nil
			}

			// Only the VK-scoped all-models wildcards this migration creates.
			var mcs []tables.TableModelConfig
			if err := tx.Where("scope = ? AND model_name = ?",
				tables.ModelConfigScopeVirtualKey, tables.ModelConfigAllModels).Find(&mcs).Error; err != nil {
				return fmt.Errorf("failed to load VK wildcard model configs: %w", err)
			}

			logger.Info("[configstore] %s: processing %d mcs", migrationName, len(mcs))
			for i := range mcs {
				mc := &mcs[i]
				if mc.ScopeID == nil {
					continue
				}
				var budgets []tables.TableBudget
				if err := tx.Where("model_config_id = ?", mc.ID).Find(&budgets).Error; err != nil {
					return fmt.Errorf("failed to load budgets for model config %q: %w", mc.ID, err)
				}

				if mc.Provider == nil {
					// VK top-level: restore VK ownership + rate limit.
					logger.Info("[configstore] %s: processing %d budgets", migrationName, len(budgets))
					for _, b := range budgets {
						if err := tx.Exec("UPDATE governance_budgets SET virtual_key_id = ?, model_config_id = NULL WHERE id = ?", *mc.ScopeID, b.ID).Error; err != nil {
							return fmt.Errorf("failed to restore VK budget %q: %w", b.ID, err)
						}
					}
					if mc.RateLimitID != nil {
						if err := tx.Exec("UPDATE governance_virtual_keys SET rate_limit_id = ? WHERE id = ?", *mc.RateLimitID, *mc.ScopeID).Error; err != nil {
							return fmt.Errorf("failed to restore VK rate limit: %w", err)
						}
					}
				} else {
					// Provider-specific: find the matching provider config to restore onto.
					var pcs []tables.TableVirtualKeyProviderConfig
					if err := tx.Where("virtual_key_id = ? AND provider = ?", *mc.ScopeID, *mc.Provider).
						Limit(1).Find(&pcs).Error; err != nil {
						return fmt.Errorf("failed to find provider config for VK %q provider %q: %w", *mc.ScopeID, *mc.Provider, err)
					}
					if len(pcs) > 0 {
						pcID := pcs[0].ID
						logger.Info("[configstore] %s: processing %d budgets", migrationName, len(budgets))
						for _, b := range budgets {
							if err := tx.Exec("UPDATE governance_budgets SET provider_config_id = ?, model_config_id = NULL WHERE id = ?", pcID, b.ID).Error; err != nil {
								return fmt.Errorf("failed to restore provider-config budget %q: %w", b.ID, err)
							}
						}
						if mc.RateLimitID != nil {
							if err := tx.Exec("UPDATE governance_virtual_key_provider_configs SET rate_limit_id = ? WHERE id = ?", *mc.RateLimitID, pcID).Error; err != nil {
								return fmt.Errorf("failed to restore provider-config rate limit: %w", err)
							}
						}
					}
				}

				if err := tx.Delete(&tables.TableModelConfig{}, "id = ?", mc.ID).Error; err != nil {
					return fmt.Errorf("failed to delete VK wildcard model config %q: %w", mc.ID, err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running migrate virtual key governance to model configs migration: %s", err.Error())
	}
	return nil
}

// migrationAddModelConfigCalendarAlignedColumn adds governance_model_configs.calendar_aligned
// and backfills VK-scoped wildcards from their owning virtual key. Budgets folded out of a
// calendar-aligned VK then keep snapping resets to calendar boundaries.
func migrationAddModelConfigCalendarAlignedColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_model_config_calendar_aligned_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := addColumnIfNotExists(tx, logger, &tables.TableModelConfig{}, "calendar_aligned"); err != nil {
				return fmt.Errorf("failed to add calendar_aligned column: %w", err)
			}

			// Backfill VK-scoped configs from their owning VK.
			type vkRow struct {
				ID              string
				CalendarAligned bool
			}
			var rows []vkRow
			if err := tx.Table("governance_virtual_keys").Select("id, calendar_aligned").Scan(&rows).Error; err != nil {
				return fmt.Errorf("failed to load virtual keys for calendar_aligned backfill: %w", err)
			}
			logger.Info("[configstore] %s: processing %d rows", migrationName, len(rows))
			for _, r := range rows {
				if !r.CalendarAligned {
					continue // default is already false
				}
				if err := tx.Exec(
					"UPDATE governance_model_configs SET calendar_aligned = ? WHERE scope = ? AND scope_id = ?",
					true, tables.ModelConfigScopeVirtualKey, r.ID,
				).Error; err != nil {
					return fmt.Errorf("failed to backfill calendar_aligned for VK %q: %w", r.ID, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableModelConfig{}, "calendar_aligned"); err != nil {
				return fmt.Errorf("failed to drop calendar_aligned column: %w", err)
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running add model config calendar_aligned column migration: %s", err.Error())
	}
	return nil
}

// migrationAddAllowedHeadersJSONColumn adds the allowed_headers_json column to the client config table
func migrationAddAllowedHeadersJSONColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_allowed_headers_json_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "allowed_headers_json"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "allowed_headers_json"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

// migrationAddDisableDBPingsInHealthColumn adds the disable_db_pings_in_health column to the client config table
func migrationAddDisableDBPingsInHealthColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_disable_db_pings_in_health_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "disable_db_pings_in_health"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "disable_db_pings_in_health"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

// migrationAddDumpErrorsInConsoleLogsColumn adds the dump_errors_in_console_logs column to the client config table
func migrationAddDumpErrorsInConsoleLogsColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_dump_errors_in_console_logs_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "dump_errors_in_console_logs"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "dump_errors_in_console_logs"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running db migration: %s", err.Error())
	}
	return nil
}

// migrationAddIsPingAvailableColumnToMCPClientTable adds the is_ping_available column to the config_mcp_clients table
func migrationAddIsPingAvailableColumnToMCPClientTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_is_ping_available_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&tables.TableMCPClient{}, "is_ping_available") {
				if err := addColumnIfNotExists(tx, logger, &tables.TableMCPClient{}, "is_ping_available"); err != nil {
					return err
				}
				// Set default value for existing rows
				if err := tx.Model(&tables.TableMCPClient{}).Where("is_ping_available IS NULL").Update("is_ping_available", true).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableMCPClient{}, "is_ping_available"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running is_ping_available migration: %s", err.Error())
	}
	return nil
}

// migrationAddRoutingRulesTable adds the routing rules table for intelligent request routing
func migrationAddRoutingRulesTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_routing_rules_table"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			if !migrator.HasTable(&tables.TableRoutingRule{}) {
				logger.Info("[configstore] %s: creating table TableRoutingRule", migrationName)
				if err := migrator.CreateTable(&tables.TableRoutingRule{}); err != nil {
					return err
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			logger.Info("[configstore] %s: dropping table TableRoutingRule", migrationName)
			if err := migrator.DropTable(&tables.TableRoutingRule{}); err != nil {
				return err
			}

			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running routing_rules_table migration: %s", err.Error())
	}
	return nil
}

// migrationAddOAuthTables creates the oauth_configs and oauth_tokens tables
func migrationAddOAuthTables(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_oauth_tables"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			// Create oauth_configs table FIRST (before adding FK columns that reference it)
			if !migrator.HasTable(&tables.TableOauthConfig{}) {
				logger.Info("[configstore] %s: creating table TableOauthConfig", migrationName)
				if err := migrator.CreateTable(&tables.TableOauthConfig{}); err != nil {
					return fmt.Errorf("failed to create oauth_configs table: %w", err)
				}
			}
			// Create oauth_tokens table
			if !migrator.HasTable(&tables.TableOauthToken{}) {
				logger.Info("[configstore] %s: creating table TableOauthToken", migrationName)
				if err := migrator.CreateTable(&tables.TableOauthToken{}); err != nil {
					return fmt.Errorf("failed to create oauth_tokens table: %w", err)
				}
			}
			// IF MCPClient table is not present, create it first
			if !migrator.HasTable(&tables.TableMCPClient{}) {
				logger.Info("[configstore] %s: creating table TableMCPClient", migrationName)
				if err := migrator.CreateTable(&tables.TableMCPClient{}); err != nil {
					return fmt.Errorf("failed to create mcp_clients table: %w", err)
				}
			}
			// Now update MCPClient table to add auth_type, oauth_config_id columns
			// (oauth_config_id has FK constraint to oauth_configs table created above)
			if err := addColumnIfNotExists(tx, logger, &tables.TableMCPClient{}, "auth_type"); err != nil {
				return fmt.Errorf("failed to add auth_type column: %w", err)
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TableMCPClient{}, "oauth_config_id"); err != nil {
				return fmt.Errorf("failed to add oauth_config_id column: %w", err)
			}
			// Set default value for auth_type column
			if err := tx.Model(&tables.TableMCPClient{}).Where("auth_type IS NULL").Update("auth_type", "headers").Error; err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			// Drop tables in reverse order
			if migrator.HasTable(&tables.TableOauthToken{}) {
				logger.Info("[configstore] %s: dropping table TableOauthToken", migrationName)
				if err := migrator.DropTable(&tables.TableOauthToken{}); err != nil {
					return fmt.Errorf("failed to drop oauth_tokens table: %w", err)
				}
			}

			if migrator.HasTable(&tables.TableOauthConfig{}) {
				logger.Info("[configstore] %s: dropping table TableOauthConfig", migrationName)
				if err := migrator.DropTable(&tables.TableOauthConfig{}); err != nil {
					return fmt.Errorf("failed to drop oauth_configs table: %w", err)
				}
			}

			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running oauth tables migration: %s", err.Error())
	}
	return nil
}

// migrationAddToolSyncIntervalColumns adds the tool_sync_interval columns to config_client and config_mcp_clients tables
func migrationAddToolSyncIntervalColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_tool_sync_interval_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			// Add mcp_tool_sync_interval column to config_client table (global setting)
			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "mcp_tool_sync_interval"); err != nil {
				return err
			}
			// Add tool_sync_interval column to config_mcp_clients table (per-client setting)
			if err := addColumnIfNotExists(tx, logger, &tables.TableMCPClient{}, "tool_sync_interval"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "mcp_tool_sync_interval"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &tables.TableMCPClient{}, "tool_sync_interval"); err != nil {
				return err
			}

			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running tool sync interval migration: %s", err.Error())
	}
	return nil
}

// migrationConvertMCPClientToolSyncIntervalMinutesToSeconds converts legacy
// config_mcp_clients.tool_sync_interval values from minutes to seconds.
// Legacy storage used minutes; runtime now persists seconds to preserve
// sub-minute precision. We only convert positive values; 0 means "use global"
// and negative values mean "disabled".
func migrationConvertMCPClientToolSyncIntervalMinutesToSeconds(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "convert_mcp_client_tool_sync_interval_minutes_to_seconds"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return tx.Exec(`
				UPDATE config_mcp_clients
				SET tool_sync_interval = tool_sync_interval * 60
				WHERE tool_sync_interval > 0
			`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			// Best-effort rollback for migrated rows.
			return tx.Exec(`
				UPDATE config_mcp_clients
				SET tool_sync_interval = tool_sync_interval / 60
				WHERE tool_sync_interval > 0
				AND tool_sync_interval % 60 = 0
			`).Error
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running mcp client tool sync interval unit conversion migration: %s", err.Error())
	}
	return nil
}

// migrationAddMCPClientConfigToOAuthConfig adds the mcp_client_config_json column to oauth_configs table
// This enables multi-instance support by storing pending MCP client config in the database
// instead of in-memory, so OAuth callbacks can be handled by any server instance
func migrationAddMCPClientConfigToOAuthConfig(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_mcp_client_config_to_oauth_config"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableOauthConfig{}, "mcp_client_config_json"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableOauthConfig{}, "mcp_client_config_json"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running mcp client config oauth migration: %s", err.Error())
	}
	return nil
}

// migrationAddBaseModelPricingColumn adds the base_model column to the model_pricing table
func migrationAddBaseModelPricingColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_base_model_pricing_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableModelPricing{}, "base_model"); err != nil {
				return fmt.Errorf("failed to add column base_model: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableModelPricing{}, "base_model"); err != nil {
				return fmt.Errorf("failed to drop column base_model: %w", err)
			}
			return nil
		},
	}})
	return m.Migrate()
}

// migrationAddAzureScopesColumn adds the azure_scopes column to the key table for Entra ID OAuth scopes
func migrationAddAzureScopesColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_azure_scopes_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, "azure_scopes"); err != nil {
				return fmt.Errorf("failed to add azure_scopes column: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, "azure_scopes"); err != nil {
				return fmt.Errorf("failed to drop azure_scopes column: %w", err)
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running azure_scopes migration: %s", err.Error())
	}
	return nil
}

// migrationAddReplicateDeploymentsJSONColumn adds the replicate_deployments_json column to the key table.
// This column is later dropped by migrationDropDeploymentColumnsAndAddAliases after data is migrated.
func migrationAddReplicateDeploymentsJSONColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_replicate_deployments_json_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if !tx.Migrator().HasColumn(&tables.TableKey{}, "replicate_deployments_json") {
				if err := tx.Exec("ALTER TABLE config_keys ADD COLUMN replicate_deployments_json TEXT").Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if tx.Migrator().HasColumn(&tables.TableKey{}, "replicate_deployments_json") {
				if err := tx.Exec("ALTER TABLE config_keys DROP COLUMN replicate_deployments_json").Error; err != nil {
					return err
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running replicate deployments JSON migration: %s", err.Error())
	}
	return nil
}

// migrationDropDeploymentColumnsAndAddAliases adds the unified aliases_json column, migrates
// existing per-provider deployment data into it, then drops the legacy columns.
// Only one deployment column will be populated per row (they were mutually exclusive).
func migrationDropDeploymentColumnsAndAddAliases(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "drop_deployment_columns_and_add_aliases"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			m := tx.Migrator()

			// Add aliases_json column first
			if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, "aliases_json"); err != nil {
				return err
			}

			// Copy data from whichever legacy deployment column is populated into aliases_json.
			// Only rows where aliases_json is not already set are touched.
			// Exactly one deployment column will be non-null per row (they were mutually exclusive).
			for _, col := range []string{
				"azure_deployments_json",
				"vertex_deployments_json",
				"bedrock_deployments_json",
				"replicate_deployments_json",
			} {
				if !m.HasColumn(&tables.TableKey{}, col) {
					continue
				}
				if err := tx.Exec(
					"UPDATE config_keys SET aliases_json = " + col +
						" WHERE aliases_json IS NULL AND " + col + " IS NOT NULL AND " + col + " != ''",
				).Error; err != nil {
					return err
				}
			}

			// Drop legacy deployment columns
			for _, col := range []string{
				"azure_deployments_json",
				"vertex_deployments_json",
				"bedrock_deployments_json",
				"replicate_deployments_json",
			} {
				if m.HasColumn(&tables.TableKey{}, col) {
					if err := tx.Exec("ALTER TABLE config_keys DROP COLUMN " + col).Error; err != nil {
						return err
					}
				}
			}

			// Fail fast if there are encrypted rows that need fixup but encryption isn't initialized.
			// The migration drops legacy deployment columns below, so skipping this fixup
			// would silently lose the ability to recover those values.
			// This case will ideally never happen
			var encryptedAliasCount int64
			if err := tx.Table("config_keys").
				Where(
					"encryption_status = ? AND aliases_json IS NOT NULL AND aliases_json != '' AND aliases_json != '{}'",
					tables.EncryptionStatusEncrypted,
				).
				Count(&encryptedAliasCount).Error; err != nil {
				return fmt.Errorf("failed to count encrypted aliases for fixup: %w", err)
			}
			if encryptedAliasCount > 0 && !encrypt.IsEnabled() {
				return fmt.Errorf("encryption must be enabled before migrating encrypted aliases")
			}

			// Encrypt aliases_json for rows where encryption_status is already 'encrypted'.
			// The raw SQL copy above preserved the original column's encryption state:
			// - bedrock_deployments_json was encrypted -> aliases_json is already encrypted
			// - azure/vertex/replicate were never encrypted -> aliases_json is plaintext
			// AfterFind will try to decrypt aliases_json for encrypted rows, so we must
			// encrypt any plaintext values first.
			if encrypt.IsEnabled() {
				type aliasRow struct {
					ID          uint
					AliasesJSON *string
				}
				var plainRows []aliasRow
				if err := tx.Raw(
					"SELECT id, aliases_json FROM config_keys WHERE encryption_status = ? AND aliases_json IS NOT NULL AND aliases_json != '' AND aliases_json != '{}'",
					tables.EncryptionStatusEncrypted,
				).Scan(&plainRows).Error; err != nil {
					return fmt.Errorf("failed to fetch aliases for encryption fixup: %w", err)
				}
				logger.Info("[configstore] %s: processing %d plainRows", migrationName, len(plainRows))
				for _, row := range plainRows {
					if row.AliasesJSON == nil || *row.AliasesJSON == "" {
						continue
					}
					// If Decrypt succeeds, the value is already encrypted — skip it (bedrock case).
					// If Decrypt fails, the value is plaintext — encrypt it.
					if _, err := encrypt.Decrypt(*row.AliasesJSON); err != nil {
						if !json.Valid([]byte(*row.AliasesJSON)) {
							return fmt.Errorf("failed to decrypt aliases for key %d: %w", row.ID, err)
						}
						encrypted, encErr := encrypt.Encrypt(*row.AliasesJSON)
						if encErr != nil {
							return fmt.Errorf("failed to encrypt aliases for key %d: %w", row.ID, encErr)
						}
						if err := tx.Exec(
							"UPDATE config_keys SET aliases_json = ? WHERE id = ?",
							encrypted, row.ID,
						).Error; err != nil {
							return fmt.Errorf("failed to update encrypted aliases for key %d: %w", row.ID, err)
						}
					}
				}
			}

			// Recompute config_hash for keys that had aliases_json populated above,
			// since aliases_json is part of the hash input and these rows now have stale hashes.
			var affectedKeys []tables.TableKey
			if err := tx.Where(
				"aliases_json IS NOT NULL AND aliases_json != ? AND aliases_json != ?", "", "{}",
			).Find(&affectedKeys).Error; err != nil {
				return fmt.Errorf("failed to fetch keys for hash recomputation: %w", err)
			}
			logger.Info("[configstore] %s: processing %d affectedKeys", migrationName, len(affectedKeys))
			for _, key := range affectedKeys {
				schemaKey := schemas.Key{
					Name:               key.Name,
					Value:              key.Value,
					Models:             key.Models,
					BlacklistedModels:  key.BlacklistedModels,
					Weight:             getWeight(key.Weight),
					AzureKeyConfig:     key.AzureKeyConfig,
					VertexKeyConfig:    key.VertexKeyConfig,
					BedrockKeyConfig:   key.BedrockKeyConfig,
					Aliases:            key.Aliases,
					VLLMKeyConfig:      key.VLLMKeyConfig,
					ReplicateKeyConfig: key.ReplicateKeyConfig,
					Enabled:            key.Enabled,
					UseForBatchAPI:     key.UseForBatchAPI,
				}
				hash, err := GenerateKeyHash(schemaKey)
				if err != nil {
					return fmt.Errorf("failed to generate hash for key %s: %w", key.Name, err)
				}
				if err := tx.Model(&key).Update("config_hash", hash).Error; err != nil {
					return fmt.Errorf("failed to update config_hash for key %s: %w", key.Name, err)
				}
				logger.Info("[Migration] Recomputed config_hash for key '%s' after aliases migration", key.Name)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, "aliases_json"); err != nil {
				return err
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running drop deployment columns and add aliases migration: %s", err.Error())
	}
	return nil
}

// migrationAddKeyStatusColumns adds status and description columns to config_keys table
// These columns track the status and description of each individual key
func migrationAddKeyStatusColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_key_status_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			// Add status column
			if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, "status"); err != nil {
				return err
			}

			// Add description column
			if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, "description"); err != nil {
				return err
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			// Drop description column
			if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, "description"); err != nil {
				return err
			}

			// Drop status column
			if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, "status"); err != nil {
				return err
			}

			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running key model discovery status migration: %s", err.Error())
	}
	return nil
}

// migrationAddProviderStatusColumns adds status and description columns to config_providers table
// These columns track the status of model discovery attempts for keyless providers
func migrationAddProviderStatusColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_provider_status_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			// Add status column
			if err := addColumnIfNotExists(tx, logger, &tables.TableProvider{}, "status"); err != nil {
				return err
			}

			// Add description column
			if err := addColumnIfNotExists(tx, logger, &tables.TableProvider{}, "description"); err != nil {
				return err
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			// Drop description column
			if err := dropColumnIfExists(tx, logger, &tables.TableProvider{}, "description"); err != nil {
				return err
			}

			// Drop status column
			if err := dropColumnIfExists(tx, logger, &tables.TableProvider{}, "status"); err != nil {
				return err
			}

			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running provider model discovery status migration: %s", err.Error())
	}
	return nil
}

// migrationAddAsyncJobResultTTLColumn adds async_job_result_ttl column to config_client table
func migrationAddAsyncJobResultTTLColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_async_job_result_ttl_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "AsyncJobResultTTL"); err != nil {
				return fmt.Errorf("failed to add async_job_result_ttl column: %w", err)
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "async_job_result_ttl"); err != nil {
				return fmt.Errorf("failed to drop async_job_result_ttl column: %w", err)
			}

			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running async_job_result_ttl migration: %s", err.Error())
	}
	return nil
}

// migrationAddRateLimitToTeamsAndCustomers adds rate_limit_id column to governance_teams and governance_customers tables
func migrationAddRateLimitToTeamsAndCustomers(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_rate_limit_to_teams_and_customers"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			// Add rate_limit_id to governance_teams table
			if err := addColumnIfNotExists(tx, logger, &tables.TableTeam{}, "rate_limit_id"); err != nil {
				return fmt.Errorf("failed to add rate_limit_id column to teams: %w", err)
			}

			// Add rate_limit_id to governance_customers table
			if err := addColumnIfNotExists(tx, logger, &tables.TableCustomer{}, "rate_limit_id"); err != nil {
				return fmt.Errorf("failed to add rate_limit_id column to customers: %w", err)
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := dropColumnIfExists(tx, logger, &tables.TableTeam{}, "rate_limit_id"); err != nil {
				return fmt.Errorf("failed to drop rate_limit_id column from teams: %w", err)
			}

			if err := dropColumnIfExists(tx, logger, &tables.TableCustomer{}, "rate_limit_id"); err != nil {
				return fmt.Errorf("failed to drop rate_limit_id column from customers: %w", err)
			}

			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running rate limit migration for teams and customers: %s", err.Error())
	}
	return nil
}

// migrationBackfillEmptyVirtualKeyConfigs backfills existing virtual keys that have
// empty ProviderConfigs or MCPConfigs with all available providers/MCP clients.
// This preserves the previous "empty means all" behavior for existing VKs after
// the semantic change to "empty means none" (deny-by-default).
func migrationBackfillEmptyVirtualKeyConfigs(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "backfill_empty_virtual_key_configs"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			// Step 1: Backfill ProviderConfigs for VKs that have none
			// Find all virtual keys
			var allVKs []tables.TableVirtualKey
			if err := tx.Find(&allVKs).Error; err != nil {
				return fmt.Errorf("failed to query virtual keys: %w", err)
			}

			// Get all available providers
			var allProviders []tables.TableProvider
			if err := tx.Find(&allProviders).Error; err != nil {
				return fmt.Errorf("failed to query providers: %w", err)
			}

			// Track which VK IDs were modified so we can recompute their config_hash
			modifiedVKIDs := make(map[string]struct{})

			logger.Info("[configstore] %s: processing %d allVKs", migrationName, len(allVKs))
			for _, vk := range allVKs {
				// Check if this VK has any provider configs
				var providerConfigCount int64
				if err := tx.Model(&tables.TableVirtualKeyProviderConfig{}).Where("virtual_key_id = ?", vk.ID).Count(&providerConfigCount).Error; err != nil {
					return fmt.Errorf("failed to count provider configs for VK %s: %w", vk.ID, err)
				}

				if providerConfigCount == 0 && len(allProviders) > 0 {
					// VK has no provider configs - backfill with all available providers
					logger.Info("[configstore] %s: processing %d allProviders", migrationName, len(allProviders))
					for _, provider := range allProviders {
						providerConfig := tables.TableVirtualKeyProviderConfig{
							VirtualKeyID:  vk.ID,
							Provider:      provider.Name,
							Weight:        bifrost.Ptr(1.0),
							AllowedModels: []string{},
							AllowAllKeys:  true,
						}
						if err := tx.Create(&providerConfig).Error; err != nil {
							return fmt.Errorf("failed to create provider config for VK %s, provider %s: %w", vk.ID, provider.Name, err)
						}
					}
					modifiedVKIDs[vk.ID] = struct{}{}
					logger.Info("[Migration] Backfilled VK '%s' with %d provider configs", vk.Name, len(allProviders))
				}
			}

			// Step 2: Backfill MCPConfigs for VKs that have none
			// Get all available MCP clients
			var allMCPClients []tables.TableMCPClient
			if err := tx.Find(&allMCPClients).Error; err != nil {
				return fmt.Errorf("failed to query MCP clients: %w", err)
			}

			logger.Info("[configstore] %s: processing %d allVKs", migrationName, len(allVKs))
			for _, vk := range allVKs {
				// Check if this VK has any MCP configs
				var mcpConfigCount int64
				if err := tx.Model(&tables.TableVirtualKeyMCPConfig{}).Where("virtual_key_id = ?", vk.ID).Count(&mcpConfigCount).Error; err != nil {
					return fmt.Errorf("failed to count MCP configs for VK %s: %w", vk.ID, err)
				}

				if mcpConfigCount == 0 && len(allMCPClients) > 0 {
					// VK has no MCP configs - backfill with all available MCP clients with wildcard
					logger.Info("[configstore] %s: processing %d allMCPClients", migrationName, len(allMCPClients))
					for _, mcpClient := range allMCPClients {
						mcpConfig := tables.TableVirtualKeyMCPConfig{
							VirtualKeyID:   vk.ID,
							MCPClientID:    mcpClient.ID,
							ToolsToExecute: []string{"*"},
						}
						if err := tx.Create(&mcpConfig).Error; err != nil {
							return fmt.Errorf("failed to create MCP config for VK %s, client %d: %w", vk.ID, mcpClient.ID, err)
						}
					}
					modifiedVKIDs[vk.ID] = struct{}{}
					logger.Info("[Migration] Backfilled VK '%s' with %d MCP client configs", vk.Name, len(allMCPClients))
				}
			}

			// Step 3: Recompute and persist config_hash for every VK that was modified.
			// Without this, subsequent config-sync diff logic would see a stale hash and
			// attempt to re-reconcile the VK (potentially undoing the backfill).
			logger.Info("[configstore] %s: processing %d modifiedVKIDs", migrationName, len(modifiedVKIDs))
			for vkID := range modifiedVKIDs {
				var vk tables.TableVirtualKey
				if err := tx.
					Preload("ProviderConfigs").
					Preload("ProviderConfigs.Keys").
					Preload("MCPConfigs").
					First(&vk, "id = ?", vkID).Error; err != nil {
					return fmt.Errorf("failed to reload VK %s for hash recomputation: %w", vkID, err)
				}
				newHash, err := GenerateVirtualKeyHash(vk)
				if err != nil {
					return fmt.Errorf("failed to generate hash for VK %s: %w", vkID, err)
				}
				if err := tx.Model(&tables.TableVirtualKey{}).
					Where("id = ?", vkID).
					Update("config_hash", newHash).Error; err != nil {
					return fmt.Errorf("failed to update config_hash for VK %s: %w", vkID, err)
				}
				logger.Info("[Migration] Recomputed config_hash for VK '%s'", vk.Name)
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			// No rollback needed - the backfilled configs are valid data
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running backfill empty virtual key configs migration: %s", err.Error())
	}
	return nil
}

// migrationAddRequiredHeadersJSONColumn adds the required_headers_json column to the config_client table
func migrationAddRequiredHeadersJSONColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_required_headers_json_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "RequiredHeadersJSON"); err != nil {
				return fmt.Errorf("failed to add required_headers_json column: %w", err)
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "required_headers_json"); err != nil {
				return fmt.Errorf("failed to drop required_headers_json column: %w", err)
			}

			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running required_headers_json migration: %s", err.Error())
	}
	return nil
}

// migrationAddOutputCostPerVideoPerSecond adds output_cost_per_video_per_second column to governance_model_pricing table
func migrationAddOutputCostPerVideoPerSecond(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_output_cost_per_video_per_second_and_output_cost_per_second_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := addColumnIfNotExists(tx, logger, &tables.TableModelPricing{}, "output_cost_per_video_per_second"); err != nil {
				return fmt.Errorf("failed to add output_cost_per_video_per_second column: %w", err)
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TableModelPricing{}, "output_cost_per_second"); err != nil {
				return fmt.Errorf("failed to add output_cost_per_second column: %w", err)
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := dropColumnIfExists(tx, logger, &tables.TableModelPricing{}, "output_cost_per_video_per_second"); err != nil {
				return fmt.Errorf("failed to drop output_cost_per_video_per_second column: %w", err)
			}

			if err := dropColumnIfExists(tx, logger, &tables.TableModelPricing{}, "output_cost_per_second"); err != nil {
				return fmt.Errorf("failed to drop output_cost_per_second column: %w", err)
			}

			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running output_cost_per_video_per_second migration: %s", err.Error())
	}
	return nil
}

// migrationAddLoggingHeadersJSONColumn adds the logging_headers_json column to the config_client table
func migrationAddLoggingHeadersJSONColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_logging_headers_json_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "LoggingHeadersJSON"); err != nil {
				return fmt.Errorf("failed to add logging_headers_json column: %w", err)
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "logging_headers_json"); err != nil {
				return fmt.Errorf("failed to drop logging_headers_json column: %w", err)
			}

			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running logging_headers_json migration: %s", err.Error())
	}
	return nil
}

// migrationAddHideDeletedVirtualKeysInFiltersColumn adds the hide_deleted_virtual_keys_in_filters column to config_client.
func migrationAddHideDeletedVirtualKeysInFiltersColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_hide_deleted_virtual_keys_in_filters_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "HideDeletedVirtualKeysInFilters"); err != nil {
				return fmt.Errorf("failed to add hide_deleted_virtual_keys_in_filters column: %w", err)
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "hide_deleted_virtual_keys_in_filters"); err != nil {
				return fmt.Errorf("failed to drop hide_deleted_virtual_keys_in_filters column: %w", err)
			}

			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running hide_deleted_virtual_keys_in_filters migration: %s", err.Error())
	}
	return nil
}

// migrationAddEnforceSCIMAuthColumn adds the enforce_scim_auth column to the client config table
func migrationAddEnforceSCIMAuthColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_enforce_scim_auth_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "enforce_scim_auth"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "enforce_scim_auth"); err != nil {
				return err
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running enforce SCIM auth column migration: %s", err.Error())
	}
	return nil
}

// migrationAddEnforceAuthOnInferenceColumn adds the enforce_auth_on_inference column to the config_client table
func migrationAddEnforceAuthOnInferenceColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_enforce_auth_on_inference_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "enforce_auth_on_inference"); err != nil {
				return err
			}
			// Populate from old fields: set to true if either old flag was true
			if err := tx.Exec("UPDATE config_client SET enforce_auth_on_inference = true WHERE enforce_governance_header = true OR enforce_scim_auth = true").Error; err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "enforce_auth_on_inference"); err != nil {
				return err
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running enforce auth on inference column migration: %s", err.Error())
	}
	return nil
}

// migrationAddDualCredentialConflictBehaviorColumn adds the dual_credential_conflict_behavior
// column to the config_client table. The column is added with its gorm-defined
// NOT NULL default ('prefer_idp'), so existing rows retain the pre-feature behavior.
func migrationAddDualCredentialConflictBehaviorColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_dual_credential_conflict_behavior_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "dual_credential_conflict_behavior"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "dual_credential_conflict_behavior"); err != nil {
				return err
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running dual credential conflict behavior column migration: %s", err.Error())
	}
	return nil
}

func migrationReconcilePricingOverridesTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "reconcile_pricing_overrides_table"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mgr := tx.Migrator()

			if !mgr.HasTable(&tables.TablePricingOverride{}) {
				logger.Info("[configstore] %s: creating table TablePricingOverride", migrationName)
				if err := mgr.CreateTable(&tables.TablePricingOverride{}); err != nil {
					return fmt.Errorf("failed to create governance_pricing_overrides table: %w", err)
				}
				return nil
			}
			logger.Info("[configstore] %s: auto-migrating TablePricingOverride", migrationName)
			if err := tx.AutoMigrate(&tables.TablePricingOverride{}); err != nil {
				return fmt.Errorf("failed to automigrate governance_pricing_overrides table: %w", err)
			}
			for _, indexName := range []string{"idx_pricing_override_scope", "idx_pricing_override_match"} {
				if mgr.HasIndex(&tables.TablePricingOverride{}, indexName) {
					continue
				}
				logger.Info("[configstore] %s: creating index %s on TablePricingOverride", migrationName, indexName)
				if err := mgr.CreateIndex(&tables.TablePricingOverride{}, indexName); err != nil {
					return fmt.Errorf("failed to create pricing override index %s: %w", indexName, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mgr := tx.Migrator()
			if mgr.HasTable(&tables.TablePricingOverride{}) {
				logger.Info("[configstore] %s: dropping table TablePricingOverride", migrationName)
				if err := mgr.DropTable(&tables.TablePricingOverride{}); err != nil {
					return fmt.Errorf("failed to drop governance_pricing_overrides table: %w", err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running pricing overrides table reconcile migration: %s", err.Error())
	}
	return nil
}

// migrationAddEncryptionColumns adds the encryption_status column to the config_keys, governance_virtual_keys, sessions, oauth_configs, oauth_tokens, config_mcp_clients, config_providers, config_vector_store, and config_plugins tables
func migrationAddEncryptionColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_encryption_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			type encryptionTable struct {
				table   interface{}
				columns []string
			}

			targets := []encryptionTable{
				{&tables.TableKey{}, []string{"encryption_status"}},
				{&tables.TableVirtualKey{}, []string{"encryption_status", "value_hash"}},
				{&tables.SessionsTable{}, []string{"encryption_status", "token_hash"}},
				{&tables.TableOauthConfig{}, []string{"encryption_status"}},
				{&tables.TableOauthToken{}, []string{"encryption_status"}},
				{&tables.TableMCPClient{}, []string{"encryption_status"}},
				{&tables.TableProvider{}, []string{"encryption_status"}},
				{&tables.TableVectorStoreConfig{}, []string{"encryption_status"}},
				{&tables.TablePlugin{}, []string{"encryption_status"}},
			}

			for _, t := range targets {
				for _, col := range t.columns {
					if err := addColumnIfNotExists(tx, logger, t.table, col); err != nil {
						return fmt.Errorf("failed to add column %s: %w", col, err)
					}
				}
			}

			// Backfill encryption_status for all tables that have the column
			backfillTables := []string{
				"config_keys",
				"governance_virtual_keys",
				"sessions",
				"oauth_configs",
				"oauth_tokens",
				"config_mcp_clients",
				"config_providers",
				"config_vector_store",
				"config_plugins",
			}
			logger.Info("[configstore] %s: processing %d backfillTables", migrationName, len(backfillTables))
			for _, table := range backfillTables {
				if err := tx.Exec(fmt.Sprintf(
					"UPDATE %s SET encryption_status = 'plain_text' WHERE encryption_status IS NULL OR encryption_status = ''",
					table,
				)).Error; err != nil {
					return fmt.Errorf("failed to backfill encryption_status in %s: %w", table, err)
				}
			}

			// Backfill value_hash for existing virtual keys
			// Use NULL instead of '' to avoid unique constraint violations
			// (multiple rows with '' would violate the unique index, but NULLs are excluded)
			if err := tx.Exec(`
				UPDATE governance_virtual_keys
				SET value_hash = NULL
				WHERE value_hash IS NULL OR value_hash = ''
			`).Error; err != nil {
				return fmt.Errorf("failed to initialize value_hash: %w", err)
			}

			// Backfill token_hash for existing sessions
			// Use NULL instead of '' to avoid unique constraint violations
			if err := tx.Exec(`
				UPDATE sessions
				SET token_hash = NULL
				WHERE token_hash IS NULL OR token_hash = ''
			`).Error; err != nil {
				return fmt.Errorf("failed to initialize token_hash: %w", err)
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			type dropInfo struct {
				table   interface{}
				columns []string
			}

			drops := []dropInfo{
				{&tables.TableKey{}, []string{"encryption_status"}},
				{&tables.TableVirtualKey{}, []string{"encryption_status", "value_hash"}},
				{&tables.SessionsTable{}, []string{"encryption_status", "token_hash"}},
				{&tables.TableOauthConfig{}, []string{"encryption_status"}},
				{&tables.TableOauthToken{}, []string{"encryption_status"}},
				{&tables.TableMCPClient{}, []string{"encryption_status"}},
				{&tables.TableProvider{}, []string{"encryption_status"}},
				{&tables.TableVectorStoreConfig{}, []string{"encryption_status"}},
				{&tables.TablePlugin{}, []string{"encryption_status"}},
			}

			for _, d := range drops {
				for _, col := range d.columns {
					if err := dropColumnIfExists(tx, logger, d.table, col); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running encryption columns migration: %s", err.Error())
	}
	return nil
}

// migrationDropEnableGovernanceColumn drops the enable_governance column from the config_client table
func migrationDropEnableGovernanceColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "drop_enable_governance_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "enable_governance"); err != nil {
				return fmt.Errorf("failed to drop enable_governance column: %w", err)
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running drop enable governance column rollback: %s", err.Error())
	}
	return nil
}

// migrationAddVLLMKeyConfigColumns adds vllm_url and vllm_model_name columns to the key table
func migrationAddVLLMKeyConfigColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_vllm_key_config_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, "vllm_url"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, "vllm_model_name"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, "vllm_url"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, "vllm_model_name"); err != nil {
				return err
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running vllm key config columns migration: %s", err.Error())
	}
	return nil
}

// migrationWidenEncryptedVarcharColumns widens varchar columns that store AES-256-GCM
// encrypted values to TEXT. Encryption adds ~28 bytes of overhead plus base64 expansion (4/3x),
// so a varchar(255) can only hold ~153-char plaintext. Using TEXT removes any size constraints.
// SQLite does not enforce varchar(n) size constraints, so no migration is needed there.
func migrationWidenEncryptedVarcharColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "widen_encrypted_varchar_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if tx.Dialector.Name() != "postgres" {
				return nil
			}
			// azure_api_version was removed in v1 API migration; only widen it if it
			// still exists (existing DBs that haven't run the drop migration yet).
			if tx.Migrator().HasColumn(&tables.TableKey{}, "azure_api_version") {
				if err := tx.Exec("ALTER TABLE config_keys ALTER COLUMN azure_api_version TYPE TEXT").Error; err != nil {
					return fmt.Errorf("failed to widen column azure_api_version: %w", err)
				}
			}

			stmts := []string{
				// config_keys table - all encrypted SecretVar fields
				"ALTER TABLE config_keys ALTER COLUMN azure_client_id TYPE TEXT",
				"ALTER TABLE config_keys ALTER COLUMN azure_tenant_id TYPE TEXT",
				"ALTER TABLE config_keys ALTER COLUMN vertex_project_id TYPE TEXT",
				"ALTER TABLE config_keys ALTER COLUMN vertex_project_number TYPE TEXT",
				"ALTER TABLE config_keys ALTER COLUMN vertex_region TYPE TEXT",
				"ALTER TABLE config_keys ALTER COLUMN bedrock_access_key TYPE TEXT",
				"ALTER TABLE config_keys ALTER COLUMN bedrock_region TYPE TEXT",
				// sessions table
				"ALTER TABLE sessions ALTER COLUMN token TYPE TEXT",
				// governance_virtual_keys table
				"ALTER TABLE governance_virtual_keys ALTER COLUMN value TYPE TEXT",
				// oauth_configs table
				"ALTER TABLE oauth_configs ALTER COLUMN code_verifier TYPE TEXT",
			}
			logger.Info("[configstore] %s: processing %d stmts", migrationName, len(stmts))
			for _, stmt := range stmts {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("failed to widen column (%s): %w", stmt, err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running widen encrypted varchar columns migration: %s", err.Error())
	}
	return nil
}

// migrationAddBedrockAssumeRoleColumns adds bedrock_role_arn, bedrock_external_id, and bedrock_role_session_name
// columns to the config_keys table for STS AssumeRole support in Bedrock keys.
func migrationAddBedrockAssumeRoleColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_bedrock_assume_role_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, "bedrock_role_arn"); err != nil {
				return fmt.Errorf("failed to add bedrock_role_arn column: %w", err)
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, "bedrock_external_id"); err != nil {
				return fmt.Errorf("failed to add bedrock_external_id column: %w", err)
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, "bedrock_role_session_name"); err != nil {
				return fmt.Errorf("failed to add bedrock_role_session_name column: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, "bedrock_role_arn"); err != nil {
				return fmt.Errorf("failed to drop bedrock_role_arn column: %w", err)
			}
			if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, "bedrock_external_id"); err != nil {
				return fmt.Errorf("failed to drop bedrock_external_id column: %w", err)
			}
			if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, "bedrock_role_session_name"); err != nil {
				return fmt.Errorf("failed to drop bedrock_role_session_name column: %w", err)
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running bedrock assume role columns migration: %s", err.Error())
	}
	return nil
}

// migrationAddAllowAllKeysToProviderConfig adds the allow_all_keys column to the provider config table
// and backfills existing rows: any provider config with no keys in the join table previously meant
// "allow all keys" (old semantic), so they get allow_all_keys = true to preserve behaviour.
func migrationAddAllowAllKeysToProviderConfig(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_allow_all_keys_to_provider_config"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	// opts is a value copy: migrator.DefaultOptions is a shared global pointer,
	// so mutating it in place would disable transactions for other migrations.
	opts := *migrator.DefaultOptions
	opts.UseTransaction = false
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			// Add the column if it doesn't exist
			if err := addColumnIfNotExists(tx, logger, &tables.TableVirtualKeyProviderConfig{}, "allow_all_keys"); err != nil {
				return fmt.Errorf("failed to add allow_all_keys column: %w", err)
			}

			// Backfill: find all provider configs that have no keys in the join table.
			// These previously meant "allow all keys", so set allow_all_keys = true.
			var allConfigs []tables.TableVirtualKeyProviderConfig
			if err := tx.Find(&allConfigs).Error; err != nil {
				return fmt.Errorf("failed to query provider configs: %w", err)
			}

			// Track which VK IDs were modified so we can recompute their config_hash.
			// Without this, subsequent config-sync diff logic would see a stale hash
			// and attempt to re-reconcile the VK (potentially undoing the backfill).
			modifiedVKIDs := make(map[string]struct{})

			logger.Info("[configstore] %s: processing %d allConfigs", migrationName, len(allConfigs))
			for _, pc := range allConfigs {
				var keyCount int64
				if err := tx.Table("governance_virtual_key_provider_config_keys").
					Where("table_virtual_key_provider_config_id = ?", pc.ID).
					Count(&keyCount).Error; err != nil {
					return fmt.Errorf("failed to count keys for provider config %d: %w", pc.ID, err)
				}

				if keyCount == 0 {
					if err := tx.Model(&tables.TableVirtualKeyProviderConfig{}).
						Where("id = ?", pc.ID).
						Update("allow_all_keys", true).Error; err != nil {
						return fmt.Errorf("failed to backfill allow_all_keys for provider config %d: %w", pc.ID, err)
					}
					modifiedVKIDs[pc.VirtualKeyID] = struct{}{}
				}
			}

			// Recompute and persist config_hash for every VK that was modified.
			logger.Info("[configstore] %s: processing %d modifiedVKIDs", migrationName, len(modifiedVKIDs))
			for vkID := range modifiedVKIDs {
				var vk tables.TableVirtualKey
				if err := tx.
					Preload("ProviderConfigs").
					Preload("ProviderConfigs.Keys").
					Preload("MCPConfigs").
					First(&vk, "id = ?", vkID).Error; err != nil {
					return fmt.Errorf("failed to reload VK %s for hash recomputation: %w", vkID, err)
				}
				newHash, err := GenerateVirtualKeyHash(vk)
				if err != nil {
					return fmt.Errorf("failed to generate hash for VK %s: %w", vkID, err)
				}
				if err := tx.Model(&tables.TableVirtualKey{}).
					Where("id = ?", vkID).
					Update("config_hash", newHash).Error; err != nil {
					return fmt.Errorf("failed to update config_hash for VK %s: %w", vkID, err)
				}
				logger.Info("[Migration] Recomputed config_hash for VK '%s'", vk.Name)
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableVirtualKeyProviderConfig{}, "allow_all_keys"); err != nil {
				return fmt.Errorf("failed to drop allow_all_keys column: %w", err)
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running allow_all_keys migration: %s", err.Error())
	}
	return nil
}

// migrationAddMCPDisableAutoToolInjectColumn adds the mcp_disable_auto_tool_inject column to the client config table.
// When true, MCP tools are not automatically injected into requests; only explicit context filters apply.
func migrationAddMCPDisableAutoToolInjectColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_mcp_disable_auto_tool_inject_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "mcp_disable_auto_tool_inject"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "mcp_disable_auto_tool_inject"); err != nil {
				return err
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running mcp disable auto tool inject migration: %s", err.Error())
	}
	return nil
}

// migrationAddMCPEnableTempTokenAuthColumn adds the mcp_enable_temp_token_auth column to the client config table.
func migrationAddMCPEnableTempTokenAuthColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_mcp_enable_temp_token_auth_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "mcp_enable_temp_token_auth"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "mcp_enable_temp_token_auth"); err != nil {
				return err
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running mcp enable temp token auth migration: %s", err.Error())
	}
	return nil
}

// migrationAddPricingRefactorColumns adds all new pricing columns introduced in the pricing module refactor
func migrationAddPricingRefactorColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_pricing_refactor_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			columns := []string{
				"input_cost_per_token_priority",
				"output_cost_per_token_priority",
				"cache_creation_input_token_cost_above_1hr",
				"cache_creation_input_token_cost_above_1hr_above_200k_tokens",
				"cache_creation_input_audio_token_cost",
				"cache_read_input_token_cost_priority",
				"input_cost_per_pixel",
				"output_cost_per_pixel",
				"output_cost_per_image_premium_image",
				"output_cost_per_image_above_512_and_512_pixels",
				"output_cost_per_image_above_512x512_pixels_premium",
				"output_cost_per_image_above_1024_and_1024_pixels",
				"output_cost_per_image_above_1024x1024_pixels_premium",
				"input_cost_per_audio_token",
				"input_cost_per_second",
				"input_cost_per_video_per_second",
				"input_cost_per_audio_per_second",
				"output_cost_per_audio_token",
				"search_context_cost_per_query",
				"code_interpreter_cost_per_session",
				"input_cost_per_character",
				"input_cost_per_token_above_128k_tokens",
				"input_cost_per_image_above_128k_tokens",
				"input_cost_per_video_per_second_above_128k_tokens",
				"input_cost_per_audio_per_second_above_128k_tokens",
				"output_cost_per_token_above_128k_tokens",
			}

			for _, field := range columns {
				if err := addColumnIfNotExists(tx, logger, &tables.TableModelPricing{}, field); err != nil {
					return fmt.Errorf("failed to add column %s: %w", field, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			columns := []string{
				"input_cost_per_token_priority",
				"output_cost_per_token_priority",
				"cache_creation_input_token_cost_above_1hr",
				"cache_creation_input_token_cost_above_1hr_above_200k_tokens",
				"cache_creation_input_audio_token_cost",
				"cache_read_input_token_cost_priority",
				"input_cost_per_pixel",
				"output_cost_per_pixel",
				"output_cost_per_image_premium_image",
				"output_cost_per_image_above_512_and_512_pixels",
				"output_cost_per_image_above_512x512_pixels_premium",
				"output_cost_per_image_above_1024_and_1024_pixels",
				"output_cost_per_image_above_1024x1024_pixels_premium",
				"input_cost_per_audio_token",
				"input_cost_per_second",
				"input_cost_per_video_per_second",
				"input_cost_per_audio_per_second",
				"output_cost_per_audio_token",
				"search_context_cost_per_query",
				"code_interpreter_cost_per_session",
				"input_cost_per_character",
				"input_cost_per_token_above_128k_tokens",
				"input_cost_per_image_above_128k_tokens",
				"input_cost_per_video_per_second_above_128k_tokens",
				"input_cost_per_audio_per_second_above_128k_tokens",
				"output_cost_per_token_above_128k_tokens",
			}

			for _, field := range columns {
				if err := dropColumnIfExists(tx, logger, &tables.TableModelPricing{}, field); err != nil {
					return fmt.Errorf("failed to drop column %s: %w", field, err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running pricing refactor columns migration: %s", err.Error())
	}
	return nil
}

// migrationRenameTruncatedPricingColumn renames the output_cost_per_image_above_512_and_512_pixels_and_premium_image
// column which at 64 chars exceeds PostgreSQL's 63-character identifier limit. PostgreSQL silently truncated
// it to output_cost_per_image_above_512_and_512_pixels_and_premium_imag (63 chars), while SQLite kept the
// full 64-char name. This migration renames whichever variant exists to the shorter canonical name.
func migrationRenameTruncatedPricingColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "rename_truncated_pricing_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()

			const newName = "output_cost_per_image_above_512x512_pixels_premium"
			if mg.HasColumn(&tables.TableModelPricing{}, newName) {
				return nil
			}

			// PostgreSQL truncated the 64-char name to 63 chars
			const oldNamePG = "output_cost_per_image_above_512_and_512_pixels_and_premium_imag"
			// SQLite kept the full 64-char name
			const oldNameSQLite = "output_cost_per_image_above_512_and_512_pixels_and_premium_image"

			if mg.HasColumn(&tables.TableModelPricing{}, oldNamePG) {
				if err := tx.Exec("ALTER TABLE governance_model_pricing RENAME COLUMN " + oldNamePG + " TO " + newName).Error; err != nil {
					return fmt.Errorf("failed to rename column %s to %s: %w", oldNamePG, newName, err)
				}
			} else if mg.HasColumn(&tables.TableModelPricing{}, oldNameSQLite) {
				if err := tx.Exec("ALTER TABLE governance_model_pricing RENAME COLUMN " + oldNameSQLite + " TO " + newName).Error; err != nil {
					return fmt.Errorf("failed to rename column %s to %s: %w", oldNameSQLite, newName, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running rename_truncated_pricing_column migration: %s", err.Error())
	}
	return nil
}

// migrationAddImageQualityPricingColumns adds quality-based per-image cost columns (low, medium, high, auto).
func migrationAddImageQualityPricingColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_image_quality_pricing_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			columns := []string{
				"output_cost_per_image_above_2048_and_2048_pixels",
				"output_cost_per_image_above_4096_and_4096_pixels",
				"output_cost_per_image_low_quality",
				"output_cost_per_image_medium_quality",
				"output_cost_per_image_high_quality",
				"output_cost_per_image_auto_quality",
			}
			for _, field := range columns {
				if err := addColumnIfNotExists(tx, logger, &tables.TableModelPricing{}, field); err != nil {
					return fmt.Errorf("failed to add column %s: %w", field, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			columns := []string{
				"output_cost_per_image_above_2048_and_2048_pixels",
				"output_cost_per_image_above_4096_and_4096_pixels",
				"output_cost_per_image_low_quality",
				"output_cost_per_image_medium_quality",
				"output_cost_per_image_high_quality",
				"output_cost_per_image_auto_quality",
			}
			for _, field := range columns {
				if err := dropColumnIfExists(tx, logger, &tables.TableModelPricing{}, field); err != nil {
					return fmt.Errorf("failed to drop column %s: %w", field, err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running image quality pricing columns migration: %s", err.Error())
	}
	return nil
}

// legacyRoutingRuleColumns is a migration-only struct that represents the old routing_rules
// schema before provider/model/key_id were moved to the routing_targets table.
// GORM's SQLite DropColumn/AddColumn need a real struct (not a string table name) to
// reconstruct the table correctly, so we keep this stub around for migration use only.
type legacyRoutingRuleColumns struct {
	Provider string `gorm:"column:provider;type:varchar(255)"`
	Model    string `gorm:"column:model;type:varchar(255)"`
}

func (legacyRoutingRuleColumns) TableName() string { return "routing_rules" }

// migrationAddRoutingTargetsTable creates the routing_targets table and seeds one target row per
// existing routing rule, migrating the legacy provider/model columns.
// After seeding, the legacy columns are dropped from routing_rules.
func migrationAddRoutingTargetsTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_routing_targets_table"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()

			// 1. Create routing_targets table
			if !mg.HasTable(&tables.TableRoutingTarget{}) {
				logger.Info("[configstore] %s: creating table TableRoutingTarget", migrationName)
				if err := mg.CreateTable(&tables.TableRoutingTarget{}); err != nil {
					return fmt.Errorf("failed to create routing_targets table: %w", err)
				}
			}
			if !mg.HasConstraint(&tables.TableRoutingRule{}, "Targets") {
				if err := mg.CreateConstraint(&tables.TableRoutingRule{}, "Targets"); err != nil {
					return fmt.Errorf("failed to create routing_targets foreign key: %w", err)
				}
			}

			// 2. Read legacy data BEFORE dropping columns, then drop columns, then seed.
			// Order matters: DropColumn on SQLite recreates the routing_rules table, which
			// triggers the OnDelete:CASCADE on routing_targets and deletes any rows inserted
			// before the drop. So we read first, drop, then insert.
			type legacyRule struct {
				ID       string
				Provider string
				Model    string
			}
			var legacyRows []legacyRule
			if mg.HasColumn("routing_rules", "provider") {
				if err := tx.Table("routing_rules").Select("id, provider, model").Scan(&legacyRows).Error; err != nil {
					return fmt.Errorf("failed to scan routing_rules for seeding: %w", err)
				}
			}

			// 3. Drop legacy single-target columns from routing_rules.
			// Must use the struct form (not string) so SQLite can reconstruct the table correctly.
			// Do this BEFORE seeding so the CASCADE triggered by table recreation hits an empty
			// routing_targets table (nothing to delete yet).
			legacyModel := &legacyRoutingRuleColumns{}
			for _, col := range []string{"provider", "model"} {
				if err := dropColumnIfExists(tx, logger, legacyModel, col); err != nil {
					return fmt.Errorf("failed to drop column %s from routing_rules: %w", col, err)
				}
			}

			// 4. Seed routing_targets from the legacy data read above (idempotent).
			logger.Info("[configstore] %s: processing %d legacyRows", migrationName, len(legacyRows))
			for _, row := range legacyRows {
				var count int64
				if err := tx.Table("routing_targets").Where("rule_id = ?", row.ID).Count(&count).Error; err != nil {
					return fmt.Errorf("failed to count targets for rule %s: %w", row.ID, err)
				}
				if count > 0 {
					continue // already seeded
				}
				target := tables.TableRoutingTarget{
					RuleID: row.ID,
					Weight: 1.0,
				}
				if row.Provider != "" {
					p := row.Provider
					target.Provider = &p
				}
				if row.Model != "" {
					m := row.Model
					target.Model = &m
				}
				if err := tx.Create(&target).Error; err != nil {
					return fmt.Errorf("failed to seed target for rule %s: %w", row.ID, err)
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()

			if !mg.HasTable(&tables.TableRoutingTarget{}) {
				return nil
			}

			// 1. Add provider and model columns back to routing_rules (before dropping targets)
			legacyModel := &legacyRoutingRuleColumns{}
			for _, col := range []string{"provider", "model"} {
				if err := addColumnIfNotExists(tx, logger, legacyModel, col); err != nil {
					return fmt.Errorf("failed to add column %s to routing_rules: %w", col, err)
				}
			}

			// 2. Backfill provider/model from routing_targets into routing_rules (join by rule_id)
			type targetRow struct {
				RuleID   string
				Provider *string
				Model    *string
			}
			var targets []targetRow
			if err := tx.Table("routing_targets").Select("rule_id, provider, model").Order("rule_id").Scan(&targets).Error; err != nil {
				return fmt.Errorf("failed to scan routing_targets for backfill: %w", err)
			}
			ruleData := make(map[string]targetRow)
			for _, t := range targets {
				if _, ok := ruleData[t.RuleID]; !ok {
					ruleData[t.RuleID] = t
				}
			}
			logger.Info("[configstore] %s: processing %d ruleData", migrationName, len(ruleData))
			for ruleID, t := range ruleData {
				provider, model := "", ""
				if t.Provider != nil {
					provider = *t.Provider
				}
				if t.Model != nil {
					model = *t.Model
				}
				if err := tx.Table("routing_rules").Where("id = ?", ruleID).Updates(map[string]interface{}{
					"provider": provider,
					"model":    model,
				}).Error; err != nil {
					return fmt.Errorf("failed to backfill routing_rule %s: %w", ruleID, err)
				}
			}

			// 3. Drop routing_targets table
			if mg.HasConstraint(&tables.TableRoutingRule{}, "Targets") {
				if err := mg.DropConstraint(&tables.TableRoutingRule{}, "Targets"); err != nil {
					return fmt.Errorf("failed to drop routing_targets foreign key: %w", err)
				}
			}
			logger.Info("[configstore] %s: dropping table TableRoutingTarget", migrationName)
			if err := mg.DropTable(&tables.TableRoutingTarget{}); err != nil {
				return fmt.Errorf("failed to drop routing_targets table: %w", err)
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running routing_targets_table migration: %s", err.Error())
	}
	return nil
}

// migrationAddPromptRepoTables adds the prompt repository tables (folders, prompts, versions, sessions)
func migrationAddPromptRepoTables(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_prompt_repo_tables"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_prompt_repo_tables",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			// Create folders table
			if !migrator.HasTable(&tables.TableFolder{}) {
				logger.Info("[configstore] %s: creating table TableFolder", migrationName)
				if err := migrator.CreateTable(&tables.TableFolder{}); err != nil {
					return err
				}
			}

			// Create prompts table
			if !migrator.HasTable(&tables.TablePrompt{}) {
				logger.Info("[configstore] %s: creating table TablePrompt", migrationName)
				if err := migrator.CreateTable(&tables.TablePrompt{}); err != nil {
					return err
				}
			}

			// Create prompt_versions table
			if !migrator.HasTable(&tables.TablePromptVersion{}) {
				logger.Info("[configstore] %s: creating table TablePromptVersion", migrationName)
				if err := migrator.CreateTable(&tables.TablePromptVersion{}); err != nil {
					return err
				}
			}

			// Create prompt_version_messages table
			if !migrator.HasTable(&tables.TablePromptVersionMessage{}) {
				logger.Info("[configstore] %s: creating table TablePromptVersionMessage", migrationName)
				if err := migrator.CreateTable(&tables.TablePromptVersionMessage{}); err != nil {
					return err
				}
			}

			// Create prompt_sessions table
			if !migrator.HasTable(&tables.TablePromptSession{}) {
				logger.Info("[configstore] %s: creating table TablePromptSession", migrationName)
				if err := migrator.CreateTable(&tables.TablePromptSession{}); err != nil {
					return err
				}
			}

			// Create prompt_session_messages table
			if !migrator.HasTable(&tables.TablePromptSessionMessage{}) {
				logger.Info("[configstore] %s: creating table TablePromptSessionMessage", migrationName)
				if err := migrator.CreateTable(&tables.TablePromptSessionMessage{}); err != nil {
					return err
				}
			}

			// Apply schema updates (indexes, constraints) to existing tables
			logger.Info("[configstore] %s: auto-migrating TablePromptVersion and TablePromptSession", migrationName)
			if err := tx.AutoMigrate(
				&tables.TablePromptVersion{},
				&tables.TablePromptSession{},
			); err != nil {
				return err
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			// Drop tables in reverse order (respecting foreign key constraints)
			logger.Info("[configstore] %s: dropping table TablePromptSessionMessage", migrationName)
			if err := migrator.DropTable(&tables.TablePromptSessionMessage{}); err != nil {
				return err
			}
			logger.Info("[configstore] %s: dropping table TablePromptSession", migrationName)
			if err := migrator.DropTable(&tables.TablePromptSession{}); err != nil {
				return err
			}
			logger.Info("[configstore] %s: dropping table TablePromptVersionMessage", migrationName)
			if err := migrator.DropTable(&tables.TablePromptVersionMessage{}); err != nil {
				return err
			}
			logger.Info("[configstore] %s: dropping table TablePromptVersion", migrationName)
			if err := migrator.DropTable(&tables.TablePromptVersion{}); err != nil {
				return err
			}
			logger.Info("[configstore] %s: dropping table TablePrompt", migrationName)
			if err := migrator.DropTable(&tables.TablePrompt{}); err != nil {
				return err
			}
			logger.Info("[configstore] %s: dropping table TableFolder", migrationName)
			if err := migrator.DropTable(&tables.TableFolder{}); err != nil {
				return err
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running prompt repo tables migration: %s", err.Error())
	}

	// Add prompt_id column to prompt message tables
	m = migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_prompt_id_to_prompt_message_tables",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := addColumnIfNotExists(tx, logger, &tables.TablePromptVersionMessage{}, "PromptID"); err != nil {
				return err
			}

			if err := addColumnIfNotExists(tx, logger, &tables.TablePromptSessionMessage{}, "PromptID"); err != nil {
				return err
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := dropColumnIfExists(tx, logger, &tables.TablePromptVersionMessage{}, "prompt_id"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &tables.TablePromptSessionMessage{}, "prompt_id"); err != nil {
				return err
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running add_prompt_id_to_prompt_message_tables migration: %s", err.Error())
	}

	m = migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_model_parameters_table",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasTable(&tables.TableModelParameters{}) {
				logger.Info("[configstore] %s: creating table TableModelParameters", migrationName)
				if err := migrator.CreateTable(&tables.TableModelParameters{}); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if migrator.HasTable(&tables.TableModelParameters{}) {
				logger.Info("[configstore] %s: dropping table TableModelParameters", migrationName)
				if err := migrator.DropTable(&tables.TableModelParameters{}); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running add_model_parameters_table migration: %s", err.Error())
	}

	return nil
}

// migrationBackfillAllowedModelsWildcard converts empty allowed_models on
// governance_virtual_key_provider_configs and empty models_json on keys to ["*"],
// preserving the previous "empty = allow all" semantics for existing records.
// After this migration the new convention applies: ["*"] = allow all, [] = deny all.
func migrationBackfillAllowedModelsWildcard(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "backfill_allowed_models_wildcard"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			// --- Field 1: vk.provider_config.allowed_models ---
			// Rows with '[]' previously meant "allow all models"; migrate to '["*"]'.
			if err := tx.Model(&tables.TableVirtualKeyProviderConfig{}).
				Where("allowed_models = ? OR allowed_models IS NULL", `[]`).
				Update("allowed_models", `["*"]`).Error; err != nil {
				return fmt.Errorf("failed to backfill provider_config allowed_models: %w", err)
			}

			// Recompute config_hash for all VKs that have provider configs
			// (any of them may have had their allowed_models updated above).
			var modifiedVKIDs []string
			if err := tx.Model(&tables.TableVirtualKeyProviderConfig{}).
				Distinct("virtual_key_id").
				Pluck("virtual_key_id", &modifiedVKIDs).Error; err != nil {
				return fmt.Errorf("failed to query VK IDs for hash recomputation: %w", err)
			}

			logger.Info("[configstore] %s: processing %d modifiedVKIDs", migrationName, len(modifiedVKIDs))
			for _, vkID := range modifiedVKIDs {
				var vk tables.TableVirtualKey
				if err := tx.
					Preload("ProviderConfigs").
					Preload("ProviderConfigs.Keys").
					Preload("MCPConfigs").
					First(&vk, "id = ?", vkID).Error; err != nil {
					if err == gorm.ErrRecordNotFound {
						// Orphaned provider config row — VK was deleted; skip.
						continue
					}
					return fmt.Errorf("failed to reload VK %s for hash recomputation: %w", vkID, err)
				}
				newHash, err := GenerateVirtualKeyHash(vk)
				if err != nil {
					return fmt.Errorf("failed to generate hash for VK %s: %w", vkID, err)
				}
				if err := tx.Model(&tables.TableVirtualKey{}).
					Where("id = ?", vkID).
					Update("config_hash", newHash).Error; err != nil {
					return fmt.Errorf("failed to update config_hash for VK %s: %w", vkID, err)
				}
				logger.Info("[Migration] Recomputed config_hash for VK '%s' after allowed_models backfill", vk.Name)
			}

			// --- Field 2: provider.key.models (models_json column) ---
			// Rows with '[]' or empty string previously meant "allow all models"; migrate to '["*"]'.
			if err := tx.Model(&tables.TableKey{}).
				Where("models_json = ? OR models_json = ? OR models_json IS NULL", `[]`, ``).
				Update("models_json", `["*"]`).Error; err != nil {
				return fmt.Errorf("failed to backfill key models_json: %w", err)
			}

			// Recompute config_hash for all keys since models_json is part of the hash input.
			var keys []tables.TableKey
			if err := tx.Find(&keys).Error; err != nil {
				return fmt.Errorf("failed to fetch keys for hash recomputation: %w", err)
			}
			logger.Info("[configstore] %s: processing %d keys", migrationName, len(keys))
			for _, key := range keys {
				schemaKey := schemas.Key{
					Name:               key.Name,
					Value:              key.Value,
					Models:             key.Models,
					Weight:             getWeight(key.Weight),
					AzureKeyConfig:     key.AzureKeyConfig,
					VertexKeyConfig:    key.VertexKeyConfig,
					BedrockKeyConfig:   key.BedrockKeyConfig,
					Aliases:            key.Aliases,
					VLLMKeyConfig:      key.VLLMKeyConfig,
					ReplicateKeyConfig: key.ReplicateKeyConfig,
					OllamaKeyConfig:    key.OllamaKeyConfig,
					SGLKeyConfig:       key.SGLKeyConfig,
					Enabled:            key.Enabled,
					UseForBatchAPI:     key.UseForBatchAPI,
				}
				hash, err := GenerateKeyHash(schemaKey)
				if err != nil {
					return fmt.Errorf("failed to generate hash for key %s: %w", key.Name, err)
				}
				if err := tx.Model(&key).Update("config_hash", hash).Error; err != nil {
					return fmt.Errorf("failed to update config_hash for key %s: %w", key.Name, err)
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			// Rollback is intentionally a no-op: reverting ["*"] back to [] would
			// re-introduce the ambiguous "empty = allow all" semantics on downgrade.
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running backfill_allowed_models_wildcard migration: %s", err.Error())
	}
	return nil
}

// migrationRepairBareWildcardAllowedModels repairs governance_virtual_key_provider_configs
// rows whose allowed_models / blacklisted_models column holds the bare one-character
// string '*' instead of the JSON array '["*"]'. Such rows abort the GORM json
// deserializer ("invalid character '*' ...") when the VK is loaded, which poisons the
// whole provider admin surface (see issue #4318). The repair rewrites the column to the
// canonical '["*"]' form the serializer:json tag is supposed to produce; the intended
// value at write time was already the WhiteList ["*"], so the config_hash — computed
// from the in-memory slice — stays consistent and needs no recomputation.
func migrationRepairBareWildcardAllowedModels(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "repair_bare_wildcard_allowed_models"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			// Match the documented manual workaround exactly: bare '*' → '["*"]'.
			// Covers both whitelist columns on the provider config, which share the
			// serializer:json tag and the same corruption class.
			for _, column := range []string{"allowed_models", "blacklisted_models"} {
				res := tx.Model(&tables.TableVirtualKeyProviderConfig{}).
					Where(column+" = ?", "*").
					Update(column, `["*"]`)
				if res.Error != nil {
					return fmt.Errorf("failed to repair bare wildcard %s: %w", column, res.Error)
				}
				if res.RowsAffected > 0 {
					logger.Info("[configstore] %s: repaired %d rows with bare wildcard %s", migrationName, res.RowsAffected, column)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			// Rollback is a no-op: reverting '["*"]' back to '*' would re-introduce
			// the value that breaks the deserializer.
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running %s migration: %s", migrationName, err.Error())
	}
	return nil
}

// migrationAddMCPClientAllowedExtraHeadersJSONColumn adds the allowed_extra_headers_json column to the mcp_client table
func migrationAddMCPClientAllowedExtraHeadersJSONColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_mcp_client_allowed_extra_headers_json_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableMCPClient{}, "allowed_extra_headers_json"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableMCPClient{}, "allowed_extra_headers_json"); err != nil {
				return err
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running add_mcp_client_allowed_extra_headers_json_column migration: %s", err.Error())
	}
	return nil
}

// migrationAddSkillsRepoTables adds the skills repository tables.
// Files belong to skill_versions (not directly to skills); blobs are reused
// across versions via shared blob_id/storage_key references.
//
// Idempotent: guards each table create so retrying after a partially applied
// migration does not fail when some tables were already created.
func migrationAddSkillsRepoTables(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	logger.Info("[configstore] running migrationAddSkillsRepoTables")
	defer logger.Info("[configstore] migrationAddSkillsRepoTables finished")
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_skills_repo_tables",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			// --- skills table ---
			if !mg.HasTable(&tables.TableSkill{}) {
				if err := mg.CreateTable(&tables.TableSkill{}); err != nil {
					return fmt.Errorf("create skills table: %w", err)
				}
			}
			// --- skill_versions table ---
			if !mg.HasTable(&tables.TableSkillVersion{}) {
				if err := mg.CreateTable(&tables.TableSkillVersion{}); err != nil {
					return fmt.Errorf("create skill_versions table: %w", err)
				}
			}
			// --- skill_file_blobs table ---
			if !mg.HasTable(&tables.TableSkillFileBlob{}) {
				if err := mg.CreateTable(&tables.TableSkillFileBlob{}); err != nil {
					return fmt.Errorf("create skill_file_blobs table: %w", err)
				}
			}
			// --- skill_files table ---
			if !mg.HasTable(&tables.TableSkillFile{}) {
				if err := mg.CreateTable(&tables.TableSkillFile{}); err != nil {
					return fmt.Errorf("create skill_files table: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			if err := mg.DropTable(&tables.TableSkillFile{}); err != nil {
				return err
			}
			if err := mg.DropTable(&tables.TableSkillVersion{}); err != nil {
				return err
			}
			if err := mg.DropTable(&tables.TableSkill{}); err != nil {
				return err
			}
			if err := mg.DropTable(&tables.TableSkillFileBlob{}); err != nil {
				return err
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running skills repo tables migration: %s", err.Error())
	}
	return nil
}

// migrationAddPluginOrderColumns adds placement and exec_order columns to config_plugins table
func migrationAddPluginOrderColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_plugin_order_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := addColumnIfNotExists(tx, logger, &tables.TablePlugin{}, "Placement"); err != nil {
				return fmt.Errorf("failed to add placement column: %w", err)
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TablePlugin{}, "Order"); err != nil {
				return fmt.Errorf("failed to add exec_order column: %w", err)
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := dropColumnIfExists(tx, logger, &tables.TablePlugin{}, "placement"); err != nil {
				return fmt.Errorf("failed to drop placement column: %w", err)
			}
			if err := dropColumnIfExists(tx, logger, &tables.TablePlugin{}, "exec_order"); err != nil {
				return fmt.Errorf("failed to drop exec_order column: %w", err)
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running add_plugin_order_columns migration: %s", err.Error())
	}
	return nil
}

// migrationMakeBasePricingColumnsNullable drops the NOT NULL constraint on
// input_cost_per_token and output_cost_per_token in governance_model_pricing,
// allowing models that only have non-token pricing (image, audio, video) to be
// stored without a placeholder zero value.
func migrationMakeBasePricingColumnsNullable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "make_base_pricing_columns_nullable"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			m := tx.Migrator()
			if err := m.AlterColumn(&tables.TableModelPricing{}, "InputCostPerToken"); err != nil {
				return fmt.Errorf("failed to alter input_cost_per_token: %w", err)
			}
			if err := m.AlterColumn(&tables.TableModelPricing{}, "OutputCostPerToken"); err != nil {
				return fmt.Errorf("failed to alter output_cost_per_token: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running make_base_pricing_columns_nullable migration: %s", err.Error())
	}
	return nil
}

// migrationAddAllowOnAllVirtualKeysColumn adds the allow_on_all_virtual_keys column to the mcp_client table
func migrationAddAllowOnAllVirtualKeysColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_allow_on_all_virtual_keys_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableMCPClient{}, "allow_on_all_virtual_keys"); err != nil {
				return fmt.Errorf("failed to add allow_on_all_virtual_keys column: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableMCPClient{}, "allow_on_all_virtual_keys"); err != nil {
				return fmt.Errorf("failed to drop allow_on_all_virtual_keys column: %w", err)
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running add_allow_on_all_virtual_keys_column migration: %s", err.Error())
	}
	return nil
}

// migrationAddOpenAIConfigJSONColumn adds the open_ai_config_json column to the provider table
func migrationAddOpenAIConfigJSONColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_open_ai_config_json_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableProvider{}, "OpenAIConfigJSON"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableProvider{}, "open_ai_config_json"); err != nil {
				return err
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running add_open_ai_config_json_column migration: %s", err.Error())
	}
	return nil
}

// migrationAddPromptVariablesColumns adds variables_json column to prompt_sessions and prompt_versions
func migrationAddPromptVariablesColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_prompt_variables_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := addColumnIfNotExists(tx, logger, &tables.TablePromptSession{}, "VariablesJSON"); err != nil {
				return fmt.Errorf("failed to add variables_json column to prompt_sessions: %w", err)
			}

			if err := addColumnIfNotExists(tx, logger, &tables.TablePromptVersion{}, "VariablesJSON"); err != nil {
				return fmt.Errorf("failed to add variables_json column to prompt_versions: %w", err)
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TablePromptSession{}, "variables_json"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &tables.TablePromptVersion{}, "variables_json"); err != nil {
				return err
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running add_prompt_variables_columns migration: %s", err.Error())
	}
	return nil
}

// migrationAddKeyBlacklistedModelsJSONColumn adds blacklisted_models_json to config_keys
// for per-key model deny lists (JSON array of model ids, default []).
func migrationAddKeyBlacklistedModelsJSONColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_key_blacklisted_models_json_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, "blacklisted_models_json"); err != nil {
				return fmt.Errorf("failed to add blacklisted_models_json column: %w", err)
			}
			if err := tx.Exec("UPDATE config_keys SET blacklisted_models_json = '[]' WHERE blacklisted_models_json IS NULL OR blacklisted_models_json = ''").Error; err != nil {
				return fmt.Errorf("failed to backfill blacklisted_models_json: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, "blacklisted_models_json"); err != nil {
				return fmt.Errorf("failed to drop blacklisted_models_json column: %w", err)
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_key_blacklisted_models_json_column migration: %s", err.Error())
	}
	return nil
}

// migrationAddChainRuleColumnToRoutingRules adds chain_rule to routing_rules.
// When true, the routing engine re-evaluates the full rule set after this rule matches,
// using the resolved provider/model as the new context input.
func migrationAddChainRuleColumnToRoutingRules(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_chain_rule_column_to_routing_rules"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableRoutingRule{}, "chain_rule"); err != nil {
				return fmt.Errorf("failed to add chain_rule column: %w", err)
			}

			// Backfill config_hash for all existing routing rules.
			// GenerateRoutingRuleHash now includes chain_rule, so existing hashes
			// (computed without it) are stale and must be recomputed to avoid
			// every rule appearing as changed after this upgrade.
			var rules []tables.TableRoutingRule
			if err := tx.Preload("Targets").Find(&rules).Error; err != nil {
				return fmt.Errorf("failed to load routing rules for config_hash backfill: %w", err)
			}
			logger.Info("[configstore] %s: processing %d rules", migrationName, len(rules))
			for _, rule := range rules {
				hash, err := GenerateRoutingRuleHash(rule)
				if err != nil {
					return fmt.Errorf("failed to generate config_hash for routing rule %s: %w", rule.ID, err)
				}
				if err := tx.Model(&tables.TableRoutingRule{}).Where("id = ?", rule.ID).Update("config_hash", hash).Error; err != nil {
					return fmt.Errorf("failed to update config_hash for routing rule %s: %w", rule.ID, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableRoutingRule{}, "chain_rule"); err != nil {
				return fmt.Errorf("failed to drop chain_rule column: %w", err)
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_chain_rule_column_to_routing_rules migration: %s", err.Error())
	}
	return nil
}

// migrationAddReplicateKeyConfigColumn adds the replicate_use_deployments_endpoint column to the key table
func migrationAddReplicateKeyConfigColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_replicate_key_config_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			if !mg.HasColumn(&tables.TableKey{}, "replicate_use_deployments_endpoint") {
				if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, "replicate_use_deployments_endpoint"); err != nil {
					return err
				}
				// Backfill: Replicate keys that had deployments configured (now in aliases_json after
				// migrationDropDeploymentColumnsAndAddAliases) were using the deployments endpoint.
				trueVal := true
				if err := tx.Model(&tables.TableKey{}).
					Where("provider = ? AND aliases_json IS NOT NULL AND aliases_json != ? AND aliases_json != ?",
						string(schemas.Replicate), "", "{}",
					).
					Update("ReplicateUseDeploymentsEndpoint", &trueVal).Error; err != nil {
					return err
				}

				// Recompute config_hash for Replicate keys that were updated above,
				// since replicate_use_deployments_endpoint is part of the hash input.
				var affectedKeys []tables.TableKey
				if err := tx.Where(
					"provider = ? AND replicate_use_deployments_endpoint IS NOT NULL",
					string(schemas.Replicate),
				).Find(&affectedKeys).Error; err != nil {
					return fmt.Errorf("failed to fetch replicate keys for hash recomputation: %w", err)
				}
				logger.Info("[configstore] %s: processing %d affectedKeys", migrationName, len(affectedKeys))
				for _, key := range affectedKeys {
					schemaKey := schemas.Key{
						Name:               key.Name,
						Value:              key.Value,
						Models:             key.Models,
						BlacklistedModels:  key.BlacklistedModels,
						Weight:             getWeight(key.Weight),
						AzureKeyConfig:     key.AzureKeyConfig,
						VertexKeyConfig:    key.VertexKeyConfig,
						BedrockKeyConfig:   key.BedrockKeyConfig,
						Aliases:            key.Aliases,
						VLLMKeyConfig:      key.VLLMKeyConfig,
						ReplicateKeyConfig: key.ReplicateKeyConfig,
						Enabled:            key.Enabled,
						UseForBatchAPI:     key.UseForBatchAPI,
					}
					hash, err := GenerateKeyHash(schemaKey)
					if err != nil {
						return fmt.Errorf("failed to generate hash for key %s: %w", key.Name, err)
					}
					if err := tx.Model(&key).Update("config_hash", hash).Error; err != nil {
						return fmt.Errorf("failed to update config_hash for key %s: %w", key.Name, err)
					}
					logger.Info("[Migration] Recomputed config_hash for replicate key '%s' after replicate config backfill", key.Name)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, "replicate_use_deployments_endpoint"); err != nil {
				return err
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_replicate_key_config_column migration: %s", err.Error())
	}
	return nil
}

// migrationAddBudgetCalendarAlignedColumn was originally for adding calendar_aligned to governance_budgets.
// Calendar alignment is now a VK-level field (governance_virtual_keys.calendar_aligned) added in migrationAddMultiBudgetTables.
// This migration is kept as a no-op so the migrator doesn't try to re-run it.
func migrationAddBudgetCalendarAlignedColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_budget_calendar_aligned_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID:       migrationName,
		Migrate:  func(tx *gorm.DB) error { return nil },
		Rollback: func(tx *gorm.DB) error { return nil },
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_budget_calendar_aligned_column migration: %s", err.Error())
	}
	return nil
}

// migrationAddRoutingChainMaxDepthColumn adds routing_chain_max_depth to the client config table.
// Defaults to 10, which is the built-in default for routing rule chain evaluation depth.
func migrationAddRoutingChainMaxDepthColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_routing_chain_max_depth_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			if !mg.HasColumn(&tables.TableClientConfig{}, "routing_chain_max_depth") {
				if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "routing_chain_max_depth"); err != nil {
					return fmt.Errorf("failed to add routing_chain_max_depth column: %w", err)
				}
				// Recompute config_hash for all existing client configs that have one.
				// RoutingChainMaxDepth is now included in the hash (when > 0), so without
				// this recompute the stored hash would mismatch on every startup after upgrade.
				var clientConfigs []tables.TableClientConfig
				if err := tx.Find(&clientConfigs).Error; err != nil {
					return fmt.Errorf("failed to fetch client configs for hash recompute: %w", err)
				}
				logger.Info("[configstore] %s: processing %d clientConfigs", migrationName, len(clientConfigs))
				for _, cc := range clientConfigs {
					if cc.ConfigHash == "" {
						continue // no stored hash to invalidate
					}
					depth := cc.RoutingChainMaxDepth
					if depth == 0 {
						// Should never happen, but just in case.
						depth = 10 // DefaultRoutingChainMaxDepth
					}
					clientConfig := ClientConfig{
						DropExcessRequests:              cc.DropExcessRequests,
						InitialPoolSize:                 cc.InitialPoolSize,
						PrometheusLabels:                cc.PrometheusLabels,
						EnableLogging:                   cc.EnableLogging,
						DisableContentLogging:           cc.DisableContentLogging,
						DisableDBPingsInHealth:          cc.DisableDBPingsInHealth,
						LogRetentionDays:                cc.LogRetentionDays,
						EnforceAuthOnInference:          cc.EnforceAuthOnInference,
						AllowedOrigins:                  cc.AllowedOrigins,
						AllowedHeaders:                  cc.AllowedHeaders,
						MaxRequestBodySizeMB:            cc.MaxRequestBodySizeMB,
						HideDeletedVirtualKeysInFilters: cc.HideDeletedVirtualKeysInFilters,
						MCPAgentDepth:                   cc.MCPAgentDepth,
						MCPToolExecutionTimeout:         cc.MCPToolExecutionTimeout,
						MCPCodeModeBindingLevel:         cc.MCPCodeModeBindingLevel,
						MCPToolSyncInterval:             cc.MCPToolSyncInterval,
						MCPDisableAutoToolInject:        cc.MCPDisableAutoToolInject,
						MCPEnableTempTokenAuth:          cc.MCPEnableTempTokenAuth,
						AsyncJobResultTTL:               cc.AsyncJobResultTTL,
						LoggingHeaders:                  cc.LoggingHeaders,
						RequiredHeaders:                 cc.RequiredHeaders,
						HeaderFilterConfig:              cc.HeaderFilterConfig,
						RoutingChainMaxDepth:            depth,
					}
					newHash, err := clientConfig.GenerateClientConfigHash()
					if err != nil {
						return fmt.Errorf("failed to generate hash for client config %d: %w", cc.ID, err)
					}
					if err := tx.Model(&cc).Update("config_hash", newHash).Error; err != nil {
						return fmt.Errorf("failed to update hash for client config %d: %w", cc.ID, err)
					}
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "routing_chain_max_depth"); err != nil {
				return fmt.Errorf("failed to drop routing_chain_max_depth column: %w", err)
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_routing_chain_max_depth_column migration: %s", err.Error())
	}
	return nil
}

// migrationAddModelCapabilityColumns adds model capability metadata columns to governance_model_pricing.
func migrationAddModelCapabilityColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_model_capability_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			columns := []string{
				"context_length",
				"max_input_tokens",
				"max_output_tokens",
				"architecture",
			}
			for _, column := range columns {
				if err := addColumnIfNotExists(tx, logger, &tables.TableModelPricing{}, column); err != nil {
					return fmt.Errorf("failed to add %s column: %w", column, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			columns := []string{
				"context_length",
				"max_input_tokens",
				"max_output_tokens",
				"architecture",
			}
			for _, column := range columns {
				if err := dropColumnIfExists(tx, logger, &tables.TableModelPricing{}, column); err != nil {
					return fmt.Errorf("failed to drop %s column: %w", column, err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_model_capability_columns migration: %s", err.Error())
	}
	return nil
}

// migrationAddOllamaSGLConfigColumns adds ollama_url and sgl_url columns to the key table
func migrationAddOllamaSGLConfigColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_ollama_sgl_config_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, "ollama_url"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TableKey{}, "sgl_url"); err != nil {
				return err
			}

			// Backfill: for each ollama/sgl provider with a base_url, create a key
			// with that URL and clear base_url from network_config.
			var providers []tables.TableProvider
			if err := tx.Where("name IN ?", []string{"ollama", "sgl"}).Find(&providers).Error; err != nil {
				return fmt.Errorf("failed to fetch ollama/sgl providers for URL backfill: %w", err)
			}
			logger.Info("[configstore] %s: processing %d providers", migrationName, len(providers))
			for _, p := range providers {
				if p.NetworkConfigJSON == "" {
					continue
				}
				var nc schemas.NetworkConfig
				if err := json.Unmarshal([]byte(p.NetworkConfigJSON), &nc); err != nil {
					logger.Info("[Migration] Failed to parse network_config for provider %s (id=%d), skipping: %v", p.Name, p.ID, err)
					continue
				}
				if nc.BaseURL == "" {
					continue
				}

				// Create a new key with the provider's base_url
				urlSecretVar := schemas.SecretVar{Val: nc.BaseURL}
				enabled := true
				weight := 1.0
				newKey := tables.TableKey{
					Provider:   p.Name,
					ProviderID: p.ID,
					KeyID:      uuid.NewString(),
					Weight:     &weight,
					Enabled:    &enabled,
					Models:     schemas.WhiteList{"*"},
				}
				if strings.ToLower(p.Name) == "ollama" {
					newKey.Name = "Default Ollama Key"
					newKey.OllamaKeyConfig = &schemas.OllamaKeyConfig{URL: urlSecretVar}
				}
				if strings.ToLower(p.Name) == "sgl" {
					newKey.Name = "Default SGL Key"
					newKey.SGLKeyConfig = &schemas.SGLKeyConfig{URL: urlSecretVar}
				}

				schemaKey := schemaKeyFromTableKey(newKey)
				hash, err := GenerateKeyHash(schemaKey)
				if err != nil {
					return fmt.Errorf("failed to generate hash for new key on provider %s: %w", p.Name, err)
				}
				newKey.ConfigHash = hash
				if err := tx.Create(&newKey).Error; err != nil {
					return fmt.Errorf("failed to create key for provider %s: %w", p.Name, err)
				}
				logger.Info("[Migration] Created key '%s' for provider '%s' from network_config.base_url", newKey.Name, p.Name)
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, "ollama_url"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &tables.TableKey{}, "sgl_url"); err != nil {
				return err
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running ollama sgl key config columns migration: %s", err.Error())
	}
	return nil
}

// migrationAddMultiBudgetTables creates junction tables for multi-budget support and backfills existing data.
func migrationAddMultiBudgetTables(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_multi_budget_tables"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			// Add calendar_aligned to governance_virtual_keys (VK-level setting)
			if err := addColumnIfNotExists(tx, logger, &tables.TableVirtualKey{}, "CalendarAligned"); err != nil {
				return fmt.Errorf("failed to add calendar_aligned column to governance_virtual_keys: %w", err)
			}

			// Add FK columns on governance_budgets for multi-budget ownership
			if err := addColumnIfNotExists(tx, logger, &tables.TableBudget{}, "VirtualKeyID"); err != nil {
				return fmt.Errorf("failed to add virtual_key_id column to governance_budgets: %w", err)
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TableBudget{}, "ProviderConfigID"); err != nil {
				return fmt.Errorf("failed to add provider_config_id column to governance_budgets: %w", err)
			}

			// Create indexes on the new FK columns (AddColumn doesn't create indexes from struct tags)
			if !mg.HasIndex(&tables.TableBudget{}, "idx_governance_budgets_virtual_key_id") {
				logger.Info("[configstore] %s: creating index VirtualKeyID on TableBudget", migrationName)
				if err := mg.CreateIndex(&tables.TableBudget{}, "VirtualKeyID"); err != nil {
					return fmt.Errorf("failed to create index on governance_budgets.virtual_key_id: %w", err)
				}
			}
			if !mg.HasIndex(&tables.TableBudget{}, "idx_governance_budgets_provider_config_id") {
				logger.Info("[configstore] %s: creating index ProviderConfigID on TableBudget", migrationName)
				if err := mg.CreateIndex(&tables.TableBudget{}, "ProviderConfigID"); err != nil {
					return fmt.Errorf("failed to create index on governance_budgets.provider_config_id: %w", err)
				}
			}

			// Backfill: set virtual_key_id from legacy VK budget_id (if column still exists)
			if mg.HasColumn(&tables.TableVirtualKey{}, "budget_id") {
				if err := tx.Exec(`
					UPDATE governance_budgets SET virtual_key_id = (
						SELECT id FROM governance_virtual_keys
						WHERE governance_virtual_keys.budget_id = governance_budgets.id
					) WHERE virtual_key_id IS NULL AND EXISTS (
						SELECT 1 FROM governance_virtual_keys
						WHERE governance_virtual_keys.budget_id = governance_budgets.id
					)
				`).Error; err != nil {
					return fmt.Errorf("failed to backfill VK budget virtual_key_id: %w", err)
				}
			}

			// Backfill: set provider_config_id from legacy PC budget_id (if column still exists)
			if mg.HasColumn(&tables.TableVirtualKeyProviderConfig{}, "budget_id") {
				if err := tx.Exec(`
					UPDATE governance_budgets SET provider_config_id = (
						SELECT id FROM governance_virtual_key_provider_configs
						WHERE governance_virtual_key_provider_configs.budget_id = governance_budgets.id
					) WHERE provider_config_id IS NULL AND EXISTS (
						SELECT 1 FROM governance_virtual_key_provider_configs
						WHERE governance_virtual_key_provider_configs.budget_id = governance_budgets.id
					)
				`).Error; err != nil {
					return fmt.Errorf("failed to backfill PC budget provider_config_id: %w", err)
				}
			}

			// Backfill: copy calendar_aligned from legacy budget column to VK-level field
			// (governance_budgets.calendar_aligned was added by add_budget_calendar_aligned_column on main)
			if mg.HasColumn(&tables.TableBudget{}, "calendar_aligned") {
				if err := tx.Exec(`
					UPDATE governance_virtual_keys SET calendar_aligned = true
					WHERE id IN (
						SELECT DISTINCT virtual_key_id FROM governance_budgets
						WHERE calendar_aligned = true AND virtual_key_id IS NOT NULL
					) AND calendar_aligned = false
				`).Error; err != nil {
					return fmt.Errorf("failed to backfill calendar_aligned from budgets to virtual keys: %w", err)
				}
				// Drop the legacy calendar_aligned column from governance_budgets.
				// Plain column with no FK references — not a correctness risk if left behind,
				// but log a warning so it's not invisible.
				if err := tx.Exec(dropColumnSQL(tx, "governance_budgets", "calendar_aligned")).Error; err != nil {
					logger.Info("[Migration] warning: could not drop legacy calendar_aligned column from governance_budgets: %v", err)
				}
			}

			// Drop legacy budget_id columns BEFORE creating FK constraints.
			// On SQLite, ALTER TABLE RENAME propagates into FK references in other tables.
			// If we create FK constraints on governance_budgets first, then rename the
			// parent table during the legacy column drop (table rebuild), SQLite updates
			// those FK references to point at the temporary backup table name.
			if err := dropLegacyBudgetColumn(tx, "governance_virtual_keys"); err != nil {
				return err
			}
			if err := dropLegacyBudgetColumn(tx, "governance_virtual_key_provider_configs"); err != nil {
				return err
			}

			// Create FK constraints with CASCADE delete (defined on parent structs).
			// Must happen after legacy column drops so SQLite rename propagation
			// cannot corrupt these FK references.
			if !mg.HasConstraint(&tables.TableVirtualKey{}, "Budgets") {
				if err := mg.CreateConstraint(&tables.TableVirtualKey{}, "Budgets"); err != nil {
					return fmt.Errorf("failed to create FK constraint for VirtualKey -> Budgets: %w", err)
				}
			}
			if !mg.HasConstraint(&tables.TableVirtualKeyProviderConfig{}, "Budgets") {
				if err := mg.CreateConstraint(&tables.TableVirtualKeyProviderConfig{}, "Budgets"); err != nil {
					return fmt.Errorf("failed to create FK constraint for ProviderConfig -> Budgets: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableBudget{}, "VirtualKeyID"); err != nil {
				return fmt.Errorf("failed to drop virtual_key_id column from governance_budgets: %w", err)
			}
			if err := dropColumnIfExists(tx, logger, &tables.TableBudget{}, "ProviderConfigID"); err != nil {
				return fmt.Errorf("failed to drop provider_config_id column from governance_budgets: %w", err)
			}
			return nil
		},
	}})
	// SQLite workaround: GORM's CreateConstraint rebuilds the table via DROP+RENAME
	// inside a transaction. The DROP fails when other tables have FKs pointing at the
	// target table and foreign_keys is ON. PRAGMA foreign_keys cannot be changed inside
	// a transaction, so we disable it before the migrator opens its transaction.
	// This only affects SQLite — Postgres supports ALTER TABLE ADD CONSTRAINT natively.
	if db.Dialector.Name() == "sqlite" {
		// PRAGMA foreign_keys is per-connection in SQLite. Pin the pool to a single
		// connection so the PRAGMA and the migration transaction share the same one.
		sqlDB, err := db.DB()
		if err != nil {
			return fmt.Errorf("failed to get underlying sql.DB: %w", err)
		}
		prevMaxOpenConns := sqlDB.Stats().MaxOpenConnections
		sqlDB.SetMaxOpenConns(1)
		defer sqlDB.SetMaxOpenConns(prevMaxOpenConns)

		if err := db.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
			return fmt.Errorf("failed to disable SQLite foreign keys: %w", err)
		}
		defer func() {
			if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
				log.Fatalf("[Migration] FATAL: failed to re-enable SQLite foreign keys: %v", err)
			}
		}()
	}
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_multi_budget_tables migration: %s", err.Error())
	}
	return nil
}

// migrationAddTeamBudgetsToBudgetsTable pivots team budgets from a single-FK on
// governance_teams.budget_id to multi-budget ownership via governance_budgets.team_id,
// mirroring how VK/ProviderConfig budgets were restructured in migrationAddMultiBudgetTables.
func migrationAddTeamBudgetsToBudgetsTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_team_budgets_to_budgets_table"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()

			// Add team_id FK column on governance_budgets
			if err := addColumnIfNotExists(tx, logger, &tables.TableBudget{}, "TeamID"); err != nil {
				return fmt.Errorf("failed to add team_id column to governance_budgets: %w", err)
			}

			// Create index on the new FK column (AddColumn doesn't create indexes from struct tags)
			if !mg.HasIndex(&tables.TableBudget{}, "idx_governance_budgets_team_id") {
				logger.Info("[configstore] %s: creating index TeamID on TableBudget", migrationName)
				if err := mg.CreateIndex(&tables.TableBudget{}, "TeamID"); err != nil {
					return fmt.Errorf("failed to create index on governance_budgets.team_id: %w", err)
				}
			}

			// Backfill: set team_id from legacy governance_teams.budget_id (if column still exists)
			if mg.HasColumn(&tables.TableTeam{}, "budget_id") {
				// Preflight: raw SQL below bypasses TableBudget.BeforeSave (which now
				// enforces exactly-one-of {TeamID, VirtualKeyID, ProviderConfigID}).
				// Fail fast if any team-referenced budget is already owned by a VK or
				// ProviderConfig, rather than silently producing a multi-owner row
				// that would later be rejected by the hook on its next update.
				var conflictCount int64
				if err := tx.Raw(`
					SELECT COUNT(*) FROM governance_budgets b
					WHERE (b.virtual_key_id IS NOT NULL OR b.provider_config_id IS NOT NULL)
					  AND EXISTS (SELECT 1 FROM governance_teams t WHERE t.budget_id = b.id)
				`).Scan(&conflictCount).Error; err != nil {
					return fmt.Errorf("failed to check for multi-owner team budget conflicts: %w", err)
				}
				if conflictCount > 0 {
					return fmt.Errorf(
						"cannot migrate team budgets: %d budget row(s) referenced by a team are already owned by a virtual key or provider config; resolve manually before re-running",
						conflictCount,
					)
				}

				if err := tx.Exec(`
					UPDATE governance_budgets SET team_id = (
						SELECT id FROM governance_teams
						WHERE governance_teams.budget_id = governance_budgets.id
					) WHERE team_id IS NULL AND EXISTS (
						SELECT 1 FROM governance_teams
						WHERE governance_teams.budget_id = governance_budgets.id
					)
				`).Error; err != nil {
					return fmt.Errorf("failed to backfill team budget team_id: %w", err)
				}

				// Drop legacy budget_id column BEFORE creating FK constraint.
				// On SQLite, ALTER TABLE RENAME propagates into FK references in other
				// tables. Dropping first prevents the FK on governance_budgets.team_id
				// from being corrupted by the table rebuild's rename step.
				if err := dropLegacyBudgetColumn(tx, "governance_teams"); err != nil {
					return err
				}
			}

			// Create FK constraint with CASCADE delete (defined on TableTeam.Budgets).
			// Must happen after legacy column drop so SQLite rename propagation
			// cannot corrupt this FK reference.
			if !mg.HasConstraint(&tables.TableTeam{}, "Budgets") {
				if err := mg.CreateConstraint(&tables.TableTeam{}, "Budgets"); err != nil {
					return fmt.Errorf("failed to create FK constraint for Team -> Budgets: %w", err)
				}
			}

			// Refresh config_hash for teams whose budgets just got linked. GenerateTeamHash
			// now includes sorted budget IDs, so hashes written by the earlier
			// migrationAddConfigHashColumn (which ran before budgets were associated)
			// are stale and would cause phantom drift on the next config.json sync.
			var teamsToRehash []tables.TableTeam
			if err := tx.Preload("Budgets").Find(&teamsToRehash).Error; err != nil {
				return fmt.Errorf("failed to fetch teams for hash refresh: %w", err)
			}
			logger.Info("[configstore] %s: processing %d teamsToRehash", migrationName, len(teamsToRehash))
			for _, team := range teamsToRehash {
				if len(team.Budgets) == 0 {
					continue // hash did not change; skip
				}
				hash, err := GenerateTeamHash(team)
				if err != nil {
					return fmt.Errorf("failed to generate hash for team %s: %w", team.ID, err)
				}
				if err := tx.Model(&tables.TableTeam{}).Where("id = ?", team.ID).Update("config_hash", hash).Error; err != nil {
					return fmt.Errorf("failed to update hash for team %s: %w", team.ID, err)
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableBudget{}, "TeamID"); err != nil {
				return fmt.Errorf("failed to drop team_id column from governance_budgets: %w", err)
			}
			return nil
		},
	}})
	// SQLite workaround — same reasoning as migrationAddMultiBudgetTables.
	if db.Dialector.Name() == "sqlite" {
		sqlDB, err := db.DB()
		if err != nil {
			return fmt.Errorf("failed to get underlying sql.DB: %w", err)
		}
		prevMaxOpenConns := sqlDB.Stats().MaxOpenConnections
		sqlDB.SetMaxOpenConns(1)
		defer sqlDB.SetMaxOpenConns(prevMaxOpenConns)

		if err := db.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
			return fmt.Errorf("failed to disable SQLite foreign keys: %w", err)
		}
		defer func() {
			if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
				log.Fatalf("[Migration] FATAL: failed to re-enable SQLite foreign keys: %v", err)
			}
		}()
	}
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_team_budgets_to_budgets_table migration: %s", err.Error())
	}
	return nil
}

// migrationAddModelConfigBudgetsFKConstraint adds the missing
// governance_budgets.model_config_id -> governance_model_configs(id)
// ON DELETE CASCADE foreign key (defined on TableModelConfig.Budgets via
// foreignKey:ModelConfigID;constraint:OnDelete:CASCADE).
//
// migrationAddMultiBudgetTables created the equivalent cascade FKs for VK- and
// ProviderConfig-owned budgets but never the model-config edge, and
// migrationAddBudgetModelConfigIDColumn added the column without a constraint.
// As a result deleting a model config never cascaded to its multi-budget rows,
// so they leaked (orphaned governance_budgets whose model_config_id points at a
// since-deleted config). This makes that cleanup structurally sound at the DB
// level, underneath the existing application-level cleanup in
// DeleteModelConfigsForScope/DeleteModelConfig. (The single owned rate-limit is
// intentionally left to application cleanup — rate-limits use the opposite
// owner.rate_limit_id convention, so reversing it just for model configs would
// introduce a one-off ownership split for a one-row-per-config leak surface.)
func migrationAddModelConfigBudgetsFKConstraint(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_model_config_budgets_fk_constraint"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()

			// Pre-clean: budgets whose model_config_id already references a
			// missing config would violate the FK we're about to add and block
			// its creation. They are exactly the rows the cascade would have
			// removed, so delete them — but only when nothing live still
			// references them via the legacy governance_model_configs.budget_id
			// (a NO ACTION FK), so this DELETE can't trip that constraint.
			if err := tx.Exec(`
				DELETE FROM governance_budgets
				WHERE model_config_id IS NOT NULL
				  AND model_config_id NOT IN (SELECT id FROM governance_model_configs)
				  AND id NOT IN (
				      SELECT budget_id FROM governance_model_configs WHERE budget_id IS NOT NULL
				  )
			`).Error; err != nil {
				return fmt.Errorf("failed to pre-clean orphaned model-config budgets: %w", err)
			}

			// Create the cascade FK (no-op if a prior fresh-DB migrate already made it).
			if !mg.HasConstraint(&tables.TableModelConfig{}, "Budgets") {
				if err := mg.CreateConstraint(&tables.TableModelConfig{}, "Budgets"); err != nil {
					return fmt.Errorf("failed to create FK constraint for ModelConfig -> Budgets: %w", err)
				}
			}
			return nil
		},
		// Partially non-rollbackable: dropping the FK restores the previous
		// schema, but the orphaned governance_budgets rows removed by the
		// pre-clean are gone permanently. That loss is intentional — they were
		// exactly the dead rows the missing cascade had leaked — so the schema
		// rollback below is still provided rather than hard-failing.
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			if mg.HasConstraint(&tables.TableModelConfig{}, "Budgets") {
				if err := mg.DropConstraint(&tables.TableModelConfig{}, "Budgets"); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	// SQLite workaround — same reasoning as migrationAddMultiBudgetTables:
	// CreateConstraint rebuilds governance_budgets via DROP+RENAME inside a
	// transaction, which fails while other tables hold FKs into it and
	// foreign_keys is ON. PRAGMA foreign_keys can't change inside a transaction,
	// so disable it (pinned to one connection) before the migrator opens its tx.
	// Postgres supports ALTER TABLE ADD CONSTRAINT natively and needs none of this.
	if db.Dialector.Name() == "sqlite" {
		sqlDB, err := db.DB()
		if err != nil {
			return fmt.Errorf("failed to get underlying sql.DB: %w", err)
		}
		prevMaxOpenConns := sqlDB.Stats().MaxOpenConnections
		sqlDB.SetMaxOpenConns(1)
		defer sqlDB.SetMaxOpenConns(prevMaxOpenConns)

		if err := db.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
			return fmt.Errorf("failed to disable SQLite foreign keys: %w", err)
		}
		defer func() {
			if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
				log.Fatalf("[Migration] FATAL: failed to re-enable SQLite foreign keys: %v", err)
			}
		}()
	}
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_model_config_budgets_fk_constraint migration: %s", err.Error())
	}
	return nil
}

// migrationAddPerUserOAuthTables adds the oauth_user_sessions and oauth_user_tokens tables
func migrationAddPerUserOAuthTables(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_per_user_oauth_tables"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			if !mg.HasTable(&tables.TableOauthUserToken{}) {
				logger.Info("[configstore] %s: creating table TableOauthUserToken", migrationName)
				if err := mg.CreateTable(&tables.TableOauthUserToken{}); err != nil {
					return fmt.Errorf("failed to create oauth_user_tokens table: %w", err)
				}
			}
			if !mg.HasTable(&tables.TableOauthUserSession{}) {
				logger.Info("[configstore] %s: creating table TableOauthUserSession", migrationName)
				if err := mg.CreateTable(&tables.TableOauthUserSession{}); err != nil {
					return fmt.Errorf("failed to create oauth_user_sessions table: %w", err)
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			for _, table := range []any{
				&tables.TableOauthUserToken{},
				&tables.TableOauthUserSession{},
			} {
				if mg.HasTable(table) {
					logger.Info("[configstore] %s: dropping table %T", migrationName, table)
					if err := mg.DropTable(table); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_per_user_oauth_tables migration: %s", err.Error())
	}
	return nil
}

// migrationMakeOAuthTokenExpiryNullable makes expires_at nullable for OAuth token tables.
func migrationMakeOAuthTokenExpiryNullable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "make_oauth_token_expiry_nullable"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			if mg.HasTable(&tables.TableOauthToken{}) && mg.HasColumn(&tables.TableOauthToken{}, "expires_at") {
				if err := mg.AlterColumn(&tables.TableOauthToken{}, "ExpiresAt"); err != nil {
					return fmt.Errorf("failed to alter oauth_tokens.expires_at to nullable: %w", err)
				}
			}
			if mg.HasTable(&tables.TableOauthUserToken{}) && mg.HasColumn(&tables.TableOauthUserToken{}, "expires_at") {
				if err := mg.AlterColumn(&tables.TableOauthUserToken{}, "ExpiresAt"); err != nil {
					return fmt.Errorf("failed to alter oauth_user_tokens.expires_at to nullable: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			// Forward-only migration: making expiry nullable is intentionally non-destructive.
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running make_oauth_token_expiry_nullable migration: %s", err.Error())
	}
	return nil
}

// migrationAddAllowPerRequestContentStorageOverrideColumn adds the allow_per_request_content_storage_override column to config_client.
func migrationAddAllowPerRequestContentStorageOverrideColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_allow_per_request_content_storage_override_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "AllowPerRequestContentStorageOverride"); err != nil {
				return fmt.Errorf("failed to add allow_per_request_content_storage_override column: %w", err)
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "allow_per_request_content_storage_override"); err != nil {
				return fmt.Errorf("failed to drop allow_per_request_content_storage_override column: %w", err)
			}

			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running allow_per_request_content_storage_override migration: %s", err.Error())
	}
	return nil
}

// migrationAddAllowPerRequestRawOverrideColumn adds the allow_per_request_raw_override column to config_client.
func migrationAddAllowPerRequestRawOverrideColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_allow_per_request_raw_override_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "AllowPerRequestRawOverride"); err != nil {
				return fmt.Errorf("failed to add allow_per_request_raw_override column: %w", err)
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "allow_per_request_raw_override"); err != nil {
				return fmt.Errorf("failed to drop allow_per_request_raw_override column: %w", err)
			}

			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running allow_per_request_raw_override migration: %s", err.Error())
	}
	return nil
}

// migrationAddMCPClientDiscoveredToolsColumns adds discovered_tools_json and tool_name_mapping_json columns to the mcp_client table
func migrationAddMCPClientDiscoveredToolsColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_mcp_client_discovered_tools_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableMCPClient{}, "discovered_tools_json"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TableMCPClient{}, "tool_name_mapping_json"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableMCPClient{}, "discovered_tools_json"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &tables.TableMCPClient{}, "tool_name_mapping_json"); err != nil {
				return err
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_mcp_client_discovered_tools_columns migration: %s", err.Error())
	}
	return nil
}

// migrationAddPriorityTierPricingColumns adds pricing columns for the 272k token tier
// and the 200k priority variants.
func migrationAddPriorityTierPricingColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_priority_tier_pricing_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			columns := []string{
				"input_cost_per_token_above_272k_tokens",
				"input_cost_per_token_above_272k_tokens_priority",
				"output_cost_per_token_above_272k_tokens",
				"output_cost_per_token_above_272k_tokens_priority",
				"cache_read_input_token_cost_above_272k_tokens",
				"cache_read_input_token_cost_above_272k_tokens_priority",
				"input_cost_per_token_above_200k_tokens_priority",
				"output_cost_per_token_above_200k_tokens_priority",
				"cache_read_input_token_cost_above_200k_tokens_priority",
			}

			for _, field := range columns {
				if err := addColumnIfNotExists(tx, logger, &tables.TableModelPricing{}, field); err != nil {
					return fmt.Errorf("failed to add column %s: %w", field, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			columns := []string{
				"input_cost_per_token_above_272k_tokens",
				"input_cost_per_token_above_272k_tokens_priority",
				"output_cost_per_token_above_272k_tokens",
				"output_cost_per_token_above_272k_tokens_priority",
				"cache_read_input_token_cost_above_272k_tokens",
				"cache_read_input_token_cost_above_272k_tokens_priority",
				"input_cost_per_token_above_200k_tokens_priority",
				"output_cost_per_token_above_200k_tokens_priority",
				"cache_read_input_token_cost_above_200k_tokens_priority",
			}

			for _, field := range columns {
				if err := dropColumnIfExists(tx, logger, &tables.TableModelPricing{}, field); err != nil {
					return fmt.Errorf("failed to drop column %s: %w", field, err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running priority tier pricing columns migration: %s", err.Error())
	}
	return nil
}

// migrationAddFlexTierPricingColumns adds pricing columns for the flex service tier
func migrationAddFlexTierPricingColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_flex_tier_pricing_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			columns := []string{
				"input_cost_per_token_flex",
				"output_cost_per_token_flex",
				"cache_read_input_token_cost_flex",
			}

			for _, field := range columns {
				if err := addColumnIfNotExists(tx, logger, &tables.TableModelPricing{}, field); err != nil {
					return fmt.Errorf("failed to add column %s: %w", field, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			columns := []string{
				"input_cost_per_token_flex",
				"output_cost_per_token_flex",
				"cache_read_input_token_cost_flex",
			}

			for _, field := range columns {
				if err := dropColumnIfExists(tx, logger, &tables.TableModelPricing{}, field); err != nil {
					return fmt.Errorf("failed to drop column %s: %w", field, err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running flex tier pricing columns migration: %s", err.Error())
	}
	return nil
}

// migrationAddFastModePricingColumns adds pricing columns for Anthropic fast mode
// (research preview, speed:"fast" on Opus 4.6/4.7/4.8).
func migrationAddFastModePricingColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_fast_mode_pricing_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			columns := []string{
				"input_cost_per_token_fast",
				"output_cost_per_token_fast",
			}

			for _, field := range columns {
				if err := addColumnIfNotExists(tx, logger, &tables.TableModelPricing{}, field); err != nil {
					return fmt.Errorf("failed to add column %s: %w", field, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			columns := []string{
				"input_cost_per_token_fast",
				"output_cost_per_token_fast",
			}

			for _, field := range columns {
				if err := dropColumnIfExists(tx, logger, &tables.TableModelPricing{}, field); err != nil {
					return fmt.Errorf("failed to drop column %s: %w", field, err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running fast mode pricing columns migration: %s", err.Error())
	}
	return nil
}

// migrationAddFastModeCachePricingColumns adds fast-mode cache pricing columns
// for Anthropic (speed:"fast"). Caching multipliers stack on the fast base input
// rate, so cache tokens need dedicated fast rates instead of the standard ones.
func migrationAddFastModeCachePricingColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_fast_mode_cache_pricing_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	columns := []string{
		"cache_creation_input_token_cost_fast",
		"cache_creation_input_token_cost_above_1hr_fast",
		"cache_read_input_token_cost_fast",
	}
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			for _, field := range columns {
				if err := addColumnIfNotExists(tx, logger, &tables.TableModelPricing{}, field); err != nil {
					return fmt.Errorf("failed to add column %s: %w", field, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			for _, field := range columns {
				if err := dropColumnIfExists(tx, logger, &tables.TableModelPricing{}, field); err != nil {
					return fmt.Errorf("failed to drop column %s: %w", field, err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running fast mode cache pricing columns migration: %s", err.Error())
	}
	return nil
}

// migrationAddInferenceGeoMultiplierColumn adds the inference_geo_us_multiplier
// column for Anthropic data residency (inference_geo:"us" applies a 1.1x
// multiplier stacking on top of all token/cache costs).
func migrationAddInferenceGeoMultiplierColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_inference_geo_multiplier_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	columns := []string{
		"inference_geo_us_multiplier",
	}
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			for _, field := range columns {
				if err := addColumnIfNotExists(tx, logger, &tables.TableModelPricing{}, field); err != nil {
					return fmt.Errorf("failed to add column %s: %w", field, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			for _, field := range columns {
				if err := dropColumnIfExists(tx, logger, &tables.TableModelPricing{}, field); err != nil {
					return fmt.Errorf("failed to drop column %s: %w", field, err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running inference geo multiplier column migration: %s", err.Error())
	}
	return nil
}

// migrationAddFlexAndCacheCreation272kPricingColumns adds the flex 272k-tier
// rates and the OpenAI cache-write (cache-creation) tiered rates introduced with
// gpt-5.6 (flex, priority, and the 272k context tier).
func migrationAddFlexAndCacheCreation272kPricingColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_flex_and_cache_creation_272k_pricing_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	columns := []string{
		"input_cost_per_token_flex_above_272k_tokens",
		"output_cost_per_token_flex_above_272k_tokens",
		"cache_read_input_token_cost_flex_above_272k_tokens",
		"cache_creation_input_token_cost_above_272k_tokens",
		"cache_creation_input_token_cost_flex",
		"cache_creation_input_token_cost_flex_above_272k_tokens",
		"cache_creation_input_token_cost_priority",
	}
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			for _, field := range columns {
				if err := addColumnIfNotExists(tx, logger, &tables.TableModelPricing{}, field); err != nil {
					return fmt.Errorf("failed to add column %s: %w", field, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			for _, field := range columns {
				if err := dropColumnIfExists(tx, logger, &tables.TableModelPricing{}, field); err != nil {
					return fmt.Errorf("failed to drop column %s: %w", field, err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running flex and cache creation 272k pricing columns migration: %s", err.Error())
	}
	return nil
}

// migrationAddWhitelistedRoutesJSONColumn adds the whitelisted_routes_json column to the config_client table
func migrationAddWhitelistedRoutesJSONColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_whitelisted_routes_json_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "WhitelistedRoutesJSON"); err != nil {
				return fmt.Errorf("failed to add whitelisted_routes_json column: %w", err)
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "whitelisted_routes_json"); err != nil {
				return fmt.Errorf("failed to drop whitelisted_routes_json column: %w", err)
			}

			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running whitelisted_routes_json migration: %s", err.Error())
	}
	return nil
}

// migrationReplaceEnableLiteLLMWithCompatColumns replaces the single enable_litellm_fallbacks
// boolean with compat feature columns. If enable_litellm_fallbacks was true,
// only convert_text_to_chat is set to true (preserving the original behavior).
func migrationReplaceEnableLiteLLMWithCompatColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "replace_enable_litellm_with_compat_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mig := tx.Migrator()

			// Add new columns
			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "compat_convert_text_to_chat"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "compat_convert_chat_to_responses"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "compat_should_drop_params"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "compat_should_convert_params"); err != nil {
				return err
			}

			if err := tx.Exec("UPDATE config_client SET compat_should_convert_params = FALSE").Error; err != nil {
				return err
			}

			// Migrate data: if enable_litellm_fallbacks was true, set convert_text_to_chat = true
			if mig.HasColumn(&tables.TableClientConfig{}, "enable_litellm_fallbacks") {
				if err := tx.Exec("UPDATE config_client SET compat_convert_text_to_chat = enable_litellm_fallbacks").Error; err != nil {
					return err
				}
				if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "enable_litellm_fallbacks"); err != nil {
					return err
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mig := tx.Migrator()
			if tx.Migrator().HasColumn(&tables.TableClientConfig{}, "enable_litellm_fallbacks") {
				if err := tx.Exec("ALTER TABLE config_client ADD COLUMN enable_litellm_fallbacks BOOLEAN DEFAULT FALSE").Error; err != nil {
					return err
				}
			}
			if mig.HasColumn(&tables.TableClientConfig{}, "compat_convert_text_to_chat") {
				if err := tx.Exec("UPDATE config_client SET enable_litellm_fallbacks = COALESCE(compat_convert_text_to_chat, FALSE)").Error; err != nil {
					return err
				}
			}
			for _, col := range []string{
				"compat_convert_text_to_chat",
				"compat_convert_chat_to_responses",
				"compat_should_drop_params",
				"compat_should_convert_params",
			} {
				if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, col); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running replace_enable_litellm_with_compat_columns migration: %s", err.Error())
	}
	return nil
}

// migrationDefaultCompatShouldConvertParamsFalse ensures existing deployments
// converge to the new default for compat_should_convert_params. The earlier
// compat migration may already be marked as applied, so changing its body is not
// sufficient for installed databases.
func migrationDefaultCompatShouldConvertParamsFalse(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "default_compat_should_convert_params_false"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mig := tx.Migrator()

			if !mig.HasColumn(&tables.TableClientConfig{}, "compat_should_convert_params") {
				return nil
			}

			if err := tx.Exec("UPDATE config_client SET compat_should_convert_params = FALSE").Error; err != nil {
				return err
			}

			if err := mig.AlterColumn(&tables.TableClientConfig{}, "CompatShouldConvertParams"); err != nil {
				return fmt.Errorf("failed to alter compat_should_convert_params default: %w", err)
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mig := tx.Migrator()

			if !mig.HasColumn(&tables.TableClientConfig{}, "compat_should_convert_params") {
				return nil
			}

			switch tx.Dialector.Name() {
			case "postgres":
				if err := tx.Exec("ALTER TABLE config_client ALTER COLUMN compat_should_convert_params SET DEFAULT FALSE").Error; err != nil {
					return err
				}
			}

			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running default_compat_should_convert_params_false migration: %s", err.Error())
	}
	return nil
}

// migrationAddModelPricingUniqueIndex ensures the composite unique index (model, provider, mode)
// exists on governance_model_pricing so that atomic ON CONFLICT upserts work correctly.
func migrationAddModelPricingUniqueIndex(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_model_pricing_unique_index"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()

			// Remove duplicate rows before creating the unique index.
			// The old find-then-insert path could have produced duplicates on
			// multinode deployments, and CREATE UNIQUE INDEX will fail on a table
			// that still contains them. Keep the row with the lowest ID for each
			// (model, provider, mode) combination.
			result := tx.Exec(`
				DELETE FROM governance_model_pricing
				WHERE id NOT IN (
					SELECT MIN(id)
					FROM governance_model_pricing
					GROUP BY model, provider, mode
				)
			`)
			if result.Error != nil {
				return fmt.Errorf("failed to deduplicate model pricing rows: %w", result.Error)
			}
			if result.RowsAffected > 0 {
				logger.Info("[migration] removed %d duplicate row(s) from governance_model_pricing before creating unique index", result.RowsAffected)
			}

			if !mg.HasIndex(&tables.TableModelPricing{}, "idx_model_provider_mode") {
				logger.Info("[configstore] %s: creating index idx_model_provider_mode on TableModelPricing", migrationName)
				if err := mg.CreateIndex(&tables.TableModelPricing{}, "idx_model_provider_mode"); err != nil {
					return fmt.Errorf("failed to create unique index idx_model_provider_mode: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			if mg.HasIndex(&tables.TableModelPricing{}, "idx_model_provider_mode") {
				logger.Info("[configstore] %s: dropping index idx_model_provider_mode on TableModelPricing", migrationName)
				if err := mg.DropIndex(&tables.TableModelPricing{}, "idx_model_provider_mode"); err != nil {
					return fmt.Errorf("failed to drop unique index idx_model_provider_mode: %w", err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_model_pricing_unique_index migration: %s", err.Error())
	}
	return nil
}

// migrationNormalizeOtelTraceType rewrites the legacy OTEL plugin trace_type value "otel" to "genai_extension".
// No-op if the plugin row is missing or trace_type is already correct.
func migrationNormalizeOtelTraceType(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "normalize_otel_trace_type"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			var plugin tables.TablePlugin
			err := tx.Where("name = ?", "otel").First(&plugin).Error
			if err != nil {
				if err == gorm.ErrRecordNotFound {
					return nil
				}
				return fmt.Errorf("failed to load otel plugin row: %w", err)
			}

			cfgMap, ok := plugin.Config.(map[string]any)
			if !ok || len(cfgMap) == 0 {
				return nil
			}
			if tt, _ := cfgMap["trace_type"].(string); tt != "otel" {
				return nil
			}

			cfgMap["trace_type"] = "genai_extension"
			plugin.Config = cfgMap
			plugin.ConfigJSON = ""
			plugin.EncryptionStatus = tables.EncryptionStatusPlainText

			if err := tx.Save(&plugin).Error; err != nil {
				return fmt.Errorf("failed to save normalized otel config: %w", err)
			}
			logger.Info("[Migration] Normalized otel trace_type 'otel' to 'genai_extension'")
			return nil
		},
		Rollback: func(tx *gorm.DB) error { return nil },
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running normalize_otel_trace_type migration: %s", err.Error())
	}
	return nil
}

// migrateCalendarAlignedToBudgetsAndRateLimitsTable
func migrateCalendarAlignedToBudgetsAndRateLimitsTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "migrate_calendar_aligned"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			// Adding columns first
			budgetsHasCol, err := hasColumn(tx, "governance_budgets", "calendar_aligned")
			if err != nil {
				return fmt.Errorf("failed to introspect governance_budgets for calendar_aligned: %w", err)
			}
			if !budgetsHasCol {
				if err := tx.Exec(`ALTER TABLE governance_budgets ADD COLUMN calendar_aligned BOOLEAN DEFAULT FALSE`).Error; err != nil {
					return fmt.Errorf("failed to add calendar_aligned column to budgets: %w", err)
				}
			}
			// Adding columns first
			rateLimitsHasCol, err := hasColumn(tx, "governance_rate_limits", "calendar_aligned")
			if err != nil {
				return fmt.Errorf("failed to introspect governance_rate_limits for calendar_aligned: %w", err)
			}
			if !rateLimitsHasCol {
				if err := tx.Exec(`ALTER TABLE governance_rate_limits ADD COLUMN calendar_aligned BOOLEAN DEFAULT FALSE`).Error; err != nil {
					return fmt.Errorf("failed to add calendar_aligned column to rate_limits: %w", err)
				}
			}
			// Prefill calendar_aligned for existing budgets and rate_limits attached to virtual keys.
			// Use subquery-based raw SQL (compatible with both PostgreSQL and SQLite) to avoid
			// "cached plan must not change result type" (SQLSTATE 0A000): earlier migrations in
			// the same run added columns to these tables, invalidating pgx's prepared-statement cache.
			if err := tx.Exec(`
				UPDATE governance_rate_limits
				SET calendar_aligned = true
				WHERE id IN (
					SELECT rate_limit_id FROM governance_virtual_keys
					WHERE calendar_aligned = true AND rate_limit_id IS NOT NULL
				)
			`).Error; err != nil {
				return fmt.Errorf("failed to propagate calendar_aligned to rate limits: %w", err)
			}
			if err := tx.Exec(`
				UPDATE governance_budgets
				SET calendar_aligned = true
				WHERE virtual_key_id IN (
					SELECT id FROM governance_virtual_keys WHERE calendar_aligned = true
				)
			`).Error; err != nil {
				return fmt.Errorf("failed to propagate calendar_aligned to budgets: %w", err)
			}
			logger.Info("[Migration] Prefilled calendar_aligned field for existing budgets and rate limits")
			return nil
		},
		Rollback: func(tx *gorm.DB) error { return nil },
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running migrate_calendar_aligned migration: %s", err.Error())
	}
	return nil
}

func migrationAddOCRPricingColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_ocr_pricing_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			columns := []string{
				"ocr_cost_per_page",
				"annotation_cost_per_page",
			}
			for _, field := range columns {
				if err := addColumnIfNotExists(tx, logger, &tables.TableModelPricing{}, field); err != nil {
					return fmt.Errorf("failed to add column %s: %w", field, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			columns := []string{
				"ocr_cost_per_page",
				"annotation_cost_per_page",
			}
			for _, field := range columns {
				if err := dropColumnIfExists(tx, logger, &tables.TableModelPricing{}, field); err != nil {
					return fmt.Errorf("failed to drop column %s: %w", field, err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_ocr_pricing_columns migration: %s", err.Error())
	}
	return nil
}

func migrationAddMCPExternalBaseURLColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_mcp_external_base_url_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			// Use raw SQL — the Go field for this column has since been split into
			// MCPExternalServerURL/MCPExternalClientURL by a follow-up migration, so
			// we can no longer reference the original field name on the struct.
			if !mg.HasColumn(&tables.TableClientConfig{}, "mcp_external_base_url") {
				if err := tx.Exec("ALTER TABLE config_client ADD COLUMN mcp_external_base_url VARCHAR(512)").Error; err != nil {
					return fmt.Errorf("failed to add mcp_external_base_url column: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			if mg.HasColumn(&tables.TableClientConfig{}, "mcp_external_base_url") {
				if err := tx.Exec("ALTER TABLE config_client DROP COLUMN mcp_external_base_url").Error; err != nil {
					return fmt.Errorf("failed to drop mcp_external_base_url column: %w", err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_mcp_external_base_url_column migration: %s", err.Error())
	}
	return nil
}

func migrationSplitMCPExternalBaseURL(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "split_mcp_external_base_url_into_server_client"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			// Use raw SQL — MCPExternalServerURL was removed from
			// TableClientConfig in a later refactor, so mg.AddColumn (which
			// looks up the column type from the struct tag) can no longer
			// find the field on a fresh upgrade. Existing deployments that
			// already ran this migration take the fast no-op path via
			// HasColumn.
			if !mg.HasColumn(&tables.TableClientConfig{}, "mcp_external_server_url") {
				if err := tx.Exec("ALTER TABLE config_client ADD COLUMN mcp_external_server_url VARCHAR(512)").Error; err != nil {
					return fmt.Errorf("failed to add mcp_external_server_url column: %w", err)
				}
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TableClientConfig{}, "MCPExternalClientURL"); err != nil {
				return fmt.Errorf("failed to add mcp_external_client_url column: %w", err)
			}
			// Backfill: existing deployments treated mcp_external_base_url as applying
			// to both roles, so copy it into both new columns to preserve behavior.
			if mg.HasColumn(&tables.TableClientConfig{}, "mcp_external_base_url") {
				if err := tx.Exec(
					"UPDATE config_client SET mcp_external_server_url = mcp_external_base_url, mcp_external_client_url = mcp_external_base_url WHERE mcp_external_base_url IS NOT NULL AND mcp_external_base_url != ''",
				).Error; err != nil {
					return fmt.Errorf("failed to backfill mcp_external_*_url columns: %w", err)
				}
				if err := tx.Exec("ALTER TABLE config_client DROP COLUMN mcp_external_base_url").Error; err != nil {
					return fmt.Errorf("failed to drop mcp_external_base_url column: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			if !mg.HasColumn(&tables.TableClientConfig{}, "mcp_external_base_url") {
				if err := tx.Exec("ALTER TABLE config_client ADD COLUMN mcp_external_base_url VARCHAR(512)").Error; err != nil {
					return fmt.Errorf("failed to recreate mcp_external_base_url column: %w", err)
				}
			}
			if mg.HasColumn(&tables.TableClientConfig{}, "mcp_external_server_url") {
				if err := tx.Exec(
					"UPDATE config_client SET mcp_external_base_url = mcp_external_server_url WHERE mcp_external_server_url IS NOT NULL AND mcp_external_server_url != ''",
				).Error; err != nil {
					return fmt.Errorf("failed to backfill mcp_external_base_url from mcp_external_server_url: %w", err)
				}
				if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "mcp_external_server_url"); err != nil {
					return fmt.Errorf("failed to drop mcp_external_server_url column: %w", err)
				}
			}
			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "mcp_external_client_url"); err != nil {
				return fmt.Errorf("failed to drop mcp_external_client_url column: %w", err)
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running split_mcp_external_base_url_into_server_client migration: %s", err.Error())
	}
	return nil
}

// migrationAddMCPClientDisabledColumn adds the disabled column to the config_mcp_clients table
func migrationAddMCPClientDisabledColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_mcp_client_disabled_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			if !mg.HasColumn(&tables.TableMCPClient{}, "disabled") {
				if err := addColumnIfNotExists(tx, logger, &tables.TableMCPClient{}, "disabled"); err != nil {
					return fmt.Errorf("failed to add disabled column: %w", err)
				}
				// Initialize existing rows with false (default value)
				if err := tx.Exec("UPDATE config_mcp_clients SET disabled = false WHERE disabled IS NULL").Error; err != nil {
					return fmt.Errorf("failed to initialize disabled column: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableMCPClient{}, "disabled"); err != nil {
				return fmt.Errorf("failed to drop disabled column: %w", err)
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_mcp_client_disabled_column migration: %s", err.Error())
	}
	return nil
}

// migrationUniqueTeamNames deduplicates governance_teams.name and adds a unique
// index. Duplicate rows (same name, different ID) have a short UUID suffix
// appended so no data is lost. The struct tag uniqueIndex makes GORM enforce
// this on new rows going forward.
func migrationUniqueTeamNames(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "gov_unique_team_names"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	return RunSingleMigration(ctx, nil, db, logger, &migrator.Migration{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			// Find all names that appear more than once.
			type dupRow struct {
				Name string
			}
			var dups []dupRow
			if err := tx.Raw(`
				SELECT name FROM governance_teams
				GROUP BY name HAVING COUNT(*) > 1
			`).Scan(&dups).Error; err != nil {
				return fmt.Errorf("find duplicate team names: %w", err)
			}

			logger.Info("[configstore] %s: processing %d dups", migrationName, len(dups))
			for _, d := range dups {
				// oldest row keeps the original name
				type row struct {
					ID string
				}
				var rows []row
				if err := tx.Raw(`
					SELECT id FROM governance_teams
					WHERE name = ?
					ORDER BY created_at ASC, id ASC
				`, d.Name).Scan(&rows).Error; err != nil {
					return fmt.Errorf("fetch duplicates for %q: %w", d.Name, err)
				}
				for _, r := range rows[1:] {
					newName := d.Name + "-" + r.ID[:8]
					if err := tx.Exec(`UPDATE governance_teams SET name = ? WHERE id = ?`, newName, r.ID).Error; err != nil {
						return fmt.Errorf("rename duplicate team %s: %w", r.ID, err)
					}
				}
			}

			// Add the unique index. Skip if it already exists.
			if !tx.Migrator().HasIndex(&tables.TableTeam{}, "idx_governance_teams_name") {
				logger.Info("[configstore] %s: creating index Name on TableTeam", migrationName)
				if err := tx.Migrator().CreateIndex(&tables.TableTeam{}, "Name"); err != nil {
					return fmt.Errorf("create unique index on governance_teams.name: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			logger.Info("[configstore] %s: dropping index idx_governance_teams_name on TableTeam", migrationName)
			_ = tx.Migrator().DropIndex(&tables.TableTeam{}, "idx_governance_teams_name")
			return nil
		},
	})
}

// migrationAddOAuthAuthModeColumns adds the AuthMode/Status/FlowMode discriminator
// columns to the per-user OAuth tables, backfills them from existing identity
// column population, drops the legacy non-unique composite indexes on
// oauth_user_tokens, and creates partial unique indexes (one per identity
// dimension).
func migrationAddOAuthAuthModeColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_oauth_auth_mode_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	return RunSingleMigration(ctx, nil, db, logger, &migrator.Migration{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()

			// 1) oauth_user_tokens: add status + auth_mode
			if mg.HasTable(&tables.TableOauthUserToken{}) {
				if err := addColumnIfNotExists(tx, logger, &tables.TableOauthUserToken{}, "Status"); err != nil {
					return fmt.Errorf("add status to oauth_user_tokens: %w", err)
				}
				if !mg.HasColumn(&tables.TableOauthUserToken{}, "auth_mode") {
					logger.Info("[configstore] %s: adding column auth_mode to TableOauthUserToken", migrationName)
					// Add as nullable first — mg.AddColumn would derive
					// NOT NULL from the struct tag and fail on tables with
					// existing rows. The backfill below populates every
					// row, then we tighten to NOT NULL on Postgres.
					if err := tx.Exec(`ALTER TABLE oauth_user_tokens ADD COLUMN auth_mode VARCHAR(20)`).Error; err != nil {
						return fmt.Errorf("add auth_mode to oauth_user_tokens: %w", err)
					}
				}
				// Backfill auth_mode from whichever identity column is populated.
				// Priority: user_id > virtual_key_id > session column. The session
				// column was renamed in a later migration (session_token_hash →
				// session_id), so the SQL has to adapt: a fresh DB starts on the
				// new schema and never had session_token_hash; an existing DB
				// hasn't been through the rename yet and still has it.
				sessionCol := ""
				switch {
				case mg.HasColumn(&tables.TableOauthUserToken{}, "session_token_hash"):
					sessionCol = "session_token_hash"
				case mg.HasColumn(&tables.TableOauthUserToken{}, "session_id"):
					sessionCol = "session_id"
				}
				var backfillSQL string
				// Priority is vk → user → session. vk wins over user so that a
				// legacy row carrying both identity columns survives as a
				// vk-mode binding through migrationDropNonVKOauthUserRows,
				// which is the binding type this refactor is meant to keep.
				if sessionCol != "" {
					backfillSQL = `
						UPDATE oauth_user_tokens
						SET auth_mode = CASE
							WHEN virtual_key_id IS NOT NULL AND virtual_key_id != '' THEN 'vk'
							WHEN user_id IS NOT NULL AND user_id != '' THEN 'user'
							WHEN ` + sessionCol + ` IS NOT NULL AND ` + sessionCol + ` != '' THEN 'session'
							ELSE NULL
						END
					`
				} else {
					backfillSQL = `
						UPDATE oauth_user_tokens
						SET auth_mode = CASE
							WHEN virtual_key_id IS NOT NULL AND virtual_key_id != '' THEN 'vk'
							WHEN user_id IS NOT NULL AND user_id != '' THEN 'user'
							ELSE NULL
						END
					`
				}
				if err := tx.Exec(backfillSQL).Error; err != nil {
					return fmt.Errorf("backfill oauth_user_tokens.auth_mode: %w", err)
				}
				// Identity-less rows can't satisfy any (mode, identity, mcp_client)
				// lookup, so they're dead data. Drop them now — leaving them as
				// 'vk' would let them sneak past migrationDropNonVKOauthUserRows
				// later in the chain and pollute the sessions UI.
				if err := tx.Exec(`DELETE FROM oauth_user_tokens WHERE auth_mode IS NULL`).Error; err != nil {
					return fmt.Errorf("delete identity-less oauth_user_tokens rows: %w", err)
				}
				// Tighten the constraint on Postgres now that every row has
				// a value. SQLite doesn't support ALTER COLUMN SET NOT NULL;
				// app-level validation (BeforeSave) enforces non-empty
				// AuthMode there instead.
				if tx.Dialector.Name() == "postgres" {
					if err := tx.Exec(`ALTER TABLE oauth_user_tokens ALTER COLUMN auth_mode SET NOT NULL`).Error; err != nil {
						return fmt.Errorf("set oauth_user_tokens.auth_mode NOT NULL: %w", err)
					}
				}
				// Ensure status is populated for legacy rows.
				if err := tx.Exec(`UPDATE oauth_user_tokens SET status = 'active' WHERE status IS NULL OR status = ''`).Error; err != nil {
					return fmt.Errorf("backfill oauth_user_tokens.status: %w", err)
				}

				// Drop legacy non-unique composite indexes. Replaced by partial unique indexes below.
				if mg.HasIndex(&tables.TableOauthUserToken{}, "idx_vk_mcp") {
					logger.Info("[configstore] %s: dropping index idx_vk_mcp on TableOauthUserToken", migrationName)
					if err := mg.DropIndex(&tables.TableOauthUserToken{}, "idx_vk_mcp"); err != nil {
						return fmt.Errorf("drop idx_vk_mcp: %w", err)
					}
				}
				if mg.HasIndex(&tables.TableOauthUserToken{}, "idx_user_mcp") {
					logger.Info("[configstore] %s: dropping index idx_user_mcp on TableOauthUserToken", migrationName)
					if err := mg.DropIndex(&tables.TableOauthUserToken{}, "idx_user_mcp"); err != nil {
						return fmt.Errorf("drop idx_user_mcp: %w", err)
					}
				}

				// Dedupe legacy bindings before flipping the indexes to UNIQUE.
				// idx_user_mcp / idx_vk_mcp were non-unique up to this migration,
				// and the OAuth-server consent flow we ripped out could write
				// multiple rows for the same (identity, mcp_client_id). Without
				// this pass the CREATE UNIQUE INDEX below would abort on the
				// first duplicate. Keep the newest row per group (highest
				// updated_at, breaking ties on id) so the live credential
				// survives and stale duplicates are deleted.
				dedupeStmts := []string{
					`DELETE FROM oauth_user_tokens
						WHERE id IN (
							SELECT id FROM (
								SELECT id,
									ROW_NUMBER() OVER (
										PARTITION BY user_id, mcp_client_id
										ORDER BY updated_at DESC, id DESC
									) AS rn
								FROM oauth_user_tokens
								WHERE auth_mode = 'user' AND user_id IS NOT NULL AND user_id != ''
							) ranked
							WHERE rn > 1
						)`,
					`DELETE FROM oauth_user_tokens
						WHERE id IN (
							SELECT id FROM (
								SELECT id,
									ROW_NUMBER() OVER (
										PARTITION BY virtual_key_id, mcp_client_id
										ORDER BY updated_at DESC, id DESC
									) AS rn
								FROM oauth_user_tokens
								WHERE auth_mode = 'vk' AND virtual_key_id IS NOT NULL AND virtual_key_id != ''
							) ranked
							WHERE rn > 1
						)`,
				}
				if sessionCol != "" {
					dedupeStmts = append(dedupeStmts,
						`DELETE FROM oauth_user_tokens
							WHERE id IN (
								SELECT id FROM (
									SELECT id,
										ROW_NUMBER() OVER (
											PARTITION BY `+sessionCol+`, mcp_client_id
											ORDER BY updated_at DESC, id DESC
										) AS rn
									FROM oauth_user_tokens
									WHERE auth_mode = 'session' AND `+sessionCol+` IS NOT NULL AND `+sessionCol+` != ''
								) ranked
								WHERE rn > 1
							)`,
					)
				}
				logger.Info("[configstore] %s: processing %d dedupeStmts", migrationName, len(dedupeStmts))
				for _, stmt := range dedupeStmts {
					if err := tx.Exec(stmt).Error; err != nil {
						return fmt.Errorf("dedupe legacy oauth_user_tokens bindings: %w", err)
					}
				}

				// Partial unique indexes (one per identity dimension), each
				// scoped by auth_mode to match the dedupe pass above. Without
				// the auth_mode predicate, a legacy row that carries both
				// (e.g.) user_id and virtual_key_id would participate in two
				// uniqueness domains and could collide with rows that own a
				// different mode. Both Postgres and SQLite (3.8+) support
				// WHERE on indexes. Use whichever session column exists
				// (legacy session_token_hash vs renamed session_id);
				// migrationReplaceOauthSessionTokenWithSessionID later drops +
				// recreates the session index on session_id anyway.
				partialUniques := []string{
					`CREATE UNIQUE INDEX IF NOT EXISTS idx_oauth_user_tokens_user_mcp
						ON oauth_user_tokens (user_id, mcp_client_id)
						WHERE auth_mode = 'user' AND user_id IS NOT NULL AND user_id != ''`,
					`CREATE UNIQUE INDEX IF NOT EXISTS idx_oauth_user_tokens_vk_mcp
						ON oauth_user_tokens (virtual_key_id, mcp_client_id)
						WHERE auth_mode = 'vk' AND virtual_key_id IS NOT NULL AND virtual_key_id != ''`,
				}
				if sessionCol != "" {
					partialUniques = append(partialUniques,
						`CREATE UNIQUE INDEX IF NOT EXISTS idx_oauth_user_tokens_session_mcp
							ON oauth_user_tokens (`+sessionCol+`, mcp_client_id)
							WHERE auth_mode = 'session' AND `+sessionCol+` IS NOT NULL AND `+sessionCol+` != ''`,
					)
				}
				logger.Info("[configstore] %s: processing %d partialUniques", migrationName, len(partialUniques))
				for _, stmt := range partialUniques {
					if err := tx.Exec(stmt).Error; err != nil {
						return fmt.Errorf("create partial unique index on oauth_user_tokens: %w", err)
					}
				}

				// Partial index on orphaned rows to keep the sessions-tab "orphaned" query cheap.
				if err := tx.Exec(`
					CREATE INDEX IF NOT EXISTS idx_oauth_user_tokens_orphaned
						ON oauth_user_tokens (status)
						WHERE status = 'orphaned'
				`).Error; err != nil {
					return fmt.Errorf("create orphaned-status index: %w", err)
				}
			}

			// 2) oauth_user_sessions: add flow_mode
			if mg.HasTable(&tables.TableOauthUserSession{}) {
				if err := addColumnIfNotExists(tx, logger, &tables.TableOauthUserSession{}, "FlowMode"); err != nil {
					return fmt.Errorf("add flow_mode to oauth_user_sessions: %w", err)
				}
				// Same vk → user precedence as the token backfill above so
				// migrationDropNonVKOauthUserRows preserves dual-identity rows.
				if err := tx.Exec(`
					UPDATE oauth_user_sessions
					SET flow_mode = CASE
						WHEN virtual_key_id IS NOT NULL AND virtual_key_id != '' THEN 'vk'
						WHEN user_id IS NOT NULL AND user_id != '' THEN 'user'
						ELSE 'none'
					END
				`).Error; err != nil {
					return fmt.Errorf("backfill oauth_user_sessions.flow_mode: %w", err)
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()

			// Drop partial unique + orphan indexes.
			for _, name := range []string{
				"idx_oauth_user_tokens_user_mcp",
				"idx_oauth_user_tokens_vk_mcp",
				"idx_oauth_user_tokens_session_mcp",
				"idx_oauth_user_tokens_orphaned",
			} {
				if err := tx.Exec("DROP INDEX IF EXISTS " + name).Error; err != nil {
					return fmt.Errorf("drop %s: %w", name, err)
				}
			}

			// Restore legacy composite non-unique indexes.
			if mg.HasTable(&tables.TableOauthUserToken{}) {
				if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_vk_mcp ON oauth_user_tokens (virtual_key_id, mcp_client_id)`).Error; err != nil {
					return fmt.Errorf("restore idx_vk_mcp: %w", err)
				}
				if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_user_mcp ON oauth_user_tokens (user_id, mcp_client_id)`).Error; err != nil {
					return fmt.Errorf("restore idx_user_mcp: %w", err)
				}
				if err := dropColumnIfExists(tx, logger, &tables.TableOauthUserToken{}, "Status"); err != nil {
					return fmt.Errorf("drop status from oauth_user_tokens: %w", err)
				}
				if err := dropColumnIfExists(tx, logger, &tables.TableOauthUserToken{}, "AuthMode"); err != nil {
					return fmt.Errorf("drop auth_mode from oauth_user_tokens: %w", err)
				}
			}
			if mg.HasTable(&tables.TableOauthUserSession{}) && mg.HasColumn(&tables.TableOauthUserSession{}, "flow_mode") {
				if err := dropColumnIfExists(tx, logger, &tables.TableOauthUserSession{}, "FlowMode"); err != nil {
					return fmt.Errorf("drop flow_mode from oauth_user_sessions: %w", err)
				}
			}

			return nil
		},
	})
}

// migrationReplaceOauthSessionTokenWithSessionID switches both
// oauth_user_sessions and oauth_user_tokens from the legacy SessionToken +
// SessionTokenHash pair (server-issued bearer credential, SHA-256 hashed for
// the lookup column) to a single plaintext SessionID column.
//
// Why the change: session-mode identity is now caller-asserted via the
// x-bf-mcp-session-id header (same trust model as a VK value). It's no longer
// a Bifrost-issued bearer token, so hashing buys nothing and the unique index
// on session_token_hash was conflating "uniqueness of token value" with
// "uniqueness of binding" — the latter is now enforced at the application
// layer by the (mode, identity, mcp_client_id) lookup in
// InitiateUserOAuthFlow and CreateOauthUserToken.
//
// Order: add SessionID first, then drop the legacy columns + their indexes.
// No data backfill: existing rows are dev-only test data; production hasn't
// landed yet.
func migrationReplaceOauthSessionTokenWithSessionID(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "replace_oauth_session_token_with_session_id"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	return RunSingleMigration(ctx, nil, db, logger, &migrator.Migration{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()

			// 1) oauth_user_sessions: add session_id, drop legacy columns + index.
			if mg.HasTable(&tables.TableOauthUserSession{}) {
				if err := addColumnIfNotExists(tx, logger, &tables.TableOauthUserSession{}, "SessionID"); err != nil {
					return fmt.Errorf("add session_id to oauth_user_sessions: %w", err)
				}
				// The legacy session_token_hash column had a uniqueIndex declared
				// via gorm tag; GORM names it something like
				// "idx_oauth_user_sessions_session_token_hash". DROP COLUMN drops
				// the dependent index on Postgres/SQLite, but be explicit on
				// Postgres for safety.
				if err := tx.Exec("DROP INDEX IF EXISTS idx_oauth_user_sessions_session_token_hash").Error; err != nil {
					return fmt.Errorf("drop legacy session_token_hash index on oauth_user_sessions: %w", err)
				}
				if err := dropColumnIfExists(tx, logger, &tables.TableOauthUserSession{}, "session_token_hash"); err != nil {
					return fmt.Errorf("drop session_token_hash from oauth_user_sessions: %w", err)
				}
				if err := dropColumnIfExists(tx, logger, &tables.TableOauthUserSession{}, "session_token"); err != nil {
					return fmt.Errorf("drop session_token from oauth_user_sessions: %w", err)
				}
			}

			// 2) oauth_user_tokens: same dance plus drop the partial unique index
			// idx_oauth_user_tokens_session_mcp created by the prior migration —
			// its key column (session_token_hash) is being removed.
			if mg.HasTable(&tables.TableOauthUserToken{}) {
				if err := addColumnIfNotExists(tx, logger, &tables.TableOauthUserToken{}, "SessionID"); err != nil {
					return fmt.Errorf("add session_id to oauth_user_tokens: %w", err)
				}
				if err := tx.Exec("DROP INDEX IF EXISTS idx_oauth_user_tokens_session_mcp").Error; err != nil {
					return fmt.Errorf("drop legacy idx_oauth_user_tokens_session_mcp: %w", err)
				}
				if err := tx.Exec("DROP INDEX IF EXISTS idx_oauth_user_tokens_session_token_hash").Error; err != nil {
					return fmt.Errorf("drop legacy session_token_hash index on oauth_user_tokens: %w", err)
				}
				if err := dropColumnIfExists(tx, logger, &tables.TableOauthUserToken{}, "session_token_hash"); err != nil {
					return fmt.Errorf("drop session_token_hash from oauth_user_tokens: %w", err)
				}
				if err := dropColumnIfExists(tx, logger, &tables.TableOauthUserToken{}, "session_token"); err != nil {
					return fmt.Errorf("drop session_token from oauth_user_tokens: %w", err)
				}

				// Replace the partial unique index with one keyed on the new
				// column. Scope by auth_mode = 'session' to match the
				// mode-scoped uniqueness set up for user / vk in
				// migrationAddOAuthAuthModeColumns; without it, a row that
				// carries both session_id and another identity column could
				// participate in two uniqueness domains.
				if err := tx.Exec(`
					CREATE UNIQUE INDEX IF NOT EXISTS idx_oauth_user_tokens_session_mcp
						ON oauth_user_tokens (session_id, mcp_client_id)
						WHERE auth_mode = 'session' AND session_id IS NOT NULL AND session_id != ''
				`).Error; err != nil {
					return fmt.Errorf("create idx_oauth_user_tokens_session_mcp on session_id: %w", err)
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()

			// Drop the new partial unique index keyed on session_id.
			if err := tx.Exec("DROP INDEX IF EXISTS idx_oauth_user_tokens_session_mcp").Error; err != nil {
				return fmt.Errorf("drop idx_oauth_user_tokens_session_mcp: %w", err)
			}

			// Re-add legacy columns via raw DDL. The typed struct fields no
			// longer exist, so we can't use mg.AddColumn. Best-effort
			// schema-only rollback; original session tokens are gone, so
			// callers depending on this data would need their own recovery.
			for _, tableName := range []string{"oauth_user_sessions", "oauth_user_tokens"} {
				if err := tx.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN session_token VARCHAR(255)", tableName)).Error; err != nil {
					// Ignore "column already exists" / dialect quirks.
					_ = err
				}
				if err := tx.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN session_token_hash VARCHAR(64)", tableName)).Error; err != nil {
					_ = err
				}
			}
			for _, model := range []interface{}{&tables.TableOauthUserSession{}, &tables.TableOauthUserToken{}} {
				if mg.HasTable(model) && mg.HasColumn(model, "session_id") {
					if err := dropColumnIfExists(tx, logger, model, "SessionID"); err != nil {
						return fmt.Errorf("drop session_id from %T: %w", model, err)
					}
				}
			}

			return nil
		},
	})
}

// migrationDropLegacyOAuthServerTables drops the four tables that backed the
// MCP-gateway-OAuth-server flow (Bifrost acting as an OAuth Authorization
// Server to upstream MCP clients), plus the gateway_session_id column on
// oauth_user_sessions that linked them. Bifrost is now strictly an OAuth
// *client* to upstream providers; the server-side flow was replaced by the
// x-bf-mcp-session-id header model (see migrationReplaceOauthSessionTokenWithSessionID).
//
// Tables removed:
//   - oauth_per_user_clients       (dynamic client registration)
//   - oauth_per_user_sessions      (Bifrost-issued bearer tokens for MCP clients)
//   - oauth_per_user_codes         (authorization codes)
//   - oauth_per_user_pending_flows (in-flight consent state)
//
// No data preservation needed: the flow that wrote these rows no longer exists
// in the code, so any rows present are orphans referring to a removed code path.
// Rollback recreates the tables empty (best-effort schema-only; original data
// is gone).
func migrationDropLegacyOAuthServerTables(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "drop_legacy_oauth_server_tables"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	return RunSingleMigration(ctx, nil, db, logger, &migrator.Migration{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			// Drop the gateway_session_id column on oauth_user_sessions first,
			// since the table dropped below (oauth_per_user_sessions) was its
			// logical pairing. DROP COLUMN with IF EXISTS is supported by
			// Postgres but not SQLite — fall back to checking via Migrator.
			mg := tx.Migrator()
			if mg.HasTable("oauth_user_sessions") && mg.HasColumn("oauth_user_sessions", "gateway_session_id") {
				if err := tx.Exec("ALTER TABLE oauth_user_sessions DROP COLUMN gateway_session_id").Error; err != nil {
					return fmt.Errorf("drop gateway_session_id from oauth_user_sessions: %w", err)
				}
			}

			for _, table := range []string{
				"oauth_per_user_codes",
				"oauth_per_user_pending_flows",
				"oauth_per_user_sessions",
				"oauth_per_user_clients",
			} {
				if err := tx.Exec("DROP TABLE IF EXISTS " + table).Error; err != nil {
					return fmt.Errorf("drop %s: %w", table, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			// Irreversible. The four oauth_per_user_* tables backed the
			// Bifrost-as-OAuth-server flow; their original schemas (dynamic
			// client metadata, hashed bearer tokens, authorization codes,
			// PKCE state) cannot be reconstructed from the live oauth_user_*
			// tables. A "best-effort" stub Rollback that re-creates empty
			// tables with just an id column would let downgrades report
			// success against a schema the pre-refactor code path cannot use,
			// and the application would then fail on first access. Fail loud
			// instead so the operator knows a true rollback requires a backup
			// restore.
			return fmt.Errorf("drop_legacy_oauth_server_tables is irreversible: restore from backup to recover the pre-refactor schema")
		},
	})
}

// migrationDropNonVKOauthUserRows hard-deletes user-mode and session-mode rows
// from oauth_user_tokens and oauth_user_sessions. Only vk-mode rows survive the
// refactor: their (virtual_key, mcp_client) binding is stable across the change.
// User-mode and session-mode semantics changed when Bifrost stopped acting as
// an OAuth server (see migrationDropLegacyOAuthServerTables); the prior rows
// are stale credentials that wouldn't satisfy a lookup under the new flow.
// Affected users / clients re-authenticate fresh — the alternative is carrying
// forward dead state that surfaces in the sessions UI but can never refresh.
func migrationDropNonVKOauthUserRows(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "drop_non_vk_oauth_user_rows"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	return RunSingleMigration(ctx, nil, db, logger, &migrator.Migration{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			// `!= 'vk'` alone doesn't match NULL in SQL three-valued logic.
			// Rolling-upgrade writes from older replicas, or any row that
			// somehow slipped past the auth_mode/flow_mode backfill, could
			// still carry a NULL discriminator — explicitly include those so
			// stale user/session state can't survive the cleanup.
			if mg.HasTable("oauth_user_tokens") {
				if err := tx.Exec("DELETE FROM oauth_user_tokens WHERE auth_mode IS NULL OR auth_mode != 'vk'").Error; err != nil {
					return fmt.Errorf("delete non-vk oauth_user_tokens: %w", err)
				}
			}
			if mg.HasTable("oauth_user_sessions") {
				if err := tx.Exec("DELETE FROM oauth_user_sessions WHERE flow_mode IS NULL OR flow_mode != 'vk'").Error; err != nil {
					return fmt.Errorf("delete non-vk oauth_user_sessions: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			// No-op: the dropped rows are unrecoverable. Users on user/session
			// modes re-authenticate after rollback the same way they would
			// after the forward migration.
			return nil
		},
	})
}

// migrationDropMCPExternalServerURL drops the mcp_external_server_url column
// from config_client. This URL was used to advertise Bifrost as an OAuth
// authorization server (.well-known endpoints, WWW-Authenticate header on
// /mcp). Bifrost no longer acts as an OAuth server (see
// migrationDropLegacyOAuthServerTables), so the column is dead.
//
// Hash recompute is handled separately by
// migrationRefreshConfigHashAfterMCPExternalServerURLRemoval so this migration
// holds the AccessExclusiveLock only for the brief catalog update.
//
// The companion mcp_external_client_url column is retained — it's still used
// as the redirect_uri base when Bifrost acts as an OAuth *client* to upstream
// MCP servers.
func migrationDropMCPExternalServerURL(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "drop_mcp_external_server_url_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	return RunSingleMigration(ctx, nil, db, logger, &migrator.Migration{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			if mg.HasTable("config_client") && mg.HasColumn("config_client", "mcp_external_server_url") {
				if err := tx.Exec("ALTER TABLE config_client DROP COLUMN mcp_external_server_url").Error; err != nil {
					return fmt.Errorf("drop mcp_external_server_url from config_client: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			// Best-effort: re-add the column (empty values; no data to restore).
			_ = tx.Exec("ALTER TABLE config_client ADD COLUMN mcp_external_server_url VARCHAR(512)").Error
			return nil
		},
	})
}

// migrationRefreshConfigHashAfterMCPExternalServerURLRemoval recomputes
// config_hash for every config_client row after MCPExternalServerURL was
// removed from GenerateClientConfigHash. Without this refresh, every stored
// hash that was computed while the field was still mixed in would mismatch
// on first startup and trigger spurious config-reload cycles.
//
// The actual DROP COLUMN is handled by migrationDropMCPExternalServerURL so
// that the DDL (AccessExclusiveLock) never shares a transaction with the
// SELECT + UPDATE on the same table — that combination was observed to lock
// config_client indefinitely on contended Postgres instances. See the same
// split for migrationDropAllowDirectKeysColumn.
func migrationRefreshConfigHashAfterMCPExternalServerURLRemoval(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "refresh_config_hash_after_mcp_external_server_url_removal"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	return RunSingleMigration(ctx, nil, db, logger, &migrator.Migration{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			if !mg.HasTable("config_client") {
				return nil
			}
			// Unconditional recompute — don't gate on the legacy column still
			// existing. The migrator framework's migrator_meta tracking
			// ensures this runs exactly once per DB, so re-stamping the same
			// hash on a fresh DB (where there's nothing to fix) is harmless.
			// Gating on the legacy column would make ordering brittle: any
			// reordering that drops the column before this runs would silently
			// turn the refresh into a no-op and leave stale hashes behind.
			// Read via an explicit column projection rather than GORM's default
			// Find (SELECT *). Earlier migrations in this same run add and drop
			// config_client columns; reusing a cached SELECT * plan whose
			// projection has since drifted is what trips PostgreSQL's "cached
			// plan must not change result type" (SQLSTATE 0A000), an error that
			// is unrecoverable inside a migration's transaction. An explicit
			// column list is a distinct, previously-uncached query: it prepares
			// fresh against the current schema and has a fixed result type. Same
			// class of guard as migrate_calendar_aligned.
			//
			// Derive the projection from the LIVE table columns intersected with
			// the struct, not from the struct alone. TableClientConfig can be
			// ahead of applied DDL: a declared column (e.g.
			// dump_errors_in_console_logs, added by a later migration step) does
			// not exist yet on upgrade, and naming a missing column in the SELECT
			// fails with 42703. Intersecting with the columns that physically
			// exist makes that impossible for any current or future column. Keep
			// the read on .Find (not a raw Scan) so the AfterFind hook still
			// deserializes the *_json columns into the virtual fields the hash
			// depends on; a raw Scan bypasses AfterFind and would corrupt them.
			existingCols, err := mg.ColumnTypes(&tables.TableClientConfig{})
			if err != nil {
				return fmt.Errorf("inspect config_client columns for hash recompute: %w", err)
			}
			present := make(map[string]struct{}, len(existingCols))
			for _, ct := range existingCols {
				present[ct.Name()] = struct{}{}
			}
			schemaStmt := &gorm.Statement{DB: tx}
			if err := schemaStmt.Parse(&tables.TableClientConfig{}); err != nil {
				return fmt.Errorf("parse config_client schema for hash recompute: %w", err)
			}
			projection := make([]string, 0, len(schemaStmt.Schema.DBNames))
			for _, name := range schemaStmt.Schema.DBNames {
				if _, ok := present[name]; ok {
					projection = append(projection, name)
				}
			}
			if len(projection) == 0 {
				logger.Info("[configstore] %s: no columns in common between config_client and TableClientConfig, skipping hash recompute", migrationName)
				return nil
			}
			var clientConfigs []tables.TableClientConfig
			if err := tx.Model(&tables.TableClientConfig{}).
				Select(projection).
				Find(&clientConfigs).Error; err != nil {
				return fmt.Errorf("fetch client configs for hash recompute: %w", err)
			}
			logger.Info("[configstore] %s: processing %d clientConfigs", migrationName, len(clientConfigs))
			for _, cc := range clientConfigs {
				if cc.ConfigHash == "" {
					continue
				}
				clientConfig := ClientConfig{
					DropExcessRequests:                    cc.DropExcessRequests,
					InitialPoolSize:                       cc.InitialPoolSize,
					PrometheusLabels:                      cc.PrometheusLabels,
					EnableLogging:                         cc.EnableLogging,
					DisableContentLogging:                 cc.DisableContentLogging,
					AllowPerRequestContentStorageOverride: cc.AllowPerRequestContentStorageOverride,
					AllowPerRequestRawOverride:            cc.AllowPerRequestRawOverride,
					DisableDBPingsInHealth:                cc.DisableDBPingsInHealth,
					LogRetentionDays:                      cc.LogRetentionDays,
					EnforceAuthOnInference:                cc.EnforceAuthOnInference,
					AllowedOrigins:                        cc.AllowedOrigins,
					AllowedHeaders:                        cc.AllowedHeaders,
					MaxRequestBodySizeMB:                  cc.MaxRequestBodySizeMB,
					MCPAgentDepth:                         cc.MCPAgentDepth,
					MCPToolExecutionTimeout:               cc.MCPToolExecutionTimeout,
					MCPCodeModeBindingLevel:               cc.MCPCodeModeBindingLevel,
					MCPToolSyncInterval:                   cc.MCPToolSyncInterval,
					MCPDisableAutoToolInject:              cc.MCPDisableAutoToolInject,
					MCPEnableTempTokenAuth:                cc.MCPEnableTempTokenAuth,
					MCPExternalClientURL:                  schemas.NewSecretVar(cc.MCPExternalClientURL),
					HeaderFilterConfig:                    cc.HeaderFilterConfig,
					AsyncJobResultTTL:                     cc.AsyncJobResultTTL,
					RequiredHeaders:                       cc.RequiredHeaders,
					LoggingHeaders:                        cc.LoggingHeaders,
					WhitelistedRoutes:                     cc.WhitelistedRoutes,
					HideDeletedVirtualKeysInFilters:       cc.HideDeletedVirtualKeysInFilters,
					RoutingChainMaxDepth:                  cc.RoutingChainMaxDepth,
					Compat: CompatConfig{
						ConvertTextToChat:      cc.CompatConvertTextToChat,
						ConvertChatToResponses: cc.CompatConvertChatToResponses,
						ShouldDropParams:       cc.CompatShouldDropParams,
						ShouldConvertParams:    cc.CompatShouldConvertParams,
					},
				}
				newHash, err := clientConfig.GenerateClientConfigHash()
				if err != nil {
					return fmt.Errorf("regenerate config hash for client config %d: %w", cc.ID, err)
				}
				if err := tx.Model(&cc).Update("config_hash", newHash).Error; err != nil {
					return fmt.Errorf("update config_hash for client config %d: %w", cc.ID, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return nil
		},
	})
}

// migrationDropLegacyCalendarAlignedColumns drops the legacy calendar_aligned
// columns from governance_budgets and governance_rate_limits. Calendar
// alignment is now a VK-only setting (governance_virtual_keys.calendar_aligned);
// budget and rate-limit reset logic derives the value from the owning VK at
// reset time. The columns were re-added by migrate_calendar_aligned after
// add_multi_budget_tables dropped governance_budgets.calendar_aligned, so any
// DB that ran both still has them — this migration cleans them up.
func migrationDropLegacyCalendarAlignedColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "drop_legacy_calendar_aligned_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			// Use a raw `ALTER TABLE ... DROP COLUMN` (dialect-aware via dropColumnSQL) instead of
			// GORM's Migrator.DropColumn, which on SQLite does a full table rebuild that aborts on
			// pre-existing FK violations; since these unconstrained boolean columns are safe to
			// leave behind, we log a warning rather than fail boot.
			if err := tx.Exec(dropColumnSQL(tx, "governance_budgets", "calendar_aligned")).Error; err != nil {
				logger.Info("[Migration] warning: could not drop legacy calendar_aligned column from governance_budgets: %v", err)
			}
			if err := tx.Exec(dropColumnSQL(tx, "governance_rate_limits", "calendar_aligned")).Error; err != nil {
				logger.Info("[Migration] warning: could not drop legacy calendar_aligned column from governance_rate_limits: %v", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error { return nil },
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running drop_legacy_calendar_aligned_columns migration: %s", err.Error())
	}
	return nil
}

// migrationAddTeamCalendarAlignedColumn adds calendar_aligned to governance_teams so
// team-level calendar alignment (governing all team budgets and the team rate limit)
// can be persisted.
func migrationAddTeamCalendarAlignedColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_team_calendar_aligned_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mig := tx.Migrator()
			if err := addColumnIfNotExists(tx, logger, &tables.TableTeam{}, "CalendarAligned"); err != nil {
				return fmt.Errorf("failed to add calendar_aligned column to governance_teams: %w", err)
			}
			// Backfill from legacy per-budget / per-rate-limit flags before the
			// drop migration removes them. Any team-owned budget with
			// calendar_aligned=true, or a team rate-limit with calendar_aligned=true,
			// promotes the team to calendar-aligned so behavior is preserved across upgrade.
			if mig.HasColumn(&tables.TableBudget{}, "calendar_aligned") {
				if err := tx.Exec(`
					UPDATE governance_teams
					SET calendar_aligned = TRUE
					WHERE EXISTS (
						SELECT 1 FROM governance_budgets b
						WHERE b.team_id = governance_teams.id AND b.calendar_aligned = TRUE
					)
				`).Error; err != nil {
					return fmt.Errorf("failed to backfill team calendar_aligned from budgets: %w", err)
				}
			}
			if mig.HasColumn(&tables.TableRateLimit{}, "calendar_aligned") {
				if err := tx.Exec(`
					UPDATE governance_teams
					SET calendar_aligned = TRUE
					WHERE rate_limit_id IN (
						SELECT id FROM governance_rate_limits WHERE calendar_aligned = TRUE
					)
				`).Error; err != nil {
					return fmt.Errorf("failed to backfill team calendar_aligned from rate limits: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return dropColumnIfExists(tx, logger, &tables.TableTeam{}, "calendar_aligned")
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_team_calendar_aligned_column migration: %s", err.Error())
	}
	return nil
}

// migrationAddModelParametersURLColumn adds the model_parameters_url column to framework_configs.
func migrationAddModelParametersURLColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_model_parameters_url_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableFrameworkConfig{}, "ModelParametersURL"); err != nil {
				return fmt.Errorf("failed to add model_parameters_url column to framework_configs: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return dropColumnIfExists(tx, logger, &tables.TableFrameworkConfig{}, "model_parameters_url")
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_model_parameters_url_column migration: %s", err.Error())
	}
	return nil
}

// migrationAddFrameworkConfigHashColumn adds the config_hash column to framework_configs
// so that file-vs-DB precedence can be determined via hash comparison on restart.
func migrationAddFrameworkConfigHashColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_framework_config_hash_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableFrameworkConfig{}, "ConfigHash"); err != nil {
				return fmt.Errorf("failed to add config_hash column to framework_configs: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return dropColumnIfExists(tx, logger, &tables.TableFrameworkConfig{}, "config_hash")
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_framework_config_hash_column migration: %s", err.Error())
	}
	return nil
}

// migrationAddTempTokensTable creates the temp_tokens table that backs the
// temptoken service.
func migrationAddTempTokensTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_temp_tokens_table"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mig := tx.Migrator()
			if !mig.HasTable(&tables.TempToken{}) {
				logger.Info("[configstore] %s: creating table TempToken", migrationName)
				if err := mig.CreateTable(&tables.TempToken{}); err != nil {
					return fmt.Errorf("failed to create temp_tokens table: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mig := tx.Migrator()
			if mig.HasTable(&tables.TempToken{}) {
				logger.Info("[configstore] %s: dropping table TempToken", migrationName)
				if err := mig.DropTable(&tables.TempToken{}); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_temp_tokens_table migration: %s", err.Error())
	}
	return nil
}

// migrationAddVirtualKeyBlacklistedModelsColumn adds the blacklisted_models JSON column
// to governance_virtual_key_provider_configs, matching the provider-key blacklist pattern.
// This migration performs the DDL only. The data backfill and config_hash recompute are
// done by migrationBackfillVirtualKeyBlacklistedModels so the ALTER's ACCESS EXCLUSIVE
// lock is not held across the backfill SELECT + UPDATE, which can deadlock Postgres.
func migrationAddVirtualKeyBlacklistedModelsColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_vk_provider_config_blacklisted_models_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableVirtualKeyProviderConfig{}, "blacklisted_models"); err != nil {
				return fmt.Errorf("failed to add blacklisted_models column: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_vk_provider_config_blacklisted_models_column migration: %s", err.Error())
	}
	return nil
}

// migrationBackfillVirtualKeyBlacklistedModels backfills empty JSON arrays into the
// blacklisted_models column added by migrationAddVirtualKeyBlacklistedModelsColumn and
// recomputes config_hash for every virtual key so they do not appear stale after upgrade.
// It is a separate migration from the column-add so the DDL's ACCESS EXCLUSIVE lock is
// never held across this SELECT + UPDATE backfill.
func migrationBackfillVirtualKeyBlacklistedModels(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "backfill_vk_provider_config_blacklisted_models"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			// Backfill empty arrays for existing rows. Idempotent via the WHERE clause.
			if err := tx.Exec("UPDATE governance_virtual_key_provider_configs SET blacklisted_models = '[]' WHERE blacklisted_models IS NULL OR blacklisted_models = ''").Error; err != nil {
				return fmt.Errorf("failed to backfill blacklisted_models: %w", err)
			}

			// Recompute config_hash for all VKs affected by the backfill so they do
			// not appear stale after upgrade. The hash is deterministic, so this is
			// safe to re-run.
			var virtualKeys []tables.TableVirtualKey
			if err := tx.
				Preload("ProviderConfigs").
				Preload("ProviderConfigs.Keys").
				Preload("MCPConfigs").
				Find(&virtualKeys).Error; err != nil {
				return fmt.Errorf("failed to fetch virtual keys for hash recomputation: %w", err)
			}
			logger.Info("[configstore] %s: processing %d virtualKeys", migrationName, len(virtualKeys))
			for _, vk := range virtualKeys {
				newHash, err := GenerateVirtualKeyHash(vk)
				if err != nil {
					return fmt.Errorf("failed to generate hash for VK %s: %w", vk.ID, err)
				}
				if err := tx.Model(&tables.TableVirtualKey{}).
					Where("id = ?", vk.ID).
					Update("config_hash", newHash).Error; err != nil {
					return fmt.Errorf("failed to update config_hash for VK %s: %w", vk.ID, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running backfill_vk_provider_config_blacklisted_models migration: %s", err.Error())
	}
	return nil
}

// migrationAddVKAccessProfileIDColumn adds the access_profile_id column to governance_virtual_keys.
func migrationAddVKAccessProfileIDColumn(_ context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_vk_access_profile_id_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_vk_access_profile_id_column migration: %s", err.Error())
	}
	return nil
}

// migrationDropVKAccessProfileIDColumn drops the access_profile_id column and its
// index from governance_virtual_keys, reverting migrationAddVKAccessProfileIDColumn.
func migrationDropVKAccessProfileIDColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "drop_vk_access_profile_id_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			// DROP INDEX IF EXISTS avoids aborting the Postgres transaction when
			// the index was never created (e.g. fresh installs where the add
			// migration ran as a no-op).
			if err := tx.Exec("DROP INDEX IF EXISTS idx_governance_virtual_keys_access_profile_id").Error; err != nil {
				return fmt.Errorf("failed to drop index idx_governance_virtual_keys_access_profile_id: %w", err)
			}
			// Use raw ALTER TABLE instead of GORM's DropColumn: GORM's SQLite
			// DropColumn does a full table rebuild inside a transaction where
			// PRAGMA foreign_keys cannot be disabled, causing FK checks on
			// unrelated columns to fail against test data.
			// ALTER TABLE DROP COLUMN (SQLite 3.35+) modifies the schema in place.
			if tx.Migrator().HasColumn("governance_virtual_keys", "access_profile_id") {
				if err := tx.Exec("ALTER TABLE governance_virtual_keys DROP COLUMN access_profile_id").Error; err != nil {
					return fmt.Errorf("failed to drop access_profile_id column from governance_virtual_keys: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableVirtualKeyProviderConfig{}, "blacklisted_models"); err != nil {
				return fmt.Errorf("failed to drop blacklisted_models column: %w", err)
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running drop_vk_access_profile_id_column migration: %s", err.Error())
	}
	return nil
}

// migrationAddPerUserHeadersTables creates the mcp_per_user_header_credentials
// table and adds the per_user_header_keys_json column to config_mcp_clients.
// Mirrors the partial-unique-index pattern from
// migrationAddOAuthAuthModeColumns so the per-user-headers credentials are
// keyed by (auth_mode, identity, mcp_client_id) the same way per-user OAuth
// tokens are. Forward-only on data — no rows exist yet.
func migrationAddPerUserHeadersTables(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_mcp_per_user_header_credentials_table"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()

			// 1) config_mcp_clients.per_user_header_keys_json (admin-defined
			//    schema of required header names; nullable / empty for all
			//    other auth types).
			if err := addColumnIfNotExists(tx, logger, &tables.TableMCPClient{}, "PerUserHeaderKeysJSON"); err != nil {
				return fmt.Errorf("add per_user_header_keys_json column to config_mcp_clients: %w", err)
			}

			// 2) mcp_per_user_header_credentials table.
			if !mg.HasTable(&tables.TableMCPPerUserHeaderCredential{}) {
				logger.Info("[configstore] %s: creating table TableMCPPerUserHeaderCredential", migrationName)
				if err := mg.CreateTable(&tables.TableMCPPerUserHeaderCredential{}); err != nil {
					return fmt.Errorf("create mcp_per_user_header_credentials table: %w", err)
				}
			}

			// 3) Partial unique indexes per auth_mode — matches the
			//    oauth_user_tokens layout so the cascade / orphan logic stays
			//    parallel.
			partialUniques := []string{
				`CREATE UNIQUE INDEX IF NOT EXISTS idx_mcp_per_user_header_credentials_user_mcp
					ON mcp_per_user_header_credentials (user_id, mcp_client_id)
					WHERE auth_mode = 'user' AND user_id IS NOT NULL AND user_id != ''`,
				`CREATE UNIQUE INDEX IF NOT EXISTS idx_mcp_per_user_header_credentials_vk_mcp
					ON mcp_per_user_header_credentials (virtual_key_id, mcp_client_id)
					WHERE auth_mode = 'vk' AND virtual_key_id IS NOT NULL AND virtual_key_id != ''`,
				`CREATE UNIQUE INDEX IF NOT EXISTS idx_mcp_per_user_header_credentials_session_mcp
					ON mcp_per_user_header_credentials (session_id, mcp_client_id)
					WHERE auth_mode = 'session' AND session_id IS NOT NULL AND session_id != ''`,
			}
			logger.Info("[configstore] %s: processing %d partialUniques", migrationName, len(partialUniques))
			for _, stmt := range partialUniques {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("create partial unique index on mcp_per_user_header_credentials: %w", err)
				}
			}

			// 4) Status-scoped partial indexes for cheap UI / cleanup queries.
			statusIndexes := []string{
				`CREATE INDEX IF NOT EXISTS idx_mcp_per_user_header_credentials_orphaned
					ON mcp_per_user_header_credentials (status)
					WHERE status = 'orphaned'`,
				`CREATE INDEX IF NOT EXISTS idx_mcp_per_user_header_credentials_needs_update
					ON mcp_per_user_header_credentials (mcp_client_id)
					WHERE status = 'needs_update'`,
			}
			logger.Info("[configstore] %s: processing %d statusIndexes", migrationName, len(statusIndexes))
			for _, stmt := range statusIndexes {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("create status partial index on mcp_per_user_header_credentials: %w", err)
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			for _, name := range []string{
				"idx_mcp_per_user_header_credentials_user_mcp",
				"idx_mcp_per_user_header_credentials_vk_mcp",
				"idx_mcp_per_user_header_credentials_session_mcp",
				"idx_mcp_per_user_header_credentials_orphaned",
				"idx_mcp_per_user_header_credentials_needs_update",
			} {
				if err := tx.Exec("DROP INDEX IF EXISTS " + name).Error; err != nil {
					return fmt.Errorf("drop %s: %w", name, err)
				}
			}
			if mg.HasTable(&tables.TableMCPPerUserHeaderCredential{}) {
				logger.Info("[configstore] %s: dropping table TableMCPPerUserHeaderCredential", migrationName)
				if err := mg.DropTable(&tables.TableMCPPerUserHeaderCredential{}); err != nil {
					return fmt.Errorf("drop mcp_per_user_header_credentials: %w", err)
				}
			}
			if err := dropColumnIfExists(tx, logger, &tables.TableMCPClient{}, "PerUserHeaderKeysJSON"); err != nil {
				return fmt.Errorf("drop per_user_header_keys_json column from config_mcp_clients: %w", err)
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_mcp_per_user_header_credentials_table migration: %s", err.Error())
	}
	return nil
}

// migrationAddPerUserHeadersFlowsTable creates the
// mcp_per_user_header_flows table. Pending submission flow rows that
// mirror oauth_user_sessions for the per-user-headers surface — the
// resolver creates one when the inline-401 fires; the submit endpoint
// deletes the row on success; the sweep worker reaps expired pending
// rows. Lives in its own migration so it can land on DBs that already
// applied migrationAddPerUserHeadersTables (which only created the
// credentials table).
func migrationAddPerUserHeadersFlowsTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_mcp_per_user_header_flows_table"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			if !mg.HasTable(&tables.TableMCPPerUserHeaderFlow{}) {
				logger.Info("[configstore] %s: creating table TableMCPPerUserHeaderFlow", migrationName)
				if err := mg.CreateTable(&tables.TableMCPPerUserHeaderFlow{}); err != nil {
					return fmt.Errorf("create mcp_per_user_header_flows table: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			if mg.HasTable(&tables.TableMCPPerUserHeaderFlow{}) {
				logger.Info("[configstore] %s: dropping table TableMCPPerUserHeaderFlow", migrationName)
				if err := mg.DropTable(&tables.TableMCPPerUserHeaderFlow{}); err != nil {
					return fmt.Errorf("drop mcp_per_user_header_flows: %w", err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_mcp_per_user_header_flows_table migration: %s", err.Error())
	}
	return nil
}

// migrationAddCreatedByUserIDColumnForVirtualKeys adds the created_by_user_id column to the governance_virtual_keys table.
func migrationAddCreatedByUserIDColumnForVirtualKeys(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_created_by_user_id_column_for_virtual_keys"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if !tx.Migrator().HasColumn(&tables.TableVirtualKey{}, "created_by_user_id") {
				if err := tx.Exec("ALTER TABLE governance_virtual_keys ADD COLUMN created_by_user_id VARCHAR(255)").Error; err != nil {
					return fmt.Errorf("failed to add created_by_user_id column to governance_virtual_keys: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if tx.Migrator().HasColumn(&tables.TableVirtualKey{}, "created_by_user_id") {
				if err := tx.Exec("ALTER TABLE governance_virtual_keys DROP COLUMN created_by_user_id").Error; err != nil {
					return fmt.Errorf("failed to drop created_by_user_id column from governance_virtual_keys: %w", err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_created_by_user_id_column_for_virtual_keys migration: %s", err.Error())
	}
	return nil
}

// migrationDropAzureAPIVersionColumn adds the created_by_user_id column to the governance_virtual_keys table
func migrationDropAzureAPIVersionColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "drop_azure_api_version_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if tx.Migrator().HasColumn(&tables.TableKey{}, "azure_api_version") {
				if err := tx.Exec("ALTER TABLE config_keys DROP COLUMN azure_api_version").Error; err != nil {
					return fmt.Errorf("failed to drop azure_api_version column: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if !tx.Migrator().HasColumn(&tables.TableKey{}, "azure_api_version") {
				if err := tx.Exec("ALTER TABLE config_keys ADD COLUMN azure_api_version TEXT").Error; err != nil {
					return fmt.Errorf("failed to re-add azure_api_version column: %w", err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running drop_azure_api_version_column migration: %s", err.Error())
	}
	return nil
}

// migrationReAddAllowDirectKeysColumn re-adds the allow_direct_keys column to config_client.
// The column was originally added then dropped in v1.5.0 when the direct key bypass feature
// was removed. It is re-added here as the feature is being restored with header-gated access.
func migrationReAddAllowDirectKeysColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "re_add_allow_direct_keys_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if !tx.Migrator().HasColumn(&tables.TableClientConfig{}, "allow_direct_keys") {
				if err := tx.Exec("ALTER TABLE config_client ADD COLUMN allow_direct_keys BOOLEAN DEFAULT FALSE").Error; err != nil {
					return fmt.Errorf("failed to re-add allow_direct_keys column: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableClientConfig{}, "allow_direct_keys"); err != nil {
				return fmt.Errorf("failed to drop allow_direct_keys column: %w", err)
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running re_add_allow_direct_keys_column migration: %s", err.Error())
	}
	return nil
}

// migrationAddMCPClientTLSConfigColumn adds the tls_config_json column to the config_mcp_clients table.
func migrationAddMCPClientTLSConfigColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_mcp_client_tls_config_json_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if !tx.Migrator().HasColumn(&tables.TableMCPClient{}, "tls_config_json") {
				if err := tx.Exec("ALTER TABLE config_mcp_clients ADD COLUMN tls_config_json TEXT").Error; err != nil {
					return fmt.Errorf("failed to add tls_config_json column: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if tx.Migrator().HasColumn(&tables.TableMCPClient{}, "tls_config_json") {
				if err := tx.Exec("ALTER TABLE config_mcp_clients DROP COLUMN tls_config_json").Error; err != nil {
					return fmt.Errorf("failed to drop tls_config_json column: %w", err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_mcp_client_tls_config_json_column migration: %s", err.Error())
	}
	return nil
}

// migrationAddAdditionalAttributesToPricing adds the additional_attributes
// column to governance_model_pricing. The column stores editorial per-model
// metadata (e.g. description) as a JSON blob. It is intentionally excluded
// from UpsertModelPrices' update list so the 24-hour pricing sync never
// overwrites it.
func migrationAddAdditionalAttributesToPricing(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_additional_attributes_to_pricing"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableModelPricing{}, "AdditionalAttributesJSON"); err != nil {
				return fmt.Errorf("failed to add additional_attributes column: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableModelPricing{}, "AdditionalAttributesJSON"); err != nil {
				return fmt.Errorf("failed to drop additional_attributes column: %w", err)
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_additional_attributes_to_pricing migration: %s", err.Error())
	}
	return nil
}

// migrationAddModelPricingIsDeprecatedColumn adds is_deprecated to
// governance_model_pricing so synced datasheets can mark models that should
// remain listable but are no longer recommended for new use.
func migrationAddModelPricingIsDeprecatedColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_model_pricing_is_deprecated_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableModelPricing{}, "IsDeprecated"); err != nil {
				return fmt.Errorf("failed to add is_deprecated column: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableModelPricing{}, "IsDeprecated"); err != nil {
				return fmt.Errorf("failed to drop is_deprecated column: %w", err)
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_model_pricing_is_deprecated_column migration: %s", err.Error())
	}
	return nil
}

// migrationAddCustomerCalendarAlignedColumn adds calendar_aligned to governance_customers
// so customer-level calendar alignment can be persisted. No backfill is needed: the
// legacy per-budget/per-rate-limit calendar_aligned columns were dropped by
// drop_legacy_calendar_aligned_columns before this migration runs, and calendar_aligned
// never worked for customers, so there is no prior behavior to preserve.
func migrationAddCustomerCalendarAlignedColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_customer_calendar_aligned_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableCustomer{}, "CalendarAligned"); err != nil {
				return fmt.Errorf("failed to add calendar_aligned column to governance_customers: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return dropColumnIfExists(tx, logger, &tables.TableCustomer{}, "calendar_aligned")
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_customer_calendar_aligned_column migration: %s", err.Error())
	}
	return nil
}

// migrationAddCustomerBudgetsToBudgetsTable pivots customer budgets from a single-FK on
// governance_customers.budget_id to multi-budget ownership via governance_budgets.customer_id,
// mirroring how team budgets were restructured in migrationAddTeamBudgetsToBudgetsTable.
func migrationAddCustomerBudgetsToBudgetsTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_customer_budgets_to_budgets_table"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()

			// Add customer_id FK column on governance_budgets.
			if err := addColumnIfNotExists(tx, logger, &tables.TableBudget{}, "CustomerID"); err != nil {
				return fmt.Errorf("failed to add customer_id column to governance_budgets: %w", err)
			}

			// Create index on the new FK column (AddColumn doesn't create indexes from struct tags).
			if !mg.HasIndex(&tables.TableBudget{}, "idx_governance_budgets_customer_id") {
				logger.Info("[configstore] %s: creating index CustomerID on TableBudget", migrationName)
				if err := mg.CreateIndex(&tables.TableBudget{}, "CustomerID"); err != nil {
					return fmt.Errorf("failed to create index on governance_budgets.customer_id: %w", err)
				}
			}

			// Backfill: set customer_id from legacy governance_customers.budget_id (if column still exists).
			// The column is intentionally kept; a future migration can drop it once all instances
			// have migrated and the column is confirmed unused.
			legacyExists, err := hasColumn(tx, "governance_customers", "budget_id")
			if err != nil {
				return fmt.Errorf("failed to introspect governance_customers for budget_id: %w", err)
			}
			if legacyExists {
				// Preflight: fail if any customer-referenced budget is already owned by another entity.
				var conflictCount int64
				if err := tx.Raw(`
					SELECT COUNT(*) FROM governance_budgets b
					WHERE (b.virtual_key_id IS NOT NULL OR b.provider_config_id IS NOT NULL OR b.team_id IS NOT NULL OR b.model_config_id IS NOT NULL)
					  AND EXISTS (SELECT 1 FROM governance_customers c WHERE c.budget_id = b.id)
				`).Scan(&conflictCount).Error; err != nil {
					return fmt.Errorf("failed to check for multi-owner customer budget conflicts: %w", err)
				}
				if conflictCount > 0 {
					return fmt.Errorf(
						"cannot migrate customer budgets: %d budget row(s) referenced by a customer are already owned by another entity; resolve manually before re-running",
						conflictCount,
					)
				}

				if err := tx.Exec(`
					UPDATE governance_budgets SET customer_id = (
						SELECT id FROM governance_customers
						WHERE governance_customers.budget_id = governance_budgets.id
					) WHERE customer_id IS NULL AND EXISTS (
						SELECT 1 FROM governance_customers
						WHERE governance_customers.budget_id = governance_budgets.id
					)
				`).Error; err != nil {
					return fmt.Errorf("failed to backfill customer budget customer_id: %w", err)
				}
			}

			// Create FK constraint with CASCADE delete (defined on TableCustomer.Budgets).
			if !mg.HasConstraint(&tables.TableCustomer{}, "Budgets") {
				if err := mg.CreateConstraint(&tables.TableCustomer{}, "Budgets"); err != nil {
					return fmt.Errorf("failed to create FK constraint for Customer -> Budgets: %w", err)
				}
			}

			// Refresh config_hash for customers whose budgets just got linked. GenerateCustomerHash
			// now includes sorted budget IDs, so hashes written before multi-budget support are stale.
			var customersToRehash []tables.TableCustomer
			if err := tx.Preload("Budgets").Find(&customersToRehash).Error; err != nil {
				return fmt.Errorf("failed to fetch customers for hash refresh: %w", err)
			}
			logger.Info("[configstore] %s: processing %d customersToRehash", migrationName, len(customersToRehash))
			for _, c := range customersToRehash {
				if len(c.Budgets) == 0 {
					continue
				}
				hash, err := GenerateCustomerHash(c)
				if err != nil {
					return fmt.Errorf("failed to generate hash for customer %s: %w", c.ID, err)
				}
				if err := tx.Model(&tables.TableCustomer{}).Where("id = ?", c.ID).Update("config_hash", hash).Error; err != nil {
					return fmt.Errorf("failed to update hash for customer %s: %w", c.ID, err)
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableBudget{}, "CustomerID"); err != nil {
				return fmt.Errorf("failed to drop customer_id column from governance_budgets: %w", err)
			}
			return nil
		},
	}})
	// SQLite workaround — same reasoning as migrationAddMultiBudgetTables: GORM's
	// CreateConstraint (Customer -> Budgets) rebuilds governance_budgets via DROP+RENAME,
	// which fails when other tables hold FKs into it and foreign_keys is ON. PRAGMA
	// foreign_keys cannot change inside a transaction, so disable it on a pinned single
	// connection before the migrator opens its transaction, then restore it.
	if db.Dialector.Name() == "sqlite" {
		sqlDB, err := db.DB()
		if err != nil {
			return fmt.Errorf("failed to get underlying sql.DB: %w", err)
		}
		prevMaxOpenConns := sqlDB.Stats().MaxOpenConnections
		sqlDB.SetMaxOpenConns(1)
		defer sqlDB.SetMaxOpenConns(prevMaxOpenConns)

		if err := db.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
			return fmt.Errorf("failed to disable SQLite foreign keys: %w", err)
		}
		defer func() {
			if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
				log.Fatalf("[Migration] FATAL: failed to re-enable SQLite foreign keys: %v", err)
			}
		}()
	}
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_customer_budgets_to_budgets_table migration: %s", err.Error())
	}
	return nil
}

// migrationNullLegacyCustomerBudgetID clears the legacy governance_customers.budget_id
// values left behind by migrationAddCustomerBudgetsToBudgetsTable. The column and its
// FK (fk_governance_customers_budget) are intentionally kept — dropping either is
// deferred to a major release — but rows still holding a value make DeleteCustomer's
// `DELETE FROM governance_budgets WHERE customer_id = ?` fail that FK check. Ownership
// already lives on governance_budgets.customer_id, so after a defensive backfill the
// legacy values can be nulled; a null reference satisfies the FK unconditionally.
func migrationNullLegacyCustomerBudgetID(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "null_legacy_customer_budget_id_refs"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			legacyExists, err := hasColumn(tx, "governance_customers", "budget_id")
			if err != nil {
				return fmt.Errorf("failed to introspect governance_customers for budget_id: %w", err)
			}
			if !legacyExists {
				return nil
			}
			// Customers the defensive backfill below will attach a budget to.
			// GenerateCustomerHash includes sorted budget IDs, so their stored
			// config_hash goes stale once the budget is linked and must be refreshed.
			var affectedCustomerIDs []string
			if err := tx.Raw(`
				SELECT DISTINCT c.id
				FROM governance_customers c
				JOIN governance_budgets b ON b.id = c.budget_id
				WHERE b.customer_id IS NULL
				  AND b.virtual_key_id IS NULL AND b.team_id IS NULL
				  AND b.provider_config_id IS NULL AND b.model_config_id IS NULL
			`).Scan(&affectedCustomerIDs).Error; err != nil {
				return fmt.Errorf("failed to identify customers affected by budget backfill: %w", err)
			}
			// Defensive backfill (same shape as migrationAddCustomerBudgetsToBudgetsTable)
			// in case a budget_id was written after that migration ran, e.g. by an older
			// instance in a mixed-version cluster. Only claims budgets with no owner yet.
			if err := tx.Exec(`
				UPDATE governance_budgets SET customer_id = (
					SELECT id FROM governance_customers
					WHERE governance_customers.budget_id = governance_budgets.id
				) WHERE customer_id IS NULL
				  AND virtual_key_id IS NULL AND team_id IS NULL
				  AND provider_config_id IS NULL AND model_config_id IS NULL
				  AND EXISTS (
					SELECT 1 FROM governance_customers
					WHERE governance_customers.budget_id = governance_budgets.id
				)
			`).Error; err != nil {
				return fmt.Errorf("failed to backfill customer budget customer_id: %w", err)
			}
			// Refresh config_hash for customers whose budgets just got linked, keeping
			// migration and runtime hash generation in parity (same as
			// migrationAddCustomerBudgetsToBudgetsTable).
			logger.Info("[configstore] %s: processing %d affectedCustomerIDs", migrationName, len(affectedCustomerIDs))
			for _, customerID := range affectedCustomerIDs {
				var customer tables.TableCustomer
				if err := tx.Preload("Budgets").First(&customer, "id = ?", customerID).Error; err != nil {
					return fmt.Errorf("failed to reload customer %s for hash refresh: %w", customerID, err)
				}
				hash, err := GenerateCustomerHash(customer)
				if err != nil {
					return fmt.Errorf("failed to generate hash for customer %s: %w", customerID, err)
				}
				if err := tx.Model(&tables.TableCustomer{}).Where("id = ?", customerID).Update("config_hash", hash).Error; err != nil {
					return fmt.Errorf("failed to update hash for customer %s: %w", customerID, err)
				}
			}
			if err := tx.Exec(`UPDATE governance_customers SET budget_id = NULL WHERE budget_id IS NOT NULL`).Error; err != nil {
				return fmt.Errorf("failed to clear legacy governance_customers.budget_id values: %w", err)
			}
			return nil
		},
		// Best-effort inverse: repopulate budget_id from governance_budgets.customer_id.
		// The legacy column held a single value while the new model allows several
		// budgets per customer, so for multi-budget customers the oldest budget is
		// picked — for any customer that predates the pivot that is the original
		// legacy budget, since later additions sort newer.
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			legacyExists, err := hasColumn(tx, "governance_customers", "budget_id")
			if err != nil {
				return fmt.Errorf("failed to introspect governance_customers for budget_id: %w", err)
			}
			if !legacyExists {
				return nil
			}
			if err := tx.Exec(`
				UPDATE governance_customers SET budget_id = (
					SELECT id FROM governance_budgets
					WHERE governance_budgets.customer_id = governance_customers.id
					ORDER BY created_at ASC, id ASC
					LIMIT 1
				) WHERE budget_id IS NULL AND EXISTS (
					SELECT 1 FROM governance_budgets
					WHERE governance_budgets.customer_id = governance_customers.id
				)
			`).Error; err != nil {
				return fmt.Errorf("failed to restore legacy governance_customers.budget_id values: %w", err)
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running null_legacy_customer_budget_id_refs migration: %s", err.Error())
	}
	return nil
}

// migrationAddMCPLibraryTable creates the mcp_library table, the synced-only
// catalog of discoverable MCP servers. Rows are populated from the external MCP
// library datasheet on a configurable interval (mirroring the model-pricing
// sync), so this migration only stands up the schema; no rows exist yet.
func migrationAddMCPLibraryTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_mcp_library_table"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			if !mg.HasTable(&tables.TableMCPLibrary{}) {
				logger.Info("[configstore] %s: creating table TableMCPLibrary", migrationName)
				if err := mg.CreateTable(&tables.TableMCPLibrary{}); err != nil {
					return fmt.Errorf("create mcp_library table: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			if mg.HasTable(&tables.TableMCPLibrary{}) {
				logger.Info("[configstore] %s: dropping table TableMCPLibrary", migrationName)
				if err := mg.DropTable(&tables.TableMCPLibrary{}); err != nil {
					return fmt.Errorf("drop mcp_library table: %w", err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_mcp_library_table migration: %s", err.Error())
	}
	return nil
}

// migrationAddMCPLibraryConfigColumns adds the mcp_library_url and
// mcp_library_sync_interval columns to framework_configs. These store the sync
// source + interval for the MCP server library catalog, mirroring pricing_url /
// pricing_sync_interval. Idempotent via HasColumn guards.
func migrationAddMCPLibraryConfigColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_mcp_library_config_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &tables.TableFrameworkConfig{}, "MCPLibraryURL"); err != nil {
				return fmt.Errorf("add mcp_library_url column: %w", err)
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TableFrameworkConfig{}, "MCPLibrarySyncInterval"); err != nil {
				return fmt.Errorf("add mcp_library_sync_interval column: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &tables.TableFrameworkConfig{}, "MCPLibraryURL"); err != nil {
				return fmt.Errorf("drop mcp_library_url column: %w", err)
			}
			if err := dropColumnIfExists(tx, logger, &tables.TableFrameworkConfig{}, "MCPLibrarySyncInterval"); err != nil {
				return fmt.Errorf("drop mcp_library_sync_interval column: %w", err)
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_mcp_library_config_columns migration: %s", err.Error())
	}
	return nil
}

// migrationAddMCPLibrarySourceColumns adds the source and deleted_at columns to
// mcp_library. `source` marks a row as remote-synced or org-internal ("custom")
// so the sync can protect custom rows; `deleted_at` is a soft-delete tombstone
// so a user-hidden row (remote or custom) is never resurrected by the next sync.
// Idempotent via HasColumn guards.
func migrationAddMCPLibrarySourceColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_mcp_library_source_columns"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			if err := addColumnIfNotExists(tx, logger, &tables.TableMCPLibrary{}, "Source"); err != nil {
				return fmt.Errorf("add source column: %w", err)
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TableMCPLibrary{}, "DeletedAt"); err != nil {
				return fmt.Errorf("add deleted_at column: %w", err)
			}
			// Create indexes on the new columns (AddColumn doesn't create indexes
			// from struct tags). `deleted_at IS NULL` is the leading predicate on
			// every paginated library query, so the index avoids a full table scan.
			if !mg.HasIndex(&tables.TableMCPLibrary{}, "idx_mcp_library_source") {
				logger.Info("[configstore] %s: creating index Source on TableMCPLibrary", migrationName)
				if err := mg.CreateIndex(&tables.TableMCPLibrary{}, "Source"); err != nil {
					return fmt.Errorf("create index on mcp_library.source: %w", err)
				}
			}
			if !mg.HasIndex(&tables.TableMCPLibrary{}, "idx_mcp_library_deleted_at") {
				logger.Info("[configstore] %s: creating index DeletedAt on TableMCPLibrary", migrationName)
				if err := mg.CreateIndex(&tables.TableMCPLibrary{}, "DeletedAt"); err != nil {
					return fmt.Errorf("create index on mcp_library.deleted_at: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			// Rollback is intentionally a no-op: dropping `source` and
			// `deleted_at` would destroy custom-row protection markers and
			// soft-delete tombstones, letting the next sync resurrect rows the
			// user hid. This migration is one-way.
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running add_mcp_library_source_columns migration: %s", err.Error())
	}
	return nil
}

// migrationAddCustomerNameUniqueConstraint deduplicates governance_customers by
// appending -1, -2, … to later occurrences of the same name (ordered by
// created_at then id), then adds a unique index on the name column.
func migrationAddCustomerNameUniqueConstraint(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_customer_name_unique_constraint_dedup"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	const idxName = "idx_governance_customers_name"

	// Step 1 (transactional): rename duplicate customer names so the later
	// CREATE UNIQUE INDEX cannot fail due to pre-existing duplicates.
	if err := RunSingleMigration(ctx, nil, db, logger, &migrator.Migration{
		ID: "add_customer_name_unique_constraint_dedup",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			// Fetch all customers in a stable order so the earliest-created row
			// always keeps the original name and later duplicates receive suffixes.
			var customers []tables.TableCustomer
			if err := tx.Order("created_at ASC, id ASC").Find(&customers).Error; err != nil {
				return fmt.Errorf("failed to fetch customers: %w", err)
			}

			// taken tracks every name that is currently (or will be) in use so
			// suffix search never collides with an existing original name.
			taken := make(map[string]bool, len(customers))
			for _, c := range customers {
				taken[c.Name] = true
			}

			firstSeen := make(map[string]bool, len(customers))
			logger.Info("[configstore] %s: processing %d customers", migrationName, len(customers))
			for _, c := range customers {
				if !firstSeen[c.Name] {
					firstSeen[c.Name] = true
					continue // earliest occurrence keeps its name
				}
				// Find the lowest suffix whose candidate name is not already taken.
				suffix := 1
				candidate := fmt.Sprintf("%s-%d", c.Name, suffix)
				for taken[candidate] {
					suffix++
					candidate = fmt.Sprintf("%s-%d", c.Name, suffix)
				}
				taken[candidate] = true
				if err := tx.Model(&tables.TableCustomer{}).Where("id = ?", c.ID).Update("name", candidate).Error; err != nil {
					return fmt.Errorf("failed to rename customer %s to %q: %w", c.ID, candidate, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return nil // name renames are not reversed; dropping the index in step 2 restores the invariant
		},
	}); err != nil {
		return err
	}

	// Step 2 (non-transactional): create the unique index.
	// UseTransaction must be false because CREATE INDEX CONCURRENTLY cannot
	// execute inside a transaction block. IF NOT EXISTS makes this step safe
	// to re-run if the process crashes after the index is built but before
	// the migration record is written.
	noTxOpts := *migrator.DefaultOptions
	noTxOpts.UseTransaction = false
	return RunSingleMigration(ctx, &noTxOpts, db, logger, &migrator.Migration{
		ID: "add_customer_name_unique_constraint_index",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			// SQLite does not support CONCURRENTLY; use the plain form there.
			var stmt string
			if tx.Dialector.Name() == "sqlite" {
				stmt = "CREATE UNIQUE INDEX IF NOT EXISTS " + idxName + " ON governance_customers (name)"
			} else {
				stmt = "CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS " + idxName + " ON governance_customers (name)"
			}
			if err := tx.Exec(stmt).Error; err != nil {
				return fmt.Errorf("failed to create unique index on governance_customers.name: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return tx.Exec("DROP INDEX IF EXISTS " + idxName).Error
		},
	})
}

func migrationAddOAuth2ServerTables(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_oauth2_server_tables"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{
		{
			ID: migrationName,
			Migrate: func(tx *gorm.DB) error {
				tx = tx.WithContext(ctx)
				mg := tx.Migrator()
				if !mg.HasColumn(&tables.TableClientConfig{}, "mcp_server_auth_mode") {
					if err := mg.AddColumn(&tables.TableClientConfig{}, "MCPServerAuthMode"); err != nil {
						return fmt.Errorf("add mcp_server_auth_mode column: %w", err)
					}
				}
				if !mg.HasColumn(&tables.TableClientConfig{}, "oauth2_server_config_json") {
					if err := mg.AddColumn(&tables.TableClientConfig{}, "OAuth2ServerConfigJSON"); err != nil {
						return fmt.Errorf("add oauth2_server_config_json column: %w", err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				tx = tx.WithContext(ctx)
				mg := tx.Migrator()
				if mg.HasColumn(&tables.TableClientConfig{}, "oauth2_server_config_json") {
					if err := mg.DropColumn(&tables.TableClientConfig{}, "OAuth2ServerConfigJSON"); err != nil {
						return fmt.Errorf("drop oauth2_server_config_json column: %w", err)
					}
				}
				if mg.HasColumn(&tables.TableClientConfig{}, "mcp_server_auth_mode") {
					if err := mg.DropColumn(&tables.TableClientConfig{}, "MCPServerAuthMode"); err != nil {
						return fmt.Errorf("drop mcp_server_auth_mode column: %w", err)
					}
				}
				return nil
			},
		},
	})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running db migration %s: %w", migrationName, err)
	}
	return nil
}

func migrationAddOAuth2IssuanceTables(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_oauth2_issuance_tables"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			if !mg.HasTable(&tables.TableOAuth2Client{}) {
				if err := mg.CreateTable(&tables.TableOAuth2Client{}); err != nil {
					return fmt.Errorf("create oauth2_clients table: %w", err)
				}
			}
			if !mg.HasTable(&tables.TableOAuth2AuthorizeRequest{}) {
				if err := mg.CreateTable(&tables.TableOAuth2AuthorizeRequest{}); err != nil {
					return fmt.Errorf("create oauth2_authorize_requests table: %w", err)
				}
			}
			if !mg.HasTable(&tables.TableOAuth2RefreshToken{}) {
				if err := mg.CreateTable(&tables.TableOAuth2RefreshToken{}); err != nil {
					return fmt.Errorf("create oauth2_refresh_tokens table: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			// Drop in reverse creation order.
			if mg.HasTable(&tables.TableOAuth2RefreshToken{}) {
				if err := mg.DropTable(&tables.TableOAuth2RefreshToken{}); err != nil {
					return fmt.Errorf("drop oauth2_refresh_tokens table: %w", err)
				}
			}
			if mg.HasTable(&tables.TableOAuth2AuthorizeRequest{}) {
				if err := mg.DropTable(&tables.TableOAuth2AuthorizeRequest{}); err != nil {
					return fmt.Errorf("drop oauth2_authorize_requests table: %w", err)
				}
			}
			if mg.HasTable(&tables.TableOAuth2Client{}) {
				if err := mg.DropTable(&tables.TableOAuth2Client{}); err != nil {
					return fmt.Errorf("drop oauth2_clients table: %w", err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running db migration %s: %w", migrationName, err)
	}
	return nil
}

func migrationAddMCPClientToolExecutionTimeoutColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_mcp_client_tool_execution_timeout_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return addColumnIfNotExists(tx, logger, &tables.TableMCPClient{}, "tool_execution_timeout")
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return dropColumnIfExists(tx, logger, &tables.TableMCPClient{}, "tool_execution_timeout")
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running %s migration: %w", migrationName, err)
	}
	return nil
}

// migrationAddVirtualKeyExpiresAtColumn adds nullable expires_at to governance_virtual_keys.
// No index: expiry is checked in-memory from the already-loaded VK, never queried by column.
func migrationAddVirtualKeyExpiresAtColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_virtual_key_expires_at_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return addColumnIfNotExists(tx, logger, &tables.TableVirtualKey{}, "expires_at")
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return dropColumnIfExists(tx, logger, &tables.TableVirtualKey{}, "expires_at")
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running %s migration: %w", migrationName, err)
	}
	return nil
}

// migrationAddVertexForceSingleRegionColumn adds the vertex_force_single_region column to the key table.
// Existing keys default to false (NULL), preserving the current multi-region promotion behaviour.
func migrationAddVertexForceSingleRegionColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_vertex_force_single_region_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return addColumnIfNotExists(tx, logger, &tables.TableKey{}, "vertex_force_single_region")
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return dropColumnIfExists(tx, logger, &tables.TableKey{}, "vertex_force_single_region")
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running %s migration: %w", migrationName, err)
	}
	return nil
}

// migrationAddSidekiqTable creates the generic `sidekiq` background-job table. Uses raw SQL
// (not GORM auto-DDL) so the schema is explicit and stable across GORM versions.
// Idempotent via CREATE TABLE IF NOT EXISTS; covers postgres and sqlite dialects.
func migrationAddSidekiqTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_sidekiq_table"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)

	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			var createTable string
			switch tx.Dialector.Name() {
			case "postgres":
				createTable = `
					CREATE TABLE IF NOT EXISTS sidekiq (
						id                  TEXT PRIMARY KEY,
						kind                TEXT NOT NULL,
						status              TEXT NOT NULL DEFAULT 'pending',
						runner_id           TEXT,
						metadata            TEXT DEFAULT '{}',
						attempts            INTEGER NOT NULL DEFAULT 0,
						last_error          TEXT,
						created_at          TIMESTAMPTZ NOT NULL,
						updated_at          TIMESTAMPTZ NOT NULL,
						started_at          TIMESTAMPTZ,
						created_by_user_id  VARCHAR(255),
						completed_at        TIMESTAMPTZ
					)`
			case "sqlite":
				createTable = `
					CREATE TABLE IF NOT EXISTS sidekiq (
						id                  TEXT PRIMARY KEY,
						kind                TEXT NOT NULL,
						status              TEXT NOT NULL DEFAULT 'pending',
						runner_id           TEXT,
						metadata            TEXT DEFAULT '{}',
						attempts            INTEGER NOT NULL DEFAULT 0,
						last_error          TEXT,
						created_at          DATETIME NOT NULL,
						updated_at          DATETIME NOT NULL,
						started_at          DATETIME,
						created_by_user_id  VARCHAR(255),
						completed_at        DATETIME
					)`
			default:
				// Fall back to GORM for any other dialect so the migration does not
				// hard-fail on an unsupported backend.
				if err := tx.Migrator().AutoMigrate(&tables.TableSidekiqJob{}); err != nil {
					return err
				}
				return nil
			}

			if err := tx.Exec(createTable).Error; err != nil {
				return err
			}

			// For existing tables, add new columns that were not present in the
			// original schema (no-op when the columns already exist).
			if err := addColumnIfNotExists(tx, logger, &tables.TableSidekiqJob{}, "runner_id"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &tables.TableSidekiqJob{}, "created_by_user_id"); err != nil {
				return err
			}

			// idx_sidekiq_status_updated supports the reaper/recovery scan.
			if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_sidekiq_status_updated ON sidekiq (status, updated_at)`).Error; err != nil {
				return err
			}
			// idx_sidekiq_runner supports fencing lookups by runner_id.
			return tx.Exec(`CREATE INDEX IF NOT EXISTS idx_sidekiq_runner ON sidekiq (runner_id)`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			// It is okay to drop the table
			return tx.Exec(`DROP TABLE IF EXISTS sidekiq`).Error
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running %s migration: %w", migrationName, err)
	}
	return nil
}

// migrationAddSidekiqKindStatusCreatedIndex adds a composite index on
// (kind, status, created_at) so GetInFlightSidekiqJobByKind — which filters by
// kind + status and orders by created_at DESC — can seek instead of doing a
// filtered full scan plus sort as the table accumulates historical jobs.
// Idempotent via CREATE INDEX IF NOT EXISTS; works for postgres and sqlite.
//
// The index is built non-transactionally (UseTransaction=false) with CREATE
// INDEX CONCURRENTLY on postgres so it does not take a ShareLock that blocks
// concurrent writes to the sidekiq table for the duration of the build. SQLite
// does not support CONCURRENTLY, so it uses the plain form there.
func migrationAddSidekiqKindStatusCreatedIndex(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_sidekiq_kind_status_created_index"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	noTxOpts := *migrator.DefaultOptions
	noTxOpts.UseTransaction = false
	if err := RunSingleMigration(ctx, &noTxOpts, db, logger, &migrator.Migration{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			// SQLite does not support CONCURRENTLY; use the plain form there.
			var stmt string
			if tx.Dialector.Name() == "sqlite" {
				stmt = "CREATE INDEX IF NOT EXISTS idx_sidekiq_kind_status_created ON sidekiq (kind, status, created_at)"
			} else {
				stmt = "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_sidekiq_kind_status_created ON sidekiq (kind, status, created_at)"
			}
			return tx.Exec(stmt).Error
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return tx.Exec(`DROP INDEX IF EXISTS idx_sidekiq_kind_status_created`).Error
		},
	}); err != nil {
		return fmt.Errorf("error running %s migration: %w", migrationName, err)
	}
	return nil
}

// migrationAddOauthConfigResourceColumn adds the RFC 8707 resource indicator to
// outbound MCP OAuth configs so authorization, token exchange, and refresh stay
// bound to the same protected MCP resource.
func migrationAddOauthConfigResourceColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_oauth_config_resource_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return addColumnIfNotExists(tx, logger, &tables.TableOauthConfig{}, "resource")
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return dropColumnIfExists(tx, logger, &tables.TableOauthConfig{}, "resource")
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running %s migration: %w", migrationName, err)
	}
	return nil
}

// migrationAddWebhookEndpointsTable creates the config_webhook_endpoints table.
func migrationAddWebhookEndpointsTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_webhook_endpoints_table"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			if !mg.HasTable(&tables.TableWebhookEndpoint{}) {
				if err := mg.CreateTable(&tables.TableWebhookEndpoint{}); err != nil {
					return fmt.Errorf("create config_webhook_endpoints table: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return tx.Migrator().DropTable(&tables.TableWebhookEndpoint{})
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running webhook endpoints table migration: %s", err.Error())
	}
	return nil
}

// migrationAddWebhookConfigClientColumn adds the webhook_config_json column
// to config_client.
func migrationAddWebhookConfigClientColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_webhook_config_client_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			if !mg.HasColumn(&tables.TableClientConfig{}, "webhook_config_json") {
				if err := mg.AddColumn(&tables.TableClientConfig{}, "WebhookConfigJSON"); err != nil {
					return fmt.Errorf("add webhook_config_json column: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			if mg.HasColumn(&tables.TableClientConfig{}, "webhook_config_json") {
				if err := mg.DropColumn(&tables.TableClientConfig{}, "WebhookConfigJSON"); err != nil {
					return fmt.Errorf("drop webhook_config_json column: %w", err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running webhook config client column migration: %s", err.Error())
	}
	return nil
}

// migrationAddWebhookJobsTable creates the webhook_jobs work-queue table.
func migrationAddWebhookJobsTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_webhook_jobs_table"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()
			if !mg.HasTable(&tables.TableWebhookJob{}) {
				if err := mg.CreateTable(&tables.TableWebhookJob{}); err != nil {
					return fmt.Errorf("create webhook_jobs table: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return tx.Migrator().DropTable(&tables.TableWebhookJob{})
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running webhook jobs table migration: %s", err.Error())
	}
	return nil
}
