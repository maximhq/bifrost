package bedrockmantle

import (
	"context"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// TestResolveProjectID verifies precedence: per-alias AliasConfig.ProjectID overrides the
// key-level BedrockMantleKeyConfig.ProjectID, and an absent project resolves to "" (AWS
// default project).
func TestResolveProjectID(t *testing.T) {
	tests := []struct {
		name  string
		alias *schemas.AliasConfig // nil = no resolved alias in context
		key   schemas.Key
		want  string
	}{
		{name: "no mantle config", key: schemas.Key{}, want: ""},
		{name: "config without project", key: schemas.Key{BedrockMantleKeyConfig: &schemas.BedrockMantleKeyConfig{}}, want: ""},
		{
			name: "key-level project",
			key:  schemas.Key{BedrockMantleKeyConfig: &schemas.BedrockMantleKeyConfig{ProjectID: schemas.NewSecretVar("proj_key")}},
			want: "proj_key",
		},
		{
			name:  "alias override wins over key",
			alias: &schemas.AliasConfig{ModelID: "chirp", ProjectID: schemas.NewSecretVar("proj_alias")},
			key:   schemas.Key{BedrockMantleKeyConfig: &schemas.BedrockMantleKeyConfig{ProjectID: schemas.NewSecretVar("proj_key")}},
			want:  "proj_alias",
		},
		{
			name:  "empty alias override falls back to key",
			alias: &schemas.AliasConfig{ModelID: "chirp", ProjectID: schemas.NewSecretVar("")},
			key:   schemas.Key{BedrockMantleKeyConfig: &schemas.BedrockMantleKeyConfig{ProjectID: schemas.NewSecretVar("proj_key")}},
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
			if got := resolveProjectID(ctx, tt.key); got != tt.want {
				t.Fatalf("resolveProjectID = %q, want %q", got, tt.want)
			}
		})
	}
}
