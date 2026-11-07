output "service_url" {
  description = "URL to access the Bifrost service."
  value = try(coalesce(
    try(module.gke[0].service_url, null),
    try(module.cloud_run[0].service_url, null),
  ), null)
}

output "health_check_url" {
  description = "URL to the Bifrost health check endpoint."
  value = try(coalesce(
    try(module.gke[0].health_check_url, null),
    try(module.cloud_run[0].health_check_url, null),
  ), null)
}

output "setup_token" {
  description = "Generated bootstrap token for the first admin account (Cloud Run with generate_secrets)."
  value       = local.generate_secrets ? random_password.setup_token[0].result : null
  sensitive   = true
}

output "database_endpoint" {
  description = "Cloud SQL connection name (Cloud Run with create_database)."
  value       = local.manage_database ? google_sql_database_instance.bifrost[0].connection_name : null
}
