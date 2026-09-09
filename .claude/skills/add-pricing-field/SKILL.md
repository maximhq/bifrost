---
name: add-pricing-field
description: Wire a new model-pricing field (a datasheet cost key like `cost_per_request`, `output_cost_per_video_per_second_720p`, etc.) end-to-end through Bifrost's pricing engine - Options struct, DB table + migration, datasheet sync upsert columns, cost calculation, custom pricing overrides, public API, OpenAPI docs, MDX docs, and the UI override form. Ends with a repo-wide probe to confirm nothing was missed. Invoked with /add-pricing-field <field_name> or /add-pricing-field (asks for the field).
allowed-tools: Read, Grep, Glob, Bash, Edit, Write, AskUserQuestion
---

# Add Pricing Field

Add a new per-model pricing field to Bifrost's cost engine so it is parsed from the upstream
datasheet, persisted, billed correctly, overridable, and documented — with no silent gap.

The pricing engine has one source-of-truth shape (`Options` in `framework/modelcatalog/datasheet/types.go`)
that gets mechanically mirrored into ~8 other places. Missing any one of them produces a field that
*looks* wired (compiles, shows up in one API) but silently doesn't bill, doesn't survive the 24h
datasheet resync, or can't be overridden — so treat every step below as mandatory, not optional.

## Before You Start

Ask (or infer from context) three things about the new field:

1. **Field name** — the exact upstream datasheet JSON key (e.g. `cost_per_request`,
   `output_cost_per_image_above_8_and_8_pixels`). This becomes the Go field name (PascalCase) and
   the DB column name (as-is, snake_case).
2. **Semantics** — what usage quantity does it multiply, and is it *additive* on top of another
   cost (like a flat per-request surcharge) or does it *replace/tier* an existing rate (like a
   pixel-threshold override)? This determines where in `cost.go` it plugs in — read the existing
   `compute*Cost` functions for the closest analog before writing new logic.
3. **Which request type(s)** it applies to — drives which `compute*Cost` function to touch and the
   UI's `requestTypeGroups` tagging. At the Go level this includes `container` (`schemas.
   ContainerCreateRequest`), but the override UI's `REQUEST_TYPE_GROUPS` only has 7 groups (chat/
   text/responses, embedding, rerank, audio, image, video, ocr) — there is no dedicated `container`
   group. A container-priced field (e.g. `code_interpreter_cost_per_session`) still needs a
   `requestTypeGroups` entry in Step 9, so tag it onto the existing group it's conceptually closest
   to (that field uses `"chat"`) rather than inventing an unsupported `"container"` value.

If any of these is unclear from the user's message, ask before writing code — silently guessing
the billing semantics of a money field is the one mistake in this skill that isn't easily caught
by tests.

## Key File Paths

| Concern | File | What to add |
|---|---|---|
| Canonical struct | `framework/modelcatalog/datasheet/types.go` | Field on `Options` (with `json:"..."` tag) |
| Entry↔Table mapping | same file, `convertEntryToTablePricing` / `convertTablePricingToEntry` | Mapping line in both directions |
| DB table | `framework/configstore/tables/modelpricing.go` | Field on `TableModelPricing` (gorm `column:` tag) |
| Migration | `framework/configstore/migrations.go` | New `migrationAdd<X>Column` func + registration in the migrations slice |
| **Datasheet sync upsert** | `framework/configstore/rdb.go`, `pricingSyncUpdateColumns` | Column name string — **easy to miss, silently breaks resync** |
| Custom pricing overrides | `framework/modelcatalog/datasheet/overrides.go`, `patchPricing` | `{dst: &patched.X, src: override.X}` entry |
| Cost calculation | `framework/modelcatalog/datasheet/cost.go` | Wire into the relevant `compute*Cost` function (or `computeCostFromInput` if it's a flat cross-cutting surcharge) |
| Public API summary | `framework/modelcatalog/modelinfo.go`, `ApplyModelInfo` | Only if the field belongs in `schemas.Pricing` (prompt/completion/request/image/web_search/internal_reasoning/cache read+write) — check `core/schemas/models.go` `Pricing` struct first |
| OpenAPI source | `docs/openapi/schemas/management/governance.yaml`, `PricingPatch` schema | New property with `type: number`, `minimum: 0`, description |
| OpenAPI bundle | `docs/openapi/openapi.json` | Regenerate — do not hand-edit (see Step 7) |
| Field reference docs | `docs/providers/custom-pricing.mdx` | Row in the relevant section's table |
| Architecture excerpt | `docs/architecture/framework/model-catalog.mdx` | Optional — file says "excerpt", but keep the "Costs - Other"-style section current when touching an adjacent field |
| UI override type | `ui/lib/types/governance.ts`, `PricingOverridePatch` | `field_name?: number;` |
| UI override form | `ui/app/workspace/custom-pricing/overrides/pricingOverrideSheet.tsx`, `PRICING_FIELDS` | `{ key, label, group, requestTypeGroups }` entry |
| Tests | `framework/modelcatalog/datasheet/cost_test.go`, `overrides_test.go` | One cost-calculation test, one `patchPricing` test |

---

## Workflow

### Step 1: Confirm Semantics Against the Closest Existing Field

Before writing anything, `grep` for the field family this belongs to (image / video / audio / cache /
tiered-token / flat-fee) and read its existing `compute*Cost` handling in `cost.go` end to end. Prefer
the closest existing pattern. If no pattern matches, stop and ask for confirmation before introducing
a new billing path — do not force the field into an ill-fitting pattern. In particular check:

- Is it **additive** (billed once regardless of/on top of usage — like `search_context_cost_per_query`
  or `code_interpreter_cost_per_session`)? → wire into the relevant compute function's return, or into
  `computeCostFromInput` directly if it applies across every request type.
- Is it a **threshold tier** (like `output_cost_per_image_above_2048_and_2048_pixels`)? → add a
  `case pixels >= threshold && pricing.X != nil:` branch in the existing `switch` in the matching
  `compute*Cost` function, ordered from largest threshold to smallest.
- Is it a **rate substitute** for an existing base rate under some condition (service tier, region,
  fast mode)? → follow the `tiered*Rate` helper pattern already used for priority/flex/fast tiers.

### Step 2: `Options` Struct + Conversions

In `framework/modelcatalog/datasheet/types.go`:

1. Add the field to `Options`, in the section matching its family (`// Costs - Image`, `// Costs -
   Other`, etc.), with the exact upstream JSON tag.
2. Add the corresponding line to `convertEntryToTablePricing` (Entry → TableModelPricing).
3. Add the corresponding line to `convertTablePricingToEntry` (TableModelPricing → Entry).

### Step 3: DB Table + Migration

1. `framework/configstore/tables/modelpricing.go`: add the field to `TableModelPricing` with
   `gorm:"default:null;column:<snake_case>"` and matching `json` tag, in the matching comment
   section.
2. `framework/configstore/migrations.go`:
   - Add a new `migrationAdd<Name>Column` function, modeled on `migrationAddOCRPricingColumns` —
     use `addColumnIfNotExists` / `dropColumnIfExists`, the `configstore` package-local aliases for
     `migrator.AddColumnIfNotExists` / `migrator.DropColumnIfExists`, never a bare `ALTER TABLE`.
   - Register it as a new entry at the **end** of the migrations slice: `{IDs: []string{"add_<x>_column"}, run: migrationAdd<Name>Column}`.

### Step 4: Datasheet Sync Upsert Columns — Do Not Skip

In `framework/configstore/rdb.go`, add the column name to `pricingSyncUpdateColumns`, in the
comment section matching its family. **This is the step most likely to be silently forgotten**:
`Create()` on a brand-new row writes every column, so a fresh sync looks fine in testing — the bug
only shows up on the *second* sync of an *existing* model, when `ON CONFLICT DO UPDATE` silently
drops the field because it isn't in the explicit update-column list. If you skip this, the field
works until the next 24h resync, then quietly reverts to null forever.

### Step 5: Custom Pricing Overrides

In `framework/modelcatalog/datasheet/overrides.go`, add `{dst: &patched.X, src: override.X}` to the
field list in `patchPricing`. No handler changes are needed — `CreatePricingOverrideRequest`/
`UpdatePricingOverrideRequest` embed `Options` generically via the `Patch` field.

### Step 6: Cost Calculation

Wire the field into `cost.go` per the semantics decided in Step 1. If it's a flat, cross-cutting
surcharge (bills once per request regardless of type), add it in `computeCostFromInput` **after**
the per-request-type `switch`, additive on the switch's result — but leave the `default:` branch
(unrecognized request types) returning `0` unconditionally, so an unmapped type never gets billed
just because a pricing row happens to carry the new field.

`computeCostFromInput` is not on every path, though: `calculateCostWithCache`'s direct-cache-hit
branch returns `0` before reaching it (no provider call happened at all), and its semantic-cache-hit
branch bills only `computeCacheEmbeddingCost`, bypassing `computeCostFromInput` entirely. A flat
surcharge wired only into `computeCostFromInput` therefore never fires on either cache-hit path.
Whether that's correct depends on what the field means — "per LLM call" (skip on cache hits, no
LLM call was made) vs. "per billed request regardless of cache" (should still fire). Don't assume
either answer silently: state the two cache-hit branches' behavior to the user and confirm which
one the new field should have before finalizing the wiring.

### Step 7: Public API

Always check `core/schemas/models.go`'s `Pricing` struct against the new field. It intentionally
exposes only a handful of fields (`Prompt`, `Completion`, `Request`, `Image`, `WebSearch`,
`InternalReasoning`, `InputCacheRead`, `InputCacheWrite`) — not every `Options` field belongs here.
If the new field maps onto one of these existing (possibly still-unpopulated) slots, populate it in
`ApplyModelInfo` (`framework/modelcatalog/modelinfo.go`) via `formatCost`. If it doesn't map to any
existing slot, do not add a new field to the public `Pricing` struct without the user explicitly
asking for a wider public surface — but the check itself is not optional.

### Step 8: OpenAPI + Docs

1. `docs/openapi/schemas/management/governance.yaml` — add the property to the `PricingPatch`
   schema (`type: number`, `minimum: 0`, plus `description` if the field's meaning isn't
   self-evident from its name).
2. Regenerate the bundle — **never hand-edit `openapi.json`**. Run the bundler in a subshell so
   the working directory doesn't leak into the following diff, then inspect content (not just
   `--stat`, which won't confirm *which* lines changed) from the repo root:
   ```bash
   (cd docs/openapi && python3 bundle.py)
   git diff -- docs/openapi/openapi.json
   ```
   Confirm only the new field's lines appear in the diff.
3. `docs/providers/custom-pricing.mdx` — add a row to the field-description table in the matching
   section (Text/Cache/Image/Audio-Video/Other/OCR).
4. `docs/architecture/framework/model-catalog.mdx` — this Go excerpt is explicitly non-exhaustive
   about the *full* `Options` struct, but always add the new field to the section it belongs to so
   the excerpt doesn't drift stale relative to the fields it does list.

Per house convention, explain the exact doc lines you're about to add and get a quick confirmation
before writing to `.mdx`/`.yaml` files — unless the user's request already explicitly named docs as
in-scope for this change.

### Step 9: UI

1. `ui/lib/types/governance.ts` — add `field_name?: number;` to `PricingOverridePatch`, in the
   matching comment section.
2. `ui/app/workspace/custom-pricing/overrides/pricingOverrideSheet.tsx` — add an entry to
   `PRICING_FIELDS`: `key` (exact JSON field name), a short human `label`, `group` (which visual
   section it renders under — usually matches an existing sibling field's group), and
   `requestTypeGroups` (which of `chat/embedding/rerank/audio/image/video/ocr` it applies to, per
   Step 1's semantics — list every group it can price for a cross-cutting flat fee).

### Step 10: Tests

- `framework/modelcatalog/datasheet/cost_test.go` — one test exercising the new field through
  `Store.CalculateCost` end-to-end (build a pricing row, a response, assert the dollar amount),
  modeled on the nearest existing `TestCalculateCost_*` test.
- `framework/modelcatalog/datasheet/overrides_test.go` — one `patchPricing` test asserting the new
  field passes through from `Options` to `TableModelPricing`.
- `framework/configstore/rdb_test.go` — a regression test for the sync-upsert path itself, since
  neither test above would catch a forgotten `pricingSyncUpdateColumns` entry (Step 4's top risk).
  Model it on `TestUpsertModelPricesBatch_SQLite`: upsert a row, re-upsert it with the new field set
  to a non-null value, then assert the re-fetched row still has it — this is exactly the
  `ON CONFLICT DO UPDATE` path that silently drops columns missing from the update-column list.

### Step 11: Build + Test

```bash
set -euo pipefail
cd framework && go build ./... && go test ./modelcatalog/... ./configstore/...
cd ../transports && go build ./...
cd ../ui && ./node_modules/.bin/tsc --noEmit -p tsconfig.json
```

Run `tsc` bare, not piped through `grep` — a filter on the output launders `tsc`'s own exit code
into `grep`'s, so a real compiler failure with no matching text would falsely report success (and a
clean run with no matching text would falsely report failure). If you want to scan the output for
just the touched files, capture it to a variable first and check `tsc`'s exit status separately:
```bash
tsc_out=$(./node_modules/.bin/tsc --noEmit -p tsconfig.json 2>&1); tsc_status=$?
echo "$tsc_out" | grep -i "pricingOverrideSheet\|governance.ts" || true
[ "$tsc_status" -eq 0 ]
```

### Step 12: Repo-Wide Probe (Do Not Skip)

Before calling this done, grep the whole repo for a sibling field already known to be fully wired
(e.g. `search_context_cost_per_query` or `code_interpreter_cost_per_session`) and check every hit —
this catches fixture files, example configs, helm values, and any other place that enumerates
pricing fields you didn't think to check:

```bash
grep -rln "search_context_cost_per_query\|SearchContextCostPerQuery" \
  --include="*.go" --include="*.ts" --include="*.tsx" --include="*.json" \
  --include="*.yaml" --include="*.yml" --include="*.mdx" --include="*.md" . \
  | grep -v node_modules | grep -v "/out/" | grep -v "\.next/"
```

For each hit not already covered by Steps 2–9, decide: is it an enumerated schema that needs the new
field (fix it), or realistic fixture/example data that's intentionally non-exhaustive (leave it)?
State which for anything ambiguous rather than silently skipping it.

Report a final table of every file touched, one row per file, so the user can review the full
diff surface before it's committed.

---

## Important Rules

- **Never** hand-edit `docs/openapi/openapi.json` — always regenerate via `docs/openapi/bundle.py`
  after editing the source YAML.
- **Never** skip `pricingSyncUpdateColumns` in `rdb.go` — this is the single most common way a new
  pricing field silently stops working after the first datasheet resync.
- **Never** add a bare `ALTER TABLE` migration — use the `configstore` package-local aliases
  `addColumnIfNotExists` / `dropColumnIfExists` (which wrap `migrator.AddColumnIfNotExists` /
  `migrator.DropColumnIfExists`) so concurrent/rolling-deploy migrations stay idempotent.
- **Never** widen `core/schemas/models.go`'s public `Pricing` struct just to surface a new field —
  that struct is a deliberately small summary; ask the user first if they want it there.
- **Always** confirm the `default:` case in `computeCostFromInput`'s request-type switch keeps
  returning `0` — an unmapped request type must never get billed just because a resolved pricing row
  happens to carry the new field.
- **Always** finish with the Step 12 repo-wide probe — it is what catches the locations this
  checklist's authors didn't think of.
