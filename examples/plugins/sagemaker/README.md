# SageMaker Plugin

This native Go plugin adds IAM-authenticated Amazon SageMaker inference to
Bifrost. Bifrost does not include a SageMaker provider or SigV4-authenticated
SageMaker transport by default.

```text
OpenAI SDK or application
  -> Bifrost OpenAI-compatible API
  -> SageMaker plugin
  -> AWS SDK InvokeEndpoint with workload IAM credentials and SigV4
  -> SageMaker endpoint
```

The direct path adds no API Gateway, Lambda, or other runtime hop. The plugin
uses the AWS SDK default credential chain, so Bifrost can authenticate with its
workload role instead of storing AWS access keys in plugin configuration.

## Supported contract

SageMaker model containers define their own request and response formats. This
plugin targets containers that implement OpenAI-compatible JSON for these
non-streaming Bifrost operations:

| Bifrost operation | OpenAI-compatible path |
| --- | --- |
| Text completion | `/v1/completions` |
| Chat completion | `/v1/chat/completions` |
| Responses | `/v1/responses` |
| Embeddings | `/v1/embeddings` |
| Rerank | `/v1/rerank` |

The plugin uses Bifrost's existing OpenAI request converters, invokes the
configured SageMaker endpoint, validates the response shape, and returns the
matching typed Bifrost response. The implementation is model-agnostic: model
names and endpoint routing live entirely in configuration.

Streaming is intentionally not included in this example. Supporting it
correctly requires `InvokeEndpointWithResponseStream`, AWS event-stream
handling, cancellation, terminal-event validation, and Bifrost stream chunks.

## Build and test

Native Go plugins must match the target Bifrost binary's operating system,
architecture, Go toolchain, libc, source packages, and complete Go workspace
module graph. Building the host and plugin separately can fail at load time
with `plugin was built with a different version of package ...` even when both
use the same checkout.

Build Bifrost and the plugin from the same root `go.work`:

```bash
make -C examples/plugins/sagemaker test
make -C examples/plugins/sagemaker verify-load
```

The plugin is written to `examples/plugins/sagemaker/build/sagemaker.so`.
`verify-load` builds a small ABI host and the plugin from the same workspace
with identical `-trimpath` settings, then opens the plugin and type-checks its
required Bifrost hooks. Build the actual Bifrost host and plugin inside the same
target OS/libc environment for deployment.

## IAM

Grant the Bifrost workload role permission to invoke only the required
endpoint or endpoints:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": "sagemaker:InvokeEndpoint",
      "Resource": [
        "arn:aws:sagemaker:us-east-1:123456789012:endpoint/openai-compatible-endpoint"
      ]
    }
  ]
}
```

No static AWS credentials are required in `config.json`.

## Configure Bifrost

Create a custom provider for the operations exposed by your SageMaker
container. The non-routable base URL is a fail-closed placeholder: matching
requests are short-circuited by the plugin before Bifrost makes a normal
provider HTTP call.

```json
{
  "providers": {
    "sagemaker": {
      "keys": [
        {
          "id": "sagemaker-iam",
          "name": "sagemaker-iam",
          "value": "iam-role",
          "models": ["*"],
          "weight": 1
        }
      ],
      "network_config": {
        "base_url": "http://sagemaker-plugin-only.invalid"
      },
      "custom_provider_config": {
        "base_provider_type": "openai",
        "allowed_requests": {
          "text_completion": true,
          "chat_completion": true,
          "responses": true,
          "embedding": true,
          "rerank": true
        }
      }
    }
  },
  "plugins": [
    {
      "enabled": true,
      "name": "sagemaker",
      "path": "/absolute/path/to/sagemaker.so",
      "config": {
        "provider": "sagemaker",
        "region": "us-east-1",
        "timeout_seconds": 60,
        "endpoints": {
          "*": {
            "endpoint_name": "openai-compatible-endpoint"
          }
        }
      }
    }
  ]
}
```

An exact model mapping takes precedence over `*`:

```json
{
  "endpoints": {
    "*": {
      "endpoint_name": "shared-openai-compatible-endpoint"
    },
    "dedicated-model": {
      "endpoint_name": "dedicated-endpoint",
      "target_variant": "blue"
    }
  }
}
```

Endpoint mappings also accept optional `target_model`, `target_variant`,
`inference_component_name`, and `custom_attributes` fields. These map directly
to SageMaker `InvokeEndpoint` options for multi-model endpoints, production
variants, inference components, and container-defined request metadata.

## Call through the OpenAI SDK

```python
from openai import OpenAI

client = OpenAI(
    base_url="https://bifrost.example.com/openai",
    api_key="your-bifrost-virtual-key",
)

response = client.chat.completions.create(
    model="sagemaker/your-model",
    messages=[{"role": "user", "content": "Hello"}],
)
print(response.choices[0].message.content)
```

The same Bifrost provider prefix works with supported embeddings, Responses,
text completion, and rerank clients.

## Adding models and endpoint types

- Another model on an endpoint with the same OpenAI-compatible contract is a
  configuration-only change.
- A dedicated SageMaker endpoint is another endpoint mapping and IAM resource.
- A SageMaker multi-model endpoint can use `target_model`.
- A model container with a different JSON contract requires a request/response
  adapter and focused tests, but the IAM/SigV4 invocation layer is reusable.
- A streaming model requires the separate streaming lifecycle described above.

## Error and fallback behavior

- Unsupported operations and unmapped models return 4xx errors and do not
  trigger fallbacks.
- SageMaker invocation failures, timeouts, and invalid endpoint responses return
  5xx errors with Bifrost fallbacks enabled.
- AWS SDK error details are recorded in plugin-scoped logs but are not exposed
  in client responses.

## Independent validation

The IAM/SigV4 flow was independently validated in an AWS account on August 31,
2026 with the current Bifrost `dev` branch on AWS App Runner and
`sentence-transformers/all-MiniLM-L6-v2` on SageMaker Serverless Inference.
An OpenAI Python SDK 3.3.1 embeddings call returned HTTP 200, one normalized
384-dimensional vector, and valid usage. A warm SDK invocation completed in
1.3 seconds. Direct SageMaker and Bifrost results had a maximum vector delta of
`0.0`.

That model is validation evidence, not a plugin dependency. The code contains
no model-specific names, dimensions, or inference logic.

The validation deployment contained no API Gateway or Lambda between Bifrost
and SageMaker. Account identifiers, endpoint URLs, and credentials are not
included in this example.
