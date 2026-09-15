package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

func requestCostsFixture(amount *float64) *schemas.RequestCosts {
	return &schemas.RequestCosts{
		Version:    1,
		Currency:   "USD",
		IsComplete: amount != nil,
		Requests: []schemas.RequestCost{{
			RequestID:  "cost-request",
			Provider:   "openai",
			Model:      "gpt-4o-mini",
			AmountUSD:  amount,
			IsComplete: amount != nil,
		}},
	}
}

func requireRequestCosts(t *testing.T, body []byte, want *schemas.RequestCosts) {
	t.Helper()
	field := gjson.GetBytes(body, "extra_fields.request_costs")
	if want == nil {
		require.False(t, field.Exists(), "absent accounting must not become a free receipt: %s", body)
		return
	}
	require.True(t, field.Exists(), "missing cost receipt: %s", body)
	var got schemas.RequestCosts
	require.NoError(t, json.Unmarshal([]byte(field.Raw), &got))
	require.Equal(t, want, &got)
	for index, request := range field.Get("requests").Array() {
		amount, present := request.Map()["amount_usd"]
		require.True(t, present, "unknown costs must explicitly serialize as null")
		if want.Requests[index].AmountUSD == nil {
			require.Equal(t, "null", amount.Raw)
		}
	}
}

func TestRequestCostsJSONResponse(t *testing.T) {
	for _, tc := range []struct {
		name    string
		receipt *schemas.RequestCosts
	}{
		{name: "priced", receipt: requestCostsFixture(schemas.Ptr(0.000123))},
		{name: "free", receipt: requestCostsFixture(schemas.Ptr(0.0))},
		{name: "unknown", receipt: requestCostsFixture(nil)},
		{name: "accounting unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			SendJSON(ctx, &schemas.BifrostChatResponse{
				ID: "chatcmpl-cost",
				ExtraFields: schemas.BifrostResponseExtraFields{
					RequestCosts: tc.receipt,
				},
			})
			require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
			require.Equal(t, "application/json", string(ctx.Response.Header.ContentType()))
			requireRequestCosts(t, ctx.Response.Body(), tc.receipt)
		})
	}
}

func TestRequestCostsStreamingFinalChunk(t *testing.T) {
	receipt := requestCostsFixture(schemas.Ptr(0.000123))
	chunks := make(chan *schemas.BifrostStreamChunk, 3)
	chunks <- &schemas.BifrostStreamChunk{BifrostChatResponse: &schemas.BifrostChatResponse{
		ID: "chatcmpl-cost", Choices: []schemas.BifrostResponseChoice{{Index: 0}},
	}}
	chunks <- &schemas.BifrostStreamChunk{BifrostChatResponse: &schemas.BifrostChatResponse{
		ID: "chatcmpl-cost", Choices: []schemas.BifrostResponseChoice{{Index: 0, FinishReason: schemas.Ptr("stop")}},
	}}
	// The usage-only chunk follows finish_reason; the receipt must survive this
	// final frame and must not be moved to an earlier text/finish frame.
	chunks <- &schemas.BifrostStreamChunk{BifrostChatResponse: &schemas.BifrostChatResponse{
		ID: "chatcmpl-cost", Choices: []schemas.BifrostResponseChoice{},
		Usage:       &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
		ExtraFields: schemas.BifrostResponseExtraFields{RequestCosts: receipt},
	}}
	close(chunks)

	ctx := streamRequestCosts(t, chunks, nil)
	require.Equal(t, "text/event-stream", string(ctx.Response.Header.ContentType()))
	body := string(ctx.Response.Body())
	frames := requestCostsSSEData(body)
	require.Len(t, frames, 4, body)
	requireRequestCosts(t, []byte(frames[0]), nil)
	requireRequestCosts(t, []byte(frames[1]), nil)
	requireRequestCosts(t, []byte(frames[2]), receipt)
	require.Equal(t, int64(12), gjson.Get(frames[2], "usage.total_tokens").Int())
	require.Equal(t, "[DONE]", frames[3])
}

func TestRequestCostsStreamingSetupError(t *testing.T) {
	receipt := requestCostsFixture(nil)
	bifrostErr := &schemas.BifrostError{
		StatusCode:  schemas.Ptr(fasthttp.StatusInternalServerError),
		Error:       &schemas.ErrorField{Message: "sql: database unavailable"},
		ExtraFields: schemas.BifrostErrorExtraFields{RequestCosts: receipt},
	}
	ctx := streamRequestCosts(t, nil, bifrostErr)
	require.Equal(t, fasthttp.StatusInternalServerError, ctx.Response.StatusCode())
	require.Equal(t, "application/json", string(ctx.Response.Header.ContentType()))
	requireRequestCosts(t, ctx.Response.Body(), receipt)
	require.Equal(t, lib.ClientSafeInternalErrorMessage, gjson.GetBytes(ctx.Response.Body(), "error.message").String())
	require.Equal(t, "sql: database unavailable", bifrostErr.Error.Message)
}

func TestRequestCostsStreamingTerminalError(t *testing.T) {
	receipt := requestCostsFixture(nil)
	chunks := make(chan *schemas.BifrostStreamChunk, 2)
	chunks <- &schemas.BifrostStreamChunk{BifrostChatResponse: &schemas.BifrostChatResponse{ID: "chatcmpl-cost"}}
	chunks <- &schemas.BifrostStreamChunk{BifrostError: &schemas.BifrostError{
		Error:       &schemas.ErrorField{Message: "upstream stream interrupted"},
		ExtraFields: schemas.BifrostErrorExtraFields{RequestCosts: receipt},
	}}
	close(chunks)

	ctx := streamRequestCosts(t, chunks, nil)
	body := string(ctx.Response.Body())
	frames := requestCostsSSEData(body)
	require.Len(t, frames, 2, body)
	requireRequestCosts(t, []byte(frames[0]), nil)
	requireRequestCosts(t, []byte(frames[1]), receipt)
	require.NotContains(t, body, "[DONE]")
}

func streamRequestCosts(t *testing.T, chunks chan *schemas.BifrostStreamChunk, setupErr *schemas.BifrostError) *fasthttp.RequestCtx {
	t.Helper()
	ctx := &fasthttp.RequestCtx{}
	bifrostCtx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	t.Cleanup(cancel)
	handler := &CompletionHandler{config: &lib.Config{}}
	handler.handleStreamingResponse(ctx, bifrostCtx, schemas.ChatCompletionStreamRequest, func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		return chunks, setupErr
	}, cancel)
	t.Cleanup(ctx.Response.Reset)
	return ctx
}

func requestCostsSSEData(body string) []string {
	var data []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data: ") {
			data = append(data, strings.TrimPrefix(line, "data: "))
		}
	}
	return data
}
