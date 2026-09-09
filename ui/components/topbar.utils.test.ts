import { describe, expect, it } from "vitest";
import { deriveTitleFromPathname } from "./topbar.utils";

describe("deriveTitleFromPathname", () => {
	it("title-cases the last path segment", () => {
		expect(deriveTitleFromPathname("/workspace/governance")).toBe("Governance");
	});

	it("splits hyphenated segments into words", () => {
		expect(deriveTitleFromPathname("/workspace/routing-rules")).toBe("Routing Rules");
	});

	it("shouts known acronyms", () => {
		expect(deriveTitleFromPathname("/workspace/mcp-registry")).toBe("MCP Registry");
	});

	it("falls back to Dashboard at the root", () => {
		expect(deriveTitleFromPathname("/")).toBe("Dashboard");
		expect(deriveTitleFromPathname("")).toBe("Dashboard");
	});

	it("ignores a trailing slash", () => {
		expect(deriveTitleFromPathname("/workspace/governance/")).toBe("Governance");
	});

	// These are why <PageTitle> has to be rendered on a page's loading and empty
	// branches too: on these routes the derived fallback is not a blank title,
	// it is a *different* one, so dropping to it is a silent mislabel.
	it("cannot reproduce titles the slug does not contain", () => {
		expect(deriveTitleFromPathname("/workspace/custom-pricing/overrides")).toBe("Overrides");
		expect(deriveTitleFromPathname("/workspace/model-limits")).toBe("Model Limits");
	});

	it("uses the override table for routes the slug mislabels", () => {
		expect(deriveTitleFromPathname("/workspace/routing-rules/tree")).toBe("Routing Tree");
		expect(deriveTitleFromPathname("/workspace/scim")).toBe("User Provisioning");
		expect(deriveTitleFromPathname("/workspace/providers")).toBe("Model Providers");
		expect(deriveTitleFromPathname("/workspace/governance/rbac")).toBe("Roles & Permissions");
	});

	it("folds the parent into sub-item labels that mean nothing alone", () => {
		expect(deriveTitleFromPathname("/workspace/alerting/rules")).toBe("Alert Rules");
		expect(deriveTitleFromPathname("/workspace/guardrails/rules")).not.toBe("Alert Rules");
		expect(deriveTitleFromPathname("/workspace/guardrails/configuration")).toBe("Guardrail Rules");
		expect(deriveTitleFromPathname("/workspace/edge-control/inventory")).toBe("Edge Approvals");
	});

	it("matches an override through a trailing slash or query string", () => {
		expect(deriveTitleFromPathname("/workspace/scim/")).toBe("User Provisioning");
		expect(deriveTitleFromPathname("/workspace/scim?tab=mappings")).toBe("User Provisioning");
	});

	it("leaves a child route alone when only its parent is overridden", () => {
		// /workspace/logs is "LLM Logs", but its child must not inherit that.
		expect(deriveTitleFromPathname("/workspace/logs")).toBe("LLM Logs");
		expect(deriveTitleFromPathname("/workspace/logs/connectors")).toBe("Log Connectors");
		expect(deriveTitleFromPathname("/workspace/config/branding")).toBe("Branding");
	});
});