package gemini

import (
	"encoding/json"
	"regexp"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// liveEvent is one OpenAI Realtime server event. The protocol has dozens of event types that share a
// handful of fields, so they share one struct. Fields where zero is a real value are pointers.
type liveEvent struct {
	Type         string        `json:"type"`
	EventID      string        `json:"event_id"`
	Session      *liveSession  `json:"session,omitempty"`
	Response     *liveResponse `json:"response,omitempty"`
	Item         *liveItem     `json:"item,omitempty"`
	Part         *livePart     `json:"part,omitempty"`
	Error        *liveError    `json:"error,omitempty"`
	ResponseID   string        `json:"response_id,omitempty"`
	ItemID       string        `json:"item_id,omitempty"`
	OutputIndex  *int          `json:"output_index,omitempty"`
	ContentIndex *int          `json:"content_index,omitempty"`
	CallID       string        `json:"call_id,omitempty"`
	Name         string        `json:"name,omitempty"`
	Delta        *string       `json:"delta,omitempty"`
	Transcript   *string       `json:"transcript,omitempty"`
	Text         *string       `json:"text,omitempty"`
	Arguments    *string       `json:"arguments,omitempty"`
	AudioStartMs *int          `json:"audio_start_ms,omitempty"`
	AudioEndMs   *int          `json:"audio_end_ms,omitempty"`
}

type liveResponse struct {
	ID            string             `json:"id"`
	Object        string             `json:"object"`
	Status        string             `json:"status"`
	StatusDetails *liveStatusDetails `json:"status_details,omitempty"`
	Output        []liveItem         `json:"output"`
	Usage         *liveUsage         `json:"usage,omitempty"`
}

type liveStatusDetails struct {
	Type   string `json:"type"`
	Reason string `json:"reason,omitempty"`
}

type liveItem struct {
	ID        string     `json:"id"`
	Object    string     `json:"object"`
	Type      string     `json:"type"`
	Status    string     `json:"status"`
	Role      string     `json:"role,omitempty"`
	Content   []livePart `json:"content,omitempty"`
	Name      string     `json:"name,omitempty"`
	CallID    string     `json:"call_id,omitempty"`
	Arguments string     `json:"arguments,omitempty"`
	Output    string     `json:"output,omitempty"`
}

type livePart struct {
	Type       string  `json:"type"`
	Text       *string `json:"text,omitempty"`
	Transcript *string `json:"transcript,omitempty"`
}

type liveError struct {
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

// liveUsage is the usage block of response.done, which is where Bifrost reads a turn's token counts.
type liveUsage struct {
	TotalTokens        int                    `json:"total_tokens"`
	InputTokens        int                    `json:"input_tokens"`
	OutputTokens       int                    `json:"output_tokens"`
	InputTokenDetails  liveInputTokenDetails  `json:"input_token_details"`
	OutputTokenDetails liveOutputTokenDetails `json:"output_token_details"`
}

type liveInputTokenDetails struct {
	TextTokens   int `json:"text_tokens"`
	AudioTokens  int `json:"audio_tokens"`
	ImageTokens  int `json:"image_tokens"`
	CachedTokens int `json:"cached_tokens"`
}

type liveOutputTokenDetails struct {
	TextTokens      int `json:"text_tokens"`
	AudioTokens     int `json:"audio_tokens"`
	ReasoningTokens int `json:"reasoning_tokens"`
}

// bifrost converts the usage into the shape Bifrost bills from, the same one the OpenAI-protocol
// route's response.done is read into.
func (u *liveUsage) bifrost() *schemas.BifrostLLMUsage {
	return &schemas.BifrostLLMUsage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.TotalTokens,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			TextTokens:       u.InputTokenDetails.TextTokens,
			AudioTokens:      u.InputTokenDetails.AudioTokens,
			ImageTokens:      u.InputTokenDetails.ImageTokens,
			CachedReadTokens: u.InputTokenDetails.CachedTokens,
		},
		CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
			TextTokens:      u.OutputTokenDetails.TextTokens,
			AudioTokens:     u.OutputTokenDetails.AudioTokens,
			ReasoningTokens: u.OutputTokenDetails.ReasoningTokens,
		},
	}
}

// add folds one generation's usage into the response's total. Live reports usage per generation, and
// an extended-thinking model can generate more than once for one response. Following the chat
// converter, thinking tokens count as output, with their own share under reasoning_tokens.
func (u *liveUsage) add(metadata *GeminiLiveUsageMetadata) {
	output := metadata.ResponseTokenCount + metadata.ThoughtsTokenCount
	u.InputTokens += metadata.PromptTokenCount
	u.OutputTokens += output
	u.TotalTokens += metadata.PromptTokenCount + output
	u.InputTokenDetails.CachedTokens += metadata.CachedContentTokenCount
	u.OutputTokenDetails.ReasoningTokens += metadata.ThoughtsTokenCount
	for _, detail := range metadata.PromptTokensDetails {
		switch detail.Modality {
		case ModalityText:
			u.InputTokenDetails.TextTokens += int(detail.TokenCount)
		case ModalityAudio:
			u.InputTokenDetails.AudioTokens += int(detail.TokenCount)
		case ModalityImage:
			u.InputTokenDetails.ImageTokens += int(detail.TokenCount)
		}
	}
	for _, detail := range metadata.ResponseTokensDetails {
		switch detail.Modality {
		case ModalityText:
			u.OutputTokenDetails.TextTokens += int(detail.TokenCount)
		case ModalityAudio:
			u.OutputTokenDetails.AudioTokens += int(detail.TokenCount)
		}
	}
}

// liveOutput collects the frames of one translation. The first marshal error is kept and returned
// with the translation, so the steps that build events need no error handling of their own.
type liveOutput struct {
	schemas.RealtimeTranslation
	err error
}

// marshal encodes a frame, or returns nil and records the error.
func (o *liveOutput) marshal(value any) json.RawMessage {
	frame, err := providerUtils.MarshalSorted(value)
	if err != nil {
		if o.err == nil {
			o.err = err
		}
		return nil
	}
	return frame
}

func (o *liveOutput) client(event *liveEvent) {
	if frame := o.marshal(event); frame != nil {
		o.Client = append(o.Client, frame)
	}
}

// liveInternalModel matches the internal model ids Live leaks into some close reasons.
var liveInternalModel = regexp.MustCompile(`models/gemini_api\S*`)

// liveCloseError turns the reason Live closes a connection with into the error event an OpenAI
// client expects. Live reports every failure, authentication included, only as a close frame whose
// plain-text reason is cut at about 120 bytes. A normal closure is not an error.
func liveCloseError(code int, reason string) *liveError {
	switch code {
	case 1000, 1001, 1005:
		return nil
	}
	lower := strings.ToLower(reason)
	switch {
	case strings.Contains(lower, "api key not valid"), strings.Contains(lower, "unregistered callers"):
		return &liveError{Type: "invalid_request_error", Code: "invalid_api_key", Message: "the Gemini API key was rejected"}
	case strings.Contains(lower, "not found for api version"), strings.Contains(lower, "not supported for bidigeneratecontent"):
		return &liveError{Type: "invalid_request_error", Code: "model_not_found", Message: "the model does not exist or does not support the Live API"}
	case strings.Contains(lower, "response modalities"):
		// The reason names the modality it refused in parentheses. Only audio is ever requested.
		return &liveError{Type: "invalid_request_error", Code: "unsupported_modality", Message: "the model cannot answer in audio, so it cannot serve a realtime voice session"}
	case strings.Contains(lower, "thinking level"):
		return &liveError{Type: "invalid_request_error", Code: "invalid_reasoning_effort", Message: "set session.reasoning.effort to low, medium or high for this model, and leave it unset for models that do not think"}
	case strings.Contains(lower, "quota"), strings.Contains(lower, "resource_exhausted"), strings.Contains(lower, "rate limit"):
		return &liveError{Type: "rate_limit_error", Code: "rate_limit_exceeded", Message: "the Gemini quota was exceeded"}
	}
	message := strings.TrimSpace(liveInternalModel.ReplaceAllString(reason, "the model"))
	if message == "" {
		message = "the Gemini Live session ended unexpectedly"
	}
	return &liveError{Type: "server_error", Code: "gemini_live_closed", Message: message}
}
