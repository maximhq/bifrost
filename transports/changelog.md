## ✨ Features

- **Truncated Label Tooltips** — Long labels in the dashboard now truncate with a hover tooltip showing the full value, applied across the logs and providers pages.

## 🐞 Fixed

- **Logs Page URL Parsing** — Array query parameters on the logs page now use `parseAsSafeArrayOf`, correctly handling special characters in URLs.
- **Bedrock Usage Calculation** — Fixed token usage calculation for the Bedrock provider.
- **Hybrid Log Token Usage** — Token usage is now rebuilt from denormalized columns in the hybrid log list (thanks [@G-XD](https://github.com/G-XD)!).

## 🔧 Maintenance

- **Governance Config Import** — Replaced `createGovernanceConfigInStore` with an empty-snapshot `mergeGovernanceConfig` path for the first config-file import.
- **Dependency Upgrades** — Bumped core to v1.6.1 and framework to v1.4.1 across all modules.
