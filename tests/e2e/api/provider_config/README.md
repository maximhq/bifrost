# Provider config (Postman env files)

Per-provider Postman environment `.json` files for running the Bifrost V1 API Newman e2e tests. Each file defines `base_url`, `provider`, `model`, and other model-type variables for that provider.

## Variables

Each `bifrost-v1-<provider>.postman_environment.json` typically includes:

| Key | Description |
|-----|-------------|
| `base_url` | Gateway base URL (default `http://localhost:8080`) |
| `provider` | Provider name (e.g. `openai`, `anthropic`, `gemini`) |
| `model` | Chat/completions model |
| `embedding_model` | Embeddings model |
| `speech_model` | TTS model |
| `transcription_model` | Transcription model |
| `image_model` | Image generation model |
| `batch_id`, `file_id`, `container_id` | Placeholders; overwritten at runtime when tests create resources |

## Usage

From `tests/e2e/api`:

```bash
# Run for all providers (each bifrost-v1-*.postman_environment.json in this folder, except sgl and ollama)
./run-newman-tests.sh

# Run for a single provider
./run-newman-tests.sh --env openai
./run-newman-tests.sh --env provider_config/bifrost-v1-openai.postman_environment.json
```

Ensure the Bifrost server is running and the chosen provider(s) are configured (API keys, etc.) so the requests succeed.

## Files

All Bifrost providers are included except **sgl** and **ollama** (excluded in `run-newman-tests.sh` when running “all providers”).

- `bifrost-v1-openai.postman_environment.json`
- `bifrost-v1-anthropic.postman_environment.json`
- `bifrost-v1-azure.postman_environment.json`
- `bifrost-v1-bedrock.postman_environment.json`
- `bifrost-v1-cerebras.postman_environment.json`
- `bifrost-v1-cohere.postman_environment.json`
- `bifrost-v1-elevenlabs.postman_environment.json`
- `bifrost-v1-gemini.postman_environment.json`
- `bifrost-v1-groq.postman_environment.json`
- `bifrost-v1-huggingface.postman_environment.json`
- `bifrost-v1-mistral.postman_environment.json`
- `bifrost-v1-nebius.postman_environment.json`
- `bifrost-v1-openrouter.postman_environment.json`
- `bifrost-v1-parasail.postman_environment.json`
- `bifrost-v1-perplexity.postman_environment.json`
- `bifrost-v1-vertex.postman_environment.json`
- `bifrost-v1-xai.postman_environment.json`

To add a provider, copy an existing env file, rename it to `bifrost-v1-<provider>.postman_environment.json`, and set the `provider` and model values for that provider.
