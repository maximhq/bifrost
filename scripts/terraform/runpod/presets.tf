# Model presets. Each preset carries one block per engine it is known to run on; a null (absent) block means unsupported.
# vllm / sglang block fields (all optional): tool_call_parser, reasoning_parser, channel ("stable" | "nightly"), extra_args, gpu_type_ids.
# ollama block fields: model (Ollama library tag, required), gpu_type_ids. Ollama picks tool/reasoning handling per model itself,
# and its default q4_K_M GGUF builds need less VRAM than the BF16/FP8 checkpoints the other engines load.
# vLLM parser names are the registry keys in vllm/tool_parsers and vllm/reasoning (v0.30.0); SGLang's are the keys in
# srt/function_call/function_call_parser.py and srt/parser/reasoning_parser.py (v0.5.21). They differ per engine.
# Sources: github.com/vllm-project/recipes, the SGLang cookbook (docs/cookbook/autoregressive), the HF model cards, and ollama.com/library.
locals {
  # GPU lists ordered by RunPod secure $/hr (as of 2026-09), lowest first; pods try them in order.
  # MXFP4/NVFP4 run on Ampere+ via Marlin; FP8 checkpoints are kept on Ada or newer for native FP8 kernels.
  gpus_24gb     = ["NVIDIA RTX A5000", "NVIDIA L4", "NVIDIA GeForce RTX 3090", "NVIDIA RTX PRO 4000 Blackwell", "NVIDIA GeForce RTX 4090"] # $0.27-0.74
  gpus_32gb     = ["NVIDIA RTX PRO 4500 Blackwell", "NVIDIA RTX 5000 Ada Generation", "NVIDIA GeForce RTX 5090"]                           # $0.72-0.99
  gpus_48gb_ada = ["NVIDIA L40", "NVIDIA RTX 6000 Ada Generation", "NVIDIA L40S"]                                                          # $0.82-1.09
  gpus_80gb     = ["NVIDIA A100 80GB PCIe", "NVIDIA A100-SXM4-80GB", "NVIDIA H100 PCIe"]                                                   # $1.59-2.89

  builtin_presets = {
    # ---------------------------------------------------------------------------------------------------------------
    # Inference models (kind = "inference"): OpenAI-compatible chat/responses, routed through Bifrost's vllm or sgl provider.
    # ---------------------------------------------------------------------------------------------------------------

    # 21B / 3.6B active, native MXFP4 (~14GB); fits a 24GB card at full context. vLLM sets its reasoning parser
    # (openai_gptoss) automatically; SGLang needs it passed. Function calling only supports tool_choice="auto".
    "gpt-oss-20b" = {
      kind              = "inference"
      hf_model          = "openai/gpt-oss-20b"
      served_model_name = "gpt-oss-20b"
      trust_remote_code = false
      max_model_len     = 131072
      gpu_count         = 1
      gpu_type_ids      = local.gpus_24gb
      env               = {}
      vllm              = { tool_call_parser = "openai" }
      sglang            = { tool_call_parser = "gpt-oss", reasoning_parser = "gpt-oss" }
      ollama            = { model = "gpt-oss:20b" }
    }

    # 31B / ~3B active MoE with MLA, BF16 ~62GB so A100 80GB. No official FP8; unsloth/GLM-4.7-Flash-FP8-Dynamic fits 48GB (unverified).
    "glm-4.7-flash" = {
      kind              = "inference"
      hf_model          = "zai-org/GLM-4.7-Flash"
      served_model_name = "glm-4.7-flash"
      trust_remote_code = false
      max_model_len     = 65536
      gpu_count         = 1
      gpu_type_ids      = local.gpus_80gb
      env               = {}
      vllm              = { tool_call_parser = "glm47", reasoning_parser = "glm45" }
      sglang            = { tool_call_parser = "glm47", reasoning_parser = "glm45" }
      ollama            = { model = "glm-4.7-flash:q4_K_M", gpu_type_ids = local.gpus_32gb }
    }

    # 49B / 3B active, linear attention, BF16 ~98GB so 2x A100 80GB. vLLM recipe pins 0.11.2; kimi_k2 parser is
    # inferred from the chat template's tool-call tokens. For 1 GPU: nm-testing/Kimi-Linear-48B-A3B-Instruct-FP8-DYNAMIC.
    "kimi-linear-48b-a3b" = {
      kind              = "inference"
      hf_model          = "moonshotai/Kimi-Linear-48B-A3B-Instruct"
      served_model_name = "kimi-linear-48b-a3b"
      trust_remote_code = true
      max_model_len     = 131072
      gpu_count         = 2
      gpu_type_ids      = local.gpus_80gb
      env               = {}
      vllm              = { tool_call_parser = "kimi_k2" }
      sglang            = { tool_call_parser = "kimi_k2" }
    }

    # 27B dense hybrid-attention + vision, FP8 ~31GB on a 48GB card. The vLLM recipe was validated on a pre-release image
    # (vllm/vllm-openai:qwen38), so vLLM defaults to nightly; SGLang supports it from v0.5.18.
    "qwen3.8-27b" = {
      kind              = "inference"
      hf_model          = "Qwen/Qwen3.8-27B-FP8"
      served_model_name = "qwen3.8-27b"
      trust_remote_code = false
      max_model_len     = 65536
      gpu_count         = 1
      gpu_type_ids      = local.gpus_48gb_ada
      env               = {}
      vllm              = { tool_call_parser = "qwen3_coder", reasoning_parser = "qwen3", channel = "nightly" }
      sglang            = { tool_call_parser = "qwen3_coder", reasoning_parser = "qwen3" }
      ollama            = { model = "qwen3.8:27b-q4_K_M", gpu_type_ids = local.gpus_32gb }
    }

    # 36B / 3B active MoE, FP8 ~38GB; tight on 48GB (hybrid attention keeps KV small).
    "qwen3.6-35b-a3b" = {
      kind              = "inference"
      hf_model          = "Qwen/Qwen3.6-35B-A3B-FP8"
      served_model_name = "qwen3.6-35b-a3b"
      trust_remote_code = false
      max_model_len     = 65536
      gpu_count         = 1
      gpu_type_ids      = local.gpus_48gb_ada
      env               = {}
      vllm              = { tool_call_parser = "qwen3_coder", reasoning_parser = "qwen3" }
      sglang            = { tool_call_parser = "qwen3_coder", reasoning_parser = "qwen3" }
      ollama            = { model = "qwen3.6:35b-a3b-q4_K_M", gpu_type_ids = local.gpus_32gb }
    }

    # 30B / 3B active coder MoE, FP8 ~31GB on a 48GB card. No reasoning mode.
    "qwen3-coder-30b-a3b" = {
      kind              = "inference"
      hf_model          = "Qwen/Qwen3-Coder-30B-A3B-Instruct-FP8"
      served_model_name = "qwen3-coder-30b-a3b"
      trust_remote_code = false
      max_model_len     = 65536
      gpu_count         = 1
      gpu_type_ids      = local.gpus_48gb_ada
      env               = {}
      vllm              = { tool_call_parser = "qwen3_coder" }
      sglang            = { tool_call_parser = "qwen3_coder" }
      ollama            = { model = "qwen3-coder:30b-a3b-q4_K_M", gpu_type_ids = local.gpus_32gb }
    }

    # 30B / 3B active hybrid Mamba-MoE, recipe-default NVFP4 (~18GB) via Marlin. BF16 repo needs 80GB. Needs vLLM >= 0.27.1
    # or SGLang >= 0.5.18. The engines name the parsers differently (vLLM qwen3_xml/nemotron_v3, SGLang qwen3_coder/nemotron_3).
    "nemotron-3.5-lightning-30b-a3b" = {
      kind              = "inference"
      hf_model          = "nvidia/NVIDIA-Nemotron-3.5-Lightning-30B-A3B-NVFP4"
      served_model_name = "nemotron-3.5-lightning-30b-a3b"
      trust_remote_code = false
      max_model_len     = 65536
      gpu_count         = 1
      gpu_type_ids      = local.gpus_32gb
      env               = {}
      vllm = {
        tool_call_parser = "qwen3_xml"
        reasoning_parser = "nemotron_v3"
        extra_args       = ["--mamba-backend", "flashinfer", "--mamba-cache-mode", "align", "--enable-prefix-caching", "--max-num-batched-tokens", "16384", "--kv-cache-dtype", "fp8", "--moe-backend", "marlin"]
      }
      sglang = {
        tool_call_parser = "qwen3_coder"
        reasoning_parser = "nemotron_3"
        extra_args       = ["--mamba-ssm-dtype", "float16"]
      }
      ollama = { model = "nemotron-3.5-lightning:30b-a3b-q4_K_M" }
    }

    # 31B dense multimodal, BF16 ~63GB so A100 80GB (recipe lists FP8 for Hopper/Blackwell only). vLLM's tool calling needs
    # the chat template shipped in its image; SGLang uses the HF template.
    "gemma-4-31b-it" = {
      kind              = "inference"
      hf_model          = "google/gemma-4-31B-it"
      served_model_name = "gemma-4-31b-it"
      trust_remote_code = false
      max_model_len     = 32768
      gpu_count         = 1
      gpu_type_ids      = local.gpus_80gb
      env               = {}
      vllm = {
        tool_call_parser = "gemma4"
        reasoning_parser = "gemma4"
        extra_args       = ["--chat-template", "/vllm-workspace/examples/tool_chat_template_gemma4.jinja"]
      }
      sglang = { tool_call_parser = "gemma4", reasoning_parser = "gemma4" }
      ollama = { model = "gemma4:31b-it-q4_K_M", gpu_type_ids = local.gpus_48gb_ada }
    }

    # ---------------------------------------------------------------------------------------------------------------
    # Decision models (kind = "decision"): served on the engine's native POST /v1/systemone (TypeSafe System One API).
    # No tool or reasoning parsers apply.
    # vLLM ships /v1/systemone only in nightlies (merged after v0.31.0). SGLang has it from v0.5.21, but Clef and
    # PPLX-Decider support landed after that release, so these presets use the dev image there too.
    # ---------------------------------------------------------------------------------------------------------------

    # 9.4B joint-schema classifier, BF16 ~19GB. Prompts cap at 16,384 tokens. On SGLang it must run at TP=1 and
    # disables the radix cache, chunked prefill and CUDA graphs on its own.
    "clef-flash" = {
      kind              = "decision"
      hf_model          = "Cloudflare/clef-flash"
      served_model_name = "clef-flash"
      trust_remote_code = false
      max_model_len     = 16384
      gpu_count         = 1
      gpu_type_ids      = local.gpus_32gb
      env               = {}
      vllm              = { channel = "nightly" }
      sglang            = { channel = "nightly" }
      ollama            = { model = "clef-flash:9b", gpu_type_ids = local.gpus_24gb }
    }

    # 27B Clef, BF16 ~55GB so A100/H100 80GB on SGLang (vLLM support undocumented); Ollama's q4_K_M build is 20GB.
    "clef" = {
      kind              = "decision"
      hf_model          = "Cloudflare/clef"
      served_model_name = "clef"
      trust_remote_code = false
      max_model_len     = 16384
      gpu_count         = 1
      gpu_type_ids      = local.gpus_80gb
      env               = {}
      sglang            = { channel = "nightly" }
      ollama            = { model = "clef:27b-q4_K_M", gpu_type_ids = local.gpus_32gb }
    }

    # 3.1B LFM2-VL decision model (~7GB). vLLM only. Prefix caching only helps when several questions share a state.
    "d1-3b" = {
      kind              = "decision"
      hf_model          = "LiquidAI/d1-3B"
      served_model_name = "d1-3b"
      trust_remote_code = false
      max_model_len     = null
      gpu_count         = 1
      gpu_type_ids      = local.gpus_24gb
      env               = {}
      vllm              = { channel = "nightly" }
    }

    # 27B Qwen3.5-based decider, BF16 ~52GB (validated on H200; A100/H100 80GB is an estimate). SGLang only: vLLM does not
    # map its Qwen3_5Model readout head. v1.1 turns off the radix cache and chunked prefill itself. Trained on prompts
    # up to 8,192 tokens. FP8 moves probabilities by up to 0.2, so this stays BF16.
    "pplx-decider-v1.1-27b" = {
      kind              = "decision"
      hf_model          = "perplexity-ai/pplx-decider-v1.1-27b"
      served_model_name = "pplx-decider-v1.1-27b"
      trust_remote_code = false
      max_model_len     = null
      gpu_count         = 1
      gpu_type_ids      = local.gpus_80gb
      env               = {}
      sglang            = { channel = "nightly" }
    }

    # Bespoke Labs' 9B decision model (Qwen3.5-9B base), Ollama only: the published weights are LoRA adapters trained on
    # their own prompt format, which Ollama's build applies and the vLLM/SGLang native routes don't. 9.5GB; prompts cap at 8,192 tokens.
    "nimble" = {
      kind              = "decision"
      hf_model          = "bespokelabs/Bespoke-Nimble-9B-v2"
      served_model_name = "nimble"
      trust_remote_code = false
      max_model_len     = 8192
      gpu_count         = 1
      gpu_type_ids      = local.gpus_24gb
      env               = {}
      ollama            = { model = "nimble:9b" }
    }

    # Together AI's experimental 4B decision model (~4.5GB), Ollama only for the same prompt-format reason. ~2K context.
    "tev1" = {
      kind              = "decision"
      hf_model          = "togethercomputer/Tev1-4B-experimental"
      served_model_name = "tev1"
      trust_remote_code = false
      max_model_len     = null
      gpu_count         = 1
      gpu_type_ids      = local.gpus_24gb
      env               = {}
      ollama            = { model = "tev1:4b" }
    }
  }
}
