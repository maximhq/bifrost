"""
Bedrock Integration Tests

🤖 MODELS USED:
- Text Completion (invoke): mistral.mistral-7b-instruct-v0:2
- Chat (converse): anthropic.claude-3-haiku-20240307-v1:0
- Vision (converse): anthropic.claude-3-haiku-20240307-v1:0
- Tools (converse): anthropic.claude-3-haiku-20240307-v1:0

Tests core scenarios using AWS SDK (boto3) directly against Bifrost:
1. Text completion (invoke)
2. Chat with tool calling and tool result (converse)
3. Image analysis (converse)
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
    mock_tool_response,
    assert_valid_chat_response,
    assert_has_tool_calls,
    skip_if_no_api_key,
    WEATHER_KEYWORDS,
    LOCATION_KEYWORDS,
)
from .utils.config_loader import get_model, get_config, get_integration_url


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


class TestBedrockIntegration:
    """Test suite for Bedrock integration covering core scenarios"""

    @skip_if_no_api_key("bedrock")
    def test_01_text_completion_invoke(self, bedrock_client, test_config):
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

    @skip_if_no_api_key("bedrock")
    def test_02_converse_with_tool_calling(self, bedrock_client, test_config):
        """Test Case 2: Chat with tool calling and tool result using converse API"""
        messages = convert_to_bedrock_messages([{"role": "user", "content": "What's the weather in Boston?"}])
        tool_config = convert_to_bedrock_tools([WEATHER_TOOL])
        model_id = get_model("bedrock", "chat")

        # 1. Initial Request - should trigger tool call
        response = bedrock_client.converse(
            modelId=model_id,
            messages=messages,
            toolConfig=tool_config,
            inferenceConfig={"maxTokens": 100}
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
            inferenceConfig={"maxTokens": 150}
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


    @skip_if_no_api_key("bedrock")
    def test_03_image_analysis(self, bedrock_client, test_config):
        """Test Case 3: Image analysis using converse API"""
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
        
        response = bedrock_client.converse(
            modelId=get_model("bedrock", "vision"),
            messages=messages,
            inferenceConfig={"maxTokens": 200}
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

    @skip_if_no_api_key("bedrock")
    def test_04_converse_streaming(self, bedrock_client, test_config):
        """Test Case 4: Streaming chat completion using converse-stream API with boto3
        
        Follows boto3 Bedrock Runtime converse_stream API:
        https://boto3.amazonaws.com/v1/documentation/api/1.35.6/reference/services/bedrock-runtime/client/converse_stream.html
        """
        messages = convert_to_bedrock_messages([{"role": "user", "content": "Say hello in exactly 3 words."}])
        model_id = get_model("bedrock", "chat")


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
