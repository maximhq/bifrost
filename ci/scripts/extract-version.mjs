#!/usr/bin/env node

const gitRef = process.argv[2];
const expectedPrefix = process.argv[3]; // 'core' or 'transports'
const outputField = process.argv[4] || "version"; // what to output (default: version)

if (!gitRef) {
  console.error("Usage: node extract-version.mjs <git-ref> <core|transports>");
  console.error("Example: node extract-version.mjs refs/tags/core/v1.2.3 core");
  process.exit(1);
}

function extractVersion(ref, prefix) {
  // Handle different ref formats
  let tagName;
  if (ref.startsWith("refs/tags/")) {
    tagName = ref.replace("refs/tags/", "");
  } else {
    tagName = ref;
  }

  if (prefix) {
    // Validate prefix and extract version
    const expectedStart = `${prefix}/v`;
    if (!tagName.startsWith(expectedStart)) {
      console.error(
        `❌ Invalid tag format '${tagName}'. Expected format: ${prefix}/vMAJOR.MINOR.PATCH`
      );
      process.exit(1);
    }
    const version = tagName.replace(`${prefix}/`, "");
    // Validate version format (vX.Y.Z)
    if (!version.match(/^v[0-9]+\.[0-9]+\.[0-9]+$/)) {
      console.error(
        `❌ Invalid version format '${version}'. Expected format: vMAJOR.MINOR.PATCH`
      );
      process.exit(1);
    }
    return {
      full_tag: tagName,
      prefix: prefix,
      version: version,
      version_number: version.replace("v", ""),
    };
  } else {
    // Just extract whatever is after the last slash
    const parts = tagName.split("/");
    const version = parts[parts.length - 1];
    const prefixPart = parts.slice(0, -1).join("/");
    return {
      full_tag: tagName,
      prefix: prefixPart || null,
      version: version,
      version_number: version.replace("v", ""),
    };
  }
}

try {
  const result = extractVersion(gitRef, expectedPrefix);
  // Output only the requested field to stdout
  if (result[outputField] !== undefined) {
    console.log(result[outputField]);
  } else {
    console.error(
      `❌ Unknown output field '${outputField}'. Valid options: full_tag, prefix, version, version_number`
    );
    process.exit(1);
  }
  // Output all info to stderr for debugging
  console.error(`📋 Extracted version info:`);
  console.error(`   Full Tag: ${result.full_tag}`);
  console.error(`   Prefix: ${result.prefix || "none"}`);
  console.error(`   Version: ${result.version}`);
  console.error(`   Version Number: ${result.version_number}`);
} catch (error) {
  console.error(`❌ Failed to extract version: ${error.message}`);
  process.exit(1);
}
