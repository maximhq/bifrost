locals {
  presets = merge(local.builtin_presets, var.custom_presets)
  preset  = local.presets[var.model]

  hf_model          = coalesce(var.hf_model, local.preset.hf_model)
  served_model_name = coalesce(var.served_model_name, local.preset.served_model_name, var.model)
  tool_call_parser  = var.tool_call_parser != null ? var.tool_call_parser : (local.preset.tool_call_parser != null ? local.preset.tool_call_parser : "")
  reasoning_parser  = var.reasoning_parser != null ? var.reasoning_parser : (local.preset.reasoning_parser != null ? local.preset.reasoning_parser : "")
  max_model_len     = var.max_model_len != null ? var.max_model_len : local.preset.max_model_len
  gpu_count         = coalesce(var.gpu_count, local.preset.gpu_count)
  tp_size           = coalesce(var.tensor_parallel_size, local.gpu_count)

  channel     = coalesce(var.vllm_channel, local.preset.vllm_channel)
  cuda_suffix = var.cuda_version == "12.9" ? "cu129" : ""
  image = coalesce(
    var.vllm_image,
    local.channel == "nightly"
    ? "vllm/vllm-openai:${join("-", compact([local.cuda_suffix, "nightly"]))}"
    : "vllm/vllm-openai:${join("-", compact([var.vllm_version, local.cuda_suffix]))}"
  )
  allowed_cuda_versions = var.allowed_cuda_versions != null ? var.allowed_cuda_versions : (var.cuda_version == "12.9" ? ["12.9", "13.0"] : ["13.0"])

  api_key = coalesce(var.api_key, random_password.api_key.result)

  # The image ENTRYPOINT is ["vllm", "serve"], so this is just the serve args.
  vllm_args = concat(
    [local.hf_model],
    ["--served-model-name", local.served_model_name],
    ["--host", "0.0.0.0", "--port", "8000"],
    ["--tensor-parallel-size", tostring(local.tp_size)],
    ["--gpu-memory-utilization", tostring(var.gpu_memory_utilization)],
    local.max_model_len != null ? ["--max-model-len", tostring(local.max_model_len)] : [],
    local.preset.trust_remote_code ? ["--trust-remote-code"] : [],
    local.tool_call_parser != "" ? ["--enable-auto-tool-choice", "--tool-call-parser", local.tool_call_parser] : [],
    local.reasoning_parser != "" ? ["--reasoning-parser", local.reasoning_parser] : [],
    local.preset.extra_args,
    var.extra_args,
  )

  env = merge(
    { HF_HOME = "/workspace/hf", VLLM_API_KEY = local.api_key },
    var.hf_token != null ? { HF_TOKEN = var.hf_token } : {},
    local.preset.env,
    var.extra_env,
    local.teardown_enabled ? { TEARDOWN_AFTER_MINUTES = tostring(var.teardown_after_minutes), TEARDOWN_ACTION = var.teardown_action } : {},
  )

  # Wrap the image's `vllm serve` entrypoint with the self-teardown timer; "$@" receives vllm_args.
  teardown_enabled = var.teardown_after_minutes > 0
  entrypoint       = local.teardown_enabled ? ["/bin/bash", "-c", file("${path.module}/teardown.sh"), "teardown"] : null

  base_url = "https://${runpod_pod.vllm.id}-8000.proxy.runpod.net"

  pod_spec = {
    name                  = coalesce(var.pod_name, "vllm-${var.model}")
    image                 = local.image
    entrypoint            = local.entrypoint
    args                  = local.vllm_args
    env                   = local.env
    gpu_type_ids          = var.gpu_type_ids != null ? var.gpu_type_ids : local.preset.gpu_type_ids
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
      error_message = "vllm_channel must be \"stable\" or \"nightly\"."
    }
    precondition {
      condition     = local.tp_size <= local.gpu_count
      error_message = "tensor_parallel_size cannot exceed gpu_count."
    }
  }
}

resource "runpod_pod" "vllm" {
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
  triggers_replace = [runpod_pod.vllm.id]

  provisioner "local-exec" {
    command = <<-EOT
      deadline=$(( $(date +%s) + ${var.ready_timeout_minutes * 60} ))
      echo "Waiting for $BASE_URL/v1/models (image pull + weight download can take a while)..."
      until curl -sf -o /dev/null -H "Authorization: Bearer $VLLM_API_KEY" "$BASE_URL/v1/models"; do
        if [ "$(date +%s)" -ge "$deadline" ]; then
          echo "Timed out after ${var.ready_timeout_minutes}m; check the pod logs in the RunPod console."
          exit 1
        fi
        sleep 20
      done
      echo "vLLM is ready."
    EOT
    environment = {
      BASE_URL     = local.base_url
      VLLM_API_KEY = local.api_key
    }
  }
}
