package deepgram

import (
	"net/url"
	"strconv"

	"github.com/maximhq/bifrost/core/schemas"
)

// bifrostToDeepgramEncoding maps Bifrost's single `response_format` string onto
// Deepgram's separate `encoding`/`container` query params. Deepgram's own default
// is encoding=mp3 with no container. Verify against a live response during the
// Phase 5 end-to-end test — this mapping is best-effort from the documented
// encodings (linear16, mulaw, alaw, mp3, opus, flac, aac), not yet confirmed live.
var bifrostToDeepgramEncoding = map[string]struct {
	encoding  string
	container string
}{
	"mp3":  {encoding: "mp3"},
	"wav":  {encoding: "linear16", container: "wav"},
	"pcm":  {encoding: "linear16"},
	"opus": {encoding: "opus", container: "ogg"},
	"flac": {encoding: "flac"},
	"aac":  {encoding: "aac"},
}

// ToDeepgramSpeakRequest converts a Bifrost speech request into Deepgram's
// /v1/speak JSON body. Deepgram's config surface (model/encoding/sample_rate/
// bit_rate/speed) lives entirely in query params, not the body, so this only
// carries the text.
func ToDeepgramSpeakRequest(bifrostReq *schemas.BifrostSpeechRequest) *DeepgramSpeakRequest {
	if bifrostReq == nil || bifrostReq.Input == nil || bifrostReq.Input.Input == "" {
		return nil
	}
	return &DeepgramSpeakRequest{Text: bifrostReq.Input.Input}
}

// BuildSpeakQueryParams builds the Deepgram /v1/speak query string from a Bifrost
// speech request. `model` doubles as Deepgram's voice selector (e.g. aura-asteria-en) —
// Deepgram has no separate voice-vs-model concept like ElevenLabs does.
func BuildSpeakQueryParams(bifrostReq *schemas.BifrostSpeechRequest) url.Values {
	q := url.Values{}
	if bifrostReq == nil {
		return q
	}

	if bifrostReq.Model != "" {
		q.Set("model", bifrostReq.Model)
	}

	if bifrostReq.Params != nil {
		if bifrostReq.Params.Speed != nil {
			q.Set("speed", strconv.FormatFloat(*bifrostReq.Params.Speed, 'f', -1, 64))
		}
		if mapped, ok := bifrostToDeepgramEncoding[bifrostReq.Params.ResponseFormat]; ok {
			q.Set("encoding", mapped.encoding)
			if mapped.container != "" {
				q.Set("container", mapped.container)
			}
		}
		if bifrostReq.Params.ExtraParams != nil {
			appendExtraParamsAsQuery(q, bifrostReq.Params.ExtraParams)
		}
	}

	return q
}
