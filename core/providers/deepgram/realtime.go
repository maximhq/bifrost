package deepgram

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

var _ schemas.ListenWebSocketProvider = (*DeepgramProvider)(nil)
var _ schemas.ListenBillingProvider = (*DeepgramProvider)(nil)

// Pay-As-You-Go live streaming rates (USD / minute). Datasheet rows for Deepgram
// currently carry pre-recorded rates (~$0.0043/min for nova-3), which under-bill
// live /v1/listen sessions — so listen cost is computed here when possible.
const (
	nova3LiveMonolingualUSDPerMin  = 0.0077
	nova3LiveMultilingualUSDPerMin = 0.0092
	fluxLiveEnglishUSDPerMin       = 0.0065
	fluxLiveMultilingualUSDPerMin  = 0.0078
)

// SupportsListenWebSocket reports that Deepgram supports the native live STT
// WebSocket at GET /v1/listen (binary audio in, JSON events out).
func (provider *DeepgramProvider) SupportsListenWebSocket() bool {
	return true
}

// ListenWebSocketURL builds the upstream Deepgram live transcription WebSocket URL.
//
// rawQuery is forwarded unchanged to Deepgram. Supported listen query params include
// (see https://developers.deepgram.com/reference/speech-to-text/listen-streaming):
//
//	model (required), language, encoding, sample_rate, channels, interim_results,
//	punctuate, smart_format, endpointing, vad_events, utterance_end_ms,
//	profanity_filter, numerals, mip_opt_out, diarize / diarize_model, dictation,
//	detect_entities, multichannel, redact, replace, search, keyterm, keywords,
//	tag, version, callback, callback_method, extra
//
// Clients (e.g. LiveKit) may also send no_delay / filler_words; those are forwarded
// as-is. Bifrost does not whitelist or rewrite listen query keys.
//
// Format: wss://api.deepgram.com/v1/listen?<rawQuery>
func (provider *DeepgramProvider) ListenWebSocketURL(key schemas.Key, rawQuery string) string {
	base := provider.getBaseURL(key)
	base = strings.Replace(base, "https://", "wss://", 1)
	base = strings.Replace(base, "http://", "ws://", 1)
	url := base + "/v1/listen"
	if q := strings.TrimPrefix(strings.TrimSpace(rawQuery), "?"); q != "" {
		url += "?" + q
	}
	return url
}

// ListenHeaders returns the headers required for the Deepgram live STT WebSocket.
// Deepgram expects Authorization: Token <api_key>.
// Client Bifrost headers (Bearer VK, x-bf-lh-workspace-id, x-bf-lh-call-id) stay on
// Bifrost for governance/logging and are not forwarded upstream.
func (provider *DeepgramProvider) ListenHeaders(_ *schemas.BifrostContext, key schemas.Key) (map[string]string, *schemas.BifrostError) {
	headers := map[string]string{
		"Authorization": "Token " + key.Value.GetValue(),
	}
	for k, v := range provider.networkConfig.ExtraHeaders {
		if strings.EqualFold(k, "Authorization") {
			continue
		}
		headers[k] = v
	}
	return headers, nil
}

// ListenCostUSD returns Deepgram live-streaming STT cost for a listen session.
// language set to a concrete code (e.g. "ar", "en") → monolingual rate;
// empty / "multi" / "multilingual" → multilingual rate.
// ok is false for models without a known live rate (caller falls back to datasheet).
func (provider *DeepgramProvider) ListenCostUSD(model string, seconds float64, language string) (float64, bool) {
	return liveListenCostUSD(model, seconds, language)
}

func liveListenCostUSD(model string, seconds float64, language string) (float64, bool) {
	if seconds <= 0 {
		return 0, false
	}
	ratePerMin, ok := liveListenUSDPerMinute(model, language)
	if !ok {
		return 0, false
	}
	return (seconds / 60.0) * ratePerMin, true
}

func liveListenUSDPerMinute(model, language string) (float64, bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	// Strip provider prefix if present (deepgram/nova-3).
	if _, name := schemas.ParseModelString(m, ""); name != "" {
		m = strings.ToLower(name)
	}
	multi := isLiveMultilingualLanguage(language)
	switch {
	case strings.HasPrefix(m, "nova-3"):
		if multi {
			return nova3LiveMultilingualUSDPerMin, true
		}
		return nova3LiveMonolingualUSDPerMin, true
	case strings.HasPrefix(m, "flux"):
		if multi {
			return fluxLiveMultilingualUSDPerMin, true
		}
		return fluxLiveEnglishUSDPerMin, true
	default:
		return 0, false
	}
}

// isLiveMultilingualLanguage reports Deepgram multilingual billing.
// A concrete language query param (e.g. language=ar) is monolingual;
// omitted language or language=multi is multilingual.
func isLiveMultilingualLanguage(language string) bool {
	lang := strings.ToLower(strings.TrimSpace(language))
	return lang == "" || lang == "multi" || lang == "multilingual"
}
