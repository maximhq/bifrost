#!/usr/bin/env node

import { execSync } from "child_process";
import { Octokit } from "@octokit/rest";

const operation = process.argv[2];
const message = process.argv[3];
const tag = process.argv[4];

if (!operation) {
  console.error("Usage: node git-operations.mjs <operation> [message] [tag]");
  console.error(
    "Operations: configure, create-tag, create-pr"
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



async function createPR(coreVersion, branchName, buildSuccess, buildError) {
  if (!process.env.GITHUB_TOKEN) {
    console.error("❌ GITHUB_TOKEN environment variable is required");
    process.exit(1);
  }

  const octokit = new Octokit({
    auth: process.env.GITHUB_TOKEN,
  });

  const title = `chore: update core dependency to ${coreVersion} --trigger-release`;
  
  let body = `## Core Dependency Update

This PR updates the core dependency to \`${coreVersion}\`.

### Build Validation
`;

  if (buildSuccess === 'true') {
    body += `✅ **Build successful** - All builds passed validation

### Auto-merge
This PR is set to auto-merge since builds passed validation.`;
  } else {
    body += `❌ **Build failed** - ${buildError}

### Manual Review Required
This PR requires manual review due to build failures.`;
  }

  body += `

### Changes
- Updated \`transports/go.mod\` to use \`github.com/maximhq/bifrost/core@${coreVersion}\`

---
_This PR was automatically created by the Core Dependency Update workflow._`;

  const prData = {
    owner: 'maximhq',
    repo: 'bifrost',
    title,
    head: branchName,
    base: 'main',
    body,
  };

  try {
    console.log(`📝 Creating PR: ${title}`);
    const { data: pr } = await octokit.rest.pulls.create(prData);
    console.log(`✅ PR created: ${pr.html_url}`);
    
    if (buildSuccess === 'true') {
      try {
        // Enable auto-merge if builds passed
        await octokit.rest.pulls.enableAutoMerge({
          owner: 'maximhq',
          repo: 'bifrost',
          pull_number: pr.number,
          merge_method: 'squash'
        });
        console.log(`🤖 Auto-merge enabled for PR #${pr.number}`);
      } catch (autoMergeError) {
        console.log(`⚠️ Could not enable auto-merge: ${autoMergeError.message}`);
        console.log(`💡 You may need to enable auto-merge in repository settings`);
      }
    } else {
      // Add labels for failed builds
      await octokit.rest.issues.addLabels({
        owner: 'maximhq',
        repo: 'bifrost',
        issue_number: pr.number,
        labels: ['needs-review', 'build-failure']
      });
      console.log(`🏷️ Added labels for manual review`);
    }
    
    return pr;
  } catch (error) {
    console.error('❌ Failed to create PR:', error.message);
    process.exit(1);
  }
}

// Main operations
switch (operation) {
  case "configure": {
    configureGit();
    break;
  }
  
  case "create-tag":{
    if (!tag) {
      console.error("❌ Tag name is required for create-tag");
      process.exit(1);
    }
    createTag(tag);
    break;
  }

  case "create-pr": {
    // Parse arguments: core-version branch-name build-success [build-error]
    const coreVersion = process.argv[3];
    const branchName = process.argv[4];
    const buildSuccess = process.argv[5];
    const buildError = process.argv[6] || "";
    
    if (!coreVersion || !branchName || !buildSuccess) {
      console.error("❌ create-pr requires: core-version branch-name build-success [build-error]");
      process.exit(1);
    }
    
    createPR(coreVersion, branchName, buildSuccess, buildError);
    break;
  }

  default:
    console.error(`❌ Unknown operation: ${operation}`);
    console.error(
      "Available operations: configure, create-tag, create-pr"
    );
    process.exit(1);
}
