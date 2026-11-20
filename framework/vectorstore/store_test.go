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
		{"weaviate", &Config{Type: VectorStoreTypeWeaviate, Config: WeaviateConfig{Scheme: "https", Host: host("w.example:8080"), APIKey: host("secret")}}, "weaviate://w.example:8080"},
		{"qdrant", &Config{Type: VectorStoreTypeQdrant, Config: QdrantConfig{Host: *host("q.example"), Port: *host("6334"), APIKey: *host("secret")}}, "qdrant://q.example:6334"},
		{"redis", &Config{Type: VectorStoreTypeRedis, Config: RedisConfig{Addr: host("r.example:6379")}}, "redis://r.example:6379"},
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
}
