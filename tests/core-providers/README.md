# Bifrost Core Providers Test Suite 🚀

This directory contains comprehensive tests for all Bifrost AI providers, ensuring compatibility and functionality across different AI services.

## 📋 Supported Providers

- **OpenAI** - GPT models and function calling
- **Anthropic** - Claude models
- **Azure OpenAI** - Azure-hosted OpenAI models
- **AWS Bedrock** - Amazon's managed AI service
- **Cohere** - Cohere's language models
- **Google Vertex AI** - Google Cloud's AI platform
- **Mistral** - Mistral AI models with vision capabilities
- **Ollama** - Local LLM serving platform
- **Groq** - OSS models
- **SGLang** - OSS models
- **Parasail** - OSS models
- **Cerebras** - Llama, Qwen and GPT-OSS models
- **Gemini** - Gemini models
- **OpenRouter** - Models supported by OpenRouter

## 🏃‍♂️ Running Tests

### Development with Local Bifrost Core

To test changes with a forked or local version of bifrost-core:

1. **Uncomment the replace directive** in `tests/core-providers/go.mod`:

   ```go
   // Uncomment this line to use your local bifrost-core
   replace github.com/maximhq/bifrost/core => ../../core
   ```

2. **Update dependencies**:

   ```bash
   cd tests/core-providers
   go mod tidy
   ```

3. **Run tests** with your local changes:

   ```bash
   go test -v ./tests/core-providers/
   ```

⚠️ **Important**: Ensure your local `../../core` directory contains your bifrost-core implementation. The path should be relative to the `tests/core-providers` directory.

### Prerequisites

Set up environment variables for the providers you want to test:

```bash
# OpenAI
export OPENAI_API_KEY="your-openai-key"

# Anthropic
export ANTHROPIC_API_KEY="your-anthropic-key"

# Azure OpenAI
export AZURE_API_KEY="your-azure-key"
export AZURE_ENDPOINT="your-azure-endpoint"

# AWS Bedrock
export AWS_ACCESS_KEY_ID_ID="your-aws-access-key"
export AWS_SECRET_ACCESS_KEY="your-aws-secret-key"
export AWS_REGION="us-east-1"

# Cohere
export COHERE_API_KEY="your-cohere-key"

# Google Vertex AI
export GOOGLE_APPLICATION_CREDENTIALS="path/to/service-account.json"
export GOOGLE_PROJECT_ID="your-project-id"

# Mistral AI
export MISTRAL_API_KEY="your-mistral-key"

# Gemini
export GEMINI_API_KEY="your-gemini-key"

# Ollama (local installation)
# No API key required - ensure Ollama is running locally
# Default endpoint: http://localhost:11434
```

### Run All Provider Tests

```bash
# Run all tests with verbose output (recommended)
go test -v ./tests/core-providers/

# Run with debug logs
go test -v ./tests/core-providers/ -debug
```

### Run Specific Provider Tests

```bash
# Test only OpenAI
go test -v ./tests/core-providers/ -run TestOpenAI

# Test only Anthropic
go test -v ./tests/core-providers/ -run TestAnthropic

# Test only Azure
go test -v ./tests/core-providers/ -run TestAzure

# Test only Bedrock
go test -v ./tests/core-providers/ -run TestBedrock

# Test only Cohere
go test -v ./tests/core-providers/ -run TestCohere

# Test only Vertex AI
go test -v ./tests/core-providers/ -run TestVertex

# Test only Mistral
go test -v ./tests/core-providers/ -run TestMistral

# Test only Gemini
go test -v ./tests/core-providers/ -run TestGemini

# Test only Ollama
go test -v ./tests/core-providers/ -run TestOllama
```

### Run Specific Test Scenarios

You can run specific scenarios across all providers:

```bash
# Test only chat completion
go test -v ./tests/core-providers/ -run "Chat"

# Test only function calling
go test -v ./tests/core-providers/ -run "Function"
```

### Run Specific Scenario for Specific Provider

You can combine provider and scenario filters to test specific functionality:

```bash
# Test only OpenAI simple chat
go test -v ./tests/core-providers/ -run "TestOpenAI/SimpleChat"

# Test only Anthropic tool calls
go test -v ./tests/core-providers/ -run "TestAnthropic/ToolCalls"

# Test only Azure multi-turn conversation
go test -v ./tests/core-providers/ -run "TestAzure/MultiTurnConversation"

# Test only Bedrock text completion
go test -v ./tests/core-providers/ -run "TestBedrock/TextCompletion"

# Test only Cohere image URL processing
go test -v ./tests/core-providers/ -run "TestCohere/ImageURL"

# Test only Vertex automatic function calling
go test -v ./tests/core-providers/ -run "TestVertex/AutomaticFunctionCalling"

# Test only Mistral image processing
go test -v ./tests/core-providers/ -run "TestMistral/ImageURL"

# Test only Gemini simple chat
go test -v ./tests/core-providers/ -run "TestGemini/SimpleChat"

# Test only Ollama simple chat
go test -v ./tests/core-providers/ -run "TestOllama/SimpleChat"

# Test only OpenAI reasoning capabilities
go test -v ./tests/core-providers/ -run "TestOpenAI/Reasoning"
```

**Available Scenario Names:**

- `SimpleChat` - Basic chat completion
- `TextCompletion` - Text completion (legacy models)
- `MultiTurnConversation` - Multi-turn chat conversations
- `ToolCalls` - Basic function/tool calling
- `MultipleToolCalls` - Multiple tool calls in one request
- `End2EndToolCalling` - Complete tool calling workflow
- `AutomaticFunctionCalling` - Automatic function selection
- `ImageURL` - Image processing from URLs
- `ImageBase64` - Image processing from base64
- `MultipleImages` - Multiple image processing
- `CompleteEnd2End` - Full end-to-end test
- `ProviderSpecific` - Provider-specific features
- `Embedding` - Basic embedding request
- `Reasoning` - Step-by-step reasoning and thinking capabilities via Responses API

## 🧪 Test Scenarios

Each provider is tested against these scenarios when supported:

✅ **Supported by Most Providers:**

- Simple Text Completion
- Simple Chat Completion
- Multi-turn Chat Conversation
- Chat with System Message
- Text Completion with Parameters
- Chat Completion with Parameters
- Error Handling (Invalid Model)
- Model Information Retrieval
- Simple Function Calling

❌ **Provider-Specific Support:**

- **Automatic Function Calling**: OpenAI, Anthropic, Bedrock, Azure, Vertex, Mistral, Ollama, Gemini
- **Vision/Image Analysis**: OpenAI, Anthropic, Bedrock, Azure, Vertex, Mistral, Gemini (limited support for Cohere and Ollama)
- **Text Completion**: Legacy models only (most providers now focus on chat completion)
- **Reasoning/Thinking**: Advanced reasoning models with step-by-step thinking capabilities via Responses API (provider support varies)

## 📊 Understanding Test Output

The test suite provides rich visual feedback:

- 🚀 **Test suite starting**
- ✅ **Successful operations and supported tests**
- ❌ **Failed operations and unsupported features**
- ⏭️ **Skipped scenarios (not supported by provider)**
- 📊 **Summary statistics**
- ℹ️ **Informational notes**

Example output:

```text
=== RUN   TestOpenAI
🚀 Starting comprehensive test suite for OpenAI provider...
✅ Simple Text Completion test completed successfully
✅ Simple Chat Completion test completed successfully
⏭️ Automatic Function Calling not supported by this provider
📊 Test Summary for OpenAI:
✅✅ Supported Tests: 11
❌ Unsupported Tests: 1
```

## 🔧 Adding New Providers

To add a new provider to the test suite:

### 1. Create Provider Test File

Create a new file `{provider}_test.go`:

```go
package tests

import (
    "testing"
    "github.com/BifrostDev/bifrost/pkg/client"
)

func TestNewProvider(t *testing.T) {
    config := client.Config{
        Provider: "newprovider",
        APIKey:   getEnvVar("NEW_PROVIDER_API_KEY"),
        // Add other required config fields
    }

    // Skip if no API key provided
    if config.APIKey == "" {
        t.Skip("NEW_PROVIDER_API_KEY not set, skipping NewProvider tests")
    }

    runProviderTests(t, config, "NewProvider")
}
```

### 2. Update Provider Configuration

Add your provider's capabilities in `tests.go`:

```go
func getProviderCapabilities(providerName string) ProviderCapabilities {
    switch providerName {
    case "NewProvider":
        return ProviderCapabilities{
            SupportsTextCompletion:       true,
            SupportsChatCompletion:       true,
            SupportsFunctionCalling:     false, // Update based on provider
            SupportsAutomaticFunctions:  false,
            SupportsVision:              false,
            SupportsSystemMessages:      true,
            SupportsMultiTurn:           true,
            SupportsParameters:          true,
            SupportsModelInfo:           true,
            SupportsErrorHandling:       true,
        }
    // ... other cases
    }
}
```

### 3. Add Default Models

Add default models for your provider:

```go
func getDefaultModel(providerName string) string {
    switch providerName {
    case "NewProvider":
        return "newprovider-model-name"
    // ... other cases
    }
}
```

### 4. Environment Variables

Document any required environment variables in this README and ensure they're handled in the test setup.

### 5. Test Your Implementation

Run your new provider tests:

```bash
go test -v ./tests/core-providers/ -run TestNewProvider
```

## 🛠️ Troubleshooting

### Common Issues

1. **Tests being skipped**: Make sure environment variables are set correctly
2. **Connection timeouts**: Check your network connection and API endpoints
3. **Authentication errors**: Verify your API keys are valid and have proper permissions
4. **Missing logs**: Use `-v` flag to see detailed test output
5. **Rate limiting**: Some providers have rate limits; tests may need delays
6. **Ollama connection issues**: Ensure Ollama is running locally (`ollama serve`)
7. **Mistral vision failures**: Check if your account has access to Pixtral models

### Debug Mode

Enable debug logging to see detailed API interactions:

```bash
go test -v ./tests/core-providers/ -debug
```

### Provider-Specific Considerations

#### Mistral AI

- **Models**: Uses `pixtral-12b-latest` for vision tasks
- **Capabilities**: Full support for chat, tools, and vision
- **API Key**: Required via `MISTRAL_API_KEY` environment variable

#### Gemini

- **Models**: Uses `gemini-2.0-flash` for chat and `text-embedding-004` for embeddings
- **Capabilities**: Full support for chat, tools, vision (base64), speech synthesis, and transcription
- **API Key**: Required via `GEMINI_API_KEY` environment variable
- **Limitations**: No text completion support, limited image URL support (base64 preferred)

#### Ollama

- **Local Setup**: Requires Ollama to be running locally (default: `http://localhost:11434`)
- **Models**: Uses `llama3.2` model by default
- **No API Key**: Authentication not required for local instances
- **Limitations**: No vision/image processing support
- **Installation**: [Download from ollama.ai](https://ollama.ai/) and ensure the service is running

### Checking Provider Status

If a provider seems to be failing, you can check their status pages:

- [OpenAI Status](https://status.openai.com/)
- [Anthropic Status](https://status.anthropic.com/)
- [Azure Status](https://status.azure.com/)
- [AWS Status](https://status.aws.amazon.com/)
- [Mistral Status](https://status.mistral.ai/)

## 📝 Test Coverage

The comprehensive test suite covers:

- ✅ **Text Completion** - Legacy completion models (where supported)
- ✅ **Simple Chat** - Basic chat completion functionality
- ✅ **Multi-Turn Conversations** - Context maintenance across messages
- ✅ **Tool Calls** - Basic function/tool calling capabilities
- ✅ **Multiple Tool Calls** - Multiple tools in a single request
- ✅ **End-to-End Tool Calling** - Complete tool workflow with result integration
- ✅ **Automatic Function Calling** - Provider-managed tool execution
- ✅ **Image URL Processing** - Image analysis from URLs
- ✅ **Image Base64 Processing** - Image analysis from base64 encoded data
- ✅ **Multiple Images** - Multi-image analysis and comparison
- ✅ **Complete End-to-End** - Full multimodal workflows
- ✅ **Provider-Specific Features** - Integration-unique capabilities

### Provider Capability Matrix

| Provider  | Chat | Tools | Vision | Text Completion | Auto Functions |
| --------- | ---- | ----- | ------ | --------------- | -------------- |
| OpenAI    | ✅   | ✅    | ✅     | ❌              | ✅             |
| Anthropic | ✅   | ✅    | ✅     | ✅              | ✅             |
| Azure     | ✅   | ✅    | ✅     | ✅              | ✅             |
| Bedrock   | ✅   | ✅    | ✅     | ✅              | ✅             |
| Vertex    | ✅   | ✅    | ✅     | ❌              | ✅             |
| Cohere    | ✅   | ✅    | ❌     | ❌              | ❌             |
| Mistral   | ✅   | ✅    | ✅     | ❌              | ✅             |
| Ollama    | ✅   | ✅    | ❌     | ❌              | ✅             |
| Gemini    | ✅   | ✅    | ✅     | ❌              | ✅             |

## 🤝 Contributing

When adding new providers or test scenarios:

### Adding New Providers

1. **Create test file**: Add `{provider}_test.go` following the existing pattern
2. **Update config**: Add provider configuration in `config/account.go`:
   - Add to `GetKeysForProvider()` (if API key required)
   - Add to `GetConfigForProvider()`
   - Add to `GetConfiguredProviders()` list
3. **Test scenarios**: Configure supported scenarios in the test file
4. **Documentation**: Update this README with environment variables and capabilities
5. **Testing**: Test with multiple scenarios to verify integration

### Adding New Test Scenarios

1. **Implement scenario**: Add new test function in `scenarios/` directory
2. **Update structure**: Add scenario to `TestScenarios` struct in `config/account.go`
3. **Configure providers**: Update each provider's scenario configuration
4. **Update runner**: Add scenario call to `runAllComprehensiveTests()` in `tests.go`
5. **Documentation**: Update README with scenario description and examples

### Testing Your Changes

```bash
# Test specific provider
go test -v ./tests/core-providers/ -run TestYourProvider

# Test all providers
go test -v ./tests/core-providers/

# Test with debug output
go test -v ./tests/core-providers/ -debug
```

## 📄 License

This test suite is part of the Bifrost project and follows the same license terms.
