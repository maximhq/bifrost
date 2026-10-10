output "pod_id" {
  value = runpod_pod.vllm.id
}

output "base_url" {
  description = "Server root (Bifrost vllm_key_config.url). OpenAI clients use <base_url>/v1."
  value       = local.base_url
}

output "served_model_name" {
  value = local.served_model_name
}

output "api_key" {
  value     = local.api_key
  sensitive = true
}

output "image" {
  value = local.image
}

output "vllm_args" {
  value = local.vllm_args
}

output "teardown" {
  value = local.teardown_enabled ? "${var.teardown_action} ${var.teardown_after_minutes}m after first container start" : "disabled"
}

output "cost_per_hr" {
  value = runpod_pod.vllm.cost_per_hr
}

output "public_ip" {
  description = "Populated after the pod starts; run `terraform apply -refresh-only` if empty. TCP port mapping is in the RunPod console."
  value       = runpod_pod.vllm.public_ip
}

output "bifrost_provider_config" {
  description = "Drop-in providers.vllm entry for Bifrost config.json."
  sensitive   = true
  value = {
    keys = [{
      name   = local.served_model_name
      value  = local.api_key
      models = [local.served_model_name]
      weight = 1
      vllm_key_config = {
        url        = local.base_url
        model_name = local.served_model_name
      }
    }]
  }
}
