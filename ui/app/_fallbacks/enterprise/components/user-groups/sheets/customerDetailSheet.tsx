import { Label } from "@/components/ui/label";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { resetDurationLabels } from "@/lib/constants/governance";
import { Customer } from "@/lib/types/governance";
import { cn } from "@/lib/utils";
import { formatCompactNumber } from "@/lib/utils/numbers";

interface Props {
	customer: Customer | null;
	open: boolean;
	onOpenChange: (open: boolean) => void;
}

function formatResetDuration(duration: string | null | undefined): string {
	if (!duration) return "";
	return resetDurationLabels[duration] || duration;
}

function formatCurrency(value: number): string {
	return `$${value.toFixed(2)}`;
}

function DetailCard({ title, children, contentClassName }: { title: string; children: React.ReactNode; contentClassName?: string }) {
	return (
		<div className="space-y-2">
			<div className="flex items-center justify-between">
				<h3 className="text-muted-foreground text-xs font-medium tracking-wider uppercase">{title}</h3>
			</div>
			<div className={cn("rounded-md border px-4 py-3", contentClassName)}>{children}</div>
		</div>
	);
}

function BudgetLineBar({ current, max, resetDuration }: { current: number; max: number; resetDuration?: string }) {
	const pct = max > 0 ? Math.min((current / max) * 100, 100) : 0;
	const isOver80 = pct >= 80;
	const isOver100 = pct >= 100;
	return (
		<div className="space-y-1.5">
			<div className="flex items-center justify-between text-xs">
				<span className="font-medium" />
				<span className="text-muted-foreground">{formatResetDuration(resetDuration)}</span>
			</div>
			<div className="bg-muted h-1.5 w-full overflow-hidden rounded-full">
				<div
					className={`h-full rounded-full transition-all ${isOver100 ? "bg-destructive" : isOver80 ? "bg-orange-500" : "bg-primary"}`}
					style={{ width: `${pct}%` }}
				/>
			</div>
			<div className="text-muted-foreground flex items-center justify-between text-xs">
				<span>
					{formatCurrency(current)} / {formatCurrency(max)}
				</span>
				<span>{pct.toFixed(0)}%</span>
			</div>
		</div>
	);
}

function RateLimitBar({ label, current, max, resetDuration }: { label: string; current: number; max: number; resetDuration?: string }) {
	const pct = max > 0 ? Math.min((current / max) * 100, 100) : 0;
	const isOver80 = pct >= 80;
	const isOver100 = pct >= 100;
	return (
		<div className="space-y-1.5">
			<div className="flex items-center justify-between text-xs">
				<span>{label}</span>
				<span className="text-muted-foreground">{formatResetDuration(resetDuration)}</span>
			</div>
			<div className="bg-muted h-1.5 w-full overflow-hidden rounded-full">
				<div
					className={`h-full rounded-full transition-all ${isOver100 ? "bg-destructive" : isOver80 ? "bg-orange-500" : "bg-primary"}`}
					style={{ width: `${pct}%` }}
				/>
			</div>
			<div className="text-muted-foreground flex items-center justify-between text-xs">
				<span>
					{formatCompactNumber(current)} / {formatCompactNumber(max)}
				</span>
				<span>{pct.toFixed(0)}%</span>
			</div>
		</div>
	);
}

//
// OSS fallback for the enterprise CustomerDetailSheet. It renders the Info,
// Budgets, and Rate Limits sections from the customer already in hand, and omits
// the Teams / Business Units sections, which depend on enterprise-only APIs.

export function CustomerDetailSheet({ customer, open, onOpenChange }: Props) {
	const budgets = customer?.budgets ?? [];
	const rateLimit = customer?.rate_limit;
	const hasRateLimit = rateLimit?.token_max_limit != null || rateLimit?.request_max_limit != null;

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="max-w-[700px] overflow-y-auto p-0 pt-4">
				<SheetHeader className="flex flex-col items-start px-0 py-4" headerClassName="mb-0 px-8 sticky -top-4 bg-card z-10">
					<SheetTitle className="text-lg">{customer?.name || "Customer Details"}</SheetTitle>
					<SheetDescription>Usage details for this customer.</SheetDescription>
				</SheetHeader>

				{customer && (
					<div className="space-y-6 px-8 py-4">
						{/* ── Info ─────────────────────────────────────────── */}
						<DetailCard title="Info">
							<div className="grid grid-cols-2 gap-x-8 gap-y-4">
								<div>
									<Label className="text-muted-foreground text-xs">Name</Label>
									<p className="mt-0.5 text-sm">{customer.name ?? "—"}</p>
								</div>
							</div>
						</DetailCard>

						{/* ── Budgets ──────────────────────────────────────── */}
						<DetailCard title="Budgets">
							{budgets.length > 0 ? (
								<div className="space-y-3">
									{[...budgets]
										.sort((a, b) => (b.max_limit || 0) - (a.max_limit || 0))
										.map((b) => (
											<BudgetLineBar key={b.id} current={b.current_usage} max={b.max_limit} resetDuration={b.reset_duration} />
										))}
								</div>
							) : (
								<p className="text-muted-foreground py-1 text-center text-sm">No budgets configured</p>
							)}
						</DetailCard>

						{/* ── Rate Limits ──────────────────────────────────── */}
						<DetailCard title="Rate Limits">
							{rateLimit && hasRateLimit ? (
								<div className="space-y-3">
									{rateLimit.token_max_limit != null && (
										<RateLimitBar
											label="Tokens"
											current={rateLimit.token_current_usage ?? 0}
											max={rateLimit.token_max_limit}
											resetDuration={rateLimit.token_reset_duration}
										/>
									)}
									{rateLimit.request_max_limit != null && (
										<RateLimitBar
											label="Requests"
											current={rateLimit.request_current_usage ?? 0}
											max={rateLimit.request_max_limit}
											resetDuration={rateLimit.request_reset_duration}
										/>
									)}
								</div>
							) : (
								<p className="text-muted-foreground py-1 text-center text-sm">No rate limits configured</p>
							)}
						</DetailCard>
					</div>
				)}
			</SheetContent>
		</Sheet>
	);
}

export default CustomerDetailSheet;