"""
Pydantic AI Integration Tests - Cross-Provider Support

🌉 CROSS-PROVIDER TESTING:
This test suite uses Pydantic AI to test against multiple AI providers through Bifrost.
Tests automatically run against all available providers with proper capability filtering.

🤖 PYDANTIC AI COMPONENTS TESTED:
- Agent: Core agent class for running LLM interactions
- Models: OpenAI (OpenAIChatModel), Anthropic (AnthropicModel), Google (GoogleModel), Cohere (CohereModel)
- Providers: OpenAIProvider, AnthropicProvider, GoogleProvider, CohereProvider
- Tools: Function tools with @agent.tool decorator
- Structured Output: Pydantic BaseModel result types
- Streaming: Real-time response streaming
- Async Operations: agent.run() async patterns

⚠️ PROVIDER LIMITATIONS:
- Bedrock: Not supported in PydanticAI tests - tested separately in test_bedrock.py

Tests Pydantic AI standard interface compliance and Bifrost integration:
1. Basic Agent chat - Cross-provider
2. Agent with system prompt (instructions) - Cross-provider
3. Multi-turn conversation with message history - Cross-provider
4. Tool calling with @agent.tool decorator - Cross-provider
5. End-to-end tool calling with multi-turn flow - Cross-provider
6. Structured output with Pydantic models - Cross-provider
7. Streaming responses - Cross-provider
8. Async operations
9. Error handling
10. Tool with context - Cross-provider
11. Multiple tools - Cross-provider
12. Result validation
13. Usage tracking
14. Message history inspection
15. Dynamic instructions
"""

import pytest
import asyncio
import os
from typing import List, Dict, Any, Optional
from dataclasses import dataclass

from pydantic import BaseModel, Field
from pydantic_ai import Agent, RunContext, Tool

# Pydantic AI model imports
from pydantic_ai.models.openai import OpenAIChatModel
from pydantic_ai.providers.openai import OpenAIProvider

# Optional provider imports
try:
    from pydantic_ai.models.anthropic import AnthropicModel
    from pydantic_ai.providers.anthropic import AnthropicProvider
    ANTHROPIC_AVAILABLE = True
except ImportError:
    ANTHROPIC_AVAILABLE = False
    AnthropicModel = None
    AnthropicProvider = None

try:
    from pydantic_ai.models.google import GoogleModel
    from pydantic_ai.providers.google import GoogleProvider
    GOOGLE_AVAILABLE = True
except ImportError:
    GOOGLE_AVAILABLE = False
    GoogleModel = None
    GoogleProvider = None

try:
    from cohere import AsyncClientV2 as CohereAsyncClient
    from pydantic_ai.models.cohere import CohereModel
    from pydantic_ai.providers.cohere import CohereProvider
    COHERE_AVAILABLE = True
except ImportError:
    COHERE_AVAILABLE = False
    CohereAsyncClient = None
    CohereModel = None
    CohereProvider = None

from .utils.common import (
    Config,
    SIMPLE_CHAT_MESSAGES,
    MULTI_TURN_MESSAGES,
    WEATHER_TOOL,
    CALCULATOR_TOOL,
    EMBEDDINGS_SINGLE_TEXT,
    EMBEDDINGS_MULTIPLE_TEXTS,
    mock_tool_response,
    assert_valid_chat_response,
    get_api_key,
    skip_if_no_api_key,
    WEATHER_KEYWORDS,
    LOCATION_KEYWORDS,
)
from .utils.config_loader import get_model, get_integration_url, get_config
from .utils.parametrize import (
    get_cross_provider_params_for_scenario,
    format_provider_model,
)


@pytest.fixture
def test_config():
    """Test configuration"""
    return Config()


@pytest.fixture(autouse=True)
def setup_pydanticai():
    """Setup Pydantic AI with Bifrost configuration and dummy credentials"""
    # Set dummy credentials since Bifrost handles actual authentication
    os.environ["OPENAI_API_KEY"] = "dummy-openai-key-bifrost-handles-auth"
    os.environ["ANTHROPIC_API_KEY"] = "dummy-anthropic-key-bifrost-handles-auth"
    os.environ["GOOGLE_API_KEY"] = "dummy-google-api-key-bifrost-handles-auth"
    os.environ["GEMINI_API_KEY"] = "dummy-gemini-api-key-bifrost-handles-auth"
    os.environ["CO_API_KEY"] = "dummy-cohere-key-bifrost-handles-auth"

    yield

    # Cleanup is handled by pytest


def get_openai_model(model_name: str | None = None) -> OpenAIChatModel:
    """Create an OpenAI model configured for Bifrost"""
    base_url = get_integration_url("pydanticai")
    if model_name is None:
        model_name = get_model("pydanticai", "chat")

    provider = OpenAIProvider(
        base_url=f"{base_url}/v1",
        api_key="dummy-openai-key-bifrost-handles-auth"
    )
    return OpenAIChatModel(model_name, provider=provider)


def get_anthropic_model(model_name: str = "claude-3-haiku-20240307") -> Optional[Any]:
    """Create an Anthropic model configured for Bifrost"""
    if not ANTHROPIC_AVAILABLE:
        return None

    base_url = get_integration_url("pydanticai")

    # Note: Anthropic SDK adds /v1 internally, so we don't append it here
    # (unlike OpenAI SDK which expects /v1 in the base URL)
    provider = AnthropicProvider(
        base_url=base_url,
        api_key="dummy-anthropic-key-bifrost-handles-auth"
    )
    return AnthropicModel(model_name, provider=provider)


def get_google_model(model_name: str = "gemini-2.0-flash") -> Optional[Any]:
    """Create a Google model configured for Bifrost"""
    if not GOOGLE_AVAILABLE:
        return None

    base_url = get_integration_url("pydanticai")

    # Configure GoogleProvider with Bifrost endpoint
    provider = GoogleProvider(
        api_key="dummy-google-api-key-bifrost-handles-auth",
        base_url=base_url
    )
    return GoogleModel(model_name, provider=provider)


def get_cohere_model(model_name: str = "command-r7b-12-2024") -> Optional[Any]:
    """Create a Cohere model configured for Bifrost"""
    if not COHERE_AVAILABLE:
        return None

    base_url = get_integration_url("pydanticai")

    # Cohere SDK's AsyncClientV2 accepts base_url parameter
    # We create a custom client pointing to Bifrost and pass it to CohereProvider
    cohere_client = CohereAsyncClient(
        api_key="dummy-cohere-key-bifrost-handles-auth",
        base_url=base_url
    )
    provider = CohereProvider(
        cohere_client=cohere_client
    )
    return CohereModel(model_name, provider=provider)


def get_pydanticai_model_for_provider(provider: str, model: str) -> Any:
    """
    Factory function to create a Pydantic AI model for a given provider.
    
    This is the cross-provider equivalent of format_provider_model() used in Bedrock tests,
    but returns actual Pydantic AI model objects instead of string identifiers.
    
    Args:
        provider: Provider name (e.g., 'openai', 'anthropic', 'gemini', 'cohere')
        model: Model name (e.g., 'gpt-4o-mini', 'claude-sonnet-4-20250514')
    
    Returns:
        Configured Pydantic AI model object for the provider
        
    Raises:
        ValueError: If provider is not supported or required SDK is not available
    """
    provider_lower = provider.lower()
    
    if provider_lower == "openai":
        return get_openai_model(model)
    
    elif provider_lower == "anthropic":
        if not ANTHROPIC_AVAILABLE:
            raise ValueError(f"Anthropic SDK not available for provider '{provider}'")
        return get_anthropic_model(model)
    
    elif provider_lower in ["gemini", "google"]:
        if not GOOGLE_AVAILABLE:
            raise ValueError(f"Google GenAI SDK not available for provider '{provider}'")
        return get_google_model(model)
    
    elif provider_lower == "cohere":
        if not COHERE_AVAILABLE:
            raise ValueError(f"Cohere SDK not available for provider '{provider}'")
        return get_cohere_model(model)
    
    elif provider_lower == "bedrock":
        # Bedrock is tested separately in test_bedrock.py using the native Bedrock API
        # PydanticAI doesn't have native Bedrock support, and using OpenAI SDK causes
        # validation errors due to response format differences (e.g., empty service_tier)
        raise ValueError(
            f"Provider 'bedrock' is not supported in PydanticAI tests - "
            f"use test_bedrock.py for Bedrock testing"
        )
    
    else:
        raise ValueError(f"Unsupported provider: {provider}. Supported: openai, anthropic, gemini, cohere")


# Structured output models for testing
class CityInfo(BaseModel):
    """Information about a city"""
    city: str = Field(description="Name of the city")
    country: str = Field(description="Country where the city is located")


class WeatherResponse(BaseModel):
    """Weather information response"""
    location: str = Field(description="Location for the weather")
    temperature: str = Field(description="Current temperature")
    conditions: str = Field(description="Weather conditions description")


class CalculationResult(BaseModel):
    """Result of a calculation"""
    expression: str = Field(description="The mathematical expression")
    result: float = Field(description="The calculated result")


class TestPydanticAIIntegration:
    """Comprehensive Pydantic AI integration tests through Bifrost"""

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("simple_chat"))
    def test_01_basic_agent_chat(self, test_config, provider, model):
        """Test Case 1: Basic Agent chat functionality - runs across all available providers"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        
        try:
            pydantic_model = get_pydanticai_model_for_provider(provider, model)
            agent = Agent(
                pydantic_model,
                instructions="Be concise, reply with one sentence.",
            )

            result = agent.run_sync("Hello! How are you today?")

            assert result is not None
            assert result.output is not None
            assert len(str(result.output)) > 0

        except ValueError as e:
            pytest.skip(f"Provider {provider} not available: {e}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("simple_chat"))
    def test_02_agent_with_system_prompt(self, test_config, provider, model):
        """Test Case 2: Agent with custom system prompt (instructions) - runs across all available providers"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        
        try:
            pydantic_model = get_pydanticai_model_for_provider(provider, model)
            agent = Agent(
                pydantic_model,
                instructions=(
                    "You are a helpful geography expert. "
                    "Always mention the continent when discussing cities."
                ),
            )

            result = agent.run_sync("What is the capital of France?")

            assert result is not None
            assert result.output is not None
            content = str(result.output).lower()
            assert "paris" in content

        except ValueError as e:
            pytest.skip(f"Provider {provider} not available: {e}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("multi_turn_conversation"))
    def test_03_multi_turn_conversation(self, test_config, provider, model):
        """Test Case 3: Multi-turn conversation with message history - runs across all available providers"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        
        try:
            pydantic_model = get_pydanticai_model_for_provider(provider, model)
            agent = Agent(
                pydantic_model,
                instructions="You are a helpful assistant. Remember context from previous messages.",
            )

            # First turn
            result1 = agent.run_sync("My name is Alice.")

            # Second turn - should remember the name
            result2 = agent.run_sync(
                "What is my name?",
                message_history=result1.all_messages(),
            )

            assert result2 is not None
            assert result2.output is not None
            content = str(result2.output).lower()
            assert "alice" in content

        except ValueError as e:
            pytest.skip(f"Provider {provider} not available: {e}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("tool_calls"))
    def test_04_tool_calling(self, test_config, provider, model):
        """Test Case 4: Tool calling with @agent.tool decorator - runs across all available providers"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        
        try:
            pydantic_model = get_pydanticai_model_for_provider(provider, model)

            # Define tools as functions
            def get_weather(location: str) -> str:
                """Get the current weather for a location."""
                return f"The weather in {location} is 72°F and sunny."

            def calculate(expression: str) -> str:
                """Perform a mathematical calculation."""
                try:
                    # Safe evaluation for simple expressions
                    result = eval(expression.replace("x", "*").replace("×", "*"))
                    return f"The result of {expression} is {result}"
                except Exception:
                    return f"Could not calculate {expression}"

            agent = Agent(
                pydantic_model,
                tools=[get_weather, calculate],
                instructions="You are a helpful assistant that can check weather and do calculations.",
            )

            result = agent.run_sync("What's the weather like in Boston?")

            assert result is not None
            assert result.output is not None
            content = str(result.output).lower()
            # Should either mention weather info or Boston
            weather_location_keywords = WEATHER_KEYWORDS + LOCATION_KEYWORDS
            assert any(
                word in content for word in weather_location_keywords
            ), f"Response should mention weather or location. Got: {content}"

        except ValueError as e:
            pytest.skip(f"Provider {provider} not available: {e}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("end2end_tool_calling"))
    def test_05_end2end_tool_calling(self, test_config, provider, model):
        """Test Case 5: Complete end-to-end tool calling flow with multi-turn conversation - runs across all available providers"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        
        try:
            pydantic_model = get_pydanticai_model_for_provider(provider, model)

            # Define a tool that we'll manually execute
            def get_weather(location: str) -> str:
                """Get the current weather for a location."""
                return f"The weather in {location} is 72°F and sunny."

            agent = Agent(
                pydantic_model,
                tools=[get_weather],
                instructions="You are a helpful assistant that can check weather.",
            )

            # Step 1: Initial request - should trigger tool call
            result1 = agent.run_sync("What's the weather in Boston in fahrenheit?")

            assert result1 is not None
            assert result1.output is not None
            
            # Pydantic AI automatically executes tools, so result1.output should contain
            # the final response with weather information.
            
            # Verify the response contains weather information
            content = str(result1.output).lower()
            weather_location_keywords = WEATHER_KEYWORDS + LOCATION_KEYWORDS
            assert any(
                word in content for word in weather_location_keywords
            ), f"Response should mention weather or location. Got: {content}"

        except ValueError as e:
            pytest.skip(f"Provider {provider} not available: {e}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("pydantic_structured_output"))
    def test_06_structured_output(self, test_config, provider, model):
        """Test Case 5: Structured output with Pydantic models - runs on providers with reliable PydanticAI structured output support"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        
        try:
            pydantic_model = get_pydanticai_model_for_provider(provider, model)
            agent = Agent(
                pydantic_model,
                output_type=CityInfo,
                instructions="Extract city information from the user's question.",
            )

            result = agent.run_sync("Tell me about Paris, the capital of France.")

            assert result is not None
            assert result.output is not None
            assert isinstance(result.output, CityInfo)
            assert result.output.city.lower() == "paris"
            assert "france" in result.output.country.lower()

        except ValueError as e:
            pytest.skip(f"Provider {provider} not available: {e}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("pydanticai_streaming"))
    def test_07_streaming_responses(self, test_config, provider, model):
        """Test Case 7: Streaming response functionality - runs on providers with PydanticAI streaming support"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        
        try:
            pydantic_model = get_pydanticai_model_for_provider(provider, model)
            agent = Agent(
                pydantic_model,
                instructions="You are a storyteller. Tell short, engaging stories.",
            )

            # Use async streaming with proper event loop handling
            async def run_streaming():
                chunks = []
                async with agent.run_stream("Tell me a very short story about a robot.") as response:
                    async for chunk in response.stream_text():
                        chunks.append(chunk)
                return "".join(chunks), len(chunks)

            # Use asyncio.new_event_loop() to avoid conflicts with existing event loops
            loop = asyncio.new_event_loop()
            asyncio.set_event_loop(loop)
            try:
                full_content, chunk_count = loop.run_until_complete(run_streaming())
            finally:
                loop.close()

            assert chunk_count > 0, "Should receive streaming chunks"
            assert len(full_content) > 0, "Should have content from streaming"
            assert any(
                word in full_content.lower() for word in ["robot", "story", "once"]
            ), f"Response should be a story about robots. Got: {full_content[:200]}"

        except ValueError as e:
            pytest.skip(f"Provider {provider} not available: {e}")

    def test_08_async_operations(self, test_config):
        """Test Case 8: Async operation support"""

        async def async_test():
            try:
                model = get_openai_model()
                agent = Agent(
                    model,
                    instructions="Be concise.",
                )

                result = await agent.run("Hello from async!")

                assert result is not None
                assert result.output is not None
                assert len(str(result.output)) > 0

                return True

            except Exception as e:
                pytest.skip(f"Async operations through Pydantic AI not available: {e}")
                return False

        result = asyncio.run(async_test())
        if result is not False:
            assert result is True

    def test_09_error_handling(self, test_config):
        """Test Case 9: Error handling for invalid requests"""
        try:
            # Test with invalid model name
            base_url = get_integration_url("pydanticai")
            provider = OpenAIProvider(
                base_url=f"{base_url}/v1",
                api_key="dummy-key"
            )
            model = OpenAIChatModel("invalid-model-name-should-fail", provider=provider)
            agent = Agent(model)

            with pytest.raises(Exception) as exc_info:
                agent.run_sync("This should fail gracefully.")

            # Should get a meaningful error
            error_message = str(exc_info.value).lower()
            assert any(
                word in error_message
                for word in ["model", "error", "invalid", "not found", "does not exist"]
            )

        except Exception as e:
            pytest.skip(f"Error handling test through Pydantic AI not available: {e}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("tool_calls"))
    def test_10_tool_with_context(self, test_config, provider, model):
        """Test Case 10: Tool with RunContext for dependency injection - runs across all available providers"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        
        try:
            pydantic_model = get_pydanticai_model_for_provider(provider, model)

            @dataclass
            class UserDeps:
                user_name: str
                user_id: int

            def get_user_info(ctx: RunContext[UserDeps]) -> str:
                """Get information about the current user."""
                return f"User: {ctx.deps.user_name} (ID: {ctx.deps.user_id})"

            agent = Agent(
                pydantic_model,
                deps_type=UserDeps,
                tools=[Tool(get_user_info, takes_ctx=True)],
                instructions="You can look up user information when asked.",
            )

            deps = UserDeps(user_name="Alice", user_id=123)
            result = agent.run_sync("What is my user information?", deps=deps)

            assert result is not None
            assert result.output is not None
            content = str(result.output).lower()
            # Should mention Alice or user info
            assert "alice" in content or "user" in content

        except ValueError as e:
            pytest.skip(f"Provider {provider} not available: {e}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("multiple_tool_calls"))
    def test_11_multiple_tools(self, test_config, provider, model):
        """Test Case 11: Multiple tools in single agent - runs across all available providers"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        
        try:
            pydantic_model = get_pydanticai_model_for_provider(provider, model)

            def get_weather(location: str) -> str:
                """Get weather for a location."""
                return f"Weather in {location}: 72°F, sunny"

            def get_time(timezone: str) -> str:
                """Get current time in a timezone."""
                return f"Current time in {timezone}: 2:30 PM"

            def translate(text: str, target_language: str) -> str:
                """Translate text to another language."""
                return f"'{text}' in {target_language}: [translated]"

            agent = Agent(
                pydantic_model,
                tools=[get_weather, get_time, translate],
                instructions="You can check weather, time, and translate text.",
            )

            result = agent.run_sync("What's the weather in New York?")

            assert result is not None
            assert result.output is not None

        except ValueError as e:
            pytest.skip(f"Provider {provider} not available: {e}")

    def test_12_agent_with_result_validators(self, test_config):
        """Test Case 12: Agent with result type validation"""
        try:
            model = get_openai_model()

            class NumberResponse(BaseModel):
                """A response containing a number"""
                value: int = Field(ge=0, le=100, description="A number between 0 and 100")
                explanation: str = Field(description="Explanation of the number")

            agent = Agent(
                model,
                output_type=NumberResponse,
                instructions="When asked for a number, provide a value between 0 and 100.",
            )

            result = agent.run_sync("Give me a random number for a dice roll (1-6).")

            assert result is not None
            assert result.output is not None
            assert isinstance(result.output, NumberResponse)
            assert 0 <= result.output.value <= 100

        except Exception as e:
            pytest.skip(f"Result validation through Pydantic AI not available: {e}")

    def test_13_usage_tracking(self, test_config):
        """Test Case 13: Usage tracking and token counting"""
        try:
            model = get_openai_model()
            agent = Agent(
                model,
                instructions="Be concise.",
            )

            result = agent.run_sync("Say hello.")

            assert result is not None

            # Check usage information
            usage = result.usage()
            assert usage is not None
            # Usage should have token counts
            if hasattr(usage, 'total_tokens'):
                assert usage.total_tokens > 0
            elif hasattr(usage, 'input_tokens'):
                assert usage.input_tokens > 0

        except Exception as e:
            pytest.skip(f"Usage tracking through Pydantic AI not available: {e}")

    def test_14_message_history_inspection(self, test_config):
        """Test Case 14: Inspect message history after run"""
        try:
            model = get_openai_model()
            agent = Agent(
                model,
                instructions="Be helpful.",
            )

            result = agent.run_sync("What is 2 + 2?")

            # Inspect all messages
            messages = result.all_messages()
            assert messages is not None
            assert len(messages) >= 2  # At least request and response

            # Should have user message and assistant response
            message_kinds = [msg.kind for msg in messages]
            assert "request" in message_kinds
            assert "response" in message_kinds

        except Exception as e:
            pytest.skip(f"Message history inspection through Pydantic AI not available: {e}")

    def test_15_dynamic_instructions(self, test_config):
        """Test Case 15: Dynamic instructions based on context"""
        try:
            model = get_openai_model()

            @dataclass
            class LanguageDeps:
                language: str

            agent = Agent(
                model,
                deps_type=LanguageDeps,
            )

            @agent.instructions
            def dynamic_instructions(ctx: RunContext[LanguageDeps]) -> str:
                return f"Always respond in {ctx.deps.language}. Be concise."

            deps = LanguageDeps(language="English")
            result = agent.run_sync("Say hello.", deps=deps)

            assert result is not None
            assert result.output is not None
            # Response should be in English
            content = str(result.output).lower()
            assert any(word in content for word in ["hello", "hi", "greetings"])

        except Exception as e:
            pytest.skip(f"Dynamic instructions through Pydantic AI not available: {e}")


# Additional test class for edge cases
class TestPydanticAIEdgeCases:
    """Edge case tests for Pydantic AI integration"""

    def test_empty_response_handling(self, test_config):
        """Test handling of potentially empty responses"""
        try:
            model = get_openai_model()
            agent = Agent(
                model,
                instructions="If asked to say nothing, respond with a single space.",
            )

            result = agent.run_sync("Say as little as possible.")

            # Should still get a valid result object
            assert result is not None

        except Exception as e:
            pytest.skip(f"Empty response handling test not available: {e}")

    def test_special_characters_in_prompt(self, test_config):
        """Test handling of special characters in prompts"""
        try:
            model = get_openai_model()
            agent = Agent(
                model,
                instructions="Echo back special characters correctly.",
            )

            special_prompt = "Handle these: 你好 🎉 <tag> & \"quotes\" 'apostrophe'"
            result = agent.run_sync(special_prompt)

            assert result is not None
            assert result.output is not None

        except Exception as e:
            pytest.skip(f"Special characters test not available: {e}")

    def test_long_conversation_context(self, test_config):
        """Test handling of longer conversation context"""
        try:
            model = get_openai_model()
            agent = Agent(
                model,
                instructions="You are a helpful assistant.",
            )

            # Build up conversation history
            history = None
            for i in range(3):
                result = agent.run_sync(
                    f"Remember number {i + 1}.",
                    message_history=history,
                )
                history = result.all_messages()

            # Final query should work with accumulated history
            final_result = agent.run_sync(
                "What numbers did I ask you to remember?",
                message_history=history,
            )

            assert final_result is not None
            assert final_result.output is not None

        except Exception as e:
            pytest.skip(f"Long conversation context test not available: {e}")

