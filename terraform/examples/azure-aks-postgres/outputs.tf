output "service_url" {
  description = "Public URL of the Bifrost LoadBalancer Service (populated once the cloud load balancer is provisioned)."
  value       = module.bifrost.external_url
}

output "health_check_url" {
  description = "Health check URL on the public Service."
  value       = module.bifrost.external_url != null ? "${module.bifrost.external_url}/health" : null
}

output "setup_token" {
  description = "Bootstrap token for creating the first admin account. Read with: terraform output -raw setup_token"
  value       = module.bifrost.setup_token
  sensitive   = true
}

output "database_endpoint" {
  description = "Managed database endpoint."
  value       = module.bifrost.database_endpoint
}

output "cluster_name" {
  description = "AKS cluster name; deploy/aks/deploy.sh reads it for get-credentials."
  value       = module.bifrost.cluster_name
}

output "cluster_resource_group" {
  description = "Cluster resource group; deploy/aks/deploy.sh reads it for get-credentials."
  value       = module.bifrost.cluster_resource_group
}
