import { describe, expect, it } from "vitest";

import { customProviderConfigSchema, formCustomProviderConfigSchema } from "./schemas";

for (const [name, schema] of Object.entries({ customProviderConfigSchema, formCustomProviderConfigSchema })) {
	describe(name, () => {
		it("accepts existing configurations without the boolean schema flag", () => {
			const config = { base_provider_type: "openai" };
			expect(schema.parse(config)).toEqual(config);
		});

		it("preserves enabled and disabled boolean schema flags when saving", () => {
			for (const boolean_schema_nodes_unsupported of [true, false]) {
				const config = { base_provider_type: "openai", boolean_schema_nodes_unsupported };
				expect(schema.parse(config)).toEqual(config);
			}
		});

		it("rejects a non-boolean compatibility flag", () => {
			expect(schema.safeParse({ base_provider_type: "openai", boolean_schema_nodes_unsupported: "true" }).success).toBe(false);
		});
	});
}
