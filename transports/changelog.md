- feat: add STS AssumeRole fields (`role_arn`, `external_id`, `session_name`) and `batch_s3_config` to Bedrock key configuration schema
- fix: rename config field `enforce_governance_header` to `enforce_auth_on_inference`
  > **Breaking change:** update any configuration files that use `enforce_governance_header` to `enforce_auth_on_inference`
- fix: case-insensitive `anthropic-beta` merge in `MergeBetaHeaders`
