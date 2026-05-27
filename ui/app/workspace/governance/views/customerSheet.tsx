import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import NumberAndSelect from "@/components/ui/numberAndSelect";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { resetDurationOptions } from "@/lib/constants/governance";
import { getErrorMessage, useCreateCustomerMutation, useUpdateCustomerMutation } from "@/lib/store";
import { CreateCustomerRequest, Customer, UpdateCustomerRequest } from "@/lib/types/governance";
import { formatCurrency } from "@/lib/utils/governance";
import { Validator } from "@/lib/utils/validation";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { formatDistanceToNow } from "date-fns";
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
	budgetMaxLimit: number | undefined;
	budgetResetDuration: string;
	tokenMaxLimit: number | undefined;
	tokenResetDuration: string;
	requestMaxLimit: number | undefined;
	requestResetDuration: string;
	isDirty: boolean;
}

const createInitialState = (customer?: Customer | null): Omit<CustomerFormData, "isDirty"> => {
	return {
		name: customer?.name || "",
		budgetMaxLimit: customer?.budget?.max_limit ?? undefined,
		budgetResetDuration: customer?.budget?.reset_duration || "1M",
		tokenMaxLimit: customer?.rate_limit?.token_max_limit ?? undefined,
		tokenResetDuration: customer?.rate_limit?.token_reset_duration || "1h",
		requestMaxLimit: customer?.rate_limit?.request_max_limit ?? undefined,
		requestResetDuration: customer?.rate_limit?.request_reset_duration || "1h",
	};
};

export default function CustomerSheet({ open, onOpenChange, customer, onSuccess }: CustomerSheetProps) {
	const isEditing = !!customer;
	const [initialState, setInitialState] = useState<Omit<CustomerFormData, "isDirty">>(createInitialState(customer));
	const [formData, setFormData] = useState<CustomerFormData>({
		...createInitialState(customer),
		isDirty: false,
	});

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
		}
	}, [open, customer]);

	useEffect(() => {
		const currentData = {
			name: formData.name,
			budgetMaxLimit: formData.budgetMaxLimit,
			budgetResetDuration: formData.budgetResetDuration,
			tokenMaxLimit: formData.tokenMaxLimit,
			tokenResetDuration: formData.tokenResetDuration,
			requestMaxLimit: formData.requestMaxLimit,
			requestResetDuration: formData.requestResetDuration,
		};
		setFormData((prev) => ({
			...prev,
			isDirty: !isEqual(initialState, currentData),
		}));
	}, [
		formData.name,
		formData.budgetMaxLimit,
		formData.budgetResetDuration,
		formData.tokenMaxLimit,
		formData.tokenResetDuration,
		formData.requestMaxLimit,
		formData.requestResetDuration,
		initialState,
	]);

	const budgetMaxLimitNum = formData.budgetMaxLimit;
	const tokenMaxLimitNum = formData.tokenMaxLimit;
	const requestMaxLimitNum = formData.requestMaxLimit;

	const validator = useMemo(
		() =>
			new Validator([
				Validator.required(formData.name.trim(), "Customer name is required"),
				Validator.custom(formData.isDirty, "No changes to save"),
				...(formData.budgetMaxLimit !== undefined && formData.budgetMaxLimit !== null
					? [
						Validator.minValue(budgetMaxLimitNum ?? 0, 0.01, "Budget max limit must be greater than $0.01"),
						Validator.required(formData.budgetResetDuration, "Budget reset duration is required"),
					]
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
		[formData, budgetMaxLimitNum, tokenMaxLimitNum, requestMaxLimitNum],
	);

	const updateField = <K extends keyof CustomerFormData>(field: K, value: CustomerFormData[K]) => {
		setFormData((prev) => ({ ...prev, [field]: value }));
	};

	const handleSubmit = async (e: React.FormEvent) => {
		e.preventDefault();

		if (!validator.isValid()) {
			toast.error(validator.getFirstError());
			return;
		}

		try {
			if (isEditing && customer) {
				const updateData: UpdateCustomerRequest = {
					name: formData.name,
				};

				const hadBudget = !!customer.budget;
				const hasBudget = budgetMaxLimitNum !== undefined && budgetMaxLimitNum !== null;
				if (hasBudget) {
					updateData.budget = {
						max_limit: budgetMaxLimitNum,
						reset_duration: formData.budgetResetDuration,
					};
				} else if (hadBudget) {
					updateData.budget = {} as UpdateCustomerRequest["budget"];
				}

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
				};

				if (budgetMaxLimitNum !== undefined && budgetMaxLimitNum !== null) {
					createData.budget = {
						max_limit: budgetMaxLimitNum,
						reset_duration: formData.budgetResetDuration,
					};
				}

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
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const isSubmitDisabled = loading || !validator.isValid() || !hasPermission;

	const getTooltipMessage = () => {
		if (!hasPermission) return "You don't have permission to perform this action";
		if (loading) return "Saving...";
		return validator.getFirstError() || "Please fix validation errors";
	};

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="sm:max-w-2xl max-w-[900px]" data-testid="customer-dialog-content">
				<SheetHeader className="flex flex-col items-start p-8 pb-6" headerClassName="mb-0">
					<SheetTitle className="flex items-center gap-2">{isEditing ? "Edit Customer" : "Create Customer"}</SheetTitle>
					<SheetDescription>
						{isEditing
							? "Update the customer information and settings."
							: "Create a new customer account to organize teams and manage resources."}
					</SheetDescription>
				</SheetHeader>

				<form onSubmit={handleSubmit} className="flex flex-1 flex-col overflow-hidden">
					<div className="flex-1 overflow-y-auto px-8">
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
									<p className="text-muted-foreground text-sm">This name will be used to identify the customer account.</p>
								</div>
							</div>

							<NumberAndSelect
								id="budgetMaxLimit"
								label="Maximum Spend (USD)"
								value={formData.budgetMaxLimit}
								selectValue={formData.budgetResetDuration}
								onChangeNumber={(value) => updateField("budgetMaxLimit", value)}
								onChangeSelect={(value) => updateField("budgetResetDuration", value)}
								options={resetDurationOptions}
								dataTestId="budget-max-limit-input"
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

							{isEditing && (customer?.budget || customer?.rate_limit) && (
								<div className="bg-muted/50 space-y-4 rounded-lg border p-4">
									<p className="text-sm font-medium">Current Usage</p>
									<div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
										{customer?.budget && (
											<div className="space-y-1">
												<p className="text-muted-foreground text-xs">Budget</p>
												<div className="flex items-center gap-2">
													<span className="font-mono text-sm">
														{formatCurrency(customer.budget.current_usage)} / {formatCurrency(customer.budget.max_limit)}
													</span>
													<Badge
														variant={customer.budget.current_usage >= customer.budget.max_limit ? "destructive" : "default"}
														className="text-xs"
													>
														{Math.round((customer.budget.current_usage / customer.budget.max_limit) * 100)}%
													</Badge>
												</div>
												<p className="text-muted-foreground text-xs">
													Last Reset: {formatDistanceToNow(new Date(customer.budget.last_reset), { addSuffix: true })}
												</p>
											</div>
										)}
										{customer?.rate_limit?.token_max_limit && (
											<div className="space-y-1">
												<p className="text-muted-foreground text-xs">Tokens</p>
												<div className="flex items-center gap-2">
													<span className="font-mono text-sm">
														{customer.rate_limit.token_current_usage.toLocaleString()} /{" "}
														{customer.rate_limit.token_max_limit.toLocaleString()}
													</span>
													<Badge
														variant={
															customer.rate_limit.token_current_usage >= customer.rate_limit.token_max_limit ? "destructive" : "default"
														}
														className="text-xs"
													>
														{Math.round((customer.rate_limit.token_current_usage / customer.rate_limit.token_max_limit) * 100)}%
													</Badge>
												</div>
												<p className="text-muted-foreground text-xs">
													Last Reset: {formatDistanceToNow(new Date(customer.rate_limit.token_last_reset), { addSuffix: true })}
												</p>
											</div>
										)}
										{customer?.rate_limit?.request_max_limit && (
											<div className="space-y-1">
												<p className="text-muted-foreground text-xs">Requests</p>
												<div className="flex items-center gap-2">
													<span className="font-mono text-sm">
														{customer.rate_limit.request_current_usage.toLocaleString()} /{" "}
														{customer.rate_limit.request_max_limit.toLocaleString()}
													</span>
													<Badge
														variant={
															customer.rate_limit.request_current_usage >= customer.rate_limit.request_max_limit ? "destructive" : "default"
														}
														className="text-xs"
													>
														{Math.round((customer.rate_limit.request_current_usage / customer.rate_limit.request_max_limit) * 100)}%
													</Badge>
												</div>
												<p className="text-muted-foreground text-xs">
													Last Reset: {formatDistanceToNow(new Date(customer.rate_limit.request_last_reset), { addSuffix: true })}
												</p>
											</div>
										)}
									</div>
								</div>
							)}
						</div>
					</div>

					<SheetFooter className="flex-row justify-end gap-2 border-t px-6 py-4">
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
