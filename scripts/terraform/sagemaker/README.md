# SageMaker endpoints for the Bifrost `sagemaker` provider

Stands up one SageMaker real-time endpoint so the planned `sagemaker` provider can be built and tested against SageMaker's real routing layer: bearer-token auth, the `/openai/v1` route, `/invocations`, event-stream framing, `ModelError` wrapping and header stripping. A RunPod pod (`../runpod`) only exercises the container, not any of that.

One `apply` gives you:

- an endpoint running the chosen engine and model preset;
- a smoke test through the real runtime host;
- a ready-to-paste `providers.sagemaker` config block;
- a scheduler that deletes the endpoint after `teardown_after_minutes`.

## Usage

```bash
aws login --profile <name> && export AWS_PROFILE=<name>   # needs deployer-policy.json; an admin creates two roles once (Prerequisites)
cp terraform.tfvars.example terraform.tfvars    # pick an engine and a model
terraform init
terraform apply                                 # ~5-15 min: blocks until InService, then runs probe.py --quick

terraform output probe_command                  # full probe of every wire path; paste and run it
terraform output -json bifrost_provider_config  # providers.sagemaker entry for config.json
terraform output bifrost_model_id               # sagemaker/<endpoint>[/<inference-component>]
terraform destroy                               # or let the scheduler delete the endpoint (60 min default)
```

Before the first apply, check your quotas (see [Prerequisites](#prerequisites)). On a new account the GPU instance quotas are usually 0.

## Engines

| `engine` | Image (tag as of 2026-10-23) | Config dialect | Bifrost `api` | Stream on `/invocations-response-stream` |
|---|---|---|---|---|
| `vllm` | `vllm:0.32.0-gpu-py312-cu130-ubuntu24.04-sagemaker` | `SM_VLLM_<FLAG>` → `--flag` | `openai` | SSE |
| `sglang` | `sglang:0.5.22-gpu-py312-cu130-ubuntu24.04-sagemaker` | `SM_SGLANG_<FLAG>` → `--flag` | `openai` | SSE |
| `lmi` | `djl-inference:0.38.0-lmi30.0.0-cu130` (vLLM inside, async mode) | `HF_MODEL_ID`, `OPTION_<FLAG>` | `invocations` | JSON lines (no `data:`) |
| `llamacpp` | `llama-cpp:server-sagemaker-cpu-v1`, `-arm64` on Graviton, `:server-sagemaker-cuda-v1` on GPUs | `SM_LLAMA_CPP_<FLAG>` | `invocations` | SSE |
| `tei` | `tei-cpu:2.0.1-tei1.9.4-…` / `tei:2.0.1-tei1.9.4-gpu-…-cu129` (registry account differs per region) | `HF_MODEL_ID` | `invocations` (embeddings) | n/a |
| `jumpstart` | from the JumpStart spec (LMI, TEI or the vLLM DLC, depending on the model) | from the spec | `invocations` | depends on the container |
| `custom` | `image` (required) | `extra_env` | `api` (default `invocations`) | as built |

- **`api`** is the route `probe.py` expects to work; the emitted Bifrost key carries only `region`. Only the vLLM and SGLang DLCs are on AWS's support list for `/openai/v1`. `probe.py` still tries every path on every engine, so you can see what actually works.
- **Images** come from the DLC registry, `763104351884` in most regions (`presets.tf` lists the exceptions). Override with `image_tag` (same repository) or `image` (anything).
- **AMI.** CUDA 13 images (`cu130` tags and the llama.cpp CUDA image) need `InferenceAmiVersion = al2023-ami-sagemaker-inference-gpu-4-1` (driver 580). The module derives it; on the default AMI those containers crash at start. CPU instances and CUDA 12.x images get no AMI pin. Override with `inference_ami_version`, or set it to `""` to unset.
- **Weights.** vLLM, SGLang, LMI and TEI download weights from Hugging Face at container start. llama.cpp downloads the preset's GGUF (`--hf-repo` / `--hf-file`). JumpStart mounts prepacked weights from S3. `model_data_url` mounts your own S3 weights at `/opt/ml/model`, and every DLC picks that up instead of downloading.

## Presets

| `model` | HF repo | Default instance | $/h | Engines (instance, where different) | Notes |
|---|---|---|---|---|---|
| `qwen2.5-0.5b` | `Qwen/Qwen2.5-0.5B-Instruct` | `ml.g6.xlarge` | 1.13 | vllm, sglang, lmi, llamacpp (`ml.c7i.2xlarge`, $0.43), jumpstart (`ml.g5.2xlarge`) | The llama.cpp GGUF is AWS's own example. Cheapest target overall; needs no GPU quota. |
| `qwen3-0.6b` | `Qwen/Qwen3-0.6B` | `ml.g6.xlarge` | 1.13 | vllm, sglang, lmi, llamacpp (CPU), jumpstart (`ml.g6e.2xlarge`) | JumpStart lists only g6e.2xlarge and larger for this model. |
| `qwen3-4b` *(default)* | `Qwen/Qwen3-4B` | `ml.g6.2xlarge` | 1.22 | vllm, sglang, lmi, llamacpp (CPU), jumpstart | AWS's example in the `/openai/v1` guide. |
| `gpt-oss-20b` | `openai/gpt-oss-20b` | `ml.g6.2xlarge` | 1.22 | vllm, sglang, lmi | MXFP4 on a 24 GB L4. Tools only with `tool_choice="auto"`. |
| `qwen3-coder-30b-a3b` | `Qwen/Qwen3-Coder-30B-A3B-Instruct-FP8` | `ml.g6e.2xlarge` | 2.80 | vllm, sglang, lmi | No thinking mode. |
| `qwen3.6-35b-a3b` | `Qwen/Qwen3.6-35B-A3B-FP8` | `ml.g6e.2xlarge` | 2.80 | vllm, sglang | Tight on 48 GB; lower `max_model_len` if it OOMs. |
| `glm-4.7-flash` | `zai-org/GLM-4.7-Flash` | `ml.g6e.12xlarge` (TP 4) | 13.12 | vllm, sglang | BF16 62 GB. SageMaker has no single-A100-80GB instance. |
| `bge-base-en-v1.5` | `BAAI/bge-base-en-v1.5` | `ml.c6i.2xlarge` | 0.41 | tei, vllm (`ml.g6.xlarge`), jumpstart (`ml.g6.xlarge`, TEI GPU) | Embedding. |
| `qwen3-embedding-0.6b` | `Qwen/Qwen3-Embedding-0.6B` | `ml.g6.xlarge` | 1.13 | vllm (pooling runner), tei (`ml.c6i.2xlarge`), jumpstart (vLLM DLC) | Embedding. |

- **Parser names** are from the vLLM v0.32.0 and SGLang v0.5.22 registries, the versions inside the DLCs. LMI 0.38 runs vLLM, so it takes vLLM's names.
- **Your own model.** Add it under `custom_presets` (see `terraform.tfvars.example`).
- **gpt-oss-20b on L4** uses vLLM's Marlin MXFP4 path. As on RunPod, it hasn't been validated on SageMaker hardware.

## JumpStart

`engine = "jumpstart"` resolves a JumpStart model the way the SageMaker SDK does. Everything comes from the public spec bucket (`jumpstart-cache-prod-<region>`), so `plan` needs no SageMaker API access.

1. **Spec.** Reads `community_models/<model_id>/specs_v<version>.json`. The presets pin a version. With `jumpstart_model_id` and no `jumpstart_model_version`, the module picks the newest version from `models_manifest.json`, an 8 MB download on every plan.
2. **Inference config.** Overlays the chosen config: `jumpstart_config`, else the spec's top ranking (`lmi`, `lmi-optimized`, `all_purpose`, …). Newer specs keep their images, instances and artifacts only inside these configs.
3. **Image and env.** Takes the image from the instance-family variant and the region alias. Env is the spec defaults merged with the per-instance variant env (tensor parallel degree, max model length, tool parser).
4. **Weights.** Mounts the prepacked S3 artifact with `ModelDataSource`. Network isolation and the deploy timeouts come from the spec too.

Plan-time checks:

- **Instance support.** An instance type the spec doesn't list fails, with the supported list in the error.
- **EULA.** A gated model (Llama and similar) fails until you set `accept_eula = true`. Read the EULA first; the `jumpstart.eula_url` output prints its URL. With the flag set, the module reads the private bucket and sets `ModelAccessConfig.AcceptEula`.
- **Not supported.** Configs that need extra model data sources (speculative-decoding drafts, e.g. gpt-oss `lmi-optimized`), and old specs that need a separate inference script, are rejected. Pick another `jumpstart_config` for the first case.

Not covered: proprietary JumpStart models (Marketplace model packages need a subscription) and Amazon Nova custom models.

```hcl
engine             = "jumpstart"
jumpstart_model_id = "huggingface-reasoning-qwen3-8b"   # any id from models_manifest.json
instance_type      = "ml.g6e.xlarge"
```

## probe.py

`probe.py` runs on the Python standard library alone. Credentials come from `AWS_*` env vars, or from `aws configure export-credentials`, so profiles and SSO work too. It signs SigV4 itself, mints the SageMaker bearer token itself, and decodes the AWS event stream itself. The minter is a port of `sagemaker-core`'s `generate_token`. Both the minter and the SigV4 signer were checked byte-for-byte against botocore 1.43.118, with and without a session token.

```bash
python3 -I probe.py run --region us-east-1 --endpoint <ep> [--ic <ic>] --kind chat --model <served-name> --out probe-results/<ep>
python3 -I probe.py token --region us-east-1 --expires 3600   # bearer token for curl or the OpenAI SDK
```

Each check records status, `x-amzn-ErrorType`, latency and a summary. With `--out`, the raw response (headers + body) for each check goes to a file next to `results.json`.

| Check | Path / auth | Answers |
|---|---|---|
| `invocations`, `invocations-stream` | `/invocations[-response-stream]`, SigV4 | Unary body; event-stream framing; `X-Amzn-SageMaker-Content-Type`; whether the container streams SSE or JSON lines |
| `openai-chat`, `openai-chat-stream` | `/openai/v1/chat/completions`, bearer | The provider's primary path; whether `model: ""` is passed through |
| `openai-chat-model-name` | same, body `model` = served name | Whether the container accepts its served model name |
| `openai-tools`, `invocations-tools` | tool call on both paths | Tool parser wiring |
| `openai-completions` / `-responses` / `-embeddings` / `-models` | other `/openai/v1/*` routes | Which OpenAI routes SageMaker forwards beyond chat |
| `openai-chat-sigv4` | `/openai/v1` with SigV4 headers instead of a bearer token | Whether the OpenAI route accepts SigV4 |
| `invocations-target-variant` | `X-Amzn-SageMaker-Target-Variant` (with `second_variant`) | Variant routing |
| `error-*` | unknown endpoint on both paths, garbage bearer token | Error shapes for the error converter |

Rows marked `expected = yes` (the path the Bifrost key uses) fail the run when they fail. Rows marked `info` are informational.

**Observed live** (2026-10-24, us-east-1, `vllm` `qwen3-4b` on `ml.g6.2xlarge`, vLLM 0.32.0 DLC):

- **Routing.**
  - `/openai/v1/chat/completions` works with a locally minted bearer token, unary and streaming, including tool calls and the qwen3 reasoning split.
  - `/openai/v1/{completions,responses,embeddings,models}` return 404 `<UnknownOperationException/>` before any auth check, so only chat is routed.
  - `model: ""` and the served name both work. A wrong name reaches the container, which returns 404 listing the valid aliases.
- **Auth on `/openai/v1`.** It takes bearer tokens only. Every failure is a 403 OpenAI-shaped `permission_error`, distinguished by message:

  | Case | Message |
  |---|---|
  | SigV4 headers, or no `Authorization` | `[REDACTED] required for OpenAI API` |
  | Expired token | `[REDACTED] has expired` |
  | Token minted for another region | `Authentication failed: Please make sure your API Key is valid.` |
  | Malformed token | `Missing required parameters in the API Key` |

  A token with `X-Amz-Expires` above 43200 gets a 500 `internal_server_error` rather than a 4xx.
- **Streaming.**
  - `/openai/v1` streams plain SSE ending in `data: [DONE]`.
  - `/invocations-response-stream` wraps the same SSE bytes in event-stream `PayloadPart` frames, with `X-Amzn-SageMaker-Content-Type: text/event-stream`.
  - `stream_options.include_usage` passes through: the last chunk before `[DONE]` carries `usage`, including `reasoning_tokens`.
- **Container errors.** Example: `max_tokens` above `max_model_len`.

  | Path | Status | Body |
  |---|---|---|
  | `/openai/v1` | The container's own status (400, 404, …) | `{"error":{"message","type":"server_error","code":"model_error"}}`. The message is `Received client error (400) from primary with message "<container JSON>". See <CloudWatch URL> …`, so the container's error is only available as an escaped string inside it. |
  | `/invocations` and `-response-stream` | Always 424, with `x-amzn-ErrorType: ModelError:…` | `{"ErrorCode":"CLIENT_ERROR_FROM_MODEL","OriginalStatusCode":400,"OriginalMessage":"<container JSON>","Message","LogStreamArn"}`. The streaming path fails before any frame is sent. |

  The `/openai/v1` stream error comes back as a plain JSON body despite `Content-Type: text/event-stream`. Malformed request JSON on `/openai/v1` is rejected by SageMaker with 400 `invalid_request_error`/`validation_error`.
- **Other errors.** An unknown endpoint returns 400 on both paths:
  - `/openai/v1`: `invalid_request_error`/`validation_error`;
  - `/invocations`: `{"ErrorCode":"NO_SUCH_ENDPOINT","Message"}`, with `x-amzn-ErrorType: ValidationError:…`.
- **Response headers.**
  - `/openai/v1` returns `x-amzn-RequestId` and `x-request-id` (the same value), and no `x-amzn-Invoked-Production-Variant`.
  - `/invocations` returns `x-amzn-RequestId` and `x-amzn-Invoked-Production-Variant`.
  - Neither path echoes `X-Amzn-SageMaker-Custom-Attributes`.

**Across containers** (2026-10-24, same account and region): `sglang`, `lmi`, `llamacpp` and
`tei`, plus `vllm` with an embedding model, JumpStart chat and embeddings, and a second
variant.

- **`/openai/v1` chat** works on every chat container: vLLM, SGLang, LMI, llama.cpp and
  JumpStart's LMI. The other `/openai/v1` routes return 404 on all of them, embedding
  containers included, so embeddings are `/invocations`-only.
- **Stream format** follows the container:
  - LMI streams JSON lines (no `data:`, no `[DONE]`) on **both** paths, while `/openai/v1`
    still labels it `text/event-stream`.
  - On `/invocations-response-stream`, `X-Amzn-SageMaker-Content-Type` is
    `application/jsonlines` for LMI and `text/event-stream` for vLLM, SGLang and llama.cpp.
- **Event-stream frames** carry an extra random-length padding header `P`.
- **Container errors** keep their status on `/openai/v1`, but each container formats the
  inner error differently:

  | Container | Error JSON |
  | --- | --- |
  | vLLM | `{"error":{…}}` |
  | SGLang | `{"object":"error",…}` |
  | LMI | `{"code":424,…}`, or a 500 for a wrong model name |

- **Reasoning field:** `reasoning` (vLLM, LMI) or `reasoning_content` (SGLang). JumpStart's
  qwen3 config sets no reasoning parser, so `<think>` stays inline. It also sets
  `OPTION_TOOL_CALL_PARSER` without `OPTION_ENABLE_AUTO_TOOL_CHOICE`, so tool calls fail with
  500. Fix that with `extra_env = { OPTION_ENABLE_AUTO_TOOL_CHOICE = "true" }`.
- **Embedding request bodies on `/invocations`:**
  - TEI takes `{"inputs": …}` and returns a bare `[[…]]`;
  - vLLM pooling takes and returns OpenAI shapes.
- **Variants.** `X-Amzn-SageMaker-Target-Variant: Variant2` routes there, and the response
  header names it.

## Inference components and variants

- **`inference_components = true`.**
  - The endpoint config carries no model; the module deploys it as an inference component (`<endpoint>-ic`, one copy).
  - It is addressed as `/endpoints/<ep>/inference-components/<ic>/openai/v1`, or with `X-Amzn-SageMaker-Inference-Component` on `/invocations`.
  - The Bifrost model id becomes `sagemaker/<ep>/<ic>`.
  - Compute requirements: `ic_min_memory_mb` (JumpStart's own requirement, else 8192; the value is the container's memory limit, and vLLM fails to load with 1024), `ic_cpu_cores`, and one accelerator per tensor-parallel rank on GPUs.
- **`second_variant = true`** adds `Variant2` (same model, weight 0) so `target_variant` on the invocations path has something to hit. It runs, and bills, a second instance and takes a second slot of the instance quota.

Neither can be combined with the other.

Verified 2026-10-24: inference components work on `ml.g6.2xlarge`. On `ml.g6.xlarge`, even 8 GiB / 2 CPUs / 1 GPU gets "not enough hardware resources", so use a 2xlarge or bigger. In IC mode the `/openai/v1` error wrapper names a container id instead of `primary` (`Received client error (400) from <id> with message …`).

## Auto-teardown

- **Mechanism.** EventBridge Scheduler one-shot `at()` schedules call `sagemaker:DeleteEndpoint` through the universal SDK target. With inference components, `DeleteInferenceComponent` runs 5 minutes earlier: an endpoint with live components can't be deleted. Both schedules delete themselves after firing. They run even if the machine that applied is offline.
- **The deadline is `now + teardown_after_minutes` at every apply**, so re-applying extends it. As a side effect every plan shows the schedules changing. `0` disables teardown.
- **What survives.** The model and endpoint config cost nothing and stay until `terraform destroy`. After the scheduler fires, the next `apply` recreates the endpoint, and `destroy` handles the missing endpoint.
- **CI** should still delete in an `if: always()` step. The scheduler is the backstop for a laptop that went to sleep.

## Prerequisites

- **Quotas** (Service Quotas → Amazon SageMaker, in the target region). Approval can take up to 48 h.
  - `ml.<type> for endpoint usage` for each instance type you plan to use. GPU types are commonly 0 on new accounts.
  - "Number of instances across all active endpoints", 4 by default.

  ```bash
  aws service-quotas list-service-quotas --service-code sagemaker --region us-east-1 \
    --query "Quotas[?contains(QuotaName,'endpoint usage') && Value>\`0\`].[QuotaName,Value]" --output table
  ```
- **Permissions for the identity that runs terraform:** [`deployer-policy.json`](deployer-policy.json), attached as a managed policy. It grants no IAM write rights:
  - SageMaker models, endpoint configs, endpoints and inference components named `bifrost-*`, plus bearer tokens;
  - `iam:PassRole` on exactly two roles, each only to the service that uses it;
  - EventBridge Scheduler on `bifrost-*` schedules, and read access to the endpoints' logs and the SageMaker quotas.
  - `aws login` (the same two `signin` actions as AWS's `SignInLocalDevelopmentAccess`), so no long-lived access key is needed.

  The probe and Bifrost call the endpoint with the same credentials. A custom `endpoint_name` must keep the `bifrost-` prefix.
- **One-time admin setup.** The module doesn't create IAM roles; an admin creates these two once per account (rename them with `execution_role_name` / `teardown_role_name`, and update the policy to match):

  ```bash
  aws iam create-role --role-name bifrost-sagemaker-exec --assume-role-policy-document \
    '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"sagemaker.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
  aws iam put-role-policy --role-name bifrost-sagemaker-exec --policy-name pull-images-write-logs-read-weights --policy-document '{"Version":"2012-10-17","Statement":[
    {"Effect":"Allow","Action":["ecr:GetAuthorizationToken","ecr:BatchCheckLayerAvailability","ecr:BatchGetImage","ecr:GetDownloadUrlForLayer"],"Resource":"*"},
    {"Effect":"Allow","Action":["logs:CreateLogGroup","logs:CreateLogStream","logs:PutLogEvents","logs:DescribeLogStreams"],"Resource":"arn:*:logs:*:*:log-group:/aws/sagemaker/*"},
    {"Effect":"Allow","Action":"cloudwatch:PutMetricData","Resource":"*"},
    {"Effect":"Allow","Action":["s3:GetObject","s3:ListBucket"],"Resource":["arn:*:s3:::jumpstart-cache-prod-*","arn:*:s3:::jumpstart-cache-prod-*/*","arn:*:s3:::jumpstart-private-cache-prod-*","arn:*:s3:::jumpstart-private-cache-prod-*/*"]}]}'

  aws iam create-role --role-name bifrost-sagemaker-teardown --assume-role-policy-document \
    '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"scheduler.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
  aws iam put-role-policy --role-name bifrost-sagemaker-teardown --policy-name delete-endpoint --policy-document \
    '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sagemaker:DeleteEndpoint","sagemaker:DeleteInferenceComponent"],"Resource":["arn:*:sagemaker:*:*:endpoint/bifrost-*","arn:*:sagemaker:*:*:inference-component/bifrost-*"]}]}'
  ```

  The execution role pulls images, writes its logs and reads AWS's JumpStart buckets. For `model_data_url` weights, the admin adds that bucket to its S3 statement.
- **Local tools** for `wait_for_ready`: `python3` (3.8+) and the AWS CLI v2.
- **Egress.** Containers download from huggingface.co (llama.cpp's GGUF, vLLM/SGLang/LMI/TEI weights) unless `model_data_url` or JumpStart supplies the weights. JumpStart models run network-isolated.

## Cost

On-demand us-east-1 hosting prices are in `presets.tf` (`price_per_hr`); `terraform output cost_per_hr` shows yours. Billing starts at creation, so the 5–15 minutes of image pull and weight download count.

| Scenario | ≈ cost |
|---|---|
| `llamacpp` on `ml.c7i.2xlarge`, 30 min smoke | $0.22 |
| `vllm` `qwen3-4b` on `ml.g6.2xlarge`, 2 h of probing | $2.50 |
| A forgotten `ml.g6.2xlarge` | $29/day, which is why teardown defaults to 60 min |

## Caveats

- **`volume_size_gb`.** Instances with local NVMe storage (g5, g6, g6e, p4d, p5) reject an explicit volume size. Leave it null there.
- **Health check timeout.** `health_check_timeout_seconds` (default 1800, max 3600) covers the image pull and weight download. Large models on slow downloads may need 3600, or `model_data_url`.
- **Insufficient capacity.**
  - When AWS has no spare instances of a type, the endpoint sits in `Creating` with no log group for up to about 30 min. Then it fails with `InsufficientInstanceCapacity`.
  - SageMaker refuses to delete an endpoint while it's creating.
  - The `Failed` endpoint is not removed by `terraform destroy`; delete it with `aws sagemaker delete-endpoint --endpoint-name <name>`.
  - Capacity is per instance type, so retry on another family. Your quota usually covers one or two of most `g5`/`g6`/`g6e` sizes.
- **Smoke-test reruns.** The smoke test reruns when the model or endpoint config changes, not when the scheduler deleted and apply recreated the endpoint. Run `terraform output probe_command` by hand then.
- **Body `model` field.** Bifrost always sends `model: ""` in the body. vLLM and SGLang accept it; LMI, TEI and llama.cpp ignore the field.
- **Unvalidated sizing.** Instance sizing for the 30B+ presets is estimated from checkpoint size, not run on SageMaker.

## API Reference

| Variable | Default | Purpose |
|---|---|---|
| `region` | `us-east-1` | Endpoint region; also the region in the emitted key. |
| `engine` | `vllm` | `vllm`, `sglang`, `lmi`, `llamacpp`, `tei`, `jumpstart`, `custom`. |
| `model`, `custom_presets` | `qwen3-4b`, `{}` | Preset key; your own presets with per-engine blocks. |
| `hf_model`, `served_model_name`, `max_model_len`, `tool_call_parser`, `reasoning_parser` | preset | Per-field overrides; `""` disables a parser. |
| `extra_env`, `hf_token` | `{}`, null | Container env merged last (engine flags go here); HF token for gated repos (lands in state and in the model env). |
| `kind` | preset | `chat` or `embedding`; drives the probe and outputs. |
| `image`, `image_tag`, `inference_ami_version` | derived | Full image, tag-only override, AMI override (`""` unsets). |
| `model_data_url` | null | `s3://…/` (uncompressed prefix) or `s3://…/model.tar.gz`, mounted at `/opt/ml/model`. |
| `jumpstart_model_id`, `jumpstart_model_version`, `jumpstart_config`, `accept_eula` | preset / newest / top-ranked / `false` | JumpStart selection. |
| `endpoint_name` | `bifrost-<engine>-<model>-<hex>` | ≤ 63 chars, `^[a-zA-Z0-9](-*[a-zA-Z0-9])*$`. |
| `instance_type`, `instance_count`, `gpu_count` | preset, `1`, preset | `gpu_count` is the tensor-parallel size; match it to the instance. |
| `volume_size_gb`, `health_check_timeout_seconds`, `routing_strategy` | null, `1800`, `LEAST_OUTSTANDING_REQUESTS` | Production variant settings. |
| `inference_components`, `ic_min_memory_mb`, `ic_cpu_cores` | `false`, derived, `2` | Inference component mode. |
| `second_variant` | `false` | Zero-weight `Variant2`. |
| `api` | derived | `openai` or `invocations`: the route the probe expects. |
| `execution_role_name`, `teardown_role_name` | `bifrost-sagemaker-exec`, `bifrost-sagemaker-teardown` | The admin-created roles. |
| `wait_for_ready` | `true` | Run `probe.py --quick` after InService. |
| `teardown_after_minutes` | `60` | Scheduler deletes the endpoint this long after the latest apply; `0` disables. |
| `tags` | `{}` | Extra tags; everything is also tagged `bifrost-test = true`. |

Outputs:

- **Endpoint:** `endpoint_name`, `inference_component_name`, `openai_base_url`, `invocations_url`.
- **Resolved config:** `image`, `instance_type`, `inference_ami_version`, `container_env` (without `HF_TOKEN`), `jumpstart`.
- **Cost and teardown:** `cost_per_hr`, `teardown_at`.
- **IAM:** `execution_role_arn`.
- **For Bifrost:** `probe_command`, `bifrost_model_id`, `bifrost_provider_config` (sensitive), and `test_env` (the `SAGEMAKER_*` env for `make test-core PROVIDER=sagemaker`).

## Tests

```bash
terraform init -backend=false && terraform test
```

All tests are plan-time, against mocked providers. JumpStart resolution runs on trimmed copies of the real specs in `tests/fixtures/`.
