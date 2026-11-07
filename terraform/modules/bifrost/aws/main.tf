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
  is_ecs                 = var.service == "ecs"
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
# Secrets Manager — stores Bifrost config.json (ECS only)
# EKS uses Kubernetes secrets directly and does not need Secrets Manager.
# =============================================================================

resource "aws_secretsmanager_secret" "bifrost_config" {
  count = local.is_ecs ? 1 : 0

  name                    = "${var.name_prefix}/config"
  description             = "Bifrost configuration (config.json)"
  recovery_window_in_days = var.secret_recovery_window_days

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-config"
  })
}

resource "aws_secretsmanager_secret_version" "bifrost_config" {
  count = local.is_ecs ? 1 : 0

  secret_id     = aws_secretsmanager_secret.bifrost_config[0].id
  secret_string = var.config_json
}

# =============================================================================
# IAM — ECS task execution role (ECS only)
# =============================================================================

resource "aws_iam_role" "ecs_execution" {
  count = local.is_ecs ? 1 : 0

  name               = "${var.name_prefix}-ecs-execution"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Action = "sts:AssumeRole"
      Effect = "Allow"
      Principal = {
        Service = "ecs-tasks.amazonaws.com"
      }
    }]
  })

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-ecs-execution"
  })
}

resource "aws_iam_role_policy_attachment" "ecs_execution_policy" {
  count = local.is_ecs ? 1 : 0

  role       = aws_iam_role.ecs_execution[0].name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

resource "aws_iam_role_policy" "bifrost_secrets" {
  count = local.is_ecs ? 1 : 0

  name   = "${var.name_prefix}-secrets-access"
  role   = aws_iam_role.ecs_execution[0].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "AllowGetBifrostSecret"
        Effect = "Allow"
        Action = ["secretsmanager:GetSecretValue"]
        Resource = [aws_secretsmanager_secret.bifrost_config[0].arn]
      },
      {
        Sid    = "AllowCloudWatchLogs"
        Effect = "Allow"
        Action = ["logs:CreateLogStream", "logs:PutLogEvents"]
        Resource = ["${aws_cloudwatch_log_group.bifrost[0].arn}:*"]
      }
    ]
  })
}

# =============================================================================
# CloudWatch Log Group (ECS only)
# =============================================================================

resource "aws_cloudwatch_log_group" "bifrost" {
  count = local.is_ecs ? 1 : 0

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
  execution_role_arn = aws_iam_role.ecs_execution[0].arn
  secret_arn         = aws_secretsmanager_secret.bifrost_config[0].arn
  log_group_name     = aws_cloudwatch_log_group.bifrost[0].name

  # Managed database and generated secrets
  extra_environment = local.managed_environment
  extra_secrets     = local.managed_secrets

  # Features
  create_cluster               = var.create_cluster
  create_load_balancer         = var.create_load_balancer
  assign_public_ip             = var.assign_public_ip
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
  ingress_path      = var.ingress_path

  # Infrastructure
  name_prefix = var.name_prefix
  region      = var.region
  tags        = var.tags

  # Compute
  desired_count = var.desired_count
  cpu           = var.cpu
  memory        = var.memory

  # Networking
  subnet_ids         = local.subnet_ids
  security_group_ids = local.security_group_ids

  # Config
  config_json = var.config_json

  # Domain
  domain_name     = var.domain_name
  certificate_arn = var.certificate_arn

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

# =============================================================================
# Managed PostgreSQL (RDS) and generated runtime secrets (ECS only)
#
# The one-click AWS path provisions these so a fresh account gets a durable
# config store, a durable log store, a stable encryption key and a setup token
# without any manual secret handling. Everything sensitive lands in one
# Secrets Manager secret; the ECS task reads individual JSON keys from it.
# =============================================================================

locals {
  manage_database       = local.is_ecs && var.create_database
  generate_secrets      = local.is_ecs && var.generate_secrets
  create_runtime_secret = local.manage_database || local.generate_secrets

  database_instance_class = coalesce(var.database_instance_class, "db.t4g.small")

  runtime_secret_payload = merge(
    local.manage_database ? { PG_PASSWORD = random_password.database[0].result } : {},
    local.generate_secrets ? {
      BIFROST_ENCRYPTION_KEY = random_password.encryption_key[0].result
      BIFROST_SETUP_TOKEN    = random_password.setup_token[0].result
    } : {},
  )

  # Plain environment for the task. Host and port are only known after apply.
  managed_environment = local.manage_database ? [
    { name = "PG_HOST", value = aws_db_instance.bifrost[0].address },
    { name = "PG_PORT", value = tostring(aws_db_instance.bifrost[0].port) },
    { name = "PG_USER", value = var.database_user },
    { name = "PG_DATABASE", value = var.database_name },
    { name = "PG_SSL_MODE", value = "require" },
  ] : []

  # Secret environment for the task: "<secret arn>:<json key>::" selects one
  # key of the JSON secret.
  managed_secrets = concat(
    local.manage_database ? [
      { name = "PG_PASSWORD", valueFrom = "${aws_secretsmanager_secret.runtime[0].arn}:PG_PASSWORD::" },
    ] : [],
    local.generate_secrets ? [
      { name = "BIFROST_ENCRYPTION_KEY", valueFrom = "${aws_secretsmanager_secret.runtime[0].arn}:BIFROST_ENCRYPTION_KEY::" },
      { name = "BIFROST_SETUP_TOKEN", valueFrom = "${aws_secretsmanager_secret.runtime[0].arn}:BIFROST_SETUP_TOKEN::" },
    ] : [],
  )
}

resource "random_password" "database" {
  count = local.manage_database ? 1 : 0

  length  = 32
  special = false
}

resource "random_password" "encryption_key" {
  count = local.generate_secrets ? 1 : 0

  length  = 48
  special = false
}

resource "random_password" "setup_token" {
  count = local.generate_secrets ? 1 : 0

  length  = 40
  special = false
}

resource "aws_secretsmanager_secret" "runtime" {
  count = local.create_runtime_secret ? 1 : 0

  name                    = "${var.name_prefix}/runtime"
  description             = "Bifrost runtime secrets (database password, encryption key, setup token)"
  recovery_window_in_days = var.secret_recovery_window_days

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-runtime"
  })
}

resource "aws_secretsmanager_secret_version" "runtime" {
  count = local.create_runtime_secret ? 1 : 0

  secret_id     = aws_secretsmanager_secret.runtime[0].id
  secret_string = jsonencode(local.runtime_secret_payload)
}

resource "aws_iam_role_policy" "bifrost_runtime_secret" {
  count = local.create_runtime_secret ? 1 : 0

  name = "${var.name_prefix}-runtime-secret-access"
  role = aws_iam_role.ecs_execution[0].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "AllowGetBifrostRuntimeSecret"
        Effect   = "Allow"
        Action   = ["secretsmanager:GetSecretValue"]
        Resource = [aws_secretsmanager_secret.runtime[0].arn]
      }
    ]
  })
}

resource "aws_db_subnet_group" "bifrost" {
  count = local.manage_database ? 1 : 0

  name       = "${var.name_prefix}-db"
  subnet_ids = local.subnet_ids

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-db"
  })
}

resource "aws_security_group" "database" {
  count = local.manage_database ? 1 : 0

  name        = "${var.name_prefix}-db-sg"
  description = "Allow PostgreSQL from the Bifrost ECS service"
  vpc_id      = local.vpc_id

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-db-sg"
  })
}

# One rule per service security group. count (not for_each) because the
# created group's id is unknown until apply, while the list length is known.
resource "aws_vpc_security_group_ingress_rule" "database_from_service" {
  count = local.manage_database ? length(local.security_group_ids) : 0

  security_group_id            = aws_security_group.database[0].id
  description                  = "PostgreSQL from Bifrost service"
  referenced_security_group_id = local.security_group_ids[count.index]
  from_port                    = 5432
  to_port                      = 5432
  ip_protocol                  = "tcp"

  tags = var.tags
}

resource "aws_db_instance" "bifrost" {
  count = local.manage_database ? 1 : 0

  identifier     = "${var.name_prefix}-db"
  engine         = "postgres"
  engine_version = "16"
  instance_class = local.database_instance_class

  allocated_storage = var.database_storage_gb
  storage_type      = "gp3"
  storage_encrypted = true

  db_name  = var.database_name
  username = var.database_user
  password = random_password.database[0].result

  db_subnet_group_name   = aws_db_subnet_group.bifrost[0].name
  vpc_security_group_ids = [aws_security_group.database[0].id]
  publicly_accessible    = false
  multi_az               = false

  backup_retention_period    = 7
  auto_minor_version_upgrade = true
  apply_immediately          = true

  deletion_protection       = var.database_deletion_protection
  skip_final_snapshot       = !var.database_deletion_protection
  final_snapshot_identifier = var.database_deletion_protection ? "${var.name_prefix}-db-final" : null

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-db"
  })
}
