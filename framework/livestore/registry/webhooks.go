package registry

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/queryscope"
	"gorm.io/gorm"
)

// WebhookEndpoint is the record of one saved webhook endpoint: the row's columns, embedded, with its
// secret decrypted and its headers and events decoded, as async job submissions and the delivery
// worker read it. Records are shared: readers never change them. An endpoint stored in a form this
// version cannot read (a secret that does not decrypt) fails every read of it with
// configstore.ErrConfigUnreadable naming it: the boot load and a refresh of the whole kind fail,
// and a refresh of it by ID fails and keeps its record.
type WebhookEndpoint struct {
	tables.TableWebhookEndpoint
}

// WebhookEndpointByName is the index of webhook endpoints by their unique name.
const WebhookEndpointByName = "name"

// WebhookEndpointID is a webhook endpoint record's primary key.
func WebhookEndpointID(e *WebhookEndpoint) string { return e.ID }

// WebhookEndpointName is a webhook endpoint's key in WebhookEndpointByName.
func WebhookEndpointName(e *WebhookEndpoint) string { return e.Name }

// WebhookEndpointsPage reads up to limit endpoints with IDs after the cursor, in ID order; one this
// version cannot read fails the read.
func WebhookEndpointsPage(ctx context.Context, db *gorm.DB, after string, limit int) ([]*WebhookEndpoint, string, error) {
	rows, err := readWebhookEndpoints(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where("id > ?", after).Order("id").Limit(limit) })
	if err != nil || len(rows) < limit {
		return rows, "", err
	}
	return rows, rows[len(rows)-1].ID, nil
}

// WebhookEndpointsByID reads the endpoints with these IDs; one with no row is left out, and one
// this version cannot read fails the read, as reading it from the config store does.
func WebhookEndpointsByID(ctx context.Context, db *gorm.DB, ids []string) ([]*WebhookEndpoint, error) {
	return readWebhookEndpoints(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where(queryscope.InStrings(q, "id", ids)) })
}

// readWebhookEndpoints reads the endpoints where selects, decoding each as loading it with hooks
// would. One that does not decode fails the read, naming it.
func readWebhookEndpoints(ctx context.Context, db *gorm.DB, where func(*gorm.DB) *gorm.DB) ([]*WebhookEndpoint, error) {
	var rows []tables.TableWebhookEndpoint
	if err := where(db.WithContext(ctx).Session(&gorm.Session{SkipHooks: true})).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*WebhookEndpoint, 0, len(rows))
	for i := range rows {
		row := rows[i]
		if err := row.AfterFind(nil); err != nil {
			return nil, fmt.Errorf("webhook endpoint %s: %w: %w", row.ID, configstore.ErrConfigUnreadable, err)
		}
		out = append(out, &WebhookEndpoint{TableWebhookEndpoint: row})
	}
	return out, nil
}
