# --- Deployment target ---
variable "service" {
  description = "Azure service to deploy on (aks or aci)."
  type        = string
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

variable "ingress_path" {
  description = "Path prefix the Kubernetes ingress routes to Bifrost; \"/\" owns the whole host."
  type        = string
  default     = "/"
}

variable "health_check_path" {
  description = "HTTP path for health checks."
  type        = string
}

# --- Infrastructure ---
variable "region" {
  description = "Azure region (e.g. eastus, westeurope)."
  type        = string
}

variable "name_prefix" {
  description = "Prefix for all resource names."
  type        = string
}

variable "tags" {
  description = "Tags to apply to all resources."
  type        = map(string)
}

# --- Compute ---
variable "desired_count" {
  description = "Number of replicas (K8s pods / ACI container groups)."
  type        = number
}

variable "cpu" {
  description = "CPU allocation (AKS: millicores, ACI: cores)."
  type        = number
}

variable "memory" {
  description = "Memory allocation in MB."
  type        = number
}

# --- Networking ---
variable "allowed_cidr" {
  description = "CIDR block or address prefix allowed for ingress traffic."
  type        = string
  default     = "*"
}

variable "existing_vpc_id" {
  description = "Existing VNet ID. If null, a new VNet will be created."
  type        = string
  default     = null
}

variable "existing_subnet_ids" {
  description = "Existing subnet IDs. If null, new subnets will be created. Must be provided together with existing_vpc_id."
  type        = list(string)
  default     = null

  validation {
    # Terraform evaluates both operands of ||, so length(null) must never run.
    condition     = var.existing_subnet_ids == null ? true : length(var.existing_subnet_ids) > 0
    error_message = "existing_subnet_ids must be null or a non-empty list."
  }
}

# --- Optional features ---
variable "create_load_balancer" {
  description = "Create a load balancer (Ingress for AKS)."
  type        = bool
}

variable "enable_autoscaling" {
  description = "Enable autoscaling for the service."
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

variable "domain_name" {
  description = "Custom domain name for the service (optional)."
  type        = string
  default     = null
}

# --- K8s-specific (AKS) ---
variable "create_cluster" {
  description = "Create a new AKS cluster. Set to false to use an existing cluster."
  type        = bool
}

variable "kubernetes_namespace" {
  description = "Kubernetes namespace to deploy into."
  type        = string
}

variable "node_count" {
  description = "Number of nodes in the AKS node pool."
  type        = number
}

variable "node_machine_type" {
  description = "VM size for AKS nodes (e.g. Standard_D2s_v3)."
  type        = string
  default     = null
}

variable "volume_size_gb" {
  description = "Persistent volume size in GB for SQLite storage."
  type        = number
}

# --- Azure-specific ---
variable "resource_group_name" {
  description = "Existing Azure resource group name. If null, a new one will be created."
  type        = string
  default     = null
}

# --- Managed database and generated secrets (ACI only) ---
variable "create_database" {
  description = "Provision an Azure Database for PostgreSQL Flexible Server (PostgreSQL 16) and wire PG_* into the container group."
  type        = bool
  default     = false
}

variable "database_name" {
  description = "Database name created on the Flexible Server."
  type        = string
  default     = "bifrost"
}

variable "database_user" {
  description = "Administrator login created on the Flexible Server."
  type        = string
  default     = "bifrost"
}

variable "database_instance_class" {
  description = "Flexible Server SKU. Defaults to B_Standard_B1ms."
  type        = string
  default     = null
}

variable "database_storage_gb" {
  description = "Flexible Server storage in GB. Azure only offers fixed tiers (32, 64, 128, 256, 512, 1024, 2048, 4095, 4096, 8192, 16384, 32767); the value is rounded up to the next tier, so smaller values are raised to 32."
  type        = number
  default     = 32
  validation {
    condition     = var.database_storage_gb > 0 && var.database_storage_gb <= 32767
    error_message = "database_storage_gb must be between 1 and 32767 (the largest Flexible Server tier)."
  }
}

variable "database_deletion_protection" {
  description = "Keep the Flexible Server on terraform destroy by refusing deletion."
  type        = bool
  default     = false
}

variable "generate_secrets" {
  description = "Generate BIFROST_ENCRYPTION_KEY and BIFROST_SETUP_TOKEN and inject them into the container group as secure environment variables."
  type        = bool
  default     = false
}

variable "key_vault_network_default_action" {
  description = "Key Vault network ACL default. Deny limits data-plane access to the service subnet and trusted Azure services; Allow lets the Terraform runner (for example Azure Cloud Shell) write the config secret from any network, authenticated by access policy."
  type        = string
  default     = "Deny"
  validation {
    condition     = contains(["Allow", "Deny"], var.key_vault_network_default_action)
    error_message = "key_vault_network_default_action must be Allow or Deny"
  }
}

variable "image_registry_credentials" {
  description = "Registry credentials for a private image (Bifrost Enterprise). Each entry becomes an image_registry_credential block on the ACI container group. AKS pulls with the kubelet identity of an attached registry instead; see the AKS guide."
  type = list(object({
    server   = string
    username = string
    password = string
  }))
  default   = []
  sensitive = true

  validation {
    condition     = alltrue([for c in var.image_registry_credentials : c.server != null && c.server != "" && c.username != null && c.username != "" && c.password != null && c.password != ""])
    error_message = "Each image_registry_credentials entry needs a non-empty server, username and password; a partial credential would only fail when the container group pulls the image."
  }
}

variable "service_type" {
  description = "Kubernetes Service type for AKS (ClusterIP, LoadBalancer, NodePort)."
  type        = string
  default     = "ClusterIP"
}
