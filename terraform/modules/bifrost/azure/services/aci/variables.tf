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
  description = "Number of container group instances."
  type        = number
}

variable "cpu" {
  description = "CPU allocation (will be converted to ACI decimal cores)."
  type        = number
}

variable "memory" {
  description = "Memory allocation in MB (will be converted to GB for ACI)."
  type        = number
}

# --- Networking ---
variable "subnet_ids" {
  description = "Subnet IDs (optional; used for private networking if needed)."
  type        = list(string)
  default     = null
}

# --- Identity ---
variable "identity_id" {
  description = "User assigned identity ID for the container group."
  type        = string
}
