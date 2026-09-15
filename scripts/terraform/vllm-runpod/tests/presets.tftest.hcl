mock_provider "runpod" {
  mock_resource "runpod_pod" {
    defaults = { id = "abc123" }
  }
}

variables {
  wait_for_ready = false
}

run "gpt_oss_defaults" {
  command = plan

  assert {
    condition     = local.image == "vllm/vllm-openai:v0.29.0-cu129"
    error_message = "unexpected stable image: ${local.image}"
  }
  assert {
    condition     = join(" ", local.vllm_args) == "openai/gpt-oss-20b --tensor-parallel-size 1 --gpu-memory-utilization 0.92 --max-model-len 131072 --enable-auto-tool-choice --tool-call-parser openai --served-model-name gpt-oss-20b --host 0.0.0.0 --port 8000"
    error_message = "unexpected args: ${join(" ", local.vllm_args)}"
  }
  assert {
    condition     = local.allowed_cuda_versions == tolist(["12.9", "13.0"])
    error_message = "unexpected cuda versions"
  }
  assert {
    condition     = runpod_pod.vllm.gpu_type_ids[0] == "NVIDIA RTX A5000" && runpod_pod.vllm.gpu_type_priority == "custom"
    error_message = "expected cheapest 24GB card first with custom priority"
  }
}

run "teardown_default_wraps_entrypoint" {
  command = plan

  assert {
    condition     = runpod_pod.vllm.docker_entrypoint[0] == "/bin/bash" && strcontains(runpod_pod.vllm.docker_entrypoint[2], "exec vllm serve")
    error_message = "expected teardown wrapper entrypoint"
  }
  assert {
    condition     = local.env["TEARDOWN_AFTER_MINUTES"] == "60" && local.env["TEARDOWN_ACTION"] == "terminate"
    error_message = "expected 60m terminate defaults"
  }
}

run "teardown_disabled_keeps_image_entrypoint" {
  command = plan
  variables { teardown_after_minutes = 0 }

  assert {
    condition     = runpod_pod.vllm.docker_entrypoint == null && !contains(keys(local.env), "TEARDOWN_ACTION")
    error_message = "expected no teardown wrapper"
  }
}

run "glm_parsers" {
  command = plan
  variables { model = "glm-4.7-flash" }

  assert {
    condition     = strcontains(join(" ", local.vllm_args), "--tool-call-parser glm47 --reasoning-parser glm45")
    error_message = "unexpected args: ${join(" ", local.vllm_args)}"
  }
}

run "kimi_tp2_trust_remote_code" {
  command = plan
  variables { model = "kimi-linear-48b-a3b" }

  assert {
    condition     = strcontains(join(" ", local.vllm_args), "--tensor-parallel-size 2") && contains(local.vllm_args, "--trust-remote-code")
    error_message = "unexpected args: ${join(" ", local.vllm_args)}"
  }
  assert {
    condition     = runpod_pod.vllm.gpu_count == 2
    error_message = "expected 2 GPUs"
  }
}

run "qwen38_preset_nightly" {
  command = plan
  variables { model = "qwen3.8-27b" }

  assert {
    condition     = local.image == "vllm/vllm-openai:cu129-nightly"
    error_message = "unexpected nightly image: ${local.image}"
  }
}

run "nightly_cuda13_override" {
  command = plan
  variables {
    model        = "qwen3.6-35b-a3b"
    vllm_channel = "nightly"
    cuda_version = "13.0"
  }

  assert {
    condition     = local.image == "vllm/vllm-openai:nightly" && local.allowed_cuda_versions == tolist(["13.0"])
    error_message = "unexpected image: ${local.image}"
  }
}

run "overrides_disable_parsers_and_append_args" {
  command = plan
  variables {
    model            = "qwen3.6-35b-a3b"
    hf_model         = "Qwen/Qwen3.6-35B-A3B"
    reasoning_parser = ""
    max_model_len    = 32768
    gpu_count        = 2
    extra_args       = ["--kv-cache-dtype", "fp8"]
    vllm_image       = "vllm/vllm-openai:nightly-deadbeef"
  }

  assert {
    condition     = join(" ", local.vllm_args) == "Qwen/Qwen3.6-35B-A3B --tensor-parallel-size 2 --gpu-memory-utilization 0.92 --max-model-len 32768 --enable-auto-tool-choice --tool-call-parser qwen3_coder --kv-cache-dtype fp8 --served-model-name qwen3.6-35b-a3b --host 0.0.0.0 --port 8000"
    error_message = "unexpected args: ${join(" ", local.vllm_args)}"
  }
  assert {
    condition     = local.image == "vllm/vllm-openai:nightly-deadbeef"
    error_message = "image override ignored"
  }
}

run "custom_preset" {
  command = plan
  variables {
    model = "seed-oss-36b"
    custom_presets = {
      "seed-oss-36b" = {
        hf_model         = "ByteDance-Seed/Seed-OSS-36B-Instruct"
        tool_call_parser = "seed_oss"
        max_model_len    = 32768
        gpu_type_ids     = ["NVIDIA H200"]
      }
    }
  }

  assert {
    condition     = join(" ", local.vllm_args) == "ByteDance-Seed/Seed-OSS-36B-Instruct --tensor-parallel-size 1 --gpu-memory-utilization 0.92 --max-model-len 32768 --enable-auto-tool-choice --tool-call-parser seed_oss --served-model-name seed-oss-36b --host 0.0.0.0 --port 8000"
    error_message = "unexpected args: ${join(" ", local.vllm_args)}"
  }
  assert {
    condition     = runpod_pod.vllm.gpu_type_ids == tolist(["NVIDIA H200"])
    error_message = "gpu override ignored"
  }
}

run "all_builtin_presets_plan" {
  command = plan
  variables { model = "gemma-4-31b-it" }

  assert {
    condition     = strcontains(join(" ", local.vllm_args), "--tool-call-parser gemma4 --reasoning-parser gemma4 --chat-template /vllm-workspace/examples/tool_chat_template_gemma4.jinja")
    error_message = "unexpected args: ${join(" ", local.vllm_args)}"
  }
}

run "nemotron_plan" {
  command = plan
  variables { model = "nemotron-3.5-lightning-30b-a3b" }

  assert {
    condition     = local.hf_model == "nvidia/NVIDIA-Nemotron-3.5-Lightning-30B-A3B-NVFP4" && strcontains(join(" ", local.vllm_args), "--tool-call-parser qwen3_xml --reasoning-parser nemotron_v3 --mamba-backend flashinfer") && strcontains(join(" ", local.vllm_args), "--moe-backend marlin")
    error_message = "unexpected args: ${join(" ", local.vllm_args)}"
  }
}

run "unknown_model_fails" {
  command = plan
  variables { model = "nope" }
  expect_failures = [var.model]
}
