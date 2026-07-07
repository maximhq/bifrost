package deepgram

import (
	"net/url"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// BuildTranscriptionQueryParams builds the Deepgram /v1/listen query string from a
// Bifrost transcription request. Deepgram configures nearly all Listen behavior via
// query parameters rather than JSON body fields (unlike OpenAI/ElevenLabs' multipart
// form fields), so only `model` and `language` are first-classed here — everything
// else (diarization, formatting toggles, analytics toggles, keyword boosts, etc.)
// passes through via ExtraParams, same pattern used for ElevenLabs' provider-specific
// fields.
func BuildTranscriptionQueryParams(bifrostReq *schemas.BifrostTranscriptionRequest) url.Values {
	q := url.Values{}
	if bifrostReq == nil {
		return q
	}

	if bifrostReq.Model != "" {
		q.Set("model", bifrostReq.Model)
	}

	if bifrostReq.Params != nil {
		if bifrostReq.Params.Language != nil && strings.TrimSpace(*bifrostReq.Params.Language) != "" {
			q.Set("language", *bifrostReq.Params.Language)
		}
		if bifrostReq.Params.ExtraParams != nil {
			appendExtraParamsAsQuery(q, bifrostReq.Params.ExtraParams)
		}
	}

	return q
}

// ToBifrostTranscriptionResponse converts a Deepgram /v1/listen response into
// Bifrost's unified transcription schema. Deepgram's analytics sidecars
// (sentiment/summary/topics/intents/entities) have no field in the unified schema
// today; they're only recoverable via the raw-response passthrough mechanism
// (send_back_raw_response), same gap noted in the compatibility report.
func ToBifrostTranscriptionResponse(resp *DeepgramTranscriptionResponse) *schemas.BifrostTranscriptionResponse {
	if resp == nil {
		return nil
	}

	response := &schemas.BifrostTranscriptionResponse{}

	if resp.Metadata != nil {
		response.Duration = resp.Metadata.Duration
	}

	if resp.Results == nil || len(resp.Results.Channels) == 0 || len(resp.Results.Channels[0].Alternatives) == 0 {
		return response
	}

	channel := resp.Results.Channels[0]
	alt := channel.Alternatives[0]

	response.Text = alt.Transcript

	if channel.DetectedLanguage != nil {
		response.Language = channel.DetectedLanguage
	}

	if len(alt.Words) > 0 {
		words := make([]schemas.TranscriptionWord, 0, len(alt.Words))
		logProbs := make([]schemas.TranscriptionLogProb, 0, len(alt.Words))
		for _, w := range alt.Words {
			word := w.Word
			if w.PunctuatedWord != nil && *w.PunctuatedWord != "" {
				word = *w.PunctuatedWord
			}
			words = append(words, schemas.TranscriptionWord{
				Word:  word,
				Start: w.Start,
				End:   w.End,
			})
			// TranscriptionWord has no per-word confidence field; carry Deepgram's
			// word-level confidence in LogProb as a best-effort mapping, same
			// precedent used by the ElevenLabs converter for its own word confidences.
			logProbs = append(logProbs, schemas.TranscriptionLogProb{
				Token:   word,
				LogProb: w.Confidence,
			})
		}
		response.Words = words
		response.LogProbs = logProbs
	}

	return response
}
