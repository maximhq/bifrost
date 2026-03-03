---
name: changelog-writer
description: Write changelogs for Bifrost releases. Reads git history, bumps module versions following the core→framework→plugins→transport hierarchy, writes transports/changelog.md (enterprise-style) and per-module changelog.md files, and updates version files. Invoked with /changelog-writer or /changelog-writer <transport-version>.
allowed-tools: Read, Grep, Glob, Bash, Edit, Write, Task, AskUserQuestion
---

# Changelog Writer

Generate changelogs for a new Bifrost release. Reads git history to identify changes per module, asks the user for version bump type, bumps all module versions respecting the dependency hierarchy, writes `transports/changelog.md` and per-module `changelog.md` files, and updates version files.

**IMPORTANT: This skill NEVER creates or modifies files under `docs/`.** No MDX files, no docs.json updates. Only `changelog.md` and `version` files within module directories.

## Module Hierarchy

Changes cascade down this dependency chain:

```
core → framework → plugins → transports
```

- **core** depends on nothing internal
- **framework** depends on core
- **plugins/*** each depend on core + framework
- **transports** depends on core + framework + all plugins

If core changes, every module below it must bump its version (at minimum a patch bump).

If framework changes (but not core), plugins and transports must bump.

If only a plugin changes, transports must bump.

If only transports changes, only transports bumps.

## Usage

```
/changelog-writer                    # Interactive — prompts for everything
/changelog-writer <transport-ver>    # Pre-set transport version (e.g., v1.5.0)
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
# Find the latest docs changelog to determine the last released version
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

**Always ask the user with AskUserQuestion what bump type to use for each module.**

Ask for **every** module that will be bumped — both modules with code changes and modules with only cascade bumps. Use AskUserQuestion with up to 4 questions at a time (the tool's limit), batching in hierarchy order:

1. First batch: core, framework, and up to 2 plugins
2. Continue with remaining plugins and transports

For each module ask: "What type of version bump for **{module}**?"

Options:
- **patch** — Bug fixes, small improvements (0.0.X)
- **minor** — New features, non-breaking changes (0.X.0)
- **major** — Breaking changes (X.0.0)

**Note:** Minor bumps reset the patch version to 0 (e.g., `1.4.24` → `1.5.0`). Patch bumps only increment the last number (e.g., `1.4.24` → `1.4.25`).

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
core               1.4.4      1.5.0    minor        code changes
framework          1.2.23     1.3.0    minor        cascade from core
governance         1.4.24     1.4.25   patch        cascade from core+framework
...
transports         1.4.9      1.5.0    minor        cascade from all
```

Wait for user confirmation. If they want to adjust any version, update accordingly.

### Step 5: Collect and Write Changelog Entries

For each module, compose changelog entries from the git log.

**Read the actual git commits and changed code** to write meaningful entries:

```bash
# For each changed module, get detailed commit messages
git log ${BASE}..HEAD --oneline --no-merges -- core/
git log ${BASE}..HEAD --oneline --no-merges -- framework/
# etc.
```

#### Credit Outside Contributors

For each commit that references a PR number (e.g., `#1234`), check if the author is an outside contributor:

```bash
# Get the repo name
REPO=$(gh repo view --json nameWithOwner --jq '.nameWithOwner')

# For each PR number found in commits:
gh api "repos/$REPO/pulls/<PR_NUMBER>" --jq '"\(.number) \(.user.login) \(.author_association)"'
```

**`author_association` values:**
- `MEMBER`, `OWNER`, `COLLABORATOR` → internal team, no credit needed
- `CONTRIBUTOR`, `FIRST_TIMER`, `FIRST_TIME_CONTRIBUTOR`, `NONE` → outside contributor, credit them

**How to credit:**

Use a markdown link to the contributor's GitHub profile: `[@username](https://github.com/username)`

- In **transports/changelog.md** (enterprise-style): append `(thanks [@username](https://github.com/username)!)` to the description
  - Example: `- **Logprobs JSON Tag** — Fixed logprobs JSON tag in BifrostResponseChoice (thanks [@contributor](https://github.com/contributor)!)`
- In **per-module changelog.md** (flat-list): append `(thanks [@username](https://github.com/username)!)` to the entry
  - Example: `- fix: fixed logprobs JSON tag in BifrostResponseChoice (thanks [@contributor](https://github.com/contributor)!)`

If multiple PRs from the same outside contributor are grouped into one entry, credit them once.

**Present the draft entries to the user for review before writing files.**

#### Per-Module changelog.md (core, framework, plugins)

Write simple flat-list entries to each module's `changelog.md`:

```markdown
- fix: description of what was fixed
- feat: description of new feature
- hotfix: description of urgent fix
```

For modules with only cascading bumps (no code changes), leave their `changelog.md` empty or unchanged.

**Formatting rules for per-module changelogs:**
- Each entry starts with `- ` followed by the type prefix and colon
- Use `fix:`, `feat:`, `hotfix:`, or `chore:` prefixes
- Breaking changes get a `<Note>` or `<Warning>` block indented under the entry
- Keep entries concise — 1 line per change unless a breaking change note is needed

#### transports/changelog.md (Enterprise-Style Format)

The transports changelog uses a categorized format with bold names. Write it using this template:

```markdown
## ✨ Features

- **Feature Name** — Description of the feature
- **Feature Name** — Description of the feature

## 🐞 Fixed

- **Bug Name** — Description of what was fixed
- **Bug Name** — Description of what was fixed
```

**Formatting rules for transports/changelog.md:**
- Use `## ✨ Features` and `## 🐞 Fixed` section headers
- Each entry uses **bold name** followed by em dash (—) and description
- Keep descriptions concise — 1-2 lines max per bullet
- Group related commits into a single bullet point
- Include changes from ALL modules (transports is the top-level summary)
- Breaking changes get a `<Warning>` or `<Note>` block indented under the entry
- Omit sections that have no entries (e.g., if there are no features, skip the Features section)

### Step 6: Update Version Files

Update the `version` file in each module that was bumped:

```bash
echo "{new_version}" > core/version
echo "{new_version}" > framework/version
echo "{new_version}" > transports/version
echo "{new_version}" > plugins/{plugin}/version
```

**Do NOT update go.mod files** — that is handled separately by the developer as part of the release process.

### Step 7: Present Summary

After all files are written, present a summary:

```
## Changelog Written: v{new_transport_version}

### Files Modified:
- transports/changelog.md
- core/changelog.md
- framework/changelog.md
- plugins/{changed_plugins}/changelog.md
- {list of version files updated}

### Version Bumps:
{table of old → new versions}

### Next Steps:
1. Review the changelogs
2. Update go.mod files with new dependency versions
3. Run `go mod tidy` in each module
4. Create the docs/changelogs MDX file and update docs.json manually
5. Tag the release: git tag v{new_transport_version}
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
If the calculated new version already has a changelog file in docs:
```
A changelog for v{version} already exists at docs/changelogs/v{version}.mdx.
Would you like to:
1. Continue anyway (version files and changelog.md will be overwritten)
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
│   ├── version              # Plain text: "1.5.0"
│   ├── changelog.md         # Simple flat-list format
│   └── go.mod
├── framework/
│   ├── version              # Plain text: "1.3.0"
│   ├── changelog.md         # Simple flat-list format
│   └── go.mod
├── plugins/
│   ├── governance/
│   │   ├── version
│   │   └── changelog.md     # Simple flat-list format
│   ├── jsonparser/version
│   ├── litellmcompat/version
│   ├── logging/
│   │   ├── version
│   │   └── changelog.md     # Simple flat-list format
│   ├── maxim/version
│   ├── mocker/version
│   ├── otel/version
│   ├── semanticcache/version
│   └── telemetry/version
├── transports/
│   ├── version              # Plain text: "1.5.0"
│   ├── changelog.md         # Enterprise-style format (✨ Features / 🐞 Fixed)
│   └── go.mod
└── docs/
    ├── changelogs/          # ⚠️ DO NOT TOUCH — MDX files managed separately
    └── docs.json            # ⚠️ DO NOT TOUCH — navigation managed separately
```

## Plugin List (Alphabetical Order)

This is the canonical order for plugins:

1. governance
2. jsonparser
3. litellmcompat
4. logging
5. maxim
6. mocker
7. otel
8. semanticcache
9. telemetry
