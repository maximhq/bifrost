package realtime

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Failures: a session that ends badly, at either end, still tells the client what happened and
// leaves an honest row for the turn it interrupted.

func TestFailure_UpstreamDropMidTurn(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)
	s.client.send(responseCreate())
	s.upstream.expect("response.create")
	s.upstream.send(responseCreated("resp_1"))
	s.client.expect("response.created")

	s.upstream.drop()

	s.client.expectError("server_error", "upstream realtime websocket stream interrupted")
	row := s.turnRow()
	assert.Equal(t, "error", row.Status)
	assert.Equal(t, 502, row.Error.StatusCode)
	s.client.expectClosed()
}

// A provider that closes the orderly way mid-turn still cut the turn short.
func TestFailure_UpstreamHangUpMidTurn(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)
	s.client.send(responseCreate())
	s.upstream.expect("response.create")

	s.upstream.hangUp()

	row := s.turnRow()
	assert.Equal(t, "error", row.Status)
	assert.Equal(t, 502, row.Error.StatusCode)
	assert.Contains(t, row.Error.Error.Message, "upstream realtime websocket closed before turn completed")
	s.client.expectClosed()
}

func TestFailure_ClientLeavesMidTurn(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)
	s.client.send(responseCreate())
	s.upstream.expect("response.create")

	s.client.leave()

	s.upstream.expectClosed()
	row := s.turnRow()
	assert.Equal(t, "cancelled", row.Status, "the client hung up; the turn did not fail")
	assert.Equal(t, 499, row.Error.StatusCode)
}

// An idle session ends without a row: rows are turns, and none was in flight.
func TestFailure_UpstreamHangUpBetweenTurnsLogsNothing(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)
	s.client.send(responseCreate())
	s.upstream.expect("response.create")
	reply := textReply("resp_1", "Hi.", tokens{input: 5, output: 1})
	s.upstream.send(reply...)
	s.client.expectRelayed(reply...)

	s.upstream.hangUp()

	assert.Equal(t, "success", s.turnRow().Status, "the finished turn is the only row")
	s.client.expectClosed()
}

// A provider error event fails the turn it belongs to, but the session carries on.
func TestFailure_UpstreamErrorEventFailsTheTurnOnly(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)
	s.client.send(responseCreate())
	s.upstream.expect("response.create")

	failure := errorEvent("invalid_request_error", "invalid_value", "Invalid modalities.")
	s.upstream.send(failure)
	s.client.expectRelayed(failure)

	s.client.send(responseCreate())
	s.upstream.expect("response.create")
	reply := textReply("resp_2", "Recovered.", tokens{input: 5, output: 1})
	s.upstream.send(reply...)
	s.client.expectRelayed(reply...)

	rows := s.turnRows(2)
	assert.Equal(t, "error", rows[0].Status)
	assert.Equal(t, "Invalid modalities.", rows[0].Error.Error.Message)
	assert.Equal(t, "success", rows[1].Status)
}

// A rate limit or exhausted quota from the provider ends the session after the client is told.
func TestFailure_UpstreamRateLimitEndsTheSession(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)
	s.client.send(responseCreate())
	s.upstream.expect("response.create")

	limited := errorEvent("rate_limit_exceeded", "rate_limit_exceeded", "Rate limit reached for requests.")
	s.upstream.send(limited)

	s.client.expectRelayed(limited)
	assert.Equal(t, "error", s.turnRow().Status)
	s.upstream.expectClosed()
	s.client.expectClosed()
}

// A provider that refuses the dial never gets a session; the client hears why in-band.
func TestFailure_UpstreamRefusesTheDial(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	model := newModel(t)
	upstream.refuse(model, http.StatusServiceUnavailable)

	c := connect(t, dial{query: "model=openai/" + model})

	c.expectError("server_error", "fake upstream refused the dial")
	c.expectClosed()
	assert.True(t, upstream.dialed(model))
	assert.Empty(t, turnRowsFor(t, model), "no turn ran")
}

// A frame the gateway cannot parse ends the session instead of reaching the client half-translated.
func TestFailure_UpstreamSendsGarbage(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)
	s.client.send(responseCreate())
	s.upstream.expect("response.create")

	s.upstream.sendRaw("this is not json")

	s.client.expectError("server_error", "failed to translate upstream realtime event")
	assert.Equal(t, "error", s.turnRow().Status)
	s.client.expectClosed()
}
