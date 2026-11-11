mock_provider "aws" {
  mock_data "aws_caller_identity" {
    defaults = { account_id = "123456789012", arn = "arn:aws:sts::123456789012:assumed-role/dev/me" }
  }
  mock_data "aws_partition" {
    defaults = { partition = "aws" }
  }
}

mock_provider "http" {
  mock_data "http" {
    defaults = { status_code = 200, response_body = "{}" }
  }
}

mock_provider "random" {
  mock_resource "random_id" {
    defaults        = { hex = "a1b2c3" }
    override_during = plan
  }
}

variables {
  wait_for_ready = false
}

run "vllm_qwen3_4b_defaults" {
  command = plan

  assert {
    condition     = local.image == "763104351884.dkr.ecr.us-east-1.amazonaws.com/vllm:0.32.0-gpu-py312-cu130-ubuntu24.04-sagemaker"
    error_message = "unexpected image: ${local.image}"
  }
  assert {
    condition     = local.instance_type == "ml.g6.2xlarge" && local.inference_ami_version == "al2023-ami-sagemaker-inference-gpu-4-1"
    error_message = "expected ml.g6.2xlarge with the CUDA 13 AMI, got ${local.instance_type} / ${coalesce(local.inference_ami_version, "null")}"
  }
  assert {
    condition = local.env_public == {
      SM_VLLM_MODEL                   = "Qwen/Qwen3-4B"
      SM_VLLM_SERVED_MODEL_NAME       = "qwen3-4b"
      SM_VLLM_TENSOR_PARALLEL_SIZE    = "1"
      SAGEMAKER_ENABLE_LOAD_AWARE     = "1"
      SM_VLLM_MAX_MODEL_LEN           = "32768"
      SM_VLLM_ENABLE_AUTO_TOOL_CHOICE = "true"
      SM_VLLM_TOOL_CALL_PARSER        = "hermes"
      SM_VLLM_REASONING_PARSER        = "qwen3"
    }
    error_message = "unexpected env: ${jsonencode(local.env_public)}"
  }
  assert {
    condition     = local.endpoint_name == "bifrost-vllm-qwen3-4b-a1b2c3" && aws_sagemaker_endpoint.this.name == local.endpoint_name
    error_message = "unexpected endpoint name: ${local.endpoint_name}"
  }
  assert {
    condition     = length(aws_sagemaker_model.this.primary_container[0].model_data_source) == 0 && !aws_sagemaker_model.this.enable_network_isolation
    error_message = "vLLM pulls from HF: expected no model data source and no network isolation"
  }
  assert {
    condition     = length(aws_sagemaker_endpoint_configuration.this.production_variants) == 1 && aws_sagemaker_endpoint_configuration.this.production_variants[0].model_name == local.model_name
    error_message = "expected one variant pointing at the model"
  }
  assert {
    condition     = local.api == "openai" && local.kind == "chat" && local.bifrost_model == local.endpoint_name
    error_message = "expected the openai path addressed by endpoint name"
  }
  assert {
    condition     = local.openai_base_url == "https://runtime.sagemaker.us-east-1.amazonaws.com/endpoints/bifrost-vllm-qwen3-4b-a1b2c3/openai/v1"
    error_message = "unexpected base url: ${local.openai_base_url}"
  }
  assert {
    condition     = output.bifrost_provider_config.keys[0].sagemaker_key_config == { region = "us-east-1" } && output.bifrost_provider_config.keys[0].models == ["bifrost-vllm-qwen3-4b-a1b2c3"]
    error_message = "unexpected bifrost key config"
  }
  assert {
    condition     = length(aws_sagemaker_inference_component.this) == 0 && length(aws_scheduler_schedule.delete_inference_component) == 0
    error_message = "no inference component expected"
  }
  assert {
    condition     = can(regex("^at\\(\\d{4}-\\d\\d-\\d\\dT\\d\\d:\\d\\d:\\d\\d\\)$", aws_scheduler_schedule.delete_endpoint[0].schedule_expression)) && jsondecode(aws_scheduler_schedule.delete_endpoint[0].target[0].input).EndpointName == local.endpoint_name
    error_message = "unexpected teardown schedule: ${aws_scheduler_schedule.delete_endpoint[0].schedule_expression}"
  }
  assert {
    condition     = output.cost_per_hr == 1.222
    error_message = "unexpected cost"
  }
  assert {
    condition     = aws_sagemaker_model.this.execution_role_arn == "arn:aws:iam::123456789012:role/bifrost-sagemaker-exec" && aws_scheduler_schedule.delete_endpoint[0].target[0].role_arn == "arn:aws:iam::123456789012:role/bifrost-sagemaker-teardown"
    error_message = "expected the admin-created execution and teardown roles"
  }
}

run "sglang_parsers_and_env" {
  command = plan
  variables { engine = "sglang" }

  assert {
    condition     = local.image == "763104351884.dkr.ecr.us-east-1.amazonaws.com/sglang:0.5.22-gpu-py312-cu130-ubuntu24.04-sagemaker" && local.inference_ami_version == "al2023-ami-sagemaker-inference-gpu-4-1"
    error_message = "unexpected image: ${local.image}"
  }
  assert {
    condition     = local.env_public["SM_SGLANG_MODEL_PATH"] == "Qwen/Qwen3-4B" && local.env_public["SM_SGLANG_TP"] == "1" && local.env_public["SM_SGLANG_CONTEXT_LENGTH"] == "32768" && local.env_public["SM_SGLANG_TOOL_CALL_PARSER"] == "qwen25" && local.env_public["SM_SGLANG_REASONING_PARSER"] == "qwen3"
    error_message = "unexpected env: ${jsonencode(local.env_public)}"
  }
  assert {
    condition     = local.api == "openai"
    error_message = "SGLang DLC serves the openai route"
  }
}

run "lmi_gpt_oss_uses_invocations" {
  command = plan
  variables {
    engine = "lmi"
    model  = "gpt-oss-20b"
  }

  assert {
    condition     = local.image == "763104351884.dkr.ecr.us-east-1.amazonaws.com/djl-inference:0.38.0-lmi30.0.0-cu130" && local.inference_ami_version == "al2023-ami-sagemaker-inference-gpu-4-1"
    error_message = "unexpected image: ${local.image}"
  }
  assert {
    condition = local.env_public == {
      HF_MODEL_ID                    = "openai/gpt-oss-20b"
      OPTION_TENSOR_PARALLEL_DEGREE  = "1"
      OPTION_MAX_MODEL_LEN           = "131072"
      OPTION_ENABLE_AUTO_TOOL_CHOICE = "true"
      OPTION_TOOL_CALL_PARSER        = "openai"
    }
    error_message = "unexpected env: ${jsonencode(local.env_public)}"
  }
  assert {
    condition     = local.api == "invocations" && keys(output.bifrost_provider_config.keys[0].sagemaker_key_config) == ["region"]
    error_message = "LMI is invocations-only and ignores the body model"
  }
}

run "llamacpp_cpu_and_graviton" {
  command = plan
  variables {
    engine = "llamacpp"
    model  = "qwen2.5-0.5b"
  }

  assert {
    condition     = local.image == "763104351884.dkr.ecr.us-east-1.amazonaws.com/llama-cpp:server-sagemaker-cpu-v1" && local.instance_type == "ml.c7i.2xlarge" && local.inference_ami_version == null
    error_message = "unexpected CPU setup: ${local.image} on ${local.instance_type}"
  }
  assert {
    condition     = local.env_public["SM_LLAMA_CPP_HF_REPO"] == "Qwen/Qwen2.5-0.5B-Instruct-GGUF" && local.env_public["SM_LLAMA_CPP_HF_FILE"] == "qwen2.5-0.5b-instruct-q4_0.gguf" && local.env_public["SM_LLAMA_CPP_JINJA"] == "true" && !contains(keys(local.env_public), "SM_LLAMA_CPP_N_GPU_LAYERS")
    error_message = "unexpected env: ${jsonencode(local.env_public)}"
  }
  assert {
    condition     = output.cost_per_hr == 0.428 && local.api == "invocations"
    error_message = "unexpected cost or api"
  }
}

run "llamacpp_graviton_image" {
  command = plan
  variables {
    engine        = "llamacpp"
    model         = "qwen2.5-0.5b"
    instance_type = "ml.c7g.2xlarge"
  }

  assert {
    condition     = local.image == "763104351884.dkr.ecr.us-east-1.amazonaws.com/llama-cpp-arm64:server-sagemaker-cpu-v1"
    error_message = "unexpected image: ${local.image}"
  }
}

run "llamacpp_gpu_offloads" {
  command = plan
  variables {
    engine        = "llamacpp"
    model         = "qwen3-4b"
    instance_type = "ml.g6.xlarge"
  }

  assert {
    condition     = local.image == "763104351884.dkr.ecr.us-east-1.amazonaws.com/llama-cpp:server-sagemaker-cuda-v1" && local.env_public["SM_LLAMA_CPP_N_GPU_LAYERS"] == "999" && local.inference_ami_version == "al2023-ami-sagemaker-inference-gpu-4-1"
    error_message = "unexpected GPU llama.cpp setup: ${local.image}"
  }
}

run "tei_cpu_regional_account" {
  command = plan
  variables {
    engine = "tei"
    model  = "bge-base-en-v1.5"
    region = "us-west-2"
  }

  assert {
    condition     = local.image == "246618743249.dkr.ecr.us-west-2.amazonaws.com/tei-cpu:2.0.1-tei1.9.4-cpu-py310-ubuntu24.04" && local.instance_type == "ml.c6i.2xlarge"
    error_message = "unexpected TEI image: ${local.image}"
  }
  assert {
    condition     = local.kind == "embedding" && local.dialect == "tei" && local.api == "invocations" && local.env_public == { HF_MODEL_ID = "BAAI/bge-base-en-v1.5" }
    error_message = "unexpected TEI wiring: ${local.kind}/${local.dialect}/${local.api} ${jsonencode(local.env_public)}"
  }
}

run "tei_gpu_keeps_default_ami" {
  command = plan
  variables {
    engine        = "tei"
    model         = "bge-base-en-v1.5"
    instance_type = "ml.g6.xlarge"
  }

  assert {
    condition     = local.image == "683313688378.dkr.ecr.us-east-1.amazonaws.com/tei:2.0.1-tei1.9.4-gpu-py310-cu129-ubuntu24.04" && local.inference_ami_version == null
    error_message = "cu129 runs on the default AMI: ${local.image} / ${coalesce(local.inference_ami_version, "null")}"
  }
}

run "vllm_pooling_for_qwen3_embedding" {
  command = plan
  variables { model = "qwen3-embedding-0.6b" }

  assert {
    condition     = local.env_public["SM_VLLM_RUNNER"] == "pooling" && !contains(keys(local.env_public), "SM_VLLM_TOOL_CALL_PARSER") && local.kind == "embedding" && local.api == "invocations"
    error_message = "unexpected env: ${jsonencode(local.env_public)}"
  }
}

run "jumpstart_preset_resolves_spec" {
  command = plan
  variables { engine = "jumpstart" }

  override_data {
    target = data.http.js_spec[0]
    values = { status_code = 200, response_body = file("tests/fixtures/huggingface-reasoning-qwen3-4b.json") }
  }

  assert {
    condition     = length(data.http.js_manifest) == 0 && data.http.js_spec[0].url == "https://jumpstart-cache-prod-us-east-1.s3.us-east-1.amazonaws.com/community_models/huggingface-reasoning-qwen3-4b/specs_v1.42.0.json"
    error_message = "pinned preset version should skip the manifest: ${data.http.js_spec[0].url}"
  }
  assert {
    condition     = local.js_config == "all_purpose" && local.instance_type == "ml.g6.2xlarge"
    error_message = "unexpected config/instance: ${coalesce(local.js_config, "null")} ${local.instance_type}"
  }
  assert {
    condition     = local.image == "763104351884.dkr.ecr.us-east-1.amazonaws.com/djl-inference:0.36.0-lmi18.0.0-cu128" && local.inference_ami_version == null
    error_message = "unexpected image: ${coalesce(local.image, "null")}"
  }
  assert {
    condition     = local.env_public["HF_MODEL_ID"] == "/opt/ml/model" && local.env_public["SAGEMAKER_MODEL_SERVER_WORKERS"] == "1" && local.env_public["OPTION_TOOL_CALL_PARSER"] == "hermes" && local.env_public["OPTION_TENSOR_PARALLEL_DEGREE"] == "1"
    error_message = "expected spec defaults merged with the ml.g6.2xlarge variant env: ${jsonencode(local.env_public)}"
  }
  assert {
    condition     = aws_sagemaker_model.this.primary_container[0].model_data_source[0].s3_data_source[0].s3_uri == "s3://jumpstart-cache-prod-us-east-1/huggingface-reasoning/huggingface-reasoning-qwen3-4b/artifacts/inference-prepack/v2.0.0/" && aws_sagemaker_model.this.primary_container[0].model_data_source[0].s3_data_source[0].s3_data_type == "S3Prefix" && aws_sagemaker_model.this.primary_container[0].model_data_source[0].s3_data_source[0].compression_type == "None"
    error_message = "unexpected weights: ${jsonencode(local.model_data)}"
  }
  assert {
    condition     = aws_sagemaker_model.this.enable_network_isolation && length(aws_sagemaker_model.this.primary_container[0].model_data_source[0].s3_data_source[0].model_access_config) == 0
    error_message = "expected network isolation and no EULA block"
  }
  assert {
    condition     = local.api == "invocations" && local.health_timeout == 3600 && aws_sagemaker_endpoint_configuration.this.production_variants[0].model_data_download_timeout_in_seconds == 3600
    error_message = "expected invocations and the spec's 3600s deploy timeouts"
  }
}

run "jumpstart_any_model_id_uses_config_default_instance" {
  command = plan
  variables {
    engine                  = "jumpstart"
    jumpstart_model_id      = "huggingface-reasoning-qwen3-4b"
    jumpstart_model_version = "1.42.0"
    jumpstart_config        = "lowest_cost"
  }

  override_data {
    target = data.http.js_spec[0]
    values = { status_code = 200, response_body = file("tests/fixtures/huggingface-reasoning-qwen3-4b.json") }
  }

  assert {
    condition     = local.instance_type == "ml.g6.xlarge" && local.endpoint_name == "bifrost-jumpstart-huggingface-reasoning-qwen3-4b-a1b2c3"
    error_message = "lowest_cost config defaults to ml.g6.xlarge: ${local.instance_type} ${local.endpoint_name}"
  }
  assert {
    condition     = local.kind == "chat" && !contains(keys(local.env_public), "SM_VLLM_MODEL")
    error_message = "a raw JumpStart id must not pick up the preset's engine env"
  }
}

run "jumpstart_manifest_lookup_picks_newest" {
  command = plan
  variables {
    engine             = "jumpstart"
    jumpstart_model_id = "huggingface-textembedding-bge-base-en-v1-5"
    instance_type      = "ml.g6.xlarge"
  }

  override_data {
    target = data.http.js_manifest[0]
    values = {
      status_code   = 200
      response_body = "[{\"model_id\":\"huggingface-textembedding-bge-base-en-v1-5\",\"version\":\"1.0.9\"},{\"model_id\":\"huggingface-textembedding-bge-base-en-v1-5\",\"version\":\"1.0.16\"},{\"model_id\":\"other\",\"version\":\"9.9.9\"}]"
    }
  }
  override_data {
    target = data.http.js_spec[0]
    values = { status_code = 200, response_body = file("tests/fixtures/huggingface-textembedding-bge-base-en-v1-5.json") }
  }

  assert {
    condition     = local.js_version == "1.0.16"
    error_message = "expected numeric version ordering, got ${local.js_version}"
  }
  assert {
    condition     = local.image == "683313688378.dkr.ecr.us-east-1.amazonaws.com/tei:2.0.1-tei1.4.0-gpu-py310-cu122-ubuntu22.04" && local.kind == "embedding" && local.dialect == "tei"
    error_message = "unexpected image/kind: ${coalesce(local.image, "null")} ${local.kind}"
  }
}

run "jumpstart_rejects_unsupported_instance" {
  command = plan
  variables {
    engine        = "jumpstart"
    instance_type = "ml.c7i.2xlarge"
  }

  override_data {
    target = data.http.js_spec[0]
    values = { status_code = 200, response_body = file("tests/fixtures/huggingface-reasoning-qwen3-4b.json") }
  }

  expect_failures = [aws_sagemaker_model.this]
}

run "jumpstart_gated_needs_eula" {
  command = plan
  variables { engine = "jumpstart" }

  override_data {
    target = data.http.js_spec[0]
    values = {
      status_code   = 200
      response_body = replace(file("tests/fixtures/huggingface-reasoning-qwen3-4b.json"), "\"hosting_eula_key\": null", "\"hosting_eula_key\": \"fmhMetadata/eula/llama3_2Eula.txt\"")
    }
  }

  expect_failures = [aws_sagemaker_model.this]
}

run "jumpstart_gated_with_eula" {
  command = plan
  variables {
    engine      = "jumpstart"
    accept_eula = true
  }

  override_data {
    target = data.http.js_spec[0]
    values = {
      status_code   = 200
      response_body = replace(replace(file("tests/fixtures/huggingface-reasoning-qwen3-4b.json"), "\"hosting_eula_key\": null", "\"hosting_eula_key\": \"fmhMetadata/eula/llama3_2Eula.txt\""), "\"gated_bucket\": null", "\"gated_bucket\": true")
    }
  }

  assert {
    condition     = startswith(local.model_data.s3_uri, "s3://jumpstart-private-cache-prod-us-east-1/") && aws_sagemaker_model.this.primary_container[0].model_data_source[0].s3_data_source[0].model_access_config[0].accept_eula
    error_message = "gated models read the private bucket with accept_eula"
  }
}

run "inference_component_addressing" {
  command = plan
  variables { inference_components = true }

  assert {
    condition     = aws_sagemaker_endpoint_configuration.this.production_variants[0].model_name == null && aws_sagemaker_endpoint_configuration.this.execution_role_arn != null
    error_message = "IC endpoints carry no model on the variant and need the role on the config"
  }
  assert {
    condition     = aws_sagemaker_inference_component.this[0].name == "bifrost-vllm-qwen3-4b-a1b2c3-ic" && aws_sagemaker_inference_component.this[0].specification[0].compute_resource_requirements[0].number_of_accelerator_devices_required == 1
    error_message = "unexpected inference component"
  }
  assert {
    condition     = local.bifrost_model == "bifrost-vllm-qwen3-4b-a1b2c3/bifrost-vllm-qwen3-4b-a1b2c3-ic" && strcontains(local.openai_base_url, "/inference-components/bifrost-vllm-qwen3-4b-a1b2c3-ic/openai/v1")
    error_message = "unexpected IC addressing: ${local.openai_base_url}"
  }
  assert {
    condition     = timecmp(local.endpoint_teardown_at, local.teardown_at) > 0 && length(aws_scheduler_schedule.delete_inference_component) == 1
    error_message = "the endpoint must be deleted after its inference component"
  }
  assert {
    condition     = aws_sagemaker_endpoint_configuration.this.execution_role_arn == "arn:aws:iam::123456789012:role/bifrost-sagemaker-exec"
    error_message = "inference-component endpoints carry the execution role on the endpoint config"
  }
}

run "second_variant_is_targetable" {
  command = plan
  variables { second_variant = true }

  assert {
    condition     = length(aws_sagemaker_endpoint_configuration.this.production_variants) == 2 && aws_sagemaker_endpoint_configuration.this.production_variants[1].variant_name == "Variant2" && aws_sagemaker_endpoint_configuration.this.production_variants[1].initial_variant_weight == 0
    error_message = "expected a zero-weight Variant2"
  }
  assert {
    condition     = output.cost_per_hr == 2.444
    error_message = "two instances bill double"
  }
}

run "teardown_disabled" {
  command = plan
  variables { teardown_after_minutes = 0 }

  assert {
    condition     = length(aws_scheduler_schedule.delete_endpoint) == 0 && output.teardown_at == "disabled"
    error_message = "expected no teardown resources"
  }
}

run "model_data_url_tarball" {
  command = plan
  variables { model_data_url = "s3://my-weights/qwen/model.tar.gz" }

  assert {
    condition     = local.model_data.s3_data_type == "S3Object" && local.model_data.compression_type == "Gzip" && !contains(keys(local.env_public), "SM_VLLM_MODEL")
    error_message = "a tarball mounts at /opt/ml/model, which the vLLM DLC auto-detects"
  }
}

run "overrides" {
  command = plan
  variables {
    endpoint_name    = "bifrost-my-ep"
    image_tag        = "0.31.0-gpu-py312-cu130-ubuntu24.04-sagemaker"
    max_model_len    = 8192
    reasoning_parser = ""
    extra_env        = { SM_VLLM_ENFORCE_EAGER = "true" }
    hf_token         = "hf_secret"
  }

  assert {
    condition     = local.endpoint_name == "bifrost-my-ep" && endswith(local.image, "/vllm:0.31.0-gpu-py312-cu130-ubuntu24.04-sagemaker")
    error_message = "unexpected name/image"
  }
  assert {
    condition     = local.env_public["SM_VLLM_MAX_MODEL_LEN"] == "8192" && !contains(keys(local.env_public), "SM_VLLM_REASONING_PARSER") && local.env_public["SM_VLLM_ENFORCE_EAGER"] == "true" && local.env["HF_TOKEN"] == "hf_secret"
    error_message = "unexpected env: ${jsonencode(local.env_public)}"
  }
  assert {
    condition     = !contains(keys(output.container_env), "HF_TOKEN")
    error_message = "container_env must not print the HF token"
  }
}

run "custom_engine_needs_image" {
  command = plan
  variables { engine = "custom" }

  expect_failures = [aws_sagemaker_model.this]
}

run "engine_without_preset_block" {
  command = plan
  variables {
    engine = "sglang"
    model  = "bge-base-en-v1.5"
  }

  expect_failures = [var.engine]
}

run "jumpstart_unknown_config_rejected" {
  command = plan
  variables {
    engine                  = "jumpstart"
    jumpstart_model_id      = "huggingface-reasoning-qwen3-4b"
    jumpstart_model_version = "1.42.0"
    jumpstart_config        = "lowst_cost"
    # A supported instance, so only the unknown config can fail the plan.
    instance_type = "ml.g6.xlarge"
  }

  override_data {
    target = data.http.js_spec[0]
    values = { status_code = 200, response_body = file("tests/fixtures/huggingface-reasoning-qwen3-4b.json") }
  }

  expect_failures = [aws_sagemaker_model.this]
}

run "endpoint_name_needs_bifrost_prefix" {
  command = plan
  variables { endpoint_name = "my-ep" }

  expect_failures = [var.endpoint_name]
}

run "endpoint_name_at_most_63_characters" {
  command = plan
  variables { endpoint_name = "bifrost-${join("", [for i in range(56) : "a"])}" }

  expect_failures = [var.endpoint_name]
}

run "long_endpoint_name_keeps_a_hash_in_derived_names" {
  command = plan
  variables {
    endpoint_name          = "bifrost-vllm-a-very-long-model-name-that-fills-it-0123456789"
    inference_components   = true
    instance_type          = "ml.g5.2xlarge"
    teardown_after_minutes = 60
  }

  assert {
    condition = alltrue([for n in [local.model_name, local.endpoint_config_name, local.ic_name, local.schedule_base] :
    strcontains(n, substr(sha1(local.endpoint_name), 0, 6))])
    error_message = "derived names must keep a hash of the full endpoint name"
  }
  assert {
    condition     = length(local.model_name) <= 63 && length(local.ic_name) <= 63 && length("${local.schedule_base}-ic-teardown") <= 64
    error_message = "derived names exceed SageMaker/Scheduler limits"
  }
}

run "execution_role_in_model_hash" {
  command = plan
  variables { execution_role_name = "bifrost-sagemaker-exec-alt" }

  assert {
    condition     = local.model_spec.execution_role == "arn:aws:iam::123456789012:role/bifrost-sagemaker-exec-alt"
    error_message = "the execution role must be part of the model's content hash"
  }
}
run "endpoint_config_hash_covers_download_timeout" {
  command = plan
  variables {
    engine                  = "jumpstart"
    jumpstart_model_id      = "huggingface-reasoning-qwen3-4b"
    jumpstart_model_version = "1.42.0"
    jumpstart_config        = "lowest_cost"
  }

  override_data {
    target = data.http.js_spec[0]
    values = { status_code = 200, response_body = file("tests/fixtures/huggingface-reasoning-qwen3-4b.json") }
  }

  assert {
    condition     = local.endpoint_config_spec.download == 3600 && aws_sagemaker_endpoint_configuration.this.production_variants[0].model_data_download_timeout_in_seconds == 3600
    error_message = "the spec's download timeout must be part of the endpoint config's content hash"
  }
}

run "endpoint_config_hash_covers_ic_execution_role" {
  command = plan
  variables {
    inference_components = true
    instance_type        = "ml.g5.2xlarge"
    execution_role_name  = "bifrost-sagemaker-exec-alt"
  }

  assert {
    condition     = local.endpoint_config_spec.role == "arn:aws:iam::123456789012:role/bifrost-sagemaker-exec-alt"
    error_message = "with inference components the endpoint config carries the execution role, so it must be in the content hash"
  }
}
