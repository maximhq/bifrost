package routing

import "testing"

func TestCELExpressionReferencesIdentifier(t *testing.T) {
	tests := []struct {
		name       string
		expression string
		expected   bool
	}{
		{
			name:       "direct identifier",
			expression: `complexity_tier == "SIMPLE"`,
			expected:   true,
		},
		{
			name:       "identifier in in-list",
			expression: `complexity_tier in ["COMPLEX", "REASONING"]`,
			expected:   true,
		},
		{
			name:       "string literal only",
			expression: `model == "complexity_tier"`,
			expected:   false,
		},
		{
			name:       "unrelated identifier containing name",
			expression: `my_complexity_tier == true`,
			expected:   false,
		},
		{
			name:       "map key string",
			expression: `headers["complexity_tier"] == "SIMPLE"`,
			expected:   false,
		},
		{
			name:       "field selection",
			expression: `metadata.complexity_tier == "SIMPLE"`,
			expected:   false,
		},
		{
			name:       "comprehension local shadows identifier",
			expression: `["SIMPLE"].exists(complexity_tier, complexity_tier == "SIMPLE")`,
			expected:   false,
		},
		{
			name:       "comprehension references outer identifier",
			expression: `["SIMPLE"].exists(tier, complexity_tier == tier)`,
			expected:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CELExpressionReferencesIdentifier(tt.expression, "complexity_tier"); got != tt.expected {
				t.Fatalf("CELExpressionReferencesIdentifier(%q) = %v, want %v", tt.expression, got, tt.expected)
			}
		})
	}
}
