"use client";

import { CodeEditor } from "@/app/workspace/logs/views/codeEditor";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { HeadersTable } from "@/components/ui/headersTable";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { TriStateCheckbox } from "@/components/ui/tristateCheckbox";
import { useToast } from "@/hooks/use-toast";
import { MCP_STATUS_COLORS } from "@/lib/constants/config";
import { getErrorMessage, useUpdateMCPClientMutation } from "@/lib/store";
import { MCPClient } from "@/lib/types/mcp";
import { mcpClientUpdateSchema, type MCPClientUpdateSchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { Info } from "lucide-react";
import { useEffect } from "react";
import { useForm } from "react-hook-form";

interface MCPClientSheetProps {
	mcpClient: MCPClient;
	onClose: () => void;
	onSubmitSuccess: () => void;
}

export default function MCPClientSheet({ mcpClient, onClose, onSubmitSuccess }: MCPClientSheetProps) {
	const hasUpdateMCPClientAccess = useRbac(RbacResource.MCPGateway, RbacOperation.Update);
	const [updateMCPClient, { isLoading: isUpdating }] = useUpdateMCPClientMutation();
	const { toast } = useToast();

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
		});
	}, [form, mcpClient]);

	const onSubmit = async (data: MCPClientUpdateSchema) => {
		try {
			await updateMCPClient({
				id: mcpClient.config.id,
				data: {
					name: data.name,
					is_code_mode_client: data.is_code_mode_client,
					is_ping_available: data.is_ping_available,
					headers: data.headers,
					tools_to_execute: data.tools_to_execute,
					tools_to_auto_execute: data.tools_to_auto_execute,
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
			<SheetContent className="dark:bg-card flex w-full flex-col overflow-x-hidden bg-white p-8 sm:max-w-2xl">
				<Form {...form}>
					<form onSubmit={form.handleSubmit(onSubmit)} className="flex h-full flex-col ">
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

						<div className="space-y-6 gap-6">
							{/* Name and Header Section */}
							<div className="space-y-4">
								<h3 className="font-semibold">Basic Information</h3>
								<FormField
									control={form.control}
									name="name"
									render={({ field }) => (
										<FormItem className="flex flex-col gap-3">
											<FormLabel>Name</FormLabel>
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
																Enable to use lightweight ping method for health checks. Disable if your MCP server doesn't support ping - will use listTools instead.
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
												const { id, name, tools_to_execute, headers, ...rest } = mcpClient.config;
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
										<FormField
											control={form.control}
											name="tools_to_execute"
											render={({ field }) => {
												const currentTools = form.watch("tools_to_execute") || [];
												const allToolNames = mcpClient.tools?.map((tool) => tool.name) || [];
												const isAllEnabled = currentTools.includes("*");
												const isNoneEnabled = currentTools.length === 0;

												// Convert to explicit IDs for TriStateCheckbox
												const selectedIds = isAllEnabled ? allToolNames : currentTools;

												return (
													<FormItem>
														<FormControl>
															<div className="flex items-center gap-2">
																<span className="text-muted-foreground text-sm">
																	{isAllEnabled ? "All enabled" : isNoneEnabled ? "All disabled" : `${currentTools.length} enabled`}
																</span>
																<TriStateCheckbox
																	allIds={allToolNames}
																	selectedIds={selectedIds}
																	onChange={(nextSelectedIds) => {
																		// Convert back to wildcard format
																		if (nextSelectedIds.length === 0) {
																			// None selected
																			form.setValue("tools_to_execute", [], { shouldDirty: true });
																		} else if (nextSelectedIds.length === allToolNames.length) {
																			// All selected - use wildcard
																			form.setValue("tools_to_execute", ["*"], { shouldDirty: true });
																		} else {
																			// Some selected - use explicit list
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
									)}
								</div>

								{mcpClient.tools && mcpClient.tools.length > 0 ? (
									<div className="space-y-4">
										{mcpClient.tools.map((tool, index) => {
											const currentTools = form.watch("tools_to_execute") || [];
											const currentAutoExecute = form.watch("tools_to_auto_execute") || [];

											// If tools_to_execute contains "*", all tools are enabled
											const isToolEnabled = currentTools?.includes("*") || currentTools?.includes(tool.name);
											// If tools_to_auto_execute contains "*", all enabled tools are auto-executed
											const isAutoExecuteEnabled =
												(currentAutoExecute?.includes("*") && isToolEnabled) || (currentAutoExecute?.includes(tool.name) && isToolEnabled);
											// Disable auto-execute toggle if tool is not in tools_to_execute
											const isAutoExecuteDisabled = !isToolEnabled;

											return (
												<div key={index} className="rounded-sm border">
													{/* Tool Header */}
													<div className="bg-muted/50 text-muted-foreground border-b px-6 py-3">
														<div className="flex items-center justify-between gap-4">
															<div className="flex-1">
																<span className="text-sm font-medium">{tool.name}</span>
																{tool.description && <p className="text-muted-foreground mt-1 text-xs">{tool.description}</p>}
															</div>
															<div className="flex flex-col items-end gap-1">
																<span className="text-muted-foreground text-xs">Enabled</span>
																<FormField
																	control={form.control}
																	name="tools_to_execute"
																	render={({ field }) => (
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
															</div>
														</div>
													</div>

													{isToolEnabled && (
														<div className="flex items-center justify-between gap-2 border-b px-6 py-2">
															<span className="text-muted-foreground text-xs font-medium">Automatically execute tool</span>
															<FormField
																control={form.control}
																name="tools_to_auto_execute"
																render={({ field }) => (
																	<FormItem>
																		<FormControl>
																			<Switch
																				size="md"
																				checked={isAutoExecuteEnabled}
																				disabled={isAutoExecuteDisabled}
																				onCheckedChange={(checked) => handleAutoExecuteToggle(tool.name, checked)}
																			/>
																		</FormControl>
																	</FormItem>
																)}
															/>
														</div>
													)}

													{/* Tool Parameters */}
													{tool.parameters ? (
														<div>
															<div className="bg-muted/30 text-muted-foreground border-b px-6 py-2 text-xs font-medium">Parameters</div>
															<CodeEditor
																className="z-0 w-full"
																shouldAdjustInitialHeight={true}
																maxHeight={400}
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
														</div>
													) : (
														<div className="text-muted-foreground px-6 py-3 text-sm">No parameters defined</div>
													)}
												</div>
											);
										})}
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
