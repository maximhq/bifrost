# --- Kubernetes connection ---

variable "kubeconfig_path" {
  description = "Path to the kubeconfig file."
  type        = string
  default     = "~/.kube/config"
}

variable "kubeconfig_context" {
  description = "Kubeconfig context to use. Defaults to current context."
  type        = string
  default     = null
}

# --- Bifrost ---

variable "image_tag" {
  description = "Bifrost Docker image tag."
  type        = string
  default     = "latest"
}

variable "name_prefix" {
  description = "Prefix for all resource names."
  type        = string
  default     = "bifrost"
}

# --- Config: file-based ---

variable "config_json_file" {
  description = "Path to a Bifrost config.json file. Variables below override matching keys."
  type        = string
  default     = null
}

# --- Config: variable-based overrides ---

variable "config_store" {
  description = "Config store configuration (type: sqlite/postgres)."
  type        = any
  default     = null
}

variable "logs_store" {
  description = "Logs store configuration (type: sqlite/postgres)."
  type        = any
  default     = null
}

variable "providers_config" {
  description = "LLM provider configurations (openai, anthropic, etc.)."
  type        = any
  default     = null
}

# --- Compute ---

variable "desired_count" {
  description = "Number of pod replicas."
  type        = number
  default     = 1
}

variable "cpu" {
  description = "CPU allocation in millicores (e.g. 500)."
  type        = number
  default     = 512
}

variable "memory" {
  description = "Memory allocation in MB."
  type        = number
  default     = 1024
}

variable "kubernetes_namespace" {
  description = "Kubernetes namespace to deploy into."
  type        = string
  default     = "bifrost"
}

variable "volume_size_gb" {
  description = "Persistent volume claim size in GB."
  type        = number
  default     = 10
}

variable "storage_class_name" {
  description = "Kubernetes StorageClass name for dynamic PVC provisioning."
  type        = string
  default     = "standard"
}

# --- Ingress ---

variable "create_load_balancer" {
  description = "Create a Kubernetes Ingress resource."
  type        = bool
  default     = false
}

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

variable "domain_name" {
  description = "Custom domain name for the Ingress host rule."
  type        = string
  default     = null
}

# --- Autoscaling ---

variable "enable_autoscaling" {
  description = "Enable Horizontal Pod Autoscaler."
  type        = bool
  default     = false
}

variable "min_capacity" {
  description = "Minimum number of replicas when autoscaling is enabled."
  type        = number
  default     = 1
}

variable "max_capacity" {
  description = "Maximum number of replicas when autoscaling is enabled."
  type        = number
  default     = 10
}
