# --- Deployment target ---
variable "cloud_provider" {
  description = "Cloud provider to deploy on"
  type        = string
  validation {
    condition     = contains(["aws", "gcp", "azure", "kubernetes"], var.cloud_provider)
    error_message = "cloud_provider must be one of: aws, gcp, azure, kubernetes"
  }
}

variable "service" {
  description = "Cloud service to deploy on. AWS: ecs, eks. GCP: gke, cloud-run. Azure: aks, aci. Kubernetes: deployment."
  type        = string
  validation {
    condition     = contains(["ecs", "eks", "gke", "cloud-run", "aks", "aci", "deployment"], var.service)
    error_message = "service must be one of: ecs, eks, gke, cloud-run, aks, aci, deployment"
  }
}

# --- Config: bring your own ---
variable "config_json" {
  description = "Complete Bifrost config.json as a string. Can be combined with individual config variables (variables override matching keys)."
  type        = string
  default     = null
  sensitive   = true
}

variable "config_json_file" {
  description = "Path to a Bifrost config.json file. Can be combined with individual config variables (variables override matching keys)."
  type        = string
  default     = null
}

# --- Config: individual sections (each mirrors a top-level property from config.schema.json) ---
variable "encryption_key" {
  description = "Encryption key for sensitive data. Accepts any string; a secure 32-byte AES-256 key will be derived using Argon2id KDF."
  type        = string
  default     = null
  sensitive   = true
}

variable "auth_config" {
  description = "Authentication configuration (admin_username, admin_password, is_enabled, disable_auth_on_inference)."
  type        = any
  default     = null
  sensitive   = true
}

variable "client" {
  description = "Client configuration (initial_pool_size, allowed_origins, enable_logging, enable_governance, max_request_body_size_mb, etc.)."
  type        = any
  default     = null
}

variable "framework" {
  description = "Framework configuration (pricing)."
  type        = any
  default     = null
}

variable "providers_config" {
  description = "LLM provider configurations (openai, anthropic, bedrock, azure, vertex, mistral, ollama, groq, gemini, openrouter, sgl, parasail, perplexity, elevenlabs, cerebras, huggingface)."
  type        = any
  default     = null
  sensitive   = true
}

variable "governance" {
  description = "Governance configuration (budgets, rate_limits, customers, teams, virtual_keys, routing_rules)."
  type        = any
  default     = null
}

variable "mcp" {
  description = "MCP configuration (client_configs, tool_manager_config)."
  type        = any
  default     = null
}

variable "vector_store" {
  description = "Vector store configuration (enabled, type: weaviate/redis/qdrant/pinecone, config)."
  type        = any
  default     = null
}

variable "config_store" {
  description = "Config store configuration (enabled, type: sqlite/postgres, config)."
  type        = any
  default     = null
}

variable "logs_store" {
  description = "Logs store configuration (enabled, type: sqlite/postgres, config)."
  type        = any
  default     = null
}

variable "cluster_config" {
  description = "Cluster mode configuration (enabled, peers, gossip, discovery)."
  type        = any
  default     = null
}

variable "saml_config" {
  description = "SAML/SSO configuration (enabled, provider: okta/entra, config)."
  type        = any
  default     = null
}

variable "load_balancer_config" {
  description = "Intelligent load balancer configuration (enabled, tracker_config, bootstrap)."
  type        = any
  default     = null
}

variable "guardrails_config" {
  description = "Guardrails configuration (guardrail_rules, guardrail_providers)."
  type        = any
  default     = null
}

variable "plugins" {
  description = "Plugins configuration. Array of plugin objects (telemetry, logging, governance, maxim, semantic_cache, otel, datadog)."
  type        = any
  default     = null
}

variable "audit_logs" {
  description = "Audit logs configuration (disabled, hmac_key)."
  type        = any
  default     = null
}

# --- Image ---
variable "image_tag" {
  description = "Bifrost Docker image tag."
  type        = string
  default     = "latest"
}

variable "image_repository" {
  description = "Bifrost Docker image repository."
  type        = string
  default     = "maximhq/bifrost"
}

# --- Infrastructure ---
variable "region" {
  description = "Cloud provider region (e.g. us-east-1, us-central1, eastus)."
  type        = string
}

variable "name_prefix" {
  description = "Prefix for all resource names."
  type        = string
  default     = "bifrost"
}

variable "tags" {
  description = "Tags to apply to all resources."
  type        = map(string)
  default     = {}
}

# --- Compute ---

# -----------------------------------------------------------------------------------------
# NOTE: If you are using OSS version - running multiple nodes has an effect on functionality
# of the system. Please read https://docs.getbifrost.ai/deployment-guides/how-to/multinode
#-----------------------------------------------------------------------------------------

variable "desired_count" {
  description = "Number of replicas (ECS tasks / K8s pods). Capped at 1 for SQLite storage on K8s services."
  type        = number
  default     = 1
}

variable "cpu" {
  description = "CPU allocation. ECS: CPU units (256-4096). K8s: millicores (e.g. 500)."
  type        = number
  default     = 512
}

variable "memory" {
  description = "Memory allocation in MB."
  type        = number
  default     = 1024
}

# --- Networking (optional — creates new if not provided) ---
variable "existing_vpc_id" {
  description = "Existing VPC/VNet/Network ID. If not provided, a new one will be created."
  type        = string
  default     = null
}

variable "existing_subnet_ids" {
  description = "Existing subnet IDs. If not provided, new subnets will be created."
  type        = list(string)
  default     = null
}

variable "allowed_cidr" {
  description = "CIDR block allowed for ingress traffic. Set to a specific range for production (e.g. your VPN or office IP)."
  type        = string
  default     = "0.0.0.0/0"
}

variable "existing_security_group_ids" {
  description = "Existing security group IDs. If not provided, a new one will be created."
  type        = list(string)
  default     = null
}

# --- Optional features ---
variable "create_load_balancer" {
  description = "Create a load balancer (ALB for ECS/EKS, Cloud Load Balancer for GKE, etc.)."
  type        = bool
  default     = false
}

variable "enable_autoscaling" {
  description = "Enable autoscaling. Disabled automatically for SQLite storage on K8s services."
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

variable "autoscaling_cpu_threshold" {
  description = "Target CPU utilization percentage for autoscaling."
  type        = number
  default     = 80
}

variable "autoscaling_memory_threshold" {
  description = "Target memory utilization percentage for autoscaling."
  type        = number
  default     = 80
}

variable "domain_name" {
  description = "Custom domain name for the service (optional)."
  type        = string
  default     = null
}

# --- K8s-specific (EKS, GKE, AKS) ---
variable "create_cluster" {
  description = "Create a new K8s cluster. Set to false to use an existing cluster."
  type        = bool
  default     = true
}

variable "kubernetes_namespace" {
  description = "Kubernetes namespace to deploy into."
  type        = string
  default     = "bifrost"
}

variable "node_count" {
  description = "Number of nodes in the K8s node pool (when creating a new cluster)."
  type        = number
  default     = 3
}

variable "node_machine_type" {
  description = "Machine type for K8s nodes (e.g. t3.medium, e2-standard-4, Standard_D2s_v3)."
  type        = string
  default     = null
}

variable "volume_size_gb" {
  description = "Persistent volume size in GB for SQLite storage."
  type        = number
  default     = 10
}

# --- GCP-specific ---
variable "gcp_project_id" {
  description = "GCP project ID (required when cloud_provider = gcp)."
  type        = string
  default     = null
}

# --- Azure-specific ---
variable "azure_resource_group_name" {
  description = "Azure resource group name. If not provided, a new one will be created."
  type        = string
  default     = null
}

# --- Generic Kubernetes ---
variable "storage_class_name" {
  description = "Kubernetes StorageClass name for dynamic PVC provisioning (e.g. standard, gp2, premium-rwo). Used when cloud_provider = kubernetes."
  type        = string
  default     = "standard"
}

variable "ingress_class_name" {
  description = "Ingress class name (e.g. nginx, traefik, haproxy). Used when cloud_provider = kubernetes."
  type        = string
  default     = "nginx"
}

variable "ingress_annotations" {
  description = "Annotations to add to the Kubernetes Ingress resource. Used when cloud_provider = kubernetes."
  type        = map(string)
  default     = {}
}

# --- PostgreSQL ---
variable "create_postgresql" {
  description = "Create a managed PostgreSQL instance for config_store and logs_store."
  type        = bool
  default     = false
}

variable "postgresql_engine_version" {
  description = "PostgreSQL engine version (e.g. 16, 15, 14)."
  type        = string
  default     = "16"
}

variable "postgresql_instance_class" {
  description = "Instance class. AWS: db.t3.micro, GCP: db-custom-2-7680, Azure: B_Standard_B1ms. If null, uses cloud-specific defaults."
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
  description = "Master username for the PostgreSQL instance."
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
  description = "Enable multi-AZ deployment for high availability."
  type        = bool
  default     = false
}

variable "postgresql_publicly_accessible" {
  description = "Whether the database is publicly accessible. Should be false for production."
  type        = bool
  default     = false
}
