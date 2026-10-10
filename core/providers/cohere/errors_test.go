package cohere

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/iotest"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestUnaryRejectionUsesProviderIdleTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusBadRequest)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	p, err := NewCohereProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{
		AllowPrivateNetwork: true, StreamIdleTimeoutInSeconds: 1,
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, override := range []time.Duration{0, 25 * time.Millisecond} {
		t.Run(override.String(), func(t *testing.T) {
			requestBudget := 3 * time.Second
			if override != 0 {
				// Ignoring the 25 ms override would now hit the request deadline before the 1 s provider idle timeout.
				requestBudget = 700 * time.Millisecond
			}
			ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), requestBudget)
			defer cancel()
			ctx.SetValue(schemas.BifrostContextKeyLargeResponseThreshold, int64(16))
			if override != 0 {
				ctx.SetValue(schemas.BifrostContextKeyStreamIdleTimeout, override)
			}
			_, _, _, got := p.completeRequest(ctx, []byte("{}"), server.URL, "")
			if got == nil || got.Error == nil || !errors.Is(got.Error.Error, providerUtils.ErrStreamIdleTimeout) {
				t.Fatalf("error = %v, want the body idle timeout", got)
			}
		})
	}
}

// TestParseCohereError_PopulatesNestedErrorType verifies the upstream exception
// type is surfaced on the nested error object (error.type), not only at the top
// level, so OpenAI-shaped consumers see it.
func TestParseCohereError_PopulatesNestedErrorType(t *testing.T) {
	var resp fasthttp.Response
	resp.SetStatusCode(fasthttp.StatusBadRequest)
	resp.SetBodyString(`{"type":"invalid_request_error","message":"model not found"}`)

	bifrostErr := parseCohereError(&resp)

	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Error)
	require.NotNil(t, bifrostErr.Error.Type, "nested error.type must be populated")
	assert.Equal(t, "invalid_request_error", *bifrostErr.Error.Type)
	require.NotNil(t, bifrostErr.Type, "top-level type must remain populated")
	assert.Equal(t, "invalid_request_error", *bifrostErr.Type)
	assert.Equal(t, "model not found", bifrostErr.Error.Message)
}

func TestParseCohereErrorPreservesStreamTimeout(t *testing.T) {
	var resp fasthttp.Response
	resp.SetStatusCode(http.StatusBadRequest)
	resp.SetBodyStream(iotest.ErrReader(providerUtils.ErrStreamIdleTimeout), -1)
	got := parseCohereError(&resp)
	require.NotNil(t, got.Error)
	assert.ErrorIs(t, got.Error.Error, providerUtils.ErrStreamIdleTimeout)
	assert.Equal(t, http.StatusGatewayTimeout, got.EffectiveHTTPStatus())
	assert.Equal(t, schemas.ErrProviderRequestTimedOut, ToCohereError(got).Message)
}

func TestParseCohereErrorCarriesUpstreamID(t *testing.T) {
	// Cohere returns {"id", "message"} on every v2 error; ToCohereError echoes the id back.
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	resp.SetStatusCode(400)
	resp.SetBody([]byte(`{"id":"1ad6894a-9ed1-48db-9316-0efa1b87a7e2","message":"invalid request: inputs[0] is too long"}`))

	bifrostErr := parseCohereError(resp)
	require.NotNil(t, bifrostErr.EventID)
	assert.Equal(t, "1ad6894a-9ed1-48db-9316-0efa1b87a7e2", *bifrostErr.EventID)
	assert.Equal(t, "1ad6894a-9ed1-48db-9316-0efa1b87a7e2", ToCohereError(bifrostErr).ID)
}
