package tables

import (
	"fmt"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
)

// TraceMaskingRule replaces every match of Pattern in a span's input, output and metadata before
// the span is stored.
type TraceMaskingRule struct {
	Name        string `json:"name"`
	Pattern     string `json:"pattern"`
	Replacement string `json:"replacement"`
}

// TraceForwardTarget is an upstream vendor a project's received requests are replayed to.
type TraceForwardTarget struct {
	Vendor  string `json:"vendor"` // braintrust | langfuse | arize | otel
	BaseURL string `json:"base_url"`
	// Headers are sent on every replayed request. For OTLP targets this carries the vendor's auth.
	Headers map[string]string `json:"headers,omitempty"`
	Enabled bool              `json:"enabled"`
}

// TraceEvaluatorRule runs one evaluator on the project's traces as they complete. The evaluator's
// result is stored as a score on the trace.
type TraceEvaluatorRule struct {
	EvaluatorID string `json:"evaluator_id"`
	// Name is the score name. Empty uses the evaluator id.
	Name string `json:"name,omitempty"`
	// SampleRate is the share of traces scored, 0 to 1.
	SampleRate float64 `json:"sample_rate"`
	// DailyCap bounds how many traces a day this rule scores. 0 means no cap.
	DailyCap int  `json:"daily_cap"`
	Enabled  bool `json:"enabled"`
}

// TableTraceProject is one Tracing project: the unit traces are grouped, retained and masked by.
type TableTraceProject struct {
	ID             string    `gorm:"primaryKey;type:varchar(64)" json:"id"`
	Name           string    `gorm:"type:varchar(255);not null;uniqueIndex:idx_trace_projects_name" json:"name"`
	Description    string    `gorm:"type:text" json:"description,omitempty"`
	RetentionDays  *int      `json:"retention_days,omitempty"` // nil = the global log retention
	MaskingJSON    *string   `gorm:"type:text" json:"-"`
	ForwardJSON    *string   `gorm:"type:text" json:"-"`
	EvaluatorsJSON *string   `gorm:"type:text" json:"-"`
	CreatedAt      time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt      time.Time `gorm:"not null" json:"updated_at"`

	MaskingRules []TraceMaskingRule   `gorm:"-" json:"masking_rules,omitempty"`
	Forward      *TraceForwardTarget  `gorm:"-" json:"forward,omitempty"`
	Evaluators   []TraceEvaluatorRule `gorm:"-" json:"evaluators,omitempty"`
}

func (TableTraceProject) TableName() string { return "trace_projects" }

func (p *TableTraceProject) BeforeSave(_ *gorm.DB) error {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	p.MaskingJSON, p.ForwardJSON, p.EvaluatorsJSON = nil, nil, nil
	if len(p.Evaluators) > 0 {
		data, err := schemas.MarshalSorted(p.Evaluators)
		if err != nil {
			return fmt.Errorf("failed to serialize trace evaluator rules: %w", err)
		}
		p.EvaluatorsJSON = new(string(data))
	}
	if len(p.MaskingRules) > 0 {
		data, err := schemas.MarshalSorted(p.MaskingRules)
		if err != nil {
			return fmt.Errorf("failed to serialize trace masking rules: %w", err)
		}
		p.MaskingJSON = new(string(data))
	}
	if p.Forward != nil {
		data, err := schemas.MarshalSorted(p.Forward)
		if err != nil {
			return fmt.Errorf("failed to serialize trace forward target: %w", err)
		}
		stored := string(data)
		// The target's headers hold the user's vendor key, so the column is encrypted whenever
		// encryption is on. The prefix says which form a row is in, so rows written before
		// encryption was enabled still read.
		if encrypt.IsEnabled() {
			ciphertext, err := encrypt.Encrypt(stored)
			if err != nil {
				return fmt.Errorf("failed to encrypt trace forward target: %w", err)
			}
			stored = traceForwardEncryptedPrefix + ciphertext
		}
		p.ForwardJSON = &stored
	}
	return nil
}

// traceForwardEncryptedPrefix marks a forward_json value that is ciphertext.
const traceForwardEncryptedPrefix = "enc:"

func (p *TableTraceProject) AfterFind(_ *gorm.DB) error {
	if p.EvaluatorsJSON != nil && *p.EvaluatorsJSON != "" {
		if err := sonic.Unmarshal([]byte(*p.EvaluatorsJSON), &p.Evaluators); err != nil {
			return fmt.Errorf("failed to read trace evaluator rules: %w", err)
		}
	}
	if p.MaskingJSON != nil && *p.MaskingJSON != "" {
		if err := sonic.Unmarshal([]byte(*p.MaskingJSON), &p.MaskingRules); err != nil {
			return fmt.Errorf("failed to read trace masking rules: %w", err)
		}
	}
	if p.ForwardJSON != nil && *p.ForwardJSON != "" {
		stored := *p.ForwardJSON
		if ciphertext, encrypted := strings.CutPrefix(stored, traceForwardEncryptedPrefix); encrypted {
			plaintext, err := encrypt.Decrypt(ciphertext)
			if err != nil {
				return fmt.Errorf("failed to decrypt trace forward target: %w", err)
			}
			stored = plaintext
		}
		var forward TraceForwardTarget
		if err := sonic.Unmarshal([]byte(stored), &forward); err != nil {
			return fmt.Errorf("failed to read trace forward target: %w", err)
		}
		p.Forward = &forward
	}
	return nil
}
