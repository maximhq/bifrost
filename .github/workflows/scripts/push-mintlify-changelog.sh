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
---"

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
# Handles both non-empty and empty array forms
route="changelogs/$VERSION"
if ! grep -q "\"$route\"" docs/docs.json; then
  awk -v route="$route" '
    function indent(line){
      x = line
      sub(/[^[:space:]].*$/, "", x)
      return x
    }
    $0 ~ /"tab": "Changelogs"/ { in_tab=1 }
    in_tab && $0 ~ /"pages": \[\]/ {
      ind = indent($0)
      print ind "\"pages\": ["
      print ind "  \"" route "\""
      print ind "]"
      fixing_empty=1
      in_tab=0
      next
    }
    in_tab && $0 ~ /"pages": \[/ {
      print
      ind = indent($0)
      print ind "  \"" route "\"," 
      in_tab=0
      next
    }
    fixing_empty && $0 ~ /^[[:space:]]*"changelogs\/[^"]+",?$/ {
      fixing_empty=0
      next
    }
    { print }
  ' docs/docs.json > docs/docs.json.tmp && mv docs/docs.json.tmp docs/docs.json
fi

# Pulling again before committing
git pull origin main
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
git push origin main
