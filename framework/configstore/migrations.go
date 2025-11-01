package configstore

import (
	"context"
	"fmt"
	"strconv"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/migrator"
	"gorm.io/gorm"
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