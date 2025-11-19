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
        const partA = partsA[i] !== undefined ? partsA[i] : 0;
        const partB = partsB[i] !== undefined ? partsB[i] : 0;
        
        // If both are numbers, compare numerically
        if (typeof partA === 'number' && typeof partB === 'number') {
          if (partA !== partB) {
            return partB - partA; // Descending order
          }
        } else {
          // String comparison for pre-release versions
          const strA = String(partA);
          const strB = String(partB);
          if (strA !== strB) {
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
    
    // Get all top-level entries and existing groups
    const topLevelEntries = changelogsTab.pages.filter(p => typeof p === 'string');
    const existingGroups = changelogsTab.pages.filter(p => typeof p === 'object');
    
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
        changelogsTab.pages = existingGroups;
      } else {
        console.log(\`📋 Same month (\${releaseMonthYear}), keeping existing top-level entries\`);
        // Keep existing structure (top-level entries + groups)
        changelogsTab.pages = [...topLevelEntries, ...existingGroups];
      }
    }
    
    const newRoute = '$route';
    
    // Add the new changelog at the top level
    changelogsTab.pages.unshift(newRoute);
    console.log(\`✅ Added \${newRoute} to top level\`);
    
    // Sort the top-level pages array by semver
    const topLevelPages = changelogsTab.pages.filter(p => typeof p === 'string');
    const groupPages = changelogsTab.pages.filter(p => typeof p === 'object');
    
    if (topLevelPages.length > 0) {
      const sortedTopLevel = sortPagesBySemver(topLevelPages);
      changelogsTab.pages = [...sortedTopLevel, ...groupPages];
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
git pull origin "$CURRENT_BRANCH"
# Commit and push changes
git add docs/changelogs/$VERSION.mdx
git add docs/docs.json
# Add all cleaned changelog files
for file in "${CLEANED_CHANGELOG_FILES[@]}"; do
  git add "$file"
done
git config user.name "github-actions[bot]"
git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
git commit -m "Adds changelog for $VERSION --skip-pipeline"
git push origin "$CURRENT_BRANCH"
