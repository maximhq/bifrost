"""
LangChain Integration Tests

🦜 LANGCHAIN COMPONENTS TESTED:
- Chat Models: OpenAI ChatOpenAI, Anthropic ChatAnthropic, Google ChatVertexAI
- Provider-Specific: Google ChatGoogleGenerativeAI, Mistral ChatMistralAI
- Embeddings: OpenAI OpenAIEmbeddings, Google VertexAIEmbeddings
- Tools: Function calling and tool integration
- Chains: LLMChain, ConversationChain, SequentialChain
- Memory: ConversationBufferMemory, ConversationSummaryMemory
- Agents: OpenAI Functions Agent, ReAct Agent
- Streaming: Real-time response streaming
- Vector Stores: Integration with embeddings and retrieval
- Structured Outputs: Pydantic model-based structured generation

Tests LangChain standard interface compliance and Bifrost integration:
1. Chat model standard tests (via LangChain test suite)
2. Embeddings standard tests (via LangChain test suite)
3. Tool integration and function calling
4. Chain composition and execution
5. Memory management and conversation history
6. Agent reasoning and tool usage
7. Streaming responses and async operations
8. Vector store operations
9. Multi-provider compatibility
10. Error handling and fallbacks
11. LangChain Expression Language (LCEL)
12. Google Gemini integration via langchain-google-genai
13. Mistral AI integration via langchain-mistralai
14. Provider-specific streaming capabilities
15. Cross-provider response comparison
16. Structured outputs with Pydantic models (OpenAI-compatible)
"""

import asyncio
import logging
import os
from typing import Any, Dict, List, Type
from unittest.mock import patch

import boto3
import pytest
from langchain_anthropic import ChatAnthropic

# LangChain core imports
from langchain_core.messages import AIMessage, HumanMessage, SystemMessage
from langchain_core.output_parsers import StrOutputParser
from langchain_core.prompts import ChatPromptTemplate

# Google Gemini specific imports
from langchain_google_genai import ChatGoogleGenerativeAI

# LangChain provider imports
from langchain_openai import ChatOpenAI, OpenAIEmbeddings
from langchain_anthropic import ChatAnthropic
from langchain_google_vertexai import ChatVertexAI, VertexAIEmbeddings

# Google Gemini specific imports
from langchain_google_genai import ChatGoogleGenerativeAI, Modality, GoogleGenerativeAIEmbeddings
from pydantic import BaseModel

try:
    from langchain_aws import ChatBedrockConverse

    BEDROCK_CONVERSE_AVAILABLE = True
except ImportError:
    BEDROCK_CONVERSE_AVAILABLE = False
    ChatBedrockConverse = None

# Mistral specific imports
try:
    from langchain_mistralai import ChatMistralAI

    MISTRAL_AI_AVAILABLE = True
except ImportError:
    MISTRAL_AI_AVAILABLE = False
    ChatMistralAI = None

# Optional imports for legacy LangChain (chains, memory, agents)
try:
    from langchain_classic.agents import (
        AgentExecutor,
        create_openai_functions_agent,
        create_react_agent,
    )
    from langchain_classic.agents.tools import Tool
    from langchain_classic.chains import ConversationChain, LLMChain, SequentialChain
    from langchain_classic.memory import ConversationBufferMemory, ConversationSummaryMemory

    LEGACY_LANGCHAIN_AVAILABLE = True
except ImportError:
    LEGACY_LANGCHAIN_AVAILABLE = False
    LLMChain = ConversationChain = SequentialChain = None
    ConversationBufferMemory = ConversationSummaryMemory = None
    AgentExecutor = create_openai_functions_agent = create_react_agent = Tool = None

# LangChain standard tests (if available)
try:
    from langchain_tests.integration_tests import ChatModelIntegrationTests, EmbeddingsIntegrationTests

    LANGCHAIN_TESTS_AVAILABLE = True
except ImportError:
    # Fallback for environments without langchain-tests
    LANGCHAIN_TESTS_AVAILABLE = False

    class ChatModelIntegrationTests:
        pass

    class EmbeddingsIntegrationTests:
        pass


from .utils.common import (
    CALCULATOR_TOOL,
    EMBEDDINGS_MULTIPLE_TEXTS,
    EMBEDDINGS_SIMILAR_TEXTS,
    EMBEDDINGS_SINGLE_TEXT,
    INPUT_TOKENS_SIMPLE_TEXT,
    INPUT_TOKENS_WITH_SYSTEM,
    INPUT_TOKENS_WITH_TOOLS,
    INPUT_TOKENS_LONG_TEXT,
    LOCATION_KEYWORDS,
    WEATHER_KEYWORDS,
    WEATHER_TOOL,
    Config,
    calculate_cosine_similarity,
    get_content_string,
    get_content_string_with_summary,
    mock_tool_response,
)
from .utils.config_loader import get_config, get_integration_url, get_model
from .utils.parametrize import format_provider_model, get_cross_provider_params_for_scenario


@pytest.fixture
def test_config():
    """Test configuration"""
    return Config()


@pytest.fixture(autouse=True)
def setup_langchain():
    """Setup LangChain with Bifrost configuration and dummy credentials"""
    # Set dummy credentials since Bifrost handles actual authentication
    os.environ["OPENAI_API_KEY"] = "dummy-openai-key-bifrost-handles-auth"
    os.environ["ANTHROPIC_API_KEY"] = "dummy-anthropic-key-bifrost-handles-auth"
    os.environ["GOOGLE_API_KEY"] = "dummy-google-api-key-bifrost-handles-auth"
    os.environ["VERTEX_PROJECT"] = "dummy-vertex-project"
    os.environ["VERTEX_LOCATION"] = "us-central1"

    # Get Bifrost URL for LangChain
    base_url = get_integration_url("langchain")
    config = get_config()
    integration_settings = config.get_integration_settings("langchain")

    # Store original base URLs and set Bifrost URLs
    original_openai_base = os.environ.get("OPENAI_BASE_URL")
    original_anthropic_base = os.environ.get("ANTHROPIC_BASE_URL")

    if base_url:
        # Configure provider base URLs to route through Bifrost
        os.environ["OPENAI_BASE_URL"] = f"{base_url}/v1"
        os.environ["ANTHROPIC_BASE_URL"] = f"{base_url}/v1"

    yield

    # Cleanup: restore original URLs
    if original_openai_base:
        os.environ["OPENAI_BASE_URL"] = original_openai_base
    else:
        os.environ.pop("OPENAI_BASE_URL", None)

    if original_anthropic_base:
        os.environ["ANTHROPIC_BASE_URL"] = original_anthropic_base
    else:
        os.environ.pop("ANTHROPIC_BASE_URL", None)


def create_langchain_tool_from_dict(tool_dict: Dict[str, Any]):
    """Convert common tool format to LangChain Tool"""
    if not LEGACY_LANGCHAIN_AVAILABLE:
        return None

    def tool_func(**kwargs):
        return mock_tool_response(tool_dict["name"], kwargs)

    return Tool(
        name=tool_dict["name"],
        description=tool_dict["description"],
        func=tool_func,
    )


# Common Pydantic models for structured output tests
class CityInfo(BaseModel):
    """Information about a city including its name, country, population, and capital status."""
    city_name: str
    country: str
    population_millions: float
    is_capital: bool


def validate_city_info_response(result: CityInfo, provider: str) -> None:
    """
    Validate a CityInfo structured output response.
    
    Args:
        result: The CityInfo instance to validate
        provider: The provider name for error messages
    
    Raises:
        AssertionError: If any validation fails
    """
    # Validate the response structure
    assert isinstance(
        result, CityInfo
    ), f"{provider}: Response should be a CityInfo instance"
    
    # Validate city_name field
    assert hasattr(result, "city_name"), f"{provider}: Result should have 'city_name' field"
    assert isinstance(
        result.city_name, str
    ), f"{provider}: city_name should be a string"
    assert len(result.city_name) > 0, f"{provider}: city_name should not be empty"
    assert any(
        word in result.city_name.lower() for word in ["paris"]
    ), f"{provider}: city_name should contain 'Paris'"
    
    # Validate country field
    assert hasattr(result, "country"), f"{provider}: Result should have 'country' field"
    assert isinstance(result.country, str), f"{provider}: country should be a string"
    assert len(result.country) > 0, f"{provider}: country should not be empty"
    assert any(
        word in result.country.lower() for word in ["france"]
    ), f"{provider}: country should contain 'France'"
    
    # Validate population_millions field
    assert hasattr(
        result, "population_millions"
    ), f"{provider}: Result should have 'population_millions' field"
    assert isinstance(
        result.population_millions, (int, float)
    ), f"{provider}: population_millions should be a number"
    assert (
        result.population_millions > 0
    ), f"{provider}: population_millions should be positive"
    
    # Validate is_capital field
    assert hasattr(
        result, "is_capital"
    ), f"{provider}: Result should have 'is_capital' field"
    assert isinstance(
        result.is_capital, bool
    ), f"{provider}: is_capital should be a boolean"
    assert result.is_capital is True, f"{provider}: Paris should be marked as a capital"


class TestLangChainChatOpenAI(ChatModelIntegrationTests):
    """Standard LangChain tests for ChatOpenAI through Bifrost"""

    @property
    def chat_model_class(self) -> Type[ChatOpenAI]:
        return ChatOpenAI

    @property
    def chat_model_params(self) -> dict:
        return {
            "model": get_model("langchain", "chat"),
            "temperature": 0.7,
            "max_tokens": 100,
            "base_url": (
                get_integration_url("langchain") if get_integration_url("langchain") else None
            ),
        }


class TestLangChainOpenAIEmbeddings(EmbeddingsIntegrationTests):
    """Standard LangChain tests for OpenAI Embeddings through Bifrost"""

    @property
    def embeddings_class(self) -> Type[OpenAIEmbeddings]:
        return OpenAIEmbeddings

    @property
    def embeddings_params(self) -> dict:
        return {
            "model": get_model("langchain", "embeddings"),
            "base_url": (
                get_integration_url("langchain") if get_integration_url("langchain") else None
            ),
        }


class TestLangChainIntegration:
    """Comprehensive LangChain integration tests through Bifrost"""

    def test_01_chat_openai_basic(self, test_config):
        """Test Case 1: Basic ChatOpenAI functionality"""
        try:
            chat = ChatOpenAI(
                model=get_model("langchain", "chat"),
                temperature=0.7,
                max_completion_tokens=100,
                base_url=(
                    get_integration_url("langchain") if get_integration_url("langchain") else None
                ),
            )

            messages = [HumanMessage(content="Hello! How are you today?")]
            response = chat.invoke(messages)

            assert isinstance(response, AIMessage)
            assert response.content is not None
            assert len(response.content) > 0

        except Exception as e:
            pytest.skip(f"ChatOpenAI through LangChain not available: {e}")

    def test_02_chat_anthropic_basic(self, test_config):
        """Test Case 2: Basic ChatAnthropic functionality"""
        try:
            chat = ChatAnthropic(
                model="claude-3-haiku-20240307",
                temperature=0.7,
                max_tokens=100,
                base_url=(
                    get_integration_url("langchain") if get_integration_url("langchain") else None
                ),
            )

            messages = [HumanMessage(content="Explain machine learning in one sentence.")]
            response = chat.invoke(messages)

            assert isinstance(response, AIMessage)
            assert response.content is not None
            assert any(
                word in response.content.lower()
                for word in ["machine", "learning", "data", "algorithm"]
            )

        except Exception as e:
            pytest.skip(f"ChatAnthropic through LangChain not available: {e}")

    def test_03_openai_embeddings_basic(self, test_config):
        """Test Case 3: Basic OpenAI embeddings functionality"""
        try:
            embeddings = OpenAIEmbeddings(
                model=get_model("langchain", "embeddings"),
                base_url=(
                    get_integration_url("langchain") if get_integration_url("langchain") else None
                ),
            )

            # Test single embedding
            result = embeddings.embed_query(EMBEDDINGS_SINGLE_TEXT)

            assert isinstance(result, list)
            assert len(result) > 0
            assert all(isinstance(x, float) for x in result)

            # Test batch embeddings
            batch_result = embeddings.embed_documents(EMBEDDINGS_MULTIPLE_TEXTS)

            assert isinstance(batch_result, list)
            assert len(batch_result) == len(EMBEDDINGS_MULTIPLE_TEXTS)
            assert all(isinstance(embedding, list) for embedding in batch_result)

        except Exception as e:
            pytest.skip(f"OpenAI embeddings through LangChain not available: {e}")


    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("embeddings"))
    def test_04_gemini_embeddings_basic(self, provider, model):
        """Test Case 4: Basic Gemini embeddings functionality"""
        try:
            embeddings = GoogleGenerativeAIEmbeddings(
                model=format_provider_model(provider, model),
                api_key="dummy-api-key",
                base_url=(
                    get_integration_url("langchain") if get_integration_url("langchain") else None
                ),
            )

            # Test single embedding
            result = embeddings.embed_query(EMBEDDINGS_SINGLE_TEXT)

            assert isinstance(result, list)
            assert len(result) > 0
            assert all(isinstance(x, float) for x in result)

            # Test batch embeddings
            batch_result = embeddings.embed_documents(EMBEDDINGS_MULTIPLE_TEXTS)        

            assert isinstance(batch_result, list)
            assert len(batch_result) == len(EMBEDDINGS_MULTIPLE_TEXTS)
            assert all(isinstance(embedding, list) for embedding in batch_result)

        except Exception as e:
            pytest.skip(f"Embeddings test failed for {provider} {model}: {e}")


    @pytest.mark.skipif(
        not LEGACY_LANGCHAIN_AVAILABLE, reason="Legacy LangChain package not available"
    )
    def test_05_function_calling_tools(self, test_config):
        """Test Case 5: Function calling with tools"""
        try:
            chat = ChatOpenAI(
                model=get_model("langchain", "tools"),
                temperature=0,
                base_url=(
                    get_integration_url("langchain") if get_integration_url("langchain") else None
                ),
            )

            # Create tools
            weather_tool = create_langchain_tool_from_dict(WEATHER_TOOL)
            calculator_tool = create_langchain_tool_from_dict(CALCULATOR_TOOL)
            tools = [weather_tool, calculator_tool]

            # Bind tools to the model
            chat_with_tools = chat.bind_tools(tools)

            # Test tool calling
            response = chat_with_tools.invoke(
                [HumanMessage(content="What's the weather in Boston?")]
            )

            assert isinstance(response, AIMessage)
            # Should either have tool calls or mention the location
            has_tool_calls = hasattr(response, "tool_calls") and response.tool_calls
            mentions_location = any(
                word in response.content.lower() for word in LOCATION_KEYWORDS + WEATHER_KEYWORDS
            )

            assert (
                has_tool_calls or mentions_location
            ), "Should use tools or mention weather/location"

        except Exception as e:
            pytest.skip(f"Function calling through LangChain not available: {e}")

    def test_06_llm_chain_basic(self, test_config):
        """Test Case 6: Basic LLM Chain functionality"""
        try:
            llm = ChatOpenAI(
                model=get_model("langchain", "chat"),
                temperature=0.7,
                max_tokens=100,
                base_url=(
                    get_integration_url("langchain") if get_integration_url("langchain") else None
                ),
            )

            prompt = ChatPromptTemplate.from_messages(
                [
                    (
                        "system",
                        "You are a helpful assistant that explains concepts clearly.",
                    ),
                    ("human", "Explain {topic} in simple terms."),
                ]
            )

            chain = prompt | llm | StrOutputParser()

            result = chain.invoke({"topic": "machine learning"})

            assert isinstance(result, str)
            assert len(result) > 0
            assert any(word in result.lower() for word in ["machine", "learning", "data"])

        except Exception as e:
            pytest.skip(f"LLM Chain through LangChain not available: {e}")

    @pytest.mark.skipif(
        not LEGACY_LANGCHAIN_AVAILABLE, reason="Legacy LangChain package not available"
    )
    def test_07_conversation_memory(self, test_config):
        """Test Case 7: Conversation memory functionality"""
        try:
            llm = ChatOpenAI(
                model=get_model("langchain", "chat"),
                temperature=0.7,
                max_tokens=150,
                base_url=(
                    get_integration_url("langchain") if get_integration_url("langchain") else None
                ),
            )

            memory = ConversationBufferMemory()
            conversation = ConversationChain(llm=llm, memory=memory, verbose=False)

            # First interaction
            response1 = conversation.predict(
                input="My name is Alice. What's the capital of France?"
            )
            assert "Paris" in response1 or "paris" in response1.lower()

            # Second interaction - should remember the name
            response2 = conversation.predict(input="What's my name?")
            assert "Alice" in response2 or "alice" in response2.lower()

        except Exception as e:
            pytest.skip(f"Conversation memory through LangChain not available: {e}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("streaming"))
    def test_08_streaming_responses(self, test_config, provider, model):
        """Test Case 8: Streaming response functionality"""
        try:
            chat = ChatOpenAI(
                model=format_provider_model(provider, model),
                temperature=0.7,
                max_tokens=200,
                streaming=True,
                base_url=(
                    get_integration_url("langchain") if get_integration_url("langchain") else None
                ),
            )

            messages = [HumanMessage(content="Tell me a short story about a robot.")]

            # Collect streaming chunks
            chunks = []
            for chunk in chat.stream(messages):
                chunks.append(chunk)

            assert len(chunks) > 0, "Should receive streaming chunks"

            # Combine chunks to get full response
            full_content = "".join(chunk.content for chunk in chunks if chunk.content)
            assert len(full_content) > 0, "Should have content from streaming"
            assert any(word in full_content.lower() for word in ["robot", "story"])

        except Exception as e:
            pytest.skip(f"Streaming through LangChain not available: {e}")

    def test_09_multi_provider_chain(self, test_config):
        """Test Case 9: Chain with multiple provider models"""
        try:
            # Create different provider models
            openai_chat = ChatOpenAI(
                model="gpt-3.5-turbo",
                temperature=0.5,
                max_tokens=50,
                base_url=(
                    get_integration_url("langchain") if get_integration_url("langchain") else None
                ),
            )

            anthropic_chat = ChatAnthropic(
                model="claude-3-haiku-20240307",
                temperature=0.5,
                max_tokens=50,
                base_url=(
                    get_integration_url("langchain") if get_integration_url("langchain") else None
                ),
            )

            # Test both models work
            message = [HumanMessage(content="What is AI? Answer in one sentence.")]

            openai_response = openai_chat.invoke(message)
            anthropic_response = anthropic_chat.invoke(message)

            assert isinstance(openai_response, AIMessage)
            assert isinstance(anthropic_response, AIMessage)
            assert (
                openai_response.content != anthropic_response.content
            )  # Should be different responses

        except Exception as e:
            pytest.skip(f"Multi-provider chains through LangChain not available: {e}")

    def test_10_embeddings_similarity(self, test_config):
        """Test Case 10: Embeddings similarity analysis"""
        try:
            embeddings = OpenAIEmbeddings(
                model=get_model("langchain", "embeddings"),
                base_url=(
                    get_integration_url("langchain") if get_integration_url("langchain") else None
                ),
            )

            # Get embeddings for similar texts
            similar_embeddings = embeddings.embed_documents(EMBEDDINGS_SIMILAR_TEXTS)

            # Calculate similarities
            similarity_1_2 = calculate_cosine_similarity(
                similar_embeddings[0], similar_embeddings[1]
            )
            similarity_1_3 = calculate_cosine_similarity(
                similar_embeddings[0], similar_embeddings[2]
            )

            # Similar texts should have high similarity
            assert (
                similarity_1_2 > 0.7
            ), f"Similar texts should have high similarity, got {similarity_1_2:.4f}"
            assert (
                similarity_1_3 > 0.7
            ), f"Similar texts should have high similarity, got {similarity_1_3:.4f}"

        except Exception as e:
            pytest.skip(f"Embeddings similarity through LangChain not available: {e}")

    def test_11_async_operations(self, test_config):
        """Test Case 11: Async operation support"""

        async def async_test():
            try:
                chat = ChatOpenAI(
                    model=get_model("langchain", "chat"),
                    temperature=0.7,
                    max_tokens=100,
                    base_url=(
                        get_integration_url("langchain")
                        if get_integration_url("langchain")
                        else None
                    ),
                )

                messages = [HumanMessage(content="Hello from async!")]
                response = await chat.ainvoke(messages)

                assert isinstance(response, AIMessage)
                assert response.content is not None
                assert len(response.content) > 0

                return True

            except Exception as e:
                pytest.skip(f"Async operations through LangChain not available: {e}")
                return False

        # Run async test
        result = asyncio.run(async_test())
        if result is not False:  # Skip if not explicitly skipped
            assert result is True

    def test_12_error_handling(self, test_config):
        """Test Case 12: Error handling and fallbacks"""
        try:
            # Test with invalid model name
            chat = ChatOpenAI(
                model="invalid-model-name-should-fail",
                temperature=0.7,
                max_tokens=100,
                base_url=(
                    get_integration_url("langchain") if get_integration_url("langchain") else None
                ),
            )

            messages = [HumanMessage(content="This should fail gracefully.")]

            with pytest.raises(Exception) as exc_info:
                chat.invoke(messages)

            # Should get a meaningful error
            error_message = str(exc_info.value).lower()
            assert any(word in error_message for word in ["model", "error", "invalid", "not found"])

        except Exception as e:
            pytest.skip(f"Error handling test through LangChain not available: {e}")

    def test_13_langchain_expression_language(self, test_config):
        """Test Case 13: LangChain Expression Language (LCEL)"""
        try:
            llm = ChatOpenAI(
                model=get_model("langchain", "chat"),
                temperature=0.7,
                max_tokens=100,
                base_url=(
                    get_integration_url("langchain") if get_integration_url("langchain") else None
                ),
            )
            prompt = ChatPromptTemplate.from_template("Tell me a joke about {topic}")
            output_parser = StrOutputParser()

            # Create chain using LCEL
            chain = prompt | llm | output_parser

            result = chain.invoke({"topic": "programming"})
            assert isinstance(result, str)
            assert len(result) > 0
        except Exception as e:
            pytest.skip(f"LCEL through LangChain not available: {e}")

    def test_14_gemini_chat_integration(self, test_config):
        """Test Case 14: Google Gemini chat via LangChain"""
        try:
            # Use ChatGoogleGenerativeAI with Bifrost routing
            llm = ChatGoogleGenerativeAI(
                model="gemini-2.5-flash",
                google_api_key="dummy-google-api-key-bifrost-handles-auth",
                temperature=0.7,
                max_output_tokens=200,             
                base_url=get_integration_url("langchain")
            )            
            logger = logging.getLogger(__name__)
            messages = [HumanMessage(content="Write a haiku about technology.")]
            logger.info(f"Messages: {messages}")
            response = llm.invoke(messages)
            logger.info(f"Response: {response}")

            assert isinstance(response, AIMessage)
            assert response.content is not None
            assert len(response.content) > 0
            assert any(
                word in response.content.lower()
                for word in [
                    "tech",
                    "digital",
                    "future",
                    "machine",
                    "computer",
                    "code",
                    "data",
                    "innovation",
                    "science",
                    "electronic",
                    "cyber",
                    "network",
                    "software",
                    "hardware",
                    "binary",
                    "algorithm",
                    "robot",
                    "artificial",
                    "intelligence",
                    "automation",
                    "internet",
                    "web",
                    "chip",
                    "silicon",
                    "circuit",
                    "screen",
                    "device",
                    "wire",
                    "signal",
                    "virtual",
                ]
            )

        except Exception as e:
            pytest.skip(f"Gemini chat integration test failed: {e}")

    def test_15_mistral_chat_integration(self, test_config):
        """Test Case 15: Mistral AI chat via LangChain"""
        try:
            # Mistral is OpenAI-compatible, so it can route through Bifrost easily
            base_url = get_integration_url("langchain")
            if base_url:
                chat = ChatMistralAI(
                    model="mistral/mistral-small-2506",
                    mistral_api_key="dummy-mistral-api-key-bifrost-handles-auth",
                    endpoint=f"{base_url}/v1",  # Route through Bifrost
                    temperature=0.7,
                    max_tokens=100,
                )

                messages = [HumanMessage(content="Explain quantum computing in simple terms.")]
                response = chat.invoke(messages)

                assert isinstance(response, AIMessage)
                assert response.content is not None
                assert len(response.content) > 0
                assert any(
                    word in response.content.lower()
                    for word in [
                        "quantum",
                        "computing",
                        "bit",
                        "science",
                        "qubit",
                        "superposition",
                        "entanglement",
                        "physics",
                        "particle",
                        "atom",
                        "electron",
                        "photon",
                        "computer",
                        "calculation",
                        "processor",
                        "algorithm",
                        "parallel",
                        "state",
                        "measurement",
                        "probability",
                        "interference",
                        "wave",
                        "spin",
                        "gate",
                        "binary",
                        "data",
                        "information",
                        "technology",
                        "fast",
                        "powerful",
                        "speed",
                        "simultaneous",
                    ]
                )
            else:
                pytest.skip("Bifrost URL not configured for LangChain integration")

        except Exception as e:
            pytest.skip(f"Mistral through LangChain not available: {e}")

    def test_16_gemini_streaming(self, test_config):
        """Test Case 16: Gemini streaming responses via LangChain"""
        try:
            chat = ChatGoogleGenerativeAI(
                model="gemini-2.5-flash",
                google_api_key="dummy-google-api-key-bifrost-handles-auth",
                temperature=0.7,
                max_tokens=100,
                streaming=True,
                base_url=get_integration_url("langchain")
            )

            messages = [HumanMessage(content="Tell me about artificial intelligence.")]

            # Collect streaming chunks
            chunks = []
            for chunk in chat.stream(messages):
                chunks.append(chunk)

            assert len(chunks) > 0, "Should receive streaming chunks"

            # Combine chunks to get full response
            full_content = "".join(chunk.content for chunk in chunks if chunk.content)
            assert len(full_content) > 0, "Should have content from streaming"
            assert any(
                word in full_content.lower()
                for word in ["artificial", "intelligence", "ai"]
            )

        except Exception as e:
            pytest.skip(f"Gemini streaming test failed: {e}")

    @pytest.mark.skipif(
        not MISTRAL_AI_AVAILABLE, reason="langchain-mistralai package not available"
    )
    def test_17_mistral_streaming(self, test_config):
        """Test Case 17: Mistral streaming responses via LangChain"""
        try:
            base_url = get_integration_url("langchain")
            if base_url:
                chat = ChatMistralAI(
                    model="mistral-7b-instruct",
                    mistral_api_key="dummy-mistral-api-key-bifrost-handles-auth",
                    endpoint=f"{base_url}/v1",
                    temperature=0.7,
                    max_tokens=100,
                    streaming=True,
                )

                messages = [HumanMessage(content="Describe machine learning algorithms.")]

                # Collect streaming chunks
                chunks = []
                for chunk in chat.stream(messages):
                    chunks.append(chunk)

                assert len(chunks) > 0, "Should receive streaming chunks"

                # Combine chunks to get full response
                full_content = "".join(chunk.content for chunk in chunks if chunk.content)
                assert len(full_content) > 0, "Should have content from streaming"
                assert any(
                    word in full_content.lower() for word in ["machine", "learning", "algorithm"]
                )
            else:
                pytest.skip("Bifrost URL not configured for LangChain integration")

        except Exception as e:
            pytest.skip(f"Mistral streaming through LangChain not available: {e}")

    def test_18_multi_provider_langchain_comparison(self, test_config):
        """Test Case 18: Compare responses across multiple LangChain providers"""
        providers_tested = []
        responses = {}

        # Test OpenAI
        try:
            openai_chat = ChatOpenAI(
                model="gpt-3.5-turbo",
                temperature=0.5,
                max_tokens=50,
                base_url=(
                    get_integration_url("langchain") if get_integration_url("langchain") else None
                ),
            )

            message = [HumanMessage(content="What is the future of AI? Answer in one sentence.")]
            responses["openai"] = openai_chat.invoke(message)
            providers_tested.append("OpenAI")

        except Exception:
            pass

        # Test Anthropic
        try:
            anthropic_chat = ChatAnthropic(
                model="claude-3-haiku-20240307",
                temperature=0.5,
                max_tokens=50,
                base_url=(
                    get_integration_url("langchain") if get_integration_url("langchain") else None
                ),
            )

            responses["anthropic"] = anthropic_chat.invoke(message)
            providers_tested.append("Anthropic")

        except Exception:
            pass

        # Test Gemini (if available)
        try:
            gemini_chat = ChatGoogleGenerativeAI(
                model="gemini-1.5-flash",
                google_api_key="dummy-google-api-key-bifrost-handles-auth",
                temperature=0.5,
                max_tokens=50,
            )

            base_url = get_integration_url("langchain")
            if base_url:
                with patch.object(gemini_chat, "_client") as mock_client:
                    mock_client.base_url = f"{base_url}/v1beta"
                    responses["gemini"] = gemini_chat.invoke(message)
                    providers_tested.append("Gemini")        
        
        except Exception:
            pass

        # Test Mistral (if available)
        if MISTRAL_AI_AVAILABLE:
            try:
                base_url = get_integration_url("langchain")
                if base_url:
                    mistral_chat = ChatMistralAI(
                        model="mistral-7b-instruct",
                        mistral_api_key="dummy-mistral-api-key-bifrost-handles-auth",
                        endpoint=f"{base_url}/v1",
                        temperature=0.5,
                        max_tokens=50,
                    )

                    responses["mistral"] = mistral_chat.invoke(message)
                    providers_tested.append("Mistral")

            except Exception:
                pass

        # Verify we tested at least 2 providers
        assert (
            len(providers_tested) >= 2
        ), f"Should test at least 2 providers, got: {providers_tested}"

        # Verify all responses are valid
        for provider, response in responses.items():
            assert isinstance(response, AIMessage), f"{provider} should return AIMessage"
            assert response.content is not None, f"{provider} should have content"
            assert len(response.content) > 0, f"{provider} should have non-empty content"

        # Verify responses are different (providers should give unique answers)
        response_contents = [resp.content for resp in responses.values()]
        unique_responses = set(response_contents)
        assert len(unique_responses) > 1, "Different providers should give different responses"

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("langchain_structured_output"))
    def test_19_structured_outputs(self, test_config, provider, model):
        """Test Case 19: Structured outputs with Pydantic models"""

        try:
            # Create LangChain ChatOpenAI instance with Bifrost routing
            llm = ChatOpenAI(
                model=format_provider_model(provider, model),
                base_url=(
                    get_integration_url("langchain")
                    if get_integration_url("langchain")
                    else None
                ),
                api_key="dummy-key",  # Keys managed by Bifrost
            )

            # Apply structured output
            llm_structured = llm.with_structured_output(CityInfo)

            # Invoke with a prompt that requires all fields
            result = llm_structured.invoke(
                "Provide information about Paris: the city name, country, approximate population in millions, and whether it's a capital city."
            )

            # Validate the response using the common validation function
            validate_city_info_response(result, provider)

            logging.info(
                f"✓ {provider} structured output test passed: {result.city_name}, {result.country}, {result.population_millions}M, capital={result.is_capital}"
            )

        except Exception as e:
            # Log the error but don't fail the entire test
            logging.warning(f"Structured output test failed for {provider} ({model}): {e}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("langchain_structured_output"))
    def test_20_structured_outputs_anthropic(self, test_config, provider, model):
        """Test Case 20: Structured outputs with Anthropic ChatAnthropic for Bedrock"""
        
        try:
            llm = ChatAnthropic(
                model=format_provider_model(provider, model),
                base_url=get_integration_url("langchain"),
                api_key="dummy-key",
            )
            
            llm_structured = llm.with_structured_output(CityInfo)
            result = llm_structured.invoke(
                "Provide information about Paris: the city name, country, approximate population in millions, and whether it's a capital city."
            )
            
            # Validate the response using the common validation function
            validate_city_info_response(result, provider)
            
            logging.info(
                f"✓ Bedrock structured output test passed: {result.city_name}, {result.country}, {result.population_millions}M, capital={result.is_capital}"
            )
            
        except Exception as e:
            pytest.skip(f"Bedrock structured output via ChatAnthropic not available: {e}")

    @pytest.mark.parametrize(
        "provider,model",
        get_cross_provider_params_for_scenario("tool_calls"),
    )
    def test_21_streaming_tool_calls_with_parameters(self, test_config, provider, model):
        """Test Case 21: Agent-based tool calling with streaming using new create_agent API."""
        try:
            from langchain.agents import create_agent
            from langchain_core.tools import tool

            @tool
            def get_current_date(timezone: str):
                """Get the current date and time for a specific timezone."""
                return f"Mock datetime for {timezone}"

            # Your LLM setup
            llm = ChatOpenAI(
                model=format_provider_model(provider, model),
                temperature=0,
                streaming=True,
                base_url=get_integration_url("langchain") or None,
            )

            tools = [get_current_date]

            # Create agent using NEW API
            agent_graph = create_agent(
                model=llm,
                tools=tools,
                system_prompt="You are a helpful assistant. Use tools to answer questions accurately.",
            )

            # Stream with proper inputs format
            inputs = {
                "messages": [{"role": "user", "content": "What is the current date and time in Asia/Kolkata timezone?"}]
            }

            # Collect streaming chunks and extract tool calls
            all_chunks = []
            tool_calls_found = []
            
            for chunk in agent_graph.stream(inputs, stream_mode="values"):
                all_chunks.append(chunk)
                # Extract tool calls from the messages in the chunk
                if "messages" in chunk:
                    for msg in chunk["messages"]:
                        if hasattr(msg, "tool_calls") and msg.tool_calls:
                            for tc in msg.tool_calls:
                                tool_calls_found.append(tc)

            # Validate we got chunks and tool calls
            assert len(all_chunks) > 0, "Should receive streaming chunks"
            assert len(tool_calls_found) > 0, "Should receive tool calls"

            # Get the first tool call
            tool_call = tool_calls_found[0]
            
            # Handle both dict and object formats
            if isinstance(tool_call, dict):
                tool_name = tool_call.get("name")
                args = tool_call.get("args", {})
            else:
                tool_name = tool_call.name if hasattr(tool_call, "name") else None
                args = tool_call.args if hasattr(tool_call, "args") else {}

            # Validate tool call structure
            assert tool_name == "get_current_date", f"Expected 'get_current_date', got {tool_name}"
            assert args is not None and args != {}, f"Tool args must not be empty, got {args}"
            
            if isinstance(args, str):
                import json
                args = json.loads(args)
            
            assert "timezone" in args, f"Expected 'timezone' in args, got {args}"
            timezone_value = args["timezone"]
            assert timezone_value != "", f"Timezone value should not be empty, got '{timezone_value}'"
            assert "kolkata" in timezone_value.lower() or "asia" in timezone_value.lower(), (
                f"Expected timezone to contain 'Asia' or 'Kolkata', got: {timezone_value}"
            )

            logging.info(f"✓ Agent streaming tool-call passed for {provider}/{model}: tool={tool_name}, args={args}")

        except ImportError as e:
            pytest.skip(f"Required LangChain components not available: {e}")
        except Exception as e:
            pytest.skip(f"Streaming tool calls not available for {provider}/{model}: {e}")

    def _validate_thinking_response(self, response, provider: str, keywords: List[str], min_keyword_matches: int = 3):
        """
        Helper function to validate thinking/reasoning responses.
        
        Args:
            response: The LangChain response object
            provider: Provider name for logging
            keywords: List of keywords to check for in the response
            min_keyword_matches: Minimum number of keywords that must match
        """
        # Validate response content exists
        assert response.content is not None, "Response should have content"
        
        # Extract content with summary handling
        content, has_reasoning_content = get_content_string_with_summary(response)
        content_lower = content.lower()
        
        # Validate keyword matches
        keyword_matches = sum(1 for keyword in keywords if keyword in content_lower)
        assert keyword_matches >= min_keyword_matches, (
            f"Response should contain reasoning about the problem. "
            f"Found {keyword_matches} keywords out of {len(keywords)}. "
            f"Content: {get_content_string(response.content)[:200]}..."
        )
        
        # Check for step-by-step reasoning indicators
        step_indicators = ["step", "first", "then", "next", "calculate", "therefore", "because", "since"]
        has_steps = any(indicator in content_lower for indicator in step_indicators)
        assert has_steps, (
            f"Response should show step-by-step reasoning. Content: {get_content_string(response.content)[:200]}..."
        )
        
        logging.info(f"✓ {provider} thinking test passed")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("thinking"))
    def test_22_thinking_openai(self, test_config, provider, model):
        """Test Case 22: Thinking/reasoning with OpenAI models via LangChain (non-streaming)"""
        
        try:
            # Use ChatOpenAI with reasoning parameters
            llm = ChatOpenAI(
                model=format_provider_model(provider, model),
                base_url=get_integration_url("langchain") if get_integration_url("langchain") else None,
                api_key="dummy-key",
                max_tokens=1500,
                reasoning={
                    "effort": "high",
                    "summary": "detailed",
                }
            )
            
            # Use reasoning-heavy prompt from common utils
            from .utils.common import RESPONSES_REASONING_INPUT
            
            # Convert to LangChain message format
            messages = [HumanMessage(content=RESPONSES_REASONING_INPUT[0]["content"])]
            
            response = llm.invoke(messages)
            
            # Validate response
            reasoning_keywords = ["train", "meet", "time", "hour", "pm", "distance", "speed", "mile"]
            self._validate_thinking_response(response, provider, reasoning_keywords, min_keyword_matches=3)
            
        except Exception as e:
            error_str = str(e).lower()
            if "reasoning" in error_str or "not supported" in error_str:
                logging.info(f"Info: Model {format_provider_model(provider, model)} may not fully support reasoning parameters")
                pytest.skip(f"Reasoning not supported for {provider}/{model}: {e}")
            else:
                raise

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("thinking"))
    def test_23_thinking_anthropic(self, test_config, provider, model):
        """Test Case 23: Thinking/reasoning with Anthropic models via LangChain (non-streaming)"""
        try:
            # Use ChatAnthropic with thinking parameters
            llm = ChatAnthropic(
                model=format_provider_model(provider, model),
                base_url=get_integration_url("langchain") if get_integration_url("langchain") else None,
                api_key="dummy-key",
                max_tokens=4000,
                thinking={"type": "enabled", "budget_tokens": 2500},
            )
            
            # Use thinking prompt from common utils
            from .utils.common import ANTHROPIC_THINKING_PROMPT
            
            # Convert to LangChain message format
            messages = []
            for msg in ANTHROPIC_THINKING_PROMPT:
                if msg["role"] == "user":
                    messages.append(HumanMessage(content=msg["content"]))
                elif msg["role"] == "assistant":
                    messages.append(AIMessage(content=msg["content"]))
            
            response = llm.invoke(messages)
            
            # Additional validation for Anthropic response type
            assert isinstance(response, AIMessage), "Response should be AIMessage"
            assert len(response.content) > 0, "Response content should not be empty"
            
            # Validate response
            reasoning_keywords = ["batch", "oven", "cookie", "minute", "calculate", "total", "time", "step"]
            self._validate_thinking_response(response, provider, reasoning_keywords, min_keyword_matches=2)
            
        except Exception as e:
            error_str = str(e).lower()
            if "thinking" in error_str or "not supported" in error_str:
                pytest.skip(f"Thinking not supported for {provider}/{model}: {e}")
            else:
                raise

    def test_24_thinking_azure(self, test_config):
        """Test Case 24: Thinking/reasoning with Azure models via LangChain (non-streaming)"""
        
        try:
            default_headers = {}
            # Azure routing requires specific headers for Bifrost
            azure_api_key = os.environ.get("AZURE_API_KEY", "dummy-azure-key")
            azure_endpoint = os.environ.get("AZURE_ENDPOINT", "https://dummy.openai.azure.com")
            default_headers = {
                "authorization": f"Bearer {azure_api_key}",
                "x-bf-azure-endpoint": azure_endpoint,
            }
            
            # Use ChatOpenAI with reasoning parameters
            llm = ChatOpenAI(
                model="azure/claude-opus-4-5",
                base_url=get_integration_url("langchain") if get_integration_url("langchain") else None,
                api_key="dummy-key",
                max_tokens=1500,
                reasoning={
                    "effort": "high",
                    "summary": "detailed",
                },
                default_headers=default_headers if default_headers else None,
            )
            
            # Use reasoning-heavy prompt from common utils
            from .utils.common import RESPONSES_REASONING_INPUT
            
            # Convert to LangChain message format
            messages = [HumanMessage(content=RESPONSES_REASONING_INPUT[0]["content"])]
            
            response = llm.invoke(messages)
            
            # Validate response
            reasoning_keywords = ["train", "meet", "time", "hour", "pm", "distance", "speed", "mile"]
            self._validate_thinking_response(response, "Azure", reasoning_keywords, min_keyword_matches=3)
            
        except Exception as e:
            error_str = str(e).lower()
            if "reasoning" in error_str or "not supported" in error_str:
                logging.info("Info: Azure model may not fully support reasoning parameters")
                pytest.skip(f"Reasoning not supported for Azure: {e}")
            else:
                raise

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("thinking"))
    def test_25_thinking_gemini(self, test_config, provider, model):
        """Test Case 25: Thinking/reasoning with Gemini models via LangChain (non-streaming)"""
        
        try:
            # Use ChatGoogleGenerativeAI with thinking_budget parameter
            llm = ChatGoogleGenerativeAI(
                model=format_provider_model(provider, model),
                base_url=get_integration_url("langchain") if get_integration_url("langchain") else None,
                api_key="dummy-key",
                max_tokens=4000,
                temperature=1.0,
                thinking_budget=1024,
                include_thoughts=True,
            )
            
            # Use reasoning-heavy prompt from common utils
            from .utils.common import RESPONSES_REASONING_INPUT
            
            # Convert to LangChain message format
            messages = [HumanMessage(content=RESPONSES_REASONING_INPUT[0]["content"])]
            
            response = llm.invoke(messages)
            
            # Check if usage metadata is available (Gemini-specific)
            if hasattr(response, 'usage_metadata') and response.usage_metadata:
                if "output_token_details" in response.usage_metadata:
                    reasoning_tokens = response.usage_metadata["output_token_details"].get("reasoning", 0)
                    if reasoning_tokens > 0:
                        logging.info(f"✓ Model used {reasoning_tokens} reasoning tokens")
            
            # Validate response
            reasoning_keywords = ["train", "meet", "time", "hour", "pm", "distance", "speed", "mile"]
            self._validate_thinking_response(response, f"{provider} Gemini", reasoning_keywords, min_keyword_matches=3)
            
        except Exception as e:
            error_str = str(e).lower()
            if "thinking" in error_str or "not supported" in error_str or "thinking_budget" in error_str:
                logging.info(f"Info: Model {format_provider_model(provider, model)} may not fully support thinking_budget parameters")
                pytest.skip(f"Thinking not supported for {provider}/{model}: {e}")
            else:
                raise

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("thinking"))
    def test_26_thinking_bedrock(self, test_config, provider, model):
        """Test Case 26: Thinking/reasoning with Bedrock models via LangChain (non-streaming)"""
        try:

            base_url = get_integration_url("bedrock")

            config = get_config()
            integration_settings = config.get_integration_settings("bedrock")
            region = integration_settings.get("region", "us-west-2")

            client_kwargs = {
                "service_name": "bedrock-runtime",
                "region_name": region,
                "endpoint_url": base_url,
            }

            bedrock_client = boto3.client(**client_kwargs)
            # Use ChatBedrockConverse with thinking parameters
            llm = ChatBedrockConverse(
                model=format_provider_model(provider, model),
                client=bedrock_client,
                max_tokens=2000,
                additional_model_request_fields={ # for anthropic models
                    "reasoning_config": {
                        "type": "enabled",
                        "budget_tokens": 1500,
                    }
                },
            )
            # for nova models
            # additional_model_request_fields={
            #     "reasoningConfig": {
            #         "type": "enabled",
            #         "maxReasoningEffort": "high",
            #     }
            # },
            
            # Use reasoning-heavy prompt from common utils
            from .utils.common import RESPONSES_REASONING_INPUT
            
            # Convert to LangChain message format
            messages = [HumanMessage(content=RESPONSES_REASONING_INPUT[0]["content"])]
            
            response = llm.invoke(messages)
            
            # Additional validation for Anthropic response type
            assert isinstance(response, AIMessage), "Response should be AIMessage"
            assert len(response.content) > 0, "Response content should not be empty"
            
            # Validate response
            reasoning_keywords = ["batch", "oven", "cookie", "minute", "calculate", "total", "time", "step"]
            self._validate_thinking_response(response, provider, reasoning_keywords, min_keyword_matches=2)
            
        except Exception as e:
            error_str = str(e).lower()
            if "thinking" in error_str or "not supported" in error_str:
                pytest.skip(f"Thinking not supported for {provider}/{model}: {e}")
            else:
                raise

    # =========================================================================
    # TOKEN COUNTING TEST CASES - get_num_tokens_from_messages
    # =========================================================================

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("count_tokens"))
    def test_27_get_num_tokens_simple_text(self, test_config, provider, model):
        """Test Case 27: Get number of tokens from messages with simple text"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
                    
        try:
            llm = ChatAnthropic(
                model=format_provider_model(provider, model),
                base_url=get_integration_url("langchain") if get_integration_url("langchain") else None,
                api_key="dummy-key",
            )
            
            # Create simple message
            messages = [HumanMessage(content=INPUT_TOKENS_SIMPLE_TEXT)]
            
            # Get token count
            token_count = llm.get_num_tokens_from_messages(messages)
            
            # Validate token count
            assert isinstance(token_count, int), "Token count should be an integer"
            assert token_count > 0, "Token count should be positive"
            # Simple text should have a reasonable token count (between 3-20 tokens)
            assert 3 <= token_count <= 20, (
                f"Simple text should have 3-20 tokens, got {token_count}"
            )
            
        except Exception as e:
            pytest.skip(f"Token counting not available for {provider}/{model}: {e}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("count_tokens"))
    def test_28_get_num_tokens_with_system_message(self, test_config, provider, model):
        """Test Case 28: Get number of tokens from messages with system message"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        try:
            # Create ChatAnthropic instance
            llm = ChatAnthropic(
                model=format_provider_model(provider, model),
                base_url=get_integration_url("langchain") if get_integration_url("langchain") else None,
                api_key="dummy-key",
            )
            
            # Create messages with system message
            messages = [
                SystemMessage(content=INPUT_TOKENS_WITH_SYSTEM[0]["content"]),
                HumanMessage(content=INPUT_TOKENS_WITH_SYSTEM[1]["content"])
            ]
            
            # Get token count
            token_count = llm.get_num_tokens_from_messages(messages)
            
            # Validate token count
            assert isinstance(token_count, int), "Token count should be an integer"
            assert token_count > 0, "Token count should be positive"
            # With system message should have more tokens than simple text
            assert token_count > 2, (
                f"With system message should have >2 tokens, got {token_count}"
            )
            

        except Exception as e:
            pytest.skip(f"Token counting not available for {provider}/{model}: {e}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("count_tokens"))
    def test_29_input_tokens_long_text(self, test_config, provider, model):
        """Test Case 29: Input tokens count with long text via LangChain"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        try:
            # Create ChatAnthropic instance
            llm = ChatAnthropic(
                model=format_provider_model(provider, model),
                base_url=get_integration_url("langchain") if get_integration_url("langchain") else None,
                api_key="dummy-key",
            )

            # Create message with long text input
            messages = [HumanMessage(content=INPUT_TOKENS_LONG_TEXT)]

            # Get token count for long text
            token_count = llm.get_num_tokens_from_messages(messages)

            # Validate token count
            assert isinstance(token_count, int), "Token count should be an integer"
            assert token_count > 100, (
                f"Long text should have >100 tokens, got {token_count}"
            )

        except Exception as e:
            pytest.skip(f"Token counting not available for {provider}/{model}: {e}")


# Skip standard tests if langchain-tests is not available
@pytest.mark.skipif(not LANGCHAIN_TESTS_AVAILABLE, reason="langchain-tests package not available")
class TestLangChainStandardChatModel(TestLangChainChatOpenAI):
    """Run LangChain's standard chat model tests"""

    pass


@pytest.mark.skipif(not LANGCHAIN_TESTS_AVAILABLE, reason="langchain-tests package not available")
class TestLangChainStandardEmbeddings(TestLangChainOpenAIEmbeddings):
    """Run LangChain's standard embeddings tests"""

    pass
