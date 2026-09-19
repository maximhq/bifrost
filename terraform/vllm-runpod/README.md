# vLLM on RunPod

Spins up a single RunPod GPU pod running the `vllm/vllm-openai` server for a ~20–50B open-weight model, with tool-call and reasoning parsers preconfigured per model. Useful as a target for Bifrost's `vllm` provider.

## Usage

```bash
export RUNPOD_API_KEY=...
cp terraform.tfvars.example terraform.tfvars    # pick a model
terraform init
terraform apply                                 # waits until /v1/models answers

terraform output -json bifrost_provider_config  # paste under providers.vllm in config.json
terraform destroy                               # or let the 1h auto-teardown do it
```

Swap models by changing `model` (or any other variable) and re-applying. Any change **replaces** the pod: the provider's in-place update is broken against the RunPod API. Weights are cached on the pod volume, which is destroyed along with the pod.

## Presets

Parsers are the registry names in vLLM v0.29.0 (`vllm/tool_parsers`, `vllm/reasoning`), taken from the [vLLM recipes](https://github.com/vllm-project/recipes) and HF model cards.

| `model` | HF repo | Size | `--tool-call-parser` | `--reasoning-parser` | Default GPU (cheapest first) | ~$/hr | Image | Notes |
|---|---|---|---|---|---|---|---|---|
| `gpt-oss-20b` | `openai/gpt-oss-20b` | 21B / 3.6B act, MXFP4 14GB | `openai` | auto (`openai_gptoss`) | 1×24GB: A5000, L4, 3090, PRO 4000, 4090 | 0.27 | stable | Full 131K context. Tools only support `tool_choice="auto"`. |
| `glm-4.7-flash` | `zai-org/GLM-4.7-Flash` | 31B / 3B act, BF16 62GB | `glm47` | `glm45` | 1×A100 80GB | 1.59 | stable | 65K context. No official FP8. |
| `kimi-linear-48b-a3b` | `moonshotai/Kimi-Linear-48B-A3B-Instruct` | 49B / 3B act, BF16 98GB | `kimi_k2` ⚠️ | — | 2×A100 80GB, TP2 | 3.18 | stable | `--trust-remote-code`. See caveats. |
| `qwen3.8-27b` | `Qwen/Qwen3.8-27B-FP8` | 27B dense + vision, FP8 31GB | `qwen3_coder` | `qwen3` | 1×48GB Ada: L40, RTX 6000 Ada, L40S | 0.82 | **nightly** | 65K context. Recipe validated on a pre-release image. |
| `qwen3.6-35b-a3b` | `Qwen/Qwen3.6-35B-A3B-FP8` | 36B / 3B act, FP8 38GB | `qwen3_coder` | `qwen3` | 1×48GB Ada | 0.82 | stable | 65K context. Tight fit; see caveats. |
| `qwen3-coder-30b-a3b` | `Qwen/Qwen3-Coder-30B-A3B-Instruct-FP8` | 30B / 3B act, FP8 31GB | `qwen3_coder` | — | 1×48GB Ada | 0.82 | stable | 65K context. |
| `nemotron-3.5-lightning-30b-a3b` | `nvidia/NVIDIA-Nemotron-3.5-Lightning-30B-A3B-NVFP4` | 30B / 3B act, NVFP4 18GB | `qwen3_xml` | `nemotron_v3` | 1×32GB: PRO 4500, RTX 5000 Ada, 5090 | 0.72 | stable | Recipe's default NVFP4 variant (`--moe-backend marlin`, FP8 KV). Needs vLLM ≥ 0.27.1. |
| `gemma-4-31b-it` | `google/gemma-4-31B-it` | 31B dense, BF16 63GB | `gemma4` | `gemma4` | 1×A100 80GB | 1.59 | stable | 32K context. Gated (`hf_token`). Uses the image's `tool_chat_template_gemma4.jinja`. |

Prices are RunPod Secure Cloud on-demand for the first GPU in the list (Community Cloud is usually 15–60% cheaper). With `gpu_type_priority = "custom"` (default) RunPod rents the first type in the list that has stock, so you only pay more when the cheap cards are sold out.

Kimi K2.x/K3 (~1T+) and GLM-4.5-Air/5.x (100B+) have no variant in this size class.

## Knobs

| Variable | Default | Purpose |
|---|---|---|
| `model` | `gpt-oss-20b` | Preset key. |
| `custom_presets` | `{}` | Add your own presets (see `terraform.tfvars.example`). |
| `hf_model`, `served_model_name`, `max_model_len` | preset | Per-field overrides, e.g. swap to an FP8/AWQ repo. |
| `tool_call_parser`, `reasoning_parser` | preset | Override; `""` disables. |
| `extra_args`, `extra_env` | `[]`, `{}` | Appended to/merged over the preset. |
| `vllm_channel` | preset | `stable` → `vllm/vllm-openai:<vllm_version>[-cu129]`; `nightly` → `vllm/vllm-openai:[cu129-]nightly`. |
| `vllm_version` | `v0.29.0` | Stable tag. |
| `cuda_version` | `12.9` | `12.9` uses the `-cu129` tags and accepts hosts with CUDA 12.9 or 13.0; `13.0` needs 13.0 hosts. |
| `vllm_image` | — | Full override: a pinned nightly (`nightly-<sha>`), a model preview tag (`qwen38`), or an old release. |
| `gpu_type_ids`, `gpu_count`, `tensor_parallel_size` | preset / `gpu_count` | RunPod GPU ids, e.g. `NVIDIA RTX A5000`, `NVIDIA A40`, `NVIDIA L40S`, `NVIDIA A100 80GB PCIe`, `NVIDIA H100 PCIe`. |
| `gpu_type_priority` | `custom` | `custom` tries GPU types in list order; `availability` lets RunPod choose. |
| `cloud_type`, `data_center_ids`, `interruptible` | `SECURE`, any, `false` | Placement and spot pricing. |
| `volume_gb`, `container_disk_gb` | `150`, `50` | Weights go to `/workspace/hf`. |
| `expose_tcp` | `false` | Also expose `8000/tcp` (see networking). |
| `wait_for_ready`, `ready_timeout_minutes` | `true`, `45` | Block `apply` until the server answers. |
| `teardown_after_minutes` | `60` | Pod tears itself down this long after first container start. `0` disables. |
| `teardown_action` | `terminate` | `terminate` deletes the pod; `stop` releases the GPU but keeps (and bills) the volume. |

## Auto-teardown

The pod enforces the timeout itself, so it fires even if your laptop is asleep or offline. `teardown.sh` becomes the container entrypoint. It records a deadline in `/workspace/.teardown_at`, then `exec`s `vllm serve` with the usual args. When the deadline passes it calls the RunPod REST API with the pod's own `RUNPOD_POD_ID` and pod-scoped `RUNPOD_API_KEY`, both injected by RunPod.

- **When the clock starts:** at first container start, so image pull and weight download count against the hour. Container restarts don't reset it.
- **Extend a running pod** from the RunPod web terminal: `echo $(( $(date +%s) + 3600 )) > /workspace/.teardown_at`. Changing `teardown_after_minutes` and re-applying replaces the pod instead.
- **Terminate falls back to stop.** If the pod-scoped key isn't allowed to delete, the script stops the pod instead. That still frees the GPU, but volume storage keeps billing until `terraform destroy`.
- **Terraform state after a terminate.** The pod is gone but Terraform still tracks it, and this provider errors on the 404 instead of dropping it. Run `terraform state rm runpod_pod.vllm` before your next `apply` or `destroy`.

## Caveats

- **Provider.** RunPod's official `runpod/runpod` provider (1.0.9) fails to load its schema, so this uses the community `decentralized-infrastructure/runpod` 1.0.1. That provider works for create, read and delete. If you delete a pod in the RunPod console, run `terraform state rm runpod_pod.vllm` before the next plan.
- **Secrets in state.** `HF_TOKEN` and the vLLM API key are passed as pod env vars, so they sit in Terraform state in plain text.
- **Networking.** The pod is reached through `https://<pod>-8000.proxy.runpod.net`, which is behind Cloudflare. Requests that send no bytes within 100s get a 524; streaming is fine once the first token arrives.
  - Long non-streaming requests, or very long prefills, can hit that limit.
  - For those, set `expose_tcp = true` and use `public_ip` plus the mapped port shown in the RunPod console.
- **Kimi-Linear.**
  - The recipe pins vLLM 0.11.2 because of a 0.12.0 regression. Running on 0.29.0 is untested; fall back with `vllm_image = "vllm/vllm-openai:v0.11.2"`, `allowed_cuda_versions = ["12.8", "12.9", "13.0"]`.
  - `kimi_k2` as the tool parser is inferred from the chat template, not documented.
  - For a single GPU: `hf_model = "nm-testing/Kimi-Linear-48B-A3B-Instruct-FP8-DYNAMIC"`, `gpu_count = 1` (community quant).
- **Cheap-GPU sizing is estimated**, not validated on RunPod. The recipes only mark H100/H200/B200-class hardware as verified.
  - gpt-oss-20b on Ampere/Ada (A5000, L4, 3090, 4090) uses the Marlin MXFP4 path.
  - FP8 presets are kept on Ada or newer (L40/L40S/RTX 6000 Ada). On Ampere (A40/A6000, $0.49) FP8 falls back to slower W8A16 Marlin kernels; try it with `gpu_type_ids = ["NVIDIA A40", "NVIDIA RTX A6000"]` if you want the extra savings.
  - `qwen3.6-35b-a3b` leaves ~4GB of headroom on 48GB. If it OOMs at startup, lower `max_model_len`, add `extra_args = ["--language-model-only"]`, or use `gpu_type_ids = ["NVIDIA A100 80GB PCIe"]`.
  - Splitting across several small GPUs (e.g. `gpu_count = 2` on 4090s) works but runs tensor parallel over PCIe; one bigger card is usually both cheaper and faster.
  - Cheaper variants of the 80GB presets: `unsloth/GLM-4.7-Flash-FP8-Dynamic` and `RedHatAI/gemma-4-31B-it-FP8-dynamic` on 1×48GB, `cyankiwi/Kimi-Linear-48B-A3B-Instruct-AWQ-4bit` on 1×48GB. All are community or less-tested quants; set `hf_model` + `gpu_type_ids` to use them.
- **Speculative decoding** is off by default. Qwen3.8/3.6 and GLM-4.7-Flash ship MTP heads; enable with e.g. `extra_args = ["--speculative-config", "{\"method\":\"mtp\",\"num_speculative_tokens\":3}"]`. Qwen3.8 needs the token count spelled out.
- **Thinking off** for Qwen: `extra_args = ["--default-chat-template-kwargs", "{\"enable_thinking\": false}"]`.

## Tests

```bash
terraform init -backend=false && terraform test
```