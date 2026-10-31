package live

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Governance: how a session counts against a virtual key's limits, on both transports.

func TestGovernance_SessionCountsAsOneRequest(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		one := int64(1)
		vk := createVirtualKey(t, virtualKeySpec{requestMaxLimit: &one})
		c, s := openFakeSession(t, tr, vk, backendModel, nil)

		// The first window closes the session's one counted request; later windows are continuations.
		s.EmitUsage(30)
		settle()
		s.EmitUsage(60)
		settle()
		s.EmitUsage(90)
		c.WaitFor("session.usage.updated")
		c.WaitFor("session.usage.updated")
		c.WaitFor("session.usage.updated")
		assert.Empty(t, c.Frames("error"), "a session's windows never trip the request limit")

		// A second session on the same key is one request too many.
		second := sessionFor(t, voiceModel, backendModel, nil)
		refusal := openRefused(t, tr, clientOptions{headers: vkHeaders(vk), session: second})
		assert.Contains(t, strings.ToLower(errorMessage(refusal)), "limit")
		assert.Equal(t, 0, fake.SessionCount(second["instructions"].(string)))
		c.CloseSession()
	})
}

func TestGovernance_TokenLimitAppliesToBackendUsage(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		limit := int64(100)
		vk := createVirtualKey(t, virtualKeySpec{tokenMaxLimit: &limit})
		c, s := openFakeSession(t, tr, vk, backendModel, nil)

		// The first response is billed; admitting the unit after it finds the limit spent.
		s.Play(delegation{ID: "item_1", ResponseID: "resp_1", Model: backendModel, Created: true, Input: 400, Output: 100, Items: []string{messageItem("msg_1", "Long answer.")}})
		c.WaitFor("response.event")
		settle()
		s.Play(delegation{ID: "item_2", ResponseID: "resp_2", Model: backendModel, Created: true, Input: 10, Output: 5, Items: []string{messageItem("msg_2", "Short.")}})
		refusal := c.WaitFor("error")
		assert.Contains(t, strings.ToLower(errorMessage(refusal)), "limit")
		s.WaitInbound(t, "session.close", 1)
		c.WaitGone()

		row := findLiveLog(t, s.ID())
		require.Len(t, row.Get("live_session.delegations").Array(), 2, "both responses are billed; the refusal stops the next one")
		assert.NotEqual(t, "success", row.Get("status").Str)
	})
}

func TestGovernance_BudgetIsDebitedWhileTheSessionRuns(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		budget := 10.0
		vk := createVirtualKey(t, virtualKeySpec{budgetUSD: &budget})
		c, s := openFakeSession(t, tr, vk, backendModel, nil)
		assert.Zero(t, budgetUsage(t, vk.BudgetID))

		s.EmitUsage(30)
		c.WaitFor("session.usage.updated")
		settle()
		waitBudgetUsage(t, vk.BudgetID, 30*fakeVoiceCostPerSecond) // charged while the call is still running
		c.CloseSession()
	})
}
