import { Badge } from "@/components/ui/badge";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { ArrowLeftRight } from "lucide-react";

const PASSTHROUGH_TOOLTIP = "Forwarded byte for byte through a passthrough route; the type was inferred from the route.";

interface PassthroughTagProps {
	testId?: string;
}

/** Marks a log row that came through a passthrough route and is shown under its inferred request type. */
export default function PassthroughTag({ testId = "passthrough-tag" }: PassthroughTagProps) {
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<Badge
					variant="outline"
					data-testid={testId}
					className="rounded-sm border-dashed bg-slate-100 px-2 py-0.5 font-medium text-slate-800 dark:bg-slate-800 dark:text-slate-200"
				>
					Passthrough
				</Badge>
			</TooltipTrigger>
			<TooltipContent>{PASSTHROUGH_TOOLTIP}</TooltipContent>
		</Tooltip>
	);
}

/** Icon-only passthrough marker for tight spaces such as the logs table's type badge. */
export function PassthroughIcon({ testId = "passthrough-icon" }: PassthroughTagProps) {
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<span data-testid={testId} aria-label="Passthrough" className="inline-flex shrink-0">
					<ArrowLeftRight className="size-3" />
				</span>
			</TooltipTrigger>
			<TooltipContent>{PASSTHROUGH_TOOLTIP}</TooltipContent>
		</Tooltip>
	);
}