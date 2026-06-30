import { getErrorMessage, useGetOAuth2GrantsQuery, useRevokeOAuth2GrantMutation } from "@/lib/store";
import type { OAuth2GrantRow } from "@/lib/store/apis/oauth2SessionsApi";
import { Loader2 } from "lucide-react";
import { useEffect, useState } from "react";
import { toast } from "sonner";
import GrantsFilterBar from "./views/grantsFilterBar";
import GrantsTable from "./views/grantsTable";
import RevokeGrantDialog from "./views/revokeGrantDialog";

const PAGE_SIZE = 50;

export default function OAuthGrantsPage() {
	const { data, isLoading, isFetching, isError, error } = useGetOAuth2GrantsQuery();
	const [revokeGrant, { isLoading: revoking }] = useRevokeOAuth2GrantMutation();

	const [search, setSearch] = useState("");
	const [modeFilter, setModeFilter] = useState<string[]>([]);
	const [offset, setOffset] = useState(0);
	const [pendingDelete, setPendingDelete] = useState<OAuth2GrantRow | null>(null);
	const [pendingActionRowId, setPendingActionRowId] = useState<string | null>(null);

	const allGrants = data?.sessions ?? [];
	const filtered = allGrants.filter((g) => {
		const matchesMode = modeFilter.length === 0 || modeFilter.includes(g.bf_mode);
		const q = search.toLowerCase();
		const matchesSearch =
			!q ||
			g.client_name?.toLowerCase().includes(q) ||
			g.client_id.toLowerCase().includes(q) ||
			(g.bf_sub_display ?? g.bf_sub).toLowerCase().includes(q);
		return matchesMode && matchesSearch;
	});
	const totalCount = filtered.length;
	const page = filtered.slice(offset, offset + PAGE_SIZE);
	const hasActiveFilters = !!search || modeFilter.length > 0;

	// Snap the offset back into range when the filtered count shrinks (e.g. a
	// revoke removes the last row on the last page). Without this the page goes
	// blank with the paginator and clear-filters affordances both hidden.
	useEffect(() => {
		if (totalCount === 0) {
			if (offset !== 0) setOffset(0);
			return;
		}
		const maxOffset = Math.floor((totalCount - 1) / PAGE_SIZE) * PAGE_SIZE;
		if (offset > maxOffset) setOffset(maxOffset);
	}, [offset, totalCount]);

	const clearFilters = () => {
		setSearch("");
		setModeFilter([]);
		setOffset(0);
	};

	const confirmRevoke = async () => {
		if (!pendingDelete) return;
		const row = pendingDelete;
		setPendingDelete(null);
		setPendingActionRowId(row.id);
		try {
			await revokeGrant(row.id).unwrap();
			toast.success("Grant revoked");
		} catch (err) {
			toast.error("Failed to revoke grant", { description: getErrorMessage(err) });
		} finally {
			setPendingActionRowId(null);
		}
	};

	return (
		<div className="mx-auto w-full max-w-7xl space-y-4">
			<RevokeGrantDialog
				open={pendingDelete !== null}
				onOpenChange={(open) => !open && setPendingDelete(null)}
				onConfirm={confirmRevoke}
			/>

			<div className="flex items-center justify-between gap-4">
				<div>
					<h2 className="text-lg font-semibold tracking-tight">OAuth Grants</h2>
					<p className="text-muted-foreground text-sm">
						Active downstream OAuth grants issued to MCP clients that connected
						via the OAuth consent flow.
					</p>
				</div>
			</div>

			<GrantsFilterBar
				search={search}
				onSearchChange={(value) => { setSearch(value); setOffset(0); }}
				modeFilter={modeFilter}
				onModeChange={(value) => { setModeFilter(value); setOffset(0); }}
				hasActiveFilters={hasActiveFilters}
				onClearFilters={clearFilters}
			/>

			{isLoading ? (
				<div className="flex items-center justify-center py-16">
					<Loader2 className="text-muted-foreground h-6 w-6 animate-spin" />
				</div>
			) : isError ? (
				<div className="rounded-lg border border-destructive bg-destructive/10 p-6 text-sm text-destructive">
					Failed to load OAuth grants: {getErrorMessage(error)}
				</div>
			) : (
				<GrantsTable
					rows={page}
					totalCount={totalCount}
					offset={offset}
					pageSize={PAGE_SIZE}
					onOffsetChange={setOffset}
					isFetching={isFetching}
					hasActiveFilters={hasActiveFilters}
					revoking={revoking}
					pendingActionRowId={pendingActionRowId}
					onRevoke={setPendingDelete}
				/>
			)}
		</div>
	);
}
