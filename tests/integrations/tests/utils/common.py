"""
Common utilities and test data for all integration tests.
This module contains shared functions, test data, and assertions
that can be used across all integration-specific test files.
"""

import ast
import base64
import json
import operator
import os
from typing import Dict, List, Any, Optional
from dataclasses import dataclass


# Test Configuration
@dataclass
class Config:
    """Configuration for test execution"""

    timeout: int = 30
    max_retries: int = 3
    debug: bool = False

# Image Test Data
IMAGE_URL = "https://upload.wikimedia.org/wikipedia/commons/thumb/d/dd/Gfp-wisconsin-madison-the-nature-boardwalk.jpg/2560px-Gfp-wisconsin-madison-the-nature-boardwalk.jpg"
IMAGE_URL_SECONDARY = "https://goo.gle/instrument-img"

# Small test image as base64 (1x1 pixel red PNG)
BASE64_IMAGE = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/5+hHgAHggJ/PchI7wAAAABJRU5ErkJggg=="

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

# Embeddings Test Data
EMBEDDINGS_SINGLE_TEXT = "The quick brown fox jumps over the lazy dog."

EMBEDDINGS_MULTIPLE_TEXTS = [
    "Artificial intelligence is transforming our world.",
    "Machine learning algorithms learn from data to make predictions.",
    "Natural language processing helps computers understand human language.",
    "Computer vision enables machines to interpret and analyze visual information.",
    "Robotics combines AI with mechanical engineering to create autonomous systems.",
]

EMBEDDINGS_SIMILAR_TEXTS = [
    "The weather is sunny and warm today.",
    "Today has bright sunshine and pleasant temperatures.",
    "It's a beautiful day with clear skies and warmth.",
]

EMBEDDINGS_DIFFERENT_TEXTS = [
    "The weather is sunny and warm today.",
    "Python is a popular programming language.",
    "The stock market closed higher yesterday.",
    "Machine learning requires large datasets.",
]

EMBEDDINGS_EMPTY_TEXTS = ["", "   ", "\n\t", ""]

EMBEDDINGS_LONG_TEXT = """
This is a longer text sample designed to test how embedding models handle 
larger inputs. It contains multiple sentences with various topics including 
technology, science, literature, and general knowledge. The purpose is to 
ensure that the embedding generation works correctly with substantial text 
inputs that might be closer to real-world usage scenarios where users 
embed entire paragraphs or documents rather than just short phrases.
""".strip()

# Tool Call Test Messages
SINGLE_TOOL_CALL_MESSAGES = [
    {"role": "user", "content": "What's the weather like in San Francisco in fahrenheit?"}
]

MULTIPLE_TOOL_CALL_MESSAGES = [
    {"role": "user", "content": "What's the weather in New York and calculate 15 * 23?"}
]

# Streaming Test Messages
STREAMING_CHAT_MESSAGES = [
    {
        "role": "user",
        "content": "Tell me a short story about a robot learning to paint. Keep it under 200 words.",
    }
]

STREAMING_TOOL_CALL_MESSAGES = [
    {
        "role": "user",
        "content": "What's the weather like in San Francisco? Please use the get_weather function.",
    }
]

# Responses API Test Data
RESPONSES_SIMPLE_TEXT_INPUT = [
    {"role": "user", "content": "Tell me a fun fact about space exploration."}
]

RESPONSES_TEXT_WITH_SYSTEM = [
    {"role": "system", "content": "You are a helpful astronomy expert."},
    {"role": "user", "content": "What's the most interesting discovery about Mars?"},
]

RESPONSES_IMAGE_INPUT = [
    {
        "role": "user",
        "content": [
            {"type": "input_text", "text": "What's in this image?"},
            {
                "type": "input_image",
                "image_url": f"data:image/png;base64,{BASE64_IMAGE}",
            },
        ],
    }
]

RESPONSES_TOOL_CALL_INPUT = [
    {
        "role": "user",
        "content": "What's the weather in Boston? Use the get_weather function.",
    }
]

RESPONSES_STREAMING_INPUT = [
    {
        "role": "user",
        "content": "Write a short poem about artificial intelligence in 4 lines.",
    }
]

RESPONSES_REASONING_INPUT = [
    {
        "role": "user",
        "content": (
            "A train leaves Station A at 2:00 PM traveling at 60 mph toward Station B. "
            "Another train leaves Station B at 3:00 PM traveling at 80 mph toward Station A. "
            "The stations are 420 miles apart. At what time will the trains meet? "
            "Show your step-by-step reasoning."
        ),
    }
]

# Text Completions Test Data
TEXT_COMPLETION_SIMPLE_PROMPT = "Once upon a time in a distant galaxy"

TEXT_COMPLETION_STREAMING_PROMPT = "Write a haiku about technology:"

# Anthropic Thinking/Reasoning Test Data
ANTHROPIC_THINKING_PROMPT = [
    {
        "role": "user",
        "content": (
            "A bakery makes 120 cookies per batch. Each batch takes 25 minutes to bake. "
            "If they have 4 ovens and need to make 960 cookies for an order, "
            "how long will it take to complete the order? Think through this step by step."
        ),
    }
]

ANTHROPIC_THINKING_STREAMING_PROMPT = [
    {
        "role": "user",
        "content": (
            "Three friends split a restaurant bill. Alice paid $45, Bob paid $30, and Carol paid $25. "
            "They want to split it equally. Who owes money to whom and how much? "
            "Show your reasoning."
        ),
    }
]

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

# Error test data for invalid role testing
INVALID_ROLE_MESSAGES = [
    {"role": "tester", "content": "Hello! This should fail due to invalid role."}
]

# GenAI-specific invalid role content that passes SDK validation but fails at Bifrost
GENAI_INVALID_ROLE_CONTENT = [
    {
        "role": "tester",  # Invalid role that should be caught by Bifrost
        "parts": [
            {"text": "Hello! This should fail due to invalid role in GenAI format."}
        ],
    }
]

# Error keywords for validating error messages
ERROR_KEYWORDS = [
    "invalid",
    "error",
    "role",
    "tester",
    "unsupported",
    "unknown",
    "bad",
    "incorrect",
    "not allowed",
    "not supported",
    "forbidden",
]


# Helper Functions
def safe_eval_arithmetic(expression: str) -> float:
    """
    Safely evaluate arithmetic expressions using AST parsing.
    Only allows basic arithmetic operations: +, -, *, /, **, (), and numbers.

    Args:
        expression: String containing arithmetic expression

    Returns:
        Evaluated result as float

    Raises:
        ValueError: If expression contains unsupported operations
        SyntaxError: If expression has invalid syntax
        ZeroDivisionError: If division by zero occurs
    """
    # Allowed operations mapping
    ALLOWED_OPS = {
        ast.Add: operator.add,
        ast.Sub: operator.sub,
        ast.Mult: operator.mul,
        ast.Div: operator.truediv,
        ast.Pow: operator.pow,
        ast.USub: operator.neg,
        ast.UAdd: operator.pos,
    }

    def eval_node(node):
        """Recursively evaluate AST nodes"""
        if isinstance(node, ast.Constant):  # Numbers
            return node.value
        elif isinstance(node, ast.Num):  # Numbers (Python < 3.8 compatibility)
            return node.n
        elif isinstance(node, ast.UnaryOp):
            if type(node.op) in ALLOWED_OPS:
                return ALLOWED_OPS[type(node.op)](eval_node(node.operand))
            else:
                raise ValueError(
                    f"Unsupported unary operation: {type(node.op).__name__}"
                )
        elif isinstance(node, ast.BinOp):
            if type(node.op) in ALLOWED_OPS:
                left = eval_node(node.left)
                right = eval_node(node.right)
                return ALLOWED_OPS[type(node.op)](left, right)
            else:
                raise ValueError(
                    f"Unsupported binary operation: {type(node.op).__name__}"
                )
        else:
            raise ValueError(f"Unsupported expression type: {type(node).__name__}")

    try:
        # Parse the expression into an AST
        tree = ast.parse(expression, mode="eval")
        # Evaluate the AST
        return eval_node(tree.body)
    except SyntaxError as e:
        raise SyntaxError(f"Invalid syntax in expression '{expression}': {e}")
    except ZeroDivisionError:
        raise ZeroDivisionError(f"Division by zero in expression '{expression}'")
    except Exception as e:
        raise ValueError(f"Error evaluating expression '{expression}': {e}")


def mock_tool_response(tool_name: str, args: Dict[str, Any]) -> str:
    """Generate mock responses for tool calls"""
    if tool_name == "get_weather":
        location = args.get("location", "Unknown")
        unit = args.get("unit", "fahrenheit")
        return f"The weather in {location} is 72°{'F' if unit == 'fahrenheit' else 'C'} and sunny."

    elif tool_name == "calculate":
        expression = args.get("expression", "")
        try:
            # Clean the expression and safely evaluate it
            cleaned_expression = expression.replace("x", "*").replace("×", "*")
            result = safe_eval_arithmetic(cleaned_expression)
            return f"The result of {expression} is {result}"
        except (ValueError, SyntaxError, ZeroDivisionError) as e:
            return f"Could not calculate {expression}: {e}"

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
    if hasattr(response, "text"):  # Google GenAI
        content = response.text
    elif hasattr(response, "content"):  # Anthropic
        if isinstance(response.content, str):
            content = response.content
        elif isinstance(response.content, list) and len(response.content) > 0:
            # Handle list content (like Anthropic)
            text_content = [
                c for c in response.content if hasattr(c, "type") and c.type == "text"
            ]
            if text_content:
                content = text_content[0].text
    elif hasattr(response, "choices") and len(response.choices) > 0:  # OpenAI
        # Handle OpenAI format
        choice = response.choices[0]
        if hasattr(choice, "message") and hasattr(choice.message, "content"):
            content = get_content_string(choice.message.content)

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
    if hasattr(response, "text"):  # Google GenAI
        content = response.text.lower()
    elif hasattr(response, "content"):  # Anthropic
        if isinstance(response.content, str):
            content = response.content.lower()
        elif isinstance(response.content, list):
            text_content = [
                c for c in response.content if hasattr(c, "type") and c.type == "text"
            ]
            if text_content:
                content = text_content[0].text.lower()
    elif hasattr(response, "choices") and len(response.choices) > 0:  # OpenAI
        choice = response.choices[0]
        if hasattr(choice, "message") and hasattr(choice.message, "content"):
            # could be array of content blocks or string
            if choice.message.content:
                if isinstance(choice.message.content, list):
                    # content is object with text field, get text from all objects
                    # Handle both dict and object formats
                    content = " ".join([
                        c.get("text", "") if isinstance(c, dict) else (c.text if hasattr(c, "text") else "")
                        for c in choice.message.content
                    ])
                else:
                    content = choice.message.content.lower()
            else:
                content = ""

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


def assert_valid_error_response(
    response_or_exception: Any, expected_invalid_role: str = "tester"
):
    """
    Assert that an error response or exception properly indicates an invalid role error.

    Args:
        response_or_exception: Either an HTTP error response or a raised exception
        expected_invalid_role: The invalid role that should be mentioned in the error
    """
    error_message = ""
    error_type = ""
    status_code = None

    # Handle different error response formats
    if hasattr(response_or_exception, "response"):
        # This is likely a requests.HTTPError or similar
        try:
            error_data = response_or_exception.response.json()
            status_code = response_or_exception.response.status_code

            # Extract error message from various formats
            if isinstance(error_data, dict):
                if "error" in error_data:
                    if isinstance(error_data["error"], dict):
                        error_message = error_data["error"].get(
                            "message", str(error_data["error"])
                        )
                        error_type = error_data["error"].get("type", "")
                    else:
                        error_message = str(error_data["error"])
                else:
                    error_message = error_data.get("message", str(error_data))
            else:
                error_message = str(error_data)
        except:
            error_message = str(response_or_exception)

    elif hasattr(response_or_exception, "message"):
        # Direct error object
        error_message = response_or_exception.message

    elif hasattr(response_or_exception, "args") and response_or_exception.args:
        # Exception with args
        error_message = str(response_or_exception.args[0])

    else:
        # Fallback to string representation
        error_message = str(response_or_exception)

    # Convert to lowercase for case-insensitive matching
    error_message_lower = error_message.lower()
    error_type_lower = error_type.lower()

    # Validate that error message indicates role-related issue
    role_error_indicators = [
        expected_invalid_role.lower(),
        "role",
        "invalid",
        "unsupported",
        "unknown",
        "not allowed",
        "not supported",
        "bad request",
        "invalid_request",
    ]

    has_role_error = any(
        indicator in error_message_lower or indicator in error_type_lower
        for indicator in role_error_indicators
    )

    assert has_role_error, (
        f"Error message should indicate invalid role '{expected_invalid_role}'. "
        f"Got error message: '{error_message}', error type: '{error_type}'"
    )

    # Validate status code if available (should be 4xx for client errors)
    if status_code is not None:
        assert (
            400 <= status_code < 500
        ), f"Expected 4xx status code for invalid role error, got {status_code}"

    return True


def assert_error_propagation(error_response: Any, integration: str):
    """
    Assert that error is properly propagated through Bifrost to the integration.

    Args:
        error_response: The error response from the integration
        integration: The integration name (openai, anthropic, etc.)
    """
    # Check that we got an error response (not a success)
    assert error_response is not None, "Should have received an error response"

    # Integration-specific error format validation
    if integration.lower() == "openai":
        # OpenAI format: should have top-level 'type', 'event_id' and 'error' field with nested structure
        if hasattr(error_response, "response"):
            error_data = error_response.response.json()
            assert "error" in error_data, "OpenAI error should have 'error' field"

            # Check nested error structure
            error_obj = error_data["error"]
            assert (
                "message" in error_obj
            ), "OpenAI error.error should have 'message' field"
            assert "type" in error_obj, "OpenAI error.error should have 'type' field"
            assert "code" in error_obj, "OpenAI error.error should have 'code' field"

    elif integration.lower() == "anthropic":
        # Anthropic format: should have 'type' and 'error' with 'type' and 'message'
        if hasattr(error_response, "response"):
            error_data = error_response.response.json()
            assert "type" in error_data, "Anthropic error should have 'type' field"
            # Type field can be empty string if not set in original error
            assert isinstance(
                error_data["type"], str
            ), "Anthropic error type should be a string"
            assert "error" in error_data, "Anthropic error should have 'error' field"
            assert (
                "type" in error_data["error"]
            ), "Anthropic error.error should have 'type' field"
            assert (
                "message" in error_data["error"]
            ), "Anthropic error.error should have 'message' field"

    elif integration.lower() in ["google", "gemini", "genai"]:
        # Gemini format: follows Google API design guidelines with error.code, error.message, error.status
        if hasattr(error_response, "response"):
            error_data = error_response.response.json()
            assert "error" in error_data, "Gemini error should have 'error' field"

            # Check Google API standard error structure
            error_obj = error_data["error"]
            assert (
                "code" in error_obj
            ), "Gemini error.error should have 'code' field (HTTP status code)"
            assert isinstance(
                error_obj["code"], int
            ), "Gemini error.error.code should be an integer"
            assert (
                "message" in error_obj
            ), "Gemini error.error should have 'message' field"
            assert isinstance(
                error_obj["message"], str
            ), "Gemini error.error.message should be a string"
            assert (
                "status" in error_obj
            ), "Gemini error.error should have 'status' field"
            assert isinstance(
                error_obj["status"], str
            ), "Gemini error.error.status should be a string"

    return True


def assert_valid_streaming_response(
    chunk: Any, integration: str, is_final: bool = False
):
    """
    Assert that a streaming response chunk is valid for the given integration.

    Args:
        chunk: Individual streaming response chunk
        integration: The integration name (openai, anthropic, etc.)
        is_final: Whether this is expected to be the final chunk
    """
    assert chunk is not None, "Streaming chunk should not be None"

    if integration.lower() == "openai":
        # OpenAI streaming format
        assert hasattr(chunk, "choices"), "OpenAI streaming chunk should have choices"
        assert (
            len(chunk.choices) > 0
        ), "OpenAI streaming chunk should have at least one choice"

        choice = chunk.choices[0]
        assert hasattr(choice, "delta"), "OpenAI streaming choice should have delta"

        # Check for content or tool calls in delta
        has_content = (
            hasattr(choice.delta, "content") and choice.delta.content is not None
        )
        has_tool_calls = (
            hasattr(choice.delta, "tool_calls") and choice.delta.tool_calls is not None
        )
        has_role = hasattr(choice.delta, "role") and choice.delta.role is not None

        # Ignore completely empty deltas (like Cohere content-start with empty text)
        if not (has_content or has_tool_calls or has_role):
            return

        # Allow empty deltas for final chunks (they just signal completion)
        if not is_final:
            assert (
                has_content or has_tool_calls or has_role
            ), "OpenAI delta should have content, tool_calls, or role (except for final chunks)"

        if is_final:
            assert hasattr(
                choice, "finish_reason"
            ), "Final chunk should have finish_reason"
            assert (
                choice.finish_reason is not None
            ), "Final chunk finish_reason should not be None"

    elif integration.lower() == "anthropic":
        # Anthropic streaming format
        assert hasattr(chunk, "type"), "Anthropic streaming chunk should have type"

        if chunk.type == "content_block_delta":
            assert hasattr(
                chunk, "delta"
            ), "Content block delta should have delta field"

            # Validate based on delta type
            if hasattr(chunk.delta, "type"):
                if chunk.delta.type == "text_delta":
                    assert hasattr(
                        chunk.delta, "text"
                    ), "Text delta should have text field"
                elif chunk.delta.type == "thinking_delta":
                    assert hasattr(
                        chunk.delta, "thinking"
                    ), "Thinking delta should have thinking field"
                elif chunk.delta.type == "input_json_delta":
                    assert hasattr(
                        chunk.delta, "partial_json"
                    ), "Input JSON delta should have partial_json field"
        elif chunk.type == "message_delta" and is_final:
            assert hasattr(chunk, "usage"), "Final message delta should have usage"

    elif integration.lower() in ["google", "gemini", "genai"]:
        # Google streaming format
        assert hasattr(
            chunk, "candidates"
        ), "Google streaming chunk should have candidates"
        assert (
            len(chunk.candidates) > 0
        ), "Google streaming chunk should have at least one candidate"

        candidate = chunk.candidates[0]
        assert hasattr(candidate, "content"), "Google candidate should have content"

        if is_final:
            assert hasattr(
                candidate, "finish_reason"
            ), "Final chunk should have finish_reason"


def collect_streaming_content(
    stream, integration: str, timeout: int = 30
) -> tuple[str, int, bool]:
    """
    Collect content from a streaming response and validate the stream.

    Args:
        stream: The streaming response iterator
        integration: The integration name (openai, anthropic, etc.)
        timeout: Maximum time to wait for stream completion

    Returns:
        tuple: (collected_content, chunk_count, tool_calls_detected)
    """
    import time

    content_parts = []
    chunk_count = 0
    tool_calls_detected = False
    start_time = time.time()

    for chunk in stream:
        chunk_count += 1

        # Check timeout
        if time.time() - start_time > timeout:
            raise TimeoutError(f"Streaming took longer than {timeout} seconds")

        # Validate chunk
        is_final = False
        if integration.lower() == "openai":
            is_final = (
                hasattr(chunk, "choices")
                and len(chunk.choices) > 0
                and hasattr(chunk.choices[0], "finish_reason")
                and chunk.choices[0].finish_reason is not None
            )

        assert_valid_streaming_response(chunk, integration, is_final)

        # Extract content based on integration
        if integration.lower() == "openai":
            choice = chunk.choices[0]
            if hasattr(choice.delta, "content") and choice.delta.content:
                content_parts.append(choice.delta.content)
            if hasattr(choice.delta, "tool_calls") and choice.delta.tool_calls:
                tool_calls_detected = True

        elif integration.lower() == "anthropic":
            if chunk.type == "content_block_delta":
                if hasattr(chunk.delta, "text") and chunk.delta.text:
                    content_parts.append(chunk.delta.text)
                elif hasattr(chunk.delta, "thinking") and chunk.delta.thinking:
                    content_parts.append(chunk.delta.thinking)
                elif hasattr(chunk.delta, "type") and chunk.delta.type == "input_json_delta":
                    content_parts.append(chunk.delta.partial_json)
                    tool_calls_detected = True
                # Note: partial_json from input_json_delta is not user-visible content
            elif chunk.type == "content_block_start":
                # Check for tool use content blocks
                if (
                    hasattr(chunk, "content_block")
                    and hasattr(chunk.content_block, "type")
                    and chunk.content_block.type == "tool_use"
                ):
                    tool_calls_detected = True

        elif integration.lower() in ["google", "gemini", "genai"]:
            if hasattr(chunk, "candidates") and len(chunk.candidates) > 0:
                candidate = chunk.candidates[0]
                if (
                    hasattr(candidate.content, "parts")
                    and len(candidate.content.parts) > 0
                ):
                    for part in candidate.content.parts:
                        if hasattr(part, "text") and part.text:
                            content_parts.append(part.text)

        # Safety check
        if chunk_count > 500:
            raise ValueError(
                "Received too many streaming chunks, something might be wrong"
            )

    content = "".join(content_parts)
    return content, chunk_count, tool_calls_detected


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
    STREAMING = "streaming"
    MULTIPLE_IMAGES = "multiple_images"
    COMPLEX_E2E = "complex_e2e"
    INTEGRATION_SPECIFIC = "integration_specific"
    ERROR_HANDLING = "error_handling"


# Speech and Transcription Test Data
SPEECH_TEST_INPUT = "Hello, this is a test of the speech synthesis functionality. The quick brown fox jumps over the lazy dog."

SPEECH_TEST_VOICES = ["alloy", "echo", "fable", "onyx", "nova", "shimmer"]


def get_provider_voice(provider: str, voice_type: str = "primary") -> str:
    """
    Get an appropriate voice for the given provider.
    
    Args:
        provider: The provider name (e.g., "openai", "google", "gemini")
        voice_type: The type of voice - "primary", "secondary", or "tertiary"
    
    Returns:
        The voice name for the specified provider and type
    """
    # Normalize provider name
    provider_lower = provider.lower()
    
    # OpenAI voices
    if provider_lower == "openai":
        return {
            "primary": "alloy",
            "secondary": "nova",
            "tertiary": "echo",
        }.get(voice_type, "alloy")
    
    # Google/Gemini voices (using capitalized names as per Google GenAI SDK)
    elif provider_lower in ["google", "gemini"]:
        return {
            "primary": "Kore",
            "secondary": "Puck",
            "tertiary": "Aoede",
        }.get(voice_type, "Kore")
    
    # Default to OpenAI voices for other providers
    else:
        return {
            "primary": "alloy",
            "secondary": "nova",
            "tertiary": "echo",
        }.get(voice_type, "alloy")


def get_provider_voices(provider: str, count: int = 3) -> List[str]:
    """
    Get a list of voices for the given provider.
    
    Args:
        provider: The provider name (e.g., "openai", "google", "gemini")
        count: Number of voices to return
    
    Returns:
        List of voice names for the specified provider
    """
    provider_lower = provider.lower()
    
    if provider_lower == "openai":
        voices = ["alloy", "nova", "echo", "fable", "onyx", "shimmer"]
    elif provider_lower in ["google", "gemini"]:
        voices = ["Kore", "Puck", "Aoede", "Zephyr"]
    else:
        # Default to OpenAI voices
        voices = ["alloy", "nova", "echo", "fable", "onyx", "shimmer"]
    
    return voices[:count]


# Generate a simple test audio file (sine wave) for transcription testing
def generate_test_audio() -> bytes:
    """Generate a simple sine wave audio file for testing transcription"""
    import wave
    import math
    import struct

    # Audio parameters
    sample_rate = 16000  # 16kHz sample rate
    duration = 2  # 2 seconds
    frequency = 440  # A4 note (440 Hz)

    # Generate sine wave samples
    samples = []
    for i in range(int(sample_rate * duration)):
        t = i / sample_rate
        sample = int(32767 * math.sin(2 * math.pi * frequency * t))
        samples.append(struct.pack("<h", sample))

    # Create WAV file in memory
    import io

    wav_buffer = io.BytesIO()

    with wave.open(wav_buffer, "wb") as wav_file:
        wav_file.setnchannels(1)  # Mono
        wav_file.setsampwidth(2)  # 16-bit
        wav_file.setframerate(sample_rate)
        wav_file.writeframes(b"".join(samples))

    wav_buffer.seek(0)
    return wav_buffer.read()


# Simple test audio content (very short WAV file header + minimal data)
# This creates a valid but minimal WAV file for testing
TEST_AUDIO_DATA = (
    b"RIFF$\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00"
    b"\x00\x7d\x00\x00\x00\xfa\x00\x00\x02\x00\x10\x00data\x00\x00\x00\x00"
)

# Speech and Transcription Test Messages/Inputs
TRANSCRIPTION_TEST_INPUTS = [
    {
        "description": "Simple English audio",
        "expected_keywords": ["hello", "test", "audio", "transcription"],
    },
    {
        "description": "Long form content",
        "expected_keywords": ["speech", "recognition", "technology", "accuracy"],
    },
]


def assert_valid_speech_response(response: Any, expected_audio_size_min: int = 1000):
    """Assert that a speech synthesis response is valid"""
    assert response is not None, "Speech response should not be None"

    # OpenAI returns binary audio data directly
    if hasattr(response, "content"):
        # Handle the response.content case (from requests)
        audio_data = response.content
    elif hasattr(response, "read"):
        # Handle file-like objects
        audio_data = response.read()
    elif isinstance(response, bytes):
        # Handle direct bytes
        audio_data = response
    else:
        # Try to extract from response object
        audio_data = getattr(response, "audio", None)
        if audio_data is None:
            # Try other common attributes
            for attr in ["data", "body", "content"]:
                if hasattr(response, attr):
                    audio_data = getattr(response, attr)
                    break

    assert audio_data is not None, "Speech response should contain audio data"
    assert isinstance(
        audio_data, bytes
    ), f"Audio data should be bytes, got {type(audio_data)}"
    assert (
        len(audio_data) >= expected_audio_size_min
    ), f"Audio data should be at least {expected_audio_size_min} bytes, got {len(audio_data)}"

    # Check for common audio file headers
    # MP3 files start with 0xFF followed by 0xFB, 0xF3, 0xF2, or 0xF0 (MPEG frame sync)
    # or with an ID3 tag
    is_mp3 = (
        audio_data.startswith(b"\xff\xfb")  # MPEG-1 Layer III
        or audio_data.startswith(b"\xff\xf3")  # MPEG-2 Layer III
        or audio_data.startswith(b"\xff\xf2")  # MPEG-2.5 Layer III
        or audio_data.startswith(b"\xff\xf0")  # MPEG-2 Layer I/II
        or audio_data.startswith(b"ID3")  # ID3 tag
    )
    is_wav = audio_data.startswith(b"RIFF") and b"WAVE" in audio_data[:20]
    is_opus = audio_data.startswith(b"OggS")
    is_aac = audio_data.startswith(b"\xff\xf1") or audio_data.startswith(b"\xff\xf9")
    is_flac = audio_data.startswith(b"fLaC")

    assert (
        is_mp3 or is_wav or is_opus or is_aac or is_flac
    ), f"Audio data should be in a recognized format (MP3, WAV, Opus, AAC, or FLAC) but got {audio_data[:100]}"


def assert_valid_transcription_response(response: Any, min_text_length: int = 1):
    """Assert that a transcription response is valid"""
    assert response is not None, "Transcription response should not be None"

    # Extract transcribed text from various response formats
    text_content = ""

    if hasattr(response, "text"):
        # Direct text attribute
        text_content = response.text
    elif hasattr(response, "content"):
        # JSON response with content
        if isinstance(response.content, str):
            text_content = response.content
        elif isinstance(response.content, dict) and "text" in response.content:
            text_content = response.content["text"]
    elif isinstance(response, dict):
        # Direct dictionary response
        text_content = response.get("text", "")
    elif isinstance(response, str):
        # Direct string response
        text_content = response

    assert text_content is not None, "Transcription response should contain text"
    assert isinstance(
        text_content, str
    ), f"Transcribed text should be string, got {type(text_content)}"
    assert (
        len(text_content.strip()) >= min_text_length
    ), f"Transcribed text should be at least {min_text_length} characters, got: '{text_content}'"


def assert_valid_embedding_response(
    response: Any, expected_dimensions: Optional[int] = None
) -> None:
    """Assert that an embedding response is valid"""
    assert response is not None, "Embedding response should not be None"

    # Check if it's an OpenAI-style response object
    if hasattr(response, "data"):
        assert (
            len(response.data) > 0
        ), "Embedding response should contain at least one embedding"

        embedding = response.data[0].embedding
        assert isinstance(
            embedding, list
        ), f"Embedding should be a list, got {type(embedding)}"
        assert len(embedding) > 0, "Embedding should not be empty"
        assert all(
            isinstance(x, (int, float)) for x in embedding
        ), "All embedding values should be numeric"

        if expected_dimensions:
            assert (
                len(embedding) == expected_dimensions
            ), f"Expected {expected_dimensions} dimensions, got {len(embedding)}"

        # Check if usage information is present
        if hasattr(response, "usage") and response.usage:
            assert hasattr(
                response.usage, "total_tokens"
            ), "Usage should include total_tokens"
            assert (
                response.usage.total_tokens > 0
            ), "Token usage should be greater than 0"

    elif hasattr(response, "embeddings"):
        assert len(response.embeddings) > 0, "Embedding should not be empty"
        embedding = response.embeddings[0].values
        assert isinstance(embedding, list), "Embedding should be a list"
        assert len(embedding) > 0, "Embedding should not be empty"
        assert all(
            isinstance(x, (int, float)) for x in embedding
        ), "All embedding values should be numeric"
        if expected_dimensions:
            assert (
                len(embedding) == expected_dimensions
            ), f"Expected {expected_dimensions} dimensions, got {len(embedding)}"

    # Check if it's a direct list (embedding vector)
    elif isinstance(response, list):
        assert len(response) > 0, "Embedding should not be empty"
        assert all(
            isinstance(x, (int, float)) for x in response
        ), "All embedding values should be numeric"

        if expected_dimensions:
            assert (
                len(response) == expected_dimensions
            ), f"Expected {expected_dimensions} dimensions, got {len(response)}"

    else:
        raise AssertionError(f"Invalid embedding response format: {type(response)}")


def assert_valid_embeddings_batch_response(
    response: Any, expected_count: int, expected_dimensions: Optional[int] = None
) -> None:
    """Assert that a batch embeddings response is valid"""
    assert response is not None, "Embeddings batch response should not be None"

    # Check if it's an OpenAI-style response object
    if hasattr(response, "data"):
        assert (
            len(response.data) == expected_count
        ), f"Expected {expected_count} embeddings, got {len(response.data)}"

        for i, embedding_obj in enumerate(response.data):
            assert hasattr(
                embedding_obj, "embedding"
            ), f"Embedding object {i} should have 'embedding' attribute"
            embedding = embedding_obj.embedding

            assert isinstance(
                embedding, list
            ), f"Embedding {i} should be a list, got {type(embedding)}"
            assert len(embedding) > 0, f"Embedding {i} should not be empty"
            assert all(
                isinstance(x, (int, float)) for x in embedding
            ), f"All values in embedding {i} should be numeric"

            if expected_dimensions:
                assert (
                    len(embedding) == expected_dimensions
                ), f"Embedding {i}: expected {expected_dimensions} dimensions, got {len(embedding)}"

        # Check usage information
        if hasattr(response, "usage") and response.usage:
            assert hasattr(
                response.usage, "total_tokens"
            ), "Usage should include total_tokens"
            assert (
                response.usage.total_tokens > 0
            ), "Token usage should be greater than 0"

    # Check if it's a direct list of embeddings
    elif isinstance(response, list):
        assert (
            len(response) == expected_count
        ), f"Expected {expected_count} embeddings, got {len(response)}"

        for i, embedding in enumerate(response):
            assert isinstance(
                embedding, list
            ), f"Embedding {i} should be a list, got {type(embedding)}"
            assert len(embedding) > 0, f"Embedding {i} should not be empty"
            assert all(
                isinstance(x, (int, float)) for x in embedding
            ), f"All values in embedding {i} should be numeric"

            if expected_dimensions:
                assert (
                    len(embedding) == expected_dimensions
                ), f"Embedding {i}: expected {expected_dimensions} dimensions, got {len(embedding)}"

    else:
        raise AssertionError(
            f"Invalid embeddings batch response format: {type(response)}"
        )


def calculate_cosine_similarity(
    embedding1: List[float], embedding2: List[float]
) -> float:
    """Calculate cosine similarity between two embedding vectors"""
    import math

    assert len(embedding1) == len(embedding2), "Embeddings must have the same dimension"

    # Calculate dot product
    dot_product = sum(a * b for a, b in zip(embedding1, embedding2))

    # Calculate magnitudes
    magnitude1 = math.sqrt(sum(a * a for a in embedding1))
    magnitude2 = math.sqrt(sum(b * b for b in embedding2))

    # Avoid division by zero
    if magnitude1 == 0 or magnitude2 == 0:
        return 0.0

    return dot_product / (magnitude1 * magnitude2)


def assert_embeddings_similarity(
    embedding1: List[float],
    embedding2: List[float],
    min_similarity: float = 0.8,
    max_similarity: float = 1.0,
) -> None:
    """Assert that two embeddings have expected similarity"""
    similarity = calculate_cosine_similarity(embedding1, embedding2)
    assert (
        min_similarity <= similarity <= max_similarity
    ), f"Embedding similarity {similarity:.4f} should be between {min_similarity} and {max_similarity}"


def assert_embeddings_dissimilarity(
    embedding1: List[float], embedding2: List[float], max_similarity: float = 0.5
) -> None:
    """Assert that two embeddings are sufficiently different"""
    similarity = calculate_cosine_similarity(embedding1, embedding2)
    assert (
        similarity <= max_similarity
    ), f"Embedding similarity {similarity:.4f} should be at most {max_similarity} for dissimilar texts"


def assert_valid_streaming_speech_response(chunk: Any, integration: str):
    """Assert that a streaming speech response chunk is valid"""
    assert chunk is not None, "Streaming speech chunk should not be None"

    if integration.lower() == "openai":
        # For OpenAI, speech streaming returns audio chunks
        # The chunk might be direct bytes or wrapped in an object
        if hasattr(chunk, "audio"):
            audio_data = chunk.audio
        elif hasattr(chunk, "data"):
            audio_data = chunk.data
        elif isinstance(chunk, bytes):
            audio_data = chunk
        else:
            # Try to find audio data in the chunk
            audio_data = None
            for attr in ["content", "chunk", "audio_chunk"]:
                if hasattr(chunk, attr):
                    audio_data = getattr(chunk, attr)
                    break

        if audio_data:
            assert isinstance(
                audio_data, bytes
            ), f"Audio chunk should be bytes, got {type(audio_data)}"
            assert len(audio_data) > 0, "Audio chunk should not be empty"


def assert_valid_streaming_transcription_response(chunk: Any, integration: str):
    """Assert that a streaming transcription response chunk is valid"""
    assert chunk is not None, "Streaming transcription chunk should not be None"

    if integration.lower() == "openai":
        # For OpenAI, transcription streaming returns text chunks
        if hasattr(chunk, "text"):
            text_chunk = chunk.text
        elif hasattr(chunk, "content"):
            text_chunk = chunk.content
        elif isinstance(chunk, str):
            text_chunk = chunk
        elif isinstance(chunk, dict) and "text" in chunk:
            text_chunk = chunk["text"]
        else:
            # Try to find text data in the chunk
            text_chunk = None
            for attr in ["data", "chunk", "text_chunk"]:
                if hasattr(chunk, attr):
                    text_chunk = getattr(chunk, attr)
                    break

        if text_chunk:
            assert isinstance(
                text_chunk, str
            ), f"Text chunk should be string, got {type(text_chunk)}"
            # Note: text chunks can be empty in streaming (e.g., just punctuation updates)
    
    elif integration.lower() in ["google", "gemini"]:
        # For Google GenAI, transcription returns GenerateContentResponse objects
        # The text is available through response.text or response.candidates
        if hasattr(chunk, "text"):
            text_chunk = chunk.text
        elif hasattr(chunk, "candidates") and chunk.candidates:
            # Extract text from candidates
            for candidate in chunk.candidates:
                if hasattr(candidate, "content") and candidate.content:
                    if hasattr(candidate.content, "parts") and candidate.content.parts:
                        for part in candidate.content.parts:
                            if hasattr(part, "text") and part.text:
                                text_chunk = part.text
                                break
        
        # Note: Google streaming chunks can be empty or contain only metadata


def collect_streaming_speech_content(
    stream, integration: str, timeout: int = 60
) -> tuple[bytes, int]:
    """
    Collect audio content from a streaming speech response.

    Args:
        stream: The streaming response iterator
        integration: The integration name (openai, etc.)
        timeout: Maximum time to wait for stream completion

    Returns:
        tuple: (collected_audio_bytes, chunk_count)
    """
    import time

    audio_chunks = []
    chunk_count = 0
    start_time = time.time()

    for chunk in stream:
        chunk_count += 1

        # Check timeout
        if time.time() - start_time > timeout:
            raise TimeoutError(f"Speech streaming took longer than {timeout} seconds")

        # Validate chunk
        assert_valid_streaming_speech_response(chunk, integration)

        # Extract audio data
        if integration.lower() == "openai":
            if hasattr(chunk, "audio") and chunk.audio:
                audio_chunks.append(chunk.audio)
            elif hasattr(chunk, "data") and chunk.data:
                audio_chunks.append(chunk.data)
            elif isinstance(chunk, bytes):
                audio_chunks.append(chunk)

        # Safety check
        if chunk_count > 1000:
            raise ValueError(
                "Received too many speech streaming chunks, something might be wrong"
            )

    # Combine all audio chunks
    complete_audio = b"".join(audio_chunks)
    return complete_audio, chunk_count


def collect_streaming_transcription_content(
    stream, integration: str, timeout: int = 60
) -> tuple[str, int]:
    """
    Collect text content from a streaming transcription response.

    Args:
        stream: The streaming response iterator
        integration: The integration name (openai, google, etc.)
        timeout: Maximum time to wait for stream completion

    Returns:
        tuple: (collected_text, chunk_count)
    """
    import time

    text_chunks = []
    chunk_count = 0
    start_time = time.time()

    for chunk in stream:
        chunk_count += 1

        # Check timeout
        if time.time() - start_time > timeout:
            raise TimeoutError(
                f"Transcription streaming took longer than {timeout} seconds"
            )

        # Validate chunk
        assert_valid_streaming_transcription_response(chunk, integration)

        # Extract text data
        if integration.lower() == "openai":
            if hasattr(chunk, "text") and chunk.text:
                text_chunks.append(chunk.text)
            elif hasattr(chunk, "content") and chunk.content:
                text_chunks.append(chunk.content)
            elif isinstance(chunk, str):
                text_chunks.append(chunk)
        
        elif integration.lower() in ["google", "gemini"]:
            # For Google GenAI streaming
            if hasattr(chunk, "text") and chunk.text:
                text_chunks.append(chunk.text)
            elif hasattr(chunk, "candidates") and chunk.candidates:
                for candidate in chunk.candidates:
                    if hasattr(candidate, "content") and candidate.content:
                        if hasattr(candidate.content, "parts") and candidate.content.parts:
                            for part in candidate.content.parts:
                                if hasattr(part, "text") and part.text:
                                    text_chunks.append(part.text)

        # Safety check
        if chunk_count > 1000:
            raise ValueError(
                "Received too many transcription streaming chunks, something might be wrong"
            )

    # Combine all text chunks
    complete_text = "".join(text_chunks)
    return complete_text, chunk_count


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


# Responses API Helpers
def convert_to_responses_tools(tools: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
    """Convert common tool format to OpenAI Responses API format"""
    responses_tools = []
    for tool in tools:
        responses_tool = {
            "type": "function",
            "name": tool["name"],
            "description": tool.get("description", ""),
            "parameters": tool.get("parameters", {}),
        }
        responses_tools.append(responses_tool)
    return responses_tools


def assert_valid_responses_response(response: Any, min_content_length: int = 1):
    """Assert that a responses API response is valid"""
    assert response is not None, "Responses response should not be None"
    assert hasattr(response, "output"), "Response should have 'output' attribute"
    assert isinstance(response.output, list), "Output should be a list"
    assert len(response.output) > 0, "Output should contain at least one message"

    # Check for message content in output or summary
    has_content = False
    total_content = ""
    
    for message in response.output:
        # Check for regular content
        if hasattr(message, "content") and message.content:
            # Content can be string or list of content blocks
            if isinstance(message.content, str):
                total_content += message.content
                if len(message.content) >= min_content_length:
                    has_content = True
            elif isinstance(message.content, list):
                for block in message.content:
                    if hasattr(block, "text") and block.text:
                        total_content += block.text
                        if len(block.text) >= min_content_length:
                            has_content = True
        
        # Check for summary field within output messages (for reasoning models)
        if hasattr(message, "summary") and message.summary:
            if isinstance(message.summary, list):
                for summary_item in message.summary:
                    if hasattr(summary_item, "text") and summary_item.text:
                        total_content += summary_item.text
                        if len(summary_item.text) >= min_content_length:
                            has_content = True
                    elif isinstance(summary_item, dict) and "text" in summary_item:
                        total_content += summary_item["text"]
                        if len(summary_item["text"]) >= min_content_length:
                            has_content = True
            elif isinstance(message.summary, str):
                total_content += message.summary
                if len(message.summary) >= min_content_length:
                    has_content = True
    

    assert has_content, (
        f"Response should contain content of at least {min_content_length} characters. "
        f"Found {len(total_content)} characters total."
    )

    # Check for usage information if present
    if hasattr(response, "usage") and response.usage:
        assert hasattr(response.usage, "total_tokens"), "Usage should include total_tokens"
        assert response.usage.total_tokens > 0, "Total tokens should be greater than 0"


def assert_responses_has_tool_calls(response: Any, expected_count: Optional[int] = None):
    """Assert that a responses API response contains function calls"""
    assert response is not None, "Response should not be None"
    assert hasattr(response, "output"), "Response should have 'output' attribute"

    tool_calls = []
    for message in response.output:
        if hasattr(message, "type") and message.type == "function_call":
            tool_calls.append(message)

    assert len(tool_calls) > 0, "Response should contain at least one function call"

    if expected_count is not None:
        assert (
            len(tool_calls) == expected_count
        ), f"Expected {expected_count} tool calls, got {len(tool_calls)}"

    # Validate tool call structure
    for tool_call in tool_calls:
        assert hasattr(tool_call, "name"), "Tool call should have a name"
        assert tool_call.name is not None, "Tool call name should not be None"


def collect_responses_streaming_content(
    stream, timeout: int = 30
) -> tuple[str, int, bool, Dict[str, int]]:
    """
    Collect content from a responses API streaming response.

    Args:
        stream: The streaming response iterator
        timeout: Maximum time to wait for stream completion

    Returns:
        tuple: (collected_content, chunk_count, tool_calls_detected, event_types)
    """
    import time

    content_parts = []
    chunk_count = 0
    tool_calls_detected = False
    event_types = {}
    start_time = time.time()

    for chunk in stream:
        chunk_count += 1

        # Check timeout
        if time.time() - start_time > timeout:
            raise TimeoutError(f"Streaming took longer than {timeout} seconds")

        # Track event types
        if hasattr(chunk, "type"):
            event_type = chunk.type
            event_types[event_type] = event_types.get(event_type, 0) + 1

            # Collect text deltas
            if event_type == "response.output_text.delta" and hasattr(chunk, "delta"):
                content_parts.append(chunk.delta)

            # Check for function calls
            if event_type == "response.function_call_arguments.delta":
                tool_calls_detected = True

            # Also check for output items that might be function calls
            if event_type == "response.output_item.added" and hasattr(chunk, "item"):
                if hasattr(chunk.item, "type") and chunk.item.type == "function_call":
                    tool_calls_detected = True

        # Safety check
        if chunk_count > 1000:
            raise ValueError(
                "Received too many streaming chunks, something might be wrong"
            )

    # Combine all content parts
    complete_content = "".join(content_parts)
    return complete_content, chunk_count, tool_calls_detected, event_types


def assert_valid_responses_streaming_chunk(chunk: Any):
    """Assert that a responses streaming chunk is valid"""
    assert chunk is not None, "Streaming chunk should not be None"
    assert hasattr(chunk, "type"), "Chunk should have a 'type' attribute"

    # Validate common streaming event types
    valid_event_types = [
        "response.created",
        "response.output_item.added",
        "response.content_part.added",
        "response.output_text.delta",
        "response.function_call_arguments.delta",
        "response.completed",
        "response.error",
    ]

    # Log the event type for debugging
    if hasattr(chunk, "type"):
        event_type = chunk.type
        # Don't fail on unknown event types, just warn
        if not any(evt in event_type for evt in ["response.", "error"]):
            print(f"Warning: Unexpected event type: {event_type}")


# Text Completions Helpers
def assert_valid_text_completion_response(response: Any, min_content_length: int = 1):
    """Assert that a text completion response is valid"""
    assert response is not None, "Text completion response should not be None"
    assert hasattr(response, "choices"), "Response should have 'choices' attribute"
    assert isinstance(response.choices, list), "Choices should be a list"
    assert len(response.choices) > 0, "Choices should contain at least one item"

    # Check for text content in first choice
    first_choice = response.choices[0]
    assert hasattr(first_choice, "text"), "Choice should have 'text' attribute"
    assert isinstance(first_choice.text, str), "Text should be a string"
    assert len(first_choice.text) >= min_content_length, (
        f"Text should be at least {min_content_length} characters, "
        f"got {len(first_choice.text)}"
    )

    # Check for usage information if present
    if hasattr(response, "usage") and response.usage:
        assert hasattr(response.usage, "total_tokens"), "Usage should include total_tokens"
        assert response.usage.total_tokens > 0, "Total tokens should be greater than 0"


def collect_text_completion_streaming_content(
    stream, timeout: int = 30
) -> tuple[str, int]:
    """
    Collect content from a text completion streaming response.

    Args:
        stream: The streaming response iterator
        timeout: Maximum time to wait for stream completion

    Returns:
        tuple: (collected_content, chunk_count)
    """
    import time

    content_parts = []
    chunk_count = 0
    start_time = time.time()

    for chunk in stream:
        chunk_count += 1

        # Check timeout
        if time.time() - start_time > timeout:
            raise TimeoutError(f"Streaming took longer than {timeout} seconds")

        # Extract text from choices
        if hasattr(chunk, "choices") and len(chunk.choices) > 0:
            choice = chunk.choices[0]
            if hasattr(choice, "text") and choice.text:
                content_parts.append(choice.text)

        # Safety check
        if chunk_count > 1000:
            raise ValueError(
                "Received too many streaming chunks, something might be wrong"
            )

    # Combine all content parts
    complete_content = "".join(content_parts)
    return complete_content, chunk_count

def get_content_string(content: Any) -> str:
    """Get a string representation of content"""
    if isinstance(content, str):
        return content
    elif isinstance(content, list):
        return " ".join([c.get("text", "") for c in content])
    else:
        return ""