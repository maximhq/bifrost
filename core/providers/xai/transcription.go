package xai

import (
	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// XAITranscriptionResponse is the JSON body of xAI's POST /v1/stt.
type XAITranscriptionResponse struct {
	Text     string                 `json:"text"`
	Language *string                `json:"language,omitempty"`
	Duration *float64               `json:"duration,omitempty"`
	Words    []XAITranscriptionWord `json:"words,omitempty"`
}

// XAITranscriptionWord is one word with its timing in seconds. xAI names the word
// "text" where OpenAI's verbose_json uses "word".
type XAITranscriptionWord struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// ToBifrostTranscriptionResponse maps an xAI STT response onto the Bifrost shape.
// xAI bills speech-to-text by audio duration, so usage is reported in seconds.
func (r *XAITranscriptionResponse) ToBifrostTranscriptionResponse() *schemas.BifrostTranscriptionResponse {
	out := &schemas.BifrostTranscriptionResponse{
		Text:     r.Text,
		Language: r.Language,
		Duration: r.Duration,
		Task:     schemas.Ptr("transcribe"),
	}
	if len(r.Words) > 0 {
		out.Words = make([]schemas.TranscriptionWord, len(r.Words))
		for i, w := range r.Words {
			out.Words[i] = schemas.TranscriptionWord{Word: w.Text, Start: w.Start, End: w.End}
		}
	}
	if r.Duration != nil {
		out.Usage = &schemas.TranscriptionUsage{Type: "duration", Seconds: r.Duration}
	}
	return out
}

// handleXAITranscriptionResponse decodes xAI's STT body for
// openai.HandleOpenAITranscriptionRequest.
func handleXAITranscriptionResponse(responseBody []byte, response *schemas.BifrostTranscriptionResponse, _ []byte, _ bool, sendBackRawResponse bool) (interface{}, interface{}, *schemas.BifrostError) {
	var xaiResp XAITranscriptionResponse
	if err := sonic.Unmarshal(responseBody, &xaiResp); err != nil {
		return nil, nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err)
	}
	*response = *xaiResp.ToBifrostTranscriptionResponse()

	var rawResponse interface{}
	if sendBackRawResponse {
		if err := sonic.Unmarshal(responseBody, &rawResponse); err != nil {
			return nil, nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRawResponseUnmarshal, err)
		}
	}
	return nil, rawResponse, nil
}
