import { describe, expect, it } from "vitest";
import { virtualKeyOwner, resolvePickedVirtualKey } from "./virtualMcpAccessTab.utils";

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

	it("names the user a key is assigned to", () => {
		expect(virtualKeyOwner({ assigned_user: { id: "u1", name: "Ada", email: "ada@example.com" } })).toEqual({ kind: "user", name: "Ada" });
		expect(virtualKeyOwner({ assigned_user: { id: "u1", name: "", email: "ada@example.com" } })).toEqual({
			kind: "user",
			name: "ada@example.com",
		});
	});

	it("falls back to the owner's id when its name was not loaded", () => {
		expect(virtualKeyOwner({ team_id: "t1" })).toEqual({ kind: "team", name: "t1" });
	});

	it("reports no owner for an unowned key or an unknown one", () => {
		expect(virtualKeyOwner({})).toBeUndefined();
		expect(virtualKeyOwner({ assigned_user: null })).toBeUndefined();
		expect(virtualKeyOwner(undefined)).toBeUndefined();
	});
});
describe("resolvePickedVirtualKey", () => {
	const owned = { team_id: "t1", team: { name: "Platform" } } as never;
	// A resolved, unassigned key: the server sends assigned_user as null once it has settled who holds it.
	const unowned = { assigned_user: null } as never;

	it("reads the picked key to find its owner", async () => {
		await expect(resolvePickedVirtualKey(async () => owned)).resolves.toEqual({
			outcome: "owned",
			owner: { kind: "team", name: "Platform" },
		});
		await expect(resolvePickedVirtualKey(async () => unowned)).resolves.toEqual({ outcome: "unowned" });
	});

	it("names the assignee the read returns", async () => {
		const fetchKey = async () => ({ assigned_user: { id: "u1", name: "Ada", email: "ada@example.com" } }) as never;
		await expect(resolvePickedVirtualKey(fetchKey)).resolves.toEqual({
			outcome: "owned",
			owner: { kind: "user", name: "Ada" },
		});
	});

	it("reports a key whose assignee is still unresolved after the read as a failed lookup", async () => {
		await expect(resolvePickedVirtualKey(async () => ({}) as never)).resolves.toEqual({ outcome: "lookupFailed" });
	});

	it("treats a key an entity owns as owned, whatever its assignee", async () => {
		await expect(resolvePickedVirtualKey(async () => ({ customer_id: "c1" }) as never)).resolves.toEqual({
			outcome: "owned",
			owner: { kind: "customer", name: "c1" },
		});
	});

	it("reports a failed read instead of treating the key as unowned", async () => {
		await expect(
			resolvePickedVirtualKey(async () => {
				throw new Error("network");
			}),
		).resolves.toEqual({ outcome: "lookupFailed" });
	});
});