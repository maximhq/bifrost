output "pod_id" {
  value = runpod_pod.server.id
}

output "engine" {
  value = var.engine
}

output "kind" {
  description = "\"inference\" (OpenAI-compatible) or \"decision\" (POST /v1/systemone)."
  value       = local.kind
}

output "base_url" {
  description = "Server root (Bifrost key/network URL). OpenAI clients use <base_url>/v1; decision models answer on <base_url>/v1/systemone."
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

output "server_args" {
  description = "Server args without the API key."
  value       = local.server_args
}

output "teardown" {
  value = local.teardown_enabled ? "${var.teardown_action} ${var.teardown_after_minutes}m after first container start" : "disabled"
}

output "cost_per_hr" {
  value = runpod_pod.server.cost_per_hr
}

output "public_ip" {
  description = "Populated after the pod starts; run `terraform apply -refresh-only` if empty. TCP port mapping is in the RunPod console."
  value       = runpod_pod.server.public_ip
}

output "bifrost_provider_name" {
  description = "Key to paste bifrost_provider_config under in config.json providers: vllm or sgl or ollama."
  value       = local.bifrost_provider_name
}

output "bifrost_provider_config" {
  description = "Drop-in providers.<bifrost_provider_name> entry for Bifrost config.json."
  sensitive   = true
  value       = local.bifrost_provider_config
}