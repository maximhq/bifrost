variable "models" {
  description = "Presets to deploy, one endpoint each."
  type        = set(string)
  default     = ["gpt-oss-20b", "qwen3.8-27b"]

  validation {
    condition     = length(setsubtract(var.models, keys(local.builtin_presets))) == 0
    error_message = "Unknown model; available: gpt-oss-20b, qwen3.8-27b."
  }
}

variable "name_prefix" {
  description = "Prefix for the volume and endpoint names."
  type        = string
  default     = "bifrost"
}

variable "data_center" {
  description = "Data center for the network volume; endpoints are pinned to it. Must stock the presets' GPU pools."
  type        = string
  default     = "US-KS-2"
}

variable "volume_gb" {
  description = "Network volume size: HF weights plus vLLM's compile cache. Can grow, never shrink."
  type        = number
  default     = 50
}

variable "worker_image" {
  description = "RunPod vLLM worker image (bundles vLLM v0.32.0 at v2.30.1)."
  type        = string
  default     = "runpod/worker-v1-vllm:v2.30.1"
}

variable "workers_min" {
  description = "Always-on workers per endpoint. Each one bills 24/7; keep 0 unless cold starts are unacceptable."
  type        = number
  default     = 0
}

variable "workers_max" {
  description = "Max workers per endpoint. 1 caps spend and stops two cold workers downloading the same weights at once."
  type        = number
  default     = 1
}

variable "idle_timeout" {
  description = "Seconds an idle worker stays up (billed) before scaling to zero. Covers gaps between harness requests."
  type        = number
  default     = 60
}

variable "flashboot" {
  description = "OFF, FLASHBOOT or PRIORITY_FLASHBOOT."
  type        = string
  default     = "FLASHBOOT"
}

variable "execution_timeout_ms" {
  description = "Per-request timeout. Long enough for a streamed response at full context."
  type        = number
  default     = 600000
}

variable "gpu_memory_utilization" {
  type    = number
  default = 0.92
}

variable "excluded_gpu_types" {
  description = "GPU type IDs removed from the selected pools. MIG slices are excluded by default (lower bandwidth)."
  type        = list(string)
  default = [
    "NVIDIA RTX PRO 6000 Blackwell Server Edition MIG 1g.24gb",
    "NVIDIA RTX PRO 6000 Blackwell Server Edition MIG 2g.48gb",
  ]
}

variable "hf_token" {
  description = "Only needed for gated models."
  type        = string
  default     = null
  sensitive   = true
}

variable "extra_env" {
  description = "Extra worker env per model, e.g. { \"qwen3.8-27b\" = { ENFORCE_EAGER = \"true\" } }. Any UPPER_SNAKE vLLM flag works."
  type        = map(map(string))
  default     = {}
}

variable "warm_up" {
  description = "After apply, send one request per endpoint so the first cold start (weight download) happens now, not in the harness."
  type        = bool
  default     = true
}

variable "warm_up_timeout_minutes" {
  type    = number
  default = 30
}
