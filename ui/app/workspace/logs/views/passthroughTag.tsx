import { Badge } from "@/components/ui/badge";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

interface PassthroughTagProps {
	compact?: boolean;
	testId?: string;
}

/** Marks a log row that came through a passthrough route and is shown under its inferred request type. */
export default function PassthroughTag({ compact, testId = "passthrough-tag" }: PassthroughTagProps) {
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<Badge
					variant="outline"
					data-testid={testId}
					className={cn(
						"border-dashed bg-slate-100 text-slate-800 dark:bg-slate-800 dark:text-slate-200",
						compact ? "px-1.5 py-0.5 font-mono text-[11px] uppercase" : "rounded-sm px-2 py-0.5 font-medium",
					)}
				>
					Passthrough
				</Badge>
			</TooltipTrigger>
			<TooltipContent>Forwarded byte for byte through a passthrough route; the type was inferred from the route.</TooltipContent>
		</Tooltip>
	);
}