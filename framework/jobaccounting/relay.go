package jobaccounting

import (
	"context"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/relay"
	"github.com/maximhq/bifrost/core/schemas"
	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
)

// ProviderJobKindRelay is an async job a relay route started.
const ProviderJobKindRelay ProviderJobKind = cstables.ProviderJobKindRelay

// Reasons a relay job ends unpriced.
const (
	UnpriceableReasonRelayJobParams  = "endpoint_job_params"
	UnpriceableReasonRelayJobTimeout = "endpoint_job_timeout"
	UnpriceableReasonRelayJobGone    = "endpoint_job_gone"
	UnpriceableReasonRelayJobDenied  = "endpoint_job_access_denied"
	UnpriceableReasonRelayJobRefused = "endpoint_job_rejected"
	UnpriceableReasonRelayJobNoUsage = "endpoint_job_no_usage"
)

// endpointJobLogNamespace is the UUIDv5 namespace for relay settlement row ids.
var endpointJobLogNamespace = uuid.MustParse("6f1c2c58-3a3e-4a59-9a77-0b8f6e0d4b21")

// RelayJobPoller sends a job poll with the endpoint's credential. *relay.Manager
// satisfies it.
type RelayJobPoller interface {
	PollJob(ctx *schemas.BifrostContext, endpointName string, poll relay.JobPollRequest) (*relay.JobPollResponse, *schemas.BifrostError)
}

// RelayJobLogStore writes the settlement row and reads it back. logstore.LogStore satisfies it.
type RelayJobLogStore interface {
	BatchCreateRelayLogsIfNotExists(ctx context.Context, entries []*logstore.RelayLog) ([]string, error)
	FindRelayLog(ctx context.Context, id string) (*logstore.RelayLog, error)
}

// RelayUsageReport is a settled job's charge, for governance. It carries no request count: the
// start call already counted against the request rate limits.
type RelayUsageReport struct {
	RequestID    string // the settlement row id; reporters must be idempotent on it
	RelayName    string
	Cost         float64
	BudgetIDs    []string
	RateLimitIDs []string
	VirtualKeyID string
	UserID       string
}

// RelayUsageReporter charges a settled job's cost to the budgets the start call was subject to.
type RelayUsageReporter interface {
	ReportRelayUsage(ctx context.Context, report RelayUsageReport) error
}

// settlementTimestamp is stable across retries: ClickHouse keys rows by (timestamp, id), so a retry
// writing a fresh timestamp would add a second copy of the same settlement.
func settlementTimestamp(job *cstables.TableProviderJob) time.Time {
	if job.CreatedAt.IsZero() {
		return time.Now().UTC()
	}
	return job.CreatedAt.UTC()
}

func decodeIDs(raw *string) []string {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil
	}
	var ids []string
	if err := sonic.Unmarshal([]byte(*raw), &ids); err != nil {
		return nil
	}
	return ids
}

func deref64(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
