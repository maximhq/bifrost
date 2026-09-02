import {
	ArrowUpRight,
	BadgeCheck,
	BadgeInfo,
	BookOpenText,
	BookUser,
	Boxes,
	BoxIcon,
	Building,
	Building2,
	ChartColumnBig,
	ChevronsLeftRightEllipsis,
	CircuitBoard,
	Construction,
	DatabaseZap,
	Flag,
	FolderGit,
	SquareKanban,
	Gavel,
	GitCompareArrows,
	Globe,
	Hexagon,
	History,
	KeyRound,
	Landmark,
	LaptopMinimalCheck,
	LayoutGrid,
	Logs,
	Megaphone,
	Network,
	Palette,
	PanelLeftClose,
	PanelLeftOpen,
	Plug,
	Puzzle,
	ScrollText,
	Search,
	SearchCheck,
	Settings,
	Settings2Icon,
	ShieldCheck,
	Shuffle,
	Siren,
	SlidersHorizontal,
	Telescope,
	ToolCase,
	TrendingUp,
	UserRoundCheck,
	Users,
	Wallet,
	WalletCards,
	Webhook,
} from "lucide-react";

import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
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
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { HIDDEN_UNTIL_NAV_COOKIE, REMIND_LATER_COOKIE, useOnboardingChecklist } from "@/hooks/useOnboardingChecklist";
import { useWebSocket } from "@/hooks/useWebSocket";
import { IS_ENTERPRISE } from "@/lib/constants/config";
import { useBranding } from "@/lib/hooks/useBranding";
import { useGetCoreConfigQuery, useGetLatestReleaseQuery, useGetVersionQuery } from "@/lib/store";
import PoweredByBifrost from "@enterprise/components/branding/poweredByBifrost";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Link, useLocation, useNavigate } from "@tanstack/react-router";
import { ChevronRight } from "lucide-react";
import { useTheme } from "next-themes";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useCookies } from "react-cookie";
import { useTranslation } from "react-i18next";
import { Badge } from "./ui/badge";
import { PromoCardStack } from "./ui/promoCardStack";

// Cookie name for dismissing production setup card
const PRODUCTION_SETUP_DISMISSED_COOKIE = "bifrost_production_setup_dismissed";
// Closing the "setup checklist incomplete" promo card only snoozes that card
// for a day — separate from the widget's own hidden/snoozed cookies, so it
// doesn't affect whether the floating widget itself reappears on next nav.
const ONBOARDING_CARD_DISMISSED_COOKIE = "bifrost_onboarding_card_dismissed";

// Custom MCP Icon Component
const MCPIcon = ({ className, label = "MCP clients icon" }: { className?: string; label?: string }) => (
	<svg
		className={className}
		fill="currentColor"
		fillRule="evenodd"
		height="1em"
		style={{ flex: "none", lineHeight: 1 }}
		viewBox="0 0 24 24"
		width="1em"
		xmlns="http://www.w3.org/2000/svg"
		aria-label={label}
	>
		<title>{label}</title>
		<path d="M15.688 2.343a2.588 2.588 0 00-3.61 0l-9.626 9.44a.863.863 0 01-1.203 0 .823.823 0 010-1.18l9.626-9.44a4.313 4.313 0 016.016 0 4.116 4.116 0 011.204 3.54 4.3 4.3 0 013.609 1.18l.05.05a4.115 4.115 0 010 5.9l-8.706 8.537a.274.274 0 000 .393l1.788 1.754a.823.823 0 010 1.18.863.863 0 01-1.203 0l-1.788-1.753a1.92 1.92 0 010-2.754l8.706-8.538a2.47 2.47 0 000-3.54l-.05-.049a2.588 2.588 0 00-3.607-.003l-7.172 7.034-.002.002-.098.097a.863.863 0 01-1.204 0 .823.823 0 010-1.18l7.273-7.133a2.47 2.47 0 00-.003-3.537z" />
		<path d="M14.485 4.703a.823.823 0 000-1.18.863.863 0 00-1.204 0l-7.119 6.982a4.115 4.115 0 000 5.9 4.314 4.314 0 006.016 0l7.12-6.982a.823.823 0 000-1.18.863.863 0 00-1.204 0l-7.119 6.982a2.588 2.588 0 01-3.61 0 2.47 2.47 0 010-3.54l7.12-6.982z" />
	</svg>
);

// Sidebar item interface
interface SidebarItem {
	id: string;
	title: string;
	url: string;
	icon: React.ComponentType<{ className?: string }>;
	description?: string;
	isAllowed?: boolean;
	hasAccess: boolean;
	subItems?: SidebarItem[];
	tag?: string;
	new?: boolean;
	isExternal?: boolean;
	queryParam?: string; // Optional: for tab-based subitems (e.g., "client-settings")
	testId?: string; // Optional: pin the data-testid slug across a rename (else derived from title)
}

const getSidebarItemHref = (item: Pick<SidebarItem, "url" | "queryParam">) => {
	return item.queryParam ? `${item.url}?tab=${item.queryParam}` : item.url;
};


const TimeFilterPages = new Set(["/workspace/dashboard", "/workspace/logs", "/workspace/mcp-logs"]);

const preserveTimeFilters = (baseHref: string, subItemUrl: string, pathname: string, search: string): string => {
	if (TimeFilterPages.has(subItemUrl) && TimeFilterPages.has(pathname)) {
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
};

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
		// Exact-match base paths that have sibling tab routes nested under them, so the base
		// tab isn't also highlighted when a child tab (e.g. /settings) is active.
		if (url === "/workspace/custom-pricing" || url === "/workspace/adaptive-routing") return pathname === url;
		// Avoid double-highlighting with "/workspace/mcp-registry/library"
		if (url === "/workspace/mcp-registry") return !pathname.startsWith("/workspace/mcp-registry/library") && pathname.startsWith(url);
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

	const buttonClassName = `group/nav-item relative h-7.5 cursor-pointer rounded-sm border px-3 transition-all duration-200 ${isHighlighted
		? "bg-sidebar-accent text-accent-foreground border-primary/20"
		: isActive || isAnySubItemActive
			? "bg-sidebar-accent text-primary border-primary/20"
			: item.hasAccess
				? "hover:bg-sidebar-accent hover:text-accent-foreground border-transparent text-slate-500 dark:text-zinc-400"
				: "hover:bg-destructive/5 hover:text-muted-foreground text-muted-foreground cursor-not-allowed border-transparent"
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
			<SidebarMenuButton
				tooltip={isSidebarCollapsed ? undefined : item.title}
				className={buttonClassName}
				onClick={handleClick}
				data-testid={`sidebar-item-btn-${item.id}`}
			>
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
					to={item.url}
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
		<SidebarMenuItem key={item.id}>
			{isSidebarCollapsed && hasSubItems ? (
				<Popover open={flyoutOpen} onOpenChange={setFlyoutOpen}>
					<PopoverTrigger asChild onMouseEnter={openFlyout} onMouseLeave={closeFlyout}>
						<div data-testid={`sidebar-flyout-trigger-${item.id}`}>{menuButton}</div>
					</PopoverTrigger>
					<PopoverContent
						side="right"
						align="start"
						sideOffset={8}
						className="w-48 p-1"
						onMouseEnter={openFlyout}
						onMouseLeave={closeFlyout}
						data-testid={`sidebar-flyout-content-${item.id}`}
					>
						<div className="text-muted-foreground px-2 py-1.5 text-xs font-medium">{item.title}</div>
						{item.subItems?.map((subItem) => {
							const baseHref = getSidebarItemHref(subItem);
							const href = preserveTimeFilters(baseHref, subItem.url, pathname, search);
							const isSubItemActive = subItem.queryParam ? pathname === subItem.url : isRouteMatch(subItem.url);
							const SubItemIcon = subItem.icon;
							const subId = subItem.id;
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
								<div key={subItem.id} data-testid={`sidebar-flyout-subitem-${subId}`} onClick={() => setFlyoutOpen(false)}>
									{subItem.hasAccess === false ? (
										<div
											data-testid={`sidebar-subitem-disabled-${subId}`}
											className="text-muted-foreground hover:bg-destructive/5 flex h-7 cursor-not-allowed items-center rounded-sm px-2"
										>
											{inner}
										</div>
									) : (
										<Link
											to={href}
											preload="intent"
											data-testid={`sidebar-subitem-link-${subId}`}
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
						const subItemHref = preserveTimeFilters(baseHref, subItem.url, pathname, search);
						// For query param based subitems, check if tab matches
						const isSubItemActive = subItem.queryParam ? pathname === subItem.url : isRouteMatch(subItem.url);
						const isSubItemHighlighted = highlightedUrl ? subItemHref.startsWith(highlightedUrl) : false;
						const SubItemIcon = subItem.icon;
						const subItemClassName = `h-7 cursor-pointer rounded-sm px-2 transition-all duration-200 ${isSubItemHighlighted
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
							<SidebarMenuSubItem key={subItem.id}>
								{subItem.hasAccess === false ? (
									<SidebarMenuSubButton
										data-nav-url={subItemHref}
										data-testid={`sidebar-subitem-disabled-${subItem.id}`}
										className={subItemClassName}
									>
										{subInner}
									</SidebarMenuSubButton>
								) : (
									<SidebarMenuSubButton asChild className={subItemClassName}>
										<Link
											to={subItemHref}
											preload="intent"
											data-nav-url={subItemHref}
											data-testid={`sidebar-subitem-link-${subItem.id}`}
										>
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
	const { t, i18n } = useTranslation("shell");
	const pathname = useLocation({ select: (l) => l.pathname });
	const search = useLocation({ select: (l) => l.searchStr ?? "" });
	const tsNavigate = useNavigate();
	// Wrapper that accepts arbitrary string URLs (TanStack Router's `to` is
	// strictly typed, but our sidebar items come from a runtime config).
	const navigate = useCallback((url: string) => tsNavigate({ to: url as string }), [tsNavigate]);
	const { state: sidebarState, isMobile, toggleSidebar } = useSidebar();
	const [mounted, setMounted] = useState(false);
	const [expandedItems, setExpandedItems] = useState<Set<string>>(new Set());
	const [searchQuery, setSearchQuery] = useState("");
	const [focusedIndex, setFocusedIndex] = useState(-1);
	const searchInputRef = useRef<HTMLInputElement>(null);
	const [cookies, setCookie, removeCookie] = useCookies([
		PRODUCTION_SETUP_DISMISSED_COOKIE,
		HIDDEN_UNTIL_NAV_COOKIE,
		REMIND_LATER_COOKIE,
		ONBOARDING_CARD_DISMISSED_COOKIE,
	]);
	const isProductionSetupDismissed = !!cookies[PRODUCTION_SETUP_DISMISSED_COOKIE];
	const isOnboardingCardDismissed = !!cookies[ONBOARDING_CARD_DISMISSED_COOKIE];
	const { data: latestRelease } = useGetLatestReleaseQuery(undefined, {
		skip: !mounted, // Only fetch after component is mounted
	});
	const hasLogsAccess = useRbac(RbacResource.Logs, RbacOperation.View);
	const hasObservabilityAccess = useRbac(RbacResource.Observability, RbacOperation.View);
	// Alerting is currently surfaced under the existing governance permission
	// until enterprise alerting gets its own RBAC resource.
	const hasAlertingAccess = useRbac(RbacResource.Governance, RbacOperation.View);
	const hasDashboardAccess = useRbac(RbacResource.Dashboard, RbacOperation.View);
	const hasModelProvidersAccess = useRbac(RbacResource.ModelProvider, RbacOperation.View);
	const hasMCPGatewayAccess = useRbac(RbacResource.MCPGateway, RbacOperation.View);
	const hasVirtualMCPsAccess = useRbac(RbacResource.VirtualMCPs, RbacOperation.View);
	const hasMCPLogsAccess = useRbac(RbacResource.MCPLogs, RbacOperation.View);
	const hasPluginsAccess = useRbac(RbacResource.Plugins, RbacOperation.View);
	const hasUsersAccess = useRbac(RbacResource.Users, RbacOperation.View);
	const hasUserProvisioningAccess = useRbac(RbacResource.UserProvisioning, RbacOperation.View);
	const hasAuditLogsAccess = useRbac(RbacResource.AuditLogs, RbacOperation.View);
	const hasCustomersAccess = useRbac(RbacResource.Customers, RbacOperation.View);
	const hasTeamsAccess = useRbac(RbacResource.Teams, RbacOperation.View);
	const hasBusinessUnitsAccess = useRbac(RbacResource.UserProvisioning, RbacOperation.View);
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
	const envLabel = coreConfig?.env_label ?? null;

	// Same completion logic the floating OnboardingWidget uses — shared so the
	// two surfaces can't disagree on what counts as "done".
	const {
		steps: onboardingSteps,
		skippedIds: onboardingSkippedIds,
		checklistReady: onboardingChecklistReady,
		isDismissedForAll: isOnboardingDismissedForAll,
	} = useOnboardingChecklist({ skip: !isDbConnected });
	const onboardingDoneCount = onboardingSteps.filter((step) => step.complete || onboardingSkippedIds.includes(step.id)).length;
	const isOnboardingIncomplete = onboardingChecklistReady && onboardingDoneCount < onboardingSteps.length;
	// The widget itself hides via these two cookies (X close / "Remind me
	// later"); "I accept the risk - hide for everyone" is a deliberate
	// permanent opt-out and should not resurrect this card.
	const showOnboardingResumeCard =
		isDbConnected &&
		isOnboardingIncomplete &&
		!isOnboardingDismissedForAll &&
		!isOnboardingCardDismissed &&
		(!!cookies[HIDDEN_UNTIL_NAV_COOKIE] || !!cookies[REMIND_LATER_COOKIE]);
	const handleResumeOnboarding = useCallback(() => {
		removeCookie(HIDDEN_UNTIL_NAV_COOKIE, { path: "/" });
		removeCookie(REMIND_LATER_COOKIE, { path: "/" });
	}, [removeCookie]);

	const items = useMemo(
		() => [
			{
				id: "observability",
				title: t("nav.observability"),
				url: "/workspace/logs",
				icon: Telescope,
				hasAccess: hasLogsAccess,
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
				id: "mcp-gateway",
				title: t("nav.mcpGateway"),
				icon: (props: { className?: string }) => <MCPIcon {...props} label={t("sidebar.mcpIconTitle")} />,
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
						id: "governance.users",
						title: t("nav.users"),
						url: "/workspace/governance/users",
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
						icon: BookUser,
						hasAccess: hasUserProvisioningAccess,
					},
					{
						id: "governance.rbac",
						title: t("nav.rolesPermissions"),
						url: "/workspace/governance/rbac",
						icon: UserRoundCheck,
						hasAccess: hasRbacAccess,
					},
					{
						id: "governance.access-profiles",
						title: t("nav.accessProfiles"),
						url: "/workspace/governance/access-profiles",
						icon: ShieldCheck,
						hasAccess: hasAccessProfilesAccess,
					},
					{
						id: "governance.projects",
						title: "Projects",
						url: "/workspace/governance/projects",
						icon: SquareKanban,
						description: "Scope requests to a project's access and budget",
						hasAccess: hasProjectsAccess,
					},
					{
						id: "governance.audit-logs",
						title: t("nav.auditLogs"),
						url: "/workspace/audit-logs",
						icon: ScrollText,
						hasAccess: hasAuditLogsAccess,
					},
				],
			},
			{
				id: "guardrails",
				title: t("nav.guardrails"),
				url: "/workspace/guardrails",
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
				id: "cluster-config",
				title: t("nav.clusterConfig"),
				url: "/workspace/cluster",
				icon: Network,
				hasAccess: hasClusterConfigAccess,
			},
			{
				id: "adaptive-routing",
				title: t("nav.adaptiveRouting"),
				url: "/workspace/adaptive-routing",
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
							hasAccess: hasPromptRepositoryAccess,
						},
						{
							id: "skills-repository",
							title: t("nav.skillsRepository"),
							url: "/workspace/skills-repo",
							icon: BookOpenText,
							hasAccess: hasSkillsRepositoryAccess,
						},
					]
				: []),
			{
				id: "settings",
				title: t("nav.settings"),
				url: "/workspace/config",
				icon: Settings2Icon,
				hasAccess: hasSettingsAccess || hasAuditLogsAccess || hasUserProvisioningAccess,
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
					...(IS_ENTERPRISE
						? [
								{
									id: "settings.proxy",
									title: t("nav.proxy"),
									url: "/workspace/config/proxy",
									icon: Globe,
									hasAccess: hasSettingsAccess,
								},
							]
						: []),
					{
						id: "settings.api-keys",
						title: t("nav.apiKeys"),
						url: "/workspace/config/api-keys",
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
									hasAccess: hasSettingsAccess,
								},
								{
									id: "settings.license",
									title: t("nav.licenseInfo"),
									url: "/workspace/config/license",
									icon: BadgeInfo,
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
			hasVirtualMCPsAccess,
			hasMCPLogsAccess,
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
			hasDevicesAccess,
			hasInventoryAccess,
			hasEdgeConfigAccess,
			hasAnyEdgeControlAccess,
			isDbConnected,
		],
	);

	const accessibleItems: SidebarItem[] = useMemo(() => {
		return items
			.map((item) => {
				const hadSubItems = !!item.subItems?.length;
				if (hadSubItems) {
					const visibleSubItems = item.subItems!.filter((sub) => sub.hasAccess !== false);
					if (visibleSubItems.length === 0) return null;
					return { ...item, subItems: visibleSubItems, hasAccess: true };
				}
				if (item.hasAccess === false) return null;
				return item;
			})
			.filter(Boolean) as SidebarItem[];
	}, [items]);

	const filteredItems: SidebarItem[] = useMemo(() => {
		const query = searchQuery.trim().toLowerCase();
		if (!query) return accessibleItems;

		return accessibleItems
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
	}, [accessibleItems, searchQuery]);

	const { data: version } = useGetVersionQuery();
	const { resolvedTheme } = useTheme();
	const showNewReleaseBanner = useMemo(() => {
		if (IS_ENTERPRISE) return false;
		if (latestRelease && version) {
			return compareVersions(latestRelease.name, version) > 0;
		}
		return false;
	}, [latestRelease, version]);

	useEffect(() => {
		setMounted(true);
	}, []);

	// Auto-expand items when their subitems are active
	useEffect(() => {
		const newExpandedItems = new Set<string>();
		const isRouteMatch = (url: string) => {
			if (url === "/workspace/custom-pricing" || url === "/workspace/adaptive-routing") return pathname === url;
			return pathname.startsWith(url);
		};
		items.forEach((item) => {
			if (item.subItems?.some((subItem) => isRouteMatch(subItem.url))) {
				newExpandedItems.add(item.id);
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
				toExpand.add(item.id);
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
				if (searchQuery.trim() || expandedItems.has(item.id)) {
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

	// Always render the light theme version for SSR to avoid hydration mismatch.
	// On a custom branding deployment useBranding returns the customer's assets
	// instead, which are theme-agnostic.
	const { logoSrc, iconSrc, logoAlt } = useBranding(mounted && resolvedTheme === "dark");

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
				title: t("promo.restartRequired.title"),
				description: (
					<div className="text-xs text-amber-700 dark:text-amber-300/80">
						{coreConfig.restart_required.reason || t("promo.restartRequired.defaultReason")}
					</div>
				),
				dismissible: false,
				variant: "warning" as const,
			});
		}
		// Setup checklist dismissed (X / snoozed) while still incomplete —
		// non-dismissible, same severity tier as restart-required.
		if (showOnboardingResumeCard) {
			const remainingSteps = onboardingSteps.length - onboardingDoneCount;
			cards.push({
				id: "onboarding-incomplete",
				title: t("promo.onboarding.title"),
				description: (
					<div className="flex h-full flex-col gap-2 text-xs text-amber-700 dark:text-amber-300/80">
						<p>{t("promo.onboarding.stepsLeft", { count: remainingSteps })}</p>
						<button
							type="button"
							onClick={handleResumeOnboarding}
							data-testid="onboarding-resume-btn"
							className="text-primary mt-auto self-start pb-1 font-medium underline"
						>
							{t("promo.onboarding.resume")}
						</button>
					</div>
				),
				dismissible: true,
				variant: "warning" as const,
			});
		}
		if (showNewReleaseBanner && latestRelease) {
			cards.push({
				id: "new-release",
				title: t("promo.newRelease.available", { name: latestRelease.name }),
				description: (
					<div className="flex h-full flex-col gap-2">
						<img src={newReleaseImage} alt={t("promo.newRelease.imageAlt")} className="h-[95px] rounded-md object-cover" />
						<a
							href={`https://docs.getbifrost.ai/changelogs/${latestRelease.name}`}
							target="_blank"
							rel="noopener noreferrer"
							className="text-primary mt-auto pb-1 font-medium underline"
						>
							{t("promo.newRelease.viewNotes")}
						</a>
					</div>
				),
				dismissible: true,
			});
		}
		// Only show after mounted to ensure cookie is properly hydrated and avoid flash
		if (!IS_ENTERPRISE && mounted && !isProductionSetupDismissed) {
			cards.push({
				id: "production-setup",
				title: t("promo.productionSetup.title"),
				description: (
					<>
						{t("promo.productionSetup.body")}
						<br />
						<br />
						{t("promo.productionSetup.bookPrefix")}{" "}
						<a
							href="https://calendly.com/maximai/bifrost-demo?utm_source=bfd_sdbr"
							target="_blank"
							className="text-primary font-medium underline"
							rel="noopener noreferrer"
						>
							{t("promo.productionSetup.here")}
						</a>
						.
					</>
				),
				dismissible: true,
			});
		}
		return cards;
	}, [
		t,
		i18n.language,
		coreConfig?.restart_required,
		showNewReleaseBanner,
		latestRelease,
		newReleaseImage,
		isProductionSetupDismissed,
		mounted,
		showOnboardingResumeCard,
		onboardingSteps.length,
		onboardingDoneCount,
		handleResumeOnboarding,
	]);

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
			if (cardId === "onboarding-incomplete") {
				// If the widget itself is snoozed via "Remind me later", align the
				// card's dismissal to that same date — otherwise the card would
				// keep nagging on its own 1-day clock while the widget stays quiet
				// for the full snooze period. Falls back to 1 day when the card is
				// only up because of the widget's X close (no snooze date to match).
				const remindAt = cookies[REMIND_LATER_COOKIE];
				const remindAtDate = remindAt ? new Date(remindAt) : null;
				const expiryDate = remindAtDate && !Number.isNaN(remindAtDate.getTime()) ? remindAtDate : new Date();
				if (!remindAtDate || Number.isNaN(remindAtDate.getTime())) {
					expiryDate.setDate(expiryDate.getDate() + 1);
				}
				setCookie(ONBOARDING_CARD_DISMISSED_COOKIE, "true", {
					path: "/",
					expires: expiryDate,
				});
			}
		},
		[setCookie, cookies],
	);

	return (
		<Sidebar collapsible="icon" className="overflow-y-clip border-none bg-transparent">
			<SidebarHeader className="mt-1 ml-2 flex justify-between px-0 group-data-[collapsible=icon]:ml-0 group-data-[collapsible=icon]:h-auto">
				{/* Expanded state: horizontal layout */}
				<div className="flex h-8 w-full items-center justify-between px-1.5 group-data-[collapsible=icon]:hidden">
					<Link to="/workspace/logs" className="group flex items-center gap-2 pl-2">
						{/* max-w caps an unusually wide uploaded logo so it cannot push the
						    collapse button out of the header; object-contain preserves its
						    aspect ratio within that box. */}
						<img className="h-[22px] w-auto max-w-[150px] object-contain" src={logoSrc} alt={logoAlt} width={70} height={70} />
					</Link>
					<button
						onClick={toggleSidebar}
						type="button"
						data-testid="sidebar-collapse-btn"
						className="text-muted-foreground hover:text-foreground hover:bg-sidebar-accent flex h-7 w-7 items-center justify-center rounded-md transition-colors"
						aria-label={t("sidebar.collapse")}
					>
						<PanelLeftClose className="h-4 w-4" />
					</button>
				</div>
				{/* Collapsed state: vertical layout */}
				<div
					className="hidden w-full cursor-pointer flex-col items-center gap-2 py-1 group-data-[collapsible=icon]:flex"
					onClick={toggleSidebar}
				>
					<img className="size-[22px] object-contain" src={iconSrc} alt={logoAlt} width={22} height={22} />
				</div>
			</SidebarHeader>
			{envLabel && (
				<div className="mx-2 -mt-1 mb-2">
					{/* Expanded: full label text */}
					<div className="flex items-center justify-center rounded-sm bg-amber-400/20 px-2 py-1 group-data-[collapsible=icon]:hidden">
						<span className="font-mono text-[10px] font-semibold tracking-widest text-amber-700 dark:text-amber-400">{envLabel}</span>
					</div>
					{/* Collapsed: dot indicator */}
					<div className="hidden justify-center group-data-[collapsible=icon]:flex">
						<Tooltip>
							<TooltipTrigger asChild>
								<div className="h-1.5 w-1.5 rounded-full bg-amber-500 dark:bg-amber-400" />
							</TooltipTrigger>
							<TooltipContent side="right">{envLabel}</TooltipContent>
						</Tooltip>
					</div>
				</div>
			)}
			<div className="mr-3 ml-2 pb-1 group-data-[collapsible=icon]:hidden">
				<div className="dark:bg-card relative rounded-sm bg-white">
					<Search className="text-muted-foreground absolute top-1/2 left-2.5 h-3.5 w-3.5 -translate-y-1/2" />
					<input
						ref={searchInputRef}
						type="text"
						aria-label={t("search.ariaLabel")}
						placeholder={t("search.placeholder")}
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
			<SidebarContent className="overflow-hidden">
				<SidebarGroup className="custom-scrollbar min-h-0 flex-1 overflow-y-auto pr-3 pt-0.5">
					<SidebarGroupContent>
						<SidebarMenu className="space-y-0.5">
							{filteredItems.map((item) => {
								const isActive = isActiveRoute(item.url);

								const highlightedUrl = focusedIndex >= 0 ? navigableItems[focusedIndex]?.url : undefined;
								return (
									<SidebarItemView
										key={item.id}
										item={item}
										isActive={isActive}
										isExternal={item.isExternal ?? false}
										isWebSocketConnected={isWebSocketConnected}
										isExpanded={expandedItems.has(item.id)}
										onToggle={() => toggleItem(item.id)}
										pathname={pathname}
										search={search}
										isSidebarCollapsed={!isMobile && sidebarState === "collapsed"}
										expandSidebar={() => toggleSidebar()}
										highlightedUrl={highlightedUrl}
									/>
								);
							})}
						</SidebarMenu>
					</SidebarGroupContent>
				</SidebarGroup>
				<div className="mt-auto flex flex-col gap-4 px-3 pb-3.5 group-data-[collapsible=icon]:px-1">
					<div className="mx-1 group-data-[collapsible=icon]:hidden">
						<PromoCardStack cards={promoCards} onDismiss={handlePromoDismiss} />
					</div>
					{/* Socials, theme toggle, the user/logout menu and the version string
					    all moved to <Topbar>. All that remains here is the expand
					    affordance for the collapsed rail, since the collapsed header
					    doubles as the collapse target. */}
					<div className="hidden w-full cursor-pointer flex-col items-center group-data-[collapsible=icon]:flex">
						<button
							onClick={toggleSidebar}
							type="button"
							data-testid="sidebar-expand-btn"
							className="text-muted-foreground hover:text-foreground hover:bg-sidebar-accent flex cursor-pointer items-center justify-center rounded-md transition-colors"
							aria-label={t("sidebar.expand")}
						>
							<PanelLeftOpen className="h-4 w-4" />
						</button>
					</div>
				</div>
			</SidebarContent>
			<PoweredByBifrost />
		</Sidebar>
	);
}