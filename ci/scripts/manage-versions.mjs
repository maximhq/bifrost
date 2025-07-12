#!/usr/bin/env node

import { execSync } from "child_process";
import fs from "fs";

const triggerType = process.argv[2]; // 'core' or 'transport'
const inputVersion = process.argv[3]; // version if provided

if (!triggerType) {
  console.error("Usage: node manage-versions.mjs <trigger-type> [version]");
  console.error("trigger-type: core, transport, transport-release");
  process.exit(1);
}

function runCommand(cmd) {
  try {
    return execSync(cmd, { encoding: "utf-8" }).trim();
  } catch (error) {
    console.error(`Command failed: ${cmd}`);
    console.error(error.message);
    process.exit(1);
  }
}

function getLatestTransportTag() {
  try {
    const tags = runCommand('git tag -l "transports/v*" | sort -V');
    const tagList = tags.split("\n").filter((tag) => tag.trim());
    return tagList.length > 0 ? tagList[tagList.length - 1] : null;
  } catch {
    return null;
  }
}

function incrementTransportVersion() {
  const latestTag = getLatestTransportTag();

  if (!latestTag) {
    return "transports/v0.1.0";
  }

  const version = latestTag.replace("transports/v", "");
  const [major, minor, patch] = version.split(".").map(Number);

  return `transports/v${major}.${minor}.${patch + 1}`;
}

function getCurrentCoreVersion() {
  try {
    const version = runCommand(
      'cd transports && go list -m -f "{{.Version}}" github.com/maximhq/bifrost/core 2>/dev/null'
    );
    return version || "latest";
  } catch {
    return "latest";
  }
}

function updateCoreDependency(version) {
  console.log(`🔧 Updating core dependency to ${version}...`);
  runCommand(
    `cd transports && go get github.com/maximhq/bifrost/core@${version}`
  );
  runCommand("cd transports && go mod tidy");
}

// Main logic
let result = {};

switch (triggerType) {
  case "core": {
    const coreVersion = inputVersion;
    if (!coreVersion) {
      console.error("Core version is required for core trigger");
      process.exit(1);
    }

    updateCoreDependency(coreVersion);
    result = {
      transport_version: incrementTransportVersion(),
      core_version: coreVersion,
    };
    break;
  }

  case "transport": {
    const transportVersion = inputVersion;
    if (!transportVersion) {
      console.error("Transport version is required for transport trigger");
      process.exit(1);
    }

    result = {
      transport_version: transportVersion,
      core_version: getCurrentCoreVersion(),
    };
    break;
  }

  case "transport-release": {
    // Used when a core dependency update is merged - generates new transport version
    const coreVersion = inputVersion || getCurrentCoreVersion();
    
    result = {
      transport_version: incrementTransportVersion(),
      core_version: coreVersion,
    };
    break;
  }

  default:
    console.error(`Unknown trigger type: ${triggerType}`);
    console.error("Available trigger types: core, transport, transport-release");
    process.exit(1);
}

// Output for GitHub Actions
console.log(`transport_version=${result.transport_version}`);
console.log(`core_version=${result.core_version}`);

// Also output as JSON for easier parsing
fs.writeFileSync("/tmp/versions.json", JSON.stringify(result, null, 2));

console.error(`📦 Transport Version: ${result.transport_version}`);
console.error(`🔧 Core Version: ${result.core_version}`);
