locals {
  presets      = merge(local.builtin_presets, var.custom_presets)
  preset       = local.presets[var.model]
  engine_block = local.js_override ? null : try(local.preset[var.engine], null)

  dns_suffix = startswith(var.region, "cn-") ? "amazonaws.com.cn" : "amazonaws.com"

  kind = coalesce(var.kind, local.js_override ? (can(regex("textembedding|sentencesimilarity|embedding", var.jumpstart_model_id)) ? "embedding" : "chat") : local.preset.kind)

  hf_model          = coalesce(var.hf_model, local.preset.hf_model)
  served_model_name = coalesce(var.served_model_name, try(local.preset.served_model_name, null), var.model)
  max_model_len     = var.max_model_len != null ? var.max_model_len : try(local.preset.max_model_len, null)
  trust_remote_code = try(local.preset.trust_remote_code, false)
  gpu_count         = coalesce(var.gpu_count, try(local.preset.gpu_count, null), 1)
  tool_call_parser  = var.tool_call_parser != null ? var.tool_call_parser : (try(local.engine_block.tool_call_parser, null) != null ? local.engine_block.tool_call_parser : "")
  reasoning_parser  = var.reasoning_parser != null ? var.reasoning_parser : (try(local.engine_block.reasoning_parser, null) != null ? local.engine_block.reasoning_parser : "")

  instance_type = coalesce(
    var.instance_type,
    try(local.engine_block.instance_type, null),
    local.js_enabled ? try(local.js_spec.default_inference_instance_type, null) : null,
    local.preset.instance_type,
  )
  is_gpu      = can(regex("^ml\\.(g|p)[0-9]", local.instance_type))
  is_graviton = can(regex("^ml\\.[a-z]+[0-9]+g[a-z]*\\.", local.instance_type))

  # --- Image ---

  dlc_registry  = "${lookup(local.dlc_account_overrides, var.region, "763104351884")}.dkr.ecr.${var.region}.${local.dns_suffix}"
  tei_registry  = "${lookup(local.tei_accounts, var.region, "unknown")}.dkr.ecr.${var.region}.${local.dns_suffix}"
  llamacpp_repo = local.is_gpu ? "llama-cpp" : (local.is_graviton ? "llama-cpp-arm64" : "llama-cpp")
  default_images = {
    vllm      = "${local.dlc_registry}/vllm:${coalesce(var.image_tag, local.default_image_tags.vllm)}"
    sglang    = "${local.dlc_registry}/sglang:${coalesce(var.image_tag, local.default_image_tags.sglang)}"
    lmi       = "${local.dlc_registry}/djl-inference:${coalesce(var.image_tag, local.default_image_tags.lmi)}"
    llamacpp  = "${local.dlc_registry}/${local.llamacpp_repo}:${coalesce(var.image_tag, local.is_gpu ? local.default_image_tags.llamacpp_gpu : local.default_image_tags.llamacpp_cpu)}"
    tei       = local.is_gpu ? "${local.tei_registry}/tei:${coalesce(var.image_tag, local.default_image_tags.tei_gpu)}" : "${local.tei_registry}/tei-cpu:${coalesce(var.image_tag, local.default_image_tags.tei_cpu)}"
    jumpstart = local.js_image
    custom    = null
  }
  image = var.image != null ? var.image : local.default_images[var.engine]

  # CUDA 13 images need the driver-580 AMI; the default host AMI only covers CUDA 12.x.
  inference_ami_version = var.inference_ami_version != null ? (var.inference_ami_version == "" ? null : var.inference_ami_version) : (
    local.is_gpu && can(regex("cu13|llama-cpp:.*cuda", coalesce(local.image, "-"))) ? "al2023-ami-sagemaker-inference-gpu-4-1" : null
  )

  # --- Weights ---

  # A prefix (trailing /) is mounted uncompressed; anything else is treated as a model.tar.gz.
  user_model_data = var.model_data_url == null ? null : {
    s3_uri           = var.model_data_url
    s3_data_type     = endswith(var.model_data_url, "/") ? "S3Prefix" : "S3Object"
    compression_type = endswith(var.model_data_url, "/") ? "None" : "Gzip"
  }
  model_data  = local.js_enabled ? local.js_model_data : local.user_model_data
  has_weights = local.model_data != null

  # --- Container env (each DLC turns its prefix into server flags: SM_VLLM_MAX_MODEL_LEN -> --max-model-len) ---

  engine_env = {
    vllm = merge(
      local.has_weights ? {} : { SM_VLLM_MODEL = local.hf_model },
      { SM_VLLM_SERVED_MODEL_NAME = local.served_model_name, SM_VLLM_TENSOR_PARALLEL_SIZE = tostring(local.gpu_count), SAGEMAKER_ENABLE_LOAD_AWARE = "1" },
      local.max_model_len != null ? { SM_VLLM_MAX_MODEL_LEN = tostring(local.max_model_len) } : {},
      local.trust_remote_code ? { SM_VLLM_TRUST_REMOTE_CODE = "true" } : {},
      local.tool_call_parser != "" ? { SM_VLLM_ENABLE_AUTO_TOOL_CHOICE = "true", SM_VLLM_TOOL_CALL_PARSER = local.tool_call_parser } : {},
      local.reasoning_parser != "" ? { SM_VLLM_REASONING_PARSER = local.reasoning_parser } : {},
    )
    sglang = merge(
      local.has_weights ? {} : { SM_SGLANG_MODEL_PATH = local.hf_model },
      { SM_SGLANG_SERVED_MODEL_NAME = local.served_model_name, SM_SGLANG_TP = tostring(local.gpu_count) },
      local.max_model_len != null ? { SM_SGLANG_CONTEXT_LENGTH = tostring(local.max_model_len) } : {},
      local.trust_remote_code ? { SM_SGLANG_TRUST_REMOTE_CODE = "true" } : {},
      local.tool_call_parser != "" ? { SM_SGLANG_TOOL_CALL_PARSER = local.tool_call_parser } : {},
      local.reasoning_parser != "" ? { SM_SGLANG_REASONING_PARSER = local.reasoning_parser } : {},
    )
    # LMI >= v17 runs vLLM in async mode; OPTION_<FLAG> maps to the vLLM engine/frontend arg of the same name.
    lmi = merge(
      { HF_MODEL_ID = local.has_weights ? "/opt/ml/model" : local.hf_model, OPTION_TENSOR_PARALLEL_DEGREE = tostring(local.gpu_count) },
      local.max_model_len != null ? { OPTION_MAX_MODEL_LEN = tostring(local.max_model_len) } : {},
      local.trust_remote_code ? { OPTION_TRUST_REMOTE_CODE = "true" } : {},
      local.tool_call_parser != "" ? { OPTION_ENABLE_AUTO_TOOL_CHOICE = "true", OPTION_TOOL_CALL_PARSER = local.tool_call_parser } : {},
      local.reasoning_parser != "" ? { OPTION_REASONING_PARSER = local.reasoning_parser } : {},
    )
    # --jinja enables tool calling; --alias is the model name llama-server reports.
    llamacpp = merge(
      local.has_weights ? {} : { SM_LLAMA_CPP_HF_REPO = try(local.engine_block.hf_repo, ""), SM_LLAMA_CPP_HF_FILE = try(local.engine_block.hf_file, "") },
      { SM_LLAMA_CPP_JINJA = "true", SM_LLAMA_CPP_ALIAS = local.served_model_name },
      local.max_model_len != null ? { SM_LLAMA_CPP_CTX_SIZE = tostring(local.max_model_len) } : {},
      local.is_gpu ? { SM_LLAMA_CPP_N_GPU_LAYERS = "999" } : {},
    )
    tei       = { HF_MODEL_ID = local.has_weights ? "/opt/ml/model" : local.hf_model }
    jumpstart = local.js_env
    custom    = {}
  }

  # env_public is everything but HF_TOKEN, so names and outputs derived from it stay non-sensitive.
  env_public = merge(
    local.engine_env[var.engine],
    local.js_override ? {} : try(local.preset.env, {}),
    try(local.engine_block.env, {}),
    var.extra_env,
  )
  env = merge(local.env_public, var.hf_token != null ? { HF_TOKEN = var.hf_token } : {})

  # --- Endpoint shape ---

  network_isolation = local.js_enabled ? local.js_network_isolation : false
  health_timeout    = min(3600, max(var.health_check_timeout_seconds, coalesce(local.js_health_timeout, 0)))
  download_timeout  = min(3600, coalesce(local.js_download_timeout, 1800))
  ic_min_memory_mb  = coalesce(var.ic_min_memory_mb, try(local.js_resources.min_memory_mb, null), 8192)
  ic_accelerators   = local.is_gpu ? coalesce(try(local.js_resources.num_accelerators, null), local.gpu_count) : null

  name_base     = trim(substr(replace(lower("bifrost-${var.engine}-${local.js_override ? var.jumpstart_model_id : var.model}"), "/[^a-z0-9]+/", "-"), 0, 56), "-")
  endpoint_name = coalesce(var.endpoint_name, "${local.name_base}-${random_id.suffix.hex}")

  name_stem    = length(local.endpoint_name) <= 50 ? local.endpoint_name : "${trim(substr(local.endpoint_name, 0, 43), "-")}-${substr(sha1(local.endpoint_name), 0, 6)}"
  ic_name      = var.inference_components ? "${local.name_stem}-ic" : null
  variant_name = "AllTraffic"

  # Model and config names carry a content hash, so a change creates new ones before the endpoint switches over.
  model_spec = {
    image             = local.image
    execution_role    = local.execution_role_arn
    env               = local.env_public
    hf_token          = var.hf_token != null ? nonsensitive(sha1(var.hf_token)) : null
    model_data        = local.model_data
    accept_eula       = var.accept_eula
    network_isolation = local.network_isolation
  }
  model_name = "${local.name_stem}-${substr(sha1(jsonencode(local.model_spec)), 0, 8)}"
  variants = concat(
    [{ name = local.variant_name, weight = 1, count = var.instance_count }],
    var.second_variant ? [{ name = "Variant2", weight = 0, count = 1 }] : [],
  )
  endpoint_config_spec = {
    model    = local.model_name
    instance = local.instance_type
    ami      = local.inference_ami_version
    variants = local.variants
    ic       = var.inference_components
    volume   = var.volume_size_gb
    routing  = var.routing_strategy
    health   = local.health_timeout
    download = var.inference_components || !local.has_weights ? null : local.download_timeout
    role     = var.inference_components ? local.execution_role_arn : null
  }
  endpoint_config_name = "${local.name_stem}-${substr(sha1(jsonencode(local.endpoint_config_spec)), 0, 8)}"

  # --- Bifrost ---

  is_openai_dlc = can(regex("/(vllm|sglang):", coalesce(local.image, "-")))
  api           = coalesce(var.api, local.kind == "chat" && local.is_openai_dlc ? "openai" : "invocations")
  dialect       = can(regex("/tei(-cpu)?:", coalesce(local.image, "-"))) ? "tei" : "openai"
  bifrost_model = var.inference_components ? "${local.endpoint_name}/${local.ic_name}" : local.endpoint_name
  # Only the vLLM / SGLang servers check the body's model field; everything else ignores it.
  bifrost_model_name = contains(["vllm", "sglang"], var.engine) ? local.served_model_name : null

  runtime_host    = "runtime.sagemaker.${var.region}.${local.dns_suffix}"
  endpoint_path   = "/endpoints/${local.endpoint_name}${var.inference_components ? "/inference-components/${local.ic_name}" : ""}"
  openai_base_url = "https://${local.runtime_host}${local.endpoint_path}/openai/v1"
  invocations_url = "https://${local.runtime_host}/endpoints/${local.endpoint_name}/invocations"
  probe_args = concat(
    ["--region", var.region, "--endpoint", local.endpoint_name],
    var.inference_components ? ["--ic", local.ic_name] : [],
    ["--kind", local.kind, "--dialect", local.dialect, "--expect", local.api],
    local.bifrost_model_name != null ? ["--model", local.bifrost_model_name] : [],
    var.second_variant ? ["--target-variant", "Variant2"] : [],
  )
}

resource "random_id" "suffix" {
  byte_length = 3
}

resource "aws_sagemaker_model" "this" {
  name                     = local.model_name
  execution_role_arn       = local.execution_role_arn
  enable_network_isolation = local.network_isolation

  primary_container {
    image       = local.image
    environment = local.env

    dynamic "model_data_source" {
      for_each = local.model_data == null ? [] : [local.model_data]
      content {
        s3_data_source {
          s3_uri           = model_data_source.value.s3_uri
          s3_data_type     = model_data_source.value.s3_data_type
          compression_type = model_data_source.value.compression_type

          dynamic "model_access_config" {
            for_each = var.accept_eula ? [1] : []
            content {
              accept_eula = true
            }
          }
        }
      }
    }
  }

  # SageMaker validates the execution role at CreateModel; give IAM a moment to propagate.

  lifecycle {
    create_before_destroy = true

    precondition {
      condition     = !local.js_enabled || local.js_config == null || can(local.js_spec_raw.inference_configs[local.js_config])
      error_message = "JumpStart config \"${coalesce(local.js_config, "?")}\" is not in ${coalesce(local.js_model_id, "?")}'s inference_configs (${join(", ", try(keys(local.js_spec_raw.inference_configs), []))})."
    }
    precondition {
      condition     = local.image != null
      error_message = var.engine == "custom" ? "engine = \"custom\" needs image." : "No ${var.engine} image for ${local.instance_type} in ${var.region}: the JumpStart spec has no variant for this instance family. Set instance_type to a supported one, or image."
    }
    precondition {
      condition     = var.engine != "tei" || var.image != null || contains(keys(local.tei_accounts), var.region)
      error_message = "No known TEI registry in ${var.region}; set image."
    }
    precondition {
      condition     = var.engine != "llamacpp" || var.model_data_url != null || try(local.engine_block.hf_repo, "") != ""
      error_message = "engine = \"llamacpp\" needs a GGUF: a preset llamacpp block (hf_repo, hf_file) or model_data_url."
    }
    precondition {
      condition     = !local.js_enabled || local.js_model_data != null
      error_message = "JumpStart spec for ${coalesce(local.js_model_id, "?")} has no hosting artifact key."
    }
    precondition {
      condition     = !local.js_enabled || try(local.js_spec.hosting_use_script_uri, false) != true
      error_message = "JumpStart model ${coalesce(local.js_model_id, "?")} needs a separate inference script; this module only deploys prepacked artifacts."
    }
    precondition {
      condition     = !local.js_enabled || try(local.js_spec.hosting_additional_data_sources, null) == null
      error_message = "JumpStart config ${coalesce(local.js_config, "default")} needs additional model data sources (e.g. speculative-decoding drafts); pick another jumpstart_config."
    }
    precondition {
      condition     = !local.js_enabled || length(local.js_supported_instances) == 0 || contains(local.js_supported_instances, local.instance_type)
      error_message = "JumpStart ${coalesce(local.js_model_id, "?")} does not list ${local.instance_type}. Supported: ${join(", ", local.js_supported_instances)}."
    }
    precondition {
      condition     = !local.js_enabled || local.js_eula_key == null || var.accept_eula
      error_message = "JumpStart ${coalesce(local.js_model_id, "?")} is gated by an EULA (${local.js_base_url}/${coalesce(local.js_eula_key, "-")}); read it and set accept_eula = true."
    }
    precondition {
      condition     = !(var.inference_components && var.second_variant)
      error_message = "inference_components and second_variant cannot be combined."
    }
  }
}

resource "aws_sagemaker_endpoint_configuration" "this" {
  name               = local.endpoint_config_name
  execution_role_arn = local.endpoint_config_spec.role

  dynamic "production_variants" {
    for_each = local.variants
    content {
      variant_name           = production_variants.value.name
      model_name             = var.inference_components ? null : aws_sagemaker_model.this.name
      instance_type          = local.instance_type
      initial_instance_count = production_variants.value.count
      initial_variant_weight = production_variants.value.weight
      inference_ami_version  = local.inference_ami_version
      volume_size_in_gb      = var.volume_size_gb
      # With inference components these move to the component's startup_parameters.
      container_startup_health_check_timeout_in_seconds = var.inference_components ? null : local.health_timeout
      model_data_download_timeout_in_seconds            = local.endpoint_config_spec.download

      routing_config {
        routing_strategy = var.routing_strategy
      }
    }
  }

  lifecycle {
    create_before_destroy = true
  }
}

# Blocks until InService (or fails with the endpoint's FailureReason). Config changes update it in place.
resource "aws_sagemaker_endpoint" "this" {
  name                 = local.endpoint_name
  endpoint_config_name = aws_sagemaker_endpoint_configuration.this.name
}

resource "aws_sagemaker_inference_component" "this" {
  count         = var.inference_components ? 1 : 0
  name          = local.ic_name
  endpoint_name = aws_sagemaker_endpoint.this.name
  variant_name  = local.variant_name

  specification {
    model_name = aws_sagemaker_model.this.name

    compute_resource_requirements {
      min_memory_required_in_mb              = local.ic_min_memory_mb
      number_of_cpu_cores_required           = var.ic_cpu_cores
      number_of_accelerator_devices_required = local.ic_accelerators
    }

    startup_parameters {
      container_startup_health_check_timeout_in_seconds = local.health_timeout
      model_data_download_timeout_in_seconds            = local.has_weights ? local.download_timeout : null
    }
  }

  runtime_config {
    copy_count = 1
  }
}

# One request per wire path through the real SageMaker routing layer, so a broken route fails apply rather than the first test.
resource "terraform_data" "wait_for_ready" {
  count            = var.wait_for_ready ? 1 : 0
  triggers_replace = [aws_sagemaker_endpoint.this.endpoint_config_name, local.model_name, local.ic_name]

  provisioner "local-exec" {
    command = "python3 -I \"$PROBE\" run --quick ${join(" ", [for a in local.probe_args : format("%q", a)])}"
    environment = {
      PROBE = "${path.module}/probe.py"
    }
  }

  depends_on = [aws_sagemaker_inference_component.this]
}