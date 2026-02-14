# --- Infrastructure ---
variable "name_prefix" {
  description = "Prefix for all resource names."
  type        = string
}

variable "region" {
  description = "Azure region."
  type        = string
}

variable "resource_group_name" {
  description = "Azure resource group name."
  type        = string
}

variable "tags" {
  description = "Tags to apply to all resources."
  type        = map(string)
}

# --- Bifrost config ---
variable "config_json" {
  description = "Complete Bifrost config.json as a string."
  type        = string
  sensitive   = true
}

# --- Image ---
variable "image" {
  description = "Full Docker image reference (repository:tag)."
  type        = string
}

# --- Container ---
variable "container_port" {
  description = "Port the Bifrost container listens on."
  type        = number
}

variable "health_check_path" {
  description = "HTTP path for health checks."
  type        = string
}

# --- Compute ---
variable "desired_count" {
  description = "Number of K8s pod replicas."
  type        = number
}

variable "cpu" {
  description = "CPU allocation in millicores (e.g. 500)."
  type        = number
}

variable "memory" {
  description = "Memory allocation in MB."
  type        = number
}

# --- Networking ---
variable "subnet_ids" {
  description = "Subnet IDs for the AKS cluster."
  type        = list(string)
}

# --- Optional features ---
variable "create_load_balancer" {
  description = "Create a Kubernetes Ingress for external access."
  type        = bool
}

variable "enable_autoscaling" {
  description = "Enable horizontal pod autoscaling."
  type        = bool
}

variable "min_capacity" {
  description = "Minimum number of replicas when autoscaling is enabled."
  type        = number
}

variable "max_capacity" {
  description = "Maximum number of replicas when autoscaling is enabled."
  type        = number
}

variable "autoscaling_cpu_threshold" {
  description = "Target CPU utilization percentage for autoscaling."
  type        = number
}

variable "autoscaling_memory_threshold" {
  description = "Target memory utilization percentage for autoscaling."
  type        = number
}

variable "domain_name" {
  description = "Custom domain name for the Ingress (optional)."
  type        = string
  default     = null
}

# --- K8s-specific ---
variable "create_cluster" {
  description = "Create a new AKS cluster. Set to false to use an existing cluster."
  type        = bool
}

variable "kubernetes_namespace" {
  description = "Kubernetes namespace to deploy into."
  type        = string
}

variable "node_count" {
  description = "Number of nodes in the AKS default node pool."
  type        = number
}

variable "node_machine_type" {
  description = "VM size for AKS nodes (e.g. Standard_D2s_v3)."
  type        = string
  default     = null
}

variable "volume_size_gb" {
  description = "Persistent volume size in GB for SQLite storage."
  type        = number
}

# --- Identity ---
variable "identity_id" {
  description = "User assigned identity ID for the AKS cluster."
  type        = string
}

variable "api_server_authorized_ip_ranges" {
  description = "IP ranges authorized to access the AKS API server."
  type        = list(string)
  default     = ["0.0.0.0/0"]
}
