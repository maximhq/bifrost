package live

import (
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Long sessions: a call held well past the logging plugin's 15-minute idle eviction of pending
// entries still lands as one complete row. Each takes longer than the rest of the suite put
// together, so they run only with LIVE_LONG=1 (make test-live-long).

// longSessionDuration outlasts the eviction TTL and the one-minute sweep that enforces it.
const longSessionDuration = 17 * time.Minute

func requireLong(t *testing.T) {
	t.Helper()
	if os.Getenv("LIVE_LONG") == "" {
		t.Skip("set LIVE_LONG=1 to run long sessions (make test-live-long)")
	}
}

func TestLong_BusySessionOutlivesPendingEviction(t *testing.T) {
	requireFake(t)
	requireLong(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		budget := 10.0
		vk := createVirtualKey(t, virtualKeySpec{budgetUSD: &budget, allowedModels: []string{voiceModel, backendModel, backendModel2}})
		c, s := openFakeSession(t, tr, vk, backendModel, map[string]any{"store": true})

		var (
			seconds     float64
			backend     = backendModel
			models      []string // the backend each delegation ran on, in order
			tokens      float64
			backendCost float64
			updates     int
			lines       int
		)
		start := time.Now()
		for tick := 1; time.Since(start) < longSessionDuration; tick++ {
			time.Sleep(30 * time.Second)
			// OpenAI reports cumulative seconds twice a minute; each report fills a billing window.
			seconds += 30
			s.EmitUsage(seconds)
			assert.Equal(t, seconds, c.WaitFor("session.usage.updated").Get("usage.seconds").Float())
			// Lookups fall on multiples of four; the switch and the budget check sit between them.
			switch {
			case tick%4 == 0:
				// Every two minutes: a spoken question, a lookup on the backend, a spoken answer.
				n := len(models) + 1
				at := int(seconds * 1000)
				s.Transcript("user", fmt.Sprintf("Question %d?", n), at-2000, at-500)
				c.WaitFor("session.input_transcript.delta")
				s.Play(delegation{ID: fmt.Sprintf("item_%d", n), ResponseID: fmt.Sprintf("resp_%d", n), Model: backend, Created: true, Input: 100, Output: 10,
					Items: []string{messageItem(fmt.Sprintf("msg_%d", n), fmt.Sprintf("Answer %d.", n))}})
				c.WaitFor("response.event")
				c.WaitFor("response.event")
				s.Transcript("assistant", fmt.Sprintf("Answer %d.", n), at, at+1500)
				c.WaitFor("session.output_transcript.delta")
				models = append(models, backend)
				tokens += 110
				backendCost += 100*fakeInputCostPerToken + 10*fakeOutputCostPerToken
				lines += 2
			case tick == 18:
				// Nine minutes in: the rest of the call delegates to another model.
				c.Send(`{"type":"session.update","event_id":"switch","session":{"delegation":{"type":"responses","responses":{"model":"` + backendModel2 + `"}}}}`)
				updates++
				assert.Equal(t, backendModel2, s.WaitInbound(t, "session.update", updates).Get("session.delegation.responses.model").Str)
				assert.Equal(t, backendModel2, c.WaitFor("session.updated").Get("session.delegation.responses.model").Str)
				backend = backendModel2
			case tick == 22:
				// Eleven minutes in: everything so far is already on the budget, not held for the end.
				settle()
				waitBudgetUsage(t, vk.BudgetID, seconds*fakeVoiceCostPerSecond+backendCost)
			}
			require.Empty(t, c.Frames("error"), "a refusal mid-call: %s", c.errorMessages())
		}
		closed := c.CloseSession()
		assert.Equal(t, seconds, closed.Get("usage.seconds").Float())

		row := findLiveLog(t, s.ID())
		assert.Equal(t, "success", row.Get("status").Str)
		assert.Equal(t, seconds, row.Get("token_usage.audio_seconds").Float(), "every window of a %s call is billed", time.Since(start).Round(time.Second))
		assert.Equal(t, seconds, row.Get("live_session.voice_seconds").Float())
		assert.Equal(t, string(tr), row.Get("live_session.transport").Str)
		delegations := row.Get("live_session.delegations").Array()
		require.Len(t, delegations, len(models), "one delegation per lookup across the whole call")
		for i, d := range delegations {
			assert.Equal(t, models[i], d.Get("model").Str, "delegation %d ran on the model active at the time", i+1)
		}
		assert.Contains(t, models, backendModel)
		assert.Contains(t, models, backendModel2, "the switch half way through took effect")
		assert.Equal(t, tokens, row.Get("token_usage.total_tokens").Float())
		assert.InDelta(t, backendCost, row.Get("live_session.backend_cost").Float(), 1e-9)
		assert.InDelta(t, seconds*fakeVoiceCostPerSecond, row.Get("live_session.voice_cost").Float(), 1e-9)
		assert.InDelta(t, seconds*fakeVoiceCostPerSecond+backendCost, row.Get("cost").Float(), 1e-9)
		assert.Len(t, row.Get("live_session.transcript").Array(), lines, "nothing said early in the call is lost")
		assert.Len(t, liveLogRows(t, s.ID()), 1, "one row, written once")
		waitBudgetUsage(t, vk.BudgetID, seconds*fakeVoiceCostPerSecond+backendCost)

		status, body, _ := downloadContent(t, s.ID(), vkHeaders(vk))
		assert.Equal(t, http.StatusOK, status, "the recording is still served after the call: %s", body)
	})
}

func TestLong_SilentSessionStillLogs(t *testing.T) {
	requireFake(t)
	requireLong(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		vk := createVirtualKey(t, virtualKeySpec{})
		c, s := openFakeSession(t, tr, vk, backendModel, nil)

		// OpenAI says nothing for the whole call: no usage, no transcript. The gateway's stale check
		// keeps admitting windows on wall time, which is what keeps the row's pending entry alive.
		time.Sleep(longSessionDuration)
		require.Empty(t, c.Frames("error"), "a silent call is not refused: %s", c.errorMessages())
		s.EmitUsage(20)
		c.WaitFor("session.usage.updated")
		c.CloseSession()

		row := findLiveLog(t, s.ID())
		assert.Equal(t, "success", row.Get("status").Str)
		assert.Equal(t, 20.0, row.Get("token_usage.audio_seconds").Float(), "only what OpenAI reported is billed")
		assert.Empty(t, row.Get("live_session.delegations").Array())
		assert.Len(t, liveLogRows(t, s.ID()), 1)
	})
}
