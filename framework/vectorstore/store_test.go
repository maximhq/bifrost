package vectorstore

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
)

// Location names where a store keeps its data and nothing that authenticates
// to it: a rotated key is the same store, a moved host is not.
func TestConfigLocation(t *testing.T) {
	host := func(v string) *schemas.SecretVar { return schemas.NewSecretVar(v) }
	for _, tc := range []struct {
		name   string
		config *Config
		want   string
	}{
		{"nil", nil, ""},
		{"weaviate", &Config{Type: VectorStoreTypeWeaviate, Config: WeaviateConfig{Scheme: "https", Host: host("w.example:8080"), APIKey: host("secret")}}, "weaviate+https://w.example:8080"},
		{"qdrant", &Config{Type: VectorStoreTypeQdrant, Config: QdrantConfig{Host: *host("q.example"), Port: *host("6334"), APIKey: *host("secret")}}, "qdrant://q.example:6334"},
		{"qdrant, port omitted", &Config{Type: VectorStoreTypeQdrant, Config: QdrantConfig{Host: *host("q.example")}}, "qdrant://q.example:6334"},
		{"redis", &Config{Type: VectorStoreTypeRedis, Config: RedisConfig{Addr: host("r.example:6379")}}, "redis://r.example:6379/0"},
		{"redis, database 3", &Config{Type: VectorStoreTypeRedis, Config: RedisConfig{Addr: host("r.example:6379"), DB: host("3")}}, "redis://r.example:6379/3"},
		{"pinecone", &Config{Type: VectorStoreTypePinecone, Config: PineconeConfig{IndexHost: *host("idx.pinecone.io"), APIKey: *host("secret")}}, "pinecone://idx.pinecone.io"},
		{"chromem on disk", &Config{Type: VectorStoreTypeChromem, Config: ChromemConfig{Path: "/data/vectors"}}, "chromem:///data/vectors"},
		{"chromem in memory, no config block", &Config{Type: VectorStoreTypeChromem}, "chromem://"},
		{"config of the wrong shape", &Config{Type: VectorStoreTypeWeaviate, Config: "nope"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.config.Location())
		})
	}

	a := &Config{Type: VectorStoreTypeWeaviate, Config: WeaviateConfig{Host: host("w.example"), APIKey: host("one")}}
	b := &Config{Type: VectorStoreTypeWeaviate, Config: WeaviateConfig{Host: host("w.example"), APIKey: host("two")}}
	assert.Equal(t, a.Location(), b.Location(), "a rotated key is the same store")

	// The client's defaults are part of the location: omitted and explicit are
	// the same endpoint, and a different scheme or database is not.
	omittedPort := &Config{Type: VectorStoreTypeQdrant, Config: QdrantConfig{Host: *host("q.example")}}
	explicitPort := &Config{Type: VectorStoreTypeQdrant, Config: QdrantConfig{Host: *host("q.example"), Port: *host("6334")}}
	assert.Equal(t, omittedPort.Location(), explicitPort.Location())
	omittedDB := &Config{Type: VectorStoreTypeRedis, Config: RedisConfig{Addr: host("r.example:6379")}}
	explicitDB := &Config{Type: VectorStoreTypeRedis, Config: RedisConfig{Addr: host("r.example:6379"), DB: host("0")}}
	otherDB := &Config{Type: VectorStoreTypeRedis, Config: RedisConfig{Addr: host("r.example:6379"), DB: host("1")}}
	assert.Equal(t, omittedDB.Location(), explicitDB.Location())
	assert.NotEqual(t, omittedDB.Location(), otherDB.Location())
	plain := &Config{Type: VectorStoreTypeWeaviate, Config: WeaviateConfig{Scheme: "http", Host: host("w.example")}}
	secure := &Config{Type: VectorStoreTypeWeaviate, Config: WeaviateConfig{Scheme: "https", Host: host("w.example")}}
	assert.NotEqual(t, plain.Location(), secure.Location())

	// One directory written two ways is one store.
	slash := &Config{Type: VectorStoreTypeChromem, Config: ChromemConfig{Path: "/data/vectors/"}}
	plainPath := &Config{Type: VectorStoreTypeChromem, Config: ChromemConfig{Path: "/data/vectors"}}
	dotted := &Config{Type: VectorStoreTypeChromem, Config: ChromemConfig{Path: "/data/./vectors"}}
	assert.Equal(t, plainPath.Location(), slash.Location())
	assert.Equal(t, plainPath.Location(), dotted.Location())

	assert.True(t, (&Config{Type: VectorStoreTypeChromem}).InMemory())
	assert.False(t, (&Config{Type: VectorStoreTypeChromem, Config: ChromemConfig{Path: "/data"}}).InMemory())
	assert.False(t, explicitDB.InMemory())
}
