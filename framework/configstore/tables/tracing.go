package tables

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
)

// TracingKeyPrefix starts every tracing key, so a key pasted in the wrong place is recognisable.
const TracingKeyPrefix = "bf-tr-"

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

// TableTracingKey is an ingest-only credential for the /tracing endpoints. It can write traces to
// its bound projects and do nothing else. Only the SHA-256 of the key is stored.
type TableTracingKey struct {
	ID                string  `gorm:"primaryKey;type:varchar(64)" json:"id"`
	Name              string  `gorm:"type:varchar(255);not null" json:"name"`
	KeyHash           string  `gorm:"type:varchar(64);not null;uniqueIndex:idx_tracing_keys_hash" json:"-"`
	KeyPrefix         string  `gorm:"type:varchar(32)" json:"key_prefix"`
	ProjectIDsJSON    *string `gorm:"type:text" json:"-"`
	DefaultProjectID  string  `gorm:"type:varchar(64)" json:"default_project_id,omitempty"`
	VirtualKeyIDsJSON *string `gorm:"type:text" json:"-"`
	LogContent        bool    `gorm:"not null;default:true" json:"log_content"` // false drops span input and output at ingest
	CanCreateProjects bool    `gorm:"not null;default:false" json:"can_create_projects"`
	// CanReadPrompts lets the key fetch prompts from the prompt repository through the SDK prompt
	// routes. It is off by default: a tracing key writes traces and does nothing else.
	CanReadPrompts bool `gorm:"not null;default:false" json:"can_read_prompts"`
	// CanUseDatasets lets the key create evals datasets and add and read their rows through the
	// SDK dataset routes. It is off by default.
	CanUseDatasets bool       `gorm:"not null;default:false" json:"can_use_datasets"`
	Enabled        bool       `gorm:"not null;default:true" json:"enabled"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	CreatedAt      time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt      time.Time  `gorm:"not null" json:"updated_at"`

	ProjectIDs    []string `gorm:"-" json:"project_ids"`
	VirtualKeyIDs []string `gorm:"-" json:"virtual_key_ids,omitempty"`
}

func (TableTracingKey) TableName() string { return "tracing_keys" }

// NewTracingKeyValue generates a tracing key. It is shown to the user once and never stored.
func NewTracingKeyValue() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("failed to generate tracing key: %w", err)
	}
	return TracingKeyPrefix + hex.EncodeToString(raw), nil
}

// HashTracingKey is the stored form of a tracing key.
func HashTracingKey(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// SetValue stores the hash of value and the short prefix shown in the dashboard.
func (k *TableTracingKey) SetValue(value string) {
	k.KeyHash = HashTracingKey(value)
	k.KeyPrefix = value[:min(len(value), len(TracingKeyPrefix)+4)]
}

// AllowsProject reports whether the key may write to the project.
func (k *TableTracingKey) AllowsProject(projectID string) bool {
	return slices.Contains(k.ProjectIDs, projectID)
}

func (k *TableTracingKey) BeforeSave(_ *gorm.DB) error {
	if k.ID == "" {
		k.ID = uuid.NewString()
	}
	k.ProjectIDsJSON, k.VirtualKeyIDsJSON = nil, nil
	for _, column := range []struct {
		values []string
		stored **string
	}{{k.ProjectIDs, &k.ProjectIDsJSON}, {k.VirtualKeyIDs, &k.VirtualKeyIDsJSON}} {
		if len(column.values) == 0 {
			continue
		}
		data, err := schemas.MarshalSorted(column.values)
		if err != nil {
			return fmt.Errorf("failed to serialize tracing key bindings: %w", err)
		}
		*column.stored = new(string(data))
	}
	return nil
}

func (k *TableTracingKey) AfterFind(_ *gorm.DB) error {
	if k.ProjectIDsJSON != nil && *k.ProjectIDsJSON != "" {
		if err := sonic.Unmarshal([]byte(*k.ProjectIDsJSON), &k.ProjectIDs); err != nil {
			return fmt.Errorf("failed to read tracing key projects: %w", err)
		}
	}
	if k.VirtualKeyIDsJSON != nil && *k.VirtualKeyIDsJSON != "" {
		if err := sonic.Unmarshal([]byte(*k.VirtualKeyIDsJSON), &k.VirtualKeyIDs); err != nil {
			return fmt.Errorf("failed to read tracing key virtual keys: %w", err)
		}
	}
	return nil
}
