#!/usr/bin/env bash
#
# seed_models.sh - seed the comprehensive model fixture into a running LiteLLM
# proxy via the management API (POST /model/new), instead of via config.yaml.
#
# Models added via /model/new are stored in the DB (db_model: true) and are
# therefore deletable via delete_entities.sh in this directory.
#
# Usage:
#   LITELLM_URL=http://localhost:4000 LITELLM_MASTER_KEY=sk-1234 ./seed_models.sh
#
#   DRY_RUN=1 ./seed_models.sh    # print payloads, POST nothing
#
# The proxy must run with `general_settings.store_model_in_db: True` and a DB
# connection, otherwise /model/new returns 500.
#
# Requires: curl, jq.

set -euo pipefail

LITELLM_URL="${LITELLM_URL:-http://localhost:4000}"
LITELLM_MASTER_KEY="${LITELLM_MASTER_KEY:-sk-1234}"
DRY_RUN="${DRY_RUN:-0}"

added=0
failed=0

# add_model posts a single Deployment to /model/new. Args:
#   $1 = human-readable description (logged only)
#   $2 = JSON body ({model_name, litellm_params, model_info?})
# In DRY_RUN mode it pretty-prints the body instead of sending it.
add_model() {
  local desc="$1" body="$2"

  if ! jq -e . >/dev/null 2>&1 <<<"${body}"; then
    echo "  SKIP ${desc}: invalid JSON payload"
    failed=$((failed + 1))
    return
  fi

  if [[ "${DRY_RUN}" == "1" ]]; then
    echo "  DRY-RUN ${desc}:"
    jq -c . <<<"${body}"
    added=$((added + 1))
    return
  fi

  if curl -sS --fail-with-body \
      --request POST "${LITELLM_URL}/model/new" \
      --header "Authorization: Bearer ${LITELLM_MASTER_KEY}" \
      --header "Content-Type: application/json" \
      --data "${body}" >/dev/null; then
    echo "  OK   ${desc}"
    added=$((added + 1))
  else
    echo "  FAIL ${desc}"
    failed=$((failed + 1))
  fi
}

# -----------------------------------------------------------------------------
# CASE 1 — Simple chat/completion, one representative model per provider.
# -----------------------------------------------------------------------------
echo "==> Case 1: simple chat per provider"
add_model "openai chat"      '{"model_name":"openai-gpt-4o","litellm_params":{"model":"openai/gpt-4o","api_key":"os.environ/OPENAI_API_KEY"}}'
add_model "anthropic chat"   '{"model_name":"anthropic-claude","litellm_params":{"model":"anthropic/claude-sonnet-4-20250514","api_key":"os.environ/ANTHROPIC_API_KEY"}}'
add_model "gemini chat"      '{"model_name":"gemini-flash","litellm_params":{"model":"gemini/gemini-2.5-flash","api_key":"os.environ/GEMINI_API_KEY"}}'
add_model "xai chat"         '{"model_name":"xai-grok","litellm_params":{"model":"xai/grok-3","api_key":"os.environ/XAI_API_KEY"}}'
add_model "zai chat"         '{"model_name":"zai-glm","litellm_params":{"model":"zai/glm-4.6","api_key":"os.environ/ZAI_API_KEY"}}'
add_model "mistral chat"     '{"model_name":"mistral-large","litellm_params":{"model":"mistral/mistral-large-latest","api_key":"os.environ/MISTRAL_API_KEY"}}'
add_model "codestral chat"   '{"model_name":"codestral","litellm_params":{"model":"codestral/codestral-latest","api_key":"os.environ/CODESTRAL_API_KEY"}}'
add_model "deepseek chat"    '{"model_name":"deepseek-chat","litellm_params":{"model":"deepseek/deepseek-chat","api_key":"os.environ/DEEPSEEK_API_KEY"}}'
add_model "groq chat"        '{"model_name":"groq-llama","litellm_params":{"model":"groq/llama-3.3-70b-versatile","api_key":"os.environ/GROQ_API_KEY"}}'
add_model "cerebras chat"    '{"model_name":"cerebras-llama","litellm_params":{"model":"cerebras/llama-3.3-70b","api_key":"os.environ/CEREBRAS_API_KEY"}}'
add_model "cohere chat"      '{"model_name":"cohere-command","litellm_params":{"model":"cohere_chat/command-r-plus","api_key":"os.environ/COHERE_API_KEY"}}'
add_model "together chat"    '{"model_name":"together-llama","litellm_params":{"model":"together_ai/meta-llama/Llama-3.3-70B-Instruct-Turbo","api_key":"os.environ/TOGETHERAI_API_KEY"}}'
add_model "fireworks chat"   '{"model_name":"fireworks-llama","litellm_params":{"model":"fireworks_ai/accounts/fireworks/models/llama-v3p3-70b-instruct","api_key":"os.environ/FIREWORKS_AI_API_KEY"}}'
add_model "openrouter chat"  '{"model_name":"openrouter-gpt","litellm_params":{"model":"openrouter/openai/gpt-4o","api_key":"os.environ/OPENROUTER_API_KEY"}}'
add_model "perplexity chat"  '{"model_name":"perplexity-sonar","litellm_params":{"model":"perplexity/sonar","api_key":"os.environ/PERPLEXITYAI_API_KEY"}}'
add_model "deepinfra chat"   '{"model_name":"deepinfra-llama","litellm_params":{"model":"deepinfra/meta-llama/Meta-Llama-3.1-70B-Instruct","api_key":"os.environ/DEEPINFRA_API_KEY"}}'
add_model "sambanova chat"   '{"model_name":"sambanova-llama","litellm_params":{"model":"sambanova/Meta-Llama-3.3-70B-Instruct","api_key":"os.environ/SAMBANOVA_API_KEY"}}'
add_model "nvidia nim chat"  '{"model_name":"nvidia-nim-llama","litellm_params":{"model":"nvidia_nim/meta/llama-3.1-70b-instruct","api_key":"os.environ/NVIDIA_NIM_API_KEY"}}'
add_model "ai21 chat"        '{"model_name":"ai21-jamba","litellm_params":{"model":"ai21_chat/jamba-1.5-large","api_key":"os.environ/AI21_API_KEY"}}'
add_model "replicate chat"   '{"model_name":"replicate-llama","litellm_params":{"model":"replicate/meta/meta-llama-3-70b-instruct","api_key":"os.environ/REPLICATE_API_KEY"}}'
add_model "huggingface chat" '{"model_name":"huggingface-llama","litellm_params":{"model":"huggingface/meta-llama/Llama-3.1-8B-Instruct","api_key":"os.environ/HUGGINGFACE_API_KEY"}}'
add_model "databricks chat"  '{"model_name":"databricks-dbrx","litellm_params":{"model":"databricks/databricks-dbrx-instruct","api_key":"os.environ/DATABRICKS_API_KEY","api_base":"os.environ/DATABRICKS_API_BASE"}}'
add_model "predibase chat"   '{"model_name":"predibase-llama","litellm_params":{"model":"predibase/llama-3-8b-instruct","api_key":"os.environ/PREDIBASE_API_KEY","tenant_id":"os.environ/PREDIBASE_TENANT_ID"}}'
add_model "nscale chat"      '{"model_name":"nscale-llama","litellm_params":{"model":"nscale/meta-llama/Llama-3.3-70B-Instruct","api_key":"os.environ/NSCALE_API_KEY"}}'
add_model "novita chat"      '{"model_name":"novita-llama","litellm_params":{"model":"novita/meta-llama/llama-3.1-70b-instruct","api_key":"os.environ/NOVITA_API_KEY"}}'
add_model "friendliai chat"  '{"model_name":"friendliai-llama","litellm_params":{"model":"friendliai/meta-llama-3.1-70b-instruct","api_key":"os.environ/FRIENDLI_TOKEN"}}'
add_model "featherless chat" '{"model_name":"featherless-llama","litellm_params":{"model":"featherless_ai/meta-llama/Meta-Llama-3.1-8B-Instruct","api_key":"os.environ/FEATHERLESS_AI_API_KEY"}}'
add_model "github chat"      '{"model_name":"github-gpt","litellm_params":{"model":"github/gpt-4o","api_key":"os.environ/GITHUB_API_KEY"}}'
add_model "github copilot"   '{"model_name":"github-copilot-gpt","litellm_params":{"model":"github_copilot/gpt-4o"}}'
add_model "cloudflare chat"  '{"model_name":"cloudflare-llama","litellm_params":{"model":"cloudflare/@cf/meta/llama-3.1-8b-instruct","api_key":"os.environ/CLOUDFLARE_API_KEY","api_base":"os.environ/CLOUDFLARE_API_BASE"}}'
add_model "gigachat chat"    '{"model_name":"gigachat-pro","litellm_params":{"model":"gigachat/GigaChat-Pro","api_key":"os.environ/GIGACHAT_API_KEY"}}'
add_model "moonshot chat"    '{"model_name":"moonshot-v1","litellm_params":{"model":"moonshot/moonshot-v1-8k","api_key":"os.environ/MOONSHOT_API_KEY"}}'
add_model "dashscope chat"   '{"model_name":"dashscope-qwen","litellm_params":{"model":"dashscope/qwen-max","api_key":"os.environ/DASHSCOPE_API_KEY"}}'
add_model "volcengine chat"  '{"model_name":"volcengine-doubao","litellm_params":{"model":"volcengine/doubao-pro-4k","api_key":"os.environ/VOLCENGINE_API_KEY"}}'
add_model "nebius chat"      '{"model_name":"nebius-llama","litellm_params":{"model":"nebius/meta-llama/Meta-Llama-3.1-70B-Instruct","api_key":"os.environ/NEBIUS_API_KEY"}}'
add_model "hyperbolic chat"  '{"model_name":"hyperbolic-llama","litellm_params":{"model":"hyperbolic/meta-llama/Meta-Llama-3.1-70B-Instruct","api_key":"os.environ/HYPERBOLIC_API_KEY"}}'
add_model "lambda chat"      '{"model_name":"lambda-llama","litellm_params":{"model":"lambda_ai/llama3.1-70b-instruct-fp8","api_key":"os.environ/LAMBDA_API_KEY"}}'
add_model "v0 chat"          '{"model_name":"v0-md","litellm_params":{"model":"v0/v0-1.5-md","api_key":"os.environ/V0_API_KEY"}}'
add_model "morph chat"       '{"model_name":"morph-large","litellm_params":{"model":"morph/morph-v3-large","api_key":"os.environ/MORPH_API_KEY"}}'
add_model "inception chat"   '{"model_name":"inception-mercury","litellm_params":{"model":"inception/mercury","api_key":"os.environ/INCEPTION_API_KEY"}}'
add_model "maritalk chat"    '{"model_name":"maritalk-sabia","litellm_params":{"model":"maritalk/sabia-3","api_key":"os.environ/MARITALK_API_KEY"}}'
add_model "meta llama api"   '{"model_name":"meta-llama-api","litellm_params":{"model":"meta_llama/Llama-3.3-70B-Instruct","api_key":"os.environ/LLAMA_API_KEY"}}'
add_model "galadriel chat"   '{"model_name":"galadriel-llama","litellm_params":{"model":"galadriel/llama3.1","api_key":"os.environ/GALADRIEL_API_KEY"}}'
add_model "aiml chat"        '{"model_name":"aiml-gpt","litellm_params":{"model":"aiml/gpt-4o","api_key":"os.environ/AIML_API_KEY"}}'
add_model "cometapi chat"    '{"model_name":"cometapi-gpt","litellm_params":{"model":"cometapi/gpt-4o","api_key":"os.environ/COMETAPI_KEY"}}'
add_model "vercel gateway"   '{"model_name":"vercel-gateway-gpt","litellm_params":{"model":"vercel_ai_gateway/openai/gpt-4o","api_key":"os.environ/VERCEL_AI_GATEWAY_API_KEY"}}'
add_model "nano-gpt chat"    '{"model_name":"nano-gpt","litellm_params":{"model":"nano-gpt/gpt-4o","api_key":"os.environ/NANO_GPT_API_KEY"}}'
add_model "chutes chat"      '{"model_name":"chutes-llama","litellm_params":{"model":"chutes/meta-llama/Llama-3.3-70B-Instruct","api_key":"os.environ/CHUTES_API_KEY"}}'
add_model "minimax chat"     '{"model_name":"minimax-text","litellm_params":{"model":"minimax/MiniMax-Text-01","api_key":"os.environ/MINIMAX_API_KEY"}}'
add_model "baseten chat"     '{"model_name":"baseten-llama","litellm_params":{"model":"baseten/llama-3-70b-instruct","api_key":"os.environ/BASETEN_API_KEY"}}'

# -----------------------------------------------------------------------------
# CASE 2 — Self-hosted / OpenAI-compatible endpoints (require api_base).
# -----------------------------------------------------------------------------
echo "==> Case 2: self-hosted / OpenAI-compatible"
add_model "ollama"           '{"model_name":"ollama-llama","litellm_params":{"model":"ollama_chat/llama3.1","api_base":"os.environ/OLLAMA_API_BASE"}}'
add_model "hosted vllm"      '{"model_name":"vllm-llama","litellm_params":{"model":"hosted_vllm/meta-llama/Llama-3.1-8B-Instruct","api_base":"os.environ/HOSTED_VLLM_API_BASE"}}'
add_model "lm studio"        '{"model_name":"lmstudio-llama","litellm_params":{"model":"lm_studio/llama-3.1-8b-instruct","api_base":"os.environ/LM_STUDIO_API_BASE"}}'
add_model "llamafile"        '{"model_name":"llamafile-local","litellm_params":{"model":"llamafile/local-model","api_base":"os.environ/LLAMAFILE_API_BASE"}}'
add_model "triton"           '{"model_name":"triton-model","litellm_params":{"model":"triton/ensemble","api_base":"os.environ/TRITON_API_BASE"}}'
add_model "xinference"       '{"model_name":"xinference-llama","litellm_params":{"model":"xinference/llama-3-instruct","api_base":"os.environ/XINFERENCE_API_BASE"}}'
add_model "openai-compat"    '{"model_name":"openai-compatible-custom","litellm_params":{"model":"openai/my-self-hosted-model","api_key":"os.environ/CUSTOM_LLM_API_KEY","api_base":"os.environ/CUSTOM_LLM_API_BASE"}}'

# -----------------------------------------------------------------------------
# CASE 3 — Provider-specific credential shapes.
# -----------------------------------------------------------------------------
echo "==> Case 3: provider-specific credentials"
add_model "vertex ai"        '{"model_name":"vertex-gemini","litellm_params":{"model":"vertex_ai/gemini-2.5-pro","vertex_project":"os.environ/VERTEX_PROJECT","vertex_location":"os.environ/VERTEX_LOCATION","vertex_credentials":"os.environ/VERTEX_CREDENTIALS"}}'
add_model "bedrock keys"     '{"model_name":"bedrock-claude","litellm_params":{"model":"bedrock/anthropic.claude-3-5-sonnet-20241022-v2:0","aws_access_key_id":"os.environ/AWS_ACCESS_KEY_ID","aws_secret_access_key":"os.environ/AWS_SECRET_ACCESS_KEY","aws_region_name":"os.environ/AWS_REGION_NAME"}}'
add_model "bedrock role"     '{"model_name":"bedrock-claude-role","litellm_params":{"model":"bedrock/converse/anthropic.claude-3-5-sonnet-20241022-v2:0","aws_role_name":"os.environ/AWS_ROLE_NAME","aws_session_name":"litellm-session","aws_region_name":"os.environ/AWS_REGION_NAME"}}'
add_model "sagemaker"        '{"model_name":"sagemaker-llama","litellm_params":{"model":"sagemaker/jumpstart-dft-meta-textgeneration-llama-3-8b","aws_region_name":"os.environ/AWS_REGION_NAME","input_cost_per_second":0.000420}}'
add_model "watsonx"          '{"model_name":"watsonx-llama","litellm_params":{"model":"watsonx/meta-llama/llama-3-3-70b-instruct","api_key":"os.environ/WATSONX_API_KEY","api_base":"os.environ/WATSONX_URL","project_id":"os.environ/WATSONX_PROJECT_ID"}}'
add_model "oci"              '{"model_name":"oci-cohere","litellm_params":{"model":"oci/cohere.command-r-plus","oci_region":"os.environ/OCI_REGION","oci_user":"os.environ/OCI_USER","oci_tenancy":"os.environ/OCI_TENANCY","oci_fingerprint":"os.environ/OCI_FINGERPRINT","oci_key_file":"os.environ/OCI_KEY_FILE"}}'
add_model "snowflake"        '{"model_name":"snowflake-llama","litellm_params":{"model":"snowflake/llama3.1-70b","api_base":"os.environ/SNOWFLAKE_API_BASE","api_key":"os.environ/SNOWFLAKE_JWT"}}'

# -----------------------------------------------------------------------------
# CASE 4 — Embeddings (mode: embedding).
# -----------------------------------------------------------------------------
echo "==> Case 4: embeddings"
add_model "openai embed"     '{"model_name":"text-embedding-3-small","litellm_params":{"model":"openai/text-embedding-3-small","api_key":"os.environ/OPENAI_API_KEY"},"model_info":{"mode":"embedding","base_model":"text-embedding-3-small"}}'
add_model "cohere embed"     '{"model_name":"cohere-embed","litellm_params":{"model":"cohere/embed-english-v3.0","api_key":"os.environ/COHERE_API_KEY"},"model_info":{"mode":"embedding"}}'
add_model "mistral embed"    '{"model_name":"mistral-embed","litellm_params":{"model":"mistral/mistral-embed","api_key":"os.environ/MISTRAL_API_KEY"},"model_info":{"mode":"embedding"}}'
add_model "voyage embed"     '{"model_name":"voyage-embed","litellm_params":{"model":"voyage/voyage-3","api_key":"os.environ/VOYAGE_API_KEY"},"model_info":{"mode":"embedding"}}'
add_model "jina embed"       '{"model_name":"jina-embed","litellm_params":{"model":"jina_ai/jina-embeddings-v3","api_key":"os.environ/JINA_AI_API_KEY"},"model_info":{"mode":"embedding"}}'
add_model "bedrock embed"    '{"model_name":"bedrock-titan-embed","litellm_params":{"model":"bedrock/amazon.titan-embed-text-v2:0","aws_region_name":"os.environ/AWS_REGION_NAME"},"model_info":{"mode":"embedding"}}'
add_model "vertex embed"     '{"model_name":"vertex-embed","litellm_params":{"model":"vertex_ai/text-embedding-005","vertex_project":"os.environ/VERTEX_PROJECT","vertex_location":"os.environ/VERTEX_LOCATION"},"model_info":{"mode":"embedding"}}'
add_model "infinity embed"   '{"model_name":"infinity-embed","litellm_params":{"model":"infinity/BAAI/bge-small-en-v1.5","api_base":"os.environ/INFINITY_API_BASE"},"model_info":{"mode":"embedding"}}'

# -----------------------------------------------------------------------------
# CASE 5 — Image generation (mode: image_generation).
# -----------------------------------------------------------------------------
echo "==> Case 5: image generation"
add_model "openai image"     '{"model_name":"gpt-image-1","litellm_params":{"model":"openai/gpt-image-1","api_key":"os.environ/OPENAI_API_KEY"},"model_info":{"mode":"image_generation"}}'
add_model "vertex imagen"    '{"model_name":"vertex-imagen","litellm_params":{"model":"vertex_ai/imagen-3.0-generate-002","vertex_project":"os.environ/VERTEX_PROJECT","vertex_location":"os.environ/VERTEX_LOCATION"},"model_info":{"mode":"image_generation"}}'
add_model "bedrock sd"       '{"model_name":"bedrock-sd","litellm_params":{"model":"bedrock/stability.sd3-large-v1:0","aws_region_name":"os.environ/AWS_REGION_NAME"},"model_info":{"mode":"image_generation"}}'
add_model "flux pro"         '{"model_name":"flux-pro","litellm_params":{"model":"black_forest_labs/flux-pro-1.1","api_key":"os.environ/BFL_API_KEY"},"model_info":{"mode":"image_generation"}}'
add_model "recraft image"    '{"model_name":"recraft-image","litellm_params":{"model":"recraft/recraftv3","api_key":"os.environ/RECRAFT_API_KEY"},"model_info":{"mode":"image_generation"}}'
add_model "xai image"        '{"model_name":"xai-image","litellm_params":{"model":"xai/grok-2-image","api_key":"os.environ/XAI_API_KEY"},"model_info":{"mode":"image_generation"}}'

# -----------------------------------------------------------------------------
# CASE 6 — Audio transcription (audio_transcription) & TTS (audio_speech).
# -----------------------------------------------------------------------------
echo "==> Case 6: audio (transcription + speech)"
add_model "openai whisper"   '{"model_name":"whisper","litellm_params":{"model":"openai/whisper-1","api_key":"os.environ/OPENAI_API_KEY"},"model_info":{"mode":"audio_transcription"}}'
add_model "groq whisper"     '{"model_name":"groq-whisper","litellm_params":{"model":"groq/whisper-large-v3","api_key":"os.environ/GROQ_API_KEY"},"model_info":{"mode":"audio_transcription"}}'
add_model "deepgram"         '{"model_name":"deepgram-nova","litellm_params":{"model":"deepgram/nova-3","api_key":"os.environ/DEEPGRAM_API_KEY"},"model_info":{"mode":"audio_transcription"}}'
add_model "assemblyai"       '{"model_name":"assemblyai-best","litellm_params":{"model":"assemblyai/best","api_key":"os.environ/ASSEMBLYAI_API_KEY"},"model_info":{"mode":"audio_transcription"}}'
add_model "openai tts"       '{"model_name":"openai-tts","litellm_params":{"model":"openai/tts-1","api_key":"os.environ/OPENAI_API_KEY"},"model_info":{"mode":"audio_speech"}}'
add_model "elevenlabs tts"   '{"model_name":"elevenlabs-tts","litellm_params":{"model":"elevenlabs/eleven_turbo_v2_5","api_key":"os.environ/ELEVENLABS_API_KEY"},"model_info":{"mode":"audio_speech"}}'

# -----------------------------------------------------------------------------
# CASE 7 — Rerank (mode: rerank).
# -----------------------------------------------------------------------------
echo "==> Case 7: rerank"
add_model "cohere rerank"    '{"model_name":"cohere-rerank","litellm_params":{"model":"cohere/rerank-english-v3.0","api_key":"os.environ/COHERE_API_KEY"},"model_info":{"mode":"rerank"}}'
add_model "jina rerank"      '{"model_name":"jina-rerank","litellm_params":{"model":"jina_ai/jina-reranker-v2-base-multilingual","api_key":"os.environ/JINA_AI_API_KEY"},"model_info":{"mode":"rerank"}}'
add_model "bedrock rerank"   '{"model_name":"bedrock-rerank","litellm_params":{"model":"bedrock/amazon.rerank-v1:0","aws_region_name":"os.environ/AWS_REGION_NAME"},"model_info":{"mode":"rerank"}}'
add_model "voyage rerank"    '{"model_name":"voyage-rerank","litellm_params":{"model":"voyage/rerank-2","api_key":"os.environ/VOYAGE_API_KEY"},"model_info":{"mode":"rerank"}}'
add_model "infinity rerank"  '{"model_name":"infinity-rerank","litellm_params":{"model":"infinity/BAAI/bge-reranker-base","api_base":"os.environ/INFINITY_API_BASE"},"model_info":{"mode":"rerank"}}'

# -----------------------------------------------------------------------------
# CASE 8 — Other modes: moderation, completion, responses, realtime.
# -----------------------------------------------------------------------------
echo "==> Case 8: moderation / completion / responses / realtime"
add_model "moderation"       '{"model_name":"omni-moderation","litellm_params":{"model":"openai/omni-moderation-latest","api_key":"os.environ/OPENAI_API_KEY"},"model_info":{"mode":"moderation"}}'
add_model "text completion"  '{"model_name":"gpt-instruct","litellm_params":{"model":"text-completion-openai/gpt-3.5-turbo-instruct","api_key":"os.environ/OPENAI_API_KEY"},"model_info":{"mode":"completion"}}'
add_model "responses api"    '{"model_name":"o1-responses","litellm_params":{"model":"openai/o1-pro","api_key":"os.environ/OPENAI_API_KEY"},"model_info":{"mode":"responses"}}'
add_model "openai realtime"  '{"model_name":"openai-realtime","litellm_params":{"model":"openai/gpt-4o-realtime-preview","api_key":"os.environ/OPENAI_API_KEY"},"model_info":{"mode":"realtime"}}'

# -----------------------------------------------------------------------------
# CASE 9 — Wildcard / pass-through routing.
# -----------------------------------------------------------------------------
echo "==> Case 9: wildcard routing"
add_model "catch-all"        '{"model_name":"*","litellm_params":{"model":"openai/*","api_key":"os.environ/OPENAI_API_KEY"}}'
add_model "anthropic/*"      '{"model_name":"anthropic/*","litellm_params":{"model":"anthropic/*","api_key":"os.environ/ANTHROPIC_API_KEY"}}'
add_model "bedrock/*"        '{"model_name":"bedrock/*","litellm_params":{"model":"bedrock/*","aws_region_name":"os.environ/AWS_REGION_NAME"}}'
add_model "groq/*"           '{"model_name":"groq/*","litellm_params":{"model":"groq/*","api_key":"os.environ/GROQ_API_KEY"}}'
add_model "vertex_ai/*"      '{"model_name":"vertex_ai/*","litellm_params":{"model":"vertex_ai/*","vertex_project":"os.environ/VERTEX_PROJECT","vertex_location":"os.environ/VERTEX_LOCATION"}}'

# -----------------------------------------------------------------------------
# CASE 10 — Multi-deployment load balancing (one model_name, many backends).
# -----------------------------------------------------------------------------
echo "==> Case 10: load-balanced deployments"
add_model "balanced openai"  '{"model_name":"gpt-4-balanced","litellm_params":{"model":"openai/gpt-4o","api_key":"os.environ/OPENAI_API_KEY","rpm":480},"model_info":{"id":"balanced-openai"}}'

# -----------------------------------------------------------------------------
# CASE 11 — Metadata / params: explicit id, base_model, region, limits,
# timeouts, retries, custom pricing, access groups, tags.
# -----------------------------------------------------------------------------
echo "==> Case 11: metadata & params"
add_model "pinned id"        '{"model_name":"gpt-4o-pinned-id","litellm_params":{"model":"openai/gpt-4o","api_key":"os.environ/OPENAI_API_KEY"},"model_info":{"id":"my-stable-model-id-001","base_model":"gpt-4o"}}'
add_model "region eu"        '{"model_name":"gpt-4o-eu","litellm_params":{"model":"openai/gpt-4o","api_key":"os.environ/OPENAI_API_KEY","region_name":"eu"}}'
add_model "throttled"        '{"model_name":"gpt-4o-throttled","litellm_params":{"model":"openai/gpt-4o","api_key":"os.environ/OPENAI_API_KEY","rpm":100,"tpm":100000,"timeout":300,"stream_timeout":60,"max_retries":3}}'
add_model "custom pricing"   '{"model_name":"custom-priced-model","litellm_params":{"model":"openai/gpt-4o","api_key":"os.environ/OPENAI_API_KEY","input_cost_per_token":0.0000025,"output_cost_per_token":0.00001}}'
add_model "access groups"    '{"model_name":"gpt-4o-restricted","litellm_params":{"model":"openai/gpt-4o","api_key":"os.environ/OPENAI_API_KEY"},"model_info":{"access_groups":["beta-users","internal"],"tags":["production","team-platform"]}}'

echo

dry_run_msg=""
[[ "${DRY_RUN}" == "1" ]] && dry_run_msg="(dry-run) "
echo "Seeding complete: ${added} model(s) ${dry_run_msg}processed, ${failed} failed."

[[ "${failed}" -gt 0 ]] && exit 1
exit 0
