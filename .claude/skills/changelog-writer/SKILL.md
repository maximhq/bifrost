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

To understand the previous release state, use the latest released tags (do NOT use docs/changelogs file mtimes — that directory also holds helm-, cli-, edge-, ent-, and prerelease files):

```bash
for prefix in core framework transports; do
  git tag -l "$prefix/v*" --sort=-v:refname | head -1
done
```

If a docs changelog for that transport version exists (`docs/changelogs/v<version>.mdx`), read it to know the previous versions of all modules.

### Step 2: Identify Changes Since Last Release

**The base for change detection is always determined per module from git — never from docs/changelogs filenames or file mtimes.** (docs/changelogs contains helm-, cli-, edge-, ent-, and prerelease mdx files, so "newest mdx" does not identify the last release.)

For each module, resolve the base commit in this priority order:

```bash
# 1. Latest released tag for the module, by version sort (NOT mtime, NOT alphabetical):
TAG=$(git tag -l "core/v*" --sort=-v:refname | head -1)   # framework/v*, transports/v*, plugins tags likewise

# 2. Module tags may be cut on release branches. If the tag is not an ancestor
#    of HEAD, fall back to the last commit that modified the module's version
#    file on the current branch:
if git merge-base --is-ancestor "$TAG" HEAD; then
  BASE=$TAG
else
  BASE=$(git log -1 --format=%H -- core/version)
fi
echo "core base: $BASE"
```

Then apply the hard rule: **if `git diff ${BASE}..HEAD -- <module>/` is non-empty (ignoring only the module's own `changelog.md` and `version` files), that module MUST get a version bump.** No exceptions, no heuristics.

```bash
# Changes in core
git diff --name-only ${BASE}..HEAD -- core/ ':(exclude)core/changelog.md' ':(exclude)core/version'

# Changes in framework
git diff --name-only ${BASE}..HEAD -- framework/ ':(exclude)framework/changelog.md' ':(exclude)framework/version'

# Changes in each plugin
for d in plugins/*/; do
  CHANGES=$(git diff --name-only ${BASE}..HEAD -- "$d" ":(exclude)${d}changelog.md" ":(exclude)${d}version" | wc -l)
  if [ "$CHANGES" -gt 0 ]; then
    echo "$(basename $d): $CHANGES files changed"
  fi
done

# Changes in transports
git diff --name-only ${BASE}..HEAD -- transports/ ':(exclude)transports/changelog.md' ':(exclude)transports/version'
```

List the commits in the window for changelog writing:

```bash
git log ${BASE}..HEAD --oneline --no-merges -- <module>/
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

#### Collect Closed GitHub Issues

Gather **every** GitHub issue closed in this release so they can be listed in the `## 🐙 Closed GitHub Issues` section of `transports/changelog.md`. Each issue MUST be rendered as a markdown link.

For each PR in the release window, query its linked closing issues (this catches `Closes #N` / `Fixes #N` links even when the commit subject doesn't mention them):

```bash
# For each PR number in the release window:
gh api graphql -f query="query{repository(owner:\"maximhq\",name:\"bifrost\"){pullRequest(number:PR_NUMBER){closingIssuesReferences(first:100){nodes{number title}}}}}" \
  --jq '.data.repository.pullRequest.closingIssuesReferences.nodes[]? | "#\(.number)\t\(.title)"'
```

Also grep commit bodies for closing keywords as a fallback (some issues are linked only in the message text):

```bash
git log ${BASE}..HEAD --no-merges --pretty=format:"%B" | grep -ioE "(close[sd]?|fix(e[sd])?|resolve[sd]?) +#[0-9]+" | sort -u
```

Merge both sources and **deduplicate by issue number** before confirming/rendering (e.g. collect all `#N` values and `sort -un`), so an issue linked via both `closingIssuesReferences` and a commit-body keyword appears only once.

Confirm each issue's state and final title before listing it:

```bash
gh api repos/maximhq/bifrost/issues/<ISSUE_NUMBER> --jq '"#\(.number) [\(.state)] \(.title)"'
```

Render every closed issue as a markdown link in ascending issue-number order:

```markdown
- [#3795](https://github.com/maximhq/bifrost/issues/3795) — MCP tools fail with Bedrock provider in v1.5.0
```

If an issue's title is generic or unhelpful (e.g. just `[Bug Report]`), read the issue body and write a short, specific description instead.

**Present the draft entries to the user for review before writing files.**

#### Per-Module changelog.md (core, framework, plugins)

Write simple flat-list entries to each module's `changelog.md`:

```markdown
- fix: description of what was fixed
- feat: description of new feature
- hotfix: description of urgent fix
```

For modules with only cascading bumps (no code changes), add a `chore:` entry describing which upstream dependencies were bumped. **No changelog should ever be left empty.** Example:

```markdown
- chore: upgraded core to v1.5.0 and framework to v1.3.0
```

If only one upstream changed, mention just that one (e.g., `- chore: upgraded core to v1.5.0`). Always use the actual new versions of the upstream modules.

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

## 🐙 Closed GitHub Issues

- [#1234](https://github.com/maximhq/bifrost/issues/1234) — Issue title
- [#1235](https://github.com/maximhq/bifrost/issues/1235) — Issue title
```

**Formatting rules for transports/changelog.md:**
- Use `## ✨ Features` and `## 🐞 Fixed` section headers
- Each entry uses **bold name** followed by em dash (—) and description
- Keep descriptions concise — 1-2 lines max per bullet
- Group related commits into a single bullet point
- Include changes from ALL modules (transports is the top-level summary)
- Breaking changes get a `<Warning>` or `<Note>` block indented under the entry
- Omit sections that have no entries (e.g., if there are no features, skip the Features section)
- If the release has only cascading bumps and no meaningful features or fixes, add a `## 🔧 Maintenance` section with an entry like: `- **Dependency Upgrades** — Bumped core to v1.5.0 and framework to v1.3.0 across all modules`
- Add a `## 🐙 Closed GitHub Issues` section listing **every** issue closed in this release (see "Collect Closed GitHub Issues" below). Each entry MUST be a markdown link to the issue: `- [#NUMBER](https://github.com/maximhq/bifrost/issues/NUMBER) — Issue title`. Omit the section only if no issues were closed.

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
│   ├── changelog.md         # Enterprise-style format (✨ Features / 🐞 Fixed / 🐙 Closed GitHub Issues)
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
