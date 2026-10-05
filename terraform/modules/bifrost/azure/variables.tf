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
  description = "Existing subnet IDs. If null, new subnets will be created."
  type        = list(string)
  default     = null
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

# --- PostgreSQL ---
variable "create_postgresql" {
  description = "Create a managed Azure Database for PostgreSQL Flexible Server."
  type        = bool
  default     = false
}

variable "postgresql_engine_version" {
  description = "PostgreSQL engine version."
  type        = string
  default     = "16"
}

variable "postgresql_instance_class" {
  description = "Azure Flexible Server SKU name."
  type        = string
  default     = null
}

variable "postgresql_storage_gb" {
  description = "Storage size in GB."
  type        = number
  default     = 20
}

variable "postgresql_database_name" {
  description = "Name of the initial database."
  type        = string
  default     = "bifrost"
}

variable "postgresql_username" {
  description = "Administrator username."
  type        = string
  default     = "bifrost"
}

variable "postgresql_password" {
  description = "Administrator password. If null, a random password is generated."
  type        = string
  default     = null
  sensitive   = true
}

variable "postgresql_backup_retention_days" {
  description = "Backup retention period in days."
  type        = number
  default     = 7
}

variable "postgresql_multi_az" {
  description = "Enable zone-redundant high availability."
  type        = bool
  default     = false
}

# --- Azure-specific ---
variable "resource_group_name" {
  description = "Existing Azure resource group name. If null, a new one will be created."
  type        = string
  default     = null
}
