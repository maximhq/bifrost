# Model presets. Parser names are the registry keys in vllm/tool_parsers and vllm/reasoning (v0.29.0).
# Sources: github.com/vllm-project/recipes (models/*.yaml) and the HF model cards.
locals {
  # Cheapest-first GPU lists (RunPod secure $/hr as of 2026-09); pods try them in order.
  # MXFP4/NVFP4 run on Ampere+ via Marlin; FP8 checkpoints are kept on Ada or newer for native FP8 kernels.
  gpus_24gb     = ["NVIDIA RTX A5000", "NVIDIA L4", "NVIDIA GeForce RTX 3090", "NVIDIA RTX PRO 4000 Blackwell", "NVIDIA GeForce RTX 4090"] # $0.27-0.74
  gpus_32gb     = ["NVIDIA RTX PRO 4500 Blackwell", "NVIDIA RTX 5000 Ada Generation", "NVIDIA GeForce RTX 5090"]                           # $0.72-0.99
  gpus_48gb_ada = ["NVIDIA L40", "NVIDIA RTX 6000 Ada Generation", "NVIDIA L40S"]                                                          # $0.82-1.09
  gpus_80gb     = ["NVIDIA A100 80GB PCIe", "NVIDIA A100-SXM4-80GB", "NVIDIA H100 PCIe"]                                                   # $1.59-2.89

  builtin_presets = {
    # 21B / 3.6B active, native MXFP4 (~14GB); fits a 24GB card at full context. Reasoning parser (openai_gptoss) is set automatically.
    # Function calling only supports tool_choice="auto".
    "gpt-oss-20b" = {
      hf_model          = "openai/gpt-oss-20b"
      served_model_name = "gpt-oss-20b"
      tool_call_parser  = "openai"
      reasoning_parser  = null
      trust_remote_code = false
      max_model_len     = 131072
      gpu_count         = 1
      gpu_type_ids      = local.gpus_24gb
      vllm_channel      = "stable"
      extra_args        = []
      env               = {}
    }

    # 31B / ~3B active MoE with MLA, BF16 ~62GB so A100 80GB. No official FP8; unsloth/GLM-4.7-Flash-FP8-Dynamic fits 48GB (unverified).
    "glm-4.7-flash" = {
      hf_model          = "zai-org/GLM-4.7-Flash"
      served_model_name = "glm-4.7-flash"
      tool_call_parser  = "glm47"
      reasoning_parser  = "glm45"
      trust_remote_code = false
      max_model_len     = 65536
      gpu_count         = 1
      gpu_type_ids      = local.gpus_80gb
      vllm_channel      = "stable"
      extra_args        = []
      env               = {}
    }

    # 49B / 3B active, linear attention, BF16 ~98GB so 2x A100 80GB. Recipe pins vllm 0.11.2; kimi_k2 parser is
    # inferred from the chat template's tool-call tokens. For 1 GPU: nm-testing/Kimi-Linear-48B-A3B-Instruct-FP8-DYNAMIC.
    "kimi-linear-48b-a3b" = {
      hf_model          = "moonshotai/Kimi-Linear-48B-A3B-Instruct"
      served_model_name = "kimi-linear-48b-a3b"
      tool_call_parser  = "kimi_k2"
      reasoning_parser  = null
      trust_remote_code = true
      max_model_len     = 131072
      gpu_count         = 2
      gpu_type_ids      = local.gpus_80gb
      vllm_channel      = "stable"
      extra_args        = []
      env               = {}
    }

    # 27B dense hybrid-attention + vision, FP8 ~31GB on a 48GB card. Recipe was validated on a pre-release image (vllm/vllm-openai:qwen38),
    # so this defaults to nightly.
    "qwen3.8-27b" = {
      hf_model          = "Qwen/Qwen3.8-27B-FP8"
      served_model_name = "qwen3.8-27b"
      tool_call_parser  = "qwen3_coder"
      reasoning_parser  = "qwen3"
      trust_remote_code = false
      max_model_len     = 65536
      gpu_count         = 1
      gpu_type_ids      = local.gpus_48gb_ada
      vllm_channel      = "nightly"
      extra_args        = []
      env               = {}
    }

    # 36B / 3B active MoE, FP8 ~38GB; tight on 48GB (hybrid attention keeps KV small).
    "qwen3.6-35b-a3b" = {
      hf_model          = "Qwen/Qwen3.6-35B-A3B-FP8"
      served_model_name = "qwen3.6-35b-a3b"
      tool_call_parser  = "qwen3_coder"
      reasoning_parser  = "qwen3"
      trust_remote_code = false
      max_model_len     = 65536
      gpu_count         = 1
      gpu_type_ids      = local.gpus_48gb_ada
      vllm_channel      = "stable"
      extra_args        = []
      env               = {}
    }

    # 30B / 3B active coder MoE, FP8 ~31GB on a 48GB card. No reasoning mode.
    "qwen3-coder-30b-a3b" = {
      hf_model          = "Qwen/Qwen3-Coder-30B-A3B-Instruct-FP8"
      served_model_name = "qwen3-coder-30b-a3b"
      tool_call_parser  = "qwen3_coder"
      reasoning_parser  = null
      trust_remote_code = false
      max_model_len     = 65536
      gpu_count         = 1
      gpu_type_ids      = local.gpus_48gb_ada
      vllm_channel      = "stable"
      extra_args        = []
      env               = {}
    }

    # 30B / 3B active hybrid Mamba-MoE, recipe-default NVFP4 (~18GB) via Marlin. BF16 repo needs 80GB. Needs vllm >= 0.27.1.
    "nemotron-3.5-lightning-30b-a3b" = {
      hf_model          = "nvidia/NVIDIA-Nemotron-3.5-Lightning-30B-A3B-NVFP4"
      served_model_name = "nemotron-3.5-lightning-30b-a3b"
      tool_call_parser  = "qwen3_xml"
      reasoning_parser  = "nemotron_v3"
      trust_remote_code = false
      max_model_len     = 65536
      gpu_count         = 1
      gpu_type_ids      = local.gpus_32gb
      vllm_channel      = "stable"
      extra_args        = ["--mamba-backend", "flashinfer", "--mamba-cache-mode", "align", "--enable-prefix-caching", "--max-num-batched-tokens", "16384", "--kv-cache-dtype", "fp8", "--moe-backend", "marlin"]
      env               = {}
    }

    # 31B dense multimodal, BF16 ~63GB so A100 80GB (recipe lists FP8 for Hopper/Blackwell only). Tool calling needs the chat template shipped in the image.
    "gemma-4-31b-it" = {
      hf_model          = "google/gemma-4-31B-it"
      served_model_name = "gemma-4-31b-it"
      tool_call_parser  = "gemma4"
      reasoning_parser  = "gemma4"
      trust_remote_code = false
      max_model_len     = 32768
      gpu_count         = 1
      gpu_type_ids      = local.gpus_80gb
      vllm_channel      = "stable"
      extra_args        = ["--chat-template", "/vllm-workspace/examples/tool_chat_template_gemma4.jinja"]
      env               = {}
    }
  }
}
