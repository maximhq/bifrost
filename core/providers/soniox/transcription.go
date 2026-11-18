package soniox

import (
	"maps"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToSonioxTranscriptionRequest maps a Bifrost transcription request onto the Soniox job body
// and the audio to upload. FileID is filled in once the upload completes.
func ToSonioxTranscriptionRequest(bifrostReq *schemas.BifrostTranscriptionRequest) *SonioxTranscriptionRequest {
	if bifrostReq == nil {
		return nil
	}

	req := &SonioxTranscriptionRequest{Model: bifrostReq.Model}
	if bifrostReq.Input != nil {
		req.File = bifrostReq.Input.File
		req.Filename = bifrostReq.Input.Filename
	}
	if bifrostReq.Params == nil {
		return req
	}

	params := bifrostReq.Params
	if params.Language != nil && strings.TrimSpace(*params.Language) != "" {
		req.LanguageHints = []string{*params.Language}
	}

	if params.ExtraParams != nil {
		// Work on a copy so a retry or fallback sees the caller's original params.
		req.ExtraParams = maps.Clone(params.ExtraParams)
		if diarization, ok := schemas.SafeExtractBoolPointer(req.ExtraParams["enable_speaker_diarization"]); ok {
			delete(req.ExtraParams, "enable_speaker_diarization")
			req.EnableSpeakerDiarization = diarization
		}
		if languageID, ok := schemas.SafeExtractBoolPointer(req.ExtraParams["enable_language_identification"]); ok {
			delete(req.ExtraParams, "enable_language_identification")
			req.EnableLanguageIdentification = languageID
		}
		if clientReferenceID, ok := schemas.SafeExtractStringPointer(req.ExtraParams["client_reference_id"]); ok {
			delete(req.ExtraParams, "client_reference_id")
			req.ClientReferenceID = clientReferenceID
		}
	}

	return req
}

// ToBifrostTranscriptionResponse maps a Soniox transcript onto a Bifrost transcription
// response. Soniox tokens are sub-word units, so a token that starts with whitespace opens
// a new word and any other token extends the current one. audioDurationMs comes from the job
// and drives duration-based usage.
func ToBifrostTranscriptionResponse(transcript *SonioxTranscript, audioDurationMs *int) *schemas.BifrostTranscriptionResponse {
	if transcript == nil {
		return nil
	}

	response := &schemas.BifrostTranscriptionResponse{
		Text:  transcript.Text,
		Words: sonioxTokensToWords(transcript.Tokens),
	}

	for _, token := range transcript.Tokens {
		if token.Language != nil && *token.Language != "" {
			response.Language = token.Language
			break
		}
	}

	switch {
	case audioDurationMs != nil:
		seconds := float64(*audioDurationMs) / 1000
		response.Duration = &seconds
		response.Usage = &schemas.TranscriptionUsage{Type: "duration", Seconds: new(seconds)}
	case len(transcript.Tokens) > 0:
		seconds := float64(transcript.Tokens[len(transcript.Tokens)-1].EndMs) / 1000
		response.Duration = &seconds
	}

	return response
}

func sonioxTokensToWords(tokens []SonioxToken) []schemas.TranscriptionWord {
	words := make([]schemas.TranscriptionWord, 0, len(tokens))
	var current *schemas.TranscriptionWord
	flush := func() {
		if current != nil && current.Word != "" {
			words = append(words, *current)
		}
	}

	for _, token := range tokens {
		first, _ := utf8.DecodeRuneInString(token.Text)
		if current == nil || unicode.IsSpace(first) {
			flush()
			current = &schemas.TranscriptionWord{
				Word:    strings.TrimSpace(token.Text),
				Start:   float64(token.StartMs) / 1000,
				End:     float64(token.EndMs) / 1000,
				Speaker: token.Speaker,
			}
			continue
		}
		if current.Word == "" {
			// The word so far was only whitespace, so it really starts at this token.
			current.Start = float64(token.StartMs) / 1000
			current.Speaker = token.Speaker
		}
		current.Word += token.Text
		current.End = float64(token.EndMs) / 1000
	}
	flush()

	if len(words) == 0 {
		return nil
	}
	return words
}
