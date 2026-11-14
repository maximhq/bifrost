package elevenlabs

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestRealtimeWebSocketURLRejectsTranscription(t *testing.T) {
	provider := &ElevenlabsProvider{}

	url, err := provider.RealtimeWebSocketURL(nil, schemas.Key{}, "agent-id", "transcription")
	if err == nil {
		t.Fatal("RealtimeWebSocketURL() error = nil, want unsupported operation")
	}
	if url != "" {
		t.Fatalf("RealtimeWebSocketURL() URL = %q, want empty", url)
	}
	if err.Error == nil || err.Error.Code == nil || *err.Error.Code != "unsupported_operation" {
		t.Fatalf("RealtimeWebSocketURL() error = %+v, want unsupported_operation", err)
	}
}

func realtimeTestProvider() *ElevenlabsProvider {
	return &ElevenlabsProvider{networkConfig: schemas.NetworkConfig{BaseURL: "https://api.elevenlabs.io"}}
}

// TestAgentRealtimePathIsUnchanged pins what an agent session did before Scribe was served: the
// Conversational AI URL and header, no translation whatever the intent, and the conversion each frame
// gets for Bifrost's own tracking while the client still receives the original bytes.
func TestAgentRealtimePathIsUnchanged(t *testing.T) {
	provider := realtimeTestProvider()

	url, bifrostErr := provider.RealtimeWebSocketURL(nil, schemas.Key{}, "agent_123", "")
	require.Nil(t, bifrostErr)
	require.Equal(t, "wss://api.elevenlabs.io/v1/convai/conversation?agent_id=agent_123", url)
	headers, bifrostErr := provider.RealtimeHeaders(nil, schemas.Key{Value: *schemas.NewSecretVar("xi-secret")})
	require.Nil(t, bifrostErr)
	require.Equal(t, map[string]string{"xi-api-key": "xi-secret"}, headers)

	for _, session := range []struct {
		model, intent string
		translated    bool
	}{
		{"agent_123", "", false},
		{"agent_123", "transcription", false},
		{"scribe_v2_realtime", "", false},
		{"scribe_v2_realtime", "transcription", true},
		{"scribe_v3_live", "transcription", true},
	} {
		require.Equal(t, session.translated, provider.TranslatesRealtime(session.model, session.intent), "%+v", session)
	}

	for _, frame := range []struct {
		raw       string
		eventType schemas.RealtimeEventType
	}{
		{`{"type":"conversation_initiation_metadata","conversation_initiation_metadata_event":{}}`, schemas.RTEventSessionCreated},
		{`{"type":"ping","ping_event":{"event_id":1}}`, "ping"},
		{`{"type":"audio","audio_event":{"audio_base_64":"AAAA"}}`, schemas.RTEventResponseAudioDelta},
		{`{"type":"user_transcript","user_transcription_event":{"user_transcript":"hi"}}`, schemas.RTEventInputAudioTransCompleted},
		{`{"type":"agent_response","agent_response_event":{"agent_response":"hello"}}`, schemas.RTEventResponseDone},
		{`{"type":"agent_response_correction","agent_response_correction_event":{"corrected_agent_response":"hello!"}}`, schemas.RTEventResponseTextDelta},
		{`{"type":"interruption"}`, schemas.RTEventResponseCancel},
		{`{"type":"client_tool_call","client_tool_call":{"tool_name":"lookup","tool_call_id":"c1","parameters":{"b":1,"a":2}}}`, "client_tool_call"},
		{`{"type":"something_new"}`, "something_new"},
	} {
		event, err := provider.ToBifrostRealtimeEvent(json.RawMessage(frame.raw))
		require.NoError(t, err)
		require.Equal(t, frame.eventType, event.Type, frame.raw)
		require.JSONEq(t, frame.raw, string(event.RawData), "the original frame is kept, so the relay writes it to the client as it is")
	}

	audio, err := provider.ToProviderRealtimeEvent(&schemas.BifrostRealtimeEvent{Type: schemas.RTEventInputAudioAppend, Delta: &schemas.RealtimeDelta{Audio: "AAAA"}})
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"user_audio_chunk","user_audio_chunk":"AAAA"}`, string(audio))
	pong, err := provider.ToProviderRealtimeEvent(&schemas.BifrostRealtimeEvent{Type: "pong"})
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"pong"}`, string(pong))
	other, err := provider.ToProviderRealtimeEvent(&schemas.BifrostRealtimeEvent{Type: schemas.RTEventResponseCancel})
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"response.cancel"}`, string(other))

	for _, frame := range []string{
		`{"type":"user_message","text":"Say hello."}`,
		`{"type":"contextual_update","text":"The user is on the pricing page."}`,
		`{"type":"client_tool_result","tool_call_id":"c1","result":"sunny","is_error":false}`,
	} {
		event, err := schemas.ParseRealtimeEvent([]byte(frame))
		require.NoError(t, err)
		forwarded, err := provider.ToProviderRealtimeEvent(event)
		require.NoError(t, err)
		require.JSONEq(t, frame, string(forwarded), "the fields of a client's event reach the agent with it")
	}
}

// TestRealtimeWebSocketURLSendsScribeToItsOwnEndpoint pins where a Scribe model goes, and that one opened
// without the transcription intent is refused instead of being dialed as an agent id.
func TestRealtimeWebSocketURLSendsScribeToItsOwnEndpoint(t *testing.T) {
	provider := realtimeTestProvider()

	url, bifrostErr := provider.RealtimeWebSocketURL(nil, schemas.Key{}, "scribe_v3_live", "transcription")
	require.Nil(t, bifrostErr)
	require.Equal(t, "wss://api.elevenlabs.io/v1/speech-to-text/realtime?model_id=scribe_v3_live&audio_format=pcm_24000&commit_strategy=vad", url)

	_, bifrostErr = provider.RealtimeWebSocketURL(nil, schemas.Key{}, "scribe_v2_realtime", "")
	require.NotNil(t, bifrostErr)
	require.Equal(t, 400, *bifrostErr.StatusCode)
	require.Contains(t, bifrostErr.Error.Message, "intent=transcription")

	translator, bifrostErr := provider.NewRealtimeTranslator(nil, schemas.Key{}, "agent_123")
	require.Nil(t, translator)
	require.NotNil(t, bifrostErr, "an agent id has no translator")
}

// TestScribeSettingsComeFromTheSessionUpdate pins how a session.update becomes Scribe's connect options.
func TestScribeSettingsComeFromTheSessionUpdate(t *testing.T) {
	for _, test := range []struct {
		name    string
		session string
		query   string
		failure string
	}{
		{name: "nothing sent", session: ``, query: "audio_format=pcm_24000&commit_strategy=vad"},
		{
			name:    "rate, language and silence",
			session: `{"audio":{"input":{"format":{"type":"audio/pcm","rate":16000},"transcription":{"language":"hi"},"turn_detection":{"type":"server_vad","silence_duration_ms":600}}}}`,
			query:   "audio_format=pcm_16000&commit_strategy=vad&language_code=hi&vad_silence_threshold_secs=0.6",
		},
		{name: "null turn detection hands commits to the client", session: `{"audio":{"input":{"turn_detection":null}}}`, query: "audio_format=pcm_24000&commit_strategy=manual"},
		{
			name:    "semantic detection is turn prediction",
			session: `{"audio":{"input":{"turn_detection":{"type":"semantic_vad","silence_duration_ms":600}}}}`,
			query:   "audio_format=pcm_24000&commit_strategy=turn_prediction",
		},
		{name: "mu-law", session: `{"audio":{"input":{"format":{"type":"audio/pcmu"}}}}`, query: "audio_format=ulaw_8000&commit_strategy=vad"},
		{
			name:    "scribe's own options",
			session: `{"audio":{"input":{"transcription":{"keyterms":["Bifrost","Jio"],"secondary_languages":["en"],"entity_detection":"pii","entity_redaction":["phi","pii"],"include_timestamps":true,"no_verbatim":true,"vad_threshold":0.6,"keepalive_interval_ms":1000}}}}`,
			query:   "audio_format=pcm_24000&commit_strategy=vad&entity_detection=pii&entity_redaction=phi&entity_redaction=pii&include_timestamps=true&keepalive_interval_ms=1000&keyterms=Bifrost&keyterms=Jio&no_verbatim=true&secondary_languages=en&vad_threshold=0.6",
		},
		{name: "the bifrost option is not a connect parameter", session: `{"bifrost":{"extensions":true}}`, query: "audio_format=pcm_24000&commit_strategy=vad"},
		{name: "a-law", session: `{"audio":{"input":{"format":{"type":"audio/pcma"}}}}`, failure: "no A-law"},
		{name: "a rate scribe has no format for", session: `{"audio":{"input":{"format":{"type":"audio/pcm","rate":12345}}}}`, failure: "12345"},
		{name: "an unknown format", session: `{"audio":{"input":{"format":{"type":"audio/opus"}}}}`, failure: "audio/opus"},
		{name: "an unknown turn detection", session: `{"audio":{"input":{"turn_detection":{"type":"clap"}}}}`, failure: "clap"},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings, err := newScribeSettings(json.RawMessage(test.session))
			if test.failure != "" {
				require.ErrorContains(t, err, test.failure)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.query, settings.query)
		})
	}
}

// TestRealtimeWebSocketURLForBootstrapUsesTheClientsFirstFrames pins that the URL is built from the session
// updates that arrive before the one naming the model, merged the way OpenAI applies them.
func TestRealtimeWebSocketURLForBootstrapUsesTheClientsFirstFrames(t *testing.T) {
	provider := realtimeTestProvider()
	frames := [][]byte{
		[]byte(`{"type":"input_audio_buffer.append","audio":"AAAA"}`),
		[]byte(`{"type":"session.update","session":{"audio":{"input":{"transcription":{"language":"hi"}}}}}`),
		[]byte(`{"type":"session.update","session":{"audio":{"input":{"format":{"type":"audio/pcm","rate":16000},"transcription":{"model":"elevenlabs/scribe_v3_live"},"turn_detection":null}}}}`),
		[]byte(`{"type":"session.update","session":{"audio":{"input":{"transcription":{"language":"ta"}}}}}`),
	}

	url, bifrostErr := provider.RealtimeWebSocketURLForBootstrap(nil, schemas.Key{}, "scribe_v3_live", "transcription", frames)
	require.Nil(t, bifrostErr)
	require.Equal(t, "wss://api.elevenlabs.io/v1/speech-to-text/realtime?model_id=scribe_v3_live&audio_format=pcm_16000&commit_strategy=manual&language_code=hi", url,
		"the update that names the model ends what the connection opens with; the one after it is the translator's to hold")

	_, bifrostErr = provider.RealtimeWebSocketURLForBootstrap(nil, schemas.Key{}, "scribe_v3_live", "transcription",
		[][]byte{[]byte(`{"type":"session.update","session":{"audio":{"input":{"format":{"type":"audio/pcma"},"transcription":{"model":"x"}}}}}`)})
	require.NotNil(t, bifrostErr)
	require.Equal(t, 400, *bifrostErr.StatusCode)

	_, bifrostErr = provider.RealtimeWebSocketURLForBootstrap(nil, schemas.Key{}, "scribe_v3_live", "transcription",
		[][]byte{[]byte(`{"type":"session.update","session":{"audio":{"input":{"transcription":{"language":"hi"}}}}}`), []byte(`{"type":"session.update","session":[1]}`)})
	require.NotNil(t, bifrostErr)
	require.Equal(t, 400, *bifrostErr.StatusCode)
	require.Contains(t, bifrostErr.Error.Message, "invalid session in session.update")

	url, bifrostErr = provider.RealtimeWebSocketURLForBootstrap(nil, schemas.Key{}, "agent_123", "", frames)
	require.Nil(t, bifrostErr)
	require.Equal(t, "wss://api.elevenlabs.io/v1/convai/conversation?agent_id=agent_123", url, "an agent session ignores the frames")
}

// Frames Scribe sent while transcribing the line "Hello from Bifrost. This is a test of real-time
// transcription. How are you doing today?", trimmed from a session against scribe_v2_realtime.
const (
	scribeSessionStarted       = `{"message_type":"session_started","session_id":"5b3bce05","config":{"sample_rate":16000,"audio_format":"pcm_16000","model_id":"scribe_v2_realtime"}}`
	scribeCommitted            = `{"message_type":"committed_transcript","text":"Hello from Bifrost. This is a test of real-time transcription. How are you doing today?"}`
	scribeTimestamps           = `{"message_type":"committed_transcript_with_timestamps","text":"Hello Bifrost.","language_code":"en","words":[{"text":"Hello","start":0.1,"end":0.38,"type":"word","speaker_id":null,"logprob":-0.5184799194335937,"characters":[{"text":"H","start":0.1,"end":0.14}]},{"text":" ","start":0.38,"end":0.44,"type":"spacing","speaker_id":null,"logprob":0,"characters":[]},{"text":"Bifrost.","start":0.44,"end":0.9,"type":"word","speaker_id":"speaker_0","logprob":-0.1,"characters":[]}]}`
	scribeEntities             = `{"message_type":"committed_transcript_entities","text":"Hello Bifrost.","entities":[{"text":"Bifrost","entity_type":"organization","start_char":6,"end_char":13}]}`
	scribeRedacted             = `{"message_type":"redacted_transcript","text":"Call 555-1234","redacted_text":"Call [PHONE_NUMBER]"}`
	scribeEdited               = `{"message_type":"edited_transcript","text":"um hello","edited_text":"hello"}`
	scribeWarning              = `{"message_type":"warning","warning":"low audio"}`
	scribeUpdate               = `{"type":"session.update","session":{"type":"transcription","audio":{"input":{"format":{"type":"audio/pcm","rate":16000},"transcription":{"model":"elevenlabs/scribe_v2_realtime","language":"en"}}}}}`
	scribeUpdateWithExtensions = `{"type":"session.update","session":{"type":"transcription","bifrost":{"extensions":true},"audio":{"input":{"format":{"type":"audio/pcm","rate":16000},"transcription":{"model":"elevenlabs/scribe_v2_realtime","language":"en"}}}}}`
)

func scribeClientEvent(t *testing.T, raw string) *schemas.BifrostRealtimeEvent {
	t.Helper()
	event, err := schemas.ParseRealtimeEvent([]byte(raw))
	require.NoError(t, err)
	return event
}

// scribeSend runs a client event through the translator and fails the test if it is refused.
func scribeSend(t *testing.T, tr *scribeTranslator, raw string) schemas.RealtimeTranslation {
	t.Helper()
	out, err := tr.ToProvider(scribeClientEvent(t, raw))
	require.NoError(t, err)
	return out
}

func scribeReceive(t *testing.T, tr *scribeTranslator, frame string) schemas.RealtimeTranslation {
	t.Helper()
	out, err := tr.ToClient(json.RawMessage(frame))
	require.NoError(t, err)
	return out
}

// scribeAppend is an input_audio_buffer.append of n bytes of audio.
func scribeAppend(n int) string {
	return `{"type":"input_audio_buffer.append","audio":"` + base64.StdEncoding.EncodeToString(make([]byte, n)) + `"}`
}

func scribePartial(text string) string {
	frame, _ := json.Marshal(map[string]string{"message_type": "partial_transcript", "text": text})
	return string(frame)
}

func scribeTypes(frames []json.RawMessage) []string {
	types := make([]string, 0, len(frames))
	for _, frame := range frames {
		types = append(types, gjson.GetBytes(frame, "type").String())
	}
	return types
}

// scribeCompleted returns the completed event among frames.
func scribeCompleted(t *testing.T, frames []json.RawMessage) json.RawMessage {
	t.Helper()
	for _, frame := range frames {
		if gjson.GetBytes(frame, "type").String() == string(schemas.RTEventInputAudioTransCompleted) {
			return frame
		}
	}
	t.Fatalf("no completed event in %v", scribeTypes(frames))
	return nil
}

// extendedScribeTranslator is a translator whose session asked for the Bifrost extension events.
func extendedScribeTranslator(t *testing.T) *scribeTranslator {
	t.Helper()
	tr := newScribeTranslator("scribe_v2_realtime")
	scribeSend(t, tr, scribeUpdateWithExtensions)
	return tr
}

func readyScribeTranslator(t *testing.T) *scribeTranslator {
	t.Helper()
	tr := newScribeTranslator("scribe_v2_realtime")
	scribeSend(t, tr, scribeUpdate)
	return tr
}

// TestScribeTranslatorAnnouncesAndHoldsTheSessionSettings pins session.created on open, the
// acknowledgement of the update the connection opened with, and the refusal of one that would change it.
func TestScribeTranslatorAnnouncesAndHoldsTheSessionSettings(t *testing.T) {
	tr := newScribeTranslator("scribe_v2_realtime")

	opened := tr.Open()
	require.Empty(t, opened.Provider)
	require.Equal(t, []string{"session.created"}, scribeTypes(opened.Client))
	require.Equal(t, "transcription", gjson.GetBytes(opened.Client[0], "session.type").String())
	require.Equal(t, "scribe_v2_realtime", gjson.GetBytes(opened.Client[0], "session.audio.input.transcription.model").String())

	acknowledged := scribeSend(t, tr, scribeUpdate)
	require.Empty(t, acknowledged.Provider, "the settings went out in the connect URL")
	require.Equal(t, []string{"session.updated"}, scribeTypes(acknowledged.Client))
	session := acknowledged.Client[0]
	require.Equal(t, int64(16000), gjson.GetBytes(session, "session.audio.input.format.rate").Int())
	require.Equal(t, "scribe_v2_realtime", gjson.GetBytes(session, "session.audio.input.transcription.model").String(), "the model Bifrost routed to, not the client's spelling")
	require.Equal(t, "server_vad", gjson.GetBytes(session, "session.audio.input.turn_detection.type").String())

	require.Equal(t, []string{"session.updated"}, scribeTypes(scribeSend(t, tr, scribeUpdate).Client), "repeating the settings is harmless")
	_, err := tr.ToProvider(scribeClientEvent(t, `{"type":"session.update","session":{"audio":{"input":{"transcription":{"language":"hi"}}}}}`))
	require.ErrorContains(t, err, "fixes a session's settings when it connects")
	_, err = tr.ToProvider(scribeClientEvent(t, `{"type":"session.update","session":{"audio":{"input":{"format":{"type":"audio/pcma"}}}}}`))
	require.ErrorContains(t, err, "A-law")
	// The relay parses events first and refuses a session that is not an object earlier; this is what the
	// translator says if one gets through.
	_, err = tr.ToProvider(&schemas.BifrostRealtimeEvent{Type: schemas.RTEventSessionUpdate, RawData: json.RawMessage(`{"type":"session.update","session":[1]}`)})
	require.ErrorContains(t, err, "invalid session in session.update")
}

// TestScribeTranslatorStreamsAudioAtTheSessionRate pins the chunk a client's append becomes.
func TestScribeTranslatorStreamsAudioAtTheSessionRate(t *testing.T) {
	audio := base64.StdEncoding.EncodeToString([]byte("pcm"))

	before := newScribeTranslator("scribe_v2_realtime")
	out := scribeSend(t, before, `{"type":"input_audio_buffer.append","audio":"`+audio+`"}`)
	require.Empty(t, out.Client)
	require.Len(t, out.Provider, 1)
	require.JSONEq(t, `{"message_type":"input_audio_chunk","audio_base_64":"`+audio+`","commit":false,"sample_rate":24000}`, string(out.Provider[0]),
		"a client that speaks before it configures gets the default the connection opened with")

	out = scribeSend(t, readyScribeTranslator(t), `{"type":"input_audio_buffer.append","audio":"`+audio+`"}`)
	require.Equal(t, int64(16000), gjson.GetBytes(out.Provider[0], "sample_rate").Int())

	// An event that carries only its raw frame, not the decoded audio, takes the escaped path.
	raw := &schemas.BifrostRealtimeEvent{Type: schemas.RTEventInputAudioAppend, RawData: json.RawMessage(`{"type":"input_audio_buffer.append","audio":"` + audio + `"}`)}
	out, err := before.ToProvider(raw)
	require.NoError(t, err)
	require.JSONEq(t, `{"message_type":"input_audio_chunk","audio_base_64":"`+audio+`","commit":false,"sample_rate":24000}`, string(out.Provider[0]))

	_, err = before.ToProvider(scribeClientEvent(t, `{"type":"input_audio_buffer.append"}`))
	require.ErrorContains(t, err, "audio must be set")
}

// TestScribeTranslatorCompletesAClientCommit follows one segment the client commits: the commit goes to
// Scribe and is announced at once, the partials stream as deltas, and the one transcript Scribe commits
// completes the item with the seconds of audio sent, however many frames Scribe describes it with.
func TestScribeTranslatorCompletesAClientCommit(t *testing.T) {
	tr := readyScribeTranslator(t)
	scribeSend(t, tr, scribeAppend(32000))
	scribeSend(t, tr, scribeAppend(32000))

	commit := scribeSend(t, tr, `{"type":"input_audio_buffer.commit"}`)
	require.Len(t, commit.Provider, 1)
	require.JSONEq(t, `{"message_type":"input_audio_chunk","audio_base_64":"","commit":true,"sample_rate":16000}`, string(commit.Provider[0]))
	require.Equal(t, []string{"input_audio_buffer.committed"}, scribeTypes(commit.Client))
	item := gjson.GetBytes(commit.Client[0], "item_id").String()
	require.NotEmpty(t, item)
	require.False(t, gjson.GetBytes(commit.Client[0], "previous_item_id").Exists())

	require.Empty(t, scribeReceive(t, tr, scribeSessionStarted).Provider)
	partial := scribeReceive(t, tr, scribePartial("Hello from Bifrost.")).Client
	require.Equal(t, []string{"conversation.item.input_audio_transcription.delta"}, scribeTypes(partial))
	require.Equal(t, item, gjson.GetBytes(partial[0], "item_id").String(), "the partials belong to the item the commit announced")
	require.Equal(t, "Hello from ", gjson.GetBytes(partial[0], "delta").String(), "the last word is withheld until it is finished")

	done := scribeReceive(t, tr, scribeCommitted).Client
	require.Equal(t, []string{
		"conversation.item.input_audio_transcription.delta", "conversation.item.added", "conversation.item.input_audio_transcription.completed", "conversation.item.done",
	}, scribeTypes(done), "the commit was announced already, so it is not announced twice; the deltas catch up to the final text first")
	completed := scribeCompleted(t, done)
	require.Equal(t, "Hello from "+gjson.GetBytes(done[0], "delta").String(), gjson.GetBytes(completed, "transcript").String(), "the deltas add up to the transcript")
	require.Equal(t, item, gjson.GetBytes(completed, "item_id").String())
	require.Equal(t, "Hello from Bifrost. This is a test of real-time transcription. How are you doing today?", gjson.GetBytes(completed, "transcript").String())
	require.Equal(t, "duration", gjson.GetBytes(completed, "usage.type").String())
	require.InDelta(t, 2.0, gjson.GetBytes(completed, "usage.seconds").Float(), 1e-9, "64000 bytes of 16 kHz pcm16 is two seconds")

	require.Empty(t, scribeReceive(t, tr, scribeTimestamps).Client, "a session that did not ask for the extension events never sees one, and the frame is not a second completion")

	scribeSend(t, tr, scribeAppend(100))
	next := scribeSend(t, tr, `{"type":"input_audio_buffer.commit"}`)
	require.Equal(t, item, gjson.GetBytes(next.Client[0], "previous_item_id").String())
	require.NotEqual(t, item, gjson.GetBytes(next.Client[0], "item_id").String())
}

// TestScribeTranslatorKeepsCommittedSegmentsApart pins commits made before Scribe has answered the first,
// which a client that keeps streaming makes: each commit is its own item, the audio sent after a commit
// belongs to the next segment, and Scribe's answers, which come in commit order (probed live, silence
// included), complete them in turn.
func TestScribeTranslatorKeepsCommittedSegmentsApart(t *testing.T) {
	tr := readyScribeTranslator(t)
	scribeSend(t, tr, scribeAppend(32000))
	first := scribeSend(t, tr, `{"type":"input_audio_buffer.commit"}`).Client
	_, err := tr.ToProvider(scribeClientEvent(t, `{"type":"input_audio_buffer.commit"}`))
	require.ErrorContains(t, err, "empty", "a commit with no audio since the last one is refused here, because Scribe ends the session on it")
	scribeSend(t, tr, scribeAppend(64000))
	second := scribeSend(t, tr, `{"type":"input_audio_buffer.commit"}`).Client
	scribeSend(t, tr, scribeAppend(16000)) // the segment after them, still streaming

	a, b := gjson.GetBytes(first[0], "item_id").String(), gjson.GetBytes(second[0], "item_id").String()
	require.NotEqual(t, a, b, "two commits are two items")
	require.Equal(t, a, gjson.GetBytes(second[0], "previous_item_id").String())

	partial := scribeReceive(t, tr, scribePartial("Hello there")).Client
	require.Equal(t, a, gjson.GetBytes(partial[0], "item_id").String(), "the partials are for the oldest commit Scribe has not answered")

	doneA := scribeReceive(t, tr, `{"message_type":"committed_transcript","text":"Hello there."}`).Client
	require.NotContains(t, scribeTypes(doneA), "input_audio_buffer.committed", "the commit was announced already")
	completedA := scribeCompleted(t, doneA)
	require.Equal(t, a, gjson.GetBytes(completedA, "item_id").String())
	require.InDelta(t, 1.0, gjson.GetBytes(completedA, "usage.seconds").Float(), 1e-9, "the audio that came after the commit is not this segment's")

	partial = scribeReceive(t, tr, scribePartial("How are you")).Client
	require.Equal(t, b, gjson.GetBytes(partial[0], "item_id").String())
	require.Equal(t, "How are ", gjson.GetBytes(partial[0], "delta").String(), "the next segment's deltas start over")
	completedB := scribeCompleted(t, scribeReceive(t, tr, `{"message_type":"committed_transcript","text":"How are you?"}`).Client)
	require.Equal(t, b, gjson.GetBytes(completedB, "item_id").String())
	require.InDelta(t, 2.0, gjson.GetBytes(completedB, "usage.seconds").Float(), 1e-9)

	// The streaming segment ends by Scribe's own commit and keeps the audio sent since the last commit.
	third := scribeReceive(t, tr, `{"message_type":"committed_transcript","text":"Bye."}`).Client
	require.Equal(t, "input_audio_buffer.committed", scribeTypes(third)[0])
	require.NotContains(t, []string{a, b}, gjson.GetBytes(third[0], "item_id").String())
	require.Equal(t, b, gjson.GetBytes(third[0], "previous_item_id").String())
	require.InDelta(t, 0.5, gjson.GetBytes(scribeCompleted(t, third), "usage.seconds").Float(), 1e-9)
}

// TestScribeTranslatorBoundsUnansweredCommits pins the limit on commits waiting for a transcript, and that
// an answer makes room.
func TestScribeTranslatorBoundsUnansweredCommits(t *testing.T) {
	tr := readyScribeTranslator(t)
	for range scribeMaxPendingCommits {
		scribeSend(t, tr, scribeAppend(32000))
		scribeSend(t, tr, `{"type":"input_audio_buffer.commit"}`)
	}
	scribeSend(t, tr, scribeAppend(32000))
	_, err := tr.ToProvider(scribeClientEvent(t, `{"type":"input_audio_buffer.commit"}`))
	require.ErrorContains(t, err, "has not answered")

	scribeReceive(t, tr, scribeCommitted)
	scribeSend(t, tr, `{"type":"input_audio_buffer.commit"}`)
}

// TestScribeTranslatorKeepsTheAudioOfARefusedCommit pins that audio committed in a segment Scribe never
// answers is still reported, with the next segment that completes.
func TestScribeTranslatorKeepsTheAudioOfARefusedCommit(t *testing.T) {
	tr := readyScribeTranslator(t)
	scribeSend(t, tr, scribeAppend(32000))
	scribeSend(t, tr, `{"type":"input_audio_buffer.commit"}`)
	events := scribeReceive(t, tr, `{"message_type":"commit_throttled","error":"too soon"}`).Client
	require.Equal(t, []string{"error"}, scribeTypes(events))

	completed := scribeCompleted(t, scribeReceive(t, tr, scribeCommitted).Client)
	require.InDelta(t, 1.0, gjson.GetBytes(completed, "usage.seconds").Float(), 1e-9)
}

// TestScribeTranslatorAnnouncesACommitOnce pins that a commit an error interrupted is announced to the
// client once. Scribe answers it after a non-fatal error such as input_error, and the client was told the
// segment was committed when it made the commit.
func TestScribeTranslatorAnnouncesACommitOnce(t *testing.T) {
	const inputError = `{"message_type":"input_error","error":"Failed to decode audio chunk"}`
	itemOf := func(frame json.RawMessage) string { return gjson.GetBytes(frame, "item_id").String() }
	selfLinked := func(frames []json.RawMessage) bool {
		for _, frame := range frames {
			if previous := gjson.GetBytes(frame, "previous_item_id").String(); previous != "" && previous == itemOf(frame) {
				return true
			}
		}
		return false
	}

	t.Run("Scribe answers the commit after the error", func(t *testing.T) {
		tr := readyScribeTranslator(t)
		scribeSend(t, tr, scribeAppend(32000))
		item := itemOf(scribeSend(t, tr, `{"type":"input_audio_buffer.commit"}`).Client[0])
		require.Equal(t, []string{"error"}, scribeTypes(scribeReceive(t, tr, inputError).Client))

		answer := scribeReceive(t, tr, scribeCommitted).Client
		require.NotContains(t, scribeTypes(answer), "input_audio_buffer.committed", "the client was told when it committed")
		require.False(t, selfLinked(answer), "no item is its own predecessor")
		completed := scribeCompleted(t, answer)
		require.Equal(t, item, itemOf(completed))
		require.InDelta(t, 1.0, gjson.GetBytes(completed, "usage.seconds").Float(), 1e-9)

		scribeSend(t, tr, scribeAppend(32000))
		next := scribeSend(t, tr, `{"type":"input_audio_buffer.commit"}`).Client
		require.Equal(t, item, gjson.GetBytes(next[0], "previous_item_id").String(), "the next segment follows it")
		require.NotEqual(t, item, itemOf(next[0]))
	})
	t.Run("the client commits again after the error", func(t *testing.T) {
		tr := readyScribeTranslator(t)
		scribeSend(t, tr, scribeAppend(32000))
		item := itemOf(scribeSend(t, tr, `{"type":"input_audio_buffer.commit"}`).Client[0])
		scribeReceive(t, tr, inputError)

		scribeSend(t, tr, scribeAppend(32000))
		again := scribeSend(t, tr, `{"type":"input_audio_buffer.commit"}`).Client
		require.Empty(t, again, "the segment is still the one the client was told about")

		completed := scribeCompleted(t, scribeReceive(t, tr, scribeCommitted).Client)
		require.Equal(t, item, itemOf(completed))
		require.InDelta(t, 2.0, gjson.GetBytes(completed, "usage.seconds").Float(), 1e-9, "the audio of both commits")
	})
	t.Run("the oldest commit left waiting is the one that completes", func(t *testing.T) {
		tr := readyScribeTranslator(t)
		scribeSend(t, tr, scribeAppend(32000))
		first := itemOf(scribeSend(t, tr, `{"type":"input_audio_buffer.commit"}`).Client[0])
		scribeSend(t, tr, scribeAppend(64000))
		second := itemOf(scribeSend(t, tr, `{"type":"input_audio_buffer.commit"}`).Client[0])
		require.NotEqual(t, first, second)
		require.Equal(t, first, itemOf(scribeCompleted(t, scribeReceive(t, tr, `{"message_type":"committed_transcript","text":"First."}`).Client)))
		scribeReceive(t, tr, inputError)

		answer := scribeReceive(t, tr, `{"message_type":"committed_transcript","text":"Second."}`).Client
		require.NotContains(t, scribeTypes(answer), "input_audio_buffer.committed", "the client was told when it committed")
		completed := scribeCompleted(t, answer)
		require.Equal(t, second, itemOf(completed), "no new item is made for audio the client already committed")
		require.InDelta(t, 2.0, gjson.GetBytes(completed, "usage.seconds").Float(), 1e-9)
	})
	t.Run("an error with no commit waiting hides nothing", func(t *testing.T) {
		tr := readyScribeTranslator(t)
		scribeSend(t, tr, scribeAppend(16000))
		scribeReceive(t, tr, scribePartial("Hello"))
		scribeReceive(t, tr, inputError)

		answer := scribeReceive(t, tr, `{"message_type":"committed_transcript","text":"Hello."}`).Client
		require.Equal(t, "input_audio_buffer.committed", scribeTypes(answer)[0], "Scribe committed it, so this is the first the client hears of it")
	})
}

// TestScribeTranslatorAnnouncesCommitsScribeMakes pins a segment Scribe commits on its own, by voice
// detection: the commit is announced with the transcript, and the audio counted since the last one is
// that segment's.
func TestScribeTranslatorAnnouncesCommitsScribeMakes(t *testing.T) {
	tr := readyScribeTranslator(t)
	scribeSend(t, tr, scribeAppend(16000))
	scribeReceive(t, tr, scribePartial("Hello"))

	done := scribeReceive(t, tr, `{"message_type":"committed_transcript","text":"Hello."}`).Client
	require.Equal(t, []string{
		"input_audio_buffer.committed", "conversation.item.input_audio_transcription.delta", "conversation.item.added", "conversation.item.input_audio_transcription.completed", "conversation.item.done",
	}, scribeTypes(done))
	completed := scribeCompleted(t, done)
	require.Equal(t, gjson.GetBytes(done[0], "item_id").String(), gjson.GetBytes(completed, "item_id").String())
	require.InDelta(t, 0.5, gjson.GetBytes(completed, "usage.seconds").Float(), 1e-9)

	next := scribeReceive(t, tr, scribePartial("How are")).Client
	require.NotEqual(t, gjson.GetBytes(done[0], "item_id").String(), gjson.GetBytes(next[0], "item_id").String(), "the next partial starts a new item")
	require.Equal(t, "How ", gjson.GetBytes(next[0], "delta").String(), "a new item's deltas start over")

	scribeSend(t, tr, scribeAppend(32000))
	second := scribeReceive(t, tr, `{"message_type":"committed_transcript","text":"How are you?"}`).Client
	require.InDelta(t, 1.0, gjson.GetBytes(scribeCompleted(t, second), "usage.seconds").Float(), 1e-9, "only the audio since the last commit")
	require.Equal(t, gjson.GetBytes(done[0], "item_id").String(), gjson.GetBytes(second[0], "previous_item_id").String())
}

// TestScribeTranslatorIgnoresARepeatOfTheCommittedSegment pins that Scribe's keepalive, which repeats the
// latest partial, does not open a new item once that text has been committed.
func TestScribeTranslatorIgnoresARepeatOfTheCommittedSegment(t *testing.T) {
	tr := readyScribeTranslator(t)
	scribeSend(t, tr, scribeAppend(100))
	scribeReceive(t, tr, scribePartial("Hello there."))
	scribeReceive(t, tr, `{"message_type":"committed_transcript","text":"Hello there."}`)

	require.Empty(t, scribeReceive(t, tr, scribePartial("Hello there.")).Client, "a repeat of the committed text is a keepalive")
	require.Empty(t, scribeReceive(t, tr, scribePartial("")).Client)
	next := scribeReceive(t, tr, scribePartial("Hello again.")).Client
	require.Len(t, next, 1, "a different partial starts the next segment")
}

// TestScribeTranslatorDeltasOnlyAppend replays the partials of a real session, which revise earlier text,
// drop a trailing period, flap on a word and, on scribe_v3_live, end in a replacement character mid-word.
// The delta carries only finished words that append to what the deltas have sent, the last word being
// the one still changing, and the transcript field always carries the whole partial.
func TestScribeTranslatorDeltasOnlyAppend(t *testing.T) {
	tr := readyScribeTranslator(t)
	for _, step := range []struct {
		partial, delta string
		silent         bool
	}{
		{partial: "Hello", delta: ""},
		{partial: "Hello from Bifrost.", delta: "Hello from "},
		{partial: "Hello from Bifrost.", silent: true}, // a keepalive repeats the latest partial
		{partial: "Hello from Bifrost. This is a test of real", delta: "Bifrost. This is a test of "},
		{partial: "Hello from Byfrost. This is a test of real-time transcription.", delta: ""}, // revised words already sent
		{partial: "Hello from Bifrost. This is a test of real-time transcription.", delta: "real-time "},
		{partial: "Hello from Bifrost. This is a test of real-time transcription", delta: ""}, // the period was dropped
		{partial: "Hello from Bifrost. This is a test of real-time transcription. How-", delta: "transcription. "},
		{partial: "Hello from Bifrost. This is a test of real-time transcription. How are\uFFFD", delta: "How "}, // cut in the word still changing
		{partial: "Hello from Bifrost. This is a test of real-time transcription. How\uFFFD are you", delta: ""}, // cut inside a finished word
		{partial: "Hello from Bifrost. This is a test of real-time transcription. How are you doing", delta: "are you "},
	} {
		events := scribeReceive(t, tr, scribePartial(step.partial)).Client
		if step.silent {
			require.Empty(t, events)
			continue
		}
		require.Len(t, events, 1, step.partial)
		require.Equal(t, step.delta, gjson.GetBytes(events[0], "delta").String(), step.partial)
		require.Equal(t, step.partial, gjson.GetBytes(events[0], "transcript").String())
		require.True(t, gjson.GetBytes(events[0], "delta").Exists(), "a delta event always carries a delta, empty when there is nothing to append")
	}

	probability := scribeReceive(t, tr, `{"message_type":"partial_transcript","text":"Hello","turn_probability":0.57}`).Client
	require.InDelta(t, 0.57, gjson.GetBytes(probability[0], "turn_probability").Float(), 1e-9)
}

// TestScribeTranslatorFlushesTheLastWordAtCommit pins that the deltas add up to the transcript when the
// text only grew, so a client that appends deltas and ignores the completed event loses nothing, and that
// text with no space to hold a word back at, such as Japanese, arrives whole at the commit.
func TestScribeTranslatorFlushesTheLastWordAtCommit(t *testing.T) {
	tr := readyScribeTranslator(t)
	scribeSend(t, tr, scribeAppend(10))
	var added strings.Builder
	for _, partial := range []string{"こんにちは", "こんにちは世界"} {
		require.Equal(t, "", gjson.GetBytes(scribeReceive(t, tr, scribePartial(partial)).Client[0], "delta").String(), "no space, no finished word")
	}
	done := scribeReceive(t, tr, `{"message_type":"committed_transcript","text":"こんにちは世界。"}`).Client
	for _, frame := range done {
		if gjson.GetBytes(frame, "type").String() == "conversation.item.input_audio_transcription.delta" {
			added.WriteString(gjson.GetBytes(frame, "delta").String())
		}
	}
	require.Equal(t, "こんにちは世界。", added.String())
}

// TestScribeTranslatorRefusesWhatScribeCannotDo pins the client events a transcription session cannot take.
func TestScribeTranslatorRefusesWhatScribeCannotDo(t *testing.T) {
	tr := readyScribeTranslator(t)

	_, err := tr.ToProvider(scribeClientEvent(t, `{"type":"input_audio_buffer.commit"}`))
	require.ErrorContains(t, err, "empty")
	require.Equal(t, []string{"input_audio_buffer.cleared"}, scribeTypes(scribeSend(t, tr, `{"type":"input_audio_buffer.clear"}`).Client), "a clear with nothing to discard is acknowledged")
	scribeSend(t, tr, scribeAppend(10))
	_, err = tr.ToProvider(scribeClientEvent(t, `{"type":"input_audio_buffer.clear"}`))
	require.ErrorContains(t, err, "cannot discard")
	for _, event := range []string{`{"type":"response.create"}`, `{"type":"conversation.item.create","item":{"type":"message"}}`} {
		_, err = tr.ToProvider(scribeClientEvent(t, event))
		require.ErrorContains(t, err, "not supported by a scribe transcription session", event)
	}
}

// TestScribeTranslatorReportsErrors pins each error frame as an error event, and that a close which
// follows an error already reported is not reported again.
func TestScribeTranslatorReportsErrors(t *testing.T) {
	for frame, want := range map[string]string{
		"invalid_request":     "invalid_request_error",
		"auth_error":          "authentication_error",
		"quota_exceeded":      "rate_limit_exceeded",
		"commit_throttled":    "rate_limit_exceeded",
		"transcriber_error":   "server_error",
		"chunk_size_exceeded": "invalid_request_error",
	} {
		tr := readyScribeTranslator(t)
		events := scribeReceive(t, tr, `{"message_type":"`+frame+`","error":"why"}`).Client
		require.Equal(t, []string{"error"}, scribeTypes(events), frame)
		require.Equal(t, want, gjson.GetBytes(events[0], "error.type").String(), frame)
		require.Equal(t, frame, gjson.GetBytes(events[0], "error.code").String())
		require.Equal(t, "why", gjson.GetBytes(events[0], "error.message").String())
		require.Empty(t, tr.Close(1008, "why").Client, "Scribe's own error came first")
	}

	tr := readyScribeTranslator(t)
	require.Equal(t, []string{"error"}, scribeTypes(tr.Close(1011, "internal error").Client))
	require.Empty(t, tr.Close(1000, "bye").Client)
	require.Empty(t, tr.Close(1006, "").Client)
}

// TestScribeTranslatorSendsExtensionEventsOnlyToASessionThatAsks pins the opt-in: the frames with no
// OpenAI event are consumed for a session that did not set session.bifrost.extensions, and for one that
// did they become neutral Bifrost events, the same shape whichever provider sent the data.
func TestScribeTranslatorSendsExtensionEventsOnlyToASessionThatAsks(t *testing.T) {
	frames := []string{scribeSessionStarted, scribeTimestamps, scribeEntities, scribeRedacted, scribeEdited, scribeWarning, `{"message_type":"brand_new","detail":1}`}

	plain := readyScribeTranslator(t)
	scribeSend(t, plain, scribeAppend(100))
	scribeReceive(t, plain, scribeCommitted)
	for _, frame := range frames {
		require.Empty(t, scribeReceive(t, plain, frame).Client, frame)
	}

	tr := extendedScribeTranslator(t)
	scribeSend(t, tr, scribeAppend(100))
	item := gjson.GetBytes(scribeCompleted(t, scribeReceive(t, tr, scribeCommitted).Client), "item_id").String()

	words := scribeReceive(t, tr, scribeTimestamps).Client
	require.Equal(t, []string{"bifrost.transcription.words"}, scribeTypes(words))
	require.Equal(t, item, gjson.GetBytes(words[0], "item_id").String(), "the event names the segment it describes")
	require.Equal(t, "en", gjson.GetBytes(words[0], "language").String())
	require.Len(t, gjson.GetBytes(words[0], "words").Array(), 2, "the spacing entry is not a word")
	require.False(t, gjson.GetBytes(words[0], "words.0.characters").Exists(), "per-character timings are not forwarded")
	require.Equal(t, "Hello", gjson.GetBytes(words[0], "words.0.text").String())
	require.Equal(t, 0.38, gjson.GetBytes(words[0], "words.0.end").Float())
	require.InDelta(t, math.Exp(-0.5184799194335937), gjson.GetBytes(words[0], "words.0.confidence").Float(), 1e-12, "Scribe's logprob becomes a 0 to 1 confidence")
	require.False(t, gjson.GetBytes(words[0], "words.0.speaker").Exists())
	require.Equal(t, "speaker_0", gjson.GetBytes(words[0], "words.1.speaker").String())

	entities := scribeReceive(t, tr, scribeEntities).Client
	require.Equal(t, []string{"bifrost.transcription.entities"}, scribeTypes(entities))
	require.Equal(t, item, gjson.GetBytes(entities[0], "item_id").String())
	require.JSONEq(t, `[{"text":"Bifrost","type":"organization","start_char":6,"end_char":13}]`, gjson.GetBytes(entities[0], "entities").Raw)

	redacted := scribeReceive(t, tr, scribeRedacted).Client
	require.Equal(t, []string{"bifrost.transcription.redacted"}, scribeTypes(redacted))
	require.Equal(t, "Call [PHONE_NUMBER]", gjson.GetBytes(redacted[0], "redacted_text").String())
	require.Equal(t, item, gjson.GetBytes(redacted[0], "item_id").String())

	edited := scribeReceive(t, tr, scribeEdited).Client
	require.Equal(t, []string{"bifrost.transcription.edited"}, scribeTypes(edited))
	require.Equal(t, "hello", gjson.GetBytes(edited[0], "edited_text").String())

	empty := scribeReceive(t, tr, `{"message_type":"committed_transcript_entities","text":"x","entities":[]}`).Client
	require.JSONEq(t, `[]`, gjson.GetBytes(empty[0], "entities").Raw, "a segment with no entities reports an empty list, not null")

	started := scribeReceive(t, tr, scribeSessionStarted).Client
	require.Equal(t, []string{"bifrost.provider_event"}, scribeTypes(started))
	require.Equal(t, "elevenlabs", gjson.GetBytes(started[0], "provider").String())
	require.Equal(t, "session_started", gjson.GetBytes(started[0], "event.message_type").String())
	require.Equal(t, "scribe_v2_realtime", gjson.GetBytes(started[0], "event.config.model_id").String())
	require.False(t, gjson.GetBytes(started[0], "item_id").Exists(), "a frame about no segment names no item")

	warning := scribeReceive(t, tr, scribeWarning).Client
	require.Equal(t, []string{"bifrost.provider_event"}, scribeTypes(warning))
	require.Equal(t, "low audio", gjson.GetBytes(warning[0], "event.warning").String())

	unknown := scribeReceive(t, tr, `{"message_type":"brand_new","detail":1}`).Client
	require.Equal(t, []string{"bifrost.provider_event"}, scribeTypes(unknown), "a frame Scribe adds later is not lost")
	require.Equal(t, int64(1), gjson.GetBytes(unknown[0], "event.detail").Int())

	_, err := tr.ToClient(json.RawMessage(`not json`))
	require.Error(t, err, "a frame that is not JSON ends the session")
}

// TestScribeTranslatorMatchesExtensionEventsToTheirSegment pins that a frame describing a committed segment
// is matched to it by the text it repeats, since Scribe names no segment and the frames can arrive after
// later segments completed: an edit for the first of four, two segments with the same text taking their
// frames in order, and a text that matches none falling back to the segment completed last.
func TestScribeTranslatorMatchesExtensionEventsToTheirSegment(t *testing.T) {
	tr := extendedScribeTranslator(t)
	complete := func(text string) string {
		scribeSend(t, tr, scribeAppend(100))
		done := scribeReceive(t, tr, `{"message_type":"committed_transcript","text":"`+text+`"}`).Client
		return gjson.GetBytes(scribeCompleted(t, done), "item_id").String()
	}
	itemOf := func(frame string) string {
		events := scribeReceive(t, tr, frame).Client
		require.Len(t, events, 1, frame)
		return gjson.GetBytes(events[0], "item_id").String()
	}
	a, b, c, d := complete("um hello"), complete("Call 555-1234"), complete("yes"), complete("yes")

	require.Equal(t, a, itemOf(scribeEdited), "the edit is for the first segment, not the last")
	require.Equal(t, b, itemOf(scribeRedacted))

	yes := `{"message_type":"committed_transcript_with_timestamps","text":"yes","words":[]}`
	require.Equal(t, c, itemOf(yes), "equal texts take their frames in order")
	require.Equal(t, d, itemOf(yes))
	require.Equal(t, d, itemOf(yes), "a frame beyond the segments that match belongs to the last of them")

	require.Equal(t, d, itemOf(`{"message_type":"redacted_transcript","text":"never heard","redacted_text":"x"}`), "a text that matches none falls back to the segment completed last")
	require.Equal(t, a, itemOf(`{"message_type":"committed_transcript_entities","text":"um hello","entities":[]}`), "each kind of frame is matched on its own")
}

// TestScribeTranslatorExtensionsCanChangeAfterConnecting pins that the option is Bifrost's, not one of the
// settings Scribe fixes at connect: a later session.update may turn it on or off, and the session
// object the client is sent says which it is.
func TestScribeTranslatorExtensionsCanChangeAfterConnecting(t *testing.T) {
	tr := readyScribeTranslator(t)
	require.False(t, gjson.GetBytes(tr.Open().Client[0], "session.bifrost").Exists(), "the default is off, and says nothing")
	require.Empty(t, scribeReceive(t, tr, scribeWarning).Client)

	on := scribeSend(t, tr, `{"type":"session.update","session":{"bifrost":{"extensions":true}}}`)
	require.Equal(t, []string{"session.updated"}, scribeTypes(on.Client), "turning the option on changes no connect setting, so it is acknowledged")
	require.True(t, gjson.GetBytes(on.Client[0], "session.bifrost.extensions").Bool())
	require.Equal(t, []string{"bifrost.provider_event"}, scribeTypes(scribeReceive(t, tr, scribeWarning).Client))

	off := scribeSend(t, tr, `{"type":"session.update","session":{"bifrost":{"extensions":false}}}`)
	require.False(t, gjson.GetBytes(off.Client[0], "session.bifrost").Exists())
	require.Empty(t, scribeReceive(t, tr, scribeWarning).Client)
}

// TestScribeTranslatorCountsTheAudioItSent pins the seconds a segment bills for, by format.
func TestScribeTranslatorCountsTheAudioItSent(t *testing.T) {
	for _, test := range []struct {
		format  string
		bytes   int
		seconds float64
	}{
		{`{"type":"audio/pcm","rate":24000}`, 96000, 2},
		{`{"type":"audio/pcm","rate":8000}`, 16000, 1},
		{`{"type":"audio/pcmu"}`, 4000, 0.5},
	} {
		tr := newScribeTranslator("scribe_v2_realtime")
		scribeSend(t, tr, `{"type":"session.update","session":{"audio":{"input":{"format":`+test.format+`,"transcription":{"model":"scribe_v2_realtime"}}}}}`)
		scribeSend(t, tr, scribeAppend(test.bytes))
		completed := scribeCompleted(t, scribeReceive(t, tr, `{"message_type":"committed_transcript","text":"x"}`).Client)
		require.InDelta(t, test.seconds, gjson.GetBytes(completed, "usage.seconds").Float(), 1e-9, test.format)
	}
}

// TestScribeTranslatorReachesTheOpenAIUsageReader pins that the usage a completed event carries is read as
// audio seconds by the reader the relay uses for every OpenAI-shaped stream.
func TestScribeTranslatorReachesTheOpenAIUsageReader(t *testing.T) {
	tr := readyScribeTranslator(t)
	scribeSend(t, tr, scribeAppend(32000))
	completed := scribeCompleted(t, scribeReceive(t, tr, `{"message_type":"committed_transcript","text":"x"}`).Client)

	usage := (&openai.OpenAIProvider{}).ExtractRealtimeTurnUsage(completed)
	require.NotNil(t, usage)
	require.NotNil(t, usage.AudioSeconds)
	require.InDelta(t, 1.0, *usage.AudioSeconds, 1e-9)
}

func BenchmarkScribeTranslatorAudioAppend(b *testing.B) {
	tr := newScribeTranslator("scribe_v2_realtime")
	event, err := schemas.ParseRealtimeEvent([]byte(scribeAppend(8000)))
	require.NoError(b, err)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := tr.ToProvider(event); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkScribeTranslatorSegment is one segment the client commits: its audio, the commit, and the
// transcript that completes it.
func BenchmarkScribeTranslatorSegment(b *testing.B) {
	tr := newScribeTranslator("scribe_v2_realtime")
	audio, err := schemas.ParseRealtimeEvent([]byte(scribeAppend(8000)))
	require.NoError(b, err)
	commit, err := schemas.ParseRealtimeEvent([]byte(`{"type":"input_audio_buffer.commit"}`))
	require.NoError(b, err)
	transcript := json.RawMessage(scribeCommitted)
	b.ReportAllocs()
	for b.Loop() {
		for _, event := range []*schemas.BifrostRealtimeEvent{audio, commit} {
			if _, err := tr.ToProvider(event); err != nil {
				b.Fatal(err)
			}
		}
		if _, err := tr.ToClient(transcript); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkScribeTranslatorPartial(b *testing.B) {
	tr := newScribeTranslator("scribe_v2_realtime")
	frames := []json.RawMessage{
		json.RawMessage(scribePartial("Hello from Bifrost.")),
		json.RawMessage(scribePartial("Hello from Bifrost. This is a test")),
	}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		if _, err := tr.ToClient(frames[i%2]); err != nil {
			b.Fatal(err)
		}
		i++
	}
}

// nativeScribeChunk is the frame an ElevenLabs SDK sends: n bytes of audio, optionally with the commit.
func nativeScribeChunk(n int, commit bool) json.RawMessage {
	return json.RawMessage(`{"message_type":"input_audio_chunk","audio_base_64":"` + base64.StdEncoding.EncodeToString(make([]byte, n)) + `","commit":` + strconv.FormatBool(commit) + `,"sample_rate":16000}`)
}

// A native client's call is addressed by its own URL: the client's query goes upstream as sent, less its
// credentials, on the key's base URL, with the model the gateway resolved in place of the one it named.
func TestNativeRealtimeDial(t *testing.T) {
	provider := NewElevenlabsProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{
		BaseURL:      "https://api.in.residency.elevenlabs.io/",
		ExtraHeaders: map[string]schemas.SecretVar{"x-residency": *schemas.NewSecretVar("in")},
	}}, nil)
	key := schemas.Key{Value: *schemas.NewSecretVar("provider-key")}

	for _, tc := range []struct {
		name, route, query, model, want string
	}{
		{"scribe keeps every option", NativeScribeRoute, "model_id=alias&audio_format=pcm_16000&commit_strategy=vad&language_code=hi&include_timestamps=true&keyterms=a&keyterms=b&enable_logging=false&vad_silence_threshold_secs=0.5",
			"scribe_v3_live", "wss://api.in.residency.elevenlabs.io/v1/speech-to-text/realtime?audio_format=pcm_16000&commit_strategy=vad&enable_logging=false&include_timestamps=true&keyterms=a&keyterms=b&language_code=hi&model_id=scribe_v3_live&vad_silence_threshold_secs=0.5"},
		{"credentials are dropped", NativeScribeRoute, "model_id=scribe_v3_live&token=sutkn_secret&xi-api-key=sk-bf-vk", "scribe_v3_live", "wss://api.in.residency.elevenlabs.io/v1/speech-to-text/realtime?model_id=scribe_v3_live"},
		{"no model_id in the query gets the resolved one", NativeDialogueRoute, "output_format=pcm_24000", "eleven_v4_turbo", "wss://api.in.residency.elevenlabs.io/v1/text-to-dialogue/stream-input?model_id=eleven_v4_turbo&output_format=pcm_24000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, headers, err := provider.NativeRealtimeDial(nil, key, tc.route, tc.query, tc.model)
			require.Nil(t, err)
			require.Equal(t, tc.want, got)
			require.Equal(t, "provider-key", headers["xi-api-key"], "the key's credential goes upstream")
			require.Equal(t, "in", headers["x-residency"])
		})
	}

	for _, tc := range []struct{ name, route, query string }{
		{"a route ElevenLabs has no native socket for", "/v1/convai/conversation", "model_id=x"},
		{"a path that only resembles one", NativeScribeRoute + "/../x", "model_id=x"},
		{"a query that does not parse", NativeScribeRoute, "model_id=%zz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := provider.NativeRealtimeDial(nil, key, tc.route, tc.query, "m")
			require.NotNil(t, err)
		})
	}
}

func TestScribeAudioBytesPerSecond(t *testing.T) {
	for format, want := range map[string]int64{
		"pcm_16000": 32000, "pcm_8000": 16000, "pcm_22050": 44100, "pcm_24000": 48000, "pcm_44100": 88200, "pcm_48000": 96000,
		"ulaw_8000": 8000, "alaw_8000": 8000,
		"": 32000, "pcm": 32000, "pcm_abc": 32000, "pcm_0": 32000, "weird": 32000,
	} {
		require.Equal(t, want, scribeAudioBytesPerSecond(format), format)
	}
}

func TestBase64DecodedLen(t *testing.T) {
	for n := range 40 {
		raw := make([]byte, n)
		require.Equal(t, int64(n), base64DecodedLen(base64.StdEncoding.EncodeToString(raw)), "padded, %d bytes", n)
		require.Equal(t, int64(n), base64DecodedLen(base64.RawStdEncoding.EncodeToString(raw)), "unpadded, %d bytes", n)
	}
	require.Zero(t, base64DecodedLen(""))
}

// A native Scribe connection is governed turn by turn from what its frames say, and its audio is metered from them.
func TestScribeNativeProtocolMetersAudioPerSegment(t *testing.T) {
	provider := &ElevenlabsProvider{}
	protocol, err := provider.NativeRealtimeProtocol(NativeScribeRoute, "model_id=scribe_v3_live&audio_format=pcm_16000")
	require.Nil(t, err)

	var starts []bool
	for _, frame := range []json.RawMessage{nativeScribeChunk(16000, false), nativeScribeChunk(16000, false), nativeScribeChunk(0, true)} {
		event, parseErr := protocol.ToBifrostRealtimeEvent(frame)
		require.NoError(t, parseErr)
		forwarded, encodeErr := protocol.ToProviderRealtimeEvent(event)
		require.NoError(t, encodeErr)
		require.JSONEq(t, string(frame), string(forwarded), "the client's frame goes upstream as it was sent")
		starts = append(starts, protocol.ShouldStartRealtimeTurn(event))
	}
	require.Equal(t, []bool{true, true, true}, starts, "a segment starts at its audio")

	committed := json.RawMessage(`{"message_type":"committed_transcript","text":"hello \"world\""}`)
	event, parseErr := protocol.ToBifrostRealtimeEvent(committed)
	require.NoError(t, parseErr)
	require.Equal(t, protocol.RealtimeTurnFinalEvent(), event.Type)
	require.Equal(t, `"hello \"world\""`, string(event.ExtraParams["transcript"]))
	require.False(t, protocol.ShouldStartRealtimeTurn(event))

	usage := protocol.(schemas.RealtimeUsageExtractor).ExtractRealtimeTurnUsage(committed)
	require.NotNil(t, usage)
	require.NotNil(t, usage.AudioSeconds)
	require.InDelta(t, 1.0, *usage.AudioSeconds, 1e-9, "32000 bytes of pcm_16000 is one second")
	require.Nil(t, protocol.(schemas.RealtimeUsageExtractor).ExtractRealtimeTurnUsage(committed), "the next segment starts from nothing")

	_, _ = protocol.ToBifrostRealtimeEvent(nativeScribeChunk(8000, false))
	usage = protocol.(schemas.RealtimeUsageExtractor).ExtractRealtimeTurnUsage(committed)
	require.InDelta(t, 0.25, *usage.AudioSeconds, 1e-9)
}

// Scribe is metered at the format of the query: a byte a sample for u-law, two for pcm.
func TestScribeNativeProtocolMetersTheQueryFormat(t *testing.T) {
	for query, wantSeconds := range map[string]float64{
		"audio_format=ulaw_8000":  2,
		"audio_format=pcm_8000":   1,
		"audio_format=pcm_48000":  1.0 / 6,
		"model_id=scribe_v3_live": 0.5, // pcm_16000 when the query names no format
	} {
		protocol, err := (&ElevenlabsProvider{}).NativeRealtimeProtocol(NativeScribeRoute, query)
		require.Nil(t, err)
		_, parseErr := protocol.ToBifrostRealtimeEvent(nativeScribeChunk(16000, false))
		require.NoError(t, parseErr)
		usage := protocol.(schemas.RealtimeUsageExtractor).ExtractRealtimeTurnUsage(nil)
		require.NotNil(t, usage, query)
		require.InDelta(t, wantSeconds, *usage.AudioSeconds, 1e-9, query)
	}
}

// Frames Scribe sends are typed for the turn pipeline, and the error ones carry their error: the frame is forwarded as it is.
func TestScribeNativeProtocolReadsScribeFrames(t *testing.T) {
	protocol, err := (&ElevenlabsProvider{}).NativeRealtimeProtocol(NativeScribeRoute, "")
	require.Nil(t, err)

	for frame, want := range map[string]schemas.RealtimeEventType{
		`{"message_type":"session_started","session_id":"sess-1","config":{}}`:              schemas.RTEventSessionCreated,
		`{"message_type":"partial_transcript","text":"hel"}`:                                "partial_transcript",
		`{"message_type":"committed_transcript_with_timestamps","text":"hello","words":[]}`: "committed_transcript_with_timestamps",
		`{"message_type":"committed_transcript","text":"hello"}`:                            schemas.RTEventInputAudioTransCompleted,
		`{"message_type":"quota_exceeded","error":"You have exceeded your quota."}`:         "quota_exceeded",
		`{"message_type":"input_error","error":"Failed to decode audio chunk"}`:             "input_error",
		`{"message_type":"auth_error","error":"Invalid API key"}`:                           "auth_error",
	} {
		event, parseErr := protocol.ToBifrostRealtimeEvent(json.RawMessage(frame))
		require.NoError(t, parseErr, frame)
		require.Equal(t, want, event.Type, frame)
		require.JSONEq(t, frame, string(event.RawData), "the frame is forwarded as it was")
		require.True(t, protocol.ShouldForwardRealtimeEvent(event))
		require.False(t, protocol.ShouldStartRealtimeTurn(event), frame)
		if message := gjson.Get(frame, "error"); message.Exists() {
			require.NotNil(t, event.Error, frame)
			require.Equal(t, message.Str, event.Error.Message)
		} else {
			require.Nil(t, event.Error, frame)
		}
	}
	event, _ := protocol.ToBifrostRealtimeEvent(json.RawMessage(`{"message_type":"session_started","session_id":"sess-1"}`))
	require.Equal(t, "sess-1", event.Session.ID)

	_, err = (&ElevenlabsProvider{}).NativeRealtimeProtocol(NativeDialogueRoute, "")
	require.NotNil(t, err, "the dialogue socket is one session, not governed turn by turn")
}

// BenchmarkScribeNativeProtocolClientFrame is one audio frame of a native Scribe client through the hooks: the
// cost the relay adds to every frame (8000 bytes is a quarter of a second of pcm_16000).
func BenchmarkScribeNativeProtocolClientFrame(b *testing.B) {
	protocol, _ := (&ElevenlabsProvider{}).NativeRealtimeProtocol(NativeScribeRoute, "audio_format=pcm_16000")
	frame := nativeScribeChunk(8000, false)
	b.SetBytes(int64(len(frame)))
	b.ReportAllocs()
	for b.Loop() {
		event, err := protocol.ToBifrostRealtimeEvent(frame)
		if err != nil {
			b.Fatal(err)
		}
		if _, err = protocol.ToProviderRealtimeEvent(event); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkScribeNativeProtocolPartial is one partial transcript from Scribe through the hooks.
func BenchmarkScribeNativeProtocolPartial(b *testing.B) {
	protocol, _ := (&ElevenlabsProvider{}).NativeRealtimeProtocol(NativeScribeRoute, "")
	frame := json.RawMessage(scribePartial("Hello from Bifrost. This is a test of the partial."))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := protocol.ToBifrostRealtimeEvent(frame); err != nil {
			b.Fatal(err)
		}
	}
}
