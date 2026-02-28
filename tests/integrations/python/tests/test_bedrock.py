"""
Bedrock Integration Tests - Cross-Provider Support

🌉 CROSS-PROVIDER TESTING:
This test suite uses the AWS SDK (boto3) to test against multiple AI providers through Bifrost.
Tests automatically run against all available providers with proper capability filtering.
All requests include the x-model-provider header to route to the appropriate provider.

Note: Tests automatically skip for providers that don't support specific capabilities.

Tests core scenarios using AWS SDK (boto3) directly against Bifrost:
1. Text completion (invoke) - Bedrock-specific
2. Chat with tool calling and tool result (converse) - Cross-provider
3. Image analysis (converse) - Cross-provider
4. Streaming chat (converse-stream) - Cross-provider
5. Streaming text completion (invoke-with-response-stream) - Bedrock-specific
6. Simple chat (converse) - Cross-provider
7. Multi-turn conversation (converse) - Cross-provider
8. Multiple tool calls (converse) - Cross-provider
9. System message handling (converse) - Bedrock-specific
10. End-to-end tool calling (converse) - Cross-provider

Files API Tests (Multi-Provider via boto3 S3 with x-model-provider header):
11. File upload - Cross-provider
12. File list - Cross-provider
13. File retrieve - Cross-provider
14. File delete - Cross-provider
15. File content download - Cross-provider

Batch API Tests (Multi-Provider via boto3 Bedrock with x-model-provider header):
16. Batch create with file - Cross-provider
17. Batch list - Cross-provider
18. Batch retrieve - Cross-provider
19. Batch cancel - Cross-provider
20. Batch end-to-end - Cross-provider

Prompt Caching Tests:
21. Prompt caching with system message checkpoint
22. Prompt caching with messages checkpoint
23. Prompt caching with tools checkpoint

Count Tokens Tests:
24. Count tokens from simple messages - Cross-provider
25. Count tokens with system message - Cross-provider
26. Count tokens with tool definitions - Cross-provider
27. Count tokens from long text - Cross-provider
28. Count tokens from multi-turn conversation - Cross-provider
"""

import base64
import json
import time
import urllib.request
from typing import Any, Dict, List

import boto3
import pytest

from .utils.common import (
    BASE64_IMAGE,
    CALCULATOR_TOOL,
    LOCATION_KEYWORDS,
    MULTI_TURN_MESSAGES,
    MULTIPLE_TOOL_CALL_MESSAGES,
    PROMPT_CACHING_LARGE_CONTEXT,
    PROMPT_CACHING_TOOLS,
    SIMPLE_CHAT_MESSAGES,
    WEATHER_KEYWORDS,
    WEATHER_TOOL,
    Config,
    assert_has_tool_calls,
    assert_valid_chat_response,
    extract_tool_calls,
    mock_tool_response,
    skip_if_no_api_key,
)
from .utils.config_loader import get_config, get_integration_url, get_model
from .utils.parametrize import (
    format_provider_model,
    get_cross_provider_params_for_scenario,
)


def create_provider_header_handler(provider: str):
    """Create a header handler function for a specific provider"""

    def add_provider_header(request, **kwargs):
        request.headers["x-model-provider"] = provider

    return add_provider_header


def get_provider_s3_client(provider: str) -> boto3.client:
    """Create S3 client with x-model-provider header for given provider"""
    base_url = get_integration_url("bedrock")
    config = get_config()
    integration_settings = config.get_integration_settings("bedrock")
    region = integration_settings.get("region", "us-west-2")

    client = boto3.client(
        "s3",
        region_name=region,
        endpoint_url=f"{base_url}/files",
    )

    # Add provider-specific header to all requests
    client.meta.events.register("before-send", create_provider_header_handler(provider))

    return client


def get_provider_bedrock_batch_client(provider: str) -> boto3.client:
    """Create Bedrock batch client with x-model-provider header for given provider"""
    base_url = get_integration_url("bedrock")
    config = get_config()
    integration_settings = config.get_integration_settings("bedrock")
    region = integration_settings.get("region", "us-west-2")

    client = boto3.client(
        "bedrock",
        region_name=region,
        endpoint_url=base_url,
    )

    # Add provider-specific header to all requests
    client.meta.events.register("before-send", create_provider_header_handler(provider))

    return client


def create_bedrock_batch_jsonl(model_id: str, num_requests: int = 2) -> str:
    """Create JSONL content for Bedrock batch processing"""
    lines = []
    for i in range(num_requests):
        record = {
            "recordId": f"request-{i+1}",
            "modelInput": {
                "messages": [
                    {
                        "role": "user",
                        "content": [
                            {"text": f"Hello, this is test message {i+1}. Say hi back briefly."}
                        ],
                    }
                ],
                "inferenceConfig": {"maxTokens": 100},
            },
        }
        lines.append(json.dumps(record))
    return "\n".join(lines)


def create_gemini_batch_jsonl(model_id: str, num_requests: int = 2) -> str:
    """Create JSONL content for Gemini batch processing

    Gemini batch format:
    {"request": {"contents": [{"role": "user", "parts": [{"text": "..."}]}]}, "metadata": {"key": "custom-id"}}
    """
    lines = []
    for i in range(num_requests):
        record = {
            "request": {
                "contents": [
                    {
                        "role": "user",
                        "parts": [
                            {"text": f"Hello, this is test message {i+1}. Say hi back briefly."}
                        ],
                    }
                ],
                "generationConfig": {"maxOutputTokens": 100},
            },
            "metadata": {"key": f"request-{i+1}"},
        }
        lines.append(json.dumps(record))
    return "\n".join(lines)


def create_batch_jsonl_for_provider(provider: str, model_id: str, num_requests: int = 2) -> str:
    """Create provider-specific JSONL content for batch processing"""
    if provider == "gemini":
        return create_gemini_batch_jsonl(model_id, num_requests)
    else:
        return create_bedrock_batch_jsonl(model_id, num_requests)


@pytest.fixture
def bedrock_client():
    """Create Bedrock Runtime client for testing (converse, invoke)"""
    base_url = get_integration_url("bedrock")

    config = get_config()
    integration_settings = config.get_integration_settings("bedrock")
    region = integration_settings.get("region", "us-west-2")

    client_kwargs = {
        "service_name": "bedrock-runtime",
        "region_name": region,
        "endpoint_url": base_url,
    }

    return boto3.client(**client_kwargs)


@pytest.fixture
def bedrock_batch_client():
    """Create Bedrock client for batch operations (model invocation jobs)"""
    base_url = get_integration_url("bedrock")

    config = get_config()
    integration_settings = config.get_integration_settings("bedrock")
    region = integration_settings.get("region", "us-west-2")

    # Use bedrock service (not bedrock-runtime) for batch operations
    client_kwargs = {
        "service_name": "bedrock",
        "region_name": region,
        "endpoint_url": base_url,
    }

    return boto3.client(**client_kwargs)


def add_provider_header(request, **kwargs):
    """Add x-model-provider header to boto3 requests"""
    request.headers["x-model-provider"] = "bedrock"


@pytest.fixture
def s3_client():
    """Create S3 client for file operations via Bifrost's S3-compatible API"""
    base_url = get_integration_url("bedrock")
    config = get_config()
    integration_settings = config.get_integration_settings("bedrock")
    region = integration_settings.get("region", "us-west-2")

    # Point boto3 S3 client to Bifrost's S3-compatible endpoint
    client = boto3.client(
        "s3",
        region_name=region,
        endpoint_url=f"{base_url}/files",
    )

    # Add provider header to all requests
    client.meta.events.register("before-send", add_provider_header)

    return client


@pytest.fixture
def test_config():
    """Test configuration"""
    return Config()


def convert_to_bedrock_messages(messages: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
    """Convert common message format to Bedrock Converse format"""
    bedrock_messages = []

    for msg in messages:
        if msg["role"] == "system":
            # System messages are handled separately in Converse API
            continue

        content = []
        if isinstance(msg.get("content"), list):
            for item in msg["content"]:
                if item["type"] == "text":
                    content.append({"text": item["text"]})
                elif item["type"] == "image_url":
                    url = item["image_url"]["url"]
                    if url.startswith("data:image"):
                        # Base64 image
                        header, data = url.split(",", 1)
                        media_type = header.split(";")[0].split(":")[1]
                        image_bytes = base64.b64decode(data)
                        content.append(
                            {
                                "image": {
                                    "format": media_type.split("/")[1],  # png, jpeg
                                    "source": {"bytes": image_bytes},
                                }
                            }
                        )
                    else:
                        # URL image - download and convert to bytes
                        with urllib.request.urlopen(url, timeout=20) as response:
                            image_bytes = response.read()
                        # Simple format detection
                        fmt = "jpeg"
                        if url.lower().endswith(".png"):
                            fmt = "png"
                        elif url.lower().endswith(".gif"):
                            fmt = "gif"
                        elif url.lower().endswith(".webp"):
                            fmt = "webp"

                        content.append({"image": {"format": fmt, "source": {"bytes": image_bytes}}})
        else:
            content.append({"text": msg["content"]})

        role = "user" if msg["role"] == "user" else "assistant"
        bedrock_messages.append({"role": role, "content": content})

    return bedrock_messages


def convert_to_bedrock_tools(tools: List[Dict[str, Any]]) -> Dict[str, Any]:
    """Convert common tool format to Bedrock ToolConfig"""
    bedrock_tools = []

    for tool in tools:
        bedrock_tools.append(
            {
                "toolSpec": {
                    "name": tool["name"],
                    "description": tool["description"],
                    "inputSchema": {"json": tool["parameters"]},
                }
            }
        )

    return {"tools": bedrock_tools}


def extract_system_messages(messages: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
    """Extract system messages from message list for Bedrock Converse API"""
    system_messages = []
    for msg in messages:
        if msg["role"] == "system":
            system_messages.append({"text": msg["content"]})
    return system_messages


class TestBedrockIntegration:
    """Test suite for Bedrock integration covering core scenarios"""

    @skip_if_no_api_key("bedrock")
    def test_01_text_completion_invoke(self, bedrock_client, test_config):
        pytest.skip("Skipping text completion invoke test")
        model_id = get_model("bedrock", "text_completion")

        request_body = {
            "prompt": "Hello! How are you today?",
            "max_tokens": 100,
            "temperature": 0.7,
        }

        response = bedrock_client.invoke_model(
            modelId=model_id,
            contentType="application/json",
            accept="application/json",
            body=json.dumps(request_body),
        )

        response_body = json.loads(response["body"].read())

        assert response_body is not None
        assert (
            "outputs" in response_body or "completion" in response_body or "text" in response_body
        )

        text = None
        if "outputs" in response_body:
            if isinstance(response_body["outputs"], list) and len(response_body["outputs"]) > 0:
                text = response_body["outputs"][0].get("text", "")
        elif "completion" in response_body:
            text = response_body["completion"]
        elif "text" in response_body:
            text = response_body["text"]

        assert text is not None and len(text) > 0, "Response should contain text"

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("tool_calls"))
    def test_02_converse_with_tool_calling(self, bedrock_client, test_config, provider, model):
        """Test Case 2: Chat with tool calling and tool result using converse API - runs across all available providers"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        messages = convert_to_bedrock_messages(
            [{"role": "user", "content": "What's the weather in Boston?"}]
        )
        tool_config = convert_to_bedrock_tools([WEATHER_TOOL])
        # Add toolChoice to force the model to use a tool
        tool_config["toolChoice"] = {"any": {}}
        model_id = format_provider_model(provider, model)

        # 1. Initial Request - should trigger tool call
        response = bedrock_client.converse(
            modelId=model_id,
            messages=messages,
            toolConfig=tool_config,
            inferenceConfig={"maxTokens": 500},
        )

        assert_has_tool_calls(response, expected_count=1)

        # 2. Append Assistant Response
        assistant_message = response["output"]["message"]
        messages.append(assistant_message)

        # 3. Handle Tool Execution
        content = assistant_message["content"]
        tool_uses = [c["toolUse"] for c in content if "toolUse" in c]

        tool_result_content = []
        for tool_use in tool_uses:
            tool_id = tool_use["toolUseId"]
            tool_name = tool_use["name"]
            tool_input = tool_use["input"]

            # Mock execution
            tool_response_text = mock_tool_response(tool_name, tool_input)

            tool_result_content.append(
                {
                    "toolResult": {
                        "toolUseId": tool_id,
                        "content": [{"text": tool_response_text}],
                        "status": "success",
                    }
                }
            )
        messages.append({"role": "user", "content": tool_result_content})

        # 4. Final Request with Tool Results
        final_response = bedrock_client.converse(
            modelId=model_id,
            messages=messages,
            toolConfig=tool_config,
            inferenceConfig={"maxTokens": 500},
        )

        # Validate response structure
        assert_valid_chat_response(final_response)
        assert "output" in final_response
        assert "message" in final_response["output"], "Response should have message in output"

        # Check if response has content
        output_message = final_response["output"]["message"]
        assert "content" in output_message, "Response message should have content"
        assert (
            len(output_message["content"]) > 0
        ), "Response message should have at least one content item"

        # Extract text content if available
        text_content = None
        for item in output_message["content"]:
            if "text" in item:
                text_content = item["text"]
                break

        assert text_content is not None, "Final response should contain a text content block"
        final_text = text_content.lower()
        assert any(word in final_text for word in WEATHER_KEYWORDS + LOCATION_KEYWORDS)

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("image_base64")
    )
    def test_03_image_analysis(self, bedrock_client, test_config, provider, model):
        """Test Case 3: Image analysis using converse API - runs across all available providers with base64 image support"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        # Use base64 image instead of URL to avoid 403 errors
        messages = convert_to_bedrock_messages(
            [
                {
                    "role": "user",
                    "content": [
                        {
                            "type": "text",
                            "text": "What do you see in this image? Describe what you see.",
                        },
                        {
                            "type": "image_url",
                            "image_url": {"url": f"data:image/png;base64,{BASE64_IMAGE}"},
                        },
                    ],
                }
            ]
        )

        model_id = format_provider_model(provider, model)
        response = bedrock_client.converse(
            modelId=model_id, messages=messages, inferenceConfig={"maxTokens": 500}
        )

        # First validate basic response structure
        assert_valid_chat_response(response)

        # Extract content for validation
        output = response["output"]
        assert "message" in output, "Response should have message"
        assert "content" in output["message"], "Response message should have content"

        content_items = output["message"]["content"]
        assert len(content_items) > 0, "Response should have at least one content item"

        # Find text content
        text_content = None
        for item in content_items:
            if "text" in item:
                text_content = item["text"]
                break

        assert (
            text_content is not None and len(text_content) > 0
        ), "Response should contain text content"

        # Check for image-related keywords (more lenient for small test image)
        text_lower = text_content.lower()
        image_keywords = [
            "image",
            "picture",
            "photo",
            "see",
            "visual",
            "show",
            "appear",
            "color",
            "scene",
            "pixel",
            "red",
            "square",
        ]
        has_image_reference = any(keyword in text_lower for keyword in image_keywords)

        # For a 1x1 pixel image, the response might be minimal, so we're more lenient
        # Just check that we got a response that acknowledges the image
        assert has_image_reference or len(text_content) > 5, (
            f"Response should reference the image or provide some description. "
            f"Got: {text_content[:100]}"
        )

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("streaming"))
    def test_04_converse_streaming(self, bedrock_client, test_config, provider, model):
        """Test Case 4: Streaming chat completion using converse-stream API with boto3 - runs across all available providers

        Follows boto3 Bedrock Runtime converse_stream API:
        https://boto3.amazonaws.com/v1/documentation/api/1.35.6/reference/services/bedrock-runtime/client/converse_stream.html
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        messages = convert_to_bedrock_messages(
            [{"role": "user", "content": "Say hello in exactly 3 words."}]
        )
        model_id = format_provider_model(provider, model)

        try:
            response_stream = bedrock_client.converse_stream(
                modelId=model_id, messages=messages, inferenceConfig={"maxTokens": 100}
            )
        except AttributeError:
            pytest.skip(
                "converse_stream method not available in this boto3 version. Please upgrade boto3."
            )
        except Exception as e:
            pytest.fail(f"converse_stream failed: {e}")

        # Collect streaming chunks
        chunks = []
        text_parts = []

        # Process the event stream from boto3
        start_time = time.time()
        timeout = 30  # 30 second timeout
        stream_completed = False

        try:
            # Use the simplified access pattern via ["stream"] which boto3 provides
            stream = response_stream.get("stream")
            if stream is None:
                # Fallback if "stream" key is missing (shouldn't happen with recent boto3)
                stream = response_stream.get("eventStream")

            if stream is None:
                pytest.fail(
                    f"Response missing 'stream' or 'eventStream'. Keys: {list(response_stream.keys())}"
                )

            for event in stream:
                # Check timeout
                if time.time() - start_time > timeout:
                    pytest.fail(
                        f"Streaming took longer than {timeout} seconds. Received {len(chunks)} chunks so far."
                    )

                chunks.append(event)

                # Extract text from contentBlockDelta events
                if "contentBlockDelta" in event:
                    delta = event["contentBlockDelta"].get("delta", {})
                    if "text" in delta and delta["text"]:
                        text_parts.append(delta["text"])

                # Check for messageStop event (stream completion)
                elif "messageStop" in event:
                    # Message stop - stream is complete
                    stream_completed = True

                # Handle messageStart event (contains role)
                elif "messageStart" in event:
                    # Message start - stream beginning
                    pass

        except Exception as e:
            pytest.fail(
                f"Error iterating event stream: {e}. Response type: {type(response_stream)}, Chunks received: {len(chunks)}"
            )

        # Verify we received streaming chunks
        assert (
            len(chunks) > 0
        ), f"Should receive at least one streaming chunk. Stream completed: {stream_completed}, Total chunks: {len(chunks)}"

        # Verify we received text content
        combined_text = "".join(text_parts)
        if len(combined_text) == 0:
            chunk_debug = []
            for i, chunk in enumerate(chunks[:5]):  # First 5 chunks for debugging
                chunk_debug.append(f"Chunk {i}: {str(chunk)[:200]}")
            pytest.fail(
                f"Streaming response should contain text content. Received {len(chunks)} chunks. Stream completed: {stream_completed}. First chunks: {chunk_debug}"
            )

        # Verify we got a reasonable response
        assert (
            len(combined_text.strip()) > 0
        ), f"Streaming response should not be empty. Combined text: {repr(combined_text[:100])}"

    @skip_if_no_api_key("bedrock")
    def test_05_invoke_streaming(self, bedrock_client, test_config):
        """Test Case 5: Streaming text completion using invoke-with-response-stream API

        Follows boto3 Bedrock Runtime invoke_model_with_response_stream API.
        The response is a stream of chunks where each chunk's 'bytes' contains the model-specific JSON response.
        """
        model_id = get_model("bedrock", "text_completion")
        prompt = "Say hello in exactly 3 words."

        # Prepare request body based on model type
        if "mistral" in model_id.lower():
            body = {
                "prompt": f"<s>[INST] {prompt} [/INST]",
                "max_tokens": 100,
                "temperature": 0.5,
            }
        elif "claude" in model_id.lower():
            body = {
                "prompt": f"\n\nHuman: {prompt}\n\nAssistant:",
                "max_tokens_to_sample": 100,
                "temperature": 0.5,
            }
        else:
            # Generic/Titan fallback
            body = {
                "inputText": prompt,
                "textGenerationConfig": {
                    "maxTokenCount": 100,
                    "temperature": 0.5,
                },
            }

        request_body = json.dumps(body)

        try:
            response = bedrock_client.invoke_model_with_response_stream(
                modelId=model_id,
                contentType="application/json",
                accept="application/json",
                body=request_body,
            )
        except AttributeError:
            pytest.skip(
                "invoke_model_with_response_stream method not available in this boto3 version."
            )
        except Exception as e:
            pytest.fail(f"invoke_model_with_response_stream failed: {e}")

        # Collect streaming chunks
        chunks = []
        text_parts = []

        start_time = time.time()
        timeout = 30

        try:
            stream = response.get("body")
            if stream is None:
                pytest.fail("Response missing 'body' stream")

            for event in stream:
                if time.time() - start_time > timeout:
                    pytest.fail(f"Streaming took longer than {timeout} seconds")

                chunks.append(event)

                if "chunk" in event:
                    chunk = event["chunk"]
                    if "bytes" in chunk:
                        # The bytes contain the raw model response JSON
                        chunk_data = chunk["bytes"].decode("utf-8")
                        try:
                            chunk_json = json.loads(chunk_data)

                            # Extract text based on model type
                            text_chunk = ""
                            if "outputs" in chunk_json:  # Mistral
                                if len(chunk_json["outputs"]) > 0:
                                    text_chunk = chunk_json["outputs"][0].get("text", "")
                            elif "completion" in chunk_json:  # Claude
                                text_chunk = chunk_json.get("completion", "")
                            elif "outputText" in chunk_json:  # Titan
                                text_chunk = chunk_json.get("outputText", "")

                            if text_chunk:
                                text_parts.append(text_chunk)
                        except json.JSONDecodeError:
                            # In case partial JSON is sent (unlikely for this API but possible)
                            pass

        except Exception as e:
            pytest.fail(f"Error iterating event stream: {e}")

        assert len(chunks) > 0, "Should receive at least one streaming chunk"
        combined_text = "".join(text_parts)
        assert (
            len(combined_text.strip()) > 0
        ), f"Streaming response should not be empty. Got: {combined_text}"

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("simple_chat")
    )
    def test_06_simple_chat(self, bedrock_client, test_config, provider, model):
        """Test Case 6: Simple chat interaction using converse API without tools - runs across all available providers"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        messages = convert_to_bedrock_messages(SIMPLE_CHAT_MESSAGES)
        model_id = format_provider_model(provider, model)

        response = bedrock_client.converse(
            modelId=model_id, messages=messages, inferenceConfig={"maxTokens": 100}
        )

        # Validate response structure
        assert_valid_chat_response(response)
        assert "output" in response
        assert "message" in response["output"], "Response should have message in output"

        # Check if response has content
        output_message = response["output"]["message"]
        assert "content" in output_message, "Response message should have content"
        assert (
            len(output_message["content"]) > 0
        ), "Response message should have at least one content item"

        # Extract and validate text content
        text_content = None
        for item in output_message["content"]:
            if "text" in item:
                text_content = item["text"]
                break

        assert text_content is not None, "Response should contain text content"
        assert len(text_content) > 0, "Response text should not be empty"

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("multi_turn_conversation")
    )
    def test_07_multi_turn_conversation(self, bedrock_client, test_config, provider, model):
        """Test Case 7: Multi-turn conversation using converse API - runs across all available providers"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        messages = convert_to_bedrock_messages(MULTI_TURN_MESSAGES)
        model_id = format_provider_model(provider, model)

        response = bedrock_client.converse(
            modelId=model_id, messages=messages, inferenceConfig={"maxTokens": 150}
        )

        # Validate response structure
        assert_valid_chat_response(response)
        assert "output" in response
        assert "message" in response["output"], "Response should have message in output"

        # Extract text content
        output_message = response["output"]["message"]
        text_content = None
        for item in output_message["content"]:
            if "text" in item:
                text_content = item["text"]
                break

        assert text_content is not None, "Response should contain text content"

        # Should mention population or numbers since we asked about Paris population
        text_lower = text_content.lower()
        population_keywords = ["population", "million", "people", "inhabitants", "resident"]
        assert any(
            word in text_lower for word in population_keywords
        ), f"Response should mention population. Got: {text_content[:200]}"

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("multiple_tool_calls")
    )
    def test_08_multiple_tool_calls(self, bedrock_client, test_config, provider, model):
        """Test Case 8: Multiple tool calls in one response using converse API - runs across all available providers"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        messages = convert_to_bedrock_messages(MULTIPLE_TOOL_CALL_MESSAGES)
        tool_config = convert_to_bedrock_tools([WEATHER_TOOL, CALCULATOR_TOOL])
        # Add toolChoice to force the model to use a tool
        tool_config["toolChoice"] = {"any": {}}
        model_id = format_provider_model(provider, model)

        response = bedrock_client.converse(
            modelId=model_id,
            messages=messages,
            toolConfig=tool_config,
            inferenceConfig={"maxTokens": 200},
        )

        # Validate that we have tool calls
        assert_has_tool_calls(response)
        tool_calls = extract_tool_calls(response)

        # Should have at least one tool call, ideally both
        assert len(tool_calls) >= 1, "Should have at least one tool call"

        tool_names = [tc["name"] for tc in tool_calls]
        expected_tools = ["get_weather", "calculate"]

        # Should call relevant tools
        made_relevant_calls = any(name in expected_tools for name in tool_names)
        assert made_relevant_calls, f"Expected tool calls from {expected_tools}, got {tool_names}"

    @skip_if_no_api_key("bedrock")
    def test_09_system_message(self, bedrock_client, test_config):
        """Test Case 9: System message handling using converse API"""
        system_content = "You are a helpful assistant that always responds in exactly 5 words."
        user_content = "Hello, how are you?"

        messages_with_system = [
            {"role": "system", "content": system_content},
            {"role": "user", "content": user_content},
        ]

        # Extract system messages and convert user messages
        system_messages = extract_system_messages(messages_with_system)
        bedrock_messages = convert_to_bedrock_messages(messages_with_system)
        model_id = get_model("bedrock", "chat")

        response = bedrock_client.converse(
            modelId=model_id,
            messages=bedrock_messages,
            system=system_messages,
            inferenceConfig={"maxTokens": 50},
        )

        # Validate response structure
        assert_valid_chat_response(response)

        # Extract text content
        output_message = response["output"]["message"]
        text_content = None
        for item in output_message["content"]:
            if "text" in item:
                text_content = item["text"]
                break

        assert text_content is not None, "Response should contain text content"

        # Check if response is approximately 5 words (allow some flexibility)
        word_count = len(text_content.split())
        assert 3 <= word_count <= 10, f"Expected ~5 words, got {word_count}: {text_content}"

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("end2end_tool_calling")
    )
    def test_10_end2end_tool_calling(self, bedrock_client, test_config, provider, model):
        """Test Case 10: Complete end-to-end tool calling flow - runs across all available providers

        This test covers the full cycle:
        1. User asks a question that requires a tool
        2. Model responds with tool call
        3. We execute the tool and send the result back
        4. Model generates final response using the tool result
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")

        messages = convert_to_bedrock_messages(
            [{"role": "user", "content": "What's the weather in San Francisco?"}]
        )
        tool_config = convert_to_bedrock_tools([WEATHER_TOOL])
        # Add toolChoice to force the model to use a tool
        tool_config["toolChoice"] = {"any": {}}
        model_id = format_provider_model(provider, model)

        # Step 1: Initial request - should trigger tool call
        response = bedrock_client.converse(
            modelId=model_id,
            messages=messages,
            toolConfig=tool_config,
            inferenceConfig={"maxTokens": 500},
        )

        assert_has_tool_calls(response, expected_count=1)
        tool_calls = extract_tool_calls(response)

        # Validate tool call structure
        assert (
            tool_calls[0]["name"] == "get_weather"
        ), f"Expected get_weather tool, got {tool_calls[0]['name']}"
        assert "id" in tool_calls[0], "Tool call should have an ID"
        assert "location" in tool_calls[0]["arguments"], "Tool call should have location argument"

        # Step 2: Append assistant response to messages
        assistant_message = response["output"]["message"]
        messages.append(assistant_message)

        # Step 3: Execute tool and append result
        content = assistant_message["content"]
        tool_uses = [c["toolUse"] for c in content if "toolUse" in c]
        tool_use = tool_uses[0]
        tool_id = tool_use["toolUseId"]
        tool_name = tool_use["name"]
        tool_input = tool_use["input"]

        tool_response_text = mock_tool_response(tool_name, tool_input)

        messages.append(
            {
                "role": "user",
                "content": [
                    {
                        "toolResult": {
                            "toolUseId": tool_id,
                            "content": [{"text": tool_response_text}],
                            "status": "success",
                        }
                    }
                ],
            }
        )

        # Step 4: Final request with tool results
        final_response = bedrock_client.converse(
            modelId=model_id,
            messages=messages,
            toolConfig=tool_config,
            inferenceConfig={"maxTokens": 500},
        )

        # Validate final response
        assert_valid_chat_response(final_response)
        assert "output" in final_response
        assert "message" in final_response["output"]

        # Extract final text content
        output_message = final_response["output"]["message"]
        text_content = None
        for item in output_message["content"]:
            if "text" in item:
                text_content = item["text"]
                break

        assert text_content is not None, "Final response should contain text content"

        # Should mention weather-related terms or location
        final_text = text_content.lower()
        weather_location_keywords = WEATHER_KEYWORDS + LOCATION_KEYWORDS + ["san francisco", "sf"]
        assert any(
            word in final_text for word in weather_location_keywords
        ), f"Final response should mention weather or location. Got: {text_content[:200]}"

    # ==================== FILE API TESTS (Multi-Provider via boto3 with x-model-provider header) ====================

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("batch_file_upload")
    )
    def test_11_file_upload(self, test_config, provider, model):
        """Test Case 11: Upload a file to S3 for batch processing

        Multi-provider test using boto3 S3 client with x-model-provider header.
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_file_upload scenario")

        if provider == "anthropic":
            pytest.skip("Anthropic does not support file upload")

        config = get_config()
        integration_settings = config.get_integration_settings("bedrock")
        s3_bucket = integration_settings.get("s3_bucket")

        if not s3_bucket:
            pytest.skip("S3 bucket not configured for file tests")

        # Get provider-specific S3 client with x-model-provider header
        s3_client: boto3.client = get_provider_s3_client(provider)

        # Create JSONL content for batch
        jsonl_content = create_bedrock_batch_jsonl(model, num_requests=2)

        # Upload to S3
        s3_key = f"bifrost-test-files/batch_input_{int(time.time())}.jsonl"

        response = s3_client.put_object(
            Bucket=s3_bucket,
            Key=s3_key,
            Body=jsonl_content.encode(),
            ContentType="application/jsonl",
        )

        # Extract file ID from ETag header
        file_id = response.get("ETag", "").strip('"')

        assert file_id, "File ID should be returned in ETag header"

        # Cleanup
        try:
            s3_client.delete_object(Bucket=s3_bucket, Key=s3_key, IfMatch=file_id)
        except Exception as e:
            print(f"Warning: Failed to clean up file: {e}")

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("file_list"))
    def test_12_file_list(self, test_config, provider, model):
        """Test Case 12: List files in S3 bucket

        Multi-provider test using boto3 S3 client with x-model-provider header.
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for file_list scenario")

        config = get_config()
        integration_settings = config.get_integration_settings("bedrock")
        s3_bucket = integration_settings.get("s3_bucket")

        if not s3_bucket:
            pytest.skip("S3 bucket not configured for file tests")

        # Get provider-specific S3 client with x-model-provider header
        s3_client = get_provider_s3_client(provider)

        # First upload a file to ensure we have at least one
        jsonl_content = create_bedrock_batch_jsonl(model, num_requests=1)

        s3_key = f"bifrost-test-files/test_list_{int(time.time())}.jsonl"

        upload_response = s3_client.put_object(
            Bucket=s3_bucket,
            Key=s3_key,
            Body=jsonl_content.encode(),
            ContentType="application/jsonl",
        )

        # Extract file ID from ETag header
        file_id = upload_response.get("ETag", "").strip('"')

        try:
            # List files
            response = s3_client.list_objects_v2(Bucket=s3_bucket, Prefix="bifrost-test-files/")

            assert "Contents" in response, "Response should contain Contents"
            assert len(response["Contents"]) >= 1, "Should have at least one file"

            print(f"Success: Listed {len(response['Contents'])} files for provider {provider}")

        finally:
            try:
                s3_client.delete_object(Bucket=s3_bucket, Key=s3_key, IfMatch=file_id)
            except Exception as e:
                print(f"Warning: Failed to clean up file: {e}")

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("file_retrieve")
    )
    def test_13_file_retrieve(self, test_config, provider, model):
        """Test Case 13: Retrieve S3 object metadata

        Multi-provider test using boto3 S3 client with x-model-provider header.
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for file_retrieve scenario")

        config = get_config()
        integration_settings = config.get_integration_settings("bedrock")
        s3_bucket = integration_settings.get("s3_bucket")

        if not s3_bucket:
            pytest.skip("S3 bucket not configured for file tests")

        # Get provider-specific S3 client with x-model-provider header
        s3_client = get_provider_s3_client(provider)

        # Upload a file first
        jsonl_content = create_bedrock_batch_jsonl(model, num_requests=1)

        s3_key = f"bifrost-test-files/test_retrieve_{int(time.time())}.jsonl"

        upload_response = s3_client.put_object(
            Bucket=s3_bucket,
            Key=s3_key,
            Body=jsonl_content.encode(),
            ContentType="application/jsonl",
        )

        # Extract file ID from upload ETag
        upload_file_id = upload_response.get("ETag", "").strip('"')
        print(f"Uploaded file with ID: {upload_file_id}")

        try:
            # Retrieve file metadata (HEAD request)
            response = s3_client.head_object(Bucket=s3_bucket, Key=s3_key, IfMatch=upload_file_id)
            print(f"head response: {response}")
            assert "ContentLength" in response, "Response should contain ContentLength"
            assert response["ContentLength"] > 0, "File should have content"

            # Verify ETag contains file ID
            head_file_id = response.get("ETag", "").strip('"')
            assert head_file_id, "HEAD response should contain file ID in ETag"
            print(
                f"Success: Retrieved metadata, file ID: {head_file_id}, size: {response['ContentLength']} bytes (provider: {provider})"
            )

        finally:
            try:
                s3_client.delete_object(Bucket=s3_bucket, Key=s3_key, IfMatch=upload_file_id)
            except Exception as e:
                print(f"Warning: Failed to clean up file: {e}")

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("file_delete")
    )
    def test_14_file_delete(self, test_config, provider, model):
        """Test Case 14: Delete S3 object

        Multi-provider test using boto3 S3 client with x-model-provider header.
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for file_delete scenario")

        config = get_config()
        integration_settings = config.get_integration_settings("bedrock")
        s3_bucket = integration_settings.get("s3_bucket")

        if not s3_bucket:
            pytest.skip("S3 bucket not configured for file tests")

        # Get provider-specific S3 client with x-model-provider header
        s3_client = get_provider_s3_client(provider)

        # Upload a file first
        jsonl_content = create_bedrock_batch_jsonl(model, num_requests=1)

        s3_key = f"bifrost-test-files/test_delete_{int(time.time())}.jsonl"

        upload_response = s3_client.put_object(
            Bucket=s3_bucket,
            Key=s3_key,
            Body=jsonl_content.encode(),
            ContentType="application/jsonl",
        )
        upload_file_id = upload_response.get("ETag", "").strip('"')

        # Delete the file
        s3_client.delete_object(Bucket=s3_bucket, Key=s3_key, IfMatch=upload_file_id)

        # Verify deletion (head_object should fail)
        try:
            s3_client.head_object(Bucket=s3_bucket, Key=s3_key, IfMatch=upload_file_id)
            pytest.fail("File should have been deleted")
        except Exception:
            # File not found is expected
            print(f"Success: Deleted file (provider: {provider})")

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("file_content")
    )
    def test_15_file_content(self, test_config, provider, model):
        """Test Case 15: Download S3 object content

        Multi-provider test using boto3 S3 client with x-model-provider header.
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for file_content scenario")

        if provider != "bedrock":
            pytest.skip("Bedrock does not support file content download")

        config = get_config()
        integration_settings = config.get_integration_settings("bedrock")
        s3_bucket = integration_settings.get("s3_bucket")

        if not s3_bucket:
            pytest.skip("S3 bucket not configured for file tests")

        # Get provider-specific S3 client with x-model-provider header
        s3_client = get_provider_s3_client(provider)

        # Upload a file with known content
        jsonl_content = create_bedrock_batch_jsonl(model, num_requests=2)

        s3_key = f"bifrost-test-files/test_content_{int(time.time())}.jsonl"

        upload_response = s3_client.put_object(
            Bucket=s3_bucket,
            Key=s3_key,
            Body=jsonl_content.encode(),
            ContentType="application/jsonl",
        )

        # Extract file ID from upload ETag
        upload_file_id = upload_response.get("ETag", "").strip('"')
        print(f"Uploaded file with ID: {upload_file_id}")

        try:
            # Download file content (GET request)
            response = s3_client.get_object(Bucket=s3_bucket, Key=s3_key, IfMatch=upload_file_id)
            downloaded_content = response["Body"].read().decode("utf-8")

            # Verify content matches what we uploaded
            assert (
                jsonl_content == downloaded_content
            ), "Downloaded content should match uploaded content"

            # Verify ETag contains file ID
            get_file_id = response.get("ETag", "").strip('"')
            assert get_file_id, "GET response should contain file ID in ETag"
            print(
                f"Success: Downloaded {len(downloaded_content)} bytes, file ID: {get_file_id} (provider: {provider})"
            )

        finally:
            try:
                s3_client.delete_object(Bucket=s3_bucket, Key=s3_key, IfMatch=upload_file_id)
            except Exception as e:
                print(f"Warning: Failed to clean up file: {e}")

    # ==================== BATCH API TESTS (Multi-Provider via boto3 with x-model-provider header) ====================

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("batch_file_upload")
    )
    def test_16_batch_create(self, test_config, provider, model):
        """Test Case 16: Create a batch inference job with S3 input

        Multi-provider test using boto3 Bedrock client with x-model-provider header.
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_file_upload scenario")

        if provider == "anthropic":
            pytest.skip("Batch API with files is not supported for Anthropic provider")

        # Get provider-specific clients with x-model-provider header
        s3_client = get_provider_s3_client(provider)
        bedrock_client = get_provider_bedrock_batch_client(provider)

        # Upload input file in provider-specific format
        jsonl_content = create_batch_jsonl_for_provider(provider, model, num_requests=2)
        s3_bucket = "bifrost-batch-api-file-upload-testing"
        s3_key = f"bifrost-batch-input/batch_input_{int(time.time())}.jsonl"
        upload_response = s3_client.put_object(
            Bucket=s3_bucket,
            Key=s3_key,
            Body=jsonl_content.encode(),
            ContentType="application/jsonl",
        )

        # Extract file ID from ETag header
        file_id = upload_response.get("ETag", "").strip('"')
        input_uri = f"s3://{s3_bucket}/{s3_key}"
        print(f"Uploaded input file with ID: {file_id}")

        try:
            # Create batch job
            response = bedrock_client.create_model_invocation_job(
                jobName=f"bifrost-test-batch-{int(time.time())}",
                modelId=model,
                roleArn="",
                inputDataConfig={
                    "s3InputDataConfig": {"s3Uri": input_uri, "s3InputFormat": "JSONL"}
                },
                outputDataConfig={
                    "s3OutputDataConfig": {"s3Uri": f"s3://{s3_bucket}/bifrost-batch-output/"}
                },
                tags=[
                    {"key": "endpoint", "value": "/v1/chat/completions"},
                    {"key": "file_id", "value": file_id},
                ],
            )

            assert "jobArn" in response, "Response should contain jobArn"
            print(f"Success: Created batch job {response['jobArn']} for provider {provider}")

        except Exception as e:
            if "not authorized" in str(e).lower() or "access denied" in str(e).lower():
                pytest.skip(f"Batch API not authorized: {e}")
            raise

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("batch_list"))
    def test_17_batch_list(self, test_config, provider, model):
        """Test Case 17: List batch inference jobs

        Multi-provider test using boto3 Bedrock client with x-model-provider header.
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_list scenario")

        if provider == "anthropic":
            pytest.skip("Batch API with files is not supported for Anthropic provider")

        # Get provider-specific Bedrock batch client with x-model-provider header
        bedrock_client = get_provider_bedrock_batch_client(provider)

        try:
            response = bedrock_client.list_model_invocation_jobs(maxResults=10)
            assert (
                "invocationJobSummaries" in response
            ), "Response should contain invocationJobSummaries"

            # Validate job summary structure if there are any jobs
            if len(response["invocationJobSummaries"]) > 0:
                first_job = response["invocationJobSummaries"][0]

                # Required fields should always be present
                assert "jobArn" in first_job, "Job summary should contain jobArn"
                assert "status" in first_job, "Job summary should contain status"

                # jobName and modelId should be present (may be empty strings for older jobs)
                assert "jobName" in first_job, "Job summary should contain jobName"
                assert "modelId" in first_job, "Job summary should contain modelId"

                print(
                    f"First job: jobArn={first_job['jobArn']}, status={first_job['status']}, "
                    f"jobName={first_job.get('jobName', '')}, modelId={first_job.get('modelId', '')}"
                )

            print(
                f"Success: Listed {len(response['invocationJobSummaries'])} batch jobs for provider {provider}"
            )

        except Exception as e:
            if "not authorized" in str(e).lower() or "access denied" in str(e).lower():
                pytest.skip(f"Batch list API not authorized: {e}")
            raise

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("batch_retrieve")
    )
    def test_18_batch_retrieve(self, test_config, provider, model):
        """Test Case 18: Retrieve batch job status

        Multi-provider test using boto3 Bedrock client with x-model-provider header.
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_retrieve scenario")

        if provider == "anthropic":
            pytest.skip("Batch API with files is not supported for Anthropic provider")

        # Get provider-specific Bedrock batch client with x-model-provider header
        bedrock_client = get_provider_bedrock_batch_client(provider)

        try:
            # First list jobs to get a job ARN
            list_response = bedrock_client.list_model_invocation_jobs(maxResults=10)

            if not list_response.get("invocationJobSummaries"):
                pytest.skip("No batch jobs available to retrieve")

            # Get the first job ARN
            job_arn = list_response["invocationJobSummaries"][0]["jobArn"]

            # Retrieve job details
            response = bedrock_client.get_model_invocation_job(jobIdentifier=job_arn)

            # Required fields
            assert "jobArn" in response, "Response should contain jobArn"
            assert response["jobArn"], "jobArn should not be empty"
            assert "status" in response, "Response should contain status"
            assert response["status"] in [
                "Submitted",
                "Validating",
                "Scheduled",
                "InProgress",
                "Completed",
                "Failed",
                "Stopping",
                "Stopped",
                "PartiallyCompleted",
                "Expired",
            ], f"Invalid status: {response['status']}"

            # Expected fields (present for most jobs)
            if "jobName" in response:
                assert isinstance(response["jobName"], str), "jobName should be a string"

            if "modelId" in response:
                assert isinstance(response["modelId"], str), "modelId should be a string"

            if "inputDataConfig" in response:
                assert "s3InputDataConfig" in response["inputDataConfig"]
                assert "s3Uri" in response["inputDataConfig"]["s3InputDataConfig"]

            if "outputDataConfig" in response:
                assert "s3OutputDataConfig" in response["outputDataConfig"]
                assert "s3Uri" in response["outputDataConfig"]["s3OutputDataConfig"]

            if "submitTime" in response:
                assert response["submitTime"] is not None, "submitTime should not be None"

            print(
                f"Success: Retrieved job {response['jobArn']} with status {response['status']} for provider {provider}"
            )

        except Exception as e:
            if "not authorized" in str(e).lower() or "access denied" in str(e).lower():
                pytest.skip(f"Batch retrieve API not authorized: {e}")
            raise

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("batch_cancel")
    )
    def test_19_batch_cancel(self, test_config, provider, model):
        """Test Case 19: Cancel/stop a batch job

        Multi-provider test using boto3 Bedrock client with x-model-provider header.
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_cancel scenario")

        if provider == "anthropic":
            pytest.skip("File based batch is not supported for Anthropic provider")

        config = get_config()
        integration_settings = config.get_integration_settings("bedrock")
        s3_bucket = integration_settings.get("s3_bucket")
        role_arn = integration_settings.get("batch_role_arn")

        if not s3_bucket or not role_arn:
            pytest.skip("S3 bucket or role ARN not configured for batch tests")

        # Get provider-specific clients with x-model-provider header
        s3_client = get_provider_s3_client(provider)
        bedrock_client = get_provider_bedrock_batch_client(provider)

        # Upload a test file to S3 (use provider-specific format)
        jsonl_content = create_batch_jsonl_for_provider(provider, model, num_requests=5)

        s3_key = f"bifrost-batch-cancel/batch_cancel_{int(time.time())}.jsonl"
        upload_response = s3_client.put_object(
            Bucket=s3_bucket,
            Key=s3_key,
            Body=jsonl_content.encode(),
            ContentType="application/jsonl",
        )

        # Validate S3 upload response
        assert upload_response is not None, "S3 upload should return a response"
        assert "ETag" in upload_response, "S3 response should contain ETag"
        assert upload_response["ETag"], "ETag should not be empty"

        # Extract file ID from ETag header
        file_id = upload_response.get("ETag", "").strip('"')
        input_uri = f"s3://{s3_bucket}/{s3_key}"
        print(f"Uploaded input file with ID: {file_id}")

        try:
            # Create a job to cancel
            create_response = bedrock_client.create_model_invocation_job(
                jobName=f"bifrost-cancel-test-{int(time.time())}",
                modelId=model,
                roleArn=role_arn,
                inputDataConfig={
                    "s3InputDataConfig": {"s3Uri": input_uri, "s3InputFormat": "JSONL"}
                },
                outputDataConfig={
                    "s3OutputDataConfig": {
                        "s3Uri": f"s3://{s3_bucket}/bifrost-batch-cancel-output/"
                    }
                },
                tags=[
                    {"key": "endpoint", "value": "/v1/chat/completions"},
                    {"key": "file_id", "value": file_id},
                ],
            )

            # Validate job creation response
            assert "jobArn" in create_response, "Response should contain jobArn"
            assert create_response["jobArn"], "jobArn should not be empty"
            assert create_response["jobArn"].startswith("arn:"), "jobArn should be a valid ARN"

            print(f"create_response: {create_response}")

            job_arn = create_response["jobArn"]

            print("stopping the job")

            # Cancel the job
            bedrock_client.stop_model_invocation_job(jobIdentifier=job_arn)

            # Verify job was cancelled by checking status
            status_response = bedrock_client.get_model_invocation_job(jobIdentifier=job_arn)
            assert "status" in status_response, "Status response should contain status"
            assert status_response["status"] in [
                "Stopping",
                "Stopped",
                "Failed",
                "Completed",
            ], f"Job status should indicate cancellation in progress or complete: {status_response['status']}"

            print(
                f"Success: Cancelled job {job_arn} with status {status_response['status']} for provider {provider}"
            )

        except Exception as e:
            error_str = str(e).lower()
            if "validation" in error_str or "conflict" in error_str:
                # Job may not be cancellable depending on its state
                print(f"Note: Could not cancel job (may already be completed): {e}")
            elif "not authorized" in error_str or "access denied" in error_str:
                pytest.skip(f"Batch cancel API not authorized: {e}")
            else:
                raise

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("batch_file_upload")
    )
    def test_20_batch_e2e(self, test_config, provider, model):
        """Test Case 20: End-to-end batch workflow

        Multi-provider test using boto3 with x-model-provider header.
        Complete workflow: upload file to S3 -> create batch job -> monitor status.
        """
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for batch_file_upload scenario")

        if provider == "anthropic":
            pytest.skip("File based batch is not supported for Anthropic provider")

        config = get_config()
        integration_settings = config.get_integration_settings("bedrock")
        s3_bucket = integration_settings.get("s3_bucket")
        role_arn = integration_settings.get("batch_role_arn")

        if not s3_bucket or not role_arn:
            pytest.skip("S3 bucket or role ARN not configured for batch tests")

        # Get provider-specific clients with x-model-provider header
        s3_client = get_provider_s3_client(provider)

        print(f"getting the bedrock client for provider {provider}")

        bedrock_client = get_provider_bedrock_batch_client(provider)

        # Step 1: Upload input file to S3
        jsonl_content = create_batch_jsonl_for_provider(provider, model, num_requests=2)

        s3_key = f"bifrost-batch-e2e/batch_e2e_{int(time.time())}.jsonl"
        upload_response = s3_client.put_object(
            Bucket=s3_bucket,
            Key=s3_key,
            Body=jsonl_content.encode(),
            ContentType="application/jsonl",
        )

        # Extract file ID from ETag header
        file_id = upload_response.get("ETag", "").strip('"')
        input_uri = f"s3://{s3_bucket}/{s3_key}"
        print(f"Step 1: Uploaded input file with ID: {file_id} for provider {provider}")

        try:
            # Step 2: Create batch job
            create_response = bedrock_client.create_model_invocation_job(
                jobName=f"bifrost-e2e-{int(time.time())}",
                modelId=model,
                roleArn=role_arn,
                inputDataConfig={
                    "s3InputDataConfig": {"s3Uri": input_uri, "s3InputFormat": "JSONL"}
                },
                outputDataConfig={
                    "s3OutputDataConfig": {"s3Uri": f"s3://{s3_bucket}/bifrost-batch-e2e-output/"}
                },
                tags=[
                    {"key": "endpoint", "value": "/v1/chat/completions"},
                    {"key": "file_id", "value": file_id},
                ],
            )

            job_arn = create_response["jobArn"]
            print(f"Step 2: Created batch job {job_arn}")

            # Step 3: Poll for status (with timeout)
            max_polls = 5  # Quick check, not waiting for completion
            for i in range(max_polls):
                status_response = bedrock_client.get_model_invocation_job(jobIdentifier=job_arn)
                status = status_response.get("status", "Unknown")
                print(f"Step 3: Job status ({i+1}/{max_polls}): {status}")

                if status in ["Completed", "Failed", "Stopped"]:
                    break

                time.sleep(2)

            print(f"Success: End-to-end batch workflow completed for provider {provider}")

        except Exception as e:
            if "not authorized" in str(e).lower() or "access denied" in str(e).lower():
                pytest.skip(f"Batch API not authorized: {e}")
            raise

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("prompt_caching")
    )
    def test_21_prompt_caching_system(self, bedrock_client, provider, model):
        """Test Case 21: Prompt caching with system message checkpoint"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for prompt_caching scenario")

        print(f"\n=== Testing System Message Caching for provider {provider} ===")
        print("First request: Creating cache with system message checkpoint...")

        system_with_cache = [
            {"text": "You are an AI assistant tasked with analyzing legal documents."},
            {"text": PROMPT_CACHING_LARGE_CONTEXT},
            {"cachePoint": {"type": "default"}},  # Cache all preceding system content
        ]

        # First request - should create cache
        response1 = bedrock_client.converse(
            modelId=format_provider_model(provider, model),
            system=system_with_cache,
            messages=[
                {
                    "role": "user",
                    "content": [{"text": "What are the key elements of contract formation?"}],
                }
            ],
        )

        # Validate first response
        assert response1 is not None
        assert "usage" in response1
        cache_write_tokens = validate_cache_write(response1["usage"], "First request")

        # Second request with same system - should hit cache
        print("\nSecond request: Hitting cache with same system checkpoint...")
        response2 = bedrock_client.converse(
            modelId=format_provider_model(provider, model),
            system=system_with_cache,
            messages=[
                {
                    "role": "user",
                    "content": [{"text": "Explain the purpose of force majeure clauses."}],
                }
            ],
        )

        cache_read_tokens = validate_cache_read(response2["usage"], "Second request")

        # Validate that cache read tokens are approximately equal to cache write tokens
        assert (
            abs(cache_write_tokens - cache_read_tokens) < 100
        ), f"Cache read tokens ({cache_read_tokens}) should be close to cache write tokens ({cache_write_tokens})"

        print(
            f"✓ System caching validated - Cache created: {cache_write_tokens} tokens, "
            f"Cache read: {cache_read_tokens} tokens"
        )

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("prompt_caching")
    )
    def test_22_prompt_caching_messages(self, bedrock_client, provider, model):
        """Test Case 22: Prompt caching with messages checkpoint"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for prompt_caching scenario")

        print(f"\n=== Testing Messages Caching for provider {provider} ===")
        print("First request: Creating cache with messages checkpoint...")

        # First request with cache point in user message
        response1 = bedrock_client.converse(
            modelId=format_provider_model(provider, model),
            messages=[
                {
                    "role": "user",
                    "content": [
                        {"text": "Here is a large legal document to analyze:"},
                        {"text": PROMPT_CACHING_LARGE_CONTEXT},
                        {"cachePoint": {"type": "default"}},  # Cache all preceding message content
                        {"text": "What are the main indemnification principles?"},
                    ],
                }
            ],
        )

        assert response1 is not None
        assert "usage" in response1
        cache_write_tokens = validate_cache_write(response1["usage"], "First request")

        # Second request with same cached content
        print("\nSecond request: Hitting cache with same messages checkpoint...")
        response2 = bedrock_client.converse(
            modelId=format_provider_model(provider, model),
            messages=[
                {
                    "role": "user",
                    "content": [
                        {"text": "Here is a large legal document to analyze:"},
                        {"text": PROMPT_CACHING_LARGE_CONTEXT},
                        {"cachePoint": {"type": "default"}},
                        {"text": "Summarize the dispute resolution methods."},
                    ],
                }
            ],
        )

        cache_read_tokens = validate_cache_read(response2["usage"], "Second request")

        # Validate that cache read tokens are approximately equal to cache write tokens
        assert (
            abs(cache_write_tokens - cache_read_tokens) < 100
        ), f"Cache read tokens ({cache_read_tokens}) should be close to cache write tokens ({cache_write_tokens})"

        print(
            f"✓ Messages caching validated - Cache created: {cache_write_tokens} tokens, "
            f"Cache read: {cache_read_tokens} tokens"
        )

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("prompt_caching")
    )
    def test_23_prompt_caching_tools(self, bedrock_client, provider, model):
        """Test Case 23: Prompt caching with tools checkpoint (12 tools)"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for prompt_caching scenario")

        print(f"\n=== Testing Tools Caching for provider {provider} ===")
        print("First request: Creating cache with tools checkpoint...")

        # Convert tools to Bedrock format (using 12 tools for larger cache test)
        bedrock_tools = []
        for tool in PROMPT_CACHING_TOOLS:
            bedrock_tools.append(
                {
                    "toolSpec": {
                        "name": tool["name"],
                        "description": tool["description"],
                        "inputSchema": {"json": tool["parameters"]},
                    }
                }
            )

        # Add cache point after all tools
        bedrock_tools.append({"cachePoint": {"type": "default"}})  # Cache all 12 tool definitions

        # First request with tool cache point
        tool_config = {
            "tools": bedrock_tools,
        }

        response1 = bedrock_client.converse(
            modelId=format_provider_model(provider, model),
            toolConfig=tool_config,
            messages=[{"role": "user", "content": [{"text": "What's the weather in Boston?"}]}],
        )

        assert response1 is not None
        assert "usage" in response1
        cache_write_tokens = validate_cache_write(response1["usage"], "First request")

        # Second request with same tools
        print("\nSecond request: Hitting cache with same tools checkpoint...")
        response2 = bedrock_client.converse(
            modelId=format_provider_model(provider, model),
            toolConfig=tool_config,
            messages=[{"role": "user", "content": [{"text": "Calculate 42 * 17"}]}],
        )

        cache_read_tokens = validate_cache_read(response2["usage"], "Second request")

        # Validate that cache read tokens are approximately equal to cache write tokens
        assert (
            abs(cache_write_tokens - cache_read_tokens) < 100
        ), f"Cache read tokens ({cache_read_tokens}) should be close to cache write tokens ({cache_write_tokens})"

        print(
            f"✓ Tools caching validated - Cache created: {cache_write_tokens} tokens, "
            f"Cache read: {cache_read_tokens} tokens"
        )


def validate_cache_write(usage: Dict[str, Any], operation: str) -> int:
    """Validate cache write operation and return tokens written"""
    print(
        f"{operation} usage - inputTokens: {usage.get('inputTokens', 0)}, "
        f"cacheWriteInputTokens: {usage.get('cacheWriteInputTokens', 0)}, "
        f"cacheReadInputTokens: {usage.get('cacheReadInputTokens', 0)}"
    )

    cache_write_tokens = usage.get("cacheWriteInputTokens", 0)
    assert (
        cache_write_tokens > 0
    ), f"{operation} should write to cache (got {cache_write_tokens} tokens)"

    return cache_write_tokens


def validate_cache_read(usage: Dict[str, Any], operation: str) -> int:
    """Validate cache read operation and return tokens read"""
    print(
        f"{operation} usage - inputTokens: {usage.get('inputTokens', 0)}, "
        f"cacheWriteInputTokens: {usage.get('cacheWriteInputTokens', 0)}, "
        f"cacheReadInputTokens: {usage.get('cacheReadInputTokens', 0)}"
    )

    cache_read_tokens = usage.get("cacheReadInputTokens", 0)
    assert (
        cache_read_tokens > 0
    ), f"{operation} should read from cache (got {cache_read_tokens} tokens)"

    return cache_read_tokens


class TestBedrockCountTokens:
    """Test suite for Bedrock Count Tokens API"""

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("count_tokens")
    )
    def test_24_count_tokens_simple_messages(self, bedrock_client, provider, model):
        """Test Case 24: Count tokens from simple messages"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for count_tokens scenario")

        print(f"\n=== Testing Count Tokens (Simple Messages) for provider {provider} ===")

        # Prepare the count tokens request with simple messages
        input_data = {
            "converse": {
                "messages": [{"role": "user", "content": [{"text": "Hello, how are you?"}]}]
            }
        }

        # Call the count_tokens method
        response = bedrock_client.count_tokens(
            modelId=format_provider_model(provider, model), input=input_data
        )

        # Validate response structure
        assert response is not None, "Response should not be None"
        assert "inputTokens" in response, "Response should have inputTokens field"
        assert isinstance(response["inputTokens"], int), "inputTokens should be an integer"
        assert (
            response["inputTokens"] > 0
        ), f"inputTokens should be positive, got {response['inputTokens']}"

        # Simple text should have a reasonable token count (between 3-20 tokens)
        assert (
            3 <= response["inputTokens"] <= 20
        ), f"Simple text should have 3-20 tokens, got {response['inputTokens']}"

        print(f"✓ Simple messages token count: {response['inputTokens']} tokens")

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("count_tokens")
    )
    def test_25_count_tokens_with_system_message(self, bedrock_client, provider, model):
        """Test Case 25: Count tokens with system message"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for count_tokens scenario")

        print(f"\n=== Testing Count Tokens (With System) for provider {provider} ===")

        # Prepare the count tokens request with system message
        input_data = {
            "converse": {
                "system": [{"text": "You are a helpful assistant that provides concise answers."}],
                "messages": [
                    {"role": "user", "content": [{"text": "What is the capital of France?"}]}
                ],
            }
        }

        # Call the count_tokens method
        response = bedrock_client.count_tokens(
            modelId=format_provider_model(provider, model), input=input_data
        )

        # Validate response structure
        assert response is not None, "Response should not be None"
        assert "inputTokens" in response, "Response should have inputTokens field"
        assert isinstance(response["inputTokens"], int), "inputTokens should be an integer"
        assert (
            response["inputTokens"] > 0
        ), f"inputTokens should be positive, got {response['inputTokens']}"

        # With system message should have more tokens than simple text
        assert (
            response["inputTokens"] > 5
        ), f"With system message should have >5 tokens, got {response['inputTokens']}"

        print(f"✓ With system message token count: {response['inputTokens']} tokens")

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("count_tokens")
    )
    def test_26_count_tokens_with_tools(self, bedrock_client, provider, model):
        """Test Case 26: Count tokens with tool definitions"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for count_tokens scenario")

        print(f"\n=== Testing Count Tokens (With Tools) for provider {provider} ===")

        # Convert tools to Bedrock format
        bedrock_tools = []
        for tool in [WEATHER_TOOL, CALCULATOR_TOOL]:
            bedrock_tools.append(
                {
                    "toolSpec": {
                        "name": tool["name"],
                        "description": tool["description"],
                        "inputSchema": {"json": tool["parameters"]},
                    }
                }
            )

        input_data = {
            "converse": {
                "toolConfig": {"tools": bedrock_tools},
                "messages": [
                    {
                        "role": "user",
                        "content": [{"text": "What's the weather in Boston and what is 25 + 17?"}],
                    }
                ],
            }
        }

        # Call the count_tokens method
        response = bedrock_client.count_tokens(
            modelId=format_provider_model(provider, model), input=input_data
        )

        # Validate response structure
        assert response is not None, "Response should not be None"
        assert "inputTokens" in response, "Response should have inputTokens field"
        assert isinstance(response["inputTokens"], int), "inputTokens should be an integer"
        assert (
            response["inputTokens"] > 0
        ), f"inputTokens should be positive, got {response['inputTokens']}"

        # With tools should have significantly more tokens
        assert (
            response["inputTokens"] > 20
        ), f"With tools should have >20 tokens, got {response['inputTokens']}"

        print(f"✓ With tools token count: {response['inputTokens']} tokens")

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("count_tokens")
    )
    def test_27_count_tokens_long_text(self, bedrock_client, provider, model):
        """Test Case 27: Count tokens from long text"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for count_tokens scenario")

        print(f"\n=== Testing Count Tokens (Long Text) for provider {provider} ===")

        # Prepare a long text message
        long_text = "This is a longer text that should have more tokens. " * 20

        input_data = {
            "converse": {"messages": [{"role": "user", "content": [{"text": long_text}]}]}
        }

        # Call the count_tokens method
        response = bedrock_client.count_tokens(
            modelId=format_provider_model(provider, model), input=input_data
        )

        # Validate response structure
        assert response is not None, "Response should not be None"
        assert "inputTokens" in response, "Response should have inputTokens field"
        assert isinstance(response["inputTokens"], int), "inputTokens should be an integer"
        assert (
            response["inputTokens"] > 50
        ), f"Long text should have >50 tokens, got {response['inputTokens']}"

        print(f"✓ Long text token count: {response['inputTokens']} tokens")

    @pytest.mark.parametrize(
        "provider,model", get_cross_provider_params_for_scenario("count_tokens")
    )
    def test_28_count_tokens_multi_turn_conversation(self, bedrock_client, provider, model):
        """Test Case 28: Count tokens from multi-turn conversation"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for count_tokens scenario")

        print(f"\n=== Testing Count Tokens (Multi-Turn) for provider {provider} ===")

        # Prepare multi-turn conversation
        input_data = {
            "converse": {
                "messages": [
                    {"role": "user", "content": [{"text": "What is the capital of France?"}]},
                    {"role": "assistant", "content": [{"text": "The capital of France is Paris."}]},
                    {"role": "user", "content": [{"text": "What is the population?"}]},
                ]
            }
        }

        # Call the count_tokens method
        response = bedrock_client.count_tokens(
            modelId=format_provider_model(provider, model), input=input_data
        )

        # Validate response structure
        assert response is not None, "Response should not be None"
        assert "inputTokens" in response, "Response should have inputTokens field"
        assert isinstance(response["inputTokens"], int), "inputTokens should be an integer"
        assert (
            response["inputTokens"] > 0
        ), f"inputTokens should be positive, got {response['inputTokens']}"

        # Multi-turn should have more tokens than simple messages
        assert (
            response["inputTokens"] > 15
        ), f"Multi-turn conversation should have >15 tokens, got {response['inputTokens']}"

        print(f"✓ Multi-turn conversation token count: {response['inputTokens']} tokens")
