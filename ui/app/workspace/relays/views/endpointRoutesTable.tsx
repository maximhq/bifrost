import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alertDialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "@/components/ui/dropdownMenu";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { getErrorMessage, useUpdateRelayMutation } from "@/lib/store";
import type { Relay } from "@/lib/types/relays";
import { cn } from "@/lib/utils";
import type { OpenAPIImport } from "@/lib/utils/openapiImport";
import { cleanUsage, describePricing, mergeUsage, routeBillableByDefault, toRelayForm } from "@/lib/utils/relayForm";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { EllipsisIcon, FileUpIcon, PencilIcon, PlusIcon, TrashIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { HeaderActionButton } from "../fragments/headerActionButton";
import { withRoutes, type RouteForm } from "../routes.utils";
import { OpenAPIImportSheet } from "../sheets/openapiImportSheet";
import { RouteSheet } from "../sheets/routeSheet";

function RouteActionsMenu({
	index,
	hasUpdateAccess,
	onEdit,
	onDelete,
}: {
	index: number;
	hasUpdateAccess: boolean;
	onEdit: () => void;
	onDelete: () => void;
}) {
	const { t } = useTranslation("config");
	return (
		<DropdownMenu>
			<DropdownMenuTrigger asChild>
				<Button variant="ghost" aria-label={t("relays.routes.actionsAria")} data-testid={`relay-route-actions-${index}`}>
					<EllipsisIcon className="h-5 w-5" aria-hidden="true" />
				</Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent align="end">
				<DropdownMenuItem onSelect={onEdit} disabled={!hasUpdateAccess} data-testid={`relay-route-edit-${index}`}>
					<PencilIcon className="mr-1 h-4 w-4" />
					{t("edit", { ns: "common" })}
				</DropdownMenuItem>
				<DropdownMenuSeparator />
				<DropdownMenuItem variant="destructive" onSelect={onDelete} disabled={!hasUpdateAccess} data-testid={`relay-route-remove-${index}`}>
					<TrashIcon className="mr-1 h-4 w-4" />
					{t("delete", { ns: "common" })}
				</DropdownMenuItem>
			</DropdownMenuContent>
		</DropdownMenu>
	);
}

/** The routes of a relay, with their own toolbar for adding and importing routes. */
export function EndpointRoutesTable({ endpoint }: { endpoint: Relay }) {
	const { t } = useTranslation("config");
	const canUpdate = useRbac(RbacResource.Relays, RbacOperation.Update);
	const [updateEndpoint, { isLoading: saving }] = useUpdateRelayMutation();
	const [editing, setEditing] = useState<{ index: number; route: RouteForm } | null>(null);
	const [deleting, setDeleting] = useState<number | null>(null);
	const [importOpen, setImportOpen] = useState(false);
	const [pendingImport, setPendingImport] = useState<{ routes: RouteForm[]; dropped: string[] } | null>(null);
	// Shown in saved order, which is the order requests are matched in; every save writes compareRoutes order.
	const routes = toRelayForm(endpoint).routes;
	const endpointUsage = cleanUsage(endpoint.usage, false);

	const save = async (next: RouteForm[], success: string) => {
		try {
			await updateEndpoint({ name: endpoint.name, data: withRoutes(endpoint, next) }).unwrap();
			toast.success(success);
			return true;
		} catch (e) {
			toast.error(t("relays.routes.toastUpdateFailed"), { description: getErrorMessage(e) });
			return false;
		}
	};

	// An import is staged for review: nothing replaces the saved routes until the person applies it.
	const stageImport = (result: OpenAPIImport) => {
		for (const w of result.warnings) toast.warning(w);
		if (result.routes.length === 0) {
			toast.error(t("relays.routes.toastNothingToImport"));
			return;
		}
		// A route the document still lists keeps what the import cannot express: its pricing
		// override, body overrides, job, billing, streaming and enabled settings.
		// Header matchers are part of a route's identity: AWS operations share POST / and differ only
		// by X-Amz-Target.
		const key = (method: string, path: string, headers: { name: string; value: string }[]) =>
			[
				`${method.toUpperCase()} ${path}`,
				...headers
					.filter((h) => h.name.trim())
					.map((h) => `${h.name.trim().toLowerCase()}: ${h.value.trim()}`)
					.sort(),
			].join(" ");
		const existing = new Map(routes.map((r) => [key(r.method, r.path, r.headers), r]));
		const imported: RouteForm[] = result.routes.map((r) => {
			const headers = Object.entries(r.headers ?? {}).map(([name, value]) => ({ name, value }));
			return { ...existing.get(key(r.method, r.path, headers)), method: r.method, path: r.path, headers };
		});
		const kept = new Set(imported.map((r) => key(r.method, r.path, r.headers)));
		const dropped = routes.filter(
			(r) => !kept.has(key(r.method, r.path, r.headers)) && (r.job || cleanUsage(r.usage, true) || r.body_overrides?.some((o) => o.name)),
		);
		setPendingImport({ routes: imported, dropped: dropped.map((r) => key(r.method, r.path, r.headers)) });
	};

	return (
		<section className="@container/card-header flex w-full flex-col gap-3 md:min-h-64 md:flex-1" data-testid="relay-routes">
			<div className="flex flex-col gap-3 md:flex-row md:items-end md:justify-between">
				<div className="min-w-0">
					<h3 className="flex items-center gap-2 text-base font-semibold">
						{t("relays.routes.title")}
						<Badge variant="secondary" className="font-normal" data-testid="relay-routes-count">
							{routes.length}
						</Badge>
					</h3>
					<p className="text-muted-foreground mt-0.5 text-sm">{t("relays.routes.description")}</p>
				</div>
				<div className="flex shrink-0 items-center gap-2">
					{canUpdate && (
						<HeaderActionButton
							variant="outline"
							icon={<FileUpIcon className="h-4 w-4" />}
							label={t("relays.routes.importOpenapi")}
							aria-label={t("relays.routes.importOpenapi")}
							disabled={saving}
							onClick={() => setImportOpen(true)}
							data-testid="relay-routes-import"
						/>
					)}
					{canUpdate && (
						<HeaderActionButton
							icon={<PlusIcon className="h-4 w-4" />}
							label={t("relays.routes.addRoute")}
							aria-label={t("relays.routes.addRoute")}
							disabled={saving}
							onClick={() => setEditing({ index: -1, route: { method: "POST", path: "/", headers: [], enabled: true } })}
							data-testid="relay-routes-add"
						/>
					)}
				</div>
			</div>
			<div className="flex w-full flex-col overflow-hidden rounded-sm border md:min-h-0">
				<Table
					className="w-full min-w-[640px] table-fixed"
					containerClassName="custom-scrollbar md:min-h-0 md:overflow-y-auto"
					data-testid="relay-routes-table"
				>
					<colgroup>
						<col className="w-24" />
						<col className="w-[36%]" />
						<col className="w-[32%]" />
						<col className="w-[12%]" />
						<col className="w-[10%]" />
					</colgroup>
					<TableHeader className="bg-muted sticky top-0 z-10">
						<TableRow>
							<TableHead>{t("relays.routes.columns.method")}</TableHead>
							<TableHead>{t("relays.routes.columns.route")}</TableHead>
							<TableHead>{t("relays.routes.columns.price")}</TableHead>
							<TableHead>{t("relays.routes.columns.enabled")}</TableHead>
							<TableHead className="text-right" />
						</TableRow>
					</TableHeader>
					<TableBody>
						{routes.length === 0 && (
							<TableRow data-testid="relay-routes-empty">
								<TableCell colSpan={5} className="py-6 text-center">
									{endpoint.skip_route_check ? t("relays.routes.emptyAnyPath") : t("relays.routes.empty")}
								</TableCell>
							</TableRow>
						)}
						{routes.map((route, index) => {
							const billable = route.billable ?? routeBillableByDefault(route.method);
							const enabled = route.enabled ?? true;
							const override = cleanUsage(route.usage, true);
							return (
								<TableRow
									key={`${route.method}-${route.path}-${index}`}
									data-testid={`relay-route-row-${index}`}
									className={cn("hover:bg-muted/40 text-sm transition-colors", !enabled && "opacity-60")}
								>
									<TableCell>
										<Badge variant="secondary" className="font-mono">
											{route.method}
										</Badge>
									</TableCell>
									<TableCell className="overflow-hidden">
										<div className="flex min-w-0 items-center gap-2">
											<span className="truncate font-mono text-sm">{route.path}</span>
											{route.job && <Badge variant="outline">{t("relays.routes.jobBadge")}</Badge>}
											{!!route.body_overrides?.some((o) => o.name) && (
												<Badge variant="outline" data-testid={`relay-route-body-badge-${index}`}>
													{t("relays.routes.bodyBadge")}
												</Badge>
											)}
										</div>
										{route.headers
											.filter((h) => h.name)
											.map((h) => (
												<div key={h.name} className="text-muted-foreground truncate font-mono text-xs">
													{h.name}: {h.value}
												</div>
											))}
									</TableCell>
									<TableCell className="truncate text-sm">
										{billable ? (
											describePricing(mergeUsage(endpointUsage, override), endpoint.ignore_upstream_cost)
										) : (
											<span className="text-muted-foreground">{t("relays.routes.free")}</span>
										)}
										{override && billable && (
											<Badge variant="outline" className="ml-2">
												{t("relays.routes.overrideBadge")}
											</Badge>
										)}
									</TableCell>
									<TableCell>
										<Switch
											aria-label={t("relays.routes.enableAria", { method: route.method, path: route.path })}
											size="md"
											checked={enabled}
											disabled={!canUpdate || saving}
											data-testid={`relay-route-enabled-switch-${index}`}
											onAsyncCheckedChange={async (checked) => {
												await save(
													routes.map((r, i) => (i === index ? { ...r, enabled: checked } : r)),
													checked ? t("relays.routes.toastEnabled") : t("relays.routes.toastDisabled"),
												);
											}}
										/>
									</TableCell>
									<TableCell className="text-right">
										<RouteActionsMenu
											index={index}
											hasUpdateAccess={canUpdate && !saving}
											onEdit={() => setEditing({ index, route: structuredClone(route) })}
											onDelete={() => setDeleting(index)}
										/>
									</TableCell>
								</TableRow>
							);
						})}
					</TableBody>
				</Table>
			</div>

			<RouteSheet
				show={!!editing}
				route={editing?.route ?? null}
				isNew={editing?.index === -1}
				endpointUsage={endpointUsage}
				endpointBodyOverridePaths={Object.keys(endpoint.body_overrides ?? {})}
				ignoreUpstreamCost={endpoint.ignore_upstream_cost}
				saving={saving}
				disabled={!canUpdate}
				onCancel={() => setEditing(null)}
				onSave={async (route) => {
					if (!editing) return;
					const next = editing.index < 0 ? [...routes, route] : routes.map((r, i) => (i === editing.index ? route : r));
					if (await save(next, editing.index < 0 ? t("relays.routes.toastAdded") : t("relays.routes.toastUpdated"))) setEditing(null);
				}}
			/>
			<AlertDialog open={deleting !== null}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>{t("relays.routes.deleteTitle")}</AlertDialogTitle>
						<AlertDialogDescription>{t("relays.routes.deleteDescription")}</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter className="pt-4">
						<AlertDialogCancel onClick={() => setDeleting(null)} disabled={saving}>
							{t("cancel", { ns: "common" })}
						</AlertDialogCancel>
						<AlertDialogAction
							disabled={saving || !canUpdate}
							data-testid="relay-route-delete-confirm"
							onClick={async () => {
								if (deleting === null) return;
								if (
									await save(
										routes.filter((_, i) => i !== deleting),
										t("relays.routes.toastDeleted"),
									)
								)
									setDeleting(null);
							}}
						>
							{t("delete", { ns: "common" })}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
			<OpenAPIImportSheet open={importOpen} onClose={() => setImportOpen(false)} onImport={stageImport} />
			<AlertDialog open={pendingImport !== null} onOpenChange={(o) => !o && setPendingImport(null)}>
				<AlertDialogContent data-testid="relay-import-review">
					<AlertDialogHeader>
						<AlertDialogTitle>{t("relays.routes.importReview.title")}</AlertDialogTitle>
						<AlertDialogDescription asChild>
							<div className="space-y-2">
								<p>{t("relays.routes.importReview.body", { count: pendingImport?.routes.length ?? 0 })}</p>
								{pendingImport && pendingImport.dropped.length > 0 && (
									<p className="text-destructive">
										{t("relays.routes.importReview.dropped", {
											count: pendingImport.dropped.length,
											routes: pendingImport.dropped.join(", "),
										})}
									</p>
								)}
							</div>
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel data-testid="relay-import-review-cancel">{t("relays.routes.importReview.keep")}</AlertDialogCancel>
						<AlertDialogAction
							data-testid="relay-import-review-apply"
							disabled={saving}
							onClick={async (e) => {
								// A Radix action closes the dialog on click; keep it open so a failed save keeps the staged import.
								e.preventDefault();
								if (!pendingImport) return;
								const imported = pendingImport.routes;
								if (await save(imported, t("relays.routes.toastImported", { count: imported.length }))) setPendingImport(null);
							}}
						>
							{t("relays.routes.importReview.apply")}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</section>
	);
}