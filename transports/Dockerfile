# --- UI Build Stage: Build the React + Vite frontend ---
  FROM node:25-alpine3.23@sha256:cf38e1f3c28ac9d81cdc0c51d8220320b3b618780e44ef96a39f76f7dbfef023 AS ui-builder
  WORKDIR /app
  
  # Copy UI package files and install dependencies
  COPY ui/package*.json ./
  RUN npm ci
  
  # Copy UI source code
  COPY ui/ ./
  
  # Build UI (skip the copy-build step)
  RUN npm run build-enterprise
  # Skip the copy-build step since we'll copy the files in the Go build stage
  
  # --- Go Build Stage: Compile the Go binary ---
  FROM golang:1.26.1-alpine3.23@sha256:2389ebfa5b7f43eeafbd6be0c3700cc46690ef842ad962f6c5bd6be49ed82039 AS builder
  WORKDIR /app
  
  # Install dependencies including gcc for CGO and sqlite
  RUN apk add --no-cache gcc musl-dev sqlite-dev binutils binutils-gold
  
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
  FROM alpine:3.23.3@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659
  WORKDIR /app
  
  # Install runtime dependencies for CGO-enabled binary
  # musl: C standard library (required for CGO binaries)
  # libgcc: GCC runtime library
  # ca-certificates: For HTTPS connections
  RUN apk add --no-cache musl libgcc ca-certificates wget zlib=1.3.2-r0
  
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
  
  
  RUN mkdir -p $APP_DIR/logs && \
    adduser -D -s /bin/sh appuser && \
    chown -R appuser:appuser /app && \
    chmod +x /app/docker-entrypoint.sh
  USER appuser
  
  
  # Declare volume for data persistence
  VOLUME ["/app/data"]
  EXPOSE $APP_PORT
  
  # Health check for container status monitoring
  HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 -O /dev/null http://127.0.0.1:${APP_PORT}/health || exit 1
  
  # Use entrypoint script that handles volume permissions and argument processing
  ENTRYPOINT ["/app/docker-entrypoint.sh"]
  CMD ["/app/main"]