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

output "setup_token" {
  description = "Generated bootstrap token for the first admin account (ECS with generate_secrets)."
  value       = local.generate_secrets ? random_password.setup_token[0].result : null
  sensitive   = true
}

output "database_endpoint" {
  description = "RDS endpoint as host:port (ECS with create_database)."
  value       = local.manage_database ? aws_db_instance.bifrost[0].endpoint : null
}
