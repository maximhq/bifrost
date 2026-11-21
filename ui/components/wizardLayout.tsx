import { ScrollArea } from "@/components/ui/scrollArea";
import { cn } from "@/lib/utils";
import { Check } from "lucide-react";
import type { ReactNode } from "react";

export interface WizardStep {
	key: string;
	title: string;
	description?: string;
}

interface WizardLayoutProps {
	steps: WizardStep[];
	currentIndex: number;
	// Earlier steps are links back; later ones open only through the footer.
	onSelect: (index: number) => void;
	// Step buttons are `${testIdPrefix}-step-${key}`.
	testIdPrefix: string;
	// Shown beside the step's title, e.g. a docs link or a status chip.
	actions?: ReactNode;
	footer: ReactNode;
	children: ReactNode;
}

// WizardLayout is a full-page creation flow: the steps in a rail on the left,
// the open step on the right under its title, and the step's actions in a
// footer that stays in view.
export function WizardLayout({ steps, currentIndex, onSelect, testIdPrefix, actions, footer, children }: WizardLayoutProps) {
	const step = steps[currentIndex];
	return (
		<div className="flex min-h-full min-w-0 flex-col gap-2 overflow-visible md:h-full md:flex-row md:overflow-hidden">
			<nav
				aria-label={step?.title}
				className="bg-card flex h-auto w-full shrink-0 flex-row gap-2 overflow-x-auto rounded-b-md px-1 py-3 md:h-full md:w-[240px] md:flex-col md:overflow-x-visible md:rounded-r-md md:rounded-b-none"
			>
				{steps.map((item, index) => {
					const done = index < currentIndex;
					const current = index === currentIndex;
					return (
						<button
							key={item.key}
							type="button"
							onClick={() => done && onSelect(index)}
							disabled={!done}
							aria-current={current ? "step" : undefined}
							data-testid={`${testIdPrefix}-step-${item.key}`}
							className={cn(
								"flex w-auto shrink-0 items-center gap-3 rounded-sm px-3 py-1.5 text-left transition-colors md:w-full",
								done && "hover:bg-muted cursor-pointer",
							)}
						>
							<span
								className={cn(
									"flex h-6 w-6 shrink-0 items-center justify-center rounded-full text-xs font-medium transition-colors",
									current ? "bg-primary text-primary-foreground" : done ? "bg-primary/20 text-primary" : "bg-muted text-muted-foreground",
								)}
							>
								{done ? <Check className="h-4 w-4" /> : index + 1}
							</span>
							<span className={cn("text-sm font-medium", current ? "text-foreground" : "text-muted-foreground")}>{item.title}</span>
						</button>
					);
				})}
			</nav>

			<ScrollArea
				className="bg-card min-w-0 grow overflow-visible rounded-t-md md:h-full md:overflow-hidden md:rounded-l-md md:rounded-tr-none"
				viewportClassName="no-table !h-auto !overflow-visible md:!h-full md:!overflow-x-hidden md:!overflow-y-scroll viewport-table-height-full"
			>
				<div className="mx-auto flex min-h-full w-full flex-col gap-6">
					<div className="bg-card z-10 mx-auto flex w-full max-w-6xl flex-col gap-3 px-4 pt-4 md:flex-row md:items-start md:justify-between">
						<div className="min-w-0">
							<h2 className="text-lg font-semibold">{step?.title}</h2>
							{step?.description && <p className="text-muted-foreground text-sm">{step.description}</p>}
						</div>
						{actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
					</div>

					<div className="mx-auto w-full max-w-6xl grow px-4">{children}</div>

					<div className="bg-card sticky bottom-0 z-10 mt-2 border-t">
						<div className="mx-auto flex w-full max-w-6xl flex-wrap items-center justify-between gap-3 px-4 py-3">{footer}</div>
					</div>
				</div>
			</ScrollArea>
		</div>
	);
}