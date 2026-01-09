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
	// Budget
	budgetMaxLimit: number | undefined;
	budgetResetDuration: string;
	isDirty: boolean;
}

// Helper function to create initial state
const createInitialState = (customer?: Customer | null): Omit<CustomerFormData, "isDirty"> => {
	return {
		name: customer?.name || "",
		// Budget
		budgetMaxLimit: customer?.budget ? customer.budget.max_limit : undefined, // Already in dollars
		budgetResetDuration: customer?.budget?.reset_duration || "1M",
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
		};
		setFormData((prev) => ({
			...prev,
			isDirty: !isEqual(initialState, currentData),
		}));
	}, [formData.name, formData.budgetMaxLimit, formData.budgetResetDuration, initialState]);

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
							Validator.minValue(formData.budgetMaxLimit || 0, 0.01, "Budget max limit must be greater than $0.01"),
							Validator.required(formData.budgetResetDuration, "Budget reset duration is required"),
						]
					: []),
			]),
		[formData],
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

				// Add budget if enabled
				if (formData.budgetMaxLimit) {
					updateData.budget = {
						max_limit: formData.budgetMaxLimit, // Already in dollars
						reset_duration: formData.budgetResetDuration,
					};
				}

				await updateCustomer({ customerId: customer.id, data: updateData }).unwrap();
				toast.success("Customer updated successfully");
			} else {
				// Create new customer
				const createData: CreateCustomerRequest = {
					name: formData.name,
				};

				// Add budget if enabled
				if (formData.budgetMaxLimit) {
					createData.budget = {
						max_limit: formData.budgetMaxLimit, // Already in dollars
						reset_duration: formData.budgetResetDuration,
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
							value={formData.budgetMaxLimit?.toString() || ""}
							selectValue={formData.budgetResetDuration}
							onChangeNumber={(value) => updateField("budgetMaxLimit", value === '' ? undefined : parseFloat(value))}
							onChangeSelect={(value) => updateField("budgetResetDuration", value)}
							options={resetDurationOptions}
						/>
						{isEditing && customer?.budget && (
							<div className="space-y-2">
								<div className="flex items-center gap-2">
									<span className="text-sm">Current Usage:</span>
									<div className="mt-0.5 flex items-center gap-2">
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
								</div>
								<div className="flex items-center gap-2">
									<span className="text-sm">Last Reset:</span>
									<div className="mt-0.5 flex items-center gap-2">
										<span className="font-mono text-sm">
											{formatDistanceToNow(new Date(customer.budget.last_reset), { addSuffix: true })}
										</span>
									</div>
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
