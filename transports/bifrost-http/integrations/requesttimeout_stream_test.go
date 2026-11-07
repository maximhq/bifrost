package integrations

import (
	"context"
	"io"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Core stops waiting for a consumer that stays busy past its send bound, so a
// timed-out stream can close without its timeout chunk. The router must still
// report the timeout instead of sending [DONE].
func TestHandleStreamingReportsFiredRequestTimeoutWithoutChunk(t *testing.T) {
	openAIRoutes := CreateOpenAIRouteConfigs("/openai", &mockHandlerStore{})
	for path, wantEvent := range map[string]bool{"/openai/v1/chat/completions": false, "/openai/v1/responses": true} {
		t.Run(path, func(t *testing.T) {
			var route RouteConfig
			for _, r := range openAIRoutes {
				if r.Method == "POST" && r.Path == path {
					route = r
				}
			}
			require.NotEmpty(t, route.Path, "route %s not found", path)

			stream := make(chan *schemas.BifrostStreamChunk)
			close(stream)

			parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx := schemas.NewBifrostContext(parent, schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyRequestTimeoutFired, true)
			router := NewGenericRouter(nil, &mockHandlerStore{}, nil, nil, nil, bifrost.NewNoOpLogger())
			httpCtx := &fasthttp.RequestCtx{}
			router.handleStreaming(httpCtx, ctx, route, "", stream, cancel)
			body, err := io.ReadAll(httpCtx.Response.BodyStream())
			require.NoError(t, err)
			wire := string(body)
			require.Contains(t, wire, "x-bf-request-timeout", "wire stream: %q", wire)
			require.NotContains(t, wire, "[DONE]")
			if wantEvent {
				require.Contains(t, wire, "event: error\n", "wire stream: %q", wire)
			}
		})
	}
}
