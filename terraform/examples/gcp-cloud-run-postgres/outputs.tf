output "service_url" {
  description = "URL to access the Bifrost service."
  value       = module.bifrost.service_url
}

output "health_check_url" {
  description = "URL to the Bifrost health check endpoint."
  value       = module.bifrost.health_check_url
}

output "setup_token" {
  description = "Bootstrap token for creating the first admin account. Read with: terraform output -raw setup_token"
  value       = module.bifrost.setup_token
  sensitive   = true
}

output "database_endpoint" {
  description = "Cloud SQL connection name."
  value       = module.bifrost.database_endpoint
}
