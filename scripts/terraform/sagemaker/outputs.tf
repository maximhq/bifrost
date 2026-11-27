output "endpoint_name" {
  value = aws_sagemaker_endpoint.this.name
}

output "inference_component_name" {
  value = local.ic_name
}

output "region" {
  value = var.region
}

output "engine" {
  value = var.engine
}

output "kind" {
  description = "chat or embedding."
  value       = local.kind
}

output "api" {
  description = "Bifrost sagemaker_key_config.api the provider config uses: openai or invocations."
  value       = local.api
}

output "image" {
  value = local.image
}

output "instance_type" {
  value = local.instance_type
}

output "inference_ami_version" {
  value = local.inference_ami_version
}

output "container_env" {
  description = "Container env without HF_TOKEN."
  value       = local.env_public
}

output "jumpstart" {
  description = "Resolved JumpStart model, or null."
  value = local.js_enabled ? {
    model_id    = local.js_model_id
    version     = local.js_version
    config      = local.js_config
    model_data  = local.js_model_data
    eula_url    = local.js_eula_key == null ? null : "${local.js_base_url}/${local.js_eula_key}"
    instances   = local.js_supported_instances
    netisolated = local.js_network_isolation
  } : null
}

output "cost_per_hr" {
  description = "On-demand $/h for all instances (null when the instance type is not in presets.tf's price table)."
  value       = try(local.price_per_hr[local.instance_type] * (var.instance_count + (var.second_variant ? 1 : 0)), null)
}

output "teardown_at" {
  description = "UTC time the scheduler deletes the endpoint (re-armed on every apply)."
  value       = local.endpoint_teardown_at != null ? local.endpoint_teardown_at : "disabled"
}

output "openai_base_url" {
  description = "OpenAI-compatible base URL; authenticate with a bearer token from `probe.py token`."
  value       = local.openai_base_url
}

output "invocations_url" {
  value = local.invocations_url
}

output "execution_role_arn" {
  value = local.execution_role_arn
}

output "probe_command" {
  description = "Full probe of every wire path (openai + invocations, streaming, tools, extra routes, error shapes)."
  value       = "python3 -I ${path.module}/probe.py run ${join(" ", local.probe_args)} --out probe-results/${local.endpoint_name}"
}

output "bifrost_model_id" {
  description = "Model id to send to Bifrost once the sagemaker provider lands."
  value       = "sagemaker/${local.bifrost_model}"
}

output "bifrost_provider_name" {
  value = "sagemaker"
}

output "bifrost_provider_config" {
  description = "providers.sagemaker entry for config.json."
  sensitive   = true
  value = {
    keys = [{
      name                 = local.endpoint_name
      models               = [local.bifrost_model]
      weight               = 1
      sagemaker_key_config = { region = var.region }
    }]
  }
}

output "test_env" {
  description = "Env for `make test-core PROVIDER=sagemaker`."
  value = merge(
    {
      SAGEMAKER_ENABLED = "1"
      SAGEMAKER_REGION  = var.region
    },
    { SAGEMAKER_ENDPOINT_NAME = local.endpoint_name },
    var.inference_components ? { SAGEMAKER_INFERENCE_COMPONENT_NAME = local.ic_name } : {},
  )
}
