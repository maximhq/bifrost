package registry

import (
	"context"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/queryscope"
	"gorm.io/gorm"
)

// Index names of the MCP client collection, for GetBy.
const (
	MCPClientByName = "name"
	MCPClientBySlug = "slug"
)

// MCPClient is one configured MCP client, as its row stores it: what it connects with, its tool
// settings, and the discovery last written to the row.
type MCPClient struct {
	Config schemas.MCPClientConfig
}

// MCPClientID is an MCP client record's primary key.
func MCPClientID(c *MCPClient) string { return c.Config.ID }

// MCPClientName is an MCP client's key in the name index.
func MCPClientName(c *MCPClient) string { return c.Config.Name }

// MCPClientSlug is an MCP client's key in the endpoint slug index; a client without a slug is not
// indexed.
func MCPClientSlug(c *MCPClient) string { return c.Config.EndpointSlug }

// MCPClientsPage reads up to limit MCP clients with IDs after the cursor, in ID order, and returns
// the next cursor, "" after the last page.
func MCPClientsPage(ctx context.Context, db *gorm.DB, after string, limit int) ([]*MCPClient, string, error) {
	rows, err := readMCPClients(ctx, db, func(q *gorm.DB) *gorm.DB {
		return q.Where("client_id > ?", after).Order("client_id").Limit(limit)
	})
	if err != nil || len(rows) < limit {
		return rows, "", err
	}
	return rows, rows[len(rows)-1].Config.ID, nil
}

// MCPClientsByID reads the MCP clients with these IDs; an ID with no row is left out. The IDs bind
// as one argument, so a refresh of any number of them stays within the statement parameter limit.
func MCPClientsByID(ctx context.Context, db *gorm.DB, ids []string) ([]*MCPClient, error) {
	return readMCPClients(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where(queryscope.InStrings(q, "client_id", ids)) })
}

// readMCPClients loads the MCP client rows where selects.
func readMCPClients(ctx context.Context, db *gorm.DB, where func(*gorm.DB) *gorm.DB) ([]*MCPClient, error) {
	var rows []tables.TableMCPClient
	if err := where(db.WithContext(ctx)).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*MCPClient, 0, len(rows))
	for i := range rows {
		out = append(out, &MCPClient{Config: configstore.MCPClientConfigFromTable(&rows[i])})
	}
	return out, nil
}
