package logstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Trace sources name the SDK family a span was received from.
const (
	TraceSourceBraintrust = "braintrust"
	TraceSourceLangfuse   = "langfuse"
	TraceSourceArize      = "arize"
	TraceSourceOTel       = "otel"
)

// Trace span kinds are the canonical kinds every vendor type maps onto. A span the mappers cannot
// classify is stored as TraceSpanKindUnknown, never dropped.
const (
	TraceSpanKindLLM       = "llm"
	TraceSpanKindTool      = "tool"
	TraceSpanKindTask      = "task"
	TraceSpanKindFunction  = "function"
	TraceSpanKindAgent     = "agent"
	TraceSpanKindChain     = "chain"
	TraceSpanKindRetriever = "retriever"
	TraceSpanKindEmbedding = "embedding"
	TraceSpanKindReranker  = "reranker"
	TraceSpanKindGuardrail = "guardrail"
	TraceSpanKindScore     = "score"
	TraceSpanKindEval      = "eval"
	TraceSpanKindEvent     = "event"
	TraceSpanKindHTTP      = "http"
	TraceSpanKindUnknown   = "unknown"
)

// Trace span statuses.
const (
	TraceSpanStatusOK    = "ok"
	TraceSpanStatusError = "error"
)

// Trace upload statuses.
const (
	TraceUploadStatusPending = "pending"
	TraceUploadStatusDone    = "done"
	TraceUploadStatusError   = "error"
)

// Score and comment sources name who wrote the row.
const (
	TraceAuthorSDK       = "sdk"
	TraceAuthorEvaluator = "evaluator"
	TraceAuthorHuman     = "human"
)

// Linked log kinds name the gateway table a trace span points at.
const (
	TraceLinkedLogKindLLM = "llm"
	TraceLinkedLogKindMCP = "mcp"
)

// TraceSpanPart is one row exactly as an SDK sent it. Parts are append-only: the span row is a fold
// of its parts, so a late or retried part never loses data. The id is derived from the part's
// content, which makes a resent part a no-op.
type TraceSpanPart struct {
	ID         string     `gorm:"primaryKey;type:varchar(64)" json:"id"`
	ProjectID  string     `gorm:"type:varchar(128);not null;index:idx_trace_span_parts_span,priority:1" json:"project_id"`
	TraceID    string     `gorm:"type:varchar(128);not null;index:idx_trace_span_parts_span,priority:2" json:"trace_id"`
	SpanID     string     `gorm:"type:varchar(128);not null;index:idx_trace_span_parts_span,priority:3" json:"span_id"`
	RowID      string     `gorm:"type:varchar(255)" json:"row_id,omitempty"` // Braintrust row id, the SDK's merge key
	Source     string     `gorm:"type:varchar(32);not null" json:"source"`
	ReceivedAt time.Time  `gorm:"not null;index:idx_trace_span_parts_span,priority:4" json:"received_at"`
	IsMerge    bool       `json:"is_merge"` // Braintrust _is_merge: a partial update of an earlier row
	Payload    string     `gorm:"type:text" json:"payload"`
	ExpiresAt  *time.Time `gorm:"index:idx_trace_span_parts_expires_at" json:"expires_at,omitempty"`
}

func (TraceSpanPart) TableName() string { return "trace_span_parts" }

// ContentID is the part's identity: the span it belongs to plus what it says. Receive time is left
// out on purpose, so a retried request maps onto the row already stored.
func (p *TraceSpanPart) ContentID() string {
	h := sha256.New()
	for _, field := range []string{p.ProjectID, p.TraceID, p.SpanID, p.RowID, strconv.FormatBool(p.IsMerge), p.Payload} {
		h.Write([]byte(strconv.Itoa(len(field))))
		h.Write([]byte{':'})
		h.Write([]byte(field))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (p *TraceSpanPart) BeforeCreate(_ *gorm.DB) error {
	if p.ReceivedAt.IsZero() {
		p.ReceivedAt = time.Now().UTC()
	}
	if p.ID == "" {
		p.ID = p.ContentID()
	}
	return nil
}

// TraceSpan is the folded span. Its identity is project, trace and span together: an OTLP span id
// is unique only inside its own trace.
type TraceSpan struct {
	ProjectID string `gorm:"primaryKey;type:varchar(128);index:idx_trace_spans_project_start,priority:1" json:"project_id"`
	TraceID   string `gorm:"primaryKey;type:varchar(128)" json:"trace_id"`
	SpanID    string `gorm:"primaryKey;type:varchar(128)" json:"span_id"`

	ParentSpanID  string  `gorm:"type:varchar(128)" json:"parent_span_id,omitempty"` // the tree parent: the first of ParentSpanIDsParsed
	ParentSpanIDs *string `gorm:"type:text" json:"-"`
	RowID         string  `gorm:"type:varchar(255);index:idx_trace_spans_row_id" json:"row_id,omitempty"`
	Source        string  `gorm:"type:varchar(32);not null" json:"source"`
	Name          string  `gorm:"type:text" json:"name"`
	Kind          string  `gorm:"type:varchar(32);not null;default:'unknown'" json:"kind"`
	Status        string  `gorm:"type:varchar(16)" json:"status,omitempty"`
	Error         string  `gorm:"type:text" json:"error,omitempty"`
	Partial       bool    `json:"partial"` // no base row or no end yet: the span is still arriving

	StartTime  time.Time  `gorm:"not null;index:idx_trace_spans_project_start,priority:2" json:"start_time"`
	EndTime    *time.Time `json:"end_time,omitempty"`
	DurationMs *float64   `json:"duration_ms,omitempty"`

	Input      string  `gorm:"type:text" json:"input,omitempty"`
	Output     string  `gorm:"type:text" json:"output,omitempty"`
	Expected   string  `gorm:"type:text" json:"expected,omitempty"`
	Metadata   *string `gorm:"type:text" json:"-"`
	Tags       *string `gorm:"type:text" json:"-"`
	Attributes *string `gorm:"type:text" json:"-"` // vendor attributes with no column of their own

	Model              string     `gorm:"type:varchar(255)" json:"model,omitempty"`
	Provider           string     `gorm:"type:varchar(128)" json:"provider,omitempty"`
	PromptTokens       int        `json:"prompt_tokens,omitempty"`
	CompletionTokens   int        `json:"completion_tokens,omitempty"`
	TotalTokens        int        `json:"total_tokens,omitempty"`
	CachedReadTokens   int        `json:"cached_read_tokens,omitempty"`
	CachedWriteTokens  int        `json:"cached_write_tokens,omitempty"`
	ReasoningTokens    int        `json:"reasoning_tokens,omitempty"`
	Cost               *float64   `json:"cost,omitempty"`
	CostEstimated      bool       `json:"cost_estimated,omitempty"` // priced by Bifrost from the model catalog, not sent by the SDK or the gateway
	TimeToFirstTokenMs *float64   `json:"time_to_first_token_ms,omitempty"`
	SessionID          string     `gorm:"type:varchar(255)" json:"session_id,omitempty"`
	UserID             string     `gorm:"type:varchar(255)" json:"user_id,omitempty"`
	Environment        string     `gorm:"type:varchar(128)" json:"environment,omitempty"`
	ExperimentID       string     `gorm:"type:varchar(128)" json:"experiment_id,omitempty"`
	DatasetID          string     `gorm:"type:varchar(128)" json:"dataset_id,omitempty"`
	ProviderResponseID string     `gorm:"type:varchar(255)" json:"provider_response_id,omitempty"`
	LinkMethod         string     `gorm:"type:varchar(32)" json:"link_method,omitempty"`
	LinkSnapshot       *string    `gorm:"type:text" json:"-"` // the gateway's numbers, copied when the link was made
	LinkedLogExpired   bool       `json:"linked_log_expired,omitempty"`
	ContentHidden      bool       `json:"content_hidden,omitempty"` // input and output were dropped by a content switch
	ExpiresAt          *time.Time `gorm:"index:idx_trace_spans_expires_at" json:"expires_at,omitempty"`
	CreatedAt          time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt          time.Time  `gorm:"not null" json:"updated_at"`

	ParentSpanIDsParsed []string           `gorm:"-" json:"parent_span_ids,omitempty"`
	MetadataParsed      map[string]any     `gorm:"-" json:"metadata,omitempty"`
	TagsParsed          []string           `gorm:"-" json:"tags,omitempty"`
	AttributesParsed    map[string]any     `gorm:"-" json:"attributes,omitempty"`
	LinkSnapshotParsed  *TraceLinkSnapshot `gorm:"-" json:"link_snapshot,omitempty"`
}

func (TraceSpan) TableName() string { return "trace_spans" }

// TraceLinkSnapshot is the gateway's own figures for a call, kept on the span so they outlive the
// gateway row's retention.
type TraceLinkSnapshot struct {
	Model             string   `json:"model,omitempty"`
	Provider          string   `json:"provider,omitempty"`
	PromptTokens      int      `json:"prompt_tokens,omitempty"`
	CompletionTokens  int      `json:"completion_tokens,omitempty"`
	TotalTokens       int      `json:"total_tokens,omitempty"`
	CachedReadTokens  int      `json:"cached_read_tokens,omitempty"`
	CachedWriteTokens int      `json:"cached_write_tokens,omitempty"`
	ReasoningTokens   int      `json:"reasoning_tokens,omitempty"`
	Cost              *float64 `json:"cost,omitempty"`
	LatencyMs         *float64 `json:"latency_ms,omitempty"`
}

func (s *TraceSpan) BeforeCreate(_ *gorm.DB) error {
	now := time.Now().UTC()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	s.UpdatedAt = now
	if s.Kind == "" {
		s.Kind = TraceSpanKindUnknown
	}
	return s.SerializeFields()
}

func (s *TraceSpan) AfterFind(_ *gorm.DB) error {
	return s.DeserializeFields()
}

// SerializeFields writes the parsed fields into their stored columns and derives the columns that
// follow from others: the tree parent and the duration.
func (s *TraceSpan) SerializeFields() error {
	if len(s.ParentSpanIDsParsed) > 0 {
		s.ParentSpanID = s.ParentSpanIDsParsed[0]
	}
	if s.EndTime != nil && !s.StartTime.IsZero() {
		s.DurationMs = new(float64(s.EndTime.Sub(s.StartTime)) / float64(time.Millisecond))
	}
	for _, column := range []struct {
		name   string
		empty  bool
		parsed any
		stored **string
	}{
		{"parent span ids", len(s.ParentSpanIDsParsed) == 0, s.ParentSpanIDsParsed, &s.ParentSpanIDs},
		{"metadata", len(s.MetadataParsed) == 0, s.MetadataParsed, &s.Metadata},
		{"tags", len(s.TagsParsed) == 0, s.TagsParsed, &s.Tags},
		{"attributes", len(s.AttributesParsed) == 0, s.AttributesParsed, &s.Attributes},
		{"link snapshot", s.LinkSnapshotParsed == nil, s.LinkSnapshotParsed, &s.LinkSnapshot},
	} {
		if column.empty {
			continue
		}
		data, err := schemas.MarshalSorted(column.parsed)
		if err != nil {
			return fmt.Errorf("failed to serialize trace span %s: %w", column.name, err)
		}
		*column.stored = new(string(data))
	}
	return nil
}

// DeserializeFields reads the stored columns back into their parsed fields. A malformed column is
// left unparsed rather than failing the read.
func (s *TraceSpan) DeserializeFields() error {
	unmarshal := func(stored *string, v any) {
		if stored != nil && *stored != "" {
			_ = sonic.Unmarshal([]byte(*stored), v)
		}
	}
	unmarshal(s.ParentSpanIDs, &s.ParentSpanIDsParsed)
	unmarshal(s.Metadata, &s.MetadataParsed)
	unmarshal(s.Tags, &s.TagsParsed)
	unmarshal(s.Attributes, &s.AttributesParsed)
	s.LinkSnapshotParsed = nil
	if s.LinkSnapshot != nil && *s.LinkSnapshot != "" {
		var snapshot TraceLinkSnapshot
		if err := sonic.Unmarshal([]byte(*s.LinkSnapshot), &snapshot); err == nil {
			s.LinkSnapshotParsed = &snapshot
		}
	}
	return nil
}

// TraceSpanLink points a span at one gateway row. A span has one link per gateway row of its call,
// so a fallback or retry chain is counted in full.
type TraceSpanLink struct {
	ProjectID string    `gorm:"primaryKey;type:varchar(128)" json:"project_id"`
	TraceID   string    `gorm:"primaryKey;type:varchar(128)" json:"trace_id"`
	SpanID    string    `gorm:"primaryKey;type:varchar(128)" json:"span_id"`
	LogKind   string    `gorm:"primaryKey;type:varchar(16);index:idx_trace_span_links_log,priority:1" json:"log_kind"` // llm | mcp
	LogID     string    `gorm:"primaryKey;type:varchar(255);index:idx_trace_span_links_log,priority:2" json:"log_id"`
	Method    string    `gorm:"type:varchar(32);not null" json:"method"`
	CreatedAt time.Time `gorm:"not null" json:"created_at"`

	ExpiresAt *time.Time `gorm:"index:idx_trace_span_links_expires_at" json:"expires_at,omitempty"`
}

func (TraceSpanLink) TableName() string { return "trace_span_links" }

// Trace is the rollup row the trace list reads. The first span to arrive creates it, so a trace
// whose root has not landed yet is still listed, marked in progress.
type Trace struct {
	ProjectID string `gorm:"primaryKey;type:varchar(128);index:idx_traces_project_start,priority:1;index:idx_traces_project_session,priority:1" json:"project_id"`
	TraceID   string `gorm:"primaryKey;type:varchar(128)" json:"trace_id"`

	Source       string     `gorm:"type:varchar(32);not null" json:"source"`
	Name         string     `gorm:"type:text" json:"name"`
	RootSpanID   string     `gorm:"type:varchar(128)" json:"root_span_id,omitempty"`
	InProgress   bool       `json:"in_progress"`
	StartTime    time.Time  `gorm:"not null;index:idx_traces_project_start,priority:2" json:"start_time"`
	EndTime      *time.Time `json:"end_time,omitempty"`
	DurationMs   *float64   `json:"duration_ms,omitempty"`
	Input        string     `gorm:"type:text" json:"input,omitempty"`
	Output       string     `gorm:"type:text" json:"output,omitempty"`
	Tags         *string    `gorm:"type:text" json:"-"`
	Scores       *string    `gorm:"type:text" json:"-"`
	SessionID    string     `gorm:"type:varchar(255);index:idx_traces_project_session,priority:2" json:"session_id,omitempty"`
	UserID       string     `gorm:"type:varchar(255)" json:"user_id,omitempty"`
	Environment  string     `gorm:"type:varchar(128)" json:"environment,omitempty"`
	ExperimentID string     `gorm:"type:varchar(128)" json:"experiment_id,omitempty"`

	SpanCount         int      `json:"span_count"`
	LLMSpanCount      int      `json:"llm_span_count"`
	LinkedSpanCount   int      `json:"linked_span_count"`
	ErrorCount        int      `json:"error_count"`
	PromptTokens      int      `json:"prompt_tokens"`
	CompletionTokens  int      `json:"completion_tokens"`
	TotalTokens       int      `json:"total_tokens"`
	CachedReadTokens  int      `json:"cached_read_tokens"`
	Cost              *float64 `json:"cost,omitempty"`
	EstimatedCost     *float64 `json:"estimated_cost,omitempty"` // the share of Cost priced by Bifrost
	UnpricedSpanCount int      `json:"unpriced_span_count"`      // LLM spans with tokens but no price

	ExpiresAt *time.Time `gorm:"index:idx_traces_expires_at" json:"expires_at,omitempty"`
	CreatedAt time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time  `gorm:"not null" json:"updated_at"`

	TagsParsed   []string           `gorm:"-" json:"tags,omitempty"`
	ScoresParsed map[string]float64 `gorm:"-" json:"scores,omitempty"`
}

func (Trace) TableName() string { return "traces" }

func (t *Trace) BeforeCreate(_ *gorm.DB) error {
	now := time.Now().UTC()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	return t.SerializeFields()
}

func (t *Trace) AfterFind(_ *gorm.DB) error {
	return t.DeserializeFields()
}

// SerializeFields writes the parsed fields into their stored columns.
func (t *Trace) SerializeFields() error {
	if t.EndTime != nil && !t.StartTime.IsZero() {
		t.DurationMs = new(float64(t.EndTime.Sub(t.StartTime)) / float64(time.Millisecond))
	}
	if len(t.TagsParsed) > 0 {
		data, err := schemas.MarshalSorted(t.TagsParsed)
		if err != nil {
			return fmt.Errorf("failed to serialize trace tags: %w", err)
		}
		t.Tags = new(string(data))
	}
	if len(t.ScoresParsed) > 0 {
		data, err := schemas.MarshalSorted(t.ScoresParsed)
		if err != nil {
			return fmt.Errorf("failed to serialize trace scores: %w", err)
		}
		t.Scores = new(string(data))
	}
	return nil
}

// DeserializeFields reads the stored columns back into their parsed fields.
func (t *Trace) DeserializeFields() error {
	if t.Tags != nil && *t.Tags != "" {
		_ = sonic.Unmarshal([]byte(*t.Tags), &t.TagsParsed)
	}
	if t.Scores != nil && *t.Scores != "" {
		_ = sonic.Unmarshal([]byte(*t.Scores), &t.ScoresParsed)
	}
	return nil
}

// TraceScore is one score on a trace or on a span of it, from an SDK, an evaluator or a reviewer.
type TraceScore struct {
	ID          string    `gorm:"primaryKey;type:varchar(64)" json:"id"`
	ProjectID   string    `gorm:"type:varchar(128);not null;index:idx_trace_scores_trace,priority:1" json:"project_id"`
	TraceID     string    `gorm:"type:varchar(128);not null;index:idx_trace_scores_trace,priority:2" json:"trace_id"`
	SpanID      string    `gorm:"type:varchar(128)" json:"span_id,omitempty"` // empty = the score is on the trace
	Name        string    `gorm:"type:varchar(255);not null" json:"name"`
	Value       *float64  `json:"value,omitempty"`
	StringValue string    `gorm:"type:text" json:"string_value,omitempty"`
	DataType    string    `gorm:"type:varchar(32)" json:"data_type,omitempty"` // numeric | categorical | boolean | text
	Comment     string    `gorm:"type:text" json:"comment,omitempty"`
	Source      string    `gorm:"type:varchar(32);not null" json:"source"` // sdk | evaluator | human
	Author      string    `gorm:"type:varchar(255)" json:"author,omitempty"`
	CreatedAt   time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt   time.Time `gorm:"not null" json:"updated_at"`

	ExpiresAt *time.Time `gorm:"index:idx_trace_scores_expires_at" json:"expires_at,omitempty"`
}

func (TraceScore) TableName() string { return "trace_scores" }

// TraceComment is a reviewer's note on a trace or on a span of it.
type TraceComment struct {
	ID        string    `gorm:"primaryKey;type:varchar(64)" json:"id"`
	ProjectID string    `gorm:"type:varchar(128);not null;index:idx_trace_comments_trace,priority:1" json:"project_id"`
	TraceID   string    `gorm:"type:varchar(128);not null;index:idx_trace_comments_trace,priority:2" json:"trace_id"`
	SpanID    string    `gorm:"type:varchar(128)" json:"span_id,omitempty"`
	Author    string    `gorm:"type:varchar(255)" json:"author,omitempty"`
	Text      string    `gorm:"type:text" json:"text"`
	Source    string    `gorm:"type:varchar(32);not null" json:"source"` // sdk | human
	CreatedAt time.Time `gorm:"not null" json:"created_at"`

	ExpiresAt *time.Time `gorm:"index:idx_trace_comments_expires_at" json:"expires_at,omitempty"`
}

func (TraceComment) TableName() string { return "trace_comments" }

// TraceUpload is a file an SDK uploads beside its spans: a Braintrust attachment or overflow body,
// Langfuse media, an Arize blob. The bytes live in the object store when one is configured, and in
// Data otherwise.
type TraceUpload struct {
	ID          string     `gorm:"primaryKey;type:varchar(128)" json:"id"`
	ProjectID   string     `gorm:"type:varchar(128);not null;index:idx_trace_uploads_project" json:"project_id"`
	Source      string     `gorm:"type:varchar(32);not null" json:"source"`
	ContentType string     `gorm:"type:varchar(255)" json:"content_type,omitempty"`
	Filename    string     `gorm:"type:text" json:"filename,omitempty"`
	SizeBytes   int64      `json:"size_bytes"`
	Status      string     `gorm:"type:varchar(16);not null" json:"status"` // pending | done | error
	ObjectKey   string     `gorm:"type:text" json:"object_key,omitempty"`
	Data        []byte     `json:"-"`
	ExpiresAt   *time.Time `gorm:"index:idx_trace_uploads_expires_at" json:"expires_at,omitempty"`
	CreatedAt   time.Time  `gorm:"not null" json:"created_at"`
}

func (TraceUpload) TableName() string { return "trace_uploads" }

// TraceDirtyMark queues one trace for a rollup recompute. It is a table of its own, not a column on
// the rollup row, so marking a trace never rewrites that wide row.
type TraceDirtyMark struct {
	ProjectID string    `gorm:"primaryKey;type:varchar(128)" json:"project_id"`
	TraceID   string    `gorm:"primaryKey;type:varchar(128)" json:"trace_id"`
	Dirty     bool      `gorm:"index:idx_trace_dirty_marks_dirty,priority:1" json:"dirty"`
	MarkedAt  time.Time `gorm:"not null;index:idx_trace_dirty_marks_dirty,priority:2" json:"marked_at"`
}

func (TraceDirtyMark) TableName() string { return "trace_dirty_marks" }

// TraceExperiment is an experiment an SDK registered: a Braintrust experiment or a Langfuse dataset
// run. Its traces are the spans that carry its id. The row keeps what the id alone cannot: the name.
type TraceExperiment struct {
	ProjectID   string     `gorm:"primaryKey;type:varchar(128)" json:"project_id"`
	ID          string     `gorm:"primaryKey;type:varchar(128)" json:"id"`
	Name        string     `gorm:"type:varchar(255);not null" json:"name"`
	Source      string     `gorm:"type:varchar(32);not null" json:"source"`
	DatasetName string     `gorm:"type:varchar(255)" json:"dataset_name,omitempty"`
	Description string     `gorm:"type:text" json:"description,omitempty"`
	CreatedAt   time.Time  `gorm:"not null" json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

func (TraceExperiment) TableName() string { return "trace_experiments" }

// traceModels lists every trace table, in creation order.
func traceModels() []any {
	return []any{&TraceSpanPart{}, &TraceSpan{}, &TraceSpanLink{}, &Trace{}, &TraceScore{}, &TraceComment{}, &TraceUpload{}, &TraceDirtyMark{}}
}

// TraceKey names one trace.
type TraceKey struct {
	ProjectID string `json:"project_id"`
	TraceID   string `json:"trace_id"`
}

// DirtyTrace is a trace waiting for a rollup recompute, with the time it was last marked.
type DirtyTrace struct {
	TraceKey
	MarkedAt time.Time `json:"marked_at"`
}

// TraceStore is the storage surface of the Tracing feature. Every LogStore implements it.
type TraceStore interface {
	TraceReadStore
	BatchCreateTraceSpanParts(ctx context.Context, parts []*TraceSpanPart) (int, error)
	ListTraceSpanParts(ctx context.Context, projectID, traceID, spanID string) ([]*TraceSpanPart, error)
	ListTraceSpanPartsForSpans(ctx context.Context, projectID, traceID string, spanIDs []string) ([]*TraceSpanPart, error)
	UpsertTraceSpans(ctx context.Context, spans []*TraceSpan) error
	ListTraceSpans(ctx context.Context, projectID, traceID string) ([]*TraceSpan, error)
	FindTraceSpans(ctx context.Context, projectID string, keys []TraceSpanKey) ([]*TraceSpan, error)
	FindTraceSpansByRowIDs(ctx context.Context, projectID string, rowIDs []string) ([]*TraceSpan, error)
	FindTrace(ctx context.Context, projectID, traceID string) (*Trace, error)
	RecomputeTraceRollup(ctx context.Context, projectID, traceID string) (*Trace, error)
	MarkTracesDirty(ctx context.Context, keys []TraceKey) error
	ListDirtyTraces(ctx context.Context, limit int) ([]DirtyTrace, error)
	ClearTraceDirty(ctx context.Context, trace DirtyTrace) error
	CreateTraceSpanLinks(ctx context.Context, links []*TraceSpanLink) error
	ListTraceSpanLinks(ctx context.Context, projectID, traceID string) ([]*TraceSpanLink, error)
	FindTraceSpanLinksByLog(ctx context.Context, logKind string, logIDs []string) ([]*TraceSpanLink, error)
	UpsertTraceScores(ctx context.Context, scores []*TraceScore) error
	ListTraceScores(ctx context.Context, projectID, traceID string) ([]*TraceScore, error)
	DeleteTraceScore(ctx context.Context, projectID, id string) error
	CreateTraceComment(ctx context.Context, comment *TraceComment) error
	ListTraceComments(ctx context.Context, projectID, traceID string) ([]*TraceComment, error)
	DeleteTraceComment(ctx context.Context, projectID, id string) error
	CreateTraceUpload(ctx context.Context, upload *TraceUpload) error
	FindTraceUpload(ctx context.Context, id string) (*TraceUpload, error)
	CompleteTraceUpload(ctx context.Context, id, status, objectKey string, data []byte) error
	// TraceUploadsAvailable reports whether the store has somewhere to keep upload bytes.
	TraceUploadsAvailable() bool
	SaveTraceUploadBytes(ctx context.Context, id string, data []byte) error
	// CompleteTraceUploadRow rewrites an upload row whole.
	CompleteTraceUploadRow(ctx context.Context, upload *TraceUpload) error
	ReadTraceUploadBytes(ctx context.Context, id string) (*TraceUpload, []byte, error)
	DeleteTraces(ctx context.Context, keys []TraceKey) error
	// Experiments registered by SDKs.
	RegisterTraceExperiment(ctx context.Context, experiment *TraceExperiment) error
	FindTraceExperiment(ctx context.Context, projectID, id string) (*TraceExperiment, error)
	ListTraceExperiments(ctx context.Context, projectIDs []string, limit int) ([]*TraceExperiment, error)
	// UpdateTraceSpanEnrichment writes what the matcher and the cost resolver add to a span.
	UpdateTraceSpanEnrichment(ctx context.Context, projectID, traceID, spanID string, enrichment TraceSpanEnrichment) error
	// Gateway rows a span can be linked to.
	FindGatewayCallsByTraceID(ctx context.Context, traceID string) ([]*GatewayCall, error)
	FindGatewayCallsByProviderResponseIDs(ctx context.Context, ids []string) ([]*GatewayCall, error)
	FindGatewayCallsByFingerprint(ctx context.Context, fingerprint string, from, to time.Time, virtualKeyIDs []string) ([]*GatewayCall, error)
	FindGatewayCallChain(ctx context.Context, logKind string, ids []string) ([]*GatewayCall, error)
	DeleteExpiredTraceRowsBatch(ctx context.Context, now time.Time, batchSize int) (int64, error)
}

// TraceSpanKey names one span inside a project.
type TraceSpanKey struct {
	TraceID string `json:"trace_id"`
	SpanID  string `json:"span_id"`
}

// replacesOnInsert reports whether a plain insert already replaces the row with the same key. That
// is ClickHouse: its trace tables are ReplacingMergeTree and it has no ON CONFLICT clause.
func (s *RDBLogStore) replacesOnInsert() bool {
	return s.db.Dialector.Name() == "clickhouse"
}

// traceUpsert inserts rows, replacing every column of a stored row with the same key except the
// kept ones. With no columns to replace, a stored row wins and the insert is a no-op.
func (s *RDBLogStore) traceUpsert(tx *gorm.DB, model any, key []string, keep ...string) *gorm.DB {
	if s.replacesOnInsert() {
		return tx
	}
	columns := make([]clause.Column, len(key))
	for i, name := range key {
		columns[i] = clause.Column{Name: name}
	}
	if keep == nil {
		return tx.Clauses(clause.OnConflict{Columns: columns, DoNothing: true})
	}
	return tx.Clauses(clause.OnConflict{Columns: columns, DoUpdates: clause.AssignmentColumns(replaceColumns(tx, model, append(key, keep...)...))})
}

// traceDelete removes rows from a trace table and reports how many. It runs as plain SQL because
// GORM's Delete becomes a heavyweight mutation on ClickHouse, which needs a lightweight delete and
// never reports rows affected.
func (s *RDBLogStore) traceDelete(ctx context.Context, table, where string, args ...any) (int64, error) {
	db := s.db.WithContext(ctx)
	if !s.replacesOnInsert() {
		result := db.Exec("DELETE FROM "+table+" WHERE "+where, args...)
		return result.RowsAffected, result.Error
	}
	var count int64
	if err := db.Raw("SELECT count() FROM "+table+" WHERE "+where, args...).Scan(&count).Error; err != nil || count == 0 {
		return 0, err
	}
	return count, db.Exec("DELETE FROM "+table+" WHERE "+where+" SETTINGS lightweight_deletes_sync = 1", args...).Error
}

// BatchCreateTraceSpanParts stores received parts and reports how many were new. A part whose
// content is already stored is skipped, which makes an SDK retry harmless. ClickHouse cannot tell
// a new part from a resent one at insert time, so there the count is the batch size.
func (s *RDBLogStore) BatchCreateTraceSpanParts(ctx context.Context, parts []*TraceSpanPart) (int, error) {
	parts = slices.DeleteFunc(slices.Clone(parts), func(p *TraceSpanPart) bool { return p == nil })
	if len(parts) == 0 {
		return 0, nil
	}
	result := s.traceUpsert(s.db.WithContext(ctx), &TraceSpanPart{}, []string{"id"}).CreateInBatches(parts, 200)
	if result.Error != nil {
		return 0, result.Error
	}
	if s.replacesOnInsert() {
		return len(parts), nil
	}
	return int(result.RowsAffected), nil
}

// ListTraceSpanParts returns one span's parts in receive order.
func (s *RDBLogStore) ListTraceSpanParts(ctx context.Context, projectID, traceID, spanID string) ([]*TraceSpanPart, error) {
	var parts []*TraceSpanPart
	err := s.db.WithContext(ctx).
		Where("project_id = ? AND trace_id = ? AND span_id = ?", projectID, traceID, spanID).
		Order("received_at ASC, id ASC").
		Find(&parts).Error
	if err != nil {
		return nil, err
	}
	return parts, nil
}

// ListTraceSpanPartsForSpans returns the parts of several spans of one trace in receive order.
func (s *RDBLogStore) ListTraceSpanPartsForSpans(ctx context.Context, projectID, traceID string, spanIDs []string) ([]*TraceSpanPart, error) {
	if len(spanIDs) == 0 {
		return nil, nil
	}
	var parts []*TraceSpanPart
	err := s.db.WithContext(ctx).
		Where("project_id = ? AND trace_id = ? AND span_id IN ?", projectID, traceID, spanIDs).
		Order("received_at ASC, id ASC").
		Find(&parts).Error
	if err != nil {
		return nil, err
	}
	return parts, nil
}

// UpsertTraceSpans writes folded spans, replacing any stored row with the same project, trace and
// span. The fold always produces the whole span, so the write replaces every column.
func (s *RDBLogStore) UpsertTraceSpans(ctx context.Context, spans []*TraceSpan) error {
	spans = slices.DeleteFunc(slices.Clone(spans), func(span *TraceSpan) bool { return span == nil })
	if len(spans) == 0 {
		return nil
	}
	return s.traceUpsert(s.db.WithContext(ctx), &TraceSpan{}, []string{"project_id", "trace_id", "span_id"}, "created_at").CreateInBatches(spans, 100).Error
}

// replaceColumns is every column of model that an upsert overwrites: all of them except the
// listed ones, which are the key and the columns that keep their first value.
func replaceColumns(tx *gorm.DB, model any, keep ...string) []string {
	stmt := &gorm.Statement{DB: tx}
	if err := stmt.Parse(model); err != nil {
		return nil
	}
	columns := make([]string, 0, len(stmt.Schema.DBNames))
	for _, name := range stmt.Schema.DBNames {
		if slices.Contains(keep, name) {
			continue
		}
		columns = append(columns, name)
	}
	return columns
}

// ListTraceSpans returns every span of one trace in start order.
func (s *RDBLogStore) ListTraceSpans(ctx context.Context, projectID, traceID string) ([]*TraceSpan, error) {
	var spans []*TraceSpan
	err := s.db.WithContext(ctx).
		Where("project_id = ? AND trace_id = ?", projectID, traceID).
		Order("start_time ASC, span_id ASC").
		Find(&spans).Error
	if err != nil {
		return nil, err
	}
	return spans, nil
}

// FindTraceSpans loads the stored rows of the named spans. A span not stored yet is left out.
func (s *RDBLogStore) FindTraceSpans(ctx context.Context, projectID string, keys []TraceSpanKey) ([]*TraceSpan, error) {
	byTrace := make(map[string][]string)
	for _, key := range keys {
		byTrace[key.TraceID] = append(byTrace[key.TraceID], key.SpanID)
	}
	var found []*TraceSpan
	for traceID, spanIDs := range byTrace {
		var spans []*TraceSpan
		if err := s.db.WithContext(ctx).Where("project_id = ? AND trace_id = ? AND span_id IN ?", projectID, traceID, spanIDs).Find(&spans).Error; err != nil {
			return nil, err
		}
		found = append(found, spans...)
	}
	return found, nil
}

// FindTraceSpansByRowIDs loads the spans stored under the given vendor row ids. Braintrust
// addresses a span by its row id when it sends feedback or a comment.
func (s *RDBLogStore) FindTraceSpansByRowIDs(ctx context.Context, projectID string, rowIDs []string) ([]*TraceSpan, error) {
	if len(rowIDs) == 0 {
		return nil, nil
	}
	var spans []*TraceSpan
	query := s.db.WithContext(ctx).Where("row_id IN ?", rowIDs)
	if projectID != "" {
		query = query.Where("project_id = ?", projectID)
	}
	if err := query.Limit(len(rowIDs) * 2).Find(&spans).Error; err != nil {
		return nil, err
	}
	return spans, nil
}

// countsTokens reports whether a span of this kind carries token usage of its own. Only model calls
// do. Frameworks often copy a child's usage onto the chain or agent span above it, so summing every
// span would count the same tokens at each level of the tree.
func countsTokens(kind string) bool {
	return kind == TraceSpanKindLLM || kind == TraceSpanKindEmbedding
}

// FindTrace loads one trace's rollup row, ErrNotFound when it does not exist.
func (s *RDBLogStore) FindTrace(ctx context.Context, projectID, traceID string) (*Trace, error) {
	var traces []Trace
	if err := s.db.WithContext(ctx).Where("project_id = ? AND trace_id = ?", projectID, traceID).Limit(1).Find(&traces).Error; err != nil {
		return nil, err
	}
	if len(traces) == 0 {
		return nil, ErrNotFound
	}
	return &traces[0], nil
}

// RecomputeTraceRollup rebuilds one trace's rollup row from its spans and scores and stores it.
//
// Each model call is counted once. A span linked to a gateway row is counted with the gateway's
// snapshot, and the numbers the SDK reported for that same call are left out. An unlinked span is
// counted with its own numbers.
//
// The root is the span that names no parent. Until it arrives the trace is in progress and takes
// its name from its earliest span.
func (s *RDBLogStore) RecomputeTraceRollup(ctx context.Context, projectID, traceID string) (*Trace, error) {
	spans, err := s.ListTraceSpans(ctx, projectID, traceID)
	if err != nil {
		return nil, err
	}
	scores, err := s.ListTraceScores(ctx, projectID, traceID)
	if err != nil {
		return nil, err
	}
	trace := buildTraceRollup(projectID, traceID, spans, scores)
	if existing, err := s.FindTrace(ctx, projectID, traceID); err == nil {
		trace.CreatedAt = existing.CreatedAt
	} else if err != ErrNotFound {
		return nil, err
	}
	if err := s.traceUpsert(s.db.WithContext(ctx), &Trace{}, []string{"project_id", "trace_id"}, "created_at").Create(trace).Error; err != nil {
		return nil, err
	}
	return trace, nil
}

// buildTraceRollup folds a trace's spans and scores into its rollup row.
func buildTraceRollup(projectID, traceID string, spans []*TraceSpan, scores []*TraceScore) *Trace {
	trace := &Trace{ProjectID: projectID, TraceID: traceID, InProgress: true}
	var root *TraceSpan
	var lastEnd, expires *time.Time
	var cost, estimated float64
	priced, keptForever := false, false
	for _, span := range spans {
		if root == nil && span.ParentSpanID == "" {
			root = span
		}
		if trace.StartTime.IsZero() || span.StartTime.Before(trace.StartTime) {
			trace.StartTime = span.StartTime
		}
		if span.EndTime != nil && (lastEnd == nil || span.EndTime.After(*lastEnd)) {
			lastEnd = span.EndTime
		}
		// The trace lives as long as its longest-lived span, and forever when any span has no expiry.
		switch {
		case span.ExpiresAt == nil:
			keptForever = true
		case expires == nil || span.ExpiresAt.After(*expires):
			expires = span.ExpiresAt
		}
		if trace.Source == "" {
			trace.Source = span.Source
		}
		if trace.SessionID == "" {
			trace.SessionID = span.SessionID
		}
		if trace.UserID == "" {
			trace.UserID = span.UserID
		}
		if trace.Environment == "" {
			trace.Environment = span.Environment
		}
		if trace.ExperimentID == "" {
			trace.ExperimentID = span.ExperimentID
		}
		trace.SpanCount++
		if span.Status == TraceSpanStatusError {
			trace.ErrorCount++
		}
		linked := span.LinkSnapshotParsed
		if linked != nil {
			trace.LinkedSpanCount++
		}
		if !countsTokens(span.Kind) && linked == nil {
			continue
		}
		trace.LLMSpanCount++
		tokens, spanCost := span.TotalTokens, span.Cost
		if linked != nil {
			tokens, spanCost = linked.TotalTokens, linked.Cost
			trace.PromptTokens += linked.PromptTokens
			trace.CompletionTokens += linked.CompletionTokens
			trace.CachedReadTokens += linked.CachedReadTokens
		} else {
			trace.PromptTokens += span.PromptTokens
			trace.CompletionTokens += span.CompletionTokens
			trace.CachedReadTokens += span.CachedReadTokens
		}
		trace.TotalTokens += tokens
		switch {
		case spanCost != nil:
			priced = true
			cost += *spanCost
			if linked == nil && span.CostEstimated {
				estimated += *spanCost
			}
		case tokens > 0:
			trace.UnpricedSpanCount++
		}
	}
	if priced {
		trace.Cost = &cost
		if estimated > 0 {
			trace.EstimatedCost = &estimated
		}
	}
	if !keptForever {
		trace.ExpiresAt = expires
	}
	if root != nil {
		trace.RootSpanID = root.SpanID
		trace.Name = root.Name
		trace.Input = root.Input
		trace.Output = root.Output
		trace.TagsParsed = root.TagsParsed
		trace.EndTime = root.EndTime
		trace.InProgress = root.EndTime == nil || root.Partial
	} else {
		trace.EndTime = lastEnd
		if len(spans) > 0 {
			trace.Name = spans[0].Name
		}
	}
	if trace.StartTime.IsZero() {
		trace.StartTime = time.Now().UTC()
	}

	// A numeric score name is shown as the mean of its values across the trace.
	sums, counts := make(map[string]float64), make(map[string]int)
	for _, score := range scores {
		if score.Value == nil {
			continue
		}
		sums[score.Name] += *score.Value
		counts[score.Name]++
	}
	if len(sums) > 0 {
		trace.ScoresParsed = make(map[string]float64, len(sums))
		for name, sum := range sums {
			trace.ScoresParsed[name] = sum / float64(counts[name])
		}
	}
	return trace
}

// MarkTracesDirty queues traces for a rollup recompute.
func (s *RDBLogStore) MarkTracesDirty(ctx context.Context, keys []TraceKey) error {
	if len(keys) == 0 {
		return nil
	}
	now := time.Now().UTC()
	marks := make([]*TraceDirtyMark, 0, len(keys))
	seen := make(map[TraceKey]struct{}, len(keys))
	for _, key := range keys {
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		marks = append(marks, &TraceDirtyMark{ProjectID: key.ProjectID, TraceID: key.TraceID, Dirty: true, MarkedAt: now})
	}
	return s.traceUpsert(s.db.WithContext(ctx), &TraceDirtyMark{}, []string{"project_id", "trace_id"}, []string{}...).CreateInBatches(marks, 200).Error
}

// ListDirtyTraces returns up to limit traces waiting for a rollup recompute, longest-waiting first.
func (s *RDBLogStore) ListDirtyTraces(ctx context.Context, limit int) ([]DirtyTrace, error) {
	var marks []TraceDirtyMark
	err := s.db.WithContext(ctx).Where("dirty = ?", true).Order("marked_at ASC").Limit(limit).Find(&marks).Error
	if err != nil {
		return nil, err
	}
	dirty := make([]DirtyTrace, len(marks))
	for i, mark := range marks {
		dirty[i] = DirtyTrace{TraceKey: TraceKey{ProjectID: mark.ProjectID, TraceID: mark.TraceID}, MarkedAt: mark.MarkedAt}
	}
	return dirty, nil
}

// ClearTraceDirty takes a trace off the recompute queue, unless it was marked again after the
// caller listed it. In that case the newer mark stays and the trace is recomputed once more.
func (s *RDBLogStore) ClearTraceDirty(ctx context.Context, trace DirtyTrace) error {
	db := s.db.WithContext(ctx)
	if !s.replacesOnInsert() {
		return db.Model(&TraceDirtyMark{}).
			Where("project_id = ? AND trace_id = ? AND marked_at = ?", trace.ProjectID, trace.TraceID, trace.MarkedAt).
			Update("dirty", false).Error
	}
	var marks []TraceDirtyMark
	if err := db.Where("project_id = ? AND trace_id = ?", trace.ProjectID, trace.TraceID).Limit(1).Find(&marks).Error; err != nil {
		return err
	}
	if len(marks) == 0 || !marks[0].MarkedAt.Equal(trace.MarkedAt) {
		return nil
	}
	return db.Create(&TraceDirtyMark{ProjectID: trace.ProjectID, TraceID: trace.TraceID, Dirty: false, MarkedAt: trace.MarkedAt}).Error
}

// traceExpiry returns the expiry of each named trace that has a rollup row with one.
func (s *RDBLogStore) traceExpiry(ctx context.Context, keys map[TraceKey]struct{}) map[TraceKey]*time.Time {
	expiry := make(map[TraceKey]*time.Time, len(keys))
	for key := range keys {
		if trace, err := s.FindTrace(ctx, key.ProjectID, key.TraceID); err == nil {
			expiry[key] = trace.ExpiresAt
		}
	}
	return expiry
}

// CreateTraceSpanLinks stores links from spans to gateway rows. A link already stored is a no-op.
func (s *RDBLogStore) CreateTraceSpanLinks(ctx context.Context, links []*TraceSpanLink) error {
	if len(links) == 0 {
		return nil
	}
	now := time.Now().UTC()
	for _, link := range links {
		if link.CreatedAt.IsZero() {
			link.CreatedAt = now
		}
	}
	return s.traceUpsert(s.db.WithContext(ctx), &TraceSpanLink{}, []string{"project_id", "trace_id", "span_id", "log_kind", "log_id"}).CreateInBatches(links, 200).Error
}

// ListTraceSpanLinks returns every link of one trace.
func (s *RDBLogStore) ListTraceSpanLinks(ctx context.Context, projectID, traceID string) ([]*TraceSpanLink, error) {
	var links []*TraceSpanLink
	err := s.db.WithContext(ctx).Where("project_id = ? AND trace_id = ?", projectID, traceID).Order("created_at ASC, log_id ASC").Find(&links).Error
	if err != nil {
		return nil, err
	}
	return links, nil
}

// FindTraceSpanLinksByLog returns the links that point at the given gateway rows.
func (s *RDBLogStore) FindTraceSpanLinksByLog(ctx context.Context, logKind string, logIDs []string) ([]*TraceSpanLink, error) {
	if len(logIDs) == 0 {
		return nil, nil
	}
	var links []*TraceSpanLink
	if err := s.db.WithContext(ctx).Where("log_kind = ? AND log_id IN ?", logKind, logIDs).Find(&links).Error; err != nil {
		return nil, err
	}
	return links, nil
}

// UpsertTraceScores writes scores, replacing any stored score with the same id. A score with no
// expiry of its own takes its trace's.
func (s *RDBLogStore) UpsertTraceScores(ctx context.Context, scores []*TraceScore) error {
	if len(scores) == 0 {
		return nil
	}
	now := time.Now().UTC()
	keys := make(map[TraceKey]struct{})
	for _, score := range scores {
		if score.ID == "" {
			return fmt.Errorf("trace score has no id")
		}
		if score.CreatedAt.IsZero() {
			score.CreatedAt = now
		}
		score.UpdatedAt = now
		if score.ExpiresAt == nil {
			keys[TraceKey{ProjectID: score.ProjectID, TraceID: score.TraceID}] = struct{}{}
		}
	}
	expiry := s.traceExpiry(ctx, keys)
	for _, score := range scores {
		if score.ExpiresAt == nil {
			score.ExpiresAt = expiry[TraceKey{ProjectID: score.ProjectID, TraceID: score.TraceID}]
		}
	}
	return s.traceUpsert(s.db.WithContext(ctx), &TraceScore{}, []string{"id"}, "created_at").CreateInBatches(scores, 200).Error
}

// ListTraceScores returns every score of one trace, oldest first.
func (s *RDBLogStore) ListTraceScores(ctx context.Context, projectID, traceID string) ([]*TraceScore, error) {
	var scores []*TraceScore
	err := s.db.WithContext(ctx).Where("project_id = ? AND trace_id = ?", projectID, traceID).Order("created_at ASC, id ASC").Find(&scores).Error
	if err != nil {
		return nil, err
	}
	return scores, nil
}

// DeleteTraceScore removes one score of a project.
func (s *RDBLogStore) DeleteTraceScore(ctx context.Context, projectID, id string) error {
	_, err := s.traceDelete(ctx, "trace_scores", "project_id = ? AND id = ?", projectID, id)
	return err
}

// CreateTraceComment stores one comment. A comment with no expiry of its own takes its trace's.
func (s *RDBLogStore) CreateTraceComment(ctx context.Context, comment *TraceComment) error {
	if comment.ID == "" {
		return fmt.Errorf("trace comment has no id")
	}
	if comment.CreatedAt.IsZero() {
		comment.CreatedAt = time.Now().UTC()
	}
	if comment.ExpiresAt == nil {
		key := TraceKey{ProjectID: comment.ProjectID, TraceID: comment.TraceID}
		comment.ExpiresAt = s.traceExpiry(ctx, map[TraceKey]struct{}{key: {}})[key]
	}
	return s.traceUpsert(s.db.WithContext(ctx), &TraceComment{}, []string{"id"}).Create(comment).Error
}

// ListTraceComments returns every comment of one trace, oldest first.
func (s *RDBLogStore) ListTraceComments(ctx context.Context, projectID, traceID string) ([]*TraceComment, error) {
	var comments []*TraceComment
	err := s.db.WithContext(ctx).Where("project_id = ? AND trace_id = ?", projectID, traceID).Order("created_at ASC, id ASC").Find(&comments).Error
	if err != nil {
		return nil, err
	}
	return comments, nil
}

// DeleteTraceComment removes one comment of a project.
func (s *RDBLogStore) DeleteTraceComment(ctx context.Context, projectID, id string) error {
	_, err := s.traceDelete(ctx, "trace_comments", "project_id = ? AND id = ?", projectID, id)
	return err
}

// CreateTraceUpload records an upload an SDK is about to send.
func (s *RDBLogStore) CreateTraceUpload(ctx context.Context, upload *TraceUpload) error {
	if upload.ID == "" {
		return fmt.Errorf("trace upload has no id")
	}
	if upload.CreatedAt.IsZero() {
		upload.CreatedAt = time.Now().UTC()
	}
	if upload.Status == "" {
		upload.Status = TraceUploadStatusPending
	}
	upload.SizeBytes = max(upload.SizeBytes, int64(len(upload.Data)))
	return s.traceUpsert(s.db.WithContext(ctx), &TraceUpload{}, []string{"id"}).Create(upload).Error
}

// FindTraceUpload loads one upload, ErrNotFound when it does not exist.
func (s *RDBLogStore) FindTraceUpload(ctx context.Context, id string) (*TraceUpload, error) {
	var uploads []TraceUpload
	if err := s.db.WithContext(ctx).Where("id = ?", id).Limit(1).Find(&uploads).Error; err != nil {
		return nil, err
	}
	if len(uploads) == 0 {
		return nil, ErrNotFound
	}
	return &uploads[0], nil
}

// CompleteTraceUpload settles an upload: its status, and where its bytes are. The bytes are either
// in the object store under objectKey or in data, kept on the row.
func (s *RDBLogStore) CompleteTraceUpload(ctx context.Context, id, status, objectKey string, data []byte) error {
	upload, err := s.FindTraceUpload(ctx, id)
	if err != nil {
		return err
	}
	upload.Status, upload.ObjectKey, upload.Data = status, objectKey, data
	if len(data) > 0 {
		upload.SizeBytes = int64(len(data))
	}
	return s.saveTraceUpload(ctx, upload)
}

// CompleteTraceUploadRow rewrites an upload row whole.
func (s *RDBLogStore) CompleteTraceUploadRow(ctx context.Context, upload *TraceUpload) error {
	return s.saveTraceUpload(ctx, upload)
}

func (s *RDBLogStore) saveTraceUpload(ctx context.Context, upload *TraceUpload) error {
	if s.replacesOnInsert() {
		return s.db.WithContext(ctx).Create(upload).Error
	}
	return s.db.WithContext(ctx).Save(upload).Error
}

// ErrTraceUploadsUnavailable is returned when the store has nowhere to keep upload bytes: ClickHouse
// with no object store configured.
var ErrTraceUploadsUnavailable = fmt.Errorf("trace uploads need an object store on this log store")

// TraceUploadsAvailable reports whether upload bytes can be kept on the row. ClickHouse is a poor
// home for blobs, so there uploads need an object store.
func (s *RDBLogStore) TraceUploadsAvailable() bool {
	return !s.replacesOnInsert()
}

// SaveTraceUploadBytes stores an upload's bytes on its row and marks it done.
func (s *RDBLogStore) SaveTraceUploadBytes(ctx context.Context, id string, data []byte) error {
	if !s.TraceUploadsAvailable() {
		return ErrTraceUploadsUnavailable
	}
	return s.CompleteTraceUpload(ctx, id, TraceUploadStatusDone, "", data)
}

// ReadTraceUploadBytes loads an upload and its bytes.
func (s *RDBLogStore) ReadTraceUploadBytes(ctx context.Context, id string) (*TraceUpload, []byte, error) {
	upload, err := s.FindTraceUpload(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return upload, upload.Data, nil
}

// traceChildTables are the tables that hold a trace's rows, the rollup row last so a trace stays
// listed until its rows are gone.
var traceChildTables = []string{"trace_span_parts", "trace_spans", "trace_span_links", "trace_scores", "trace_comments", "trace_dirty_marks", "traces"}

// DeleteTraces removes every row of the named traces. Uploads are keyed by project, not by trace,
// and go by their own expiry.
func (s *RDBLogStore) DeleteTraces(ctx context.Context, keys []TraceKey) error {
	_, err := s.deleteTraceRows(ctx, keys)
	return err
}

func (s *RDBLogStore) deleteTraceRows(ctx context.Context, keys []TraceKey) (int64, error) {
	var deleted int64
	for _, key := range keys {
		for _, table := range traceChildTables {
			count, err := s.traceDelete(ctx, table, "project_id = ? AND trace_id = ?", key.ProjectID, key.TraceID)
			if err != nil {
				return deleted, fmt.Errorf("failed to delete %s of trace %s: %w", table, key.TraceID, err)
			}
			if table != "trace_dirty_marks" {
				deleted += count
			}
		}
	}
	return deleted, nil
}

// DeleteExpiredTraceRowsBatch deletes trace rows whose own expires_at has passed and reports how
// many. Each row carries its expiry because retention is set per project. ClickHouse expires the
// same rows with a TTL on the column, so there this deletes nothing.
func (s *RDBLogStore) DeleteExpiredTraceRowsBatch(ctx context.Context, now time.Time, batchSize int) (int64, error) {
	if s.replacesOnInsert() || batchSize <= 0 {
		return 0, nil
	}
	var deleted int64
	// Parts, scores, comments and uploads have a single-column key and expire on their own.
	for _, table := range []string{"trace_span_parts", "trace_scores", "trace_comments", "trace_uploads"} {
		count, err := s.deleteExpiredBatch(ctx, table, "expires_at", now, batchSize)
		if err != nil {
			return deleted, err
		}
		deleted += count
	}
	// Spans and links go with their trace. A trace expires with its longest-lived span.
	var keys []TraceKey
	if err := s.db.WithContext(ctx).Model(&Trace{}).Select("project_id", "trace_id").
		Where("expires_at < ?", now).Order("expires_at ASC").Limit(max(batchSize/50, 1)).Find(&keys).Error; err != nil {
		return deleted, err
	}
	count, err := s.deleteTraceRows(ctx, keys)
	return deleted + count, err
}

// TraceSpanEnrichment is what Bifrost adds to a span after the fold: the gateway link and a cost
// estimate. It is written as a column update, never as a rewrite of the span, so it cannot undo a
// part folded in the meantime. A nil field is left as stored.
type TraceSpanEnrichment struct {
	LinkMethod       *string
	LinkSnapshot     *TraceLinkSnapshot
	LinkedLogExpired *bool
	Cost             *float64
	CostEstimated    *bool
	Provider         *string
}

// UpdateTraceSpanEnrichment writes the given enrichment fields onto one span.
func (s *RDBLogStore) UpdateTraceSpanEnrichment(ctx context.Context, projectID, traceID, spanID string, enrichment TraceSpanEnrichment) error {
	updates := map[string]any{}
	if enrichment.LinkMethod != nil {
		updates["link_method"] = *enrichment.LinkMethod
	}
	if enrichment.LinkSnapshot != nil {
		data, err := schemas.MarshalSorted(enrichment.LinkSnapshot)
		if err != nil {
			return fmt.Errorf("failed to serialize trace link snapshot: %w", err)
		}
		updates["link_snapshot"] = string(data)
	}
	if enrichment.LinkedLogExpired != nil {
		updates["linked_log_expired"] = *enrichment.LinkedLogExpired
	}
	if enrichment.Cost != nil {
		updates["cost"] = *enrichment.Cost
	}
	if enrichment.CostEstimated != nil {
		updates["cost_estimated"] = *enrichment.CostEstimated
	}
	if enrichment.Provider != nil {
		updates["provider"] = *enrichment.Provider
	}
	if len(updates) == 0 {
		return nil
	}
	db := s.db.WithContext(ctx)
	if !s.replacesOnInsert() {
		updates["updated_at"] = time.Now().UTC()
		return db.Model(&TraceSpan{}).Where("project_id = ? AND trace_id = ? AND span_id = ?", projectID, traceID, spanID).Updates(updates).Error
	}
	// ClickHouse has no column update that is cheap: read the row, patch it, insert it again.
	found, err := s.FindTraceSpans(ctx, projectID, []TraceSpanKey{{TraceID: traceID, SpanID: spanID}})
	if err != nil || len(found) == 0 {
		return err
	}
	span := found[0]
	if enrichment.LinkMethod != nil {
		span.LinkMethod = *enrichment.LinkMethod
	}
	if enrichment.LinkSnapshot != nil {
		span.LinkSnapshotParsed = enrichment.LinkSnapshot
	}
	if enrichment.LinkedLogExpired != nil {
		span.LinkedLogExpired = *enrichment.LinkedLogExpired
	}
	if enrichment.Cost != nil {
		span.Cost = enrichment.Cost
	}
	if enrichment.CostEstimated != nil {
		span.CostEstimated = *enrichment.CostEstimated
	}
	if enrichment.Provider != nil {
		span.Provider = *enrichment.Provider
	}
	return db.Create(span).Error
}

// GatewayCall is the part of a gateway LLM or MCP row the trace matcher reads.
type GatewayCall struct {
	LogKind            string    `json:"log_kind"` // llm | mcp
	ID                 string    `json:"id"`
	ParentRequestID    *string   `json:"parent_request_id,omitempty"`
	Timestamp          time.Time `json:"timestamp"`
	Provider           string    `json:"provider,omitempty"`
	Model              string    `json:"model,omitempty"` // the tool name for an MCP row
	Status             string    `json:"status"`
	TraceID            *string   `json:"trace_id,omitempty"`
	ParentSpanID       *string   `json:"parent_span_id,omitempty"`
	ProviderResponseID *string   `json:"provider_response_id,omitempty"`
	InputFingerprint   *string   `json:"input_fingerprint,omitempty"`
	VirtualKeyID       *string   `json:"virtual_key_id,omitempty"`
	PromptTokens       int       `json:"prompt_tokens"`
	CompletionTokens   int       `json:"completion_tokens"`
	TotalTokens        int       `json:"total_tokens"`
	CachedReadTokens   int       `json:"cached_read_tokens"`
	Cost               *float64  `json:"cost,omitempty"`
	Latency            *float64  `json:"latency,omitempty"`
}

const (
	gatewayLLMCallColumns = "'llm' AS log_kind, id, parent_request_id, timestamp, provider, model, status, trace_id, parent_span_id, provider_response_id, input_fingerprint, virtual_key_id, prompt_tokens, completion_tokens, total_tokens, cached_read_tokens, cost, latency"
	gatewayMCPCallColumns = "'mcp' AS log_kind, id, timestamp, tool_name AS model, status, trace_id, parent_span_id, virtual_key_id, cost, latency"
	// gatewayCallLimit bounds one matcher lookup. A trace with more gateway calls than this links
	// the first ones and leaves the rest unlinked.
	gatewayCallLimit = 2000
)

// FindGatewayCallsByTraceID returns the gateway LLM and MCP rows that named this trace on their
// request, oldest first.
func (s *RDBLogStore) FindGatewayCallsByTraceID(ctx context.Context, traceID string) ([]*GatewayCall, error) {
	if traceID == "" {
		return nil, nil
	}
	var calls []*GatewayCall
	if err := s.db.WithContext(ctx).Table("logs").Select(gatewayLLMCallColumns).Where("trace_id = ?", traceID).Order("timestamp ASC").Limit(gatewayCallLimit).Scan(&calls).Error; err != nil {
		return nil, err
	}
	var tools []*GatewayCall
	if err := s.db.WithContext(ctx).Table("mcp_tool_logs").Select(gatewayMCPCallColumns).Where("trace_id = ?", traceID).Order("timestamp ASC").Limit(gatewayCallLimit).Scan(&tools).Error; err != nil {
		return nil, err
	}
	return append(calls, tools...), nil
}

// FindGatewayCallsByProviderResponseIDs returns the gateway LLM rows whose provider gave one of
// these response ids.
func (s *RDBLogStore) FindGatewayCallsByProviderResponseIDs(ctx context.Context, ids []string) ([]*GatewayCall, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var calls []*GatewayCall
	err := s.db.WithContext(ctx).Table("logs").Select(gatewayLLMCallColumns).Where("provider_response_id IN ?", ids).Limit(gatewayCallLimit).Scan(&calls).Error
	return calls, err
}

// FindGatewayCallsByFingerprint returns the gateway LLM rows in a time window whose last user
// message has this fingerprint, limited to the given virtual keys when any are named.
func (s *RDBLogStore) FindGatewayCallsByFingerprint(ctx context.Context, fingerprint string, from, to time.Time, virtualKeyIDs []string) ([]*GatewayCall, error) {
	if fingerprint == "" {
		return nil, nil
	}
	query := s.db.WithContext(ctx).Table("logs").Select(gatewayLLMCallColumns).
		Where("timestamp >= ? AND timestamp <= ? AND input_fingerprint = ?", from, to, fingerprint)
	if len(virtualKeyIDs) > 0 {
		query = query.Where("virtual_key_id IN ?", virtualKeyIDs)
	}
	var calls []*GatewayCall
	err := query.Order("timestamp ASC").Limit(50).Scan(&calls).Error
	return calls, err
}

// FindGatewayCallChain returns the given gateway rows together with the fallback and retry
// attempts that hang off them, which name the first attempt as their parent request.
func (s *RDBLogStore) FindGatewayCallChain(ctx context.Context, logKind string, ids []string) ([]*GatewayCall, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var calls []*GatewayCall
	if logKind == TraceLinkedLogKindMCP {
		err := s.db.WithContext(ctx).Table("mcp_tool_logs").Select(gatewayMCPCallColumns).Where("id IN ?", ids).Limit(gatewayCallLimit).Scan(&calls).Error
		return calls, err
	}
	err := s.db.WithContext(ctx).Table("logs").Select(gatewayLLMCallColumns).
		Where("id IN ? OR parent_request_id IN ?", ids, ids).Order("timestamp ASC").Limit(gatewayCallLimit).Scan(&calls).Error
	return calls, err
}

// RegisterTraceExperiment records an experiment. Registering one that exists keeps the stored row:
// an SDK registers its experiment on every run of a script.
func (s *RDBLogStore) RegisterTraceExperiment(ctx context.Context, experiment *TraceExperiment) error {
	if experiment.CreatedAt.IsZero() {
		experiment.CreatedAt = time.Now().UTC()
	}
	if s.replacesOnInsert() {
		if _, err := s.FindTraceExperiment(ctx, experiment.ProjectID, experiment.ID); err == nil {
			return nil
		} else if err != ErrNotFound {
			return err
		}
	}
	return s.traceUpsert(s.db.WithContext(ctx), &TraceExperiment{}, []string{"project_id", "id"}).Create(experiment).Error
}

// FindTraceExperiment loads one experiment, ErrNotFound when it does not exist.
func (s *RDBLogStore) FindTraceExperiment(ctx context.Context, projectID, id string) (*TraceExperiment, error) {
	var experiments []TraceExperiment
	if err := s.db.WithContext(ctx).Where("project_id = ? AND id = ?", projectID, id).Limit(1).Find(&experiments).Error; err != nil {
		return nil, err
	}
	if len(experiments) == 0 {
		return nil, ErrNotFound
	}
	return &experiments[0], nil
}

// ListTraceExperiments lists experiments, newest first. An empty project list means every project.
func (s *RDBLogStore) ListTraceExperiments(ctx context.Context, projectIDs []string, limit int) ([]*TraceExperiment, error) {
	query := s.db.WithContext(ctx)
	if len(projectIDs) > 0 {
		query = query.Where("project_id IN ?", projectIDs)
	}
	var experiments []*TraceExperiment
	err := query.Order("created_at DESC, id DESC").Limit(limit).Find(&experiments).Error
	return experiments, err
}
