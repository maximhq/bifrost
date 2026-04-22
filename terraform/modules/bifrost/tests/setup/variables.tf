# All variables pass through to the bifrost module.
# Defaults match the parent module's defaults.

variable "cloud_provider" { type = string }
variable "service" { type = string }
variable "region" { type = string }

variable "config_json" {
  type      = string
  default   = null
  sensitive = true
}

variable "config_json_file" { default = null }

variable "encryption_key" {
  type      = string
  default   = null
  sensitive = true
}

variable "auth_config" {
  type      = any
  default   = null
  sensitive = true
}

variable "providers_config" {
  type      = any
  default   = null
  sensitive = true
}

variable "client" { default = null }
variable "framework" { default = null }
variable "governance" { default = null }
variable "mcp" { default = null }
variable "vector_store" { default = null }
variable "config_store" { default = null }
variable "logs_store" { default = null }
variable "cluster_config" { default = null }
variable "scim_config" { default = null }
variable "load_balancer_config" { default = null }
variable "guardrails_config" { default = null }
variable "plugins" { default = null }
variable "audit_logs" { default = null }
variable "websocket" { default = null }

variable "image_tag" { default = "latest" }
variable "image_repository" { default = "maximhq/bifrost" }
variable "name_prefix" { default = "bifrost" }

variable "tags" {
  type    = map(string)
  default = {}
}

variable "desired_count" { default = 1 }
variable "cpu" { default = 512 }
variable "memory" { default = 1024 }
variable "existing_vpc_id" { default = null }

variable "existing_subnet_ids" {
  type    = list(string)
  default = null
}

variable "allowed_cidr" { default = "0.0.0.0/0" }

variable "existing_security_group_ids" {
  type    = list(string)
  default = null
}

variable "create_load_balancer" { default = false }
variable "assign_public_ip" { default = true }
variable "enable_autoscaling" { default = false }
variable "min_capacity" { default = 1 }
variable "max_capacity" { default = 10 }
variable "autoscaling_cpu_threshold" { default = 80 }
variable "autoscaling_memory_threshold" { default = 80 }
variable "domain_name" { default = null }
variable "certificate_arn" { default = null }
variable "create_cluster" { default = true }
variable "kubernetes_namespace" { default = "bifrost" }
variable "node_count" { default = 3 }
variable "node_machine_type" { default = null }
variable "volume_size_gb" { default = 10 }
variable "gcp_project_id" { default = null }
variable "azure_resource_group_name" { default = null }
variable "storage_class_name" { default = "standard" }
variable "ingress_class_name" { default = "nginx" }

variable "ingress_annotations" {
  type    = map(string)
  default = {}
}
