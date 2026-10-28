---
name: helm-update
description: Apply Bifrost Helm chart updates, checking the published repository index and grouping changes into the next unreleased version. Keep values, schemas, templates, README, changelog, and navigation consistent. Invoked with /helm-update.
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

### Step 1: Check the published release and local state

Read the production Helm repository index used by the installation and upgrade docs:

```bash
curl -fsSL https://maximhq.github.io/bifrost/helm-charts/index.yaml
```

Parse `entries.bifrost` and select the highest stable chart `version` using semantic version ordering. Do not confuse `appVersion` with the chart version or rely on array order. This is the repository users add with `helm repo add bifrost https://maximhq.github.io/bifrost/helm-charts`; `helm repo update` refreshes its index.

Inspect `Chart.yaml`, README Upcoming notes, existing Helm changelog pages, and `docs.json`. A local version or changelog can describe unreleased work. Neither its presence nor its modification time establishes that it was published.

Record the published version and index URL. Identify its source commit using the matching GitHub release/tag `helm-chart-v<VERSION>`, through read-only local git queries or GitHub inspection. Use that commit for the schema comparison below. Do not substitute the commit that created a local changelog.

If the index cannot be retrieved or parsed, report the blocker and leave version selection pending. Do not fall back to guessing from local files. If the published source commit cannot be established, report that limitation before claiming the schema comparison is complete.

### Step 2: Check config.schema.json Changes Since Last Helm Release

Compare `transports/config.schema.json` at the published source commit from Step 1 with the current working tree. Include staged and unstaged changes, not only committed changes. For example, use `git diff <published-source-commit> -- transports/config.schema.json`.

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

### Step 4: Reuse or create the next unreleased version

Use the latest published chart version from Step 1 as the baseline. The target is its immediate next patch version, unless the user explicitly chooses another release line.

- If that next version already exists locally in `Chart.yaml` or a changelog, append the current changes to it. Keep its existing changes. Do not increment the version again.
- If it does not exist, set `Chart.yaml` to that next version and create its changelog in Step 6.
- If this work already introduced a separate, later unreleased version, set `Chart.yaml` to the selected target version and consolidate the later version's notes and navigation into it. Preserve all existing notes; remove only the superseded unreleased page and its navigation entry.
- Never modify a published version's release notes to add new work. If the local chart is behind the published version or uses a different intended release line, clarify the mismatch before changing it.

Examples:

| Latest published | Existing local work | Action |
|---|---|---|
| `2.1.44` | Unreleased `2.1.45` | Add changes to `2.1.45` |
| `2.1.44` | No next version yet | Create `2.1.45` |
| `2.1.45` | Local chart still at `2.1.45` | Create `2.1.46` |

Repeated runs before publication must keep using the same target version.

### Step 5: Update Helm README

File: `helm-charts/bifrost/README.md`

**5a. Set "Latest Version" to the target from Step 4**

Keep this line aligned with `Chart.yaml`. It describes the chart in the checkout; use the published index, not this line, to determine release status. For example:
```
**Latest Version:** 2.1.27
```
→
```
**Latest Version:** 2.1.28
```

**5b. Update the Upcoming section**

Look for an `### Upcoming` section immediately after the `## Changelog` heading. If it **does not exist**, insert one. Append the current changes while preserving existing bullets and avoiding duplicates on repeated runs. Do not create a new versioned heading; that happens at release time.

Structure:
```markdown
## Changelog

### Upcoming

- Brief bullet describing what was added/changed. Reference the values.yaml path (e.g. `bifrost.foo.bar`) and the config.json field it renders into (`foo_bar`). One bullet per logical change.

### 2.1.27
...existing entries...
```

Keep bullets concise — one line each. No paragraph prose. Mirror the style of existing changelog entries.

### Step 6: Update or create the target changelog

Use `docs/changelogs/helm-v<TARGET_VERSION>.mdx` for the version selected in Step 4. If it exists and is unreleased, append the current changes without replacing existing notes or duplicating bullets. Preserve its existing date. Only create a file when the target changelog does not exist:

```mdx
---
title: "v<TARGET_VERSION>"
description: "Helm v<TARGET_VERSION> changelog - <YYYY-MM-DD>"
---

<Update label="Bifrost Helm" description="v<TARGET_VERSION>">

## Changelog

- <bullet 1 — same content as README Upcoming bullets>
- <bullet 2>
...

</Update>
```

For a new file, use today's date and mirror the existing changelog style. Include all changes grouped into the target version, including its previous unreleased notes.

### Step 7: Update docs.json Navigation

In `docs/docs.json`, find the `"item": "Helm"` group under changelogs. Ensure the target page appears exactly once at the top of its `pages` array. Reuse an existing entry rather than inserting another one:

```json
"pages": [
  "changelogs/helm-v2.1.28",   ← insert here
  "changelogs/helm-v2.1.27",
  ...
]
```

### Step 8: Final Diff Review (Correctness Check)

After all edits, run a fresh perspective check:

Confirm that `Chart.yaml`, README, changelog, and navigation use the same target version. Report the latest published version, the chosen target, and whether the target was reused or created. Check that consolidation preserved all earlier unreleased notes and left no references to a superseded page.

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

- Check the live production index on each run. Do not bump solely because the skill was invoked or because a local changelog exists.
- **Never** omit the `### Upcoming` section after Step 5 — always ensure it exists and is populated.
- **Never** promote `### Upcoming` to a versioned heading — that is done by the changelog-writer skill at release time.
- **Never** write values.yaml fields as uncommented unless they were already uncommented (i.e., they are mandatory defaults like `replicaCount`).
- **Always** verify config.schema.json mapping in the final diff review.
- If a config.schema.json gap is found but its helm mapping would be complex (e.g. a new top-level plugin system), flag it to the user rather than silently skipping.
- Keep changelog bullets brief: one line, what was added/changed, what values.yaml path, what config.json key it renders into.
