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

export interface OwnerProfileStateInput {
	/** A team, customer or business unit is selected as the key's owner. */
	hasOwner: boolean;
	/** The caller holds View on the owner's kind; without it the profile lookup is skipped. */
	canReadOwnerProfile: boolean;
	isFetching: boolean;
	isError: boolean;
	/** HTTP status of the failed lookup, when there is one. */
	errorStatus: number | undefined;
	/** The lookup returned a profile for the selected owner. */
	hasOwnerProfile: boolean;
	/** The selected owner is the key's current owner and the server flags the key as governed by it. */
	isServerFlaggedCurrentOwner: boolean;
}

/**
 * What the sheet knows about whether the selected owner's access profile governs the key.
 *
 * Only a 404 means "no profile"; any other failure, or a lookup still in flight, leaves it unknown
 * and holds Save back. A lookup the caller is not allowed to make is different: it would fail the
 * same way every time, so it must not block saving - the server's guard enforces governance either
 * way. Without the lookup the sheet still trusts the server's `is_access_profile_managed` for the
 * key's current owner, so a governed key keeps its governance fields out of the save instead of
 * having the guard refuse it.
 */
export function resolveOwnerProfileState({
	hasOwner,
	canReadOwnerProfile,
	isFetching,
	isError,
	errorStatus,
	hasOwnerProfile,
	isServerFlaggedCurrentOwner,
}: OwnerProfileStateInput): { isGoverned: boolean; failed: boolean; unknown: boolean } {
	if (!hasOwner) return { isGoverned: false, failed: false, unknown: false };
	if (!canReadOwnerProfile) return { isGoverned: isServerFlaggedCurrentOwner, failed: false, unknown: false };
	const failed = isError && errorStatus !== 404;
	return { isGoverned: hasOwnerProfile, failed, unknown: isFetching || failed };
}