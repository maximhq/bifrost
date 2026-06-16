import FormFooter from "@/components/formFooter";
import { Badge } from "@/components/ui/badge";
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
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import NumberAndSelect from "@/components/ui/numberAndSelect";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { resetDurationOptions, supportsCalendarAlignment } from "@/lib/constants/governance";
import { getErrorMessage, useCreateTeamMutation, useUpdateTeamMutation } from "@/lib/store";
import { CreateTeamRequest, Customer, Team, UpdateTeamRequest } from "@/lib/types/governance";
import { formatCurrency } from "@/lib/utils/governance";
import { Validator } from "@/lib/utils/validation";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { formatDistanceToNow } from "date-fns";
import isEqual from "lodash.isequal";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

interface TeamDialogProps {
	team?: Team | null;
	customers: Customer[];
	onSave: () => void;
	onCancel: () => void;
}

interface TeamFormData {
	name: string;
	customerId: string;
	// Budget
	budgetMaxLimit: number | undefined;
	budgetResetDuration: string;
	budgetCalendarAligned: boolean;
	// Rate Limit
	tokenMaxLimit: number | undefined;
	tokenResetDuration: string;
	requestMaxLimit: number | undefined;
	requestResetDuration: string;
	isDirty: boolean;
}

// Helper function to create initial state
const createInitialState = (team?: Team | null): Omit<TeamFormData, "isDirty"> => {
	return {
		name: team?.name || "",
		customerId: team?.customer_id || "",
		// Budget
		budgetMaxLimit: team?.budget?.max_limit ?? undefined,
		budgetResetDuration: team?.budget?.reset_duration || "1M",
		budgetCalendarAligned: team?.budget?.calendar_aligned ?? false,
		// Rate Limit
		tokenMaxLimit: team?.rate_limit?.token_max_limit ?? undefined,
		tokenResetDuration: team?.rate_limit?.token_reset_duration || "1h",
		requestMaxLimit: team?.rate_limit?.request_max_limit ?? undefined,
		requestResetDuration: team?.rate_limit?.request_reset_duration || "1h",
	};
};

export default function TeamDialog({ team, customers, onSave, onCancel }: TeamDialogProps) {
	const isEditing = !!team;
	const [initialState] = useState<Omit<TeamFormData, "isDirty">>(createInitialState(team));
	const [formData, setFormData] = useState<TeamFormData>({
		...initialState,
		isDirty: false,
	});

	const hasCreateAccess = useRbac(RbacResource.Teams, RbacOperation.Create);
	const hasUpdateAccess = useRbac(RbacResource.Teams, RbacOperation.Update);
	const hasPermission = isEditing ? hasUpdateAccess : hasCreateAccess;

	// RTK Query hooks
	const [createTeam, { isLoading: isCreating }] = useCreateTeamMutation();
	const [updateTeam, { isLoading: isUpdating }] = useUpdateTeamMutation();
	const loading = isCreating || isUpdating;

	const [showCalendarAlignWarning, setShowCalendarAlignWarning] = useState(false);

	const handleCalendarAlignedChange = (checked: boolean) => {
		if (checked && isEditing && team?.budget && !team.budget.calendar_aligned) {
			setShowCalendarAlignWarning(true);
		} else {
			updateField("budgetCalendarAligned", checked);
		}
	};

	// Track isDirty state
	useEffect(() => {
		const currentData = {
			name: formData.name,
			customerId: formData.customerId,
			budgetMaxLimit: formData.budgetMaxLimit,
			budgetResetDuration: formData.budgetResetDuration,
			budgetCalendarAligned: formData.budgetCalendarAligned,
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
		formData.customerId,
		formData.budgetMaxLimit,
		formData.budgetResetDuration,
		formData.budgetCalendarAligned,
		formData.tokenMaxLimit,
		formData.tokenResetDuration,
		formData.requestMaxLimit,
		formData.requestResetDuration,
		initialState,
	]);

	// Values for validation and submission (already numbers)
	const budgetMaxLimitNum = formData.budgetMaxLimit;
	const tokenMaxLimitNum = formData.tokenMaxLimit;
	const requestMaxLimitNum = formData.requestMaxLimit;

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
				const hasBudget = budgetMaxLimitNum !== undefined && budgetMaxLimitNum !== null;
				if (hasBudget) {
					updateData.budget = {
						max_limit: budgetMaxLimitNum,
						reset_duration: formData.budgetResetDuration,
						calendar_aligned: formData.budgetCalendarAligned,
					};
				} else if (hadBudget) {
					updateData.budget = {} as UpdateTeamRequest["budget"];
				}

				// Detect rate limit changes using had/has pattern
				const hadRateLimit = !!team.rate_limit;
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
				if (budgetMaxLimitNum !== undefined && budgetMaxLimitNum !== null) {
					createData.budget = {
						max_limit: budgetMaxLimitNum,
						reset_duration: formData.budgetResetDuration,
						calendar_aligned: formData.budgetCalendarAligned,
					};
				}

				// Add rate limit if enabled (token or request limits)
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
			<DialogContent className="max-w-2xl" data-testid="team-dialog-content">
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
									data-testid="team-name-input"
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
											<SelectItem value="__none__" data-testid="team-customer-option-none">
												None
											</SelectItem>
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
							onChangeSelect={(value) => {
								updateField("budgetResetDuration", value);
								if (!supportsCalendarAlignment(value)) {
									updateField("budgetCalendarAligned", false);
								}
							}}
							options={resetDurationOptions}
							dataTestId="budget-max-limit-input"
						/>

						{/* Calendar alignment toggle — only shown when a budget is set and the period supports alignment */}
						{formData.budgetMaxLimit && supportsCalendarAlignment(formData.budgetResetDuration) && (
							<div className="flex items-center justify-between gap-4 rounded-md border px-3 py-2">
								<div className="space-y-0.5">
									<Label htmlFor="team-budget-calendar-aligned-toggle" className="text-sm font-normal">
										Align to calendar cycle
									</Label>
									<p id="team-budget-calendar-aligned-description" className="text-muted-foreground text-xs">
										Reset at the start of each period (e.g. 1st of month) instead of rolling from creation date
									</p>
								</div>
								<Switch
									id="team-budget-calendar-aligned-toggle"
									aria-describedby="team-budget-calendar-aligned-description"
									checked={formData.budgetCalendarAligned}
									onCheckedChange={handleCalendarAlignedChange}
									data-testid="team-budget-calendar-aligned-toggle"
								/>
							</div>
						)}

						{/* Warning dialog shown when enabling calendar alignment on an existing budget */}
						<AlertDialog open={showCalendarAlignWarning} onOpenChange={setShowCalendarAlignWarning}>
							<AlertDialogContent>
								<AlertDialogHeader>
									<AlertDialogTitle>Reset budget usage?</AlertDialogTitle>
									<AlertDialogDescription>
										Enabling calendar alignment will reset this budget&apos;s current usage to <span className="font-semibold">$0.00</span>{" "}
										and snap the reset date to the start of the current{" "}
										{formData.budgetResetDuration === "1d"
											? "day"
											: formData.budgetResetDuration === "1w"
												? "week"
												: formData.budgetResetDuration === "1M"
													? "month"
													: formData.budgetResetDuration === "1Y"
														? "year"
														: "period"}
										. The usage reset to $0.00 cannot be undone, but calendar alignment can be turned off later. This will take effect when
										you save.
									</AlertDialogDescription>
								</AlertDialogHeader>
								<AlertDialogFooter>
									<AlertDialogCancel data-testid="team-calendar-align-cancel-btn">Cancel</AlertDialogCancel>
									<AlertDialogAction
										data-testid="team-calendar-align-enable-btn"
										onClick={() => {
											updateField("budgetCalendarAligned", true);
											setShowCalendarAlignWarning(false);
										}}
									>
										Enable Calendar Alignment
									</AlertDialogAction>
								</AlertDialogFooter>
							</AlertDialogContent>
						</AlertDialog>

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
							<div className="bg-muted/50 space-y-4 rounded-lg border p-4">
								<p className="text-sm font-medium">Current Usage</p>
								<div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
									{team?.budget && (
										<div className="space-y-1">
											<p className="text-muted-foreground text-xs">Budget</p>
											<div className="flex items-center gap-2">
												<span className="font-mono text-sm">
													{formatCurrency(team.budget.current_usage)} / {formatCurrency(team.budget.max_limit)}
												</span>
												<Badge variant={team.budget.current_usage >= team.budget.max_limit ? "destructive" : "default"} className="text-xs">
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

					<FormFooter
						validator={validator}
						label="Team"
						onCancel={onCancel}
						isLoading={loading}
						isEditing={isEditing}
						hasPermission={hasPermission}
					/>
				</form>
			</DialogContent>
		</Dialog>
	);
}