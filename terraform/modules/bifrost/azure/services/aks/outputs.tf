output "cluster_name" {
  description = "Name of the AKS cluster."
  value       = var.create_cluster ? azurerm_kubernetes_cluster.this[0].name : null
}

output "cluster_fqdn" {
  description = "FQDN of the AKS cluster."
  value       = var.create_cluster ? azurerm_kubernetes_cluster.this[0].fqdn : null
}

output "namespace" {
  description = "Kubernetes namespace where Bifrost is deployed."
  value       = kubernetes_namespace.this.metadata[0].name
}

output "service_name" {
  description = "Name of the Kubernetes service."
  value       = kubernetes_service.bifrost.metadata[0].name
}

output "ingress_ip" {
  description = "IP address of the Ingress (if created)."
  value       = var.create_load_balancer ? try(kubernetes_ingress_v1.bifrost[0].status[0].load_balancer[0].ingress[0].ip, null) : null
}

output "service_url" {
  description = "URL to access the Bifrost service."
  value = var.create_load_balancer ? (
    var.domain_name != null ? "http://${var.domain_name}" : (
      try("http://${kubernetes_ingress_v1.bifrost[0].status[0].load_balancer[0].ingress[0].ip}", null)
    )
  ) : "http://${kubernetes_service.bifrost.metadata[0].name}.${kubernetes_namespace.this.metadata[0].name}.svc.cluster.local"
}

output "health_check_url" {
  description = "URL to the Bifrost health check endpoint."
  value = var.create_load_balancer ? (
    var.domain_name != null ? "http://${var.domain_name}${var.health_check_path}" : (
      try("http://${kubernetes_ingress_v1.bifrost[0].status[0].load_balancer[0].ingress[0].ip}${var.health_check_path}", null)
    )
  ) : "http://${kubernetes_service.bifrost.metadata[0].name}.${kubernetes_namespace.this.metadata[0].name}.svc.cluster.local${var.health_check_path}"
}
