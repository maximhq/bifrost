# --- UI Build Stage: Build the React + Vite frontend ---
  FROM node:25-alpine3.23@sha256:bdf2cca6fe3dabd014ea60163eca3f0f7015fbd5c7ee1b0e9ccb4ced6eb02ef4 AS ui-builder
  WORKDIR /app

  # Apply latest alpine security patches before installing packages
  RUN apk upgrade --no-cache

  # Copy UI package files and install dependencies
  COPY ui/package*.json ./
  RUN npm ci

  # Copy UI source code
  COPY ui/ ./

  # Build UI (skip the copy-build step)
  RUN npm run build-enterprise
  # Skip the copy-build step since we'll copy the files in the Go build stage

  # --- Go Build Stage: Compile the Go binary ---
  FROM golang:1.26.4-alpine3.23@sha256:f23e8b227fb4493eabe03bede4d5a32d04092da71962f1fb79b5f7d1e6c2a17f AS builder
  WORKDIR /app

  # Install dependencies including gcc for CGO and sqlite (with latest security patches)
  RUN apk upgrade --no-cache && \
    apk add --no-cache gcc musl-dev sqlite-dev binutils binutils-gold

  # Set environment for CGO-enabled build (required for go-sqlite3)
  ENV CGO_ENABLED=1 GOOS=linux

  COPY transports/go.mod transports/go.sum ./
  RUN ls
  RUN cat go.mod
  RUN go mod download

  # Copy source code and dependencies
  COPY transports/ ./

  COPY --from=ui-builder /app/out ./bifrost-http/ui

  # Build the binary with CGO enabled and static SQLite linking
  ENV GOWORK=off
  ARG VERSION=unknown
  RUN go build \
    -ldflags="-w -s -X main.Version=v${VERSION} -extldflags '-static'" \
    -a -trimpath \
    -tags "sqlite_static" \
    -o /app/main \
    ./bifrost-http

  # Verify build succeeded
  RUN test -f /app/main || (echo "Build failed" && exit 1)

  # --- Runtime Stage: Minimal runtime image ---
  FROM alpine:3.23.4@sha256:5b10f432ef3da1b8d4c7eb6c487f2f5a8f096bc91145e68878dd4a5019afde11
  WORKDIR /app

  # Install runtime dependencies for CGO-enabled binary
  # musl: C standard library (required for CGO binaries)
  # libgcc: GCC runtime library
  # ca-certificates: For HTTPS connections
  RUN apk upgrade --no-cache && \
    apk add --no-cache musl libgcc ca-certificates zlib

  # Create data directory and set up user
  COPY --from=builder /app/main .
  COPY --from=builder /app/docker-entrypoint.sh .

  # Getting arguments
  ARG ARG_APP_PORT=8080
  ARG ARG_APP_HOST=0.0.0.0
  ARG ARG_LOG_LEVEL=info
  ARG ARG_LOG_STYLE=json
  ARG ARG_APP_DIR=/app/data

  # Environment variables with defaults (can be overridden at runtime)
  ENV APP_PORT=$ARG_APP_PORT \
    APP_HOST=$ARG_APP_HOST \
    LOG_LEVEL=$ARG_LOG_LEVEL \
    LOG_STYLE=$ARG_LOG_STYLE \
    APP_DIR=$ARG_APP_DIR

  # Go runtime performance tuning (override at runtime for your workload)
  # GOGC: GC target percentage. Higher = less frequent GC, more memory usage.
  #       Default: 100. For high-throughput with available memory, try 200-400.
  # GOMEMLIMIT: Soft memory limit for Go runtime. Set to ~90% of container memory limit.
  #             Example: "1800MiB" for a 2GB container, "3600MiB" for 4GB.
  #             When set, Go will be more aggressive about GC as it approaches this limit.
  # Note: GOMAXPROCS is automatically detected from cgroup CPU limits via automaxprocs.
  ENV GOGC="" \
    GOMEMLIMIT=""


  # adduser -D (no -u) on this alpine base assigns the first free UID, which is
  # 1000 on a fresh image — so the numeric USER 1000:0 below resolves to appuser.
  # Guard the group-writable chmod so it never applies when APP_DIR resolves to
  # /app itself (e.g. --build-arg ARG_APP_DIR=/app), which would make the binary
  # group-0-writable.
  RUN mkdir -p "$APP_DIR/logs" && \
    adduser -D -s /bin/sh appuser && \
    chown -R appuser:appuser /app && \
    chown -R appuser:0 "$APP_DIR" && \
    { [ "$APP_DIR" = "/app" ] || chmod -R g=rwX "$APP_DIR"; } && \
    chmod +x /app/docker-entrypoint.sh
  # Numeric USER (uid:gid) so kubelet can verify runAsNonRoot; group 0 grants
  # write access to the group-0-owned data dir under arbitrary OpenShift UIDs.
  USER 1000:0


  # Declare volume for data persistence
  VOLUME ["${APP_DIR}"]
  EXPOSE $APP_PORT

  # Health check for container status monitoring
  # Uses busybox wget (built into alpine) instead of the standalone wget package
  # to avoid CVE-2025-69194; busybox wget supports -q/-O but not --no-verbose/--tries.
  HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:${APP_PORT}/health || exit 1

  # Use entrypoint script that handles volume permissions and argument processing
  ENTRYPOINT ["/app/docker-entrypoint.sh"]
  CMD ["/app/main"]
