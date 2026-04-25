import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { getErrorMessage, useApproveBudgetExtensionMutation, useCreateBudgetExtensionMutation } from "@/lib/store";
import { selectCurrentUser } from "@/lib/store/slices/appSlice";
import { useSelector } from "react-redux";
import { useState } from "react";
import { toast } from "sonner";

const DURATION_OPTIONS = [
	{ value: "1h", label: "1 hour" },
	{ value: "6h", label: "6 hours" },
	{ value: "24h", label: "24 hours" },
	{ value: "3d", label: "3 days" },
	{ value: "7d", label: "7 days" },
	{ value: "30d", label: "30 days" },
] as const;

interface ExtendBudgetDialogProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	budgetId: string;
	modelName: string;
}

export default function ExtendBudgetDialog({ open, onOpenChange, budgetId, modelName }: ExtendBudgetDialogProps) {
	const currentUser = useSelector(selectCurrentUser);
	const actorId = currentUser?.email ?? currentUser?.name ?? "admin";

	const [amount, setAmount] = useState("");
	const [duration, setDuration] = useState("24h");
	const [reason, setReason] = useState("");

	const [createExtension, { isLoading: isCreating }] = useCreateBudgetExtensionMutation();
	const [approveExtension, { isLoading: isApproving }] = useApproveBudgetExtensionMutation();

	const isSubmitting = isCreating || isApproving;

	const handleSubmit = async () => {
		const parsedAmount = parseFloat(amount);
		if (!amount || isNaN(parsedAmount) || parsedAmount <= 0) {
			toast.error("Please enter a valid amount greater than 0");
			return;
		}

		try {
			// Step 1: create the extension request
			const createResult = await createExtension({
				budget_id: budgetId,
				amount: parsedAmount,
				duration,
				reason: reason || undefined,
				requested_by: actorId,
			}).unwrap();

			// Step 2: immediately approve it - this is an admin-level instant extension.
			// The create/approve steps are kept separate so a pending-review workflow
			// can be introduced later without restructuring the call.
			await approveExtension({
				extensionId: createResult.budget_extension.id,
				data: { reviewed_by: actorId },
			}).unwrap();

			toast.success(`Budget extended by $${parsedAmount} for ${duration}`);
			onOpenChange(false);
			resetForm();
		} catch (error) {
			const msg = getErrorMessage(error);
			if (msg.toLowerCase().includes("active extension")) {
				toast.error("An active budget extension already exists for this budget. Wait for it to expire before adding another.");
			} else {
				toast.error(msg);
			}
		}
	};

	const resetForm = () => {
		setAmount("");
		setDuration("24h");
		setReason("");
	};

	const handleOpenChange = (nextOpen: boolean) => {
		if (!nextOpen) resetForm();
		onOpenChange(nextOpen);
	};

	return (
		<Dialog open={open} onOpenChange={handleOpenChange}>
			<DialogContent className="sm:max-w-md" data-testid="extend-budget-dialog">
				<DialogHeader>
					<DialogTitle>Extend Budget</DialogTitle>
					<DialogDescription>
						Temporarily increase the budget for <span className="font-medium">{modelName}</span>. The base limit is unchanged. The extension
					expires automatically. Extensions are applied immediately (admin action).
					</DialogDescription>
				</DialogHeader>

				<div className="space-y-4 py-2">
					{/* Amount */}
					<div className="space-y-1.5">
						<label htmlFor="extension-amount" className="text-sm font-medium">
							Additional Amount ($)
						</label>
						<Input
							id="extension-amount"
							type="number"
							min="0.01"
							step="0.01"
							placeholder="e.g. 50"
							value={amount}
							onChange={(e) => setAmount(e.target.value)}
							data-testid="extend-budget-amount-input"
						/>
					</div>

					{/* Duration */}
					<div className="space-y-1.5">
						<label htmlFor="extension-duration" className="text-sm font-medium">
							Duration
						</label>
						<Select value={duration} onValueChange={setDuration}>
							<SelectTrigger id="extension-duration" data-testid="extend-budget-duration-select">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								{DURATION_OPTIONS.map((opt) => (
									<SelectItem key={opt.value} value={opt.value}>
										{opt.label}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</div>

					{/* Optional reason */}
					<div className="space-y-1.5">
						<label htmlFor="extension-reason" className="text-sm font-medium">
							Reason <span className="text-muted-foreground font-normal">(optional)</span>
						</label>
						<Input
							id="extension-reason"
							placeholder="e.g. Temporary campaign spike"
							value={reason}
							onChange={(e) => setReason(e.target.value)}
							data-testid="extend-budget-reason-input"
						/>
					</div>
				</div>

				<DialogFooter>
					<Button variant="outline" onClick={() => handleOpenChange(false)} disabled={isSubmitting}>
						Cancel
					</Button>
					<Button onClick={handleSubmit} disabled={isSubmitting || !amount} data-testid="extend-budget-submit-btn">
						{isSubmitting ? "Applying..." : "Apply Extension"}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
