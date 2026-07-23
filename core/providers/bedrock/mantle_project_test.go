package bedrock

import (
	"context"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// TestWithMantleProject verifies the project header is added only when a project ID is present,
// the target header name is honoured, and the base map is never mutated.
func TestWithMantleProject(t *testing.T) {
	t.Run("empty project returns base unchanged", func(t *testing.T) {
		base := map[string]string{"X-Custom": "v"}
		got := WithMantleProject(base, MantleOpenAIProjectHeader, "")
		if _, ok := got[MantleOpenAIProjectHeader]; ok {
			t.Fatalf("expected no project header when project ID is empty, got %v", got)
		}
		// Empty project must return the base map as-is (default-project behaviour).
		if len(got) != 1 || got["X-Custom"] != "v" {
			t.Fatalf("expected base returned unchanged, got %v", got)
		}
	})

	t.Run("OpenAI project header set", func(t *testing.T) {
		base := map[string]string{"X-Custom": "v"}
		got := WithMantleProject(base, MantleOpenAIProjectHeader, "proj_abc")
		if got[MantleOpenAIProjectHeader] != "proj_abc" {
			t.Fatalf("expected %s=proj_abc, got %v", MantleOpenAIProjectHeader, got)
		}
		if got["X-Custom"] != "v" {
			t.Fatalf("existing headers must be preserved, got %v", got)
		}
		// base must not be mutated.
		if _, ok := base[MantleOpenAIProjectHeader]; ok {
			t.Fatalf("base map was mutated: %v", base)
		}
	})

	t.Run("Anthropic workspace header set", func(t *testing.T) {
		got := WithMantleProject(nil, MantleAnthropicProjectHeader, "proj_xyz")
		if got[MantleAnthropicProjectHeader] != "proj_xyz" {
			t.Fatalf("expected %s=proj_xyz, got %v", MantleAnthropicProjectHeader, got)
		}
	})

	t.Run("nil base with empty project stays nil", func(t *testing.T) {
		if got := WithMantleProject(nil, MantleOpenAIProjectHeader, ""); got != nil {
			t.Fatalf("expected nil when base is nil and project is empty, got %v", got)
		}
	})
}

// TestResolveMantleProjectID verifies precedence: per-alias AliasConfig.ProjectID
// overrides the key-level BedrockKeyConfig.ProjectID.
func TestResolveMantleProjectID(t *testing.T) {
	tests := []struct {
		name  string
		alias *schemas.AliasConfig // nil = no resolved alias in context
		key   schemas.Key
		want  string
	}{
		{name: "no bedrock config", key: schemas.Key{}, want: ""},
		{name: "config without project", key: schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{}}, want: ""},
		{
			name: "key-level project",
			key:  schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{ProjectID: schemas.NewSecretVar("proj_key")}},
			want: "proj_key",
		},
		{
			name:  "alias override wins over key",
			alias: &schemas.AliasConfig{ModelID: "chirp", ProjectID: schemas.NewSecretVar("proj_alias")},
			key:   schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{ProjectID: schemas.NewSecretVar("proj_key")}},
			want:  "proj_alias",
		},
		{
			name:  "empty alias override falls back to key",
			alias: &schemas.AliasConfig{ModelID: "chirp", ProjectID: schemas.NewSecretVar("")},
			key:   schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{ProjectID: schemas.NewSecretVar("proj_key")}},
			want:  "proj_key",
		},
		{
			name:  "alias override with no key config",
			alias: &schemas.AliasConfig{ModelID: "chirp", ProjectID: schemas.NewSecretVar("proj_alias")},
			key:   schemas.Key{},
			want:  "proj_alias",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			defer ctx.Cancel()
			if tt.alias != nil {
				ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{Key: "chirp", Config: tt.alias})
			}
			if got := resolveMantleProjectID(ctx, tt.key); got != tt.want {
				t.Fatalf("resolveMantleProjectID = %q, want %q", got, tt.want)
			}
		})
	}
}
