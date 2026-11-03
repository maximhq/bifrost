import { describe, expect, it } from "vitest";

import { withActionParam, withActionParamOnPage } from "./useActionParam";

describe("withActionParam", () => {
	it("adds the action as the first query param", () => {
		expect(withActionParam("/workspace/governance/virtual-keys", "create")).toBe("/workspace/governance/virtual-keys?action=create");
	});

	it("appends to an href that already has a query", () => {
		expect(withActionParam("/workspace/config/caching?tab=semantic", "create")).toBe(
			"/workspace/config/caching?tab=semantic&action=create",
		);
	});

	it("encodes the action", () => {
		expect(withActionParam("/x", "a b&c")).toBe("/x?action=a%20b%26c");
	});
});
describe("withActionParamOnPage", () => {
	it("keeps the open view's search and paging", () => {
		expect(withActionParamOnPage("/workspace/governance/teams", "?search=ops&offset=20", "create")).toBe(
			"/workspace/governance/teams?search=ops&offset=20&action=create",
		);
	});

	it("replaces an action already in the URL instead of repeating it", () => {
		expect(withActionParamOnPage("/workspace/governance/teams", "?action=create&offset=20", "create")).toBe(
			"/workspace/governance/teams?offset=20&action=create",
		);
	});

	it("matches withActionParam when the page has no params", () => {
		expect(withActionParamOnPage("/workspace/config/caching?tab=semantic", "", "create")).toBe(
			"/workspace/config/caching?tab=semantic&action=create",
		);
	});
});