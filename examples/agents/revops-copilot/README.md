# revops-copilot

A multi-agent LangGraph example where **every** model call, tool call and agent-to-agent call goes through Bifrost, and each one is governed, secured and observed.

The job:

> "Prepare the Q3 2026 customer health report and upload the PDF to the shared drive."

One job crosses all three Bifrost gateways (LLM, MCP, Agent) and all eight policy surfaces, including round-trips to a third-party guardrail (Google Model Armor). Pylon and Google Drive are real SaaS, reached only through Bifrost's MCP Gateway, and Bifrost holds their OAuth tokens. No agent holds a SaaS credential.

## Agents

| Agent | Does | Calls through Bifrost |
|---|---|---|
| `revops-copilot` | Answers any question. Looks things up itself with MCP tools, or delegates over A2A; builds a report only when asked. | LLM, MCP `/mcp`, A2A to the 3 subagents |
| `support-analyst` | Answers support questions, or gathers a quarter's metrics, from Pylon. | LLM, MCP `/mcp` |
| `report-writer` | Optionally looks up missing facts, writes the narrative, draws charts, renders the PDF. | LLM, MCP `/mcp` |
| `publisher` | Uploads the PDF to a fixed Drive folder and returns the link. | LLM, MCP `/mcp` |

**Every agent connects to Bifrost's MCP server** at `/mcp`, which serves every MCP tool the identity's access profile grants: today the Pylon tools from `pylon-readonly` and the Drive tools from `drive-publisher`. Grant another Virtual MCP and every agent sees its tools on its next call, with no code change. `MCP_SLUG` narrows all agents to one Virtual MCP instead.

**One identity runs the job.** `revops-copilot` holds it: a virtual key, or an Entra app that gets a JWT. The CLI starts jobs as that identity. When `revops-copilot` delegates, Bifrost forwards the same credential to the subagent (Forward Bifrost authentication: on), and the subagent uses it for its own LLM and MCP calls. One access profile therefore governs every LLM, MCP and A2A call in the job.

Each agent is an A2A v1.0 server registered on Bifrost's Agent Gateway. Each one also refuses any request that lacks the `x-agent-key` Bifrost injects, so nothing can reach an agent except through the gateway.

## Architecture

```mermaid
flowchart LR
  CLI["CLI / operator"] -->|"A2A"| AG

  subgraph BF["Bifrost Enterprise"]
    direction TB
    AG["Agent Gateway<br/>/agents/a2a/*"]
    LLM["LLM Gateway<br/>/v1/chat/completions"]
    MCP["MCP Gateway<br/>/mcp/&lt;virtual-mcp&gt;"]
    GOV[["Governance<br/>identity · grants · budgets"]]
    GR[["Guardrails"]]
    LOG[("Logs · audit · traces")]
  end

  AG --> SUP["revops-copilot"]
  SUP -->|"A2A"| AG
  AG --> SA["support-analyst"]
  AG --> RW["report-writer"]
  AG --> PUB["publisher"]

  SUP & SA & RW & PUB --> LLM --> PROV[("LLM providers")]
  SA --> MCP
  PUB --> MCP
  MCP --> PYL[("Pylon MCP")]
  MCP --> GDR[("Google Drive MCP")]

  GR -. "scan" .-> MA[("Google Model Armor")]
  GR -. "redact" .-> PII[("Presidio / Azure PII")]
```

## The 8 policy surfaces

| # | Surface | Where it runs in Bifrost | What this example configures |
|---|---|---|---|
| 1 | Ingress identity | HTTP transport pre-auth | Entra app-only token mapped to a service account, or a VK |
| 2 | Agent grant | `PreA2AHook` (governance) | Access-profile agent grants; `allow_by_default` off |
| 3 | LLM governance | `PreLLMHook` (governance) | Model allowlist, budget, rate limit on the identity's profile |
| 4 | LLM input guardrail | `PreLLMHook` (guardrails) | Model Armor: prompt injection, jailbreak |
| 5 | MCP discovery | `/mcp` admission + Virtual MCPs | `/mcp` lists only the tools of the granted Virtual MCPs; an ungranted slug answers 403 |
| 6 | MCP tool authorization | `PreMCPHook` (governance) | Tool allowlist; no Pylon writes, no Drive delete or share |
| 7 | MCP tool guardrails | `PreMCPHook` / `PostMCPHook` (guardrails) | PII redaction on Pylon results; secrets detection on Drive upload args |
| 8 | LLM output guardrail | `PostLLMHook` (guardrails, streaming too) | Model Armor on responses (monitor mode) |

Every hop also carries `x-bf-session-id` (the A2A context id) and `x-bf-agent-correlation-id` (the A2A task id). One job is therefore one session across Agent Logs, LLM Logs and MCP Logs.

## Request pipeline: when each stage runs

```mermaid
sequenceDiagram
  autonumber
  participant U as CLI (operator)
  participant AG as Bifrost Agent Gateway
  participant S as revops-copilot
  participant L as Bifrost LLM Gateway
  participant M as Bifrost MCP Gateway
  participant MA as Google Model Armor
  participant A as support-analyst
  participant W as report-writer
  participant P as publisher
  participant PY as Pylon
  participant GD as Google Drive

  U->>AG: SendMessage "Prepare Q3 report…" (token)
  Note over AG: ① identity · ② grant revops-copilot
  AG->>S: + x-agent-key
  S->>L: parse the ask (the identity)
  Note over L: ① identity · ③ model, budget, rate limit
  L->>MA: ④ scan prompt
  MA-->>L: allow
  L-->>S: ReportRequest (Q3 2026)
  Note over L,MA: ⑧ response scanned (monitor)

  S->>AG: A2A → support-analyst
  Note over AG: ① · ② grant support-analyst
  AG->>A: + x-agent-key, forwarded identity
  A->>M: list tools /mcp
  Note over M: ⑤ only granted tools visible (Pylon read, Drive upload)
  loop tool loop
    A->>L: next step (③ ④ ⑧, Model Armor round-trips)
    A->>M: call pylon search
    Note over M: ⑥ tool allowlisted
    M->>PY: Bifrost-held OAuth
    PY-->>M: issues
    Note over M: ⑦ PII redacted from the result
    M-->>A: redacted result
  end
  A-->>S: SupportMetrics

  S->>AG: A2A → report-writer
  Note over AG: ① · ②
  AG->>W: + x-agent-key, forwarded identity
  W->>L: draft narrative (③ ④ ⑧)
  W-->>S: PDF (A2A file part)

  S->>AG: A2A → publisher (PDF)
  Note over AG: ① · ②
  AG->>P: + x-agent-key, forwarded identity
  P->>L: file description (③ ④ ⑧)
  P->>M: upload via /mcp (Drive-create_file)
  Note over M: ⑤ ⑥ upload allowlisted · ⑦ secrets scan of args
  M->>GD: Bifrost-held OAuth
  GD-->>M: file link
  M-->>P: link
  P-->>S: link
  S->>L: summarize (③ ④ ⑧)
  S-->>AG: done
  AG-->>U: summary + Drive link
```

### Inside Bifrost, for a single request

```mermaid
flowchart TB
  IN(["request"]) --> AUTH{"① Transport pre-auth<br/>Entra token, SSO or VK"}
  AUTH -- "unknown / unmapped" --> R401["401 / 403"]
  AUTH --> KIND{"which gateway?"}

  KIND -- "LLM" --> G3{"③ Governance<br/>model · budget · rate"}
  G3 -- "deny" --> R403a["403 / 429"]
  G3 --> GI{"④ Input guardrail"}
  GI <-. "HTTPS" .-> MA1[("Model Armor")]
  GI -- "block" --> R400a["400 guardrail_intervention"]
  GI --> PROV["Provider call"]
  PROV --> GO{"⑧ Output guardrail"}
  GO <-. "HTTPS" .-> MA2[("Model Armor")]
  GO --> POSTG["Governance post-hook<br/>usage · cost · budget debit"]

  KIND -- "MCP" --> G5{"⑤ Virtual MCP admission<br/>is the slug granted?"}
  G5 -- "no" --> R403b["403"]
  G5 --> G6{"⑥ Tool authorization"}
  G6 -- "deny" --> R403c["mcp_tool_blocked"]
  G6 --> G7i{"⑦ Tool-input guardrail"}
  G7i -.-> SEC[("Secrets / DLP")]
  G7i --> TOOL["Upstream MCP server<br/>(Bifrost-held OAuth)"]
  TOOL --> G7o{"⑦ Tool-output guardrail"}
  G7o -.-> PIIS[("PII redaction")]

  KIND -- "A2A" --> G2{"② Agent grant"}
  G2 -- "deny" --> R403d["agent_blocked"]
  G2 --> A2AG["A2A guardrail<br/>(server-side, upcoming)"]
  A2AG --> AGENT["Agent<br/>(+ x-agent-key)"]
  style A2AG stroke-dasharray: 5 5

  POSTG & G7o & AGENT --> OBS[("Logs · audit · traces<br/>keyed by session id")]
```

## Run it

1. **Set up Bifrost.** Follow [BIFROST_SETUP.md](BIFROST_SETUP.md). You create the identities, access profiles, MCP clients, Virtual MCPs, agent registrations and guardrails. The example provisions nothing.
2. **Configure and install.**
   ```bash
   cp .env.example .env   # the identity, inbound keys, Drive folder
   make install
   make test              # unit tests, no Bifrost needed
   ```
3. **Start the agents and check the setup.**
   ```bash
   make agents            # terminal 1: four A2A servers on :9100-9103
   make preflight         # terminal 2: asks for the gateway URL, then checks identity, grants, discovery, registrations
   ```
   **Remote gateway** (Bifrost cannot reach `127.0.0.1`): run `make agents-cloudflared` instead of `make agents`. With a `cloudflared.yml` (copy `cloudflared.yml.example`; needs a domain on Cloudflare) it runs a named tunnel with fixed hostnames, so you register the agents once. Without one it falls back to free quick tunnels, whose hostnames change on every start. `make agents-ngrok` also works, but four endpoints need a paid ngrok plan. Every mode prints the four Agent Card URLs to register with their forwarding setting.
4. **Run the job.** `make chat` opens an interactive prompt (like Claude Code): type a request and watch every step live, from each LLM call and MCP tool call to each A2A delegation, with the policy surfaces that evaluated it and how long it took. `/trace` shows what Bifrost logged for the job. One-shot runs use the same live view:
   ```bash
   make ask Q="Prepare the Q3 2026 customer health report and upload the PDF to the shared drive"
   make trace ID="$SESSION_ID"   # set SESSION_ID to the x-bf-session-id printed above
   ```

## Scenarios

Each scenario targets one surface and checks that surface fired (or that the call passed). `make demo` lists them; `make demo-all` runs all of them.

| Scenario | Surface | Caller | Expected |
|---|---|---|---|
| `happy-path` | all | CLI | PDF in Drive; trace shows every hop |
| `unknown-identity` | ① | unknown VK or unmapped SP | 401 / 403 |
| `agent-not-granted` | ② | a second VK with no grants (`OUTSIDER_VK`) → publisher | `agent_blocked` |
| `model-not-allowed` | ③ | the identity, `BLOCKED_MODEL` | `model_blocked` |
| `prompt-injection` | ④ | CLI with a jailbreak ask | Model Armor blocks at the supervisor's first LLM call |
| `tool-hidden` | ⑤ | the identity → an ungranted slug | 403 |
| `tool-denied` | ⑥ | Drive delete or share | `mcp_tool_blocked` |
| `pii-redaction` | ⑦ | Pylon search | no email address reaches the agent |
| `upload-dlp` | ⑦ | upload a credential to Drive | secrets rule blocks before Drive |
| `output-scan` | ⑧ | an LLM call | allowed; verdict in LLM Logs |

A refusal never crashes the job. The refusing agent returns a structured `policy_denied` (surface, hop, status, code), the supervisor stops at the first one, and the CLI prints which surface fired.

## Layout

```
src/revops_copilot/
  common/
    config.py      env settings, read per agent via env prefix
    identity.py    VK, Entra (client credentials / certificate / managed identity), or the forwarded credential
    bifrost.py     per-task LLM, MCP and A2A clients + correlation headers
    a2aserver.py   LangGraph → A2A v1.0 server, inbound-key guard, A2A client via gateway
    toolloop.py    explicit LangGraph tool loop over a Bifrost MCP session
    policy.py      Bifrost refusal → policy surface
    contracts.py   typed payloads passed between agents
  supervisor/agent.py              revops-copilot
  agents/supportanalyst/agent.py   support-analyst
  agents/reportwriter/agent.py     report-writer (+ pdf.py)
  agents/publisher/agent.py        publisher (+ upload.py: Drive schema mapping)
  preflight.py  trace.py  scenarios.py  tunnel.py (ngrok mode)  cli.py
scenarios/*.yaml
```

## Design notes

- **One identity, forwarded by Bifrost.** Only `revops-copilot` holds a credential. Subagents hold none: they reuse the one Bifrost forwards, in the header it arrived in (`x-bf-vk` or `Authorization: Bearer`). A subagent reached without a forwarded credential rejects the task at surface 1. The trade-off: one profile covers every agent, so the Virtual MCP tool lists and the guardrails, not separate profiles, bound what each agent can do.
- **The PDF never passes through an LLM.** It travels as an A2A raw file part. The publisher uploads it with a deterministic tool call; its LLM only writes the file description.
- **Upload schema mapping happens at runtime.** The Drive MCP server is in preview, so the publisher reads the upload tool's input schema and fills fields by name. It fails with the schema in the message rather than guessing.
- **Retries are disabled on purpose** (`max_retries=0`). A policy refusal is an answer, not a transient error.
