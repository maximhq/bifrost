package logstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/migrator"
	"gorm.io/gorm"
)

// isValidJSON checks if a string is valid JSON.
func isValidJSON(s string) bool {
	var js json.RawMessage
	return json.Unmarshal([]byte(s), &js) == nil
}

// addColumnIfNotExists is a package-local alias for migrator.AddColumnIfNotExists,
// the idempotent column-add helper shared with configstore. Declared at package
// scope (where `migrator` resolves to the package, not the `migrator :=
// tx.Migrator()` locals inside migration closures) so every call site can keep
// calling addColumnIfNotExists(tx, ...) directly.
var addColumnIfNotExists = migrator.AddColumnIfNotExists

// dropColumnIfExists is the drop counterpart to addColumnIfNotExists: a
// package-local alias for migrator.DropColumnIfExists, the idempotent
// column-drop helper shared with configstore. Declared at package scope for the
// same reason as above so call sites can use dropColumnIfExists(tx, ...).
var dropColumnIfExists = migrator.DropColumnIfExists

const (
	// migrationAdvisoryLockKey is used for PostgreSQL advisory locks
	// to serialize migrations across cluster nodes.
	// This is intentionally separate from the configstore migration lock so
	// configstore and logstore migrations can proceed independently.
	migrationAdvisoryLockKey = 1000011

	// indexAdvisoryLockKey serializes the background index build across
	// cluster nodes. It is intentionally a DIFFERENT key from migrationAdvisoryLockKey
	// so that the long-running CREATE INDEX CONCURRENTLY held by one pod's goroutine
	// does not block other pods from running their (fast) migrations on startup.
	indexAdvisoryLockKey = 1000012

	// matviewRefreshAdvisoryLockKey serializes materialized view maintenance
	// across cluster nodes. Startup create/repair and periodic refresh both use
	// this key so they never overlap.
	matviewRefreshAdvisoryLockKey = 1000015

	// advisoryLockRetryInterval is how long to wait between lock acquisition attempts.
	advisoryLockRetryInterval = 5 * time.Second

	// advisoryLockTimeout is the maximum time to wait for an advisory lock
	// before giving up with actionable operator guidance.
	advisoryLockTimeout = 5 * time.Minute

	// maintenanceUpdateBatchSize bounds background data cleanups so they don't
	// lock or rewrite very large log tables in one transaction.
	maintenanceUpdateBatchSize = 10_000
)

// advisoryLock holds a dedicated connection and the advisory lock key.
// This ensures the lock is held on the same connection throughout its lifetime,
// preventing race conditions caused by GORM's connection pooling.
type advisoryLock struct {
	conn    *sql.Conn
	lockKey int64
}

// acquireAdvisoryLock gets a dedicated connection and acquires a PostgreSQL advisory lock
// using pg_try_advisory_lock with retry + timeout. This prevents pods from
// blocking indefinitely if a previous pod crashed without releasing the lock
// (e.g., behind a connection proxy or with slow TCP keepalive detection).
// For non-PostgreSQL databases, returns a no-op lock.
func acquireAdvisoryLock(ctx context.Context, db *gorm.DB, logger schemas.Logger, lockKey int64, label string) (*advisoryLock, error) {
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

	logger.Info("[logstore] attempting to acquire %s lock %d", label, lockKey)
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
		err = conn.QueryRowContext(attemptCtx, "SELECT pg_try_advisory_lock($1)", lockKey).Scan(&acquired)
		attemptCancel()
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("failed to attempt %s advisory lock: %w", label, err)
		}

		if acquired {
			if attempt > 1 {
				logger.Info("[logstore] %s lock acquired after %d attempts", label, attempt)
			}
			return &advisoryLock{conn: conn, lockKey: lockKey}, nil
		}

		// Lock not acquired -- check if we've exceeded the timeout
		if time.Now().After(deadline) {
			conn.Close()
			return nil, fmt.Errorf(
				"failed to acquire logstore %s lock (key=%d) after %d attempts over %s\n\n"+
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
				label, lockKey, attempt, advisoryLockTimeout,
				lockKey, lockKey,
			)
		}

		logger.Info("[logstore] waiting for %s lock (attempt %d/%d) - another node is running %s operations, retrying in %s...",
			label, attempt, maxAttempts, label, advisoryLockRetryInterval)

		// Wait before retrying, but respect context cancellation
		select {
		case <-ctx.Done():
			conn.Close()
			return nil, fmt.Errorf("context cancelled while waiting for %s lock: %w", label, ctx.Err())
		case <-time.After(advisoryLockRetryInterval):
		}
	}
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
func acquireMigrationLock(ctx context.Context, db *gorm.DB, logger schemas.Logger) (*advisoryLock, error) {
	return acquireAdvisoryLock(ctx, db, logger, migrationAdvisoryLockKey, "migration")
}

// acquireIndexLock acquires the serialization lock for the background index build.
func acquireIndexLock(ctx context.Context, db *gorm.DB, logger schemas.Logger) (*advisoryLock, error) {
	return acquireAdvisoryLock(ctx, db, logger, indexAdvisoryLockKey, "index")
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

// logstoreMigrationSteps is the ordered source of truth for logstore migration
// execution and preflight checks.
var logstoreMigrationSteps = []migrationStep{
	{IDs: []string{"logs_init"}, run: migrationInit},
	{IDs: []string{"logs_init_update_object_column_values"}, run: migrationUpdateObjectColumnValues},
	{IDs: []string{"logs_init_add_parent_request_id_column"}, run: migrationAddParentRequestIDColumn},
	{IDs: []string{"logs_init_add_responses_output_column"}, run: migrationAddResponsesOutputColumn},
	{IDs: []string{"logs_init_add_cost_and_cache_debug_column"}, run: migrationAddCostAndCacheDebugColumn},
	{IDs: []string{"logs_init_add_responses_input_history_column"}, run: migrationAddResponsesInputHistoryColumn},
	{IDs: []string{"logs_init_add_number_of_retries_and_fallback_index_and_selected_key_and_virtual_key_columns"}, run: migrationAddNumberOfRetriesAndFallbackIndexAndSelectedKeyAndVirtualKeyColumns},
	{IDs: []string{"logs_add_performance_indexes"}, run: migrationAddPerformanceIndexes},
	{IDs: []string{"logs_add_performance_indexes_v2"}, run: migrationAddPerformanceIndexesV2},
	{IDs: []string{"logs_update_timestamp_format"}, run: migrationUpdateTimestampFormat},
	{IDs: []string{"logs_add_raw_request_column"}, run: migrationAddRawRequestColumn},
	{IDs: []string{"mcp_tool_logs_init"}, run: migrationCreateMCPToolLogsTable},
	{IDs: []string{"mcp_tool_logs_add_cost_column"}, run: migrationAddCostColumnToMCPToolLogs},
	{IDs: []string{"logs_add_image_generation_output_column"}, run: migrationAddImageGenerationOutputColumn},
	{IDs: []string{"logs_add_image_generation_input_column"}, run: migrationAddImageGenerationInputColumn},
	{IDs: []string{"logs_add_routing_rule_id_and_routing_rule_name_columns"}, run: migrationAddRoutingRuleIDAndRoutingRuleNameColumns},
	{IDs: []string{"mcp_tool_logs_add_virtual_key_columns"}, run: migrationAddVirtualKeyColumnsToMCPToolLogs},
	{IDs: []string{"logs_add_routing_engine_used_column"}, run: migrationAddRoutingEngineUsedColumn},
	{IDs: []string{"logs_add_routing_engines_used_column"}, run: migrationAddRoutingEnginesUsedColumn},
	{IDs: []string{"logs_add_list_models_output_column"}, run: migrationAddListModelsOutputColumn},
	{IDs: []string{"logs_add_rerank_output_column"}, run: migrationAddRerankOutputColumn},
	{IDs: []string{"logs_add_routing_engine_logs_column"}, run: migrationAddRoutingEngineLogsColumn},
	{IDs: []string{"async_jobs_init"}, run: migrationCreateAsyncJobsTable},
	{IDs: []string{"logs_add_metadata_column"}, run: migrationAddMetadataColumn},
	{IDs: []string{"mcp_tool_logs_add_metadata_column"}, run: migrationAddMetadataColumnToMCPToolLogs},
	{IDs: []string{"logs_add_histogram_composite_indexes"}, run: migrationAddHistogramCompositeIndexes},
	{IDs: []string{"logs_add_video_columns"}, run: migrationAddVideoColumns},
	{IDs: []string{"logs_add_provider_histogram_index"}, run: migrationAddProviderHistogramIndex},
	{IDs: []string{"logs_add_large_payload_columns"}, run: migrationAddLargePayloadColumns},
	{IDs: []string{"logs_add_passthrough_request_body_column"}, run: migrationAddPassthroughRequestBodyColumn},
	{IDs: []string{"logs_add_passthrough_response_body_column"}, run: migrationAddPassthroughResponseBodyColumn},
	{IDs: []string{"logs_add_metadata_gin_index_v3"}, run: migrationAddMetadataGINIndex},
	{IDs: []string{"logs_dashboard_enhancements"}, run: migrationAddDashboardEnhancements},
	{IDs: []string{"logs_and_dashboard_performance_indexes"}, run: migrationAddLogsAndDashboardPerformanceIndexes},
	{IDs: []string{"logs_add_image_edit_input_column"}, run: migrationAddImageEditInputColumn},
	{IDs: []string{"logs_add_image_variation_input_column"}, run: migrationAddImageVariationInputColumn},
	{IDs: []string{"logs_add_plugin_logs_column"}, run: migrationAddPluginLogsColumn},
	{IDs: []string{"logs_add_alias_column"}, run: migrationAddAliasColumn},
	{IDs: []string{"logs_add_governance_context_columns"}, run: migrationAddGovernanceContextColumns},
	{IDs: []string{"logs_recreate_matviews_with_governance_columns"}, run: migrationRecreateMatViewsWithGovernanceColumns},
	{IDs: []string{"logs_add_ocr_output_column"}, run: migrationAddOCROutputColumn},
	{IDs: []string{"mcp_tool_logs_add_request_id_column"}, run: migrationAddRequestIDColumnToMCPToolLogs},
	{IDs: []string{"logs_add_has_object_column"}, run: migrationAddHasObjectColumn},
	{IDs: []string{"mcp_tool_logs_add_has_object_column"}, run: migrationAddHasObjectColumnToMCPToolLogs},
	{IDs: []string{"logs_add_attempt_trail_column"}, run: migrationAddAttemptTrailColumn},
	{IDs: []string{"logs_add_selected_prompt_columns"}, run: migrationAddSelectedPromptColumns},
	{IDs: []string{"logs_add_user_name_column"}, run: migrationAddUserNameColumn},
	{IDs: []string{"logs_add_ocr_input_column"}, run: migrationAddOCRInputColumn},
	{IDs: []string{"logs_add_stop_reason_column"}, run: migrationAddStopReasonColumn},
	{IDs: []string{"logs_add_safe_jsonb_function"}, run: migrationAddSafeJsonbFunction},
	{IDs: []string{"mcp_tool_logs_add_dac_columns"}, run: migrationAddDACColumnsToMCPToolLogs},
	{IDs: []string{"logs_add_cluster_governance_columns"}, run: migrationAddClusterGovernanceColumns},
	{IDs: []string{"logs_add_inc_number_column"}, run: migrationAddLogIncNumberColumn},
	{IDs: []string{"logs_recreate_filter_users_matview"}, run: migrationRecreateFilterUsersMatView},
	{IDs: []string{"logs_add_multi_team_business_unit_columns"}, run: migrationAddMultiTeamBusinessUnitColumns},
	{IDs: []string{"logs_add_multi_team_bu_gin_indexes_v1"}, run: migrationAddMultiTeamBusinessUnitGINIndexes},
	{IDs: []string{"logs_recreate_filter_team_bu_matviews_multivalue"}, run: migrationRecreateFilterTeamBUMatViews},
	{IDs: []string{"logs_add_customer_array_columns"}, run: migrationAddCustomerArrayColumns},
	{IDs: []string{"logs_add_customer_array_gin_indexes_v1"}, run: migrationAddCustomerArrayGINIndexes},
	{IDs: []string{"logs_recreate_filter_customers_matview_multivalue"}, run: migrationRecreateFilterCustomersMatView},
	{IDs: []string{"logs_add_canonical_model_columns_v2"}, run: migrationAddCanonicalModelColumns},
	{IDs: []string{"logs_add_redaction_mapping_column"}, run: migrationAddRedactionMappingColumn},
	{IDs: []string{"webhook_deliveries_init"}, run: migrationCreateWebhookDeliveriesTable},
	{IDs: []string{"async_jobs_add_webhook_endpoint_id_column"}, run: migrationAddWebhookEndpointIDColumn},
	{IDs: []string{"async_jobs_add_request_id_column"}, run: migrationAddAsyncJobRequestIDColumn},
	{IDs: []string{"webhook_deliveries_add_request_id_column"}, run: migrationAddWebhookDeliveryRequestIDColumn},
	{IDs: []string{"logs_add_content_hidden_column"}, run: migrationAddContentHiddenColumn},
	{IDs: []string{"logs_add_server_side_fallback_model_column"}, run: migrationAddServerSideFallbackModelColumn},
}

// areThereAnyPendingMigrations returns true if there are any pending migrations to be applied.
func areThereAnyPendingMigrations(ctx context.Context, db *gorm.DB, logger schemas.Logger) bool {
	pending, err := pendingMigrationStepIDs(ctx, db, logstoreMigrationSteps)
	if err != nil {
		logger.Warn("[logstore] migration preflight failed; acquiring migration lock and running migrations: %v", err)
	}
	if len(pending) > 0 {
		logger.Info("[logstore] pending migrations")
		for _, id := range pending {
			logger.Info("[logstore] migration : %s", id)
		}
	}
	return err != nil || len(pending) > 0
}

// triggerMigrations runs all registered logstore schema migrations in order under
// a PostgreSQL advisory lock so only one node migrates the logstore at a time.
func triggerMigrations(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	if migrator.SkipStartupMigrations() {
		if areThereAnyPendingMigrations(ctx, db, logger) {
			return fmt.Errorf("[logstore] schema migrations are pending but this process was started with --no-migrate; apply them out of band first (e.g. run bifrost with --migrate-only)")
		}
		logger.Info("[logstore] --no-migrate set and schema is current; skipping migration run")
		return nil
	}
	if !areThereAnyPendingMigrations(ctx, db, logger) {
		logger.Info("[logstore] migrations already current; skipping migration lock")
		return nil
	}

	// Acquire advisory lock to serialize migrations across cluster nodes.
	lock, err := acquireMigrationLock(ctx, db, logger)
	if err != nil {
		return err
	}
	defer lock.release(ctx)

	if !areThereAnyPendingMigrations(ctx, db, logger) {
		logger.Info("[logstore] migrations completed by another node; skipping migration run")
		return nil
	}

	return runMigrationSteps(ctx, db, logger, logstoreMigrationSteps)
}

// migrationInit creates the logs table if it does not exist.
func migrationInit(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_init"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if !migrator.HasTable(&Log{}) {
				logger.Info("[logstore] %s: creating table Log", migrationName)
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
			logger.Info("[logstore] %s: dropping table Log", migrationName)
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
func migrationUpdateObjectColumnValues(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_init_update_object_column_values"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = false
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if tx.Dialector.Name() != "postgres" {
				result := tx.Exec(`
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
						)`)
				if result.Error != nil {
					return fmt.Errorf("failed to update object_type values: %w", result.Error)
				}
				return nil
			}

			updateSQL := `
				WITH batch AS (
					SELECT ctid,
						CASE object_type
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
						END AS normalized_object_type
					FROM logs
					WHERE object_type IN (
						'chat.completion', 'text.completion', 'list',
						'audio.speech', 'audio.transcription', 'chat.completion.chunk',
						'audio.speech.chunk', 'audio.transcription.chunk',
						'response', 'response.completion.chunk'
					)
					LIMIT ?
					FOR UPDATE SKIP LOCKED
				)
				UPDATE logs
				SET object_type = batch.normalized_object_type
				FROM batch
				WHERE logs.ctid = batch.ctid`

			if err := execBatchedGormMaintenanceUpdate(tx, "object_type normalization", updateSQL); err != nil {
				return err
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
func migrationAddParentRequestIDColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_init_add_parent_request_id_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "parent_request_id"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "parent_request_id"); err != nil {
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
func migrationAddResponsesOutputColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_init_add_responses_output_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "responses_output"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &Log{}, "input_history"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &Log{}, "output_message"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &Log{}, "embedding_output"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &Log{}, "raw_response"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "responses_output"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &Log{}, "input_history"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &Log{}, "output_message"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &Log{}, "embedding_output"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &Log{}, "raw_response"); err != nil {
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
func migrationAddCostAndCacheDebugColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_init_add_cost_and_cache_debug_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "cost"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &Log{}, "cache_debug"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "cost"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &Log{}, "cache_debug"); err != nil {
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
func migrationAddResponsesInputHistoryColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_init_add_responses_input_history_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "responses_input_history"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "responses_input_history"); err != nil {
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
func migrationAddNumberOfRetriesAndFallbackIndexAndSelectedKeyAndVirtualKeyColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_init_add_number_of_retries_and_fallback_index_and_selected_key_and_virtual_key_columns"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "number_of_retries"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &Log{}, "fallback_index"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &Log{}, "selected_key_id"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &Log{}, "selected_key_name"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &Log{}, "virtual_key_id"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &Log{}, "virtual_key_name"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "number_of_retries"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &Log{}, "fallback_index"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &Log{}, "selected_key_id"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &Log{}, "selected_key_name"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &Log{}, "virtual_key_id"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &Log{}, "virtual_key_name"); err != nil {
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
func migrationAddPerformanceIndexes(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_performance_indexes"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			if tx.Dialector.Name() == "postgres" {
				return nil
			}
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			// Add index on latency for AVG aggregation queries
			if !migrator.HasIndex(&Log{}, "idx_logs_latency") {
				logger.Info("[logstore] %s: creating index idx_logs_latency on Log", migrationName)
				if err := migrator.CreateIndex(&Log{}, "idx_logs_latency"); err != nil {
					return fmt.Errorf("failed to create index on latency: %w", err)
				}
			}

			// Add index on total_tokens for SUM aggregation queries
			if !migrator.HasIndex(&Log{}, "idx_logs_total_tokens") {
				logger.Info("[logstore] %s: creating index idx_logs_total_tokens on Log", migrationName)
				if err := migrator.CreateIndex(&Log{}, "idx_logs_total_tokens"); err != nil {
					return fmt.Errorf("failed to create index on total_tokens: %w", err)
				}
			}

			// Add index on selected_key_id for filtering
			if !migrator.HasIndex(&Log{}, "idx_logs_selected_key_id") {
				logger.Info("[logstore] %s: creating index idx_logs_selected_key_id on Log", migrationName)
				if err := migrator.CreateIndex(&Log{}, "idx_logs_selected_key_id"); err != nil {
					return fmt.Errorf("failed to create index on selected_key_id: %w", err)
				}
			}

			// Add index on virtual_key_id for filtering
			if !migrator.HasIndex(&Log{}, "idx_logs_virtual_key_id") {
				logger.Info("[logstore] %s: creating index idx_logs_virtual_key_id on Log", migrationName)
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
				logger.Info("[logstore] %s: dropping index idx_logs_latency on Log", migrationName)
				if err := migrator.DropIndex(&Log{}, "idx_logs_latency"); err != nil {
					return err
				}
			}
			if migrator.HasIndex(&Log{}, "idx_logs_total_tokens") {
				logger.Info("[logstore] %s: dropping index idx_logs_total_tokens on Log", migrationName)
				if err := migrator.DropIndex(&Log{}, "idx_logs_total_tokens"); err != nil {
					return err
				}
			}
			if migrator.HasIndex(&Log{}, "idx_logs_selected_key_id") {
				logger.Info("[logstore] %s: dropping index idx_logs_selected_key_id on Log", migrationName)
				if err := migrator.DropIndex(&Log{}, "idx_logs_selected_key_id"); err != nil {
					return err
				}
			}
			if migrator.HasIndex(&Log{}, "idx_logs_virtual_key_id") {
				logger.Info("[logstore] %s: dropping index idx_logs_virtual_key_id on Log", migrationName)
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
func migrationAddPerformanceIndexesV2(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_performance_indexes_v2"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			if tx.Dialector.Name() == "postgres" {
				return nil
			}
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

			logger.Info("[logstore] %s: processing %d indices", migrationName, len(indices))
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
func migrationUpdateTimestampFormat(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_update_timestamp_format"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	// only run the migration for sqlite databases
	dialect := db.Dialector.Name()
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			if dialect != "sqlite" {
				logger.Info("[logstore] skipping migration %s for dialect %s", migrationName, dialect)
				return nil
			}
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
func migrationAddRawRequestColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_raw_request_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "raw_request"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "raw_request"); err != nil {
				return err
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
func migrationCreateMCPToolLogsTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "mcp_tool_logs_init"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			tableExists := migrator.HasTable(&MCPToolLog{})
			if !tableExists {
				logger.Info("[logstore] %s: creating table MCPToolLog", migrationName)
				if err := migrator.CreateTable(&MCPToolLog{}); err != nil {
					return err
				}
			}
			if tx.Dialector.Name() == "postgres" && tableExists {
				return nil
			}

			// Explicitly create indexes as declared in struct tags
			if !migrator.HasIndex(&MCPToolLog{}, "idx_mcp_logs_llm_request_id") {
				logger.Info("[logstore] %s: creating index idx_mcp_logs_llm_request_id on MCPToolLog", migrationName)
				if err := migrator.CreateIndex(&MCPToolLog{}, "idx_mcp_logs_llm_request_id"); err != nil {
					return fmt.Errorf("failed to create index on llm_request_id: %w", err)
				}
			}

			if !migrator.HasIndex(&MCPToolLog{}, "idx_mcp_logs_tool_name") {
				logger.Info("[logstore] %s: creating index idx_mcp_logs_tool_name on MCPToolLog", migrationName)
				if err := migrator.CreateIndex(&MCPToolLog{}, "idx_mcp_logs_tool_name"); err != nil {
					return fmt.Errorf("failed to create index on tool_name: %w", err)
				}
			}

			if !migrator.HasIndex(&MCPToolLog{}, "idx_mcp_logs_server_label") {
				logger.Info("[logstore] %s: creating index idx_mcp_logs_server_label on MCPToolLog", migrationName)
				if err := migrator.CreateIndex(&MCPToolLog{}, "idx_mcp_logs_server_label"); err != nil {
					return fmt.Errorf("failed to create index on server_label: %w", err)
				}
			}

			if !migrator.HasIndex(&MCPToolLog{}, "idx_mcp_logs_latency") {
				logger.Info("[logstore] %s: creating index idx_mcp_logs_latency on MCPToolLog", migrationName)
				if err := migrator.CreateIndex(&MCPToolLog{}, "idx_mcp_logs_latency"); err != nil {
					return fmt.Errorf("failed to create index on latency: %w", err)
				}
			}

			if !migrator.HasIndex(&MCPToolLog{}, "idx_mcp_logs_status") {
				logger.Info("[logstore] %s: creating index idx_mcp_logs_status on MCPToolLog", migrationName)
				if err := migrator.CreateIndex(&MCPToolLog{}, "idx_mcp_logs_status"); err != nil {
					return fmt.Errorf("failed to create index on status: %w", err)
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			logger.Info("[logstore] %s: dropping table MCPToolLog", migrationName)
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
func migrationAddCostColumnToMCPToolLogs(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "mcp_tool_logs_add_cost_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			// Add cost column if it doesn't exist
			if err := addColumnIfNotExists(tx, logger, &MCPToolLog{}, "cost"); err != nil {
				return fmt.Errorf("failed to add cost column: %w", err)
			}

			// Create index on cost column
			if tx.Dialector.Name() != "postgres" && !migrator.HasIndex(&MCPToolLog{}, "idx_mcp_logs_cost") {
				logger.Info("[logstore] %s: creating index idx_mcp_logs_cost on MCPToolLog", migrationName)
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
				logger.Info("[logstore] %s: dropping index idx_mcp_logs_cost on MCPToolLog", migrationName)
				if err := migrator.DropIndex(&MCPToolLog{}, "idx_mcp_logs_cost"); err != nil {
					return err
				}
			}

			// Drop column
			if err := dropColumnIfExists(tx, logger, &MCPToolLog{}, "cost"); err != nil {
				return err
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
func migrationAddImageGenerationOutputColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_image_generation_output_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "image_generation_output"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "image_generation_output"); err != nil {
				return err
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
func migrationAddImageGenerationInputColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_image_generation_input_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "image_generation_input"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "image_generation_input"); err != nil {
				return err
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
func migrationAddRoutingRuleIDAndRoutingRuleNameColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_routing_rule_id_and_routing_rule_name_columns"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "routing_rule_id"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &Log{}, "routing_rule_name"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "routing_rule_id"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &Log{}, "routing_rule_name"); err != nil {
				return err
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
func migrationAddVirtualKeyColumnsToMCPToolLogs(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "mcp_tool_logs_add_virtual_key_columns"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()

			// Add virtual_key_id column if it doesn't exist
			if err := addColumnIfNotExists(tx, logger, &MCPToolLog{}, "virtual_key_id"); err != nil {
				return fmt.Errorf("failed to add virtual_key_id column: %w", err)
			}

			// Add virtual_key_name column if it doesn't exist
			if err := addColumnIfNotExists(tx, logger, &MCPToolLog{}, "virtual_key_name"); err != nil {
				return fmt.Errorf("failed to add virtual_key_name column: %w", err)
			}

			// Create index on virtual_key_id column
			if tx.Dialector.Name() != "postgres" && !migrator.HasIndex(&MCPToolLog{}, "idx_mcp_logs_virtual_key_id") {
				logger.Info("[logstore] %s: creating index idx_mcp_logs_virtual_key_id on MCPToolLog", migrationName)
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
				logger.Info("[logstore] %s: dropping index idx_mcp_logs_virtual_key_id on MCPToolLog", migrationName)
				if err := migrator.DropIndex(&MCPToolLog{}, "idx_mcp_logs_virtual_key_id"); err != nil {
					return err
				}
			}

			// Drop virtual_key_name column
			if err := dropColumnIfExists(tx, logger, &MCPToolLog{}, "virtual_key_name"); err != nil {
				return err
			}

			// Drop virtual_key_id column
			if err := dropColumnIfExists(tx, logger, &MCPToolLog{}, "virtual_key_id"); err != nil {
				return err
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
func migrationAddRoutingEngineUsedColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_routing_engine_used_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
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
			if err := dropColumnIfExists(tx, logger, &Log{}, "routing_engine_used"); err != nil {
				return err
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
func migrationAddRoutingEnginesUsedColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_routing_engines_used_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
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
				if err := dropColumnIfExists(tx, logger, &Log{}, "routing_engine_used"); err != nil {
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
func migrationAddListModelsOutputColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_list_models_output_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "list_models_output"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "list_models_output"); err != nil {
				return err
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
func migrationAddRerankOutputColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_rerank_output_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "rerank_output"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "rerank_output"); err != nil {
				return err
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
func migrationAddRoutingEngineLogsColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_routing_engine_logs_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "routing_engine_logs"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "routing_engine_logs"); err != nil {
				return err
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
func migrationAddLargePayloadColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_large_payload_columns"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "is_large_payload_request"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &Log{}, "is_large_payload_response"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "is_large_payload_request"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &Log{}, "is_large_payload_response"); err != nil {
				return err
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
func migrationCreateAsyncJobsTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "async_jobs_init"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			dbMigrator := tx.Migrator()
			if !dbMigrator.HasTable(&AsyncJob{}) {
				logger.Info("[logstore] %s: creating table AsyncJob", migrationName)
				if err := dbMigrator.CreateTable(&AsyncJob{}); err != nil {
					return err
				}
			}

			// Explicitly create indexes as declared in struct tags
			if !dbMigrator.HasIndex(&AsyncJob{}, "idx_async_jobs_status") {
				logger.Info("[logstore] %s: creating index idx_async_jobs_status on AsyncJob", migrationName)
				if err := dbMigrator.CreateIndex(&AsyncJob{}, "idx_async_jobs_status"); err != nil {
					return fmt.Errorf("failed to create index on status: %w", err)
				}
			}

			if !dbMigrator.HasIndex(&AsyncJob{}, "idx_async_jobs_vk_id") {
				logger.Info("[logstore] %s: creating index idx_async_jobs_vk_id on AsyncJob", migrationName)
				if err := dbMigrator.CreateIndex(&AsyncJob{}, "idx_async_jobs_vk_id"); err != nil {
					return fmt.Errorf("failed to create index on virtual_key_id: %w", err)
				}
			}

			if !dbMigrator.HasIndex(&AsyncJob{}, "idx_async_jobs_expires_at") {
				logger.Info("[logstore] %s: creating index idx_async_jobs_expires_at on AsyncJob", migrationName)
				if err := dbMigrator.CreateIndex(&AsyncJob{}, "idx_async_jobs_expires_at"); err != nil {
					return fmt.Errorf("failed to create index on expires_at: %w", err)
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			logger.Info("[logstore] %s: dropping table AsyncJob", migrationName)
			return tx.Migrator().DropTable(&AsyncJob{})
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while creating async_jobs table: %s", err.Error())
	}
	return nil
}

// migrationCreateWebhookDeliveriesTable creates the webhook_deliveries table
// and its indexes if missing.
func migrationCreateWebhookDeliveriesTable(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "webhook_deliveries_init"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			dbMigrator := tx.Migrator()
			if !dbMigrator.HasTable(&WebhookDelivery{}) {
				logger.Info("[logstore] %s: creating table WebhookDelivery", migrationName)
				if err := dbMigrator.CreateTable(&WebhookDelivery{}); err != nil {
					return err
				}
			}

			// Explicitly create indexes as declared in struct tags
			for _, index := range []string{
				"idx_webhook_deliveries_webhook_id",
				"idx_webhook_deliveries_endpoint_id",
				"idx_webhook_deliveries_created_at",
				"idx_webhook_deliveries_expires_at",
			} {
				if !dbMigrator.HasIndex(&WebhookDelivery{}, index) {
					logger.Info("[logstore] %s: creating index %s on WebhookDelivery", migrationName, index)
					if err := dbMigrator.CreateIndex(&WebhookDelivery{}, index); err != nil {
						return fmt.Errorf("failed to create index %s: %w", index, err)
					}
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			logger.Info("[logstore] %s: dropping table WebhookDelivery", migrationName)
			return tx.Migrator().DropTable(&WebhookDelivery{})
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while creating webhook_deliveries table: %s", err.Error())
	}
	return nil
}

// migrationAddWebhookEndpointIDColumn adds the webhook_endpoint_id column to
// the async_jobs table. It references the webhook endpoint to notify when a
// job reaches a terminal state.
func migrationAddWebhookEndpointIDColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "async_jobs_add_webhook_endpoint_id_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return addColumnIfNotExists(tx, logger, &AsyncJob{}, "webhook_endpoint_id")
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return dropColumnIfExists(tx, logger, &AsyncJob{}, "webhook_endpoint_id")
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding webhook_endpoint_id column: %s", err.Error())
	}
	return nil
}

// migrationAddAsyncJobRequestIDColumn adds the request_id column to the
// async_jobs table: the inference request id the background execution runs
// under, which is also the id its LLM log row is keyed by.
func migrationAddAsyncJobRequestIDColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "async_jobs_add_request_id_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return addColumnIfNotExists(tx, logger, &AsyncJob{}, "request_id")
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return dropColumnIfExists(tx, logger, &AsyncJob{}, "request_id")
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding request_id column: %s", err.Error())
	}
	return nil
}

// migrationAddWebhookDeliveryRequestIDColumn adds the request_id column to
// the webhook_deliveries table for databases created before the column
// existed; fresh databases get it from the table-create migration.
func migrationAddWebhookDeliveryRequestIDColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "webhook_deliveries_add_request_id_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return addColumnIfNotExists(tx, logger, &WebhookDelivery{}, "request_id")
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return dropColumnIfExists(tx, logger, &WebhookDelivery{}, "request_id")
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding request_id column to webhook_deliveries: %s", err.Error())
	}
	return nil
}

// migrationAddMetadataColumn adds the metadata JSON column to the logs table.
func migrationAddMetadataColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_metadata_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "metadata"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "metadata"); err != nil {
				return err
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
func migrationAddMetadataColumnToMCPToolLogs(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "mcp_tool_logs_add_metadata_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &MCPToolLog{}, "metadata"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &MCPToolLog{}, "metadata"); err != nil {
				return err
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
func migrationAddRequestIDColumnToMCPToolLogs(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "mcp_tool_logs_add_request_id_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = false
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			migrator := tx.Migrator()
			if err := addColumnIfNotExists(tx, logger, &MCPToolLog{}, "request_id"); err != nil {
				return err
			}

			if tx.Dialector.Name() == "postgres" {
				if err := execBatchedGormMaintenanceUpdate(tx, "mcp request_id backfill", `
          WITH batch AS (
            SELECT ctid
            FROM mcp_tool_logs
            WHERE request_id IS NULL OR request_id = ''
            LIMIT ?
            FOR UPDATE SKIP LOCKED
          )
          UPDATE mcp_tool_logs
          SET request_id = id
          FROM batch
          WHERE mcp_tool_logs.ctid = batch.ctid
        `); err != nil {
					return err
				}
			} else {
				result := tx.Exec("UPDATE mcp_tool_logs SET request_id = id WHERE request_id IS NULL OR request_id = ''")
				if result.Error != nil {
					return fmt.Errorf("failed to backfill mcp request_id values: %w", result.Error)
				}
			}
			if tx.Dialector.Name() != "postgres" && !migrator.HasIndex(&MCPToolLog{}, "idx_mcp_logs_request_id") {
				logger.Info("[logstore] %s: creating index idx_mcp_logs_request_id on MCPToolLog", migrationName)
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
				logger.Info("[logstore] %s: dropping index idx_mcp_logs_request_id on MCPToolLog", migrationName)
				if err := migrator.DropIndex(&MCPToolLog{}, "idx_mcp_logs_request_id"); err != nil {
					return err
				}
			}
			if err := dropColumnIfExists(tx, logger, &MCPToolLog{}, "request_id"); err != nil {
				return err
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
func migrationAddHistogramCompositeIndexes(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_histogram_composite_indexes"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			if tx.Dialector.Name() == "postgres" {
				return nil
			}
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
func migrationAddVideoColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_video_columns"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			videoColumns := []string{
				"video_generation_input",
				"video_generation_output",
				"video_retrieve_output",
				"video_download_output",
				"video_list_output",
				"video_delete_output",
			}

			for _, column := range videoColumns {
				if err := addColumnIfNotExists(tx, logger, &Log{}, column); err != nil {
					return err
				}
			}

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			videoColumns := []string{
				"video_generation_input",
				"video_generation_output",
				"video_retrieve_output",
				"video_download_output",
				"video_list_output",
				"video_delete_output",
			}

			for _, column := range videoColumns {
				if err := dropColumnIfExists(tx, logger, &Log{}, column); err != nil {
					return err
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
func migrationAddProviderHistogramIndex(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_provider_histogram_index"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = false
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
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
func migrationAddPassthroughRequestBodyColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_passthrough_request_body_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "passthrough_request_body"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "passthrough_request_body"); err != nil {
				return err
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
func migrationAddPassthroughResponseBodyColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_passthrough_response_body_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "passthrough_response_body"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "passthrough_response_body"); err != nil {
				return err
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
func migrationAddMetadataGINIndex(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_metadata_gin_index_v3"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	// UseTransaction must be false because CREATE INDEX CONCURRENTLY cannot
	// run inside a transaction. This avoids deadlocks during rolling upgrades
	// where old pods are still writing to the logs table.
	opts := *migrator.DefaultOptions
	opts.UseTransaction = false
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
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

// migrationAddMultiTeamBusinessUnitGINIndexes registers the GIN indexes backing
// multi-team / multi-BU log filtering. Like migrationAddMetadataGINIndex, the
// build itself is deferred to ensureMultiTeamBusinessUnitGINIndexes (post-startup,
// background, CONCURRENTLY); this migration exists only to provide a rollback that
// drops the indexes. Postgres-only.
func migrationAddMultiTeamBusinessUnitGINIndexes(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_multi_team_bu_gin_indexes_v1"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = false
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if tx.Dialector.Name() == "postgres" {
				if err := tx.Exec("DROP INDEX IF EXISTS idx_logs_team_ids_gin").Error; err != nil {
					return fmt.Errorf("failed to drop team_ids GIN index: %w", err)
				}
				if err := tx.Exec("DROP INDEX IF EXISTS idx_logs_business_unit_ids_gin").Error; err != nil {
					return fmt.Errorf("failed to drop business_unit_ids GIN index: %w", err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while registering multi team/BU GIN indexes: %s", err.Error())
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
		if err := cleanupInvalidLogMetadata(ctx, conn); err != nil {
			return err
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

	// Defensively clean up invalid metadata values before building the index.
	// This runs in small autocommitted batches to avoid one massive row-lock/WAL event.
	if err := cleanupInvalidLogMetadata(ctx, conn); err != nil {
		return err
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

func cleanupInvalidLogMetadata(ctx context.Context, conn *sql.Conn) error {
	return execBatchedMaintenanceUpdate(ctx, conn, "invalid metadata cleanup", `
		WITH batch AS (
			SELECT ctid
			FROM logs
			WHERE metadata IS NOT NULL
			  AND metadata IS NOT JSON OBJECT
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE logs
		SET metadata = NULL
		FROM batch
		WHERE logs.ctid = batch.ctid
	`)
}

// ensureArrayGINIndex builds a partial jsonb_path_ops GIN index on a JSON-array
// text column (e.g. team_ids) so `column::jsonb @> '[...]'` containment filters
// are indexed. Mirrors ensureMetadataGINIndex's lifecycle: tolerates an INVALID
// remnant from an interrupted build and builds CONCURRENTLY so writers are not
// blocked. No data cleanup is needed — the log writer only ever stores a valid
// JSON array or NULL in these columns. indexName/column are internal constants
// (not user input), so identifier interpolation is safe. Postgres-only.
func ensureArrayGINIndex(ctx context.Context, conn *sql.Conn, indexName, column string) error {
	var indexValid bool
	if err := conn.QueryRowContext(ctx, `
		SELECT COALESCE(bool_and(pi.indisvalid), false)
		FROM pg_class pc
		JOIN pg_index pi ON pi.indrelid = pc.oid
		JOIN pg_class ic ON ic.oid = pi.indexrelid
		WHERE pc.relname = 'logs' AND ic.relname = $1
	`, indexName).Scan(&indexValid); err != nil {
		return fmt.Errorf("failed to query GIN index validity for %s: %w", indexName, err)
	}
	if indexValid {
		return nil
	}

	// Drop any INVALID remnant left by a prior interrupted CONCURRENTLY build.
	if _, err := conn.ExecContext(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+indexName); err != nil {
		return fmt.Errorf("failed to drop invalid GIN index %s: %w", indexName, err)
	}

	// Non-fatal tuning to speed up the build.
	_, _ = conn.ExecContext(ctx, "SET maintenance_work_mem = '512MB'")
	_, _ = conn.ExecContext(ctx, "SET max_parallel_maintenance_workers = 4")

	// jsonb_path_ops supports @> (containment) and is ~3x smaller than the default
	// opclass. The partial predicate matches the IS JSON ARRAY guard the filter
	// query adds (rdb.go), so the planner uses this index.
	stmt := fmt.Sprintf(
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS %s ON logs USING gin ((%s::jsonb) jsonb_path_ops) WHERE %s IS NOT NULL AND %s IS JSON ARRAY",
		indexName, column, column, column,
	)
	if _, err := conn.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("failed to create GIN index %s: %w", indexName, err)
	}
	return nil
}

// ensureMultiTeamBusinessUnitGINIndexes builds the GIN indexes backing multi-team
// and multi-BU log filtering (team_ids / business_unit_ids).
func ensureMultiTeamBusinessUnitGINIndexes(ctx context.Context, conn *sql.Conn) error {
	if err := ensureArrayGINIndex(ctx, conn, "idx_logs_team_ids_gin", "team_ids"); err != nil {
		return err
	}
	if err := ensureArrayGINIndex(ctx, conn, "idx_logs_business_unit_ids_gin", "business_unit_ids"); err != nil {
		return err
	}
	return ensureArrayGINIndex(ctx, conn, "idx_logs_customer_ids_gin", "customer_ids")
}

// migrationAddDashboardEnhancements adds cached_read_tokens column to logs table.
// The expensive backfill, covering index rebuild, and MCP index creation are deferred
// to ensureDashboardEnhancements (called post-startup in a background goroutine) so
// they do not block pod startup on large tables.
func migrationAddDashboardEnhancements(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_dashboard_enhancements"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = false
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := addColumnIfNotExists(tx, logger, &Log{}, "CachedReadTokens"); err != nil {
				return fmt.Errorf("failed to add cached_read_tokens column: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			if err := dropColumnIfExists(tx, logger, &Log{}, "cached_read_tokens"); err != nil {
				return err
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
	if err := backfillCachedReadTokens(ctx, conn); err != nil {
		return err
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

func backfillCachedReadTokens(ctx context.Context, conn *sql.Conn) error {
	return execBatchedMaintenanceUpdate(ctx, conn, "cached_read_tokens backfill", `
		WITH batch AS (
			SELECT ctid
			FROM logs
			WHERE cached_read_tokens = 0
			  AND token_usage IS NOT NULL
			  AND token_usage != ''
			  AND token_usage != 'null'
			  AND token_usage ~ '^\s*\{.*\}\s*$'
			  AND COALESCE((token_usage::jsonb->'prompt_tokens_details'->>'cached_read_tokens')::int, 0) > 0
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE logs
		SET cached_read_tokens = (token_usage::jsonb->'prompt_tokens_details'->>'cached_read_tokens')::int
		FROM batch
		WHERE logs.ctid = batch.ctid
	`)
}

func execBatchedMaintenanceUpdate(ctx context.Context, conn *sql.Conn, label string, query string) error {
	for {
		result, err := conn.ExecContext(ctx, query, maintenanceUpdateBatchSize)
		if err != nil {
			return fmt.Errorf("failed to run %s batch: %w", label, err)
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("failed to read %s batch rows affected: %w", label, err)
		}
		if rowsAffected == 0 {
			return nil
		}
	}
}

func execBatchedGormMaintenanceUpdate(tx *gorm.DB, label string, query string) error {
	for {
		result := tx.Exec(query, maintenanceUpdateBatchSize)
		if result.Error != nil {
			return fmt.Errorf("failed to run %s batch: %w", label, result.Error)
		}
		if result.RowsAffected == 0 {
			return nil
		}
	}
}

// migrationAddLogsAndDashboardPerformanceIndexes records the migration version for the performance
// indexes. Actual index creation is deferred to ensurePerformanceIndexes (called
// post-startup in a background goroutine) because CREATE INDEX CONCURRENTLY cannot
// run inside a transaction.
func migrationAddLogsAndDashboardPerformanceIndexes(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_and_dashboard_performance_indexes"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = false
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
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

// ftsInputCharLimit is the maximum number of characters fed to to_tsvector via left().
// It must be low enough that even worst-case multi-byte / high-lexeme content keeps
// the tsvector output under Postgres's 1,048,575-byte hard limit. The same constant
// must be used in both the index expression and the query predicate so the planner
// recognises the expression match and uses the GIN index.
const ftsInputCharLimit = 250000

// performanceIndexes is the set of full-text and GIN indexes built by ensurePerformanceIndexes.
// Each statement uses CREATE INDEX CONCURRENTLY to avoid blocking writes.
var performanceIndexes = []performanceIndexDef{
	{
		table: "logs",
		name:  "idx_logs_latency",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_latency ON logs(latency)",
	},
	{
		table: "logs",
		name:  "idx_logs_total_tokens",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_total_tokens ON logs(total_tokens)",
	},
	{
		table: "logs",
		name:  "idx_logs_selected_key_id",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_selected_key_id ON logs(selected_key_id)",
	},
	{
		table: "logs",
		name:  "idx_logs_virtual_key_id",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_virtual_key_id ON logs(virtual_key_id)",
	},
	{
		table: "logs",
		name:  "idx_logs_timestamp",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_timestamp ON logs(timestamp)",
	},
	{
		table: "logs",
		name:  "idx_logs_status",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_status ON logs(status)",
	},
	{
		table: "logs",
		name:  "idx_logs_created_at",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_created_at ON logs(created_at)",
	},
	{
		table: "logs",
		name:  "idx_logs_provider",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_provider ON logs(provider)",
	},
	{
		table: "logs",
		name:  "idx_logs_model",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_model ON logs(model)",
	},
	{
		table: "logs",
		name:  "idx_logs_object_type",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_object_type ON logs(object_type)",
	},
	{
		table: "logs",
		name:  "idx_logs_cost",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_cost ON logs(cost)",
	},
	{
		table: "logs",
		name:  "idx_logs_status_timestamp",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_status_timestamp ON logs(status, timestamp)",
	},
	{
		table: "logs",
		name:  "idx_logs_status_created_at",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_status_created_at ON logs(status, created_at)",
	},
	{
		table: "mcp_tool_logs",
		name:  "idx_mcp_logs_llm_request_id",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_mcp_logs_llm_request_id ON mcp_tool_logs(llm_request_id)",
	},
	{
		table: "mcp_tool_logs",
		name:  "idx_mcp_logs_tool_name",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_mcp_logs_tool_name ON mcp_tool_logs(tool_name)",
	},
	{
		table: "mcp_tool_logs",
		name:  "idx_mcp_logs_server_label",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_mcp_logs_server_label ON mcp_tool_logs(server_label)",
	},
	{
		table: "mcp_tool_logs",
		name:  "idx_mcp_logs_latency",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_mcp_logs_latency ON mcp_tool_logs(latency)",
	},
	{
		table: "mcp_tool_logs",
		name:  "idx_mcp_logs_status",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_mcp_logs_status ON mcp_tool_logs(status)",
	},
	{
		table: "mcp_tool_logs",
		name:  "idx_mcp_logs_cost",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_mcp_logs_cost ON mcp_tool_logs(cost)",
	},
	{
		table: "mcp_tool_logs",
		name:  "idx_mcp_logs_virtual_key_id",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_mcp_logs_virtual_key_id ON mcp_tool_logs(virtual_key_id)",
	},
	{
		table: "mcp_tool_logs",
		name:  "idx_mcp_logs_request_id",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_mcp_logs_request_id ON mcp_tool_logs(request_id)",
	},
	{
		table: "logs",
		name:  "idx_logs_content_summary_fts",
		// left() caps input characters to stay within to_tsvector's 1MB output limit.
		// Use ftsInputCharLimit: even worst-case UTF-8 / high-lexeme content stays
		// well under the 1,048,575-byte tsvector limit.
		sql: fmt.Sprintf("CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_content_summary_fts ON logs USING GIN (to_tsvector('simple', left(content_summary, %d))) WHERE content_summary IS NOT NULL", ftsInputCharLimit),
	},
	{
		table: "mcp_tool_logs",
		name:  "idx_mcp_logs_arguments_fts",
		sql:   fmt.Sprintf("CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_mcp_logs_arguments_fts ON mcp_tool_logs USING GIN (to_tsvector('simple', left(arguments, %d))) WHERE arguments IS NOT NULL", ftsInputCharLimit),
	},
	{
		table: "mcp_tool_logs",
		name:  "idx_mcp_logs_result_fts",
		sql:   fmt.Sprintf("CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_mcp_logs_result_fts ON mcp_tool_logs USING GIN (to_tsvector('simple', left(result, %d))) WHERE result IS NOT NULL", ftsInputCharLimit),
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
		name:  "idx_logs_canonical_model_name",
		// Filtering by model matches either `model` or `canonical_model_name`
		// (see applyFilters). This single-column index lets Postgres BitmapOr it
		// with idx_logs_model instead of scanning the whole table for the OR branch.
		sql: "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_canonical_model_name ON logs(canonical_model_name) WHERE canonical_model_name IS NOT NULL",
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
	{
		table: "logs",
		name:  "idx_logs_status_parent_request_id",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_status_parent_request_id ON logs(status, parent_request_id) WHERE parent_request_id IS NOT NULL",
	},
	{
		table: "logs",
		name:  "idx_logs_stop_reason",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_stop_reason ON logs(stop_reason)",
	},
	{
		table: "mcp_tool_logs",
		name:  "idx_mcp_logs_user_id",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_mcp_logs_user_id ON mcp_tool_logs(user_id)",
	},
	{
		table: "mcp_tool_logs",
		name:  "idx_mcp_logs_team_id",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_mcp_logs_team_id ON mcp_tool_logs(team_id)",
	},
	{
		table: "mcp_tool_logs",
		name:  "idx_mcp_logs_customer_id",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_mcp_logs_customer_id ON mcp_tool_logs(customer_id)",
	},
	{
		table: "mcp_tool_logs",
		name:  "idx_mcp_logs_business_unit_id",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_mcp_logs_business_unit_id ON mcp_tool_logs(business_unit_id)",
	},
	{
		table: "logs",
		name:  "idx_logs_cluster_node_id",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_cluster_node_id ON logs(cluster_node_id, timestamp) WHERE cluster_node_id IS NOT NULL",
	},
	{
		table: "logs",
		name:  "idx_logs_cluster_node_usage",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_cluster_node_usage ON logs(cluster_node_id, status, timestamp, id) WHERE cluster_node_id IS NOT NULL",
	},
	{
		table: "logs",
		name:  "idx_logs_cluster_node_inc_usage",
		sql:   "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_cluster_node_inc_usage ON logs(cluster_node_id, status, inc_number) WHERE cluster_node_id IS NOT NULL AND inc_number IS NOT NULL",
	},
}

// ensurePerformanceIndexes checks whether each performance GIN index exists and is
// valid. If an index is missing or was left in an INVALID state by a previously
// interrupted CREATE INDEX CONCURRENTLY, it drops the remnant and rebuilds.
//
// This is intentionally separate from migrationAddPerformanceGINIndexes so that the
// long-running CREATE INDEX CONCURRENTLY does not block pod startup. Callers that
// want non-blocking behaviour should invoke this in a goroutine (see postgres.go).
func ensurePerformanceIndexes(ctx context.Context, conn *sql.Conn, logger schemas.Logger) error {
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

		logger.Info("[logstore] building performance index %s on %s", idx.name, idx.table)

		// Drop any INVALID remnant left by a prior interrupted CONCURRENTLY build.
		if _, err := conn.ExecContext(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+idx.name); err != nil {
			return fmt.Errorf("failed to drop invalid index %s: %w", idx.name, err)
		}

		// Create the index concurrently (does not block writes).
		if _, err := conn.ExecContext(ctx, idx.sql); err != nil {
			return fmt.Errorf("failed to create index %s: %w", idx.name, err)
		}
		logger.Info("[logstore] built performance index %s on %s", idx.name, idx.table)
	}

	return nil
}

// migrationAddImageEditInputColumn adds the image_edit_input column to the logs table.
func migrationAddImageEditInputColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_image_edit_input_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "image_edit_input"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "image_edit_input"); err != nil {
				return err
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
func migrationAddPluginLogsColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_plugin_logs_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "plugin_logs"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "plugin_logs"); err != nil {
				return err
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
func migrationAddAliasColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_alias_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "alias"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "alias"); err != nil {
				return err
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

// migrationAddCanonicalModelColumns adds the canonical_model_name and
// alias_model_family columns to the logs table. Both are copied from the resolved
// alias config when the request's model was resolved via alias mapping and the
// alias defines them.
func migrationAddCanonicalModelColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_canonical_model_columns_v2"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			for _, column := range []string{"canonical_model_name", "alias_model_family"} {
				if err := addColumnIfNotExists(tx, logger, &Log{}, column); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			for _, column := range []string{"canonical_model_name", "alias_model_family"} {
				if err := dropColumnIfExists(tx, logger, &Log{}, column); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while adding canonical model columns: %s", err.Error())
	}
	return nil
}

// migrationAddHasObjectColumn adds the has_object boolean column to the logs table.
// Used by the hybrid log store to track whether a log's payload is stored in object storage.
func migrationAddHasObjectColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_has_object_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "has_object"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "has_object"); err != nil {
				return err
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

// migrationAddHasObjectColumnToMCPToolLogs adds the has_object boolean column to the mcp_tool_logs table.
// Used by the hybrid log store to track whether an MCP tool log's payload is stored in object storage.
func migrationAddHasObjectColumnToMCPToolLogs(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "mcp_tool_logs_add_has_object_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &MCPToolLog{}, "has_object"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &MCPToolLog{}, "has_object"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding has_object column to mcp_tool_logs: %s", err.Error())
	}
	return nil
}

// migrationAddContentHiddenColumn adds the content_hidden boolean column to the logs table.
// Marks logs whose payload is retained in object storage but must never be served back
// through the API/UI.
func migrationAddContentHiddenColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_content_hidden_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "content_hidden"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "content_hidden"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding content_hidden column: %w", err)
	}
	return nil
}

// migrationAddImageVariationInputColumn adds the image_variation_input column to the logs table.
func migrationAddImageVariationInputColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_image_variation_input_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "image_variation_input"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "image_variation_input"); err != nil {
				return err
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
func migrationAddUserNameColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_user_name_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "user_name"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "user_name"); err != nil {
				return err
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
func migrationAddGovernanceContextColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_governance_context_columns"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true

	columns := []string{"user_id", "team_id", "team_name", "customer_id", "customer_name", "business_unit_id", "business_unit_name"}

	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			for _, col := range columns {
				if err := addColumnIfNotExists(tx, logger, &Log{}, col); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			for _, col := range columns {
				if err := dropColumnIfExists(tx, logger, &Log{}, col); err != nil {
					return err
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

// migrationAddMultiTeamBusinessUnitColumns adds the JSON-array columns capturing
// the full deduped set of teams / business units a request belongs to (enterprise
// user/AP path). The scalar team_id/business_unit_id remain the primary; these
// power display, multi-team filtering (jsonb @> + GIN), and fan-out aggregation.
func migrationAddMultiTeamBusinessUnitColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_multi_team_business_unit_columns"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true

	columns := []string{"team_ids", "team_names", "business_unit_ids", "business_unit_names"}

	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			for _, col := range columns {
				if err := addColumnIfNotExists(tx, logger, &Log{}, col); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			for _, col := range columns {
				if err := dropColumnIfExists(tx, logger, &Log{}, col); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding multi team/business-unit columns: %s", err.Error())
	}
	return nil
}

// migrationRecreateMatViewsWithGovernanceColumns drops and recreates materialized views
// so they include the new governance context columns (user_id, team_id, customer_id, business_unit_id).
// The actual rebuild is deferred to ensureMatViews, which runs after startup on
// a dedicated connection. Dropping materialized views inline in this migration
// can queue heavy locks during rolling deploys on large log tables.
func migrationRecreateMatViewsWithGovernanceColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_recreate_matviews_with_governance_columns"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
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

// migrationSplitFilterDataMatView drops the legacy mv_logs_filterdata view so
// ensureMatViews recreates it as per-dimension matviews (mv_filter_models,
// mv_filter_selected_keys, ...). The old view DISTINCTed across 16 columns and
// could grow nearly as large as the source `logs` table on multi-tenant
// deployments, making REFRESH ... CONCURRENTLY memory-intensive. Splitting per
// dimension keeps each view bounded by a single column's cardinality.
//
// NOT CALLED YET. Multi-replica deployments do rolling restarts, so dropping
// mv_logs_filterdata in this release would make every filterdata request on
// not-yet-upgraded replicas return "relation does not exist" until they
// restart. The per-dimension views ship in this release and the legacy view
// is intentionally left in place. A follow-up release — after this one has
// fully rolled out everywhere — will wire this migration into RunMigrations
// (or add "mv_logs_filterdata" to legacyMatViewNames in matviews.go) to
// actually perform the drop.
func migrationSplitFilterDataMatView(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_split_filter_data_matview"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	// Materialized views are PostgreSQL-only; skip on other dialects.
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			if db.Dialector.Name() != "postgres" {
				return nil
			}
			tx = tx.WithContext(ctx)
			if err := tx.Exec("DROP MATERIALIZED VIEW IF EXISTS mv_logs_filterdata CASCADE").Error; err != nil {
				return fmt.Errorf("failed to drop legacy mv_logs_filterdata: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			// No rollback — ensureMatViews recreates the per-dim views on next startup.
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while splitting filter-data matview: %s", err.Error())
	}
	return nil
}

// migrationAddOCROutputColumn adds the ocr_output column to the Log table
func migrationAddOCROutputColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_ocr_output_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "ocr_output"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "ocr_output"); err != nil {
				return err
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
func migrationAddAttemptTrailColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_attempt_trail_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "attempt_trail"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "attempt_trail"); err != nil {
				return err
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
func migrationAddSelectedPromptColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_selected_prompt_columns"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true

	columns := []string{"selected_prompt_name", "selected_prompt_version", "selected_prompt_id"}

	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			for _, col := range columns {
				if err := addColumnIfNotExists(tx, logger, &Log{}, col); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			for _, col := range columns {
				if err := dropColumnIfExists(tx, logger, &Log{}, col); err != nil {
					return err
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

// migrationAddOCRInputColumn adds the ocr_input column to the logs table.
func migrationAddOCRInputColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_ocr_input_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "ocr_input"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "ocr_input"); err != nil {
				return err
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while adding ocr_input column: %s", err.Error())
	}
	return nil
}

// migrationAddStopReasonColumn adds the stop_reason column to the logs table.
// This column stores the reason why the model stopped generating (e.g., "stop", "length", "content_filter", "tool_calls").
func migrationAddStopReasonColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_stop_reason_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "stop_reason"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "stop_reason"); err != nil {
				return err
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while adding stop_reason column: %s", err.Error())
	}
	return nil
}

// migrationAddRedactionMappingColumn adds the redaction_mapping column to the
// logs table. It stores the reversible redaction mapping (encrypted when an
// encryption key is configured) so that reveal data shares the log row's
// lifecycle: deleting the log deletes the mapping.
func migrationAddRedactionMappingColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_redaction_mapping_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "redaction_mapping"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			// No-op rollback: dropping the column would permanently destroy
			// reveal data for already-redacted logs.
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while adding redaction_mapping column: %s", err.Error())
	}
	return nil
}

// migrationAddSafeJsonbFunction installs a PL/pgSQL helper that the
// /api/logs list query uses to extract the last element of input_history /
// responses_input_history without aborting the whole query on a single bad row.
//
// The previous inline guard (`left(btrim(x),1)='['`) only checked the first
// character before casting to jsonb. Any row that looked array-shaped but
// contained malformed JSON (unterminated structures, trailing commas, unpaired
// UTF-16 surrogates, etc.) would fail the cast with 22P02 / 22P05 and abort the
// entire list response. The helper wraps the cast in an EXCEPTION block and
// returns the raw TEXT on any parse failure.
//
// Postgres-only; SQLite is guarded inline in listSelectColumns via json_valid().
func migrationAddSafeJsonbFunction(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_safe_jsonb_function"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			if db.Dialector.Name() != "postgres" {
				return nil
			}
			tx = tx.WithContext(ctx)
			const stmt = `
CREATE OR REPLACE FUNCTION bifrost_safe_jsonb(t text) RETURNS text
LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    j jsonb;
BEGIN
    IF t IS NULL OR t = '' OR t = '[]' THEN
        RETURN t;
    END IF;
    IF left(btrim(t), 1) <> '[' THEN
        RETURN t;
    END IF;
    BEGIN
        j := t::jsonb;
    EXCEPTION WHEN invalid_text_representation OR untranslatable_character THEN
        RETURN t;
    END;
    IF jsonb_typeof(j) <> 'array' OR jsonb_array_length(j) = 0 THEN
        RETURN t;
    END IF;
    RETURN jsonb_build_array(j->-1)::text;
END;
$$;`
			if err := tx.Exec(stmt).Error; err != nil {
				return fmt.Errorf("failed to create bifrost_safe_jsonb: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return tx.Exec("DROP FUNCTION IF EXISTS bifrost_safe_jsonb(text)").Error
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while adding bifrost_safe_jsonb function: %s", err.Error())
	}
	return nil
}

// migrationAddDACColumnsToMCPToolLogs adds user_id, team_id, customer_id,
// and business_unit_id columns to mcp_tool_logs so DAC scope can apply the
// same ownership predicates it does on the logs table. The columns are
// nullable; pre-existing rows stay NULL and the DAC resolver fails closed
// for non-admin principals against them.
//
// Indexes are built CONCURRENTLY by ensurePerformanceIndexes (entries appended
// to performanceIndexes) so adding them does not block writes on a populated
// table.
func migrationAddDACColumnsToMCPToolLogs(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "mcp_tool_logs_add_dac_columns"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)

			for _, col := range []string{"user_id", "team_id", "customer_id", "business_unit_id"} {
				if err := addColumnIfNotExists(tx, logger, &MCPToolLog{}, col); err != nil {
					return fmt.Errorf("failed to add %s column to mcp_tool_logs: %w", col, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			for _, col := range []string{"business_unit_id", "customer_id", "team_id", "user_id"} {
				if err := dropColumnIfExists(tx, logger, &MCPToolLog{}, col); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while adding DAC columns to mcp_tool_logs: %s", err.Error())
	}
	return nil
}

// migrationAddClusterGovernanceColumns adds cluster_node_id, budget_ids, and rate_limit_ids
// columns to the logs table for node usage recovery in clustered deployments.
func migrationAddClusterGovernanceColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_cluster_governance_columns"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "cluster_node_id"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &Log{}, "budget_ids"); err != nil {
				return err
			}
			if err := addColumnIfNotExists(tx, logger, &Log{}, "rate_limit_ids"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := dropColumnIfExists(tx, logger, &Log{}, "cluster_node_id"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &Log{}, "budget_ids"); err != nil {
				return err
			}
			if err := dropColumnIfExists(tx, logger, &Log{}, "rate_limit_ids"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding cluster governance columns: %s", err.Error())
	}
	return nil
}

// migrationAddLogIncNumberColumn adds a database-assigned monotonic cursor to
// logs. Existing rows remain NULL; new rows receive values from a PostgreSQL
// sequence at insert time. Ghost reconciliation uses this column after its
// initial timestamp query to avoid missing rows flushed late by the async log
// writer.
func migrationAddLogIncNumberColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_inc_number_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if err := addColumnIfNotExists(tx, logger, &Log{}, "IncNumber"); err != nil {
				return err
			}

			if tx.Dialector.Name() == "postgres" {
				if err := tx.Exec("CREATE SEQUENCE IF NOT EXISTS logs_inc_number_seq").Error; err != nil {
					return fmt.Errorf("failed to create logs_inc_number_seq: %w", err)
				}
				if err := tx.Exec("ALTER SEQUENCE logs_inc_number_seq OWNED BY logs.inc_number").Error; err != nil {
					return fmt.Errorf("failed to set logs_inc_number_seq ownership: %w", err)
				}
				if err := tx.Exec("ALTER TABLE logs ALTER COLUMN inc_number SET DEFAULT nextval('logs_inc_number_seq')").Error; err != nil {
					return fmt.Errorf("failed to set inc_number default: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if tx.Dialector.Name() == "postgres" {
				if err := tx.Exec("ALTER TABLE logs ALTER COLUMN inc_number DROP DEFAULT").Error; err != nil {
					return fmt.Errorf("failed to drop inc_number default: %w", err)
				}
				if err := tx.Exec("DROP SEQUENCE IF EXISTS logs_inc_number_seq").Error; err != nil {
					return fmt.Errorf("failed to drop logs_inc_number_seq: %w", err)
				}
			}
			if err := dropColumnIfExists(tx, logger, &Log{}, "IncNumber"); err != nil {
				return err
			}
			return nil
		},
	}})
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("error while adding log inc_number column: %s", err.Error())
	}
	return nil
}

// migrationRecreateFilterUsersMatView drops mv_filter_users so ensureMatViews
// recreates it with the corrected WHERE clause that excludes rows without a
// user_name, preventing duplicate entries where name falls back to user_id.
func migrationRecreateFilterUsersMatView(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_recreate_filter_users_matview"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			if db.Dialector.Name() != "postgres" {
				return nil
			}
			tx = tx.WithContext(ctx)
			if err := tx.Exec("DROP MATERIALIZED VIEW IF EXISTS mv_filter_users CASCADE").Error; err != nil {
				return fmt.Errorf("failed to drop mv_filter_users: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while recreating filter users matview: %s", err.Error())
	}
	return nil
}

// migrationRecreateFilterTeamBUMatViews drops mv_filter_teams and
// mv_filter_business_units so ensureMatViews recreates them with the multi-value
// body (scalar column UNION the JSON-array column). Required because
// repairMatViewShapes only detects drift by column presence, and the column
// shape (id, name, user_id, team_id, virtual_key_id) is unchanged — only the
// SELECT body changed — so the views would otherwise keep their old scalar-only
// definition. Recreated views keep identical columns, so old replicas reading
// them during a rolling deploy are unaffected (no legacyMatViewNames dance).
func migrationRecreateFilterTeamBUMatViews(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_recreate_filter_team_bu_matviews_multivalue"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			if db.Dialector.Name() != "postgres" {
				return nil
			}
			tx = tx.WithContext(ctx)
			for _, view := range []string{"mv_filter_teams", "mv_filter_business_units"} {
				if err := tx.Exec("DROP MATERIALIZED VIEW IF EXISTS " + view + " CASCADE").Error; err != nil {
					return fmt.Errorf("failed to drop %s: %w", view, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while recreating filter team/business-unit matviews: %s", err.Error())
	}
	return nil
}

// migrationAddCustomerArrayColumns adds the JSON-array columns capturing the full
// deduped set of customers a request belongs to (a team can belong to many
// customers via the enterprise team↔customer M2M). The scalar customer_id remains
// the primary; these power display, multi-customer filtering (jsonb @> + GIN), and
// fan-out aggregation, mirroring team_ids / business_unit_ids.
func migrationAddCustomerArrayColumns(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_customer_array_columns"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true

	columns := []string{"customer_ids", "customer_names"}

	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			for _, col := range columns {
				if err := addColumnIfNotExists(tx, logger, &Log{}, col); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			for _, col := range columns {
				if err := dropColumnIfExists(tx, logger, &Log{}, col); err != nil {
					return err
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while adding customer array columns: %s", err.Error())
	}
	return nil
}

// migrationAddCustomerArrayGINIndexes registers the GIN index backing multi-customer
// log filtering. Like the team/BU GIN migration, the build itself is deferred to
// ensureMultiTeamBusinessUnitGINIndexes (post-startup, background, CONCURRENTLY);
// this migration exists only to provide a rollback that drops the index. Postgres-only.
func migrationAddCustomerArrayGINIndexes(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_customer_array_gin_indexes_v1"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = false
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			if tx.Dialector.Name() == "postgres" {
				if err := tx.Exec("DROP INDEX IF EXISTS idx_logs_customer_ids_gin").Error; err != nil {
					return fmt.Errorf("failed to drop customer_ids GIN index: %w", err)
				}
			}
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while registering customer GIN indexes: %s", err.Error())
	}
	return nil
}

// migrationRecreateFilterCustomersMatView drops mv_filter_customers so ensureMatViews
// recreates it with the multi-value body (scalar customer_id UNION the JSON-array
// customer_ids), mirroring migrationRecreateFilterTeamBUMatViews. The column shape
// is unchanged so old replicas reading it during a rolling deploy are unaffected.
func migrationRecreateFilterCustomersMatView(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_recreate_filter_customers_matview_multivalue"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			if db.Dialector.Name() != "postgres" {
				return nil
			}
			tx = tx.WithContext(ctx)
			if err := tx.Exec("DROP MATERIALIZED VIEW IF EXISTS mv_filter_customers CASCADE").Error; err != nil {
				return fmt.Errorf("failed to drop mv_filter_customers: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return nil
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while recreating filter customers matview: %s", err.Error())
	}
	return nil
}

// migrationAddServerSideFallbackModelColumn adds the server_side_fallback_model
// column to the logs table. Records the model that actually produced the response
// when the provider swapped models inside a single call (Anthropic server-side
// fallback) — which routing never sees, so the log's model column still names what
// the caller asked for.
func migrationAddServerSideFallbackModelColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "logs_add_server_side_fallback_model_column"
	logger.Info("[logstore] starting migration %s", migrationName)
	defer logger.Info("[logstore] finished migration %s", migrationName)
	opts := *migrator.DefaultOptions
	opts.UseTransaction = true
	m := migrator.New(db, &opts, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			return addColumnIfNotExists(tx.WithContext(ctx), logger, &Log{}, "server_side_fallback_model")
		},
		Rollback: func(tx *gorm.DB) error {
			return dropColumnIfExists(tx.WithContext(ctx), logger, &Log{}, "server_side_fallback_model")
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while adding server side fallback model column: %s", err.Error())
	}
	return nil
}
