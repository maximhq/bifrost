package typesafe

import (
	"context"
	"net/http"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestDecisionIdempotentReplayIsNotBilled pins that a same-key replay keeps its
// answers but reports no usage, so cost and usage export do not charge the
// original operation a second time, while a first evaluation keeps its usage.
func TestDecisionIdempotentReplayIsNotBilled(t *testing.T) {
	for _, tc := range []struct {
		name      string
		replayed  string
		wantUsage bool
	}{
		{name: "first evaluation", replayed: "", wantUsage: true},
		{name: "explicit false", replayed: "false", wantUsage: true},
		{name: "replay", replayed: "true", wantUsage: false},
		{name: "replay mixed case", replayed: "True", wantUsage: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, provider := newTypesafeFixture(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
				if tc.replayed != "" {
					w.Header().Set("X-Idempotency-Replayed", tc.replayed)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(fixtureSuccessBody))
			})
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			resp, bifrostErr := provider.Decision(ctx, fixtureKey(), decisionRequest(t, "fixture", map[string]TypesafeQuestion{
				"q": {Type: TypesafeQuestionTypeNoul, Instructions: "Evaluate this state."},
			}))
			if bifrostErr != nil {
				t.Fatalf("unexpected error: %v", bifrostErr)
			}
			if len(resp.Answers) != 1 {
				t.Fatalf("answers lost: %+v", resp.Answers)
			}
			if got := resp.Usage != nil; got != tc.wantUsage {
				t.Fatalf("usage present = %v, want %v (%+v)", got, tc.wantUsage, resp.Usage)
			}
			if tc.wantUsage && resp.Usage.PromptTokens != 3 {
				t.Fatalf("prompt tokens = %d, want 3", resp.Usage.PromptTokens)
			}
		})
	}
}
