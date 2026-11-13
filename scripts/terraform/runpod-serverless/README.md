# vLLM on RunPod Serverless

Runs a few inference models as RunPod Serverless endpoints, each a `vllm` provider key in Bifrost. Use it instead of `../runpod` when you only need these models and don't want a 20–30 minute pod boot every time. Decision models are not supported here; use `../runpod` for those.

Each model is a queue endpoint on RunPod's stock worker (`runpod/worker-v1-vllm`, vLLM v0.32.0). All endpoints share one network volume, mounted at `/runpod-volume`. The worker keeps the Hugging Face cache and vLLM's compile cache on that volume, so only the first cold start downloads weights.

| `models` entry | HF repo | GPU pool | vLLM tool / reasoning | Context |
|---|---|---|---|---|
| `gpt-oss-20b` | `openai/gpt-oss-20b` | `AMPERE_24`, `ADA_24` | `openai` / auto | 131K |
| `qwen3.8-27b` | `Qwen/Qwen3.8-27B-FP8` | `ADA_48_PRO` | `qwen3_coder` / `qwen3` | 65K |

## Usage

```bash
export RUNPOD_API_KEY=...
terraform init
terraform apply          # warm-up downloads weights to the volume; the first run can take 10+ min per model

terraform output -json bifrost_provider_config   # paste under providers.vllm in config.json
```

Start Bifrost with `RUNPOD_API_KEY` set; the keys reference it as `env.RUNPOD_API_KEY`. Clients reach a model at `<base_url>/v1` with the RunPod API key as the bearer token.

You can leave the endpoints up. With `workers_min = 0` they bill nothing while idle, so `terraform destroy` is only needed when you're done for good. Destroy deletes the volume too, so the next apply downloads weights again.

## Notes

- **Data center.** The volume lives in one data center (`data_center`, default `US-KS-2`), and the endpoints are pinned to it. If workers sit in "throttled" or never start, the data center lacks capacity in that pool - pick another one and re-apply. That replaces the volume, and the weights download again.
- **One worker per endpoint.** `workers_max = 1` caps spend and keeps two cold workers from downloading the same weights onto the volume at once. One worker still serves concurrent requests (the worker's `MAX_CONCURRENCY` defaults to 30).
- **Queue timeouts.** Requests pass through RunPod's queue. A request that arrives during a cold start waits for the worker. `execution_timeout_ms` (10 min) bounds each job.
- **Worker env.** `extra_env` adds any vLLM flag as an UPPER_SNAKE env var (`ENFORCE_EAGER`, `KV_CACHE_DTYPE`, ...), or raw args through `VLLM_EXTRA_ARGS`.
- **Adding a model.** Add an entry to `presets.tf`. It must load on the worker's vLLM version and fit a serverless pool; multi-GPU workers set `gpu_count`, which also sets `TENSOR_PARALLEL_SIZE`. Grow `volume_gb` to cover its weights.

## Tests

```bash
terraform test   # mock provider; no RunPod calls
```