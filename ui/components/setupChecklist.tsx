import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";
import { Check } from "lucide-react";

export interface SetupChecklistItem {
	key: string;
	title: string;
	detail: string;
	done: boolean;
	actionLabel: string;
	onAction: () => void;
}

// SetupChecklist is the "Finish setting up" card an entity's overview opens
// with: each thing it still needs, ticked off as it is done, with a button
// onto the place that does it.
export function SetupChecklist({
	title,
	summary,
	items,
	testId,
}: {
	title: string;
	// e.g. "1 of 4 done", phrased by the caller.
	summary: string;
	items: SetupChecklistItem[];
	testId: string;
}) {
	return (
		<Card className="gap-0 rounded-sm p-0 shadow-none" data-testid={testId}>
			<div className="flex items-center gap-2 border-b px-4 py-2.5">
				<span className="text-sm font-semibold">{title}</span>
				<span className="text-muted-foreground text-xs">{summary}</span>
			</div>
			{items.map((item) => (
				<div
					key={item.key}
					className="flex items-center gap-3 border-b px-4 py-2.5 text-sm last:border-b-0"
					data-testid={`${testId}-${item.key}`}
					data-done={item.done}
				>
					<span
						className={cn(
							"flex size-5 shrink-0 items-center justify-center rounded-full border",
							item.done && "bg-primary border-primary text-primary-foreground",
						)}
						aria-hidden="true"
					>
						{item.done && <Check className="size-3" />}
					</span>
					<span className="min-w-0 flex-1">
						<span className="block font-medium">{item.title}</span>
						<span className="text-muted-foreground block text-xs">{item.detail}</span>
					</span>
					<Button
						type="button"
						variant="outline"
						size="sm"
						className="h-7 shrink-0 text-xs"
						onClick={item.onAction}
						data-testid={`${testId}-${item.key}-action`}
					>
						{item.actionLabel}
					</Button>
				</div>
			))}
		</Card>
	);
}