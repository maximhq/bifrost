"use client";

import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from "@/components/ui/accordion";
import { AsyncMultiSelect } from "@/components/ui/asyncMultiselect";
import { Button } from "@/components/ui/button";
import { ConfigSyncAlert } from "@/components/ui/configSyncAlert";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import { MultiSelect } from "@/components/ui/multiSelect";
import NumberAndSelect from "@/components/ui/numberAndSelect";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import Toggle from "@/components/ui/toggle";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/components/ui/utils";
import { ModelPlaceholders } from "@/lib/constants/config";
import { resetDurationOptions } from "@/lib/constants/governance";
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
import { Building, Info, RotateCcw, Trash2, Users, X } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { components, MultiValueProps, OptionProps } from "react-select";
import { toast } from "sonner";
import { z } from "zod";

interface VirtualKeySheetProps {
	virtualKey?: VirtualKey | null;
	teams: Team[];
	customers: Customer[];
	onSave: () => void;
	onCancel: () => void;
}

// Provider configuration schema
const providerConfigSchema = z.object({
	id: z.number().optional(),
	provider: z.string().min(1, "Provider is required"),
	weight: z
		.union([
			z.literal("").transform(() => undefined as undefined),
			z
				.string()
				.transform((v) => {
					const n = Number.parseFloat(v);
					return Number.isNaN(n) ? undefined : n;
				})
				.pipe(z.number().min(0, "Weight must be at least 0").max(1, "Weight must be at most 1").optional()),
			z.number().min(0, "Weight must be at least 0").max(1, "Weight must be at most 1"),
		])
		.optional(),
	allowed_models: z.array(z.string()).optional(),
	key_ids: z.array(z.string()).optional(), // Keys associated with this provider config
	// Provider-level budget
	budget: z
		.object({
			max_limit: z.string().optional(),
			reset_duration: z.string().optional(),
		})
		.optional(),
	// Provider-level rate limits
	rate_limit: z
		.object({
			token_max_limit: z.string().optional(),
			token_reset_duration: z.string().optional(),
			request_max_limit: z.string().optional(),
			request_reset_duration: z.string().optional(),
		})
		.optional(),
});

const mcpConfigSchema = z.object({
	id: z.number().optional(),
	mcp_client_name: z.string().min(1, "MCP client name is required"),
	tools_to_execute: z.array(z.string()).optional(),
});

// Main form schema
const formSchema = z
	.object({
		name: z.string().min(1, "Virtual key name is required"),
		description: z.string().optional(),
		providerConfigs: z.array(providerConfigSchema).optional(),
		mcpConfigs: z.array(mcpConfigSchema).optional(),
		entityType: z.enum(["team", "customer", "none"]),
		teamId: z.string().optional(),
		customerId: z.string().optional(),
		isActive: z.boolean(),
		// Budget
		budgetMaxLimit: z.string().optional(),
		budgetResetDuration: z.string().optional(),
		// Token limits
		tokenMaxLimit: z.string().optional(),
		tokenResetDuration: z.string().optional(),
		// Request limits
		requestMaxLimit: z.string().optional(),
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
			message: "Please select a valid team or customer when assignment type is chosen",
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

export default function VirtualKeySheet({ virtualKey, teams, customers, onSave, onCancel }: VirtualKeySheetProps) {
	const [isOpen, setIsOpen] = useState(true);
	const isEditing = !!virtualKey;

	const hasCreateAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.Create);
	const hasUpdateAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.Update);
	const canSubmit = isEditing ? hasUpdateAccess : hasCreateAccess;

	const handleClose = () => {
		setIsOpen(false);
		setTimeout(() => {
			onCancel();
		}, 150); // Slightly longer than the 100ms animation duration
	};

	// RTK Query hooks
	const { data: providersData, error: providersError, isLoading: providersLoading } = useGetProvidersQuery();
	const { data: keysData, error: keysError, isLoading: keysLoading } = useGetAllKeysQuery();
	const [createVirtualKey, { isLoading: isCreating }] = useCreateVirtualKeyMutation();
	const [updateVirtualKey, { isLoading: isUpdating }] = useUpdateVirtualKeyMutation();
	const { data: mcpClientsResponse, error: mcpClientsError, isLoading: mcpClientsLoading } = useGetMCPClientsQuery();
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
					...config,
					weight: config.weight ?? "",
					key_ids: config.allow_all_keys ? ["*"] : (config.keys?.map((key) => key.key_id) || []),
					budget: config.budget
						? {
							max_limit: String(config.budget.max_limit),
							reset_duration: config.budget.reset_duration,
						}
						: undefined,
					rate_limit: config.rate_limit
						? {
							token_max_limit: config.rate_limit.token_max_limit ? String(config.rate_limit.token_max_limit) : undefined,
							token_reset_duration: config.rate_limit.token_reset_duration,
							request_max_limit: config.rate_limit.request_max_limit ? String(config.rate_limit.request_max_limit) : undefined,
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
			entityType: virtualKey?.team_id ? "team" : virtualKey?.customer_id ? "customer" : "none",
			teamId: virtualKey?.team_id || "",
			customerId: virtualKey?.customer_id || "",
			isActive: virtualKey?.is_active ?? true,
			budgetMaxLimit: virtualKey?.budget ? String(virtualKey.budget.max_limit) : "",
			budgetResetDuration: virtualKey?.budget?.reset_duration || "1M",
			tokenMaxLimit: virtualKey?.rate_limit?.token_max_limit ? String(virtualKey.rate_limit.token_max_limit) : "",
			tokenResetDuration: virtualKey?.rate_limit?.token_reset_duration || "1h",
			requestMaxLimit: virtualKey?.rate_limit?.request_max_limit ? String(virtualKey.rate_limit.request_max_limit) : "",
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
	const watchedBudgetMaxLimit = form.watch("budgetMaxLimit");
	const watchedTokenMaxLimit = form.watch("tokenMaxLimit");
	const watchedRequestMaxLimit = form.watch("requestMaxLimit");

	// Handle adding a new provider configuration
	const handleAddProvider = (provider: string) => {
		const existingConfig = providerConfigs.find((config) => config.provider === provider);
		if (existingConfig) {
			toast.error("This provider is already configured");
			return;
		}

		const newConfig = {
			provider: provider,
			weight: "" as string | number, // Default empty string = excluded from weighted routing until user sets a weight
			allowed_models: ["*"],
			key_ids: [],
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
			toast.error("This MCP client is already configured");
			return;
		}

		const newConfig = {
			mcp_client_name: mcpClientName,
			tools_to_execute: [], // Empty means no tools allowed
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

	const clearVirtualKeyBudget = () => {
		form.setValue("budgetMaxLimit", "", { shouldDirty: true });
		form.setValue("budgetResetDuration", "1M", { shouldDirty: true });
	};

	const clearVirtualKeyRateLimits = () => {
		form.setValue("tokenMaxLimit", "", { shouldDirty: true });
		form.setValue("tokenResetDuration", "1h", { shouldDirty: true });
		form.setValue("requestMaxLimit", "", { shouldDirty: true });
		form.setValue("requestResetDuration", "1h", { shouldDirty: true });
	};

	const normalizeIntegerField = (value: string | undefined): number | undefined => {
		if (value === undefined || value === "") return undefined;
		const num = parseInt(value, 10);
		return isNaN(num) ? undefined : num;
	};

	// Helper function to convert string weights to numbers and normalize budget/rate limit fields
	const normalizeProviderConfigs = (
		configs: NonNullable<FormData["providerConfigs"]>,
		existingConfigs?: VirtualKey["provider_configs"],
	): any[] => {
		return configs.map((config) => ({
			...config,
			weight: config.weight === undefined || config.weight === null
				? null
				: typeof config.weight === "string" ? (Number.isNaN(parseFloat(config.weight)) ? null : parseFloat(config.weight)) : config.weight, budget: (() => {
					const budgetMaxLimit = normalizeNumericField(config.budget?.max_limit);
					if (budgetMaxLimit !== undefined) {
						return {
							max_limit: budgetMaxLimit,
							reset_duration: config.budget?.reset_duration || "1M",
						};
					}

					const existingConfig = existingConfigs?.find((item) => (config.id ? item.id === config.id : item.provider === config.provider));
					if (existingConfig?.budget) {
						return {};
					}

					return undefined;
				})(),
			rate_limit: (() => {
				const tokenMaxLimit = normalizeIntegerField(config.rate_limit?.token_max_limit);
				const requestMaxLimit = normalizeIntegerField(config.rate_limit?.request_max_limit);
				const hasTokenMaxLimit = tokenMaxLimit !== undefined;
				const hasRequestMaxLimit = requestMaxLimit !== undefined;
				if (hasTokenMaxLimit || hasRequestMaxLimit) {
					return {
						token_max_limit: tokenMaxLimit ?? null,
						token_reset_duration: hasTokenMaxLimit ? config.rate_limit?.token_reset_duration || "1h" : null,
						request_max_limit: requestMaxLimit ?? null,
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

	// Normalize numeric fields to ensure they are numbers or undefined
	const normalizeNumericField = (value: string | undefined): number | undefined => {
		if (value === undefined || value === "") return undefined;
		const num = parseFloat(value);
		return isNaN(num) ? undefined : num;
	};

	// Handle form submission
	const onSubmit = async (data: FormData) => {
		if (!canSubmit) {
			toast.error("You don't have permission to perform this action");
			return;
		}
		try {
			// Normalize provider configs to ensure weights are numbers and handle budget/rate limits
			const normalizedProviderConfigs = data.providerConfigs
				? normalizeProviderConfigs(data.providerConfigs, virtualKey?.provider_configs)
				: [];
			if (isEditing && virtualKey) {
				// Update existing virtual key
				const updateData: UpdateVirtualKeyRequest = {
					name: data.name || undefined,
					description: data.description || undefined,
					provider_configs: normalizedProviderConfigs,
					mcp_configs: data.mcpConfigs,
					team_id: data.entityType === "team" && data.teamId && data.teamId.trim() !== "" ? data.teamId : undefined,
					customer_id: data.entityType === "customer" && data.customerId && data.customerId.trim() !== "" ? data.customerId : undefined,
					is_active: data.isActive,
				};

				// Add budget if enabled
				const budgetMaxLimit = normalizeNumericField(data.budgetMaxLimit);
				const hadBudget = !!virtualKey.budget;
				const hasBudget = budgetMaxLimit !== undefined;
				if (hasBudget) {
					updateData.budget = {
						max_limit: budgetMaxLimit,
						reset_duration: data.budgetResetDuration || "1M",
					};
				} else if (hadBudget) {
					updateData.budget = {};
				}

				// Add rate limit if enabled
				const tokenMaxLimit = normalizeIntegerField(data.tokenMaxLimit);
				const requestMaxLimit = normalizeIntegerField(data.requestMaxLimit);
				const hadRateLimit = !!virtualKey.rate_limit;
				const hasTokenMaxLimit = tokenMaxLimit !== undefined;
				const hasRequestMaxLimit = requestMaxLimit !== undefined;
				const hasRateLimit = hasTokenMaxLimit || hasRequestMaxLimit;
				if (hasRateLimit) {
					updateData.rate_limit = {
						token_max_limit: tokenMaxLimit ?? null,
						token_reset_duration: hasTokenMaxLimit ? data.tokenResetDuration || "1h" : null,
						request_max_limit: requestMaxLimit ?? null,
						request_reset_duration: hasRequestMaxLimit ? data.requestResetDuration || "1h" : null,
					};
				} else if (hadRateLimit) {
					updateData.rate_limit = {};
				}

				await updateVirtualKey({ vkId: virtualKey.id, data: updateData }).unwrap();
				toast.success("Virtual key updated successfully");
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

				// Add budget if enabled
				const budgetMaxLimit = normalizeNumericField(data.budgetMaxLimit);
				if (budgetMaxLimit !== undefined) {
					createData.budget = {
						max_limit: budgetMaxLimit,
						reset_duration: data.budgetResetDuration || "1M",
					};
				}

				// Add rate limit if enabled
				const tokenMaxLimit = normalizeIntegerField(data.tokenMaxLimit);
				const requestMaxLimit = normalizeIntegerField(data.requestMaxLimit);
				const hasTokenMaxLimit = tokenMaxLimit !== undefined;
				const hasRequestMaxLimit = requestMaxLimit !== undefined;
				if (hasTokenMaxLimit || hasRequestMaxLimit) {
					createData.rate_limit = {
						token_max_limit: tokenMaxLimit,
						token_reset_duration: hasTokenMaxLimit ? data.tokenResetDuration || "1h" : undefined,
						request_max_limit: requestMaxLimit,
						request_reset_duration: hasRequestMaxLimit ? data.requestResetDuration || "1h" : undefined,
					};
				}

				await createVirtualKey(createData).unwrap();
				toast.success("Virtual key created successfully");
			}

			onSave();
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	return (
		<Sheet open={isOpen} onOpenChange={(open) => !open && handleClose()}>
			<SheetContent className="flex w-full flex-col overflow-x-hidden px-4 pb-8" data-testid="vk-sheet">
				<SheetHeader className="flex flex-col items-start px-3 pt-8">
					<SheetTitle className="flex items-center gap-2">{isEditing ? virtualKey?.name : "Create Virtual Key"}</SheetTitle>
					<SheetDescription>
						{isEditing
							? "Update the virtual key configuration and permissions."
							: "Create a new virtual key with specific permissions, budgets, and rate limits."}
					</SheetDescription>
				</SheetHeader>

				<Form {...form}>
					<form onSubmit={form.handleSubmit(onSubmit)} className="flex h-full flex-col gap-6 px-4">
						<div className="space-y-4">
							{/* Basic Information */}
							<div className="space-y-4">
								<FormField
									control={form.control}
									name="name"
									render={({ field }) => (
										<FormItem>
											<FormLabel>Name *</FormLabel>
											<FormControl>
												<Input placeholder="e.g., Production API Key" data-testid="vk-name-input" {...field} />
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
											<FormLabel>Description</FormLabel>
											<FormControl>
												<Textarea placeholder="This key is used for..." data-testid="vk-description-input" {...field} rows={3} />
											</FormControl>
											<FormMessage />
										</FormItem>
									)}
								/>

								<FormField
									control={form.control}
									name="isActive"
									render={({ field }) => (
										<FormItem>
											<Toggle label="Is this key active?" val={field.value} setVal={field.onChange} data-testid="vk-is-active-toggle" />
										</FormItem>
									)}
								/>
							</div>

							{/* Provider Configurations */}
							<div className="space-y-2">
								<div className="flex items-center gap-2">
									<Label className="text-sm font-medium">Provider Configurations</Label>
									<TooltipProvider>
										<Tooltip>
											<TooltipTrigger asChild>
												<span>
													<Info className="text-muted-foreground h-3 w-3" />
												</span>
											</TooltipTrigger>
											<TooltipContent>
												<p>
													Configure which providers this virtual key can use and their specific settings. Leave empty to block all
													providers. Add providers to allow them.
												</p>
											</TooltipContent>
										</Tooltip>
									</TooltipProvider>
								</div>

								{/* Add Provider Dropdown */}
								<div className="flex gap-2">
									<Select
										value={selectedProvider}
										onValueChange={(provider) => {
											handleAddProvider(provider);
											setSelectedProvider(""); // Reset to placeholder state
										}}
									>
										<SelectTrigger className="flex-1" data-testid="vk-provider-select">
											<SelectValue placeholder="Select a provider to add" />
										</SelectTrigger>
										<SelectContent>
											{(() => {
												// Filter out already configured providers
												const unconfiguredProviders = availableProviders.filter(
													(provider) => !providerConfigs.some((config) => config.provider === provider.name),
												);

												if (unconfiguredProviders.length === 0) {
													return <div className="text-muted-foreground px-2 py-1.5 text-sm">No providers left to configure</div>;
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
																<div className="hover:bg-accent/50 cursor-pointer rounded-sm p-2">
																	<Trash2 onClick={() => handleRemoveProvider(index)} className="h-4 w-4 opacity-75" data-testid={`vk-delete-provider-${index}`} />
																</div>
															</div>
														</AccordionTrigger>
														<AccordionContent className="flex flex-col gap-4 px-1 text-balance">
															<div className="flex w-full items-start gap-2">
																<div className="w-1/4 space-y-2">
																	<Label className="text-sm font-medium">Weight</Label>
																	<Input
																		placeholder="Exclude from routing"
																		className="h-10 w-full"
																		data-testid={`vk-weight-input-${index}`}
																		value={config.weight ?? ""}
																		onChange={(e) => {
																			const inputValue = e.target.value;
																			// Allow empty string, numbers, and partial decimal inputs like "0."
																			if (inputValue === "" || !isNaN(parseFloat(inputValue)) || inputValue.endsWith(".")) {
																				handleUpdateProviderConfig(index, "weight", inputValue);
																			}
																		}}
																		onBlur={(e) => {
																			const inputValue = e.target.value.trim();
																			if (inputValue === "") {
																				handleUpdateProviderConfig(index, "weight", "");
																			} else {
																				const num = parseFloat(inputValue);
																				if (!isNaN(num)) {
																					handleUpdateProviderConfig(index, "weight", String(num));
																				} else {
																					handleUpdateProviderConfig(index, "weight", "");
																				}
																			}
																		}}
																		type="text"
																	/>
																</div>
																<div className="w-3/4 space-y-2">
																	<Label className="text-sm font-medium">
																		Allowed Models <span className="text-muted-foreground ml-auto text-xs italic">type to search</span>
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
																				value={hasWildcardModels ? ["*"] : (config.allowed_models || [])}
																				onChange={(models: string[]) => {
																					const hadStar = (config.allowed_models || []).includes("*");
																					const hasStar = models.includes("*");
																					if (!hadStar && hasStar) {
																						handleUpdateProviderConfig(index, "allowed_models", ["*"]);
																					} else if (hadStar && hasStar && models.length > 1) {
																						handleUpdateProviderConfig(index, "allowed_models", models.filter((m) => m !== "*"));
																					} else {
																						handleUpdateProviderConfig(index, "allowed_models", models);
																					}
																				}}
																				placeholder={
																					hasWildcardModels
																						? "All models allowed"
																						: (config.allowed_models || []).length === 0
																							? "No models (deny all)"
																							: config.provider
																								? ModelPlaceholders[config.provider as keyof typeof ModelPlaceholders] || ModelPlaceholders.default
																								: ModelPlaceholders.default
																				}
																				className="min-h-10 max-w-[500px] min-w-[200px]"
																			/>
																		);
																	})()}
																	<p className="text-muted-foreground text-xs">Select specific models or choose “Allow All Models” to allow all. Leave empty to deny all.</p>
																</div>
															</div>

															{/* Allowed Keys for this provider */}
															{(() => {
																const providerKeys = availableKeys.filter((key) => key.provider === config.provider);
																const configKeyIds = config.key_ids || [];
																const hasWildcard = configKeyIds.includes("*");
																const allKeyOptions = [
																	{
																		label: "Allow All Keys",
																		value: "*",
																		description: "Allow all current and future keys for this provider",
																		provider: "",
																	},
																	...providerKeys.map((key) => ({
																		label: key.name,
																		value: key.key_id,
																		description:
																			key.models == null || key.models.includes("*")
																				? "All models"
																				: key.models.filter((m) => m !== "*").join(", ") || "No models (deny all)",
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
																					? "All models"
																					: key.models.filter((m) => m !== "*").join(", ") || "No models (deny all)",
																			provider: key.provider,
																		}));

																return (
																	<div className="mx-0.5 space-y-2">
																		<Label className="text-sm font-medium">Allowed Keys</Label>
																		<p className="text-muted-foreground text-xs">Select specific keys or allow all. Leave empty to block all keys for this provider.</p>
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
																					handleUpdateProviderConfig(index, "key_ids", keys.filter((k) => k.value !== "*").map((k) => k.value as string));
																				} else {
																					handleUpdateProviderConfig(index, "key_ids", keys.map((k) => k.value as string));
																				}
																			}}
																			placeholder={
																				hasWildcard
																					? "All keys allowed"
																					: configKeyIds.length === 0
																						? "No keys selected"
																						: "Select keys..."
																			}
																			className="hover:bg-accent w-full"
																			menuClassName="z-[60] max-h-[300px] overflow-y-auto w-full cursor-pointer custom-scrollbar"
																		/>
																	</div>
																);
															})()}

															<DottedSeparator />

															{/* Provider Budget Configuration */}
															<div className="space-y-4">
																<Label className="text-sm font-medium">Provider Budget</Label>
																<NumberAndSelect
																	id={`providerBudget-${index}`}
																	labelClassName="font-normal"
																	label="Maximum Spend (USD)"
																	value={config.budget?.max_limit || ""}
																	selectValue={config.budget?.reset_duration || "1M"}
																	onChangeNumber={(value) => {
																		const currentBudget = config.budget || {};
																		handleUpdateProviderConfig(index, "budget", {
																			...currentBudget,
																			max_limit: value,
																		});
																	}}
																	onChangeSelect={(value) => {
																		const currentBudget = config.budget || {};
																		handleUpdateProviderConfig(index, "budget", {
																			...currentBudget,
																			reset_duration: value,
																		});
																	}}
																	options={resetDurationOptions}
																/>
															</div>

															<DottedSeparator />

															{/* Provider Rate Limit Configuration */}
															<div className="space-y-4">
																<Label className="text-sm font-medium">Provider Rate Limits</Label>

																<NumberAndSelect
																	id={`providerTokenLimit-${index}`}
																	labelClassName="font-normal"
																	label="Maximum Tokens"
																	value={config.rate_limit?.token_max_limit || ""}
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
																	label="Maximum Requests"
																	value={config.rate_limit?.request_max_limit || ""}
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
										<Label className="text-sm font-medium">MCP Client Configurations</Label>
										<TooltipProvider>
											<Tooltip>
												<TooltipTrigger asChild>
													<span>
														<Info className="text-muted-foreground h-3 w-3" />
													</span>
												</TooltipTrigger>
												<TooltipContent>
													<p>
														Configure which MCP clients this virtual key can use and their allowed tools. Leaving this section empty
														blocks all MCP tools. After adding an MCP client, you must select specific tools or choose{" "}
														<span className="font-medium">Allow All Tools</span> to grant tool access.
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
														The following MCP servers are available to this key by default with all tools enabled on that client:{" "}
														<span className="text-foreground font-medium">
															{defaultMCPClients.map((c) => c.config.name).join(", ")}
														</span>
														. Adding an explicit config for any of them below will override the all-tools default for this key.
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
													<SelectValue placeholder="Select an MCP client to add" />
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
																				({totalTools} {totalTools === 1 ? "enabled tool" : "enabled tools"})
																			</span>
																		</div>
																	</SelectItem>
																);
															})
													) : (
														<div className="text-muted-foreground px-2 py-1.5 text-sm">All MCP clients configured</div>
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
														<TableHead>MCP Client</TableHead>
														<TableHead>Allowed Tools</TableHead>
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
																				label: "Allow All Tools",
																				value: "*",
																				description: "Allow all current and future tools",
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
																				handleUpdateMCPConfig(index, "tools_to_execute", tools.filter((t) => t !== "*"));
																			} else {
																				handleUpdateMCPConfig(index, "tools_to_execute", tools);
																			}
																		}}
																		placeholder={
																			selectedTools.length === 0
																				? "No tools selected"
																				: selectedTools.includes("*")
																					? "All tools allowed"
																					: "Select tools..."
																		}
																		variant="inverted"
																		className="hover:bg-accent w-full bg-white dark:bg-zinc-800"
																		commandClassName="w-full max-w-96"
																		modalPopover={true}
																		animation={0}
																	/>
																</TableCell>
																<TableCell>
																	<Button type="button" variant="ghost" size="sm" onClick={() => handleRemoveMCPClient(index)} data-testid={`vk-delete-mcp-${index}`}>
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
								<div className="flex items-center justify-between gap-2">
									<Label className="text-sm font-medium">Budget Configuration</Label>
									{isEditing && (virtualKey?.budget || watchedBudgetMaxLimit) && (
										<Button
											type="button"
											variant="ghost"
											size="sm"
											onClick={clearVirtualKeyBudget}
											data-testid="vk-budget-reset-button"
										>
											<RotateCcw className="h-4 w-4" />
											Reset
										</Button>
									)}
								</div>
								<FormField
									control={form.control}
									name="budgetMaxLimit"
									render={({ field }) => (
										<FormItem>
											<NumberAndSelect
												id="budgetMaxLimit"
												labelClassName="font-normal"
												label="Maximum Spend (USD)"
												value={field.value || ""}
												selectValue={form.watch("budgetResetDuration") || "1M"}
												onChangeNumber={(value) => {
													field.onChange(value);
												}}
												onChangeSelect={(value) => form.setValue("budgetResetDuration", value, { shouldDirty: true })}
												options={resetDurationOptions}
											/>
											<FormMessage />
										</FormItem>
									)}
								/>
							</div>

							{/* Rate Limiting Configuration */}
							<div className="space-y-4">
								<div className="flex items-center justify-between gap-2">
									<Label className="text-sm font-medium">Rate Limiting Configuration</Label>
									{isEditing && (virtualKey?.rate_limit || watchedTokenMaxLimit || watchedRequestMaxLimit) && (
										<Button
											type="button"
											variant="ghost"
											size="sm"
											onClick={clearVirtualKeyRateLimits}
											data-testid="vk-rate-limit-reset-button"
										>
											<RotateCcw className="h-4 w-4" />
											Reset
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
												label="Maximum Tokens"
												value={field.value || ""}
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
												label="Maximum Requests"
												value={field.value || ""}
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
										<Label className="text-sm font-medium">Entity Assignment</Label>

										<div className="grid grid-cols-1 items-center gap-2 md:grid-cols-2">
											<FormField
												control={form.control}
												name="entityType"
												render={({ field }) => (
													<FormItem>
														<FormLabel className="font-normal">Assignment Type</FormLabel>
														<Select
															onValueChange={async (value) => {
																field.onChange(value);
																// Auto-select first entry when switching to team or customer
																if (value === "team" && teams && teams.length > 0) {
																	form.setValue("teamId", teams[0].id, { shouldDirty: true, shouldValidate: true });
																	form.setValue("customerId", "", { shouldDirty: true, shouldValidate: true });
																	// Trigger validation after state updates
																	await form.trigger(["teamId", "customerId", "entityType"]);
																} else if (value === "customer" && customers && customers.length > 0) {
																	form.setValue("customerId", customers[0].id, { shouldDirty: true, shouldValidate: true });
																	form.setValue("teamId", "", { shouldDirty: true, shouldValidate: true });
																	// Trigger validation after state updates
																	await form.trigger(["teamId", "customerId", "entityType"]);
																} else if (value === "none") {
																	form.setValue("teamId", "", { shouldDirty: true, shouldValidate: true });
																	form.setValue("customerId", "", { shouldDirty: true, shouldValidate: true });
																	// Trigger validation after state updates
																	await form.trigger(["teamId", "customerId", "entityType"]);
																}
															}}
															defaultValue={field.value}
														>
															<FormControl className="w-full">
																<SelectTrigger data-testid="vk-entity-type-select">
																	<SelectValue />
																</SelectTrigger>
															</FormControl>
															<SelectContent >
																<SelectItem value="none">No Assignment</SelectItem>
																{teams?.length > 0 && <SelectItem value="team">Assign to Team</SelectItem>}
																{customers?.length > 0 && <SelectItem value="customer">Assign to Customer</SelectItem>}
															</SelectContent>
														</Select>
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
															<FormLabel className="font-normal">Select Team</FormLabel>
															<Select onValueChange={field.onChange} defaultValue={field.value}>
																<FormControl className="w-full">
																	<SelectTrigger data-testid="vk-team-select">
																		<SelectValue placeholder="Select a team" />
																	</SelectTrigger>
																</FormControl>
																<SelectContent>
																	{teams.map((team) => (
																		<SelectItem key={team.id} value={team.id}>
																			<div className="flex items-center gap-2">
																				<Users className="h-4 w-4" />
																				{team.name}
																				{team.customer && (
																					<span className="text-muted-foreground flex items-center gap-1">
																						<Building className="h-2 w-2" />
																						{team.customer.name}
																					</span>
																				)}
																			</div>
																		</SelectItem>
																	))}
																</SelectContent>
															</Select>
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
															<FormLabel className="font-normal">Select Customer</FormLabel>
															<Select onValueChange={field.onChange} defaultValue={field.value}>
																<FormControl className="w-full">
																	<SelectTrigger data-testid="vk-customer-select">
																		<SelectValue placeholder="Select a customer" />
																	</SelectTrigger>
																</FormControl>
																<SelectContent>
																	{customers.map((customer) => (
																		<SelectItem key={customer.id} value={customer.id}>
																			<div className="flex items-center gap-2">
																				<Building className="h-4 w-4" />
																				{customer.name}
																			</div>
																		</SelectItem>
																	))}
																</SelectContent>
															</Select>
															<FormMessage />
														</FormItem>
													)}
												/>
											)}
										</div>
									</div>
								</>
							)}
						</div>
						{isEditing && virtualKey?.config_hash && <ConfigSyncAlert className="mt-2" />}
						{/* Form Footer */}
						<div className="dark:bg-card border-border bg-white py-6">
							<div className="flex justify-end gap-2">
								<Button type="button" variant="outline" onClick={handleClose} data-testid="vk-cancel-btn">
									Cancel
								</Button>
								<TooltipProvider>
									<Tooltip>
										<TooltipTrigger asChild>
											<span className="inline-block">
												<Button
													type="submit"
													disabled={isLoading || !form.formState.isDirty || !form.formState.isValid || !canSubmit}
													data-testid="vk-save-btn"
												>
													{isLoading ? "Saving..." : isEditing ? "Update" : "Create"}
												</Button>
											</span>
										</TooltipTrigger>
										{(isLoading || !form.formState.isDirty || !form.formState.isValid || !canSubmit) && (
											<TooltipContent>
												<p>
													{!canSubmit
														? "You don't have permission to perform this action"
														: isLoading
															? "Saving..."
															: !form.formState.isDirty && !form.formState.isValid
																? "No changes made and validation errors present"
																: !form.formState.isDirty
																	? "No changes made"
																	: "Please fix validation errors"}
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
