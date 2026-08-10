package handlers

import "testing"

func TestResolveListenBillingDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		metadataDuration float64
		resultAudioEnd   float64
		pcmDuration      float64
		wallClock        float64
		want             float64
	}{
		{
			name:           "results window alone must not win over start+duration",
			resultAudioEnd: 30.0, // start=25 + duration=5
			pcmDuration:    29.5,
			want:           30.0,
		},
		{
			name:             "metadata preferred when largest",
			metadataDuration: 31.2,
			resultAudioEnd:   30.0,
			pcmDuration:      29.5,
			want:             31.2,
		},
		{
			name:        "pcm used when no provider timeline",
			pcmDuration: 28.0,
			wallClock:   35.0,
			want:        28.0,
		},
		{
			name:      "wall clock last resort",
			wallClock: 32.0,
			want:      32.0,
		},
		{
			name: "all zero",
			want: 0,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := resolveListenBillingDuration(tc.metadataDuration, tc.resultAudioEnd, tc.pcmDuration, tc.wallClock)
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
