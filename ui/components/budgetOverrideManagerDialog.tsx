import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { getErrorMessage } from "@/lib/store/apis/baseApi";
import type { Budget, BudgetOverrideRequest } from "@/lib/types/governance";
import { budgetOverrideFormSchema } from "@/lib/types/schemas";
import { formatCurrency, getBudgetOverrideValidUntil, getEffectiveBudgetLimit, hasActiveBudgetOverride } from "@/lib/utils/governance";
import { Pencil, Plus } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

// One overridable budget row. `budget` is the persisted row the override applies to;
// `label` is scope-relative (the section already names the scope), e.g. the reset
// duration. `calendarAligned` rides along for the finite-cycle "valid until" preview.
export interface BudgetOverrideRow {
	key: string;
	label: string;
	budget: Budget;
	calendarAligned?: boolean;
}

// A titled group of budget rows in the modal (e.g. "Provider budget", "Model: gpt-4o").
export interface BudgetOverrideSection {
	key: string;
	title: string;
	rows: BudgetOverrideRow[];
}

interface BudgetOverrideManagerDialogProps {
	title: string;
	sections: BudgetOverrideSection[];
	onSave: (budgetId: string, request: BudgetOverrideRequest) => Promise<void>;
	onRemove: (budgetId: string) => Promise<void>;
	disabled?: boolean;
	triggerLabel?: string;
}

/**
 * One "Add override" button opening a modal whose sections group a scope's budgets —
 * e.g. a provider's own budget plus one section per model beneath it. Each row's
 * additive override can be added, edited, or removed inline and independently.
 */
export function BudgetOverrideManagerDialog({ title, sections, onSave, onRemove, disabled, triggerLabel = "Add override" }: BudgetOverrideManagerDialogProps) {
	const [open, setOpen] = useState(false);
	const [expandedKey, setExpandedKey] = useState<string | null>(null);
	const [amount, setAmount] = useState("");
	const [mode, setMode] = useState<"cycles" | "forever">("cycles");
	const [cycles, setCycles] = useState("1");
	const [busyKey, setBusyKey] = useState<string | null>(null);
	const [error, setError] = useState<string | null>(null);

	const startEdit = (row: BudgetOverrideRow) => {
		const b = row.budget;
		const active = hasActiveBudgetOverride(b);
		setAmount(active ? String(b.override_amount) : "");
		setMode(active && b.override_mode ? b.override_mode : "cycles");
		setCycles(active && b.override_mode === "cycles" ? String(b.override_cycles_remaining ?? 1) : "1");
		setError(null);
		setExpandedKey(row.key);
	};

	const closeForm = () => {
		setExpandedKey(null);
		setError(null);
	};

	const submit = async (row: BudgetOverrideRow) => {
		const parsedAmount = Number(amount);
		const parsedCycles = Number(cycles);
		const parsed = budgetOverrideFormSchema.safeParse({
			amount: parsedAmount,
			mode,
			...(mode === "cycles" ? { cycles: parsedCycles } : {}),
		});
		if (!parsed.success) {
			setError(parsed.error.issues[0]?.message ?? "Invalid input");
			return;
		}
		setBusyKey(row.key);
		setError(null);
		try {
			const wasActive = hasActiveBudgetOverride(row.budget);
			await onSave(row.budget.id, { amount: parsedAmount, mode, ...(mode === "cycles" ? { cycles: parsedCycles } : {}) });
			toast.success(wasActive ? "Budget override updated" : "Budget override added");
			setExpandedKey(null);
		} catch (mutationError) {
			setError(getErrorMessage(mutationError));
		} finally {
			setBusyKey(null);
		}
	};

	const remove = async (row: BudgetOverrideRow) => {
		setBusyKey(row.key);
		setError(null);
		try {
			await onRemove(row.budget.id);
			toast.success("Budget override removed");
			if (expandedKey === row.key) setExpandedKey(null);
		} catch (mutationError) {
			// Remove can fire from a collapsed row where the inline error never renders,
			// so surface the failure with a toast.
			toast.error(getErrorMessage(mutationError));
		} finally {
			setBusyKey(null);
		}
	};

	const renderRow = (row: BudgetOverrideRow) => {
		const b = row.budget;
		const active = hasActiveBudgetOverride(b);
		const expanded = expandedKey === row.key;
		const busy = busyKey === row.key;
		// Lock every row while any mutation is in flight so two overrides can't race.
		const locked = busyKey !== null;
		const validUntil = mode === "cycles" ? getBudgetOverrideValidUntil(b, Number(cycles), row.calendarAligned) : null;

		return (
			<div key={row.key} className="rounded-sm border px-3 py-2" data-testid={`budget-override-row-${b.id}`}>
				<div className="flex items-center justify-between gap-2">
					<div className="min-w-0">
						<p className="truncate text-sm font-medium">{row.label}</p>
						<p className="text-muted-foreground text-xs">
							{active
								? `+${formatCurrency(b.override_amount ?? 0)} override · effective ${formatCurrency(getEffectiveBudgetLimit(b))}`
								: `Base ${formatCurrency(b.max_limit)}`}
						</p>
					</div>
					<div className="flex shrink-0 items-center gap-1">
						{active ? (
							<>
								<Button
									type="button"
									variant="ghost"
									size="sm"
									className="h-7 gap-1 px-2 text-xs"
									disabled={disabled || locked}
									onClick={() => (expanded ? closeForm() : startEdit(row))}
									data-testid={`budget-override-edit-${b.id}`}
								>
									<Pencil className="h-3 w-3" />
									Edit
								</Button>
								<Button
									type="button"
									variant="ghost"
									size="sm"
									className="text-destructive h-7 px-2 text-xs"
									disabled={disabled || locked}
									isLoading={busy && !expanded}
									onClick={() => remove(row)}
									data-testid={`budget-override-remove-${b.id}`}
								>
									Remove
								</Button>
							</>
						) : (
							<Button
								type="button"
								variant="ghost"
								size="sm"
								className="h-7 gap-1 px-2 text-xs"
								disabled={disabled || locked}
								onClick={() => (expanded ? closeForm() : startEdit(row))}
								data-testid={`budget-override-add-${b.id}`}
							>
								<Plus className="h-3 w-3" />
								Add override
							</Button>
						)}
					</div>
				</div>

				{expanded ? (
					<div className="mt-3 space-y-3 border-t pt-3">
						<div className="grid grid-cols-2 gap-3">
							<div className="space-y-1.5">
								<Label className="text-xs" htmlFor={`override-amount-${b.id}`}>
									Additional budget
								</Label>
								<div className="relative">
									<span className="text-muted-foreground pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-sm" aria-hidden="true">
										$
									</span>
									<Input
										id={`override-amount-${b.id}`}
										type="number"
										min="0.01"
										step="0.01"
										value={amount}
										onChange={(event) => setAmount(event.target.value)}
										placeholder="0.00"
										className="rounded-sm pl-7"
										disabled={busy}
										data-testid="budget-override-amount"
									/>
								</div>
							</div>
							<div className="space-y-1.5">
								<Label className="text-xs">Duration</Label>
								<Select value={mode} onValueChange={(value) => setMode(value as "cycles" | "forever")} disabled={busy}>
									<SelectTrigger className="w-full rounded-sm" data-testid="budget-override-mode">
										<SelectValue />
									</SelectTrigger>
									<SelectContent className="rounded-sm">
										<SelectItem value="cycles">For a number of reset cycles</SelectItem>
										<SelectItem value="forever">Until removed</SelectItem>
									</SelectContent>
								</Select>
							</div>
						</div>

						{mode === "cycles" ? (
							<div className="space-y-1.5">
								<Label className="text-xs" htmlFor={`override-cycles-${b.id}`}>
									Reset cycles
								</Label>
								<Input
									id={`override-cycles-${b.id}`}
									type="number"
									min="1"
									step="1"
									value={cycles}
									onChange={(event) => setCycles(event.target.value)}
									className="rounded-sm"
									disabled={busy}
									data-testid="budget-override-cycles"
								/>
								{validUntil ? (
									<p className="text-muted-foreground text-xs">
										Valid until <span className="text-foreground font-medium">{validUntil.toLocaleString()}</span>
									</p>
								) : null}
							</div>
						) : null}

						{error ? (
							<p className="text-destructive text-xs" data-testid="budget-override-error">
								{error}
							</p>
						) : null}

						<div className="flex justify-end gap-2">
							<Button type="button" variant="ghost" size="sm" className="rounded-sm" onClick={closeForm} disabled={busy} data-testid={`budget-override-cancel-${b.id}`}>
								Cancel
							</Button>
							<Button type="button" size="sm" className="rounded-sm" onClick={() => submit(row)} isLoading={busy} data-testid="budget-override-save">
								{active ? "Update" : "Add"}
							</Button>
						</div>
					</div>
				) : null}
			</div>
		);
	};

	const visibleSections = sections.filter((s) => s.rows.length > 0);
	if (visibleSections.length === 0) return null;

	return (
		<Dialog
			open={open}
			onOpenChange={(next) => {
				setOpen(next);
				if (!next) closeForm();
			}}
		>
			<DialogTrigger asChild>
				<Button type="button" variant="ghost" size="sm" className="h-7 gap-1.5 rounded-sm px-2 text-xs" disabled={disabled} data-testid="budget-override-open">
					<Plus className="h-3 w-3" />
					{triggerLabel}
				</Button>
			</DialogTrigger>
			<DialogContent className="rounded-sm sm:max-w-lg" data-testid="budget-override-manager-dialog">
				<DialogHeader>
					<DialogTitle>{title}</DialogTitle>
					<DialogDescription>Temporarily add spending capacity to a budget without changing its base limit.</DialogDescription>
				</DialogHeader>

				<div className="max-h-[60vh] space-y-4 overflow-y-auto py-2">
					{visibleSections.map((section) => (
						<div key={section.key} className="space-y-2">
							<p className="text-muted-foreground text-xs font-medium tracking-wider uppercase">{section.title}</p>
							<div className="space-y-2">{section.rows.map(renderRow)}</div>
						</div>
					))}
				</div>
			</DialogContent>
		</Dialog>
	);
}
