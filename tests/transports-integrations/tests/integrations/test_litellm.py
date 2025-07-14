"""
LiteLLM Integration Tests

🤖 MODELS USED:
- Chat: gpt-3.5-turbo (OpenAI via LiteLLM)
- Vision: gpt-4o (OpenAI via LiteLLM)
- Tools: gpt-3.5-turbo (OpenAI via LiteLLM)
- Alternatives: claude-3-haiku-20240307, gemini-pro, gpt-4, command-r-plus

Tests all 11 core scenarios using LiteLLM SDK directly:
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
import litellm
from typing import List, Dict, Any

from ..utils.common import (
    Config,
    SIMPLE_CHAT_MESSAGES,
    MULTI_TURN_MESSAGES,
    SINGLE_TOOL_CALL_MESSAGES,
    MULTIPLE_TOOL_CALL_MESSAGES,
    IMAGE_URL_MESSAGES,
    IMAGE_BASE64_MESSAGES,
    MULTIPLE_IMAGES_MESSAGES,
    COMPLEX_E2E_MESSAGES,
    INVALID_ROLE_MESSAGES,
    STREAMING_CHAT_MESSAGES,
    STREAMING_TOOL_CALL_MESSAGES,
    WEATHER_TOOL,
    CALCULATOR_TOOL,
    mock_tool_response,
    assert_valid_chat_response,
    assert_has_tool_calls,
    assert_valid_image_response,
    assert_valid_error_response,
    assert_error_propagation,
    assert_valid_streaming_response,
    collect_streaming_content,
    extract_tool_calls,
    get_api_key,
    skip_if_no_api_key,
    COMPARISON_KEYWORDS,
    WEATHER_KEYWORDS,
    LOCATION_KEYWORDS,
)
from ..utils.config_loader import get_model


@pytest.fixture
def test_config():
    """Test configuration"""
    return Config()


@pytest.fixture(autouse=True)
def setup_litellm():
    """Setup LiteLLM with Bifrost configuration"""
    from ..utils.config_loader import get_integration_url, get_config

    # Get Bifrost URL for LiteLLM
    base_url = get_integration_url("litellm")
    config = get_config()
    integration_settings = config.get_integration_settings("litellm")
    api_config = config.get_api_config()

    # Configure LiteLLM globally
    if base_url:
        litellm.api_base = base_url

    # Set timeout and other settings
    litellm.request_timeout = api_config.get("timeout", 30)

    # Apply integration-specific settings
    if integration_settings.get("drop_params"):
        litellm.drop_params = integration_settings["drop_params"]
    if integration_settings.get("debug"):
        litellm.set_verbose = integration_settings["debug"]


def convert_to_litellm_tools(tools: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
    """Convert common tool format to LiteLLM format (OpenAI-compatible)"""
    return [{"type": "function", "function": tool} for tool in tools]


class TestLiteLLMIntegration:
    """Test suite for LiteLLM integration covering all 11 core scenarios"""

    def test_01_simple_chat(self, test_config):
        """Test Case 1: Simple chat interaction"""
        response = litellm.completion(
            model=get_model("litellm", "chat"),
            messages=SIMPLE_CHAT_MESSAGES,
            max_tokens=100,
        )

        assert_valid_chat_response(response)
        assert response.choices[0].message.content is not None
        assert len(response.choices[0].message.content) > 0

    def test_02_multi_turn_conversation(self, test_config):
        """Test Case 2: Multi-turn conversation"""
        response = litellm.completion(
            model=get_model("litellm", "chat"),
            messages=MULTI_TURN_MESSAGES,
            max_tokens=150,
        )

        assert_valid_chat_response(response)
        content = response.choices[0].message.content.lower()
        # Should mention population or numbers since we asked about Paris population
        assert any(
            word in content
            for word in ["population", "million", "people", "inhabitants"]
        )

    def test_03_single_tool_call(self, test_config):
        """Test Case 3: Single tool call"""
        tools = convert_to_litellm_tools([WEATHER_TOOL])

        response = litellm.completion(
            model=get_model("litellm", "chat"),
            messages=SINGLE_TOOL_CALL_MESSAGES,
            tools=tools,
            max_tokens=100,
        )

        assert_has_tool_calls(response, expected_count=1)
        tool_calls = extract_tool_calls(response)
        assert tool_calls[0]["name"] == "get_weather"
        assert "location" in tool_calls[0]["arguments"]

    def test_04_multiple_tool_calls(self, test_config):
        """Test Case 4: Multiple tool calls in one response"""
        tools = convert_to_litellm_tools([WEATHER_TOOL, CALCULATOR_TOOL])

        response = litellm.completion(
            model=get_model("litellm", "chat"),
            messages=MULTIPLE_TOOL_CALL_MESSAGES,
            tools=tools,
            max_tokens=200,
        )

        assert_has_tool_calls(response, expected_count=2)
        tool_calls = extract_tool_calls(response)
        tool_names = [tc["name"] for tc in tool_calls]
        assert "get_weather" in tool_names
        assert "calculate" in tool_names

    def test_05_end2end_tool_calling(self, test_config):
        """Test Case 5: Complete tool calling flow with responses"""
        messages = [{"role": "user", "content": "What's the weather in Boston?"}]
        tools = convert_to_litellm_tools([WEATHER_TOOL])

        response = litellm.completion(
            model=get_model("litellm", "chat"),
            messages=messages,
            tools=tools,
            max_tokens=100,
        )

        assert_has_tool_calls(response, expected_count=1)

        # Add assistant's tool call to conversation
        messages.append(response.choices[0].message)

        # Add tool response
        tool_calls = extract_litellm_tool_calls(response)
        tool_response = mock_tool_response(
            tool_calls[0]["name"], tool_calls[0]["arguments"]
        )

        messages.append(
            {
                "role": "tool",
                "tool_call_id": response.choices[0].message.tool_calls[0].id,
                "content": tool_response,
            }
        )

        # Get final response
        final_response = litellm.completion(
            model=get_model("litellm", "chat"), messages=messages, max_tokens=150
        )

        assert_valid_chat_response(final_response)
        content = final_response.choices[0].message.content.lower()
        weather_location_keywords = WEATHER_KEYWORDS + LOCATION_KEYWORDS
        assert any(word in content for word in weather_location_keywords)

    def test_06_automatic_function_calling(self, test_config):
        """Test Case 6: Automatic function calling"""
        tools = convert_to_litellm_tools([CALCULATOR_TOOL])

        response = litellm.completion(
            model=get_model("litellm", "chat"),
            messages=[{"role": "user", "content": "Calculate 25 * 4 for me"}],
            tools=tools,
            tool_choice="auto",
            max_tokens=100,
        )

        # Should automatically choose to use the calculator
        assert_has_tool_calls(response, expected_count=1)
        tool_calls = extract_litellm_tool_calls(response)
        assert tool_calls[0]["name"] == "calculate"

    def test_07_image_url(self, test_config):
        """Test Case 7: Image analysis from URL"""
        response = litellm.completion(
            model=get_model("litellm", "vision"),
            messages=IMAGE_URL_MESSAGES,
            max_tokens=200,
        )

        assert_valid_image_response(response)

    def test_08_image_base64(self, test_config):
        """Test Case 8: Image analysis from base64"""
        response = litellm.completion(
            model=get_model("litellm", "vision"),
            messages=IMAGE_BASE64_MESSAGES,
            max_tokens=200,
        )

        assert_valid_image_response(response)

    def test_09_multiple_images(self, test_config):
        """Test Case 9: Multiple image analysis"""
        response = litellm.completion(
            model=get_model("litellm", "vision"),
            messages=MULTIPLE_IMAGES_MESSAGES,
            max_tokens=300,
        )

        assert_valid_image_response(response)
        content = response.choices[0].message.content.lower()
        # Should mention comparison or differences
        assert any(
            word in content for word in COMPARISON_KEYWORDS
        ), f"Response should contain comparison keywords. Got content: {content}"

    def test_10_complex_end2end(self, test_config):
        """Test Case 10: Complex end-to-end with conversation, images, and tools"""
        messages = COMPLEX_E2E_MESSAGES.copy()
        tools = convert_to_litellm_tools([WEATHER_TOOL])

        # First, analyze the image
        response1 = litellm.completion(
            model=get_model("litellm", "vision"),
            messages=messages,
            tools=tools,
            max_tokens=300,
        )

        # Should either describe image or call weather tool (or both)
        assert (
            response1.choices[0].message.content is not None
            or response1.choices[0].message.tool_calls is not None
        )

        # Add response to conversation
        messages.append(response1.choices[0].message)

        # If there were tool calls, handle them
        if response1.choices[0].message.tool_calls:
            for tool_call in response1.choices[0].message.tool_calls:
                tool_name = tool_call.function.name
                tool_args = json.loads(tool_call.function.arguments)
                tool_response = mock_tool_response(tool_name, tool_args)

                messages.append(
                    {
                        "role": "tool",
                        "tool_call_id": tool_call.id,
                        "content": tool_response,
                    }
                )

            # Get final response after tool calls
            final_response = litellm.completion(
                model=get_model("litellm", "vision"), messages=messages, max_tokens=200
            )

            assert_valid_chat_response(final_response)

    def test_11_integration_specific_features(self, test_config):
        """Test Case 11: LiteLLM-specific features"""

        # Test 1: Multiple integrations through LiteLLM
        integrations_to_test = [
            "gpt-3.5-turbo",  # OpenAI
            "claude-3-haiku-20240307",  # Anthropic
            # Add more integrations as needed
        ]

        for model in integrations_to_test:
            try:
                response = litellm.completion(
                    model=model,
                    messages=[{"role": "user", "content": "Hello, how are you?"}],
                    max_tokens=50,
                )

                assert_valid_chat_response(response)

            except Exception as e:
                # Some integrations might not be available, skip gracefully
                pytest.skip(f"Integration {model} not available: {e}")

        # Test 2: Function calling with specific tool choice
        tools = convert_to_litellm_tools([CALCULATOR_TOOL, WEATHER_TOOL])

        response2 = litellm.completion(
            model=get_model("litellm", "chat"),
            messages=[{"role": "user", "content": "What's 15 + 27?"}],
            tools=tools,
            tool_choice={"type": "function", "function": {"name": "calculate"}},
            max_tokens=100,
        )

        assert_has_tool_calls(response2, expected_count=1)
        tool_calls = extract_litellm_tool_calls(response2)
        assert tool_calls[0]["name"] == "calculate"

        # Test 3: Temperature and other parameters
        response3 = litellm.completion(
            model=get_model("litellm", "chat"),
            messages=[
                {"role": "user", "content": "Tell me a creative story in one sentence."}
            ],
            temperature=0.9,
            top_p=0.9,
            max_tokens=100,
        )

        assert_valid_chat_response(response3)

    def test_12_error_handling_invalid_roles(self, test_config):
        """Test Case 12: Error handling for invalid roles"""
        with pytest.raises(Exception) as exc_info:
            litellm.completion(
                model=get_model("litellm", "chat"),
                messages=INVALID_ROLE_MESSAGES,
                max_tokens=100,
            )

        # Verify the error is properly caught and contains role-related information
        error = exc_info.value
        assert_valid_error_response(error, "tester")
        assert_error_propagation(error, "litellm")

    def test_13_streaming(self, test_config):
        """Test Case 13: Streaming chat completion"""
        # Test basic streaming
        stream = litellm.completion(
            model=get_model("litellm", "chat"),
            messages=STREAMING_CHAT_MESSAGES,
            max_tokens=200,
            stream=True,
        )

        content, chunk_count, tool_calls_detected = collect_streaming_content(
            stream, "openai", timeout=30  # LiteLLM uses OpenAI format
        )

        # Validate streaming results
        assert chunk_count > 0, "Should receive at least one chunk"
        assert len(content) > 10, "Should receive substantial content"
        assert not tool_calls_detected, "Basic streaming shouldn't have tool calls"

        # Test streaming with tool calls
        stream_with_tools = litellm.completion(
            model=get_model("litellm", "tools"),
            messages=STREAMING_TOOL_CALL_MESSAGES,
            max_tokens=150,
            tools=convert_to_litellm_tools([WEATHER_TOOL]),
            stream=True,
        )

        content_tools, chunk_count_tools, tool_calls_detected_tools = (
            collect_streaming_content(
                stream_with_tools, "openai", timeout=30  # LiteLLM uses OpenAI format
            )
        )

        # Validate tool streaming results
        assert chunk_count_tools > 0, "Should receive at least one chunk with tools"
        assert (
            tool_calls_detected_tools
        ), "Should detect tool calls in streaming response"


# Additional helper functions specific to LiteLLM
def extract_litellm_tool_calls(response: Any) -> List[Dict[str, Any]]:
    """Extract tool calls from LiteLLM response format (OpenAI-compatible) with proper type checking"""
    tool_calls = []

    # Type check for LiteLLM response (OpenAI-compatible format)
    if not hasattr(response, "choices") or not response.choices:
        return tool_calls

    choice = response.choices[0]
    if not hasattr(choice, "message") or not hasattr(choice.message, "tool_calls"):
        return tool_calls

    if not choice.message.tool_calls:
        return tool_calls

    for tool_call in choice.message.tool_calls:
        if hasattr(tool_call, "function") and hasattr(tool_call.function, "name"):
            try:
                arguments = (
                    json.loads(tool_call.function.arguments)
                    if isinstance(tool_call.function.arguments, str)
                    else tool_call.function.arguments
                )
                tool_calls.append(
                    {
                        "name": tool_call.function.name,
                        "arguments": arguments,
                    }
                )
            except (json.JSONDecodeError, AttributeError) as e:
                print(f"Warning: Failed to parse LiteLLM tool call arguments: {e}")
                continue

    return tool_calls
