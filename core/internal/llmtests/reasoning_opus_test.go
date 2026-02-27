package llmtests

import (
	"testing"
)

// TestOpusReasoningAnthropicOpus45 tests Opus 4.5 extended thinking via direct Anthropic API
func TestOpusReasoningAnthropicOpus45(t *testing.T) {
	t.Parallel()
	t.Skip("Skipping Opus 4.5 reasoning test - requires valid API keys and model access")

	client, ctx, cancel, err := SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	configs := GetOpusReasoningTestConfigs()
	for _, config := range configs {
		if config.Provider == "anthropic" {
			RunOpus45ReasoningTest(t, client, ctx, config)
			return
		}
	}
	t.Skip("No Anthropic config found")
}

// TestOpusReasoningAnthropicOpus46 tests Opus 4.6 adaptive thinking via direct Anthropic API
func TestOpusReasoningAnthropicOpus46(t *testing.T) {
	t.Parallel()
	t.Skip("Skipping Opus 4.6 reasoning test - requires valid API keys and model access")

	client, ctx, cancel, err := SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	configs := GetOpusReasoningTestConfigs()
	for _, config := range configs {
		if config.Provider == "anthropic" {
			RunOpus46ReasoningTest(t, client, ctx, config)
			return
		}
	}
	t.Skip("No Anthropic config found")
}

// TestOpusReasoningBedrockOpus45 tests Opus 4.5 extended thinking via AWS Bedrock
func TestOpusReasoningBedrockOpus45(t *testing.T) {
	t.Parallel()
	t.Skip("Skipping Opus 4.5 reasoning test - requires valid AWS credentials and model access")

	client, ctx, cancel, err := SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	configs := GetOpusReasoningTestConfigs()
	for _, config := range configs {
		if config.Provider == "bedrock" {
			RunOpus45ReasoningTest(t, client, ctx, config)
			return
		}
	}
	t.Skip("No Bedrock config found")
}

// TestOpusReasoningBedrockOpus46 tests Opus 4.6 adaptive thinking via AWS Bedrock
func TestOpusReasoningBedrockOpus46(t *testing.T) {
	t.Parallel()
	t.Skip("Skipping Opus 4.6 reasoning test - requires valid AWS credentials and model access")

	client, ctx, cancel, err := SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	configs := GetOpusReasoningTestConfigs()
	for _, config := range configs {
		if config.Provider == "bedrock" {
			RunOpus46ReasoningTest(t, client, ctx, config)
			return
		}
	}
	t.Skip("No Bedrock config found")
}

// TestOpusReasoningAzureOpus45 tests Opus 4.5 extended thinking via Azure
func TestOpusReasoningAzureOpus45(t *testing.T) {
	t.Parallel()
	t.Skip("Skipping Opus 4.5 reasoning test - requires valid Azure credentials and model deployment")

	client, ctx, cancel, err := SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	configs := GetOpusReasoningTestConfigs()
	for _, config := range configs {
		if config.Provider == "azure" {
			RunOpus45ReasoningTest(t, client, ctx, config)
			return
		}
	}
	t.Skip("No Azure config found")
}

// TestOpusReasoningAzureOpus46 tests Opus 4.6 adaptive thinking via Azure
func TestOpusReasoningAzureOpus46(t *testing.T) {
	t.Parallel()
	t.Skip("Skipping Opus 4.6 reasoning test - requires valid Azure credentials and model deployment")

	client, ctx, cancel, err := SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	configs := GetOpusReasoningTestConfigs()
	for _, config := range configs {
		if config.Provider == "azure" {
			RunOpus46ReasoningTest(t, client, ctx, config)
			return
		}
	}
	t.Skip("No Azure config found")
}

// TestOpusReasoningVertexOpus45 tests Opus 4.5 extended thinking via Google Vertex AI
func TestOpusReasoningVertexOpus45(t *testing.T) {
	t.Parallel()
	t.Skip("Skipping Opus 4.5 reasoning test - requires valid Vertex credentials and model access")

	client, ctx, cancel, err := SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	configs := GetOpusReasoningTestConfigs()
	for _, config := range configs {
		if config.Provider == "vertex" {
			RunOpus45ReasoningTest(t, client, ctx, config)
			return
		}
	}
	t.Skip("No Vertex config found")
}

// TestOpusReasoningVertexOpus46 tests Opus 4.6 adaptive thinking via Google Vertex AI
func TestOpusReasoningVertexOpus46(t *testing.T) {
	t.Parallel()
	t.Skip("Skipping Opus 4.6 reasoning test - requires valid Vertex credentials and model access")

	client, ctx, cancel, err := SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	configs := GetOpusReasoningTestConfigs()
	for _, config := range configs {
		if config.Provider == "vertex" {
			RunOpus46ReasoningTest(t, client, ctx, config)
			return
		}
	}
	t.Skip("No Vertex config found")
}

// TestAllOpusReasoning runs all Opus reasoning tests for all providers
// This is a comprehensive test that can be un-skipped for integration testing
func TestAllOpusReasoning(t *testing.T) {
	t.Parallel()
	t.Skip("Skipping all Opus reasoning tests - requires valid credentials for all providers")

	client, ctx, cancel, err := SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	configs := GetOpusReasoningTestConfigs()
	for _, config := range configs {
		RunAllOpusReasoningTests(t, client, ctx, config)
	}
}
