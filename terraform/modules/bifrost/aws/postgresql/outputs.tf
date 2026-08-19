output "endpoint" {
  description = "PostgreSQL endpoint hostname."
  value       = aws_db_instance.this.address
}

output "port" {
  description = "PostgreSQL port."
  value       = tostring(aws_db_instance.this.port)
}

output "database_name" {
  description = "Database name."
  value       = aws_db_instance.this.db_name
}

output "username" {
  description = "Master username."
  value       = aws_db_instance.this.username
}

output "password" {
  description = "Master password."
  value       = local.password
  sensitive   = true
}
