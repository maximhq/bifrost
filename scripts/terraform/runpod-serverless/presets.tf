locals {
  builtin_presets = {
    "gpt-oss-20b" = {
      hf_model         = "openai/gpt-oss-20b"
      max_model_len    = 131072
      gpu_count        = 1
      gpu_pools        = ["AMPERE_24", "ADA_24"]
      tool_call_parser = "openai"
      reasoning_parser = ""
      env              = {}
    }

    "qwen3.8-27b" = {
      hf_model         = "Qwen/Qwen3.8-27B-FP8"
      max_model_len    = 65536
      gpu_count        = 1
      gpu_pools        = ["ADA_48_PRO"]
      tool_call_parser = "qwen3_coder"
      reasoning_parser = "qwen3"
      env              = {}
    }
  }
}