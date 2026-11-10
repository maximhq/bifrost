# =============================================================================
# GCP managed database: GKE reaches Cloud SQL over private IP with TLS
# enforced, Cloud Run over the connector socket; Secret Manager copies of the
# runtime values and the cloudsql.client binding exist for Cloud Run only,
# since GKE receives its values through the bifrost-runtime Kubernetes Secret.
# =============================================================================

mock_provider "google" {}
mock_provider "kubernetes" {}
mock_provider "random" {}

variables {
  config_json                  = "{}"
  image                        = "maximhq/bifrost:latest"
  container_port               = 8080
  health_check_path            = "/health"
  region                       = "us-central1"
  name_prefix                  = "test"
  tags                         = {}
  desired_count                = 1
  cpu                          = 1000
  memory                       = 2048
  create_load_balancer         = false
  enable_autoscaling           = false
  min_capacity                 = 1
  max_capacity                 = 3
  autoscaling_cpu_threshold    = 70
  autoscaling_memory_threshold = 80
  create_cluster               = true
  kubernetes_namespace         = "bifrost"
  node_count                   = 1
  volume_size_gb               = 10
  project_id                   = "test-project"
  create_database              = true
  generate_secrets             = true
}

run "gke_private_ip_with_tls_and_no_secret_manager_copies" {
  command = plan
  module { source = "./gcp" }
  variables {
    service = "gke"
  }

  assert {
    condition     = google_sql_database_instance.bifrost[0].settings[0].ip_configuration[0].ssl_mode == "ENCRYPTED_ONLY"
    error_message = "the GKE instance must enforce TLS on its private IP"
  }
  assert {
    condition     = google_sql_database_instance.bifrost[0].settings[0].ip_configuration[0].ipv4_enabled == false
    error_message = "the GKE instance must not expose a public IP"
  }
  assert {
    condition     = length(google_secret_manager_secret.database_password) == 0 && length(google_secret_manager_secret.encryption_key) == 0 && length(google_secret_manager_secret.setup_token) == 0
    error_message = "GKE consumes the Kubernetes Secret; no Secret Manager copies of the runtime values"
  }
  assert {
    condition     = length(google_project_iam_member.bifrost_cloudsql_client) == 0
    error_message = "GKE connects over private IP and needs no cloudsql.client binding"
  }
}

run "cloud_run_keeps_connector_and_secret_manager" {
  command = plan
  module { source = "./gcp" }
  variables {
    service = "cloud-run"
  }

  assert {
    condition     = google_sql_database_instance.bifrost[0].settings[0].ip_configuration[0].ipv4_enabled == true
    error_message = "Cloud Run reaches the instance through the connector, which needs the public IP enabled"
  }
  assert {
    condition     = length(google_secret_manager_secret.database_password) == 1 && length(google_secret_manager_secret.encryption_key) == 1 && length(google_secret_manager_secret.setup_token) == 1
    error_message = "Cloud Run reads the runtime values from Secret Manager"
  }
  assert {
    condition     = length(google_project_iam_member.bifrost_cloudsql_client) == 1
    error_message = "Cloud Run's service account needs cloudsql.client for the connector"
  }
}
