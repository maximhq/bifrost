import type { GetVirtualKeysResponse } from "@/lib/types/governance";
import { RbacResource } from "@enterprise/lib";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, test, vi } from "vitest";
import { useOnboardingChecklist } from "./useOnboardingChecklist";

const mocks = vi.hoisted(() => ({
	enterprise: true,
	// Resources the viewer may not read; every other one is allowed.
	denied: new Set<string>(),
	coreConfig: { data: undefined as object | undefined },
	// Stands in for the virtual-keys read: called with the query argument and options, answers with the query's state.
	virtualKeys: vi.fn(),
}));

vi.mock("@/lib/constants/config", async (importOriginal) => ({
	...(await importOriginal<typeof import("@/lib/constants/config")>()),
	get IS_ENTERPRISE() {
		return mocks.enterprise;
	},
}));

vi.mock("@/lib/store", async (importOriginal) => ({
	...(await importOriginal<typeof import("@/lib/store")>()),
	useGetCoreConfigQuery: () => mocks.coreConfig,
}));

vi.mock("@/lib/store/apis/governanceApi", async (importOriginal) => ({
	...(await importOriginal<typeof import("@/lib/store/apis/governanceApi")>()),
	useGetVirtualKeysQuery: (arg: unknown, options: unknown) => mocks.virtualKeys(arg, options),
	useGetModelConfigsQuery: () => ({ data: undefined }),
}));

vi.mock("@/lib/store/apis/providersApi", async (importOriginal) => ({
	...(await importOriginal<typeof import("@/lib/store/apis/providersApi")>()),
	useGetAllKeysQuery: () => ({ data: undefined }),
}));

vi.mock("@enterprise/lib/store/apis/scimApi", async (importOriginal) => ({
	...(await importOriginal<typeof import("@enterprise/lib/store/apis/scimApi")>()),
	useGetSCIMProvidersQuery: () => ({ data: undefined }),
}));

vi.mock("@enterprise/lib", async (importOriginal) => ({
	...(await importOriginal<typeof import("@enterprise/lib")>()),
	useRbac: (resource: string) => !mocks.denied.has(resource),
}));

// readHook runs a hook through one render and returns what it gave.
function readHook<T>(hook: () => T): T {
	let value: { current: T } | undefined;
	const Probe = () => {
		value = { current: hook() };
		return null;
	};
	renderToStaticMarkup(createElement(Probe));
	if (!value) throw new Error("the hook did not run");
	return value.current;
}

const answered = (total_count: number | undefined) => ({
	data: { virtual_keys: [], count: 0, total_count, limit: 1, offset: 0 } as unknown as GetVirtualKeysResponse,
});

const virtualKeysStep = () => readHook(() => useOnboardingChecklist()).steps.find((step) => step.id === "virtual-keys");

describe("useOnboardingChecklist virtual keys", () => {
	beforeEach(() => {
		mocks.enterprise = true;
		mocks.denied.clear();
		mocks.coreConfig = { data: {} };
		mocks.virtualKeys.mockReset();
		mocks.virtualKeys.mockReturnValue({ data: undefined });
	});

	test("asks for one virtual key, since only the total matters", () => {
		readHook(() => useOnboardingChecklist());
		expect(mocks.virtualKeys).toHaveBeenCalledWith({ limit: 1, offset: 0 }, { skip: false });
	});

	test("skips the read until the config arrives, when told to, when dismissed, outside enterprise and without access", () => {
		const skipped = () => mocks.virtualKeys.mock.lastCall?.[1];

		mocks.coreConfig = { data: undefined };
		readHook(() => useOnboardingChecklist());
		expect(skipped(), "config pending").toEqual({ skip: true });

		mocks.coreConfig = { data: {} };
		readHook(() => useOnboardingChecklist({ skip: true }));
		expect(skipped(), "caller skip").toEqual({ skip: true });

		mocks.coreConfig = { data: { disable_onboarding: true } };
		readHook(() => useOnboardingChecklist());
		expect(skipped(), "dismissed").toEqual({ skip: true });

		mocks.coreConfig = { data: {} };
		mocks.enterprise = false;
		readHook(() => useOnboardingChecklist());
		expect(skipped(), "not enterprise").toEqual({ skip: true });

		mocks.enterprise = true;
		mocks.denied.add(RbacResource.VirtualKeys);
		readHook(() => useOnboardingChecklist());
		expect(skipped(), "no virtual-keys access").toEqual({ skip: true });
	});

	test("is complete once any virtual key exists", () => {
		mocks.virtualKeys.mockReturnValue(answered(3));
		expect(virtualKeysStep()?.complete).toBe(true);
	});

	test("is incomplete with no virtual keys or no total", () => {
		mocks.virtualKeys.mockReturnValue(answered(0));
		expect(virtualKeysStep()?.complete).toBe(false);
		mocks.virtualKeys.mockReturnValue(answered(undefined));
		expect(virtualKeysStep()?.complete).toBe(false);
		mocks.virtualKeys.mockReturnValue({ data: undefined });
		expect(virtualKeysStep()?.complete).toBe(false);
	});
});
