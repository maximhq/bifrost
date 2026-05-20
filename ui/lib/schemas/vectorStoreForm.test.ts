import { describe, expect, test } from "vitest";
import { vectorStoreFormSchema } from "@/lib/types/schemas";

const env = (value: string) => ({ value, env_var: "", from_env: false });
const envRef = (name: string) => ({ value: "", env_var: name, from_env: true });
const empty = () => ({ value: "", env_var: "", from_env: false });

describe("vectorStoreFormSchema", () => {
	describe("redis", () => {
		test("passes with addr value set", () => {
			const result = vectorStoreFormSchema.safeParse({ provider: "redis", addr: env("localhost:6379") });
			expect(result.success).toBe(true);
		});

		test("passes with addr from env var", () => {
			const result = vectorStoreFormSchema.safeParse({ provider: "redis", addr: envRef("env.REDIS_ADDR") });
			expect(result.success).toBe(true);
		});

		test("fails when addr is empty", () => {
			const result = vectorStoreFormSchema.safeParse({ provider: "redis", addr: empty() });
			expect(result.success).toBe(false);
			expect(result.error?.issues[0].message).toBe("Redis address is required");
		});

		test("fails when addr is missing", () => {
			const result = vectorStoreFormSchema.safeParse({ provider: "redis" });
			expect(result.success).toBe(false);
		});

		test("ignores optional fields", () => {
			const result = vectorStoreFormSchema.safeParse({ provider: "redis", addr: env("localhost:6379"), password: empty() });
			expect(result.success).toBe(true);
		});
	});

	describe("weaviate", () => {
		test("passes with host value set", () => {
			const result = vectorStoreFormSchema.safeParse({ provider: "weaviate", host: env("localhost:8080") });
			expect(result.success).toBe(true);
		});

		test("passes with host from env var", () => {
			const result = vectorStoreFormSchema.safeParse({ provider: "weaviate", host: envRef("env.WEAVIATE_HOST") });
			expect(result.success).toBe(true);
		});

		test("fails when host is empty", () => {
			const result = vectorStoreFormSchema.safeParse({ provider: "weaviate", host: empty() });
			expect(result.success).toBe(false);
			expect(result.error?.issues[0].message).toBe("Weaviate host is required");
		});
	});

	describe("qdrant", () => {
		test("passes with host value set", () => {
			const result = vectorStoreFormSchema.safeParse({ provider: "qdrant", host: env("localhost") });
			expect(result.success).toBe(true);
		});

		test("passes with host from env var", () => {
			const result = vectorStoreFormSchema.safeParse({ provider: "qdrant", host: envRef("env.QDRANT_HOST") });
			expect(result.success).toBe(true);
		});

		test("fails when host is empty", () => {
			const result = vectorStoreFormSchema.safeParse({ provider: "qdrant", host: empty() });
			expect(result.success).toBe(false);
			expect(result.error?.issues[0].message).toBe("Qdrant host is required");
		});
	});

	describe("pinecone", () => {
		test("passes with both api_key and index_host set", () => {
			const result = vectorStoreFormSchema.safeParse({
				provider: "pinecone",
				api_key: env("pc-123"),
				index_host: env("my-index.svc.pinecone.io"),
			});
			expect(result.success).toBe(true);
		});

		test("passes with both fields from env vars", () => {
			const result = vectorStoreFormSchema.safeParse({
				provider: "pinecone",
				api_key: envRef("env.PINECONE_KEY"),
				index_host: envRef("env.PINECONE_HOST"),
			});
			expect(result.success).toBe(true);
		});

		test("fails when api_key is empty", () => {
			const result = vectorStoreFormSchema.safeParse({
				provider: "pinecone",
				api_key: empty(),
				index_host: env("my-index.svc.pinecone.io"),
			});
			expect(result.success).toBe(false);
			expect(result.error?.issues[0].message).toBe("Pinecone API key is required");
		});

		test("fails when index_host is empty", () => {
			const result = vectorStoreFormSchema.safeParse({
				provider: "pinecone",
				api_key: env("pc-123"),
				index_host: empty(),
			});
			expect(result.success).toBe(false);
			expect(result.error?.issues[0].message).toBe("Pinecone index host is required");
		});

		test("fails when both fields are empty", () => {
			const result = vectorStoreFormSchema.safeParse({
				provider: "pinecone",
				api_key: empty(),
				index_host: empty(),
			});
			expect(result.success).toBe(false);
			expect(result.error!.issues.length).toBeGreaterThanOrEqual(1);
		});
	});

	describe("edge cases", () => {
		test("fails with unknown provider", () => {
			const result = vectorStoreFormSchema.safeParse({ provider: "milvus", host: env("localhost") });
			expect(result.success).toBe(false);
		});

		test("whitespace-only value is treated as empty", () => {
			const result = vectorStoreFormSchema.safeParse({ provider: "redis", addr: env("   ") });
			expect(result.success).toBe(false);
			expect(result.error?.issues[0].message).toBe("Redis address is required");
		});

		test("env var with name but no value passes", () => {
			const result = vectorStoreFormSchema.safeParse({ provider: "redis", addr: { value: "", env_var: "REDIS_ADDR", from_env: true } });
			expect(result.success).toBe(true);
		});
	});
});