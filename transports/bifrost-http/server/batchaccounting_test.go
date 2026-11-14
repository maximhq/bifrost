package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/jobaccounting"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The sweeper skips the plugin pipeline, so nothing upstream resolves a key for it.
// Pinning to the key that created the job is what stops core from polling with an
// arbitrary one — which for a video is not a preference but a correctness matter:
// OpenAI scopes a video id to its creating key.
func TestInternalJobContextPinsTheCreatingKey(t *testing.T) {
	ctx := internalJobContext(context.Background(), "key-abc")

	keyID, ok := ctx.Value(schemas.BifrostContextKeyAPIKeyID).(string)
	if !ok || keyID != "key-abc" {
		t.Fatalf("api key id = %q (ok=%v), want key-abc", keyID, ok)
	}
}

// BifrostContextKeyAPIKeyID is a reserved key: writes to it are dropped while the
// plugin pipeline holds restricted writes. This context never enters that phase, so
// the write must land — if that ever changes, the pin silently stops working and
// the sweeper goes back to polling with whatever key core picks.
func TestInternalJobContextReservedWriteIsNotDropped(t *testing.T) {
	ctx := internalJobContext(context.Background(), "key-reserved")
	if _, ok := ctx.Value(schemas.BifrostContextKeyAPIKeyID).(string); !ok {
		t.Fatal("a reserved-key write on a fresh sweeper context must not be dropped")
	}
	// Same reasoning as the skip flags this context has always set.
	if skip, ok := ctx.Value(schemas.BifrostContextKeySkipBudgetAndRateLimits).(bool); !ok || !skip {
		t.Fatal("expected the sweeper context to keep skipping budgets")
	}
}

// A job row written before the key was recorded, or by a path that never had one,
// must still poll rather than pin to the empty string and match nothing.
func TestInternalJobContextWithoutKeyLeavesSelectionOpen(t *testing.T) {
	for _, selectedKeyID := range []string{"", "   "} {
		ctx := internalJobContext(context.Background(), selectedKeyID)
		if _, ok := ctx.Value(schemas.BifrostContextKeyAPIKeyID).(string); ok {
			t.Fatalf("selectedKeyID %q must leave key selection unpinned", selectedKeyID)
		}
	}
}

// The sweeper's own invariants must survive the pin.
func TestInternalJobContextKeepsSweeperInvariants(t *testing.T) {
	ctx := internalJobContext(context.Background(), "key-abc")

	if skip, ok := ctx.Value(schemas.BifrostContextKeySkipPluginPipeline).(bool); !ok || !skip {
		t.Fatal("the sweeper must not re-enter the plugin pipeline")
	}
	if requestID, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string); !ok || requestID == "" {
		t.Fatal("every sweeper call needs its own request id")
	}
}

// Both job kinds poll through the same pinned context. An earlier revision gave
// batch an unpinned retry on failure, on the theory that a batch id is org-scoped
// so any key could rescue it. That fires on every transient error — an outage, a
// rate limit — doubling load on an upstream already failing, to cover only a key
// deleted mid-flight; and that case is already answered by the job parking as
// unpriceable and by /v1/batches/{id}/results settling inline with the caller's own
// key. One code path, no retry.
func TestInternalJobContextIsTheOnlySweeperContext(t *testing.T) {
	batchCtx := internalJobContext(context.Background(), "key-batch")
	videoCtx := internalJobContext(context.Background(), "key-video")

	for name, ctx := range map[string]*schemas.BifrostContext{"batch": batchCtx, "video": videoCtx} {
		if _, ok := ctx.Value(schemas.BifrostContextKeyAPIKeyID).(string); !ok {
			t.Fatalf("%s: both kinds must poll pinned to their creating key", name)
		}
	}
}

// The interaction sweeper reads a stored run through passthrough, pinned to the key that created
// it: a Gemini interaction lives in the creating key's project, so another key is told it does not
// exist. The poll must hit the run's stored path under its stored version, report the run back on
// ProviderJob, and turn a provider error, which passthrough returns as a response, into a
// ProviderCallError the settler can classify.
func TestInteractionPollerReadsTheRunWithTheCreatingKey(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	var mu sync.Mutex
	var seenKeys, seenPaths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenKeys = append(seenKeys, r.Header.Get("x-goog-api-key"))
		seenPaths = append(seenPaths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1beta/interactions/int_srv" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"interaction not found"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"int_srv","agent":"deep-research-preview-04-2026","status":"completed","usage":{"total_input_tokens":1000,"total_output_tokens":50,"total_tokens":1050}}`))
	}))
	defer upstream.Close()

	cfg := &lib.Config{
		ModelCatalog: modelcatalog.NewTestCatalog(nil),
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.Gemini: {
				Keys: []schemas.Key{
					{ID: "key-other", Value: *schemas.NewSecretVar("gk-other"), Models: schemas.WhiteList{"*"}, Weight: 1},
					{ID: "key-creator", Value: *schemas.NewSecretVar("gk-creator"), Models: schemas.WhiteList{"*"}, Weight: 1},
				},
				NetworkConfig: &schemas.NetworkConfig{
					BaseURL:                        upstream.URL + "/v1beta",
					AllowPrivateNetwork:            true,
					DefaultRequestTimeoutInSeconds: 5,
				},
			},
		},
	}
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: lib.NewBaseAccount(cfg),
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	defer client.Shutdown()

	params, err := jobaccounting.MarshalInteractionParams(jobaccounting.InteractionJobParams{APIVersion: "v1beta"})
	require.NoError(t, err)
	job := func(id string) *cstables.TableProviderJob {
		return &cstables.TableProviderJob{
			Kind:          cstables.ProviderJobKindInteraction,
			Provider:      string(schemas.Gemini),
			JobID:         id,
			Model:         "deep-research-preview-04-2026",
			Endpoint:      "/interactions/" + id,
			Params:        params,
			SelectedKeyID: "key-creator",
		}
	}
	poller := &bifrostInteractionPoller{client: client}

	// Several polls, so a key picked by weight rather than by the pin would show up.
	for range 4 {
		resp, err := poller.PollInteraction(context.Background(), job("int_srv"))
		require.NoError(t, err)
		require.NotNil(t, resp.ProviderJob)
		assert.Equal(t, "int_srv", resp.ProviderJob.ID)
		assert.True(t, resp.ProviderJob.Terminal)
		require.NotNil(t, resp.ProviderJob.Usage)
		assert.Equal(t, 1050, resp.ProviderJob.Usage.LLMUsage.TotalTokens)
		assert.Nil(t, resp.PassthroughUsage, "the sweeper's read bills nothing on its own")
	}
	mu.Lock()
	assert.Equal(t, []string{"gk-creator", "gk-creator", "gk-creator", "gk-creator"}, seenKeys, "every poll uses the key that created the run")
	assert.Equal(t, "GET /v1beta/interactions/int_srv", seenPaths[0])
	mu.Unlock()

	_, err = poller.PollInteraction(context.Background(), job("int_gone"))
	require.Error(t, err)
	assert.Equal(t, http.StatusNotFound, jobaccounting.ProviderCallStatus(err), "a 404 answered as a passthrough response becomes a provider call error")
	assert.Equal(t, jobaccounting.ProviderCallGone, jobaccounting.ClassifyProviderCall(err))
}
