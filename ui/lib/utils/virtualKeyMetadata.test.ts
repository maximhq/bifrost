import { describe, expect, test } from "vitest";
import {
	MAX_VIRTUAL_KEY_METADATA_ENTRIES,
	validateVirtualKeyMetadata,
	virtualKeyMetadataKeyError,
	virtualKeyMetadataLogsFilter,
	virtualKeyMetadataQueryParams,
	virtualKeyMetadataUpdate,
} from "./virtualKeyMetadata";

describe("validateVirtualKeyMetadata", () => {
	const many = (n: number) => Object.fromEntries(Array.from({ length: n }, (_, i) => [`k${i}`, "v"]));

	test.each([
		["undefined", undefined],
		["empty", {}],
		["valid keys", { cost_center: "cc-42", "owner.email": "a@example.com", "env-1": "" }],
		["entries at limit", many(MAX_VIRTUAL_KEY_METADATA_ENTRIES)],
		["key at max length", { ["k".repeat(256)]: "v" }],
		["value at max length in characters", { k: "é".repeat(512) }],
	])("accepts %s", (_, metadata) => {
		expect(validateVirtualKeyMetadata(metadata)).toBeUndefined();
	});

	test.each([
		["too many entries", many(MAX_VIRTUAL_KEY_METADATA_ENTRIES + 1), "At most 50"],
		["key with space", { "cost center": "v" }, 'Invalid key "cost center"'],
		["key with slash", { "a/b": "v" }, "Invalid key"],
		["key too long", { ["k".repeat(257)]: "v" }, "Invalid key"],
		["reserved key", { isAsyncRequest: "true" }, "reserved"],
		["load balancer prefix", { bifrost_alb_provider: "x" }, "reserved"],
		["value too long", { k: "v".repeat(513) }, "longer than 512"],
	])("rejects %s", (_, metadata, message) => {
		expect(validateVirtualKeyMetadata(metadata)).toContain(message);
	});
});

describe("virtualKeyMetadataKeyError", () => {
	test("rejects an empty key", () => {
		expect(virtualKeyMetadataKeyError("")).toContain("Invalid key");
	});
});

describe("virtualKeyMetadataQueryParams", () => {
	test("prefixes each key and skips empty keys", () => {
		expect(virtualKeyMetadataQueryParams({ cost_center: "cc-42", env: "prod", "": "x" })).toEqual({
			metadata_cost_center: "cc-42",
			metadata_env: "prod",
		});
	});

	test("returns no params without metadata", () => {
		expect(virtualKeyMetadataQueryParams(undefined)).toEqual({});
	});
});

describe("virtualKeyMetadataLogsFilter", () => {
	test("encodes one entry the way the logs page parses metadata_filters", () => {
		expect(JSON.parse(virtualKeyMetadataLogsFilter("cost_center", 'cc "42"'))).toEqual({ cost_center: 'cc "42"' });
	});
});
describe("virtualKeyMetadataUpdate", () => {
	test.each([
		["both empty", {}, {}],
		["both undefined", undefined, undefined],
		["same entries", { cost_center: "cc-42", env: "prod" }, { cost_center: "cc-42", env: "prod" }],
		["same entries in another order", { env: "prod", cost_center: "cc-42" }, { cost_center: "cc-42", env: "prod" }],
	])("omits metadata when unchanged: %s", (_, initial, current) => {
		expect(virtualKeyMetadataUpdate(initial, current)).toEqual({});
	});

	test.each([
		["value changed", { cost_center: "cc-42" }, { cost_center: "cc-43" }],
		["entry added", { cost_center: "cc-42" }, { cost_center: "cc-42", env: "prod" }],
		["entry removed", { cost_center: "cc-42", env: "prod" }, { cost_center: "cc-42" }],
		["key renamed", { cost_center: "cc-42" }, { costcenter: "cc-42" }],
		["first metadata", undefined, { cost_center: "cc-42" }],
	])("sends the edited map when changed: %s", (_, initial, current) => {
		expect(virtualKeyMetadataUpdate(initial, current)).toEqual({ metadata: current });
	});

	test("sends {} to clear metadata the key had", () => {
		expect(virtualKeyMetadataUpdate({ cost_center: "cc-42" }, {})).toEqual({ metadata: {} });
	});
});
