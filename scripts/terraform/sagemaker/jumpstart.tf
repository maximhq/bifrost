locals {
  js_enabled  = var.engine == "jumpstart"
  js_override = var.jumpstart_model_id != null
  js_block    = local.js_override ? null : try(local.preset.jumpstart, null)
  js_model_id = local.js_override ? var.jumpstart_model_id : try(local.js_block.model_id, null)
  js_pinned   = var.jumpstart_model_version != null ? var.jumpstart_model_version : try(local.js_block.version, null)
  js_bucket   = "jumpstart-cache-prod-${var.region}"
  js_base_url = "https://${local.js_bucket}.s3.${var.region}.${local.dns_suffix}"
}

data "http" "js_manifest" {
  count = local.js_enabled && local.js_pinned == null ? 1 : 0
  url   = "${local.js_base_url}/models_manifest.json"

  lifecycle {
    postcondition {
      condition     = self.status_code == 200
      error_message = "Could not read ${local.js_base_url}/models_manifest.json (HTTP ${self.status_code})."
    }
  }
}

locals {
  js_manifest_versions = length(data.http.js_manifest) == 0 ? [] : [
    for e in jsondecode(data.http.js_manifest[0].response_body) : e.version if e.model_id == local.js_model_id
  ]
  # Zero-padded parts sort lexically in numeric order.
  js_sortable_versions = { for v in local.js_manifest_versions : join("", [for p in split(".", v) : format("%08d", try(tonumber(p), 0))]) => v }
  js_version = local.js_pinned != null ? local.js_pinned : (
    length(local.js_sortable_versions) > 0 ? local.js_sortable_versions[reverse(sort(keys(local.js_sortable_versions)))[0]] : null
  )
}

data "http" "js_spec" {
  count = local.js_enabled ? 1 : 0
  url   = "${local.js_base_url}/community_models/${coalesce(local.js_model_id, "unset")}/specs_v${coalesce(local.js_version, "unset")}.json"

  lifecycle {
    precondition {
      condition     = local.js_model_id != null
      error_message = "engine = \"jumpstart\" needs a model id: set jumpstart_model_id, or pick a preset with a jumpstart block."
    }
    precondition {
      condition     = local.js_version != null
      error_message = "JumpStart model ${coalesce(local.js_model_id, "?")} is not in ${local.js_bucket}/models_manifest.json."
    }
    postcondition {
      condition     = self.status_code == 200
      error_message = "JumpStart spec ${self.url} returned HTTP ${self.status_code}; check jumpstart_model_id and jumpstart_model_version."
    }
  }
}

locals {
  js_spec_raw = jsondecode(local.js_enabled ? data.http.js_spec[0].response_body : "{}")

  # Newer specs keep image/instance/artifact data in named inference configs; the SDK overlays the chosen config's components.
  js_config = try(coalesce(
    var.jumpstart_config,
    try(local.js_block.config, null),
    try(local.js_spec_raw.inference_config_rankings.overall.rankings[0], null),
  ), null)
  js_component_names = local.js_config == null ? [] : try(local.js_spec_raw.inference_configs[local.js_config].component_names, [])
  js_spec            = merge(local.js_spec_raw, [for c in local.js_component_names : local.js_spec_raw.inference_config_components[c]]...)

  js_family   = try(split(".", local.instance_type)[1], "")
  js_variants = try(local.js_spec.hosting_instance_type_variants.variants, {})
  js_aliases  = try(local.js_spec.hosting_instance_type_variants.regional_aliases[var.region], {})
  js_image_ref = try(
    local.js_variants[local.instance_type].regional_properties.image_uri,
    local.js_variants[local.js_family].regional_properties.image_uri,
    null,
  )
  js_image = local.js_image_ref == null ? null : (
    startswith(local.js_image_ref, "$") ? try(local.js_aliases[substr(local.js_image_ref, 1, -1)], null) : local.js_image_ref
  )

  js_env = merge(
    { for e in try(local.js_spec.inference_environment_variables, []) : e.name => tostring(e.default) if try(e.scope, "container") == "container" && try(e.default, null) != null },
    try(local.js_variants[local.js_family].properties.environment_variables, {}),
    try(local.js_variants[local.instance_type].properties.environment_variables, {}),
  )

  js_artifact_key = try(coalesce(try(local.js_spec.hosting_prepacked_artifact_key, null), try(local.js_spec.hosting_artifact_key, null)), null)
  js_gated        = try(local.js_spec.gated_bucket, null) == true
  js_eula_key     = try(local.js_spec.hosting_eula_key, null)
  js_model_data = local.js_artifact_key == null ? null : {
    s3_uri           = "s3://${local.js_gated ? "jumpstart-private-cache-prod-${var.region}" : local.js_bucket}/${local.js_artifact_key}"
    s3_data_type     = try(local.js_spec.hosting_artifact_s3_data_type, "S3Prefix")
    compression_type = try(local.js_spec.hosting_artifact_compression_type, "None")
  }
  js_resources = try(
    local.js_variants[local.instance_type].properties.resource_requirements,
    local.js_spec.hosting_resource_requirements,
    null,
  )
  js_network_isolation   = try(local.js_spec.inference_enable_network_isolation, false) == true
  js_health_timeout      = try(local.js_spec.deploy_kwargs.container_startup_health_check_timeout, null)
  js_download_timeout    = try(local.js_spec.deploy_kwargs.model_data_download_timeout, null)
  js_supported_instances = try(local.js_spec.supported_inference_instance_types, [])
}
