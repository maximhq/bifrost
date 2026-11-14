import { IS_ENTERPRISE } from "@/lib/constants/config";
import { useGetCoreConfigQuery } from "@/lib/store";
import { useGetModelConfigsQuery, useGetVirtualKeysQuery } from "@/lib/store/apis/governanceApi";
import { useGetAllKeysQuery } from "@/lib/store/apis/providersApi";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useGetSCIMProvidersQuery } from "@enterprise/lib/store/apis/scimApi";
import { useMemo } from "react";
import { isOnboardingChecklistReady, onboardingStepsHiddenByAccess, type OnboardingAccess } from "./useOnboardingChecklist.utils";

export const METADATA_DISMISSED_KEY = "onboarding_dismissed";
export const METADATA_SKIPPED_KEY = "onboarding_skipped";
// "Remind me later" snoozes the widget by setting this cookie's expiry to the
// chosen date — once the browser drops the cookie, the widget is due again.
export const REMIND_LATER_COOKIE = "bifrost_onboarding_remind_at";
// Closing via X is a session-scoped hide, not a long-lived dismissal — no
// expiry, explicitly cleared on route change. Both cookies are shared
// constants (not just widget-local) so the sidebar's "resume setup" promo
// card can read the same hidden/snoozed state the floating widget uses.
export const HIDDEN_UNTIL_NAV_COOKIE = "bifrost_onboarding_hidden_until_nav";

export type OnboardingSection = "Security" | "Provider Setup" | "Everything Else";

export interface OnboardingStep {
	id: string;
	title: string;
	route: string;
	complete: boolean;
	section: OnboardingSection;
}

export const parseSkippedIds = (raw: unknown) => (Array.isArray(raw) ? raw.filter((id): id is string => typeof id === "string") : []);

const authValueSet = (secretVar: { value?: string; ref?: string; type?: string } | undefined) => {
	if (!secretVar) return false;
	return !!secretVar.value || !!secretVar.ref;
};

// Shared onboarding checklist state — both the floating OnboardingWidget and
// the sidebar's "resume setup" promo card read this, so they can't silently
// disagree on what counts as done. RTK Query dedupes the underlying
// core-config/keys/governance calls across both consumers automatically.
// This runs from the sidebar on every page, so each gated source is fetched only
// with the permission its endpoint needs; a step the user cannot read is hidden
// (see onboardingStepsHiddenByAccess) instead of waiting on a 403 forever.
export function useOnboardingChecklist({ skip = false }: { skip?: boolean } = {}) {
	const { data: bifrostConfig } = useGetCoreConfigQuery({}, { skip });
	// disable_onboarding comes from config.json / BIFROST_DISABLE_ONBOARDING and holds on
	// stateless deployments where the metadata flag would be lost on restart.
	const isDismissedForAll = bifrostConfig?.disable_onboarding === true || bifrostConfig?.metadata?.[METADATA_DISMISSED_KEY] === true;
	// Hold the checklist queries until config arrives, so a dismissed instance never fetches them.
	const shouldSkipChecklistQueries = skip || bifrostConfig === undefined || isDismissedForAll;

	const hasVirtualKeysAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.View);
	const hasGovernanceAccess = useRbac(RbacResource.Governance, RbacOperation.View);
	const hasUserProvisioningAccess = useRbac(RbacResource.UserProvisioning, RbacOperation.View);
	const access = useMemo<OnboardingAccess>(
		() => ({
			providerKeys: hasVirtualKeysAccess,
			virtualKeys: hasVirtualKeysAccess,
			modelConfigs: hasGovernanceAccess,
			scimProviders: hasUserProvisioningAccess,
		}),
		[hasVirtualKeysAccess, hasGovernanceAccess, hasUserProvisioningAccess],
	);

	const { data: allKeys } = useGetAllKeysQuery(undefined, { skip: shouldSkipChecklistQueries || !access.providerKeys });
	const { data: vksResponse } = useGetVirtualKeysQuery(undefined, {
		skip: shouldSkipChecklistQueries || !IS_ENTERPRISE || !access.virtualKeys,
	});
	const { data: modelConfigsResponse } = useGetModelConfigsQuery(undefined, {
		skip: shouldSkipChecklistQueries || !IS_ENTERPRISE || !access.modelConfigs,
	});
	const { data: scimProviders } = useGetSCIMProvidersQuery(undefined, {
		skip: shouldSkipChecklistQueries || !IS_ENTERPRISE || !access.scimProviders,
	});

	const checklistReady = isOnboardingChecklistReady({
		isEnterprise: IS_ENTERPRISE,
		access,
		loaded: {
			config: bifrostConfig !== undefined,
			providerKeys: allKeys !== undefined,
			virtualKeys: vksResponse !== undefined,
			modelConfigs: modelConfigsResponse !== undefined,
			scimProviders: scimProviders !== undefined,
		},
	});
	const hiddenStepIds = useMemo(() => onboardingStepsHiddenByAccess({ isEnterprise: IS_ENTERPRISE, access }), [access]);

	const skippedIds = useMemo<string[]>(() => {
		return parseSkippedIds(bifrostConfig?.metadata?.[METADATA_SKIPPED_KEY]);
	}, [bifrostConfig?.metadata]);

	const authConfig = bifrostConfig?.auth_config;
	const clientConfig = bifrostConfig?.client_config;
	// Enterprise SSO satisfies dashboard auth on its own. An identity-provider
	// record holds the OIDC credentials (clientId/clientSecret/tenantId), and the
	// server installs SCIMController.Middleware() in place of the OSS
	// AuthMiddleware whenever one is enabled, so admin_username/admin_password
	// are never set on these deployments and would strand this step forever.
	// Mirrors the server's own gate: SCIMConfig != nil && SCIMConfig.Enabled.
	const ssoGatesDashboard = IS_ENTERPRISE && (scimProviders?.some((provider) => provider.enabled) ?? false);

	const steps: OnboardingStep[] = useMemo(() => {
		// Order: 1) Security, 2) Provider Setup, 3) Everything Else.
		// Security comes first so admins lock down access before exposing keys.
		const allowedOrigins = clientConfig?.allowed_origins ?? [];
		// Titles are English fallbacks: the widget translates through shell:onboarding.steps.<id>.
		const common: OnboardingStep[] = [
			{
				id: "cors",
				title: "Restrict CORS origins", // i18n-ignore
				route: "/workspace/config/security",
				section: "Security",
				complete: allowedOrigins.some((origin) => origin.trim().length > 0) && allowedOrigins.every((origin) => origin.trim() !== "*"),
			},
			{
				id: "dashboard-auth",
				title: "Set up dashboard auth", // i18n-ignore
				route: "/workspace/config/security",
				section: "Security",
				complete:
					ssoGatesDashboard ||
					(!!authConfig?.is_enabled && authValueSet(authConfig?.admin_username) && authValueSet(authConfig?.admin_password)),
			},
			{
				id: "enforce-inference-auth",
				title: "Enforce auth on inference", // i18n-ignore
				route: "/workspace/config/security",
				section: "Security",
				complete: !!clientConfig?.enforce_auth_on_inference,
			},
			{
				id: "provider-key",
				title: "Add a provider key", // i18n-ignore
				route: "/workspace/providers",
				section: "Provider Setup",
				complete: (allKeys?.length ?? 0) > 0,
			},
		];
		const enterprise: OnboardingStep[] = IS_ENTERPRISE
			? [
					{
						id: "scim",
						title: "Configure SCIM provisioning", // i18n-ignore
						route: "/workspace/scim",
						section: "Everything Else",
						complete: (scimProviders?.length ?? 0) > 0,
					},
					{
						id: "models",
						title: "Configure governance model catalog", // i18n-ignore
						route: "/workspace/model-catalog",
						section: "Everything Else",
						complete: (modelConfigsResponse?.total_count ?? 0) > 0,
					},
					{
						id: "virtual-keys",
						title: "Set up virtual keys / access profiles", // i18n-ignore
						route: "/workspace/virtual-keys",
						section: "Everything Else",
						complete: (vksResponse?.total_count ?? 0) > 0,
					},
				]
			: [];
		return [...common, ...enterprise].filter((step) => !hiddenStepIds.has(step.id));
	}, [allKeys, clientConfig, authConfig, ssoGatesDashboard, scimProviders, modelConfigsResponse, vksResponse, hiddenStepIds]);

	return { bifrostConfig, steps, skippedIds, checklistReady, isDismissedForAll };
}