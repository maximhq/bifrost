import type { ModelRouter } from "@/lib/types/complexityRouter";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, test, vi } from "vitest";
import { RouterEnabledSwitch } from "./routersTable";
import { matchesRouterSearch } from "./routersTable.utils";

const mocks = vi.hoisted(() => ({
	setRouting: vi.fn(),
	// The props the switch control was last rendered with.
	switchProps: undefined as Record<string, unknown> | undefined,
	toastSuccess: vi.fn(),
	toastError: vi.fn(),
}));

vi.mock("@/lib/store/apis/governanceApi", async (importOriginal) => ({
	...(await importOriginal<typeof import("@/lib/store/apis/governanceApi")>()),
	useSetModelRouterRoutingMutation: () => [mocks.setRouting, { isLoading: false }],
}));
// The control itself is not under test: what the routers list hands it is.
vi.mock("@/components/ui/switch", () => ({
	Switch: (props: Record<string, unknown>) => {
		mocks.switchProps = props;
		return null;
	},
}));
vi.mock("sonner", async (importOriginal) => ({
	...(await importOriginal<typeof import("sonner")>()),
	toast: { success: mocks.toastSuccess, error: mocks.toastError },
}));

const router = { id: "support-desk", name: "Support", description: "Customer tickets" };

describe("matchesRouterSearch", () => {
	test("an empty or blank search matches every router", () => {
		expect(matchesRouterSearch(router, "")).toBe(true);
		expect(matchesRouterSearch(router, "   ")).toBe(true);
	});

	test("matches the name, the description or the id, ignoring case", () => {
		expect(matchesRouterSearch(router, "SUPP")).toBe(true);
		expect(matchesRouterSearch(router, "tickets")).toBe(true);
		expect(matchesRouterSearch(router, "desk")).toBe(true);
	});

	test("ignores the spaces around the search", () => {
		expect(matchesRouterSearch(router, "  support  ")).toBe(true);
	});

	test("does not match text none of them contains", () => {
		expect(matchesRouterSearch(router, "billing")).toBe(false);
		expect(matchesRouterSearch({ id: "x", name: "Billing" }, "tickets")).toBe(false);
	});

	test("matches a router whose name is typed in another case than it was saved in", () => {
		expect(matchesRouterSearch({ id: "r1", name: "EU Support" }, "eu support")).toBe(true);
	});

	test("keeps the spaces inside the search: the words must appear together", () => {
		expect(matchesRouterSearch(router, "customer tickets")).toBe(true);
		expect(matchesRouterSearch(router, "tickets customer")).toBe(false);
		expect(matchesRouterSearch(router, "customer  tickets")).toBe(false);
	});

	test("reads the search as plain text, not as a pattern", () => {
		const dotted = { id: "r1", name: "gpt-4.1 (beta)" };
		expect(matchesRouterSearch(dotted, "4.1 (")).toBe(true);
		expect(matchesRouterSearch(dotted, "gpt.4")).toBe(false);
		expect(matchesRouterSearch(dotted, ".*")).toBe(false);
		expect(matchesRouterSearch(dotted, "[")).toBe(false);
	});

	test("a router with no description is still found by its name or id", () => {
		expect(matchesRouterSearch({ id: "b7f3c2", name: "Billing", description: undefined }, "b7f3")).toBe(true);
		expect(matchesRouterSearch({ id: "b7f3c2", name: "Billing", description: "" }, "bill")).toBe(true);
		expect(matchesRouterSearch({ id: "b7f3c2", name: "Billing" }, "undefined")).toBe(false);
	});

	test("does not join the name, description and id into one text", () => {
		// "Support" ends the name and "Customer" starts the description.
		expect(matchesRouterSearch(router, "supportcustomer")).toBe(false);
		expect(matchesRouterSearch(router, "support customer")).toBe(false);
	});
});

describe("RouterEnabledSwitch", () => {
	// A router narrowed to a team, with a condition and a priority of its own.
	const teamRouter = (enabled: boolean) =>
		({
			id: "support-desk",
			name: "Support",
			routing: {
				enabled,
				scope: "team",
				scope_id: "team-1",
				cel_expression: "model == 'gpt-4o'",
				query: { combinator: "and", rules: [{ field: "model", operator: "=", value: "gpt-4o" }] },
				priority: 3,
			},
		}) as unknown as ModelRouter;

	const render = (props: { router: ModelRouter; disabled?: boolean }) => {
		renderToStaticMarkup(createElement(RouterEnabledSwitch, props));
		const rendered = mocks.switchProps;
		if (!rendered) throw new Error("the switch was not rendered");
		return rendered as { checked: boolean; disabled?: boolean; onAsyncCheckedChange: (checked: boolean) => Promise<void> };
	};
	const saves = (result: Promise<unknown>) => mocks.setRouting.mockReturnValue({ unwrap: () => result });

	beforeEach(() => {
		mocks.switchProps = undefined;
		mocks.setRouting.mockReset();
		mocks.toastSuccess.mockReset();
		mocks.toastError.mockReset();
	});

	test("shows whether the router is on, and can be locked for a reader", () => {
		expect(render({ router: teamRouter(true) }).checked).toBe(true);
		expect(render({ router: teamRouter(false) }).checked).toBe(false);
		expect(render({ router: teamRouter(true), disabled: true }).disabled).toBe(true);
	});

	test("turning a router off sends only enabled, never the scope, condition or priority it was read with", async () => {
		saves(Promise.resolve({}));
		await render({ router: teamRouter(true) }).onAsyncCheckedChange(false);
		expect(mocks.setRouting).toHaveBeenCalledTimes(1);
		expect(mocks.setRouting.mock.calls[0]).toStrictEqual([{ id: "support-desk", routing: { enabled: false } }]);
	});

	test("turning a router on sends only enabled", async () => {
		saves(Promise.resolve({}));
		await render({ router: teamRouter(false) }).onAsyncCheckedChange(true);
		expect(mocks.setRouting.mock.calls[0]).toStrictEqual([{ id: "support-desk", routing: { enabled: true } }]);
	});

	test("says the router is on or off once the gateway has saved it", async () => {
		saves(Promise.resolve({}));
		await render({ router: teamRouter(false) }).onAsyncCheckedChange(true);
		expect(mocks.toastSuccess).toHaveBeenCalledTimes(1);
		expect(mocks.toastSuccess.mock.calls[0][0]).toContain("Support");
		expect(mocks.toastError).not.toHaveBeenCalled();
	});

	test("shows the gateway's reason when the change is refused, and lets the switch settle", async () => {
		saves(Promise.reject({ status: 409, data: { error: { message: "priority 3 is taken in this scope" } } }));
		// Resolving, not rejecting, is what ends the switch's pending state.
		await expect(render({ router: teamRouter(false) }).onAsyncCheckedChange(true)).resolves.toBeUndefined();
		expect(mocks.toastSuccess).not.toHaveBeenCalled();
		expect(mocks.toastError).toHaveBeenCalledTimes(1);
		expect(mocks.toastError.mock.calls[0][0]).toMatch(/Support.*priority 3 is taken in this scope/i);
	});
});