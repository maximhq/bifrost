package gemini

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPassthroughFollowsFileDownloadRedirect covers Google's file-download flow:
// /v1beta/files/{id}:download answers 302 to /download/v1beta, and only Bifrost
// holds the API key that target requires. Everything else must keep receiving
// redirects untouched — notably resumable uploads, whose 308 "Resume Incomplete"
// carries no Location and would break a redirect-following client.
func TestPassthroughFollowsFileDownloadRedirect(t *testing.T) {
	const videoBytes = "fake-mp4-bytes"

	var sawAPIKeyOnRedirect string
	mux := http.NewServeMux()
	// The versioned surface redirects, exactly as generativelanguage does.
	mux.HandleFunc("/v1beta/files/abc:download", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/download/v1beta/files/abc:download?alt=media", http.StatusFound)
	})
	// The redirect target requires the key.
	mux.HandleFunc("/download/v1beta/files/abc:download", func(w http.ResponseWriter, r *http.Request) {
		sawAPIKeyOnRedirect = r.Header.Get("x-goog-api-key")
		if sawAPIKeyOnRedirect == "" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":403,"message":"Method doesn't allow unregistered callers"}}`))
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte(videoBytes))
	})
	// A resumable upload chunk: 308 with no Location header.
	mux.HandleFunc("/upload/v1beta/files", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Goog-Upload-Status", "active")
		w.WriteHeader(http.StatusPermanentRedirect)
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	provider := NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: ts.URL + "/v1beta"},
	}, testNoopLogger{})
	key := schemas.Key{Value: *schemas.NewSecretVar("dummy-key")}

	t.Run("download follows the redirect and returns the bytes", func(t *testing.T) {
		sawAPIKeyOnRedirect = ""
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		resp, bifrostErr := provider.Passthrough(ctx, key, &schemas.BifrostPassthroughRequest{
			Provider: schemas.Gemini,
			Method:   http.MethodGet,
			Path:     "/files/abc:download",
			RawQuery: "alt=media",
		})

		require.Nil(t, bifrostErr)
		require.NotNil(t, resp)
		assert.Equal(t, http.StatusOK, resp.StatusCode, "client should get the bytes, not the 302")
		assert.Equal(t, videoBytes, string(resp.Body))
		assert.Equal(t, "dummy-key", sawAPIKeyOnRedirect, "api key must survive onto the redirect hop")
	})

	t.Run("resumable upload 308 is forwarded, not followed", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		resp, bifrostErr := provider.Passthrough(ctx, key, &schemas.BifrostPassthroughRequest{
			Provider: schemas.Gemini,
			Method:   http.MethodPost,
			Path:     "/upload/v1beta/files",
			RawQuery: "upload_id=xyz",
		})

		require.Nil(t, bifrostErr, "a Location-less 308 must not surface as a redirect error")
		require.NotNil(t, resp)
		assert.Equal(t, http.StatusPermanentRedirect, resp.StatusCode)
	})

	t.Run("ordinary calls are unaffected", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		resp, bifrostErr := provider.Passthrough(ctx, key, &schemas.BifrostPassthroughRequest{
			Provider: schemas.Gemini,
			Method:   http.MethodGet,
			Path:     "/files/abc",
		})

		require.Nil(t, bifrostErr)
		require.NotNil(t, resp)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "unregistered path, but no redirect handling either")
	})
}

// TestPassthroughInteractionsReportsRun pins the Gemini route for stored interactions: a poll of a
// finished run reports the run's usage on ProviderJob, to be settled once, and never on
// PassthroughUsage, which governance and the request row would bill on every read. The stream route
// does the same from its interaction.completed event.
func TestPassthroughInteractionsReportsRun(t *testing.T) {
	const completed = `{"id":"int_gem","agent":"deep-research-preview-04-2026","status":"completed","usage":{"total_input_tokens":1000,"total_output_tokens":50,"total_tokens":1050}}`
	mux := http.NewServeMux()
	mux.HandleFunc("/v1beta/interactions/int_gem", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("stream") == "true" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(`data: {"event_type":"interaction.completed","interaction":` + completed + "}\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(completed))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	provider := NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: ts.URL + "/v1beta"},
	}, testNoopLogger{})
	key := schemas.Key{Value: *schemas.NewSecretVar("dummy-key")}
	req := &schemas.BifrostPassthroughRequest{Provider: schemas.Gemini, Method: http.MethodGet, Path: "/interactions/int_gem", APIVersion: "v1beta"}

	check := func(label string, resp *schemas.BifrostPassthroughResponse) {
		t.Helper()
		require.NotNil(t, resp, label)
		assert.Nil(t, resp.PassthroughUsage, "%s: a read of a stored run bills nothing on the request", label)
		require.NotNil(t, resp.ProviderJob, label)
		assert.Equal(t, "int_gem", resp.ProviderJob.ID, label)
		assert.True(t, resp.ProviderJob.Terminal, label)
		assert.False(t, resp.ProviderJob.Created, "%s: a poll does not create the run", label)
		assert.Equal(t, "/interactions/int_gem", resp.ProviderJob.PollPath, label)
		require.NotNil(t, resp.ProviderJob.Usage, label)
		assert.Equal(t, 1050, resp.ProviderJob.Usage.LLMUsage.TotalTokens, label)
		assert.Equal(t, "deep-research-preview-04-2026", resp.ServedModel, label)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.Passthrough(ctx, key, req)
	require.Nil(t, bifrostErr)
	check("unary", resp)

	streamReq := *req
	streamReq.RawQuery = "stream=true"
	noHooks := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}
	stream, bifrostErr := provider.PassthroughStream(ctx, noHooks, nil, key, &streamReq)
	require.Nil(t, bifrostErr)
	var final *schemas.BifrostPassthroughResponse
	for chunk := range stream {
		if chunk.BifrostPassthroughResponse != nil && len(chunk.BifrostPassthroughResponse.Body) == 0 {
			final = chunk.BifrostPassthroughResponse
		}
	}
	check("stream", final)
}
