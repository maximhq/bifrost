package registry

import (
	"context"
	"fmt"
	"slices"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/queryscope"
	"gorm.io/gorm"
)

// ModelRouter is one saved model router, with its targets and routing, as the routing plugin
// serves it. The record is shared: readers never change it. A router stored in a form this version
// cannot read fails every read of it with configstore.ErrConfigUnreadable naming it: the boot load
// and a refresh of the whole kind fail, and a refresh of it by ID fails and keeps its record.
type ModelRouter struct {
	Router configstore.ModelRouter
}

// ModelRouterID is a model router record's primary key.
func ModelRouterID(r *ModelRouter) string { return r.Router.ID }

// ModelRoutersPage reads up to limit routers with IDs after the cursor, in ID order. A router this
// version cannot read fails the read.
func ModelRoutersPage(ctx context.Context, db *gorm.DB, after string, limit int) ([]*ModelRouter, string, error) {
	routers, unreadable, err := configstore.ReadModelRouters(ctx, db, func(q *gorm.DB) *gorm.DB {
		return q.Where("id > ?", after).Order("id").Limit(limit)
	})
	if err != nil {
		return nil, "", err
	}
	if err := firstUnreadableRouter(unreadable); err != nil {
		return nil, "", err
	}
	out := make([]*ModelRouter, 0, len(routers))
	for i := range routers {
		out = append(out, &ModelRouter{Router: routers[i]})
	}
	if len(out) < limit {
		return out, "", nil
	}
	return out, out[len(out)-1].Router.ID, nil
}

// ModelRoutersByID reads the routers with these IDs, as a refresh of them does; one with no row is
// left out. A router this version cannot read fails the read, so its record stays as it was.
func ModelRoutersByID(ctx context.Context, db *gorm.DB, ids []string) ([]*ModelRouter, error) {
	routers, unreadable, err := configstore.ReadModelRouters(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where(queryscope.InStrings(q, "id", ids)) })
	if err != nil {
		return nil, err
	}
	if err := firstUnreadableRouter(unreadable); err != nil {
		return nil, err
	}
	out := make([]*ModelRouter, 0, len(routers))
	for i := range routers {
		out = append(out, &ModelRouter{Router: routers[i]})
	}
	return out, nil
}

// firstUnreadableRouter returns the error for the unreadable router with the lowest ID, naming it,
// or nil when every router read.
func firstUnreadableRouter(unreadable map[string]error) error {
	if len(unreadable) == 0 {
		return nil
	}
	ids := make([]string, 0, len(unreadable))
	for id := range unreadable {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return fmt.Errorf("model router %q: %w", ids[0], unreadable[ids[0]])
}
