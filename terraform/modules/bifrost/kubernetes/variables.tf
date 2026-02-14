# --- Bifrost ---
variable "service_name" {
  description = "Name for the Bifrost Kubernetes service/deployment."
  type        = string
}

variable "config_json" {
  description = "Complete Bifrost config.json as a string."
  type        = string
  sensitive   = true
}

variable "image" {
  description = "Full Docker image reference (repository:tag)."
  type        = string
}

variable "container_port" {
  description = "Port the Bifrost container listens on."
  type        = number
}

variable "health_check_path" {
  description = "HTTP path for health checks."
  type        = string
}

# --- Naming ---
variable "name_prefix" {
  description = "Prefix for all resource names."
  type        = string
}

variable "tags" {
  description = "Labels to apply to all resources."
  type        = map(string)
}

# --- Compute ---
variable "desired_count" {
  description = "Number of pod replicas."
  type        = number
}

variable "cpu" {
  description = "CPU allocation in millicores (e.g. 500)."
  type        = number
}

variable "memory" {
  description = "Memory allocation in MB."
  type        = number
}

# --- Kubernetes ---
variable "kubernetes_namespace" {
  description = "Kubernetes namespace to deploy into."
  type        = string
}

variable "volume_size_gb" {
  description = "Persistent volume claim size in GB."
  type        = number
}

variable "storage_class_name" {
  description = "Kubernetes StorageClass name for dynamic PVC provisioning (e.g. standard, gp2, premium-rwo)."
  type        = string
  default     = "standard"
}

# --- Optional features ---
variable "create_load_balancer" {
  description = "Create a Kubernetes Ingress resource."
  type        = bool
}

variable "enable_autoscaling" {
  description = "Enable Horizontal Pod Autoscaler."
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
  description = "Custom domain name for the Ingress host rule (optional)."
  type        = string
  default     = null
}

# --- Ingress ---
variable "ingress_class_name" {
  description = "Ingress class name (e.g. nginx, traefik, haproxy)."
  type        = string
  default     = "nginx"
}

variable "ingress_annotations" {
  description = "Annotations to add to the Ingress resource."
  type        = map(string)
  default     = {}
}
