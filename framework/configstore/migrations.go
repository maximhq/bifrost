package configstore

import (
	"context"
	"fmt"

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
	if err := migrationAddOpenAIUseResponsesAPIColumn(ctx, db); err != nil {
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
	return nil
}

// migrationInit is the first migration
func migrationInit(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "init",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasTable(&TableConfigHash{}) {
				if err := migrator.CreateTable(&TableConfigHash{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&TableProvider{}) {
				if err := migrator.CreateTable(&TableProvider{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&TableKey{}) {
				if err := migrator.CreateTable(&TableKey{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&TableModel{}) {
				if err := migrator.CreateTable(&TableModel{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&TableMCPClient{}) {
				if err := migrator.CreateTable(&TableMCPClient{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&TableClientConfig{}) {
				if err := migrator.CreateTable(&TableClientConfig{}); err != nil {
					return err
				}
			} else if !migrator.HasColumn(&TableClientConfig{}, "max_request_body_size_mb") {
				if err := migrator.AddColumn(&TableClientConfig{}, "max_request_body_size_mb"); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&TableEnvKey{}) {
				if err := migrator.CreateTable(&TableEnvKey{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&TableVectorStoreConfig{}) {
				if err := migrator.CreateTable(&TableVectorStoreConfig{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&TableLogStoreConfig{}) {
				if err := migrator.CreateTable(&TableLogStoreConfig{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&TableBudget{}) {
				if err := migrator.CreateTable(&TableBudget{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&TableRateLimit{}) {
				if err := migrator.CreateTable(&TableRateLimit{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&TableCustomer{}) {
				if err := migrator.CreateTable(&TableCustomer{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&TableTeam{}) {
				if err := migrator.CreateTable(&TableTeam{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&TableVirtualKey{}) {
				if err := migrator.CreateTable(&TableVirtualKey{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&TableConfig{}) {
				if err := migrator.CreateTable(&TableConfig{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&TableModelPricing{}) {
				if err := migrator.CreateTable(&TableModelPricing{}); err != nil {
					return err
				}
			}
			if !migrator.HasTable(&TablePlugin{}) {
				if err := migrator.CreateTable(&TablePlugin{}); err != nil {
					return err
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			// Drop children first, then parents (adjust if your actual FKs differ)
			if err := migrator.DropTable(&TableVirtualKey{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&TableKey{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&TableTeam{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&TableProvider{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&TableCustomer{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&TableBudget{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&TableRateLimit{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&TableModel{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&TableMCPClient{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&TableClientConfig{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&TableEnvKey{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&TableVectorStoreConfig{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&TableLogStoreConfig{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&TableConfig{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&TableModelPricing{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&TablePlugin{}); err != nil {
				return err
			}
			if err := migrator.DropTable(&TableConfigHash{}); err != nil {
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

			if !migrator.HasColumn(&TableProvider{}, "custom_provider_config_json") {
				if err := migrator.AddColumn(&TableProvider{}, "custom_provider_config_json"); err != nil {
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

			if !migrator.HasTable(&TableVirtualKeyProviderConfig{}) {
				if err := migrator.CreateTable(&TableVirtualKeyProviderConfig{}); err != nil {
					return err
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			if err := migrator.DropTable(&TableVirtualKeyProviderConfig{}); err != nil {
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

// migrationAddOpenAIUseResponsesAPIColumn adds the open_ai_use_responses_api column to the key table
func migrationAddOpenAIUseResponsesAPIColumn(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_open_ai_use_responses_api_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			if !migrator.HasColumn(&TableKey{}, "open_ai_use_responses_api") {
				if err := migrator.AddColumn(&TableKey{}, "open_ai_use_responses_api"); err != nil {
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

// migrationAddAllowedOriginsJSONColumn adds the allowed_origins_json column to the client config table
func migrationAddAllowedOriginsJSONColumn(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "add_allowed_origins_json_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			if !migrator.HasColumn(&TableClientConfig{}, "allowed_origins_json") {
				if err := migrator.AddColumn(&TableClientConfig{}, "allowed_origins_json"); err != nil {
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

			if !migrator.HasColumn(&TableClientConfig{}, "allow_direct_keys") {
				if err := migrator.AddColumn(&TableClientConfig{}, "allow_direct_keys"); err != nil {
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
			if !migrator.HasColumn(&TableClientConfig{}, "enable_litellm_fallbacks") {
				if err := migrator.AddColumn(&TableClientConfig{}, "enable_litellm_fallbacks"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			if err := migrator.DropColumn(&TableClientConfig{}, "enable_litellm_fallbacks"); err != nil {
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
			if !migrator.HasColumn(&TableTeam{}, "profile") {
				if err := migrator.AddColumn(&TableTeam{}, "profile"); err != nil {
					return err
				}
			}
			if !migrator.HasColumn(&TableTeam{}, "config") {
				if err := migrator.AddColumn(&TableTeam{}, "config"); err != nil {
					return err
				}
			}
			if !migrator.HasColumn(&TableTeam{}, "claims") {
				if err := migrator.AddColumn(&TableTeam{}, "claims"); err != nil {
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