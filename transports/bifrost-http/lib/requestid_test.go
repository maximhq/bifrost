package lib

import "testing"

func TestNormalizeRequestID(t *testing.T) {
	t.Run("preserves valid caller id", func(t *testing.T) {
		const want = "req-abc-123"
		if got := NormalizeRequestID(want); got != want {
			t.Fatalf("NormalizeRequestID() = %q, want %q", got, want)
		}
	})

	for _, tc := range []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "whitespace", value: "   "},
		{name: "all-zero sentinel", value: zeroRequestID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeRequestID(tc.value)
			if got == "" || got == tc.value || got == zeroRequestID {
				t.Fatalf("NormalizeRequestID(%q) returned unusable id %q", tc.value, got)
			}
		})
	}
}
