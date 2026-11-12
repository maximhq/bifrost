package configstore

import (
	"context"
	"errors"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// ErrTraceProjectExists is returned when creating or renaming a project onto a taken name.
var ErrTraceProjectExists = errors.New("trace project already exists")

// ListTraceProjects loads every tracing project, by name.
func (s *RDBConfigStore) ListTraceProjects(ctx context.Context) ([]*tables.TableTraceProject, error) {
	var projects []*tables.TableTraceProject
	if err := s.DB().WithContext(ctx).Order("name ASC").Find(&projects).Error; err != nil {
		return nil, err
	}
	return projects, nil
}

// GetTraceProject loads one project by id, ErrNotFound when it does not exist.
func (s *RDBConfigStore) GetTraceProject(ctx context.Context, id string) (*tables.TableTraceProject, error) {
	return s.firstTraceProject(ctx, "id = ?", id)
}

// GetTraceProjectByName loads one project by name, ErrNotFound when it does not exist.
func (s *RDBConfigStore) GetTraceProjectByName(ctx context.Context, name string) (*tables.TableTraceProject, error) {
	return s.firstTraceProject(ctx, "name = ?", name)
}

func (s *RDBConfigStore) firstTraceProject(ctx context.Context, where string, arg string) (*tables.TableTraceProject, error) {
	var project tables.TableTraceProject
	if err := s.DB().WithContext(ctx).First(&project, where, arg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &project, nil
}

// CreateTraceProject stores a new project, ErrTraceProjectExists when the name is taken.
func (s *RDBConfigStore) CreateTraceProject(ctx context.Context, project *tables.TableTraceProject) error {
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := traceProjectNameFree(tx, project.Name, ""); err != nil {
			return err
		}
		now := time.Now().UTC()
		project.CreatedAt, project.UpdatedAt = now, now
		return tx.Create(project).Error
	})
}

// UpdateTraceProject rewrites a project's settings, ErrNotFound when it does not exist.
func (s *RDBConfigStore) UpdateTraceProject(ctx context.Context, project *tables.TableTraceProject) error {
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing tables.TableTraceProject
		if err := tx.First(&existing, "id = ?", project.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if err := traceProjectNameFree(tx, project.Name, project.ID); err != nil {
			return err
		}
		project.CreatedAt, project.UpdatedAt = existing.CreatedAt, time.Now().UTC()
		// Save writes cleared fields too: a nil retention must overwrite a stored one.
		return tx.Save(project).Error
	})
}

func traceProjectNameFree(tx *gorm.DB, name, exceptID string) error {
	var count int64
	if err := tx.Model(&tables.TableTraceProject{}).Where("name = ? AND id <> ?", name, exceptID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrTraceProjectExists
	}
	return nil
}

// DeleteTraceProject removes a project's settings, ErrNotFound when it does not exist. Its traces
// live in the log store and are deleted by the caller.
func (s *RDBConfigStore) DeleteTraceProject(ctx context.Context, id string) error {
	result := s.DB().WithContext(ctx).Where("id = ?", id).Delete(&tables.TableTraceProject{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ListTracingKeys loads every tracing key, oldest first.
func (s *RDBConfigStore) ListTracingKeys(ctx context.Context) ([]*tables.TableTracingKey, error) {
	var keys []*tables.TableTracingKey
	if err := s.DB().WithContext(ctx).Order("created_at ASC, id ASC").Find(&keys).Error; err != nil {
		return nil, err
	}
	return keys, nil
}

// GetTracingKey loads one tracing key by id, ErrNotFound when it does not exist.
func (s *RDBConfigStore) GetTracingKey(ctx context.Context, id string) (*tables.TableTracingKey, error) {
	return s.firstTracingKey(ctx, "id = ?", id)
}

// GetTracingKeyByHash loads the tracing key with the given hash, ErrNotFound when none has it.
func (s *RDBConfigStore) GetTracingKeyByHash(ctx context.Context, hash string) (*tables.TableTracingKey, error) {
	return s.firstTracingKey(ctx, "key_hash = ?", hash)
}

func (s *RDBConfigStore) firstTracingKey(ctx context.Context, where string, arg string) (*tables.TableTracingKey, error) {
	var key tables.TableTracingKey
	if err := s.DB().WithContext(ctx).First(&key, where, arg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &key, nil
}

// CreateTracingKey stores a new tracing key. The caller has already set its hash with SetValue.
func (s *RDBConfigStore) CreateTracingKey(ctx context.Context, key *tables.TableTracingKey) error {
	if key.KeyHash == "" {
		return errors.New("tracing key has no value")
	}
	now := time.Now().UTC()
	key.CreatedAt, key.UpdatedAt = now, now
	return s.DB().WithContext(ctx).Create(key).Error
}

// UpdateTracingKey rewrites a tracing key's settings, ErrNotFound when it does not exist.
func (s *RDBConfigStore) UpdateTracingKey(ctx context.Context, key *tables.TableTracingKey) error {
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing tables.TableTracingKey
		if err := tx.First(&existing, "id = ?", key.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		// The key's value never changes on an update: rotation creates a new key.
		key.KeyHash, key.KeyPrefix = existing.KeyHash, existing.KeyPrefix
		key.CreatedAt, key.UpdatedAt = existing.CreatedAt, time.Now().UTC()
		return tx.Save(key).Error
	})
}

// TouchTracingKey records that a key was just used.
func (s *RDBConfigStore) TouchTracingKey(ctx context.Context, id string, at time.Time) error {
	return s.DB().WithContext(ctx).Model(&tables.TableTracingKey{}).Where("id = ?", id).UpdateColumn("last_used_at", at).Error
}

// DeleteTracingKey removes a tracing key, ErrNotFound when it does not exist.
func (s *RDBConfigStore) DeleteTracingKey(ctx context.Context, id string) error {
	result := s.DB().WithContext(ctx).Where("id = ?", id).Delete(&tables.TableTracingKey{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
