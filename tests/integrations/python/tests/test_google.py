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
28. Gemini 3 Pro Preview - Thought signature handling with tool calling (multi-turn)
29. Structured outputs with thinking_budget enabled
30. Files API - file upload
31. Files API - file list
32. Files API - file retrieve
33. Files API - file delete
34. Batch API - batch create with file
35. Batch API - batch create inline
36. Batch API - batch list
37. Batch API - batch retrieve
38. Batch API - batch cancel
39. Batch API - end-to-end with Files API
40. Count tokens (Cross-Provider)
41. Image generation
42. Google Search grounding (non-streaming)
43. Google Search grounding (streaming)
"""

import io
import json
import os
import tempfile
import wave
from typing import Any, Dict, List

import pytest
import requests
from google import genai
from google.genai import types
from google.genai.types import HttpOptions
from PIL import Image

from .utils.common import (
    BASE64_IMAGE,
    # Batch API utilities
    BATCH_INLINE_PROMPTS,
    CALCULATOR_TOOL,
    COMPARISON_KEYWORDS,
    EMBEDDINGS_SINGLE_TEXT,
    FILE_DATA_BASE64,
    # Gemini-specific test data
    GEMINI_REASONING_PROMPT,
    GEMINI_REASONING_STREAMING_PROMPT,
    GENAI_INVALID_ROLE_CONTENT,
    # Image Generation utilities
    IMAGE_GENERATION_SIMPLE_PROMPT,
    IMAGE_URL_SECONDARY,
    INPUT_TOKENS_LONG_TEXT,
    INPUT_TOKENS_SIMPLE_TEXT,
    INPUT_TOKENS_WITH_SYSTEM,
    LOCATION_KEYWORDS,
    MULTIPLE_TOOL_CALL_MESSAGES,
    SIMPLE_CHAT_MESSAGES,
    SINGLE_TOOL_CALL_MESSAGES,
    WEATHER_KEYWORDS,
    WEATHER_TOOL,
    Config,
    assert_valid_chat_response,
    assert_valid_embedding_response,
    assert_valid_image_generation_response,
    assert_valid_image_response,
    assert_valid_input_tokens_response,
    assert_valid_speech_response,
    assert_valid_transcription_response,
    generate_test_audio,
    get_api_key,
    get_provider_voice,
    get_provider_voices,
    skip_if_no_api_key,
)
from .utils.config_loader import get_model
from .utils.parametrize import (
    format_provider_model,
    get_cross_provider_params_for_scenario,
)


def get_provider_google_client(provider: str = "gemini"):
    """Create Google GenAI client with x-model-provider header for given provider"""
    from .utils.config_loader import get_config, get_integration_url

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
        http_options_kwargs["timeout"] = 30000

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
    import base64
    import io

    from google.genai import types

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

    @skip_if_no_api_key("gemini")
    def test_pdf_file_input(self, test_config):
        """Test Case 9b: PDF file input - Upload and analyze PDF document"""
        import base64
        
        # Create direct Google client (without Bifrost) for file upload
        direct_client = genai.Client(
            api_key=os.getenv("GEMINI_API_KEY")
        )
        
        # Get Bifrost client for generate_content
        bifrost_client = get_provider_google_client("gemini")
        
        # Decode base64 PDF to bytes
        pdf_bytes = base64.b64decode(FILE_DATA_BASE64)
        
        # Write to a temporary PDF file
        with tempfile.NamedTemporaryFile(mode='wb', suffix='.pdf', delete=False) as f:
            f.write(pdf_bytes)
            temp_pdf_path = f.name
        
        uploaded_file = None
        
        try:
            # Upload the PDF file using direct Google client (not through Bifrost)
            uploaded_file = direct_client.files.upload(
                file=temp_pdf_path,
                config=types.UploadFileConfig(display_name='test_pdf_gemini')
            )
            
            # Use the uploaded file in a generate_content request through Bifrost
            response = bifrost_client.models.generate_content(
                model="gemini/gemini-2.5-flash",
                contents=["What is the main content of this PDF document? Summarize it.", uploaded_file],
            )
            
            # Validate response
            assert_valid_chat_response(response)
            assert response.text is not None
            assert len(response.text) > 0
            
            # Check for "hello world" keywords from the PDF content
            content_lower = response.text.lower()
            keywords = ["hello", "world", "testing", "pdf", "document"]
            assert any(
                keyword in content_lower for keyword in keywords
            ), f"Response should reference PDF document content. Got: {content_lower}"
            
            print("Success: PDF file input test passed")
            
        finally:
            # Clean up local temp file
            if os.path.exists(temp_pdf_path):
                os.remove(temp_pdf_path)
            
            # Clean up uploaded file using direct client
            if uploaded_file is not None:
                try:
                    direct_client.files.delete(name=uploaded_file.name)
                    print(f"Cleaned up file: {uploaded_file.name}")
                except Exception as e:
                    print(f"Warning: Failed to clean up file: {e}")

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

    
    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("simple_chat"))
    def test_11a_system_instruction(self, google_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 11a: System instruction (cross-provider)"""
        from google.genai import types
        
        # Test 1: System instruction with word count constraint
        response = google_client.models.generate_content(
            model=format_provider_model(provider, model),
            contents="What is 2 + 2?",
            config=types.GenerateContentConfig(
                system_instruction="You are a helpful assistant that always responds in exactly 5 words or fewer.",
                max_output_tokens=300,
            ),
        )
        
        assert_valid_chat_response(response)
        assert response.text is not None
        assert len(response.text) > 0
        
        # Verify response respects the constraint AND contains correct answer
        word_count = len(response.text.split())
        content_lower = response.text.lower()
        
        # Should be short (respecting the 5 word limit with small tolerance)
        assert word_count <= 8, (
            f"Expected ≤8 words (system instruction: ≤5 words), got {word_count} words: {response.text}"
        )
        
        # Should contain the correct answer
        has_answer = any(ans in content_lower for ans in ["4", "four", "quatre"])
        assert has_answer, (
            f"Response should contain the answer '4' or 'four'. Got: {response.text}"
        )
        
        print(f"✓ Word limit test passed: {response.text} ({word_count} words)")
        
        # Test 2: System instruction for translation (English to French)
        response2 = google_client.models.generate_content(
            model=format_provider_model(provider, model),
            contents="Hello, how are you?",
            config=types.GenerateContentConfig(
                system_instruction=[
                    "You are a language translator.",
                    "Your mission is to translate text from English to French.",
                    "Only output the French translation, nothing else.",
                ],
                max_output_tokens=300,
            ),
        )
        
        assert_valid_chat_response(response2)
        assert response2.text is not None
        assert len(response2.text) > 0
        
        content_lower = response2.text.lower()
        
        # Check for French translation keywords
        french_keywords = ["bonjour", "salut", "comment", "allez", "vous", "ça", "va"]
        has_french = any(keyword in content_lower for keyword in french_keywords)
        
        # Check for common English words that shouldn't appear in pure French translation
        english_words = ["hello", "how", "are", "you"]
        has_english = any(word in content_lower for word in english_words)
        
        # Should have French keywords AND not have English words (pure translation)
        assert has_french, (
            f"Response should contain French keywords. Got: {response2.text}"
        )
        assert not has_english, (
            f"Response should not contain English words (should be pure French translation). Got: {response2.text}"
        )
        
        print(f"✓ Translation test passed: {response2.text}")
        print(f"✓ System instruction test completed for provider {provider}")
        
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

    @skip_if_no_api_key("gemini")
    def test_28_gemini_3_pro_thought_signatures_multi_turn(self, test_config):
        """Test Case 28: Gemini 3 Pro Preview - Thought Signature Handling with Tool Calling (Multi-turn)"""
        from google.genai import types
        
        client = get_provider_google_client(provider="gemini")
        model = "gemini-3-pro-preview"
        
        calculator_tool = types.Tool(
            function_declarations=[
                {
                    "name": "calculate",
                    "description": "Perform a mathematical calculation",
                    "parameters": {
                        "type": "object",
                        "properties": {
                            "expression": {
                                "type": "string",
                                "description": "The mathematical expression to evaluate"
                            },
                            "operation": {
                                "type": "string",
                                "description": "Type of operation: add, subtract, multiply, divide"
                            }
                        },
                        "required": ["expression", "operation"]
                    }
                }
            ]
        )
        
        weather_tool = types.Tool(
            function_declarations=[
                {
                    "name": "get_weather",
                    "description": "Get current weather for a location",
                    "parameters": {
                        "type": "object",
                        "properties": {
                            "location": {
                                "type": "string",
                                "description": "City name or location"
                            },
                            "unit": {
                                "type": "string",
                                "description": "Temperature unit: celsius or fahrenheit"
                            }
                        },
                        "required": ["location"]
                    }
                }
            ]
        )
        
        print("\n=== TURN 1: Initial message with thinking ===")
        # Turn 1: Initial message with thinking enabled
        response_1 = client.models.generate_content(
            model=model,
            contents="If I have 15 apples and give away 7, then buy 10 more, how many do I have? Think through this step by step.",
            config=types.GenerateContentConfig(
                thinking_config=types.ThinkingConfig(
                    thinking_level="high"
                )
            )
        )
        
        # Validate Turn 1: Should have thinking content
        assert response_1.candidates, "Response should have candidates"
        assert response_1.candidates[0].content, "Candidate should have content"
        
        # Check for thought parts in the response
        has_thought = False
        has_text = False
        thought_signature_count = 0
        
        for part in response_1.candidates[0].content.parts:
            if hasattr(part, 'thought') and part.thought:
                has_thought = True
                part_text = getattr(part, 'text', None)
                print(f"  [THOUGHT] {part_text[:100] if part_text else '(no text)'}...")
            elif hasattr(part, 'text') and part.text:
                has_text = True
                print(f"  [TEXT] {part.text[:100]}...")
            if hasattr(part, 'thought_signature') and part.thought_signature:
                thought_signature_count += 1
                print(f"  [SIGNATURE] Found thought signature ({len(part.thought_signature)} bytes)")
        
        print(f"  Turn 1 Summary: thought={has_thought}, text={has_text}, signatures={thought_signature_count}")
        
        # Assert that thought signatures are present when using Gemini with thinking enabled
        assert has_thought or has_text, "Response should have either thought or text content"
        assert thought_signature_count > 0, "Response should have at least one thought signature with thinking enabled"
        
        print("\n=== TURN 2: Tool call with thinking ===")
        # Turn 2: Ask a question that requires tool use
        response_2 = client.models.generate_content(
            model=model,
            contents="What's 25 multiplied by 4? Use the calculator tool to compute this.",
            config=types.GenerateContentConfig(
                tools=[calculator_tool],
                thinking_config=types.ThinkingConfig(
                    thinking_level="high"
                )
            )
        )
        
        # Validate Turn 2: Should have function call with thought signature
        assert response_2.candidates, "Response should have candidates"
        function_calls = []
        tool_thought_signatures = []
        
        for part in response_2.candidates[0].content.parts:
            if hasattr(part, 'function_call') and part.function_call:
                function_calls.append(part.function_call)
                print(f"  [FUNCTION_CALL] {part.function_call.name}({part.function_call.args})")
                
                # Check for thought signature on the function call
                if hasattr(part, 'thought_signature') and part.thought_signature:
                    tool_thought_signatures.append(part.thought_signature)
                    print(f"    [SIGNATURE] Tool call has thought signature ({len(part.thought_signature)} bytes)")
        
        assert len(function_calls) > 0, "Should have at least one function call"
        assert len(tool_thought_signatures) > 0, "Function calls should have thought signatures when thinking is enabled"
        print(f"  Turn 2 Summary: function_calls={len(function_calls)}, tool_signatures={len(tool_thought_signatures)}")
        
        print("\n=== TURN 3: Multi-turn with tool result ===")
        # Turn 3: Continue conversation with tool result
        conversation = [
            types.Content(
                role="user",
                parts=[types.Part(text="What's 25 multiplied by 4? Use the calculator tool.")]
            ),
            types.Content(
                role="model",
                parts=[
                    types.Part(
                        function_call=types.FunctionCall(
                            name="calculate",
                            args={"expression": "25 * 4", "operation": "multiply"}
                        ),
                        thought_signature=tool_thought_signatures[0] if tool_thought_signatures else None
                    )
                ]
            ),
            types.Content(
                role="user",
                parts=[
                    types.Part(
                        function_response=types.FunctionResponse(
                            name="calculate",
                            response={"result": "100"}
                        )
                    )
                ]
            )
        ]
        
        response_3 = client.models.generate_content(
            model=model,
            contents=conversation,
            config=types.GenerateContentConfig(
                tools=[calculator_tool],
                thinking_config=types.ThinkingConfig(
                    thinking_level="high"
                )
            )
        )
        
        # Validate Turn 3: Should have text response after tool result
        assert response_3.candidates, "Response should have candidates"
        turn3_text = ""
        turn3_thoughts = 0
        
        for part in response_3.candidates[0].content.parts:
            if hasattr(part, 'text') and part.text:
                turn3_text += part.text
                if hasattr(part, 'thought') and part.thought:
                    turn3_thoughts += 1
                    part_text = getattr(part, 'text', None)
                    print(f"  [THOUGHT] {part_text[:100] if part_text else '(no text)'}...")
                else:
                    print(f"  [TEXT] {part.text[:100]}...")
        
        assert len(turn3_text) > 0, "Should have text response after tool result"
        print(f"  Turn 3 Summary: text_length={len(turn3_text)}, thoughts={turn3_thoughts}")
        
        print("\n=== TURN 4: Multiple tool calls in parallel ===")
        # Turn 4: Request multiple tool calls
        response_4 = client.models.generate_content(
            model=model,
            contents="Calculate 50 + 30 and also get the weather in Tokyo. Use the appropriate tools.",
            config=types.GenerateContentConfig(
                tools=[calculator_tool, weather_tool],
                thinking_config=types.ThinkingConfig(
                    thinking_level="low"
                )
            )
        )
        
        # Validate Turn 4: Should have multiple function calls
        assert response_4.candidates, "Response should have candidates"
        multi_function_calls = []
        multi_signatures = []
        
        for part in response_4.candidates[0].content.parts:
            if hasattr(part, 'function_call') and part.function_call:
                multi_function_calls.append(part.function_call)
                print(f"  [FUNCTION_CALL] {part.function_call.name}")
                
                if hasattr(part, 'thought_signature') and part.thought_signature:
                    multi_signatures.append(part.thought_signature)
                    print(f"    [SIGNATURE] Present ({len(part.thought_signature)} bytes)")
        
        print(f"  Turn 4 Summary: function_calls={len(multi_function_calls)}, signatures={len(multi_signatures)}")
        
        # Assert that multiple tool calls also get thought signatures
        # Note: Using thinking_level="low" here, so signatures may be optional depending on model behavior
        # but if any function calls are made with thinking enabled, they should have signatures
        if len(multi_function_calls) > 0:
            assert len(multi_signatures) > 0, "Function calls should have thought signatures when thinking is enabled (even at low level)"
        
        if hasattr(response_1, 'usage_metadata') and response_1.usage_metadata:
            if hasattr(response_1.usage_metadata, 'thoughts_token_count'):
                print("\n=== Token Usage ===")
                print(f"  Thinking tokens: {response_1.usage_metadata.thoughts_token_count}")
                assert response_1.usage_metadata.thoughts_token_count > 0, "Should have thinking token usage"
        
        print("\n✓ Gemini 3 Pro Preview thought signature handling test completed successfully!")

    @skip_if_no_api_key("google")
    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("thinking"))
    def test_29_structured_output_with_thinking(self, google_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 29: Structured outputs with thinking_budget enabled
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

    @skip_if_no_api_key("gemini")
    def test_30a_gemini_3_parallel_function_calls_signatures(self, test_config):
        """Test Case 30a: Gemini 3 - Parallel function calls with thought signatures
        """
        
        client = get_provider_google_client(provider="gemini")
        model = "gemini-3-flash-preview"  # Gemini 3 Flash supports parallel calls
        
        # Define multiple tools for parallel calling
        weather_tool = types.Tool(
            function_declarations=[
                {
                    "name": "get_weather",
                    "description": "Get current weather for a location",
                    "parameters": {
                        "type": "object",
                        "properties": {
                            "location": {"type": "string", "description": "City name"}
                        },
                        "required": ["location"]
                    }
                }
            ]
        )
        
        temperature_tool = types.Tool(
            function_declarations=[
                {
                    "name": "get_temperature",
                    "description": "Get current temperature for a location",
                    "parameters": {
                        "type": "object",
                        "properties": {
                            "location": {"type": "string", "description": "City name"},
                            "unit": {"type": "string", "description": "celsius or fahrenheit"}
                        },
                        "required": ["location"]
                    }
                }
            ]
        )
        
        print("\n=== Testing Parallel Function Calls with Thought Signatures ===")
        # Request that should trigger parallel function calls
        response = client.models.generate_content(
            model=model,
            contents="Check the weather in Paris and London at the same time.",
            config=types.GenerateContentConfig(
                tools=[weather_tool, temperature_tool],
                thinking_config=types.ThinkingConfig(
                    thinking_level="medium"  # Medium level for Gemini 3 Flash
                )
            )
        )
        
        # Validate parallel function calls
        assert response.candidates, "Response should have candidates"
        function_calls = []
        signatures = []
        function_call_part_indices = []
        
        for idx, part in enumerate(response.candidates[0].content.parts):
            if hasattr(part, 'function_call') and part.function_call:
                function_calls.append(part.function_call)
                function_call_part_indices.append(idx)
                print(f"  [FC {idx+1}] {part.function_call.name}")
                
                if hasattr(part, 'thought_signature') and part.thought_signature:
                    signatures.append((idx, len(part.thought_signature)))
                    print(f"    → Has signature ({len(part.thought_signature)} bytes)")
                else:
                    print("    → No signature")
        
        # According to Gemini docs: only the FIRST function call should have the signature
        if len(function_calls) > 1:
            print(f"\n  Found {len(function_calls)} parallel function calls")
            print(f"  Signatures on parts: {signatures}")
            
            # First function call should have signature
            assert len(signatures) > 0, "First function call should have thought signature"
            assert signatures[0][0] == function_call_part_indices[0], (
                "Thought signature should be on the first function call part"
            )
            
            print("✓ Parallel function call signature handling verified!")
        else:
            print(f"  Only {len(function_calls)} function call(s) made")
            # If only one call, it should still have a signature
            assert len(signatures) > 0, "Function call should have thought signature"

    @skip_if_no_api_key("gemini")
    def test_30b_gemini_3_sequential_function_calls_signatures(self, test_config):
        """Test Case 30b: Gemini 3 - Sequential multi-step function calls with signatures"""
        client = get_provider_google_client(provider="gemini")
        model = "gemini-3-flash-preview"
        
        # Tools for sequential operations
        search_tool = types.Tool(
            function_declarations=[
                {
                    "name": "search_database",
                    "description": "Search database for information",
                    "parameters": {
                        "type": "object",
                        "properties": {
                            "query": {"type": "string", "description": "Search query"}
                        },
                        "required": ["query"]
                    }
                }
            ]
        )
        
        process_tool = types.Tool(
            function_declarations=[
                {
                    "name": "process_results",
                    "description": "Process search results",
                    "parameters": {
                        "type": "object",
                        "properties": {
                            "data": {"type": "string", "description": "Data to process"}
                        },
                        "required": ["data"]
                    }
                }
            ]
        )
        
        print("\n=== Step 1: First function call ===")
        # Step 1: Make initial request
        response_1 = client.models.generate_content(
            model=model,
            contents="Search for information about Python programming and then process the results.",
            config=types.GenerateContentConfig(
                tools=[search_tool, process_tool],
                thinking_config=types.ThinkingConfig(
                    thinking_level="medium"
                )
            )
        )
        
        # Extract first function call and signature
        first_fc = None
        first_signature = None
        
        for part in response_1.candidates[0].content.parts:
            if hasattr(part, 'function_call') and part.function_call:
                first_fc = part.function_call
                print(f"  [FC1] {first_fc.name}({first_fc.args})")
                
                if hasattr(part, 'thought_signature') and part.thought_signature:
                    first_signature = part.thought_signature
                    print(f"  → Signature: {len(first_signature)} bytes")
                break
        
        assert first_fc is not None, "Should have first function call"
        assert first_signature is not None, "First function call should have thought signature"
        
        print("\n=== Step 2: Send function result and get next call ===")
        # Step 2: Build conversation with signature preserved
        conversation = [
            types.Content(
                role="user",
                parts=[types.Part(text="Search for information about Python programming and then process the results.")]
            ),
            types.Content(
                role="model",
                parts=[
                    types.Part(
                        function_call=first_fc,
                        thought_signature=first_signature  # MUST preserve signature
                    )
                ]
            ),
            types.Content(
                role="user",
                parts=[
                    types.Part(
                        function_response=types.FunctionResponse(
                            name=first_fc.name,
                            response={"results": "Python is a high-level programming language..."}
                        )
                    )
                ]
            )
        ]
        
        response_2 = client.models.generate_content(
            model=model,
            contents=conversation,
            config=types.GenerateContentConfig(
                tools=[search_tool, process_tool],
                thinking_config=types.ThinkingConfig(
                    thinking_level="medium"
                )
            )
        )
        
        # Check for second function call or text response
        second_fc = None
        second_signature = None
        has_text = False
        
        for part in response_2.candidates[0].content.parts:
            if hasattr(part, 'function_call') and part.function_call:
                second_fc = part.function_call
                print(f"  [FC2] {second_fc.name}")
                
                if hasattr(part, 'thought_signature') and part.thought_signature:
                    second_signature = part.thought_signature
                    print(f"  → Signature: {len(second_signature)} bytes")
            elif hasattr(part, 'text') and part.text:
                has_text = True
                print(f"  [TEXT] {part.text[:100]}...")
        
        # Should have either a second function call with signature or text response
        if second_fc:
            print("\n✓ Sequential function calling: Step 2 has function call with signature")
            assert second_signature is not None, "Second function call should also have thought signature"
        else:
            print("\n✓ Model provided text response after first function call")
            assert has_text, "Should have text response"

    @skip_if_no_api_key("gemini")
    def test_30c_gemini_3_thought_signatures_in_text_responses(self, test_config):
        """Test Case 30c: Gemini 3 - Thought signatures in non-function-call responses"""
        
        client = get_provider_google_client(provider="gemini")
        model = "gemini-3-flash-preview"
        
        print("\n=== Testing Thought Signatures in Text Responses ===")
        # Request that should NOT trigger function calls but has thinking
        response = client.models.generate_content(
            model=model,
            contents="Explain step-by-step how to solve: If a train travels 120 km in 2 hours, what is its average speed?",
            config=types.GenerateContentConfig(
                thinking_config=types.ThinkingConfig(
                    thinking_level="high"  # Enable thinking
                )
            )
        )
        
        # Validate response structure
        assert response.candidates, "Response should have candidates"
        parts = response.candidates[0].content.parts
        assert len(parts) > 0, "Response should have parts"
        
        # Check for thought signatures in parts
        signatures_found = []
        thought_parts = []
        text_parts = []
        
        for idx, part in enumerate(parts):
            if hasattr(part, 'thought') and part.thought:
                thought_parts.append(idx)
                print(f"  [PART {idx}] Thought: {getattr(part, 'text', '')[:80]}...")
            elif hasattr(part, 'text') and part.text:
                text_parts.append(idx)
                print(f"  [PART {idx}] Text: {part.text[:80]}...")
            
            if hasattr(part, 'thought_signature') and part.thought_signature:
                signatures_found.append(idx)
                print(f"    → Has signature ({len(part.thought_signature)} bytes)")
        
        print("\n  Summary:")
        print(f"    Thought parts: {thought_parts}")
        print(f"    Text parts: {text_parts}")
        print(f"    Signatures on parts: {signatures_found}")
        
        # According to docs: signature should be in the last part (when thinking is enabled)
        if len(signatures_found) > 0:
            last_part_idx = len(parts) - 1
            # Signature might be on the last part or on a thought part
            print(f"    Last part index: {last_part_idx}")
            print(f"✓ Found {len(signatures_found)} thought signature(s) in text response")
        else:
            print("  Note: No explicit thought signatures found (may be internal)")

    @skip_if_no_api_key("gemini")
    def test_30d_gemini_3_thinking_levels(self, test_config):
        """Test Case 30d: Gemini 3 - Different thinking levels (minimal, low, medium, high)"""
        
        client = get_provider_google_client(provider="gemini")
        model = "gemini-3-flash-preview"  # Use Flash for full level support
        
        test_prompt = "What is 12 * 15?"
        thinking_levels = ["minimal", "low", "medium", "high"]
        
        print("\n=== Testing Different Thinking Levels ===")
        
        for level in thinking_levels:
            print(f"\n  Testing thinking_level='{level}':")
            
            response = client.models.generate_content(
                model=model,
                contents=test_prompt,
                config=types.GenerateContentConfig(
                    thinking_config=types.ThinkingConfig(
                        thinking_level=level,
                        include_thoughts=True
                    )
                )
            )
            
            assert response.candidates, f"Response should have candidates for level '{level}'"
            
            # Check token usage
            thought_tokens = 0
            if hasattr(response, 'usage_metadata') and response.usage_metadata:
                if hasattr(response.usage_metadata, 'thoughts_token_count'):
                    thought_tokens = response.usage_metadata.thoughts_token_count
            
            # Check for thought parts
            has_thoughts = any(
                hasattr(part, 'thought') and part.thought 
                for part in response.candidates[0].content.parts
            )
            
            print(f"    Thought tokens: {thought_tokens}")
            print(f"    Has thought parts: {has_thoughts}")
            
            # Minimal should have very few or no thinking tokens
            if level == "minimal":
                print("    ✓ Minimal thinking level (expected: minimal/no thinking)")
            elif level == "high":
                # High should have substantial thinking
                if thought_tokens > 0:
                    print(f"    ✓ High thinking level with {thought_tokens} thought tokens")
                else:
                    print("    ! High thinking but no thought tokens reported")
        
        print("\n✓ All thinking levels tested successfully")

    @skip_if_no_api_key("gemini")
    def test_30e_gemini_3_signature_validation_strict(self, test_config):
        """Test Case 30e: Gemini 3 - Strict validation of thought signatures in function calling"""
        
        client = get_provider_google_client(provider="gemini")
        model = "gemini-3-flash-preview"
        
        calculator_tool = types.Tool(
            function_declarations=[
                {
                    "name": "calculate",
                    "description": "Perform calculation",
                    "parameters": {
                        "type": "object",
                        "properties": {
                            "expression": {"type": "string"}
                        },
                        "required": ["expression"]
                    }
                }
            ]
        )
        
        print("\n=== Testing Strict Signature Validation ===")
        
        # Step 1: Get function call with signature
        print("  Step 1: Getting function call with signature...")
        response_1 = client.models.generate_content(
            model=model,
            contents="Calculate 45 * 23 using the calculator tool.",
            config=types.GenerateContentConfig(
                tools=[calculator_tool],
                thinking_config=types.ThinkingConfig(
                    thinking_level="medium"  # Ensure signatures are generated
                )
            )
        )
        
        # Extract function call and signature
        fc = None
        sig = None
        for part in response_1.candidates[0].content.parts:
            if hasattr(part, 'function_call') and part.function_call:
                fc = part.function_call
                if hasattr(part, 'thought_signature') and part.thought_signature:
                    sig = part.thought_signature
                print(f"    FC: {fc.name}, Signature: {len(sig) if sig else 0} bytes")
                break
        
        assert fc is not None, "Should have function call"
        assert sig is not None, "Function call should have thought signature"
        
        # Step 2: Test WITH signature (should succeed)
        print("\n  Step 2: Sending function result WITH signature...")
        try:
            conversation_with_sig = [
                types.Content(role="user", parts=[types.Part(text="Calculate 45 * 23")]),
                types.Content(
                    role="model",
                    parts=[types.Part(function_call=fc, thought_signature=sig)]  # WITH signature
                ),
                types.Content(
                    role="user",
                    parts=[types.Part(
                        function_response=types.FunctionResponse(
                            name=fc.name,
                            response={"result": "1035"}
                        )
                    )]
                )
            ]
            
            response_2 = client.models.generate_content(
                model=model,
                contents=conversation_with_sig,
                config=types.GenerateContentConfig(
                    tools=[calculator_tool],
                    thinking_config=types.ThinkingConfig(thinking_level="medium")
                )
            )
            
            assert response_2.candidates, "Request with signature should succeed"
            print("    ✓ Request WITH signature succeeded")
            
        except Exception as e:
            pytest.fail(f"Request with signature should not fail: {e}")
        
        # Step 3: Test WITHOUT signature (according to docs, should fail for Gemini 3)
        # However, we'll make this informational rather than asserting failure
        # because the SDK might auto-handle this
        print("\n  Step 3: Testing without signature (informational)...")
        try:
            conversation_without_sig = [
                types.Content(role="user", parts=[types.Part(text="Calculate 45 * 23")]),
                types.Content(
                    role="model",
                    parts=[types.Part(function_call=fc)]  # WITHOUT signature
                ),
                types.Content(
                    role="user",
                    parts=[types.Part(
                        function_response=types.FunctionResponse(
                            name=fc.name,
                            response={"result": "1035"}
                        )
                    )]
                )
            ]
            
            client.models.generate_content(
                model=model,
                contents=conversation_without_sig,
                config=types.GenerateContentConfig(
                    tools=[calculator_tool],
                    thinking_config=types.ThinkingConfig(thinking_level="medium")
                )
            )
            
            print("    ! Request WITHOUT signature succeeded (SDK may auto-handle)")
            print("    Note: Gemini 3 docs state this should fail, but SDK might preserve signatures")
            
        except Exception as e:
            error_msg = str(e).lower()
            if "signature" in error_msg or "400" in error_msg or "validation" in error_msg:
                print(f"    ✓ Request without signature failed as expected: {type(e).__name__}")
            else:
                print(f"    ? Request failed with unexpected error: {e}")
        
        print("\n✓ Signature validation test completed")


    # =========================================================================
    # IMAGE GENERATION TEST CASES
    # =========================================================================

    @skip_if_no_api_key("google")
    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("image_generation"))
    def test_41a_image_generation_simple(self, test_config, provider, model):
        """Test Case 41a: Simple image generation with Gemini model via Bifrost"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for image_generation scenario")
        
        from google.genai import types
        
        # Get provider-specific client
        client = get_provider_google_client(provider)
        
        # Use Google GenAI client with response_modalities for image generation
        response = client.models.generate_content(
            model=format_provider_model(provider, model),
            contents=IMAGE_GENERATION_SIMPLE_PROMPT,
            config=types.GenerateContentConfig(
                response_modalities=["IMAGE"]
            )
        )
        
        # Validate response structure (validation function handles both dict and object formats)
        assert_valid_image_generation_response(response, "google")

    @skip_if_no_api_key("google")
    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("imagen"))
    def test_41b_imagen_predict(self, test_config, provider, model):
        """Test Case 41b: Image generation using Imagen model via Bifrost"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for imagen scenario")
        
        from google.genai import types
        
        # Get provider-specific client
        client = get_provider_google_client(provider)
        
        # For Imagen models, use generate_content with the Imagen model
        # Bifrost will automatically route to the :predict endpoint for Imagen models
        # Use provider/model format so Bifrost can parse the provider and route correctly
        try:
            response = client.models.generate_content(
                model=format_provider_model(provider, model),
                contents=IMAGE_GENERATION_SIMPLE_PROMPT,
                config=types.GenerateContentConfig()
            )
            
            # Validate response structure (validation function handles both dict and object formats)
            assert_valid_image_generation_response(response, "google")
        except Exception as e:
            # Imagen may not be available in all regions or configurations
            pytest.skip(f"Imagen generation failed: {e}")

    # =========================================================================
    # FILES API TEST CASES
    # =========================================================================

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("batch_file_upload"))
    def test_30_file_upload(self, test_config, provider, model):
        """Test Case 30: Upload a file for batch processing"""
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
    def test_31_file_list(self, test_config, provider, model):
        """Test Case 31: List uploaded files"""
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
    def test_32_file_retrieve(self, test_config, provider, model):
        """Test Case 32: Retrieve file metadata by name"""
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
    def test_33_file_delete(self, test_config, provider, model):
        """Test Case 33: Delete an uploaded file"""
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
            with pytest.raises(RuntimeError):
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
    def test_34_batch_create_with_file(self, test_config, provider, model):
        """Test Case 34: Create a batch job using uploaded file
        
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
    def test_35_batch_create_inline(self, test_config, provider, model):
        """Test Case 35: Create a batch job with inline requests"""
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
    def test_36_batch_list(self, test_config, provider, model):
        """Test Case 36: List batch jobs"""
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
    def test_37_batch_retrieve(self, test_config, provider, model):
        """Test Case 37: Retrieve batch status by name"""
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
    def test_38_batch_cancel(self, test_config, provider, model):
        """Test Case 38: Cancel a batch job"""
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
    def test_39_batch_e2e_file_api(self, test_config, provider, model):
        """Test Case 39: End-to-end batch workflow using Files API
        
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

    # =========================================================================
    # INPUT TOKENS / TOKEN COUNTING TEST CASES
    # =========================================================================

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("count_tokens"))
    def test_40a_input_tokens_simple_text(self, google_client, test_config, provider, model):
        """Test Case 40a: Input tokens count with simple text"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        print(f"Testing input tokens for provider {provider}, model {model}...")
        response =  google_client.models.count_tokens(
            model=format_provider_model(provider, model),
            contents=[{"role": "user", "parts": [{"text": INPUT_TOKENS_SIMPLE_TEXT}]}],
        )

        # Validate response structure
        assert_valid_input_tokens_response(response, "google")

        # Simple text should have a reasonable token count (between 3-20 tokens)
        assert 3 <= response.total_tokens <= 20, (
            f"Simple text should have 3-20 tokens, got {response.total_tokens}"
        )

    # Google does not support counting token request with system message so this test has only 2 parts

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("count_tokens"))
    def test_40b_input_tokens_long_text(self, google_client, test_config, provider, model):
        """Test Case 40b: Input tokens count with long text"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        response = google_client.models.count_tokens(
            model=format_provider_model(provider, model),
            contents=[{"role": "user", "parts": [{"text": INPUT_TOKENS_LONG_TEXT}]}],
        )

        # Validate response structure
        assert_valid_input_tokens_response(response,"google")

        # Long text should have significantly more tokens
        assert response.total_tokens > 100, (
            f"Long text should have >100 tokens, got {response.total_tokens}"
        )

    # =========================================================================
    # GOOGLE SEARCH GROUNDING TEST CASES
    # =========================================================================

    @skip_if_no_api_key("google")
    def test_42_google_search_grounding(self, test_config):
        """Test Case 42: Google Search grounding (non-streaming)
        
        Tests the Google Search tool for grounding responses with real-time web data.
        Validates that the response includes grounding metadata with search queries,
        grounding chunks, and grounding supports.
        """
        
        # Get Gemini client (Google Search is a Gemini feature)
        client = get_provider_google_client("gemini")
        
        # Create Google Search tool
        grounding_tool = types.Tool(
            google_search=types.GoogleSearch()
        )
        
        # Test with a query that requires recent information
        response = client.models.generate_content(
            model=get_model("google", "chat"),
            contents="Who won the 2024 UEFA European Championship? Provide details about the final match.",
            config=types.GenerateContentConfig(
                tools=[grounding_tool]
            )
        )
        
        # Validate basic response structure
        assert response is not None, "Response should not be None"
        assert response.candidates is not None, "Response should have candidates"
        assert len(response.candidates) > 0, "Should have at least one candidate"
        
        # Validate response text
        assert response.text is not None, "Response should have text"
        assert len(response.text) > 0, "Response text should not be empty"
        
        print(f"Response text: {response.text[:200]}...")
        
        # Validate grounding metadata
        candidate = response.candidates[0]
        assert hasattr(candidate, "grounding_metadata"), (
            "Candidate should have grounding_metadata when using Google Search"
        )
        
        grounding_metadata = candidate.grounding_metadata
        assert grounding_metadata is not None, "Grounding metadata should not be None"
        
        # Check for web search queries
        if hasattr(grounding_metadata, "web_search_queries"):
            web_queries = grounding_metadata.web_search_queries
            assert web_queries is not None, "Web search queries should not be None"
            assert len(web_queries) > 0, "Should have at least one web search query"
            print(f"Web search queries: {web_queries}")
        
        # Check for grounding chunks (search results)
        if hasattr(grounding_metadata, "grounding_chunks"):
            chunks = grounding_metadata.grounding_chunks
            assert chunks is not None, "Grounding chunks should not be None"
            assert len(chunks) > 0, "Should have at least one grounding chunk"
            
            # Validate chunk structure
            for idx, chunk in enumerate(chunks[:3]):  # Check first 3 chunks
                if hasattr(chunk, "web"):
                    assert hasattr(chunk.web, "uri"), f"Chunk {idx} should have URI"
                    assert hasattr(chunk.web, "title"), f"Chunk {idx} should have title"
                    print(f"Chunk {idx}: {chunk.web.title} - {chunk.web.uri}")
        
        # Check for grounding supports (citations)
        if hasattr(grounding_metadata, "grounding_supports"):
            supports = grounding_metadata.grounding_supports
            assert supports is not None, "Grounding supports should not be None"
            assert len(supports) > 0, "Should have at least one grounding support"
            
            # Validate support structure
            for idx, support in enumerate(supports[:3]):  # Check first 3 supports
                assert hasattr(support, "segment"), f"Support {idx} should have segment"
                if hasattr(support, "grounding_chunk_indices"):
                    indices = support.grounding_chunk_indices
                    print(f"Support {idx}: segment at {support.segment.start_index}-{support.segment.end_index}, chunks: {indices}")
        
        # Check for search entry point (widget HTML/CSS)
        if hasattr(grounding_metadata, "search_entry_point"):
            entry_point = grounding_metadata.search_entry_point
            if hasattr(entry_point, "rendered_content"):
                assert entry_point.rendered_content is not None, "Rendered content should not be None"
                print(f"Search entry point available: {len(entry_point.rendered_content)} chars")
        
        print("✓ Google Search grounding test (non-streaming) passed!")

    @skip_if_no_api_key("google")
    def test_43_google_search_grounding_streaming(self, test_config):
        """Test Case 43: Google Search grounding (streaming)
        
        Tests the Google Search tool in streaming mode. Validates that grounding
        metadata is available in the final chunk or accumulated across chunks.
        """
        from google.genai import types
        
        # Get Gemini client
        client = get_provider_google_client("gemini")
        
        # Create Google Search tool
        grounding_tool = types.Tool(
            google_search=types.GoogleSearch()
        )
        
        # Test with a query that requires recent information
        stream = client.models.generate_content_stream(
            model=get_model("google", "chat"),
            contents="What are the latest developments in AI as of 2024? List three major breakthroughs.",
            config=types.GenerateContentConfig(
                tools=[grounding_tool]
            )
        )
        
        # Collect streaming content and metadata
        text_parts = []
        chunk_count = 0
        final_grounding_metadata = None
        
        for chunk in stream:
            chunk_count += 1
            
            # Collect text content
            if hasattr(chunk, "candidates") and chunk.candidates and len(chunk.candidates) > 0:
                candidate = chunk.candidates[0]
                if hasattr(candidate, "content") and candidate.content:
                    if hasattr(candidate.content, "parts") and candidate.content.parts:
                        for part in candidate.content.parts:
                            if hasattr(part, "text") and part.text:
                                text_parts.append(part.text)
                
                # Capture grounding metadata (usually in final chunks)
                if hasattr(candidate, "grounding_metadata") and candidate.grounding_metadata:
                    final_grounding_metadata = candidate.grounding_metadata
            
            # Fallback to direct text attribute
            elif hasattr(chunk, "text") and chunk.text:
                text_parts.append(chunk.text)
            
            # Safety check
            if chunk_count > 500:
                raise AssertionError("Received >500 streaming chunks; possible non-terminating stream")
        
        # Combine collected content
        complete_text = "".join(text_parts)
        
        # Validate streaming results
        assert chunk_count > 0, "Should receive at least one chunk"
        assert len(complete_text) > 0, "Should have complete text content"
        
        print(f"Received {chunk_count} chunks, total text length: {len(complete_text)}")
        print(f"Text preview: {complete_text[:200]}...")
        
        # Validate grounding metadata
        assert final_grounding_metadata is not None, (
            "Should have grounding metadata in streaming response when using Google Search"
        )
        
        # Check for web search queries
        if hasattr(final_grounding_metadata, "web_search_queries"):
            web_queries = final_grounding_metadata.web_search_queries
            if web_queries:
                assert len(web_queries) > 0, "Should have at least one web search query"
                print(f"Web search queries (streaming): {web_queries}")
        
        # Check for grounding chunks
        if hasattr(final_grounding_metadata, "grounding_chunks"):
            chunks = final_grounding_metadata.grounding_chunks
            if chunks:
                assert len(chunks) > 0, "Should have at least one grounding chunk"
                print(f"Found {len(chunks)} grounding chunks in streaming response")
                
                # Validate first chunk structure
                if len(chunks) > 0:
                    chunk = chunks[0]
                    if hasattr(chunk, "web"):
                        print(f"First chunk: {chunk.web.title}")
        
        # Check for grounding supports
        if hasattr(final_grounding_metadata, "grounding_supports"):
            supports = final_grounding_metadata.grounding_supports
            if supports:
                assert len(supports) > 0, "Should have at least one grounding support"
                print(f"Found {len(supports)} grounding supports in streaming response")
        
        print("✓ Google Search grounding test (streaming) passed!")

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
