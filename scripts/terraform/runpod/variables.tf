# --- Engine & model ---

variable "engine" {
  description = "Inference server: \"vllm\" (vllm/vllm-openai), \"sglang\" (lmsysorg/sglang), or \"ollama\" (ollama/ollama)."
  type        = string
  default     = "vllm"

  validation {
    condition     = contains(["vllm", "sglang", "ollama"], var.engine)
    error_message = "engine must be \"vllm\", \"sglang\", or \"ollama\"."
  }
  validation {
    condition     = !contains(keys(local.presets), var.model) || try(local.presets[var.model][var.engine], null) != null
    error_message = "Preset ${var.model} has no ${var.engine} config; pick another engine or add a custom preset."
  }
}

variable "model" {
  description = "Preset key from presets.tf or custom_presets (e.g. gpt-oss-20b, qwen3.6-35b-a3b, or a decision model like clef-flash)."
  type        = string
  default     = "gpt-oss-20b"

  validation {
    condition     = contains(concat(keys(local.builtin_presets), keys(var.custom_presets)), var.model)
    error_message = "Unknown model preset. Built-in: ${join(", ", keys(local.builtin_presets))}; or add it to custom_presets."
  }
}

variable "custom_presets" {
  description = "Extra presets merged over the built-in ones; select with var.model. Custom presets run on every engine; parsers go in the vllm/sglang blocks, the Ollama tag in the ollama block."
  type = map(object({
    hf_model          = string
    kind              = optional(string, "inference")
    served_model_name = optional(string)
    trust_remote_code = optional(bool, false)
    max_model_len     = optional(number)
    gpu_count         = optional(number, 1)
    gpu_type_ids      = optional(list(string), ["NVIDIA L40", "NVIDIA RTX 6000 Ada Generation", "NVIDIA L40S", "NVIDIA A100 80GB PCIe"])
    env               = optional(map(string), {})
    vllm = optional(object({
      tool_call_parser = optional(string)
      reasoning_parser = optional(string)
      channel          = optional(string)
      extra_args       = optional(list(string), [])
      gpu_type_ids     = optional(list(string))
    }), {})
    sglang = optional(object({
      tool_call_parser = optional(string)
      reasoning_parser = optional(string)
      channel          = optional(string, "stable")
      extra_args       = optional(list(string), [])
      gpu_type_ids     = optional(list(string))
    }), {})
    ollama = optional(object({
      model        = optional(string)
      gpu_type_ids = optional(list(string))
    }), {})
  }))
  default = {}

  validation {
    condition     = alltrue([for p in values(var.custom_presets) : p.gpu_count > 0 && floor(p.gpu_count) == p.gpu_count && (p.max_model_len == null ? true : p.max_model_len > 0 && floor(p.max_model_len) == p.max_model_len)])
    error_message = "custom_presets gpu_count and max_model_len must be positive whole numbers (max_model_len may be omitted)."
  }
  validation {
    condition     = alltrue([for p in values(var.custom_presets) : contains(["inference", "decision"], p.kind)])
    error_message = "custom_presets kind must be \"inference\" or \"decision\"."
  }
}

variable "hf_model" {
  description = "Override the preset's HF repo (e.g. swap to an FP8/AWQ variant). vLLM and SGLang only."
  type        = string
  default     = null
}

variable "ollama_model" {
  description = "Override the preset's Ollama library tag (e.g. gpt-oss:20b, or hf.co/<user>/<gguf-repo>:<quant>). Ollama only."
  type        = string
  default     = null
}

variable "served_model_name" {
  description = "Override the model name exposed on /v1/models."
  type        = string
  default     = null
}

variable "tool_call_parser" {
  description = "Override the preset's --tool-call-parser. Empty string disables auto tool choice. Ignored by Ollama."
  type        = string
  default     = null
}

variable "reasoning_parser" {
  description = "Override the preset's --reasoning-parser. Empty string disables it. Ignored by Ollama."
  type        = string
  default     = null
}

variable "max_model_len" {
  description = "Override the preset's context length (vLLM --max-model-len, SGLang --context-length, Ollama OLLAMA_CONTEXT_LENGTH)."
  type        = number
  default     = null

  validation {
    condition     = var.max_model_len == null ? true : var.max_model_len > 0 && floor(var.max_model_len) == var.max_model_len
    error_message = "max_model_len must be a positive whole number or null."
  }
}

variable "gpu_memory_utilization" {
  description = "vLLM --gpu-memory-utilization (null means 0.92) or SGLang --mem-fraction-static (null lets SGLang size it)."
  type        = number
  default     = null

  validation {
    condition     = var.gpu_memory_utilization == null ? true : var.gpu_memory_utilization > 0 && var.gpu_memory_utilization <= 1
    error_message = "gpu_memory_utilization must be in (0, 1]."
  }
}

variable "extra_args" {
  description = "Extra server args appended after the preset's (e.g. [\"--kv-cache-dtype\", \"fp8\"]). Flag names differ per engine."
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
  description = "Server API key (vLLM VLLM_API_KEY, SGLang --api-key, Ollama's nginx proxy). Generated when null."
  type        = string
  default     = null
  sensitive   = true

  validation {
    condition     = var.api_key == null ? true : can(regex("^[A-Za-z0-9._~+/=-]+$", var.api_key))
    error_message = "api_key may only contain letters, digits and ._~+/=- (it is embedded in a bearer-token check)."
  }
}

# --- Image ---

variable "channel" {
  description = "\"stable\" or \"nightly\". Null uses the preset's channel for the engine."
  type        = string
  default     = null
}

variable "vllm_version" {
  description = "Stable release tag used when channel is stable. v0.30.0 is the base of RunPod's own vLLM worker image, so its layers are the likeliest to be cached on hosts."
  type        = string
  default     = "v0.30.0"
}

variable "sglang_version" {
  description = "Stable lmsysorg/sglang tag used when channel is stable. v0.5.21 is the first with /v1/systemone (CUDA 13 only)."
  type        = string
  default     = "v0.5.21"
}

variable "ollama_version" {
  description = "ollama/ollama tag used when channel is stable. 0.40.0+ serves all of nimble, tev1, clef and clef-flash on /v1/systemone."
  type        = string
  default     = "0.40.4"
}

variable "cuda_version" {
  description = "Image CUDA build: \"13.0\" or \"12.9\" (12.9 is vLLM only: it runs on more RunPod hosts, but the v0.30.0+ cu129 images ship a mismatched torch, vllm#59157)."
  type        = string
  default     = "13.0"

  validation {
    condition     = contains(["12.9", "13.0"], var.cuda_version)
    error_message = "cuda_version must be \"12.9\" or \"13.0\"."
  }
  validation {
    condition     = var.cuda_version == "13.0" || var.engine == "vllm"
    error_message = "SGLang publishes no CUDA 12.9 images after v0.5.19; use cuda_version = \"13.0\"."
  }
}

variable "image" {
  description = "Full image override, e.g. vllm/vllm-openai:nightly-<sha>, vllm/vllm-openai:qwen38, or lmsysorg/sglang:nightly-dev-<date>-<sha>."
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
  description = "Pod name. Defaults to <engine>-<model>."
  type        = string
  default     = null
}

variable "gpu_type_ids" {
  description = "RunPod GPU type ids in preference order. Null uses the preset's list."
  type        = list(string)
  default     = null
}

variable "gpu_type_priority" {
  description = "\"custom\" rents GPU types in list order (presets order them by $/hr, lowest first); \"availability\" lets RunPod pick."
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
