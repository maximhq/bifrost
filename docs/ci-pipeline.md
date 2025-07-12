# Bifrost CI/CD Pipeline

This document provides comprehensive documentation for the Bifrost CI/CD pipeline, a modular, script-driven system that automates builds, deployments, and releases across the entire Bifrost ecosystem.

## Overview

The Bifrost CI/CD pipeline consists of a single main workflow powered by a suite of specialized scripts:

- **Transports CI** (`transports-ci.yml`) - Builds UI static files, Go binaries, manages dependencies, and creates Docker images

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

- **`git-operations.mjs`** - Manages Git operations (commit, tag, push)
- **`run-pipeline.mjs`** - Orchestrates complete pipeline workflows

## Workflow Triggers & Behavior

### Core Library Releases (`core/v*` tags)

**Trigger**: Pushing tags like `core/v1.2.3`

**Workflow**:

1. **Transports CI** updates Go module dependencies to the new core version
2. Builds the UI static files from the current repo state (`/ui`)
3. Builds Go binaries and uploads to S3
4. Creates and pushes new transport tag automatically
5. Builds and pushes Docker image

**Use Case**: Core library updates, API changes, new features

```bash
git tag core/v1.2.3
git push origin core/v1.2.3
```

### Transport Releases (`transports/v*` tags)

**Trigger**: Pushing tags like `transports/v1.2.3`

**Workflow**:

1. **Transports CI** uses existing core version
2. Builds the UI static files from the current repo state (`/ui`)
3. Builds Go binaries and uploads to S3
4. Builds and pushes Docker image

**Use Case**: Transport-specific fixes, configuration changes, hotfixes

```bash
git tag transports/v1.2.3
git push origin transports/v1.2.3
```

## Detailed Workflow Documentation

### Transports CI Workflow

**File**: `.github/workflows/transports-ci.yml`

**Purpose**: Build UI static files, Go binaries, manage dependencies, and create Docker images

**Steps**:

1. **Git Configuration**: Set up automated commit user
2. **Version Management**: Determine versions based on trigger type
3. **UI Build**: Build static files from `/ui` (`npm ci && npm run build`)
4. **Go Build**: Cross-compile binaries for multiple platforms
5. **Distribution**: Upload binaries to S3 for public download
6. **Git Operations**: Commit changes and create tags (when needed)
7. **Docker Build**: Create multi-architecture images with integrated UI

**Outputs**: Transport version, core version for Docker build

## Version Management Strategy

### Automatic Versioning

- **Transport versions** are automatically incremented (patch level) when triggered by core updates
- **Semantic versioning** (`vMAJOR.MINOR.PATCH`) is enforced across all components
- **Tag validation** ensures consistent format and prevents conflicts

### Dependency Resolution

| Trigger Type    | Core Version   | Transport Version |
| --------------- | -------------- | ----------------- |
| `core/v*`       | New (from tag) | Auto-increment    |
| `transports/v*` | Current        | From tag          |

### Version Coordination

The pipeline ensures version compatibility:

- Core updates trigger transport rebuilds with updated dependencies
- Transport tags create releases with current dependency versions

## S3 Storage Structure

### Binary Distributions

```
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
npm install @aws-sdk/client-s3

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

# Test transport pipeline
node run-pipeline.mjs transport-build core v1.2.3
node run-pipeline.mjs transport-build transport transports/v1.2.3
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
- **Solution**: Ensure UI files are built before Docker build

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
