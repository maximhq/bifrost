package gemini

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// refusedAudioURL is a link-local metadata address. The real fetcher (no stub) refuses it at
// dial time without any network traffic, which is what the 400-mapping tests rely on.
const refusedAudioURL = "https://169.254.169.254/latest/audio.wav"

func chatAudioURLRequest(urls ...string) *schemas.BifrostChatRequest {
	format := "wav"
	blocks := make([]schemas.ChatContentBlock, 0, len(urls))
	for _, url := range urls {
		blocks = append(blocks, schemas.ChatContentBlock{
			Type:       schemas.ChatContentBlockTypeInputAudio,
			InputAudio: &schemas.ChatInputAudio{URL: url, Format: &format},
		})
	}
	return &schemas.BifrostChatRequest{
		Model: "gemini-2.0-flash",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentBlocks: blocks},
		}},
	}
}

func chatTextRequest() *schemas.BifrostChatRequest {
	text := "hello"
	return &schemas.BifrostChatRequest{
		Model: "gemini-2.0-flash",
		Input: []schemas.ChatMessage{{
			Role: schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
				{Type: schemas.ChatContentBlockTypeText, Text: &text},
			}},
		}},
	}
}

func batchAudioBody(url string) map[string]interface{} {
	return map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{
						"type":        "input_audio",
						"input_audio": map[string]interface{}{"url": url, "format": "wav"},
					},
				},
			},
		},
	}
}

func TestResolveChatAudioURLsDownloadsAudioForConverter(t *testing.T) {
	fetches := stubAudioFetchBytes(t, []byte("audio-bytes"))

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := chatAudioURLRequest("https://audio.example/sample.wav")
	resolved, err := ResolveChatAudioURLs(ctx, request)
	require.NoError(t, err)
	require.Empty(t, request.Input[0].Content.ContentBlocks[0].InputAudio.Data, "the original request must survive for a retry")

	result, err := ToGeminiChatCompletionRequest(ctx, resolved)
	require.NoError(t, err)
	require.Len(t, result.Contents, 1)
	require.Len(t, result.Contents[0].Parts, 1)
	require.NotNil(t, result.Contents[0].Parts[0].InlineData)
	require.Equal(t, "audio/wav", result.Contents[0].Parts[0].InlineData.MIMEType)
	require.Equal(t, "YXVkaW8tYnl0ZXM=", result.Contents[0].Parts[0].InlineData.Data, "base64 of audio-bytes")
	require.Equal(t, int32(1), fetches.Load())
}

func TestToGeminiChatCompletionRequestDoesNotDownloadAudio(t *testing.T) {
	fetches := stubAudioFetchBytes(t, []byte("audio-bytes"))

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, err := ToGeminiChatCompletionRequest(ctx, chatAudioURLRequest("https://audio.example/sample.wav"))
	require.ErrorIs(t, err, errUnresolvedAudioURL)
	require.Zero(t, fetches.Load(), "converters must make no HTTP calls")
}

func TestResolveChatAudioURLsSkipsToolResultAudio(t *testing.T) {
	fetches := stubAudioFetchBytes(t, []byte("audio-bytes"))

	request := chatAudioURLRequest("https://audio.example/gone.wav")
	request.Input[0].Role = schemas.ChatMessageRoleTool
	request.Input[0].ChatToolMessage = &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("call_1")}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resolved, err := ResolveChatAudioURLs(ctx, request)
	require.NoError(t, err, "tool result audio is flattened to text, so its URL must not be fetched")
	require.Same(t, request, resolved)
	require.Zero(t, fetches.Load())
}

// A refused URL goes through the real fetcher (core/network SSRF dialer) and must come back as
// a caller error naming the block, not a conversion fault.
// The CGNAT and IPv6-transition cases are the ones a stdlib-predicate guard lets through.
func TestResolveChatAudioURLsRefusedURLIsBadRequest(t *testing.T) {
	for _, audioURL := range []string{
		refusedAudioURL,                      // link-local metadata
		"https://100.100.100.200/audio.wav",  // Alibaba metadata, CGNAT 100.64.0.0/10
		"https://100.64.0.1/audio.wav",       // CGNAT, EKS pod space
		"https://[64:ff9b::a9fe:a9fe]/a.wav", // NAT64-wrapped 169.254.169.254
		"https://[2002:a9fe:a9fe::1]/a.wav",  // 6to4-wrapped 169.254.169.254
		"https://127.0.0.1/audio.wav",        // loopback
	} {
		t.Run(audioURL, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			_, err := ResolveChatAudioURLs(ctx, chatAudioURLRequest(audioURL))
			require.Error(t, err)
			require.True(t, providerUtils.IsInvalidRequestError(err), "want a 400-class error, got %v", err)
			require.Contains(t, err.Error(), "messages[0].content[0]")
		})
	}
}

// The 400 must survive the provider method: CheckContextAndGetRequestBody used to turn every
// resolver error into a 500, and the upstream must never be called.
func TestChatCompletionRefusedAudioURLReturns400(t *testing.T) {
	var upstreamHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	provider := NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL},
	}, testNoopLogger{})
	key := schemas.Key{Value: *schemas.NewSecretVar("dummy-key")}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bErr := provider.ChatCompletion(ctx, key, chatAudioURLRequest(refusedAudioURL))
	require.NotNil(t, bErr)
	require.NotNil(t, bErr.StatusCode)
	require.Equal(t, http.StatusBadRequest, *bErr.StatusCode, "message: %s", bErr.Error.Message)
	require.Contains(t, bErr.Error.Message, "messages[0].content[0]")
	require.NotContains(t, bErr.Error.Message, "latest/audio.wav?", "no query string may be echoed")
	require.Zero(t, upstreamHits.Load())
}

func TestResolveChatAudioURLsCancelledContextIsNotBadRequest(t *testing.T) {
	stubAudioFetch(t, func(ctx context.Context, _ string) (string, string, error) {
		return "", "", errors.New("connection reset")
	})
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	ctx := schemas.NewBifrostContext(parent, schemas.NoDeadline)

	_, err := ResolveChatAudioURLs(ctx, chatAudioURLRequest("https://audio.example/sample.wav"))
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, providerUtils.IsInvalidRequestError(err), "a cancelled request is not the caller's bad input")
}

// Three 10 MiB clips pass the 25 MiB per-URL cap one by one, but not the per-request total.
func TestResolveChatAudioURLsEnforcesPerRequestTotal(t *testing.T) {
	fetches := stubAudioFetchBytes(t, make([]byte, 10*1024*1024))

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, err := ResolveChatAudioURLs(ctx, chatAudioURLRequest(
		"https://audio.example/a.wav", "https://audio.example/b.wav", "https://audio.example/c.wav",
	))
	require.Error(t, err)
	require.True(t, providerUtils.IsInvalidRequestError(err), "want a 400-class error, got %v", err)
	require.Contains(t, err.Error(), "messages[0].content[2]")
	require.Equal(t, int32(3), fetches.Load(), "the fetch that crosses the budget is the last one")
}

func TestResolveChatAudioURLsLeavesTextRequestUntouched(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := chatTextRequest()
	var resolved *schemas.BifrostChatRequest
	allocs := testing.AllocsPerRun(100, func() {
		resolved, _ = ResolveChatAudioURLs(ctx, request)
	})
	require.Same(t, request, resolved)
	require.Zero(t, allocs)
}

func TestResolveBatchAudioURLsLeavesBodyWithoutAudioUntouched(t *testing.T) {
	fetches := stubAudioFetchBytes(t, []byte("audio-bytes"))

	body := map[string]interface{}{
		"messages": []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
	}
	resolved, err := ResolveBatchAudioURLs(context.Background(), body, NewAudioURLBudget())
	require.NoError(t, err)
	require.Equal(t, reflect.ValueOf(body).Pointer(), reflect.ValueOf(resolved).Pointer(), "a body with no audio URL must be returned as-is")
	require.Zero(t, fetches.Load())
}

func TestResolveBatchAudioURLsResolvesIntoCopy(t *testing.T) {
	stubAudioFetchBytes(t, []byte("audio-bytes"))

	body := batchAudioBody("https://audio.example/sample.wav")
	resolved, err := ResolveBatchAudioURLs(context.Background(), body, NewAudioURLBudget())
	require.NoError(t, err)

	req, err := ToGeminiBatchGenerateContentRequest(resolved)
	require.NoError(t, err)
	require.Len(t, req.Contents, 1)
	require.NotNil(t, req.Contents[0].Parts[0].InlineData)
	require.Equal(t, "YXVkaW8tYnl0ZXM=", req.Contents[0].Parts[0].InlineData.Data, "base64 of audio-bytes")

	_, err = ToGeminiBatchGenerateContentRequest(body)
	require.ErrorIs(t, err, errUnresolvedAudioURL, "the caller's body must not be mutated")
}

// A 10 MiB clip encodes with two padding characters; the budget charges its decoded size, so
// 10 MiB plus 15 MiB fits the 25 MiB budget exactly.
func TestAudioURLBudgetChargesDecodedSizeNotPadding(t *testing.T) {
	payloads := map[string][]byte{
		"https://audio.example/a.wav": make([]byte, 10*1024*1024),
		"https://audio.example/b.wav": make([]byte, 15*1024*1024),
	}
	stubAudioFetch(t, func(_ context.Context, url string) (string, string, error) {
		return "audio/wav", base64.StdEncoding.EncodeToString(payloads[url]), nil
	})
	require.True(t, strings.HasSuffix(base64.StdEncoding.EncodeToString(payloads["https://audio.example/a.wav"]), "=="))

	budget := NewAudioURLBudget()
	_, err := ResolveBatchAudioURLs(context.Background(), batchAudioBody("https://audio.example/a.wav"), budget)
	require.NoError(t, err)
	_, err = ResolveBatchAudioURLs(context.Background(), batchAudioBody("https://audio.example/b.wav"), budget)
	require.NoError(t, err, "10 MiB + 15 MiB equals the budget and must be accepted")
	require.Zero(t, budget.remaining)
}

// One budget is shared by every item of an inline batch, so a batch cannot hold N x 25 MiB.
func TestResolveBatchAudioURLsSharesBudgetAcrossItems(t *testing.T) {
	stubAudioFetchBytes(t, make([]byte, 15*1024*1024))

	budget := NewAudioURLBudget()
	_, err := ResolveBatchAudioURLs(context.Background(), batchAudioBody("https://audio.example/a.wav"), budget)
	require.NoError(t, err)
	_, err = ResolveBatchAudioURLs(context.Background(), batchAudioBody("https://audio.example/b.wav"), budget)
	require.Error(t, err)

	bErr := BatchAudioURLError(1, "item-b", err)
	require.NotNil(t, bErr.StatusCode)
	require.Equal(t, http.StatusBadRequest, *bErr.StatusCode)
	require.True(t, strings.HasPrefix(bErr.Error.Message, `batch request item 1 (custom_id "item-b"): `), bErr.Error.Message)
}

func TestBatchAudioURLErrorKeepsInternalFaultsAs500(t *testing.T) {
	bErr := BatchAudioURLError(0, "x", errors.New("failed to marshal messages"))
	require.Nil(t, bErr.StatusCode, "a non-caller fault keeps the operation-error default")
}

// The check runs on every Gemini chat request, so a request with no audio URL must
// pay for a scan and nothing else.
func BenchmarkResolveChatAudioURLs(b *testing.B) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := chatTextRequest()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ResolveChatAudioURLs(ctx, request); err != nil {
			b.Fatal(err)
		}
	}
}
