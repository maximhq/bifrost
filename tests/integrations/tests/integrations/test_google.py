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
26. Speech generation - streaming (if supported)
"""

import pytest
import base64
import requests
from PIL import Image
import io
import wave
from google import genai
from google.genai.types import HttpOptions
from google.genai import types
from typing import List, Dict, Any

from ..utils.common import (
    Config,
    SIMPLE_CHAT_MESSAGES,
    SINGLE_TOOL_CALL_MESSAGES,
    MULTIPLE_TOOL_CALL_MESSAGES,
    IMAGE_URL_SECONDARY,
    BASE64_IMAGE,
    INVALID_ROLE_MESSAGES,
    STREAMING_CHAT_MESSAGES,
    STREAMING_TOOL_CALL_MESSAGES,
    WEATHER_TOOL,
    CALCULATOR_TOOL,
    assert_valid_chat_response,
    assert_valid_embedding_response,
    assert_valid_image_response,
    assert_valid_error_response,
    assert_error_propagation,
    assert_valid_streaming_response,
    assert_valid_transcription_response,
    assert_valid_streaming_transcription_response,
    assert_valid_speech_response,
    collect_streaming_content,
    collect_streaming_transcription_content,
    generate_test_audio,
    get_provider_voice,
    get_provider_voices,
    get_api_key,
    skip_if_no_api_key,
    COMPARISON_KEYWORDS,
    WEATHER_KEYWORDS,
    LOCATION_KEYWORDS,
    GENAI_INVALID_ROLE_CONTENT,
    EMBEDDINGS_SINGLE_TEXT,
    SPEECH_TEST_INPUT,
)
from ..utils.config_loader import get_model
from ..utils.parametrize import (
    get_cross_provider_params_for_scenario,
    format_provider_model,
)


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
    from google.genai import types
    import io
    import base64

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


class TestGoogleIntegration:
    """Test suite for Google GenAI integration with cross-provider support"""

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("simple_chat"))
    def test_01_simple_chat(self, google_client, test_config, provider, model):
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
        """Test Case 7: Image analysis from URL"""
        image = load_image_from_url(IMAGE_URL_SECONDARY)


        response = google_client.models.generate_content(
            model=format_provider_model(provider, model),
            contents=["What do you see in this image?", image],
        )

        assert_valid_image_response(response)

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("image_base64"))
    def test_08_image_base64(self, google_client, test_config, provider, model):
        """Test Case 8: Image analysis from base64"""
        image = load_image_from_url(f"data:image/png;base64,{BASE64_IMAGE}")

        response = google_client.models.generate_content(
            model=format_provider_model(provider, model), contents=["Describe this image", image]
        )

        assert_valid_image_response(response)

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("multiple_images"))
    def test_09_multiple_images(self, google_client, test_config, provider, model):
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

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("streaming"))
    def test_13_streaming(self, google_client, test_config, provider, model):
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
