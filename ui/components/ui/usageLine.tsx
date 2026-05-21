import { Progress } from "@/components/ui/progress";
import { calculateUsagePercentage } from "@/lib/utils/governance";
import { cn } from "@/lib/utils";

function usageBarClass(pct: number, exhausted: boolean) {
  if (exhausted) return "[&>div]:bg-red-500/70";
  if (pct > 80) return "[&>div]:bg-amber-500/70";
  return "[&>div]:bg-emerald-500/70";
}

export function UsageLine({
  current,
  max,
  format,
}: {
  current: number;
  max: number;
  format: (n: number) => string;
}) {
  const pct = calculateUsagePercentage(current, max);
  const exhausted = max > 0 && current >= max;
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between gap-3">
        <span className="font-mono text-sm">
          {format(current)} <span className="text-muted-foreground">/</span>{" "}
          {format(max)}
        </span>
        <span
          className={cn(
            "text-xs font-medium tabular-nums",
            exhausted ? "text-red-500" : pct > 80 ? "text-amber-500" : "text-muted-foreground",
          )}
        >
          {pct}%
        </span>
      </div>
      <Progress
        value={Math.min(pct, 100)}
        className={cn("bg-muted/70 dark:bg-muted/30 h-1.5", usageBarClass(pct, exhausted))}
      />
    </div>
  );
}
