package tables

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// TableWebhookEndpoint validation tests
// ============================================================================

func validatableWebhookEndpoint(name string) *TableWebhookEndpoint {
	return &TableWebhookEndpoint{
		Name:   name,
		URL:    "https://93.184.216.34/hook",
		Events: []WebhookEvent{WebhookEventAsyncJobCompleted},
	}
}

func TestValidateWebhookEndpointURL(t *testing.T) {
	// IP-literal hosts keep these cases deterministic (no DNS lookups).
	tests := []struct {
		name                string
		url                 string
		allowPrivateNetwork bool
		wantErr             string
	}{
		{"https public", "https://93.184.216.34/hook", false, ""},
		{"http public denied", "http://93.184.216.34/hook", false, "must use https"},
		{"http allowed with private network flag", "http://10.1.2.3/hook", true, ""},
		{"https private denied without flag", "https://10.1.2.3/hook", false, "private IP"},
		{"link-local blocked despite flag", "https://169.254.169.254/hook", true, "link-local"},
		{"metadata endpoint blocked", "https://169.254.169.254/latest/meta-data", false, "link-local"},
		{"credentials rejected", "https://user:pw@93.184.216.34/hook", false, "credentials"},
		{"fragment rejected", "https://93.184.216.34/hook#section", false, "fragment"},
		{"unsupported scheme", "ftp://93.184.216.34/hook", false, "only https and http"},
		{"empty", "", false, "cannot be empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateWebhookEndpointURL(tt.url, tt.allowPrivateNetwork)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestTableWebhookEndpointValidate(t *testing.T) {
	t.Run("nil endpoint", func(t *testing.T) {
		var endpoint *TableWebhookEndpoint
		assert.Error(t, endpoint.Validate())
	})

	t.Run("empty name", func(t *testing.T) {
		endpoint := validatableWebhookEndpoint("")
		assert.ErrorContains(t, endpoint.Validate(), "name cannot be empty")
	})

	t.Run("invalid url", func(t *testing.T) {
		endpoint := validatableWebhookEndpoint("bad-url")
		endpoint.URL = "http://93.184.216.34/hook"
		assert.ErrorContains(t, endpoint.Validate(), "must use https")
	})

	t.Run("no events", func(t *testing.T) {
		endpoint := validatableWebhookEndpoint("no-events")
		endpoint.Events = nil
		assert.ErrorContains(t, endpoint.Validate(), "at least one event")
	})

	t.Run("unknown event", func(t *testing.T) {
		endpoint := validatableWebhookEndpoint("bad-event")
		endpoint.Events = []WebhookEvent{"async_job.started"}
		assert.ErrorContains(t, endpoint.Validate(), "unknown webhook event")
	})

	t.Run("duplicate event", func(t *testing.T) {
		endpoint := validatableWebhookEndpoint("dup-event")
		endpoint.Events = []WebhookEvent{WebhookEventAsyncJobCompleted, WebhookEventAsyncJobCompleted}
		assert.ErrorContains(t, endpoint.Validate(), "duplicate webhook event")
	})

	t.Run("valid", func(t *testing.T) {
		endpoint := validatableWebhookEndpoint("valid")
		endpoint.Events = []WebhookEvent{WebhookEventAsyncJobCompleted, WebhookEventAsyncJobFailed}
		assert.NoError(t, endpoint.Validate())
	})
}

// ============================================================================
// TableWebhookEndpoint encryption tests
// ============================================================================

func TestTableWebhookEndpoint_EncryptDecrypt(t *testing.T) {
	db := setupTestDB(t)

	endpoint := &TableWebhookEndpoint{
		ID:     "wh-encrypt-1",
		Name:   "encrypt-test",
		URL:    "https://receiver.example.com/hook",
		Secret: &schemas.SecretVar{Val: "whsec_current_secret_value"},
		Events: []WebhookEvent{WebhookEventAsyncJobCompleted, WebhookEventAsyncJobFailed},
	}
	require.NoError(t, db.Create(endpoint).Error)

	// The stored row must hold ciphertext, never the plaintext secret.
	row := rawRow(t, db, "config_webhook_endpoints", "wh-encrypt-1")
	assert.Equal(t, EncryptionStatusEncrypted, row["encryption_status"])
	assert.NotEmpty(t, row["secret"])
	assert.NotEqual(t, "whsec_current_secret_value", row["secret"])

	// Reading back through GORM decrypts and restores the runtime fields.
	var fetched TableWebhookEndpoint
	require.NoError(t, db.First(&fetched, "id = ?", "wh-encrypt-1").Error)
	assert.Equal(t, "whsec_current_secret_value", secretVarPtrValue(fetched.Secret))
	assert.Equal(t, []WebhookEvent{WebhookEventAsyncJobCompleted, WebhookEventAsyncJobFailed}, fetched.Events)
}

func TestTableWebhookEndpoint_SecretsExcludedFromJSON(t *testing.T) {
	db := setupTestDB(t)

	endpoint := &TableWebhookEndpoint{
		ID:     "wh-json-1",
		Name:   "json-test",
		URL:    "https://receiver.example.com/hook",
		Secret: &schemas.SecretVar{Val: "whsec_must_not_leak"},
		Events: []WebhookEvent{WebhookEventAsyncJobCompleted},
	}
	require.NoError(t, db.Create(endpoint).Error)

	var fetched TableWebhookEndpoint
	require.NoError(t, db.First(&fetched, "id = ?", "wh-json-1").Error)

	data, err := json.Marshal(fetched)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(data), "whsec_"), "serialized endpoint must not contain secret material: %s", data)
	assert.Contains(t, string(data), "async_job.completed")
}

func TestTableWebhookEndpoint_EnvRefSecretNotEncrypted(t *testing.T) {
	db := setupTestDB(t)

	t.Setenv("TEST_WEBHOOK_SECRET", "whsec_from_env")
	endpoint := &TableWebhookEndpoint{
		ID:     "wh-env-1",
		Name:   "env-ref-test",
		URL:    "https://receiver.example.com/hook",
		Secret: schemas.NewSecretVar("env.TEST_WEBHOOK_SECRET"),
		Events: []WebhookEvent{WebhookEventAsyncJobFailed},
	}
	require.NoError(t, db.Create(endpoint).Error)

	// Env-referenced secrets store the reference, not a resolved (or encrypted) value.
	row := rawRow(t, db, "config_webhook_endpoints", "wh-env-1")
	assert.Equal(t, "env.TEST_WEBHOOK_SECRET", row["secret"])

	var fetched TableWebhookEndpoint
	require.NoError(t, db.First(&fetched, "id = ?", "wh-env-1").Error)
	require.NotNil(t, fetched.Secret)
	assert.True(t, fetched.Secret.IsFromSecret())
	assert.Equal(t, "whsec_from_env", fetched.Secret.GetValue())
}

func TestTableWebhookEndpoint_NilEventsStoredAsEmptyList(t *testing.T) {
	db := setupTestDB(t)

	endpoint := &TableWebhookEndpoint{
		ID:     "wh-events-1",
		Name:   "events-test",
		URL:    "https://receiver.example.com/hook",
		Secret: &schemas.SecretVar{Val: "whsec_x"},
	}
	require.NoError(t, db.Create(endpoint).Error)

	row := rawRow(t, db, "config_webhook_endpoints", "wh-events-1")
	assert.Equal(t, "[]", row["events_json"])
}

func TestWebhookEndpointHeaderValidation(t *testing.T) {
	endpoint := validatableWebhookEndpoint("header-test")
	endpoint.Headers = map[string]schemas.SecretVar{"Authorization": {Val: "Bearer x"}}
	require.NoError(t, endpoint.Validate())

	for name, headers := range map[string]map[string]schemas.SecretVar{
		"reserved signing header": {"Webhook-Signature": {Val: "x"}},
		"reserved content type":   {"Content-Type": {Val: "x"}},
		"invalid name":            {"bad header": {Val: "x"}},
		"empty name":              {"": {Val: "x"}},
	} {
		endpoint := validatableWebhookEndpoint("header-test")
		endpoint.Headers = headers
		assert.Error(t, endpoint.Validate(), name)
	}
}

func TestTableWebhookEndpoint_HeadersEncryptDecrypt(t *testing.T) {
	db := setupTestDB(t)

	endpoint := &TableWebhookEndpoint{
		ID:     "wh-headers-1",
		Name:   "headers-test",
		URL:    "https://receiver.example.com/hook",
		Events: []WebhookEvent{WebhookEventAsyncJobCompleted},
		Headers: map[string]schemas.SecretVar{
			"Authorization": {Val: "Bearer receiver-token"},
			"X-Env-Header":  *schemas.NewSecretVar("env.WEBHOOK_HEADER_TOKEN"),
		},
	}
	require.NoError(t, db.Create(endpoint).Error)

	// The stored column must hold ciphertext, never plaintext header values.
	row := rawRow(t, db, "config_webhook_endpoints", "wh-headers-1")
	headersColumn, _ := row["headers_json"].(string)
	require.NotEmpty(t, headersColumn)
	assert.NotContains(t, headersColumn, "receiver-token")
	assert.NotContains(t, headersColumn, "Authorization")

	// Reading back decrypts values and keeps env references as references.
	var fetched TableWebhookEndpoint
	require.NoError(t, db.First(&fetched, "id = ?", "wh-headers-1").Error)
	require.Len(t, fetched.Headers, 2)
	authHeader := fetched.Headers["Authorization"]
	assert.Equal(t, "Bearer receiver-token", authHeader.GetValue())
	envHeader := fetched.Headers["X-Env-Header"]
	assert.True(t, envHeader.IsFromSecret(), "env references must survive the round-trip as references")
	assert.Equal(t, "env.WEBHOOK_HEADER_TOKEN", envHeader.GetRawRef())
}
