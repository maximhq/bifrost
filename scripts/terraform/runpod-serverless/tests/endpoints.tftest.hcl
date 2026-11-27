mock_provider "runpod" {
  mock_resource "runpod_network_volume" {
    defaults        = { id = "vol123" }
    override_during = plan
  }
  mock_resource "runpod_serverless_endpoint" {
    defaults        = { id = "ep123" }
    override_during = plan
  }
}

variables {
  warm_up = false
}

run "defaults" {
  command = plan

  assert {
    condition     = toset(keys(runpod_serverless_endpoint.model)) == toset(["gpt-oss-20b", "qwen3.8-27b"])
    error_message = "expected one endpoint per default model"
  }
  assert {
    condition     = runpod_serverless_endpoint.model["gpt-oss-20b"].env["TOOL_CALL_PARSER"] == "openai" && !contains(keys(runpod_serverless_endpoint.model["gpt-oss-20b"].env), "REASONING_PARSER")
    error_message = "gpt-oss should set the openai tool parser and leave the reasoning parser to vLLM"
  }
  assert {
    condition     = runpod_serverless_endpoint.model["qwen3.8-27b"].env["REASONING_PARSER"] == "qwen3" && runpod_serverless_endpoint.model["qwen3.8-27b"].env["MAX_MODEL_LEN"] == "65536"
    error_message = "unexpected qwen3.8 env"
  }
  assert {
    condition     = runpod_serverless_endpoint.model["qwen3.8-27b"].gpu_pools == tolist(["ADA_48_PRO"])
    error_message = "FP8 qwen should stay on Ada"
  }
  assert {
    condition     = runpod_serverless_endpoint.model["gpt-oss-20b"].workers_min == 0 && runpod_serverless_endpoint.model["gpt-oss-20b"].data_center_ids == tolist(["US-KS-2"])
    error_message = "endpoints must scale to zero and stay in the volume's data center"
  }
  assert {
    condition     = local.base_urls["gpt-oss-20b"] == "https://api.runpod.ai/v2/ep123/openai"
    error_message = "unexpected base url: ${local.base_urls["gpt-oss-20b"]}"
  }
  assert {
    condition     = local.bifrost_provider_config.keys[0].value == "env.RUNPOD_API_KEY" && local.bifrost_provider_config.keys[0].vllm_key_config.model_name == "gpt-oss-20b"
    error_message = "unexpected bifrost key"
  }
}

run "subset_and_overrides" {
  command = plan

  variables {
    models    = ["gpt-oss-20b"]
    extra_env = { "gpt-oss-20b" = { MAX_MODEL_LEN = "32768" } }
  }

  assert {
    condition     = length(runpod_serverless_endpoint.model) == 1 && runpod_serverless_endpoint.model["gpt-oss-20b"].env["MAX_MODEL_LEN"] == "32768"
    error_message = "extra_env should override the preset"
  }
}

run "unknown_model" {
  command = plan

  variables {
    models = ["kimi-k2"]
  }

  expect_failures = [var.models]
}
