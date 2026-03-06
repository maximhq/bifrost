package configstore

import (
	"context"
	"errors"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// ============================================================================
// Prompt Repository - Folders
// ============================================================================

// GetFolders gets all folders
func (s *RDBConfigStore) GetFolders(ctx context.Context) ([]tables.TableFolder, error) {
	var folders []tables.TableFolder
	if err := s.db.WithContext(ctx).
		Order("created_at DESC").
		Find(&folders).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return []tables.TableFolder{}, nil
		}
		return nil, err
	}

	// Get prompts count for each folder
	for i := range folders {
		var count int64
		s.db.WithContext(ctx).Model(&tables.TablePrompt{}).Where("folder_id = ?", folders[i].ID).Count(&count)
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
	return s.db.WithContext(ctx).Where("id = ?", folder.ID).Save(folder).Error
}

// DeleteFolder deletes a folder and cascades to prompts
func (s *RDBConfigStore) DeleteFolder(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Get all prompts in folder
		var prompts []tables.TablePrompt
		if err := tx.Where("folder_id = ?", id).Find(&prompts).Error; err != nil {
			return err
		}

		// Delete all related entities for each prompt
		for _, prompt := range prompts {
			// Delete session messages
			if err := tx.Where("session_id IN (SELECT id FROM prompt_sessions WHERE prompt_id = ?)", prompt.ID).
				Delete(&tables.TablePromptSessionMessage{}).Error; err != nil {
				return err
			}
			// Delete sessions
			if err := tx.Where("prompt_id = ?", prompt.ID).Delete(&tables.TablePromptSession{}).Error; err != nil {
				return err
			}
			// Delete version messages
			if err := tx.Where("version_id IN (SELECT id FROM prompt_versions WHERE prompt_id = ?)", prompt.ID).
				Delete(&tables.TablePromptVersionMessage{}).Error; err != nil {
				return err
			}
			// Delete versions
			if err := tx.Where("prompt_id = ?", prompt.ID).Delete(&tables.TablePromptVersion{}).Error; err != nil {
				return err
			}
		}

		// Delete prompts
		if err := tx.Where("folder_id = ?", id).Delete(&tables.TablePrompt{}).Error; err != nil {
			return err
		}

		// Delete folder
		result := tx.Delete(&tables.TableFolder{}, "id = ?", id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
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
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return []tables.TablePrompt{}, nil
		}
		return nil, err
	}

	// Get latest version and versions count for each prompt
	for i := range prompts {
		var latestVersion tables.TablePromptVersion
		if err := s.db.WithContext(ctx).
			Preload("Messages").
			Where("prompt_id = ? AND is_latest = ?", prompts[i].ID, true).
			First(&latestVersion).Error; err == nil {
			prompts[i].LatestVersion = &latestVersion
		}

		var count int64
		s.db.WithContext(ctx).Model(&tables.TablePromptVersion{}).Where("prompt_id = ?", prompts[i].ID).Count(&count)
		prompts[i].VersionsCount = int(count)
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
		Preload("Messages").
		Where("prompt_id = ? AND is_latest = ?", prompt.ID, true).
		First(&latestVersion).Error; err == nil {
		prompt.LatestVersion = &latestVersion
	}

	// Get versions count
	var count int64
	s.db.WithContext(ctx).Model(&tables.TablePromptVersion{}).Where("prompt_id = ?", prompt.ID).Count(&count)
	prompt.VersionsCount = int(count)

	return &prompt, nil
}

// CreatePrompt creates a new prompt
func (s *RDBConfigStore) CreatePrompt(ctx context.Context, prompt *tables.TablePrompt) error {
	return s.db.WithContext(ctx).Create(prompt).Error
}

// UpdatePrompt updates a prompt
func (s *RDBConfigStore) UpdatePrompt(ctx context.Context, prompt *tables.TablePrompt) error {
	// Use Select to explicitly include FolderID so GORM writes NULL when it's nil
	return s.db.WithContext(ctx).
		Model(prompt).
		Where("id = ?", prompt.ID).
		Select("Name", "FolderID", "UpdatedAt").
		Updates(prompt).Error
}

// DeletePrompt deletes a prompt and cascades to versions and sessions
func (s *RDBConfigStore) DeletePrompt(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Delete session messages
		if err := tx.Where("session_id IN (SELECT id FROM prompt_sessions WHERE prompt_id = ?)", id).
			Delete(&tables.TablePromptSessionMessage{}).Error; err != nil {
			return err
		}
		// Delete sessions
		if err := tx.Where("prompt_id = ?", id).Delete(&tables.TablePromptSession{}).Error; err != nil {
			return err
		}
		// Delete version messages
		if err := tx.Where("version_id IN (SELECT id FROM prompt_versions WHERE prompt_id = ?)", id).
			Delete(&tables.TablePromptVersionMessage{}).Error; err != nil {
			return err
		}
		// Delete versions
		if err := tx.Where("prompt_id = ?", id).Delete(&tables.TablePromptVersion{}).Error; err != nil {
			return err
		}
		// Delete prompt
		result := tx.Delete(&tables.TablePrompt{}, "id = ?", id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ============================================================================
// Prompt Repository - Versions
// ============================================================================

// GetPromptVersions gets all versions for a prompt
func (s *RDBConfigStore) GetPromptVersions(ctx context.Context, promptID string) ([]tables.TablePromptVersion, error) {
	var versions []tables.TablePromptVersion
	if err := s.db.WithContext(ctx).
		Preload("Messages").
		Where("prompt_id = ?", promptID).
		Order("version_number DESC").
		Find(&versions).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return []tables.TablePromptVersion{}, nil
		}
		return nil, err
	}
	return versions, nil
}

// GetPromptVersionByID gets a version by ID
func (s *RDBConfigStore) GetPromptVersionByID(ctx context.Context, id uint) (*tables.TablePromptVersion, error) {
	var version tables.TablePromptVersion
	if err := s.db.WithContext(ctx).
		Preload("Messages").
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
		Preload("Messages").
		Where("prompt_id = ? AND is_latest = ?", promptID, true).
		First(&version).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &version, nil
}

// CreatePromptVersion creates a new version and marks it as latest
func (s *RDBConfigStore) CreatePromptVersion(ctx context.Context, version *tables.TablePromptVersion) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Get the next version number
		var maxVersionNumber int
		tx.Model(&tables.TablePromptVersion{}).
			Where("prompt_id = ?", version.PromptID).
			Select("COALESCE(MAX(version_number), 0)").
			Scan(&maxVersionNumber)
		version.VersionNumber = maxVersionNumber + 1

		// Mark all existing versions as not latest
		if err := tx.Model(&tables.TablePromptVersion{}).
			Where("prompt_id = ?", version.PromptID).
			Update("is_latest", false).Error; err != nil {
			return err
		}

		// Mark new version as latest
		version.IsLatest = true

		// Set order index on messages before create (GORM will auto-create associations)
		for i := range version.Messages {
			version.Messages[i].OrderIndex = i
		}

		// Create the version (GORM auto-creates associated messages)
		if err := tx.Create(version).Error; err != nil {
			return err
		}

		return nil
	})
}

// DeletePromptVersion deletes a version
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

		// Delete messages
		if err := tx.Where("version_id = ?", id).Delete(&tables.TablePromptVersionMessage{}).Error; err != nil {
			return err
		}

		// Delete version
		if err := tx.Delete(&tables.TablePromptVersion{}, "id = ?", id).Error; err != nil {
			return err
		}

		// If this was the latest version, mark the previous one as latest
		if version.IsLatest {
			var prevVersion tables.TablePromptVersion
			if err := tx.Where("prompt_id = ?", version.PromptID).
				Order("version_number DESC").
				First(&prevVersion).Error; err == nil {
				tx.Model(&prevVersion).Update("is_latest", true)
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
		Preload("Messages").
		Preload("Version").
		Where("prompt_id = ?", promptID).
		Order("updated_at DESC").
		Find(&sessions).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return []tables.TablePromptSession{}, nil
		}
		return nil, err
	}
	return sessions, nil
}

// GetPromptSessionByID gets a session by ID
func (s *RDBConfigStore) GetPromptSessionByID(ctx context.Context, id uint) (*tables.TablePromptSession, error) {
	var session tables.TablePromptSession
	if err := s.db.WithContext(ctx).
		Preload("Messages").
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
		// Update the session
		if err := tx.Where("id = ?", session.ID).Save(session).Error; err != nil {
			return err
		}

		// Delete old messages
		if err := tx.Where("session_id = ?", session.ID).Delete(&tables.TablePromptSessionMessage{}).Error; err != nil {
			return err
		}

		// Create new messages
		for i := range session.Messages {
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

// DeletePromptSession deletes a session
func (s *RDBConfigStore) DeletePromptSession(ctx context.Context, id uint) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Delete messages
		if err := tx.Where("session_id = ?", id).Delete(&tables.TablePromptSessionMessage{}).Error; err != nil {
			return err
		}

		// Delete session
		result := tx.Delete(&tables.TablePromptSession{}, "id = ?", id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
}
