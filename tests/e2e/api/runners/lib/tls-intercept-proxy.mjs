import fs from "node:fs";
import http from "node:http";
import https from "node:https";
import os from "node:os";
import path from "node:path";
import tls from "node:tls";
import { execFileSync } from "node:child_process";

// Shared plumbing for the endpoint fixtures (vertex-endpoint-fixture.mjs, bedrock-endpoint-fixture.mjs).
// A fixture needs to see, and answer, requests a provider would send to a public cloud host, without
// any traffic leaving the machine. Two shapes cover the two kinds of provider client:
//
//   createInterceptProxy  for clients that honour proxy_config. The fixture is an HTTP proxy that
//                         terminates TLS for whatever host the client CONNECTs to.
//   createTlsEndpoints    for clients with endpoint overrides plus a trusted CA but no proxy_config
//                         (Bedrock). The fixture is a TLS server the client is pointed at directly.
//
// Either way a throwaway CA is minted; the gateway is told to trust it (proxy_config.ca_cert_pem or
// network_config.ca_cert_pem), and each decrypted request is recorded before respond() answers it.
//
// respond(request, body) -> { status, headers?, body | chunks } answers an intercepted origin request;
// request.socket.fixtureHost is the CONNECT target (proxy shape) and request.socket.localPort the
// endpoint port (direct shape). Control endpoints: GET /__connects returns {connects, requests};
// POST /__reset clears them.

const mintCa = () => {
	const work = fs.mkdtempSync(path.join(os.tmpdir(), "fixture-ca-"));
	const sh = (cmd, cmdArgs) => execFileSync(cmd, cmdArgs, { cwd: work, stdio: "pipe" });
	sh("openssl", ["req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", "ca.key", "-out", "ca.crt", "-days", "2", "-subj", "/CN=bifrost-fixture-ca", "-addext", "basicConstraints=critical,CA:TRUE", "-addext", "keyUsage=critical,keyCertSign,cRLSign"]);
	const caPem = fs.readFileSync(path.join(work, "ca.crt"), "utf8");
	const leaves = new Map();
	// sans is a list like ["DNS:host", "IP:127.0.0.1"]
	const leafFor = (name, sans) => {
		if (leaves.has(name)) return leaves.get(name);
		const base = name.replace(/[^a-z0-9.-]/gi, "_");
		fs.writeFileSync(path.join(work, `${base}.ext`), `subjectAltName=${sans.join(",")}\nextendedKeyUsage=serverAuth\n`);
		sh("openssl", ["req", "-newkey", "rsa:2048", "-nodes", "-keyout", `${base}.key`, "-out", `${base}.csr`, "-subj", `/CN=${name}`]);
		sh("openssl", ["x509", "-req", "-in", `${base}.csr`, "-CA", "ca.crt", "-CAkey", "ca.key", "-CAcreateserial", "-out", `${base}.crt`, "-days", "2", "-extfile", `${base}.ext`]);
		const leaf = { key: fs.readFileSync(path.join(work, `${base}.key`)), cert: fs.readFileSync(path.join(work, `${base}.crt`)) };
		leaves.set(name, leaf);
		return leaf;
	};
	return { caPem, leafFor };
};

const makeRecorder = (respond, staticJson = {}) => {
	const connects = [];
	const requests = [];
	const handler = (req, res) => {
		const chunks = [];
		req.on("data", (c) => chunks.push(c));
		req.on("end", () => {
			const bodyText = Buffer.concat(chunks).toString("utf8");
			requests.push({ host: req.socket.fixtureHost, port: req.socket.localPort, method: req.method, path: (req.url || "").replace(/key=[^&]*/, "key=REDACTED"), headers: req.headers, body: bodyText.slice(0, 4000) });
			const r = respond(req, bodyText) || { status: 404, body: JSON.stringify({ error: { message: "fixture does not serve " + req.url } }) };
			res.writeHead(r.status, { "Content-Type": "application/json", ...(r.headers || {}) });
			if (!r.chunks) return res.end(r.body);
			// chunks: [{ delayMs, data }] are written one at a time with a flush, so a case can see a stream arrive
			// incrementally (and outlive any whole-response timeout) instead of as one body.
			let i = 0;
			const next = () => {
				if (i >= r.chunks.length) return res.end();
				const c = r.chunks[i++];
				setTimeout(() => {
					res.write(c.data);
					next();
				}, c.delayMs || 0);
			};
			next();
		});
	};
	const control = (req, res) => {
		const send = (status, body) => {
			res.writeHead(status, { "Content-Type": "application/json" });
			res.end(JSON.stringify(body));
		};
		const url = new URL(req.url, "http://localhost");
		if (req.method === "GET" && staticJson[url.pathname] !== undefined) {
			return send(200, staticJson[url.pathname]);
		}
		if (req.method === "POST" && url.pathname === "/__reset") {
			connects.length = 0;
			requests.length = 0;
			return send(200, { data: [{ reset: true }] });
		}
		if (req.method === "GET" && url.pathname === "/__connects") {
			return send(200, { data: [{ connects: [...connects], requests: requests.map((r) => ({ ...r })) }] });
		}
		send(404, { error: { message: "not found" } });
	};
	return { connects, handler, control };
};

export function createInterceptProxy({ port, respond, route, staticJson }) {
	const { caPem, leafFor } = mintCa();
	const rec = makeRecorder(respond, staticJson);
	const origin = http.createServer(rec.handler);
	// The gateway pools connections through the proxy; an idle one the fixture had closed would make
	// the next request fail on a dead socket, so idle connections are never timed out here.
	origin.keepAliveTimeout = 0;

	const server = http.createServer((req, res) => {
		const url = new URL(req.url, "http://localhost");
		if (route && route(url, req, res)) return;
		rec.control(req, res);
	});
	server.keepAliveTimeout = 0;

	server.on("connect", (req, clientSocket, head) => {
		const host = String(req.url).split(":")[0];
		rec.connects.push(host);
		clientSocket.write("HTTP/1.1 200 Connection Established\r\n\r\n");
		if (head && head.length) clientSocket.unshift(head);
		const { key, cert } = leafFor(host, [`DNS:${host}`]);
		const tlsSocket = new tls.TLSSocket(clientSocket, { isServer: true, secureContext: tls.createSecureContext({ key, cert }), ALPNProtocols: ["http/1.1"] });
		tlsSocket.fixtureHost = host;
		tlsSocket.on("error", () => {});
		clientSocket.on("error", () => {});
		origin.emit("connection", tlsSocket);
	});

	return { caPem, listen: (cb) => server.listen(port, "127.0.0.1", cb) };
}

// createTlsEndpoints serves the same recorder over TLS on any number of ports (one per service
// endpoint, so a case can tell which service the gateway chose) and over plain HTTP on the control
// port, which newman can reach without trusting the CA. staticJson maps a control-port path to a JSON
// body, so a gateway can fetch offline stand-ins (for example its pricing datasheet) from the fixture.
export function createTlsEndpoints({ respond, staticJson }) {
	const { caPem, leafFor } = mintCa();
	const rec = makeRecorder(respond, staticJson);
	// Wildcards cover the virtual-hosted style S3 uses: Bifrost prepends the bucket name to an endpoint host that
	// carries a literal "bucket." prefix, giving <bucket>.bucket.localhost.
	const { key, cert } = leafFor("localhost", ["DNS:localhost", "DNS:*.localhost", "DNS:*.bucket.localhost", "IP:127.0.0.1"]);
	return {
		caPem,
		listenEndpoint: (port, cb) => {
			const srv = https.createServer({ key, cert, ALPNProtocols: ["http/1.1"] }, rec.handler);
			srv.keepAliveTimeout = 0;
			srv.on("secureConnection", (sock) => {
				sock.fixtureHost = "localhost:" + port;
			});
			srv.on("tlsClientError", () => {});
			srv.listen(port, "127.0.0.1", cb);
		},
		listenControl: (port, cb) => http.createServer(rec.control).listen(port, "127.0.0.1", cb),
	};
}

export const argValue = (args, name, fallback) => {
	const i = args.indexOf(name);
	return i >= 0 && args[i + 1] ? args[i + 1] : fallback;
};
