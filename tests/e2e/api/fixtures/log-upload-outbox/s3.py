"""Minimal local S3 HTTP mock: HEAD/GET remain available while PUT can fail."""
import io
import json
import pathlib
import socket
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import unquote, urlsplit, parse_qs

LOCK=threading.RLock()
MODE='ok'
ATTEMPTS=[]
DELETE_ATTEMPTS=[]
ROOT=pathlib.Path('/objects')
RESULTS=pathlib.Path('/results')

class Handler(BaseHTTPRequestHandler):
    """Emulate the S3 operations and controllable write failures used by the fixture."""
    protocol_version='HTTP/1.1'
    def log_message(self, *args):
        """Suppress routine HTTP access logs from the mock S3 service."""
        pass
    def reply(self, status, body=b'', kind='application/json'):
        """Send a response with content length and preserve HEAD and gzip semantics."""
        self.send_response(status)
        self.send_header('Content-Type',kind)
        self.send_header('Content-Length',str(len(body)))
        if body.startswith(b'\x1f\x8b'): self.send_header('Content-Encoding','gzip')
        self.end_headers()
        if self.command!='HEAD': self.wfile.write(body)
    def object_key(self):
        """Decode the bucket and object path from the incoming request URL."""
        return unquote(urlsplit(self.path).path).lstrip('/')
    def object_path(self):
        """Resolve the requested object beneath the fixture storage directory."""
        path=(ROOT/self.object_key()).resolve()
        assert path.is_relative_to(ROOT)
        return path
    def body(self):
        """Read the request body and decode HTTP or AWS chunk framing when present."""
        if self.headers.get('Transfer-Encoding','').lower()=='chunked':
            raw=self.read_chunks(self.rfile)
        else: raw=self.rfile.read(int(self.headers.get('Content-Length','0')))
        if 'aws-chunked' in self.headers.get('Content-Encoding',''):
            raw=self.read_chunks(io.BytesIO(raw))
        return raw
    def read_chunks(self, stream):
        """Collect chunked bytes, consuming chunk extensions and trailing headers."""
        chunks=[]
        while True:
            line=stream.readline().strip()
            size=int(line.split(b';',1)[0],16)
            if size==0:
                while stream.readline().strip(): pass
                break
            chunks.append(stream.read(size))
            assert stream.read(2)==b'\r\n'
        return b''.join(chunks)
    def do_HEAD(self):
        """Report storage availability even while simulated writes are failing."""
        self.reply(200)
    def do_GET(self):
        """Expose control state, list stored objects, or serve an object payload."""
        if self.path.startswith('/control'):
            with LOCK:
                data={'mode':MODE,'objects':[str(p.relative_to(ROOT)) for p in ROOT.rglob('*') if p.is_file()],'put_attempts':list(ATTEMPTS),'delete_attempts':list(DELETE_ATTEMPTS)}
            return self.reply(200,json.dumps(data).encode())
        query=parse_qs(urlsplit(self.path).query)
        if query.get('list-type')==['2']:
            prefix=query.get('prefix',[''])[0]
            contents=''.join('<Contents><Key>'+str(p.relative_to(ROOT)).split('/',1)[-1]+'</Key><LastModified>2026-10-09T00:00:00Z</LastModified><Size>'+str(p.stat().st_size)+'</Size></Contents>' for p in ROOT.rglob('*') if p.is_file() and str(p.relative_to(ROOT)).split('/',1)[-1].startswith(prefix))
            xml='<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>bifrost-test-logs</Name><IsTruncated>false</IsTruncated>'+contents+'</ListBucketResult>'
            return self.reply(200,xml.encode(),'application/xml')
        target=self.object_path()
        if target.is_file(): return self.reply(200,target.read_bytes())
        self.reply(404,b'<Error><Code>NoSuchKey</Code><Message>Object does not exist</Message></Error>','application/xml')
    def do_POST(self):
        """Set the simulated storage failure mode through the control endpoint."""
        global MODE
        values=json.loads(self.body())
        assert self.path=='/control'
        assert values['mode'] in ['ok','denied','unavailable','disconnect']
        with LOCK: MODE=values['mode']
        self.reply(200,json.dumps({'mode':MODE}).encode())
    def do_PUT(self):
        """Record an upload attempt and store or reject it according to the failure mode."""
        raw=self.body()
        with LOCK: mode=MODE
        status={'ok':200,'denied':403,'unavailable':503,'disconnect':0}[mode]
        attempt={'time':time.time(),'method':'PUT','path':self.path,'mode':mode,'status':status,'body_bytes':len(raw)}
        with LOCK:
            ATTEMPTS.append(attempt)
            with (RESULTS/'s3-attempts.jsonl').open('a') as f: f.write(json.dumps(attempt)+'\n')
        if mode=='disconnect':
            self.close_connection=True
            self.connection.shutdown(socket.SHUT_RDWR)
            self.connection.close()
            return
        if mode!='ok':
            code='AccessDenied' if mode=='denied' else 'ServiceUnavailable'
            message='Mock S3 is read-only for maintenance' if mode=='denied' else 'Mock S3 temporary service failure'
            error=f'<Error><Code>{code}</Code><Message>{message}</Message><RequestId>mock-s3-request</RequestId></Error>'
            return self.reply(status,error.encode(),'application/xml')
        target=self.object_path()
        target.parent.mkdir(parents=True,exist_ok=True)
        target.write_bytes(raw)
        self.reply(200,b'','application/xml')
    def do_DELETE(self):
        """Record a deletion attempt and retain the object while deletes are denied."""
        with LOCK:
            mode=MODE
            status=204 if mode=='ok' else 403
            DELETE_ATTEMPTS.append({'path':self.path,'status':status})
        if status!=204:
            return self.reply(status,b'<Error><Code>AccessDenied</Code><Message>Mock S3 is read-only</Message></Error>','application/xml')
        self.object_path().unlink(missing_ok=True)
        self.reply(204,b'','application/xml')

ThreadingHTTPServer(('0.0.0.0',8080),Handler).serve_forever()
