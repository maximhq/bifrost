// Unit tests for the relay matrix's local proxies. Run directly: `node local-proxies.test.mjs`.
// Loopback only: no Bifrost, no credentials.
import assert from "node:assert";
import http from "node:http";
import net from "node:net";
import { startHttpProxy, startSocks5Proxy } from "./local-proxies.mjs";

let passed = 0;
async function test(name, fn) {
	await fn();
	passed++;
	console.log(`  ok - ${name}`);
}

const target = http.createServer((req, res) => {
	res.writeHead(200, { "content-type": "application/json" });
	res.end(JSON.stringify({ path: req.url, via: req.headers.via ?? null }));
});
await new Promise((r) => target.listen(0, "127.0.0.1", r));
const targetPort = target.address().port;

function viaHttpProxy(proxyURL, auth) {
	return new Promise((resolve, reject) => {
		const u = new URL(proxyURL);
		const headers = auth ? { "proxy-authorization": `Basic ${Buffer.from(auth).toString("base64")}` } : {};
		const req = http.request({ host: u.hostname, port: u.port, method: "GET", path: `http://127.0.0.1:${targetPort}/x?y=1`, headers }, (res) => {
			let body = "";
			res.on("data", (c) => (body += c));
			res.on("end", () => resolve({ status: res.statusCode, body }));
		});
		req.on("error", reject);
		req.end();
	});
}

function viaSocks(proxyURL, user, pass) {
	return new Promise((resolve, reject) => {
		const u = new URL(proxyURL);
		const s = net.connect(Number(u.port), u.hostname);
		let stage = 0;
		let data = Buffer.alloc(0);
		s.on("connect", () => s.write(Buffer.from([0x05, 0x01, 0x02])));
		s.on("data", (chunk) => {
			if (stage === 0) {
				stage = 1;
				s.write(Buffer.concat([Buffer.from([0x01, user.length]), Buffer.from(user), Buffer.from([pass.length]), Buffer.from(pass)]));
				return;
			}
			if (stage === 1) {
				if (chunk[1] !== 0x00) {
					resolve({ authorized: false });
					s.destroy();
					return;
				}
				stage = 2;
				const req = Buffer.alloc(10);
				req.set([0x05, 0x01, 0x00, 0x01, 127, 0, 0, 1]);
				req.writeUInt16BE(targetPort, 8);
				s.write(req);
				return;
			}
			if (stage === 2) {
				stage = 3;
				s.write(`GET /socks HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n`);
				return;
			}
			data = Buffer.concat([data, chunk]);
		});
		s.on("end", () => resolve({ authorized: true, text: data.toString() }));
		s.on("error", reject);
	});
}

const httpProxy = await startHttpProxy({ user: "u", pass: "p" });
const socks = await startSocks5Proxy({ user: "u", pass: "p" });
try {
	await test("the HTTP proxy forwards an authorized absolute-form request and records it", async () => {
		const res = await viaHttpProxy(httpProxy.url, "u:p");
		assert.strictEqual(res.status, 200);
		assert.strictEqual(JSON.parse(res.body).path, "/x?y=1");
		assert.strictEqual(JSON.parse(res.body).via, "1.1 relay-matrix-proxy");
		assert.deepStrictEqual(httpProxy.hits.at(-1), { kind: "absolute", target: `127.0.0.1:${targetPort}`, authorized: true });
	});

	await test("the HTTP proxy answers 407 to wrong credentials", async () => {
		const res = await viaHttpProxy(httpProxy.url, "u:wrong");
		assert.strictEqual(res.status, 407);
		assert.strictEqual(httpProxy.hits.at(-1).authorized, false);
	});

	await test("the SOCKS5 proxy tunnels an authorized CONNECT and records the target", async () => {
		const res = await viaSocks(socks.url, "u", "p");
		assert.ok(res.text.includes('"path":"/socks"'), res.text);
		assert.deepStrictEqual(socks.hits.at(-1), { target: `127.0.0.1:${targetPort}`, authorized: true });
	});

	await test("the SOCKS5 proxy refuses wrong credentials", async () => {
		const res = await viaSocks(socks.url, "u", "nope");
		assert.strictEqual(res.authorized, false);
		assert.strictEqual(socks.hits.at(-1).authorized, false);
	});
} finally {
	await httpProxy.close();
	await socks.close();
	target.close();
}
console.log(`local-proxies: ${passed} passed`);
