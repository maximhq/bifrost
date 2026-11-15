import { LoadErrorView } from "@/components/loadErrorView";
import PageTitle from "@/components/pageTitle";
import { PIN_SHADOW_RIGHT } from "@/components/table/columnPinning";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { getLocale } from "@/lib/i18n";
import { getErrorMessage } from "@/lib/store";
import { useDeleteModelRouterMutation, useGetModelRoutersQuery, useSetModelRouterRoutingMutation } from "@/lib/store/apis/governanceApi";
import { ModelRouter } from "@/lib/types/complexityRouter";
import { getScopeLabel } from "@/lib/utils/labels";
import { showLoadError } from "@/lib/utils/loadError";
import { getPriorityBadgeClass } from "@/lib/utils/routingRules";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Plus, Search } from "lucide-react";
import { parseAsString, useQueryState } from "nuqs";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { DeleteConfirmDialog, RowActionsMenu } from "./formParts";
import { classifierSummary } from "./overviewTab";
import { RouterSheet } from "./routerSheet";
import { matchesRouterSearch } from "./routersTable.utils";
import { TrafficPeriodPicker } from "./trafficPeriodPicker";
import { routerRequests, type RouterTrafficRead, type TrafficFilter } from "./useRouterTraffic";

// RouterEnabledSwitch turns a router on or off, as a routing rule's switch
// does. It sends only `enabled`, so a scope, condition or priority changed
// elsewhere since this view loaded is left as it is.
export function RouterEnabledSwitch({ router, disabled }: { router: ModelRouter; disabled?: boolean }) {
	const { t } = useTranslation("models", { keyPrefix: "routing.modelRouters" });
	const [setRouting] = useSetModelRouterRoutingMutation();
	return (
		<Switch
			data-testid={`model-router-enabled-${router.id}-switch`}
			aria-label={t("enabledAria", { name: router.name })}
			checked={router.routing.enabled}
			size="md"
			disabled={disabled}
			onAsyncCheckedChange={async (checked) => {
				await setRouting({ id: router.id, routing: { enabled: checked } })
					.unwrap()
					.then(() => toast.success(checked ? t("turnedOn", { name: router.name }) : t("turnedOff", { name: router.name })))
					.catch((err) => toast.error(t("toggleFailed", { name: router.name, error: getErrorMessage(err) })));
			}}
		/>
	);
}

interface RoutersTableProps {
	// What each router took over the chosen period, read once by the page, with the state of that read.
	traffic: RouterTrafficRead;
	// That period, and what the picker needs to change it.
	trafficFilter: TrafficFilter;
	onOpenRouter: (id: string) => void;
	onCreate: () => void;
}

// RoutersTable is the Routers section's list: each router with what it
// classifies with, where it takes requests (scope and priority), how many it
// took over the chosen period, and whether it is on, with the same row actions as
// routing rules.
export function RoutersTable({ traffic, trafficFilter, onOpenRouter, onCreate }: RoutersTableProps) {
	const { t } = useTranslation("models", { keyPrefix: "routing.modelRouters" });
	const { t: tcpx } = useTranslation("models", { keyPrefix: "routing.complexityUi" });
	const { t: trouting } = useTranslation("models", { keyPrefix: "routing" });
	const { t: tc } = useTranslation("common");
	const canCreate = useRbac(RbacResource.Routers, RbacOperation.Create);
	const canUpdate = useRbac(RbacResource.Routers, RbacOperation.Update);
	const canDelete = useRbac(RbacResource.Routers, RbacOperation.Delete);
	const { data, isLoading, error, refetch } = useGetModelRoutersQuery();
	const [deleteRouter, { isLoading: isDeleting }] = useDeleteModelRouterMutation();
	// Held by id, so the sheet always compares against the router as last read.
	const [editingId, setEditingId] = useState<string | null>(null);
	const [deleting, setDeleting] = useState<ModelRouter | null>(null);
	const routers = data?.routers ?? [];
	const editing = editingId ? routers.find((router) => router.id === editingId) : undefined;
	// Kept in the URL, as the projects list keeps its search.
	const [search, setSearch] = useQueryState("search", parseAsString.withDefault(""));
	const shown = useMemo(() => (data?.routers ?? []).filter((router) => matchesRouterSearch(router, search)), [data, search]);
	const entity = tc("entities.modelRouters");

	const confirmDelete = () => {
		if (!deleting) return;
		deleteRouter(deleting.id)
			.unwrap()
			.then(() => toast.success(t("deleted"), { position: "top-right" }))
			.catch((err) => toast.error(getErrorMessage(err)))
			.finally(() => setDeleting(null));
	};

	// A first load that failed replaces the list, with a way to retry, as the
	// routing rules list does.
	if (showLoadError(error, data)) {
		return <LoadErrorView entity="model routers" error={error} onRetry={() => void refetch()} testId="model-routers-load-error" />;
	}

	return (
		<div className="flex min-h-0 flex-col gap-4 overflow-y-auto" data-testid="model-routers-view">
			<PageTitle title={t("title")} beta>
				{t("pageDescription")}
			</PageTitle>

			{/* Toolbar: search on the left, actions on the right, as the routing rules and projects lists lay it out. */}
			<div className="flex flex-wrap items-center gap-3">
				<div className="relative max-w-sm flex-1">
					<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
					<Input
						aria-label={tc("searchEntities", { entity })}
						placeholder={tc("searchEntities", { entity })}
						value={search}
						onChange={(event) => void setSearch(event.target.value || null)}
						className="pl-9"
						data-testid="model-routers-search-input"
					/>
				</div>
				<div className="flex items-center gap-2 sm:ml-auto">
					<TrafficPeriodPicker filter={trafficFilter} testId="model-routers-filter-daterange" />
					{canCreate && (
						<Button type="button" className="h-9" onClick={onCreate} data-testid="model-router-create-button">
							<Plus className="size-4" />
							{t("newRouter")}
						</Button>
					)}
				</div>
			</div>

			<div className="overflow-hidden rounded-sm border">
				{/* Laid out fixed with a width per column, as the projects table is: the content
				    columns share the width, and the switch sits in a narrow Active column beside the actions. */}
				<Table containerClassName="h-full overflow-auto" className="w-full min-w-[1000px] table-fixed">
					<TableHeader className="bg-muted sticky top-0 z-10">
						<TableRow className="bg-muted/50">
							<TableHead className="w-[24%] font-semibold">{t("columnName")}</TableHead>
							<TableHead className="w-[26%] font-semibold">{tcpx("stepClassifier")}</TableHead>
							<TableHead className="w-[15%] font-semibold">{trouting("scope")}</TableHead>
							<TableHead className="w-[11%] text-right font-semibold">{trouting("priority")}</TableHead>
							<TableHead className="w-[14%] text-right font-semibold">{t("columnRequests")}</TableHead>
							<TableHead className="w-[80px] font-semibold">{t("columnActive")}</TableHead>
							<TableHead className={`bg-muted sticky right-0 z-30 w-[56px] text-right font-semibold ${PIN_SHADOW_RIGHT}`} />
						</TableRow>
					</TableHeader>
					<TableBody>
						{isLoading ? (
							<TableRow>
								<TableCell colSpan={7} className="h-10">
									<div className="bg-muted h-2 w-32 animate-pulse rounded" />
								</TableCell>
							</TableRow>
						) : routers.length === 0 ? (
							<TableRow>
								<TableCell colSpan={7} className="h-24 text-center" data-testid="model-routers-empty">
									<p className="text-sm font-medium">{t("emptyTitle")}</p>
									<p className="text-muted-foreground text-sm">{t("emptyDescription")}</p>
								</TableCell>
							</TableRow>
						) : shown.length === 0 ? (
							<TableRow>
								<TableCell colSpan={7} className="text-muted-foreground h-24 text-center text-sm" data-testid="model-routers-no-match">
									{tc("noMatchingEntities", { entity })}
								</TableCell>
							</TableRow>
						) : (
							shown.map((router) => {
								const classifier = classifierSummary(router, tcpx);
								const requests = routerRequests(traffic.byRouter, router.id);
								return (
									<TableRow
										key={router.id}
										className="group hover:bg-muted/50 cursor-pointer transition-colors"
										onClick={() => onOpenRouter(router.id)}
										data-testid={`model-router-row-${router.id}`}
									>
										<TableCell className="font-medium">
											<div className="flex flex-col gap-1">
												<span className="max-w-xs truncate">{router.name}</span>
												{router.description && (
													<span className="text-muted-foreground max-w-md truncate text-xs">{router.description}</span>
												)}
											</div>
										</TableCell>
										<TableCell>
											<div className="flex flex-col gap-0.5">
												<span>{classifier.name}</span>
												<span className="text-muted-foreground text-xs">{classifier.model || t("overview.facts.notConfigured")}</span>
											</div>
										</TableCell>
										<TableCell>
											<Badge variant="secondary">{getScopeLabel(router.routing.scope)}</Badge>
										</TableCell>
										<TableCell className="text-right">
											<div className={`inline-block rounded px-2.5 py-1 text-xs font-medium ${getPriorityBadgeClass()}`}>
												{router.routing.priority}
											</div>
										</TableCell>
										<TableCell className="text-right font-mono tabular-nums">
											{requests !== undefined ? requests.toLocaleString(getLocale()) : <span className="text-muted-foreground">-</span>}
										</TableCell>
										<TableCell onClick={(event) => event.stopPropagation()}>
											<RouterEnabledSwitch router={router} disabled={!canUpdate} />
										</TableCell>
										<TableCell
											className={`group-hover:bg-muted dark:bg-card dark:group-hover:bg-muted sticky right-0 z-20 bg-white text-right ${PIN_SHADOW_RIGHT}`}
											onClick={(event) => event.stopPropagation()}
										>
											<div className="flex items-center justify-center">
												<RowActionsMenu
													id={router.id}
													testIdPrefix="model-router"
													ariaLabel={t("actionsAria", { name: router.name })}
													canUpdate={canUpdate}
													canDelete={canDelete}
													onEdit={() => setEditingId(router.id)}
													onDelete={() => setDeleting(router)}
												/>
											</div>
										</TableCell>
									</TableRow>
								);
							})
						)}
					</TableBody>
				</Table>
			</div>

			{editing && <RouterSheet key={editing.id} router={editing} onClose={() => setEditingId(null)} />}
			<DeleteConfirmDialog
				open={deleting !== null}
				onOpenChange={(open) => !open && setDeleting(null)}
				title={t("deleteTitle", { name: deleting?.name ?? "" })}
				description={t("deleteDescription")}
				isDeleting={isDeleting}
				onConfirm={confirmDelete}
			/>
		</div>
	);
}