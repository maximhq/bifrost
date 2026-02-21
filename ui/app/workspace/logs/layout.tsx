"use client";

import { ObservabilityConnectorsSheet } from "@/app/workspace/logs/sheets/observabilityConnectorsSheet";
import { ObservabilitySettingsSheet } from "@/app/workspace/logs/sheets/observabilitySettingsSheet";
import FullPageLoader from "@/components/fullPageLoader";
import { LoggingDisabledView } from "@/components/loggingDisabledView";
import { NoPermissionView } from "@/components/noPermissionView";
import { Button } from "@/components/ui/button";
import { useGetCoreConfigQuery } from "@/lib/store";
import { cn } from "@/lib/utils";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { ChartColumnBig, Logs, Server, Settings } from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useRef, useState } from "react";

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
		aria-label="MCP"
	>
		<path d="M15.688 2.343a2.588 2.588 0 00-3.61 0l-9.626 9.44a.863.863 0 01-1.203 0 .823.823 0 010-1.18l9.626-9.44a4.313 4.313 0 016.016 0 4.116 4.116 0 011.204 3.54 4.3 4.3 0 013.609 1.18l.05.05a4.115 4.115 0 010 5.9l-8.706 8.537a.274.274 0 000 .393l1.788 1.754a.823.823 0 010 1.18.863.863 0 01-1.203 0l-1.788-1.753a1.92 1.92 0 010-2.754l8.706-8.538a2.47 2.47 0 000-3.54l-.05-.049a2.588 2.588 0 00-3.607-.003l-7.172 7.034-.002.002-.098.097a.863.863 0 01-1.204 0 .823.823 0 010-1.18l7.273-7.133a2.47 2.47 0 00-.003-3.537z" />
		<path d="M14.485 4.703a.823.823 0 000-1.18.863.863 0 00-1.204 0l-7.119 6.982a4.115 4.115 0 000 5.9 4.314 4.314 0 006.016 0l7.12-6.982a.823.823 0 000-1.18.863.863 0 00-1.204 0l-7.119 6.982a2.588 2.588 0 01-3.61 0 2.47 2.47 0 010-3.54l7.12-6.982z" />
	</svg>
);

const tabs = [
	{ label: "Overview", href: "/workspace/logs/dashboard", icon: ChartColumnBig, testId: "logs-tab-overview" },
	{ label: "LLM Logs", href: "/workspace/logs", icon: Logs, testId: "logs-tab-llm" },
	{ label: "MCP Logs", href: "/workspace/logs/mcp-logs", icon: MCPIcon, testId: "logs-tab-mcp" },
];

export default function LogsLayout({ children }: { children: React.ReactNode }) {
	const hasViewLogsAccess = useRbac(RbacResource.Logs, RbacOperation.View);
	const hasObservabilityAccess = useRbac(RbacResource.Observability, RbacOperation.View);
	const hasSettingsAccess = useRbac(RbacResource.Settings, RbacOperation.View);
	const { data: bifrostConfig, isLoading } = useGetCoreConfigQuery({ fromDB: true });
	const loggingEnabled = bifrostConfig?.client_config?.enable_logging ?? false;
	const pathname = usePathname();
	const [settingsSheetOpen, setSettingsSheetOpen] = useState(false);
	const [connectorsSheetOpen, setConnectorsSheetOpen] = useState(false);
	const headerRef = useRef<HTMLDivElement>(null);
	const tabRefs = useRef<(HTMLAnchorElement | null)[]>([]);
	const [indicatorStyle, setIndicatorStyle] = useState({ left: 0, width: 0 });

	const path = pathname.replace(/\/$/, "") || "/";
	const isLogsRoot = path === "/workspace/logs";
	const isLogsDashboard = path === "/workspace/logs/dashboard" || path.startsWith("/workspace/logs/dashboard/");

	let activeIndex = tabs.findIndex((tab) =>
		tab.href === "/workspace/logs" ? isLogsRoot : path === tab.href || path.startsWith(tab.href + "/"),
	);
	if (activeIndex === -1 && isLogsRoot) activeIndex = 1;
	if (activeIndex === -1 && isLogsDashboard) activeIndex = 0;
	if (activeIndex === -1) activeIndex = 0;

	useEffect(() => {
		const header = headerRef.current;
		const el = activeIndex >= 0 ? tabRefs.current[activeIndex] : null;
		const updateIndicator = () => {
			if (header && el) {
				const headerRect = header.getBoundingClientRect();
				const tabRect = el.getBoundingClientRect();
				setIndicatorStyle({
					left: tabRect.left - headerRect.left,
					width: tabRect.width,
				});
			}
		};
		updateIndicator();
		const raf = requestAnimationFrame(updateIndicator);
		return () => cancelAnimationFrame(raf);
	}, [activeIndex, pathname, isLoading, loggingEnabled, hasViewLogsAccess]);

	if (!hasViewLogsAccess) {
		return <NoPermissionView entity="logs" />;
	}

	if (isLoading) {
		return <FullPageLoader />;
	}

	if (!loggingEnabled) {
		return <LoggingDisabledView />;
	}

	return (
		<div className="flex h-full w-full flex-col">
			<div ref={headerRef} className="border-border relative mb-7 w-full border-b">
				<div className="flex h-10 w-full items-center justify-between gap-2 pb-3">
					<div className="relative flex h-full items-center gap-1">
						{tabs.map((tab, i) => {
							const isActive = i === activeIndex;
							return (
								<Link
									key={tab.href}
									ref={(el) => {
										tabRefs.current[i] = el;
									}}
									href={tab.href}
									data-testid={tab.testId}
									className={cn(
										"focus-visible:ring-ring inline-flex cursor-pointer items-center justify-center gap-1.5 px-5 py-2.5 text-sm font-medium transition-colors focus-visible:ring-2 focus-visible:outline-none disabled:pointer-events-none disabled:opacity-50",
										isActive ? "text-foreground" : "text-muted-foreground hover:text-foreground",
									)}
								>
									<tab.icon className="size-4" />
									{tab.label}
								</Link>
							);
						})}
					</div>
					<div className="flex h-full items-center gap-2">
						{hasSettingsAccess && (
							<Button
								data-testid="logs-settings-button"
								variant="outline"
								size="sm"
								className="h-8"
								onClick={() => setSettingsSheetOpen(true)}
							>
								<Settings className="size-4" />
								Settings
							</Button>
						)}
						{hasObservabilityAccess && (
							<Button
								data-testid="logs-connectors-button"
								variant="outline"
								size="sm"
								className="h-8"
								onClick={() => setConnectorsSheetOpen(true)}
							>
								<Server className="size-4" />
								Connectors
							</Button>
						)}
					</div>
				</div>
				<span
					className="bg-primary absolute bottom-0 left-0 h-0.5 transition-[transform,width] duration-200 ease-out will-change-transform"
					style={{ width: indicatorStyle.width, transform: `translateX(${indicatorStyle.left}px)` }}
					aria-hidden
				/>
			</div>
			<div className="min-h-0 flex-1">{children}</div>
			{hasSettingsAccess && <ObservabilitySettingsSheet open={settingsSheetOpen} onOpenChange={setSettingsSheetOpen} />}
			{hasObservabilityAccess && <ObservabilityConnectorsSheet open={connectorsSheetOpen} onOpenChange={setConnectorsSheetOpen} />}
		</div>
	);
}
