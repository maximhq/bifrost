package openai

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestListModelsByKeyResponseShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        string
		wantIDs     []string
		wantOwnedBy string
		wantCreated int64
		wantContext int
	}{
		{
			name:        "OpenAI envelope",
			body:        `{"object":"list","data":[{"id":"gpt-5","owned_by":"openai","context_window":128000}]}`,
			wantIDs:     []string{"test/gpt-5"},
			wantOwnedBy: "openai",
			wantContext: 128000,
		},
		{
			name:        "Together array",
			body:        `[{"id":"zai-org/GLM-5.2","organization":"Z.ai","context_length":131072}]`,
			wantIDs:     []string{"test/zai-org/GLM-5.2"},
			wantOwnedBy: "Z.ai",
			wantContext: 131072,
		},
		{
			name:    "models key instead of data",
			body:    `{"models":[{"id":"gemini-3-pro"}]}`,
			wantIDs: []string{"test/gemini-3-pro"},
		},
		{
			name:    "bare array of ids",
			body:    `["gpt-5","gpt-5-mini"]`,
			wantIDs: []string{"test/gpt-5", "test/gpt-5-mini"},
		},
		{
			name:    "ids inside data",
			body:    `{"object":"list","data":["glm-4.6","glm-4.5-air"]}`,
			wantIDs: []string{"test/glm-4.6", "test/glm-4.5-air"},
		},
		{
			name:        "quoted scalars",
			body:        `{"object":"list","data":[{"id":"kimi-k2","owned_by":"moonshot","created":"1760000000","context_window":"256000"}]}`,
			wantIDs:     []string{"test/kimi-k2"},
			wantOwnedBy: "moonshot",
			wantCreated: 1760000000,
			wantContext: 256000,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			response, bifrostErr := ListModelsByKey(
				schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
				&fasthttp.Client{},
				server.URL,
				schemas.Key{Models: schemas.WhiteList{"*"}},
				false,
				nil,
				schemas.ModelProvider("test"),
				false,
				false,
			)

			require.Nil(t, bifrostErr)
			require.Len(t, response.Data, len(test.wantIDs))

			gotIDs := make([]string, 0, len(response.Data))
			for _, model := range response.Data {
				gotIDs = append(gotIDs, model.ID)
			}
			require.Equal(t, test.wantIDs, gotIDs)

			if test.wantOwnedBy != "" {
				require.Equal(t, schemas.Ptr(test.wantOwnedBy), response.Data[0].OwnedBy)
			}
			if test.wantCreated != 0 {
				require.Equal(t, schemas.Ptr(test.wantCreated), response.Data[0].Created)
			}
			if test.wantContext != 0 {
				require.Equal(t, schemas.Ptr(test.wantContext), response.Data[0].ContextLength)
			}
		})
	}
}

// Compatible shapes are tolerated, but a body that is not a model list at all still
// has to fail: answering with an empty catalog would hide the provider from callers.
func TestListModelsByKeyUnrecognisedResponseStillFails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{
			name: "truncated JSON",
			body: `{"object":"list","data":[{"id":`,
		},
		{
			name: "data keyed by model id",
			body: `{"object":"list","data":{"gpt-5":{"id":"gpt-5"}}}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			response, bifrostErr := ListModelsByKey(
				schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
				&fasthttp.Client{},
				server.URL,
				schemas.Key{Models: schemas.WhiteList{"*"}},
				false,
				nil,
				schemas.ModelProvider("test"),
				false,
				false,
			)

			require.Nil(t, response)
			require.NotNil(t, bifrostErr)
			require.NotNil(t, bifrostErr.Error)
			require.Equal(t, schemas.ErrProviderResponseUnmarshal, bifrostErr.Error.Message)
		})
	}
}

// Providers are free to answer with a compressed body: fasthttp neither sends an
// Accept-Encoding nor decompresses on its own, so the list models path has to decode
// explicitly. Undecoded bytes surface as `invalid char '\x1f\x8b'` unmarshal errors.
func TestListModelsByKeyDecodesCompressedResponses(t *testing.T) {
	t.Parallel()

	const modelsBody = `{"object":"list","data":[{"id":"gpt-5","owned_by":"openai"},{"id":"gpt-5-mini"}]}`

	tests := []struct {
		name     string
		encoding string
		encode   func(*testing.T, []byte) []byte
	}{
		{
			name:     "gzip",
			encoding: "gzip",
			encode: func(t *testing.T, payload []byte) []byte {
				t.Helper()
				var buf bytes.Buffer
				writer := gzip.NewWriter(&buf)
				_, err := writer.Write(payload)
				require.NoError(t, err)
				require.NoError(t, writer.Close())
				return buf.Bytes()
			},
		},
		{
			name:     "deflate",
			encoding: "deflate",
			encode: func(t *testing.T, payload []byte) []byte {
				t.Helper()
				var buf bytes.Buffer
				writer := zlib.NewWriter(&buf)
				_, err := writer.Write(payload)
				require.NoError(t, err)
				require.NoError(t, writer.Close())
				return buf.Bytes()
			},
		},
		{
			name:     "brotli",
			encoding: "br",
			encode: func(t *testing.T, payload []byte) []byte {
				t.Helper()
				var buf bytes.Buffer
				writer := brotli.NewWriter(&buf)
				_, err := writer.Write(payload)
				require.NoError(t, err)
				require.NoError(t, writer.Close())
				return buf.Bytes()
			},
		},
		{
			name:     "zstd",
			encoding: "zstd",
			encode: func(t *testing.T, payload []byte) []byte {
				t.Helper()
				var buf bytes.Buffer
				writer, err := zstd.NewWriter(&buf)
				require.NoError(t, err)
				_, err = writer.Write(payload)
				require.NoError(t, err)
				require.NoError(t, writer.Close())
				return buf.Bytes()
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			compressed := test.encode(t, []byte(modelsBody))

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Encoding", test.encoding)
				_, _ = w.Write(compressed)
			}))
			defer server.Close()

			response, bifrostErr := ListModelsByKey(
				schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
				&fasthttp.Client{},
				server.URL,
				schemas.Key{Models: schemas.WhiteList{"*"}},
				false,
				nil,
				schemas.ModelProvider("test"),
				false,
				false,
			)

			require.Nil(t, bifrostErr)
			require.NotNil(t, response)
			require.Len(t, response.Data, 2)
			require.Equal(t, "test/gpt-5", response.Data[0].ID)
			require.Equal(t, "test/gpt-5-mini", response.Data[1].ID)
		})
	}
}

// A body that claims a compression it does not carry, or one this build cannot
// decode, must be reported as a decode failure instead of a bogus unmarshal error.
func TestListModelsByKeyReportsUndecodableBodies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		contentEncoding string
		body            []byte
	}{
		{
			name:            "corrupt gzip",
			contentEncoding: "gzip",
			body:            []byte(`{"object":"list","data":[{"id":"gpt-5"}]}`),
		},
		{
			name:            "unsupported encoding",
			contentEncoding: "compress",
			body:            []byte(`{"object":"list","data":[{"id":"gpt-5"}]}`),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Encoding", test.contentEncoding)
				_, _ = w.Write(test.body)
			}))
			defer server.Close()

			response, bifrostErr := ListModelsByKey(
				schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
				&fasthttp.Client{},
				server.URL,
				schemas.Key{Models: schemas.WhiteList{"*"}},
				false,
				nil,
				schemas.ModelProvider("test"),
				false,
				false,
			)

			require.Nil(t, response)
			require.NotNil(t, bifrostErr)
			require.NotNil(t, bifrostErr.Error)
			require.Equal(t, schemas.ErrProviderResponseDecode, bifrostErr.Error.Message)
		})
	}
}
