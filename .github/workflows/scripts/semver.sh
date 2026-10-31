#!/usr/bin/env bash
# Sourced by read-version-file.sh and extract-npx-version.sh.
#
# is_semver <value>: succeeds only for a Semantic Versioning 2.0.0 string
# (https://semver.org, the BNF at the end). Numeric identifiers carry no leading
# zero and no identifier is empty, so `01.2.3` and `1.0.0-..` fail. npm refuses
# anything else, and these values become release tags before npm sees them.
is_semver() {
  local num='(0|[1-9][0-9]*)'
  local pre='(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)'
  local build='[0-9A-Za-z-]+'
  [[ "$1" =~ ^${num}\.${num}\.${num}(-${pre}(\.${pre})*)?(\+${build}(\.${build})*)?$ ]]
}
