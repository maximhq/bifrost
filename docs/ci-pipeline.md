# Bifrost CI/CD Pipeline

This document provides comprehensive documentation for the Bifrost CI/CD pipeline, a modular, script-driven system that automates builds, deployments, and releases across the entire Bifrost ecosystem.

## Overview

The Bifrost CI/CD pipeline consists of three specialized workflows that handle different aspects of the release process:

- **Core Dependency Update** (`core-dependency-update.yml`) - Creates PRs when core is tagged, validates builds
- **Transports Release** (`transports-release.yml`) - Builds and releases when dependency updates are merged  
- **Direct Transport Release** (`transports-ci.yml`) - Handles direct transport tag releases

## Architecture

### Script-Driven Design

The pipeline is built around modular Node.js scripts and a bash build script that handle specific responsibilities. This approach provides:

- **Testability**: Each script can be run and tested locally
- **Maintainability**: Logic is centralized and easy to update
- **Reusability**: Scripts work across different workflows and environments
- **Clarity**: Workflows are clean and focus on orchestration

### Core Scripts

#### Version Management

- **`extract-version.mjs`** - Extracts and validates versions from Git tags
- **`manage-versions.mjs`** - Handles dependency updates and version increments

#### Build & Distribution

- **`go-executable-build.sh`** - Cross-compiles Go binaries for multiple platforms
- **`upload-builds.mjs`** - Distributes Go binaries to S3

#### Operations

- **`git-operations.mjs`** - Manages Git operations (commit, tag, push, PR creation)
- **`run-pipeline.mjs`** - Orchestrates complete pipeline workflows

## Workflow Triggers & Behavior

### Core Library Releases (`core/v*` tags)

**Trigger**: Pushing tags like `core/v1.2.3`

**Workflow**:

1. **Core Dependency Update** workflow creates a new branch and updates `transports/go.mod`
2. Validates that builds succeed with the new dependency
3. Creates a pull request with `--trigger-release` flag and auto-merge (if builds pass)
4. When PR is merged, **Transports Release** workflow triggers automatically due to the flag
5. Creates transport tag, builds binaries, uploads to S3, and pushes to Docker Hub

**Use Case**: Core library updates, API changes, new features

```bash
git tag core/v1.2.3
git push origin core/v1.2.3
```

### Direct Transport Releases (`transports/v*` tags)

**Trigger**: Pushing tags like `transports/v1.2.3`

**Workflow**:

1. **Direct Transport Release** workflow uses existing core dependencies
2. Builds UI static files and Go binaries
3. Uploads to S3 and pushes to Docker Hub

**Use Case**: Transport-specific fixes, configuration changes, hotfixes

```bash
git tag transports/v1.2.3
git push origin transports/v1.2.3
```

### Manual Transport Dependency Changes

When manually updating `transports/go.mod` (adding dependencies, version changes, etc.), you can control whether a transport release is triggered:

**To trigger a release:**
```bash
git commit -m "feat: add new dependency --trigger-release"
git push origin main
```

**Default behavior (no release):**
```bash
git commit -m "chore: minor dependency update"
git push origin main  # No release triggered
```

## Detailed Workflow Documentation

### Core Dependency Update Workflow

**File**: `.github/workflows/core-dependency-update.yml`

**Purpose**: Handle core library updates by creating PRs with build validation

**Steps**:

1. **Extract Core Version**: Get version from the core tag
2. **Create Branch**: Create feature branch for dependency update
3. **Update Dependencies**: Update `transports/go.mod` to new core version
4. **Build Validation**: Test Go build and UI build to ensure compatibility
5. **Create PR**: Auto-merge if builds pass, manual review if builds fail

### Transports Release Workflow

**File**: `.github/workflows/transports-release.yml`

**Purpose**: Release transports when dependency updates are merged to main

**Trigger Control**: Uses commit message flags to control release behavior:
- `--trigger-release`: Required flag to trigger a transport release (default: skip release)

**Steps**:

1. **Flag Check**: Examine commit message for release control flags
2. **Create Tag**: Generate and push new transport version tag (if releasing)
3. **UI Build**: Build static files from `/ui` (`npm ci && npm run build`)
4. **Go Build**: Cross-compile binaries for multiple platforms
5. **Distribution**: Upload binaries to S3 for public download
6. **Docker Build**: Create multi-architecture images with integrated UI

### Direct Transport Release Workflow

**File**: `.github/workflows/transports-ci.yml`

**Purpose**: Handle direct transport releases for hotfixes and minor changes

**Steps**:

1. **Version Management**: Use existing core dependencies
2. **UI Build**: Build static files from current state
3. **Go Build**: Cross-compile binaries for multiple platforms
4. **Distribution**: Upload binaries to S3 and push to Docker Hub

## Version Management Strategy

### Automatic Versioning

- **Transport versions** are automatically incremented (patch level) when core dependency updates are merged
- **Semantic versioning** (`vMAJOR.MINOR.PATCH`) is enforced across all components
- **Tag validation** ensures consistent format and prevents conflicts
- **Build validation** ensures compatibility before creating releases

### Dependency Resolution

| Trigger Type         | Core Version   | Transport Version | Action                    |
| -------------------- | -------------- | ----------------- | ------------------------- |
| `core/v*`            | New (from tag) | Auto-increment    | Create PR with validation |
| PR merge (go.mod)    | Updated        | Auto-increment    | Create tag and release    |
| `transports/v*`      | Current        | From tag          | Direct release            |

### Version Coordination

The pipeline ensures version compatibility through build validation:

- Core updates create PRs with build validation before merging
- Transport releases happen only after successful dependency updates
- Direct transport tags use existing, validated dependencies

## S3 Storage Structure

### Binary Distributions

```text
bifrost/
├── v1.2.3/          # Versioned binary releases
│   ├── windows/
│   ├── darwin/
│   └── linux/
├── latest/           # Always points to newest binaries
│   ├── windows/
│   └── ...
```

## Docker Image Strategy

### Build Process

- **Local Source**: Uses repository source code, not remote packages
- **UI Integration**: Always builds UI from the current repo state as part of the pipeline
- **Multi-Architecture**: Builds for both `linux/amd64` and `linux/arm64`
- **Caching**: Leverages GitHub Actions cache for faster builds

### Image Tags

- **Versioned**: `maximhq/bifrost:v1.2.3`
- **Latest**: `maximhq/bifrost:latest`

### Metadata

Images include comprehensive OCI labels with build information, source links, and version details.

## Local Development & Testing

### Prerequisites

```bash
# Install dependencies
cd ci/scripts
npm ci

# Set up environment variables
export R2_ENDPOINT="https://your-endpoint.r2.cloudflarestorage.com"
export R2_ACCESS_KEY_ID="your-access-key"
export R2_SECRET_ACCESS_KEY="your-secret-key"
```

### Testing Individual Scripts

```bash
cd ci/scripts

# Test version extraction
node extract-version.mjs refs/tags/core/v1.2.3 core

# Test version management
node manage-versions.mjs core v1.2.3

# Test Go build and upload
./go-executable-build.sh bifrost-http ../dist/apps/bifrost ./bifrost-http /path/to/transports
node upload-builds.mjs v1.2.3

# Test Git operations
node git-operations.mjs configure
```

### Testing Complete Pipelines

```bash
cd ci/scripts

# Test core dependency update pipeline
node run-pipeline.mjs core-dependency-update v1.2.3

# Test tag extraction
node run-pipeline.mjs extract-tag refs/tags/core/v1.2.3 core
```

## Environment Configuration

### Required Secrets

#### S3/R2 Storage

- `R2_ENDPOINT` - Cloudflare R2 endpoint URL
- `R2_ACCESS_KEY_ID` - R2 access key ID
- `R2_SECRET_ACCESS_KEY` - R2 secret access key

#### Git Operations

- `GH_TOKEN` - GitHub personal access token with repo and actions permissions

#### Docker Registry

- `DOCKER_USERNAME` - Docker Hub username
- `DOCKER_PASSWORD` - Docker Hub password or access token

### GitHub Actions Context

These variables are automatically available in workflows:

- `GITHUB_REF` - Git reference that triggered the workflow
- `GITHUB_TOKEN` - GitHub token for API operations
- `GITHUB_SHA` - Commit SHA for Docker image labels

## Monitoring & Troubleshooting

### Workflow Monitoring

Each workflow provides detailed logging with emoji indicators:

- 🔧 Core dependency operations
- 🚀 Transport build operations
- 📦 Version management
- 📥/📤 Download/upload operations
- ✅ Success indicators
- ❌ Error indicators

### Common Issues

#### Version Conflicts

- **Symptom**: Tag already exists errors
- **Solution**: Check existing tags, increment appropriately

#### S3 Upload Failures

- **Symptom**: AWS SDK errors during upload
- **Solution**: Verify R2 credentials and endpoint configuration

#### Build Failures

- **Symptom**: Go build errors or missing dependencies
- **Solution**: Check go.mod files and dependency versions

#### Docker Build Issues

- **Symptom**: Docker build context errors
- **Solution**: The multi-stage Dockerfile automatically builds UI files during the Docker build process

### Debug Mode

Enable verbose logging by modifying script calls:

```bash
# Add debug flag to scripts (when implemented)
node script-name.mjs --debug
```

## Performance Optimization

### Caching Strategy

- **Node.js dependencies**: Cached based on package-lock.json
- **Docker builds**: GitHub Actions cache for layers
- **UI builds**: Always built fresh from repo state

### Parallel Execution

- Docker build runs parallel to binary uploads
- Multi-architecture builds use parallel jobs
- Independent script operations can run concurrently

### Resource Management

- Concurrent workflow limits prevent resource conflicts
- Build artifacts are cleaned up automatically
- Incremental version updates minimize rebuild scope

## Security Considerations

### Secret Management

- All sensitive data stored in GitHub Secrets
- Limited scope permissions for tokens
- Regular rotation of access keys recommended

### Build Integrity

- Source code verification through Git SHA tracking
- Signed commits recommended for releases
- Docker images include verification metadata

### Access Control

- Workflow permissions follow principle of least privilege
- Separate read/write permissions for different operations
- Personal access tokens limited to required scopes

## Best Practices

### Release Management

1. **Test locally** before pushing tags
2. **Follow semantic versioning** for all components
3. **Coordinate releases** when multiple components change
4. **Monitor workflows** during critical releases

### Development Workflow

1. **Use feature branches** for development
2. **Test scripts individually** before integration
3. **Validate tag formats** before pushing
4. **Review workflow logs** for issues

### Maintenance

1. **Update dependencies** regularly in scripts
2. **Monitor S3 storage usage** and cleanup old builds
3. **Review and rotate secrets** periodically
4. **Keep documentation current** with pipeline changes
