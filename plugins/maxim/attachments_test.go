package maxim

import (
	"context"
	"encoding/base64"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/maxim-go/logging"
)

// Test URLs and assets from core/internal/llmtests/utils.go for consistency across Bifrost tests.
var (
	testFileURL     = "https://www.berkshirehathaway.com/letters/2024ltr.pdf"
	testImageURL    = "https://pestworldcdn-dcf2a8gbggazaghf.z01.azurefd.net/media/561791/carpenter-ant4.jpg"
	testImageBase64 = "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQEAYABgAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/2wBDAQkJCQwLDBgNDRgyIRwhMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjL/wAARCAAIAAoDASIAAhEBAxEB/8QAFQEBAQAAAAAAAAAAAAAAAAAAAAb/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/8QAFQEBAQAAAAAAAAAAAAAAAAAAAAX/xAAUEQEAAAAAAAAAAAAAAAAAAAAA/9oADAMBAAIRAxEAPwCdABmX/9k="
)

func strPtr(s string) *string { return &s }

func responsesUserRole() *schemas.ResponsesMessageRoleType {
	r := schemas.ResponsesInputMessageRoleUser
	return &r
}

func TestExtractAttachmentsFromRequest_ChatImageUrlHttp(t *testing.T) {
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentBlocks: []schemas.ChatContentBlock{
							{
								Type: schemas.ChatContentBlockTypeImage,
								ImageURLStruct: &schemas.ChatInputImage{
									URL: testImageURL,
								},
							},
						},
					},
				},
			},
		},
	}
	attachments := ExtractAttachmentsFromRequest(req)
	if len(attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(attachments))
	}
	if _, ok := attachments[0].(*logging.UrlAttachment); !ok {
		t.Errorf("expected *logging.UrlAttachment, got %T", attachments[0])
	}
	ua := attachments[0].(*logging.UrlAttachment)
	if ua.URL != testImageURL {
		t.Errorf("expected URL %q, got %q", testImageURL, ua.URL)
	}
}

func TestExtractAttachmentsFromRequest_ChatImageUrlData(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("fake-png-bytes"))
	dataURL := "data:image/png;base64," + b64
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentBlocks: []schemas.ChatContentBlock{
							{
								Type: schemas.ChatContentBlockTypeImage,
								ImageURLStruct: &schemas.ChatInputImage{
									URL: dataURL,
								},
							},
						},
					},
				},
			},
		},
	}
	attachments := ExtractAttachmentsFromRequest(req)
	if len(attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(attachments))
	}
	if _, ok := attachments[0].(*logging.FileDataAttachment); !ok {
		t.Errorf("expected *logging.FileDataAttachment, got %T", attachments[0])
	}
	fda := attachments[0].(*logging.FileDataAttachment)
	if string(fda.Data) != "fake-png-bytes" {
		t.Errorf("expected data 'fake-png-bytes', got %q", string(fda.Data))
	}
	if fda.MimeType != "image/png" {
		t.Errorf("expected mime image/png, got %q", fda.MimeType)
	}
}

func TestExtractAttachmentsFromRequest_ChatFileUrl(t *testing.T) {
	fileURL := testFileURL
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentBlocks: []schemas.ChatContentBlock{
							{
								Type: schemas.ChatContentBlockTypeFile,
								File: &schemas.ChatInputFile{
									FileURL:  &fileURL,
									Filename: strPtr("2024ltr.pdf"),
								},
							},
						},
					},
				},
			},
		},
	}
	attachments := ExtractAttachmentsFromRequest(req)
	if len(attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(attachments))
	}
	if _, ok := attachments[0].(*logging.UrlAttachment); !ok {
		t.Errorf("expected *logging.UrlAttachment, got %T", attachments[0])
	}
	ua := attachments[0].(*logging.UrlAttachment)
	if ua.URL != fileURL {
		t.Errorf("expected URL %q, got %q", fileURL, ua.URL)
	}
}

func TestExtractAttachmentsFromRequest_ChatFileData(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("pdf content"))
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentBlocks: []schemas.ChatContentBlock{
							{
								Type: schemas.ChatContentBlockTypeFile,
								File: &schemas.ChatInputFile{
									FileData: &b64,
									Filename: strPtr("doc.pdf"),
									FileType: strPtr("application/pdf"),
								},
							},
						},
					},
				},
			},
		},
	}
	attachments := ExtractAttachmentsFromRequest(req)
	if len(attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(attachments))
	}
	if _, ok := attachments[0].(*logging.FileDataAttachment); !ok {
		t.Errorf("expected *logging.FileDataAttachment, got %T", attachments[0])
	}
	fda := attachments[0].(*logging.FileDataAttachment)
	if string(fda.Data) != "pdf content" {
		t.Errorf("expected data 'pdf content', got %q", string(fda.Data))
	}
}

func TestExtractAttachmentsFromRequest_ChatInputAudio(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("audio-bytes"))
	format := "wav"
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentBlocks: []schemas.ChatContentBlock{
							{
								Type: schemas.ChatContentBlockTypeInputAudio,
								InputAudio: &schemas.ChatInputAudio{
									Data:   b64,
									Format: &format,
								},
							},
						},
					},
				},
			},
		},
	}
	attachments := ExtractAttachmentsFromRequest(req)
	if len(attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(attachments))
	}
	if _, ok := attachments[0].(*logging.FileDataAttachment); !ok {
		t.Errorf("expected *logging.FileDataAttachment, got %T", attachments[0])
	}
	fda := attachments[0].(*logging.FileDataAttachment)
	if string(fda.Data) != "audio-bytes" {
		t.Errorf("expected data 'audio-bytes', got %q", string(fda.Data))
	}
	if fda.MimeType != "audio/wav" {
		t.Errorf("expected mime audio/wav, got %q", fda.MimeType)
	}
}

func TestExtractAttachmentsFromRequest_ResponsesInputImage(t *testing.T) {
	req := &schemas.BifrostRequest{
		RequestType: schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Input: []schemas.ResponsesMessage{
				{
					Role: responsesUserRole(),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: schemas.ResponsesInputMessageContentBlockTypeImage,
								ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
									ImageURL: &testImageURL,
								},
							},
						},
					},
				},
			},
		},
	}
	attachments := ExtractAttachmentsFromRequest(req)
	if len(attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(attachments))
	}
	if _, ok := attachments[0].(*logging.UrlAttachment); !ok {
		t.Errorf("expected *logging.UrlAttachment, got %T", attachments[0])
	}
}

func TestExtractAttachmentsFromRequest_ResponsesInputFile(t *testing.T) {
	fileURL := testFileURL
	req := &schemas.BifrostRequest{
		RequestType: schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Input: []schemas.ResponsesMessage{
				{
					Role: responsesUserRole(),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: schemas.ResponsesInputMessageContentBlockTypeFile,
								ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
									FileURL:  &fileURL,
									Filename: strPtr("2024ltr.pdf"),
									FileType: strPtr("application/pdf"),
								},
							},
						},
					},
				},
			},
		},
	}
	attachments := ExtractAttachmentsFromRequest(req)
	if len(attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(attachments))
	}
	if _, ok := attachments[0].(*logging.UrlAttachment); !ok {
		t.Errorf("expected *logging.UrlAttachment, got %T", attachments[0])
	}
}

func TestExtractAttachmentsFromRequest_ResponsesInputAudio(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("mp3-bytes"))
	req := &schemas.BifrostRequest{
		RequestType: schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Input: []schemas.ResponsesMessage{
				{
					Role: responsesUserRole(),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: schemas.ResponsesInputMessageContentBlockTypeAudio,
								Audio: &schemas.ResponsesInputMessageContentBlockAudio{
									Format: "mp3",
									Data:   b64,
								},
							},
						},
					},
				},
			},
		},
	}
	attachments := ExtractAttachmentsFromRequest(req)
	if len(attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(attachments))
	}
	if _, ok := attachments[0].(*logging.FileDataAttachment); !ok {
		t.Errorf("expected *logging.FileDataAttachment, got %T", attachments[0])
	}
	fda := attachments[0].(*logging.FileDataAttachment)
	if string(fda.Data) != "mp3-bytes" {
		t.Errorf("expected data 'mp3-bytes', got %q", string(fda.Data))
	}
	if fda.MimeType != "audio/mpeg" {
		t.Errorf("expected mime audio/mpeg, got %q", fda.MimeType)
	}
}

func TestExtractAttachmentsFromRequest_NoAttachments(t *testing.T) {
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: strPtr("Hello, world!"),
					},
				},
			},
		},
	}
	attachments := ExtractAttachmentsFromRequest(req)
	if len(attachments) != 0 {
		t.Fatalf("expected 0 attachments, got %d", len(attachments))
	}
}

func TestExtractAttachmentsFromRequest_TextCompletion(t *testing.T) {
	req := &schemas.BifrostRequest{
		RequestType: schemas.TextCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentBlocks: []schemas.ChatContentBlock{
							{
								Type: schemas.ChatContentBlockTypeImage,
								ImageURLStruct: &schemas.ChatInputImage{
									URL: "https://example.com/image.png",
								},
							},
						},
					},
				},
			},
		},
	}
	attachments := ExtractAttachmentsFromRequest(req)
	if len(attachments) != 0 {
		t.Fatalf("expected 0 attachments for text completion, got %d", len(attachments))
	}
}

func TestExtractAttachmentsFromRequest_NilRequest(t *testing.T) {
	attachments := ExtractAttachmentsFromRequest(nil)
	if attachments != nil {
		t.Fatalf("expected nil for nil request, got %v", attachments)
	}
}

// TestExtractAttachmentsFromRequest_ChatFileDataFromBase64 uses testImageBase64
// and verifies FileData extraction from base64-encoded content.
func TestExtractAttachmentsFromRequest_ChatFileDataFromBase64(t *testing.T) {
	// Extract base64 from data URL (format: data:image/jpeg;base64,...)
	idx := strings.Index(testImageBase64, ";base64,")
	if idx == -1 {
		t.Fatal("invalid testImageBase64 format")
	}
	b64 := testImageBase64[idx+8:]
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("failed to decode test image base64: %v", err)
	}
	b64ForRequest := base64.StdEncoding.EncodeToString(data)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentBlocks: []schemas.ChatContentBlock{
							{
								Type: schemas.ChatContentBlockTypeFile,
								File: &schemas.ChatInputFile{
									FileData: &b64ForRequest,
									Filename: strPtr("grey_solid.jpg"),
									FileType: strPtr("image/jpeg"),
								},
							},
						},
					},
				},
			},
		},
	}
	attachments := ExtractAttachmentsFromRequest(req)
	if len(attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(attachments))
	}
	if _, ok := attachments[0].(*logging.FileDataAttachment); !ok {
		t.Errorf("expected *logging.FileDataAttachment, got %T", attachments[0])
	}
	fda := attachments[0].(*logging.FileDataAttachment)
	if len(fda.Data) != len(data) {
		t.Errorf("expected data length %d, got %d", len(data), len(fda.Data))
	}
	if fda.MimeType != "image/jpeg" {
		t.Errorf("expected mime image/jpeg, got %q", fda.MimeType)
	}
}

// requireIntegrationEnv skips the test if required env vars for real API calls are not set.
func requireIntegrationEnv(t *testing.T) {
	t.Helper()
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set, skipping integration test")
	}
	if os.Getenv("MAXIM_API_KEY") == "" {
		t.Skip("MAXIM_API_KEY not set, skipping integration test")
	}
	if os.Getenv("MAXIM_LOG_REPO_ID") == "" {
		t.Skip("MAXIM_LOG_REPO_ID not set, skipping integration test")
	}
}

// TestVisionWithImageUrl_Integration sends a real OpenAI vision request via Bifrost
// with the maxim plugin. The plugin extracts the image_url and logs it as an attachment
// to Maxim. Verify attachments in the Maxim dashboard after the test.
func TestVisionWithImageUrl_Integration(t *testing.T) {
	requireIntegrationEnv(t)

	plugin, err := getPlugin()
	if err != nil {
		t.Fatalf("failed to get plugin: %v", err)
	}

	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account:    &BaseAccount{},
		LLMPlugins: []schemas.LLMPlugin{plugin},
		Logger:     bifrost.NewDefaultLogger(schemas.LogLevelDebug),
	})
	if err != nil {
		t.Fatalf("failed to init Bifrost: %v", err)
	}
	defer client.Shutdown()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := client.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentBlocks: []schemas.ChatContentBlock{
						{
							Type: schemas.ChatContentBlockTypeText,
							Text: bifrost.Ptr("Describe this image in one sentence."),
						},
						{
							Type: schemas.ChatContentBlockTypeImage,
							ImageURLStruct: &schemas.ChatInputImage{
								URL: testImageURL,
							},
						},
					},
				},
			},
		},
	})

	if bifrostErr != nil {
		t.Fatalf("ChatCompletionRequest failed: %v", bifrostErr)
	}

	log.Printf("Vision request with image URL completed. Check Maxim dashboard for trace with attachment.")
}

// TestVisionWithImageData_Integration fetches testImageURL, encodes it as a data URL,
// and sends to OpenAI vision via Bifrost. The maxim plugin extracts the data URL and
// logs it as a FileDataAttachment. Uses a real image (carpenter ant) since OpenAI
// rejects minimal test images like the grey solid.
func TestVisionWithImageData_Integration(t *testing.T) {
	requireIntegrationEnv(t)

	// Fetch real image and encode as data URL (OpenAI rejects minimal grey solid)
	httpClient := &http.Client{Timeout: 20 * time.Second}
	resp, err := httpClient.Get(testImageURL)

	if err != nil {
		t.Skipf("failed to fetch test image: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("test image URL returned %d", resp.StatusCode)
	}
	imgData, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Skipf("failed to read test image: %v", err)
	}
	dataURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(imgData)

	plugin, err := getPlugin()
	if err != nil {
		t.Fatalf("failed to get plugin: %v", err)
	}

	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account:    &BaseAccount{},
		LLMPlugins: []schemas.LLMPlugin{plugin},
		Logger:     bifrost.NewDefaultLogger(schemas.LogLevelDebug),
	})
	if err != nil {
		t.Fatalf("failed to init Bifrost: %v", err)
	}
	defer client.Shutdown()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := client.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentBlocks: []schemas.ChatContentBlock{
						{
							Type: schemas.ChatContentBlockTypeText,
							Text: bifrost.Ptr("What do you see in this image? One sentence."),
						},
						{
							Type: schemas.ChatContentBlockTypeImage,
							ImageURLStruct: &schemas.ChatInputImage{
								URL: dataURL,
							},
						},
					},
				},
			},
		},
	})

	if bifrostErr != nil {
		t.Fatalf("ChatCompletionRequest failed: %v", bifrostErr)
	}

	log.Printf("Vision request with base64 image completed. Check Maxim dashboard for trace with FileData attachment.")
}
