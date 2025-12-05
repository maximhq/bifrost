package gemini_test

import (
	"context"
	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/schemas"
	"testing"
)

func TestGeminiProvider_BatchCreate(t *testing.T) {
	tests := []struct {
		name string // description of this test case
		// Named input parameters for receiver constructor.
		config *schemas.ProviderConfig
		logger schemas.Logger
		// Named input parameters for target function.
		key     schemas.Key
		request *schemas.BifrostBatchCreateRequest
		want    *schemas.BifrostBatchCreateResponse
		want2   *schemas.BifrostError
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := gemini.NewGeminiProvider(tt.config, tt.logger)
			got, got2 := provider.BatchCreate(context.Background(), tt.key, tt.request)
			// TODO: update the condition below to compare got with tt.want.
			if true {
				t.Errorf("BatchCreate() = %v, want %v", got, tt.want)
			}
			if true {
				t.Errorf("BatchCreate() = %v, want %v", got2, tt.want2)
			}
		})
	}
}
