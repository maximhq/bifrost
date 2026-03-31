package handlers

import (
	"testing"

	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

func TestValidateHeaderFilterConfig(t *testing.T) {
	tests := []struct {
		name      string
		config    *configstoreTables.GlobalHeaderFilterConfig
		wantErr   bool
		errSubstr string
	}{
		{
			name:   "nil config",
			config: nil,
		},
		{
			name:   "empty lists",
			config: &configstoreTables.GlobalHeaderFilterConfig{},
		},
		{
			name: "empty allowlist and denylist slices",
			config: &configstoreTables.GlobalHeaderFilterConfig{
				Allowlist: []string{},
				Denylist:  []string{},
			},
		},
		{
			name: "valid allowlist patterns",
			config: &configstoreTables.GlobalHeaderFilterConfig{
				Allowlist: []string{"anthropic-beta", "x-custom-*", "content-type"},
			},
		},
		{
			name: "valid denylist patterns",
			config: &configstoreTables.GlobalHeaderFilterConfig{
				Denylist: []string{"x-internal-*", "x-debug"},
			},
		},
		{
			name: "valid allowlist and denylist together",
			config: &configstoreTables.GlobalHeaderFilterConfig{
				Allowlist: []string{"anthropic-*", "content-type"},
				Denylist:  []string{"x-internal-*"},
			},
		},
		// Empty/whitespace entries should be silently dropped, not cause errors
		{
			name: "whitespace-only entries in allowlist are dropped",
			config: &configstoreTables.GlobalHeaderFilterConfig{
				Allowlist: []string{"  ", "anthropic-beta", ""},
			},
		},
		{
			name: "whitespace-only entries in denylist are dropped",
			config: &configstoreTables.GlobalHeaderFilterConfig{
				Denylist: []string{"", "x-debug", "   "},
			},
		},
		{
			name: "all-empty allowlist becomes effectively empty",
			config: &configstoreTables.GlobalHeaderFilterConfig{
				Allowlist: []string{"", "  ", "\t"},
			},
		},
		// Security header checks
		{
			name: "security header in allowlist rejected",
			config: &configstoreTables.GlobalHeaderFilterConfig{
				Allowlist: []string{"authorization"},
			},
			wantErr:   true,
			errSubstr: "not allowed to be configured",
		},
		{
			name: "security header in denylist rejected",
			config: &configstoreTables.GlobalHeaderFilterConfig{
				Denylist: []string{"x-api-key"},
			},
			wantErr:   true,
			errSubstr: "not allowed to be configured",
		},
		{
			name: "wildcard matching security header allowed (runtime strips security headers)",
			config: &configstoreTables.GlobalHeaderFilterConfig{
				Allowlist: []string{"authorization*"},
			},
		},
		{
			name: "wildcard prefix matching security headers allowed (runtime strips security headers)",
			config: &configstoreTables.GlobalHeaderFilterConfig{
				Allowlist: []string{"x-api-*"},
			},
		},
		{
			name: "bare wildcard in allowlist allowed (runtime strips security headers)",
			config: &configstoreTables.GlobalHeaderFilterConfig{
				Allowlist: []string{"*"},
			},
		},
		// Invalid wildcard syntax
		{
			name: "wildcard in middle of pattern rejected",
			config: &configstoreTables.GlobalHeaderFilterConfig{
				Allowlist: []string{"x-*-header"},
			},
			wantErr:   true,
			errSubstr: "wildcard (*) is only supported at the end",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateHeaderFilterConfig(tt.config)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errSubstr)
				}
				if tt.errSubstr != "" && !contains(err.Error(), tt.errSubstr) {
					t.Fatalf("expected error containing %q, got %q", tt.errSubstr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateHeaderFilterConfig_EmptyEntriesDropped(t *testing.T) {
	// Verify that empty/whitespace entries are actually removed from the stored config
	config := &configstoreTables.GlobalHeaderFilterConfig{
		Allowlist: []string{"  ", "anthropic-beta", "", "content-type", "\t"},
		Denylist:  []string{"", "x-debug", "   "},
	}
	if err := validateHeaderFilterConfig(config); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(config.Allowlist) != 2 {
		t.Fatalf("expected allowlist length 2, got %d: %v", len(config.Allowlist), config.Allowlist)
	}
	if config.Allowlist[0] != "anthropic-beta" || config.Allowlist[1] != "content-type" {
		t.Fatalf("unexpected allowlist: %v", config.Allowlist)
	}
	if len(config.Denylist) != 1 {
		t.Fatalf("expected denylist length 1, got %d: %v", len(config.Denylist), config.Denylist)
	}
	if config.Denylist[0] != "x-debug" {
		t.Fatalf("unexpected denylist: %v", config.Denylist)
	}
}

// TestValidateHeaderFilterConfig_EmptyConfigStillForwardsHeaders verifies that when
// all entries are empty/whitespace, validation strips them and the compiled matcher
// allows all headers through (same behavior as no config — x-bf-eh-* headers forwarded as-is).
func TestValidateHeaderFilterConfig_EmptyConfigStillForwardsHeaders(t *testing.T) {
	// Config where all entries are whitespace-only
	config := &configstoreTables.GlobalHeaderFilterConfig{
		Allowlist: []string{"", "  ", "\t"},
		Denylist:  []string{"", "   "},
	}
	if err := validateHeaderFilterConfig(config); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// After validation, both lists should be empty
	if len(config.Allowlist) != 0 {
		t.Fatalf("expected empty allowlist, got %v", config.Allowlist)
	}
	if len(config.Denylist) != 0 {
		t.Fatalf("expected empty denylist, got %v", config.Denylist)
	}
	// Compile the validated config into a matcher — should allow everything
	m := lib.NewHeaderMatcher(config)
	// Matcher with empty lists should allow all headers (x-bf-eh-* forwarded as-is)
	for _, header := range []string{"anthropic-beta", "x-custom-header", "content-type", "x-anything"} {
		if !m.ShouldAllow(header) {
			t.Errorf("expected header %q to be allowed with empty config, but it was denied", header)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
