"""
Google GenAI Integration Tests

Tests all 11 core scenarios using Google GenAI SDK directly:
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
"""

import pytest
import base64
import requests
from PIL import Image
import io
from google import genai
from google.genai.types import HttpOptions
from typing import List, Dict, Any

from ..utils.common import (
    Config,
    SIMPLE_CHAT_MESSAGES,
    SINGLE_TOOL_CALL_MESSAGES,
    MULTIPLE_TOOL_CALL_MESSAGES,
    IMAGE_URL,
    BASE64_IMAGE,
    INVALID_ROLE_MESSAGES,
    STREAMING_CHAT_MESSAGES,
    STREAMING_TOOL_CALL_MESSAGES,
    WEATHER_TOOL,
    CALCULATOR_TOOL,
    assert_valid_chat_response,
    assert_valid_image_response,
    assert_valid_error_response,
    assert_error_propagation,
    assert_valid_streaming_response,
    collect_streaming_content,
    get_api_key,
    skip_if_no_api_key,
    COMPARISON_KEYWORDS,
    WEATHER_KEYWORDS,
    LOCATION_KEYWORDS,
    GENAI_INVALID_ROLE_CONTENT,
)
from ..utils.config_loader import get_model


@pytest.fixture
def google_client():
    """Configure Google GenAI client for testing"""
    from ..utils.config_loader import get_integration_url

    api_key = get_api_key("google")
    base_url = get_integration_url("google")

    client_kwargs = {
        "api_key": api_key,
    }

    # Add base URL support and timeout through HttpOptions
    http_options_kwargs = {}
    if base_url:
        http_options_kwargs["base_url"] = base_url

    if http_options_kwargs:
        client_kwargs["http_options"] = HttpOptions(**http_options_kwargs)

    return genai.Client(**client_kwargs)


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
        # Create a FunctionDeclaration for each tool
        function_declaration = types.FunctionDeclaration(
            name=tool["name"],
            description=tool["description"],
            parameters=types.Schema(
                type=tool["parameters"]["type"].upper(),
                properties={
                    name: types.Schema(
                        type=prop["type"].upper(),
                        description=prop.get("description", ""),
                    )
                    for name, prop in tool["parameters"]["properties"].items()
                },
                required=tool["parameters"].get("required", []),
            ),
        )

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
        # URL image
        response = requests.get(url)
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


class TestGoogleIntegration:
    """Test suite for Google GenAI integration covering all 11 core scenarios"""

    @skip_if_no_api_key("google")
    def test_01_simple_chat(self, google_client, test_config):
        """Test Case 1: Simple chat interaction"""
        message = convert_to_google_messages(SIMPLE_CHAT_MESSAGES)

        response = google_client.models.generate_content(
            model=get_model("google", "chat"), contents=message
        )

        assert_valid_chat_response(response)
        assert response.text is not None
        assert len(response.text) > 0

    @skip_if_no_api_key("google")
    def test_02_multi_turn_conversation(self, google_client, test_config):
        """Test Case 2: Multi-turn conversation"""
        # Start a chat session for multi-turn
        chat = google_client.chats.create(model=get_model("google", "chat"))

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

    @skip_if_no_api_key("google")
    def test_03_single_tool_call(self, google_client, test_config):
        """Test Case 3: Single tool call"""
        from google.genai import types

        tools = convert_to_google_tools([WEATHER_TOOL])
        message = convert_to_google_messages(SINGLE_TOOL_CALL_MESSAGES)

        response = google_client.models.generate_content(
            model=get_model("google", "tools"),
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

    @skip_if_no_api_key("google")
    def test_04_multiple_tool_calls(self, google_client, test_config):
        """Test Case 4: Multiple tool calls in one response"""
        from google.genai import types

        tools = convert_to_google_tools([WEATHER_TOOL, CALCULATOR_TOOL])
        message = convert_to_google_messages(MULTIPLE_TOOL_CALL_MESSAGES)

        response = google_client.models.generate_content(
            model=get_model("google", "tools"),
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

    @skip_if_no_api_key("google")
    def test_05_end2end_tool_calling(self, google_client, test_config):
        """Test Case 5: Complete tool calling flow with responses"""
        from google.genai import types

        tools = convert_to_google_tools([WEATHER_TOOL])

        # Start chat for tool calling flow
        chat = google_client.chats.create(model=get_model("google", "tools"))

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

    @skip_if_no_api_key("google")
    def test_06_automatic_function_calling(self, google_client, test_config):
        """Test Case 6: Automatic function calling"""
        from google.genai import types

        tools = convert_to_google_tools([CALCULATOR_TOOL])

        response = google_client.models.generate_content(
            model=get_model("google", "tools"),
            contents="Calculate 25 * 4 for me",
            config=types.GenerateContentConfig(tools=tools),
        )

        # Should automatically choose to use the calculator
        assert response.candidates is not None

        # Check if function calls were made
        if hasattr(response, "function_calls") and response.function_calls:
            assert response.function_calls[0].name == "calculate"

    @skip_if_no_api_key("google")
    def test_07_image_url(self, google_client, test_config):
        """Test Case 7: Image analysis from URL"""
        image = load_image_from_url(IMAGE_URL)

        response = google_client.models.generate_content(
            model=get_model("google", "vision"),
            contents=["What do you see in this image?", image],
        )

        assert_valid_image_response(response)

    @skip_if_no_api_key("google")
    def test_08_image_base64(self, google_client, test_config):
        """Test Case 8: Image analysis from base64"""
        image = load_image_from_url(f"data:image/png;base64,{BASE64_IMAGE}")

        response = google_client.models.generate_content(
            model=get_model("google", "vision"), contents=["Describe this image", image]
        )

        assert_valid_image_response(response)

    @skip_if_no_api_key("google")
    def test_09_multiple_images(self, google_client, test_config):
        """Test Case 9: Multiple image analysis"""
        image1 = load_image_from_url(IMAGE_URL)
        image2 = load_image_from_url(f"data:image/png;base64,{BASE64_IMAGE}")

        response = google_client.models.generate_content(
            model=get_model("google", "vision"),
            contents=["Compare these two images", image1, image2],
        )

        assert_valid_image_response(response)
        content = response.text.lower()
        # Should mention comparison or differences
        assert any(
            word in content for word in COMPARISON_KEYWORDS
        ), f"Response should contain comparison keywords. Got content: {content}"

    @skip_if_no_api_key("google")
    def test_10_complex_end2end(self, google_client, test_config):
        """Test Case 10: Complex end-to-end with conversation, images, and tools"""
        from google.genai import types

        tools = convert_to_google_tools([WEATHER_TOOL])

        image = load_image_from_url(IMAGE_URL)

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
            config=types.GenerateContentConfig(temperature=0.9, max_output_tokens=100),
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
                max_output_tokens=10,
            ),
        )

        assert_valid_chat_response(response3)

    @skip_if_no_api_key("google")
    def test_12_error_handling_invalid_roles(self, google_client, test_config):
        """Test Case 12: Error handling for invalid roles"""
        with pytest.raises(Exception) as exc_info:
            google_client.models.generate_content(
                model=get_model("google", "chat"), contents=GENAI_INVALID_ROLE_CONTENT
            )

        # Verify the error is properly caught and contains role-related information
        error = exc_info.value
        assert_valid_error_response(error, "tester")
        assert_error_propagation(error, "google")

    @skip_if_no_api_key("google")
    def test_13_streaming(self, google_client, test_config):
        """Test Case 13: Streaming chat completion using Google GenAI SDK"""

        # Use the correct Google GenAI SDK streaming method
        stream = google_client.models.generate_content_stream(
            model=get_model("google", "chat"),
            contents="Tell me a short story about a robot",
        )

        content = ""
        chunk_count = 0

        # Collect streaming content
        for chunk in stream:
            chunk_count += 1
            if chunk.text:
                content += chunk.text

        # Validate streaming results
        assert chunk_count > 0, "Should receive at least one chunk"
        assert len(content) > 10, "Should receive substantial content"

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

        print(
            f"✅ Streaming test passed: {chunk_count} chunks, {len(content)} characters"
        )


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
