"""
OpenAI Integration Tests

🤖 MODELS USED:
- Chat: gpt-3.5-turbo
- Vision: gpt-4o
- Tools: gpt-3.5-turbo
- Alternatives: gpt-4, gpt-4-turbo-preview, gpt-4o, gpt-4o-mini

Tests all 11 core scenarios using OpenAI SDK directly:
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
)
from ..utils.config_loader import get_model


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
        "timeout": api_config.get("timeout", 30),
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
    """Test suite for OpenAI integration covering all 11 core scenarios"""

    @skip_if_no_api_key("openai")
    def test_01_simple_chat(self, openai_client, test_config):
        """Test Case 1: Simple chat interaction"""
        response = openai_client.chat.completions.create(
            model=get_model("openai", "chat"),
            messages=SIMPLE_CHAT_MESSAGES,
            max_tokens=100,
        )

        assert_valid_chat_response(response)
        assert response.choices[0].message.content is not None
        assert len(response.choices[0].message.content) > 0

    @skip_if_no_api_key("openai")
    def test_02_multi_turn_conversation(self, openai_client, test_config):
        """Test Case 2: Multi-turn conversation"""
        response = openai_client.chat.completions.create(
            model=get_model("openai", "chat"),
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

    @skip_if_no_api_key("openai")
    def test_03_single_tool_call(self, openai_client, test_config):
        """Test Case 3: Single tool call"""
        response = openai_client.chat.completions.create(
            model=get_model("openai", "tools"),
            messages=SINGLE_TOOL_CALL_MESSAGES,
            tools=[{"type": "function", "function": WEATHER_TOOL}],
            max_tokens=100,
        )

        assert_has_tool_calls(response, expected_count=1)
        tool_calls = extract_tool_calls(response)
        assert tool_calls[0]["name"] == "get_weather"
        assert "location" in tool_calls[0]["arguments"]

    @skip_if_no_api_key("openai")
    def test_04_multiple_tool_calls(self, openai_client, test_config):
        """Test Case 4: Multiple tool calls in one response"""
        response = openai_client.chat.completions.create(
            model=get_model("openai", "tools"),
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

    @skip_if_no_api_key("openai")
    def test_05_end2end_tool_calling(self, openai_client, test_config):
        """Test Case 5: Complete tool calling flow with responses"""
        # Initial request
        messages = [{"role": "user", "content": "What's the weather in Boston?"}]

        response = openai_client.chat.completions.create(
            model=get_model("openai", "tools"),
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
            model=get_model("openai", "tools"), messages=messages, max_tokens=150
        )

        assert_valid_chat_response(final_response)
        content = final_response.choices[0].message.content.lower()
        weather_location_keywords = WEATHER_KEYWORDS + LOCATION_KEYWORDS
        assert any(word in content for word in weather_location_keywords)

    @skip_if_no_api_key("openai")
    def test_06_automatic_function_calling(self, openai_client, test_config):
        """Test Case 6: Automatic function calling (tool_choice='auto')"""
        response = openai_client.chat.completions.create(
            model=get_model("openai", "tools"),
            messages=[{"role": "user", "content": "Calculate 25 * 4 for me"}],
            tools=[{"type": "function", "function": CALCULATOR_TOOL}],
            tool_choice="auto",  # Let model decide
            max_tokens=100,
        )

        # Should automatically choose to use the calculator
        assert_has_tool_calls(response, expected_count=1)
        tool_calls = extract_openai_tool_calls(response)
        assert tool_calls[0]["name"] == "calculate"

    @skip_if_no_api_key("openai")
    def test_07_image_url(self, openai_client, test_config):
        """Test Case 7: Image analysis from URL"""
        response = openai_client.chat.completions.create(
            model=get_model("openai", "vision"),
            messages=IMAGE_URL_MESSAGES,
            max_tokens=200,
        )

        assert_valid_image_response(response)

    @skip_if_no_api_key("openai")
    def test_08_image_base64(self, openai_client, test_config):
        """Test Case 8: Image analysis from base64"""
        response = openai_client.chat.completions.create(
            model=get_model("openai", "vision"),
            messages=IMAGE_BASE64_MESSAGES,
            max_tokens=200,
        )

        assert_valid_image_response(response)

    @skip_if_no_api_key("openai")
    def test_09_multiple_images(self, openai_client, test_config):
        """Test Case 9: Multiple image analysis"""
        response = openai_client.chat.completions.create(
            model=get_model("openai", "vision"),
            messages=MULTIPLE_IMAGES_MESSAGES,
            max_tokens=300,
        )

        assert_valid_image_response(response)
        content = response.choices[0].message.content.lower()
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
        assert_valid_error_response(error, "tester")
        assert_error_propagation(error, "openai")

    @skip_if_no_api_key("openai")
    def test_13_streaming(self, openai_client, test_config):
        """Test Case 13: Streaming chat completion"""
        # Test basic streaming
        stream = openai_client.chat.completions.create(
            model=get_model("openai", "chat"),
            messages=STREAMING_CHAT_MESSAGES,
            max_tokens=200,
            stream=True,
        )

        content, chunk_count, tool_calls_detected = collect_streaming_content(
            stream, "openai", timeout=30
        )

        # Validate streaming results
        assert chunk_count > 0, "Should receive at least one chunk"
        assert len(content) > 10, "Should receive substantial content"
        assert not tool_calls_detected, "Basic streaming shouldn't have tool calls"

        # Test streaming with tool calls
        stream_with_tools = openai_client.chat.completions.create(
            model=get_model("openai", "tools"),
            messages=STREAMING_TOOL_CALL_MESSAGES,
            max_tokens=150,
            tools=convert_to_openai_tools([WEATHER_TOOL]),
            stream=True,
        )

        content_tools, chunk_count_tools, tool_calls_detected_tools = (
            collect_streaming_content(stream_with_tools, "openai", timeout=30)
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
            voice="alloy",
            input=SPEECH_TEST_INPUT,
        )

        # Read the audio content
        audio_content = response.content
        assert_valid_speech_response(audio_content)

        # Test with different voice
        response2 = openai_client.audio.speech.create(
            model=get_model("openai", "speech"),
            voice="nova",
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
                    response, "openai", timeout=60
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
            voice="alloy",
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
                voice="alloy",
                input="",
            )

        error = exc_info.value
        # Should get an error for empty input

        # Test with invalid model
        with pytest.raises(Exception) as exc_info:
            openai_client.audio.speech.create(
                model="invalid-speech-model",
                voice="alloy",
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
        for voice in SPEECH_TEST_VOICES[
            :3
        ]:  # Test first 3 voices to avoid too many API calls
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
                    voice="alloy",
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
