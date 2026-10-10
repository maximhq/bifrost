package logging

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// liveUnitCtx is the context the live transport gives one billing unit.
func liveUnitCtx(sessionID, requestID, kind string, start, end bool) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, requestID)
	ctx.SetValue(schemas.BifrostContextKeyLiveSessionID, sessionID)
	ctx.SetValue(schemas.BifrostContextKeyLiveUnit, kind)
	ctx.SetValue(schemas.BifrostContextKeyParentRequestID, sessionID)
	ctx.SetValue(schemas.BifrostContextKeySelectedKeyID, "key-1")
	ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, "vk-1")
	ctx.SetValue(schemas.BifrostContextKeyRealtimeTransport, "websocket")
	if start {
		ctx.SetValue(schemas.BifrostContextKeyLiveSessionStart, true)
	}
	if end {
		ctx.SetValue(schemas.BifrostContextKeyLiveSessionEnd, true)
	}
	return ctx
}

// liveDelegationCtx is a backend unit's context: which delegation its response ran, and when it began.
func liveDelegationCtx(sessionID, requestID, delegationID string, startedMs int64) *schemas.BifrostContext {
	ctx := liveUnitCtx(sessionID, requestID, "backend", false, false)
	ctx.SetValue(schemas.BifrostContextKeyLiveDelegationID, delegationID)
	ctx.SetValue(schemas.BifrostContextKeyLiveDelegationStartMs, startedMs)
	return ctx
}

func liveVoiceResponse(seconds float64, session *schemas.LiveSessionLog) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Object: "response",
			Model:  "gpt-live-1",
			Usage:  &schemas.ResponsesResponseUsage{AudioSeconds: &seconds},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.LiveRequest,
				Provider:               schemas.OpenAI,
				OriginalModelRequested: "gpt-live-1",
			},
		},
		LiveSession: session,
	}
}

func liveBackendResponse(id string, output []schemas.ResponsesMessage, usage *schemas.ResponsesResponseUsage) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{
		ID:     new(id),
		Object: "response",
		Model:  "gpt-5.6-luna",
		Output: output,
		Usage:  usage,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:            schemas.LiveRequest,
			PricingRequestType:     schemas.ResponsesRequest,
			Provider:               schemas.OpenAI,
			OriginalModelRequested: "gpt-5.6-luna",
		},
	}}
}

func liveStartRequest(model string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType:      schemas.LiveRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: model},
	}
}

func TestLiveSessionLogsAsOneRowWithItsDelegations(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	require.NoError(t, err)

	// The first voice unit opens the session; the row is registered under the session id.
	_, _, err = plugin.PreLLMHook(liveUnitCtx("bfsess-1", "unit-1", "voice", true, false), liveStartRequest("gpt-live-1"))
	require.NoError(t, err)
	// Later units register nothing of their own.
	_, _, err = plugin.PreLLMHook(liveUnitCtx("bfsess-1", "unit-2", "backend", false, false), liveStartRequest("gpt-5.6-luna"))
	require.NoError(t, err)

	// A full voice window closes.
	_, _, err = plugin.PostLLMHook(liveUnitCtx("bfsess-1", "unit-1", "voice", false, false), liveVoiceResponse(30, nil), nil)
	require.NoError(t, err)

	// A delegated backend call closes with what it produced.
	answer := "It's sunny in Paris."
	output := []schemas.ResponsesMessage{
		// On the live socket a delegation's output items arrive one by one: a server tool call, then the message as blocks.
		{Type: new(schemas.ResponsesMessageTypeWebSearchCall), ID: new("ws_1")},
		{Type: new(schemas.ResponsesMessageTypeMessage), Role: new(schemas.ResponsesInputMessageRoleAssistant), Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{Type: schemas.ResponsesMessageContentBlockType("output_text"), Text: &answer}}}},
	}
	usage := &schemas.ResponsesResponseUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15,
		InputTokensDetails: &schemas.ResponsesResponseInputTokens{CachedReadTokens: 4}, OutputTokensDetails: &schemas.ResponsesResponseOutputTokens{ReasoningTokens: 2}}
	_, _, err = plugin.PostLLMHook(liveDelegationCtx("bfsess-1", "unit-2", "item_1", 2500), liveBackendResponse("resp_1", output, usage), nil)
	require.NoError(t, err)
	// A function call is two responses of one delegation: the call, then the answer once the app
	// has returned the result. They log as one delegation.
	call := []schemas.ResponsesMessage{{Type: new(schemas.ResponsesMessageTypeFunctionCall), ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: new("call_1"), Name: new("get_calendar")}}}
	second := &schemas.ResponsesResponseUsage{InputTokens: 20, OutputTokens: 5, TotalTokens: 25,
		InputTokensDetails: &schemas.ResponsesResponseInputTokens{CachedReadTokens: 6}, OutputTokensDetails: &schemas.ResponsesResponseOutputTokens{ReasoningTokens: 3}}
	_, _, err = plugin.PostLLMHook(liveDelegationCtx("bfsess-1", "unit-5", "item_2", 20000), liveBackendResponse("resp_2", call, second), nil)
	require.NoError(t, err)
	continued := []schemas.ResponsesMessage{
		{Type: new(schemas.ResponsesMessageTypeFunctionCallOutput), ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: new("call_1")}},
		output[1],
	}
	third := &schemas.ResponsesResponseUsage{InputTokens: 30, OutputTokens: 10, TotalTokens: 40}
	_, _, err = plugin.PostLLMHook(liveDelegationCtx("bfsess-1", "unit-6", "item_2", 0), liveBackendResponse("resp_3", continued, third), nil)
	require.NoError(t, err)
	// The backend lane's open unit closes empty at session end; it is not a delegation.
	_, _, err = plugin.PostLLMHook(liveUnitCtx("bfsess-1", "unit-3", "backend", false, false), liveBackendResponse("", nil, &schemas.ResponsesResponseUsage{}), nil)
	require.NoError(t, err)

	// The last voice unit ends the session and carries the session log with the transcript.
	transcript := []schemas.LiveTranscriptLine{
		{Role: "user", Text: "What's the weather in Paris?", StartMs: 0, EndMs: 1800},
		{Role: "assistant", Text: "It's sunny in Paris.", StartMs: 4000, EndMs: 5600},
	}
	session := &schemas.LiveSessionLog{Transport: "websocket", ProviderSessionID: "live_abc", Transcript: transcript}
	_, _, err = plugin.PostLLMHook(liveUnitCtx("bfsess-1", "unit-4", "voice", false, true), liveVoiceResponse(7, session), nil)
	require.NoError(t, err)
	require.NoError(t, plugin.Cleanup())

	root, err := store.FindByID(context.Background(), "bfsess-1")
	require.NoError(t, err)
	assert.Equal(t, liveSessionObject, root.Object)
	assert.Equal(t, logStatusSuccess, root.Status)
	assert.Equal(t, "gpt-live-1", root.Model)
	assert.Empty(t, root.ParentRequestID, "the session row is the root; its units name it as their parent, it names nobody")
	assert.Equal(t, "key-1", root.SelectedKeyID)
	require.NotNil(t, root.VirtualKeyID)
	assert.Equal(t, "vk-1", *root.VirtualKeyID)
	require.NotNil(t, root.TokenUsageParsed)
	assert.Equal(t, 80, root.TokenUsageParsed.TotalTokens, "backend tokens are the session's tokens, counted once")
	require.NotNil(t, root.TokenUsageParsed.PromptTokensDetails)
	assert.Equal(t, 10, root.TokenUsageParsed.PromptTokensDetails.CachedReadTokens, "token details sum across delegations")
	require.NotNil(t, root.TokenUsageParsed.CompletionTokensDetails)
	assert.Equal(t, 5, root.TokenUsageParsed.CompletionTokensDetails.ReasoningTokens)
	assert.Equal(t, 80, root.TotalTokens)
	require.NotNil(t, root.TokenUsageParsed.AudioSeconds)
	assert.Equal(t, 37.0, *root.TokenUsageParsed.AudioSeconds)
	assert.Equal(t, []string{"get_calendar", "web_search"}, root.ToolCallNames)

	live := root.LiveSessionParsed
	require.NotNil(t, live, "the session's typed log is stored")
	assert.Equal(t, "websocket", live.Transport)
	assert.Equal(t, "live_abc", live.ProviderSessionID)
	assert.Equal(t, 37.0, live.VoiceSeconds)
	assert.Equal(t, transcript, live.Transcript)
	require.Len(t, live.Delegations, 2, "responses group by delegation; an empty backend unit is not one")
	delegation := live.Delegations[0]
	assert.Equal(t, "item_1", delegation.DelegationID)
	assert.Equal(t, "unit-2", delegation.RequestID)
	assert.Equal(t, []string{"resp_1"}, delegation.ResponseIDs)
	assert.Equal(t, "gpt-5.6-luna", delegation.Model)
	assert.Equal(t, int64(2500), delegation.StartedMs)
	assert.Equal(t, output, delegation.Output, "the backend's output items, as the Responses view renders them")
	require.NotNil(t, delegation.Usage)
	assert.Equal(t, 15, delegation.Usage.TotalTokens)
	assert.Equal(t, 4, delegation.Usage.PromptTokensDetails.CachedReadTokens, "a delegation keeps its own details")

	function := live.Delegations[1]
	assert.Equal(t, "item_2", function.DelegationID)
	assert.Equal(t, "unit-5", function.RequestID, "the delegation is filed under its first unit")
	assert.Equal(t, []string{"resp_2", "resp_3"}, function.ResponseIDs)
	assert.Equal(t, int64(20000), function.StartedMs, "the continuation does not move the start")
	assert.Equal(t, append(call, continued...), function.Output, "call, the app's result, then the answer")
	require.NotNil(t, function.Usage)
	assert.Equal(t, 65, function.Usage.TotalTokens, "both responses' tokens")
	assert.Equal(t, 6, function.Usage.PromptTokensDetails.CachedReadTokens)

	// The transcript is also the row's conversation, so the table and detail views show it.
	require.Len(t, root.InputHistoryParsed, 2)
	assert.Equal(t, schemas.ChatMessageRoleUser, root.InputHistoryParsed[0].Role)
	assert.Equal(t, "What's the weather in Paris?", *root.InputHistoryParsed[0].Content.ContentStr)
	require.NotNil(t, root.OutputMessageParsed)
	assert.Equal(t, "It's sunny in Paris.", *root.OutputMessageParsed.Content.ContentStr)

	for _, unit := range []string{"unit-1", "unit-2", "unit-3", "unit-4"} {
		_, err := store.FindByID(context.Background(), unit)
		assert.ErrorIs(t, err, logstore.ErrNotFound, "units write no rows of their own")
	}
}

func TestLiveSessionEndingOnARefusalKeepsItsRow(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	require.NoError(t, err)

	_, _, err = plugin.PreLLMHook(liveUnitCtx("bfsess-2", "unit-1", "voice", true, false), liveStartRequest("gpt-live-1"))
	require.NoError(t, err)
	refusal := &schemas.BifrostError{
		StatusCode: new(402),
		Error:      &schemas.ErrorField{Message: "Budget exceeded"},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType:            schemas.LiveRequest,
			Provider:               schemas.OpenAI,
			OriginalModelRequested: "gpt-live-1",
		},
	}
	// A backend unit refused at admission ran nothing: it is the session's failure, not a delegation.
	_, _, err = plugin.PostLLMHook(liveUnitCtx("bfsess-2", "unit-2", "backend", false, false), nil, refusal)
	require.NoError(t, err)
	_, _, err = plugin.PostLLMHook(liveUnitCtx("bfsess-2", "unit-1", "voice", false, true), nil, refusal)
	require.NoError(t, err)
	require.NoError(t, plugin.Cleanup())

	root, err := store.FindByID(context.Background(), "bfsess-2")
	require.NoError(t, err)
	assert.Equal(t, liveSessionObject, root.Object)
	assert.Equal(t, logStatusError, root.Status)
	require.NotNil(t, root.ErrorDetailsParsed)
	assert.Equal(t, "Budget exceeded", root.ErrorDetailsParsed.Error.Message)
	require.NotNil(t, root.LiveSessionParsed)
	assert.Equal(t, 0.0, root.LiveSessionParsed.VoiceSeconds)
	assert.Empty(t, root.LiveSessionParsed.Delegations)
}

func TestLiveSessionRowStaysSuccessfulWhenADelegationFails(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	require.NoError(t, err)

	_, _, err = plugin.PreLLMHook(liveUnitCtx("bfsess-5", "unit-1", "voice", true, false), liveStartRequest("gpt-live-1"))
	require.NoError(t, err)
	refused := &schemas.BifrostError{
		StatusCode: new(400),
		Error:      &schemas.ErrorField{Message: "The model gpt-6-does-not-exist does not exist"},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType:            schemas.LiveRequest,
			Provider:               schemas.OpenAI,
			OriginalModelRequested: "gpt-6-does-not-exist",
		},
	}
	// The provider refused to run the delegation: its backend unit closes with the error while
	// the session goes on and ends normally.
	failedCtx := liveUnitCtx("bfsess-5", "unit-2", "backend", false, false)
	failedCtx.SetValue(schemas.BifrostContextKeyLiveDelegationFailed, true)
	_, _, err = plugin.PostLLMHook(failedCtx, nil, refused)
	require.NoError(t, err)
	_, _, err = plugin.PostLLMHook(liveUnitCtx("bfsess-5", "unit-1", "voice", false, true), liveVoiceResponse(20, nil), nil)
	require.NoError(t, err)
	require.NoError(t, plugin.Cleanup())

	root, err := store.FindByID(context.Background(), "bfsess-5")
	require.NoError(t, err)
	assert.Equal(t, logStatusSuccess, root.Status)
	assert.Nil(t, root.ErrorDetailsParsed)
	require.NotNil(t, root.LiveSessionParsed)
	assert.Equal(t, 20.0, root.LiveSessionParsed.VoiceSeconds)
	require.Len(t, root.LiveSessionParsed.Delegations, 1)
	assert.Equal(t, "gpt-6-does-not-exist", root.LiveSessionParsed.Delegations[0].Model)
	assert.Equal(t, "The model gpt-6-does-not-exist does not exist", root.LiveSessionParsed.Delegations[0].Error)
	assert.Nil(t, root.LiveSessionParsed.Delegations[0].Usage)
}

func TestLiveSessionPendingEntryOutlivesIdleEviction(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	require.NoError(t, err)

	// Two sessions admitted long before the TTL: one keeps closing units, the other went silent.
	stale := time.Now().Add(-pendingLogTTL - time.Minute)
	for _, id := range []string{"bfsess-busy", "bfsess-silent"} {
		_, _, err = plugin.PreLLMHook(liveUnitCtx(id, id+"-unit-1", "voice", true, false), liveStartRequest("gpt-live-1"))
		require.NoError(t, err)
		pendingVal, ok := plugin.pendingLogsEntries.Load(id)
		require.True(t, ok)
		pending := pendingVal.(*PendingLogData)
		pending.CreatedAt = stale
		pending.LastActivity.Store(stale.UnixNano())
	}
	// A voice window closing is the activity that keeps the busy session's entry alive.
	_, _, err = plugin.PostLLMHook(liveUnitCtx("bfsess-busy", "bfsess-busy-unit-1", "voice", false, false), liveVoiceResponse(30, nil), nil)
	require.NoError(t, err)

	plugin.cleanupStalePendingLogs()
	_, busy := plugin.pendingLogsEntries.Load("bfsess-busy")
	assert.True(t, busy, "a session whose units keep closing is never idle, however old it is")
	_, silent := plugin.pendingLogsEntries.Load("bfsess-silent")
	assert.False(t, silent, "a session with no unit activity past the TTL is reaped")

	// The busy session ends normally and lands as one row with the whole call's usage.
	session := &schemas.LiveSessionLog{Transport: "websocket", ProviderSessionID: "live_long"}
	_, _, err = plugin.PostLLMHook(liveUnitCtx("bfsess-busy", "bfsess-busy-unit-2", "voice", false, true), liveVoiceResponse(1000, session), nil)
	require.NoError(t, err)
	// The reaped session's end finds nothing to write.
	_, _, err = plugin.PostLLMHook(liveUnitCtx("bfsess-silent", "bfsess-silent-unit-2", "voice", false, true), liveVoiceResponse(1000, session), nil)
	require.NoError(t, err)
	require.NoError(t, plugin.Cleanup())

	root, err := store.FindByID(context.Background(), "bfsess-busy")
	require.NoError(t, err)
	require.NotNil(t, root.TokenUsageParsed)
	require.NotNil(t, root.TokenUsageParsed.AudioSeconds)
	assert.Equal(t, 1030.0, *root.TokenUsageParsed.AudioSeconds)
	_, err = store.FindByID(context.Background(), "bfsess-silent")
	assert.ErrorIs(t, err, logstore.ErrNotFound)
}

func TestLiveContentDownloadLogsItsOwnRow(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	require.NoError(t, err)

	// A recording download is an ordinary request like a file download, with the provider's
	// session id in its metadata so the row can be traced back to the session.
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "dl-1")
	ctx.SetValue(schemas.BifrostContextKeySelectedKeyID, "key-1")
	ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, "vk-1")
	ctx.SetValue(schemas.BifrostContextKeyRealtimeProviderSessionID, "live_abc")
	req := &schemas.BifrostRequest{RequestType: schemas.LiveContentRequest, LiveContentRequest: &schemas.BifrostLiveContentRequest{Provider: schemas.OpenAI, SessionID: "live_abc"}}
	_, _, err = plugin.PreLLMHook(ctx, req)
	require.NoError(t, err)
	resp := &schemas.BifrostResponse{LiveContentResponse: &schemas.LiveContentResponse{
		SessionID:     "live_abc",
		ContentType:   "audio/wav",
		ContentLength: 16,
		ExtraFields:   schemas.BifrostResponseExtraFields{RequestType: schemas.LiveContentRequest, Provider: schemas.OpenAI},
	}}
	_, _, err = plugin.PostLLMHook(ctx, resp, nil)
	require.NoError(t, err)
	require.NoError(t, plugin.Cleanup())

	row, err := store.FindByID(context.Background(), "dl-1")
	require.NoError(t, err)
	assert.Equal(t, string(schemas.LiveContentRequest), row.Object)
	assert.Equal(t, logStatusSuccess, row.Status)
	assert.Equal(t, "openai", row.Provider)
	assert.Equal(t, "key-1", row.SelectedKeyID)
	require.NotNil(t, row.VirtualKeyID)
	assert.Equal(t, "vk-1", *row.VirtualKeyID)
	assert.Equal(t, "live_abc", row.MetadataParsed["provider_session_id"])
	assert.Nil(t, row.LiveSessionParsed, "a download is not a session")
	assert.Zero(t, row.Cost, "nothing is billed for a download")
}

// TestLiveSessionRowFollowsTheContentPolicy: a session under a key with content logging disabled
// is written through the same policy path as every other row, so it is marked hidden rather than
// looking like a call where nothing was said.
func TestLiveSessionRowFollowsTheContentPolicy(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	require.NoError(t, err)

	hidden := func(sessionID, requestID string, start, end bool) *schemas.BifrostContext {
		ctx := liveUnitCtx(sessionID, requestID, "voice", start, end)
		ctx.SetValue(schemas.BifrostContextKeyGovernanceDisableContentLogging, true)
		return ctx
	}
	_, _, err = plugin.PreLLMHook(hidden("bfsess-hidden", "unit-1", true, false), liveStartRequest("gpt-live-1"))
	require.NoError(t, err)
	transcript := []schemas.LiveTranscriptLine{{Role: "user", Text: "my card number is 4111", StartMs: 0, EndMs: 900}}
	session := &schemas.LiveSessionLog{Transport: "websocket", ProviderSessionID: "live_hidden", Transcript: transcript}
	_, _, err = plugin.PostLLMHook(hidden("bfsess-hidden", "unit-1", false, true), liveVoiceResponse(12, session), nil)
	require.NoError(t, err)
	require.NoError(t, plugin.Cleanup())

	row, err := store.FindByID(context.Background(), "bfsess-hidden")
	require.NoError(t, err)
	assert.True(t, row.ContentHidden, "the row says its content is hidden, as every other row under this policy does")
	require.NotNil(t, row.LiveSessionParsed)
	assert.Empty(t, row.LiveSessionParsed.Transcript, "no transcript is stored")
	assert.Equal(t, 12.0, row.LiveSessionParsed.VoiceSeconds, "the billing facts are kept")
}

// TestLiveSessionAbortedRowKeepsItsSessionFacts: a session aborted during setup closes with an
// error and no response, so its row takes the transport and provider session id from the unit
// context the meter stamps, instead of writing neither.
func TestLiveSessionAbortedRowKeepsItsSessionFacts(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	require.NoError(t, err)

	_, _, err = plugin.PreLLMHook(liveUnitCtx("bfsess-aborted", "unit-1", "voice", true, false), liveStartRequest("gpt-live-1"))
	require.NoError(t, err)
	closing := liveUnitCtx("bfsess-aborted", "unit-1", "voice", false, true)
	closing.SetValue(schemas.BifrostContextKeyRealtimeTransport, "webrtc")
	closing.SetValue(schemas.BifrostContextKeyRealtimeProviderSessionID, "live_aborted")
	status := 502
	refusal := &schemas.BifrostError{StatusCode: &status, Error: &schemas.ErrorField{Message: "upstream WebRTC connection failed"},
		ExtraFields: schemas.BifrostErrorExtraFields{RequestType: schemas.LiveRequest, Provider: schemas.OpenAI}}
	_, _, err = plugin.PostLLMHook(closing, nil, refusal)
	require.NoError(t, err)
	require.NoError(t, plugin.Cleanup())

	row, err := store.FindByID(context.Background(), "bfsess-aborted")
	require.NoError(t, err)
	assert.Equal(t, logStatusError, row.Status)
	require.NotNil(t, row.LiveSessionParsed)
	assert.Equal(t, "webrtc", row.LiveSessionParsed.Transport, "the transport the meter recorded")
	assert.Equal(t, "live_aborted", row.LiveSessionParsed.ProviderSessionID, "the provider session id the meter recorded")
}

func TestLiveSessionSetupFailureKeepsBilledUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pricing.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"gpt-live-1":{"provider":"openai","mode":"live","input_cost_per_second":0.001}}`), 0o600))
	ds := datasheet.New(nil, testLogger{}, datasheet.Config{URL: "file://" + path})
	require.NoError(t, ds.LoadFromURLIntoMemory(context.Background()))
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, modelcatalog.NewTestCatalogWithDatasheet(ds), nil)
	require.NoError(t, err)

	_, _, err = plugin.PreLLMHook(liveUnitCtx("bfsess-billed-abort", "voice-1", "voice", true, false), liveStartRequest("gpt-live-1"))
	require.NoError(t, err)
	closing := liveUnitCtx("bfsess-billed-abort", "voice-1", "voice", false, true)
	closing.SetValue(schemas.BifrostContextKeyRealtimeTransport, "webrtc")
	closing.SetValue(schemas.BifrostContextKeyRealtimeProviderSessionID, "live_created")
	failure := &schemas.BifrostError{
		StatusCode: new(502),
		Error:      &schemas.ErrorField{Message: "upstream WebRTC connection failed"},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType: schemas.LiveRequest, Provider: schemas.OpenAI, OriginalModelRequested: "gpt-live-1",
			BilledUsage: &schemas.BifrostLLMUsage{AudioSeconds: new(15.0)},
		},
	}
	result, returnedError, err := plugin.PostLLMHook(closing, nil, failure)
	require.NoError(t, err)
	assert.Nil(t, result)
	assert.Same(t, failure, returnedError)
	require.NoError(t, plugin.Cleanup())

	row, err := store.FindByID(context.Background(), "bfsess-billed-abort")
	require.NoError(t, err)
	assert.Equal(t, logStatusError, row.Status)
	require.NotNil(t, row.ErrorDetailsParsed)
	assert.Equal(t, failure.Error.Message, row.ErrorDetailsParsed.Error.Message)
	require.NotNil(t, row.LiveSessionParsed)
	assert.Equal(t, "webrtc", row.LiveSessionParsed.Transport)
	assert.Equal(t, "live_created", row.LiveSessionParsed.ProviderSessionID)
	assert.Equal(t, 15.0, row.LiveSessionParsed.VoiceSeconds)
	require.NotNil(t, row.LiveSessionParsed.VoiceCost)
	assert.InDelta(t, 0.015, *row.LiveSessionParsed.VoiceCost, 1e-9)
	require.NotNil(t, row.Cost)
	assert.InDelta(t, 0.015, *row.Cost, 1e-9)
	assert.Empty(t, row.LiveSessionParsed.Delegations)
	require.NotNil(t, row.TokenUsageParsed)
	require.NotNil(t, row.TokenUsageParsed.AudioSeconds)
	assert.Equal(t, 15.0, *row.TokenUsageParsed.AudioSeconds)
}
