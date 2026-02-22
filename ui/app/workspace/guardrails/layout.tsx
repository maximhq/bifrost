"use client";

import { NoPermissionView } from "@/components/noPermissionView";
import { IS_ENTERPRISE } from "@/lib/constants/config";
import { cn } from "@/lib/utils";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Boxes, ScrollText } from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useRef, useState } from "react";

const tabs = [
	{
		label: "Rules",
		href: "/workspace/guardrails",
		icon: ScrollText,
		testId: "guardrails-tab-rules",
		resource: RbacResource.GuardrailsConfig,
	},
	{
		label: "Providers",
		href: "/workspace/guardrails/providers",
		icon: Boxes,
		testId: "guardrails-tab-providers",
		resource: RbacResource.GuardrailsProviders,
	},
];

export default function GuardrailsLayout({ children }: { children: React.ReactNode }) {
	const hasGuardrailsConfigAccess = useRbac(RbacResource.GuardrailsConfig, RbacOperation.View);
	const hasGuardrailsProvidersAccess = useRbac(RbacResource.GuardrailsProviders, RbacOperation.View);
	const hasGuardrailsAccess = hasGuardrailsConfigAccess || hasGuardrailsProvidersAccess;
	const pathname = usePathname();
	const headerRef = useRef<HTMLDivElement>(null);
	const tabRefs = useRef<(HTMLAnchorElement | null)[]>([]);
	const [indicatorStyle, setIndicatorStyle] = useState({ left: 0, width: 0 });

	const rbacMap: Record<string, boolean> = {
		[RbacResource.GuardrailsConfig]: hasGuardrailsConfigAccess,
		[RbacResource.GuardrailsProviders]: hasGuardrailsProvidersAccess,
	};

	const visibleTabs = IS_ENTERPRISE ? tabs.filter((tab) => rbacMap[tab.resource]) : tabs;

	const path = pathname.replace(/\/$/, "") || "/";
	const isRoot = path === "/workspace/guardrails";
	const isConfiguration = path === "/workspace/guardrails/configuration" || path.startsWith("/workspace/guardrails/configuration/");

	let activeIndex = visibleTabs.findIndex((tab) =>
		tab.href === "/workspace/guardrails" ? isRoot || isConfiguration : path === tab.href || path.startsWith(tab.href + "/"),
	);
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
	}, [activeIndex, pathname]);

	if (IS_ENTERPRISE && !hasGuardrailsAccess) {
		return <NoPermissionView entity="guardrails configuration" />;
	}

	return (
		<div className="flex h-full w-full flex-col">
			<div ref={headerRef} className="border-border relative w-full border-b">
				<div className="flex h-10 w-full items-center gap-2 pb-3">
					<div className="relative flex h-full items-center gap-1">
						{visibleTabs.map((tab, i) => {
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
				</div>
				<span
					className="bg-primary absolute bottom-0 left-0 h-0.5 transition-[transform,width] duration-200 ease-out will-change-transform"
					style={{ width: indicatorStyle.width, transform: `translateX(${indicatorStyle.left}px)` }}
					aria-hidden
				/>
			</div>
			<div className="min-h-0 flex-1">{children}</div>
		</div>
	);
}
