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
  description = "Prefix for all resource names."
  type        = string
  default     = "bifrost"
}

variable "resource_group_name" {
  description = "Existing resource group to deploy into. Null creates <name_prefix>-rg."
  type        = string
  default     = null
}

variable "kubeconfig_path" {
  description = "Kubeconfig the kubernetes provider reads. deploy/aks/deploy.sh writes it with az aks get-credentials after the cluster exists."
  type        = string
  default     = "~/.kube/config"
}

variable "kubeconfig_context" {
  description = "Kubeconfig context to use. Null uses the current context."
  type        = string
  default     = null
}

# --- Cluster ---

variable "node_count" {
  description = "Nodes in the default node pool."
  type        = number
  default     = 2
}

variable "node_machine_type" {
  description = "VM size for the node pool."
  type        = string
  default     = "Standard_D2s_v3"
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
  description = "Put a CanNotDelete lock on the database."
  type        = bool
  default     = false
}

# --- Access ---

variable "enforce_auth_on_inference" {
  description = "Require a virtual key on inference routes. Keep true while the load balancer is public."
  type        = bool
  default     = true
}

variable "allowed_cidr" {
  description = "CIDR allowed to reach the Bifrost Service: applied as loadBalancerSourceRanges on the public load balancer and as the subnet NSG rule for the Service port. Narrow this for production."
  type        = string
  default     = "*"
}

variable "providers_config" {
  description = "Optional LLM provider configuration written to config.json."
  type        = any
  default     = null
  sensitive   = true
}

# --- Compute ---

variable "service_type" {
  description = "Kubernetes Service type for Bifrost. LoadBalancer exposes the public Azure load balancer the deploy script prints (plain HTTP); set ClusterIP once a TLS ingress or load balancer is in front so the public port-80 endpoint disappears."
  type        = string
  default     = "LoadBalancer"

  validation {
    condition     = contains(["ClusterIP", "LoadBalancer", "NodePort"], var.service_type)
    error_message = "service_type must be ClusterIP, LoadBalancer or NodePort."
  }
}

variable "cpu" {
  description = "CPU request in millicores."
  type        = number
  default     = 1000
}

variable "memory" {
  description = "Memory request in MB."
  type        = number
  default     = 2048
}

variable "tags" {
  description = "Tags applied to every resource."
  type        = map(string)
  default     = { app = "bifrost" }
}
