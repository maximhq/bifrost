output "service_url" {
  description = "URL to access the Bifrost service."
  value = try(coalesce(
    try(module.gke[0].service_url, null),
    try(module.cloud_run[0].service_url, null),
  ), null)
}

output "health_check_url" {
  description = "URL to the Bifrost health check endpoint."
  value = try(coalesce(
    try(module.gke[0].health_check_url, null),
    try(module.cloud_run[0].health_check_url, null),
  ), null)
}

output "setup_token" {
  description = "Generated bootstrap token for the first admin account (Cloud Run with generate_secrets)."
  value       = local.generate_secrets ? random_password.setup_token[0].result : null
  sensitive   = true
}

output "database_endpoint" {
  description = "Cloud SQL connection name (Cloud Run with create_database)."
  value       = local.manage_database ? google_sql_database_instance.bifrost[0].connection_name : null
}

output "external_url" {
  description = "Public URL of the GKE LoadBalancer Service when service_type is LoadBalancer."
  value       = try(module.gke[0].external_url, null)
}

output "cluster_name" {
  description = "GKE cluster name (GKE only), for gcloud container clusters get-credentials after a targeted first apply."
  value       = try(module.gke[0].cluster_name, null)
}

output "cluster_region" {
  description = "Region the cluster was created in."
  value       = var.region
}

output "cluster_project" {
  description = "Project the cluster was created in."
  value       = var.project_id
}
