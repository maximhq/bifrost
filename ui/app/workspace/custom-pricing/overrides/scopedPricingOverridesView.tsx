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
import { RequestTypeLabels } from "@/lib/constants/logs";
import { PricingOverride, PricingOverrideScopeKind } from "@/lib/types/governance";
import { useSearchParams } from "next/navigation";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import PricingOverrideDrawer, { patchSummary } from "./pricingOverrideDrawer";

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

function scopeDisplay(
	override: PricingOverride,
	providerMap: Map<string, string>,
	keyMap: Map<string, string>,
	virtualKeyMap: Map<string, string>,
): string {
	const scopeKind = resolveScopeKind(override);
	switch (scopeKind) {
		case "global":
			return "Global";
		case "provider":
			return `Provider: ${providerMap.get(override.provider_id || "") || override.provider_id || "-"}`;
		case "provider_key":
			return `Provider key: ${keyMap.get(override.provider_key_id || "") || override.provider_key_id || "-"}`;
		case "virtual_key":
			return `Virtual key: ${virtualKeyMap.get(override.virtual_key_id || "") || override.virtual_key_id || "-"}`;
		case "virtual_key_provider":
			return `VK+Provider: ${virtualKeyMap.get(override.virtual_key_id || "") || override.virtual_key_id || "-"} / ${providerMap.get(override.provider_id || "") || override.provider_id || "-"}`;
		case "virtual_key_provider_key":
			return `VK+Provider+Key: ${virtualKeyMap.get(override.virtual_key_id || "") || override.virtual_key_id || "-"} / ${providerMap.get(override.provider_id || "") || override.provider_id || "-"} / ${keyMap.get(override.provider_key_id || "") || override.provider_key_id || "-"}`;
		default:
			return "Global";
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

export default function ScopedPricingOverridesView() {
	const searchParams = useSearchParams();

	const [scopeKind, setScopeKind] = useState<ScopeFilter>("all");
	const [virtualKeyID, setVirtualKeyID] = useState("");
	const [providerID, setProviderID] = useState("");
	const [providerKeyID, setProviderKeyID] = useState("");

	useEffect(() => {
		setScopeKind(parseScopeKind(searchParams.get("scope_kind")));
		setVirtualKeyID((searchParams.get("virtual_key_id") || "").trim());
		setProviderID((searchParams.get("provider_id") || "").trim());
		setProviderKeyID((searchParams.get("provider_key_id") || "").trim());
	}, [searchParams]);

	const queryArgs = useMemo(() => {
		if (scopeKind === "all" && !virtualKeyID && !providerID && !providerKeyID) return undefined;
		return {
			scopeKind: scopeKind === "all" ? undefined : scopeKind,
			virtualKeyID: virtualKeyID || undefined,
			providerID: providerID || undefined,
			providerKeyID: providerKeyID || undefined,
		};
	}, [scopeKind, virtualKeyID, providerID, providerKeyID]);

	const { data, isLoading, error } = useGetPricingOverridesQuery(queryArgs);
	const { data: providersData } = useGetProvidersQuery();
	const { data: virtualKeysData } = useGetVirtualKeysQuery();
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
			providers.flatMap((provider) =>
				(provider.keys || []).map((key) => ({
					id: key.id,
					label: key.name || key.id,
					providerName: provider.name,
				})),
			),
		[providers],
	);
	const providerKeyMap = useMemo(() => new Map<string, string>(providerKeyOptions.map((key) => [key.id, key.label])), [providerKeyOptions]);
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

	return (
		<div className="mt-6 space-y-4">
			<div className="flex items-start justify-between gap-4">
				<div>
					<h2 className="text-lg font-semibold tracking-tight">Pricing Overrides</h2>
				</div>
				<Button onClick={openCreateDrawer}>Create Override</Button>
			</div>

			<div className="rounded-sm border">
				{isLoading ? (
					<div className="p-4 text-sm">Loading overrides...</div>
				) : error ? (
					<div className="p-4 text-sm text-red-500">Failed to load pricing overrides. Please try refreshing the page.</div>
				) : rows.length === 0 ? (
					<div className="text-muted-foreground p-4 text-sm">No pricing overrides configured.</div>
				) : (
					<Table>
						<TableHeader>
							<TableRow>
								<TableHead>Name</TableHead>
								<TableHead>Scope</TableHead>
								<TableHead>Match Type</TableHead>
								<TableHead>Pattern</TableHead>
								<TableHead>Request Types</TableHead>
								<TableHead>Overrides</TableHead>
								<TableHead className="w-[140px]">Actions</TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{rows.map((row) => (
								<TableRow key={row.id}>
									<TableCell>{row.name || "-"}</TableCell>
									<TableCell>{scopeDisplay(row, providerMap, providerKeyMap, virtualKeyMap)}</TableCell>
									<TableCell>
										<Badge variant="outline">{row.match_type}</Badge>
									</TableCell>
									<TableCell>{row.pattern}</TableCell>
									<TableCell>
										{row.request_types && row.request_types.length > 0
											? row.request_types.map((rt) => RequestTypeLabels[rt as keyof typeof RequestTypeLabels] ?? rt).join(", ")
											: "All"}
									</TableCell>
									<TableCell>{patchSummary(row)}</TableCell>
									<TableCell>
										<div className="flex gap-2">
											<Button size="sm" variant="outline" onClick={() => openEditDrawer(row)}>
												Edit
											</Button>
											<Button size="sm" variant="destructive" onClick={() => setDeleteTarget(row)}>
												Delete
											</Button>
										</div>
									</TableCell>
								</TableRow>
							))}
						</TableBody>
					</Table>
				)}
			</div>

			<PricingOverrideDrawer
				open={isDrawerOpen}
				onOpenChange={setIsDrawerOpen}
				editingOverride={editingOverride}
				scopeLock={createScopeLock}
			/>

			<AlertDialog open={!!deleteTarget} onOpenChange={(open) => (!open ? setDeleteTarget(null) : undefined)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Delete pricing override?</AlertDialogTitle>
						<AlertDialogDescription>This action cannot be undone.</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel disabled={isDeleting}>Cancel</AlertDialogCancel>
						<AlertDialogAction onClick={handleDeleteConfirm} disabled={isDeleting}>
							Delete
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</div>
	);
}
