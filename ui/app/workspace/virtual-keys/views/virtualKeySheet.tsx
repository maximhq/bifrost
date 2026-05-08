import { useVirtualKeyUsage } from "@/app/workspace/virtual-keys/hooks/useVirtualKeyUsage";
import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from "@/components/ui/accordion";
import { Alert, AlertDescription } from "@/components/ui/alert";
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
import { AsyncMultiSelect } from "@/components/ui/asyncMultiselect";
import { Button } from "@/components/ui/button";
import { ComboboxSelect } from "@/components/ui/combobox";
import { ConfigSyncAlert } from "@/components/ui/configSyncAlert";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import MultiBudgetLines from "@/components/ui/multibudgets";
import { MultiSelect } from "@/components/ui/multiSelect";
import NumberAndSelect from "@/components/ui/numberAndSelect";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import Toggle from "@/components/ui/toggle";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/components/ui/utils";
import { ModelPlaceholders } from "@/lib/constants/config";
import { resetDurationOptions, supportsCalendarAlignment } from "@/lib/constants/governance";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels, ProviderName } from "@/lib/constants/logs";
import {
	getErrorMessage,
	useCreateVirtualKeyMutation,
	useGetAllKeysQuery,
	useGetMCPClientsQuery,
	useGetProvidersQuery,
	useUpdateVirtualKeyMutation,
} from "@/lib/store";
import { KnownProvider } from "@/lib/types/config";
import { CreateVirtualKeyRequest, Customer, Team, UpdateVirtualKeyRequest, VirtualKey } from "@/lib/types/governance";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { useNavigate } from "@tanstack/react-router";
import { Info, Lock, RotateCcw, Trash2, Users, X } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { components, MultiValueProps, OptionProps } from "react-select";
import { toast } from "sonner";
import { z } from "zod";
import i18n from "@/lib/i18n";

interface VirtualKeySheetProps {
	virtualKey?: VirtualKey | null;
	teams: Team[];
	customers: Customer[];
	// When set and not editing, the new VK is created owned by this team and the sheet locks
	// all fields except name/description (same treatment as access-profile-managed keys).
	defaultTeamId?: string;
	onSave: () => void;
	onCancel: () => void;
}

// Provider configuration schema
const providerConfigSchema = z.object({
	id: z.number().optional(),
	provider: z.string().min(1, i18n.t("workspace.virtualKeys.providerRequired")),
	weight: z.number().min(0, i18n.t("workspace.virtualKeys.weightMin")).max(1, i18n.t("workspace.virtualKeys.weightMax")).optional(),
	allowed_models: z.array(z.string()).optional(),
	key_ids: z.array(z.string()).optional(), // Keys associated with this provider config
	// Provider-level budget
	budgets: z
		.array(
			z.object({
				max_limit: z.number().nonnegative().optional(),
				reset_duration: z.string().optional(),
			}),
		)
		.optional(),
	// Provider-level rate limits
	rate_limit: z
		.object({
			token_max_limit: z.number().int().nonnegative().optional(),
			token_reset_duration: z.string().optional(),
			request_max_limit: z.number().int().nonnegative().optional(),
			request_reset_duration: z.string().optional(),
		})
		.optional(),
});

const mcpConfigSchema = z.object({
	id: z.number().optional(),
	mcp_client_name: z.string().min(1, i18n.t("workspace.virtualKeys.mcpClientNameRequired")),
	tools_to_execute: z.array(z.string()).optional(),
});

// Main form schema
const formSchema = z
	.object({
		name: z.string().min(1, i18n.t("workspace.virtualKeys.virtualKeyNameRequired")),
		description: z.string().optional(),
		providerConfigs: z.array(providerConfigSchema).optional(),
		mcpConfigs: z.array(mcpConfigSchema).optional(),
		entityType: z.enum(["team", "customer", "none"]),
		teamId: z.string().optional(),
		customerId: z.string().optional(),
		isActive: z.boolean(),
		// Budget
		budgetCalendarAligned: z.boolean(),
		budgets: z
			.array(
				z.object({
					max_limit: z.number().nonnegative().optional(),
					reset_duration: z.string(),
				}),
			)
			.optional(),
		// Token limits
		tokenMaxLimit: z.number().int().nonnegative().optional(),
		tokenResetDuration: z.string().optional(),
		// Request limits
		requestMaxLimit: z.number().int().nonnegative().optional(),
		requestResetDuration: z.string().optional(),
	})
	.refine(
		(data) => {
			// If entityType is "team", teamId must be provided and not empty
			if (data.entityType === "team") {
				return data.teamId && data.teamId.trim() !== "";
			}
			// If entityType is "customer", customerId must be provided and not empty
			if (data.entityType === "customer") {
				return data.customerId && data.customerId.trim() !== "";
			}
			return true;
		},
		{
			message: i18n.t("workspace.virtualKeys.validAssignmentRequired"),
			path: ["entityType"], // This will show the error on the entityType field
		},
	);

type FormData = z.infer<typeof formSchema>;

type VirtualKeyType = {
	label: string;
	value: string;
	description: string;
	provider: string;
};

export default function VirtualKeySheet({ virtualKey, teams, customers, defaultTeamId, onSave, onCancel }: VirtualKeySheetProps) {
	const [isOpen, setIsOpen] = useState(true);
	const navigate = useNavigate();
	const isEditing = !!virtualKey;

	const hasCreateAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.Create);
	const hasUpdateAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.Update);
	const canSubmit = isEditing ? hasUpdateAccess : hasCreateAccess;

	// Detect AP-managed status via the managing profile's virtual_key_ids, not just by the presence
	// of assignees — directly-attached users don't imply an access-profile relation.
	const { assignedUsers, isManagedByProfile: isManagedByProfileHook } = useVirtualKeyUsage(virtualKey);
	const isManagedByProfile = isEditing && isManagedByProfileHook;
	// Team attachment: when a VK already belongs to a team (edit) or will be created for one
	// (create from team detail sheet via defaultTeamId), the team assignment is fixed — users
	// can still edit providers/budgets/rate limits/MCP, but not reparent the VK.
	const attachedTeamId = isEditing ? virtualKey?.team_id || "" : defaultTeamId || "";
	const attachedTeam = attachedTeamId ? teams.find((t) => t.id === attachedTeamId) : undefined;
	const isTeamLocked = !!attachedTeamId;

	const handleClose = () => {
		setIsOpen(false);
		setTimeout(() => {
			onCancel();
		}, 150); // Slightly longer than the 100ms animation duration
	};

	// RTK Query hooks
	const { data: providersData, error: providersError } = useGetProvidersQuery();
	const { data: keysData, error: keysError } = useGetAllKeysQuery();
	const [createVirtualKey, { isLoading: isCreating }] = useCreateVirtualKeyMutation();
	const [updateVirtualKey, { isLoading: isUpdating }] = useUpdateVirtualKeyMutation();
	const { data: mcpClientsResponse, error: mcpClientsError } = useGetMCPClientsQuery();
	const mcpClientsData = mcpClientsResponse?.clients || [];
	const isLoading = isCreating || isUpdating;

	const availableKeys = keysData || [];
	const availableProviders = providersData || [];

	// Form setup
	const form = useForm<z.input<typeof formSchema>, unknown, FormData>({
		resolver: zodResolver(formSchema),
		defaultValues: {
			name: virtualKey?.name || "",
			description: virtualKey?.description || "",
			providerConfigs:
				virtualKey?.provider_configs?.map((config) => ({
					id: config.id,
					provider: config.provider,
					weight: config.weight ?? undefined,
					allowed_models: config.allowed_models,
					key_ids: config.allow_all_keys ? ["*"] : config.keys?.map((key) => key.key_id) || [],
					budgets: config.budgets?.map((b) => ({
						max_limit: b.max_limit,
						reset_duration: b.reset_duration,
					})),
					rate_limit: config.rate_limit
						? {
								token_max_limit: config.rate_limit.token_max_limit ?? undefined,
								token_reset_duration: config.rate_limit.token_reset_duration,
								request_max_limit: config.rate_limit.request_max_limit ?? undefined,
								request_reset_duration: config.rate_limit.request_reset_duration,
							}
						: undefined,
				})) || [],
			mcpConfigs:
				virtualKey?.mcp_configs?.map((config) => ({
					id: config.id,
					mcp_client_name: config.mcp_client?.name || "",
					tools_to_execute: config.tools_to_execute || [],
				})) || [],
			entityType: virtualKey?.team_id ? "team" : virtualKey?.customer_id ? "customer" : !isEditing && defaultTeamId ? "team" : "none",
			teamId: virtualKey?.team_id || (!isEditing ? defaultTeamId || "" : ""),
			customerId: virtualKey?.customer_id || "",
			isActive: virtualKey?.is_active ?? true,
			budgets:
				virtualKey?.budgets && virtualKey.budgets.length > 0
					? virtualKey.budgets.map((b) => ({ max_limit: b.max_limit, reset_duration: b.reset_duration ?? "1M" }))
					: [],
			budgetCalendarAligned: virtualKey?.calendar_aligned ?? false,
			tokenMaxLimit: virtualKey?.rate_limit?.token_max_limit ?? undefined,
			tokenResetDuration: virtualKey?.rate_limit?.token_reset_duration || "1h",
			requestMaxLimit: virtualKey?.rate_limit?.request_max_limit ?? undefined,
			requestResetDuration: virtualKey?.rate_limit?.request_reset_duration || "1h",
		},
	});

	// Handle keys loading error
	useEffect(() => {
		if (keysError) {
			toast.error(`Failed to load available keys: ${getErrorMessage(keysError)}`);
		}
	}, [keysError]);

	// Handle providers loading error
	useEffect(() => {
		if (providersError) {
			toast.error(`Failed to load available providers: ${getErrorMessage(providersError)}`);
		}
	}, [providersError]);

	// Handle mcp clients loading error
	useEffect(() => {
		if (mcpClientsError) {
			toast.error(`Failed to load available MCP clients: ${getErrorMessage(mcpClientsError)}`);
		}
	}, [mcpClientsError]);

	// Clear team/customer IDs when entityType changes to "none"
	useEffect(() => {
		const entityType = form.watch("entityType");
		if (entityType === "none") {
			form.setValue("teamId", "", { shouldDirty: true });
			form.setValue("customerId", "", { shouldDirty: true });
		} else if (entityType === "team") {
			form.setValue("customerId", "", { shouldDirty: true });
		} else if (entityType === "customer") {
			form.setValue("teamId", "", { shouldDirty: true });
		}
	}, [form.watch("entityType"), form]);

	// Provider configuration state
	const [selectedProvider, setSelectedProvider] = useState<string>("");

	// MCP client configuration state
	const [selectedMCPClient, setSelectedMCPClient] = useState<string>("");

	// Get current provider configs from form
	const providerConfigs = form.watch("providerConfigs") || [];

	// Get current MCP configs from form
	const mcpConfigs = form.watch("mcpConfigs") || [];

	// Watch budget/rate-limit fields for conditional rendering of reset buttons
	const watchedBudgets = form.watch("budgets");
	const watchedTokenMaxLimit = form.watch("tokenMaxLimit");
	const watchedRequestMaxLimit = form.watch("requestMaxLimit");
	const watchedBudgetCalendarAligned = form.watch("budgetCalendarAligned");

	// Calendar alignment is VK-wide: show toggle if any budget has a max_limit and supports alignment
	const hasAnyAlignableBudget =
		watchedBudgets &&
		watchedBudgets.length > 0 &&
		watchedBudgets.some((b) => b.max_limit !== undefined && b.max_limit !== null && supportsCalendarAlignment(b.reset_duration || "1M"));

	// Handle adding a new provider configuration
	const handleAddProvider = (provider: string) => {
		const existingConfig = providerConfigs.find((config) => config.provider === provider);
		if (existingConfig) {
			toast.error(i18n.t("workspace.virtualKeys.providerAlreadyConfigured"));
			return;
		}

		const newConfig = {
			provider: provider,
			weight: undefined as number | undefined, // undefined = excluded from weighted routing until user sets a weight
			allowed_models: ["*"],
			key_ids: ["*"],
		};

		form.setValue("providerConfigs", [...providerConfigs, newConfig], { shouldDirty: true });
	};

	// Handle removing a provider configuration
	const handleRemoveProvider = (index: number) => {
		const updatedConfigs = providerConfigs.filter((_, i) => i !== index);
		form.setValue("providerConfigs", updatedConfigs, { shouldDirty: true });
	};

	// Handle updating provider configuration
	const handleUpdateProviderConfig = (index: number, field: string, value: any) => {
		const updatedConfigs = [...providerConfigs];
		updatedConfigs[index] = { ...updatedConfigs[index], [field]: value };
		form.setValue("providerConfigs", updatedConfigs, { shouldDirty: true });
	};

	// Handle adding a new MCP client configuration
	const handleAddMCPClient = (mcpClientName: string) => {
		const existingConfig = mcpConfigs.find((config) => config.mcp_client_name === mcpClientName);
		if (existingConfig) {
			toast.error(i18n.t("workspace.virtualKeys.mcpClientAlreadyConfigured"));
			return;
		}

		const newConfig = {
			mcp_client_name: mcpClientName,
			tools_to_execute: ["*"],
		};

		form.setValue("mcpConfigs", [...mcpConfigs, newConfig], { shouldDirty: true });
	};

	// Handle removing an MCP client configuration
	const handleRemoveMCPClient = (index: number) => {
		const updatedConfigs = mcpConfigs.filter((_, i) => i !== index);
		form.setValue("mcpConfigs", updatedConfigs, { shouldDirty: true });
	};

	// Handle updating MCP client configuration
	const handleUpdateMCPConfig = (index: number, field: keyof (typeof mcpConfigs)[0], value: any) => {
		const updatedConfigs = [...mcpConfigs];
		updatedConfigs[index] = { ...updatedConfigs[index], [field]: value };
		form.setValue("mcpConfigs", updatedConfigs, { shouldDirty: true });
	};

	const [showCalendarAlignWarning, setShowCalendarAlignWarning] = useState(false);

	const handleCalendarAlignedChange = (checked: boolean) => {
		if (checked && isEditing) {
			// Show warning when enabling on an existing VK
			setShowCalendarAlignWarning(true);
		} else {
			form.setValue("budgetCalendarAligned", checked, { shouldDirty: true });
		}
	};

	const clearVirtualKeyBudget = () => {
		form.setValue("budgets", [], { shouldDirty: true });
		form.setValue("budgetCalendarAligned", false, { shouldDirty: true });
	};

	const clearVirtualKeyRateLimits = () => {
		form.setValue("tokenMaxLimit", undefined, { shouldDirty: true });
		form.setValue("tokenResetDuration", "1h", { shouldDirty: true });
		form.setValue("requestMaxLimit", undefined, { shouldDirty: true });
		form.setValue("requestResetDuration", "1h", { shouldDirty: true });
	};

	const normalizeProviderConfigs = (configs: typeof providerConfigs, existingConfigs?: VirtualKey["provider_configs"]): any[] => {
		return configs.map((config) => ({
			...config,
			budgets: config.budgets?.filter((b): b is { max_limit: number; reset_duration: string } => b.max_limit !== undefined),
			weight: config.weight ?? null,
			rate_limit: (() => {
				const hasTokenMaxLimit = config.rate_limit?.token_max_limit !== undefined;
				const hasRequestMaxLimit = config.rate_limit?.request_max_limit !== undefined;
				if (hasTokenMaxLimit || hasRequestMaxLimit) {
					return {
						token_max_limit: config.rate_limit?.token_max_limit ?? null,
						token_reset_duration: hasTokenMaxLimit ? config.rate_limit?.token_reset_duration || "1h" : null,
						request_max_limit: config.rate_limit?.request_max_limit ?? null,
						request_reset_duration: hasRequestMaxLimit ? config.rate_limit?.request_reset_duration || "1h" : null,
					};
				}

				const existingConfig = existingConfigs?.find((item) => (config.id ? item.id === config.id : item.provider === config.provider));
				if (existingConfig?.rate_limit) {
					return {};
				}

				return undefined;
			})(),
		}));
	};

	// Handle form submission
	const onSubmit = async (data: FormData) => {
		if (!canSubmit) {
			toast.error(i18n.t("workspace.virtualKeys.permissionDenied"));
			return;
		}
		try {
			// Managed VKs only allow name + description updates; all other fields are owned by the access profile.
			if (isManagedByProfile && virtualKey) {
				await updateVirtualKey({
					vkId: virtualKey.id,
					data: {
						name: data.name,
						description: data.description,
					},
				}).unwrap();
				toast.success(i18n.t("workspace.virtualKeys.virtualKeyUpdated"));
				onSave();
				return;
			}

			// Normalize provider configs to ensure weights are numbers and handle budget/rate limits
			const normalizedProviderConfigs = data.providerConfigs
				? normalizeProviderConfigs(data.providerConfigs, virtualKey?.provider_configs)
				: [];
			if (isEditing && virtualKey) {
				// Update existing virtual key
				const updateData: UpdateVirtualKeyRequest = {
					name: data.name,
					description: data.description,
					provider_configs: normalizedProviderConfigs,
					mcp_configs: data.mcpConfigs,
					team_id: data.entityType === "team" && data.teamId && data.teamId.trim() !== "" ? data.teamId : undefined,
					customer_id: data.entityType === "customer" && data.customerId && data.customerId.trim() !== "" ? data.customerId : undefined,
					is_active: data.isActive,
				};

				// Add budgets if enabled
				const validBudgets = (data.budgets || []).filter(
					(b): b is { max_limit: number; reset_duration: string } => b.max_limit !== undefined,
				);
				const hadBudget = virtualKey.budgets && virtualKey.budgets.length > 0;
				if (validBudgets.length > 0) {
					updateData.budgets = validBudgets;
					updateData.calendar_aligned = data.budgetCalendarAligned;
				} else if (hadBudget) {
					updateData.budgets = [];
					updateData.calendar_aligned = false;
				}

				// Add rate limit if enabled
				const hadRateLimit = !!virtualKey.rate_limit;
				const hasTokenMaxLimit = data.tokenMaxLimit !== undefined;
				const hasRequestMaxLimit = data.requestMaxLimit !== undefined;
				const hasRateLimit = hasTokenMaxLimit || hasRequestMaxLimit;
				if (hasRateLimit) {
					updateData.rate_limit = {
						token_max_limit: data.tokenMaxLimit ?? null,
						token_reset_duration: hasTokenMaxLimit ? data.tokenResetDuration || "1h" : null,
						request_max_limit: data.requestMaxLimit ?? null,
						request_reset_duration: hasRequestMaxLimit ? data.requestResetDuration || "1h" : null,
					};
				} else if (hadRateLimit) {
					updateData.rate_limit = {};
				}

				await updateVirtualKey({ vkId: virtualKey.id, data: updateData }).unwrap();
				toast.success(i18n.t("workspace.virtualKeys.virtualKeyUpdatedSuccess"));
			} else {
				// Create new virtual key
				const createData: CreateVirtualKeyRequest = {
					name: data.name,
					description: data.description || undefined,
					provider_configs: normalizedProviderConfigs,
					mcp_configs: data.mcpConfigs,
					team_id: data.entityType === "team" && data.teamId && data.teamId.trim() !== "" ? data.teamId : undefined,
					customer_id: data.entityType === "customer" && data.customerId && data.customerId.trim() !== "" ? data.customerId : undefined,
					is_active: data.isActive,
				};

				// Add budgets if enabled
				const validBudgets = (data.budgets || []).filter(
					(b): b is { max_limit: number; reset_duration: string } => b.max_limit !== undefined,
				);
				if (validBudgets.length > 0) {
					createData.budgets = validBudgets;
					createData.calendar_aligned = data.budgetCalendarAligned;
				}

				// Add rate limit if enabled
				const hasTokenMaxLimit = data.tokenMaxLimit !== undefined;
				const hasRequestMaxLimit = data.requestMaxLimit !== undefined;
				if (hasTokenMaxLimit || hasRequestMaxLimit) {
					createData.rate_limit = {
						token_max_limit: data.tokenMaxLimit,
						token_reset_duration: hasTokenMaxLimit ? data.tokenResetDuration || "1h" : undefined,
						request_max_limit: data.requestMaxLimit,
						request_reset_duration: hasRequestMaxLimit ? data.requestResetDuration || "1h" : undefined,
					};
				}

				await createVirtualKey(createData).unwrap();
				toast.success(i18n.t("workspace.virtualKeys.virtualKeyCreatedSuccess"));
			}

			onSave();
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	return (
		<Sheet open={isOpen} onOpenChange={(open) => !open && handleClose()}>
			<SheetContent
				className="flex w-full flex-col gap-4 overflow-x-hidden p-0 pt-4"
				data-testid="vk-sheet-content"
				onInteractOutside={(e) => e.preventDefault()}
				onEscapeKeyDown={() => handleClose()}
			>
				<SheetHeader className="flex flex-col items-start px-8 py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10">
					<SheetTitle className="flex items-center gap-2">
						{isEditing ? virtualKey?.name : i18n.t("workspace.virtualKeys.createVirtualKey")}
					</SheetTitle>
					<SheetDescription>
						{isEditing
							? i18n.t("workspace.virtualKeys.updateVirtualKeyDescription")
							: i18n.t("workspace.virtualKeys.createVirtualKeyDescription")}
					</SheetDescription>
				</SheetHeader>

				<Form {...form}>
					<form onSubmit={form.handleSubmit(onSubmit)} className="flex h-full flex-col gap-6">
						<div className="space-y-4 px-8">
							{isManagedByProfile && (
								<Alert variant="info">
									<Lock className="h-4 w-4" />
									<AlertDescription>{i18n.t("workspace.virtualKeys.managedByAccessProfileDescription")}</AlertDescription>
								</Alert>
							)}

							{isTeamLocked && !isManagedByProfile && (
								<Alert variant="info">
									<Users className="h-4 w-4" />
									<AlertDescription>
										<p>
											{i18n.t("workspace.virtualKeys.teamAttachedDescription", {
												teamName: attachedTeam?.name ?? virtualKey?.team?.name ?? attachedTeamId,
											})}
										</p>
									</AlertDescription>
								</Alert>
							)}

							{/* Assigned User */}
							{assignedUsers.length > 0 && (
								<div className="space-y-1">
									<Label className="text-sm font-medium">{i18n.t("workspace.virtualKeys.assignedTo")}</Label>
									<div className="flex items-center gap-2">
										<Users className="text-muted-foreground h-4 w-4" />
										<span className="text-sm">{assignedUsers.map((u) => u.name || u.email).join(", ")}</span>
									</div>
								</div>
							)}

							{/* Basic Information */}
							<div className="space-y-4">
								<FormField
									control={form.control}
									name="name"
									render={({ field }) => (
										<FormItem>
											<FormLabel>{i18n.t("workspace.virtualKeys.nameLabel")}</FormLabel>
											<FormControl>
												<Input placeholder={i18n.t("workspace.virtualKeys.namePlaceholder")} data-testid="vk-name-input" {...field} />
											</FormControl>
											<FormMessage />
										</FormItem>
									)}
								/>

								<FormField
									control={form.control}
									name="description"
									render={({ field }) => (
										<FormItem>
											<FormLabel>{i18n.t("workspace.virtualKeys.descriptionLabel")}</FormLabel>
											<FormControl>
												<Textarea
													placeholder={i18n.t("workspace.virtualKeys.descriptionPlaceholder")}
													data-testid="vk-description-input"
													{...field}
													rows={3}
												/>
											</FormControl>
											<FormMessage />
										</FormItem>
									)}
								/>
							</div>
							<fieldset
								disabled={isManagedByProfile}
								aria-disabled={isManagedByProfile}
								inert={isManagedByProfile ? true : undefined}
								className={isManagedByProfile ? "pointer-events-none space-y-4 opacity-50" : "space-y-4"}
							>
								<div className="space-y-4">
									<FormField
										control={form.control}
										name="isActive"
										render={({ field }) => (
											<FormItem>
												<Toggle
													label={i18n.t("workspace.virtualKeys.isActiveLabel")}
													val={field.value}
													setVal={field.onChange}
													data-testid="vk-is-active-toggle"
												/>
											</FormItem>
										)}
									/>
								</div>
								{/* Provider Configurations */}
								<div className="space-y-2">
									<div className="flex items-center gap-2">
										<Label className="text-sm font-medium">{i18n.t("workspace.virtualKeys.providerConfigurationsLabel")}</Label>
										<TooltipProvider>
											<Tooltip>
												<TooltipTrigger asChild>
													<span>
														<Info className="text-muted-foreground h-3 w-3" />
													</span>
												</TooltipTrigger>
												<TooltipContent>
													<p>{i18n.t("workspace.virtualKeys.providerConfigurationsTooltip")}</p>
												</TooltipContent>
											</Tooltip>
										</TooltipProvider>
									</div>

									{/* Add Provider Dropdown */}
									<div className="flex gap-2">
										<Select
											value={selectedProvider}
											onValueChange={(provider) => {
												if (provider === "__manage_providers__") {
													navigate({ to: "/workspace/providers" });
													setSelectedProvider("");
													return;
												}
												handleAddProvider(provider);
												setSelectedProvider(""); // Reset to placeholder state
											}}
										>
											<SelectTrigger className="flex-1" data-testid="vk-provider-select">
												<SelectValue placeholder={i18n.t("workspace.virtualKeys.selectProviderToAdd")} />
											</SelectTrigger>
											<SelectContent>
												{(() => {
													// Filter out already configured providers
													const unconfiguredProviders = availableProviders.filter(
														(provider) => !providerConfigs.some((config) => config.provider === provider.name),
													);

													if (unconfiguredProviders.length === 0) {
														return (
															<SelectItem
																value="__manage_providers__"
																className="text-muted-foreground hover:text-foreground"
																data-testid="vk-provider-config-link"
															>
																<span>
																	{i18n.t("workspace.virtualKeys.noProvidersLeftToConfigure")}.{" "}
																	<span className="text-primary font-medium underline">{i18n.t("workspace.virtualKeys.clickToAdd")}</span>
																</span>
															</SelectItem>
														);
													}

													// Separate base providers and custom providers
													const baseProviders = unconfiguredProviders.filter((provider) => !provider.custom_provider_config);
													const customProviders = unconfiguredProviders.filter((provider) => provider.custom_provider_config);

													return (
														<>
															{/* Base providers first */}
															{baseProviders
																.filter((p) => p.name)
																.map((provider, index) => (
																	<SelectItem key={`base-${index}`} value={provider.name}>
																		<RenderProviderIcon provider={provider.name as KnownProvider} size="sm" className="h-4 w-4" />
																		{ProviderLabels[provider.name as ProviderName]}
																	</SelectItem>
																))}

															{/* Custom providers second */}
															{customProviders
																.filter((p) => p.name)
																.map((provider, index) => (
																	<SelectItem key={`custom-${index}`} value={provider.name}>
																		<RenderProviderIcon
																			provider={provider.custom_provider_config?.base_provider_type || (provider.name as KnownProvider)}
																			size="sm"
																			className="h-4 w-4"
																		/>
																		{provider.name}
																	</SelectItem>
																))}
														</>
													);
												})()}
											</SelectContent>
										</Select>
									</div>

									{/* Provider Configurations Table */}
									{providerConfigs.length > 0 && (
										<div className="rounded-md border px-2">
											<Accordion type="multiple" className="w-full">
												{providerConfigs.map((config, index) => {
													const providerConfig = availableProviders.find((provider) => provider.name === config.provider);
													return (
														<AccordionItem key={index} className="w-full" value={`${config.provider}-${index}`}>
															<AccordionTrigger className="flex h-12 items-center gap-0 px-1">
																<div className="flex w-full items-center justify-between">
																	<div className="flex w-fit items-center gap-2">
																		<RenderProviderIcon
																			provider={
																				providerConfig?.custom_provider_config?.base_provider_type || (config.provider as ProviderIconType)
																			}
																			size="sm"
																			className="h-4 w-4"
																		/>
																		{providerConfig?.custom_provider_config
																			? providerConfig.name
																			: ProviderLabels[config.provider as ProviderName]}
																	</div>
																	<Button
																		type="button"
																		variant="ghost"
																		size="icon"
																		aria-label={`Remove ${config.provider} provider`}
																		className="hover:bg-accent/50 h-8 w-8 rounded-sm p-2"
																		data-testid={`vk-delete-provider-${index}`}
																		onClick={(e) => {
																			e.stopPropagation();
																			handleRemoveProvider(index);
																		}}
																	>
																		<Trash2 className="h-4 w-4 opacity-75" />
																	</Button>
																</div>
															</AccordionTrigger>
															<AccordionContent className="flex flex-col gap-4 px-1 text-balance">
																<div className="flex w-full items-start gap-2">
																	<div className="w-1/4">
																		<NumberAndSelect
																			id={`vk-weight-${index}`}
																			label={i18n.t("workspace.virtualKeys.weight")}
																			labelClassName="text-sm font-medium"
																			placeholder={i18n.t("workspace.virtualKeys.excludeFromRouting")}
																			inputClassName="h-[38px] w-full"
																			dataTestId={`vk-weight-input-${index}`}
																			value={config.weight}
																			onChangeNumber={(value) => handleUpdateProviderConfig(index, "weight", value)}
																		/>
																	</div>
																	<div className="w-3/4 space-y-2">
																		<Label className="text-sm font-medium">
																			{i18n.t("workspace.virtualKeys.allowedModels")}{" "}
																			<span className="text-muted-foreground ml-auto text-xs italic">
																				{i18n.t("workspace.virtualKeys.typeToSearch")}
																			</span>
																		</Label>
																		{(() => {
																			const hasWildcardModels = (config.allowed_models || []).includes("*");
																			return (
																				<ModelMultiselect
																					data-testid={`vk-models-multiselect-${index}`}
																					provider={config.provider}
																					keys={(() => {
																						const providerKeys = availableKeys.filter((key) => key.provider === config.provider);
																						const configKeyIds = config.key_ids || [];
																						return configKeyIds.includes("*")
																							? providerKeys.map((key) => key.key_id)
																							: providerKeys.filter((key) => configKeyIds.includes(key.key_id)).map((key) => key.key_id);
																					})()}
																					allowAllOption={true}
																					value={hasWildcardModels ? ["*"] : config.allowed_models || []}
																					onChange={(models: string[]) => {
																						const hadStar = (config.allowed_models || []).includes("*");
																						const hasStar = models.includes("*");
																						if (!hadStar && hasStar) {
																							handleUpdateProviderConfig(index, "allowed_models", ["*"]);
																						} else if (hadStar && hasStar && models.length > 1) {
																							handleUpdateProviderConfig(
																								index,
																								"allowed_models",
																								models.filter((m) => m !== "*"),
																							);
																						} else {
																							handleUpdateProviderConfig(index, "allowed_models", models);
																						}
																					}}
																					placeholder={
																						hasWildcardModels
																							? i18n.t("workspace.virtualKeys.allModelsAllowed")
																							: (config.allowed_models || []).length === 0
																								? i18n.t("workspace.virtualKeys.noModelsDenyAll")
																								: config.provider
																									? ModelPlaceholders[config.provider as keyof typeof ModelPlaceholders] ||
																										ModelPlaceholders.default
																									: ModelPlaceholders.default
																					}
																					className="min-h-10 max-w-[500px] min-w-[200px]"
																				/>
																			);
																		})()}
																		<p className="text-muted-foreground text-xs">
																			{i18n.t("workspace.virtualKeys.selectSpecificModels")} "
																			{i18n.t("workspace.virtualKeys.allowAllModels")}"{" "}
																			{i18n.t("workspace.virtualKeys.leaveEmptyToDenyAll")}
																		</p>
																	</div>
																</div>

																{/* Allowed Keys for this provider */}
																{(() => {
																	const providerKeys = availableKeys.filter((key) => key.provider === config.provider);
																	const configKeyIds = config.key_ids || [];
																	const hasWildcard = configKeyIds.includes("*");
																	const allKeyOptions = [
																		{
																			label: i18n.t("workspace.virtualKeys.allowAll"),
																			value: "*",
																			description: i18n.t("workspace.virtualKeys.allowAllCurrentFutureKeys"),
																			provider: "",
																		},
																		...providerKeys.map((key) => ({
																			label: key.name,
																			value: key.key_id,
																			description:
																				key.models == null || key.models.includes("*")
																					? i18n.t("workspace.virtualKeys.allModels")
																					: key.models.filter((m) => m !== "*").join(", ") ||
																						i18n.t("workspace.virtualKeys.noModelsDenyAll"),
																			provider: key.provider,
																		})),
																	];
																	const selectedProviderKeys = hasWildcard
																		? [allKeyOptions[0]]
																		: providerKeys
																				.filter((key) => configKeyIds.includes(key.key_id))
																				.map((key) => ({
																					label: key.name,
																					value: key.key_id,
																					description:
																						key.models == null || key.models.includes("*")
																							? i18n.t("workspace.virtualKeys.allModels")
																							: key.models.filter((m) => m !== "*").join(", ") ||
																								i18n.t("workspace.virtualKeys.noModelsDenyAll"),
																					provider: key.provider,
																				}));

																	return (
																		<div className="mx-0.5 space-y-2">
																			<Label className="text-sm font-medium">{i18n.t("workspace.virtualKeys.allowedKeys")}</Label>
																			<p className="text-muted-foreground text-xs">
																				{i18n.t("workspace.virtualKeys.selectSpecificKeysOrAllowAll")}
																			</p>
																			<AsyncMultiSelect
																				hideSelectedOptions
																				isNonAsync
																				closeMenuOnSelect={false}
																				menuPlacement="auto"
																				defaultOptions={allKeyOptions}
																				views={{
																					multiValue: (multiValueProps: MultiValueProps<VirtualKeyType>) => {
																						return (
																							<div
																								{...multiValueProps.innerProps}
																								className="bg-accent dark:!bg-card flex cursor-pointer items-center gap-1 rounded-sm px-1 py-0.5 text-sm"
																							>
																								{multiValueProps.data.label}{" "}
																								<X
																									className="hover:text-foreground text-muted-foreground h-4 w-4 cursor-pointer"
																									onClick={(e) => {
																										e.stopPropagation();
																										multiValueProps.removeProps.onClick?.(e as any);
																									}}
																								/>
																							</div>
																						);
																					},
																					option: (optionProps: OptionProps<VirtualKeyType>) => {
																						const { Option } = components;
																						return (
																							<Option
																								{...optionProps}
																								className={cn(
																									"flex w-full cursor-pointer items-center gap-2 rounded-sm px-2 py-2 text-sm",
																									optionProps.isFocused && "bg-accent dark:!bg-card",
																									"hover:bg-accent",
																									optionProps.isSelected && "bg-accent dark:!bg-card",
																								)}
																							>
																								<span className="text-content-primary grow truncate text-sm">{optionProps.data.label}</span>
																								{optionProps.data.description && (
																									<span className="text-content-tertiary max-w-[70%] text-sm">
																										{optionProps.data.description}
																									</span>
																								)}
																							</Option>
																						);
																					},
																				}}
																				value={selectedProviderKeys}
																				onChange={(keys) => {
																					const hadStar = hasWildcard;
																					const hasStar = keys.some((k) => k.value === "*");
																					if (!hadStar && hasStar) {
																						// Just selected "Allow All Keys" — set to ["*"] only
																						handleUpdateProviderConfig(index, "key_ids", ["*"]);
																					} else if (hadStar && hasStar && keys.length > 1) {
																						// Had "*", still has "*", but user also selected a specific key — drop "*"
																						handleUpdateProviderConfig(
																							index,
																							"key_ids",
																							keys.filter((k) => k.value !== "*").map((k) => k.value as string),
																						);
																					} else {
																						handleUpdateProviderConfig(
																							index,
																							"key_ids",
																							keys.map((k) => k.value as string),
																						);
																					}
																				}}
																				placeholder={
																					hasWildcard
																						? i18n.t("workspace.virtualKeys.allKeysAllowed")
																						: configKeyIds.length === 0
																							? i18n.t("workspace.virtualKeys.noKeysSelected")
																							: i18n.t("workspace.virtualKeys.selectKeys")
																				}
																				className="hover:bg-accent w-full"
																				menuClassName="z-[60] max-h-[300px] overflow-y-auto w-full cursor-pointer custom-scrollbar"
																			/>
																		</div>
																	);
																})()}

																<DottedSeparator />

																{/* Provider Budget Configuration */}
																<MultiBudgetLines
																	id={`providerBudget-${index}`}
																	data-testid={`vk-provider-budget-${index}`}
																	label={i18n.t("workspace.virtualKeys.providerBudget")}
																	lines={
																		config.budgets && config.budgets.length > 0
																			? config.budgets.map((b) => ({
																					max_limit: b.max_limit,
																					reset_duration: b.reset_duration || "1M",
																				}))
																			: []
																	}
																	onChange={(lines) => {
																		const updatedConfigs = [...providerConfigs];
																		updatedConfigs[index] = {
																			...updatedConfigs[index],
																			budgets: lines.map((l) => ({
																				max_limit: l.max_limit,
																				reset_duration: l.reset_duration,
																			})),
																		};
																		form.setValue("providerConfigs", updatedConfigs, { shouldDirty: true });
																	}}
																/>

																<DottedSeparator />

																{/* Provider Rate Limit Configuration */}
																<div className="space-y-4">
																	<Label className="text-sm font-medium">{i18n.t("workspace.virtualKeys.providerRateLimits")}</Label>

																	<NumberAndSelect
																		id={`providerTokenLimit-${index}`}
																		labelClassName="font-normal"
																		label={i18n.t("workspace.virtualKeys.maximumTokens")}
																		value={config.rate_limit?.token_max_limit}
																		selectValue={config.rate_limit?.token_reset_duration || "1h"}
																		onChangeNumber={(value) => {
																			const currentRateLimit = config.rate_limit || {};
																			handleUpdateProviderConfig(index, "rate_limit", {
																				...currentRateLimit,
																				token_max_limit: value,
																			});
																		}}
																		onChangeSelect={(value) => {
																			const currentRateLimit = config.rate_limit || {};
																			handleUpdateProviderConfig(index, "rate_limit", {
																				...currentRateLimit,
																				token_reset_duration: value,
																			});
																		}}
																		options={resetDurationOptions}
																	/>

																	<NumberAndSelect
																		id={`providerRequestLimit-${index}`}
																		labelClassName="font-normal"
																		label={i18n.t("workspace.virtualKeys.maximumRequests")}
																		value={config.rate_limit?.request_max_limit}
																		selectValue={config.rate_limit?.request_reset_duration || "1h"}
																		onChangeNumber={(value) => {
																			const currentRateLimit = config.rate_limit || {};
																			handleUpdateProviderConfig(index, "rate_limit", {
																				...currentRateLimit,
																				request_max_limit: value,
																			});
																		}}
																		onChangeSelect={(value) => {
																			const currentRateLimit = config.rate_limit || {};
																			handleUpdateProviderConfig(index, "rate_limit", {
																				...currentRateLimit,
																				request_reset_duration: value,
																			});
																		}}
																		options={resetDurationOptions}
																	/>
																</div>
															</AccordionContent>
														</AccordionItem>
													);
												})}
											</Accordion>
										</div>
									)}
									{/* Display validation errors for provider configurations */}
									{form.formState.errors.providerConfigs && (
										<div className="text-destructive text-sm">{form.formState.errors.providerConfigs.message}</div>
									)}
								</div>
								{/* MCP Client Configurations */}
								{((mcpClientsData && mcpClientsData.length > 0) || (mcpConfigs && mcpConfigs.length > 0)) && (
									<div className="mt-6 space-y-2">
										<div className="flex items-center gap-2">
											<Label className="text-sm font-medium">{i18n.t("workspace.virtualKeys.mcpClientConfigurations")}</Label>
											<TooltipProvider>
												<Tooltip>
													<TooltipTrigger asChild>
														<span>
															<Info className="text-muted-foreground h-3 w-3" />
														</span>
													</TooltipTrigger>
													<TooltipContent>
														<p>
															{i18n.t("workspace.virtualKeys.mcpClientConfigurationsTooltip")}{" "}
															<span className="font-medium">{i18n.t("workspace.virtualKeys.allowAllTools")}</span>{" "}
															{i18n.t("workspace.virtualKeys.toGrantToolAccess")}
														</p>
													</TooltipContent>
												</Tooltip>
											</TooltipProvider>
										</div>

										{/* MCP servers available on all virtual keys by default, excluding explicitly overridden ones */}
										{(() => {
											const defaultMCPClients = mcpClientsData.filter(
												(client) =>
													client.config.allow_on_all_virtual_keys &&
													!mcpConfigs.some((config) => config.mcp_client_name === client.config.name),
											);
											return defaultMCPClients.length > 0 ? (
												<div className="text-muted-foreground rounded-md border p-3 text-xs">
													<div className="flex items-start gap-1.5">
														<Info className="mt-0.5 h-3 w-3 shrink-0" />
														<span>
															{i18n.t("workspace.virtualKeys.defaultMcpServersAvailable", {
																servers: defaultMCPClients.map((c) => c.config.name).join(", "),
															})}
														</span>
													</div>
												</div>
											) : null;
										})()}

										{/* Add MCP Client Dropdown */}
										{mcpClientsData && mcpClientsData.length > 0 && (
											<div className="flex gap-2">
												<Select
													value={selectedMCPClient}
													onValueChange={(mcpClientId) => {
														handleAddMCPClient(mcpClientId);
														setSelectedMCPClient(""); // Reset to placeholder state
													}}
												>
													<SelectTrigger className="flex-1">
														<SelectValue placeholder={i18n.t("workspace.virtualKeys.selectAnMcpClientToAdd")} />
													</SelectTrigger>
													<SelectContent>
														{mcpClientsData.filter((client) => !mcpConfigs.some((config) => config.mcp_client_name === client.config.name))
															.length > 0 ? (
															mcpClientsData
																.filter(
																	(client) =>
																		client.config.name && !mcpConfigs.some((config) => config.mcp_client_name === client.config.name),
																)
																.map((client, index) => {
																	const client_tools = client.tools || [];
																	const totalTools = client.config.tools_to_execute?.includes("*")
																		? client_tools.length
																		: client_tools.filter((tool) => client.config.tools_to_execute?.includes(tool.name)).length;
																	return (
																		<SelectItem key={index} value={client.config.name}>
																			<div className="flex items-center gap-2">
																				{client.config.name}
																				<span className="text-muted-foreground text-xs">
																					({totalTools}{" "}
																					{totalTools === 1
																						? i18n.t("workspace.virtualKeys.enabledTool")
																						: i18n.t("workspace.virtualKeys.enabledTools")}
																					)
																				</span>
																			</div>
																		</SelectItem>
																	);
																})
														) : (
															<div className="text-muted-foreground px-2 py-1.5 text-sm">
																{i18n.t("workspace.virtualKeys.allMcpClientsConfigured")}
															</div>
														)}
													</SelectContent>
												</Select>
											</div>
										)}

										{/* MCP Configurations Table */}
										{mcpConfigs.length > 0 && (
											<div className="rounded-md border">
												<Table>
													<TableHeader>
														<TableRow>
															<TableHead>{i18n.t("workspace.virtualKeys.mcpClient")}</TableHead>
															<TableHead>{i18n.t("workspace.virtualKeys.allowedTools")}</TableHead>
															<TableHead className="w-[50px]"></TableHead>
														</TableRow>
													</TableHeader>
													<TableBody>
														{mcpConfigs.map((config, index) => {
															const mcpClient = mcpClientsData?.find((client) => client.config.name === config.mcp_client_name);

															// Handle new wildcard semantics for client-level filtering
															const clientToolsToExecute = mcpClient?.config?.tools_to_execute;
															let availableTools: any[] = [];

															if (!clientToolsToExecute || clientToolsToExecute.length === 0) {
																// nil/undefined or empty array - no tools available from client config
																availableTools = [];
															} else if (clientToolsToExecute.includes("*")) {
																// Wildcard - all tools available
																availableTools = mcpClient?.tools || [];
															} else {
																// Specific tools listed
																availableTools = (mcpClient?.tools || []).filter((tool) => clientToolsToExecute.includes(tool.name)) || [];
															}

															const enabledToolsByConfig =
																(mcpClient?.tools || []).filter((tool) => config.tools_to_execute?.includes(tool.name)) || [];
															const selectedTools = config.tools_to_execute || [];

															return (
																<TableRow key={`${config.mcp_client_name}-${index}`}>
																	<TableCell className="w-[150px]">{config.mcp_client_name}</TableCell>
																	<TableCell>
																		<MultiSelect
																			options={[
																				{
																					label: i18n.t("workspace.virtualKeys.allowAllTools"),
																					value: "*",
																					description: i18n.t("workspace.virtualKeys.allowAllCurrentFutureTools"),
																				},
																				...[...availableTools, ...enabledToolsByConfig]
																					.filter((tool, index, arr) => arr.findIndex((t) => t.name === tool.name) === index)
																					.map((tool) => ({
																						label: tool.name,
																						value: tool.name,
																						description: tool.description,
																					})),
																			]}
																			defaultValue={selectedTools}
																			onValueChange={(tools: string[]) => {
																				const hadStar = selectedTools.includes("*");
																				const hasStar = tools.includes("*");
																				if (!hadStar && hasStar) {
																					// Just selected "Allow All Tools" — set to ["*"] only
																					handleUpdateMCPConfig(index, "tools_to_execute", ["*"]);
																				} else if (hadStar && hasStar && tools.length > 1) {
																					// Had "*", still has "*", but user also selected a specific tool — drop "*"
																					handleUpdateMCPConfig(
																						index,
																						"tools_to_execute",
																						tools.filter((t) => t !== "*"),
																					);
																				} else {
																					handleUpdateMCPConfig(index, "tools_to_execute", tools);
																				}
																			}}
																			placeholder={
																				selectedTools.length === 0
																					? i18n.t("workspace.virtualKeys.noToolsSelected")
																					: selectedTools.includes("*")
																						? i18n.t("workspace.virtualKeys.allToolsAllowed")
																						: i18n.t("workspace.virtualKeys.selectTools")
																			}
																			variant="inverted"
																			className="hover:bg-accent w-full bg-white dark:bg-zinc-800"
																			commandClassName="w-full max-w-96"
																			modalPopover={true}
																			animation={0}
																		/>
																	</TableCell>
																	<TableCell>
																		<Button
																			type="button"
																			variant="ghost"
																			size="sm"
																			onClick={() => handleRemoveMCPClient(index)}
																			data-testid={`vk-delete-mcp-${index}`}
																		>
																			<Trash2 className="h-4 w-4" />
																		</Button>
																	</TableCell>
																</TableRow>
															);
														})}
													</TableBody>
												</Table>
											</div>
										)}
									</div>
								)}
								<DottedSeparator className="mt-6 mb-5" />
								{/* Budget Configuration */}
								<div className="space-y-4">
									<MultiBudgetLines
										id="vkBudget"
										data-testid="vk-budget-lines"
										label={i18n.t("workspace.virtualKeys.budgetConfiguration")}
										lines={form.watch("budgets") ?? []}
										onChange={(lines) => {
											form.setValue("budgets", lines, { shouldDirty: true });
										}}
										onReset={clearVirtualKeyBudget}
										showReset={isEditing && !!(virtualKey?.budgets?.length || (watchedBudgets && watchedBudgets.length > 0))}
									/>

									{/* Calendar alignment toggle — shown when any budget supports alignment */}
									{hasAnyAlignableBudget && (
										<div className="flex items-center justify-between gap-4 rounded-md border px-3 py-2">
											<div className="space-y-0.5">
												<Label htmlFor="vk-budget-calendar-aligned-toggle" className="text-sm font-normal">
													{i18n.t("workspace.virtualKeys.alignToCalendarCycle")}
												</Label>
												<p id="vk-budget-calendar-aligned-description" className="text-muted-foreground text-xs">
													{i18n.t("workspace.virtualKeys.alignToCalendarCycleDescription")}
												</p>
											</div>
											<Switch
												id="vk-budget-calendar-aligned-toggle"
												aria-describedby="vk-budget-calendar-aligned-description"
												checked={watchedBudgetCalendarAligned}
												onCheckedChange={handleCalendarAlignedChange}
												data-testid="vk-budget-calendar-aligned-toggle"
											/>
										</div>
									)}

									{/* Warning dialog shown when enabling calendar alignment on an existing budget */}
									<AlertDialog open={showCalendarAlignWarning} onOpenChange={setShowCalendarAlignWarning}>
										<AlertDialogContent>
											<AlertDialogHeader>
												<AlertDialogTitle>{i18n.t("workspace.virtualKeys.resetBudgetUsageTitle")}</AlertDialogTitle>
												<AlertDialogDescription>
													{i18n.t("workspace.virtualKeys.resetBudgetUsageDescription", { amount: "$0.00" })}
												</AlertDialogDescription>
											</AlertDialogHeader>
											<AlertDialogFooter>
												<AlertDialogCancel data-testid="vk-calendar-align-cancel-btn">
													{i18n.t("workspace.virtualKeys.cancel")}
												</AlertDialogCancel>
												<AlertDialogAction
													data-testid="vk-calendar-align-enable-btn"
													onClick={() => {
														form.setValue("budgetCalendarAligned", true, { shouldDirty: true });
														setShowCalendarAlignWarning(false);
													}}
												>
													{i18n.t("workspace.virtualKeys.enableCalendarAlignment")}
												</AlertDialogAction>
											</AlertDialogFooter>
										</AlertDialogContent>
									</AlertDialog>
								</div>
								{/* Rate Limiting Configuration */}
								<div className="space-y-4">
									<div className="flex items-center justify-between gap-2">
										<Label className="text-sm font-medium">{i18n.t("workspace.virtualKeys.rateLimitingConfiguration")}</Label>
										{isEditing && (virtualKey?.rate_limit || watchedTokenMaxLimit || watchedRequestMaxLimit) && (
											<Button
												type="button"
												variant="ghost"
												size="sm"
												onClick={clearVirtualKeyRateLimits}
												data-testid="vk-rate-limit-reset-button"
											>
												<RotateCcw className="h-4 w-4" />
												{i18n.t("workspace.virtualKeys.reset")}
											</Button>
										)}
									</div>

									<FormField
										control={form.control}
										name="tokenMaxLimit"
										render={({ field }) => (
											<FormItem>
												<NumberAndSelect
													id="tokenMaxLimit"
													labelClassName="font-normal"
													label={i18n.t("workspace.virtualKeys.maximumTokens")}
													value={field.value}
													selectValue={form.watch("tokenResetDuration") || "1h"}
													onChangeNumber={(value) => {
														field.onChange(value);
													}}
													onChangeSelect={(value) => form.setValue("tokenResetDuration", value, { shouldDirty: true })}
													options={resetDurationOptions}
												/>
												<FormMessage />
											</FormItem>
										)}
									/>

									<FormField
										control={form.control}
										name="requestMaxLimit"
										render={({ field }) => (
											<FormItem>
												<NumberAndSelect
													id="requestMaxLimit"
													labelClassName="font-normal"
													label={i18n.t("workspace.virtualKeys.maximumRequests")}
													value={field.value}
													selectValue={form.watch("requestResetDuration") || "1h"}
													onChangeNumber={(value) => {
														field.onChange(value);
													}}
													onChangeSelect={(value) => form.setValue("requestResetDuration", value, { shouldDirty: true })}
													options={resetDurationOptions}
												/>
												<FormMessage />
											</FormItem>
										)}
									/>
								</div>
								{(teams?.length > 0 || customers?.length > 0) && (
									<>
										<DottedSeparator className="my-6" />

										{/* Entity Assignment */}
										<div className="space-y-4">
											<Label className="text-sm font-medium">{i18n.t("workspace.virtualKeys.entityAssignment")}</Label>

											<div className="grid grid-cols-1 items-center gap-2 md:grid-cols-2">
												<FormField
													control={form.control}
													name="entityType"
													render={({ field }) => (
														<FormItem>
															<FormLabel className="font-normal">{i18n.t("workspace.virtualKeys.assignmentType")}</FormLabel>
															<ComboboxSelect
																options={[
																	{ value: "none", label: i18n.t("workspace.virtualKeys.noAssignment") },
																	...(teams?.length > 0 ? [{ value: "team", label: i18n.t("workspace.virtualKeys.assignToTeam") }] : []),
																	...(customers?.length > 0
																		? [{ value: "customer", label: i18n.t("workspace.virtualKeys.assignToCustomer") }]
																		: []),
																]}
																value={field.value}
																onValueChange={async (value) => {
																	const val = value ?? "none";
																	field.onChange(val);
																	if (val === "team" && teams?.length > 0) {
																		form.setValue("teamId", teams[0].id, { shouldDirty: true, shouldValidate: true });
																		form.setValue("customerId", "", { shouldDirty: true, shouldValidate: true });
																		await form.trigger(["teamId", "customerId", "entityType"]);
																	} else if (val === "customer" && customers?.length > 0) {
																		form.setValue("customerId", customers[0].id, { shouldDirty: true, shouldValidate: true });
																		form.setValue("teamId", "", { shouldDirty: true, shouldValidate: true });
																		await form.trigger(["teamId", "customerId", "entityType"]);
																	} else {
																		form.setValue("teamId", "", { shouldDirty: true, shouldValidate: true });
																		form.setValue("customerId", "", { shouldDirty: true, shouldValidate: true });
																		await form.trigger(["teamId", "customerId", "entityType"]);
																	}
																}}
																disabled={isTeamLocked}
																disableSearch
																hideClear
																className="h-9"
															/>
															<FormMessage />
														</FormItem>
													)}
												/>
												{form.watch("entityType") === "team" && teams?.length > 0 && (
													<FormField
														control={form.control}
														name="teamId"
														render={({ field }) => (
															<FormItem>
																<FormLabel className="font-normal">{i18n.t("workspace.virtualKeys.selectTeam")}</FormLabel>
																<ComboboxSelect
																	options={teams.map((team) => ({
																		value: team.id,
																		label: team.customer ? `${team.name} — ${team.customer.name}` : team.name,
																	}))}
																	value={field.value || null}
																	onValueChange={(val) => field.onChange(val ?? "")}
																	placeholder={i18n.t("workspace.virtualKeys.selectTeamPlaceholder")}
																	disabled={isTeamLocked}
																	emptyMessage={i18n.t("workspace.virtualKeys.noTeamsFound")}
																	className="h-9"
																/>
																<FormMessage />
															</FormItem>
														)}
													/>
												)}

												{form.watch("entityType") === "customer" && customers?.length > 0 && (
													<FormField
														control={form.control}
														name="customerId"
														render={({ field }) => (
															<FormItem>
																<FormLabel className="font-normal">{i18n.t("workspace.virtualKeys.selectCustomer")}</FormLabel>
																<ComboboxSelect
																	options={customers.map((customer) => ({
																		value: customer.id,
																		label: customer.name,
																	}))}
																	value={field.value || null}
																	onValueChange={(val) => field.onChange(val ?? "")}
																	placeholder={i18n.t("workspace.virtualKeys.selectCustomerPlaceholder")}
																	emptyMessage={i18n.t("workspace.virtualKeys.noCustomersFound")}
																	className="h-9"
																/>
																<FormMessage />
															</FormItem>
														)}
													/>
												)}
											</div>
										</div>
									</>
								)}
							</fieldset>
						</div>
						{isEditing && virtualKey?.config_hash && (
							<div className="px-8">
								<ConfigSyncAlert className="mt-2" />
							</div>
						)}
						{/* Form Footer */}
						<div className="border-border bg-card sticky bottom-0 z-10 border-t px-8 py-4">
							<div className="flex justify-end gap-2">
								<Button type="button" variant="outline" onClick={handleClose} data-testid="vk-cancel-btn">
									{i18n.t("workspace.virtualKeys.cancel")}
								</Button>
								<TooltipProvider>
									<Tooltip>
										<TooltipTrigger asChild>
											<span className="inline-block">
												<Button type="submit" disabled={isLoading || !form.formState.isDirty || !canSubmit} data-testid="vk-save-btn">
													{isLoading
														? i18n.t("workspace.virtualKeys.saving")
														: isEditing
															? i18n.t("workspace.virtualKeys.update")
															: i18n.t("workspace.virtualKeys.create")}
												</Button>
											</span>
										</TooltipTrigger>
										{(isLoading || !form.formState.isDirty || !canSubmit) && (
											<TooltipContent>
												<p>
													{!canSubmit
														? i18n.t("workspace.virtualKeys.permissionDenied")
														: isLoading
															? i18n.t("workspace.virtualKeys.saving")
															: !form.formState.isDirty
																? i18n.t("workspace.virtualKeys.noChangesMade")
																: ""}
												</p>
											</TooltipContent>
										)}
									</Tooltip>
								</TooltipProvider>
							</div>
						</div>
					</form>
				</Form>
			</SheetContent>
		</Sheet>
	);
}