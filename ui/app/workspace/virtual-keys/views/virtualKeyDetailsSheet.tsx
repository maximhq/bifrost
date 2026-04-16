import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { Progress } from "@/components/ui/progress";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels, ProviderName } from "@/lib/constants/logs";
import { VirtualKey } from "@/lib/types/governance";
import { cn } from "@/lib/utils";
import { calculateUsagePercentage, formatCurrency, parseResetPeriod } from "@/lib/utils/governance";
import { formatDistanceToNow } from "date-fns";
import { Lock, Users } from "lucide-react";
import { useVirtualKeyUsage } from "../hooks/useVirtualKeyUsage";

function usageBarClass(pct: number, exhausted: boolean) {
	if (exhausted) return "[&>div]:bg-red-500/70";
	if (pct > 80) return "[&>div]:bg-amber-500/70";
	return "[&>div]:bg-emerald-500/70";
}

function UsageLine({ current, max, format }: { current: number; max: number; format: (n: number) => string }) {
	const pct = calculateUsagePercentage(current, max);
	const exhausted = max > 0 && current >= max;
	return (
		<div className="space-y-2">
			<div className="flex items-center justify-between gap-3">
				<span className="font-mono text-sm">
					{format(current)} <span className="text-muted-foreground">/</span> {format(max)}
				</span>
				<span className={cn("text-xs font-medium tabular-nums", exhausted ? "text-red-500" : pct > 80 ? "text-amber-500" : "text-muted-foreground")}>
					{pct}%
				</span>
			</div>
			<Progress value={Math.min(pct, 100)} className={cn("bg-muted/70 dark:bg-muted/30 h-1.5", usageBarClass(pct, exhausted))} />
		</div>
	);
}

interface VirtualKeyDetailSheetProps {
	virtualKey: VirtualKey;
	onClose: () => void;
}

export default function VirtualKeyDetailSheet({ virtualKey, onClose }: VirtualKeyDetailSheetProps) {
	const { assignedUsers, isManagedByProfile, managingProfile, hasApRateLimit, displayBudgets, displayRateLimit } =
		useVirtualKeyUsage(virtualKey);

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
		// Budget exhausted (AP-mirrored when managed, VK-own otherwise)
		displayBudgets?.some((b) => b.current_usage >= b.max_limit) ||
		// Rate limits exhausted
		(displayRateLimit?.token_current_usage &&
			displayRateLimit?.token_max_limit &&
			displayRateLimit.token_current_usage >= displayRateLimit.token_max_limit) ||
		(displayRateLimit?.request_current_usage &&
			displayRateLimit?.request_max_limit &&
			displayRateLimit.request_current_usage >= displayRateLimit.request_max_limit);

	return (
		<Sheet open onOpenChange={onClose}>
			<SheetContent className="flex w-full flex-col overflow-x-hidden p-8 sm:max-w-2xl">
				<SheetHeader className="flex flex-col items-start p-0">
					<SheetTitle>{virtualKey.name}</SheetTitle>
					<SheetDescription>{virtualKey.description || "Virtual key details and usage information"}</SheetDescription>
				</SheetHeader>

				<div className="space-y-6">
					{isManagedByProfile ? (
						<Alert variant="info">
							<Lock className="h-4 w-4" />
							<AlertDescription>
								This virtual key is managed by an access profile. You can rename it or update its description from the edit button, but
								providers, budgets, rate limits, and MCP access are controlled by the profile and must be changed there.
							</AlertDescription>
						</Alert>
					) : null}

					{assignedUsers.length > 0 ? (
						<div className="space-y-1">
							<Label className="text-sm font-medium">Assigned Users</Label>
							<div className="flex items-center gap-2">
								<Users className="text-muted-foreground h-4 w-4" />
								<span className="text-sm">{assignedUsers.map((u) => u.name || u.email).join(", ")}</span>
							</div>
						</div>
					) : null}

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
								<span className="text-muted-foreground text-sm">No providers configured (deny-by-default)</span>
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
													<span className="text-muted-foreground pt-0.5 text-sm font-medium">Allowed Models</span>
													<div className="col-span-2">
														{config.allowed_models?.includes("*") ? (
															<Badge variant="success" className="text-xs">
																All Models
															</Badge>
														) : config.allowed_models && config.allowed_models.length > 0 ? (
															<div className="flex flex-wrap gap-1">
																{config.allowed_models.map((model) => (
																	<Badge key={model} variant="secondary" className="text-xs">
																		{model}
																	</Badge>
																))}
															</div>
														) : (
															<Badge variant="destructive" className="text-xs">
																No models (deny all)
															</Badge>
														)}
													</div>
												</div>

												<div className="grid grid-cols-3 items-start gap-4">
													<span className="text-muted-foreground pt-0.5 text-sm font-medium">Allowed Keys</span>
													<div className="col-span-2">
														{config.allow_all_keys ? (
															<Badge variant="success" className="text-xs">
																All Keys
															</Badge>
														) : config.keys && config.keys.length > 0 ? (
															<div className="flex flex-wrap gap-1">
																{config.keys.map((key) => (
																	<Badge key={key.key_id} variant="outline" className="text-xs">
																		{key.name}
																	</Badge>
																))}
															</div>
														) : (
															<Badge variant="destructive" className="text-xs">
																No keys (deny all)
															</Badge>
														)}
													</div>
												</div>

												{/* Provider Budgets */}
												{config.budgets && config.budgets.length > 0 && (
													<>
														<DottedSeparator />
														<div className="space-y-2">
															<h4 className="text-sm font-medium">Provider Budgets</h4>
															{config.budgets.map((b, bIdx) => (
																<div key={bIdx} className="space-y-2">
																	<UsageLine current={b.current_usage} max={b.max_limit} format={formatCurrency} />
																	<div className="text-muted-foreground flex items-center justify-between text-xs">
																		<span>
																			Resets {parseResetPeriod(b.reset_duration)}
																			{virtualKey.calendar_aligned && " (calendar)"}
																		</span>
																		{b.last_reset ? (
																			<span>Last reset {formatDistanceToNow(new Date(b.last_reset), { addSuffix: true })}</span>
																		) : null}
																	</div>
																</div>
															))}
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
															{config.rate_limit.token_max_limit != null ? (
																<div className="space-y-2">
																	<span className="text-muted-foreground text-xs font-medium">TOKEN LIMITS</span>
																	<UsageLine
																		current={config.rate_limit.token_current_usage}
																		max={config.rate_limit.token_max_limit}
																		format={(n) => n.toLocaleString()}
																	/>
																	<div className="text-muted-foreground flex items-center justify-between text-xs">
																		<span>Resets {parseResetPeriod(config.rate_limit.token_reset_duration || "")}</span>
																		{config.rate_limit.token_last_reset ? (
																				<span>Last reset {formatDistanceToNow(new Date(config.rate_limit.token_last_reset), { addSuffix: true })}</span>
																			) : null}
																	</div>
																</div>
															) : null}

															{/* Request Limits */}
															{config.rate_limit.request_max_limit != null ? (
																<div className="space-y-2">
																	<span className="text-muted-foreground text-xs font-medium">REQUEST LIMITS</span>
																	<UsageLine
																		current={config.rate_limit.request_current_usage}
																		max={config.rate_limit.request_max_limit}
																		format={(n) => n.toLocaleString()}
																	/>
																	<div className="text-muted-foreground flex items-center justify-between text-xs">
																		<span>Resets {parseResetPeriod(config.rate_limit.request_reset_duration || "")}</span>
																		{config.rate_limit.request_last_reset ? (
																				<span>Last reset {formatDistanceToNow(new Date(config.rate_limit.request_last_reset), { addSuffix: true })}</span>
																			) : null}
																	</div>
																</div>
															) : null}

															{config.rate_limit.token_max_limit == null && config.rate_limit.request_max_limit == null && (
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
								<span className="text-muted-foreground text-sm">No MCP clients configured (deny-by-default)</span>
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
															<Badge variant="success" className="text-xs">
																All Tools
															</Badge>
														) : config.tools_to_execute && config.tools_to_execute.length > 0 ? (
															<div className="flex flex-wrap gap-1">
																{config.tools_to_execute.map((tool) => (
																	<Badge key={tool} variant="secondary" className="text-xs">
																		{tool}
																	</Badge>
																))}
															</div>
														) : (
															<Badge variant="destructive" className="text-xs">
																No tools (deny all)
															</Badge>
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
						<h3 className="font-semibold">
							Budget Information
							{isManagedByProfile && managingProfile?.budget_lines?.length ? (
								<span className="text-muted-foreground ml-2 text-xs font-normal">(from {managingProfile.name})</span>
							) : null}
						</h3>

						{displayBudgets && displayBudgets.length > 0 ? (
							<div className="space-y-4">
								{displayBudgets.map((b, bIdx) => (
									<div key={bIdx} className="rounded-lg border p-4 space-y-2">
										<UsageLine current={b.current_usage} max={b.max_limit} format={formatCurrency} />
										<div className="text-muted-foreground flex items-center justify-between text-xs">
											<span>
												Resets {parseResetPeriod(b.reset_duration)}
												{virtualKey.calendar_aligned && " (calendar)"}
											</span>
											{b.last_reset ? (
												<span>Last reset {formatDistanceToNow(new Date(b.last_reset), { addSuffix: true })}</span>
											) : null}
										</div>
									</div>
								))}
							</div>
						) : (
							<p className="text-muted-foreground text-sm">No budget limits configured</p>
						)}
					</div>

					{/* Rate Limits */}
					<div className="space-y-4">
						<h3 className="font-semibold">
							Rate Limits
							{isManagedByProfile && hasApRateLimit ? (
								<span className="text-muted-foreground ml-2 text-xs font-normal">(from {managingProfile?.name})</span>
							) : null}
						</h3>

						{displayRateLimit ? (
							<div className="space-y-4">
								{/* Token Limits */}
								{displayRateLimit.token_max_limit != null ? (
									<div className="rounded-lg border p-4 space-y-3">
										<span className="font-medium">Token Limits</span>
										<UsageLine
											current={displayRateLimit.token_current_usage}
											max={displayRateLimit.token_max_limit}
											format={(n) => n.toLocaleString()}
										/>
										<div className="text-muted-foreground flex items-center justify-between text-xs">
											<span>Resets {parseResetPeriod(displayRateLimit.token_reset_duration || "")}</span>
											{displayRateLimit.token_last_reset ? (
												<span>Last reset {formatDistanceToNow(new Date(displayRateLimit.token_last_reset), { addSuffix: true })}</span>
											) : null}
										</div>
									</div>
								) : null}

								{/* Request Limits */}
								{displayRateLimit.request_max_limit != null ? (
									<div className="rounded-lg border p-4 space-y-3">
										<span className="font-medium">Request Limits</span>
										<UsageLine
											current={displayRateLimit.request_current_usage}
											max={displayRateLimit.request_max_limit}
											format={(n) => n.toLocaleString()}
										/>
										<div className="text-muted-foreground flex items-center justify-between text-xs">
											<span>Resets {parseResetPeriod(displayRateLimit.request_reset_duration || "")}</span>
											{displayRateLimit.request_last_reset ? (
												<span>Last reset {formatDistanceToNow(new Date(displayRateLimit.request_last_reset), { addSuffix: true })}</span>
											) : null}
										</div>
									</div>
								) : null}

								{displayRateLimit.token_max_limit == null && displayRateLimit.request_max_limit == null && (
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