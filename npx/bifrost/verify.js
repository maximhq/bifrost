import { createHash } from 'node:crypto';
import { constants, createReadStream, createWriteStream, openSync, fstatSync, closeSync, chmodSync, renameSync, unlinkSync } from 'node:fs';
import { Readable, Transform } from 'node:stream';
import { pipeline } from 'node:stream/promises';

const MAX_BINARY_BYTES = 512 * 1024 * 1024;

// These helpers are shipped in both dependency-free launchers; CI checks parity.
export function parseChecksum(text) {
  const match = /^([a-fA-F0-9]{64})(?:[ \t]+\*?[^\r\n]+)?\s*$/.exec(text);
  if (!match || text.length > 1024) throw new Error('Missing or malformed SHA-256 checksum');
  return match[1].toLowerCase();
}

const DOWNLOAD_ORIGIN = 'https://downloads.getmaxim.ai';
const RELEASE_ORIGIN = 'https://github.com';
const RELEASE_REPOSITORY = 'maximhq/bifrost';
const MAX_CHECKSUM_LIST_BYTES = 64 * 1024;

// readText bounds a response body so a hostile server cannot exhaust memory.
async function readText(response, limit, what) {
  let text = '';
  for await (const bytes of response.body) {
    text += Buffer.from(bytes).toString('utf8');
    if (text.length > limit) throw new Error(`Oversized ${what} response`);
  }
  return text;
}

export async function getExpectedHash(url, fetcher = fetch) {
  const parsed = new URL(url);
  if (parsed.origin !== DOWNLOAD_ORIGIN) throw new Error('Untrusted download origin');
  const response = await fetcher(url, { redirect: 'error', signal: AbortSignal.timeout(15000) });
  if (!response.ok) throw new Error(`Checksum download failed (${response.status}); refusing to execute`);
  return parseChecksum(await readText(response, 1024, 'checksum'));
}

// parseChecksumList reads the `<sha256>  <path>` lines of a release checksums asset.
export function parseChecksumList(text) {
  const entries = new Map();
  for (const line of text.split(/\r?\n/)) {
    if (line.trim() === '') continue;
    const match = /^([a-fA-F0-9]{64})[ \t]+\*?(\S+)$/.exec(line.trim());
    if (!match) throw new Error('Malformed release checksum list');
    entries.set(match[2], match[1].toLowerCase());
  }
  if (entries.size === 0) throw new Error('Empty release checksum list');
  return entries;
}

// getReleaseHash reads the checksum the CI publisher attached to the GitHub Release for
// this tag. The release asset is written with the repository token, which the object-store
// key cannot forge, so the two sources must agree before any download is trusted.
export async function getReleaseHash(tag, relativePath, fetcher = fetch) {
  if (!/^[a-z-]+\/v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(tag)) throw new Error('Invalid release tag');
  const url = `${RELEASE_ORIGIN}/${RELEASE_REPOSITORY}/releases/download/${encodeURIComponent(tag)}/checksums.txt`;
  // GitHub serves release assets through a redirect to its object store; follow it but
  // require the final hop to stay on GitHub infrastructure.
  const response = await fetcher(url, { redirect: 'follow', signal: AbortSignal.timeout(15000) });
  const landed = new URL(response.url || url);
  if (!/(^|\.)(github\.com|githubusercontent\.com)$/.test(landed.hostname)) throw new Error('Release asset left GitHub');
  if (!response.ok) throw new Error(`Release checksum download failed (${response.status}); refusing to execute`);
  const entries = parseChecksumList(await readText(response, MAX_CHECKSUM_LIST_BYTES, 'release checksum'));
  const expected = entries.get(relativePath);
  if (!expected) throw new Error(`Release ${tag} publishes no checksum for ${relativePath}`);
  return expected;
}

// getAgreedHash fails unless the object store and the GitHub Release name the same bytes.
export async function getAgreedHash(url, tag, relativePath, fetcher = fetch) {
  const [stored, released] = await Promise.all([getExpectedHash(`${url}.sha256`, fetcher), getReleaseHash(tag, relativePath, fetcher)]);
  if (stored !== released) throw new Error('Download server and GitHub Release disagree on the checksum; refusing to execute');
  return released;
}

export async function verifyFile(path, expected) {
  if (!/^[a-f0-9]{64}$/.test(expected)) throw new Error('Invalid expected checksum');
  const descriptor = openSync(path, constants.O_RDONLY | (constants.O_NOFOLLOW || 0));
  try {
    const stat = fstatSync(descriptor);
    if (!stat.isFile() || stat.size <= 0 || stat.size > MAX_BINARY_BYTES) throw new Error('Invalid binary file');
    const hash = createHash('sha256');
    for await (const chunk of createReadStream(path, { fd: descriptor, autoClose: false })) hash.update(chunk);
    if (hash.digest('hex') !== expected) throw new Error('Binary checksum mismatch; refusing to execute');
  } finally {
    closeSync(descriptor);
  }
}

export async function downloadVerified(url, destination, expected, fetcher = fetch) {
  if (new URL(url).origin !== DOWNLOAD_ORIGIN) throw new Error('Untrusted download origin');
  const temporary = `${destination}.download-${process.pid}-${Date.now()}`;
  let created = false;
  try {
    const response = await fetcher(url, { redirect: 'error', signal: AbortSignal.timeout(120000) });
    if (!response.ok || !response.body) throw new Error(`Binary download failed (${response.status})`);
    let size = 0;
    const limit = new Transform({ transform(chunk, encoding, done) {
      size += chunk.length;
      done(size > MAX_BINARY_BYTES ? new Error('Binary download exceeds size limit') : null, chunk);
    } });
    const output = createWriteStream(temporary, { flags: 'wx', mode: 0o600 });
    output.once('open', () => { created = true; });
    await pipeline(Readable.fromWeb(response.body), limit, output);
    await verifyFile(temporary, expected);
    chmodSync(temporary, 0o755);
    renameSync(temporary, destination);
    created = false;
  } finally {
    if (created) unlinkSync(temporary);
  }
}
