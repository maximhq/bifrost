# Bifrost Integration Tests

Production-ready end-to-end test suite for testing AI integrations through Bifrost proxy. This test suite provides uniform testing across multiple AI integrations with comprehensive coverage of chat, tool calling, image processing, and multimodal workflows.

## 🌉 Architecture Overview

The Bifrost integration tests use a centralized configuration system that routes all AI integration requests through Bifrost as a gateway/proxy:

```
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   Test Client   │───▶│  Bifrost Gateway │───▶│  AI Integration    │
│                 │    │  localhost:8080  │    │  (OpenAI, etc.) │
└─────────────────┘    └─────────────────┘    └─────────────────┘
```

### URL Structure

- **Base URL**: `http://localhost:8080` (configurable via `BIFROST_BASE_URL`)
- **Integration Endpoints**:
  - OpenAI: `http://localhost:8080/openai`
  - Anthropic: `http://localhost:8080/anthropic`
  - Google: `http://localhost:8080/google`
  - LiteLLM: `http://localhost:8080/litellm`

## 🚀 Features

- **🌉 Bifrost Gateway Integration**: All integrations route through Bifrost proxy
- **🤖 Centralized Configuration**: YAML-based configuration with environment variable support
- **🔧 Integration-Specific Clients**: Type-safe, integration-optimized implementations
- **📋 Comprehensive Test Coverage**: 11 categories covering all major AI functionality
- **⚙️ Flexible Execution**: Selective test running with command-line flags
- **🛡️ Robust Error Handling**: Graceful fallbacks and detailed error reporting
- **🎯 Production-Ready**: Async support, timeouts, retries, and logging

## 📋 Test Categories

Our test suite covers 11 comprehensive scenarios for each integration:

1. **Simple Chat** - Basic single-message conversations
2. **Multi-turn Conversation** - Conversation history and context retention
3. **Single Tool Call** - Basic function calling capabilities
4. **Multiple Tool Calls** - Multiple tools in single request
5. **End-to-End Tool Calling** - Complete tool workflow with results
6. **Automatic Function Calling** - Integration-managed tool execution
7. **Image Analysis (URL)** - Image processing from URLs
8. **Image Analysis (Base64)** - Image processing from base64 data
9. **Multiple Images** - Multi-image analysis and comparison
10. **Complex End-to-End** - Comprehensive multimodal workflows
11. **Integration-Specific Features** - Integration-unique capabilities

## 📁 Directory Structure

```
bifrost-integration-tests/
├── config.yml                   # Central configuration file
├── CONFIGURATION.md             # Configuration system documentation
├── requirements.txt             # Python dependencies
├── run_all_tests.py            # Test runner script
├── run_integration_tests.py       # Integration-specific test runner
├── tests/
│   ├── utils/
│   │   ├── common.py           # Shared test utilities and fixtures
│   │   ├── config_loader.py    # Configuration system
│   │   └── models.py           # Model configurations (compatibility layer)
│   └── integrations/
│       ├── test_openai.py      # OpenAI integration tests
│       ├── test_anthropic.py   # Anthropic integration tests
│       ├── test_google.py      # Google AI integration tests
│       └── test_litellm.py     # LiteLLM integration tests
```

## ⚡ Quick Start

### 1. Installation

```bash
# Clone the repository
git clone <repository-url>
cd bifrost-integration-tests

# Install dependencies
pip install -r requirements.txt
```

### 2. Configuration

The system uses `config.yml` for centralized configuration. Set up your environment variables:

```bash
# Required: Bifrost gateway
export BIFROST_BASE_URL="http://localhost:8080"

# Required: Integration API keys
export OPENAI_API_KEY="your-openai-key"
export ANTHROPIC_API_KEY="your-anthropic-key"
export GOOGLE_API_KEY="your-google-api-key"

# Optional: Integration-specific settings
export OPENAI_ORG_ID="your-org-id"
export OPENAI_PROJECT_ID="your-project-id"
export TEST_ENV="development"
```

### 3. Verify Configuration

```bash
# Test the configuration system
python tests/utils/config_loader.py
```

This will display:

- 🌉 Bifrost gateway URLs
- 🤖 Model configurations
- ⚙️ API settings
- ✅ Validation status

### 4. Run Tests

```bash
# Run all integrations
python run_all_tests.py

# Run specific integration
python run_integration_tests.py openai
python run_integration_tests.py anthropic
python run_integration_tests.py google
python run_integration_tests.py litellm

# Run with pytest directly
pytest tests/integrations/test_openai.py -v
```

## 🔧 Configuration System

### Configuration Files

#### 1. `config.yml` - Main Configuration

Central configuration file containing:

- Bifrost gateway settings and endpoints
- Model configurations for all integrations
- API settings (timeouts, retries)
- Test parameters and limits
- Environment-specific overrides
- Integration-specific settings

#### 2. `tests/utils/config_loader.py` - Configuration Loader

Python module that:

- Loads and parses `config.yml`
- Expands environment variables with `${VAR:-default}` syntax
- Provides convenience functions for URLs and models
- Validates configuration completeness
- Handles fallback scenarios

#### 3. `tests/utils/models.py` - Compatibility Layer

Maintains backward compatibility while delegating to the new config system.

### Key Configuration Sections

#### Bifrost Gateway

```yaml
bifrost:
  base_url: "${BIFROST_BASE_URL:-http://localhost:8080}"
  endpoints:
    openai: "/openai"
    anthropic: "/anthropic"
    google: "/google"
    litellm: "/litellm"
```

#### Model Configurations

```yaml
models:
  openai:
    chat: "gpt-3.5-turbo"
    vision: "gpt-4o"
    tools: "gpt-3.5-turbo"
    streaming: "gpt-3.5-turbo"
    alternatives: ["gpt-4", "gpt-4o-mini"]
```

#### API Settings

```yaml
api:
  timeout: 30
  max_retries: 3
  retry_delay: 1
```

### Usage Examples

#### Getting Integration URLs

```python
from tests.utils.config_loader import get_integration_url

# Get Bifrost URL for OpenAI
openai_url = get_integration_url("openai")
# Returns: http://localhost:8080/openai

# Get fallback URL (direct integration)
openai_direct = get_integration_url("openai", use_fallback=True)
# Returns: https://api.openai.com/v1
```

#### Getting Model Names

```python
from tests.utils.config_loader import get_model

# Get chat model for OpenAI
chat_model = get_model("openai", "chat")
# Returns: gpt-3.5-turbo

# Get vision model for Anthropic
vision_model = get_model("anthropic", "vision")
# Returns: claude-3-haiku-20240307
```

## 🤖 Integration Support

### Currently Supported Integrations

#### OpenAI

- ✅ **Full Bifrost Integration**: Complete base URL support
- ✅ **Models**: gpt-3.5-turbo, gpt-4, gpt-4o, gpt-4o-mini
- ✅ **Features**: Chat, tools, vision, streaming
- ✅ **Settings**: Organization/project IDs, timeouts, retries
- ✅ **All Test Categories**: 11/11 scenarios supported

#### Anthropic

- ✅ **Full Bifrost Integration**: Complete base URL support
- ✅ **Models**: claude-3-haiku, claude-3-sonnet, claude-3-opus, claude-3.5-sonnet
- ✅ **Features**: Chat, tools, vision, streaming
- ✅ **Settings**: API version headers, timeouts, retries
- ✅ **All Test Categories**: 11/11 scenarios supported

#### Google AI

- ⚠️ **Limited Bifrost Integration**: SDK limitations for custom base URLs
- ✅ **Models**: gemini-pro, gemini-pro-vision, gemini-1.5-pro, gemini-1.5-flash
- ✅ **Features**: Chat, tools, vision, streaming
- ✅ **Settings**: Project ID, location, API configuration
- ✅ **All Test Categories**: 11/11 scenarios supported
- 🔄 **TODO**: Custom transport for full Bifrost routing

#### LiteLLM

- ✅ **Full Bifrost Integration**: Global base URL configuration
- ✅ **Models**: Supports all LiteLLM-compatible models
- ✅ **Features**: Chat, tools, vision, streaming (integration-dependent)
- ✅ **Settings**: Drop params, debug mode, integration-specific configs
- ✅ **All Test Categories**: 11/11 scenarios supported
- ✅ **Multi-Integration**: OpenAI, Anthropic, Google, Azure, Cohere, Mistral, etc.

## 🧪 Running Tests

### Test Execution Methods

#### 1. Using Test Runner Scripts

##### `run_integration_tests.py` - Advanced Integration Testing

```bash
# Basic usage - run all available integrations
python run_integration_tests.py

# Run specific integration
python run_integration_tests.py --integrations openai

# Run multiple integrations
python run_integration_tests.py --integrations openai anthropic google

# Run specific test across integrations
python run_integration_tests.py --integrations openai anthropic --test "test_03_single_tool_call"

# Run test pattern (e.g., all tool calling tests)
python run_integration_tests.py --integrations google --test "tool_call"

# Run with verbose output
python run_integration_tests.py --integrations openai --test "test_01_simple_chat" --verbose

# Utility commands
python run_integration_tests.py --check-keys      # Check API key availability
python run_integration_tests.py --show-models     # Show model configuration
```

##### `run_all_tests.py` - Simple Sequential Testing

```bash
# Run all integrations sequentially
python run_all_tests.py

# Run with custom configuration
BIFROST_BASE_URL=https://your-bifrost.com python run_all_tests.py
```

#### 2. Using pytest Directly

```bash
# Run all tests for a integration
pytest tests/integrations/test_openai.py -v

# Run specific test categories
pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_01_simple_chat -v

# Run with coverage
pytest tests/integrations/ --cov=tests --cov-report=html

# Run with custom markers
pytest tests/integrations/ -m "not slow" -v
```

#### 3. Selective Test Execution

```bash
# Skip tests that require API keys you don't have
pytest tests/integrations/test_openai.py -v  # Will skip if OPENAI_API_KEY not set

# Run only specific test methods
pytest tests/integrations/test_anthropic.py -k "tool_call" -v

# Run with timeout
pytest tests/integrations/ --timeout=300 -v
```

### 🔍 Checking and Running Specific Tests

#### 🚀 Quick Commands (Most Common)

```bash
# Run specific test for specific integration (your example!)
python run_integration_tests.py --integrations google --test "test_03_single_tool_call"

# Run all tool calling tests across multiple integrations
python run_integration_tests.py --integrations openai anthropic --test "tool_call"

# Run all tests for one integration
python run_integration_tests.py --integrations openai -v

# Check what integrations are available
python run_integration_tests.py --check-keys

# Run specific test with pytest directly
pytest tests/integrations/test_google.py::TestGoogleIntegration::test_03_single_tool_call -v
```

#### Quick Reference: Test Categories

```
Test 01: Simple Chat              - Basic single-message conversations
Test 02: Multi-turn Conversation  - Conversation history and context
Test 03: Single Tool Call         - Basic function calling
Test 04: Multiple Tool Calls      - Multiple tools in one request
Test 05: End-to-End Tool Calling  - Complete tool workflow with results
Test 06: Automatic Function Call  - Integration-managed tool execution
Test 07: Image Analysis (URL)     - Image processing from URLs
Test 08: Image Analysis (Base64)  - Image processing from base64
Test 09: Multiple Images          - Multi-image analysis and comparison
Test 10: Complex End-to-End       - Comprehensive multimodal workflows
Test 11: Integration-Specific        - Integration-unique features
```

#### Listing Available Tests

```bash
# List all tests for a specific integration
pytest tests/integrations/test_openai.py --collect-only

# List all test methods with descriptions
pytest tests/integrations/test_openai.py --collect-only -q

# Show test structure for all integrations
pytest tests/integrations/ --collect-only
```

#### Running Individual Test Categories

```bash
# Test 1: Simple Chat
pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_01_simple_chat -v

# Test 3: Single Tool Call
pytest tests/integrations/test_anthropic.py::TestAnthropicIntegration::test_03_single_tool_call -v

# Test 7: Image Analysis (URL)
pytest tests/integrations/test_google.py::TestGoogleIntegration::test_07_image_url -v

# Test 9: Multiple Images
pytest tests/integrations/test_litellm.py::TestLiteLLMIntegration::test_09_multiple_images -v
```

#### Running Test Categories by Pattern

```bash
# Run all simple chat tests across integrations
pytest tests/integrations/ -k "test_01_simple_chat" -v

# Run all tool calling tests (single and multiple)
pytest tests/integrations/ -k "tool_call" -v

# Run all image-related tests
pytest tests/integrations/ -k "image" -v

# Run all end-to-end tests
pytest tests/integrations/ -k "end2end" -v

# Run integration-specific feature tests
pytest tests/integrations/ -k "test_11_integration_specific" -v
```

#### Running Tests by Integration

```bash
# Run all OpenAI tests
pytest tests/integrations/test_openai.py -v

# Run all Anthropic tests with detailed output
pytest tests/integrations/test_anthropic.py -v -s

# Run Google tests with coverage
pytest tests/integrations/test_google.py --cov=tests --cov-report=term-missing -v

# Run LiteLLM tests with timing
pytest tests/integrations/test_litellm.py --durations=10 -v
```

#### Advanced Test Selection

```bash
# Run tests 1-5 (basic functionality) for OpenAI
pytest tests/integrations/test_openai.py -k "test_01 or test_02 or test_03 or test_04 or test_05" -v

# Run only vision tests (tests 7, 8, 9, 10)
pytest tests/integrations/ -k "test_07 or test_08 or test_09 or test_10" -v

# Run tests excluding images (skip tests 7, 8, 9, 10)
pytest tests/integrations/ -k "not (test_07 or test_08 or test_09 or test_10)" -v

# Run only tool-related tests (tests 3, 4, 5, 6)
pytest tests/integrations/ -k "test_03 or test_04 or test_05 or test_06" -v
```

#### Test Status and Validation

```bash
# Check which tests would run (dry run)
pytest tests/integrations/test_openai.py --collect-only --quiet

# Validate test setup without running
pytest tests/integrations/test_openai.py --setup-only -v

# Run tests with immediate failure reporting
pytest tests/integrations/ -x -v  # Stop on first failure

# Run tests with detailed failure information
pytest tests/integrations/ --tb=long -v
```

#### Integration-Specific Test Validation

```bash
# Check if integration supports all test categories
python -c "
from tests.integrations.test_openai import TestOpenAIIntegration
import inspect
methods = [m for m in dir(TestOpenAIIntegration) if m.startswith('test_')]
print('OpenAI Test Methods:')
for i, method in enumerate(sorted(methods), 1):
    print(f'  {i:2d}. {method}')
print(f'Total: {len(methods)} tests')
"

# Verify integration configuration
python -c "
from tests.utils.config_loader import get_config, get_model
config = get_config()
integration = 'openai'
print(f'{integration.upper()} Configuration:')
for model_type in ['chat', 'vision', 'tools', 'streaming']:
    try:
        model = get_model(integration, model_type)
        print(f'  {model_type}: {model}')
    except Exception as e:
        print(f'  {model_type}: ERROR - {e}')
"
```

#### Test Results Analysis

```bash
# Run tests with detailed reporting
pytest tests/integrations/test_openai.py -v --tb=short --report=term-missing

# Generate HTML test report
pytest tests/integrations/ --html=test_report.html --self-contained-html

# Run tests with JSON output for analysis
pytest tests/integrations/test_openai.py --json-report --json-report-file=openai_results.json

# Compare test results across integrations
pytest tests/integrations/ -v | grep -E "(PASSED|FAILED|SKIPPED)" | sort
```

#### Debugging Specific Tests

```bash
# Debug a failing test with full output
pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_03_single_tool_call -v -s --tb=long

# Run test with Python debugger
pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_03_single_tool_call --pdb

# Run test with custom logging
pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_03_single_tool_call --log-cli-level=DEBUG -s

# Test with environment variable override
OPENAI_API_KEY=sk-test pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_01_simple_chat -v
```

#### Practical Testing Scenarios

```bash
# Scenario 1: Test a new integration integration
# 1. Check configuration
python tests/utils/config_loader.py

# 2. List available tests
pytest tests/integrations/test_your_integration.py --collect-only

# 3. Run basic tests first (using test runner)
python run_integration_tests.py --integrations your_integration --test "test_01 or test_02" -v

# 4. Test tool calling if supported (using test runner)
python run_integration_tests.py --integrations your_integration --test "tool_call" -v

# Alternative: Direct pytest approach
pytest tests/integrations/test_your_integration.py -k "test_01 or test_02" -v
pytest tests/integrations/test_your_integration.py -k "tool_call" -v

# Scenario 2: Debug a failing tool call test
# 1. Run with full debugging
pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_03_single_tool_call -v -s --tb=long

# 2. Check tool extraction function
python -c "
from tests.integrations.test_openai import extract_openai_tool_calls
print('Tool extraction function available:', callable(extract_openai_tool_calls))
"

# 3. Test with different model
OPENAI_CHAT_MODEL=gpt-4 pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_03_single_tool_call -v

# Scenario 3: Compare integration capabilities
# Run the same test across all integrations (using test runner)
python run_integration_tests.py --integrations openai anthropic google litellm --test "test_01_simple_chat" -v

# Alternative: Direct pytest approach
pytest tests/integrations/ -k "test_01_simple_chat" -v --tb=short

# Scenario 4: Test only supported features
# For a integration that doesn't support images
pytest tests/integrations/test_your_integration.py -k "not (test_07 or test_08 or test_09 or test_10)" -v

# Scenario 5: Performance testing
# Run with timing to identify slow tests
pytest tests/integrations/test_openai.py --durations=0 -v

# Scenario 6: Continuous integration testing
# Run all tests with coverage and reports
pytest tests/integrations/ --cov=tests --cov-report=xml --junit-xml=test_results.xml -v
```

#### Test Output Examples

```bash
# Successful test run
$ pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_01_simple_chat -v
========================= test session starts =========================
tests/integrations/test_openai.py::TestOpenAIIntegration::test_01_simple_chat PASSED [100%]
✓ OpenAI simple chat test passed
Response: "Hello! I'm Claude, an AI assistant. How can I help you today?"

# Failed test with debugging info
$ pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_03_single_tool_call -v -s
========================= FAILURES =========================
_____________ TestOpenAIIntegration.test_03_single_tool_call _____________
AssertionError: Expected tool calls but got none
Response content: "I can help with weather information, but I need a specific location."
Tool calls found: []

# Test collection output
$ pytest tests/integrations/test_openai.py --collect-only -q
tests/integrations/test_openai.py::TestOpenAIIntegration::test_01_simple_chat
tests/integrations/test_openai.py::TestOpenAIIntegration::test_02_multi_turn_conversation
tests/integrations/test_openai.py::TestOpenAIIntegration::test_03_single_tool_call
tests/integrations/test_openai.py::TestOpenAIIntegration::test_04_multiple_tool_calls
tests/integrations/test_openai.py::TestOpenAIIntegration::test_05_end2end_tool_calling
tests/integrations/test_openai.py::TestOpenAIIntegration::test_06_automatic_function_calling
tests/integrations/test_openai.py::TestOpenAIIntegration::test_07_image_url
tests/integrations/test_openai.py::TestOpenAIIntegration::test_08_image_base64
tests/integrations/test_openai.py::TestOpenAIIntegration::test_09_multiple_images
tests/integrations/test_openai.py::TestOpenAIIntegration::test_10_complex_end2end
tests/integrations/test_openai.py::TestOpenAIIntegration::test_11_integration_specific_features
11 tests collected

# Test runner script output
$ python run_integration_tests.py --integrations google --test "test_03_single_tool_call" -v
🚀 Starting integration tests...
📋 Testing integrations: google
============================================================
🧪 TESTING GOOGLE INTEGRATION
============================================================
========================= test session starts =========================
tests/integrations/test_google.py::TestGoogleIntegration::test_03_single_tool_call PASSED [100%]
✅ GOOGLE tests PASSED

================================================================================
🎯 FINAL SUMMARY
================================================================================

🔑 API Key Status:
  ✅ GOOGLE: Available

📊 Test Results:
  ✅ GOOGLE: All tests passed

🏆 Overall Results:
  Integrations tested: 1
  Integrations passed: 1
  Success rate: 100.0%
```

### Environment Variables

#### Required Variables

```bash
# Bifrost gateway (required)
export BIFROST_BASE_URL="http://localhost:8080"

# Integration API keys (at least one required)
export OPENAI_API_KEY="sk-..."
export ANTHROPIC_API_KEY="sk-ant-..."
export GOOGLE_API_KEY="AIza..."
```

#### Optional Variables

```bash
# Integration-specific settings
export OPENAI_ORG_ID="org-..."
export OPENAI_PROJECT_ID="proj_..."
export GOOGLE_PROJECT_ID="your-project"
export GOOGLE_LOCATION="us-central1"

# Environment configuration
export TEST_ENV="development"  # or "production"

# Fallback URLs (for direct integration access)
export OPENAI_DIRECT_URL="https://api.openai.com/v1"
export ANTHROPIC_DIRECT_URL="https://api.anthropic.com"
```

### Test Output and Debugging

#### Understanding Test Results

```bash
# Successful test output
✓ OpenAI Integration Tests
  ✓ test_01_simple_chat - Response: "Hello! How can I help you today?"
  ✓ test_03_single_tool_call - Tool called: get_weather(location="New York")
  ✓ test_07_image_url - Image analyzed successfully

# Failed test output
✗ test_03_single_tool_call - AssertionError: Expected tool calls but got none
  Response content: "I can help with weather, but I need a specific location."
```

#### Debug Mode

```bash
# Enable verbose output
pytest tests/integrations/test_openai.py -v -s

# Show full tracebacks
pytest tests/integrations/test_openai.py --tb=long

# Enable debug logging
pytest tests/integrations/test_openai.py --log-cli-level=DEBUG
```

## 🔨 Adding New Integrations

### Step-by-Step Guide

#### 1. Update Configuration

Add your integration to `config.yml`:

```yaml
# Add to bifrost endpoints
bifrost:
  endpoints:
    your_integration: "/your_integration"

# Add to fallback URLs
fallback_urls:
  your_integration: "${YOUR_INTEGRATION_DIRECT_URL:-https://api.yourintegration.com}"

# Add model configuration
models:
  your_integration:
    chat: "your-chat-model"
    vision: "your-vision-model"
    tools: "your-tools-model"
    streaming: "your-streaming-model"
    alternatives: ["alternative-model-1", "alternative-model-2"]

# Add model capabilities
model_capabilities:
  "your-chat-model":
    chat: true
    tools: true
    vision: false
    streaming: true
    max_tokens: 4096
    context_window: 8192

# Add integration settings
integration_settings:
  your_integration:
    api_version: "v1"
    custom_header: "value"
```

#### 2. Create Integration Test File

Create `tests/integrations/test_your_integration.py`:

```python
"""
Your Integration Tests

Tests all 11 core scenarios using Your Integration SDK.
"""

import pytest
from your_integration_sdk import YourIntegrationClient

from ..utils.common import (
    Config,
    SIMPLE_CHAT_MESSAGES,
    MULTI_TURN_MESSAGES,
    # ... import all test fixtures
    get_api_key,
    skip_if_no_api_key,
    get_model,
)


@pytest.fixture
def your_integration_client():
    """Create Your Integration client for testing"""
    from ..utils.config_loader import get_integration_url, get_config

    api_key = get_api_key("your_integration")
    base_url = get_integration_url("your_integration")

    # Get additional integration settings
    config = get_config()
    integration_settings = config.get_integration_settings("your_integration")
    api_config = config.get_api_config()

    client_kwargs = {
        "api_key": api_key,
        "base_url": base_url,
        "timeout": api_config.get("timeout", 30),
        "max_retries": api_config.get("max_retries", 3),
    }

    # Add integration-specific settings
    if integration_settings.get("api_version"):
        client_kwargs["api_version"] = integration_settings["api_version"]

    return YourIntegrationClient(**client_kwargs)


@pytest.fixture
def test_config():
    """Test configuration"""
    return Config()


class TestYourIntegrationIntegration:
    """Test suite for Your Integration covering all 11 core scenarios"""

    @skip_if_no_api_key("your_integration")
    def test_01_simple_chat(self, your_integration_client, test_config):
        """Test Case 1: Simple chat interaction"""
        response = your_integration_client.chat.create(
            model=get_model("your_integration", "chat"),
            messages=SIMPLE_CHAT_MESSAGES,
            max_tokens=100,
        )

        assert_valid_chat_response(response)
        assert response.content is not None
        assert len(response.content) > 0

    # ... implement all 11 test methods following the same pattern
    # See existing integration test files for complete examples


def extract_your_integration_tool_calls(response) -> List[Dict[str, Any]]:
    """Extract tool calls from Your Integration response format"""
    tool_calls = []

    # Implement based on your integration's response format
    if hasattr(response, 'tool_calls') and response.tool_calls:
        for tool_call in response.tool_calls:
            tool_calls.append({
                "name": tool_call.function.name,
                "arguments": json.loads(tool_call.function.arguments)
            })

    return tool_calls
```

#### 3. Update Common Utilities

Add your integration to `tests/utils/common.py`:

```python
def get_api_key(integration: str) -> str:
    """Get API key for integration"""
    key_map = {
        "openai": "OPENAI_API_KEY",
        "anthropic": "ANTHROPIC_API_KEY",
        "google": "GOOGLE_API_KEY",
        "litellm": "LITELLM_API_KEY",
        "your_integration": "YOUR_INTEGRATION_API_KEY",  # Add this line
    }

    env_var = key_map.get(integration)
    if not env_var:
        raise ValueError(f"Unknown integration: {integration}")

    api_key = os.getenv(env_var)
    if not api_key:
        raise ValueError(f"{env_var} environment variable not set")

    return api_key
```

#### 4. Add Integration-Specific Tool Extraction

Update the tool extraction functions in your test file:

```python
def extract_your_integration_tool_calls(response: Any) -> List[Dict[str, Any]]:
    """Extract tool calls from Your Integration response format"""
    tool_calls = []

    try:
        # Implement based on your integration's response structure
        # Example for a hypothetical integration:
        if hasattr(response, 'function_calls'):
            for fc in response.function_calls:
                tool_calls.append({
                    "name": fc.name,
                    "arguments": fc.parameters
                })

        return tool_calls

    except Exception as e:
        print(f"Error extracting tool calls: {e}")
        return []
```

#### 5. Test Your Implementation

```bash
# Set up environment
export YOUR_INTEGRATION_API_KEY="your-api-key"
export BIFROST_BASE_URL="http://localhost:8080"

# Test configuration
python tests/utils/config_loader.py

# Run your integration tests
pytest tests/integrations/test_your_integration.py -v

# Run specific test
pytest tests/integrations/test_your_integration.py::TestYourIntegrationIntegration::test_01_simple_chat -v
```

### 🎯 Key Implementation Points

#### 1. **Follow the Pattern**

- Use existing integration test files as templates
- Implement all 11 test scenarios
- Follow the same naming conventions and structure

#### 2. **Handle Integration Differences**

```python
# Example: Different response formats
def assert_valid_chat_response(response):
    """Validate chat response - adapt for your integration"""
    if hasattr(response, 'choices'):  # OpenAI-style
        assert response.choices[0].message.content
    elif hasattr(response, 'content'):  # Anthropic-style
        assert response.content[0].text
    elif hasattr(response, 'text'):  # Google-style
        assert response.text
    # Add your integration's format here
```

#### 3. **Implement Tool Calling**

```python
def convert_to_your_integration_tools(tools: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
    """Convert common tool format to your integration's format"""
    your_integration_tools = []

    for tool in tools:
        # Convert to your integration's tool schema
        your_integration_tools.append({
            "name": tool["name"],
            "description": tool["description"],
            "parameters": tool["parameters"],
            # Add integration-specific fields
        })

    return your_integration_tools
```

#### 4. **Handle Image Processing**

```python
def convert_to_your_integration_messages(messages: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
    """Convert common message format to your integration's format"""
    your_integration_messages = []

    for msg in messages:
        if isinstance(msg.get("content"), list):
            # Handle multimodal content (text + images)
            content = []
            for item in msg["content"]:
                if item["type"] == "text":
                    content.append({"type": "text", "text": item["text"]})
                elif item["type"] == "image_url":
                    # Convert to your integration's image format
                    content.append({
                        "type": "image",
                        "source": item["image_url"]["url"]
                    })
            your_integration_messages.append({"role": msg["role"], "content": content})
        else:
            your_integration_messages.append(msg)

    return your_integration_messages
```

#### 5. **Error Handling and Fallbacks**

```python
@skip_if_no_api_key("your_integration")
def test_03_single_tool_call(self, your_integration_client, test_config):
    """Test Case 3: Single tool call"""
    try:
        response = your_integration_client.chat.create(
            model=get_model("your_integration", "tools"),
            messages=SINGLE_TOOL_CALL_MESSAGES,
            tools=convert_to_your_integration_tools([WEATHER_TOOL]),
            max_tokens=100,
        )

        assert_has_tool_calls(response, expected_count=1)
        tool_calls = extract_your_integration_tool_calls(response)
        assert tool_calls[0]["name"] == "get_weather"
        assert "location" in tool_calls[0]["arguments"]

    except Exception as e:
        pytest.skip(f"Tool calling not supported or failed: {e}")
```

### 🔍 Testing Checklist

Before submitting your integration implementation:

- [ ] **Configuration**: Integration added to `config.yml` with all required sections
- [ ] **Environment**: API key environment variable documented and tested
- [ ] **All 11 Tests**: Every test scenario implemented and passing
- [ ] **Tool Extraction**: Integration-specific tool call extraction function
- [ ] **Message Conversion**: Proper handling of multimodal messages
- [ ] **Error Handling**: Graceful handling of unsupported features
- [ ] **Documentation**: Integration added to README with capabilities
- [ ] **Bifrost Integration**: Base URL properly configured and tested

### 🚨 Common Pitfalls

1. **Incorrect Response Parsing**: Each integration has different response formats
2. **Tool Schema Differences**: Tool calling schemas vary significantly
3. **Image Format Handling**: Base64 vs URL handling differs per integration
4. **Missing Error Handling**: Some integrations don't support all features
5. **Configuration Errors**: Forgetting to add integration to all config sections

## 🔧 Troubleshooting

### Common Issues

#### 1. Configuration Problems

```bash
# Error: Configuration file not found
FileNotFoundError: Configuration file not found: config.yml

# Solution: Ensure config.yml exists in project root
ls -la config.yml
```

#### 2. Integration Connection Issues

```bash
# Error: Connection refused to Bifrost
ConnectionError: Connection refused to localhost:8080

# Solutions:
# 1. Check if Bifrost is running
curl http://localhost:8080/health

# 2. Use fallback URL for testing
export OPENAI_DIRECT_URL="https://api.openai.com/v1"
python -c "from tests.utils.config_loader import get_integration_url; print(get_integration_url('openai', use_fallback=True))"
```

#### 3. API Key Issues

```bash
# Error: API key not set
ValueError: OPENAI_API_KEY environment variable not set

# Solution: Set required environment variables
export OPENAI_API_KEY="sk-..."
export ANTHROPIC_API_KEY="sk-ant-..."
export GOOGLE_API_KEY="AIza..."
```

#### 4. Model Configuration Errors

```bash
# Error: Unknown model type
ValueError: Unknown model type 'vision' for integration 'your_integration'

# Solution: Check config.yml has all model types defined
python tests/utils/config_loader.py
```

#### 5. Test Failures

```bash
# Error: Tool calls not found
AssertionError: Response should contain tool calls

# Debug steps:
# 1. Check if integration supports tool calling
# 2. Verify tool extraction function
# 3. Check integration-specific tool format
pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_03_single_tool_call -v -s
```

### Debug Mode

Enable comprehensive debugging:

```bash
# Full verbose output with debugging
pytest tests/integrations/test_openai.py -v -s --tb=long --log-cli-level=DEBUG

# Test configuration system
python tests/utils/config_loader.py

# Check specific integration URL
python -c "
from tests.utils.config_loader import get_integration_url, get_model
print('OpenAI URL:', get_integration_url('openai'))
print('OpenAI Chat Model:', get_model('openai', 'chat'))
"
```

## 📚 Additional Resources

### Configuration Examples

- See `config.yml` for complete configuration reference
- Check `tests/utils/config_loader.py` for usage examples
- Review integration test files for implementation patterns

### Integration Documentation

- [OpenAI API Documentation](https://platform.openai.com/docs)
- [Anthropic API Documentation](https://docs.anthropic.com)
- [Google AI API Documentation](https://ai.google.dev/docs)
- [LiteLLM Documentation](https://docs.litellm.ai)

### Contributing

1. Fork the repository
2. Create feature branch: `git checkout -b feature/new-integration`
3. Follow the integration implementation guide above
4. Add comprehensive tests and documentation
5. Submit pull request with test results

## 📄 License

[Add your license information here]

## 🆘 Support

For issues and questions:

- Create GitHub issues for bugs and feature requests
- Check existing issues for solutions
- Review integration-specific documentation
- Test configuration with `python tests/utils/config_loader.py`

---

**Note**: This test suite is designed for testing AI integrations through Bifrost proxy. Ensure your Bifrost instance is properly configured and running before executing tests. The configuration system provides both Bifrost routing and direct integration fallbacks for maximum flexibility.
