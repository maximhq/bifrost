## ✨ Features

- **Quarterly Budget Windows** - Budgets support a quarterly reset period, with a configurable fiscal start month so a fiscal year that does not begin in January windows correctly.
- **Per-Model Budgets and Rate Limits** - Virtual key provider configs accept budgets and rate limits scoped to individual models, surfaced in the UI through a unified budget override manager that groups provider and model budgets together.
- **Budget Usage Reset** - The reset budget usage flow now covers teams, customers, model limits and provider governance, not just virtual keys.
- **Fiscal Quarter UI** - The governance UI exposes the quarterly period with fiscal-quarter advanced settings, and provider cards show a model budget count in the header summary.

## 🐞 Fixed

- **Calendar Alignment Semantics** - Enabling calendar alignment preserves the currently open window and applies from the next period, instead of truncating the window in flight.
- **Together Pricing Lookup** - Fixed the pricing provider lookup for Together so model costs resolve correctly (thanks [@dani29](https://github.com/dani29)!)
- **Encrypted Reasoning Verification** - Patched encrypted reasoning content that providers rejected as unverifiable.
- **Gemini Reasoning Replay** - Standalone Gemini reasoning messages are no longer skipped when converting Responses history to Gemini contents, and a consumed reasoning item's thought text is carried alongside the signature the preceding function call took from it.
- **Bedrock Reasoning Blocks** - Bedrock no longer receives a reasoning block with an absent text key, the replayed signature attaches to the first reasoning summary block, and signature-only replay blocks serialize to a shape Bifrost can decode.
- **Cohere Encrypted Reasoning** - Encrypted reasoning is emitted alongside the summary rather than instead of it, and the marker is parsed back into EncryptedContent on ingress so it no longer reaches clients as visible reasoning text.
- **Replayed Reasoning Dropped** - Messages carrying a non-nil but empty ContentBlocks list no longer drop replayed reasoning in the Anthropic, Bedrock and Cohere converters.

## 🗄️ Database Migrations

- **add_budget_reset_config_column** - Adds the nullable `reset_config_json` column to `governance_budgets` to hold a budget's fiscal-quarter definition. Additive with no backfill, so it is safe during a rolling deploy: older binaries ignore the column and a NULL value reads back as the January default. **Non-reversible**: dropping the column would permanently delete every budget's fiscal-quarter definition and silently re-window those budgets onto the calendar year.

  <Warning>
    This migration cannot be rolled back. Take a backup of `governance_budgets` before upgrading if you need a path back to the previous release.
  </Warning>

## 🐙 Closed GitHub Issues

- [#4851](https://github.com/maximhq/bifrost/issues/4851) - v1.6.2 and v1.6.3 governance rate-limit reset causes high CPU in BumpRateLimitUsage/updateRateLimitReferences
