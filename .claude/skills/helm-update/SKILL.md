---
name: helm-update
description: Apply helm chart updates for Bifrost. Detects config.schema.json changes since the last helm release, applies user-requested changes, updates values.yaml / values.schema.json / _helpers.tpl, bumps Chart.yaml version, updates helm README (Latest Version + Upcoming section), creates a docs MDX changelog, and updates docs.json navigation. Ends with a diff review to verify every helm field maps back to a config.schema.json counterpart. Invoked with /helm-update.
allowed-tools: Read, Grep, Glob, Bash, Edit, Write, AskUserQuestion
---

# Helm Update

Apply changes to the Bifrost Helm chart, keep it in sync with `config.schema.json`, and update all changelogs.

## Key File Paths

| File | Path |
|------|------|
| Config schema | `transports/config.schema.json` |
| Chart metadata | `helm-charts/bifrost/Chart.yaml` |
| Values | `helm-charts/bifrost/values.yaml` |
| Values schema | `helm-charts/bifrost/values.schema.json` |
| Helpers template | `helm-charts/bifrost/templates/_helpers.tpl` |
| Helm README | `helm-charts/bifrost/README.md` |
| Docs changelogs | `docs/changelogs/helm-v<version>.mdx` |
| Docs navigation | `docs/docs.json` |

---

## Workflow

### Step 1: Gather Current State

Read the current helm chart version and identify the last helm release commit:

```bash
# Current helm chart version
cat helm-charts/bifrost/Chart.yaml | grep '^version:'

# Latest docs helm changelog (= last released version)
ls -1t docs/changelogs/helm-v*.mdx | head -1

# Find the commit that added the latest helm changelog
LAST_HELM_MDX=$(ls -1t docs/changelogs/helm-v*.mdx | head -1)
git log --oneline -- "$LAST_HELM_MDX" | head -1
```

Save the last-release commit SHA — you'll need it to scope the config.schema.json diff.

### Step 2: Check config.schema.json Changes Since Last Helm Release

```bash
LAST_COMMIT=$(git log --oneline -- "$(ls -1t docs/changelogs/helm-v*.mdx | head -1)" | awk '{print $1}')

# What changed in config.schema.json since then?
git diff ${LAST_COMMIT}..HEAD -- transports/config.schema.json
```

Identify any fields added, removed, or changed in `config.schema.json` that are **not yet reflected** in the helm chart. These are gaps that must be closed — even if the user did not explicitly ask for them. Note each gap; you will apply them alongside the user's requested changes.

### Step 3: Apply User-Requested Changes + Fill Gaps

For every change (user-requested AND schema gaps detected in Step 2):

#### values.yaml

Add new fields as **commented-out** blocks with a realistic sample value. Place new sections near related existing fields. Keep existing uncommented defaults intact.

```yaml
# bifrost.newFeature.someField -- brief description
# someField: "example-value"
```

#### values.schema.json

Add a matching JSON schema property with:
- Correct `type` (string/boolean/integer/object/array)
- `description` matching the config.schema.json description (paraphrased concisely)
- Any `enum`, `default`, `minimum`/`maximum`, or nested `properties`/`items` needed
- Mark required fields in the parent object's `required` array if mandatory

Locate the correct parent path in `values.schema.json` by searching for the nearest ancestor field.

#### _helpers.tpl

Wire new values into the generated config.json block inside `{{- define "bifrost.config" -}}`. Follow the existing rendering patterns:

- Simple scalar: `{{- if .Values.bifrost.someField }}` → emit `"some_field": {{ .Values.bifrost.someField | toJson }}`
- Optional block: wrap in `{{- if ... }}` / `{{- end }}`
- Duration strings: pass through `toJson` unchanged
- `env.VAR_NAME` references: pass through `toJson` unchanged

Search the existing template for a nearby field to find the right insertion point.

### Step 4: Determine New Helm Chart Version

Read `helm-charts/bifrost/Chart.yaml`. Increment the **patch** version (third number) unless the scope of changes clearly warrants a minor bump. Ask the user if unsure:

```
Current: 2.1.27  →  New: 2.1.28
```

Update `Chart.yaml`:

```yaml
version: 2.1.28   # updated line
```

### Step 5: Update Helm README

File: `helm-charts/bifrost/README.md`

**5a. Bump "Latest Version" line**

Find and update:
```
**Latest Version:** 2.1.27
```
→
```
**Latest Version:** 2.1.28
```

**5b. Update the Upcoming section**

Look for an `### Upcoming` section immediately after the `## Changelog` heading. If it **does not exist**, insert one. On every run, **append** bullet points for the current changes to `### Upcoming` — do not create a new versioned heading; that happens at release time.

Structure:
```markdown
## Changelog

### Upcoming

- Brief bullet describing what was added/changed. Reference the values.yaml path (e.g. `bifrost.foo.bar`) and the config.json field it renders into (`foo_bar`). One bullet per logical change.

### 2.1.27
...existing entries...
```

Keep bullets concise — one line each. No paragraph prose. Mirror the style of existing changelog entries.

### Step 6: Create Docs MDX Changelog

Create a new file `docs/changelogs/helm-v<NEW_VERSION>.mdx`:

```mdx
---
title: "v<NEW_VERSION>"
description: "Helm v<NEW_VERSION> changelog - <YYYY-MM-DD>"
---

<Update label="Bifrost Helm" description="v<NEW_VERSION>">

## Changelog

- <bullet 1 — same content as README Upcoming bullets>
- <bullet 2>
...

</Update>
```

Use today's date (`2026-07-10` or whatever the current date is). Mirror the bullet style from `docs/changelogs/helm-v2.1.26.mdx`.

### Step 7: Update docs.json Navigation

In `docs/docs.json`, find the `"item": "Helm"` group (under changelogs). Prepend the new entry at the top of its `pages` array:

```json
"pages": [
  "changelogs/helm-v2.1.28",   ← insert here
  "changelogs/helm-v2.1.27",
  ...
]
```

### Step 8: Final Diff Review (Correctness Check)

After all edits, run a fresh perspective check:

```bash
git diff -- helm-charts/bifrost/values.yaml helm-charts/bifrost/values.schema.json helm-charts/bifrost/templates/_helpers.tpl
```

For **every** new field in the diff, verify:

1. **values.yaml** — field exists (commented out with sample value)
2. **values.schema.json** — matching property with correct type and description
3. **_helpers.tpl** — field is rendered into config.json output under the correct JSON key
4. **config.schema.json counterpart** — confirm the JSON key emitted by `_helpers.tpl` exists in `transports/config.schema.json` (or is a known helm-only field like `replicaCount`)

If any field fails check 4, either fix the mapping or flag it to the user explicitly.

Report a short table:

| Helm values path | config.json key | In config.schema.json? |
|---|---|---|
| `bifrost.foo.bar` | `foo_bar` | ✓ |
| `bifrost.baz` | `baz_config` | ✓ |

---

## Common Rendering Patterns in _helpers.tpl

```go-template
{{/* Simple optional string */}}
{{- if .Values.bifrost.server.readBufferSize }}
"read_buffer_size": {{ .Values.bifrost.server.readBufferSize | toJson }},
{{- end }}

{{/* Optional boolean */}}
{{- if hasKey .Values.bifrost.loadBalancer "directionSelectionEnabled" }}
"direction_selection_enabled": {{ .Values.bifrost.loadBalancer.directionSelectionEnabled | toJson }},
{{- end }}

{{/* Nested object — only emit if any subfield set */}}
{{- if .Values.bifrost.newFeature }}
"new_feature": {
  {{- if .Values.bifrost.newFeature.timeout }}
  "timeout": {{ .Values.bifrost.newFeature.timeout | toJson }},
  {{- end }}
},
{{- end }}
```

## Important Rules

- **Never** omit the `### Upcoming` section after Step 5 — always ensure it exists and is populated.
- **Never** promote `### Upcoming` to a versioned heading — that is done by the changelog-writer skill at release time.
- **Never** write values.yaml fields as uncommented unless they were already uncommented (i.e., they are mandatory defaults like `replicaCount`).
- **Always** verify config.schema.json mapping in the final diff review.
- If a config.schema.json gap is found but its helm mapping would be complex (e.g. a new top-level plugin system), flag it to the user rather than silently skipping.
- Keep changelog bullets brief: one line, what was added/changed, what values.yaml path, what config.json key it renders into.
