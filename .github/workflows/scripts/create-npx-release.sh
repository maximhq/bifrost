#!/bin/bash
set -euo pipefail

# Create GitHub release for NPX package
# Usage: ./create-npx-release.sh <version> <full-tag>

VERSION="$1"
FULL_TAG="$2"

TITLE="NPX Package v$VERSION"

# Create release body
BODY="## NPX Package Release

### 📦 NPX Package v$VERSION

The Bifrost CLI is now available on npm!

### Installation

\`\`\`bash
# Install globally
npm install -g @maximhq/bifrost

# Or use with npx (no installation needed)
npx @maximhq/bifrost --help
\`\`\`

### Usage

\`\`\`bash
# Start Bifrost HTTP server
bifrost

# Use specific transport version
bifrost --transport-version v1.2.3

# Get help
bifrost --help
\`\`\`

### Links

- 📦 [View on npm](https://www.npmjs.com/package/@maximhq/bifrost)
- 📚 [Documentation](https://github.com/maximhq/bifrost)
- 🐛 [Report Issues](https://github.com/maximhq/bifrost/issues)

### What's New

This NPX package provides a convenient way to run Bifrost without manual binary downloads. The CLI automatically:

- Detects your platform and architecture
- Downloads the appropriate binary
- Supports version pinning with \`--transport-version\`
- Provides progress indicators for downloads

---
_This release was automatically created from tag \`$FULL_TAG\`_"

# Create release
echo "🎉 Creating GitHub release for $TITLE..."
gh release create "$FULL_TAG" \
  --title "$TITLE" \
  --notes "$BODY" \
  --latest=false

echo "✅ GitHub release created: $FULL_TAG"
