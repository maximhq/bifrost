package openai

import (
	"bytes"
	"mime/multipart"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPayloadOrdering_OpenAIChatRequest(t *testing.T) {
	req := &OpenAIChatRequest{
		Model: "gpt-4o",
		Messages: []OpenAIMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")},
			},
		},
		ChatParameters: schemas.ChatParameters{
			Temperature: schemas.Ptr(0.7),
			Tools: []schemas.ChatTool{
				{
					Type: "function",
					Function: &schemas.ChatToolFunction{
						Name:        "get_weather",
						Description: schemas.Ptr("Get weather"),
						Parameters: &schemas.ToolFunctionParameters{
							Type: "object",
							Properties: schemas.NewOrderedMapFromPairs(
								schemas.KV("location", map[string]interface{}{"type": "string"}),
							),
							Required: []string{"location"},
						},
					},
				},
			},
			Reasoning: &schemas.ChatReasoning{
				Effort: schemas.Ptr("high"),
			},
		},
		Stream: schemas.Ptr(true),
	}

	result, err := providerUtils.MarshalSorted(req)
	require.NoError(t, err)

	golden := `{"model":"gpt-4o","temperature":0.7,"stream":true,"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}}}],"reasoning_effort":"high"}`

	assert.Equal(t, golden, string(result), "payload field ordering changed — if intentional, update the golden string")

	// Determinism: 100 iterations must produce identical bytes
	for i := 0; i < 100; i++ {
		iter, err := providerUtils.MarshalSorted(req)
		require.NoError(t, err)
		assert.Equal(t, string(result), string(iter), "non-deterministic marshal output on iteration %d", i)
	}
}

func TestParseImageEditFormDataBodyFromRequest_OrdersMetadataBeforeFiles(t *testing.T) {
	req := &OpenAIImageEditRequest{
		Model: "gpt-image-1",
		Input: &schemas.ImageEditInput{
			Prompt: "edit this",
			Images: []schemas.ImageInput{{Image: []byte("image-one")}, {Image: []byte("image-two")}},
		},
		ImageEditParameters: schemas.ImageEditParameters{
			N:                 schemas.Ptr(2),
			Size:              schemas.Ptr("1024x1024"),
			ResponseFormat:    schemas.Ptr("b64_json"),
			Quality:           schemas.Ptr("high"),
			Background:        schemas.Ptr("transparent"),
			InputFidelity:     schemas.Ptr("high"),
			PartialImages:     schemas.Ptr(1),
			OutputFormat:      schemas.Ptr("png"),
			OutputCompression: schemas.Ptr(80),
			User:              schemas.Ptr("user-123"),
			Mask:              []byte("mask-image"),
		},
		Stream: schemas.Ptr(true),
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.Nil(t, parseImageEditFormDataBodyFromRequest(writer, req, schemas.OpenAI))

	order := multipartPartOrder(t, writer.FormDataContentType(), body.Bytes())
	assert.Equal(t,
		[]string{"model", "prompt", "stream", "n", "size", "response_format", "quality", "background", "input_fidelity", "partial_images", "output_format", "output_compression", "user", "image[]", "image[]", "mask"},
		order,
	)
}

func TestParseImageVariationFormDataBodyFromRequest_OrdersMetadataBeforeFile(t *testing.T) {
	req := &OpenAIImageVariationRequest{
		Model: "gpt-image-1",
		Input: &schemas.ImageVariationInput{
			Image: schemas.ImageInput{Image: []byte("image-variation")},
		},
		ImageVariationParameters: schemas.ImageVariationParameters{
			N:              schemas.Ptr(3),
			ResponseFormat: schemas.Ptr("url"),
			Size:           schemas.Ptr("512x512"),
			User:           schemas.Ptr("user-456"),
		},
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.Nil(t, parseImageVariationFormDataBodyFromRequest(writer, req, schemas.OpenAI))

	order := multipartPartOrder(t, writer.FormDataContentType(), body.Bytes())
	assert.Equal(t,
		[]string{"model", "n", "response_format", "size", "user", "image"},
		order,
	)
}

