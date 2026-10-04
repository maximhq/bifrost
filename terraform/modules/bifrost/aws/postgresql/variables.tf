# --- PostgreSQL Configuration ---

variable "name_prefix" {
  description = "Prefix for all resource names."
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

variable "instance_class" {
  description = "RDS instance class (e.g. db.t3.micro, db.t3.small, db.r6g.large)."
  type        = string
  default     = null
}

variable "allocated_storage" {
  description = "Allocated storage in GB."
  type        = number
}

variable "database_name" {
  description = "Name of the initial database."
  type        = string
}

variable "username" {
  description = "Master username."
  type        = string
}

variable "password" {
  description = "Master password. If null, a random password is generated."
  type        = string
  default     = null
  sensitive   = true
}

variable "backup_retention_period" {
  description = "Backup retention period in days."
  type        = number
}

variable "multi_az" {
  description = "Enable multi-AZ deployment."
  type        = bool
}

variable "publicly_accessible" {
  description = "Whether the instance is publicly accessible."
  type        = bool
}

# --- Networking (passed from parent AWS module) ---

variable "vpc_id" {
  description = "VPC ID for the database."
  type        = string
}

variable "subnet_ids" {
  description = "Subnet IDs for the DB subnet group."
  type        = list(string)
}

variable "source_security_group_ids" {
  description = "Security group IDs allowed to connect to PostgreSQL."
  type        = list(string)
}
