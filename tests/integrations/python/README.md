# Bifrost Integration Tests

Production-ready end-to-end test suite for testing AI integrations through Bifrost proxy. This test suite provides uniform testing across multiple AI integrations with comprehensive coverage of chat, tool calling, image processing, embeddings, speech synthesis, and multimodal workflows.

## 🎯 Quick Start (TL;DR)

```bash
# 1. Install uv (if not already installed)
curl -LsSf https://astral.sh/uv/install.sh | sh

# 2. Install dependencies
cd bifrost/tests/integrations
uv sync

# 3. Set environment variables
export BIFROST_BASE_URL="http://localhost:8080"
export OPENAI_API_KEY="your-key"
export ANTHROPIC_API_KEY="your-key"

# 4. Run tests
uv run pytest                          # All tests
uv run pytest tests/integrations/test_openai.py -v  # Specific integration
uv run pytest -k "tool_call" -v       # By pattern
uv run pytest -n auto                  # Parallel execution
```

**Note:** All `pytest` commands in this README can be prefixed with `uv run`. If you prefer traditional pip, run `pip install -r requirements.txt` and use `pytest` directly.

## 🌉 Architecture Overview

The Bifrost integration tests use a centralized configuration system that routes all AI integration requests through Bifrost as a gateway/proxy:

```text
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
  - Google: `http://localhost:8080/genai`
  - LiteLLM: `http://localhost:8080/litellm`

## 🚀 Features

- **🌉 Bifrost Gateway Integration**: All integrations route through Bifrost proxy
- **🤖 Centralized Configuration**: YAML-based configuration with environment variable support
- **🔧 Integration-Specific Clients**: Type-safe, integration-optimized implementations
- **📋 Comprehensive Test Coverage**: 14 categories covering all major AI functionality
- **⚙️ Flexible Execution**: Selective test running with command-line flags
- **🛡️ Robust Error Handling**: Graceful error handling and detailed error reporting
- **🎯 Production-Ready**: Async support, timeouts, retries, and logging
- **🎵 Speech & Audio Support**: Text-to-speech synthesis and speech-to-text transcription testing
- **🔗 Embeddings Support**: Text-to-vector conversion and similarity analysis testing

## 📋 Test Categories

Our test suite covers 30 comprehensive scenarios for each integration:

### Core Chat & Conversation Tests
1. **Simple Chat** - Basic single-message conversations
2. **Multi-turn Conversation** - Conversation history and context retention
3. **Streaming** - Real-time streaming responses and tool calls

### Tool Calling & Function Tests
4. **Single Tool Call** - Basic function calling capabilities
5. **Multiple Tool Calls** - Multiple tools in single request
6. **End-to-End Tool Calling** - Complete tool workflow with results
7. **Automatic Function Calling** - Integration-managed tool execution

### Image & Vision Tests
8. **Image Analysis (URL)** - Image processing from URLs
9. **Image Analysis (Base64)** - Image processing from base64 data
10. **Multiple Images** - Multi-image analysis and comparison

### Speech & Audio Tests (OpenAI)
11. **Speech Synthesis** - Text-to-speech conversion with different voices
12. **Audio Transcription** - Speech-to-text conversion with multiple formats
13. **Transcription Streaming** - Real-time transcription processing
14. **Speech Round-Trip** - Complete text→speech→text workflow validation
15. **Speech Error Handling** - Invalid voice, model, and input error handling
16. **Transcription Error Handling** - Invalid audio format and model error handling
17. **Voice & Format Testing** - Multiple voices and audio format validation

### Embeddings Tests (OpenAI)
18. **Single Text Embedding** - Basic text-to-vector conversion
19. **Batch Text Embeddings** - Multiple text embeddings in single request
20. **Embedding Similarity Analysis** - Cosine similarity testing for similar texts
21. **Embedding Dissimilarity Analysis** - Validation of different topic embeddings
22. **Different Embedding Models** - Testing various embedding model capabilities
23. **Long Text Embedding** - Handling of longer text inputs and token usage
24. **Embedding Error Handling** - Invalid model and input error processing
25. **Dimensionality Reduction** - Custom embedding dimensions (if supported)
26. **Encoding Format Testing** - Different embedding output formats
27. **Usage Tracking** - Token consumption and batch processing validation

### Integration & Error Tests
28. **Complex End-to-End** - Comprehensive multimodal workflows
29. **Integration-Specific Features** - Integration-unique capabilities
30. **Error Handling** - Invalid request error processing and propagation

## 📁 Directory Structure

```text
integrations/
├── config.yml                   # Central configuration file
├── pyproject.toml               # Python project configuration (uv/pip)
├── requirements.txt             # Python dependencies (legacy compatibility)
├── .python-version              # Python version specification for uv
├── pytest.ini                   # Pytest configuration
├── tests/
│   ├── conftest.py             # Pytest configuration and fixtures
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
cd bifrost/tests/integrations

# Install uv (if not already installed)
curl -LsSf https://astral.sh/uv/install.sh | sh

# Install dependencies with uv (recommended - fastest)
uv sync

# Or with traditional pip
pip install -r requirements.txt
```

#### Why use uv?

[uv](https://github.com/astral-sh/uv) is an extremely fast Python package installer and resolver, written in Rust. It's 10-100x faster than pip and provides better dependency resolution.

```bash
# Install dependencies
uv sync

# Run all tests
uv run pytest

# Run specific integration tests
uv run pytest tests/integrations/test_openai.py -v

# Run specific test categories
uv run pytest -k "tool_call" -v
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
export OPENAI_ORG_ID="org-..."
export OPENAI_PROJECT_ID="proj_..."
export GOOGLE_PROJECT_ID="your-project"
export GOOGLE_LOCATION="us-central1"
export TEST_ENV="development"

# Quick check using Makefile
make check-env
```

### 3. Verify Configuration

```bash
# Test the configuration system
uv run python tests/utils/config_loader.py
```

This will display:

- 🌉 Bifrost gateway URLs
- 🤖 Model configurations
- ⚙️ API settings
- ✅ Validation status

### 4. Pytest Configuration

The project includes a `pytest.ini` file with optimized settings:

```ini
[pytest]
# Test discovery
testpaths = .
python_files = test_*.py
python_classes = Test*
python_functions = test_*

# Output formatting
addopts =
    -v
    --tb=short
    --strict-markers
    --disable-warnings
    --color=yes

# Timeout settings (3 minutes per test)
timeout = 180

# Markers for test categorization
markers =
    integration: marks tests as integration tests
    slow: marks tests as slow running
    e2e: marks tests as end-to-end tests
    tool_calling: marks tests as tool calling tests
```

### 5. Run Tests

```bash
# Run all tests
uv run pytest

# Run all tests with verbose output
uv run pytest -v

# Run specific integration tests
uv run pytest tests/integrations/test_openai.py -v
uv run pytest tests/integrations/test_anthropic.py -v
uv run pytest tests/integrations/test_google.py -v
uv run pytest tests/integrations/test_litellm.py -v

# Run specific test by name
uv run pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_01_simple_chat -v

# Run tests by pattern/category
uv run pytest -k "tool_call" -v           # All tool calling tests
uv run pytest -k "image" -v               # All image tests
uv run pytest -k "speech or transcription" -v  # All audio tests
uv run pytest -k "embedding" -v           # All embedding tests

# Run tests in parallel (faster)
uv run pytest -n auto

# Run with coverage report
uv run pytest --cov=tests --cov-report=html

# Traditional pip usage (if not using uv)
pytest tests/integrations/test_openai.py -v
```

## 🚄 Using uv (Recommended)

[uv](https://github.com/astral-sh/uv) is an extremely fast Python package installer and resolver, written in Rust. It's 10-100x faster than pip and provides better dependency resolution, making it the recommended way to run these tests.

### Installation

```bash
# Install uv
curl -LsSf https://astral.sh/uv/install.sh | sh

# Or with pip
pip install uv

# Or with Homebrew (macOS)
brew install uv
```

### Quick Start with uv

```bash
# 1. Install dependencies
uv sync

# 2. Run all tests
uv run pytest

# 3. Run specific integration
uv run pytest tests/integrations/test_openai.py -v

# 4. Run tests by pattern
uv run pytest -k "tool_call" -v
```

### Common Commands

```bash
# Setup
uv sync                          # Install all dependencies from pyproject.toml

# Running tests
uv run pytest                    # Run all tests
uv run pytest -v                 # Verbose output
uv run pytest -n auto            # Run tests in parallel
uv run pytest -k "pattern"       # Run tests matching pattern
uv run pytest tests/integrations/test_openai.py  # Run specific file

# Development
uv run black .                   # Format code
uv run flake8 .                  # Lint code
uv run mypy .                    # Type check

# Managing dependencies
uv add package-name              # Add a new dependency
uv remove package-name           # Remove a dependency
uv pip list                      # List installed packages
```

### Why use uv?

1. **Speed**: 10-100x faster than pip for package installation
2. **Reliability**: Better dependency resolution and conflict detection
3. **Simplicity**: Single tool for package management and running scripts
4. **Modern**: Built with Rust, designed for speed and efficiency
5. **Compatible**: Works with standard Python packaging (pyproject.toml, requirements.txt)

### Migration from pip

If you're currently using pip, migrating to uv is straightforward:

```bash
# Old way (pip)
pip install -r requirements.txt
pytest tests/integrations/test_openai.py -v

# New way (uv)
uv sync
uv run pytest tests/integrations/test_openai.py -v
```

All existing pytest commands work the same way, just prefix them with `uv run`.

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
- Handles error scenarios

#### 3. `tests/utils/models.py` - Compatibility Layer

Maintains backward compatibility while delegating to the new config system.

### Key Configuration Sections

#### Bifrost Gateway

```yaml
bifrost:
  base_url: "${BIFROST_BASE_URL:-http://localhost:8080}"
  endpoints:
    openai: "openai"
    anthropic: "anthropic"
    google: "genai"
    litellm: "litellm"
```

#### Model Configurations

```yaml
models:
  openai:
    chat: "gpt-3.5-turbo"
    vision: "gpt-4o"
    tools: "gpt-3.5-turbo"
    speech: "tts-1"
    transcription: "whisper-1"
    alternatives: ["gpt-4", "gpt-4-turbo-preview", "gpt-4o", "gpt-4o-mini"]
    speech_alternatives: ["tts-1-hd"]
    transcription_alternatives: ["whisper-1"]
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

# Get integration URL through Bifrost
openai_url = get_integration_url("openai")
# Returns: http://localhost:8080/openai
```

#### Getting Model Names

```python
from tests.utils.config_loader import get_model

# Get different model types
chat_model = get_model("openai", "chat")          # "gpt-3.5-turbo"
vision_model = get_model("openai", "vision")      # "gpt-4o"
speech_model = get_model("openai", "speech")      # "tts-1"
transcription_model = get_model("openai", "transcription")  # "whisper-1"
```

## 🎵 Speech & Transcription Testing

The test suite includes comprehensive speech synthesis and transcription testing for supported integrations (currently OpenAI).

### Speech & Audio Test Categories

#### 1. Speech Synthesis (Text-to-Speech)
- **Basic synthesis**: Convert text to audio with different voices
- **Format testing**: Multiple audio formats (MP3, WAV, Opus)
- **Voice validation**: Test all available voices (alloy, echo, fable, onyx, nova, shimmer)
- **Parameter testing**: Response format, voice settings, and quality options

#### 2. Speech Streaming
- **Real-time generation**: Streaming audio synthesis for large texts
- **Chunk validation**: Verify audio chunk integrity and format
- **Performance testing**: Measure streaming latency and throughput

#### 3. Audio Transcription (Speech-to-Text)
- **File format support**: WAV, MP3, and other audio formats
- **Language detection**: Multi-language transcription capabilities
- **Parameter testing**: Language hints, response formats, temperature settings
- **Quality validation**: Transcription accuracy and completeness

#### 4. Transcription Streaming
- **Real-time processing**: Streaming transcription for long audio files
- **Progressive results**: Incremental text output validation
- **Error handling**: Network interruption and recovery testing

#### 5. Round-Trip Testing
- **Complete workflow**: Text → Speech → Transcription → Text validation
- **Accuracy measurement**: Compare original text with round-trip result
- **Quality assessment**: Measure transcription fidelity and word preservation

### Running Speech & Transcription Tests

#### Quick Start

```bash
# Run all speech and transcription tests
uv run pytest -k "speech or transcription" -v

# Run with verbose output and show print statements
uv run pytest -k "speech or transcription" -v -s

# Run specific test
uv run pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_14_speech_synthesis -v

# List available tests
uv run pytest --collect-only -k "speech or transcription"
```

#### Individual Test Examples

```bash
# Test speech synthesis
pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_14_speech_synthesis -v

# Test transcription
pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_16_transcription_audio -v

# Test round-trip workflow
pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_18_speech_transcription_round_trip -v

# Test error handling
pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_19_speech_error_handling -v
pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_20_transcription_error_handling -v
```

#### Available Test Audio Types

1. **Sine Wave**: Pure tone audio for basic testing
2. **Chord**: Multi-frequency audio for complex signal testing
3. **Frequency Sweep**: Variable frequency audio for range testing
4. **White Noise**: Random audio for noise handling testing
5. **Silence**: Empty audio for edge case testing
6. **Various Durations**: Short (0.5s) to long (10s) audio files

### Speech & Transcription Configuration

#### Model Configuration

```yaml
models:
  openai:
    speech: "tts-1"                    # Default speech synthesis model
    transcription: "whisper-1"         # Default transcription model
    speech_alternatives: ["tts-1-hd"]  # Higher quality speech model
    transcription_alternatives: ["whisper-1"]  # Alternative transcription models

# Model capabilities
model_capabilities:
  "tts-1":
    speech: true
    streaming: false  # Streaming support varies
    max_tokens: null
    context_window: null

  "whisper-1":
    transcription: true
    streaming: false  # Streaming support varies
    max_tokens: null
    context_window: null
```

#### Test Settings

```yaml
test_settings:
  max_tokens:
    speech: null          # Speech doesn't use token limits
    transcription: null   # Transcription doesn't use token limits

  timeouts:
    speech: 60           # Speech generation timeout
    transcription: 60    # Transcription processing timeout
```

### Speech Test Examples

#### Basic Speech Synthesis

```python
# Test basic speech synthesis
response = openai_client.audio.speech.create(
    model="tts-1",
    voice="alloy",
    input="Hello, this is a test of speech synthesis.",
)
audio_content = response.content
assert len(audio_content) > 1000  # Ensure substantial audio data
```

#### Transcription Testing

```python
# Test audio transcription
test_audio = generate_test_audio()  # Generate test WAV file
response = openai_client.audio.transcriptions.create(
    model="whisper-1",
    file=("test.wav", test_audio, "audio/wav"),
    language="en",
)
transcribed_text = response.text
assert len(transcribed_text) > 0  # Ensure transcription occurred
```

#### Round-Trip Validation

```python
# Complete round-trip test
original_text = "The quick brown fox jumps over the lazy dog."

# Step 1: Text to speech
speech_response = openai_client.audio.speech.create(
    model="tts-1",
    voice="alloy",
    input=original_text,
    response_format="wav",
)

# Step 2: Speech to text
transcription_response = openai_client.audio.transcriptions.create(
    model="whisper-1",
    file=("speech.wav", speech_response.content, "audio/wav"),
)

# Step 3: Validate similarity
transcribed_text = transcription_response.text
# Check for key word preservation (allowing for transcription variations)
```

### Error Handling Tests

#### Speech Synthesis Errors

```python
# Test invalid voice
with pytest.raises(Exception):
    openai_client.audio.speech.create(
        model="tts-1",
        voice="invalid_voice",
        input="This should fail",
    )

# Test empty input
with pytest.raises(Exception):
    openai_client.audio.speech.create(
        model="tts-1",
        voice="alloy",
        input="",
    )
```

#### Transcription Errors

```python
# Test invalid audio format
invalid_audio = b"This is not audio data"
with pytest.raises(Exception):
    openai_client.audio.transcriptions.create(
        model="whisper-1",
        file=("invalid.wav", invalid_audio, "audio/wav"),
    )

# Test unsupported file type
with pytest.raises(Exception):
    openai_client.audio.transcriptions.create(
        model="whisper-1",
        file=("test.txt", b"text content", "text/plain"),
    )
```

### Integration Support Matrix

| Integration | Speech Synthesis | Transcription | Streaming | Notes |
|------------|------------------|---------------|-----------|-------|
| OpenAI     | ✅ Full Support  | ✅ Full Support | 🔄 Varies | Complete implementation |
| Anthropic  | ❌ Not Available | ❌ Not Available | ❌ No    | No speech/audio APIs |
| Google     | ❌ Not Available* | ❌ Not Available* | ❌ No    | *Not through Gemini API |
| LiteLLM    | ✅ Via OpenAI    | ✅ Via OpenAI    | 🔄 Varies | Proxies to OpenAI |

*Note: Google offers speech services through separate APIs (Cloud Speech-to-Text, Cloud Text-to-Speech) that are not currently integrated.*

### Performance Considerations

#### Speech Synthesis
- **File Size**: Generated audio files range from 50KB to 5MB depending on length and quality
- **Generation Time**: Typically 2-10 seconds for short texts, longer for complex content
- **Format Impact**: WAV files are larger but offer better compatibility; MP3 is more compressed

#### Transcription
- **Processing Time**: Usually 1-5 seconds for short audio files (under 30 seconds)
- **File Size Limits**: Most services support files up to 25MB
- **Accuracy Factors**: Audio quality, background noise, speaker clarity affect results

### Best Practices

#### For Speech Testing
1. **Use consistent test text** for reproducible results
2. **Test multiple voices** to ensure voice switching works
3. **Validate audio headers** to confirm proper format generation
4. **Check file sizes** to ensure reasonable audio generation

#### For Transcription Testing
1. **Use high-quality test audio** for consistent transcription results
2. **Test various audio formats** (WAV, MP3, etc.) for compatibility
3. **Include silence and noise** tests for edge case handling
4. **Validate response formats** (JSON, text) as needed

#### For Round-Trip Testing
1. **Use simple, clear phrases** to maximize transcription accuracy
2. **Allow for minor variations** in transcribed text
3. **Focus on key word preservation** rather than exact matches
4. **Test with different voices** to ensure consistency across voice models

### Troubleshooting

#### Common Issues

1. **Audio Format Errors**
   ```bash
   # Check audio file headers
   file test_audio.wav
   # Should show: RIFF (little-endian) data, WAVE audio
   ```

2. **API Key Issues**
   ```bash
   # Verify OpenAI API key
   export OPENAI_API_KEY="your-key-here"
   python test_audio.py --test test_14_speech_synthesis
   ```

3. **Bifrost Configuration**
   ```bash
   # Ensure Bifrost is running and accessible
   curl http://localhost:8080/openai/v1/audio/speech -I
   ```

4. **Model Availability**
   ```python
   # Check if speech/transcription models are available
   from tests.utils.config_loader import get_model
   print("Speech model:", get_model("openai", "speech"))
   print("Transcription model:", get_model("openai", "transcription"))
   ```

#### Debug Commands

```bash
# Test individual components
uv run pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_14_speech_synthesis -v -s

# Check Bifrost logs for audio endpoint requests
# (Check your Bifrost instance logs)
```

## Getting Model Names

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
- ✅ **Models**: gpt-3.5-turbo, gpt-4, gpt-4o, gpt-4o-mini, text-embedding-3-small, tts-1, whisper-1
- ✅ **Features**: Chat, tools, vision, speech synthesis, transcription, embeddings
- ✅ **Settings**: Organization/project IDs, timeouts, retries
- ✅ **All Test Categories**: 30/30 scenarios supported (including speech & embeddings)

#### Anthropic

- ✅ **Full Bifrost Integration**: Complete base URL support
- ✅ **Models**: claude-3-haiku-20240307, claude-3-sonnet-20240229, claude-3-opus-20240229, claude-3-5-sonnet-20241022
- ✅ **Features**: Chat, tools, vision
- ✅ **Settings**: API version headers, timeouts, retries
- ✅ **All Test Categories**: 11/11 scenarios supported

#### Google AI

- ✅ **Full Bifrost Integration**: Complete custom transport implementation
- ✅ **Models**: gemini-2.0-flash-001, gemini-1.5-pro, gemini-1.5-flash, gemini-1.0-pro
- ✅ **Features**: Chat, tools, vision, multimodal processing
- ✅ **Settings**: Project ID, location, API configuration
- ✅ **All Test Categories**: 11/11 scenarios supported
- ✅ **Custom Base64 Handling**: Resolved cross-language encoding compatibility

#### LiteLLM

- ✅ **Full Bifrost Integration**: Global base URL configuration
- ✅ **Models**: Supports all LiteLLM-compatible models
- ✅ **Features**: Chat, tools, vision (integration-dependent)
- ✅ **Settings**: Drop params, debug mode, integration-specific configs
- ✅ **All Test Categories**: 11/11 scenarios supported
- ✅ **Multi-Integration**: OpenAI, Anthropic, Google, Azure, Cohere, Mistral, etc.

## 🧪 Running Tests

### Test Execution Methods

#### Using pytest with uv

```bash
# Run all tests for an integration
uv run pytest tests/integrations/test_openai.py -v

# Run specific test categories
uv run pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_01_simple_chat -v

# Run with coverage
uv run pytest tests/integrations/ --cov=tests --cov-report=html

# Run with custom markers
uv run pytest tests/integrations/ -m "not slow" -v
```

#### Selective Test Execution

```bash
# Skip tests that require API keys you don't have
uv run pytest tests/integrations/test_openai.py -v  # Will skip if OPENAI_API_KEY not set

# Run only specific test methods
uv run pytest tests/integrations/test_anthropic.py -k "tool_call" -v

# Run with timeout
uv run pytest tests/integrations/ --timeout=300 -v
```

### 🔍 Checking and Running Specific Tests

#### 🚀 Quick Commands (Most Common)

```bash
# Run specific test for specific integration
uv run pytest tests/integrations/test_google.py::TestGoogleIntegration::test_03_single_tool_call -v

# Run all tool calling tests across all integrations
uv run pytest -k "tool_call" -v

# Run all tests for one integration
uv run pytest tests/integrations/test_openai.py -v

# Run tests in parallel (faster)
uv run pytest -n auto

# Run with coverage
uv run pytest --cov=tests --cov-report=html -v
```

#### Quick Reference: Test Categories

```text
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

# Test 21: Single Text Embedding (OpenAI only)
pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_21_single_text_embedding -v

# Test 23: Embedding Similarity Analysis (OpenAI only)
pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_23_embedding_similarity_analysis -v
```

#### Running Test Categories by Pattern

```bash
# Run all simple chat tests across integrations
pytest tests/integrations/ -k "test_01_simple_chat" -v

# Run all tool calling tests (single and multiple)
pytest tests/integrations/ -k "tool_call" -v

# Run all image-related tests
pytest tests/integrations/ -k "image" -v

# Run all embedding tests (OpenAI only)
pytest tests/integrations/test_openai.py -k "embedding" -v

# Run all speech and audio tests (OpenAI only)
pytest tests/integrations/test_openai.py -k "speech or transcription" -v

# Run all end-to-end tests
pytest tests/integrations/ -k "end2end" -v

# Run integration-specific feature tests
pytest tests/integrations/ -k "integration_specific" -v
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
for model_type in ['chat', 'vision', 'tools']:
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
# Scenario 1: Test a new integration
# 1. Check configuration
uv run python tests/utils/config_loader.py

# 2. List available tests
uv run pytest tests/integrations/test_your_integration.py --collect-only

# 3. Run basic tests first
uv run pytest tests/integrations/test_your_integration.py -k "test_01 or test_02" -v

# 4. Test tool calling if supported
uv run pytest tests/integrations/test_your_integration.py -k "tool_call" -v

# Scenario 2: Debug a failing tool call test
# 1. Run with full debugging
uv run pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_03_single_tool_call -v -s --tb=long

# 2. Check tool extraction function
uv run python -c "
from tests.integrations.test_openai import extract_openai_tool_calls
print('Tool extraction function available:', callable(extract_openai_tool_calls))
"

# 3. Test with different model
OPENAI_CHAT_MODEL=gpt-4 uv run pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_03_single_tool_call -v

# Scenario 3: Compare integration capabilities
# Run the same test across all integrations
uv run pytest tests/integrations/ -k "test_01_simple_chat" -v --tb=short

# Scenario 4: Test only supported features
# For an integration that doesn't support images
uv run pytest tests/integrations/test_your_integration.py -k "not (test_07 or test_08 or test_09 or test_10)" -v

# Scenario 5: Performance testing
# Run with timing to identify slow tests
uv run pytest tests/integrations/test_openai.py --durations=0 -v

# Scenario 6: Continuous integration testing
# Run all tests with coverage and reports
uv run pytest tests/integrations/ --cov=tests --cov-report=xml --junit-xml=test_results.xml -v
```

#### Test Output Examples

```bash
# Successful test run
$ uv run pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_01_simple_chat -v
========================= test session starts =========================
tests/integrations/test_openai.py::TestOpenAIIntegration::test_01_simple_chat PASSED [100%]
✓ OpenAI simple chat test passed
Response: "Hello! I'm an AI assistant. How can I help you today?"

# Failed test with debugging info
$ uv run pytest tests/integrations/test_openai.py::TestOpenAIIntegration::test_03_single_tool_call -v -s
========================= FAILURES =========================
_____________ TestOpenAIIntegration.test_03_single_tool_call _____________
AssertionError: Expected tool calls but got none
Response content: "I can help with weather information, but I need a specific location."
Tool calls found: []

# Test collection output
$ uv run pytest tests/integrations/test_openai.py --collect-only -q
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

# Running all tests with summary
$ uv run pytest tests/integrations/test_google.py::TestGoogleIntegration::test_03_single_tool_call -v
========================= test session starts =========================
tests/integrations/test_google.py::TestGoogleIntegration::test_03_single_tool_call PASSED [100%]
✅ All tests passed

# Running tests in parallel
$ uv run pytest -n auto
========================= test session starts =========================
plugins: xdist-3.5.0, forked-2.0.0
gw0 [11] / gw1 [11] / gw2 [11] / gw3 [11]
...........                                                          [100%]
========================= 11 passed in 5.21s =========================
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

# Add model configuration
models:
  your_integration:
    chat: "your-chat-model"
    vision: "your-vision-model"
    tools: "your-tools-model"
    alternatives: ["alternative-model-1", "alternative-model-2"]

# Add model capabilities
model_capabilities:
  "your-chat-model":
    chat: true
    tools: true
    vision: false
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

#### 5. **Error Handling**

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

# 2. Ensure BIFROST_BASE_URL is set correctly
echo $BIFROST_BASE_URL
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

### Contributing

1. Fork the repository
2. Create feature branch: `git checkout -b feature/new-integration`
3. Follow the integration implementation guide above
4. Add comprehensive tests and documentation
5. Submit pull request with test results

## 🆘 Support

For issues and questions:

- Create GitHub issues for bugs and feature requests
- Check existing issues for solutions
- Review integration-specific documentation
- Test configuration with `python tests/utils/config_loader.py`

---

**Note**: This test suite is designed for testing AI integrations through Bifrost proxy. Ensure your Bifrost instance is properly configured and running before executing tests. The configuration system provides Bifrost routing for maximum flexibility.
