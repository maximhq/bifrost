"use client";

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
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import {
	getErrorMessage,
	useDeletePricingOverrideMutation,
	useGetPricingOverridesQuery,
	useGetProvidersQuery,
	useGetVirtualKeysQuery,
} from "@/lib/store";
import { useGetAllKeysQuery } from "@/lib/store/apis/providersApi";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";
import { PricingOverride, PricingOverrideScopeKind } from "@/lib/types/governance";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { Input } from "@/components/ui/input";
import { ChevronLeft, ChevronRight, Edit, Plus, Search, Trash2 } from "lucide-react";
import { useSearchParams } from "next/navigation";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import PricingOverrideSheet from "./pricingOverrideSheet";
import { PricingOverridesEmptyState } from "./pricingOverridesEmptyState";

type ScopeFilter = "all" | PricingOverrideScopeKind;

function parseScopeKind(value: string | null): ScopeFilter {
	if (
		value === "global" ||
		value === "provider" ||
		value === "provider_key" ||
		value === "virtual_key" ||
		value === "virtual_key_provider" ||
		value === "virtual_key_provider_key"
	) {
		return value;
	}
	return "all";
}

// Returns the top-level scope label: "Global" or the virtual key name.
function scopeLabel(override: PricingOverride, virtualKeyMap: Map<string, string>): string {
	const scopeKind = resolveScopeKind(override);
	if (override.virtual_key_id && scopeKind.startsWith("virtual_key")) {
		return "Virtual Key";
	}
	return "Global";
}

// Returns the key label for the override, or "-" when no specific key is scoped.
function keyLabel(override: PricingOverride, keyLabelMap: Map<string, string>): string {
	if (!override.provider_key_id) {
		if (!override.provider_id) return "-";
		return "All Keys"
	};
	return keyLabelMap.get(override.provider_key_id) || override.provider_key_id;
}

// Returns the provider label for the override, or "-" if not applicable.
function providerLabel(override: PricingOverride, providerMap: Map<string, string>, keyProviderMap: Map<string, string>): string {
	const scopeKind = resolveScopeKind(override);
	switch (scopeKind) {
		case "provider":
		case "virtual_key_provider":
			return providerMap.get(override.provider_id || "") || override.provider_id || "-";
		case "provider_key":
		case "virtual_key_provider_key": {
			const keyID = override.provider_key_id || "";
			return providerMap.get(keyProviderMap.get(keyID) || "") || keyProviderMap.get(keyID) || "-";
		}
		default:
			return "-";
	}
}

function resolveScopeKind(override: PricingOverride): PricingOverrideScopeKind {
	if (
		override.scope_kind === "global" ||
		override.scope_kind === "provider" ||
		override.scope_kind === "provider_key" ||
		override.scope_kind === "virtual_key" ||
		override.scope_kind === "virtual_key_provider" ||
		override.scope_kind === "virtual_key_provider_key"
	) {
		return override.scope_kind;
	}
	if (override.virtual_key_id) {
		if (override.provider_key_id) return "virtual_key_provider_key";
		if (override.provider_id) return "virtual_key_provider";
		return "virtual_key";
	}
	if (override.provider_key_id) return "provider_key";
	if (override.provider_id) return "provider";
	return "global";
}

const PAGE_SIZE = 25;

export default function ScopedPricingOverridesView() {
	const searchParams = useSearchParams();

	const [scopeKind, setScopeKind] = useState<ScopeFilter>(() => parseScopeKind(searchParams.get("scope_kind")));
	const [virtualKeyID, setVirtualKeyID] = useState(() => (searchParams.get("virtual_key_id") || "").trim());
	const [providerID, setProviderID] = useState(() => (searchParams.get("provider_id") || "").trim());
	const [providerKeyID, setProviderKeyID] = useState(() => (searchParams.get("provider_key_id") || "").trim());

	const [search, setSearch] = useState("");
	const [offset, setOffset] = useState(0);
	const debouncedSearch = useDebouncedValue(search, 300);

	useEffect(() => {
		setScopeKind(parseScopeKind(searchParams.get("scope_kind")));
		setVirtualKeyID((searchParams.get("virtual_key_id") || "").trim());
		setProviderID((searchParams.get("provider_id") || "").trim());
		setProviderKeyID((searchParams.get("provider_key_id") || "").trim());
	}, [searchParams]);

	// Reset to first page when filters or search change
	useEffect(() => {
		setOffset(0);
	}, [scopeKind, virtualKeyID, providerID, providerKeyID, debouncedSearch]);

	const queryArgs = useMemo(() => ({
		scopeKind: scopeKind === "all" ? undefined : scopeKind,
		virtualKeyID: virtualKeyID || undefined,
		providerID: providerID || undefined,
		providerKeyID: providerKeyID || undefined,
		limit: PAGE_SIZE,
		offset,
		search: debouncedSearch || undefined,
	}), [scopeKind, virtualKeyID, providerID, providerKeyID, offset, debouncedSearch]);

	const { data, isLoading, error } = useGetPricingOverridesQuery(queryArgs);

	// Snap offset back when total shrinks past current page
	const totalCount = data?.total_count ?? 0;
	useEffect(() => {
		if (!data || offset < totalCount) return;
		setOffset(totalCount === 0 ? 0 : Math.floor((totalCount - 1) / PAGE_SIZE) * PAGE_SIZE);
	}, [totalCount, offset]);
	const { data: providersData } = useGetProvidersQuery();
	const { data: virtualKeysData } = useGetVirtualKeysQuery();
	const { data: allKeysData = [] } = useGetAllKeysQuery();
	const [deleteOverride, { isLoading: isDeleting }] = useDeletePricingOverrideMutation();

	useEffect(() => {
		if (error) {
			toast.error("Failed to load pricing overrides", { description: getErrorMessage(error) });
		}
	}, [error]);

	const [isDrawerOpen, setIsDrawerOpen] = useState(false);
	const [editingOverride, setEditingOverride] = useState<PricingOverride | null>(null);
	const [deleteTarget, setDeleteTarget] = useState<PricingOverride | null>(null);

	const rows = data?.pricing_overrides ?? [];
	const providers = useMemo(() => providersData ?? [], [providersData]);
	const virtualKeys = useMemo(() => virtualKeysData?.virtual_keys ?? [], [virtualKeysData]);

	const providerMap = useMemo(() => new Map<string, string>(providers.map((provider) => [provider.name, provider.name])), [providers]);
	const providerKeyOptions = useMemo(
		() =>
			allKeysData.map((key) => ({
				id: key.key_id,
				label: key.name || key.key_id,
				providerName: key.provider,
			})),
		[allKeysData],
	);
	const providerKeyProviderMap = useMemo(
		() => new Map<string, string>(providerKeyOptions.map((key) => [key.id, key.providerName])),
		[providerKeyOptions],
	);
	const providerKeyLabelMap = useMemo(
		() => new Map<string, string>(providerKeyOptions.map((key) => [key.id, key.label])),
		[providerKeyOptions],
	);
	const virtualKeyMap = useMemo(() => new Map<string, string>(virtualKeys.map((vk) => [vk.id, vk.name])), [virtualKeys]);

	const createScopeLock = useMemo(() => {
		if (scopeKind === "all") return undefined;
		return {
			scopeKind,
			virtualKeyID: virtualKeyID || undefined,
			providerID: providerID || undefined,
			providerKeyID: providerKeyID || undefined,
			label: `${scopeKind}${virtualKeyID || providerID || providerKeyID ? " (filtered)" : ""}`,
		};
	}, [scopeKind, virtualKeyID, providerID, providerKeyID]);

	const openCreateDrawer = () => {
		setEditingOverride(null);
		setIsDrawerOpen(true);
	};

	const openEditDrawer = (override: PricingOverride) => {
		setEditingOverride(override);
		setIsDrawerOpen(true);
	};

	const handleDeleteConfirm = async () => {
		if (!deleteTarget) return;
		try {
			await deleteOverride(deleteTarget.id).unwrap();
			toast.success("Pricing override deleted");
			setDeleteTarget(null);
		} catch (deleteError) {
			toast.error("Failed to delete pricing override", { description: getErrorMessage(deleteError) });
		}
	};

	const hasActiveFilters = debouncedSearch || scopeKind !== "all" || virtualKeyID || providerID || providerKeyID;

	if (!isLoading && !error && totalCount === 0 && !hasActiveFilters) {
		return (
			<>
				<PricingOverridesEmptyState onCreateClick={openCreateDrawer} />
				<PricingOverrideSheet
					open={isDrawerOpen}
					onOpenChange={setIsDrawerOpen}
					editingOverride={editingOverride}
					scopeLock={createScopeLock}
				/>
			</>
		);
	}

	return (
		<div className="space-y-4">
			<div className="flex items-center justify-between gap-4">
				<div>
					<h2 className="text-lg font-semibold tracking-tight">Pricing Overrides</h2>
					<p className="text-muted-foreground text-sm">Set custom rates for any model across global or virtual key scopes, optionally narrowed to a specific provider or key</p>
				</div>
				<Button data-testid="pricing-override-create-btn" onClick={openCreateDrawer} className="gap-2">
					<Plus className="h-4 w-4" />
					<span className="hidden sm:inline">New Override</span>
				</Button>
			</div>

			{/* Search */}
			<div className="relative max-w-sm">
				<Search className="text-muted-foreground absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2" />
				<Input
					aria-label="Search pricing overrides by name"
					placeholder="Search by name..."
					value={search}
					onChange={(e) => setSearch(e.target.value)}
					className="pl-9"
					data-testid="pricing-overrides-search-input"
				/>
			</div>

			<div className="rounded-sm border overflow-hidden">
				{isLoading ? (
					<div className="p-4 text-sm">Loading overrides...</div>
				) : error ? (
					<div className="p-4 text-sm text-red-500">Failed to load pricing overrides. Please try refreshing the page.</div>
				) : (
					<Table>
						<TableHeader>
							<TableRow className="bg-muted/50">
								<TableHead className="font-semibold">Name</TableHead>
								<TableHead className="font-semibold">Scope</TableHead>
								<TableHead className="font-semibold">Provider</TableHead>
								<TableHead className="font-semibold">Key</TableHead>
								<TableHead className="font-semibold">Model</TableHead>
								<TableHead className="w-[100px] text-right font-semibold">Actions</TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{rows.length === 0 ? (
								<TableRow>
									<TableCell colSpan={6} className="h-24 text-center">
										<span className="text-muted-foreground text-sm">No matching pricing overrides found.</span>
									</TableCell>
								</TableRow>
							) : rows.map((row) => (
								<TableRow key={row.id} className="hover:bg-muted/50 cursor-pointer transition-colors">
									<TableCell>{row.name || "-"}</TableCell>
									<TableCell>
										<Badge variant="secondary">{scopeLabel(row, virtualKeyMap)}</Badge>
									</TableCell>
									<TableCell>
										{(() => {
											const name = providerLabel(row, providerMap, providerKeyProviderMap);
											if (name === "-") return <span className="text-muted-foreground text-sm">-</span>;
											return (
												<div className="flex items-center gap-1.5">
													<RenderProviderIcon provider={name as ProviderIconType} size="sm" className="h-4 w-4 shrink-0" />
													<span className="text-sm">{getProviderLabel(name)}</span>
												</div>
											);
										})()}
									</TableCell>
									<TableCell>{keyLabel(row, providerKeyLabelMap)}</TableCell>
									<TableCell>{row.pattern}</TableCell>
									<TableCell className="text-right" onClick={(e) => e.stopPropagation()}>
										<div className="flex items-center justify-end gap-2">
											<Button data-testid={`pricing-override-edit-btn-${row.id}`} variant="ghost" size="sm" onClick={() => openEditDrawer(row)} aria-label="Edit pricing override">
												<Edit className="h-4 w-4" />
											</Button>
											<Button data-testid={`pricing-override-delete-btn-${row.id}`} variant="ghost" size="sm" onClick={() => setDeleteTarget(row)} aria-label="Delete pricing override">
												<Trash2 className="h-4 w-4" />
											</Button>
										</div>
									</TableCell>
								</TableRow>
							))}
						</TableBody>
					</Table>
				)}
			</div>

			{/* Pagination */}
			{totalCount > 0 && (
				<div className="flex items-center justify-between px-2">
					<p className="text-muted-foreground text-sm">
						Showing {offset + 1}-{Math.min(offset + PAGE_SIZE, totalCount)} of {totalCount}
					</p>
					<div className="flex gap-2">
						<Button
							variant="outline"
							size="sm"
							disabled={offset === 0}
							onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
							data-testid="pricing-overrides-pagination-prev-btn"
						>
							<ChevronLeft className="mr-1 h-4 w-4" />
							Previous
						</Button>
						<Button
							variant="outline"
							size="sm"
							disabled={offset + PAGE_SIZE >= totalCount}
							onClick={() => setOffset(offset + PAGE_SIZE)}
							data-testid="pricing-overrides-pagination-next-btn"
						>
							Next
							<ChevronRight className="ml-1 h-4 w-4" />
						</Button>
					</div>
				</div>
			)}

			<PricingOverrideSheet
				open={isDrawerOpen}
				onOpenChange={setIsDrawerOpen}
				editingOverride={editingOverride}
				scopeLock={createScopeLock}
			/>

			<AlertDialog open={!!deleteTarget} onOpenChange={(open) => (!open ? setDeleteTarget(null) : undefined)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Delete Pricing Override</AlertDialogTitle>
						<AlertDialogDescription>
							Are you sure you want to delete &quot;{deleteTarget?.name}&quot;? This action cannot be undone.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel data-testid="pricing-override-delete-cancel-btn" disabled={isDeleting}>Cancel</AlertDialogCancel>
						<AlertDialogAction
							data-testid="pricing-override-delete-confirm-btn"
							onClick={(e) => {
								e.preventDefault();
								void handleDeleteConfirm();
							}}
							disabled={isDeleting}
							className="bg-destructive hover:bg-destructive/90"
						>
							{isDeleting ? "Deleting..." : "Delete"}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</div>
	);
}
