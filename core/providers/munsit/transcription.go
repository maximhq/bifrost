package munsit

import (
	"errors"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

func ToMunsitTranscriptionRequest(
	bifrostReq *schemas.BifrostTranscriptionRequest,
) *MunsitTranscriptionRequest {

	if bifrostReq == nil {
		return nil
	}

	model := bifrostReq.Model
	if model == "" {
		model = "munsit"
	}

	req := &MunsitTranscriptionRequest{
		Model: model,
	}

	if bifrostReq.Input != nil {
		req.File = bifrostReq.Input.File
		req.Filename = bifrostReq.Input.Filename
	}

	if bifrostReq.Params != nil &&
		bifrostReq.Params.ExtraParams != nil {

		if v, ok := schemas.SafeExtractBoolPointer(
			bifrostReq.Params.ExtraParams["return_timestamps"],
		); ok {
			req.ReturnTimestamps = *v
		}

		if v, ok := schemas.SafeExtractBoolPointer(
			bifrostReq.Params.ExtraParams["return_confidence"],
		); ok {
			req.ReturnConfidence = *v
		}

		if v, ok := schemas.SafeExtractBoolPointer(
			bifrostReq.Params.ExtraParams["return_turns"],
		); ok {
			req.ReturnTurns = *v
		}

		if v, ok := schemas.SafeExtractBoolPointer(
			bifrostReq.Params.ExtraParams["return_gender"],
		); ok {
			req.ReturnGender = *v
		}

		if v, ok := schemas.SafeExtractBoolPointer(
			bifrostReq.Params.ExtraParams["return_sentiment"],
		); ok {
			req.ReturnSentiment = *v
		}
	}

	return req
}

func ToBifrostTranscriptionResponse(
    resp *MunsitTranscriptionResponse,
) *schemas.BifrostTranscriptionResponse {

    if resp == nil {
        return nil
    }

    result := &schemas.BifrostTranscriptionResponse{
        Text: resp.Data.Transcription,
    }

    result.Duration = &resp.Data.Duration

    words := make([]schemas.TranscriptionWord, 0, len(resp.Data.Timestamps))

	for _, ts := range resp.Data.Timestamps {
		words = append(words, schemas.TranscriptionWord{
			Word:  ts.Word,
			Start: ts.Start,
			End:   ts.End,
		})
	}

    result.Words = words

    return result
}

func parseTranscriptionResponse(body []byte) (*MunsitTranscriptionResponse, error) {

    var resp MunsitTranscriptionResponse

    if err := sonic.Unmarshal(body, &resp); err != nil {
        return nil, err
    }

    if resp.StatusCode != 200 {
        return nil, errors.New(resp.Message)
    }

    return &resp, nil
}
