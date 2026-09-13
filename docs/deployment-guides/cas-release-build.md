# Building a CAS release with the complete UI

Build only from an isolated checkout containing all approved CAS commits. No deployed config, logs database, credentials, or model files are build inputs. Building these artifacts does not start or deploy a service.

## Frontend

Use Node >=22.12 (validated with Node 24.14.0 on Linux arm64). Install a Node tarball into an independent user directory if the host has no suitable Node; verify it against the release SHASUMS256.txt and prepend its bin directory to PATH. Do not change system Node, proxies, or Docker daemon settings.

```bash
bash scripts/build-cas-ui.sh
python3 scripts/check-cas-schema.py # requires Python jsonschema
```

The actual complete path is `ui/package-lock.json` + `ui/` source → `npm ci` → Vite `ui/out/` → TypeScript check → `npm run copy-build` → `transports/bifrost-http/ui/`. The Go HTTP transport embeds that last directory. Copy the **entire** directory, including index.html, hashed JS/CSS chunks, and public assets; an index.html placeholder or a source checkout is not a UI build. UI assets are architecture independent. The shell script runs the export regression suite and verifies both directories match.

A failed log detail request, including failed CAS hydration, must show `logdetails-load-error` and a retry action, with no detail export button. The list endpoint is a preview and must never be used as an export fallback. Only successful current-ID detail data may reach the detail view. Successful background polling retains the loaded detail; a failed refresh blocks it. Browser acceptance is a separate release gate: test failure, retry success, log navigation, and successful complete JSON export against the integrated backend.

## Local Go modules are mandatory

The normal `transports/Dockerfile` and default `make build` use `GOWORK=off`; they can compile published modules without the local CAS fixes. Do not use them for this release.

Use `transports/Dockerfile.local`, which constructs a workspace containing local core, framework, transports, and every plugin module, builds the complete UI in its Node stage, and embeds it before compilation:

```bash
docker build --platform linux/amd64 -f transports/Dockerfile.local   --build-arg VERSION=cas-release -t bifrost:cas-release .
```

This command only builds an image. Run it on an approved isolated amd64 builder or a builder with approved cross-platform support; it is not authorization to modify production Docker or install emulation. The image pins its Node/Go/Alpine bases and may need registry access.

For a native build, create an untracked go.work with `go work init ./core ./framework ./transports`, then `go work use` every `plugins/*/go.mod` parent. Do not run `go work sync` or `go mod tidy` as part of packaging because they can modify dependency files. Run `bash scripts/check-cas-workspace.sh` before compiling. `LOCAL=1` only preserves a workspace; it does not create or validate one.

host1 is arm64; the target production runtime is amd64 Alpine. A host1 native binary cannot run there. The Go build requires CGO for SQLite; use amd64 musl/static CGO as in Dockerfile.local, then verify ELF architecture/linkage and embedded UI on the resulting binary. A glibc dynamically linked binary is not compatible with the Alpine runtime merely because GOARCH is amd64. The coordinating agent owns the integrated amd64 build and deployment.

## Configuration

`logs_store.content_addressed` accepts enabled, exclude_fields, min_field_bytes, and min_chunk_bytes. Zero/omitted thresholds select runtime defaults (1024 and 256 bytes). Enabled CAS requires SQLite or PostgreSQL and cannot coexist with object_storage. The schema regression command uses synthetic configs only and covers acceptance, unknown fields, invalid types/thresholds, unsupported stores, and conflicting storage modes.
