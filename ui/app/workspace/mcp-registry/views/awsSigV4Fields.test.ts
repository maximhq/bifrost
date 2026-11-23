import { describe, expect, it } from "vitest";
import { awsSigV4FormValues, awsSigV4Payload } from "./awsSigV4Fields";

describe("awsSigV4Payload", () => {
	it("defaults the service and drops empty optional fields", () => {
		expect(awsSigV4Payload({ region: { value: "us-east-1" }, role_arn: { value: "", ref: "" } })).toEqual({
			region: { value: "us-east-1" },
			service: "bedrock-agentcore",
			role_arn: undefined,
			external_id: undefined,
			session_name: undefined,
		});
	});

	it("sends the external ID and session name only with a role", () => {
		const role = { value: "arn:aws:iam::123456789012:role/gateway" };
		const withRole = awsSigV4Payload({
			region: { value: "", ref: "env.AWS_REGION", type: "env" },
			service: "lambda",
			role_arn: role,
			external_id: { value: "ext" },
			session_name: { value: "bifrost-mcp" },
		});
		expect(withRole).toEqual({
			region: { value: "", ref: "env.AWS_REGION", type: "env" },
			service: "lambda",
			role_arn: role,
			external_id: { value: "ext" },
			session_name: { value: "bifrost-mcp" },
		});

		const roleCleared = awsSigV4Payload({ region: { value: "us-east-1" }, external_id: { value: "ext" }, session_name: { value: "s1" } });
		expect(roleCleared.external_id).toBeUndefined();
		expect(roleCleared.session_name).toBeUndefined();
	});

	it("sends an empty region rather than omitting it, so the server reports it", () => {
		expect(awsSigV4Payload(undefined).region).toEqual({ value: "", ref: "" });
	});
});

describe("awsSigV4FormValues", () => {
	it("seeds the stored block, defaulting the service", () => {
		const region = { value: "us-e****t-1", ref: "env.AWS_REGION", type: "env" as const };
		expect(awsSigV4FormValues({ region })).toEqual({
			region,
			service: "bedrock-agentcore",
			role_arn: undefined,
			external_id: undefined,
			session_name: undefined,
		});
		expect(awsSigV4FormValues(undefined).service).toBe("bedrock-agentcore");
	});
});