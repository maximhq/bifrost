// Local forward proxies for the relay matrix's proxy suite: an HTTP proxy (absolute-form requests
// and CONNECT tunnels) and a SOCKS5 proxy, both requiring a username and password. Each records the
// targets it was asked to reach, so a case can prove a relay's traffic went through it (or did not).
//
// They are loopback-only test doubles, not general proxies: no keep-alive, no chunked upstream
// framing beyond what node's http module handles. The real squid/stunnel/microsocks matrix
// (proxy/run-proxy-matrix.sh) covers production proxies; this covers relay proxy configuration
// without installing them or spending provider calls.
import http from "node:http";
import net from "node:net";

function basicOK(header, user, pass) {
	if (!header?.startsWith("Basic ")) return false;
	return Buffer.from(header.slice(6), "base64").toString("utf8") === `${user}:${pass}`;
}

function listenLoopback(server) {
	return new Promise((resolve, reject) => {
		server.once("error", reject);
		server.listen(0, "127.0.0.1", () => {
			server.off("error", reject);
			resolve(server.address().port);
		});
	});
}

// startHttpProxy resolves to { url, hits, close }. hits holds { kind: "absolute"|"connect",
// target, authorized } for every request the proxy received.
export async function startHttpProxy({ user, pass }) {
	const hits = [];
	const sockets = new Set();
	const server = http.createServer((req, res) => {
		const authorized = basicOK(req.headers["proxy-authorization"], user, pass);
		let target;
		try {
			target = new URL(req.url);
		} catch {
			res.writeHead(400);
			res.end("absolute-form request required");
			return;
		}
		hits.push({ kind: "absolute", target: target.host, authorized });
		if (!authorized) {
			res.writeHead(407, { "proxy-authenticate": 'Basic realm="relay-matrix"' });
			res.end();
			return;
		}
		const headers = { ...req.headers, via: "1.1 relay-matrix-proxy" };
		delete headers["proxy-authorization"];
		delete headers["proxy-connection"];
		const upstream = http.request({ host: target.hostname, port: target.port || 80, method: req.method, path: `${target.pathname}${target.search}`, headers }, (up) => {
			res.writeHead(up.statusCode ?? 502, up.headers);
			up.pipe(res);
		});
		upstream.on("error", () => {
			if (!res.headersSent) res.writeHead(502);
			res.end();
		});
		req.pipe(upstream);
	});
	server.on("connect", (req, client, head) => {
		const authorized = basicOK(req.headers["proxy-authorization"], user, pass);
		hits.push({ kind: "connect", target: req.url, authorized });
		if (!authorized) {
			client.end('HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm="relay-matrix"\r\n\r\n');
			return;
		}
		const [host, port] = req.url.split(":");
		const upstream = net.connect(Number(port), host, () => {
			client.write("HTTP/1.1 200 Connection Established\r\n\r\n");
			if (head?.length) upstream.write(head);
			upstream.pipe(client);
			client.pipe(upstream);
		});
		upstream.on("error", () => client.destroy());
		client.on("error", () => upstream.destroy());
	});
	server.on("connection", (s) => {
		sockets.add(s);
		s.on("close", () => sockets.delete(s));
	});
	const port = await listenLoopback(server);
	return {
		url: `http://127.0.0.1:${port}`,
		hits,
		close: () =>
			new Promise((resolve) => {
				for (const s of sockets) s.destroy();
				server.close(() => resolve());
			}),
	};
}

// startSocks5Proxy resolves to { url, hits, close }. It speaks RFC 1928 with RFC 1929
// username/password authentication and the CONNECT command; hits holds { target, authorized }.
export async function startSocks5Proxy({ user, pass }) {
	const hits = [];
	const sockets = new Set();
	const server = net.createServer((client) => {
		sockets.add(client);
		client.on("close", () => sockets.delete(client));
		client.on("error", () => {});
		let stage = "greeting";
		let buf = Buffer.alloc(0);
		let authorized = false;
		const onData = (chunk) => {
			buf = Buffer.concat([buf, chunk]);
			if (stage === "greeting") {
				if (buf.length < 2 || buf.length < 2 + buf[1]) return;
				const methods = [...buf.subarray(2, 2 + buf[1])];
				buf = buf.subarray(2 + buf[1]);
				if (!methods.includes(0x02)) {
					client.end(Buffer.from([0x05, 0xff]));
					return;
				}
				client.write(Buffer.from([0x05, 0x02]));
				stage = "auth";
			}
			if (stage === "auth") {
				if (buf.length < 2) return;
				const ulen = buf[1];
				if (buf.length < 2 + ulen + 1) return;
				const plen = buf[2 + ulen];
				if (buf.length < 3 + ulen + plen) return;
				const u = buf.subarray(2, 2 + ulen).toString("utf8");
				const p = buf.subarray(3 + ulen, 3 + ulen + plen).toString("utf8");
				buf = buf.subarray(3 + ulen + plen);
				authorized = u === user && p === pass;
				if (!authorized) {
					hits.push({ target: null, authorized: false });
					client.end(Buffer.from([0x01, 0x01]));
					return;
				}
				client.write(Buffer.from([0x01, 0x00]));
				stage = "request";
			}
			if (stage === "request") {
				if (buf.length < 5) return;
				const atyp = buf[3];
				let host;
				let offset;
				if (atyp === 0x01) {
					if (buf.length < 10) return;
					host = [...buf.subarray(4, 8)].join(".");
					offset = 8;
				} else if (atyp === 0x03) {
					const len = buf[4];
					if (buf.length < 5 + len + 2) return;
					host = buf.subarray(5, 5 + len).toString("utf8");
					offset = 5 + len;
				} else {
					client.end(Buffer.from([0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0]));
					return;
				}
				const port = buf.readUInt16BE(offset);
				const rest = buf.subarray(offset + 2);
				client.off("data", onData);
				hits.push({ target: `${host}:${port}`, authorized: true });
				const upstream = net.connect(port, host, () => {
					client.write(Buffer.from([0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0]));
					if (rest.length) upstream.write(rest);
					upstream.pipe(client);
					client.pipe(upstream);
				});
				upstream.on("error", () => client.destroy());
				stage = "tunnel";
			}
		};
		client.on("data", onData);
	});
	const port = await listenLoopback(server);
	return {
		url: `socks5://127.0.0.1:${port}`,
		hits,
		close: () =>
			new Promise((resolve) => {
				for (const s of sockets) s.destroy();
				server.close(() => resolve());
			}),
	};
}
