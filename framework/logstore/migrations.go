package logstore

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/maximhq/bifrost/framework/migrator"
	"gorm.io/gorm"
)

const (
	// migrationAdvisoryLockKey is used for PostgreSQL advisory locks
	// to serialize migrations across cluster nodes.
	// This is the SAME key used by configstore migrations to ensure
	// all migrations are fully serialized.
	migrationAdvisoryLockKey = 1000001
)

// migrationLock holds a dedicated connection for the advisory lock.
// This ensures the lock is held on the same connection throughout migrations,
// preventing race conditions caused by GORM's connection pooling.
type migrationLock struct {
	conn *sql.Conn
}

// acquireMigrationLock gets a dedicated connection and acquires an advisory lock.
// For non-PostgreSQL databases, returns a no-op lock.
func acquireMigrationLock(ctx context.Context, db *gorm.DB) (*migrationLock, error) {
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

	// Acquire advisory lock on this dedicated connection.
	// This will BLOCK if another node holds the lock.
	_, err = conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationAdvisoryLockKey)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to acquire migration advisory lock: %w", err)
	}

	return &migrationLock{conn: conn}, nil
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

// Migrate performs the necessary database migrations.
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
	return nil
}

// migrationInit is the first migration
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

// migrationUpdateObjectColumnValues updates the object column values from old format to new format
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

// migrationAddParentRequestIDColumn adds the parent_request_id column to the logs table
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

// migrationAddPerformanceIndexes adds indexes for performance optimization
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

// migrationUpdateLogsTimestampFormat converts local timestamps to UTC timestamps in logs table
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
