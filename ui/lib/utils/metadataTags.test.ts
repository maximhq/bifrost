import { describe, expect, it } from "vitest";

import {
	collectTags,
	hasAllTags,
	MAX_METADATA_ENTRIES,
	MAX_METADATA_VALUE_LENGTH,
	MAX_TAG_LENGTH,
	MAX_TAGS,
	normalizeTags,
	tagsQueryParam,
	validateMetadata,
} from "./metadataTags";

describe("validateMetadata", () => {
	it("accepts valid metadata", () => {
		expect(validateMetadata(undefined)).toBeUndefined();
		expect(validateMetadata({ owner: "team-a", "region.primary": "eu-west-1", cost_center: "" })).toBeUndefined();
	});

	it("rejects bad keys, long values and too many entries", () => {
		expect(validateMetadata({ "cost center": "x" })).toMatch(/Invalid key/);
		expect(validateMetadata({ "": "x" })).toMatch(/Invalid key/);
		expect(validateMetadata({ k: "é".repeat(MAX_METADATA_VALUE_LENGTH) })).toBeUndefined();
		expect(validateMetadata({ k: "v".repeat(MAX_METADATA_VALUE_LENGTH + 1) })).toMatch(/longer than/);
		const tooMany = Object.fromEntries(Array.from({ length: MAX_METADATA_ENTRIES + 1 }, (_, i) => [`k${i}`, "v"]));
		expect(validateMetadata(tooMany)).toMatch(/At most/);
	});
});

describe("normalizeTags", () => {
	it("trims, de-duplicates and sorts like the server", () => {
		expect(normalizeTags([" prod ", "eu", "prod", "approved-for-pii"])).toEqual({ tags: ["approved-for-pii", "eu", "prod"] });
		expect(normalizeTags(["prod", "Prod"])).toEqual({ tags: ["Prod", "prod"] });
		expect(normalizeTags(undefined)).toEqual({ tags: [] });
	});

	it("rejects invalid tags and too many tags", () => {
		expect(normalizeTags(["a,b"]).error).toMatch(/Invalid tag/);
		expect(normalizeTags(["env:prod"]).error).toMatch(/Invalid tag/);
		expect(normalizeTags(["  "]).error).toMatch(/Invalid tag/);
		expect(normalizeTags(["t".repeat(MAX_TAG_LENGTH)]).error).toBeUndefined();
		expect(normalizeTags(["t".repeat(MAX_TAG_LENGTH + 1)]).error).toMatch(/Invalid tag/);
		const tooMany = Array.from({ length: MAX_TAGS + 1 }, (_, i) => `t${i}`);
		expect(normalizeTags(tooMany).error).toMatch(/At most/);
		expect(normalizeTags([...tooMany.slice(0, MAX_TAGS), "t0"]).error).toBeUndefined();
	});
});

describe("tag filters", () => {
	it("matches only items carrying every wanted tag", () => {
		expect(hasAllTags(["eu", "prod"], [])).toBe(true);
		expect(hasAllTags(["eu", "prod"], ["prod", "eu"])).toBe(true);
		expect(hasAllTags(["eu", "prod"], ["prod", "us"])).toBe(false);
		expect(hasAllTags(undefined, ["prod"])).toBe(false);
	});

	it("collects distinct tags and builds the query value", () => {
		expect(collectTags([{ tags: ["prod", "eu"] }, {}, { tags: ["prod", "approved-for-pii"] }])).toEqual(["approved-for-pii", "eu", "prod"]);
		expect(tagsQueryParam(["prod", "eu"])).toBe("prod,eu");
		expect(tagsQueryParam([])).toBeUndefined();
	});
});