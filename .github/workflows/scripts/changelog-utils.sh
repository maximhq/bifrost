#!/usr/bin/env bash

# Function to extract content from a file
# Usage: get_file_content <file_path>
# Returns the file content with comments removed, or empty string if file doesn't exist
get_file_content() {
    if [ -f "$1" ]; then
        content=$(cat "$1")
        # Skip comments from content
        content=$(echo "$content" | grep -v '^<!--' | grep -v '^-->')
        # For version files, also trim newlines and whitespace
        if [[ "$1" == *"/version" ]]; then
            content=$(echo "$content" | tr -d '\n' | xargs)
        fi
        echo "$content"
    else
        echo ""
    fi
}