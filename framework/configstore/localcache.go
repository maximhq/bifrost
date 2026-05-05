package configstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// LocalCacheConfig is the runtime configuration for the local cache plugin.
// The framework holds a single *LocalCacheConfig that the plugin shares by
// pointer. PUT /api/local-cache/config mutates the struct in place via
// ReloadLocalCacheConfigFromConfigStore so the plugin sees fresh values on
// the next request without a restart.
type LocalCacheConfig struct {
	// Embedding model settings.
	// Modes:
	//   - Semantic mode: Provider + EmbeddingModel + Dimension > 0. Both
	//     direct hash matching and embedding-based similarity search engage.
	//   - Direct-only mode: Provider="" with Dimension=1. Semantic search is
	//     disabled; lookups go through the deterministic direct hash path.
	//     Dimension=1 keeps stores that require a vector happy.
	Provider       schemas.ModelProvider `json:"provider"`
	EmbeddingModel string                `json:"embedding_model,omitempty"`

	// Plugin behavior settings
	CleanUpOnShutdown    bool          `json:"cleanup_on_shutdown,omitempty"`
	TTL                  time.Duration `json:"ttl,omitempty"`
	Threshold            float64       `json:"threshold,omitempty"`
	VectorStoreNamespace string        `json:"vector_store_namespace,omitempty"`
	Dimension            int           `json:"dimension"`

	// Advanced caching behavior
	DefaultCacheKey              string `json:"default_cache_key,omitempty"`
	ConversationHistoryThreshold int    `json:"conversation_history_threshold,omitempty"`
	CacheByModel                 *bool  `json:"cache_by_model,omitempty"`
	CacheByProvider              *bool  `json:"cache_by_provider,omitempty"`
	ExcludeSystemPrompt          *bool  `json:"exclude_system_prompt,omitempty"`

	// ConfigHash is used by the config-sync layer to detect changes between
	// config.json and the database; not serialized to API responses.
	ConfigHash string `json:"-"`
}

// UnmarshalJSON accepts either a duration string ("1m", "1h") or a JSON
// number (seconds) for the TTL field. Mirrors the prior plugin Config
// UnmarshalJSON so existing config.json files continue to parse after the
// rename to local_cache.
func (c *LocalCacheConfig) UnmarshalJSON(data []byte) error {
	// alias suppresses LocalCacheConfig's UnmarshalJSON to avoid infinite
	// recursion. The outer TTL (json.RawMessage) shadows alias.TTL because
	// the json package picks the shallower field on a name conflict.
	type alias LocalCacheConfig
	aux := &struct {
		TTL json.RawMessage `json:"ttl,omitempty"`
		*alias
	}{alias: (*alias)(c)}
	if err := json.Unmarshal(data, aux); err != nil {
		return fmt.Errorf("failed to unmarshal local cache config: %w", err)
	}
	if len(aux.TTL) == 0 || string(aux.TTL) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(aux.TTL, &s); err == nil {
		d, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("failed to parse TTL duration string '%s': %w", s, err)
		}
		c.TTL = d
	} else {
		var seconds float64
		if err := json.Unmarshal(aux.TTL, &seconds); err != nil {
			return fmt.Errorf("unsupported TTL value: %s", string(aux.TTL))
		}
		c.TTL = time.Duration(seconds * float64(time.Second))
	}
	if c.TTL < 0 {
		return fmt.Errorf("TTL must be non-negative, got %v", c.TTL)
	}
	return nil
}

// GenerateLocalCacheConfigHash generates a SHA256 hash of the local cache
// configuration. Used by the config-sync layer to detect when the
// config.json-side LocalCacheConfig differs from what's persisted in the
// database.
func (c *LocalCacheConfig) GenerateLocalCacheConfigHash() (string, error) {
	hash := sha256.New()
	hash.Write([]byte("provider:" + string(c.Provider)))
	hash.Write([]byte("embedding_model:" + c.EmbeddingModel))
	hash.Write([]byte("cleanup_on_shutdown:" + strconv.FormatBool(c.CleanUpOnShutdown)))
	hash.Write([]byte("ttl_ns:" + strconv.FormatInt(int64(c.TTL), 10)))
	hash.Write([]byte("threshold:" + strconv.FormatFloat(c.Threshold, 'f', -1, 64)))
	hash.Write([]byte("namespace:" + c.VectorStoreNamespace))
	hash.Write([]byte("dimension:" + strconv.Itoa(c.Dimension)))
	hash.Write([]byte("default_cache_key:" + c.DefaultCacheKey))
	hash.Write([]byte("conv_history_threshold:" + strconv.Itoa(c.ConversationHistoryThreshold)))
	if c.CacheByModel != nil {
		hash.Write([]byte("cache_by_model:" + strconv.FormatBool(*c.CacheByModel)))
	}
	if c.CacheByProvider != nil {
		hash.Write([]byte("cache_by_provider:" + strconv.FormatBool(*c.CacheByProvider)))
	}
	if c.ExcludeSystemPrompt != nil {
		hash.Write([]byte("exclude_system_prompt:" + strconv.FormatBool(*c.ExcludeSystemPrompt)))
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
