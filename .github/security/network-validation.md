# Network isolation diagnostics

Normal PR checks inspect workflow definitions and run offline policy regressions.
They must not deliberately contact blocked endpoints: each denied request creates
a real StepSecurity baseline alert, multiplied across every PR in a stack.

`network-security-validation.yml` is manual-only. An operator must explicitly
acknowledge the expected alerts and obtain `pr-testing` admin approval before a
runner starts. It has no checkout or release credentials. Do not disable baseline
alerts globally or allow raw-content domains to make this diagnostic pass.

The diagnostic tests strict hostname isolation. HTTP errors and redirects count
as reachable; it does not follow redirects or download executable payloads. It
reports HTTP status and peer address and checks every hostname before failing.

## Observed limitation, 2026-10-21

In [run 39640554011, job 119362617578](https://github.com/maximhq/bifrost/actions/runs/39640554011/job/119362617578),
`raw.githubusercontent.com` failed DNS resolution, but `raw.github.com` was
reachable. The agent's effective wildcard policy included `*.github.com` even
though the workflow supplied only `github.com:443`. Thus removing a hostname from
the YAML allowlist is not proof that built-in exceptions block it.

A separate read-only HTTP HEAD request to the benign `raw.github.com` URL returned
301 to `raw.githubusercontent.com`. The CI log did not record its HTTP status.
The job proves hostname reachability, not successful retrieval of a malicious
payload, and is not evidence of a compromise.

The initial PR-wide probe was responsible for repeated blocked-endpoint alerts.
It has been moved to this dedicated manual workflow; the static checks still
reject explicit raw-host allowlists and wildcard rules that would allow them.
The runtime alias limitation remains open. Resolving it requires a supported
StepSecurity policy override or independent domain-aware enforcement, verified
against the effective policy. No path-level restriction is claimed.

Do not automatically rerun this diagnostic to clear a failed check. Coordinate a
single admin-approved run when validating a relevant policy change, and expect
the deliberate blocked requests to appear in the security dashboard.
