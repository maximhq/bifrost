package configstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// isUniqueConstraintError checks if the error is a unique constraint violation (SQLite or PostgreSQL)
func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "duplicate key value violates unique constraint")
}

// ============================================================================
// Prompt Repository - Folders
// ============================================================================

// GetFolders gets all folders
func (s *RDBConfigStore) GetFolders(ctx context.Context) ([]tables.TableFolder, error) {
	var folders []tables.TableFolder
	if err := s.db.WithContext(ctx).
		Order("created_at DESC").
		Find(&folders).Error; err != nil {
		return nil, err
	}

	// Get prompts count for each folder
	for i := range folders {
		var count int64
		if err := s.db.WithContext(ctx).Model(&tables.TablePrompt{}).Where("folder_id = ?", folders[i].ID).Count(&count).Error; err != nil {
			return nil, err
		}
		folders[i].PromptsCount = int(count)
	}

	return folders, nil
}

// GetFolderByID gets a folder by ID
func (s *RDBConfigStore) GetFolderByID(ctx context.Context, id string) (*tables.TableFolder, error) {
	var folder tables.TableFolder
	if err := s.db.WithContext(ctx).
		First(&folder, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &folder, nil
}

// CreateFolder creates a new folder
func (s *RDBConfigStore) CreateFolder(ctx context.Context, folder *tables.TableFolder) error {
	return s.db.WithContext(ctx).Create(folder).Error
}

// UpdateFolder updates a folder
func (s *RDBConfigStore) UpdateFolder(ctx context.Context, folder *tables.TableFolder) error {
	res := s.db.WithContext(ctx).Where("id = ?", folder.ID).Save(folder)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteFolder deletes a folder and all its child prompts (with their versions, sessions, and messages).
// PostgreSQL uses native ON DELETE CASCADE; SQLite requires manual cascade because it cannot
// alter foreign key constraints after table creation.
func (s *RDBConfigStore) DeleteFolder(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Check folder exists
		var folder tables.TableFolder
		if err := tx.First(&folder, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}

		// PostgreSQL: ON DELETE CASCADE handles all child deletions
		if s.db.Dialector.Name() == "postgres" {
			return tx.Delete(&folder).Error
		}

		// SQLite: manual cascade deletion
		var promptIDs []string
		if err := tx.Model(&tables.TablePrompt{}).Where("folder_id = ?", id).Pluck("id", &promptIDs).Error; err != nil {
			return err
		}

		if len(promptIDs) > 0 {
			// Delete version messages
			if err := tx.Where("prompt_id IN ?", promptIDs).Delete(&tables.TablePromptVersionMessage{}).Error; err != nil {
				return err
			}
			// Delete versions
			if err := tx.Where("prompt_id IN ?", promptIDs).Delete(&tables.TablePromptVersion{}).Error; err != nil {
				return err
			}
			// Delete session messages
			if err := tx.Where("prompt_id IN ?", promptIDs).Delete(&tables.TablePromptSessionMessage{}).Error; err != nil {
				return err
			}
			// Delete sessions
			if err := tx.Where("prompt_id IN ?", promptIDs).Delete(&tables.TablePromptSession{}).Error; err != nil {
				return err
			}
			// Delete prompts
			if err := tx.Where("folder_id = ?", id).Delete(&tables.TablePrompt{}).Error; err != nil {
				return err
			}
		}

		// Delete the folder
		return tx.Delete(&folder).Error
	})
}

// ============================================================================
// Prompt Repository - Prompts
// ============================================================================

// GetPrompts gets all prompts, optionally filtered by folder ID
func (s *RDBConfigStore) GetPrompts(ctx context.Context, folderID *string) ([]tables.TablePrompt, error) {
	var prompts []tables.TablePrompt
	query := s.db.WithContext(ctx).
		Preload("Folder").
		Order("created_at DESC")

	if folderID != nil {
		query = query.Where("folder_id = ?", *folderID)
	}

	if err := query.Find(&prompts).Error; err != nil {
		return nil, err
	}

	// Get latest version for each prompt
	for i := range prompts {
		var latestVersion tables.TablePromptVersion
		if err := s.db.WithContext(ctx).
			Preload("Messages", func(db *gorm.DB) *gorm.DB { return db.Order("order_index ASC") }).
			Where("prompt_id = ? AND is_latest = ?", prompts[i].ID, true).
			First(&latestVersion).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, err
			}
		} else {
			prompts[i].LatestVersion = &latestVersion
		}
	}

	return prompts, nil
}

// GetPromptByID gets a prompt by ID with latest version
func (s *RDBConfigStore) GetPromptByID(ctx context.Context, id string) (*tables.TablePrompt, error) {
	var prompt tables.TablePrompt
	if err := s.db.WithContext(ctx).
		Preload("Folder").
		First(&prompt, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	// Get latest version
	var latestVersion tables.TablePromptVersion
	if err := s.db.WithContext(ctx).
		Preload("Messages", func(db *gorm.DB) *gorm.DB { return db.Order("order_index ASC") }).
		Where("prompt_id = ? AND is_latest = ?", prompt.ID, true).
		First(&latestVersion).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	} else {
		prompt.LatestVersion = &latestVersion
	}

	return &prompt, nil
}

// CreatePrompt creates a new prompt
func (s *RDBConfigStore) CreatePrompt(ctx context.Context, prompt *tables.TablePrompt) error {
	return s.db.WithContext(ctx).Create(prompt).Error
}

// UpdatePrompt updates a prompt
func (s *RDBConfigStore) UpdatePrompt(ctx context.Context, prompt *tables.TablePrompt) error {
	// Use Select to explicitly include FolderID so GORM writes NULL when it's nil
	res := s.db.WithContext(ctx).
		Model(prompt).
		Where("id = ?", prompt.ID).
		Select("Name", "FolderID", "UpdatedAt").
		Updates(prompt)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePrompt deletes a prompt and all its child versions, sessions, and messages.
// PostgreSQL uses native ON DELETE CASCADE; SQLite requires manual cascade because it cannot
// alter foreign key constraints after table creation.
func (s *RDBConfigStore) DeletePrompt(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Check prompt exists
		var prompt tables.TablePrompt
		if err := tx.First(&prompt, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}

		// PostgreSQL: ON DELETE CASCADE handles all child deletions
		if s.db.Dialector.Name() == "postgres" {
			return tx.Delete(&prompt).Error
		}

		// SQLite: manual cascade deletion
		if err := tx.Where("prompt_id = ?", id).Delete(&tables.TablePromptVersionMessage{}).Error; err != nil {
			return err
		}
		if err := tx.Where("prompt_id = ?", id).Delete(&tables.TablePromptVersion{}).Error; err != nil {
			return err
		}
		if err := tx.Where("prompt_id = ?", id).Delete(&tables.TablePromptSessionMessage{}).Error; err != nil {
			return err
		}
		if err := tx.Where("prompt_id = ?", id).Delete(&tables.TablePromptSession{}).Error; err != nil {
			return err
		}
		return tx.Delete(&prompt).Error
	})
}

// ============================================================================
// Prompt Repository - Versions
// ============================================================================

// GetAllPromptVersions returns every version across all prompts in a single query.
func (s *RDBConfigStore) GetAllPromptVersions(ctx context.Context) ([]tables.TablePromptVersion, error) {
	var versions []tables.TablePromptVersion
	if err := s.db.WithContext(ctx).
		Preload("Messages", func(db *gorm.DB) *gorm.DB { return db.Order("order_index ASC") }).
		Order("prompt_id ASC, version_number DESC").
		Find(&versions).Error; err != nil {
		return nil, err
	}
	return versions, nil
}

// GetPromptVersions gets all versions for a prompt
func (s *RDBConfigStore) GetPromptVersions(ctx context.Context, promptID string) ([]tables.TablePromptVersion, error) {
	var versions []tables.TablePromptVersion
	if err := s.db.WithContext(ctx).
		Preload("Messages", func(db *gorm.DB) *gorm.DB { return db.Order("order_index ASC") }).
		Where("prompt_id = ?", promptID).
		Order("version_number DESC").
		Find(&versions).Error; err != nil {
		return nil, err
	}
	return versions, nil
}

// GetPromptVersionByID gets a version by ID
func (s *RDBConfigStore) GetPromptVersionByID(ctx context.Context, id uint) (*tables.TablePromptVersion, error) {
	var version tables.TablePromptVersion
	if err := s.db.WithContext(ctx).
		Preload("Messages", func(db *gorm.DB) *gorm.DB { return db.Order("order_index ASC") }).
		Preload("Prompt").
		First(&version, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &version, nil
}

// GetLatestPromptVersion gets the latest version for a prompt
func (s *RDBConfigStore) GetLatestPromptVersion(ctx context.Context, promptID string) (*tables.TablePromptVersion, error) {
	var version tables.TablePromptVersion
	if err := s.db.WithContext(ctx).
		Preload("Messages", func(db *gorm.DB) *gorm.DB { return db.Order("order_index ASC") }).
		Where("prompt_id = ? AND is_latest = ?", promptID, true).
		First(&version).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &version, nil
}

// CreatePromptVersion creates a new version and marks it as latest.
// Retries on unique constraint conflict (concurrent version_number allocation).
func (s *RDBConfigStore) CreatePromptVersion(ctx context.Context, version *tables.TablePromptVersion) error {
	const maxRetries = 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			// Get the next version number
			var maxVersionNumber int
			if err := tx.Model(&tables.TablePromptVersion{}).
				Where("prompt_id = ?", version.PromptID).
				Select("COALESCE(MAX(version_number), 0)").
				Scan(&maxVersionNumber).Error; err != nil {
				return err
			}
			version.VersionNumber = maxVersionNumber + 1

			// Mark all existing versions as not latest
			if err := tx.Model(&tables.TablePromptVersion{}).
				Where("prompt_id = ?", version.PromptID).
				Update("is_latest", false).Error; err != nil {
				return err
			}

			// Mark new version as latest
			version.IsLatest = true

			// Reset IDs and set order index on messages before create (GORM will auto-create associations)
			for i := range version.Messages {
				version.Messages[i].ID = 0
				version.Messages[i].PromptID = version.PromptID
				version.Messages[i].OrderIndex = i
			}

			// Create the version (GORM auto-creates associated messages)
			if err := tx.Create(version).Error; err != nil {
				return err
			}

			return nil
		})
		if err == nil {
			return nil
		}
		// Retry on unique constraint conflict, otherwise return immediately
		if !isUniqueConstraintError(err) {
			return err
		}
	}
	return fmt.Errorf("failed to create prompt version after %d retries due to concurrent version_number conflict", maxRetries)
}

// DeletePromptVersion deletes a version and promotes the previous version to latest if needed.
// PostgreSQL uses native ON DELETE CASCADE for messages; SQLite requires manual cascade.
func (s *RDBConfigStore) DeletePromptVersion(ctx context.Context, id uint) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Get the version to check if it's latest
		var version tables.TablePromptVersion
		if err := tx.First(&version, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}

		// SQLite: manually delete version messages (PostgreSQL CASCADE handles this)
		if s.db.Dialector.Name() != "postgres" {
			if err := tx.Where("version_id = ?", id).Delete(&tables.TablePromptVersionMessage{}).Error; err != nil {
				return err
			}
		}

		// Delete the version
		if err := tx.Delete(&tables.TablePromptVersion{}, "id = ?", id).Error; err != nil {
			return err
		}

		// If this was the latest version, mark the previous one as latest
		if version.IsLatest {
			var prevVersion tables.TablePromptVersion
			if err := tx.Where("prompt_id = ?", version.PromptID).
				Order("version_number DESC").
				First(&prevVersion).Error; err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
			} else {
				if err := tx.Model(&prevVersion).UpdateColumn("is_latest", true).Error; err != nil {
					return err
				}
			}
		}

		return nil
	})
}

// ============================================================================
// Prompt Repository - Sessions
// ============================================================================

// GetPromptSessions gets all sessions for a prompt
func (s *RDBConfigStore) GetPromptSessions(ctx context.Context, promptID string) ([]tables.TablePromptSession, error) {
	var sessions []tables.TablePromptSession
	if err := s.db.WithContext(ctx).
		Preload("Messages", func(db *gorm.DB) *gorm.DB { return db.Order("order_index ASC") }).
		Preload("Version").
		Where("prompt_id = ?", promptID).
		Order("created_at DESC").
		Find(&sessions).Error; err != nil {
		return nil, err
	}
	return sessions, nil
}

// GetPromptSessionByID gets a session by ID
func (s *RDBConfigStore) GetPromptSessionByID(ctx context.Context, id uint) (*tables.TablePromptSession, error) {
	var session tables.TablePromptSession
	if err := s.db.WithContext(ctx).
		Preload("Messages", func(db *gorm.DB) *gorm.DB { return db.Order("order_index ASC") }).
		Preload("Prompt").
		Preload("Version").
		First(&session, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &session, nil
}

// CreatePromptSession creates a new session
func (s *RDBConfigStore) CreatePromptSession(ctx context.Context, session *tables.TablePromptSession) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Verify version belongs to the same prompt if set
		if session.VersionID != nil {
			var version tables.TablePromptVersion
			if err := tx.First(&version, "id = ?", *session.VersionID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return fmt.Errorf("version not found")
				}
				return err
			}
			if version.PromptID != session.PromptID {
				return fmt.Errorf("version does not belong to the specified prompt")
			}
		}

		// Save messages and clear from session to prevent GORM auto-creating them
		msgs := session.Messages
		session.Messages = nil

		// Create the session without associated messages
		if err := tx.Create(session).Error; err != nil {
			return err
		}

		// Create messages with fresh IDs
		for i := range msgs {
			msgs[i].ID = 0 // Ensure new auto-increment ID
			msgs[i].PromptID = session.PromptID
			msgs[i].SessionID = session.ID
			msgs[i].OrderIndex = i
			if err := tx.Create(&msgs[i]).Error; err != nil {
				return err
			}
		}

		session.Messages = msgs
		return nil
	})
}

// UpdatePromptSession updates a session and its messages
func (s *RDBConfigStore) UpdatePromptSession(ctx context.Context, session *tables.TablePromptSession) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Verify version belongs to the same prompt if set
		if session.VersionID != nil {
			var version tables.TablePromptVersion
			if err := tx.First(&version, "id = ?", *session.VersionID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return fmt.Errorf("version not found")
				}
				return err
			}
			if version.PromptID != session.PromptID {
				return fmt.Errorf("version does not belong to the specified prompt")
			}
		}

		// Update the session
		res := tx.Where("id = ?", session.ID).Save(session)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}

		// Delete old messages
		if err := tx.Where("session_id = ?", session.ID).Delete(&tables.TablePromptSessionMessage{}).Error; err != nil {
			return err
		}

		// Create new messages
		for i := range session.Messages {
			session.Messages[i].PromptID = session.PromptID
			session.Messages[i].SessionID = session.ID
			session.Messages[i].OrderIndex = i
			session.Messages[i].ID = 0 // Reset ID for new creation
			if err := tx.Create(&session.Messages[i]).Error; err != nil {
				return err
			}
		}

		return nil
	})
}

// RenamePromptSession updates only the name of a session
func (s *RDBConfigStore) RenamePromptSession(ctx context.Context, id uint, name string) error {
	result := s.db.WithContext(ctx).Model(&tables.TablePromptSession{}).Where("id = ?", id).Update("name", name)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePromptSession deletes a session and its messages.
// PostgreSQL uses native ON DELETE CASCADE for messages; SQLite requires manual cascade.
func (s *RDBConfigStore) DeletePromptSession(ctx context.Context, id uint) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var session tables.TablePromptSession
		if err := tx.First(&session, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}

		// PostgreSQL: ON DELETE CASCADE handles message deletion
		if s.db.Dialector.Name() == "postgres" {
			return tx.Delete(&session).Error
		}

		// SQLite: manually delete messages first
		if err := tx.Where("session_id = ?", id).Delete(&tables.TablePromptSessionMessage{}).Error; err != nil {
			return err
		}

		return tx.Delete(&session).Error
	})
}
