import { describe, expect, test } from "vitest";
import { matchesRouterSearch } from "./routersTable.utils";

const router = { id: "support-desk", name: "Support", description: "Customer tickets" };

describe("matchesRouterSearch", () => {
	test("an empty or blank search matches every router", () => {
		expect(matchesRouterSearch(router, "")).toBe(true);
		expect(matchesRouterSearch(router, "   ")).toBe(true);
	});

	test("matches the name, the description or the id, ignoring case", () => {
		expect(matchesRouterSearch(router, "SUPP")).toBe(true);
		expect(matchesRouterSearch(router, "tickets")).toBe(true);
		expect(matchesRouterSearch(router, "desk")).toBe(true);
	});

	test("ignores the spaces around the search", () => {
		expect(matchesRouterSearch(router, "  support  ")).toBe(true);
	});

	test("does not match text none of them contains", () => {
		expect(matchesRouterSearch(router, "billing")).toBe(false);
		expect(matchesRouterSearch({ id: "x", name: "Billing" }, "tickets")).toBe(false);
	});
});