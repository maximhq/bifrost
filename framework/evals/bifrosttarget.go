package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ModelCallRequest is one model call a test run makes to the model under test.
type ModelCallRequest struct {
	Provider string
	Model    string
	Messages []schemas.ChatMessage
	Params   map[string]any // provider model parameters, in wire form
}

// ModelCallResponse is what the model under test answered.
type ModelCallResponse struct {
	Text      string
	ToolCalls json.RawMessage // the tool calls as a JSON list, in the OpenAI shape; nil when there were none
	Usage     *Usage
	Cost      *float64
}

// ModelCaller calls the model under test. NewBifrostModelCaller adapts the gateway
// client to this shape; tests pass a stub.
type ModelCaller func(ctx context.Context, req ModelCallRequest) (ModelCallResponse, error)

// NewBifrostModelCaller returns a ModelCaller that calls models through the gateway,
// the same way the judge does: through the plugin pipeline, tagged as evaluation
// traffic, and pinned to the eval key the runner placed the job on. A call with no eval
// key is refused, because it would land on a production key.
func NewBifrostModelCaller(chat ChatExecutor, responses ResponsesExecutor, opts BifrostJudgeOptions) ModelCaller {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultJudgeTimeout
	}
	return func(ctx context.Context, req ModelCallRequest) (ModelCallResponse, error) {
		if chat == nil {
			return ModelCallResponse{}, errors.New("no model client is configured")
		}
		keyID := KeyIDFromContext(ctx)
		if keyID == "" {
			return ModelCallResponse{}, fmt.Errorf("%w: a test run's model call must run on an eval key", ErrNoEvalKey)
		}
		chatReq := &schemas.BifrostChatRequest{Provider: schemas.ModelProvider(req.Provider), Model: req.Model, Input: req.Messages}
		if len(req.Params) > 0 {
			data, err := json.Marshal(req.Params)
			if err != nil {
				return ModelCallResponse{}, fmt.Errorf("invalid model parameters: %w", err)
			}
			chatReq.Params = &schemas.ChatParameters{}
			if err := json.Unmarshal(data, chatReq.Params); err != nil {
				return ModelCallResponse{}, fmt.Errorf("invalid model parameters: %w", err)
			}
		}

		bifrostCtx, cancel := newEvalRequestContext(ctx, keyID, timeout, opts.PrepareContext)
		defer cancel()

		response, bifrostErr := chat(bifrostCtx, chatReq)
		if bifrostErr != nil && responses != nil && shouldRetryOnResponsesAPI(bifrostErr) {
			responsesReq := chatReq.ToResponsesRequest()
			if responsesReq.Params != nil {
				responsesReq.Params.Temperature = nil
			}
			responsesResponse, responsesErr := responses(bifrostCtx, responsesReq)
			if responsesErr != nil {
				return ModelCallResponse{}, errors.New(responsesErr.GetErrorString())
			}
			response, bifrostErr = responsesResponse.ToBifrostChatResponse(), nil
		}
		if bifrostErr != nil {
			return ModelCallResponse{}, errors.New(bifrostErr.GetErrorString())
		}
		if response == nil {
			return ModelCallResponse{}, errors.New("the model returned no response")
		}
		judged := judgeResponseFromChat(response)
		out := ModelCallResponse{Text: judged.Text, Usage: judged.Usage, Cost: judged.Cost}
		for _, choice := range response.Choices {
			if choice.ChatNonStreamResponseChoice == nil || choice.Message == nil || choice.Message.ChatAssistantMessage == nil {
				continue
			}
			if calls := choice.Message.ChatAssistantMessage.ToolCalls; len(calls) > 0 {
				data, err := providerUtils.MarshalSorted(calls)
				if err != nil {
					return ModelCallResponse{}, fmt.Errorf("the model's tool calls could not be encoded: %w", err)
				}
				out.ToolCalls = data
				break
			}
		}
		return out, nil
	}
}
