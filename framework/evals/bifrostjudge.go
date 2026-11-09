package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// JudgeUserAgent tags every model call an evaluator makes. Logs record it as the
// caller, which is how evaluation traffic is told apart from production traffic and
// how online evals know not to evaluate an evaluator's own calls.
const JudgeUserAgent = "bifrost-evals/1"

// defaultJudgeTimeout bounds one judge model call.
const defaultJudgeTimeout = 90 * time.Second

// judgeSchemaName names the structured-output schema sent to the provider.
const judgeSchemaName = "evaluation"

// ChatExecutor mirrors bifrost.Client.ChatCompletionRequest.
type ChatExecutor func(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError)

// ResponsesExecutor mirrors bifrost.Client.ResponsesRequest.
type ResponsesExecutor func(ctx *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError)

// BifrostJudgeOptions tunes the gateway-backed judge.
type BifrostJudgeOptions struct {
	Timeout time.Duration // per model call; zero takes the default
	// PrepareContext lets the server add what its deployment needs to the request
	// context, such as a governance grant. It runs after the tags and the key pin.
	PrepareContext func(ctx context.Context, bifrostCtx *schemas.BifrostContext)
}

// NewBifrostJudge returns a Judge that calls models through the gateway. Calls go
// through the normal plugin pipeline, so they are logged and governed like any other
// request. Each call is pinned to the eval key the runner placed the job on; a call
// with no eval key is refused, because it would land on a production key.
func NewBifrostJudge(chat ChatExecutor, responses ResponsesExecutor, opts BifrostJudgeOptions) Judge {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultJudgeTimeout
	}
	return func(ctx context.Context, req JudgeRequest) (JudgeResponse, error) {
		if chat == nil {
			return JudgeResponse{}, errors.New("no judge model client is configured")
		}
		keyID := KeyIDFromContext(ctx)
		if keyID == "" {
			return JudgeResponse{}, fmt.Errorf("%w: an evaluation model call must run on an eval key", ErrNoEvalKey)
		}
		chatReq, err := buildJudgeChatRequest(req)
		if err != nil {
			return JudgeResponse{}, err
		}

		bifrostCtx, cancel := newEvalRequestContext(ctx, keyID, timeout, opts.PrepareContext)
		defer cancel()

		response, bifrostErr := chat(bifrostCtx, chatReq)
		if bifrostErr != nil && responses != nil && shouldRetryOnResponsesAPI(bifrostErr) {
			responsesReq := chatReq.ToResponsesRequest()
			if responsesReq.Params != nil {
				// Reasoning models on the Responses API reject a non-default temperature.
				responsesReq.Params.Temperature = nil
			}
			responsesResponse, responsesErr := responses(bifrostCtx, responsesReq)
			if responsesErr != nil {
				// Chat already said it cannot serve this model, so this error is the useful one.
				return JudgeResponse{}, errors.New(responsesErr.GetErrorString())
			}
			response, bifrostErr = responsesResponse.ToBifrostChatResponse(), nil
		}
		if bifrostErr != nil {
			return JudgeResponse{}, errors.New(bifrostErr.GetErrorString())
		}
		if response == nil {
			return JudgeResponse{}, errors.New("the judge model returned no response")
		}
		return judgeResponseFromChat(response), nil
	}
}

// newEvalRequestContext builds the request context for a model call an evaluator makes:
// tagged as evaluation traffic, pinned to the eval key, and bounded by timeout.
func newEvalRequestContext(ctx context.Context, keyID string, timeout time.Duration, prepare func(context.Context, *schemas.BifrostContext)) (*schemas.BifrostContext, context.CancelFunc) {
	bifrostCtx, cancel := schemas.NewBifrostContextWithTimeout(ctx, timeout)
	headers := map[string]string{"user-agent": JudgeUserAgent}
	bifrostCtx.SetValue(schemas.BifrostContextKeyRequestHeaders, headers)
	bifrostCtx.SetValue(schemas.BifrostContextKeyGuardrailHeaders, maps.Clone(headers))
	bifrostCtx.SetValue(schemas.BifrostContextKeyAPIKeyID, keyID)
	if prepare != nil {
		prepare(ctx, bifrostCtx)
	}
	return bifrostCtx, cancel
}

// encodeJudgeParams encodes an evaluator's model parameters once, for every call it
// will make. Nil when there are none, or when they cannot be encoded: the call then
// encodes them itself and reports the error.
func encodeJudgeParams(params map[string]any) []byte {
	if len(params) == 0 {
		return nil
	}
	data, err := json.Marshal(params)
	if err != nil {
		return nil
	}
	return data
}

// buildJudgeChatRequest turns a judge request into a chat completion request.
func buildJudgeChatRequest(req JudgeRequest) (*schemas.BifrostChatRequest, error) {
	chatReq := &schemas.BifrostChatRequest{
		Provider: schemas.ModelProvider(req.Provider),
		Model:    req.Model,
		Input:    make([]schemas.ChatMessage, 0, 2),
	}
	// The built-in judges send one user message and no system message; an empty system
	// message is rejected by some providers, so it is left out.
	if req.System != "" {
		chatReq.Input = append(chatReq.Input, schemas.ChatMessage{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: &req.System}})
	}
	chatReq.Input = append(chatReq.Input, schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &req.User}})
	if len(req.Params) > 0 {
		// The user's model parameters are in wire form, so they decode straight into the
		// typed parameters, unknown ones included. Each call decodes its own copy: a
		// provider may change a request's parameters, and calls must not share them.
		data := req.ParamsJSON
		if len(data) == 0 {
			var err error
			if data, err = json.Marshal(req.Params); err != nil {
				return nil, fmt.Errorf("invalid model parameters: %w", err)
			}
		}
		params := &schemas.ChatParameters{}
		if err := json.Unmarshal(data, params); err != nil {
			return nil, fmt.Errorf("invalid model parameters: %w", err)
		}
		chatReq.Params = params
	}
	if req.Schema != nil {
		if chatReq.Params == nil {
			chatReq.Params = &schemas.ChatParameters{}
		}
		var format any = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   judgeSchemaName,
				"strict": true,
				"schema": req.Schema,
			},
		}
		chatReq.Params.ResponseFormat = &format
	}
	return chatReq, nil
}

// judgeResponseFromChat extracts the text, truncation flag, usage and cost of a reply.
func judgeResponseFromChat(response *schemas.BifrostChatResponse) JudgeResponse {
	var out JudgeResponse
	for _, choice := range response.Choices {
		if choice.FinishReason != nil && *choice.FinishReason == string(schemas.BifrostFinishReasonLength) {
			out.Truncated = true
		}
		if out.Text != "" || choice.ChatNonStreamResponseChoice == nil || choice.Message == nil || choice.Message.Content == nil {
			continue
		}
		content := choice.Message.Content
		if content.ContentStr != nil {
			out.Text = *content.ContentStr
			continue
		}
		var parts []string
		for _, block := range content.ContentBlocks {
			if block.Text != nil && *block.Text != "" {
				parts = append(parts, *block.Text)
			}
		}
		out.Text = strings.Join(parts, "\n")
	}
	if usage := response.Usage; usage != nil {
		// Provider-reported counts are untrusted: a negative one must not subtract.
		out.Usage = &Usage{
			PromptTokens:     max(usage.PromptTokens, 0),
			CompletionTokens: max(usage.CompletionTokens, 0),
			TotalTokens:      max(usage.TotalTokens, 0),
		}
		if usage.Cost != nil && usage.Cost.TotalCost > 0 {
			out.Cost = new(usage.Cost.TotalCost)
		}
	}
	return out
}

// shouldRetryOnResponsesAPI reports whether a failed chat completion should be retried
// on the Responses API: the provider said to switch endpoints, or it rejected a
// temperature that reasoning models require to be the default. This is the heuristic
// the routing classifier uses for its judge model.
func shouldRetryOnResponsesAPI(bifrostErr *schemas.BifrostError) bool {
	message := strings.ToLower(bifrostErr.GetErrorString())
	switch {
	case strings.Contains(message, "/v1/responses") &&
		(strings.Contains(message, "/v1/chat/completions") || strings.Contains(message, "function tool")):
		return true
	case strings.Contains(message, "temperature") &&
		(strings.Contains(message, "does not support") || strings.Contains(message, "only the default")):
		return true
	}
	return false
}
