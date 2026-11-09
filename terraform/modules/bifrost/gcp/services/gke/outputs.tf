output "cluster_name" {
  description = "Name of the GKE cluster."
  value       = try(google_container_cluster.bifrost[0].name, null)
}

output "cluster_endpoint" {
  description = "Endpoint of the GKE cluster."
  value       = try(google_container_cluster.bifrost[0].endpoint, null)
}

output "namespace" {
  description = "Kubernetes namespace where Bifrost is deployed."
  value       = kubernetes_namespace.bifrost.metadata[0].name
}

output "service_name" {
  description = "Name of the Kubernetes service."
  value       = kubernetes_service.bifrost.metadata[0].name
}

output "ingress_ip" {
  description = "Static IP address of the Ingress (if domain_name is set)."
  value       = try(google_compute_global_address.bifrost[0].address, null)
}

output "service_url" {
  description = "URL to access the Bifrost service."
  value = (
    var.domain_name != null
    ? "http://${var.domain_name}"
    : "http://${kubernetes_service.bifrost.metadata[0].name}.${kubernetes_namespace.bifrost.metadata[0].name}.svc.cluster.local"
  )
}

output "health_check_url" {
  description = "URL to the Bifrost health check endpoint."
  value = (
    var.domain_name != null
    ? "http://${var.domain_name}${trimsuffix(var.ingress_path, "/")}${var.health_check_path}"
    : "http://${kubernetes_service.bifrost.metadata[0].name}.${kubernetes_namespace.bifrost.metadata[0].name}.svc.cluster.local${var.health_check_path}"
  )
}

output "external_url" {
  description = "Public URL of the LoadBalancer Service when service_type is LoadBalancer (known after apply)."
  value = var.service_type == "LoadBalancer" ? try(
    "http://${coalesce(
      try(kubernetes_service.bifrost.status[0].load_balancer[0].ingress[0].hostname, ""),
      try(kubernetes_service.bifrost.status[0].load_balancer[0].ingress[0].ip, ""),
    )}",
    null,
  ) : null
}
