output "service_url" {
  description = "URL to access the Bifrost service."
  value = coalesce(
    try(module.aks[0].service_url, null),
    try(module.aci[0].service_url, null),
  )
}

output "health_check_url" {
  description = "URL to the Bifrost health check endpoint."
  value = coalesce(
    try(module.aks[0].health_check_url, null),
    try(module.aci[0].health_check_url, null),
  )
}

# --- PostgreSQL outputs ---

output "postgresql_endpoint" {
  description = "PostgreSQL server FQDN."
  value       = var.create_postgresql ? module.postgresql[0].endpoint : null
}

output "postgresql_port" {
  description = "PostgreSQL port."
  value       = var.create_postgresql ? module.postgresql[0].port : null
}

output "postgresql_password" {
  description = "PostgreSQL administrator password."
  value       = var.create_postgresql ? module.postgresql[0].password : null
  sensitive   = true
}
