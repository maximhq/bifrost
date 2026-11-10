variable "region" {
  description = "Azure region to deploy into."
  type        = string
  default     = "eastus"
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
  description = "Prefix for all resource names. Also the public DNS label of the container group, so it must be unique in the region."
  type        = string
  default     = "bifrost"
}

variable "resource_group_name" {
  description = "Existing resource group to deploy into. Null creates <name_prefix>-rg."
  type        = string
  default     = null
}

# --- Database ---

variable "database_instance_class" {
  description = "Flexible Server SKU."
  type        = string
  default     = "B_Standard_B1ms"
}

variable "database_storage_gb" {
  description = "Flexible Server storage in GB (minimum 32)."
  type        = number
  default     = 32
}

variable "database_deletion_protection" {
  description = "Put a CanNotDelete lock on the database. Set true once the deployment carries real traffic."
  type        = bool
  default     = false
}

# --- Access ---

variable "enforce_auth_on_inference" {
  description = "Require a virtual key on inference routes. Keep true while the container group is reachable from the internet."
  type        = bool
  default     = true
}

variable "allowed_cidr" {
  description = "Source prefix for the subnet network security group rule. The container group has a public IP and is not attached to the subnet, so this does not restrict the public endpoint, and a Front Door or Application Gateway in front adds TLS but does not stop clients from reaching that public IP directly. Use the AKS example when the origin itself must be private."
  type        = string
  default     = "*"
}

# --- Private registry (Bifrost Enterprise) ---

variable "registry_server" {
  description = "Registry host of the private Enterprise image, for example myorg.azurecr.io. Leave null for the public image."
  type        = string
  default     = null
}

variable "registry_username" {
  description = "Pull username for registry_server."
  type        = string
  default     = null
}

variable "registry_password" {
  description = "Pull password or token for registry_server."
  type        = string
  default     = null
  sensitive   = true
}

variable "providers_config" {
  description = "Optional LLM provider configuration written to config.json. Leave null and add providers in the dashboard."
  type        = any
  default     = null
  sensitive   = true
}

# --- Compute ---

variable "cpu" {
  description = "CPU in millicores (1000 = 1 core)."
  type        = number
  default     = 1000
}

variable "memory" {
  description = "Memory in MB."
  type        = number
  default     = 2048
}

variable "tags" {
  description = "Tags applied to every resource."
  type        = map(string)
  default     = { app = "bifrost" }
}
