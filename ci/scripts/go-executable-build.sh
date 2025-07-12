#!/usr/bin/env bash

package=$1
output_path=$(pwd)/$2
app_dir=$4

# Validate all required parameters
if [[ -z "$package" ]]; then
  echo "usage: $0 <package-name> <output-dir> <build-flags> <app-dir>"
  echo "error: package name is required"
  exit 1
fi

if [[ -z "$2" ]]; then
  echo "usage: $0 <package-name> <output-dir> <build-flags> <app-dir>"
  echo "error: output directory is required"
  exit 1
fi

if [[ -z "$3" ]]; then
  echo "usage: $0 <package-name> <output-dir> <build-flags> <app-dir>"
  echo "error: build flags are required"
  exit 1
fi

if [[ -z "$app_dir" ]]; then
  echo "usage: $0 <package-name> <output-dir> <build-flags> <app-dir>"
  echo "error: application directory is required"
  exit 1
fi

package_name="$package"
echo "cleaning dist..."
# Use -rf to force removal without errors if directory doesn't exist
rm -rf "$output_path"
# Create directory with -p to create parent directories safely
mkdir -p "$output_path"
# Add error checking for directory creation
if [ $? -ne 0 ]; then
    echo "Error: Failed to create output directory $output_path"
    exit 1
fi

platforms=("windows/amd64" "windows/386" "darwin/amd64" "darwin/arm64" "linux/386" "linux/amd64")
cd "$app_dir" || { echo "Error: Failed to change to directory $app_dir"; exit 1; }
for platform in "${platforms[@]}"
do
	# Use IFS and read for safer platform string splitting
	IFS='/' read -r GOOS GOARCH <<< "$platform"
	output_name="$package_name"
	if [ "$GOOS" = "windows" ]; then
		output_name+='.exe'
	fi
	echo "building $package_name for $GOOS/$GOARCH..."
	mkdir -p "$output_path/$GOOS/$GOARCH"
	if ! env GOOS="$GOOS" GOARCH="$GOARCH" go build -o "$output_path/$GOOS/$GOARCH/$output_name" "$3"; then
   		echo 'An error has occurred! Aborting the script execution...'
		exit 1
	fi
done
