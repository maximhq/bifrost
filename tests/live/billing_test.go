package live

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Billing: voice seconds in windows, backend responses once each, and what happens to the bill
// when the session ends badly. Every scenario runs on both transports.

// settle gives the gateway's async post-hooks a moment to record spend between usage snapshots.
func settle() { time.Sleep(400 * time.Millisecond) }

func TestBilling_VoiceWindowsAndFinalUsage(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		budget := 10.0
		vk := createVirtualKey(t, virtualKeySpec{budgetUSD: &budget})
		c, s := openFakeSession(t, tr, vk, backendModel, nil)

		// OpenAI reports cumulative seconds; each full 30 s window bills as it fills.
		for _, seconds := range []float64{30, 60, 75} {
			s.EmitUsage(seconds)
			assert.Equal(t, seconds, c.WaitFor("session.usage.updated").Get("usage.seconds").Float(), "usage reaches the client unchanged")
			settle()
		}
		c.Send(`{"type":"session.close"}`)
		s.WaitInbound(t, "session.close", 1)
		closed := c.WaitFor("session.closed")
		assert.Equal(t, 75.0, closed.Get("usage.seconds").Float())

		row := findLiveLog(t, s.ID())
		assert.Equal(t, 75.0, row.Get("token_usage.audio_seconds").Float())
		assert.Equal(t, 75.0, row.Get("live_session.voice_seconds").Float())
		assert.InDelta(t, 75*fakeVoiceCostPerSecond, row.Get("live_session.voice_cost").Float(), 1e-9)
		assert.InDelta(t, 75*fakeVoiceCostPerSecond, row.Get("cost").Float(), 1e-9)
		waitBudgetUsage(t, vk.BudgetID, 75*fakeVoiceCostPerSecond)
	})
}

func TestBilling_MinimumSecondsDependOnTransport(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		vk := createVirtualKey(t, virtualKeySpec{})
		c, s := openFakeSession(t, tr, vk, backendModel, nil)

		s.EmitUsage(3)
		c.WaitFor("session.usage.updated")
		c.CloseSession()

		// OpenAI bills a WebRTC session 15 s at creation; a WebSocket session bills what it reports.
		expected := 3.0
		if tr == webrtcTransport {
			expected = 15.0
		}
		row := findLiveLog(t, s.ID())
		assert.Equal(t, expected, row.Get("token_usage.audio_seconds").Float())
		assert.Equal(t, string(tr), row.Get("metadata.realtime_transport").Str)
		assert.Equal(t, string(tr), row.Get("live_session.transport").Str)
	})
}

func TestBilling_BackendResponseIsBilledOncePerID(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		vk := createVirtualKey(t, virtualKeySpec{})
		c, s := openFakeSession(t, tr, vk, backendModel, nil)

		d := delegation{ID: "item_1", ResponseID: "resp_1", Model: backendModel, Created: true, Input: 1000, Output: 100,
			Items: []string{messageItem("msg_1", "Once.")}}
		s.Play(d)
		// OpenAI replays the terminal event; the second copy must not bill again.
		d.Created = false
		d.Items = nil
		s.Play(d)
		c.WaitFor("response.event")
		settle()
		c.CloseSession()

		row := findLiveLog(t, s.ID())
		require.Len(t, row.Get("live_session.delegations").Array(), 1)
		assert.Equal(t, 1100.0, row.Get("token_usage.total_tokens").Float())
		assert.InDelta(t, 1000*fakeInputCostPerToken+100*fakeOutputCostPerToken, row.Get("live_session.backend_cost").Float(), 1e-9)
	})
}

func TestBilling_BackendSwitchFromPrimary(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		vk := createVirtualKey(t, virtualKeySpec{allowedModels: []string{voiceModel, backendModel, backendModel2}})
		c, s := openFakeSession(t, tr, vk, backendModel, nil)

		c.Send(`{"type":"session.update","event_id":"switch","session":{"delegation":{"type":"responses","responses":{"model":"openai/` + backendModel2 + `"}}}}`)
		update := s.WaitInbound(t, "session.update", 1)
		assert.Equal(t, backendModel2, update.Get("session.delegation.responses.model").Str, "the bare model goes upstream")
		assert.Equal(t, backendModel2, c.WaitFor("session.updated").Get("session.delegation.responses.model").Str)

		s.Play(delegation{ID: "item_1", ResponseID: "resp_1", Model: backendModel2, Created: true, Input: 10, Output: 5, Items: []string{messageItem("msg_1", "On terra.")}})
		c.WaitFor("response.event")
		settle()
		c.CloseSession()

		row := findLiveLog(t, s.ID())
		delegations := row.Get("live_session.delegations").Array()
		require.Len(t, delegations, 1)
		assert.Equal(t, backendModel2, delegations[0].Get("model").Str, "billed to the model the session switched to")
	})
}

func TestBilling_BackendSwitchToForbiddenModelIsRefusedBeforeForwarding(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		vk := createVirtualKey(t, virtualKeySpec{allowedModels: []string{voiceModel, backendModel}})
		c, s := openFakeSession(t, tr, vk, backendModel, nil)

		c.Send(`{"type":"session.update","event_id":"switch","session":{"delegation":{"type":"responses","responses":{"model":"` + backendModel2 + `"}}}}`)
		frame := c.WaitFor("error")
		assert.NotEmpty(t, errorMessage(frame))
		c.Send(`{"type":"session.thinking.append","delegation_id":null,"content":"still here"}`)
		s.WaitInbound(t, "session.thinking.append", 1)
		assert.Empty(t, s.Inbound("session.update"), "the refused update never reached the provider")
		c.CloseSession()
	})
}

func TestBilling_BudgetExhaustionEndsTheSessionGracefully(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		budget := 0.05 // two 30 s windows at $0.03 each cross it; the third is refused
		vk := createVirtualKey(t, virtualKeySpec{budgetUSD: &budget})
		c, s := openFakeSession(t, tr, vk, backendModel, nil)

		s.EmitUsage(30)
		settle()
		s.EmitUsage(60)
		settle()
		s.EmitUsage(90)
		refusal := c.WaitFor("error")
		assert.Contains(t, strings.ToLower(errorMessage(refusal)), "budget")
		// The gateway closes upstream itself; OpenAI answers with the final usage, which is still billed.
		s.WaitInbound(t, "session.close", 1)
		c.WaitFor("session.closed")
		c.WaitGone()

		row := findLiveLog(t, s.ID())
		assert.Equal(t, 90.0, row.Get("token_usage.audio_seconds").Float(), "every reported second is billed, refusal included")
		assert.InDelta(t, 90*fakeVoiceCostPerSecond, row.Get("cost").Float(), 1e-9)
		assert.NotEqual(t, "success", row.Get("status").Str, "the session ended on a refusal")
		assert.Contains(t, strings.ToLower(row.Get("error_details.error.message").Str), "budget")
	})
}

func TestBilling_UpstreamDropBillsLastReportedUsage(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		vk := createVirtualKey(t, virtualKeySpec{})
		c, s := openFakeSession(t, tr, vk, backendModel, nil)

		s.EmitUsage(45)
		c.WaitFor("session.usage.updated")
		s.Drop()
		assert.Contains(t, errorMessage(c.WaitFor("error")), "before session.closed", "the client is told the session ended")
		c.WaitGone()

		row := findLiveLog(t, s.ID())
		assert.Equal(t, 45.0, row.Get("token_usage.audio_seconds").Float(), "what OpenAI last reported: no session.closed arrived")
		assert.Equal(t, "success", row.Get("status").Str, "an upstream drop is not the session's fault")
	})
}

func TestBilling_UpstreamHangUpWithoutSessionClosedTellsTheClient(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		vk := createVirtualKey(t, virtualKeySpec{})
		c, s := openFakeSession(t, tr, vk, backendModel, nil)

		s.EmitUsage(30)
		c.WaitFor("session.usage.updated")
		s.HangUp()
		assert.Contains(t, errorMessage(c.WaitFor("error")), "before session.closed", "an orderly close without session.closed is still the session ending")
		c.WaitGone()

		row := findLiveLog(t, s.ID())
		assert.Equal(t, 30.0, row.Get("token_usage.audio_seconds").Float())
		assert.Equal(t, "success", row.Get("status").Str)
	})
}

func TestBilling_ClientLeavingStillClosesUpstreamAndBills(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		vk := createVirtualKey(t, virtualKeySpec{})
		c, s := openFakeSession(t, tr, vk, backendModel, nil)
		s.EmitUsage(20)
		c.WaitFor("session.usage.updated")

		c.Close()
		s.WaitInbound(t, "session.close", 1)
		// The fake answered session.close with session.closed carrying 20 s.

		row := findLiveLog(t, s.ID())
		assert.Equal(t, 20.0, row.Get("token_usage.audio_seconds").Float(), "the final usage OpenAI sent on close")
	})
}

func TestBilling_WebRTCSetupFailureKeepsInitializationCharge(t *testing.T) {
	requireFake(t)
	t.Parallel()
	budget := 1.0
	vk := createVirtualKey(t, virtualKeySpec{budgetUSD: &budget})
	instructions := marker(t) + invalidWebRTCAnswerMarker
	failure := openRefused(t, webrtcTransport, clientOptions{
		headers: vkHeaders(vk),
		session: sessionFor(t, voiceModel, backendModel, map[string]any{"instructions": instructions}),
	})
	assert.Contains(t, errorMessage(failure), "invalid upstream SDP answer")
	s := fake.WaitSession(t, instructions)
	row := findLiveLog(t, s.ID())
	assert.Equal(t, "error", row.Get("status").Str)
	assert.Contains(t, row.Get("error_details.error.message").Str, "invalid upstream SDP answer")
	assert.Equal(t, "webrtc", row.Get("live_session.transport").Str)
	assert.Equal(t, 15.0, row.Get("live_session.voice_seconds").Float())
	assert.Equal(t, 15.0, row.Get("token_usage.audio_seconds").Float())
	assert.InDelta(t, 15*fakeVoiceCostPerSecond, row.Get("live_session.voice_cost").Float(), 1e-9)
	assert.InDelta(t, 15*fakeVoiceCostPerSecond, row.Get("cost").Float(), 1e-9)
	assert.Empty(t, row.Get("live_session.delegations").Array(), "no backend call ran during setup")
	waitBudgetUsage(t, vk.BudgetID, 15*fakeVoiceCostPerSecond)
}

func TestBilling_BackendAliasAcknowledgmentKeepsSession(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		for _, rejectDirect := range []bool{false, true} {
			name := "alias only"
			if rejectDirect {
				name = "refused direct model"
			}
			t.Run(name, func(t *testing.T) {
				budget := 1.0
				vk := createVirtualKey(t, virtualKeySpec{budgetUSD: &budget, allowedModels: []string{voiceModel, "live-luna", "live-terra", "live-terra-other"}})
				c, s := openFakeSession(t, tr, vk, "live-luna", nil)
				assert.Equal(t, backendModel, s.StartBackend())
				// A settings-only update acknowledges the full configuration, including the resolved model.
				c.Send(`{"type":"session.update","session":{"instructions":"Keep the admitted alias."}}`)
				assert.Equal(t, backendModel, c.WaitFor("session.updated").Get("session.delegation.responses.model").Str)
				s.Play(delegation{ID: "item_luna", ResponseID: "resp_luna", Model: backendModel, Created: true, Input: 8, Output: 2, Items: []string{messageItem("msg_luna", "On luna.")}})
				c.WaitFor("response.event")
				c.WaitFor("response.event")

				for i, alias := range []string{"live-terra", "live-terra-other"} {
					c.Send(`{"type":"session.update","session":{"delegation":{"responses":{"model":"` + alias + `"}}}}`)
					update := s.WaitInbound(t, "session.update", i+2)
					assert.Equal(t, backendModel2, update.Get("session.delegation.responses.model").Str)
					ack := c.WaitFor("session.updated")
					assert.Equal(t, backendModel2, ack.Get("session.delegation.responses.model").Str)
					// Repeated provider snapshots must not switch to a raw-name lane.
					s.Emit(ack.Raw)
					c.WaitFor("session.updated")
				}
				// Direct use of the underlying model remains forbidden despite its admitted alias.
				if rejectDirect {
					c.Send(`{"type":"session.update","session":{"delegation":{"responses":{"model":"` + backendModel2 + `"}}}}`)
					assert.NotEmpty(t, errorMessage(c.WaitFor("error")))
				}
				s.Play(delegation{ID: "item_terra", ResponseID: "resp_terra", Model: backendModel2, Created: true, Input: 3, Output: 2, Items: []string{messageItem("msg_terra", "Still here.")}})
				c.WaitFor("response.event")
				c.WaitFor("response.event")
				s.EmitUsage(20)
				c.WaitFor("session.usage.updated")
				c.CloseSession()

				row := findLiveLog(t, s.ID())
				if rejectDirect {
					// Existing logging records the refused request, although the live session continues.
					assert.Equal(t, "error", row.Get("status").Str)
					assert.Contains(t, row.Get("error_details.error.message").Str, "Model '"+backendModel2+"' is not allowed")
				} else {
					assert.Equal(t, "success", row.Get("status").Str)
				}
				delegations := row.Get("live_session.delegations").Array()
				require.Len(t, delegations, 2)
				assert.Equal(t, backendModel, delegations[0].Get("model").Str)
				assert.Equal(t, backendModel2, delegations[1].Get("model").Str)
				assert.Equal(t, 15.0, row.Get("token_usage.total_tokens").Float())
				backendCost := 11*fakeInputCostPerToken + 4*fakeOutputCostPerToken
				assert.InDelta(t, backendCost, row.Get("live_session.backend_cost").Float(), 1e-9)
				waitBudgetUsage(t, vk.BudgetID, 20*fakeVoiceCostPerSecond+backendCost)
			})
		}
	})
}
