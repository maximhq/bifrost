// Before the first admin account exists, Bifrost's management API requires the operator's
// setup token (config.json setup_token or BIFROST_SETUP_TOKEN) in X-Bifrost-Setup-Token.
// Runners read the same BIFROST_SETUP_TOKEN the gateway was started with. Once an admin
// exists the gateway ignores the header, so sending it is always safe.

export const SETUP_TOKEN_HEADER = "X-Bifrost-Setup-Token";

// withSetupToken returns headers plus X-Bifrost-Setup-Token for a management API path
// (/api/...) when BIFROST_SETUP_TOKEN is set. Other paths, an unset token, or headers that
// already carry the header (in any case) come back unchanged. headers is never modified.
export function withSetupToken(headers = {}, path = "", env = process.env) {
  const token = env.BIFROST_SETUP_TOKEN;
  if (!token || !String(path).startsWith("/api/")) return headers;
  if (Object.keys(headers).some((k) => k.toLowerCase() === SETUP_TOKEN_HEADER.toLowerCase())) return headers;
  return { ...headers, [SETUP_TOKEN_HEADER]: token };
}
