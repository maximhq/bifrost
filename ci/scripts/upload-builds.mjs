import { PutObjectCommand, S3Client } from "@aws-sdk/client-s3";
import fs from "fs";
import path from "path";

const cliVersion = process.argv[2];
if (!cliVersion) {
  console.error(
    "CLI version not provided. Usage: node upload-maxim-cli.mjs <version>"
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
// Uploadig new folder
console.log("uploading new release...");
const files = getFiles("./dist/apps/bifrost");
// Now creating paths from the file
for (const file of files) {
  const filePath = file.split("dist/apps/bifrost/")[1];
  const fileStream = fs.createReadStream(file);
  const uploadCommand = new PutObjectCommand({
    Bucket: bucket,
    Key: `bifrost/${cliVersion}/${filePath}`,
    Body: fileStream,
  });
  const latestUploadCommand = new PutObjectCommand({
    Bucket: bucket,
    Key: `bifrost/latest/${filePath}`,
    Body: fileStream,
  });
  await s3Client.send(uploadCommand);
  await s3Client.send(latestUploadCommand);
}

console.log("up and running...");
