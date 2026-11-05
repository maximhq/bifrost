package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func TestRealtimeWebRTCUpstreamErrorPreservesProviderResponse(t *testing.T) {
	t.Parallel()

	provider := &AzureProvider{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	var resp fasthttp.Response
	resp.SetStatusCode(fasthttp.StatusTooManyRequests)
	resp.SetBodyString(`{"error":{"message":"Quota exceeded","type":"insufficient_quota","code":"insufficient_quota","param":"model"}}`)

	bifrostErr := provider.realtimeWebRTCUpstreamError(ctx, &resp)
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != fasthttp.StatusTooManyRequests {
		t.Fatalf("status = %v, want %d", bifrostErr.StatusCode, fasthttp.StatusTooManyRequests)
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Message != "Quota exceeded" {
		t.Fatalf("error = %#v", bifrostErr.Error)
	}
	if bifrostErr.Error.Type == nil || *bifrostErr.Error.Type != "insufficient_quota" {
		t.Fatalf("error type = %v", bifrostErr.Error.Type)
	}
	if bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "insufficient_quota" {
		t.Fatalf("error code = %v", bifrostErr.Error.Code)
	}
	if bifrostErr.ExtraFields.RoutingInfo.Provider != schemas.Azure || bifrostErr.ExtraFields.RequestType != schemas.RealtimeRequest {
		t.Fatalf("extra fields = %#v", bifrostErr.ExtraFields)
	}
	if bifrostErr.ExtraFields.Provider != schemas.Azure { //nolint:staticcheck // deprecated field must stay in sync for backward compatibility
		t.Fatalf("deprecated provider = %v, want %v", bifrostErr.ExtraFields.Provider, schemas.Azure)
	}
	if bifrostErr.ExtraFields.RawResponse != nil {
		t.Fatalf("raw response = %#v, want nil", bifrostErr.ExtraFields.RawResponse)
	}
}

func TestRealtimeWebSocketURL(t *testing.T) {
	t.Parallel()

	provider := &AzureProvider{}
	key := schemas.Key{AzureKeyConfig: &schemas.AzureKeyConfig{
		Endpoint: *schemas.NewSecretVar("https://example.openai.azure.com/"),
	}}

	if got, err := provider.RealtimeWebSocketURL(nil, key, "deployment name", ""); err != nil || got != "wss://example.openai.azure.com/openai/v1/realtime?model=deployment+name" {
		t.Fatalf("RealtimeWebSocketURL() = %q, %v", got, err)
	}
	if got, err := provider.RealtimeWebSocketURL(nil, key, "deployment name", "transcription"); err != nil || got != "wss://example.openai.azure.com/openai/v1/realtime?intent=transcription" {
		t.Fatalf("RealtimeWebSocketURL() = %q, %v", got, err)
	}
}

// TestRealtimeRejectsKeyWithoutAzureConfig pins that a key carrying only a bearer value
// (the shape an unmapped ephemeral token arrives in) is answered with a client error by
// both realtime entry points instead of dereferencing the missing Azure config.
func TestRealtimeRejectsKeyWithoutAzureConfig(t *testing.T) {
	t.Parallel()

	provider := &AzureProvider{}
	keys := map[string]schemas.Key{
		"no azure config": {Value: *schemas.NewSecretVar("ek_unmapped_token")},
		"empty endpoint":  {Value: *schemas.NewSecretVar("ek_unmapped_token"), AzureKeyConfig: &schemas.AzureKeyConfig{Endpoint: *schemas.NewSecretVar("  ")}},
	}
	for name, key := range keys {
		t.Run(name, func(t *testing.T) {
			got, bifrostErr := provider.RealtimeWebSocketURL(nil, key, "gpt-realtime", "")
			if bifrostErr == nil || got != "" {
				t.Fatalf("RealtimeWebSocketURL() = %q, %v, want an error and no URL", got, bifrostErr)
			}
			if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != fasthttp.StatusBadRequest {
				t.Fatalf("RealtimeWebSocketURL() status = %v, want 400", bifrostErr.StatusCode)
			}
			if bifrostErr.Error == nil || bifrostErr.Error.Message != "azure realtime requires a configured endpoint" {
				t.Fatalf("RealtimeWebSocketURL() error = %#v, want the configured-endpoint message", bifrostErr.Error)
			}

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			answer, sdpErr := provider.ExchangeRealtimeWebRTCSDP(ctx, key, "gpt-realtime", "v=0", nil)
			if sdpErr == nil || answer != "" {
				t.Fatalf("ExchangeRealtimeWebRTCSDP() = %q, %v, want an error and no answer", answer, sdpErr)
			}
			if sdpErr.StatusCode == nil || *sdpErr.StatusCode != fasthttp.StatusBadRequest {
				t.Fatalf("ExchangeRealtimeWebRTCSDP() status = %v, want 400", sdpErr.StatusCode)
			}
		})
	}
}

func TestExchangeRealtimeWebRTCSDPUsesIntentForTranscription(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		session   string
		wantQuery string
	}{
		{
			name:      "dedicated transcription",
			session:   `{"type":"transcription","audio":{"input":{"transcription":{"model":"gpt-4o-transcribe"}}}}`,
			wantQuery: "intent=transcription",
		},
		{
			name:      "normal realtime with input transcription",
			session:   `{"type":"realtime","model":"gpt-realtime","audio":{"input":{"transcription":{"model":"whisper-1"}}}}`,
			wantQuery: "model=gpt-realtime",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handlerResult := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var handlerErr error
				if r.URL.Path != "/openai/v1/realtime" || r.URL.RawQuery != tt.wantQuery {
					handlerErr = fmt.Errorf("upstream URL = %s?%s, want /openai/v1/realtime?%s", r.URL.Path, r.URL.RawQuery, tt.wantQuery)
				}
				reader, err := r.MultipartReader()
				if err != nil {
					if handlerErr == nil {
						handlerErr = fmt.Errorf("MultipartReader() error = %w", err)
					}
				} else {
					fields := make(map[string]string)
					for {
						part, err := reader.NextPart()
						if err == io.EOF {
							break
						}
						if err != nil {
							handlerErr = fmt.Errorf("NextPart() error = %w", err)
							break
						}
						value, err := io.ReadAll(part)
						if err != nil {
							handlerErr = fmt.Errorf("ReadAll() error = %w", err)
							break
						}
						fields[part.FormName()] = string(value)
					}
					if handlerErr == nil && (fields["sdp"] != "v=0\r\n" || fields["session"] != tt.session) {
						handlerErr = fmt.Errorf("multipart fields = %#v", fields)
					}
				}
				handlerResult <- handlerErr
				w.Header().Set("Content-Type", "application/sdp")
				_, _ = w.Write([]byte("v=0\r\nanswer"))
			}))
			defer server.Close()

			provider, err := NewAzureProvider(&schemas.ProviderConfig{
				NetworkConfig: schemas.NetworkConfig{
					DefaultRequestTimeoutInSeconds: 10,
					AllowPrivateNetwork:            true,
				},
			}, &authTestLogger{})
			if err != nil {
				t.Fatalf("NewAzureProvider() error = %v", err)
			}
			key := schemas.Key{
				Value: *schemas.NewSecretVar("test-api-key"),
				AzureKeyConfig: &schemas.AzureKeyConfig{
					Endpoint: *schemas.NewSecretVar(server.URL),
				},
			}
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			answer, bifrostErr := provider.ExchangeRealtimeWebRTCSDP(ctx, key, "gpt-realtime", "v=0\r\n", []byte(tt.session))
			if bifrostErr != nil {
				t.Fatalf("ExchangeRealtimeWebRTCSDP() error = %v", bifrostErr)
			}
			if answer != "v=0\r\nanswer" {
				t.Fatalf("answer = %q, want SDP answer", answer)
			}
			if err := <-handlerResult; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRealtimeUpstreamErrorsCarryRetryHint(t *testing.T) {
	t.Parallel()

	var resp fasthttp.Response
	resp.SetStatusCode(fasthttp.StatusTooManyRequests)
	resp.Header.Set("retry-after-ms", "2500")
	resp.SetBodyString(`{"error":{"code":"429","message":"Requests to the Realtime API have exceeded the rate limit."}}`)

	provider := &AzureProvider{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	for name, bifrostErr := range map[string]*schemas.BifrostError{
		"webrtc handshake": provider.realtimeWebRTCUpstreamError(ctx, &resp),
		"client secret":    provider.parseRealtimeClientSecretError(ctx, &resp),
	} {
		if bifrostErr.Error.Message != "Requests to the Realtime API have exceeded the rate limit." {
			t.Errorf("%s: Message = %q", name, bifrostErr.Error.Message)
		}
		if bifrostErr.ExtraFields.RetryAfter != 2500 {
			t.Errorf("%s: RetryAfter = %d, want 2500", name, bifrostErr.ExtraFields.RetryAfter)
		}
	}
}

// azureVoiceLiveSessionUpdated is a session.updated event captured from Azure Voice Live
// (api-version 2026-04-10, gpt-realtime) after a session.update with an Azure HD voice.
const azureVoiceLiveSessionUpdated = `{"event_id":"event_46nM7FiJWfznxdvRfm9n0c","type":"session.updated","session":{"id":"sess_EcRcjUDsfvDkHAAj3UTNU","model":"gpt-realtime","modalities":["audio","text"],"instructions":"Answer in one short sentence.","voice":{"name":"en-US-Ava:DragonHDLatestNeural","type":"azure-standard","temperature":0.8,"custom_lexicon_url":null,"custom_text_normalization_url":null,"prefer_locales":null,"locale":null,"style":null,"pitch":null,"rate":null,"volume":null},"input_audio_format":"pcm16","output_audio_format":"pcm16","input_audio_transcription":null,"input_audio_noise_reduction":{"type":"azure_deep_noise_suppression"},"turn_detection":{"type":"azure_semantic_vad","threshold":0.5,"prefix_padding_ms":420,"silence_duration_ms":500,"create_response":true,"interrupt_response":true,"idle_timeout_ms":null,"profile":"classic","neg_threshold":null,"speech_duration_ms":null,"window_size":null,"distinct_ci_phones":null,"require_vowel":null,"require_vowel_assistant_speaking":null,"remove_filler_words":false,"languages":null,"auto_truncate":false,"appended_text_after_truncation":null},"tools":[],"tool_choice":"auto","max_response_output_tokens":"inf","expires_at":1792834957,"object":"realtime.session","input_audio_sampling_rate":24000,"animation":{"model_name":"default","outputs":["viseme_id"],"emotion_detection_interval_ms":null},"input_audio_echo_cancellation":null,"avatar":null,"output_audio_timestamp_types":["word"],"truncation_strategy":null,"include":null,"greeting":null,"handoff":null}}`

// TestAzureVoiceLiveSessionUpdatedKeepsSession pins that a Voice Live session, whose voice is
// always an object, still yields the session ID and voice the gateway logs.
func TestAzureVoiceLiveSessionUpdatedKeepsSession(t *testing.T) {
	t.Parallel()
	event, err := (&AzureProvider{}).ToBifrostRealtimeEvent([]byte(azureVoiceLiveSessionUpdated))
	if err != nil {
		t.Fatalf("ToBifrostRealtimeEvent: %v", err)
	}
	if event.Session == nil || event.Session.ID != "sess_EcRcjUDsfvDkHAAj3UTNU" {
		t.Fatalf("session = %+v, want the Voice Live session ID", event.Session)
	}
	voice := event.Session.Voice
	if voice.GetName() != "en-US-Ava:DragonHDLatestNeural" || voice.VoiceObject == nil || voice.VoiceObject.Type != "azure-standard" {
		t.Fatalf("voice = %+v", voice)
	}
	for _, field := range []string{"input_audio_noise_reduction", "animation", "output_audio_timestamp_types"} {
		if _, ok := event.Session.ExtraParams[field]; !ok {
			t.Errorf("session extra params lack %q", field)
		}
	}
}

func newVoiceLiveAliasContext(alias *schemas.AliasConfig) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{Key: "gpt-realtime-vl", Config: alias})
	return ctx
}

// TestAzureVoiceLiveWebSocketURL pins the upstream URL: use_voice_live_endpoints on the key or the
// alias sends the session to /voice-live/realtime, the alias wins in both directions, the alias
// api_version replaces the default, and the alias endpoint picks the Voice Live resource.
func TestAzureVoiceLiveWebSocketURL(t *testing.T) {
	t.Parallel()
	provider := &AzureProvider{}
	keyOn := schemas.Key{AzureKeyConfig: &schemas.AzureKeyConfig{Endpoint: *schemas.NewSecretVar("https://example.openai.azure.com/"), UseVoiceLiveEndpoints: true}}
	keyOff := schemas.Key{AzureKeyConfig: &schemas.AzureKeyConfig{Endpoint: *schemas.NewSecretVar("https://example.openai.azure.com/")}}
	const voiceLive = "wss://example.openai.azure.com/voice-live/realtime?api-version=2026-04-10&model=gpt-realtime"
	const openAI = "wss://example.openai.azure.com/openai/v1/realtime?model=gpt-realtime"
	cases := []struct {
		name string
		ctx  *schemas.BifrostContext
		key  schemas.Key
		want string
	}{
		{"key on", nil, keyOn, voiceLive},
		{"key off", nil, keyOff, openAI},
		{"alias on over key off", newVoiceLiveAliasContext(&schemas.AliasConfig{ModelID: "gpt-realtime", AzureAliasCfg: &schemas.AzureAliasCfg{UseVoiceLiveEndpoints: new(true)}}), keyOff, voiceLive},
		{"alias off over key on", newVoiceLiveAliasContext(&schemas.AliasConfig{ModelID: "gpt-realtime", AzureAliasCfg: &schemas.AzureAliasCfg{UseVoiceLiveEndpoints: new(false)}}), keyOn, openAI},
		{"alias api_version", newVoiceLiveAliasContext(&schemas.AliasConfig{ModelID: "gpt-realtime", AzureAliasCfg: &schemas.AzureAliasCfg{UseVoiceLiveEndpoints: new(true), APIVersion: new("2025-10-01")}}), keyOff,
			"wss://example.openai.azure.com/voice-live/realtime?api-version=2025-10-01&model=gpt-realtime"},
		{"alias endpoint", newVoiceLiveAliasContext(&schemas.AliasConfig{ModelID: "gpt-realtime", AzureAliasCfg: &schemas.AzureAliasCfg{UseVoiceLiveEndpoints: new(true), Endpoint: schemas.NewSecretVar("https://voice.services.ai.azure.com/")}}), keyOff,
			"wss://voice.services.ai.azure.com/voice-live/realtime?api-version=2026-04-10&model=gpt-realtime"},
		{"alias endpoint without voice live", newVoiceLiveAliasContext(&schemas.AliasConfig{ModelID: "gpt-realtime", AzureAliasCfg: &schemas.AzureAliasCfg{Endpoint: schemas.NewSecretVar("https://voice.services.ai.azure.com/")}}), keyOff, openAI},
	}
	for _, tc := range cases {
		if got, err := provider.RealtimeWebSocketURL(tc.ctx, tc.key, "gpt-realtime", ""); err != nil || got != tc.want {
			t.Errorf("%s: RealtimeWebSocketURL() = %q, %v, want %q", tc.name, got, err, tc.want)
		}
	}
	if got, err := provider.RealtimeWebSocketURL(nil, keyOn, "gpt-realtime", "transcription"); err == nil || got != "" || err.StatusCode == nil || *err.StatusCode != fasthttp.StatusBadRequest {
		t.Errorf("intent on Voice Live: RealtimeWebSocketURL() = %q, %v, want a 400", got, err)
	}
}

// TestAzureVoiceLiveMarksPricingSurface pins that a session sent to Voice Live is marked for the
// model's _voice_live rates, and one sent to Azure OpenAI is not.
func TestAzureVoiceLiveMarksPricingSurface(t *testing.T) {
	t.Parallel()
	provider := &AzureProvider{}
	for _, tc := range []struct {
		name      string
		voiceLive bool
		want      schemas.PricingSurface
	}{
		{"voice live", true, schemas.PricingSurfaceVoiceLive},
		{"azure openai", false, ""},
	} {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		key := schemas.Key{AzureKeyConfig: &schemas.AzureKeyConfig{Endpoint: *schemas.NewSecretVar("https://example.openai.azure.com/"), UseVoiceLiveEndpoints: tc.voiceLive}}
		if _, err := provider.RealtimeWebSocketURL(ctx, key, "gpt-realtime", ""); err != nil {
			t.Fatalf("%s: RealtimeWebSocketURL: %v", tc.name, err.Error.Message)
		}
		got, _ := ctx.Value(schemas.BifrostContextKeyRealtimePricingSurface).(schemas.PricingSurface)
		if got != tc.want {
			t.Errorf("%s: pricing surface = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestAzureVoiceLiveRejectsWebRTCAndClientSecrets pins the 400 on the routes Voice Live does not
// serve the OpenAI way, before any upstream call.
func TestAzureVoiceLiveRejectsWebRTCAndClientSecrets(t *testing.T) {
	t.Parallel()
	provider := &AzureProvider{}
	key := schemas.Key{AzureKeyConfig: &schemas.AzureKeyConfig{Endpoint: *schemas.NewSecretVar("https://example.openai.azure.com")}}
	ctx := newVoiceLiveAliasContext(&schemas.AliasConfig{ModelID: "gpt-realtime", AzureAliasCfg: &schemas.AzureAliasCfg{UseVoiceLiveEndpoints: new(true)}})
	const want = "azure voice live is served over the realtime websocket only"

	answer, sdpErr := provider.ExchangeRealtimeWebRTCSDP(ctx, key, "gpt-realtime", "v=0", nil)
	if answer != "" || sdpErr == nil || sdpErr.StatusCode == nil || *sdpErr.StatusCode != fasthttp.StatusBadRequest || sdpErr.Error.Message != want {
		t.Fatalf("ExchangeRealtimeWebRTCSDP() = %q, %+v, want a 400", answer, sdpErr)
	}
	resp, secretErr := provider.CreateRealtimeClientSecret(ctx, key, []byte(`{"session":{"type":"realtime","model":"gpt-realtime"}}`))
	if resp != nil || secretErr == nil || secretErr.StatusCode == nil || *secretErr.StatusCode != fasthttp.StatusBadRequest || secretErr.Error.Message != want {
		t.Fatalf("CreateRealtimeClientSecret() = %+v, %+v, want a 400", resp, secretErr)
	}
}

// TestAzureVoiceLiveEventsRoundTrip pins that Voice Live-only events parse without tearing the
// session down and that a client session.update keeps its Azure voice and session fields on the way
// upstream.
func TestAzureVoiceLiveEventsRoundTrip(t *testing.T) {
	t.Parallel()
	provider := &AzureProvider{}
	// Captured from Azure Voice Live with output_audio_timestamp_types ["word"] and viseme output.
	for _, raw := range []string{
		`{"event_id":"event_4olHXDdnb1qWpza6oVxN9G","type":"response.animation_viseme.delta","response_id":"resp_EcRcjmACOQveeLpYElB3A","item_id":"item_EcRcjuWbUv9D3806OKCUq","output_index":0,"content_index":0,"audio_offset_ms":0,"viseme_id":2}`,
		`{"event_id":"event_589Q87mBJD5lsN7twr1lkg","type":"response.audio_timestamp.delta","response_id":"resp_EcRcjmACOQveeLpYElB3A","item_id":"item_EcRcjuWbUv9D3806OKCUq","output_index":0,"content_index":0,"audio_offset_ms":70,"audio_duration_ms":320,"text":"Hello","timestamp_type":"word"}`,
	} {
		event, err := provider.ToBifrostRealtimeEvent([]byte(raw))
		if err != nil {
			t.Fatalf("ToBifrostRealtimeEvent(%s): %v", raw, err)
		}
		if string(event.RawData) != raw || !provider.ShouldForwardRealtimeEvent(event) {
			t.Errorf("event %s is not forwarded unchanged", event.Type)
		}
	}

	clientEvents := map[string][]string{
		`{"type":"session.update","session":{"voice":{"name":"en-US-Ava:DragonHDLatestNeural","type":"azure-standard","temperature":0.8},"input_audio_noise_reduction":{"type":"azure_deep_noise_suppression"},"turn_detection":{"type":"azure_semantic_vad"}}}`: {
			`"voice":{"name":"en-US-Ava:DragonHDLatestNeural","temperature":0.8,"type":"azure-standard"}`,
			`"input_audio_noise_reduction":{"type":"azure_deep_noise_suppression"}`,
			`"turn_detection":{"type":"azure_semantic_vad"}`,
		},
		`{"type":"session.avatar.connect","client_sdp":"v=0"}`: {`"client_sdp":"v=0"`},
	}
	for raw, wants := range clientEvents {
		event, err := schemas.ParseRealtimeEvent([]byte(raw))
		if err != nil {
			t.Fatalf("ParseRealtimeEvent(%s): %v", raw, err)
		}
		out, err := provider.ToProviderRealtimeEvent(event)
		if err != nil {
			t.Fatalf("ToProviderRealtimeEvent(%s): %v", raw, err)
		}
		for _, want := range wants {
			if !strings.Contains(string(out), want) {
				t.Errorf("upstream event %s lacks %s", out, want)
			}
		}
	}
}

// TestAzureVoiceLiveAvatarPassThrough pins the avatar flow verified live through the gateway: the
// avatar config goes upstream intact, and the avatar server events, WebSocket video chunks included,
// come back unchanged without entering the turn's output text.
func TestAzureVoiceLiveAvatarPassThrough(t *testing.T) {
	t.Parallel()
	provider := &AzureProvider{}
	avatar := `{"type":"photo-avatar","model":"vasa-1","character":"anika","customized":false,"output_protocol":"websocket","scene":{"zoom":1,"position_x":0,"amplitude":0.6},"video":{"codec":"h264","bitrate":2000000,"resolution":{"width":1280,"height":720},"background":{"color":"#00FF00FF"}},"ice_servers":[{"urls":["turn:relay.example.com:3478"],"username":"u","credential":"c"}]}`
	update, err := schemas.ParseRealtimeEvent([]byte(`{"type":"session.update","session":{"modalities":["text","audio"],"voice":{"name":"en-US-AvaMultilingualNeural","type":"azure-standard"},"avatar":` + avatar + `}}`))
	if err != nil {
		t.Fatalf("ParseRealtimeEvent: %v", err)
	}
	out, err := provider.ToProviderRealtimeEvent(update)
	if err != nil {
		t.Fatalf("ToProviderRealtimeEvent: %v", err)
	}
	var upstream struct {
		Session struct {
			Avatar any `json:"avatar"`
		} `json:"session"`
	}
	var want any
	if err := json.Unmarshal(out, &upstream); err != nil || json.Unmarshal([]byte(avatar), &want) != nil {
		t.Fatalf("unmarshal upstream %s: %v", out, err)
	}
	if !reflect.DeepEqual(upstream.Session.Avatar, want) {
		t.Errorf("upstream avatar = %v, want %v", upstream.Session.Avatar, want)
	}

	// Captured through the gateway; the SDP answer and the fMP4 chunk are shortened.
	for _, raw := range []string{
		`{"event_id":"event_5iGogkA5XVNmhOo6A208Pc","type":"session.avatar.connecting","server_sdp":"eyJ0eXBlIjogImFuc3dlciJ9"}`,
		`{"event_id":"event_3yejyN75W2alI2jsIbie2J","type":"session.avatar.switch_to_speaking","turn_id":"item_EcloMkjHMaghNX9NNoMnB"}`,
		`{"event_id":"event_4GoEcMSVnijN9bPnUtDsg9","type":"session.avatar.switch_to_idle","turn_id":""}`,
		`{"event_id":"event_6Z05skHFAxvvdmEhdfHwRy","type":"response.video.delta","output_index":0,"codec":"h264","delta":"AAAAHGZ0eXBpc281AAACAGlz"}`,
	} {
		event, err := provider.ToBifrostRealtimeEvent([]byte(raw))
		if err != nil {
			t.Fatalf("ToBifrostRealtimeEvent(%s): %v", raw, err)
		}
		if string(event.RawData) != raw || !provider.ShouldForwardRealtimeEvent(event) {
			t.Errorf("event %s is not forwarded unchanged", event.Type)
		}
		if event.Delta != nil || provider.ShouldAccumulateRealtimeOutput(event.Type) {
			t.Errorf("event %s feeds the turn output text", event.Type)
		}
	}
}
