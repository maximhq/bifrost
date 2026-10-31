# vLLM / SGLang / Ollama on RunPod

Runs one RunPod GPU pod with `vllm/vllm-openai`, `lmsysorg/sglang` or `ollama/ollama`, as a target for Bifrost's `vllm`, `sgl` or `ollama` provider. Each preset is one of two kinds:

- **Inference models** (~20–50B open-weight). On vLLM and SGLang the preset sets the tool-call and reasoning parsers; Ollama handles both per model itself.
- **Decision models** (Clef, Liquid d1, PPLX-Decider, Nimble, Tev1), served on the engine's native `POST /v1/systemone` (TypeSafe's System One API).

## Usage

```bash
export RUNPOD_API_KEY=...
cp terraform.tfvars.example terraform.tfvars    # pick an engine and a model
terraform init
terraform apply                                 # waits until the model is listed on /v1/models

terraform output bifrost_provider_name          # vllm, sgl or ollama
terraform output -json bifrost_provider_config  # paste under providers.<bifrost_provider_name> in config.json
terraform destroy                               # or let the auto-teardown (60m by default) do it
```

Changing any variable and re-applying **replaces** the pod, because the provider's in-place update doesn't work against the RunPod API. Weights are cached on the pod volume, which is destroyed with the pod.

## Engines

| | vLLM | SGLang | Ollama |
|---|---|---|---|
| Image | `vllm/vllm-openai:v0.30.0` (stable), `:nightly` | `lmsysorg/sglang:v0.5.21` (stable), `:dev` | `ollama/ollama:0.40.4`, `:latest` |
| Weights | HF checkpoints (BF16 / FP8 / NVFP4 / MXFP4) | HF checkpoints | Ollama library GGUF builds (presets use `q4_K_M`) |
| Host CUDA | 13.0 (12.9 with `cuda_version = "12.9"`) | 13.0 | 12.8+ |
| Multi-GPU | Tensor parallel (`gpu_count`) | Tensor parallel (`gpu_count`) | Splits a model across GPUs only when it doesn't fit on one |
| Auth | `VLLM_API_KEY` | `--api-key` | nginx in front of Ollama checks the bearer token (Ollama has no server-side auth) |
| Config surface | `extra_args` (+ `extra_env`) | `extra_args` (+ `extra_env`) | `extra_env` (`OLLAMA_*`); no server args |

All three listen on port 8000 behind the same generated API key. `server_args` shows the args without the key.

**Ollama specifics.** `ollama-serve.sh` runs as the server command:
1. Installs nginx and curl with apt on first container start (curl is for the auto-teardown, which calls the RunPod API).
2. Starts Ollama on `127.0.0.1:11434`.
3. Pulls the preset's tag and copies it to the served name (`ollama cp gpt-oss:20b gpt-oss-20b`).
4. Starts nginx on `:8000`.

The module also sets `OLLAMA_KEEP_ALIVE=-1` so the model stays loaded, `OLLAMA_CONTEXT_LENGTH` from the preset, and `OLLAMA_MODELS=/workspace/ollama` on the volume. Ollama sizes its default context by VRAM (4K under 24 GiB), which truncates tool-calling prompts. That's why the presets set it explicitly.

## Inference presets

vLLM parsers are the registry names in vLLM v0.30.0 (`vllm/tool_parsers`, `vllm/reasoning`), from the [vLLM recipes](https://github.com/vllm-project/recipes) and HF model cards. SGLang parsers are the registry names in SGLang v0.5.21 (`srt/function_call/function_call_parser.py`, `srt/parser/reasoning_parser.py`), from the [SGLang cookbook](https://github.com/sgl-project/sglang/tree/main/docs/cookbook/autoregressive). The two name several parsers differently. Ollama tags are from [ollama.com/library](https://ollama.com/library).

| `model` | HF repo | Size | vLLM tool / reasoning | SGLang tool / reasoning | Default GPU | ~$/hr | Notes |
|---|---|---|---|---|---|---|---|
| `gpt-oss-20b` | `openai/gpt-oss-20b` | 21B / 3.6B act, MXFP4 14GB | `openai` / auto | `gpt-oss` / `gpt-oss` | 1×24GB: A5000, L4, 3090, PRO 4000, 4090 | 0.27 | 131K context. Tools only support `tool_choice="auto"`. |
| `glm-4.7-flash` | `zai-org/GLM-4.7-Flash` | 31B / 3B act, BF16 62GB | `glm47` / `glm45` | `glm47` / `glm45` | 1×A100 80GB | 1.59 | 65K context. No official FP8. SGLang ≥ 0.5.8. |
| `kimi-linear-48b-a3b` | `moonshotai/Kimi-Linear-48B-A3B-Instruct` | 49B / 3B act, BF16 98GB | `kimi_k2` ⚠️ / — | `kimi_k2` / — | 2×A100 80GB, TP2 | 3.18 | `--trust-remote-code`. Not in the Ollama library. See caveats. |
| `qwen3.8-27b` | `Qwen/Qwen3.8-27B-FP8` | 27B dense + vision, FP8 31GB | `qwen3_coder` / `qwen3` | `qwen3_coder` / `qwen3` | 1×48GB Ada: L40, RTX 6000 Ada, L40S | 0.82 | 65K context. vLLM uses **nightly** (recipe validated on a pre-release image); SGLang ≥ 0.5.18. |
| `qwen3.6-35b-a3b` | `Qwen/Qwen3.6-35B-A3B-FP8` | 36B / 3B act, FP8 38GB | `qwen3_coder` / `qwen3` | `qwen3_coder` / `qwen3` | 1×48GB Ada | 0.82 | 65K context. ~4GB headroom on 48GB; see caveats. |
| `qwen3-coder-30b-a3b` | `Qwen/Qwen3-Coder-30B-A3B-Instruct-FP8` | 30B / 3B act, FP8 31GB | `qwen3_coder` / — | `qwen3_coder` / — | 1×48GB Ada | 0.82 | 65K context. No thinking mode. |
| `nemotron-3.5-lightning-30b-a3b` | `nvidia/NVIDIA-Nemotron-3.5-Lightning-30B-A3B-NVFP4` | 30B / 3B act, NVFP4 18GB | `qwen3_xml` / `nemotron_v3` | `qwen3_coder` / `nemotron_3` | 1×32GB: PRO 4500, RTX 5000 Ada, 5090 | 0.72 | vLLM uses the recipe's NVFP4 flags (`--moe-backend marlin`, FP8 KV), ≥ 0.27.1. SGLang uses `--mamba-ssm-dtype float16`, ≥ 0.5.18. |
| `gemma-4-31b-it` | `google/gemma-4-31B-it` | 31B dense, BF16 63GB | `gemma4` / `gemma4` | `gemma4` / `gemma4` | 1×A100 80GB | 1.59 | 32K context. Gated (`hf_token`). vLLM uses the image's `tool_chat_template_gemma4.jinja`; SGLang uses the HF template. |

On Ollama the same presets load `q4_K_M` GGUF builds. These are smaller than the checkpoints above, so several use a smaller GPU list:

| `model` | Ollama tag | Size | Default GPU on Ollama | ~$/hr |
|---|---|---|---|---|
| `gpt-oss-20b` | `gpt-oss:20b` | 13.8GB (MXFP4) | 1×24GB | 0.27 |
| `glm-4.7-flash` | `glm-4.7-flash:q4_K_M` | 19.0GB | 1×32GB | 0.72 (vs 1.59 for BF16 on A100) |
| `qwen3.8-27b` | `qwen3.8:27b-q4_K_M` | 17.7GB + vision projector | 1×32GB | 0.72 |
| `qwen3.6-35b-a3b` | `qwen3.6:35b-a3b-q4_K_M` | 23.9GB | 1×32GB | 0.72 |
| `qwen3-coder-30b-a3b` | `qwen3-coder:30b-a3b-q4_K_M` | 18.6GB | 1×32GB | 0.72 |
| `nemotron-3.5-lightning-30b-a3b` | `nemotron-3.5-lightning:30b-a3b-q4_K_M` | 25.4GB | 1×32GB | 0.72 |
| `gemma-4-31b-it` | `gemma4:31b-it-q4_K_M` | 20.4GB + vision projector + draft model | 1×48GB | 0.82 |

Prices are RunPod Secure Cloud on-demand for the first GPU in the list; Community Cloud is typically 15–60% lower. With `gpu_type_priority = "custom"` (default), RunPod rents the first type in the list that has stock.

Kimi K2.x/K3 (~1T+) and GLM-4.5-Air/5.x (100B+) have no variant in this size class.

## Decision presets

Decision models answer typed questions (`choice`, `noul`, `score`) with probabilities on `POST /v1/systemone`. A preset without a block for the chosen engine fails at plan time.

| `model` | Weights | vLLM | SGLang | Ollama | Default GPU | ~$/hr | Notes |
|---|---|---|---|---|---|---|---|
| `clef-flash` | `Cloudflare/clef-flash`, 9.4B BF16 19GB | ✅ nightly | ✅ `dev` | ✅ `clef-flash:9b` (10.6GB) | 1×32GB (Ollama: 1×24GB) | 0.72 / 0.27 | 16,384-token prompt cap. On SGLang it runs at TP=1 and disables radix cache, chunked prefill and CUDA graphs itself. |
| `clef` | `Cloudflare/clef`, 27B BF16 55GB | — | ✅ `dev` | ✅ `clef:27b-q4_K_M` (20.2GB) | 1×A100 80GB (Ollama: 1×32GB) | 1.59 / 0.72 | vLLM support is undocumented. |
| `d1-3b` | `LiquidAI/d1-3B`, 3.1B BF16 7GB | ✅ nightly | — | — | 1×24GB | 0.27 | LFM1.0 license. `--no-enable-prefix-caching` suits one-question traffic over a changing state. |
| `pplx-decider-v1.1-27b` | `perplexity-ai/pplx-decider-v1.1-27b`, 27B BF16 52GB | — | ✅ `dev` | — | 1×A100 80GB | 1.59 | Validated on H200; 80GB is an estimate. vLLM doesn't map its readout head. Trained on prompts up to 8,192 tokens. FP8 moves probabilities by up to 0.2. |
| `nimble` | `nimble:9b` (9.5GB) | — | — | ✅ | 1×24GB | 0.27 | Bespoke Labs, Qwen3.5-9B base. Prompts must fit 8,192 tokens. |
| `tev1` | `tev1:4b` (4.6GB) | — | — | ✅ | 1×24GB | 0.27 | Together AI, experimental, ~2K context. `ollama_model = "tev1:0.8b"` for the 0.8B variant. |

**Why some combinations are missing:**
- **Nimble and Tev1 on vLLM/SGLang.** Both are trained on their own prompt formats. Nimble's HF weights are LoRA adapters with a calibrated temperature. The vLLM and SGLang native routes use a generic prompt, so their answers wouldn't match; Ollama's builds carry the model's own format.
- **Laya.** Every Ollama build is MLX-format and untested on NVIDIA; it otherwise runs on `laya-serve`, not on any of these engines.

**Image versions.** vLLM's `/v1/systemone` (all three question types) merged after v0.31.0, so only nightly images have it. SGLang v0.5.21 has the route, but Clef and PPLX-Decider support merged after that release. Ollama serves all five of its decision models from 0.40.0. The vLLM/SGLang decision presets therefore default to `vllm/vllm-openai:nightly` / `lmsysorg/sglang:dev`. Pin a build for repeatable runs, e.g. `image = "vllm/vllm-openai:nightly-77154ca6116ae34cbfe314beef1016bde9b63989"` (2026-10-14, all three question types) or `lmsysorg/sglang:nightly-dev-<date>-<sha>`.

**Your own decision model.** Add a `custom_presets` entry with `kind = "decision"` (see `terraform.tfvars.example`):
- **SGLang:** Qwen3.8-27B and Qwen3.5-35B-A3B are validated.
- **vLLM:** covers Qwen3, Qwen3.5 and Qwen3.5-MoE, and returns 501 for other architectures. The preset runs on the nightly image, since stable releases lack `/v1/systemone`, unless you set `vllm.channel`, `channel` or `image`.

## API Reference

| Variable | Default | Purpose |
|---|---|---|
| `engine` | `vllm` | `vllm`, `sglang` or `ollama`. |
| `model` | `gpt-oss-20b` | Preset key. |
| `custom_presets` | `{}` | Your own presets (see `terraform.tfvars.example`). They run on every engine; parsers go in the `vllm` / `sglang` blocks, the Ollama tag in the `ollama` block. |
| `hf_model`, `served_model_name`, `max_model_len` | preset | Per-field overrides. `hf_model` applies to vLLM/SGLang. `max_model_len` maps to vLLM `--max-model-len`, SGLang `--context-length` and Ollama `OLLAMA_CONTEXT_LENGTH`. |
| `ollama_model` | preset | Ollama tag override, e.g. `gpt-oss:20b-q8_0` or `hf.co/<user>/<gguf-repo>:<quant>`. |
| `tool_call_parser`, `reasoning_parser` | preset | Override; `""` disables. Names are engine-specific; Ollama ignores them. |
| `gpu_memory_utilization` | vLLM `0.92`, SGLang auto | vLLM `--gpu-memory-utilization` or SGLang `--mem-fraction-static` (SGLang's fraction covers weights + KV). Ollama ignores it. |
| `extra_args`, `extra_env` | `[]`, `{}` | Appended to/merged over the preset. Flags are engine-specific; Ollama accepts `extra_env` only. |
| `channel` | preset | `stable` → `vllm/vllm-openai:<vllm_version>[-cu129]`, `lmsysorg/sglang:<sglang_version>` or `ollama/ollama:<ollama_version>`; `nightly` → `vllm/vllm-openai:[cu129-]nightly`, `lmsysorg/sglang:dev` or `ollama/ollama:latest`. |
| `vllm_version` | `v0.30.0` | vLLM stable tag. It's the base of RunPod's `runpod/worker-v1-vllm` image, so hosts may already have its layers; other tags pull ~9 GB. |
| `sglang_version` | `v0.5.21` | SGLang stable tag (CUDA 13). The first release with `/v1/systemone`. |
| `ollama_version` | `0.40.4` | Ollama tag. 0.40.0+ serves all of `nimble`, `tev1`, `clef` and `clef-flash`. |
| `cuda_version` | `13.0` | vLLM/SGLang image CUDA build. `12.9` (vLLM only) uses the `-cu129` tags and accepts CUDA 12.9 or 13.0 hosts, but those images crash on import from v0.30.0 ([vllm#59157](https://github.com/vllm-project/vllm/issues/59157)). SGLang has published no CUDA 12 images since v0.5.19. Ollama always accepts 12.8+ hosts. |
| `image` | — | Full override: a pinned nightly (`vllm/vllm-openai:nightly-<sha>`, `lmsysorg/sglang:nightly-dev-<date>-<sha>`), a model preview tag (`vllm/vllm-openai:qwen38`), or another release. |
| `gpu_type_ids`, `gpu_count`, `tensor_parallel_size` | preset / `gpu_count` | RunPod GPU ids, e.g. `NVIDIA RTX A5000`, `NVIDIA A40`, `NVIDIA L40S`, `NVIDIA A100 80GB PCIe`, `NVIDIA H100 PCIe`. |
| `gpu_type_priority` | `custom` | `custom` tries GPU types in list order; `availability` lets RunPod choose. |
| `cloud_type`, `data_center_ids`, `interruptible` | `SECURE`, any, `false` | Placement and spot pricing. |
| `volume_gb`, `container_disk_gb` | `150`, `50` | Weights go to `/workspace/hf` (vLLM/SGLang) or `/workspace/ollama`. |
| `expose_tcp` | `false` | Also expose `8000/tcp` (see networking). |
| `wait_for_ready`, `ready_timeout_minutes` | `true`, `45` | Block `apply` until the served model is listed. Needs `curl` and `jq` on the machine running Terraform. |
| `teardown_after_minutes` | `60` | Pod tears itself down this long after first container start. `0` disables. |
| `teardown_action` | `terminate` | `terminate` deletes the pod; `stop` releases the GPU but keeps (and bills) the volume. |

## Auto-teardown

The pod enforces the timeout itself, so it fires even if the machine that ran `apply` is offline. `entrypoint.sh` becomes the container entrypoint:
1. It records a deadline in `/workspace/.teardown_at`.
2. It `exec`s the server command: `vllm serve …`, `python3 -m sglang.launch_server …` or `ollama-serve.sh`.
3. When the deadline passes, it calls the RunPod REST API with the pod's own `RUNPOD_POD_ID` and pod-scoped `RUNPOD_API_KEY`, both injected by RunPod.

- **When the clock starts:** at the first container start, after the image pull. The model download that follows counts. Container restarts don't reset it.
- **Extend a running pod** from the RunPod web terminal: `echo $(( $(date +%s) + 3600 )) > /workspace/.teardown_at`. Changing `teardown_after_minutes` and re-applying replaces the pod instead.
- **Terminate falls back to stop.** If the pod-scoped key can't delete, the script stops the pod. That frees the GPU, but the volume keeps billing until `terraform destroy`.
- **Terraform state after a terminate.** The pod is gone but Terraform still tracks it, and this provider errors on the 404 instead of dropping it. Run `terraform state rm runpod_pod.server` before the next `apply` or `destroy`.

## Caveats

- **Networking.** The pod is reached through `https://<pod>-8000.proxy.runpod.net`, which is behind Cloudflare. Requests that send no bytes within 100s get a 524; streams are unaffected once the first token arrives.
  - Long non-streaming requests and long prefills can hit that limit.
  - For those, stream the response, or reach port 8000 through a TLS-protected tunnel or endpoint you control (for example an SSH tunnel to the pod). `expose_tcp = true` maps `8000/tcp` to the pod's `public_ip`, but that port is plain HTTP: the API key and prompts cross the internet unencrypted, so don't connect to it directly.
- **SGLang.**
  - Current images are CUDA 13 only, so pods land on CUDA 13 hosts.
  - RunPod pods can't set `--shm-size`. TP>1 may fail in NCCL's shared-memory transport; `extra_env = { NCCL_SHM_DISABLE = "1" }` is the usual workaround. Single-GPU presets are unaffected.
- **Ollama.**
  - The first container start needs apt access to install nginx (a few seconds).
  - A model that doesn't fit in VRAM partly runs on the CPU. Run `ollama ps` in the pod's web terminal to see the GPU/CPU split.
  - `OLLAMA_NUM_PARALLEL` (default 1) multiplies KV-cache memory. `OLLAMA_KV_CACHE_TYPE = "q8_0"` halves it.
- **Kimi-Linear.**
  - The vLLM recipe pins 0.11.2 because of a 0.12.0 regression, and 0.30.0 is untested. To fall back: `image = "vllm/vllm-openai:v0.11.2"`, `allowed_cuda_versions = ["12.8", "12.9", "13.0"]`.
  - `kimi_k2` as the tool parser is inferred from the chat template, not documented.
  - For a single GPU: `hf_model = "nm-testing/Kimi-Linear-48B-A3B-Instruct-FP8-DYNAMIC"`, `gpu_count = 1` (community quant).
- **GPU sizing is estimated** from model size, not validated on RunPod. The recipes only mark H100/H200/B200-class hardware as verified.
  - gpt-oss-20b on Ampere/Ada (A5000, L4, 3090, 4090) uses the Marlin MXFP4 path on vLLM and the Triton MoE kernel on SGLang.
  - FP8 presets are kept on Ada or newer (L40/L40S/RTX 6000 Ada). On Ampere (A40/A6000, $0.49/hr vs $0.82/hr for an L40) FP8 falls back to slower W8A16 Marlin kernels. To try it: `gpu_type_ids = ["NVIDIA A40", "NVIDIA RTX A6000"]`.
  - On vLLM, `qwen3.6-35b-a3b` leaves ~4GB of headroom on 48GB. If it OOMs at startup, lower `max_model_len`, add `extra_args = ["--language-model-only"]` (vLLM), or use `gpu_type_ids = ["NVIDIA A100 80GB PCIe"]`.
  - Several small GPUs (e.g. `gpu_count = 2` on 4090s) run tensor parallel over PCIe, which is slower than one larger card with the same total memory.
  - Smaller quantized variants of the 80GB presets fit 1×48GB: `unsloth/GLM-4.7-Flash-FP8-Dynamic`, `RedHatAI/gemma-4-31B-it-FP8-dynamic`, `cyankiwi/Kimi-Linear-48B-A3B-Instruct-AWQ-4bit`. All are community or less-tested quants; set `hf_model` + `gpu_type_ids` to use them.
- **Speculative decoding** is off by default. Qwen3.8/3.6 and GLM-4.7-Flash ship MTP heads; on vLLM enable it with e.g. `extra_args = ["--speculative-config", "{\"method\":\"mtp\",\"num_speculative_tokens\":3}"]`. Qwen3.8 needs the token count spelled out.
- **Thinking off** for Qwen on vLLM: `extra_args = ["--default-chat-template-kwargs", "{\"enable_thinking\": false}"]`. On SGLang, send `chat_template_kwargs.enable_thinking` per request.

## Tests

```bash
terraform init -backend=false && terraform test
```