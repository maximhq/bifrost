import { cn } from "@/lib/utils";
import { LOG_LEVEL_BADGE_CLASSES, LOG_LEVELS, type LogLevel } from "@/lib/utils/logLevel";

interface LogLevelTabsProps {
	value: LogLevel;
	onChange: (level: LogLevel) => void;
	testId?: string;
}

/**
 * Segmented level filter. The selected level is a floor: each tab shows that level
 * and everything more severe, so debug shows all entries. The active tab borrows the
 * level's badge colors so it reads the same as the badges on the rows it filters.
 */
export default function LogLevelTabs({ value, onChange, testId }: LogLevelTabsProps) {
	return (
		<div role="group" aria-label="Minimum log level" className="bg-muted/60 inline-flex gap-0.5 rounded-sm p-0.5" data-testid={testId}>
			{LOG_LEVELS.map((level) => {
				const active = level === value;
				return (
					<button
						key={level}
						type="button"
						aria-pressed={active}
						onClick={() => onChange(level)}
						data-testid={testId ? `${testId}-${level}` : undefined}
						className={cn(
							"inline-flex items-center rounded-[3px] px-2 py-1 font-mono text-[10px] font-semibold tracking-wide uppercase transition-colors",
							active ? LOG_LEVEL_BADGE_CLASSES[level] : "text-muted-foreground hover:text-foreground",
						)}
					>
						{level}
					</button>
				);
			})}
		</div>
	);
}