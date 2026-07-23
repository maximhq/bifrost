import { BudgetOverrideDialog } from "@/components/budgetOverrideDialog";
import { SheetNavigationButtons } from "@/components/sheetNavigationButtons";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { Progress } from "@/components/ui/progress";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useSheetNavigation } from "@/hooks/useSheetNavigation";
import { supportsCalendarAlignment } from "@/lib/constants/governance";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels, ProviderName } from "@/lib/constants/logs";
import { useRemoveVirtualKeyBudgetOverrideMutation, useSetVirtualKeyBudgetOverrideMutation } from "@/lib/store/apis/governanceApi";
import { BudgetOverrideRequest, VirtualKey } from "@/lib/types/governance";
import { cn } from "@/lib/utils";
import {
	calculateUsagePercentage,
	formatCurrency,
	getEffectiveBudgetLimit,
	hasActiveBudgetOverride,
	parseResetPeriod,
} from "@/lib/utils/governance";
import ManagedVirtualKeyNotice from "@enterprise/components/access-profiles/managedVirtualKeyNotice";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { formatDistanceToNow } from "date-fns";
import { Users } from "lucide-react";
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
	onNavigate?: (direction: "prev" | "next") => void;
	hasPrev?: boolean;
	hasNext?: boolean;
}

export default function VirtualKeyDetailSheet({
	virtualKey,
	onClose,
	onNavigate,
	hasPrev = false,
	hasNext = false,
}: VirtualKeyDetailSheetProps) {
	const { assignedUsers, isManagedByProfile, managingProfile, hasApRateLimit, displayBudgets, displayRateLimit } =
		useVirtualKeyUsage(virtualKey);
	const canUpdateVirtualKeys = useRbac(RbacResource.VirtualKeys, RbacOperation.Update);
	const [setBudgetOverride] = useSetVirtualKeyBudgetOverrideMutation();
	const [removeBudgetOverride] = useRemoveVirtualKeyBudgetOverrideMutation();
	const saveBudgetOverride = async (budgetId: string, data: BudgetOverrideRequest) => {
		await setBudgetOverride({ vkId: virtualKey.id, budgetId, data }).unwrap();
	};
	const clearBudgetOverride = async (budgetId: string) => {
		await removeBudgetOverride({ vkId: virtualKey.id, budgetId }).unwrap();
	};

	const { prev: prevKeys, next: nextKeys } = useSheetNavigation({
		enabled: true,
		hasPrev,
		hasNext,
		onNavigate: (direction) => onNavigate?.(direction),
	});

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
		displayBudgets?.some((b) => b.current_usage >= getEffectiveBudgetLimit(b)) ||
		// Rate limits exhausted
		(displayRateLimit?.token_current_usage &&
			displayRateLimit?.token_max_limit &&
			displayRateLimit.token_current_usage >= displayRateLimit.token_max_limit) ||
		(displayRateLimit?.request_current_usage &&
			displayRateLimit?.request_max_limit &&
			displayRateLimit.request_current_usage >= displayRateLimit.request_max_limit);

	return (
		<Sheet open onOpenChange={onClose}>
			<SheetContent className="flex w-full flex-col overflow-x-hidden p-0 pt-4 sm:max-w-2xl">
				<SheetHeader
					className="flex flex-row items-center justify-between px-0 py-4"
					headerClassName="mb-0 sticky -top-4 bg-card z-10 px-8"
				>
					<div className="flex flex-col items-start">
						<SheetTitle>{virtualKey.name}</SheetTitle>
						<SheetDescription>{virtualKey.description || "Virtual key details and usage information"}</SheetDescription>
					</div>
					<SheetNavigationButtons
						hasPrev={hasPrev}
						hasNext={hasNext}
						onNavigate={(dir) => onNavigate?.(dir)}
						prevKeys={prevKeys}
						nextKeys={nextKeys}
						entityLabel="virtual key"
					/>
				</SheetHeader>

				<div className="space-y-6 px-8 py-4">
					<ManagedVirtualKeyNotice managingProfile={managingProfile} />

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
									{(() => {
										const isExpired = !!virtualKey.expires_at && Date.now() >= new Date(virtualKey.expires_at).getTime();
										const variant = !virtualKey.is_active ? "secondary" : isExpired || isExhausted ? "destructive" : "default";
										const label = !virtualKey.is_active ? "Inactive" : isExpired ? "Expired" : isExhausted ? "Exhausted" : "Active";
										return <Badge variant={variant}>{label}</Badge>;
									})()}
								</div>
							</div>

							{virtualKey.expires_at && (
								<div className="grid grid-cols-3 items-center gap-4">
									<span className="text-muted-foreground text-sm">Expires</span>
									<div className="col-span-2 text-sm">
										{formatDistanceToNow(new Date(virtualKey.expires_at), {
											addSuffix: true,
										})}
										<span className="text-muted-foreground ml-1 text-xs">({new Date(virtualKey.expires_at).toLocaleString()})</span>
									</div>
								</div>
							)}

							<div className="grid grid-cols-3 items-center gap-4">
								<span className="text-muted-foreground text-sm">Created</span>
								<div className="col-span-2 text-sm">
									{formatDistanceToNow(new Date(virtualKey.created_at), {
										addSuffix: true,
									})}
								</div>
							</div>

							<div className="grid grid-cols-3 items-center gap-4">
								<span className="text-muted-foreground text-sm">Last Updated</span>
								<div className="col-span-2 text-sm">
									{formatDistanceToNow(new Date(virtualKey.updated_at), {
										addSuffix: true,
									})}
								</div>
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
													<span className="text-muted-foreground pt-0.5 text-sm font-medium">Blocked Models</span>
													<div className="col-span-2">
														{config.blacklisted_models?.includes("*") ? (
															<Badge variant="destructive" className="text-xs">
																All Models Blocked
															</Badge>
														) : config.blacklisted_models && config.blacklisted_models.length > 0 ? (
															<div className="flex flex-wrap gap-1">
																{config.blacklisted_models.map((model) => (
																	<Badge key={model} variant="destructive" className="text-xs">
																		{model}
																	</Badge>
																))}
															</div>
														) : (
															<Badge variant="secondary" className="text-xs">
																No models blocked
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
																	{!isManagedByProfile && b.id ? (
																		<div className="flex justify-end">
																			<BudgetOverrideDialog
																				budget={b}
																				onSave={(data) => saveBudgetOverride(b.id, data)}
																				onRemove={() => clearBudgetOverride(b.id)}
																				disabled={!canUpdateVirtualKeys}
																				calendarAligned={virtualKey.calendar_aligned}
																			/>
																		</div>
																	) : null}
																	<UsageLine current={b.current_usage} max={getEffectiveBudgetLimit(b)} format={formatCurrency} />
																	{hasActiveBudgetOverride(b) ? (
																		<p className="text-muted-foreground text-xs">
																			Base {formatCurrency(b.max_limit)} + {formatCurrency(b.override_amount ?? 0)} override
																		</p>
																	) : null}
																	<div className="text-muted-foreground flex items-center justify-between text-xs">
																		<span>
																			Resets {parseResetPeriod(b.reset_duration)}
																			{virtualKey.calendar_aligned && supportsCalendarAlignment(b.reset_duration) && " (calendar)"}
																		</span>
																		{b.last_reset ? (
																			<span>
																				Last reset{" "}
																				{formatDistanceToNow(new Date(b.last_reset), {
																					addSuffix: true,
																				})}
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
																		<span>
																			Resets {parseResetPeriod(config.rate_limit.token_reset_duration || "")}
																			{virtualKey.calendar_aligned &&
																				supportsCalendarAlignment(config.rate_limit.token_reset_duration || "") &&
																				" (calendar)"}
																		</span>
																		{config.rate_limit.token_last_reset ? (
																			<span>
																				Last reset {formatDistanceToNow(new Date(config.rate_limit.token_last_reset), { addSuffix: true })}
																			</span>
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
																		<span>
																			Resets {parseResetPeriod(config.rate_limit.request_reset_duration || "")}
																			{virtualKey.calendar_aligned &&
																				supportsCalendarAlignment(config.rate_limit.request_reset_duration || "") &&
																				" (calendar)"}
																		</span>
																		{config.rate_limit.request_last_reset ? (
																			<span>
																				Last reset{" "}
																				{formatDistanceToNow(new Date(config.rate_limit.request_last_reset), { addSuffix: true })}
																			</span>
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
							{isManagedByProfile && managingProfile?.budgets?.length ? (
								<span className="text-muted-foreground ml-2 text-xs font-normal">(from {managingProfile.name})</span>
							) : null}
						</h3>

						{displayBudgets && displayBudgets.length > 0 ? (
							<div className="space-y-4">
								{displayBudgets.map((b, bIdx) => (
									<div key={bIdx} className="space-y-2 rounded-lg border p-4">
										{!isManagedByProfile && b.id ? (
											<div className="flex justify-end">
												<BudgetOverrideDialog
													budget={b}
													onSave={(data) => saveBudgetOverride(b.id, data)}
													onRemove={() => clearBudgetOverride(b.id)}
													disabled={!canUpdateVirtualKeys}
													calendarAligned={virtualKey.calendar_aligned}
												/>
											</div>
										) : null}
										<UsageLine current={b.current_usage} max={getEffectiveBudgetLimit(b)} format={formatCurrency} />
										{hasActiveBudgetOverride(b) ? (
											<p className="text-muted-foreground text-xs">
												Base {formatCurrency(b.max_limit)} + {formatCurrency(b.override_amount ?? 0)} override
												{b.override_mode === "cycles" ? ` · ${b.override_cycles_remaining} cycles remaining` : " · until removed"}
											</p>
										) : null}
										<div className="text-muted-foreground flex items-center justify-between text-xs">
											<span>
												Resets {parseResetPeriod(b.reset_duration)}
												{virtualKey.calendar_aligned && supportsCalendarAlignment(b.reset_duration) && " (calendar)"}
											</span>
											{b.last_reset ? (
												<span>
													Last reset{" "}
													{formatDistanceToNow(new Date(b.last_reset), {
														addSuffix: true,
													})}
												</span>
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
									<div className="space-y-3 rounded-lg border p-4">
										<span className="text-sm font-medium">Token Limits</span>
										<UsageLine
											current={displayRateLimit.token_current_usage}
											max={displayRateLimit.token_max_limit}
											format={(n) => n.toLocaleString()}
										/>
										<div className="text-muted-foreground flex items-center justify-between text-xs">
											<span>
												Resets {parseResetPeriod(displayRateLimit.token_reset_duration || "")}
												{virtualKey.calendar_aligned &&
													supportsCalendarAlignment(displayRateLimit.token_reset_duration || "") &&
													" (calendar)"}
											</span>
											{displayRateLimit.token_last_reset ? (
												<span>
													Last reset{" "}
													{formatDistanceToNow(new Date(displayRateLimit.token_last_reset), {
														addSuffix: true,
													})}
												</span>
											) : null}
										</div>
									</div>
								) : null}

								{/* Request Limits */}
								{displayRateLimit.request_max_limit != null ? (
									<div className="space-y-3 rounded-lg border p-4">
										<span className="text-sm font-medium">Request Limits</span>
										<UsageLine
											current={displayRateLimit.request_current_usage}
											max={displayRateLimit.request_max_limit}
											format={(n) => n.toLocaleString()}
										/>
										<div className="text-muted-foreground flex items-center justify-between text-xs">
											<span>
												Resets {parseResetPeriod(displayRateLimit.request_reset_duration || "")}
												{virtualKey.calendar_aligned &&
													supportsCalendarAlignment(displayRateLimit.request_reset_duration || "") &&
													" (calendar)"}
											</span>
											{displayRateLimit.request_last_reset ? (
												<span>
													Last reset{" "}
													{formatDistanceToNow(new Date(displayRateLimit.request_last_reset), {
														addSuffix: true,
													})}
												</span>
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