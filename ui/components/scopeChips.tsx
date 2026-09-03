// ScopeChips renders an OAuth scope list as compact monospace chips: the
// first few inline, the rest behind a "+N" that reveals the full list on
// hover or focus.

import { Badge } from "@/components/ui/badge";
import { HoverCard, HoverCardContent, HoverCardTrigger } from "@/components/ui/hoverCard";
import { cn } from "@/lib/utils";

interface ScopeChipsProps {
	scopes: string[];
	// Chips shown inline before the "+N" overflow. Pass scopes.length to
	// show every scope inline (the sheet does; the table keeps rows short).
	max?: number;
	className?: string;
}

export function ScopeChips({ scopes, max = 2, className }: ScopeChipsProps) {
	if (scopes.length === 0) {
		return <span className="text-muted-foreground text-sm">-</span>;
	}
	const visible = scopes.slice(0, max);
	const hidden = scopes.length - visible.length;

	return (
		<div className={cn("flex flex-wrap items-center gap-1", className)}>
			{visible.map((scope) => (
				<Badge key={scope} variant="outline" className="font-mono text-xs font-normal">
					{scope}
				</Badge>
			))}
			{hidden > 0 && (
				<HoverCard openDelay={100} closeDelay={100}>
					<HoverCardTrigger asChild>
						<button
							type="button"
							className="text-muted-foreground hover:text-foreground focus-visible:ring-ring/50 cursor-default rounded-sm px-1 text-xs focus-visible:ring-[3px] focus-visible:outline-none"
							aria-label={`Show all ${scopes.length} scopes`}
						>
							+{hidden}
						</button>
					</HoverCardTrigger>
					<HoverCardContent align="start" className="w-auto max-w-sm p-3">
						<div className="text-muted-foreground mb-2 font-mono text-[10px] tracking-wider uppercase">Granted scopes</div>
						<div className="flex flex-col items-start gap-1">
							{scopes.map((scope) => (
								<Badge key={scope} variant="outline" className="max-w-full font-mono text-xs font-normal break-all whitespace-normal">
									{scope}
								</Badge>
							))}
						</div>
					</HoverCardContent>
				</HoverCard>
			)}
		</div>
	);
}