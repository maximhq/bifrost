package vertex

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"golang.org/x/oauth2"
)

const (
	liveUSPath   = "/ws/google.cloud.aiplatform.v1beta1.LlmBidiService/BidiGenerateContent"
	liveModel    = "gemini-3.8-live"
	liveSettings = `{"type":"session.update","session":{"type":"realtime"}}`
)

func liveTestKey(project, region string) schemas.Key {
	return schemas.Key{VertexKeyConfig: &schemas.VertexKeyConfig{
		ProjectID:       *schemas.NewSecretVar(project),
		Region:          *schemas.NewSecretVar(region),
		AuthCredentials: *schemas.NewSecretVar("test-credentials"),
	}}
}

func liveTestProvider(key schemas.Key, source oauth2.TokenSource) *VertexProvider {
	provider := &VertexProvider{}
	provider.tokenSources.Store(getClientKey(vertexCredentialIdentity(key)), source)
	return provider
}

// liveAliasContext is the context of a connection whose model resolved through an alias that overrides the
// project and the location, which is what the relay sets before it asks the provider for anything.
func liveAliasContext(t *testing.T, project, region string) *schemas.BifrostContext {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	t.Cleanup(ctx.Cancel)
	config := &schemas.AliasConfig{ModelID: liveModel}
	if project != "" {
		config.ProjectID = schemas.NewSecretVar(project)
	}
	if region != "" {
		config.Region = schemas.NewSecretVar(region)
	}
	ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{Key: "live", Config: config})
	return ctx
}

type failingTokenSource struct{}

func (failingTokenSource) Token() (*oauth2.Token, error) { return nil, errors.New("token refused") }

func TestVertexLiveIsRealtimeCapableButNotOverWebRTC(t *testing.T) {
	provider := &VertexProvider{}
	assert.True(t, provider.SupportsRealtimeAPI())
	assert.False(t, provider.SupportsRealtimeWebRTC())
	_, bifrostErr := provider.ExchangeRealtimeWebRTCSDP(nil, schemas.Key{}, liveModel, "sdp", nil)
	assert.NotNil(t, bifrostErr)
}

func TestVertexLiveURLFollowsTheLocation(t *testing.T) {
	provider := &VertexProvider{}
	for _, tc := range []struct {
		region string
		want   string
	}{
		{"us", "wss://aiplatform.us.rep.googleapis.com" + liveUSPath},
		{"eu", "wss://aiplatform.eu.rep.googleapis.com" + liveUSPath},
		{"us-central1", "wss://us-central1-aiplatform.googleapis.com" + liveUSPath},
		{"global", "wss://aiplatform.googleapis.com" + liveUSPath},
	} {
		t.Run(tc.region, func(t *testing.T) {
			got, bifrostErr := provider.RealtimeWebSocketURL(nil, liveTestKey("p", tc.region), liveModel, "")
			require.Nil(t, bifrostErr)
			assert.Equal(t, tc.want, got)
		})
	}

	t.Run("the location a model alias resolves to wins over the key's", func(t *testing.T) {
		got, bifrostErr := provider.RealtimeWebSocketURL(liveAliasContext(t, "", "us"), liveTestKey("p", "us-central1"), liveModel, "")
		require.Nil(t, bifrostErr)
		assert.Equal(t, "wss://aiplatform.us.rep.googleapis.com"+liveUSPath, got)
	})
	t.Run("what the host cannot be built from is refused", func(t *testing.T) {
		for name, key := range map[string]schemas.Key{
			"no project":      liveTestKey("", "us"),
			"no location":     liveTestKey("p", ""),
			"a bad location":  liveTestKey("p", "evil.com/x"),
			"no key settings": {},
		} {
			_, bifrostErr := provider.RealtimeWebSocketURL(nil, key, liveModel, "")
			assert.NotNil(t, bifrostErr, name)
		}
	})
	t.Run("transcription is not served", func(t *testing.T) {
		_, bifrostErr := provider.RealtimeWebSocketURL(nil, liveTestKey("p", "us"), liveModel, "transcription")
		require.NotNil(t, bifrostErr)
	})
}

func TestVertexLiveHeadersCarryABearerToken(t *testing.T) {
	key := liveTestKey("p", "us")
	provider := liveTestProvider(key, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}))
	provider.networkConfig.ExtraHeaders = map[string]schemas.SecretVar{
		"X-Trace":       *schemas.NewSecretVar("trace-1"),
		"authorization": *schemas.NewSecretVar("Bearer extra"),
	}

	headers, bifrostErr := provider.RealtimeHeaders(nil, key)

	require.Nil(t, bifrostErr)
	assert.Equal(t, map[string]string{"Authorization": "Bearer test-token", "X-Trace": "trace-1"}, headers, "extra headers cannot replace the token")
}

func TestVertexLiveHeadersRefuseWhatCannotAuthenticate(t *testing.T) {
	t.Run("an api key", func(t *testing.T) {
		key := liveTestKey("p", "us")
		key.Value = *schemas.NewSecretVar("an-api-key")
		_, bifrostErr := (&VertexProvider{}).RealtimeHeaders(nil, key)
		require.NotNil(t, bifrostErr)
		assert.Contains(t, bifrostErr.Error.Message, "api key auth not supported")
	})
	t.Run("a key with no vertex settings", func(t *testing.T) {
		_, bifrostErr := (&VertexProvider{}).RealtimeHeaders(nil, schemas.Key{})
		require.NotNil(t, bifrostErr)
	})
	t.Run("a token that cannot be fetched is evicted so the next session builds a fresh source", func(t *testing.T) {
		key := liveTestKey("p", "us")
		provider := liveTestProvider(key, failingTokenSource{})
		_, bifrostErr := provider.RealtimeHeaders(nil, key)
		require.NotNil(t, bifrostErr)
		_, cached := provider.tokenSources.Load(getClientKey(vertexCredentialIdentity(key)))
		assert.False(t, cached)
	})
}

// setupModel starts a session on the translator and returns the model its setup frame names.
func setupModel(t *testing.T, translator schemas.RealtimeTranslator) string {
	t.Helper()
	event, err := schemas.ParseRealtimeEvent([]byte(liveSettings))
	require.NoError(t, err)
	out, err := translator.ToProvider(event)
	require.NoError(t, err)
	require.Len(t, out.Provider, 1)
	return gjson.GetBytes(out.Provider[0], "setup.model").String()
}

const vertexTurnFrame = `{"serverContent":{"turnComplete":true},"usageMetadata":{"promptTokenCount":6,"candidatesTokenCount":79,"candidatesTokensDetails":[{"modality":"AUDIO","tokenCount":71},{"modality":"TEXT","tokenCount":8}],"promptTokensDetails":[{"modality":"TEXT","tokenCount":6}],"totalTokenCount":85}}`

// A client that speaks Live itself, such as the Google GenAI SDK on /genai, names the model by a resource path with its
// own project and location. Live gets the key's: the gateway holds the credentials, and the key decides where it runs.
func TestVertexLiveNativeSetupAddressesTheModelOnTheKeysProject(t *testing.T) {
	provider := &VertexProvider{}
	setup := []byte(`{"setup":{"model":"projects/client-project/locations/europe-west4/publishers/google/models/gemini-3.8-live","generationConfig":{"responseModalities":["AUDIO"]},"sessionResumption":{}}}`)

	rewritten, bifrostErr := provider.NativeRealtimeSetup(liveAliasContext(t, "", ""), liveTestKey("key-project", "us"), setup, liveModel)
	require.Nil(t, bifrostErr)
	assert.JSONEq(t, `{"setup":{"model":"projects/key-project/locations/us/publishers/google/models/gemini-3.8-live","generationConfig":{"responseModalities":["AUDIO"]},"sessionResumption":{}}}`, string(rewritten),
		"only the model changes, every other setting reaches Live as the client wrote it")

	rewritten, bifrostErr = provider.NativeRealtimeSetup(liveAliasContext(t, "alias-project", "eu"), liveTestKey("key-project", "us"), setup, liveModel)
	require.Nil(t, bifrostErr)
	assert.Equal(t, "projects/alias-project/locations/eu/publishers/google/models/gemini-3.8-live", gjson.GetBytes(rewritten, "setup.model").String(),
		"an alias that overrides the location moves the session with it")

	_, bifrostErr = provider.NativeRealtimeSetup(liveAliasContext(t, "", ""), liveTestKey("", "us"), setup, liveModel)
	require.NotNil(t, bifrostErr, "a key with no project has nowhere to run the session")
	assert.Contains(t, bifrostErr.Error.Message, "project_id")
}

// The frames of a Live client are classified as Gemini's are, and the usage Vertex AI reports, which names the response
// fields candidates*, is read off the frame that ends a turn.
func TestVertexLiveNativeFramesAreClassifiedAsLiveFrames(t *testing.T) {
	provider := &VertexProvider{}

	event, err := provider.ToBifrostRealtimeEvent(json.RawMessage(vertexTurnFrame))
	require.NoError(t, err)
	assert.Equal(t, schemas.RTEventResponseDone, event.Type)
	assert.Equal(t, vertexTurnFrame, string(event.RawData), "the frame is relayed as it came in")

	audio, err := provider.ToBifrostRealtimeEvent(json.RawMessage(`{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"mimeType":"audio/pcm;rate=24000","data":"AAAA"}}]}}}`))
	require.NoError(t, err)
	assert.True(t, provider.ShouldStartRealtimeTurn(audio), "the model's first audio opens the turn")

	usage := provider.ExtractRealtimeTurnUsage([]byte(vertexTurnFrame))
	require.NotNil(t, usage)
	assert.Equal(t, 6, usage.PromptTokens)
	assert.Equal(t, 79, usage.CompletionTokens)
	assert.Equal(t, 85, usage.TotalTokens)
}

func TestVertexLiveTranslatorNamesTheModelByItsPath(t *testing.T) {
	provider := &VertexProvider{}
	key := liveTestKey("my-project", "us")

	t.Run("from the key", func(t *testing.T) {
		translator, bifrostErr := provider.NewRealtimeTranslator(nil, key, liveModel)
		require.Nil(t, bifrostErr)
		assert.Equal(t, "projects/my-project/locations/us/publishers/google/models/gemini-3.8-live", setupModel(t, translator))
		created := gjson.ParseBytes(translator.Open().Client[0])
		assert.Equal(t, liveModel, created.Get("session.model").String(), "the client sees the short name")
	})
	t.Run("from the project and location a model alias resolves to", func(t *testing.T) {
		translator, bifrostErr := provider.NewRealtimeTranslator(liveAliasContext(t, "alias-project", "eu"), key, liveModel)
		require.Nil(t, bifrostErr)
		assert.Equal(t, "projects/alias-project/locations/eu/publishers/google/models/gemini-3.8-live", setupModel(t, translator))
	})
	t.Run("a model written the Gemini API way gets the same path", func(t *testing.T) {
		translator, bifrostErr := provider.NewRealtimeTranslator(nil, key, "models/"+liveModel)
		require.Nil(t, bifrostErr)
		assert.Equal(t, "projects/my-project/locations/us/publishers/google/models/gemini-3.8-live", setupModel(t, translator))
	})
	t.Run("what the path cannot be built from is refused", func(t *testing.T) {
		_, bifrostErr := provider.NewRealtimeTranslator(nil, key, " ")
		assert.NotNil(t, bifrostErr)
		_, bifrostErr = provider.NewRealtimeTranslator(nil, liveTestKey("", "us"), liveModel)
		assert.NotNil(t, bifrostErr)
		_, bifrostErr = provider.NewRealtimeTranslator(nil, liveTestKey("p", ""), liveModel)
		assert.NotNil(t, bifrostErr)
	})
}
