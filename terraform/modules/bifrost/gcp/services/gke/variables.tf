# --- Bifrost ---
variable "service_name" {
  description = "Name for the Bifrost Kubernetes service/deployment."
  type        = string
}

variable "config_json" {
  description = "Complete Bifrost config.json as a string."
  type        = string
  sensitive   = true
}

variable "image" {
  description = "Full Docker image reference (repository:tag)."
  type        = string
}

variable "container_port" {
  description = "Port the Bifrost container listens on."
  type        = number
}

variable "health_check_path" {
  description = "HTTP path for health checks."
  type        = string
}

# --- GCP ---
variable "project_id" {
  description = "GCP project ID."
  type        = string
}

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

variable "service_account" {
  description = "GCP service account email for the GKE nodes."
  type        = string
}

# --- Networking ---
variable "vpc_id" {
  description = "VPC network self_link."
  type        = string
}

variable "subnet_id" {
  description = "Subnet self_link."
  type        = string
}

# --- Compute ---
variable "desired_count" {
  description = "Number of pod replicas."
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

# --- Cluster ---
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

# --- Optional features ---
variable "create_load_balancer" {
  description = "Create a load balancer via Kubernetes Ingress."
  type        = bool
}

variable "enable_autoscaling" {
  description = "Enable Horizontal Pod Autoscaler."
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

variable "master_authorized_cidr" {
  description = "CIDR block authorized to access the GKE master endpoint."
  type        = string
  default     = "0.0.0.0/0"
}
