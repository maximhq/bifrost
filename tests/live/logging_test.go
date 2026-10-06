package live

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Logging: one row per session, carrying everything the panel shows, on both transports.

func TestLogging_OneRowPerSessionWithTotals(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		budget := 10.0
		vk := createVirtualKey(t, virtualKeySpec{budgetUSD: &budget})
		c, s := openFakeSession(t, tr, vk, backendModel, nil)

		s.Transcript("user", "Weather in Paris?", 0, 1500)
		s.Play(delegation{ID: "item_1", ResponseID: "resp_1", Model: backendModel, Created: true, Input: 400, Output: 40, Items: []string{webSearchItem("ws_1", "weather paris"), messageItem("msg_1", "Sunny.")}})
		s.Transcript("assistant", "Sunny in Paris.", 3000, 5000)
		s.EmitUsage(30)
		settle()
		s.Play(delegation{ID: "item_2", ResponseID: "resp_2", Model: backendModel, Created: true, Input: 600, Output: 60, Items: []string{messageItem("msg_2", "Anything else?")}})
		s.EmitUsage(50)
		settle()
		c.CloseSession()

		row := findLiveLog(t, s.ID())
		assert.Equal(t, "live.session", row.Get("object").Str)
		assert.Equal(t, "success", row.Get("status").Str)
		assert.Equal(t, "openai", row.Get("provider").Str)
		assert.Equal(t, voiceModel, row.Get("model").Str)
		assert.Empty(t, row.Get("parent_request_id").Str, "the session row is the root")
		assert.Equal(t, "fake-openai", row.Get("selected_key_name").Str)
		assert.Equal(t, vk.ID, row.Get("virtual_key_id").Str)
		assert.Equal(t, string(tr), row.Get("metadata.realtime_transport").Str)
		assert.Equal(t, string(tr), row.Get("live_session.transport").Str)

		assert.Equal(t, 50.0, row.Get("token_usage.audio_seconds").Float())
		assert.Equal(t, 1100.0, row.Get("token_usage.total_tokens").Float(), "backend tokens, counted once")
		voiceCost := 50 * fakeVoiceCostPerSecond
		backendCost := 1000*fakeInputCostPerToken + 100*fakeOutputCostPerToken
		assert.InDelta(t, voiceCost, row.Get("live_session.voice_cost").Float(), 1e-9)
		assert.InDelta(t, backendCost, row.Get("live_session.backend_cost").Float(), 1e-9)
		assert.InDelta(t, voiceCost+backendCost, row.Get("cost").Float(), 1e-9, "the row's cost is the whole session")
		assert.Equal(t, s.ID(), row.Get("live_session.provider_session_id").Str)
		require.Len(t, row.Get("live_session.delegations").Array(), 2)
		require.Len(t, row.Get("live_session.transcript").Array(), 2)
		waitBudgetUsage(t, vk.BudgetID, voiceCost+backendCost)
		assert.Len(t, liveLogRows(t, s.ID()), 1, "units write no rows of their own")
	})
}

func TestLogging_ErrorSessionKeepsItsRow(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		vk := createVirtualKey(t, virtualKeySpec{})
		c, s := openFakeSession(t, tr, vk, backendModel, nil)
		s.Transcript("user", "Hello?", 0, 500)
		s.EmitUsage(20)
		c.WaitFor("session.usage.updated")
		s.Drop()
		c.WaitGone()

		row := findLiveLog(t, s.ID())
		assert.Equal(t, "success", row.Get("status").Str, "an upstream drop is not the session's fault; the row is flagged, not failed")
		assert.Equal(t, 20.0, row.Get("token_usage.audio_seconds").Float())
		require.Len(t, row.Get("live_session.transcript").Array(), 1, "what was said before the drop is kept")
	})
}
