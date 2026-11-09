output "service_url" {
  description = "URL to access the Bifrost service."
  value = try(coalesce(
    try(module.aws[0].service_url, null),
    try(module.gcp[0].service_url, null),
    try(module.azure[0].service_url, null),
    try(module.kubernetes[0].service_url, null),
  ), null)
}

output "health_check_url" {
  description = "URL to the Bifrost health check endpoint."
  value = try(coalesce(
    try(module.aws[0].health_check_url, null),
    try(module.gcp[0].health_check_url, null),
    try(module.azure[0].health_check_url, null),
    try(module.kubernetes[0].health_check_url, null),
  ), null)
}

output "config_json" {
  description = "The resolved Bifrost configuration JSON (for debugging)."
  value       = local.config_json
  sensitive   = true
}

output "setup_token" {
  description = "Generated bootstrap token for the first admin account (only when generate_secrets is true)."
  value = try(coalesce(
    try(module.aws[0].setup_token, null),
    try(module.gcp[0].setup_token, null),
    try(module.azure[0].setup_token, null),
  ), null)
  sensitive = true
}

output "database_endpoint" {
  description = "Connection endpoint of the managed PostgreSQL database (only when create_database is true). AWS: host:port. GCP: the Cloud SQL connection name. Azure: the Flexible Server FQDN."
  value = try(coalesce(
    try(module.aws[0].database_endpoint, null),
    try(module.gcp[0].database_endpoint, null),
    try(module.azure[0].database_endpoint, null),
  ), null)
}

output "external_url" {
  description = "Public URL of the Kubernetes LoadBalancer Service on EKS, GKE or AKS when service_type is LoadBalancer (known after apply)."
  value = try(coalesce(
    try(module.aws[0].external_url, null),
    try(module.gcp[0].external_url, null),
    try(module.azure[0].external_url, null),
  ), null)
}
