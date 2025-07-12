#!/usr/bin/env node

import { execSync } from "child_process";
import fs from "fs";

const pipeline = process.argv[2];
const params = process.argv.slice(3);

if (!pipeline) {
  console.error("Usage: node run-pipeline.mjs <pipeline> [params...]");
  console.error("Pipelines: transport-build, extract-tag");
  process.exit(1);
}

function runScript(scriptName, args = []) {
  const cmd = `node ${scriptName} ${args.join(" ")}`;
  console.log(`🚀 Running: ${cmd}`);

  try {
    const result = execSync(cmd, {
      encoding: "utf-8",
      stdio: "inherit",
    });
    return result;
  } catch (error) {
    console.error(`❌ Script failed: ${scriptName}`);
    throw error;
  }
}

function runScriptWithOutput(scriptName, args = []) {
  const cmd = `node ${scriptName} ${args.join(" ")}`;
  console.log(`🚀 Running: ${cmd}`);

  try {
    const result = execSync(cmd, {
      encoding: "utf-8",
    });
    return result.trim();
  } catch (error) {
    console.error(`❌ Script failed: ${scriptName}`);
    throw error;
  }
}

function transportBuildPipeline() {
  const [triggerType, triggerVersion] = params;

  if (!triggerType) {
    console.error("❌ Trigger type is required for transport build pipeline");
    console.error(
      "Usage: node run-pipeline.mjs transport-build <core|transport> [version]"
    );
    process.exit(1);
  }

  console.log("🚀 Starting Transport Build Pipeline...");

  // 1. Configure git
  runScript("git-operations.mjs", ["configure"]);

  // 2. Manage versions and dependencies
  const versionOutput = runScriptWithOutput("manage-versions.mjs", [
    triggerType,
    triggerVersion,
  ]);

  // Parse version outputs
  const versions = {};
  versionOutput.split("\n").forEach((line) => {
    if (line.includes("=")) {
      const [key, value] = line.split("=");
      versions[key] = value;
    }
  });

  console.log("📦 Versions determined:", versions);

  // 3. Build UI static files from repo
  console.log("🎨 Building UI static files...");
  execSync("npm install", { cwd: "../ui", stdio: "inherit" });
  execSync("npm run build", { cwd: "../ui", stdio: "inherit" });

  // 4. Build Go executables
  console.log("🔨 Building Go executables...");
  execSync("chmod +x go-executable-build.sh", { stdio: "inherit" });
  execSync(
    "./go-executable-build.sh bifrost-http ../dist/apps/bifrost ./bifrost-http $(pwd)",
    {
      cwd: "../transports",
      stdio: "inherit",
    }
  );

  // 5. Upload builds
  const uploadVersion = versions.transport_version.replace("transports/v", "v");
  runScript("upload-builds.mjs", [uploadVersion]);

  // 6. Commit and tag (if needed)
  if (triggerType === "core") {
    const commitMsg = `chore: update transport's core dependency to ${versions.core_version}`;
    runScript("git-operations.mjs", [
      "commit-and-tag",
      commitMsg,
      versions.transport_version,
    ]);
  }

  console.log("✅ Transport Build Pipeline completed");
  return versions;
}

function extractTagPipeline() {
  const [gitRef, expectedPrefix] = params;

  if (!gitRef) {
    console.error("❌ Git ref is required for extract tag pipeline");
    process.exit(1);
  }

  console.log("📋 Extracting tag information...");
  const result = runScriptWithOutput("extract-version.mjs", [
    gitRef,
    expectedPrefix,
  ]);
  console.log(result);

  return result;
}

// Main execution
try {
  let result;

  switch (pipeline) {
    case "transport-build":
      result = transportBuildPipeline();
      break;

    case "extract-tag":
      result = extractTagPipeline();
      break;

    default:
      console.error(`❌ Unknown pipeline: ${pipeline}`);
      console.error("Available pipelines: transport-build, extract-tag");
      process.exit(1);
  }

  console.log(`🎉 Pipeline '${pipeline}' completed successfully!`);

  if (result && typeof result === "object") {
    fs.writeFileSync(
      "/tmp/pipeline-result.json",
      JSON.stringify(result, null, 2)
    );
  }
} catch (error) {
  console.error(`💥 Pipeline '${pipeline}' failed:`, error.message);
  process.exit(1);
}
