import { describe, expect, it } from "vitest";
import { describeRelayRoute, isAllRoutes, nextRouteSelection, relayGrantsPayload, relayGrantWithoutRoutes } from "./relayGrants";

describe("relayGrants", () => {
	it("keeps All routes exclusive in the picker", () => {
		expect(nextRouteSelection(["a"], ["a", "*"])).toEqual(["*"]);
		expect(nextRouteSelection(["*"], ["*", "a"])).toEqual(["a"]);
		expect(nextRouteSelection(["a", "b"], ["b"])).toEqual(["b"]);
		expect(nextRouteSelection(["a"], [])).toEqual([]);
	});

	it("never widens an empty selection to every route", () => {
		expect(relayGrantsPayload([{ relay_name: "r" }])).toEqual([{ relay_name: "r", routes: ["*"] }]);
		expect(relayGrantsPayload([{ relay_name: "r", routes: [] }])).toEqual([{ relay_name: "r", routes: [] }]);
		expect(relayGrantWithoutRoutes([{ relay_name: "a", routes: ["x"] }, { relay_name: "b", routes: [] }])).toBe("b");
		expect(relayGrantWithoutRoutes([{ relay_name: "a" }])).toBeUndefined();
	});

	it("reads unset or star routes as every route, and describes a route", () => {
		expect(isAllRoutes(undefined)).toBe(true);
		expect(isAllRoutes(["*"])).toBe(true);
		expect(isAllRoutes(["x"])).toBe(false);
		expect(describeRelayRoute({ method: "post", path: "/v1/search", headers: { "X-Op": "List*" } })).toBe("POST /v1/search · X-Op: List*");
	});
});