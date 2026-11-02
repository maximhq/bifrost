locals {
  presets = { for name in var.models : name => local.builtin_presets[name] }

  env = {
    for name, p in local.presets : name => merge(
      {
        MODEL_NAME                        = p.hf_model
        OPENAI_SERVED_MODEL_NAME_OVERRIDE = name
        MAX_MODEL_LEN                     = tostring(p.max_model_len)
        TENSOR_PARALLEL_SIZE              = tostring(p.gpu_count)
        GPU_MEMORY_UTILIZATION            = tostring(var.gpu_memory_utilization)
        ENABLE_AUTO_TOOL_CHOICE           = "true"
        TOOL_CALL_PARSER                  = p.tool_call_parser
      },
      p.reasoning_parser != "" ? { REASONING_PARSER = p.reasoning_parser } : {},
      var.hf_token != null ? { HF_TOKEN = var.hf_token } : {},
      p.env,
      lookup(var.extra_env, name, {}),
    )
  }

  # OpenAI-compatible root for Bifrost's vllm provider, which appends /v1/chat/completions etc.
  base_urls = { for name, e in runpod_serverless_endpoint.model : name => "https://api.runpod.ai/v2/${e.id}/openai" }

  # RunPod authenticates with the account API key; Bifrost resolves env.* at load time, so no secret lands in state output.
  bifrost_provider_config = {
    keys = [
      for name in sort(keys(local.presets)) : {
        name            = "runpod-${name}"
        value           = "env.RUNPOD_API_KEY"
        models          = [name]
        weight          = 1
        vllm_key_config = { url = local.base_urls[name], model_name = name }
      }
    ]
  }
}

# Holds HF weights and vLLM's compile cache (the worker mounts it at /runpod-volume), so only the first cold start downloads.
resource "runpod_network_volume" "cache" {
  name        = "${var.name_prefix}-vllm-cache"
  data_center = var.data_center
  size        = var.volume_gb
}

resource "runpod_serverless_endpoint" "model" {
  for_each = local.presets

  name          = "${var.name_prefix}-${each.key}"
  endpoint_type = "QUEUE"
  image         = var.worker_image
  env           = local.env[each.key]

  gpu_pools        = each.value.gpu_pools
  gpu_count        = each.value.gpu_count
  excluded_types   = var.excluded_gpu_types
  min_cuda_version = "13.0" # vLLM v0.32.0 image
  disk             = 30

  network_volumes = [runpod_network_volume.cache.id]
  data_center_ids = [var.data_center]

  workers_min   = var.workers_min
  workers_max   = var.workers_max
  idle_timeout  = var.idle_timeout
  flashboot     = var.flashboot
  timeout       = var.execution_timeout_ms
  scaling_type  = "QUEUE_DELAY"
  scaling_value = 4
}

resource "terraform_data" "warm_up" {
  for_each = var.warm_up ? local.presets : {}

  triggers_replace = [runpod_serverless_endpoint.model[each.key].id, local.env[each.key]]

  provisioner "local-exec" {
    interpreter = ["/bin/bash", "-c"]
    command     = <<-EOT
      set -u
      deadline=$(( $(date +%s) + TIMEOUT_MIN * 60 ))
      body='{"model":"'"$MODEL"'","messages":[{"role":"user","content":"hi"}],"max_tokens":1}'
      echo "Warming $MODEL at $BASE_URL (first cold start downloads weights; can take 10+ min)..."
      while [ "$(date +%s)" -lt "$deadline" ]; do
        code=$(curl -s -o /tmp/warm_up_$$.json -w '%%{http_code}' --max-time 300 \
          -H "Authorization: Bearer $RUNPOD_API_KEY" -H 'Content-Type: application/json' \
          -d "$body" "$BASE_URL/v1/chat/completions" || true)
        if [ "$code" = "200" ]; then echo "$MODEL ready"; rm -f /tmp/warm_up_$$.json; exit 0; fi
        echo "  $MODEL not ready (HTTP $code): $(head -c 300 /tmp/warm_up_$$.json 2>/dev/null)"
        sleep 20
      done
      echo "Timed out warming $MODEL; check the endpoint's worker logs in the RunPod console." >&2
      exit 1
    EOT
    environment = {
      MODEL       = each.key
      BASE_URL    = local.base_urls[each.key]
      TIMEOUT_MIN = var.warm_up_timeout_minutes
    }
  }
}