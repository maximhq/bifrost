import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alertDialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import MultiBudgetLines, { BudgetLineEntry } from "@/components/ui/multibudgets";
import NumberAndSelect from "@/components/ui/numberAndSelect";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { resetDurationOptions, supportsCalendarAlignment } from "@/lib/constants/governance";
import { getErrorMessage, useCreateCustomerMutation, useUpdateCustomerMutation } from "@/lib/store";
import { CreateBudgetRequest, CreateCustomerRequest, Customer, UpdateCustomerRequest } from "@/lib/types/governance";
import { Validator } from "@/lib/utils/validation";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import isEqual from "lodash.isequal";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

interface CustomerSheetProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	customer?: Customer | null;
	onSuccess?: () => void;
}

interface CustomerFormData {
	name: string;
	budgets: BudgetLineEntry[];
	tokenMaxLimit: number | undefined;
	tokenResetDuration: string;
	requestMaxLimit: number | undefined;
	requestResetDuration: string;
	calendarAligned: boolean;
	isDirty: boolean;
}

const createInitialState = (customer?: Customer | null): Omit<CustomerFormData, "isDirty"> => {
	return {
		name: customer?.name || "",
		budgets: (customer?.budgets ?? []).map((b) => ({
			id: b.id,
			max_limit: b.max_limit,
			reset_duration: b.reset_duration,
		})),
		tokenMaxLimit: customer?.rate_limit?.token_max_limit ?? undefined,
		tokenResetDuration: customer?.rate_limit?.token_reset_duration || "1h",
		requestMaxLimit: customer?.rate_limit?.request_max_limit ?? undefined,
		requestResetDuration: customer?.rate_limit?.request_reset_duration || "1h",
		calendarAligned: customer?.calendar_aligned ?? false,
	};
};

export default function CustomerSheet({ open, onOpenChange, customer, onSuccess }: CustomerSheetProps) {
	const isEditing = !!customer;
	const [initialState, setInitialState] = useState<Omit<CustomerFormData, "isDirty">>(createInitialState(customer));
	const [formData, setFormData] = useState<CustomerFormData>({
		...createInitialState(customer),
		isDirty: false,
	});
	const [nameError, setNameError] = useState<string | null>(null);

	const [showCalendarAlignWarning, setShowCalendarAlignWarning] = useState(false);

	const hasCreateAccess = useRbac(RbacResource.Customers, RbacOperation.Create);
	const hasUpdateAccess = useRbac(RbacResource.Customers, RbacOperation.Update);
	const hasPermission = isEditing ? hasUpdateAccess : hasCreateAccess;

	const [createCustomer, { isLoading: isCreating }] = useCreateCustomerMutation();
	const [updateCustomer, { isLoading: isUpdating }] = useUpdateCustomerMutation();
	const loading = isCreating || isUpdating;

	useEffect(() => {
		if (open) {
			const init = createInitialState(customer);
			setInitialState(init);
			setFormData({ ...init, isDirty: false });
			setNameError(null);
		}
	}, [open, customer]);

	const handleCalendarAlignedChange = (checked: boolean) => {
		if (checked && isEditing && !initialState.calendarAligned) {
			setShowCalendarAlignWarning(true);
		} else {
			updateField("calendarAligned", checked);
		}
	};

	useEffect(() => {
		const currentData = {
			name: formData.name,
			budgets: formData.budgets,
			tokenMaxLimit: formData.tokenMaxLimit,
			tokenResetDuration: formData.tokenResetDuration,
			requestMaxLimit: formData.requestMaxLimit,
			requestResetDuration: formData.requestResetDuration,
			calendarAligned: formData.calendarAligned,
		};
		setFormData((prev) => ({
			...prev,
			isDirty: !isEqual(initialState, currentData),
		}));
	}, [
		formData.name,
		formData.budgets,
		formData.tokenMaxLimit,
		formData.tokenResetDuration,
		formData.requestMaxLimit,
		formData.requestResetDuration,
		formData.calendarAligned,
		initialState,
	]);

	const canCalendarAlign = useMemo(() => {
		const hasAlignableBudget = formData.budgets.some(
			(b) => b.max_limit !== undefined && b.max_limit !== null && supportsCalendarAlignment(b.reset_duration),
		);
		const hasAlignableRateLimit =
			(formData.tokenMaxLimit !== undefined && formData.tokenMaxLimit !== null && supportsCalendarAlignment(formData.tokenResetDuration)) ||
			(formData.requestMaxLimit !== undefined &&
				formData.requestMaxLimit !== null &&
				supportsCalendarAlignment(formData.requestResetDuration));
		return hasAlignableBudget || hasAlignableRateLimit;
	}, [formData.budgets, formData.tokenMaxLimit, formData.tokenResetDuration, formData.requestMaxLimit, formData.requestResetDuration]);

	// Reset calendarAligned when no duration supports alignment,
	// so a hidden toggle doesn't silently submit calendar_aligned: true.
	useEffect(() => {
		if (!formData.calendarAligned) return;
		if (!canCalendarAlign) {
			updateField("calendarAligned", false);
		}
	}, [canCalendarAlign, formData.calendarAligned]);

	const tokenMaxLimitNum = formData.tokenMaxLimit;
	const requestMaxLimitNum = formData.requestMaxLimit;

	const hasDuplicateDuration = useMemo(() => {
		const seen = new Set<string>();
		return formData.budgets
			.filter((b) => b.max_limit !== undefined && b.max_limit !== null)
			.some((b) => {
				if (seen.has(b.reset_duration)) return true;
				seen.add(b.reset_duration);
				return false;
			});
	}, [formData.budgets]);

	const validator = useMemo(
		() =>
			new Validator([
				Validator.required(formData.name.trim(), "Customer name is required"),
				Validator.custom(formData.isDirty, "No changes to save"),
				Validator.custom(!hasDuplicateDuration, "Each budget must have a unique reset period"),
				...(formData.budgets.some((b) => b.max_limit !== undefined && b.max_limit !== null && b.max_limit < 0.01)
					? [Validator.custom(false, "Budget max limit must be greater than $0.01")]
					: []),
				...(formData.tokenMaxLimit !== undefined && formData.tokenMaxLimit !== null
					? [
							Validator.minValue(tokenMaxLimitNum ?? 0, 1, "Token max limit must be at least 1"),
							Validator.required(formData.tokenResetDuration, "Token reset duration is required"),
						]
					: []),
				...(formData.requestMaxLimit !== undefined && formData.requestMaxLimit !== null
					? [
							Validator.minValue(requestMaxLimitNum ?? 0, 1, "Request max limit must be at least 1"),
							Validator.required(formData.requestResetDuration, "Request reset duration is required"),
						]
					: []),
			]),
		[formData, hasDuplicateDuration, tokenMaxLimitNum, requestMaxLimitNum],
	);

	const updateField = <K extends keyof CustomerFormData>(field: K, value: CustomerFormData[K]) => {
		if (field === "name") {
			setNameError(null);
		}
		setFormData((prev) => ({ ...prev, [field]: value }));
	};

	const handleSubmit = async (e: React.FormEvent) => {
		e.preventDefault();

		if (!validator.isValid()) {
			toast.error(validator.getFirstError());
			return;
		}

		const budgetRequests: CreateBudgetRequest[] = formData.budgets
			.filter((b) => b.max_limit !== undefined && b.max_limit !== null)
			.map((b) => ({ id: b.id, max_limit: b.max_limit!, reset_duration: b.reset_duration }));

		try {
			if (isEditing && customer) {
				const updateData: UpdateCustomerRequest = {
					name: formData.name,
					calendar_aligned: formData.calendarAligned,
					budgets: budgetRequests,
				};

				const hadRateLimit = !!customer.rate_limit;
				const hasRateLimit =
					(tokenMaxLimitNum !== undefined && tokenMaxLimitNum !== null) ||
					(requestMaxLimitNum !== undefined && requestMaxLimitNum !== null);
				if (hasRateLimit) {
					updateData.rate_limit = {
						token_max_limit: tokenMaxLimitNum,
						token_reset_duration: tokenMaxLimitNum !== undefined && tokenMaxLimitNum !== null ? formData.tokenResetDuration : undefined,
						request_max_limit: requestMaxLimitNum,
						request_reset_duration:
							requestMaxLimitNum !== undefined && requestMaxLimitNum !== null ? formData.requestResetDuration : undefined,
					};
				} else if (hadRateLimit) {
					updateData.rate_limit = {} as UpdateCustomerRequest["rate_limit"];
				}

				await updateCustomer({ customerId: customer.id, data: updateData }).unwrap();
				toast.success("Customer updated successfully");
			} else {
				const createData: CreateCustomerRequest = {
					name: formData.name,
					calendar_aligned: formData.calendarAligned,
					budgets: budgetRequests,
				};

				if (
					(tokenMaxLimitNum !== undefined && tokenMaxLimitNum !== null) ||
					(requestMaxLimitNum !== undefined && requestMaxLimitNum !== null)
				) {
					createData.rate_limit = {
						token_max_limit: tokenMaxLimitNum,
						token_reset_duration: tokenMaxLimitNum !== undefined && tokenMaxLimitNum !== null ? formData.tokenResetDuration : undefined,
						request_max_limit: requestMaxLimitNum,
						request_reset_duration:
							requestMaxLimitNum !== undefined && requestMaxLimitNum !== null ? formData.requestResetDuration : undefined,
					};
				}

				await createCustomer(createData).unwrap();
				toast.success("Customer created successfully");
			}

			onOpenChange(false);
			onSuccess?.();
		} catch (error: any) {
			if (error?.status === 409) {
				setNameError(getErrorMessage(error));
				return;
			}
			toast.error(getErrorMessage(error));
		}
	};

	const isSubmitDisabled = loading || !validator.isValid() || !hasPermission;

	const getTooltipMessage = () => {
		if (!hasPermission) return "You don't have permission to perform this action";
		if (loading) return "Saving...";
		return validator.getFirstError() || "Please fix validation errors";
	};

	const showCalendarAlignToggle = canCalendarAlign;

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="max-w-[900px] p-0 pt-4 sm:max-w-2xl" data-testid="customer-dialog-content">
				<SheetHeader className="flex flex-col items-start px-0 py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-8">
					<SheetTitle className="flex items-center gap-2">{isEditing ? "Edit Customer" : "Create Customer"}</SheetTitle>
					<SheetDescription>
						{isEditing
							? "Update the customer information and settings."
							: "Create a new customer account to organize teams and manage resources."}
					</SheetDescription>
				</SheetHeader>

				<form onSubmit={handleSubmit} className="flex flex-1 flex-col">
					<div className="flex-1 px-8 py-4">
						<div className="space-y-6">
							<div className="space-y-4">
								<div className="space-y-2">
									<Label htmlFor="name">Customer Name *</Label>
									<Input
										id="name"
										data-testid="customer-name-input"
										placeholder="e.g., Acme Corporation"
										value={formData.name}
										maxLength={50}
										onChange={(e) => updateField("name", e.target.value)}
									/>
									{nameError && <p className="text-destructive text-sm">{nameError}</p>}
									<p className="text-muted-foreground text-sm">This name will be used to identify the customer account.</p>
								</div>
							</div>

							<MultiBudgetLines
								data-testid="customer-budgets"
								label="Budget Limits"
								lines={formData.budgets}
								onChange={(lines) => updateField("budgets", lines)}
								options={resetDurationOptions}
							/>

							<NumberAndSelect
								id="tokenMaxLimit"
								label="Maximum Tokens"
								value={formData.tokenMaxLimit}
								selectValue={formData.tokenResetDuration}
								onChangeNumber={(value) => updateField("tokenMaxLimit", value)}
								onChangeSelect={(value) => updateField("tokenResetDuration", value)}
								options={resetDurationOptions}
							/>

							<NumberAndSelect
								id="requestMaxLimit"
								label="Maximum Requests"
								value={formData.requestMaxLimit}
								selectValue={formData.requestResetDuration}
								onChangeNumber={(value) => updateField("requestMaxLimit", value)}
								onChangeSelect={(value) => updateField("requestResetDuration", value)}
								options={resetDurationOptions}
							/>

							{showCalendarAlignToggle && (
								<div className="flex items-center justify-between gap-4 rounded-md border px-3 py-2">
									<div className="space-y-0.5">
										<Label htmlFor="customer-calendar-aligned-toggle" className="text-sm font-normal">
											Align to calendar cycle
										</Label>
										<p className="text-muted-foreground text-xs">
											Reset budgets and rate limits at the start of each period (e.g. 1st of month) instead of rolling from creation date.
											Applies to durations of a day or longer.
										</p>
									</div>
									<Switch
										id="customer-calendar-aligned-toggle"
										checked={formData.calendarAligned}
										onCheckedChange={handleCalendarAlignedChange}
										data-testid="customer-calendar-aligned-toggle"
									/>
								</div>
							)}

							<AlertDialog open={showCalendarAlignWarning} onOpenChange={setShowCalendarAlignWarning}>
								<AlertDialogContent>
									<AlertDialogHeader>
										<AlertDialogTitle>Reset budget and rate-limit usage?</AlertDialogTitle>
										<AlertDialogDescription>
											Enabling calendar alignment will reset budget usage to <span className="font-semibold">$0.00</span> and token/request
											rate-limit counters to <span className="font-semibold">0</span> for this customer, then snap each reset date to the
											start of its current period (e.g. start of day, week, month, or year). The usage reset cannot be undone, but calendar
											alignment can be turned off later. This will take effect when you save.
										</AlertDialogDescription>
									</AlertDialogHeader>
									<AlertDialogFooter>
										<AlertDialogCancel data-testid="customer-calendar-align-cancel-btn">Cancel</AlertDialogCancel>
										<AlertDialogAction
											data-testid="customer-calendar-align-enable-btn"
											onClick={() => {
												updateField("calendarAligned", true);
												setShowCalendarAlignWarning(false);
											}}
										>
											Enable Calendar Alignment
										</AlertDialogAction>
									</AlertDialogFooter>
								</AlertDialogContent>
							</AlertDialog>
						</div>
					</div>

					<SheetFooter className="bg-card sticky bottom-0 flex-row justify-end gap-2 border-t px-6 py-4">
						<Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={loading}>
							Cancel
						</Button>
						<TooltipProvider>
							<Tooltip>
								<TooltipTrigger asChild>
									<span>
										<Button type="submit" disabled={isSubmitDisabled}>
											{loading ? "Saving..." : isEditing ? "Update Customer" : "Create Customer"}
										</Button>
									</span>
								</TooltipTrigger>
								{isSubmitDisabled && (
									<TooltipContent>
										<p>{getTooltipMessage()}</p>
									</TooltipContent>
								)}
							</Tooltip>
						</TooltipProvider>
					</SheetFooter>
				</form>
			</SheetContent>
		</Sheet>
	);
}