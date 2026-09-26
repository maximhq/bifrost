import { BudgetOverrideDialog } from "@/components/budgetOverrideDialog";
import { BudgetOverrideManagerDialog, type BudgetOverrideSection } from "@/components/budgetOverrideManagerDialog";
import { CopyableId } from "@/components/copyableId";
import { isWildcardList, ModelAccessBadges } from "@/components/modelAccess";
import { SheetNavigationButtons } from "@/components/sheetNavigationButtons";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { Progress } from "@/components/ui/progress";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useSheetNavigation } from "@/hooks/useSheetNavigation";
import { fiscalQuarterNote, supportsCalendarAlignment } from "@/lib/constants/governance";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels, ProviderName } from "@/lib/constants/logs";
import { useRemoveVirtualKeyBudgetOverrideMutation, useSetVirtualKeyBudgetOverrideMutation } from "@/lib/store/apis/governanceApi";
import { BudgetOverrideRequest, VirtualKey, VirtualKeyProviderConfig } from "@/lib/types/governance";
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
import { useTranslation } from "react-i18next";
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
	const { t } = useTranslation("models");
	const { assignedUsers, isManagedByProfile, managingProfile, displayBudgets, displayRateLimit } = useVirtualKeyUsage(virtualKey);
	const canUpdateVirtualKeys = useRbac(RbacResource.VirtualKeys, RbacOperation.Update);
	const [setBudgetOverride] = useSetVirtualKeyBudgetOverrideMutation();
	const [removeBudgetOverride] = useRemoveVirtualKeyBudgetOverrideMutation();
	const saveBudgetOverride = async (budgetId: string, data: BudgetOverrideRequest) => {
		await setBudgetOverride({ vkId: virtualKey.id, budgetId, data }).unwrap();
	};
	const clearBudgetOverride = async (budgetId: string) => {
		await removeBudgetOverride({ vkId: virtualKey.id, budgetId }).unwrap();
	};
	// Provider budget + one section per model, for the provider's single override modal.
	// Only persisted budgets (with an id) can carry an override.
	const buildProviderOverrideSections = (config: VirtualKeyProviderConfig): BudgetOverrideSection[] => {
		const toRows = (budgets: VirtualKeyProviderConfig["budgets"]) =>
			(budgets ?? [])
				.filter((b) => b.id)
				.map((b) => ({ key: b.id, label: parseResetPeriod(b.reset_duration), budget: b, calendarAligned: virtualKey.calendar_aligned }));
		const sections: BudgetOverrideSection[] = [];
		const providerRows = toRows(config.budgets);
		if (providerRows.length > 0) sections.push({ key: "provider", title: t("virtualKeys.providerBudget"), rows: providerRows });
		for (const mb of config.model_budgets ?? []) {
			const rows = toRows(mb.budgets);
			if (rows.length > 0) sections.push({ key: `m:${mb.model_name}`, title: t("virtualKeys.modelNamed", { name: mb.model_name }), rows });
		}
		return sections;
	};

	const { prev: prevKeys, next: nextKeys } = useSheetNavigation({
		enabled: true,
		hasPrev,
		hasNext,
		onNavigate: (direction) => onNavigate?.(direction),
	});

	const getEntityInfo = () => {
		if (virtualKey.team) {
			return { type: t("routing.team"), name: virtualKey.team.name };
		}
		if (virtualKey.customer) {
			return { type: t("routing.customer"), name: virtualKey.customer.name };
		}
		return { type: t("virtualKeys.none"), name: "" };
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
					headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8"
				>
					<div className="flex min-w-0 flex-col items-start">
						<div className="flex min-w-0 items-center gap-1">
							<SheetTitle className="truncate">{virtualKey.name}</SheetTitle>
							<CopyableId id={virtualKey.id} entityLabel="Virtual key" />
						</div>
						<SheetDescription>{virtualKey.description || t("virtualKeys.detailsFallback")}</SheetDescription>
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

				<div className="space-y-6 px-4 py-4 md:px-8">
					<ManagedVirtualKeyNotice isManagedByProfile={isManagedByProfile} managingProfile={managingProfile} />

					{assignedUsers.length > 0 ? (
						<div className="space-y-1">
							<Label className="text-sm font-medium">{t("virtualKeys.assignedUsers")}</Label>
							<div className="flex items-center gap-2">
								<Users className="text-muted-foreground h-4 w-4" />
								<span className="text-sm">{assignedUsers.map((u) => u.name || u.email).join(", ")}</span>
							</div>
						</div>
					) : null}

					{/* Basic Information */}
					<div className="space-y-4">
						<h3 className="font-semibold">{t("virtualKeys.basicInformation")}</h3>

						<div className="grid gap-4">
							<div className="grid grid-cols-1 items-center gap-4 md:grid-cols-3">
								<span className="text-muted-foreground text-sm">{t("virtualKeys.status")}</span>
								<div className="col-span-2">
									{(() => {
										const isExpired = !!virtualKey.expires_at && Date.now() >= new Date(virtualKey.expires_at).getTime();
										const variant = !virtualKey.is_active ? "secondary" : isExpired || isExhausted ? "destructive" : "default";
										const label = !virtualKey.is_active
											? t("virtualKeys.inactive")
											: isExpired
												? t("virtualKeys.expired")
												: isExhausted
													? t("virtualKeys.exhausted")
													: t("virtualKeys.active");
										return <Badge variant={variant}>{label}</Badge>;
									})()}
								</div>
							</div>

							{virtualKey.expires_at && (
								<div className="grid grid-cols-1 items-center gap-4 md:grid-cols-3">
									<span className="text-muted-foreground text-sm">{t("virtualKeys.expires")}</span>
									<div className="col-span-2 text-sm">
										{formatDistanceToNow(new Date(virtualKey.expires_at), {
											addSuffix: true,
										})}
										<span className="text-muted-foreground ml-1 text-xs">({new Date(virtualKey.expires_at).toLocaleString()})</span>
									</div>
								</div>
							)}

							{typeof virtualKey.disable_content_logging === "boolean" && (
								<div className="grid grid-cols-1 items-center gap-4 md:grid-cols-3">
									<span className="text-muted-foreground text-sm">{t("virtualKeys.contentLogging")}</span>
									<div className="col-span-2">
										<Badge
											variant={virtualKey.disable_content_logging ? "secondary" : "default"}
											data-testid="vk-details-content-logging-badge"
										>
											{virtualKey.disable_content_logging ? t("virtualKeys.contentLoggingOff") : t("virtualKeys.contentLoggingOn")}
										</Badge>
									</div>
								</div>
							)}

							<div className="grid grid-cols-1 items-center gap-4 md:grid-cols-3">
								<span className="text-muted-foreground text-sm">{t("virtualKeys.created")}</span>
								<div className="col-span-2 text-sm">
									{formatDistanceToNow(new Date(virtualKey.created_at), {
										addSuffix: true,
									})}
								</div>
							</div>

							<div className="grid grid-cols-1 items-center gap-4 md:grid-cols-3">
								<span className="text-muted-foreground text-sm">{t("virtualKeys.lastUpdated")}</span>
								<div className="col-span-2 text-sm">
									{formatDistanceToNow(new Date(virtualKey.updated_at), {
										addSuffix: true,
									})}
								</div>
							</div>

							{						entityInfo.type !== t("virtualKeys.none") && (
								<div className="grid grid-cols-1 items-center gap-4 md:grid-cols-3">
									<span className="text-muted-foreground text-sm">{t("virtualKeys.assignedTo")}</span>
									<div className="col-span-2 flex items-center gap-2">
										<Badge variant={entityInfo.type === t("virtualKeys.none") ? "outline" : "secondary"}>{entityInfo.type}</Badge>
										<span className="text-sm">{entityInfo.name}</span>
									</div>
								</div>
							)}
						</div>
					</div>

					{/* Providers, MCP access, budgets, and rate limits are all owned by the access profile for a
					managed key (ManagedVirtualKeyNotice above says so, with a link to it) - the VK itself never
					carries this data, so showing these sections would only ever read as a false deny-all. */}
					{!isManagedByProfile && (
						<>
							<DottedSeparator />

							{/* Provider Configurations */}
							<div className="space-y-4">
								<div className="flex items-center gap-2">
									<h3 className="font-semibold">{t("virtualKeys.providerConfigs")}</h3>
									{virtualKey.allow_all_providers && (
										<Badge variant="success" className="text-xs">
											{t("virtualKeys.allProvidersBadge")}
										</Badge>
									)}
								</div>

								{/* A key that allows every provider grants ones it holds no entry for, including ones added
								later, so the entries below are overrides rather than the whole of what it may reach. */}
								{virtualKey.allow_all_providers && (
									<p className="text-muted-foreground text-sm">{t("virtualKeys.allProvidersHelp")}</p>
								)}

								<div className="space-y-3">
									{!virtualKey.provider_configs || virtualKey.provider_configs.length === 0 ? (
										<span className="text-muted-foreground text-sm">
											{virtualKey.allow_all_providers ? t("virtualKeys.noProviderOverrides") : t("virtualKeys.noProvidersDeny")}
										</span>
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
														<div className="flex items-center gap-2">
															<Badge variant="outline" className="font-mono text-xs">
																{t("virtualKeys.weightLabel")}
																{config.weight != null ? config.weight : <span className="text-muted-foreground italic">{t("virtualKeys.weightNotSet")}</span>}
															</Badge>
															{!isManagedByProfile ? (
																<BudgetOverrideManagerDialog
																	title={t("virtualKeys.budgetOverridesTitle", {
																		provider: ProviderLabels[config.provider as ProviderName] || config.provider,
																	})}
																	sections={buildProviderOverrideSections(config)}
																	onSave={saveBudgetOverride}
																	onRemove={clearBudgetOverride}
																	disabled={!canUpdateVirtualKeys}
																/>
															) : null}
														</div>
													</div>

													{/* Basic Config */}
													<div className="space-y-3">
														<div className="grid grid-cols-1 items-start gap-4 md:grid-cols-3">
															<span className="text-muted-foreground pt-0.5 text-sm font-medium">{t("virtualKeys.allowedModels")}</span>
															<div className="col-span-2">
																<ModelAccessBadges value={config.allowed_models} mode="allow" />
															</div>
														</div>

														<div className="grid grid-cols-1 items-start gap-4 md:grid-cols-3">
															<span className="text-muted-foreground pt-0.5 text-sm font-medium">{t("virtualKeys.blockedModels")}</span>
															<div className="col-span-2">
																<ModelAccessBadges
																	value={config.blacklisted_models}
																	mode="block"
																	allowsAllModels={isWildcardList(config.allowed_models)}
																/>
															</div>
														</div>

														<div className="grid grid-cols-1 items-start gap-4 md:grid-cols-3">
															<span className="text-muted-foreground pt-0.5 text-sm font-medium">{t("virtualKeys.allowedKeys")}</span>
															<div className="col-span-2">
																{config.allow_all_keys ? (
																	<Badge variant="success" className="text-xs">
																		{t("customPricing.allKeys")}
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
																		{t("virtualKeys.noKeysDenyAll")}
																	</Badge>
																)}
															</div>
														</div>

														{/* Provider Budgets */}
														{config.budgets && config.budgets.length > 0 && (
															<>
																<DottedSeparator />
																<div className="space-y-2">
																	<h4 className="text-sm font-medium">{t("virtualKeys.providerBudgets")}</h4>
																	{config.budgets.map((b, bIdx) => (
																		<div key={bIdx} className="space-y-2">
																			<UsageLine current={b.current_usage} max={getEffectiveBudgetLimit(b)} format={formatCurrency} />
																			{hasActiveBudgetOverride(b) ? (
																				<p className="text-muted-foreground text-xs">
																					{t("virtualKeys.basePlusOverride", { base: formatCurrency(b.max_limit), override: formatCurrency(b.override_amount ?? 0) })}
																				</p>
																			) : null}
																			<div className="text-muted-foreground flex items-center justify-between text-xs">
																				<span>
																					{t("virtualKeys.resets", { period: parseResetPeriod(b.reset_duration) })}
																					{virtualKey.calendar_aligned && supportsCalendarAlignment(b.reset_duration) && t("virtualKeys.calendarSuffix")}
																					{fiscalQuarterNote(b.reset_duration, b.reset_config)}
																				</span>
																				{b.last_reset ? (
																					<span>
																						{t("virtualKeys.lastReset", { when: formatDistanceToNow(new Date(b.last_reset), {
																							addSuffix: true,
																						}) })}
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
																	<h4 className="text-sm font-medium">{t("virtualKeys.providerRateLimits")}</h4>

																	{/* Token Limits */}
																	{config.rate_limit.token_max_limit != null ? (
																		<div className="space-y-2">
																			<span className="text-muted-foreground text-xs font-medium">{t("virtualKeys.tokenLimits")}</span>
																			<UsageLine
																				current={config.rate_limit.token_current_usage}
																				max={config.rate_limit.token_max_limit}
																				format={(n) => n.toLocaleString()}
																			/>
																			<div className="text-muted-foreground flex items-center justify-between text-xs">
																				<span>
																					{t("virtualKeys.resets", { period: parseResetPeriod(config.rate_limit.token_reset_duration || "") })}
																					{virtualKey.calendar_aligned &&
																						supportsCalendarAlignment(config.rate_limit.token_reset_duration || "") &&
																						t("virtualKeys.calendarSuffix")}
																				</span>
																				{config.rate_limit.token_last_reset ? (
																					<span>
																						{t("virtualKeys.lastReset", { when: formatDistanceToNow(new Date(config.rate_limit.token_last_reset), { addSuffix: true }) })}
																					</span>
																				) : null}
																			</div>
																		</div>
																	) : null}

																	{/* Request Limits */}
																	{config.rate_limit.request_max_limit != null ? (
																		<div className="space-y-2">
																			<span className="text-muted-foreground text-xs font-medium">{t("virtualKeys.requestLimits")}</span>
																			<UsageLine
																				current={config.rate_limit.request_current_usage}
																				max={config.rate_limit.request_max_limit}
																				format={(n) => n.toLocaleString()}
																			/>
																			<div className="text-muted-foreground flex items-center justify-between text-xs">
																				<span>
																					{t("virtualKeys.resets", { period: parseResetPeriod(config.rate_limit.request_reset_duration || "") })}
																					{virtualKey.calendar_aligned &&
																						supportsCalendarAlignment(config.rate_limit.request_reset_duration || "") &&
																						t("virtualKeys.calendarSuffix")}
																				</span>
																				{config.rate_limit.request_last_reset ? (
																					<span>
																						{t("virtualKeys.lastReset", { when: formatDistanceToNow(new Date(config.rate_limit.request_last_reset), { addSuffix: true }) })}
																					</span>
																				) : null}
																			</div>
																		</div>
																	) : null}

																	{config.rate_limit.token_max_limit == null && config.rate_limit.request_max_limit == null && (
																		<p className="text-muted-foreground text-sm">{t("virtualKeys.noProviderRateLimits")}</p>
																	)}
																</div>
															</>
														)}

														{/* Model Budgets — per-model caps/rate-limits under this provider */}
														{config.model_budgets && config.model_budgets.length > 0 && (
															<>
																<DottedSeparator />
																<div className="space-y-3">
																	<h4 className="text-sm font-medium">{t("virtualKeys.modelBudgets")}</h4>
																	{config.model_budgets.map((mb, mbIdx) => (
																		<div key={`${mb.model_name}-${mbIdx}`} className="space-y-3 rounded-md border p-3">
																			<span className="text-sm font-medium">{mb.model_name}</span>

																			{/* Budgets */}
																			{mb.budgets && mb.budgets.length > 0
																				? mb.budgets.map((b, bIdx) => (
																						<div key={bIdx} className="space-y-2">
																							<UsageLine
																								current={b.current_usage}
																								max={getEffectiveBudgetLimit(b)}
																								format={formatCurrency}
																							/>
																							{hasActiveBudgetOverride(b) ? (
																								<p className="text-muted-foreground text-xs">
																									{t("virtualKeys.basePlusOverride", { base: formatCurrency(b.max_limit), override: formatCurrency(b.override_amount ?? 0) })}
																								</p>
																							) : null}
																							<div className="text-muted-foreground flex items-center justify-between text-xs">
																								<span>
																									{t("virtualKeys.resets", { period: parseResetPeriod(b.reset_duration) })}
																									{virtualKey.calendar_aligned &&
																										supportsCalendarAlignment(b.reset_duration) &&
																										t("virtualKeys.calendarSuffix")}
																									{fiscalQuarterNote(b.reset_duration, b.reset_config)}
																								</span>
																								{b.last_reset ? (
																									<span>{t("virtualKeys.lastReset", { when: formatDistanceToNow(new Date(b.last_reset), { addSuffix: true }) })}</span>
																								) : null}
																							</div>
																						</div>
																					))
																				: null}

																			{/* Token Limits */}
																			{mb.rate_limit?.token_max_limit != null ? (
																				<div className="space-y-2">
																					<span className="text-muted-foreground text-xs font-medium">{t("virtualKeys.tokenLimits")}</span>
																					<UsageLine
																						current={mb.rate_limit.token_current_usage}
																						max={mb.rate_limit.token_max_limit}
																						format={(n) => n.toLocaleString()}
																					/>
																					<div className="text-muted-foreground text-xs">
																						{t("virtualKeys.resets", { period: parseResetPeriod(mb.rate_limit.token_reset_duration || "") })}
																						{virtualKey.calendar_aligned &&
																							supportsCalendarAlignment(mb.rate_limit.token_reset_duration || "") &&
																							t("virtualKeys.calendarSuffix")}
																					</div>
																				</div>
																			) : null}

																			{/* Request Limits */}
																			{mb.rate_limit?.request_max_limit != null ? (
																				<div className="space-y-2">
																					<span className="text-muted-foreground text-xs font-medium">{t("virtualKeys.requestLimits")}</span>
																					<UsageLine
																						current={mb.rate_limit.request_current_usage}
																						max={mb.rate_limit.request_max_limit}
																						format={(n) => n.toLocaleString()}
																					/>
																					<div className="text-muted-foreground text-xs">
																						{t("virtualKeys.resets", { period: parseResetPeriod(mb.rate_limit.request_reset_duration || "") })}
																						{virtualKey.calendar_aligned &&
																							supportsCalendarAlignment(mb.rate_limit.request_reset_duration || "") &&
																							t("virtualKeys.calendarSuffix")}
																					</div>
																				</div>
																			) : null}
																		</div>
																	))}
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

							{/* MCP Server Configurations */}
							<div className="space-y-4">
								<h3 className="font-semibold">{t("virtualKeys.mcpServerConfigurations")}</h3>

								<div className="space-y-3">
									{!virtualKey.mcp_configs || virtualKey.mcp_configs.length === 0 ? (
										<span className="text-muted-foreground text-sm">{t("virtualKeys.noMcpServers")}</span>
									) : (
										<div className="rounded-md border">
											<Table>
												<TableHeader>
													<TableRow>
														<TableHead>{t("virtualKeys.mcpServer")}</TableHead>
														<TableHead>{t("virtualKeys.allowedTools")}</TableHead>
													</TableRow>
												</TableHeader>
												<TableBody>
													{virtualKey.mcp_configs.map((config, index) => (
														<TableRow key={`${config.mcp_client?.name || config.id}-${index}`}>
															<TableCell>{config.mcp_client?.name || t("virtualKeys.unknownClient")}</TableCell>
															<TableCell>
																{config.tools_to_execute?.includes("*") ? (
																	<Badge variant="success" className="text-xs">
																		{t("virtualKeys.allTools")}
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
																		{t("virtualKeys.noToolsDenyAll")}
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
								<h3 className="font-semibold">{t("virtualKeys.budgetInformation")}</h3>

								{displayBudgets && displayBudgets.length > 0 ? (
									<div className="space-y-4">
										{displayBudgets.map((b, bIdx) => (
											<div key={bIdx} className="space-y-2 rounded-lg border p-4">
												{b.id ? (
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
														{t("virtualKeys.basePlusOverride", { base: formatCurrency(b.max_limit), override: formatCurrency(b.override_amount ?? 0) })}
														{b.override_mode === "cycles" ? ` · ${t("virtualKeys.cyclesRemaining", { count: b.override_cycles_remaining })}` : ` · ${t("virtualKeys.untilRemoved")}`}
													</p>
												) : null}
												<div className="text-muted-foreground flex items-center justify-between text-xs">
													<span>
														{t("virtualKeys.resets", { period: parseResetPeriod(b.reset_duration) })}
														{virtualKey.calendar_aligned && supportsCalendarAlignment(b.reset_duration) && t("virtualKeys.calendarSuffix")}
														{fiscalQuarterNote(b.reset_duration, b.reset_config)}
													</span>
													{b.last_reset ? (
														<span>
															{t("virtualKeys.lastReset", { when: formatDistanceToNow(new Date(b.last_reset), {
																addSuffix: true,
															}) })}
														</span>
													) : null}
												</div>
											</div>
										))}
									</div>
								) : (
									<p className="text-muted-foreground text-sm">{t("virtualKeys.noBudgetLimits")}</p>
								)}
							</div>

							{/* Rate Limits */}
							<div className="space-y-4">
								<h3 className="font-semibold">{t("virtualKeys.rateLimits")}</h3>

								{displayRateLimit ? (
									<div className="space-y-4">
										{/* Token Limits */}
										{displayRateLimit.token_max_limit != null ? (
											<div className="space-y-3 rounded-lg border p-4">
												<span className="text-sm font-medium">{t("virtualKeys.tokenLimitsTitle")}</span>
												<UsageLine
													current={displayRateLimit.token_current_usage}
													max={displayRateLimit.token_max_limit}
													format={(n) => n.toLocaleString()}
												/>
												<div className="text-muted-foreground flex items-center justify-between text-xs">
													<span>
														{t("virtualKeys.resets", { period: parseResetPeriod(displayRateLimit.token_reset_duration || "") })}
														{virtualKey.calendar_aligned &&
															supportsCalendarAlignment(displayRateLimit.token_reset_duration || "") &&
															t("virtualKeys.calendarSuffix")}
													</span>
													{displayRateLimit.token_last_reset ? (
														<span>
															{t("virtualKeys.lastReset", { when: formatDistanceToNow(new Date(displayRateLimit.token_last_reset), {
																addSuffix: true,
															}) })}
														</span>
													) : null}
												</div>
											</div>
										) : null}

										{/* Request Limits */}
										{displayRateLimit.request_max_limit != null ? (
											<div className="space-y-3 rounded-lg border p-4">
												<span className="text-sm font-medium">{t("virtualKeys.requestLimitsTitle")}</span>
												<UsageLine
													current={displayRateLimit.request_current_usage}
													max={displayRateLimit.request_max_limit}
													format={(n) => n.toLocaleString()}
												/>
												<div className="text-muted-foreground flex items-center justify-between text-xs">
													<span>
														{t("virtualKeys.resets", { period: parseResetPeriod(displayRateLimit.request_reset_duration || "") })}
														{virtualKey.calendar_aligned &&
															supportsCalendarAlignment(displayRateLimit.request_reset_duration || "") &&
															t("virtualKeys.calendarSuffix")}
													</span>
													{displayRateLimit.request_last_reset ? (
														<span>
															{t("virtualKeys.lastReset", { when: formatDistanceToNow(new Date(displayRateLimit.request_last_reset), {
																addSuffix: true,
															}) })}
														</span>
													) : null}
												</div>
											</div>
										) : null}

										{displayRateLimit.token_max_limit == null && displayRateLimit.request_max_limit == null && (
											<p className="text-muted-foreground text-sm">{t("virtualKeys.noRateLimits")}</p>
										)}
									</div>
								) : (
									<p className="text-muted-foreground text-sm">{t("virtualKeys.noRateLimits")}</p>
								)}
							</div>
						</>
					)}
				</div>
			</SheetContent>
		</Sheet>
	);
}