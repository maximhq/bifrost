"use client";

import { Fragment } from "react";
import { CodeEditor } from "@/app/workspace/logs/views/codeEditor";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { HeadersTable } from "@/components/ui/headersTable";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { TriStateCheckbox } from "@/components/ui/tristateCheckbox";
import { useToast } from "@/hooks/use-toast";
import { MCP_STATUS_COLORS } from "@/lib/constants/config";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateMCPClientMutation } from "@/lib/store";
import { MCPClient } from "@/lib/types/mcp";
import { mcpClientUpdateSchema, type MCPClientUpdateSchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { ChevronDown, ChevronRight, Info } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";

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
			headers: mcpClient.config.headers,
			tools_to_execute: mcpClient.config.tools_to_execute || [],
			tools_to_auto_execute: mcpClient.config.tools_to_auto_execute || [],
			tool_pricing: mcpClient.config.tool_pricing || {},
			tool_sync_interval: toolSyncIntervalToMinutes(mcpClient.config.tool_sync_interval),
		},
	});

	// Reset form when mcpClient changes
	useEffect(() => {
		form.reset({
			name: mcpClient.config.name,
			is_code_mode_client: mcpClient.config.is_code_mode_client || false,
			is_ping_available: mcpClient.config.is_ping_available === true || mcpClient.config.is_ping_available === undefined,
			headers: mcpClient.config.headers,
			tools_to_execute: mcpClient.config.tools_to_execute || [],
			tools_to_auto_execute: mcpClient.config.tools_to_auto_execute || [],
			tool_pricing: mcpClient.config.tool_pricing || {},
			tool_sync_interval: toolSyncIntervalToMinutes(mcpClient.config.tool_sync_interval),
		});
	}, [form, mcpClient]);

	const onSubmit = async (data: MCPClientUpdateSchema) => {
		try {
			await updateMCPClient({
				id: mcpClient.config.client_id,
				data: {
					name: data.name,
					is_code_mode_client: data.is_code_mode_client,
					is_ping_available: data.is_ping_available,
					headers: data.headers ?? {},
					tools_to_execute: data.tools_to_execute,
					tools_to_auto_execute: data.tools_to_auto_execute,
					tool_pricing: data.tool_pricing,
					tool_sync_interval: data.tool_sync_interval ?? 0,
				},
			}).unwrap();

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

				// If we now have all allowed tools selected, switch to wildcard mode
				const allowedTools = isAllToolsMode ? allToolNames : currentTools;
				if (newAutoExecute.length === allowedTools.length && allowedTools.every((tool) => newAutoExecute.includes(tool))) {
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
			<SheetContent className="dark:bg-card flex w-full flex-col overflow-x-hidden bg-white p-8 sm:max-w-[60%]">
				<Form {...form}>
					<form onSubmit={form.handleSubmit(onSubmit)} className="flex h-full flex-col">
						<SheetHeader className="w-full p-0" showCloseButton={false}>
							<div className="flex w-full items-center justify-between">
								<div className="space-y-2">
									<SheetTitle className="flex w-fit items-center gap-2 font-medium">
										{mcpClient.config.name}
										<Badge className={MCP_STATUS_COLORS[mcpClient.state]}>{mcpClient.state}</Badge>
									</SheetTitle>
									<SheetDescription>MCP server configuration and available tools</SheetDescription>
								</div>
								<Button
									className="ml-auto"
									type="submit"
									disabled={isUpdating || !form.formState.isDirty || !hasUpdateMCPClientAccess}
									isLoading={isUpdating}
								>
									Save Changes
								</Button>
							</div>
						</SheetHeader>

						<div className="gap-6 space-y-6">
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
							</div>
							{/* Client Configuration */}
							<div className="space-y-4">
								<h3 className="font-semibold">Configuration</h3>
								<div className="rounded-sm border">
									<div className="bg-muted/50 text-muted-foreground border-b px-6 py-2 text-xs font-medium">Client ConnectionConfig</div>
									<CodeEditor
										className="z-0 w-full"
										shouldAdjustInitialHeight={true}
										maxHeight={300}
										wrap={true}
										code={JSON.stringify(
											(() => {
												const { client_id, name, tools_to_execute, headers, ...rest } = mcpClient.config;
												return rest;
											})(),
											null,
											2,
										)}
										lang="json"
										readonly={true}
										options={{
											scrollBeyondLastLine: false,
											collapsibleBlocks: true,
											lineNumbers: "off",
											alwaysConsumeMouseWheel: false,
										}}
									/>
								</div>
							</div>
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
												render={({ field }) => {
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
												render={({ field }) => {
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
							</div>
						</div>
					</form>
				</Form>
			</SheetContent>
		</Sheet>
	);
}
