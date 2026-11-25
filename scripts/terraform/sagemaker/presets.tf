# Model presets. A preset carries one block per engine it is known to run on; an absent block means unsupported.
# vllm / sglang / lmi blocks: tool_call_parser, reasoning_parser, instance_type, env (all optional).
# llamacpp block: hf_repo + hf_file (a GGUF), instance_type, env. tei block: instance_type, env.
# jumpstart block: model_id, version (pinned so plans don't download the 8 MB manifest), config, instance_type.
# Parser names are the registry keys in vLLM v0.32.0 (vllm/tool_parsers, vllm/reasoning) and SGLang v0.5.22
# (srt/function_call/function_call_parser.py, srt/parser/reasoning_parser.py); LMI 0.38 runs vLLM, so it takes vLLM's names.
locals {
  builtin_presets = {
    # ---------------------------------------------------------------------------------------------------------------
    # Chat models. Small ones first: they start fastest and fit the cheapest instances.
    # ---------------------------------------------------------------------------------------------------------------

    # AWS's own llama.cpp example model; the GGUF is the one its deployment guide uses. Cheapest JumpStart LLM (g5.2xlarge only).
    "qwen2.5-0.5b" = {
      kind              = "chat"
      hf_model          = "Qwen/Qwen2.5-0.5B-Instruct"
      served_model_name = "qwen2.5-0.5b"
      max_model_len     = 32768
      trust_remote_code = false
      gpu_count         = 1
      instance_type     = "ml.g6.xlarge"
      env               = {}
      vllm              = { tool_call_parser = "hermes" }
      sglang            = { tool_call_parser = "qwen25" }
      lmi               = { tool_call_parser = "hermes" }
      llamacpp          = { hf_repo = "Qwen/Qwen2.5-0.5B-Instruct-GGUF", hf_file = "qwen2.5-0.5b-instruct-q4_0.gguf", instance_type = "ml.c7i.2xlarge" }
      jumpstart         = { model_id = "huggingface-llm-qwen2-0-5b-instruct", version = "1.2.12", instance_type = "ml.g5.2xlarge" }
    }

    # Fastest cold start on a GPU. The JumpStart spec only lists ml.g6e.2xlarge and up for it.
    "qwen3-0.6b" = {
      kind              = "chat"
      hf_model          = "Qwen/Qwen3-0.6B"
      served_model_name = "qwen3-0.6b"
      max_model_len     = 32768
      trust_remote_code = false
      gpu_count         = 1
      instance_type     = "ml.g6.xlarge"
      env               = {}
      vllm              = { tool_call_parser = "hermes", reasoning_parser = "qwen3" }
      sglang            = { tool_call_parser = "qwen25", reasoning_parser = "qwen3" }
      lmi               = { tool_call_parser = "hermes", reasoning_parser = "qwen3" }
      llamacpp          = { hf_repo = "Qwen/Qwen3-0.6B-GGUF", hf_file = "Qwen3-0.6B-Q8_0.gguf", instance_type = "ml.c7i.2xlarge" }
      jumpstart         = { model_id = "huggingface-reasoning-qwen3-06b", version = "3.15.0", instance_type = "ml.g6e.2xlarge" }
    }

    # AWS's example in the OpenAI-compatible route guide (vLLM DLC, hermes parser, ml.g6.2xlarge). Default.
    "qwen3-4b" = {
      kind              = "chat"
      hf_model          = "Qwen/Qwen3-4B"
      served_model_name = "qwen3-4b"
      max_model_len     = 32768
      trust_remote_code = false
      gpu_count         = 1
      instance_type     = "ml.g6.2xlarge"
      env               = {}
      vllm              = { tool_call_parser = "hermes", reasoning_parser = "qwen3" }
      sglang            = { tool_call_parser = "qwen25", reasoning_parser = "qwen3" }
      lmi               = { tool_call_parser = "hermes", reasoning_parser = "qwen3" }
      llamacpp          = { hf_repo = "Qwen/Qwen3-4B-GGUF", hf_file = "Qwen3-4B-Q4_K_M.gguf", instance_type = "ml.c7i.2xlarge" }
      jumpstart         = { model_id = "huggingface-reasoning-qwen3-4b", version = "1.42.0", config = "all_purpose", instance_type = "ml.g6.2xlarge" }
    }

    # MXFP4 ~14 GB on a 24 GB L4. vLLM sets its reasoning parser itself; tools only support tool_choice="auto".
    "gpt-oss-20b" = {
      kind              = "chat"
      hf_model          = "openai/gpt-oss-20b"
      served_model_name = "gpt-oss-20b"
      max_model_len     = 131072
      trust_remote_code = false
      gpu_count         = 1
      instance_type     = "ml.g6.2xlarge"
      env               = {}
      vllm              = { tool_call_parser = "openai" }
      sglang            = { tool_call_parser = "gpt-oss", reasoning_parser = "gpt-oss" }
      lmi               = { tool_call_parser = "openai" }
    }

    # FP8 ~31 GB on a 48 GB L40S. No thinking mode.
    "qwen3-coder-30b-a3b" = {
      kind              = "chat"
      hf_model          = "Qwen/Qwen3-Coder-30B-A3B-Instruct-FP8"
      served_model_name = "qwen3-coder-30b-a3b"
      max_model_len     = 65536
      trust_remote_code = false
      gpu_count         = 1
      instance_type     = "ml.g6e.2xlarge"
      env               = {}
      vllm              = { tool_call_parser = "qwen3_coder" }
      sglang            = { tool_call_parser = "qwen3_coder" }
      lmi               = { tool_call_parser = "qwen3_coder" }
    }

    # FP8 ~38 GB on a 48 GB L40S; tight, lower max_model_len if it OOMs at startup.
    "qwen3.6-35b-a3b" = {
      kind              = "chat"
      hf_model          = "Qwen/Qwen3.6-35B-A3B-FP8"
      served_model_name = "qwen3.6-35b-a3b"
      max_model_len     = 65536
      trust_remote_code = false
      gpu_count         = 1
      instance_type     = "ml.g6e.2xlarge"
      env               = {}
      vllm              = { tool_call_parser = "qwen3_coder", reasoning_parser = "qwen3" }
      sglang            = { tool_call_parser = "qwen3_coder", reasoning_parser = "qwen3" }
    }

    # BF16 ~62 GB, so 4x L40S with TP 4 (SageMaker has no single-A100-80GB instance). $13/h.
    "glm-4.7-flash" = {
      kind              = "chat"
      hf_model          = "zai-org/GLM-4.7-Flash"
      served_model_name = "glm-4.7-flash"
      max_model_len     = 65536
      trust_remote_code = false
      gpu_count         = 4
      instance_type     = "ml.g6e.12xlarge"
      env               = {}
      vllm              = { tool_call_parser = "glm47", reasoning_parser = "glm45" }
      sglang            = { tool_call_parser = "glm47", reasoning_parser = "glm45" }
    }

    # ---------------------------------------------------------------------------------------------------------------
    # Embedding models (kind = "embedding"): TEI, vLLM's pooling runner, or JumpStart.
    # ---------------------------------------------------------------------------------------------------------------

    "bge-base-en-v1.5" = {
      kind              = "embedding"
      hf_model          = "BAAI/bge-base-en-v1.5"
      served_model_name = "bge-base-en-v1.5"
      max_model_len     = null
      trust_remote_code = false
      gpu_count         = 1
      instance_type     = "ml.c6i.2xlarge"
      env               = {}
      tei               = {}
      vllm              = { instance_type = "ml.g6.xlarge" }
      jumpstart         = { model_id = "huggingface-textembedding-bge-base-en-v1-5", version = "1.0.16", instance_type = "ml.g6.xlarge" }
    }

    # Qwen3ForCausalLM checkpoint, so vLLM needs the pooling runner. JumpStart serves it on the vLLM DLC.
    "qwen3-embedding-0.6b" = {
      kind              = "embedding"
      hf_model          = "Qwen/Qwen3-Embedding-0.6B"
      served_model_name = "qwen3-embedding-0.6b"
      max_model_len     = 32768
      trust_remote_code = false
      gpu_count         = 1
      instance_type     = "ml.g6.xlarge"
      env               = {}
      vllm              = { env = { SM_VLLM_RUNNER = "pooling" } }
      tei               = { instance_type = "ml.c6i.2xlarge" }
      jumpstart         = { model_id = "huggingface-textembedding-qwen3-embedding-0-6b", version = "1.0.2", instance_type = "ml.g6.xlarge" }
    }
  }

  # ---------------------------------------------------------------------------------------------------------------
  # Images. DLC tags checked against aws.github.io/deep-learning-containers/reference/available_images on 2026-10-23.
  # ---------------------------------------------------------------------------------------------------------------

  # Regions whose DLC registry is not 763104351884 (from JumpStart's regional_aliases).
  dlc_account_overrides = {
    "af-south-1"     = "626614931356"
    "ap-east-1"      = "871362719292"
    "ap-northeast-3" = "364406365360"
    "ap-southeast-3" = "907027046896"
    "ca-west-1"      = "204538143572"
    "cn-north-1"     = "727897471807"
    "cn-northwest-1" = "727897471807"
    "eu-central-2"   = "380420809688"
    "eu-south-1"     = "692866216735"
    "il-central-1"   = "780543022126"
    "me-central-1"   = "914824155844"
    "me-south-1"     = "217643126080"
    "us-gov-east-1"  = "446045086412"
    "us-gov-west-1"  = "442386744353"
  }

  # Hugging Face TEI images live in a different account in every region (from JumpStart's TEI regional_aliases).
  tei_accounts = {
    "af-south-1"     = "510948584623"
    "ap-east-1"      = "651117190479"
    "ap-northeast-1" = "354813040037"
    "ap-northeast-2" = "366743142698"
    "ap-northeast-3" = "867004704886"
    "ap-south-1"     = "720646828776"
    "ap-south-2"     = "628508329040"
    "ap-southeast-1" = "121021644041"
    "ap-southeast-2" = "783357654285"
    "ap-southeast-3" = "951798379941"
    "ap-southeast-4" = "106583098589"
    "ca-central-1"   = "341280168497"
    "ca-west-1"      = "190319476487"
    "cn-north-1"     = "450853457545"
    "cn-northwest-1" = "451049120500"
    "eu-central-1"   = "492215442770"
    "eu-central-2"   = "680994064768"
    "eu-north-1"     = "662702820516"
    "eu-south-1"     = "978288397137"
    "eu-south-2"     = "104374241257"
    "eu-west-1"      = "141502667606"
    "eu-west-2"      = "764974769150"
    "eu-west-3"      = "659782779980"
    "il-central-1"   = "898809789911"
    "me-central-1"   = "272398656194"
    "me-south-1"     = "801668240914"
    "sa-east-1"      = "737474898029"
    "us-east-1"      = "683313688378"
    "us-east-2"      = "257758044811"
    "us-gov-east-1"  = "237065988967"
    "us-gov-west-1"  = "414596584902"
    "us-west-1"      = "746614075791"
    "us-west-2"      = "246618743249"
  }

  default_image_tags = {
    vllm         = "0.32.0-gpu-py312-cu130-ubuntu24.04-sagemaker"
    sglang       = "0.5.22-gpu-py312-cu130-ubuntu24.04-sagemaker"
    lmi          = "0.38.0-lmi30.0.0-cu130"
    llamacpp_cpu = "server-sagemaker-cpu-v1"
    llamacpp_gpu = "server-sagemaker-cuda-v1"
    tei_cpu      = "2.0.1-tei1.9.4-cpu-py310-ubuntu24.04"
    tei_gpu      = "2.0.1-tei1.9.4-gpu-py310-cu129-ubuntu24.04"
  }

  # On-demand hosting $/h, us-east-1 and us-west-2 (identical), AWS price list published 2026-10-22.
  price_per_hr = {
    "ml.g6.xlarge"    = 1.127
    "ml.g6.2xlarge"   = 1.222
    "ml.g5.xlarge"    = 1.408
    "ml.g5.2xlarge"   = 1.515
    "ml.g6e.xlarge"   = 2.605
    "ml.g6e.2xlarge"  = 2.803
    "ml.g6.12xlarge"  = 5.752
    "ml.g5.12xlarge"  = 7.090
    "ml.g6e.12xlarge" = 13.116
    "ml.p4d.24xlarge" = 25.251
    "ml.p5.48xlarge"  = 63.296
    "ml.inf2.xlarge"  = 0.990
    "ml.c7i.2xlarge"  = 0.428
    "ml.c6i.2xlarge"  = 0.408
    "ml.c7g.2xlarge"  = 0.348
    "ml.m5.xlarge"    = 0.230
  }
}
