package tables

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
)

// WebhookEvent identifies a server-side event that can be delivered to a
// registered webhook endpoint.
type WebhookEvent string

const (
	// WebhookEventAsyncJobCompleted fires when an async inference job finishes successfully.
	WebhookEventAsyncJobCompleted WebhookEvent = "async_job.completed"
	// WebhookEventAsyncJobFailed fires when an async inference job reaches a terminal failure.
	WebhookEventAsyncJobFailed WebhookEvent = "async_job.failed"
)

// WebhookEvents lists every supported webhook event.
var WebhookEvents = []WebhookEvent{
	WebhookEventAsyncJobCompleted,
	WebhookEventAsyncJobFailed,
}

// IsValid reports whether e is a supported webhook event.
func (e WebhookEvent) IsValid() bool {
	return slices.Contains(WebhookEvents, e)
}

// TableWebhookEndpoint represents a registered webhook endpoint in the database.
type TableWebhookEndpoint struct {
	ID   string `gorm:"type:varchar(36);primaryKey" json:"id"`
	Name string `gorm:"type:varchar(255);uniqueIndex;not null" json:"name"`
	URL  string `gorm:"type:text;not null" json:"url"`

	// Secret signs outgoing deliveries. Excluded from JSON — API responses
	// expose it only once at creation/rotation time.
	Secret *schemas.SecretVar `gorm:"type:text" json:"-"`

	EventsJSON          string `gorm:"type:text" json:"-"` // JSON serialized []WebhookEvent
	HeadersJSON         string `gorm:"type:text" json:"-"` // JSON serialized map[string]string (encrypted at rest): custom delivery headers
	IncludeResponse     bool   `gorm:"default:false" json:"include_response"`
	AllowPrivateNetwork bool   `gorm:"default:false" json:"allow_private_network"`

	// Per-endpoint delivery tuning. Zero means "use the delivery worker's
	// default" — every knob must be positive when set.
	MaxRetries                 int `gorm:"default:0" json:"max_retries,omitempty"`                                              // Retries after the first delivery attempt (default: 4)
	RetryBackoffInitialSeconds int `gorm:"default:0" json:"retry_backoff_initial_seconds,omitempty"`                            // Delay before the first retry; doubles per retry (default: 30)
	RetryBackoffMaxSeconds     int `gorm:"default:0" json:"retry_backoff_max_seconds,omitempty"`                                // Cap on the per-retry delay (default: 1800)
	AttemptTimeoutSeconds      int `gorm:"default:0" json:"attempt_timeout_seconds,omitempty"`                                  // End-to-end bound for one delivery attempt (default: 10)
	MaxResponsePayloadKBs      int `gorm:"column:max_response_payload_kbs;default:0" json:"max_response_payload_kbs,omitempty"` // Cap for inlined response payloads in KB (default: 256)
	MaxConcurrentDeliveries    int `gorm:"default:0" json:"max_concurrent_deliveries,omitempty"`                                // Concurrent in-flight deliveries to this endpoint per node (default: 10)

	Disabled bool `gorm:"default:false" json:"disabled"`

	ConsecutiveFailures int        `gorm:"default:0" json:"consecutive_failures"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	LastFailureAt       *time.Time `json:"last_failure_at,omitempty"`

	// Config hash is used to detect the changes synced from config.json file
	// Every time we sync the config.json file, we will update the config hash
	ConfigHash string `gorm:"type:varchar(255);null" json:"config_hash"`

	EncryptionStatus string `gorm:"type:varchar(20);default:'plain_text'" json:"-"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`

	// Virtual fields for runtime use (not stored in DB)
	Events  []WebhookEvent               `gorm:"-" json:"events"`
	Headers map[string]schemas.SecretVar `gorm:"-" json:"headers,omitempty"`
}

// TableName sets the table name for the webhook endpoint model
func (TableWebhookEndpoint) TableName() string { return "config_webhook_endpoints" }

// protectedWebhookHeaders are delivery headers callers can never override:
// the Standard Webhooks signing headers plus protocol- and identity-level
// headers the delivery client owns.
var protectedWebhookHeaders = map[string]bool{
	"webhook-id":        true,
	"webhook-timestamp": true,
	"webhook-signature": true,
	"x-bifrost-event":   true,
	"content-type":      true,
	"content-length":    true,
	"user-agent":        true,
	"host":              true,
	"connection":        true,
	"transfer-encoding": true,
}

// IsProtectedWebhookHeader reports whether a custom delivery header name is
// reserved and must not be caller-supplied.
func IsProtectedWebhookHeader(name string) bool {
	return protectedWebhookHeaders[strings.ToLower(name)]
}

// isValidHeaderName reports whether name is a valid HTTP header field name
// (RFC 7230 token characters).
func isValidHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return true
}

// Validate checks the caller-editable fields of a webhook endpoint: name,
// URL, and events. Entry points that accept endpoint definitions (API
// handlers, config file loading) must call this before persisting; the store
// methods themselves do not re-validate.
func (w *TableWebhookEndpoint) Validate() error {
	if w == nil {
		return fmt.Errorf("webhook endpoint cannot be nil")
	}
	if w.Name == "" {
		return fmt.Errorf("webhook endpoint name cannot be empty")
	}
	if err := validateWebhookEndpointURL(w.URL, w.AllowPrivateNetwork); err != nil {
		return err
	}
	if len(w.Events) == 0 {
		return fmt.Errorf("webhook endpoint must subscribe to at least one event")
	}
	seen := make(map[WebhookEvent]bool, len(w.Events))
	for _, event := range w.Events {
		if !event.IsValid() {
			return fmt.Errorf("unknown webhook event %q", event)
		}
		if seen[event] {
			return fmt.Errorf("duplicate webhook event %q", event)
		}
		seen[event] = true
	}
	for name, value := range map[string]int{
		"max_retries":                   w.MaxRetries,
		"retry_backoff_initial_seconds": w.RetryBackoffInitialSeconds,
		"retry_backoff_max_seconds":     w.RetryBackoffMaxSeconds,
		"attempt_timeout_seconds":       w.AttemptTimeoutSeconds,
		"max_response_payload_kbs":      w.MaxResponsePayloadKBs,
		"max_concurrent_deliveries":     w.MaxConcurrentDeliveries,
	} {
		if value < 0 {
			return fmt.Errorf("webhook endpoint %s must be positive when set", name)
		}
	}
	for name := range w.Headers {
		if !isValidHeaderName(name) {
			return fmt.Errorf("invalid webhook header name %q", name)
		}
		if IsProtectedWebhookHeader(name) {
			return fmt.Errorf("webhook header %q is reserved and cannot be overridden", name)
		}
	}
	return nil
}

// validateWebhookEndpointURL validates a webhook delivery URL. HTTPS is
// required unless allowPrivateNetwork is set (which also unlocks private
// address ranges for LAN/cluster receivers); URLs must not carry credentials
// or fragments. Scheme allowlisting and IP-range checks (link-local and
// metadata addresses rejected regardless of allowPrivateNetwork) are
// delegated to bifrost.ValidateExternalURL.
func validateWebhookEndpointURL(rawURL string, allowPrivateNetwork bool) error {
	if rawURL == "" {
		return fmt.Errorf("webhook URL cannot be empty")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid webhook URL: %w", err)
	}
	if parsed.Scheme == "http" && !allowPrivateNetwork {
		return fmt.Errorf("webhook URL must use https (http requires allow_private_network)")
	}
	if parsed.User != nil {
		return fmt.Errorf("webhook URL must not contain credentials")
	}
	if parsed.Fragment != "" {
		return fmt.Errorf("webhook URL must not contain a fragment")
	}
	return bifrost.ValidateExternalURL(rawURL, allowPrivateNetwork)
}

// BeforeSave is a GORM hook that serializes the events list and custom
// headers into their JSON columns and encrypts the sensitive fields before
// writing to the database.
func (w *TableWebhookEndpoint) BeforeSave(tx *gorm.DB) error {
	if w.Events != nil {
		data, err := json.Marshal(w.Events)
		if err != nil {
			return err
		}
		w.EventsJSON = string(data)
	} else {
		w.EventsJSON = "[]"
	}

	if w.Headers != nil {
		headersToSerialize := make(map[string]string, len(w.Headers))
		for key, value := range w.Headers {
			if value.IsFromSecret() {
				headersToSerialize[key] = value.GetRawRef()
			} else {
				headersToSerialize[key] = value.GetValue()
			}
		}
		data, err := json.Marshal(headersToSerialize)
		if err != nil {
			return err
		}
		w.HeadersJSON = string(data)
	} else {
		w.HeadersJSON = "{}"
	}

	// Encrypt sensitive fields after serialization.
	// Always set EncryptionStatus when encryption is enabled so the startup
	// batch pass does not re-process this row indefinitely.
	if encrypt.IsEnabled() {
		if w.Secret != nil {
			// Copy to avoid encrypting the caller's value through the pointer
			secret := *w.Secret
			if err := encryptSecretVar(&secret); err != nil {
				return fmt.Errorf("failed to encrypt webhook secret: %w", err)
			}
			w.Secret = &secret
		}
		if w.HeadersJSON != "" && w.HeadersJSON != "{}" {
			encrypted, err := encrypt.Encrypt(w.HeadersJSON)
			if err != nil {
				return fmt.Errorf("failed to encrypt webhook headers: %w", err)
			}
			w.HeadersJSON = encrypted
		}
		w.EncryptionStatus = EncryptionStatusEncrypted
	}

	return nil
}

// AfterFind is a GORM hook that decrypts the sensitive fields (if encrypted)
// and deserializes the events and headers JSON columns after reading from
// the database.
func (w *TableWebhookEndpoint) AfterFind(tx *gorm.DB) error {
	if w.EncryptionStatus == EncryptionStatusEncrypted {
		if err := decryptSecretVar(w.Secret); err != nil {
			return fmt.Errorf("failed to decrypt webhook secret: %w", err)
		}
		if w.HeadersJSON != "" && w.HeadersJSON != "{}" {
			decrypted, err := encrypt.Decrypt(w.HeadersJSON)
			if err != nil {
				return fmt.Errorf("failed to decrypt webhook headers: %w", err)
			}
			w.HeadersJSON = decrypted
		}
	}
	if w.EventsJSON != "" {
		if err := sonic.Unmarshal([]byte(w.EventsJSON), &w.Events); err != nil {
			return err
		}
	}
	if w.HeadersJSON != "" && w.HeadersJSON != "{}" {
		if err := sonic.Unmarshal([]byte(w.HeadersJSON), &w.Headers); err != nil {
			return err
		}
	}
	return nil
}

// TableWebhookJob is one in-flight webhook delivery in the work queue. A row
// exists only while its delivery is pending or retrying: it is created when a
// subscribed event fires, claimed for each attempt, and deleted once the
// delivery reaches a terminal outcome — so the table's steady-state size is
// the number of concurrent in-flight deliveries. The row id doubles as the
// delivery's stable `webhook-id` header value across attempts and redeliveries.
type TableWebhookJob struct {
	ID         string       `gorm:"type:varchar(36);primaryKey" json:"id"`
	EndpointID string       `gorm:"type:varchar(36);not null;index" json:"endpoint_id"`
	AsyncJobID string       `gorm:"type:varchar(255);not null" json:"async_job_id"`
	Event      WebhookEvent `gorm:"type:varchar(255);not null" json:"event"`

	AttemptCount  int       `gorm:"not null;default:0" json:"attempt_count"`
	NextAttemptAt time.Time `gorm:"not null;index" json:"next_attempt_at"`

	// ClaimedBy and ClaimedUntil form the delivery lease: a row with a live
	// lease is owned by exactly one worker; once the lease expires the row is
	// reclaimable (its owner is presumed dead mid-attempt). ClaimedBy holds
	// the claiming runner's id and is empty in single-node mode, where the
	// lease alone provides the fencing.
	ClaimedBy    string     `gorm:"type:varchar(255);not null;default:''" json:"claimed_by,omitempty"`
	ClaimedUntil *time.Time `json:"claimed_until,omitempty"`

	CreatedAt time.Time `gorm:"not null" json:"created_at"`
}

// TableName sets the table name for the webhook job model
func (TableWebhookJob) TableName() string { return "webhook_jobs" }
