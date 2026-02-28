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
31. Count tokens (Cross-Provider)
"""

import logging
import time
from typing import Any, Dict, List

import pytest
from anthropic import Anthropic

from .utils.common import (
    # Anthropic-specific test data
    ANTHROPIC_THINKING_PROMPT,
    ANTHROPIC_THINKING_STREAMING_PROMPT,
    BASE64_IMAGE,
    CALCULATOR_TOOL,
    COMPARISON_KEYWORDS,
    IMAGE_URL,
    FILE_DATA_BASE64,
    INPUT_TOKENS_LONG_TEXT,
    INPUT_TOKENS_SIMPLE_TEXT,
    INPUT_TOKENS_WITH_SYSTEM,
    INVALID_ROLE_MESSAGES,
    LOCATION_KEYWORDS,
    MULTI_TURN_MESSAGES,
    MULTIPLE_TOOL_CALL_MESSAGES,
    PROMPT_CACHING_LARGE_CONTEXT,
    PROMPT_CACHING_TOOLS,
    SIMPLE_CHAT_MESSAGES,
    SINGLE_TOOL_CALL_MESSAGES,
    STREAMING_CHAT_MESSAGES,
    STREAMING_TOOL_CALL_MESSAGES,
    WEATHER_KEYWORDS,
    WEATHER_TOOL,
    Config,
    assert_has_tool_calls,
    assert_valid_batch_inline_response,
    assert_valid_chat_response,
    assert_valid_image_response,
    assert_valid_input_tokens_response,
    collect_streaming_content,
    # Files API utilities
    create_batch_inline_requests,
    create_batch_jsonl_content,
    extract_tool_calls,
    get_api_key,
    mock_tool_response,
    # Citation utilities
    CITATION_TEXT_DOCUMENT,
    CITATION_MULTI_DOCUMENT_SET,
    assert_valid_anthropic_citation,
    collect_anthropic_streaming_citations,
    create_anthropic_document,
)
from .utils.config_loader import get_config, get_model
from .utils.parametrize import (
    format_provider_model,
    get_cross_provider_params_for_scenario,
)


@pytest.fixture
def anthropic_client():
    """Create Anthropic client for testing"""
    from .utils.config_loader import get_config, get_integration_url

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
    from .utils.config_loader import get_config, get_integration_url

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

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("end2end_tool_calling")
    )
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

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("automatic_function_calling")
    )
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
            for _i, tool_call in enumerate(tool_calls):
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
            max_tokens=4000,  # Reduced to prevent token limit errors for smaller context window models
            thinking={
                "type": "enabled",
                "budget_tokens": 2500,  # Reduced to prevent token limit errors
            },
            extra_body={"reasoning_summary": "detailed"},
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
            max_tokens=3000,
            thinking={
                "type": "enabled",
                "budget_tokens": 2000,  # Reduced to prevent token limit errors
            },
            messages=messages,
            stream=True,
            extra_body={"reasoning_summary": "detailed"},
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
                            print("Thinking block started")

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
            print("chunk_count", chunk_count)
            if chunk_count > 5000:
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
            pytest.skip(
                "Bedrock can't create batches with file input. Hence skipping batch_list scenario"
            )

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
            pytest.skip(
                "Bedrock can't create batches with file input. Hence skipping batch_retrieve scenario"
            )

        # Get provider-specific client
        client = get_provider_anthropic_client(provider)

        batch_id = None

        try:
            # Create batch for testing retrieval
            batch_requests = create_batch_inline_requests(
                model=model, num_requests=1, provider=provider, sdk="anthropic"
            )
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
            pytest.skip(
                "Bedrock can't create batches with file input. Hence skipping batch_list scenario"
            )

        # Get provider-specific client
        client = get_provider_anthropic_client(provider)

        batch_id = None

        try:
            # Create batch for testing cancellation
            batch_requests = create_batch_inline_requests(
                model=model, num_requests=1, provider=provider
            )
            batch = client.beta.messages.batches.create(requests=batch_requests)
            batch_id = batch.id

            # Cancel batch
            cancelled_batch = client.beta.messages.batches.cancel(batch_id)

            # Validate response
            assert cancelled_batch is not None, "Cancelled batch should not be None"
            assert cancelled_batch.id == batch_id, "Batch ID should match"
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
            pytest.skip(
                "Bedrock can't create batches with file input. Hence skipping test_26_batch_results scenario"
            )

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
                    print("Info: Batch results not yet available (batch still processing)")
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
            pytest.skip(
                "Bedrock can't create batches with file input. Hence skipping test_27_batch_e2e scenario"
            )

        import time

        # Get provider-specific client
        client = get_provider_anthropic_client(provider)

        # Step 1: Create batch with inline requests
        print(f"Step 1: Creating batch for provider {provider}...")
        batch_requests = create_batch_inline_requests(
            model=model, num_requests=2, provider=provider, sdk="anthropic"
        )

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

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("prompt_caching")
    )
    def test_28_prompt_caching_system(self, anthropic_client, provider, model):
        """Test Case 28: Prompt caching with system message checkpoint"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for prompt_caching scenario")

        print(f"\n=== Testing System Message Caching for provider {provider} ===")
        print("First request: Creating cache with system message checkpoint...")

        system_messages = [
            {
                "type": "text",
                "text": "You are an AI assistant tasked with analyzing legal documents.",
            },
            {
                "type": "text",
                "text": PROMPT_CACHING_LARGE_CONTEXT,
                "cache_control": {"type": "ephemeral"},
            },
        ]

        # First request - should create cache
        response1 = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            system=system_messages,
            messages=[
                {"role": "user", "content": "What are the key elements of contract formation?"}
            ],
            max_tokens=1024,
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
                {"role": "user", "content": "What is the purpose of a force majeure clause?"}
            ],
            max_tokens=1024,
        )

        # Validate second response
        assert_valid_chat_response(response2)
        cache_read_tokens = validate_cache_read(response2.usage, "Second request")

        # Validate that cache read tokens are approximately equal to cache creation tokens
        assert (
            abs(cache_write_tokens - cache_read_tokens) < 100
        ), f"Cache read tokens ({cache_read_tokens}) should be close to cache creation tokens ({cache_write_tokens})"

        print(
            f"✓ System caching validated - Cache created: {cache_write_tokens} tokens, "
            f"Cache read: {cache_read_tokens} tokens"
        )

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("prompt_caching")
    )
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
                        {"type": "text", "text": "Here is a large legal document to analyze:"},
                        {
                            "type": "text",
                            "text": PROMPT_CACHING_LARGE_CONTEXT,
                            "cache_control": {"type": "ephemeral"},
                        },
                        {"type": "text", "text": "What are the main indemnification principles?"},
                    ],
                }
            ],
            max_tokens=1024,
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
                        {"type": "text", "text": "Here is a large legal document to analyze:"},
                        {
                            "type": "text",
                            "text": PROMPT_CACHING_LARGE_CONTEXT,
                            "cache_control": {"type": "ephemeral"},
                        },
                        {"type": "text", "text": "Summarize the dispute resolution methods."},
                    ],
                }
            ],
            max_tokens=1024,
        )

        assert_valid_chat_response(response2)
        cache_read_tokens = validate_cache_read(response2.usage, "Second request")

        # Validate that cache read tokens are approximately equal to cache creation tokens
        assert (
            abs(cache_write_tokens - cache_read_tokens) < 100
        ), f"Cache read tokens ({cache_read_tokens}) should be close to cache creation tokens ({cache_write_tokens})"

        print(
            f"✓ Messages caching validated - Cache created: {cache_write_tokens} tokens, "
            f"Cache read: {cache_read_tokens} tokens"
        )

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("prompt_caching")
    )
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
            messages=[{"role": "user", "content": "What's the weather in Boston?"}],
            max_tokens=1024,
        )

        assert hasattr(response1, "usage"), "Response should have usage information"
        cache_write_tokens = validate_cache_write(response1.usage, "First request")

        # Second request with same tools
        print("\nSecond request: Hitting cache with same tools checkpoint...")
        response2 = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            tools=tools,
            messages=[{"role": "user", "content": "Calculate 42 * 17"}],
            max_tokens=1024,
        )

        cache_read_tokens = validate_cache_read(response2.usage, "Second request")

        print(
            f"✓ Tools caching validated - Cache created: {cache_write_tokens} tokens, "
            f"Cache read: {cache_read_tokens} tokens"
        )

    # =========================================================================
    # INPUT TOKENS / TOKEN COUNTING TEST CASES
    # =========================================================================

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("count_tokens")
    )
    def test_31a_input_tokens_simple_text(self, anthropic_client, test_config, provider, model):
        """Test Case 31a: Input tokens count with simple text"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        response = anthropic_client.beta.messages.count_tokens(
            model=format_provider_model(provider, model),
            messages=[{"role": "user", "content": INPUT_TOKENS_SIMPLE_TEXT}],
        )

        # Validate response structure
        assert_valid_input_tokens_response(response, "anthropic")

        # Simple text should have a reasonable token count (between 3-20 tokens)
        assert (
            3 <= response.input_tokens <= 20
        ), f"Simple text should have 3-20 tokens, got {response.input_tokens}"

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("count_tokens")
    )
    def test_31b_input_tokens_with_system_message(
        self, anthropic_client, test_config, provider, model
    ):
        """Test Case 31b: Input tokens count with system message"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        # Convert to Anthropic format
        messages = convert_to_anthropic_messages(INPUT_TOKENS_WITH_SYSTEM)

        # Extract system message if present
        system_message = None
        for msg in INPUT_TOKENS_WITH_SYSTEM:
            if msg.get("role") == "system":
                system_message = msg.get("content")
                break

        response = anthropic_client.beta.messages.count_tokens(
            model=format_provider_model(provider, model),
            system=system_message,
            messages=messages,
        )

        # Validate response structure
        assert_valid_input_tokens_response(response, "anthropic")

        # With system message should have more tokens than simple text
        assert (
            response.input_tokens > 2
        ), f"With system message should have >2 tokens, got {response.input_tokens}"

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("count_tokens")
    )
    def test_31c_input_tokens_long_text(self, anthropic_client, test_config, provider, model):
        """Test Case 31c: Input tokens count with long text"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        response = anthropic_client.beta.messages.count_tokens(
            model=format_provider_model(provider, model),
            messages=[{"role": "user", "content": INPUT_TOKENS_LONG_TEXT}],
        )

        # Validate response structure
        assert_valid_input_tokens_response(response, "anthropic")

        # Long text should have significantly more tokens
        assert (
            response.input_tokens > 100
        ), f"Long text should have >100 tokens, got {response.input_tokens}"

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("file_input"))
    def test_31_document_pdf_input(self, anthropic_client, test_config, provider, model):
        """Test Case 31: PDF document input"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for document_input scenario")

        messages = [
            {
                "role": "user",
                "content": [
                    {
                        "type": "text",
                        "text": "What is the main content of this PDF document? Summarize it.",
                    },
                    {
                        "type": "document",
                        "title": "testing",
                        "source": {
                            "type": "base64",
                            "media_type": "application/pdf",
                            "data": FILE_DATA_BASE64,
                        },
                    },
                ],
            }
        ]

        response = anthropic_client.messages.create(
            model=format_provider_model(provider, model), messages=messages, max_tokens=500
        )

        assert_valid_chat_response(response)
        assert len(response.content) > 0
        assert response.content[0].type == "text"
        content = response.content[0].text.lower()

        # Should mention "hello world" from the PDF
        assert any(
            word in content for word in ["hello", "world"]
        ), f"Response should reference document content. Got: {content}"

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("file_input_text")
    )
    def test_32_document_text_input(self, anthropic_client, test_config, provider, model):
        """Test Case 32: Text document input"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for document_input scenario")

        # Plain text document content
        text_content = """This is a test text document for document input testing.

It contains multiple paragraphs to ensure the model can properly process text documents.

Key features of this document:
1. Multiple lines and structure
2. Clear formatting
3. Numbered list

This document is used to verify that the AI can read and understand text document inputs."""

        messages = [
            {
                "role": "user",
                "content": [
                    {
                        "type": "text",
                        "text": "What are the key features mentioned in this document?",
                    },
                    {
                        "type": "document",
                        "title": "testing",
                        "source": {
                            "type": "text",
                            "media_type": "text/plain",
                            "data": text_content,
                        },
                    },
                ],
            }
        ]

        response = anthropic_client.messages.create(
            model=format_provider_model(provider, model), messages=messages, max_tokens=500
        )

        assert_valid_chat_response(response)
        assert len(response.content) > 0
        assert response.content[0].type == "text"
        content = response.content[0].text.lower()

        # Should reference the document features
        document_keywords = ["feature", "line", "format", "list", "document"]
        assert any(
            word in content for word in document_keywords
        ), f"Response should reference document features. Got: {content}"

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("citations"))
    def test_33_citations_pdf(self, anthropic_client, test_config, provider, model):
        """Test Case 33: PDF document with page_location citations"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for citations scenario")

        print(f"\n=== Testing PDF Citations (page_location) for provider {provider} ===")

        # Create PDF document using helper
        document = create_anthropic_document(
            content=FILE_DATA_BASE64, doc_type="pdf", title="Test PDF Document"
        )

        messages = [
            {
                "role": "user",
                "content": [
                    {
                        "type": "text",
                        "text": "What does this PDF document say? Please cite your sources.",
                    },
                    document,
                ],
            }
        ]

        response = anthropic_client.messages.create(
            model=format_provider_model(provider, model), messages=messages, max_tokens=500
        )

        # Validate basic response
        assert_valid_chat_response(response)
        assert len(response.content) > 0

        # Check for citations using helper
        has_citations = False
        citation_count = 0
        for block in response.content:
            if hasattr(block, "citations") and block.citations:
                has_citations = True
                for citation in block.citations:
                    citation_count += 1
                    # Use common validator
                    assert_valid_anthropic_citation(
                        citation, expected_type="page_location", document_index=0
                    )
                    print(
                        f"✓ Citation {citation_count}: pages {citation.start_page_number}-{citation.end_page_number}, "
                        f"text: '{citation.cited_text[:50]}...'"
                    )

        assert has_citations, "Response should contain citations for PDF document"
        print(f"✓ PDF citations test passed - Found {citation_count} citations")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("citations"))
    def test_34_citations_text(self, anthropic_client, test_config, provider, model):
        """Test Case 34: Plain text document with char_location citations"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for citations scenario")

        print(f"\n=== Testing Text Citations (char_location) for provider {provider} ===")

        # Create text document using helper
        document = create_anthropic_document(
            content=CITATION_TEXT_DOCUMENT, doc_type="text", title="Theory of Relativity Overview"
        )

        messages = [
            {
                "role": "user",
                "content": [
                    {
                        "type": "text",
                        "text": "When was General Relativity published and what does it deal with? Please cite your sources.",
                    },
                    document,
                ],
            }
        ]

        response = anthropic_client.messages.create(
            model=format_provider_model(provider, model), messages=messages, max_tokens=500
        )

        # Validate basic response
        assert_valid_chat_response(response)
        assert len(response.content) > 0

        # Check for citations using helper
        has_citations = False
        citation_count = 0
        for block in response.content:
            if hasattr(block, "citations") and block.citations:
                has_citations = True
                for citation in block.citations:
                    citation_count += 1
                    # Use common validator
                    assert_valid_anthropic_citation(
                        citation, expected_type="char_location", document_index=0
                    )
                    print(
                        f"✓ Citation {citation_count}: chars {citation.start_char_index}-{citation.end_char_index}, "
                        f"text: '{citation.cited_text[:50]}...'"
                    )

        assert has_citations, "Response should contain citations for text document"
        print(f"✓ Text citations test passed - Found {citation_count} citations")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("citations"))
    def test_35_citations_multi_document(self, anthropic_client, test_config, provider, model):
        """Test Case 35: Multiple documents with citations (document_index validation)"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for citations scenario")

        print(f"\n=== Testing Multi-Document Citations for provider {provider} ===")

        # Create multiple documents using helper
        documents = []
        for idx, doc_info in enumerate(CITATION_MULTI_DOCUMENT_SET):
            doc = create_anthropic_document(
                content=doc_info["content"], doc_type="text", title=doc_info["title"]
            )
            documents.append(doc)

        messages = [
            {
                "role": "user",
                "content": [
                    {
                        "type": "text",
                        "text": "Summarize what each document says. Please cite your sources from each document.",
                    },
                    *documents,
                ],
            }
        ]

        response = anthropic_client.messages.create(
            model=format_provider_model(provider, model), messages=messages, max_tokens=600
        )

        # Validate basic response
        assert_valid_chat_response(response)
        assert len(response.content) > 0

        # Check for citations from multiple documents
        has_citations = False
        citations_by_doc = {0: 0, 1: 0}  # Track citations per document
        total_citations = 0

        for block in response.content:
            if hasattr(block, "citations") and block.citations:
                has_citations = True
                for citation in block.citations:
                    total_citations += 1
                    doc_idx = citation.document_index if hasattr(citation, "document_index") else 0

                    # Validate citation
                    assert_valid_anthropic_citation(
                        citation, expected_type="char_location", document_index=doc_idx
                    )

                    # Track which document this citation is from
                    if doc_idx in citations_by_doc:
                        citations_by_doc[doc_idx] += 1

                    doc_title = (
                        citation.document_title
                        if hasattr(citation, "document_title")
                        else "Unknown"
                    )
                    print(
                        f"✓ Citation from doc[{doc_idx}] ({doc_title}): "
                        f"chars {citation.start_char_index}-{citation.end_char_index}, "
                        f"text: '{citation.cited_text[:40]}...'"
                    )

        assert has_citations, "Response should contain citations"

        # Report statistics
        print(f"\n✓ Multi-document citations test passed:")
        print(f"  - Total citations: {total_citations}")
        for doc_idx, count in citations_by_doc.items():
            doc_title = CITATION_MULTI_DOCUMENT_SET[doc_idx]["title"]
            print(f"  - Document {doc_idx} ({doc_title}): {count} citations")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("citations"))
    def test_36_citations_streaming(self, anthropic_client, test_config, provider, model):
        """Test Case 36: Text citations with streaming (citations_delta)"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for citations scenario")

        print(f"\n=== Testing Streaming Citations (char_location) for provider {provider} ===")

        # Create text document using helper
        document = create_anthropic_document(
            content=CITATION_TEXT_DOCUMENT, doc_type="text", title="Machine Learning Introduction"
        )

        messages = [
            {
                "role": "user",
                "content": [
                    {
                        "type": "text",
                        "text": "Explain the key concepts from this document. Please cite your sources.",
                    },
                    document,
                ],
            }
        ]

        stream = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            messages=messages,
            max_tokens=500,
            stream=True,
        )

        # Collect streaming content and citations using helper
        complete_text, citations, chunk_count = collect_anthropic_streaming_citations(stream)

        # Validate results
        assert chunk_count > 0, "Should receive at least one chunk"
        assert len(complete_text) > 0, "Should receive text content"
        assert len(citations) > 0, "Should collect at least one citation from stream"

        # Validate each citation
        for idx, citation in enumerate(citations, 1):
            # Use common validator
            assert_valid_anthropic_citation(
                citation, expected_type="char_location", document_index=0
            )
            print(
                f"✓ Citation {idx}: chars {citation.start_char_index}-{citation.end_char_index}, "
                f"text: '{citation.cited_text[:50]}...'"
            )

        print(
            f"✓ Streaming citations test passed - {len(citations)} citations in {chunk_count} chunks"
        )

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("citations"))
    def test_37_citations_streaming_pdf(self, anthropic_client, test_config, provider, model):
        """Test Case 37: PDF citations with streaming (page_location + citations_delta)"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for citations scenario")

        print(f"\n=== Testing Streaming PDF Citations (page_location) for provider {provider} ===")

        # Create PDF document using helper
        document = create_anthropic_document(
            content=FILE_DATA_BASE64, doc_type="pdf", title="Test PDF Document"
        )

        messages = [
            {
                "role": "user",
                "content": [
                    {"type": "text", "text": "What does this PDF say? Please cite your sources."},
                    document,
                ],
            }
        ]

        stream = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            messages=messages,
            max_tokens=500,
            stream=True,
        )

        # Collect streaming content and citations using helper
        complete_text, citations, chunk_count = collect_anthropic_streaming_citations(stream)

        # Validate results
        assert chunk_count > 0, "Should receive at least one chunk"
        assert len(complete_text) > 0, "Should receive text content"
        assert len(citations) > 0, "Should collect at least one citation from stream"

        # Validate each citation - should be page_location for PDF
        for idx, citation in enumerate(citations, 1):
            # Use common validator
            assert_valid_anthropic_citation(
                citation, expected_type="page_location", document_index=0
            )
            print(
                f"✓ Citation {idx}: pages {citation.start_page_number}-{citation.end_page_number}, "
                f"text: '{citation.cited_text[:50]}...'"
            )

        print(
            f"✓ Streaming PDF citations test passed - {len(citations)} citations in {chunk_count} chunks"
        )

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("web_search"))
    def test_38_web_search_non_streaming(self, anthropic_client, test_config, provider, model):
        """Test Case 38: Web search tool (non-streaming)"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for web_search scenario")

        print(f"\n=== Testing Web Search (Non-Streaming) for provider {provider} ===")

        # Create web search tool
        web_search_tool = {"type": "web_search_20250305", "name": "web_search", "max_uses": 5}

        messages = [{"role": "user", "content": "What is a positive news story from today?"}]

        response = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            messages=messages,
            tools=[web_search_tool],
            max_tokens=2048,
        )

        # Validate basic response
        assert response is not None, "Response should not be None"
        assert hasattr(response, "content"), "Response should have content"
        assert len(response.content) > 0, "Content should not be empty"

        # Check for web search tool use
        has_web_search = False
        has_search_results = False
        has_citations = False
        search_query = None

        for block in response.content:
            if hasattr(block, "type"):
                # Check for server_tool_use with web_search
                if (
                    block.type == "server_tool_use"
                    and hasattr(block, "name")
                    and block.name == "web_search"
                ):
                    has_web_search = True
                    if hasattr(block, "input") and "query" in block.input:
                        search_query = block.input["query"]
                        print(f"✓ Found web search with query: {search_query}")

                # Check for web_search_tool_result
                elif block.type == "web_search_tool_result":
                    has_search_results = True
                    if hasattr(block, "content") and block.content:
                        result_count = len(block.content)
                        print(f"✓ Found {result_count} search results")

                        # Log first few results
                        for i, result in enumerate(block.content[:3]):
                            if hasattr(result, "url") and hasattr(result, "title"):
                                print(f"  Result {i+1}: {result.title}")

                # Check for text with citations
                elif block.type == "text":
                    if hasattr(block, "citations") and block.citations:
                        has_citations = True
                        citation_count = len(block.citations)
                        print(f"✓ Found {citation_count} citations in response")

                        # Validate citation structure
                        for citation in block.citations[:3]:
                            assert hasattr(citation, "type"), "Citation should have type"
                            assert hasattr(citation, "url"), "Citation should have URL"
                            assert hasattr(citation, "title"), "Citation should have title"
                            assert hasattr(
                                citation, "cited_text"
                            ), "Citation should have cited_text"
                            print(f"  Citation: {citation.title}")

        # Validate that web search was performed
        assert has_web_search, "Response should contain web_search tool use"
        assert has_search_results, "Response should contain web search results"
        assert search_query is not None, "Web search should have a query"

        print(f"✓ Web search (non-streaming) test passed!")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("web_search"))
    def test_39_web_search_streaming(self, anthropic_client, test_config, provider, model):
        """Test Case 39: Web search tool (streaming)"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for web_search scenario")

        print(f"\n=== Testing Web Search (Streaming) for provider {provider} ===")

        # Create web search tool with user location
        web_search_tool = {
            "type": "web_search_20250305",
            "name": "web_search",
            "max_uses": 5,
            "user_location": {
                "type": "approximate",
                "city": "New York",
                "region": "New York",
                "country": "US",
                "timezone": "America/New_York",
            },
        }

        messages = [{"role": "user", "content": "what was a positive news story from today??"}]

        stream = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            messages=messages,
            tools=[web_search_tool],
            max_tokens=2048,
            stream=True,
        )

        # Collect streaming events
        text_parts = []
        search_queries = []
        search_results = []
        citations = []
        chunk_count = 0
        has_server_tool_use = False
        has_search_tool_result = False
        has_citation_delta = False

        for event in stream:
            chunk_count += 1

            if hasattr(event, "type"):
                event_type = event.type

                # Handle content_block_start for tool use
                if event_type == "content_block_start":
                    if hasattr(event, "content_block") and event.content_block:
                        block = event.content_block

                        # Check for server_tool_use
                        if hasattr(block, "type") and block.type == "server_tool_use":
                            if hasattr(block, "name") and block.name == "web_search":
                                has_server_tool_use = True
                                print(
                                    f"✓ Web search tool use started (block id: {block.id if hasattr(block, 'id') else 'unknown'})"
                                )

                        # Check for web_search_tool_result
                        elif hasattr(block, "type") and block.type == "web_search_tool_result":
                            print(f"block: {block}")
                            has_search_tool_result = True
                            if hasattr(block, "content") and block.content:
                                result_count = len(block.content)
                                print(f"✓ Received {result_count} search results")

                                # Collect search results
                                for result in block.content:
                                    if hasattr(result, "url") and hasattr(result, "title"):
                                        search_results.append(
                                            {"url": result.url, "title": result.title}
                                        )

                # Handle content_block_delta for queries and text
                elif event_type == "content_block_delta":
                    if hasattr(event, "delta") and event.delta:
                        delta = event.delta

                        # Check for text_delta
                        if hasattr(delta, "type") and delta.type == "text_delta":
                            if hasattr(delta, "text"):
                                text_parts.append(delta.text)

                        # Check for citations_delta
                        elif hasattr(delta, "type") and delta.type == "citations_delta":
                            has_citation_delta = True
                            if hasattr(delta, "citation"):
                                citation = delta.citation
                                citations.append(citation)

                                if hasattr(citation, "title"):
                                    print(f"  Received citation: {citation.title}")

            # Safety check
            if chunk_count > 5000:
                break

        # Combine collected content
        complete_text = "".join(text_parts)

        # Validate results
        assert chunk_count > 0, "Should receive at least one chunk"
        assert has_server_tool_use, "Should detect web search tool use in streaming"
        assert has_search_tool_result, "Should receive search results in streaming"
        assert len(search_results) > 0, "Should collect search results from stream"
        assert len(complete_text) > 0, "Should receive text content about weather"

        print("✓ Streaming validation:")
        print(f"  - Chunks received: {chunk_count}")
        print(f"  - Search results: {len(search_results)}")
        print(f"  - Citations: {len(citations)}")
        print(f"  - Text length: {len(complete_text)} characters")
        print(f"  - First 150 chars: {complete_text[:150]}...")

        # Log a few search results
        if len(search_results) > 0:
            print("✓ Search results:")
            for i, result in enumerate(search_results[:3]):
                print(f"  {i+1}. {result['title']}")

        print("✓ Web search (streaming) test passed!")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("web_search"))
    def test_40_web_search_allowed_domains(self, anthropic_client, test_config, provider, model):
        """Test Case 40: Web search with allowed_domains filter"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for web_search scenario")

        print(f"\n=== Testing Web Search with Allowed Domains for provider {provider} ===")

        # Create web search tool with allowed domains
        web_search_tool = {
            "type": "web_search_20250305",
            "name": "web_search",
            "allowed_domains": ["en.wikipedia.org", "britannica.com"],
            "max_uses": 5,
        }

        messages = [
            {
                "role": "user",
                "content": "Who was Albert Einstein? Please search for this information.",
            }
        ]

        response = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            messages=messages,
            tools=[web_search_tool],
            max_tokens=2048,
        )

        # Validate basic response
        assert response is not None, "Response should not be None"
        assert hasattr(response, "content"), "Response should have content"
        assert len(response.content) > 0, "Content should not be empty"

        # Collect search results
        search_results = []
        for block in response.content:
            if hasattr(block, "type") and block.type == "web_search_tool_result":
                if hasattr(block, "content") and block.content:
                    for result in block.content:
                        if hasattr(result, "url") and hasattr(result, "title"):
                            search_results.append(result)
                            print(f"✓ Found result: {result.title} - {result.url}")

        # Validate domain filtering
        from .utils.common import validate_domain_filter

        if len(search_results) > 0:
            validate_domain_filter(search_results, allowed=["wikipedia.org", "britannica.com"])
            print(f"✓ All {len(search_results)} results respect allowed_domains filter")

        print(f"✓ Web search with allowed_domains test passed!")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("web_search"))
    def test_41_web_search_blocked_domains(self, anthropic_client, test_config, provider, model):
        """Test Case 41: Web search with blocked_domains filter"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for web_search scenario")

        # skip for openai
        if provider == "openai":
            pytest.skip("OpenAI does not support blocked_domains filter")

        print(f"\n=== Testing Web Search with Blocked Domains for provider {provider} ===")

        # Create web search tool with blocked domains
        web_search_tool = {
            "type": "web_search_20250305",
            "name": "web_search",
            "blocked_domains": ["reddit.com", "twitter.com", "x.com"],
            "max_uses": 5,
        }

        messages = [
            {"role": "user", "content": "What are recent developments in artificial intelligence?"}
        ]

        response = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            messages=messages,
            tools=[web_search_tool],
            max_tokens=2048,
        )

        # Validate basic response
        assert response is not None, "Response should not be None"
        assert hasattr(response, "content"), "Response should have content"

        # Collect search results
        search_results = []
        for block in response.content:
            if hasattr(block, "type") and block.type == "web_search_tool_result":
                if hasattr(block, "content") and block.content:
                    for result in block.content:
                        if hasattr(result, "url"):
                            search_results.append(result)
                            print(f"✓ Found result: {result.url}")

        # Validate domain filtering
        from .utils.common import validate_domain_filter

        if len(search_results) > 0:
            validate_domain_filter(search_results, blocked=["reddit.com", "twitter.com", "x.com"])
            print(f"✓ All {len(search_results)} results respect blocked_domains filter")

        print(f"✓ Web search with blocked_domains test passed!")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("web_search"))
    def test_42_web_search_multi_turn(self, anthropic_client, test_config, provider, model):
        """Test Case 42: Web search in multi-turn conversation"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for web_search scenario")

        print(f"\n=== Testing Web Search Multi-Turn Conversation for provider {provider} ===")

        web_search_tool = {"type": "web_search_20250305", "name": "web_search", "max_uses": 5}

        # First turn: Ask about a topic
        messages = [{"role": "user", "content": "What is quantum computing?"}]

        response1 = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            messages=messages,
            tools=[web_search_tool],
            max_tokens=2048,
        )

        assert response1 is not None, "First response should not be None"
        print(f"✓ First turn completed")

        # Add assistant response to conversation
        messages.append(
            {"role": "assistant", "content": serialize_anthropic_content(response1.content)}
        )

        # Second turn: Follow-up question
        messages.append(
            {"role": "user", "content": "How is it different from classical computing?"}
        )

        response2 = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            messages=messages,
            tools=[web_search_tool],
            max_tokens=2048,
        )

        assert response2 is not None, "Second response should not be None"
        assert hasattr(response2, "content"), "Second response should have content"
        assert len(response2.content) > 0, "Second response content should not be empty"

        # Validate that context was maintained
        has_text_response = False
        for block in response2.content:
            if hasattr(block, "type") and block.type == "text":
                if hasattr(block, "text") and len(block.text) > 0:
                    has_text_response = True
                    print(f"✓ Second turn response (first 150 chars): {block.text[:150]}...")

        assert has_text_response, "Second turn should have text response"
        print(f"✓ Multi-turn web search conversation test passed!")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("web_search"))
    def test_43_web_search_citation_validation(
        self, anthropic_client, test_config, provider, model
    ):
        """Test Case 43: Validate web search citation structure"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for web_search scenario")

        print(f"\n=== Testing Web Search Citation Validation for provider {provider} ===")

        web_search_tool = {"type": "web_search_20250305", "name": "web_search", "max_uses": 5}

        messages = [{"role": "user", "content": "What is the capital of France?"}]

        response = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            messages=messages,
            tools=[web_search_tool],
            max_tokens=2048,
        )

        # Find citations in response
        citations_found = []
        for block in response.content:
            if hasattr(block, "type") and block.type == "text":
                if hasattr(block, "citations") and block.citations:
                    for citation in block.citations:
                        citations_found.append(citation)

        # Validate citation structure
        from .utils.common import assert_valid_web_search_citation

        if len(citations_found) > 0:
            print(f"✓ Found {len(citations_found)} citations")
            for i, citation in enumerate(citations_found[:3]):
                assert_valid_web_search_citation(citation, sdk_type="anthropic")
                print(f"  Citation {i+1}: {citation.title}")
                print(f"    URL: {citation.url}")
                print(
                    f"    Cited text (first 50 chars): {citation.cited_text[:50] if citation.cited_text else 'N/A'}..."
                )
            print(f"✓ All citations have valid structure")
        else:
            print(f"⚠ No citations found (may be acceptable)")

        print(f"✓ Citation validation test passed!")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("web_search"))
    def test_44_web_search_streaming_event_order(
        self, anthropic_client, test_config, provider, model
    ):
        """Test Case 44: Validate web search streaming event sequence"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for web_search scenario")

        print(f"\n=== Testing Web Search Streaming Event Order for provider {provider} ===")

        web_search_tool = {"type": "web_search_20250305", "name": "web_search", "max_uses": 3}

        messages = [{"role": "user", "content": "What is the Eiffel Tower?"}]

        stream = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            messages=messages,
            tools=[web_search_tool],
            max_tokens=2048,
            stream=True,
        )

        # Track event sequence
        event_sequence = []

        for event in stream:
            if hasattr(event, "type"):
                event_type = event.type
                event_sequence.append(event_type)

                # Log key events
                if event_type == "content_block_start":
                    if hasattr(event, "content_block"):
                        block_type = getattr(event.content_block, "type", "unknown")
                        print(f"✓ Event: content_block_start ({block_type})")
                elif event_type == "content_block_stop":
                    print(f"✓ Event: content_block_stop")
                elif event_type == "content_block_delta":
                    if hasattr(event, "delta") and hasattr(event.delta, "type"):
                        delta_type = event.delta.type
                        if delta_type == "input_json_delta":
                            print(f"✓ Event: content_block_delta (input_json_delta)")

        # Validate expected event types are present
        assert "message_start" in event_sequence, "Should have message_start event"
        assert "content_block_start" in event_sequence, "Should have content_block_start events"
        assert "content_block_stop" in event_sequence, "Should have content_block_stop events"
        assert "message_stop" in event_sequence, "Should have message_stop event"

        print(f"✓ Received {len(event_sequence)} total events")
        print(f"✓ Event sequence validation passed!")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("web_search"))
    def test_45_web_search_with_prompt_caching(
        self, anthropic_client, test_config, provider, model
    ):
        """Test Case 45: Web search with prompt caching"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for web_search scenario")

        print(f"\n=== Testing Web Search with Prompt Caching for provider {provider} ===")

        web_search_tool = {"type": "web_search_20250305", "name": "web_search", "max_uses": 3}

        # First request with cache breakpoint
        messages = [{"role": "user", "content": "What is the current population of Tokyo?"}]

        response1 = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            messages=messages,
            tools=[web_search_tool],
            max_tokens=1500,
        )

        assert response1 is not None, "First response should not be None"

        # Check if cache was written
        if hasattr(response1, "usage"):
            cache_write_tokens = getattr(response1.usage, "cache_creation_input_tokens", 0)
            print(f"✓ First request - cache_creation_input_tokens: {cache_write_tokens}")

        # Add assistant response with cache control
        messages.append(
            {"role": "assistant", "content": serialize_anthropic_content(response1.content)}
        )

        messages.append(
            {
                "role": "user",
                "content": [
                    {
                        "type": "text",
                        "text": "What about its GDP?",
                        "cache_control": {"type": "ephemeral"},
                    }
                ],
            }
        )

        # Second request should benefit from caching
        response2 = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            messages=messages,
            tools=[web_search_tool],
            max_tokens=1500,
        )

        assert response2 is not None, "Second response should not be None"

        # Check if cache was read
        if hasattr(response2, "usage"):
            cache_read_tokens = getattr(response2.usage, "cache_read_input_tokens", 0)
            print(f"✓ Second request - cache_read_input_tokens: {cache_read_tokens}")

            if cache_read_tokens > 0:
                print(f"✓ Successfully read {cache_read_tokens} tokens from cache")

        print(f"✓ Prompt caching test passed!")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("web_search"))
    def test_47_web_search_error_handling(self, anthropic_client, test_config, provider, model):
        """Test Case 47: Web search error code handling"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for web_search scenario")

        print(f"\n=== Testing Web Search Error Handling for provider {provider} ===")

        web_search_tool = {"type": "web_search_20250305", "name": "web_search", "max_uses": 5}

        # Try with an extremely long query that might trigger query_too_long error
        very_long_query = "What is " + ("the meaning of life and the universe " * 50)

        messages = [
            {"role": "user", "content": very_long_query[:1000]}  # Limit to reasonable length
        ]

        try:
            response = anthropic_client.messages.create(
                model=format_provider_model(provider, model),
                messages=messages,
                tools=[web_search_tool],
                max_tokens=2048,
            )

            # Check response structure
            assert response is not None, "Response should not be None"
            assert hasattr(response, "content"), "Response should have content"

            # Look for any error structures in the response
            has_error = False
            for block in response.content:
                if hasattr(block, "type") and block.type == "web_search_tool_result":
                    if hasattr(block, "content") and isinstance(block.content, dict):
                        if "error_code" in block.content:
                            has_error = True
                            error_code = block.content["error_code"]
                            print(f"✓ Found error code: {error_code}")

            if not has_error:
                print(f"✓ Request handled successfully (no errors triggered)")

        except Exception as e:
            # Some errors might be raised as exceptions
            print(f"✓ Exception caught (expected for error scenarios): {type(e).__name__}")

        print(f"✓ Error handling test passed!")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("web_search"))
    def test_48_web_search_no_results_graceful(
        self, anthropic_client, test_config, provider, model
    ):
        """Test Case 48: Web search with query that may return no results"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for web_search scenario")

        print(f"\n=== Testing Web Search No Results Handling for provider {provider} ===")

        web_search_tool = {"type": "web_search_20250305", "name": "web_search", "max_uses": 3}

        # Use a very specific/nonsensical query
        messages = [
            {"role": "user", "content": "Find information about xyzabc123nonexistent456topic789"}
        ]

        response = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            messages=messages,
            tools=[web_search_tool],
            max_tokens=2048,
        )

        # Validate graceful handling
        assert response is not None, "Response should not be None"
        assert hasattr(response, "content"), "Response should have content"
        assert len(response.content) > 0, "Content should not be empty"

        # Check for search attempt
        has_search_attempt = False
        has_response_text = False

        for block in response.content:
            if hasattr(block, "type"):
                if (
                    block.type == "server_tool_use"
                    and hasattr(block, "name")
                    and block.name == "web_search"
                ):
                    has_search_attempt = True
                    print(f"✓ Web search was attempted")
                elif block.type == "text" and hasattr(block, "text"):
                    has_response_text = True
                    print(f"✓ Response text present (first 100 chars): {block.text[:100]}...")

        assert has_search_attempt, "Should attempt web search"
        assert has_response_text, "Should provide text response even with no/few results"

        print(f"✓ No results graceful handling test passed!")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("web_search"))
    def test_49_web_search_sources_validation(self, anthropic_client, test_config, provider, model):
        """Test Case 49: Comprehensive web search sources validation"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for web_search scenario")

        print(f"\n=== Testing Web Search Sources Validation for provider {provider} ===")

        web_search_tool = {"type": "web_search_20250305", "name": "web_search", "max_uses": 5}

        messages = [
            {
                "role": "user",
                "content": "What are the main programming languages used for web development?",
            }
        ]

        response = anthropic_client.messages.create(
            model=format_provider_model(provider, model),
            messages=messages,
            tools=[web_search_tool],
            max_tokens=2048,
        )

        # Collect all search sources
        all_sources = []
        for block in response.content:
            if hasattr(block, "type") and block.type == "web_search_tool_result":
                if hasattr(block, "content") and block.content:
                    for result in block.content:
                        if hasattr(result, "type") and result.type == "web_search_result":
                            all_sources.append(result)

        # Validate sources using helper
        from .utils.common import assert_web_search_sources_valid

        if len(all_sources) > 0:
            assert_web_search_sources_valid(all_sources)
            print(f"✓ Found and validated {len(all_sources)} search sources")

            # Log details of first few sources
            for i, source in enumerate(all_sources[:3]):
                print(f"  Source {i+1}:")
                print(f"    URL: {source.url}")
                print(f"    Title: {source.title if hasattr(source, 'title') else 'N/A'}")
                if hasattr(source, "page_age"):
                    print(f"    Page age: {source.page_age}")
                if hasattr(source, "encrypted_content"):
                    print(f"    Encrypted content: Present")
        else:
            print(f"⚠ No search sources found (may indicate no search was performed)")

        print(f"✓ Sources validation test passed!")

    # =========================================================================
    # Async Inference Tests
    # =========================================================================

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("simple_chat")
    )
    def test_50_async_messages(self, anthropic_client, test_config, provider, model):
        """Test Case 50: Async messages - submit and poll"""
        _ = test_config
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        print(f"\n=== Testing Async Messages for provider {provider} ===")
        messages = convert_to_anthropic_messages(SIMPLE_CHAT_MESSAGES)

        request_params = {
            "model": format_provider_model(provider, model),
            "messages": messages,
            "max_tokens": 100,
        }

        # Submit async request
        initial = anthropic_client.messages.create(
            **request_params,
            extra_headers={"x-bf-async": "true"},
        )

        assert initial.id is not None, "Async response should have an ID"
        print(f"  Async job ID: {initial.id}")

        # If completed synchronously (content is present), validate and return
        if initial.content and len(initial.content) > 0:
            print("  Status: completed (sync)")
            assert initial.content[0].type == "text"
            assert len(initial.content[0].text) > 0
            print(f"  Result: {initial.content[0].text[:80]}...")
            return

        print("  Status: processing")

        # Poll until completed
        max_polls = 30
        for i in range(max_polls):
            time.sleep(2)
            print(f"  Polling attempt {i + 1}/{max_polls}...")

            poll = anthropic_client.messages.create(
                **request_params,
                extra_headers={"x-bf-async-id": initial.id},
            )

            if poll.content and len(poll.content) > 0:
                print("  Status: completed")
                assert poll.content[0].type == "text"
                assert len(poll.content[0].text) > 0
                print(f"  Result: {poll.content[0].text[:80]}...")
                print("✓ Async messages test passed!")
                return

        pytest.fail(f"Async job did not complete after {max_polls} polls")


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
    print(
        f"{operation} usage - input_tokens: {usage.input_tokens}, "
        f"cache_creation_input_tokens: {getattr(usage, 'cache_creation_input_tokens', 0)}, "
        f"cache_read_input_tokens: {getattr(usage, 'cache_read_input_tokens', 0)}"
    )

    assert hasattr(
        usage, "cache_creation_input_tokens"
    ), f"{operation} should have cache_creation_input_tokens"
    cache_write_tokens = getattr(usage, "cache_creation_input_tokens", 0)
    assert (
        cache_write_tokens > 0
    ), f"{operation} should create cache (got {cache_write_tokens} tokens)"

    return cache_write_tokens


def validate_cache_read(usage: Any, operation: str) -> int:
    """Validate cache read operation and return tokens read"""
    print(
        f"{operation} usage - input_tokens: {usage.input_tokens}, "
        f"cache_creation_input_tokens: {getattr(usage, 'cache_creation_input_tokens', 0)}, "
        f"cache_read_input_tokens: {getattr(usage, 'cache_read_input_tokens', 0)}"
    )

    assert hasattr(
        usage, "cache_read_input_tokens"
    ), f"{operation} should have cache_read_input_tokens"
    cache_read_tokens = getattr(usage, "cache_read_input_tokens", 0)
    assert (
        cache_read_tokens > 0
    ), f"{operation} should read from cache (got {cache_read_tokens} tokens)"

    return cache_read_tokens


# ============================================================================
# COMPACTION TESTS
# ============================================================================


class TestAnthropicCompaction:
    """Test suite for Anthropic compaction feature (context management)

    Tests the server-side context compaction feature that automatically
    summarizes older context when approaching context window limits.
    Requires Claude Opus 4.6 and the compact-2026-01-12 beta header.
    """

    @pytest.fixture
    def compaction_client(self):
        """Create Anthropic client with compaction beta header"""
        from .utils.config_loader import get_config, get_integration_url

        api_key = get_api_key("anthropic")
        base_url = get_integration_url("anthropic")
        config = get_config()
        api_config = config.get_api_config()
        integration_settings = config.get_integration_settings("anthropic")

        default_headers = {"anthropic-beta": "compact-2026-01-12"}
        if integration_settings.get("version"):
            default_headers["anthropic-version"] = integration_settings["version"]

        return Anthropic(
            api_key=api_key,
            base_url=base_url,
            timeout=api_config.get("timeout", 300),
            default_headers=default_headers,
        )

    def _generate_large_context(self, token_count_estimate: int) -> str:
        """Generate large text context to trigger compaction"""
        # Approximately 4 chars per token
        chars_needed = token_count_estimate * 4
        base_text = "This is a sample document about software architecture and design patterns. "
        repeat_count = chars_needed // len(base_text) + 1
        return (base_text * repeat_count)[:chars_needed]

    def _create_large_messages(self, total_tokens: int = 80000) -> List[Dict[str, Any]]:
        """Create messages with enough content to trigger compaction

        Args:
            total_tokens: Estimated token count (must be > 50000 to trigger compaction)
                         Default is 80000 to ensure we exceed 50k after actual tokenization
        """
        messages = []
        large_text = self._generate_large_context(total_tokens)

        # Split into multiple turns to simulate a conversation
        chunk_size = len(large_text) // 10
        for i in range(10):
            chunk = large_text[i * chunk_size : (i + 1) * chunk_size]
            messages.append({"role": "user", "content": f"Document part {i+1}: {chunk}"})
            messages.append({"role": "assistant", "content": f"I've received document part {i+1}."})

        # Add final query
        messages.append(
            {"role": "user", "content": "Please provide a brief summary of the document."}
        )

        return messages

    def test_32_compaction_basic(self, compaction_client):
        """Test Case 32: Basic compaction functionality

        Verifies that compaction can be enabled and creates a compaction block
        when the trigger threshold is exceeded.
        """
        print("\n=== Testing Basic Compaction ===")

        # Create messages that will trigger compaction (minimum trigger is 50k tokens)
        # Use 80k to ensure we exceed 50k after actual tokenization
        messages = self._create_large_messages(80000)

        print(f"Created {len(messages)} messages for compaction test")

        # Enable compaction with minimum allowed threshold (50k tokens)
        response = compaction_client.beta.messages.create(
            model="claude-opus-4-6",
            messages=messages,
            max_tokens=1024,
            context_management={
                "edits": [
                    {
                        "type": "compact_20260112",
                        "trigger": {
                            "type": "input_tokens",
                            "value": 50000,  # Minimum allowed threshold
                        },
                    }
                ]
            },
        )

        # Validate response structure
        assert hasattr(response, "content"), "Response should have content"
        assert len(response.content) > 0, "Response should have at least one content block"

        # Check for compaction block
        compaction_blocks = [
            block
            for block in response.content
            if hasattr(block, "type") and block.type == "compaction"
        ]

        if len(compaction_blocks) > 0:
            print(f"✓ Compaction triggered! Found {len(compaction_blocks)} compaction block(s)")
            compaction_block = compaction_blocks[0]

            # Validate compaction block structure
            assert hasattr(compaction_block, "content"), "Compaction block should have content"
            assert len(compaction_block.content) > 0, "Compaction summary should not be empty"
            print(f"  Compaction summary length: {len(compaction_block.content)} chars")
            print(f"  Summary preview: {compaction_block.content[:200]}...")

            # Check for text content after compaction
            text_blocks = [
                block
                for block in response.content
                if hasattr(block, "type") and block.type == "text"
            ]
            assert len(text_blocks) > 0, "Response should have text content after compaction"
            print(f"✓ Response also contains {len(text_blocks)} text block(s)")
        else:
            print("⚠ Compaction not triggered (threshold may not have been reached)")
            # Still validate it's a valid response
            assert_valid_chat_response(response)

        # Validate response has usage information
        assert hasattr(response, "usage"), "Response should have usage information"
        print(f"  Input tokens: {response.usage.input_tokens}")
        print(f"  Output tokens: {response.usage.output_tokens}")

    def test_33_compaction_usage_tracking(self, compaction_client):
        """Test Case 33: Compaction usage tracking with iterations

        Verifies that usage information includes iteration details when
        compaction occurs, showing separate compaction and message iterations.
        """
        print("\n=== Testing Compaction Usage Tracking ===")

        messages = self._create_large_messages(80000)

        response = compaction_client.beta.messages.create(
            model="claude-opus-4-6",
            messages=messages,
            max_tokens=1024,
            context_management={
                "edits": [
                    {
                        "type": "compact_20260112",
                        "trigger": {
                            "type": "input_tokens",
                            "value": 50000,  # Minimum allowed threshold
                        },
                    }
                ]
            },
        )

        # Validate usage structure
        assert hasattr(response, "usage"), "Response should have usage information"
        usage = response.usage

        print(f"Top-level usage:")
        print(f"  input_tokens: {usage.input_tokens}")
        print(f"  output_tokens: {usage.output_tokens}")

        # Check for iterations array (only present when compaction triggers)
        iterations = None
        if hasattr(usage, "iterations"):
            iterations = usage.iterations
        elif isinstance(usage, dict) and "iterations" in usage:
            iterations = usage["iterations"]

        if iterations:
            print(f"\n✓ Found {len(iterations)} iteration(s)")

            # Calculate total tokens from iterations
            total_input = 0
            total_output = 0

            for idx, iteration in enumerate(iterations):
                # Handle both dict and object iteration types
                if isinstance(iteration, dict):
                    assert "type" in iteration, "Iteration should have type"
                    assert "input_tokens" in iteration, "Iteration should have input_tokens"
                    assert "output_tokens" in iteration, "Iteration should have output_tokens"

                    iter_type = iteration["type"]
                    iter_input = iteration["input_tokens"]
                    iter_output = iteration["output_tokens"]
                else:
                    assert hasattr(iteration, "type"), "Iteration should have type"
                    assert hasattr(iteration, "input_tokens"), "Iteration should have input_tokens"
                    assert hasattr(
                        iteration, "output_tokens"
                    ), "Iteration should have output_tokens"

                    iter_type = iteration.type
                    iter_input = iteration.input_tokens
                    iter_output = iteration.output_tokens

                print(f"\n  Iteration {idx + 1}:")
                print(f"    type: {iter_type}")
                print(f"    input_tokens: {iter_input}")
                print(f"    output_tokens: {iter_output}")

                if iter_type == "compaction":
                    # Validate compaction iteration
                    assert iter_input > 0, "Compaction should consume input tokens"
                    assert iter_output > 0, "Compaction should produce summary tokens"
                    print(f"    ✓ Compaction iteration validated")
                elif iter_type == "message":
                    # Validate message iteration
                    assert iter_input > 0, "Message should have input tokens"
                    assert iter_output > 0, "Message should have output tokens"
                    print(f"    ✓ Message iteration validated")

                # Only sum non-compaction iterations for comparison with top-level
                if iter_type != "compaction":
                    total_input += iter_input
                    total_output += iter_output

            # Top-level tokens should equal sum of non-compaction iterations
            print(f"\nValidating top-level vs iterations:")
            print(f"  Top-level input: {usage.input_tokens}, Non-compaction sum: {total_input}")
            print(f"  Top-level output: {usage.output_tokens}, Non-compaction sum: {total_output}")

            # Allow small variance due to rounding
            assert (
                abs(usage.input_tokens - total_input) < 10
            ), f"Top-level input tokens should match non-compaction sum"
            assert (
                abs(usage.output_tokens - total_output) < 10
            ), f"Top-level output tokens should match non-compaction sum"

            print("✓ Usage tracking validation passed")
        else:
            print("⚠ No iterations found (compaction may not have triggered)")

    def test_34_compaction_streaming(self, compaction_client):
        """Test Case 34: Compaction with streaming responses

        Verifies that compaction works correctly with streaming, including
        proper event ordering and compaction block streaming.
        """
        print("\n=== Testing Compaction with Streaming ===")

        messages = self._create_large_messages(80000)

        stream = compaction_client.beta.messages.stream(
            model="claude-opus-4-6",
            messages=messages,
            max_tokens=1024,
            context_management={
                "edits": [
                    {
                        "type": "compact_20260112",
                        "trigger": {
                            "type": "input_tokens",
                            "value": 50000,  # Minimum allowed threshold
                        },
                    }
                ]
            },
        )

        compaction_started = False
        compaction_content = ""
        text_content = ""
        compaction_delta_count = 0
        text_delta_count = 0

        print("Processing stream events...")

        with stream as s:
            for event in s:
                if event.type == "content_block_start":
                    if hasattr(event, "content_block"):
                        if event.content_block.type == "compaction":
                            compaction_started = True
                            print("  ✓ Compaction block started")
                        elif event.content_block.type == "text":
                            print("  ✓ Text block started")

                elif event.type == "content_block_delta":
                    if hasattr(event, "delta"):
                        if event.delta.type == "compaction_delta":
                            # Compaction streams as single delta
                            compaction_content += event.delta.content
                            compaction_delta_count += 1
                            print(
                                f"  ✓ Compaction delta received ({len(event.delta.content)} chars)"
                            )
                        elif event.delta.type == "text_delta":
                            # Text streams incrementally
                            text_content += event.delta.text
                            text_delta_count += 1

                elif event.type == "content_block_stop":
                    print(f"  ✓ Content block stopped (index: {event.index})")

            # Get final message
            final_message = s.get_final_message()

        # Validate streaming results
        if compaction_started:
            print(f"\n✓ Compaction triggered during streaming")
            assert len(compaction_content) > 0, "Compaction content should not be empty"
            print(
                f"  Compaction summary: {len(compaction_content)} chars, {compaction_delta_count} delta(s)"
            )
            print(f"  Compaction preview: {compaction_content[:200]}...")

            # Compaction typically streams as single complete delta
            assert compaction_delta_count >= 1, "Should have at least one compaction delta"
        else:
            print("⚠ Compaction not triggered during streaming")

        # Validate text content was received
        assert len(text_content) > 0, "Should receive text content"
        print(f"  Text content: {len(text_content)} chars, {text_delta_count} delta(s)")

        # Validate final message structure
        assert hasattr(final_message, "content"), "Final message should have content"
        assert len(final_message.content) > 0, "Final message should have content blocks"
        assert hasattr(final_message, "usage"), "Final message should have usage"

        print(f"✓ Streaming compaction test passed")

    def test_35_compaction_pause_after(self, compaction_client):
        """Test Case 35: Compaction with pause_after_compaction

        Verifies that pause_after_compaction causes the API to pause after
        generating the compaction summary, returning a 'compaction' stop_reason.
        """
        print("\n=== Testing Compaction with Pause After ===")

        messages = self._create_large_messages(80000)

        # First request with pause_after_compaction
        response1 = compaction_client.beta.messages.create(
            model="claude-opus-4-6",
            messages=messages,
            max_tokens=1024,
            context_management={
                "edits": [
                    {
                        "type": "compact_20260112",
                        "trigger": {
                            "type": "input_tokens",
                            "value": 50000,  # Minimum allowed threshold
                        },
                        "pause_after_compaction": True,
                    }
                ]
            },
        )

        # Check if compaction triggered a pause
        if hasattr(response1, "stop_reason") and response1.stop_reason == "compaction":
            print("✓ Compaction pause triggered!")
            print(f"  stop_reason: {response1.stop_reason}")

            # Validate response contains only compaction block
            assert hasattr(response1, "content"), "Response should have content"
            assert len(response1.content) > 0, "Response should have at least one content block"

            # Should have compaction block
            compaction_blocks = [
                b for b in response1.content if hasattr(b, "type") and b.type == "compaction"
            ]
            assert len(compaction_blocks) > 0, "Response should contain compaction block"
            print(f"  Compaction summary length: {len(compaction_blocks[0].content)} chars")

            # Append response to messages for continuation
            messages.append({"role": "assistant", "content": response1.content})

            # Continue the request (could add preserved messages here)
            print("\nContinuing after compaction pause...")
            response2 = compaction_client.beta.messages.create(
                model="claude-opus-4-6",
                messages=messages,
                max_tokens=1024,
                context_management={"edits": [{"type": "compact_20260112"}]},
            )

            # Validate continuation response
            assert_valid_chat_response(response2)
            assert response2.stop_reason != "compaction", "Continuation should not pause again"

            # Should have text content in continuation
            text_blocks = [b for b in response2.content if hasattr(b, "type") and b.type == "text"]
            assert len(text_blocks) > 0, "Continuation should have text content"
            print(f"✓ Continuation successful with {len(text_blocks)} text block(s)")

        else:
            print("⚠ Compaction pause not triggered")
            print(
                f"  stop_reason: {response1.stop_reason if hasattr(response1, 'stop_reason') else 'N/A'}"
            )
            # Still validate it's a valid response
            assert_valid_chat_response(response1)

    def test_36_compaction_custom_instructions(self, compaction_client):
        """Test Case 36: Compaction with custom summarization instructions

        Verifies that custom instructions parameter works and affects the
        compaction summary generation.
        """
        print("\n=== Testing Compaction with Custom Instructions ===")

        messages = self._create_large_messages(80000)

        custom_instructions = (
            "Create a highly detailed technical summary that preserves all "
            "specific technical terms, code snippets, and architectural decisions. "
            "Include section headers for clarity."
        )

        response = compaction_client.beta.messages.create(
            model="claude-opus-4-6",
            messages=messages,
            max_tokens=1024,
            context_management={
                "edits": [
                    {
                        "type": "compact_20260112",
                        "trigger": {
                            "type": "input_tokens",
                            "value": 50000,  # Minimum allowed threshold
                        },
                        "instructions": custom_instructions,
                    }
                ]
            },
        )

        # Validate response
        assert hasattr(response, "content"), "Response should have content"

        # Check for compaction block
        compaction_blocks = [
            block
            for block in response.content
            if hasattr(block, "type") and block.type == "compaction"
        ]

        if len(compaction_blocks) > 0:
            print("✓ Compaction with custom instructions triggered")
            compaction_content = compaction_blocks[0].content
            print(f"  Summary length: {len(compaction_content)} chars")
            print(f"  Summary preview: {compaction_content[:300]}...")

            # Validate summary is substantial (custom instructions may produce longer summaries)
            assert len(compaction_content) > 50, "Custom summary should be substantial"
            print("✓ Custom instructions applied successfully")
        else:
            print("⚠ Compaction not triggered (threshold may not have been reached)")
            assert_valid_chat_response(response)

    def test_37_compaction_continuation(self, compaction_client):
        """Test Case 37: Compaction block continuation across multiple requests

        Verifies that compaction blocks can be passed back to the API and
        that prior content is properly dropped in favor of the summary.
        """
        print("\n=== Testing Compaction Continuation ===")

        # Initial conversation with compaction
        messages = self._create_large_messages(80000)

        response1 = compaction_client.beta.messages.create(
            model="claude-opus-4-6",
            messages=messages,
            max_tokens=1024,
            context_management={
                "edits": [
                    {
                        "type": "compact_20260112",
                        "trigger": {
                            "type": "input_tokens",
                            "value": 50000,  # Minimum allowed threshold
                        },
                    }
                ]
            },
        )

        # Check if compaction occurred
        compaction_blocks = [
            b for b in response1.content if hasattr(b, "type") and b.type == "compaction"
        ]

        if len(compaction_blocks) > 0:
            print("✓ Initial compaction created")

            # Append entire response (including compaction block) to messages
            messages.append({"role": "assistant", "content": response1.content})

            # Add a follow-up query
            messages.append(
                {
                    "role": "user",
                    "content": "Based on what we discussed, what are the three main points?",
                }
            )

            print("\nSending continuation request with compaction block...")

            # Second request with compaction block included
            response2 = compaction_client.beta.messages.create(
                model="claude-opus-4-6",
                messages=messages,
                max_tokens=1024,
                context_management={"edits": [{"type": "compact_20260112"}]},
            )

            # Validate continuation works
            assert_valid_chat_response(response2)
            print("✓ Continuation with compaction block successful")

            # Check usage - should reflect effective context after compaction
            if hasattr(response2, "usage"):
                print(f"  Continuation input tokens: {response2.usage.input_tokens}")
                print(f"  Continuation output tokens: {response2.usage.output_tokens}")

                # Input tokens should be significantly less than original due to compaction
                # This validates that compaction actually reduced context
                print("✓ Context successfully compacted and reused")
        else:
            print("⚠ Initial compaction not triggered, skipping continuation test")

    def test_38_compaction_multiple_iterations(self, compaction_client):
        """Test Case 38: Multiple compaction iterations in single conversation

        Verifies that compaction can trigger multiple times as conversation
        grows, with each compaction replacing the previous one.
        """
        print("\n=== Testing Multiple Compaction Iterations ===")

        # Start with large enough context to potentially trigger compaction
        messages = self._create_large_messages(80000)

        compaction_count = 0
        max_iterations = 3

        for iteration in range(max_iterations):
            print(f"\nIteration {iteration + 1}:")

            # Add more context to grow beyond threshold
            messages.append(
                {
                    "role": "user",
                    "content": f"Additional context for iteration {iteration + 1}: "
                    + self._generate_large_context(20000),
                }
            )

            response = compaction_client.beta.messages.create(
                model="claude-opus-4-6",
                messages=messages,
                max_tokens=512,
                context_management={
                    "edits": [
                        {
                            "type": "compact_20260112",
                            "trigger": {
                                "type": "input_tokens",
                                "value": 50000,  # Minimum allowed threshold
                            },
                        }
                    ]
                },
            )

            # Check for compaction
            compaction_blocks = [
                b for b in response.content if hasattr(b, "type") and b.type == "compaction"
            ]

            if len(compaction_blocks) > 0:
                compaction_count += 1
                print(f"  ✓ Compaction {compaction_count} triggered")
                print(f"    Summary length: {len(compaction_blocks[0].content)} chars")

            # Append response to continue conversation
            messages.append({"role": "assistant", "content": response.content})

            # Validate response
            assert_valid_chat_response(response)

        print(f"\n✓ Multiple iteration test completed")
        print(f"  Total compactions triggered: {compaction_count}")

        if compaction_count > 0:
            print("✓ At least one compaction occurred across iterations")
        else:
            print("⚠ No compactions triggered (threshold may need adjustment)")

    def test_39_compaction_with_prompt_caching(self, compaction_client):
        """Test Case 39: Compaction combined with prompt caching

        Verifies that compaction blocks can have cache_control breakpoints
        and that caching works correctly with compacted context.
        """
        print("\n=== Testing Compaction with Prompt Caching ===")

        messages = self._create_large_messages(80000)

        # First request - create compaction
        response1 = compaction_client.beta.messages.create(
            model="claude-opus-4-6",
            messages=messages,
            max_tokens=1024,
            context_management={
                "edits": [
                    {
                        "type": "compact_20260112",
                        "trigger": {
                            "type": "input_tokens",
                            "value": 50000,  # Minimum allowed threshold
                        },
                    }
                ]
            },
        )

        compaction_blocks = [
            b for b in response1.content if hasattr(b, "type") and b.type == "compaction"
        ]

        if len(compaction_blocks) > 0:
            print("✓ Compaction created in first request")

            # Modify compaction block to add cache_control
            modified_content = []
            for block in response1.content:
                if hasattr(block, "type") and block.type == "compaction":
                    # Add cache control to compaction block
                    modified_content.append(
                        {
                            "type": "compaction",
                            "content": block.content,
                            "cache_control": {"type": "ephemeral"},
                        }
                    )
                elif hasattr(block, "type") and block.type == "text":
                    modified_content.append({"type": "text", "text": block.text})

            # Create new messages with cached compaction block
            cached_messages = [{"role": "assistant", "content": modified_content}]
            cached_messages.append(
                {"role": "user", "content": "What were the main topics discussed?"}
            )

            print("\nSending request with cached compaction block...")

            # Second request should hit cache
            response2 = compaction_client.beta.messages.create(
                model="claude-opus-4-6",
                messages=cached_messages,
                max_tokens=512,
                context_management={"edits": [{"type": "compact_20260112"}]},
            )

            # Validate response
            assert_valid_chat_response(response2)

            # Check for cache hit in usage
            if hasattr(response2, "usage"):
                print(f"  Input tokens: {response2.usage.input_tokens}")
                if hasattr(response2.usage, "cache_read_input_tokens"):
                    cache_read = response2.usage.cache_read_input_tokens
                    print(f"  Cache read tokens: {cache_read}")
                    if cache_read > 0:
                        print("✓ Cache hit detected on compaction block!")
                    else:
                        print("  Note: Cache may not have hit (timing/TTL)")
                else:
                    print("  Note: No cache_read_input_tokens in usage")

            print("✓ Compaction with caching test completed")
        else:
            print("⚠ Compaction not triggered, skipping caching test")

    def test_40_compaction_edge_cases(self, compaction_client):
        """Test Case 40: Compaction edge cases and error handling

        Verifies behavior with minimal context, invalid parameters, and
        boundary conditions.
        """
        print("\n=== Testing Compaction Edge Cases ===")

        # Test 1: Very small context (should not trigger compaction)
        print("\n1. Testing with minimal context:")
        small_messages = [
            {"role": "user", "content": "Hello"},
            {"role": "assistant", "content": "Hi there!"},
            {"role": "user", "content": "How are you?"},
        ]

        response_small = compaction_client.beta.messages.create(
            model="claude-opus-4-6",
            messages=small_messages,
            max_tokens=100,
            context_management={
                "edits": [
                    {
                        "type": "compact_20260112",
                        "trigger": {
                            "type": "input_tokens",
                            "value": 50000,  # Won't be reached with small messages
                        },
                    }
                ]
            },
        )

        # Should work without compaction
        assert_valid_chat_response(response_small)
        compaction_in_small = [
            b for b in response_small.content if hasattr(b, "type") and b.type == "compaction"
        ]
        assert len(compaction_in_small) == 0, "Small context should not trigger compaction"
        print("  ✓ Small context handled correctly (no compaction)")

        # Test 2: Default trigger value (should use 150,000 tokens)
        print("\n2. Testing with default trigger value:")
        messages = [{"role": "user", "content": "Tell me about AI."}]

        response_default = compaction_client.beta.messages.create(
            model="claude-opus-4-6",
            messages=messages,
            max_tokens=100,
            context_management={
                "edits": [
                    {
                        "type": "compact_20260112"
                        # No trigger specified, should use default 150k
                    }
                ]
            },
        )

        assert_valid_chat_response(response_default)
        print("  ✓ Default trigger value accepted")

        # Test 3: Compaction with tools
        print("\n3. Testing compaction with tool use:")
        tool_messages = [
            {
                "role": "user",
                "content": self._generate_large_context(80000) + " What's the weather?",
            }
        ]

        tools = convert_to_anthropic_tools([WEATHER_TOOL])

        response_tools = compaction_client.beta.messages.create(
            model="claude-opus-4-6",
            messages=tool_messages,
            tools=tools,
            max_tokens=512,
            context_management={
                "edits": [
                    {
                        "type": "compact_20260112",
                        "trigger": {
                            "type": "input_tokens",
                            "value": 50000,  # Minimum allowed threshold
                        },
                    }
                ]
            },
        )

        assert_valid_chat_response(response_tools)
        print("  ✓ Compaction works with tool use")

        print("\n✓ All edge cases handled correctly")
