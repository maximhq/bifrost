import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { getErrorMessage } from "@/lib/store/apis/baseApi";
import { Budget, BudgetOverrideRequest } from "@/lib/types/governance";
import { budgetOverrideFormSchema } from "@/lib/types/schemas";
import { formatCurrency, getBudgetOverrideValidUntil, getEffectiveBudgetLimit, hasActiveBudgetOverride } from "@/lib/utils/governance";
import { Pencil, Plus } from "lucide-react";
import { FormEvent, useEffect, useState } from "react";
import { toast } from "sonner";

interface BudgetOverrideDialogProps {
	budget: Budget;
	onSave: (request: BudgetOverrideRequest) => Promise<void>;
	onRemove: () => Promise<void>;
	disabled?: boolean;
	calendarAligned?: boolean;
}

/** Lets an operator add, replace, or remove the additive override on one persisted budget. */
export function BudgetOverrideDialog({ budget, onSave, onRemove, disabled, calendarAligned }: BudgetOverrideDialogProps) {
	const active = hasActiveBudgetOverride(budget);
	const [open, setOpen] = useState(false);
	const [amount, setAmount] = useState("");
	const [mode, setMode] = useState<"cycles" | "forever">("cycles");
	const [cycles, setCycles] = useState("1");
	const [isSaving, setIsSaving] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const validUntil = mode === "cycles" ? getBudgetOverrideValidUntil(budget, Number(cycles), calendarAligned) : null;

	useEffect(() => {
		if (!open) return;
		setAmount(active ? String(budget.override_amount) : "");
		setMode(active && budget.override_mode ? budget.override_mode : "cycles");
		setCycles(active && budget.override_mode === "cycles" ? String(budget.override_cycles_remaining ?? 1) : "1");
		setError(null);
	}, [active, budget.override_amount, budget.override_cycles_remaining, budget.override_mode, open]);

	const handleSubmit = async (event: FormEvent) => {
		event.preventDefault();
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

		setIsSaving(true);
		setError(null);
		try {
			await onSave({ amount: parsedAmount, mode, ...(mode === "cycles" ? { cycles: parsedCycles } : {}) });
			toast.success(active ? "Budget override updated" : "Budget override added");
			setOpen(false);
		} catch (mutationError) {
			setError(getErrorMessage(mutationError));
		} finally {
			setIsSaving(false);
		}
	};

	const handleRemove = async () => {
		setIsSaving(true);
		setError(null);
		try {
			await onRemove();
			toast.success("Budget override removed");
			setOpen(false);
		} catch (mutationError) {
			setError(getErrorMessage(mutationError));
		} finally {
			setIsSaving(false);
		}
	};

	return (
		<Dialog open={open} onOpenChange={setOpen}>
			<DialogTrigger asChild>
				<Button
					type="button"
					variant={active ? "outline" : "ghost"}
					size="sm"
					className="h-7 gap-1.5 rounded-sm px-2 text-xs"
					disabled={disabled}
					data-testid={`budget-override-open-${budget.id}`}
				>
					{active ? <Pencil className="h-3 w-3" /> : <Plus className="h-3 w-3" />}
					{active ? "Edit override" : "Add override"}
				</Button>
			</DialogTrigger>
			<DialogContent className="rounded-sm sm:max-w-md" data-testid={`budget-override-dialog-${budget.id}`}>
				<form onSubmit={handleSubmit}>
					<DialogHeader>
						<DialogTitle>{active ? "Edit budget override" : "Add budget override"}</DialogTitle>
						<DialogDescription>
							Temporarily add spending capacity without changing the base {formatCurrency(budget.max_limit)} budget.
						</DialogDescription>
					</DialogHeader>

					<div className="space-y-4 py-5">
						<div className="space-y-2">
							<Label htmlFor={`budget-override-amount-${budget.id}`}>Additional budget</Label>
							<div className="relative">
								<span
									className="text-muted-foreground pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-sm"
									aria-hidden="true"
								>
									$
								</span>
								<Input
									id={`budget-override-amount-${budget.id}`}
									type="number"
									min="0.01"
									step="0.01"
									value={amount}
									onChange={(event) => setAmount(event.target.value)}
									placeholder="0.00"
									className="rounded-sm pl-7"
									disabled={isSaving}
									data-testid="budget-override-amount"
								/>
							</div>
						</div>

						<div className="space-y-2">
							<Label>Duration</Label>
							<Select value={mode} onValueChange={(value) => setMode(value as "cycles" | "forever")} disabled={isSaving}>
								<SelectTrigger className="w-full rounded-sm" data-testid="budget-override-mode">
									<SelectValue />
								</SelectTrigger>
								<SelectContent className="rounded-sm">
									<SelectItem value="cycles">For a number of reset cycles</SelectItem>
									<SelectItem value="forever">Until removed</SelectItem>
								</SelectContent>
							</Select>
						</div>

						{mode === "cycles" ? (
							<div className="space-y-2">
								<Label htmlFor={`budget-override-cycles-${budget.id}`}>Reset cycles</Label>
								<Input
									id={`budget-override-cycles-${budget.id}`}
									type="number"
									min="1"
									step="1"
									value={cycles}
									onChange={(event) => setCycles(event.target.value)}
									className="rounded-sm"
									disabled={isSaving}
									data-testid="budget-override-cycles"
								/>
								<p className="text-muted-foreground text-xs">The current reset cycle counts as the first cycle.</p>
								{validUntil ? (
									<p className="text-muted-foreground text-xs">
										Valid until <span className="text-foreground font-medium">{validUntil.toLocaleString()}</span>
									</p>
								) : null}
							</div>
						) : null}

						{active ? (
							<div className="bg-muted/50 rounded-sm px-3 py-2 text-xs">
								Current effective limit: <span className="font-medium">{formatCurrency(getEffectiveBudgetLimit(budget))}</span>
							</div>
						) : null}

						{error ? (
							<p className="text-destructive text-sm" data-testid="budget-override-error">
								{error}
							</p>
						) : null}
					</div>

					<DialogFooter className="sm:justify-between">
						{active ? (
							<Button
								type="button"
								variant="destructive"
								className="rounded-sm"
								onClick={handleRemove}
								disabled={isSaving}
								data-testid="budget-override-remove"
							>
								Remove override
							</Button>
						) : (
							<span />
						)}
						<Button type="submit" className="rounded-sm" isLoading={isSaving} data-testid="budget-override-save">
							{active ? "Update override" : "Add override"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}