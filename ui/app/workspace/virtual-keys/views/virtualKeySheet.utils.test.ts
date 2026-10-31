import { describe, expect, it } from "vitest";
import {
	createDeleteAfterExpire,
	diffVmcpAssignments,
	resolveOwnerProfileState,
	updateDeleteAfterExpire,
	vmcpAssignmentsDirty,
} from "./virtualKeySheet.utils";

describe("diffVmcpAssignments", () => {
	it("returns empty diffs when nothing changed (order-insensitive)", () => {
		expect(diffVmcpAssignments([1, 2, 3], [3, 2, 1])).toEqual({ toAttach: [], toDetach: [] });
	});

	it("attaches ids added and detaches ids removed", () => {
		expect(diffVmcpAssignments([1, 2], [2, 3])).toEqual({ toAttach: [3], toDetach: [1] });
	});

	it("attaches all when starting from empty", () => {
		expect(diffVmcpAssignments([], [5, 6])).toEqual({ toAttach: [5, 6], toDetach: [] });
	});

	it("detaches all when clearing", () => {
		expect(diffVmcpAssignments([5, 6], [])).toEqual({ toAttach: [], toDetach: [5, 6] });
	});

	it("collapses duplicates within an input", () => {
		expect(diffVmcpAssignments([1, 1], [1, 2, 2])).toEqual({ toAttach: [2], toDetach: [] });
	});
});

describe("vmcpAssignmentsDirty", () => {
	it("is false for equal sets regardless of order", () => {
		expect(vmcpAssignmentsDirty([1, 2, 3], [3, 1, 2])).toBe(false);
	});

	it("is true when an id is added or removed", () => {
		expect(vmcpAssignmentsDirty([1, 2], [1, 2, 3])).toBe(true);
		expect(vmcpAssignmentsDirty([1, 2], [1])).toBe(true);
	});
});
describe("createDeleteAfterExpire", () => {
	it("omits a value matching the loaded default so the key inherits", () => {
		expect(createDeleteAfterExpire(true, true)).toBeUndefined();
		expect(createDeleteAfterExpire(false, false)).toBeUndefined();
	});

	it("sends a value that differs from the default", () => {
		expect(createDeleteAfterExpire(true, false)).toBe(true);
		expect(createDeleteAfterExpire(false, true)).toBe(false);
	});

	it("sends the shown value explicitly while the default is unknown", () => {
		// Omitting it would inherit a default the user never saw, possibly true.
		expect(createDeleteAfterExpire(false, undefined)).toBe(false);
	});
});

describe("updateDeleteAfterExpire", () => {
	const base = { switchTouched: false, expiryChanged: false, hasExpiry: true, value: true, clientDefault: true, storedOverride: null };

	it("omits the flag when nothing relevant changed", () => {
		expect(updateDeleteAfterExpire(base)).toBeUndefined();
	});

	it("keeps an explicit override matching the default when only the expiry changed", () => {
		expect(updateDeleteAfterExpire({ ...base, expiryChanged: true, storedOverride: true })).toBeUndefined();
	});

	it("omits the flag when the expiry is cleared, since the server resets it", () => {
		expect(updateDeleteAfterExpire({ ...base, expiryChanged: true, hasExpiry: false, storedOverride: true })).toBeUndefined();
	});

	it("clears the override when the user sets the switch back to the default", () => {
		expect(updateDeleteAfterExpire({ ...base, switchTouched: true, storedOverride: false })).toBeNull();
	});

	it("stores an override when the user sets the switch against the default", () => {
		expect(updateDeleteAfterExpire({ ...base, switchTouched: true, value: false })).toBe(false);
	});

	it("sends the shown value explicitly when the default is unknown", () => {
		expect(updateDeleteAfterExpire({ ...base, switchTouched: true, clientDefault: undefined, value: false })).toBe(false);
		// A new expiry on a key without an override would otherwise inherit an unseen default.
		expect(updateDeleteAfterExpire({ ...base, expiryChanged: true, clientDefault: undefined, value: false })).toBe(false);
	});
});
describe("resolveOwnerProfileState", () => {
	const base = {
		hasOwner: true,
		canReadOwnerProfile: true,
		isFetching: false,
		isError: false,
		errorStatus: undefined,
		hasOwnerProfile: false,
		isServerFlaggedCurrentOwner: false,
	};

	it("is ungoverned and settled without an owner", () => {
		expect(resolveOwnerProfileState({ ...base, hasOwner: false, hasOwnerProfile: true })).toEqual({
			isGoverned: false,
			failed: false,
			unknown: false,
		});
	});

	it("is governed when the lookup returns the owner's profile", () => {
		expect(resolveOwnerProfileState({ ...base, hasOwnerProfile: true })).toEqual({ isGoverned: true, failed: false, unknown: false });
	});

	it("reads a 404 as no profile", () => {
		expect(resolveOwnerProfileState({ ...base, isError: true, errorStatus: 404 })).toEqual({
			isGoverned: false,
			failed: false,
			unknown: false,
		});
	});

	it("holds Save back while the lookup is in flight or has failed", () => {
		expect(resolveOwnerProfileState({ ...base, isFetching: true }).unknown).toBe(true);
		expect(resolveOwnerProfileState({ ...base, isError: true, errorStatus: 500 })).toEqual({
			isGoverned: false,
			failed: true,
			unknown: true,
		});
	});

	it("never blocks saving when the caller cannot read the owner's profile", () => {
		// A skipped lookup reports no error, but a stale one must not leak through either.
		expect(resolveOwnerProfileState({ ...base, canReadOwnerProfile: false, isError: true, errorStatus: 403 })).toEqual({
			isGoverned: false,
			failed: false,
			unknown: false,
		});
	});

	it("trusts the server flag for the current owner when the profile cannot be read", () => {
		expect(resolveOwnerProfileState({ ...base, canReadOwnerProfile: false, isServerFlaggedCurrentOwner: true })).toEqual({
			isGoverned: true,
			failed: false,
			unknown: false,
		});
	});
});