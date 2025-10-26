"use client";

import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { MultiSelect } from "@/components/ui/multiSelect";
import NumberAndSelect from "@/components/ui/numberAndSelect";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { DottedSeparator } from "@/components/ui/separator";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { TagInput } from "@/components/ui/tagInput";
import { Textarea } from "@/components/ui/textarea";
import Toggle from "@/components/ui/toggle";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { ModelPlaceholders } from "@/lib/constants/config";
import { resetDurationOptions } from "@/lib/constants/governance";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels, ProviderName, ProviderNames } from "@/lib/constants/logs";
import {
	getErrorMessage,
	useCreateVirtualKeyMutation,
	useGetProvidersQuery,
	useGetAllKeysQuery,
	useGetMCPClientsQuery,
	useUpdateVirtualKeyMutation,
} from "@/lib/store";
import {
	CreateVirtualKeyRequest,
	Customer,
	Team,
	UpdateVirtualKeyRequest,
	VirtualKey,
	VirtualKeyProviderConfig,
} from "@/lib/types/governance";
import { zodResolver } from "@hookform/resolvers/zod";
import { Building, Info, Trash2, Users } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";
import { KnownProvider } from "@/lib/types/config";

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
	weight: z.union([z.number().min(0, "Weight must be at least 0").max(1, "Weight must be at most 1"), z.string()]),
	allowed_models: z.array(z.string()).optional(),
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
		selectedDBKeys: z.array(z.string()).optional(),
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
			// Validate that sum of provider weights equals 1 (only when there are multiple providers)
			if (data.providerConfigs && data.providerConfigs.length > 1) {
				const totalWeight = data.providerConfigs.reduce((sum, config) => {
					const weight = typeof config.weight === "string" ? parseFloat(config.weight) : config.weight;
					return sum + (isNaN(weight) ? 0 : weight);
				}, 0);
				return Math.abs(totalWeight - 1) < 0.001; // Allow small floating point errors
			}
			return true;
		},
		{
			message: "Sum of all provider weights must equal 1 when multiple providers are configured",
			path: ["providerConfigs"],
		},
	);

type FormData = z.infer<typeof formSchema>;

export default function VirtualKeySheet({ virtualKey, teams, customers, onSave, onCancel }: VirtualKeySheetProps) {
	const isEditing = !!virtualKey;

	// RTK Query hooks
	const { data: providersData, error: providersError, isLoading: providersLoading } = useGetProvidersQuery();
	const { data: keysData, error: keysError, isLoading: keysLoading } = useGetAllKeysQuery();
	const [createVirtualKey, { isLoading: isCreating }] = useCreateVirtualKeyMutation();
	const [updateVirtualKey, { isLoading: isUpdating }] = useUpdateVirtualKeyMutation();
	const { data: mcpClientsData, error: mcpClientsError, isLoading: mcpClientsLoading } = useGetMCPClientsQuery();
	const isLoading = isCreating || isUpdating;

	const availableKeys = keysData || [];
	const availableProviders = providersData || [];

	// Form setup
	const form = useForm<FormData>({
		resolver: zodResolver(formSchema),
		defaultValues: {
			name: virtualKey?.name || "",
			description: virtualKey?.description || "",
			providerConfigs: virtualKey?.provider_configs || [],
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
			selectedDBKeys: virtualKey?.keys?.map((key) => key.key_id) || [],
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

	// Provider configuration state
	const [selectedProvider, setSelectedProvider] = useState<string>("");

	// MCP client configuration state
	const [selectedMCPClient, setSelectedMCPClient] = useState<string>("");

	// Get current provider configs from form
	const providerConfigs = form.watch("providerConfigs") || [];

	// Get current MCP configs from form
	const mcpConfigs = form.watch("mcpConfigs") || [];

	// Handle adding a new provider configuration
	const handleAddProvider = (provider: string) => {
		const existingConfig = providerConfigs.find((config) => config.provider === provider);
		if (existingConfig) {
			toast.error("This provider is already configured");
			return;
		}

		const newConfig: VirtualKeyProviderConfig = {
			provider: provider,
			weight: 0.5, // Default weight, user can adjust
			allowed_models: [],
		};

		form.setValue("providerConfigs", [...providerConfigs, newConfig], { shouldDirty: true });
	};

	// Handle removing a provider configuration
	const handleRemoveProvider = (index: number) => {
		const updatedConfigs = providerConfigs.filter((_, i) => i !== index);
		form.setValue("providerConfigs", updatedConfigs, { shouldDirty: true });
	};

	// Handle updating provider configuration
	const handleUpdateProviderConfig = (index: number, field: keyof VirtualKeyProviderConfig, value: any) => {
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

	// Helper function to convert string weights to numbers
	const normalizeProviderConfigs = (configs: (VirtualKeyProviderConfig & { weight: string | number })[]): VirtualKeyProviderConfig[] => {
		return configs.map((config) => ({
			...config,
			weight: typeof config.weight === "string" ? parseFloat(config.weight) || 0 : config.weight,
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
		try {
			// Normalize provider configs to ensure weights are numbers
			const normalizedProviderConfigs = data.providerConfigs
				? normalizeProviderConfigs(data.providerConfigs as (VirtualKeyProviderConfig & { weight: string | number })[])
				: [];

			if (isEditing && virtualKey) {
				// Update existing virtual key
				const updateData: UpdateVirtualKeyRequest = {
					description: data.description || undefined,
					provider_configs: normalizedProviderConfigs,
					mcp_configs: data.mcpConfigs,
					team_id: data.entityType === "team" ? data.teamId : undefined,
					customer_id: data.entityType === "customer" ? data.customerId : undefined,
					key_ids: data.selectedDBKeys,
					is_active: data.isActive,
				};

				// Add budget if enabled
				const budgetMaxLimit = normalizeNumericField(data.budgetMaxLimit);
				if (budgetMaxLimit) {
					updateData.budget = {
						max_limit: budgetMaxLimit,
						reset_duration: data.budgetResetDuration || "1M",
					};
				}

				// Add rate limit if enabled
				const tokenMaxLimit = normalizeNumericField(data.tokenMaxLimit);
				const requestMaxLimit = normalizeNumericField(data.requestMaxLimit);
				if (tokenMaxLimit || requestMaxLimit) {
					updateData.rate_limit = {
						token_max_limit: tokenMaxLimit,
						token_reset_duration: data.tokenResetDuration || "1h",
						request_max_limit: requestMaxLimit,
						request_reset_duration: data.requestResetDuration || "1h",
					};
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
					team_id: data.entityType === "team" ? data.teamId : undefined,
					customer_id: data.entityType === "customer" ? data.customerId : undefined,
					key_ids: data.selectedDBKeys,
					is_active: data.isActive,
				};

				// Add budget if enabled
				const budgetMaxLimit = normalizeNumericField(data.budgetMaxLimit);
				if (budgetMaxLimit) {
					createData.budget = {
						max_limit: budgetMaxLimit,
						reset_duration: data.budgetResetDuration || "1M",
					};
				}

				// Add rate limit if enabled
				const tokenMaxLimit = normalizeNumericField(data.tokenMaxLimit);
				const requestMaxLimit = normalizeNumericField(data.requestMaxLimit);
				if (tokenMaxLimit || requestMaxLimit) {
					createData.rate_limit = {
						token_max_limit: tokenMaxLimit,
						token_reset_duration: data.tokenResetDuration || "1h",
						request_max_limit: requestMaxLimit,
						request_reset_duration: data.requestResetDuration || "1h",
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
		<Sheet open onOpenChange={onCancel}>
			<SheetContent className="dark:bg-card flex w-full flex-col overflow-x-hidden bg-white p-8 sm:max-w-2xl">
				<SheetHeader className="p-0">
					<SheetTitle className="flex items-center gap-2">{isEditing ? virtualKey?.name : "Create Virtual Key"}</SheetTitle>
					<SheetDescription>
						{isEditing
							? "Update the virtual key configuration and permissions."
							: "Create a new virtual key with specific permissions, budgets, and rate limits."}
					</SheetDescription>
				</SheetHeader>

				<Form {...form}>
					<form onSubmit={form.handleSubmit(onSubmit)} className="flex h-full flex-col gap-6">
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
												<Input placeholder="e.g., Production API Key" {...field} disabled={isEditing} />
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
												<Textarea placeholder="This key is used for..." {...field} rows={3} />
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
											<Toggle label="Is this key active?" val={field.value} setVal={field.onChange} />
										</FormItem>
									)}
								/>
							</div>

							<DottedSeparator className="mt-6 mb-5" />

							{/* DBKey Selection */}
							<div className="space-y-2">
								<div className="flex items-center gap-2">
									<Label className="text-sm font-medium">Allowed Keys</Label>
									<TooltipProvider>
										<Tooltip>
											<TooltipTrigger asChild>
												<span>
													<Info className="text-muted-foreground h-3 w-3" />
												</span>
											</TooltipTrigger>
											<TooltipContent>
												<p>Select specific database keys to associate with this virtual key. Leave empty to allow all keys.</p>
											</TooltipContent>
										</Tooltip>
									</TooltipProvider>
								</div>
								<FormField
									control={form.control}
									name="selectedDBKeys"
									render={({ field }) => (
										<FormItem>
											<FormControl>
												<MultiSelect
													options={availableKeys.map((key) => ({
														label: key.name,
														value: key.key_id,
														description: key.models.join(", "),
														icon: ({ className }: { className?: string }) => (
															<RenderProviderIcon
																provider={key.provider as ProviderIconType}
																size="sm"
																className={className || "h-4 w-4"}
															/>
														),
													}))}
													defaultValue={field.value || []}
													onValueChange={field.onChange}
													placeholder="Select keys..."
													variant="inverted"
													className="hover:bg-accent w-full bg-white dark:bg-zinc-800"
													popoverClassName="z-[60] max-h-[300px] overflow-y-auto w-full"
													modalPopover={true}
													animation={0}
													animationConfig={{
														badgeAnimation: "none",
														popoverAnimation: "none",
														optionHoverAnimation: "none",
													}}
												/>
											</FormControl>
											<FormMessage />
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
													Configure which providers this virtual key can use and their specific settings. Leave empty to allow all
													providers.
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
										<SelectTrigger className="flex-1">
											<SelectValue placeholder="Select a provider to add" />
										</SelectTrigger>
										<SelectContent>
											{(() => {
												// Filter out already configured providers
												const unconfiguredProviders = availableProviders.filter(
													(provider) => !providerConfigs.some((config) => config.provider === provider.name),
												);

												if (unconfiguredProviders.length === 0) {
													return <div className="text-muted-foreground px-2 py-1.5 text-sm">All providers configured</div>;
												}

												// Separate base providers and custom providers
												const baseProviders = unconfiguredProviders.filter((provider) => !provider.custom_provider_config);
												const customProviders = unconfiguredProviders.filter((provider) => provider.custom_provider_config);

												return (
													<>
														{/* Base providers first */}
														{baseProviders.map((provider, index) => (
															<SelectItem key={`base-${index}`} value={provider.name}>
																<RenderProviderIcon provider={provider.name as KnownProvider} size="sm" className="h-4 w-4" />
																{ProviderLabels[provider.name as ProviderName]}
															</SelectItem>
														))}

														{/* Custom providers second */}
														{customProviders.map((provider, index) => (
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
									<div className="rounded-md border">
										<Table>
											<TableHeader>
												<TableRow>
													<TableHead>Provider</TableHead>
													<TableHead>Weight</TableHead>
													<TableHead>Allowed Models</TableHead>
													<TableHead className="w-[50px]"></TableHead>
												</TableRow>
											</TableHeader>
											<TableBody>
												{providerConfigs.map((config, index) => {
													const providerConfig = availableProviders.find((provider) => provider.name === config.provider);
													return (
														<TableRow key={`${config.provider}-${index}`}>
															<TableCell>
																<div className="flex items-center gap-2">
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
															</TableCell>
															<TableCell className="max-w-[100px]">
																<Input
																	placeholder="0.5"
																	className="w-full border-none"
																	value={config.weight}
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
															</TableCell>
															<TableCell className="max-w-[500px]">
																<TagInput
																	placeholder={
																		config.provider
																			? ModelPlaceholders[config.provider as keyof typeof ModelPlaceholders] || ModelPlaceholders.default
																			: ModelPlaceholders.default
																	}
																	value={config.allowed_models || []}
																	onValueChange={(models: string[]) => handleUpdateProviderConfig(index, "allowed_models", models)}
																	className="max-w-[500px] min-w-[200px] border-none"
																/>
															</TableCell>
															<TableCell>
																<Button type="button" variant="ghost" size="sm" onClick={() => handleRemoveProvider(index)}>
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

								{/* Display validation errors for provider configurations */}
								{form.formState.errors.providerConfigs && (
									<div className="text-destructive text-sm">{form.formState.errors.providerConfigs.message}</div>
								)}
							</div>

							{/* MCP Client Configurations */}
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
													Configure which MCP clients this virtual key can use and their allowed tools. Leave empty to allow all MCP clients
													and tools.
												</p>
											</TooltipContent>
										</Tooltip>
									</TooltipProvider>
								</div>

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
												{mcpClientsData.filter((client) => !mcpConfigs.some((config) => config.mcp_client_name === client.name)).length >
												0 ? (
													mcpClientsData
														.filter((client) => !mcpConfigs.some((config) => config.mcp_client_name === client.name))
														.map((client, index) => {
															const client_tools = client.tools || [];
															const totalTools = client.config.tools_to_execute?.includes("*")
																? client_tools.length
																: client_tools.filter((tool) => client.config.tools_to_execute?.includes(tool.name)).length;
															return (
																<SelectItem key={index} value={client.name}>
																	<div className="flex items-center gap-2">
																		{client.name}
																		<span className="text-muted-foreground text-xs">
																			({totalTools} {totalTools === 1 ? "tool" : "tools"})
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
													const mcpClient = mcpClientsData?.find((client) => client.name === config.mcp_client_name);

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
																	options={[...availableTools, ...enabledToolsByConfig]
																		.filter((tool, index, arr) => arr.findIndex((t) => t.name === tool.name) === index)
																		.map((tool) => ({
																			label: tool.name,
																			value: tool.name,
																			description: tool.description,
																		}))}
																	defaultValue={selectedTools}
																	onValueChange={(tools: string[]) => handleUpdateMCPConfig(index, "tools_to_execute", tools)}
																	placeholder={
																		selectedTools.length === 0
																			? "No tools selected"
																			: selectedTools.includes("*")
																				? "All tools selected"
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
																<Button type="button" variant="ghost" size="sm" onClick={() => handleRemoveMCPClient(index)}>
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

							<DottedSeparator className="mt-6 mb-5" />

							{/* Budget Configuration */}
							<div className="space-y-4">
								<Label className="text-sm font-medium">Budget Configuration</Label>
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
												onChangeSelect={(value) => form.setValue("budgetResetDuration", value)}
												options={resetDurationOptions}
											/>
											<FormMessage />
										</FormItem>
									)}
								/>
							</div>

							{/* Rate Limiting Configuration */}
							<div className="space-y-4">
								<Label className="text-sm font-medium">Rate Limiting Configuration</Label>

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
												onChangeSelect={(value) => form.setValue("tokenResetDuration", value)}
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
												onChangeSelect={(value) => form.setValue("requestResetDuration", value)}
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
														<Select onValueChange={field.onChange} defaultValue={field.value}>
															<FormControl className="w-full">
																<SelectTrigger>
																	<SelectValue />
																</SelectTrigger>
															</FormControl>
															<SelectContent>
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
																	<SelectTrigger>
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
																	<SelectTrigger>
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

						{/* Form Footer */}
						<div className="dark:bg-card border-border bg-white py-6">
							<div className="flex justify-end gap-2">
								<Button type="button" variant="outline" onClick={onCancel}>
									Cancel
								</Button>
								<TooltipProvider>
									<Tooltip>
										<TooltipTrigger asChild>
											<Button type="submit" disabled={isLoading || !form.formState.isDirty || !form.formState.isValid}>
												{isLoading ? "Saving..." : isEditing ? "Update" : "Create"}
											</Button>
										</TooltipTrigger>
										{(isLoading || !form.formState.isDirty || !form.formState.isValid) && (
											<TooltipContent>
												<p>
													{isLoading
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
