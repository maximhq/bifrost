// Virtual MCP assignments are staged locally in the sheet and reconciled against the key's
// persisted set on Save via attach/detach calls.

/**
 * Splits a staged Virtual MCP assignment set against the persisted one into the ids to
 * attach (staged but not original) and to detach (original but not staged). Order follows
 * the input arrays; duplicates within an input are collapsed.
 */
export function diffVmcpAssignments(original: number[], staged: number[]): { toAttach: number[]; toDetach: number[] } {
	const originalSet = new Set(original);
	const stagedSet = new Set(staged);
	const toAttach = [...new Set(staged)].filter((id) => !originalSet.has(id));
	const toDetach = [...new Set(original)].filter((id) => !stagedSet.has(id));
	return { toAttach, toDetach };
}

/** Reports whether a staged assignment set differs from the persisted one, ignoring order. */
export function vmcpAssignmentsDirty(original: number[], staged: number[]): boolean {
	const { toAttach, toDetach } = diffVmcpAssignments(original, staged);
	return toAttach.length > 0 || toDetach.length > 0;
}
// delete_after_expire is tri-state server-side: null inherits client.delete_expired_virtual_keys.
// clientDefault is undefined until the client config has loaded.

/**
 * The delete_after_expire value for a create request with an expiry; undefined omits it (inherit).
 * While the default is unknown the shown value is sent, so the key never inherits one the user did not see.
 */
export function createDeleteAfterExpire(value: boolean, clientDefault: boolean | undefined): boolean | undefined {
	return clientDefault === undefined || value !== clientDefault ? value : undefined;
}

export interface UpdateDeleteAfterExpireInput {
	switchTouched: boolean;
	expiryChanged: boolean;
	hasExpiry: boolean;
	value: boolean;
	clientDefault: boolean | undefined;
	storedOverride: boolean | null | undefined;
}

/**
 * The delete_after_expire value for an update; undefined omits it (the stored value stays), null resets
 * it to inherit. Only a touched switch changes the stored value, so editing just the expiry keeps an
 * explicit override; clearing the expiry needs nothing, since the server resets the flag then.
 */
export function updateDeleteAfterExpire({
	switchTouched,
	expiryChanged,
	hasExpiry,
	value,
	clientDefault,
	storedOverride,
}: UpdateDeleteAfterExpireInput): boolean | null | undefined {
	if (!hasExpiry) return undefined;
	if (switchTouched) return clientDefault !== undefined && value === clientDefault ? null : value;
	// A key that inherits would otherwise pick up a default the user never saw.
	if (expiryChanged && storedOverride == null && clientDefault === undefined) return value;
	return undefined;
}