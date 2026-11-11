// Unit tests for the fake S3 used by the log-store matrix. Run directly: `node fake-s3.test.mjs`.
// Loopback only: no Bifrost, no credentials.
import assert from "node:assert";
import { gzipSync } from "node:zlib";
import { decodeAwsChunked, s3ObjectStorageConfig, startFakeS3 } from "./fake-s3.mjs";

let passed = 0;
async function test(name, fn) {
	await fn();
	passed++;
	console.log(`  ok - ${name}`);
}

await test("aws-chunked bodies are unwrapped to their payload", () => {
	const framed = Buffer.from("5;chunk-signature=abc\r\nhello\r\n6\r\n world\r\n0\r\nx-amz-checksum-crc32:AAAA\r\n\r\n");
	assert.strictEqual(decodeAwsChunked(framed).toString(), "hello world");
});

await test("an unsigned trailer body with no extensions decodes too", () => {
	assert.strictEqual(decodeAwsChunked(Buffer.from("3\r\nabc\r\n0\r\n\r\n")).toString(), "abc");
});

const s3 = await startFakeS3({ bucket: "unit" });
const base = `${s3.url}/unit`;
try {
	await test("HeadBucket answers 200 for the bucket and 404 for another", async () => {
		assert.strictEqual((await fetch(base, { method: "HEAD" })).status, 200);
		assert.strictEqual((await fetch(`${s3.url}/other`, { method: "HEAD" })).status, 404);
	});

	await test("PutObject keeps the body, content encoding and tags; GetObject returns them", async () => {
		const put = await fetch(`${base}/a/b%20c.json`, {
			method: "PUT",
			body: gzipSync("{}"),
			headers: { "content-type": "application/json", "content-encoding": "gzip", "x-amz-tagging": "kind=call&relay_name=r1" },
		});
		assert.strictEqual(put.status, 200);
		const stored = s3.objects.get("a/b c.json");
		assert.deepStrictEqual(stored.tags, { kind: "call", relay_name: "r1" });
		assert.strictEqual(stored.contentEncoding, "gzip");
		const get = await fetch(`${base}/a/b%20c.json`);
		assert.strictEqual(get.headers.get("content-encoding"), "gzip");
		assert.strictEqual(await get.text(), "{}", "fetch gunzips the stored gzip body back to the payload");
	});

	await test("an aws-chunked PutObject stores the payload and drops the framing encoding", async () => {
		await fetch(`${base}/chunked`, {
			method: "PUT",
			body: "4\r\ndata\r\n0\r\n\r\n",
			headers: { "content-encoding": "aws-chunked", "x-amz-content-sha256": "STREAMING-UNSIGNED-PAYLOAD-TRAILER" },
		});
		assert.strictEqual(s3.objects.get("chunked").body.toString(), "data");
		assert.strictEqual(s3.objects.get("chunked").contentEncoding, "");
	});

	await test("ListObjectsV2 lists by prefix", async () => {
		const xml = await (await fetch(`${base}?list-type=2&prefix=a/`)).text();
		assert.match(xml, /<Key>a\/b c\.json<\/Key>/);
		assert.doesNotMatch(xml, /<Key>chunked<\/Key>/);
	});

	await test("the inspection endpoint lists stored objects with their bodies", async () => {
		const res = await (await fetch(`${s3.url}/__fake-s3/objects?prefix=a/`)).json();
		assert.strictEqual(res.objects.length, 1);
		assert.strictEqual(res.objects[0].key, "a/b c.json");
		assert.strictEqual(res.requests.PutObject >= 1, true);
	});

	await test("GetObject of a missing key is NoSuchKey", async () => {
		const res = await fetch(`${base}/missing`);
		assert.strictEqual(res.status, 404);
		assert.match(await res.text(), /NoSuchKey/);
	});

	await test("DeleteObject and DeleteObjects remove keys", async () => {
		await fetch(`${base}/chunked`, { method: "DELETE" });
		assert.ok(!s3.objects.has("chunked"));
		await fetch(`${base}/x`, { method: "PUT", body: "1" });
		await fetch(`${base}?delete`, { method: "POST", body: "<Delete><Object><Key>x</Key></Object><Object><Key>a/b c.json</Key></Object></Delete>" });
		assert.strictEqual(s3.objects.size, 0);
	});

	await test("the object_storage block points a gateway at the fake bucket", () => {
		const cfg = s3ObjectStorageConfig(s3);
		assert.strictEqual(cfg.endpoint, s3.url);
		assert.strictEqual(cfg.bucket, "unit");
		assert.strictEqual(cfg.force_path_style, true);
	});
} finally {
	await s3.close();
}
console.log(`fake-s3: ${passed} passed`);
