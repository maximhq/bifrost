// A minimal S3-compatible object store for E2E runs of the hybrid log store, so object storage is
// exercised end to end without docker or a cloud bucket. Bifrost's S3 backend
// (framework/objectstore/s3.go) uses six operations, and this serves exactly those, path-style:
//
//   HEAD   /<bucket>                       HeadBucket (the startup ping)
//   PUT    /<bucket>/<key>                 PutObject, with Content-Encoding and x-amz-tagging kept
//   GET    /<bucket>/<key>                 GetObject
//   DELETE /<bucket>/<key>                 DeleteObject
//   POST   /<bucket>?delete                DeleteObjects
//   GET    /<bucket>?list-type=2&prefix=   ListObjectsV2
//
// Signatures are not checked: the gateway is configured with static dummy credentials and an
// endpoint override (force_path_style), which is all a test bucket needs. A body the AWS SDK sends
// in aws-chunked framing (flexible checksums) is unwrapped, so the stored bytes are the payload.
//
// The objects map is the inspection surface for a runner in the same process; a runner in another
// process reads GET /__fake-s3/objects?prefix= (JSON, bodies base64-encoded).
import http from "node:http";
import { createHash } from "node:crypto";

function readBody(req) {
	return new Promise((resolve, reject) => {
		const chunks = [];
		req.on("data", (c) => chunks.push(c));
		req.on("end", () => resolve(Buffer.concat(chunks)));
		req.on("error", reject);
	});
}

// decodeAwsChunked unwraps an aws-chunked body: "<hex size>[;chunk-signature=...]\r\n<data>\r\n"
// repeated, ending with a zero-size chunk and optional trailer headers.
export function decodeAwsChunked(buf) {
	const out = [];
	let i = 0;
	while (i < buf.length) {
		const lineEnd = buf.indexOf("\r\n", i);
		if (lineEnd < 0) throw new Error("aws-chunked: missing chunk header terminator");
		const header = buf.subarray(i, lineEnd).toString("latin1");
		const size = parseInt(header.split(";")[0].trim(), 16);
		if (!Number.isFinite(size) || size < 0) throw new Error(`aws-chunked: bad chunk size ${JSON.stringify(header)}`);
		i = lineEnd + 2;
		if (size === 0) break;
		out.push(buf.subarray(i, i + size));
		i += size + 2;
	}
	return Buffer.concat(out);
}

function isAwsChunked(req) {
	const encoding = String(req.headers["content-encoding"] || "").toLowerCase();
	const sha = String(req.headers["x-amz-content-sha256"] || "");
	return encoding.split(",").map((s) => s.trim()).includes("aws-chunked") || sha.startsWith("STREAMING-");
}

// storedEncoding drops the aws-chunked token from Content-Encoding: it describes the transfer
// framing, not the object, so a gzip object keeps only "gzip".
function storedEncoding(req) {
	return String(req.headers["content-encoding"] || "")
		.split(",")
		.map((s) => s.trim())
		.filter((s) => s && s.toLowerCase() !== "aws-chunked")
		.join(",");
}

function xmlEscape(s) {
	return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

function sendXML(res, status, body) {
	const payload = `<?xml version="1.0" encoding="UTF-8"?>\n${body}`;
	res.writeHead(status, { "content-type": "application/xml", "content-length": Buffer.byteLength(payload) });
	res.end(payload);
}

function noSuchKey(res, key) {
	sendXML(res, 404, `<Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message><Key>${xmlEscape(key)}</Key></Error>`);
}

// startFakeS3 listens on loopback and serves one bucket. It resolves to { url, bucket, objects,
// requests, close }: objects maps key -> { body, contentType, contentEncoding, tags, lastModified },
// and requests counts operations by name.
export async function startFakeS3({ bucket = "bifrost-e2e", host = "127.0.0.1" } = {}) {
	const objects = new Map();
	const requests = { HeadBucket: 0, PutObject: 0, GetObject: 0, DeleteObject: 0, DeleteObjects: 0, ListObjectsV2: 0 };
	const server = http.createServer(async (req, res) => {
		try {
			const url = new URL(req.url || "/", "http://fake-s3");
			if (url.pathname === "/__fake-s3/objects") {
				// Inspection for runners in another process: every stored object under a prefix,
				// body base64-encoded.
				const prefix = url.searchParams.get("prefix") || "";
				const list = [...objects.entries()]
					.filter(([k]) => k.startsWith(prefix))
					.map(([k, o]) => ({ key: k, size: o.body.length, contentType: o.contentType, contentEncoding: o.contentEncoding, tags: o.tags, body: o.body.toString("base64") }));
				res.writeHead(200, { "content-type": "application/json" });
				res.end(JSON.stringify({ objects: list, requests }));
				return;
			}
			const parts = url.pathname.split("/").filter(Boolean);
			const reqBucket = decodeURIComponent(parts[0] || "");
			const key = parts.slice(1).map(decodeURIComponent).join("/");
			const raw = await readBody(req);
			if (reqBucket !== bucket) {
				sendXML(res, 404, `<Error><Code>NoSuchBucket</Code><Message>no bucket ${xmlEscape(reqBucket)}</Message></Error>`);
				return;
			}
			if (!key) {
				if (req.method === "HEAD") {
					requests.HeadBucket++;
					res.writeHead(200);
					res.end();
					return;
				}
				if (req.method === "POST" && url.searchParams.has("delete")) {
					requests.DeleteObjects++;
					const keys = [...raw.toString("utf8").matchAll(/<Key>([\s\S]*?)<\/Key>/g)].map((m) =>
						m[1].replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&quot;/g, '"').replace(/&amp;/g, "&"),
					);
					for (const k of keys) objects.delete(k);
					sendXML(res, 200, `<DeleteResult>${keys.map((k) => `<Deleted><Key>${xmlEscape(k)}</Key></Deleted>`).join("")}</DeleteResult>`);
					return;
				}
				if (req.method === "GET" && url.searchParams.get("list-type") === "2") {
					requests.ListObjectsV2++;
					const prefix = url.searchParams.get("prefix") || "";
					const keys = [...objects.keys()].filter((k) => k.startsWith(prefix)).sort();
					const contents = keys
						.map((k) => {
							const o = objects.get(k);
							return `<Contents><Key>${xmlEscape(k)}</Key><LastModified>${o.lastModified.toISOString()}</LastModified><ETag>"${o.etag}"</ETag><Size>${o.body.length}</Size><StorageClass>STANDARD</StorageClass></Contents>`;
						})
						.join("");
					sendXML(
						res,
						200,
						`<ListBucketResult><Name>${xmlEscape(bucket)}</Name><Prefix>${xmlEscape(prefix)}</Prefix><KeyCount>${keys.length}</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>${contents}</ListBucketResult>`,
					);
					return;
				}
				sendXML(res, 400, `<Error><Code>NotImplemented</Code><Message>${xmlEscape(req.method)} on bucket</Message></Error>`);
				return;
			}
			if (req.method === "PUT") {
				requests.PutObject++;
				const body = isAwsChunked(req) ? decodeAwsChunked(raw) : raw;
				const etag = createHash("md5").update(body).digest("hex");
				const tags = Object.fromEntries(new URLSearchParams(String(req.headers["x-amz-tagging"] || "")).entries());
				objects.set(key, {
					body,
					etag,
					contentType: String(req.headers["content-type"] || "application/octet-stream"),
					contentEncoding: storedEncoding(req),
					tags,
					lastModified: new Date(),
				});
				res.writeHead(200, { etag: `"${etag}"` });
				res.end();
				return;
			}
			if (req.method === "GET" || req.method === "HEAD") {
				const o = objects.get(key);
				if (!o) {
					requests.GetObject++;
					noSuchKey(res, key);
					return;
				}
				requests.GetObject++;
				const headers = { "content-type": o.contentType, "content-length": o.body.length, etag: `"${o.etag}"`, "last-modified": o.lastModified.toUTCString() };
				if (o.contentEncoding) headers["content-encoding"] = o.contentEncoding;
				res.writeHead(200, headers);
				res.end(req.method === "HEAD" ? undefined : o.body);
				return;
			}
			if (req.method === "DELETE") {
				requests.DeleteObject++;
				objects.delete(key);
				res.writeHead(204);
				res.end();
				return;
			}
			sendXML(res, 400, `<Error><Code>NotImplemented</Code><Message>${xmlEscape(req.method)} on object</Message></Error>`);
		} catch (err) {
			sendXML(res, 500, `<Error><Code>InternalError</Code><Message>${xmlEscape(err.message)}</Message></Error>`);
		}
	});
	await new Promise((resolve, reject) => {
		server.once("error", reject);
		server.listen(0, host, () => {
			server.off("error", reject);
			resolve();
		});
	});
	const { port } = server.address();
	return {
		url: `http://${host}:${port}`,
		bucket,
		objects,
		requests,
		close: () => new Promise((resolve) => server.close(() => resolve())),
	};
}

// s3ObjectStorageConfig is the logs_store.object_storage block that points a gateway at a fake S3.
export function s3ObjectStorageConfig(fake, prefix = "bifrost") {
	return {
		type: "s3",
		bucket: fake.bucket,
		prefix,
		region: "us-east-1",
		endpoint: fake.url,
		force_path_style: true,
		access_key_id: "fake-access-key",
		secret_access_key: "fake-secret-key",
	};
}
