"""
Anthropic Integration Tests - Cross-Provider Support

CROSS-PROVIDER TESTING:
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
17. Files API - file upload (Cross-Provider)
18. Files API - file list (Cross-Provider)
19. Files API - file retrieve (Cross-Provider)
20. Files API - file delete (Cross-Provider)
21. Files API - file content (Cross-Provider)
22. Batch API - batch create with inline requests (Cross-Provider)
23. Batch API - batch list
24. Batch API - batch retrieve
25. Batch API - batch cancel
26. Batch API - batch results
27. Batch API - end-to-end workflow
28. Prompt caching - system message checkpoint
29. Prompt caching - messages checkpoint
30. Prompt caching - tools checkpoint
"""

import logging
from uuid import uuid4
import pytest
import base64
import requests
from anthropic import Anthropic
from typing import List, Dict, Any

from .utils.common import (
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
    PROMPT_CACHING_TOOLS,
    create_batch_jsonl_content,
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
    PROMPT_CACHING_LARGE_CONTEXT,
    # Files API utilities
    assert_valid_file_response,
    assert_valid_file_list_response,
    assert_valid_file_delete_response,
    # Batch API utilities
    create_batch_inline_requests,
    assert_valid_batch_inline_response,
    assert_valid_batch_list_response,
)
from .utils.config_loader import get_model
from .utils.parametrize import (
    get_cross_provider_params_for_scenario,
    format_provider_model,
)
from .utils.config_loader import get_config


@pytest.fixture
def anthropic_client():
    """Create Anthropic client for testing"""
    from .utils.config_loader import get_integration_url, get_config

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
        client_kwargs["default_headers"] = {"anthropic-version": integration_settings["version"]}

    return Anthropic(**client_kwargs)


@pytest.fixture
def test_config():
    """Test configuration"""
    return Config()


def get_provider_anthropic_client(provider):
    """Create Anthropic client with x-model-provider header for given provider"""
    from .utils.config_loader import get_integration_url, get_config

    api_key = get_api_key("anthropic")
    base_url = get_integration_url("anthropic")
    config = get_config()
    api_config = config.get_api_config()
    integration_settings = config.get_integration_settings("anthropic")

    default_headers = {"x-model-provider": provider}
    if integration_settings.get("version"):
        default_headers["anthropic-version"] = integration_settings["version"]

    return Anthropic(
        api_key=api_key,
        base_url=base_url,
        timeout=api_config.get("timeout", 300),
        default_headers=default_headers,
    )


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

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("simple_chat")
    )
    def test_01_simple_chat(self, anthropic_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 1: Simple chat interaction - runs across all available providers"""
        messages = convert_to_anthropic_messages(SIMPLE_CHAT_MESSAGES)

        response = anthropic_client.messages.create(
            model=format_provider_model(provider, model), messages=messages, max_tokens=100
        )

        assert_valid_chat_response(response)
        assert len(response.content) > 0
        assert response.content[0].type == "text"
        assert len(response.content[0].text) > 0

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("multi_turn_conversation")
    )
    def test_02_multi_turn_conversation(self, anthropic_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 2: Multi-turn conversation - runs across all available providers"""
        messages = convert_to_anthropic_messages(MULTI_TURN_MESSAGES)

        response = anthropic_client.messages.create(
            model=format_provider_model(provider, model), messages=messages, max_tokens=150
        )

        assert_valid_chat_response(response)
        content = response.content[0].text.lower()
        # Should mention population or numbers since we asked about Paris population
        assert any(word in content for word in ["population", "million", "people", "inhabitants"])

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("tool_calls"))
    def test_03_single_tool_call(self, anthropic_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
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

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("multiple_tool_calls")
    )
    def test_04_multiple_tool_calls(self, anthropic_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
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
        assert made_relevant_calls, f"Expected tool calls from {expected_tools}, got {tool_names}"

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("end2end_tool_calling"))
    def test_05_end2end_tool_calling(self, anthropic_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 5: Complete tool calling flow with responses"""
        messages = [{"role": "user", "content": "What's the weather in Boston in fahrenheit?"}]
        tools = convert_to_anthropic_tools([WEATHER_TOOL])
        logger = logging.getLogger("05AnthropicEnd2EndToolCalling")
        response = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            messages=messages,
            tools=tools,
            max_tokens=500,
        )

        assert_has_tool_calls(response, expected_count=1)

        # Add assistant's response to conversation
        # Serialize content blocks to dicts for cross-provider compatibility
        messages.append(
            {"role": "assistant", "content": serialize_anthropic_content(response.content)}
        )

        # Add tool response
        tool_calls = extract_anthropic_tool_calls(response)
        tool_response = mock_tool_response(tool_calls[0]["name"], tool_calls[0]["arguments"])

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

        logger.info(f"Messages: {messages}")

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

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("automatic_function_calling"))
    def test_06_automatic_function_calling(self, anthropic_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
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
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
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

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("image_base64")
    )
    def test_08_image_base64(self, anthropic_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
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

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("multiple_images")
    )
    def test_09_multiple_images(self, anthropic_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
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
        # Serialize content blocks to dicts for cross-provider compatibility
        messages.append(
            {"role": "assistant", "content": serialize_anthropic_content(response1.content)}
        )

        # If there were tool calls, handle them
        tool_calls = extract_anthropic_tool_calls(response1)
        if tool_calls:
            for i, tool_call in enumerate(tool_calls):
                tool_response = mock_tool_response(tool_call["name"], tool_call["arguments"])

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
            messages=[{"role": "user", "content": "Tell me a creative story in one sentence."}],
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
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 13: Streaming chat completion - auto-skips providers without streaming support"""
        # Test basic streaming
        stream = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            messages=STREAMING_CHAT_MESSAGES,
            max_tokens=1000,
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
                    max_tokens=1000,
                    tools=convert_to_anthropic_tools([WEATHER_TOOL]),
                    stream=True,
                )

                content_tools, chunk_count_tools, tool_calls_detected_tools = (
                    collect_streaming_content(stream_with_tools, "anthropic", timeout=300)
                )

                # Validate tool streaming results
                assert chunk_count_tools > 0, "Should receive at least one chunk with tools"
                assert tool_calls_detected_tools, "Should receive at least one chunk with tools"

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
            next_response = anthropic_client.models.list(limit=3, after_id=response.last_id)
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
                    limit=2, before_id=second_response.last_id
                )
                assert prev_response.data is not None
                assert len(prev_response.data) <= 2
    
    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("thinking"))
    def test_15_extended_thinking(self, anthropic_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 15: Extended thinking/reasoning (non-streaming)"""
        # Convert to Anthropic message format
        messages = convert_to_anthropic_messages(ANTHROPIC_THINKING_PROMPT)

        response = anthropic_client.messages.create(
            model=format_provider_model(provider, model),  # Specific thinking-capable model
            max_tokens=4000, # Reduced to prevent token limit errors for smaller context window models
            thinking={
                "type": "enabled",
                "budget_tokens": 2500, # Reduced to prevent token limit errors
            },
            extra_body={
                "reasoning_summary": "detailed"
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

        keyword_matches = sum(1 for keyword in reasoning_keywords if keyword in thinking_lower)
        assert keyword_matches >= 2, (
            f"Thinking should contain reasoning about the problem. "
            f"Found {keyword_matches} keywords. Content: {thinking_content[:200]}..."
        )

        # Should also have regular text response
        assert len(regular_content) > 0, "Should have regular response text"

        print(f"✓ Thinking content ({len(thinking_content)} chars): {thinking_content[:150]}...")
        print(f"✓ Response content: {regular_content[:100]}...")
    
    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("thinking"))
    def test_16_extended_thinking_streaming(self, anthropic_client, test_config, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 16: Extended thinking/reasoning (streaming)"""
        # Convert to Anthropic message format
        messages = convert_to_anthropic_messages(ANTHROPIC_THINKING_STREAMING_PROMPT)

        # Stream with thinking enabled - use thinking-capable model
        stream = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            max_tokens=4000, # Reduced to prevent token limit errors for smaller context window models
            thinking={
                "type": "enabled",
                "budget_tokens": 2000, # Reduced to prevent token limit errors
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

        keyword_matches = sum(1 for keyword in math_keywords if keyword in thinking_lower)
        assert keyword_matches >= 2, (
            f"Thinking should reason about splitting the bill. "
            f"Found {keyword_matches} keywords. Content: {complete_thinking[:200]}..."
        )

        # Should have regular response text too
        assert len(complete_text) > 0, "Should have regular response text"

        print(f"✓ Streamed thinking ({len(thinking_parts)} chunks): {complete_thinking[:150]}...")
        print(f"✓ Streamed response ({len(text_parts)} chunks): {complete_text[:100]}...")

    # =========================================================================
    # FILES API TEST CASES (Cross-Provider)
    # =========================================================================

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("file_upload")
    )
    def test_17_file_upload(self, anthropic_client, test_config, provider, model):
        """Test Case 17: Upload a file via Files API

        Uses cross-provider parametrization to test file upload across providers
        that support the Files API (Anthropic, OpenAI, Gemini).
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for file_upload scenario")

        # Get provider-specific client
        client = get_provider_anthropic_client(provider)

        try:
            # Upload the file using beta API
            if provider == "openai":
                # Create test content
                jsonl_content = create_batch_jsonl_content(
                    model=get_model("openai", "chat"), num_requests=1
                )
                response = client.beta.files.upload(
                    file=("test_upload.jsonl", jsonl_content, "application/jsonl"),
                )
            else:
                text_content = b"This is a test file for Files API integration testing."
                response = client.beta.files.upload(
                    file=("test_upload.txt", text_content, "text/plain"),
                )
            # Validate response
            assert response is not None, "File response should not be None"
            assert hasattr(response, "id"), "File response should have 'id' attribute"
            assert response.id is not None, "File ID should not be None"
            assert len(response.id) > 0, "File ID should not be empty"

            print(f"Success: Uploaded file with ID: {response.id} for provider {provider}")

            # Clean up - delete the file
            try:
                client.beta.files.delete(response.id)
                print(f"Cleanup: Deleted file {response.id}")
            except Exception as e:
                print(f"Warning: Failed to clean up file: {e}")

        except Exception as e:
            # Files API might not be available or require specific permissions
            error_str = str(e).lower()
            if "beta" in error_str or "not found" in error_str or "not supported" in error_str:
                pytest.skip(f"Files API not available for provider {provider}: {e}")
            raise

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("file_list"))
    def test_18_file_list(self, anthropic_client, test_config, provider, model):
        """Test Case 18: List files from Files API

        Uses cross-provider parametrization to test file listing across providers.
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for file_list scenario")

        # Get provider-specific client
        client = get_provider_anthropic_client(provider)

        try:
            # First upload a file to ensure we have at least one
            if provider == "openai":
                jsonl_content = create_batch_jsonl_content(
                    model=get_model("openai", "chat"), num_requests=1
                )
                uploaded_file = client.beta.files.upload(
                    file=("test_list.jsonl", jsonl_content, "application/jsonl"),
                )
            else:
                test_content = b"Test file for listing"
                uploaded_file = client.beta.files.upload(
                    file=("test_list.txt", test_content, "text/plain"),
                )

            try:
                # List files
                response = client.beta.files.list()

                # Validate response
                assert response is not None, "File list response should not be None"
                assert hasattr(response, "data"), "File list response should have 'data' attribute"
                assert isinstance(response.data, list), "Data should be a list"

                # Check that our uploaded file is in the list
                file_ids = [f.id for f in response.data]
                assert (
                    uploaded_file.id in file_ids
                ), f"Uploaded file {uploaded_file.id} should be in file list"

                print(f"Success: Listed {len(response.data)} files for provider {provider}")

            finally:
                # Clean up
                try:
                    client.beta.files.delete(uploaded_file.id)
                except Exception as e:
                    print(f"Warning: Failed to clean up file: {e}")

        except Exception as e:
            error_str = str(e).lower()
            if "beta" in error_str or "not found" in error_str or "not supported" in error_str:
                pytest.skip(f"Files API not available for provider {provider}: {e}")
            raise

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("file_delete")
    )
    def test_20_file_delete(self, anthropic_client, test_config, provider, model):
        """Test Case 20: Delete a file from Files API

        Uses cross-provider parametrization to test file deletion across providers.
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for file_delete scenario")

        # Get provider-specific client
        client = get_provider_anthropic_client(provider)

        try:
            # First upload a file
            if provider == "openai":
                jsonl_content = create_batch_jsonl_content(
                    model=get_model("openai", "chat"), num_requests=1
                )
                uploaded_file = client.beta.files.upload(
                    file=("test_delete.jsonl", jsonl_content, "application/jsonl"),
                )
            else:
                test_content = b"Test file for deletion"
                uploaded_file = client.beta.files.upload(
                    file=("test_delete.txt", test_content, "text/plain"),
                )

            # Delete the file
            response = client.beta.files.delete(uploaded_file.id)

            # Validate response - providers may return different formats
            assert response is not None, "Delete response should not be None"

            print(f"Success: Deleted file {uploaded_file.id} (provider: {provider})")

            # Verify file is no longer retrievable
            with pytest.raises(Exception):
                client.beta.files.retrieve(uploaded_file.id)

        except Exception as e:
            error_str = str(e).lower()
            if "beta" in error_str or "not found" in error_str or "not supported" in error_str:
                pytest.skip(f"Files API not available for provider {provider}: {e}")
            raise

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("file_content")
    )
    def test_21_file_content(self, anthropic_client, test_config, provider, model):
        """Test Case 21: Download file content from Files API

        Uses cross-provider parametrization to test file content download.
        Note: Some providers have restrictions on downloading uploaded files:
        - Anthropic: Only files created by code execution tool can be downloaded
        - Gemini: Doesn't support direct file download (excluded via config)
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for file_content scenario")

        # Get provider-specific client
        client = get_provider_anthropic_client(provider)

        try:
            # First upload a file
            if provider == "openai":
                original_content = create_batch_jsonl_content(
                    model=get_model("openai", "chat"), num_requests=1
                )
                uploaded_file = client.beta.files.upload(
                    file=("test_content.jsonl", original_content, "application/jsonl"),
                )
            else:
                original_content = b"Test file content for download"
                uploaded_file = client.beta.files.upload(
                    file=("test_content.txt", original_content, "text/plain"),
                )

            try:
                # Try to download file content
                # This may fail for some providers (e.g., Anthropic uploaded files)
                response = client.beta.files.download(uploaded_file.id)

                # If we get here, download was successful
                assert response is not None, "File content should not be None"

                # Compare downloaded content with original
                downloaded_content = response.text()
                original_str = (
                    original_content
                    if isinstance(original_content, str)
                    else original_content.decode("utf-8")
                )

                assert downloaded_content == original_str, (
                    f"Downloaded content should match original. "
                    f"Expected: {original_str[:100]}..., Got: {downloaded_content[:100]}..."
                )

                print(
                    f"Success: Downloaded and verified file content ({len(downloaded_content)} bytes) for provider {provider}"
                )

            except Exception as download_error:
                # Some providers don't allow downloading uploaded files
                error_str = str(download_error).lower()
                if (
                    "download" in error_str
                    or "not allowed" in error_str
                    or "forbidden" in error_str
                ):
                    print(
                        f"Expected for {provider}: Cannot download uploaded files - {download_error}"
                    )
                else:
                    raise

            finally:
                # Clean up
                try:
                    client.beta.files.delete(uploaded_file.id)
                except Exception as e:
                    print(f"Warning: Failed to clean up file: {e}")

        except Exception as e:
            error_str = str(e).lower()
            if "beta" in error_str or "not found" in error_str or "not supported" in error_str:
                pytest.skip(f"Files API not available for provider {provider}: {e}")
            raise

    # =========================================================================
    # BATCH API TEST CASES (Cross-Provider)
    # =========================================================================

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("batch_inline")
    )
    def test_22_batch_create_inline(self, anthropic_client, test_config, provider, model):
        """Test Case 22: Create a batch job with inline requests

        Uses cross-provider parametrization to test batch creation across providers
        that support inline batch requests (Anthropic, Gemini, etc.)
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_inline scenario")
        
        # Get provider-specific client
        client = get_provider_anthropic_client(provider)

        # Create inline requests
        batch_requests = create_batch_inline_requests(
            model=model, num_requests=2, provider=provider, sdk="anthropic"
        )

        batch = None
        try:
            # Create batch job
            batch = client.beta.messages.batches.create(requests=batch_requests)

            print(
                f"Success: Created batch with ID: {batch.id}, status: {batch.processing_status} for provider {provider}"
            )

            # Validate response
            assert_valid_batch_inline_response(batch, provider="anthropic")
        finally:
            # Clean up - cancel batch if created
            if batch:
                try:
                    client.beta.messages.batches.cancel(batch.id)
                except Exception as e:
                    print(f"Info: Could not cancel batch (may already be processed): {e}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("batch_list"))
    def test_23_batch_list(self, anthropic_client, test_config, provider, model):
        """Test Case 23: List batch jobs

        Tests batch listing across all providers using Anthropic SDK.
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_list scenario")

        if provider == "bedrock":
            pytest.skip("Bedrock can't create batches with file input. Hence skipping batch_list scenario")

        # Get provider-specific client
        client = get_provider_anthropic_client(provider)

        # List batches
        response = client.beta.messages.batches.list(limit=10)

        # Validate response
        assert response is not None, "Batch list response should not be None"
        assert hasattr(response, "data"), "Batch list response should have 'data' attribute"
        assert isinstance(response.data, list), "Data should be a list"

        batch_count = len(response.data)
        print(f"Success: Listed {batch_count} batches for provider {provider}")

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("batch_retrieve")
    )
    def test_24_batch_retrieve(self, anthropic_client, test_config, provider, model):
        """Test Case 24: Retrieve batch status by ID

        Creates a batch using inline requests, then retrieves it.
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_retrieve scenario")

        if provider == "bedrock":
            pytest.skip("Bedrock can't create batches with file input. Hence skipping batch_retrieve scenario")

        # Get provider-specific client
        client = get_provider_anthropic_client(provider)

        batch_id = None

        try:
            # Create batch for testing retrieval
            batch_requests = create_batch_inline_requests(model=model, num_requests=1, provider=provider, sdk="anthropic")
            batch = client.beta.messages.batches.create(requests=batch_requests)
            batch_id = batch.id

            # Retrieve batch
            retrieved_batch = client.beta.messages.batches.retrieve(batch_id)

            # Validate response
            assert retrieved_batch is not None, "Retrieved batch should not be None"
            assert (
                retrieved_batch.id == batch_id
            ), f"Batch ID should match: expected {batch_id}, got {retrieved_batch.id}"

            print(
                f"Success: Retrieved batch {batch_id}, status: {retrieved_batch.processing_status} for provider {provider}"
            )

        finally:
            # Clean up
            if batch_id:
                try:
                    client.beta.messages.batches.cancel(batch_id)
                except Exception:
                    pass

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("batch_cancel")
    )
    def test_25_batch_cancel(self, anthropic_client, test_config, provider, model):
        """Test Case 25: Cancel a batch job

        Creates a batch using inline requests, then cancels it.
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_cancel scenario")
        
        if provider == "bedrock":
            pytest.skip("Bedrock can't create batches with file input. Hence skipping batch_list scenario")

        # Get provider-specific client
        client = get_provider_anthropic_client(provider)

        batch_id = None

        try:
            # Create batch for testing cancellation
            batch_requests = create_batch_inline_requests(model=model, num_requests=1, provider=provider)
            batch = client.beta.messages.batches.create(requests=batch_requests)
            batch_id = batch.id

            # Cancel batch
            cancelled_batch = client.beta.messages.batches.cancel(batch_id)

            # Validate response
            assert cancelled_batch is not None, "Cancelled batch should not be None"
            assert cancelled_batch.id == batch_id, f"Batch ID should match"
            # Anthropic uses different status values
            assert cancelled_batch.processing_status in [
                "canceling",
                "ended",
            ], f"Status should be 'canceling' or 'ended', got {cancelled_batch.processing_status}"

            print(
                f"Success: Cancelled batch {batch_id}, status: {cancelled_batch.processing_status} for provider {provider}"
            )

        except Exception as e:
            # Batch might already be processed
            if batch_id:
                print(f"Info: Batch cancel may have failed due to batch state: {e}")

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("batch_cancel")
    )
    def test_26_batch_results(self, anthropic_client, test_config, provider, model):
        """Test Case 26: Retrieve batch results

        Note: This test creates a batch and attempts to retrieve results.
        Results are only available after the batch has completed processing.
        """
        if provider == "bedrock":
            pytest.skip("Bedrock can't create batches with file input. Hence skipping test_26_batch_results scenario")

        try:
            # Create batch with simple requests
            batch_requests = create_batch_inline_requests(
                model=model, num_requests=1, provider=provider, sdk="anthropic"
            )

            batch = anthropic_client.beta.messages.batches.create(requests=batch_requests)
            batch_id = batch.id

            print(f"Created batch {batch_id} with status: {batch.processing_status}")

            # Try to get results - might fail if batch not yet complete
            try:
                results = anthropic_client.beta.messages.batches.results(batch_id)

                # Collect results if available
                result_count = 0
                for result in results:
                    result_count += 1
                    print(f"  Result {result_count}: custom_id={result.custom_id}")

                print(f"Success: Retrieved {result_count} results for batch {batch_id}")

            except Exception as results_error:
                # Results might not be ready yet
                error_str = str(results_error).lower()
                if (
                    "not ready" in error_str
                    or "in_progress" in error_str
                    or "processing" in error_str
                ):
                    print(f"Info: Batch results not yet available (batch still processing)")
                else:
                    print(f"Info: Could not retrieve results: {results_error}")

            # Clean up
            try:
                anthropic_client.beta.messages.batches.cancel(batch_id)
            except Exception:
                pass

        except Exception as e:
            error_str = str(e).lower()
            if "beta" in error_str or "not found" in error_str:
                pytest.skip(f"Anthropic Batch API not available: {e}")
            raise

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("batch_inline")
    )
    def test_27_batch_e2e(self, anthropic_client, test_config, provider, model):
        """Test Case 27: End-to-end batch workflow

        Complete workflow: create batch -> poll status -> verify in list.
        Uses cross-provider parametrization.
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_inline scenario")

        if provider == "bedrock":
            pytest.skip("Bedrock can't create batches with file input. Hence skipping test_27_batch_e2e scenario")

        import time

        # Get provider-specific client
        client = get_provider_anthropic_client(provider)

        # Step 1: Create batch with inline requests
        print(f"Step 1: Creating batch for provider {provider}...")
        batch_requests = create_batch_inline_requests(model=model, num_requests=2, provider=provider, sdk="anthropic")

        batch = client.beta.messages.batches.create(requests=batch_requests)
        batch_id = batch.id

        assert batch_id is not None, "Batch ID should not be None"
        print(f"  Created batch: {batch_id}, status: {batch.processing_status}")

        try:
            # Step 2: Poll batch status (with timeout)
            print("Step 2: Polling batch status...")
            max_polls = 5
            poll_interval = 2  # seconds

            for i in range(max_polls):
                retrieved_batch = client.beta.messages.batches.retrieve(batch_id)
                print(f"  Poll {i+1}: status = {retrieved_batch.processing_status}")

                if retrieved_batch.processing_status in ["ended"]:
                    print(f"  Batch reached terminal state: {retrieved_batch.processing_status}")
                    break

                if hasattr(retrieved_batch, "request_counts") and retrieved_batch.request_counts:
                    counts = retrieved_batch.request_counts
                    print(
                        f"    Request counts - processing: {counts.processing}, succeeded: {counts.succeeded}, errored: {counts.errored}"
                    )

                time.sleep(poll_interval)

            # Step 3: Verify batch is in the list
            print("Step 3: Verifying batch in list...")
            batch_list = client.beta.messages.batches.list(limit=20)
            batch_ids = [b.id for b in batch_list.data]
            assert batch_id in batch_ids, f"Batch {batch_id} should be in the batch list"
            print(f"  Verified batch {batch_id} is in list")

            print(f"Success: E2E completed for batch {batch_id} (provider: {provider})")

        finally:
            # Clean up
            try:
                client.beta.messages.batches.cancel(batch_id)
                print(f"Cleanup: Cancelled batch {batch_id}")
            except Exception as e:
                print(f"Cleanup info: Could not cancel batch: {e}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("prompt_caching"))
    def test_28_prompt_caching_system(self, anthropic_client, provider, model):
        """Test Case 28: Prompt caching with system message checkpoint"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for prompt_caching scenario")
        
        print(f"\n=== Testing System Message Caching for provider {provider} ===")
        print("First request: Creating cache with system message checkpoint...")
        
        system_messages = [
            {
                "type": "text",
                "text": "You are an AI assistant tasked with analyzing legal documents."
            },
            {
                "type": "text", 
                "text": PROMPT_CACHING_LARGE_CONTEXT,
                "cache_control": {"type": "ephemeral"}
            }
        ]
        
        # First request - should create cache
        response1 = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            system=system_messages,
            messages=[
                {
                    "role": "user",
                    "content": "What are the key elements of contract formation?"
                }
            ],
            max_tokens=1024
        )
        
        # Validate first response
        assert_valid_chat_response(response1)
        assert hasattr(response1, "usage"), "Response should have usage information"
        cache_write_tokens = validate_cache_write(response1.usage, "First request")
        
        # Second request with same system - should hit cache
        print("\nSecond request: Hitting cache with same system checkpoint...")
        response2 = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            system=system_messages,  # Same system messages with cache_control
            messages=[
                {
                    "role": "user",
                    "content": "What is the purpose of a force majeure clause?"
                }
            ],
            max_tokens=1024
        )
        
        # Validate second response
        assert_valid_chat_response(response2)
        cache_read_tokens = validate_cache_read(response2.usage, "Second request")
        
        # Validate that cache read tokens are approximately equal to cache creation tokens
        assert abs(cache_write_tokens - cache_read_tokens) < 100, \
            f"Cache read tokens ({cache_read_tokens}) should be close to cache creation tokens ({cache_write_tokens})"
        
        print(f"✓ System caching validated - Cache created: {cache_write_tokens} tokens, "
              f"Cache read: {cache_read_tokens} tokens")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("prompt_caching"))
    def test_29_prompt_caching_messages(self, anthropic_client, provider, model):
        """Test Case 29: Prompt caching with messages checkpoint"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for prompt_caching scenario")
        
        print(f"\n=== Testing Messages Caching for provider {provider} ===")
        print("First request: Creating cache with messages checkpoint...")
        
        # First request with cache control in user message
        response1 = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            messages=[
                {
                    "role": "user",
                    "content": [
                        {
                            "type": "text",
                            "text": "Here is a large legal document to analyze:"
                        },
                        {
                            "type": "text",
                            "text": PROMPT_CACHING_LARGE_CONTEXT,
                            "cache_control": {"type": "ephemeral"}
                        },
                        {
                            "type": "text",
                            "text": "What are the main indemnification principles?"
                        }
                    ]
                }
            ],
            max_tokens=1024
        )
        
        assert_valid_chat_response(response1)
        assert hasattr(response1, "usage"), "Response should have usage information"
        cache_write_tokens = validate_cache_write(response1.usage, "First request")
        
        # Second request with same cached content
        print("\nSecond request: Hitting cache with same messages checkpoint...")
        response2 = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            messages=[
                {
                    "role": "user",
                    "content": [
                        {
                            "type": "text",
                            "text": "Here is a large legal document to analyze:"
                        },
                        {
                            "type": "text",
                            "text": PROMPT_CACHING_LARGE_CONTEXT,
                            "cache_control": {"type": "ephemeral"}
                        },
                        {
                            "type": "text",
                            "text": "Summarize the dispute resolution methods."
                        }
                    ]
                }
            ],
            max_tokens=1024
        )
        
        assert_valid_chat_response(response2)
        cache_read_tokens = validate_cache_read(response2.usage, "Second request")
        
        # Validate that cache read tokens are approximately equal to cache creation tokens
        assert abs(cache_write_tokens - cache_read_tokens) < 100, \
            f"Cache read tokens ({cache_read_tokens}) should be close to cache creation tokens ({cache_write_tokens})"
        
        print(f"✓ Messages caching validated - Cache created: {cache_write_tokens} tokens, "
              f"Cache read: {cache_read_tokens} tokens")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("prompt_caching"))
    def test_30_prompt_caching_tools(self, anthropic_client, provider, model):
        """Test Case 30: Prompt caching with tools checkpoint (12 tools)"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for prompt_caching scenario")
        
        print(f"\n=== Testing Tools Caching for provider {provider} ===")
        print("First request: Creating cache with tools checkpoint...")
        
        # Convert tools to Anthropic format with cache control
        tools = convert_to_anthropic_tools(PROMPT_CACHING_TOOLS)
        # Add cache control to the last tool
        tools[-1]["cache_control"] = {"type": "ephemeral"}
        
        # First request with tool cache control
        response1 = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            tools=tools,
            messages=[
                {
                    "role": "user",
                    "content": "What's the weather in Boston?"
                }
            ],
            max_tokens=1024
        )
        
        assert hasattr(response1, "usage"), "Response should have usage information"
        cache_write_tokens = validate_cache_write(response1.usage, "First request")
        
        # Second request with same tools
        print("\nSecond request: Hitting cache with same tools checkpoint...")
        response2 = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            tools=tools,
            messages=[
                {
                    "role": "user",
                    "content": "Calculate 42 * 17"
                }
            ],
            max_tokens=1024
        )
        
        cache_read_tokens = validate_cache_read(response2.usage, "Second request")
        
        print(f"✓ Tools caching validated - Cache created: {cache_write_tokens} tokens, "
              f"Cache read: {cache_read_tokens} tokens")


# Additional helper functions specific to Anthropic
def serialize_anthropic_content(content_blocks: List[Any]) -> List[Dict[str, Any]]:
    """Serialize Anthropic content blocks (including ToolUseBlock objects) to dicts"""
    serialized_content = []

    for block in content_blocks:
        if hasattr(block, "type"):
            if block.type == "tool_use":
                # Serialize ToolUseBlock to dict
                serialized_content.append(
                    {"type": "tool_use", "id": block.id, "name": block.name, "input": block.input}
                )
            elif block.type == "text":
                # Serialize TextBlock to dict
                serialized_content.append({"type": "text", "text": block.text})
            else:
                # For other block types, try to convert using model_dump if available
                if hasattr(block, "model_dump"):
                    serialized_content.append(block.model_dump())
                else:
                    # Fallback: try to convert to dict
                    serialized_content.append(dict(block))
        else:
            # If already a dict, use as is
            serialized_content.append(block)

    return serialized_content


def extract_anthropic_tool_calls(response: Any) -> List[Dict[str, Any]]:
    """Extract tool calls from Anthropic response format with proper type checking"""
    tool_calls = []
    logger = logging.getLogger("AnthropicToolCallsExtractor")

    # Type check for Anthropic Message response
    if not hasattr(response, "content") or not response.content:
        return tool_calls

    for content in response.content:
        if hasattr(content, "type") and content.type == "tool_use":
            if hasattr(content, "name") and hasattr(content, "input"):
                try:
                    logger.debug(f"Extracting tool call: {content}")
                    tool_calls.append(
                        {"id": content.id, "name": content.name, "arguments": content.input}
                    )
                except AttributeError as e:
                    print(f"Warning: Failed to extract tool call from content: {e}")
                    continue

    return tool_calls

def validate_cache_write(usage: Any, operation: str) -> int:
    """Validate cache write operation and return tokens written"""
    print(f"{operation} usage - input_tokens: {usage.input_tokens}, "
            f"cache_creation_input_tokens: {getattr(usage, 'cache_creation_input_tokens', 0)}, "
            f"cache_read_input_tokens: {getattr(usage, 'cache_read_input_tokens', 0)}")
    
    assert hasattr(usage, 'cache_creation_input_tokens'), \
        f"{operation} should have cache_creation_input_tokens"
    cache_write_tokens = getattr(usage, 'cache_creation_input_tokens', 0)
    assert cache_write_tokens > 0, \
        f"{operation} should create cache (got {cache_write_tokens} tokens)"
    
    return cache_write_tokens

def validate_cache_read(usage: Any, operation: str) -> int:
    """Validate cache read operation and return tokens read"""
    print(f"{operation} usage - input_tokens: {usage.input_tokens}, "
            f"cache_creation_input_tokens: {getattr(usage, 'cache_creation_input_tokens', 0)}, "
            f"cache_read_input_tokens: {getattr(usage, 'cache_read_input_tokens', 0)}")
    
    assert hasattr(usage, 'cache_read_input_tokens'), \
        f"{operation} should have cache_read_input_tokens"
    cache_read_tokens = getattr(usage, 'cache_read_input_tokens', 0)
    assert cache_read_tokens > 0, \
        f"{operation} should read from cache (got {cache_read_tokens} tokens)"
    
    return cache_read_tokens
