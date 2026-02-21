"""
Common utilities and test data for all integration tests.
This module contains shared functions, test data, and assertions
that can be used across all integration-specific test files.
"""

import ast
import json
import operator
import os
from dataclasses import dataclass
from typing import Any, Dict, List, Optional


# Test Configuration
@dataclass
class Config:
    """Configuration for test execution"""

    timeout: int = 30
    max_retries: int = 3
    debug: bool = False


# Image Test Data
IMAGE_URL = "https://pub-cdead89c2f004d8f963fd34010c479d0.r2.dev/Gfp-wisconsin-madison-the-nature-boardwalk.jpg"
IMAGE_URL_SECONDARY = "https://goo.gle/instrument-img"

FILE_DATA_BASE64 = (
            "JVBERi0xLjcKCjEgMCBvYmogICUgZW50cnkgcG9pbnQKPDwKICAvVHlwZSAvQ2F0YWxvZwogIC"
            "9QYWdlcyAyIDAgUgo+PgplbmRvYmoKCjIgMCBvYmoKPDwKICAvVHlwZSAvUGFnZXwKICAvTWV"
            "kaWFCb3ggWyAwIDAgMjAwIDIwMCBdCiAgL0NvdW50IDEKICAvS2lkcyBbIDMgMCBSIF0KPj4K"
            "ZW5kb2JqCgozIDAgb2JqCjw8CiAgL1R5cGUgL1BhZ2UKICAvUGFyZW50IDIgMCBSCiAgL1Jlc"
            "291cmNlcyA8PAogICAgL0ZvbnQgPDwKICAgICAgL0YxIDQgMCBSCj4+CiAgPj4KICAvQ29udG"
            "VudHMgNSAwIFIKPj4KZW5kb2JqCgo0IDAgb2JqCjw8CiAgL1R5cGUgL0ZvbnQKICAvU3VidHl"
            "wZSAvVHlwZTEKICAvQmFzZUZvbnQgL1RpbWVzLVJvbWFuCj4+CmVuZG9iagoKNSAwIG9iago8"
            "PAogIC9MZW5ndGggNDQKPj4Kc3RyZWFtCkJUCjcwIDUwIFRECi9GMSAxMiBUZgooSGVsbG8gV"
            "29ybGQhKSBUagpFVAplbmRzdHJlYW0KZW5kb2JqCgp4cmVmCjAgNgowMDAwMDAwMDAwIDY1NT"
            "M1IGYgCjAwMDAwMDAwMTAgMDAwMDAgbiAKMDAwMDAwMDA2MCAwMDAwMCBuIAowMDAwMDAwMTU"
            "3IDAwMDAwIG4gCjAwMDAwMDAyNTUgMDAwMDAgbiAKMDAwMDAwMDM1MyAwMDAwMCBuIAp0cmFp"
            "bGVyCjw8CiAgL1NpemUgNgogIC9Sb290IDEgMCBSCj4+CnN0YXJ0eHJlZgo0NDkKJSVFT0YK"
        )

# Small test image as base64 (64x64 pixel red PNG - minimum size for some providers)
def _create_base64_image(width: int = 64, height: int = 64) -> str:
    """Create a base64-encoded PNG image of specified size (default 64x64 for minimum requirements)."""
    from PIL import Image
    import io
    import base64
    
    # Create a simple red image
    img = Image.new('RGB', (width, height), color='red')
    
    # Encode as PNG
    buffer = io.BytesIO()
    img.save(buffer, format='PNG')
    img_bytes = buffer.getvalue()
    
    return base64.b64encode(img_bytes).decode('utf-8')

BASE64_IMAGE = _create_base64_image(64, 64)

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

# Tools for Prompt Caching Tests 
PROMPT_CACHING_TOOLS = [
    {
        "name": "get_weather",
        "description": "Get the current weather for a location",
        "parameters": {
            "type": "object",
            "required": ["location"],
            "properties": {
                "location": {
                    "description": "The city and state, e.g. San Francisco, CA",
                    "type": "string"
                },
                "unit": {
                    "description": "The temperature unit",
                    "enum": ["celsius", "fahrenheit"],
                    "type": "string"
                }
            }
        }
    },
    {
        "name": "get_current_time",
        "description": "Get the current local time for a given city",
        "parameters": {
            "type": "object",
            "required": ["location"],
            "properties": {
                "location": {
                    "description": "The city and country, e.g. London, UK",
                    "type": "string"
                }
            }
        }
    },
    {
        "name": "unit_converter",
        "description": "Convert a numeric value from one unit to another",
        "parameters": {
            "type": "object",
            "required": ["value", "from_unit", "to_unit"],
            "properties": {
                "value": {
                    "description": "The numeric value to convert",
                    "type": "number"
                },
                "from_unit": {
                    "description": "The source unit",
                    "type": "string"
                },
                "to_unit": {
                    "description": "The target unit",
                    "type": "string"
                }
            }
        }
    },
    {
        "name": "get_exchange_rate",
        "description": "Get the current exchange rate between two currencies",
        "parameters": {
            "type": "object",
            "required": ["base_currency", "target_currency"],
            "properties": {
                "base_currency": {
                    "description": "The base currency code, e.g. USD",
                    "type": "string"
                },
                "target_currency": {
                    "description": "The target currency code, e.g. EUR",
                    "type": "string"
                }
            }
        }
    },
    {
        "name": "translate_text",
        "description": "Translate text from one language to another",
        "parameters": {
            "type": "object",
            "required": ["text", "target_language"],
            "properties": {
                "text": {
                    "description": "The text to translate",
                    "type": "string"
                },
                "target_language": {
                    "description": "The target language code, e.g. fr, es",
                    "type": "string"
                }
            }
        }
    },
    {
        "name": "summarize_text",
        "description": "Summarize a long piece of text into a concise form",
        "parameters": {
            "type": "object",
            "required": ["text"],
            "properties": {
                "text": {
                    "description": "The text to summarize",
                    "type": "string"
                },
                "max_length": {
                    "description": "Maximum length of the summary",
                    "type": "integer"
                }
            }
        }
    },
    {
        "name": "detect_language",
        "description": "Detect the language of a given text",
        "parameters": {
            "type": "object",
            "required": ["text"],
            "properties": {
                "text": {
                    "description": "The text whose language should be detected",
                    "type": "string"
                }
            }
        }
    },
    {
        "name": "extract_keywords",
        "description": "Extract important keywords from a block of text",
        "parameters": {
            "type": "object",
            "required": ["text"],
            "properties": {
                "text": {
                    "description": "The input text",
                    "type": "string"
                },
                "max_keywords": {
                    "description": "Maximum number of keywords to return",
                    "type": "integer"
                }
            }
        }
    },
    {
        "name": "sentiment_analysis",
        "description": "Analyze the sentiment of a given text",
        "parameters": {
            "type": "object",
            "required": ["text"],
            "properties": {
                "text": {
                    "description": "The text to analyze",
                    "type": "string"
                }
            }
        }
    },
    {
        "name": "generate_uuid",
        "description": "Generate a random UUID",
        "parameters": {
            "type": "object",
            "properties": {}
        }
    },
    {
        "name": "check_url_status",
        "description": "Check if a URL is accessible and return its HTTP status",
        "parameters": {
            "type": "object",
            "required": ["url"],
            "properties": {
                "url": {
                    "description": "The URL to check",
                    "type": "string"
                }
            }
        }
    },
    {
        "name": "calculate",
        "description": "Perform basic mathematical calculations",
        "parameters": {
            "type": "object",
            "required": ["expression"],
            "properties": {
                "expression": {
                    "description": "Mathematical expression to evaluate, e.g. '2 + 2'",
                    "type": "string"
                }
            }
        }
    }
]

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

# Prompt Caching Test Data
PROMPT_CACHING_LARGE_CONTEXT = """You are an AI assistant tasked with analyzing legal documents. 
Here is a detailed legal framework for contract analysis:

1. CONTRACT FORMATION: A contract requires offer, acceptance, and consideration. The offer must be 
definite and communicated to the offeree. Acceptance must mirror the terms of the offer (mirror image 
rule). Consideration is the bargained-for exchange that makes the contract legally binding. Both parties 
must have the legal capacity to enter into a contract, and the contract's purpose must be legal.

2. WARRANTIES: Express warranties are explicit promises made by the seller about the product or service. 
Implied warranties include the warranty of merchantability (the product is fit for ordinary purposes) and 
the warranty of fitness for a particular purpose (the product is suitable for a specific buyer's needs). 
These warranties provide guarantees about product or service quality and can be the basis for breach of 
contract claims.

3. LIMITATION OF LIABILITY: These clauses limit the amount or types of damages that can be recovered in 
case of breach. They may cap damages at a specific amount, exclude certain types of damages (like 
consequential or punitive damages), or limit liability to repair or replacement of defective goods. Courts 
scrutinize these clauses carefully and may refuse to enforce them if they are unconscionable or against 
public policy.

4. INDEMNIFICATION: Indemnification clauses require one party to compensate the other for losses, damages, 
or liabilities arising from specified events or claims. These provisions allocate risk between parties and 
are particularly important in contracts involving potential third-party claims. The scope of indemnification 
can vary widely, from narrow protection for specific claims to broad coverage for any losses arising from 
the relationship.

5. TERMINATION: Contract termination provisions specify the conditions under which either party may end the 
contractual relationship. These may include termination for cause (breach of contract), termination for 
convenience (with or without notice), automatic termination upon certain events, or mutual agreement. 
Termination clauses often address notice requirements, cure periods, and the parties' rights and obligations 
upon termination.

6. DISPUTE RESOLUTION: These provisions establish the methods for resolving disagreements between parties. 
Options include litigation in courts, arbitration (binding resolution by a neutral arbitrator), mediation 
(facilitated negotiation), or a combination of methods. Arbitration clauses often specify the arbitration 
rules (such as AAA or JAMS), the number of arbitrators, the location of arbitration, and whether arbitration 
decisions are binding and final.

7. FORCE MAJEURE: Force majeure clauses excuse performance when extraordinary events or circumstances beyond 
the parties' control prevent fulfillment of contractual obligations. These events typically include natural 
disasters, wars, pandemics, government actions, and other unforeseeable circumstances. The clause usually 
defines what constitutes a force majeure event and specifies the parties' obligations during such events, 
including notice requirements and efforts to mitigate damages.

8. INTELLECTUAL PROPERTY: These provisions address rights related to patents, copyrights, trademarks, trade 
secrets, and other intellectual property. They may cover ownership of pre-existing IP, IP created during the 
contract term, licensing arrangements, and protection of proprietary information. IP clauses are crucial in 
technology, creative works, and research and development contracts.

9. CONFIDENTIALITY: Confidentiality provisions (also called non-disclosure clauses) impose obligations to 
protect sensitive information shared between parties. They define what constitutes confidential information, 
specify how it must be protected, limit its disclosure and use, and establish the duration of confidentiality 
obligations. These clauses often survive contract termination and may include exceptions for information that 
is publicly available or independently developed.

10. GOVERNING LAW: Governing law clauses specify which jurisdiction's laws will apply to interpret and enforce 
the contract. This is particularly important in contracts between parties in different states or countries. 
The chosen jurisdiction's laws will govern issues like contract formation, performance, breach, and remedies. 
These clauses often work in conjunction with venue or forum selection clauses that specify where disputes must 
be resolved.""" * 3  # Repeat to ensure sufficient tokens (1024+ minimum)

# Gemini Reasoning Test Prompts
GEMINI_REASONING_PROMPT = [
    {
        "role": "user",
        "content": (
            "A farmer has 100 chickens and 50 cows. Each chicken lays 5 eggs per week, and each cow produces 20 liters of milk per day. "
            "If the farmer sells eggs for $0.25 each and milk for $1.50 per liter, and it costs $2 per week to feed each chicken and $15 per week to feed each cow, "
            "what is the farmer's weekly profit? Please show your step-by-step reasoning."
        ),
    }
]

GEMINI_REASONING_STREAMING_PROMPT = [
    {
        "role": "user",
        "content": (
            "A library has 1200 books. In January, they lent out 40% of their books. In February, they got 150 books returned and lent out 200 new books. "
            "In March, they received 80 new books as donations and lent out 25% of their current inventory. "
            "How many books does the library have available at the end of March? Think through this step by step."
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
        "parts": [{"text": "Hello! This should fail due to invalid role in GenAI format."}],
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
                raise ValueError(f"Unsupported unary operation: {type(node.op).__name__}")
        elif isinstance(node, ast.BinOp):
            if type(node.op) in ALLOWED_OPS:
                left = eval_node(node.left)
                right = eval_node(node.right)
                return ALLOWED_OPS[type(node.op)](left, right)
            else:
                raise ValueError(f"Unsupported binary operation: {type(node.op).__name__}")
        else:
            raise ValueError(f"Unsupported expression type: {type(node).__name__}")

    try:
        # Parse the expression into an AST
        tree = ast.parse(expression, mode="eval")
        # Evaluate the AST
        return eval_node(tree.body)
    except SyntaxError as e:
        raise SyntaxError(f"Invalid syntax in expression '{expression}': {e}") from e
    except ZeroDivisionError:
        raise ZeroDivisionError(f"Division by zero in expression '{expression}'") from None
    except Exception as e:
        raise ValueError(f"Error evaluating expression '{expression}': {e}") from e


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
                            "id": tool_call.id,
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
                        "id": tool_call.id,
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
                tool_calls.append(
                    {"id": content.id, "name": content.name, "arguments": content.input}
                )

    # Handle Bedrock format
    elif isinstance(response, dict) and "output" in response:
        output = response.get("output")
        if output and isinstance(output, dict) and "message" in output:
            message = output.get("message")
            if message and isinstance(message, dict) and "content" in message:
                for content in message.get("content", []):
                    if isinstance(content, dict) and "toolUse" in content:
                        tool_use = content["toolUse"]
                        if isinstance(tool_use, dict):
                            tool_calls.append(
                                {
                                    "id": tool_use.get("toolUseId"),
                                    "name": tool_use.get("name"),
                                    "arguments": tool_use.get("input"),
                                }
                            )

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
            text_content = [c for c in response.content if hasattr(c, "type") and c.type == "text"]
            if text_content:
                content = text_content[0].text
            else:
                # Check for compaction blocks
                compaction_content = [c for c in response.content if hasattr(c, "type") and c.type == "compaction"]
                if compaction_content and hasattr(compaction_content[0], "content"):
                    content = compaction_content[0].content
    elif hasattr(response, "choices") and len(response.choices) > 0:  # OpenAI
        # Handle OpenAI format (content can be string or list)
        choice = response.choices[0]
        if hasattr(choice, "message") and hasattr(choice.message, "content"):
            content = get_content_string(choice.message.content)
    elif isinstance(response, dict) and "output" in response:  # Bedrock (boto3)
        # Handle Bedrock format
        output = response["output"]
        if "message" in output and "content" in output["message"]:
            for item in output["message"]["content"]:
                if "text" in item:
                    content = item["text"]
                    break

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
        assert "id" in tool_call, "Tool call should have an ID"
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
            text_content = [c for c in response.content if hasattr(c, "type") and c.type == "text"]
            if text_content:
                content = text_content[0].text.lower()
    elif hasattr(response, "choices") and len(response.choices) > 0:  # OpenAI
        choice = response.choices[0]
        if hasattr(choice, "message") and hasattr(choice.message, "content"):
            content = get_content_string(choice.message.content).lower()
    elif isinstance(response, dict) and "output" in response:  # Bedrock (boto3)
        output = response["output"]
        if "message" in output and "content" in output["message"]:
            for item in output["message"]["content"]:
                if "text" in item:
                    content = item["text"].lower()
                    break

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
        # Action/descriptive verbs
        "depicts",
        "displays",
        "contains",
        "features",
        "includes",
        "shows",
        "has",
        "presents",
        # Demonstrative phrases
        "this is",
        "there is",
        "there are",
        "here is",
        "it is",
        # Visual descriptors
        "visible",
        "shown",
        "depicted",
        "appearing",
        "illustrated",
        # Structural words
        "with",
        "showing",
        "displaying",
        "featuring",
    ]
    has_image_reference = any(keyword in content for keyword in image_keywords)

    assert has_image_reference, f"Response should reference the image content. Got: {content}"


def assert_valid_error_response(response_or_exception: Any, expected_invalid_role: str = "tester"):
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
                        error_message = error_data["error"].get("message", str(error_data["error"]))
                        error_type = error_data["error"].get("type", "")
                    else:
                        error_message = str(error_data["error"])
                else:
                    error_message = error_data.get("message", str(error_data))
            else:
                error_message = str(error_data)
        except Exception:
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
        "resource does not exist"
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
    if integration.lower() in ("openai", "azure"):
        # OpenAI format: should have top-level 'type', 'event_id' and 'error' field with nested structure
        if hasattr(error_response, "response"):
            error_data = error_response.response.json()
            assert "error" in error_data, "OpenAI error should have 'error' field"

            # Check nested error structure
            error_obj = error_data["error"]
            assert "message" in error_obj, "OpenAI error.error should have 'message' field"
            assert "type" in error_obj, "OpenAI error.error should have 'type' field"
            assert "code" in error_obj, "OpenAI error.error should have 'code' field"

    elif integration.lower() == "anthropic":
        # Anthropic format: should have 'type' and 'error' with 'type' and 'message'
        if hasattr(error_response, "response"):
            error_data = error_response.response.json()
            assert "type" in error_data, "Anthropic error should have 'type' field"
            # Type field can be empty string if not set in original error
            assert isinstance(error_data["type"], str), "Anthropic error type should be a string"
            assert "error" in error_data, "Anthropic error should have 'error' field"
            assert "type" in error_data["error"], "Anthropic error.error should have 'type' field"
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
            assert "message" in error_obj, "Gemini error.error should have 'message' field"
            assert isinstance(
                error_obj["message"], str
            ), "Gemini error.error.message should be a string"
            assert "status" in error_obj, "Gemini error.error should have 'status' field"
            assert isinstance(
                error_obj["status"], str
            ), "Gemini error.error.status should be a string"

    return True


def assert_valid_streaming_response(chunk: Any, integration: str, is_final: bool = False):
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
        assert len(chunk.choices) > 0, "OpenAI streaming chunk should have at least one choice"

        choice = chunk.choices[0]
        assert hasattr(choice, "delta"), "OpenAI streaming choice should have delta"

        # Check for content or tool calls in delta
        has_content = hasattr(choice.delta, "content") and choice.delta.content is not None
        has_tool_calls = hasattr(choice.delta, "tool_calls") and choice.delta.tool_calls is not None
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
            assert hasattr(choice, "finish_reason"), "Final chunk should have finish_reason"
            assert choice.finish_reason is not None, "Final chunk finish_reason should not be None"

    elif integration.lower() == "anthropic":
        # Anthropic streaming format
        assert hasattr(chunk, "type"), "Anthropic streaming chunk should have type"

        if chunk.type == "content_block_delta":
            assert hasattr(chunk, "delta"), "Content block delta should have delta field"

            # Validate based on delta type
            if hasattr(chunk.delta, "type"):
                if chunk.delta.type == "text_delta":
                    assert hasattr(chunk.delta, "text"), "Text delta should have text field"
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
        assert hasattr(chunk, "candidates"), "Google streaming chunk should have candidates"
        assert (
            len(chunk.candidates) > 0
        ), "Google streaming chunk should have at least one candidate"

        candidate = chunk.candidates[0]
        assert hasattr(candidate, "content"), "Google candidate should have content"

        if is_final:
            assert hasattr(candidate, "finish_reason"), "Final chunk should have finish_reason"


def collect_streaming_content(stream, integration: str, timeout: int = 30) -> tuple[str, int, bool]:
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
                if hasattr(candidate.content, "parts") and len(candidate.content.parts) > 0:
                    for part in candidate.content.parts:
                        if hasattr(part, "text") and part.text:
                            content_parts.append(part.text)

        # Safety check
        if chunk_count > 500:
            raise ValueError("Received too many streaming chunks, something might be wrong")

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
    import math
    import struct
    import wave

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
    assert isinstance(audio_data, bytes), f"Audio data should be bytes, got {type(audio_data)}"
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
        assert len(response.data) > 0, "Embedding response should contain at least one embedding"

        embedding = response.data[0].embedding
        assert isinstance(embedding, list), f"Embedding should be a list, got {type(embedding)}"
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
            assert hasattr(response.usage, "total_tokens"), "Usage should include total_tokens"
            assert response.usage.total_tokens > 0, "Token usage should be greater than 0"

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
            assert hasattr(response.usage, "total_tokens"), "Usage should include total_tokens"
            assert response.usage.total_tokens > 0, "Token usage should be greater than 0"

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
        raise AssertionError(f"Invalid embeddings batch response format: {type(response)}")


def calculate_cosine_similarity(embedding1: List[float], embedding2: List[float]) -> float:
    """Calculate cosine similarity between two embedding vectors"""
    import math

    assert len(embedding1) == len(embedding2), "Embeddings must have the same dimension"

    # Calculate dot product
    dot_product = sum(a * b for a, b in zip(embedding1, embedding2, strict=False))

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
            raise ValueError("Received too many speech streaming chunks, something might be wrong")

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
            raise TimeoutError(f"Transcription streaming took longer than {timeout} seconds")

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
        "google": "GEMINI_API_KEY",
        "gemini": "GEMINI_API_KEY",
        "litellm": "LITELLM_API_KEY",
        "bedrock": "AWS_ACCESS_KEY_ID",  # Bedrock uses AWS credentials
        "cohere": "COHERE_API_KEY",
        "vertex": "VERTEX_API_KEY",
        "xai": "XAI_API_KEY",
        "nebius": "NEBIUS_API_KEY",
        "huggingface": "HUGGING_FACE_API_KEY",
        "azure": "AZURE_API_KEY",
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

            # collect summary text deltas
            if event_type == "response.reasoning_summary_text.delta" and hasattr(chunk, "delta") and chunk.delta:
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
            raise ValueError("Received too many streaming chunks, something might be wrong")

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
        f"Text should be at least {min_content_length} characters, " f"got {len(first_choice.text)}"
    )

    # Check for usage information if present
    if hasattr(response, "usage") and response.usage:
        assert hasattr(response.usage, "total_tokens"), "Usage should include total_tokens"
        assert response.usage.total_tokens > 0, "Total tokens should be greater than 0"


def collect_text_completion_streaming_content(stream, timeout: int = 30) -> tuple[str, int]:
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
            raise ValueError("Received too many streaming chunks, something might be wrong")

    # Combine all content parts
    complete_content = "".join(content_parts)
    return complete_content, chunk_count


def get_content_string(content: Any) -> str:
    """Get a string representation of content"""
    if isinstance(content, str):
        return content
    elif isinstance(content, list):
        parts: List[str] = []
        for c in content:
            if isinstance(c, dict):
                parts.append(c.get("text", ""))
            elif hasattr(c, "text"):
                parts.append(c.text or "")
        return " ".join(filter(None, parts))
    else:
        return ""


# =============================================================================
# Files API Test Utilities
# =============================================================================


def create_batch_jsonl_content(
    model: str = "gpt-4o-mini", num_requests: int = 2, provider: str | None = None
) -> str:
    """
    Create JSONL content for batch API testing.

    Args:
        model: The model to use in batch requests
        num_requests: Number of requests to include
        provider: Provider name (e.g., 'openai', 'anthropic'). If provided,
                  formats the model as 'provider/model'

    Returns:
        JSONL formatted string with batch requests
    """
    requests = []
    prompts = [
        "What is 2 + 2?",
        "What is the capital of France?",
        "Name a primary color.",
        "What planet is closest to the Sun?",
    ]

    # Format model with provider prefix if specified
    # formatted_model = f"{provider}/{model}" if provider else model

    for i in range(num_requests):
        request = {
            "custom_id": f"request-{i+1}",
            "method": "POST",
            "url": "/v1/chat/completions",
            "body": {
                "model": model,
                "messages": [{"role": "user", "content": prompts[i % len(prompts)]}],
                "max_tokens": 50,
            },
        }
        requests.append(json.dumps(request))

    return "\n".join(requests)


def assert_valid_file_response(response, expected_purpose: str | None = None) -> None:
    """
    Assert that a file upload/retrieve response is valid.

    Args:
        response: The file response object
        expected_purpose: Expected purpose field (optional)
    """
    assert response is not None, "File response should not be None"
    assert hasattr(response, "id"), "File response should have 'id' attribute"
    assert response.id is not None, "File ID should not be None"
    assert len(response.id) > 0, "File ID should not be empty"

    assert hasattr(response, "object"), "File response should have 'object' attribute"
    assert response.object == "file", f"Object should be 'file', got {response.object}"

    assert hasattr(response, "bytes"), "File response should have 'bytes' attribute"
    assert response.bytes > 0, "File bytes should be greater than 0"

    assert hasattr(response, "filename"), "File response should have 'filename' attribute"
    assert response.filename is not None, "Filename should not be None"

    assert hasattr(response, "purpose"), "File response should have 'purpose' attribute"
    if expected_purpose:
        assert (
            response.purpose == expected_purpose
        ), f"Purpose should be '{expected_purpose}', got {response.purpose}"


def assert_valid_file_list_response(response, min_count: int = 0) -> None:
    """
    Assert that a file list response is valid.

    Args:
        response: The file list response object
        min_count: Minimum expected number of files
    """
    assert response is not None, "File list response should not be None"
    assert hasattr(response, "data"), "File list response should have 'data' attribute"
    assert isinstance(response.data, list), "Data should be a list"
    assert (
        len(response.data) >= min_count
    ), f"Should have at least {min_count} files, got {len(response.data)}"


def assert_valid_file_delete_response(response, expected_id: str | None = None) -> None:
    """
    Assert that a file delete response is valid.

    Args:
        response: The file delete response object
        expected_id: Expected file ID that was deleted
    """
    assert response is not None, "File delete response should not be None"
    assert hasattr(response, "id"), "Delete response should have 'id' attribute"
    assert hasattr(response, "deleted"), "Delete response should have 'deleted' attribute"
    assert response.deleted is True, "Deleted should be True"

    if expected_id:
        assert (
            response.id == expected_id
        ), f"Deleted file ID should be '{expected_id}', got {response.id}"


# =============================================================================
# Batch API Test Utilities
# =============================================================================

BATCH_VALID_STATUSES = [
    "validating",
    "failed",
    "in_progress",
    "finalizing",
    "completed",
    "expired",
    "cancelling",
    "cancelled",
]


def assert_valid_batch_response(response, expected_status: str | None = None) -> None:
    """
    Assert that a batch create/retrieve response is valid.

    Args:
        response: The batch response object
        expected_status: Expected status (optional)
    """
    assert response is not None, "Batch response should not be None"
    assert hasattr(response, "id"), "Batch response should have 'id' attribute"
    assert response.id is not None, "Batch ID should not be None"
    assert len(response.id) > 0, "Batch ID should not be empty"

    assert hasattr(response, "object"), "Batch response should have 'object' attribute"
    assert response.object == "batch", f"Object should be 'batch', got {response.object}"

    assert hasattr(response, "status"), "Batch response should have 'status' attribute"
    assert (
        response.status in BATCH_VALID_STATUSES
    ), f"Status should be one of {BATCH_VALID_STATUSES}, got {response.status}"

    if expected_status:
        assert (
            response.status == expected_status
        ), f"Status should be '{expected_status}', got {response.status}"

    assert hasattr(response, "endpoint"), "Batch response should have 'endpoint' attribute"


def assert_valid_batch_list_response(response, min_count: int = 0) -> None:
    """
    Assert that a batch list response is valid.

    Args:
        response: The batch list response object
        min_count: Minimum expected number of batches
    """
    assert response is not None, "Batch list response should not be None"
    assert hasattr(response, "data"), "Batch list response should have 'data' attribute"
    assert isinstance(response.data, list), "Data should be a list"
    assert (
        len(response.data) >= min_count
    ), f"Should have at least {min_count} batches, got {len(response.data)}"


# =============================================================================
# Batch API - Provider-Specific Utilities
# =============================================================================

# Batch inline request prompts
BATCH_INLINE_PROMPTS = [
    "What is 2 + 2?",
    "What is the capital of France?",
    "Name a primary color.",
    "What planet is closest to the Sun?",
]


def create_batch_inline_requests(
    model: str, num_requests: int = 2, provider: str | None = None, sdk: str | None = None
) -> List[Dict[str, Any]]:
    """
    Create inline requests array for batch API (Anthropic/Gemini/OpenAI inline format).

    Args:
        model: The model to use in batch requests
        num_requests: Number of requests to include
        provider: Provider name (e.g., 'openai', 'anthropic', 'gemini', 'bedrock')

    Returns:
        List of inline request items
    """
    requests = []

    # Format model with provider prefix if specified
    formatted_model = f"{provider}/{model}" if provider else model

    for i in range(num_requests):
        prompt = BATCH_INLINE_PROMPTS[i % len(BATCH_INLINE_PROMPTS)]

        # Build the request body/params based on provider
        if sdk == "anthropic":
            # Anthropic uses 'params' instead of 'body'
            if provider == "openai":
                request_item = {
                    "custom_id": f"request-{i+1}",
                    "params": {
                        "url": "/v1/chat/completions",
                        "model": model,  # Anthropic doesn't use provider prefix
                        "messages": [{"role": "user", "content": prompt}],
                        "max_tokens": 100,
                    },
                }
            else:
                request_item = {
                    "custom_id": f"request-{i+1}",
                    "params": {
                        "model": model,  # Anthropic doesn't use provider prefix
                        "messages": [{"role": "user", "content": prompt}],
                        "max_tokens": 100,
                    },
                }
        elif sdk == "gemini":
            # Gemini batch uses inline content format
            request_item = {
                "custom_id": f"request-{i+1}",
                "body": {
                    "model": model,  # Gemini doesn't use provider prefix
                    "messages": [{"role": "user", "content": prompt}],
                    "max_tokens": 100,
                },
            }
        elif sdk == "openai":
            # OpenAI/Azure style - use body with full model path
            request_item = {
                "custom_id": f"request-{i+1}",
                "method": "POST",
                "url": "/v1/chat/completions",
                "body": {
                    "model": formatted_model,
                    "messages": [{"role": "user", "content": prompt}],
                    "max_tokens": 100,
                },
            }
        else:
            raise ValueError(f"Invalid SDK: {sdk}")
        requests.append(request_item)

    return requests


def get_bedrock_s3_config() -> Dict[str, Optional[str]]:
    """
    Get Bedrock S3 configuration from environment variables.

    Returns:
        Dictionary with S3 configuration:
        - s3_bucket: S3 bucket name (from AWS_S3_BUCKET)
        - role_arn: IAM role ARN (from AWS_BEDROCK_ROLE_ARN)
        - output_s3_prefix: Output S3 prefix (from AWS_OUTPUT_S3_PREFIX or default)
        - region: AWS region (from AWS_REGION or default us-west-2)
    """
    return {
        "s3_bucket": os.environ.get("AWS_S3_BUCKET"),
        "role_arn": os.environ.get("AWS_ARN"),
        "output_s3_prefix": os.environ.get("AWS_OUTPUT_S3_PREFIX", "bifrost-batch-output/"),
        "region": os.environ.get("AWS_REGION", "us-west-2"),
    }


def is_bedrock_s3_configured() -> bool:
    """
    Check if Bedrock S3 configuration is available.

    Returns:
        True if AWS_S3_BUCKET is set, False otherwise
    """
    config = get_bedrock_s3_config()
    return config["s3_bucket"] is not None and len(config["s3_bucket"]) > 0


def get_bedrock_batch_extra_params() -> Dict[str, Any]:
    """
    Get extra params required for Bedrock batch API.

    Returns:
        Dictionary with role_arn and output_s3_uri

    Raises:
        ValueError if required S3 config is not available
    """
    config = get_bedrock_s3_config()

    if not config["s3_bucket"]:
        raise ValueError("AWS_S3_BUCKET environment variable is required for Bedrock batch API")

    output_s3_uri = f"s3://{config['s3_bucket']}/{config['output_s3_prefix']}"

    extra_params = {
        "output_s3_uri": output_s3_uri,
    }

    # Add role_arn if available
    if config["role_arn"]:
        extra_params["role_arn"] = config["role_arn"]

    return extra_params


def create_bedrock_batch_s3_uri(
    bucket: str, prefix: str = "bifrost-batch-input/", filename: str | None = None
) -> str:
    """
    Create an S3 URI for Bedrock batch input.

    Args:
        bucket: S3 bucket name
        prefix: S3 key prefix
        filename: Optional filename (auto-generated if not provided)

    Returns:
        S3 URI string (e.g., s3://bucket/prefix/filename.jsonl)
    """
    import time

    if filename is None:
        filename = f"batch-input-{int(time.time())}.jsonl"

    return f"s3://{bucket}/{prefix}{filename}"


def assert_valid_batch_inline_response(response, provider: str | None = None) -> None:
    """
    Assert that a batch response from inline requests is valid.
    This handles provider-specific response formats.

    Args:
        response: The batch response object
        provider: Provider name for provider-specific validation
    """
    assert response is not None, "Batch response should not be None"
    assert hasattr(response, "id"), "Batch response should have 'id' attribute"
    assert response.id is not None, "Batch ID should not be None"
    assert len(response.id) > 0, "Batch ID should not be empty"

    # Check status - different providers may return different valid statuses
    if provider == "anthropic":
        # Anthropic uses processing_status field with values: in_progress, canceling, ended
        valid_statuses = ["in_progress", "canceling", "ended"]
        if hasattr(response, "processing_status") and response.processing_status:
            assert (
                response.processing_status in valid_statuses
            ), f"Processing status should be one of {valid_statuses}, got {response.processing_status}"
        else:
            assert hasattr(response, "status"), "Batch response should have 'status' attribute"
            assert (
                response.status in BATCH_VALID_STATUSES
            ), f"Status should be one of {BATCH_VALID_STATUSES}, got {response.status}"
    elif provider == "gemini":
        # Gemini synchronous batch returns completed immediately
        assert hasattr(response, "status"), "Batch response should have 'status' attribute"
        assert (
            response.status in BATCH_VALID_STATUSES or response.status == "completed"
        ), f"Status should be valid for Gemini, got {response.status}"
    else:
        # OpenAI/Azure/Bedrock
        assert hasattr(response, "status"), "Batch response should have 'status' attribute"
        assert (
            response.status in BATCH_VALID_STATUSES
        ), f"Status should be one of {BATCH_VALID_STATUSES}, got {response.status}"


def skip_if_no_bedrock_s3():
    """
    Pytest skip decorator/marker for tests requiring Bedrock S3 configuration.
    Use as: @skip_if_no_bedrock_s3() or call skip_if_no_bedrock_s3() at test start.
    """
    import pytest

    if not is_bedrock_s3_configured():
        pytest.skip("Bedrock S3 tests require AWS_S3_BUCKET environment variable")


def get_content_string_with_summary(response: Any) -> tuple[str, bool]:
    """
    Extract content from response, handling both OpenAI API responses and LangChain AIMessage objects.
    
    Returns:
        tuple: (content_string, has_reasoning_content)
    """
    content = ""
    has_reasoning_content = False
    
    # Check if this is a LangChain AIMessage object
    if hasattr(response, 'content') and hasattr(response, 'response_metadata'):
        # LangChain AIMessage
        if isinstance(response.content, str):
            content = response.content
        elif isinstance(response.content, list):
            for item in response.content:
                if isinstance(item, dict):
                    # Check for thinking block (Anthropic format)
                    if item.get('type') == 'thinking' and 'thinking' in item:
                        has_reasoning_content = True
                        thinking_text = item.get('thinking')
                        if isinstance(thinking_text, str):
                            content += thinking_text + " "
                    # Check for reasoning block with summary
                    elif item.get('type') == 'reasoning' and 'summary' in item:
                        has_reasoning_content = True
                        summary = item.get('summary')
                        if isinstance(summary, list):
                            for summary_block in summary:
                                if isinstance(summary_block, dict) and 'text' in summary_block:
                                    content += summary_block['text'] + " "
                    # Check for reasoning block with content (Gemini format)
                    elif item.get('type') == 'reasoning' and 'content' in item:
                        has_reasoning_content = True
                        reasoning_content = item.get('content')
                        if isinstance(reasoning_content, list):
                            for content_block in reasoning_content:
                                if isinstance(content_block, dict) and 'text' in content_block:
                                    content += content_block['text'] + " "
                    # Check for text block
                    elif item.get('type') == 'text' and 'text' in item:
                        content += item['text'] + " "
                elif isinstance(item, str):
                    # Handle plain string items in the content list
                    content += item + " "
        return content.strip(), has_reasoning_content
    
    # OpenAI API response - check output messages
    if hasattr(response, 'output'):
        for message in response.output:
            if hasattr(message, "type"):
                # Check if we have a reasoning message type
                if message.type == "reasoning":
                    has_reasoning_content = True
            
            # Check regular content
            if hasattr(message, "content") and message.content:
                if isinstance(message.content, str):
                    content += message.content
                elif isinstance(message.content, list):
                    for block in message.content:
                        if hasattr(block, "text") and block.text:
                            content += block.text
                        # Check for reasoning content blocks
                        if hasattr(block, "type") and block.type == "reasoning_text":
                            has_reasoning_content = True
                    
            # Check summary field within output messages (reasoning models)
            if hasattr(message, "summary") and message.summary:
                has_reasoning_content = True  # Presence of summary indicates reasoning
                if isinstance(message.summary, list):
                    for summary_item in message.summary:
                        if hasattr(summary_item, "text") and summary_item.text:
                            content += " " + summary_item.text
                        elif isinstance(summary_item, dict) and "text" in summary_item:
                            content += " " + summary_item["text"]
                        # Check for summary_text type
                        if hasattr(summary_item, "type") and summary_item.type == "summary_text":
                            has_reasoning_content = True
                        elif isinstance(summary_item, dict) and summary_item.get("type") == "summary_text":
                            has_reasoning_content = True
                elif isinstance(message.summary, str):
                    content += " " + message.summary
    return content, has_reasoning_content


# =========================================================================
# INPUT TOKENS / TOKEN COUNTING UTILITIES
# =========================================================================

# Test inputs for token counting
INPUT_TOKENS_SIMPLE_TEXT = "Hello, how are you?"
INPUT_TOKENS_LONG_TEXT = "This is a longer text that should have more tokens. " * 20
INPUT_TOKENS_WITH_SYSTEM = [
    {"role": "system", "content": "You are a helpful assistant."},
    {"role": "user", "content": "Hello!"},
]
INPUT_TOKENS_WITH_TOOLS = {
    "messages": [{"role": "user", "content": "What's the weather in Paris?"}],
    "tools": [WEATHER_TOOL],
}


def assert_valid_input_tokens_response(response: Any, library: str):
    """
    Assert that a response from input_tokens endpoint is valid.
    
    Args:
        response: The response object from input_tokens.count()
        library: Name of the library/integration used (e.g., 'openai', 'google').
    """
    assert response is not None, "Response should not be None"

    if library == "google":
        assert hasattr(response, "total_tokens"), "Response should have total_tokens attribute for Gemini"
        assert isinstance(response.total_tokens, int), "total_tokens should be a int for Gemini"
        assert response.total_tokens > 0, f"total_tokens should be positive, got {response.total_tokens}"
    elif library == "openai":
        assert isinstance(response.object, str), "object should be a string"
        assert (
            "input_tokens" in response.object
        ), f"object should indicate input_tokens, got {response.object}"

        assert isinstance(response.input_tokens, int), "input_tokens should be an integer"
        assert response.input_tokens > 0, (
            f"input_tokens should be positive, got {response.input_tokens}"
        )
    else:
        assert hasattr(response, "input_tokens"), "Response should have input_tokens attribute"
        assert isinstance(response.input_tokens, int), "input_tokens should be an integer"
        assert response.input_tokens > 0, f"input_tokens should be positive, got {response.input_tokens}"


# =========================================================================
# IMAGE GENERATION UTILITIES
# =========================================================================

# Test prompts for image generation
IMAGE_GENERATION_SIMPLE_PROMPT = "A serene mountain landscape at sunset with a calm lake in the foreground"

# Image edit test data
IMAGE_EDIT_SIMPLE_PROMPT = "Add a beautiful sunset in the background with vibrant orange and pink colors"
IMAGE_EDIT_PROMPT_OUTPAINT = "Extend the image with a scenic landscape continuation"

def assert_valid_image_generation_response(response: Any, library: str = "openai"):
    """
    Assert that an image generation response is valid.
    
    Args:
        response: The response object from image generation
        library: Name of the library/integration used (e.g., 'openai', 'google')
    """
    assert response is not None, "Image generation response should not be None"
    
    if library == "openai":
        # OpenAI returns ImagesResponse with data array
        assert hasattr(response, "data"), "Response should have 'data' attribute"
        assert isinstance(response.data, list), "data should be a list"
        assert len(response.data) > 0, "data list should not be empty"
        
        # Each item in data should have either url or b64_json
        for i, image in enumerate(response.data):
            has_url = hasattr(image, "url") and image.url
            has_b64 = hasattr(image, "b64_json") and image.b64_json
            assert has_url or has_b64, f"Image {i} should have either 'url' or 'b64_json'"
            
            # If b64_json, validate it looks like base64
            if has_b64:
                assert len(image.b64_json) > 100, f"Image {i} b64_json seems too short"
                
    elif library == "google":
        # Google GenAI returns GenerateContentResponse with candidates
        # Handle both dict (raw HTTP) and object (SDK) responses
        candidates = None
        if isinstance(response, dict):
            candidates = response.get("candidates")
        elif hasattr(response, "candidates"):
            candidates = response.candidates
            
        if candidates:
            # Native Gemini image generation
            assert len(candidates) > 0, "Response should have at least one candidate"
            candidate = candidates[0]
            
            # Get content (handle dict or object)
            content = candidate.get("content") if isinstance(candidate, dict) else getattr(candidate, "content", None)
            assert content is not None, "Candidate should have content"
            
            # Get parts (handle dict or object)
            parts = content.get("parts") if isinstance(content, dict) else getattr(content, "parts", None)
            
            # Check for inline_data with image
            found_image = False
            if parts:
                for part in parts:
                    inline_data = part.get("inlineData") if isinstance(part, dict) else getattr(part, "inline_data", None)
                    if inline_data:
                        found_image = True
                        mime_type = inline_data.get("mimeType") if isinstance(inline_data, dict) else getattr(inline_data, "mime_type", "")
                        data = inline_data.get("data") if isinstance(inline_data, dict) else getattr(inline_data, "data", "")
                        assert mime_type.startswith("image/"), \
                            f"Expected image mime type, got {mime_type}"
                        assert len(data) > 100, "Image data seems too short"
            assert found_image, "Response should contain at least one image"
        elif (isinstance(response, dict) and "predictions" in response) or hasattr(response, "predictions"):
            # Imagen response
            predictions = response.get("predictions") if isinstance(response, dict) else response.predictions
            assert len(predictions) > 0, "Response should have at least one prediction"
            for i, prediction in enumerate(predictions):
                has_b64 = (prediction.get("bytesBase64Encoded") if isinstance(prediction, dict) 
                          else (hasattr(prediction, "bytesBase64Encoded") or hasattr(prediction, "bytes_base64_encoded")))
                assert has_b64, f"Prediction {i} should have base64 encoded bytes"
        else:
            # Raw dict response - check for candidates or other formats
            assert "candidates" in response or "data" in response or "predictions" in response, \
                f"Response should have 'candidates', 'data' or 'predictions'. Got keys: {list(response.keys()) if isinstance(response, dict) else 'N/A'}"
    else:
        # Generic validation
        assert hasattr(response, "data") or hasattr(response, "predictions"), \
            "Response should have 'data' or 'predictions' attribute"


def assert_image_generation_usage(response: Any, library: str = "openai"):
    """
    Assert that image generation usage information is present (if supported).
    
    Args:
        response: The response object from image generation
        library: Name of the library/integration used
    """
    if library == "openai":
        # OpenAI may include usage for some image models
        if hasattr(response, "usage") and response.usage:
            if hasattr(response.usage, "total_tokens"):
                assert response.usage.total_tokens >= 0, "total_tokens should be non-negative"
    elif library == "google":
        # Google may include usage metadata
        if hasattr(response, "usage_metadata") and response.usage_metadata:
            pass  # Usage metadata format varies


# =========================================================================
# IMAGE EDIT UTILITIES
# =========================================================================

def create_simple_mask_image(width: int = 1024, height: int = 1024) -> str:
    """
    Create a simple mask image for inpainting tests.
    Returns base64-encoded PNG mask with alpha channel (white center = edit area, black borders = preserve).
    
    Args:
        width: Mask width in pixels
        height: Mask height in pixels
    
    Returns:
        Base64-encoded PNG mask image with alpha channel
    """
    from PIL import Image, ImageDraw
    import io
    import base64
    
    # Create RGBA image with alpha channel (required by OpenAI)
    mask = Image.new('RGBA', (width, height), color=(0, 0, 0, 255))  # Black with full opacity
    
    # Create white rectangle in center (area to edit) with full opacity
    center_x, center_y = width // 2, height // 2
    mask_width, mask_height = width // 3, height // 3
    
    draw = ImageDraw.Draw(mask)
    draw.rectangle(
        [center_x - mask_width // 2, center_y - mask_height // 2,
         center_x + mask_width // 2, center_y + mask_height // 2],
        fill=(255, 255, 255, 255)  # White with full opacity
    )
    
    # Encode as PNG (supports alpha channel)
    buffer = io.BytesIO()
    mask.save(buffer, format='PNG')
    mask_bytes = buffer.getvalue()
    
    return base64.b64encode(mask_bytes).decode('utf-8')


def assert_valid_image_edit_response(response: Any, library: str = "openai"):
    """
    Assert that an image edit response is valid.
    
    Image edit responses have the same structure as image generation responses,
    so we can reuse the generation validation with additional checks.
    
    Args:
        response: The response object from image edit
        library: Name of the library/integration used (e.g., 'openai', 'google')
    """
    # Image edit responses use the same format as generation
    assert_valid_image_generation_response(response, library)
    
    # Additional validation specific to edits could go here
    # (currently same structure as generation)


def assert_image_edit_usage(response: Any, library: str = "openai"):
    """
    Assert that image edit usage information is present (if supported).
    
    Args:
        response: The response object from image edit
        library: Name of the library/integration used
    """
    # Image edit usage follows same format as generation
    assert_image_generation_usage(response, library)


# =========================================================================
# CITATIONS TEST DATA AND UTILITIES
# =========================================================================

# Test document content for citations
CITATION_TEXT_DOCUMENT = """The Theory of Relativity was developed by Albert Einstein in the early 20th century.
It consists of two parts: Special Relativity published in 1905, and General Relativity published in 1915.

Special Relativity deals with objects moving at constant velocities and introduced the famous equation E=mc².
General Relativity extends this to accelerating objects and provides a new understanding of gravity.

Einstein's work revolutionized our understanding of space, time, and gravity, and its predictions have been
confirmed by numerous experiments and observations over the past century."""

# Multiple documents for testing document_index
CITATION_MULTI_DOCUMENT_SET = [
    {
        "title": "Physics Document",
        "content": """Quantum mechanics is a fundamental theory in physics that describes the behavior of matter and energy at the atomic and subatomic level.
It was developed in the early 20th century by physicists including Max Planck, Albert Einstein, Niels Bohr, and Werner Heisenberg."""
    },
    {
        "title": "Chemistry Document", 
        "content": """The periodic table organizes chemical elements by their atomic number, electron configuration, and recurring chemical properties.
It was first published by Dmitri Mendeleev in 1869 and has become a fundamental tool in chemistry."""
    }
]


def create_anthropic_document(
    content: str,
    doc_type: str,
    title: str = "Test Document",
    citations_enabled: bool = True
) -> Dict[str, Any]:
    """
    Create a properly formatted document block for Anthropic API with citations.
    
    Args:
        content: Document content (text or base64)
        doc_type: Document type - "text", "pdf", or "base64"
        title: Document title
        citations_enabled: Whether to enable citations
    
    Returns:
        Formatted document block dict
    """
    document = {
        "type": "document",
        "title": title,
        "citations": {"enabled": citations_enabled}
    }
    
    if doc_type == "text":
        document["source"] = {
            "type": "text",
            "media_type": "text/plain",
            "data": content
        }
    elif doc_type == "pdf" or doc_type == "base64":
        document["source"] = {
            "type": "base64",
            "media_type": "application/pdf",
            "data": content
        }
    else:
        raise ValueError(f"Unsupported doc_type: {doc_type}. Use 'text', 'pdf', or 'base64'")
    
    return document


def validate_citation_indices(citation: Any, citation_type: str) -> None:
    """
    Validate citation indices based on type.
    
    Args:
        citation: Citation object to validate
        citation_type: Expected citation type (char_location, page_location)
    """
    if citation_type == "char_location":
        # Character indices: 0-indexed, exclusive end
        assert hasattr(citation, "start_char_index"), "char_location should have start_char_index"
        assert hasattr(citation, "end_char_index"), "char_location should have end_char_index"
        assert citation.start_char_index >= 0, "start_char_index should be >= 0"
        assert citation.end_char_index > citation.start_char_index, \
            f"end_char_index ({citation.end_char_index}) should be > start_char_index ({citation.start_char_index})"
    
    elif citation_type == "page_location":
        # Page numbers: 1-indexed, exclusive end
        assert hasattr(citation, "start_page_number"), "page_location should have start_page_number"
        assert hasattr(citation, "end_page_number"), "page_location should have end_page_number"
        assert citation.start_page_number >= 1, "start_page_number should be >= 1 (1-indexed)"
        assert citation.end_page_number > citation.start_page_number, \
            f"end_page_number ({citation.end_page_number}) should be > start_page_number ({citation.start_page_number})"


def assert_valid_anthropic_citation(
    citation: Any,
    expected_type: str,
    document_index: int = 0
) -> None:
    """
    Assert that an Anthropic citation is valid and matches expected structure.
    
    Args:
        citation: Citation object from Anthropic response
        expected_type: Expected citation type (char_location, page_location)
        document_index: Expected document index (0-indexed)
    """
    # Check basic structure
    assert hasattr(citation, "type"), "Citation should have type field"
    assert citation.type == expected_type, \
        f"Citation type should be {expected_type}, got {citation.type}"
    
    # Check required fields
    assert hasattr(citation, "cited_text"), "Citation should have cited_text"
    assert isinstance(citation.cited_text, str), "cited_text should be a string"
    assert len(citation.cited_text) > 0, "cited_text should not be empty"
    
    # Check document reference
    assert hasattr(citation, "document_index"), "Citation should have document_index"
    assert citation.document_index == document_index, \
        f"document_index should be {document_index}, got {citation.document_index}"
    
    # Check document title (optional but common)
    if hasattr(citation, "document_title"):
        assert isinstance(citation.document_title, str), "document_title should be a string"
    
    # Validate type-specific indices
    validate_citation_indices(citation, expected_type)


def assert_valid_openai_annotation(
    annotation: Any,
    expected_type: str
) -> None:
    """
    Assert that an OpenAI annotation is valid and matches expected structure.
    
    Args:
        annotation: Annotation object from OpenAI Responses API
        expected_type: Expected annotation type (file_citation, url_citation, etc.)
    """
    if isinstance(annotation, dict):
        ann_type = annotation.get("type")
        assert ann_type == expected_type, f"Annotation type should be {expected_type}, got {ann_type}"
        getter = annotation.get
        has = annotation.__contains__
    else:
        assert hasattr(annotation, "type"), "Annotation should have type field"
        assert annotation.type == expected_type, f"Annotation type should be {expected_type}, got {annotation.type}"
        getter = lambda k: getattr(annotation, k, None)
        has = lambda k: hasattr(annotation, k)
    
    # Validate based on type
    if expected_type == "file_citation":
        if has("file_id"):
            assert isinstance(getter("file_id"), str), "file_id should be a string"
        if has("filename"):
            assert isinstance(getter("filename"), str), "filename should be a string"
        if has("index"):
            assert isinstance(getter("index"), int), "index should be an integer"
            assert getter("index") >= 0, "index should be >= 0"
    
    elif expected_type == "url_citation":
        # url_citation: url, title, start_index, end_index
        if has("url"):
            assert isinstance(getter("url"), str), "url should be a string"
        if has("title"):
            assert isinstance(getter("title"), str), "title should be a string"
        if has("start_index") and has("end_index"):
            assert isinstance(getter("start_index"), int), "start_index should be an integer"
            assert isinstance(getter("end_index"), int), "end_index should be an integer"
            assert getter("end_index") > getter("start_index"), "end_index should be > start_index"

    
    elif expected_type == "container_file_citation":
        # container_file_citation: container_id, file_id, filename, start_index, end_index
        if has("container_id"):
            assert isinstance(getter("container_id"), str), "container_id should be a string"
        if has("file_id"):
            assert isinstance(getter("file_id"), str), "file_id should be a string"
        if has("filename"):
            assert isinstance(getter("filename"), str), "filename should be a string"

    
    elif expected_type == "file_path":
        if has("file_id"):
            assert isinstance(getter("file_id"), str), "file_id should be a string"
        if has("index"):
            assert isinstance(getter("index"), int), "index should be an integer"
            assert getter("index") >= 0, "index should be >= 0"
    
    # Check for char_location (Anthropic native type that may come through)
    elif expected_type == "char_location":
        if has("start_char_index"):
            assert isinstance(getter("start_char_index"), int), "start_char_index should be an integer"
        if has("end_char_index"):
            assert isinstance(getter("end_char_index"), int), "end_char_index should be an integer"


def collect_anthropic_streaming_citations(
    stream,
    timeout: int = 30
) -> tuple[str, list, int]:
    """
    Collect text content and citations from an Anthropic streaming response.
    
    Args:
        stream: Anthropic streaming response iterator
        timeout: Maximum time to collect (seconds)
    
    Returns:
        Tuple of (complete_text, citations_list, chunk_count)
    """
    import time
    start_time = time.time()
    
    text_parts = []
    citations = []
    chunk_count = 0
    
    for event in stream:
        chunk_count += 1
        
        # Check timeout
        if time.time() - start_time > timeout:
            break
        
        if hasattr(event, "type"):
            event_type = event.type
            
            # Handle content_block_delta events
            if event_type == "content_block_delta":
                if hasattr(event, "delta") and event.delta:
                    # Check for text delta
                    if hasattr(event.delta, "type"):
                        if event.delta.type == "text_delta":
                            if hasattr(event.delta, "text"):
                                text_parts.append(str(event.delta.text))
                        
                        # Check for citations delta
                        elif event.delta.type == "citations_delta":
                            if hasattr(event.delta, "citation"):
                                citations.append(event.delta.citation)
        
        # Safety check
        if chunk_count > 2000:
            break
    
    complete_text = "".join(text_parts)
    return complete_text, citations, chunk_count


def collect_openai_streaming_annotations(
    stream,
    timeout: int = 30
) -> tuple[str, list, int]:
    """
    Collect text content and annotations from OpenAI Responses API streaming.
    
    Args:
        stream: OpenAI Responses API streaming response iterator
        timeout: Maximum time to collect (seconds)
    
    Returns:
        Tuple of (complete_text, annotations_list, chunk_count)
    """
    import time
    start_time = time.time()
    
    text_parts = []
    annotations = []
    chunk_count = 0
    
    for chunk in stream:
        chunk_count += 1
        
        # Check timeout
        if time.time() - start_time > timeout:
            break
        
        if hasattr(chunk, "type"):
            chunk_type = chunk.type
            
            # Handle text delta
            if chunk_type == "response.output_text.delta":
                if hasattr(chunk, "delta"):
                    text_parts.append(chunk.delta)
            
            # Handle annotation added
            elif chunk_type == "response.output_text.annotation.added":
                if hasattr(chunk, "annotation"):
                    annotations.append(chunk.annotation)
        
        # Safety check
        if chunk_count > 5000:
            break
    
    complete_text = "".join(text_parts)
    return complete_text, annotations, chunk_count


# ============================================================================
# WEB SEARCH VALIDATION HELPERS
# ============================================================================

def assert_valid_web_search_citation(citation, sdk_type="anthropic"):
    """
    Validate web search citation structure.
    
    Args:
        citation: Citation object to validate
        sdk_type: Either "anthropic" or "openai"
    """
    if sdk_type == "anthropic":
        assert hasattr(citation, "type"), "Citation should have type"
        assert citation.type == "web_search_result_location", f"Expected web_search_result_location, got {citation.type}"
        assert hasattr(citation, "url") and citation.url, "Citation should have non-empty URL"
        assert hasattr(citation, "title") and citation.title, "Citation should have non-empty title"
        assert hasattr(citation, "encrypted_index"), "Citation should have encrypted_index"
        assert hasattr(citation, "cited_text"), "Citation should have cited_text"
        if citation.cited_text:
            assert len(citation.cited_text) <= 150, f"cited_text should be <= 150 chars, got {len(citation.cited_text)}"
    elif sdk_type == "openai":
        assert hasattr(citation, "type"), "Annotation should have type"
        assert citation.type == "url_citation", f"Expected url_citation, got {citation.type}"
        assert hasattr(citation, "url") and citation.url, "Annotation should have non-empty URL"
        assert hasattr(citation, "title"), "Annotation should have title"
    else:
        raise ValueError(f"Unknown sdk_type: {sdk_type}")


def assert_web_search_sources_valid(sources):
    """
    Validate web search sources structure.
    
    Args:
        sources: List of source objects to validate
    """
    assert sources is not None, "Sources should not be None"
    assert len(sources) > 0, "Sources should not be empty"
    
    for i, source in enumerate(sources):
        assert hasattr(source, "url"), f"Source {i} should have url"
        assert source.url, f"Source {i} url should not be empty"
        assert hasattr(source, "title"), f"Source {i} should have title"
        # encrypted_content and page_age are optional


def extract_domain(url: str) -> str:
    """
    Extract domain from URL for validation.
    
    Args:
        url: Full URL string
        
    Returns:
        Domain string (e.g., "en.wikipedia.org")
    """
    from urllib.parse import urlparse
    parsed = urlparse(url)
    return parsed.netloc.lower()


def validate_domain_filter(sources, allowed=None, blocked=None):
    """
    Validate sources respect domain filters.
    
    Args:
        sources: List of source objects with url attribute
        allowed: List of allowed domain patterns (optional)
        blocked: List of blocked domain patterns (optional)
    """
    for source in sources:
        if not hasattr(source, "url"):
            continue
            
        domain = extract_domain(source.url)
        
        if allowed:
            # Check if domain matches any allowed pattern
            matches_allowed = False
            for allowed_pattern in allowed:
                # Handle subdomains: example.com should match docs.example.com
                if domain == allowed_pattern or domain.endswith('.' + allowed_pattern):
                    matches_allowed = True
                    break
                # Handle subdomain pattern: docs.example.com matches exactly
                if allowed_pattern == domain:
                    matches_allowed = True
                    break
            
            assert matches_allowed, f"Domain {domain} not in allowed domains {allowed}"
        
        if blocked:
            # Check if domain matches any blocked pattern
            for blocked_pattern in blocked:
                is_blocked = (domain == blocked_pattern or 
                            domain.endswith('.' + blocked_pattern))
                assert not is_blocked, f"Domain {domain} should be blocked by {blocked_pattern}"