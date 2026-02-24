"use client"

import FormFooter from "@/components/formFooter"
import { Badge } from "@/components/ui/badge"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import NumberAndSelect from "@/components/ui/numberAndSelect"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { resetDurationOptions } from "@/lib/constants/governance"
import { getErrorMessage, useCreateTeamMutation, useUpdateTeamMutation } from "@/lib/store"
import { CreateTeamRequest, Customer, Team, UpdateTeamRequest } from "@/lib/types/governance"
import { formatCurrency } from "@/lib/utils/governance"
import { Validator } from "@/lib/utils/validation"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"
import { formatDistanceToNow } from "date-fns"
import isEqual from "lodash.isequal"
import { useEffect, useMemo, useState } from "react"
import { toast } from "sonner"

interface TeamDialogProps {
	team?: Team | null;
	customers: Customer[];
	onSave: () => void;
	onCancel: () => void;
}

interface TeamFormData {
	name: string;
	customerId: string;
	// Budget (stored as string to allow intermediate decimal states like "1.")
	budgetMaxLimit: string;
	budgetResetDuration: string;
	// Rate Limit
	tokenMaxLimit: string;
	tokenResetDuration: string;
	requestMaxLimit: string;
	requestResetDuration: string;
	isDirty: boolean;
}

// Helper function to create initial state
const createInitialState = (team?: Team | null): Omit<TeamFormData, "isDirty"> => {
	return {
		name: team?.name || "",
		customerId: team?.customer_id || "",
		// Budget (stored as string)
		budgetMaxLimit: team?.budget ? String(team.budget.max_limit) : "",
		budgetResetDuration: team?.budget?.reset_duration || "1M",
		// Rate Limit (stored as string)
		tokenMaxLimit: team?.rate_limit?.token_max_limit ? String(team.rate_limit.token_max_limit) : "",
		tokenResetDuration: team?.rate_limit?.token_reset_duration || "1h",
		requestMaxLimit: team?.rate_limit?.request_max_limit ? String(team.rate_limit.request_max_limit) : "",
		requestResetDuration: team?.rate_limit?.request_reset_duration || "1h",
	};
};

export default function TeamDialog({ team, customers, onSave, onCancel }: TeamDialogProps) {
  const isEditing = !!team
  const [initialState] = useState<Omit<TeamFormData, "isDirty">>(createInitialState(team))
  const [formData, setFormData] = useState<TeamFormData>({
    ...initialState,
    isDirty: false,
  })

  const hasCreateAccess = useRbac(RbacResource.Teams, RbacOperation.Create)
  const hasUpdateAccess = useRbac(RbacResource.Teams, RbacOperation.Update)
  const hasPermission = isEditing ? hasUpdateAccess : hasCreateAccess

  // RTK Query hooks
  const [createTeam, { isLoading: isCreating }] = useCreateTeamMutation()
  const [updateTeam, { isLoading: isUpdating }] = useUpdateTeamMutation()
  const loading = isCreating || isUpdating

	// Track isDirty state
	useEffect(() => {
		const currentData = {
			name: formData.name,
			customerId: formData.customerId,
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
	}, [formData.name, formData.customerId, formData.budgetMaxLimit, formData.budgetResetDuration, formData.tokenMaxLimit, formData.tokenResetDuration, formData.requestMaxLimit, formData.requestResetDuration, initialState]);

	// Parse string values to numbers for validation and submission
	const budgetMaxLimitNum = formData.budgetMaxLimit ? parseFloat(formData.budgetMaxLimit) : undefined;
	const tokenMaxLimitNum = formData.tokenMaxLimit ? parseInt(formData.tokenMaxLimit) : undefined;
	const requestMaxLimitNum = formData.requestMaxLimit ? parseInt(formData.requestMaxLimit) : undefined;

	// Validation
	const validator = useMemo(
		() =>
			new Validator([
				// Basic validation
				Validator.required(formData.name.trim(), "Team name is required"),

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

	const updateField = <K extends keyof TeamFormData>(field: K, value: TeamFormData[K]) => {
		setFormData((prev) => ({ ...prev, [field]: value }));
	};

	const handleSubmit = async (e: React.FormEvent) => {
		e.preventDefault();

		if (!validator.isValid()) {
			toast.error(validator.getFirstError());
			return;
		}

		try {
			if (isEditing && team) {
				// Update existing team
				const updateData: UpdateTeamRequest = {
					name: formData.name,
					customer_id: formData.customerId,
				};

				// Detect budget changes using had/has pattern
				const hadBudget = !!team.budget;
				const hasBudget = !!budgetMaxLimitNum;
				if (hasBudget) {
					updateData.budget = {
						max_limit: budgetMaxLimitNum,
						reset_duration: formData.budgetResetDuration,
					};
				} else if (hadBudget) {
					updateData.budget = {} as UpdateTeamRequest["budget"];
				}

				// Detect rate limit changes using had/has pattern
				const hadRateLimit = !!team.rate_limit;
				const hasRateLimit = !!tokenMaxLimitNum || !!requestMaxLimitNum;
				if (hasRateLimit) {
					updateData.rate_limit = {
						token_max_limit: tokenMaxLimitNum,
						token_reset_duration: tokenMaxLimitNum ? formData.tokenResetDuration : undefined,
						request_max_limit: requestMaxLimitNum,
						request_reset_duration: requestMaxLimitNum ? formData.requestResetDuration : undefined,
					};
				} else if (hadRateLimit) {
					updateData.rate_limit = {} as UpdateTeamRequest["rate_limit"];
				}

				await updateTeam({ teamId: team.id, data: updateData }).unwrap();
				toast.success("Team updated successfully");
			} else {
				// Create new team
				const createData: CreateTeamRequest = {
					name: formData.name,
					customer_id: formData.customerId || undefined,
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

				await createTeam(createData).unwrap();
				toast.success("Team created successfully");
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
					<DialogTitle className="flex items-center gap-2">{isEditing ? "Edit Team" : "Create Team"}</DialogTitle>
					<DialogDescription>
						{isEditing ? "Update the team information and settings." : "Create a new team to organize users and manage shared resources."}
					</DialogDescription>
				</DialogHeader>

				<form onSubmit={handleSubmit} className="space-y-6">
					<div className="space-y-6">
						{/* Basic Information */}
						<div className="space-y-6">
							<div className="space-y-2">
								<Label htmlFor="name">Team Name *</Label>
								<Input
									id="name"
									placeholder="e.g., Engineering Team"
									value={formData.name}
									maxLength={50}
									onChange={(e) => updateField("name", e.target.value)}
								/>
							</div>

							{/* Customer Assignment */}
							{customers?.length > 0 && (
								<div className="space-y-2">
									<Label htmlFor="customer">Customer (optional)</Label>
									<Select
										value={formData.customerId || "__none__"}
										onValueChange={(value) => updateField("customerId", value === "__none__" ? "" : value)}
									>
										<SelectTrigger id="customer" className="w-full" data-testid="team-customer-select-trigger">
											<SelectValue placeholder="Select a customer" />
										</SelectTrigger>
										<SelectContent>
											<SelectItem value="__none__" data-testid="team-customer-option-none">None</SelectItem>
											{customers.map((customer) => (
												<SelectItem key={customer.id} value={customer.id} data-testid={`team-customer-option-${customer.id}`}>
													{customer.name}
												</SelectItem>
											))}
										</SelectContent>
									</Select>
									<p className="text-muted-foreground text-sm">Assign to a customer or leave independent.</p>
								</div>
							)}
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
						{isEditing && (team?.budget || team?.rate_limit) && (
							<div className="rounded-lg border bg-muted/50 p-4 space-y-4">
								<p className="text-sm font-medium">Current Usage</p>
								<div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
									{team?.budget && (
										<div className="space-y-1">
											<p className="text-muted-foreground text-xs">Budget</p>
											<div className="flex items-center gap-2">
												<span className="font-mono text-sm">
													{formatCurrency(team.budget.current_usage)} / {formatCurrency(team.budget.max_limit)}
												</span>
												<Badge
													variant={team.budget.current_usage >= team.budget.max_limit ? "destructive" : "default"}
													className="text-xs"
												>
													{Math.round((team.budget.current_usage / team.budget.max_limit) * 100)}%
												</Badge>
											</div>
											<p className="text-muted-foreground text-xs">
												Last Reset: {formatDistanceToNow(new Date(team.budget.last_reset), { addSuffix: true })}
											</p>
										</div>
									)}
									{team?.rate_limit?.token_max_limit && (
										<div className="space-y-1">
											<p className="text-muted-foreground text-xs">Tokens</p>
											<div className="flex items-center gap-2">
												<span className="font-mono text-sm">
													{team.rate_limit.token_current_usage.toLocaleString()} / {team.rate_limit.token_max_limit.toLocaleString()}
												</span>
												<Badge
													variant={team.rate_limit.token_current_usage >= team.rate_limit.token_max_limit ? "destructive" : "default"}
													className="text-xs"
												>
													{Math.round((team.rate_limit.token_current_usage / team.rate_limit.token_max_limit) * 100)}%
												</Badge>
											</div>
											<p className="text-muted-foreground text-xs">
												Last Reset: {formatDistanceToNow(new Date(team.rate_limit.token_last_reset), { addSuffix: true })}
											</p>
										</div>
									)}
									{team?.rate_limit?.request_max_limit && (
										<div className="space-y-1">
											<p className="text-muted-foreground text-xs">Requests</p>
											<div className="flex items-center gap-2">
												<span className="font-mono text-sm">
													{team.rate_limit.request_current_usage.toLocaleString()} / {team.rate_limit.request_max_limit.toLocaleString()}
												</span>
												<Badge
													variant={team.rate_limit.request_current_usage >= team.rate_limit.request_max_limit ? "destructive" : "default"}
													className="text-xs"
												>
													{Math.round((team.rate_limit.request_current_usage / team.rate_limit.request_max_limit) * 100)}%
												</Badge>
											</div>
											<p className="text-muted-foreground text-xs">
												Last Reset: {formatDistanceToNow(new Date(team.rate_limit.request_last_reset), { addSuffix: true })}
											</p>
										</div>
									)}
								</div>
							</div>
						)}
					</div>

					<FormFooter validator={validator} label="Team" onCancel={onCancel} isLoading={loading} isEditing={isEditing} hasPermission={hasPermission} />
				</form>
			</DialogContent>
		</Dialog>
	);
}
