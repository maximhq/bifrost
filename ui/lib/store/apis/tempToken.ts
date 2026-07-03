// Module-level state for the temp-token scope wrapper.
//
// The wrapper component (components/tempTokenScope.tsx) sets these on mount
// and clears them on unmount. baseApi reads them on every request:
//   - prepareHeaders attaches `X-Bifrost-Temp-Token: <token>` when a token
//     is active, so APIs called from inside the scope can authenticate via
//     temp token instead of the dashboard session cookie.
//   - baseQueryWithErrorHandling consults the suppression flag before
//     force-redirecting to /login on a 401, so a scoped page can render its
//     own "invalid/expired link" view instead of yanking the user away.
//
// A module-level singleton is fine because we never expect two TempTokenScope
// wrappers to be mounted concurrently in the same tab. The wrapper guards
// against nested mounts via the same-token check on set.

let activeTempToken: string | null = null;
let suppressGlobal401 = false;

export function setActiveTempToken(token: string | null): void {
	activeTempToken = token;
}

export function getActiveTempToken(): string | null {
	return activeTempToken;
}

export function setSuppressGlobal401(value: boolean): void {
	suppressGlobal401 = value;
}

export function getSuppressGlobal401(): boolean {
	return suppressGlobal401;
}