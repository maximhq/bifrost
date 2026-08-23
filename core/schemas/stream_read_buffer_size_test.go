package schemas

import "testing"

func TestStreamReadBufferSize(t *testing.T) {
	cases := []struct {
		name string
		kb   int
		want int
	}{
		{"zero falls back to default", 0, DefaultStreamReadBufferSizeKB * 1024},
		{"negative falls back to default", -5, DefaultStreamReadBufferSizeKB * 1024},
		{"in range is honoured", 256, 256 * 1024},
		{"at the cap is honoured", MaxStreamReadBufferSizeKB, MaxStreamReadBufferSizeKB * 1024},
		{"above the cap is clamped", MaxStreamReadBufferSizeKB + 1, MaxStreamReadBufferSizeKB * 1024},
		{"absurd value is clamped", 1 << 20, MaxStreamReadBufferSizeKB * 1024},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nc := &NetworkConfig{StreamReadBufferSizeKB: tc.kb}
			if got := nc.StreamReadBufferSize(); got != tc.want {
				t.Fatalf("StreamReadBufferSize() with %d KB = %d, want %d", tc.kb, got, tc.want)
			}
		})
	}
}
