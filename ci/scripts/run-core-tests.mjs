#!/usr/bin/env node

import { execSync } from "child_process";
import path from "path";
import fs from "fs";

/**
 * Runs core-providers tests excluding providers that require local instances
 * Excludes: Ollama, SGL (require local setups)
 * Includes: OpenAI, Anthropic, Azure, Bedrock, Cohere, Vertex, Mistral, Groq
 */

console.log("🧪 Running Bifrost Core Providers Tests...");
console.log("📋 Excluding providers that require local instances: Ollama, SGL");

// Get the project root directory by finding where tests/core-providers exists
let projectRoot = process.cwd();
let testDir = path.join(projectRoot, "tests/core-providers");

// If tests/core-providers doesn't exist in current dir, try going up 2 levels (for when run from ci/scripts)
if (!fs.existsSync(testDir)) {
  projectRoot = path.resolve(process.cwd(), "../..");
  testDir = path.join(projectRoot, "tests/core-providers");
}

// Verify we found the correct directory
if (!fs.existsSync(testDir)) {
  console.error(`❌ Could not find tests/core-providers directory`);
  console.error(`   Searched in: ${process.cwd()}/tests/core-providers`);
  console.error(`   And in: ${projectRoot}/tests/core-providers`);
  process.exit(1);
}

console.log(`📁 Test directory: ${testDir}`);

try {
  // Change to test directory
  process.chdir(testDir);
  
  // Run go mod tidy first to ensure dependencies are up to date
  console.log("🔧 Updating Go dependencies...");
  execSync("go mod tidy", { stdio: "inherit" });
  
  // Define providers to test (excluding Ollama and SGL)
  const providersToTest = [
    { name: "TestOpenAI", displayName: "OpenAI" },
    { name: "TestAnthropic", displayName: "Anthropic" }, 
    { name: "TestAzure", displayName: "Azure" },
    { name: "TestBedrock", displayName: "Bedrock" },
    { name: "TestCohere", displayName: "Cohere" },
    { name: "TestVertex", displayName: "Vertex" },
    { name: "TestMistral", displayName: "Mistral" },
    { name: "TestGroq", displayName: "Groq" }
  ];
  
  console.log(`🚀 Running tests for ${providersToTest.length} providers...`);
  console.log("");
  
  const testResults = [];
  let hasFailures = false;
  
  // Run each provider test individually
  for (const provider of providersToTest) {
    console.log(`🧪 Testing ${provider.displayName}...`);
    
    try {
      const testCommand = `go test -run "^${provider.name}$" ./`;
      
      execSync(testCommand, { 
        stdio: "pipe", // Capture output instead of inheriting
        env: {
          ...process.env,
          GO111MODULE: "on"
        }
      });
      
      console.log(`✅ ${provider.displayName}: PASSED`);
      testResults.push({ provider: provider.displayName, status: "PASSED" });
      
    } catch (error) {
      console.log(`❌ ${provider.displayName}: FAILED`);
      testResults.push({ provider: provider.displayName, status: "FAILED", error: error.message });
      hasFailures = true;
    }
    
    console.log(""); // Add spacing between tests
  }
  
  // Print summary
  console.log("📋 Test Results Summary:");
  console.log("========================");
  for (const result of testResults) {
    const statusIcon = result.status === "PASSED" ? "✅" : "❌";
    console.log(`${statusIcon} ${result.provider}: ${result.status}`);
  }
  console.log("");
  
  if (hasFailures) {
    console.log("❌ Some core provider tests failed!");
    process.exit(1);
  } else {
    console.log("✅ All core provider tests passed successfully!");
  }
  
} catch (error) {
  console.error("❌ Core provider tests failed:");
  console.error(error.message);
  process.exit(1);
} 