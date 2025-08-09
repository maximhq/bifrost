# --- UI Build Stage: Build the Next.js frontend ---
FROM node:24-alpine AS ui-builder
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
FROM golang:1.24-alpine AS builder
WORKDIR /app

# Install dependencies including gcc for CGO and sqlite
RUN apk add --no-cache upx gcc musl-dev sqlite-dev

# Set environment for CGO-enabled build (required for go-sqlite3)
ENV CGO_ENABLED=1 GOOS=linux

COPY transports/go.mod transports/go.sum ./
RUN go mod download

# Copy source code and dependencies
COPY transports/ ./

COPY --from=ui-builder /app/out ./bifrost-http/ui

# Build the binary with CGO enabled and static SQLite linking
ENV GOWORK=off
RUN go build \
        -ldflags="-w -s -extldflags '-static'" \
        -a -trimpath \
        -tags "sqlite_static" \
        -o /app/main \
        ./bifrost-http

# Compress binary with upx
RUN upx --best --lzma /app/main

# Verify build succeeded
RUN test -f /app/main || (echo "Build failed" && exit 1)

# --- Runtime Stage: Minimal runtime image ---
FROM alpine:3.19
WORKDIR /app

# Create data directory and set up user
COPY --from=builder /app/main .
COPY transports/docker-entrypoint.sh .
RUN mkdir -p /app/data/logs && \
    adduser -D -s /bin/sh appuser && \
    chown -R appuser:appuser /app && \
    chmod +x /app/docker-entrypoint.sh
USER appuser

# Environment variables with defaults
ENV APP_PORT=8080 \
    APP_HOST=0.0.0.0 \
    APP_PLUGINS=""

# Declare volume for data persistence
VOLUME ["/app/data"]
EXPOSE 8080

# Health check for container status monitoring
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://127.0.0.1:${APP_PORT}/metrics || exit 1

# Use entrypoint script that handles volume permissions
ENTRYPOINT ["/bin/sh", "-c", "/app/docker-entrypoint.sh -app-dir /app/data -port ${APP_PORT} -host \"${APP_HOST}\" -plugins \"${APP_PLUGINS}\""]
