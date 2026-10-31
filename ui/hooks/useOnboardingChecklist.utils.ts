/**
 * Which of the checklist's RBAC-gated data sources the signed-in user can read. Core config is
 * readable by everyone, so it has no flag here.
 */
export interface OnboardingAccess {
	/** GET /keys, gated on VirtualKeys:View. */
	providerKeys: boolean;
	/** GET /governance/virtual-keys, gated on VirtualKeys:View. */
	virtualKeys: boolean;
	/** GET /governance/model-configs, gated on Governance:View. */
	modelConfigs: boolean;
	/** GET /scim/providers, gated on UserProvisioning:View. */
	scimProviders: boolean;
}

/**
 * The steps the checklist cannot judge for this user, because the data that completes them is
 * behind a permission they lack. Such a step is hidden rather than left incomplete: it would
 * otherwise stay unticked forever (and keep the "resume setup" card up) on a deployment where it
 * is already done. Dashboard auth goes with SCIM on enterprise, since an enabled SSO provider is
 * what completes it there.
 */
export function onboardingStepsHiddenByAccess({ isEnterprise, access }: { isEnterprise: boolean; access: OnboardingAccess }): Set<string> {
	const hidden = new Set<string>();
	if (!access.providerKeys) hidden.add("provider-key");
	if (!isEnterprise) return hidden;
	if (!access.scimProviders) {
		hidden.add("scim");
		hidden.add("dashboard-auth");
	}
	if (!access.modelConfigs) hidden.add("models");
	if (!access.virtualKeys) hidden.add("virtual-keys");
	return hidden;
}

/**
 * Whether every source the checklist reads has loaded. A source the user cannot read is never
 * fetched, so it is not waited on; waiting would keep the checklist from ever showing.
 */
export function isOnboardingChecklistReady({
	isEnterprise,
	access,
	loaded,
}: {
	isEnterprise: boolean;
	access: OnboardingAccess;
	loaded: { config: boolean } & OnboardingAccess;
}): boolean {
	if (!loaded.config) return false;
	if (access.providerKeys && !loaded.providerKeys) return false;
	if (!isEnterprise) return true;
	return (
		(!access.virtualKeys || loaded.virtualKeys) &&
		(!access.modelConfigs || loaded.modelConfigs) &&
		(!access.scimProviders || loaded.scimProviders)
	);
}