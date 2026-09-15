# --- Model ---

variable "model" {
  description = "Preset key from presets.tf or custom_presets (e.g. gpt-oss-20b, glm-4.7-flash, qwen3.6-35b-a3b)."
  type        = string
  default     = "gpt-oss-20b"

  validation {
    condition     = contains(concat(keys(local.builtin_presets), keys(var.custom_presets)), var.model)
    error_message = "Unknown model preset. Built-in: ${join(", ", keys(local.builtin_presets))}; or add it to custom_presets."
  }
}

variable "custom_presets" {
  description = "Extra presets merged over the built-in ones; select with var.model."
  type = map(object({
    hf_model          = string
    served_model_name = optional(string)
    tool_call_parser  = optional(string)
    reasoning_parser  = optional(string)
    trust_remote_code = optional(bool, false)
    max_model_len     = optional(number)
    gpu_count         = optional(number, 1)
    gpu_type_ids      = optional(list(string), ["NVIDIA L40", "NVIDIA RTX 6000 Ada Generation", "NVIDIA L40S", "NVIDIA A100 80GB PCIe"])
    vllm_channel      = optional(string, "stable")
    extra_args        = optional(list(string), [])
    env               = optional(map(string), {})
  }))
  default = {}

  validation {
    condition     = alltrue([for p in values(var.custom_presets) : p.gpu_count > 0 && floor(p.gpu_count) == p.gpu_count && (p.max_model_len == null ? true : p.max_model_len > 0 && floor(p.max_model_len) == p.max_model_len)])
    error_message = "custom_presets gpu_count and max_model_len must be positive whole numbers (max_model_len may be omitted)."
  }
}

variable "hf_model" {
  description = "Override the preset's HF repo (e.g. swap to an FP8/AWQ variant)."
  type        = string
  default     = null
}

variable "served_model_name" {
  description = "Override the model name exposed on /v1/models."
  type        = string
  default     = null
}

variable "tool_call_parser" {
  description = "Override the preset's --tool-call-parser. Empty string disables auto tool choice."
  type        = string
  default     = null
}

variable "reasoning_parser" {
  description = "Override the preset's --reasoning-parser. Empty string disables it."
  type        = string
  default     = null
}

variable "max_model_len" {
  description = "Override the preset's --max-model-len."
  type        = number
  default     = null

  validation {
    condition     = var.max_model_len == null ? true : var.max_model_len > 0 && floor(var.max_model_len) == var.max_model_len
    error_message = "max_model_len must be a positive whole number or null."
  }
}

variable "gpu_memory_utilization" {
  description = "--gpu-memory-utilization."
  type        = number
  default     = 0.92

  validation {
    condition     = var.gpu_memory_utilization > 0 && var.gpu_memory_utilization <= 1
    error_message = "gpu_memory_utilization must be in (0, 1]."
  }
}

variable "extra_args" {
  description = "Extra vllm serve args appended after the preset's (e.g. [\"--kv-cache-dtype\", \"fp8\"])."
  type        = list(string)
  default     = []
}

variable "extra_env" {
  description = "Extra container env vars merged over the preset's."
  type        = map(string)
  default     = {}
}

variable "hf_token" {
  description = "HuggingFace token for gated models. Stored in state in plain text."
  type        = string
  default     = null
  sensitive   = true
}

variable "api_key" {
  description = "vLLM API key. Generated when null."
  type        = string
  default     = null
  sensitive   = true
}

# --- Image ---

variable "vllm_channel" {
  description = "\"stable\" or \"nightly\". Null uses the preset's channel."
  type        = string
  default     = null
}

variable "vllm_version" {
  description = "Stable release tag used when channel is stable."
  type        = string
  default     = "v0.29.0"
}

variable "cuda_version" {
  description = "Image CUDA build: \"12.9\" (runs on more RunPod hosts) or \"13.0\"."
  type        = string
  default     = "12.9"

  validation {
    condition     = contains(["12.9", "13.0"], var.cuda_version)
    error_message = "cuda_version must be \"12.9\" or \"13.0\"."
  }
}

variable "vllm_image" {
  description = "Full image override, e.g. vllm/vllm-openai:nightly-<sha> or a model preview tag like vllm/vllm-openai:qwen38."
  type        = string
  default     = null
}

variable "allowed_cuda_versions" {
  description = "Host CUDA versions to accept. Null derives from cuda_version."
  type        = list(string)
  default     = null
}

# --- Pod ---

variable "pod_name" {
  description = "Pod name. Defaults to vllm-<model>."
  type        = string
  default     = null
}

variable "gpu_type_ids" {
  description = "RunPod GPU type ids in preference order. Null uses the preset's list."
  type        = list(string)
  default     = null
}

variable "gpu_type_priority" {
  description = "\"custom\" rents GPU types in list order (cheapest first in the presets); \"availability\" lets RunPod pick."
  type        = string
  default     = "custom"
}

variable "gpu_count" {
  description = "GPUs on the pod. Null uses the preset's count."
  type        = number
  default     = null

  validation {
    condition     = var.gpu_count == null ? true : var.gpu_count > 0 && floor(var.gpu_count) == var.gpu_count
    error_message = "gpu_count must be a positive whole number or null."
  }
}

variable "tensor_parallel_size" {
  description = "--tensor-parallel-size. Null uses gpu_count."
  type        = number
  default     = null

  validation {
    condition     = var.tensor_parallel_size == null ? true : var.tensor_parallel_size > 0 && floor(var.tensor_parallel_size) == var.tensor_parallel_size
    error_message = "tensor_parallel_size must be a positive whole number or null."
  }
}

variable "cloud_type" {
  description = "SECURE or COMMUNITY."
  type        = string
  default     = "SECURE"
}

variable "data_center_ids" {
  description = "Restrict to these data centers (e.g. [\"US-TX-3\", \"US-KS-2\"]). Empty means any."
  type        = list(string)
  default     = []
}

variable "interruptible" {
  description = "Rent as a spot pod."
  type        = bool
  default     = false
}

variable "container_disk_gb" {
  description = "Container disk size (wiped on restart)."
  type        = number
  default     = 50
}

variable "volume_gb" {
  description = "Pod volume size mounted at /workspace; HF weights are cached here."
  type        = number
  default     = 150
}

variable "expose_tcp" {
  description = "Also expose 8000/tcp for direct access, bypassing the proxy's 100s time-to-first-byte limit."
  type        = bool
  default     = false
}

# --- Readiness ---

variable "wait_for_ready" {
  description = "Block apply until /v1/models answers."
  type        = bool
  default     = true
}

variable "ready_timeout_minutes" {
  description = "How long to wait for the server to come up (image pull + weight download + load)."
  type        = number
  default     = 45
}

# --- Teardown ---

variable "teardown_after_minutes" {
  description = "Pod tears itself down this many minutes after its first container start (0 disables)."
  type        = number
  default     = 60

  validation {
    condition     = var.teardown_after_minutes >= 0 && floor(var.teardown_after_minutes) == var.teardown_after_minutes
    error_message = "teardown_after_minutes must be a whole number >= 0."
  }
}

variable "teardown_action" {
  description = "\"terminate\" deletes the pod (falls back to stop if the pod-scoped key can't delete); \"stop\" releases the GPU but keeps the volume."
  type        = string
  default     = "terminate"

  validation {
    condition     = contains(["terminate", "stop"], var.teardown_action)
    error_message = "teardown_action must be \"terminate\" or \"stop\"."
  }
}
