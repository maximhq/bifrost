package saladcloud_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/saladcloud"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestSaladcloud(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("SALAD_CLOUD_API_KEY")) == "" {
		t.Skip("Skipping Salad AI Gateway tests because SALAD_CLOUD_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:       schemas.SaladCloud,
		ChatModel:      "qwen3.6-35b-a3b",
		VisionModel:    "qwen3.6-35b-a3b",
		ReasoningModel: "qwen3.6-35b-a3b",
		Scenarios: llmtests.TestScenarios{
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			CompleteEnd2End:       true,
			ListModels:            true,
			StructuredOutputs:     true,
		},
	}

	t.Run("SaladCloudTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

func TestSaladCloudListModelsOnlyReturns35B(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" {
			t.Errorf("unexpected path: %s", request.URL.Path)
		}
		if authorization := request.Header.Get("Authorization"); authorization != "Bearer test-key" {
			t.Errorf("unexpected authorization header: %q", authorization)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(writer, `{"object":"list","data":[{"id":"qwen3.6-35b-a3b","object":"model","owned_by":"salad"},{"id":"qwen3.6-27b","object":"model","owned_by":"salad"},{"id":"qwen3.5-9b","object":"model","owned_by":"salad"}]}`)
	}))
	defer server.Close()

	provider, err := saladcloud.NewSaladCloudProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL,
			DefaultRequestTimeoutInSeconds: 10,
			AllowPrivateNetwork:            true,
		},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("NewSaladCloudProvider: %v", err)
	}

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	response, bifrostErr := provider.ListModels(
		ctx,
		[]schemas.Key{{
			Value:  *schemas.NewSecretVar("test-key"),
			Models: schemas.WhiteList{"*"},
		}},
		&schemas.BifrostListModelsRequest{Provider: schemas.SaladCloud, Unfiltered: true},
	)
	if bifrostErr != nil {
		t.Fatalf("ListModels: %v", bifrostErr)
	}
	if len(response.Data) != 1 {
		t.Fatalf("expected one listed model, got %d", len(response.Data))
	}
	if response.Data[0].ID != "saladcloud/qwen3.6-35b-a3b" {
		t.Fatalf("unexpected model ID: %s", response.Data[0].ID)
	}
}
