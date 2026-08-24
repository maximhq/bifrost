package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// attemptProvider is what every ShouldEmbedReasoningItemID call site in
// responses.go reads, so its two branches decide whether a reasoning item's id is
// smuggled into its signature/data. Both branches are load-bearing:
//
//   - RoutingInfo.Provider is the non-deprecated field and core populates it on every
//     stream chunk, so it must win when set.
//   - ExtraFields.Provider is the wider field: syncDeprecatedFromRoutingInfo only
//     backfills it when RoutingInfo.Provider is non-empty, so frames built outside
//     core's pipeline carry only the deprecated one. Dropping the fallback would stop
//     embedding ids on exactly those frames, and silently -- nothing errors, the two
//     halves of a reasoning item just stop merging on re-ingest.
func TestStreamAttemptProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		extra schemas.BifrostResponseExtraFields
		want  schemas.ModelProvider
	}{
		{
			name: "routing info wins when set",
			extra: schemas.BifrostResponseExtraFields{
				Provider:    schemas.Anthropic,
				RoutingInfo: schemas.RoutingInfo{Provider: schemas.OpenAI},
			},
			want: schemas.OpenAI,
		},
		{
			name: "falls back to the deprecated field when routing info is empty",
			extra: schemas.BifrostResponseExtraFields{
				Provider: schemas.OpenAI,
			},
			want: schemas.OpenAI,
		},
		{
			name: "both agree",
			extra: schemas.BifrostResponseExtraFields{
				Provider:    schemas.OpenAI,
				RoutingInfo: schemas.RoutingInfo{Provider: schemas.OpenAI},
			},
			want: schemas.OpenAI,
		},
		{
			name:  "neither set",
			extra: schemas.BifrostResponseExtraFields{},
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := attemptProvider(tt.extra); got != tt.want {
				t.Errorf("attemptProvider = %q, want %q", got, tt.want)
			}
		})
	}
}
