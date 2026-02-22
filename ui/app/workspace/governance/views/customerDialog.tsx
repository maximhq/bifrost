"use client"

import FormFooter from "@/components/formFooter"
import { Badge } from "@/components/ui/badge"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import NumberAndSelect from "@/components/ui/numberAndSelect"
import { resetDurationOptions } from "@/lib/constants/governance"
import { getErrorMessage, useCreateCustomerMutation, useUpdateCustomerMutation } from "@/lib/store"
import { CreateCustomerRequest, Customer, UpdateCustomerRequest } from "@/lib/types/governance"
import { formatCurrency } from "@/lib/utils/governance"
import { Validator } from "@/lib/utils/validation"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"
import { formatDistanceToNow } from "date-fns"
import isEqual from "lodash.isequal"
import { useEffect, useMemo, useState } from "react"
import { toast } from "sonner"

interface CustomerDialogProps {
	customer?: Customer | null;
	onSave: () => void;
	onCancel: () => void;
}

interface CustomerFormData {
	name: string;
	// Budget (stored as string to allow intermediate decimal states like "1.")
	budgetMaxLimit: string;
	budgetResetDuration: string;
	// Rate Limit (stored as string)
	tokenMaxLimit: string;
	tokenResetDuration: string;
	requestMaxLimit: string;
	requestResetDuration: string;
	isDirty: boolean;
}

// Helper function to create initial state
const createInitialState = (customer?: Customer | null): Omit<CustomerFormData, "isDirty"> => {
	return {
		name: customer?.name || "",
		// Budget (stored as string)
		budgetMaxLimit: customer?.budget ? String(customer.budget.max_limit) : "",
		budgetResetDuration: customer?.budget?.reset_duration || "1M",
		// Rate Limit (stored as string)
		tokenMaxLimit: customer?.rate_limit?.token_max_limit ? String(customer.rate_limit.token_max_limit) : "",
		tokenResetDuration: customer?.rate_limit?.token_reset_duration || "1h",
		requestMaxLimit: customer?.rate_limit?.request_max_limit ? String(customer.rate_limit.request_max_limit) : "",
		requestResetDuration: customer?.rate_limit?.request_reset_duration || "1h",
	};
};

export default function CustomerDialog({ customer, onSave, onCancel }: CustomerDialogProps) {
  const isEditing = !!customer
  const [initialState] = useState<Omit<CustomerFormData, "isDirty">>(createInitialState(customer))
  const [formData, setFormData] = useState<CustomerFormData>({
    ...initialState,
    isDirty: false,
  })

  const hasCreateAccess = useRbac(RbacResource.Customers, RbacOperation.Create)
  const hasUpdateAccess = useRbac(RbacResource.Customers, RbacOperation.Update)
  const hasPermission = isEditing ? hasUpdateAccess : hasCreateAccess

  // RTK Query hooks
  const [createCustomer, { isLoading: isCreating }] = useCreateCustomerMutation()
  const [updateCustomer, { isLoading: isUpdating }] = useUpdateCustomerMutation()
  const loading = isCreating || isUpdating

	// Track isDirty state
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
	}, [formData.name, formData.budgetMaxLimit, formData.budgetResetDuration, formData.tokenMaxLimit, formData.tokenResetDuration, formData.requestMaxLimit, formData.requestResetDuration, initialState]);

	// Parse string values to numbers for validation and submission
	const budgetMaxLimitNum = formData.budgetMaxLimit ? parseFloat(formData.budgetMaxLimit) : undefined;
	const tokenMaxLimitNum = formData.tokenMaxLimit ? parseInt(formData.tokenMaxLimit) : undefined;
	const requestMaxLimitNum = formData.requestMaxLimit ? parseInt(formData.requestMaxLimit) : undefined;

	// Validation
	const validator = useMemo(
		() =>
			new Validator([
				// Basic validation
				Validator.required(formData.name.trim(), "Customer name is required"),

				// Check if anything is dirty
				Validator.custom(formData.isDirty, "No changes to save"),

				// Budget validation
				...(formData.budgetMaxLimit
					? [
							Validator.minValue(budgetMaxLimitNum || 0, 0.01, "Budget max limit must be greater than $0.01"),
							Validator.required(formData.budgetResetDuration, "Budget reset duration is required"),
						]
					: []),

				// Rate limit validation - token limits
				...(formData.tokenMaxLimit
					? [
							Validator.minValue(tokenMaxLimitNum || 0, 1, "Token max limit must be at least 1"),
							Validator.required(formData.tokenResetDuration, "Token reset duration is required"),
						]
					: []),

				// Rate limit validation - request limits
				...(formData.requestMaxLimit
					? [
							Validator.minValue(requestMaxLimitNum || 0, 1, "Request max limit must be at least 1"),
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
				// Update existing customer
				const updateData: UpdateCustomerRequest = {
					name: formData.name,
				};

				// Detect budget changes using had/has pattern
				const hadBudget = !!customer.budget;
				const hasBudget = !!budgetMaxLimitNum;
				if (hasBudget) {
					updateData.budget = {
						max_limit: budgetMaxLimitNum,
						reset_duration: formData.budgetResetDuration,
					};
				} else if (hadBudget) {
					updateData.budget = {} as UpdateCustomerRequest["budget"];
				}

				// Detect rate limit changes using had/has pattern
				const hadRateLimit = !!customer.rate_limit;
				const hasRateLimit = !!tokenMaxLimitNum || !!requestMaxLimitNum;
				if (hasRateLimit) {
					updateData.rate_limit = {
						token_max_limit: tokenMaxLimitNum,
						token_reset_duration: tokenMaxLimitNum ? formData.tokenResetDuration : undefined,
						request_max_limit: requestMaxLimitNum,
						request_reset_duration: requestMaxLimitNum ? formData.requestResetDuration : undefined,
					};
				} else if (hadRateLimit) {
					updateData.rate_limit = {} as UpdateCustomerRequest["rate_limit"];
				}

				await updateCustomer({ customerId: customer.id, data: updateData }).unwrap();
				toast.success("Customer updated successfully");
			} else {
				// Create new customer
				const createData: CreateCustomerRequest = {
					name: formData.name,
				};

				// Add budget if enabled
				if (budgetMaxLimitNum) {
					createData.budget = {
						max_limit: budgetMaxLimitNum,
						reset_duration: formData.budgetResetDuration,
					};
				}

				// Add rate limit if enabled (token or request limits)
				if (tokenMaxLimitNum || requestMaxLimitNum) {
					createData.rate_limit = {
						token_max_limit: tokenMaxLimitNum,
						token_reset_duration: tokenMaxLimitNum ? formData.tokenResetDuration : undefined,
						request_max_limit: requestMaxLimitNum,
						request_reset_duration: requestMaxLimitNum ? formData.requestResetDuration : undefined,
					};
				}

				await createCustomer(createData).unwrap();
				toast.success("Customer created successfully");
			}

			onSave();
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	return (
		<Dialog open onOpenChange={onCancel}>
			<DialogContent className="max-w-2xl">
				<DialogHeader>
					<DialogTitle className="flex items-center gap-2">{isEditing ? "Edit Customer" : "Create Customer"}</DialogTitle>
					<DialogDescription>
						{isEditing
							? "Update the customer information and settings."
							: "Create a new customer account to organize teams and manage resources."}
					</DialogDescription>
				</DialogHeader>

				<form onSubmit={handleSubmit} className="space-y-6">
					<div className="space-y-6">
						{/* Basic Information */}
						<div className="space-y-4">
							<div className="space-y-2">
								<Label htmlFor="name">Customer Name *</Label>
								<Input
									id="name"
									placeholder="e.g., Acme Corporation"
									value={formData.name}
									maxLength={50}
									onChange={(e) => updateField("name", e.target.value)}
								/>
								<p className="text-muted-foreground text-sm">This name will be used to identify the customer account.</p>
							</div>
						</div>

						{/* Budget Configuration */}
						<NumberAndSelect
							id="budgetMaxLimit"
							label="Maximum Spend (USD)"
							value={formData.budgetMaxLimit}
							selectValue={formData.budgetResetDuration}
							onChangeNumber={(value) => updateField("budgetMaxLimit", value)}
							onChangeSelect={(value) => updateField("budgetResetDuration", value)}
							options={resetDurationOptions}
						/>

						{/* Rate Limit Configuration - Token Limits */}
						<NumberAndSelect
							id="tokenMaxLimit"
							label="Maximum Tokens"
							value={formData.tokenMaxLimit}
							selectValue={formData.tokenResetDuration}
							onChangeNumber={(value) => updateField("tokenMaxLimit", value)}
							onChangeSelect={(value) => updateField("tokenResetDuration", value)}
							options={resetDurationOptions}
						/>

						{/* Rate Limit Configuration - Request Limits */}
						<NumberAndSelect
							id="requestMaxLimit"
							label="Maximum Requests"
							value={formData.requestMaxLimit}
							selectValue={formData.requestResetDuration}
							onChangeNumber={(value) => updateField("requestMaxLimit", value)}
							onChangeSelect={(value) => updateField("requestResetDuration", value)}
							options={resetDurationOptions}
						/>

						{/* Current Usage Section (only shown when editing with existing limits) */}
						{isEditing && (customer?.budget || customer?.rate_limit) && (
							<div className="rounded-lg border bg-muted/50 p-4 space-y-4">
								<p className="text-sm font-medium">Current Usage</p>
								<div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
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
													{customer.rate_limit.token_current_usage.toLocaleString()} / {customer.rate_limit.token_max_limit.toLocaleString()}
												</span>
												<Badge
													variant={customer.rate_limit.token_current_usage >= customer.rate_limit.token_max_limit ? "destructive" : "default"}
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
													{customer.rate_limit.request_current_usage.toLocaleString()} / {customer.rate_limit.request_max_limit.toLocaleString()}
												</span>
												<Badge
													variant={customer.rate_limit.request_current_usage >= customer.rate_limit.request_max_limit ? "destructive" : "default"}
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

					<FormFooter validator={validator} label="Customer" onCancel={onCancel} isLoading={loading} isEditing={isEditing} hasPermission={hasPermission} />
				</form>
			</DialogContent>
		</Dialog>
	);
}
