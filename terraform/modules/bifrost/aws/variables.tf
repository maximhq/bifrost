# --- Deployment target ---
variable "service" {
  description = "AWS service to deploy on (ecs or eks)."
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
  description = "AWS region."
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
  description = "Number of replicas (ECS tasks / K8s pods)."
  type        = number
}

variable "cpu" {
  description = "CPU allocation (ECS: CPU units 256-4096, K8s: millicores)."
  type        = number
}

variable "memory" {
  description = "Memory allocation in MB."
  type        = number
}

# --- Networking ---
variable "existing_vpc_id" {
  description = "Existing VPC ID. If null, a new VPC will be created."
  type        = string
  default     = null
}

variable "existing_subnet_ids" {
  description = "Existing subnet IDs. If null, new subnets will be created."
  type        = list(string)
  default     = null
}

variable "allowed_cidr" {
  description = "CIDR block allowed for ingress traffic."
  type        = string
  default     = "0.0.0.0/0"
}

variable "existing_security_group_ids" {
  description = "Existing security group IDs. If null, a new one will be created."
  type        = list(string)
  default     = null
}

# --- Optional features ---
variable "create_load_balancer" {
  description = "Create an Application Load Balancer."
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

variable "secret_recovery_window_days" {
  description = "Number of days to retain deleted secrets before permanent deletion. Set to 0 for immediate deletion (useful for dev/testing)."
  type        = number
  default     = 0
}

variable "domain_name" {
  description = "Custom domain name for the service (optional)."
  type        = string
  default     = null
}

# --- K8s-specific (EKS) ---
variable "create_cluster" {
  description = "Create a new EKS cluster. Set to false to use an existing cluster."
  type        = bool
}

variable "kubernetes_namespace" {
  description = "Kubernetes namespace to deploy into."
  type        = string
}

variable "node_count" {
  description = "Number of nodes in the EKS node group."
  type        = number
}

variable "node_machine_type" {
  description = "EC2 instance type for EKS nodes (e.g. t3.medium)."
  type        = string
  default     = null
}

variable "volume_size_gb" {
  description = "Persistent volume size in GB for SQLite storage."
  type        = number
}

# --- PostgreSQL ---
variable "create_postgresql" {
  description = "Create a managed PostgreSQL RDS instance."
  type        = bool
  default     = false
}

variable "postgresql_engine_version" {
  description = "PostgreSQL engine version."
  type        = string
  default     = "16"
}

variable "postgresql_instance_class" {
  description = "RDS instance class."
  type        = string
  default     = null
}

variable "postgresql_storage_gb" {
  description = "Allocated storage in GB."
  type        = number
  default     = 20
}

variable "postgresql_database_name" {
  description = "Name of the initial database."
  type        = string
  default     = "bifrost"
}

variable "postgresql_username" {
  description = "Master username."
  type        = string
  default     = "bifrost"
}

variable "postgresql_password" {
  description = "Master password. If null, a random password is generated."
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
  description = "Enable multi-AZ deployment."
  type        = bool
  default     = false
}

variable "postgresql_publicly_accessible" {
  description = "Whether the database is publicly accessible."
  type        = bool
  default     = false
}
