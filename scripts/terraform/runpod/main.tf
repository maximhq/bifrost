locals {
  presets = merge(local.builtin_presets, var.custom_presets)
  preset  = local.presets[var.model]
  kind    = try(local.preset.kind, "inference")

  # Per-engine preset block; var.engine validation guarantees it is present.
  engine_preset           = try(local.preset[var.engine], null)
  preset_tool_call_parser = try(local.engine_preset.tool_call_parser, null)
  preset_reasoning_parser = try(local.engine_preset.reasoning_parser, null)
  preset_extra_args       = try(coalesce(local.engine_preset.extra_args, []), [])
  preset_gpu_type_ids     = try(local.engine_preset.gpu_type_ids, null) != null ? local.engine_preset.gpu_type_ids : local.preset.gpu_type_ids
  ollama_model            = var.ollama_model != null ? var.ollama_model : try(local.engine_preset.model, null)

  hf_model          = coalesce(var.hf_model, local.preset.hf_model)
  served_model_name = coalesce(var.served_model_name, local.preset.served_model_name, var.model)
  tool_call_parser  = var.tool_call_parser != null ? var.tool_call_parser : (local.preset_tool_call_parser != null ? local.preset_tool_call_parser : "")
  reasoning_parser  = var.reasoning_parser != null ? var.reasoning_parser : (local.preset_reasoning_parser != null ? local.preset_reasoning_parser : "")
  max_model_len     = var.max_model_len != null ? var.max_model_len : local.preset.max_model_len
  gpu_count         = coalesce(var.gpu_count, local.preset.gpu_count)
  tp_size           = coalesce(var.tensor_parallel_size, local.gpu_count)

  channel     = coalesce(var.channel, try(local.engine_preset.channel, null), var.engine == "vllm" && local.kind == "decision" ? "nightly" : "stable")
  cuda_suffix = var.cuda_version == "12.9" ? "cu129" : ""
  default_images = {
    vllm = local.channel == "nightly" ? "vllm/vllm-openai:${join("-", compact([local.cuda_suffix, "nightly"]))}" : "vllm/vllm-openai:${join("-", compact([var.vllm_version, local.cuda_suffix]))}"
    # SGLang images are CUDA 13 only after v0.5.19 (no -cu129 tags); `dev` tracks main.
    sglang = local.channel == "nightly" ? "lmsysorg/sglang:dev" : "lmsysorg/sglang:${var.sglang_version}"
    # Ollama has no nightly image; `latest` tracks the newest release.
    ollama = local.channel == "nightly" ? "ollama/ollama:latest" : "ollama/ollama:${var.ollama_version}"
  }
  image = coalesce(var.image, local.default_images[var.engine])
  # The Ollama image bundles CUDA 12.8 and 13.0 runners, so it accepts 12.8+ hosts.
  default_cuda_versions = var.engine == "ollama" ? ["12.8", "12.9", "13.0"] : (var.cuda_version == "12.9" ? ["12.9", "13.0"] : ["13.0"])
  allowed_cuda_versions = var.allowed_cuda_versions != null ? var.allowed_cuda_versions : local.default_cuda_versions

  api_key = coalesce(var.api_key, random_password.api_key.result)

  vllm_args = concat(
    [local.hf_model],
    ["--tensor-parallel-size", tostring(local.tp_size)],
    ["--gpu-memory-utilization", tostring(coalesce(var.gpu_memory_utilization, 0.92))],
    local.max_model_len != null ? ["--max-model-len", tostring(local.max_model_len)] : [],
    local.preset.trust_remote_code ? ["--trust-remote-code"] : [],
    local.tool_call_parser != "" ? ["--enable-auto-tool-choice", "--tool-call-parser", local.tool_call_parser] : [],
    local.reasoning_parser != "" ? ["--reasoning-parser", local.reasoning_parser] : [],
    local.preset_extra_args,
    var.extra_args,
    ["--served-model-name", local.served_model_name],
    ["--host", "0.0.0.0", "--port", "8000"],
  )

  # SGLang sizes its static memory pool itself unless gpu_memory_utilization is set.
  sglang_args = concat(
    ["--model-path", local.hf_model],
    ["--tp-size", tostring(local.tp_size)],
    var.gpu_memory_utilization != null ? ["--mem-fraction-static", tostring(var.gpu_memory_utilization)] : [],
    local.max_model_len != null ? ["--context-length", tostring(local.max_model_len)] : [],
    local.preset.trust_remote_code ? ["--trust-remote-code"] : [],
    local.tool_call_parser != "" ? ["--tool-call-parser", local.tool_call_parser] : [],
    local.reasoning_parser != "" ? ["--reasoning-parser", local.reasoning_parser] : [],
    local.preset_extra_args,
    var.extra_args,
    ["--served-model-name", local.served_model_name],
    ["--host", "0.0.0.0", "--port", "8000"],
  )

  # Ollama is configured through env (below); ollama-serve.sh pulls the tag and aliases it to the served name.
  ollama_args = [coalesce(local.ollama_model, "unset"), local.served_model_name]

  server_args = { vllm = local.vllm_args, sglang = local.sglang_args, ollama = local.ollama_args }[var.engine]
  server_cmd = {
    vllm   = ["vllm", "serve"]
    sglang = ["python3", "-m", "sglang.launch_server"]
    ollama = ["/bin/bash", "-c", file("${path.module}/ollama-serve.sh"), "ollama-serve"]
  }[var.engine]

  # vLLM reads its key from VLLM_API_KEY, SGLang only takes the --api-key flag, and Ollama's nginx proxy reads PROXY_API_KEY.
  pod_args = var.engine == "sglang" ? concat(local.server_args, ["--api-key", local.api_key]) : local.server_args

  env = merge(
    { HF_HOME = "/workspace/hf" },
    var.hf_token != null ? { HF_TOKEN = var.hf_token } : {},
    # Keep the model loaded (first-request loads would blow short decision timeouts) and the store on the volume.
    var.engine == "ollama" ? { OLLAMA_MODELS = "/workspace/ollama", OLLAMA_KEEP_ALIVE = "-1" } : {},
    var.engine == "ollama" && local.max_model_len != null ? { OLLAMA_CONTEXT_LENGTH = tostring(local.max_model_len) } : {},
    local.preset.env,
    var.extra_env,
    local.teardown_enabled ? { TEARDOWN_AFTER_MINUTES = tostring(var.teardown_after_minutes), TEARDOWN_ACTION = var.teardown_action } : {},
    var.engine == "vllm" ? { VLLM_API_KEY = local.api_key } : {},
    var.engine == "ollama" ? { PROXY_API_KEY = local.api_key } : {},
  )

  # With teardown on, entrypoint.sh arms the timer and then execs the server command passed as its arguments.
  teardown_enabled = var.teardown_after_minutes > 0
  entrypoint       = local.teardown_enabled ? ["/bin/bash", "-c", file("${path.module}/entrypoint.sh"), "teardown"] : local.server_cmd
  start_cmd        = local.teardown_enabled ? concat(local.server_cmd, local.pod_args) : local.pod_args

  base_url = "https://${runpod_pod.server.id}-8000.proxy.runpod.net"

  # The key's models list keeps routing to the served model.
  bifrost_provider_name = { vllm = "vllm", sglang = "sgl", ollama = "ollama" }[var.engine]
  bifrost_key = {
    name   = local.served_model_name
    value  = local.api_key
    models = [local.served_model_name]
    weight = 1
  }
  bifrost_provider_configs = {
    vllm = {
      keys = [merge(local.bifrost_key, { vllm_key_config = { url = local.base_url, model_name = local.served_model_name } })]
    }
    sglang = {
      keys = [merge(local.bifrost_key, { sgl_key_config = { url = local.base_url } })]
    }
    ollama = {
      keys = [merge(local.bifrost_key, { ollama_key_config = { url = local.base_url } })]
    }
  }
  bifrost_provider_config = local.bifrost_provider_configs[var.engine]

  pod_spec = {
    name                  = coalesce(var.pod_name, "${var.engine}-${var.model}")
    image                 = local.image
    entrypoint            = local.entrypoint
    args                  = local.start_cmd
    env                   = local.env
    gpu_type_ids          = var.gpu_type_ids != null ? var.gpu_type_ids : local.preset_gpu_type_ids
    gpu_type_priority     = var.gpu_type_priority
    gpu_count             = local.gpu_count
    allowed_cuda_versions = local.allowed_cuda_versions
    cloud_type            = var.cloud_type
    data_center_ids       = var.data_center_ids
    interruptible         = var.interruptible
    container_disk_gb     = var.container_disk_gb
    volume_gb             = var.volume_gb
    ports                 = concat(["8000/http"], var.expose_tcp ? ["8000/tcp"] : [])
  }
}

resource "random_password" "api_key" {
  length  = 40
  special = false
}

# Any spec change replaces the pod: the provider's in-place update calls PUT /pods/{id}, which the RunPod API doesn't support.
resource "terraform_data" "pod_spec" {
  input = local.pod_spec

  lifecycle {
    precondition {
      condition     = contains(["stable", "nightly"], local.channel)
      error_message = "channel must be \"stable\" or \"nightly\"."
    }
    precondition {
      condition     = contains(["inference", "decision"], local.kind)
      error_message = "preset kind must be \"inference\" or \"decision\"."
    }
    precondition {
      condition     = local.tp_size <= local.gpu_count
      error_message = "tensor_parallel_size cannot exceed gpu_count."
    }
    precondition {
      condition     = var.engine != "ollama" || local.ollama_model != null
      error_message = "engine = \"ollama\" needs an Ollama library tag: set ollama_model, or model in the preset's ollama block."
    }
    precondition {
      condition     = var.engine != "ollama" || length(var.extra_args) == 0
      error_message = "Ollama takes no server args; configure it with extra_env (OLLAMA_* variables)."
    }
  }
}

moved {
  from = runpod_pod.vllm
  to   = runpod_pod.server
}

resource "runpod_pod" "server" {
  name                  = local.pod_spec.name
  image_name            = local.pod_spec.image
  docker_entrypoint     = local.pod_spec.entrypoint
  docker_start_cmd      = local.pod_spec.args
  env                   = local.pod_spec.env
  compute_type          = "GPU"
  cloud_type            = local.pod_spec.cloud_type
  gpu_type_ids          = local.pod_spec.gpu_type_ids
  gpu_type_priority     = local.pod_spec.gpu_type_priority
  gpu_count             = local.pod_spec.gpu_count
  allowed_cuda_versions = local.pod_spec.allowed_cuda_versions
  data_center_ids       = length(local.pod_spec.data_center_ids) > 0 ? local.pod_spec.data_center_ids : null
  interruptible         = local.pod_spec.interruptible
  container_disk_in_gb  = local.pod_spec.container_disk_gb
  volume_in_gb          = local.pod_spec.volume_gb
  volume_mount_path     = "/workspace"
  ports                 = local.pod_spec.ports

  lifecycle {
    ignore_changes       = all
    replace_triggered_by = [terraform_data.pod_spec]
  }
}

resource "terraform_data" "wait_for_ready" {
  count            = var.wait_for_ready ? 1 : 0
  triggers_replace = [runpod_pod.server.id]

  # Decision models also get one System One probe, so an image without /v1/systemone shows up at apply time.
  provisioner "local-exec" {
    command = <<-EOT
      deadline=$(( $(date +%s) + ${var.ready_timeout_minutes * 60} ))
      echo "Waiting for $MODEL on $BASE_URL/v1/models (image pull + weight download can take a while)..."
      until curl -sf --connect-timeout 10 --max-time 30 -H "Authorization: Bearer $API_KEY" "$BASE_URL/v1/models" | jq -e --arg m "$MODEL" 'any(.data[]?.id; . == $m or . == $m + ":latest")' >/dev/null 2>&1; do
        if [ "$(date +%s)" -ge "$deadline" ]; then
          echo "Timed out after ${var.ready_timeout_minutes}m; check the pod logs in the RunPod console."
          exit 1
        fi
        sleep 20
      done
      echo "$ENGINE is ready."
      if [ "$KIND" = "decision" ]; then
        body='{"model":"'"$MODEL"'","state":"The deploy finished and every health check passed.","questions":{"healthy":{"type":"noul","instructions":"Is the system healthy?"}}}'
        if ! curl -sS --fail-with-body --max-time 60 -H "Authorization: Bearer $API_KEY" -H "Content-Type: application/json" -d "$body" "$BASE_URL/v1/systemone"; then
          echo
          echo "WARNING: POST /v1/systemone failed; this image may not serve decisions for $MODEL."
        fi
        echo
      fi
    EOT
    environment = {
      BASE_URL = local.base_url
      API_KEY  = local.api_key
      ENGINE   = var.engine
      KIND     = local.kind
      MODEL    = local.served_model_name
    }
  }
}