mock_provider "runpod" {
  mock_resource "runpod_pod" {
    defaults        = { id = "abc123" }
    override_during = plan
  }
}

variables {
  model          = "gpt-oss-20b"
  wait_for_ready = false
}

run "gpt_oss_defaults" {
  command = plan

  assert {
    condition     = local.image == "vllm/vllm-openai:v0.30.0"
    error_message = "unexpected stable image: ${local.image}"
  }
  assert {
    condition     = join(" ", local.server_args) == "openai/gpt-oss-20b --tensor-parallel-size 1 --gpu-memory-utilization 0.92 --max-model-len 131072 --enable-auto-tool-choice --tool-call-parser openai --served-model-name gpt-oss-20b --host 0.0.0.0 --port 8000"
    error_message = "unexpected args: ${join(" ", local.server_args)}"
  }
  assert {
    condition     = local.allowed_cuda_versions == tolist(["13.0"])
    error_message = "unexpected cuda versions"
  }
  assert {
    condition     = runpod_pod.server.gpu_type_ids[0] == "NVIDIA RTX A5000" && runpod_pod.server.gpu_type_priority == "custom"
    error_message = "expected the first 24GB card in the list with custom priority"
  }
  assert {
    condition     = runpod_pod.server.name == "vllm-gpt-oss-20b" && local.bifrost_provider_name == "vllm"
    error_message = "unexpected pod/provider name"
  }
  assert {
    condition     = local.bifrost_provider_config.keys[0].vllm_key_config.model_name == "gpt-oss-20b" && local.bifrost_provider_config.keys[0].models == ["gpt-oss-20b"]
    error_message = "expected a vllm key pinned to the served model"
  }
}

run "teardown_default_wraps_entrypoint" {
  command = plan

  assert {
    condition     = runpod_pod.server.docker_entrypoint[0] == "/bin/bash" && strcontains(runpod_pod.server.docker_entrypoint[2], "exec \"$@\"")
    error_message = "expected teardown wrapper entrypoint"
  }
  assert {
    condition     = slice(runpod_pod.server.docker_start_cmd, 0, 3) == tolist(["vllm", "serve", "openai/gpt-oss-20b"])
    error_message = "expected the wrapper to exec vllm serve"
  }
  assert {
    condition     = local.env["TEARDOWN_AFTER_MINUTES"] == "60" && local.env["TEARDOWN_ACTION"] == "terminate"
    error_message = "expected 60m terminate defaults"
  }
}

run "teardown_disabled_runs_server_directly" {
  command = plan
  variables { teardown_after_minutes = 0 }

  assert {
    condition     = runpod_pod.server.docker_entrypoint == tolist(["vllm", "serve"]) && !contains(keys(local.env), "TEARDOWN_ACTION")
    error_message = "expected no teardown wrapper"
  }
  assert {
    condition     = runpod_pod.server.docker_start_cmd[0] == "openai/gpt-oss-20b"
    error_message = "expected serve args only"
  }
}

run "glm_parsers" {
  command = plan
  variables { model = "glm-4.7-flash" }

  assert {
    condition     = strcontains(join(" ", local.server_args), "--tool-call-parser glm47 --reasoning-parser glm45")
    error_message = "unexpected args: ${join(" ", local.server_args)}"
  }
}

run "kimi_tp2_trust_remote_code" {
  command = plan
  variables { model = "kimi-linear-48b-a3b" }

  assert {
    condition     = strcontains(join(" ", local.server_args), "--tensor-parallel-size 2") && contains(local.server_args, "--trust-remote-code")
    error_message = "unexpected args: ${join(" ", local.server_args)}"
  }
  assert {
    condition     = runpod_pod.server.gpu_count == 2
    error_message = "expected 2 GPUs"
  }
}

run "qwen38_preset_nightly" {
  command = plan
  variables { model = "qwen3.8-27b" }

  assert {
    condition     = local.image == "vllm/vllm-openai:nightly"
    error_message = "unexpected nightly image: ${local.image}"
  }
}

run "nightly_cuda129_override" {
  command = plan
  variables {
    model        = "qwen3.6-35b-a3b"
    channel      = "nightly"
    cuda_version = "12.9"
  }

  assert {
    condition     = local.image == "vllm/vllm-openai:cu129-nightly" && local.allowed_cuda_versions == tolist(["12.9", "13.0"])
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
    image            = "vllm/vllm-openai:nightly-deadbeef"
  }

  assert {
    condition     = join(" ", local.server_args) == "Qwen/Qwen3.6-35B-A3B --tensor-parallel-size 2 --gpu-memory-utilization 0.92 --max-model-len 32768 --enable-auto-tool-choice --tool-call-parser qwen3_coder --kv-cache-dtype fp8 --served-model-name qwen3.6-35b-a3b --host 0.0.0.0 --port 8000"
    error_message = "unexpected args: ${join(" ", local.server_args)}"
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
        hf_model      = "ByteDance-Seed/Seed-OSS-36B-Instruct"
        max_model_len = 32768
        gpu_type_ids  = ["NVIDIA H200"]
        vllm          = { tool_call_parser = "seed_oss" }
      }
    }
  }

  assert {
    condition     = join(" ", local.server_args) == "ByteDance-Seed/Seed-OSS-36B-Instruct --tensor-parallel-size 1 --gpu-memory-utilization 0.92 --max-model-len 32768 --enable-auto-tool-choice --tool-call-parser seed_oss --served-model-name seed-oss-36b --host 0.0.0.0 --port 8000"
    error_message = "unexpected args: ${join(" ", local.server_args)}"
  }
  assert {
    condition     = runpod_pod.server.gpu_type_ids == tolist(["NVIDIA H200"])
    error_message = "gpu override ignored"
  }
}

run "custom_preset_runs_on_sglang_without_engine_block" {
  command = plan
  variables {
    engine = "sglang"
    model  = "seed-oss-36b"
    custom_presets = {
      "seed-oss-36b" = {
        hf_model = "ByteDance-Seed/Seed-OSS-36B-Instruct"
        vllm     = { tool_call_parser = "seed_oss" }
      }
    }
  }

  assert {
    condition     = join(" ", local.server_args) == "--model-path ByteDance-Seed/Seed-OSS-36B-Instruct --tp-size 1 --served-model-name seed-oss-36b --host 0.0.0.0 --port 8000" && local.image == "lmsysorg/sglang:v0.5.21"
    error_message = "unexpected args: ${join(" ", local.server_args)}"
  }
}

run "custom_preset_engine_gpu_override" {
  command = plan
  variables {
    engine = "sglang"
    model  = "seed-oss-36b"
    custom_presets = {
      "seed-oss-36b" = {
        hf_model     = "ByteDance-Seed/Seed-OSS-36B-Instruct"
        gpu_type_ids = ["NVIDIA L40S"]
        vllm         = { gpu_type_ids = ["NVIDIA A100 80GB PCIe"] }
        sglang       = { gpu_type_ids = ["NVIDIA H200"] }
      }
    }
  }

  assert {
    condition     = runpod_pod.server.gpu_type_ids == tolist(["NVIDIA H200"])
    error_message = "expected the sglang block's GPU list, got ${jsonencode(runpod_pod.server.gpu_type_ids)}"
  }
}

run "custom_preset_engine_gpu_override_vllm" {
  command = plan
  variables {
    model = "seed-oss-36b"
    custom_presets = {
      "seed-oss-36b" = {
        hf_model     = "ByteDance-Seed/Seed-OSS-36B-Instruct"
        gpu_type_ids = ["NVIDIA L40S"]
        vllm         = { gpu_type_ids = ["NVIDIA A100 80GB PCIe"] }
        sglang       = { gpu_type_ids = ["NVIDIA H200"] }
      }
    }
  }

  assert {
    condition     = runpod_pod.server.gpu_type_ids == tolist(["NVIDIA A100 80GB PCIe"])
    error_message = "expected the vllm block's GPU list, got ${jsonencode(runpod_pod.server.gpu_type_ids)}"
  }
}

run "gemma_plan" {
  command = plan
  variables { model = "gemma-4-31b-it" }

  assert {
    condition     = strcontains(join(" ", local.server_args), "--tool-call-parser gemma4 --reasoning-parser gemma4 --chat-template /vllm-workspace/examples/tool_chat_template_gemma4.jinja")
    error_message = "unexpected args: ${join(" ", local.server_args)}"
  }
}

run "nemotron_plan" {
  command = plan
  variables { model = "nemotron-3.5-lightning-30b-a3b" }

  assert {
    condition     = local.hf_model == "nvidia/NVIDIA-Nemotron-3.5-Lightning-30B-A3B-NVFP4" && strcontains(join(" ", local.server_args), "--tool-call-parser qwen3_xml --reasoning-parser nemotron_v3 --mamba-backend flashinfer") && strcontains(join(" ", local.server_args), "--moe-backend marlin")
    error_message = "unexpected args: ${join(" ", local.server_args)}"
  }
}

run "unknown_model_fails" {
  command = plan
  variables { model = "nope" }
  expect_failures = [var.model]
}

# --- SGLang ---

run "sglang_gpt_oss" {
  command = plan
  variables { engine = "sglang" }

  assert {
    condition     = local.image == "lmsysorg/sglang:v0.5.21"
    error_message = "unexpected image: ${local.image}"
  }
  assert {
    condition     = join(" ", local.server_args) == "--model-path openai/gpt-oss-20b --tp-size 1 --context-length 131072 --tool-call-parser gpt-oss --reasoning-parser gpt-oss --served-model-name gpt-oss-20b --host 0.0.0.0 --port 8000"
    error_message = "unexpected args: ${join(" ", local.server_args)}"
  }
  assert {
    condition     = slice(nonsensitive(runpod_pod.server.docker_start_cmd), 0, 3) == tolist(["python3", "-m", "sglang.launch_server"]) && contains(nonsensitive(runpod_pod.server.docker_start_cmd), "--api-key")
    error_message = "expected the wrapper to exec sglang.launch_server with --api-key"
  }
  assert {
    condition     = !contains(keys(nonsensitive(local.env)), "VLLM_API_KEY")
    error_message = "VLLM_API_KEY should not be set for sglang"
  }
  assert {
    condition     = runpod_pod.server.name == "sglang-gpt-oss-20b" && local.bifrost_provider_name == "sgl" && nonsensitive(local.bifrost_provider_config.keys[0].sgl_key_config.url) == "https://abc123-8000.proxy.runpod.net"
    error_message = "expected an sgl provider entry"
  }
}

run "sglang_teardown_disabled" {
  command = plan
  variables {
    engine                 = "sglang"
    teardown_after_minutes = 0
  }

  assert {
    condition     = runpod_pod.server.docker_entrypoint == tolist(["python3", "-m", "sglang.launch_server"]) && nonsensitive(runpod_pod.server.docker_start_cmd)[0] == "--model-path"
    error_message = "expected sglang.launch_server as the entrypoint"
  }
}

run "sglang_parsers_differ_from_vllm" {
  command = plan
  variables {
    engine                 = "sglang"
    model                  = "nemotron-3.5-lightning-30b-a3b"
    gpu_memory_utilization = 0.85
  }

  assert {
    condition     = join(" ", local.server_args) == "--model-path nvidia/NVIDIA-Nemotron-3.5-Lightning-30B-A3B-NVFP4 --tp-size 1 --mem-fraction-static 0.85 --context-length 65536 --tool-call-parser qwen3_coder --reasoning-parser nemotron_3 --mamba-ssm-dtype float16 --served-model-name nemotron-3.5-lightning-30b-a3b --host 0.0.0.0 --port 8000"
    error_message = "unexpected args: ${join(" ", local.server_args)}"
  }
}

run "sglang_nightly" {
  command = plan
  variables {
    engine  = "sglang"
    model   = "qwen3.6-35b-a3b"
    channel = "nightly"
  }

  assert {
    condition     = local.image == "lmsysorg/sglang:dev"
    error_message = "unexpected image: ${local.image}"
  }
}

run "sglang_rejects_cuda129" {
  command = plan
  variables {
    engine       = "sglang"
    cuda_version = "12.9"
  }
  expect_failures = [var.cuda_version]
}

# --- Decision models ---

run "decision_clef_flash_vllm" {
  command = plan
  variables { model = "clef-flash" }

  assert {
    condition     = local.kind == "decision" && local.image == "vllm/vllm-openai:nightly"
    error_message = "unexpected kind/image: ${local.kind} ${local.image}"
  }
  assert {
    condition     = join(" ", local.server_args) == "Cloudflare/clef-flash --tensor-parallel-size 1 --gpu-memory-utilization 0.92 --max-model-len 16384 --served-model-name clef-flash --host 0.0.0.0 --port 8000"
    error_message = "unexpected args: ${join(" ", local.server_args)}"
  }
  assert {
    condition     = local.bifrost_provider_name == "vllm" && local.bifrost_provider_config.keys[0].models == ["clef-flash"] && local.bifrost_provider_config.keys[0].vllm_key_config.model_name == "clef-flash"
    error_message = "expected a vllm key pinned to the served model"
  }
}

run "decision_clef_flash_sglang" {
  command = plan
  variables {
    engine = "sglang"
    model  = "clef-flash"
  }

  assert {
    condition     = local.image == "lmsysorg/sglang:dev" && join(" ", local.server_args) == "--model-path Cloudflare/clef-flash --tp-size 1 --context-length 16384 --served-model-name clef-flash --host 0.0.0.0 --port 8000"
    error_message = "unexpected image/args: ${local.image} ${join(" ", local.server_args)}"
  }
  assert {
    condition     = local.bifrost_provider_name == "sgl" && local.bifrost_provider_config.keys[0].models == ["clef-flash"] && nonsensitive(local.bifrost_provider_config.keys[0].sgl_key_config.url) == "https://abc123-8000.proxy.runpod.net"
    error_message = "expected an sgl key pinned to the served model"
  }
}

run "decision_pplx_sglang" {
  command = plan
  variables {
    engine = "sglang"
    model  = "pplx-decider-v1.1-27b"
  }

  assert {
    condition     = join(" ", local.server_args) == "--model-path perplexity-ai/pplx-decider-v1.1-27b --tp-size 1 --served-model-name pplx-decider-v1.1-27b --host 0.0.0.0 --port 8000"
    error_message = "unexpected args: ${join(" ", local.server_args)}"
  }
}

run "decision_d1_vllm" {
  command = plan
  variables { model = "d1-3b" }

  assert {
    condition     = join(" ", local.server_args) == "LiquidAI/d1-3B --tensor-parallel-size 1 --gpu-memory-utilization 0.92 --served-model-name d1-3b --host 0.0.0.0 --port 8000"
    error_message = "unexpected args: ${join(" ", local.server_args)}"
  }
}

run "decision_pplx_rejects_vllm" {
  command = plan
  variables { model = "pplx-decider-v1.1-27b" }
  expect_failures = [var.engine]
}

run "decision_d1_rejects_sglang" {
  command = plan
  variables {
    engine = "sglang"
    model  = "d1-3b"
  }
  expect_failures = [var.engine]
}

run "custom_decision_preset" {
  command = plan
  variables {
    engine = "sglang"
    model  = "my-decider"
    custom_presets = {
      "my-decider" = {
        kind     = "decision"
        hf_model = "Qwen/Qwen3.5-35B-A3B"
      }
    }
  }

  assert {
    condition     = local.kind == "decision" && join(" ", local.server_args) == "--model-path Qwen/Qwen3.5-35B-A3B --tp-size 1 --served-model-name my-decider --host 0.0.0.0 --port 8000"
    error_message = "unexpected kind/args: ${local.kind} ${join(" ", local.server_args)}"
  }
}
# --- Ollama ---

run "custom_decision_preset_vllm_defaults_to_nightly" {
  command = plan
  variables {
    model = "my-decider"
    custom_presets = {
      "my-decider" = {
        kind     = "decision"
        hf_model = "Qwen/Qwen3.5-35B-A3B"
      }
    }
  }

  assert {
    condition     = local.image == "vllm/vllm-openai:nightly"
    error_message = "a custom vLLM decision preset needs a build with /v1/systemone, got ${local.image}"
  }
}

run "custom_decision_preset_vllm_keeps_explicit_channel" {
  command = plan
  variables {
    model = "my-decider"
    custom_presets = {
      "my-decider" = {
        kind     = "decision"
        hf_model = "Qwen/Qwen3.5-35B-A3B"
        vllm     = { channel = "stable" }
      }
    }
  }

  assert {
    condition     = local.image == "vllm/vllm-openai:v0.30.0"
    error_message = "an explicit channel must win, got ${local.image}"
  }
}

run "custom_inference_preset_vllm_stays_stable" {
  command = plan
  variables {
    model = "my-chat"
    custom_presets = {
      "my-chat" = { hf_model = "Qwen/Qwen3.5-35B-A3B" }
    }
  }

  assert {
    condition     = local.image == "vllm/vllm-openai:v0.30.0"
    error_message = "inference presets keep the stable channel, got ${local.image}"
  }
}

run "ollama_gpt_oss" {
  command = plan
  variables { engine = "ollama" }

  assert {
    condition     = local.image == "ollama/ollama:0.40.4" && join(" ", local.server_args) == "gpt-oss:20b gpt-oss-20b"
    error_message = "unexpected image/args: ${local.image} ${join(" ", local.server_args)}"
  }
  assert {
    condition     = runpod_pod.server.docker_entrypoint[0] == "/bin/bash" && slice(nonsensitive(runpod_pod.server.docker_start_cmd), 0, 2) == tolist(["/bin/bash", "-c"]) && strcontains(nonsensitive(runpod_pod.server.docker_start_cmd)[2], "ollama pull") && slice(nonsensitive(runpod_pod.server.docker_start_cmd), 3, 6) == tolist(["ollama-serve", "gpt-oss:20b", "gpt-oss-20b"])
    error_message = "expected the teardown wrapper to exec ollama-serve.sh with tag and served name"
  }
  assert {
    condition     = nonsensitive(local.env)["OLLAMA_KEEP_ALIVE"] == "-1" && nonsensitive(local.env)["OLLAMA_CONTEXT_LENGTH"] == "131072" && nonsensitive(local.env)["OLLAMA_MODELS"] == "/workspace/ollama" && contains(keys(nonsensitive(local.env)), "PROXY_API_KEY") && !contains(keys(nonsensitive(local.env)), "VLLM_API_KEY")
    error_message = "unexpected ollama env"
  }
  assert {
    condition     = local.allowed_cuda_versions == tolist(["12.8", "12.9", "13.0"]) && runpod_pod.server.gpu_type_ids[0] == "NVIDIA RTX A5000"
    error_message = "unexpected cuda versions or GPUs"
  }
  assert {
    condition     = local.bifrost_provider_name == "ollama" && local.bifrost_provider_config.keys[0].models == ["gpt-oss-20b"] && nonsensitive(local.bifrost_provider_config.keys[0].ollama_key_config.url) == "https://abc123-8000.proxy.runpod.net"
    error_message = "expected an ollama key pinned to the served model"
  }
}

run "ollama_teardown_disabled" {
  command = plan
  variables {
    engine                 = "ollama"
    teardown_after_minutes = 0
  }

  assert {
    condition     = runpod_pod.server.docker_entrypoint[3] == "ollama-serve" && nonsensitive(runpod_pod.server.docker_start_cmd) == tolist(["gpt-oss:20b", "gpt-oss-20b"])
    error_message = "expected ollama-serve.sh as the entrypoint"
  }
}

run "ollama_engine_gpu_override" {
  command = plan
  variables {
    engine = "ollama"
    model  = "glm-4.7-flash"
  }

  assert {
    condition     = runpod_pod.server.gpu_type_ids[0] == "NVIDIA RTX PRO 4500 Blackwell" && local.server_args[0] == "glm-4.7-flash:q4_K_M"
    error_message = "expected the ollama block's 32GB GPU list"
  }
}

run "ollama_model_override" {
  command = plan
  variables {
    engine       = "ollama"
    ollama_model = "gpt-oss:20b-q8_0"
  }

  assert {
    condition     = join(" ", local.server_args) == "gpt-oss:20b-q8_0 gpt-oss-20b"
    error_message = "ollama_model override ignored"
  }
}

run "ollama_decision_nimble" {
  command = plan
  variables {
    engine = "ollama"
    model  = "nimble"
  }

  assert {
    condition     = local.kind == "decision" && join(" ", local.server_args) == "nimble:9b nimble" && nonsensitive(local.env)["OLLAMA_CONTEXT_LENGTH"] == "8192"
    error_message = "unexpected nimble config"
  }
}

run "ollama_decision_clef_flash" {
  command = plan
  variables {
    engine = "ollama"
    model  = "clef-flash"
  }

  assert {
    condition     = join(" ", local.server_args) == "clef-flash:9b clef-flash" && runpod_pod.server.gpu_type_ids[0] == "NVIDIA RTX A5000"
    error_message = "unexpected clef-flash config"
  }
}

run "nimble_rejects_vllm" {
  command = plan
  variables { model = "nimble" }
  expect_failures = [var.engine]
}

run "kimi_rejects_ollama" {
  command = plan
  variables {
    engine = "ollama"
    model  = "kimi-linear-48b-a3b"
  }
  expect_failures = [var.engine]
}

run "ollama_custom_preset_needs_tag" {
  command = plan
  variables {
    engine = "ollama"
    model  = "seed-oss-36b"
    custom_presets = {
      "seed-oss-36b" = { hf_model = "ByteDance-Seed/Seed-OSS-36B-Instruct" }
    }
  }
  expect_failures = [terraform_data.pod_spec]
}

run "ollama_rejects_extra_args" {
  command = plan
  variables {
    engine     = "ollama"
    extra_args = ["--foo"]
  }
  expect_failures = [terraform_data.pod_spec]
}

run "api_key_charset" {
  command = plan
  variables { api_key = "has space\"quote" }
  expect_failures = [var.api_key]
}
