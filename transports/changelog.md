## ✨ Features

- **Model Details API** — Added /api/models/details endpoint for model capability metadata
- **Anthropic Beta Headers** — Support for Anthropic beta feature headers in requests

## 🐞 Fixed

- **Reasoning Content Leak** — Prevented reasoning text from leaking into Gemini response content
- **Timeout Status Code** — Fixed timeout status code handling across all providers
- **Cross-Provider Cache** — Preserved cached provider metadata on cross-provider cache hits
- **Governance Virtual Keys** — Populated customer virtual_keys in governance APIs
- **List Models Integration** — Removed default provider override on list models request in integrations
- **Client Settings Headers** — Fixed Client settings UI to accept * as allowed headers

## 🔧 Maintenance

- **FIPS Docker Image** — Switched to FIPS-compliant base image for Docker builds
- **Security Hardening** — Applied StepSecurity best practices to CI/CD pipeline (thanks [@step-security-bot](https://github.com/step-security-bot)!)
- **Snyk Fixes** — Addressed Snyk vulnerability findings in Docker configuration
