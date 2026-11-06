import { describe, expect, it } from "vitest";
import { MESSAGE_ROLES, parseStoredMessageRoles, serializeMessageRoles } from "./useMessageRoleFilterPreference";

describe("parseStoredMessageRoles", () => {
	it("returns null when nothing is stored", () => {
		expect(parseStoredMessageRoles(null)).toBeNull();
		expect(parseStoredMessageRoles("")).toBeNull();
	});

	it("restores a reasoning-only filter", () => {
		expect(parseStoredMessageRoles('["reasoning"]')).toEqual(new Set(["reasoning"]));
	});

	it("drops unknown roles and keeps known ones", () => {
		expect(parseStoredMessageRoles('["reasoning","developer",42,null]')).toEqual(new Set(["reasoning"]));
	});

	it("falls back to every role for an empty or fully unknown filter", () => {
		expect(parseStoredMessageRoles("[]")).toBeNull();
		expect(parseStoredMessageRoles('["developer"]')).toBeNull();
	});

	it("falls back to every role for malformed JSON or a non-array value", () => {
		expect(parseStoredMessageRoles("{not json")).toBeNull();
		expect(parseStoredMessageRoles('{"reasoning":true}')).toBeNull();
		expect(parseStoredMessageRoles('"reasoning"')).toBeNull();
	});
});

describe("serializeMessageRoles", () => {
	it("writes roles in menu order regardless of insertion order", () => {
		expect(serializeMessageRoles(new Set(["reasoning", "user"]))).toBe('["user","reasoning"]');
	});

	it("round-trips through parse", () => {
		const all = new Set(MESSAGE_ROLES);
		expect(parseStoredMessageRoles(serializeMessageRoles(all))).toEqual(all);
		const some = new Set(["assistant", "tool"] as const);
		expect(parseStoredMessageRoles(serializeMessageRoles(some))).toEqual(new Set(some));
	});
});