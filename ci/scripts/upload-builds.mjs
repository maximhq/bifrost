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

// Exit if any required environment variables are missing
const requiredEnvVars = ['R2_ENDPOINT', 'R2_ACCESS_KEY_ID', 'R2_SECRET_ACCESS_KEY'];
const missingEnvVars = requiredEnvVars.filter(varName => !process.env[varName]);

if (missingEnvVars.length > 0) {
  console.error(`❌ Missing required environment variables: ${missingEnvVars.join(', ')}`);
  console.error('Please set all required environment variables before running this script.');
  process.exit(1);
}

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
