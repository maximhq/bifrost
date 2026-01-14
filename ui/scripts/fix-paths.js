#!/usr/bin/env node

const fs = require("fs");
const path = require("path");

function calculateDepth(filePath) {
	// Count the number of directory separators to determine depth
	// Remove the filename and count directories
	const dir = path.dirname(filePath);
	if (dir === "." || dir === "/") {
		return 0; // Root level
	}
	return dir.split(path.sep).filter((part) => part !== "").length;
}

function generateRelativePath(depth) {
	if (depth === 0) {
		return "./"; // Root level uses ./
	}
	return "../".repeat(depth); // Go up {depth} directories
}

function fixPathsInFile(filePath) {
	const content = fs.readFileSync(filePath, "utf8");
	const depth = calculateDepth(filePath);
	const relativePath = generateRelativePath(depth);

	console.log(`Processing ${filePath} (depth: ${depth}, prefix: ${relativePath})`);

	// Replace absolute paths with relative paths
	// /_next/ -> ./_next/ or ../_next/ depending on depth
	const updatedContent = content.replace(/"\/_next\//g, `"${relativePath}_next/`);

	if (content !== updatedContent) {
		fs.writeFileSync(filePath, updatedContent);
		console.log(`  ✓ Fixed paths in ${filePath}`);
	} else {
		console.log(`  - No changes needed in ${filePath}`);
	}
}

function findHtmlFiles(dir) {
	const files = [];

	function traverse(currentDir) {
		const items = fs.readdirSync(currentDir);

		for (const item of items) {
			const fullPath = path.join(currentDir, item);
			const stat = fs.statSync(fullPath);

			if (stat.isDirectory()) {
				traverse(fullPath);
			} else if (item.endsWith(".html")) {
				// Convert to relative path from the out directory
				const relativePath = path.relative("out", fullPath);
				files.push(relativePath);
			}
		}
	}

	traverse(dir);
	return files;
}

// Main execution
const outDir = "out";
if (!fs.existsSync(outDir)) {
	console.error("Output directory not found. Please run next build first.");
	process.exit(1);
}

console.log("Finding HTML files...");
const htmlFiles = findHtmlFiles(outDir);

console.log(`Found ${htmlFiles.length} HTML files`);

for (const file of htmlFiles) {
	const fullPath = path.join(outDir, file);

	// Skip root index.html and 404.html as they already have correct paths
	if (file === "index.html" || file === "404.html") {
		console.log(`Skipping root file: ${file}`);
		continue;
	}

	fixPathsInFile(fullPath);
}

console.log("✅ Path fixing complete!");
