# Self-teardown that fires even if the machine that ran apply is offline: one-shot EventBridge Scheduler at() schedules call
# DeleteInferenceComponent and then DeleteEndpoint. The deadline is re-armed to now + N on every apply, so re-applying
# extends it (and every plan shows the schedules changing). The schedules run as the admin-created teardown role.
# Models and endpoint configs cost nothing and stay until destroy.
locals {
  teardown_enabled     = var.teardown_after_minutes > 0
  teardown_at          = local.teardown_enabled ? timeadd(plantimestamp(), "${var.teardown_after_minutes}m") : null
  endpoint_teardown_at = local.teardown_enabled ? timeadd(local.teardown_at, var.inference_components ? "5m" : "0m") : null
  schedule_base        = local.name_stem
}

resource "aws_scheduler_schedule" "delete_inference_component" {
  count                   = local.teardown_enabled && var.inference_components ? 1 : 0
  name                    = "${local.schedule_base}-ic-teardown"
  schedule_expression     = "at(${formatdate("YYYY-MM-DD'T'hh:mm:ss", local.teardown_at)})"
  action_after_completion = "DELETE"

  flexible_time_window {
    mode = "OFF"
  }

  target {
    arn      = "arn:${local.partition}:scheduler:::aws-sdk:sagemaker:deleteInferenceComponent"
    role_arn = local.teardown_role_arn
    input    = jsonencode({ InferenceComponentName = local.ic_name })
  }
}

resource "aws_scheduler_schedule" "delete_endpoint" {
  count                   = local.teardown_enabled ? 1 : 0
  name                    = "${local.schedule_base}-teardown"
  schedule_expression     = "at(${formatdate("YYYY-MM-DD'T'hh:mm:ss", local.endpoint_teardown_at)})"
  action_after_completion = "DELETE"

  flexible_time_window {
    mode = "OFF"
  }

  target {
    arn      = "arn:${local.partition}:scheduler:::aws-sdk:sagemaker:deleteEndpoint"
    role_arn = local.teardown_role_arn
    input    = jsonencode({ EndpointName = local.endpoint_name })

    retry_policy {
      maximum_event_age_in_seconds = 3600
      maximum_retry_attempts       = 10
    }
  }
}
