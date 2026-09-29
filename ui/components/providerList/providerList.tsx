// A provider list for a cell too narrow to hold it: as many chips as `lines` rows fit, then a "+N"
// chip whose tooltip names the rest. Chip widths come off a hidden mirror, so labelled chips of
// different lengths pack correctly, and a ResizeObserver re-fits the row on resize.

import { Badge } from "@/components/ui/badge";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { ProviderIcons, ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";
import { cn } from "@/lib/utils";
import { ComponentProps, ReactNode, useLayoutEffect, useRef, useState } from "react";
import { fitCount, measureKey, uniqueProviders } from "./utils";

const GAP_PX = 4; // gap-1

export interface ProviderListProps {
	providers: string[];
	/** "icon" renders a tooltipped logo square; "label" renders a badge with logo and name. */
	variant?: "icon" | "label";
	/** Rows of chips shown before the overflow chip. */
	lines?: number;
	/** Hard cap on visible chips. Overrides the measured count. */
	max?: number;
	/** Rendered when `providers` is empty. */
	empty?: ReactNode;
	className?: string;
	/** Applied to the overflow chip, for tests that need to find it. */
	overflowTestId?: string;
}

export function ProviderList({
	providers,
	variant = "icon",
	lines = 2,
	max,
	empty = <span className="text-muted-foreground text-sm">-</span>,
	className,
	overflowTestId,
}: ProviderListProps) {
	const [container, setContainer] = useState<HTMLDivElement | null>(null);
	const mirror = useRef<HTMLDivElement | null>(null);
	const names = uniqueProviders(providers);
	const [visible, setVisible] = useState(names.length);

	const key = measureKey(variant, names);

	useLayoutEffect(() => {
		if (!container || !mirror.current || max !== undefined) return;

		const chips = Array.from(mirror.current.children) as HTMLElement[];
		// Last child is the widest-case overflow chip; the rest mirror the providers.
		const overflowWidth = chips[chips.length - 1]?.offsetWidth ?? 0;
		const widths = chips.slice(0, -1).map((chip) => chip.offsetWidth);

		const fit = () => setVisible(fitCount({ widths, containerWidth: container.clientWidth, lines, overflowWidth, gap: GAP_PX }));

		fit();
		const observer = new ResizeObserver(fit);
		observer.observe(container);
		return () => observer.disconnect();
	}, [container, key, lines, max]);

	if (names.length === 0) return <>{empty}</>;

	const shown = names.slice(0, max ?? visible);
	const hidden = names.slice(shown.length);

	return (
		<div ref={setContainer} className={cn("relative flex w-full flex-wrap items-center gap-1 overflow-hidden", className)}>
			{shown.map((name) => (
				<ProviderChip key={name} name={name} variant={variant} />
			))}
			{hidden.length > 0 && (
				<Tooltip>
					<TooltipTrigger asChild>
						<Badge
							variant="outline"
							className="h-5 shrink-0 px-1.5 font-mono text-xs"
							data-testid={overflowTestId}
							tabIndex={0}
							aria-label={`${hidden.length} more ${hidden.length === 1 ? "provider" : "providers"}`}
						>
							{overflowLabel(hidden.length, variant)}
						</Badge>
					</TooltipTrigger>
					<TooltipContent className="shadow-none">
						<ul className="flex flex-col gap-1">
							{hidden.map((name) => (
								<li key={name} className="flex items-center gap-2">
									<ProviderIcon name={name} />
									<span>{getProviderLabel(name)}</span>
								</li>
							))}
						</ul>
					</TooltipContent>
				</Tooltip>
			)}
			{max === undefined && (
				<div
					ref={mirror}
					aria-hidden
					className="pointer-events-none absolute -z-10 flex items-center gap-1 opacity-0"
					style={{ left: 0, top: 0 }}
				>
					{names.map((name) => (
						<ProviderChip key={name} name={name} variant={variant} measuring />
					))}
					<Badge variant="outline" className="h-5 shrink-0 px-1.5 font-mono text-xs">
						{overflowLabel(names.length, variant)}
					</Badge>
				</div>
			)}
		</div>
	);
}

function overflowLabel(count: number, variant: "icon" | "label") {
	return variant === "label" ? `+${count} more` : `+${count}`;
}

// RenderProviderIcon returns null for providers it has no logo for, which is every custom one.
function hasLogo(name: string) {
	return Object.prototype.hasOwnProperty.call(ProviderIcons, name);
}

function monogram(name: string) {
	return name.trim().charAt(0).toUpperCase() || "?";
}

// Spreads its remaining props and ref onto the span so it can serve as a TooltipTrigger child.
function ProviderIcon({ name, className, ...rest }: { name: string } & ComponentProps<"span">) {
	return (
		<span {...rest} className={cn("bg-secondary inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-sm", className)}>
			{hasLogo(name) ? (
				<RenderProviderIcon provider={name as ProviderIconType} size="sm" />
			) : (
				<span className="text-muted-foreground font-mono text-[10px] leading-none font-medium">{monogram(name)}</span>
			)}
		</span>
	);
}

// `measuring` drops the tooltip: a hidden mirror chip should not be a reachable trigger.
function ProviderChip({ name, variant, measuring }: { name: string; variant: "icon" | "label"; measuring?: boolean }) {
	if (variant === "label") {
		return (
			<Badge variant="secondary" className="h-5 shrink-0 gap-1 text-xs whitespace-nowrap">
				{hasLogo(name) && <RenderProviderIcon provider={name as ProviderIconType} size="sm" />}
				{getProviderLabel(name)}
			</Badge>
		);
	}
	if (measuring) return <ProviderIcon name={name} />;
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<ProviderIcon name={name} />
			</TooltipTrigger>
			<TooltipContent className="shadow-none">{getProviderLabel(name)}</TooltipContent>
		</Tooltip>
	);
}