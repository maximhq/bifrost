package handlers

import (
	"context"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLiveRunner stands in for the plugin pipeline: it records every unit it admits and every
// post-hook that closes one, and refuses units once refuse is set.
type fakeLiveRunner struct {
	mu       sync.Mutex
	opens    []fakeLiveOpen
	posts    []fakeLivePost
	cleanups int
	refuse   bool
	keyIDs   map[string][]string
}

type fakeLiveOpen struct {
	model        string
	continuation bool
	kind         string
	start        bool
}

type fakeLivePost struct {
	model        string
	continuation bool
	parentID     string
	requestID    string
	kind         string
	end          bool
	delegationID string
	failed       bool // the provider refused to run the unit's delegation
	resp         *schemas.BifrostResponsesResponse
	live         *schemas.LiveSessionLog
	err          *schemas.BifrostError
}

func (f *fakeLiveRunner) RunRealtimeTurnPreHooks(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*bifrost.RealtimeTurnHooks, *schemas.BifrostError) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, model, _ := req.GetRequestFields()
	continuation, _ := ctx.Value(schemas.BifrostContextKeySessionContinuation).(bool)
	kind, _ := ctx.Value(schemas.BifrostContextKeyLiveUnit).(string)
	start, _ := ctx.Value(schemas.BifrostContextKeyLiveSessionStart).(bool)
	f.opens = append(f.opens, fakeLiveOpen{model: model, continuation: continuation, kind: kind, start: start})
	if f.refuse {
		return nil, newRealtimeWireBifrostError(402, "budget_exceeded", "Budget exceeded: virtual key budget spent")
	}
	if ids, restricted := f.keyIDs[model]; restricted {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceIncludeOnlyKeys, ids)
	}
	return &bifrost.RealtimeTurnHooks{
		PostHookRunner: func(postCtx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
			f.mu.Lock()
			defer f.mu.Unlock()
			post := fakeLivePost{model: model, err: bifrostErr}
			post.continuation, _ = postCtx.Value(schemas.BifrostContextKeySessionContinuation).(bool)
			post.parentID, _ = postCtx.Value(schemas.BifrostContextKeyParentRequestID).(string)
			post.requestID, _ = postCtx.Value(schemas.BifrostContextKeyRequestID).(string)
			post.kind, _ = postCtx.Value(schemas.BifrostContextKeyLiveUnit).(string)
			post.end, _ = postCtx.Value(schemas.BifrostContextKeyLiveSessionEnd).(bool)
			post.delegationID, _ = postCtx.Value(schemas.BifrostContextKeyLiveDelegationID).(string)
			post.failed, _ = postCtx.Value(schemas.BifrostContextKeyLiveDelegationFailed).(bool)
			if result != nil {
				post.resp = result.ResponsesResponse
				post.live = result.LiveSession
			}
			f.posts = append(f.posts, post)
			return result, nil
		},
		Cleanup: func() {
			f.mu.Lock()
			f.cleanups++
			f.mu.Unlock()
		},
	}, nil
}

func (f *fakeLiveRunner) setRefuse(refuse bool) {
	f.mu.Lock()
	f.refuse = refuse
	f.mu.Unlock()
}

func (f *fakeLiveRunner) snapshot() ([]fakeLiveOpen, []fakeLivePost, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeLiveOpen(nil), f.opens...), append([]fakeLivePost(nil), f.posts...), f.cleanups
}

func newTestLiveMeter(runner *fakeLiveRunner) *liveMeter {
	baseCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	return newLiveMeter(runner, baseCtx, schemas.OpenAI, schemas.Key{ID: "key-1", Name: "primary"}, "bf-session-1")
}

func postSeconds(t *testing.T, post fakeLivePost) float64 {
	t.Helper()
	require.NotNil(t, post.resp)
	require.NotNil(t, post.resp.Usage)
	require.NotNil(t, post.resp.Usage.AudioSeconds)
	return *post.resp.Usage.AudioSeconds
}

func TestLiveMeterAdmission(t *testing.T) {
	t.Parallel()

	runner := &fakeLiveRunner{}
	meter := newTestLiveMeter(runner)
	require.Nil(t, meter.admit("gpt-live-1", "gpt-5.6-luna"))

	opens, posts, _ := runner.snapshot()
	require.Equal(t, []fakeLiveOpen{
		{model: "gpt-live-1", kind: liveUnitVoice, start: true}, // the session's one request
		{model: "gpt-5.6-luna", kind: liveUnitBackend, continuation: true},
	}, opens)
	assert.Empty(t, posts, "nothing is billed at admission")

	// Client delegation has no backend on the socket.
	clientMode := &fakeLiveRunner{}
	require.Nil(t, newTestLiveMeter(clientMode).admit("gpt-live-1", ""))
	opens, _, _ = clientMode.snapshot()
	assert.Len(t, opens, 1)
}

func TestLiveMeterAdmissionRefusedBackendClosesVoiceUnit(t *testing.T) {
	t.Parallel()

	runner := &fakeLiveRunner{}
	meter := newTestLiveMeter(runner)
	meter.runner = &refuseSecondRunner{inner: runner}
	require.NotNil(t, meter.admit("gpt-live-1", "gpt-5.6-terra"), "a backend model the key may not use refuses the session")

	_, posts, cleanups := runner.snapshot()
	require.Len(t, posts, 1, "the admitted voice unit is closed so plugins see both halves")
	assert.NotNil(t, posts[0].err)
	assert.Equal(t, 1, cleanups)

	meter.finish(10)
	_, posts, _ = runner.snapshot()
	assert.Len(t, posts, 1, "a refused session bills nothing further")
}

// refuseSecondRunner admits the first unit and refuses every later one.
type refuseSecondRunner struct {
	inner *fakeLiveRunner
	calls int
}

func (r *refuseSecondRunner) RunRealtimeTurnPreHooks(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*bifrost.RealtimeTurnHooks, *schemas.BifrostError) {
	r.calls++
	if r.calls > 1 {
		return nil, newRealtimeWireBifrostError(403, "invalid_request_error", "model not allowed")
	}
	return r.inner.RunRealtimeTurnPreHooks(ctx, req)
}

func TestLiveMeterBillsVoiceWindowsFromCumulativeSnapshots(t *testing.T) {
	t.Parallel()

	runner := &fakeLiveRunner{}
	meter := newTestLiveMeter(runner)
	require.Nil(t, meter.admit("gpt-live-1", ""))
	meter.setProviderSessionID("live_abc")

	// Snapshots are cumulative: 14s then 29s is 29s of voice, not 43s, and below one window.
	require.Nil(t, meter.onUsage(14))
	require.Nil(t, meter.onUsage(29))
	_, posts, _ := runner.snapshot()
	assert.Empty(t, posts)

	// Crossing the window admits the next unit first, then bills the first.
	require.Nil(t, meter.onUsage(44))
	opens, posts, _ := runner.snapshot()
	require.Len(t, opens, 2)
	assert.True(t, opens[1].continuation, "later windows are continuations")
	require.Len(t, posts, 1)
	assert.Equal(t, 44.0, postSeconds(t, posts[0]))
	assert.False(t, posts[0].continuation, "the first window is the session's request")
	assert.Equal(t, "bf-session-1", posts[0].parentID, "units group under one stable session id")
	assert.Equal(t, schemas.LiveRequest, posts[0].resp.ExtraFields.RequestType)
	assert.Empty(t, posts[0].resp.ExtraFields.PricingRequestType)

	// A repeated or stale snapshot adds nothing.
	require.Nil(t, meter.onUsage(44))
	require.Nil(t, meter.onUsage(40))

	meter.finish(51.5)
	meter.finish(60) // idempotent
	_, posts, cleanups := runner.snapshot()
	require.Len(t, posts, 2)
	assert.Equal(t, 7.5, postSeconds(t, posts[1]))
	assert.True(t, posts[1].continuation)
	assert.Equal(t, 2, cleanups, "every admitted unit is released")
}

func TestLiveMeterRefusedWindowKeepsBillingOnCurrentUnit(t *testing.T) {
	t.Parallel()

	runner := &fakeLiveRunner{}
	meter := newTestLiveMeter(runner)
	require.Nil(t, meter.admit("gpt-live-1", ""))

	runner.setRefuse(true)
	refusal := meter.onUsage(31)
	require.NotNil(t, refusal, "the next window was not admitted")
	_, posts, _ := runner.snapshot()
	assert.Empty(t, posts, "the current unit stays open to carry the rest of the session")

	// Usage reported while the session drains lands on the unit governance already admitted.
	assert.Equal(t, refusal, meter.onUsage(40))
	meter.finish(42)
	_, posts, _ = runner.snapshot()
	require.Len(t, posts, 1)
	assert.Equal(t, 42.0, postSeconds(t, posts[0]), "every reported second is billed")
}

func TestLiveMeterBillsEachBackendResponseOnce(t *testing.T) {
	t.Parallel()

	runner := &fakeLiveRunner{}
	meter := newTestLiveMeter(runner)
	require.Nil(t, meter.admit("gpt-live-1", "gpt-5.6-luna"))

	priority := schemas.BifrostServiceTierPriority
	response := &schemas.BifrostResponsesResponse{
		ID:          new("resp_1"),
		Model:       "gpt-5.6-luna-2026-09-01", // OpenAI may report a dated snapshot
		ServiceTier: &priority,
		Usage: &schemas.ResponsesResponseUsage{
			InputTokens: 5203, OutputTokens: 161, TotalTokens: 5364,
			InputTokensDetails:  &schemas.ResponsesResponseInputTokens{CachedReadTokens: 64},
			OutputTokensDetails: &schemas.ResponsesResponseOutputTokens{ReasoningTokens: 111},
		},
	}
	require.Nil(t, meter.onBackendResponse(response, "", 0))
	require.Nil(t, meter.onBackendResponse(response, "", 0), "a replayed response is not billed twice")

	_, posts, _ := runner.snapshot()
	require.Len(t, posts, 1)
	post := posts[0]
	assert.Equal(t, "gpt-5.6-luna", post.model)
	assert.True(t, post.continuation)
	assert.Equal(t, schemas.LiveRequest, post.resp.ExtraFields.RequestType)
	assert.Equal(t, schemas.ResponsesRequest, post.resp.ExtraFields.PricingRequestType, "backend tokens price as Responses usage")
	require.NotNil(t, post.resp.ServiceTier)
	assert.Equal(t, schemas.BifrostServiceTierPriority, *post.resp.ServiceTier)
	assert.Equal(t, 5203, post.resp.Usage.InputTokens)
	assert.Equal(t, 64, post.resp.Usage.InputTokensDetails.CachedReadTokens)
	assert.Equal(t, 111, post.resp.Usage.OutputTokensDetails.ReasoningTokens)
	assert.Nil(t, response.Usage.AudioSeconds)

	// A response with no usage is ignored.
	require.Nil(t, meter.onBackendResponse(&schemas.BifrostResponsesResponse{ID: new("resp_2")}, "", 0))
	_, posts, _ = runner.snapshot()
	assert.Len(t, posts, 1)
}

func TestLiveMeterSumsBackendUsageWhileRefused(t *testing.T) {
	t.Parallel()

	runner := &fakeLiveRunner{}
	meter := newTestLiveMeter(runner)
	require.Nil(t, meter.admit("gpt-live-1", "gpt-5.6-luna"))
	runner.setRefuse(true)

	for _, id := range []string{"resp_1", "resp_2"} {
		require.NotNil(t, meter.onBackendResponse(&schemas.BifrostResponsesResponse{
			ID:    new(id),
			Model: "gpt-5.6-luna",
			Usage: &schemas.ResponsesResponseUsage{InputTokens: 100, OutputTokens: 10, TotalTokens: 110},
		}, "", 0))
	}
	meter.finish(5)
	_, posts, _ := runner.snapshot()
	var backend *schemas.BifrostResponsesResponse
	for _, post := range posts {
		if post.model == "gpt-5.6-luna" {
			backend = post.resp
		}
	}
	require.NotNil(t, backend)
	assert.Equal(t, 200, backend.Usage.InputTokens, "both responses bill on the admitted unit")
	assert.Equal(t, 220, backend.Usage.TotalTokens)
}

func TestLiveMeterFailedDelegationClosesItsUnitWithTheError(t *testing.T) {
	t.Parallel()

	runner := &fakeLiveRunner{}
	meter := newTestLiveMeter(runner)
	require.Nil(t, meter.admit("gpt-live-1", "gpt-5.6-luna"))

	refused := newRealtimeWireBifrostError(400, "responses_validation_failed", "The model does not exist")
	require.Nil(t, meter.failBackend(refused))

	opens, posts, _ := runner.snapshot()
	require.Len(t, posts, 1, "the refused delegation closes as a backend unit")
	assert.Equal(t, liveUnitBackend, posts[0].kind)
	assert.Equal(t, "gpt-5.6-luna", posts[0].model)
	assert.True(t, posts[0].continuation)
	require.NotNil(t, posts[0].err)
	assert.Equal(t, "The model does not exist", posts[0].err.Error.Message)
	assert.Nil(t, posts[0].resp, "nothing ran")
	assert.True(t, posts[0].failed, "plugins can tell a refused delegation from a refused session")
	assert.Len(t, opens, 3, "the next backend unit is admitted before the failed one closes")

	// The session goes on: the next response bills on the new unit.
	response := &schemas.BifrostResponsesResponse{ID: new("resp_1"), Model: "gpt-5.6-luna", Usage: &schemas.ResponsesResponseUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}}
	require.Nil(t, meter.onBackendResponse(response, "item_1", 0))
	_, posts, _ = runner.snapshot()
	require.Len(t, posts, 2)
	assert.Nil(t, posts[1].err)
	assert.False(t, posts[1].failed)
	assert.Equal(t, 12, posts[1].resp.Usage.TotalTokens)

	// A next unit governance refuses ends the session instead, as a billed response would.
	runner.setRefuse(true)
	assert.NotNil(t, meter.failBackend(refused))
}

func TestLiveMeterSwitchBackend(t *testing.T) {
	t.Parallel()

	runner := &fakeLiveRunner{}
	meter := newTestLiveMeter(runner)
	require.Nil(t, meter.admit("gpt-live-1", "gpt-5.6-luna"))

	require.Nil(t, meter.switchBackend("gpt-5.6-terra"))
	opens, _, _ := runner.snapshot()
	require.Len(t, opens, 3)
	assert.Equal(t, fakeLiveOpen{model: "gpt-5.6-terra", kind: liveUnitBackend, continuation: true}, opens[2], "the new model is admitted by governance")

	// A response naming neither lane bills to the active backend.
	require.Nil(t, meter.onBackendResponse(&schemas.BifrostResponsesResponse{
		ID: new("resp_1"), Model: "unexpected", Usage: &schemas.ResponsesResponseUsage{TotalTokens: 1},
	}, "", 0))
	_, posts, _ := runner.snapshot()
	require.Len(t, posts, 1)
	assert.Equal(t, "gpt-5.6-terra", posts[0].model)

	// A refused switch keeps the current backend.
	runner.setRefuse(true)
	require.NotNil(t, meter.switchBackend("gpt-5.6-sol"))
	assert.Equal(t, "gpt-5.6-terra", meter.activeBackend)

	// A switch that lands after the session ended opens nothing: finish ran its post-hooks already
	// and never runs again, so a unit opened now would leak its pipeline.
	runner.setRefuse(false)
	meter.finish(10)
	opens, _, cleanups := runner.snapshot()
	bifrostErr := meter.switchBackend("gpt-5.6-nova")
	require.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, "ended")
	opensAfter, _, cleanupsAfter := runner.snapshot()
	assert.Len(t, opensAfter, len(opens), "no unit is opened on a finished meter")
	assert.Equal(t, cleanups, cleanupsAfter)
	assert.Equal(t, "gpt-5.6-terra", meter.activeBackend)
}

func TestLiveMeterStaleCheckReadmitsWithoutEstimating(t *testing.T) {
	t.Parallel()

	runner := &fakeLiveRunner{}
	meter := newTestLiveMeter(runner)
	require.Nil(t, meter.admit("gpt-live-1", ""))
	require.Nil(t, meter.onUsage(12))

	require.Nil(t, meter.checkStale(time.Now()))
	opens, _, _ := runner.snapshot()
	assert.Len(t, opens, 1, "usage is still fresh")

	require.Nil(t, meter.checkStale(time.Now().Add(61*time.Second)))
	opens, posts, _ := runner.snapshot()
	assert.Len(t, opens, 2, "a silent session is checked again")
	require.Len(t, posts, 1)
	assert.Equal(t, 12.0, postSeconds(t, posts[0]), "only what OpenAI reported is billed")

	runner.setRefuse(true)
	assert.NotNil(t, meter.checkStale(time.Now().Add(200*time.Second)))
}

func TestLiveMeterMarksSessionBoundariesForPlugins(t *testing.T) {
	t.Parallel()

	runner := &fakeLiveRunner{}
	meter := newTestLiveMeter(runner)
	meter.setTransport("websocket")
	require.Nil(t, meter.admit("gpt-live-1", "gpt-5.6-luna"))
	meter.setProviderSessionID("live_abc")
	opens, _, _ := runner.snapshot()
	assert.Equal(t, []fakeLiveOpen{
		{model: "gpt-live-1", kind: liveUnitVoice, start: true},
		{model: "gpt-5.6-luna", kind: liveUnitBackend, continuation: true},
	}, opens, "only the unit that opens the session is marked as its start")

	// A backend unit closes with what its response produced, for the delegation's log row.
	output := []schemas.ResponsesMessage{{Type: new(schemas.ResponsesMessageTypeMessage), Role: new(schemas.ResponsesInputMessageRoleAssistant)}}
	require.Nil(t, meter.onBackendResponse(&schemas.BifrostResponsesResponse{ID: new("resp_1"), Model: "gpt-5.6-luna", Output: output, Usage: &schemas.ResponsesResponseUsage{TotalTokens: 5}}, "item_1", 2500))
	_, posts, _ := runner.snapshot()
	require.Len(t, posts, 1)
	assert.Equal(t, liveUnitBackend, posts[0].kind)
	assert.Equal(t, "item_1", posts[0].delegationID, "the unit names the delegation its response ran")
	assert.Equal(t, output, posts[0].resp.Output)
	require.NotNil(t, posts[0].resp.ID)
	assert.Equal(t, "resp_1", *posts[0].resp.ID)
	assert.False(t, posts[0].end)

	// The voice unit closes last, carrying the session log with the transcript and the confirmed end.
	transcript := []schemas.LiveTranscriptLine{{Role: "user", Text: "Hi there.", StartMs: 0, EndMs: 900}}
	meter.setEnding(transcript)
	meter.finish(40)
	_, posts, _ = runner.snapshot()
	require.Len(t, posts, 3)
	assert.Equal(t, liveUnitBackend, posts[1].kind)
	assert.False(t, posts[1].end)
	last := posts[2]
	assert.Equal(t, liveUnitVoice, last.kind)
	assert.True(t, last.end, "the last unit ends the session")
	require.NotNil(t, last.live)
	assert.Equal(t, transcript, last.live.Transcript)
	assert.Equal(t, "websocket", last.live.Transport)
	assert.Equal(t, "live_abc", last.live.ProviderSessionID)
	assert.Nil(t, last.resp.Output, "the transcript rides on the session log, not on the response")
	assert.Equal(t, 40.0, postSeconds(t, last))

	// A dropped session still closes its unit and carries the session log.
	dropped := &fakeLiveRunner{}
	droppedMeter := newTestLiveMeter(dropped)
	require.Nil(t, droppedMeter.admit("gpt-live-1", ""))
	droppedMeter.finish(12)
	_, posts, _ = dropped.snapshot()
	require.Len(t, posts, 1)
	assert.True(t, posts[0].end)
	require.NotNil(t, posts[0].live)

	// A session that never ran ends with the error that stopped it.
	aborted := &fakeLiveRunner{}
	abortedMeter := newTestLiveMeter(aborted)
	require.Nil(t, abortedMeter.admit("gpt-live-1", "gpt-5.6-luna"))
	abortedMeter.abort(newRealtimeWireBifrostError(502, "server_error", "upstream refused"))
	abortedMeter.finish(30)
	_, posts, cleanups := aborted.snapshot()
	require.Len(t, posts, 2, "abort closes both units and finish adds nothing")
	assert.Equal(t, 2, cleanups)
	assert.Equal(t, liveUnitVoice, posts[1].kind)
	assert.True(t, posts[1].end)
	require.NotNil(t, posts[1].err)
	assert.Equal(t, "upstream refused", posts[1].err.Error.Message)
}

func (f *fakeLiveRunner) restrictKeys(model string, ids []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.keyIDs == nil {
		f.keyIDs = make(map[string][]string)
	}
	f.keyIDs[model] = ids
}

func TestLiveMeterBackendSwitchEnforcesPinnedKey(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		keys   []string
		reject bool
	}{
		{name: "different key", keys: []string{"key-2"}, reject: true},
		{name: "no keys", keys: []string{}, reject: true},
		{name: "pinned key allowed", keys: []string{"key-1", "key-2"}},
		{name: "unrestricted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeLiveRunner{}
			keys := map[string][]string{"gpt-live-1": {"key-1", "key-2"}, "gpt-5.6-luna": {"key-1"}}
			if tc.keys != nil {
				keys["gpt-5.6-terra"] = tc.keys
			}
			runner.keyIDs = keys
			meter := newLiveMeter(runner, schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), schemas.OpenAI, schemas.Key{}, "bf-session-1")
			require.Nil(t, meter.admit("gpt-live-1", "gpt-5.6-luna"))
			meter.setKey(schemas.Key{ID: "key-1", Models: schemas.WhiteList{"*"}})
			defer meter.finish(0)
			controller := newTestLiveController(fakeLiveModels{allowed: true}, meter, nil)
			checked, refusal := controller.admitSessionUpdate([]byte(`{"type":"session.update","session":{"delegation":{"responses":{"model":"gpt-5.6-terra"}}}}`))
			if tc.reject {
				require.NotNil(t, refusal, "the update cannot use a different upstream key")
				assert.Equal(t, 403, *refusal.StatusCode)
				assert.Nil(t, checked, "a refused update must not be forwarded")
				assert.Equal(t, "gpt-5.6-luna", meter.activeBackend)
				assert.NotContains(t, meter.backends, "gpt-5.6-terra")
				assert.Equal(t, []string{"key-1"}, meter.allowedKeys, "a refused unit must not narrow admitted units' keys")
				_, posts, cleanups := runner.snapshot()
				require.Len(t, posts, 1)
				assert.NotNil(t, posts[0].err)
				assert.Nil(t, posts[0].resp, "a refused unit bills no usage")
				assert.True(t, posts[0].continuation)
				assert.False(t, posts[0].end, "rejecting an update leaves the session open")
				assert.Equal(t, 1, cleanups)
				// Retrying with a permitted grant must succeed without stale restrictions or lanes.
				keys["gpt-5.6-terra"] = []string{"key-1"}
				require.Nil(t, meter.switchBackend("gpt-5.6-terra"))
			} else {
				require.Nil(t, refusal)
				assert.NotEmpty(t, checked)
			}
			assert.Equal(t, "gpt-5.6-terra", meter.activeBackend)
			require.Nil(t, meter.onUsage(30), "voice renewal must still use its permitted pinned key")
			meter.finish(30)
			opens, posts, cleanups := runner.snapshot()
			assert.Len(t, posts, len(opens), "every admitted pre-hook has a post-hook")
			assert.Equal(t, len(opens), cleanups, "every admitted unit is cleaned exactly once")
		})
	}
}

func TestLiveMeterRenewalEnforcesPinnedKey(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{liveUnitVoice, liveUnitBackend} {
		t.Run(kind, func(t *testing.T) {
			runner := &fakeLiveRunner{}
			keys := map[string][]string{"gpt-live-1": {"key-1"}, "gpt-5.6-luna": {"key-1"}}
			runner.keyIDs = keys
			meter := newLiveMeter(runner, schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), schemas.OpenAI, schemas.Key{}, "bf-session-1")
			require.Nil(t, meter.admit("gpt-live-1", "gpt-5.6-luna"))
			meter.setKey(schemas.Key{ID: "key-1"})
			var refusal *schemas.BifrostError
			if kind == liveUnitVoice {
				keys["gpt-live-1"] = []string{"key-2"}
				refusal = meter.onUsage(30)
			} else {
				keys["gpt-5.6-luna"] = []string{"key-2"}
				refusal = meter.onBackendResponse(&schemas.BifrostResponsesResponse{ID: new("resp-1"), Model: "gpt-5.6-luna", Usage: &schemas.ResponsesResponseUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}}, "delegation-1", 10)
			}
			require.NotNil(t, refusal, "a renewal cannot continue on a key excluded by its grant")
			assert.Same(t, refusal, meter.refusal)
			meter.finish(meter.lastReportedSeconds())
			meter.finish(100)
			opens, posts, cleanups := runner.snapshot()
			require.Len(t, posts, 3)
			assert.Equal(t, len(opens), cleanups)
			assert.NotNil(t, posts[0].err, "the refused renewal closes with an error")
			if kind == liveUnitVoice {
				assert.Equal(t, 30.0, postSeconds(t, posts[2]), "already spent voice usage is settled")
			} else {
				require.NotNil(t, posts[1].resp)
				assert.Equal(t, 15, posts[1].resp.Usage.TotalTokens, "already spent backend usage is settled")
			}
		})
	}
}
