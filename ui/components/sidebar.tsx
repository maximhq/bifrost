"use client";

import {
	BookUser,
	Boxes,
	BoxIcon,
	BugIcon,
	ChevronsLeftRightEllipsis,
	Cog,
	Construction,
	FolderGit,
	KeyRound,
	Landmark,
	Layers,
	LogOut,
	Logs,
	Puzzle,
	ScrollText,
	Settings2Icon,
	Shuffle,
	Telescope,
	Users
} from "lucide-react";

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
} from "@/components/ui/sidebar";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { useWebSocket } from "@/hooks/useWebSocket";
import { IS_ENTERPRISE } from "@/lib/constants/config";
import { useGetCoreConfigQuery, useGetLatestReleaseQuery, useGetVersionQuery } from "@/lib/store";
import { BooksIcon, DiscordLogoIcon, GithubLogoIcon } from "@phosphor-icons/react";
import { ChevronRight } from "lucide-react";
import { useTheme } from "next-themes";
import Image from "next/image";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useEffect, useMemo, useState } from "react";
import { ThemeToggle } from "./themeToggle";
import { Badge } from "./ui/badge";
import { PromoCardStack } from "./ui/promoCardStack";

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
const items = [
	{
		title: "Observability",
		url: "/workspace/logs",
		icon: Telescope,
		description: "Request logs & monitoring",
		subItems: [
			{
				title: "Logs",
				url: "/workspace/logs",
				icon: Logs,
				description: "Request logs & monitoring",
			},
			{
				title: "Connectors",
				url: "/workspace/observability",
				icon: ChevronsLeftRightEllipsis,
				description: "Log connectors",
			},
		],
	},
	{
		title: "Prompt Repository",
		url: "/workspace/prompt-repo",
		icon: FolderGit,
		description: "Prompt repository",
	},
	{
		title: "Model Providers",
		url: "/workspace/providers",
		icon: BoxIcon,
		description: "Configure models",
	},
	{
		title: "Governance",
		url: "/workspace/governance",
		icon: Landmark,
		description: "Govern access",
		subItems: [
			{
				title: "Virtual Keys",
				url: "/workspace/virtual-keys",
				icon: KeyRound,
				description: "Manage virtual keys & access",
			},
			{
				title: "Users & Groups",
				url: "/workspace/user-groups",
				icon: Users,
				description: "Manage users & groups",
			},
			{
				title: "User Provisioning",
				url: "/workspace/scim",
				icon: BookUser,
				description: "User management and provisioning",
			},
			{
				title: "Audit Logs",
				url: "/workspace/audit-logs",
				icon: ScrollText,
				description: "Audit logs and compliance",
			},
		],
	},

	{
		title: "Plugins",
		url: "/workspace/plugins",
		icon: Puzzle,		
		tag: "BETA",
		description: "Manage custom plugins",
	},

	{
		title: "MCP Gateway",
		url: "/workspace/mcp-clients",
		icon: MCPIcon,
		description: "MCP configuration",
	},
	{
		title: "Guardrails",
		url: "/workspace/guardrails",
		icon: Construction,
		description: "Guardrails configuration",
		subItems: [
			{
				title: "Configuration",
				url: "/workspace/guardrails/configuration",
				icon: Cog,
				description: "Guardrail configuration",
			},
			{
				title: "Providers",
				url: "/workspace/guardrails/providers",
				icon: Boxes,
				description: "Guardrail providers configuration",
			},
		],
	},
	{
		title: "Cluster Config",
		url: "/workspace/cluster",
		icon: Layers,
		description: "Manage Bifrost cluster",
	},
	{
		title: "Adaptive Routing",
		url: "/workspace/adaptive-routing",
		icon: Shuffle,
		description: "Manage adaptive load balancer",
	},
	{
		title: "Config",
		url: "/workspace/config",
		icon: Settings2Icon,
		description: "Bifrost settings",
	},
];

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
	dismissible: false,
};

const SidebarItem = ({
	item,
	isActive,
	isAllowed,
	isWebSocketConnected,
	isExpanded,
	onToggle,
	pathname,
	router,
}: {
	item: (typeof items)[0];
	isActive: boolean;
	isAllowed: boolean;
	isWebSocketConnected: boolean;
	isExpanded?: boolean;
	onToggle?: () => void;
	pathname: string;
	router: ReturnType<typeof useRouter>;
}) => {
	const hasSubItems = "subItems" in item && item.subItems && item.subItems.length > 0;
	const isAnySubItemActive = hasSubItems && item.subItems?.some((subItem) => pathname.startsWith(subItem.url));

	const handleClick = (e: React.MouseEvent) => {
		if (hasSubItems && onToggle) {
			e.preventDefault();
			onToggle();
		}
	};

	const handleNavigation = (url: string) => {
		if (!isAllowed) return;
		router.push(url);
	};

	const handleSubItemClick = (url: string) => {
		router.push(url);
	};

	return (
		<SidebarMenuItem key={item.title}>
			<TooltipProvider>
				<Tooltip>
					<TooltipTrigger asChild>
						<SidebarMenuButton
							className={`relative h-7.5 cursor-pointer rounded-md border px-3 transition-all duration-200 ${
								isActive || isAnySubItemActive
									? "bg-sidebar-accent text-primary border-primary/20"
									: isAllowed
										? "hover:bg-sidebar-accent hover:text-accent-foreground border-transparent text-slate-500 dark:text-zinc-400"
										: "hover:bg-destructive/5 hover:text-muted-foreground text-muted-foreground cursor-default border-transparent"
							} `}
							onClick={hasSubItems ? handleClick : () => handleNavigation(item.url)}
						>
							<div className="flex w-full items-center justify-between">
								<div className="flex items-center gap-2 w-full">
									<item.icon className={`h-4 w-4 ${isActive || isAnySubItemActive ? "text-primary" : "text-muted-foreground"}`} />
									<span className={`text-sm ${isActive || isAnySubItemActive ? "font-medium" : "font-normal"}`}>{item.title}</span>
									{item.tag && <Badge variant="secondary" className="text-xs text-muted-foreground ml-auto">{item.tag}</Badge>}
								</div>
								{hasSubItems && <ChevronRight className={`h-4 w-4 transition-transform duration-200 ${isExpanded ? "rotate-90" : ""}`} />}
								{!hasSubItems && item.url === "/logs" && isWebSocketConnected && (
									<div className="h-2 w-2 animate-pulse rounded-full bg-green-800 dark:bg-green-200" />
								)}
							</div>
						</SidebarMenuButton>
					</TooltipTrigger>
					{!isAllowed && <TooltipContent side="right">Please enable governance in the config page</TooltipContent>}
				</Tooltip>
			</TooltipProvider>
			{hasSubItems && isExpanded && (
				<SidebarMenuSub className="border-sidebar-border mt-1 ml-4 space-y-0.5 border-l pl-2">
					{item.subItems?.map((subItem: any) => {
						const isSubItemActive = pathname.startsWith(subItem.url);
						const SubItemIcon = subItem.icon;
						return (
							<SidebarMenuSubItem key={subItem.title}>
								<SidebarMenuSubButton
									className={`h-7 cursor-pointer rounded-md px-2 transition-all duration-200 ${
										isSubItemActive
											? "bg-sidebar-accent text-primary font-medium"
											: "hover:bg-sidebar-accent hover:text-accent-foreground text-slate-500 dark:text-zinc-400"
									}`}
									onClick={() => handleSubItemClick(subItem.url)}
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
	const { data: latestRelease } = useGetLatestReleaseQuery(undefined, {
		skip: !mounted, // Only fetch after component is mounted
	});
	const { data: version } = useGetVersionQuery();
	const { resolvedTheme } = useTheme();
	const showNewReleaseBanner = useMemo(() => {
		if (latestRelease && version) {
			return compareVersions(latestRelease.name, version) > 0;
		}
		return false;
	}, [latestRelease, version]);

	// Get governance config from RTK Query
	const { data: coreConfig } = useGetCoreConfigQuery({});
	const isGovernanceEnabled = coreConfig?.client_config.enable_governance || false;

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

	const { isConnected: isWebSocketConnected } = useWebSocket();

	// Memoize promo cards array to prevent duplicates and unnecessary re-renders
	const promoCards = useMemo(() => {
		const cards = [];
		if (!IS_ENTERPRISE) {
			cards.push(productionSetupHelpCard);
		}
		if (showNewReleaseBanner && latestRelease) {
			cards.push({
				id: "new-release",
				title: `${latestRelease.name} is now available.`,
				description: (
					<div className="flex h-full flex-col gap-2">
						<img src={`/images/new-release-image.png`} alt="Bifrost" className="h-[95px] object-cover" />
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
		return cards;
	}, [showNewReleaseBanner, latestRelease]);

	// Reset areCardsEmpty when promoCards changes
	useEffect(() => {
		if (promoCards.length > 0) {
			setAreCardsEmpty(false);
		}
	}, [promoCards]);

	const hasPromoCards = promoCards.length > 0 && !areCardsEmpty;
	// When cards are present: 13rem (header 3rem + bottom section ~10rem)
	// When no cards: 8rem (header 3rem + bottom section without cards ~5rem)
	const sidebarGroupHeight = hasPromoCards ? 'h-[calc(100vh-13rem)]' : 'h-[calc(100vh-8rem)]';

	const handleCardsEmpty = () => {
		setAreCardsEmpty(true);
	};

	return (
		<Sidebar className="overflow-y-clip border-none bg-transparent">
			<SidebarHeader className="mt-1 ml-2 flex h-12 justify-between px-0">
				<div className="flex h-full items-center justify-between gap-2 px-1.5">
					<Link href="/" className="group flex items-center gap-2">
						<Image className="h-10 w-auto" src={logoSrc} alt="Bifrost" width={100} height={100} />
					</Link>
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
									<SidebarItem
										key={item.title}
										item={item}
										isActive={isActive}
										isAllowed={isAllowed}
										isWebSocketConnected={isWebSocketConnected}
										isExpanded={expandedItems.has(item.title)}
										onToggle={() => toggleItem(item.title)}
										pathname={pathname}
										router={router}
									/>
								);
							})}
						</SidebarMenu>
					</SidebarGroupContent>
				</SidebarGroup>
				<div className="flex flex-col gap-4 px-3">
					<div className="mx-1">
						<PromoCardStack cards={promoCards} onCardsEmpty={handleCardsEmpty} />
					</div>
					<div className="flex flex-row">
						<div className="mx-auto flex flex-row gap-4">
							{externalLinks.map((item, index) => (
								<a
									key={index}
									href={item.url}
									target="_blank"
									rel="noopener noreferrer"
									className="group flex w-full items-center justify-between"
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
							{IS_ENTERPRISE && (
								<div>
									<div
										className="flex items-center space-x-3"
										onClick={() => {
											window.location.href = "/api/logout";
										}}
									>
										<LogOut className="hover:text-primary text-muted-foreground h-4.5 w-4.5" size={20} strokeWidth={1.5} />
									</div>
								</div>
							)}
						</div>
					</div>
					<div className="mx-auto font-mono text-xs">{version ?? ""}</div>
				</div>
			</SidebarContent>
		</Sidebar>
	);
}
