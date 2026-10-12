package configstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm/clause"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/queryscope"
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

// providerIDListInlineMax is the longest id list bound one parameter per id.
const providerIDListInlineMax = 1000

// GetProviderObjectsByIDs returns the provider objects among ids that exist, in no
// particular order. Ids with no row are simply absent from the result. A list page fans
// its ids out across every provider name sharing the API, so ids can outnumber the
// bind-parameter limit (SQLite 32,766, Postgres 65,535). A list longer than
// providerIDListInlineMax travels as one parameter (queryscope.InStrings); a short one,
// the common case, keeps the plain IN, which SQLite plans faster than a json_each scan.
func (s *RDBConfigStore) GetProviderObjectsByIDs(ctx context.Context, ids []string) ([]*tables.TableProviderObject, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	db := s.DB().WithContext(ctx)
	if len(ids) <= providerIDListInlineMax {
		db = db.Where("id IN ?", ids)
	} else {
		db = db.Where(queryscope.InStrings(db, "id", ids))
	}
	var objects []*tables.TableProviderObject
	if err := db.Find(&objects).Error; err != nil {
		return nil, err
	}
	return objects, nil
}

// DeleteProviderObjectsByObjectIDSuffix forgets the provider objects of kind on
// provider that virtualKeyID created and whose provider-side id ends in "/"+suffix:
// a full resource name such as "projects/<p>/locations/<l>/batchPredictionJobs/<n>"
// that a delete named by its last segment alone. A job number is scoped to a project
// and region, so another key's job of the same number in another project is not the
// deleter's to forget. The suffix matches whole, at a path boundary, with LIKE
// metacharacters taken literally.
func (s *RDBConfigStore) DeleteProviderObjectsByObjectIDSuffix(ctx context.Context, kind, provider, suffix, virtualKeyID string) error {
	if kind == "" || provider == "" || suffix == "" || virtualKeyID == "" {
		return fmt.Errorf("provider object kind, provider, id suffix and virtual key id are required")
	}
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(suffix)
	return s.DB().WithContext(ctx).
		Where(`kind = ? AND provider = ? AND virtual_key_id = ? AND object_id LIKE ? ESCAPE '\'`, kind, provider, virtualKeyID, "%/"+escaped).
		Delete(&tables.TableProviderObject{}).Error
}

// GetProviderObjectsByObjectIDSuffixes returns the provider objects of kind on provider
// whose provider-side id ends in "/"+one of suffixes, in no particular order: full
// resource names such as "projects/<p>/locations/<l>/cachedContents/<n>" addressed by
// their last segment alone. Each suffix matches whole, at a path boundary, with LIKE
// metacharacters taken literally; empty suffixes are ignored. Each suffix is its own LIKE
// term, so the suffixes are read suffixPatternChunkSize at a time.
func (s *RDBConfigStore) GetProviderObjectsByObjectIDSuffixes(ctx context.Context, kind, provider string, suffixes []string) ([]*tables.TableProviderObject, error) {
	patterns := suffixLikePatterns(suffixes)
	if kind == "" || provider == "" || len(patterns) == 0 {
		return nil, nil
	}
	var objects []*tables.TableProviderObject
	seen := make(map[string]struct{})
	for start := 0; start < len(patterns); start += suffixPatternChunkSize {
		chunk := patterns[start:min(start+suffixPatternChunkSize, len(patterns))]
		suffixWhere := s.DB()
		for _, pattern := range chunk {
			suffixWhere = suffixWhere.Or(`object_id LIKE ? ESCAPE '\'`, pattern)
		}
		var page []*tables.TableProviderObject
		if err := s.DB().WithContext(ctx).
			Where("kind = ? AND provider = ?", kind, provider).
			Where(suffixWhere).
			Find(&page).Error; err != nil {
			return nil, err
		}
		for _, object := range page {
			if _, dup := seen[object.ID]; dup {
				continue
			}
			seen[object.ID] = struct{}{}
			objects = append(objects, object)
		}
	}
	return objects, nil
}

// GetProviderObjectsByObjectID returns the provider objects of kind recorded for objectID under
// any provider name, in no particular order. An id carries its provider in the stable key, so a
// lookup by the stable key misses a row recorded under a provider name that is no longer
// configured; this one does not. The kind leads the composite index, so only rows of that kind
// are read.
func (s *RDBConfigStore) GetProviderObjectsByObjectID(ctx context.Context, kind, objectID string) ([]*tables.TableProviderObject, error) {
	if kind == "" || objectID == "" {
		return nil, nil
	}
	var objects []*tables.TableProviderObject
	if err := s.DB().WithContext(ctx).Where("kind = ? AND object_id = ?", kind, objectID).Find(&objects).Error; err != nil {
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
