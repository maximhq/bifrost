import { describe, expect, it } from "vitest";

import { vertexKeyConfigSchema } from "./schemas";

const secret = (value: string) => ({ value, ref: "" });
const base = { project_id: secret("my-project"), region: secret("us-central1") };
const audience = secret("//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/eks/providers/aws");

describe("vertexKeyConfigSchema, aws workload identity", () => {
	// The federation tab hides the JSON and API-key inputs, so a key saved from it with no
	// audience has no credentials at all and would silently fall back to ADC on the server.
	it("requires the audience when the AWS workload identity method is selected", () => {
		const result = vertexKeyConfigSchema.safeParse({ ...base, _auth_type: "aws_workload_identity" });
		expect(result.success).toBe(false);
		expect(result.error?.issues.map((issue) => issue.path.join("."))).toContain("aws_workload_identity.audience");
	});

	it("accepts a minimal federation block", () => {
		const result = vertexKeyConfigSchema.safeParse({
			...base,
			_auth_type: "aws_workload_identity",
			aws_workload_identity: { audience },
		});
		expect(result.success).toBe(true);
	});

	it("accepts the full federation block with an env-referenced role", () => {
		const result = vertexKeyConfigSchema.safeParse({
			...base,
			_auth_type: "aws_workload_identity",
			aws_workload_identity: {
				audience,
				service_account_email: secret("vertex@my-project.iam.gserviceaccount.com"),
				token_lifetime_seconds: 1800,
				aws_region: secret("us-east-1"),
				aws_role_arn: { value: "", ref: "env.VERTEX_AWS_ROLE_ARN" },
			},
		});
		expect(result.success).toBe(true);
	});

	it("bounds the impersonated token lifetime", () => {
		for (const token_lifetime_seconds of [599, 43201, 1800.5]) {
			const result = vertexKeyConfigSchema.safeParse({
				...base,
				_auth_type: "aws_workload_identity",
				aws_workload_identity: { audience, token_lifetime_seconds },
			});
			expect(result.success, `lifetime ${token_lifetime_seconds}`).toBe(false);
			expect(result.error?.issues.map((issue) => issue.path.join("."))).toContain("aws_workload_identity.token_lifetime_seconds");
		}
	});

	it("rejects a credentials JSON alongside a federation audience regardless of the selected tab", () => {
		const result = vertexKeyConfigSchema.safeParse({
			...base,
			_auth_type: "service_account_json",
			auth_credentials: secret('{"type":"service_account"}'),
			aws_workload_identity: { audience },
		});
		expect(result.success).toBe(false);
		expect(result.error?.issues.map((issue) => issue.path.join("."))).toContain("auth_credentials");
	});

	it("ignores an empty federation block left behind by a tab switch", () => {
		const result = vertexKeyConfigSchema.safeParse({
			...base,
			_auth_type: "service_account",
			aws_workload_identity: { audience: secret("") },
		});
		expect(result.success).toBe(true);
	});
});