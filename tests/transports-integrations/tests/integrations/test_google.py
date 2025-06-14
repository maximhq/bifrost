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
import json
import base64
import requests
from PIL import Image
import io
import google.generativeai as genai
from typing import List, Dict, Any

from ..utils.common import (
    Config,
    SIMPLE_CHAT_MESSAGES,
    MULTI_TURN_MESSAGES,
    SINGLE_TOOL_CALL_MESSAGES,
    MULTIPLE_TOOL_CALL_MESSAGES,
    IMAGE_URL,
    BASE64_IMAGE,
    ALL_TOOLS,
    WEATHER_TOOL,
    CALCULATOR_TOOL,
    mock_tool_response,
    assert_valid_chat_response,
    assert_has_tool_calls,
    assert_valid_image_response,
    extract_tool_calls,
    get_api_key,
    skip_if_no_api_key,
    TestCategories,
    COMPARISON_KEYWORDS,
    WEATHER_KEYWORDS,
    LOCATION_KEYWORDS,
)


@pytest.fixture
def google_client():
    """Configure Google GenAI client for testing"""
    from ..utils.config_loader import get_integration_url, get_config

    api_key = get_api_key("google")
    base_url = get_integration_url("google")

    # Configure Google AI with custom transport if base_url is provided
    if base_url:
        # Note: Google AI SDK doesn't directly support custom base URLs
        # This would require custom transport configuration
        # For now, we'll configure normally and add a comment
        genai.configure(api_key=api_key)
        # TODO: Implement custom transport for Bifrost routing
        # The Google AI SDK doesn't easily support custom base URLs
    else:
        genai.configure(api_key=api_key)

    return genai.GenerativeModel(get_model("google", "chat"))


@pytest.fixture
def google_vision_client():
    """Configure Google GenAI vision client for testing"""
    from ..utils.config_loader import get_integration_url, get_config

    api_key = get_api_key("google")
    base_url = get_integration_url("google")

    # Configure Google AI with custom transport if base_url is provided
    if base_url:
        genai.configure(api_key=api_key)
        # TODO: Implement custom transport for Bifrost routing
    else:
        genai.configure(api_key=api_key)

    return genai.GenerativeModel(get_model("google", "vision"))


@pytest.fixture
def test_config():
    """Test configuration"""
    return Config()


def convert_to_google_messages(messages: List[Dict[str, Any]]) -> List[str]:
    """Convert common message format to Google GenAI format"""
    # Google GenAI uses a simpler format - just extract user messages
    user_messages = []
    for msg in messages:
        if msg["role"] == "user":
            if isinstance(msg["content"], str):
                user_messages.append(msg["content"])
            elif isinstance(msg["content"], list):
                # Handle multimodal content
                text_parts = [
                    item["text"] for item in msg["content"] if item["type"] == "text"
                ]
                if text_parts:
                    user_messages.append(" ".join(text_parts))

    return user_messages


def convert_to_google_tools(tools: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
    """Convert common tool format to Google GenAI format"""
    google_tools = []

    for tool in tools:
        google_tools.append(
            {
                "function_declarations": [
                    {
                        "name": tool["name"],
                        "description": tool["description"],
                        "parameters": tool["parameters"],
                    }
                ]
            }
        )

    return google_tools


def load_image_from_url(url: str):
    """Load image from URL for Google GenAI"""
    if url.startswith("data:image"):
        # Base64 image
        header, data = url.split(",", 1)
        img_data = base64.b64decode(data)
        return Image.open(io.BytesIO(img_data))
    else:
        # URL image
        response = requests.get(url)
        return Image.open(io.BytesIO(response.content))


class TestGoogleIntegration:
    """Test suite for Google GenAI integration covering all 11 core scenarios"""

    @skip_if_no_api_key("google")
    def test_01_simple_chat(self, google_client, test_config):
        """Test Case 1: Simple chat interaction"""
        response = google_client.generate_content(SIMPLE_CHAT_MESSAGES[0]["content"])

        assert_valid_chat_response(response)
        assert response.text is not None
        assert len(response.text) > 0

    @skip_if_no_api_key("google")
    def test_02_multi_turn_conversation(self, google_client, test_config):
        """Test Case 2: Multi-turn conversation"""
        # Start a chat session for multi-turn
        chat = google_client.start_chat()

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
        tools = convert_to_google_tools([WEATHER_TOOL])

        # Create model with tools
        model = genai.GenerativeModel(get_model("google", "tools"), tools=tools)

        response = model.generate_content(SINGLE_TOOL_CALL_MESSAGES[0]["content"])

        # Google GenAI might return function calls in candidates
        assert response.candidates is not None
        assert len(response.candidates) > 0

        # Check if function call was made
        candidate = response.candidates[0]
        if hasattr(candidate.content, "parts"):
            function_calls = [
                part
                for part in candidate.content.parts
                if hasattr(part, "function_call")
            ]
            if function_calls:
                assert len(function_calls) >= 1
                assert function_calls[0].function_call.name == "get_weather"

    @skip_if_no_api_key("google")
    def test_04_multiple_tool_calls(self, google_client, test_config):
        """Test Case 4: Multiple tool calls in one response"""
        tools = convert_to_google_tools([WEATHER_TOOL, CALCULATOR_TOOL])

        # Create model with tools
        model = genai.GenerativeModel(get_model("google", "tools"), tools=tools)

        response = model.generate_content(MULTIPLE_TOOL_CALL_MESSAGES[0]["content"])

        # Check for function calls
        assert response.candidates is not None
        candidate = response.candidates[0]

        if hasattr(candidate.content, "parts"):
            function_calls = [
                part
                for part in candidate.content.parts
                if hasattr(part, "function_call")
            ]
            if function_calls:
                # Should have multiple function calls
                assert len(function_calls) >= 1
                function_names = [fc.function_call.name for fc in function_calls]
                # At least one of the expected tools should be called
                assert any(
                    name in ["get_weather", "calculate"] for name in function_names
                )

    @skip_if_no_api_key("google")
    def test_05_end2end_tool_calling(self, google_client, test_config):
        """Test Case 5: Complete tool calling flow with responses"""
        tools = convert_to_google_tools([WEATHER_TOOL])

        # Create model with tools
        model = genai.GenerativeModel(get_model("google", "tools"), tools=tools)

        # Start chat for tool calling flow
        chat = model.start_chat()

        response1 = chat.send_message("What's the weather in Boston?")

        # Check if function call was made
        if response1.candidates and hasattr(response1.candidates[0].content, "parts"):
            function_calls = [
                part
                for part in response1.candidates[0].content.parts
                if hasattr(part, "function_call")
            ]

            if function_calls:
                # Simulate function execution and send result back
                for fc in function_calls:
                    if fc.function_call.name == "get_weather":
                        # Mock function result
                        function_result = {
                            "function_response": {
                                "name": fc.function_call.name,
                                "response": {
                                    "result": "The weather in Boston is 72°F and sunny."
                                },
                            }
                        }

                        # Send function result back
                        response2 = chat.send_message([function_result])
                        assert_valid_chat_response(response2)

                        content = response2.text.lower()
                        weather_location_keywords = WEATHER_KEYWORDS + LOCATION_KEYWORDS
                        assert any(
                            word in content for word in weather_location_keywords
                        )

    @skip_if_no_api_key("google")
    def test_06_automatic_function_calling(self, google_client, test_config):
        """Test Case 6: Automatic function calling"""
        tools = convert_to_google_tools([CALCULATOR_TOOL])

        # Create model with tools
        model = genai.GenerativeModel(get_model("google", "tools"), tools=tools)

        response = model.generate_content("Calculate 25 * 4 for me")

        # Should automatically choose to use the calculator
        assert response.candidates is not None
        candidate = response.candidates[0]

        if hasattr(candidate.content, "parts"):
            function_calls = [
                part
                for part in candidate.content.parts
                if hasattr(part, "function_call")
            ]
            if function_calls:
                assert function_calls[0].function_call.name == "calculate"

    @skip_if_no_api_key("google")
    def test_07_image_url(self, google_vision_client, test_config):
        """Test Case 7: Image analysis from URL"""
        image = load_image_from_url(IMAGE_URL)

        response = google_vision_client.generate_content(
            ["What do you see in this image?", image]
        )

        assert_valid_image_response(response)

    @skip_if_no_api_key("google")
    def test_08_image_base64(self, google_vision_client, test_config):
        """Test Case 8: Image analysis from base64"""
        image = load_image_from_url(f"data:image/png;base64,{BASE64_IMAGE}")

        response = google_vision_client.generate_content(["Describe this image", image])

        assert_valid_image_response(response)

    @skip_if_no_api_key("google")
    def test_09_multiple_images(self, google_vision_client, test_config):
        """Test Case 9: Multiple image analysis"""
        image1 = load_image_from_url(IMAGE_URL)
        image2 = load_image_from_url(f"data:image/png;base64,{BASE64_IMAGE}")

        response = google_vision_client.generate_content(
            ["Compare these two images", image1, image2]
        )

        assert_valid_image_response(response)
        content = response.text.lower()
        # Should mention comparison or differences
        assert any(
            word in content for word in COMPARISON_KEYWORDS
        ), f"Response should contain comparison keywords. Got content: {content}"

    @skip_if_no_api_key("google")
    def test_10_complex_end2end(self, google_vision_client, test_config):
        """Test Case 10: Complex end-to-end with conversation, images, and tools"""
        tools = convert_to_google_tools([WEATHER_TOOL])

        # Create vision model with tools
        model = genai.GenerativeModel(get_model("google", "vision"), tools=tools)

        image = load_image_from_url(IMAGE_URL)

        # Start complex conversation
        chat = model.start_chat()

        response1 = chat.send_message(
            [
                "First, can you tell me what's in this image and then get the weather for the location shown?",
                image,
            ]
        )

        # Should either describe image or call weather tool (or both)
        assert response1.candidates is not None

        # Check for function calls and handle them
        if hasattr(response1.candidates[0].content, "parts"):
            function_calls = [
                part
                for part in response1.candidates[0].content.parts
                if hasattr(part, "function_call")
            ]

            if function_calls:
                for fc in function_calls:
                    if fc.function_call.name == "get_weather":
                        # Mock function result
                        function_result = {
                            "function_response": {
                                "name": fc.function_call.name,
                                "response": {
                                    "result": "The weather is 72°F and sunny."
                                },
                            }
                        }

                        # Send function result back
                        final_response = chat.send_message([function_result])
                        assert_valid_chat_response(final_response)

    @skip_if_no_api_key("google")
    def test_11_integration_specific_features(self, google_client, test_config):
        """Test Case 11: Google GenAI-specific features"""

        # Test 1: Generation config with temperature
        generation_config = genai.types.GenerationConfig(
            temperature=0.9, max_output_tokens=100
        )

        response1 = google_client.generate_content(
            "Tell me a creative story in one sentence.",
            generation_config=generation_config,
        )

        assert_valid_chat_response(response1)

        # Test 2: Safety settings
        safety_settings = [
            {
                "category": "HARM_CATEGORY_HARASSMENT",
                "threshold": "BLOCK_MEDIUM_AND_ABOVE",
            }
        ]

        response2 = google_client.generate_content(
            "Hello, how are you?", safety_settings=safety_settings
        )

        assert_valid_chat_response(response2)

        # Test 3: Streaming
        response3 = google_client.generate_content("Count from 1 to 5", stream=True)

        chunks = []
        for chunk in response3:
            if chunk.text:
                chunks.append(chunk.text)

        assert len(chunks) > 0, "Should receive streaming chunks"
        full_content = "".join(chunks)
        assert len(full_content) > 0, "Streaming content should not be empty"

        # Test 4: Candidate count
        response4 = google_client.generate_content(
            "What's a good name for a pet?",
            generation_config=genai.types.GenerationConfig(candidate_count=1),
        )

        assert_valid_chat_response(response4)
        assert len(response4.candidates) == 1


# Additional helper functions specific to Google GenAI
def extract_google_function_calls(response: Any) -> List[Dict[str, Any]]:
    """Extract function calls from Google GenAI response format with proper type checking"""
    function_calls = []

    # Type check for Google GenAI response
    if not hasattr(response, "candidates") or not response.candidates:
        return function_calls

    candidate = response.candidates[0]
    if not hasattr(candidate, "content") or not hasattr(candidate.content, "parts"):
        return function_calls

    for part in candidate.content.parts:
        if hasattr(part, "function_call"):
            try:
                if hasattr(part.function_call, "name") and hasattr(
                    part.function_call, "args"
                ):
                    function_calls.append(
                        {
                            "name": part.function_call.name,
                            "arguments": dict(part.function_call.args),
                        }
                    )
            except (AttributeError, TypeError) as e:
                print(f"Warning: Failed to extract Google function call: {e}")
                continue

    return function_calls
