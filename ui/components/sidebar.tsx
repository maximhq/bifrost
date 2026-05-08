import {
	ArrowUpRight,
	BookUser,
	Boxes,
	BoxIcon,
	BugIcon,
	Building,
	Building2,
	ChartColumnBig,
	ChevronsLeftRightEllipsis,
	Construction,
	DatabaseZap,
	FlaskConical,
	FolderGit,
	Globe,
	KeyRound,
	Landmark,
	LayoutGrid,
	LogOut,
	Logs,
	Network,
	PanelLeftOpen,
	PanelLeftClose,
	Plug,
	Puzzle,
	ScrollText,
	Search,
	SearchCheck,
	Settings,
	Settings2Icon,
	ShieldCheck,
	Shuffle,
	SlidersHorizontal,
	Telescope,
	ToolCase,
	TrendingUp,
	User,
	UserRoundCheck,
	Users,
	Wallet,
	WalletCards,
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
import { differenceInDays } from "date-fns";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import type { UserInfo } from "@enterprise/lib/store/utils/tokenManager";
import { getUserInfo } from "@enterprise/lib/store/utils/tokenManager";
import { BooksIcon, DiscordLogoIcon, GithubLogoIcon } from "@phosphor-icons/react";
import { Link, useLocation, useNavigate } from "@tanstack/react-router";
import { ChevronRight } from "lucide-react";
import { useTheme } from "next-themes";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useCookies } from "react-cookie";
import { useTranslation } from "react-i18next";
import { LanguageSwitcher } from "./languageSwitcher";
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
		titleKey: "sidebar.external.discord",
		url: "https://discord.gg/exN5KAydbU",
		icon: DiscordLogoIcon,
	},
	{
		titleKey: "sidebar.external.github",
		url: "https://github.com/maximhq/bifrost",
		icon: GithubLogoIcon,
	},
	{
		titleKey: "sidebar.external.reportBug",
		url: "https://github.com/maximhq/bifrost/issues/new?title=[Bug Report]&labels=bug&type=bug&projects=maximhq/1",
		icon: BugIcon,
		strokeWidth: 1.5,
	},
	{
		titleKey: "sidebar.external.fullDocs",
		url: "https://docs.getbifrost.ai",
		icon: BooksIcon,
		strokeWidth: 1,
	},
];

const ProductionSetupHelpCard = ({ t }: { t: (key: string) => string }) => ({
	id: "production-setup",
	title: t("sidebar.productionSetup.title"),
	description: (
		<>
			{t("sidebar.productionSetup.description")}
			<br />
			<br />
			<a
				href="https://calendly.com/maximai/bifrost-demo?utm_source=bfd_sdbr"
				target="_blank"
				className="text-primary font-medium underline"
				rel="noopener noreferrer"
			>
				{t("sidebar.productionSetup.bookDemo")}
			</a>
		</>
	),
	dismissible: true,
});

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
	isExternal?: boolean;
	queryParam?: string; // Optional: for tab-based subitems (e.g., "client-settings")
}

const getSidebarItemHref = (item: Pick<SidebarItem, "url" | "queryParam">) => {
	return item.queryParam ? `${item.url}?tab=${item.queryParam}` : item.url;
};

const slug = (s: string) => s.toLowerCase().replace(/\s+/g, "-");

const TIME_FILTER_PAGES = new Set(["/workspace/dashboard", "/workspace/logs", "/workspace/mcp-logs"]);

const SidebarItemView = ({
	item,
	isActive,
	isExternal,
	isWebSocketConnected,
	isExpanded,
	onToggle,
	pathname,
	search,
	isSidebarCollapsed,
	expandSidebar,
	highlightedUrl,
}: {
	item: SidebarItem;
	isActive: boolean;
	isExternal?: boolean;
	isWebSocketConnected: boolean;
	isExpanded?: boolean;
	onToggle?: () => void;
	pathname: string;
	search: string;
	isSidebarCollapsed: boolean;
	expandSidebar: () => void;
	highlightedUrl?: string;
}) => {
	const [flyoutOpen, setFlyoutOpen] = useState(false);
	const flyoutCloseTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
	const openFlyout = () => {
		if (flyoutCloseTimer.current) clearTimeout(flyoutCloseTimer.current);
		setFlyoutOpen(true);
	};
	const closeFlyout = () => {
		if (flyoutCloseTimer.current) clearTimeout(flyoutCloseTimer.current);
		flyoutCloseTimer.current = setTimeout(() => {
			setFlyoutOpen(false);
			flyoutCloseTimer.current = null;
		}, 80);
	};
	useEffect(() => {
		return () => {
			if (flyoutCloseTimer.current) clearTimeout(flyoutCloseTimer.current);
		};
	}, []);
	const hasSubItems = "subItems" in item && item.subItems && item.subItems.length > 0;
	const isRouteMatch = (url: string) => {
		if (url === "/workspace/custom-pricing") return pathname === url;
		return pathname.startsWith(url);
	};
	const isAnySubItemActive =
		hasSubItems &&
		item.subItems?.some((subItem) => {
			return isRouteMatch(subItem.url);
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

	const isHighlighted = !hasSubItems && highlightedUrl === item.url;

	const buttonClassName = `relative h-7.5 cursor-pointer rounded-sm border px-3 transition-all duration-200 ${
		isHighlighted
			? "bg-sidebar-accent text-accent-foreground border-primary/20"
			: isActive || isAnySubItemActive
				? "bg-sidebar-accent text-primary border-primary/20"
				: item.hasAccess
					? "hover:bg-sidebar-accent hover:text-accent-foreground border-transparent text-slate-500 dark:text-zinc-400"
					: "hover:bg-destructive/5 hover:text-muted-foreground cursor-not-allowed border-transparent"
	} `;

	const innerContent = (
		<div className="flex w-full items-center justify-between">
			<div className="flex w-full items-center gap-2">
				<item.icon className={`h-4 w-4 shrink-0 ${isActive || isAnySubItemActive ? "text-primary" : "text-muted-foreground"}`} />
				<span className={`text-sm group-data-[collapsible=icon]:hidden ${isActive || isAnySubItemActive ? "font-medium" : "font-normal"}`}>
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
	);

	// Render strategy:
	//   - Items with sub-items: <button> (toggle, not navigation)
	//   - Leaf items, no access: <button> (disabled-style, non-clickable)
	//   - Leaf items, external:  <a target="_blank">
	//   - Leaf items, internal:  TanStack <Link> with preload-on-hover
	let menuButton: React.ReactNode;
	if (hasSubItems) {
		menuButton = (
			<SidebarMenuButton tooltip={isSidebarCollapsed ? undefined : item.title} className={buttonClassName} onClick={handleClick}>
				{innerContent}
			</SidebarMenuButton>
		);
	} else if (!item.hasAccess) {
		menuButton = (
			<SidebarMenuButton tooltip={item.title} data-nav-url={item.url} className={buttonClassName}>
				{innerContent}
			</SidebarMenuButton>
		);
	} else if (isExternal) {
		menuButton = (
			<SidebarMenuButton asChild tooltip={item.title} className={buttonClassName}>
				<a
					href={item.url}
					target="_blank"
					rel="noopener noreferrer"
					data-nav-url={item.url}
					onClick={isSidebarCollapsed ? (e: React.MouseEvent) => e.stopPropagation() : undefined}
				>
					{innerContent}
				</a>
			</SidebarMenuButton>
		);
	} else {
		menuButton = (
			<SidebarMenuButton asChild tooltip={item.title} className={buttonClassName}>
				<Link
					to={item.url as any}
					preload="intent"
					data-nav-url={item.url}
					onClick={isSidebarCollapsed ? (e: React.MouseEvent) => e.stopPropagation() : undefined}
				>
					{innerContent}
				</Link>
			</SidebarMenuButton>
		);
	}

	return (
		<SidebarMenuItem key={item.title}>
			{isSidebarCollapsed && hasSubItems ? (
				<Popover open={flyoutOpen} onOpenChange={setFlyoutOpen}>
					<PopoverTrigger asChild onMouseEnter={openFlyout} onMouseLeave={closeFlyout}>
						<div data-testid={`sidebar-flyout-trigger-${slug(item.title)}`}>{menuButton}</div>
					</PopoverTrigger>
					<PopoverContent
						side="right"
						align="start"
						sideOffset={8}
						className="w-48 p-1"
						onMouseEnter={openFlyout}
						onMouseLeave={closeFlyout}
						data-testid={`sidebar-flyout-content-${slug(item.title)}`}
					>
						<div className="text-muted-foreground px-2 py-1.5 text-xs font-medium">{item.title}</div>
						{item.subItems?.map((subItem) => {
							const href = getSidebarItemHref(subItem);
							const isSubItemActive = subItem.queryParam ? pathname === subItem.url : pathname.startsWith(subItem.url);
							const SubItemIcon = subItem.icon;
							const subSlug = slug(subItem.title);
							const inner = (
								<div className="flex items-center gap-2">
									{SubItemIcon && <SubItemIcon className={`h-3.5 w-3.5 ${isSubItemActive ? "text-primary" : "text-muted-foreground"}`} />}
									<span className={`text-sm ${isSubItemActive ? "text-primary font-medium" : "text-slate-500 dark:text-zinc-400"}`}>
										{subItem.title}
									</span>
									{subItem.tag && (
										<Badge variant="secondary" className="text-muted-foreground ml-auto text-xs">
											{subItem.tag}
										</Badge>
									)}
								</div>
							);
							return (
								<div key={subItem.title} data-testid={`sidebar-flyout-subitem-${subSlug}`} onClick={() => setFlyoutOpen(false)}>
									{subItem.hasAccess === false ? (
										<div
											data-testid={`sidebar-subitem-disabled-${subSlug}`}
											className="text-muted-foreground hover:bg-destructive/5 flex h-7 cursor-not-allowed items-center rounded-sm px-2"
										>
											{inner}
										</div>
									) : (
										<Link
											to={href as any}
											preload="intent"
											data-testid={`sidebar-subitem-link-${subSlug}`}
											className={`flex h-7 items-center rounded-sm px-2 ${isSubItemActive ? "bg-sidebar-accent" : "hover:bg-sidebar-accent"}`}
										>
											{inner}
										</Link>
									)}
								</div>
							);
						})}
					</PopoverContent>
				</Popover>
			) : (
				menuButton
			)}
			{hasSubItems && isExpanded && (
				<SidebarMenuSub className="border-sidebar-border mt-1 ml-4 space-y-0.5 border-l pl-2">
					{item.subItems?.map((subItem: SidebarItem) => {
						const baseHref = getSidebarItemHref(subItem);
						const subItemHref = (() => {
							if (TIME_FILTER_PAGES.has(subItem.url) && TIME_FILTER_PAGES.has(pathname)) {
								const currentParams = new URLSearchParams(search);
								const startTime = currentParams.get("start_time");
								const endTime = currentParams.get("end_time");
								const period = currentParams.get("period");
								if ((startTime && endTime) || period) {
									const params = new URLSearchParams();
									if (startTime) params.set("start_time", startTime);
									if (endTime) params.set("end_time", endTime);
									if (period) params.set("period", period);
									const sep = baseHref.includes("?") ? "&" : "?";
									return `${baseHref}${sep}${params.toString()}`;
								}
							}
							return baseHref;
						})();
						const isSubItemActive = subItem.queryParam ? pathname === subItem.url : isRouteMatch(subItem.url);
						const isSubItemHighlighted = highlightedUrl ? subItemHref.startsWith(highlightedUrl) : false;
						const SubItemIcon = subItem.icon;
						const subItemClassName = `h-7 cursor-pointer rounded-sm px-2 transition-all duration-200 ${
							isSubItemHighlighted
								? "bg-sidebar-accent text-accent-foreground"
								: isSubItemActive
									? "bg-sidebar-accent text-primary font-medium"
									: subItem.hasAccess === false
										? "hover:bg-destructive/5 hover:text-muted-foreground text-muted-foreground cursor-not-allowed border-transparent"
										: "hover:bg-sidebar-accent hover:text-accent-foreground text-slate-500 dark:text-zinc-400"
						}`;
						const subInner = (
							<div className="flex w-full items-center gap-2">
								{SubItemIcon && <SubItemIcon className={`h-3.5 w-3.5 ${isSubItemActive ? "text-primary" : "text-muted-foreground"}`} />}
								<span className={`text-sm ${isSubItemActive ? "font-medium" : "font-normal"}`}>{subItem.title}</span>
								{subItem.tag && (
									<Badge variant="secondary" className="text-muted-foreground ml-auto text-xs">
										{subItem.tag}
									</Badge>
								)}
							</div>
						);
						return (
							<SidebarMenuSubItem key={subItem.title}>
								{subItem.hasAccess === false ? (
									<SidebarMenuSubButton data-nav-url={subItemHref} className={subItemClassName}>
										{subInner}
									</SidebarMenuSubButton>
								) : (
									<SidebarMenuSubButton asChild className={subItemClassName}>
										<Link to={subItemHref as any} preload="intent" data-nav-url={subItemHref}>
											{subInner}
										</Link>
									</SidebarMenuSubButton>
								)}
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
	const { t } = useTranslation();
	const pathname = useLocation({ select: (l) => l.pathname });
	const search = useLocation({ select: (l) => l.searchStr ?? "" });
	const tsNavigate = useNavigate();
	// Wrapper that accepts arbitrary string URLs (TanStack Router's `to` is
	// strictly typed, but our sidebar items come from a runtime config).
	const navigate = useCallback((url: string) => tsNavigate({ to: url as string }), [tsNavigate]);
	const [mounted, setMounted] = useState(false);
	const [expandedItems, setExpandedItems] = useState<Set<string>>(new Set());
	const [areCardsEmpty, setAreCardsEmpty] = useState(false);
	const [userPopoverOpen, setUserPopoverOpen] = useState(false);
	const [searchQuery, setSearchQuery] = useState("");
	const [focusedIndex, setFocusedIndex] = useState(-1);
	const searchInputRef = useRef<HTMLInputElement>(null);
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
	const hasUsersAccess = useRbac(RbacResource.Users, RbacOperation.View);
	const hasUserProvisioningAccess = useRbac(RbacResource.UserProvisioning, RbacOperation.View);
	const hasAuditLogsAccess = useRbac(RbacResource.AuditLogs, RbacOperation.View);
	const hasCustomersAccess = useRbac(RbacResource.Customers, RbacOperation.View);
	const hasTeamsAccess = useRbac(RbacResource.Teams, RbacOperation.View);
	const hasBusinessUnitsAccess = useRbac(RbacResource.Governance, RbacOperation.View);
	const hasRbacAccess = useRbac(RbacResource.RBAC, RbacOperation.View);
	const hasVirtualKeysAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.View);
	const hasGovernanceLegacyAccess = useRbac(RbacResource.Governance, RbacOperation.View);
	const hasRoutingRulesAccess = useRbac(RbacResource.RoutingRules, RbacOperation.View);
	const hasGuardrailsProvidersAccess = useRbac(RbacResource.GuardrailsProviders, RbacOperation.View);
	const hasGuardrailsConfigAccess = useRbac(RbacResource.GuardrailsConfig, RbacOperation.View);
	const hasClusterConfigAccess = useRbac(RbacResource.Cluster, RbacOperation.View);
	const isAdaptiveRoutingAllowed = useRbac(RbacResource.AdaptiveRouter, RbacOperation.View);
	const hasSettingsAccess = useRbac(RbacResource.Settings, RbacOperation.View);
	const hasPromptRepositoryAccess = useRbac(RbacResource.PromptRepository, RbacOperation.View);
	const hasAccessProfilesAccess = useRbac(RbacResource.AccessProfiles, RbacOperation.View);
	const hasAnyGovernanceAccess =
		hasVirtualKeysAccess ||
		hasTeamsAccess ||
		hasUsersAccess ||
		hasCustomersAccess ||
		hasBusinessUnitsAccess ||
		hasRbacAccess ||
		hasAccessProfilesAccess ||
		hasGovernanceLegacyAccess;
	const { data: coreConfig } = useGetCoreConfigQuery({});
	const isDbConnected = coreConfig?.is_db_connected ?? false;

	const items = useMemo(
		() => [
			{
				title: t("sidebar.nav.observability"),
				url: "/workspace/logs",
				icon: Telescope,
				description: t("sidebar.desc.requestLogsMonitoring"),
				hasAccess: hasLogsAccess,
				subItems: [
					{
						title: t("sidebar.sub.dashboard"),
						url: "/workspace/dashboard",
						icon: ChartColumnBig,
						description: t("sidebar.desc.dashboard"),
						hasAccess: hasObservabilityAccess,
					},
					{
						title: t("sidebar.sub.llmLogs"),
						url: "/workspace/logs",
						icon: Logs,
						description: t("sidebar.desc.llmRequestLogs"),
						hasAccess: hasLogsAccess,
					},
					{
						title: t("sidebar.sub.mcpLogs"),
						url: "/workspace/mcp-logs",
						icon: MCPIcon,
						description: t("sidebar.desc.mcpToolLogs"),
						hasAccess: hasLogsAccess,
					},
					{
						title: t("sidebar.sub.connectors"),
						url: "/workspace/observability",
						icon: ChevronsLeftRightEllipsis,
						description: t("sidebar.desc.logConnectors"),
						hasAccess: hasObservabilityAccess,
					},
					{
						title: t("sidebar.sub.logsSettings"),
						url: "/workspace/config/logging",
						icon: Settings,
						description: t("sidebar.desc.logsConfiguration"),
						hasAccess: hasSettingsAccess,
					},
				],
			},
			{
				title: t("sidebar.nav.models"),
				url: "/workspace/providers",
				icon: BoxIcon,
				description: t("sidebar.desc.configureModels"),
				hasAccess: true,
				subItems: [
					{
						title: t("sidebar.sub.modelCatalog"),
						url: "/workspace/model-catalog",
						icon: LayoutGrid,
						description: t("sidebar.desc.overviewProvidersKeys"),
						hasAccess: hasModelProvidersAccess,
					},
					{
						title: t("sidebar.sub.modelProviders"),
						url: "/workspace/providers",
						icon: Boxes,
						description: t("sidebar.desc.configureModels"),
						hasAccess: hasModelProvidersAccess,
					},
					{
						title: t("sidebar.sub.budgetsLimits"),
						url: "/workspace/model-limits",
						icon: Wallet,
						description: t("sidebar.desc.modelLimits"),
						hasAccess: hasGovernanceLegacyAccess,
					},
					{
						title: t("sidebar.sub.routingRules"),
						url: "/workspace/routing-rules",
						icon: Network,
						description: t("sidebar.desc.intelligentRouting"),
						hasAccess: hasRoutingRulesAccess,
					},
					{
						title: t("sidebar.sub.pricingOverrides"),
						url: "/workspace/custom-pricing/overrides",
						icon: SlidersHorizontal,
						description: t("sidebar.desc.scopedPricingOverrides"),
						hasAccess: hasSettingsAccess,
					},
					{
						title: t("sidebar.sub.modelSettings"),
						url: "/workspace/custom-pricing",
						icon: Settings,
						description: t("sidebar.desc.modelRoutingConfig"),
						hasAccess: hasSettingsAccess,
					},
				],
			},
			{
				title: t("sidebar.nav.mcpGateway"),
				icon: MCPIcon,
				description: t("sidebar.desc.mcpConfiguration"),
				url: "/workspace/mcp-gateway",
				hasAccess: hasMCPGatewayAccess,
				subItems: [
					{
						title: t("sidebar.sub.mcpCatalog"),
						url: "/workspace/mcp-registry",
						icon: LayoutGrid,
						description: t("sidebar.desc.mcpToolCatalog"),
						hasAccess: hasMCPGatewayAccess,
					},
					{
						title: t("sidebar.sub.toolGroups"),
						url: "/workspace/mcp-tool-groups",
						icon: ToolCase,
						description: t("sidebar.desc.toolGroups"),
						hasAccess: hasMCPGatewayAccess,
					},
					{
						title: t("sidebar.sub.mcpSettings"),
						url: "/workspace/mcp-settings",
						icon: Settings,
						description: t("sidebar.desc.mcpConfiguration"),
						hasAccess: hasMCPGatewayAccess,
					},
				],
			},
			{
				title: t("sidebar.nav.plugins"),
				url: "/workspace/plugins",
				icon: Puzzle,
				description: t("sidebar.desc.manageCustomPlugins"),
				hasAccess: hasPluginsAccess,
			},
			{
				title: t("sidebar.nav.governance"),
				url: "/workspace/governance",
				icon: Landmark,
				description: t("sidebar.desc.virtualKeysUsersTeams"),
				hasAccess: hasAnyGovernanceAccess,
				subItems: [
					{
						title: t("sidebar.sub.virtualKeys"),
						url: "/workspace/governance/virtual-keys",
						icon: KeyRound,
						description: t("sidebar.desc.manageVirtualKeys"),
						hasAccess: hasVirtualKeysAccess,
					},
					{
						title: t("sidebar.sub.users"),
						url: "/workspace/governance/users",
						icon: Users,
						description: t("sidebar.desc.manageUsers"),
						hasAccess: hasUsersAccess,
					},
					{
						title: t("sidebar.sub.teams"),
						url: "/workspace/governance/teams",
						icon: Building,
						description: t("sidebar.desc.manageTeams"),
						hasAccess: hasTeamsAccess,
					},
					{
						title: t("sidebar.sub.businessUnits"),
						url: "/workspace/governance/business-units",
						icon: Building2,
						description: t("sidebar.desc.manageBusinessUnits"),
						hasAccess: hasBusinessUnitsAccess,
					},
					{
						title: t("sidebar.sub.customers"),
						url: "/workspace/governance/customers",
						icon: WalletCards,
						description: t("sidebar.desc.manageCustomers"),
						hasAccess: hasCustomersAccess,
					},
					{
						title: t("sidebar.sub.userProvisioning"),
						url: "/workspace/scim",
						icon: BookUser,
						description: t("sidebar.desc.userManagementProvisioning"),
						hasAccess: hasUserProvisioningAccess,
					},
					{
						title: t("sidebar.sub.rolesPermissions"),
						url: "/workspace/governance/rbac",
						icon: UserRoundCheck,
						description: t("sidebar.desc.userRolesPermissions"),
						hasAccess: hasRbacAccess,
					},
					{
						title: t("sidebar.sub.accessProfiles"),
						url: "/workspace/governance/access-profiles",
						icon: ShieldCheck,
						description: t("sidebar.desc.manageAccessProfiles"),
						hasAccess: hasAccessProfilesAccess,
					},
					{
						title: t("sidebar.sub.auditLogs"),
						url: "/workspace/audit-logs",
						icon: ScrollText,
						description: t("sidebar.desc.auditLogsCompliance"),
						hasAccess: hasAuditLogsAccess,
					},
				],
			},
			{
				title: t("sidebar.nav.guardrails"),
				url: "/workspace/guardrails",
				icon: Construction,
				description: t("sidebar.desc.guardrailsConfig"),
				hasAccess: hasGuardrailsConfigAccess || hasGuardrailsProvidersAccess,
				subItems: [
					{
						title: t("sidebar.sub.rules"),
						url: "/workspace/guardrails/configuration",
						icon: SearchCheck,
						description: t("sidebar.desc.guardrailRules"),
						hasAccess: hasGuardrailsConfigAccess,
					},
					{
						title: t("sidebar.sub.providers"),
						url: "/workspace/guardrails/providers",
						icon: Boxes,
						description: t("sidebar.desc.guardrailProviders"),
						hasAccess: hasGuardrailsProvidersAccess,
					},
				],
			},
			{
				title: t("sidebar.nav.clusterConfig"),
				url: "/workspace/cluster",
				icon: Network,
				description: t("sidebar.desc.manageBifrostCluster"),
				hasAccess: hasClusterConfigAccess,
			},
			{
				title: t("sidebar.nav.adaptiveRouting"),
				url: "/workspace/adaptive-routing",
				icon: Shuffle,
				description: t("sidebar.desc.manageAdaptiveLoadBalancer"),
				hasAccess: isAdaptiveRoutingAllowed,
			},
			...(isDbConnected
				? [
						{
							title: t("sidebar.nav.promptRepository"),
							url: "/workspace/prompt-repo",
							icon: FolderGit,
							description: t("sidebar.desc.promptRepository"),
							hasAccess: hasPromptRepositoryAccess,
						},
					]
				: []),
			{
				title: t("sidebar.nav.evals"),
				url: "https://www.getmaxim.ai",
				icon: FlaskConical,
				isExternal: true,
				description: t("sidebar.desc.evaluations"),
				hasAccess: true,
			},
			{
				title: t("sidebar.nav.settings"),
				url: "/workspace/config",
				icon: Settings2Icon,
				description: t("sidebar.desc.bifrostSettings"),
				hasAccess: hasSettingsAccess || hasAuditLogsAccess || hasUserProvisioningAccess,
				subItems: [
					{
						title: t("sidebar.sub.clientSettings"),
						url: "/workspace/config/client-settings",
						icon: Settings,
						description: t("sidebar.desc.clientConfiguration"),
						hasAccess: hasSettingsAccess,
					},
					{
						title: t("sidebar.sub.compatibility"),
						url: "/workspace/config/compatibility",
						icon: Plug,
						description: t("sidebar.desc.compatibilitySettings"),
						hasAccess: hasSettingsAccess,
					},
					{
						title: t("sidebar.sub.caching"),
						url: "/workspace/config/caching",
						icon: DatabaseZap,
						description: t("sidebar.desc.cachingConfiguration"),
						hasAccess: hasSettingsAccess,
					},
					{
						title: t("sidebar.sub.security"),
						url: "/workspace/config/security",
						icon: ShieldCheck,
						description: t("sidebar.desc.securitySettings"),
						hasAccess: hasSettingsAccess,
					},
					...(IS_ENTERPRISE
						? [
								{
									title: t("sidebar.sub.proxy"),
									url: "/workspace/config/proxy",
									icon: Globe,
									description: t("sidebar.desc.proxyConfiguration"),
									hasAccess: hasSettingsAccess,
								},
							]
						: []),
					{
						title: t("sidebar.sub.apiKeys"),
						url: "/workspace/config/api-keys",
						icon: KeyRound,
						description: t("sidebar.desc.apiKeysManagement"),
						hasAccess: hasSettingsAccess,
					},
					{
						title: t("sidebar.sub.performanceTuning"),
						url: "/workspace/config/performance-tuning",
						icon: TrendingUp,
						description: t("sidebar.desc.performanceTuningSettings"),
						hasAccess: hasSettingsAccess,
					},
				],
			},
		],
		[
			t,
			hasLogsAccess,
			hasObservabilityAccess,
			hasModelProvidersAccess,
			hasMCPGatewayAccess,
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
			hasClusterConfigAccess,
			isAdaptiveRoutingAllowed,
			hasSettingsAccess,
			hasPromptRepositoryAccess,
			hasAccessProfilesAccess,
			isDbConnected,
		],
	);

	const filteredItems: SidebarItem[] = useMemo(() => {
		const query = searchQuery.trim().toLowerCase();
		if (!query) return items;

		return items
			.map((item) => {
				const parentMatches = item.title.toLowerCase().includes(query);
				if (parentMatches) return item;

				if (item.subItems) {
					const matchingSubItems = item.subItems.filter((sub) => sub.title.toLowerCase().includes(query));
					if (matchingSubItems.length > 0) {
						return { ...item, subItems: matchingSubItems };
					}
				}
				return null;
			})
			.filter(Boolean) as SidebarItem[];
	}, [items, searchQuery]);

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
	const isAuthEnabled = coreConfig?.auth_config?.is_enabled || false;

	useEffect(() => {
		setMounted(true);
	}, []);

	// Auto-expand items when their subitems are active
	useEffect(() => {
		const newExpandedItems = new Set<string>();
		const isRouteMatch = (url: string) => {
			if (url === "/workspace/custom-pricing") return pathname === url;
			return pathname.startsWith(url);
		};
		items.forEach((item) => {
			if (item.subItems?.some((subItem) => isRouteMatch(subItem.url))) {
				newExpandedItems.add(item.title);
			}
		});
		if (newExpandedItems.size > 0) {
			setExpandedItems((prev) => new Set([...prev, ...newExpandedItems]));
		}
	}, [pathname, items]);

	// Auto-expand parents when search matches their subItems
	useEffect(() => {
		const query = searchQuery.trim().toLowerCase();
		if (!query) return;
		const toExpand = new Set<string>();
		items.forEach((item) => {
			if (!item.subItems?.length) return;
			const parentMatches = item.title.toLowerCase().includes(query);
			if (parentMatches) return;
			const hasMatchingChild = item.subItems.some((sub) => sub.title.toLowerCase().includes(query));
			if (hasMatchingChild) {
				toExpand.add(item.title);
			}
		});
		if (toExpand.size > 0) {
			setExpandedItems((prev) => {
				const hasAll = [...toExpand].every((t) => prev.has(t));
				if (hasAll) return prev;
				return new Set([...prev, ...toExpand]);
			});
		}
	}, [searchQuery, items]);

	// Cmd+K to focus search input
	useEffect(() => {
		const handleKeyDown = (event: KeyboardEvent) => {
			if (event.key === "k" && (event.metaKey || event.ctrlKey)) {
				event.preventDefault();
				searchInputRef.current?.focus();
			}
		};
		window.addEventListener("keydown", handleKeyDown);
		return () => window.removeEventListener("keydown", handleKeyDown);
	}, []);

	// Flat list of navigable items for keyboard navigation
	const navigableItems = useMemo(() => {
		const result: {
			title: string;
			url: string;
			queryParam?: string;
			isExternal?: boolean;
		}[] = [];
		for (const item of filteredItems) {
			if (item.isExternal) {
				if (item.hasAccess) result.push({ title: item.title, url: item.url, isExternal: true });
				continue;
			}
			const hasSubItems = item.subItems && item.subItems.length > 0;
			if (hasSubItems) {
				// When search is active or parent is expanded, include visible subItems
				if (searchQuery.trim() || expandedItems.has(item.title)) {
					for (const sub of item.subItems!) {
						if (sub.hasAccess === false) continue;
						result.push({
							title: sub.title,
							url: getSidebarItemHref(sub),
							queryParam: sub.queryParam,
						});
					}
				} else {
					// Parent is collapsed - include parent as a toggle target
					if (item.hasAccess) result.push({ title: item.title, url: item.url });
				}
			} else {
				if (item.hasAccess) result.push({ title: item.title, url: item.url });
			}
		}
		return result;
	}, [filteredItems, expandedItems, searchQuery]);

	const handleSearchKeyDown = useCallback(
		(e: React.KeyboardEvent<HTMLInputElement>) => {
			if (e.key === "ArrowDown") {
				e.preventDefault();
				setFocusedIndex((prev) => Math.min(prev + 1, navigableItems.length - 1));
			} else if (e.key === "ArrowUp") {
				e.preventDefault();
				setFocusedIndex((prev) => Math.max(prev - 1, 0));
			} else if (e.key === "Enter") {
				e.preventDefault();
				const target = navigableItems[focusedIndex];
				if (target) {
					const url = target.url;
					if (target.isExternal || e.metaKey || e.ctrlKey) {
						window.open(url, "_blank", "noopener,noreferrer");
					} else {
						navigate(url);
					}
					setSearchQuery("");
					setFocusedIndex(-1);
					searchInputRef.current?.blur();
				}
			} else if (e.key === "Escape") {
				setSearchQuery("");
				setFocusedIndex(-1);
				searchInputRef.current?.blur();
			}
		},
		[navigableItems, focusedIndex, navigate],
	);

	// Auto-scroll focused item into view
	useEffect(() => {
		if (focusedIndex < 0) return;
		const url = navigableItems[focusedIndex]?.url;
		if (!url) return;
		const el = document.querySelector(`[data-nav-url="${url}"]`);
		el?.scrollIntoView({ block: "nearest" });
	}, [focusedIndex, navigableItems]);

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

	const configExceptions = ["/workspace/config/logging"];

	const isActiveRoute = (url: string) => {
		if (url === "/" && pathname === "/") return true;
		// Avoid double-highlighting with "/workspace/custom-pricing/overrides"
		if (url === "/workspace/custom-pricing") return pathname === url;
		if (url !== "/" && pathname.startsWith(url)) {
			if (url === "/workspace/config" && configExceptions.some((e) => pathname.startsWith(e))) {
				return false;
			}
			return true;
		}
		return false;
	};

	// Always render the light theme version for SSR to avoid hydration mismatch
	const logoSrc = mounted && resolvedTheme === "dark" ? "/bifrost-logo-dark.webp" : "/bifrost-logo.webp";
	const iconSrc = mounted && resolvedTheme === "dark" ? "/bifrost-icon-dark.webp" : "/bifrost-icon.webp";

	const { isConnected: isWebSocketConnected } = useWebSocket();

	// New release image - based on theme
	const newReleaseImage = mounted && resolvedTheme === "dark" ? "/images/new-release-image-dark.webp" : "/images/new-release-image.webp";

	// Memoize promo cards array to prevent duplicates and unnecessary re-renders
	const promoCards = useMemo(() => {
		const cards = [];
		// Restart required card - non-dismissible, shown first
		if (coreConfig?.restart_required?.required) {
			cards.push({
				id: "restart-required",
				title: t("common.restartRequired"),
				description: (
					<div className="text-xs text-amber-700 dark:text-amber-300/80">
						{coreConfig.restart_required.reason || t("common.restartRequiredReason")}
					</div>
				),
				dismissible: false,
				variant: "warning" as const,
			});
		}
		if (showNewReleaseBanner && latestRelease) {
			cards.push({
				id: "new-release",
				title: t("common.newReleaseAvailable", { version: latestRelease.name }),
				description: (
					<div className="flex h-full flex-col gap-2">
						<img src={newReleaseImage} alt="Bifrost" className="h-[95px] rounded-md object-cover" />
						<a
							href={`https://docs.getbifrost.ai/changelogs/${latestRelease.name}`}
							target="_blank"
							rel="noopener noreferrer"
							className="text-primary mt-auto pb-1 font-medium underline"
						>
							{t("common.viewReleaseNotes")}
						</a>
					</div>
				),
				dismissible: true,
			});
		}
		// Only show after mounted to ensure cookie is properly hydrated and avoid flash
		if (!IS_ENTERPRISE && mounted && !isProductionSetupDismissed) {
			cards.push(ProductionSetupHelpCard({ t }));
		}
		return cards;
	}, [t, coreConfig?.restart_required, showNewReleaseBanner, latestRelease, newReleaseImage, isProductionSetupDismissed, mounted]);

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
			navigate("/login");
		} catch {
			// Even if logout fails on server, redirect to login
			navigate("/login");
		}
	};

	const trialDaysRemaining = useMemo(() => {
		if (IS_ENTERPRISE && TRIAL_EXPIRY) {
			const daysRemaining = differenceInDays(new Date(TRIAL_EXPIRY), new Date());
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
					<Link to="/workspace/logs" className="group flex items-center gap-2 pl-2">
						<img className="h-[22px] w-auto" src={logoSrc} alt="Bifrost" width={70} height={70} />
					</Link>
					<button
						onClick={toggleSidebar}
						type="button"
						data-testid="sidebar-collapse-btn"
						className="text-muted-foreground hover:text-foreground hover:bg-sidebar-accent flex h-7 w-7 items-center justify-center rounded-md transition-colors"
						aria-label={t("sidebar.collapseSidebar")}
					>
						<PanelLeftClose className="h-4 w-4" />
					</button>
				</div>
				{/* Collapsed state: vertical layout */}
				<div
					className="hidden w-full cursor-pointer flex-col items-center gap-2 py-2 group-data-[collapsible=icon]:flex"
					onClick={toggleSidebar}
				>
					<img className="h-[22px] w-auto" src={iconSrc} alt="Bifrost" width={22} height={22} style={{ width: 18 }} />
				</div>
			</SidebarHeader>
			<div className="mx-2 pb-1 group-data-[collapsible=icon]:hidden">
				<div className="relative">
					<Search className="text-muted-foreground absolute top-1/2 left-2.5 h-3.5 w-3.5 -translate-y-1/2" />
					<input
						ref={searchInputRef}
						type="text"
						aria-label={t("sidebar.searchNavigation")}
						placeholder={t("sidebar.searchPlaceholder")}
						value={searchQuery}
						onChange={(e) => {
							setSearchQuery(e.target.value);
							setFocusedIndex(-1);
						}}
						onKeyDown={handleSearchKeyDown}
						className="border-input text-foreground placeholder:text-shadow-muted-foreground focus:ring-ring h-8 w-full rounded-sm border bg-transparent pr-14 pl-8 text-sm outline-none focus:bg-transparent"
					/>
					<kbd className="text-muted-foreground pointer-events-none absolute top-1/2 right-2 flex -translate-y-1/2 gap-0.5 text-[10px]">
						<span className="border-border bg-muted rounded-sm px-1 font-mono shadow-sm">⌘</span>
						<span className="border-border bg-muted rounded-sm px-1 font-mono shadow-sm">K</span>
					</kbd>
				</div>
			</div>
			<SidebarContent className="overflow-hidden pb-4">
				<SidebarGroup className={`custom-scrollbar ${sidebarGroupHeight} overflow-scroll`}>
					<SidebarGroupContent>
						<SidebarMenu className="space-y-0.5">
							{filteredItems.map((item) => {
								const isActive = isActiveRoute(item.url);

								const highlightedUrl = focusedIndex >= 0 ? navigableItems[focusedIndex]?.url : undefined;
								return (
									<SidebarItemView
										key={item.title}
										item={item}
										isActive={isActive}
										isExternal={item.isExternal ?? false}
										isWebSocketConnected={isWebSocketConnected}
										isExpanded={expandedItems.has(item.title)}
										onToggle={() => toggleItem(item.title)}
										pathname={pathname}
										search={search}
										isSidebarCollapsed={sidebarState === "collapsed"}
										expandSidebar={() => toggleSidebar()}
										highlightedUrl={highlightedUrl}
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
									title={t(item.titleKey)}
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
							<LanguageSwitcher />
							{IS_ENTERPRISE && userInfo && (userInfo.name || userInfo.email) ? (
								<Popover open={userPopoverOpen} onOpenChange={setUserPopoverOpen}>
									<PopoverTrigger asChild>
										<button
											className="hover:text-primary text-muted-foreground flex cursor-pointer items-center space-x-3 p-0.5"
											type="button"
											aria-label={t("sidebar.userMenu")}
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
												<span>{t("common.logout")}</span>
											</button>
										</div>
									</PopoverContent>
								</Popover>
							) : isAuthEnabled ? (
								<div>
									<button
										className="hover:text-primary text-muted-foreground flex cursor-pointer items-center space-x-3 p-0.5"
										onClick={handleLogout}
										type="button"
										aria-label={t("common.logout")}
									>
										<LogOut className="hover:text-primary text-muted-foreground h-4 w-4" size={20} strokeWidth={2} />
									</button>
								</div>
							) : null}
							<div className="hidden w-full cursor-pointer flex-col items-center group-data-[collapsible=icon]:flex">
								<button
									onClick={toggleSidebar}
									type="button"
									data-testid="sidebar-expand-btn"
									className="text-muted-foreground hover:text-foreground hover:bg-sidebar-accent flex cursor-pointer items-center justify-center rounded-md transition-colors"
									aria-label={t("sidebar.expandSidebar", { defaultValue: "Expand sidebar" })}
								>
									<PanelLeftOpen className="h-4 w-4" />
								</button>
							</div>
						</div>
					</div>
					<div className="mx-auto flex flex-col items-center gap-1 group-data-[collapsible=icon]:hidden">
						<div className="font-mono text-xs">{version ?? ""}</div>
						{trialDaysRemaining !== null && (
							<div className={cn("text-xs", trialDaysRemaining < 3 ? "text-red-500" : "text-muted-foreground")}>
								{trialDaysRemaining} {trialDaysRemaining === 1 ? t("common.day") : t("common.days")} {t("common.remaining")}
							</div>
						)}
					</div>
				</div>
			</SidebarContent>
		</Sidebar>
	);
}
