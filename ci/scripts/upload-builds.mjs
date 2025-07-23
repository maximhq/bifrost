import { PutObjectCommand, S3Client } from "@aws-sdk/client-s3";
import fs from "fs";
import path from "path";

const cliVersion = process.argv[2];
if (!cliVersion) {
  console.error(
    "CLI version not provided. Usage: node upload-builds.mjs <version>"
  );
  process.exit(1);
}

function getFiles(dir) {
  const dirents = fs.readdirSync(dir, { withFileTypes: true });
  const files = dirents.map((dirent) => {
    const res = path.resolve(dir, dirent.name);
    return dirent.isDirectory() ? getFiles(res) : res;
  });
  return Array.prototype.concat(...files);
}

const s3Client = new S3Client({
  endpoint: process.env.R2_ENDPOINT,
  region: "us-east-1", // auto
  credentials: {
    accessKeyId: process.env.R2_ACCESS_KEY_ID,
    secretAccessKey: process.env.R2_SECRET_ACCESS_KEY,
  },
});

const bucket = "prod-downloads";

async function uploadWithRetry(filePath, s3Key, maxRetries = 3) {
  for (let attempt = 1; attempt <= maxRetries; attempt++) {
    try {
      // Create a fresh stream for each attempt
      const fileStream = fs.createReadStream(filePath);
      const command = new PutObjectCommand({
        Bucket: bucket,
        Key: s3Key,
        Body: fileStream,
      });
      
      await s3Client.send(command);
      console.log(`🌟 Uploaded: ${s3Key}`);
      return;
    } catch (error) {
      console.log(`⚠️  Attempt ${attempt}/${maxRetries} failed for ${s3Key}: ${error.message}`);
      
      if (attempt === maxRetries) {
        console.error(`❌ All ${maxRetries} attempts failed for ${s3Key}`);
        throw error;
      }
      
      // Wait before retrying (exponential backoff)
      const delay = Math.pow(2, attempt) * 1000; // 2s, 4s, 8s
      console.log(`🔄 Retrying in ${delay/1000}s...`);
      await new Promise(resolve => setTimeout(resolve, delay));
    }
  }
}

// Debug and validate environment variables
console.log('🔍 Environment variables debug:');
const requiredEnvVars = ['R2_ENDPOINT', 'R2_ACCESS_KEY_ID', 'R2_SECRET_ACCESS_KEY'];

requiredEnvVars.forEach(varName => {
  const value = process.env[varName];
  if (!value) {
    console.log(`❌ ${varName}: Missing`);
    process.exit(1);
  } else {
    // Show first/last few chars to verify without exposing secrets
    const masked = value.length > 4 
      ? `${value.substring(0, 2)}...${value.substring(value.length - 2)}`
      : `${value.substring(0, 1)}...`;
    console.log(`✅ ${varName}: Set (${value.length} chars) ${masked}`);
    
    // Check for common issues
    if (value.includes('\n')) console.log(`⚠️  ${varName}: Contains newlines`);
    if (value.startsWith(' ') || value.endsWith(' ')) console.log(`⚠️  ${varName}: Contains leading/trailing spaces`);
  }
});

// Uploadig new folder
console.log("uploading new release...");
const files = getFiles("./dist");
// Now creating paths from the file
for (const file of files) {
  const filePath = file.split("dist/")[1];
  
  // Upload to versioned path
  await uploadWithRetry(file, `bifrost/${cliVersion}/${filePath}`);
  
  // Small delay between uploads to avoid rate limiting
  await new Promise(resolve => setTimeout(resolve, 500));
  
  // Upload to latest path
  await uploadWithRetry(file, `bifrost/latest/${filePath}`);
  
  // Small delay between files
  await new Promise(resolve => setTimeout(resolve, 500));
}

console.log("✅ All binaries uploaded");
