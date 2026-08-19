# Security Policy

  ## Reporting a Vulnerability

  The Bifrost team takes security issues seriously. We appreciate your efforts to responsibly
   disclose any vulnerabilities you find.

  **Please do NOT report security vulnerabilities through public GitHub issues.**

  Instead, please report them via one of the following methods:

  1. **GitHub Security Advisories (preferred)**: Use [GitHub's private vulnerability
  reporting](https://github.com/maximhq/bifrost/security/advisories/new) to submit a report
  directly through the repository.
  2. **Email**: Send an email to **akshay@getmaxim.ai** with the details of the
  vulnerability.

  ### What to include

  To help us triage and respond quickly, please include:

  - A description of the vulnerability and its potential impact
  - Step-by-step instructions to reproduce the issue
  - Affected version(s) and component(s) (e.g., `core`, `transports`, `plugins/*`)
  - Any relevant configuration or environment details
  - Proof-of-concept code, if available

  ### What to expect

  - **Acknowledgment**: We will acknowledge receipt of your report within **48 hours**.
  - **Updates**: We will provide status updates as we investigate, typically within **5
  business days**.
  - **Resolution**: Once a fix is available, we will coordinate with you on disclosure
  timing.
  - **Credit**: We are happy to credit reporters in our release notes and security advisories
   (unless you prefer to remain anonymous).

  ## Supported Versions

  Security updates are provided for the following versions:

  | Module       | Version | Supported          |
  | ------------ | ------- | ------------------ |
  | transports   | 1.4.x   | :white_check_mark: |
  | core         | 1.4.x   | :white_check_mark: |
  | framework    | 1.2.x  | :white_check_mark: |
  | plugins/*    | current minor version tracks  | :white_check_mark: |

  Only the latest minor release of each supported major version receives security patches. We
   recommend always running the latest version.

  ## Security Considerations

  Bifrost is an AI gateway that routes requests to multiple LLM providers. When deploying
  Bifrost, keep the following in mind:

  - **API Key Management**: Bifrost handles provider API keys. Ensure keys are stored
  securely and never committed to version control. Use environment variables or a secrets
  manager.
  - **Network Exposure**: Restrict access to the Bifrost admin interface and API endpoints
  using firewalls, VPNs, or authentication layers appropriate for your environment.
  - **TLS**: Always use TLS when exposing Bifrost to external networks.
  - **Access Profiles**: Use Bifrost's access profile and virtual key features to enforce
  least-privilege access to upstream providers.
  - **Plugin Security**: Only use plugins from trusted sources. Plugins execute within the
  request pipeline and have access to request/response data.

  ## Disclosure Policy

  We follow a coordinated disclosure process:

  1. The reporter submits the vulnerability privately.
  2. We confirm the issue and develop a fix.
  3. We release the fix and publish a security advisory.
  4. The vulnerability details are made public after users have had reasonable time to update
   (typically 30 days after the fix is released).

  We kindly ask that you do not publicly disclose the vulnerability until we have had a
  chance to address it.

  ## Scope

  The following are **in scope** for security reports:

  - The Bifrost gateway (core, transports, framework, CLI)
  - Official Bifrost plugins (`plugins/` directory)
  - The Bifrost Docker image (`maximhq/bifrost`)
  - The Bifrost web UI

  The following are **out of scope**:

  - Social engineering attacks
  - Denial of service attacks that rely purely on volumetric traffic
