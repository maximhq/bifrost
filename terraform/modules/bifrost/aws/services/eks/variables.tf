# =============================================================================
# AWS EKS Service Module — Variables
# =============================================================================

# --- Infrastructure ---

variable "name_prefix" {
  description = "Prefix for all resource names."
  type        = string
}

variable "region" {
  description = "AWS region."
  type        = string
}

variable "tags" {
  description = "Tags to apply to all resources."
  type        = map(string)
}

# --- Container ---

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

# --- Bifrost config ---

variable "config_json" {
  description = "Complete Bifrost config.json as a string, stored as a Kubernetes secret."
  type        = string
  sensitive   = true
}

# --- Compute ---

variable "desired_count" {
  description = "Number of Kubernetes pod replicas."
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
  description = "Subnet IDs for the EKS cluster."
  type        = list(string)
}

variable "security_group_ids" {
  description = "Security group IDs for the EKS cluster."
  type        = list(string)
}

variable "vpc_id" {
  description = "VPC ID for the EKS cluster."
  type        = string
}

# --- Secrets ---

variable "secret_arn" {
  description = "ARN of the Secrets Manager secret containing config.json."
  type        = string
}

# --- EKS cluster ---

variable "create_cluster" {
  description = "Create a new EKS cluster. Set to false to use an existing cluster."
  type        = bool
}

variable "kubernetes_namespace" {
  description = "Kubernetes namespace to deploy into."
  type        = string
}

variable "node_count" {
  description = "Number of nodes in the EKS node group."
  type        = number
}

variable "node_machine_type" {
  description = "EC2 instance type for EKS nodes (e.g. t3.medium)."
  type        = string
  default     = null
}

variable "volume_size_gb" {
  description = "Persistent volume size in GB for SQLite storage."
  type        = number
}

# --- Load balancer ---

variable "create_load_balancer" {
  description = "Create an AWS Load Balancer Controller ingress."
  type        = bool
}

# --- Autoscaling ---

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

# --- Domain ---

variable "domain_name" {
  description = "Custom domain name for the service (optional). When set, a Kubernetes Ingress is created."
  type        = string
  default     = null
}

variable "certificate_arn" {
  description = "ACM certificate ARN for HTTPS on the ALB. Required when using HTTPS."
  type        = string
  default     = null
}
