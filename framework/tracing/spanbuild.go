package tracing

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// SpanBuildOptions carries the per-request values that are not derivable from
// the request or response alone.
type SpanBuildOptions struct {
	Attachments schemas.AttachmentOptions
	Cost        *schemas.BifrostCost
	// WantContent is false when no connector reads message content, in which
	// case messages are never summarized and never marshalled.
	WantContent bool
	// WantRawPayloads is true only when a connector declared RawPayloadConsumer.
	WantRawPayloads bool
}

// BuildLLMSpanData assembles the typed payload for an LLM-call span.
//
// Chat is the only family filled today; the others fall through with identity
// and usage populated, and land their params as the remaining Populate*
// functions are migrated.
func BuildLLMSpanData(
	req *schemas.BifrostRequest,
	resp *schemas.BifrostResponse,
	bifrostErr *schemas.BifrostError,
	opts SpanBuildOptions,
) *schemas.LLMSpanData {
	d := &schemas.LLMSpanData{Cost: opts.Cost}

	if req != nil {
		provider, model, _ := req.GetRequestFields()
		d.Provider = provider
		d.RequestModel = model
		d.RequestType = req.RequestType
		buildRequestSide(d, req, opts)
	}
	if resp != nil {
		buildResponseSide(d, resp, opts)
	}
	if bifrostErr != nil {
		buildErrorSide(d, bifrostErr)
	}
	return d
}

// ApplyResponse fills the response and error sides of a record built earlier in
// the request. The two halves are populated at different points in the request
// lifecycle, so the record is assembled in two passes.
func ApplyResponse(d *schemas.LLMSpanData, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError, opts SpanBuildOptions) {
	if d == nil {
		return
	}
	if resp != nil {
		buildResponseSide(d, resp, opts)
	}
	if bifrostErr != nil {
		buildErrorSide(d, bifrostErr)
	}
}

func buildRequestSide(d *schemas.LLMSpanData, req *schemas.BifrostRequest, opts SpanBuildOptions) {
	switch req.RequestType {
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		r := req.ChatRequest
		if r == nil {
			return
		}
		d.Chat = r.Params
		if r.Input != nil {
			d.MessageCount = len(r.Input)
			if opts.WantContent {
				d.InputMessages = schemas.ExtractChatMessages(r.Input, opts.Attachments)
			}
		}
		if r.Params != nil {
			d.Extra = mergeExtraParams(d.Extra, r.Params.ExtraParams)
		}

	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
		r := req.ResponsesRequest
		if r == nil || r.Params == nil {
			return
		}
		d.Responses = r.Params
		if r.Input != nil {
			d.MessageCount = len(r.Input)
			if opts.WantContent {
				d.InputMessages = toMessageSummaries(extractResponsesInputMessages(r.Input))
			}
		}
		d.Extra = mergeExtraParamsJSON(d.Extra, r.Params.ExtraParams)

	case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
		r := req.TextCompletionRequest
		if r == nil {
			return
		}
		d.TextCompletion = r.Params
		d.TextPrompt = r.Input
		if r.Params != nil {
			d.Extra = mergeExtraParams(d.Extra, r.Params.ExtraParams)
		}

	case schemas.EmbeddingRequest:
		r := req.EmbeddingRequest
		if r == nil {
			return
		}
		d.Embedding = r.Params
		d.EmbeddingInput = r.Input
		if r.Params != nil {
			d.Extra = mergeExtraParams(d.Extra, r.Params.ExtraParams)
		}

	case schemas.SpeechRequest, schemas.SpeechStreamRequest:
		r := req.SpeechRequest
		if r == nil {
			return
		}
		d.Speech = r.Params
		d.SpeechInput = r.Input

	case schemas.TranscriptionRequest, schemas.TranscriptionStreamRequest:
		r := req.TranscriptionRequest
		if r == nil {
			return
		}
		d.Transcription = r.Params
	}
}

// toMessageSummaries adapts the Responses-shaped summaries onto the unified type.
// Collapsing ResponsesMessageSummary into MessageSummary needs a matching change
// in the enterprise Datadog converter, so the adapter stays until then.
func toMessageSummaries(in []ResponsesMessageSummary) []schemas.MessageSummary {
	if len(in) == 0 {
		return nil
	}
	out := make([]schemas.MessageSummary, 0, len(in))
	for _, m := range in {
		out = append(out, schemas.MessageSummary{
			Role:        m.Role,
			Content:     m.Content,
			Attachments: m.Attachments,
			Reasoning:   m.Reasoning,
			ToolCalls:   m.ToolCalls,
			ToolCallID:  m.ToolCallID,
		})
	}
	return out
}

func buildResponseSide(d *schemas.LLMSpanData, resp *schemas.BifrostResponse, opts SpanBuildOptions) {
	d.Usage = resp.NormalizedUsage()

	ef := resp.GetExtraFields()
	if ef != nil {
		if opts.WantRawPayloads {
			d.RawRequest = schemas.EncodeRawPayload(ef.RawRequest)
			d.RawResponse = schemas.EncodeRawPayload(ef.RawResponse)
		}
		d.ResponseModel = ef.ResolvedModelUsed
		if ef.ResolvedModelUsed != "" && ef.OriginalModelRequested != "" &&
			ef.ResolvedModelUsed != ef.OriginalModelRequested {
			d.Alias = ef.OriginalModelRequested
		}
	}

	if r := resp.ChatResponse; r != nil {
		d.ResponseID = r.ID
		d.ResponseModel = r.Model
		d.Object = r.Object
		d.SystemFingerprint = r.SystemFingerprint
		d.Created = int64(r.Created)
		if r.ServiceTier != nil {
			d.ServiceTier = string(*r.ServiceTier)
		}
		if opts.WantContent {
			d.OutputMessages = schemas.ExtractChatResponseMessages(r, opts.Attachments)
		}
		for _, choice := range r.Choices {
			if choice.FinishReason != nil {
				d.FinishReasons = append(d.FinishReasons, *choice.FinishReason)
			}
		}
		return
	}

	if r := resp.ResponsesResponse; r != nil {
		if r.ID != nil {
			d.ResponseID = *r.ID
		}
		d.ResponseModel = r.Model
		if r.ServiceTier != nil {
			d.ServiceTier = string(*r.ServiceTier)
		}
		if opts.WantContent {
			d.OutputMessages = toMessageSummaries(extractResponsesOutputMessages(r))
		}
		d.ResponsesEcho = responsesEchoFromResponse(r)
		if r.Reasoning != nil && r.Reasoning.Summary != nil {
			d.ReasoningText = *r.Reasoning.Summary
		}
		// The Responses API carries one top-level stop_reason, not per-choice
		// finish reasons. Without this a refusal is invisible to connectors.
		if r.StopReason != nil && *r.StopReason != "" {
			d.FinishReasons = []string{*r.StopReason}
		}
		return
	}

	if r := resp.TextCompletionResponse; r != nil {
		d.ResponseID = r.ID
		d.ResponseModel = r.Model
		d.Object = r.Object
		d.SystemFingerprint = r.SystemFingerprint
		for _, choice := range r.Choices {
			if opts.WantContent && choice.TextCompletionResponseChoice != nil && choice.TextCompletionResponseChoice.Text != nil {
				d.OutputMessages = append(d.OutputMessages, schemas.MessageSummary{Content: *choice.TextCompletionResponseChoice.Text})
			}
			if choice.FinishReason != nil {
				d.FinishReasons = append(d.FinishReasons, *choice.FinishReason)
			}
		}
		return
	}

	// Embedding and speech carry usage only, already folded in above.
	if r := resp.TranscriptionResponse; r != nil && opts.WantContent {
		d.OutputMessages = []schemas.MessageSummary{{Content: r.Text}}
	}
}

// responsesEchoFromResponse projects the params the Responses API echoes on its
// response back onto the params type, so the record has one shape for both sides.
func responsesEchoFromResponse(r *schemas.BifrostResponsesResponse) *schemas.ResponsesParameters {
	return &schemas.ResponsesParameters{
		Include:            r.Include,
		MaxOutputTokens:    r.MaxOutputTokens,
		MaxToolCalls:       r.MaxToolCalls,
		Metadata:           r.Metadata,
		PreviousResponseID: r.PreviousResponseID,
		PromptCacheKey:     r.PromptCacheKey,
		Reasoning:          r.Reasoning,
		SafetyIdentifier:   r.SafetyIdentifier,
		Store:              r.Store,
		Temperature:        r.Temperature,
		Text:               r.Text,
		TopLogProbs:        r.TopLogProbs,
		TopP:               r.TopP,
		ToolChoice:         r.ToolChoice,
		Tools:              r.Tools,
		Truncation:         r.Truncation,
	}
}

// mergeExtraParamsJSON is the Responses variant: it marshals each value rather
// than using formatTraceValue's reflect-based rendering.
func mergeExtraParamsJSON(dst map[string]any, extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]any, len(extra))
	}
	for k, v := range extra {
		if data, err := schemas.MarshalString(v); err == nil {
			dst[k] = data
		}
	}
	return dst
}

func buildErrorSide(d *schemas.LLMSpanData, bifrostErr *schemas.BifrostError) {
	if bifrostErr.Error == nil {
		return
	}
	d.Error = &schemas.SpanError{
		Detail:     bifrostErr.Error,
		StatusCode: schemas.Ptr(bifrostErr.EffectiveHTTPStatus()),
	}
	// Billed usage is what the provider charged for a failed or cancelled turn;
	// without it every span-based consumer records zero tokens.
	if d.Usage == nil && bifrostErr.ExtraFields.BilledUsage != nil {
		d.Usage = bifrostErr.ExtraFields.BilledUsage
	}
}

// mergeExtraParams folds provider-specific params into Extra, which is the only
// place free-form values belong on the record.
func mergeExtraParams(dst map[string]any, extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]any, len(extra))
	}
	for k, v := range extra {
		dst[k] = formatTraceValue(v)
	}
	return dst
}
