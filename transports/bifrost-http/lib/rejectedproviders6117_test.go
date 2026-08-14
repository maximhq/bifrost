package lib

import (
	"errors"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

// Regression for #6117: a custom provider whose name collides with a built-in
// provider type is rejected at config load, but the provider lookup then
// reported a bare "not found" — nothing pointed at the collision. The recorded
// rejection reason must surface in the lookup error.
func TestGetProviderConfigRawSurfacesLoadRejection(t *testing.T) {
	cfg := &Config{
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{},
		RejectedProviders: map[schemas.ModelProvider]string{
			schemas.DeepSeek: `custom provider validation failed: cannot be created on standard providers: deepseek`,
		},
	}

	_, err := cfg.GetProviderConfigRaw(schemas.DeepSeek)
	if err == nil {
		t.Fatal("expected error for rejected provider")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error must keep ErrNotFound semantics, got %v", err)
	}
	if !strings.Contains(err.Error(), "rejected at config load") ||
		!strings.Contains(err.Error(), "cannot be created on standard providers") {
		t.Errorf("error must carry the rejection reason, got %q", err.Error())
	}

	// Providers never seen in config.json keep the bare ErrNotFound.
	_, err = cfg.GetProviderConfigRaw(schemas.Groq)
	if !errors.Is(err, ErrNotFound) || err.Error() != ErrNotFound.Error() {
		t.Errorf("unknown provider must return bare ErrNotFound, got %v", err)
	}
}

// The load-time validation itself must reject a custom provider config on a
// built-in name with a message naming the collision.
func TestValidateCustomProviderRejectsBuiltinName(t *testing.T) {
	cfg := configstore.ProviderConfig{
		CustomProviderConfig: &schemas.CustomProviderConfig{
			BaseProviderType: schemas.OpenAI,
		},
	}
	err := ValidateCustomProvider(cfg, schemas.DeepSeek)
	if err == nil {
		t.Fatal("expected validation error for built-in name collision")
	}
	if !strings.Contains(err.Error(), "standard providers") {
		t.Errorf("error should name the collision, got %q", err.Error())
	}
}
