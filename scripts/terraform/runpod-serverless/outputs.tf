output "endpoint_ids" {
  value = { for name, e in runpod_serverless_endpoint.model : name => e.id }
}

output "base_urls" {
  description = "OpenAI-compatible roots; clients use <base_url>/v1."
  value       = local.base_urls
}

output "network_volume_id" {
  value = runpod_network_volume.cache.id
}

output "bifrost_provider_config" {
  description = "Drop-in providers.vllm entry for Bifrost config.json. Keys read RUNPOD_API_KEY from Bifrost's environment."
  value       = local.bifrost_provider_config
}
