package tables

import "time"

// The Heimdall plugin keeps its runs and rollouts here, in the config store, because they need the same
// durability as configuration. Nothing in these tables is read by core.
//
// Snapshots and replay actions hold prompts, tool arguments and tool results, so they are as sensitive as the
// logs they came from. A retention job nulls them after the configured number of days; scores, verdicts and
// findings stay.

// TableHeimdallRun is one evaluation of a candidate model against recorded sessions.
type TableHeimdallRun struct {
	ID     string `gorm:"primaryKey;type:varchar(36)" json:"id"`
	Name   string `gorm:"type:varchar(255)" json:"name"`
	Status string `gorm:"type:varchar(32);not null;index:idx_heimdall_runs_status_created,priority:1" json:"status"` // queued, running, complete, cancelled, failed, interrupted
	Stage  string `gorm:"type:varchar(32)" json:"stage"`                                                             // planning, replaying, evaluating, done

	CandidateProvider string `gorm:"type:varchar(255);index:idx_heimdall_runs_candidate,priority:1" json:"candidate_provider"`
	CandidateModel    string `gorm:"type:varchar(255);index:idx_heimdall_runs_candidate,priority:2" json:"candidate_model"`
	BaselineProvider  string `gorm:"type:varchar(255);not null" json:"baseline_provider"`
	BaselineModel     string `gorm:"type:varchar(255);not null" json:"baseline_model"`

	SelectionJSON string `gorm:"type:text" json:"-"` // what the run was asked to cover
	ParamsJSON    string `gorm:"type:text" json:"-"` // RunParams, the seed included
	RulesJSON     string `gorm:"type:text" json:"-"` // snapshot of the tool rules

	MaxUSD   float64 `gorm:"default:0" json:"max_usd"`
	MaxCalls int     `gorm:"default:0" json:"max_calls"` // caps model calls, because a model with no catalog price costs nothing
	SpendUSD float64 `gorm:"default:0" json:"spend_usd"`

	ReplaysTotal     int `gorm:"default:0" json:"replays_total"`
	ReplaysDone      int `gorm:"default:0" json:"replays_done"`
	SessionsSelected int `gorm:"default:0" json:"sessions_selected"`
	SessionsEligible int `gorm:"default:0" json:"sessions_eligible"`
	SessionsScored   int `gorm:"default:0" json:"sessions_scored"`

	IneligibleJSON string  `gorm:"type:text" json:"-"` // counts by reason code
	Verdict        *string `gorm:"type:varchar(16);index" json:"verdict"`
	DecisionJSON   string  `gorm:"type:text" json:"-"` // the whole GateDecision
	MetricsJSON    string  `gorm:"type:text" json:"-"` // latency, cost and tokens by arm, and tool activity
	ErrorCode      string  `gorm:"type:varchar(64)" json:"error_code"`
	ErrorMessage   string  `gorm:"type:text" json:"error_message"`
	CreatedBy      string  `gorm:"type:varchar(255)" json:"created_by"`

	CreatedAt  time.Time  `gorm:"not null;index:idx_heimdall_runs_status_created,priority:2" json:"created_at"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// TableName returns the table name for runs.
func (TableHeimdallRun) TableName() string { return "heimdall_runs" }

// TableHeimdallRunSession is one session within a run: whether it could be replayed and how it scored.
type TableHeimdallRunSession struct {
	RunID     string `gorm:"primaryKey;type:varchar(36);index:idx_heimdall_run_sessions_critical,priority:1" json:"run_id"`
	SessionID string `gorm:"primaryKey;type:varchar(255)" json:"session_id"`

	Eligibility  string  `gorm:"type:varchar(32);index" json:"eligibility"`
	SnapshotJSON *string `gorm:"type:text" json:"-"` // the trace and its recorded tool calls; nulled by retention
	// EncryptionStatus is "unencrypted" until snapshot encryption lands.
	EncryptionStatus string `gorm:"type:varchar(20)" json:"encryption_status"`

	BaselineDivergence  *float64 `json:"baseline_divergence"`
	CandidateDivergence *float64 `json:"candidate_divergence"`
	Delta               *float64 `json:"delta"`
	CriticalCount       int      `gorm:"default:0;index:idx_heimdall_run_sessions_critical,priority:2" json:"critical_count"`
	FirstDivergence     int      `gorm:"default:-1" json:"first_divergence"`
	ScoreJSON           string   `gorm:"type:text" json:"-"` // the SessionScore the gate decided from, so the verdict can be recomputed

	Run *TableHeimdallRun `gorm:"foreignKey:RunID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

// TableName returns the table name for run sessions.
func (TableHeimdallRunSession) TableName() string { return "heimdall_run_sessions" }

// TableHeimdallReplay is one replay of one session on one model.
type TableHeimdallReplay struct {
	ID        string `gorm:"primaryKey;type:varchar(36)" json:"id"`
	RunID     string `gorm:"type:varchar(36);not null;index:idx_heimdall_replays_run_session,priority:1;uniqueIndex:idx_heimdall_replays_attempt,priority:1" json:"run_id"`
	SessionID string `gorm:"type:varchar(255);not null;index:idx_heimdall_replays_run_session,priority:2;uniqueIndex:idx_heimdall_replays_attempt,priority:2" json:"session_id"`
	Arm       string `gorm:"type:varchar(16);not null;uniqueIndex:idx_heimdall_replays_attempt,priority:3" json:"arm"`
	Attempt   int    `gorm:"not null;uniqueIndex:idx_heimdall_replays_attempt,priority:4" json:"attempt"`

	Outcome        string   `gorm:"type:varchar(32)" json:"outcome"`
	ActionsJSON    *string  `gorm:"type:text" json:"-"` // the replay's actions, with model text; nulled by retention
	ComparisonJSON string   `gorm:"type:text" json:"-"` // levels and findings: tool names and codes only
	Divergence     *float64 `json:"divergence"`

	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	LatencyMS        float64 `json:"latency_ms"`
	ErrorMessage     string  `gorm:"type:text" json:"error_message"`

	CreatedAt time.Time `json:"created_at"`

	Run *TableHeimdallRun `gorm:"foreignKey:RunID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

// TableName returns the table name for replays.
func (TableHeimdallReplay) TableName() string { return "heimdall_replays" }

// TableHeimdallRollout is a canary rollout of a candidate that passed the gate.
type TableHeimdallRollout struct {
	ID    string `gorm:"primaryKey;type:varchar(36)" json:"id"`
	RunID string `gorm:"type:varchar(36);not null;index" json:"run_id"` // every rollout traces to a gate
	// GateVerdict is copied from the run at creation, and a check requires it to be PASS.
	GateVerdict string `gorm:"type:varchar(16);not null" json:"gate_verdict"`

	BaselineProvider  string `gorm:"type:varchar(255)" json:"baseline_provider"`
	BaselineModel     string `gorm:"type:varchar(255)" json:"baseline_model"`
	CandidateProvider string `gorm:"type:varchar(255)" json:"candidate_provider"`
	CandidateModel    string `gorm:"type:varchar(255)" json:"candidate_model"`

	Status            string     `gorm:"type:varchar(32);index" json:"status"` // pending, ramping, evaluating, completed, rolled_back, retired
	StagesJSON        string     `gorm:"type:text" json:"-"`
	GuardrailsJSON    string     `gorm:"type:text" json:"-"`
	StageIndex        int        `json:"stage_index"`
	WindowSeconds     int        `json:"window_seconds"`
	WindowStartedAt   *time.Time `json:"window_started_at"`
	NoSessionRequests int64      `json:"no_session_requests"` // requests that could not be bucketed
	CreatedBy         string     `gorm:"type:varchar(255)" json:"created_by"`
	CreatedAt         time.Time  `json:"created_at"`
	FinishedAt        *time.Time `json:"finished_at"`

	Run *TableHeimdallRun `gorm:"foreignKey:RunID;references:ID" json:"-"`
}

// TableName returns the table name for rollouts.
func (TableHeimdallRollout) TableName() string { return "heimdall_rollouts" }

// TableHeimdallRolloutEvent is one step in a rollout's history, including every rollback.
type TableHeimdallRolloutEvent struct {
	ID                 string    `gorm:"primaryKey;type:varchar(36)" json:"id"`
	RolloutID          string    `gorm:"type:varchar(36);not null;index:idx_heimdall_rollout_events_rollout,priority:1" json:"rollout_id"`
	Type               string    `gorm:"type:varchar(32)" json:"type"` // created, stage_started, stage_passed, window_extended, rollback, completed, retired
	Actor              string    `gorm:"type:varchar(255)" json:"actor"`
	Reason             string    `gorm:"type:text" json:"reason"`
	BaselineStatsJSON  string    `gorm:"type:text" json:"-"`
	CandidateStatsJSON string    `gorm:"type:text" json:"-"`
	CreatedAt          time.Time `gorm:"index:idx_heimdall_rollout_events_rollout,priority:2" json:"created_at"`

	Rollout *TableHeimdallRollout `gorm:"foreignKey:RolloutID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

// TableName returns the table name for rollout events.
func (TableHeimdallRolloutEvent) TableName() string { return "heimdall_rollout_events" }
