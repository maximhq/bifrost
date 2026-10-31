// TempTokenScope wraps a page that authenticates via a short-lived temp token
// embedded in the URL fragment (`#t=<token>`). It does four things:
//
//   1. Installs the token — from the fragment, or from sessionStorage once the
//      fragment is gone — so RTK Query attaches `X-Bifrost-Temp-Token`.
//   2. Mirrors it into sessionStorage, since /login is a full navigation that
//      drops both the fragment and the module state.
//   3. Strips the fragment via `history.replaceState` so the token stays out of
//      Referer headers.
//   4. Suppresses the global 401 redirect, even with no token, so the wrapped
//      page renders its own sign-in / invalid-link UI instead of bouncing.
//
// The `name` prop identifies the scope in log lines and namespaces the storage
// key. Routes must also declare `staticData: { tempTokenScoped: true }` so
// ClientLayout skips the protected dashboard fetches.

import { setActiveTempToken, setSuppressGlobal401 } from "@/lib/store/apis/tempToken";
import { useEffect, useState } from "react";

interface TempTokenScopeProps {
	name: string;
	children: React.ReactNode;
}

export default function TempTokenScope({ name, children }: TempTokenScopeProps) {
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
	const [token] = useState(() => {
		if (typeof window === "undefined") {
			return null;
		}
		const found = parseTokenFromFragment(window.location.hash) ?? readStoredToken(name);
		if (found) {
			setActiveTempToken(found);
		}
		setSuppressGlobal401(true);
		return found;
	});

	useEffect(() => {
		if (typeof window === "undefined") {
			return;
		}
		if (token) {
			// Re-install: strict mode's remount clears it before the second pass.
			setActiveTempToken(token);
			storeToken(name, token);
		}
		setSuppressGlobal401(true);
		// Strip the fragment so the token doesn't end up in Referer headers on
		// outbound navigation (e.g. the redirect to the upstream OAuth provider
		// when the user clicks Authenticate). Pure URL cosmetics — safe to defer
		// to an effect, doesn't affect auth correctness.
		if (window.location.hash) {
			window.history.replaceState(null, "", window.location.pathname + window.location.search);
		}
		return () => {
			setActiveTempToken(null);
			setSuppressGlobal401(false);
		};
	}, [name, token]);

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

// Namespaced by scope and flow so two flows in one tab never cross tokens.
function storageKey(name: string): string {
	const flowId = new URLSearchParams(window.location.search).get("flow");
	return `${name}_token_${flowId ?? window.location.pathname}`;
}

function readStoredToken(name: string): string | null {
	try {
		return sessionStorage.getItem(storageKey(name));
	} catch {
		return null;
	}
}

function storeToken(name: string, token: string): void {
	try {
		sessionStorage.setItem(storageKey(name), token);
	} catch {
		// Quota / private-browsing: only costs the round-trip rescue.
	}
}