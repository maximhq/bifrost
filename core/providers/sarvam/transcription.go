package sarvam

import (
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// ToSarvamTranscriptionRequest maps a Bifrost transcription request onto Sarvam's speech-to-text fields.
func ToSarvamTranscriptionRequest(bifrostReq *schemas.BifrostTranscriptionRequest) *SarvamTranscriptionRequest {
	if bifrostReq == nil {
		return nil
	}

	req := &SarvamTranscriptionRequest{
		Model: bifrostReq.Model,
	}

	if bifrostReq.Input != nil {
		req.File = bifrostReq.Input.File
		req.Filename = bifrostReq.Input.Filename
	}

	if bifrostReq.Params == nil {
		return req
	}

	if bifrostReq.Params.Language != nil {
		req.LanguageCode = bifrostReq.Params.Language
	}

	if ep := bifrostReq.Params.ExtraParams; ep != nil {
		if v, ok := schemas.SafeExtractStringPointer(ep["mode"]); ok {
			req.Mode = v
		}
		if v, ok := schemas.SafeExtractStringPointer(ep["language_code"]); ok {
			req.LanguageCode = v
		}
		if v, ok := schemas.SafeExtractStringPointer(ep["input_audio_codec"]); ok {
			req.InputAudioCodec = v
		}
	}

	return req
}

// ToBifrostTranscriptionResponse maps Sarvam's speech-to-text response onto Bifrost's transcription response.
func ToBifrostTranscriptionResponse(sarvamResp *SarvamTranscriptionResponse) *schemas.BifrostTranscriptionResponse {
	if sarvamResp == nil {
		return nil
	}

	response := &schemas.BifrostTranscriptionResponse{
		Text: sarvamResp.Transcript,
	}

	if sarvamResp.LanguageCode != nil && *sarvamResp.LanguageCode != "" {
		response.Language = sarvamResp.LanguageCode
	}

	var maxEnd float64
	var hasEnd bool

	if ts := sarvamResp.Timestamps; ts != nil && len(ts.Words) > 0 {
		words := make([]schemas.TranscriptionWord, 0, len(ts.Words))
		for i, w := range ts.Words {
			tw := schemas.TranscriptionWord{Word: w}
			if i < len(ts.StartTimeSeconds) {
				tw.Start = ts.StartTimeSeconds[i]
			}
			if i < len(ts.EndTimeSeconds) {
				tw.End = ts.EndTimeSeconds[i]
				if !hasEnd || tw.End > maxEnd {
					maxEnd = tw.End
					hasEnd = true
				}
			}
			words = append(words, tw)
		}
		response.Words = words
	}

	if dt := sarvamResp.DiarizedTranscript; dt != nil && len(dt.Entries) > 0 {
		segments := make([]schemas.TranscriptionDiarizedSegment, 0, len(dt.Entries))
		for _, e := range dt.Entries {
			segments = append(segments, schemas.TranscriptionDiarizedSegment{
				Type:    "transcript.text.segment",
				Speaker: e.SpeakerID,
				Start:   e.StartTimeSeconds,
				End:     e.EndTimeSeconds,
				Text:    e.Transcript,
			})
			if !hasEnd || e.EndTimeSeconds > maxEnd {
				maxEnd = e.EndTimeSeconds
				hasEnd = true
			}
		}
		response.DiarizedSegments = segments
	}

	if hasEnd {
		response.Duration = &maxEnd
	}

	return response
}
