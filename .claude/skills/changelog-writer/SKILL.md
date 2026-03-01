---
name: changelog-writer
description: Write changelog MDX files for Bifrost releases. Reads git history, bumps module versions following the core→framework→plugins→transport hierarchy, generates changelogs with fix/feat/hotfix/chore entries, updates docs.json navigation, and version files. Invoked with /changelog-writer or /changelog-writer <transport-version>.
allowed-tools: Read, Grep, Glob, Bash, Edit, Write, Task, AskUserQuestion
---

# Changelog Writer

Generate a complete changelog MDX file for a new Bifrost release. Reads git history to identify changes per module, asks the user for version bump type, bumps all module versions respecting the dependency hierarchy, writes the changelog MDX, and updates docs.json navigation.

## Module Hierarchy

Changes cascade down this dependency chain:

```
core → framework → plugins → transports
```

- **core** depends on nothing internal
- **framework** depends on core
- **plugins/*** each depend on core + framework
- **transports** depends on core + framework + all plugins

If core changes, every module below it must bump its version (at minimum a patch bump with a `chore:` entry like "upgraded core to vX.Y.Z").

If framework changes (but not core), plugins and transports must bump.

If only a plugin changes, transports must bump.

If only transports changes, only transports bumps.

**No changelog entry may be empty.** At minimum, every bumped module gets:
```
- chore: upgraded core to X.Y.Z and framework to X.Y.Z
```
or
```
- chore: upgraded framework to X.Y.Z
```
as appropriate.

## Usage

```
/changelog-writer                    # Interactive — prompts for everything
/changelog-writer <transport-ver>    # Pre-set transport version (e.g., v1.4.9)
```

## Workflow

### Step 1: Gather Current State

Read the current version of every module:

```bash
echo "core: $(cat core/version)"
echo "framework: $(cat framework/version)"
echo "transports: $(cat transports/version)"
for d in plugins/*/; do echo "$(basename $d): $(cat ${d}version)"; done
```

Read the latest changelog file to understand the previous release state:

```bash
# Find the latest changelog
ls -1t docs/changelogs/*.mdx | head -1
```

Then read that file to know the previous versions of all modules.

### Step 2: Identify Changes Since Last Release

Use git log to find commits since the last release tag or since the last changelog was written:

```bash
# Get the transport version from the latest changelog (it matches the release version)
LAST_VERSION=$(ls -1t docs/changelogs/*.mdx | head -1 | sed 's/.*\/v/v/' | sed 's/.mdx//')
echo "Last release: $LAST_VERSION"

# Check if a git tag exists
git tag -l "$LAST_VERSION" "v*"

# Get commits since last release
# If tag exists:
git log ${LAST_VERSION}..HEAD --oneline --no-merges

# If no tag, use date-based or commit-based approach:
# Find the commit that added the last changelog
git log --oneline --all -- "docs/changelogs/$(ls -1t docs/changelogs/*.mdx | head -1 | xargs basename)" | head -1
```

For each module, identify which files changed:

```bash
# Changes in core
git diff --name-only ${BASE}..HEAD -- core/

# Changes in framework
git diff --name-only ${BASE}..HEAD -- framework/

# Changes in each plugin
for d in plugins/*/; do
  CHANGES=$(git diff --name-only ${BASE}..HEAD -- "$d" | wc -l)
  if [ "$CHANGES" -gt 0 ]; then
    echo "$(basename $d): $CHANGES files changed"
  fi
done

# Changes in transports
git diff --name-only ${BASE}..HEAD -- transports/
```

### Step 3: Classify Changes and Determine Bump Types

Present the identified changes to the user and ask what type of version bump each changed module needs.

**Ask the user with AskUserQuestion:**

For EACH module that has actual code changes (not just dependency bumps), ask:

"What type of version bump for **{module}**? Changes detected: {summary of changes}"

Options:
- **patch** — Bug fixes, small improvements (0.0.X)
- **minor** — New features, non-breaking changes (0.X.0)
- **major** — Breaking changes (X.0.0)

**Change type classification for changelog entries:**
- `fix:` — Bug fixes
- `feat:` — New features or capabilities
- `hotfix:` — Urgent production fixes
- `chore:` — Dependency bumps, maintenance, non-functional changes

**Rules:**
1. Only modules with actual code changes get asked about bump type
2. Modules that only need a dependency bump automatically get a **patch** bump
3. The user decides the bump type for modules with real changes
4. Ask about bumps in hierarchy order: core first, then framework, then plugins, then transports

### Step 4: Calculate New Versions

Apply version bumps. Semver rules:

- **patch**: `1.4.4` → `1.4.5`
- **minor**: `1.4.4` → `1.5.0`
- **major**: `1.4.4` → `2.0.0`

Calculate new versions for ALL modules following the cascade rules:

```
new_core_version = bump(current_core, user_chosen_bump) if core changed, else current_core
new_framework_version = bump(current_framework, user_chosen_bump) if framework changed, else patch_bump(current_framework) if core changed, else current_framework
new_plugin_X_version = bump(current_plugin_X, user_chosen_bump) if plugin_X changed, else patch_bump(current_plugin_X) if core or framework changed, else current_plugin_X
new_transport_version = bump(current_transport, user_chosen_bump) if transport changed, else patch_bump(current_transport) if any upstream changed
```

**Present the version plan to the user for confirmation before proceeding.**

Show a table like:

```
Module             Current    New       Bump Type    Reason
core               1.4.4      1.4.5    patch        code changes
framework          1.2.22     1.2.23   patch        cascade from core
governance         1.4.23     1.4.24   patch        cascade from core+framework
jsonparser         1.4.22     1.4.23   patch        cascade from core+framework
litellmcompat      0.0.12     0.0.13   patch        cascade from core+framework
logging            1.4.22     1.4.23   patch        code changes + cascade
maxim              1.5.21     1.5.22   patch        cascade from core+framework
mocker             1.4.22     1.4.23   patch        cascade from core+framework
otel               1.1.22     1.1.23   patch        cascade from core+framework
semanticcache      1.4.21     1.4.22   patch        cascade from core+framework
telemetry          1.4.23     1.4.24   patch        cascade from core+framework
transports         1.4.8      1.4.9    patch        cascade from all
```

Wait for user confirmation. If they want to adjust any version, update accordingly.

### Step 5: Collect Changelog Entries

For each module that has actual code changes (not just cascading bumps), compose the changelog entries from the git log.

**Read the actual git commits and changed code** to write meaningful entries:

```bash
# For each changed module, get detailed commit messages
git log ${BASE}..HEAD --oneline --no-merges -- core/
git log ${BASE}..HEAD --oneline --no-merges -- framework/
# etc.
```

**Format each entry with the appropriate prefix:**
- `- fix:` description of what was fixed
- `- feat:` description of new feature
- `- hotfix:` description of urgent fix
- `- chore:` dependency bumps, maintenance

**For modules with only cascading bumps, use:**
- `- chore: upgraded core to {new_core_version} and framework to {new_framework_version}` (if both changed)
- `- chore: upgraded core to {new_core_version}` (if only core changed)
- `- chore: upgraded framework to {new_framework_version}` (if only framework changed)

**For the Bifrost(HTTP) / transports entry:**
- Include ALL notable changes from ALL modules (this is the top-level summary)
- Always end with `- chore: bump core version to {X} and framework to {Y}` if those changed

**Present the draft entries to the user for review before writing the file.**

### Step 6: Write the Changelog MDX File

The new transport version determines the filename: `docs/changelogs/v{new_transport_version}.mdx`

**Template:**

```mdx
---
title: "v{new_transport_version}"
description: "v{new_transport_version} changelog - {YYYY-MM-DD}"
---
<Tabs>
  <Tab title="NPX">
    ```bash
    npx -y @maximhq/bifrost --transport-version v{new_transport_version}
    ```
  </Tab>
  <Tab title="Docker">
    ```bash
    docker pull maximhq/bifrost:v{new_transport_version}
    docker run -p 8080:8080 maximhq/bifrost:v{new_transport_version}
    ```
  </Tab>
</Tabs>

<Update label="Bifrost(HTTP)" description="{new_transport_version}">
{transport_entries}

</Update>
<Update label="Core" description="{new_core_version}">
{core_entries}

</Update>
<Update label="Framework" description="{new_framework_version}">
{framework_entries}

</Update>
<Update label="governance" description="{new_governance_version}">
{governance_entries}

</Update>
<Update label="jsonparser" description="{new_jsonparser_version}">
{jsonparser_entries}

</Update>
<Update label="litellmcompat" description="{new_litellmcompat_version}">
{litellmcompat_entries}

</Update>
<Update label="logging" description="{new_logging_version}">
{logging_entries}

</Update>
<Update label="maxim" description="{new_maxim_version}">
{maxim_entries}

</Update>
<Update label="mocker" description="{new_mocker_version}">
{mocker_entries}

</Update>
<Update label="otel" description="{new_otel_version}">
{otel_entries}

</Update>
<Update label="semanticcache" description="{new_semanticcache_version}">
{semanticcache_entries}

</Update>
<Update label="telemetry" description="{new_telemetry_version}">
{telemetry_entries}

</Update>
```

**Important formatting rules:**
- Plugin labels are **lowercase** (e.g., `governance`, not `Governance`)
- Core and Framework labels are **capitalized** (e.g., `Core`, `Framework`)
- Bifrost(HTTP) label is always `Bifrost(HTTP)`
- Version in `description` attribute has NO `v` prefix (e.g., `1.4.9` not `v1.4.9`)
- Each entry starts with `- ` followed by the type prefix and colon
- There is a blank line before the closing `</Update>` tag
- No blank line between entries within an `<Update>` block
- Module order is always: Bifrost(HTTP), Core, Framework, then plugins alphabetically (governance, jsonparser, litellmcompat, logging, maxim, mocker, otel, semanticcache, telemetry)

### Step 7: Update docs.json Navigation

Add the new changelog to the navigation in `docs/docs.json`.

The Changelogs tab is at `navigation.tabs[6]`. New changelogs go at the **top** of the `pages` array (before any month groups), as the most recent entries are ungrouped at the top.

```python
# Pseudo-logic:
# 1. Read docs.json
# 2. Find the Changelogs tab (tabs[6])
# 3. Insert "changelogs/v{new_transport_version}" at position 0 of pages array
# 4. Write docs.json back
```

Use Edit tool to insert the new entry at the top of the changelogs pages list. The entry should be:
```
"changelogs/v{new_transport_version}",
```

**When the current month changes from what's at the top of the list:**
If the ungrouped entries at the top of the pages array are from a previous month, consider creating a new month group to move them into. Ask the user if they want to group previous month's entries. The format is:
```json
{
  "group": "Month YYYY",
  "pages": [...]
}
```

### Step 8: Update Version Files

Update the `version` file in each module that was bumped:

```bash
echo "{new_version}" > core/version
echo "{new_version}" > framework/version
echo "{new_version}" > transports/version
echo "{new_version}" > plugins/{plugin}/version
```

**Do NOT update go.mod files** — that is handled separately by the developer as part of the release process.

### Step 9: Present Summary

After all files are written, present a summary:

```
## Changelog Written: v{new_transport_version}

### Files Created/Modified:
- Created: docs/changelogs/v{new_transport_version}.mdx
- Modified: docs/docs.json (added to navigation)
- Updated: {list of version files updated}

### Version Bumps:
{table of old → new versions}

### Next Steps:
1. Review the changelog: docs/changelogs/v{new_transport_version}.mdx
2. Update go.mod files with new dependency versions
3. Run `go mod tidy` in each module
4. Tag the release: git tag v{new_transport_version}
```

## Error Handling

### No Changes Detected
If git diff shows no changes since the last release:
```
No changes detected since the last release (v{last_version}).
Are you sure you want to create a new changelog?
```
Ask the user to confirm or provide a different base commit/tag.

### Version Conflict
If the calculated new version already exists as a changelog file:
```
Changelog for v{version} already exists at docs/changelogs/v{version}.mdx.
Would you like to:
1. Overwrite the existing changelog
2. Choose a different version number
```

### Missing Module Version File
If a version file is missing:
```bash
# Fallback: read version from go.mod
grep "^module" {module}/go.mod
```
Ask the user what version to use.

## Project Directory Reference

```
bifrost/
├── core/
│   ├── version              # Plain text: "1.4.4"
│   └── go.mod               # Module: github.com/maximhq/bifrost/core
├── framework/
│   ├── version              # Plain text: "1.2.22"
│   └── go.mod               # Depends on: core
├── plugins/
│   ├── governance/version   # Each plugin has its own version file
│   ├── jsonparser/version
│   ├── litellmcompat/version
│   ├── logging/version
│   ├── maxim/version
│   ├── mocker/version
│   ├── otel/version
│   ├── semanticcache/version
│   └── telemetry/version
├── transports/
│   ├── version              # Plain text: "1.4.8"
│   └── go.mod               # Depends on: core, framework, all plugins
└── docs/
    ├── changelogs/          # v{transport_version}.mdx files
    └── docs.json            # Navigation config (changelogs tab at index 6)
```

## Plugin List (Alphabetical Order for Changelog)

This is the canonical order for plugins in the changelog:

1. governance
2. jsonparser
3. litellmcompat
4. logging
5. maxim
6. mocker
7. otel
8. semanticcache
9. telemetry
