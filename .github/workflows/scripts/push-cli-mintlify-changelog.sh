#!/usr/bin/env bash

VERSION=$1

if [ -z "$VERSION" ]; then
  echo "Usage: $0 <version>"
  echo "Example: $0 0.10.0"
  exit 1
fi

VERSION_WITH_PREFIX="cli-v$VERSION"

# Check if this page already exists in docs/changelogs/
if [ -f "docs/changelogs/$VERSION_WITH_PREFIX.mdx" ]; then
  echo "✅ Changelog for $VERSION_WITH_PREFIX already exists"
  exit 0
fi

# Source changelog utilities
source "$(dirname "$0")/changelog-utils.sh"

# Get current date
CURRENT_DATE=$(date +"%Y-%m-%d")

# Get changelog content from cli/changelog.md
CLI_CHANGELOG_PATH="cli/changelog.md"
if [ ! -f "$CLI_CHANGELOG_PATH" ]; then
  echo "❌ CLI changelog not found at $CLI_CHANGELOG_PATH"
  exit 1
fi

CHANGELOG_CONTENT=$(get_file_content "$CLI_CHANGELOG_PATH")
if [ -z "$CHANGELOG_CONTENT" ]; then
  echo "❌ CLI changelog is empty"
  exit 1
fi

# Preparing changelog file
CHANGELOG_BODY="---
title: \"v$VERSION\"
description: \"v$VERSION changelog - $CURRENT_DATE\"
---

<Update label=\"Bifrost CLI\" description=\"v$VERSION\">
$CHANGELOG_CONTENT

</Update>
"

# Write to file
mkdir -p docs/changelogs
echo "$CHANGELOG_BODY" > "docs/changelogs/$VERSION_WITH_PREFIX.mdx"
echo "✅ Created docs/changelogs/$VERSION_WITH_PREFIX.mdx"

# Clear the CLI changelog file after processing
printf '' > "$CLI_CHANGELOG_PATH"
echo "✅ Cleared $CLI_CHANGELOG_PATH"

# Update docs.json to include this new changelog route in the Bifrost CLI menu
route="changelogs/$VERSION_WITH_PREFIX"
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
      
      // Extract version from route (e.g., 'changelogs/cli-v0.10.0' -> 'cli-v0.10.0')
      const versionA = routeA.split('/').pop() || '';
      const versionB = routeB.split('/').pop() || '';
      
      // Remove 'cli-v' or 'v' prefix and split into parts
      const partsA = versionA.replace(/^(cli-)?v/, '').split(/[.-]/).map(p => {
        const num = parseInt(p, 10);
        return isNaN(num) ? p : num;
      });
      const partsB = versionB.replace(/^(cli-)?v/, '').split(/[.-]/).map(p => {
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
    
    // Find the Bifrost CLI menu item
    const cliMenuItem = changelogsTab.menu.find(item => item.item === 'Bifrost CLI');
    if (!cliMenuItem) {
      console.error('Bifrost CLI menu item not found');
      process.exit(1);
    }
    
    // Get all top-level entries and existing groups
    const topLevelEntries = cliMenuItem.pages.filter(p => typeof p === 'string');
    const existingGroups = cliMenuItem.pages.filter(p => typeof p === 'object');
    
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
        cliMenuItem.pages = existingGroups;
      } else {
        console.log(\`📋 Same month (\${releaseMonthYear}), keeping existing top-level entries\`);
        // Keep existing structure (top-level entries + groups)
        cliMenuItem.pages = [...topLevelEntries, ...existingGroups];
      }
    }
    
    const newRoute = '$route';
    
    // Add the new changelog at the top level
    cliMenuItem.pages.unshift(newRoute);
    console.log(\`✅ Added \${newRoute} to top level\`);
    
    // Sort the top-level pages array by semver
    const topLevelPages = cliMenuItem.pages.filter(p => typeof p === 'string');
    const groupPages = cliMenuItem.pages.filter(p => typeof p === 'object');
    
    if (topLevelPages.length > 0) {
      const sortedTopLevel = sortPagesBySemver(topLevelPages);
      cliMenuItem.pages = [...sortedTopLevel, ...groupPages];
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
git add "docs/changelogs/$VERSION_WITH_PREFIX.mdx"
git add docs/docs.json
git add "$CLI_CHANGELOG_PATH"
git config user.name "github-actions[bot]"
git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
git commit -m "Adds CLI changelog for v$VERSION --skip-pipeline"
git push origin "$CURRENT_BRANCH"

echo "✅ Pushed CLI changelog for v$VERSION"
