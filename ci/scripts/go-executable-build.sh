#!/usr/bin/env bash

package=$1
output_path=$(pwd)/$2
app_dir=$4

if [[ -z "$package" ]]; then
  echo "usage: $0 <package-name>"
  exit 1
fi
package_name=$package
echo "cleaning dist..."
rm -r $output_path
mkdir -p $output_path
platforms=("windows/amd64" "windows/386" "darwin/amd64" "darwin/arm64" "linux/386" "linux/amd64")
cd $app_dir
for platform in "${platforms[@]}"
do
	platform_split=(${platform//\// })
	GOOS=${platform_split[0]}
	GOARCH=${platform_split[1]}
	output_name=$package_name
	if [ $GOOS = "windows" ]; then
		output_name+='.exe'
	fi
	echo "building $package_name for $GOOS/$GOARCH..."
	mkdir -p $output_path/$GOOS/$GOARCH
	env GOOS=$GOOS GOARCH=$GOARCH go build -o $output_path/$GOOS/$GOARCH/$output_name $3
	if [ $? -ne 0 ]; then
   		echo 'An error has occurred! Aborting the script execution...'
		exit 1
	fi
done
