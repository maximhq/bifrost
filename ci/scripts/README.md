# Bifrost CI Scripts

This directory contains all the scripts that power the Bifrost CI/CD pipeline. These scripts are designed to be modular, testable, and reusable across different workflows.

## Script Overview

### Version Management

#### `extract-version.mjs`

Extracts and validates version information from GitHub refs.

```bash
# Extract core version from tag
node extract-version.mjs refs/tags/core/v2.1.0 core

# Extract transport version
node extract-version.mjs refs/tags/transports/v1.0.0 transports
```

#### `manage-versions.mjs`

Handles version management and dependency updates for the transport layer.

```bash
# Handle core version update (updates go.mod, increments transport version)
node manage-versions.mjs core v1.2.3

# Handle transport version (uses existing dependencies)
node manage-versions.mjs transport transports/v1.3.0
```

### Build and Upload

#### `go-executable-build.sh`

Cross-compiles Go binaries for multiple platforms.

```bash
# Build bifrost-http for all platforms
./go-executable-build.sh bifrost-http ./dist/apps/bifrost ./bifrost-http /path/to/transports
```

#### `upload-builds.mjs`

Uploads Go binary builds to S3.

```bash
# Upload builds (must be run from root, looks for ./dist/apps/bifrost)
node upload-builds.mjs v1.2.3
```

### Git Operations

#### `git-operations.mjs`

Manages all git operations with proper error handling.

```bash
# Configure git for CI
node git-operations.mjs configure

# Create and push a tag
node git-operations.mjs create-tag transports/v1.2.3

# Create a pull request (used by core dependency updates)
node git-operations.mjs create-pr v1.2.3 chore/update-core-v1.2.3 true
```

### Pipeline Orchestration

#### `run-pipeline.mjs`

Master script that orchestrates complete pipeline workflows.

```bash
# Run core dependency update pipeline (creates PR with build validation)
node run-pipeline.mjs core-dependency-update v1.2.3

# Extract tag information
node run-pipeline.mjs extract-tag refs/tags/core/v1.2.3 core
```

## Environment Variables

### Required for S3 Operations

```bash
export R2_ENDPOINT="https://your-r2-endpoint.r2.cloudflarestorage.com"
export R2_ACCESS_KEY_ID="your-r2-access-key"
export R2_SECRET_ACCESS_KEY="your-r2-secret-key"
```

### GitHub Actions Context

These are automatically available in GitHub Actions:

- `GITHUB_REF` - Git reference that triggered the workflow
- `GITHUB_TOKEN` - GitHub token for API operations

## Testing Scripts Locally

### Prerequisites

```bash
# Install dependencies
npm ci

# Ensure you have the required environment variables set
```

### Test Individual Scripts

```bash
# Test version extraction
cd scripts
node extract-version.mjs refs/tags/core/v1.2.3 core

# Test git operations (dry run)
node git-operations.mjs configure

# Test Go build (requires Go and source code)
./go-executable-build.sh bifrost-http ../dist/apps/bifrost ./bifrost-http /path/to/transports

# Test binary upload
node upload-builds.mjs v1.2.3
```

### Test Complete Pipelines

```bash
# Test core dependency update pipeline
cd scripts
node run-pipeline.mjs core-dependency-update v1.2.3

# Test tag extraction
node run-pipeline.mjs extract-tag refs/tags/core/v1.2.3 core
```

## Directory Structure

```text
scripts/
├── README.md                 # This file
├── extract-version.mjs       # Version extraction and validation
├── manage-versions.mjs       # Version management and dependencies
├── git-operations.mjs        # Git operations (commit, tag, push)
├── upload-builds.mjs         # Binary upload to S3
├── go-executable-build.sh    # Go cross-compilation
└── run-pipeline.mjs          # Pipeline orchestration
```

## Error Handling

All scripts include proper error handling and will:

- Exit with code 1 on failure
- Provide descriptive error messages
- Validate required parameters and environment variables
- Include emoji indicators for easy visual parsing

## Integration with Workflows

These scripts are designed to work seamlessly with GitHub Actions:

```yaml
# Example workflow step
- name: Extract version
  id: version
  working-directory: scripts
  run: node extract-version.mjs "${{ github.ref }}" core >> "$GITHUB_OUTPUT"
```

## Best Practices

1. **Always run scripts from the scripts directory** for consistent relative paths
2. **Set required environment variables** before running S3 operations
3. **Test scripts locally** before pushing workflow changes
4. **Use the pipeline orchestrator** for complex operations
5. **Check script outputs** for GitHub Actions integration
