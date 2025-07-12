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

async function uploadWithRetry(command, key, maxRetries = 3) {
  for (let attempt = 1; attempt <= maxRetries; attempt++) {
    try {
      await s3Client.send(command);
      console.log(`🌟 Uploaded: ${key}`);
      return;
    } catch (error) {
      console.log(`⚠️  Attempt ${attempt}/${maxRetries} failed for ${key}: ${error.message}`);
      
      if (attempt === maxRetries) {
        console.error(`❌ All ${maxRetries} attempts failed for ${key}`);
        throw error;
      }
      
      // Wait before retrying (exponential backoff)
      const delay = Math.pow(2, attempt) * 1000; // 2s, 4s, 8s
      console.log(`🔄 Retrying in ${delay/1000}s...`);
      await new Promise(resolve => setTimeout(resolve, delay));
    }
  }
}

// Uploadig new folder
console.log("uploading new release...");
const files = getFiles("./dist");
// Now creating paths from the file
for (const file of files) {
  const filePath = file.split("dist/")[1];
  
  // Upload to versioned path
  const versionedFileStream = fs.createReadStream(file);
  const uploadCommand = new PutObjectCommand({
    Bucket: bucket,
    Key: `bifrost/${cliVersion}/${filePath}`,
    Body: versionedFileStream,
  });
  await uploadWithRetry(uploadCommand, `bifrost/${cliVersion}/${filePath}`);
  
  // Small delay between uploads to avoid rate limiting
  await new Promise(resolve => setTimeout(resolve, 500));
  
  // Upload to latest path (create new stream)
  const latestFileStream = fs.createReadStream(file);
  const latestUploadCommand = new PutObjectCommand({
    Bucket: bucket,
    Key: `bifrost/latest/${filePath}`,
    Body: latestFileStream,
  });
  await uploadWithRetry(latestUploadCommand, `bifrost/latest/${filePath}`);
  
  // Small delay between files
  await new Promise(resolve => setTimeout(resolve, 500));
}

console.log("✅ All binaries uploaded");
