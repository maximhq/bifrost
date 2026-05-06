import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Fragment } from "react";

import { CodeEditor } from "@/components/ui/codeEditor";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { HeadersTable } from "@/components/ui/headersTable";
import { EnvVarInput } from "@/components/ui/envVarInput";
import { Input } from "@/components/ui/input";
import { MultiSelect } from "@/components/ui/multiSelect";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { TriStateCheckbox } from "@/components/ui/tristateCheckbox";
import { useToast } from "@/hooks/use-toast";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { MCP_STATUS_COLORS } from "@/lib/constants/config";
import { getErrorMessage, useGetCoreConfigQuery, useGetVirtualKeysQuery, useUpdateMCPClientMutation } from "@/lib/store";
import { MCPClient, MCPVKConfig } from "@/lib/types/mcp";
import { mcpClientUpdateSchema, type MCPClientUpdateSchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { ChevronDown, ChevronRight, Info, Plus, Trash2 } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useForm } from "react-hook-form";
import { OAuth2Authorizer } from "./oauth2Authorizer";

interface MCPClientSheetProps {
	mcpClient: MCPClient;
	onClose: () => void;
	onSubmitSuccess: () => void;
}

/** API sends tool_sync_interval as nanoseconds (Go time.Duration). Normalize to minutes for form/store. */
function toolSyncIntervalToMinutes(v: number | undefined | null): number {
	if (v === undefined || v === null) return 0;
	const n = Number(v);
	if (Number.isNaN(n)) return 0;
	if (Math.abs(n) >= 1e9) return Math.round(n / 6e10);
	return n;
}

export default function MCPClientSheet({ mcpClient, onClose, onSubmitSuccess }: MCPClientSheetProps) {
	const hasUpdateMCPClientAccess = useRbac(RbacResource.MCPGateway, RbacOperation.Update);
	const [updateMCPClient, { isLoading: isUpdating }] = useUpdateMCPClientMutation();
	const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true });
	const globalToolSyncInterval = bifrostConfig?.client_config?.mcp_tool_sync_interval ?? 10;
	const { toast } = useToast();
	const [expandedTools, setExpandedTools] = useState<Set<string>>(new Set());

	// VK access management — search-based dropdown (limit 20), no pagination issue
	const [vkSearch, setVKSearch] = useState("");
	const [vkPopoverOpen, setVKPopoverOpen] = useState(false);
	const debouncedVkSearch = useDebouncedValue(vkSearch, 300);
	const { data: vksData } = useGetVirtualKeysQuery({ limit: 20, search: debouncedVkSearch || undefined });
	const allToolNames = useMemo(() => mcpClient.tools?.map((t) => t.name) ?? [], [mcpClient.tools]);

	// Initial VK configs come directly from the MCP client response — always complete, no pagination issue.
	const initialVKConfigs = useMemo<MCPVKConfig[]>(
		() => (mcpClient.vk_configs ?? []).map((vc) => ({ virtual_key_id: vc.virtual_key_id, tools_to_execute: vc.tools_to_execute })),
		[mcpClient.vk_configs],
	);

	const [vkConfigs, setVKConfigs] = useState<MCPVKConfig[]>([]);
	const [vkConfigsDirty, setVKConfigsDirty] = useState(false);
	const [allowedExtraHeadersRaw, setAllowedExtraHeadersRaw] = useState<string>((mcpClient.config.allowed_extra_headers || []).join(", "));
	const [oauthFlow, setOauthFlow] = useState<{
		authorizeUrl: string;
		oauthConfigId: string;
		mcpClientId: string;
		isPerUserOauth?: boolean;
	} | null>(null);
	// Persists names for newly added VKs so they survive search result changes
	const [localVKNames, setLocalVKNames] = useState<Record<string, string>>({});

	// Sync vkConfigs when mcpClient changes
	useEffect(() => {
		setVKConfigs(initialVKConfigs);
		setVKConfigsDirty(false);
		setLocalVKNames({});
	}, [initialVKConfigs]);

	// Sync allowedExtraHeadersRaw when mcpClient changes
	useEffect(() => {
		setAllowedExtraHeadersRaw((mcpClient.config.allowed_extra_headers || []).join(", "));
	}, [mcpClient.config.allowed_extra_headers]);

	// Name lookup: server response names → search results → locally cached names (highest priority)
	const vkNameByID = useMemo<Record<string, string>>(() => {
		const m: Record<string, string> = {};
		for (const vc of mcpClient.vk_configs ?? []) m[vc.virtual_key_id] = vc.virtual_key_name;
		for (const vk of vksData?.virtual_keys ?? []) m[vk.id] = vk.name;
		Object.assign(m, localVKNames);
		return m;
	}, [mcpClient.vk_configs, vksData, localVKNames]);

	const vkOptions = useMemo(
		() =>
			(vksData?.virtual_keys ?? [])
				.filter((vk) => !vkConfigs.some((vc) => vc.virtual_key_id === vk.id))
				.map((vk) => ({ value: vk.id, label: vk.name })),
		[vksData, vkConfigs],
	);

	const toolOptions = useMemo(
		() => [
			{ value: "*", label: "Allow All Tools", description: "Allow all current and future tools" },
			...allToolNames.map((n) => ({ value: n, label: n })),
		],
		[allToolNames],
	);
	const supportsOAuthCredentialUpdate =
		mcpClient.config.auth_type === "oauth" || mcpClient.config.auth_type === "per_user_oauth";

	const addVKConfig = (vkId: string) => {
		const name = vksData?.virtual_keys?.find((vk) => vk.id === vkId)?.name;
		if (name) setLocalVKNames((prev) => ({ ...prev, [vkId]: name }));
		setVKConfigs((prev) => [...prev, { virtual_key_id: vkId, tools_to_execute: ["*"] }]);
		setVKConfigsDirty(true);
	};

	const removeVKConfig = (vkId: string) => {
		setVKConfigs((prev) => prev.filter((vc) => vc.virtual_key_id !== vkId));
		setVKConfigsDirty(true);
	};

	const updateVKConfigTools = (vkId: string, tools: string[]) => {
		setVKConfigs((prev) => prev.map((vc) => (vc.virtual_key_id === vkId ? { ...vc, tools_to_execute: tools } : vc)));
		setVKConfigsDirty(true);
	};

	const toggleToolExpanded = (toolName: string) => {
		setExpandedTools((prev) => {
			const next = new Set(prev);
			if (next.has(toolName)) {
				next.delete(toolName);
			} else {
				next.add(toolName);
			}
			return next;
		});
	};

	const form = useForm<MCPClientUpdateSchema>({
		resolver: zodResolver(mcpClientUpdateSchema),
		mode: "onBlur",
		defaultValues: {
			name: mcpClient.config.name,
			is_code_mode_client: mcpClient.config.is_code_mode_client || false,
			is_ping_available: mcpClient.config.is_ping_available === true || mcpClient.config.is_ping_available === undefined,
			allow_on_all_virtual_keys: mcpClient.config.allow_on_all_virtual_keys || false,
			disabled: mcpClient.config.disabled || false,
			headers: mcpClient.config.headers,
			tools_to_execute: mcpClient.config.tools_to_execute || [],
			tools_to_auto_execute: mcpClient.config.tools_to_auto_execute || [],
			tool_pricing: mcpClient.config.tool_pricing || {},
			tool_sync_interval: toolSyncIntervalToMinutes(mcpClient.config.tool_sync_interval),
			allowed_extra_headers: mcpClient.config.allowed_extra_headers || [],
			oauth_config: supportsOAuthCredentialUpdate
				? { client_id: mcpClient.config.oauth_client_id, client_secret: mcpClient.config.oauth_client_secret }
				: undefined,
		},
	});
	const isDisabled = form.watch("disabled");

	// Reset form when mcpClient changes
	useEffect(() => {
		form.reset({
			name: mcpClient.config.name,
			is_code_mode_client: mcpClient.config.is_code_mode_client || false,
			is_ping_available: mcpClient.config.is_ping_available === true || mcpClient.config.is_ping_available === undefined,
			allow_on_all_virtual_keys: mcpClient.config.allow_on_all_virtual_keys || false,
			disabled: mcpClient.config.disabled || false,
			headers: mcpClient.config.headers,
			tools_to_execute: mcpClient.config.tools_to_execute || [],
			tools_to_auto_execute: mcpClient.config.tools_to_auto_execute || [],
			tool_pricing: mcpClient.config.tool_pricing || {},
			tool_sync_interval: toolSyncIntervalToMinutes(mcpClient.config.tool_sync_interval),
			allowed_extra_headers: mcpClient.config.allowed_extra_headers || [],
			oauth_config: supportsOAuthCredentialUpdate
				? { client_id: mcpClient.config.oauth_client_id, client_secret: mcpClient.config.oauth_client_secret }
				: undefined,
		});
	}, [form, mcpClient]);

	const onSubmit = async (data: MCPClientUpdateSchema) => {
		try {
			const oauthClientID = data.oauth_config?.client_id;
			const oauthClientSecret = data.oauth_config?.client_secret;
			// Only rotate when the user actually changed a credential field.
			// dirtyFields tracks deep changes vs. the pre-populated default values.
			const oauthDirty = !!(form.formState.dirtyFields.oauth_config?.client_id || form.formState.dirtyFields.oauth_config?.client_secret);
			const shouldRotateOAuthCredentials = supportsOAuthCredentialUpdate && oauthDirty;
			const response = await updateMCPClient({
				id: mcpClient.config.client_id,
				data: {
					name: data.name,
					is_code_mode_client: data.is_code_mode_client,
					is_ping_available: data.is_ping_available,
					allow_on_all_virtual_keys: data.allow_on_all_virtual_keys,
					disabled: data.disabled,
					headers: data.headers ?? {},
					tools_to_execute: data.tools_to_execute,
					tools_to_auto_execute: data.tools_to_auto_execute,
					tool_pricing: data.tool_pricing,
					tool_sync_interval: data.tool_sync_interval ?? 0,
					allowed_extra_headers: data.allowed_extra_headers,
					oauth_config: shouldRotateOAuthCredentials
						? {
								client_id: oauthClientID,
								client_secret: oauthClientSecret,
							}
						: undefined,
					vk_configs: vkConfigsDirty ? vkConfigs : undefined,
				},
			}).unwrap();

			if (response.status === "pending_oauth" && response.authorize_url) {
				setOauthFlow({
					authorizeUrl: response.authorize_url,
					oauthConfigId: response.oauth_config_id,
					mcpClientId: response.mcp_client_id,
					isPerUserOauth: mcpClient.config.auth_type === "per_user_oauth",
				});
				return;
			}

			toast({
				title: "Success",
				description: "MCP client updated successfully",
			});
			onSubmitSuccess();
		} catch (error) {
			toast({
				title: "Error",
				description: getErrorMessage(error),
				variant: "destructive",
			});
		}
	};

	const handleToolToggle = (toolName: string, checked: boolean) => {
		const currentTools = form.getValues("tools_to_execute") || [];
		let newTools: string[];
		const allToolNames = mcpClient.tools?.map((tool) => tool.name) || [];

		// Check if we're in "all tools" mode (wildcard)
		const isAllToolsMode = currentTools.includes("*");

		if (isAllToolsMode) {
			if (checked) {
				// Already all selected, keep wildcard
				newTools = ["*"];
			} else {
				// Unchecking a tool when all are selected - switch to explicit list without this tool
				newTools = allToolNames.filter((name) => name !== toolName);
			}
		} else {
			// We're in explicit tool selection mode
			if (checked) {
				// Add tool to selection
				newTools = currentTools.includes(toolName) ? currentTools : [...currentTools, toolName];

				// If we now have all tools selected, switch to wildcard mode
				if (newTools.length === allToolNames.length) {
					newTools = ["*"];
				}
			} else {
				// Remove tool from selection
				newTools = currentTools.filter((tool) => tool !== toolName);
			}
		}

		form.setValue("tools_to_execute", newTools, { shouldDirty: true });

		// If tool is being removed from tools_to_execute, also remove it from tools_to_auto_execute
		if (!checked) {
			const currentAutoExecute = form.getValues("tools_to_auto_execute") || [];
			if (currentAutoExecute.includes(toolName) || currentAutoExecute.includes("*")) {
				const newAutoExecute = currentAutoExecute.filter((tool) => tool !== toolName);
				// If we had "*" and removed a tool, we need to recalculate
				if (currentAutoExecute.includes("*")) {
					// If all tools mode, keep "*" only if tool is still in tools_to_execute
					if (newTools.includes("*")) {
						form.setValue("tools_to_auto_execute", ["*"], { shouldDirty: true });
					} else {
						// Switch to explicit list - when in wildcard mode, all remaining tools should be auto-execute
						form.setValue("tools_to_auto_execute", newTools, { shouldDirty: true });
					}
				} else {
					form.setValue("tools_to_auto_execute", newAutoExecute, { shouldDirty: true });
				}
			}
		}
	};

	const handleAutoExecuteToggle = (toolName: string, checked: boolean) => {
		const currentAutoExecute = form.getValues("tools_to_auto_execute") || [];
		const currentTools = form.getValues("tools_to_execute") || [];
		const allToolNames = mcpClient.tools?.map((tool) => tool.name) || [];

		// Check if we're in "all tools" mode (wildcard)
		const isAllToolsMode = currentTools.includes("*");
		const isAllAutoExecuteMode = currentAutoExecute.includes("*");

		let newAutoExecute: string[];

		if (isAllAutoExecuteMode) {
			if (checked) {
				// Already all selected, keep wildcard
				newAutoExecute = ["*"];
			} else {
				// Unchecking a tool when all are selected - switch to explicit list without this tool
				if (isAllToolsMode) {
					newAutoExecute = allToolNames.filter((name) => name !== toolName);
				} else {
					newAutoExecute = currentTools.filter((name) => name !== toolName);
				}
			}
		} else {
			// We're in explicit tool selection mode
			if (checked) {
				// Add tool to selection
				newAutoExecute = currentAutoExecute.includes(toolName) ? currentAutoExecute : [...currentAutoExecute, toolName];

				// Only switch to wildcard if ALL tools are enabled (tools_to_execute is "*")
				// and all of those tools are now auto-executed. When specific tools are
				// explicitly listed, keep the explicit list to avoid sending "*" when only
				// a subset of tools is enabled.
				if (
					isAllToolsMode &&
					newAutoExecute.length === allToolNames.length &&
					allToolNames.every((tool) => newAutoExecute.includes(tool))
				) {
					newAutoExecute = ["*"];
				}
			} else {
				// Remove tool from selection
				newAutoExecute = currentAutoExecute.filter((tool) => tool !== toolName);
			}
		}

		form.setValue("tools_to_auto_execute", newAutoExecute, { shouldDirty: true });
	};

	return (
		<Sheet open onOpenChange={onClose}>
			<SheetContent className="flex w-full flex-col overflow-x-hidden pt-4 sm:max-w-[60%]">
				<SheetHeader className="w-full p-0 px-8 py-4" showCloseButton={false} headerClassName="mb-0 sticky -top-4 bg-card z-10">
					<div className="flex w-full items-center justify-between">
						<div className="space-y-2">
							<SheetTitle className="flex w-fit items-center gap-2 font-medium">
								{mcpClient.config.name}
								<Badge className={MCP_STATUS_COLORS[mcpClient.state]}>{mcpClient.state}</Badge>
							</SheetTitle>
							<SheetDescription>MCP server configuration and available tools</SheetDescription>
						</div>
					</div>
				</SheetHeader>
				<Form {...form}>
					<form onSubmit={form.handleSubmit(onSubmit)} className="flex h-full flex-col">
						<div className="gap-6 space-y-6 px-8">
							{/* Name and Header Section */}
							<div className="space-y-4">
								<h3 className="font-semibold">Basic Information</h3>
								<FormField
									control={form.control}
									name="name"
									render={({ field }) => (
										<FormItem className="flex flex-col gap-3">
											<div className="flex items-center gap-2">
												<FormLabel>Name</FormLabel>
												<TooltipProvider>
													<Tooltip>
														<TooltipTrigger asChild>
															<Info className="text-muted-foreground h-4 w-4 cursor-help" />
														</TooltipTrigger>
														<TooltipContent className="max-w-xs">
															<p>
																Use a descriptive, meaningful name that clearly identifies the server. For example, use "google_drive"
																instead of "gdrive", or "hacker_news" instead of "hn". This name is used as the Python module name in code
																mode.
															</p>
														</TooltipContent>
													</Tooltip>
												</TooltipProvider>
											</div>
											<div>
												<FormControl>
													<Input placeholder="Client name" {...field} value={field.value || ""} />
												</FormControl>
												<FormMessage />
											</div>
										</FormItem>
									)}
								/>
								<FormField
									control={form.control}
									name="is_code_mode_client"
									render={({ field }) => (
										<FormItem className="flex items-center justify-between rounded-lg border p-4">
											<FormLabel>Code Mode Client</FormLabel>
											<FormControl>
												<Switch checked={field.value || false} onCheckedChange={field.onChange} />
											</FormControl>
										</FormItem>
									)}
								/>
								<FormField
									control={form.control}
									name="is_ping_available"
									render={({ field }) => (
										<FormItem className="flex items-center justify-between rounded-lg border p-4">
											<div className="flex items-center gap-2">
												<FormLabel>Ping Available for Health Check</FormLabel>
												<TooltipProvider>
													<Tooltip>
														<TooltipTrigger asChild>
															<Info className="text-muted-foreground h-4 w-4 cursor-help" />
														</TooltipTrigger>
														<TooltipContent className="max-w-xs">
															<p>
																Enable to use lightweight ping method for health checks. Disable if your MCP server doesn't support ping -
																will use listTools instead.
															</p>
														</TooltipContent>
													</Tooltip>
												</TooltipProvider>
											</div>
											<FormControl>
												<Switch checked={field.value === true} onCheckedChange={field.onChange} />
											</FormControl>
										</FormItem>
									)}
								/>
								<FormField
									control={form.control}
									name="allow_on_all_virtual_keys"
									render={({ field }) => (
										<FormItem className="flex items-center justify-between rounded-lg border p-4">
											<div className="flex items-center gap-2">
												<FormLabel>Allow on All Virtual Keys</FormLabel>
												<TooltipProvider>
													<Tooltip>
														<TooltipTrigger asChild>
															<Info className="text-muted-foreground h-4 w-4 cursor-help" />
														</TooltipTrigger>
														<TooltipContent className="max-w-xs">
															<p>
																When enabled, this MCP server is accessible to all virtual keys without requiring explicit per-key
																assignment. All tools are allowed by default. If a virtual key has an explicit MCP config for this server,
																that config takes precedence and overrides this behaviour.
															</p>
														</TooltipContent>
													</Tooltip>
												</TooltipProvider>
											</div>
											<FormControl>
												<Switch
													checked={field.value === true}
													onCheckedChange={field.onChange}
													data-testid="mcpclient-allow-on-all-virtual-keys-switch"
												/>
											</FormControl>
										</FormItem>
									)}
								/>
								<FormField
									control={form.control}
									name="disabled"
									render={({ field }) => (
										<FormItem className="flex items-center justify-between rounded-lg border p-4">
											<div className="flex items-center gap-2">
												<FormLabel>Disable Client</FormLabel>
												<TooltipProvider>
													<Tooltip>
														<TooltipTrigger asChild>
															<Info className="text-muted-foreground h-4 w-4 cursor-help" />
														</TooltipTrigger>
														<TooltipContent className="max-w-xs">
															<p>
																When enabled, the client's connection, health monitor, and tool syncer are shut down. Tools from this
																client will not be available for inference until it is re-enabled.
															</p>
														</TooltipContent>
													</Tooltip>
												</TooltipProvider>
											</div>
											<FormControl>
												<Switch
													checked={field.value === true}
													onCheckedChange={(checked) => {
														field.onChange(checked);
														if (checked) {
															form.setValue("oauth_config", undefined);
														}
													}}
													data-testid="mcpclient-disabled-switch"
												/>
											</FormControl>
										</FormItem>
									)}
								/>
								<FormField
									control={form.control}
									name="tool_sync_interval"
									render={({ field }) => {
										const isUsingGlobal = field.value === undefined || field.value === null || field.value === 0;
										return (
											<FormItem className="flex items-center justify-between rounded-lg border px-4 py-2">
												<div className="flex flex-col items-start gap-0.5">
													<div className="flex items-start gap-2">
														<div>
															<FormLabel>Tool Sync Interval (minutes)</FormLabel>
														</div>
														<TooltipProvider>
															<Tooltip>
																<TooltipTrigger asChild>
																	<Info className="text-muted-foreground h-4 w-4 cursor-help" />
																</TooltipTrigger>
																<TooltipContent className="max-w-xs">
																	<p>
																		Override the global tool sync interval for this server. Leave empty to use global setting. Set to -1 to
																		disable sync for this server.
																	</p>
																</TooltipContent>
															</Tooltip>
														</TooltipProvider>
													</div>
													<div>{isUsingGlobal && <p className="text-muted-foreground text-xs">Using global setting</p>}</div>
												</div>
												<FormControl>
													<Input
														type="number"
														className={`w-24 ${isUsingGlobal ? "text-muted-foreground" : ""}`}
														placeholder={String(globalToolSyncInterval)}
														value={field.value === 0 || field.value === undefined ? "" : String(field.value)}
														onChange={(e) => {
															const val = e.target.value === "" ? undefined : parseInt(e.target.value);
															field.onChange(val);
														}}
														min="-1"
													/>
												</FormControl>
											</FormItem>
										);
									}}
								/>
								<FormField
									control={form.control}
									name="headers"
									render={({ field }) => (
										<FormItem className="flex flex-col gap-3">
											<FormControl>
												<HeadersTable
													value={field.value || {}}
													onChange={field.onChange}
													keyPlaceholder="Header name"
													valuePlaceholder="Header value"
													label="Headers"
													useEnvVarInput
												/>
											</FormControl>
											<FormMessage />
										</FormItem>
									)}
								/>
								<FormField
									control={form.control}
									name="allowed_extra_headers"
									render={({ field }) => (
										<FormItem className="flex flex-col gap-2">
											<div className="flex items-center gap-2">
												<FormLabel>Allowed Extra Headers</FormLabel>
												<TooltipProvider>
													<Tooltip>
														<TooltipTrigger asChild>
															<Info className="text-muted-foreground h-4 w-4 cursor-help" />
														</TooltipTrigger>
														<TooltipContent className="max-w-xs">
															<p>Allowlist of headers that callers can forward to this MCP server at request time.</p>
														</TooltipContent>
													</Tooltip>
												</TooltipProvider>
											</div>
											<FormControl>
												<Input
													data-testid="mcpclient-input-allowed-extra-headers"
													placeholder="*, or: authorization, x-user-id"
													name={field.name}
													ref={field.ref}
													value={allowedExtraHeadersRaw}
													onChange={(e) => {
														setAllowedExtraHeadersRaw(e.target.value);
													}}
													onBlur={() => {
														const parsed = allowedExtraHeadersRaw.trim()
															? allowedExtraHeadersRaw
																.split(",")
																.map((h) => h.trim())
																.filter(Boolean)
															: [];
														field.onChange(parsed);
														field.onBlur();
													}}
												/>
											</FormControl>
											<p className="text-muted-foreground text-xs">
												Comma-separated header names, or <code>*</code> to allow all. Leave empty to block all extra headers.
											</p>
											<FormMessage />
										</FormItem>
									)}
								/>
							</div>
							{supportsOAuthCredentialUpdate ? (
								<div className="space-y-4">
									<h3 className="font-semibold">OAuth Credentials</h3>
									{isDisabled ? (
										<div className="flex items-start gap-2 rounded-lg border border-amber-200 bg-amber-50 p-3 text-sm text-amber-800">
											<Info className="mt-0.5 h-4 w-4 shrink-0 text-amber-600" />
											<p>
												OAuth credentials cannot be rotated while the client is disabled. Re-enable the client to update credentials.
											</p>
										</div>
									) : (
										<p className="text-muted-foreground text-sm">
											Update OAuth client credentials only. Connection type, auth type, and connection URL cannot be changed.
										</p>
									)}
									<div className="grid grid-cols-1 gap-4 md:grid-cols-2">
										<FormField
											control={form.control}
											name="oauth_config.client_id"
											render={({ field }) => (
												<FormItem className="flex flex-col gap-2">
													<FormLabel>Client ID</FormLabel>
													<FormControl>
														<EnvVarInput
															data-testid="mcpclient-input-oauth-client-id"
															placeholder="Enter new OAuth client ID"
															disabled={isDisabled}
															value={field.value}
															onChange={field.onChange}
														/>
													</FormControl>
													{!isDisabled && (
														<p className="text-muted-foreground text-xs">
															Leave empty to keep existing credentials unchanged.
														</p>
													)}
													<FormMessage />
												</FormItem>
											)}
										/>
										<FormField
											control={form.control}
											name="oauth_config.client_secret"
											render={({ field }) => (
												<FormItem className="flex flex-col gap-2">
													<FormLabel>Client Secret</FormLabel>
													<FormControl>
														<EnvVarInput
															data-testid="mcpclient-input-oauth-client-secret"
															placeholder="Enter new OAuth client secret"
															disabled={isDisabled}
															hideValueWhenEnv
															maskNonEnvValue
															value={field.value}
															onChange={field.onChange}
														/>
													</FormControl>
													<FormMessage />
												</FormItem>
											)}
										/>
									</div>
								</div>
							) : null}
							{/* Tools Section */}
							<div className="space-y-4 pb-10">
								<div className="flex items-center justify-between">
									<h3 className="font-semibold">Available Tools ({mcpClient.tools?.length || 0})</h3>
									{mcpClient.tools && mcpClient.tools.length > 0 && (
										<div className="flex items-center gap-4">
											{/* Enable All */}
											<FormField
												control={form.control}
												name="tools_to_execute"
												render={() => {
													const currentTools = form.watch("tools_to_execute") || [];
													const allToolNames = mcpClient.tools?.map((tool) => tool.name) || [];
													const isAllEnabled = currentTools.includes("*");
													const isNoneEnabled = currentTools.length === 0;
													const selectedIds = isAllEnabled ? allToolNames : currentTools;

													return (
														<FormItem>
															<FormControl>
																<div className="flex items-center gap-2">
																	<span className="text-muted-foreground text-sm">
																		{isAllEnabled ? "All enabled" : isNoneEnabled ? "None enabled" : `${currentTools.length} enabled`}
																	</span>
																	<TriStateCheckbox
																		allIds={allToolNames}
																		selectedIds={selectedIds}
																		onChange={(nextSelectedIds) => {
																			if (nextSelectedIds.length === 0) {
																				form.setValue("tools_to_execute", [], { shouldDirty: true });
																				// Also clear auto-execute when disabling all
																				form.setValue("tools_to_auto_execute", [], { shouldDirty: true });
																			} else if (nextSelectedIds.length === allToolNames.length) {
																				form.setValue("tools_to_execute", ["*"], { shouldDirty: true });
																			} else {
																				form.setValue("tools_to_execute", nextSelectedIds, { shouldDirty: true });
																			}
																		}}
																	/>
																</div>
															</FormControl>
														</FormItem>
													);
												}}
											/>
											{/* Auto-execute All */}
											<FormField
												control={form.control}
												name="tools_to_auto_execute"
												render={() => {
													const currentTools = form.watch("tools_to_execute") || [];
													const currentAutoExecute = form.watch("tools_to_auto_execute") || [];
													const allToolNames = mcpClient.tools?.map((tool) => tool.name) || [];

													// Get the list of enabled tools
													const enabledToolNames = currentTools.includes("*") ? allToolNames : currentTools;
													const isAllAutoExecute = currentAutoExecute.includes("*");
													const isNoneAutoExecute = currentAutoExecute.length === 0;

													// For TriStateCheckbox, we need the selected auto-execute tools that are also enabled
													const selectedAutoExecuteIds = isAllAutoExecute
														? enabledToolNames
														: currentAutoExecute.filter((t) => enabledToolNames.includes(t));

													const autoExecuteCount = isAllAutoExecute ? enabledToolNames.length : selectedAutoExecuteIds.length;

													return (
														<FormItem>
															<FormControl>
																<div className="flex items-center gap-2">
																	<span className="text-muted-foreground text-sm">
																		{isAllAutoExecute
																			? "All auto-execute"
																			: isNoneAutoExecute
																				? "None auto-execute"
																				: `${autoExecuteCount} auto-execute`}
																	</span>
																	<TriStateCheckbox
																		allIds={enabledToolNames}
																		selectedIds={selectedAutoExecuteIds}
																		disabled={enabledToolNames.length === 0}
																		onChange={(nextSelectedIds) => {
																			if (nextSelectedIds.length === 0) {
																				form.setValue("tools_to_auto_execute", [], { shouldDirty: true });
																			} else if (nextSelectedIds.length === enabledToolNames.length) {
																				form.setValue("tools_to_auto_execute", ["*"], { shouldDirty: true });
																			} else {
																				form.setValue("tools_to_auto_execute", nextSelectedIds, { shouldDirty: true });
																			}
																		}}
																	/>
																</div>
															</FormControl>
														</FormItem>
													);
												}}
											/>
										</div>
									)}
								</div>

								{mcpClient.tools && mcpClient.tools.length > 0 ? (
									<div className="rounded-md border">
										<Table>
											<TableHeader>
												<TableRow>
													<TableHead className="w-10"></TableHead>
													<TableHead className="max-w-[300px]">Tool Name</TableHead>
													<TableHead className="w-24 text-center">Enabled</TableHead>
													<TableHead className="w-28 text-center">Auto-execute</TableHead>
													<TableHead className="w-32 text-center">Cost (USD)</TableHead>
												</TableRow>
											</TableHeader>
											<TableBody>
												{mcpClient.tools.map((tool, index) => {
													const currentTools = form.watch("tools_to_execute") || [];
													const currentAutoExecute = form.watch("tools_to_auto_execute") || [];
													const isToolEnabled = currentTools?.includes("*") || currentTools?.includes(tool.name);
													const isAutoExecuteEnabled =
														(currentAutoExecute?.includes("*") && isToolEnabled) ||
														(currentAutoExecute?.includes(tool.name) && isToolEnabled);
													const isExpanded = expandedTools.has(tool.name);

													return (
														<Fragment key={index}>
															<TableRow className="group">
																<TableCell className="p-2">
																	<button
																		type="button"
																		className="hover:bg-muted flex h-8 w-8 items-center justify-center rounded-md transition-colors"
																		onClick={() => toggleToolExpanded(tool.name)}
																	>
																		{isExpanded ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
																	</button>
																</TableCell>
																<TableCell className="max-w-[300px]">
																	<div className="min-w-0">
																		<div className="text-foreground truncate text-sm font-medium">{tool.name}</div>
																		{tool.description && (
																			<p className="text-muted-foreground mt-0.5 truncate text-xs">{tool.description}</p>
																		)}
																	</div>
																</TableCell>
																<TableCell className="text-center">
																	<FormField
																		control={form.control}
																		name="tools_to_execute"
																		render={() => (
																			<FormItem>
																				<FormControl>
																					<Switch
																						size="md"
																						checked={isToolEnabled}
																						onCheckedChange={(checked) => handleToolToggle(tool.name, checked)}
																					/>
																				</FormControl>
																			</FormItem>
																		)}
																	/>
																</TableCell>
																<TableCell className="text-center">
																	<FormField
																		control={form.control}
																		name="tools_to_auto_execute"
																		render={() => (
																			<FormItem>
																				<FormControl>
																					<Switch
																						size="md"
																						checked={isAutoExecuteEnabled}
																						disabled={!isToolEnabled}
																						onCheckedChange={(checked) => handleAutoExecuteToggle(tool.name, checked)}
																					/>
																				</FormControl>
																			</FormItem>
																		)}
																	/>
																</TableCell>
																<TableCell className="text-center">
																	<FormField
																		control={form.control}
																		name="tool_pricing"
																		render={({ field }) => (
																			<FormItem>
																				<FormControl>
																					<Input
																						type="number"
																						step="0.000001"
																						min="0"
																						placeholder="0.00"
																						className="h-8 w-24"
																						disabled={!isToolEnabled}
																						value={field.value?.[tool.name] ?? ""}
																						onChange={(e) => {
																							const value = e.target.value === "" ? undefined : parseFloat(e.target.value);
																							const newPricing = { ...field.value };
																							if (value === undefined || isNaN(value)) {
																								delete newPricing[tool.name];
																							} else {
																								newPricing[tool.name] = value;
																							}
																							field.onChange(newPricing);
																						}}
																					/>
																				</FormControl>
																			</FormItem>
																		)}
																	/>
																</TableCell>
															</TableRow>
															{isExpanded && (
																<tr>
																	<td colSpan={5} className="p-0">
																		<div className="bg-muted/30 border-b px-4 py-3">
																			<div className="text-muted-foreground mb-2 text-xs font-medium">Parameters Schema</div>
																			{tool.parameters ? (
																				<CodeEditor
																					className="z-0 w-full rounded-sm border"
																					shouldAdjustInitialHeight={true}
																					maxHeight={300}
																					wrap={true}
																					code={JSON.stringify(tool.parameters, null, 2)}
																					lang="json"
																					readonly={true}
																					options={{
																						scrollBeyondLastLine: false,
																						collapsibleBlocks: true,
																						lineNumbers: "off",
																						alwaysConsumeMouseWheel: false,
																					}}
																				/>
																			) : (
																				<div className="text-muted-foreground text-sm">No parameters defined</div>
																			)}
																		</div>
																	</td>
																</tr>
															)}
														</Fragment>
													);
												})}
											</TableBody>
										</Table>
									</div>
								) : (
									<div className="text-muted-foreground rounded-sm border p-6 text-center">
										<p className="text-sm">No tools available</p>
									</div>
								)}

								{mcpClient.tools && mcpClient.tools.length > 0 && (
									<div className="mt-6 space-y-4 pb-10">
										<div className="flex flex-col gap-2">
											<div className="flex items-center justify-between">
												<div className="flex items-center gap-2">
													<div className="text-md font-semibold">Virtual Key Access</div>
													<TooltipProvider>
														<Tooltip>
															<TooltipTrigger asChild>
																<Info className="text-muted-foreground h-4 w-4 cursor-help" />
															</TooltipTrigger>
															<TooltipContent className="max-w-xs">
																<p>Control which virtual keys can use this MCP server and which specific tools they can call.</p>
															</TooltipContent>
														</Tooltip>
													</TooltipProvider>
												</div>
												<Popover
													open={vkPopoverOpen}
													onOpenChange={(open) => {
														setVKPopoverOpen(open);
														if (!open) setVKSearch("");
													}}
												>
													<PopoverTrigger asChild>
														<Button
															type="button"
															variant="outline"
															size="sm"
															className="h-7.5 gap-1.5 px-2 py-1 text-sm font-medium"
															data-testid="mcpclient-virtualkey-add-trigger"
														>
															<Plus className="h-4 w-4" />
															Add Virtual Key
														</Button>
													</PopoverTrigger>
													<PopoverContent side="top" align="end" className="w-56 p-0" noPortal>
														<div className="pb-1">
															<Input
																data-testid="mcpclient-virtualkey-search-input"
																placeholder="Start typing to search…"
																value={vkSearch}
																onChange={(e) => setVKSearch(e.target.value)}
																onKeyDown={(e) => {
																	e.stopPropagation();
																	if (e.key === "Enter") e.preventDefault();
																}}
																className="h-7 rounded-b-none border-0 border-b text-sm focus-visible:ring-0"
																autoFocus
															/>
														</div>
														<div className="max-h-48 overflow-y-auto p-1">
															{vkOptions.length > 0 ? (
																vkOptions.map((opt) => (
																	<button
																		data-testid={`mcpclient-virtualkey-option-${opt.value}`}
																		key={opt.value}
																		type="button"
																		className="hover:bg-accent hover:text-accent-foreground w-full cursor-pointer rounded-sm px-2 py-1.5 text-left text-sm"
																		onClick={() => {
																			addVKConfig(opt.value);
																			setVKSearch("");
																			setVKPopoverOpen(false);
																		}}
																	>
																		{opt.label}
																	</button>
																))
															) : (
																<div className="text-muted-foreground px-2 py-1.5 text-sm">No virtual keys found</div>
															)}
														</div>
													</PopoverContent>
												</Popover>
											</div>
											{form.watch("allow_on_all_virtual_keys") && (
												<p className="text-muted-foreground flex items-center gap-1 text-xs">
													<Info className="h-3 w-3 shrink-0" />
													Configuring access for a virtual key here overrides the{" "}
													<span className="font-medium">Allow on All Virtual Keys</span>&nbsp;setting for that key.
												</p>
											)}
										</div>

										{vkConfigs.length > 0 ? (
											<div className="rounded-md border">
												<Table>
													<TableHeader>
														<TableRow>
															<TableHead>Virtual Key</TableHead>
															<TableHead>Allowed Tools</TableHead>
															<TableHead className="w-12"></TableHead>
														</TableRow>
													</TableHeader>
													<TableBody>
														{vkConfigs.map((vc) => (
															<TableRow key={vc.virtual_key_id}>
																<TableCell className="font-medium">{vkNameByID[vc.virtual_key_id] ?? vc.virtual_key_id}</TableCell>
																<TableCell>
																	<MultiSelect
																		data-testid={`mcpclient-virtualkey-tool-selector-${vc.virtual_key_id}`}
																		options={toolOptions}
																		defaultValue={vc.tools_to_execute}
																		resetOnDefaultValueChange
																		onValueChange={(tools) => {
																			const hadStar = vc.tools_to_execute.includes("*");
																			const hasStar = tools.includes("*");
																			let next: string[];
																			if (!hadStar && hasStar) {
																				next = ["*"];
																			} else if (hadStar && hasStar && tools.length > 1) {
																				next = tools.filter((t) => t !== "*");
																			} else {
																				next = tools;
																			}
																			updateVKConfigTools(vc.virtual_key_id, next);
																		}}
																		placeholder={
																			vc.tools_to_execute.includes("*")
																				? "All tools allowed"
																				: vc.tools_to_execute.length === 0
																					? "No tools allowed"
																					: "Select tools..."
																		}
																		maxCount={3}
																		className="bg-background dark:bg-input/30 border-input text-foreground hover:bg-accent hover:text-accent-foreground rounded-sm font-normal"
																	/>
																</TableCell>
																<TableCell>
																	<Button
																		type="button"
																		variant="ghost"
																		size="icon"
																		onClick={() => removeVKConfig(vc.virtual_key_id)}
																		className="text-muted-foreground hover:text-destructive"
																		data-testid={`mcpclient-virtualkey-remove-${vc.virtual_key_id}`}
																	>
																		<Trash2 className="h-4 w-4" />
																	</Button>
																</TableCell>
															</TableRow>
														))}
													</TableBody>
												</Table>
											</div>
										) : form.watch("allow_on_all_virtual_keys") ? (
											<div className="text-muted-foreground rounded-sm border p-6 text-center">
												<p className="text-sm">All virtual keys can access this MCP server unless a key has an explicit override.</p>
											</div>
										) : (
											<div className="text-muted-foreground rounded-sm border p-6 text-center">
												<p className="text-sm">No virtual keys have access to this MCP server</p>
											</div>
										)}
									</div>
								)}
							</div>
						</div>

						<div className="bg-card sticky bottom-0 z-10 flex justify-end gap-2 border-t px-8 py-4">
							<Button type="button" variant="outline" onClick={onClose}>
								Cancel
							</Button>
							<Button
								type="submit"
								disabled={isUpdating || (!form.formState.isDirty && !vkConfigsDirty) || !hasUpdateMCPClientAccess}
								isLoading={isUpdating}
							>
								Save Changes
							</Button>
						</div>
					</form>
				</Form>
			</SheetContent>
			{oauthFlow && (
				<OAuth2Authorizer
					open={!!oauthFlow}
					onClose={() => setOauthFlow(null)}
					onSuccess={() => {
						toast({ title: "Success", description: "MCP client OAuth credentials updated successfully" });
						onSubmitSuccess();
						onClose();
					}}
					onError={(error) => {
						toast({ title: "Error", description: error, variant: "destructive" });
					}}
					authorizeUrl={oauthFlow.authorizeUrl}
					oauthConfigId={oauthFlow.oauthConfigId}
					mcpClientId={oauthFlow.mcpClientId}
					isPerUserOauth={oauthFlow.isPerUserOauth}
				/>
			)}
		</Sheet>
	);
}