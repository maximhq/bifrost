# --- First Stage: Builder image ---
FROM golang:1.24 AS builder
WORKDIR /app

# Set environment for static build
ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOARCH=amd64

# Define build-time variable for transport type
ARG TRANSPORT_TYPE=http

# Initialize Go module and fetch the bifrost transport package
RUN go mod init bifrost-transports && \
    go get github.com/maximhq/bifrost/transports/bifrost-${TRANSPORT_TYPE}@latest

# Build the binary from the fetched package with static linking
RUN go build -ldflags="-w -s" -o /app/main github.com/maximhq/bifrost/transports/${TRANSPORT_TYPE} && \
    test -f /app/main || (echo "Build failed: /app/main not found" && exit 1) && \
    ls -lh /app/main

# --- Second Stage: Runtime image ---
FROM alpine:latest
WORKDIR /app

# Copy the compiled binary from the builder stage
COPY --from=builder /app/main .
# Ensure the binary is executable
RUN chmod +x /app/main
# Create a directory to store configuration files
RUN mkdir -p /app/config

# Define build-time variables for config file paths
ARG CONFIG_PATH
ARG ENV_PATH
ARG PORT
ARG POOL_SIZE
ARG DROP_EXCESS_REQUESTS
ARG PLUGINS
ARG MAXIM_LOG_REPO_ID
ARG PROMETHEUS_LABELS

# Set default values if args are not provided
ENV APP_PORT=${PORT:-8080}
ENV APP_POOL_SIZE=${POOL_SIZE:-300}
ENV APP_DROP_EXCESS_REQUESTS=${DROP_EXCESS_REQUESTS:-false}
ENV APP_PLUGINS=${PLUGINS:-""}
ENV APP_MAXIM_LOG_REPO_ID=${MAXIM_LOG_REPO_ID:-""}
ENV APP_PROMETHEUS_LABELS=${PROMETHEUS_LABELS:-""}

# Copy the config and environment files into the image
COPY ${CONFIG_PATH} /app/config/config.json
COPY ${ENV_PATH} /app/config/.env

# Write a small script to validate config presence and run the app
RUN echo '#!/bin/sh' > /app/entrypoint.sh && \
    echo 'if [ ! -f /app/config/config.json ]; then echo "Missing config.json"; exit 1; fi' >> /app/entrypoint.sh && \
    echo 'if [ ! -f /app/config/.env ]; then echo "Missing .env"; exit 1; fi' >> /app/entrypoint.sh && \
    echo 'if [ ! -f /app/main ]; then echo "Missing main binary"; exit 1; fi' >> /app/entrypoint.sh && \
    echo 'exec /app/main -config /app/config/config.json -env /app/config/.env -port "$APP_PORT" -pool-size "$APP_POOL_SIZE" -drop-excess-requests "$APP_DROP_EXCESS_REQUESTS" -plugins "$APP_PLUGINS" -maxim-log-repo-id "$APP_MAXIM_LOG_REPO_ID" -prometheus-labels "$APP_PROMETHEUS_LABELS"' >> /app/entrypoint.sh && \
    chmod +x /app/entrypoint.sh

# Expose the port defined by argument
EXPOSE ${PORT:-8080}

# Use the script as the entry point
ENTRYPOINT ["/app/entrypoint.sh"]