package replicate_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/replicate"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplicate(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("REPLICATE_API_KEY")) == "" {
		t.Skip("Skipping Replicate tests because REPLICATE_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:             schemas.Replicate,
		ChatModel:            "openai/gpt-4.1-mini",
		TextModel:            "openai/gpt-4.1-mini",
		ImageGenerationModel: "black-forest-labs/flux-dev",
		ImageEditModel:       "black-forest-labs/flux-dev",
		FileExtraParams: map[string]interface{}{
			"owner":  os.Getenv("REPLICATE_OWNER"),
			"expiry": 1830297599,
		},
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        true,
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: false,
			ToolCalls:             false,
			ToolCallsStreaming:    false,
			MultipleToolCalls:     false,
			End2EndToolCalling:    false,
			AutomaticFunctionCall: false,
			ImageURL:              false,
			ImageBase64:           false,
			ImageGeneration:       true,
			ImageEdit:             true,
			ImageEditStream:       true,
			ImageGenerationStream: true,
			FileBase64:            false,
			FileURL:               false,
			MultipleImages:        false,
			CompleteEnd2End:       false,
			Reasoning:             false,
			Embedding:             false,
			ListModels:            true,
			FileUpload:            true,
			FileList:              true,
			FileRetrieve:          true,
			FileDelete:            true,
			FileContent:           false,
		},
	}

	t.Run("ReplicateTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}

// TestBifrostToReplicateChatRequestConversion tests the conversion from Bifrost chat request to Replicate format
// with special handling based on model names
func TestBifrostToReplicateChatRequestConversion(t *testing.T) {
	maxTokens := 100
	temp := 0.7
	topP := 0.9
	seed := 42
	presencePenalty := 0.5
	frequencyPenalty := 0.3

	tests := []struct {
		name     string
		input    *schemas.BifrostChatRequest
		validate func(t *testing.T, result *replicate.ReplicatePredictionRequest)
		wantErr  bool
	}{
		{
			name: "OpenAI_Model_With_Messages",
			input: &schemas.BifrostChatRequest{
				Model: "openai/gpt-4.1-mini",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleSystem,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("You are a helpful assistant."),
						},
					},
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Hello!"),
						},
					},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				// OpenAI models should use Messages field
				assert.NotNil(t, result.Input.Messages)
				assert.Len(t, result.Input.Messages, 2)
				assert.Equal(t, schemas.ChatMessageRoleSystem, result.Input.Messages[0].Role)
				assert.Equal(t, schemas.ChatMessageRoleUser, result.Input.Messages[1].Role)
			},
		},
		{
			name: "OpenAI_Model_MaxCompletionTokens",
			input: &schemas.BifrostChatRequest{
				Model: "openai/gpt-4o",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Test"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: &maxTokens,
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				// OpenAI models should use MaxCompletionTokens field
				assert.NotNil(t, result.Input.MaxCompletionTokens)
				assert.Equal(t, maxTokens, *result.Input.MaxCompletionTokens)
				assert.Nil(t, result.Input.MaxTokens)
			},
		},
		{
			name: "Deepseek_Model_NoSystemPrompt",
			input: &schemas.BifrostChatRequest{
				Model: "deepseek-ai/deepseek-coder-33b-instruct",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleSystem,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("You are a helpful assistant."),
						},
					},
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Hello!"),
						},
					},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				// Deepseek models don't support system_prompt, it should be prepended to prompt
				assert.Nil(t, result.Input.SystemPrompt)
				assert.NotNil(t, result.Input.Prompt)
				// System prompt should be prepended to conversation
				assert.Contains(t, *result.Input.Prompt, "You are a helpful assistant.")
				assert.Contains(t, *result.Input.Prompt, "Hello!")
			},
		},
		{
			name: "Meta_Llama_NoSystemPrompt",
			input: &schemas.BifrostChatRequest{
				Model: "meta/meta-llama-3-8b",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleSystem,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Be concise."),
						},
					},
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("What is AI?"),
						},
					},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				// Meta llama models don't support system_prompt
				assert.Nil(t, result.Input.SystemPrompt)
				assert.NotNil(t, result.Input.Prompt)
				assert.Contains(t, *result.Input.Prompt, "Be concise.")
				assert.Contains(t, *result.Input.Prompt, "What is AI?")
			},
		},
		{
			name: "Regular_Model_WithSystemPrompt",
			input: &schemas.BifrostChatRequest{
				Model: "meta/llama-3.1-70b-instruct",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleSystem,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("You are helpful."),
						},
					},
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Hi there"),
						},
					},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				// Regular models support system_prompt
				assert.NotNil(t, result.Input.SystemPrompt)
				assert.Equal(t, "You are helpful.", *result.Input.SystemPrompt)
				assert.NotNil(t, result.Input.Prompt)
				assert.Equal(t, "Hi there", *result.Input.Prompt)
			},
		},
		{
			name: "Non_OpenAI_Model_MaxTokens",
			input: &schemas.BifrostChatRequest{
				Model: "meta/llama-3.1-70b-instruct",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Test"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: &maxTokens,
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				// Non-OpenAI models should use MaxTokens field
				assert.NotNil(t, result.Input.MaxTokens)
				assert.Equal(t, maxTokens, *result.Input.MaxTokens)
				assert.Nil(t, result.Input.MaxCompletionTokens)
			},
		},
		{
			name: "Model_With_Version_ID",
			input: &schemas.BifrostChatRequest{
				Model: "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Test version ID"),
						},
					},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				// Version ID should be set in Version field
				assert.NotNil(t, result.Version)
				assert.Equal(t, "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef", *result.Version)
			},
		},
		{
			name: "AllParameters",
			input: &schemas.BifrostChatRequest{
				Model: "meta/llama-3.1-70b-instruct",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Test all params"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: &maxTokens,
					Temperature:         &temp,
					TopP:                &topP,
					Seed:                &seed,
					PresencePenalty:     &presencePenalty,
					FrequencyPenalty:    &frequencyPenalty,
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				assert.Equal(t, maxTokens, *result.Input.MaxTokens)
				assert.Equal(t, temp, *result.Input.Temperature)
				assert.Equal(t, topP, *result.Input.TopP)
				assert.Equal(t, seed, *result.Input.Seed)
				assert.Equal(t, presencePenalty, *result.Input.PresencePenalty)
				assert.Equal(t, frequencyPenalty, *result.Input.FrequencyPenalty)
			},
		},
		{
			name: "MultipartContent_WithImageURL",
			input: &schemas.BifrostChatRequest{
				Model: "meta/llama-3.1-70b-instruct",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentBlocks: []schemas.ChatContentBlock{
								{
									Text: schemas.Ptr("Describe this image"),
								},
								{
									ImageURLStruct: &schemas.ChatInputImage{
										URL: "https://example.com/image.jpg",
									},
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				// Image URL should be added to ImageInput
				assert.NotNil(t, result.Input.ImageInput)
				assert.Len(t, result.Input.ImageInput, 1)
				assert.Equal(t, "https://example.com/image.jpg", result.Input.ImageInput[0])
				// Text should be in prompt
				assert.NotNil(t, result.Input.Prompt)
				assert.Equal(t, "Describe this image", *result.Input.Prompt)
			},
		},
		{
			name: "MultipartContent_Base64Images",
			input: &schemas.BifrostChatRequest{
				Model: "meta/llama-3.1-70b-instruct",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentBlocks: []schemas.ChatContentBlock{
								{
									Text: schemas.Ptr("Test"),
								},
								{
									ImageURLStruct: &schemas.ChatInputImage{
										URL: "data:image/png;base64,iVBORw0KGgoAAAANS",
									},
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				assert.NotNil(t, result.Input.ImageInput)
			},
		},
		{
			name: "ReasoningEffort",
			input: &schemas.BifrostChatRequest{
				Model: "meta/llama-3.1-70b-instruct",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Test reasoning"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("high"),
					},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				assert.NotNil(t, result.Input.ReasoningEffort)
				assert.Equal(t, "high", *result.Input.ReasoningEffort)
			},
		},
		{
			name:    "NilRequest",
			input:   nil,
			wantErr: true,
		},
		{
			name: "NilInput",
			input: &schemas.BifrostChatRequest{
				Model: "meta/llama-3.1-70b-instruct",
				Input: nil,
			},
			wantErr: true,
		},
		{
			name: "EmptyMessages",
			input: &schemas.BifrostChatRequest{
				Model: "meta/llama-3.1-70b-instruct",
				Input: []schemas.ChatMessage{},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, err := replicate.ToReplicateChatRequest(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, actual)
			} else {
				require.NoError(t, err)
				require.NotNil(t, actual)
				if tt.validate != nil {
					tt.validate(t, actual)
				}
			}
		})
	}
}

// TestBifrostToReplicateImageGenerationConversion tests the conversion from Bifrost image generation request
// to Replicate format with special handling for different model families
func TestBifrostToReplicateImageGenerationConversion(t *testing.T) {
	prompt := "A beautiful sunset"
	aspectRatio := "16:9"
	numImages := 2
	seed := 42
	negativePrompt := "blurry"
	numInferenceSteps := 50
	quality := "high"
	outputFormat := "png"
	background := "white"

	tests := []struct {
		name     string
		input    *schemas.BifrostImageGenerationRequest
		validate func(t *testing.T, result *replicate.ReplicatePredictionRequest)
		wantErr  bool
	}{
		{
			name: "Flux_1_1_Pro_ImagePrompt",
			input: &schemas.BifrostImageGenerationRequest{
				Model: "black-forest-labs/flux-1.1-pro",
				Input: &schemas.ImageGenerationInput{
					Prompt: prompt,
				},
				Params: &schemas.ImageGenerationParameters{
					InputImages: []string{"https://example.com/input.jpg"},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				// Flux 1.1 Pro should use ImagePrompt field
				assert.NotNil(t, result.Input.ImagePrompt)
				assert.Equal(t, "https://example.com/input.jpg", *result.Input.ImagePrompt)
				assert.Nil(t, result.Input.InputImage)
				assert.Nil(t, result.Input.Image)
				assert.Nil(t, result.Input.InputImages)
			},
		},
		{
			name: "Flux_1_1_Pro_Ultra_ImagePrompt",
			input: &schemas.BifrostImageGenerationRequest{
				Model: "black-forest-labs/flux-1.1-pro-ultra",
				Input: &schemas.ImageGenerationInput{
					Prompt: prompt,
				},
				Params: &schemas.ImageGenerationParameters{
					InputImages: []string{"https://example.com/input.jpg"},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				assert.NotNil(t, result.Input.ImagePrompt)
				assert.Equal(t, "https://example.com/input.jpg", *result.Input.ImagePrompt)
			},
		},
		{
			name: "Flux_Pro_ImagePrompt",
			input: &schemas.BifrostImageGenerationRequest{
				Model: "black-forest-labs/flux-pro",
				Input: &schemas.ImageGenerationInput{
					Prompt: prompt,
				},
				Params: &schemas.ImageGenerationParameters{
					InputImages: []string{"https://example.com/input.jpg"},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				assert.NotNil(t, result.Input.ImagePrompt)
				assert.Equal(t, "https://example.com/input.jpg", *result.Input.ImagePrompt)
			},
		},
		{
			name: "Flux_Kontext_Pro_InputImage",
			input: &schemas.BifrostImageGenerationRequest{
				Model: "black-forest-labs/flux-kontext-pro",
				Input: &schemas.ImageGenerationInput{
					Prompt: prompt,
				},
				Params: &schemas.ImageGenerationParameters{
					InputImages: []string{"https://example.com/input.jpg"},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				// Kontext models should use InputImage field
				assert.NotNil(t, result.Input.InputImage)
				assert.Equal(t, "https://example.com/input.jpg", *result.Input.InputImage)
				assert.Nil(t, result.Input.ImagePrompt)
				assert.Nil(t, result.Input.Image)
				assert.Nil(t, result.Input.InputImages)
			},
		},
		{
			name: "Flux_Kontext_Max_InputImage",
			input: &schemas.BifrostImageGenerationRequest{
				Model: "black-forest-labs/flux-kontext-max",
				Input: &schemas.ImageGenerationInput{
					Prompt: prompt,
				},
				Params: &schemas.ImageGenerationParameters{
					InputImages: []string{"https://example.com/input.jpg"},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				assert.NotNil(t, result.Input.InputImage)
				assert.Equal(t, "https://example.com/input.jpg", *result.Input.InputImage)
			},
		},
		{
			name: "Flux_Dev_Image",
			input: &schemas.BifrostImageGenerationRequest{
				Model: "black-forest-labs/flux-dev",
				Input: &schemas.ImageGenerationInput{
					Prompt: prompt,
				},
				Params: &schemas.ImageGenerationParameters{
					InputImages: []string{"https://example.com/input.jpg"},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				// Flux Dev should use Image field
				assert.NotNil(t, result.Input.Image)
				assert.Equal(t, "https://example.com/input.jpg", *result.Input.Image)
				assert.Nil(t, result.Input.ImagePrompt)
				assert.Nil(t, result.Input.InputImage)
				assert.Nil(t, result.Input.InputImages)
			},
		},
		{
			name: "Flux_Fill_Pro_Image",
			input: &schemas.BifrostImageGenerationRequest{
				Model: "black-forest-labs/flux-fill-pro",
				Input: &schemas.ImageGenerationInput{
					Prompt: prompt,
				},
				Params: &schemas.ImageGenerationParameters{
					InputImages: []string{"https://example.com/input.jpg"},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				assert.NotNil(t, result.Input.Image)
				assert.Equal(t, "https://example.com/input.jpg", *result.Input.Image)
			},
		},
		{
			name: "Other_Model_InputImages",
			input: &schemas.BifrostImageGenerationRequest{
				Model: "stability-ai/sdxl",
				Input: &schemas.ImageGenerationInput{
					Prompt: prompt,
				},
				Params: &schemas.ImageGenerationParameters{
					InputImages: []string{"https://example.com/input1.jpg", "https://example.com/input2.jpg"},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				// Other models should use InputImages array
				assert.NotNil(t, result.Input.InputImages)
				assert.Len(t, result.Input.InputImages, 2)
				assert.Equal(t, "https://example.com/input1.jpg", result.Input.InputImages[0])
				assert.Equal(t, "https://example.com/input2.jpg", result.Input.InputImages[1])
				assert.Nil(t, result.Input.ImagePrompt)
				assert.Nil(t, result.Input.InputImage)
				assert.Nil(t, result.Input.Image)
			},
		},
		{
			name: "Model_With_Version",
			input: &schemas.BifrostImageGenerationRequest{
				Model: "black-forest-labs/flux-1.1-pro:v1.0",
				Input: &schemas.ImageGenerationInput{
					Prompt: prompt,
				},
				Params: &schemas.ImageGenerationParameters{
					InputImages: []string{"https://example.com/input.jpg"},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				// Should still match flux-1.1-pro and use ImagePrompt
				assert.NotNil(t, result.Input.ImagePrompt)
				assert.Equal(t, "https://example.com/input.jpg", *result.Input.ImagePrompt)
			},
		},
		{
			name: "AllParameters",
			input: &schemas.BifrostImageGenerationRequest{
				Model: "black-forest-labs/flux-dev",
				Input: &schemas.ImageGenerationInput{
					Prompt: prompt,
				},
				Params: &schemas.ImageGenerationParameters{
					N:                 &numImages,
					AspectRatio:       &aspectRatio,
					Seed:              &seed,
					NegativePrompt:    &negativePrompt,
					NumInferenceSteps: &numInferenceSteps,
					Quality:           &quality,
					OutputFormat:      &outputFormat,
					Background:        &background,
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				assert.Equal(t, prompt, *result.Input.Prompt)
				assert.Equal(t, numImages, *result.Input.NumberOfImages)
				assert.Equal(t, aspectRatio, *result.Input.AspectRatio)
				assert.Equal(t, seed, *result.Input.Seed)
				assert.Equal(t, negativePrompt, *result.Input.NegativePrompt)
				assert.Equal(t, numInferenceSteps, *result.Input.NumInferenceStep)
				assert.Equal(t, quality, *result.Input.Quality)
				assert.Equal(t, outputFormat, *result.Input.OutputFormat)
				assert.Equal(t, background, *result.Input.Background)
			},
		},
		{
			name: "Version_ID",
			input: &schemas.BifrostImageGenerationRequest{
				Model: "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
				Input: &schemas.ImageGenerationInput{
					Prompt: prompt,
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				// Version ID should be set in Version field
				assert.NotNil(t, result.Version)
				assert.Equal(t, "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef", *result.Version)
			},
		},
		{
			name: "ExtraParams",
			input: &schemas.BifrostImageGenerationRequest{
				Model: "black-forest-labs/flux-dev",
				Input: &schemas.ImageGenerationInput{
					Prompt: prompt,
				},
				Params: &schemas.ImageGenerationParameters{
					ExtraParams: map[string]interface{}{
						"custom_param": "value",
					},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				assert.NotNil(t, result.Input.ExtraParams)
				assert.Equal(t, "value", result.Input.ExtraParams["custom_param"])
			},
		},
		{
			name:    "NilRequest",
			input:   nil,
			wantErr: false, // Function returns nil, not error
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				assert.Nil(t, result)
			},
		},
		{
			name: "NilInput",
			input: &schemas.BifrostImageGenerationRequest{
				Model: "black-forest-labs/flux-dev",
				Input: nil,
			},
			wantErr: false,
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				assert.Nil(t, result)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := replicate.ToReplicateImageGenerationInput(tt.input)
			if tt.wantErr {
				assert.Nil(t, actual)
			} else {
				if tt.validate != nil {
					tt.validate(t, actual)
				}
			}
		})
	}
}

// TestBifrostToReplicateResponsesRequestConversion tests the conversion from Bifrost responses request to Replicate format
func TestBifrostToReplicateResponsesRequestConversion(t *testing.T) {
	maxOutputTokens := 100
	temp := 0.7
	topP := 0.9
	reasoningEffort := "medium"
	instructions := "Be concise"

	tests := []struct {
		name     string
		input    *schemas.BifrostResponsesRequest
		validate func(t *testing.T, result *replicate.ReplicatePredictionRequest)
		wantErr  bool
	}{
		{
			name: "GPT5_Structured_With_InputItemList",
			input: &schemas.BifrostResponsesRequest{
				Model: "openai/gpt-5-structured",
				Input: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("Hello, how are you?"),
						},
					},
				},
				Params: &schemas.ResponsesParameters{
					Instructions:    &instructions,
					MaxOutputTokens: &maxOutputTokens,
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				// GPT-5 structured models should use InputItemList
				assert.NotNil(t, result.Input.InputItemList)
				assert.Len(t, result.Input.InputItemList, 1)
				assert.Equal(t, schemas.ResponsesInputMessageRoleUser, *result.Input.InputItemList[0].Role)
				assert.Equal(t, "Hello, how are you?", *result.Input.InputItemList[0].Content.ContentStr)
				// Check parameters
				assert.Equal(t, &instructions, result.Input.Instructions)
				assert.Equal(t, &maxOutputTokens, result.Input.MaxOutputTokens)
			},
		},
		{
			name: "GPT5_Structured_With_Tools",
			input: &schemas.BifrostResponsesRequest{
				Model: "openai/gpt-5-structured-preview",
				Input: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("What's the weather?"),
						},
					},
				},
				Params: &schemas.ResponsesParameters{
					Tools: []schemas.ResponsesTool{
						{
							Type:        schemas.ResponsesToolTypeFunction,
							Name:        schemas.Ptr("get_weather"),
							Description: schemas.Ptr("Get weather information"),
							ResponsesToolFunction: &schemas.ResponsesToolFunction{
								Parameters: &schemas.ToolFunctionParameters{
									Type: "object",
									Properties: schemas.NewOrderedMapFromPairs(
										schemas.KV("location", map[string]interface{}{
											"type": "string",
										}),
									),
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				assert.NotNil(t, result.Input.Tools)
				assert.Len(t, result.Input.Tools, 1)
				assert.Equal(t, "get_weather", *result.Input.Tools[0].Name)
				assert.NotNil(t, result.Input.InputItemList)
			},
		},
		{
			name: "OpenAI_Family_With_Messages",
			input: &schemas.BifrostResponsesRequest{
				Model: "openai/gpt-4o",
				Input: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("You are helpful."),
						},
					},
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("Hello!"),
						},
					},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				// OpenAI family (non-gpt5-structured) should use Messages
				assert.NotNil(t, result.Input.Messages)
				assert.Len(t, result.Input.Messages, 2)
				assert.Equal(t, schemas.ChatMessageRoleSystem, result.Input.Messages[0].Role)
				assert.Equal(t, schemas.ChatMessageRoleUser, result.Input.Messages[1].Role)
				assert.Nil(t, result.Input.InputItemList)
			},
		},
		{
			name: "NonOpenAI_Model_With_SystemPrompt",
			input: &schemas.BifrostResponsesRequest{
				Model: "meta/llama-3.1-70b-instruct",
				Input: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("Be helpful."),
						},
					},
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("Hi there!"),
						},
					},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				// Non-OpenAI models that support system_prompt
				assert.NotNil(t, result.Input.SystemPrompt)
				assert.Equal(t, "Be helpful.", *result.Input.SystemPrompt)
				assert.NotNil(t, result.Input.Prompt)
				assert.Equal(t, "Hi there!", *result.Input.Prompt)
			},
		},
		{
			name: "NonOpenAI_Model_NoSystemPrompt_Support",
			input: &schemas.BifrostResponsesRequest{
				Model: "deepseek-ai/deepseek-coder-33b-instruct",
				Input: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("You are a code assistant."),
						},
					},
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("Write a function."),
						},
					},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				// Deepseek models don't support system_prompt, should be prepended to prompt
				assert.Nil(t, result.Input.SystemPrompt)
				assert.NotNil(t, result.Input.Prompt)
				assert.Contains(t, *result.Input.Prompt, "You are a code assistant.")
				assert.Contains(t, *result.Input.Prompt, "Write a function.")
			},
		},
		{
			name: "ContentBlocks_With_Text",
			input: &schemas.BifrostResponsesRequest{
				Model: "meta/llama-3.1-70b-instruct",
				Input: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Text: schemas.Ptr("Part 1"),
								},
								{
									Text: schemas.Ptr("Part 2"),
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				assert.NotNil(t, result.Input.Prompt)
				// Text parts should be joined with newline
				assert.Equal(t, "Part 1\nPart 2", *result.Input.Prompt)
			},
		},
		{
			name: "ContentBlocks_With_ImageURL",
			input: &schemas.BifrostResponsesRequest{
				Model: "meta/llama-3.1-70b-instruct",
				Input: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Text: schemas.Ptr("Describe this"),
								},
								{
									ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
										ImageURL: schemas.Ptr("https://example.com/image.jpg"),
									},
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				// Non-base64 image URLs should be added to ImageInput
				assert.NotNil(t, result.Input.ImageInput)
				assert.Len(t, result.Input.ImageInput, 1)
				assert.Equal(t, "https://example.com/image.jpg", result.Input.ImageInput[0])
				assert.NotNil(t, result.Input.Prompt)
				assert.Equal(t, "Describe this", *result.Input.Prompt)
			},
		},
		{
			name: "ContentBlocks_Base64_Images",
			input: &schemas.BifrostResponsesRequest{
				Model: "meta/llama-3.1-70b-instruct",
				Input: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Text: schemas.Ptr("Test"),
								},
								{
									ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
										ImageURL: schemas.Ptr("data:image/png;base64,abc123"),
									},
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				assert.NotNil(t, result.Input.ImageInput)
			},
		},
		{
			name: "MultipleMessages_Assistant_User",
			input: &schemas.BifrostResponsesRequest{
				Model: "meta/llama-3.1-70b-instruct",
				Input: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("What is AI?"),
						},
					},
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("AI is artificial intelligence."),
						},
					},
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("Tell me more."),
						},
					},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				assert.NotNil(t, result.Input.Prompt)
				// All conversation parts should be joined
				assert.Contains(t, *result.Input.Prompt, "What is AI?")
				assert.Contains(t, *result.Input.Prompt, "AI is artificial intelligence.")
				assert.Contains(t, *result.Input.Prompt, "Tell me more.")
			},
		},
		{
			name: "Parameters_OpenAI_MaxCompletionTokens",
			input: &schemas.BifrostResponsesRequest{
				Model: "openai/gpt-4o",
				Input: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("Test"),
						},
					},
				},
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: &maxOutputTokens,
					Temperature:     &temp,
					TopP:            &topP,
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				// OpenAI models should use MaxCompletionTokens
				assert.NotNil(t, result.Input.MaxCompletionTokens)
				assert.Equal(t, maxOutputTokens, *result.Input.MaxCompletionTokens)
				assert.Nil(t, result.Input.MaxTokens)
				// Check other parameters
				assert.Equal(t, temp, *result.Input.Temperature)
				assert.Equal(t, topP, *result.Input.TopP)
			},
		},
		{
			name: "Parameters_NonOpenAI_MaxTokens",
			input: &schemas.BifrostResponsesRequest{
				Model: "meta/llama-3.1-70b-instruct",
				Input: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("Test"),
						},
					},
				},
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: &maxOutputTokens,
					Temperature:     &temp,
					TopP:            &topP,
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				// Non-OpenAI models should use MaxTokens
				assert.NotNil(t, result.Input.MaxTokens)
				assert.Equal(t, maxOutputTokens, *result.Input.MaxTokens)
				assert.Nil(t, result.Input.MaxCompletionTokens)
			},
		},
		{
			name: "ReasoningEffort",
			input: &schemas.BifrostResponsesRequest{
				Model: "meta/llama-3.1-70b-instruct",
				Input: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("Test reasoning"),
						},
					},
				},
				Params: &schemas.ResponsesParameters{
					Reasoning: &schemas.ResponsesParametersReasoning{
						Effort: &reasoningEffort,
					},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				assert.NotNil(t, result.Input.ReasoningEffort)
				assert.Equal(t, reasoningEffort, *result.Input.ReasoningEffort)
			},
		},
		{
			name: "Instructions_With_SystemPrompt_Support",
			input: &schemas.BifrostResponsesRequest{
				Model: "meta/llama-3.1-70b-instruct",
				Input: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("Hello"),
						},
					},
				},
				Params: &schemas.ResponsesParameters{
					Instructions: &instructions,
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				// Models that support system_prompt should use it for instructions
				assert.NotNil(t, result.Input.SystemPrompt)
				assert.Equal(t, instructions, *result.Input.SystemPrompt)
			},
		},
		{
			name: "Instructions_Without_SystemPrompt_Support",
			input: &schemas.BifrostResponsesRequest{
				Model: "deepseek-ai/deepseek-coder-33b-instruct",
				Input: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("Hello"),
						},
					},
				},
				Params: &schemas.ResponsesParameters{
					Instructions: &instructions,
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				// Models that don't support system_prompt should prepend instructions to prompt
				assert.Nil(t, result.Input.SystemPrompt)
				assert.NotNil(t, result.Input.Prompt)
				assert.Contains(t, *result.Input.Prompt, instructions)
				assert.Contains(t, *result.Input.Prompt, "Hello")
			},
		},
		{
			name: "Instructions_Prepended_To_Existing_Prompt",
			input: &schemas.BifrostResponsesRequest{
				Model: "deepseek-ai/deepseek-coder-33b-instruct",
				Input: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("Existing content"),
						},
					},
				},
				Params: &schemas.ResponsesParameters{
					Instructions: &instructions,
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				assert.NotNil(t, result.Input.Prompt)
				// Instructions should be prepended before existing content
				promptStr := *result.Input.Prompt
				instrIdx := strings.Index(promptStr, instructions)
				contentIdx := strings.Index(promptStr, "Existing content")
				assert.Less(t, instrIdx, contentIdx, "Instructions should come before content")
			},
		},
		{
			name: "Version_ID",
			input: &schemas.BifrostResponsesRequest{
				Model: "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
				Input: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("Test version"),
						},
					},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				// Version ID should be set in Version field
				assert.NotNil(t, result.Version)
				assert.Equal(t, "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef", *result.Version)
			},
		},
		{
			name: "EmptyContent_Messages_Skipped",
			input: &schemas.BifrostResponsesRequest{
				Model: "meta/llama-3.1-70b-instruct",
				Input: []schemas.ResponsesMessage{
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr(""),
						},
					},
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("Valid message"),
						},
					},
				},
			},
			validate: func(t *testing.T, result *replicate.ReplicatePredictionRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Input)
				assert.NotNil(t, result.Input.Prompt)
				// Only valid message should be present
				assert.Equal(t, "Valid message", *result.Input.Prompt)
			},
		},
		{
			name:    "NilRequest",
			input:   nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, err := replicate.ToReplicateResponsesRequest(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, actual)
			} else {
				require.NoError(t, err)
				require.NotNil(t, actual)
				if tt.validate != nil {
					tt.validate(t, actual)
				}
			}
		})
	}
}

// TestReplicateToBifrostResponsesResponse tests the conversion from Replicate prediction response to Bifrost responses format
func TestReplicateToBifrostResponsesResponse(t *testing.T) {
	predictionID := "test-prediction-123"
	model := "openai/gpt-4o"
	createdAt := "2024-01-25T12:00:00.000000Z"
	completedAt := "2024-01-25T12:00:05.000000Z"
	errorMsg := "Something went wrong"

	tests := []struct {
		name     string
		input    *replicate.ReplicatePredictionResponse
		validate func(t *testing.T, result *schemas.BifrostResponsesResponse)
	}{
		{
			name: "Successful_Response_OutputStr",
			input: &replicate.ReplicatePredictionResponse{
				ID:        predictionID,
				Model:     model,
				CreatedAt: createdAt,
				Status:    replicate.ReplicatePredictionStatusSucceeded,
				Output: &replicate.ReplicateOutput{
					OutputStr: schemas.Ptr("This is the response text."),
				},
				Logs: schemas.Ptr("Input token count: 10\nOutput token count: 20\nTotal token count: 30"),
			},
			validate: func(t *testing.T, result *schemas.BifrostResponsesResponse) {
				require.NotNil(t, result)
				assert.Equal(t, predictionID, *result.ID)
				assert.Equal(t, model, result.Model)
				assert.NotNil(t, result.Status)
				assert.Equal(t, "completed", *result.Status)
				// Check output messages
				require.NotNil(t, result.Output)
				require.Len(t, result.Output, 1)
				assert.Equal(t, schemas.ResponsesMessageTypeMessage, *result.Output[0].Type)
				assert.Equal(t, schemas.ResponsesInputMessageRoleAssistant, *result.Output[0].Role)
				assert.Equal(t, "This is the response text.", *result.Output[0].Content.ContentStr)
				// Check usage
				require.NotNil(t, result.Usage)
				assert.Equal(t, 10, result.Usage.InputTokens)
				assert.Equal(t, 20, result.Usage.OutputTokens)
				assert.Equal(t, 30, result.Usage.TotalTokens)
			},
		},
		{
			name: "Successful_Response_OutputArray",
			input: &replicate.ReplicatePredictionResponse{
				ID:        predictionID,
				Model:     model,
				CreatedAt: createdAt,
				Status:    replicate.ReplicatePredictionStatusSucceeded,
				Output: &replicate.ReplicateOutput{
					OutputArray: []string{"Part 1", " Part 2", " Part 3"},
				},
			},
			validate: func(t *testing.T, result *schemas.BifrostResponsesResponse) {
				require.NotNil(t, result)
				require.NotNil(t, result.Output)
				require.Len(t, result.Output, 1)
				// Array should be joined into a single string
				assert.Equal(t, "Part 1 Part 2 Part 3", *result.Output[0].Content.ContentStr)
			},
		},
		{
			name: "Successful_Response_OutputObject",
			input: &replicate.ReplicatePredictionResponse{
				ID:        predictionID,
				Model:     model,
				CreatedAt: createdAt,
				Status:    replicate.ReplicatePredictionStatusSucceeded,
				Output: &replicate.ReplicateOutput{
					OutputObject: &replicate.ReplicateOutputText{
						Text: schemas.Ptr("Object text content"),
					},
				},
			},
			validate: func(t *testing.T, result *schemas.BifrostResponsesResponse) {
				require.NotNil(t, result)
				require.NotNil(t, result.Output)
				require.Len(t, result.Output, 1)
				assert.Equal(t, "Object text content", *result.Output[0].Content.ContentStr)
			},
		},
		{
			name: "Failed_Response_With_Error",
			input: &replicate.ReplicatePredictionResponse{
				ID:        predictionID,
				Model:     model,
				CreatedAt: createdAt,
				Status:    replicate.ReplicatePredictionStatusFailed,
				Error:     &errorMsg,
			},
			validate: func(t *testing.T, result *schemas.BifrostResponsesResponse) {
				require.NotNil(t, result)
				assert.NotNil(t, result.Status)
				assert.Equal(t, "failed", *result.Status)
				// Check error
				require.NotNil(t, result.Error)
				assert.Equal(t, "provider_error", result.Error.Code)
				assert.Equal(t, errorMsg, result.Error.Message)
			},
		},
		{
			name: "Cancelled_Response",
			input: &replicate.ReplicatePredictionResponse{
				ID:        predictionID,
				Model:     model,
				CreatedAt: createdAt,
				Status:    replicate.ReplicatePredictionStatusCanceled,
			},
			validate: func(t *testing.T, result *schemas.BifrostResponsesResponse) {
				require.NotNil(t, result)
				assert.NotNil(t, result.Status)
				assert.Equal(t, "cancelled", *result.Status)
			},
		},
		{
			name: "InProgress_Response",
			input: &replicate.ReplicatePredictionResponse{
				ID:        predictionID,
				Model:     model,
				CreatedAt: createdAt,
				Status:    replicate.ReplicatePredictionStatusProcessing,
			},
			validate: func(t *testing.T, result *schemas.BifrostResponsesResponse) {
				require.NotNil(t, result)
				assert.NotNil(t, result.Status)
				assert.Equal(t, "in_progress", *result.Status)
			},
		},
		{
			name: "Queued_Response",
			input: &replicate.ReplicatePredictionResponse{
				ID:        predictionID,
				Model:     model,
				CreatedAt: createdAt,
				Status:    replicate.ReplicatePredictionStatusStarting,
			},
			validate: func(t *testing.T, result *schemas.BifrostResponsesResponse) {
				require.NotNil(t, result)
				assert.NotNil(t, result.Status)
				assert.Equal(t, "queued", *result.Status)
			},
		},
		{
			name: "Response_With_CompletedAt",
			input: &replicate.ReplicatePredictionResponse{
				ID:          predictionID,
				Model:       model,
				CreatedAt:   createdAt,
				CompletedAt: &completedAt,
				Status:      replicate.ReplicatePredictionStatusSucceeded,
				Output: &replicate.ReplicateOutput{
					OutputStr: schemas.Ptr("Done"),
				},
			},
			validate: func(t *testing.T, result *schemas.BifrostResponsesResponse) {
				require.NotNil(t, result)
				assert.NotZero(t, result.CreatedAt)
				assert.NotNil(t, result.CompletedAt)
				assert.NotZero(t, *result.CompletedAt)
			},
		},
		{
			name: "Response_With_Partial_Usage",
			input: &replicate.ReplicatePredictionResponse{
				ID:        predictionID,
				Model:     model,
				CreatedAt: createdAt,
				Status:    replicate.ReplicatePredictionStatusSucceeded,
				Output: &replicate.ReplicateOutput{
					OutputStr: schemas.Ptr("Response"),
				},
				Logs: schemas.Ptr("Input token count: 15\nOutput token count: 0"),
			},
			validate: func(t *testing.T, result *schemas.BifrostResponsesResponse) {
				require.NotNil(t, result)
				require.NotNil(t, result.Usage)
				assert.Equal(t, 15, result.Usage.InputTokens)
				assert.Equal(t, 0, result.Usage.OutputTokens)
				assert.Equal(t, 15, result.Usage.TotalTokens)
			},
		},
		{
			name: "Empty_Output_Content",
			input: &replicate.ReplicatePredictionResponse{
				ID:        predictionID,
				Model:     model,
				CreatedAt: createdAt,
				Status:    replicate.ReplicatePredictionStatusSucceeded,
				Output: &replicate.ReplicateOutput{
					OutputStr: schemas.Ptr(""),
				},
			},
			validate: func(t *testing.T, result *schemas.BifrostResponsesResponse) {
				require.NotNil(t, result)
				// Empty content should not create output messages
				assert.Empty(t, result.Output)
			},
		},
		{
			name: "No_Output",
			input: &replicate.ReplicatePredictionResponse{
				ID:        predictionID,
				Model:     model,
				CreatedAt: createdAt,
				Status:    replicate.ReplicatePredictionStatusProcessing,
				Output:    nil,
			},
			validate: func(t *testing.T, result *schemas.BifrostResponsesResponse) {
				require.NotNil(t, result)
				assert.Empty(t, result.Output)
			},
		},
		{
			name: "Empty_Error_Not_Set",
			input: &replicate.ReplicatePredictionResponse{
				ID:        predictionID,
				Model:     model,
				CreatedAt: createdAt,
				Status:    replicate.ReplicatePredictionStatusFailed,
				Error:     schemas.Ptr(""),
			},
			validate: func(t *testing.T, result *schemas.BifrostResponsesResponse) {
				require.NotNil(t, result)
				// Empty error string should not set error field
				assert.Nil(t, result.Error)
			},
		},
		{
			name:  "Nil_Response",
			input: nil,
			validate: func(t *testing.T, result *schemas.BifrostResponsesResponse) {
				assert.Nil(t, result)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := tt.input.ToBifrostResponsesResponse()
			if tt.validate != nil {
				tt.validate(t, actual)
			}
		})
	}
}
