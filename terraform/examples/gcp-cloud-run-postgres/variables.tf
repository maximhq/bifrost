variable "project_id" {
  description = "GCP project ID."
  type        = string
}

variable "region" {
  description = "GCP region to deploy into."
  type        = string
  default     = "us-central1"
}

variable "image_repository" {
  description = "Bifrost image repository. Enterprise: the Artifact Registry path issued for your organization."
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
  description = "Cloud SQL tier."
  type        = string
  default     = "db-custom-1-3840"
}

variable "database_storage_gb" {
  description = "Cloud SQL disk size in GB."
  type        = number
  default     = 20
}

variable "database_deletion_protection" {
  description = "Protect the database from terraform destroy. Set true once the deployment carries real traffic."
  type        = bool
  default     = false
}

# --- Access ---

variable "enforce_auth_on_inference" {
  description = "Require a virtual key on inference routes. Keep true while the service is public."
  type        = bool
  default     = true
}

variable "providers_config" {
  description = "Optional LLM provider configuration written to config.json. Leave null and add providers in the dashboard."
  type        = any
  default     = null
  sensitive   = true
}

# --- Compute ---

variable "min_instances" {
  description = "Minimum Cloud Run instances. 1 keeps the gateway warm; 0 scales to zero between requests."
  type        = number
  default     = 1
}

variable "max_instances" {
  description = "Maximum Cloud Run instances. Keep 1 for OSS: instances do not share live configuration."
  type        = number
  default     = 1
}

variable "cpu" {
  description = "CPU in millicores (1000 = 1 vCPU)."
  type        = number
  default     = 1000
}

variable "memory" {
  description = "Memory in MB."
  type        = number
  default     = 2048
}

variable "tags" {
  description = "Labels applied to every resource."
  type        = map(string)
  default     = { app = "bifrost" }
}
