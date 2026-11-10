# =============================================================================
# AWS managed database: snapshot behavior is independent of deletion
# protection. Runs target ./aws directly so assertions can read the RDS
# resource; through the root module it sits in a nested module.
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
mock_provider "kubernetes" {}
mock_provider "random" {}

variables {
  service                      = "ecs"
  config_json                  = "{}"
  image                        = "maximhq/bifrost:latest"
  container_port               = 8080
  health_check_path            = "/health"
  region                       = "us-east-1"
  name_prefix                  = "test"
  tags                         = {}
  desired_count                = 1
  cpu                          = 1024
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
  create_database              = true
}

run "database_skips_final_snapshot_by_default" {
  command = plan
  module { source = "./aws" }

  assert {
    condition     = aws_db_instance.bifrost[0].skip_final_snapshot == true
    error_message = "the default must skip the final snapshot so evaluation stacks destroy cleanly"
  }
  assert {
    condition     = aws_db_instance.bifrost[0].final_snapshot_identifier == null
    error_message = "no snapshot identifier is needed when the snapshot is skipped"
  }
  assert {
    condition     = aws_db_instance.bifrost[0].deletion_protection == false
    error_message = "deletion protection stays off by default"
  }
}

run "database_final_snapshot_without_deletion_protection" {
  command = plan
  module { source = "./aws" }
  variables {
    database_deletion_protection = false
    database_skip_final_snapshot = false
  }

  assert {
    condition     = aws_db_instance.bifrost[0].skip_final_snapshot == false
    error_message = "database_skip_final_snapshot = false must keep the final snapshot even with deletion protection off, which is the state a protected database is in right before terraform destroy"
  }
  assert {
    condition     = aws_db_instance.bifrost[0].final_snapshot_identifier == "test-db-final"
    error_message = "the final snapshot identifier must be set when the snapshot is kept"
  }
}

run "database_deletion_protection_does_not_force_snapshot" {
  command = plan
  module { source = "./aws" }
  variables {
    database_deletion_protection = true
  }

  assert {
    condition     = aws_db_instance.bifrost[0].deletion_protection == true
    error_message = "database_deletion_protection must enable RDS deletion protection"
  }
  assert {
    condition     = aws_db_instance.bifrost[0].skip_final_snapshot == true
    error_message = "deletion protection must not change the snapshot setting; database_skip_final_snapshot controls it"
  }
}

# BIFROST_CONFIG comes from an unversioned Secrets Manager ARN, so a changed
# config.json alone leaves running tasks on the old file. A hash of the config
# on the task definition registers a new revision and rolls the service.
run "config_change_rolls_ecs_tasks" {
  command = plan
  module { source = "./aws" }
  variables {
    config_json = "{\"providers\":{}}"
  }

  assert {
    condition     = contains([for e in module.ecs[0].container_environment : e.value if e.name == "BIFROST_CONFIG_HASH"], sha256("{\"providers\":{}}"))
    error_message = "the ECS task definition must carry BIFROST_CONFIG_HASH = sha256(config.json) so a configuration change rolls the tasks, got ${jsonencode([for e in module.ecs[0].container_environment : e.name])}"
  }
}
