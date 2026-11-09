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

variable "schema_url" {
  description = "Schema location written to config.json $schema. Override for isolated deployments that mirror the Bifrost schema internally. Accepts an HTTP(S) URL, file:// URL, or filesystem path. When null, an existing $schema in config_json/config_json_file is preserved, otherwise the public Bifrost schema URL is injected."
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
  description = "Client configuration (initial_pool_size, allowed_origins, enable_logging, max_request_body_size_mb, etc.)."
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

variable "scim_config" {
  description = "SCIM/SSO configuration (enabled, provider: okta/entra, config)."
  type        = any
  default     = null
  sensitive   = true
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

variable "websocket" {
  description = "WebSocket gateway configuration (max_connections_per_user, transcript_buffer_size, pool)."
  type        = any
  default     = null
}

variable "server" {
  description = "HTTP server configuration (read_buffer_size, base_path, body_buffer_pool_limit_bytes, ...). Set base_path (e.g. \"/bifrost\") together with ingress_path to serve Bifrost under a URL prefix."
  type        = any
  default     = null
}

variable "ingress_path" {
  description = "Path prefix the Kubernetes ingress routes to Bifrost (EKS, GKE, AKS, generic Kubernetes). Keep \"/\" to own the whole host; set it to the same value as server.base_path (e.g. \"/bifrost\") to serve under a prefix. Bifrost accepts requests with or without the prefix, so probes stay on /health."
  type        = string
  default     = "/"
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
  description = "Create a load balancer. ECS: creates an ALB. EKS: creates a Kubernetes Ingress with ALB annotations (requires AWS Load Balancer Controller). GKE: creates a GCE Ingress. AKS: creates a Kubernetes Ingress."
  type        = bool
  default     = false
}

variable "assign_public_ip" {
  description = "Assign a public IP to the container (ECS Fargate). Set to false for private subnet deployments."
  type        = bool
  default     = true
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

variable "certificate_arn" {
  description = "ACM/SSL certificate ARN for HTTPS. Used by EKS (ALB Ingress) and can be extended for other services."
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

# --- Managed database and generated secrets (AWS ECS and GCP Cloud Run) ---
variable "create_database" {
  description = "Provision a managed PostgreSQL 16 database (RDS on AWS, Cloud SQL on GCP, Flexible Server on Azure) and point both Bifrost stores at it through PG_* environment variables. Supported for aws/ecs, gcp/cloud-run and azure/aci. Explicit config_store or logs_store values, and stores already present in config_json or config_json_file, still win."
  type        = bool
  default     = false
}

variable "database_name" {
  description = "Database name created on the managed PostgreSQL instance."
  type        = string
  default     = "bifrost"
}

variable "database_user" {
  description = "Application user created on the managed PostgreSQL instance. Its password is generated and stored in the cloud secret manager."
  type        = string
  default     = "bifrost"
}

variable "database_instance_class" {
  description = "Managed database size. AWS: an RDS instance class (default db.t4g.small). GCP: a Cloud SQL tier (default db-custom-1-3840). Azure: a Flexible Server SKU (default B_Standard_B1ms)."
  type        = string
  default     = null
}

variable "database_storage_gb" {
  description = "Managed database disk size in GB. Azure raises values below its 32 GB minimum."
  type        = number
  default     = 20
}

variable "database_deletion_protection" {
  description = "Protect the managed database from terraform destroy. Leave false for evaluation stacks so cleanup works; set true for production."
  type        = bool
  default     = false
}

variable "database_skip_final_snapshot" {
  description = "AWS only. Skip the final RDS snapshot on destroy. Set false to keep one named <name_prefix>-db-final; independent of database_deletion_protection, which has to be switched off before a destroy can run at all."
  type        = bool
  default     = true
}

variable "generate_secrets" {
  description = "Generate BIFROST_ENCRYPTION_KEY and BIFROST_SETUP_TOKEN, store them in the cloud secret manager (secure environment variables on ACI), inject them into the container, and reference them from config.json. Explicit encryption_key or setup_token values, and values already present in config_json or config_json_file, still win. Supported for aws/ecs, gcp/cloud-run and azure/aci."
  type        = bool
  default     = false
}

variable "setup_token" {
  description = "Bootstrap token used once to create the first admin account. Written to config.json as setup_token; accepts a literal or an env.VARIABLE reference."
  type        = string
  default     = null
  sensitive   = true
}

variable "key_vault_network_default_action" {
  description = "Azure only. Key Vault network ACL default: Deny limits data-plane access to the service subnet and trusted Azure services; Allow lets the Terraform runner (for example Azure Cloud Shell) write the config secret from any network."
  type        = string
  default     = "Deny"
  validation {
    condition     = contains(["Allow", "Deny"], var.key_vault_network_default_action)
    error_message = "key_vault_network_default_action must be Allow or Deny"
  }
}

variable "image_registry_credentials" {
  description = "Azure Container Instances only. Registry credentials for a private image (Bifrost Enterprise); each entry becomes an image_registry_credential block on the container group."
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
