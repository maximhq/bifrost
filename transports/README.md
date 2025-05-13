# Bifrost Transports

This package contains clients for various transports that can be used to spin up your Bifrost client with just a single line of code.

## 📑 Table of Contents

- [Bifrost Transports](#bifrost-transports)
  - [📑 Table of Contents](#-table-of-contents)
  - [🚀 Setting Up Transports](#-setting-up-transports)
    - [Prerequisites](#prerequisites)
    - [Configuration](#configuration)
    - [Docker Setup](#docker-setup)
    - [Go Setup](#go-setup)
  - [🧰 Usage](#-usage)
    - [Text Completions](#text-completions)
    - [Chat Completions](#chat-completions)
  - [🔧 Advanced Features](#-advanced-features)
    - [Prometheus Support](#prometheus-support)
    - [Plugin Support](#plugin-support)
    - [Fallbacks](#fallbacks)

---

## 🚀 Setting Up Transports

### Prerequisites

- Go 1.23 or higher (if not using Docker)
- Access to at least one AI model provider (OpenAI, Anthropic, etc.)
- API keys for the providers you wish to use

### Configuration

Bifrost uses a combination of a JSON configuration file and environment variables:

1. **JSON Configuration File**: Bifrost requires a configuration file to set up the gateway. This includes all your provider-level settings, keys, and meta configs for each of your providers.
2. **Environment Variables**: If you don't want to include your keys in your config file, you can provide a `.env` file and add a prefix of `env.` followed by its key in your `.env` file.

```json
{
  "keys": [
    {
      "value": "env.OPENAI_API_KEY",
      "models": ["gpt-4o-mini", "gpt-4-turbo"],
      "weight": 1.0
    }
  ]
}
```

In this example, `OPENAI_API_KEY` refers to a key in the `.env` file. At runtime, its value will be used to replace the placeholder.

The same setup applies to keys in meta configs of all providers:

```json
{
  "meta_config": {
    "secret_access_key": "env.BEDROCK_ACCESS_KEY",
    "region": "env.BEDROCK_REGION"
  }
}
```

In this example, `BEDROCK_ACCESS_KEY` and `BEDROCK_REGION` refer to keys in the `.env` file.

Please refer to `config.example.json` and `.env.sample` for examples.

### Docker Setup

1. Download the Dockerfile:

```bash
curl -L -o Dockerfile https://raw.githubusercontent.com/maximhq/bifrost/main/transports/Dockerfile
```

2. Build the Docker image:

```bash
docker build \
  --build-arg CONFIG_PATH=./config.example.json \
  --build-arg ENV_PATH=./.env.sample \
  --build-arg PORT=8080 \
  --build-arg POOL_SIZE=300 \
  -t bifrost-transports .
```

3. Run the Docker container:

```bash
docker run --name bifrost-api -p 8080:8080 bifrost-transports
```

You can also add a flag for `DROP_EXCESS_REQUESTS=false` in your Docker build command to drop excess requests when the buffer is full. Read more about `DROP_EXCESS_REQUESTS` and `POOL_SIZE` [here](https://github.com/maximhq/bifrost/tree/main?tab=README-ov-file#additional-configurations).

---

### Go Setup

If you wish to run Bifrost in your Go environment, follow these steps:

1. Install your binary:

```bash
go install github.com/maximhq/bifrost/transports/bifrost-http@latest
```

2. Run your binary:

- If it's in your PATH:

```bash
bifrost-http -config config.json -env .env -port 8080 -pool-size 300
```

- Otherwise:

```bash
./bifrost-http -config config.json -env .env -port 8080 -pool-size 300
```

You can also add a flag for `-drop-excess-requests=false` in your command to drop excess requests when the buffer is full. Read more about `DROP_EXCESS_REQUESTS` and `POOL_SIZE` [here](https://github.com/maximhq/bifrost/tree/main?tab=README-ov-file#additional-configurations).

## 🧰 Usage

Ensure that:

- Bifrost's HTTP server is running
- The providers/models you use are configured in your JSON config file

### Text Completions

```bash
# Make sure to setup anthropic and claude-2.1 in your config.json
curl -X POST http://localhost:8080/v1/text/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "anthropic",
    "model": "claude-2.1",
    "text": "Once upon a time in the land of AI,",
    "params": {
      "temperature": 0.7,
      "max_tokens": 100
    }
  }'
```

### Chat Completions

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "Tell me about Bifrost in Norse mythology."}
    ],
    "params": {
      "temperature": 0.8,
      "max_tokens": 500
    }
  }'
```

---

## 🔧 Advanced Features

### Prometheus Support

HTTP transport supports Prometheus out of the box. By default all the metrics are available at `/metrics` endpoint. It providers metrics for httpRequestsTotal, httpRequestDuration, httpRequestSizeBytes, httpResponseSizeBytes, bifrostUpstreamRequestsTotal, and bifrostUpstreamLatencySeconds. To add custom labels to these metrics using can pass a flag of `-prometheus-labels` while running the http transport.

For eg. `-prometheus-labels team-id,task-id,location`

Values for labels are then picked up from the HTTP request headers with the prefix `x-bf-prom-`.

### Plugin Support

You can explore the available plugins [here](https://github.com/maximhq/bifrost/tree/main/plugins). And to attached these plugins to your HTTP transport, just pass the flag `-plugins`.

For eg. `-plugins maxim`

Note: Please check plugin specific documentations (github.com/maximhq/bifrost/tree/main/plugins/{plugin_name}) for more nuanced control.

### Fallbacks

Configure fallback options in your requests:

```json
{
  "provider": "openai",
  "model": "gpt-4",
  "messages": [...],
  "fallbacks": [
    {
      "provider": "anthropic",
      "model": "claude-3-opus-20240229"
    },
    {
      "provider": "bedrock",
      "model": "anthropic.claude-3-sonnet-20240229-v1:0"
    }
  ]
}
```

Read more about fallbacks and other additional configurations [here](https://github.com/maximhq/bifrost/tree/main?tab=README-ov-file#additional-configurations).

---

Built with ❤️ by [Maxim](https://github.com/maximhq)
