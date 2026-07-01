output "endpoint" {
  description = "PostgreSQL private IP address."
  value       = google_sql_database_instance.this.private_ip_address
}

output "port" {
  description = "PostgreSQL port."
  value       = "5432"
}

output "database_name" {
  description = "Database name."
  value       = google_sql_database.this.name
}

output "username" {
  description = "Database username."
  value       = google_sql_user.this.name
}

output "password" {
  description = "Database password."
  value       = local.password
  sensitive   = true
}
