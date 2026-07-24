package governance

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

const estimatedContextBytesPerToken int64 = 4

// computeContextLength returns the request input context length in tokens.
// It uses Bifrost's provider-native CountTokensRequest path when available and
// falls back to a local byte-size estimate for providers without support.
func (p *GovernancePlugin) computeContextLength(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (int64, bool) {
	countReq, estimateBytes, ok := buildContextLengthCountRequest(req)
	if !ok {
		if p.logger != nil {
			p.logger.Debug("[Governance] Context length skipped: unsupported request type")
		}
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelInfo, "Context length skipped: no supported input detected")
		return 0, false
	}

	if p.countTokensRequestExecutor != nil && countReq != nil && countReq.Provider != "" && countReq.Model != "" {
		countCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		defer countCtx.Cancel()
		countCtx.SetValue(schemas.BifrostContextKeySkipPluginPipeline, true)

		resp, err := p.countTokensRequestExecutor(countCtx, countReq)
		if err == nil && resp != nil {
			inputTokens := resp.InputTokens
			if inputTokens == 0 && resp.TotalTokens != nil {
				inputTokens = *resp.TotalTokens
			}
			ctx.AppendRoutingEngineLog(
				schemas.RoutingEngineRoutingRule,
				schemas.LogLevelInfo,
				fmt.Sprintf("Context length: %d tokens (provider count)", inputTokens),
			)
			return int64(inputTokens), true
		}
		if p.logger != nil && err != nil {
			p.logger.Debug("[Governance] Context length provider count failed, falling back to estimate: %v", err)
		}
	}

	estimate := estimateContextTokens(estimateBytes)
	ctx.AppendRoutingEngineLog(
		schemas.RoutingEngineRoutingRule,
		schemas.LogLevelInfo,
		fmt.Sprintf("Context length: %d tokens (estimated from %d bytes)", estimate, estimateBytes),
	)
	return estimate, true
}

func buildContextLengthCountRequest(req *schemas.BifrostRequest) (*schemas.BifrostResponsesRequest, int64, bool) {
	if req == nil {
		return nil, 0, false
	}

	var countReq *schemas.BifrostResponsesRequest
	var rawRequestBody []byte

	switch {
	case req.ResponsesRequest != nil:
		copyReq := *req.ResponsesRequest
		copyReq.Fallbacks = nil
		countReq = &copyReq
		rawRequestBody = copyReq.RawRequestBody
	case req.ChatRequest != nil:
		countReq = req.ChatRequest.ToResponsesRequest()
		countReq.Fallbacks = nil
		rawRequestBody = req.ChatRequest.RawRequestBody
	case req.TextCompletionRequest != nil:
		chatReq := req.TextCompletionRequest.ToBifrostChatRequest()
		if chatReq != nil {
			countReq = chatReq.ToResponsesRequest()
			countReq.Fallbacks = nil
		}
		rawRequestBody = req.TextCompletionRequest.RawRequestBody
	case req.CountTokensRequest != nil:
		copyReq := *req.CountTokensRequest
		copyReq.Fallbacks = nil
		countReq = &copyReq
		rawRequestBody = copyReq.RawRequestBody
	default:
		return nil, 0, false
	}

	if countReq != nil && countReq.Input != nil {
		provider, model, _ := req.GetRequestFields()
		countReq.Provider = provider
		countReq.Model = model
		estimateBytes, ok := serializedRequestBytes(countReq)
		return countReq, estimateBytes, ok
	}

	if len(rawRequestBody) > 0 {
		return nil, int64(len(rawRequestBody)), true
	}

	return nil, 0, false
}

func serializedRequestBytes(req *schemas.BifrostResponsesRequest) (int64, bool) {
	body, err := schemas.Marshal(req)
	if err != nil {
		return 0, false
	}
	return int64(len(body)), true
}

func estimateContextTokens(byteLen int64) int64 {
	if byteLen <= 0 {
		return 0
	}
	return (byteLen + estimatedContextBytesPerToken - 1) / estimatedContextBytesPerToken
}
