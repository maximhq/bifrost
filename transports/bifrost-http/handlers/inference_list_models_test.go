package handlers

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
)

func TestShouldReturnEmptyListModelsResponse(t *testing.T) {
	tests := []struct {
		name     string
		errCode  string
		expected bool
	}{
		{
			name:     "returns true for unsupported_operation",
			errCode:  "unsupported_operation",
			expected: true,
		},
		{
			name:     "returns false for other errors",
			errCode:  "internal_error",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &schemas.BifrostError{
				Error: &schemas.ErrorField{Code: &tt.errCode},
				ExtraFields: schemas.BifrostErrorExtraFields{
					RequestType: schemas.ListModelsRequest,
				},
			}

			assert.Equal(t, tt.expected, shouldReturnEmptyListModelsResponse(err))
		})
	}
}

func TestShouldSkipListModelsRequest(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		config   *schemas.CustomProviderConfig
		expected bool
	}{
		{
			name:     "skip when list models is disabled",
			provider: "openai",
			config: &schemas.CustomProviderConfig{
				AllowedRequests: &schemas.AllowedRequests{
					ListModels: false,
				},
			},
			expected: true,
		},
		{
			name:     "do not skip when list models is enabled",
			provider: "openai",
			config: &schemas.CustomProviderConfig{
				AllowedRequests: &schemas.AllowedRequests{
					ListModels: true,
				},
			},
			expected: false,
		},
		{
			name:     "do not skip when provider is empty",
			provider: "",
			config: &schemas.CustomProviderConfig{
				AllowedRequests: &schemas.AllowedRequests{
					ListModels: false,
				},
			},
			expected: false,
		},
		{
			name:     "do not skip when config is nil",
			provider: "openai",
			config:   nil,
			expected: false,
		},
		{
			name:     "do not skip when allowed requests is nil",
			provider: "openai",
			config:   &schemas.CustomProviderConfig{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, shouldSkipListModelsRequest(tt.provider, tt.config))
		})
	}
}

func TestBuildDisabledListModelsResponse(t *testing.T) {
	resp := buildDisabledListModelsResponse()

	assert.Equal(t, "The model_list request is disabled for this provider.", resp.Message)
	assert.NotNil(t, resp.Data)
	assert.Empty(t, resp.Data)
}
