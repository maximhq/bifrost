package deepseek

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// TestDeepSeekProviderKey verifies the provider key constant.
func TestDeepSeekProviderKey(t *testing.T) {
	if schemas.DeepSeek != "deepseek" {
		t.Errorf("schemas.DeepSeek = %q, want %q", schemas.DeepSeek, "deepseek")
	}
}

// TestDeepSeekInStandardProviders verifies DeepSeek is in the StandardProviders list.
func TestDeepSeekInStandardProviders(t *testing.T) {
	found := false
	for _, p := range schemas.StandardProviders {
		if p == schemas.DeepSeek {
			found = true
			break
		}
	}
	if !found {
		t.Error("schemas.DeepSeek not found in schemas.StandardProviders")
	}
}

// compile-time check that DeepSeekProvider implements schemas.Provider
var _ schemas.Provider = (*DeepSeekProvider)(nil)
