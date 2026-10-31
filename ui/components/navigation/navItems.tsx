import {
	BadgeCheck,
	BadgeInfo,
	BookOpenText,
	BookUser,
	Bot,
	BoxIcon,
	Boxes,
	Building,
	Building2,
	ChartColumnBig,
	ChevronsLeftRightEllipsis,
	CircuitBoard,
	Construction,
	Database,
	DatabaseZap,
	Flag,
	FolderGit,
	Gavel,
	GitCompareArrows,
	Globe,
	Hexagon,
	History,
	House,
	KeyRound,
	Landmark,
	LaptopMinimalCheck,
	LayoutGrid,
	Logs,
	Megaphone,
	Network,
	Palette,
	Plug,
	Puzzle,
	ScrollText,
	SearchCheck,
	Server,
	Settings,
	Settings2Icon,
	ShieldCheck,
	Shuffle,
	Siren,
	SlidersHorizontal,
	SquareKanban,
	Telescope,
	ToolCase,
	TrendingUp,
	TriangleAlert,
	UserRoundCheck,
	Users,
	Wallet,
	WalletCards,
	Webhook,
	Waypoints,
} from "lucide-react";

import { WarpIcon } from "@/components/ui/icons";
import { useFeatureFlag } from "@/hooks/useFeatureFlag";
import { IS_ENTERPRISE } from "@/lib/constants/config";
import { FEATURE_FLAGS } from "@/lib/constants/featureFlags";
import { useGetCoreConfigQuery } from "@/lib/store";
import { cn } from "@/lib/utils";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import i18n from "@/lib/i18n";

import type { NavItem } from "./navItem";

export { getNavItemHref, type NavItem } from "./navItem";

// Custom MCP Icon Component
export const MCPIcon = ({ className, label }: { className?: string; label?: string }) => {
	const resolvedLabel = label ?? i18n.t("sidebar.mcpIconTitle", { ns: "shell" });
	return (
		<svg
			className={className}
			fill="currentColor"
			fillRule="evenodd"
			height="1em"
			style={{ flex: "none", lineHeight: 1 }}
			viewBox="0 0 24 24"
			width="1em"
			xmlns="http://www.w3.org/2000/svg"
			aria-label={resolvedLabel}
		>
			<title>{resolvedLabel}</title>
			<path d="M15.688 2.343a2.588 2.588 0 00-3.61 0l-9.626 9.44a.863.863 0 01-1.203 0 .823.823 0 010-1.18l9.626-9.44a4.313 4.313 0 016.016 0 4.116 4.116 0 011.204 3.54 4.3 4.3 0 013.609 1.18l.05.05a4.115 4.115 0 010 5.9l-8.706 8.537a.274.274 0 000 .393l1.788 1.754a.823.823 0 010 1.18.863.863 0 01-1.203 0l-1.788-1.753a1.92 1.92 0 010-2.754l8.706-8.538a2.47 2.47 0 000-3.54l-.05-.049a2.588 2.588 0 00-3.607-.003l-7.172 7.034-.002.002-.098.097a.863.863 0 01-1.204 0 .823.823 0 010-1.18l7.273-7.133a2.47 2.47 0 00-.003-3.537z" />
			<path d="M14.485 4.703a.823.823 0 000-1.18.863.863 0 00-1.204 0l-7.119 6.982a4.115 4.115 0 000 5.9 4.314 4.314 0 006.016 0l7.12-6.982a.823.823 0 000-1.18.863.863 0 00-1.204 0l-7.119 6.982a2.588 2.588 0 01-3.61 0 2.47 2.47 0 010-3.54l7.12-6.982z" />
		</svg>
	);
};

/**
 * Warp's mark, sized above the settings nav's default.
 *
 * The nav sizes every sub-item icon at h-3.5. Warp's is a filled glyph among
 * lucide's stroked ones, and a filled shape reads smaller at the same box
 * because its weight sits in the middle rather than on the outline, so it needs
 * to render larger to match them.
 *
 * It is scaled rather than resized, and that is the whole point. A bigger box
 * moves two things at once: the icon's left edge shifts out of the column its
 * neighbours share, and every pixel of extra width pushes the label right, so
 * "Warp" no longer starts where "Security" and "API Keys" do. A transform
 * changes none of that - layout still sees h-3.5, so the row stays on the grid
 * while the mark alone grows.
 */
export function WarpNavIcon({ className, ...props }: React.SVGProps<SVGSVGElement>) {
	return <WarpIcon className={cn(className, "scale-125")} {...props} />;
}

/**
 * The workspace navigation tree, with `hasAccess` resolved against the caller's RBAC
 * permissions and feature flags. The sidebar and the command palette both render from
 * this, so a page added here shows up in both.
 */
export function useNavItems(): NavItem[] {
	const { t, i18n } = useTranslation("shell");
	const hasLogsAccess = useRbac(RbacResource.Logs, RbacOperation.View);
	const hasObservabilityAccess = useRbac(RbacResource.Observability, RbacOperation.View);
	// Alerting is currently surfaced under the existing governance permission
	// until enterprise alerting gets its own RBAC resource.
	const hasAlertingAccess = useRbac(RbacResource.Governance, RbacOperation.View);
	const hasDashboardAccess = useRbac(RbacResource.Dashboard, RbacOperation.View);
	const hasModelProvidersAccess = useRbac(RbacResource.ModelProvider, RbacOperation.View);
	const hasMCPGatewayAccess = useRbac(RbacResource.MCPGateway, RbacOperation.View);
	const hasAgentGatewayAccess = useRbac(RbacResource.AgentGateway, RbacOperation.View);
	const hasAgentLogsAccess = useRbac(RbacResource.AgentLogs, RbacOperation.View);
	const hasRelaysAccess = useRbac(RbacResource.Relays, RbacOperation.View);
	const hasRelayLogsAccess = useRbac(RbacResource.RelayLogs, RbacOperation.View);
	const hasVirtualMCPsAccess = useRbac(RbacResource.VirtualMCPs, RbacOperation.View);
	const hasMCPLogsAccess = useRbac(RbacResource.MCPLogs, RbacOperation.View);
	const hasPluginsAccess = useRbac(RbacResource.Plugins, RbacOperation.View);
	const hasUsersAccess = useRbac(RbacResource.Users, RbacOperation.View);
	const hasUserProvisioningAccess = useRbac(RbacResource.UserProvisioning, RbacOperation.View);
	const hasAuditLogsAccess = useRbac(RbacResource.AuditLogs, RbacOperation.View);
	const hasCustomersAccess = useRbac(RbacResource.Customers, RbacOperation.View);
	const hasTeamsAccess = useRbac(RbacResource.Teams, RbacOperation.View);
	const hasBusinessUnitsAccess = useRbac(RbacResource.BusinessUnits, RbacOperation.View);
	const hasRbacAccess = useRbac(RbacResource.RBAC, RbacOperation.View);
	const hasVirtualKeysAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.View);
	const hasGovernanceLegacyAccess = useRbac(RbacResource.Governance, RbacOperation.View);
	const hasRoutingRulesAccess = useRbac(RbacResource.RoutingRules, RbacOperation.View);
	const hasGuardrailsProvidersAccess = useRbac(RbacResource.GuardrailsProviders, RbacOperation.View);
	const hasGuardrailsConfigAccess = useRbac(RbacResource.GuardrailsConfig, RbacOperation.View);
	const hasCircuitBreakerAccess = useRbac(RbacResource.CircuitBreaker, RbacOperation.View);
	const hasClusterConfigAccess = useRbac(RbacResource.Cluster, RbacOperation.View);
	const isAdaptiveRoutingAllowed = useRbac(RbacResource.AdaptiveRouter, RbacOperation.View);
	const hasSettingsAccess = useRbac(RbacResource.Settings, RbacOperation.View);
	const hasFeatureFlagsAccess = useRbac(RbacResource.FeatureFlags, RbacOperation.View);
	const isWarpEnabled = useFeatureFlag(FEATURE_FLAGS.warp);
	const hasWarpAccess = useRbac(RbacResource.Warp, RbacOperation.View);
	const hasAPIKeyAccess = useRbac(RbacResource.APIKeys, RbacOperation.View);
	const hasPromptRepositoryAccess = useRbac(RbacResource.PromptRepository, RbacOperation.View);
	const hasSkillsRepositoryAccess = useRbac(RbacResource.SkillsRepository, RbacOperation.View);
	const hasDevicesAccess = useRbac(RbacResource.Devices, RbacOperation.View);
	const hasInventoryAccess = useRbac(RbacResource.Inventory, RbacOperation.View);
	const hasEdgeConfigAccess = useRbac(RbacResource.EdgeConfig, RbacOperation.View);
	const hasAnyEdgeControlAccess = hasDevicesAccess || hasInventoryAccess || hasEdgeConfigAccess;
	const hasAccessProfilesAccess = useRbac(RbacResource.AccessProfiles, RbacOperation.View);
	const hasProjectsAccess = useRbac(RbacResource.Projects, RbacOperation.View);
	const hasAnyGovernanceAccess =
		hasVirtualKeysAccess ||
		hasTeamsAccess ||
		hasUsersAccess ||
		hasCustomersAccess ||
		hasBusinessUnitsAccess ||
		hasRbacAccess ||
		hasAccessProfilesAccess ||
		hasProjectsAccess ||
		hasGovernanceLegacyAccess;
	const { data: coreConfig } = useGetCoreConfigQuery({});
	const isDbConnected = coreConfig?.is_db_connected ?? false;

	const items = useMemo(
		() => [
			...(IS_ENTERPRISE
				? [
						{
							id: "home",
							title: t("nav.home"),
							url: "/workspace/home",
							icon: House,
							description: t("navDesc.home"),
							hasAccess: true,
						},
					]
				: []),
			{
				id: "observability",
				title: t("nav.observability"),
				url: "/workspace/logs",
				icon: Telescope,
				description: t("navDesc.observability"),
				hasAccess:
					hasLogsAccess ||
					hasDashboardAccess ||
					hasMCPLogsAccess ||
					hasAgentLogsAccess ||
					hasRelayLogsAccess ||
					hasObservabilityAccess ||
					hasSettingsAccess,
				subItems: [
					{
						id: "observability.dashboard",
						title: t("nav.dashboard"),
						url: "/workspace/dashboard",
						icon: ChartColumnBig,
						hasAccess: hasDashboardAccess,
					},
					{
						id: "observability.llm-logs",
						title: t("nav.llmLogs"),
						url: "/workspace/logs",
						icon: Logs,
						hasAccess: hasLogsAccess,
					},
					{
						id: "observability.mcp-logs",
						title: t("nav.mcpLogs"),
						url: "/workspace/mcp-logs",
						icon: (props: { className?: string }) => <MCPIcon {...props} label={t("sidebar.mcpIconTitle")} />,
						hasAccess: hasMCPLogsAccess,
					},
					{
						id: "observability.agent-logs",
						title: t("nav.agentLogs"),
						url: "/workspace/agent-logs",
						icon: Bot,
						description: t("navDesc.agentLogs"),
						hasAccess: hasAgentLogsAccess,
					},
					{
						id: "observability.relay-logs",
						title: t("nav.relayLogs"),
						url: "/workspace/relay-logs",
						icon: Waypoints,
						description: t("navDesc.relayLogs"),
						hasAccess: hasRelayLogsAccess,
					},
					{
						id: "observability.connectors",
						title: t("nav.connectors"),
						url: "/workspace/observability",
						icon: ChevronsLeftRightEllipsis,
						hasAccess: hasObservabilityAccess,
					},
					{
						id: "observability.logs-settings",
						title: t("nav.logsSettings"),
						url: "/workspace/config/logging",
						icon: Settings,
						hasAccess: hasSettingsAccess,
					},
				],
			},
			{
				id: "models",
				title: t("nav.models"),
				url: "/workspace/providers",
				icon: BoxIcon,
				hasAccess: true,
				subItems: [
					{
						id: "models.catalog",
						testId: "model-catalog",
						title: t("nav.modelCatalog"),
						url: "/workspace/model-catalog",
						icon: LayoutGrid,
						hasAccess: hasModelProvidersAccess,
					},
					{
						id: "models.providers",
						title: t("nav.modelProviders"),
						url: "/workspace/providers",
						icon: Boxes,
						hasAccess: hasModelProvidersAccess,
					},
					{
						id: "models.budgets",
						title: t("nav.budgetsLimits"),
						url: "/workspace/model-limits",
						icon: Wallet,
						hasAccess: hasGovernanceLegacyAccess,
					},
					{
						id: "models.routing-rules",
						title: t("nav.routingRules"),
						url: "/workspace/routing-rules",
						icon: Network,
						hasAccess: hasRoutingRulesAccess,
					},
					{
						id: "models.complexity-router",
						title: t("nav.complexityRouter"),
						url: "/workspace/complexity-router",
						icon: GitCompareArrows,
						hasAccess: hasRoutingRulesAccess,
					},
					{
						id: "models.circuit-breaker",
						title: t("nav.circuitBreaker"),
						url: "/workspace/circuit-breaker",
						enterpriseOnly: true,
						icon: CircuitBoard,
						hasAccess: hasCircuitBreakerAccess,
					},
					{
						id: "models.pricing-overrides",
						title: t("nav.pricingOverrides"),
						url: "/workspace/custom-pricing/overrides",
						icon: SlidersHorizontal,
						hasAccess: hasSettingsAccess,
					},
					{
						id: "models.settings",
						title: t("nav.modelSettings"),
						url: "/workspace/custom-pricing",
						icon: Settings,
						hasAccess: hasSettingsAccess,
					},
				],
			},
			{
				id: "mcp",
				title: t("nav.mcpGateway"),
				testId: "mcp-gateway", // keep the pre-rename E2E selector stable
				icon: MCPIcon,
				description: t("navDesc.mcpGateway"),
				url: "/workspace/mcp-gateway",
				hasAccess: hasMCPGatewayAccess || hasVirtualMCPsAccess,
				subItems: [
					{
						id: "mcp.catalog",
						title: t("nav.mcpCatalog"),
						url: "/workspace/mcp-registry",
						icon: LayoutGrid,
						hasAccess: hasMCPGatewayAccess,
					},
					{
						id: "mcp.library",
						title: t("nav.mcpLibrary"),
						url: "/workspace/mcp-registry/library",
						icon: Boxes,
						hasAccess: hasMCPGatewayAccess,
					},
					{
						id: "mcp.virtual-mcps",
						title: t("nav.virtualMcps"),
						url: "/workspace/virtual-mcps",
						icon: ToolCase,
						description: t("navDesc.virtualMcps"),
						hasAccess: hasVirtualMCPsAccess,
						testId: "tool-groups", // keep the pre-rename E2E selector stable
					},
					{
						id: "mcp.auth-sessions",
						title: t("nav.authSessions"),
						url: "/workspace/mcp-sessions",
						icon: KeyRound,
						hasAccess: hasMCPGatewayAccess,
					},
					{
						id: "mcp.oauth-grants",
						title: t("nav.oauthGrants"),
						url: "/workspace/oauth-grants",
						icon: ShieldCheck,
						hasAccess: hasMCPGatewayAccess,
					},
					{
						id: "mcp.settings",
						title: t("nav.mcpSettings"),
						url: "/workspace/mcp-settings",
						icon: Settings,
						hasAccess: hasMCPGatewayAccess,
					},
				],
			},
			{
				id: "agents",
				title: t("nav.agents"),
				url: "/workspace/agent-gateway",
				icon: Bot,
				description: t("navDesc.agents"),
				hasAccess: hasAgentGatewayAccess,
				new: true,
				subItems: [
					{
						id: "agents.catalog",
						title: t("nav.agentCatalog"),
						url: "/workspace/agent-gateway",
						icon: LayoutGrid,
						description: t("navDesc.agentCatalog"),
						hasAccess: hasAgentGatewayAccess,
					},
					{
						id: "agents.push-configurations",
						title: t("nav.pushConfigurations"),
						url: "/workspace/agent-gateway/push-configs",
						icon: Webhook,
						description: t("navDesc.pushConfigurations"),
						hasAccess: hasAgentGatewayAccess,
					},
					{
						id: "agents.settings",
						title: t("nav.agentSettings"),
						url: "/workspace/config/agent-gateway",
						icon: Settings,
						description: t("navDesc.agentSettings"),
						hasAccess: hasSettingsAccess,
					},
				],
			},
			{
				id: "relays",
				title: t("nav.relays"),
				url: "/workspace/relays",
				icon: Waypoints,
				description: t("navDesc.relays"),
				hasAccess: hasRelaysAccess,
			},
			{
				id: "plugins",
				title: t("nav.plugins"),
				url: "/workspace/plugins",
				icon: Puzzle,
				hasAccess: hasPluginsAccess,
			},
			{
				id: "alerting",
				title: t("nav.alerting"),
				url: "/workspace/alerting",
				enterpriseOnly: true,
				icon: Siren,
				hasAccess: hasAlertingAccess,
				subItems: [
					{
						id: "alerting.channels",
						title: t("nav.channels"),
						url: "/workspace/alerting/channels",
						icon: Megaphone,
						hasAccess: hasAlertingAccess,
					},
					{
						id: "alerting.rules",
						title: t("nav.rules"),
						url: "/workspace/alerting/rules",
						icon: Gavel,
						hasAccess: hasAlertingAccess,
					},
					{
						id: "alerting.history",
						title: t("nav.history"),
						url: "/workspace/alerting/history",
						icon: History,
						hasAccess: hasAlertingAccess,
					},
				],
			},
			{
				id: "governance",
				title: t("nav.governance"),
				url: "/workspace/governance",
				icon: Landmark,
				hasAccess: hasAnyGovernanceAccess,
				subItems: [
					{
						id: "governance.virtual-keys",
						title: t("nav.virtualKeys"),
						url: "/workspace/governance/virtual-keys",
						icon: KeyRound,
						hasAccess: hasVirtualKeysAccess,
					},
					{
						id: "governance.org-chart",
						title: t("nav.orgChart"),
						url: "/workspace/governance/org-chart",
						enterpriseOnly: true,
						icon: Network,
						description: t("navDesc.orgChart"),
						// The chart is read from business units down, so it follows the Business Units entry.
						hasAccess: hasBusinessUnitsAccess,
						new: true,
					},
					{
						id: "governance.users",
						title: t("nav.users"),
						url: "/workspace/governance/users",
						enterpriseOnly: true,
						icon: Users,
						hasAccess: hasUsersAccess,
					},
					{
						id: "governance.teams",
						title: t("nav.teams"),
						url: "/workspace/governance/teams",
						icon: Building,
						hasAccess: hasTeamsAccess,
					},
					{
						id: "governance.business-units",
						title: t("nav.businessUnits"),
						url: "/workspace/governance/business-units",
						enterpriseOnly: true,
						icon: Building2,
						hasAccess: hasBusinessUnitsAccess,
					},
					{
						id: "governance.customers",
						title: t("nav.customers"),
						url: "/workspace/governance/customers",
						icon: WalletCards,
						hasAccess: hasCustomersAccess,
					},
					{
						id: "governance.user-provisioning",
						title: t("nav.userProvisioning"),
						url: "/workspace/scim",
						enterpriseOnly: true,
						icon: BookUser,
						hasAccess: hasUserProvisioningAccess,
					},
					{
						id: "governance.rbac",
						title: t("nav.rolesPermissions"),
						url: "/workspace/governance/rbac",
						enterpriseOnly: true,
						icon: UserRoundCheck,
						hasAccess: hasRbacAccess,
					},
					{
						id: "governance.access-profiles",
						title: t("nav.accessProfiles"),
						url: "/workspace/governance/access-profiles",
						enterpriseOnly: true,
						icon: ShieldCheck,
						hasAccess: hasAccessProfilesAccess,
					},
					{
						id: "governance.projects",
						title: t("nav.projects"),
						url: "/workspace/governance/projects",
						enterpriseOnly: true,
						icon: SquareKanban,
						description: t("navDesc.projects"),
						hasAccess: hasProjectsAccess,
					},
					{
						id: "governance.audit-logs",
						title: t("nav.auditLogs"),
						url: "/workspace/audit-logs",
						enterpriseOnly: true,
						icon: ScrollText,
						hasAccess: hasAuditLogsAccess,
					},
				],
			},
			{
				id: "guardrails",
				title: t("nav.guardrails"),
				url: "/workspace/guardrails",
				enterpriseOnly: true,
				icon: Construction,
				hasAccess: hasGuardrailsConfigAccess || hasGuardrailsProvidersAccess,
				subItems: [
					{
						id: "guardrails.rules",
						title: t("nav.guardrailRules"),
						url: "/workspace/guardrails/configuration",
						icon: SearchCheck,
						hasAccess: hasGuardrailsConfigAccess,
					},
					{
						id: "guardrails.providers",
						title: t("nav.guardrailProviders"),
						url: "/workspace/guardrails/providers",
						icon: Boxes,
						hasAccess: hasGuardrailsProvidersAccess,
					},
					{
						id: "guardrails.incidents",
						title: t("nav.incidents"),
						url: "/workspace/guardrails/incidents",
						icon: TriangleAlert,
						description: t("navDesc.guardrailIncidents"),
						hasAccess: hasGuardrailsConfigAccess && hasLogsAccess,
					},
				],
			},
			{
				id: "webhooks",
				title: t("nav.webhooks"),
				url: "/workspace/webhooks",
				icon: Webhook,
				hasAccess: hasGovernanceLegacyAccess,
			},
			{
				id: "edge-control",
				title: t("nav.edgeControl"),
				icon: Hexagon,
				url: "/workspace/edge-control",
				enterpriseOnly: true,
				hasAccess: hasAnyEdgeControlAccess,
				subItems: [
					{
						id: "edge.devices",
						title: t("nav.devices"),
						url: "/workspace/edge-control/devices",
						icon: LaptopMinimalCheck,
						hasAccess: hasDevicesAccess,
					},
					{
						id: "edge.approvals",
						title: t("nav.approvals"),
						url: "/workspace/edge-control/inventory",
						icon: BadgeCheck,
						hasAccess: hasInventoryAccess,
					},
					{
						id: "edge.settings",
						title: t("nav.edgeSettings"),
						url: "/workspace/edge-control/config",
						icon: Settings,
						hasAccess: hasEdgeConfigAccess,
					},
				],
			},
			{
				id: "cluster",
				title: t("nav.cluster"),
				url: "/workspace/cluster",
				enterpriseOnly: true,
				icon: Network,
				description: t("navDesc.cluster"),
				hasAccess: hasClusterConfigAccess,
				subItems: [
					{
						id: "cluster.nodes",
						title: t("nav.clusterNodes"),
						url: "/workspace/cluster/overview",
						icon: Server,
						description: t("navDesc.clusterNodes"),
						hasAccess: hasClusterConfigAccess,
					},
					{
						id: "cluster.storage",
						title: t("nav.clusterStorage"),
						url: "/workspace/cluster/storage",
						icon: Database,
						description: t("navDesc.clusterStorage"),
						hasAccess: hasClusterConfigAccess,
					},
					{
						id: "cluster.incidents",
						title: t("nav.incidents"),
						url: "/workspace/cluster/incidents",
						icon: TriangleAlert,
						description: t("navDesc.clusterIncidents"),
						hasAccess: hasClusterConfigAccess,
					},
					{
						id: "cluster.settings",
						title: t("nav.settings"),
						url: "/workspace/cluster/settings",
						icon: Settings,
						description: t("navDesc.clusterSettings"),
						// The settings API sits under the Settings resource; saving also needs Settings update.
						hasAccess: hasClusterConfigAccess && hasSettingsAccess,
					},
				],
			},
			{
				id: "adaptive-routing",
				title: t("nav.adaptiveRouting"),
				url: "/workspace/adaptive-routing",
				enterpriseOnly: true,
				icon: Shuffle,
				hasAccess: isAdaptiveRoutingAllowed,
				subItems: [
					{
						id: "adaptive.dashboard",
						title: t("nav.adaptiveRoutingDashboard"),
						url: "/workspace/adaptive-routing",
						icon: ChartColumnBig,
						hasAccess: isAdaptiveRoutingAllowed,
					},
					{
						id: "adaptive.incidents",
						title: t("nav.incidents"),
						url: "/workspace/adaptive-routing/incidents",
						icon: TriangleAlert,
						description: t("navDesc.adaptiveRoutingIncidents"),
						hasAccess: isAdaptiveRoutingAllowed,
					},
					{
						id: "adaptive.settings",
						title: t("nav.adaptiveRoutingSettings"),
						url: "/workspace/adaptive-routing/settings",
						icon: Settings,
						hasAccess: isAdaptiveRoutingAllowed,
					},
				],
			},
			...(isDbConnected
				? [
						{
							id: "prompt-repository",
							title: t("nav.promptRepository"),
							url: "/workspace/prompt-repo",
							icon: FolderGit,
							description: t("navDesc.promptRepository"),
							hasAccess: hasPromptRepositoryAccess,
						},
						{
							id: "skills",
							title: t("nav.skills"),
							url: "/workspace/skills-repo",
							icon: BookOpenText,
							description: t("navDesc.skills"),
							hasAccess: hasSkillsRepositoryAccess || hasSettingsAccess,
							subItems: [
								{
									id: "skills.repository",
									title: t("nav.skillsRepository"),
									url: "/workspace/skills-repo",
									icon: BookOpenText,
									description: t("navDesc.skillsRepository"),
									hasAccess: hasSkillsRepositoryAccess,
								},
								{
									id: "skills.settings",
									title: t("nav.skillsSettings"),
									url: "/workspace/config/skills",
									icon: Settings,
									description: t("navDesc.skillsSettings"),
									hasAccess: hasSettingsAccess,
								},
							],
						},
					]
				: []),
			{
				id: "settings",
				title: t("nav.settings"),
				url: "/workspace/config",
				icon: Settings2Icon,
				description: t("navDesc.settings"),
				hasAccess: hasSettingsAccess || hasAuditLogsAccess || hasUserProvisioningAccess || (hasWarpAccess && isWarpEnabled),
				subItems: [
					{
						id: "settings.client",
						title: t("nav.clientSettings"),
						url: "/workspace/config/client-settings",
						icon: Settings,
						hasAccess: hasSettingsAccess,
					},
					{
						id: "settings.compatibility",
						title: t("nav.compatibility"),
						url: "/workspace/config/compatibility",
						icon: Plug,
						hasAccess: hasSettingsAccess,
					},
					{
						id: "settings.caching",
						title: t("nav.caching"),
						url: "/workspace/config/caching",
						icon: DatabaseZap,
						hasAccess: hasSettingsAccess,
					},
					{
						id: "settings.security",
						title: t("nav.security"),
						url: "/workspace/config/security",
						icon: ShieldCheck,
						hasAccess: hasSettingsAccess,
					},
					{
						id: "settings.warp",
						title: t("nav.warp"),
						url: "/workspace/config/warp",
						icon: WarpNavIcon,
						description: t("navDesc.warp"),
						hasAccess: hasWarpAccess && isWarpEnabled,
					},
					...(IS_ENTERPRISE
						? [
								{
									id: "settings.proxy",
									title: t("nav.proxy"),
									url: "/workspace/config/proxy",
									icon: Globe,
									description: t("navDesc.proxy"),
									hasAccess: hasSettingsAccess,
								},
							]
						: []),
					{
						id: "settings.api-keys",
						title: t("nav.apiKeys"),
						url: "/workspace/config/api-keys",
						enterpriseOnly: true,
						icon: KeyRound,
						hasAccess: hasAPIKeyAccess,
					},
					{
						id: "settings.performance",
						title: t("nav.performanceTuning"),
						url: "/workspace/config/performance-tuning",
						icon: TrendingUp,
						hasAccess: hasSettingsAccess,
					},
					{
						id: "settings.feature-flags",
						title: t("nav.featureFlags"),
						url: "/workspace/config/feature-flags",
						icon: Flag,
						hasAccess: hasFeatureFlagsAccess,
					},
					...(IS_ENTERPRISE
						? [
								{
									id: "settings.branding",
									title: t("nav.branding"),
									url: "/workspace/config/branding",
									icon: Palette,
									description: t("navDesc.branding"),
									hasAccess: hasSettingsAccess,
								},
								{
									id: "settings.license",
									title: t("nav.licenseInfo"),
									url: "/workspace/config/license",
									icon: BadgeInfo,
									description: t("navDesc.licenseInfo"),
									hasAccess: hasSettingsAccess,
								},
							]
						: []),
				],
			},
		],
		[
			t,
			i18n.language,
			hasLogsAccess,
			hasAPIKeyAccess,
			hasObservabilityAccess,
			hasAlertingAccess,
			hasDashboardAccess,
			hasModelProvidersAccess,
			hasMCPGatewayAccess,
			hasAgentGatewayAccess,
			hasAgentLogsAccess,
			hasVirtualMCPsAccess,
			hasMCPLogsAccess,
			hasRelaysAccess,
			hasRelayLogsAccess,
			hasPluginsAccess,
			hasUsersAccess,
			hasUserProvisioningAccess,
			hasAuditLogsAccess,
			hasCustomersAccess,
			hasTeamsAccess,
			hasBusinessUnitsAccess,
			hasRbacAccess,
			hasVirtualKeysAccess,
			hasGovernanceLegacyAccess,
			hasAnyGovernanceAccess,
			hasRoutingRulesAccess,
			hasGuardrailsProvidersAccess,
			hasGuardrailsConfigAccess,
			hasCircuitBreakerAccess,
			hasClusterConfigAccess,
			isAdaptiveRoutingAllowed,
			hasSettingsAccess,
			hasPromptRepositoryAccess,
			hasSkillsRepositoryAccess,
			hasAccessProfilesAccess,
			hasProjectsAccess,
			hasFeatureFlagsAccess,
			isWarpEnabled,
			hasWarpAccess,
			hasDevicesAccess,
			hasInventoryAccess,
			hasEdgeConfigAccess,
			hasAnyEdgeControlAccess,
			isDbConnected,
		],
	);

	return items;
}