output "service_url" {
  description = "URL to access the Bifrost service."
  value = try(
    module.ecs[0].service_url,
    module.eks[0].service_url,
    null,
  )
}

output "health_check_url" {
  description = "URL to the Bifrost health check endpoint."
  value = try(
    module.ecs[0].health_check_url,
    module.eks[0].health_check_url,
    null,
  )
}

# --- PostgreSQL outputs ---

output "postgresql_endpoint" {
  description = "PostgreSQL endpoint hostname."
  value       = var.create_postgresql ? module.postgresql[0].endpoint : null
}

output "postgresql_port" {
  description = "PostgreSQL port."
  value       = var.create_postgresql ? module.postgresql[0].port : null
}

output "postgresql_password" {
  description = "PostgreSQL master password."
  value       = var.create_postgresql ? module.postgresql[0].password : null
  sensitive   = true
}
