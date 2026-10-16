package realtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Governance: every turn is a request against the virtual key's limits, checked when the turn
// starts, so a session that outlives its allowance is stopped at the next turn.

func TestGovernance_RequestLimitStopsTheNextTurn(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{maxRequests: 1})
	s := openSession(t, dial{key: vk.Value})
	s.client.send(responseCreate())
	s.upstream.expect("response.create")
	reply := textReply("resp_1", "First.", tokens{input: 3, output: 1})
	s.upstream.send(reply...)
	s.client.expectRelayed(reply...)
	s.turnRow()

	s.client.send(responseCreate())

	s.client.expectError("rate_limit_exceeded", "")
	s.client.expectClosed()
	s.upstream.expectClosed() // the second response.create never reached the provider
}

func TestGovernance_ExhaustedBudgetStopsTheNextTurn(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	expensive := tokens{input: 12, output: 3}
	vk := createVirtualKey(t, keySpec{budgetUSD: costOf(expensive) / 2})
	s := openSession(t, dial{key: vk.Value})
	s.client.send(responseCreate())
	s.upstream.expect("response.create")
	reply := textReply("resp_1", "Pricey.", expensive)
	s.upstream.send(reply...)
	s.client.expectRelayed(reply...)
	s.turnRow()
	waitBudgetUsage(t, vk.BudgetID, costOf(expensive))

	s.client.send(responseCreate())

	s.client.expectError("insufficient_quota", "")
	s.client.expectClosed()
	s.upstream.expectClosed()
	assert.Len(t, s.turnRows(1), 1, "the refused turn is not a row of its own")
}
