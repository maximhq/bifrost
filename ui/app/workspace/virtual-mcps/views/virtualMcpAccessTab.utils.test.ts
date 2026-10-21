import { describe, expect, it } from "vitest";
import { virtualKeyOwner } from "./virtualMcpAccessTab.utils";

describe("virtualKeyOwner", () => {
	it("names the team, business unit or customer that owns the key", () => {
		expect(virtualKeyOwner({ team_id: "t1", team: { id: "t1", name: "Platform" } as never })).toEqual({ kind: "team", name: "Platform" });
		expect(virtualKeyOwner({ business_unit_id: "b1", business_unit: { id: "b1", name: "Finance" } })).toEqual({
			kind: "business unit",
			name: "Finance",
		});
		expect(virtualKeyOwner({ customer_id: "c1", customer: { id: "c1", name: "Acme" } as never })).toEqual({
			kind: "customer",
			name: "Acme",
		});
	});

	it("falls back to the owner's id when its name was not loaded", () => {
		expect(virtualKeyOwner({ team_id: "t1" })).toEqual({ kind: "team", name: "t1" });
	});

	it("reports no owner for an unowned key or an unknown one", () => {
		expect(virtualKeyOwner({})).toBeUndefined();
		expect(virtualKeyOwner(undefined)).toBeUndefined();
	});
});