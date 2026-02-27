# --- Deployment target ---
variable "service" {
  description = "GCP service to deploy on (gke or cloud-run)."
  type        = string
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

# --- GCP project ---
variable "project_id" {
  description = "GCP project ID."
  type        = string
}

# --- Infrastructure ---
variable "region" {
  description = "GCP region."
  type        = string
}

variable "name_prefix" {
  description = "Prefix for all resource names."
  type        = string
}

variable "tags" {
  description = "Labels to apply to all resources."
  type        = map(string)
}

# --- Compute ---
variable "desired_count" {
  description = "Number of replicas (K8s pods / Cloud Run instances)."
  type        = number
}

variable "cpu" {
  description = "CPU allocation (K8s: millicores, Cloud Run: vCPUs as millicores)."
  type        = number
}

variable "memory" {
  description = "Memory allocation in MB."
  type        = number
}

# --- Networking ---
variable "allowed_cidr" {
  description = "CIDR block allowed for ingress traffic."
  type        = string
  default     = "0.0.0.0/0"
}

variable "existing_vpc_id" {
  description = "Existing VPC network self_link. If null, a new VPC will be created."
  type        = string
  default     = null
}

variable "existing_subnet_ids" {
  description = "Existing subnet self_links. If null, new subnets will be created. Must be provided together with existing_vpc_id."
  type        = list(string)
  default     = null

  validation {
    condition     = var.existing_subnet_ids == null || length(var.existing_subnet_ids) > 0
    error_message = "existing_subnet_ids must be null or a non-empty list."
  }
}

# --- Optional features ---
variable "create_load_balancer" {
  description = "Create a load balancer (GKE Ingress / Cloud Run domain mapping)."
  type        = bool
}

variable "enable_autoscaling" {
  description = "Enable autoscaling for the service."
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
  description = "Custom domain name for the service (optional)."
  type        = string
  default     = null
}

# --- K8s-specific (GKE) ---
variable "create_cluster" {
  description = "Create a new GKE cluster. Set to false to use an existing cluster."
  type        = bool
}

variable "kubernetes_namespace" {
  description = "Kubernetes namespace to deploy into."
  type        = string
}

variable "node_count" {
  description = "Number of nodes in the GKE node pool."
  type        = number
}

variable "node_machine_type" {
  description = "Machine type for GKE nodes (e.g. e2-standard-4)."
  type        = string
  default     = null
}

variable "volume_size_gb" {
  description = "Persistent volume size in GB for SQLite storage."
  type        = number
}
