"""
Google GenAI Integration Tests - Cross-Provider Support

🌉 CROSS-PROVIDER TESTING:
This test suite uses the Google GenAI SDK to test against multiple AI providers through Bifrost.
Tests automatically run against all available providers with proper capability filtering.

Note: Tests automatically skip for providers that don't support specific capabilities.

Tests all core scenarios using Google GenAI SDK directly:
1. Simple chat
2. Multi turn conversation
3. Tool calls
4. Multiple tool calls
5. End2End tool calling
6. Automatic function calling
7. Image (url)
8. Image (base64)
9. Multiple images
10. Complete end2end test with conversation history, tool calls, tool results and images
11. Integration specific tests
12. Error handling
13. Streaming chat
14. Single text embedding
15. List models
16. Audio transcription
17. Audio transcription with parameters
18. Transcription with timestamps
19. Audio transcription inline
20. Audio transcription token count
21. Audio transcription different formats
22. Speech generation - single speaker
23. Speech generation - multi speaker
24. Speech generation - different voices
25. Speech generation - language support
26. Extended thinking/reasoning (non-streaming)
27. Extended thinking/reasoning (streaming)
28. Structured outputs with thinking_budget enabled
26. Speech generation - streaming (if supported)
27. Extended thinking/reasoning (non-streaming)
28. Extended thinking/reasoning (streaming)
26. Files API - file upload
27. Files API - file list
28. Files API - file retrieve
29. Files API - file delete
30. Batch API - batch create with file
31. Batch API - batch create inline
32. Batch API - batch list
33. Batch API - batch retrieve
34. Batch API - batch cancel
35. Batch API - end-to-end with Files API
"""

import pytest
import base64
import json
import requests
import tempfile
import os
from PIL import Image
import io
import wave
from google import genai
from google.genai.types import HttpOptions
from google.genai import types
from typing import List, Dict, Any

from .utils.common import (
    Config,
    SIMPLE_CHAT_MESSAGES,
    SINGLE_TOOL_CALL_MESSAGES,
    MULTIPLE_TOOL_CALL_MESSAGES,
    IMAGE_URL_SECONDARY,
    BASE64_IMAGE,
    INVALID_ROLE_MESSAGES,
    STREAMING_CHAT_MESSAGES,
    STREAMING_TOOL_CALL_MESSAGES,
    WEATHER_TOOL,
    CALCULATOR_TOOL,
    assert_valid_chat_response,
    assert_valid_embedding_response,
    assert_valid_image_response,
    assert_valid_error_response,
    assert_error_propagation,
    assert_valid_streaming_response,
    assert_valid_transcription_response,
    assert_valid_streaming_transcription_response,
    assert_valid_speech_response,
    collect_streaming_content,
    collect_streaming_transcription_content,
    generate_test_audio,
    get_provider_voice,
    get_provider_voices,
    get_api_key,
    skip_if_no_api_key,
    COMPARISON_KEYWORDS,
    WEATHER_KEYWORDS,
    LOCATION_KEYWORDS,
    GENAI_INVALID_ROLE_CONTENT,
    EMBEDDINGS_SINGLE_TEXT,
    SPEECH_TEST_INPUT,
    # Gemini-specific test data
    GEMINI_REASONING_PROMPT,
    GEMINI_REASONING_STREAMING_PROMPT,
    # Batch API utilities
    BATCH_INLINE_PROMPTS,
)
from .utils.config_loader import get_model
from .utils.parametrize import (
    get_cross_provider_params_for_scenario,
    format_provider_model,
)


def get_provider_google_client(provider: str = "gemini"):
    """Create Google GenAI client with x-model-provider header for given provider"""
    from .utils.config_loader import get_integration_url, get_config

    api_key = get_api_key(provider)
    base_url = get_integration_url("google")
    config = get_config()
    api_config = config.get_api_config()

    client_kwargs = {
        "api_key": api_key,
    }

    # Add base URL support, timeout, and x-model-provider header through HttpOptions
    http_options_kwargs = {
        "headers": {"x-model-provider": provider},
    }
    if base_url:
        http_options_kwargs["base_url"] = base_url
    if api_config.get("timeout"):
        http_options_kwargs["timeout"] = api_config.get("timeout", 300)

    client_kwargs["http_options"] = HttpOptions(**http_options_kwargs)

    return genai.Client(**client_kwargs)


@pytest.fixture
def google_client():
    """Configure Google GenAI client for testing with default gemini provider"""
    return get_provider_google_client(provider="gemini")


@pytest.fixture
def test_config():
    """Test configuration"""
    return Config()


def convert_to_google_messages(messages: List[Dict[str, Any]]) -> str:
    """Convert common message format to Google GenAI format"""
    # Google GenAI uses a simpler format - just extract the first user message
    for msg in messages:
        if msg["role"] == "user":
            if isinstance(msg["content"], str):
                return msg["content"]
            elif isinstance(msg["content"], list):
                # Handle multimodal content
                text_parts = [
                    item["text"] for item in msg["content"] if item["type"] == "text"
                ]
                if text_parts:
                    return text_parts[0]
    return "Hello"


def convert_to_google_tools(tools: List[Dict[str, Any]]) -> List[Any]:
    """Convert common tool format to Google GenAI format using FunctionDeclaration"""
    from google.genai import types

    google_tools = []

    for tool in tools:
        # Create a function declaration dictionary with lowercase types
        function_declaration = {
            "name": tool["name"],
            "description": tool["description"],
            "parameters": {
                "type": tool["parameters"]["type"].lower(),
                "properties": {
                    name: {
                        "type": prop["type"].lower(),
                        "description": prop.get("description", ""),
                    }
                    for name, prop in tool["parameters"]["properties"].items()
                },
                "required": tool["parameters"].get("required", []),
            },
        }

        # Create a Tool object containing the function declaration
        google_tool = types.Tool(function_declarations=[function_declaration])
        google_tools.append(google_tool)

    return google_tools


def load_image_from_url(url: str):
    """Load image from URL for Google GenAI"""
    from google.genai import types
    import io
    import base64

    if url.startswith("data:image"):
        # Base64 image - extract the base64 data part
        header, data = url.split(",", 1)
        img_data = base64.b64decode(data)
        image = Image.open(io.BytesIO(img_data))
    else:
        # URL image - use headers to avoid 403 errors from servers like Wikipedia
        headers = {
            'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36'
        }
        response = requests.get(url, headers=headers, timeout=30)
        response.raise_for_status()  # Raise an error for bad status codes
        image = Image.open(io.BytesIO(response.content))

    # Resize image to reduce payload size (max width/height of 512px)
    max_size = 512
    if image.width > max_size or image.height > max_size:
        image.thumbnail((max_size, max_size), Image.Resampling.LANCZOS)

    # Convert to RGB if necessary (for JPEG compatibility)
    if image.mode in ("RGBA", "LA", "P"):
        # Create a white background
        background = Image.new("RGB", image.size, (255, 255, 255))
        if image.mode == "P":
            image = image.convert("RGBA")
        background.paste(
            image, mask=image.split()[-1] if image.mode in ("RGBA", "LA") else None
        )
        image = background

    # Convert PIL Image to compressed JPEG bytes
    img_byte_arr = io.BytesIO()
    image.save(img_byte_arr, format="JPEG", quality=85, optimize=True)
    img_byte_arr = img_byte_arr.getvalue()

    # Use the correct Part.from_bytes method as per Google GenAI documentation
    return types.Part.from_bytes(data=img_byte_arr, mime_type="image/jpeg")


def convert_pcm_to_wav(pcm_data: bytes, channels: int = 1, sample_rate: int = 24000, sample_width: int = 2) -> bytes:
    """Convert raw PCM audio data to WAV format"""
    wav_buffer = io.BytesIO()
    with wave.open(wav_buffer, 'wb') as wav_file:
        wav_file.setnchannels(channels)
        wav_file.setsampwidth(sample_width)
        wav_file.setframerate(sample_rate)
        wav_file.writeframes(pcm_data)
    wav_buffer.seek(0)
    return wav_buffer.read()


# =============================================================================
# Google GenAI Files and Batch API Helper Functions
# =============================================================================

def create_google_batch_json_content(model: str, num_requests: int = 2) -> str:
    """
    Create JSON content for Google GenAI batch API.
    
    Google batch format uses newline-delimited JSON with 'key' and 'request' fields:
    {"key":"request_1", "request": {"contents": [{"parts": [{"text": "..."}]}]}}
    
    Args:
        model: The model to use (not included in request, passed to batches.create)
        num_requests: Number of requests to include
        
    Returns:
        Newline-delimited JSON string
    """
    requests_list = []
    
    for i in range(num_requests):
        prompt = BATCH_INLINE_PROMPTS[i % len(BATCH_INLINE_PROMPTS)]
        request = {
            "key": f"request_{i+1}",
            "request": {
                "contents": [
                    {
                        "parts": [{"text": prompt}],
                        "role": "user"
                    }
                ]
            }
        }
        requests_list.append(json.dumps(request))
    
    return "\n".join(requests_list)


def create_google_batch_inline_requests(num_requests: int = 2) -> List[Dict[str, Any]]:
    """
    Create inline requests for Google GenAI batch API.
    
    Args:
        num_requests: Number of requests to include
        
    Returns:
        List of inline request dictionaries
    """
    requests_list = []
    
    for i in range(num_requests):
        prompt = BATCH_INLINE_PROMPTS[i % len(BATCH_INLINE_PROMPTS)]
        request = {
            "contents": [
                {
                    "parts": [{"text": prompt}],
                    "role": "user"
                }
            ],
            "config": {"response_modalities": ["TEXT"]}
        }
        requests_list.append(request)
    
    return requests_list


# Google GenAI batch job states
GOOGLE_BATCH_VALID_STATES = [
    "JOB_STATE_UNSPECIFIED",
    "JOB_STATE_QUEUED",
    "JOB_STATE_PENDING",
    "JOB_STATE_RUNNING",
    "JOB_STATE_SUCCEEDED",
    "JOB_STATE_FAILED",
    "JOB_STATE_CANCELLING",
    "JOB_STATE_CANCELLED",
    "JOB_STATE_PAUSED",
]

GOOGLE_BATCH_TERMINAL_STATES = [
    "JOB_STATE_SUCCEEDED",
    "JOB_STATE_FAILED",
    "JOB_STATE_CANCELLED",
    "JOB_STATE_PAUSED",
]


def assert_valid_google_file_response(response, expected_display_name: str | None = None) -> None:
    """
    Assert that a Google GenAI file upload/retrieve response is valid.
    
    Args:
        response: The file response object
        expected_display_name: Expected display_name field (optional)
    """
    assert response is not None, "File response should not be None"
    assert hasattr(response, "name"), "File response should have 'name' attribute"
    assert response.name is not None, "File name should not be None"
    assert len(response.name) > 0, "File name should not be empty"
    
    # Google files have display_name instead of filename
    if hasattr(response, "display_name") and expected_display_name:
        assert response.display_name == expected_display_name, (
            f"Display name should be '{expected_display_name}', got {response.display_name}"
        )
    
    # Check for size_bytes if available
    if hasattr(response, "size_bytes"):
        assert response.size_bytes >= 0, "File size_bytes should be non-negative"


def assert_valid_google_batch_response(response, expected_state: str | None = None) -> None:
    """
    Assert that a Google GenAI batch create/retrieve response is valid.
    
    Args:
        response: The batch job response object
        expected_state: Expected state (optional)
    """
    assert response is not None, "Batch response should not be None"
    assert hasattr(response, "name"), "Batch response should have 'name' attribute"
    assert response.name is not None, "Batch job name should not be None"
    assert len(response.name) > 0, "Batch job name should not be empty"
    
    # Google uses 'state' instead of 'status'
    if hasattr(response, "state"):
        assert response.state in GOOGLE_BATCH_VALID_STATES, (
            f"State should be one of {GOOGLE_BATCH_VALID_STATES}, got {response.state}"
        )
        
        if expected_state:
            assert response.state == expected_state, (
                f"State should be '{expected_state}', got {response.state}"
            )


def assert_valid_google_batch_list_response(pager, min_count: int = 0) -> None:
    """
    Assert that a Google GenAI batch list response is valid.
    
    Args:
        pager: The batch list pager/iterator
        min_count: Minimum expected number of batches
    """
    assert pager is not None, "Batch list response should not be None"
    
    # Google returns a pager object, count items
    count = 0
    for job in pager:
        count += 1
        # Each job should have a name
        assert hasattr(job, "name"), "Each batch job should have 'name' attribute"
    
    assert count >= min_count, (
        f"Should have at least {min_count} batches, got {count}"
    )


class TestGoogleIntegration:
    """Test suite for Google GenAI integration with cross-provider support"""

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("simple_chat"))
    def test_01_simple_chat(self, google_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 1: Simple chat interaction - runs across all available providers"""
        message = convert_to_google_messages(SIMPLE_CHAT_MESSAGES)

        response = google_client.models.generate_content(
            model=format_provider_model(provider, model), contents=message
        )

        assert_valid_chat_response(response)
        assert response.text is not None
        assert len(response.text) > 0        

    @skip_if_no_api_key("google")
    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("multi_turn_conversation"))
    def test_02_multi_turn_conversation(self, google_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 2: Multi-turn conversation"""
        # Start a chat session for multi-turn
        chat = google_client.chats.create(model=format_provider_model(provider, model))

        # Send first message
        response1 = chat.send_message("What's the capital of France?")
        assert_valid_chat_response(response1)

        # Send follow-up message
        response2 = chat.send_message("What's the population of that city?")
        assert_valid_chat_response(response2)

        content = response2.text.lower()
        # Should mention population or numbers since we asked about Paris population
        assert any(
            word in content
            for word in ["population", "million", "people", "inhabitants"]
        )

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("tool_calls"))
    def test_03_single_tool_call(self, google_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 3: Single tool call - auto-skips providers without tool model"""
        from google.genai import types

        tools = convert_to_google_tools([WEATHER_TOOL])
        message = convert_to_google_messages(SINGLE_TOOL_CALL_MESSAGES)

        response = google_client.models.generate_content(
            model=format_provider_model(provider, model),
            contents=message,
            config=types.GenerateContentConfig(tools=tools),
        )

        # Check for function calls in response
        assert response.candidates is not None
        assert len(response.candidates) > 0

        # Check if function call was made (Google GenAI might return function calls)
        if hasattr(response, "function_calls") and response.function_calls:
            assert len(response.function_calls) >= 1
            assert response.function_calls[0].name == "get_weather"

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("multiple_tool_calls"))
    def test_04_multiple_tool_calls(self, google_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 4: Multiple tool calls in one response - auto-skips providers without tool model"""
        from google.genai import types

        tools = convert_to_google_tools([WEATHER_TOOL, CALCULATOR_TOOL])
        message = convert_to_google_messages(MULTIPLE_TOOL_CALL_MESSAGES)

        response = google_client.models.generate_content(
            model=format_provider_model(provider, model),
            contents=message,
            config=types.GenerateContentConfig(tools=tools),
        )

        # Check for function calls
        assert response.candidates is not None

        # Check if function calls were made
        if hasattr(response, "function_calls") and response.function_calls:
            # Should have multiple function calls
            assert len(response.function_calls) >= 1
            function_names = [fc.name for fc in response.function_calls]
            # At least one of the expected tools should be called
            assert any(name in ["get_weather", "calculate"] for name in function_names)

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("end2end_tool_calling"))
    def test_05_end2end_tool_calling(self, google_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 5: Complete tool calling flow with responses"""
        from google.genai import types

        tools = convert_to_google_tools([WEATHER_TOOL])

        # Start chat for tool calling flow
        chat = google_client.chats.create(model=format_provider_model(provider, model))

        response1 = chat.send_message(
            "What's the weather in Boston?",
            config=types.GenerateContentConfig(tools=tools),
        )

        # Check if function call was made
        if hasattr(response1, "function_calls") and response1.function_calls:
            # Simulate function execution and send result back
            for fc in response1.function_calls:
                if fc.name == "get_weather":
                    # Mock function result and send back
                    response2 = chat.send_message(
                        types.Part.from_function_response(
                            name=fc.name,
                            response={
                                "result": "The weather in Boston is 72°F and sunny."
                            },
                        )
                    )
                    assert_valid_chat_response(response2)

                    content = response2.text.lower()
                    weather_location_keywords = WEATHER_KEYWORDS + LOCATION_KEYWORDS
                    assert any(word in content for word in weather_location_keywords)

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("automatic_function_calling"))
    def test_06_automatic_function_calling(self, google_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 6: Automatic function calling"""
        from google.genai import types

        tools = convert_to_google_tools([CALCULATOR_TOOL])

        response = google_client.models.generate_content(
            model=format_provider_model(provider, model),
            contents="Calculate 25 * 4 for me",
            config=types.GenerateContentConfig(tools=tools),
        )

        # Should automatically choose to use the calculator
        assert response.candidates is not None

        # Check if function calls were made
        if hasattr(response, "function_calls") and response.function_calls:
            assert response.function_calls[0].name == "calculate"

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("image_url"))
    def test_07_image_url(self, google_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 7: Image analysis from URL"""
        image = load_image_from_url(IMAGE_URL_SECONDARY)


        response = google_client.models.generate_content(
            model=format_provider_model(provider, model),
            contents=["What do you see in this image?", image],
        )

        assert_valid_image_response(response)

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("image_base64"))
    def test_08_image_base64(self, google_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 8: Image analysis from base64"""
        image = load_image_from_url(f"data:image/png;base64,{BASE64_IMAGE}")

        response = google_client.models.generate_content(
            model=format_provider_model(provider, model), contents=["Describe this image", image]
        )

        assert_valid_image_response(response)

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("multiple_images"))
    def test_09_multiple_images(self, google_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 9: Multiple image analysis"""
        image1 = load_image_from_url(IMAGE_URL_SECONDARY)
        image2 = load_image_from_url(f"data:image/png;base64,{BASE64_IMAGE}")

        response = google_client.models.generate_content(
            model=format_provider_model(provider, model),
            contents=["Compare these two images", image1, image2],
        )

        assert_valid_image_response(response)
        content = response.text.lower()
        # Should mention comparison or differences
        assert any(
            word in content for word in COMPARISON_KEYWORDS
        ), f"Response should contain comparison keywords. Got content: {content}"


    def test_10_complex_end2end(self, google_client, test_config):
        """Test Case 10: Complex end-to-end with conversation, images, and tools"""
        from google.genai import types

        tools = convert_to_google_tools([WEATHER_TOOL])

        image = load_image_from_url(f"data:image/png;base64,{BASE64_IMAGE}")

        # Start complex conversation
        chat = google_client.chats.create(model=get_model("google", "vision"))

        response1 = chat.send_message(
            [
                "First, can you tell me what's in this image and then get the weather for the location shown?",
                image,
            ],
            config=types.GenerateContentConfig(tools=tools),
        )

        # Should either describe image or call weather tool (or both)
        assert response1.candidates is not None

        # Check for function calls and handle them
        if hasattr(response1, "function_calls") and response1.function_calls:
            for fc in response1.function_calls:
                if fc.name == "get_weather":
                    # Send function result back
                    final_response = chat.send_message(
                        types.Part.from_function_response(
                            name=fc.name,
                            response={"result": "The weather is 72°F and sunny."},
                        )
                    )
                    assert_valid_chat_response(final_response)

    @skip_if_no_api_key("google")
    def test_11_integration_specific_features(self, google_client, test_config):
        """Test Case 11: Google GenAI-specific features"""

        # Test 1: Generation config with temperature
        from google.genai import types

        response1 = google_client.models.generate_content(
            model=get_model("google", "chat"),
            contents="Tell me a creative story in one sentence.",
            config=types.GenerateContentConfig(temperature=0.9, max_output_tokens=1000),
        )

        assert_valid_chat_response(response1)

        # Test 2: Safety settings
        response2 = google_client.models.generate_content(
            model=get_model("google", "chat"),
            contents="Hello, how are you?",
            config=types.GenerateContentConfig(
                safety_settings=[
                    types.SafetySetting(
                        category="HARM_CATEGORY_HARASSMENT",
                        threshold="BLOCK_MEDIUM_AND_ABOVE",
                    )
                ]
            ),
        )

        assert_valid_chat_response(response2)

        # Test 3: System instruction
        response3 = google_client.models.generate_content(
            model=get_model("google", "chat"),
            contents="high",
            config=types.GenerateContentConfig(
                system_instruction="I say high, you say low",
                max_output_tokens=500,
            ),
        )

        assert_valid_chat_response(response3)

    @skip_if_no_api_key("google")
    def test_12_error_handling_invalid_roles(self, google_client, test_config):
        """Test Case 12: Error handling for invalid roles"""
        response = google_client.models.generate_content(
            model=get_model("google", "chat"), contents=GENAI_INVALID_ROLE_CONTENT
        )

        # Verify the response is successful
        assert response is not None
        assert hasattr(response, "candidates")
        assert len(response.candidates) > 0

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("streaming"))
    def test_13_streaming(self, google_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 13: Streaming chat completion - auto-skips providers without streaming model"""

        # Use the correct Google GenAI SDK streaming method
        stream = google_client.models.generate_content_stream(
            model=format_provider_model(provider, model),
            contents="Tell me a short story about a robot",
        )

        content = ""
        chunk_count = 0

        # Collect streaming content
        for chunk in stream:
            chunk_count += 1
            # Google GenAI streaming returns chunks with candidates containing parts with text
            if hasattr(chunk, 'candidates') and chunk.candidates:
                for candidate in chunk.candidates:
                    if hasattr(candidate, 'content') and candidate.content:
                        if hasattr(candidate.content, 'parts') and candidate.content.parts:
                            for part in candidate.content.parts:
                                if hasattr(part, 'text') and part.text:
                                    content += part.text
            # Fallback to direct text attribute (for compatibility)
            elif hasattr(chunk, 'text') and chunk.text:
                content += chunk.text

        # Validate streaming results
        assert chunk_count > 0, "Should receive at least one chunk"
        assert len(content) > 5, "Should receive substantial content"

        # Check for robot-related terms (the story might not use the exact word "robot")
        robot_terms = [
            "robot",
            "metallic",
            "programmed",
            "unit",
            "custodian",
            "mechanical",
            "android",
            "machine",
        ]
        has_robot_content = any(term in content.lower() for term in robot_terms)
        assert (
            has_robot_content
        ), f"Content should relate to robots. Found content: {content[:200]}..."
    
    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("embeddings"))
    def test_14_single_text_embedding(self, google_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 21: Single text embedding generation"""
        response = google_client.models.embed_content(
            model=format_provider_model(provider, model), contents=EMBEDDINGS_SINGLE_TEXT,
            config=types.EmbedContentConfig(output_dimensionality=1536)
        )

        assert_valid_embedding_response(response, expected_dimensions=1536)

        # Verify response structure
        assert len(response.embeddings) == 1, "Should have exactly one embedding"
    
    @skip_if_no_api_key("google")
    def test_15_list_models(self, google_client, test_config):
        """Test Case 15: List models"""
        response = google_client.models.list(config={"page_size": 5})
        assert response is not None
        assert len(response) <= 5

    @skip_if_no_api_key("google")
    def test_16_transcription_basic(self, google_client, test_config):
        """Test Case 16: Basic audio transcription (speech-to-text)"""
        from google.genai import types
        
        # Generate test audio for transcription
        test_audio = generate_test_audio()
        
        # Basic transcription test using Gemini model with inline audio
        response = google_client.models.generate_content(
            model=get_model("google", "transcription"),
            contents=[
                "Generate a transcript of the speech in this audio file.",
                types.Part.from_bytes(
                    data=test_audio,
                    mime_type="audio/wav"
                )
            ]
        )
        
        assert_valid_transcription_response(response)

        assert response.text is not None

    @skip_if_no_api_key("google")
    def test_17_transcription_with_parameters(self, google_client, test_config):
        """Test Case 17: Audio transcription with additional parameters"""
        from google.genai import types
        
        # Generate test audio
        test_audio = generate_test_audio()
        
        # Test with language specification and custom prompt using inline audio
        response = google_client.models.generate_content(
            model=get_model("google", "transcription"),
            contents=[
                "Transcribe the audio in English. The audio may contain technical terms.",
                types.Part.from_bytes(
                    data=test_audio,
                    mime_type="audio/wav"
                )
            ],
            config=types.GenerateContentConfig(
                temperature=0.0,  # More deterministic
            )
        )
        
        assert_valid_transcription_response(response)
        assert response.text is not None

    @skip_if_no_api_key("google")
    def test_18_transcription_with_timestamps(self, google_client, test_config):
        """Test Case 18: Audio transcription with timestamp references"""
        from google.genai import types
        
        # Generate test audio
        test_audio = generate_test_audio()
        
        # Test transcription with timestamp reference using inline audio
        # Gemini supports MM:SS format for timestamp references
        response = google_client.models.generate_content(
            model=get_model("google", "transcription"),
            contents=[
                "Provide a transcript of the speech from 00:00 to 00:02.",
                types.Part.from_bytes(
                    data=test_audio,
                    mime_type="audio/wav"
                )
            ]
        )
        
        assert_valid_transcription_response(response, min_text_length=0)
        assert response.text is not None

    @skip_if_no_api_key("google")
    def test_19_transcription_inline(self, google_client, test_config):
        """Test Case 19: Audio transcription with inline audio data"""
        from google.genai import types
        
        # Generate small test audio for inline upload (< 20MB)
        test_audio = generate_test_audio()
        
        # Use inline audio data (Part.from_bytes)
        response = google_client.models.generate_content(
            model=get_model("google", "transcription"),
            contents=[
                "Describe and transcribe this audio clip.",
                types.Part.from_bytes(
                    data=test_audio,
                    mime_type="audio/wav"
                )
            ]
        )
        
        assert_valid_transcription_response(response, min_text_length=0)
        assert response.text is not None


    @skip_if_no_api_key("google")
    def test_21_transcription_different_formats(self, google_client, test_config):
        """Test Case 21: Audio transcription with different audio formats"""
        from google.genai import types
        
        # Test with WAV format (already tested above, but let's be explicit)
        test_audio = generate_test_audio()
        
        # Test inline with WAV
        response_wav = google_client.models.generate_content(
            model=get_model("google", "transcription"),
            contents=[
                "Transcribe this audio.",
                types.Part.from_bytes(
                    data=test_audio,
                    mime_type="audio/wav"
                )
            ]
        )
        
        assert_valid_transcription_response(response_wav, min_text_length=0)
        assert response_wav.text is not None

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("speech_synthesis"))
    def test_22_speech_generation_single_speaker(self, google_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 22: Single-speaker text-to-speech generation"""
        from google.genai import types
        
        # Basic single-speaker TTS test
        response = google_client.models.generate_content(
            model=format_provider_model(provider, model),
            contents="Say cheerfully: Have a wonderful day!",
            config=types.GenerateContentConfig(
                response_modalities=["AUDIO"],
                speech_config=types.SpeechConfig(
                    voice_config=types.VoiceConfig(
                        prebuilt_voice_config=types.PrebuiltVoiceConfig(
                            voice_name=get_provider_voice(provider, "primary"),
                        )
                    )
                ),
            )
        )
        
        # Extract audio data from response
        assert response.candidates is not None, "Response should have candidates"
        assert len(response.candidates) > 0, "Should have at least one candidate"
        
        audio_data = response.candidates[0].content.parts[0].inline_data.data
        assert audio_data is not None, "Should have audio data"
        assert isinstance(audio_data, bytes), "Audio data should be bytes"
        
        # Convert PCM to WAV for validation
        wav_audio = convert_pcm_to_wav(audio_data)
        assert_valid_speech_response(wav_audio, expected_audio_size_min=1000)
        
    def test_23_speech_generation_multi_speaker(self, google_client, test_config):
        """Test Case 23: Multi-speaker text-to-speech generation"""
        from google.genai import types
        
        # Multi-speaker conversation
        prompt = """TTS the following conversation between Joe and Jane.

Joe: Hi Jane, how are you doing today?
Jane: I'm doing great, Joe! How about you?
Joe: Pretty good, thanks for asking."""
        
        response = google_client.models.generate_content(
            model=get_model("google", "speech"),
            contents=prompt,
            config=types.GenerateContentConfig(
                response_modalities=["AUDIO"],
                speech_config=types.SpeechConfig(
                    multi_speaker_voice_config=types.MultiSpeakerVoiceConfig(
                        speaker_voice_configs=[
                            types.SpeakerVoiceConfig(
                                speaker='Joe',
                                voice_config=types.VoiceConfig(
                                    prebuilt_voice_config=types.PrebuiltVoiceConfig(
                                        voice_name=get_provider_voice("google", "secondary"),
                                    )
                                )
                            ),
                            types.SpeakerVoiceConfig(
                                speaker='Jane',
                                voice_config=types.VoiceConfig(
                                    prebuilt_voice_config=types.PrebuiltVoiceConfig(
                                        voice_name=get_provider_voice("google", "primary"),
                                    )
                                )
                            ),
                        ]
                    )
                )
            )
        )
        
        # Extract and validate audio
        assert response.candidates is not None
        assert len(response.candidates) > 0
        
        audio_data = response.candidates[0].content.parts[0].inline_data.data
        assert audio_data is not None
        assert isinstance(audio_data, bytes)
        
        # Multi-speaker audio should be longer than single speaker
        wav_audio = convert_pcm_to_wav(audio_data)
        assert_valid_speech_response(wav_audio, expected_audio_size_min=2000)

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("speech_synthesis"))
    def test_24_speech_generation_different_voices(self, google_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 24: Test different voice options for TTS"""
        from google.genai import types
        
        test_text = "This is a test of the speech synthesis functionality."
        
        # Test a few different voices
        voices_to_test = get_provider_voices(provider, count=4)
        voice_audio_sizes = []
        
        for voice_name in voices_to_test:
            response = google_client.models.generate_content(
                model=format_provider_model(provider, model),
                contents=test_text,
                config=types.GenerateContentConfig(
                    response_modalities=["AUDIO"],
                    speech_config=types.SpeechConfig(
                        voice_config=types.VoiceConfig(
                            prebuilt_voice_config=types.PrebuiltVoiceConfig(
                                voice_name=voice_name,
                            )
                        )
                    ),
                )
            )
            
            # Extract audio data
            audio_data = response.candidates[0].content.parts[0].inline_data.data
            assert audio_data is not None
            
            # Convert and validate
            wav_audio = convert_pcm_to_wav(audio_data)
            assert_valid_speech_response(wav_audio, expected_audio_size_min=1000)
            
            voice_audio_sizes.append((voice_name, len(audio_data)))
        
        # Verify all voices produced valid audio
        assert len(voice_audio_sizes) == len(voices_to_test), "All voices should produce audio"
        
        # Verify audio sizes are reasonable (different voices may produce slightly different sizes)
        for voice_name, size in voice_audio_sizes:
            assert size > 5000, f"Voice {voice_name} should produce substantial audio"

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("speech_synthesis"))
    def test_25_speech_generation_language_support(self, google_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 25: Test TTS with different languages"""
        from google.genai import types
        
        # Test with different languages (auto-detected by the model)
        test_texts = {
            "English": "Hello, how are you today?",
            "Spanish": "Hola, ¿cómo estás hoy?",
            "French": "Bonjour, comment allez-vous aujourd'hui?",
            "German": "Hallo, wie geht es dir heute?",
        }
        
        # Test at least English and one other language
        for language, text in list(test_texts.items())[:2]:
            response = google_client.models.generate_content(
                model=format_provider_model(provider, model),
                contents=text,
                config=types.GenerateContentConfig(
                    response_modalities=["AUDIO"],
                    speech_config=types.SpeechConfig(
                        voice_config=types.VoiceConfig(
                            prebuilt_voice_config=types.PrebuiltVoiceConfig(
                                voice_name=get_provider_voice(provider, "primary"),
                            )
                        )
                    ),
                )
            )
            
            # Extract and validate audio
            audio_data = response.candidates[0].content.parts[0].inline_data.data
            assert audio_data is not None, f"Should have audio for {language}"
            
            wav_audio = convert_pcm_to_wav(audio_data)
            assert_valid_speech_response(wav_audio, expected_audio_size_min=1000)

    @skip_if_no_api_key("google")
    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("thinking"))
    def test_26_extended_thinking(self, google_client, test_config, provider, model):
        """Test Case 26: Extended thinking/reasoning (non-streaming)"""
        from google.genai import types
        
        # Convert to Google GenAI message format
        messages = GEMINI_REASONING_PROMPT[0]["content"]
        
        # Use a thinking-capable model (Gemini 2.0+ supports thinking)
        response = google_client.models.generate_content(
            model=format_provider_model(provider, model),
            contents=messages,
            config=types.GenerateContentConfig(
                thinking_config=types.ThinkingConfig(
                    include_thoughts=True,
                    thinking_budget=2000,
                ),
                max_output_tokens=2500,
            ),
        )
        
        # Validate response structure
        assert response is not None, "Response should not be None"
        assert hasattr(response, "candidates"), "Response should have candidates"
        assert len(response.candidates) > 0, "Should have at least one candidate"
        
        candidate = response.candidates[0]
        assert hasattr(candidate, "content"), "Candidate should have content"
        assert hasattr(candidate.content, "parts"), "Content should have parts"
        
        # Check for thoughts in usage metadata
        if provider == "gemini":
            has_thoughts = False
            thoughts_token_count = 0
            
            if hasattr(response, "usage_metadata"):
                usage = response.usage_metadata
                if hasattr(usage, "thoughts_token_count"):
                    thoughts_token_count = usage.thoughts_token_count
                    has_thoughts = thoughts_token_count > 0
                    print(f"Found thoughts with {thoughts_token_count} tokens")
            
            # Should have thinking/thoughts tokens
            assert has_thoughts, (
                f"Response should contain thoughts/reasoning tokens. "
                f"Usage metadata: {response.usage_metadata if hasattr(response, 'usage_metadata') else 'None'}"
            )
        
        # Validate that we have a response (even if thoughts aren't directly visible in parts)
        # In Gemini, thoughts are counted but may not be directly exposed in the response
        regular_text = ""
        for part in candidate.content.parts:
            if hasattr(part, "text") and part.text:
                regular_text += part.text
        
        # Should have regular response text
        assert len(regular_text) > 0, "Should have regular response text"
        
        print(f"✓ Response content: {regular_text[:200]}...")
        
        # Validate the response makes sense for the problem
        response_lower = regular_text.lower()
        reasoning_keywords = [
            "egg", "milk", "chicken", "cow", "profit", 
            "cost", "revenue", "week", "calculate", "total"
        ]
        
        keyword_matches = sum(
            1 for keyword in reasoning_keywords if keyword in response_lower
        )
        assert keyword_matches >= 3, (
            f"Response should address the farmer problem. "
            f"Found {keyword_matches} keywords. Content: {regular_text[:200]}..."
        )

    @skip_if_no_api_key("google")
    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("thinking"))
    def test_27_extended_thinking_streaming(self, google_client, test_config, provider, model):
        """Test Case 27: Extended thinking/reasoning (streaming)"""
        from google.genai import types
        
        # Convert to Google GenAI message format
        messages = GEMINI_REASONING_STREAMING_PROMPT[0]["content"]
        
        # Stream with thinking enabled
        stream = google_client.models.generate_content_stream(
            model=format_provider_model(provider, model),
            contents=messages,
            config=types.GenerateContentConfig(
                thinking_config=types.ThinkingConfig(
                    include_thoughts=True,
                    thinking_budget=2000,
                ),
                max_output_tokens=2500,
            ),
        )
        
        # Collect streaming content
        text_parts = []
        chunk_count = 0
        final_usage = None
        
        for chunk in stream:
            chunk_count += 1
            
            # Collect text content
            if hasattr(chunk, "candidates") and chunk.candidates is not None and len(chunk.candidates) > 0:
                candidate = chunk.candidates[0]
                if hasattr(candidate, "content") and hasattr(candidate.content, "parts") and candidate.content.parts:
                    for part in candidate.content.parts:
                        if hasattr(part, "text") and part.text:
                            text_parts.append(part.text)
            
            # Capture final usage metadata
            if hasattr(chunk, "usage_metadata"):
                final_usage = chunk.usage_metadata
            
            # Safety check
            if chunk_count > 500:
                raise AssertionError("Received >500 streaming chunks; possible non-terminating stream")
        
        # Combine collected content
        complete_text = "".join(text_parts)
        
        # Validate results
        assert chunk_count > 0, "Should receive at least one chunk"
        assert final_usage is not None, "Should have usage metadata"
        
        # Check for thoughts in usage metadata
        if provider == "gemini":
            has_thoughts = False
            thoughts_token_count = 0
            
            if hasattr(final_usage, "thoughts_token_count"):
                thoughts_token_count = final_usage.thoughts_token_count
                has_thoughts = thoughts_token_count > 0
                print(f"Found thoughts with {thoughts_token_count} tokens")
            
            assert has_thoughts, (
                f"Response should contain thoughts/reasoning tokens. "
                f"Usage metadata: {final_usage if hasattr(final_usage, 'thoughts_token_count') else 'None'}"
            )
        
        # Should have regular response text too
        assert len(complete_text) > 0, "Should have regular response text"
        
        # Validate thinking content
        text_lower = complete_text.lower()
        library_keywords = [
            "book", "library", "lent", "return", "donation",
            "total", "available", "inventory", "calculate", "percent"
        ]
        
        keyword_matches = sum(
            1 for keyword in library_keywords if keyword in text_lower
        )
        assert keyword_matches >= 3, (
            f"Response should reason about the library problem. "
            f"Found {keyword_matches} keywords. Content: {complete_text[:200]}..."
        )
        
        print(f"✓ Streamed response ({len(text_parts)} chunks): {complete_text[:150]}...")

    @skip_if_no_api_key("google")
    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("thinking"))
    def test_28_structured_output_with_thinking(self, google_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 28: Structured outputs with thinking_budget enabled
        """
        from google.genai import types
        from pydantic import BaseModel
        
        # Define a Pydantic model for structured output
        class MathProblem(BaseModel):
            problem_statement: str
            reasoning_steps: list[str]
            final_answer: int
            confidence: str
        
        messages = "A farmer sells eggs at $3 per dozen and milk at $5 per gallon. " \
                   "In a week, she sells 20 dozen eggs and 15 gallons of milk. " \
                   "Calculate her total weekly revenue. Show your reasoning steps."
        
        response = google_client.models.generate_content(
            model=format_provider_model(provider, model),
            contents=messages,
            config=types.GenerateContentConfig(
                response_mime_type="application/json",
                response_schema=MathProblem,
                thinking_config=types.ThinkingConfig(
                    include_thoughts=True,
                    thinking_budget=2400,
                ),
                max_output_tokens=3000,
            ),
        )

        # Validate response structure
        assert response is not None, "Response should not be None"
        
        assert hasattr(response, "parsed"), "Response should have 'parsed' attribute"
        
        parsed = response.parsed
        assert parsed is not None, (
            "The response returned narrative text instead of JSON matching the schema. "
            "Check the raw response text to confirm."
        )
        
        # Validate it's an instance of our Pydantic model
        assert isinstance(parsed, MathProblem), (
            f"response.parsed should be a MathProblem instance, got {type(parsed)}"
        )
        
        # Validate the fields exist and have correct types
        assert hasattr(parsed, "problem_statement"), "Should have problem_statement field"
        assert hasattr(parsed, "reasoning_steps"), "Should have reasoning_steps field"
        assert hasattr(parsed, "final_answer"), "Should have final_answer field"
        assert hasattr(parsed, "confidence"), "Should have confidence field"
        
        assert isinstance(parsed.problem_statement, str), "problem_statement should be string"
        assert isinstance(parsed.reasoning_steps, list), "reasoning_steps should be list"
        assert isinstance(parsed.final_answer, int), "final_answer should be int"
        assert isinstance(parsed.confidence, str), "confidence should be string"
        
        # Check that reasoning steps were provided
        assert len(parsed.reasoning_steps) > 0, "Should have at least one reasoning step"
        
        # Verify thinking tokens were counted (Gemini only)
        if provider == "gemini" and hasattr(response, "usage_metadata"):
            usage = response.usage_metadata
            if hasattr(usage, "thoughts_token_count"):
                thoughts_token_count = usage.thoughts_token_count
                print(f"✓ Thinking tokens used: {thoughts_token_count}")
        
        print("✓ Structured output with thinking works correctly!")
        print(f"  Problem: {parsed.problem_statement[:80]}...")
        print(f"  Steps: {len(parsed.reasoning_steps)} reasoning steps")
        print(f"  Answer: {parsed.final_answer}")
        print(f"  Confidence: {parsed.confidence}")


    # =========================================================================
    # FILES API TEST CASES
    # =========================================================================

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("batch_file_upload"))
    def test_26_file_upload(self, test_config, provider, model):
        """Test Case 26: Upload a file for batch processing"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_file_upload scenario")
        
        # Get provider-specific client
        client = get_provider_google_client(provider)
        
        # Create JSON content for batch
        json_content = create_google_batch_json_content(model=model, num_requests=2)
        
        # Write to a temporary file
        with tempfile.NamedTemporaryFile(mode='w', suffix='.json', delete=False) as f:
            f.write(json_content)
            temp_file_path = f.name
        
        response = None
        try:
            print(f"Uploading file to {temp_file_path}")
            # Upload the file
            response = client.files.upload(
                file=temp_file_path,
                config=types.UploadFileConfig(display_name=f'batch_test_{provider}')
            )
            
            print(f"Response: {response}")
            # Validate response
            assert_valid_google_file_response(response)
            
            print(f"Success: Uploaded file with name: {response.name} for provider {provider}")
            
            # Verify file exists in list
            found = False
            for f in client.files.list(config={'page_size': 50}):
                if f.name == response.name:
                    found = True
                    break
            
            assert found, f"Uploaded file {response.name} should be in file list"
            print(f"Success: Verified file {response.name} exists in file list")
            
        finally:
            # Clean up local temp file
            if os.path.exists(temp_file_path):
                os.remove(temp_file_path)
            
            # Clean up uploaded file
            if response is not None:
                try:
                    client.files.delete(name=response.name)
                except Exception as e:
                    print(f"Warning: Failed to clean up file: {e}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("file_list"))
    def test_27_file_list(self, test_config, provider, model):
        """Test Case 27: List uploaded files"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for file_list scenario")
        
        # Get provider-specific client
        client = get_provider_google_client(provider)
        
        # First upload a file to ensure we have at least one
        json_content = create_google_batch_json_content(model=model, num_requests=1)
        
        with tempfile.NamedTemporaryFile(mode='w', suffix='.json', delete=False) as f:
            f.write(json_content)
            temp_file_path = f.name
        
        uploaded_file = None
        try:
            uploaded_file = client.files.upload(
                file=temp_file_path,
                config=types.UploadFileConfig(display_name=f'list_test_{provider}')
            )
            
            # List files
            file_count = 0
            found_uploaded = False
            
            for f in client.files.list(config={'page_size': 50}):
                file_count += 1
                if f.name == uploaded_file.name:
                    found_uploaded = True
            
            assert file_count >= 1, "Should have at least one file"
            assert found_uploaded, f"Uploaded file {uploaded_file.name} should be in file list"
            
            print(f"Success: Listed {file_count} files for provider {provider}")
            
        finally:
            # Clean up
            if os.path.exists(temp_file_path):
                os.remove(temp_file_path)
            if uploaded_file is not None:
                try:
                    client.files.delete(name=uploaded_file.name)
                except Exception as e:
                    print(f"Warning: Failed to clean up file: {e}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("file_retrieve"))
    def test_28_file_retrieve(self, test_config, provider, model):
        """Test Case 28: Retrieve file metadata by name"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for file_retrieve scenario")
        
        # Get provider-specific client
        client = get_provider_google_client(provider)
        
        # First upload a file
        json_content = create_google_batch_json_content(model=model, num_requests=1)
        
        with tempfile.NamedTemporaryFile(mode='w', suffix='.json', delete=False) as f:
            f.write(json_content)
            temp_file_path = f.name
        
        uploaded_file = None
        try:
            uploaded_file = client.files.upload(
                file=temp_file_path,
                config=types.UploadFileConfig(display_name=f'retrieve_test_{provider}')
            )
            
            # Retrieve file metadata
            response = client.files.get(name=uploaded_file.name)
            
            # Validate response
            assert_valid_google_file_response(response)
            assert response.name == uploaded_file.name, (
                f"Retrieved file name should match: expected {uploaded_file.name}, got {response.name}"
            )
            
            print(f"Success: Retrieved file metadata for {response.name}")
            
        finally:
            # Clean up
            if os.path.exists(temp_file_path):
                os.remove(temp_file_path)
            if uploaded_file is not None:
                try:
                    client.files.delete(name=uploaded_file.name)
                except Exception as e:
                    print(f"Warning: Failed to clean up file: {e}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("file_delete"))
    def test_29_file_delete(self, test_config, provider, model):
        """Test Case 29: Delete an uploaded file"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for file_delete scenario")
        
        # Get provider-specific client
        client = get_provider_google_client(provider)
        
        # First upload a file
        json_content = create_google_batch_json_content(model=model, num_requests=1)
        
        with tempfile.NamedTemporaryFile(mode='w', suffix='.json', delete=False) as f:
            f.write(json_content)
            temp_file_path = f.name
        
        uploaded_file = None
        try:
            uploaded_file = client.files.upload(
                file=temp_file_path,
                config=types.UploadFileConfig(display_name=f'delete_test_{provider}')
            )
            
            file_name = uploaded_file.name
            
            # Delete the file
            client.files.delete(name=file_name)
            
            print(f"Success: Deleted file {file_name}")
            
            # Verify file is no longer retrievable
            with pytest.raises(Exception):
                client.files.get(name=file_name)
            
            print(f"Success: Verified file {file_name} no longer exists")
            
        finally:
            # Clean up local temp file
            if os.path.exists(temp_file_path):
                os.remove(temp_file_path)

    # =========================================================================
    # BATCH API TEST CASES
    # =========================================================================

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("batch_file_upload"))
    def test_30_batch_create_with_file(self, test_config, provider, model):
        """Test Case 30: Create a batch job using uploaded file
        
        This test uploads a JSON file first, then creates a batch using the file reference.
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_file_upload scenario")
        
        # Get provider-specific client
        client = get_provider_google_client(provider)
        
        # Create JSON content for batch
        json_content = create_google_batch_json_content(model=model, num_requests=2)
        
        # Write to a temporary file
        with tempfile.NamedTemporaryFile(mode='w', suffix='.json', delete=False) as f:
            f.write(json_content)
            temp_file_path = f.name
        
        batch_job = None
        uploaded_file = None
        
        try:
            # Upload the file
            uploaded_file = client.files.upload(
                file=temp_file_path,
                config=types.UploadFileConfig(display_name=f'batch_file_test_{provider}')
            )
            
            # Create batch job using file reference
            batch_job = client.batches.create(
                model=format_provider_model(provider, model),
                src=uploaded_file.name,
            )
            
            # Validate response
            assert_valid_google_batch_response(batch_job)
            
            print(f"Success: Created file-based batch with name: {batch_job.name}, state: {batch_job.state} for provider {provider}")
            
        finally:
            # Clean up local temp file
            if os.path.exists(temp_file_path):
                os.remove(temp_file_path)
            
            # Clean up batch job
            if batch_job:
                try:
                    client.batches.delete(name=batch_job.name)
                except Exception as e:
                    print(f"Info: Could not delete batch: {e}")
            
            # Clean up uploaded file
            if uploaded_file:
                try:
                    client.files.delete(name=uploaded_file.name)
                except Exception as e:
                    print(f"Warning: Failed to clean up file: {e}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("batch_inline"))
    def test_31_batch_create_inline(self, test_config, provider, model):
        """Test Case 31: Create a batch job with inline requests"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_inline scenario")
        
        # Get provider-specific client
        client = get_provider_google_client(provider)
        
        batch_job = None
        
        try:
            # Create inline requests
            inline_requests = create_google_batch_inline_requests(num_requests=2)
            
            # Create batch job with inline requests
            batch_job = client.batches.create(
                model=format_provider_model(provider, model),
                src=inline_requests,
            )
            
            # Validate response
            assert_valid_google_batch_response(batch_job)
            
            print(f"Success: Created inline batch with name: {batch_job.name}, state: {batch_job.state} for provider {provider}")
            
        finally:
            # Clean up batch job
            if batch_job:
                try:
                    client.batches.delete(name=batch_job.name)
                except Exception as e:
                    print(f"Info: Could not delete batch: {e}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("batch_list"))
    def test_32_batch_list(self, test_config, provider, model):
        """Test Case 32: List batch jobs"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_list scenario")
        
        # Get provider-specific client
        client = get_provider_google_client(provider)
        
        # List batch jobs
        batch_count = 0
        for job in client.batches.list(config=types.ListBatchJobsConfig(page_size=10)):
            batch_count += 1
            # Each job should have a name and state
            assert hasattr(job, "name"), "Batch job should have 'name' attribute"
            if batch_count >= 10:  # Limit iteration for the test
                break
        
        print(f"Success: Listed {batch_count} batches for provider {provider}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("batch_retrieve"))
    def test_33_batch_retrieve(self, test_config, provider, model):
        """Test Case 33: Retrieve batch status by name"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_retrieve scenario")
        
        # Get provider-specific client
        client = get_provider_google_client(provider)
        
        batch_job = None
        
        try:
            # Create inline requests for batch
            inline_requests = create_google_batch_inline_requests(num_requests=1)
            
            # Create batch job
            batch_job = client.batches.create(
                model=format_provider_model(provider, model),
                src=inline_requests,
            )
            
            # Retrieve batch job by name
            retrieved_job = client.batches.get(name=batch_job.name)
            
            # Validate response
            assert_valid_google_batch_response(retrieved_job)
            assert retrieved_job.name == batch_job.name, (
                f"Retrieved batch name should match: expected {batch_job.name}, got {retrieved_job.name}"
            )
            
            print(f"Success: Retrieved batch {batch_job.name}, state: {retrieved_job.state} for provider {provider}")
            
        finally:
            # Clean up
            if batch_job:
                try:
                    client.batches.delete(name=batch_job.name)
                except Exception as e:
                    print(f"Info: Could not delete batch: {e}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("batch_cancel"))
    def test_34_batch_cancel(self, test_config, provider, model):
        """Test Case 34: Cancel a batch job"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_cancel scenario")
        
        # Get provider-specific client
        client = get_provider_google_client(provider)
        
        batch_job = None
        
        try:
            # Create inline requests for batch
            inline_requests = create_google_batch_inline_requests(num_requests=2)
            
            # Create batch job
            batch_job = client.batches.create(
                model=format_provider_model(provider, model),
                src=inline_requests,
            )
            
            # Cancel the batch job
            cancelled_job = client.batches.cancel(name=batch_job.name)
            
            # Validate response - job should be cancelling or cancelled
            assert cancelled_job is not None, "Cancel should return a response"
            
            # Check state after cancel
            retrieved_job = client.batches.get(name=batch_job.name)
            assert retrieved_job.state in ["JOB_STATE_CANCELLING", "JOB_STATE_CANCELLED"], (
                f"Job state should be cancelling or cancelled, got {retrieved_job.state}"
            )
            
            print(f"Success: Cancelled batch {batch_job.name}, state: {retrieved_job.state} for provider {provider}")
            
        finally:
            # Clean up
            if batch_job:
                try:
                    client.batches.delete(name=batch_job.name)
                except Exception as e:
                    print(f"Info: Could not delete batch: {e}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("batch_file_upload"))
    def test_35_batch_e2e_file_api(self, test_config, provider, model):
        """Test Case 35: End-to-end batch workflow using Files API
        
        Complete workflow: upload file -> create batch -> poll status -> verify in list.
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_file_upload scenario")
        
        import time
        
        # Get provider-specific client
        client = get_provider_google_client(provider)
        
        # Create JSON content for batch
        json_content = create_google_batch_json_content(model=model, num_requests=2)
        
        # Write to a temporary file
        with tempfile.NamedTemporaryFile(mode='w', suffix='.json', delete=False) as f:
            f.write(json_content)
            temp_file_path = f.name
        
        batch_job = None
        uploaded_file = None
        
        try:
            # Step 1: Upload batch input file
            print(f"Step 1: Uploading batch input file for provider {provider}...")
            uploaded_file = client.files.upload(
                file=temp_file_path,
                config=types.UploadFileConfig(display_name=f'batch_e2e_test_{provider}')
            )
            assert_valid_google_file_response(uploaded_file)
            print(f"  Uploaded file: {uploaded_file.name}")
            
            # Step 2: Create batch job using file
            print("Step 2: Creating batch job with file...")
            batch_job = client.batches.create(
                model=format_provider_model(provider, model),
                src=uploaded_file.name,
            )
            assert_valid_google_batch_response(batch_job)
            print(f"  Created batch: {batch_job.name}, state: {batch_job.state}")
            
            # Step 3: Poll batch status (with timeout)
            print("Step 3: Polling batch status...")
            max_polls = 5
            poll_interval = 2  # seconds
            
            for i in range(max_polls):
                retrieved_job = client.batches.get(name=batch_job.name)
                print(f"  Poll {i+1}: state = {retrieved_job.state}")
                
                if retrieved_job.state in GOOGLE_BATCH_TERMINAL_STATES:
                    print(f"  Batch reached terminal state: {retrieved_job.state}")
                    break
                
                time.sleep(poll_interval)
            
            # Step 4: Verify batch is in the list
            print("Step 4: Verifying batch in list...")
            found_in_list = False
            for job in client.batches.list(config=types.ListBatchJobsConfig(page_size=20)):
                if job.name == batch_job.name:
                    found_in_list = True
                    break
            
            assert found_in_list, f"Batch {batch_job.name} should be in the batch list"
            print(f"  Verified batch {batch_job.name} is in list")
            
            print(f"Success: File API E2E completed for batch {batch_job.name} (provider: {provider})")
            
        finally:
            # Clean up local temp file
            if os.path.exists(temp_file_path):
                os.remove(temp_file_path)
            
            # Clean up batch job
            if batch_job:
                try:
                    client.batches.delete(name=batch_job.name)
                    print(f"Cleanup: Deleted batch {batch_job.name}")
                except Exception as e:
                    print(f"Cleanup info: Could not delete batch: {e}")
            
            # Clean up uploaded file
            if uploaded_file:
                try:
                    client.files.delete(name=uploaded_file.name)
                    print(f"Cleanup: Deleted file {uploaded_file.name}")
                except Exception as e:
                    print(f"Cleanup warning: Failed to delete file: {e}")

# Additional helper functions specific to Google GenAI
def extract_google_function_calls(response: Any) -> List[Dict[str, Any]]:
    """Extract function calls from Google GenAI response format with proper type checking"""
    function_calls = []

    # Type check for Google GenAI response
    if not hasattr(response, "function_calls") or not response.function_calls:
        return function_calls

    for fc in response.function_calls:
        if hasattr(fc, "name") and hasattr(fc, "args"):
            try:
                function_calls.append(
                    {
                        "name": fc.name,
                        "arguments": dict(fc.args) if fc.args else {},
                    }
                )
            except (AttributeError, TypeError) as e:
                print(f"Warning: Failed to extract Google function call: {e}")
                continue

    return function_calls
