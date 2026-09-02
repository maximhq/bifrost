package jobaccounting

import (
	"context"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

const defaultClaimTTL = 5 * time.Minute

// ProviderJobKind discriminates the job families sharing this engine. It is
// deliberately not named "async job": logstore.AsyncJobExecutor and the /v1/async/*
// routes already own that term for Bifrost's own fire-and-forget request queue,
// which is a different thing entirely — these are jobs the *provider* runs.
type ProviderJobKind string

const ProviderJobKindBatch ProviderJobKind = cstables.ProviderJobKindBatch

// JobStore is the mutable coordination-state store for delayed settlement. It is
// satisfied by configstore.ConfigStore.
type JobStore interface {
	UpsertProviderJob(ctx context.Context, job *cstables.TableProviderJob) error
	GetProviderJob(ctx context.Context, jobID string) (*cstables.TableProviderJob, error)
	ClaimProviderJob(ctx context.Context, jobID, runnerID string, staleBefore time.Time, allowUnpriceable bool) (bool, error)
	MarkProviderJobAggregateLogWritten(ctx context.Context, jobID, runnerID string) error
	MarkProviderJobGovernanceReported(ctx context.Context, jobID, runnerID string) error
	CompleteProviderJob(ctx context.Context, jobID, runnerID string) error
	MarkProviderJobUnpriceable(ctx context.Context, jobID, runnerID, reason string, err error) error
	FailProviderJob(ctx context.Context, jobID, runnerID string, err error) error
}

// SweepStore adds the due-job scan the sweeper needs. The scan is per kind: a
// sweeper must never be handed a kind its settler cannot talk to.
type SweepStore interface {
	JobStore
	ListDueProviderJobs(ctx context.Context, kind, provider string, now time.Time, limit int) ([]*cstables.TableProviderJob, error)
}

// AggregateLogStore writes the append-only aggregate cost record. It is satisfied
// by logstore.LogStore — the cost record lives in the log store next to every other
// request cost row.
type AggregateLogStore interface {
	CreateIfNotExists(ctx context.Context, entry *logstore.Log) error
	// FindByID reads the aggregate cost row back, keyed by the same deterministic
	// id CreateIfNotExists wrote it under. Used only to mirror an already-settled
	// job's price onto a caller that did not win this call's settlement claim —
	// never to drive a write.
	FindByID(ctx context.Context, id string) (*logstore.Log, error)
}

type AggregateLogEmitter interface {
	EmitAggregateLog(ctx context.Context, entry *logstore.Log)
}

// UsageReporter receives the settled usage/cost for a job so it can be billed.
//
// Implementations MUST be idempotent on UsageReport.RequestID: settlement is
// at-least-once, so the same report can be delivered more than once when the
// durable "reported" marker fails to persist after a successful report. See the
// package doc for the exact window and its limits.
type UsageReporter interface {
	ReportUsage(ctx context.Context, usage UsageReport) error
}

type UsageReport struct {
	RequestID    string
	Provider     schemas.ModelProvider
	Model        string
	Cost         float64
	TokensUsed   int64
	BudgetIDs    []string
	RateLimitIDs []string
	UserID       string
	VirtualKeyID string
	ModelUsage   []ModelUsage
}

// ModelUsage is one model's share of a settled job.
type ModelUsage struct {
	Model      string
	Cost       float64
	TokensUsed int64
}

type PricingManager interface {
	CalculateBatchCostDetailsForUsage(usage *schemas.BifrostLLMUsage, provider schemas.ModelProvider, model string, requestType schemas.RequestType, scopes *modelcatalog.PricingLookupScopes) modelcatalog.BatchCostDetails
}

// Settler supplies everything about a job kind that the engine does not know: how
// to poll the provider, how to turn a terminal job into a price, and how that
// kind's detail rides on the aggregate log row.
type Settler interface {
	Kind() ProviderJobKind

	// SupportsProvider reports whether this kind can settle jobs for a provider.
	// The sweeper skips jobs it cannot price rather than burning poll attempts.
	SupportsProvider(provider schemas.ModelProvider) bool

	// Poll fetches current provider state. Returning an error asks the engine to
	// reschedule; see PollResult for the non-error outcomes.
	Poll(ctx context.Context, job *cstables.TableProviderJob) (*PollResult, error)

	// Settle prices a job the poll (or an inline caller) says is ready.
	Settle(ctx context.Context, pricing PricingManager, req JobRequest) (*Settlement, error)

	// Backoff returns the delay before the next poll attempt, before jitter. Kinds
	// differ by orders of magnitude here: a provider batch runs for hours, a video
	// job finishes in minutes.
	Backoff(attempts int, interval time.Duration) time.Duration

	// HydrateFromLog reads this kind's detail back off an already-written aggregate
	// row, for a caller that lost the settlement claim and only wants to display a
	// price. The inverse of Settlement.ApplyDebug.
	HydrateFromLog(entry *logstore.Log, out *Outcome)
}

// PollResult is one poll's outcome. Terminal, Settleable and Retry are separate
// because "the provider is done" and "we can price it" are different facts, and a
// poll can succeed while its settlement inputs are still unavailable.
type PollResult struct {
	// Job is the updated coordination row to persist. Nil leaves the row untouched.
	// It is persisted before any Retry/Terminal decision, so a poll that advanced
	// the provider status records that even when settlement cannot proceed.
	Job *cstables.TableProviderJob
	// Terminal reports that the provider will not advance this job further.
	Terminal bool
	// Settleable reports that the payload below is sufficient to price the job.
	Settleable bool
	// Retry asks for another attempt later even though the poll itself succeeded —
	// e.g. a live job whose results could not be downloaded this time.
	Retry bool
	// UnpriceableReason names why a terminal job cannot be settled.
	UnpriceableReason string
	// Payload is the kind's settlement input, handed back to Settle.
	Payload any
}

// Settlement is a kind's verdict on one job.
type Settlement struct {
	// Priced reports that this settlement carries a final number. A terminal job
	// that owes nothing sets Priced with Cost 0 — that is a real price, not the
	// absence of one, and it must not be confused with the unpriceable path (which
	// stays re-drivable so a later repricing can recover it).
	Priced bool
	// Complete reports that every unit of work priced. When false, Cost is only the
	// known part: the engine writes the row with an unknown cost and parks the job
	// rather than reporting a total it knows is short.
	Complete bool

	Cost            float64
	Usage           schemas.BifrostLLMUsage
	ModelBreakdowns map[string]schemas.BatchModelBreakdown

	// Model labels the aggregate row. Object is the request type recorded on it.
	Model  string
	Object schemas.RequestType

	// ApplyDebug attaches this kind's typed detail to the aggregate row. Keeping it
	// a closure lets logstore.Log stay fully typed while the engine stays generic.
	ApplyDebug func(entry *logstore.Log)

	// RecordUnpriced asks the engine to still write an unpriced row (cost NULL) when
	// Priced is false, so the usage stays visible and backfillable instead of being
	// dropped. Ignored when Priced is true.
	RecordUnpriced    bool
	UnpriceableReason string
	ReasonErr         error

	PricedCount   int
	UnpricedCount int
	FailedCount   int
	UnpricedModel string
}

// JobRequest is one settlement attempt.
type JobRequest struct {
	Provider schemas.ModelProvider
	// ProviderJobID is the provider-side identifier (a batch id, a video id).
	ProviderJobID string
	FallbackModel string

	Job       *cstables.TableProviderJob
	BaseLog   *logstore.Log
	SourceLog *logstore.Log

	Emitter       AggregateLogEmitter
	UsageReporter UsageReporter
	ClaimedBy     string
	Scopes        *modelcatalog.PricingLookupScopes
	Now           time.Time

	// Payload is the kind's settlement input, from PollResult or an inline caller.
	Payload any
	// ForceClaim relaxes the terminal-state guard to admit "unpriceable" jobs. That
	// state means "stop polling", not "refuse money", so a caller that actually
	// holds settlement inputs may re-drive one. "accounted" is terminal regardless.
	ForceClaim bool
}

// Outcome is the engine's report on one settlement attempt.
type Outcome struct {
	JobID           string
	LogID           string
	Provider        schemas.ModelProvider
	ProviderJobID   string
	Cost            float64
	Usage           schemas.BifrostLLMUsage
	ModelBreakdowns map[string]schemas.BatchModelBreakdown
	PricedCount     int
	UnpricedCount   int
	FailedCount     int
	// UnpricedModel names the model behind wholly-unpriced usage when it is
	// unambiguous, so the logged row is attributable (and therefore backfillable).
	UnpricedModel string
	// Status is the provider's lifecycle status as of this settlement attempt.
	Status string
	// Complete reports that every unit of work priced, so Cost is the real total.
	Complete          bool
	Accounted         bool
	Claimed           bool
	UnpriceableReason string
}
