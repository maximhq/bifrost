"use client";

import { Badge } from "@/components/ui/badge";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels, ProviderName } from "@/lib/constants/logs";
import { VirtualKey } from "@/lib/types/governance";
import { calculateUsagePercentage, formatCurrency, getUsageVariant, parseResetPeriod } from "@/lib/utils/governance";
import { formatDistanceToNow } from "date-fns";

interface VirtualKeyDetailSheetProps {
	virtualKey: VirtualKey;
	onClose: () => void;
}

export default function VirtualKeyDetailSheet({ virtualKey, onClose }: VirtualKeyDetailSheetProps) {
	const getEntityInfo = () => {
		if (virtualKey.team) {
			return { type: "Team", name: virtualKey.team.name };
		}
		if (virtualKey.customer) {
			return { type: "Customer", name: virtualKey.customer.name };
		}
		return { type: "None", name: "" };
	};

	const entityInfo = getEntityInfo();

	const isExhausted =
		// VK-level budget exhausted
		(virtualKey.budget?.current_usage && virtualKey.budget?.max_limit && virtualKey.budget.current_usage >= virtualKey.budget.max_limit) ||
		// VK-level rate limits exhausted
		(virtualKey.rate_limit?.token_current_usage &&
			virtualKey.rate_limit?.token_max_limit &&
			virtualKey.rate_limit.token_current_usage >= virtualKey.rate_limit.token_max_limit) ||
		(virtualKey.rate_limit?.request_current_usage &&
			virtualKey.rate_limit?.request_max_limit &&
			virtualKey.rate_limit.request_current_usage >= virtualKey.rate_limit.request_max_limit);

	return (
		<Sheet open onOpenChange={onClose}>
			<SheetContent className="dark:bg-card flex w-full flex-col overflow-x-hidden bg-white p-4 sm:max-w-2xl">
				<SheetHeader className="p-0 flex flex-col items-start">
					<SheetTitle>{virtualKey.name}</SheetTitle>
					<SheetDescription>{virtualKey.description || "Virtual key details and usage information"}</SheetDescription>
				</SheetHeader>

				<div className="space-y-6 px-4">
					{/* Basic Information */}
					<div className="space-y-4">
						<h3 className="font-semibold">Basic Information</h3>

						<div className="grid gap-4">
							<div className="grid grid-cols-3 items-center gap-4">
								<span className="text-muted-foreground text-sm">Status</span>
								<div className="col-span-2">
									<Badge variant={virtualKey.is_active ? (isExhausted ? "destructive" : "default") : "secondary"}>
										{virtualKey.is_active ? (isExhausted ? "Exhausted" : "Active") : "Inactive"}
									</Badge>
								</div>
							</div>

							<div className="grid grid-cols-3 items-center gap-4">
								<span className="text-muted-foreground text-sm">Created</span>
								<div className="col-span-2 text-sm">{formatDistanceToNow(new Date(virtualKey.created_at), { addSuffix: true })}</div>
							</div>

							<div className="grid grid-cols-3 items-center gap-4">
								<span className="text-muted-foreground text-sm">Last Updated</span>
								<div className="col-span-2 text-sm">{formatDistanceToNow(new Date(virtualKey.updated_at), { addSuffix: true })}</div>
							</div>

							{entityInfo.type !== "None" && (
								<div className="grid grid-cols-3 items-center gap-4">
									<span className="text-muted-foreground text-sm">Assigned To</span>
									<div className="col-span-2 flex items-center gap-2">
										<Badge variant={entityInfo.type === "None" ? "outline" : "secondary"}>{entityInfo.type}</Badge>
										<span className="text-sm">{entityInfo.name}</span>
									</div>
								</div>
							)}
						</div>
					</div>

					<DottedSeparator />
					
					{/* Provider Configurations */}
					<div className="space-y-4">
						<h3 className="font-semibold">Provider Configurations</h3>

						<div className="space-y-3">
							{!virtualKey.provider_configs || virtualKey.provider_configs.length === 0 ? (
								<span className="text-muted-foreground text-sm">All providers allowed with default settings</span>
							) : (
								<div className="space-y-4">
									{virtualKey.provider_configs.map((config, index) => (
										<div key={`${config.provider}-${index}`} className="rounded-lg border p-4">
											{/* Provider Header */}
											<div className="mb-4 flex items-center justify-between">
												<div className="flex items-center gap-2">
													<RenderProviderIcon provider={config.provider as ProviderIconType} size="sm" className="h-5 w-5" />
													<span className="font-medium">{ProviderLabels[config.provider as ProviderName] || config.provider}</span>
												</div>
												<Badge variant="outline" className="font-mono text-xs">
													Weight: {config.weight}
												</Badge>
											</div>

											{/* Basic Config */}
											<div className="space-y-3">
												<div className="grid grid-cols-3 items-start gap-4">
													<span className="text-muted-foreground text-sm pt-0.5 font-medium">Allowed Models</span>
													<div className="col-span-2">
														{config.allowed_models && config.allowed_models.length > 0 ? (
															<div className="flex flex-wrap gap-1">
																{config.allowed_models.map((model) => (
																	<Badge key={model} variant="secondary" className="text-xs">
																		{model}
																	</Badge>
																))}
															</div>
														) : (
															<span className="text-muted-foreground text-sm">All models allowed</span>
														)}
													</div>
												</div>

												<div className="grid grid-cols-3 items-start gap-4">
													<span className="text-muted-foreground text-sm pt-0.5 font-medium">Allowed Keys</span>
													<div className="col-span-2">
														{config.keys && config.keys.length > 0 ? (
															<div className="flex flex-wrap gap-1">
																{config.keys.map((key) => (
																	<Badge key={key.key_id} variant="outline" className="text-xs">
																		{key.name}
																	</Badge>
																))}
															</div>
														) : (
															<span className="text-muted-foreground text-sm">All keys allowed</span>
														)}
													</div>
												</div>

												{/* Provider Budget */}
												{config.budget && (
													<>
														<DottedSeparator />
														<div className="space-y-2">
															<h4 className="text-sm font-medium">Provider Budget</h4>
															<div className="grid grid-cols-3 items-center gap-4">
																<span className="text-muted-foreground text-sm">Usage</span>
																<div className="col-span-2">
																	<div className="flex items-center gap-2">
																		<span className="font-mono text-sm">
																			{formatCurrency(config.budget.current_usage)} / {formatCurrency(config.budget.max_limit)}
																		</span>
																		<Badge
																			variant={config.budget.current_usage >= config.budget.max_limit ? "destructive" : "default"}
																			className="text-xs"
																		>
																			{Math.round((config.budget.current_usage / config.budget.max_limit) * 100)}%
																		</Badge>
																	</div>
																</div>
															</div>
															<div className="grid grid-cols-3 items-center gap-4">
																<span className="text-muted-foreground text-sm">Reset Period</span>
																<div className="col-span-2 text-sm">{parseResetPeriod(config.budget.reset_duration)}</div>
															</div>
															<div className="grid grid-cols-3 items-center gap-4">
																<span className="text-muted-foreground text-sm">Last Reset</span>
																<div className="col-span-2 text-sm">
																	{formatDistanceToNow(new Date(config.budget.last_reset), { addSuffix: true })}
																</div>
															</div>
														</div>
													</>
												)}

												{/* Provider Rate Limits */}
												{config.rate_limit && (
													<>
														<DottedSeparator />
														<div className="space-y-3">
															<h4 className="text-sm font-medium">Provider Rate Limits</h4>

															{/* Token Limits */}
															{config.rate_limit.token_max_limit && (
																<div className="space-y-2">
																	<span className="text-muted-foreground text-xs font-medium">TOKEN LIMITS</span>
																	<div className="grid grid-cols-3 items-center gap-4">
																		<span className="text-muted-foreground text-sm">Usage</span>
																		<div className="col-span-2">
																			<div className="flex items-center gap-2">
																				<span className="font-mono text-sm">
																					{config.rate_limit.token_current_usage} / {config.rate_limit.token_max_limit}
																				</span>
																				<Badge
																					variant={getUsageVariant(
																						calculateUsagePercentage(
																							config.rate_limit.token_current_usage,
																							config.rate_limit.token_max_limit,
																						),
																					)}
																					className="text-xs"
																				>
																					{calculateUsagePercentage(
																						config.rate_limit.token_current_usage,
																						config.rate_limit.token_max_limit,
																					)}
																					%
																				</Badge>
																			</div>
																		</div>
																	</div>
																	<div className="grid grid-cols-3 items-center gap-4">
																		<span className="text-muted-foreground text-sm">Reset Period</span>
																		<div className="col-span-2 text-sm">
																			{parseResetPeriod(config.rate_limit.token_reset_duration || "")}
																		</div>
																	</div>
																	<div className="grid grid-cols-3 items-center gap-4">
																		<span className="text-muted-foreground text-sm">Last Reset</span>
																		<div className="col-span-2 text-sm">
																			{formatDistanceToNow(new Date(config.rate_limit.token_last_reset), { addSuffix: true })}
																		</div>
																	</div>
																</div>
															)}

															{/* Request Limits */}
															{config.rate_limit.request_max_limit && (
																<div className="space-y-2">
																	<span className="text-muted-foreground text-xs font-medium">REQUEST LIMITS</span>
																	<div className="grid grid-cols-3 items-center gap-4">
																		<span className="text-muted-foreground text-sm">Usage</span>
																		<div className="col-span-2">
																			<div className="flex items-center gap-2">
																				<span className="font-mono text-sm">
																					{config.rate_limit.request_current_usage} / {config.rate_limit.request_max_limit}
																				</span>
																				<Badge
																					variant={getUsageVariant(
																						calculateUsagePercentage(
																							config.rate_limit.request_current_usage,
																							config.rate_limit.request_max_limit,
																						),
																					)}
																					className="text-xs"
																				>
																					{calculateUsagePercentage(
																						config.rate_limit.request_current_usage,
																						config.rate_limit.request_max_limit,
																					)}
																					%
																				</Badge>
																			</div>
																		</div>
																	</div>
																	<div className="grid grid-cols-3 items-center gap-4">
																		<span className="text-muted-foreground text-sm">Reset Period</span>
																		<div className="col-span-2 text-sm">
																			{parseResetPeriod(config.rate_limit.request_reset_duration || "")}
																		</div>
																	</div>
																	<div className="grid grid-cols-3 items-center gap-4">
																		<span className="text-muted-foreground text-sm">Last Reset</span>
																		<div className="col-span-2 text-sm">
																			{formatDistanceToNow(new Date(config.rate_limit.request_last_reset), { addSuffix: true })}
																		</div>
																	</div>
																</div>
															)}

															{!config.rate_limit.token_max_limit && !config.rate_limit.request_max_limit && (
																<p className="text-muted-foreground text-sm">No rate limits configured for this provider</p>
															)}
														</div>
													</>
												)}
											</div>
										</div>
									))}
								</div>
							)}
						</div>
					</div>

					{/* MCP Client Configurations */}
					<div className="space-y-4">
						<h3 className="font-semibold">MCP Client Configurations</h3>

						<div className="space-y-3">
							{!virtualKey.mcp_configs || virtualKey.mcp_configs.length === 0 ? (
								<span className="text-muted-foreground text-sm">All MCP clients allowed with default settings</span>
							) : (
								<div className="rounded-md border">
									<Table>
										<TableHeader>
											<TableRow>
												<TableHead>MCP Client</TableHead>
												<TableHead>Allowed Tools</TableHead>
											</TableRow>
										</TableHeader>
										<TableBody>
											{virtualKey.mcp_configs.map((config, index) => (
												<TableRow key={`${config.mcp_client?.name || config.id}-${index}`}>
													<TableCell>{config.mcp_client?.name || "Unknown Client"}</TableCell>
													<TableCell>
														{config.tools_to_execute?.includes("*") ? (
															<span className="text-muted-foreground text-sm">All tools allowed</span>
														) : config.tools_to_execute && config.tools_to_execute.length > 0 ? (
															<div className="flex flex-wrap gap-1">
																{config.tools_to_execute.map((tool) => (
																	<Badge key={tool} variant="secondary" className="text-xs">
																		{tool}
																	</Badge>
																))}
															</div>
														) : (
															<span className="text-muted-foreground text-sm">No tools selected</span>
														)}
													</TableCell>
												</TableRow>
											))}
										</TableBody>
									</Table>
								</div>
							)}
						</div>
					</div>

					<DottedSeparator />

					{/* Budget Information */}
					<div className="space-y-4">
						<h3 className="font-semibold">Budget Information</h3>

						{virtualKey.budget ? (
							<div className="space-y-3">
								<div className="grid grid-cols-3 items-center gap-4">
									<span className="text-muted-foreground text-sm">Usage</span>
									<div className="col-span-2">
										<div className="flex items-center gap-2">
											<span className="font-mono text-sm">
												{formatCurrency(virtualKey.budget.current_usage)} / {formatCurrency(virtualKey.budget.max_limit)}
											</span>
											<Badge
												variant={virtualKey.budget.current_usage >= virtualKey.budget.max_limit ? "destructive" : "default"}
												className="text-xs"
											>
												{Math.round((virtualKey.budget.current_usage / virtualKey.budget.max_limit) * 100)}%
											</Badge>
										</div>
									</div>
								</div>

								<div className="grid grid-cols-3 items-center gap-4">
									<span className="text-muted-foreground text-sm">Reset Period</span>
									<div className="col-span-2 text-sm">{parseResetPeriod(virtualKey.budget.reset_duration)}</div>
								</div>

								<div className="grid grid-cols-3 items-center gap-4">
									<span className="text-muted-foreground text-sm">Last Reset</span>
									<div className="col-span-2 text-sm">
										{formatDistanceToNow(new Date(virtualKey.budget.last_reset), { addSuffix: true })}
									</div>
								</div>
							</div>
						) : (
							<p className="text-muted-foreground text-sm">No budget limits configured</p>
						)}
					</div>

					{/* Rate Limits */}
					<div className="space-y-4">
						<h3 className="font-semibold">Rate Limits</h3>

						{virtualKey.rate_limit ? (
							<div className="space-y-4">
								{/* Token Limits */}
								{virtualKey.rate_limit.token_max_limit && (
									<div className="rounded-lg border p-4">
										<div className="mb-3">
											<span className="font-medium">Token Limits</span>
										</div>

										<div className="space-y-2">
											<div className="grid grid-cols-3 items-center gap-4">
												<span className="text-muted-foreground text-sm">Usage</span>
												<div className="col-span-2">
													<div className="flex items-center gap-2">
														<span className="font-mono text-sm">
															{virtualKey.rate_limit.token_current_usage} / {virtualKey.rate_limit.token_max_limit}
														</span>
														<Badge
															variant={getUsageVariant(
																calculateUsagePercentage(virtualKey.rate_limit.token_current_usage, virtualKey.rate_limit.token_max_limit),
															)}
															className="text-xs"
														>
															{calculateUsagePercentage(virtualKey.rate_limit.token_current_usage, virtualKey.rate_limit.token_max_limit)}%
														</Badge>
													</div>
												</div>
											</div>

											<div className="grid grid-cols-3 items-center gap-4">
												<span className="text-muted-foreground text-sm">Reset Period</span>
												<div className="col-span-2 text-sm">{parseResetPeriod(virtualKey.rate_limit.token_reset_duration || "")}</div>
											</div>

											<div className="grid grid-cols-3 items-center gap-4">
												<span className="text-muted-foreground text-sm">Last Reset</span>
												<div className="col-span-2 text-sm">
													{formatDistanceToNow(new Date(virtualKey.rate_limit.token_last_reset), { addSuffix: true })}
												</div>
											</div>
										</div>
									</div>
								)}

								{/* Request Limits */}
								{virtualKey.rate_limit.request_max_limit && (
									<div className="rounded-lg border p-4">
										<div className="mb-3">
											<span className="font-medium">Request Limits</span>
										</div>

										<div className="space-y-2">
											<div className="grid grid-cols-3 items-center gap-4">
												<span className="text-muted-foreground text-sm">Usage</span>
												<div className="col-span-2">
													<div className="flex items-center gap-2">
														<span className="font-mono text-sm">
															{virtualKey.rate_limit.request_current_usage} / {virtualKey.rate_limit.request_max_limit}
														</span>
														<Badge
															variant={getUsageVariant(
																calculateUsagePercentage(
																	virtualKey.rate_limit.request_current_usage,
																	virtualKey.rate_limit.request_max_limit,
																),
															)}
															className="text-xs"
														>
															{calculateUsagePercentage(
																virtualKey.rate_limit.request_current_usage,
																virtualKey.rate_limit.request_max_limit,
															)}
															%
														</Badge>
													</div>
												</div>
											</div>

											<div className="grid grid-cols-3 items-center gap-4">
												<span className="text-muted-foreground text-sm">Reset Period</span>
												<div className="col-span-2 text-sm">{parseResetPeriod(virtualKey.rate_limit.request_reset_duration || "")}</div>
											</div>

											<div className="grid grid-cols-3 items-center gap-4">
												<span className="text-muted-foreground text-sm">Last Reset</span>
												<div className="col-span-2 text-sm">
													{formatDistanceToNow(new Date(virtualKey.rate_limit.request_last_reset), { addSuffix: true })}
												</div>
											</div>
										</div>
									</div>
								)}

								{!virtualKey.rate_limit.token_max_limit && !virtualKey.rate_limit.request_max_limit && (
									<p className="text-muted-foreground text-sm">No rate limits configured</p>
								)}
							</div>
						) : (
							<p className="text-muted-foreground text-sm">No rate limits configured</p>
						)}
					</div>
				</div>
			</SheetContent>
		</Sheet>
	);
}
