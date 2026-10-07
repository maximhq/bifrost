package handlers

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestMCPHeadersEqual pins the diff that decides whether a header edit has to
// re-dial the upstream connection. A sticky client bakes its credential onto
// the transport at connect time, so a false negative here leaves it talking to
// the upstream with the header the admin just replaced; a false positive
// cycles a live connection for an edit that changed nothing.
func TestMCPHeadersEqual(t *testing.T) {
	plain := func(v string) schemas.SecretVar { return *schemas.NewSecretVar(v) }

	tests := []struct {
		name string
		a    map[string]schemas.SecretVar
		b    map[string]schemas.SecretVar
		want bool
	}{
		{
			name: "both empty",
			want: true,
		},
		{
			name: "identical single header",
			a:    map[string]schemas.SecretVar{"Authorization": plain("Bearer one")},
			b:    map[string]schemas.SecretVar{"Authorization": plain("Bearer one")},
			want: true,
		},
		{
			name: "changed value",
			a:    map[string]schemas.SecretVar{"Authorization": plain("Bearer one")},
			b:    map[string]schemas.SecretVar{"Authorization": plain("Bearer two")},
			want: false,
		},
		{
			name: "added header",
			a:    map[string]schemas.SecretVar{"Authorization": plain("Bearer one")},
			b:    map[string]schemas.SecretVar{"Authorization": plain("Bearer one"), "X-Tenant": plain("acme")},
			want: false,
		},
		{
			name: "removed header",
			a:    map[string]schemas.SecretVar{"Authorization": plain("Bearer one"), "X-Tenant": plain("acme")},
			b:    map[string]schemas.SecretVar{"Authorization": plain("Bearer one")},
			want: false,
		},
		{
			name: "renamed key with same value",
			a:    map[string]schemas.SecretVar{"X-Api-Key": plain("secret")},
			b:    map[string]schemas.SecretVar{"X-Other-Key": plain("secret")},
			want: false,
		},
		{
			// Header names are case-sensitive in Go maps and the resolvers
			// compare them case-insensitively, but the stored map is what gets
			// baked onto the transport verbatim — a case change is a real
			// change to what is persisted, so re-dialing is the safe answer.
			name: "case-differing key",
			a:    map[string]schemas.SecretVar{"X-Api-Key": plain("secret")},
			b:    map[string]schemas.SecretVar{"x-api-key": plain("secret")},
			want: false,
		},
		{
			name: "empty vs populated",
			a:    map[string]schemas.SecretVar{},
			b:    map[string]schemas.SecretVar{"Authorization": plain("Bearer one")},
			want: false,
		},
		{
			name: "nil vs empty are both no headers",
			a:    nil,
			b:    map[string]schemas.SecretVar{},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mcpHeadersEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("mcpHeadersEqual() = %v, want %v", got, tt.want)
			}
			// The comparison must not depend on argument order: the caller
			// passes (stored, resolved) and either side can be the larger map.
			if got := mcpHeadersEqual(tt.b, tt.a); got != tt.want {
				t.Errorf("mcpHeadersEqual() reversed = %v, want %v", got, tt.want)
			}
		})
	}
}
