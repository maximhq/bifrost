import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';

const [metadataPath, archivePath] = process.argv.slice(2);
const metadata = JSON.parse(readFileSync(metadataPath, 'utf8'));
if (!['@maximhq/bifrost', '@maximhq/bifrost-cli'].includes(metadata.name) ||
    !/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(metadata.version)) {
  throw new Error('Unexpected npm package identity');
}
if (process.env.NODE_AUTH_TOKEN || process.env.NPM_TOKEN) {
  throw new Error('Long-lived npm tokens are forbidden in the publisher');
}
const expected = 'sha512-' + createHash('sha512').update(readFileSync(archivePath)).digest('base64');
const existing = spawnSync('npm', ['view', `${metadata.name}@${metadata.version}`, 'dist.integrity', '--json'], { encoding: 'utf8' });
if (existing.error) throw existing.error;
if (existing.status === 0) {
  if (JSON.parse(existing.stdout) !== expected) throw new Error('Existing npm version has different bytes; bump the version');
  console.log('Identical npm artifact already published');
} else {
  let error;
  try { error = JSON.parse(existing.stdout); } catch { /* Fail closed below. */ }
  if (error?.error?.code !== 'E404') throw new Error('Cannot establish npm version availability');
  const result = spawnSync('npm', ['publish', archivePath, '--ignore-scripts', '--provenance', '--access', 'public'], { stdio: 'inherit' });
  if (result.error) throw result.error;
  if (result.status !== 0) process.exit(result.status || 1);
}
