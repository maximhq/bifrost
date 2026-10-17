# =============================================================================
# AWS RDS PostgreSQL
# =============================================================================

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.0"
    }
    random = {
      source  = "hashicorp/random"
      version = ">= 3.0"
    }
  }
}

locals {
  instance_class = coalesce(var.instance_class, "db.t3.micro")
  password       = var.password != null ? var.password : random_password.master[0].result
}

# --- Random password (generated only when var.password is null) ---

resource "random_password" "master" {
  count = var.password == null ? 1 : 0

  length           = 32
  special          = true
  override_special = "!#$%&*()-_=+[]<>:?"
}

# --- DB Subnet Group ---

resource "aws_db_subnet_group" "this" {
  name        = "${var.name_prefix}-pg-subnets"
  description = "Subnets for ${var.name_prefix} PostgreSQL"
  subnet_ids  = var.subnet_ids

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-pg-subnets"
  })
}

# --- Security Group ---

resource "aws_security_group" "postgresql" {
  name        = "${var.name_prefix}-pg-sg"
  description = "Security group for ${var.name_prefix} PostgreSQL"
  vpc_id      = var.vpc_id

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-pg-sg"
  })
}

resource "aws_vpc_security_group_ingress_rule" "postgresql" {
  count = length(var.source_security_group_ids)

  security_group_id            = aws_security_group.postgresql.id
  description                  = "Allow PostgreSQL from Bifrost"
  referenced_security_group_id = var.source_security_group_ids[count.index]
  from_port                    = 5432
  to_port                      = 5432
  ip_protocol                  = "tcp"

  tags = var.tags
}

resource "aws_vpc_security_group_egress_rule" "postgresql" {
  security_group_id = aws_security_group.postgresql.id
  description       = "Allow all outbound traffic"
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"

  tags = var.tags
}

# --- RDS Instance ---

resource "aws_db_instance" "this" {
  identifier = "${var.name_prefix}-pg"

  engine         = "postgres"
  engine_version = var.engine_version
  instance_class = local.instance_class

  allocated_storage = var.allocated_storage
  storage_type      = "gp3"
  storage_encrypted = true

  db_name  = var.database_name
  username = var.username
  password = local.password

  multi_az            = var.multi_az
  publicly_accessible = var.publicly_accessible

  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.postgresql.id]

  backup_retention_period = var.backup_retention_period
  skip_final_snapshot     = true

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-pg"
  })
}
