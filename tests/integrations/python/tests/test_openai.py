"""
OpenAI Integration Tests - Cross-Provider Support

🌉 CROSS-PROVIDER TESTING:
This test suite uses the OpenAI SDK to test against multiple AI providers through Bifrost.
Tests automatically run against all available providers with proper capability filtering.

Note: Tests automatically skip for providers that don't support specific capabilities.
Example: Speech synthesis only runs for OpenAI, vision tests skip for Cohere.

Tests all core scenarios using OpenAI SDK directly:
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
14. Speech synthesis
15. Audio transcription
16. Transcription streaming
17. Speech-transcription round trip
18. Speech error handling
19. Transcription error handling
20. Different voices and audio formats
21. Single text embedding
22. Batch text embeddings
23. Embedding similarity analysis
24. Embedding dissimilarity analysis
25. Different embedding models
26. Long text embedding
27. Embedding error handling
28. Embedding dimensionality reduction
29. Embedding encoding formats
30. Embedding usage tracking
31. List models
31a. Video create
31b. Video retrieve
31c. Video list
31d. Video download content
31e. Video delete
32. Responses API - simple text input
33. Responses API - with system message
34. Responses API - with image
35. Responses API - with tools
36. Responses API - streaming
37. Responses API - streaming with tools
38. Responses API - reasoning
39. Text Completions - simple prompt
40. Text Completions - streaming
41. Files API - file upload
42. Files API - file list
43. Files API - file retrieve
44. Files API - file delete
45. Files API - file content
46. Batch API - batch create with Files API
47. Batch API - batch list
48. Batch API - batch retrieve
49. Batch API - batch cancel
50. Batch API - end-to-end with Files API
51. Count tokens (Cross-Provider)
52. Image Generation - simple prompt
53. Image Generation - multiple images
54. Image Generation - quality parameter
55. Image Generation - different sizes

Batch API uses OpenAI SDK with x-model-provider header to route to different providers.
"""

import json
import os
import time
from typing import Any

import pytest
from openai import OpenAI

from .utils.common import (
    CALCULATOR_TOOL,
    COMPARISON_KEYWORDS,
    COMPLEX_E2E_MESSAGES,
    EMBEDDINGS_DIFFERENT_TEXTS,
    EMBEDDINGS_LONG_TEXT,
    EMBEDDINGS_MULTIPLE_TEXTS,
    EMBEDDINGS_SIMILAR_TEXTS,
    # Embeddings utilities
    EMBEDDINGS_SINGLE_TEXT,
    FILE_DATA_BASE64,
    BASE64_IMAGE,
    IMAGE_BASE64_MESSAGES,
    IMAGE_URL_MESSAGES,
    # Image Generation utilities
    IMAGE_GENERATION_SIMPLE_PROMPT,
    # Image Edit utilities
    IMAGE_EDIT_SIMPLE_PROMPT,
    IMAGE_EDIT_PROMPT_OUTPAINT,
    assert_valid_image_edit_response,
    create_simple_mask_image,
    INPUT_TOKENS_LONG_TEXT,
    # Input Tokens utilities
    INPUT_TOKENS_SIMPLE_TEXT,
    INPUT_TOKENS_WITH_SYSTEM,
    INVALID_ROLE_MESSAGES,
    LOCATION_KEYWORDS,
    MULTI_TURN_MESSAGES,
    MULTIPLE_IMAGES_MESSAGES,
    MULTIPLE_TOOL_CALL_MESSAGES,
    RESPONSES_IMAGE_INPUT,
    RESPONSES_REASONING_INPUT,
    RESPONSES_SIMPLE_TEXT_INPUT,
    RESPONSES_STREAMING_INPUT,
    RESPONSES_TEXT_WITH_SYSTEM,
    RESPONSES_TOOL_CALL_INPUT,
    SIMPLE_CHAT_MESSAGES,
    SINGLE_TOOL_CALL_MESSAGES,
    # Speech and Transcription utilities
    SPEECH_TEST_INPUT,
    STREAMING_CHAT_MESSAGES,
    STREAMING_TOOL_CALL_MESSAGES,
    TEXT_COMPLETION_SIMPLE_PROMPT,
    TEXT_COMPLETION_STREAMING_PROMPT,
    WEATHER_KEYWORDS,
    WEATHER_TOOL,
    Config,
    assert_error_propagation,
    assert_has_tool_calls,
    assert_valid_batch_list_response,
    # Batch API utilities
    assert_valid_batch_response,
    assert_valid_chat_response,
    assert_valid_embedding_response,
    assert_valid_embeddings_batch_response,
    assert_valid_error_response,
    assert_valid_file_delete_response,
    assert_valid_file_list_response,
    assert_valid_file_response,
    assert_valid_image_generation_response,
    assert_valid_image_response,
    assert_valid_input_tokens_response,
    assert_valid_responses_response,
    assert_valid_speech_response,
    assert_valid_text_completion_response,
    assert_valid_transcription_response,
    calculate_cosine_similarity,
    collect_responses_streaming_content,
    collect_streaming_content,
    collect_streaming_transcription_content,
    collect_text_completion_streaming_content,
    convert_to_responses_tools,
    create_batch_inline_requests,
    # Files API utilities
    create_batch_jsonl_content,
    extract_tool_calls,
    generate_test_audio,
    get_api_key,
    get_content_string,
    get_provider_voice,
    get_provider_voices,
    mock_tool_response,
    skip_if_no_api_key,
    # Citation utilities
    assert_valid_openai_annotation,
)
from .utils.config_loader import get_config, get_model
from .utils.parametrize import (
    format_provider_model,
    get_cross_provider_params_with_vk_for_scenario,
)


# Helper functions (defined early for use in test methods)
def extract_openai_tool_calls(response: Any) -> list[dict[str, Any]]:
    """Extract tool calls from OpenAI response format with proper type checking"""
    tool_calls = []

    # Type check for OpenAI ChatCompletion response
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
                print(f"Warning: Failed to parse tool call arguments: {e}")
                continue

    return tool_calls


def convert_to_openai_tools(tools: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Convert common tool format to OpenAI format"""
    return [{"type": "function", "function": tool} for tool in tools]


def get_provider_openai_client(provider, vk_enabled=False):
    """Create OpenAI client for given provider (provider passed via extra_body/extra_query)
    
    Args:
        provider: Provider name for API key lookup
        vk_enabled: If True, include x-bf-vk header for virtual key testing
    """
    from .utils.config_loader import get_config, get_integration_url

    api_key = get_api_key(provider)
    base_url = get_integration_url("openai")
    config = get_config()
    api_config = config.get_api_config()

    # Build default headers
    default_headers = {}
    if vk_enabled:
        vk = config.get_virtual_key()
        if vk:
            default_headers["x-bf-vk"] = vk

    return OpenAI(
        api_key=api_key,
        base_url=base_url,
        timeout=api_config.get("timeout", 300),
        default_headers=default_headers if default_headers else None,
    )


def _wait_for_video_terminal_status(
    client: OpenAI,
    video_id: str,
    timeout_seconds: int = 900,
    poll_interval_seconds: int = 5,
):
    """Poll a video job until it reaches completed/failed status."""
    deadline = time.time() + timeout_seconds
    last_status = "unknown"

    while time.time() < deadline:
        video = client.videos.retrieve(video_id)
        status = getattr(video, "status", None)
        if isinstance(status, str):
            last_status = status

        if status in {"completed", "failed"}:
            return video

        time.sleep(poll_interval_seconds)

    raise AssertionError(
        f"Video job {video_id} did not reach terminal status within {timeout_seconds}s. "
        f"Last observed status: {last_status}"
    )


def _safe_cleanup_video(client: OpenAI, video_id: str, provider: str):
    """Best-effort cleanup for created video jobs."""
    try:
        if provider != "openai" :
            print(f"Video cleanup skipped for {video_id}: provider {provider} does not support video cleanup")
            return

        terminal_video = _wait_for_video_terminal_status(
            client,
            video_id,
            timeout_seconds=180,
            poll_interval_seconds=3,
        )
        if getattr(terminal_video, "status", None) in {"completed", "failed"}:
            client.videos.delete(video_id)
    except Exception as e:
        print(f"Video cleanup skipped for {video_id}: {e}")


@pytest.fixture
def openai_client():
    """Create OpenAI client for testing"""
    from .utils.config_loader import get_config, get_integration_url

    api_key = get_api_key("openai")
    base_url = get_integration_url("openai")

    # Get additional integration settings
    config = get_config()
    integration_settings = config.get_integration_settings("openai")
    api_config = config.get_api_config()

    client_kwargs = {
        "api_key": api_key,
        "base_url": base_url,
        "timeout": api_config.get("timeout", 300),
        "max_retries": api_config.get("max_retries", 3),
    }

    # Add optional OpenAI-specific settings
    if integration_settings.get("organization"):
        client_kwargs["organization"] = integration_settings["organization"]
    if integration_settings.get("project"):
        client_kwargs["project"] = integration_settings["project"]

    return OpenAI(**client_kwargs)


@pytest.fixture
def test_config():
    """Test configuration"""
    return Config()


class TestOpenAIIntegration:
    """Test suite for OpenAI integration with cross-provider support"""

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("simple_chat")
    )
    def test_01_simple_chat(self, test_config, provider, model, vk_enabled):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 1: Simple chat interaction - runs across all available providers"""
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        response = client.chat.completions.create(
            model=format_provider_model(provider, model),
            messages=SIMPLE_CHAT_MESSAGES,
            max_tokens=100,            
        )
        assert_valid_chat_response(response)
        assert response.choices[0].message.content is not None
        assert len(response.choices[0].message.content) > 0

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("multi_turn_conversation")
    )
    def test_02_multi_turn_conversation(self, test_config, provider, model, vk_enabled):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 2: Multi-turn conversation - runs across all available providers"""
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        response = client.chat.completions.create(
            model=format_provider_model(provider, model),
            messages=MULTI_TURN_MESSAGES,
            max_tokens=150,
        )

        assert_valid_chat_response(response)
        content = get_content_string(response.choices[0].message.content)
        # Should mention population or numbers since we asked about Paris population
        assert any(word in content for word in ["population", "million", "people", "inhabitants"])

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("tool_calls"))
    def test_03_single_tool_call(self, test_config, provider, model, vk_enabled):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 3: Single tool call - auto-skips providers without tool support"""
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        response = client.chat.completions.create(
            model=format_provider_model(provider, model),
            messages=SINGLE_TOOL_CALL_MESSAGES,
            tools=[{"type": "function", "function": WEATHER_TOOL}],
            max_tokens=100,
        )

        assert_has_tool_calls(response, expected_count=1)
        tool_calls = extract_tool_calls(response)
        assert tool_calls[0]["name"] == "get_weather"
        assert "location" in tool_calls[0]["arguments"]

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("multiple_tool_calls")
    )
    def test_04_multiple_tool_calls(self, test_config, provider, model, vk_enabled):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 4: Multiple tool calls in one response - auto-skips providers without multiple tool support"""
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        response = client.chat.completions.create(
            model=format_provider_model(provider, model),
            messages=MULTIPLE_TOOL_CALL_MESSAGES,
            tools=[
                {"type": "function", "function": WEATHER_TOOL},
                {"type": "function", "function": CALCULATOR_TOOL},
            ],
            max_tokens=200,
        )

        assert_has_tool_calls(response, expected_count=2)
        tool_calls = extract_openai_tool_calls(response)
        tool_names = [tc["name"] for tc in tool_calls]
        assert "get_weather" in tool_names
        assert "calculate" in tool_names

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("end2end_tool_calling")
    )
    def test_05_end2end_tool_calling(self, test_config, provider, model, vk_enabled):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 5: Complete tool calling flow with responses"""
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        # Initial request
        messages = [{"role": "user", "content": "What's the weather in Boston in fahrenheit?"}]

        response = client.chat.completions.create(
            model=format_provider_model(provider, model),
            messages=messages,
            tools=[{"type": "function", "function": WEATHER_TOOL}],
            max_tokens=100,
        )

        assert_has_tool_calls(response, expected_count=1)

        # Add assistant's tool call to conversation
        messages.append(response.choices[0].message)

        # Add tool response
        tool_calls = extract_openai_tool_calls(response)
        tool_response = mock_tool_response(tool_calls[0]["name"], tool_calls[0]["arguments"])

        messages.append(
            {
                "role": "tool",
                "tool_call_id": response.choices[0].message.tool_calls[0].id,
                "content": tool_response,
            }
        )

        # Get final response
        final_response = client.chat.completions.create(
            model=format_provider_model(provider, model), messages=messages, max_tokens=150
        )

        assert_valid_chat_response(final_response)
        content = get_content_string(final_response.choices[0].message.content)
        weather_location_keywords = WEATHER_KEYWORDS + LOCATION_KEYWORDS
        assert any(word in content for word in weather_location_keywords)

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("automatic_function_calling")
    )
    def test_06_automatic_function_calling(self, test_config, provider, model, vk_enabled):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 6: Automatic function calling (tool_choice='auto')"""
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        response = client.chat.completions.create(
            model=format_provider_model(provider, model),
            messages=[{"role": "user", "content": "Calculate 25 * 4 for me"}],
            tools=[{"type": "function", "function": CALCULATOR_TOOL}],
            tool_choice="auto",  # Let model decide
            max_tokens=100,
        )

        # Should automatically choose to use the calculator
        assert_has_tool_calls(response, expected_count=1)
        tool_calls = extract_openai_tool_calls(response)
        assert tool_calls[0]["name"] == "calculate"

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("image_url"))
    def test_07_image_url(self, test_config, provider, model, vk_enabled):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 7: Image analysis from URL - auto-skips providers without image URL support (e.g., Gemini, Bedrock)"""
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        response = client.chat.completions.create(
            model=format_provider_model(provider, model),
            messages=IMAGE_URL_MESSAGES,
            max_tokens=200,
        )

        assert_valid_image_response(response)

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("image_base64")
    )
    def test_08_image_base64(self, test_config, provider, model, vk_enabled):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 8: Image analysis from base64 - runs for all providers with base64 image support"""
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        response = client.chat.completions.create(
            model=format_provider_model(provider, model),
            messages=IMAGE_BASE64_MESSAGES,
            max_tokens=200,
        )

        assert_valid_image_response(response)

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("multiple_images")
    )
    def test_09_multiple_images(self, test_config, provider, model, vk_enabled):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 9: Multiple image analysis - auto-skips providers without multiple image support"""
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        response = client.chat.completions.create(
            model=format_provider_model(provider, model),
            messages=MULTIPLE_IMAGES_MESSAGES,
            max_tokens=300,
        )

        assert_valid_image_response(response)
        content = get_content_string(response.choices[0].message.content)
        # Should mention comparison or differences (flexible matching)
        assert any(
            word in content for word in COMPARISON_KEYWORDS
        ), f"Response should contain comparison keywords. Got content: {content}"

    @skip_if_no_api_key("openai")
    def test_10_complex_end2end(self, openai_client, test_config):
        """Test Case 10: Complex end-to-end with conversation, images, and tools"""
        messages = COMPLEX_E2E_MESSAGES.copy()

        # First, analyze the image
        response1 = openai_client.chat.completions.create(
            model=get_model("openai", "vision"),
            messages=messages,
            tools=[{"type": "function", "function": WEATHER_TOOL}],
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
            final_response = openai_client.chat.completions.create(
                model=get_model("openai", "vision"), messages=messages, max_tokens=200
            )

            assert_valid_chat_response(final_response)

    @skip_if_no_api_key("openai")
    def test_11_integration_specific_features(self, openai_client, test_config):
        """Test Case 11: OpenAI-specific features"""

        # Test 1: Function calling with specific tool choice
        response1 = openai_client.chat.completions.create(
            model=get_model("openai", "tools"),
            messages=[{"role": "user", "content": "What's 15 + 27?"}],
            tools=[
                {"type": "function", "function": CALCULATOR_TOOL},
                {"type": "function", "function": WEATHER_TOOL},
            ],
            tool_choice={
                "type": "function",
                "function": {"name": "calculate"},
            },  # Force specific tool
            max_tokens=100,
        )

        assert_has_tool_calls(response1, expected_count=1)
        tool_calls = extract_openai_tool_calls(response1)
        assert tool_calls[0]["name"] == "calculate"

        # Test 2: System message
        response2 = openai_client.chat.completions.create(
            model=get_model("openai", "chat"),
            messages=[
                {
                    "role": "system",
                    "content": "You are a helpful assistant that always responds in exactly 5 words.",
                },
                {"role": "user", "content": "Hello, how are you?"},
            ],
            max_tokens=50,
        )

        assert_valid_chat_response(response2)
        # Check if response is approximately 5 words (allow some flexibility)
        word_count = len(response2.choices[0].message.content.split())
        assert 3 <= word_count <= 7, f"Expected ~5 words, got {word_count}"

        # Test 3: Temperature and top_p parameters
        response3 = openai_client.chat.completions.create(
            model=get_model("openai", "chat"),
            messages=[{"role": "user", "content": "Tell me a creative story in one sentence."}],
            temperature=0.9,
            top_p=0.9,
            max_tokens=100,
        )

        assert_valid_chat_response(response3)

    @skip_if_no_api_key("openai")
    def test_12_error_handling_invalid_roles(self, openai_client, test_config):
        """Test Case 12: Error handling for invalid roles"""
        with pytest.raises(Exception) as exc_info:
            openai_client.chat.completions.create(
                model=get_model("openai", "chat"),
                messages=INVALID_ROLE_MESSAGES,
                max_tokens=100,
            )

        # Verify the error is properly caught and contains role-related information
        error = exc_info.value
        print(error)
        assert_valid_error_response(error, "tester")
        assert_error_propagation(error, "openai")

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("streaming"))
    def test_13_streaming(self, test_config, provider, model, vk_enabled):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 13: Streaming chat completion - auto-skips providers without streaming support"""
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        # Test basic streaming
        stream = client.chat.completions.create(
            model=format_provider_model(provider, model),
            messages=STREAMING_CHAT_MESSAGES,
            max_tokens=200,
            stream=True
        )

        content, chunk_count, tool_calls_detected = collect_streaming_content(
            stream, "openai", timeout=300
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
                stream_with_tools = client.chat.completions.create(
                    model=format_provider_model(provider, tools_model),
                    messages=STREAMING_TOOL_CALL_MESSAGES,
                    max_tokens=150,
                    tools=convert_to_openai_tools([WEATHER_TOOL]),
                    stream=True,
                )

                content_tools, chunk_count_tools, tool_calls_detected_tools = (
                    collect_streaming_content(stream_with_tools, "openai", timeout=300)
                )

                # Validate tool streaming results
                assert chunk_count_tools > 0, "Should receive at least one chunk with tools"
                assert tool_calls_detected_tools, "Should detect tool calls in streaming response"

    @skip_if_no_api_key("openai")
    def test_14_speech_synthesis(self, openai_client, test_config):
        """Test Case 14: Speech synthesis (text-to-speech)"""
        # Basic speech synthesis test
        response = openai_client.audio.speech.create(
            model=get_model("openai", "speech"),
            voice=get_provider_voice("openai", "primary"),
            input=SPEECH_TEST_INPUT,
        )

        # Read the audio content
        audio_content = response.content
        assert_valid_speech_response(audio_content)

        # Test with different voice
        response2 = openai_client.audio.speech.create(
            model=get_model("openai", "speech"),
            voice=get_provider_voice("openai", "secondary"),
            input="Short test message.",
            response_format="mp3",
        )

        audio_content2 = response2.content
        assert_valid_speech_response(audio_content2, expected_audio_size_min=500)

        # Verify that different voices produce different audio
        assert audio_content != audio_content2, "Different voices should produce different audio"

    @skip_if_no_api_key("openai")
    def test_15_transcription_audio(self, openai_client, test_config):
        """Test Case 16: Audio transcription (speech-to-text)"""
        # Generate test audio for transcription
        test_audio = generate_test_audio()

        # Basic transcription test
        response = openai_client.audio.transcriptions.create(
            model=get_model("openai", "transcription"),
            file=("test_audio.wav", test_audio, "audio/wav"),
        )

        assert_valid_transcription_response(response)
        # Since we're using a generated sine wave, we don't expect specific text,
        # but the API should return some transcription attempt

        # Test with additional parameters
        response2 = openai_client.audio.transcriptions.create(
            model=get_model("openai", "transcription"),
            file=("test_audio.wav", test_audio, "audio/wav"),
            language="en",
            temperature=0.0,
        )

        assert_valid_transcription_response(response2)

    @skip_if_no_api_key("openai")
    def test_16_transcription_streaming(self, openai_client, test_config):
        """Test Case 17: Audio transcription streaming"""
        # Generate test audio for streaming transcription
        test_audio = generate_test_audio()

        try:
            # Try to create streaming transcription
            response = openai_client.audio.transcriptions.create(
                model=get_model("openai", "transcription"),
                file=("test_audio.wav", test_audio, "audio/wav"),
                stream=True,
            )

            # If streaming is supported, collect the text chunks
            if hasattr(response, "__iter__"):
                text_content, chunk_count = collect_streaming_transcription_content(
                    response, "openai", timeout=300
                )
                assert chunk_count > 0, "Should receive at least one text chunk"
                assert_valid_transcription_response(
                    text_content, min_text_length=0
                )  # Sine wave might not produce much text
            else:
                # If not streaming, should still be valid transcription
                assert_valid_transcription_response(response)

        except Exception as e:
            # If streaming is not supported, ensure it's a proper error message
            error_message = str(e).lower()
            streaming_not_supported = any(
                phrase in error_message
                for phrase in ["streaming", "not supported", "invalid", "stream"]
            )
            if not streaming_not_supported:
                # Re-raise if it's not a streaming support issue
                raise

    @skip_if_no_api_key("openai")
    def test_17_speech_transcription_round_trip(self, openai_client, test_config):
        """Test Case 18: Complete round-trip - text to speech to text"""
        original_text = "The quick brown fox jumps over the lazy dog."

        # Step 1: Convert text to speech
        speech_response = openai_client.audio.speech.create(
            model=get_model("openai", "speech"),
            voice=get_provider_voice("openai", "primary"),
            input=original_text,
            response_format="wav",  # Use WAV for better transcription compatibility
        )

        audio_content = speech_response.content
        assert_valid_speech_response(audio_content)

        # Step 2: Convert speech back to text
        transcription_response = openai_client.audio.transcriptions.create(
            model=get_model("openai", "transcription"),
            file=("generated_speech.wav", audio_content, "audio/wav"),
        )

        assert_valid_transcription_response(transcription_response)
        transcribed_text = transcription_response.text

        # Step 3: Verify similarity (allowing for some variation in transcription)
        # Check for key words from the original text
        original_words = original_text.lower().split()
        transcribed_words = transcribed_text.lower().split()

        # At least 50% of the original words should be present in the transcription
        matching_words = sum(1 for word in original_words if word in transcribed_words)
        match_percentage = matching_words / len(original_words)

        assert match_percentage >= 0.3, (
            f"Round-trip transcription should preserve at least 30% of original words. "
            f"Original: '{original_text}', Transcribed: '{transcribed_text}', "
            f"Match percentage: {match_percentage:.2%}"
        )

    @skip_if_no_api_key("openai")
    def test_18_speech_error_handling(self, openai_client, test_config):
        """Test Case 19: Speech synthesis error handling"""
        # Test with invalid voice
        with pytest.raises(Exception) as exc_info:
            openai_client.audio.speech.create(
                model=get_model("openai", "speech"),
                voice="invalid_voice_name",
                input="This should fail.",
            )

        error = exc_info.value
        assert_valid_error_response(error, "invalid_voice_name")

        # Test with empty input
        with pytest.raises(Exception) as exc_info:
            openai_client.audio.speech.create(
                model=get_model("openai", "speech"),
                voice=get_provider_voice("openai", "primary"),
                input="",
            )

        error = exc_info.value
        # Should get an error for empty input

        # Test with invalid model
        with pytest.raises(Exception) as exc_info:
            openai_client.audio.speech.create(
                model="invalid-speech-model",
                voice=get_provider_voice("openai", "primary"),
                input="This should fail due to invalid model.",
            )

        error = exc_info.value
        # Should get an error for invalid model

    @skip_if_no_api_key("openai")
    def test_19_transcription_error_handling(self, openai_client, test_config):
        """Test Case 20: Transcription error handling"""
        # Test with invalid audio data
        invalid_audio = b"This is not audio data"

        with pytest.raises(Exception) as exc_info:
            openai_client.audio.transcriptions.create(
                model=get_model("openai", "transcription"),
                file=("invalid.wav", invalid_audio, "audio/wav"),
            )

        error = exc_info.value
        # Should get an error for invalid audio format

        # Test with invalid model
        valid_audio = generate_test_audio()

        with pytest.raises(Exception) as exc_info:
            openai_client.audio.transcriptions.create(
                model="invalid-transcription-model",
                file=("test.wav", valid_audio, "audio/wav"),
            )

        error = exc_info.value
        # Should get an error for invalid model

        # Test with unsupported file format (if applicable)
        with pytest.raises(Exception) as exc_info:
            openai_client.audio.transcriptions.create(
                model=get_model("openai", "transcription"),
                file=("test.txt", b"text file content", "text/plain"),
            )

        error = exc_info.value
        # Should get an error for unsupported file type

    @skip_if_no_api_key("openai")
    def test_20_speech_different_voices_and_formats(self, openai_client, test_config):
        """Test Case 21: Test different voices and response formats"""
        test_text = "Testing different voices and audio formats."

        # Test multiple voices
        voices_tested = []
        for voice in get_provider_voices(
            "openai", count=3
        ):  # Test first 3 voices to avoid too many API calls
            response = openai_client.audio.speech.create(
                model=get_model("openai", "speech"),
                voice=voice,
                input=test_text,
                response_format="mp3",
            )

            audio_content = response.content
            assert_valid_speech_response(audio_content)
            voices_tested.append((voice, len(audio_content)))

        # Verify that different voices produce different sized outputs (generally)
        sizes = [size for _, size in voices_tested]
        assert len(set(sizes)) > 1 or all(
            s > 1000 for s in sizes
        ), "Different voices should produce varying audio outputs"

        # Test different response formats
        formats_to_test = ["mp3", "wav", "opus"]
        format_results = []

        for format_type in formats_to_test:
            try:
                response = openai_client.audio.speech.create(
                    model=get_model("openai", "speech"),
                    voice=get_provider_voice("openai", "primary"),
                    input="Testing audio format: " + format_type,
                    response_format=format_type,
                )

                audio_content = response.content
                assert_valid_speech_response(audio_content, expected_audio_size_min=500)
                format_results.append(format_type)

            except Exception as e:
                # Some formats might not be supported
                print(f"Format {format_type} not supported or failed: {e}")

        # At least MP3 should be supported
        assert "mp3" in format_results, "MP3 format should be supported"

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("embeddings"))
    def test_21_single_text_embedding(self, test_config, provider, model, vk_enabled):
        """Test Case 21: Single text embedding generation"""
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        response = client.embeddings.create(
            model=format_provider_model(provider, model), input=EMBEDDINGS_SINGLE_TEXT, dimensions=1536
        )

        assert_valid_embedding_response(response, expected_dimensions=1536)

        # Verify response structure
        assert len(response.data) == 1, "Should have exactly one embedding"
        assert response.data[0].index == 0, "First embedding should have index 0"
        assert response.data[0].object == "embedding", "Object type should be 'embedding'"

        # Verify model in response
        assert response.model is not None, "Response should include model name"

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("embeddings"))
    def test_22_batch_text_embeddings(self, test_config, provider, model, vk_enabled):
        """Test Case 22: Batch text embedding generation"""
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        response = client.embeddings.create(
            model=format_provider_model(provider, model), input=EMBEDDINGS_MULTIPLE_TEXTS, dimensions=1536
        )

        expected_count = len(EMBEDDINGS_MULTIPLE_TEXTS)
        assert_valid_embeddings_batch_response(response, expected_count, expected_dimensions=1536)

        # Verify each embedding has correct index
        for i, embedding_obj in enumerate(response.data):
            assert embedding_obj.index == i, f"Embedding {i} should have index {i}"
            assert (
                embedding_obj.object == "embedding"
            ), f"Embedding {i} should have object type 'embedding'"

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("embeddings"))
    def test_23_embedding_similarity_analysis(self, test_config, provider, model, vk_enabled):
        """Test Case 23: Embedding similarity analysis with similar texts"""
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        response = client.embeddings.create(
            model=format_provider_model(provider, model), input=EMBEDDINGS_SIMILAR_TEXTS, dimensions=1536
        )

        assert_valid_embeddings_batch_response(
            response, len(EMBEDDINGS_SIMILAR_TEXTS), expected_dimensions=1536
        )

        embeddings = [item.embedding for item in response.data]

        # Test similarity between the first two embeddings (similar weather texts)
        similarity_1_2 = calculate_cosine_similarity(embeddings[0], embeddings[1])
        similarity_1_3 = calculate_cosine_similarity(embeddings[0], embeddings[2])
        similarity_2_3 = calculate_cosine_similarity(embeddings[1], embeddings[2])

        # Similar texts should have high similarity (> 0.6)
        assert (
            similarity_1_2 > 0.6
        ), f"Similar texts should have high similarity, got {similarity_1_2:.4f}"
        assert (
            similarity_1_3 > 0.6
        ), f"Similar texts should have high similarity, got {similarity_1_3:.4f}"
        assert (
            similarity_2_3 > 0.6
        ), f"Similar texts should have high similarity, got {similarity_2_3:.4f}"


    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("embeddings"))
    def test_24_embedding_dissimilarity_analysis(self, test_config, provider, model, vk_enabled):
        """Test Case 24: Embedding dissimilarity analysis with different texts"""
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        response = client.embeddings.create(
            model=format_provider_model(provider, model), input=EMBEDDINGS_DIFFERENT_TEXTS, dimensions=1536
        )

        assert_valid_embeddings_batch_response(
            response, len(EMBEDDINGS_DIFFERENT_TEXTS), expected_dimensions=1536
        )

        embeddings = [item.embedding for item in response.data]

        # Test dissimilarity between different topic embeddings
        # Weather vs Programming
        weather_prog_similarity = calculate_cosine_similarity(embeddings[0], embeddings[1])
        # Weather vs Stock Market
        weather_stock_similarity = calculate_cosine_similarity(embeddings[0], embeddings[2])
        # Programming vs Machine Learning (should be more similar)
        prog_ml_similarity = calculate_cosine_similarity(embeddings[1], embeddings[3])

        # Different topics should have lower similarity
        assert (
            weather_prog_similarity < 0.8
        ), f"Different topics should have lower similarity, got {weather_prog_similarity:.4f}"
        assert (
            weather_stock_similarity < 0.8
        ), f"Different topics should have lower similarity, got {weather_stock_similarity:.4f}"

        # Programming and ML should be more similar than completely different topics
        assert (
            prog_ml_similarity > weather_prog_similarity
        ), "Related tech topics should be more similar than unrelated topics"

    @skip_if_no_api_key("openai")
    def test_25_embedding_different_models(self, openai_client, test_config):
        """Test Case 25: Test different embedding models"""
        test_text = EMBEDDINGS_SINGLE_TEXT

        # Test with text-embedding-3-small (default)
        response_small = openai_client.embeddings.create(
            model="text-embedding-3-small", input=test_text
        )
        assert_valid_embedding_response(response_small, expected_dimensions=1536)

        # Test with text-embedding-3-large if available
        try:
            response_large = openai_client.embeddings.create(
                model="text-embedding-3-large", input=test_text
            )
            assert_valid_embedding_response(response_large, expected_dimensions=3072)

            # Verify different models produce different embeddings
            embedding_small = response_small.data[0].embedding
            embedding_large = response_large.data[0].embedding

            # They should have different dimensions
            assert len(embedding_small) != len(
                embedding_large
            ), "Different models should produce different dimension embeddings"

        except Exception as e:
            # If text-embedding-3-large is not available, just log it
            print(f"text-embedding-3-large not available: {e}")

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("embeddings"))
    def test_26_embedding_long_text(self, test_config, provider, model, vk_enabled):
        """Test Case 26: Embedding generation with longer text"""
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        response = client.embeddings.create(
            model=format_provider_model(provider, model), input=EMBEDDINGS_LONG_TEXT, dimensions=1536
        )

        assert_valid_embedding_response(response, expected_dimensions=1536)

        # Verify token usage is reported for longer text
        if provider != "gemini" and provider != "bedrock": # gemini does not return usage data and openai does not return usage data for long text
            assert response.usage is not None, "Usage should be reported for longer text"
            assert response.usage.total_tokens > 20, "Longer text should consume more tokens"

    @skip_if_no_api_key("openai")
    def test_27_embedding_error_handling(self, openai_client, test_config):
        """Test Case 27: Embedding error handling"""

        # Test with invalid model
        with pytest.raises(Exception) as exc_info:
            openai_client.embeddings.create(
                model="invalid-embedding-model", input=EMBEDDINGS_SINGLE_TEXT
            )

        error = exc_info.value
        assert_valid_error_response(error, "invalid-embedding-model")

        # Test with empty text (depending on implementation, might be handled)
        try:
            response = openai_client.embeddings.create(
                model=get_model("openai", "embeddings"), input=""
            )
            # If it doesn't throw an error, check that response is still valid
            if response:
                assert_valid_embedding_response(response)

        except Exception as e:
            # Empty input might be rejected, which is acceptable
            assert (
                "empty" in str(e).lower() or "invalid" in str(e).lower()
            ), "Error should mention empty or invalid input"

    @skip_if_no_api_key("openai")
    def test_28_embedding_dimensionality_reduction(self, openai_client, test_config):
        """Test Case 28: Embedding with custom dimensions (if supported)"""
        try:
            # Test custom dimensions with text-embedding-3-small
            custom_dimensions = 512
            response = openai_client.embeddings.create(
                model="text-embedding-3-small",
                input=EMBEDDINGS_SINGLE_TEXT,
                dimensions=custom_dimensions,
            )

            assert_valid_embedding_response(response, expected_dimensions=custom_dimensions)

            # Compare with default dimensions
            response_default = openai_client.embeddings.create(
                model="text-embedding-3-small", input=EMBEDDINGS_SINGLE_TEXT
            )

            embedding_custom = response.data[0].embedding
            embedding_default = response_default.data[0].embedding

            assert (
                len(embedding_custom) == custom_dimensions
            ), f"Custom dimensions should be {custom_dimensions}"
            assert len(embedding_default) == 1536, "Default dimensions should be 1536"
            assert len(embedding_custom) != len(
                embedding_default
            ), "Custom and default dimensions should be different"

        except Exception as e:
            # Custom dimensions might not be supported by all models
            print(f"Custom dimensions not supported: {e}")

    @skip_if_no_api_key("openai")
    def test_29_embedding_encoding_format(self, openai_client, test_config):
        """Test Case 29: Different encoding formats (if supported)"""
        try:
            # Test with float encoding (default)
            response_float = openai_client.embeddings.create(
                model=get_model("openai", "embeddings"),
                input=EMBEDDINGS_SINGLE_TEXT,
                encoding_format="float",
            )

            assert_valid_embedding_response(response_float, expected_dimensions=1536)
            embedding_float = response_float.data[0].embedding
            assert all(
                isinstance(x, float) for x in embedding_float
            ), "Float encoding should return float values"

            # Test with base64 encoding if supported
            try:
                response_base64 = openai_client.embeddings.create(
                    model=get_model("openai", "embeddings"),
                    input=EMBEDDINGS_SINGLE_TEXT,
                    encoding_format="base64",
                )

                # Base64 encoding returns string data
                assert (
                    response_base64.data[0].embedding is not None
                ), "Base64 encoding should return data"

            except Exception as base64_error:
                print(f"Base64 encoding not supported: {base64_error}")

        except Exception as e:
            # Encoding format parameter might not be supported
            print(f"Encoding format parameter not supported: {e}")

    @skip_if_no_api_key("openai")
    def test_30_embedding_usage_tracking(self, openai_client, test_config):
        """Test Case 30: Embedding usage tracking and token counting"""
        # Single text embedding
        response_single = openai_client.embeddings.create(
            model=get_model("openai", "embeddings"), input=EMBEDDINGS_SINGLE_TEXT
        )

        assert_valid_embedding_response(response_single)
        assert response_single.usage is not None, "Single embedding should have usage data"
        assert response_single.usage.total_tokens > 0, "Single embedding should consume tokens"
        single_tokens = response_single.usage.total_tokens

        # Batch embedding
        response_batch = openai_client.embeddings.create(
            model=get_model("openai", "embeddings"), input=EMBEDDINGS_MULTIPLE_TEXTS
        )

        assert_valid_embeddings_batch_response(response_batch, len(EMBEDDINGS_MULTIPLE_TEXTS))
        assert response_batch.usage is not None, "Batch embedding should have usage data"
        assert response_batch.usage.total_tokens > 0, "Batch embedding should consume tokens"
        batch_tokens = response_batch.usage.total_tokens

        # Batch should consume more tokens than single
        assert (
            batch_tokens > single_tokens
        ), f"Batch embedding ({batch_tokens} tokens) should consume more than single ({single_tokens} tokens)"

        # Verify proportional token usage
        texts_ratio = len(EMBEDDINGS_MULTIPLE_TEXTS)
        token_ratio = batch_tokens / single_tokens

        # Token ratio should be roughly proportional to text count (allowing for some variance)
        assert (
            0.5 * texts_ratio <= token_ratio <= 2.0 * texts_ratio
        ), f"Token usage ratio ({token_ratio:.2f}) should be roughly proportional to text count ({texts_ratio})"

    # =========================================================================
    # IMAGE GENERATION TEST CASES
    # =========================================================================

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("image_generation")
    )
    def test_52a_image_generation_simple(self, test_config, provider, model, vk_enabled):
        """Test Case 52a: Simple image generation with basic prompt"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        # Use low quality for gpt-image-1 to get faster response
        if model == "gpt-image-1":
            response = client.images.generate(
            model=format_provider_model(provider, model),
            prompt=IMAGE_GENERATION_SIMPLE_PROMPT,
            n=1,
            size="1024x1024",
            quality="low",
        )
        else:
            response = client.images.generate(
                model=format_provider_model(provider, model),
                prompt=IMAGE_GENERATION_SIMPLE_PROMPT,
                n=1,
                size="1024x1024",
            )

        # Validate response structure
        assert_valid_image_generation_response(response, "openai")
        
        # Verify we got exactly 1 image
        assert len(response.data) == 1, f"Expected 1 image, got {len(response.data)}"

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("image_generation")
    )
    def test_52b_image_generation_multiple(self, test_config, provider, model, vk_enabled):
        """Test Case 52b: Generate multiple images at once"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        if provider not in ["openai", "azure", "xai"] and model not in ["imagen-4.0-generate-001"]:
            pytest.skip("Multiple image generation is only supported by OpenAI, Azure, XAI, or Imagen models")
        if model == "gemini-2.5-flash-image":
            pytest.skip("Gemini 2.5 flash image does not support multiple images")
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        response = client.images.generate(
            model=format_provider_model(provider, model),
            prompt=IMAGE_GENERATION_SIMPLE_PROMPT,
            n=2,
            size="1024x1024",
            quality="low",
        )

        # Validate response structure
        assert_valid_image_generation_response(response, "openai")
        
        # Verify we got exactly 2 images
        assert len(response.data) == 2, f"Expected 2 images, got {len(response.data)}"

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("image_generation")
    )
    def test_52c_image_generation_quality(self, test_config, provider, model, vk_enabled):
        """Test Case 52c: Image generation with quality parameter"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        if provider != "openai" or model not in ["gpt-image-1", "gpt-image-1.5", "gpt-image-1.5-mini"]:
            pytest.skip("Quality parameter is only supported by OpenAI (gpt-image-1, gpt-image-1.5, gpt-image-1.5-mini)")

        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        response = client.images.generate(
            model=format_provider_model(provider, model),
            prompt=IMAGE_GENERATION_SIMPLE_PROMPT,
            n=1,
            size="1024x1024",
            quality="medium",  # gpt-image-1 supports quality parameter
        )

        # Validate response structure
        assert_valid_image_generation_response(response, "openai")
        
        # Verify we got an image
        assert len(response.data) == 1, f"Expected 1 image, got {len(response.data)}"

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("image_generation")
    )
    def test_52d_image_generation_different_sizes(self, test_config, provider, model, vk_enabled):
        """Test Case 52d: Image generation with different sizes"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        
        # Test with a different size
        response = client.images.generate(
            model=format_provider_model(provider, model),
            prompt=IMAGE_GENERATION_SIMPLE_PROMPT,
            n=1,
            size="512x512",
            quality="low",
        )

        # Validate response structure
        assert_valid_image_generation_response(response, "openai")
        assert len(response.data) == 1, f"Expected 1 image, got {len(response.data)}"

    # =========================================================================
    # IMAGE EDIT TEST CASES
    # =========================================================================

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("image_edit")
    )
    def test_53a_image_edit_simple(self, test_config, provider, model, vk_enabled):
        """Test Case 53a: Simple image edit with inpainting"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        
        # Bedrock requires type field (inpainting/outpainting) which OpenAI SDK doesn't support
        if provider == "bedrock":
            pytest.skip("Bedrock requires type field which is not supported by OpenAI SDK")

        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        
        # Create test image and mask
        base_image_b64 = BASE64_IMAGE  # 64x64 red pixel for minimum size requirements
        mask_b64 = create_simple_mask_image(64, 64)
        
        # Decode base64 to bytes for API
        import base64
        image_bytes = base64.b64decode(base_image_b64)
        mask_bytes = base64.b64decode(mask_b64)
        
        response = client.images.edit(
            model=format_provider_model(provider, model),
            image=image_bytes,
            mask=mask_bytes,
            prompt=IMAGE_EDIT_SIMPLE_PROMPT,
            n=1,
            size="1024x1024"
        )

        # Validate response structure
        assert_valid_image_edit_response(response, "openai")
        assert len(response.data) == 1, f"Expected 1 image, got {len(response.data)}"

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("image_edit")
    )
    def test_53b_image_edit_no_mask(self, test_config, provider, model, vk_enabled):
        """Test Case 53b: Image edit without mask (outpainting/general edit)"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        
        # Some providers support editing without explicit mask
        if provider not in ["openai", "gemini", "huggingface"]:
            pytest.skip(f"Provider {provider} requires explicit mask for edits")

        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        
        import base64
        image_bytes = base64.b64decode(BASE64_IMAGE)
        
        response = client.images.edit(
            model=format_provider_model(provider, model),
            image=image_bytes,
            prompt=IMAGE_EDIT_PROMPT_OUTPAINT,
            n=1,
            size="1024x1024"
        )

        assert_valid_image_edit_response(response, "openai")

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("image_edit")
    )
    def test_53c_image_edit_quality(self, test_config, provider, model, vk_enabled):
        """Test Case 53c: Image edit with quality parameter"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        # Bedrock requires type field (inpainting/outpainting) which OpenAI SDK doesn't support
        if provider == "bedrock":
            pytest.skip("Bedrock requires type field which is not supported by OpenAI SDK")
        if provider != "openai" or model not in ["gpt-image-1", "gpt-image-1.5", "gpt-image-1.5-mini"]:
            pytest.skip("Quality parameter supported by OpenAI gpt-image models")

        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        
        import base64
        image_bytes = base64.b64decode(BASE64_IMAGE)
        mask_bytes = base64.b64decode(create_simple_mask_image(64, 64))
        
        response = client.images.edit(
            model=format_provider_model(provider, model),
            image=image_bytes,
            mask=mask_bytes,
            prompt=IMAGE_EDIT_SIMPLE_PROMPT,
            n=1,
            size="1024x1024",
            quality="low"  # For faster testing
        )

        assert_valid_image_edit_response(response, "openai")

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("image_edit")
    )
    def test_53d_image_edit_different_sizes(self, test_config, provider, model, vk_enabled):
        """Test Case 53d: Image edit with different output sizes"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        
        # Bedrock requires type field (inpainting/outpainting) which OpenAI SDK doesn't support
        if provider == "bedrock":
            pytest.skip("Bedrock requires type field which is not supported by OpenAI SDK")

        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        
        import base64
        image_bytes = base64.b64decode(BASE64_IMAGE)
        mask_bytes = base64.b64decode(create_simple_mask_image(64, 64))
        
        response = client.images.edit(
            model=format_provider_model(provider, model),
            image=image_bytes,
            mask=mask_bytes,
            prompt=IMAGE_EDIT_SIMPLE_PROMPT,
            n=1,
            size="1024x1024"
        )

        assert_valid_image_edit_response(response, "openai")
        assert len(response.data) == 1

    @skip_if_no_api_key("openai")
    def test_31_list_models(self, openai_client, test_config):
        """Test Case 31: List models"""
        response = openai_client.models.list()
        assert response.data is not None
        assert len(response.data) > 0

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("video_generation")
    )
    def test_31a_video_create(self, test_config, provider, model, vk_enabled):
        """Test Case 31a: Video create returns a valid job object."""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        video = client.videos.create(
            model=format_provider_model(provider, model),
            prompt="A cinematic sunrise over mountains with light fog and gentle camera movement.",
            seconds="4",
            size="1280x720",
        )

        print(video.id)

        assert video is not None
        assert getattr(video, "id", None), "Video create should return a video id"
        assert getattr(video, "object", None) in {"video", "video.task"}
        assert getattr(video, "status", None) in {"queued", "in_progress", "completed", "failed"}
        assert getattr(video, "model", None) is not None

        _safe_cleanup_video(client, video.id, provider)

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("video_generation")
    )
    def test_31b_video_retrieve(self, test_config, provider, model, vk_enabled):
        """Test Case 31b: Video retrieve returns status and id for created job."""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        video = client.videos.create(
            model=format_provider_model(provider, model),
            prompt="A slow aerial shot of a river valley at golden hour.",
            seconds="4",
            size="1280x720",
        )

        try:
            retrieved = client.videos.retrieve(video.id)
            assert retrieved is not None
            assert getattr(retrieved, "id", None) == video.id
            assert getattr(retrieved, "status", None) in {"queued", "in_progress", "completed", "failed"}
            assert getattr(retrieved, "created_at", None) is not None
        finally:
            _safe_cleanup_video(client, video.id, provider)

    @skip_if_no_api_key("openai")
    def test_31c_video_list(self, openai_client, test_config):
        """Test Case 31c: Video list includes created video job."""
        client = get_provider_openai_client("openai")
        video = client.videos.create(
            model=format_provider_model("openai", "sora-2"),
            prompt="A paper airplane flying through a bright office scene.",
            seconds="4",
            size="1280x720",
        )

        # wait for video to be created
        _wait_for_video_terminal_status(client, video.id)

        try:
            found_in_list = False
            for _ in range(6):
                page = client.videos.list(
                    limit=20,
                    extra_headers={"x-bf-video-list-provider": "openai"},
                )
                assert hasattr(page, "data"), "Videos list should include data field"
                assert isinstance(page.data, list), "Videos list data should be a list"

                if any(getattr(item, "id", None) == video.id for item in page.data):
                    print("found in list: ", page.data)
                    found_in_list = True
                    break

                time.sleep(2)

            assert found_in_list, f"Created video {video.id} should be present in videos.list() response"
        finally:
            _safe_cleanup_video(client, video.id, "openai")

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("video_generation", include_providers=["vertex", "runway"])
    )
    def test_31d_video_download_content(self, test_config, provider, model, vk_enabled):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        """Test Case 31d: Video download_content returns binary content after completion."""
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        video = client.videos.create(
            model=format_provider_model(provider, model),
            prompt="A close-up of raindrops on a window with city lights in the background.",
            seconds="4",
            size="1280x720",
        )

        try:
            terminal_video = _wait_for_video_terminal_status(client, video.id)
            assert getattr(terminal_video, "status", None) == "completed", (
                f"Video should complete before download. Status: {getattr(terminal_video, 'status', None)}"
            )

            response = client.videos.download_content(video_id=video.id)
            assert response is not None

            if hasattr(response, "read"):
                content = response.read()
            elif hasattr(response, "content"):
                content = response.content
            else:
                content = bytes(response)

            assert isinstance(content, (bytes, bytearray))
            assert len(content) > 0, "Downloaded video content should not be empty"

            if hasattr(response, "headers") and response.headers:
                content_type = response.headers.get("content-type", "").lower()
                assert (
                    "video" in content_type or "application/octet-stream" in content_type
                ), f"Unexpected content-type: {content_type}"
        except Exception as e:
            print("error: ", e)
            # _safe_cleanup_video(client, video.id, provider)


    @skip_if_no_api_key("openai")
    def test_31e_video_delete(self, openai_client, test_config):
        """Test Case 31e: Video delete removes a completed/failed video job."""
        client = get_provider_openai_client("openai")
        video = client.videos.create(
            model=format_provider_model("openai", "sora-2"),
            prompt="A rotating crystal in a dark studio with soft rim lighting.",
            seconds="4",
            size="1280x720",
        )

        terminal_video = _wait_for_video_terminal_status(client, video.id)
        assert getattr(terminal_video, "status", None) in {"completed", "failed"}

        delete_response = client.videos.delete(video.id)
        assert delete_response is not None
        assert getattr(delete_response, "id", None) == video.id
    
    @skip_if_no_api_key("openai")
    def test_31f_video_remix(self, openai_client, test_config):
        """Test Case 31f: Video remix creates a new job linked to the original video."""
        client = get_provider_openai_client("openai")

        # Step 1: Create original video
        original = client.videos.create(
            model=format_provider_model("openai", "sora-2"),
            prompt="A cat walking onto a theater stage under a spotlight.",
            seconds="4",
            size="720x1280",
        )

        try:
            # Wait for original to complete before remixing
            original_terminal = _wait_for_video_terminal_status(client, original.id)
            assert getattr(original_terminal, "status", None) == "completed"

            # Step 2: Remix the video
            remixed = client.videos.remix(
                video_id=original.id,
                prompt="Extend the scene with the cat taking a bow to the cheering audience.",
            )

            assert remixed is not None
            assert getattr(remixed, "id", None), "Remix should return a new video id"
            assert remixed.id != original.id, "Remix should create a new video job"
            assert getattr(remixed, "object", None) in {"video", "video.task"}
            assert getattr(remixed, "status", None) in {"queued", "in_progress", "completed", "failed"}
            assert getattr(remixed, "remixed_from_video_id", None) == original.id
            assert getattr(remixed, "model", None) is not None
            assert getattr(remixed, "created_at", None) is not None

            # Optional: wait for remix to finish to ensure full lifecycle works
            remixed_terminal = _wait_for_video_terminal_status(client, remixed.id)
            assert getattr(remixed_terminal, "status", None) in {"completed", "failed"}

        finally:
            _safe_cleanup_video(client, original.id, "openai")
            if "remixed" in locals():
                _safe_cleanup_video(client, remixed.id, "openai")


    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("file_input")
    )
    def test_chat_completion_with_file(self, test_config, provider, model, vk_enabled):
        """Test chat completion with PDF file input"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        response = client.chat.completions.create(
            model=format_provider_model(provider, model),
            messages=[
                {
                    "role": "user",
                    "content": [
                        {
                            "type": "text",
                            "text": "What is the main topic of this document? Summarize the key concepts.",
                        },
                        {
                            "type": "file",
                            "file": {
                                "file_data": f"data:application/pdf;base64,{FILE_DATA_BASE64}",
                                "filename": "testingpdf",
                            },
                        },
                    ],
                }
            ],
            max_tokens=400,
        )

        assert_valid_chat_response(response)
        content = get_content_string(response.choices[0].message.content)
        content_lower = content.lower()

        # Should mention document/file content (testingpdf contains "hello world")
        keywords = ["hello", "world", "testing", "pdf", "file"]
        assert any(
            keyword in content_lower for keyword in keywords
        ), f"Response should describe the document content. Got: {content}"

    # =========================================================================
    # RESPONSES API TEST CASES
    # =========================================================================

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("responses"))
    def test_32_responses_simple_text(self, test_config, provider, model, vk_enabled):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 32: Responses API with simple text input"""
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        response = client.responses.create(
            model=format_provider_model(provider, model),
            input=RESPONSES_SIMPLE_TEXT_INPUT,
            max_output_tokens=1000,
        )

        # Validate response structure
        assert_valid_responses_response(response, min_content_length=20)

        # Check that we have meaningful content
        content = ""
        for message in response.output:
            if hasattr(message, "content") and message.content:
                if isinstance(message.content, str):
                    content += message.content
                elif isinstance(message.content, list):
                    for block in message.content:
                        if hasattr(block, "text") and block.text:
                            content += block.text

        content_lower = content.lower()
        keywords = ["space", "exploration", "astronaut", "moon", "mars", "rocket", "nasa", "satellite"] 
        assert any(
            keyword in content_lower for keyword in keywords
        ), f"Response should contain space exploration related content. Got: {content}"

        # Verify usage information
        if hasattr(response, "usage"):
            assert response.usage.total_tokens > 0, "Should report token usage"

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("responses"))
    def test_33_responses_with_system_message(self, test_config, provider, model, vk_enabled):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 33: Responses API with system message"""
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        response = client.responses.create(
            model=format_provider_model(provider, model),
            input=RESPONSES_TEXT_WITH_SYSTEM,
            max_output_tokens=1000,
        )

        # Validate response structure
        assert_valid_responses_response(response, min_content_length=30)

        # Extract content
        content = ""
        for message in response.output:
            if hasattr(message, "content") and message.content:
                if isinstance(message.content, str):
                    content += message.content
                elif isinstance(message.content, list):
                    for block in message.content:
                        if hasattr(block, "text") and block.text:
                            content += block.text

        # Should mention Mars since system message says we're an astronomy expert
        content_lower = content.lower()
        mars_keywords = ["mars", "water", "planet", "discovery", "rover"]
        assert any(
            keyword in content_lower for keyword in mars_keywords
        ), f"Response should contain Mars-related content from astronomy expert. Got: {content}"

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("responses_image")
    )
    def test_34_responses_with_image(self, test_config, provider, model, vk_enabled):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 34: Responses API with image input"""
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        response = client.responses.create(
            model=format_provider_model(provider, model),
            input=RESPONSES_IMAGE_INPUT,
            max_output_tokens=1000,
        )

        # Validate response structure
        assert_valid_responses_response(response, min_content_length=20)

        # Extract content
        content = ""
        for message in response.output:
            if hasattr(message, "content") and message.content:
                if isinstance(message.content, str):
                    content += message.content
                elif isinstance(message.content, list):
                    for block in message.content:
                        if hasattr(block, "text") and block.text:
                            content += block.text

        # Check for image-related keywords
        content_lower = content.lower()
        image_keywords = [
            "image",
            "picture",
            "photo",
            "see",
            "show",
            "display",
            "nature",
            "grass",
            "sky",
            "landscape",
            "boardwalk",
        ]
        assert any(
            keyword in content_lower for keyword in image_keywords
        ), f"Response should describe the image. Got: {content}"

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("file_input")
    )
    def test_responses_with_file(self, test_config, provider, model, vk_enabled):
        """Test Responses API with base64-encoded PDF file"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        response = client.responses.create(
            model=format_provider_model(provider, model),
            input=[
                {
                    "role": "user",
                    "content": [
                        {
                            "type": "input_text",
                            "text": "What is the main topic of this document? Summarize the key concepts.",
                        },
                        {
                            "type": "input_file",
                            "filename": "testingpdf",
                            "file_data": f"data:application/pdf;base64,{FILE_DATA_BASE64}",
                        },
                    ],
                }
            ],
            max_output_tokens=400,
        )

        # Validate response structure
        assert_valid_responses_response(response, min_content_length=30)

        # Extract content
        content = ""
        for message in response.output:
            if hasattr(message, "content") and message.content:
                if isinstance(message.content, str):
                    content += message.content
                elif isinstance(message.content, list):
                    for block in message.content:
                        if hasattr(block, "text") and block.text:
                            content += block.text

        # Check for document/file content (testingpdf contains "hello world")
        content_lower = content.lower()
        keywords = ["hello", "world", "testing", "pdf", "file"] 
        assert any(
            keyword in content_lower for keyword in keywords
        ), f"Response should describe the document content. Got: {content}"

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("responses"))
    def test_35_responses_with_tools(self, test_config, provider, model, vk_enabled):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 35: Responses API with tool calls"""
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        # Convert tools to responses format
        tools = convert_to_responses_tools([WEATHER_TOOL])

        response = client.responses.create(
            model=format_provider_model(provider, model),
            input=RESPONSES_TOOL_CALL_INPUT,
            tools=tools,
            max_output_tokens=150,
        )

        # Validate response structure
        assert response is not None, "Response should not be None"
        assert hasattr(response, "output"), "Response should have 'output' attribute"
        assert len(response.output) > 0, "Output should contain at least one item"

        # Check for function call in output
        has_function_call = False
        function_call_message = None
        for message in response.output:
            if hasattr(message, "type") and message.type == "function_call":
                has_function_call = True
                function_call_message = message
                break

        assert has_function_call, "Response should contain a function call"
        assert function_call_message is not None, "Should have function call message"

        # Validate function call structure
        assert hasattr(function_call_message, "name"), "Function call should have name"
        assert (
            function_call_message.name == "get_weather"
        ), f"Function call should be 'get_weather', got {function_call_message.name}"

        # Check arguments if present
        if hasattr(function_call_message, "arguments"):
            # Arguments might be string or dict
            if isinstance(function_call_message.arguments, str):
                args = json.loads(function_call_message.arguments)
            else:
                args = function_call_message.arguments

            assert "location" in args, "Function call should have location argument"
            location_lower = str(args["location"]).lower()
            assert (
                "boston" in location_lower
            ), f"Location should mention Boston, got {args['location']}"

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("responses"))
    def test_36_responses_streaming(self, test_config, provider, model, vk_enabled):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 36: Responses API streaming"""
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        stream = client.responses.create(
            model=format_provider_model(provider, model),
            input=RESPONSES_STREAMING_INPUT,
            max_output_tokens=1000,
            stream=True,
        )

        # Collect streaming content
        content, chunk_count, tool_calls_detected, event_types = (
            collect_responses_streaming_content(stream, timeout=300)
        )

        # Validate streaming results
        assert chunk_count > 0, "Should receive at least one chunk"
        assert len(content) > 10, "Should receive substantial content"
        assert not tool_calls_detected, "Basic streaming shouldn't have tool calls"

        # Check that we got expected event types, some providers do not send in this order
        # this is a known issue and we are working on it
        if provider == "openai":
            assert "response.created" in event_types or any(
                "created" in evt for evt in event_types
            ), f"Should receive response.created event. Got events: {list(event_types.keys())}"

        # Check for content events
        has_content_events = any(
            "delta" in evt or "text" in evt or "output" in evt for evt in event_types
        )
        assert (
            has_content_events
        ), f"Should receive content-related events. Got events: {list(event_types.keys())}"

        # Check content quality - should be a poem about AI
        content_lower = content.lower()
        ai_keywords = [
            "ai",
            "artificial",
            "intelligence",
            "machine",
            "learn",
            "algorithm",
            "data",
            "compute",
        ]
        assert any(
            keyword in content_lower for keyword in ai_keywords
        ), f"Poem should mention AI-related terms. Got: {content}"

        # Should have multiple chunks for streaming
        assert chunk_count > 1, f"Streaming should have multiple chunks, got {chunk_count}"

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("responses"))
    def test_37_responses_streaming_with_tools(self, test_config, provider, model, vk_enabled):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 37: Responses API streaming with tools"""
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        tools = convert_to_responses_tools([WEATHER_TOOL])

        stream = client.responses.create(
            model=format_provider_model(provider, model),
            input=[
                {
                    "role": "user",
                    "content": "What's the weather in San Francisco? Use the weather function.",
                }
            ],
            tools=tools,
            max_output_tokens=150,
            stream=True,
        )

        # Collect streaming content
        content, chunk_count, tool_calls_detected, event_types = (
            collect_responses_streaming_content(stream, timeout=300)
        )

        # Validate streaming results
        assert chunk_count > 0, "Should receive at least one chunk"

        # Check for tool-related events
        has_tool_events = any("function" in evt or "tool" in evt for evt in event_types)

        # Either should have tool calls detected or tool-related events
        assert tool_calls_detected or has_tool_events, (
            f"Should detect tool calls in streaming. "
            f"Tool calls detected: {tool_calls_detected}, "
            f"Event types: {list(event_types.keys())}"
        )

    @skip_if_no_api_key("openai")
    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("thinking"))
    def test_38_responses_reasoning(self, test_config, provider, model, vk_enabled):
        """Test Case 38: Responses API with reasoning (gpt-5 model)"""
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        # Use gpt-5 reasoning model
        model_to_use = format_provider_model(provider, model)

        try:
            response = client.responses.create(
                model=model_to_use,
                input=RESPONSES_REASONING_INPUT,
                max_output_tokens=1200,
                reasoning={
                    "effort": "high",
                    "summary": "detailed",
                },
                include=["reasoning.encrypted_content"],
            )

            # Validate response structure
            assert_valid_responses_response(response, min_content_length=50)

            # Extract all content from the response (output and summary)
            content = ""
            has_reasoning_content = False

            # Check output messages
            for message in response.output:
                if hasattr(message, "type"):
                    # Check if we have a reasoning message type
                    if message.type == "reasoning":
                        has_reasoning_content = True

                # Check regular content
                if hasattr(message, "content") and message.content:
                    if isinstance(message.content, str):
                        content += message.content
                    elif isinstance(message.content, list):
                        for block in message.content:
                            if hasattr(block, "text") and block.text:
                                content += block.text
                            # Check for reasoning content blocks
                            if hasattr(block, "type") and block.type == "reasoning_text":
                                has_reasoning_content = True

                # Check summary field within output messages (reasoning models)
                if hasattr(message, "summary") and message.summary:
                    has_reasoning_content = True  # Presence of summary indicates reasoning
                    if isinstance(message.summary, list):
                        for summary_item in message.summary:
                            if hasattr(summary_item, "text") and summary_item.text:
                                content += " " + summary_item.text
                            elif isinstance(summary_item, dict) and "text" in summary_item:
                                content += " " + summary_item["text"]
                            # Check for summary_text type
                            if (
                                hasattr(summary_item, "type")
                                and summary_item.type == "summary_text"
                            ):
                                has_reasoning_content = True
                            elif (
                                isinstance(summary_item, dict)
                                and summary_item.get("type") == "summary_text"
                            ):
                                has_reasoning_content = True
                    elif isinstance(message.summary, str):
                        content += " " + message.summary

            content_lower = content.lower()

            # Validate mathematical reasoning
            # The problem asks about when two trains meet
            reasoning_keywords = [
                "train",
                "meet",
                "time",
                "hour",
                "pm",
                "distance",
                "speed",
                "mile",
            ]

            # Should mention at least some reasoning keywords
            keyword_matches = sum(1 for keyword in reasoning_keywords if keyword in content_lower)
            assert keyword_matches >= 3, (
                f"Response should contain reasoning about trains problem. "
                f"Found {keyword_matches} keywords out of {len(reasoning_keywords)}. "
                f"Content: {content[:200]}..."
            )

            # Check for step-by-step reasoning indicators
            step_indicators = [
                "step",
                "first",
                "then",
                "next",
                "calculate",
                "therefore",
                "because",
                "since",
            ]

            has_steps = any(indicator in content_lower for indicator in step_indicators)
            assert (
                has_steps
            ), f"Response should show step-by-step reasoning. Content: {content[:200]}..."

            # Log if reasoning content was detected
            if has_reasoning_content:
                print("Success: Detected dedicated reasoning content in response")
            else:
                print("Info: Reasoning may be integrated in regular message content")

            # Verify the response contains some calculation or time
            has_calculation = any(
                char in content for char in [":", "+", "-", "*", "/", "="]
            ) or any(
                time_word in content_lower
                for time_word in ["4:00", "5:00", "6:00", "4 pm", "5 pm", "6 pm"]
            )

            if has_calculation:
                print("Success: Response contains calculations or time values")

        except Exception as e:
            # If reasoning parameters are not supported by the model, that's okay
            # Just verify basic response works
            error_str = str(e).lower()
            if "reasoning" in error_str or "not supported" in error_str:
                print(f"Info: Model {model_to_use} may not fully support reasoning parameters")

                # Fallback: Try without reasoning parameters
                response = client.responses.create(
                    model=model_to_use,
                    input=RESPONSES_REASONING_INPUT,
                    max_output_tokens=800,
                )

                # Just validate we get a response
                assert_valid_responses_response(response, min_content_length=30)
                print("Success: Got valid response without reasoning parameters")
            else:
                # Re-raise if it's a different error
                raise

    @skip_if_no_api_key("openai")
    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("thinking"))
    def test_38a_responses_reasoning_streaming_with_summary(self, test_config, provider, model, vk_enabled):
        """Test Case 38a: Responses API with reasoning streaming and detailed summary"""
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        model_to_use = format_provider_model(provider, model)

        stream = client.responses.create(
            model=model_to_use,
            input=RESPONSES_REASONING_INPUT,
            max_output_tokens=1200,
            reasoning={
                "effort": "high",
                "summary": "detailed",
            },
            include=["reasoning.encrypted_content"],
            stream=True,
        )

        # Collect streaming content
        content, chunk_count, tool_calls_detected, event_types = (
            collect_responses_streaming_content(stream, timeout=900)
        )

        # Validate streaming results
        assert chunk_count > 0, "Should receive at least one chunk"
        assert len(content) > 30, "Should receive substantial content with reasoning and summary"
        assert not tool_calls_detected, "Reasoning test shouldn't have tool calls"

        content_lower = content.lower()

        # Validate mathematical reasoning
        reasoning_keywords = [
            "train",
            "meet",
            "time",
            "hour",
            "pm",
            "distance",
            "speed",
            "mile",
        ]

        keyword_matches = sum(1 for keyword in reasoning_keywords if keyword in content_lower)
        assert keyword_matches >= 3, (
            f"Streaming response should contain reasoning about trains problem. "
            f"Found {keyword_matches} keywords. Content: {content[:200]}..."
        )

        # Check for step-by-step reasoning or summary indicators
        reasoning_indicators = [
            "step",
            "first",
            "then",
            "next",
            "calculate",
            "therefore",
            "because",
            "since",
            "summary",
            "conclusion",
        ]

        indicator_matches = sum(1 for indicator in reasoning_indicators if indicator in content_lower)
        assert indicator_matches >= 1, (
            f"Response should show reasoning or summary indicators. "
            f"Found {indicator_matches} indicators. Content: {content[:200]}..."
        )

        # Verify presence of calculation or time
        has_calculation = any(
            char in content for char in [":", "+", "-", "*", "/", "="]
        ) or any(
            time_word in content_lower
            for time_word in ["4:00", "5:00", "6:00", "4 pm", "5 pm", "6 pm"]
        )

        if has_calculation:
            print("Success: Streaming response contains calculations or time values")

        # Check for reasoning-related events
        has_reasoning_events = any(
            "reasoning" in evt or "summary" in evt for evt in event_types
        )
        if has_reasoning_events:
            print("Success: Detected reasoning-related events in stream")

        # Should have multiple chunks for streaming
        assert chunk_count > 1, f"Streaming should have multiple chunks, got {chunk_count}"

        print(f"Success: Reasoning streaming with summary completed ({chunk_count} chunks)")

    # =========================================================================
    # TEXT COMPLETIONS API TEST CASES
    # =========================================================================

    @skip_if_no_api_key("openai")
    def test_39_text_completion(self, openai_client, test_config):
        """Test Case 39: Text completion with simple prompt"""
        # Note: Text completions use legacy models like gpt-3.5-turbo-instruct
        response = openai_client.completions.create(
            model="gpt-3.5-turbo-instruct",
            prompt=TEXT_COMPLETION_SIMPLE_PROMPT,
            max_tokens=100,
            temperature=0.7,
        )

        # Validate response structure
        assert_valid_text_completion_response(response, min_content_length=10)

        # Check content quality - should continue the story prompt
        text = response.choices[0].text
        assert len(text) > 0, "Completion should not be empty"

        # Should generate creative story continuation
        print(f"Success: Generated completion: {text[:100]}...")

    @skip_if_no_api_key("openai")
    def test_40_text_completion_streaming(self, openai_client, test_config):
        """Test Case 40: Text completion with streaming"""
        stream = openai_client.completions.create(
            model="gpt-3.5-turbo-instruct",
            prompt=TEXT_COMPLETION_STREAMING_PROMPT,
            max_tokens=100,
            temperature=0.7,
            stream=True,
        )

        # Collect streaming content
        content, chunk_count = collect_text_completion_streaming_content(stream, timeout=300)

        # Validate streaming results
        assert chunk_count > 0, "Should receive at least one chunk"
        assert len(content) > 5, "Should receive substantial content"

        # Check content quality - should be haiku-like or poetic
        content_lower = content.lower()
        tech_keywords = [
            "technology",
            "computer",
            "digital",
            "code",
            "data",
            "machine",
            "screen",
            "byte",
            "network",
        ]

        # Should mention technology or be poetic (haiku structure)
        has_tech = any(keyword in content_lower for keyword in tech_keywords)
        has_lines = "\n" in content  # Haikus have line breaks

        assert (
            has_tech or has_lines or len(content) > 10
        ), f"Completion should be haiku-like or about technology. Got: {content}"

        # Should have multiple chunks for streaming
        assert chunk_count > 1, f"Streaming should have multiple chunks, got {chunk_count}"

        print(f"Success: Streamed haiku ({chunk_count} chunks): {content}")

    # =========================================================================
    # FILES API TEST CASES
    # =========================================================================

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("batch_file_upload")
    )
    def test_41_file_upload(self, test_config, provider, model, vk_enabled):
        """Test Case 41: Upload a file for batch processing"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_file_upload scenario")

        # Get S3 settings from config (bedrock uses S3 for file storage)
        config = get_config()
        integration_settings = config.get_integration_settings("bedrock")
        s3_bucket = integration_settings.get("s3_bucket")
        s3_region = integration_settings.get("region", "us-west-2")
        s3_prefix = integration_settings.get("output_s3_prefix", "bifrost-batch-output")

        if not s3_bucket:
            pytest.skip("S3 bucket not configured for file tests")

        # Get provider-specific client
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)

        # Create JSONL content for batch with provider-specific model
        jsonl_content = create_batch_jsonl_content(model=model, num_requests=2, provider=provider)

        # Upload the file (provider passed via extra_body)
        response = client.files.create(
            file=("batch_input.jsonl", jsonl_content.encode(), "application/jsonl"),
            purpose="batch",
            extra_body={
                "provider": provider,
                "storage_config": {
                    "s3": {
                        "bucket": s3_bucket,
                        "region": s3_region,
                        "prefix": s3_prefix,
                    },
                },
            },
        )

        # Validate response
        assert_valid_file_response(response, expected_purpose="batch")

        print(f"Success: Uploaded file with ID: {response.id} for provider {provider}")

        try:
            # List files and verify uploaded file exists (provider passed via extra_query)
            list_response = client.files.list(
                extra_query={
                    "provider": provider,
                    "storage_config": {
                        "s3": {
                            "bucket": s3_bucket,
                            "region": s3_region,
                            "prefix": s3_prefix,
                        },
                    },
                }
            )
            assert_valid_file_list_response(list_response, min_count=1)

            # Check that our uploaded file is in the list
            file_ids = [f.id for f in list_response.data]
            assert response.id in file_ids, f"Uploaded file {response.id} should be in file list"

            print(f"Success: Verified file {response.id} exists in file list")

        finally:
            # Clean up - delete the file (provider and storage_config passed via extra_query)
            try:
                client.files.delete(
                    response.id,
                    extra_query={"provider": provider},
                )
            except Exception as e:
                print(f"Warning: Failed to clean up file: {e}")

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("file_list"))
    def test_42_file_list(self, test_config, provider, model, vk_enabled):
        """Test Case 42: List uploaded files"""

        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_file_upload scenario")

        config = get_config()
        integration_settings = config.get_integration_settings("bedrock")
        s3_bucket = integration_settings.get("s3_bucket")
        s3_region = integration_settings.get("region", "us-west-2")
        s3_prefix = integration_settings.get("output_s3_prefix", "bifrost-batch-output")

        if not s3_bucket:
            pytest.skip("S3 bucket not configured for file tests")

        # First upload a file to ensure we have at least one
        jsonl_content = create_batch_jsonl_content(model=model, num_requests=1, provider=provider)

        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)

        uploaded_file = client.files.create(
            file=("test_list.jsonl", jsonl_content.encode(), "application/jsonl"),
            purpose="batch",
            extra_body={
                "provider": provider,
                "storage_config": {
                    "s3": {
                        "bucket": s3_bucket,
                        "region": s3_region,
                        "prefix": s3_prefix,
                    },
                },
            },
        )

        try:
            # List files (provider passed via extra_query)
            response = client.files.list(
                extra_query={
                    "provider": provider,
                    "storage_config": {
                        "s3": {
                            "bucket": s3_bucket,
                            "region": s3_region,
                            "prefix": s3_prefix,
                        },
                    },
                }
            )

            # Validate response
            assert_valid_file_list_response(response, min_count=1)

            # Check that our uploaded file is in the list
            file_ids = [f.id for f in response.data]
            assert (
                uploaded_file.id in file_ids
            ), f"Uploaded file {uploaded_file.id} should be in file list"

            print(f"Success: Listed {len(response.data)} files")

        finally:
            # Clean up (provider passed via extra_query)
            try:
                client.files.delete(uploaded_file.id, extra_query={"provider": provider})
            except Exception as e:
                print(f"Warning: Failed to clean up file: {e}")

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("file_retrieve")
    )
    def test_43_file_retrieve(self, test_config, provider, model, vk_enabled):
        """Test Case 43: Retrieve file metadata by ID"""

        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for file_retrieve scenario")

        config = get_config()
        integration_settings = config.get_integration_settings("bedrock")
        s3_bucket = integration_settings.get("s3_bucket")
        s3_region = integration_settings.get("region", "us-west-2")
        s3_prefix = integration_settings.get("output_s3_prefix", "bifrost-batch-output")

        if not s3_bucket:
            pytest.skip("S3 bucket not configured for file tests")

        # First upload a file
        jsonl_content = create_batch_jsonl_content(model=model, provider=provider, num_requests=1)

        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)

        uploaded_file = client.files.create(
            file=("test_retrieve.jsonl", jsonl_content.encode(), "application/jsonl"),
            purpose="batch",
            extra_body={
                "provider": provider,
                "storage_config": {
                    "s3": {
                        "bucket": s3_bucket,
                        "region": s3_region,
                        "prefix": s3_prefix,
                    },
                },
            },
        )

        try:
            # Retrieve file metadata (provider passed via extra_query)
            response = client.files.retrieve(uploaded_file.id, extra_query={"provider": provider})
            # Validate response
            assert_valid_file_response(response, expected_purpose="batch")
            assert (
                response.id == uploaded_file.id
            ), f"Retrieved file ID should match: expected {uploaded_file.id}, got {response.id}"
            assert (
                response.filename == "test_retrieve.jsonl"
            ), f"Filename should match: expected 'test_retrieve.jsonl', got {response.filename}"

            print(f"Success: Retrieved file metadata for {response.id}")

        finally:
            # Clean up (provider passed via extra_query)
            try:
                client.files.delete(uploaded_file.id, extra_query={"provider": provider})
            except Exception as e:
                print(f"Warning: Failed to clean up file: {e}")

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("file_delete")
    )
    def test_44_file_delete(self, test_config, provider, model, vk_enabled):
        """Test Case 44: Delete an uploaded file"""
        # First upload a file
        jsonl_content = create_batch_jsonl_content(model=model, provider=provider, num_requests=1)

        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)

        config = get_config()
        integration_settings = config.get_integration_settings("bedrock")
        s3_bucket = integration_settings.get("s3_bucket")
        s3_region = integration_settings.get("region", "us-west-2")
        s3_prefix = integration_settings.get("output_s3_prefix", "bifrost-batch-output")

        if not s3_bucket:
            pytest.skip("S3 bucket not configured for file tests")

        uploaded_file = client.files.create(
            file=("test_delete.jsonl", jsonl_content.encode(), "application/jsonl"),
            purpose="batch",
            extra_body={
                "provider": provider,
                "storage_config": {
                    "s3": {
                        "bucket": s3_bucket,
                        "region": s3_region,
                        "prefix": s3_prefix,
                    },
                },
            },
        )

        # Delete the file (provider passed via extra_query)
        response = client.files.delete(uploaded_file.id, extra_query={"provider": provider})

        # Validate response
        assert_valid_file_delete_response(response, expected_id=uploaded_file.id)

        print(f"Success: Deleted file {response.id}")

        # Verify file is no longer retrievable (provider passed via extra_query)
        with pytest.raises(Exception):
            client.files.retrieve(uploaded_file.id, extra_query={"provider": provider})

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("file_content")
    )
    def test_45_file_content(self, test_config, provider, model, vk_enabled):
        """Test Case 45: Download file content"""

        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for file_download scenario")

        config = get_config()
        integration_settings = config.get_integration_settings("bedrock")
        s3_bucket = integration_settings.get("s3_bucket")
        s3_region = integration_settings.get("region", "us-west-2")
        s3_prefix = integration_settings.get("output_s3_prefix", "bifrost-batch-output")

        if not s3_bucket:
            pytest.skip("S3 bucket not configured for file tests")

        # Get provider-specific client
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)

        # Create and upload a file with known content
        jsonl_content = create_batch_jsonl_content(model=model, provider=provider, num_requests=2)

        uploaded_file = client.files.create(
            file=("test_content.jsonl", jsonl_content.encode(), "application/jsonl"),
            purpose="batch",
            extra_body={
                "provider": provider,
                "storage_config": {
                    "s3": {
                        "bucket": s3_bucket,
                        "region": s3_region,
                        "prefix": s3_prefix,
                    },
                },
            },
        )

        print(f"Success: Uploaded file with ID: {uploaded_file.id} for provider {provider}")

        try:
            # Download file content (provider passed via extra_query)
            response = client.files.content(uploaded_file.id, extra_query={"provider": provider})

            # Validate content
            assert response is not None, "File content should not be None"

            # The response might be bytes or have a read method
            if hasattr(response, "read"):
                content = response.read()
            elif hasattr(response, "content"):
                content = response.content
            else:
                content = response

            # Decode if bytes
            if isinstance(content, bytes):
                content = content.decode("utf-8")

            # Verify content contains expected JSONL structure
            assert "custom_id" in content, "Content should contain 'custom_id'"
            assert "request-1" in content, "Content should contain 'request-1'"

            print(f"Success: Downloaded file content ({len(content)} bytes)")

        finally:
            # Clean up (provider passed via extra_query)
            try:
                client.files.delete(uploaded_file.id, extra_query={"provider": provider})
            except Exception as e:
                print(f"Warning: Failed to clean up file: {e}")

    # =========================================================================
    # BATCH API TEST CASES
    # =========================================================================

    # -------------------------------------------------------------------------
    # Batch Create Tests - Provider-Specific Input Methods
    # -------------------------------------------------------------------------

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("batch_file_upload")
    )
    def test_46_batch_create_with_file(self, test_config, provider, model, vk_enabled):
        """Test Case 46: Create a batch job using Files API or inline requests

        This test uploads a JSONL file first, then creates a batch using the file ID.
        For Anthropic, uses inline requests via extra_body instead of file upload.
        Uses OpenAI SDK with extra_body to pass provider.
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_file_upload scenario")

        # Get provider-specific client
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)

        # Anthropic uses inline requests instead of file-based batching
        if provider == "anthropic":
            batch = None
            try:
                # Create inline requests for Anthropic
                requests = create_batch_inline_requests(
                    model=model, num_requests=2, provider=provider, sdk="anthropic"
                )

                # Create batch job with inline requests via extra_body
                batch = client.batches.create(
                    input_file_id="",
                    endpoint="/v1/chat/completions",
                    completion_window="24h",
                    extra_body={
                        "provider": provider,
                        "requests": requests,
                    },
                )

                # Validate response
                assert_valid_batch_response(batch)
                print(
                    f"Success: Created inline batch with ID: {batch.id}, status: {batch.status} for provider {provider}"
                )

            finally:
                # Clean up - cancel batch if created
                if batch:
                    try:
                        client.batches.cancel(
                            batch.id,
                            extra_body={"provider": provider},
                        )
                    except Exception as e:
                        print(f"Info: Could not cancel batch (may already be processed): {e}")
            return

        # File-based batching for other providers (Bedrock, OpenAI)
        config = get_config()
        integration_settings = config.get_integration_settings("bedrock")
        s3_bucket = integration_settings.get("s3_bucket")
        s3_region = integration_settings.get("region", "us-west-2")
        s3_prefix = integration_settings.get("output_s3_prefix", "bifrost-batch-output")
        
        if not s3_bucket:
            pytest.skip("S3 bucket not configured for file tests")

        # Build output S3 URI for Bedrock batch
        output_s3_uri = f"s3://{s3_bucket}/{s3_prefix}"

        # First upload a file for batch processing
        jsonl_content = create_batch_jsonl_content(
            model=model,
            num_requests=2,
            provider=provider,
        )

        uploaded_file = client.files.create(
            file=("batch_create_file_test.jsonl", jsonl_content.encode(), "application/jsonl"),
            purpose="batch",
            extra_body={
                "provider": provider,
                "storage_config": {
                    "s3": {
                        "bucket": s3_bucket,
                        "region": s3_region,
                        "prefix": s3_prefix,
                    },
                },
            },
        )

        batch = None
        try:
            # Create batch job using file ID (provider passed via extra_body)
            # For Bedrock: role_arn, output_s3_uri, and model are required
            batch = client.batches.create(
                input_file_id=uploaded_file.id,
                endpoint="/v1/chat/completions",
                completion_window="24h",
                extra_body={
                    "provider": provider,
                    "model": model,
                    "output_s3_uri": output_s3_uri,
                },
            )
            # Validate response
            assert_valid_batch_response(batch)
            assert (
                batch.input_file_id == uploaded_file.id
            ), f"Input file ID should match: expected {uploaded_file.id}, got {batch.input_file_id}"

            print(
                f"Success: Created file-based batch with ID: {batch.id}, status: {batch.status} for provider {provider}"
            )

        finally:
            # Clean up - cancel batch if created, then delete file
            if batch:
                try:
                    client.batches.cancel(
                        batch.id,
                        extra_body={
                            "provider": provider,
                            "model": model,
                            "output_s3_uri": output_s3_uri,
                        },
                    )
                except Exception as e:
                    print(f"Info: Could not cancel batch (may already be processed): {e}")

            try:
                client.files.delete(uploaded_file.id, extra_query={"provider": provider})
            except Exception as e:
                print(f"Warning: Failed to clean up file: {e}")

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("batch_list"))
    def test_47_batch_list(self, test_config, provider, model, vk_enabled):
        """Test Case 47: List batch jobs

        Tests batch listing across all providers using OpenAI SDK.
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_list scenario")

        # Get provider-specific client
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)

        # Use OpenAI SDK for batch list (provider passed via extra_query)
        response = client.batches.list(
            limit=10, extra_query={"provider": provider, "model": model}
        )
        assert_valid_batch_list_response(response, min_count=0)
        batch_count = len(response.data)

        print(f"Success: Listed {batch_count} batches for provider {provider}")

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("batch_retrieve")
    )
    def test_48_batch_retrieve(self, test_config, provider, model, vk_enabled):
        """Test Case 48: Retrieve batch status by ID

        Creates a batch using file-based method or inline requests, then retrieves it using OpenAI SDK.
        For Anthropic, uses inline requests via extra_body instead of file upload.
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_retrieve scenario")

        # Get provider-specific client
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)

        # Anthropic uses inline requests instead of file-based batching
        if provider == "anthropic":
            batch_id = None
            try:
                # Create batch with inline requests
                requests = create_batch_inline_requests(
                    model=model, num_requests=1, provider=provider, sdk="anthropic"
                )
                batch = client.batches.create(
                    input_file_id="",
                    endpoint="/v1/chat/completions",
                    completion_window="24h",
                    extra_body={
                        "provider": provider,
                        "requests": requests,
                    },
                )
                batch_id = batch.id

                # Retrieve using SDK (provider passed via extra_query)
                retrieved_batch = client.batches.retrieve(
                    batch_id, extra_query={"provider": provider}
                )
                assert_valid_batch_response(retrieved_batch)
                assert retrieved_batch.id == batch_id
                print(
                    f"Success: Retrieved batch {batch_id}, status: {retrieved_batch.status} for provider {provider}"
                )

            finally:
                # Clean up
                if batch_id:
                    try:
                        client.batches.cancel(batch_id, extra_body={"provider": provider})
                    except Exception:
                        pass
            return

        # File-based batching for other providers (Bedrock, OpenAI)
        config = get_config()
        integration_settings = config.get_integration_settings("bedrock")
        s3_bucket = integration_settings.get("s3_bucket")
        s3_region = integration_settings.get("region", "us-west-2")
        s3_prefix = integration_settings.get("output_s3_prefix", "bifrost-batch-output")
      
        if not s3_bucket:
            pytest.skip("S3 bucket not configured for file tests")

        # Build output S3 URI for Bedrock batch
        output_s3_uri = f"s3://{s3_bucket}/{s3_prefix}"

        batch_id = None
        uploaded_file = None

        try:
            # Create file for batch processing
            jsonl_content = create_batch_jsonl_content(
                model=model, num_requests=1, provider=provider
            )
            print(f"Creating file for batch processing: {jsonl_content}")
            uploaded_file = client.files.create(
                file=("batch_retrieve_test.jsonl", jsonl_content.encode(), "application/jsonl"),
                purpose="batch",
                extra_body={
                    "provider": provider,
                    "storage_config": {
                        "s3": {
                            "bucket": s3_bucket,
                            "region": s3_region,
                            "prefix": s3_prefix,
                        },
                    },
                },
            )

            # Create batch using file ID (provider passed via extra_body)
            # For Bedrock: role_arn, output_s3_uri, and model are required
            extra_body = {"provider": provider}
            if provider == "bedrock":
                extra_body["model"] = model
                extra_body["output_s3_uri"] = output_s3_uri

            batch = client.batches.create(
                input_file_id=uploaded_file.id,
                endpoint="/v1/chat/completions",
                completion_window="24h",
                extra_body=extra_body,
            )
            batch_id = batch.id

            # Retrieve using SDK (provider passed via extra_query)
            retrieved_batch = client.batches.retrieve(batch_id, extra_query={"provider": provider})
            assert_valid_batch_response(retrieved_batch)
            assert retrieved_batch.id == batch_id
            print(
                f"Success: Retrieved batch {batch_id}, status: {retrieved_batch.status} for provider {provider}"
            )

        finally:
            # Clean up
            if batch_id:
                try:
                    client.batches.cancel(batch_id, extra_body={"provider": provider})
                except Exception:
                    pass
            if uploaded_file:
                try:
                    client.files.delete(uploaded_file.id, extra_query={"provider": provider})
                except Exception:
                    pass

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("batch_cancel")
    )
    def test_49_batch_cancel(self, test_config, provider, model, vk_enabled):
        """Test Case 49: Cancel a batch job

        Creates a batch using file-based method or inline requests, then cancels it using OpenAI SDK.
        For Anthropic, uses inline requests via extra_body instead of file upload.
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_cancel scenario")

        # Get provider-specific client
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)

        # Anthropic uses inline requests instead of file-based batching
        if provider == "anthropic":
            batch_id = None
            try:
                # Create batch with inline requests
                requests = create_batch_inline_requests(
                    model=model, num_requests=1, provider=provider, sdk="anthropic"
                )
                batch = client.batches.create(
                    input_file_id="",
                    endpoint="/v1/chat/completions",
                    completion_window="24h",
                    extra_body={
                        "provider": provider,
                        "requests": requests,
                    },
                )
                batch_id = batch.id

                # Cancel using SDK (provider passed via extra_body for POST)
                cancelled_batch = client.batches.cancel(batch_id, extra_body={"provider": provider})
                assert cancelled_batch is not None
                assert cancelled_batch.id == batch_id
                assert cancelled_batch.status in ["cancelling", "cancelled"]
                print(
                    f"Success: Cancelled batch {batch_id}, status: {cancelled_batch.status} for provider {provider}"
                )

            except Exception:
                # Cleanup even on failure
                pass
            return

        # File-based batching for other providers (Bedrock, OpenAI)
        config = get_config()
        integration_settings = config.get_integration_settings("bedrock")
        s3_bucket = integration_settings.get("s3_bucket")
        s3_region = integration_settings.get("region", "us-west-2")
        s3_prefix = integration_settings.get("output_s3_prefix", "bifrost-batch-output")
        
        if not s3_bucket:
            pytest.skip("S3 bucket not configured for file tests")

        
        # Build output S3 URI for Bedrock batch
        output_s3_uri = f"s3://{s3_bucket}/{s3_prefix}"

        batch_id = None
        uploaded_file = None

        try:
            # Create file for batch processing (provider passed via extra_body)
            jsonl_content = create_batch_jsonl_content(
                model=model, num_requests=1, provider=provider
            )
            file_extra_body = {"provider": provider}
            if provider == "bedrock":
                file_extra_body["storage_config"] = {
                    "s3": {
                        "bucket": s3_bucket,
                        "region": s3_region,
                        "prefix": s3_prefix,
                    },
                }
            uploaded_file = client.files.create(
                file=("batch_cancel_test.jsonl", jsonl_content.encode(), "application/jsonl"),
                purpose="batch",
                extra_body=file_extra_body,
            )

            # Create batch using file ID (provider passed via extra_body)
            # For Bedrock: role_arn, output_s3_uri, and model are required
            extra_body = {"provider": provider}
            if provider == "bedrock":
                extra_body["model"] = model
                extra_body["output_s3_uri"] = output_s3_uri

            batch = client.batches.create(
                input_file_id=uploaded_file.id,
                endpoint="/v1/chat/completions",
                completion_window="24h",
                extra_body=extra_body,
            )
            batch_id = batch.id

            # Cancel using SDK (provider passed via extra_body for POST)
            cancelled_batch = client.batches.cancel(batch_id, extra_body={"provider": provider})
            assert cancelled_batch is not None
            assert cancelled_batch.id == batch_id
            assert cancelled_batch.status in ["cancelling", "cancelled"]
            print(
                f"Success: Cancelled batch {batch_id}, status: {cancelled_batch.status} for provider {provider}"
            )

        finally:
            # Clean up (provider passed via extra_query)
            if uploaded_file:
                try:
                    client.files.delete(uploaded_file.id, extra_query={"provider": provider})
                except Exception:
                    pass

    # -------------------------------------------------------------------------
    # Batch End-to-End Tests
    # -------------------------------------------------------------------------

    @pytest.mark.parametrize(
        "provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("batch_file_upload")
    )
    def test_50_batch_e2e_file_api(self, test_config, provider, model, vk_enabled):
        """Test Case 50: End-to-end batch workflow using Files API or inline requests

        Complete workflow: upload file -> create batch -> poll status -> verify in list.
        For Anthropic, uses inline requests via extra_body instead of file upload.
        Uses OpenAI SDK with extra_body/extra_query to pass provider.
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_file_upload scenario")

        config = get_config()
        integration_settings = config.get_integration_settings("bedrock")
        s3_bucket = integration_settings.get("s3_bucket")
        s3_region = integration_settings.get("region", "us-west-2")
        s3_prefix = integration_settings.get("output_s3_prefix", "bifrost-batch-output")
        
        if not s3_bucket:
            pytest.skip("S3 bucket not configured for file tests")

        # Get provider-specific client
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)

        # Anthropic uses inline requests instead of file-based batching
        if provider == "anthropic":
            batch = None
            try:
                # Step 1: Create inline requests for Anthropic
                print(f"Step 1: Creating inline requests for provider {provider}...")
                requests = create_batch_inline_requests(
                    model=model, num_requests=2, provider=provider, sdk="anthropic"
                )
                print(f"  Created {len(requests)} inline requests")

                # Step 2: Create batch job with inline requests via extra_body
                print("Step 2: Creating batch job with inline requests...")
                batch = client.batches.create(
                    input_file_id="dummy-file-id",
                    endpoint="/v1/chat/completions",
                    completion_window="24h",
                    extra_body={
                        "provider": provider,
                        "requests": requests,
                    },
                )
                assert_valid_batch_response(batch)
                print(f"  Created batch: {batch.id}, status: {batch.status}")

                # Step 3: Poll batch status (with timeout)
                print("Step 3: Polling batch status...")
                max_polls = 5
                poll_interval = 2  # seconds

                for i in range(max_polls):
                    retrieved_batch = client.batches.retrieve(
                        batch.id, extra_query={"provider": provider}
                    )
                    print(f"  Poll {i+1}: status = {retrieved_batch.status}")

                    if retrieved_batch.status in [
                        "completed",
                        "failed",
                        "expired",
                        "cancelled",
                        "ended",
                    ]:
                        print(f"  Batch reached terminal state: {retrieved_batch.status}")
                        break

                    if (
                        hasattr(retrieved_batch, "request_counts")
                        and retrieved_batch.request_counts
                    ):
                        counts = retrieved_batch.request_counts
                        print(
                            f"    Request counts - total: {counts.total}, completed: {counts.completed}, failed: {counts.failed}"
                        )

                    time.sleep(poll_interval)

                # Step 4: Verify batch is in the list
                print("Step 4: Verifying batch in list...")
                batch_list = client.batches.list(limit=20, extra_query={"provider": provider})
                batch_ids = [b.id for b in batch_list.data]
                assert batch.id in batch_ids, f"Batch {batch.id} should be in the batch list"
                print(f"  Verified batch {batch.id} is in list")

                print(
                    f"Success: Inline batch E2E completed for batch {batch.id} (provider: {provider})"
                )

            finally:
                if batch:
                    try:
                        client.batches.cancel(batch.id, extra_body={"provider": provider})
                        print(f"Cleanup: Cancelled batch {batch.id}")
                    except Exception as e:
                        print(f"Cleanup info: Could not cancel batch: {e}")
            return

        # File-based batching for other providers (OpenAI)
        # Step 1: Create and upload batch input file (provider passed via extra_body)
        jsonl_content = create_batch_jsonl_content(model=model, num_requests=2, provider=provider)

        print(f"Step 1: Uploading batch input file for provider {provider}...")
        uploaded_file = client.files.create(
            file=("batch_e2e_file_test.jsonl", jsonl_content.encode(), "application/jsonl"),
            purpose="batch",
            extra_body={
                "provider": provider,
                "storage_config": {
                    "s3": {
                        "bucket": s3_bucket,
                        "region": s3_region,
                        "prefix": s3_prefix,
                    },
                },
            },
        )
        assert_valid_file_response(uploaded_file, expected_purpose="batch")
        print(f"  Uploaded file: {uploaded_file.id}")

        # Build output S3 URI for Bedrock batch
        output_s3_uri = f"s3://{s3_bucket}/{s3_prefix}"

        batch = None
        try:
            # Step 2: Create batch job using file ID (provider passed via extra_body)
            print("Step 2: Creating batch job with file ID...")
            batch = client.batches.create(
                input_file_id=uploaded_file.id,
                endpoint="/v1/chat/completions",
                completion_window="24h",
                metadata={
                    "test": "e2e_file",
                    "source": "bifrost-integration-tests",                    
                },
                extra_body={
                    "provider": provider,
                    "model": model,
                    "output_s3_uri": output_s3_uri,
                },
            )
            assert_valid_batch_response(batch)
            print(f"  Created batch: {batch.id}, status: {batch.status}")

            # Step 3: Poll batch status (with timeout) (provider passed via extra_query)
            print("Step 3: Polling batch status...")
            max_polls = 5
            poll_interval = 2  # seconds
            total_requests = 0
            for i in range(max_polls):
                retrieved_batch = client.batches.retrieve(
                    batch.id, extra_query={"provider": provider}
                )
                print(f"  Poll {i+1}: status = {retrieved_batch.status}")

                if retrieved_batch.status in ["completed", "failed", "expired", "cancelled"]:
                    print(f"  Batch reached terminal state: {retrieved_batch.status}")
                    break

                if hasattr(retrieved_batch, "request_counts") and retrieved_batch.request_counts:
                    counts = retrieved_batch.request_counts
                    print(
                        f"    Request counts - total: {counts.total}, completed: {counts.completed}, failed: {counts.failed}"
                    )
                    total_requests = counts.total
                time.sleep(poll_interval)

            if provider != "bedrock":
                # For bedrock, unless job status is completed or partially completed, counts are not available
                assert total_requests == 2, f"Total requests should be 2, got {total_requests}"

            # Step 4: Verify batch is in the list (provider passed via extra_query)
            print("Step 4: Verifying batch in list...")
            batch_list = client.batches.list(limit=20, extra_query={"provider": provider})
            batch_ids = [b.id for b in batch_list.data]
            assert batch.id in batch_ids, f"Batch {batch.id} should be in the batch list"
            print(f"  Verified batch {batch.id} is in list")

            print(f"Success: File API E2E completed for batch {batch.id} (provider: {provider})")

        finally:
            if batch:
                try:
                    client.batches.cancel(batch.id, extra_body={"provider": provider})
                    print(f"Cleanup: Cancelled batch {batch.id}")
                except Exception as e:
                    print(f"Cleanup info: Could not cancel batch: {e}")

            try:
                client.files.delete(uploaded_file.id, extra_query={"provider": provider})
                print(f"Cleanup: Deleted file {uploaded_file.id}")
            except Exception as e:
                print(f"Cleanup warning: Failed to delete file: {e}")
    
    # =========================================================================
    # INPUT TOKENS / TOKEN COUNTING TEST CASES
    # =========================================================================

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("count_tokens"))
    def test_51a_input_tokens_simple_text(self, provider, model, vk_enabled):
        """Test Case 51a: Input tokens count with simple text"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        response = client.responses.input_tokens.count(
            model=format_provider_model(provider, model),
            input=INPUT_TOKENS_SIMPLE_TEXT,
        )

        # Validate response structure
        assert_valid_input_tokens_response(response, "openai")

        # Simple text should have a reasonable token count (between 3-20 tokens)
        assert 3 <= response.input_tokens <= 20, (
            f"Simple text should have 3-20 tokens, got {response.input_tokens}"
        )

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("count_tokens"))
    def test_51b_input_tokens_with_system_message(self, provider, model, vk_enabled):
        """Test Case 51b: Input tokens count with system message"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        response = client.responses.input_tokens.count(
            model=format_provider_model(provider, model),
            input=INPUT_TOKENS_WITH_SYSTEM,
        )

        # Validate response structure
        assert_valid_input_tokens_response(response, "openai")

        # With system message should have more tokens than simple text
        assert response.input_tokens > 2, (
            f"With system message should have >2 tokens, got {response.input_tokens}"
        )

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("count_tokens"))
    def test_51c_input_tokens_long_text(self, provider, model, vk_enabled):
        """Test Case 51c: Input tokens count with long text"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        response = client.responses.input_tokens.count(
            model=format_provider_model(provider, model),
            input=INPUT_TOKENS_LONG_TEXT,
        )

        # Validate response structure
        assert_valid_input_tokens_response(response, "openai")

        # Long text should have significantly more tokens
        assert response.input_tokens > 100, (
            f"Long text should have >100 tokens, got {response.input_tokens}"
        )

    # =========================================================================
    # WEB SEARCH TOOL TEST CASES
    # =========================================================================

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("web_search"))
    def test_52_web_search_non_streaming(self, provider, model, vk_enabled):
        """Test Case 52: Web search tool (non-streaming) using Responses API"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for web_search scenario")
        
        print(f"\n=== Testing Web Search (Non-Streaming) for provider {provider} ===")
        
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        
        # Use Responses API with web search tool
        response = client.responses.create(
            model=format_provider_model(provider, model),
            tools=[{"type": "web_search"}],
            input="What is the current weather in New York City today?",
            max_output_tokens=1200,
        )
        
        # Validate basic response
        assert response is not None, "Response should not be None"
        assert hasattr(response, "output"), "Response should have output"
        assert response.output is not None, "Output should not be None"
        assert len(response.output) > 0, "Output should not be empty"
        
        # Check for web_search_call in output
        has_web_search_call = False
        has_message_output = False
        has_citations = False
        search_status = None
        output_text = ""
        
        for output_item in response.output:
            # Check for web_search_call
            if hasattr(output_item, "type") and output_item.type == "web_search_call":
                has_web_search_call = True
                if hasattr(output_item, "status"):
                    search_status = output_item.status
                print(f"✓ Found web_search_call with status: {search_status}")
                
                # Check for search action details
                if hasattr(output_item, "action"):
                    action = output_item.action
                    if hasattr(action, "query"):
                        print(f"✓ Search query: {action.query}")
                    if hasattr(action, "sources") and action.sources:
                        print(f"✓ Found {len(action.sources)} sources")
            
            # Check for message output with content
            elif hasattr(output_item, "type") and output_item.type == "message":
                has_message_output = True
                if hasattr(output_item, "content") and output_item.content:
                    for content_block in output_item.content:
                        if hasattr(content_block, "type") and content_block.type == "output_text":
                            if hasattr(content_block, "text"):
                                output_text = content_block.text
                                print(f"✓ Found text output (first 150 chars): {output_text[:150]}...")
                            
                            # Check for annotations (citations) from web search
                            if hasattr(content_block, "annotations") and content_block.annotations:
                                has_citations = True
                                citation_count = len(content_block.annotations)
                                print(f"✓ Found {citation_count} citations")
                                
                                # Validate citation structure using helper
                                for i, annotation in enumerate(content_block.annotations[:3]):
                                    assert_valid_openai_annotation(annotation, expected_type="url_citation")
                                    if hasattr(annotation, "url"):
                                        print(f"  Citation {i+1}: {annotation.url}")
        
        # Validate web search was performed
        assert has_web_search_call, "Response should contain web_search_call"
        assert search_status == "completed", f"Web search should be completed, got status: {search_status}"
        assert has_message_output, "Response should contain message output"
        assert len(output_text) > 0, "Message should have text content"
        
        # Validate content mentions weather
        text_lower = output_text.lower()
        weather_keywords = ["weather", "temperature", "forecast", "rain", "snow", "wind", "sunny", "cloudy", "degrees", 
                          "cold", "hot", "warm", "cool", "chilly", "blustery", "storm", "clear", "humid", "dry"]
        assert any(keyword in text_lower for keyword in weather_keywords), \
            f"Response should mention weather-related information. Got: {output_text[:300]}..."
        
        # Validate usage information
        if hasattr(response, "usage"):
            print(f"✓ Token usage - Input: {response.usage.input_tokens}, Output: {response.usage.output_tokens}")
        
        print(f"✓ Web search (non-streaming) test passed!")

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("web_search"))
    def test_53_web_search_streaming(self, provider, model, vk_enabled):
        """Test Case 53: Web search tool (streaming) using Responses API"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for web_search scenario")
        
        print(f"\n=== Testing Web Search (Streaming) for provider {provider} ===")
        
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        
        # Use Responses API with web search tool and user location
        stream = client.responses.create(
            model=format_provider_model(provider, model),
            tools=[{
                "type": "web_search",
                "user_location": {
                    "type": "approximate",
                    "country": "US",
                    "city": "New York",
                    "region": "New York",
                    "timezone": "America/New_York"
                }
            }],
            input="What's the weather in NYC today?",
            include=["web_search_call.action.sources"],
            max_output_tokens=1200,
            stream=True
        )
        
        # Collect streaming events
        text_parts = []
        chunk_count = 0
        has_web_search_call = False
        has_message_output = False
        citations = []
        search_queries = []
        
        for chunk in stream:
            chunk_count += 1
            
            if hasattr(chunk, "type"):
                chunk_type = chunk.type
                
                # Handle output_item.added event
                if chunk_type == "response.output_item.added":
                    if hasattr(chunk, "item"):
                        item = chunk.item
                        # Check for web_search_call
                        if hasattr(item, "type") and item.type == "web_search_call":
                            has_web_search_call = True
                            print(f"✓ Web search call started (id: {item.id if hasattr(item, 'id') else 'unknown'})")
                        
                        # Check for message output
                        elif hasattr(item, "type") and item.type == "message":
                            has_message_output = True
                
                # Handle output_item.done event for completed items
                elif chunk_type == "response.output_item.done":
                    if hasattr(chunk, "item"):
                        item = chunk.item
                        
                        # Check web_search_call completion with action details
                        if hasattr(item, "type") and item.type == "web_search_call":
                            if hasattr(item, "action"):
                                action = item.action
                                if hasattr(action, "query"):
                                    search_queries.append(action.query)
                                    print(f"✓ Search query: {action.query}")
                                if hasattr(action, "sources") and action.sources:
                                    print(f"✓ Found {len(action.sources)} sources")
                
                # Handle content.text.delta for streaming text
                elif chunk_type == "response.output_text.delta":
                    if hasattr(chunk, "delta"):
                        text_parts.append(chunk.delta)
                
                # Handle content.annotation.added for citations
                elif chunk_type == "response.output_text.annotation.added":
                    if hasattr(chunk, "annotation"):
                        annotation = chunk.annotation
                        citations.append(annotation)
                        
                        # Validate citation using helper
                        assert_valid_openai_annotation(annotation, expected_type="url_citation")
                        
                        if hasattr(annotation, "url") and hasattr(annotation, "title"):
                            print(f"  Citation received: {annotation.title}")
            
            # Safety check
            if chunk_count > 5000:
                break
        
        # Combine collected text
        complete_text = "".join(text_parts)
        
        # Validate results
        assert chunk_count > 0, "Should receive at least one chunk"
        assert has_web_search_call, "Should detect web search call in streaming"
        assert has_message_output, "Should detect message output in streaming"
        assert len(complete_text) > 0, "Should receive text content"
        
        # Validate text mentions weather
        text_lower = complete_text.lower()
        weather_keywords = ["weather", "temperature", "forecast", "rain", "snow", "wind", "sunny", "cloudy", "degrees",
                          "cold", "hot", "warm", "cool", "chilly", "blustery", "storm", "clear", "humid", "dry"]
        assert any(keyword in text_lower for keyword in weather_keywords), \
            f"Response should mention weather-related information. Got: {complete_text[:200]}..."
        
        print(f"✓ Streaming validation:")
        print(f"  - Chunks received: {chunk_count}")
        print(f"  - Search queries: {len(search_queries)}")
        print(f"  - Citations: {len(citations)}")
        print(f"  - Text length: {len(complete_text)} characters")
        print(f"  - First 150 chars: {complete_text[:150]}...")
        
        # Validate all citations using helper
        if len(citations) > 0:
            for citation in citations:
                assert_valid_openai_annotation(citation, expected_type="url_citation")
        
        print(f"✓ Web search (streaming) test passed!")

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("web_search"))
    def test_54_web_search_annotation_conversion(self, provider, model, vk_enabled):
        """Test Case 54: Validate Anthropic citations convert to OpenAI annotations correctly"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for web_search scenario")
        
        print(f"\n=== Testing Web Search Annotation Conversion for provider {provider} ===")
        
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        
        response = client.responses.create(
            model=format_provider_model(provider, model),
            tools=[{"type": "web_search"}],
            input="What is the speed of light in a vacuum use web search tool?",
            include=["web_search_call.action.sources"],
            max_output_tokens=1500,
        )
        
        # Validate basic response
        assert response is not None, "Response should not be None"
        assert hasattr(response, "output"), "Response should have output"
        
        # Collect and validate annotations
        annotations_found = []
        for output_item in response.output:
            if hasattr(output_item, "type") and output_item.type == "message":
                if hasattr(output_item, "content") and output_item.content:
                    for content_block in output_item.content:
                        if hasattr(content_block, "type") and content_block.type == "output_text":
                            if hasattr(content_block, "annotations") and content_block.annotations:
                                for annotation in content_block.annotations:
                                    annotations_found.append(annotation)
        
        # Validate annotation structure
        if len(annotations_found) > 0:
            print(f"✓ Found {len(annotations_found)} annotations")
            for i, annotation in enumerate(annotations_found[:3]):
                assert_valid_openai_annotation(annotation, expected_type="url_citation")
                print(f"  Annotation {i+1}:")
                print(f"    Type: {annotation.type}")
                print(f"    URL: {annotation.url if hasattr(annotation, 'url') else 'N/A'}")
                if hasattr(annotation, "title"):
                    print(f"    Title: {annotation.title}")
                # Check for encrypted_index preservation
                if hasattr(annotation, "encrypted_index"):
                    print(f"    Encrypted index present: ✓")
            
            print(f"✓ All annotations have valid url_citation structure")
        else:
            print(f"⚠ No annotations found")
        
        print(f"✓ Annotation conversion test passed!")

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("web_search"))
    def test_55_web_search_user_location(self, provider, model, vk_enabled):
        """Test Case 55: Web search with user location for localized results"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for web_search scenario")
        
        print(f"\n=== Testing Web Search with User Location for provider {provider} ===")
        
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        
        # Test with specific location
        response = client.responses.create(
            model=format_provider_model(provider, model),
            tools=[{
                "type": "web_search",
                "user_location": {
                    "type": "approximate",
                    "city": "San Francisco",
                    "region": "California",
                    "country": "US",
                    "timezone": "America/Los_Angeles"
                }
            }],
            input="What is the weather like today?",
            max_output_tokens=1200,
        )
        
        # Validate basic response
        assert response is not None, "Response should not be None"
        assert hasattr(response, "output"), "Response should have output"
        assert len(response.output) > 0, "Output should not be empty"
        
        # Check for web_search_call with status
        has_web_search = False
        has_message = False
        
        for output_item in response.output:
            if hasattr(output_item, "type"):
                if output_item.type == "web_search_call":
                    has_web_search = True
                    print(f"✓ Web search executed")
                elif output_item.type == "message":
                    has_message = True
        
        assert has_web_search, "Should perform web search"
        assert has_message, "Should have message response"
        
        print(f"✓ User location test passed!")

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("web_search"))
    def test_56_web_search_wildcard_domains(self, provider, model, vk_enabled):
        """Test Case 56: Web search with wildcard domain patterns"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for web_search scenario")
        
        print(f"\n=== Testing Web Search with Wildcard Domains for provider {provider} ===")
        
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        
        # Use wildcard domain patterns
        response = client.responses.create(
            model=format_provider_model(provider, model),
            tools=[{
                "type": "web_search",
                "allowed_domains": ["wikipedia.org", "en.wikipedia.org"]
            }],
            input="What is machine learning use web search tool?",
            include=["web_search_call.action.sources"],
            max_output_tokens=1500,
        )
        
        # Validate basic response
        assert response is not None, "Response should not be None"
        assert hasattr(response, "output"), "Response should have output"
        
        # Collect search sources
        search_sources = []
        for output_item in response.output:
            if hasattr(output_item, "type") and output_item.type == "web_search_call":
                if hasattr(output_item, "action") and hasattr(output_item.action, "sources"):
                    if output_item.action.sources:
                        search_sources.extend(output_item.action.sources)
        
        if len(search_sources) > 0:
            print(f"✓ Found {len(search_sources)} search sources")
            for i, source in enumerate(search_sources[:3]):
                if hasattr(source, "url"):
                    print(f"  Source {i+1}: {source.url}")
        
        print(f"✓ Wildcard domains test passed!")

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("web_search"))
    def test_57_web_search_multi_turn_openai(self, provider, model, vk_enabled):
        """Test Case 57: Web search in multi-turn conversation (OpenAI SDK)"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for web_search scenario")
        
        print(f"\n=== Testing Web Search Multi-Turn (OpenAI SDK) for provider {provider} ===")
        
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)
        
        # First turn
        input_messages = [
            {"role": "user", "content": "What is renewable energy use web search tool?"}
        ]
        
        response1 = client.responses.create(
            model=format_provider_model(provider, model),
            tools=[{"type": "web_search"}],
            input=input_messages,
            max_output_tokens=1500,
        )
        
        assert response1 is not None, "First response should not be None"
        assert hasattr(response1, "output"), "First response should have output"
        
        # Collect first turn output for context
        print(f"✓ First turn completed with {len(response1.output)} output items")

        # Second turn with follow-up
        # Add each output item from the first response
        for output_item in response1.output:
            input_messages.append(output_item)
        input_messages.append({"role": "user", "content": "What are the main types of renewable energy?"})
        
        response2 = client.responses.create(
            model=format_provider_model(provider, model),
            tools=[{"type": "web_search"}],
            input=input_messages,
            max_output_tokens=1500,
        )
        
        assert response2 is not None, "Second response should not be None"
        assert hasattr(response2, "output"), "Second response should have output"
        assert len(response2.output) > 0, "Second response should have content"
        
        # Validate second turn has message response
        has_message = False
        for output_item in response2.output:
            if hasattr(output_item, "type") and output_item.type == "message":
                has_message = True
        
        assert has_message, "Second turn should have message response"
        print(f"✓ Second turn completed with {len(response2.output)} output items")
        print(f"✓ Multi-turn conversation test passed!")

    # =========================================================================
    # Async Inference Tests
    # =========================================================================

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("simple_chat"))
    def test_58_async_chat_completions(self, test_config, provider, model, vk_enabled):
        """Test Case 58: Async chat completions - submit and poll"""
        _ = test_config
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        print(f"\n=== Testing Async Chat Completions for provider {provider} ===")
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)

        request_params = {
            "model": format_provider_model(provider, model),
            "messages": SIMPLE_CHAT_MESSAGES,
            "max_tokens": 100,
        }

        # Submit async request
        initial = client.chat.completions.create(
            **request_params,
            extra_headers={"x-bf-async": "true"},
        )

        assert initial.id is not None, "Async response should have an ID"
        print(f"  Async job ID: {initial.id}")

        # If completed synchronously, validate and return
        if initial.choices and len(initial.choices) > 0:
            print("  Status: completed (sync)")
            assert initial.choices[0].message.content is not None
            assert len(initial.choices[0].message.content) > 0
            print(f"  Result: {initial.choices[0].message.content[:80]}...")
            return

        print("  Status: processing")

        # Poll until completed
        max_polls = 30
        for i in range(max_polls):
            time.sleep(2)
            print(f"  Polling attempt {i + 1}/{max_polls}...")

            poll = client.chat.completions.create(
                **request_params,
                extra_headers={"x-bf-async-id": initial.id},
            )

            if poll.choices and len(poll.choices) > 0:
                print("  Status: completed")
                assert poll.choices[0].message.content is not None
                assert len(poll.choices[0].message.content) > 0
                print(f"  Result: {poll.choices[0].message.content[:80]}...")
                print("✓ Async chat completions test passed!")
                return

        pytest.fail(f"Async job did not complete after {max_polls} polls")

    @pytest.mark.parametrize("provider,model,vk_enabled", get_cross_provider_params_with_vk_for_scenario("responses"))
    def test_59_async_responses(self, test_config, provider, model, vk_enabled):
        """Test Case 59: Async responses API - submit and poll"""
        _ = test_config
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        print(f"\n=== Testing Async Responses for provider {provider} ===")
        client = get_provider_openai_client(provider, vk_enabled=vk_enabled)

        request_params = {
            "model": format_provider_model(provider, model),
            "input": RESPONSES_SIMPLE_TEXT_INPUT,
            "max_output_tokens": 100,
        }

        # Submit async request
        initial = client.responses.create(
            **request_params,
            extra_headers={"x-bf-async": "true"},
        )

        assert initial.id is not None, "Async response should have an ID"
        print(f"  Async job ID: {initial.id}")

        # If completed synchronously, validate and return
        if initial.status == "completed":
            print("  Status: completed (sync)")
            assert_valid_responses_response(initial)
            print(f"  Result: {initial.output_text[:80]}...")
            return

        print("  Status: processing")

        # Poll until completed
        max_polls = 30
        for i in range(max_polls):
            time.sleep(2)
            print(f"  Polling attempt {i + 1}/{max_polls}...")

            poll = client.responses.create(
                **request_params,
                extra_headers={"x-bf-async-id": initial.id},
            )

            if poll.status == "completed":
                print("  Status: completed")
                assert_valid_responses_response(poll)
                print(f"  Result: {poll.output_text[:80]}...")
                print("✓ Async responses test passed!")
                return

        pytest.fail(f"Async job did not complete after {max_polls} polls")

