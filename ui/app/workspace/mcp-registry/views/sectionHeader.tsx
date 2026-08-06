import { Label } from "@/components/ui/label";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { Info } from "lucide-react";

/** Consistent title + one-line description used for every subsection across the MCP client create form and edit sheet. */
export function SectionHeader({
	title,
	description,
	testId,
	tooltip,
	action,
}: {
	title: string;
	description: string;
	testId?: string;
	/** Optional info tooltip rendered inline next to the title. */
	tooltip?: React.ReactNode;
	/** Optional trailing action (e.g. an "Add X" button) rendered right-aligned on the same row as the title. */
	action?: React.ReactNode;
}) {
	const header = (
		<div className="space-y-1">
			<div className="flex items-center gap-2">
				<Label className="text-sm font-medium" data-testid={testId}>
					{title}
				</Label>
				{tooltip && (
					<TooltipProvider>
						<Tooltip>
							<TooltipTrigger asChild>
								<Info className="text-muted-foreground h-4 w-4 cursor-help" />
							</TooltipTrigger>
							<TooltipContent className="max-w-xs">{tooltip}</TooltipContent>
						</Tooltip>
					</TooltipProvider>
				)}
			</div>
			<p className="text-muted-foreground text-xs">{description}</p>
		</div>
	);

	if (!action) return header;

	return (
		<div className="flex items-start justify-between gap-4">
			{header}
			{action}
		</div>
	);
}