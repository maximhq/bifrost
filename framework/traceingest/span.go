// Package traceingest receives traces pushed by observability SDKs (Braintrust, Langfuse, Arize,
// any OTLP exporter), folds them into the trace store, and links them to the gateway calls they
// describe. The HTTP adapters that speak each vendor's wire format live in the transport.
package traceingest

import (
	"fmt"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// SpanPart is one received piece of a span, already mapped onto Bifrost's own field names. An SDK
// may send a span whole (OTLP) or as a base row followed by merge rows (Braintrust, Langfuse
// events). An empty field means "this part does not say", never "clear it".
//
// Every vendor attribute with no field of its own is kept in Attributes, so nothing an SDK sent is
// dropped by the mapping.
type SpanPart struct {
	TraceID       string   `json:"trace_id"`
	SpanID        string   `json:"span_id"`
	ParentSpanIDs []string `json:"parent_span_ids,omitempty"`
	RowID         string   `json:"row_id,omitempty"`
	// IsMerge marks a partial update of a span whose base row is sent separately.
	IsMerge bool `json:"is_merge,omitempty"`

	Name      string     `json:"name,omitempty"`
	Kind      string     `json:"kind,omitempty"`
	Status    string     `json:"status,omitempty"`
	Error     string     `json:"error,omitempty"`
	StartTime *time.Time `json:"start_time,omitempty"`
	EndTime   *time.Time `json:"end_time,omitempty"`

	Input    any `json:"input,omitempty"`
	Output   any `json:"output,omitempty"`
	Expected any `json:"expected,omitempty"`

	Metadata   map[string]any `json:"metadata,omitempty"`
	Tags       []string       `json:"tags,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`

	Model             string   `json:"model,omitempty"`
	Provider          string   `json:"provider,omitempty"`
	PromptTokens      int      `json:"prompt_tokens,omitempty"`
	CompletionTokens  int      `json:"completion_tokens,omitempty"`
	TotalTokens       int      `json:"total_tokens,omitempty"`
	CachedReadTokens  int      `json:"cached_read_tokens,omitempty"`
	CachedWriteTokens int      `json:"cached_write_tokens,omitempty"`
	ReasoningTokens   int      `json:"reasoning_tokens,omitempty"`
	Cost              *float64 `json:"cost,omitempty"` // only a cost the SDK itself reported
	TimeToFirstToken  *float64 `json:"time_to_first_token_ms,omitempty"`

	SessionID          string `json:"session_id,omitempty"`
	UserID             string `json:"user_id,omitempty"`
	Environment        string `json:"environment,omitempty"`
	ExperimentID       string `json:"experiment_id,omitempty"`
	DatasetID          string `json:"dataset_id,omitempty"`
	ProviderResponseID string `json:"provider_response_id,omitempty"`

	// Scores and Comments sent on the span row. They become rows of their own.
	Scores   map[string]float64 `json:"scores,omitempty"`
	Comments []string           `json:"comments,omitempty"`

	// ReceivedAt and ID are set from the stored part, not from the payload.
	ReceivedAt time.Time `json:"-"`
	ID         string    `json:"-"`
}

// Validate reports whether the part names the span it belongs to.
func (p *SpanPart) Validate() error {
	if p.TraceID == "" || p.SpanID == "" {
		return fmt.Errorf("span part has no trace id or span id")
	}
	return nil
}

// toStoredPart wraps the part in the row the trace store keeps.
func (p *SpanPart) toStoredPart(projectID, source string, receivedAt time.Time, expiresAt *time.Time) (*logstore.TraceSpanPart, error) {
	payload, err := schemas.MarshalSorted(p)
	if err != nil {
		return nil, fmt.Errorf("failed to encode span part: %w", err)
	}
	stored := &logstore.TraceSpanPart{
		ProjectID: projectID, TraceID: p.TraceID, SpanID: p.SpanID, RowID: p.RowID, Source: source,
		ReceivedAt: receivedAt, IsMerge: p.IsMerge, Payload: ownedString(payload), ExpiresAt: expiresAt,
	}
	stored.ID = stored.ContentID()
	return stored, nil
}

// partFromStored reads a part back from its stored row.
func partFromStored(stored *logstore.TraceSpanPart) (*SpanPart, error) {
	var part SpanPart
	if err := sonic.UnmarshalString(stored.Payload, &part); err != nil {
		return nil, fmt.Errorf("failed to decode span part %s: %w", stored.ID, err)
	}
	part.TraceID, part.SpanID, part.IsMerge = stored.TraceID, stored.SpanID, stored.IsMerge
	part.ReceivedAt, part.ID = stored.ReceivedAt, stored.ID
	return &part, nil
}
