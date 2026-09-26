#!/usr/bin/env bash

# Shared utilities for Go operations in release scripts
# Usage: source .github/workflows/scripts/go-utils.sh

# Function to perform go get with exponential backoff
# Usage: go_get_with_backoff <package@version> [package@version ...]
go_get_with_backoff() {
  local package="$*"
  local max_attempts=30
  local initial_wait=30
  local max_wait=120  # 2 minutes
  local attempt=1
  local wait_time=$initial_wait

  echo "🔄 Attempting to get $package with exponential backoff..."
  
  while [ $attempt -le $max_attempts ]; do
    echo "📦 Attempt $attempt/$max_attempts: go get $package"
    
    if go get "$@"; then
      echo "✅ Successfully retrieved $package on attempt $attempt"
      return 0
    fi
    
    if [ $attempt -eq $max_attempts ]; then
      echo "❌ Failed to get $package after $max_attempts attempts"
      return 1
    fi
    
    echo "⏳ Waiting ${wait_time}s before retry (attempt $attempt/$max_attempts failed)..."
    sleep $wait_time
    
    # Calculate next wait time (exponential backoff)
    # Double the wait time, but cap at max_wait
    wait_time=$((wait_time * 2))
    if [ $wait_time -gt $max_wait ]; then
      wait_time=$max_wait
    fi
    
    attempt=$((attempt + 1))
  done
  
  return 1
}
