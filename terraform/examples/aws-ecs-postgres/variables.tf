variable "region" {
  description = "AWS region to deploy into."
  type        = string
  default     = "us-east-1"
}

variable "image_repository" {
  description = "Bifrost image repository. Enterprise: the registry path issued for your organization."
  type        = string
  default     = "maximhq/bifrost"
}

variable "image_tag" {
  description = "Bifrost image tag. Pin a released version for production."
  type        = string
  default     = "latest"
}

variable "name_prefix" {
  description = "Prefix for all resource names."
  type        = string
  default     = "bifrost"
}

# --- Database ---

variable "database_instance_class" {
  description = "RDS instance class."
  type        = string
  default     = "db.t4g.small"
}

variable "database_storage_gb" {
  description = "RDS allocated storage in GB."
  type        = number
  default     = 20
}

variable "database_deletion_protection" {
  description = "Protect the database from terraform destroy. Set true once the deployment carries real traffic; switch it back off and apply before destroying."
  type        = bool
  default     = false
}

variable "database_skip_final_snapshot" {
  description = "Skip the final RDS snapshot on destroy. Set false to keep one named <name_prefix>-db-final; independent of deletion protection."
  type        = bool
  default     = true
}

# --- Access ---

variable "enforce_auth_on_inference" {
  description = "Require a virtual key on inference routes. Keep true while the ALB is reachable from the internet."
  type        = bool
  default     = true
}

variable "allowed_cidr" {
  description = "CIDR allowed to reach the ALB. Narrow this to your network for production."
  type        = string
  default     = "0.0.0.0/0"
}

variable "providers_config" {
  description = "Optional LLM provider configuration written to config.json. Leave null and add providers in the dashboard."
  type        = any
  default     = null
  sensitive   = true
}

# --- Compute ---

variable "desired_count" {
  description = "Number of ECS tasks. Keep 1 for OSS: replicas do not share live configuration."
  type        = number
  default     = 1
}

variable "cpu" {
  description = "CPU units for the ECS task (256-4096)."
  type        = number
  default     = 1024
}

variable "memory" {
  description = "Memory in MB for the ECS task."
  type        = number
  default     = 2048
}

variable "tags" {
  description = "Tags applied to every resource."
  type        = map(string)
  default     = { app = "bifrost" }
}
