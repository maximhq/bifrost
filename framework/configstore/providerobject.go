package configstore

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm/clause"

	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// UpsertProviderObject records the creating virtual key of a provider object. The
// first record for an id wins: an object has one creator, and a later write for
// the same id (a retrieve seen after the create, a replayed response) must not
// hand it to someone else.
func (s *RDBConfigStore) UpsertProviderObject(ctx context.Context, object *tables.TableProviderObject) error {
	if object == nil || object.ID == "" {
		return fmt.Errorf("provider object and id are required")
	}
	if object.Kind == "" || object.Provider == "" || object.ObjectID == "" || object.VirtualKeyID == "" {
		return fmt.Errorf("provider object kind, provider, object id and virtual key id are required")
	}
	if object.CreatedAt.IsZero() {
		object.CreatedAt = time.Now().UTC()
	}
	return s.DB().WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoNothing: true,
	}).Create(object).Error
}

// GetProviderObjectsByIDs returns the provider objects among ids that exist, in no
// particular order. Ids with no row are simply absent from the result.
func (s *RDBConfigStore) GetProviderObjectsByIDs(ctx context.Context, ids []string) ([]*tables.TableProviderObject, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var objects []*tables.TableProviderObject
	if err := s.DB().WithContext(ctx).Where("id IN ?", ids).Find(&objects).Error; err != nil {
		return nil, err
	}
	return objects, nil
}

// DeleteProviderObject forgets a provider object's creator once the object itself
// is gone. Deleting an id with no row is not an error.
func (s *RDBConfigStore) DeleteProviderObject(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("provider object id is required")
	}
	return s.DB().WithContext(ctx).Where("id = ?", id).Delete(&tables.TableProviderObject{}).Error
}
