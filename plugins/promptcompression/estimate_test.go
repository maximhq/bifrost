package promptcompression

import "testing"

func TestEstimateTokens(t *testing.T) {
	cases := map[string]int{"": 0, "abcd": 1, "abcde": 2}
	for in, want := range cases {
		if got := estimateTokens(in); got != want {
			t.Errorf("estimateTokens(%q)=%d want %d", in, got, want)
		}
	}
}
