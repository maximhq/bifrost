import { describe, expect, it } from "vitest";
import { modelRouterIdentitySchema, modelRouterSheetSchema } from "./schemas";

// The router sheet refuses what the routing endpoint would, so the error shows
// under its field instead of after a failed save.
describe("model router sheet validation", () => {
	const valid = { name: "Support", description: "", enabled: true, scope: "global", scope_id: "", priority: "3" };
	const issuePaths = (values: typeof valid) => {
		const result = modelRouterSheetSchema.safeParse(values);
		return result.success ? [] : result.error.issues.map((issue) => issue.path.join("."));
	};

	it("accepts a global router with a whole-number priority", () => {
		expect(issuePaths(valid)).toEqual([]);
	});

	it("needs a name that is not only spaces", () => {
		expect(issuePaths({ ...valid, name: "   " })).toEqual(["name"]);
	});

	it("needs the entity of a non-global scope", () => {
		expect(issuePaths({ ...valid, scope: "team", scope_id: " " })).toEqual(["scope_id"]);
		expect(issuePaths({ ...valid, scope: "team", scope_id: "team-1" })).toEqual([]);
	});

	it("takes priorities from 0 to 1000 and refuses anything else", () => {
		expect(issuePaths({ ...valid, priority: "0" })).toEqual([]);
		expect(issuePaths({ ...valid, priority: "1000" })).toEqual([]);
		for (const priority of ["", "-1", "1001", "2.5", "abc"]) {
			expect(issuePaths({ ...valid, priority })).toEqual(["priority"]);
		}
	});

	it("trims the name and description it hands back", () => {
		const parsed = modelRouterSheetSchema.parse({ ...valid, name: " Support ", description: " Tickets " });
		expect(parsed.name).toBe("Support");
		expect(parsed.description).toBe("Tickets");
	});
});

describe("model router identity validation", () => {
	it("is the name and description the creation wizard asks for", () => {
		expect(modelRouterIdentitySchema.safeParse({ name: "Support", description: "" }).success).toBe(true);
		expect(modelRouterIdentitySchema.safeParse({ name: "", description: "" }).success).toBe(false);
	});
});