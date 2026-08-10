import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import NumberAndSelect from "@/components/ui/numberAndSelect";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { resetDurationOptions } from "@/lib/constants/governance";
import type { ModelRateLimitMetric } from "@/lib/types/governance";
import { Plus, Trash2 } from "lucide-react";
import { useMemo } from "react";

export interface ModelRateLimitLine {
	id?: string;
	metric: ModelRateLimitMetric;
	max_limit?: number;
	reset_duration: string;
}

interface MultiRateLimitLinesProps {
	"data-testid"?: string;
	lines: ModelRateLimitLine[];
	onChange: (lines: ModelRateLimitLine[]) => void;
}

const metricOptions: { label: string; value: ModelRateLimitMetric }[] = [
	{ label: "Requests", value: "requests" },
	{ label: "Tokens", value: "tokens" },
];

export default function MultiRateLimitLines({
	"data-testid": testId = "model-rate-limit-lines",
	lines,
	onChange,
}: MultiRateLimitLinesProps) {
	const usedRules = useMemo(() => {
		const counts = new Map<string, number>();
		for (const line of lines) {
			const key = `${line.metric}:${line.reset_duration}`;
			counts.set(key, (counts.get(key) ?? 0) + 1);
		}
		return counts;
	}, [lines]);

	const addLine = () => {
		const firstAvailable = metricOptions
			.flatMap((metric) => resetDurationOptions.map((duration) => ({ metric: metric.value, reset_duration: duration.value })))
			.find((candidate) => !usedRules.has(`${candidate.metric}:${candidate.reset_duration}`));
		onChange([
			...lines,
			{
				metric: firstAvailable?.metric ?? "requests",
				max_limit: undefined,
				reset_duration: firstAvailable?.reset_duration ?? "1m",
			},
		]);
	};

	const updateLine = (index: number, patch: Partial<ModelRateLimitLine>) => {
		const updated = [...lines];
		updated[index] = { ...updated[index], ...patch };
		onChange(updated);
	};

	return (
		<div className="space-y-3" data-testid={testId}>
			<div className="flex items-center justify-between">
				<div>
					<Label className="text-sm font-medium">Rate limits</Label>
					<p className="text-muted-foreground mt-1 text-xs">Each rule tracks its own metric and reset window.</p>
				</div>
				<div className="flex items-center gap-2">
					{lines.length > 0 && (
						<Button data-testid={`${testId}-clear-btn`} variant="ghost" size="sm" type="button" onClick={() => onChange([])}>
							Clear all
						</Button>
					)}
					<Button data-testid={`${testId}-add-btn`} variant="outline" size="sm" type="button" onClick={addLine}>
						<Plus className="mr-1 h-3 w-3" />
						Add rule
					</Button>
				</div>
			</div>

			{lines.length === 0 && (
				<div className="text-muted-foreground rounded-md border border-dashed p-3 text-center text-sm">
					No rate limits configured. Add a request or token rule.
				</div>
			)}

			{lines.map((line, index) => {
				const duplicate = (usedRules.get(`${line.metric}:${line.reset_duration}`) ?? 0) > 1;
				const invalidAmount = line.max_limit === undefined || line.max_limit === null || line.max_limit <= 0;
				const label = line.metric === "tokens" ? "Maximum tokens" : "Maximum requests";
				return (
					<div
						key={line.id ?? index}
						className={`border-border/70 bg-muted/20 space-y-1 rounded-md border p-3 ${invalidAmount || duplicate ? "border-destructive/60" : ""}`}
						data-testid={`${testId}-line-${index}`}
					>
						<div className="flex items-end gap-2">
							<div className="w-36 shrink-0 space-y-2">
								<Label htmlFor={`${testId}-metric-${index}`} className="font-normal">
									Metric
								</Label>
								<Select value={line.metric} onValueChange={(metric) => updateLine(index, { metric: metric as ModelRateLimitMetric })}>
									<SelectTrigger id={`${testId}-metric-${index}`} data-testid={`${testId}-metric-${index}`}>
										<SelectValue />
									</SelectTrigger>
									<SelectContent>
										{metricOptions.map((option) => (
											<SelectItem key={option.value} value={option.value}>
												{option.label}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
							</div>
							<div className="min-w-0 flex-1">
								<NumberAndSelect
									key={`${line.metric}-${index}`}
									id={`${testId}-amount-${index}`}
									dataTestId={`${testId}-amount-${index}`}
									labelClassName="font-normal"
									label={label}
									value={line.max_limit}
									selectValue={line.reset_duration}
									onChangeNumber={(value) => updateLine(index, { max_limit: value })}
									onChangeSelect={(value) => updateLine(index, { reset_duration: value })}
									options={resetDurationOptions}
								/>
							</div>
							<Button
								data-testid={`${testId}-remove-${index}`}
								aria-label={`Remove ${line.metric} rule ${index + 1}`}
								variant="ghost"
								size="icon"
								type="button"
								className="text-destructive mb-0.5 h-8 w-8 shrink-0"
								onClick={() => onChange(lines.filter((_, lineIndex) => lineIndex !== index))}
							>
								<Trash2 className="h-4 w-4" />
							</Button>
						</div>
						{invalidAmount && <p className="text-destructive text-xs">Enter a positive limit.</p>}
						{duplicate && <p className="text-destructive text-xs">Duplicate metric and reset period — choose a different window.</p>}
					</div>
				);
			})}
		</div>
	);
}