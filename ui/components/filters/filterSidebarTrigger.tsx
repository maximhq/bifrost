import { Button } from "@/components/ui/button";
import { useMobileFilterSlot } from "@/lib/contexts/topbarContext";
import { Filter, PanelLeftOpen } from "lucide-react";
import { createPortal } from "react-dom";

interface FilterSidebarTriggerProps {
	activeFilterCount: number;
	onClick: () => void;
	testId?: string;
}

/**
 * Shared collapsed-state trigger for filter sidebars.
 *
 * On mobile the compact trigger is portalled into the topbar, immediately
 * before notifications. Desktop keeps the existing full-height sidebar rail.
 */
export function FilterSidebarTrigger({ activeFilterCount, onClick, testId }: FilterSidebarTriggerProps) {
	const mobileFilterSlot = useMobileFilterSlot();

	return (
		<>
			{mobileFilterSlot &&
				createPortal(
					<Button
						type="button"
						onClick={onClick}
						variant="ghost"
						size="sm"
						className="group relative flex size-8 shrink-0 items-center justify-center rounded-sm border-0 p-0 shadow-none md:hidden"
						title="Show filters"
						aria-label="Show filters"
						data-testid={testId ? `${testId}-mobile` : undefined}
					>
						<Filter className="text-muted-foreground group-hover:text-foreground size-4 transition-colors" />
						{activeFilterCount > 0 && (
							<span className="bg-primary text-primary-foreground absolute -top-1 -right-1 flex size-5 items-center justify-center rounded-full text-[10px] font-medium">
								{activeFilterCount}
							</span>
						)}
					</Button>,
					mobileFilterSlot,
				)}

			<Button
				type="button"
				onClick={onClick}
				variant="outline"
				size="sm"
				className="group bg-card hover:bg-card dark:bg-card dark:hover:bg-card hidden h-full w-10 shrink-0 flex-col items-center justify-start gap-3 rounded-l-none rounded-r-md border-0 py-4 shadow-none hover:text-current active:scale-100 md:flex"
				title="Show filters"
				aria-label="Show filters"
				data-testid={testId}
			>
				<PanelLeftOpen className="text-muted-foreground group-hover:text-foreground size-4 transition-colors" />
				<span className="rotate-180 select-none [writing-mode:vertical-rl]">Filters</span>
				{activeFilterCount > 0 && (
					<span className="bg-primary/10 text-primary flex size-6 items-center justify-center rounded-full text-xs font-medium">
						{activeFilterCount}
					</span>
				)}
			</Button>
		</>
	);
}