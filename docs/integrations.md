# Bifrost Integrations Guide

This guide shows how to use popular AI framework SDKs (LangChain, LangGraph, LiteLLM) with Bifrost by simply changing the base URL. This allows you to leverage all the benefits of Bifrost (fallbacks, load balancing, unified error handling) while using the familiar SDKs you already know.

## Overview

Bifrost provides integration endpoints that are compatible with popular AI framework SDKs:

- **LangChain**: Compatible with `langchain-openai` and other LangChain providers
- **LangGraph**: Full compatibility with LangGraph workflows and state management
- **LiteLLM**: Direct OpenAI-compatible proxy for 100+ models
- **Direct Provider SDKs**: OpenAI, Anthropic, Google, Mistral SDKs

## Quick Start

### 1. LangChain Integration

Use your existing LangChain code with Bifrost by changing the `base_url`:

```python
# Before: Direct OpenAI
from langchain_openai import ChatOpenAI

llm = ChatOpenAI(
    model="gpt-4o",
    api_key="your-openai-key"
)

# After: Through Bifrost
llm = ChatOpenAI(
    model="gpt-4o",
    api_key="your-openai-key",
    base_url="http://localhost:8080/integrations/langchain"  # Bifrost LangChain endpoint
)
```

**Complete LangChain Example:**

```python
from langchain_openai import ChatOpenAI
from langchain_core.messages import HumanMessage
import os

# Configure your API keys
os.environ["OPENAI_API_KEY"] = "your-openai-key"

# Create LangChain client pointing to Bifrost
llm = ChatOpenAI(
    model="gpt-4o",
    base_url="http://localhost:8080/integrations/langchain",
    temperature=0.7
)

# Use normally - all LangChain features work
messages = [HumanMessage(content="Hello, how are you?")]
response = llm.invoke(messages)
print(response.content)

# Streaming works too
for chunk in llm.stream(messages):
    print(chunk.content, end="", flush=True)

# Tool calling, batch processing, etc. all work as expected
```

**LangChain with Multiple Providers:**

```python
from langchain_openai import ChatOpenAI
from langchain_anthropic import ChatAnthropic

# OpenAI through Bifrost
openai_llm = ChatOpenAI(
    model="gpt-4o",
    base_url="http://localhost:8080/integrations/langchain"
)

# Anthropic through Bifrost
anthropic_llm = ChatAnthropic(
    model="claude-3-opus-20240229",
    base_url="http://localhost:8080/integrations/langchain"
)

# Both get Bifrost's benefits: fallbacks, load balancing, etc.
```

### 2. LangGraph Integration

LangGraph workflows work seamlessly with Bifrost endpoints:

```python
from langgraph.prebuilt import create_react_agent
from langchain_openai import ChatOpenAI
from langchain_core.tools import tool

# Define a tool
@tool
def get_weather(location: str) -> str:
    """Get the weather for a location."""
    return f"The weather in {location} is sunny."

# Create LLM with Bifrost endpoint
llm = ChatOpenAI(
    model="gpt-4o",
    base_url="http://localhost:8080/integrations/langgraph"  # LangGraph-specific endpoint
)

# Create agent normally
agent = create_react_agent(llm, [get_weather])

# Run the agent
response = agent.invoke({
    "messages": [{"role": "user", "content": "What's the weather in Paris?"}]
})
print(response["messages"][-1].content)
```

**Advanced LangGraph with State Management:**

```python
from langgraph.graph import StateGraph, MessagesState
from langchain_openai import ChatOpenAI
from langchain_core.messages import HumanMessage

# Define LLM with Bifrost
llm = ChatOpenAI(
    model="gpt-4o",
    base_url="http://localhost:8080/integrations/langgraph"
)

# Define your graph nodes
def chatbot(state: MessagesState):
    return {"messages": [llm.invoke(state["messages"])]}

def human_feedback(state: MessagesState):
    # Add human-in-the-loop logic
    pass

# Build the graph
graph = StateGraph(MessagesState)
graph.add_node("chatbot", chatbot)
graph.add_node("human", human_feedback)
graph.set_entry_point("chatbot")

# Compile and run
app = graph.compile()
result = app.invoke({"messages": [HumanMessage(content="Hello")]})
```

### 3. LiteLLM Integration

LiteLLM provides the most direct integration - just change the base URL:

```python
import litellm

# Before: Direct to OpenAI
response = litellm.completion(
    model="gpt-4o",
    messages=[{"role": "user", "content": "Hello"}]
)

# After: Through Bifrost
response = litellm.completion(
    model="gpt-4o",
    messages=[{"role": "user", "content": "Hello"}],
    api_base="http://localhost:8080/integrations/litellm"  # Bifrost LiteLLM endpoint
)
```

**LiteLLM with Multiple Providers:**

```python
import litellm

# Configure base URL globally
litellm.api_base = "http://localhost:8080/integrations/litellm"

# Now all calls go through Bifrost
openai_response = litellm.completion(
    model="gpt-4o",
    messages=[{"role": "user", "content": "Hello from OpenAI"}]
)

anthropic_response = litellm.completion(
    model="claude-3-opus-20240229",
    messages=[{"role": "user", "content": "Hello from Claude"}]
)

# Embeddings work too
embeddings = litellm.embedding(
    model="text-embedding-3-small",
    input=["Hello world"]
)
```

**LiteLLM Router with Bifrost:**

```python
import litellm
from litellm import Router

# Create router with Bifrost endpoints
model_list = [
    {
        "model_name": "gpt-4o",
        "litellm_params": {
            "model": "gpt-4o",
            "api_base": "http://localhost:8080/integrations/litellm"
        }
    },
    {
        "model_name": "claude-3",
        "litellm_params": {
            "model": "claude-3-opus-20240229",
            "api_base": "http://localhost:8080/integrations/litellm"
        }
    }
]

router = Router(model_list=model_list)

# Router handles load balancing, Bifrost handles provider fallbacks
response = router.completion(
    model="gpt-4o",
    messages=[{"role": "user", "content": "Hello"}]
)
```

### 4. Direct Provider SDKs

You can also use provider SDKs directly with Bifrost endpoints:

**OpenAI Python SDK:**

```python
from openai import OpenAI

# Create client pointing to Bifrost
client = OpenAI(
    api_key="your-openai-key",
    base_url="http://localhost:8080/integrations/openai"
)

# Use normally
response = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "Hello"}]
)

print(response.choices[0].message.content)
```

**Anthropic SDK:**

```python
import anthropic

# Create client pointing to Bifrost
client = anthropic.Anthropic(
    api_key="your-anthropic-key",
    base_url="http://localhost:8080/integrations/anthropic"
)

# Use normally
response = client.messages.create(
    model="claude-3-opus-20240229",
    max_tokens=1000,
    messages=[{"role": "user", "content": "Hello"}]
)

print(response.content[0].text)
```

## Configuration

### Environment Variables

Set your provider API keys as usual:

```bash
export OPENAI_API_KEY="your-openai-key"
export ANTHROPIC_API_KEY="your-anthropic-key"
export GOOGLE_API_KEY="your-google-key"
export MISTRAL_API_KEY="your-mistral-key"
```

### Bifrost Configuration

Configure Bifrost with your providers in a JSON configuration file. Create a `config.json` file with your provider settings:

```json
{
  "OpenAI": {
    "keys": [
      {
        "value": "env.OPENAI_API_KEY",
        "models": ["gpt-4o", "gpt-4o-mini", "gpt-3.5-turbo"],
        "weight": 1.0
      }
    ],
    "network_config": {
      "default_request_timeout_in_seconds": 30,
      "max_retries": 3,
      "retry_backoff_initial_ms": 100,
      "retry_backoff_max_ms": 2000
    },
    "concurrency_and_buffer_size": {
      "concurrency": 5,
      "buffer_size": 10
    }
  },
  "Anthropic": {
    "keys": [
      {
        "value": "env.ANTHROPIC_API_KEY",
        "models": ["claude-3-5-sonnet-20240620", "claude-3-haiku-20240307"],
        "weight": 1.0
      }
    ],
    "network_config": {
      "default_request_timeout_in_seconds": 30,
      "max_retries": 3
    }
  },
  "Bedrock": {
    "keys": [
      {
        "value": "env.BEDROCK_API_KEY",
        "models": ["anthropic.claude-3-sonnet-20240229-v1:0"],
        "weight": 1.0
      }
    ],
    "meta_config": {
      "secret_access_key": "env.BEDROCK_ACCESS_KEY",
      "region": "us-east-1"
    }
  },
  "Azure": {
    "keys": [
      {
        "value": "env.AZURE_API_KEY",
        "models": ["gpt-4o"],
        "weight": 1.0
      }
    ],
    "meta_config": {
      "endpoint": "env.AZURE_ENDPOINT",
      "deployments": {
        "gpt-4o": "gpt-4o-deployment-name"
      },
      "api_version": "2024-08-01-preview"
    }
  }
}
```

**Environment Variables Setup:**

Set the corresponding environment variables referenced in your config:

```bash
export OPENAI_API_KEY="your-openai-key"
export ANTHROPIC_API_KEY="your-anthropic-key"
export BEDROCK_API_KEY="your-bedrock-key"
export BEDROCK_ACCESS_KEY="your-aws-access-key"
export AZURE_API_KEY="your-azure-key"
export AZURE_ENDPOINT="https://your-resource.openai.azure.com/"
```

**Running Bifrost HTTP Server:**

```bash
# Start the server with your configuration
go run transports/bifrost-http/main.go \
  -config config.json \
  -port 8080 \
  -pool-size 300
```

**Configuration Options:**

- **Keys**: Multiple API keys per provider with model restrictions and load balancing weights
- **Network Config**: Timeout settings, retry policies, and backoff strategies
- **Meta Config**: Provider-specific settings (AWS regions, Azure endpoints, etc.)
- **Concurrency**: Control request concurrency and buffer sizes per provider

**Load Balancing:**

Bifrost automatically load balances requests across multiple keys for the same provider based on the `weight` parameter and model availability.

## Integration Endpoints

Bifrost provides these integration endpoints:

| Framework | Endpoint                  | Purpose                            |
| --------- | ------------------------- | ---------------------------------- |
| LangChain | `/integrations/langchain` | LangChain SDK compatibility        |
| LangGraph | `/integrations/langgraph` | LangGraph workflows and agents     |
| LiteLLM   | `/integrations/litellm`   | LiteLLM proxy compatibility        |
| OpenAI    | `/integrations/openai`    | Direct OpenAI SDK compatibility    |
| Anthropic | `/integrations/anthropic` | Direct Anthropic SDK compatibility |
| Google    | `/integrations/genai`     | Google GenAI SDK compatibility     |
| Mistral   | `/integrations/mistral`   | Mistral SDK compatibility          |

## Benefits

By using SDKs with Bifrost endpoints, you get:

### 🔄 **Automatic Fallbacks**

- If one provider fails, Bifrost automatically tries configured fallbacks
- No code changes needed - handled transparently

### ⚖️ **Load Balancing**

- Distribute requests across multiple provider instances
- Reduce rate limiting and improve reliability

### 📊 **Unified Monitoring**

- All requests flow through Bifrost for consistent logging and metrics
- Track usage, costs, and performance across all providers

### 🛡️ **Error Handling**

- Standardized error handling and retry logic
- Graceful degradation when providers are unavailable

### 💰 **Cost Optimization**

- Route to cost-effective alternatives based on your rules
- Track spending across all providers in one place

## Migration

### From Direct Provider Calls

```python
# Before
from openai import OpenAI
client = OpenAI(api_key="...")

# After
from openai import OpenAI
client = OpenAI(
    api_key="...",
    base_url="http://localhost:8080/integrations/openai"
)
```

### From LangChain

```python
# Before
from langchain_openai import ChatOpenAI
llm = ChatOpenAI(model="gpt-4o")

# After
from langchain_openai import ChatOpenAI
llm = ChatOpenAI(
    model="gpt-4o",
    base_url="http://localhost:8080/integrations/langchain"
)
```

### From LiteLLM

```python
# Before
import litellm
response = litellm.completion(model="gpt-4o", ...)

# After
import litellm
litellm.api_base = "http://localhost:8080/integrations/litellm"
response = litellm.completion(model="gpt-4o", ...)
```
