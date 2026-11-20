// Helpers for the code a device sign-in (a client gateway's /login) shows in the terminal.
// Shared by the device page, which takes the code, and the consent page, which must send
// it back to approve the sign-in.

// Same charset and shape the gateways issue (RFC 8628 §6.1 base-20 consonants).
export const USER_CODE_LENGTH = 8;

export function normalizeUserCode(value: string): string {
	return value.toUpperCase().replace(/[^A-Z]/g, "");
}

export function formatUserCode(value: string): string {
	const code = normalizeUserCode(value).slice(0, USER_CODE_LENGTH);
	return code.length > 4 ? `${code.slice(0, 4)}-${code.slice(4)}` : code;
}

export function isCompleteUserCode(value: string): boolean {
	return normalizeUserCode(value).length === USER_CODE_LENGTH;
}

const storageKey = (flowId: string) => `bifrost.deviceUserCode.${flowId}`;

// rememberDeviceUserCode keeps the typed code for this tab only, so the consent page it opens
// can show and send it. A consent link opened anywhere else has no code and asks for it.
export function rememberDeviceUserCode(flowId: string, code: string): void {
	try {
		sessionStorage.setItem(storageKey(flowId), formatUserCode(code));
	} catch {
		// Storage blocked: the consent page asks for the code instead.
	}
}

export function recallDeviceUserCode(flowId: string): string {
	try {
		return formatUserCode(sessionStorage.getItem(storageKey(flowId)) ?? "");
	} catch {
		return "";
	}
}