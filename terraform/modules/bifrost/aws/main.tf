# =============================================================================
# AWS Platform Module — shared infrastructure for ECS and EKS
# =============================================================================

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.0"
    }
  }
}

# --- Data sources for existing resources ---

data "aws_availability_zones" "available" {
  state = "available"
}

data "aws_region" "current" {}

# --- Locals ---

locals {
  create_vpc             = var.existing_vpc_id == null
  create_subnets         = var.existing_subnet_ids == null
  create_security_groups = var.existing_security_group_ids == null

  vpc_id             = local.create_vpc ? aws_vpc.this[0].id : var.existing_vpc_id
  subnet_ids         = local.create_subnets ? aws_subnet.public[*].id : var.existing_subnet_ids
  security_group_ids = local.create_security_groups ? [aws_security_group.this[0].id] : var.existing_security_group_ids

  azs = slice(data.aws_availability_zones.available.names, 0, 2)
}

# =============================================================================
# VPC (optional — created only when existing_vpc_id is not provided)
# =============================================================================

resource "aws_vpc" "this" {
  count = local.create_vpc ? 1 : 0

  cidr_block           = "10.0.0.0/16"
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-vpc"
  })
}

resource "aws_internet_gateway" "this" {
  count = local.create_vpc ? 1 : 0

  vpc_id = aws_vpc.this[0].id

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-igw"
  })
}

resource "aws_subnet" "public" {
  count = local.create_subnets ? length(local.azs) : 0

  vpc_id                  = local.vpc_id
  cidr_block              = cidrsubnet("10.0.0.0/16", 8, count.index)
  availability_zone       = local.azs[count.index]
  map_public_ip_on_launch = true

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-public-${local.azs[count.index]}"
  })
}

resource "aws_route_table" "public" {
  count = local.create_vpc ? 1 : 0

  vpc_id = aws_vpc.this[0].id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.this[0].id
  }

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-public-rt"
  })
}

resource "aws_route_table_association" "public" {
  count = local.create_subnets && local.create_vpc ? length(local.azs) : 0

  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public[0].id
}

# =============================================================================
# Security Group (optional — created only when existing IDs not provided)
# =============================================================================

resource "aws_security_group" "this" {
  count = local.create_security_groups ? 1 : 0

  name        = "${var.name_prefix}-sg"
  description = "Security group for Bifrost ${var.service} service"
  vpc_id      = local.vpc_id

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-sg"
  })
}

resource "aws_vpc_security_group_ingress_rule" "container_port" {
  count = local.create_security_groups ? 1 : 0

  security_group_id = aws_security_group.this[0].id
  description       = "Allow inbound traffic on container port"
  cidr_ipv4         = var.allowed_cidr
  from_port         = var.container_port
  to_port           = var.container_port
  ip_protocol       = "tcp"

  tags = var.tags
}

resource "aws_vpc_security_group_egress_rule" "all_outbound" {
  count = local.create_security_groups ? 1 : 0

  security_group_id = aws_security_group.this[0].id
  description       = "Allow all outbound traffic"
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"

  tags = var.tags
}

# =============================================================================
# Secrets Manager — stores Bifrost config.json
# =============================================================================

resource "aws_secretsmanager_secret" "bifrost_config" {
  name                    = "${var.name_prefix}/config"
  description             = "Bifrost configuration (config.json)"
  recovery_window_in_days = var.secret_recovery_window_days

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-config"
  })
}

resource "aws_secretsmanager_secret_version" "bifrost_config" {
  secret_id     = aws_secretsmanager_secret.bifrost_config.id
  secret_string = var.config_json
}

# =============================================================================
# IAM — ECS task execution role
# =============================================================================

data "aws_iam_policy_document" "ecs_assume_role" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "ecs_execution" {
  name               = "${var.name_prefix}-ecs-execution"
  assume_role_policy = data.aws_iam_policy_document.ecs_assume_role.json

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-ecs-execution"
  })
}

resource "aws_iam_role_policy_attachment" "ecs_execution_policy" {
  role       = aws_iam_role.ecs_execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

data "aws_iam_policy_document" "bifrost_secrets" {
  statement {
    sid    = "AllowGetBifrostSecret"
    effect = "Allow"

    actions = [
      "secretsmanager:GetSecretValue",
    ]

    resources = [
      aws_secretsmanager_secret.bifrost_config.arn,
    ]
  }

  statement {
    sid    = "AllowCloudWatchLogs"
    effect = "Allow"

    actions = [
      "logs:CreateLogStream",
      "logs:PutLogEvents",
    ]

    resources = [
      "${aws_cloudwatch_log_group.bifrost.arn}:*",
    ]
  }
}

resource "aws_iam_role_policy" "bifrost_secrets" {
  name   = "${var.name_prefix}-secrets-access"
  role   = aws_iam_role.ecs_execution.id
  policy = data.aws_iam_policy_document.bifrost_secrets.json
}

# =============================================================================
# CloudWatch Log Group
# =============================================================================

resource "aws_cloudwatch_log_group" "bifrost" {
  name              = "/ecs/${var.name_prefix}"
  retention_in_days = 30

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-logs"
  })
}

# =============================================================================
# Service Modules — route to ECS or EKS
# =============================================================================

module "ecs" {
  source = "./services/ecs"
  count  = var.service == "ecs" ? 1 : 0

  # Container
  image             = var.image
  container_port    = var.container_port
  health_check_path = var.health_check_path

  # Infrastructure
  name_prefix = var.name_prefix
  region      = var.region
  tags        = var.tags

  # Compute
  desired_count = var.desired_count
  cpu           = var.cpu
  memory        = var.memory

  # Networking
  vpc_id             = local.vpc_id
  subnet_ids         = local.subnet_ids
  security_group_ids = local.security_group_ids

  # IAM & secrets
  execution_role_arn = aws_iam_role.ecs_execution.arn
  secret_arn         = aws_secretsmanager_secret.bifrost_config.arn
  log_group_name     = aws_cloudwatch_log_group.bifrost.name

  # Features
  create_cluster               = var.create_cluster
  create_load_balancer         = var.create_load_balancer
  enable_autoscaling           = var.enable_autoscaling
  min_capacity                 = var.min_capacity
  max_capacity                 = var.max_capacity
  autoscaling_cpu_threshold    = var.autoscaling_cpu_threshold
  autoscaling_memory_threshold = var.autoscaling_memory_threshold
}

module "eks" {
  source = "./services/eks"
  count  = var.service == "eks" ? 1 : 0

  # Container
  image             = var.image
  container_port    = var.container_port
  health_check_path = var.health_check_path

  # Infrastructure
  name_prefix = var.name_prefix
  region      = var.region
  tags        = var.tags

  # Compute
  desired_count = var.desired_count
  cpu           = var.cpu
  memory        = var.memory

  # Networking
  vpc_id             = local.vpc_id
  subnet_ids         = local.subnet_ids
  security_group_ids = local.security_group_ids

  # IAM & secrets
  secret_arn  = aws_secretsmanager_secret.bifrost_config.arn
  config_json = var.config_json

  # Domain
  domain_name = var.domain_name

  # K8s-specific
  create_cluster       = var.create_cluster
  kubernetes_namespace = var.kubernetes_namespace
  node_count           = var.node_count
  node_machine_type    = var.node_machine_type
  volume_size_gb       = var.volume_size_gb

  # Features
  create_load_balancer         = var.create_load_balancer
  enable_autoscaling           = var.enable_autoscaling
  min_capacity                 = var.min_capacity
  max_capacity                 = var.max_capacity
  autoscaling_cpu_threshold    = var.autoscaling_cpu_threshold
  autoscaling_memory_threshold = var.autoscaling_memory_threshold
}
