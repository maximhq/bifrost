package mistral

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestToMistralOCRRequest tests conversion from Bifrost OCR request to Mistral OCR request.
func TestToMistralOCRRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    *schemas.BifrostOCRRequest
		validate func(t *testing.T, result *MistralOCRRequest)
	}{
		{
			name:  "nil request returns nil",
			input: nil,
			validate: func(t *testing.T, result *MistralOCRRequest) {
				assert.Nil(t, result)
			},
		},
		{
			name: "basic document_url request",
			input: &schemas.BifrostOCRRequest{
				Model: "mistral-ocr-latest",
				Document: schemas.OCRDocument{
					Type:        schemas.OCRDocumentTypeDocumentURL,
					DocumentURL: schemas.Ptr("https://example.com/doc.pdf"),
				},
			},
			validate: func(t *testing.T, result *MistralOCRRequest) {
				require.NotNil(t, result)
				assert.Equal(t, "mistral-ocr-latest", result.Model)
				assert.Equal(t, "document_url", result.Document.Type)
				assert.Equal(t, "https://example.com/doc.pdf", result.Document.DocumentURL)
				assert.Empty(t, result.Document.ImageURL)
				assert.Empty(t, result.ID)
			},
		},
		{
			name: "basic image_url request",
			input: &schemas.BifrostOCRRequest{
				Model: "mistral-ocr-latest",
				Document: schemas.OCRDocument{
					Type:     schemas.OCRDocumentTypeImageURL,
					ImageURL: schemas.Ptr("https://example.com/image.png"),
				},
			},
			validate: func(t *testing.T, result *MistralOCRRequest) {
				require.NotNil(t, result)
				assert.Equal(t, "mistral-ocr-latest", result.Model)
				assert.Equal(t, "image_url", result.Document.Type)
				assert.Equal(t, "https://example.com/image.png", result.Document.ImageURL)
				assert.Empty(t, result.Document.DocumentURL)
			},
		},
		{
			name: "request with ID",
			input: &schemas.BifrostOCRRequest{
				Model: "mistral-ocr-latest",
				ID:    schemas.Ptr("req-123"),
				Document: schemas.OCRDocument{
					Type:        schemas.OCRDocumentTypeDocumentURL,
					DocumentURL: schemas.Ptr("https://example.com/doc.pdf"),
				},
			},
			validate: func(t *testing.T, result *MistralOCRRequest) {
				require.NotNil(t, result)
				assert.Equal(t, "req-123", result.ID)
			},
		},
		{
			name: "request with all parameters",
			input: &schemas.BifrostOCRRequest{
				Model: "mistral-ocr-latest",
				Document: schemas.OCRDocument{
					Type:        schemas.OCRDocumentTypeDocumentURL,
					DocumentURL: schemas.Ptr("https://example.com/doc.pdf"),
				},
				Params: &schemas.OCRParameters{
					IncludeImageBase64:       schemas.Ptr(true),
					Pages:                    []int{0, 1, 2},
					ImageLimit:               schemas.Ptr(10),
					ImageMinSize:             schemas.Ptr(100),
					TableFormat:              schemas.Ptr("html"),
					ExtractHeader:            schemas.Ptr(true),
					ExtractFooter:            schemas.Ptr(false),
					BBoxAnnotationFormat:     schemas.Ptr("json"),
					DocumentAnnotationFormat: schemas.Ptr("markdown"),
					DocumentAnnotationPrompt: schemas.Ptr("Summarize this document"),
				},
			},
			validate: func(t *testing.T, result *MistralOCRRequest) {
				require.NotNil(t, result)
				assert.Equal(t, "mistral-ocr-latest", result.Model)
				assert.Equal(t, "document_url", result.Document.Type)
				assert.Equal(t, "https://example.com/doc.pdf", result.Document.DocumentURL)

				require.NotNil(t, result.IncludeImageBase64)
				assert.True(t, *result.IncludeImageBase64)
				assert.Equal(t, []int{0, 1, 2}, result.Pages)
				require.NotNil(t, result.ImageLimit)
				assert.Equal(t, 10, *result.ImageLimit)
				require.NotNil(t, result.ImageMinSize)
				assert.Equal(t, 100, *result.ImageMinSize)
				require.NotNil(t, result.TableFormat)
				assert.Equal(t, "html", *result.TableFormat)
				require.NotNil(t, result.ExtractHeader)
				assert.True(t, *result.ExtractHeader)
				require.NotNil(t, result.ExtractFooter)
				assert.False(t, *result.ExtractFooter)
				require.NotNil(t, result.BBoxAnnotationFormat)
				assert.Equal(t, "json", *result.BBoxAnnotationFormat)
				require.NotNil(t, result.DocumentAnnotationFormat)
				assert.Equal(t, "markdown", *result.DocumentAnnotationFormat)
				require.NotNil(t, result.DocumentAnnotationPrompt)
				assert.Equal(t, "Summarize this document", *result.DocumentAnnotationPrompt)
			},
		},
		{
			name: "request with nil params",
			input: &schemas.BifrostOCRRequest{
				Model: "mistral-ocr-latest",
				Document: schemas.OCRDocument{
					Type:        schemas.OCRDocumentTypeDocumentURL,
					DocumentURL: schemas.Ptr("https://example.com/doc.pdf"),
				},
				Params: nil,
			},
			validate: func(t *testing.T, result *MistralOCRRequest) {
				require.NotNil(t, result)
				assert.Nil(t, result.IncludeImageBase64)
				assert.Nil(t, result.Pages)
				assert.Nil(t, result.ImageLimit)
				assert.Nil(t, result.ImageMinSize)
				assert.Nil(t, result.TableFormat)
				assert.Nil(t, result.ExtractHeader)
				assert.Nil(t, result.ExtractFooter)
				assert.Nil(t, result.BBoxAnnotationFormat)
				assert.Nil(t, result.DocumentAnnotationFormat)
				assert.Nil(t, result.DocumentAnnotationPrompt)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := ToMistralOCRRequest(tt.input)
			tt.validate(t, result)
		})
	}
}

// TestToBifrostOCRResponse tests conversion from Mistral OCR response to Bifrost OCR response.
func TestToBifrostOCRResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    *MistralOCRResponse
		validate func(t *testing.T, result *schemas.BifrostOCRResponse)
	}{
		{
			name:  "nil response returns nil",
			input: nil,
			validate: func(t *testing.T, result *schemas.BifrostOCRResponse) {
				assert.Nil(t, result)
			},
		},
		{
			name: "basic response with single page",
			input: &MistralOCRResponse{
				Model: "mistral-ocr-latest",
				Pages: []MistralOCRPage{
					{
						Index:    0,
						Markdown: "# Hello World\n\nThis is a test document.",
					},
				},
			},
			validate: func(t *testing.T, result *schemas.BifrostOCRResponse) {
				require.NotNil(t, result)
				assert.Equal(t, "mistral-ocr-latest", result.Model)
				require.Len(t, result.Pages, 1)
				assert.Equal(t, 0, result.Pages[0].Index)
				assert.Equal(t, "# Hello World\n\nThis is a test document.", result.Pages[0].Markdown)
				assert.Nil(t, result.Pages[0].Images)
				assert.Nil(t, result.Pages[0].Dimensions)
				assert.Nil(t, result.UsageInfo)
				assert.Nil(t, result.DocumentAnnotation)
			},
		},
		{
			name: "response with images",
			input: &MistralOCRResponse{
				Model: "mistral-ocr-latest",
				Pages: []MistralOCRPage{
					{
						Index:    0,
						Markdown: "Page with image",
						Images: []MistralOCRPageImage{
							{
								ID:           "img-1",
								TopLeftX:     10.5,
								TopLeftY:     20.3,
								BottomRightX: 100.0,
								BottomRightY: 200.0,
								ImageBase64:  schemas.Ptr("base64encodeddata"),
							},
							{
								ID:           "img-2",
								TopLeftX:     50.0,
								TopLeftY:     60.0,
								BottomRightX: 150.0,
								BottomRightY: 250.0,
							},
						},
					},
				},
			},
			validate: func(t *testing.T, result *schemas.BifrostOCRResponse) {
				require.NotNil(t, result)
				require.Len(t, result.Pages, 1)
				require.Len(t, result.Pages[0].Images, 2)

				img1 := result.Pages[0].Images[0]
				assert.Equal(t, "img-1", img1.ID)
				assert.Equal(t, 10.5, img1.TopLeftX)
				assert.Equal(t, 20.3, img1.TopLeftY)
				assert.Equal(t, 100.0, img1.BottomRightX)
				assert.Equal(t, 200.0, img1.BottomRightY)
				require.NotNil(t, img1.ImageBase64)
				assert.Equal(t, "base64encodeddata", *img1.ImageBase64)

				img2 := result.Pages[0].Images[1]
				assert.Equal(t, "img-2", img2.ID)
				assert.Nil(t, img2.ImageBase64)
			},
		},
		{
			name: "response with dimensions",
			input: &MistralOCRResponse{
				Model: "mistral-ocr-latest",
				Pages: []MistralOCRPage{
					{
						Index:    0,
						Markdown: "Page with dimensions",
						Dimensions: &MistralOCRPageDimensions{
							DPI:    300,
							Height: 2200,
							Width:  1700,
						},
					},
				},
			},
			validate: func(t *testing.T, result *schemas.BifrostOCRResponse) {
				require.NotNil(t, result)
				require.Len(t, result.Pages, 1)
				require.NotNil(t, result.Pages[0].Dimensions)
				assert.Equal(t, 300, result.Pages[0].Dimensions.DPI)
				assert.Equal(t, 2200, result.Pages[0].Dimensions.Height)
				assert.Equal(t, 1700, result.Pages[0].Dimensions.Width)
			},
		},
		{
			name: "response with usage info",
			input: &MistralOCRResponse{
				Model: "mistral-ocr-latest",
				Pages: []MistralOCRPage{
					{Index: 0, Markdown: "Page 1"},
					{Index: 1, Markdown: "Page 2"},
				},
				UsageInfo: &MistralOCRUsageInfo{
					PagesProcessed: 2,
					DocSizeBytes:   1024000,
				},
			},
			validate: func(t *testing.T, result *schemas.BifrostOCRResponse) {
				require.NotNil(t, result)
				require.Len(t, result.Pages, 2)
				require.NotNil(t, result.UsageInfo)
				assert.Equal(t, 2, result.UsageInfo.PagesProcessed)
				assert.Equal(t, 1024000, result.UsageInfo.DocSizeBytes)
			},
		},
		{
			name: "response with document annotation",
			input: &MistralOCRResponse{
				Model: "mistral-ocr-latest",
				Pages: []MistralOCRPage{
					{Index: 0, Markdown: "Page content"},
				},
				DocumentAnnotation: schemas.Ptr("This is a legal contract."),
			},
			validate: func(t *testing.T, result *schemas.BifrostOCRResponse) {
				require.NotNil(t, result)
				require.NotNil(t, result.DocumentAnnotation)
				assert.Equal(t, "This is a legal contract.", *result.DocumentAnnotation)
			},
		},
		{
			name: "response with empty pages",
			input: &MistralOCRResponse{
				Model: "mistral-ocr-latest",
				Pages: []MistralOCRPage{},
			},
			validate: func(t *testing.T, result *schemas.BifrostOCRResponse) {
				require.NotNil(t, result)
				assert.Empty(t, result.Pages)
			},
		},
		{
			name: "full response with all fields",
			input: &MistralOCRResponse{
				Model: "mistral-ocr-latest",
				Pages: []MistralOCRPage{
					{
						Index:    0,
						Markdown: "# Title\n\nParagraph with **bold** text.",
						Images: []MistralOCRPageImage{
							{
								ID:           "img-0-1",
								TopLeftX:     0,
								TopLeftY:     0,
								BottomRightX: 500,
								BottomRightY: 300,
								ImageBase64:  schemas.Ptr("aW1hZ2VkYXRh"),
							},
						},
						Dimensions: &MistralOCRPageDimensions{
							DPI:    150,
							Height: 1100,
							Width:  850,
						},
					},
				},
				UsageInfo: &MistralOCRUsageInfo{
					PagesProcessed: 1,
					DocSizeBytes:   512000,
				},
				DocumentAnnotation: schemas.Ptr("A technical report."),
			},
			validate: func(t *testing.T, result *schemas.BifrostOCRResponse) {
				require.NotNil(t, result)
				assert.Equal(t, "mistral-ocr-latest", result.Model)
				require.Len(t, result.Pages, 1)

				page := result.Pages[0]
				assert.Equal(t, 0, page.Index)
				assert.Contains(t, page.Markdown, "# Title")
				require.Len(t, page.Images, 1)
				assert.Equal(t, "img-0-1", page.Images[0].ID)
				require.NotNil(t, page.Images[0].ImageBase64)
				assert.Equal(t, "aW1hZ2VkYXRh", *page.Images[0].ImageBase64)
				require.NotNil(t, page.Dimensions)
				assert.Equal(t, 150, page.Dimensions.DPI)

				require.NotNil(t, result.UsageInfo)
				assert.Equal(t, 1, result.UsageInfo.PagesProcessed)
				assert.Equal(t, 512000, result.UsageInfo.DocSizeBytes)

				require.NotNil(t, result.DocumentAnnotation)
				assert.Equal(t, "A technical report.", *result.DocumentAnnotation)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := tt.input.ToBifrostOCRResponse()
			tt.validate(t, result)
		})
	}
}

// TestOCRWithMockServer tests the OCR method with a mock HTTP server.
func TestOCRWithMockServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		request        *schemas.BifrostOCRRequest
		statusCode     int
		responseBody   interface{}
		expectError    bool
		errorContains  string
		validateError  func(t *testing.T, err *schemas.BifrostError)
		validateResult func(t *testing.T, resp *schemas.BifrostOCRResponse)
	}{
		{
			name: "successful OCR with document_url",
			request: &schemas.BifrostOCRRequest{
				Model: "mistral-ocr-latest",
				Document: schemas.OCRDocument{
					Type:        schemas.OCRDocumentTypeDocumentURL,
					DocumentURL: schemas.Ptr("https://example.com/doc.pdf"),
				},
			},
			statusCode: http.StatusOK,
			responseBody: MistralOCRResponse{
				Model: "mistral-ocr-latest",
				Pages: []MistralOCRPage{
					{
						Index:    0,
						Markdown: "# Test Document\n\nThis is page 1.",
					},
					{
						Index:    1,
						Markdown: "## Section 2\n\nThis is page 2.",
					},
				},
				UsageInfo: &MistralOCRUsageInfo{
					PagesProcessed: 2,
					DocSizeBytes:   2048,
				},
			},
			expectError: false,
			validateResult: func(t *testing.T, resp *schemas.BifrostOCRResponse) {
				assert.Equal(t, "mistral-ocr-latest", resp.Model)
				require.Len(t, resp.Pages, 2)
				assert.Equal(t, 0, resp.Pages[0].Index)
				assert.Contains(t, resp.Pages[0].Markdown, "Test Document")
				assert.Equal(t, 1, resp.Pages[1].Index)
				require.NotNil(t, resp.UsageInfo)
				assert.Equal(t, 2, resp.UsageInfo.PagesProcessed)
			},
		},
		{
			name: "successful OCR with image_url",
			request: &schemas.BifrostOCRRequest{
				Model: "mistral-ocr-latest",
				Document: schemas.OCRDocument{
					Type:     schemas.OCRDocumentTypeImageURL,
					ImageURL: schemas.Ptr("https://example.com/image.png"),
				},
			},
			statusCode: http.StatusOK,
			responseBody: MistralOCRResponse{
				Model: "mistral-ocr-latest",
				Pages: []MistralOCRPage{
					{
						Index:    0,
						Markdown: "Text extracted from image",
						Images: []MistralOCRPageImage{
							{
								ID:           "img-1",
								TopLeftX:     0,
								TopLeftY:     0,
								BottomRightX: 100,
								BottomRightY: 100,
							},
						},
					},
				},
			},
			expectError: false,
			validateResult: func(t *testing.T, resp *schemas.BifrostOCRResponse) {
				assert.Equal(t, "mistral-ocr-latest", resp.Model)
				require.Len(t, resp.Pages, 1)
				require.Len(t, resp.Pages[0].Images, 1)
				assert.Equal(t, "img-1", resp.Pages[0].Images[0].ID)
			},
		},
		{
			name: "server error 500",
			request: &schemas.BifrostOCRRequest{
				Model: "mistral-ocr-latest",
				Document: schemas.OCRDocument{
					Type:        schemas.OCRDocumentTypeDocumentURL,
					DocumentURL: schemas.Ptr("https://example.com/doc.pdf"),
				},
			},
			statusCode: http.StatusInternalServerError,
			responseBody: map[string]interface{}{
				"message": "Internal server error",
				"type":    "server_error",
				"code":    "internal_error",
			},
			expectError:   true,
			errorContains: "Internal server error",
			validateError: func(t *testing.T, err *schemas.BifrostError) {
				require.NotNil(t, err)
				require.NotNil(t, err.Error)
				require.NotNil(t, err.StatusCode)
				assert.Equal(t, http.StatusInternalServerError, *err.StatusCode)
				require.NotNil(t, err.Error.Type)
				assert.Equal(t, "server_error", *err.Error.Type)
				require.NotNil(t, err.Error.Code)
				assert.Equal(t, "internal_error", *err.Error.Code)
			},
		},
		{
			name: "unauthorized 401",
			request: &schemas.BifrostOCRRequest{
				Model: "mistral-ocr-latest",
				Document: schemas.OCRDocument{
					Type:        schemas.OCRDocumentTypeDocumentURL,
					DocumentURL: schemas.Ptr("https://example.com/doc.pdf"),
				},
			},
			statusCode: http.StatusUnauthorized,
			responseBody: map[string]interface{}{
				"message": "Unauthorized",
				"type":    "authentication_error",
				"code":    "invalid_api_key",
			},
			expectError:   true,
			errorContains: "Unauthorized",
			validateError: func(t *testing.T, err *schemas.BifrostError) {
				require.NotNil(t, err)
				require.NotNil(t, err.Error)
				require.NotNil(t, err.StatusCode)
				assert.Equal(t, http.StatusUnauthorized, *err.StatusCode)
				require.NotNil(t, err.Error.Type)
				assert.Equal(t, "authentication_error", *err.Error.Type)
				require.NotNil(t, err.Error.Code)
				assert.Equal(t, "invalid_api_key", *err.Error.Code)
			},
		},
		{
			name: "empty response body",
			request: &schemas.BifrostOCRRequest{
				Model: "mistral-ocr-latest",
				Document: schemas.OCRDocument{
					Type:        schemas.OCRDocumentTypeDocumentURL,
					DocumentURL: schemas.Ptr("https://example.com/doc.pdf"),
				},
			},
			statusCode:    http.StatusOK,
			responseBody:  nil, // will send empty body
			expectError:   true,
			errorContains: "",
		},
		{
			name: "HTML error response",
			request: &schemas.BifrostOCRRequest{
				Model: "mistral-ocr-latest",
				Document: schemas.OCRDocument{
					Type:        schemas.OCRDocumentTypeDocumentURL,
					DocumentURL: schemas.Ptr("https://example.com/doc.pdf"),
				},
			},
			statusCode:    http.StatusOK,
			responseBody:  "html_error", // sentinel to trigger HTML response
			expectError:   true,
			errorContains: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "/v1/ocr", r.URL.Path)
				assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

				authHeader := r.Header.Get("Authorization")
				assert.Contains(t, authHeader, "Bearer")

				switch body := tt.responseBody.(type) {
				case nil:
					// Send empty body
				case string:
					if body == "html_error" {
						w.Header().Set("Content-Type", "text/html")
					}
				default:
					w.Header().Set("Content-Type", "application/json")
				}

				w.WriteHeader(tt.statusCode)

				switch body := tt.responseBody.(type) {
				case nil:
					// Send empty body
				case string:
					if body == "html_error" {
						w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
					}
				default:
					responseJSON, err := sonic.Marshal(body)
					if err != nil {
						t.Fatalf("failed to marshal response: %v", err)
					}
					w.Write(responseJSON)
				}
			}))
			defer server.Close()

			provider := NewMistralProvider(&schemas.ProviderConfig{
				NetworkConfig: schemas.NetworkConfig{
					BaseURL:                        server.URL,
					DefaultRequestTimeoutInSeconds: 30,
				},
			}, &testLogger{})

			ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			resp, err := provider.OCR(ctx, schemas.Key{Value: *schemas.NewEnvVar("test-api-key")}, tt.request)

			if tt.expectError {
				require.NotNil(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error.Message, tt.errorContains)
				}
				if tt.validateError != nil {
					tt.validateError(t, err)
				}
				return
			}

			require.Nil(t, err)
			require.NotNil(t, resp)
			tt.validateResult(t, resp)
		})
	}
}

// TestOCRNilInput tests handling of nil OCR request.
func TestOCRNilInput(t *testing.T) {
	t.Parallel()

	provider := NewMistralProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        "https://api.mistral.ai",
			DefaultRequestTimeoutInSeconds: 30,
		},
	}, &testLogger{})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	resp, err := provider.OCR(ctx, schemas.Key{Value: *schemas.NewEnvVar("test-key")}, nil)

	require.NotNil(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error.Message, "ocr request input is not provided")
}

// TestOCRRequestValidation tests that the mock server receives correctly serialized request bodies.
func TestOCRRequestValidation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Parse the request body to validate it was serialized correctly
		var mistralReq MistralOCRRequest
		err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&mistralReq)
		require.NoError(t, err)

		assert.Equal(t, "mistral-ocr-latest", mistralReq.Model)
		assert.Equal(t, "document_url", mistralReq.Document.Type)
		assert.Equal(t, "https://example.com/doc.pdf", mistralReq.Document.DocumentURL)
		assert.NotNil(t, mistralReq.IncludeImageBase64)
		assert.True(t, *mistralReq.IncludeImageBase64)
		assert.Equal(t, []int{0, 1}, mistralReq.Pages)

		// Return a valid response
		resp := MistralOCRResponse{
			Model: "mistral-ocr-latest",
			Pages: []MistralOCRPage{
				{Index: 0, Markdown: "Page 1"},
				{Index: 1, Markdown: "Page 2"},
			},
		}
		responseJSON, _ := sonic.Marshal(resp)
		w.WriteHeader(http.StatusOK)
		w.Write(responseJSON)
	}))
	defer server.Close()

	provider := NewMistralProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL,
			DefaultRequestTimeoutInSeconds: 30,
		},
	}, &testLogger{})

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	request := &schemas.BifrostOCRRequest{
		Model: "mistral-ocr-latest",
		Document: schemas.OCRDocument{
			Type:        schemas.OCRDocumentTypeDocumentURL,
			DocumentURL: schemas.Ptr("https://example.com/doc.pdf"),
		},
		Params: &schemas.OCRParameters{
			IncludeImageBase64: schemas.Ptr(true),
			Pages:              []int{0, 1},
		},
	}

	resp, err := provider.OCR(ctx, schemas.Key{Value: *schemas.NewEnvVar("test-api-key")}, request)

	require.Nil(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "mistral-ocr-latest", resp.Model)
	require.Len(t, resp.Pages, 2)
}

// TestMistralOCRIntegration tests the OCR endpoint with the real Mistral API.
// This test requires MISTRAL_API_KEY environment variable to be set.
// Run with: MISTRAL_API_KEY=xxx go test -v -run TestMistralOCRIntegration
func TestMistralOCRIntegration(t *testing.T) {
	apiKey := os.Getenv("MISTRAL_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping integration test: MISTRAL_API_KEY not set")
	}

	provider := NewMistralProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        "https://api.mistral.ai",
			DefaultRequestTimeoutInSeconds: 60,
		},
	}, &testLogger{})

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	request := &schemas.BifrostOCRRequest{
		Model: "mistral-ocr-latest",
		Document: schemas.OCRDocument{
			Type:        schemas.OCRDocumentTypeDocumentURL,
			DocumentURL: schemas.Ptr("https://arxiv.org/pdf/2201.04234"),
		},
		Params: &schemas.OCRParameters{
			Pages: []int{0},
		},
	}

	resp, bifrostErr := provider.OCR(ctx, schemas.Key{Value: *schemas.NewEnvVar(apiKey)}, request)

	require.Nil(t, bifrostErr, "OCR request failed: %v", bifrostErr)
	require.NotNil(t, resp)
	assert.Equal(t, "mistral-ocr-latest", resp.Model)
	require.NotEmpty(t, resp.Pages, "Expected at least one page")
	assert.Equal(t, 0, resp.Pages[0].Index)
	assert.NotEmpty(t, resp.Pages[0].Markdown, "Expected non-empty markdown for page 0")
	assert.Greater(t, resp.ExtraFields.Latency, int64(0))
}
