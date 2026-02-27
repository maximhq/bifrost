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
