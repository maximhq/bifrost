package deepgram

import (
	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

func ToDeepgramTranscriptionRequest(
	bifrostReq *schemas.BifrostTranscriptionRequest,
) *DeepgramTranscriptionRequest {

	if bifrostReq == nil {
		return nil
	}

	req := &DeepgramTranscriptionRequest{}

	if bifrostReq.Model != "" {
		req.Model = bifrostReq.Model
	}

	if bifrostReq.Input != nil {
		req.File = bifrostReq.Input.File
		req.Filename = bifrostReq.Input.Filename
	}

	if bifrostReq.Params != nil && bifrostReq.Params.ExtraParams != nil {
		extra := bifrostReq.Params.ExtraParams

		if v, ok := schemas.SafeExtractBoolPointer(extra["smart_format"]); ok {
			req.SmartFormat = *v
		}

		if v, ok := schemas.SafeExtractBoolPointer(extra["punctuate"]); ok {
			req.Punctuate = *v
		}

		if v, ok := schemas.SafeExtractBoolPointer(extra["diarize"]); ok {
			req.Diarize = *v
		}

		if v, ok := schemas.SafeExtractBoolPointer(extra["paragraphs"]); ok {
			req.Paragraphs = *v
		}

		if v, ok := schemas.SafeExtractBoolPointer(extra["utterances"]); ok {
			req.Utterances = *v
		}

		if v, ok := schemas.SafeExtractBoolPointer(extra["numerals"]); ok {
			req.Numerals = *v
		}

		if v, ok := schemas.SafeExtractBoolPointer(extra["detect_language"]); ok {
			req.DetectLanguage = *v
		}

		if v, ok := schemas.SafeExtractStringPointer(extra["language"]); ok {
			req.Language = *v
		}

		if v, ok := schemas.SafeExtractStringPointer(extra["keywords"]); ok {
			req.Keywords = []string{*v}
		}

		if v, ok := schemas.SafeExtractStringPointer(extra["replace"]); ok {
			req.Replace = []string{*v}
		}

		if v, ok := schemas.SafeExtractStringPointer(extra["redact"]); ok {
			req.Redact = *v
		}

		if v, ok := schemas.SafeExtractStringPointer(extra["search"]); ok {
			req.Search = []string{*v}
		}

		if v, ok := schemas.SafeExtractBoolPointer(extra["summarize"]); ok {
			req.Summarize = *v
		}

		if v, ok := schemas.SafeExtractBoolPointer(extra["topics"]); ok {
			req.Topics = *v
		}

		if v, ok := schemas.SafeExtractBoolPointer(extra["intents"]); ok {
			req.Intents = *v
		}

		if v, ok := schemas.SafeExtractBoolPointer(extra["sentiment"]); ok {
			req.Sentiment = *v
		}
	}

	return req
}

func ToBifrostTranscriptionResponse(
	resp *DeepgramTranscriptionResponse,
) *schemas.BifrostTranscriptionResponse {

	if resp == nil {
		return nil
	}

	result := &schemas.BifrostTranscriptionResponse{}

	if len(resp.Results.Channels) == 0 {
		return result
	}

	channel := resp.Results.Channels[0]

	if len(channel.Alternatives) == 0 {
		return result
	}

	alt := channel.Alternatives[0]

	result.Text = alt.Transcript
	result.Duration = &resp.Metadata.Duration

	result.Words = make([]schemas.TranscriptionWord, 0, len(alt.Words))

	for _, w := range alt.Words {
		result.Words = append(result.Words, schemas.TranscriptionWord{
			Word:       w.Word,
			Start:      w.Start,
			End:        w.End,
		})
	}

	return result
}

func parseTranscriptionResponse(
	body []byte,
) (*DeepgramTranscriptionResponse, error) {

	var resp DeepgramTranscriptionResponse

	if err := sonic.Unmarshal(body, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}