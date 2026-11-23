package jobaccounting

import (
	"testing"
	"time"

	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
)

func TestEndpointJobSettlementTimestampIsStable(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	job := &cstables.TableProviderJob{CreatedAt: created}
	assert.Equal(t, created, settlementTimestamp(job), "a retried settlement reuses the same ClickHouse key")
}
