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
});