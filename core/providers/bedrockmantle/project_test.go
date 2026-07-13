package bedrockmantle

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// TestResolveProjectID verifies the BedrockMantleKeyConfig.ProjectID field is honoured and that an
// absent project resolves to "" (AWS default project).
func TestResolveProjectID(t *testing.T) {
	tests := []struct {
		name string
		key  schemas.Key
		want string
	}{
		{name: "no mantle config", key: schemas.Key{}, want: ""},
		{name: "config without project", key: schemas.Key{BedrockMantleKeyConfig: &schemas.BedrockMantleKeyConfig{}}, want: ""},
		{
			name: "config with project",
			key:  schemas.Key{BedrockMantleKeyConfig: &schemas.BedrockMantleKeyConfig{ProjectID: schemas.NewSecretVar("proj_xyz")}},
			want: "proj_xyz",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveProjectID(tt.key); got != tt.want {
				t.Fatalf("resolveProjectID = %q, want %q", got, tt.want)
			}
		})
	}
}
