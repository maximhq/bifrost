package azure

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const azureSpeechSSMLPrefix = `<speak version="1.0" xmlns="http://www.w3.org/2001/10/synthesis" xmlns:mstts="https://www.w3.org/2001/mstts" xml:lang="en-US">`

// Live fast transcription responses captured from an eastus2 resource.
const (
	azureSpeechFastResponse     = `{"durationMilliseconds":4262,"combinedPhrases":[{"text":"Hello from Bifrost. testing Azure Speech."}],"phrases":[{"offsetMilliseconds":110,"durationMilliseconds":1280,"text":"Hello from Bifrost.","words":[{"text":"Hello","offsetMilliseconds":110,"durationMilliseconds":400},{"text":"from","offsetMilliseconds":510,"durationMilliseconds":240},{"text":"Bifrost.","offsetMilliseconds":750,"durationMilliseconds":640}],"locale":"en-US","confidence":0.69548494},{"offsetMilliseconds":2190,"durationMilliseconds":1280,"text":"testing Azure Speech.","words":[{"text":"testing","offsetMilliseconds":2190,"durationMilliseconds":480},{"text":"Azure","offsetMilliseconds":2670,"durationMilliseconds":320},{"text":"Speech.","offsetMilliseconds":2990,"durationMilliseconds":480}],"locale":"en-US","confidence":0.69548494}]}`
	azureSpeechDiarizedResponse = `{"durationMilliseconds":4262,"combinedPhrases":[{"text":"Hello from Bifrost. testing Azure Speech."}],"phrases":[{"speaker":1,"offsetMilliseconds":110,"durationMilliseconds":1280,"text":"Hello from Bifrost.","words":[{"text":"Hello","offsetMilliseconds":110,"durationMilliseconds":400}],"locale":"en-US","confidence":0.6563118},{"speaker":2,"offsetMilliseconds":2190,"durationMilliseconds":1280,"text":"testing Azure Speech.","words":[{"text":"testing","offsetMilliseconds":2190,"durationMilliseconds":480}],"locale":"en-US","confidence":0.6563118}]}`
)

// azureSpeechShortAudioResponse is a live detailed short audio result from the same resource.
const azureSpeechShortAudioResponse = `{"RecognitionStatus":"Success","Offset":700000,"Duration":47200000,"DisplayText":"Hello from Bifrost, this is a Python speech SDK test.","NBest":[{"Confidence":0.70007783,"Lexical":"hello from bifrost this is a python speech SDK test","ITN":"hello from bifrost this is a python speech SDK test","MaskedITN":"hello from bifrost this is a python speech sdk test","Display":"Hello from Bifrost, this is a Python speech SDK test."}]}`

func TestToAzureSpeechSSML(t *testing.T) {
	t.Parallel()
	speechRequest := func(input, voice string, params *schemas.SpeechParameters) *schemas.BifrostSpeechRequest {
		if params == nil {
			params = &schemas.SpeechParameters{}
		}
		if voice != "" {
			params.VoiceConfig = &schemas.SpeechVoiceInput{Voice: new(voice)}
		}
		return &schemas.BifrostSpeechRequest{Input: &schemas.SpeechInput{Input: input}, Params: params}
	}

	cases := []struct {
		name    string
		model   string
		request *schemas.BifrostSpeechRequest
		want    string
	}{
		{
			name:    "neural voice is sent by name",
			model:   "neural",
			request: speechRequest("Hello", "en-US-JennyNeural", nil),
			want:    azureSpeechSSMLPrefix + `<voice name="en-US-JennyNeural">Hello</voice></speak>`,
		},
		{
			name:    "voice suffix comes from the model",
			model:   "MAI-Voice-2.1-Flash",
			request: speechRequest("Hello", "en-US-Harper", nil),
			want:    azureSpeechSSMLPrefix + `<voice name="en-US-Harper:MAI-Voice-2.1-Flash">Hello</voice></speak>`,
		},
		{
			name:    "voice that names its suffix is sent as is",
			model:   "DragonHDLatestNeural",
			request: speechRequest("Hello", "en-US-Ava:DragonHDOmniLatestNeural", nil),
			want:    azureSpeechSSMLPrefix + `<voice name="en-US-Ava:DragonHDOmniLatestNeural">Hello</voice></speak>`,
		},
		{
			name:    "text and attributes are escaped",
			model:   "neural",
			request: speechRequest(`Tom & "Jerry" <3`, `a"b`, nil),
			want:    azureSpeechSSMLPrefix + `<voice name="a&quot;b">Tom &amp; "Jerry" &lt;3</voice></speak>`,
		},
		{
			name:    "speed, style and language",
			model:   "neural",
			request: speechRequest("Hi", "de-DE-KatjaNeural", &schemas.SpeechParameters{Speed: new(1.2), LanguageCode: new("de-DE"), ExtraParams: map[string]interface{}{"style": "cheerful", "styledegree": "1.5", "role": "Girl"}}),
			want:    `<speak version="1.0" xmlns="http://www.w3.org/2001/10/synthesis" xmlns:mstts="https://www.w3.org/2001/mstts" xml:lang="de-DE"><voice name="de-DE-KatjaNeural"><mstts:express-as style="cheerful" styledegree="1.5" role="Girl"><prosody rate="1.2">Hi</prosody></mstts:express-as></voice></speak>`,
		},
		{
			name:    "instructions have no SSML form and are dropped",
			model:   "neural",
			request: speechRequest("Hi", "en-US-JennyNeural", &schemas.SpeechParameters{Instructions: "speak slowly"}),
			want:    azureSpeechSSMLPrefix + `<voice name="en-US-JennyNeural">Hi</voice></speak>`,
		},
		{
			name:    "SSML input is sent as is",
			model:   "neural",
			request: speechRequest(` <speak version="1.0"><voice name="x">raw</voice></speak>`, "ignored", nil),
			want:    ` <speak version="1.0"><voice name="x">raw</voice></speak>`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ToAzureSpeechSSML(tc.model, tc.request)
			if err != nil {
				t.Fatalf("ToAzureSpeechSSML: %v", err)
			}
			if got != tc.want {
				t.Errorf("ssml =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}

	t.Run("missing voice", func(t *testing.T) {
		t.Parallel()
		if _, err := ToAzureSpeechSSML("neural", speechRequest("Hi", "", nil)); err == nil {
			t.Fatal("want an error for a plain-text input without a voice")
		}
	})
}

func TestAzureSpeechOutputFormat(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":                          "audio-24khz-48kbitrate-mono-mp3",
		"mp3":                       "audio-24khz-48kbitrate-mono-mp3",
		"aac":                       "audio-24khz-48kbitrate-mono-mp3",
		"flac":                      "audio-24khz-48kbitrate-mono-mp3",
		"opus":                      "ogg-24khz-16bit-mono-opus",
		"wav":                       "riff-24khz-16bit-mono-pcm",
		"pcm":                       "raw-24khz-16bit-mono-pcm",
		"riff-48khz-16bit-mono-pcm": "riff-48khz-16bit-mono-pcm",
	}
	for in, want := range cases {
		if got := azureSpeechOutputFormat(in); got != want {
			t.Errorf("azureSpeechOutputFormat(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAzureSpeechBillableChars(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ssml string
		want int
	}{
		{"speak and voice tags are free", azureSpeechSSMLPrefix + `<voice name="en-US-JennyNeural">Hello world</voice></speak>`, 11},
		{"other markup is billed", azureSpeechSSMLPrefix + `<voice name="v"><prosody rate="1.2">Hi</prosody></voice></speak>`, len(`<prosody rate="1.2">Hi</prosody>`)},
		{"Han characters count twice", azureSpeechSSMLPrefix + `<voice name="zh-CN-XiaoxiaoNeural">你好, ok</voice></speak>`, 8},
		{"whitespace between tags is billed", "<speak>\n  <voice name=\"v\">a</voice>\n</speak>", 5},
		{"self-closing voice tag", `<speak><voice name="v"/>b</speak>`, 1},
	}
	for _, tc := range cases {
		if got := azureSpeechBillableChars(tc.ssml); got != tc.want {
			t.Errorf("%s: azureSpeechBillableChars = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestToAzureSpeechTranscriptionDefinition(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		model  string
		params *schemas.TranscriptionParameters
		want   string
	}{
		{"fast with a locale", "fast-transcription", &schemas.TranscriptionParameters{Language: new("en-US")}, `{"locales":["en-US"]}`},
		{"fast leaves a bare language code to identification", "fast-transcription", &schemas.TranscriptionParameters{Language: new("en"), Prompt: new("ignored")}, `{}`},
		{"fast with no params", "fast-transcription", nil, `{}`},
		{"llm speech with prompt", "llm-speech", &schemas.TranscriptionParameters{Prompt: new("Output lexical format.")}, `{"enhancedMode":{"enabled":true,"task":"transcribe","prompt":["Output lexical format."]}}`},
		{"llm speech translate", "llm-speech", &schemas.TranscriptionParameters{ExtraParams: map[string]interface{}{"target_language": "de"}}, `{"enhancedMode":{"enabled":true,"task":"translate","targetLanguage":"de"}}`},
		{
			"MAI model with word timestamps and style",
			"MAI-Transcribe-2",
			&schemas.TranscriptionParameters{Language: new("en"), ResponseFormat: new("verbose_json"), ExtraParams: map[string]interface{}{"transcribe_style": "clean"}},
			`{"locales":["en"],"enhancedMode":{"enabled":true,"model":"MAI-Transcribe-2","modelOptions":{"timestamps":"word","transcribeStyle":"clean"}}}`,
		},
		{"diarized_json turns on diarization", "fast-transcription", &schemas.TranscriptionParameters{ResponseFormat: new("diarized_json")}, `{"diarization":{"enabled":true}}`},
		{
			"multipart extra params arrive as strings",
			"fast-transcription",
			&schemas.TranscriptionParameters{ExtraParams: map[string]interface{}{"diarization": "true", "max_speakers": "3", "channels": "0, 1", "phrases": "Contoso, Rehaan", "profanity_filter_mode": "Masked"}},
			`{"channels":[0,1],"diarization":{"enabled":true,"maxSpeakers":3},"profanityFilterMode":"Masked","phraseList":{"phrases":["Contoso","Rehaan"]}}`,
		},
		{
			"multipart extra params can hold JSON lists",
			"fast-transcription",
			&schemas.TranscriptionParameters{ExtraParams: map[string]interface{}{"channels": "[0,1]", "phrases": `["Contoso, Ltd", "Rehaan"]`}},
			`{"channels":[0,1],"phraseList":{"phrases":["Contoso, Ltd","Rehaan"]}}`,
		},
		{
			"JSON extra params arrive typed",
			"fast-transcription",
			&schemas.TranscriptionParameters{ExtraParams: map[string]interface{}{"diarization": true, "channels": []interface{}{float64(0), float64(1)}, "phrases": []interface{}{"Contoso, Ltd"}}},
			`{"channels":[0,1],"diarization":{"enabled":true},"phraseList":{"phrases":["Contoso, Ltd"]}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.Marshal(ToAzureSpeechTranscriptionDefinition(tc.model, tc.params))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("definition = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestToBifrostAzureSpeechTranscriptionResponse(t *testing.T) {
	t.Parallel()
	decode := func(t *testing.T, body string) *schemas.BifrostTranscriptionResponse {
		t.Helper()
		var azureResponse AzureSpeechTranscriptionResponse
		if err := sonic.UnmarshalString(body, &azureResponse); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return ToBifrostTranscriptionResponse(&azureResponse)
	}

	t.Run("plain", func(t *testing.T) {
		t.Parallel()
		got := decode(t, azureSpeechFastResponse)
		if got.Text != "Hello from Bifrost. testing Azure Speech." {
			t.Errorf("text = %q", got.Text)
		}
		if got.Duration == nil || *got.Duration != 4.262 {
			t.Errorf("duration = %v, want 4.262", got.Duration)
		}
		// Azure metered 5 seconds for this 4.262 s clip.
		if got.Usage == nil || got.Usage.Type != "duration" || got.Usage.Seconds == nil || *got.Usage.Seconds != 5 {
			t.Errorf("usage = %+v, want duration of 5 seconds", got.Usage)
		}
		if got.Language == nil || *got.Language != "en-US" {
			t.Errorf("language = %v", got.Language)
		}
		if len(got.Segments) != 2 || got.Segments[1].Start != 2.19 || got.Segments[1].End != 3.47 || got.Segments[1].Text != "testing Azure Speech." {
			t.Errorf("segments = %+v", got.Segments)
		}
		if got.DiarizedSegments != nil {
			t.Errorf("diarized segments = %+v, want none without speakers", got.DiarizedSegments)
		}
		if len(got.Words) != 6 || got.Words[0].Word != "Hello" || got.Words[0].Start != 0.11 || got.Words[0].End != 0.51 || got.Words[0].Speaker != nil {
			t.Errorf("words = %+v", got.Words)
		}
	})

	t.Run("diarized", func(t *testing.T) {
		t.Parallel()
		got := decode(t, azureSpeechDiarizedResponse)
		if len(got.DiarizedSegments) != 2 || got.DiarizedSegments[1].Speaker != "2" || got.DiarizedSegments[1].Type != "transcript.text.segment" {
			t.Errorf("diarized segments = %+v", got.DiarizedSegments)
		}
		if got.Segments != nil {
			t.Errorf("segments = %+v, want none when speakers are present", got.Segments)
		}
		if len(got.Words) != 2 || got.Words[1].Speaker == nil || *got.Words[1].Speaker != "2" {
			t.Errorf("words = %+v", got.Words)
		}
	})
}

func TestParseAzureSpeechError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		status      int
		body        string
		wantCode    string
		wantMessage string
	}{
		{"text to speech empty 400", 400, "", "", "azure speech rejected the request (400): check the voice name, SSML and output format"},
		{"transcription error", 400, `{"code":"InvalidArgument","message":"The specified locale is not supported.","innerError":{"code":"InvalidLocale","message":"The specified locale is not supported."}}`, "InvalidLocale", "The specified locale is not supported."},
		{"transcription error without inner error", 400, `{"code":"InvalidRequest","message":"Audio data must be provided."}`, "InvalidRequest", "Audio data must be provided."},
		{"gateway error", 401, `{"error":{"code":"401","message":"Access denied due to invalid subscription key or wrong API endpoint."}}`, "401", "Access denied due to invalid subscription key or wrong API endpoint."},
		{"problem details", 400, `{"type":"https://tools.ietf.org/html/rfc9110#section-15.5.1","title":"One or more validation errors occurred.","status":400,"errors":{"Definition":["Invalid JSON format."]},"traceId":"00-1"}`, "", "One or more validation errors occurred. Definition: Invalid JSON format."},
		{"empty 401 keeps the generic message", 401, "", "", "authentication failed: unauthorized (401) - check your API key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resp := fasthttp.AcquireResponse()
			defer fasthttp.ReleaseResponse(resp)
			resp.SetStatusCode(tc.status)
			resp.SetBodyString(tc.body)

			got := parseAzureSpeechError(resp)
			if got.StatusCode == nil || *got.StatusCode != tc.status {
				t.Errorf("status = %v, want %d", got.StatusCode, tc.status)
			}
			if got.Error.Message != tc.wantMessage {
				t.Errorf("message = %q, want %q", got.Error.Message, tc.wantMessage)
			}
			gotCode := ""
			if got.Error.Code != nil {
				gotCode = *got.Error.Code
			}
			if gotCode != tc.wantCode {
				t.Errorf("code = %q, want %q", gotCode, tc.wantCode)
			}
		})
	}
}

// TestAzureUseSpeechEndpoints pins the routing gate: use_speech_endpoints on the key sends audio to
// Azure Speech, and a per-alias value wins over the key's in both directions.
func TestAzureUseSpeechEndpoints(t *testing.T) {
	t.Parallel()
	keyOn := schemas.Key{AzureKeyConfig: &schemas.AzureKeyConfig{UseSpeechEndpoints: true}}
	keyOff := schemas.Key{AzureKeyConfig: &schemas.AzureKeyConfig{}}
	aliasCtx := func(alias *schemas.AliasConfig) *schemas.BifrostContext {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{Key: "tts", Config: alias})
		return ctx
	}
	cases := []struct {
		name string
		ctx  *schemas.BifrostContext
		key  schemas.Key
		want bool
	}{
		{"key on", nil, keyOn, true},
		{"key off", nil, keyOff, false},
		{"no azure key config", nil, schemas.Key{}, false},
		{"alias on over key off", aliasCtx(&schemas.AliasConfig{ModelID: "neural", AzureAliasCfg: &schemas.AzureAliasCfg{UseSpeechEndpoints: new(true)}}), keyOff, true},
		{"alias off over key on", aliasCtx(&schemas.AliasConfig{ModelID: "gpt-4o-mini-tts", AzureAliasCfg: &schemas.AzureAliasCfg{UseSpeechEndpoints: new(false)}}), keyOn, false},
		{"alias without the field keeps key on", aliasCtx(&schemas.AliasConfig{ModelID: "neural", AzureAliasCfg: &schemas.AzureAliasCfg{APIVersion: new("v1")}}), keyOn, true},
		{"alias without azure config keeps key off", aliasCtx(&schemas.AliasConfig{ModelID: "neural"}), keyOff, false},
	}
	for _, tc := range cases {
		if got := useSpeechEndpoints(tc.ctx, tc.key); got != tc.want {
			t.Errorf("%s: useSpeechEndpoints = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestAzureSpeechBackfillKeepsProviderChars pins that core's backfill does not overwrite the
// billable characters Azure Speech counted with a plain rune count of the input.
func TestAzureSpeechBackfillKeepsProviderChars(t *testing.T) {
	t.Parallel()
	request := &schemas.BifrostSpeechRequest{Input: &schemas.SpeechInput{Input: "你好"}}

	counted := &schemas.BifrostSpeechResponse{Usage: &schemas.SpeechUsage{InputChars: 4}}
	counted.BackfillParams(request)
	if counted.Usage.InputChars != 4 {
		t.Errorf("InputChars = %d, want the provider's 4", counted.Usage.InputChars)
	}

	uncounted := &schemas.BifrostSpeechResponse{}
	uncounted.BackfillParams(request)
	if uncounted.Usage == nil || uncounted.Usage.InputChars != 2 {
		t.Errorf("usage = %+v, want the rune count 2 when the provider set none", uncounted.Usage)
	}
}

// azureSpeechUpstream is a fake Azure resource that records the last request and answers the
// AI Speech and Azure OpenAI audio paths.
type azureSpeechUpstream struct {
	*httptest.Server
	mu      sync.Mutex
	path    string
	query   string
	headers http.Header
	body    []byte
}

func newAzureSpeechUpstream(t *testing.T) *azureSpeechUpstream {
	t.Helper()
	u := &azureSpeechUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.path, u.query, u.headers, u.body = r.URL.Path, r.URL.RawQuery, r.Header.Clone(), body
		u.mu.Unlock()
		switch r.URL.Path {
		case azureSpeechSynthesisPath:
			w.Header().Set("Content-Type", "audio/mpeg")
			flusher, _ := w.(http.Flusher)
			for _, part := range []string{"ID3-part-1", "part-2"} {
				_, _ = w.Write([]byte(part))
				if flusher != nil {
					flusher.Flush()
				}
			}
		case azureSpeechTranscriptionPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(azureSpeechFastResponse))
		case "/stt/speech/recognition/interactive/cognitiveservices/v1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(azureSpeechShortAudioResponse))
		case "/openai/v1/audio/speech":
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte("openai-audio"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(u.Server.Close)
	return u
}

func (u *azureSpeechUpstream) last() (string, string, http.Header, []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.path, u.query, u.headers, u.body
}

func newAzureSpeechTestProvider(t *testing.T) *AzureProvider {
	t.Helper()
	provider, err := NewAzureProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{DefaultRequestTimeoutInSeconds: 10}}, &authTestLogger{})
	if err != nil {
		t.Fatalf("NewAzureProvider: %v", err)
	}
	return provider
}

func newAzureSpeechTestKey(endpoint string) schemas.Key {
	return schemas.Key{
		Value:          *schemas.NewSecretVar("speech-key"),
		Models:         []string{"*"},
		AzureKeyConfig: &schemas.AzureKeyConfig{Endpoint: *schemas.NewSecretVar(endpoint), UseSpeechEndpoints: true},
	}
}

func TestAzureSpeechSynthesisWire(t *testing.T) {
	t.Parallel()
	request := &schemas.BifrostSpeechRequest{
		Provider: schemas.Azure,
		Model:    "MAI-Voice-2.1-Flash",
		Input:    &schemas.SpeechInput{Input: "Your order shipped."},
		Params: &schemas.SpeechParameters{
			VoiceConfig:    &schemas.SpeechVoiceInput{Voice: new("en-US-Harper")},
			ResponseFormat: "opus",
			Speed:          new(1.2),
		},
	}
	wantSSML := azureSpeechSSMLPrefix + `<voice name="en-US-Harper:MAI-Voice-2.1-Flash"><prosody rate="1.2">Your order shipped.</prosody></voice></speak>`
	wantChars := len(`<prosody rate="1.2">Your order shipped.</prosody>`)
	assertWire := func(t *testing.T, upstream *azureSpeechUpstream) {
		t.Helper()
		path, _, headers, body := upstream.last()
		if path != azureSpeechSynthesisPath {
			t.Fatalf("path = %q, want %q", path, azureSpeechSynthesisPath)
		}
		if got := headers.Get("Ocp-Apim-Subscription-Key"); got != "speech-key" {
			t.Errorf("Ocp-Apim-Subscription-Key = %q, want the key", got)
		}
		if got := headers.Get("api-key"); got != "" {
			t.Errorf("api-key = %q, want none (Azure Speech answers 401 to it)", got)
		}
		if got := headers.Get("Content-Type"); got != "application/ssml+xml" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := headers.Get("X-Microsoft-OutputFormat"); got != "ogg-24khz-16bit-mono-opus" {
			t.Errorf("X-Microsoft-OutputFormat = %q", got)
		}
		if headers.Get("User-Agent") == "" {
			t.Error("User-Agent is missing; Azure answers 400 without one")
		}
		if string(body) != wantSSML {
			t.Errorf("body =\n%s\nwant\n%s", body, wantSSML)
		}
	}

	t.Run("Speech", func(t *testing.T) {
		t.Parallel()
		upstream := newAzureSpeechUpstream(t)
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		resp, bifrostErr := newAzureSpeechTestProvider(t).Speech(ctx, newAzureSpeechTestKey(upstream.URL), request)
		if bifrostErr != nil {
			t.Fatalf("Speech: %+v", bifrostErr.Error)
		}
		assertWire(t, upstream)
		if string(resp.Audio) != "ID3-part-1part-2" {
			t.Errorf("audio = %q", resp.Audio)
		}
		if resp.Usage == nil || resp.Usage.InputChars != wantChars {
			t.Errorf("usage = %+v, want %d billable characters", resp.Usage, wantChars)
		}
	})

	t.Run("SpeechStream", func(t *testing.T) {
		t.Parallel()
		upstream := newAzureSpeechUpstream(t)
		ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		postHook := func(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
			return resp, err
		}
		stream, bifrostErr := newAzureSpeechTestProvider(t).SpeechStream(ctx, postHook, nil, newAzureSpeechTestKey(upstream.URL), request)
		if bifrostErr != nil {
			t.Fatalf("SpeechStream: %+v", bifrostErr.Error)
		}
		var audio []byte
		var done *schemas.BifrostSpeechStreamResponse
		for chunk := range stream {
			if chunk.BifrostError != nil {
				t.Fatalf("stream error: %+v", chunk.BifrostError.Error)
			}
			if r := chunk.BifrostSpeechStreamResponse; r != nil {
				audio = append(audio, r.Audio...)
				if r.Type == schemas.SpeechStreamResponseTypeDone {
					done = r
				}
			}
		}
		assertWire(t, upstream)
		if string(audio) != "ID3-part-1part-2" {
			t.Errorf("streamed audio = %q", audio)
		}
		if done == nil || done.Usage == nil || done.Usage.InputChars != wantChars {
			t.Fatalf("done event = %+v, want usage with %d billable characters", done, wantChars)
		}
	})

	t.Run("BearerTokenIsUnchanged", func(t *testing.T) {
		t.Parallel()
		upstream := newAzureSpeechUpstream(t)
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(AzureAuthorizationTokenKey, "entra-token")
		if _, bifrostErr := newAzureSpeechTestProvider(t).Speech(ctx, newAzureSpeechTestKey(upstream.URL), request); bifrostErr != nil {
			t.Fatalf("Speech: %+v", bifrostErr.Error)
		}
		_, _, headers, _ := upstream.last()
		if got := headers.Get("Authorization"); got != "Bearer entra-token" {
			t.Errorf("Authorization = %q, want the bearer token", got)
		}
		if got := headers.Get("Ocp-Apim-Subscription-Key"); got != "" {
			t.Errorf("Ocp-Apim-Subscription-Key = %q, want none with a bearer token", got)
		}
	})

	t.Run("AliasOverrideRoutesToSpeech", func(t *testing.T) {
		t.Parallel()
		upstream := newAzureSpeechUpstream(t)
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{Key: "mai-voice", Config: &schemas.AliasConfig{
			ModelID:       "MAI-Voice-2.1-Flash",
			AzureAliasCfg: &schemas.AzureAliasCfg{UseSpeechEndpoints: new(true)},
		}})
		key := newAzureSpeechTestKey(upstream.URL)
		key.AzureKeyConfig.UseSpeechEndpoints = false
		if _, bifrostErr := newAzureSpeechTestProvider(t).Speech(ctx, key, request); bifrostErr != nil {
			t.Fatalf("Speech: %+v", bifrostErr.Error)
		}
		assertWire(t, upstream)
	})

	t.Run("KeyWithoutSpeechEndpointsKeepsOpenAIRoute", func(t *testing.T) {
		t.Parallel()
		upstream := newAzureSpeechUpstream(t)
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		openAIRequest := &schemas.BifrostSpeechRequest{
			Provider: schemas.Azure,
			Model:    "gpt-4o-mini-tts",
			Input:    &schemas.SpeechInput{Input: "Hi"},
			Params:   &schemas.SpeechParameters{VoiceConfig: &schemas.SpeechVoiceInput{Voice: new("alloy")}},
		}
		key := newAzureSpeechTestKey(upstream.URL)
		key.AzureKeyConfig.UseSpeechEndpoints = false
		if _, bifrostErr := newAzureSpeechTestProvider(t).Speech(ctx, key, openAIRequest); bifrostErr != nil {
			t.Fatalf("Speech: %+v", bifrostErr.Error)
		}
		if path, _, headers, _ := upstream.last(); path != "/openai/v1/audio/speech" || headers.Get("api-key") != "speech-key" {
			t.Errorf("path = %q, api-key = %q; want the Azure OpenAI route with api-key", path, headers.Get("api-key"))
		}
	})
}

// TestAzureSpeechBillsTheSynthesizedVoice pins that a voice whose suffix differs from the requested
// model is priced as that suffix: Azure synthesizes, and bills, the voice it is sent.
func TestAzureSpeechBillsTheSynthesizedVoice(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		model  string
		voice  string
		input  string
		served string
	}{
		{name: "HD voice under neural", model: "neural", voice: "en-US-Ava:DragonHDLatestNeural", input: "Hi", served: "DragonHDLatestNeural"},
		{name: "voice suffix wins over another suffix model", model: "DragonHDLatestNeural", voice: "en-US-Harper:MAI-Voice-2.1-Flash", input: "Hi", served: "MAI-Voice-2.1-Flash"},
		{name: "HD voice in SSML under neural", model: "neural", input: `<speak version="1.0" xmlns="http://www.w3.org/2001/10/synthesis" xml:lang="en-US"><voice name="en-US-Andrew:DragonHDLatestNeural">Hi</voice></speak>`, served: "DragonHDLatestNeural"},
		{name: "standard voice under neural", model: "neural", voice: "en-US-AvaNeural", input: "Hi"},
		{name: "voice takes the model suffix", model: "MAI-Voice-2.1-Flash", voice: "en-US-Harper", input: "Hi"},
	}
	for _, tt := range tests {
		request := &schemas.BifrostSpeechRequest{
			Provider: schemas.Azure,
			Model:    tt.model,
			Input:    &schemas.SpeechInput{Input: tt.input},
			Params:   &schemas.SpeechParameters{},
		}
		if tt.voice != "" {
			request.Params.VoiceConfig = &schemas.SpeechVoiceInput{Voice: new(tt.voice)}
		}
		assertServed := func(t *testing.T, got *string) {
			t.Helper()
			switch {
			case tt.served == "" && got != nil:
				t.Errorf("served model = %q, want none", *got)
			case tt.served != "" && (got == nil || *got != tt.served):
				t.Errorf("served model = %v, want %q", got, tt.served)
			}
		}
		t.Run(tt.name+"/Speech", func(t *testing.T) {
			t.Parallel()
			upstream := newAzureSpeechUpstream(t)
			ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
			resp, bifrostErr := newAzureSpeechTestProvider(t).Speech(ctx, newAzureSpeechTestKey(upstream.URL), request)
			if bifrostErr != nil {
				t.Fatalf("Speech: %+v", bifrostErr.Error)
			}
			assertServed(t, resp.ExtraFields.RoutingInfo.ServerSideFallbackModel)
		})
		t.Run(tt.name+"/SpeechStream", func(t *testing.T) {
			t.Parallel()
			upstream := newAzureSpeechUpstream(t)
			ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			postHook := func(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
				return resp, err
			}
			stream, bifrostErr := newAzureSpeechTestProvider(t).SpeechStream(ctx, postHook, nil, newAzureSpeechTestKey(upstream.URL), request)
			if bifrostErr != nil {
				t.Fatalf("SpeechStream: %+v", bifrostErr.Error)
			}
			var done *schemas.BifrostSpeechStreamResponse
			for chunk := range stream {
				if chunk.BifrostError != nil {
					t.Fatalf("stream error: %+v", chunk.BifrostError.Error)
				}
				if r := chunk.BifrostSpeechStreamResponse; r != nil && r.Type == schemas.SpeechStreamResponseTypeDone {
					done = r
				}
			}
			if done == nil {
				t.Fatal("no done event")
			}
			assertServed(t, done.ExtraFields.RoutingInfo.ServerSideFallbackModel)
		})
	}
}

func TestAzureSpeechTranscriptionWire(t *testing.T) {
	t.Parallel()
	upstream := newAzureSpeechUpstream(t)
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	request := &schemas.BifrostTranscriptionRequest{
		Provider: schemas.Azure,
		Model:    "fast-transcription",
		Input:    &schemas.TranscriptionInput{File: []byte("RIFF-audio"), Filename: "call.wav"},
		Params: &schemas.TranscriptionParameters{
			Language:    new("en-US"),
			ExtraParams: map[string]interface{}{"diarization": "true"},
		},
	}
	resp, bifrostErr := newAzureSpeechTestProvider(t).Transcription(ctx, newAzureSpeechTestKey(upstream.URL), request)
	if bifrostErr != nil {
		t.Fatalf("Transcription: %+v", bifrostErr.Error)
	}

	path, query, headers, body := upstream.last()
	if path != azureSpeechTranscriptionPath || query != "api-version=2025-10-15" {
		t.Fatalf("url = %s?%s, want %s?api-version=2025-10-15", path, query, azureSpeechTranscriptionPath)
	}
	if got := headers.Get("Ocp-Apim-Subscription-Key"); got != "speech-key" {
		t.Errorf("Ocp-Apim-Subscription-Key = %q, want the key", got)
	}
	_, mediaParams, err := mime.ParseMediaType(headers.Get("Content-Type"))
	if err != nil {
		t.Fatalf("content type: %v", err)
	}
	form, err := multipart.NewReader(strings.NewReader(string(body)), mediaParams["boundary"]).ReadForm(1 << 20)
	if err != nil {
		t.Fatalf("multipart: %v", err)
	}
	if got := form.Value["definition"]; len(got) != 1 || got[0] != `{"locales":["en-US"],"diarization":{"enabled":true}}` {
		t.Errorf("definition = %v", got)
	}
	if files := form.File["audio"]; len(files) != 1 || files[0].Filename != "call.wav" {
		t.Errorf("audio part = %v, want call.wav", files)
	}
	if resp.Text != "Hello from Bifrost. testing Azure Speech." || resp.Usage == nil || resp.Usage.Seconds == nil || *resp.Usage.Seconds != 5 {
		t.Errorf("response text = %q, usage = %+v", resp.Text, resp.Usage)
	}
}

// TestAzureSpeechTranscriptionDiarizedNoSpeech pins that diarized_json keeps its required segments list
// when Azure hears no speech, so an OpenAI SDK client can still read the response.
func TestAzureSpeechTranscriptionDiarizedNoSpeech(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"durationMilliseconds":2000,"combinedPhrases":[],"phrases":[]}`))
	}))
	t.Cleanup(upstream.Close)
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	request := &schemas.BifrostTranscriptionRequest{
		Provider: schemas.Azure,
		Model:    "speech/fast-transcription",
		Input:    &schemas.TranscriptionInput{File: []byte("RIFF-silence"), Filename: "silence.wav"},
		Params:   &schemas.TranscriptionParameters{ResponseFormat: new("diarized_json")},
	}
	resp, bifrostErr := newAzureSpeechTestProvider(t).Transcription(ctx, newAzureSpeechTestKey(upstream.URL), request)
	if bifrostErr != nil {
		t.Fatalf("Transcription: %+v", bifrostErr.Error)
	}
	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(body), `"segments":[]`) {
		t.Errorf("diarized_json without speech = %s, want \"segments\":[]", body)
	}
}

// TestAzureSpeechSynthesisRequestToBifrost pins the /azure text to speech conversion: the SSML goes
// through unchanged, the model is the first voice's suffix, so the price follows the voice, and the
// provider is left for routing to resolve.
func TestAzureSpeechSynthesisRequestToBifrost(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, ssml, wantModel, wantVoice, wantLanguage string
	}{
		{"standard voice", `<speak version="1.0" xml:lang="en-US"><voice name="en-US-AvaMultilingualNeural">Hi &amp; bye.</voice></speak>`, "neural", "en-US-AvaMultilingualNeural", "en-US"},
		{"first voice decides", `<speak version="1.0" xmlns="http://www.w3.org/2001/10/synthesis" xml:lang="de-DE"><voice name="de-DE-Seraphina:DragonHDLatestNeural">Hallo.</voice><voice name="en-US-Harper:MAI-Voice-2.1-Flash">Hi.</voice></speak>`, "DragonHDLatestNeural", "de-DE-Seraphina:DragonHDLatestNeural", "de-DE"},
		{"no voice", `<speak version="1.0">Hi.</speak>`, "neural", "", ""},
		{"not XML", `plain text`, "neural", "", ""},
	}
	for _, tc := range cases {
		request := (&AzureSpeechSynthesisRequest{SSML: tc.ssml, OutputFormat: "riff-24khz-16bit-mono-pcm"}).ToBifrostSpeechRequest()
		voice, language := "", ""
		if request.Params.VoiceConfig != nil && request.Params.VoiceConfig.Voice != nil {
			voice = *request.Params.VoiceConfig.Voice
		}
		if request.Params.LanguageCode != nil {
			language = *request.Params.LanguageCode
		}
		if request.Provider != "" || request.Model != tc.wantModel || voice != tc.wantVoice || language != tc.wantLanguage {
			t.Errorf("%s: provider %q model %q voice %q language %q, want no provider and %q %q %q", tc.name, request.Provider, request.Model, voice, language, tc.wantModel, tc.wantVoice, tc.wantLanguage)
		}
		if request.Input.Input != tc.ssml || request.Params.ResponseFormat != "riff-24khz-16bit-mono-pcm" {
			t.Errorf("%s: input %q format %q, want the SSML and format unchanged", tc.name, request.Input.Input, request.Params.ResponseFormat)
		}
	}
	request := (&AzureSpeechSynthesisRequest{SSML: cases[1].ssml}).ToBifrostSpeechRequest()
	if ssml, err := ToAzureSpeechSSML(request.Model, request); err != nil || ssml != cases[1].ssml {
		t.Errorf("SSML sent = %q, %v; want the request's SSML", ssml, err)
	}
}

// TestAzureSpeechContentType pins the Content-Type per output format family, as Azure sent them live.
func TestAzureSpeechContentType(t *testing.T) {
	t.Parallel()
	for format, want := range map[string]string{
		"":                                   "audio/mpeg",
		"audio-24khz-48kbitrate-mono-mp3":    "audio/mpeg",
		"wav":                                "audio/x-wav",
		"riff-8khz-8bit-mono-alaw":           "audio/x-wav",
		"ogg-24khz-16bit-mono-opus":          "audio/ogg",
		"webm-24khz-16bit-mono-opus":         "audio/webm",
		"raw-8khz-8bit-mono-mulaw":           "audio/basic",
		"audio-24khz-16bit-24kbps-mono-opus": "audio/opus",
		"amr-wb-16000hz":                     "audio/x-amr-wb-sh",
		"g722-16khz-64kbps":                  "audio/g722",
		"something-new":                      "application/octet-stream",
	} {
		if got := AzureSpeechContentType(format); got != want {
			t.Errorf("AzureSpeechContentType(%q) = %q, want %q", format, got, want)
		}
	}
}

// TestAzureSpeechFastTranscriptionRequestRoundTrip pins that a fast transcription definition sent to
// /azure reaches Azure as sent, that the model follows enhancedMode, and that routing picks the provider.
func TestAzureSpeechFastTranscriptionRequestRoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		definition, wantModel string
	}{
		{`{"locales":["en-US","de-DE"],"channels":[0,1],"diarization":{"enabled":true,"maxSpeakers":3},"profanityFilterMode":"Masked","phraseList":{"phrases":["Bifrost","Contoso"]}}`, "fast-transcription"},
		{`{"locales":["en-US"],"enhancedMode":{"enabled":true,"model":"MAI-Transcribe-2","modelOptions":{"timestamps":"word","transcribeStyle":"verbatim"}}}`, "MAI-Transcribe-2"},
		{`{"locales":["en-US"],"enhancedMode":{"enabled":true,"task":"translate","targetLanguage":"de","prompt":["Use a formal register."]}}`, "llm-speech"},
	}
	for _, tc := range cases {
		var definition AzureSpeechTranscriptionDefinition
		if err := sonic.Unmarshal([]byte(tc.definition), &definition); err != nil {
			t.Fatalf("definition: %v", err)
		}
		request := (&AzureSpeechFastTranscriptionRequest{Audio: []byte("RIFF"), Filename: "call.wav", Definition: definition}).ToBifrostTranscriptionRequest()
		if request.Provider != "" || request.Model != tc.wantModel || request.Input.Filename != "call.wav" {
			t.Errorf("provider %q model %q filename %q, want no provider, %q and call.wav", request.Provider, request.Model, request.Input.Filename, tc.wantModel)
		}
		got, _ := sonic.Marshal(ToAzureSpeechTranscriptionDefinition(request.Model, request.Params))
		want, _ := sonic.Marshal(definition)
		if string(got) != string(want) {
			t.Errorf("definition sent = %s\nwant %s", got, want)
		}
	}
}

// TestAzureSpeechShortAudioWire pins the short-audio model on the wire: the mode is the path, the
// options are the query and headers, and usage is the WAV length rounded up to the second.
func TestAzureSpeechShortAudioWire(t *testing.T) {
	t.Parallel()
	upstream := newAzureSpeechUpstream(t)
	provider := newAzureSpeechTestProvider(t)
	key := newAzureSpeechTestKey(upstream.URL)
	wav, err := providerUtils.ConvertPCMToWAV(make([]byte, 48000), providerUtils.PCMConfig{SampleRate: 16000, NumChannels: 1, BitsPerSample: 16})
	if err != nil {
		t.Fatalf("wav: %v", err)
	}
	request := (&AzureSpeechShortAudioRequest{Audio: wav, Mode: "interactive", Language: "de-DE", Format: "detailed", Profanity: "raw", EndpointID: "cid-1", PronunciationAssessment: "eyJSZWZlcmVuY2VUZXh0IjoiSGkifQ=="}).ToBifrostTranscriptionRequest()
	if request.Provider != "" || request.Model != "short-audio" {
		t.Fatalf("provider %q model %q, want no provider and short-audio", request.Provider, request.Model)
	}
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyCaptureRawResponse, true)
	resp, bifrostErr := provider.Transcription(ctx, key, request)
	if bifrostErr != nil {
		t.Fatalf("Transcription: %+v", bifrostErr.Error)
	}

	path, query, headers, body := upstream.last()
	if path != "/stt/speech/recognition/interactive/cognitiveservices/v1" || query != "cid=cid-1&format=detailed&language=de-DE&profanity=raw" {
		t.Errorf("url = %s?%s", path, query)
	}
	if headers.Get("Ocp-Apim-Subscription-Key") != "speech-key" || headers.Get("Pronunciation-Assessment") != "eyJSZWZlcmVuY2VUZXh0IjoiSGkifQ==" || headers.Get("Content-Type") != "audio/wav; codecs=audio/pcm" {
		t.Errorf("headers = %v", headers)
	}
	if string(body) != string(wav) {
		t.Errorf("body is not the audio")
	}
	if resp.Text != "Hello from Bifrost, this is a Python speech SDK test." || resp.Usage == nil || *resp.Usage.Seconds != 2 || *resp.Duration != 1.5 || *resp.Language != "de-DE" {
		t.Errorf("response text %q usage %+v duration %v", resp.Text, resp.Usage, resp.Duration)
	}
	if len(resp.Segments) != 1 || resp.Segments[0].Start != 0.07 || resp.Segments[0].End != 4.79 {
		t.Errorf("segments = %+v", resp.Segments)
	}
	if raw, ok := resp.ExtraFields.RawResponse.(map[string]interface{}); !ok || raw["NBest"] == nil {
		t.Errorf("raw response = %v, want Azure's JSON", resp.ExtraFields.RawResponse)
	}

	// A mode outside the three endpoints never reaches the URL path.
	request.Params.ExtraParams["recognition_mode"] = "../../tts"
	_, bifrostErr = provider.Transcription(ctx, key, request)
	if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 400 {
		t.Fatalf("unknown mode error = %+v, want 400", bifrostErr)
	}
	if path, _, _, _ := upstream.last(); path != "/stt/speech/recognition/interactive/cognitiveservices/v1" {
		t.Errorf("unknown mode reached %s", path)
	}
}

func TestAzureSpeechAudioSeconds(t *testing.T) {
	t.Parallel()
	wav, _ := providerUtils.ConvertPCMToWAV(make([]byte, 48000), providerUtils.PCMConfig{SampleRate: 16000, NumChannels: 1, BitsPerSample: 16})
	streamed := bytes.Clone(wav)
	binary.LittleEndian.PutUint32(streamed[40:44], 0xFFFFFFFF)
	// A LIST chunk with an odd size and its pad byte before the data chunk.
	listed := append(bytes.Clone(wav[:36]), append([]byte("LIST\x03\x00\x00\x00abc\x00"), wav[36:]...)...)
	ogg := []byte("OggS\x00\x02\x00\x00\x00\x00\x00\x00\x00\x00opus-head-pages")
	ogg = append(ogg, []byte("OggS\x00\x04")...)
	ogg = binary.LittleEndian.AppendUint64(ogg, 96000)
	ogg = append(ogg, make([]byte, 13)...)
	for name, tc := range map[string]struct {
		audio []byte
		want  float64
		ok    bool
	}{
		"wav":           {wav, 1.5, true},
		"streamed wav":  {streamed, 1.5, true},
		"wav with list": {listed, 1.5, true},
		"ogg opus":      {ogg, 2, true},
		"mp3":           {[]byte("ID3\x04\x00frames"), 0, false},
	} {
		if got, ok := azureSpeechAudioSeconds(tc.audio); got != tc.want || ok != tc.ok {
			t.Errorf("%s: %v %v, want %v %v", name, got, ok, tc.want, tc.ok)
		}
	}
}

// TestToAzureSpeechResponsesFromBifrost pins the Azure shapes rendered for results Azure AI Speech
// did not produce, such as an alias that sends the request to Whisper.
func TestToAzureSpeechResponsesFromBifrost(t *testing.T) {
	t.Parallel()
	response := &schemas.BifrostTranscriptionResponse{
		Text:     "Hello there. General Kenobi.",
		Duration: new(4.2),
		Language: new("en"),
		Segments: []schemas.TranscriptionSegment{{Start: 0.1, End: 1.4, Text: "Hello there."}, {Start: 2.0, End: 3.5, Text: "General Kenobi."}},
		Words:    []schemas.TranscriptionWord{{Word: "Hello", Start: 0.1, End: 0.6}, {Word: "there.", Start: 0.6, End: 1.4}, {Word: "General", Start: 2.0, End: 2.7}},
	}
	got, _ := sonic.Marshal(ToAzureSpeechTranscriptionResponse(response))
	want := `{"durationMilliseconds":4200,"combinedPhrases":[{"text":"Hello there. General Kenobi."}],"phrases":[{"offsetMilliseconds":100,"durationMilliseconds":1300,"text":"Hello there.","words":[{"text":"Hello","offsetMilliseconds":100,"durationMilliseconds":500},{"text":"there.","offsetMilliseconds":600,"durationMilliseconds":800}],"locale":"en"},{"offsetMilliseconds":2000,"durationMilliseconds":1500,"text":"General Kenobi.","words":[{"text":"General","offsetMilliseconds":2000,"durationMilliseconds":700}],"locale":"en"}]}`
	if string(got) != want {
		t.Errorf("fast transcription shape = %s\nwant %s", got, want)
	}

	diarized := &schemas.BifrostTranscriptionResponse{Text: "Hi.", DiarizedSegments: []schemas.TranscriptionDiarizedSegment{{Start: 0, End: 1, Text: "Hi.", Speaker: "2"}, {Start: 1, End: 2, Text: "Yo.", Speaker: "A"}}}
	phrases := ToAzureSpeechTranscriptionResponse(diarized).Phrases
	if len(phrases) != 2 || phrases[0].Speaker == nil || *phrases[0].Speaker != 2 || phrases[1].Speaker != nil {
		t.Errorf("diarized phrases = %+v, want speaker 2 then none", phrases)
	}

	got, _ = sonic.Marshal(ToAzureSpeechShortAudioResponse(response))
	want = `{"RecognitionStatus":"Success","Offset":1000000,"Duration":34000000,"DisplayText":"Hello there. General Kenobi.","NBest":[{"Display":"Hello there. General Kenobi."}]}`
	if string(got) != want {
		t.Errorf("short audio shape = %s\nwant %s", got, want)
	}
}

func TestToAzureSpeechError(t *testing.T) {
	t.Parallel()
	got, _ := sonic.Marshal(ToAzureSpeechError(&schemas.BifrostError{Error: &schemas.ErrorField{Message: "budget exceeded", Type: new("budget_exceeded")}}))
	if string(got) != `{"code":"budget_exceeded","message":"budget exceeded"}` {
		t.Errorf("error = %s", got)
	}
}

// BenchmarkAzureSpeechModelGate is the only cost the feature adds to Azure audio requests that
// are not Azure Speech: one alias lookup and a key flag read.
func BenchmarkAzureSpeechModelGate(b *testing.B) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	key := schemas.Key{AzureKeyConfig: &schemas.AzureKeyConfig{}}
	b.ReportAllocs()
	for b.Loop() {
		_ = useSpeechEndpoints(ctx, key)
	}
}

func BenchmarkToAzureSpeechSSML(b *testing.B) {
	request := &schemas.BifrostSpeechRequest{
		Input: &schemas.SpeechInput{Input: "Your order shipped and will arrive on Tuesday between 9 and 11 in the morning."},
		Params: &schemas.SpeechParameters{
			VoiceConfig: &schemas.SpeechVoiceInput{Voice: new("en-US-Harper")},
			Speed:       new(1.2),
			ExtraParams: map[string]interface{}{"style": "cheerful"},
		},
	}
	b.ReportAllocs()
	for b.Loop() {
		ssml, _ := ToAzureSpeechSSML("MAI-Voice-2.1-Flash", request)
		_ = azureSpeechBillableChars(ssml)
		_ = azureSpeechServedModel("MAI-Voice-2.1-Flash", *request.Params.VoiceConfig.Voice)
	}
}

// TestAzureListModelsWithSpeechEndpoints pins that a key with use_speech_endpoints lists the Azure AI
// Speech models beside its Azure OpenAI models: "neural", each voice suffix the voices list reports,
// and the transcription models, filtered like any other model. A failed voices list keeps the rest.
func TestAzureListModelsWithSpeechEndpoints(t *testing.T) {
	t.Parallel()
	const models = `{"object":"list","data":[{"id":"gpt-4o","status":"succeeded","created_at":1715558400,"object":"model"}]}`
	const voices = `[{"ShortName":"en-US-AvaNeural","VoiceType":"Neural"},{"ShortName":"en-US-Ava:DragonHDLatestNeural","VoiceType":"NeuralHD"},{"ShortName":"en-US-Andrew:DragonHDLatestNeural","VoiceType":"NeuralHD"},{"ShortName":"en-US-Harper:MAI-Voice-2.1-Flash","VoiceType":"NeuralHD"}]`
	transcription := []string{"azure/fast-transcription", "azure/llm-speech", "azure/short-audio", "azure/MAI-Transcribe-2"}
	tests := []struct {
		name         string
		speech       bool
		models       []string
		aliases      schemas.KeyAliases
		voicesStatus int
		want         []string
		wantVoices   bool
	}{
		{
			name:         "speech key lists the speech models",
			speech:       true,
			models:       []string{"*"},
			voicesStatus: http.StatusOK,
			want:         slices.Concat([]string{"azure/gpt-4o", "azure/neural", "azure/DragonHDLatestNeural", "azure/MAI-Voice-2.1-Flash"}, transcription),
			wantVoices:   true,
		},
		{
			name:         "key without speech endpoints",
			models:       []string{"*"},
			voicesStatus: http.StatusOK,
			want:         []string{"azure/gpt-4o"},
		},
		{
			name:         "alias with the speech override on a key without speech endpoints",
			models:       []string{"*"},
			aliases:      schemas.KeyAliases{"speech-neural": {ModelID: "neural", AzureAliasCfg: &schemas.AzureAliasCfg{UseSpeechEndpoints: new(true)}}},
			voicesStatus: http.StatusOK,
			want:         []string{"azure/gpt-4o", "azure/speech-neural"},
		},
		{
			name:         "allowlist filters the speech models",
			speech:       true,
			models:       []string{"gpt-4o", "neural", "fast-transcription"},
			voicesStatus: http.StatusOK,
			want:         []string{"azure/gpt-4o", "azure/neural", "azure/fast-transcription"},
			wantVoices:   true,
		},
		{
			name:         "failed voices list keeps the other models",
			speech:       true,
			models:       []string{"*"},
			voicesStatus: http.StatusInternalServerError,
			want:         slices.Concat([]string{"azure/gpt-4o", "azure/neural"}, transcription),
			wantVoices:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var voicesHeaders http.Header
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/openai/v1/models":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(models))
				case "/tts/cognitiveservices/voices/list":
					mu.Lock()
					voicesHeaders = r.Header.Clone()
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tt.voicesStatus)
					if tt.voicesStatus == http.StatusOK {
						_, _ = w.Write([]byte(voices))
					}
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(upstream.Close)

			key := newAzureSpeechTestKey(upstream.URL)
			key.Models = tt.models
			key.Aliases = tt.aliases
			key.AzureKeyConfig.UseSpeechEndpoints = tt.speech
			ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
			response, bifrostErr := newAzureSpeechTestProvider(t).listModelsByKey(ctx, key, &schemas.BifrostListModelsRequest{Provider: schemas.Azure})
			if bifrostErr != nil {
				t.Fatalf("listModelsByKey: %v", bifrostErr.Error.Message)
			}

			got := make([]string, 0, len(response.Data))
			for _, model := range response.Data {
				got = append(got, model.ID)
				if model.ID != "azure/gpt-4o" && model.Created != nil {
					t.Errorf("%s created = %d, want none", model.ID, *model.Created)
				}
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("models = %v, want %v", got, tt.want)
			}

			mu.Lock()
			defer mu.Unlock()
			if (voicesHeaders != nil) != tt.wantVoices {
				t.Fatalf("voices list requested = %v, want %v", voicesHeaders != nil, tt.wantVoices)
			}
			if voicesHeaders != nil && (voicesHeaders.Get("Ocp-Apim-Subscription-Key") != "speech-key" || voicesHeaders.Get("api-key") != "") {
				t.Errorf("voices list auth = Ocp-Apim-Subscription-Key %q, api-key %q; want the key as Ocp-Apim-Subscription-Key only", voicesHeaders.Get("Ocp-Apim-Subscription-Key"), voicesHeaders.Get("api-key"))
			}
		})
	}
}

// TestAzureListModelsSpeechVoicesTiming pins that the voices list runs beside the model list and stops
// before the key's deadline, so a slow voices list costs only the voice suffixes, never the listing.
func TestAzureListModelsSpeechVoicesTiming(t *testing.T) {
	t.Parallel()
	const models = `{"object":"list","data":[{"id":"gpt-4o","status":"succeeded","created_at":1715558400,"object":"model"}]}`
	const voices = `[{"ShortName":"en-US-Ava:DragonHDLatestNeural"}]`
	transcription := []string{"azure/fast-transcription", "azure/llm-speech", "azure/short-audio", "azure/MAI-Transcribe-2"}
	tests := []struct {
		name        string
		modelsDelay time.Duration
		voicesDelay time.Duration
		deadline    time.Duration
		maxElapsed  time.Duration
		want        []string
	}{
		{
			name:        "voices list runs beside the model list",
			modelsDelay: 800 * time.Millisecond,
			voicesDelay: 800 * time.Millisecond,
			deadline:    10 * time.Second,
			maxElapsed:  1400 * time.Millisecond,
			want:        slices.Concat([]string{"azure/gpt-4o", "azure/neural", "azure/DragonHDLatestNeural"}, transcription),
		},
		{
			name:        "voices list slower than the key's deadline",
			voicesDelay: 10 * time.Second,
			deadline:    2 * time.Second,
			maxElapsed:  1500 * time.Millisecond,
			want:        slices.Concat([]string{"azure/gpt-4o", "azure/neural"}, transcription),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			wait := func(r *http.Request, d time.Duration) {
				select {
				case <-time.After(d):
				case <-r.Context().Done():
				}
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/openai/v1/models":
					wait(r, tt.modelsDelay)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(models))
				case "/tts/cognitiveservices/voices/list":
					wait(r, tt.voicesDelay)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(voices))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(upstream.Close)

			ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(tt.deadline))
			defer ctx.Cancel()
			start := time.Now()
			response, bifrostErr := newAzureSpeechTestProvider(t).listModelsByKey(ctx, newAzureSpeechTestKey(upstream.URL), &schemas.BifrostListModelsRequest{Provider: schemas.Azure})
			elapsed := time.Since(start)
			if bifrostErr != nil {
				t.Fatalf("listModelsByKey: %v", bifrostErr.Error.Message)
			}
			if elapsed > tt.maxElapsed {
				t.Errorf("listModelsByKey took %v, want at most %v", elapsed.Round(time.Millisecond), tt.maxElapsed)
			}
			got := make([]string, 0, len(response.Data))
			for _, model := range response.Data {
				got = append(got, model.ID)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("models = %v, want %v", got, tt.want)
			}
		})
	}
}
