variable "project_id" {
  description = "GCP project ID."
  type        = string
}

variable "region" {
  description = "GCP region to deploy into."
  type        = string
  default     = "us-central1"
}

variable "image_repository" {
  description = "Bifrost image repository. Enterprise: the Artifact Registry path issued for your organization."
  type        = string
  default     = "maximhq/bifrost"
}

variable "image_tag" {
  description = "Bifrost image tag. Pin a released version for production."
  type        = string
  default     = "latest"
}

variable "name_prefix" {
  description = "Prefix for all resource names."
  type        = string
  default     = "bifrost"
}

variable "kubeconfig_path" {
  description = "Kubeconfig the kubernetes provider reads. deploy/gke/deploy.sh writes it with gcloud container clusters get-credentials after the cluster exists."
  type        = string
  default     = "~/.kube/config"
}

variable "kubeconfig_context" {
  description = "Kubeconfig context to use. Null uses the current context."
  type        = string
  default     = null
}

# --- Cluster ---

variable "node_count" {
  description = "Nodes in the node pool (per zone for a regional cluster)."
  type        = number
  default     = 1
}

variable "node_machine_type" {
  description = "Machine type for the node pool."
  type        = string
  default     = "e2-standard-2"
}

# --- Database ---

variable "database_instance_class" {
  description = "Cloud SQL tier."
  type        = string
  default     = "db-custom-1-3840"
}

variable "database_storage_gb" {
  description = "Cloud SQL disk size in GB."
  type        = number
  default     = 20
}

variable "database_deletion_protection" {
  description = "Enable Cloud SQL deletion protection."
  type        = bool
  default     = false
}

# --- Access ---

variable "enforce_auth_on_inference" {
  description = "Require a virtual key on inference routes. Keep true while the load balancer is public."
  type        = bool
  default     = true
}

variable "providers_config" {
  description = "Optional LLM provider configuration written to config.json."
  type        = any
  default     = null
  sensitive   = true
}

# --- Compute ---

variable "service_type" {
  description = "Kubernetes Service type for Bifrost. LoadBalancer exposes the public TCP load balancer the deploy script prints (plain HTTP); set ClusterIP once a TLS ingress or load balancer is in front so the public port-80 endpoint disappears."
  type        = string
  default     = "LoadBalancer"

  validation {
    condition     = contains(["ClusterIP", "LoadBalancer", "NodePort"], var.service_type)
    error_message = "service_type must be ClusterIP, LoadBalancer or NodePort."
  }
}

variable "allowed_cidr" {
  description = "CIDR allowed to reach the Bifrost Service (loadBalancerSourceRanges on the public load balancer) and the VPC ingress firewall rule. It does not restrict the GKE control plane endpoint; narrow this for production."
  type        = string
  default     = "0.0.0.0/0"
}

variable "cpu" {
  description = "CPU request in millicores."
  type        = number
  default     = 1000
}

variable "memory" {
  description = "Memory request in MB."
  type        = number
  default     = 2048
}

variable "tags" {
  description = "Labels applied to every resource."
  type        = map(string)
  default     = { app = "bifrost" }
}
