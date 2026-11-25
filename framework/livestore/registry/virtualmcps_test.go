package registry

import (
	"context"
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

// TestVirtualMCPsPageWalksRowIDsInNumericOrder checks that pages follow the numeric row ID cursor,
// so row 10 comes after row 9 rather than after row 1 as it would in string order, and that a
// record keeps its parsed tool specs and slug.
func TestVirtualMCPsPageWalksRowIDsInNumericOrder(t *testing.T) {
	ctx := context.Background()
	db := newProviderDB(t, nil).DB()
	for i := 1; i <= 11; i++ {
		name := fmt.Sprintf("vmcp-%d", i)
		insert(t, db, &tables.TableVirtualMCP{
			Name: name, EndpointSlug: name, Enabled: true,
			ParsedTools: []tables.MCPToolSpec{{MCPClientID: "c1", ToolNames: []string{"search"}}},
		})
	}
	// Create leaves a false Enabled to the column default, so the disabled one is updated after.
	require.NoError(t, db.Model(&tables.TableVirtualMCP{}).Where("name = ?", "vmcp-10").UpdateColumn("enabled", false).Error)

	var ids []string
	after := ""
	for {
		rows, next, err := VirtualMCPsPage(ctx, db, after, 4)
		require.NoError(t, err)
		for _, v := range rows {
			ids = append(ids, VirtualMCPID(v))
		}
		if next == "" {
			break
		}
		after = next
	}
	want := make([]string, 0, 11)
	for i := uint(1); i <= 11; i++ {
		want = append(want, NumericID(i))
	}
	require.Equal(t, want, ids, "IDs are zero-padded, so they sort as the numbers do")

	_, _, err := VirtualMCPsPage(ctx, db, "not-a-number", 4)
	require.Error(t, err, "a cursor that is not a row ID is refused")

	rows, err := VirtualMCPsByID(ctx, db, []string{NumericID(10), "x", NumericID(99)})
	require.NoError(t, err)
	require.Len(t, rows, 1, "an ID with no row, or one that is not a row ID, is left out")
	require.Equal(t, NumericID(10), VirtualMCPID(rows[0]))
	require.Equal(t, "vmcp-10", rows[0].EndpointSlug)
	require.False(t, rows[0].Enabled)
	require.Equal(t, []tables.MCPToolSpec{{MCPClientID: "c1", ToolNames: []string{"search"}}}, rows[0].ParsedTools)

	rows, err = VirtualMCPsByID(ctx, db, []string{"x"})
	require.NoError(t, err)
	require.Empty(t, rows)
	require.Equal(t, NumericID(7), VirtualMCPKey(7))
}
