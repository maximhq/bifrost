package gemini

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRawBodyServiceTier pins that a key or alias service tier reaches a raw-body request (the
// /genai route sends the client's bytes as-is when Gemini is named explicitly): the attempt's tier
// is written as service_tier, default removes the field since an omitted tier is Standard, the
// camelCase spelling is replaced, and a request with no tier keeps its bytes.
func TestRawBodyServiceTier(t *testing.T) {
	rawCtx := func() *schemas.BifrostContext {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
		return ctx
	}
	priority, standard := schemas.BifrostServiceTierPriority, schemas.BifrostServiceTierDefault
	const plain = `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`

	t.Run("helper", func(t *testing.T) {
		cases := []struct {
			name, body string
			tier       *schemas.BifrostServiceTier
			want       string // expected service_tier, "" for absent
		}{
			{"key tier written when the client sent none", plain, &priority, "priority"},
			{"key tier overrides the client tier", `{"service_tier":"flex","contents":[]}`, &priority, "priority"},
			{"default removes the field", `{"service_tier":"flex","contents":[]}`, &standard, ""},
			{"camelCase spelling is replaced", `{"serviceTier":"flex","contents":[]}`, &priority, "priority"},
		}
		for _, tc := range cases {
			out := setRawServiceTier(rawCtx(), []byte(tc.body), tc.tier)
			assert.Equal(t, tc.want, providerUtils.GetJSONField(out, "service_tier").String(), tc.name+": "+string(out))
			assert.False(t, providerUtils.JSONFieldExists(out, "serviceTier"), tc.name+": serviceTier must not remain: "+string(out))
		}
		for _, body := range []string{`{"serviceTier":"flex","contents":[]}`, plain} {
			assert.Equal(t, body, string(setRawServiceTier(rawCtx(), []byte(body), nil)), "no tier must keep the bytes")
		}
		same := `{"service_tier":"priority","contents":[]}`
		assert.Equal(t, same, string(setRawServiceTier(rawCtx(), []byte(same), &priority)), "a matching client tier must keep the bytes")
		typed := `{"contents":[]}`
		assert.Equal(t, typed, string(setRawServiceTier(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), []byte(typed), &priority)), "the typed path is left to the converter")
	})

	t.Run("every_chat_and_responses_route", func(t *testing.T) {
		var mu sync.Mutex
		var sent []string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			mu.Lock()
			sent = append(sent, string(raw))
			mu.Unlock()
			reply := `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2},"modelVersion":"gemini-3.8-flash"}`
			if strings.Contains(r.URL.Path, ":streamGenerateContent") {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: "+reply+"\n\n")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, reply)
		}))
		defer server.Close()

		provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, testNoopLogger{})
		key := schemas.Key{Value: schemas.SecretVar{Val: "test-key"}, Models: []string{"*"}}
		postHook := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
			return result, err
		}
		chatReq := func() *schemas.BifrostChatRequest {
			return &schemas.BifrostChatRequest{Provider: schemas.Gemini, Model: "gemini-3.8-flash", RawRequestBody: []byte(plain), Params: &schemas.ChatParameters{ServiceTier: &priority}}
		}
		respReq := func() *schemas.BifrostResponsesRequest {
			return &schemas.BifrostResponsesRequest{Provider: schemas.Gemini, Model: "gemini-3.8-flash", RawRequestBody: []byte(plain), Params: &schemas.ResponsesParameters{ServiceTier: &priority}}
		}
		drain := func(stream chan *schemas.BifrostStreamChunk, err *schemas.BifrostError) {
			require.Nil(t, err)
			for range stream {
			}
		}

		_, err := provider.ChatCompletion(rawCtx(), key, chatReq())
		require.Nil(t, err)
		drain(provider.ChatCompletionStream(rawCtx(), postHook, func(context.Context) {}, key, chatReq()))
		_, err = provider.Responses(rawCtx(), key, respReq())
		require.Nil(t, err)
		drain(provider.ResponsesStream(rawCtx(), postHook, func(context.Context) {}, key, respReq()))

		mu.Lock()
		defer mu.Unlock()
		require.Len(t, sent, 4)
		for i, body := range sent {
			assert.Equal(t, "priority", providerUtils.GetJSONField([]byte(body), "service_tier").String(), "route %d: %s", i, body)
		}
	})
}
