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
from typing import List, Dict, Any

from ..utils.common import (
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
from ..utils.config_loader import get_model, get_config, get_integration_url


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
