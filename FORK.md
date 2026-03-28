# Bifrost Fork -- Stragix Innovations

## Fork Origin

- **Upstream:** [maximhq/bifrost](https://github.com/maximhq/bifrost) (Apache 2.0)
- **Fork:** [stragix-innovations/bifrost](https://github.com/stragix-innovations/bifrost)
- **Forked at:** v1.4.16 (2026-03-28)

## What We Changed

> **Note:** This section documents the fork's intended final state after Phase 2 completes.
> Files listed below may not all exist yet if Phase 2 is still in progress.

### New Files (no upstream conflict risk)
- `framework/oidc/` -- OIDC discovery, JWKS caching, JWT validation, claims extraction
- `transports/bifrost-http/handlers/oidc.go` -- OIDC middleware for fasthttp chain
- `.github/workflows/docker-build.yml` -- Multi-arch Docker image build + push to GHCR
- `.github/workflows/upstream-check.yml` -- Weekly upstream drift detection

### Modified Files (potential merge conflicts)
- `transports/bifrost-http/server/server.go` -- OIDC middleware insertion in Bootstrap()
- `transports/go.mod` / `core/go.mod` / `framework/go.mod` -- golang-jwt v5.3.1 bump, go-oidc dep

### Import Path Strategy

We keep `maximhq/bifrost` import paths in ALL Go source files. This is critical for minimizing merge conflicts with upstream. For local development, use `go.work` (see setup-go-workspace.sh). The Dockerfile uses `GOWORK=off`.

**Never change import paths from `maximhq/bifrost` to `stragix-innovations/bifrost`.**

## Upstream Merge Process

### Prerequisites
- `upstream` remote configured: `git remote add upstream https://github.com/maximhq/bifrost.git`
- Clean working tree

### Steps

1. Fetch upstream changes:
   ```bash
   git fetch upstream main
   ```

2. Create a merge branch:
   ```bash
   git checkout -b merge/upstream-$(date +%Y%m%d) main
   ```

3. Attempt merge:
   ```bash
   git merge upstream/main --no-ff
   ```

4. If conflicts arise:
   - Our files (`framework/oidc/`, `handlers/oidc.go`): Keep ours (they don't exist upstream)
   - `server.go`: Manually resolve -- our changes are the OIDC middleware insertion block only
   - `go.mod`/`go.sum`: Accept upstream, then re-add our dependencies (`go-oidc`, `golang-jwt` bump)
   - Run `go mod tidy` in each module after resolving go.mod conflicts

5. Verify build:
   ```bash
   source .github/workflows/scripts/setup-go-workspace.sh
   go build ./transports/bifrost-http/...
   go test ./transports/bifrost-http/handlers/... ./framework/oidc/...
   ```

6. Create PR targeting `main`, title: `merge: upstream bifrost $(date +%Y-%m-%d)`

### Automated Drift Detection

The `.github/workflows/upstream-check.yml` workflow runs weekly (Monday 9am UTC) and on manual trigger. It fetches upstream and attempts a test merge. If conflicts are detected, it creates a GitHub warning annotation.

## Security Patches

When upstream releases a security fix:
1. Check if the fix is in files we modified (likely `go.mod` only)
2. If yes: merge immediately following the process above
3. If no (new files only): merge at next scheduled merge
