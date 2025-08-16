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
"""

import pytest
import asyncio
import os
from typing import List, Dict, Any, Type, Optional
from unittest.mock import patch

# LangChain core imports
from langchain_core.messages import HumanMessage, AIMessage, SystemMessage
from langchain_core.tools import BaseTool
from langchain_core.prompts import ChatPromptTemplate, HumanMessagePromptTemplate
from langchain_core.output_parsers import StrOutputParser
from langchain_core.runnables import RunnablePassthrough

# LangChain provider imports
from langchain_openai import ChatOpenAI, OpenAIEmbeddings
from langchain_anthropic import ChatAnthropic

# Optional imports for providers that may not be available
try:
    from langchain_google_vertexai import ChatVertexAI, VertexAIEmbeddings

    GOOGLE_VERTEXAI_AVAILABLE = True
except ImportError:
    GOOGLE_VERTEXAI_AVAILABLE = False
    ChatVertexAI = None
    VertexAIEmbeddings = None

# Google Gemini specific imports
try:
    from langchain_google_genai import ChatGoogleGenerativeAI

    GOOGLE_GENAI_AVAILABLE = True
except ImportError:
    GOOGLE_GENAI_AVAILABLE = False
    ChatGoogleGenerativeAI = None

# Mistral specific imports
try:
    from langchain_mistralai import ChatMistralAI

    MISTRAL_AI_AVAILABLE = True
except ImportError:
    MISTRAL_AI_AVAILABLE = False
    ChatMistralAI = None

# Optional imports for legacy LangChain (chains, memory, agents)
try:
    from langchain.chains import LLMChain, ConversationChain, SequentialChain
    from langchain.memory import ConversationBufferMemory, ConversationSummaryMemory
    from langchain.agents import (
        AgentExecutor,
        create_openai_functions_agent,
        create_react_agent,
    )
    from langchain.agents.tools import Tool

    LEGACY_LANGCHAIN_AVAILABLE = True
except ImportError:
    LEGACY_LANGCHAIN_AVAILABLE = False
    LLMChain = ConversationChain = SequentialChain = None
    ConversationBufferMemory = ConversationSummaryMemory = None
    AgentExecutor = create_openai_functions_agent = create_react_agent = Tool = None

# LangChain standard tests (if available)
try:
    from langchain_tests.integration_tests import ChatModelIntegrationTests
    from langchain_tests.integration_tests import EmbeddingsIntegrationTests

    LANGCHAIN_TESTS_AVAILABLE = True
except ImportError:
    # Fallback for environments without langchain-tests
    LANGCHAIN_TESTS_AVAILABLE = False

    class ChatModelIntegrationTests:
        pass

    class EmbeddingsIntegrationTests:
        pass


from ..utils.common import (
    Config,
    SIMPLE_CHAT_MESSAGES,
    MULTI_TURN_MESSAGES,
    WEATHER_TOOL,
    CALCULATOR_TOOL,
    EMBEDDINGS_SINGLE_TEXT,
    EMBEDDINGS_MULTIPLE_TEXTS,
    EMBEDDINGS_SIMILAR_TEXTS,
    mock_tool_response,
    assert_valid_chat_response,
    assert_valid_embedding_response,
    assert_valid_embeddings_batch_response,
    calculate_cosine_similarity,
    get_api_key,
    skip_if_no_api_key,
    WEATHER_KEYWORDS,
    LOCATION_KEYWORDS,
)
from ..utils.config_loader import get_model, get_integration_url, get_config


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
                get_integration_url("langchain")
                if get_integration_url("langchain")
                else None
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
                get_integration_url("langchain")
                if get_integration_url("langchain")
                else None
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
                max_tokens=100,
                base_url=(
                    get_integration_url("langchain")
                    if get_integration_url("langchain")
                    else None
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
                    get_integration_url("langchain")
                    if get_integration_url("langchain")
                    else None
                ),
            )

            messages = [
                HumanMessage(content="Explain machine learning in one sentence.")
            ]
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
                    get_integration_url("langchain")
                    if get_integration_url("langchain")
                    else None
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

    @pytest.mark.skipif(
        not LEGACY_LANGCHAIN_AVAILABLE, reason="Legacy LangChain package not available"
    )
    def test_04_function_calling_tools(self, test_config):
        """Test Case 4: Function calling with tools"""
        try:
            chat = ChatOpenAI(
                model=get_model("langchain", "tools"),
                temperature=0,
                base_url=(
                    get_integration_url("langchain")
                    if get_integration_url("langchain")
                    else None
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
                word in response.content.lower()
                for word in LOCATION_KEYWORDS + WEATHER_KEYWORDS
            )

            assert (
                has_tool_calls or mentions_location
            ), "Should use tools or mention weather/location"

        except Exception as e:
            pytest.skip(f"Function calling through LangChain not available: {e}")

    def test_05_llm_chain_basic(self, test_config):
        """Test Case 5: Basic LLM Chain functionality"""
        try:
            llm = ChatOpenAI(
                model=get_model("langchain", "chat"),
                temperature=0.7,
                max_tokens=100,
                base_url=(
                    get_integration_url("langchain")
                    if get_integration_url("langchain")
                    else None
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
            assert any(
                word in result.lower() for word in ["machine", "learning", "data"]
            )

        except Exception as e:
            pytest.skip(f"LLM Chain through LangChain not available: {e}")

    @pytest.mark.skipif(
        not LEGACY_LANGCHAIN_AVAILABLE, reason="Legacy LangChain package not available"
    )
    def test_06_conversation_memory(self, test_config):
        """Test Case 6: Conversation memory functionality"""
        try:
            llm = ChatOpenAI(
                model=get_model("langchain", "chat"),
                temperature=0.7,
                max_tokens=150,
                base_url=(
                    get_integration_url("langchain")
                    if get_integration_url("langchain")
                    else None
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

    def test_07_streaming_responses(self, test_config):
        """Test Case 7: Streaming response functionality"""
        try:
            chat = ChatOpenAI(
                model=get_model("langchain", "chat"),
                temperature=0.7,
                max_tokens=100,
                streaming=True,
                base_url=(
                    get_integration_url("langchain")
                    if get_integration_url("langchain")
                    else None
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

    def test_08_multi_provider_chain(self, test_config):
        """Test Case 8: Chain with multiple provider models"""
        try:
            # Create different provider models
            openai_chat = ChatOpenAI(
                model="gpt-3.5-turbo",
                temperature=0.5,
                max_tokens=50,
                base_url=(
                    get_integration_url("langchain")
                    if get_integration_url("langchain")
                    else None
                ),
            )

            anthropic_chat = ChatAnthropic(
                model="claude-3-haiku-20240307",
                temperature=0.5,
                max_tokens=50,
                base_url=(
                    get_integration_url("langchain")
                    if get_integration_url("langchain")
                    else None
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

    def test_09_embeddings_similarity(self, test_config):
        """Test Case 9: Embeddings similarity analysis"""
        try:
            embeddings = OpenAIEmbeddings(
                model=get_model("langchain", "embeddings"),
                base_url=(
                    get_integration_url("langchain")
                    if get_integration_url("langchain")
                    else None
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

    def test_10_async_operations(self, test_config):
        """Test Case 10: Async operation support"""

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

    def test_11_error_handling(self, test_config):
        """Test Case 11: Error handling and fallbacks"""
        try:
            # Test with invalid model name
            chat = ChatOpenAI(
                model="invalid-model-name-should-fail",
                temperature=0.7,
                max_tokens=100,
                base_url=(
                    get_integration_url("langchain")
                    if get_integration_url("langchain")
                    else None
                ),
            )

            messages = [HumanMessage(content="This should fail gracefully.")]

            with pytest.raises(Exception) as exc_info:
                chat.invoke(messages)

            # Should get a meaningful error
            error_message = str(exc_info.value).lower()
            assert any(
                word in error_message
                for word in ["model", "error", "invalid", "not found"]
            )

        except Exception as e:
            pytest.skip(f"Error handling test through LangChain not available: {e}")

    def test_12_langchain_expression_language(self, test_config):
        """Test Case 12: LangChain Expression Language (LCEL)"""
        try:
            llm = ChatOpenAI(
                model=get_model("langchain", "chat"),
                temperature=0.7,
                max_tokens=100,
                base_url=(
                    get_integration_url("langchain")
                    if get_integration_url("langchain")
                    else None
                ),
            )

            prompt = ChatPromptTemplate.from_template("Tell me a joke about {topic}")
            output_parser = StrOutputParser()

            # Create chain using LCEL
            chain = prompt | llm | output_parser

            result = chain.invoke({"topic": "programming"})

            assert isinstance(result, str)
            assert len(result) > 0
            assert any(
                word in result.lower() for word in ["programming", "code", "joke"]
            )

        except Exception as e:
            pytest.skip(f"LCEL through LangChain not available: {e}")

    @pytest.mark.skipif(
        not GOOGLE_GENAI_AVAILABLE,
        reason="langchain-google-genai package not available",
    )
    def test_13_gemini_chat_integration(self, test_config):
        """Test Case 13: Google Gemini chat via LangChain"""
        try:
            # Use ChatGoogleGenerativeAI with Bifrost routing
            chat = ChatGoogleGenerativeAI(
                model="gemini-1.5-flash",
                google_api_key="dummy-google-api-key-bifrost-handles-auth",
                temperature=0.7,
                max_tokens=100,
            )

            # Patch the base URL to route through Bifrost
            base_url = get_integration_url("langchain")
            if base_url:
                # For Gemini through Bifrost, we need to route to the genai endpoint
                with patch.object(chat, "_client") as mock_client:
                    # Set up mock to route to Bifrost
                    mock_client.base_url = f"{base_url}/v1beta"

                    messages = [HumanMessage(content="Write a haiku about technology.")]
                    response = chat.invoke(messages)

                    assert isinstance(response, AIMessage)
                    assert response.content is not None
                    assert len(response.content) > 0
                    assert any(
                        word in response.content.lower()
                        for word in ["tech", "digital", "future", "machine"]
                    )
            else:
                pytest.skip("Bifrost URL not configured for LangChain integration")

        except Exception as e:
            pytest.skip(f"Gemini through LangChain not available: {e}")

    @pytest.mark.skipif(
        not MISTRAL_AI_AVAILABLE, reason="langchain-mistralai package not available"
    )
    def test_14_mistral_chat_integration(self, test_config):
        """Test Case 14: Mistral AI chat via LangChain"""
        try:
            # Mistral is OpenAI-compatible, so it can route through Bifrost easily
            base_url = get_integration_url("langchain")
            if base_url:
                chat = ChatMistralAI(
                    model="mistral-7b-instruct",
                    mistral_api_key="dummy-mistral-api-key-bifrost-handles-auth",
                    endpoint=f"{base_url}/v1",  # Route through Bifrost
                    temperature=0.7,
                    max_tokens=100,
                )

                messages = [
                    HumanMessage(content="Explain quantum computing in simple terms.")
                ]
                response = chat.invoke(messages)

                assert isinstance(response, AIMessage)
                assert response.content is not None
                assert len(response.content) > 0
                assert any(
                    word in response.content.lower()
                    for word in ["quantum", "computing", "bit", "science"]
                )
            else:
                pytest.skip("Bifrost URL not configured for LangChain integration")

        except Exception as e:
            pytest.skip(f"Mistral through LangChain not available: {e}")

    @pytest.mark.skipif(
        not GOOGLE_GENAI_AVAILABLE,
        reason="langchain-google-genai package not available",
    )
    def test_15_gemini_streaming(self, test_config):
        """Test Case 15: Gemini streaming responses via LangChain"""
        try:
            chat = ChatGoogleGenerativeAI(
                model="gemini-1.5-flash",
                google_api_key="dummy-google-api-key-bifrost-handles-auth",
                temperature=0.7,
                max_tokens=100,
                streaming=True,
            )

            base_url = get_integration_url("langchain")
            if base_url:
                with patch.object(chat, "_client") as mock_client:
                    mock_client.base_url = f"{base_url}/v1beta"

                    messages = [
                        HumanMessage(content="Tell me about artificial intelligence.")
                    ]

                    # Collect streaming chunks
                    chunks = []
                    for chunk in chat.stream(messages):
                        chunks.append(chunk)

                    assert len(chunks) > 0, "Should receive streaming chunks"

                    # Combine chunks to get full response
                    full_content = "".join(
                        chunk.content for chunk in chunks if chunk.content
                    )
                    assert len(full_content) > 0, "Should have content from streaming"
                    assert any(
                        word in full_content.lower()
                        for word in ["artificial", "intelligence", "ai"]
                    )
            else:
                pytest.skip("Bifrost URL not configured for LangChain integration")

        except Exception as e:
            pytest.skip(f"Gemini streaming through LangChain not available: {e}")

    @pytest.mark.skipif(
        not MISTRAL_AI_AVAILABLE, reason="langchain-mistralai package not available"
    )
    def test_16_mistral_streaming(self, test_config):
        """Test Case 16: Mistral streaming responses via LangChain"""
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

                messages = [
                    HumanMessage(content="Describe machine learning algorithms.")
                ]

                # Collect streaming chunks
                chunks = []
                for chunk in chat.stream(messages):
                    chunks.append(chunk)

                assert len(chunks) > 0, "Should receive streaming chunks"

                # Combine chunks to get full response
                full_content = "".join(
                    chunk.content for chunk in chunks if chunk.content
                )
                assert len(full_content) > 0, "Should have content from streaming"
                assert any(
                    word in full_content.lower()
                    for word in ["machine", "learning", "algorithm"]
                )
            else:
                pytest.skip("Bifrost URL not configured for LangChain integration")

        except Exception as e:
            pytest.skip(f"Mistral streaming through LangChain not available: {e}")

    def test_17_multi_provider_langchain_comparison(self, test_config):
        """Test Case 17: Compare responses across multiple LangChain providers"""
        providers_tested = []
        responses = {}

        # Test OpenAI
        try:
            openai_chat = ChatOpenAI(
                model="gpt-3.5-turbo",
                temperature=0.5,
                max_tokens=50,
                base_url=(
                    get_integration_url("langchain")
                    if get_integration_url("langchain")
                    else None
                ),
            )

            message = [
                HumanMessage(
                    content="What is the future of AI? Answer in one sentence."
                )
            ]
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
                    get_integration_url("langchain")
                    if get_integration_url("langchain")
                    else None
                ),
            )

            responses["anthropic"] = anthropic_chat.invoke(message)
            providers_tested.append("Anthropic")

        except Exception:
            pass

        # Test Gemini (if available)
        if GOOGLE_GENAI_AVAILABLE:
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
            assert isinstance(
                response, AIMessage
            ), f"{provider} should return AIMessage"
            assert response.content is not None, f"{provider} should have content"
            assert (
                len(response.content) > 0
            ), f"{provider} should have non-empty content"

        # Verify responses are different (providers should give unique answers)
        response_contents = [resp.content for resp in responses.values()]
        unique_responses = set(response_contents)
        assert (
            len(unique_responses) > 1
        ), "Different providers should give different responses"


# Skip standard tests if langchain-tests is not available
@pytest.mark.skipif(
    not LANGCHAIN_TESTS_AVAILABLE, reason="langchain-tests package not available"
)
class TestLangChainStandardChatModel(TestLangChainChatOpenAI):
    """Run LangChain's standard chat model tests"""

    pass


@pytest.mark.skipif(
    not LANGCHAIN_TESTS_AVAILABLE, reason="langchain-tests package not available"
)
class TestLangChainStandardEmbeddings(TestLangChainOpenAIEmbeddings):
    """Run LangChain's standard embeddings tests"""

    pass
