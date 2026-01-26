package configstore

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/migrator"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Migrate performs the necessary database migrations.
func triggerMigrations(ctx context.Context, db *gorm.DB) error {
	if err := migrationInit(ctx, db); err != nil {
		return err
	}
	if err := migrationMany2ManyJoinTable(ctx, db); err != nil {
		return err
	}
	if err := migrationAddCustomProviderConfigJSONColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddVirtualKeyProviderConfigTable(ctx, db); err != nil {
		return err
	}
	if err := migrationAddAllowedOriginsJSONColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddAllowDirectKeysColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddEnableLiteLLMFallbacksColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationTeamsTableUpdates(ctx, db); err != nil {
		return err
	}
	if err := migrationAddKeyNameColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddFrameworkConfigsTable(ctx, db); err != nil {
		return err
	}
	if err := migrationCleanupMCPClientToolsConfig(ctx, db); err != nil {
		return err
	}
	if err := migrationAddVirtualKeyMCPConfigsTable(ctx, db); err != nil {
		return err
	}
	if err := migrationAddPluginPathColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddProviderConfigBudgetRateLimit(ctx, db); err != nil {
		return err
	}
	if err := migrationAddSessionsTable(ctx, db); err != nil {
		return err
	}
	if err := migrationAddHeadersJSONColumnIntoMCPClient(ctx, db); err != nil {
		return err
	}
	if err := migrationAddDisableContentLoggingColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddMCPClientIDColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddVertexProjectNumberColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddVertexDeploymentsJSONColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationMissingProviderColumnInKeyTable(ctx, db); err != nil {
		return err
	}
	if err := migrationAddToolsToAutoExecuteJSONColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddIsCodeModeClientColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddLogRetentionDaysColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddEnabledColumnToKeyTable(ctx, db); err != nil {
		return err
	}
	if err := migrationAddBatchAndCachePricingColumns(ctx, db); err != nil {
		return err
	}
	if err := migrationAddMCPAgentDepthAndMCPToolExecutionTimeoutColumns(ctx, db); err != nil {
		return err
	}
	if err := migrationAddMCPCodeModeBindingLevelColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationNormalizeMCPClientNames(ctx, db); err != nil {
		return err
	}
	if err := migrationMoveKeysToProviderConfig(ctx, db); err != nil {
		return err
	}
	if err := migrationAddPluginVersionColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddSendBackRawRequestColumns(ctx, db); err != nil {
		return err
	}
	if err := migrationAddConfigHashColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddVirtualKeyConfigHashColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddAdditionalConfigHashColumns(ctx, db); err != nil {
		return err
	}
	if err := migrationAdd200kTokenPricingColumns(ctx, db); err != nil {
		return err
	}
	if err := migrationAddImagePricingColumns(ctx, db); err != nil {
		return err
	}
	if err := migrationAddUseForBatchAPIColumnAndS3BucketsConfig(ctx, db); err != nil {
		return err
	}
	if err := migrationAddHeaderFilterConfigJSONColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddAzureClientIDAndClientSecretAndTenantIDColumns(ctx, db); err != nil {
		return err
	}
	if err := migrationAddDistributedLocksTable(ctx, db); err != nil {
		return err
	}
	if err := migrationAddModelConfigTable(ctx, db); err != nil {
		return err
	}
	if err := migrationAddProviderGovernanceColumns(ctx, db); err != nil {
		return err
	}
	if err := migrationAddAllowedHeadersJSONColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddDisableDBPingsInHealthColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddIsPingAvailableColumn(ctx, db); err != nil {
		return err
	}
	return nil
}

// migrationInit is the first migration
func migrationInit(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "init",		
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasTable(&tables.TableConfigHash{}) {
				if err := migrator.CreateTable(&tables.TableConfigHash{}); err != nil {
					return err
				}
			}
			// TableBudget and TableRateLimit must be created before TableProvider
			// because TableProvider has FK references to them
			if !migrator.HasTable(&tables.TableBudget{}) {
				if err := migrator.CreateTable(&tables.TableBudget{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableRateLimit{}) {
				if err := migrator.CreateTable(&tables.TableRateLimit{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableProvider{}) {
				if err := migrator.CreateTable(&tables.TableProvider{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableKey{}) {
				if err := migrator.CreateTable(&tables.TableKey{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableModel{}) {
				if err := migrator.CreateTable(&tables.TableModel{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableMCPClient{}) {
				if err := migrator.CreateTable(&tables.TableMCPClient{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableClientConfig{}) {
				if err := migrator.CreateTable(&tables.TableClientConfig{}); err != nil {
					return err
				}
			} else if !migrator.HasColumn(&tables.TableClientConfig{}, "max_request_body_size_mb") {
				if err := migrator.AddColumn(&tables.TableClientConfig{}, "max_request_body_size_mb"); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableEnvKey{}) {
				if err := migrator.CreateTable(&tables.TableEnvKey{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableVectorStoreConfig{}) {
				if err := migrator.CreateTable(&tables.TableVectorStoreConfig{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableLogStoreConfig{}) {
				if err := migrator.CreateTable(&tables.TableLogStoreConfig{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableCustomer{}) {
				if err := migrator.CreateTable(&tables.TableCustomer{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableTeam{}) {
				if err := migrator.CreateTable(&tables.TableTeam{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableVirtualKey{}) {
				if err := migrator.CreateTable(&tables.TableVirtualKey{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableGovernanceConfig{}) {
				if err := migrator.CreateTable(&tables.TableGovernanceConfig{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TableModelPricing{}) {
				if err := migrator.CreateTable(&tables.TableModelPricing{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&tables.TablePlugin{}) {
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
			if err := migrator.DropTable(&tables.TableVirtualKey{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&tables.TableKey{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&tables.TableTeam{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&tables.TableProvider{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&tables.TableCustomer{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&tables.TableBudget{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&tables.TableRateLimit{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&tables.TableModel{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&tables.TableMCPClient{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&tables.TableClientConfig{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&tables.TableEnvKey{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&tables.TableVectorStoreConfig{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&tables.TableLogStoreConfig{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&tables.TableGovernanceConfig{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&tables.TableModelPricing{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&tables.TablePlugin{}); err != nil {
				return err
			}
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
func migrationMany2ManyJoinTable(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "many2manyjoin",
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
				if err := tx.Exec(createJoinTableSQL).Error; err != nil {
					return fmt.Errorf("failed to create governance_virtual_key_keys table: %w", err)
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
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
func migrationAddCustomProviderConfigJSONColumn(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "addcustomproviderconfigjsoncolumn",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			if !migrator.HasColumn(&tables.TableProvider{}, "custom_provider_config_json") {
				if err := migrator.AddColumn(&tables.TableProvider{}, "custom_provider_config_json"); err != nil {
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

// migrationAddVirtualKeyProviderConfigTable adds the virtual_key_provider_config table
func migrationAddVirtualKeyProviderConfigTable(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "addvirtualkeyproviderconfig",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			if !migrator.HasTable(&tables.TableVirtualKeyProviderConfig{}) {
				if err := migrator.CreateTable(&tables.TableVirtualKeyProviderConfig{}); err != nil {
					return err
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

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
func migrationAddAllowedOriginsJSONColumn(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_allowed_origins_json_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			if !migrator.HasColumn(&tables.TableClientConfig{}, "allowed_origins_json") {
				if err := migrator.AddColumn(&tables.TableClientConfig{}, "allowed_origins_json"); err != nil {
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

// migrationAddAllowDirectKeysColumn adds the allow_direct_keys column to the client config table
func migrationAddAllowDirectKeysColumn(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_allow_direct_keys_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			if !migrator.HasColumn(&tables.TableClientConfig{}, "allow_direct_keys") {
				if err := migrator.AddColumn(&tables.TableClientConfig{}, "allow_direct_keys"); err != nil {
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

// migrationAddEnableLiteLLMFallbacksColumn adds the enable_litellm_fallbacks column to the client config table
func migrationAddEnableLiteLLMFallbacksColumn(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_enable_litellm_fallbacks_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&tables.TableClientConfig{}, "enable_litellm_fallbacks") {
				if err := migrator.AddColumn(&tables.TableClientConfig{}, "enable_litellm_fallbacks"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			if err := migrator.DropColumn(&tables.TableClientConfig{}, "enable_litellm_fallbacks"); err != nil {
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
func migrationTeamsTableUpdates(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_profile_config_claims_columns_to_team_table",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&tables.TableTeam{}, "profile") {
				if err := migrator.AddColumn(&tables.TableTeam{}, "profile"); err != nil {
					return err
				}
			}
			if !migrator.HasColumn(&tables.TableTeam{}, "config") {
				if err := migrator.AddColumn(&tables.TableTeam{}, "config"); err != nil {
					return err
				}
			}
			if !migrator.HasColumn(&tables.TableTeam{}, "claims") {
				if err := migrator.AddColumn(&tables.TableTeam{}, "claims"); err != nil {
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

// migrationAddFrameworkConfigsTable adds the framework_configs table
func migrationAddFrameworkConfigsTable(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_framework_configs_table",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasTable(&tables.TableFrameworkConfig{}) {
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

// migrationAddKeyNameColumn adds the name column to the key table and populates unique names
func migrationAddKeyNameColumn(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_key_name_column",
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
			migrator := tx.Migrator()
			// Drop the unique index first to avoid orphaned index artifacts
			if err := tx.Exec("DROP INDEX IF EXISTS idx_key_name").Error; err != nil {
				return err
			}
			if err := migrator.DropColumn(&tables.TableKey{}, "name"); err != nil {
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
func migrationCleanupMCPClientToolsConfig(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "cleanup_mcp_client_tools_config",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			// Step 1: Remove ToolsToSkipJSON column if it exists (cleanup from old versions)
			if migrator.HasColumn(&tables.TableMCPClient{}, "tools_to_skip_json") {
				if err := migrator.DropColumn(&tables.TableMCPClient{}, "tools_to_skip_json"); err != nil {
					return fmt.Errorf("failed to drop tools_to_skip_json column: %w", err)
				}
			}

			// Alternative column name variations that might exist
			if migrator.HasColumn(&tables.TableMCPClient{}, "ToolsToSkipJSON") {
				if err := migrator.DropColumn(&tables.TableMCPClient{}, "ToolsToSkipJSON"); err != nil {
					return fmt.Errorf("failed to drop ToolsToSkipJSON column: %w", err)
				}
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
func migrationAddVirtualKeyMCPConfigsTable(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_vk_mcp_configs_table",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasTable(&tables.TableVirtualKeyMCPConfig{}) {
				if err := migrator.CreateTable(&tables.TableVirtualKeyMCPConfig{}); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
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
func migrationAddProviderConfigBudgetRateLimit(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_provider_config_budget_rate_limit",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			// Add BudgetID column if it doesn't exist
			if migrator.HasTable(&tables.TableVirtualKeyProviderConfig{}) {
				if !migrator.HasColumn(&tables.TableVirtualKeyProviderConfig{}, "budget_id") {
					if err := migrator.AddColumn(&tables.TableVirtualKeyProviderConfig{}, "budget_id"); err != nil {
						return fmt.Errorf("failed to add budget_id column: %w", err)
					}
				}

				// Add RateLimitID column if it doesn't exist
				if !migrator.HasColumn(&tables.TableVirtualKeyProviderConfig{}, "rate_limit_id") {
					if err := migrator.AddColumn(&tables.TableVirtualKeyProviderConfig{}, "rate_limit_id"); err != nil {
						return fmt.Errorf("failed to add rate_limit_id column: %w", err)
					}
				}

				// Create foreign key indexes for better performance
				if !migrator.HasIndex(&tables.TableVirtualKeyProviderConfig{}, "idx_provider_config_budget") {
					if err := tx.Exec("CREATE INDEX IF NOT EXISTS idx_provider_config_budget ON governance_virtual_key_provider_configs (budget_id)").Error; err != nil {
						return fmt.Errorf("failed to create budget_id index: %w", err)
					}
				}

				if !migrator.HasIndex(&tables.TableVirtualKeyProviderConfig{}, "idx_provider_config_rate_limit") {
					if err := tx.Exec("CREATE INDEX IF NOT EXISTS idx_provider_config_rate_limit ON governance_virtual_key_provider_configs (rate_limit_id)").Error; err != nil {
						return fmt.Errorf("failed to create rate_limit_id index: %w", err)
					}
				}

				// Create FK constraints (dialect‑agnostic)
				if !migrator.HasConstraint(&tables.TableVirtualKeyProviderConfig{}, "Budget") {
					if err := migrator.CreateConstraint(&tables.TableVirtualKeyProviderConfig{}, "Budget"); err != nil {
						return fmt.Errorf("failed to create Budget FK constraint: %w", err)
					}
				}
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

			// Drop indexes first
			if err := tx.Exec("DROP INDEX IF EXISTS idx_provider_config_budget").Error; err != nil {
				return fmt.Errorf("failed to drop budget_id index: %w", err)
			}
			if err := tx.Exec("DROP INDEX IF EXISTS idx_provider_config_rate_limit").Error; err != nil {
				return fmt.Errorf("failed to drop rate_limit_id index: %w", err)
			}

			// Drop FK constraints
			if migrator.HasConstraint(&tables.TableVirtualKeyProviderConfig{}, "Budget") {
				if err := migrator.DropConstraint(&tables.TableVirtualKeyProviderConfig{}, "Budget"); err != nil {
					return fmt.Errorf("failed to drop Budget FK constraint: %w", err)
				}
			}
			if migrator.HasConstraint(&tables.TableVirtualKeyProviderConfig{}, "RateLimit") {
				if err := migrator.DropConstraint(&tables.TableVirtualKeyProviderConfig{}, "RateLimit"); err != nil {
					return fmt.Errorf("failed to drop RateLimit FK constraint: %w", err)
				}
			}

			// Drop columns
			if migrator.HasColumn(&tables.TableVirtualKeyProviderConfig{}, "budget_id") {
				if err := migrator.DropColumn(&tables.TableVirtualKeyProviderConfig{}, "budget_id"); err != nil {
					return fmt.Errorf("failed to drop budget_id column: %w", err)
				}
			}
			if migrator.HasColumn(&tables.TableVirtualKeyProviderConfig{}, "rate_limit_id") {
				if err := migrator.DropColumn(&tables.TableVirtualKeyProviderConfig{}, "rate_limit_id"); err != nil {
					return fmt.Errorf("failed to drop rate_limit_id column: %w", err)
				}
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
func migrationAddPluginPathColumn(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "update_plugins_table_for_custom_plugins",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&tables.TablePlugin{}, "path") {
				if err := migrator.AddColumn(&tables.TablePlugin{}, "path"); err != nil {
					return err
				}
			}
			if !migrator.HasColumn(&tables.TablePlugin{}, "is_custom") {
				if err := migrator.AddColumn(&tables.TablePlugin{}, "is_custom"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if err := migrator.DropColumn(&tables.TablePlugin{}, "path"); err != nil {
				return err
			}
			if err := migrator.DropColumn(&tables.TablePlugin{}, "is_custom"); err != nil {
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
func migrationAddSessionsTable(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_sessions_table",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasTable(&tables.SessionsTable{}) {
				if err := migrator.CreateTable(&tables.SessionsTable{}); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
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
func migrationAddHeadersJSONColumnIntoMCPClient(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_headers_json_column_into_mcp_client",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&tables.TableMCPClient{}, "headers_json") {
				if err := migrator.AddColumn(&tables.TableMCPClient{}, "headers_json"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if err := migrator.DropColumn(&tables.TableMCPClient{}, "headers_json"); err != nil {
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
func migrationAddDisableContentLoggingColumn(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_disable_content_logging_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&tables.TableClientConfig{}, "disable_content_logging") {
				if err := migrator.AddColumn(&tables.TableClientConfig{}, "disable_content_logging"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if err := migrator.DropColumn(&tables.TableClientConfig{}, "disable_content_logging"); err != nil {
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
func migrationAddMCPClientIDColumn(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_mcp_client_id_column",
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
			migrator := tx.Migrator()

			// Drop the unique index first to avoid orphaned index artifacts
			if err := tx.Exec("DROP INDEX IF EXISTS idx_mcp_client_id").Error; err != nil {
				return fmt.Errorf("failed to drop client_id index: %w", err)
			}

			if err := migrator.DropColumn(&tables.TableMCPClient{}, "client_id"); err != nil {
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
func migrationAddVertexProjectNumberColumn(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_vertex_project_number_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&tables.TableKey{}, "vertex_project_number") {
				if err := migrator.AddColumn(&tables.TableKey{}, "vertex_project_number"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if err := migrator.DropColumn(&tables.TableKey{}, "vertex_project_number"); err != nil {
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

// migrationAddVertexDeploymentsJSONColumn adds the vertex_deployments_json column to the key table
func migrationAddVertexDeploymentsJSONColumn(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_vertex_deployments_json_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&tables.TableKey{}, "vertex_deployments_json") {
				if err := migrator.AddColumn(&tables.TableKey{}, "vertex_deployments_json"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if err := migrator.DropColumn(&tables.TableKey{}, "vertex_deployments_json"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running vertex deployments JSON migration: %s", err.Error())
	}
	return nil
}

func migrationMissingProviderColumnInKeyTable(ctx context.Context, db *gorm.DB) error {
	options := &migrator.Options{
		TableName:                 migrator.DefaultOptions.TableName,
		IDColumnName:              migrator.DefaultOptions.IDColumnName,
		IDColumnSize:              migrator.DefaultOptions.IDColumnSize,
		UseTransaction:            true,
		ValidateUnknownMigrations: migrator.DefaultOptions.ValidateUnknownMigrations,
	}
	m := migrator.New(db, options, []*migrator.Migration{{
		ID: "add_and_fill_provider_column_in_key_table",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			// Step 1: Add the provider column if it doesn't exist
			if migrator.HasColumn(&tables.TableKey{}, "provider") {
				return nil
			}
			if err := migrator.AddColumn(&tables.TableKey{}, "provider"); err != nil {
				return fmt.Errorf("failed to add provider column: %w", err)
			}

			// Step 2: Find all keys where provider is empty/null but provider_id is set
			var keys []tables.TableKey
			if err := tx.Where("provider IS NULL OR provider = ''").Find(&keys).Error; err != nil {
				return fmt.Errorf("failed to fetch keys with missing provider: %w", err)
			}

			// Step 3: Update each key with the provider name from the provider table
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
			migrator := tx.Migrator()
			if err := migrator.DropColumn(&tables.TableKey{}, "provider"); err != nil {
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
func migrationAddToolsToAutoExecuteJSONColumn(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_tools_to_auto_execute_json_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&tables.TableMCPClient{}, "tools_to_auto_execute_json") {
				if err := migrator.AddColumn(&tables.TableMCPClient{}, "tools_to_auto_execute_json"); err != nil {
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
			migrator := tx.Migrator()
			if err := migrator.DropColumn(&tables.TableMCPClient{}, "tools_to_auto_execute_json"); err != nil {
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
func migrationAddIsCodeModeClientColumn(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_is_code_mode_client_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&tables.TableMCPClient{}, "is_code_mode_client") {
				if err := migrator.AddColumn(&tables.TableMCPClient{}, "is_code_mode_client"); err != nil {
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
			migrator := tx.Migrator()
			if err := migrator.DropColumn(&tables.TableMCPClient{}, "is_code_mode_client"); err != nil {
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
func migrationAddLogRetentionDaysColumn(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_log_retention_days_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&tables.TableClientConfig{}, "log_retention_days") {
				if err := migrator.AddColumn(&tables.TableClientConfig{}, "log_retention_days"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if err := migrator.DropColumn(&tables.TableClientConfig{}, "log_retention_days"); err != nil {
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
func migrationAddEnabledColumnToKeyTable(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_enabled_column_to_key_table",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()

			// Check if column already exists
			if !mg.HasColumn(&tables.TableKey{}, "enabled") {
				// Add the column
				if err := mg.AddColumn(&tables.TableKey{}, "enabled"); err != nil {
					return fmt.Errorf("failed to add enabled column: %w", err)
				}
			}
			// Set default = true for existing rows
			if err := tx.Exec("UPDATE config_keys SET enabled = TRUE WHERE enabled IS NULL").Error; err != nil {
				return fmt.Errorf("failed to backfill enabled column: %w", err)
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()

			if mg.HasColumn(&tables.TableKey{}, "enabled") {
				if err := mg.DropColumn(&tables.TableKey{}, "enabled"); err != nil {
					return fmt.Errorf("failed to drop enabled column: %w", err)
				}
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
func migrationAddBatchAndCachePricingColumns(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "update_model_pricing_table_to_add_cache_and_batch_pricing",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&tables.TableModelPricing{}, "cache_read_input_token_cost") {
				if err := migrator.AddColumn(&tables.TableModelPricing{}, "cache_read_input_token_cost"); err != nil {
					return err
				}
			}
			if !migrator.HasColumn(&tables.TableModelPricing{}, "cache_creation_input_token_cost") {
				if err := migrator.AddColumn(&tables.TableModelPricing{}, "cache_creation_input_token_cost"); err != nil {
					return err
				}
			}
			if !migrator.HasColumn(&tables.TableModelPricing{}, "input_cost_per_token_batches") {
				if err := migrator.AddColumn(&tables.TableModelPricing{}, "input_cost_per_token_batches"); err != nil {
					return err
				}
			}
			if !migrator.HasColumn(&tables.TableModelPricing{}, "output_cost_per_token_batches") {
				if err := migrator.AddColumn(&tables.TableModelPricing{}, "output_cost_per_token_batches"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if err := migrator.DropColumn(&tables.TableModelPricing{}, "cache_read_input_token_cost"); err != nil {
				return err
			}
			if err := migrator.DropColumn(&tables.TableModelPricing{}, "cache_creation_input_token_cost"); err != nil {
				return err
			}
			if err := migrator.DropColumn(&tables.TableModelPricing{}, "input_cost_per_token_batches"); err != nil {
				return err
			}
			if err := migrator.DropColumn(&tables.TableModelPricing{}, "output_cost_per_token_batches"); err != nil {
				return err
			}
			return nil
		},
	}})
	return m.Migrate()
}

func migrationAddMCPAgentDepthAndMCPToolExecutionTimeoutColumns(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_mcp_agent_depth_and_mcp_tool_execution_timeout_columns",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&tables.TableClientConfig{}, "mcp_agent_depth") {
				if err := migrator.AddColumn(&tables.TableClientConfig{}, "mcp_agent_depth"); err != nil {
					return err
				}
			}
			if !migrator.HasColumn(&tables.TableClientConfig{}, "mcp_tool_execution_timeout") {
				if err := migrator.AddColumn(&tables.TableClientConfig{}, "mcp_tool_execution_timeout"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if err := migrator.DropColumn(&tables.TableClientConfig{}, "mcp_agent_depth"); err != nil {
				return err
			}
			if err := migrator.DropColumn(&tables.TableClientConfig{}, "mcp_tool_execution_timeout"); err != nil {
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
func migrationAddMCPCodeModeBindingLevelColumn(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_mcp_code_mode_binding_level_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migratorInstance := tx.Migrator()
			if !migratorInstance.HasColumn(&tables.TableClientConfig{}, "mcp_code_mode_binding_level") {
				if err := migratorInstance.AddColumn(&tables.TableClientConfig{}, "mcp_code_mode_binding_level"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migratorInstance := tx.Migrator()
			if err := migratorInstance.DropColumn(&tables.TableClientConfig{}, "mcp_code_mode_binding_level"); err != nil {
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
func migrationNormalizeMCPClientNames(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "normalize_mcp_client_names",
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
							log.Printf("MCP Client Name Normalized: '%s' -> '%s'", originalName, baseName)
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
							log.Printf("MCP Client Name Normalized: '%s' -> '%s'", originalName, candidateName)
							return candidateName, nil
						} else if err != nil {
							return "", fmt.Errorf("failed to check name availability: %w", err)
						}
					}
					suffix++
				}
			}

			// Process each client
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
func migrationMoveKeysToProviderConfig(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "move_keys_to_provider_config",
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
func migrationAddPluginVersionColumn(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_plugin_version_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&tables.TablePlugin{}, "version") {
				if err := migrator.AddColumn(&tables.TablePlugin{}, "version"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if err := migrator.DropColumn(&tables.TablePlugin{}, "version"); err != nil {
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

func migrationAddSendBackRawRequestColumns(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_send_back_raw_request_columns",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&tables.TableProvider{}, "send_back_raw_request") {
				if err := migrator.AddColumn(&tables.TableProvider{}, "send_back_raw_request"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if err := migrator.DropColumn(&tables.TableProvider{}, "send_back_raw_request"); err != nil {
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
func migrationAddConfigHashColumn(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_config_hash_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			// Add config_hash to providers table
			if !migrator.HasColumn(&tables.TableProvider{}, "config_hash") {
				if err := migrator.AddColumn(&tables.TableProvider{}, "config_hash"); err != nil {
					return err
				}
				// Pre-populate hashes for existing providers
				var providers []tables.TableProvider
				if err := tx.Find(&providers).Error; err != nil {
					return fmt.Errorf("failed to fetch providers for hash migration: %w", err)
				}
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
				if err := migrator.AddColumn(&tables.TableKey{}, "config_hash"); err != nil {
					return err
				}
				// Pre-populate hashes for existing keys
				var keys []tables.TableKey
				if err := tx.Find(&keys).Error; err != nil {
					return fmt.Errorf("failed to fetch keys for hash migration: %w", err)
				}
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
			migrator := tx.Migrator()
			if err := migrator.DropColumn(&tables.TableProvider{}, "config_hash"); err != nil {
				return err
			}
			if err := migrator.DropColumn(&tables.TableKey{}, "config_hash"); err != nil {
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
func migrationAddVirtualKeyConfigHashColumn(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_virtual_key_config_hash_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			// Add config_hash to virtual keys table
			if !migrator.HasColumn(&tables.TableVirtualKey{}, "config_hash") {
				if err := migrator.AddColumn(&tables.TableVirtualKey{}, "config_hash"); err != nil {
					return err
				}
				// Pre-populate hashes for existing virtual keys
				var virtualKeys []tables.TableVirtualKey
				if err := tx.Preload("ProviderConfigs").Preload("ProviderConfigs.Keys").Preload("MCPConfigs").Find(&virtualKeys).Error; err != nil {
					return fmt.Errorf("failed to fetch virtual keys for hash migration: %w", err)
				}
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
			migrator := tx.Migrator()
			if err := migrator.DropColumn(&tables.TableVirtualKey{}, "config_hash"); err != nil {
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
func migrationAddAdditionalConfigHashColumns(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_additional_config_hash_columns",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			// Add config_hash to client config table
			if !migrator.HasColumn(&tables.TableClientConfig{}, "config_hash") {
				if err := migrator.AddColumn(&tables.TableClientConfig{}, "config_hash"); err != nil {
					return err
				}
				// Pre-populate hashes for existing client configs
				var clientConfigs []tables.TableClientConfig
				if err := tx.Find(&clientConfigs).Error; err != nil {
					return fmt.Errorf("failed to fetch client configs for hash migration: %w", err)
				}
				for _, cc := range clientConfigs {
					if cc.ConfigHash == "" {
						clientConfig := ClientConfig{
							DropExcessRequests:      cc.DropExcessRequests,
							InitialPoolSize:         cc.InitialPoolSize,
							PrometheusLabels:        cc.PrometheusLabels,
							EnableLogging:           cc.EnableLogging,
							DisableContentLogging:   cc.DisableContentLogging,
							LogRetentionDays:        cc.LogRetentionDays,
							EnableGovernance:        cc.EnableGovernance,
							EnforceGovernanceHeader: cc.EnforceGovernanceHeader,
							AllowDirectKeys:         cc.AllowDirectKeys,
							AllowedOrigins:          cc.AllowedOrigins,
							MaxRequestBodySizeMB:    cc.MaxRequestBodySizeMB,
							EnableLiteLLMFallbacks:  cc.EnableLiteLLMFallbacks,
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
				if err := migrator.AddColumn(&tables.TableBudget{}, "config_hash"); err != nil {
					return err
				}
				// Pre-populate hashes for existing budgets
				var budgets []tables.TableBudget
				if err := tx.Find(&budgets).Error; err != nil {
					return fmt.Errorf("failed to fetch budgets for hash migration: %w", err)
				}
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
				if err := migrator.AddColumn(&tables.TableRateLimit{}, "config_hash"); err != nil {
					return err
				}
				// Pre-populate hashes for existing rate limits
				var rateLimits []tables.TableRateLimit
				if err := tx.Find(&rateLimits).Error; err != nil {
					return fmt.Errorf("failed to fetch rate limits for hash migration: %w", err)
				}
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
				if err := migrator.AddColumn(&tables.TableCustomer{}, "config_hash"); err != nil {
					return err
				}
				// Pre-populate hashes for existing customers
				var customers []tables.TableCustomer
				if err := tx.Find(&customers).Error; err != nil {
					return fmt.Errorf("failed to fetch customers for hash migration: %w", err)
				}
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
				if err := migrator.AddColumn(&tables.TableTeam{}, "config_hash"); err != nil {
					return err
				}
				// Pre-populate hashes for existing teams
				var teams []tables.TableTeam
				if err := tx.Find(&teams).Error; err != nil {
					return fmt.Errorf("failed to fetch teams for hash migration: %w", err)
				}
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
				if err := migrator.AddColumn(&tables.TableMCPClient{}, "config_hash"); err != nil {
					return err
				}
				// Pre-populate hashes for existing MCP clients
				var mcpClients []tables.TableMCPClient
				if err := tx.Find(&mcpClients).Error; err != nil {
					return fmt.Errorf("failed to fetch MCP clients for hash migration: %w", err)
				}
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
				if err := migrator.AddColumn(&tables.TablePlugin{}, "config_hash"); err != nil {
					return err
				}
				// Pre-populate hashes for existing plugins
				var plugins []tables.TablePlugin
				if err := tx.Find(&plugins).Error; err != nil {
					return fmt.Errorf("failed to fetch plugins for hash migration: %w", err)
				}
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
			migrator := tx.Migrator()
			if err := migrator.DropColumn(&tables.TableClientConfig{}, "config_hash"); err != nil {
				return err
			}
			if err := migrator.DropColumn(&tables.TableBudget{}, "config_hash"); err != nil {
				return err
			}
			if err := migrator.DropColumn(&tables.TableRateLimit{}, "config_hash"); err != nil {
				return err
			}
			if err := migrator.DropColumn(&tables.TableCustomer{}, "config_hash"); err != nil {
				return err
			}
			if err := migrator.DropColumn(&tables.TableTeam{}, "config_hash"); err != nil {
				return err
			}
			if err := migrator.DropColumn(&tables.TableMCPClient{}, "config_hash"); err != nil {
				return err
			}
			if err := migrator.DropColumn(&tables.TablePlugin{}, "config_hash"); err != nil {
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
func migrationAdd200kTokenPricingColumns(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_200k_token_pricing_columns",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			columns := []string{
				"input_cost_per_token_above_200k_tokens",
				"output_cost_per_token_above_200k_tokens",
				"cache_creation_input_token_cost_above_200k_tokens",
				"cache_read_input_token_cost_above_200k_tokens",
			}

			for _, field := range columns {
				if !migrator.HasColumn(&tables.TableModelPricing{}, field) {
					if err := migrator.AddColumn(&tables.TableModelPricing{}, field); err != nil {
						return fmt.Errorf("failed to add column %s: %w", field, err)
					}
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			columns := []string{
				"input_cost_per_token_above_200k_tokens",
				"output_cost_per_token_above_200k_tokens",
				"cache_creation_input_token_cost_above_200k_tokens",
				"cache_read_input_token_cost_above_200k_tokens",
			}

			for _, field := range columns {
				if migrator.HasColumn(&tables.TableModelPricing{}, field) {
					if err := migrator.DropColumn(&tables.TableModelPricing{}, field); err != nil {
						return fmt.Errorf("failed to drop column %s: %w", field, err)
					}
				}
			}
			return nil
		},
	}})
	return m.Migrate()
}

// migrationAddImagePricingColumns adds the image generation pricing columns to the model_pricing table
func migrationAddImagePricingColumns(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_image_pricing_columns",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			columns := []string{
				"input_cost_per_image_token",
				"output_cost_per_image_token",
				"input_cost_per_image",
				"output_cost_per_image",
				"cache_read_input_image_token_cost",
			}

			for _, field := range columns {
				if !migrator.HasColumn(&tables.TableModelPricing{}, field) {
					if err := migrator.AddColumn(&tables.TableModelPricing{}, field); err != nil {
						return fmt.Errorf("failed to add column %s: %w", field, err)
					}
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			columns := []string{
				"input_cost_per_image_token",
				"output_cost_per_image_token",
				"input_cost_per_image",
				"output_cost_per_image",
				"cache_read_input_image_token_cost",
			}

			for _, field := range columns {
				if migrator.HasColumn(&tables.TableModelPricing{}, field) {
					if err := migrator.DropColumn(&tables.TableModelPricing{}, field); err != nil {
						return fmt.Errorf("failed to drop column %s: %w", field, err)
					}
				}
			}
			return nil
		},
	}})
	return m.Migrate()
}

// migrationAddUseForBatchAPIColumnAndS3BucketsConfig adds the use_for_batch_api and bedrock_batch_s3_config_json columns to the config_keys table
// Existing keys are backfilled with use_for_batch_api = TRUE to preserve current behavior
func migrationAddUseForBatchAPIColumnAndS3BucketsConfig(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_use_for_batch_api_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()

			// Add use_for_batch_api column
			if !mg.HasColumn(&tables.TableKey{}, "use_for_batch_api") {
				if err := mg.AddColumn(&tables.TableKey{}, "use_for_batch_api"); err != nil {
					return fmt.Errorf("failed to add use_for_batch_api column: %w", err)
				}
			}

			// Add bedrock_batch_s3_config_json column
			if !mg.HasColumn(&tables.TableKey{}, "bedrock_batch_s3_config_json") {
				if err := mg.AddColumn(&tables.TableKey{}, "bedrock_batch_s3_config_json"); err != nil {
					return fmt.Errorf("failed to add bedrock_batch_s3_config_json column: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()

			if mg.HasColumn(&tables.TableKey{}, "use_for_batch_api") {
				if err := mg.DropColumn(&tables.TableKey{}, "use_for_batch_api"); err != nil {
					return fmt.Errorf("failed to drop use_for_batch_api column: %w", err)
				}
			}

			if mg.HasColumn(&tables.TableKey{}, "bedrock_batch_s3_config_json") {
				if err := mg.DropColumn(&tables.TableKey{}, "bedrock_batch_s3_config_json"); err != nil {
					return fmt.Errorf("failed to drop bedrock_batch_s3_config_json column: %w", err)
				}
			}

			return nil
		},
	}})

	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running use_for_batch_api migration: %s", err.Error())
	}
	return nil
}

// migrationAddHeaderFilterConfigJSONColumn adds the header_filter_config_json column to the config_client table
func migrationAddHeaderFilterConfigJSONColumn(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_header_filter_config_json_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()

			if !mg.HasColumn(&tables.TableClientConfig{}, "header_filter_config_json") {
				if err := mg.AddColumn(&tables.TableClientConfig{}, "header_filter_config_json"); err != nil {
					return fmt.Errorf("failed to add header_filter_config_json column: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mg := tx.Migrator()

			if mg.HasColumn(&tables.TableClientConfig{}, "header_filter_config_json") {
				if err := mg.DropColumn(&tables.TableClientConfig{}, "header_filter_config_json"); err != nil {
					return fmt.Errorf("failed to drop header_filter_config_json column: %w", err)
				}
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
func migrationAddAzureClientIDAndClientSecretAndTenantIDColumns(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_azure_client_id_and_client_secret_and_tenant_id_columns",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&tables.TableKey{}, "azure_client_id") {
				if err := migrator.AddColumn(&tables.TableKey{}, "azure_client_id"); err != nil {
					return fmt.Errorf("failed to add azure_client_id column: %w", err)
				}
			}
			if !migrator.HasColumn(&tables.TableKey{}, "azure_client_secret") {
				if err := migrator.AddColumn(&tables.TableKey{}, "azure_client_secret"); err != nil {
					return fmt.Errorf("failed to add azure_client_secret column: %w", err)
				}
			}
			if !migrator.HasColumn(&tables.TableKey{}, "azure_tenant_id") {
				if err := migrator.AddColumn(&tables.TableKey{}, "azure_tenant_id"); err != nil {
					return fmt.Errorf("failed to add azure_tenant_id column: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if err := migrator.DropColumn(&tables.TableKey{}, "azure_client_id"); err != nil {
				return fmt.Errorf("failed to drop azure_client_id column: %w", err)
			}
			if err := migrator.DropColumn(&tables.TableKey{}, "azure_client_secret"); err != nil {
				return fmt.Errorf("failed to drop azure_client_secret column: %w", err)
			}
			if err := migrator.DropColumn(&tables.TableKey{}, "azure_tenant_id"); err != nil {
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

// migrationAddDistributedLocksTable adds the distributed_locks table for distributed locking
func migrationAddDistributedLocksTable(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_distributed_locks_table",
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
func migrationAddModelConfigTable(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_model_config_table",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasTable(&tables.TableModelConfig{}) {
				if err := migrator.CreateTable(&tables.TableModelConfig{}); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
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
func migrationAddProviderGovernanceColumns(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_provider_governance_columns",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			provider := &tables.TableProvider{}

			// Add budget_id column if it doesn't exist
			if !migrator.HasColumn(provider, "budget_id") {
				if err := migrator.AddColumn(provider, "budget_id"); err != nil {
					return fmt.Errorf("failed to add budget_id column: %w", err)
				}
			}
			// Create index for budget_id (outside HasColumn to handle reruns where column exists but index doesn't)
			if !migrator.HasIndex(provider, "idx_provider_budget") {
				if err := tx.Exec("CREATE INDEX IF NOT EXISTS idx_provider_budget ON config_providers (budget_id)").Error; err != nil {
					return fmt.Errorf("failed to create budget_id index: %w", err)
				}
			}

			// Add rate_limit_id column if it doesn't exist
			if !migrator.HasColumn(provider, "rate_limit_id") {
				if err := migrator.AddColumn(provider, "rate_limit_id"); err != nil {
					return fmt.Errorf("failed to add rate_limit_id column: %w", err)
				}
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
			if migrator.HasColumn(provider, "rate_limit_id") {
				if err := migrator.DropColumn(provider, "rate_limit_id"); err != nil {
					return fmt.Errorf("failed to drop rate_limit_id column: %w", err)
				}
			}

			// Drop budget_id column if it exists
			if migrator.HasColumn(provider, "budget_id") {
				if err := migrator.DropColumn(provider, "budget_id"); err != nil {
					return fmt.Errorf("failed to drop budget_id column: %w", err)
				}
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

// migrationAddAllowedHeadersJSONColumn adds the allowed_headers_json column to the client config table
func migrationAddAllowedHeadersJSONColumn(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_allowed_headers_json_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&tables.TableClientConfig{}, "allowed_headers_json") {
				if err := migrator.AddColumn(&tables.TableClientConfig{}, "allowed_headers_json"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if migrator.HasColumn(&tables.TableClientConfig{}, "allowed_headers_json") {
				if err := migrator.DropColumn(&tables.TableClientConfig{}, "allowed_headers_json"); err != nil {
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

// migrationAddDisableDBPingsInHealthColumn adds the disable_db_pings_in_health column to the client config table
func migrationAddDisableDBPingsInHealthColumn(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_disable_db_pings_in_health_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&tables.TableClientConfig{}, "disable_db_pings_in_health") {
				if err := migrator.AddColumn(&tables.TableClientConfig{}, "disable_db_pings_in_health"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if migrator.HasColumn(&tables.TableClientConfig{}, "disable_db_pings_in_health") {
				if err := migrator.DropColumn(&tables.TableClientConfig{}, "disable_db_pings_in_health"); err != nil {
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

// migrationAddIsPingAvailableColumn adds the is_ping_available column to the config_mcp_clients table
func migrationAddIsPingAvailableColumn(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_is_ping_available_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&tables.TableMCPClient{}, "is_ping_available") {
				if err := migrator.AddColumn(&tables.TableMCPClient{}, "is_ping_available"); err != nil {
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
			migrator := tx.Migrator()
			if migrator.HasColumn(&tables.TableMCPClient{}, "is_ping_available") {
				if err := migrator.DropColumn(&tables.TableMCPClient{}, "is_ping_available"); err != nil {
					return err
				}
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
