"use client";

import { CodeEditor } from "@/app/workspace/logs/views/codeEditor";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { HeadersTable } from "@/components/ui/headersTable";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { useToast } from "@/hooks/use-toast";
import { MCP_STATUS_COLORS } from "@/lib/constants/config";
import { getErrorMessage, useUpdateMCPClientMutation } from "@/lib/store";
import { MCPClient } from "@/lib/types/mcp";
import { mcpClientUpdateSchema, type MCPClientUpdateSchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
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
			headers: mcpClient.config.headers,
			tools_to_execute: mcpClient.config.tools_to_execute || [],
		},
	});

	// Reset form when mcpClient changes
	useEffect(() => {
		form.reset({
			name: mcpClient.config.name,
			headers: mcpClient.config.headers,
			tools_to_execute: mcpClient.config.tools_to_execute || [],
		});
	}, [form, mcpClient]);

	const onSubmit = async (data: MCPClientUpdateSchema) => {
		try {
			await updateMCPClient({
				id: mcpClient.config.id,
				data: {
					name: data.name,
					headers: data.headers,
					tools_to_execute: data.tools_to_execute,
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
	};

	return (
		<Sheet open onOpenChange={onClose}>
			<SheetContent className="dark:bg-card flex w-full flex-col overflow-x-hidden bg-white p-8 sm:max-w-2xl">
				<Form {...form}>
					<form onSubmit={form.handleSubmit(onSubmit)} className="flex h-full flex-col gap-6">
						<SheetHeader className="p-0">
							<div className="flex items-center justify-between w-full">
								<div className="space-y-2">
									<SheetTitle className="flex w-fit items-center gap-2 font-medium">
										{mcpClient.config.name}
										<Badge className={MCP_STATUS_COLORS[mcpClient.state]}>{mcpClient.state}</Badge>
									</SheetTitle>
									<SheetDescription>MCP client configuration and available tools</SheetDescription>
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

						<div className="space-y-6">
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
												const isAllEnabled = currentTools.includes("*");
												const isNoneEnabled = currentTools.length === 0;
												const isSomeEnabled = currentTools.length > 0 && !isAllEnabled;

												return (
													<FormItem>
														<FormControl>
															<div className="flex items-center gap-2">
																<span className="text-muted-foreground text-sm">
																	{isAllEnabled ? "All enabled" : isNoneEnabled ? "All disabled" : `${currentTools.length} enabled`}
																</span>
																<Switch
																	size="md"
																	checked={isAllEnabled || isSomeEnabled}
																	onCheckedChange={(checked) => {
																		if (checked) {
																			// Enable all tools (wildcard)
																			form.setValue("tools_to_execute", ["*"], { shouldDirty: true });
																		} else {
																			// Disable all tools (empty array)
																			form.setValue("tools_to_execute", [], { shouldDirty: true });
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

											// If tools_to_execute contains "*", all tools are enabled
											const isToolEnabled = currentTools?.includes("*") || currentTools?.includes(tool.name);

											return (
												<div key={index} className="rounded-sm border">
													{/* Tool Header */}
													<div className="bg-muted/50 text-muted-foreground border-b px-6 py-3">
														<div className="flex items-center justify-between gap-4">
															<div>
																<span className="text-sm font-medium">{tool.name}</span>
																{tool.description && <p className="text-muted-foreground mt-1 text-xs">{tool.description}</p>}
															</div>
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
