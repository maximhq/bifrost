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

output "external_url" {
  description = "Public URL of the EKS LoadBalancer Service when service_type is LoadBalancer."
  value       = try(module.eks[0].external_url, null)
}

output "cluster_name" {
  description = "EKS cluster name (EKS only), for aws eks update-kubeconfig after a targeted first apply."
  value       = try(module.eks[0].cluster_name, null)
}

output "cluster_region" {
  description = "Region the cluster was created in."
  value       = var.region
}
