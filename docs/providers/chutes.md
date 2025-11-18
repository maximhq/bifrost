# Chutes.ai Provider Documentation

## Overview

The Chutes.ai provider enables Bifrost to integrate with Chutes.ai's serverless AI platform. This provider supports chat completions, text completions, and embeddings using OpenAI-compatible API patterns.

**Status**: ✅ **Production Ready** - Fully implemented and validated

## Quick Setup

```yaml
# Bifrost Configuration
providers:
  chutes:
    api_key: "your-chutes-api-key"
    models:
      - "deepseek-ai/DeepSeek-R1"
      - "Qwen/Qwen3-32B"
```

## API Authentication

### Obtaining API Keys

1. Visit [Chutes.ai Dashboard](https://chutes.ai/app)
2. Navigate to API Keys section
3. Generate new API key
4. Set environment variable:

```bash
export CHUTES_API_KEY="your-api-key-here"
```

### Authentication Format

Chutes.ai uses **Bearer token authentication**:

```http
Authorization: Bearer <your-api-key>
```

## Available Models

Chutes.ai provides access to 52+ models including:

### Popular Models
- `deepseek-ai/DeepSeek-R1` - High-performance reasoning model
- `deepseek-ai/DeepSeek-V3.1` - Latest DeepSeek model
- `Qwen/Qwen3-32B` - Efficient general purpose model
- `Qwen/Qwen3-235B-A22B-Instruct-2507` - Large instruction model
- `moonshotai/Kimi-K2-Instruct-0905` - Conversation model

### Model Discovery

View all available models:

```bash
curl -X GET "https://llm.chutes.ai/v1/models" \
  -H "Authorization: Bearer $CHUTES_API_KEY" \
  -H "Content-Type: application/json"
```

## Configuration Examples

### Basic Chat Provider

```yaml
providers:
  chutes:
    api_key: "${CHUTES_API_KEY}"
    base_url: "https://llm.chutes.ai"  # Optional override
    models: ["deepseek-ai/DeepSeek-R1"]
    max_retries: 3
    timeout: 30
```

### Multi-Model Setup with Fallbacks

```yaml
providers:
  chutes:
    api_key: "${CHUTES_API_KEY}"
    models:
      primary: "deepseek-ai/DeepSeek-R1"
      fallback: "Qwen/Qwen3-32B"
    fallbacks:
      - provider: chutes
        model: "deepseek-ai/DeepSeek-V3.1"
```

### Embeddings Provider

```yaml
providers:
  chutes:
    api_key: "${CHUTES_API_KEY}"
    models:
      chat: "deepseek-ai/DeepSeek-R1"
      embedding: "deepseek-ai/DeepSeek-R1"
```

## Supported Operations

| Operation | Support | Notes |
|------------|----------|---------|
| ChatCompletion | ✅ | Full streaming and non-streaming support |
| TextCompletion | ✅ | OpenAI-compatible completions |
| Embedding | ✅ | Standard OpenAI embeddings format |
| ListModels | ✅ | Model discovery and pagination |
| Speech | ❌ | Not supported by platform |
| Transcription | ❌ | Not supported by platform |
| Image Processing | ❌ | Not supported by platform |

## API Endpoints

Chutes.ai uses OpenAI-compatible endpoints:

### Base URL
```
https://llm.chutes.ai
```

### Endpoint Paths
- **Chat Completions**: `/v1/chat/completions`
- **Text Completions**: `/v1/completions`
- **Embeddings**: `/v1/embeddings`
- **Models**: `/v1/models`

## Request Examples

### Chat Completion

```bash
curl -X POST "https://llm.chutes.ai/v1/chat/completions" \
  -H "Authorization: Bearer $CHUTES_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-ai/DeepSeek-R1",
    "messages": [
      {"role": "user", "content": "Hello, how are you?"}
    ],
    "max_tokens": 100,
    "temperature": 0.7
  }'
```

### Embeddings

```bash
curl -X POST "https://llm.chutes.ai/v1/embeddings" \
  -H "Authorization: Bearer $CHUTES_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-ai/DeepSeek-R1",
    "input": "Hello world"
  }'
```

## Error Handling

### Common Errors and Solutions

#### Model Not Found
```json
{"detail": "model not found: chutes/deepseek-ai/DeepSeek-R1"}
```
**Solution**: Use exact model names from `/v1/models` endpoint

#### Authentication Failed
```json
{"error": "Invalid API key"}
```
**Solution**: Verify API key is correct and has proper permissions

#### Rate Limited
```json
{"error": "Rate limit exceeded"}
```
**Solution**: Implement retry logic with exponential backoff

## Integration with Bifrost

### Programmatic Configuration

```go
package main

import (
    "github.com/maximhq/bifrost"
    "github.com/maximhq/bifrost/core/schemas"
)

func main() {
    bifrostConfig := &schemas.BifrostConfig{
        Account: schemas.Account{
            Providers: map[schemas.ModelProvider]*schemas.ProviderConfig{
                schemas.Chutes: {
                    APIKeys: []schemas.Key{
                        {Value: os.Getenv("CHUTES_API_KEY")},
                    },
                    NetworkConfig: schemas.NetworkConfig{
                        BaseURL: "https://llm.chutes.ai",
                    },
                },
            },
        },
    }

    bifrost, err := bifrost.NewBifrost(bifrostConfig)
    if err != nil {
        panic(err)
    }

    // Use Chutes.ai provider
    response, bifrostError := bifrost.ChatCompletion(context.Background(),
        &schemas.BifrostChatRequest{
            Model: "deepseek-ai/DeepSeek-R1",
            Messages: []schemas.ChatMessage{
                {Role: "user", Content: "Hello!"},
            },
        })

    if bifrostError != nil {
        fmt.Printf("Error: %v\n", bifrostError)
    } else {
        fmt.Printf("Response: %s\n", response.Choices[0].Message.Content)
    }
}
```

## Testing and Validation

### Unit Tests

Run the comprehensive test suite:

```bash
cd tests/core-providers
go test -run TestChutes -v
```

### Manual API Testing

Verify connectivity and model availability:

```bash
# Test list models
curl -X GET "https://llm.chutes.ai/v1/models" \
  -H "Authorization: Bearer $CHUTES_API_KEY" \
  -s | jq '.data[0:3] | {id, created}'

# Test chat completion
curl -X POST "https://llm.chutes.ai/v1/chat/completions" \
  -H "Authorization: Bearer $CHUTES_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-ai/DeepSeek-R1","messages":[{"role":"user","content":"test"}]}'
```

## Production Deployment

### Environment Variables

```bash
# Required
export CHUTES_API_KEY="your-production-api-key"

# Optional
export CHUTES_BASE_URL="https://llm.chutes.ai"
export CHUTES_TIMEOUT="30"
export CHUTES_MAX_RETRIES="3"
```

### Monitoring and Metrics

Monitor Chutes.ai provider performance:

- Request success rates
- Latency measurements
- Error rate tracking
- Token usage statistics

### Health Checks

Implement provider health verification:

```go
// Health check for Chutes.ai
func checkChutesHealth(apiKey string) error {
    client := &http.Client{Timeout: 10 * time.Second}
    req, _ := http.NewRequest("GET", "https://llm.chutes.ai/v1/models", nil)
    req.Header.Set("Authorization", "Bearer "+apiKey)

    resp, err := client.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    if resp.StatusCode == 200 {
        return nil
    }
    return fmt.Errorf("health check failed with status: %d", resp.StatusCode)
}
```

## Troubleshooting

### Debug Mode

Enable detailed logging:

```yaml
providers:
  chutes:
    api_key: "${CHUTES_API_KEY}"
    debug: true
    log_level: "debug"
```

### Common Issues

1. **Model Name Mismatches**: Always use exact model IDs from `/v1/models`
2. **Network Timeouts**: Increase timeout for large models
3. **Rate Limits**: Implement proper retry logic
4. **Invalid Responses**: Check model availability before requests

## Performance Considerations

### Model Selection

- **deepseek-ai/DeepSeek-R1**: Best for reasoning and code tasks
- **Qwen/Qwen3-32B**: Good balance of performance and cost
- **deepseek-ai/DeepSeek-V3.1**: Latest model with best reasoning

### Cost Optimization

- Use appropriate model sizes for tasks
- Monitor token usage closely
- Implement response caching where suitable
- Set reasonable `max_tokens` limits

### Performance Tuning

```yaml
providers:
  chutes:
    api_key: "${CHUTES_API_KEY}"
    performance:
      timeout: 60
      max_retries: 3
      retry_backoff: "exponential"
      buffer_size: 1000
```

## Security Best Practices

1. **API Key Management**
   ```bash
   # Use environment variables, never hardcode keys
   export CHUTES_API_KEY="key-from-secure-source"

   # Rotate keys regularly
   # Use different keys for development vs production
   ```

2. **Request Security**
   - Always validate input parameters
   - Sanitize user content
   - Use HTTPS endpoints only
   - Implement proper error handling

3. **Access Control**
   - Implement proper API key scopes
   - Monitor for unusual usage patterns
   - Use IP whitelisting if available

## Support and Resources

### Documentation
- [Chutes.ai Official Documentation](https://chutes.ai/docs)
- [API Reference](https://chutes.ai/docs/api-reference)
- [Model Catalog](https://llm.chutes.ai/v1/models)

### Community Support
- [GitHub Issues](https://github.com/chutesai/chutes-api/issues)
- [Discord Community](https://discord.gg/chutes)
- [Developer Forums](https://github.com/chutesai/chutes-api/discussions)

---

**Last Updated**: November 18, 2025
**Provider Version**: 1.0.0
**Compatibility**: Bifrost v2.x+