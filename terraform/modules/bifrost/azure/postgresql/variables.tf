# --- PostgreSQL Configuration ---

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

variable "engine_version" {
  description = "PostgreSQL engine version."
  type        = string
}

variable "sku_name" {
  description = "Azure Flexible Server SKU name (e.g. B_Standard_B1ms, GP_Standard_D2s_v3)."
  type        = string
  default     = null
}

variable "storage_mb" {
  description = "Storage size in MB."
  type        = number
}

variable "database_name" {
  description = "Name of the initial database."
  type        = string
}

variable "username" {
  description = "Administrator username."
  type        = string
}

variable "password" {
  description = "Administrator password. If null, a random password is generated."
  type        = string
  default     = null
  sensitive   = true
}

variable "backup_retention_days" {
  description = "Backup retention period in days."
  type        = number
}

variable "high_availability" {
  description = "Enable zone-redundant high availability."
  type        = bool
}

# --- Networking ---

variable "vnet_id" {
  description = "Virtual network ID."
  type        = string
}

variable "subnet_id" {
  description = "Delegated subnet ID for the Flexible Server. If null, a new subnet is created."
  type        = string
  default     = null
}
