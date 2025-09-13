"use client";

import { BoxIcon, BugIcon, Puzzle, Settings2Icon, Shield, Telescope } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import {
	Sidebar,
	SidebarContent,
	SidebarFooter,
	SidebarGroup,
	SidebarGroupContent,
	SidebarHeader,
	SidebarMenu,
	SidebarMenuButton,
	SidebarMenuItem,
} from "@/components/ui/sidebar";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { useWebSocket } from "@/hooks/useWebSocket";
import { useGetCoreConfigQuery, useGetVersionQuery } from "@/lib/store";
import { cn } from "@/lib/utils";
import { BooksIcon, DiscordLogoIcon, GithubLogoIcon } from "@phosphor-icons/react";
import { useTheme } from "next-themes";
import Image from "next/image";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useState } from "react";
import { ThemeToggle } from "./themeToggle";
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
const navigationItems = [
	{
		title: "Logs",
		url: "/logs",
		icon: Telescope,
		description: "Request logs & monitoring",
	},
	{
		title: "Providers",
		url: "/providers",
		icon: BoxIcon,
		description: "Configure models",
	},
	{
		title: "MCP clients",
		url: "/mcp-clients",
		icon: MCPIcon,
		description: "MCP configuration",
	},
	{
		title: "Governance",
		url: "/governance",
		icon: Shield,
		description: "Manage virtual keys & access",
	},
	{
		title: "Config",
		url: "/config",
		icon: Settings2Icon,
		description: "Bifrost settings",
	},
	{
		title: "Plugins",
		url: "/plugins",
		icon: Puzzle,
		description: "Extend Bifrost functionality",
		badge: "Beta",
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
		url: "https://getmaxim.ai/bifrost/docs",
		icon: BooksIcon,
		strokeWidth: 1,
	},
];

// Promotional cards configuration
const promoCards = [
	{
		title: "Need help with production setup?",
		description: (
			<>
				We offer help with production setup including custom integrations and dedicated support.
				<br />
				<br />
				Book a demo with our team{" "}
				<Link href="https://calendly.com/maximai/bifrost-demo" target="_blank" className="text-primary" rel="noopener noreferrer">
					here
				</Link>
				.
			</>
		),
	},
];

export default function AppSidebar() {
	const pathname = usePathname();
	const [mounted, setMounted] = useState(false);
	const { data: version } = useGetVersionQuery();
	const { resolvedTheme } = useTheme();

	// Get governance config from RTK Query
	const { data: coreConfig } = useGetCoreConfigQuery({});
	const isGovernanceEnabled = coreConfig?.client_config.enable_governance || false;

	useEffect(() => {
		setMounted(true);
	}, []);

	const isActiveRoute = (url: string) => {
		if (url === "/" && pathname === "/") return true;
		if (url !== "/" && pathname.startsWith(url)) return true;
		return false;
	};

	// Always render the light theme version for SSR to avoid hydration mismatch
	const logoSrc = mounted && resolvedTheme === "dark" ? "/bifrost-logo-dark.png" : "/bifrost-logo.png";

	const { isConnected: isWebSocketConnected } = useWebSocket();

	return (
		<Sidebar className="custom-scrollbar border-none bg-transparent">
			<SidebarHeader className="mt-1 ml-2 flex h-12 justify-between px-0">
				<div className="flex h-full items-center justify-between gap-2 px-1.5">
					<Link href="/" className="group flex items-center gap-2">
						<Image className="h-10 w-auto" src={logoSrc} alt="Bifrost" width={100} height={100} />
					</Link>
				</div>
			</SidebarHeader>

			<SidebarContent>
				<SidebarGroup>
					<SidebarGroupContent>
						<SidebarMenu className="space-y-1">
							{navigationItems.map((item) => {
								const isActive = isActiveRoute(item.url);
								const isAllowed = item.title === "Governance" ? isGovernanceEnabled : true;
								return (
									<TooltipProvider key={item.title}>
										<Tooltip>
											<TooltipTrigger>
												<SidebarMenuItem>
													<SidebarMenuButton
														asChild
														className={`relative h-8 rounded-md border px-3 transition-all duration-200 ${
															isActive
																? "bg-sidebar-accent text-primary border-primary/20"
																: isAllowed
																	? "hover:bg-sidebar-accent hover:text-accent-foreground border-transparent text-slate-500 dark:text-zinc-400"
																	: "hover:bg-destructive/5 hover:text-muted-foreground text-muted-foreground cursor-default border-transparent"
														} `}
													>
														<Link href={isAllowed ? item.url : "#"} className="flex w-full items-center justify-between">
															<div>
																<div className="hover:text-accent-foreground flex items-center gap-2">
																	<item.icon className={`h-4 w-4 ${isActive ? "text-primary" : "text-muted-foreground"}`} />
																	<span className={`text-sm ${isActive ? "font-medium" : "font-normal"}`}>{item.title}</span>
																</div>
															</div>
															{item.url === "/" && isWebSocketConnected && (
																<div className="h-2 w-2 animate-pulse rounded-full bg-green-800 dark:bg-green-200" />
															)}
															{item.badge && (
																<Badge
																	variant={item.badge === "Live" ? "default" : "outline"}
																	className={cn("h-5 px-2 py-0.5 text-xs", item.badge === "Live" && "animate-pulse duration-200")}
																>
																	{item.badge}
																</Badge>
															)}
														</Link>
													</SidebarMenuButton>
												</SidebarMenuItem>
											</TooltipTrigger>
											{!isAllowed && <TooltipContent side="right">Please enable governance in the config page</TooltipContent>}
										</Tooltip>
									</TooltipProvider>
								);
							})}
						</SidebarMenu>
					</SidebarGroupContent>
				</SidebarGroup>
				<div className="mt-auto flex flex-col gap-4 px-3">
					<div className="mx-1">
						<PromoCardStack cards={promoCards} />
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
						</div>
					</div>
					<div className="mx-auto font-mono text-xs">{version ?? ""}</div>
				</div>
			</SidebarContent>

			<SidebarFooter className="px-6 py-3"></SidebarFooter>
		</Sidebar>
	);
}
