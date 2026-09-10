output "namespace" {
  description = "Kubernetes namespace where Bifrost is deployed."
  value       = kubernetes_namespace_v1.bifrost.metadata[0].name
}

output "service_name" {
  description = "Name of the Kubernetes service."
  value       = kubernetes_service_v1.bifrost.metadata[0].name
}

output "service_url" {
  description = "URL to access the Bifrost service."
  value = (
    var.domain_name != null
    ? "http://${var.domain_name}"
    : "http://${kubernetes_service_v1.bifrost.metadata[0].name}.${kubernetes_namespace_v1.bifrost.metadata[0].name}.svc.cluster.local"
  )
}

output "health_check_url" {
  description = "URL to the Bifrost health check endpoint."
  value = (
    var.domain_name != null
    ? "http://${var.domain_name}/health"
    : "http://${kubernetes_service_v1.bifrost.metadata[0].name}.${kubernetes_namespace_v1.bifrost.metadata[0].name}.svc.cluster.local/health"
  )
}

# --- PostgreSQL outputs ---

output "postgresql_endpoint" {
  description = "PostgreSQL service DNS name."
  value       = var.create_postgresql ? "${kubernetes_service_v1.postgresql[0].metadata[0].name}.${kubernetes_namespace_v1.bifrost.metadata[0].name}.svc.cluster.local" : null
}

output "postgresql_port" {
  description = "PostgreSQL port."
  value       = var.create_postgresql ? "5432" : null
}

output "postgresql_password" {
  description = "PostgreSQL password."
  value       = local.pg_password
  sensitive   = true
}
