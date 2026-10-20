import { describe, expect, it } from "vitest";

import { authProofOfControlSchema } from "./schemas";

describe("authProofOfControlSchema", () => {
	// The server verifies current_password against the stored hash byte for byte (and the
	// password policy allows spaces), so the value must reach the request exactly as typed.
	it("keeps current_password byte-exact, including surrounding whitespace", () => {
		const result = authProofOfControlSchema.safeParse({ current_password: " pass word ", setup_token: "" });
		expect(result.success).toBe(true);
		expect(result.data?.current_password).toBe(" pass word ");
	});

	it("still treats a whitespace-only current_password as missing", () => {
		const result = authProofOfControlSchema.safeParse({ current_password: "   ", setup_token: "" });
		expect(result.success).toBe(false);
		expect(result.error?.issues.map((issue) => issue.path.join("."))).toContain("current_password");
	});

	it("accepts a setup token on its own and trims it, as the server does", () => {
		const result = authProofOfControlSchema.safeParse({ current_password: "", setup_token: "  tok  " });
		expect(result.success).toBe(true);
		expect(result.data?.setup_token).toBe("tok");
	});

	it("requires one of the two", () => {
		const result = authProofOfControlSchema.safeParse({ current_password: "", setup_token: "" });
		expect(result.success).toBe(false);
	});
});