# --- Placement ---

variable "region" {
  description = "Region the endpoint runs in; also the region in the emitted Bifrost key config."
  type        = string
  default     = "us-east-1"
}

variable "tags" {
  description = "Extra tags for every resource (all resources also get bifrost-test = true)."
  type        = map(string)
  default     = {}
}

# --- Engine & model ---

variable "engine" {
  description = "Serving container: vllm, sglang, lmi (DJL LMI), llamacpp, tei, jumpstart, or custom."
  type        = string
  default     = "vllm"

  validation {
    condition     = contains(["vllm", "sglang", "lmi", "llamacpp", "tei", "jumpstart", "custom"], var.engine)
    error_message = "engine must be one of vllm, sglang, lmi, llamacpp, tei, jumpstart, custom."
  }
  validation {
    condition     = contains(["custom"], var.engine) || (var.engine == "jumpstart" && var.jumpstart_model_id != null) || !contains(keys(local.presets), var.model) || try(local.presets[var.model][var.engine], null) != null
    error_message = "Preset ${var.model} has no ${var.engine} block; pick another engine or model, or add a custom preset."
  }
}

variable "model" {
  description = "Preset key from presets.tf or custom_presets."
  type        = string
  default     = "qwen3-4b"

  validation {
    condition     = contains(concat(keys(local.builtin_presets), keys(var.custom_presets)), var.model)
    error_message = "Unknown model preset. Built-in: ${join(", ", keys(local.builtin_presets))}; or add it to custom_presets."
  }
}

variable "custom_presets" {
  description = "Extra presets merged over the built-in ones; select with var.model. Same shape as presets.tf."
  type = map(object({
    kind              = optional(string, "chat")
    hf_model          = string
    served_model_name = optional(string)
    max_model_len     = optional(number)
    trust_remote_code = optional(bool, false)
    gpu_count         = optional(number, 1)
    instance_type     = optional(string, "ml.g6.2xlarge")
    env               = optional(map(string), {})
    vllm = optional(object({
      tool_call_parser = optional(string)
      reasoning_parser = optional(string)
      instance_type    = optional(string)
      env              = optional(map(string), {})
    }))
    sglang = optional(object({
      tool_call_parser = optional(string)
      reasoning_parser = optional(string)
      instance_type    = optional(string)
      env              = optional(map(string), {})
    }))
    lmi = optional(object({
      tool_call_parser = optional(string)
      reasoning_parser = optional(string)
      instance_type    = optional(string)
      env              = optional(map(string), {})
    }))
    llamacpp = optional(object({
      hf_repo       = string
      hf_file       = string
      instance_type = optional(string)
      env           = optional(map(string), {})
    }))
    tei = optional(object({
      instance_type = optional(string)
      env           = optional(map(string), {})
    }))
    jumpstart = optional(object({
      model_id      = string
      version       = optional(string)
      config        = optional(string)
      instance_type = optional(string)
    }))
  }))
  default = {}

  validation {
    condition     = alltrue([for p in values(var.custom_presets) : contains(["chat", "embedding"], p.kind)])
    error_message = "custom_presets kind must be \"chat\" or \"embedding\"."
  }
  validation {
    condition     = alltrue([for p in values(var.custom_presets) : p.gpu_count >= 0 && floor(p.gpu_count) == p.gpu_count])
    error_message = "custom_presets gpu_count must be a whole number >= 0."
  }
}

variable "hf_model" {
  description = "Override the preset's HF repo (vllm, sglang, lmi, tei)."
  type        = string
  default     = null
}

variable "served_model_name" {
  description = "Override the model name the container serves (vllm, sglang)."
  type        = string
  default     = null
}

variable "max_model_len" {
  description = "Override the preset's context length (SM_VLLM_MAX_MODEL_LEN, SM_SGLANG_CONTEXT_LENGTH, OPTION_MAX_MODEL_LEN, SM_LLAMA_CPP_CTX_SIZE)."
  type        = number
  default     = null

  validation {
    condition     = var.max_model_len == null ? true : var.max_model_len > 0 && floor(var.max_model_len) == var.max_model_len
    error_message = "max_model_len must be a positive whole number or null."
  }
}

variable "tool_call_parser" {
  description = "Override the preset's tool-call parser (vllm, sglang, lmi). Empty string disables tool calling."
  type        = string
  default     = null
}

variable "reasoning_parser" {
  description = "Override the preset's reasoning parser (vllm, sglang, lmi). Empty string disables it."
  type        = string
  default     = null
}

variable "extra_env" {
  description = "Container env merged over everything else. Engine flags go here: SM_VLLM_*, SM_SGLANG_*, OPTION_*, SM_LLAMA_CPP_*."
  type        = map(string)
  default     = {}
}

variable "hf_token" {
  description = "HuggingFace token for gated repos. Stored in state and in the SageMaker model env in plain text."
  type        = string
  default     = null
  sensitive   = true
}

variable "kind" {
  description = "Override the workload kind the probe and outputs assume: chat or embedding."
  type        = string
  default     = null

  validation {
    condition     = var.kind == null ? true : contains(["chat", "embedding"], var.kind)
    error_message = "kind must be \"chat\" or \"embedding\"."
  }
}

# --- Image ---

variable "image" {
  description = "Full image URI override. Required for engine = custom."
  type        = string
  default     = null
}

variable "image_tag" {
  description = "Tag-only override on the engine's default repository (e.g. 0.31.0-gpu-py312-cu130-ubuntu24.04-sagemaker for vllm)."
  type        = string
  default     = null
}

variable "inference_ami_version" {
  description = "ProductionVariant InferenceAmiVersion. Null derives it: al2023-ami-sagemaker-inference-gpu-4-1 for CUDA 13 images on GPUs, unset otherwise. \"\" forces unset."
  type        = string
  default     = null
}

variable "model_data_url" {
  description = "S3 URI of a model.tar.gz (or prefix ending in /) mounted at /opt/ml/model. Optional for every engine except jumpstart."
  type        = string
  default     = null
}

# --- JumpStart ---

variable "jumpstart_model_id" {
  description = "Deploy any JumpStart model id (e.g. huggingface-reasoning-qwen3-4b) instead of the preset's jumpstart block."
  type        = string
  default     = null
}

variable "jumpstart_model_version" {
  description = "Spec version for jumpstart_model_id. Null pins the preset's version, or reads the newest from models_manifest.json (8 MB download per plan)."
  type        = string
  default     = null
}

variable "jumpstart_config" {
  description = "JumpStart inference config name (e.g. lmi, lmi-optimized, all_purpose). Null uses the preset's, else the spec's top ranking."
  type        = string
  default     = null
}

variable "accept_eula" {
  description = "Accept the model EULA for gated JumpStart models (Llama and similar). Read the EULA first; jumpstart_eula_url prints it."
  type        = bool
  default     = false
}

# --- Endpoint ---

variable "endpoint_name" {
  description = "Endpoint name. Defaults to bifrost-<engine>-<model>-<random>. Must start with bifrost- (the deployer policy's scope), match ^[a-zA-Z0-9](-*[a-zA-Z0-9])*$, and be at most 63 characters."
  type        = string
  default     = null

  validation {
    condition     = var.endpoint_name == null ? true : (length(var.endpoint_name) <= 63 && startswith(var.endpoint_name, "bifrost-") && can(regex("^[a-zA-Z0-9](-*[a-zA-Z0-9])*$", var.endpoint_name)))
    error_message = "endpoint_name must start with bifrost-, match ^[a-zA-Z0-9](-*[a-zA-Z0-9])*$, and be at most 63 characters."
  }
}

variable "instance_type" {
  description = "Override the preset's instance type, e.g. ml.g6e.2xlarge."
  type        = string
  default     = null

  validation {
    condition     = var.instance_type == null ? true : can(regex("^ml\\.[a-z0-9-]+\\.[a-z0-9]+$", var.instance_type))
    error_message = "instance_type must look like ml.<family>.<size>, e.g. ml.g6.2xlarge."
  }
}

variable "instance_count" {
  description = "Initial instance count of the (first) production variant."
  type        = number
  default     = 1

  validation {
    condition     = var.instance_count >= 1 && floor(var.instance_count) == var.instance_count
    error_message = "instance_count must be a whole number >= 1."
  }
}

variable "gpu_count" {
  description = "Tensor-parallel size. Null uses the preset's (match it to the instance's GPU count, e.g. 4 on ml.g6e.12xlarge)."
  type        = number
  default     = null

  validation {
    condition     = var.gpu_count == null ? true : var.gpu_count >= 1 && floor(var.gpu_count) == var.gpu_count
    error_message = "gpu_count must be a whole number >= 1 or null."
  }
}

variable "volume_size_gb" {
  description = "Per-instance storage volume for weights (1-512). Null uses the instance default (instances with NVMe instance storage reject an explicit size)."
  type        = number
  default     = null
}

variable "health_check_timeout_seconds" {
  description = "ContainerStartupHealthCheckTimeoutInSeconds (60-3600). Weight download counts against it."
  type        = number
  default     = 1800

  validation {
    condition     = var.health_check_timeout_seconds >= 60 && var.health_check_timeout_seconds <= 3600
    error_message = "health_check_timeout_seconds must be between 60 and 3600."
  }
}

variable "routing_strategy" {
  description = "LEAST_OUTSTANDING_REQUESTS or RANDOM."
  type        = string
  default     = "LEAST_OUTSTANDING_REQUESTS"

  validation {
    condition     = contains(["LEAST_OUTSTANDING_REQUESTS", "RANDOM"], var.routing_strategy)
    error_message = "routing_strategy must be LEAST_OUTSTANDING_REQUESTS or RANDOM."
  }
}

variable "inference_components" {
  description = "Deploy the model as an inference component, so it is addressed as /endpoints/<ep>/inference-components/<ic>."
  type        = bool
  default     = false
}

variable "ic_min_memory_mb" {
  description = "Inference component MinMemoryRequiredInMb, enforced as the container's memory limit. Null uses the JumpStart spec's requirement, else 8192 (vLLM fails to load in 1024)."
  type        = number
  default     = null
}

variable "ic_cpu_cores" {
  description = "Inference component NumberOfCpuCoresRequired."
  type        = number
  default     = 2
}

variable "second_variant" {
  description = "Add a second production variant (same model, weight 0) so X-Amzn-SageMaker-Target-Variant has something to target. Runs and bills a second instance."
  type        = bool
  default     = false
}

# --- Bifrost ---

variable "api" {
  description = "Route the endpoint is probed on: openai or invocations. Null picks openai for vllm/sglang and invocations otherwise. Not part of the Bifrost key, which carries region only."
  type        = string
  default     = null

  validation {
    condition     = var.api == null ? true : contains(["openai", "invocations"], var.api)
    error_message = "api must be \"openai\" or \"invocations\"."
  }
}

# --- IAM ---

variable "execution_role_name" {
  description = "Role the endpoint's containers run as, created once by an admin (README, \"One-time admin setup\")."
  type        = string
  default     = "bifrost-sagemaker-exec"
}

# --- Readiness ---

variable "wait_for_ready" {
  description = "After the endpoint is InService, run probe.py --quick against it (needs python3 and the aws CLI locally)."
  type        = bool
  default     = true
}

# --- Teardown ---

variable "teardown_after_minutes" {
  description = "EventBridge Scheduler deletes the endpoint this many minutes after the latest apply (0 disables). Runs even if this machine is offline."
  type        = number
  default     = 60

  validation {
    condition     = var.teardown_after_minutes >= 0 && floor(var.teardown_after_minutes) == var.teardown_after_minutes
    error_message = "teardown_after_minutes must be a whole number >= 0."
  }
}

variable "teardown_role_name" {
  description = "Role EventBridge Scheduler assumes to delete the endpoint, created once by an admin (README, \"One-time admin setup\")."
  type        = string
  default     = "bifrost-sagemaker-teardown"
}
