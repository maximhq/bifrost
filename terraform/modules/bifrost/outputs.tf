output "service_url" {
  description = "URL to access the Bifrost service."
  value = coalesce(
    try(module.aws[0].service_url, null),
    try(module.gcp[0].service_url, null),
    try(module.azure[0].service_url, null),
    try(module.kubernetes[0].service_url, null),
  )
}

output "health_check_url" {
  description = "URL to the Bifrost health check endpoint."
  value = coalesce(
    try(module.aws[0].health_check_url, null),
    try(module.gcp[0].health_check_url, null),
    try(module.azure[0].health_check_url, null),
    try(module.kubernetes[0].health_check_url, null),
  )
}

output "config_json" {
  description = "The resolved Bifrost configuration JSON (for debugging)."
  value       = local.config_json
  sensitive   = true
}
