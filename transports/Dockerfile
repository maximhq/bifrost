# --- UI Build Stage: Build the Next.js frontend ---
FROM node:25-alpine3.23@sha256:cf38e1f3c28ac9d81cdc0c51d8220320b3b618780e44ef96a39f76f7dbfef023 AS ui-builder
WORKDIR /app

# Copy UI package files and install dependencies
COPY ui/package*.json ./
RUN npm ci

# Copy UI source code
COPY ui/ ./

# Build UI (skip the copy-build step)
RUN npx next build
RUN node scripts/fix-paths.js
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

# Verify build succeeded and prepare entrypoint
RUN test -f /app/main || (echo "Build failed" && exit 1)
RUN chmod +x /build/transports/docker-entrypoint.sh

# --- Runtime Stage: Minimal runtime image ---
FROM bifrosthq/dhi-alpine-base:3.22-fips_bifrost-v27032026
WORKDIR /app

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


RUN mkdir -p $APP_DIR/logs
USER appuser


# Declare volume for data persistence
VOLUME ["/app/data"]
EXPOSE $APP_PORT

# Use entrypoint script that handles volume permissions and argument processing
ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["/app/main"]
