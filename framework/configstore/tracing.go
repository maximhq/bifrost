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
