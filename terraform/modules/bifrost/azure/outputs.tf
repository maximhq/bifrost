output "service_url" {
  description = "URL to access the Bifrost service."
  value = try(coalesce(
    try(module.aks[0].service_url, null),
    try(module.aci[0].service_url, null),
  ), null)
}

output "health_check_url" {
  description = "URL to the Bifrost health check endpoint."
  value = try(coalesce(
    try(module.aks[0].health_check_url, null),
    try(module.aci[0].health_check_url, null),
  ), null)
}

output "setup_token" {
  description = "Generated bootstrap token for the first admin account (ACI with generate_secrets)."
  value       = local.generate_secrets ? random_password.setup_token[0].result : null
  sensitive   = true
}

output "database_endpoint" {
  description = "Flexible Server FQDN (ACI with create_database)."
  value       = local.manage_database ? azurerm_postgresql_flexible_server.bifrost[0].fqdn : null
}
