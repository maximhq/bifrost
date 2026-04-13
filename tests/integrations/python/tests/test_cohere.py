"""
Cohere Integration Tests

NATIVE COMPATIBILITY TESTING:
This test suite exercises Cohere-native HTTP routes exposed by Bifrost.
Tests use Cohere-shaped request and response payloads directly rather than the OpenAI-compatible transport.

Note: Tests automatically skip when Cohere credentials or required Cohere models are not configured.

Tests the current Cohere-native scenarios directly:
1. Simple chat and multi-turn chat
2. Tool calling and end-to-end tool execution
3. Image URL and base64 image inputs
4. Streaming chat
5. Embeddings and count tokens
6. Rerank and top_n limiting
7. Free-choice tools without tool_choice
8. Structured output with response_format.schema
9. strict_tools enforcement
10. Documents grounding
"""

import json

import pytest
import requests

from .utils.common import (
    CALCULATOR_TOOL,
    EMBEDDINGS_SINGLE_TEXT,
    IMAGE_BASE64_MESSAGES,
    IMAGE_URL_MESSAGES,
    INPUT_TOKENS_SIMPLE_TEXT,
    LOCATION_KEYWORDS,
    MULTIPLE_TOOL_CALL_MESSAGES,
    RERANK_DOCUMENTS,
    RERANK_EXPECTED_TOP_INDEX,
    RERANK_QUERY,
    SIMPLE_CHAT_MESSAGES,
    SINGLE_TOOL_CALL_MESSAGES,
    STREAMING_CHAT_MESSAGES,
    WEATHER_KEYWORDS,
    WEATHER_TOOL,
    assert_valid_rerank_response,
    get_api_key,
    mock_tool_response,
    skip_if_no_api_key,
)
from .utils.config_loader import get_integration_url, get_model


def build_cohere_headers() -> dict:
    return {
        "Authorization": f"Bearer {get_api_key('cohere')}",
        "Content-Type": "application/json",
    }


def require_cohere_model(capability: str) -> str:
    model = get_model("cohere", capability)
    if not model:
        pytest.skip(f"No Cohere model configured for capability: {capability}")
    return model


def extract_cohere_text(response_json: dict) -> str:
    message = response_json.get("message") or {}
    content = message.get("content")
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        text_parts = []
        for block in content:
            if isinstance(block, dict) and isinstance(block.get("text"), str):
                text_parts.append(block["text"])
        return "".join(text_parts)
    return ""


def extract_cohere_tool_calls(response_json: dict) -> list[dict]:
    tool_calls = []
    message = response_json.get("message") or {}
    raw_tool_calls = message.get("tool_calls") or []

    for tool_call in raw_tool_calls:
        function = tool_call.get("function") or {}
        arguments = function.get("arguments", "{}")
        if isinstance(arguments, str):
            try:
                arguments = json.loads(arguments)
            except json.JSONDecodeError:
                arguments = {}

        tool_calls.append(
            {
                "id": tool_call.get("id"),
                "name": function.get("name"),
                "arguments": arguments,
            }
        )

    return tool_calls


def iter_text_values(value):
    if isinstance(value, dict):
        for key, nested in value.items():
            if key == "text" and isinstance(nested, str):
                yield nested
            else:
                yield from iter_text_values(nested)
    elif isinstance(value, list):
        for item in value:
            yield from iter_text_values(item)


def collect_cohere_stream_text(response: requests.Response) -> tuple[str, set[str]]:
    text_parts = []
    event_types = set()

    for raw_line in response.iter_lines(decode_unicode=True):
        if not raw_line:
            continue
        line = raw_line.strip()
        if line.startswith("event:"):
            event_types.add(line.split(":", 1)[1].strip())
            continue
        if not line.startswith("data:"):
            continue

        data = line.split(":", 1)[1].strip()
        if data == "[DONE]":
            continue

        payload = json.loads(data)
        payload_type = payload.get("type")
        if isinstance(payload_type, str):
            event_types.add(payload_type)
        text_parts.extend(iter_text_values(payload))

    return "".join(text_parts), event_types


def assert_cohere_text_contains_any(
    response_json: dict, keywords: list[str], min_length: int = 1
):
    content = extract_cohere_text(response_json).lower()
    assert len(content) >= min_length
    assert any(keyword in content for keyword in keywords), (
        f"Expected one of {keywords} in Cohere response. Got: {content}"
    )


def extract_cohere_json(response_json: dict) -> dict:
    content = extract_cohere_text(response_json).strip()
    if content.startswith("```"):
        lines = content.splitlines()
        if len(lines) >= 3 and lines[0].startswith("```") and lines[-1] == "```":
            content = "\n".join(lines[1:-1]).strip()
        if content.lower().startswith("json"):
            content = content[4:].strip()
    return json.loads(content)


class TestCohereIntegration:
    """Test suite for Cohere-native compatibility routes."""

    @skip_if_no_api_key("cohere")
    def test_01_simple_chat(self):
        """Test Case 1: Simple chat via Cohere-native route."""
        model = require_cohere_model("chat")

        response = requests.post(
            f"{get_integration_url('cohere')}/v2/chat",
            headers=build_cohere_headers(),
            json={
                "model": model,
                "messages": SIMPLE_CHAT_MESSAGES,
                "max_tokens": 100,
            },
            timeout=30,
        )
        response.raise_for_status()

        response_json = response.json()
        assert isinstance(response_json.get("id"), str) and response_json["id"]
        assert "extra_fields" not in response_json
        assert len(extract_cohere_text(response_json)) > 0

    @skip_if_no_api_key("cohere")
    def test_02_multi_turn_conversation(self):
        """Test Case 2: Multi-turn conversation via Cohere-native route."""
        model = require_cohere_model("chat")

        response = requests.post(
            f"{get_integration_url('cohere')}/v2/chat",
            headers=build_cohere_headers(),
            json={
                "model": model,
                "messages": [
                    {"role": "user", "content": "What is the capital of France?"},
                    {"role": "assistant", "content": "The capital of France is Paris."},
                    {"role": "user", "content": "What is its population?"},
                ],
                "max_tokens": 150,
            },
            timeout=30,
        )
        response.raise_for_status()

        assert_cohere_text_contains_any(
            response.json(),
            ["population", "million", "people", "inhabitants"],
            min_length=10,
        )

    @skip_if_no_api_key("cohere")
    def test_03_tool_call(self):
        """Test Case 3: Tool calling via Cohere-native route."""
        model = require_cohere_model("tools")

        response = requests.post(
            f"{get_integration_url('cohere')}/v2/chat",
            headers=build_cohere_headers(),
            json={
                "model": model,
                "messages": SINGLE_TOOL_CALL_MESSAGES,
                "tools": [{"type": "function", "function": WEATHER_TOOL}],
                "tool_choice": "REQUIRED",
                "max_tokens": 200,
            },
            timeout=30,
        )
        response.raise_for_status()

        tool_calls = extract_cohere_tool_calls(response.json())
        assert len(tool_calls) == 1
        assert tool_calls[0]["name"] == WEATHER_TOOL["name"]
        assert "location" in tool_calls[0]["arguments"]

    @skip_if_no_api_key("cohere")
    def test_04_multiple_tool_calls(self):
        """Test Case 4: Multiple tool calls via Cohere-native route."""
        model = require_cohere_model("tools")

        response = requests.post(
            f"{get_integration_url('cohere')}/v2/chat",
            headers=build_cohere_headers(),
            json={
                "model": model,
                "messages": MULTIPLE_TOOL_CALL_MESSAGES,
                "tools": [
                    {"type": "function", "function": WEATHER_TOOL},
                    {"type": "function", "function": CALCULATOR_TOOL},
                ],
                "tool_choice": "REQUIRED",
                "max_tokens": 250,
            },
            timeout=30,
        )
        response.raise_for_status()

        tool_calls = extract_cohere_tool_calls(response.json())
        assert len(tool_calls) == 2
        tool_names = [tool_call["name"] for tool_call in tool_calls]
        assert "get_weather" in tool_names
        assert "calculate" in tool_names

    @skip_if_no_api_key("cohere")
    def test_05_end2end_tool_calling(self):
        """Test Case 5: End-to-end tool calling via Cohere-native route."""
        model = require_cohere_model("tools")
        messages = [{"role": "user", "content": "What's the weather in Boston in fahrenheit?"}]

        first_response = requests.post(
            f"{get_integration_url('cohere')}/v2/chat",
            headers=build_cohere_headers(),
            json={
                "model": model,
                "messages": messages,
                "tools": [{"type": "function", "function": WEATHER_TOOL}],
                "tool_choice": "REQUIRED",
                "max_tokens": 200,
            },
            timeout=30,
        )
        first_response.raise_for_status()

        first_response_json = first_response.json()
        tool_calls = extract_cohere_tool_calls(first_response_json)
        assert len(tool_calls) == 1

        tool_response = mock_tool_response(tool_calls[0]["name"], tool_calls[0]["arguments"])
        messages.append(first_response_json["message"])
        messages.append(
            {
                "role": "tool",
                "tool_call_id": tool_calls[0]["id"],
                "content": tool_response,
            }
        )

        final_response = requests.post(
            f"{get_integration_url('cohere')}/v2/chat",
            headers=build_cohere_headers(),
            json={
                "model": model,
                "messages": messages,
                "max_tokens": 200,
            },
            timeout=30,
        )
        final_response.raise_for_status()

        assert_cohere_text_contains_any(
            final_response.json(),
            WEATHER_KEYWORDS + LOCATION_KEYWORDS,
            min_length=10,
        )

    @skip_if_no_api_key("cohere")
    def test_06_image_url(self):
        """Test Case 6: Image analysis from URL via Cohere-native route."""
        model = require_cohere_model("vision")

        response = requests.post(
            f"{get_integration_url('cohere')}/v2/chat",
            headers=build_cohere_headers(),
            json={
                "model": model,
                "messages": IMAGE_URL_MESSAGES,
                "max_tokens": 200,
            },
            timeout=30,
        )
        response.raise_for_status()

        assert_cohere_text_contains_any(
            response.json(),
            ["image", "photo", "picture", "scene", "boardwalk", "nature"],
            min_length=10,
        )

    @skip_if_no_api_key("cohere")
    def test_07_image_base64(self):
        """Test Case 7: Image analysis from base64 via Cohere-native route."""
        model = require_cohere_model("vision")

        response = requests.post(
            f"{get_integration_url('cohere')}/v2/chat",
            headers=build_cohere_headers(),
            json={
                "model": model,
                "messages": IMAGE_BASE64_MESSAGES,
                "max_tokens": 200,
            },
            timeout=30,
        )
        response.raise_for_status()

        assert_cohere_text_contains_any(
            response.json(),
            ["image", "picture", "red", "square", "shape"],
            min_length=10,
        )

    @skip_if_no_api_key("cohere")
    def test_09_streaming_chat(self):
        """Test Case 9: Streaming chat via Cohere-native route."""
        model = require_cohere_model("streaming")

        response = requests.post(
            f"{get_integration_url('cohere')}/v2/chat",
            headers=build_cohere_headers(),
            json={
                "model": model,
                "messages": STREAMING_CHAT_MESSAGES,
                "max_tokens": 100,
                "stream": True,
            },
            stream=True,
            timeout=30,
        )
        response.raise_for_status()

        content, event_types = collect_cohere_stream_text(response)
        assert len(content) > 0
        assert any(
            event_type in event_types
            for event_type in ("message-start", "content-start", "content-delta", "message-end")
        )

    @skip_if_no_api_key("cohere")
    def test_10_embeddings(self):
        """Test Case 10: Embeddings via Cohere-native route."""
        model = require_cohere_model("embeddings")

        response = requests.post(
            f"{get_integration_url('cohere')}/v2/embed",
            headers=build_cohere_headers(),
            json={
                "model": model,
                "input_type": "search_document",
                "texts": [EMBEDDINGS_SINGLE_TEXT],
                "embedding_types": ["float"],
            },
            timeout=30,
        )
        response.raise_for_status()

        response_json = response.json()
        assert isinstance(response_json.get("id"), str) and response_json["id"]
        assert "extra_fields" not in response_json
        assert "data" not in response_json
        embeddings = ((response_json.get("embeddings") or {}).get("float")) or []
        assert len(embeddings) == 1
        assert isinstance(embeddings[0], list) and len(embeddings[0]) > 0

    @skip_if_no_api_key("cohere")
    def test_11_count_tokens(self):
        """Test Case 11: Tokenize via Cohere-native route."""
        model = require_cohere_model("count_tokens")

        response = requests.post(
            f"{get_integration_url('cohere')}/v1/tokenize",
            headers=build_cohere_headers(),
            json={
                "model": model,
                "text": INPUT_TOKENS_SIMPLE_TEXT,
            },
            timeout=30,
        )
        response.raise_for_status()

        response_json = response.json()
        assert "extra_fields" not in response_json
        assert "input_tokens" not in response_json
        assert isinstance(response_json.get("tokens"), list) and len(response_json["tokens"]) > 0

    @skip_if_no_api_key("cohere")
    def test_12_rerank_basic(self):
        """Test Case 12: Basic rerank through Cohere-native route."""
        model = require_cohere_model("rerank")

        response = requests.post(
            f"{get_integration_url('cohere')}/v2/rerank",
            headers=build_cohere_headers(),
            json={
                "model": model,
                "query": RERANK_QUERY,
                "documents": RERANK_DOCUMENTS,
                "top_n": 2,
            },
            timeout=30,
        )
        response.raise_for_status()

        assert_valid_rerank_response(
            response.json(),
            "cohere",
            expected_top_index=RERANK_EXPECTED_TOP_INDEX,
            expected_count=2,
        )

    @skip_if_no_api_key("cohere")
    def test_13_rerank_top_n_limit(self):
        """Test Case 13: Cohere rerank respects top_n."""
        model = require_cohere_model("rerank")

        response = requests.post(
            f"{get_integration_url('cohere')}/v2/rerank",
            headers=build_cohere_headers(),
            json={
                "model": model,
                "query": RERANK_QUERY,
                "documents": RERANK_DOCUMENTS,
                "top_n": 1,
            },
            timeout=30,
        )
        response.raise_for_status()

        assert_valid_rerank_response(
            response.json(),
            "cohere",
            expected_top_index=RERANK_EXPECTED_TOP_INDEX,
            expected_count=1,
        )

    @skip_if_no_api_key("cohere")
    def test_14_tools_without_tool_choice(self):
        """Test Case 14: Tools without tool_choice should succeed via Cohere-native route."""
        model = require_cohere_model("tools")

        response = requests.post(
            f"{get_integration_url('cohere')}/v2/chat",
            headers=build_cohere_headers(),
            json={
                "model": model,
                "messages": SINGLE_TOOL_CALL_MESSAGES,
                "tools": [{"type": "function", "function": WEATHER_TOOL}],
                "max_tokens": 200,
            },
            timeout=30,
        )
        response.raise_for_status()

        response_json = response.json()
        tool_calls = extract_cohere_tool_calls(response_json)
        text_content = extract_cohere_text(response_json)
        assert tool_calls or len(text_content) > 0

    @skip_if_no_api_key("cohere")
    def test_15_structured_output_schema(self):
        """Test Case 15: Structured output uses native Cohere schema shape."""
        model = require_cohere_model("chat")

        response = requests.post(
            f"{get_integration_url('cohere')}/v2/chat",
            headers=build_cohere_headers(),
            json={
                "model": model,
                "messages": [
                    {
                        "role": "user",
                        "content": (
                            "Return JSON only. Set city to Paris and country to France."
                        ),
                    }
                ],
                "response_format": {
                    "type": "json_object",
                    "schema": {
                        "type": "object",
                        "properties": {
                            "city": {"type": "string"},
                            "country": {"type": "string"},
                        },
                        "required": ["city", "country"],
                        "additionalProperties": False,
                    },
                },
                "temperature": 0,
                "max_tokens": 100,
            },
            timeout=30,
        )
        response.raise_for_status()

        response_json = response.json()
        parsed = extract_cohere_json(response_json)
        assert set(parsed.keys()) == {"city", "country"}
        assert parsed["city"].lower() == "paris"
        assert parsed["country"].lower() == "france"

    @skip_if_no_api_key("cohere")
    def test_16_strict_tools(self):
        """Test Case 16: strict_tools should produce schema-conformant tool calls."""
        model = require_cohere_model("tools")

        strict_weather_tool = {
            "name": "get_weather",
            "description": "Get the current weather for a city.",
            "parameters": {
                "type": "object",
                "properties": {
                    "location": {
                        "type": "string",
                        "description": "City and region, e.g. Boston, MA",
                    }
                },
                "required": ["location"],
                "additionalProperties": False,
            },
        }

        response = requests.post(
            f"{get_integration_url('cohere')}/v2/chat",
            headers=build_cohere_headers(),
            json={
                "model": model,
                "messages": [
                    {
                        "role": "user",
                        "content": "Use the weather tool for Boston, MA.",
                    }
                ],
                "tools": [{"type": "function", "function": strict_weather_tool}],
                "strict_tools": True,
                "tool_choice": "REQUIRED",
                "temperature": 0,
                "max_tokens": 150,
            },
            timeout=30,
        )
        response.raise_for_status()

        tool_calls = extract_cohere_tool_calls(response.json())
        assert len(tool_calls) == 1
        assert tool_calls[0]["name"] == strict_weather_tool["name"]
        assert set(tool_calls[0]["arguments"].keys()) == {"location"}
        assert isinstance(tool_calls[0]["arguments"]["location"], str)
        assert "boston" in tool_calls[0]["arguments"]["location"].lower()

    @skip_if_no_api_key("cohere")
    def test_17_documents_grounding(self):
        """Test Case 17: Native Cohere documents should ground the reply."""
        model = require_cohere_model("chat")

        response = requests.post(
            f"{get_integration_url('cohere')}/v2/chat",
            headers=build_cohere_headers(),
            json={
                "model": model,
                "messages": [
                    {
                        "role": "user",
                        "content": "What is the launch codename? Answer with the codename only.",
                    }
                ],
                "documents": [
                    {
                        "id": "launch-doc",
                        "data": {
                            "title": "Launch Brief",
                            "snippet": "The internal launch codename is Atlas.",
                        },
                    },
                    {
                        "id": "support-doc",
                        "data": {
                            "title": "Support Note",
                            "snippet": "Atlas is the codename referenced in launch planning documents.",
                        },
                    },
                ],
                "temperature": 0,
                "max_tokens": 50,
            },
            timeout=30,
        )
        response.raise_for_status()

        assert_cohere_text_contains_any(response.json(), ["atlas"], min_length=3)
