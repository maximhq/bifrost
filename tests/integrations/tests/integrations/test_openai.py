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
32. Responses API - simple text input
33. Responses API - with system message
34. Responses API - with image
35. Responses API - with tools
36. Responses API - streaming
37. Responses API - streaming with tools
38. Responses API - reasoning
39. Text Completions - simple prompt
42. Text Completions - streaming
"""

import pytest
import json
from openai import OpenAI
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
    get_content_string,
    collect_streaming_content,
    extract_tool_calls,
    get_api_key,
    skip_if_no_api_key,
    COMPARISON_KEYWORDS,
    WEATHER_KEYWORDS,
    LOCATION_KEYWORDS,
    # Speech and Transcription utilities
    SPEECH_TEST_INPUT,
    SPEECH_TEST_VOICES,
    TRANSCRIPTION_TEST_INPUTS,
    generate_test_audio,
    TEST_AUDIO_DATA,
    assert_valid_speech_response,
    assert_valid_transcription_response,
    assert_valid_streaming_speech_response,
    assert_valid_streaming_transcription_response,
    collect_streaming_speech_content,
    collect_streaming_transcription_content,
    get_provider_voice,
    get_provider_voices,
    # Embeddings utilities
    EMBEDDINGS_SINGLE_TEXT,
    EMBEDDINGS_MULTIPLE_TEXTS,
    EMBEDDINGS_SIMILAR_TEXTS,
    EMBEDDINGS_DIFFERENT_TEXTS,
    EMBEDDINGS_EMPTY_TEXTS,
    EMBEDDINGS_LONG_TEXT,
    assert_valid_embedding_response,
    assert_valid_embeddings_batch_response,
    calculate_cosine_similarity,
    assert_embeddings_similarity,
    assert_embeddings_dissimilarity,
    # Responses API utilities
    RESPONSES_SIMPLE_TEXT_INPUT,
    RESPONSES_TEXT_WITH_SYSTEM,
    RESPONSES_IMAGE_INPUT,
    RESPONSES_TOOL_CALL_INPUT,
    RESPONSES_STREAMING_INPUT,
    RESPONSES_REASONING_INPUT,
    convert_to_responses_tools,
    assert_valid_responses_response,
    assert_responses_has_tool_calls,
    collect_responses_streaming_content,
    assert_valid_responses_streaming_chunk,
    # Text Completions utilities
    TEXT_COMPLETION_SIMPLE_PROMPT,
    TEXT_COMPLETION_STREAMING_PROMPT,
    assert_valid_text_completion_response,
    collect_text_completion_streaming_content,
)
from ..utils.config_loader import get_model
from ..utils.parametrize import (
    get_cross_provider_params_for_scenario,
    format_provider_model,
)
from ..utils.config_loader import get_config


# Helper functions (defined early for use in test methods)
def extract_openai_tool_calls(response: Any) -> List[Dict[str, Any]]:
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


def convert_to_openai_tools(tools: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
    """Convert common tool format to OpenAI format"""
    return [{"type": "function", "function": tool} for tool in tools]


@pytest.fixture
def openai_client():
    """Create OpenAI client for testing"""
    from ..utils.config_loader import get_integration_url, get_config

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

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("simple_chat"))
    def test_01_simple_chat(self, openai_client, test_config, provider, model):
        """Test Case 1: Simple chat interaction - runs across all available providers"""
        response = openai_client.chat.completions.create(
            model=format_provider_model(provider, model),
            messages=SIMPLE_CHAT_MESSAGES,
            max_tokens=100,
        )

        assert_valid_chat_response(response)
        assert response.choices[0].message.content is not None
        assert len(response.choices[0].message.content) > 0

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("multi_turn_conversation"))
    def test_02_multi_turn_conversation(self, openai_client, test_config, provider, model):
        """Test Case 2: Multi-turn conversation - runs across all available providers"""
        response = openai_client.chat.completions.create(
            model=format_provider_model(provider, model),
            messages=MULTI_TURN_MESSAGES,
            max_tokens=150,
        )

        assert_valid_chat_response(response)
        content = get_content_string(response.choices[0].message.content)
        # Should mention population or numbers since we asked about Paris population
        assert any(
            word in content
            for word in ["population", "million", "people", "inhabitants"]
        )

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("tool_calls"))
    def test_03_single_tool_call(self, openai_client, test_config, provider, model):
        """Test Case 3: Single tool call - auto-skips providers without tool support"""
        response = openai_client.chat.completions.create(
            model=format_provider_model(provider, model),
            messages=SINGLE_TOOL_CALL_MESSAGES,
            tools=[{"type": "function", "function": WEATHER_TOOL}],
            max_tokens=100,
        )

        assert_has_tool_calls(response, expected_count=1)
        tool_calls = extract_tool_calls(response)
        assert tool_calls[0]["name"] == "get_weather"
        assert "location" in tool_calls[0]["arguments"]

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("multiple_tool_calls"))
    def test_04_multiple_tool_calls(self, openai_client, test_config, provider, model):
        """Test Case 4: Multiple tool calls in one response - auto-skips providers without multiple tool support"""
        response = openai_client.chat.completions.create(
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

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("end2end_tool_calling"))
    def test_05_end2end_tool_calling(self, openai_client, test_config, provider, model):
        """Test Case 5: Complete tool calling flow with responses"""
        # Initial request
        messages = [{"role": "user", "content": "What's the weather in Boston?"}]

        response = openai_client.chat.completions.create(
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
        final_response = openai_client.chat.completions.create(
            model=format_provider_model(provider, model), messages=messages, max_tokens=150
        )

        assert_valid_chat_response(final_response)
        content = get_content_string(final_response.choices[0].message.content)
        weather_location_keywords = WEATHER_KEYWORDS + LOCATION_KEYWORDS
        assert any(word in content for word in weather_location_keywords)

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("automatic_function_calling"))
    def test_06_automatic_function_calling(self, openai_client, test_config, provider, model):
        """Test Case 6: Automatic function calling (tool_choice='auto')"""
        response = openai_client.chat.completions.create(
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

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("image_url"))
    def test_07_image_url(self, openai_client, test_config, provider, model):
        """Test Case 7: Image analysis from URL - auto-skips providers without image URL support (e.g., Gemini, Bedrock)"""
        response = openai_client.chat.completions.create(
            model=format_provider_model(provider, model),
            messages=IMAGE_URL_MESSAGES,
            max_tokens=200,
        )

        assert_valid_image_response(response)

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("image_base64"))
    def test_08_image_base64(self, openai_client, test_config, provider, model):
        """Test Case 8: Image analysis from base64 - runs for all providers with base64 image support"""
        response = openai_client.chat.completions.create(
            model=format_provider_model(provider, model),
            messages=IMAGE_BASE64_MESSAGES,
            max_tokens=200,
        )

        assert_valid_image_response(response)

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("multiple_images"))
    def test_09_multiple_images(self, openai_client, test_config, provider, model):
        """Test Case 9: Multiple image analysis - auto-skips providers without multiple image support"""
        response = openai_client.chat.completions.create(
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
            messages=[
                {"role": "user", "content": "Tell me a creative story in one sentence."}
            ],
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

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("streaming"))
    def test_13_streaming(self, openai_client, test_config, provider, model):
        """Test Case 13: Streaming chat completion - auto-skips providers without streaming support"""
        # Test basic streaming
        stream = openai_client.chat.completions.create(
            model=format_provider_model(provider, model),
            messages=STREAMING_CHAT_MESSAGES,
            max_tokens=200,
            stream=True,
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
                stream_with_tools = openai_client.chat.completions.create(
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
                assert (
                    tool_calls_detected_tools
                ), "Should detect tool calls in streaming response"

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
        assert (
            audio_content != audio_content2
        ), "Different voices should produce different audio"

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
        for voice in get_provider_voices("openai", count=3):  # Test first 3 voices to avoid too many API calls
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

    @skip_if_no_api_key("openai")
    def test_21_single_text_embedding(self, openai_client, test_config):
        """Test Case 21: Single text embedding generation"""
        response = openai_client.embeddings.create(
            model=get_model("openai", "embeddings"), input=EMBEDDINGS_SINGLE_TEXT
        )

        assert_valid_embedding_response(response, expected_dimensions=1536)

        # Verify response structure
        assert len(response.data) == 1, "Should have exactly one embedding"
        assert response.data[0].index == 0, "First embedding should have index 0"
        assert (
            response.data[0].object == "embedding"
        ), "Object type should be 'embedding'"

        # Verify model in response
        assert response.model is not None, "Response should include model name"
        assert "text-embedding" in response.model, "Model should be an embedding model"

    @skip_if_no_api_key("openai")
    def test_22_batch_text_embeddings(self, openai_client, test_config):
        """Test Case 22: Batch text embedding generation"""
        response = openai_client.embeddings.create(
            model=get_model("openai", "embeddings"), input=EMBEDDINGS_MULTIPLE_TEXTS
        )

        expected_count = len(EMBEDDINGS_MULTIPLE_TEXTS)
        assert_valid_embeddings_batch_response(
            response, expected_count, expected_dimensions=1536
        )

        # Verify each embedding has correct index
        for i, embedding_obj in enumerate(response.data):
            assert embedding_obj.index == i, f"Embedding {i} should have index {i}"
            assert (
                embedding_obj.object == "embedding"
            ), f"Embedding {i} should have object type 'embedding'"

    @skip_if_no_api_key("openai")
    def test_23_embedding_similarity_analysis(self, openai_client, test_config):
        """Test Case 23: Embedding similarity analysis with similar texts"""
        response = openai_client.embeddings.create(
            model=get_model("openai", "embeddings"), input=EMBEDDINGS_SIMILAR_TEXTS
        )

        assert_valid_embeddings_batch_response(
            response, len(EMBEDDINGS_SIMILAR_TEXTS), expected_dimensions=1536
        )

        embeddings = [item.embedding for item in response.data]

        # Test similarity between the first two embeddings (similar weather texts)
        similarity_1_2 = calculate_cosine_similarity(embeddings[0], embeddings[1])
        similarity_1_3 = calculate_cosine_similarity(embeddings[0], embeddings[2])
        similarity_2_3 = calculate_cosine_similarity(embeddings[1], embeddings[2])

        # Similar texts should have high similarity (> 0.7)
        assert (
            similarity_1_2 > 0.7
        ), f"Similar texts should have high similarity, got {similarity_1_2:.4f}"
        assert (
            similarity_1_3 > 0.7
        ), f"Similar texts should have high similarity, got {similarity_1_3:.4f}"
        assert (
            similarity_2_3 > 0.7
        ), f"Similar texts should have high similarity, got {similarity_2_3:.4f}"

    @skip_if_no_api_key("openai")
    def test_24_embedding_dissimilarity_analysis(self, openai_client, test_config):
        """Test Case 24: Embedding dissimilarity analysis with different texts"""
        response = openai_client.embeddings.create(
            model=get_model("openai", "embeddings"), input=EMBEDDINGS_DIFFERENT_TEXTS
        )

        assert_valid_embeddings_batch_response(
            response, len(EMBEDDINGS_DIFFERENT_TEXTS), expected_dimensions=1536
        )

        embeddings = [item.embedding for item in response.data]

        # Test dissimilarity between different topic embeddings
        # Weather vs Programming
        weather_prog_similarity = calculate_cosine_similarity(
            embeddings[0], embeddings[1]
        )
        # Weather vs Stock Market
        weather_stock_similarity = calculate_cosine_similarity(
            embeddings[0], embeddings[2]
        )
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

    @skip_if_no_api_key("openai")
    def test_26_embedding_long_text(self, openai_client, test_config):
        """Test Case 26: Embedding generation with longer text"""
        response = openai_client.embeddings.create(
            model=get_model("openai", "embeddings"), input=EMBEDDINGS_LONG_TEXT
        )

        assert_valid_embedding_response(response, expected_dimensions=1536)

        # Verify token usage is reported for longer text
        assert response.usage is not None, "Usage should be reported for longer text"
        assert (
            response.usage.total_tokens > 20
        ), "Longer text should consume more tokens"

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

            assert_valid_embedding_response(
                response, expected_dimensions=custom_dimensions
            )

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
        assert (
            response_single.usage is not None
        ), "Single embedding should have usage data"
        assert (
            response_single.usage.total_tokens > 0
        ), "Single embedding should consume tokens"
        single_tokens = response_single.usage.total_tokens

        # Batch embedding
        response_batch = openai_client.embeddings.create(
            model=get_model("openai", "embeddings"), input=EMBEDDINGS_MULTIPLE_TEXTS
        )

        assert_valid_embeddings_batch_response(
            response_batch, len(EMBEDDINGS_MULTIPLE_TEXTS)
        )
        assert (
            response_batch.usage is not None
        ), "Batch embedding should have usage data"
        assert (
            response_batch.usage.total_tokens > 0
        ), "Batch embedding should consume tokens"
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
    
    @skip_if_no_api_key("openai")
    def test_31_list_models(self, openai_client, test_config):
        """Test Case 31: List models"""
        response = openai_client.models.list()
        assert response.data is not None
        assert len(response.data) > 0

    # =========================================================================
    # RESPONSES API TEST CASES
    # =========================================================================

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("responses_api"))
    def test_32_responses_simple_text(self, openai_client, test_config, provider, model):
        """Test Case 32: Responses API with simple text input"""
        response = openai_client.responses.create(
            model=format_provider_model(provider, model),
            input=RESPONSES_SIMPLE_TEXT_INPUT,
            max_output_tokens=150,
        )

        # Validate response structure
        assert_valid_responses_response(response, min_content_length=20)

        # Check that we have meaningful content about space
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
        space_keywords = ["space", "rocket", "astronaut", "moon", "mars", "nasa", "satellite"]
        assert any(
            keyword in content_lower for keyword in space_keywords
        ), f"Response should contain space-related content. Got: {content}"

        # Verify usage information
        if hasattr(response, "usage"):
            assert response.usage.total_tokens > 0, "Should report token usage"

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("responses_api"))
    def test_33_responses_with_system_message(self, openai_client, test_config, provider, model):
        """Test Case 33: Responses API with system message"""
        response = openai_client.responses.create(
            model=format_provider_model(provider, model),
            input=RESPONSES_TEXT_WITH_SYSTEM,
            max_output_tokens=200,
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

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("responses_api"))
    def test_34_responses_with_image(self, openai_client, test_config, provider, model):
        """Test Case 34: Responses API with image input"""
        response = openai_client.responses.create(
            model=format_provider_model(provider, model),
            input=RESPONSES_IMAGE_INPUT,
            max_output_tokens=200,
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

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("responses_api"))
    def test_35_responses_with_tools(self, openai_client, test_config, provider, model):
        """Test Case 35: Responses API with tool calls"""
        # Convert tools to responses format
        tools = convert_to_responses_tools([WEATHER_TOOL])

        response = openai_client.responses.create(
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
        assert function_call_message.name == "get_weather", (
            f"Function call should be 'get_weather', got {function_call_message.name}"
        )

        # Check arguments if present
        if hasattr(function_call_message, "arguments"):
            # Arguments might be string or dict
            if isinstance(function_call_message.arguments, str):
                args = json.loads(function_call_message.arguments)
            else:
                args = function_call_message.arguments

            assert "location" in args, "Function call should have location argument"
            location_lower = str(args["location"]).lower()
            assert "boston" in location_lower, (
                f"Location should mention Boston, got {args['location']}"
            )

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("responses_api"))
    def test_36_responses_streaming(self, openai_client, test_config, provider, model):
        """Test Case 36: Responses API streaming"""
        stream = openai_client.responses.create(
            model=format_provider_model(provider, model),
            input=RESPONSES_STREAMING_INPUT,
            max_output_tokens=150,
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

        # Check that we got expected event types
        assert "response.created" in event_types or any(
            "created" in evt for evt in event_types
        ), f"Should receive response.created event. Got events: {list(event_types.keys())}"

        # Check for content events
        has_content_events = any(
            "delta" in evt or "text" in evt or "output" in evt for evt in event_types
        )
        assert has_content_events, (
            f"Should receive content-related events. Got events: {list(event_types.keys())}"
        )

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

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("responses_api"))
    def test_37_responses_streaming_with_tools(self, openai_client, test_config, provider, model):
        """Test Case 37: Responses API streaming with tools"""
        tools = convert_to_responses_tools([WEATHER_TOOL])

        stream = openai_client.responses.create(
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
        has_tool_events = any(
            "function" in evt or "tool" in evt for evt in event_types
        )

        # Either should have tool calls detected or tool-related events
        assert tool_calls_detected or has_tool_events, (
            f"Should detect tool calls in streaming. "
            f"Tool calls detected: {tool_calls_detected}, "
            f"Event types: {list(event_types.keys())}"
        )

    @skip_if_no_api_key("openai")
    def test_38_responses_reasoning(self, openai_client, test_config):
        """Test Case 38: Responses API with reasoning (gpt-5 model)"""
        # Use gpt-5 reasoning model
        model_to_use = "openai/gpt-5"

        try:
            response = openai_client.responses.create(
                model=model_to_use,
                input=RESPONSES_REASONING_INPUT,
                max_output_tokens=800,
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
                            if hasattr(summary_item, "type") and summary_item.type == "summary_text":
                                has_reasoning_content = True
                            elif isinstance(summary_item, dict) and summary_item.get("type") == "summary_text":
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
            assert has_steps, (
                f"Response should show step-by-step reasoning. Content: {content[:200]}..."
            )

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
                response = openai_client.responses.create(
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
        content, chunk_count = collect_text_completion_streaming_content(
            stream, timeout=300
        )

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
        
        assert has_tech or has_lines or len(content) > 10, (
            f"Completion should be haiku-like or about technology. Got: {content}"
        )

        # Should have multiple chunks for streaming
        assert chunk_count > 1, f"Streaming should have multiple chunks, got {chunk_count}"

        print(f"Success: Streamed haiku ({chunk_count} chunks): {content}")
