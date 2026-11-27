package realtime

import (
	"testing"
)

// Governance: every turn is a request against the virtual key's limits, checked when the turn
// starts, so a session that outlives its allowance is stopped at the next turn.

func TestGovernance_RequestLimitStopsTheNextTurn(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{maxRequests: 1})
	s := openSession(t, dial{vk: vk})
	s.configure(textSession())
	s.ask("Say first.")
	s.turnRow()

	s.client.send(userMessage("Say second."), responseCreate())

	frames := s.client.until("error")
	assertError(t, frames[len(frames)-1], "rate_limit_exceeded", "")
	s.client.expectClosed()
	requireRowCountSettles(t, vk, 1, "the refused turn is not a row of its own")
}

func TestGovernance_ExhaustedBudgetStopsTheNextTurn(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	const budget = 0.0000001 // less than any turn costs
	vk := createVirtualKey(t, keySpec{budgetUSD: budget})
	s := openSession(t, dial{vk: vk})
	s.configure(textSession())
	s.ask("Say first.")
	s.turnRow()
	waitBudgetUsage(t, vk.BudgetID, budget)

	s.client.send(userMessage("Say second."), responseCreate())

	frames := s.client.collect("error")
	assertError(t, frames[len(frames)-1], "insufficient_quota", "")
	s.client.expectClosed()
	requireRowCountSettles(t, vk, 1, "the refused turn is not a row of its own")
}
