// Package vectorstore provides a vector store for Bifrost.
package vectorstore

import (
	"encoding/json"
	"fmt"
)

// RedisVectorStoreConfig represents the configuration for the Redis vector store.
type RedisVectorStoreConfig struct {
	Addr     string `json:"addr"`               // Cache server address (host:port)
	Username string `json:"username,omitempty"` // Username for Cache AUTH
	Password string `json:"password,omitempty"` // Password for Cache AUTH
	DB       int    `json:"db"`                 // Cache database number
	Prefix   string `json:"prefix,omitempty"`   // Cache key prefix
}

func (v *Config) UnmarshalJSON(data []byte) error {
	// First unmarshal into a temporary struct to get the type
	type Alias Config
	aux := &struct {
		Config json.RawMessage `json:"config"`
		*Alias
	}{
		Alias: (*Alias)(v),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	// Parse the config based on type
	switch v.Type {
	case "redis":
		var redisConfig RedisVectorStoreConfig
		if err := json.Unmarshal(aux.Config, &redisConfig); err != nil {
			return err
		}
		v.Config = redisConfig
	default:
		return fmt.Errorf("unknown vector store type: %s", v.Type)
	}
	return nil
}

func (v *Config) RedisConfig() (*RedisVectorStoreConfig, error) {
	if v.Type != "redis" {
		return nil, fmt.Errorf("vector store type is not redis")
	}
	return v.Config.(*RedisVectorStoreConfig), nil
}

func newRedisVectorStore(config *RedisVectorStoreConfig) (VectorStore, error) {
	return nil, nil
}
