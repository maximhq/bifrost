package bedrock

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func clearBedrockEndpointEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME", "")
	t.Setenv("AWS_ENDPOINT_URL_BEDROCK", "")
	t.Setenv("AWS_ENDPOINT_URL", "")
}

func TestRuntimeModelURLUsesNetworkConfigBaseURL(t *testing.T) {
	clearBedrockEndpointEnv(t)
	provider := &BedrockProvider{
		networkConfig: schemas.NetworkConfig{BaseURL: "https://bedrock-runtime.internal.example"},
	}

	got := provider.runtimeModelURL("us-east-1", "example-model/converse")
	want := "https://bedrock-runtime.internal.example/model/example-model/converse"
	if got != want {
		t.Fatalf("runtimeModelURL() = %q, want %q", got, want)
	}
	if strings.Contains(got, "amazonaws.com") {
		t.Fatalf("runtimeModelURL() unexpectedly used public AWS endpoint: %q", got)
	}
}

func TestRuntimeModelURLUsesPublicDefault(t *testing.T) {
	clearBedrockEndpointEnv(t)
	provider := &BedrockProvider{}

	got := provider.runtimeModelURL("eu-west-1", "example-model/converse")
	want := "https://bedrock-runtime.eu-west-1.amazonaws.com/model/example-model/converse"
	if got != want {
		t.Fatalf("runtimeModelURL() = %q, want %q", got, want)
	}
}

func TestRuntimeModelURLTrimsBaseURLTrailingSlash(t *testing.T) {
	clearBedrockEndpointEnv(t)
	provider := &BedrockProvider{
		networkConfig: schemas.NetworkConfig{BaseURL: "https://bedrock-runtime.internal.example/"},
	}

	got := provider.runtimeModelURL("us-east-1", "example-model/converse")
	want := "https://bedrock-runtime.internal.example/model/example-model/converse"
	if got != want {
		t.Fatalf("runtimeModelURL() = %q, want %q", got, want)
	}
}

func TestRuntimeModelURLUsesBedrockRuntimeEnvironmentEndpoint(t *testing.T) {
	clearBedrockEndpointEnv(t)
	t.Setenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME", "https://bedrock-runtime.env.example/")
	provider := &BedrockProvider{}

	got := provider.runtimeModelURL("us-east-1", "example-model/converse")
	want := "https://bedrock-runtime.env.example/model/example-model/converse"
	if got != want {
		t.Fatalf("runtimeModelURL() = %q, want %q", got, want)
	}
}

func TestRuntimeModelURLNetworkConfigBaseURLWinsOverEnvironment(t *testing.T) {
	clearBedrockEndpointEnv(t)
	t.Setenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME", "https://bedrock-runtime.env.example")
	provider := &BedrockProvider{
		networkConfig: schemas.NetworkConfig{BaseURL: "https://bedrock-runtime.config.example"},
	}

	got := provider.runtimeModelURL("us-east-1", "example-model/converse")
	want := "https://bedrock-runtime.config.example/model/example-model/converse"
	if got != want {
		t.Fatalf("runtimeModelURL() = %q, want %q", got, want)
	}
}
