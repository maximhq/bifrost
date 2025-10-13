package logstore

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
	if err := migrationUpdateObjectColumnValues(ctx, db); err != nil {
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
