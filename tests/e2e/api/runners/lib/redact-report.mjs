// Redaction for the PUBLISHED provider-harness report.
//
// The live viewer serves this data to whoever ran the harness, on their own
// machine, from their own keys - full fidelity there is the point. The --static
// page is a different audience entirely: it is uploaded to R2 and linked from
// the public changelog, so anything inlined into it is world-readable and stays
// that way in caches and mirrors long after any key is rotated.
//
// So this is applied on the static path only. The shape is preserved - the
// report still shows WHICH headers were sent and how big a body was - because a
// report that hides what was exercised is not worth publishing either.

// Header names whose value is a credential. Matched case-insensitively against
// the whole name, since these are exact header names rather than prefixes.
const SECRET_HEADERS = new Set([
  "authorization",
  "proxy-authorization",
  "cookie",
  "set-cookie",
  "x-api-key",
  "api-key",
  "apikey",
  "x-goog-api-key",
  "x-goog-iam-authorization-token",
  "x-amz-security-token",
  "x-amz-content-sha256",
  "x-auth-token",
  "x-access-token",
  "openai-api-key",
  "anthropic-api-key",
  "x-bifrost-api-key",
]);

// Query parameters that carry credentials in the URL itself. Matched
// case-insensitively.
const SECRET_PARAMS = new Set([
  "key",
  "api_key",
  "apikey",
  "access_token",
  "token",
  "signature",
  "sig",
  "awsaccesskeyid",
  "x-amz-signature",
  "x-amz-credential",
  "x-amz-security-token",
  "x-goog-api-key",
]);

export const REDACTED = "[REDACTED]";

// Credential-shaped runs that appear INSIDE bodies. Providers echo keys back in
// error messages ("Incorrect API key provided: sk-..."), and a signed URL in a
// response body is just as live as one in a request.
//
// Ordered most specific first; each is applied globally.
const BODY_SECRET_PATTERNS = [
  /\bBearer\s+[A-Za-z0-9._~+/=-]{8,}/gi, // Authorization values quoted into errors
  /\b(?:sk|rk|pk)-[A-Za-z0-9._-]{12,}/g, // OpenAI/Anthropic-style keys
  /\bAKIA[0-9A-Z]{16}\b/g, // AWS access key id
  /\bASIA[0-9A-Z]{16}\b/g, // AWS temporary access key id
  /\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}/g, // JWT
  /\bAIza[0-9A-Za-z._-]{20,}/g, // Google API key
  /\bxox[abposr]-[A-Za-z0-9-]{8,}/g, // Slack-style token
];

const redactHeaders = (headers) =>
  (headers || []).map((h) =>
    SECRET_HEADERS.has(String(h.key || "").toLowerCase()) ? { ...h, value: REDACTED } : h
  );

// redactUrl blanks credential-bearing query parameters while leaving the path
// and every other parameter legible, so the report still says which endpoint
// was called.
// decodeParamName tolerates a malformed escape rather than throwing: a name that
// cannot be decoded is compared as-is, which is the pre-existing behaviour.
const decodeParamName = (name) => {
  try {
    return decodeURIComponent(name.replace(/\+/g, " "));
  } catch {
    return name;
  }
};

// redactUserinfo blanks a `user:password@` credential embedded in the authority.
//
// This is not reachable from the query-parameter path at all - such a URL often
// has no query string - and it is a live credential just the same.
const redactUserinfo = (raw) =>
  // The WHOLE userinfo goes, username included. Keeping the username "because it is only a name"
  // was the bug: token-in-URL auth is normally written with the credential in the username
  // position and no password at all (https://sk-proj-...@host/), so preserving it published the
  // key while redacting an empty password beside it.
  raw.replace(/^([a-zA-Z][a-zA-Z0-9+.-]*:\/\/)[^/?#@]*@/, `$1${REDACTED}@`);

// redactParamList blanks credential-bearing entries in an `a=1&b=2` list,
// leaving every other entry legible. Shared by the query string and the
// fragment, which use the same encoding.
const redactParamList = (list) =>
  list
    .split("&")
    .map((pair) => {
      const eq = pair.indexOf("=");
      if (eq === -1) return pair;
      const name = pair.slice(0, eq);
      // Decode before matching: `api%5Fkey` is the same parameter as `api_key`,
      // and comparing the raw name published its value.
      return SECRET_PARAMS.has(decodeParamName(name).toLowerCase()) ? `${name}=${REDACTED}` : pair;
    })
    .join("&");

export function redactUrl(url) {
  const raw = redactUserinfo(String(url || ""));

  // The fragment comes off FIRST, before the query is located. Splitting on "?"
  // alone leaves `a=1#access_token=...` as a single pair named "a", which
  // matches nothing in SECRET_PARAMS and publishes the token - and a URL whose
  // credential lives in the fragment (the implicit-flow OAuth response puts it
  // there by design) often has no query string to split on at all.
  const h = raw.indexOf("#");
  const beforeFragment = h === -1 ? raw : raw.slice(0, h);
  const fragment = h === -1 ? "" : raw.slice(h + 1);

  const q = beforeFragment.indexOf("?");
  const base =
    q === -1
      ? beforeFragment
      : `${beforeFragment.slice(0, q)}?${redactParamList(beforeFragment.slice(q + 1))}`;

  return fragment ? `${base}#${redactParamList(fragment)}` : base;
}

// URLs embedded in a body. Bounded by whitespace and the delimiters a URL cannot
// contain unescaped, so a link inside JSON ("...") or HTML (<a href=...>) ends where
// the payload says it ends rather than swallowing the rest of the document.
//
// Both spellings of the separators are accepted: `\/` is a legal JSON escape for `/`
// and many serializers emit it, so the same signed URL reaches us as
// https:\/\/host\/path just as often as the literal form. Matching only the literal
// one left the escaped variant unredacted, which is the harder case to notice.
const URL_IN_BODY = /https?:(?:\\?\/){2}(?:\\\/|[^\s"'<>\\)\]}])+/g;

export function redactBody(body) {
  let out = String(body || "");
  // A signed URL is a live credential wherever it appears, and providers hand them
  // back inside response payloads - a presigned S3 link in a body is exactly as usable
  // as one in the request line. BODY_SECRET_PATTERNS cannot cover this: it matches
  // credential SHAPES, and an HMAC signature is just hex. So run the same
  // SECRET_PARAMS logic the request URL gets, rather than enumerating more shapes.
  // redactUrl parses a real URL, so an escaped one is unescaped before redaction and
  // re-escaped afterwards - stripping the escaping here would leave the body invalid
  // JSON, and a report that will not parse is no more publishable than one that leaks.
  out = out.replace(URL_IN_BODY, (url) => {
    if (!url.includes("\\/")) return redactUrl(url);
    return redactUrl(url.split("\\/").join("/")).split("/").join("\\/");
  });
  for (const re of BODY_SECRET_PATTERNS) out = out.replace(re, REDACTED);
  return out;
}

// redactItemsForPublic returns a copy safe to inline into a world-readable page.
export function redactItemsForPublic(items) {
  return (items || []).map((item) => ({
    ...item,
    url: redactUrl(item.url),
    reqHeaders: redactHeaders(item.reqHeaders),
    respHeaders: redactHeaders(item.respHeaders),
    reqBody: redactBody(item.reqBody),
    respBody: redactBody(item.respBody),
  }));
}
