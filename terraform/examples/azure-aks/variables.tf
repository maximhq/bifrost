variable "region" {
  description = "Azure region to deploy into."
  type        = string
  default     = "eastus"
}

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

variable "resource_group_name" {
  description = "Azure resource group name. If null, a new one will be created."
  type        = string
  default     = null
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
  description = "LLM provider configurations (openai, anthropic, azure, etc.)."
  type        = any
  default     = null
}

# --- Compute ---

variable "desired_count" {
  description = "Number of Kubernetes pods."
  type        = number
  default     = 1
}

variable "cpu" {
  description = "CPU in millicores for each pod."
  type        = number
  default     = 500
}

variable "memory" {
  description = "Memory in MB for each pod."
  type        = number
  default     = 1024
}

# --- Cluster ---

variable "create_cluster" {
  description = "Create a new AKS cluster. Set to false to use an existing cluster."
  type        = bool
  default     = true
}

variable "node_count" {
  description = "Number of nodes in the AKS node pool."
  type        = number
  default     = 3
}

variable "create_load_balancer" {
  description = "Create a load balancer via Kubernetes Ingress."
  type        = bool
  default     = true
}

# --- Autoscaling ---

variable "enable_autoscaling" {
  description = "Enable Horizontal Pod Autoscaler."
  type        = bool
  default     = false
}

variable "min_capacity" {
  description = "Minimum number of pods when autoscaling is enabled."
  type        = number
  default     = 1
}

variable "max_capacity" {
  description = "Maximum number of pods when autoscaling is enabled."
  type        = number
  default     = 10
}
