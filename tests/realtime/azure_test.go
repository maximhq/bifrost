package realtime

import (
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openAzureSession(t *testing.T, opts ...dial) *session {
	t.Helper()
	var d dial
	if len(opts) > 0 {
		d = opts[0]
	}
	if d.query == "" {
		d.query = "model=azure/" + azureModel
	}
	return openSession(t, d)
}

// azureEndpointHost is the Azure resource's hostname, which no error may reveal; without
// AZURE_ENDPOINT in the test's environment it falls back to Azure's shared domain.
func azureEndpointHost() string {
	if endpoint, err := url.Parse(os.Getenv("AZURE_ENDPOINT")); err == nil && endpoint.Hostname() != "" {
		return endpoint.Hostname()
	}
	return "azure.com"
}

func TestAzure_TextTurnIsRelayedAndLogged(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openAzureSession(t)
	s.configure(textSession())

	done := s.ask("What is the capital of France?")

	require.Equal(t, "completed", done.Get("response.status").Str, "response.done: %s", done.Raw)
	row := s.turnRow()
	assert.Equal(t, "success", row.Status)
	assert.Equal(t, "azure", row.Provider)
	assert.Equal(t, azureModel, row.Model)
	assert.Equal(t, "azure", row.SelectedKeyName)
	assert.Equal(t, s.client.vk.ID, row.VirtualKeyID)
	assert.Equal(t, s.created.Get("session.id").Str, row.Metadata.ProviderSessionID)
	assert.Equal(t, "What is the capital of France?", row.inputText())
	assert.Equal(t, replyText(done), row.outputText())
	assert.Contains(t, strings.ToLower(row.outputText()), "paris")
	usage := usageOf(done)
	assert.Equal(t, usage.input, row.TokenUsage.Input)
	assert.Equal(t, usage.output, row.TokenUsage.Output)
	assert.Positive(t, row.Cost, "the turn is priced from the model catalog")
}

// The transcription model in session.update is an Azure deployment too.
func TestAzure_VoiceTurnIsTranscribedAndLogged(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openAzureSession(t)
	config := textSession()
	config["audio"] = map[string]any{"input": map[string]any{
		"transcription":  map[string]any{"model": azureTranscribeModel},
		"turn_detection": map[string]any{"type": "server_vad"},
	}}
	s.configure(config)

	s.client.streamAudio("testdata/ask-weather.wav") // "What's the weather like in Paris right now?"
	frames := s.client.untilAll("response.done", "conversation.item.input_audio_transcription.completed")

	assert.Equal(t, 1, countType(frames, "input_audio_buffer.committed"))
	assert.Contains(t, strings.ToLower(findType(frames, "conversation.item.input_audio_transcription.completed").Get("transcript").Str), "weather")
	row := s.turnRow()
	assert.Equal(t, "success", row.Status)
	assert.Contains(t, strings.ToLower(row.inputText()), "weather")
	assert.Equal(t, replyText(findType(frames, "response.done")), row.outputText())
}

func TestAzure_TranscriptionSessionDiscoversItsDeploymentInBand(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	c := connect(t, dial{query: "intent=transcription"})

	c.send(sessionUpdate(map[string]any{
		"type": "transcription",
		"audio": map[string]any{"input": map[string]any{
			"transcription":  map[string]any{"model": "azure/" + azureTranscribeModel},
			"turn_detection": map[string]any{"type": "server_vad"},
		}},
	}))
	c.until("session.created")
	c.streamAudio("testdata/ask-weather.wav")
	frames := c.until("conversation.item.input_audio_transcription.completed")

	transcript := frames[len(frames)-1].Get("transcript").Str
	assert.Contains(t, strings.ToLower(transcript), "weather")
	row := waitTurnRows(t, c.vk, 1)[0]
	assert.Equal(t, "success", row.Status)
	assert.Equal(t, "azure", row.Provider)
	assert.Equal(t, azureTranscribeModel, row.Model)
	assert.Contains(t, row.raw.Raw, transcript)
}

// The key alias is how a model name maps to an Azure deployment of another name.
func TestAzure_DeploymentAliasIsResolved(t *testing.T) {
	requireGateway(t)
	t.Parallel()

	s := openAzureSession(t, dial{query: "model=azure/rt-azure-alias"}) // config.json: rt-azure-alias → gpt-realtime

	model := s.created.Get("session.model").Str
	assert.True(t, strings.HasPrefix(model, "gpt-realtime"), "the resource serves the alias's deployment, got %q", model)
}

// The resource mints the client secret; the browser gets the gateway's token for it.
func TestAzure_ClientSecretOpensABrowserSession(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{})

	minted := mintClientSecret(t, vk, "azure/"+azureModel)
	token := minted.Get("value").Str
	assert.True(t, strings.HasPrefix(token, "ek_bf_"), "the browser gets the gateway's token, got %s", tokenShape(token))

	s := openSession(t, browserDial("model=azure/"+azureModel, vk, token))
	s.configure(textSession())
	done := s.ask("Say hello browser.")

	require.Equal(t, "completed", done.Get("response.status").Str, "response.done: %s", done.Raw)
	row := s.turnRow()
	assert.Equal(t, "success", row.Status)
	assert.Equal(t, "azure", row.Provider)
	assert.Equal(t, vk.ID, row.VirtualKeyID, "the turn is billed to the key that minted the token")
}

// A deployment the resource does not have never gets a session. Azure refuses the handshake with a
// 400, so the client hears a request error, without the operator's endpoint in it.
func TestAzure_UnknownDeploymentIsRefused(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	c := connect(t, dial{query: "model=azure/rt-deployment-does-not-exist"})

	frames := c.collect("error")

	require.Zero(t, countType(frames, "session.created"), "no session opened: %s", typesOf(frames))
	errObj := assertError(t, frames[len(frames)-1], "invalid_request_error", "")
	message := errObj.Get("message").Str
	assert.NotContains(t, message, "://", "the error carries an upstream URL")
	assert.NotContains(t, message, azureEndpointHost(), "the error names the operator's Azure resource")
	c.expectClosed()
	requireRowCountSettles(t, c.vk, 0, "no turn ran")
}
