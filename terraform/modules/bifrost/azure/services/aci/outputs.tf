output "container_group_name" {
  description = "Name of the Azure Container Group."
  value       = azurerm_container_group.bifrost.name
}

output "fqdn" {
  description = "FQDN of the container group."
  value       = azurerm_container_group.bifrost.fqdn
}

output "ip_address" {
  description = "Public IP address of the container group."
  value       = azurerm_container_group.bifrost.ip_address
}

output "service_url" {
  description = "URL to access the Bifrost service."
  value       = "http://${azurerm_container_group.bifrost.fqdn}:${var.container_port}"
}

output "health_check_url" {
  description = "URL to the Bifrost health check endpoint."
  value       = "http://${azurerm_container_group.bifrost.fqdn}:${var.container_port}${var.health_check_path}"
}

# Names only (never values): lets the platform-module tests check that the
# managed database settings and generated secrets reach the planned container.
output "environment_keys" {
  description = "Environment variable names set on the Bifrost container."
  value       = nonsensitive(keys(azurerm_container_group.bifrost.container[0].environment_variables))
}

output "secure_environment_keys" {
  description = "Secure environment variable names set on the Bifrost container."
  value       = nonsensitive(keys(azurerm_container_group.bifrost.container[0].secure_environment_variables))
}
