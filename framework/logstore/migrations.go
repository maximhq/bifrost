package logstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/maximhq/bifrost/framework/migrator"
	"gorm.io/gorm"
)

// isValidJSON checks if a string is valid JSON.
func isValidJSON(s string) bool {
	var js json.RawMessage
	return json.Unmarshal([]byte(s), &js) == nil
}

const (
	// migrationAdvisoryLockKey is used for PostgreSQL advisory locks
	// to serialize migrations across cluster nodes.
	// This is the SAME key used by configstore migrations to ensure
	// all migrations are fully serialized.
	migrationAdvisoryLockKey = 1000001

	// indexAdvisoryLockKey serializes the background index build across
	// cluster nodes. It is intentionally a DIFFERENT key from migrationAdvisoryLockKey
	// so that the long-running CREATE INDEX CONCURRENTLY held by one pod's goroutine
	// does not block other pods from running their (fast) migrations on startup.
	indexAdvisoryLockKey = 1000002

	// matviewRefreshAdvisoryLockKey serializes periodic materialized view
	// refreshes across cluster nodes so only one replica refreshes at a time.
	matviewRefreshAdvisoryLockKey = 1000005
)

// advisoryLock holds a dedicated connection and the advisory lock key.
// This ensures the lock is held on the same connection throughout its lifetime,
// preventing race conditions caused by GORM's connection pooling.
type advisoryLock struct {
	conn    *sql.Conn
	lockKey int64
}

// acquireAdvisoryLock gets a dedicated connection and acquires a PostgreSQL advisory lock
// for the given key. For non-PostgreSQL databases, returns a no-op lock.
func acquireAdvisoryLock(ctx context.Context, db *gorm.DB, lockKey int64, label string) (*advisoryLock, error) {
	if db.Dialector.Name() != "postgres" {
		return &advisoryLock{}, nil
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get sql.DB: %w", err)
	}

	// Get a dedicated connection (not returned to pool until Close())
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get dedicated connection for %s lock: %w", label, err)
	}

	// Acquire advisory lock on this dedicated connection.
	// This will BLOCK if another node holds the lock.
	if _, err = conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to acquire %s advisory lock: %w", label, err)
	}

	return &advisoryLock{conn: conn, lockKey: lockKey}, nil
}

// release unlocks and closes the dedicated connection.
func (l *advisoryLock) release(ctx context.Context) {
	if l.conn == nil {
		return
	}
	// Release lock on the SAME connection that acquired it.
	_, _ = l.conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", l.lockKey)
	l.conn.Close()
}

// acquireMigrationLock acquires the serialization lock for schema migrations.
func acquireMigrationLock(ctx context.Context, db *gorm.DB) (*advisoryLock, error) {
	return acquireAdvisoryLock(ctx, db, migrationAdvisoryLockKey, "migration")
}

// acquireIndexLock acquires the serialization lock for the background index build.
func acquireIndexLock(ctx context.Context, db *gorm.DB) (*advisoryLock, error) {
	return acquireAdvisoryLock(ctx, db, indexAdvisoryLockKey, "index")
}

// triggerMigrations runs all registered logstore schema migrations in order under a
// PostgreSQL advisory lock (shared with configstore) so only one node migrates at a time.
func triggerMigrations(ctx context.Context, db *gorm.DB) error {
	// Acquire advisory lock to serialize migrations across cluster nodes.
	// Uses the same key as configstore to ensure all migrations are serialized.
	lock, err := acquireMigrationLock(ctx, db)
	if err != nil {
		return err
	}
	defer lock.release(ctx)

	if err := migrationInit(ctx, db); err != nil {
		return err
	}
	if err := migrationUpdateObjectColumnValues(ctx, db); err != nil {
		return err
	}
	if err := migrationAddParentRequestIDColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddResponsesOutputColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddCostAndCacheDebugColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddResponsesInputHistoryColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddNumberOfRetriesAndFallbackIndexAndSelectedKeyAndVirtualKeyColumns(ctx, db); err != nil {
		return err
	}
	if err := migrationAddPerformanceIndexes(ctx, db); err != nil {
		return err
	}
	if err := migrationAddPerformanceIndexesV2(ctx, db); err != nil {
		return err
	}
	if err := migrationUpdateTimestampFormat(ctx, db); err != nil {
		return err
	}
	if err := migrationAddRawRequestColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationCreateMCPToolLogsTable(ctx, db); err != nil {
		return err
	}
	if err := migrationAddCostColumnToMCPToolLogs(ctx, db); err != nil {
		return err
	}
	if err := migrationAddImageGenerationOutputColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddImageGenerationInputColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddRoutingRuleIDAndRoutingRuleNameColumns(ctx, db); err != nil {
		return err
	}
	if err := migrationAddVirtualKeyColumnsToMCPToolLogs(ctx, db); err != nil {
		return err
	}
	if err := migrationAddRoutingEngineUsedColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddRoutingEnginesUsedColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddListModelsOutputColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddRerankOutputColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddRoutingEngineLogsColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationCreateAsyncJobsTable(ctx, db); err != nil {
		return err
	}
	if err := migrationAddMetadataColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddMetadataColumnToMCPToolLogs(ctx, db); err != nil {
		return err
	}
	if err := migrationAddHistogramCompositeIndexes(ctx, db); err != nil {
		return err
	}
	if err := migrationAddVideoColumns(ctx, db); err != nil {
		return err
	}
	if err := migrationAddProviderHistogramIndex(ctx, db); err != nil {
		return err
	}
	if err := migrationAddLargePayloadColumns(ctx, db); err != nil {
		return err
	}
	if err := migrationAddPassthroughRequestBodyColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddPassthroughResponseBodyColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddMetadataGINIndex(ctx, db); err != nil {
		return err
	}
	if err := migrationAddDashboardEnhancements(ctx, db); err != nil {
		return err
	}
	if err := migrationAddLogsAndDashboardPerformanceIndexes(ctx, db); err != nil {
		return err
	}
	if err := migrationAddImageEditInputColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddImageVariationInputColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddPluginLogsColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddAliasColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddGovernanceContextColumns(ctx, db); err != nil {
		return err
	}
	if err := migrationRecreateMatViewsWithGovernanceColumns(ctx, db); err != nil {
		return err
	}
	if err := migrationAddOCROutputColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddRequestIDColumnToMCPToolLogs(ctx, db); err != nil {
		return err
	}
	if err := migrationAddHasObjectColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddAttemptTrailColumn(ctx, db); err != nil {
		return err
	}
	if err := migrationAddSelectedPromptColumns(ctx, db); err != nil {
		return err
	}
	if err := migrationAddUserNameColumn(ctx, db); err != nil {
		return err
	}
	return nil
}

// migrationInit creates the logs table if it does not exist.
func migrationInit(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "logs_init",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasTable(&Log{}) {
				if err := migrator.CreateTable(&Log{}); err != nil {
					return err
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			// Drop children first, then parents (adjust if your actual FKs differ)
			if err := migrator.DropTable(&Log{}); err != nil {
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

// migrationUpdateObjectColumnValues normalizes legacy object_type string values on the logs table.
func migrationUpdateObjectColumnValues(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_init_update_object_column_values",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			updateSQL := `
				UPDATE logs
				SET object_type = CASE object_type
					WHEN 'chat.completion' THEN 'chat_completion'
					WHEN 'text.completion' THEN 'text_completion'
					WHEN 'list' THEN 'embedding'
					WHEN 'audio.speech' THEN 'speech'
					WHEN 'audio.transcription' THEN 'transcription'
					WHEN 'chat.completion.chunk' THEN 'chat_completion_stream'
					WHEN 'audio.speech.chunk' THEN 'speech_stream'
					WHEN 'audio.transcription.chunk' THEN 'transcription_stream'
					WHEN 'response' THEN 'responses'
					WHEN 'response.completion.chunk' THEN 'responses_stream'
					ELSE object_type
				END
				WHERE object_type IN (
					'chat.completion', 'text.completion', 'list',
					'audio.speech', 'audio.transcription', 'chat.completion.chunk',
					'audio.speech.chunk', 'audio.transcription.chunk',
					'response', 'response.completion.chunk'
				)`

			result := tx.Exec(updateSQL)
			if result.Error != nil {
				return fmt.Errorf("failed to update object_type values: %w", result.Error)
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			// Use a single CASE statement for efficient bulk rollback
			rollbackSQL := `
				UPDATE logs
				SET object_type = CASE object_type
					WHEN 'chat_completion' THEN 'chat.completion'
					WHEN 'text_completion' THEN 'text.completion'
					WHEN 'embedding' THEN 'list'
					WHEN 'speech' THEN 'audio.speech'
					WHEN 'transcription' THEN 'audio.transcription'
					WHEN 'chat_completion_stream' THEN 'chat.completion.chunk'
					WHEN 'speech_stream' THEN 'audio.speech.chunk'
					WHEN 'transcription_stream' THEN 'audio.transcription.chunk'
					WHEN 'responses' THEN 'response'
					WHEN 'responses_stream' THEN 'response.completion.chunk'
					ELSE object_type
				END
				WHERE object_type IN (
					'chat_completion', 'text_completion', 'embedding', 'speech',
					'transcription', 'chat_completion_stream', 'speech_stream',
					'transcription_stream', 'responses', 'responses_stream'
				)`

			result := tx.Exec(rollbackSQL)
			if result.Error != nil {
				return fmt.Errorf("failed to rollback object_type values: %w", result.Error)
			}

			return nil
		},
	}})

	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running object column migration: %s", err.Error())
	}
	return nil
}

// migrationAddParentRequestIDColumn adds the parent_request_id column to the logs table.
func migrationAddParentRequestIDColumn(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_init_add_parent_request_id_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&Log{}, "parent_request_id") {
				if err := migrator.AddColumn(&Log{}, "parent_request_id"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if err := migrator.DropColumn(&Log{}, "parent_request_id"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding parent_request_id column: %s", err.Error())
	}
	return nil
}

// migrationAddResponsesOutputColumn adds columns for Responses API output, chat/embedding
// payloads, and raw_response on the logs table.
func migrationAddResponsesOutputColumn(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_init_add_responses_output_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&Log{}, "responses_output") {
				if err := migrator.AddColumn(&Log{}, "responses_output"); err != nil {
					return err
				}
			}
			if !migrator.HasColumn(&Log{}, "input_history") {
				if err := migrator.AddColumn(&Log{}, "input_history"); err != nil {
					return err
				}
			}
			if !migrator.HasColumn(&Log{}, "output_message") {
				if err := migrator.AddColumn(&Log{}, "output_message"); err != nil {
					return err
				}
			}
			if !migrator.HasColumn(&Log{}, "embedding_output") {
				if err := migrator.AddColumn(&Log{}, "embedding_output"); err != nil {
					return err
				}
			}
			if !migrator.HasColumn(&Log{}, "raw_response") {
				if err := migrator.AddColumn(&Log{}, "raw_response"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if err := migrator.DropColumn(&Log{}, "responses_output"); err != nil {
				return err
			}
			if err := migrator.DropColumn(&Log{}, "input_history"); err != nil {
				return err
			}
			if err := migrator.DropColumn(&Log{}, "output_message"); err != nil {
				return err
			}
			if err := migrator.DropColumn(&Log{}, "embedding_output"); err != nil {
				return err
			}
			if err := migrator.DropColumn(&Log{}, "raw_response"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding responses_output column: %s", err.Error())
	}
	return nil
}

// migrationAddCostAndCacheDebugColumn adds cost and cache_debug columns to the logs table.
func migrationAddCostAndCacheDebugColumn(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_init_add_cost_and_cache_debug_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&Log{}, "cost") {
				if err := migrator.AddColumn(&Log{}, "cost"); err != nil {
					return err
				}
			}
			if !migrator.HasColumn(&Log{}, "cache_debug") {
				if err := migrator.AddColumn(&Log{}, "cache_debug"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if err := migrator.DropColumn(&Log{}, "cost"); err != nil {
				return err
			}
			if err := migrator.DropColumn(&Log{}, "cache_debug"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding cost column: %s", err.Error())
	}
	return nil
}

// migrationAddResponsesInputHistoryColumn adds the responses_input_history column to the logs table.
func migrationAddResponsesInputHistoryColumn(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_init_add_responses_input_history_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&Log{}, "responses_input_history") {
				if err := migrator.AddColumn(&Log{}, "responses_input_history"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if err := migrator.DropColumn(&Log{}, "responses_input_history"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding responses_input_history column: %s", err.Error())
	}
	return nil
}

// migrationAddNumberOfRetriesAndFallbackIndexAndSelectedKeyAndVirtualKeyColumns adds retry,
// fallback, selected API key, and virtual key columns to the logs table.
func migrationAddNumberOfRetriesAndFallbackIndexAndSelectedKeyAndVirtualKeyColumns(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_init_add_number_of_retries_and_fallback_index_and_selected_key_and_virtual_key_columns",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&Log{}, "number_of_retries") {
				if err := migrator.AddColumn(&Log{}, "number_of_retries"); err != nil {
					return err
				}
			}
			if !migrator.HasColumn(&Log{}, "fallback_index") {
				if err := migrator.AddColumn(&Log{}, "fallback_index"); err != nil {
					return err
				}
			}
			if !migrator.HasColumn(&Log{}, "selected_key_id") {
				if err := migrator.AddColumn(&Log{}, "selected_key_id"); err != nil {
					return err
				}
			}
			if !migrator.HasColumn(&Log{}, "selected_key_name") {
				if err := migrator.AddColumn(&Log{}, "selected_key_name"); err != nil {
					return err
				}
			}
			if !migrator.HasColumn(&Log{}, "virtual_key_id") {
				if err := migrator.AddColumn(&Log{}, "virtual_key_id"); err != nil {
					return err
				}
			}
			if !migrator.HasColumn(&Log{}, "virtual_key_name") {
				if err := migrator.AddColumn(&Log{}, "virtual_key_name"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if err := migrator.DropColumn(&Log{}, "number_of_retries"); err != nil {
				return err
			}
			if err := migrator.DropColumn(&Log{}, "fallback_index"); err != nil {
				return err
			}
			if err := migrator.DropColumn(&Log{}, "selected_key_id"); err != nil {
				return err
			}
			if err := migrator.DropColumn(&Log{}, "selected_key_name"); err != nil {
				return err
			}
			if err := migrator.DropColumn(&Log{}, "virtual_key_id"); err != nil {
				return err
			}
			if err := migrator.DropColumn(&Log{}, "virtual_key_name"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding number_of_retries and fallback_index columns: %s", err.Error())
	}
	return nil
}

// migrationAddPerformanceIndexes adds btree indexes on latency, total_tokens, and key columns.
func migrationAddPerformanceIndexes(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_performance_indexes",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			// Add index on latency for AVG aggregation queries
			if !migrator.HasIndex(&Log{}, "idx_logs_latency") {
				if err := migrator.CreateIndex(&Log{}, "idx_logs_latency"); err != nil {
					return fmt.Errorf("failed to create index on latency: %w", err)
				}
			}

			// Add index on total_tokens for SUM aggregation queries
			if !migrator.HasIndex(&Log{}, "idx_logs_total_tokens") {
				if err := migrator.CreateIndex(&Log{}, "idx_logs_total_tokens"); err != nil {
					return fmt.Errorf("failed to create index on total_tokens: %w", err)
				}
			}

			// Add index on selected_key_id for filtering
			if !migrator.HasIndex(&Log{}, "idx_logs_selected_key_id") {
				if err := migrator.CreateIndex(&Log{}, "idx_logs_selected_key_id"); err != nil {
					return fmt.Errorf("failed to create index on selected_key_id: %w", err)
				}
			}

			// Add index on virtual_key_id for filtering
			if !migrator.HasIndex(&Log{}, "idx_logs_virtual_key_id") {
				if err := migrator.CreateIndex(&Log{}, "idx_logs_virtual_key_id"); err != nil {
					return fmt.Errorf("failed to create index on virtual_key_id: %w", err)
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			if migrator.HasIndex(&Log{}, "idx_logs_latency") {
				if err := migrator.DropIndex(&Log{}, "idx_logs_latency"); err != nil {
					return err
				}
			}
			if migrator.HasIndex(&Log{}, "idx_logs_total_tokens") {
				if err := migrator.DropIndex(&Log{}, "idx_logs_total_tokens"); err != nil {
					return err
				}
			}
			if migrator.HasIndex(&Log{}, "idx_logs_selected_key_id") {
				if err := migrator.DropIndex(&Log{}, "idx_logs_selected_key_id"); err != nil {
					return err
				}
			}
			if migrator.HasIndex(&Log{}, "idx_logs_virtual_key_id") {
				if err := migrator.DropIndex(&Log{}, "idx_logs_virtual_key_id"); err != nil {
					return err
				}
			}

			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding performance indexes: %s", err.Error())
	}
	return nil
}

// migrationAddPerformanceIndexesV2 adds additional indexes for improved query performance
// This migration adds indices based on query patterns in rdb.go
func migrationAddPerformanceIndexesV2(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_performance_indexes_v2",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			// Single-column indices for filtering and sorting
			// These indices optimize queries in applyFilters, SearchLogs, GetStats, and Flush

			// Add index on timestamp for range queries and default ordering
			// Used in: WHERE timestamp >= ? AND timestamp <= ? and ORDER BY timestamp
			if !migrator.HasIndex(&Log{}, "idx_logs_timestamp") {
				if err := tx.Exec("CREATE INDEX IF NOT EXISTS idx_logs_timestamp ON logs(timestamp)").Error; err != nil {
					return fmt.Errorf("failed to create index on timestamp: %w", err)
				}
			}

			// Add index on status for filtering (success, error, processing)
			// Used in: WHERE status IN ('success', 'error'), WHERE status = 'processing'
			if !migrator.HasIndex(&Log{}, "idx_logs_status") {
				if err := tx.Exec("CREATE INDEX IF NOT EXISTS idx_logs_status ON logs(status)").Error; err != nil {
					return fmt.Errorf("failed to create index on status: %w", err)
				}
			}

			// Add index on created_at for Flush operations
			// Used in: WHERE created_at < ?
			if !migrator.HasIndex(&Log{}, "idx_logs_created_at") {
				if err := tx.Exec("CREATE INDEX IF NOT EXISTS idx_logs_created_at ON logs(created_at)").Error; err != nil {
					return fmt.Errorf("failed to create index on created_at: %w", err)
				}
			}

			// Add index on provider for filtering
			// Used in: WHERE provider IN (?)
			if !migrator.HasIndex(&Log{}, "idx_logs_provider") {
				if err := tx.Exec("CREATE INDEX IF NOT EXISTS idx_logs_provider ON logs(provider)").Error; err != nil {
					return fmt.Errorf("failed to create index on provider: %w", err)
				}
			}

			// Add index on model for filtering
			// Used in: WHERE model IN (?)
			if !migrator.HasIndex(&Log{}, "idx_logs_model") {
				if err := tx.Exec("CREATE INDEX IF NOT EXISTS idx_logs_model ON logs(model)").Error; err != nil {
					return fmt.Errorf("failed to create index on model: %w", err)
				}
			}

			// Add index on object_type for filtering
			// Used in: WHERE object_type IN (?)
			if !migrator.HasIndex(&Log{}, "idx_logs_object_type") {
				if err := tx.Exec("CREATE INDEX IF NOT EXISTS idx_logs_object_type ON logs(object_type)").Error; err != nil {
					return fmt.Errorf("failed to create index on object_type: %w", err)
				}
			}

			// Add index on cost for range queries and ordering
			// Used in: WHERE cost >= ? AND cost <= ?, ORDER BY cost
			if !migrator.HasIndex(&Log{}, "idx_logs_cost") {
				if err := tx.Exec("CREATE INDEX IF NOT EXISTS idx_logs_cost ON logs(cost)").Error; err != nil {
					return fmt.Errorf("failed to create index on cost: %w", err)
				}
			}

			// Composite indices for common query patterns

			// Add composite index on (status, timestamp) for GetStats queries
			// Used when filtering completed requests (status IN ('success', 'error')) with timestamp ranges
			// This composite index is more efficient than individual indices for these combined queries
			if !migrator.HasIndex(&Log{}, "idx_logs_status_timestamp") {
				if err := tx.Exec("CREATE INDEX IF NOT EXISTS idx_logs_status_timestamp ON logs(status, timestamp)").Error; err != nil {
					return fmt.Errorf("failed to create composite index on (status, timestamp): %w", err)
				}
			}

			// Add composite index on (status, created_at) for Flush operations
			// Used in Flush: WHERE status = 'processing' AND created_at < ?
			// This composite index significantly improves cleanup query performance
			if !migrator.HasIndex(&Log{}, "idx_logs_status_created_at") {
				if err := tx.Exec("CREATE INDEX IF NOT EXISTS idx_logs_status_created_at ON logs(status, created_at)").Error; err != nil {
					return fmt.Errorf("failed to create composite index on (status, created_at): %w", err)
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			// Drop all indices added in this migration
			indices := []string{
				"idx_logs_timestamp",
				"idx_logs_status",
				"idx_logs_created_at",
				"idx_logs_provider",
				"idx_logs_model",
				"idx_logs_object_type",
				"idx_logs_cost",
				"idx_logs_status_timestamp",
				"idx_logs_status_created_at",
			}

			for _, indexName := range indices {
				if migrator.HasIndex(&Log{}, indexName) {
					if err := tx.Exec(fmt.Sprintf("DROP INDEX IF EXISTS %s", indexName)).Error; err != nil {
						return fmt.Errorf("failed to drop index %s: %w", indexName, err)
					}
				}
			}

			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding performance indexes v2: %s", err.Error())
	}
	return nil
}

// migrationUpdateTimestampFormat converts timestamp and created_at values to UTC ISO-8601 form
// on SQLite only; other dialects are unchanged.
func migrationUpdateTimestampFormat(ctx context.Context, db *gorm.DB) error {
	// only run the migration for sqlite databases
	dialect := db.Dialector.Name()
	if dialect != "sqlite" {
		return nil
	}

	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_update_timestamp_format",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			updateSQL := `
				UPDATE logs
				SET "timestamp" = strftime('%Y-%m-%dT%H:%M:%S', "timestamp", 'utc') || '.' ||
                    CAST(CAST(strftime('%f', "timestamp") * 1000 AS INTEGER) % 1000 AS TEXT) || 'Z'
				WHERE
					"timestamp" NOT LIKE '%Z'
					AND "timestamp" NOT LIKE '%+00%';
				UPDATE logs
				SET created_at = strftime('%Y-%m-%dT%H:%M:%S', created_at, 'utc') || '.' ||
                    CAST(CAST(strftime('%f', created_at) * 1000 AS INTEGER) % 1000 AS TEXT) ||
                    'Z'
				WHERE
					created_at NOT LIKE '%Z'
					AND created_at NOT LIKE '%+00%';
				`

			result := tx.Exec(updateSQL)
			if result.Error != nil {
				return fmt.Errorf("failed to update timestamp values: %w", result.Error)
			}

			return nil
		},
	}})

	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while running update timestamp for logs migration: %s", err.Error())
	}
	return nil
}

// migrationAddRawRequestColumn adds the raw_request column to the logs table.
func migrationAddRawRequestColumn(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_raw_request_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&Log{}, "raw_request") {
				if err := migrator.AddColumn(&Log{}, "raw_request"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if migrator.HasColumn(&Log{}, "raw_request") {
				if err := migrator.DropColumn(&Log{}, "raw_request"); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding raw request column: %s", err.Error())
	}
	return nil
}

// migrationCreateMCPToolLogsTable creates the mcp_tool_logs table for MCP tool execution logs
func migrationCreateMCPToolLogsTable(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "mcp_tool_logs_init",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasTable(&MCPToolLog{}) {
				if err := migrator.CreateTable(&MCPToolLog{}); err != nil {
					return err
				}
			}

			// Explicitly create indexes as declared in struct tags
			if !migrator.HasIndex(&MCPToolLog{}, "idx_mcp_logs_llm_request_id") {
				if err := migrator.CreateIndex(&MCPToolLog{}, "idx_mcp_logs_llm_request_id"); err != nil {
					return fmt.Errorf("failed to create index on llm_request_id: %w", err)
				}
			}

			if !migrator.HasIndex(&MCPToolLog{}, "idx_mcp_logs_tool_name") {
				if err := migrator.CreateIndex(&MCPToolLog{}, "idx_mcp_logs_tool_name"); err != nil {
					return fmt.Errorf("failed to create index on tool_name: %w", err)
				}
			}

			if !migrator.HasIndex(&MCPToolLog{}, "idx_mcp_logs_server_label") {
				if err := migrator.CreateIndex(&MCPToolLog{}, "idx_mcp_logs_server_label"); err != nil {
					return fmt.Errorf("failed to create index on server_label: %w", err)
				}
			}

			if !migrator.HasIndex(&MCPToolLog{}, "idx_mcp_logs_latency") {
				if err := migrator.CreateIndex(&MCPToolLog{}, "idx_mcp_logs_latency"); err != nil {
					return fmt.Errorf("failed to create index on latency: %w", err)
				}
			}

			if !migrator.HasIndex(&MCPToolLog{}, "idx_mcp_logs_status") {
				if err := migrator.CreateIndex(&MCPToolLog{}, "idx_mcp_logs_status"); err != nil {
					return fmt.Errorf("failed to create index on status: %w", err)
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if err := migrator.DropTable(&MCPToolLog{}); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while creating mcp_tool_logs table: %s", err.Error())
	}
	return nil
}

// migrationAddCostColumnToMCPToolLogs adds the cost column to the mcp_tool_logs table
func migrationAddCostColumnToMCPToolLogs(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "mcp_tool_logs_add_cost_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			// Add cost column if it doesn't exist
			if !migrator.HasColumn(&MCPToolLog{}, "cost") {
				if err := migrator.AddColumn(&MCPToolLog{}, "cost"); err != nil {
					return fmt.Errorf("failed to add cost column: %w", err)
				}
			}

			// Create index on cost column
			if !migrator.HasIndex(&MCPToolLog{}, "idx_mcp_logs_cost") {
				if err := migrator.CreateIndex(&MCPToolLog{}, "idx_mcp_logs_cost"); err != nil {
					return fmt.Errorf("failed to create index on cost: %w", err)
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			// Drop index first
			if migrator.HasIndex(&MCPToolLog{}, "idx_mcp_logs_cost") {
				if err := migrator.DropIndex(&MCPToolLog{}, "idx_mcp_logs_cost"); err != nil {
					return err
				}
			}

			// Drop column
			if migrator.HasColumn(&MCPToolLog{}, "cost") {
				if err := migrator.DropColumn(&MCPToolLog{}, "cost"); err != nil {
					return err
				}
			}

			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding cost column to mcp_tool_logs: %s", err.Error())
	}
	return nil
}

// migrationAddImageGenerationOutputColumn adds the image_generation_output column to the logs table.
func migrationAddImageGenerationOutputColumn(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_image_generation_output_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&Log{}, "image_generation_output") {
				if err := migrator.AddColumn(&Log{}, "image_generation_output"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if migrator.HasColumn(&Log{}, "image_generation_output") {
				if err := migrator.DropColumn(&Log{}, "image_generation_output"); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding image generation output column: %s", err.Error())
	}
	return nil
}

// migrationAddImageGenerationInputColumn adds the image_generation_input column to the logs table.
func migrationAddImageGenerationInputColumn(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_image_generation_input_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&Log{}, "image_generation_input") {
				if err := migrator.AddColumn(&Log{}, "image_generation_input"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if migrator.HasColumn(&Log{}, "image_generation_input") {
				if err := migrator.DropColumn(&Log{}, "image_generation_input"); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding image generation input column: %s", err.Error())
	}
	return nil
}

// migrationAddRoutingRuleIDAndRoutingRuleNameColumns adds routing_rule_id and routing_rule_name to the logs table.
func migrationAddRoutingRuleIDAndRoutingRuleNameColumns(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_routing_rule_id_and_routing_rule_name_columns",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&Log{}, "routing_rule_id") {
				if err := migrator.AddColumn(&Log{}, "routing_rule_id"); err != nil {
					return err
				}
			}
			if !migrator.HasColumn(&Log{}, "routing_rule_name") {
				if err := migrator.AddColumn(&Log{}, "routing_rule_name"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if migrator.HasColumn(&Log{}, "routing_rule_id") {
				if err := migrator.DropColumn(&Log{}, "routing_rule_id"); err != nil {
					return err
				}
			}
			if migrator.HasColumn(&Log{}, "routing_rule_name") {
				if err := migrator.DropColumn(&Log{}, "routing_rule_name"); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding routing rule id and routing rule name columns: %s", err.Error())
	}
	return nil
}

// migrationAddVirtualKeyColumnsToMCPToolLogs adds virtual_key_id and virtual_key_name columns to the mcp_tool_logs table
func migrationAddVirtualKeyColumnsToMCPToolLogs(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "mcp_tool_logs_add_virtual_key_columns",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			// Add virtual_key_id column if it doesn't exist
			if !migrator.HasColumn(&MCPToolLog{}, "virtual_key_id") {
				if err := migrator.AddColumn(&MCPToolLog{}, "virtual_key_id"); err != nil {
					return fmt.Errorf("failed to add virtual_key_id column: %w", err)
				}
			}

			// Add virtual_key_name column if it doesn't exist
			if !migrator.HasColumn(&MCPToolLog{}, "virtual_key_name") {
				if err := migrator.AddColumn(&MCPToolLog{}, "virtual_key_name"); err != nil {
					return fmt.Errorf("failed to add virtual_key_name column: %w", err)
				}
			}

			// Create index on virtual_key_id column
			if !migrator.HasIndex(&MCPToolLog{}, "idx_mcp_logs_virtual_key_id") {
				if err := migrator.CreateIndex(&MCPToolLog{}, "idx_mcp_logs_virtual_key_id"); err != nil {
					return fmt.Errorf("failed to create index on virtual_key_id: %w", err)
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			// Drop index first
			if migrator.HasIndex(&MCPToolLog{}, "idx_mcp_logs_virtual_key_id") {
				if err := migrator.DropIndex(&MCPToolLog{}, "idx_mcp_logs_virtual_key_id"); err != nil {
					return err
				}
			}

			// Drop virtual_key_name column
			if migrator.HasColumn(&MCPToolLog{}, "virtual_key_name") {
				if err := migrator.DropColumn(&MCPToolLog{}, "virtual_key_name"); err != nil {
					return err
				}
			}

			// Drop virtual_key_id column
			if migrator.HasColumn(&MCPToolLog{}, "virtual_key_id") {
				if err := migrator.DropColumn(&MCPToolLog{}, "virtual_key_id"); err != nil {
					return err
				}
			}

			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding virtual key columns to mcp_tool_logs: %s", err.Error())
	}
	return nil
}

// migrationAddRoutingEngineUsedColumn adds routing_engine_used when the plural column does not exist yet.
func migrationAddRoutingEngineUsedColumn(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_routing_engine_used_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			// Only add the column if it doesn't exist
			if !migrator.HasColumn(&Log{}, "routing_engine_used") && !migrator.HasColumn(&Log{}, "routing_engines_used") {
				// Use raw SQL to avoid GORM struct field dependency
				if err := tx.Exec("ALTER TABLE logs ADD COLUMN routing_engine_used VARCHAR(255)").Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if migrator.HasColumn(&Log{}, "routing_engine_used") {
				if err := migrator.DropColumn(&Log{}, "routing_engine_used"); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding routing engine used column: %s", err.Error())
	}
	return nil
}

// migrationAddRoutingEnginesUsedColumn renames routing_engine_used to routing_engines_used or drops the legacy column.
func migrationAddRoutingEnginesUsedColumn(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_routing_engines_used_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			hasOldColumn := migrator.HasColumn(&Log{}, "routing_engine_used")
			hasNewColumn := migrator.HasColumn(&Log{}, "routing_engines_used")

			if hasOldColumn && !hasNewColumn {
				// Rename old column to new if new doesn't exist yet
				if err := migrator.RenameColumn(&Log{}, "routing_engine_used", "routing_engines_used"); err != nil {
					return fmt.Errorf("failed to rename routing_engine_used to routing_engines_used: %w", err)
				}
			} else if hasOldColumn && hasNewColumn {
				// Both columns exist - drop the old one (new column is already in use)
				if err := migrator.DropColumn(&Log{}, "routing_engine_used"); err != nil {
					return fmt.Errorf("failed to drop old routing_engine_used column: %w", err)
				}
			}
			// If only new column exists, do nothing (already migrated)

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			hasNewColumn := migrator.HasColumn(&Log{}, "routing_engines_used")
			hasOldColumn := migrator.HasColumn(&Log{}, "routing_engine_used")

			if hasNewColumn && !hasOldColumn {
				// Rename new column back to old if old doesn't exist
				if err := migrator.RenameColumn(&Log{}, "routing_engines_used", "routing_engine_used"); err != nil {
					return fmt.Errorf("failed to rename routing_engines_used back to routing_engine_used: %w", err)
				}
			}
			// If old column was dropped, recreate it would be complex, so we skip

			return nil
		},
	}})

	return m.Migrate()
}

// migrationAddListModelsOutputColumn adds the list_models_output column to the logs table.
func migrationAddListModelsOutputColumn(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_list_models_output_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&Log{}, "list_models_output") {
				if err := migrator.AddColumn(&Log{}, "list_models_output"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if migrator.HasColumn(&Log{}, "list_models_output") {
				if err := migrator.DropColumn(&Log{}, "list_models_output"); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding list models output column: %s", err.Error())
	}
	return nil
}

// migrationAddRerankOutputColumn adds the rerank_output column to the logs table.
func migrationAddRerankOutputColumn(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_rerank_output_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&Log{}, "rerank_output") {
				if err := migrator.AddColumn(&Log{}, "rerank_output"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if migrator.HasColumn(&Log{}, "rerank_output") {
				if err := migrator.DropColumn(&Log{}, "rerank_output"); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding rerank output column: %s", err.Error())
	}
	return nil
}

// migrationAddRoutingEngineLogsColumn adds the routing_engine_logs column to the logs table.
func migrationAddRoutingEngineLogsColumn(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_routing_engine_logs_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&Log{}, "routing_engine_logs") {
				if err := migrator.AddColumn(&Log{}, "routing_engine_logs"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if migrator.HasColumn(&Log{}, "routing_engine_logs") {
				if err := migrator.DropColumn(&Log{}, "routing_engine_logs"); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding routing engine logs column: %s", err.Error())
	}
	return nil
}

// migrationAddLargePayloadColumns adds is_large_payload_request and is_large_payload_response to the logs table.
func migrationAddLargePayloadColumns(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_large_payload_columns",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&Log{}, "is_large_payload_request") {
				if err := migrator.AddColumn(&Log{}, "is_large_payload_request"); err != nil {
					return err
				}
			}
			if !migrator.HasColumn(&Log{}, "is_large_payload_response") {
				if err := migrator.AddColumn(&Log{}, "is_large_payload_response"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if migrator.HasColumn(&Log{}, "is_large_payload_request") {
				if err := migrator.DropColumn(&Log{}, "is_large_payload_request"); err != nil {
					return err
				}
			}
			if migrator.HasColumn(&Log{}, "is_large_payload_response") {
				if err := migrator.DropColumn(&Log{}, "is_large_payload_response"); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding large payload columns: %s", err.Error())
	}
	return nil
}

// migrationCreateAsyncJobsTable creates the async_jobs table and its indexes if missing.
func migrationCreateAsyncJobsTable(ctx context.Context, db *gorm.DB) error {
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: "async_jobs_init",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			dbMigrator := tx.Migrator()
			if !dbMigrator.HasTable(&AsyncJob{}) {
				if err := dbMigrator.CreateTable(&AsyncJob{}); err != nil {
					return err
				}
			}

			// Explicitly create indexes as declared in struct tags
			if !dbMigrator.HasIndex(&AsyncJob{}, "idx_async_jobs_status") {
				if err := dbMigrator.CreateIndex(&AsyncJob{}, "idx_async_jobs_status"); err != nil {
					return fmt.Errorf("failed to create index on status: %w", err)
				}
			}

			if !dbMigrator.HasIndex(&AsyncJob{}, "idx_async_jobs_vk_id") {
				if err := dbMigrator.CreateIndex(&AsyncJob{}, "idx_async_jobs_vk_id"); err != nil {
					return fmt.Errorf("failed to create index on virtual_key_id: %w", err)
				}
			}

			if !dbMigrator.HasIndex(&AsyncJob{}, "idx_async_jobs_expires_at") {
				if err := dbMigrator.CreateIndex(&AsyncJob{}, "idx_async_jobs_expires_at"); err != nil {
					return fmt.Errorf("failed to create index on expires_at: %w", err)
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return tx.Migrator().DropTable(&AsyncJob{})
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while creating async_jobs table: %s", err.Error())
	}
	return nil
}

// migrationAddMetadataColumn adds the metadata JSON column to the logs table.
func migrationAddMetadataColumn(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_metadata_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&Log{}, "metadata") {
				if err := migrator.AddColumn(&Log{}, "metadata"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if migrator.HasColumn(&Log{}, "metadata") {
				if err := migrator.DropColumn(&Log{}, "metadata"); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding metadata column: %s", err.Error())
	}
	return nil
}

// migrationAddMetadataColumnToMCPToolLogs adds the metadata column to the mcp_tool_logs table
func migrationAddMetadataColumnToMCPToolLogs(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "mcp_tool_logs_add_metadata_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&MCPToolLog{}, "metadata") {
				if err := migrator.AddColumn(&MCPToolLog{}, "metadata"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if migrator.HasColumn(&MCPToolLog{}, "metadata") {
				if err := migrator.DropColumn(&MCPToolLog{}, "metadata"); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding metadata column to mcp_tool_logs: %s", err.Error())
	}
	return nil
}

// migrationAddRequestIDColumnToMCPToolLogs adds the request_id column to the mcp_tool_logs table.
// This stores the original context request ID separately from the primary key (which is now a UUID),
// enabling correct logging of parallel tool calls that share the same request ID.
func migrationAddRequestIDColumnToMCPToolLogs(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "mcp_tool_logs_add_request_id_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&MCPToolLog{}, "request_id") {
				if err := migrator.AddColumn(&MCPToolLog{}, "request_id"); err != nil {
					return err
				}
			}
			if err := tx.Exec(
				"UPDATE mcp_tool_logs SET request_id = id WHERE request_id IS NULL OR request_id = ''",
			).Error; err != nil {
				return fmt.Errorf("failed to backfill request_id: %w", err)
			}
			if !migrator.HasIndex(&MCPToolLog{}, "idx_mcp_logs_request_id") {
				if err := migrator.CreateIndex(&MCPToolLog{}, "idx_mcp_logs_request_id"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if migrator.HasIndex(&MCPToolLog{}, "idx_mcp_logs_request_id") {
				if err := migrator.DropIndex(&MCPToolLog{}, "idx_mcp_logs_request_id"); err != nil {
					return err
				}
			}
			if migrator.HasColumn(&MCPToolLog{}, "request_id") {
				if err := migrator.DropColumn(&MCPToolLog{}, "request_id"); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding request_id column to mcp_tool_logs: %s", err.Error())
	}
	return nil
}

// migrationAddHistogramCompositeIndexes adds a covering index that optimizes all 4 histogram queries.
// Without this, even though idx_logs_status_timestamp filters the WHERE clause correctly,
// SQLite must seek back to the main table to read aggregation columns (tokens, cost, model).
// With large rows (~800 KB of JSON per log entry), these main-table lookups dominate query time.
// A covering index includes all columns the histogram queries need, so SQLite resolves
// them entirely from the compact index B-tree (~100 bytes/entry) without touching the main table.
func migrationAddHistogramCompositeIndexes(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_histogram_composite_indexes",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			// Covering index for all 4 histogram queries with any combination of dashboard filters.
			//
			// Leading columns (status, timestamp) drive the range scan.
			// Filter columns (selected_key_id, virtual_key_id, etc.) let the DB evaluate
			// WHERE predicates directly from the index without main-table lookups.
			// Aggregation columns (model, cost, tokens) provide data for GROUP BY / SUM.
			//
			// Without these filter columns in the index, the DB must seek back to the
			// main table (~800 KB per row with JSON blobs) to check each filter,
			// turning a 17 ms query into a 35+ second one.
			if !migrator.HasIndex(&Log{}, "idx_logs_histogram_cover") {
				dialect := tx.Dialector.Name()

				var createSQL string
				switch dialect {
				case "mysql":
					// MySQL/MariaDB: InnoDB has a 3072-byte composite key limit.
					// With utf8mb4 each varchar(255) uses up to 1020 bytes, so use
					// prefix lengths (50 chars) to keep the total well under the limit.
					createSQL = `CREATE INDEX idx_logs_histogram_cover ON logs(
						status(50), timestamp,
						selected_key_id(50), virtual_key_id(50), routing_rule_id(50), provider(50), object_type(50),
						model(50), cost, prompt_tokens, completion_tokens, total_tokens
					)`
				default:
					// SQLite / PostgreSQL: no prefix-index limit concerns.
					createSQL = `CREATE INDEX IF NOT EXISTS idx_logs_histogram_cover ON logs(
						status, timestamp,
						selected_key_id, virtual_key_id, routing_rule_id, provider, object_type,
						model, cost, prompt_tokens, completion_tokens, total_tokens
					)`
				}

				if err := tx.Exec(createSQL).Error; err != nil {
					return fmt.Errorf("failed to create covering index for histograms: %w", err)
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			if migrator.HasIndex(&Log{}, "idx_logs_histogram_cover") {
				if err := tx.Exec("DROP INDEX IF EXISTS idx_logs_histogram_cover").Error; err != nil {
					return fmt.Errorf("failed to drop index idx_logs_histogram_cover: %w", err)
				}
			}

			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding histogram covering index: %s", err.Error())
	}
	return nil
}

// migrationAddVideoColumns adds video generation, retrieval, download, list, and delete payload columns to the logs table.
func migrationAddVideoColumns(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_video_columns",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			videoColumns := []string{
				"video_generation_input",
				"video_generation_output",
				"video_retrieve_output",
				"video_download_output",
				"video_list_output",
				"video_delete_output",
			}

			for _, column := range videoColumns {
				if !migrator.HasColumn(&Log{}, column) {
					if err := migrator.AddColumn(&Log{}, column); err != nil {
						return err
					}
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			videoColumns := []string{
				"video_generation_input",
				"video_generation_output",
				"video_retrieve_output",
				"video_download_output",
				"video_list_output",
				"video_delete_output",
			}

			for _, column := range videoColumns {
				if migrator.HasColumn(&Log{}, column) {
					if err := migrator.DropColumn(&Log{}, column); err != nil {
						return err
					}
				}
			}

			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding video columns: %s", err.Error())
	}
	return nil
}

// migrationAddProviderHistogramIndex records the migration version for the provider histogram
// index. Actual index creation is deferred to ensurePerformanceIndexes (called post-startup
// in a background goroutine) because CREATE INDEX CONCURRENTLY cannot run inside a
// transaction and a regular CREATE INDEX takes an AccessExclusiveLock that blocks all
// reads/writes on large tables.
func migrationAddProviderHistogramIndex(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = false
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_provider_histogram_index",
		Migrate: func(tx *gorm.DB) error {
			// No-op: actual index creation is handled by ensurePerformanceIndexes
			// to avoid blocking pod startup on large tables.
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := tx.Exec("DROP INDEX IF EXISTS idx_logs_ts_provider_status").Error; err != nil {
				return fmt.Errorf("failed to drop index idx_logs_ts_provider_status: %w", err)
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding provider histogram index: %s", err.Error())
	}
	return nil
}

// migrationAddPassthroughRequestBodyColumn adds passthrough_request_body to the logs table.
func migrationAddPassthroughRequestBodyColumn(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_passthrough_request_body_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&Log{}, "passthrough_request_body") {
				if err := migrator.AddColumn(&Log{}, "passthrough_request_body"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if migrator.HasColumn(&Log{}, "passthrough_request_body") {
				if err := migrator.DropColumn(&Log{}, "passthrough_request_body"); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding passthrough request body column: %s", err.Error())
	}
	return nil
}

// migrationAddPassthroughResponseBodyColumn adds passthrough_response_body to the logs table.
func migrationAddPassthroughResponseBodyColumn(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_passthrough_response_body_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&Log{}, "passthrough_response_body") {
				if err := migrator.AddColumn(&Log{}, "passthrough_response_body"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if migrator.HasColumn(&Log{}, "passthrough_response_body") {
				if err := migrator.DropColumn(&Log{}, "passthrough_response_body"); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding passthrough response body column: %s", err.Error())
	}
	return nil
}

// migrationAddMetadataGINIndex adds a GIN index on the metadata column for Postgres
// to speed up jsonb ->> queries used for metadata filtering.
// For SQLite, this is a no-op since json_extract works without special indices.
func migrationAddMetadataGINIndex(ctx context.Context, db *gorm.DB) error {
	// UseTransaction must be false because CREATE INDEX CONCURRENTLY cannot
	// run inside a transaction. This avoids deadlocks during rolling upgrades
	// where old pods are still writing to the logs table.
	opts := *migrator.DefaultOptions
	opts.UseTransaction = false
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_metadata_gin_index_v3",
		Migrate: func(tx *gorm.DB) error {
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if tx.Dialector.Name() == "postgres" {
				if err := tx.Exec("DROP INDEX IF EXISTS idx_logs_metadata_gin").Error; err != nil {
					return fmt.Errorf("failed to drop metadata GIN index: %w", err)
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding metadata GIN index: %s", err.Error())
	}
	return nil
}

// ensureMetadataGINIndex checks whether idx_logs_metadata_gin exists and is valid.
// If the index is missing or was left in an INVALID state by a previously interrupted
// CREATE INDEX CONCURRENTLY, it drops the remnant and rebuilds the index synchronously.
//
// This is intentionally separate from the migrationAddMetadataGINIndex migration so that
// the long-running CREATE INDEX CONCURRENTLY does not block pod startup. Callers that
// want non-blocking behaviour should invoke this in a goroutine (see postgres.go).
func ensureMetadataGINIndex(ctx context.Context, conn *sql.Conn) error {
	// pg_index.indisvalid is false when a CONCURRENTLY build was interrupted.
	// COALESCE returns false when no row matches (index does not exist yet).
	var indexValid bool

	if err := conn.QueryRowContext(ctx, `
		SELECT COALESCE(bool_and(pi.indisvalid), false)
		FROM pg_class pc
		JOIN pg_index pi ON pi.indrelid = pc.oid
		JOIN pg_class ic ON ic.oid = pi.indexrelid
		WHERE pc.relname = 'logs'
		  AND ic.relname = 'idx_logs_metadata_gin'
	`).Scan(&indexValid); err != nil {
		return fmt.Errorf("failed to query GIN index validity: %w", err)
	}

	if indexValid {
		// Defensively clean up any invalid metadata values written after index creation.
		// Use EXISTS + LIMIT 1 to avoid a full sequential scan when (the common case) no invalid rows exist.
		var hasInvalid bool
		if err := conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM logs WHERE metadata IS NOT NULL AND metadata IS NOT JSON OBJECT LIMIT 1)").Scan(&hasInvalid); err != nil {
			return fmt.Errorf("failed to query invalid metadata values: %w", err)
		}
		if hasInvalid {
			if _, err := conn.ExecContext(ctx, "UPDATE logs SET metadata = NULL WHERE metadata IS NOT NULL AND metadata IS NOT JSON OBJECT"); err != nil {
				return fmt.Errorf("failed to clean invalid metadata values: %w", err)
			}
		}
		return nil
	}

	// Drop any INVALID remnant left by a prior interrupted CONCURRENTLY build.
	if _, err := conn.ExecContext(ctx, "DROP INDEX CONCURRENTLY IF EXISTS idx_logs_metadata_gin"); err != nil {
		return fmt.Errorf("failed to drop invalid metadata GIN index: %w", err)
	}

	// Boost memory available for the sort phase so PostgreSQL needs fewer merge
	// passes. Non-fatal: a lower maintenance_work_mem just means a slower build.
	_, _ = conn.ExecContext(ctx, "SET maintenance_work_mem = '512MB'")

	// Allow parallel workers for the index build (supported since PG 11).
	// Non-fatal: falls back to a single worker on older versions.
	_, _ = conn.ExecContext(ctx, "SET max_parallel_maintenance_workers = 4")

	// Defensively clean up any invalid metadata values before building the index.
	// Use EXISTS + LIMIT 1 to short-circuit when no invalid rows exist.
	var hasInvalid bool
	if err := conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM logs WHERE metadata IS NOT NULL AND metadata IS NOT JSON OBJECT LIMIT 1)").Scan(&hasInvalid); err != nil {
		return fmt.Errorf("failed to query invalid metadata values: %w", err)
	}
	if hasInvalid {
		if _, err := conn.ExecContext(ctx, "UPDATE logs SET metadata = NULL WHERE metadata IS NOT NULL AND metadata IS NOT JSON OBJECT"); err != nil {
			return fmt.Errorf("failed to clean invalid metadata values: %w", err)
		}
	}

	// CONCURRENTLY takes only a ShareUpdateExclusiveLock, which is compatible with
	// RowExclusiveLock (INSERT/UPDATE/DELETE), so concurrent writes from other pods
	// are not blocked during the build.
	//
	// jsonb_path_ops stores one hash per JSON path rather than indexing every key
	// and value separately, making the index ~3x smaller and faster to build.
	// It supports the @> containment operator used by all metadata filter queries.
	//
	// The partial predicate (WHERE metadata IS NOT NULL AND metadata IS JSON OBJECT) skips NULL and non-object rows,
	// further reducing build time and index size. Queries that filter on metadata
	// always include an IS NOT NULL guard (rdb.go) so the planner will use this index.
	if _, err := conn.ExecContext(ctx, "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_metadata_gin ON logs USING gin ((metadata::jsonb) jsonb_path_ops) WHERE metadata IS NOT NULL AND metadata IS JSON OBJECT"); err != nil {
		return fmt.Errorf("failed to create metadata GIN index: %w", err)
	}
	return nil
}

// migrationAddDashboardEnhancements adds cached_read_tokens column to logs table.
// The expensive backfill, covering index rebuild, and MCP index creation are deferred
// to ensureDashboardEnhancements (called post-startup in a background goroutine) so
// they do not block pod startup on large tables.
func migrationAddDashboardEnhancements(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = false
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_dashboard_enhancements",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			dbMigrator := tx.Migrator()

			if !dbMigrator.HasColumn(&Log{}, "cached_read_tokens") {
				if err := dbMigrator.AddColumn(&Log{}, "CachedReadTokens"); err != nil {
					return fmt.Errorf("failed to add cached_read_tokens column: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			dbMigrator := tx.Migrator()

			if dbMigrator.HasColumn(&Log{}, "cached_read_tokens") {
				_ = dbMigrator.DropColumn(&Log{}, "cached_read_tokens")
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error running dashboard enhancements migration: %s", err.Error())
	}
	return nil
}

// ensureDashboardEnhancements performs the expensive dashboard migration work that was
// deferred from migrationAddDashboardEnhancements: backfilling cached_read_tokens from
// the token_usage JSON, rebuilding the histogram covering index to include the new column,
// and creating the MCP histogram covering index.
//
// This is intentionally separate so that the long-running UPDATE and index rebuild do not
// block pod startup. Callers that want non-blocking behaviour should invoke this in a
// goroutine (see postgres.go). All operations are idempotent and safe to re-run.
func ensureDashboardEnhancements(ctx context.Context, conn *sql.Conn) error {
	// Backfill cached_read_tokens from token_usage JSON.
	// The extra `AND cached_read_tokens = 0` plus `AND COALESCE(...) > 0` makes
	// re-runs cheap: rows already backfilled have non-zero values (skipped),
	// and rows with genuinely zero cached tokens are also skipped (correct as-is).
	backfillSQL := `UPDATE logs SET
		cached_read_tokens = (token_usage::jsonb->'prompt_tokens_details'->>'cached_read_tokens')::int
		WHERE cached_read_tokens = 0
		AND token_usage IS NOT NULL AND token_usage != '' AND token_usage != 'null'
		AND token_usage ~ '^\s*\{.*\}\s*$'
		AND COALESCE((token_usage::jsonb->'prompt_tokens_details'->>'cached_read_tokens')::int, 0) > 0`
	if _, err := conn.ExecContext(ctx, backfillSQL); err != nil {
		return fmt.Errorf("failed to backfill cached_read_tokens: %w", err)
	}

	// Rebuild histogram covering index with cached_read_tokens included,
	// but only if missing or invalid (skip if already healthy).
	var logsIndexValid bool
	if err := conn.QueryRowContext(ctx, `
		SELECT COALESCE(bool_and(pi.indisvalid), false)
		FROM pg_class pc
		JOIN pg_index pi ON pi.indrelid = pc.oid
		JOIN pg_class ic ON ic.oid = pi.indexrelid
		WHERE pc.relname = 'logs'
		  AND ic.relname = 'idx_logs_histogram_cover'
	`).Scan(&logsIndexValid); err != nil {
		return fmt.Errorf("failed to check logs histogram index validity: %w", err)
	}
	if !logsIndexValid {
		if _, err := conn.ExecContext(ctx, "DROP INDEX CONCURRENTLY IF EXISTS idx_logs_histogram_cover"); err != nil {
			return fmt.Errorf("failed to drop old covering index: %w", err)
		}
		createLogsIndexSQL := `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_histogram_cover ON logs(
			status, timestamp,
			selected_key_id, virtual_key_id, routing_rule_id, provider, object_type,
			model, cost, prompt_tokens, completion_tokens, total_tokens, cached_read_tokens
		)`
		if _, err := conn.ExecContext(ctx, createLogsIndexSQL); err != nil {
			return fmt.Errorf("failed to create updated covering index: %w", err)
		}
	}

	// Create MCP histogram covering index if missing or invalid.
	var mcpIndexValid bool
	if err := conn.QueryRowContext(ctx, `
		SELECT COALESCE(bool_and(pi.indisvalid), false)
		FROM pg_class pc
		JOIN pg_index pi ON pi.indrelid = pc.oid
		JOIN pg_class ic ON ic.oid = pi.indexrelid
		WHERE pc.relname = 'mcp_tool_logs'
		  AND ic.relname = 'idx_mcp_logs_histogram_cover'
	`).Scan(&mcpIndexValid); err != nil {
		return fmt.Errorf("failed to check MCP histogram index validity: %w", err)
	}
	if !mcpIndexValid {
		if _, err := conn.ExecContext(ctx, "DROP INDEX CONCURRENTLY IF EXISTS idx_mcp_logs_histogram_cover"); err != nil {
			return fmt.Errorf("failed to drop invalid MCP histogram index: %w", err)
		}
		createMCPIndexSQL := `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_mcp_logs_histogram_cover ON mcp_tool_logs(
			status, timestamp, tool_name, server_label, virtual_key_id, cost
		)`
		if _, err := conn.ExecContext(ctx, createMCPIndexSQL); err != nil {
			return fmt.Errorf("failed to create MCP histogram covering index: %w", err)
		}
	}

	return nil
}

// migrationAddLogsAndDashboardPerformanceIndexes records the migration version for the performance
// indexes. Actual index creation is deferred to ensurePerformanceIndexes (called
// post-startup in a background goroutine) because CREATE INDEX CONCURRENTLY cannot
// run inside a transaction.
func migrationAddLogsAndDashboardPerformanceIndexes(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = false
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_and_dashboard_performance_indexes",
		Migrate: func(tx *gorm.DB) error {
			// No-op: actual index creation is handled by ensurePerformanceIndexes
			// to avoid blocking pod startup.
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			if tx.Dialector.Name() != "postgres" {
				return nil
			}
			tx = tx.WithContext(ctx)
			for _, indexName := range []string{
				"idx_logs_content_summary_fts",
				"idx_mcp_logs_arguments_fts",
				"idx_mcp_logs_result_fts",
				"idx_logs_routing_engines_arr",
				"idx_mcp_logs_timestamp",
			} {
				if err := tx.Exec("DROP INDEX CONCURRENTLY IF EXISTS " + indexName).Error; err != nil {
					return fmt.Errorf("failed to drop performance index %s: %w", indexName, err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error recording performance gin indexes migration: %w", err)
	}
	return nil
}

// performanceIndexDef is the table name, index name, and CREATE INDEX SQL for one Postgres index.
type performanceIndexDef struct {
	table string
	name  string
	sql   string
}

// performanceIndexes is the set of full-text and GIN indexes built by ensurePerformanceIndexes.
// Each statement uses CREATE INDEX CONCURRENTLY to avoid blocking writes.
var performanceIndexes = []performanceIndexDef{
	{
		table: "logs",
		name:  "idx_logs_content_summary_fts",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_content_summary_fts ON logs USING GIN (to_tsvector('simple', content_summary)) WHERE content_summary IS NOT NULL",
	},
	{
		table: "mcp_tool_logs",
		name:  "idx_mcp_logs_arguments_fts",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_mcp_logs_arguments_fts ON mcp_tool_logs USING GIN (to_tsvector('simple', arguments)) WHERE arguments IS NOT NULL",
	},
	{
		table: "mcp_tool_logs",
		name:  "idx_mcp_logs_result_fts",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_mcp_logs_result_fts ON mcp_tool_logs USING GIN (to_tsvector('simple', result)) WHERE result IS NOT NULL",
	},
	{
		table: "logs",
		name:  "idx_logs_routing_engines_arr",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_routing_engines_arr ON logs USING GIN (string_to_array(routing_engines_used, ',')) WHERE routing_engines_used IS NOT NULL",
	},
	{
		table: "mcp_tool_logs",
		name:  "idx_mcp_logs_timestamp",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_mcp_logs_timestamp ON mcp_tool_logs (timestamp)",
	},
	{
		table: "logs",
		name:  "idx_logs_ts_provider_status",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_ts_provider_status ON logs(timestamp, provider, status)",
	},
	{
		table: "logs",
		name:  "idx_logs_alias",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_alias ON logs(alias)",
	},
	{
		table: "logs",
		name:  "idx_logs_team_id",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_team_id ON logs(team_id)",
	},
	{
		table: "logs",
		name:  "idx_logs_customer_id",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_customer_id ON logs(customer_id)",
	},
	{
		table: "logs",
		name:  "idx_logs_user_id",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_user_id ON logs(user_id)",
	},
	{
		table: "logs",
		name:  "idx_logs_business_unit_id",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_business_unit_id ON logs(business_unit_id)",
	},
	{
		table: "logs",
		name:  "idx_logs_parent_request_id",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_parent_request_id ON logs(parent_request_id) WHERE parent_request_id IS NOT NULL",
	},
}

// ensurePerformanceIndexes checks whether each performance GIN index exists and is
// valid. If an index is missing or was left in an INVALID state by a previously
// interrupted CREATE INDEX CONCURRENTLY, it drops the remnant and rebuilds.
//
// This is intentionally separate from migrationAddPerformanceGINIndexes so that the
// long-running CREATE INDEX CONCURRENTLY does not block pod startup. Callers that
// want non-blocking behaviour should invoke this in a goroutine (see postgres.go).
func ensurePerformanceIndexes(ctx context.Context, conn *sql.Conn) error {
	// Boost memory for sort phase during index builds.
	_, _ = conn.ExecContext(ctx, "SET maintenance_work_mem = '512MB'")
	_, _ = conn.ExecContext(ctx, "SET max_parallel_maintenance_workers = 4")

	for _, idx := range performanceIndexes {
		// Check if the index exists and is valid.
		var indexValid bool
		if err := conn.QueryRowContext(ctx, `
			SELECT COALESCE(bool_and(pi.indisvalid), false)
			FROM pg_class pc
			JOIN pg_index pi ON pi.indrelid = pc.oid
			JOIN pg_class ic ON ic.oid = pi.indexrelid
			WHERE pc.relname = $1
			  AND ic.relname = $2
		`, idx.table, idx.name).Scan(&indexValid); err != nil {
			return fmt.Errorf("failed to check index %s validity: %w", idx.name, err)
		}
		if indexValid {
			continue
		}

		// Drop any INVALID remnant left by a prior interrupted CONCURRENTLY build.
		if _, err := conn.ExecContext(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+idx.name); err != nil {
			return fmt.Errorf("failed to drop invalid index %s: %w", idx.name, err)
		}

		// Create the index concurrently (does not block writes).
		if _, err := conn.ExecContext(ctx, idx.sql); err != nil {
			return fmt.Errorf("failed to create index %s: %w", idx.name, err)
		}
	}

	return nil
}

// migrationAddImageEditInputColumn adds the image_edit_input column to the logs table.
func migrationAddImageEditInputColumn(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_image_edit_input_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&Log{}, "image_edit_input") {
				if err := migrator.AddColumn(&Log{}, "image_edit_input"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if migrator.HasColumn(&Log{}, "image_edit_input") {
				if err := migrator.DropColumn(&Log{}, "image_edit_input"); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while adding image edit input column: %s", err.Error())

	}
	return nil
}

// migrationAddPluginLogsColumn adds the plugin_logs column to the logs table.
func migrationAddPluginLogsColumn(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_plugin_logs_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&Log{}, "plugin_logs") {
				if err := migrator.AddColumn(&Log{}, "plugin_logs"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if migrator.HasColumn(&Log{}, "plugin_logs") {
				if err := migrator.DropColumn(&Log{}, "plugin_logs"); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding plugin logs column: %s", err.Error())
	}
	return nil
}

// migrationAddAliasColumn adds the alias column to the logs table.
// The alias field stores the original model name the caller used when routing resolved it to a different model via alias mapping.
// Index creation is deferred to ensurePerformanceIndexes (called post-startup in a background goroutine)
// because CREATE INDEX CONCURRENTLY cannot run inside a transaction and a regular CREATE INDEX
// takes a SHARE lock that blocks writes on large tables during rolling deploys.
func migrationAddAliasColumn(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_alias_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mig := tx.Migrator()
			if !mig.HasColumn(&Log{}, "alias") {
				if err := mig.AddColumn(&Log{}, "alias"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mig := tx.Migrator()
			if mig.HasColumn(&Log{}, "alias") {
				if err := mig.DropColumn(&Log{}, "alias"); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding alias column: %s", err.Error())

	}
	return nil
}

// migrationAddHasObjectColumn adds the has_object boolean column to the logs table.
// Used by the hybrid log store to track whether a log's payload is stored in object storage.
func migrationAddHasObjectColumn(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_has_object_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mgr := tx.Migrator()
			if !mgr.HasColumn(&Log{}, "has_object") {
				if err := mgr.AddColumn(&Log{}, "has_object"); err != nil {

					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mgr := tx.Migrator()
			if mgr.HasColumn(&Log{}, "has_object") {
				if err := mgr.DropColumn(&Log{}, "has_object"); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding has_object column: %s", err.Error())
	}
	return nil
}

// migrationAddImageVariationInputColumn adds the image_variation_input column to the logs table.
func migrationAddImageVariationInputColumn(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_image_variation_input_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&Log{}, "image_variation_input") {
				if err := migrator.AddColumn(&Log{}, "image_variation_input"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if migrator.HasColumn(&Log{}, "image_variation_input") {
				if err := migrator.DropColumn(&Log{}, "image_variation_input"); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding image variation input column: %s", err.Error())
	}
	return nil
}

// migrationAddUserNameColumn adds the user_name column to the logs table.
// Adding a nullable column is instant in Postgres (metadata-only change, no table rewrite).
func migrationAddUserNameColumn(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_user_name_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mig := tx.Migrator()
			if !mig.HasColumn(&Log{}, "user_name") {
				if err := mig.AddColumn(&Log{}, "user_name"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mig := tx.Migrator()
			if mig.HasColumn(&Log{}, "user_name") {
				if err := mig.DropColumn(&Log{}, "user_name"); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while adding user_name column: %s", err.Error())
	}
	return nil
}

// migrationAddGovernanceContextColumns adds user_id, team_id, team_name, customer_id, customer_name,
// business_unit_id, business_unit_name columns to the logs table.
func migrationAddGovernanceContextColumns(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true

	columns := []string{"user_id", "team_id", "team_name", "customer_id", "customer_name", "business_unit_id", "business_unit_name"}

	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_governance_context_columns",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mig := tx.Migrator()
			for _, col := range columns {
				if !mig.HasColumn(&Log{}, col) {
					if err := mig.AddColumn(&Log{}, col); err != nil {
						return err
					}
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mig := tx.Migrator()
			for _, col := range columns {
				if mig.HasColumn(&Log{}, col) {
					if err := mig.DropColumn(&Log{}, col); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding governance context columns: %s", err.Error())
	}
	return nil
}

// migrationRecreateMatViewsWithGovernanceColumns drops and recreates materialized views
// so they include the new governance context columns (user_id, team_id, customer_id, business_unit_id).
// The views are recreated by ensureMatViews on startup, so we just need to drop the old ones.
func migrationRecreateMatViewsWithGovernanceColumns(ctx context.Context, db *gorm.DB) error {
	// Materialized views are PostgreSQL-only; skip on other dialects
	if db.Dialector.Name() != "postgres" {
		return nil
	}
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_recreate_matviews_with_governance_columns",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			for _, view := range []string{"mv_logs_hourly", "mv_logs_filterdata"} {
				if err := tx.Exec("DROP MATERIALIZED VIEW IF EXISTS " + view + " CASCADE").Error; err != nil {
					return fmt.Errorf("failed to drop %s: %w", view, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			// No rollback needed — ensureMatViews will recreate on next startup
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while recreating matviews with governance columns: %s", err.Error())
	}
	return nil
}

// migrationAddOCROutputColumn adds the ocr_output column to the Log table
func migrationAddOCROutputColumn(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_ocr_output_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&Log{}, "ocr_output") {
				if err := migrator.AddColumn(&Log{}, "ocr_output"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if migrator.HasColumn(&Log{}, "ocr_output") {
				if err := migrator.DropColumn(&Log{}, "ocr_output"); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding ocr output column: %s", err.Error())
	}
	return nil
}

// migrationAddAttemptTrailColumn adds the attempt_trail column to the Log table.
// This column stores a JSON-serialized []schemas.KeyAttemptRecord capturing the per-attempt
// key selection history for requests that use key-based providers.
func migrationAddAttemptTrailColumn(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_attempt_trail_column",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasColumn(&Log{}, "attempt_trail") {
				if err := migrator.AddColumn(&Log{}, "attempt_trail"); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if migrator.HasColumn(&Log{}, "attempt_trail") {
				if err := migrator.DropColumn(&Log{}, "attempt_trail"); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding attempt trail column: %s", err.Error())
	}
	return nil
}

// migrationAddSelectedPromptColumns adds selected_prompt_name, selected_prompt_version, selected_prompt_id for logs UI.
func migrationAddSelectedPromptColumns(ctx context.Context, db *gorm.DB) error {
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true

	columns := []string{"selected_prompt_name", "selected_prompt_version", "selected_prompt_id"}

	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: "logs_add_selected_prompt_columns",
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mig := tx.Migrator()
			for _, col := range columns {
				if !mig.HasColumn(&Log{}, col) {
					if err := mig.AddColumn(&Log{}, col); err != nil {
						return err
					}
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			mig := tx.Migrator()
			for _, col := range columns {
				if mig.HasColumn(&Log{}, col) {
					if err := mig.DropColumn(&Log{}, col); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding selected prompt columns: %s", err.Error())
	}
	return nil
}
