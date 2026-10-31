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
	if err := s.DB().WithContext(ctx).
		Order("created_at DESC").
		Find(&folders).Error; err != nil {
		return nil, err
	}

	// Get prompts count for each folder
	for i := range folders {
		var count int64
		if err := s.DB().WithContext(ctx).Model(&tables.TablePrompt{}).Where("folder_id = ?", folders[i].ID).Count(&count).Error; err != nil {
			return nil, err
		}
		folders[i].PromptsCount = int(count)
	}

	return folders, nil
}

// GetFolderByID gets a folder by ID
func (s *RDBConfigStore) GetFolderByID(ctx context.Context, id string) (*tables.TableFolder, error) {
	var folder tables.TableFolder
	if err := s.DB().WithContext(ctx).
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
	return s.DB().WithContext(ctx).Create(folder).Error
}

// UpdateFolder updates a folder
func (s *RDBConfigStore) UpdateFolder(ctx context.Context, folder *tables.TableFolder) error {
	res := s.DB().WithContext(ctx).Where("id = ?", folder.ID).Save(folder)
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
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Check folder exists
		var folder tables.TableFolder
		if err := tx.First(&folder, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}

		// PostgreSQL: ON DELETE CASCADE handles all child deletions
		if s.DB().Dialector.Name() == "postgres" {
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

// GetPrompts gets all prompts, optionally filtered by folder ID.
//
// When ctx carries a QueryScope, the query is narrowed to prompts the
// caller is allowed to see.
func (s *RDBConfigStore) GetPrompts(ctx context.Context, folderID *string) ([]tables.TablePrompt, error) {
	var prompts []tables.TablePrompt
	query := s.ScopedDB(ctx).
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
		if err := s.DB().WithContext(ctx).
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

// GetPromptByID gets a prompt by ID with latest version.
//
// When ctx carries a QueryScope, a prompt that exists but falls
// outside the scope returns ErrNotFound so URL guessing cannot
// distinguish "hidden" from "absent".
func (s *RDBConfigStore) GetPromptByID(ctx context.Context, id string) (*tables.TablePrompt, error) {
	var prompt tables.TablePrompt
	q := s.ScopedDB(ctx).Preload("Folder")
	if err := q.First(&prompt, "prompts.id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	// Get latest version
	var latestVersion tables.TablePromptVersion
	if err := s.DB().WithContext(ctx).
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

// CreatePrompt creates a new prompt. The optional tx allows callers to
// chain the insert with follow-up writes in a single transaction (used
// by the enterprise wrapper to atomically stamp ownership columns).
func (s *RDBConfigStore) CreatePrompt(ctx context.Context, prompt *tables.TablePrompt, tx ...*gorm.DB) error {
	db := s.DB()
	if len(tx) > 0 && tx[0] != nil {
		db = tx[0]
	}
	return db.WithContext(ctx).Create(prompt).Error
}

// UpdatePrompt updates a prompt
func (s *RDBConfigStore) UpdatePrompt(ctx context.Context, prompt *tables.TablePrompt) error {
	if err := s.assertPromptInScope(ctx, prompt.ID); err != nil {
		return err
	}
	// Use Select to explicitly include FolderID so GORM writes NULL when it's nil
	res := s.DB().WithContext(ctx).
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
	// Gated before the transaction: deleting a prompt the caller cannot see is a worse
	// outcome than reading one, and the handler for this route resolves no parent first.
	if err := s.assertPromptInScope(ctx, id); err != nil {
		return err
	}
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Check prompt exists
		var prompt tables.TablePrompt
		if err := tx.First(&prompt, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}

		// PostgreSQL: ON DELETE CASCADE handles all child deletions
		if s.DB().Dialector.Name() == "postgres" {
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

// assertPromptInScope reports whether promptID names a prompt the caller may see, returning
// ErrNotFound when it does not - the same answer GetPromptByID gives, so a caller probing child
// collections cannot distinguish "hidden" from "absent" any more than it can on the parent.
//
// Child rows carry no scope columns of their own, and a QueryScope is written against the
// prompts table (GetPromptByID's own query is table-qualified, "prompts.id = ?"), so ScopedDB
// cannot be applied to a prompt_versions or prompt_sessions query directly - it would reference
// columns that query does not select. Reaching the parent through the scope and gating on the
// answer is what makes the child reads honour it, and it mirrors what the version-creation path
// already does before inserting a child.
//
// Note this deliberately re-reads the parent per call rather than trusting a scope filter to
// have been applied upstream: attaching a scope to a context does not retroactively restrict a
// query built from the unscoped handle, which is exactly how these reads came to leak.
func (s *RDBConfigStore) assertPromptInScope(ctx context.Context, promptID string) error {
	if promptID == "" {
		return ErrNotFound
	}
	// Existence is all that is needed, and a scope is a WHERE predicate on prompts columns,
	// which SQL evaluates independently of the SELECT list, so the projection can be narrowed
	// to the key without affecting what any scope can filter on.
	var prompt tables.TablePrompt
	if err := s.ScopedDB(ctx).Select("prompts.id").First(&prompt, "prompts.id = ?", promptID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// GetAllPromptVersions returns every version across all prompts in a single query.
//
// Deliberately unscoped: its only caller is the prompts plugin, which loads every version at
// startup to serve prompt injection for the whole deployment and runs on a background context
// that carries no scope. It is not reachable from any HTTP route, so no caller-supplied
// identifier reaches it. Do not wire it to a request-scoped path without adding a scope filter.
func (s *RDBConfigStore) GetAllPromptVersions(ctx context.Context) ([]tables.TablePromptVersion, error) {
	var versions []tables.TablePromptVersion
	if err := s.DB().WithContext(ctx).
		Preload("Messages", func(db *gorm.DB) *gorm.DB { return db.Order("order_index ASC") }).
		Order("prompt_id ASC, version_number DESC").
		Find(&versions).Error; err != nil {
		return nil, err
	}
	return versions, nil
}

// GetPromptVersions gets all versions for a prompt.
//
// Returns ErrNotFound when the parent prompt is outside the caller's scope, matching
// GetPromptByID rather than returning the versions of a prompt the caller cannot see.
func (s *RDBConfigStore) GetPromptVersions(ctx context.Context, promptID string) ([]tables.TablePromptVersion, error) {
	if err := s.assertPromptInScope(ctx, promptID); err != nil {
		return nil, err
	}
	var versions []tables.TablePromptVersion
	if err := s.DB().WithContext(ctx).
		Preload("Messages", func(db *gorm.DB) *gorm.DB { return db.Order("order_index ASC") }).
		Where("prompt_id = ?", promptID).
		Order("version_number DESC").
		Find(&versions).Error; err != nil {
		return nil, err
	}
	return versions, nil
}

// GetPromptVersionByID gets a version by ID.
//
// The row is fetched first because the version's own ID is all the caller supplies - which
// prompt it belongs to is only known once it is read - and the parent is then checked. A version
// whose prompt is outside the caller's scope reports ErrNotFound, the same answer an absent ID
// gets, so nothing distinguishes the two.
func (s *RDBConfigStore) GetPromptVersionByID(ctx context.Context, id uint) (*tables.TablePromptVersion, error) {
	var version tables.TablePromptVersion
	if err := s.DB().WithContext(ctx).
		Preload("Messages", func(db *gorm.DB) *gorm.DB { return db.Order("order_index ASC") }).
		Preload("Prompt").
		First(&version, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := s.assertPromptInScope(ctx, version.PromptID); err != nil {
		return nil, err
	}
	return &version, nil
}

// GetLatestPromptVersion gets the latest version for a prompt.
//
// Scoped on the parent for the same reason as GetPromptVersions.
func (s *RDBConfigStore) GetLatestPromptVersion(ctx context.Context, promptID string) (*tables.TablePromptVersion, error) {
	if err := s.assertPromptInScope(ctx, promptID); err != nil {
		return nil, err
	}
	var version tables.TablePromptVersion
	if err := s.DB().WithContext(ctx).
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
//
// The parent is checked through the scope before the transaction opens, for the reason given
// on DeletePromptVersion: creating a child is a write against the parent, and a scoped caller
// must not be able to attach one to a prompt it cannot see, however the handler reached here.
func (s *RDBConfigStore) CreatePromptVersion(ctx context.Context, version *tables.TablePromptVersion) error {
	if err := s.assertPromptInScope(ctx, version.PromptID); err != nil {
		return err
	}
	const maxRetries = 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		err := s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
	// Resolved and checked before the transaction opens, not inside it: assertPromptInScope
	// queries through s.DB(), a different handle from tx, so asking inside the transaction
	// reads outside its snapshot - and on SQLite does not see the transaction's database at
	// all. The parent is only knowable once the child is read, hence the extra lookup.
	var version tables.TablePromptVersion
	if err := s.DB().WithContext(ctx).Select("prompt_id").First(&version, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	if err := s.assertPromptInScope(ctx, version.PromptID); err != nil {
		return err
	}
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Get the version to check if it's latest
		var version tables.TablePromptVersion
		if err := tx.First(&version, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}

		// SQLite: manually delete version messages (PostgreSQL CASCADE handles this)
		if s.DB().Dialector.Name() != "postgres" {
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

// GetPromptSessions gets all sessions for a prompt.
//
// Scoped on the parent for the same reason as GetPromptVersions. Sessions carry the prompt's
// message contents, so they leak the same data the versions do.
func (s *RDBConfigStore) GetPromptSessions(ctx context.Context, promptID string) ([]tables.TablePromptSession, error) {
	if err := s.assertPromptInScope(ctx, promptID); err != nil {
		return nil, err
	}
	var sessions []tables.TablePromptSession
	if err := s.DB().WithContext(ctx).
		Preload("Messages", func(db *gorm.DB) *gorm.DB { return db.Order("order_index ASC") }).
		Preload("Version").
		Where("prompt_id = ?", promptID).
		Order("created_at DESC").
		Find(&sessions).Error; err != nil {
		return nil, err
	}
	return sessions, nil
}

// GetPromptSessionByID gets a session by ID.
//
// Fetched then checked against its parent, for the same reason as GetPromptVersionByID. This is
// the widest-reaching of these reads: four handlers resolve a session from a caller-supplied ID,
// including the ones that then update or delete it, so the check belongs here rather than at any
// one of them.
func (s *RDBConfigStore) GetPromptSessionByID(ctx context.Context, id uint) (*tables.TablePromptSession, error) {
	var session tables.TablePromptSession
	if err := s.DB().WithContext(ctx).
		Preload("Messages", func(db *gorm.DB) *gorm.DB { return db.Order("order_index ASC") }).
		Preload("Prompt").
		Preload("Version").
		First(&session, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := s.assertPromptInScope(ctx, session.PromptID); err != nil {
		return nil, err
	}
	return &session, nil
}

// CreatePromptSession creates a new session.
//
// The parent is scoped before the transaction opens, as on CreatePromptVersion.
func (s *RDBConfigStore) CreatePromptSession(ctx context.Context, session *tables.TablePromptSession) error {
	if err := s.assertPromptInScope(ctx, session.PromptID); err != nil {
		return err
	}
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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

// UpdatePromptSession updates a session and its messages.
//
// The parent it scopes is the one already stored under session.ID, not the one the caller
// supplies: the update carries the whole row, so trusting session.PromptID would let a caller
// who can see one prompt pair it with a hidden session's ID, pass the scope check on the
// visible parent, and have Save overwrite the hidden session and re-parent it. A session cannot
// be moved between prompts through an update at all, so a stored parent that differs from the
// supplied one is rejected outright. Its HTTP handler resolves the session through the scoped
// GetPromptSessionByID first, so as with RenamePromptSession this is defence in depth, but the
// store must not depend on a caller having looked the parent up in the right order.
func (s *RDBConfigStore) UpdatePromptSession(ctx context.Context, session *tables.TablePromptSession) error {
	var stored tables.TablePromptSession
	if err := s.DB().WithContext(ctx).Select("prompt_id").First(&stored, "id = ?", session.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	if err := s.assertPromptInScope(ctx, stored.PromptID); err != nil {
		return err
	}
	if stored.PromptID != session.PromptID {
		return fmt.Errorf("session does not belong to the specified prompt")
	}
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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

// RenamePromptSession updates only the name of a session.
//
// Its own handler resolves the session through GetPromptSessionByID first, which is scoped, so
// this is defence in depth rather than the only gate - but the store should not depend on a
// caller having looked the parent up in the right order.
func (s *RDBConfigStore) RenamePromptSession(ctx context.Context, id uint, name string) error {
	var session tables.TablePromptSession
	if err := s.DB().WithContext(ctx).First(&session, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	if err := s.assertPromptInScope(ctx, session.PromptID); err != nil {
		return err
	}
	result := s.DB().WithContext(ctx).Model(&tables.TablePromptSession{}).Where("id = ?", id).Update("name", name)
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
	// Checked before the transaction, for the reason given on DeletePromptVersion.
	var owner tables.TablePromptSession
	if err := s.DB().WithContext(ctx).Select("prompt_id").First(&owner, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	if err := s.assertPromptInScope(ctx, owner.PromptID); err != nil {
		return err
	}
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var session tables.TablePromptSession
		if err := tx.First(&session, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}

		// PostgreSQL: ON DELETE CASCADE handles message deletion
		if s.DB().Dialector.Name() == "postgres" {
			return tx.Delete(&session).Error
		}

		// SQLite: manually delete messages first
		if err := tx.Where("session_id = ?", id).Delete(&tables.TablePromptSessionMessage{}).Error; err != nil {
			return err
		}

		return tx.Delete(&session).Error
	})
}
