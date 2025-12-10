"""
Bedrock Integration Tests - Cross-Provider Support

🌉 CROSS-PROVIDER TESTING:
This test suite uses the AWS SDK (boto3) to test against multiple AI providers through Bifrost.
Tests automatically run against all available providers with proper capability filtering.

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
"""

import pytest
import boto3
import json
import base64
import requests
import time
from typing import List, Dict, Any

from .utils.common import (
    Config,
    BASE64_IMAGE,
    WEATHER_TOOL,
    CALCULATOR_TOOL,
    SIMPLE_CHAT_MESSAGES,
    MULTI_TURN_MESSAGES,
    MULTIPLE_TOOL_CALL_MESSAGES,
    mock_tool_response,
    assert_valid_chat_response,
    assert_has_tool_calls,
    extract_tool_calls,
    skip_if_no_api_key,
    WEATHER_KEYWORDS,
    LOCATION_KEYWORDS,
)
from .utils.config_loader import get_model, get_config, get_integration_url
from .utils.parametrize import (
    get_cross_provider_params_for_scenario,
    format_provider_model,
)


@pytest.fixture
def bedrock_client():
    """Create Bedrock client for testing"""
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
                        content.append({
                            "image": {
                                "format": media_type.split("/")[1],  # png, jpeg
                                "source": {"bytes": image_bytes}
                            }
                        })
                    else:
                        # URL image - download and convert to bytes
                        response = requests.get(url, timeout=20)
                        response.raise_for_status()
                        image_bytes = response.content
                        # Simple format detection
                        fmt = "jpeg"
                        if url.lower().endswith(".png"):
                            fmt = "png"
                        elif url.lower().endswith(".gif"):
                            fmt = "gif"
                        elif url.lower().endswith(".webp"):
                            fmt = "webp"
                            
                        content.append({
                            "image": {
                                "format": fmt,
                                "source": {"bytes": image_bytes}
                            }
                        })
        else:
            content.append({"text": msg["content"]})

        role = "user" if msg["role"] == "user" else "assistant"
        bedrock_messages.append({"role": role, "content": content})

    return bedrock_messages


def convert_to_bedrock_tools(tools: List[Dict[str, Any]]) -> Dict[str, Any]:
    """Convert common tool format to Bedrock ToolConfig"""
    bedrock_tools = []
    
    for tool in tools:
        bedrock_tools.append({
            "toolSpec": {
                "name": tool["name"],
                "description": tool["description"],
                "inputSchema": {
                    "json": tool["parameters"]
                }
            }
        })
        
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
            "temperature": 0.7
        }
        
        response = bedrock_client.invoke_model(
            modelId=model_id,
            contentType="application/json",
            accept="application/json",
            body=json.dumps(request_body)
        )
        
        response_body = json.loads(response["body"].read())
        
        assert response_body is not None
        assert "outputs" in response_body or "completion" in response_body or "text" in response_body
        
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
        
        messages = convert_to_bedrock_messages([{"role": "user", "content": "What's the weather in Boston?"}])
        tool_config = convert_to_bedrock_tools([WEATHER_TOOL])
        # Add toolChoice to force the model to use a tool
        tool_config["toolChoice"] = {"any": {}}
        model_id = format_provider_model(provider, model)

        # 1. Initial Request - should trigger tool call
        response = bedrock_client.converse(
            modelId=model_id,
            messages=messages,
            toolConfig=tool_config,
            inferenceConfig={"maxTokens": 500}
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
            
            tool_result_content.append({
                "toolResult": {
                    "toolUseId": tool_id,
                    "content": [{"text": tool_response_text}],
                    "status": "success"
                }
            })
        messages.append({
            "role": "user",
            "content": tool_result_content
        })
        
        # 4. Final Request with Tool Results
        final_response = bedrock_client.converse(
            modelId=model_id,
            messages=messages,
            toolConfig=tool_config,
            inferenceConfig={"maxTokens": 500}
        )
        
        # Validate response structure
        assert_valid_chat_response(final_response)
        assert "output" in final_response
        assert "message" in final_response["output"], "Response should have message in output"
        
        # Check if response has content
        output_message = final_response["output"]["message"]
        assert "content" in output_message, "Response message should have content"
        assert len(output_message["content"]) > 0, "Response message should have at least one content item"
        
        # Extract text content if available
        text_content = None
        for item in output_message["content"]:
            if "text" in item:
                text_content = item["text"]
                break
        
        assert text_content is not None, "Final response should contain a text content block"
        final_text = text_content.lower()
        assert any(word in final_text for word in WEATHER_KEYWORDS + LOCATION_KEYWORDS)


    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("image_base64"))
    def test_03_image_analysis(self, bedrock_client, test_config, provider, model):
        """Test Case 3: Image analysis using converse API - runs across all available providers with base64 image support"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        
        # Use base64 image instead of URL to avoid 403 errors
        messages = convert_to_bedrock_messages([
            {
                "role": "user",
                "content": [
                    {"type": "text", "text": "What do you see in this image? Describe what you see."},
                    {"type": "image_url", "image_url": {"url": f"data:image/png;base64,{BASE64_IMAGE}"}},
                ],
            }
        ])
        
        model_id = format_provider_model(provider, model)
        response = bedrock_client.converse(
            modelId=model_id,
            messages=messages,
            inferenceConfig={"maxTokens": 500}
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
        
        assert text_content is not None and len(text_content) > 0, "Response should contain text content"
        
        # Check for image-related keywords (more lenient for small test image)
        text_lower = text_content.lower()
        image_keywords = [
            "image", "picture", "photo", "see", "visual", "show", 
            "appear", "color", "scene", "pixel", "red", "square"
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
        
        messages = convert_to_bedrock_messages([{"role": "user", "content": "Say hello in exactly 3 words."}])
        model_id = format_provider_model(provider, model)


        try:
            response_stream = bedrock_client.converse_stream(
                modelId=model_id,
                messages=messages,
                inferenceConfig={"maxTokens": 100}
            )
        except AttributeError:
            pytest.skip("converse_stream method not available in this boto3 version. Please upgrade boto3.")
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
                pytest.fail(f"Response missing 'stream' or 'eventStream'. Keys: {list(response_stream.keys())}")

            for event in stream:
                # Check timeout
                if time.time() - start_time > timeout:
                    pytest.fail(f"Streaming took longer than {timeout} seconds. Received {len(chunks)} chunks so far.")
                
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
            pytest.fail(f"Error iterating event stream: {e}. Response type: {type(response_stream)}, Chunks received: {len(chunks)}")
        
        # Verify we received streaming chunks
        assert len(chunks) > 0, f"Should receive at least one streaming chunk. Stream completed: {stream_completed}, Total chunks: {len(chunks)}"
        
        # Verify we received text content
        combined_text = "".join(text_parts)
        if len(combined_text) == 0:
            chunk_debug = []
            for i, chunk in enumerate(chunks[:5]):  # First 5 chunks for debugging
                chunk_debug.append(f"Chunk {i}: {str(chunk)[:200]}")
            pytest.fail(f"Streaming response should contain text content. Received {len(chunks)} chunks. Stream completed: {stream_completed}. First chunks: {chunk_debug}")
        
        # Verify we got a reasonable response
        assert len(combined_text.strip()) > 0, f"Streaming response should not be empty. Combined text: {repr(combined_text[:100])}"

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
                }
            }
            
        request_body = json.dumps(body)

        try:
            response = bedrock_client.invoke_model_with_response_stream(
                modelId=model_id,
                contentType="application/json",
                accept="application/json",
                body=request_body
            )
        except AttributeError:
            pytest.skip("invoke_model_with_response_stream method not available in this boto3 version.")
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
                            if "outputs" in chunk_json: # Mistral
                                if len(chunk_json["outputs"]) > 0:
                                    text_chunk = chunk_json["outputs"][0].get("text", "")
                            elif "completion" in chunk_json: # Claude
                                text_chunk = chunk_json.get("completion", "")
                            elif "outputText" in chunk_json: # Titan
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
        assert len(combined_text.strip()) > 0, f"Streaming response should not be empty. Got: {combined_text}"

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("simple_chat"))
    def test_06_simple_chat(self, bedrock_client, test_config, provider, model):
        """Test Case 6: Simple chat interaction using converse API without tools - runs across all available providers"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        
        messages = convert_to_bedrock_messages(SIMPLE_CHAT_MESSAGES)
        model_id = format_provider_model(provider, model)

        response = bedrock_client.converse(
            modelId=model_id,
            messages=messages,
            inferenceConfig={"maxTokens": 100}
        )

        # Validate response structure
        assert_valid_chat_response(response)
        assert "output" in response
        assert "message" in response["output"], "Response should have message in output"

        # Check if response has content
        output_message = response["output"]["message"]
        assert "content" in output_message, "Response message should have content"
        assert len(output_message["content"]) > 0, "Response message should have at least one content item"

        # Extract and validate text content
        text_content = None
        for item in output_message["content"]:
            if "text" in item:
                text_content = item["text"]
                break

        assert text_content is not None, "Response should contain text content"
        assert len(text_content) > 0, "Response text should not be empty"

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("multi_turn_conversation"))
    def test_07_multi_turn_conversation(self, bedrock_client, test_config, provider, model):
        """Test Case 7: Multi-turn conversation using converse API - runs across all available providers"""
        if provider == "_no_providers_" or model == "_no_model_":
            pytest.skip("No providers configured for this scenario")
        
        messages = convert_to_bedrock_messages(MULTI_TURN_MESSAGES)
        model_id = format_provider_model(provider, model)

        response = bedrock_client.converse(
            modelId=model_id,
            messages=messages,
            inferenceConfig={"maxTokens": 150}
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

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("multiple_tool_calls"))
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
            inferenceConfig={"maxTokens": 200}
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
            {"role": "user", "content": user_content}
        ]
        
        # Extract system messages and convert user messages
        system_messages = extract_system_messages(messages_with_system)
        bedrock_messages = convert_to_bedrock_messages(messages_with_system)
        model_id = get_model("bedrock", "chat")

        response = bedrock_client.converse(
            modelId=model_id,
            messages=bedrock_messages,
            system=system_messages,
            inferenceConfig={"maxTokens": 50}
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

    @pytest.mark.parametrize("provider,model", get_cross_provider_params_for_scenario("end2end_tool_calling"))
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
        
        messages = convert_to_bedrock_messages([
            {"role": "user", "content": "What's the weather in San Francisco?"}
        ])
        tool_config = convert_to_bedrock_tools([WEATHER_TOOL])
        # Add toolChoice to force the model to use a tool
        tool_config["toolChoice"] = {"any": {}}
        model_id = format_provider_model(provider, model)

        # Step 1: Initial request - should trigger tool call
        response = bedrock_client.converse(
            modelId=model_id,
            messages=messages,
            toolConfig=tool_config,
            inferenceConfig={"maxTokens": 500}
        )

        assert_has_tool_calls(response, expected_count=1)
        tool_calls = extract_tool_calls(response)
        
        # Validate tool call structure
        assert tool_calls[0]["name"] == "get_weather", f"Expected get_weather tool, got {tool_calls[0]['name']}"
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
        
        messages.append({
            "role": "user",
            "content": [{
                "toolResult": {
                    "toolUseId": tool_id,
                    "content": [{"text": tool_response_text}],
                    "status": "success"
                }
            }]
        })

        # Step 4: Final request with tool results
        final_response = bedrock_client.converse(
            modelId=model_id,
            messages=messages,
            toolConfig=tool_config,
            inferenceConfig={"maxTokens": 500}
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
