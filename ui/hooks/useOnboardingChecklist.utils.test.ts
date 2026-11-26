import { describe, expect, it } from "vitest";
import { isOnboardingChecklistReady, onboardingStepsHiddenByAccess, type OnboardingAccess } from "./useOnboardingChecklist.utils";

const fullAccess: OnboardingAccess = { providerKeys: true, virtualKeys: true, modelConfigs: true, scimProviders: true };
const noAccess: OnboardingAccess = { providerKeys: false, virtualKeys: false, modelConfigs: false, scimProviders: false };
const allLoaded = { config: true, ...fullAccess };

describe("onboardingStepsHiddenByAccess", () => {
	it("hides nothing with full access", () => {
		expect([...onboardingStepsHiddenByAccess({ isEnterprise: true, access: fullAccess })]).toEqual([]);
	});

	it("hides each step whose data the user cannot read", () => {
		expect([...onboardingStepsHiddenByAccess({ isEnterprise: true, access: noAccess })].sort()).toEqual(
			["dashboard-auth", "models", "provider-key", "scim", "virtual-keys"].sort(),
		);
	});

	it("hides dashboard auth with SCIM on enterprise only, where SSO completes it", () => {
		const access = { ...fullAccess, scimProviders: false };
		expect(onboardingStepsHiddenByAccess({ isEnterprise: true, access }).has("dashboard-auth")).toBe(true);
		expect(onboardingStepsHiddenByAccess({ isEnterprise: false, access }).has("dashboard-auth")).toBe(false);
	});

	it("only hides the provider key step in OSS", () => {
		expect([...onboardingStepsHiddenByAccess({ isEnterprise: false, access: noAccess })]).toEqual(["provider-key"]);
	});
});

describe("isOnboardingChecklistReady", () => {
	it("waits for core config", () => {
		expect(isOnboardingChecklistReady({ isEnterprise: true, access: fullAccess, loaded: { ...allLoaded, config: false } })).toBe(false);
	});

	it("waits for every readable source", () => {
		expect(isOnboardingChecklistReady({ isEnterprise: true, access: fullAccess, loaded: allLoaded })).toBe(true);
		expect(isOnboardingChecklistReady({ isEnterprise: true, access: fullAccess, loaded: { ...allLoaded, scimProviders: false } })).toBe(
			false,
		);
		expect(isOnboardingChecklistReady({ isEnterprise: true, access: fullAccess, loaded: { ...allLoaded, providerKeys: false } })).toBe(
			false,
		);
	});

	it("does not wait for sources the user cannot read", () => {
		const loaded = { config: true, ...noAccess };
		expect(isOnboardingChecklistReady({ isEnterprise: true, access: noAccess, loaded })).toBe(true);
		expect(isOnboardingChecklistReady({ isEnterprise: true, access: { ...noAccess, modelConfigs: true }, loaded })).toBe(false);
	});

	it("ignores enterprise sources in OSS", () => {
		const loaded = { config: true, providerKeys: true, virtualKeys: false, modelConfigs: false, scimProviders: false };
		expect(isOnboardingChecklistReady({ isEnterprise: false, access: fullAccess, loaded })).toBe(true);
	});
});