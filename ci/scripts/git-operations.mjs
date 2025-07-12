#!/usr/bin/env node

import { execSync } from "child_process";
import fs from "fs";

const operation = process.argv[2];
const message = process.argv[3];
const tag = process.argv[4];

if (!operation) {
  console.error("Usage: node git-operations.mjs <operation> [message] [tag]");
  console.error(
    "Operations: configure, commit-and-push, create-tag, commit-and-tag"
  );
  process.exit(1);
}

function runCommand(cmd, options = {}) {
  try {
    const result = execSync(cmd, {
      encoding: "utf-8",
      stdio: options.silent ? "pipe" : "inherit",
      ...options,
    });
    return result ? result.trim() : "";
  } catch (error) {
    if (!options.ignoreErrors) {
      console.error(`Command failed: ${cmd}`);
      console.error(error.message);
      process.exit(1);
    }
    return null;
  }
}

function configureGit() {
  console.log("🔧 Configuring Git...");
  runCommand('git config user.name "GitHub Actions Bot"');
  runCommand(
    'git config user.email "github-actions[bot]@users.noreply.github.com"'
  );
  console.log("✅ Git configured");
}

function hasChanges() {
  const status = runCommand("git status --porcelain", { silent: true });
  return status && status.length > 0;
}

function hasStagedChanges() {
  const status = runCommand("git diff --staged --name-only", { silent: true });
  return status && status.length > 0;
}

function commitAndPush(commitMessage) {
  if (!commitMessage) {
    console.error("❌ Commit message is required");
    process.exit(1);
  }

  console.log("📝 Checking for changes...");

  // Add all changes
  runCommand("git add -A");

  if (!hasStagedChanges()) {
    console.log("📝 No changes to commit");
    return false;
  }

  console.log(`📝 Committing changes: ${commitMessage}`);
  runCommand(`git commit -m "${commitMessage}"`);

  console.log("📤 Pushing changes...");
  runCommand("git push");

  console.log("✅ Changes committed and pushed");
  return true;
}

function createTag(tagName) {
  if (!tagName) {
    console.error("❌ Tag name is required");
    process.exit(1);
  }

  // Check if tag already exists
  const existingTag = runCommand(`git tag --list | grep -q "^${tagName}$"`, {
    silent: true,
    ignoreErrors: true,
  });

  if (existingTag === null) {
    // grep failed, tag doesn't exist
    console.log(`🏷️  Creating tag: ${tagName}`);
    runCommand(`git tag ${tagName}`);

    console.log(`📤 Pushing tag: ${tagName}`);
    runCommand(`git push origin ${tagName}`);

    console.log("✅ Tag created and pushed");
  } else {
    console.log(`⚠️  Tag ${tagName} already exists, skipping creation`);
  }
}

function commitAndTag(commitMessage, tagName) {
  const hasCommitted = commitAndPush(commitMessage);

  if (hasCommitted || tagName) {
    createTag(tagName);
  }
}

// Main operations
switch (operation) {
  case "configure":
    configureGit();
    break;

  case "commit-and-push":
    if (!message) {
      console.error("❌ Commit message is required for commit-and-push");
      process.exit(1);
    }
    commitAndPush(message);
    break;

  case "create-tag":
    if (!tag) {
      console.error("❌ Tag name is required for create-tag");
      process.exit(1);
    }
    createTag(tag);
    break;

  case "commit-and-tag":
    if (!message || !tag) {
      console.error(
        "❌ Both commit message and tag are required for commit-and-tag"
      );
      process.exit(1);
    }
    commitAndTag(message, tag);
    break;

  default:
    console.error(`❌ Unknown operation: ${operation}`);
    console.error(
      "Available operations: configure, commit-and-push, create-tag, commit-and-tag"
    );
    process.exit(1);
}
