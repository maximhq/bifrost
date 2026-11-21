data "aws_caller_identity" "current" {}

data "aws_partition" "current" {}

# The two roles are created once by an admin (README, "One-time admin setup"), so whoever applies needs no IAM write rights.
locals {
  account_id         = data.aws_caller_identity.current.account_id
  partition          = data.aws_partition.current.partition
  execution_role_arn = "arn:${local.partition}:iam::${local.account_id}:role/${var.execution_role_name}"
  teardown_role_arn  = "arn:${local.partition}:iam::${local.account_id}:role/${var.teardown_role_name}"
}
