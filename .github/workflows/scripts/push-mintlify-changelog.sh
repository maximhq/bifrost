#!/usr/bin/env bash

VERSION=$1

if [ -z "$VERSION" ]; then
  echo "Usage: $0 <version>"
  echo "Example: $0 1.2.0"
  exit 1
fi

VERSION="v$VERSION"

# Check if this page already exists in docs/changelogs/
if [ -f "docs/changelogs/$VERSION.mdx" ]; then
  echo "✅ Changelog for $VERSION already exists"
  exit 0
fi

# Source changelog utilities
source "$(dirname "$0")/changelog-utils.sh"

# Get current date
CURRENT_DATE=$(date +"%Y-%m-%d")

# Preparing changelog file
CHANGELOG_BODY="---
title: \"$VERSION\"
description: \"$VERSION changelog - $CURRENT_DATE\"
---
<Tabs>
  <Tab title=\"NPX\">
    \`\`\`bash
    npx -y @maximhq/bifrost --transport-version $VERSION
    \`\`\`
  </Tab>
  <Tab title=\"Docker\">
    \`\`\`bash
    docker pull maximhq/bifrost:$VERSION
    docker run -p 8080:8080 maximhq/bifrost:$VERSION
    \`\`\`
  </Tab>
</Tabs>
"

# Test reports table.
#
# Sits directly after the upgrade instructions and before the per-module
# changelog sections: what shipped is only half the question, and "was it
# verified, against which CLI versions" is the other half.
#
# The URLs are derived from the version alone - test-core and test-cli-harness
# publish to test-reports/v<version>/<label>/ (see
# upload-test-reports-to-r2.sh), so no job outputs have to be threaded through
# the release graph to get here.
#
# TEST_REPORTS_BASE_URL is the public origin serving the R2 bucket. It is
# deliberately not hardcoded: publishing a guessed domain into public release
# notes would ship dead links on every release. With it unset the table is
# skipped entirely rather than emitted with broken hrefs.
# One row per CLI harness leg that actually ran.
#
# The legs are not always latest/latest-1/latest-2. When npm resolution fails,
# resolve-cli-versions.sh degrades to a single leg labelled "pinned", and
# test-cli-harness then publishes to test-reports/v<version>/cli-harness-pinned/.
# Hardcoding the three normal labels would ship three links to objects that do
# not exist in exactly that run - the same dead-link outcome the
# TEST_REPORTS_BASE_URL guard below exists to avoid. So the labels are passed in
# from the resolve step's matrix, and the rows are generated from them.
#
# The default keeps the script usable standalone; CI always supplies the real
# labels.
cli_harness_rows () {
  local base=$1
  # ${VAR:-default} treats an explicitly EMPTY value as unset. That is wrong
  # here: empty means the resolver produced no legs (it was skipped), and
  # falling back to the three standard labels would publish links to reports
  # that never ran. Only a genuinely unset variable takes the default, which is
  # what keeps the script usable standalone.
  local labels
  if [ -n "${CLI_HARNESS_LABELS+set}" ]; then
    labels=$CLI_HARNESS_LABELS
  else
    labels="latest latest-1 latest-2"
  fi
  local label what n
  for label in $labels; do
    case "$label" in
      latest)
        what="Claude Code, Codex and OpenCode at their newest releases"
        ;;
      latest-1)
        what="The same suite one CLI release back"
        ;;
      latest-*)
        n=${label#latest-}
        what="The same suite $n CLI releases back"
        ;;
      pinned)
        what="Claude Code, Codex and OpenCode at pinned fallback versions - npm resolution failed, so three-version coverage was NOT in effect"
        ;;
      *)
        what="The same suite at $label"
        ;;
    esac
    echo "| CLI harness ($label) | $what | [conversations]($base/cli-harness-$label/index.html) |"
  done
}

if [ -n "${TEST_REPORTS_BASE_URL:-}" ]; then
  REPORTS_BASE="${TEST_REPORTS_BASE_URL%/}/test-reports/$VERSION"
  CLI_HARNESS_ROWS=$(cli_harness_rows "$REPORTS_BASE")
  # The claim below has to match the legs that ran, for the same reason the rows
  # do: on the fallback path there is one pinned leg, not three CLI releases.
  # Keyed off the LABELS, not the row count. A row count of one only means one
  # leg ran; it says nothing about which, so a manual CLI_HARNESS_LABELS=latest
  # run would have been described as "pinned".
  case "${CLI_HARNESS_LABELS-latest latest-1 latest-2}" in
    "")
      CLI_HARNESS_INTRO="The CLI harness did not run for this release."
      ;;
    *pinned*)
      CLI_HARNESS_INTRO="The CLI harness ran a single pinned leg for this release - npm version resolution failed, so three-version coverage was not in effect."
      ;;
    *" "*)
      CLI_HARNESS_INTRO="The CLI harness runs against the three most recent releases of each coding CLI, so the table below is also the compatibility record for this version."
      ;;
    *)
      CLI_HARNESS_INTRO="The CLI harness ran a single leg (\`${CLI_HARNESS_LABELS}\`) for this release."
      ;;
  esac
  CHANGELOG_BODY+="
## Test reports

Every release is gated on the provider harness and the CLI harness.
$CLI_HARNESS_INTRO

| Suite | What it covers | Report |
| --- | --- | --- |
| Provider harness | Every provider × modality through a live gateway | [requests]($REPORTS_BASE/provider-harness/index.html) · [failure breakdown]($REPORTS_BASE/provider-harness/harness-failures.md) |
$CLI_HARNESS_ROWS

The CLI harness reports include the full conversation for every scenario - each
prompt sent and each response received, turn by turn.
"
else
  echo "⚠️  TEST_REPORTS_BASE_URL unset - omitting the test reports table from $VERSION"
fi

# Array to track cleaned changelog files
CLEANED_CHANGELOG_FILES=()

# Helper to append a section if changelog file exists and is non-empty
append_section () {
  label=$1
  path=$2
  if [ -f "$path" ]; then
    # Get changelog content
    content=$(get_file_content "$path")
    # If changelog is empty, skip
    if [ -z "$content" ]; then
      echo "❌ Changelog is empty"
      return
    fi
    # Remove /changelog.md from the path and add /version
    version_file_path="${path%/changelog.md}/version"
    # Get version content
    version_body=$(get_file_content "$version_file_path")
    # Build the changelog section
    CHANGELOG_BODY+=$'\n'"<Update label=\"$label\" description=\"$version_body\">"$'\n'"$content"$'\n\n'"</Update>"
    # Clear the changelog file after processing
    printf '' > "$path"
    # Track this file for git commit
    CLEANED_CHANGELOG_FILES+=("$path")
  fi
}

# HTTP changelog
append_section "Bifrost(HTTP)" transports/changelog.md

# Core changelog
append_section "Core" core/changelog.md

# Framework changelog
append_section "Framework" framework/changelog.md

# Plugins changelogs
for plugin in plugins/*; do
  name=$(basename "$plugin")
  append_section "$name" "$plugin/changelog.md"
done

# Write to file
mkdir -p docs/changelogs
echo "$CHANGELOG_BODY" > docs/changelogs/$VERSION.mdx

# Update docs.json to include this new changelog route in the Changelogs tab pages array
# Uses month-based grouping: current month at top level, older months in groups
# Automatically reorganizes when month changes
route="changelogs/$VERSION"
if ! grep -q "\"$route\"" docs/docs.json; then
  node -e "
    const fs = require('fs');
    const docs = JSON.parse(fs.readFileSync('docs/docs.json', 'utf8'));

    // Semantic version comparison function
    // Extracts version from route/filename and compares in descending order (newest first)
    function compareVersionsDesc(a, b) {
      // Extract route string from string or object
      const routeA = typeof a === 'string' ? a : '';
      const routeB = typeof b === 'string' ? b : '';

      // Extract version from route (e.g., 'changelogs/v1.3.34' -> 'v1.3.34')
      const versionA = routeA.split('/').pop() || '';
      const versionB = routeB.split('/').pop() || '';

      // Remove 'v' prefix and split into parts
      const partsA = versionA.replace(/^v/, '').split(/[.-]/).map(p => {
        const num = parseInt(p, 10);
        return isNaN(num) ? p : num;
      });
      const partsB = versionB.replace(/^v/, '').split(/[.-]/).map(p => {
        const num = parseInt(p, 10);
        return isNaN(num) ? p : num;
      });

      // Compare each part (major, minor, patch, pre-release, etc.)
      const maxLength = Math.max(partsA.length, partsB.length);
      for (let i = 0; i < maxLength; i++) {
        // Release vs prerelease: release is newer (no suffix > has suffix)
        if (partsA[i] === undefined && partsB[i] !== undefined) {
          return -1; // A (release) comes first in descending order
        }
        if (partsB[i] === undefined && partsA[i] !== undefined) {
          return 1; // B (release) comes first in descending order
        }

        const partA = partsA[i];
        const partB = partsB[i];

        // If both are numbers, compare numerically
        if (typeof partA === 'number' && typeof partB === 'number') {
          if (partA !== partB) {
            return partB - partA; // Descending order
          }
        } else {
          // Handle prerelease strings with numeric suffixes (e.g., 'prerelease10')
          const strA = String(partA);
          const strB = String(partB);
          const matchA = strA.match(/^([a-zA-Z]+)(\\d+)$/);
          const matchB = strB.match(/^([a-zA-Z]+)(\\d+)$/);

          if (matchA && matchB && matchA[1] === matchB[1]) {
            // Same prefix, compare numbers numerically
            const numA = parseInt(matchA[2], 10);
            const numB = parseInt(matchB[2], 10);
            if (numA !== numB) {
              return numB - numA; // Descending order
            }
          } else if (strA !== strB) {
            return strB.localeCompare(strA); // Descending order
          }
        }
      }

      return 0; // Equal
    }

    // Sort a pages array by semver (descending)
    function sortPagesBySemver(pages) {
      return pages.slice().sort(compareVersionsDesc);
    }

    // Get current month/year
    const releaseDate = new Date('$CURRENT_DATE');
    const currentDate = new Date();
    const releaseMonthYear = releaseDate.toLocaleDateString('en-US', { year: 'numeric', month: 'long' });
    const currentMonthYear = currentDate.toLocaleDateString('en-US', { year: 'numeric', month: 'long' });

    // Find the Changelogs tab
    const changelogsTab = docs.navigation.tabs.find(tab => tab.tab === 'Changelogs');
    if (!changelogsTab) {
      console.error('Changelogs tab not found');
      process.exit(1);
    }

    // Find the Open Source menu item
    const openSourceItem = changelogsTab.menu?.find(item => item.item === 'Open Source');
    if (!openSourceItem) {
      console.error('Open Source menu item not found in Changelogs tab');
      process.exit(1);
    }

    // Get all top-level entries and existing groups
    const topLevelEntries = openSourceItem.pages.filter(p => typeof p === 'string');
    const existingGroups = openSourceItem.pages.filter(p => typeof p === 'object');

    // Check if we need to group existing top-level entries
    if (topLevelEntries.length > 0) {
      // Get the month of the first top-level entry (they should all be from same month)
      const firstEntryPath = topLevelEntries[0].replace('changelogs/', '') + '.mdx';
      const firstEntryFile = 'docs/changelogs/' + firstEntryPath;

      let topLevelMonth = null;
      try {
        const content = fs.readFileSync(firstEntryFile, 'utf8');
        const descMatch = content.match(/description:\\s*\"[^\"]*?(\\d{4}-\\d{2}-\\d{2})[^\"]*\"/);
        if (descMatch) {
          const entryDate = new Date(descMatch[1]);
          topLevelMonth = entryDate.toLocaleDateString('en-US', { year: 'numeric', month: 'long' });
        }
      } catch (e) {
        console.log(\`Warning: Could not read entry file \${firstEntryFile}: \${e.message}\`);
      }

      // Only group if the month has changed
      if (topLevelMonth && topLevelMonth !== releaseMonthYear) {
        console.log(\`📦 Month changed from \${topLevelMonth} to \${releaseMonthYear}\`);
        console.log(\`📦 Grouping \${topLevelEntries.length} top-level entries into \${topLevelMonth} group...\`);

        // Create a group for all existing top-level entries
        const previousMonthGroup = {
          group: topLevelMonth,
          pages: sortPagesBySemver(topLevelEntries)
        };

        // Add this group at the top of existing groups
        existingGroups.unshift(previousMonthGroup);
        console.log(\`✅ Created \${topLevelMonth} group with \${topLevelEntries.length} entries (sorted)\`);

        // Clear top-level entries (they're now in the group)
        openSourceItem.pages = existingGroups;
      } else {
        console.log(\`📋 Same month (\${releaseMonthYear}), keeping existing top-level entries\`);
        // Keep existing structure (top-level entries + groups)
        openSourceItem.pages = [...topLevelEntries, ...existingGroups];
      }
    }

    const newRoute = '$route';

    // Add the new changelog at the top level
    openSourceItem.pages.unshift(newRoute);
    console.log(\`✅ Added \${newRoute} to top level\`);

    // Sort the top-level pages array by semver
    const topLevelPages = openSourceItem.pages.filter(p => typeof p === 'string');
    const groupPages = openSourceItem.pages.filter(p => typeof p === 'object');

    if (topLevelPages.length > 0) {
      const sortedTopLevel = sortPagesBySemver(topLevelPages);
      openSourceItem.pages = [...sortedTopLevel, ...groupPages];
      console.log(\`✅ Sorted \${topLevelPages.length} top-level pages by semver\`);
    }

    // Sort each group's pages by semver
    for (const group of groupPages) {
      if (group.pages && Array.isArray(group.pages)) {
        group.pages = sortPagesBySemver(group.pages);
      }
    }

    fs.writeFileSync('docs/docs.json', JSON.stringify(docs, null, 2) + '\n');
    console.log('✅ Updated docs.json');
  "
fi

# Pulling again before committing
CURRENT_BRANCH="$(git rev-parse --abbrev-ref HEAD)"
if [ "$CURRENT_BRANCH" = "HEAD" ]; then
  # In detached HEAD state (common in CI), use GITHUB_REF_NAME or default to main
  CURRENT_BRANCH="${GITHUB_REF_NAME:-main}"
fi

echo "Pulling latest changes from origin/$CURRENT_BRANCH..."
if ! git pull origin "$CURRENT_BRANCH"; then
  echo "❌ Error: git pull origin $CURRENT_BRANCH failed"
  exit 1
fi

# Commit and push changes
git add docs/changelogs/$VERSION.mdx
git add docs/docs.json
# Add all cleaned changelog files
for file in "${CLEANED_CHANGELOG_FILES[@]}"; do
  git add "$file"
done
git config user.name "github-actions[bot]"
git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
git commit -m "Adds changelog for $VERSION --skip-ci"
git push origin "$CURRENT_BRANCH"
