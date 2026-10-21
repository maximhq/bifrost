import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtempSync, writeFileSync, readFileSync, readdirSync, rmSync, symlinkSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const payload = Buffer.from('synthetic harmless executable fixture');
const digest = createHash('sha256').update(payload).digest('hex');
const url = 'https://downloads.getmaxim.ai/bifrost/v1.2.3/linux/amd64/bifrost-http';
for (const packageName of ['bifrost', 'bifrost-cli']) {
  const security = await import(`../../npx/${packageName}/verify.js`);
  test(`${packageName}: absent or malformed checksums fail closed`, async () => {
    for (const text of ['', 'not-a-hash', `${digest}\n${digest}`, 'x'.repeat(1025)]) {
      assert.throws(() => security.parseChecksum(text));
    }
    assert.equal(security.parseChecksum(`${digest}  binary\n`), digest);
    await assert.rejects(security.getExpectedHash(`${url}.sha256`, async () => new Response('', { status: 404 })));
    await assert.rejects(security.getExpectedHash(`${url}.sha256`, async () => new Response('x'.repeat(2048))));
    await assert.rejects(security.getExpectedHash('https://attacker.invalid/checksum'));
  });
  test(`${packageName}: failed verification preserves the installed file`, async () => {
    const directory = mkdtempSync(join(tmpdir(), 'bifrost-verification-'));
    const destination = join(directory, 'binary');
    try {
      writeFileSync(destination, 'previous approved release');
      await assert.rejects(security.downloadVerified(url, destination, digest, async (_, options) => {
        assert.equal(options.redirect, 'error');
        return new Response('substituted bytes');
      }));
      assert.equal(readFileSync(destination, 'utf8'), 'previous approved release');
      assert.deepEqual(readdirSync(directory), ['binary']);
      await security.downloadVerified(url, destination, digest, async () => new Response(payload));
      await security.verifyFile(destination, digest);
      // The symlink points at a file that verifies, so only O_NOFOLLOW can reject it.
      symlinkSync(destination, join(directory, 'link'));
      await assert.rejects(security.verifyFile(join(directory, 'link'), digest), process.platform === 'win32' ? undefined : /ELOOP/);
      writeFileSync(destination, 'tampered cache');
      await assert.rejects(security.verifyFile(destination, digest), /mismatch/);
      writeFileSync(destination, Buffer.alloc(0));
      await assert.rejects(security.verifyFile(destination, digest), /Invalid binary file/);
    } finally {
      rmSync(directory, { recursive: true, force: true });
    }
  });
}
for (const packageName of ['bifrost', 'bifrost-cli']) {
  const security = await import(`../../npx/${packageName}/verify.js`);
  const listing = `${digest}  linux/amd64/bifrost-http\n${'0'.repeat(64)}  darwin/arm64/bifrost-http\n`;
  const serve = (body, status = 200, finalUrl = 'https://objects.githubusercontent.com/x') => async () =>
    Object.defineProperty(new Response(body, { status }), 'url', { value: finalUrl });
  test(`${packageName}: the release checksum list is parsed strictly`, () => {
    assert.equal(security.parseChecksumList(listing).get('linux/amd64/bifrost-http'), digest);
    for (const text of ['', 'garbage', `${digest}\n`, `${digest}  a b`]) {
      assert.throws(() => security.parseChecksumList(text));
    }
  });
  test(`${packageName}: both sources must agree before a download is trusted`, async () => {
    await assert.rejects(security.getReleaseHash('../evil', 'linux/amd64/bifrost-http'), /Invalid release tag/);
    await assert.rejects(security.getReleaseHash('transports/v1.2.3', 'linux/amd64/bifrost-http', serve(listing, 200, 'https://attacker.invalid/x')), /left GitHub/);
    await assert.rejects(security.getReleaseHash('transports/v1.2.3', 'linux/amd64/bifrost-http', serve('', 404)), /404/);
    await assert.rejects(security.getReleaseHash('transports/v1.2.3', 'windows/amd64/bifrost-http.exe', serve(listing)), /publishes no checksum/);
    await assert.rejects(security.getReleaseHash('transports/v1.2.3', 'linux/amd64/bifrost-http', serve('x'.repeat(70 * 1024))), /Oversized/);
    assert.equal(await security.getReleaseHash('transports/v1.2.3', 'linux/amd64/bifrost-http', serve(listing)), digest);
    const both = (storeText) => async (requested, options) => {
      if (requested.endsWith('.sha256')) {
        assert.equal(options.redirect, 'error');
        return new Response(storeText);
      }
      return serve(listing)();
    };
    assert.equal(await security.getAgreedHash(url, 'transports/v1.2.3', 'linux/amd64/bifrost-http', both(`${digest}  bifrost-http\n`)), digest);
    await assert.rejects(security.getAgreedHash(url, 'transports/v1.2.3', 'linux/amd64/bifrost-http', both(`${'f'.repeat(64)}  bifrost-http\n`)), /disagree/);
  });
}
test('both separately published launchers use identical verification code', () => {
  assert.deepEqual(readFileSync(new URL('../../npx/bifrost/verify.js', import.meta.url)),
                   readFileSync(new URL('../../npx/bifrost-cli/verify.js', import.meta.url)));
});
