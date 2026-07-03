package edenai

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// Compile-time check that edenaiProvider satisfies the full Provider interface.
var _ schemas.Provider = (*edenaiProvider)(nil)

func TestEdenAIProviderConstructorDefaults(t *testing.T) {
	t.Parallel()

	config := &schemas.ProviderConfig{}
	config.CheckAndSetDefaults()
	provider, err := NewEdenAIProvider(config, nil)
	if err != nil {
		t.Fatalf("NewEdenAIProvider failed: %v", err)
	}
	if provider.GetProviderKey() != schemas.EdenAI {
		t.Errorf("expected provider key %s, got %s", schemas.EdenAI, provider.GetProviderKey())
	}
	if provider.networkConfig.BaseURL != "https://api.edenai.run/v3" {
		t.Errorf("expected base URL https://api.edenai.run/v3, got %s", provider.networkConfig.BaseURL)
	}
}

func TestEdenAIUnsupportedOperations(t *testing.T) {
	t.Parallel()

	p := &edenaiProvider{}

	if _, err := p.TextCompletion(nil, schemas.Key{}, nil); err == nil {
		t.Error("expected TextCompletion to be unsupported")
	}
	if _, err := p.Embedding(nil, schemas.Key{}, nil); err == nil {
		t.Error("expected Embedding to be unsupported")
	}
	if _, err := p.Speech(nil, schemas.Key{}, nil); err == nil {
		t.Error("expected Speech to be unsupported")
	}
}
