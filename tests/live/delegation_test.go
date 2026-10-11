package live

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Delegations: what the backend did for the session, as the log records it, on both transports.

func TestDelegation_WebSearchIsAssembledFromItemEvents(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		vk := createVirtualKey(t, virtualKeySpec{})
		c, s := openFakeSession(t, tr, vk, backendModel, nil)

		s.Play(delegation{ID: "item_ws", ResponseID: "resp_ws", Model: backendModel, Created: true, Input: 500, Output: 60, Reasoning: 20, Cached: 100,
			Items: []string{reasoningItem("rs_1"), webSearchItem("ws_1", "weather paris"), messageItem("msg_1", "Sunny in Paris.")}})
		// Every nested event reaches the client as sent.
		for i := 0; i < 4; i++ {
			c.WaitFor("response.event")
		}
		settle()
		c.CloseSession()

		row := findLiveLog(t, s.ID())
		delegations := row.Get("live_session.delegations").Array()
		require.Len(t, delegations, 1)
		d := delegations[0]
		assert.Equal(t, "item_ws", d.Get("delegation_id").Str)
		assert.Equal(t, []string{"resp_ws"}, stringsOf(d.Get("response_ids")))
		assert.Equal(t, backendModel, d.Get("model").Str)
		assert.Equal(t, 560.0, d.Get("usage.total_tokens").Float())
		assert.Equal(t, 100.0, d.Get("usage.prompt_tokens_details.cached_tokens").Float())
		assert.Equal(t, 20.0, d.Get("usage.completion_tokens_details.reasoning_tokens").Float())
		items := d.Get("output").Array()
		require.Len(t, items, 2, "the tool call and the message; reasoning carries nothing readable")
		assert.Equal(t, "web_search_call", items[0].Get("type").Str)
		assert.Equal(t, "weather paris", items[0].Get("action.query").Str)
		assert.Equal(t, "message", items[1].Get("type").Str)
		assert.Contains(t, row.Get("tool_call_names").Raw, "web_search")
		assert.GreaterOrEqual(t, d.Get("started_ms").Int(), int64(0))
	})
}

func TestDelegation_FunctionCallRoundTripGroupsIntoOneDelegation(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		vk := createVirtualKey(t, virtualKeySpec{})
		session := sessionFor(t, voiceModel, backendModel, nil)
		answered := make(chan gjson.Result, 1)
		c := openLive(t, tr, clientOptions{headers: vkHeaders(vk), session: session, onFunctionCall: func(name string, args gjson.Result) any {
			answered <- args
			return map[string]any{"events": []string{"Design review at 10:00"}}
		}})
		s := fake.WaitSession(t, session["instructions"].(string))

		// The first response ends on the call.
		s.Play(delegation{ID: "item_fc", ResponseID: "resp_call", Model: backendModel, Created: true, Input: 300, Output: 20,
			Items: []string{functionCallItem("fc_1", "call_1", "get_calendar", `{"date":"today"}`)}})
		args := await(t, answered, "the function call")
		assert.Equal(t, "today", args.Get("date").Str)
		// The app's result goes back through the gateway; OpenAI then continues the same delegation.
		result := s.WaitInbound(t, "response.item.create", 1)
		assert.Equal(t, "call_1", result.Get("item.call_id").Str)
		s.WaitInbound(t, "response.create", 1)
		s.Play(delegation{ID: "item_fc", ResponseID: "resp_answer", Model: backendModel, Input: 340, Output: 30,
			Items: []string{messageItem("msg_1", "You have a design review at 10.")}})
		settle()
		c.CloseSession()

		row := findLiveLog(t, s.ID())
		delegations := row.Get("live_session.delegations").Array()
		require.Len(t, delegations, 1, "a function call is two responses of one delegation")
		d := delegations[0]
		assert.Equal(t, []string{"resp_call", "resp_answer"}, stringsOf(d.Get("response_ids")))
		assert.Equal(t, 690.0, d.Get("usage.total_tokens").Float(), "both responses' tokens")
		types := []string{}
		for _, item := range d.Get("output").Array() {
			types = append(types, item.Get("type").Str)
		}
		assert.Equal(t, []string{"function_call", "function_call_output", "message"}, types, "call, the app's result, then the answer")
		assert.Contains(t, row.Get("tool_call_names").Raw, "get_calendar")
		assert.Equal(t, 690.0, row.Get("token_usage.total_tokens").Float())
	})
}

func TestDelegation_TranscriptIsGroupedIntoSpeakerTurns(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		vk := createVirtualKey(t, virtualKeySpec{})
		c, s := openFakeSession(t, tr, vk, backendModel, nil)

		s.Transcript("user", "What is on", 0, 800)
		s.Transcript("user", " my calendar?", 800, 1600)
		s.Transcript("assistant", "Sure,", 2000, 2300)
		s.Transcript("assistant", " one moment.", 2300, 3000)
		s.Transcript("assistant", "A design review at ten.", 9000, 11000)
		s.Transcript("user", "Thanks", 14000, 14500)
		for i := 0; i < 6; i++ {
			c.WaitForAny("session.input_transcript.delta", "session.output_transcript.delta")
		}
		c.CloseSession()

		row := findLiveLog(t, s.ID())
		lines := row.Get("live_session.transcript").Array()
		require.Len(t, lines, 3, "a pause does not split a speaker's turn; the other speaker does")
		assert.Equal(t, "user", lines[0].Get("role").Str)
		assert.Equal(t, "What is on my calendar?", lines[0].Get("text").Str)
		assert.Equal(t, "assistant", lines[1].Get("role").Str)
		assert.Equal(t, "Sure, one moment. A design review at ten.", lines[1].Get("text").Str, "a resumed turn gets the space it lacks")
		assert.Equal(t, int64(11000), lines[1].Get("end_ms").Int())
		assert.Equal(t, "Thanks", lines[2].Get("text").Str)
		history := row.Get("input_history").Array()
		require.Len(t, history, 3, "the transcript is also the row's conversation")
		assert.True(t, strings.HasSuffix(row.Get("output_message.content").Str, "A design review at ten."), "output_message.content ends with the last assistant turn: %q", row.Get("output_message.content").Str)
	})
}

// Client delegation: the voice model hands the app a task, the app calls the backend itself
// through the gateway and speaks the result back. Bifrost bills voice on the session and the
// backend call as the ordinary request it is.

func TestClientDelegation_NeedsNoBackendModel(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		// The key may use only the voice model: there is no backend for the session to admit.
		vk := createVirtualKey(t, virtualKeySpec{allowedModels: []string{voiceModel}})
		session := sessionFor(t, voiceModel, "", map[string]any{"delegation": map[string]any{"type": "client"}})
		c := openLive(t, tr, clientOptions{headers: vkHeaders(vk), session: session})
		s := fake.WaitSession(t, session["instructions"].(string))
		assert.True(t, s.IsClientDelegation(), "the delegation type goes upstream untouched")
		assert.Empty(t, s.StartBackend())

		// A client-mode update carries no backend model; nothing to check, forwarded as sent.
		c.Send(`{"type":"session.update","event_id":"upd","session":{"delegation":{"type":"client"}}}`)
		assert.Equal(t, "client", s.WaitInbound(t, "session.update", 1).Get("session.delegation.type").Str)
		c.WaitFor("session.updated")
		s.EmitUsage(30)
		c.WaitFor("session.usage.updated")
		c.CloseSession()

		row := findLiveLog(t, s.ID())
		assert.Empty(t, row.Get("live_session.delegations").Array())
		assert.False(t, row.Get("live_session.backend_cost").Exists(), "no backend ran on the session")
		assert.Equal(t, 30.0, row.Get("token_usage.audio_seconds").Float())
		assert.Zero(t, row.Get("token_usage.total_tokens").Float())
	})
}

func TestClientDelegation_AppBackendCallIsItsOwnRequest(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		budget := 10.0
		vk := createVirtualKey(t, virtualKeySpec{budgetUSD: &budget})
		session := sessionFor(t, voiceModel, "", map[string]any{"delegation": map[string]any{"type": "client"}})
		var c *liveClient
		answered := make(chan appCall, 1)
		c = openLive(t, tr, clientOptions{headers: vkHeaders(vk), session: session, onClientTask: func(delegationID string) {
			// The app builds its own context and calls the backend through the gateway, grouped with the session.
			response, err := callResponses("/v1/responses", vkHeaders(vk), c.ProviderSessionID, backendModel,
				[]map[string]any{{"role": "user", "content": "What is the weather in Paris?"}})
			if err == nil {
				c.Commentary(delegationID, response.Get(`output.#(type=="message").content.0.text`).Str)
			}
			answered <- appCall{response: response, err: err}
		}})
		s := fake.WaitSession(t, session["instructions"].(string))

		s.Transcript("user", "What is the weather in Paris?", 0, 1500)
		s.DelegateToClient("task_1")
		call := await(t, answered, "the app's backend call")
		require.NoError(t, call.err)
		assert.Equal(t, backendModel, call.response.Get("model").Str)
		commentary := s.WaitInbound(t, "session.commentary.append", 1)
		assert.Equal(t, "task_1", commentary.Get("delegation_id").Str)
		assert.Equal(t, clientBackendAnswer, commentary.Get("content").Str, "the app's result goes back on the socket")
		assert.Equal(t, "result_task_1", c.WaitFor("session.commentary.appended").Get("client_event_id").Str)
		s.EmitUsage(20)
		c.WaitFor("session.usage.updated")
		settle()
		c.CloseSession()

		// The session row carries voice only.
		row := findLiveLog(t, s.ID())
		assert.Empty(t, row.Get("live_session.delegations").Array())
		assert.InDelta(t, 20*fakeVoiceCostPerSecond, row.Get("cost").Float(), 1e-9)
		assert.Zero(t, row.Get("token_usage.total_tokens").Float())

		// The backend call is an ordinary responses row, grouped with the session by its id.
		backendRow := findSessionRow(t, s.ID(), "responses")
		assert.Equal(t, backendModel, backendRow.Get("model").Str)
		assert.Equal(t, float64(clientBackendInput+clientBackendOutput), backendRow.Get("token_usage.total_tokens").Float())
		backendCost := clientBackendInput*fakeInputCostPerToken + clientBackendOutput*fakeOutputCostPerToken
		assert.InDelta(t, backendCost, backendRow.Get("cost").Float(), 1e-9)
		assert.Equal(t, vk.ID, backendRow.Get("virtual_key_id").Str)
		waitBudgetUsage(t, vk.BudgetID, 20*fakeVoiceCostPerSecond+backendCost)
	})
}

func stringsOf(list gjson.Result) []string {
	var out []string
	for _, item := range list.Array() {
		out = append(out, item.Str)
	}
	return out
}

// delegationRefusedFrame is OpenAI's answer when the backend configuration cannot run: an error
// frame naming session.delegation, with no delegation ever created.
const delegationRefusedFrame = `{"type":"error","event_id":"evt_err_1","error":{"type":"invalid_request_error","code":"responses_validation_failed","message":"Responses configuration validation failed: The model gpt-6-does-not-exist does not exist or you do not have access to it.","param":"session.delegation.responses","client_event_id":"evt_start"}}`

func TestDelegation_BackendFailureIsOnTheRow(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		vk := createVirtualKey(t, virtualKeySpec{})
		c, s := openFakeSession(t, tr, vk, backendModel, nil)

		s.Emit(delegationRefusedFrame)
		assert.Contains(t, errorMessage(c.WaitFor("error")), "does not exist", "the provider's error still reaches the client")
		// The voice session goes on: a later delegation runs and bills as usual.
		s.Play(delegation{ID: "item_later", ResponseID: "resp_later", Model: backendModel, Created: true, Input: 300, Output: 40,
			Items: []string{messageItem("msg_1", "Still here.")}})
		c.WaitFor("response.event")
		settle()
		s.EmitUsage(12)
		c.WaitFor("session.usage.updated")
		c.CloseSession()

		row := findLiveLog(t, s.ID())
		assert.Equal(t, "success", row.Get("status").Str, "the session itself succeeded")
		assert.Greater(t, row.Get("token_usage.audio_seconds").Float(), 0.0, "voice is still billed")
		delegations := row.Get("live_session.delegations").Array()
		require.Len(t, delegations, 2, "the refused delegation and the one that ran")
		failed := delegations[0]
		assert.Equal(t, backendModel, failed.Get("model").Str)
		assert.Contains(t, failed.Get("error").Str, "does not exist")
		assert.False(t, failed.Get("usage").Exists(), "nothing ran, nothing to bill")
		assert.Equal(t, "item_later", delegations[1].Get("delegation_id").Str)
		assert.Equal(t, 340.0, delegations[1].Get("usage.total_tokens").Float())
	})
}
