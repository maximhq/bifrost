output "endpoint" {
  description = "PostgreSQL server FQDN."
  value       = azurerm_postgresql_flexible_server.this.fqdn
}

output "port" {
  description = "PostgreSQL port."
  value       = "5432"
}

output "database_name" {
  description = "Database name."
  value       = azurerm_postgresql_flexible_server_database.this.name
}

output "username" {
  description = "Administrator username."
  value       = azurerm_postgresql_flexible_server.this.administrator_login
}

output "password" {
  description = "Administrator password."
  value       = local.password
  sensitive   = true
}
