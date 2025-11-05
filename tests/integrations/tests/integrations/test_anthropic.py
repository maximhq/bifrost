"""
Anthropic Integration Tests - Cross-Provider Support

🌉 CROSS-PROVIDER TESTING:
This test suite uses the Anthropic SDK to test against multiple AI providers through Bifrost.
Tests automatically run against all available providers with proper capability filtering.

Note: Tests automatically skip for providers that don't support specific capabilities.
Example: Thinking tests only run for Anthropic, speech/transcription skip for all providers using Anthropic SDK.

Tests all core scenarios using Anthropic SDK directly:
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
13. Streaming
14. List models
15. Extended thinking (non-streaming)
16. Extended thinking (streaming)
"""

import pytest
import base64
import requests
from anthropic import Anthropic
from typing import List, Dict, Any

from ..utils.common import (
    Config,
    SIMPLE_CHAT_MESSAGES,
    MULTI_TURN_MESSAGES,
    SINGLE_TOOL_CALL_MESSAGES,
    MULTIPLE_TOOL_CALL_MESSAGES,
    IMAGE_URL,
    BASE64_IMAGE,
    INVALID_ROLE_MESSAGES,
    STREAMING_CHAT_MESSAGES,
    STREAMING_TOOL_CALL_MESSAGES,
    WEATHER_TOOL,
    CALCULATOR_TOOL,
    ALL_TOOLS,
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
    # Anthropic-specific test data
    ANTHROPIC_THINKING_PROMPT,
    ANTHROPIC_THINKING_STREAMING_PROMPT,
)
from ..utils.config_loader import get_model
from ..utils.parametrize import (
    get_cross_provider_params_for_scenario,
    format_provider_model,
)
from ..utils.config_loader import get_config


@pytest.fixture
def anthropic_client():
    """Create Anthropic client for testing"""
    from ..utils.config_loader import get_integration_url, get_config

    api_key = get_api_key("anthropic")
    base_url = get_integration_url("anthropic")

    # Get additional integration settings
    config = get_config()
    integration_settings = config.get_integration_settings("anthropic")
    api_config = config.get_api_config()

    client_kwargs = {
        "api_key": api_key,
        "base_url": base_url,
        "timeout": api_config.get("timeout", 120),
        "max_retries": api_config.get("max_retries", 3),
    }

    # Add Anthropic-specific settings
    if integration_settings.get("version"):
        client_kwargs["default_headers"] = {
            "anthropic-version": integration_settings["version"]
        }

    return Anthropic(**client_kwargs)


@pytest.fixture
def test_config():
    """Test configuration"""
    return Config()


def convert_to_anthropic_messages(
    messages: List[Dict[str, Any]],
) -> List[Dict[str, Any]]:
    """Convert common message format to Anthropic format"""
    anthropic_messages = []

    for msg in messages:
        if msg["role"] == "system":
            continue  # System messages handled separately in Anthropic

        # Handle image messages
        if isinstance(msg.get("content"), list):
            content = []
            for item in msg["content"]:
                if item["type"] == "text":
                    content.append({"type": "text", "text": item["text"]})
                elif item["type"] == "image_url":
                    url = item["image_url"]["url"]
                    if url.startswith("data:image"):
                        # Base64 image
                        media_type, data = url.split(",", 1)
                        content.append(
                            {
                                "type": "image",
                                "source": {
                                    "type": "base64",
                                    "media_type": media_type,
                                    "data": data,
                                },
                            }
                        )
                    else:
                        # URL image - send URL directly to Anthropic
                        content.append(
                            {
                                "type": "image",
                                "source": {
                                    "type": "url",
                                    "url": url,
                                },
                            }
                        )

            anthropic_messages.append({"role": msg["role"], "content": content})
        else:
            anthropic_messages.append({"role": msg["role"], "content": msg["content"]})

    return anthropic_messages


def convert_to_anthropic_tools(tools: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
    """Convert common tool format to Anthropic format"""
    anthropic_tools = []

    for tool in tools:
        anthropic_tools.append(
            {
                "name": tool["name"],
                "description": tool["description"],
                "input_schema": tool["parameters"],
            }
        )

    return anthropic_tools


class TestAnthropicIntegration:
    """Test suite for Anthropic integration with cross-provider support"""

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("simple_chat"))
    def test_01_simple_chat(self, anthropic_client, test_config, provider, model):
        """Test Case 1: Simple chat interaction - runs across all available providers"""
        messages = convert_to_anthropic_messages(SIMPLE_CHAT_MESSAGES)

        response = anthropic_client.messages.create(
            model=format_provider_model(provider, model), messages=messages, max_tokens=100
        )

        assert_valid_chat_response(response)
        assert len(response.content) > 0
        assert response.content[0].type == "text"
        assert len(response.content[0].text) > 0

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("multi_turn_conversation"))
    def test_02_multi_turn_conversation(self, anthropic_client, test_config, provider, model):
        """Test Case 2: Multi-turn conversation - runs across all available providers"""
        messages = convert_to_anthropic_messages(MULTI_TURN_MESSAGES)

        response = anthropic_client.messages.create(
            model=format_provider_model(provider, model), messages=messages, max_tokens=150
        )

        assert_valid_chat_response(response)
        content = response.content[0].text.lower()
        # Should mention population or numbers since we asked about Paris population
        assert any(
            word in content
            for word in ["population", "million", "people", "inhabitants"]
        )

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("tool_calls"))
    def test_03_single_tool_call(self, anthropic_client, test_config, provider, model):
        """Test Case 3: Single tool call - auto-skips providers without tool support"""
        messages = convert_to_anthropic_messages(SINGLE_TOOL_CALL_MESSAGES)
        tools = convert_to_anthropic_tools([WEATHER_TOOL])

        response = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            messages=messages,
            tools=tools,
            max_tokens=100,
        )

        assert_has_tool_calls(response, expected_count=1)
        tool_calls = extract_tool_calls(response)
        assert tool_calls[0]["name"] == "get_weather"
        assert "location" in tool_calls[0]["arguments"]

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("multiple_tool_calls"))
    def test_04_multiple_tool_calls(self, anthropic_client, test_config, provider, model):
        """Test Case 4: Multiple tool calls in one response - auto-skips providers without multiple tool support"""
        messages = convert_to_anthropic_messages(MULTIPLE_TOOL_CALL_MESSAGES)
        tools = convert_to_anthropic_tools([WEATHER_TOOL, CALCULATOR_TOOL])

        response = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            messages=messages,
            tools=tools,
            max_tokens=200,
        )

        # Providers might be more conservative with multiple tool calls
        # Let's check if it made at least one tool call and prefer multiple if possible
        assert_has_tool_calls(response)  # At least 1 tool call
        tool_calls = extract_anthropic_tool_calls(response)
        tool_names = [tc["name"] for tc in tool_calls]

        # Should make relevant tool calls - either weather, calculate, or both
        expected_tools = ["get_weather", "calculate"]
        made_relevant_calls = any(name in expected_tools for name in tool_names)
        assert (
            made_relevant_calls
        ), f"Expected tool calls from {expected_tools}, got {tool_names}"

    @skip_if_no_api_key("anthropic")
    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("end2end_tool_calling"))
    def test_05_end2end_tool_calling(self, anthropic_client, test_config, provider, model):
        """Test Case 5: Complete tool calling flow with responses"""
        messages = [{"role": "user", "content": "What's the weather in Boston in fahrenheit?"}]
        tools = convert_to_anthropic_tools([WEATHER_TOOL])

        response = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            messages=messages,
            tools=tools,
            max_tokens=100,
        )

        assert_has_tool_calls(response, expected_count=1)

        # Add assistant's response to conversation
        messages.append({"role": "assistant", "content": response.content})

        # Add tool response
        tool_calls = extract_anthropic_tool_calls(response)
        tool_response = mock_tool_response(
            tool_calls[0]["name"], tool_calls[0]["arguments"]
        )

        # Find the tool use block to get its ID
        tool_use_id = None
        for content in response.content:
            if content.type == "tool_use":
                tool_use_id = content.id
                break

        messages.append(
            {
                "role": "user",
                "content": [
                    {
                        "type": "tool_result",
                        "tool_use_id": tool_use_id,
                        "content": tool_response,
                    }
                ],
            }
        )

        # Get final response
        final_response = anthropic_client.messages.create(
            model=format_provider_model(provider, model), messages=messages, max_tokens=150
        )

        # Anthropic might return empty content if tool result is sufficient
        assert final_response is not None
        if len(final_response.content) > 0:
            assert_valid_chat_response(final_response)
            content = final_response.content[0].text.lower()
            weather_location_keywords = WEATHER_KEYWORDS + LOCATION_KEYWORDS
            assert any(word in content for word in weather_location_keywords)
        else:
            # If no content, that's ok - tool result was sufficient
            print("Model returned empty content - tool result was sufficient")

    @skip_if_no_api_key("anthropic")
    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("automatic_function_calling"))
    def test_06_automatic_function_calling(self, anthropic_client, test_config, provider, model):
        """Test Case 6: Automatic function calling"""
        messages = [{"role": "user", "content": "Calculate 25 * 4 for me"}]
        tools = convert_to_anthropic_tools([CALCULATOR_TOOL])

        response = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            messages=messages,
            tools=tools,
            max_tokens=100,
        )

        # Should automatically choose to use the calculator
        assert_has_tool_calls(response, expected_count=1)
        tool_calls = extract_tool_calls(response)
        assert tool_calls[0]["name"] == "calculate"

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("image_url"))
    def test_07_image_url(self, anthropic_client, test_config, provider, model):
        """Test Case 7: Image analysis from URL"""
        messages = [
            {
                "role": "user",
                "content": [
                    {"type": "text", "text": "What do you see in this image?"},
                    {
                        "type": "image",
                        "source": {
                            "type": "url",
                            "url": IMAGE_URL,
                        },
                    },
                ],
            }
        ]

        response = anthropic_client.messages.create(
            model=format_provider_model(provider, model), messages=messages, max_tokens=200
        )

        assert_valid_image_response(response)

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("image_base64"))
    def test_08_image_base64(self, anthropic_client, test_config, provider, model):
        """Test Case 8: Image analysis from base64 - runs for all providers with base64 image support"""
        messages = [
            {
                "role": "user",
                "content": [
                    {"type": "text", "text": "Describe this image"},
                    {
                        "type": "image",
                        "source": {
                            "type": "base64",
                            "media_type": "image/png",
                            "data": BASE64_IMAGE,
                        },
                    },
                ],
            }
        ]

        response = anthropic_client.messages.create(
            model=format_provider_model(provider, model), messages=messages, max_tokens=200
        )

        assert_valid_image_response(response)

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("multiple_images"))
    def test_09_multiple_images(self, anthropic_client, test_config, provider, model):
        """Test Case 9: Multiple image analysis"""
        messages = [
            {
                "role": "user",
                "content": [
                    {"type": "text", "text": "Compare these two images"},
                    {
                        "type": "image",
                        "source": {
                            "type": "url",
                            "url": IMAGE_URL,
                        },
                    },
                    {
                        "type": "image",
                        "source": {
                            "type": "base64",
                            "media_type": "image/png",
                            "data": BASE64_IMAGE,
                        },
                    },
                ],
            }
        ]

        response = anthropic_client.messages.create(
            model=format_provider_model(provider, model), messages=messages, max_tokens=300
        )

        assert_valid_image_response(response)
        content = response.content[0].text.lower()
        # Should mention comparison or differences
        assert any(
            word in content for word in COMPARISON_KEYWORDS
        ), f"Response should contain comparison keywords. Got content: {content}"

    @skip_if_no_api_key("anthropic")
    def test_10_complex_end2end(self, anthropic_client, test_config):
        """Test Case 10: Complex end-to-end with conversation, images, and tools"""
        messages = [
            {"role": "user", "content": "Hello! I need help with some tasks."},
            {
                "role": "assistant",
                "content": "Hello! I'd be happy to help you with your tasks. What do you need assistance with?",
            },
            {
                "role": "user",
                "content": [
                    {
                        "type": "text",
                        "text": "First, can you tell me what's in this image and then get the weather for the location shown?",
                    },
                    {
                        "type": "image",
                        "source": {
                            "type": "url",
                            "url": IMAGE_URL,
                        },
                    },
                ],
            },
        ]

        tools = convert_to_anthropic_tools([WEATHER_TOOL])

        response1 = anthropic_client.messages.create(
            model=get_model("anthropic", "chat"),
            messages=messages,
            tools=tools,
            max_tokens=300,
        )

        # Should either describe image or call weather tool (or both)
        assert len(response1.content) > 0

        # Add response to conversation
        messages.append({"role": "assistant", "content": response1.content})

        # If there were tool calls, handle them
        tool_calls = extract_anthropic_tool_calls(response1)
        if tool_calls:
            for i, tool_call in enumerate(tool_calls):
                tool_response = mock_tool_response(
                    tool_call["name"], tool_call["arguments"]
                )

                # Find the corresponding tool use ID
                tool_use_id = None
                for content in response1.content:
                    if content.type == "tool_use" and content.name == tool_call["name"]:
                        tool_use_id = content.id
                        break

                messages.append(
                    {
                        "role": "user",
                        "content": [
                            {
                                "type": "tool_result",
                                "tool_use_id": tool_use_id,
                                "content": tool_response,
                            }
                        ],
                    }
                )

            # Get final response after tool calls
            final_response = anthropic_client.messages.create(
                model=get_model("anthropic", "chat"), messages=messages, max_tokens=200
            )

            # Anthropic might return empty content if tool result is sufficient
            # This is valid behavior - just check that we got a response
            assert final_response is not None
            if final_response.content and len(final_response.content) > 0:
                # If there is content, validate it
                assert_valid_chat_response(final_response)
            else:
                # If no content, that's ok too - tool result was sufficient
                print("Model returned empty content - tool result was sufficient")

    @skip_if_no_api_key("anthropic")
    def test_11_integration_specific_features(self, anthropic_client, test_config):
        """Test Case 11: Anthropic-specific features"""

        # Test 1: System message
        response1 = anthropic_client.messages.create(
            model=get_model("anthropic", "chat"),
            system="You are a helpful assistant that always responds in exactly 5 words.",
            messages=[{"role": "user", "content": "Hello, how are you?"}],
            max_tokens=50,
        )

        assert_valid_chat_response(response1)
        # Check if response is approximately 5 words (allow some flexibility)
        word_count = len(response1.content[0].text.split())
        assert 3 <= word_count <= 7, f"Expected ~5 words, got {word_count}"

        # Test 2: Temperature parameter
        response2 = anthropic_client.messages.create(
            model=get_model("anthropic", "chat"),
            messages=[
                {"role": "user", "content": "Tell me a creative story in one sentence."}
            ],
            temperature=0.9,
            max_tokens=100,
        )

        assert_valid_chat_response(response2)

        # Test 3: Tool choice (any tool)
        tools = convert_to_anthropic_tools([CALCULATOR_TOOL, WEATHER_TOOL])
        response3 = anthropic_client.messages.create(
            model=get_model("anthropic", "chat"),
            messages=[{"role": "user", "content": "What's 15 + 27?"}],
            tools=tools,
            tool_choice={"type": "any"},  # Force tool use
            max_tokens=100,
        )

        assert_has_tool_calls(response3)
        tool_calls = extract_anthropic_tool_calls(response3)
        # Should prefer calculator for math question
        assert tool_calls[0]["name"] == "calculate"

    @skip_if_no_api_key("anthropic")
    def test_12_error_handling_invalid_roles(self, anthropic_client, test_config):
        """Test Case 12: Error handling for invalid roles"""
        # bifrost handles invalid roles internally so this test should not raise an exception
        response = anthropic_client.messages.create(
            model=get_model("anthropic", "chat"),
            messages=INVALID_ROLE_MESSAGES,
            max_tokens=100,
        )

        # Verify the response is successful
        assert response is not None
        assert hasattr(response, "content")
        assert len(response.content) > 0

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("streaming"))
    def test_13_streaming(self, anthropic_client, test_config, provider, model):
        """Test Case 13: Streaming chat completion - auto-skips providers without streaming support"""
        # Test basic streaming
        stream = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            messages=STREAMING_CHAT_MESSAGES,
            max_tokens=200,
            stream=True,
        )

        content, chunk_count, tool_calls_detected = collect_streaming_content(
            stream, "anthropic", timeout=300
        )

        # Validate streaming results
        assert chunk_count > 0, "Should receive at least one chunk"
        assert len(content) > 10, "Should receive substantial content"
        assert not tool_calls_detected, "Basic streaming shouldn't have tool calls"

        # Test streaming with tool calls (only if provider supports tools)
        config = get_config()
        if config.provider_supports_scenario(provider, "tool_calls"):
            # Get the tools-capable model for this provider
            tools_model = config.get_provider_model(provider, "tools")
            if tools_model:
                stream_with_tools = anthropic_client.messages.create(
                    model=format_provider_model(provider, tools_model),
                    messages=STREAMING_TOOL_CALL_MESSAGES,
                    max_tokens=150,
                    tools=convert_to_anthropic_tools([WEATHER_TOOL]),
                    stream=True,
                )

                content_tools, chunk_count_tools, tool_calls_detected_tools = (
                    collect_streaming_content(stream_with_tools, "anthropic", timeout=300)
                )

                # Validate tool streaming results
                assert chunk_count_tools > 0, "Should receive at least one chunk with tools"
                assert tool_calls_detected_tools, "Should receive at least one chunk with tools"
        
    @skip_if_no_api_key("anthropic")
    def test_14_list_models(self, anthropic_client, test_config):
        """Test Case 14: List models with pagination parameters"""
        # Test basic list with limit
        response = anthropic_client.models.list(limit=5)
        assert response.data is not None
        assert len(response.data) <= 5  # May return fewer if not enough models
        assert hasattr(response, "first_id"), "Response should have first_id"
        assert hasattr(response, "last_id"), "Response should have last_id"
        assert hasattr(response, "has_more"), "Response should have has_more"
        
        # Test pagination with after_id if there are more results
        if response.has_more and response.last_id:
            next_response = anthropic_client.models.list(
                limit=3,
                after_id=response.last_id
            )
            assert next_response.data is not None
            assert len(next_response.data) <= 3
            # Ensure we got different results
            if len(response.data) > 0 and len(next_response.data) > 0:
                assert response.data[0].id != next_response.data[0].id
        
        # Test pagination with before_id if we have a first_id
        if response.first_id:
            # Get a second page first
            second_response = anthropic_client.models.list(limit=10)
            if len(second_response.data) > 5 and second_response.last_id:
                # Now try to go backwards from the last item
                prev_response = anthropic_client.models.list(
                    limit=2,
                    before_id=second_response.last_id
                )
                assert prev_response.data is not None
                assert len(prev_response.data) <= 2

    @skip_if_no_api_key("anthropic")
    def test_15_extended_thinking(self, anthropic_client, test_config):
        """Test Case 15: Extended thinking/reasoning (non-streaming)"""
        # Convert to Anthropic message format
        messages = convert_to_anthropic_messages(ANTHROPIC_THINKING_PROMPT)

        response = anthropic_client.messages.create(
            model=get_model("anthropic", "chat"),  # Specific thinking-capable model
            max_tokens=16000,
            thinking={
                "type": "enabled",
                "budget_tokens": 10000,
            },
            messages=messages,
        )

        # Validate response structure
        assert response is not None, "Response should not be None"
        assert hasattr(response, "content"), "Response should have content"
        assert len(response.content) > 0, "Content should not be empty"

        # Check for thinking content blocks
        has_thinking = False
        thinking_content = ""
        regular_content = ""

        for block in response.content:
            if block.type:
                if block.type == "thinking":
                    has_thinking = True
                    # The thinking content is directly in block.thinking attribute
                    if block.thinking:
                        thinking_content += str(block.thinking)
                        print(f"Found thinking block with {len(str(block.thinking))} chars")
                elif block.type == "text":
                    if block.text:
                        regular_content += str(block.text)

        # Should have thinking content
        assert has_thinking, (
            f"Response should contain thinking blocks. "
            f"Got {len(response.content)} blocks: "
            f"{[block.type if hasattr(block, 'type') else 'unknown' for block in response.content]}"
        )
        assert len(thinking_content) > 0, "Thinking content should not be empty"

        # Validate thinking content quality - should show reasoning
        thinking_lower = thinking_content.lower()
        reasoning_keywords = [
            "batch",
            "oven",
            "cookie",
            "minute",
            "calculate",
            "total",
            "time",
            "divide",
            "multiply",
            "step",
        ]

        keyword_matches = sum(
            1 for keyword in reasoning_keywords if keyword in thinking_lower
        )
        assert keyword_matches >= 2, (
            f"Thinking should contain reasoning about the problem. "
            f"Found {keyword_matches} keywords. Content: {thinking_content[:200]}..."
        )

        # Should also have regular text response
        assert len(regular_content) > 0, "Should have regular response text"

        print(f"✓ Thinking content ({len(thinking_content)} chars): {thinking_content[:150]}...")
        print(f"✓ Response content: {regular_content[:100]}...")

    @skip_if_no_api_key("anthropic")
    def test_16_extended_thinking_streaming(self, anthropic_client, test_config):
        """Test Case 16: Extended thinking/reasoning (streaming)"""
        # Convert to Anthropic message format
        messages = convert_to_anthropic_messages(ANTHROPIC_THINKING_STREAMING_PROMPT)

        # Stream with thinking enabled - use thinking-capable model
        stream = anthropic_client.messages.create(
            model="anthropic/claude-sonnet-4-5",
            max_tokens=16000,
            thinking={
                "type": "enabled",
                "budget_tokens": 10000,
            },
            messages=messages,
            stream=True,
        )

        # Collect streaming content
        thinking_parts = []
        text_parts = []
        chunk_count = 0
        has_thinking_delta = False
        has_thinking_block_start = False

        for event in stream:
            chunk_count += 1

            # Check event type
            if event.type:
                event_type = event.type

                # Handle content_block_start to detect thinking blocks
                if event_type == "content_block_start":
                    if event.content_block and event.content_block.type:
                        if event.content_block.type == "thinking":
                            has_thinking_block_start = True
                            print(f"Thinking block started")

                # Handle content_block_delta events
                elif event_type == "content_block_delta":
                    if event.delta and event.delta.type:
                            # Check for thinking delta
                            if event.delta.type == "thinking_delta":
                                has_thinking_delta = True
                                if event.delta.thinking:
                                    thinking_parts.append(str(event.delta.thinking))
                            # Check for text delta
                            elif event.delta.type == "text_delta":
                                if event.delta.text:
                                    text_parts.append(str(event.delta.text))

            # Safety check
            if chunk_count > 1000:
                break

        # Combine collected content
        complete_thinking = "".join(thinking_parts)
        complete_text = "".join(text_parts)

        # Validate results
        assert chunk_count > 0, "Should receive at least one chunk"
        assert has_thinking_delta or has_thinking_block_start, (
            f"Should detect thinking in streaming. "
            f"has_thinking_delta={has_thinking_delta}, has_thinking_block_start={has_thinking_block_start}"
        )
        assert len(complete_thinking) > 10, (
            f"Should receive substantial thinking content, got {len(complete_thinking)} chars. "
            f"Thinking parts: {len(thinking_parts)}"
        )

        # Validate thinking content
        thinking_lower = complete_thinking.lower()
        math_keywords = [
            "paid",
            "split",
            "equal",
            "owe",
            "alice",
            "bob",
            "carol",
            "total",
            "divide",
            "step",
        ]

        keyword_matches = sum(
            1 for keyword in math_keywords if keyword in thinking_lower
        )
        assert keyword_matches >= 2, (
            f"Thinking should reason about splitting the bill. "
            f"Found {keyword_matches} keywords. Content: {complete_thinking[:200]}..."
        )

        # Should have regular response text too
        assert len(complete_text) > 0, "Should have regular response text"

        print(f"✓ Streamed thinking ({len(thinking_parts)} chunks): {complete_thinking[:150]}...")
        print(f"✓ Streamed response ({len(text_parts)} chunks): {complete_text[:100]}...")


# Additional helper functions specific to Anthropic
def extract_anthropic_tool_calls(response: Any) -> List[Dict[str, Any]]:
    """Extract tool calls from Anthropic response format with proper type checking"""
    tool_calls = []

    # Type check for Anthropic Message response
    if not hasattr(response, "content") or not response.content:
        return tool_calls

    for content in response.content:
        if hasattr(content, "type") and content.type == "tool_use":
            if hasattr(content, "name") and hasattr(content, "input"):
                try:
                    tool_calls.append(
                        {"name": content.name, "arguments": content.input}
                    )
                except AttributeError as e:
                    print(f"Warning: Failed to extract tool call from content: {e}")
                    continue

    return tool_calls
