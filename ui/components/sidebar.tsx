"use client";

import {
	ArrowUpRight,
	BookUser,
	Boxes,
	BoxIcon,
	BugIcon,
	ChartColumnBig,
	ChevronsLeftRightEllipsis,
	CircleDollarSign,
	Cog,
	Construction,
	FlaskConical,
	FolderGit,
	Gauge,
	Globe,
	KeyRound,
	Landmark,
	Layers,
	LayoutGrid,
	LogOut,
	Logs,
	Network,
	PanelLeftClose,
	Puzzle,
	ScrollText,
	Settings,
	Settings2Icon,
	Shield,
	ShieldUser,
	Shuffle,
	Telescope,
	ToolCase,
	User,
	UserRoundCheck,
	Users,
	Zap,
} from "lucide-react";

import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Separator } from "@/components/ui/separator";
import {
	Sidebar,
	SidebarContent,
	SidebarGroup,
	SidebarGroupContent,
	SidebarHeader,
	SidebarMenu,
	SidebarMenuButton,
	SidebarMenuItem,
	SidebarMenuSub,
	SidebarMenuSubButton,
	SidebarMenuSubItem,
	useSidebar,
} from "@/components/ui/sidebar";
import { useWebSocket } from "@/hooks/useWebSocket";
import { IS_ENTERPRISE, TRIAL_EXPIRY } from "@/lib/constants/config";
import { useGetCoreConfigQuery, useGetLatestReleaseQuery, useGetVersionQuery, useLogoutMutation } from "@/lib/store";
import { cn } from "@/lib/utils";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import type { UserInfo } from "@enterprise/lib/store/utils/tokenManager";
import { getUserInfo } from "@enterprise/lib/store/utils/tokenManager";
import { BooksIcon, DiscordLogoIcon, GithubLogoIcon } from "@phosphor-icons/react";
import { ChevronRight } from "lucide-react";
import moment from "moment";
import { useTheme } from "next-themes";
import Image from "next/image";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useCookies } from "react-cookie";
import { ThemeToggle } from "./themeToggle";
import { Badge } from "./ui/badge";
import { PromoCardStack } from "./ui/promoCardStack";

// Cookie name for dismissing production setup card
const PRODUCTION_SETUP_DISMISSED_COOKIE = "bifrost_production_setup_dismissed";

// Custom MCP Icon Component
const MCPIcon = ({ className }: { className?: string }) => (
	<svg
		className={className}
		fill="currentColor"
		fillRule="evenodd"
		height="1em"
		style={{ flex: "none", lineHeight: 1 }}
		viewBox="0 0 24 24"
		width="1em"
		xmlns="http://www.w3.org/2000/svg"
		aria-label="MCP clients icon"
	>
		<title>MCP clients icon</title>
		<path d="M15.688 2.343a2.588 2.588 0 00-3.61 0l-9.626 9.44a.863.863 0 01-1.203 0 .823.823 0 010-1.18l9.626-9.44a4.313 4.313 0 016.016 0 4.116 4.116 0 011.204 3.54 4.3 4.3 0 013.609 1.18l.05.05a4.115 4.115 0 010 5.9l-8.706 8.537a.274.274 0 000 .393l1.788 1.754a.823.823 0 010 1.18.863.863 0 01-1.203 0l-1.788-1.753a1.92 1.92 0 010-2.754l8.706-8.538a2.47 2.47 0 000-3.54l-.05-.049a2.588 2.588 0 00-3.607-.003l-7.172 7.034-.002.002-.098.097a.863.863 0 01-1.204 0 .823.823 0 010-1.18l7.273-7.133a2.47 2.47 0 00-.003-3.537z" />
		<path d="M14.485 4.703a.823.823 0 000-1.18.863.863 0 00-1.204 0l-7.119 6.982a4.115 4.115 0 000 5.9 4.314 4.314 0 006.016 0l7.12-6.982a.823.823 0 000-1.18.863.863 0 00-1.204 0l-7.119 6.982a2.588 2.588 0 01-3.61 0 2.47 2.47 0 010-3.54l7.12-6.982z" />
	</svg>
);

// Main navigation items

// External links
const externalLinks = [
	{
		title: "Discord Server",
		url: "https://getmax.im/bifrost-discord",
		icon: DiscordLogoIcon,
	},
	{
		title: "GitHub Repository",
		url: "https://github.com/maximhq/bifrost",
		icon: GithubLogoIcon,
	},
	{
		title: "Report a bug",
		url: "https://github.com/maximhq/bifrost/issues/new?title=[Bug Report]&labels=bug&type=bug&projects=maximhq/1",
		icon: BugIcon,
		strokeWidth: 1.5,
	},
	{
		title: "Full Documentation",
		url: "https://docs.getbifrost.ai",
		icon: BooksIcon,
		strokeWidth: 1,
	},
];

// Base promotional card (memoized outside component to prevent recreation)
const productionSetupHelpCard = {
	id: "production-setup",
	title: "Need help with production setup?",
	description: (
		<>
			We offer help with production setup including custom integrations and dedicated support.
			<br />
			<br />
			Book a demo with our team{" "}
			<Link
				href="https://calendly.com/maximai/bifrost-demo?utm_source=bfd_sdbr"
				target="_blank"
				className="text-primary font-medium underline"
				rel="noopener noreferrer"
			>
				here
			</Link>
			.
		</>
	),
	dismissible: true,
};

// Sidebar item interface
interface SidebarItem {
	title: string;
	url: string;
	icon: React.ComponentType<{ className?: string }>;
	description: string;
	isAllowed?: boolean;
	hasAccess: boolean;
	subItems?: SidebarItem[];
	tag?: string;
	queryParam?: string; // Optional: for tab-based subitems (e.g., "client-settings")
}

const SidebarItemView = ({
	item,
	isActive,
	isAllowed,
	isExternal,
	isWebSocketConnected,
	isExpanded,
	onToggle,
	pathname,
	router,
	isSidebarCollapsed,
	expandSidebar,
}: {
	item: SidebarItem;
	isActive: boolean;
	isAllowed: boolean;
	isExternal?: boolean;
	isWebSocketConnected: boolean;
	isExpanded?: boolean;
	onToggle?: () => void;
	pathname: string;
	router: ReturnType<typeof useRouter>;
	isSidebarCollapsed: boolean;
	expandSidebar: () => void;
}) => {
	const hasSubItems = "subItems" in item && item.subItems && item.subItems.length > 0;
	const isAnySubItemActive =
		hasSubItems &&
		item.subItems?.some((subItem) => {
			return pathname.startsWith(subItem.url);
		});

	const handleClick = (e: React.MouseEvent) => {
		if (hasSubItems && item.hasAccess) {
			e.preventDefault();
			// If sidebar is collapsed, expand it first then toggle the submenu
			if (isSidebarCollapsed) {
				expandSidebar();
				// Small delay to allow sidebar to expand before toggling submenu
				setTimeout(() => {
					if (onToggle) onToggle();
				}, 100);
			} else if (onToggle) {
				onToggle();
			}
		}
	};

	const handleNavigation = (url: string) => {
		if (!isAllowed) return;
		if (isExternal) {
			window.open(url, "_blank");
			return;
		}
		router.push(url);
	};

	const handleSubItemClick = (subItem: SidebarItem) => {
		if (subItem.queryParam) {
			router.push(`${subItem.url}?tab=${subItem.queryParam}`);
		} else {
			router.push(subItem.url);
		}
	};

	return (
		<SidebarMenuItem key={item.title}>
			<SidebarMenuButton
				tooltip={item.title}
				className={`relative h-7.5 cursor-pointer rounded-sm border px-3 transition-all duration-200 ${isActive || isAnySubItemActive
					? "bg-sidebar-accent text-primary border-primary/20"
					: isAllowed && item.hasAccess
						? "hover:bg-sidebar-accent hover:text-accent-foreground border-transparent text-slate-500 dark:text-zinc-400"
						: "hover:bg-destructive/5 hover:text-muted-foreground text-muted-foreground cursor-not-allowed border-transparent"
					} `}
				onClick={hasSubItems ? handleClick : (item.hasAccess ? () => handleNavigation(item.url) : undefined)}
			>
				<div className="flex w-full items-center justify-between">
					<div className="flex w-full items-center gap-2">
						<item.icon className={`h-4 w-4 shrink-0 ${isActive || isAnySubItemActive ? "text-primary" : "text-muted-foreground"}`} />
						<span
							className={`text-sm group-data-[collapsible=icon]:hidden ${isActive || isAnySubItemActive ? "font-medium" : "font-normal"}`}
						>
							{item.title}
						</span>
						{item.tag && (
							<Badge variant="secondary" className="text-muted-foreground ml-auto text-xs group-data-[collapsible=icon]:hidden">
								{item.tag}
							</Badge>
						)}
					</div>
					{hasSubItems && (
						<ChevronRight
							className={`h-4 w-4 transition-transform duration-200 group-data-[collapsible=icon]:hidden ${isExpanded ? "rotate-90" : ""}`}
						/>
					)}
					{!hasSubItems && item.url === "/logs" && isWebSocketConnected && (
						<div className="h-2 w-2 animate-pulse rounded-full bg-green-800 dark:bg-green-200" />
					)}
					{isExternal && <ArrowUpRight className="text-muted-foreground h-4 w-4 group-data-[collapsible=icon]:hidden" size={16} />}
				</div>
			</SidebarMenuButton>
			{hasSubItems && isExpanded && (
				<SidebarMenuSub className="border-sidebar-border mt-1 ml-4 space-y-0.5 border-l pl-2">
					{item.subItems?.map((subItem: SidebarItem) => {
						// For query param based subitems, check if tab matches
						const isSubItemActive = subItem.queryParam ? pathname === subItem.url : pathname.startsWith(subItem.url);
						const SubItemIcon = subItem.icon;
						return (
							<SidebarMenuSubItem key={subItem.title}>
								<SidebarMenuSubButton
									className={`h-7 cursor-pointer rounded-sm px-2 transition-all duration-200 ${isSubItemActive
										? "bg-sidebar-accent text-primary font-medium"
										: subItem.hasAccess === false
											? "hover:bg-destructive/5 hover:text-muted-foreground text-muted-foreground cursor-not-allowed border-transparent"
											: "hover:bg-sidebar-accent hover:text-accent-foreground text-slate-500 dark:text-zinc-400"
										}`}
									onClick={() => (subItem.hasAccess === false ? undefined : handleSubItemClick(subItem))}
								>
									<div className="flex items-center gap-2">
										{SubItemIcon && <SubItemIcon className={`h-3.5 w-3.5 ${isSubItemActive ? "text-primary" : "text-muted-foreground"}`} />}
										<span className={`text-sm ${isSubItemActive ? "font-medium" : "font-normal"}`}>{subItem.title}</span>
									</div>
								</SidebarMenuSubButton>
							</SidebarMenuSubItem>
						);
					})}
				</SidebarMenuSub>
			)}
		</SidebarMenuItem>
	);
};

// Helper function to compare semantic versions
const compareVersions = (v1: string, v2: string): number => {
	// Remove 'v' prefix if present
	const cleanV1 = v1.startsWith("v") ? v1.slice(1) : v1;
	const cleanV2 = v2.startsWith("v") ? v2.slice(1) : v2;

	// Split into main version and prerelease
	const [mainV1, prereleaseV1] = cleanV1.split("-");
	const [mainV2, prereleaseV2] = cleanV2.split("-");

	// Compare main version numbers (major.minor.patch)
	const partsV1 = mainV1.split(".").map(Number);
	const partsV2 = mainV2.split(".").map(Number);

	for (let i = 0; i < Math.max(partsV1.length, partsV2.length); i++) {
		const num1 = partsV1[i] || 0;
		const num2 = partsV2[i] || 0;

		if (num1 > num2) return 1;
		if (num1 < num2) return -1;
	}

	// If main versions are equal, check prerelease
	// Version without prerelease is higher than version with prerelease
	if (!prereleaseV1 && prereleaseV2) return 1;
	if (prereleaseV1 && !prereleaseV2) return -1;

	// Both have prereleases, compare them
	if (prereleaseV1 && prereleaseV2) {
		// Extract prerelease number (e.g., "prerelease1" -> 1)
		const prereleaseNum1 = parseInt(prereleaseV1.replace(/\D/g, "")) || 0;
		const prereleaseNum2 = parseInt(prereleaseV2.replace(/\D/g, "")) || 0;

		if (prereleaseNum1 > prereleaseNum2) return 1;
		if (prereleaseNum1 < prereleaseNum2) return -1;
	}

	return 0;
};

export default function AppSidebar() {
	const pathname = usePathname();
	const router = useRouter();
	const [mounted, setMounted] = useState(false);
	const [expandedItems, setExpandedItems] = useState<Set<string>>(new Set());
	const [areCardsEmpty, setAreCardsEmpty] = useState(false);
	const [userPopoverOpen, setUserPopoverOpen] = useState(false);
	const [cookies, setCookie] = useCookies([PRODUCTION_SETUP_DISMISSED_COOKIE]);
	const isProductionSetupDismissed = !!cookies[PRODUCTION_SETUP_DISMISSED_COOKIE];
	const { data: latestRelease } = useGetLatestReleaseQuery(undefined, {
		skip: !mounted, // Only fetch after component is mounted
	});
	const hasLogsAccess = useRbac(RbacResource.Logs, RbacOperation.View);
	const hasObservabilityAccess = useRbac(RbacResource.Observability, RbacOperation.View);
	const hasModelProvidersAccess = useRbac(RbacResource.ModelProvider, RbacOperation.View);
	const hasMCPGatewayAccess = useRbac(RbacResource.MCPGateway, RbacOperation.View);
	const hasPluginsAccess = useRbac(RbacResource.Plugins, RbacOperation.View);
	const hasUserProvisioningAccess = useRbac(RbacResource.UserProvisioning, RbacOperation.View);
	const hasAuditLogsAccess = useRbac(RbacResource.AuditLogs, RbacOperation.View);
	const hasCustomersAccess = useRbac(RbacResource.Customers, RbacOperation.View);
	const hasTeamsAccess = useRbac(RbacResource.Teams, RbacOperation.View);
	const hasRbacAccess = useRbac(RbacResource.RBAC, RbacOperation.View);
	const hasVirtualKeysAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.View);
	const hasGovernanceAccess = useRbac(RbacResource.Governance, RbacOperation.View);
	const hasRoutingRulesAccess = useRbac(RbacResource.RoutingRules, RbacOperation.View);
	const hasGuardrailsProvidersAccess = useRbac(RbacResource.GuardrailsProviders, RbacOperation.View);
	const hasGuardrailsConfigAccess = useRbac(RbacResource.GuardrailsConfig, RbacOperation.View);
	const hasClusterConfigAccess = useRbac(RbacResource.Cluster, RbacOperation.View);
	const isAdaptiveRoutingAllowed = useRbac(RbacResource.AdaptiveRouter, RbacOperation.View);
	const hasSettingsAccess = useRbac(RbacResource.Settings, RbacOperation.View);

	const items = [
		{
			title: "Observability",
			url: "/workspace/logs",
			icon: Telescope,
			description: "Request logs & monitoring",
			hasAccess: hasLogsAccess,
			subItems: [
				{
					title: "Dashboard",
					url: "/workspace/dashboard",
					icon: ChartColumnBig,
					description: "Dashboard",
					hasAccess: hasObservabilityAccess,
				},
				{
					title: "LLM Logs",
					url: "/workspace/logs",
					icon: Logs,
					description: "LLM request logs & monitoring",
					hasAccess: hasLogsAccess,
				},
				{
					title: "MCP Logs",
					url: "/workspace/mcp-logs",
					icon: MCPIcon,
					description: "MCP tool execution logs",
					hasAccess: hasLogsAccess,
				},
				{
					title: "Connectors",
					url: "/workspace/observability",
					icon: ChevronsLeftRightEllipsis,
					description: "Log connectors",
					hasAccess: hasObservabilityAccess,
				},
			],
		},
		{
			title: "Prompt Repository",
			url: "/workspace/prompt-repo",
			icon: FolderGit,
			description: "Prompt repository",
			hasAccess: true,
		},
		{
			title: "Model Providers",
			url: "/workspace/providers",
			icon: BoxIcon,
			description: "Configure models",
			hasAccess: hasModelProvidersAccess || hasRoutingRulesAccess || hasGovernanceAccess,
			subItems: [
				{
					title: "Configurations",
					url: "/workspace/providers",
					icon: Cog,
					description: "Configure models",
					hasAccess: hasModelProvidersAccess,
				},
				{
					title: "Budgets & Limits",
					url: "/workspace/model-limits",
					icon: Gauge,
					description: "Model limits",
					hasAccess: hasGovernanceAccess,
				},
				{
					title: "Routing Rules",
					url: "/workspace/routing-rules",
					icon: Network,
					description: "Intelligent routing rules",
					hasAccess: hasRoutingRulesAccess,
				},
			],
		},
		{
			title: "MCP Gateway",
			icon: MCPIcon,
			description: "MCP configuration",
			url: "/workspace/mcp-gateway",
			hasAccess: hasMCPGatewayAccess,
			subItems: [
				{
					title: "MCP Catalog",
					url: "/workspace/mcp-registry",
					icon: LayoutGrid,
					description: "MCP tool catalog",
					hasAccess: hasMCPGatewayAccess,
				},
				{
					title: "Tool groups",
					url: "/workspace/mcp-tool-groups",
					icon: ToolCase,
					description: "MCP tool groups",
					hasAccess: hasMCPGatewayAccess,
				},
				{
					title: "Auth Config",
					url: "/workspace/mcp-auth-config",
					icon: ShieldUser,
					description: "MCP auth config",
					hasAccess: hasMCPGatewayAccess,
				},
			],
		},
		{
			title: "Plugins",
			url: "/workspace/plugins",
			icon: Puzzle,
			tag: "BETA",
			description: "Manage custom plugins",
			hasAccess: hasPluginsAccess,
		},
		{
			title: "Governance",
			url: "/workspace/governance",
			icon: Landmark,
			description: "Govern access",
			hasAccess:
				hasGovernanceAccess ||
				hasVirtualKeysAccess ||
				hasCustomersAccess ||
				hasTeamsAccess ||
				hasUserProvisioningAccess ||
				hasRbacAccess ||
				hasAuditLogsAccess,
			subItems: [
				{
					title: "Virtual Keys",
					url: "/workspace/virtual-keys",
					icon: KeyRound,
					description: "Manage virtual keys & access",
					hasAccess: hasVirtualKeysAccess,
				},
				{
					title: "Users & Groups",
					url: "/workspace/user-groups",
					icon: Users,
					description: "Manage users & groups",
					hasAccess: hasCustomersAccess || hasTeamsAccess,
				},
				{
					title: "User Provisioning",
					url: "/workspace/scim",
					icon: BookUser,
					description: "User management and provisioning",
					hasAccess: hasUserProvisioningAccess,
				},
				{
					title: "Roles & Permissions",
					url: "/workspace/rbac",
					icon: UserRoundCheck,
					description: "User roles and permissions",
					hasAccess: hasRbacAccess,
				},
				{
					title: "Audit Logs",
					url: "/workspace/audit-logs",
					icon: ScrollText,
					description: "Audit logs and compliance",
					hasAccess: hasAuditLogsAccess,
				},
			],
		},
		{
			title: "Guardrails",
			url: "/workspace/guardrails",
			icon: Construction,
			description: "Guardrails configuration",
			hasAccess: hasGuardrailsConfigAccess || hasGuardrailsProvidersAccess,
			subItems: [
				{
					title: "Configuration",
					url: "/workspace/guardrails/configuration",
					icon: Cog,
					description: "Guardrail configuration",
					hasAccess: hasGuardrailsConfigAccess,
				},
				{
					title: "Providers",
					url: "/workspace/guardrails/providers",
					icon: Boxes,
					description: "Guardrail providers configuration",
					hasAccess: hasGuardrailsProvidersAccess,
				},
			],
		},
		{
			title: "Evals",
			url: "https://www.getmaxim.ai",
			icon: FlaskConical,
			isExternal: true,
			description: "Evaluations",
			hasAccess: true,
		},
		{
			title: "Cluster Config",
			url: "/workspace/cluster",
			icon: Layers,
			description: "Manage Bifrost cluster",
			hasAccess: hasClusterConfigAccess,
		},
		{
			title: "Adaptive Routing",
			url: "/workspace/adaptive-routing",
			icon: Shuffle,
			description: "Manage adaptive load balancer",
			hasAccess: isAdaptiveRoutingAllowed,
		},
		{
			title: "Config",
			url: "/workspace/config",
			icon: Settings2Icon,
			description: "Bifrost settings",
			hasAccess: hasSettingsAccess,
			subItems: [
				{
					title: "Client Settings",
					url: "/workspace/config/client-settings",
					icon: Settings,
					description: "Client configuration settings",
					hasAccess: hasSettingsAccess,
				},
				{
					title: "MCP Gateway",
					url: "/workspace/config/mcp-gateway",
					icon: MCPIcon,
					description: "MCP gateway configuration",
					hasAccess: hasMCPGatewayAccess,
				},
				{
					title: "Pricing Config",
					url: "/workspace/config/pricing-config",
					icon: CircleDollarSign,
					description: "Pricing configuration",
					hasAccess: hasSettingsAccess,
				},
				{
					title: "Logging",
					url: "/workspace/config/logging",
					icon: Logs,
					description: "Logging configuration",
					hasAccess: hasSettingsAccess,
				},
				{
					title: "Governance",
					url: "/workspace/config/governance",
					icon: Landmark,
					description: "Governance settings",
					hasAccess: hasSettingsAccess,
				},
				{
					title: "Caching",
					url: "/workspace/config/caching",
					icon: Zap,
					description: "Caching configuration",
					hasAccess: hasSettingsAccess,
				},
				{
					title: "Observability",
					url: "/workspace/config/observability",
					icon: Gauge,
					description: "Observability settings",
					hasAccess: hasSettingsAccess,
				},
				{
					title: "Security",
					url: "/workspace/config/security",
					icon: Shield,
					description: "Security settings",
					hasAccess: hasSettingsAccess,
				},
				...(IS_ENTERPRISE
					? [
						{
							title: "Proxy",
							url: "/workspace/config/proxy",
							icon: Globe,
							description: "Proxy configuration",
							hasAccess: hasSettingsAccess,
						},
					]
					: []),
				{
					title: "API Keys",
					url: "/workspace/config/api-keys",
					icon: KeyRound,
					description: "API keys management",
					hasAccess: hasSettingsAccess,
				},
				{
					title: "Performance Tuning",
					url: "/workspace/config/performance-tuning",
					icon: Zap,
					description: "Performance tuning settings",
					hasAccess: hasSettingsAccess,
				},
			],
		},
	];
	const { data: version } = useGetVersionQuery();
	const { resolvedTheme } = useTheme();
	const [logout] = useLogoutMutation();

	// Get user info from localStorage (for enterprise SCIM OAuth)
	const [userInfo, setUserInfo] = useState<UserInfo | null>(null);

	useEffect(() => {
		if (IS_ENTERPRISE) {
			const info = getUserInfo();
			setUserInfo(info);
		}
	}, []);

	const showNewReleaseBanner = useMemo(() => {
		if (IS_ENTERPRISE) return false;
		if (latestRelease && version) {
			return compareVersions(latestRelease.name, version) > 0;
		}
		return false;
	}, [latestRelease, version]);
	// Get governance config from RTK Query
	const { data: coreConfig } = useGetCoreConfigQuery({});
	const isGovernanceEnabled = coreConfig?.client_config.enable_governance || false;
	const isAuthEnabled = coreConfig?.auth_config?.is_enabled || false;

	useEffect(() => {
		setMounted(true);
	}, []);

	// Auto-expand items when their subitems are active
	useEffect(() => {
		const newExpandedItems = new Set<string>();
		items.forEach((item) => {
			if (item.subItems?.some((subItem) => pathname.startsWith(subItem.url))) {
				newExpandedItems.add(item.title);
			}
		});
		if (newExpandedItems.size > 0) {
			setExpandedItems((prev) => new Set([...prev, ...newExpandedItems]));
		}
	}, [pathname]);

	const toggleItem = (title: string) => {
		setExpandedItems((prev) => {
			const next = new Set(prev);
			if (next.has(title)) {
				next.delete(title);
			} else {
				next.add(title);
			}
			return next;
		});
	};

	const isActiveRoute = (url: string) => {
		if (url === "/" && pathname === "/") return true;
		if (url !== "/" && pathname.startsWith(url)) return true;
		return false;
	};

	// Always render the light theme version for SSR to avoid hydration mismatch
	const logoSrc = mounted && resolvedTheme === "dark" ? "/bifrost-logo-dark.png" : "/bifrost-logo.png";
	const iconSrc = mounted && resolvedTheme === "dark" ? "/bifrost-icon-dark.png" : "/bifrost-icon.png";

	const { isConnected: isWebSocketConnected } = useWebSocket();

	// New release image - based on theme
	const newReleaseImage = mounted && resolvedTheme === "dark" ? "/images/new-release-image-dark.png" : "/images/new-release-image.png";

	// Memoize promo cards array to prevent duplicates and unnecessary re-renders
	const promoCards = useMemo(() => {
		const cards = [];
		// Restart required card - non-dismissible, shown first
		if (coreConfig?.restart_required?.required) {
			cards.push({
				id: "restart-required",
				title: "Restart Required",
				description: (
					<div className="text-xs text-amber-700 dark:text-amber-300/80">
						{coreConfig.restart_required.reason || "Configuration changes require a server restart to take effect."}
					</div>
				),
				dismissible: false,
				variant: "warning" as const,
			});
		}
		if (showNewReleaseBanner && latestRelease) {
			cards.push({
				id: "new-release",
				title: `${latestRelease.name} is now available.`,
				description: (
					<div className="flex h-full flex-col gap-2">
						<img src={newReleaseImage} alt="Bifrost" className="h-[95px] rounded-md object-cover" />
						<Link
							href={`https://docs.getbifrost.ai/changelogs/${latestRelease.name}`}
							target="_blank"
							className="text-primary mt-auto pb-1 font-medium underline"
						>
							View release notes
						</Link>
					</div>
				),
				dismissible: true,
			});
		}
		// Only show after mounted to ensure cookie is properly hydrated and avoid flash
		if (!IS_ENTERPRISE && mounted && !isProductionSetupDismissed) {
			cards.push(productionSetupHelpCard);
		}
		return cards;
	}, [coreConfig?.restart_required, showNewReleaseBanner, latestRelease, newReleaseImage, isProductionSetupDismissed, mounted]);

	// Reset areCardsEmpty when promoCards changes
	useEffect(() => {
		if (promoCards.length > 0) {
			setAreCardsEmpty(false);
		}
	}, [promoCards]);

	const hasPromoCards = promoCards.length > 0 && !areCardsEmpty;
	// When cards are present: 13rem (header 3rem + bottom section ~10rem)
	// When no cards: 8rem (header 3rem + bottom section without cards ~5rem)
	const sidebarGroupHeight = hasPromoCards ? "h-[calc(100vh-13rem)]" : "h-[calc(100vh-8rem)]";

	const handleCardsEmpty = () => {
		setAreCardsEmpty(true);
	};

	const handlePromoDismiss = useCallback(
		(cardId: string) => {
			if (cardId === "production-setup") {
				const expiryDate = new Date();
				expiryDate.setDate(expiryDate.getDate() + 7);
				setCookie(PRODUCTION_SETUP_DISMISSED_COOKIE, "true", {
					path: "/",
					expires: expiryDate,
				});
			}
		},
		[setCookie],
	);

	const handleLogout = async () => {
		try {
			setUserPopoverOpen(false);
			await logout().unwrap();
			router.push("/login");
		} catch (error) {
			// Even if logout fails on server, redirect to login
			router.push("/login");
		}
	};

	const trialDaysRemaining = useMemo(() => {
		if (IS_ENTERPRISE && TRIAL_EXPIRY) {
			const daysRemaining = moment(TRIAL_EXPIRY).diff(moment(), "days");
			return daysRemaining > 0 ? daysRemaining : 0;
		}
		return null;
	}, []);

	const { state: sidebarState, toggleSidebar } = useSidebar();

	return (
		<Sidebar collapsible="icon" className="overflow-y-clip border-none bg-transparent">
			<SidebarHeader className="mt-1 ml-2 flex justify-between px-0 group-data-[collapsible=icon]:ml-0 group-data-[collapsible=icon]:h-auto">
				{/* Expanded state: horizontal layout */}
				<div className="flex h-10 w-full items-center justify-between px-1.5 group-data-[collapsible=icon]:hidden">
					<Link href="/workspace/logs" className="group flex items-center gap-2 pl-2">
						<Image className="h-[22px] w-auto" src={logoSrc} alt="Bifrost" width={70} height={70} />
					</Link>
					<button
						onClick={toggleSidebar}
						className="text-muted-foreground hover:text-foreground hover:bg-sidebar-accent flex h-7 w-7 items-center justify-center rounded-md transition-colors"
						aria-label="Collapse sidebar"
					>
						<PanelLeftClose className="h-4 w-4" />
					</button>
				</div>
				{/* Collapsed state: vertical layout */}
				<div
					className="hidden w-full cursor-pointer flex-col items-center gap-2 py-2 group-data-[collapsible=icon]:flex"
					onClick={toggleSidebar}
				>
					<Image className="h-[22px] w-auto" src={iconSrc} alt="Bifrost" width={22} height={22} />
				</div>
			</SidebarHeader>
			<SidebarContent className="overflow-hidden pb-4">
				<SidebarGroup className={`custom-scrollbar ${sidebarGroupHeight} overflow-scroll`}>
					<SidebarGroupContent>
						<SidebarMenu className="space-y-0.5">
							{items.map((item) => {
								const isActive = isActiveRoute(item.url);
								const isAllowed = item.title === "Governance" ? isGovernanceEnabled : true;
								return (
									<SidebarItemView
										key={item.title}
										item={item}
										isActive={isActive}
										isAllowed={isAllowed}
										isExternal={item.isExternal ?? false}
										isWebSocketConnected={isWebSocketConnected}
										isExpanded={expandedItems.has(item.title)}
										onToggle={() => toggleItem(item.title)}
										pathname={pathname}
										router={router}
										isSidebarCollapsed={sidebarState === "collapsed"}
										expandSidebar={() => toggleSidebar()}
									/>
								);
							})}
						</SidebarMenu>
					</SidebarGroupContent>
				</SidebarGroup>
				<div className="flex flex-col gap-4 px-3 group-data-[collapsible=icon]:px-1">
					<div className="mx-1 group-data-[collapsible=icon]:hidden">
						<PromoCardStack cards={promoCards} onCardsEmpty={handleCardsEmpty} onDismiss={handlePromoDismiss} />
					</div>
					<div className="flex flex-row">
						<div className="mx-auto flex flex-row gap-4 group-data-[collapsible=icon]:flex-col group-data-[collapsible=icon]:gap-2">
							{externalLinks.map((item, index) => (
								<a
									key={index}
									href={item.url}
									target="_blank"
									rel="noopener noreferrer"
									className="group flex w-full items-center justify-between"
									title={item.title}
								>
									<div className="flex items-center space-x-3">
										<item.icon
											className="hover:text-primary text-muted-foreground h-5 w-5"
											size={22}
											weight="regular"
											strokeWidth={item.strokeWidth}
										/>
									</div>
								</a>
							))}
							<ThemeToggle />
							{IS_ENTERPRISE && userInfo && (userInfo.name || userInfo.email) ? (
								<Popover open={userPopoverOpen} onOpenChange={setUserPopoverOpen}>
									<PopoverTrigger asChild>
										<button
											className="hover:text-primary text-muted-foreground flex cursor-pointer items-center space-x-3 p-0.5"
											type="button"
											aria-label="User menu"
										>
											<User className="hover:text-primary text-muted-foreground h-4 w-4" size={20} strokeWidth={2} />
										</button>
									</PopoverTrigger>
									<PopoverContent side="top" align="start" className="w-56 p-0">
										<div className="flex flex-col">
											<div className="px-4 py-3">
												<p className="text-sm font-medium">{userInfo.name || userInfo.email || "User"}</p>
											</div>
											<Separator />
											<button
												onClick={handleLogout}
												className="hover:bg-accent hover:text-accent-foreground flex w-full items-center gap-2 px-4 py-2.5 text-left text-sm transition-colors"
												type="button"
											>
												<LogOut className="h-4 w-4" strokeWidth={2} />
												<span>Logout</span>
											</button>
										</div>
									</PopoverContent>
								</Popover>
							) : isAuthEnabled && !IS_ENTERPRISE ? (
								<div>
									<button
										className="hover:text-primary text-muted-foreground flex cursor-pointer items-center space-x-3 p-0.5"
										onClick={handleLogout}
										type="button"
										aria-label="Logout"
									>
										<LogOut className="hover:text-primary text-muted-foreground h-4 w-4" size={20} strokeWidth={2} />
									</button>
								</div>
							) : null}
						</div>
					</div>
					<div className="mx-auto flex flex-col items-center gap-1 group-data-[collapsible=icon]:hidden">
						<div className="font-mono text-xs">{version ?? ""}</div>
						{trialDaysRemaining !== null && (
							<div className={cn("text-xs", trialDaysRemaining < 3 ? "text-red-500" : "text-muted-foreground")}>
								{trialDaysRemaining} {trialDaysRemaining === 1 ? "day" : "days"} remaining
							</div>
						)}
					</div>
				</div>
			</SidebarContent>
		</Sidebar>
	);
}
