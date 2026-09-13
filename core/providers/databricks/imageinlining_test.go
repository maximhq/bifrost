package databricks

import (
	"context"
	"errors"
	"strings"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// stubFetcher replaces the network for the duration of a test. Tests that install it must
// not run in parallel with each other: the seam is a package variable.
func stubFetcher(t *testing.T, mediaType, encoded string, err error) *[]string {
	t.Helper()
	var fetched []string
	previous := fetchAndEncodeURL
	fetchAndEncodeURL = func(_ context.Context, resourceURL string) (string, string, error) {
		fetched = append(fetched, resourceURL)
		return mediaType, encoded, err
	}
	t.Cleanup(func() { fetchAndEncodeURL = previous })
	return &fetched
}

func chatImageRequest(urls ...string) *schemas.BifrostChatRequest {
	blocks := []schemas.ChatContentBlock{{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("what is this?")}}
	for _, u := range urls {
		blocks = append(blocks, schemas.ChatContentBlock{
			Type:           schemas.ChatContentBlockTypeImage,
			ImageURLStruct: &schemas.ChatInputImage{URL: u, Detail: schemas.Ptr("high")},
		})
	}
	return &schemas.BifrostChatRequest{
		Provider: schemas.Databricks,
		Model:    "databricks-claude-sonnet-4-5",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("be brief")}},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentBlocks: blocks}},
		},
	}
}

func responsesImageRequest(urls ...string) *schemas.BifrostResponsesRequest {
	blocks := []schemas.ResponsesMessageContentBlock{{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: schemas.Ptr("what is this?")}}
	for _, u := range urls {
		blocks = append(blocks, schemas.ResponsesMessageContentBlock{
			Type:                                   schemas.ResponsesInputMessageContentBlockTypeImage,
			ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{ImageURL: schemas.Ptr(u)},
		})
	}
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.Databricks,
		Model:    "databricks-claude-sonnet-4-5",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentBlocks: blocks},
		}},
	}
}

// TestInlineChatImageURLs reproduces the live rejection "Http data URLs are not supported by
// Claude": a remote image must reach Databricks as a data URL, a data URL is left alone, and
// the caller's request is not mutated.
func TestInlineChatImageURLs(t *testing.T) {
	fetched := stubFetcher(t, "image/png", "AAAA", nil)

	const remote = "https://example.com/cat.png?sig=secret"
	const inline = "data:image/jpeg;base64,BBBB"
	original := chatImageRequest(remote, inline)

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 0)
	defer cancel()
	got, bErr := inlineChatImageURLs(ctx, original)
	if bErr != nil {
		t.Fatalf("inlineChatImageURLs returned an error: %v", bErr)
	}

	if len(*fetched) != 1 || (*fetched)[0] != remote {
		t.Errorf("fetched URLs: got %v, want [%s]", *fetched, remote)
	}
	blocks := got.Input[1].Content.ContentBlocks
	if want := "data:image/png;base64,AAAA"; blocks[1].ImageURLStruct.URL != want {
		t.Errorf("remote image: got %q, want %q", blocks[1].ImageURLStruct.URL, want)
	}
	if blocks[1].ImageURLStruct.Detail == nil || *blocks[1].ImageURLStruct.Detail != "high" {
		t.Error("inlining dropped the image detail hint")
	}
	if blocks[2].ImageURLStruct.URL != inline {
		t.Errorf("data URL was rewritten: got %q", blocks[2].ImageURLStruct.URL)
	}
	if got.Input[0].Content.ContentStr == nil || *got.Input[0].Content.ContentStr != "be brief" {
		t.Error("string-content message was not carried over")
	}
	if original.Input[1].Content.ContentBlocks[1].ImageURLStruct.URL != remote {
		t.Error("inlining mutated the original request")
	}
}

// TestInlineChatImageURLsNoRemoteImagesIsPassthrough pins that a request without a remote
// image is returned as-is: no copy, no fetch.
func TestInlineChatImageURLsNoRemoteImagesIsPassthrough(t *testing.T) {
	fetched := stubFetcher(t, "", "", errors.New("must not be called"))

	original := chatImageRequest("data:image/png;base64,AAAA")
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 0)
	defer cancel()
	got, bErr := inlineChatImageURLs(ctx, original)
	if bErr != nil {
		t.Fatalf("inlineChatImageURLs returned an error: %v", bErr)
	}
	if got != original {
		t.Error("a request with nothing to inline must be returned unchanged")
	}
	if len(*fetched) != 0 {
		t.Errorf("fetcher was called for %v", *fetched)
	}
}

// TestInlineChatImageURLsFetchFailureIsClientError pins that a fetch failure aborts the
// request with a 400 that names the URL by its redacted form only.
func TestInlineChatImageURLsFetchFailureIsClientError(t *testing.T) {
	stubFetcher(t, "", "", errors.New("dial tcp: i/o timeout"))

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 0)
	defer cancel()
	_, bErr := inlineChatImageURLs(ctx, chatImageRequest("https://example.com/cat.png?X-Amz-Signature=secret"))
	if bErr == nil {
		t.Fatal("inlineChatImageURLs succeeded, want an error")
	}
	if bErr.StatusCode == nil || *bErr.StatusCode != 400 {
		t.Errorf("status: got %v, want 400", bErr.StatusCode)
	}
	if strings.Contains(bErr.Error.Message, "secret") {
		t.Errorf("error leaks the URL query: %q", bErr.Error.Message)
	}
	if !strings.Contains(bErr.Error.Message, "i/o timeout") {
		t.Errorf("error lost the cause: %q", bErr.Error.Message)
	}
}

// TestInlineChatImageURLsFallsBackToExtension covers a server answering octet-stream for a
// PNG: the extension decides the media type.
func TestInlineChatImageURLsFallsBackToExtension(t *testing.T) {
	stubFetcher(t, "application/octet-stream", "AAAA", nil)

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 0)
	defer cancel()
	got, bErr := inlineChatImageURLs(ctx, chatImageRequest("https://example.com/cat.PNG"))
	if bErr != nil {
		t.Fatalf("inlineChatImageURLs returned an error: %v", bErr)
	}
	if want := "data:image/png;base64,AAAA"; got.Input[1].Content.ContentBlocks[1].ImageURLStruct.URL != want {
		t.Errorf("got %q, want %q", got.Input[1].Content.ContentBlocks[1].ImageURLStruct.URL, want)
	}
}

// TestInlineResponsesImageURLs is the native Responses surface counterpart.
func TestInlineResponsesImageURLs(t *testing.T) {
	fetched := stubFetcher(t, "image/jpeg; charset=binary", "CCCC", nil)

	const remote = "http://example.com/dog.jpg"
	original := responsesImageRequest(remote, "data:image/png;base64,AAAA")

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 0)
	defer cancel()
	got, bErr := inlineResponsesImageURLs(ctx, original)
	if bErr != nil {
		t.Fatalf("inlineResponsesImageURLs returned an error: %v", bErr)
	}
	if len(*fetched) != 1 || (*fetched)[0] != remote {
		t.Errorf("fetched URLs: got %v, want [%s]", *fetched, remote)
	}
	blocks := got.Input[0].Content.ContentBlocks
	if want := "data:image/jpeg;base64,CCCC"; *blocks[1].ImageURL != want {
		t.Errorf("remote image: got %q, want %q", *blocks[1].ImageURL, want)
	}
	if *blocks[2].ImageURL != "data:image/png;base64,AAAA" {
		t.Errorf("data URL was rewritten: got %q", *blocks[2].ImageURL)
	}
	if *original.Input[0].Content.ContentBlocks[1].ImageURL != remote {
		t.Error("inlining mutated the original request")
	}
}
