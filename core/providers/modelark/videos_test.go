package modelark

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeVideoReq(prompt string, inputReference *string, params *schemas.VideoGenerationParameters) *schemas.BifrostVideoGenerationRequest {
	return &schemas.BifrostVideoGenerationRequest{
		Model: "seedance-1-0-pro-fast-251015",
		Input: &schemas.VideoGenerationInput{
			Prompt:         prompt,
			InputReference: inputReference,
		},
		Params: params,
	}
}

func TestToModelArkVideoGenerationRequest(t *testing.T) {
	t.Run("text_to_video_sends_single_text_part", func(t *testing.T) {
		result, err := ToModelArkVideoGenerationRequest(makeVideoReq("a cat surfing", nil, nil))
		require.NoError(t, err)
		assert.Equal(t, "seedance-1-0-pro-fast-251015", result.Model)
		require.Len(t, result.Content, 1)
		assert.Equal(t, "text", result.Content[0].Type)
		assert.Equal(t, "a cat surfing", result.Content[0].Text)
		assert.Nil(t, result.Content[0].ImageURL)
		require.NotNil(t, result.Ratio)
		assert.Equal(t, "adaptive", *result.Ratio)
	})

	t.Run("image_to_video_appends_image_part", func(t *testing.T) {
		result, err := ToModelArkVideoGenerationRequest(makeVideoReq("a cat surfing", schemas.Ptr("https://example.com/cat.jpg"), nil))
		require.NoError(t, err)
		require.Len(t, result.Content, 2)
		assert.Equal(t, "text", result.Content[0].Type)
		assert.Equal(t, "image_url", result.Content[1].Type)
		require.NotNil(t, result.Content[1].ImageURL)
		assert.Equal(t, "https://example.com/cat.jpg", result.Content[1].ImageURL.URL)
	})

	t.Run("image_to_video_accepts_data_uri", func(t *testing.T) {
		dataURI := "data:image/jpeg;base64,/9j/4AAQSkZJRg=="
		result, err := ToModelArkVideoGenerationRequest(makeVideoReq("", schemas.Ptr(dataURI), nil))
		require.NoError(t, err)
		require.Len(t, result.Content, 1, "an empty prompt must not produce a text part")
		assert.Equal(t, "image_url", result.Content[0].Type)
		require.NotNil(t, result.Content[0].ImageURL)
		assert.Equal(t, dataURI, result.Content[0].ImageURL.URL)
	})

	t.Run("nil_request_errors", func(t *testing.T) {
		_, err := ToModelArkVideoGenerationRequest(nil)
		require.Error(t, err)
	})

	t.Run("nil_input_errors", func(t *testing.T) {
		_, err := ToModelArkVideoGenerationRequest(&schemas.BifrostVideoGenerationRequest{Model: "seedance-1-0-pro-fast-251015"})
		require.Error(t, err)
	})

	t.Run("empty_prompt_without_image_errors", func(t *testing.T) {
		_, err := ToModelArkVideoGenerationRequest(makeVideoReq("", nil, nil))
		require.Error(t, err)
	})

	t.Run("invalid_input_reference_errors", func(t *testing.T) {
		_, err := ToModelArkVideoGenerationRequest(makeVideoReq("a cat", schemas.Ptr("ftp://example.com/cat.jpg"), nil))
		require.Error(t, err)
	})

	t.Run("invalid_seconds_errors", func(t *testing.T) {
		_, err := ToModelArkVideoGenerationRequest(makeVideoReq("a cat", nil, &schemas.VideoGenerationParameters{
			Seconds: schemas.Ptr("six"),
		}))
		require.Error(t, err)
	})

	t.Run("standard_params_mapped", func(t *testing.T) {
		result, err := ToModelArkVideoGenerationRequest(makeVideoReq("a cat", nil, &schemas.VideoGenerationParameters{
			Seconds: schemas.Ptr("6"),
			Size:    "1280x720",
			Seed:    schemas.Ptr(42),
			Audio:   schemas.Ptr(true),
		}))
		require.NoError(t, err)
		require.NotNil(t, result.Duration)
		assert.Equal(t, 6, *result.Duration)
		require.NotNil(t, result.Resolution)
		assert.Equal(t, "720p", *result.Resolution)
		require.NotNil(t, result.Seed)
		assert.Equal(t, 42, *result.Seed)
		require.NotNil(t, result.GenerateAudio)
		assert.True(t, *result.GenerateAudio)
	})

	t.Run("extra_params_lifted_into_typed_fields", func(t *testing.T) {
		result, err := ToModelArkVideoGenerationRequest(makeVideoReq("a cat", nil, &schemas.VideoGenerationParameters{
			ExtraParams: map[string]interface{}{
				"ratio":        "16:9",
				"watermark":    true,
				"callback_url": "https://example.com/hook",
				"draft":        true,
			},
		}))
		require.NoError(t, err)
		require.NotNil(t, result.Ratio)
		assert.Equal(t, "16:9", *result.Ratio)
		require.NotNil(t, result.Watermark)
		assert.True(t, *result.Watermark)
		require.NotNil(t, result.CallbackURL)
		assert.Equal(t, "https://example.com/hook", *result.CallbackURL)
		assert.NotContains(t, result.ExtraParams, "ratio", "lifted keys must not be sent twice")
		assert.NotContains(t, result.ExtraParams, "watermark")
		assert.NotContains(t, result.ExtraParams, "callback_url")
		assert.Contains(t, result.ExtraParams, "draft", "unrecognised keys pass through to ModelArk")
	})
}

func TestResolutionFromSize(t *testing.T) {
	tests := []struct {
		name   string
		size   string
		want   string
		wantOK bool
	}{
		{"width_by_height", "1280x720", "720p", true},
		{"uppercase_separator", "1920X1080", "1080p", true},
		{"already_a_tier", "480p", "480p", true},
		{"empty", "", "", false},
		{"no_separator", "square", "", false},
		{"non_numeric_height", "1280xtall", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := resolutionFromSize(tt.size)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestToBifrostVideoGenerationResponse(t *testing.T) {
	t.Run("nil_task_details_errors", func(t *testing.T) {
		_, err := ToBifrostVideoGenerationResponse(nil)
		require.NotNil(t, err)
	})

	t.Run("maps_status", func(t *testing.T) {
		tests := []struct {
			name   string
			status ModelArkTaskStatus
			want   schemas.VideoStatus
		}{
			{"queued", ModelArkTaskStatusQueued, schemas.VideoStatusQueued},
			{"running", ModelArkTaskStatusRunning, schemas.VideoStatusInProgress},
			{"succeeded", ModelArkTaskStatusSucceeded, schemas.VideoStatusCompleted},
			{"failed", ModelArkTaskStatusFailed, schemas.VideoStatusFailed},
			{"cancelled", ModelArkTaskStatusCancelled, schemas.VideoStatusFailed},
			{"unknown", ModelArkTaskStatus("throttled"), schemas.VideoStatusQueued},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				resp, err := ToBifrostVideoGenerationResponse(&ModelArkTaskDetailsResponse{
					ID:     "cgt-20260803171320-r6rlg",
					Status: tt.status,
				})
				require.Nil(t, err)
				assert.Equal(t, tt.want, resp.Status)
			})
		}
	})

	t.Run("maps_completed_task", func(t *testing.T) {
		resp, err := ToBifrostVideoGenerationResponse(&ModelArkTaskDetailsResponse{
			ID:         "cgt-20260803171320-r6rlg",
			Model:      "seedance-1-0-pro-fast-251015",
			Status:     ModelArkTaskStatusSucceeded,
			Content:    &ModelArkTaskContent{VideoURL: "https://example.com/out.mp4?X-Tos-Signature=abc"},
			CreatedAt:  1754241200,
			UpdatedAt:  1754241320,
			Duration:   6,
			Resolution: "720p",
		})
		require.Nil(t, err)
		assert.Equal(t, "cgt-20260803171320-r6rlg", resp.ID)
		assert.Equal(t, "seedance-1-0-pro-fast-251015", resp.Model)
		assert.Equal(t, "video", resp.Object)
		assert.Equal(t, int64(1754241200), resp.CreatedAt)
		require.NotNil(t, resp.CompletedAt)
		assert.Equal(t, int64(1754241320), *resp.CompletedAt)
		require.NotNil(t, resp.Seconds)
		assert.Equal(t, "6", *resp.Seconds)
		assert.Equal(t, "720p", resp.Size)
		require.Len(t, resp.Videos, 1)
		assert.Equal(t, schemas.VideoOutputTypeURL, resp.Videos[0].Type)
		require.NotNil(t, resp.Videos[0].URL)
		assert.Equal(t, "https://example.com/out.mp4?X-Tos-Signature=abc", *resp.Videos[0].URL)
		assert.Equal(t, "video/mp4", resp.Videos[0].ContentType)
		assert.Nil(t, resp.Error)
	})

	t.Run("running_task_has_no_completion_timestamp", func(t *testing.T) {
		resp, err := ToBifrostVideoGenerationResponse(&ModelArkTaskDetailsResponse{
			ID:        "cgt-20260803171320-r6rlg",
			Status:    ModelArkTaskStatusRunning,
			UpdatedAt: 1754241320,
		})
		require.Nil(t, err)
		assert.Nil(t, resp.CompletedAt)
		assert.Empty(t, resp.Videos)
	})

	t.Run("failed_task_surfaces_provider_error", func(t *testing.T) {
		resp, err := ToBifrostVideoGenerationResponse(&ModelArkTaskDetailsResponse{
			ID:     "cgt-20260803171320-r6rlg",
			Status: ModelArkTaskStatusFailed,
			Error: &ModelArkError{
				Code:    "ModelNotOpen",
				Message: "The model is not activated for this account.",
			},
		})
		require.Nil(t, err)
		require.NotNil(t, resp.Error)
		assert.Equal(t, "ModelNotOpen", resp.Error.Code)
		assert.Equal(t, "The model is not activated for this account.", resp.Error.Message)
	})

	t.Run("failed_task_without_error_payload_falls_back_to_status", func(t *testing.T) {
		resp, err := ToBifrostVideoGenerationResponse(&ModelArkTaskDetailsResponse{
			ID:     "cgt-20260803171320-r6rlg",
			Status: ModelArkTaskStatusCancelled,
		})
		require.Nil(t, err)
		require.NotNil(t, resp.Error)
		assert.Equal(t, "cancelled", resp.Error.Code)
		assert.Equal(t, "Task cancelled", resp.Error.Message)
	})
}
