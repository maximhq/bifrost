variable "region" {
  description = "AWS region to deploy into."
  type        = string
  default     = "us-east-1"
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
  description = "LLM provider configurations (openai, anthropic, bedrock, etc.)."
  type        = any
  default     = null
}

# --- Compute ---

variable "desired_count" {
  description = "Number of ECS tasks."
  type        = number
  default     = 1
}

variable "cpu" {
  description = "CPU units for the ECS task (256-4096)."
  type        = number
  default     = 512
}

variable "memory" {
  description = "Memory in MB for the ECS task."
  type        = number
  default     = 1024
}

variable "create_load_balancer" {
  description = "Create an Application Load Balancer in front of the service."
  type        = bool
  default     = false
}

# --- Autoscaling ---

variable "enable_autoscaling" {
  description = "Enable ECS service autoscaling."
  type        = bool
  default     = false
}

variable "min_capacity" {
  description = "Minimum number of tasks when autoscaling is enabled."
  type        = number
  default     = 1
}

variable "max_capacity" {
  description = "Maximum number of tasks when autoscaling is enabled."
  type        = number
  default     = 10
}
