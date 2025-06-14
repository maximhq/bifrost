"""
Common utilities and test data for all integration tests.
This module contains shared functions, test data, and assertions
that can be used across all integration-specific test files.
"""

import base64
import json
import os
from typing import Dict, List, Any, Optional
from dataclasses import dataclass

# Import model configurations
from .config_loader import get_model, get_model_capabilities, supports_capability
from .models import get_alternatives, INTEGRATION_MODELS


# Test Configuration
@dataclass
class Config:
    """Configuration for test execution"""

    timeout: int = 30
    max_retries: int = 3
    debug: bool = False


# Common Test Data
SIMPLE_CHAT_MESSAGES = [{"role": "user", "content": "Hello! How are you today?"}]

MULTI_TURN_MESSAGES = [
    {"role": "user", "content": "What's the capital of France?"},
    {"role": "assistant", "content": "The capital of France is Paris."},
    {"role": "user", "content": "What's the population of that city?"},
]

# Tool Definitions
WEATHER_TOOL = {
    "name": "get_weather",
    "description": "Get the current weather for a location",
    "parameters": {
        "type": "object",
        "properties": {
            "location": {
                "type": "string",
                "description": "The city and state, e.g. San Francisco, CA",
            },
            "unit": {
                "type": "string",
                "enum": ["celsius", "fahrenheit"],
                "description": "The temperature unit",
            },
        },
        "required": ["location"],
    },
}

CALCULATOR_TOOL = {
    "name": "calculate",
    "description": "Perform basic mathematical calculations",
    "parameters": {
        "type": "object",
        "properties": {
            "expression": {
                "type": "string",
                "description": "Mathematical expression to evaluate, e.g. '2 + 2'",
            }
        },
        "required": ["expression"],
    },
}

SEARCH_TOOL = {
    "name": "search_web",
    "description": "Search the web for information",
    "parameters": {
        "type": "object",
        "properties": {"query": {"type": "string", "description": "Search query"}},
        "required": ["query"],
    },
}

ALL_TOOLS = [WEATHER_TOOL, CALCULATOR_TOOL, SEARCH_TOOL]

# Tool Call Test Messages
SINGLE_TOOL_CALL_MESSAGES = [
    {"role": "user", "content": "What's the weather like in San Francisco?"}
]

MULTIPLE_TOOL_CALL_MESSAGES = [
    {"role": "user", "content": "What's the weather in New York and calculate 15 * 23?"}
]

# Image Test Data
IMAGE_URL = "https://upload.wikimedia.org/wikipedia/commons/thumb/d/dd/Gfp-wisconsin-madison-the-nature-boardwalk.jpg/2560px-Gfp-wisconsin-madison-the-nature-boardwalk.jpg"

# Small test image as base64 (1x1 pixel red PNG)
BASE64_IMAGE = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/5+hHgAHggJ/PchI7wAAAABJRU5ErkJggg=="

IMAGE_URL_MESSAGES = [
    {
        "role": "user",
        "content": [
            {"type": "text", "text": "What do you see in this image?"},
            {"type": "image_url", "image_url": {"url": IMAGE_URL}},
        ],
    }
]

IMAGE_BASE64_MESSAGES = [
    {
        "role": "user",
        "content": [
            {"type": "text", "text": "Describe this image"},
            {
                "type": "image_url",
                "image_url": {"url": f"data:image/png;base64,{BASE64_IMAGE}"},
            },
        ],
    }
]

MULTIPLE_IMAGES_MESSAGES = [
    {
        "role": "user",
        "content": [
            {"type": "text", "text": "Compare these two images"},
            {"type": "image_url", "image_url": {"url": IMAGE_URL}},
            {
                "type": "image_url",
                "image_url": {"url": f"data:image/png;base64,{BASE64_IMAGE}"},
            },
        ],
    }
]

# Complex End-to-End Test Data
COMPLEX_E2E_MESSAGES = [
    {"role": "user", "content": "Hello! I need help with some tasks."},
    {
        "role": "assistant",
        "content": "Hello! I'd be happy to help you with your tasks. What do you need assistance with?",
    },
    {
        "role": "user",
        "content": [
            {
                "type": "text",
                "text": "First, can you tell me what's in this image and then get the weather for the location shown?",
            },
            {"type": "image_url", "image_url": {"url": IMAGE_URL}},
        ],
    },
]


# Helper Functions
def mock_tool_response(tool_name: str, args: Dict[str, Any]) -> str:
    """Generate mock responses for tool calls"""
    if tool_name == "get_weather":
        location = args.get("location", "Unknown")
        unit = args.get("unit", "fahrenheit")
        return f"The weather in {location} is 72°{'F' if unit == 'fahrenheit' else 'C'} and sunny."

    elif tool_name == "calculate":
        expression = args.get("expression", "")
        try:
            # Simple evaluation for basic math
            result = eval(expression.replace("x", "*").replace("×", "*"))
            return f"The result of {expression} is {result}"
        except:
            return f"Could not calculate {expression}"

    elif tool_name == "search_web":
        query = args.get("query", "")
        return f"Here are the search results for '{query}': [Mock search results]"

    return f"Tool {tool_name} executed with args: {args}"


def validate_response_structure(response: Any, expected_fields: List[str]) -> bool:
    """Validate that a response has the expected structure"""
    if not hasattr(response, "__dict__") and not isinstance(response, dict):
        return False

    response_dict = response.__dict__ if hasattr(response, "__dict__") else response

    for field in expected_fields:
        if field not in response_dict:
            return False

    return True


def extract_tool_calls(response: Any) -> List[Dict[str, Any]]:
    """Extract tool calls from various response formats"""
    tool_calls = []

    # Handle OpenAI format: response.choices[0].message.tool_calls
    if hasattr(response, "choices") and len(response.choices) > 0:
        choice = response.choices[0]
        if (
            hasattr(choice, "message")
            and hasattr(choice.message, "tool_calls")
            and choice.message.tool_calls
        ):
            for tool_call in choice.message.tool_calls:
                if hasattr(tool_call, "function"):
                    tool_calls.append(
                        {
                            "name": tool_call.function.name,
                            "arguments": (
                                json.loads(tool_call.function.arguments)
                                if isinstance(tool_call.function.arguments, str)
                                else tool_call.function.arguments
                            ),
                        }
                    )

    # Handle direct tool_calls attribute (other formats)
    elif hasattr(response, "tool_calls") and response.tool_calls:
        for tool_call in response.tool_calls:
            if hasattr(tool_call, "function"):
                tool_calls.append(
                    {
                        "name": tool_call.function.name,
                        "arguments": (
                            json.loads(tool_call.function.arguments)
                            if isinstance(tool_call.function.arguments, str)
                            else tool_call.function.arguments
                        ),
                    }
                )

    # Handle Anthropic format: response.content with tool_use blocks
    elif hasattr(response, "content") and isinstance(response.content, list):
        for content in response.content:
            if hasattr(content, "type") and content.type == "tool_use":
                tool_calls.append({"name": content.name, "arguments": content.input})

    return tool_calls


def assert_valid_chat_response(response: Any, min_length: int = 1):
    """Assert that a chat response is valid"""
    assert response is not None, "Response should not be None"

    # Extract content from various response formats
    content = ""
    if hasattr(response, "content"):
        if isinstance(response.content, str):
            content = response.content
        elif isinstance(response.content, list) and len(response.content) > 0:
            # Handle list content (like Anthropic)
            text_content = [
                c for c in response.content if hasattr(c, "type") and c.type == "text"
            ]
            if text_content:
                content = text_content[0].text
    elif hasattr(response, "choices") and len(response.choices) > 0:
        # Handle OpenAI format
        choice = response.choices[0]
        if hasattr(choice, "message") and hasattr(choice.message, "content"):
            content = choice.message.content or ""

    assert (
        len(content) >= min_length
    ), f"Response content should be at least {min_length} characters, got: {content}"


def assert_has_tool_calls(response: Any, expected_count: Optional[int] = None):
    """Assert that a response contains tool calls"""
    tool_calls = extract_tool_calls(response)

    assert len(tool_calls) > 0, "Response should contain tool calls"

    if expected_count is not None:
        assert (
            len(tool_calls) == expected_count
        ), f"Expected {expected_count} tool calls, got {len(tool_calls)}"

    # Validate tool call structure
    for tool_call in tool_calls:
        assert "name" in tool_call, "Tool call should have a name"
        assert "arguments" in tool_call, "Tool call should have arguments"


def assert_valid_image_response(response: Any):
    """Assert that an image analysis response is valid"""
    assert_valid_chat_response(response, min_length=10)

    # Extract content for image-specific validation
    content = ""
    if hasattr(response, "content"):
        if isinstance(response.content, str):
            content = response.content.lower()
        elif isinstance(response.content, list):
            text_content = [
                c for c in response.content if hasattr(c, "type") and c.type == "text"
            ]
            if text_content:
                content = text_content[0].text.lower()
    elif hasattr(response, "choices") and len(response.choices) > 0:
        choice = response.choices[0]
        if hasattr(choice, "message") and hasattr(choice.message, "content"):
            content = (choice.message.content or "").lower()

    # Check for image-related keywords
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
    ]
    has_image_reference = any(keyword in content for keyword in image_keywords)

    assert (
        has_image_reference
    ), f"Response should reference the image content. Got: {content}"


# Common keyword arrays for flexible assertions
COMPARISON_KEYWORDS = [
    "compare",
    "comparison",
    "different",
    "difference",
    "differences",
    "both",
    "two",
    "first",
    "second",
    "images",
    "image",
    "versus",
    "vs",
    "contrast",
    "unlike",
    "while",
    "whereas",
]

WEATHER_KEYWORDS = [
    "weather",
    "temperature",
    "sunny",
    "cloudy",
    "rain",
    "snow",
    "celsius",
    "fahrenheit",
    "degrees",
    "hot",
    "cold",
    "warm",
    "cool",
]

LOCATION_KEYWORDS = ["boston", "san francisco", "new york", "city", "location", "place"]


# Test Categories
class TestCategories:
    """Constants for test categories"""

    SIMPLE_CHAT = "simple_chat"
    MULTI_TURN = "multi_turn"
    SINGLE_TOOL = "single_tool"
    MULTIPLE_TOOLS = "multiple_tools"
    E2E_TOOLS = "e2e_tools"
    AUTO_FUNCTION = "auto_function"
    IMAGE_URL = "image_url"
    IMAGE_BASE64 = "image_base64"
    MULTIPLE_IMAGES = "multiple_images"
    COMPLEX_E2E = "complex_e2e"
    INTEGRATION_SPECIFIC = "integration_specific"


# Environment helpers
def get_api_key(integration: str) -> str:
    """Get API key for a integration from environment variables"""
    key_map = {
        "openai": "OPENAI_API_KEY",
        "anthropic": "ANTHROPIC_API_KEY",
        "google": "GOOGLE_API_KEY",
        "litellm": "LITELLM_API_KEY",
    }

    env_var = key_map.get(integration.lower())
    if not env_var:
        raise ValueError(f"Unknown integration: {integration}")

    api_key = os.getenv(env_var)
    if not api_key:
        raise ValueError(f"Missing environment variable: {env_var}")

    return api_key


def skip_if_no_api_key(integration: str):
    """Decorator to skip tests if API key is not available"""
    import pytest

    def decorator(func):
        try:
            get_api_key(integration)
            return func
        except ValueError:
            return pytest.mark.skip(f"No API key available for {integration}")(func)

    return decorator
