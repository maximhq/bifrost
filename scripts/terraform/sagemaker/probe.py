#!/usr/bin/env python3
"""Probe a SageMaker endpoint on every wire path Bifrost's sagemaker provider can use.

Stdlib only. Credentials come from AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY / AWS_SESSION_TOKEN,
else from `aws configure export-credentials` (profiles, SSO). --role-arn assumes a role first.

  probe.py run --endpoint EP --region R [--ic IC] [--kind chat|embedding] [--quick] [--expect openai|invocations]
  probe.py token --region R [--expires 3600]      # bearer token for curl / the OpenAI SDK
"""

import argparse
import base64
import datetime
import hashlib
import hmac
import http.client
import json
import os
import struct
import subprocess
import sys
import time
import urllib.parse

TOKEN_HOST = "sagemaker.amazonaws.com"
EMPTY_SHA256 = hashlib.sha256(b"").hexdigest()
MAX_SAVED_BODY = 64 * 1024
PROMPT = "Reply with one short sentence: what is 2 + 2?"
WEATHER_TOOL = {
    "type": "function",
    "function": {
        "name": "get_weather",
        "description": "Get the current weather for a city.",
        "parameters": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]},
    },
}


# --- credentials -------------------------------------------------------------------------------------------------


def load_credentials(role_arn=None, region=None):
    ak, sk, tok = os.environ.get("AWS_ACCESS_KEY_ID"), os.environ.get("AWS_SECRET_ACCESS_KEY"), os.environ.get("AWS_SESSION_TOKEN")
    if not (ak and sk):
        out = _aws(["configure", "export-credentials", "--format", "process"], os.environ)
        ak, sk, tok = out["AccessKeyId"], out["SecretAccessKey"], out.get("SessionToken")
    if role_arn:
        env = {k: v for k, v in os.environ.items() if k not in ("AWS_PROFILE", "AWS_SESSION_TOKEN")}
        env.update({"AWS_ACCESS_KEY_ID": ak, "AWS_SECRET_ACCESS_KEY": sk})
        if tok:
            env["AWS_SESSION_TOKEN"] = tok
        args = ["sts", "assume-role", "--role-arn", role_arn, "--role-session-name", "bifrost-sagemaker-probe", "--output", "json"]
        if region:
            args += ["--region", region]
        c = _aws(args, env)["Credentials"]
        ak, sk, tok = c["AccessKeyId"], c["SecretAccessKey"], c["SessionToken"]
    return {"access_key": ak, "secret_key": sk, "token": tok}


def _aws(args, env):
    proc = subprocess.run(["aws"] + args, env=env, capture_output=True, text=True)
    if proc.returncode != 0:
        sys.exit("aws %s failed: %s" % (" ".join(args[:2]), proc.stderr.strip()))
    return json.loads(proc.stdout)


# --- SigV4 -------------------------------------------------------------------------------------------------------


def _quote(s):
    return urllib.parse.quote(s, safe="-_.~")


def _hmac(key, msg):
    return hmac.new(key, msg.encode("utf-8"), hashlib.sha256).digest()


def _signature(creds, amz_date, region, service, canonical_request):
    scope = "%s/%s/%s/aws4_request" % (amz_date[:8], region, service)
    string_to_sign = "\n".join(["AWS4-HMAC-SHA256", amz_date, scope, hashlib.sha256(canonical_request.encode("utf-8")).hexdigest()])
    key = _hmac(("AWS4" + creds["secret_key"]).encode("utf-8"), amz_date[:8])
    for part in (region, service, "aws4_request"):
        key = _hmac(key, part)
    return scope, hmac.new(key, string_to_sign.encode("utf-8"), hashlib.sha256).hexdigest()


def mint_token(creds, region, expires=3600, now=None):
    """Port of sagemaker-core's generate_token: base64 of a SigV4 query-presigned CallWithBearerToken URL."""
    amz_date = (now or datetime.datetime.now(datetime.timezone.utc)).strftime("%Y%m%dT%H%M%SZ")
    scope = "%s/%s/sagemaker/aws4_request" % (amz_date[:8], region)
    params = [
        ("Action", "CallWithBearerToken"),
        ("X-Amz-Algorithm", "AWS4-HMAC-SHA256"),
        ("X-Amz-Credential", "%s/%s" % (creds["access_key"], scope)),
        ("X-Amz-Date", amz_date),
        ("X-Amz-Expires", str(expires)),
        ("X-Amz-SignedHeaders", "host"),
    ]
    if creds.get("token"):
        params.append(("X-Amz-Security-Token", creds["token"]))
    canonical_query = "&".join("%s=%s" % (_quote(k), _quote(v)) for k, v in sorted(params))
    canonical = "\n".join(["POST", "/", canonical_query, "host:%s\n" % TOKEN_HOST, "host", EMPTY_SHA256])
    _, sig = _signature(creds, amz_date, region, "sagemaker", canonical)
    query = "&".join("%s=%s" % (_quote(k), _quote(v)) for k, v in params)
    url = "%s/?%s&X-Amz-Signature=%s&Version=1" % (TOKEN_HOST, query, sig)
    return "sagemaker-api-key-" + base64.b64encode(url.encode("utf-8")).decode("ascii")


def sigv4_headers(creds, region, method, host, path, body, headers, now=None):
    amz_date = (now or datetime.datetime.now(datetime.timezone.utc)).strftime("%Y%m%dT%H%M%SZ")
    signed = {k.lower(): str(v).strip() for k, v in headers.items()}
    signed.update({"host": host, "x-amz-date": amz_date})
    if creds.get("token"):
        signed["x-amz-security-token"] = creds["token"]
    names = sorted(signed)
    canonical = "\n".join([
        method,
        urllib.parse.quote(path, safe="/-_.~"),
        "",
        "".join("%s:%s\n" % (n, " ".join(signed[n].split())) for n in names),
        ";".join(names),
        hashlib.sha256(body or b"").hexdigest(),
    ])
    scope, sig = _signature(creds, amz_date, region, "sagemaker", canonical)
    signed["authorization"] = "AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s" % (creds["access_key"], scope, ";".join(names), sig)
    del signed["host"]
    return signed


# --- AWS event stream (application/vnd.amazon.eventstream) -------------------------------------------------------


def decode_event_stream(data):
    """Split an event-stream body into (headers, payload) messages; CRCs are not checked."""
    messages, i = [], 0
    while i + 12 <= len(data):
        total, header_len = struct.unpack(">II", data[i : i + 8])
        if total < 16 or i + total > len(data):
            break
        messages.append((_event_headers(data[i + 12 : i + 12 + header_len]), data[i + 12 + header_len : i + total - 4]))
        i += total
    return messages, len(data) - i


def _event_headers(b):
    out, j = {}, 0
    fixed = {2: ">b", 3: ">h", 4: ">i", 5: ">q", 8: ">q"}
    while j < len(b):
        n = b[j]
        name = b[j + 1 : j + 1 + n].decode("utf-8")
        j += 1 + n
        t = b[j]
        j += 1
        if t in (0, 1):
            value = t == 0
        elif t in fixed:
            size = struct.calcsize(fixed[t])
            value = struct.unpack(fixed[t], b[j : j + size])[0]
            j += size
        elif t in (6, 7):
            (length,) = struct.unpack(">H", b[j : j + 2])
            raw = b[j + 2 : j + 2 + length]
            value = raw.decode("utf-8", "replace") if t == 7 else raw
            j += 2 + length
        elif t == 9:
            value = b[j : j + 16].hex()
            j += 16
        else:
            break
        out[name] = value
    return out


# --- HTTP --------------------------------------------------------------------------------------------------------


def http_call(host, method, path, headers, body, timeout):
    conn = http.client.HTTPSConnection(host, timeout=timeout)
    t0 = time.monotonic()
    try:
        conn.request(method, path, body=body, headers=headers)
        resp = conn.getresponse()
        ttfb = time.monotonic() - t0
        chunks = []
        while True:
            chunk = resp.read1(65536)
            if not chunk:
                break
            chunks.append(chunk)
        return {
            "status": resp.status,
            "headers": {k.lower(): v for k, v in resp.getheaders()},
            "body": b"".join(chunks),
            "ttfb_ms": int(ttfb * 1000),
            "ms": int((time.monotonic() - t0) * 1000),
        }
    except (OSError, http.client.HTTPException) as e:
        return {"status": 0, "headers": {}, "body": str(e).encode(), "ttfb_ms": 0, "ms": int((time.monotonic() - t0) * 1000)}
    finally:
        conn.close()


# --- response analysis -------------------------------------------------------------------------------------------


def _json(body):
    try:
        return json.loads(body)
    except ValueError:
        return None


def summarize_chat(body):
    doc = _json(body)
    if not isinstance(doc, dict) or not doc.get("choices"):
        return False, _excerpt(body)
    choice = doc["choices"][0]
    msg = choice.get("message") or {}
    text = msg.get("content") or choice.get("text") or ""
    reasoning = msg.get("reasoning_content") or msg.get("reasoning") or ""
    tools = msg.get("tool_calls") or []
    ok = bool(text or reasoning or tools)
    parts = ["finish=%s" % choice.get("finish_reason")]
    if tools:
        parts.append("tool_calls=%s" % ",".join(t.get("function", {}).get("name", "?") for t in tools))
    if reasoning:
        parts.append("reasoning=%d chars" % len(reasoning))
    parts.append("content=%r" % text[:60])
    return ok, " ".join(parts)


def summarize_embedding(body):
    doc = _json(body)
    vec = None
    if isinstance(doc, dict) and doc.get("data"):
        vec = doc["data"][0].get("embedding")
    elif isinstance(doc, dict) and "embedding" in doc:
        vec = doc["embedding"]
    elif isinstance(doc, list) and doc:
        vec = doc[0] if isinstance(doc[0], list) else doc
    if isinstance(vec, list) and vec and isinstance(vec[0], (int, float)):
        return True, "dim=%d" % len(vec)
    return False, _excerpt(body)


def summarize_records(text):
    """Reassemble a chat stream whether it is SSE (data: lines) or JSON lines, as the provider must."""
    sse = done = records = finished = 0
    content, reasoning, errors = [], 0, []
    for line in text.splitlines():
        line = line.strip()
        if not line or line.startswith((":", "event:", "id:", "retry:")):
            continue
        if line.startswith("data:"):
            sse += 1
            line = line[5:].strip()
        if line == "[DONE]":
            done += 1
            continue
        doc = _json(line)
        if not isinstance(doc, dict):
            continue
        records += 1
        if "error" in doc or doc.get("object") == "error":
            errors.append(_excerpt(line.encode(), 120))
        for choice in doc.get("choices") or []:
            finished += choice.get("finish_reason") is not None
            delta = choice.get("delta") or {}
            content.append(delta.get("content") or choice.get("text") or "")
            reasoning += len(delta.get("reasoning_content") or delta.get("reasoning") or "")
    fmt = "sse" if sse else ("jsonlines" if records else "unknown")
    # A stream must end: [DONE] or a finish_reason, so a truncated response does not pass.
    ok = records > 0 and not errors and (done or finished) and ("".join(content) or reasoning)
    note = "format=%s records=%d done=%s finished=%s reasoning=%d content=%r" % (fmt, records, bool(done), bool(finished), reasoning, "".join(content)[:60])
    if errors:
        note += " errors=%s" % errors[:2]
    return bool(ok), note


def _excerpt(body, n=200):
    return body[:n].decode("utf-8", "replace").replace("\n", " ")


# --- checks ------------------------------------------------------------------------------------------------------


class Prober:
    def __init__(self, args, creds):
        self.args = args
        self.creds = creds
        self.host = args.host or "runtime.sagemaker.%s.%s" % (args.region, "amazonaws.com.cn" if args.region.startswith("cn-") else "amazonaws.com")
        self.token = mint_token(creds, args.region, expires=900)
        self.results = []

    def base(self, endpoint=None):
        p = "/endpoints/%s" % (endpoint or self.args.endpoint)
        if self.args.ic and endpoint is None:
            p += "/inference-components/%s" % self.args.ic
        return p

    def openai(self, name, path, payload, expected, method="POST", stream=False, auth="bearer", token=None, endpoint=None, check=None):
        body = json.dumps(payload).encode() if payload is not None else None
        url = self.base(endpoint) + "/openai/v1/" + path
        headers = {"content-type": "application/json"} if body else {}
        if auth == "bearer":
            headers["authorization"] = "Bearer " + (token or self.token)
        else:
            headers = sigv4_headers(self.creds, self.args.region, method, self.host, url, body, headers)
        res = http_call(self.host, method, url, headers, body, self.args.timeout)
        if check is None:
            check = (lambda b: summarize_records(b.decode("utf-8", "replace"))) if stream else summarize_chat
        self.record(name, "openai " + auth, method + " " + url, res, expected, check)

    def invocations(self, name, payload, expected, stream=False, endpoint=None, extra=None, check=None):
        body = json.dumps(payload).encode()
        url = self.base(endpoint).split("/inference-components/")[0] + ("/invocations-response-stream" if stream else "/invocations")
        # No X-Amzn-SageMaker-Accept on streams: the container picks its default format, which is what the provider must parse.
        headers = {"content-type": "application/json", "accept": "application/vnd.amazon.eventstream" if stream else "application/json"}
        if self.args.ic and endpoint is None:
            headers["x-amzn-sagemaker-inference-component"] = self.args.ic
        headers.update(extra or {})
        res = http_call(self.host, "POST", url, sigv4_headers(self.creds, self.args.region, "POST", self.host, url, body, headers), body, self.args.timeout)
        if stream and res["status"] == 200:
            check = self._event_stream_check(res)
        self.record(name, "invocations sigv4", "POST " + url, res, expected, check or summarize_chat)

    def _event_stream_check(self, res):
        def check(body):
            messages, leftover = decode_event_stream(body)
            parts = [p for h, p in messages if h.get(":event-type") == "PayloadPart"]
            exceptions = ["%s: %s" % (h.get(":exception-type") or h.get(":error-code"), _excerpt(p, 120)) for h, p in messages if h.get(":message-type") != "event"]
            ok, note = summarize_records(b"".join(parts).decode("utf-8", "replace"))
            note = "parts=%d sm-content-type=%s %s" % (len(parts), res["headers"].get("x-amzn-sagemaker-content-type"), note)
            if exceptions:
                note += " exceptions=%s" % exceptions
            if leftover:
                note += " trailing_bytes=%d" % leftover
            return ok and not exceptions and not leftover, note

        return check

    def record(self, name, auth, target, res, expected, check):
        if 200 <= res["status"] < 300:
            ok, note = check(res["body"])
        else:
            ok, note = False, _excerpt(res["body"])
        error_type = res["headers"].get("x-amzn-errortype", "")
        self.results.append({
            "check": name, "auth": auth, "target": target, "expected": expected, "status": res["status"], "ok": ok,
            "error_type": error_type, "content_type": res["headers"].get("content-type", ""),
            "ttfb_ms": res["ttfb_ms"], "ms": res["ms"], "note": note,
        })
        if self.args.out:
            with open(os.path.join(self.args.out, name + ".txt"), "wb") as f:
                f.write(("%s\nHTTP %d\n" % (target, res["status"])).encode())
                f.write("".join("%s: %s\n" % kv for kv in sorted(res["headers"].items())).encode() + b"\n")
                f.write(res["body"][:MAX_SAVED_BODY])

    def negative(self, name, res_status_ok, target, res):
        self.results.append({
            "check": name, "auth": target.split(" ")[0], "target": target, "expected": "error", "status": res["status"],
            "ok": res_status_ok(res["status"]), "error_type": res["headers"].get("x-amzn-errortype", ""),
            "content_type": res["headers"].get("content-type", ""), "ttfb_ms": res["ttfb_ms"], "ms": res["ms"], "note": _excerpt(res["body"]),
        })

    def run(self):
        a = self.args
        chat = {"model": "", "messages": [{"role": "user", "content": PROMPT}], "max_tokens": 256}
        tei = a.dialect == "tei"
        embed_inv = {"inputs": "The quick brown fox."} if tei else {"model": "", "input": ["The quick brown fox."]}
        embed_openai = {"model": "", "input": ["The quick brown fox."]}
        exp_openai = "yes" if a.expect == "openai" else "info"
        exp_inv = "yes" if a.expect == "invocations" else "info"

        if a.kind == "embedding":
            self.invocations("invocations", embed_inv, exp_inv, check=summarize_embedding)
            self.openai("openai-embeddings", "embeddings", embed_openai, exp_openai, check=summarize_embedding)
            if a.quick:
                return
        else:
            self.invocations("invocations", chat, exp_inv)
            self.openai("openai-chat", "chat/completions", chat, exp_openai)
            if a.quick:
                return
            self.invocations("invocations-stream", dict(chat, stream=True), exp_inv, stream=True)
            self.openai("openai-chat-stream", "chat/completions", dict(chat, stream=True, stream_options={"include_usage": True}), exp_openai, stream=True)
            if a.model:
                self.openai("openai-chat-model-name", "chat/completions", dict(chat, model=a.model), "info")
            tools = dict(chat, messages=[{"role": "user", "content": "What is the weather in Paris? Use the tool."}], tools=[WEATHER_TOOL])
            self.openai("openai-tools", "chat/completions", tools, "info")
            self.invocations("invocations-tools", tools, "info")
            self.openai("openai-completions", "completions", {"model": a.model or "", "prompt": "2 + 2 =", "max_tokens": 16}, "info")
            self.openai("openai-responses", "responses", {"model": a.model or "", "input": PROMPT, "max_output_tokens": 256}, "info", check=lambda b: (bool((_json(b) or {}).get("output")), _excerpt(b, 160)))
            self.openai("openai-embeddings", "embeddings", embed_openai, "info", check=summarize_embedding)
            self.openai("openai-chat-sigv4", "chat/completions", chat, "info", auth="sigv4")

        self.openai("openai-models", "models", None, "info", method="GET", check=lambda b: (bool((_json(b) or {}).get("data")), _excerpt(b, 160)))
        if a.target_variant:
            payload = embed_inv if a.kind == "embedding" else chat
            check = summarize_embedding if a.kind == "embedding" else summarize_chat
            self.invocations("invocations-target-variant", payload, "info", extra={"x-amzn-sagemaker-target-variant": a.target_variant}, check=check)

        # Error shapes the provider's error converter has to map.
        missing = "bifrost-probe-missing-endpoint"
        url = "/endpoints/%s/openai/v1/chat/completions" % missing
        body = json.dumps(chat).encode()
        res = http_call(self.host, "POST", url, {"content-type": "application/json", "authorization": "Bearer " + self.token}, body, a.timeout)
        self.negative("error-unknown-endpoint-openai", lambda s: 400 <= s < 500, "bearer POST " + url, res)
        url = "/endpoints/%s/invocations" % missing
        res = http_call(self.host, "POST", url, sigv4_headers(self.creds, a.region, "POST", self.host, url, body, {"content-type": "application/json"}), body, a.timeout)
        self.negative("error-unknown-endpoint-invocations", lambda s: 400 <= s < 500, "sigv4 POST " + url, res)
        url = self.base() + "/openai/v1/chat/completions"
        res = http_call(self.host, "POST", url, {"content-type": "application/json", "authorization": "Bearer sagemaker-api-key-bm90LWEtdG9rZW4="}, body, a.timeout)
        self.negative("error-bad-bearer-token", lambda s: s in (401, 403), "bearer POST " + url, res)


def print_table(results):
    cols = ["check", "expected", "status", "ok", "error_type", "ms", "note"]
    rows = [[str(r[c]) if c != "ok" else ("PASS" if r["ok"] else "FAIL") for c in cols] for r in results]
    widths = [min(max(len(c), *(len(row[i]) for row in rows)), 48 if c != "note" else 200) for i, c in enumerate(cols)]
    print("  ".join(c.ljust(w) for c, w in zip(cols, widths)))
    for row in rows:
        print("  ".join(v[:w].ljust(w) for v, w in zip(row, widths)))


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    for name in ("run", "token"):
        s = sub.add_parser(name)
        s.add_argument("--region", required=True)
        s.add_argument("--role-arn", help="assume this role first (e.g. the module's invoke_role_arn)")
        if name == "token":
            s.add_argument("--expires", type=int, default=3600, help="seconds, 1-43200")
            continue
        s.add_argument("--endpoint", required=True)
        s.add_argument("--ic", help="inference component name")
        s.add_argument("--kind", choices=["chat", "embedding"], default="chat")
        s.add_argument("--dialect", choices=["openai", "tei"], default="openai", help="embedding body on /invocations")
        s.add_argument("--model", default="", help="served model name, for the named-model check")
        s.add_argument("--expect", choices=["openai", "invocations"], help="path that must pass (exit 1 otherwise)")
        s.add_argument("--target-variant")
        s.add_argument("--host", help="runtime host override (VPC endpoint, FIPS, dual-stack)")
        s.add_argument("--timeout", type=float, default=120)
        s.add_argument("--quick", action="store_true", help="one request per path only")
        s.add_argument("--out", help="directory for raw responses and results.json")
    args = p.parse_args()

    creds = load_credentials(args.role_arn, args.region)
    if args.cmd == "token":
        print(mint_token(creds, args.region, args.expires))
        return

    if args.out:
        os.makedirs(args.out, exist_ok=True)
    prober = Prober(args, creds)
    prober.run()
    print_table(prober.results)
    if args.out:
        with open(os.path.join(args.out, "results.json"), "w") as f:
            json.dump(prober.results, f, indent=2)
        print("\nraw responses: %s" % os.path.abspath(args.out))
    failed = [r["check"] for r in prober.results if r["expected"] == "yes" and not r["ok"]]
    if failed:
        sys.exit("\nexpected path failed: %s" % ", ".join(failed))


if __name__ == "__main__":
    main()
