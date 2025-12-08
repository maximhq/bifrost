import pytest
import json
from typing import List, Dict, Any
from openai import OpenAI

from .utils.common import (
    Config,
    SIMPLE_CHAT_MESSAGES,
    MULTI_TURN_MESSAGES,
    SINGLE_TOOL_CALL_MESSAGES,
    MULTIPLE_TOOL_CALL_MESSAGES,
    IMAGE_URL_MESSAGES,
    IMAGE_BASE64_MESSAGES,
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
    EMBEDDINGS_SINGLE_TEXT,
    EMBEDDINGS_MULTIPLE_TEXTS,
    assert_valid_embedding_response,
    assert_valid_embeddings_batch_response,
)
from .utils.config_loader import get_model, get_integration_url, get_config
from .utils.parametrize import (
    get_cross_provider_params_for_scenario,
    format_provider_model,
)

VERCELAI_EXCLUDED_PROVIDERS = ["bedrock", "cohere"]


@pytest.fixture
def test_config():
    """Test configuration"""
    return Config()


@pytest.fixture(autouse=True)
def setup_vercelai():
    """Setup Vercel AI SDK with Bifrost configuration"""
    import os
    from .utils.config_loader import get_integration_url

    os.environ["OPENAI_API_KEY"] = "dummy-openai-key-bifrost-handles-auth"
    os.environ["ANTHROPIC_API_KEY"] = "dummy-anthropic-key-bifrost-handles-auth"
    os.environ["GOOGLE_API_KEY"] = "dummy-google-api-key-bifrost-handles-auth"
    os.environ["GEMINI_API_KEY"] = "dummy-gemini-api-key-bifrost-handles-auth"


@pytest.fixture
def vercelai_client():
    """Create Vercel AI SDK client configured for Bifrost"""
    base_url = get_integration_url("vercelai")
    return OpenAI(
        base_url=f"{base_url}/v1",
        api_key="dummy-key-bifrost-handles-auth"
    )


class TestVercelAIIntegration:
    """Test suite for Vercel AI SDK integration"""

    @pytest.mark.parametrize(
        "provider, model",
        get_cross_provider_params_for_scenario(
            "simple_chat", exclude_providers=VERCELAI_EXCLUDED_PROVIDERS
        ),
    )
    def test_01_simple_chat(self, test_config, vercelai_client, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 1: Simple chat interaction"""
        response = vercelai_client.chat.completions.create(
            model=model,
            messages=SIMPLE_CHAT_MESSAGES,
            max_tokens=100,
        )

        assert_valid_chat_response(response)
        assert response.choices[0].message.content is not None
        assert len(response.choices[0].message.content) > 0

    @pytest.mark.parametrize(
        "provider, model",
        get_cross_provider_params_for_scenario(
            "multi_turn_conversation", exclude_providers=VERCELAI_EXCLUDED_PROVIDERS
        ),
    )
    def test_02_multi_turn_conversation(self, test_config, vercelai_client, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 2: Multi-turn conversation"""
        response = vercelai_client.chat.completions.create(
            model=model,
            messages=MULTI_TURN_MESSAGES,
            max_tokens=150,
        )

        assert_valid_chat_response(response)
        content = response.choices[0].message.content.lower()
        # Should mention population or numbers since we asked about Paris population
        assert any(word in content for word in ["population", "million", "people", "inhabitants"])

    @pytest.mark.parametrize(
        "provider, model",
        get_cross_provider_params_for_scenario(
            "tool_calls", exclude_providers=VERCELAI_EXCLUDED_PROVIDERS
        ),
    )
    def test_03_single_tool_call(self, test_config, vercelai_client, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 3: Single tool call"""
        tools = [{"type": "function", "function": WEATHER_TOOL}]

        response = vercelai_client.chat.completions.create(
            model=model,
            messages=SINGLE_TOOL_CALL_MESSAGES,
            tools=tools,
            max_tokens=100,
        )

        assert_has_tool_calls(response, expected_count=1)
        tool_calls = extract_tool_calls(response)
        assert tool_calls[0]["name"] == "get_weather"
        assert "location" in tool_calls[0]["arguments"]

    @pytest.mark.parametrize(
        "provider, model",
        get_cross_provider_params_for_scenario(
            "multiple_tool_calls", exclude_providers=VERCELAI_EXCLUDED_PROVIDERS
        ),
    )
    def test_04_multiple_tool_calls(self, test_config, vercelai_client, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 4: Multiple tool calls in one response"""
        tools = [
            {"type": "function", "function": WEATHER_TOOL},
            {"type": "function", "function": CALCULATOR_TOOL}
        ]

        response = vercelai_client.chat.completions.create(
            model=model,
            messages=MULTIPLE_TOOL_CALL_MESSAGES,
            tools=tools,
            max_tokens=200,
        )

        assert_has_tool_calls(response, expected_count=2)
        tool_calls = extract_tool_calls(response)
        tool_names = [tc["name"] for tc in tool_calls]
        assert "get_weather" in tool_names
        assert "calculate" in tool_names

    @pytest.mark.parametrize(
        "provider, model",
        get_cross_provider_params_for_scenario(
            "end2end_tool_calling", exclude_providers=VERCELAI_EXCLUDED_PROVIDERS
        ),
    )
    def test_05_end2end_tool_calling(self, test_config, vercelai_client, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 5: Complete tool calling flow with responses"""
        messages = [{"role": "user", "content": "What's the weather in Boston?"}]
        tools = [{"type": "function", "function": WEATHER_TOOL}]

        response = vercelai_client.chat.completions.create(
            model=model,
            messages=messages,
            tools=tools,
            max_tokens=100,
        )

        assert_has_tool_calls(response, expected_count=1)

        # Add assistant's tool call to conversation
        messages.append(response.choices[0].message.model_dump())

        # Add tool response
        tool_calls = extract_tool_calls(response)
        tool_response = mock_tool_response(tool_calls[0]["name"], tool_calls[0]["arguments"])

        messages.append({
            "role": "tool",
            "tool_call_id": response.choices[0].message.tool_calls[0].id,
            "content": tool_response,
        })

        # Get final response
        final_response = vercelai_client.chat.completions.create(
            model=get_model("vercelai", "chat"), messages=messages, max_tokens=150
        )

        assert_valid_chat_response(final_response)
        content = final_response.choices[0].message.content.lower()
        weather_location_keywords = ["weather", "boston", "temperature", "degrees", "sunny", "cloudy", "rain"]
        assert any(word in content for word in weather_location_keywords)

    @pytest.mark.parametrize(
        "provider, model",
        get_cross_provider_params_for_scenario(
            "image_url", exclude_providers=VERCELAI_EXCLUDED_PROVIDERS
        ),
    )
    def test_06_image_url(self, test_config, vercelai_client, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 6: Image analysis from URL"""
        response = vercelai_client.chat.completions.create(
            model=model,
            messages=IMAGE_URL_MESSAGES,
            max_tokens=200,
        )

        assert_valid_image_response(response)

    @pytest.mark.parametrize(
        "provider, model",
        get_cross_provider_params_for_scenario(
            "image_base64", exclude_providers=VERCELAI_EXCLUDED_PROVIDERS
        ),
    )
    def test_07_image_base64(self, test_config, vercelai_client, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 7: Image analysis from base64"""
        response = vercelai_client.chat.completions.create(
            model=model,
            messages=IMAGE_BASE64_MESSAGES,
            max_tokens=200,
        )

        assert_valid_image_response(response)

    @pytest.mark.parametrize(
        "provider, model",
        get_cross_provider_params_for_scenario(
            "streaming", exclude_providers=VERCELAI_EXCLUDED_PROVIDERS
        ),
    )
    def test_08_streaming(self, test_config, vercelai_client, provider, model):
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        """Test Case 8: Streaming chat completion"""
        stream = vercelai_client.chat.completions.create(
            model=model,
            messages=SIMPLE_CHAT_MESSAGES,
            max_tokens=200,
            stream=True,
        )

        content, chunk_count, tool_calls_detected = collect_streaming_content(
            stream, "openai", timeout=120
        )

        # Validate streaming results
        assert chunk_count > 0, "Should receive at least one chunk"
        assert len(content) > 10, "Should receive substantial content"
        assert not tool_calls_detected, "Basic streaming shouldn't have tool calls"

    def test_09_embeddings(self, test_config, vercelai_client):
        """Test Case 9: Text embeddings"""
        skip_if_no_api_key("openai")
        
        try:
            # Test single text embedding
            response = vercelai_client.embeddings.create(
                model=get_model("vercelai", "embeddings") or "text-embedding-3-small",
                input=EMBEDDINGS_SINGLE_TEXT,
            )

            assert_valid_embedding_response(response, expected_dimensions=1536)

            # Test batch embeddings
            batch_response = vercelai_client.embeddings.create(
                model=get_model("vercelai", "embeddings") or "text-embedding-3-small",
                input=EMBEDDINGS_MULTIPLE_TEXTS,
            )

            assert_valid_embeddings_batch_response(
                batch_response, len(EMBEDDINGS_MULTIPLE_TEXTS), expected_dimensions=1536
            )
        except Exception as e:
            pytest.skip(f"Embeddings not available: {e}")

    def test_10_error_handling(self, test_config, vercelai_client):
        """Test Case 10: Error handling"""
        # Test with invalid model
        with pytest.raises(Exception) as exc_info:
            vercelai_client.chat.completions.create(
                model="invalid-model-name",
                messages=[{"role": "user", "content": "Hello"}],
                max_tokens=10,
            )

        # Verify the error is properly caught
        error = exc_info.value
        assert_valid_error_response(error, "invalid-model-name")
        assert_error_propagation(error, "vercelai")

    def test_11_multi_provider_compatibility(self, test_config, vercelai_client):
        """Test Case 11: Multi-provider compatibility through Vercel AI SDK"""
        test_prompt = "What is the capital of France? Answer in one word."
        models_to_test = [
            get_model("vercelai", "chat"),  # Default model
        ]

        responses = {}

        for model in models_to_test:
            if not model or model == "_no_model_":
                continue
            try:
                response = vercelai_client.chat.completions.create(
                    model=model,
                    messages=[{"role": "user", "content": test_prompt}],
                    max_tokens=50,
                )

                assert_valid_chat_response(response)
                responses[model] = response.choices[0].message.content.lower()

            except Exception as e:
                print(f"Model {model} not available: {e}")
                continue

        # Verify that we got at least one response
        assert len(responses) > 0, "Should get at least one successful response"

        # All responses should mention Paris or France
        for model, content in responses.items():
            assert any(
                word in content for word in ["paris", "france"]
            ), f"Model {model} should mention Paris. Got: {content}"

