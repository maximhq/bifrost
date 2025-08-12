package vectorstore

// Config represents the configuration for the vector store.
type Config struct {
	Enabled         bool   `json:"enabled"`           // Enable vector store
	Type            string `json:"type"`              // Vector store type (redis, memory, etc.)
	TTLSeconds      int    `json:"ttl_seconds"`       // TTL in seconds (default: 5 minutes)
	CacheByModel    bool   `json:"cache_by_model"`    // Include model in cache key
	CacheByProvider bool   `json:"cache_by_provider"` // Include provider in cache key
	Config          any
}
