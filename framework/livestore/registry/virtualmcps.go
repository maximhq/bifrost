package registry

import (
	"context"
	"strconv"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/queryscope"
	"gorm.io/gorm"
)

// VirtualMCP is the record of one virtual MCP definition: the row's columns, embedded, with its
// parsed tool specs. The keys it is assigned to
// carry its ID, so an assignment change refreshes the key.
type VirtualMCP struct {
	tables.TableVirtualMCP
}

// VirtualMCPID is a virtual MCP record's primary key: its row ID in decimal.
func VirtualMCPID(v *VirtualMCP) string { return NumericID(v.ID) }

// VirtualMCPKey is the primary key of the virtual MCP with row ID id.
func VirtualMCPKey(id uint) string { return NumericID(id) }

// VirtualMCPsPage reads up to limit virtual MCPs with row IDs after the cursor, in ID order, and
// returns the next cursor, "" after the last page.
func VirtualMCPsPage(ctx context.Context, db *gorm.DB, after string, limit int) ([]*VirtualMCP, string, error) {
	var from uint64
	if after != "" {
		var err error
		if from, err = strconv.ParseUint(after, 10, 64); err != nil {
			return nil, "", err
		}
	}
	rows, err := readVirtualMCPs(ctx, db, func(q *gorm.DB) *gorm.DB {
		return q.Where("id > ?", from).Order("id").Limit(limit)
	})
	if err != nil || len(rows) < limit {
		return rows, "", err
	}
	return rows, VirtualMCPID(rows[len(rows)-1]), nil
}

// VirtualMCPsByID reads the virtual MCPs with these IDs; an ID with no row, or one that is not a
// row ID, is left out.
func VirtualMCPsByID(ctx context.Context, db *gorm.DB, ids []string) ([]*VirtualMCP, error) {
	rowIDs := make([]uint, 0, len(ids))
	for _, id := range ids {
		if n, err := strconv.ParseUint(id, 10, strconv.IntSize); err == nil {
			rowIDs = append(rowIDs, uint(n))
		}
	}
	if len(rowIDs) == 0 {
		return nil, nil
	}
	return readVirtualMCPs(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where(queryscope.InUints(q, "id", rowIDs)) })
}

// readVirtualMCPs loads the virtual MCP rows where selects.
func readVirtualMCPs(ctx context.Context, db *gorm.DB, where func(*gorm.DB) *gorm.DB) ([]*VirtualMCP, error) {
	var rows []tables.TableVirtualMCP
	if err := where(db.WithContext(ctx)).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*VirtualMCP, 0, len(rows))
	for i := range rows {
		out = append(out, &VirtualMCP{TableVirtualMCP: rows[i]})
	}
	return out, nil
}
