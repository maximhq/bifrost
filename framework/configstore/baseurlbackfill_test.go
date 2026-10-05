package configstore

import (
	"encoding/json"
	"strings"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// decodeBackfillBaseURL mirrors how add_ollama_sgl_config_columns reads a
// provider's stored base_url, so the decision the migration makes is testable
// without standing up a database.
func decodeBackfillBaseURL(t *testing.T, networkConfigJSON string) (*schemas.SecretVar, bool) {
	t.Helper()
	var stored struct {
		BaseURL *schemas.SecretVar `json:"base_url,omitempty"`
	}
	if err := json.Unmarshal([]byte(networkConfigJSON), &stored); err != nil {
		return nil, true // the migration logs and skips
	}
	skip := stored.BaseURL == nil || (!stored.BaseURL.IsFromSecret() && strings.TrimSpace(stored.BaseURL.GetValue()) == "")
	return stored.BaseURL, skip
}

// TestBaseURLBackfillMigratesUnresolvedSecretReference pins the decode the
// ollama/sgl backfill makes.
//
// It must not go through NetworkConfig.UnmarshalJSON, which fails loud when a
// reference resolves to an empty value. That is the right stance at serve time,
// where the alternative is dialing an empty host, but inside the migration the
// error becomes a skip: no key created, the old JSON left in place, and only a
// log line to say so. An operator whose base_url is env.OLLAMA_URL, with the
// variable absent from the migration's own environment, would silently keep an
// unmigrated provider.
func TestBaseURLBackfillMigratesUnresolvedSecretReference(t *testing.T) {
	t.Run("a reference migrates on the reference alone, resolved or not", func(t *testing.T) {
		// Deliberately not set in the environment.
		baseURL, skip := decodeBackfillBaseURL(t, `{"base_url":"env.BIFROST_TEST_BACKFILL_UNSET"}`)
		require.False(t, skip, "a present secret reference must migrate even when it currently resolves to nothing")
		require.NotNil(t, baseURL)
		assert.Equal(t, "env.BIFROST_TEST_BACKFILL_UNSET", baseURL.GetRawRef(),
			"the reference itself is what gets carried onto the new key, not its resolved value")

		// The old path is the one that would have skipped it.
		var nc schemas.NetworkConfig
		assert.Error(t, json.Unmarshal([]byte(`{"base_url":"env.BIFROST_TEST_BACKFILL_UNSET"}`), &nc),
			"NetworkConfig.UnmarshalJSON is expected to fail loud here; that is why the migration decodes base_url on its own")
	})

	t.Run("a resolved reference migrates", func(t *testing.T) {
		t.Setenv("BIFROST_TEST_BACKFILL_SET", "http://ollama.internal:11434")
		baseURL, skip := decodeBackfillBaseURL(t, `{"base_url":"env.BIFROST_TEST_BACKFILL_SET"}`)
		require.False(t, skip)
		assert.Equal(t, "http://ollama.internal:11434", baseURL.GetValue())
		assert.Equal(t, "env.BIFROST_TEST_BACKFILL_SET", baseURL.GetRawRef())
	})

	t.Run("a plain url migrates", func(t *testing.T) {
		baseURL, skip := decodeBackfillBaseURL(t, `{"base_url":"http://localhost:11434"}`)
		require.False(t, skip)
		assert.Equal(t, "http://localhost:11434", baseURL.GetValue())
	})

	t.Run("an absent or blank plain url is still skipped", func(t *testing.T) {
		for _, cfg := range []string{`{}`, `{"base_url":""}`, `{"base_url":"   "}`} {
			_, skip := decodeBackfillBaseURL(t, cfg)
			assert.True(t, skip, "nothing worth carrying in %s", cfg)
		}
	})
}
