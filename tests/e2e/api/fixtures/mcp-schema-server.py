#!/usr/bin/env python3
"""Local MCP fixture for the provider harness schema-projection regression.

Run: python3 tests/e2e/api/fixtures/mcp-schema-server.py
Register http://127.0.0.1:3013/mcp as an HTTP MCP client named schema_projection
with auth_type=none, tools_to_execute=["*"], tools_to_auto_execute=[], and
is_ping_available=true in the gateway's authenticated admin UI. The gateway
must allow local-network MCP targets. The fixture binds loopback only.
Then run the provider harness with INCLUDE_PREVIEW=1 and FEATURE="schema projection".
No tool execution is needed: the harness sends tool_choice=none and checks the
actual provider-bound tool definitions in extra_fields.raw_request.
"""
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

TOOLS = [
    {"name": "boolean_schema", "description": "Schema projection fixture", "inputSchema": True, "outputSchema": False},
    {"name": "union_schema", "description": "Schema projection fixture", "inputSchema": {"type": ["object", "null"]}},
    {"name": "untyped_schema", "description": "Schema projection fixture", "inputSchema": {"properties": {"value": {"type": "string"}}}},
]

class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
        if "id" not in request:
            self.send_response(202)
            self.end_headers()
            return
        method = request.get("method")
        if method == "initialize":
            result = {"protocolVersion": request.get("params", {}).get("protocolVersion", "2025-03-26"), "serverInfo": {"name": "schema-projection-fixture", "version": "1"}, "capabilities": {"tools": {}}}
        elif method == "tools/list":
            result = {"tools": TOOLS}
        elif method == "ping":
            result = {}
        else:
            result = None
        response = {"jsonrpc": "2.0", "id": request["id"]}
        if result is None:
            response["error"] = {"code": -32601, "message": "Method not supported by discovery-only fixture"}
        else:
            response["result"] = result
        body = json.dumps(response).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", 3013), Handler).serve_forever()
