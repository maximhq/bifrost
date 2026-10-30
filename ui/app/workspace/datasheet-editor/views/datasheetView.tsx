import OverriddenPrice from "@/app/workspace/model-catalog/views/overriddenPrice";
import FullPageLoader from "@/components/fullPageLoader";
import { NoPermissionView } from "@/components/noPermissionView";
import PageTitle from "@/components/pageTitle";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ProviderSelector } from "@/components/ui/providerSelector";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels, ProviderName } from "@/lib/constants/logs";
import { getLocale } from "@/lib/i18n";
import { parseAsSafeString } from "@/lib/queryParamsParser";
import { type DatasheetEntry, type DatasheetEntryTarget, useGetDatasheetEntriesQuery } from "@/lib/store";
import { KnownProvider } from "@/lib/types/config";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { ChevronLeft, ChevronRight, Edit, Plus, Search } from "lucide-react";
import { parseAsBoolean, useQueryStates } from "nuqs";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import DatasheetEntrySheet from "./datasheetEntrySheet";
import { formatDatasheetValue } from "./datasheetFields";

const ALL_PROVIDERS_VALUE = "__all__";
const PAGE_SIZE = 25;

const toTestIdPart = (value: string) =>
	value
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, "-")
		.replace(/^-|-$/g, "");

const entryKey = (e: { provider: string; model: string; mode: string }) => `${e.provider}|${e.model}|${e.mode}`;

/** Effective value, with the upstream one struck through when an override changed it. */
function LimitCell({ entry, field }: { entry: DatasheetEntry; field: "context_length" | "max_output_tokens" }) {
	const effective = entry.effective?.[field];
	const upstream = entry.upstream?.[field];
	if (!entry.upstream || effective === upstream) return <span>{formatDatasheetValue(effective, getLocale())}</span>;
	return (
		<span className="inline-flex flex-wrap items-baseline justify-end gap-x-1.5">
			<span className="text-muted-foreground text-xs line-through">{formatDatasheetValue(upstream, getLocale())}</span>
			<span>{formatDatasheetValue(effective, getLocale())}</span>
		</span>
	);
}

function priceOverride(entry: DatasheetEntry, field: "input_cost_per_token" | "output_cost_per_token") {
	const effective = entry.effective?.[field];
	return entry.upstream && effective !== entry.upstream[field] ? effective : undefined;
}

export default function DatasheetView() {
	const { t } = useTranslation("models");
	const hasAccess = useRbac(RbacResource.ModelProvider, RbacOperation.View);
	const canEdit = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const allProvidersOption = useMemo(() => ({ value: ALL_PROVIDERS_VALUE, label: t("datasheet.allProviders") }), [t]);

	const [urlState, setUrlState] = useQueryStates(
		{
			search: parseAsSafeString.withDefault(""),
			provider: parseAsSafeString.withDefault(""),
			overridden: parseAsBoolean.withDefault(false),
		},
		{ history: "replace" },
	);
	const { search, provider: providerFilter, overridden } = urlState;
	const debouncedSearch = useDebouncedValue(search, 300);
	const [offset, setOffset] = useState(0);
	// undefined: sheet closed; null: adding a custom entry.
	const [editing, setEditing] = useState<DatasheetEntryTarget | null | undefined>(undefined);

	useEffect(() => {
		setOffset(0);
	}, [debouncedSearch, providerFilter, overridden]);

	const { data, isLoading, error, refetch } = useGetDatasheetEntriesQuery(
		{
			query: debouncedSearch || undefined,
			provider: providerFilter || undefined,
			overridden_only: overridden || undefined,
			limit: PAGE_SIZE,
			offset,
		},
		{ skip: !hasAccess },
	);
	const entries = data?.entries ?? [];
	const totalCount = data?.total_count ?? 0;

	useEffect(() => {
		if (offset < totalCount) return;
		setOffset(totalCount === 0 ? 0 : Math.floor((totalCount - 1) / PAGE_SIZE) * PAGE_SIZE);
	}, [totalCount, offset]);

	if (!hasAccess) return <NoPermissionView entity={t("datasheet.entityName")} />;

	return (
		<div className="no-padding-parent mx-auto flex h-[calc(var(--app-content-viewport)_-_var(--app-bottom-padding))] min-h-0 w-full flex-col overflow-hidden p-4">
			{editing !== undefined && <DatasheetEntrySheet target={editing} canEdit={canEdit} onClose={() => setEditing(undefined)} />}

			<div className="mb-4 flex shrink-0 flex-wrap items-center justify-end gap-2">
				<PageTitle title={t("datasheet.title")}>{t("datasheet.subtitle")}</PageTitle>
				<Button onClick={() => setEditing(null)} disabled={!canEdit} data-testid="datasheet-add-custom-btn">
					<Plus className="h-4 w-4" />
					{t("datasheet.addCustomModel")}
				</Button>
			</div>

			<div className="mb-4 flex shrink-0 flex-wrap items-center gap-3">
				<div className="relative w-full max-w-sm flex-1 basis-full sm:basis-auto">
					<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
					<Input
						aria-label={t("datasheet.searchAria")}
						placeholder={t("datasheet.searchPlaceholder")}
						value={search}
						onChange={(e) => setUrlState({ search: e.target.value || null })}
						className="pl-9"
						data-testid="datasheet-search-input"
					/>
				</div>
				<ProviderSelector
					source="catalog"
					data-testid="datasheet-provider-filter"
					className="w-full sm:w-[200px]"
					allOption={allProvidersOption}
					value={providerFilter || ALL_PROVIDERS_VALUE}
					onChange={(v: string) => setUrlState({ provider: v === ALL_PROVIDERS_VALUE ? null : v })}
				/>
				<div className="flex items-center gap-2">
					<Switch
						id="datasheet-overridden-only"
						checked={overridden}
						onCheckedChange={(checked) => setUrlState({ overridden: checked || null })}
						data-testid="datasheet-overridden-only-switch"
					/>
					<Label htmlFor="datasheet-overridden-only" className="text-sm">
						{t("datasheet.overriddenOnly")}
					</Label>
				</div>
			</div>

			{isLoading && !data ? (
				<FullPageLoader />
			) : error ? (
				<div className="flex grow flex-col items-center justify-center gap-4 text-center">
					<p className="text-muted-foreground text-sm">{t("datasheet.failedToLoad")}</p>
					<Button variant="outline" size="sm" onClick={refetch} data-testid="datasheet-retry-btn">
						{t("datasheet.retry")}
					</Button>
				</div>
			) : (
				<div className="mb-2 min-h-0 grow overflow-hidden rounded-sm border" data-testid="datasheet-table">
					<Table containerClassName="h-full overflow-y-auto overflow-x-auto" className="table-fixed">
						<TableHeader className="bg-muted sticky top-0 z-20">
							<TableRow className="hover:bg-transparent">
								<TableHead className="w-[130px] font-medium">{t("datasheet.provider")}</TableHead>
								<TableHead className="font-medium">{t("datasheet.model")}</TableHead>
								<TableHead className="w-[130px] font-medium">{t("datasheet.mode")}</TableHead>
								<TableHead className="w-[120px] text-right font-medium">{t("datasheet.contextLength")}</TableHead>
								<TableHead className="w-[120px] text-right font-medium">{t("datasheet.maxOutputTokens")}</TableHead>
								<TableHead className="w-[110px] text-right font-medium">{t("datasheet.colInput")}</TableHead>
								<TableHead className="w-[110px] text-right font-medium">{t("datasheet.colOutput")}</TableHead>
								<TableHead className="w-[170px] font-medium">{t("datasheet.colStatus")}</TableHead>
								<TableHead className="w-[56px]"></TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{entries.length === 0 ? (
								<TableRow>
									<TableCell colSpan={9} className="h-24 text-center">
										<span className="text-muted-foreground text-sm">
											{overridden && !debouncedSearch && !providerFilter ? t("datasheet.noOverrides") : t("datasheet.noMatchingEntries")}
										</span>
									</TableCell>
								</TableRow>
							) : (
								entries.map((entry) => {
									const testKey = `${toTestIdPart(entry.provider)}-${toTestIdPart(entry.model)}-${toTestIdPart(entry.mode)}`;
									const isOverridden = (entry.overrides?.length ?? 0) > 0;
									const drifted = (entry.drifted_fields?.length ?? 0) > 0;
									return (
										<TableRow key={entryKey(entry)} data-testid={`datasheet-row-${testKey}`}>
											<TableCell className="py-3">
												<div className="flex min-w-0 items-center gap-2">
													<RenderProviderIcon provider={entry.provider as KnownProvider} size="sm" className="h-4 w-4" />
													<span className="truncate text-sm">{ProviderLabels[entry.provider as ProviderName] || entry.provider}</span>
												</div>
											</TableCell>
											<TableCell className="truncate py-3 font-mono text-sm" title={entry.model}>
												{entry.model}
											</TableCell>
											<TableCell className="truncate py-3 font-mono text-xs">{entry.mode || "—"}</TableCell>
											<TableCell className="py-3 text-right font-mono text-sm" data-testid={`datasheet-context-${testKey}`}>
												<LimitCell entry={entry} field="context_length" />
											</TableCell>
											<TableCell className="py-3 text-right font-mono text-sm">
												<LimitCell entry={entry} field="max_output_tokens" />
											</TableCell>
											<TableCell className="py-3 text-right font-mono text-sm">
												<OverriddenPrice
													variant="compact"
													base={entry.upstream?.input_cost_per_token ?? entry.effective?.input_cost_per_token}
													override={priceOverride(entry, "input_cost_per_token")}
													overrideName={t("datasheet.yourEdit")}
												/>
											</TableCell>
											<TableCell className="py-3 text-right font-mono text-sm">
												<OverriddenPrice
													variant="compact"
													base={entry.upstream?.output_cost_per_token ?? entry.effective?.output_cost_per_token}
													override={priceOverride(entry, "output_cost_per_token")}
													overrideName={t("datasheet.yourEdit")}
												/>
											</TableCell>
											<TableCell className="py-3">
												<div className="flex flex-wrap gap-1">
													{entry.is_custom && <Badge variant="secondary">{t("datasheet.custom")}</Badge>}
													{isOverridden && !entry.is_custom && (
														<Badge variant="outline" data-testid={`datasheet-overridden-badge-${testKey}`}>
															{t("datasheet.overridden")}
														</Badge>
													)}
													{drifted && (
														<Badge
															variant="outline"
															className="border-amber-500 text-amber-600 dark:text-amber-400"
															data-testid={`datasheet-drift-badge-${testKey}`}
														>
															{t("datasheet.upstreamChangedBadge")}
														</Badge>
													)}
													{!entry.effective && <Badge variant="destructive">{t("datasheet.notInDatasheet")}</Badge>}
													{entry.effective?.is_deprecated && <Badge variant="outline">{t("datasheet.deprecatedBadge")}</Badge>}
												</div>
											</TableCell>
											<TableCell className="px-1 py-3">
												<Button
													variant="ghost"
													size="icon"
													className="h-7 w-7"
													onClick={() => setEditing({ provider: entry.provider, model: entry.model, mode: entry.mode })}
													aria-label={t("datasheet.editEntryFor", { name: entry.model })}
													data-testid={`datasheet-edit-${testKey}`}
												>
													<Edit className="h-4 w-4" />
												</Button>
											</TableCell>
										</TableRow>
									);
								})
							)}
						</TableBody>
					</Table>
				</div>
			)}

			{totalCount > 0 && (
				<div className="flex shrink-0 items-center justify-between text-xs" data-testid="datasheet-pagination">
					<div className="text-muted-foreground">
						{t("datasheet.rangeOfEntries", {
							from: (offset + 1).toLocaleString(getLocale()),
							to: Math.min(offset + PAGE_SIZE, totalCount).toLocaleString(getLocale()),
							total: totalCount.toLocaleString(getLocale()),
						})}
					</div>
					<div className="flex items-center gap-2">
						<Button
							variant="ghost"
							size="sm"
							onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
							disabled={offset === 0}
							aria-label={t("datasheet.previousPage")}
							data-testid="datasheet-pagination-prev-btn"
						>
							<ChevronLeft className="size-3" />
						</Button>
						<Button
							variant="ghost"
							size="sm"
							onClick={() => setOffset(offset + PAGE_SIZE)}
							disabled={offset + PAGE_SIZE >= totalCount}
							aria-label={t("datasheet.nextPage")}
							data-testid="datasheet-pagination-next-btn"
						>
							<ChevronRight className="size-3" />
						</Button>
					</div>
				</div>
			)}
		</div>
	);
}