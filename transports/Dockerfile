# Use Go 1.24 as the base image
FROM golang:1.24

# Set the working directory
WORKDIR /app

# Create a new module and get the bifrost/transports package
RUN go mod init bifrost-transports && \
    go get github.com/bifrost/transports

# Create directories for config files
RUN mkdir -p /app/config

# Build the application
RUN go build -o main github.com/bifrost/transports/http.go

# Expose the port the application runs on
EXPOSE 8080

# Define required arguments for config and env file paths
ARG CONFIG_PATH
ARG ENV_PATH

# Validate that required arguments are provided
RUN test -n "$CONFIG_PATH" || (echo "CONFIG_PATH argument is required" && exit 1) && \
    test -n "$ENV_PATH" || (echo "ENV_PATH argument is required" && exit 1)

# Copy the config files (will fail if files don't exist)
COPY ${CONFIG_PATH} /app/config/config.json
COPY ${ENV_PATH} /app/config/.env

# Command to run the application with the provided config paths
CMD ["./main", "-config", "/app/config/config.json", "-env", "/app/config/.env", "-port", "8080", "-pool-size", "300"]
