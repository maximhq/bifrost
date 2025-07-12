#!/usr/bin/env node

import { execSync } from "child_process";
import fs from "fs";

const pipeline = process.argv[2];
const params = process.argv.slice(3);

if (!pipeline) {
  console.error("Usage: node run-pipeline.mjs <pipeline> [params...]");
  console.error("Pipelines: extract-tag, core-dependency-update");
  process.exit(1);
}

function runScript(scriptName, args = [], options = {}) {
  const cmd = `node ${scriptName} ${args.join(" ")}`;
  console.log(`🚀 Running: ${cmd}`);

  try {
    const result = execSync(cmd, {
      encoding: "utf-8",
      stdio: "inherit",
      ...options,
    });
    return result;
  } catch (error) {
    console.error(`❌ Script failed: ${scriptName}`);
    throw error;
  }
}

function runScriptWithOutput(scriptName, args = [], options = {}) {
  const cmd = `node ${scriptName} ${args.join(" ")}`;
  console.log(`🚀 Running: ${cmd}`);

  try {
    const result = execSync(cmd, {
      encoding: "utf-8",
      ...options,
    });
    return result.trim();
  } catch (error) {
    console.error(`❌ Script failed: ${scriptName}`);
    throw error;
  }
}

function runCommand(cmd, options = {}) {
  console.log(`🔧 Running: ${cmd}`);
  
  try {
    const result = execSync(cmd, {
      encoding: "utf-8",
      stdio: options.stdio || "inherit",
      ...options,
    });
    return result ? result.trim() : "";
  } catch (error) {
    console.error(`❌ Command failed: ${cmd}`);
    throw error;
  }
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

function coreDependencyUpdatePipeline() {
  const [coreVersion] = params;

  if (!coreVersion) {
    console.error("❌ Core version is required for core dependency update pipeline");
    console.error("Usage: node run-pipeline.mjs core-dependency-update <core-version>");
    process.exit(1);
  }

  console.log("🚀 Starting Core Dependency Update Pipeline...");

  const branchName = `chore/update-core-${coreVersion}`;
  
  // Add branch check
  try {
    runCommand(`git rev-parse --verify ${branchName}`, { stdio: 'ignore' });
    console.error(`❌ Branch ${branchName} already exists. Aborting to prevent overwriting.`);
    process.exit(1);
  } catch (error) {
    // Branch does not exist, proceed with creation
  }

  // 1. Create branch and update dependency
  console.log(`🌿 Creating branch: ${branchName}`);
  runCommand(`git checkout -b "${branchName}"`);
  
  console.log(`🔧 Updating core dependency to ${coreVersion}`);
  runCommand(`cd ../../transports && go get github.com/maximhq/bifrost/core@${coreVersion}`);
  runCommand("cd ../../transports && go mod tidy");
  runCommand("git add transports/go.mod transports/go.sum");

  // 2. Build validation
  console.log("🔨 Validating builds...");
  let buildSuccess = true;
  let buildError = "";

  try {
    // Validate Go build
    console.log("🏗️ Testing Go build...");
    runCommand("cd ../../transports && go build ./...", { stdio: "pipe" });
    console.log("✅ Go build successful");

    // Validate UI build
    console.log("🎨 Testing UI build...");
    runCommand("cd ../../ui && npm ci", { stdio: "pipe" });
    runCommand("cd ../../ui && npm run build", { stdio: "pipe" });
    console.log("✅ UI build successful");

    console.log("🎉 All builds successful");
  } catch (error) {
    buildSuccess = false;
    buildError = error.message;
    console.log(`❌ Build failed: ${buildError}`);
  }

  // 3. Push branch
  console.log("📤 Pushing branch to origin");
  runCommand(`git push origin "${branchName}"`);

  // 4. Create PR
  console.log("📝 Creating pull request...");
  runScript("git-operations.mjs", [
    "create-pr",
    coreVersion,
    branchName,
    buildSuccess.toString(),
    buildError
  ]);

  console.log("✅ Core Dependency Update Pipeline completed");
  
  return {
    core_version: coreVersion,
    branch_name: branchName,
    build_success: buildSuccess,
    build_error: buildError
  };
}

// Main execution
async function main() {
  try {
    let result;

    switch (pipeline) {
      case "extract-tag":
        result = extractTagPipeline();
        break;

      case "core-dependency-update":
        result = await coreDependencyUpdatePipeline();
        break;

      default:
        console.error(`❌ Unknown pipeline: ${pipeline}`);
        console.error("Available pipelines: extract-tag, core-dependency-update");
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
}

main();
