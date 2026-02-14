# --- Bifrost ---
variable "service_name" {
  description = "Name for the Cloud Run service."
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
  description = "GCP service account email for the Cloud Run service."
  type        = string
}

variable "secret_id" {
  description = "Secret Manager secret ID containing config.json."
  type        = string
}

variable "secret_version" {
  description = "Secret Manager secret version for config.json."
  type        = string
}

# --- Networking ---
variable "vpc_id" {
  description = "VPC network self_link (used for VPC connector if needed)."
  type        = string
}

# --- Compute ---
variable "desired_count" {
  description = "Minimum number of Cloud Run instances."
  type        = number
}

variable "cpu" {
  description = "CPU allocation in millicores (e.g. 1000 = 1 vCPU)."
  type        = number
}

variable "memory" {
  description = "Memory allocation in MB."
  type        = number
}

# --- Scaling ---
variable "max_capacity" {
  description = "Maximum number of Cloud Run instances."
  type        = number
}

# --- Optional features ---
variable "create_load_balancer" {
  description = "Create a load balancer for the Cloud Run service."
  type        = bool
}

variable "domain_name" {
  description = "Custom domain name for the service (optional)."
  type        = string
  default     = null
}
