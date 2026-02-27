output "service_url" {
  description = "URL to access the Bifrost service."
  value       = module.bifrost.service_url
}

output "health_check_url" {
  description = "URL to the Bifrost health check endpoint."
  value       = module.bifrost.health_check_url
}
