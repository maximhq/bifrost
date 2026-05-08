import i18n from "@/lib/i18n";
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
				<span
					className={cn(
						"text-xs font-medium tabular-nums",
						exhausted ? "text-red-500" : pct > 80 ? "text-amber-500" : "text-muted-foreground",
					)}
				>
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
					<SheetDescription>{virtualKey.description || i18n.t("workspace.virtualKeys.noDescription")}</SheetDescription>
				</SheetHeader>

				<div className="space-y-6">
					{isManagedByProfile ? (
						<Alert variant="info">
							<Lock className="h-4 w-4" />
							<AlertDescription>{i18n.t("workspace.virtualKeys.vkManagedByProfileDescription")}</AlertDescription>
						</Alert>
					) : null}

					{assignedUsers.length > 0 ? (
						<div className="space-y-1">
							<Label className="text-sm font-medium">{i18n.t("workspace.virtualKeys.assignedUsers")}</Label>
							<div className="flex items-center gap-2">
								<Users className="text-muted-foreground h-4 w-4" />
								<span className="text-sm">{assignedUsers.map((u) => u.name || u.email).join(", ")}</span>
							</div>
						</div>
					) : null}

					{/* Basic Information */}
					<div className="space-y-4">
						<h3 className="font-semibold">{i18n.t("workspace.virtualKeys.basicInformation")}</h3>

						<div className="grid gap-4">
							<div className="grid grid-cols-3 items-center gap-4">
								<span className="text-muted-foreground text-sm">{i18n.t("workspace.virtualKeys.status")}</span>
								<div className="col-span-2">
									<Badge variant={virtualKey.is_active ? (isExhausted ? "destructive" : "default") : "secondary"}>
										{virtualKey.is_active
											? isExhausted
												? i18n.t("workspace.virtualKeys.exhausted")
												: i18n.t("workspace.virtualKeys.active")
											: i18n.t("workspace.virtualKeys.inactive")}
									</Badge>
								</div>
							</div>

							<div className="grid grid-cols-3 items-center gap-4">
								<span className="text-muted-foreground text-sm">{i18n.t("workspace.virtualKeys.createdAt")}</span>
								<div className="col-span-2 text-sm">{formatDistanceToNow(new Date(virtualKey.created_at), { addSuffix: true })}</div>
							</div>

							<div className="grid grid-cols-3 items-center gap-4">
								<span className="text-muted-foreground text-sm">{i18n.t("workspace.virtualKeys.lastUpdatedAt")}</span>
								<div className="col-span-2 text-sm">{formatDistanceToNow(new Date(virtualKey.updated_at), { addSuffix: true })}</div>
							</div>

							{entityInfo.type !== "None" && (
								<div className="grid grid-cols-3 items-center gap-4">
									<span className="text-muted-foreground text-sm">{i18n.t("workspace.virtualKeys.assignedTo")}</span>
									<div className="col-span-2 flex items-center gap-2">
										<Badge variant={entityInfo.type === "None" ? "outline" : "secondary"}>
											{entityInfo.type === "Team" ? i18n.t("workspace.virtualKeys.team") : i18n.t("workspace.virtualKeys.customer")}
										</Badge>
										<span className="text-sm">{entityInfo.name}</span>
									</div>
								</div>
							)}
						</div>
					</div>

					<DottedSeparator />

					{/* Provider Configurations */}
					<div className="space-y-4">
						<h3 className="font-semibold">{i18n.t("workspace.virtualKeys.providerConfigurations")}</h3>

						<div className="space-y-3">
							{!virtualKey.provider_configs || virtualKey.provider_configs.length === 0 ? (
								<span className="text-muted-foreground text-sm">{i18n.t("workspace.virtualKeys.noProvidersConfigured")}</span>
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
													{i18n.t("workspace.virtualKeys.weight")}: {config.weight}
												</Badge>
											</div>

											{/* Basic Config */}
											<div className="space-y-3">
												<div className="grid grid-cols-3 items-start gap-4">
													<span className="text-muted-foreground pt-0.5 text-sm font-medium">
														{i18n.t("workspace.virtualKeys.allowedModels")}
													</span>
													<div className="col-span-2">
														{config.allowed_models?.includes("*") ? (
															<Badge variant="success" className="text-xs">
																{i18n.t("workspace.virtualKeys.allModels")}
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
																{i18n.t("workspace.virtualKeys.noModelsDenyAll")}
															</Badge>
														)}
													</div>
												</div>

												<div className="grid grid-cols-3 items-start gap-4">
													<span className="text-muted-foreground pt-0.5 text-sm font-medium">
														{i18n.t("workspace.virtualKeys.allowedKeys")}
													</span>
													<div className="col-span-2">
														{config.allow_all_keys ? (
															<Badge variant="success" className="text-xs">
																{i18n.t("workspace.virtualKeys.allKeys")}
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
																{i18n.t("workspace.virtualKeys.noKeysDenyAll")}
															</Badge>
														)}
													</div>
												</div>

												{/* Provider Budgets */}
												{config.budgets && config.budgets.length > 0 && (
													<>
														<DottedSeparator />
														<div className="space-y-2">
															<h4 className="text-sm font-medium">{i18n.t("workspace.virtualKeys.providerBudgets")}</h4>
															{config.budgets.map((b, bIdx) => (
																<div key={bIdx} className="space-y-2">
																	<UsageLine current={b.current_usage} max={b.max_limit} format={formatCurrency} />
																	<div className="text-muted-foreground flex items-center justify-between text-xs">
																		<span>
																			{i18n.t("workspace.virtualKeys.resetsDuration", { duration: parseResetPeriod(b.reset_duration) })}
																			{virtualKey.calendar_aligned && i18n.t("workspace.virtualKeys.calendarAligned")}
																		</span>
																		{b.last_reset ? (
																			<span>
																				{i18n.t("workspace.virtualKeys.lastReset")}{" "}
																				{formatDistanceToNow(new Date(b.last_reset), { addSuffix: true })}
																			</span>
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
															<h4 className="text-sm font-medium">{i18n.t("workspace.virtualKeys.providerRateLimits")}</h4>

															{/* Token Limits */}
															{config.rate_limit.token_max_limit != null ? (
																<div className="space-y-2">
																	<span className="text-muted-foreground text-xs font-medium">
																		{i18n.t("workspace.virtualKeys.tokenLimits").toUpperCase()}
																	</span>
																	<UsageLine
																		current={config.rate_limit.token_current_usage}
																		max={config.rate_limit.token_max_limit}
																		format={(n) => n.toLocaleString()}
																	/>
																	<div className="text-muted-foreground flex items-center justify-between text-xs">
																		<span>
																			{i18n.t("workspace.virtualKeys.resetsDuration", {
																				duration: parseResetPeriod(config.rate_limit.token_reset_duration || ""),
																			})}
																		</span>
																		{config.rate_limit.token_last_reset ? (
																			<span>
																				{i18n.t("workspace.virtualKeys.lastReset")}{" "}
																				{formatDistanceToNow(new Date(config.rate_limit.token_last_reset), { addSuffix: true })}
																			</span>
																		) : null}
																	</div>
																</div>
															) : null}

															{/* Request Limits */}
															{config.rate_limit.request_max_limit != null ? (
																<div className="space-y-2">
																	<span className="text-muted-foreground text-xs font-medium">
																		{i18n.t("workspace.virtualKeys.requestLimits").toUpperCase()}
																	</span>
																	<UsageLine
																		current={config.rate_limit.request_current_usage}
																		max={config.rate_limit.request_max_limit}
																		format={(n) => n.toLocaleString()}
																	/>
																	<div className="text-muted-foreground flex items-center justify-between text-xs">
																		<span>
																			{i18n.t("workspace.virtualKeys.resetsDuration", {
																				duration: parseResetPeriod(config.rate_limit.request_reset_duration || ""),
																			})}
																		</span>
																		{config.rate_limit.request_last_reset ? (
																			<span>
																				{i18n.t("workspace.virtualKeys.lastReset")}{" "}
																				{formatDistanceToNow(new Date(config.rate_limit.request_last_reset), { addSuffix: true })}
																			</span>
																		) : null}
																	</div>
																</div>
															) : null}

															{config.rate_limit.token_max_limit == null && config.rate_limit.request_max_limit == null && (
																<p className="text-muted-foreground text-sm">{i18n.t("workspace.virtualKeys.noRateLimitsConfigured")}</p>
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
						<h3 className="font-semibold">{i18n.t("workspace.virtualKeys.mcpClientConfigurations")}</h3>

						<div className="space-y-3">
							{!virtualKey.mcp_configs || virtualKey.mcp_configs.length === 0 ? (
								<span className="text-muted-foreground text-sm">{i18n.t("workspace.virtualKeys.noMcpClientsConfigured")}</span>
							) : (
								<div className="rounded-md border">
									<Table>
										<TableHeader>
											<TableRow>
												<TableHead>{i18n.t("workspace.virtualKeys.mcpClient")}</TableHead>
												<TableHead>{i18n.t("workspace.virtualKeys.allowedTools")}</TableHead>
											</TableRow>
										</TableHeader>
										<TableBody>
											{virtualKey.mcp_configs.map((config, index) => (
												<TableRow key={`${config.mcp_client?.name || config.id}-${index}`}>
													<TableCell>{config.mcp_client?.name || "Unknown Client"}</TableCell>
													<TableCell>
														{config.tools_to_execute?.includes("*") ? (
															<Badge variant="success" className="text-xs">
																{i18n.t("workspace.virtualKeys.allTools")}
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
																{i18n.t("workspace.virtualKeys.noToolsDenyAll")}
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
							{i18n.t("workspace.virtualKeys.budgetInformation")}
							{isManagedByProfile && managingProfile?.budgets?.length ? (
								<span className="text-muted-foreground ml-2 text-xs font-normal">
									{i18n.t("workspace.virtualKeys.fromAccessProfile", { name: managingProfile.name })}
								</span>
							) : null}
						</h3>

						{displayBudgets && displayBudgets.length > 0 ? (
							<div className="space-y-4">
								{displayBudgets.map((b, bIdx) => (
									<div key={bIdx} className="space-y-2 rounded-lg border p-4">
										<UsageLine current={b.current_usage} max={b.max_limit} format={formatCurrency} />
										<div className="text-muted-foreground flex items-center justify-between text-xs">
											<span>
												{i18n.t("workspace.virtualKeys.resetsDuration", { duration: parseResetPeriod(b.reset_duration) })}
												{virtualKey.calendar_aligned && i18n.t("workspace.virtualKeys.calendarAligned")}
											</span>
											{b.last_reset ? (
												<span>
													{i18n.t("workspace.virtualKeys.lastReset")} {formatDistanceToNow(new Date(b.last_reset), { addSuffix: true })}
												</span>
											) : null}
										</div>
									</div>
								))}
							</div>
						) : (
							<p className="text-muted-foreground text-sm">{i18n.t("workspace.virtualKeys.noBudgetLimitsConfigured")}</p>
						)}
					</div>

					{/* Rate Limits */}
					<div className="space-y-4">
						<h3 className="font-semibold">
							{i18n.t("workspace.virtualKeys.rateLimitsSection")}
							{isManagedByProfile && hasApRateLimit ? (
								<span className="text-muted-foreground ml-2 text-xs font-normal">
									{i18n.t("workspace.virtualKeys.fromAccessProfile", { name: managingProfile?.name })}
								</span>
							) : null}
						</h3>

						{displayRateLimit ? (
							<div className="space-y-4">
								{/* Token Limits */}
								{displayRateLimit.token_max_limit != null ? (
									<div className="space-y-3 rounded-lg border p-4">
										<span className="font-medium">{i18n.t("workspace.virtualKeys.tokenLimits")}</span>
										<UsageLine
											current={displayRateLimit.token_current_usage}
											max={displayRateLimit.token_max_limit}
											format={(n) => n.toLocaleString()}
										/>
										<div className="text-muted-foreground flex items-center justify-between text-xs">
											<span>
												{i18n.t("workspace.virtualKeys.resetsDuration", {
													duration: parseResetPeriod(displayRateLimit.token_reset_duration || ""),
												})}
											</span>
											{displayRateLimit.token_last_reset ? (
												<span>
													{i18n.t("workspace.virtualKeys.lastReset")}{" "}
													{formatDistanceToNow(new Date(displayRateLimit.token_last_reset), { addSuffix: true })}
												</span>
											) : null}
										</div>
									</div>
								) : null}

								{/* Request Limits */}
								{displayRateLimit.request_max_limit != null ? (
									<div className="space-y-3 rounded-lg border p-4">
										<span className="font-medium">{i18n.t("workspace.virtualKeys.requestLimits")}</span>
										<UsageLine
											current={displayRateLimit.request_current_usage}
											max={displayRateLimit.request_max_limit}
											format={(n) => n.toLocaleString()}
										/>
										<div className="text-muted-foreground flex items-center justify-between text-xs">
											<span>
												{i18n.t("workspace.virtualKeys.resetsDuration", {
													duration: parseResetPeriod(displayRateLimit.request_reset_duration || ""),
												})}
											</span>
											{displayRateLimit.request_last_reset ? (
												<span>
													{i18n.t("workspace.virtualKeys.lastReset")}{" "}
													{formatDistanceToNow(new Date(displayRateLimit.request_last_reset), { addSuffix: true })}
												</span>
											) : null}
										</div>
									</div>
								) : null}

								{displayRateLimit.token_max_limit == null && displayRateLimit.request_max_limit == null && (
									<p className="text-muted-foreground text-sm">{i18n.t("workspace.virtualKeys.noRateLimitsConfigured")}</p>
								)}
							</div>
						) : (
							<p className="text-muted-foreground text-sm">{i18n.t("workspace.virtualKeys.noRateLimitsConfigured")}</p>
						)}
					</div>
				</div>
			</SheetContent>
		</Sheet>
	);
}