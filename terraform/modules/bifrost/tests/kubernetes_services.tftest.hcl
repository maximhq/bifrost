# =============================================================================
# Kubernetes service modules (EKS, GKE, AKS), asserted directly: the Service
# honours service_type and restricts a LoadBalancer to allowed_cidr, the pod
# template carries a hash of the runtime Secret so a rotation rolls the pods,
# and the managed environment reaches the container (plain and Secret-backed).
# The Azure platform module must also open the Service port on its subnet NSG.
# =============================================================================

mock_provider "aws" {
  mock_data "aws_availability_zones" {
    defaults = { names = ["us-east-1a", "us-east-1b", "us-east-1c"] }
  }
  mock_data "aws_caller_identity" {
    defaults = { account_id = "123456789012" }
  }
  mock_data "aws_region" {
    defaults = { name = "us-east-1" }
  }
  mock_data "aws_iam_policy_document" {
    defaults = { json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}" }
  }
}
mock_provider "google" {}
mock_provider "azurerm" {
  mock_data "azurerm_client_config" {
    defaults = {
      tenant_id       = "00000000-0000-0000-0000-000000000000"
      subscription_id = "00000000-0000-0000-0000-000000000000"
      object_id       = "00000000-0000-0000-0000-000000000000"
    }
  }
}
mock_provider "kubernetes" {}
mock_provider "random" {}

variables {
  image                        = "maximhq/bifrost:latest"
  container_port               = 8080
  health_check_path            = "/health"
  config_json                  = "{}"
  name_prefix                  = "test"
  region                       = "us-east-1"
  tags                         = {}
  desired_count                = 1
  cpu                          = 1000
  memory                       = 2048
  create_cluster               = true
  kubernetes_namespace         = "bifrost"
  node_count                   = 1
  volume_size_gb               = 10
  create_load_balancer         = false
  enable_autoscaling           = false
  min_capacity                 = 1
  max_capacity                 = 1
  autoscaling_cpu_threshold    = 80
  autoscaling_memory_threshold = 80
  service_type                 = "LoadBalancer"
  allowed_cidr                 = "203.0.113.0/24"
  extra_environment            = { PG_HOST = "db.internal", PG_PORT = "5432" }
  extra_secret_environment     = { PG_PASSWORD = "s3cret", BIFROST_ENCRYPTION_KEY = "k" }
  # EKS
  subnet_ids         = ["subnet-1", "subnet-2"]
  security_group_ids = ["sg-1"]
  # GKE
  service_name    = "test-service"
  project_id      = "test-project"
  service_account = "bifrost@test-project.iam.gserviceaccount.com"
  vpc_id          = "projects/test-project/global/networks/test"
  subnet_id       = "projects/test-project/regions/us-east-1/subnetworks/test"
  # AKS
  resource_group_name = "test-rg"
  identity_id         = "/subscriptions/0/resourceGroups/test-rg/providers/Microsoft.ManagedIdentity/userAssignedIdentities/test"
}

run "eks_service_and_deployment" {
  command = plan
  module { source = "./aws/services/eks" }

  assert {
    condition     = kubernetes_service.bifrost.spec[0].type == "LoadBalancer"
    error_message = "service_type must reach the Service"
  }
  assert {
    condition     = length(kubernetes_service.bifrost.spec[0].load_balancer_source_ranges) == 1 && contains(kubernetes_service.bifrost.spec[0].load_balancer_source_ranges, "203.0.113.0/24")
    error_message = "a LoadBalancer Service must restrict its sources to allowed_cidr"
  }
  assert {
    condition     = kubernetes_deployment.bifrost.spec[0].template[0].metadata[0].annotations["bifrost.getbifrost.ai/runtime-secret-hash"] == sha256(jsonencode(var.extra_secret_environment))
    error_message = "the pod template must carry the sha256 of the runtime Secret data so a rotation rolls the pods"
  }
  assert {
    condition     = kubernetes_deployment.bifrost.spec[0].template[0].metadata[0].annotations["bifrost.getbifrost.ai/config-hash"] == sha256(var.config_json)
    error_message = "the pod template must carry the sha256 of config.json: it is a subPath mount, which running pods never see change, so a configuration change must roll them"
  }
  assert {
    condition     = kubernetes_secret.bifrost_config.metadata[0].name == "test-config" && kubernetes_secret.bifrost_runtime[0].metadata[0].name == "test-runtime"
    error_message = "both Secrets must be named after name_prefix"
  }
  assert {
    condition     = contains([for e in kubernetes_deployment.bifrost.spec[0].template[0].spec[0].container[0].env : e.name], "PG_HOST")
    error_message = "extra_environment must reach the container"
  }
  assert {
    condition     = anytrue([for e in kubernetes_deployment.bifrost.spec[0].template[0].spec[0].container[0].env : e.name == "PG_PASSWORD" && try(e.value_from[0].secret_key_ref[0].name, "") == "test-runtime"])
    error_message = "secret-backed entries must come from the <name_prefix>-runtime Secret"
  }
}

run "gke_service_and_deployment" {
  command = plan
  module { source = "./gcp/services/gke" }
  variables {
    region = "us-central1"
  }

  assert {
    condition     = kubernetes_service.bifrost.spec[0].type == "LoadBalancer"
    error_message = "service_type must reach the Service"
  }
  assert {
    condition     = length(kubernetes_service.bifrost.spec[0].load_balancer_source_ranges) == 1 && contains(kubernetes_service.bifrost.spec[0].load_balancer_source_ranges, "203.0.113.0/24")
    error_message = "a LoadBalancer Service must restrict its sources to allowed_cidr"
  }
  assert {
    condition     = kubernetes_deployment.bifrost.spec[0].template[0].metadata[0].annotations["bifrost.getbifrost.ai/runtime-secret-hash"] == sha256(jsonencode(var.extra_secret_environment))
    error_message = "the pod template must carry the sha256 of the runtime Secret data so a rotation rolls the pods"
  }
  assert {
    condition     = kubernetes_deployment.bifrost.spec[0].template[0].metadata[0].annotations["bifrost.getbifrost.ai/config-hash"] == sha256(var.config_json)
    error_message = "the pod template must carry the sha256 of config.json: it is a subPath mount, which running pods never see change, so a configuration change must roll them"
  }
  assert {
    condition     = kubernetes_secret.bifrost_config.metadata[0].name == "test-config" && kubernetes_secret.bifrost_runtime[0].metadata[0].name == "test-runtime"
    error_message = "both Secrets must be named after name_prefix"
  }
  assert {
    condition     = anytrue([for e in kubernetes_deployment.bifrost.spec[0].template[0].spec[0].container[0].env : e.name == "PG_PASSWORD" && try(e.value_from[0].secret_key_ref[0].name, "") == "test-runtime"])
    error_message = "secret-backed entries must come from the <name_prefix>-runtime Secret"
  }
}

run "aks_service_and_deployment" {
  command = plan
  module { source = "./azure/services/aks" }
  variables {
    region     = "eastus"
    subnet_ids = ["/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/test-rg/providers/Microsoft.Network/virtualNetworks/test-vnet/subnets/test-subnet"]
  }

  assert {
    condition     = kubernetes_service.bifrost.spec[0].type == "LoadBalancer"
    error_message = "service_type must reach the Service"
  }
  assert {
    condition     = length(kubernetes_service.bifrost.spec[0].load_balancer_source_ranges) == 1 && contains(kubernetes_service.bifrost.spec[0].load_balancer_source_ranges, "203.0.113.0/24")
    error_message = "a LoadBalancer Service must restrict its sources to allowed_cidr"
  }
  assert {
    condition     = kubernetes_deployment.bifrost.spec[0].template[0].metadata[0].annotations["bifrost.getbifrost.ai/runtime-secret-hash"] == sha256(jsonencode(var.extra_secret_environment))
    error_message = "the pod template must carry the sha256 of the runtime Secret data so a rotation rolls the pods"
  }
  assert {
    condition     = kubernetes_deployment.bifrost.spec[0].template[0].metadata[0].annotations["bifrost.getbifrost.ai/config-hash"] == sha256(var.config_json)
    error_message = "the pod template must carry the sha256 of config.json: it is a subPath mount, which running pods never see change, so a configuration change must roll them"
  }
  assert {
    condition     = kubernetes_secret.bifrost_config.metadata[0].name == "test-config" && kubernetes_secret.bifrost_runtime[0].metadata[0].name == "test-runtime"
    error_message = "both Secrets must be named after name_prefix"
  }
  assert {
    condition     = anytrue([for e in kubernetes_deployment.bifrost.spec[0].template[0].spec[0].container[0].env : e.name == "PG_PASSWORD" && try(e.value_from[0].secret_key_ref[0].name, "") == "test-runtime"])
    error_message = "secret-backed entries must come from the <name_prefix>-runtime Secret"
  }
}

run "open_cidr_leaves_source_ranges_unset" {
  command = plan
  module { source = "./aws/services/eks" }
  variables {
    allowed_cidr = "0.0.0.0/0"
  }

  assert {
    condition     = try(length(kubernetes_service.bifrost.spec[0].load_balancer_source_ranges), 0) == 0
    error_message = "0.0.0.0/0 must leave load_balancer_source_ranges unset"
  }
}

run "aks_platform_opens_service_port_on_the_nsg" {
  command = plan
  module { source = "./azure" }
  variables {
    service         = "aks"
    region          = "eastus"
    create_database = false
  }

  assert {
    condition     = anytrue([for r in azurerm_network_security_group.this.security_rule : r.destination_port_range == "80" && r.source_address_prefix == "203.0.113.0/24"])
    error_message = "with an AKS LoadBalancer Service the subnet NSG must admit allowed_cidr on the Service port 80"
  }
}

# Rotating a runtime secret must change the pod-template annotation, which is
# what makes the Deployment roll; a constant annotation would pass a mere
# presence check.

run "eks_rotation_changes_the_pod_annotation" {
  command = plan
  module { source = "./aws/services/eks" }
  variables {
    extra_secret_environment = { PG_PASSWORD = "rotated", BIFROST_ENCRYPTION_KEY = "k" }
  }

  assert {
    condition     = kubernetes_deployment.bifrost.spec[0].template[0].metadata[0].annotations["bifrost.getbifrost.ai/runtime-secret-hash"] == sha256(jsonencode({ PG_PASSWORD = "rotated", BIFROST_ENCRYPTION_KEY = "k" }))
    error_message = "the annotation must be the hash of the rotated Secret data"
  }
  assert {
    condition     = kubernetes_deployment.bifrost.spec[0].template[0].metadata[0].annotations["bifrost.getbifrost.ai/runtime-secret-hash"] != sha256(jsonencode({ PG_PASSWORD = "s3cret", BIFROST_ENCRYPTION_KEY = "k" }))
    error_message = "the annotation must differ from the hash of the previous Secret data"
  }
}

run "gke_rotation_changes_the_pod_annotation" {
  command = plan
  module { source = "./gcp/services/gke" }
  variables {
    region                   = "us-central1"
    extra_secret_environment = { PG_PASSWORD = "rotated", BIFROST_ENCRYPTION_KEY = "k" }
  }

  assert {
    condition     = kubernetes_deployment.bifrost.spec[0].template[0].metadata[0].annotations["bifrost.getbifrost.ai/runtime-secret-hash"] == sha256(jsonencode({ PG_PASSWORD = "rotated", BIFROST_ENCRYPTION_KEY = "k" }))
    error_message = "the annotation must be the hash of the rotated Secret data"
  }
  assert {
    condition     = kubernetes_deployment.bifrost.spec[0].template[0].metadata[0].annotations["bifrost.getbifrost.ai/runtime-secret-hash"] != sha256(jsonencode({ PG_PASSWORD = "s3cret", BIFROST_ENCRYPTION_KEY = "k" }))
    error_message = "the annotation must differ from the hash of the previous Secret data"
  }
}

run "aks_rotation_changes_the_pod_annotation" {
  command = plan
  module { source = "./azure/services/aks" }
  variables {
    region                   = "eastus"
    subnet_ids               = ["/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/test-rg/providers/Microsoft.Network/virtualNetworks/test-vnet/subnets/test-subnet"]
    extra_secret_environment = { PG_PASSWORD = "rotated", BIFROST_ENCRYPTION_KEY = "k" }
  }

  assert {
    condition     = kubernetes_deployment.bifrost.spec[0].template[0].metadata[0].annotations["bifrost.getbifrost.ai/runtime-secret-hash"] == sha256(jsonencode({ PG_PASSWORD = "rotated", BIFROST_ENCRYPTION_KEY = "k" }))
    error_message = "the annotation must be the hash of the rotated Secret data"
  }
  assert {
    condition     = kubernetes_deployment.bifrost.spec[0].template[0].metadata[0].annotations["bifrost.getbifrost.ai/runtime-secret-hash"] != sha256(jsonencode({ PG_PASSWORD = "s3cret", BIFROST_ENCRYPTION_KEY = "k" }))
    error_message = "the annotation must differ from the hash of the previous Secret data"
  }
}

run "eks_config_change_rolls_the_pods" {
  command = plan
  module { source = "./aws/services/eks" }
  variables {
    config_json = "{\"providers\":{}}"
  }

  assert {
    condition     = kubernetes_deployment.bifrost.spec[0].template[0].metadata[0].annotations["bifrost.getbifrost.ai/config-hash"] == sha256("{\"providers\":{}}")
    error_message = "the config-hash annotation must follow the new config.json"
  }
  assert {
    condition     = kubernetes_deployment.bifrost.spec[0].template[0].metadata[0].annotations["bifrost.getbifrost.ai/config-hash"] != sha256("{}")
    error_message = "the config-hash annotation must differ from the previous config.json hash"
  }
}

run "gke_config_change_rolls_the_pods" {
  command = plan
  module { source = "./gcp/services/gke" }
  variables {
    region      = "us-central1"
    config_json = "{\"providers\":{}}"
  }

  assert {
    condition     = kubernetes_deployment.bifrost.spec[0].template[0].metadata[0].annotations["bifrost.getbifrost.ai/config-hash"] == sha256("{\"providers\":{}}")
    error_message = "the config-hash annotation must follow the new config.json"
  }
  assert {
    condition     = kubernetes_deployment.bifrost.spec[0].template[0].metadata[0].annotations["bifrost.getbifrost.ai/config-hash"] != sha256("{}")
    error_message = "the config-hash annotation must differ from the previous config.json hash"
  }
}

run "aks_config_change_rolls_the_pods" {
  command = plan
  module { source = "./azure/services/aks" }
  variables {
    region      = "eastus"
    subnet_ids  = ["/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/test-rg/providers/Microsoft.Network/virtualNetworks/test-vnet/subnets/test-subnet"]
    config_json = "{\"providers\":{}}"
  }

  assert {
    condition     = kubernetes_deployment.bifrost.spec[0].template[0].metadata[0].annotations["bifrost.getbifrost.ai/config-hash"] == sha256("{\"providers\":{}}")
    error_message = "the config-hash annotation must follow the new config.json"
  }
  assert {
    condition     = kubernetes_deployment.bifrost.spec[0].template[0].metadata[0].annotations["bifrost.getbifrost.ai/config-hash"] != sha256("{}")
    error_message = "the config-hash annotation must differ from the previous config.json hash"
  }
}

# The one-click scripts read these outputs after the first, cluster-only apply
# to fetch kubeconfig credentials, so each platform must report what it created
# rather than what the shell assumed.
run "eks_platform_reports_cluster_identifiers" {
  command = plan
  module { source = "./aws" }
  variables {
    service         = "eks"
    region          = "us-west-2"
    create_database = false
  }

  assert {
    condition     = output.cluster_name == "test-cluster"
    error_message = "cluster_name must be the EKS cluster the module creates (<name_prefix>-cluster)"
  }
  assert {
    condition     = output.cluster_region == "us-west-2"
    error_message = "cluster_region must be the region the cluster was created in"
  }
}

run "gke_platform_reports_cluster_identifiers" {
  command = plan
  module { source = "./gcp" }
  variables {
    service         = "gke"
    region          = "us-central1"
    create_database = false
  }

  assert {
    condition     = output.cluster_name == "test-cluster"
    error_message = "cluster_name must be the GKE cluster the module creates (<name_prefix>-cluster)"
  }
  assert {
    condition     = output.cluster_region == "us-central1"
    error_message = "cluster_region must be the region the cluster was created in"
  }
  assert {
    condition     = output.cluster_project == "test-project"
    error_message = "cluster_project must be the project the cluster was created in"
  }
}

run "aks_platform_reports_cluster_identifiers" {
  command = plan
  module { source = "./azure" }
  variables {
    service         = "aks"
    region          = "eastus"
    create_database = false
  }

  assert {
    condition     = output.cluster_name == "test-aks"
    error_message = "cluster_name must be the AKS cluster the module creates (<name_prefix>-aks)"
  }
  assert {
    condition     = output.cluster_resource_group == "test-rg"
    error_message = "cluster_resource_group must be the resource group the cluster lives in"
  }
}
