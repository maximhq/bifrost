# --- PostgreSQL Configuration ---

variable "name_prefix" {
  description = "Prefix for all resource names."
  type        = string
}

variable "project_id" {
  description = "GCP project ID."
  type        = string
}

variable "region" {
  description = "GCP region."
  type        = string
}

variable "tags" {
  description = "Labels to apply to all resources."
  type        = map(string)
}

variable "engine_version" {
  description = "PostgreSQL engine version."
  type        = string
}

variable "tier" {
  description = "Cloud SQL machine tier (e.g. db-custom-2-7680, db-f1-micro)."
  type        = string
  default     = null
}

variable "disk_size_gb" {
  description = "Disk size in GB."
  type        = number
}

variable "database_name" {
  description = "Name of the initial database."
  type        = string
}

variable "username" {
  description = "Database username."
  type        = string
}

variable "password" {
  description = "Database password. If null, a random password is generated."
  type        = string
  default     = null
  sensitive   = true
}

variable "backup_retention_days" {
  description = "Number of days to retain backups."
  type        = number
}

variable "high_availability" {
  description = "Enable high availability (regional) configuration."
  type        = bool
}

# --- Networking ---

variable "vpc_id" {
  description = "VPC self_link for private networking."
  type        = string
}
