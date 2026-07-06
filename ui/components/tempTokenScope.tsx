// TempTokenScope wraps a page that authenticates via a short-lived temp token
// embedded in the URL fragment (`#t=<token>`). It does three things:
//
//   1. On mount, reads the token from `window.location.hash` and installs it
//      in the baseApi module state so all RTK Query calls attach a
//      `X-Bifrost-Temp-Token` header.
//   2. Strips the fragment from the URL via `history.replaceState` so the
//      token does not leak into Referer headers if the user later navigates
//      away.
//   3. Sets the suppression flag so a 401 from a wrapped API call does NOT
//      trigger the global redirect-to-/login in baseQueryWithErrorHandling.
//      The wrapped page renders its own invalid/expired-link UI.
//
// The wrapper is scope-agnostic — the `name` prop only identifies the scope in
// log lines (and is wired into future error UI). Routes that opt in still need
// to declare `staticData: { tempTokenScoped: true }` on their `createFileRoute`
// so ClientLayout skips the protected dashboard fetches; that piece is
// orthogonal to this wrapper.

import { setActiveTempToken, setSuppressGlobal401 } from "@/lib/store/apis/tempToken";
import { useEffect, useState } from "react";

interface TempTokenScopeProps {
	name: string;
	children: React.ReactNode;
}

export default function TempTokenScope({ name: _name, children }: TempTokenScopeProps) {
	// Install the module state synchronously during render — NOT in useEffect.
	// React fires child effects before parent effects, so a child API call
	// triggered from its own useEffect would race ahead of a parent useEffect
	// and go out without the X-Bifrost-Temp-Token header (and without the
	// global-401 suppression flag set, so the 401 would force a /login
	// redirect). useState's initializer runs once during the parent's render,
	// strictly before any descendant render or effect — so by the time the
	// child's query effect fires, the module state is already in place.
	//
	// Both setters are idempotent, which makes this safe under React strict
	// mode's double-invocation.
	useState(() => {
		if (typeof window === "undefined") {
			return null;
		}
		const token = parseTokenFromFragment(window.location.hash);
		if (token) {
			// Token present: install both. The page authenticates via temp
			// token and handles its own 401 display.
			setActiveTempToken(token);
			setSuppressGlobal401(true);
		}
		// No token: leave both unset so a 401 (e.g. a dashboard user whose
		// session expired mid-page) still triggers the normal /login redirect.
		// This preserves the existing reauth-from-sessions-tab flow.
		return token;
	});

	useEffect(() => {
		// Strip the fragment so the token doesn't end up in Referer headers on
		// outbound navigation (e.g. the redirect to the upstream OAuth provider
		// when the user clicks Authenticate). Pure URL cosmetics — safe to defer
		// to an effect, doesn't affect auth correctness.
		if (typeof window !== "undefined" && window.location.hash) {
			window.history.replaceState(null, "", window.location.pathname + window.location.search);
		}
		return () => {
			setActiveTempToken(null);
			setSuppressGlobal401(false);
		};
	}, []);

	return <>{children}</>;
}

// parseTokenFromFragment extracts the `t` parameter from a URL fragment like
// `#t=abc123` or `#foo=bar&t=abc123`. Returns null if absent.
function parseTokenFromFragment(fragment: string): string | null {
	if (!fragment || fragment.length < 2) {
		return null;
	}
	// URLSearchParams handles `?` and `&` separators; the fragment shape used
	// by the server (`#t=...`) parses cleanly after stripping the leading `#`.
	const params = new URLSearchParams(fragment.slice(1));
	const token = params.get("t");
	return token && token.length > 0 ? token : null;
}