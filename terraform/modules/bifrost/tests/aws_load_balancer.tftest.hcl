# =============================================================================
# AWS ECS behind the ALB: the load balancer gets its own security group that
# admits allowed_cidr on the listener port, and the task security group admits
# the container port only from that group. Runs target ./aws directly so the
# rules can be asserted; through the root module they sit in a nested module.
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
  enable_autoscaling           = false
  min_capacity                 = 1
  max_capacity                 = 3
  autoscaling_cpu_threshold    = 70
  autoscaling_memory_threshold = 80
  create_cluster               = true
  kubernetes_namespace         = "bifrost"
  node_count                   = 1
  volume_size_gb               = 10
  allowed_cidr                 = "203.0.113.0/24"
}

run "alb_is_the_only_public_entry_point" {
  command = plan
  module { source = "./aws" }
  variables {
    create_load_balancer = true
  }

  assert {
    condition     = length(aws_security_group.load_balancer) == 1
    error_message = "the ALB must get a security group of its own"
  }
  assert {
    condition     = aws_vpc_security_group_ingress_rule.load_balancer_http[0].from_port == 80 && aws_vpc_security_group_ingress_rule.load_balancer_http[0].cidr_ipv4 == "203.0.113.0/24"
    error_message = "allowed_cidr must reach the ALB listener on port 80"
  }
  assert {
    condition     = aws_vpc_security_group_ingress_rule.container_port[0].cidr_ipv4 == null
    error_message = "with an ALB the task security group must not admit the container port from a CIDR; only the ALB security group may reach it"
  }
}

run "no_alb_keeps_direct_cidr_access" {
  command = plan
  module { source = "./aws" }
  variables {
    create_load_balancer = false
  }

  assert {
    condition     = length(aws_security_group.load_balancer) == 0
    error_message = "no ALB means no ALB security group"
  }
  assert {
    condition     = aws_vpc_security_group_ingress_rule.container_port[0].cidr_ipv4 == "203.0.113.0/24"
    error_message = "without an ALB allowed_cidr reaches the container port directly, as before"
  }
}
