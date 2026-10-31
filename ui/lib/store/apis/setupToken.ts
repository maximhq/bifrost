// OSS setup lock helpers.
//
// While dashboard auth is not active (no admin account, or auth disabled), the
// server locks every /api call behind the operator's setup token. The dashboard
// never keeps that token: the login setup view sends it once to
// POST /api/session/setup, which answers with an HttpOnly setup session cookie.
// The browser then sends that cookie on every call (reloads and WebSocket
// upgrades included) and no script can read it. API clients send the token in
// the X-Bifrost-Setup-Token header instead.

export const SETUP_TOKEN_HEADER = "X-Bifrost-Setup-Token";

// Earlier builds kept the token in sessionStorage. Remove any copy left behind.
if (typeof window !== "undefined") {
	try {
		window.sessionStorage.removeItem("bifrost-setup-token");
	} catch {
		// Storage unavailable: nothing to remove.
	}
}