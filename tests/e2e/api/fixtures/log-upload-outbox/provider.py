import json, time, uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
class Handler(BaseHTTPRequestHandler):
    """Serve deterministic model metadata and chat completions for the local fixture."""
    def log_message(self, *args):
        """Suppress routine HTTP access logs from the mock provider."""
        pass
    def send_json(self, data):
        """Send a successful JSON response with an explicit content length."""
        raw=json.dumps(data).encode()
        self.send_response(200)
        self.send_header("Content-Type","application/json")
        self.send_header("Content-Length",str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)
    def do_GET(self):
        """Return the model list used by gateway startup and discovery."""
        self.send_json({"object":"list","data":[{"id":"gpt-4o-mini","object":"model","created":1,"owned_by":"local-mock"}]})
    def do_POST(self):
        """Return a deterministic completion derived from the final request message."""
        body=json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        answer="ASSISTANT-RESPONSE-"+body["messages"][-1]["content"]
        self.send_json({"id":"chatcmpl-"+uuid.uuid4().hex,"object":"chat.completion","created":int(time.time()),"model":body["model"],"choices":[{"index":0,"message":{"role":"assistant","content":answer},"finish_reason":"stop"}],"usage":{"prompt_tokens":42,"completion_tokens":15,"total_tokens":57}})
ThreadingHTTPServer(("0.0.0.0",8080),Handler).serve_forever()
