import { IS_ENTERPRISE } from "@/lib/constants/config";
import { useGetCoreConfigQuery } from "@/lib/store";
import { useGetModelConfigsQuery, useGetVirtualKeysQuery } from "@/lib/store/apis/governanceApi";
import { useGetAllKeysQuery } from "@/lib/store/apis/providersApi";
import { useGetSCIMProvidersQuery } from "@enterprise/lib/store/apis/scimApi";
import { useMemo } from "react";

export const METADATA_DISMISSED_KEY = "onboarding_dismissed";
export const METADATA_SKIPPED_KEY = "onboarding_skipped";
// "稍后提醒" snoozes the widget by setting this cookie's expiry to the
// chosen date — once the browser drops the cookie, the widget is due again.
export const REMIND_LATER_COOKIE = "bifrost_onboarding_remind_at";
// Closing via X is a session-scoped hide, not a long-lived dismissal — no
// expiry, explicitly cleared on route change. Both cookies are shared
// constants (not just widget-local) so the sidebar's "resume setup" promo
// card can read the same hidden/snoozed state the floating widget uses.
export const HIDDEN_UNTIL_NAV_COOKIE = "bifrost_onboarding_hidden_until_nav";

export type OnboardingSection = "安全" | "Provider Setup" | "Everything Else";

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
export function useOnboardingChecklist({ skip = false }: { skip?: boolean } = {}) {
	const { data: bifrostConfig } = useGetCoreConfigQuery({}, { skip });
	const isDismissedForAll = bifrostConfig?.metadata?.[METADATA_DISMISSED_KEY] === true;
	const shouldSkipChecklistQueries = skip || isDismissedForAll;

	const { data: allKeys } = useGetAllKeysQuery(undefined, { skip: shouldSkipChecklistQueries });
	const { data: vksResponse } = useGetVirtualKeysQuery(undefined, {
		skip: shouldSkipChecklistQueries || !IS_ENTERPRISE,
	});
	const { data: modelConfigsResponse } = useGetModelConfigsQuery(undefined, {
		skip: shouldSkipChecklistQueries || !IS_ENTERPRISE,
	});
	const { data: scimProviders } = useGetSCIMProvidersQuery(undefined, {
		skip: shouldSkipChecklistQueries || !IS_ENTERPRISE,
	});

	const checklistReady =
		bifrostConfig !== undefined &&
		allKeys !== undefined &&
		(!IS_ENTERPRISE || (vksResponse !== undefined && modelConfigsResponse !== undefined && scimProviders !== undefined));

	const skippedIds = useMemo<string[]>(() => {
		return parseSkippedIds(bifrostConfig?.metadata?.[METADATA_SKIPPED_KEY]);
	}, [bifrostConfig?.metadata]);

	const authConfig = bifrostConfig?.auth_config;
	const clientConfig = bifrostConfig?.client_config;

	const steps: OnboardingStep[] = useMemo(() => {
		// Order: 1) Security, 2) Provider Setup, 3) Everything Else.
		// Security comes first so admins lock down access before exposing keys.
		const allowedOrigins = clientConfig?.allowed_origins ?? [];
		const common: OnboardingStep[] = [
			{
				id: "cors",
				title: "限制 CORS 来源",
				route: "/workspace/config/security",
				section: "安全",
				complete:
					allowedOrigins.some((origin) => origin.trim().length > 0) && allowedOrigins.every((origin) => origin.trim() !== "*"),
			},
			{
				id: "dashboard-auth",
				title: "设置仪表盘认证",
				route: "/workspace/config/security",
				section: "安全",
				complete: !!authConfig?.is_enabled && authValueSet(authConfig?.admin_username) && authValueSet(authConfig?.admin_password),
			},
			{
				id: "enforce-inference-auth",
				title: "在推理上强制执行认证",
				route: "/workspace/config/security",
				section: "安全",
				complete: !!clientConfig?.enforce_auth_on_inference,
			},
			{
				id: "provider-key",
				title: "添加提供商密钥",
				route: "/workspace/providers",
				section: "Provider Setup",
				complete: (allKeys?.length ?? 0) > 0,
			},
		];
		const enterprise: OnboardingStep[] = IS_ENTERPRISE
			? [
					{
						id: "scim",
						title: "配置 SCIM 供给",
						route: "/workspace/scim",
						section: "Everything Else",
						complete: (scimProviders?.length ?? 0) > 0,
					},
					{
						id: "models",
						title: "配置治理模型目录",
						route: "/workspace/model-catalog",
						section: "Everything Else",
						complete: (modelConfigsResponse?.total_count ?? 0) > 0,
					},
					{
						id: "virtual-keys",
						title: "设置虚拟密钥 / 访问配置文件",
						route: "/workspace/virtual-keys",
						section: "Everything Else",
						complete: (vksResponse?.total_count ?? 0) > 0,
					},
				]
			: [];
		return [...common, ...enterprise];
	}, [allKeys, clientConfig, authConfig, scimProviders, modelConfigsResponse, vksResponse]);

	return { bifrostConfig, steps, skippedIds, checklistReady, isDismissedForAll };
}
