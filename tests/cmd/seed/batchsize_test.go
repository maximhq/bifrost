package seed

import (
	"sync"
	"testing"

	"github.com/maximhq/bifrost/framework/logstore"
	"gorm.io/gorm/schema"
)

// postgresMaxBindParams is the bind parameter limit of the Postgres extended protocol.
const postgresMaxBindParams = 65535

// TestDefaultBatchSize_FitsPostgresBindLimit fails when a default batch of log rows
// would exceed the Postgres bind limit. Each new log column raises the parameters per
// row, and at BatchSize 1000 the 540 seeded rows stopped fitting at 122 columns.
func TestDefaultBatchSize_FitsPostgresBindLimit(t *testing.T) {
	batchSize := DefaultOptions().BatchSize

	for name, model := range map[string]any{
		"logs":          &logstore.Log{},
		"mcp_tool_logs": &logstore.MCPToolLog{},
	} {
		parsed, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
		if err != nil {
			t.Fatalf("parse %s schema: %v", name, err)
		}
		columns := len(parsed.DBNames)
		if params := batchSize * columns; params > postgresMaxBindParams {
			t.Errorf("%s: batch of %d rows with %d columns binds %d parameters, over the limit of %d",
				name, batchSize, columns, params, postgresMaxBindParams)
		}
	}
}
