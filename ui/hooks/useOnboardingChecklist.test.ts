import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

const queries = vi.hoisted(() => ({
	config: { metadata: { onboarding_dismissed: false } },
	virtualKeys: vi.fn(() => ({ data: { total_count: 45087, virtual_keys: [] } })),
}));

vi.mock("@/lib/constants/config", () => ({ IS_ENTERPRISE: true }));
vi.mock("@/lib/store", () => ({ useGetCoreConfigQuery: () => ({ data: queries.config }) }));
vi.mock("@/lib/store/apis/governanceApi", () => ({
	useGetVirtualKeysQuery: queries.virtualKeys,
	useGetModelConfigsQuery: () => ({ data: { total_count: 0 } }),
}));
vi.mock("@/lib/store/apis/providersApi", () => ({ useGetAllKeysQuery: () => ({ data: [] }) }));
vi.mock("@enterprise/lib", () => ({
	RbacOperation: { View: "view" },
	RbacResource: { VirtualKeys: "virtual-keys", Governance: "governance", UserProvisioning: "users" },
	useRbac: () => true,
}));
vi.mock("@enterprise/lib/store/apis/scimApi", () => ({ useGetSCIMProvidersQuery: () => ({ data: [] }) }));

import { useOnboardingChecklist } from "./useOnboardingChecklist";

function Checklist() {
	const { steps } = useOnboardingChecklist();
	return createElement("span", null, String(steps.find((step) => step.id === "virtual-keys")?.complete));
}

describe("onboarding virtual-key existence check", () => {
	beforeEach(() => {
		vi.clearAllMocks();
		queries.config.metadata.onboarding_dismissed = false;
		queries.virtualKeys.mockReturnValue({ data: { total_count: 45087, virtual_keys: [] } });
	});

	it("requests only one key and uses total_count for completion", () => {
		expect(renderToStaticMarkup(createElement(Checklist))).toContain("true");
		expect(queries.virtualKeys).toHaveBeenCalledWith({ limit: 1, offset: 0 }, { skip: false });
	});

	it("keeps the setup step incomplete when there are no keys", () => {
		queries.virtualKeys.mockReturnValue({ data: { total_count: 0, virtual_keys: [] } });
		expect(renderToStaticMarkup(createElement(Checklist))).toContain("false");
	});

	it("keeps the query skipped after permanent dismissal", () => {
		queries.config.metadata.onboarding_dismissed = true;
		renderToStaticMarkup(createElement(Checklist));
		expect(queries.virtualKeys).toHaveBeenCalledWith({ limit: 1, offset: 0 }, { skip: true });
	});
});