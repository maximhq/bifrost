package realtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failures: a session that ends badly, at either end, still tells the client what happened and
// leaves an honest row for the turn it interrupted.

func TestFailure_ClientLeavesMidTurn(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)
	s.configure(textSession())
	s.client.send(userMessage("Count slowly from one to fifty."), responseCreate())
	s.client.until("response.created")

	s.client.leave()

	row := s.turnRow()
	assert.Equal(t, "cancelled", row.Status, "the client hung up; the turn did not fail")
	assert.Equal(t, 499, row.Error.StatusCode)
}

// A provider error event fails the turn it belongs to, but the session carries on.
func TestFailure_ProviderErrorEventFailsTheTurnOnly(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)
	s.configure(textSession())

	s.client.send(userMessage("Hello."), responseCreateWith(map[string]any{"output_modalities": []string{"video"}}))
	frames := s.client.collect("error")
	failure := assertError(t, frames[len(frames)-1], "invalid_request_error", "")

	done := s.ask("Say recovered.")

	rows := s.turnRows(2)
	assert.Equal(t, "error", rows[0].Status)
	assert.Equal(t, failure.Get("message").Str, rows[0].Error.Error.Message)
	assert.Equal(t, "success", rows[1].Status)
	assert.Equal(t, replyText(done), rows[1].outputText())
}

// A model the provider does not serve never gets a session; the client hears why in-band.
func TestFailure_ProviderRefusesTheModel(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	c := connect(t, dial{query: "model=openai/gpt-realtime-does-not-exist"})

	frames := c.collect("error")

	require.Zero(t, countType(frames, "session.created"), "no session opened: %s", typesOf(frames))
	errObj := assertError(t, frames[len(frames)-1], "invalid_request_error", "gpt-realtime-does-not-exist")
	assert.Equal(t, "model_not_found", errObj.Get("code").Str, "the provider's reason reaches the client")
	c.expectClosed()
	requireRowCountSettles(t, c.vk, 0, "no turn ran")
}
