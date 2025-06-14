# Bifrost Core Providers Test Suite 🚀

This directory contains comprehensive tests for all Bifrost AI providers, ensuring compatibility and functionality across different AI services.

## 📋 Supported Providers

- **OpenAI** - GPT models and function calling
- **Anthropic** - Claude models
- **Azure OpenAI** - Azure-hosted OpenAI models
- **AWS Bedrock** - Amazon's managed AI service
- **Cohere** - Cohere's language models
- **Google Vertex AI** - Google Cloud's AI platform

## 🏃‍♂️ Running Tests

### Development with Local Bifrost Core

If you're working with a forked or local version of bifrost-core and want to test your changes:

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

⚠️ **Important**: Make sure your local `../../core` directory contains your bifrost-core implementation. The path should be relative to the `tests/core-providers` directory.

### Prerequisites

Set up your environment variables for the providers you want to test:

```bash
# OpenAI
export OPENAI_API_KEY="your-openai-key"

# Anthropic
export ANTHROPIC_API_KEY="your-anthropic-key"

# Azure OpenAI
export AZURE_OPENAI_API_KEY="your-azure-key"
export AZURE_OPENAI_ENDPOINT="your-azure-endpoint"

# AWS Bedrock
export AWS_ACCESS_KEY_ID="your-aws-access-key"
export AWS_SECRET_ACCESS_KEY="your-aws-secret-key"
export AWS_REGION="us-east-1"

# Cohere
export COHERE_API_KEY="your-cohere-key"

# Google Vertex AI
export GOOGLE_APPLICATION_CREDENTIALS="path/to/service-account.json"
export GOOGLE_PROJECT_ID="your-project-id"
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
```

### Run Specific Test Scenarios

You can run specific scenarios across all providers:

```bash
# Test only chat completion
go test -v ./tests/core-providers/ -run "Chat"

# Test only streaming
go test -v ./tests/core-providers/ -run "Streaming"

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

## 🧪 Test Scenarios

Each provider is tested against these scenarios when supported:

✅ **Supported by Most Providers:**

- Simple Text Completion
- Simple Chat Completion
- Multi-turn Chat Conversation
- Chat with System Message
- Streaming Chat Completion
- Streaming Text Completion
- Text Completion with Parameters
- Chat Completion with Parameters
- Error Handling (Invalid Model)
- Model Information Retrieval
- Simple Function Calling

❌ **Provider-Specific Support:**

- Automatic Function Calling (OpenAI, some others)
- Vision/Image Analysis (provider-dependent)
- Advanced streaming features

## 📊 Understanding Test Output

The test suite provides rich visual feedback:

- 🚀 **Test suite starting**
- ✅ **Successful operations and supported tests**
- ❌ **Failed operations and unsupported features**
- ⏭️ **Skipped scenarios (not supported by provider)**
- 📊 **Summary statistics**
- ℹ️ **Informational notes**

Example output:

```
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
package main

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
            SupportsStreaming:           true,
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

### Debug Mode

Enable debug logging to see detailed API interactions:

```bash
go test -v ./tests/core-providers/ -debug
```

### Checking Provider Status

If a provider seems to be failing, you can check their status pages:

- OpenAI: https://status.openai.com/
- Anthropic: https://status.anthropic.com/
- Azure: https://status.azure.com/
- AWS: https://status.aws.amazon.com/

## 📝 Test Coverage

The test suite aims to cover:

- ✅ **Core functionality** - Basic text and chat completion
- ✅ **Streaming** - Real-time response handling
- ✅ **Function calling** - Tool usage and structured outputs
- ✅ **Error handling** - Graceful failure management
- ✅ **Parameters** - Temperature, max tokens, etc.
- ✅ **Multi-turn conversations** - Context maintenance
- ✅ **System messages** - Role-based interactions

## 🤝 Contributing

When adding new test scenarios:

1. Add the scenario to the `ProviderCapabilities` struct
2. Implement the test function following existing patterns
3. Update provider capabilities mapping
4. Test with at least 2 different providers
5. Update this README with any new environment variables or setup steps

## 📄 License

This test suite is part of the Bifrost project and follows the same license terms.
