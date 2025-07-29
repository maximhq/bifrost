package api

import (
	"encoding/json"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

var validProviders = map[schemas.ModelProvider]bool{
	schemas.OpenAI:    true,
	schemas.Anthropic: true,
	schemas.Bedrock:   true,
	schemas.Cohere:    true,
	schemas.Vertex:    true,
	schemas.Mistral:   true,
	schemas.Ollama:    true,
}

// ParseModelString extracts provider and model from a model string.
// For model strings like "anthropic/claude", it returns ("anthropic", "claude").
// For model strings like "claude", it returns ("", "claude").
// If the extracted provider is not valid, it treats the whole string as a model name.
func ParseModelString(model string, defaultProvider schemas.ModelProvider) (schemas.ModelProvider, string) {
	// Check if model contains a provider prefix (only split on first "/" to preserve model names with "/")
	if strings.Contains(model, "/") {
		parts := strings.SplitN(model, "/", 2)
		if len(parts) == 2 {
			extractedProvider := parts[0]
			extractedModel := parts[1]

			// Validate that the extracted provider is actually a valid provider
			if validProviders[schemas.ModelProvider(extractedProvider)] {
				return schemas.ModelProvider(extractedProvider), extractedModel
			}
			// If extracted provider is not valid, treat the whole string as model name
			// This prevents corrupting model names that happen to contain "/"
		}
	}
	// No provider prefix found or invalid provider, return empty provider and the original model
	return defaultProvider, model
}

// isOpenAIModel checks for OpenAI model patterns
func IsOpenAIModel(model string) bool {
	// Exclude Azure models to prevent overlap
	if strings.Contains(model, "azure/") {
		return false
	}

	openaiPatterns := []string{
		"gpt", "davinci", "curie", "babbage", "ada", "o1", "o3", "o4",
		"text-embedding", "dall-e", "whisper", "tts", "chatgpt",
	}

	return matchesAnyPattern(model, openaiPatterns)
}

// isAzureModel checks for Azure OpenAI specific patterns
func IsAzureModel(model string) bool {
	azurePatterns := []string{
		"azure", "model-router", "computer-use-preview",
	}

	return matchesAnyPattern(model, azurePatterns)
}

// isAnthropicModel checks for Anthropic Claude model patterns
func IsAnthropicModel(model string) bool {
	anthropicPatterns := []string{
		"claude", "anthropic/",
	}

	return matchesAnyPattern(model, anthropicPatterns)
}

// isVertexModel checks for Google Vertex AI model patterns
func IsVertexModel(model string) bool {
	vertexPatterns := []string{
		"gemini", "palm", "bison", "gecko", "vertex/", "google/",
	}

	return matchesAnyPattern(model, vertexPatterns)
}

// isBedrockModel checks for AWS Bedrock model patterns
func IsBedrockModel(model string) bool {
	bedrockPatterns := []string{
		"bedrock", "bedrock.amazonaws.com/", "bedrock/",
		"amazon.titan", "amazon.nova", "aws/amazon.",
		"ai21.jamba", "ai21.j2", "aws/ai21.",
		"meta.llama", "aws/meta.",
		"stability.stable-diffusion", "stability.sd3", "aws/stability.",
		"anthropic.claude", "aws/anthropic.",
		"cohere.command", "cohere.embed", "aws/cohere.",
		"mistral.mistral", "mistral.mixtral", "aws/mistral.",
		"titan-text", "titan-embed", "nova-micro", "nova-lite", "nova-pro",
		"jamba-instruct", "j2-ultra", "j2-mid",
		"llama-2", "llama-3", "llama-3.1", "llama-3.2",
		"stable-diffusion-xl", "sd3-large",
	}

	return matchesAnyPattern(model, bedrockPatterns)
}

// isCohereModel checks for Cohere model patterns
func IsCohereModel(model string) bool {
	coherePatterns := []string{
		"command-", "embed-", "cohere",
	}

	return matchesAnyPattern(model, coherePatterns)
}

// matchesAnyPattern checks if the model matches any of the given patterns
func matchesAnyPattern(model string, patterns []string) bool {
	for _, pattern := range patterns {
		if strings.Contains(model, pattern) {
			return true
		}
	}
	return false
}

// Helper function to convert interface{} to JSON string
func JsonifyInput(input interface{}) string {
	if input == nil {
		return "{}"
	}
	jsonBytes, err := json.Marshal(input)
	if err != nil {
		return "{}"
	}
	return string(jsonBytes)
}

func Ptr[T any](v T) *T {
	return &v
}