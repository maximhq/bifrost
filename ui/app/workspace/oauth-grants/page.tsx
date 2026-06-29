import { useDebouncedValue } from "@/hooks/useDebounce";
import { getErrorMessage, useGetOAuth2GrantsQuery, useRevokeOAuth2GrantMutation } from "@/lib/store";
import type { OAuth2GrantRow } from "@/lib/store/apis/oauth2SessionsApi";
import { Loader2 } from "lucide-react";
import { parseAsArrayOf, parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useEffect, useState } from "react";
import { toast } from "sonner";
import GrantsFilterBar from "./views/grantsFilterBar";
import GrantsTable from "./views/grantsTable";
import RevokeGrantDialog from "./views/revokeGrantDialog";

const PAGE_SIZE = 50;

export default function OAuthGrantsPage() {
	const [urlState, setUrlState] = useQueryStates(
		{
			q: parseAsString.withDefault(""),
			bf_mode: parseAsArrayOf(parseAsString).withDefault([]),
			offset: parseAsInteger.withDefault(0),
		},
		{ history: "push" },
	);

	const debouncedSearch = useDebouncedValue(urlState.q, 300);

	const { data, isLoading, isFetching, isError, error } = useGetOAuth2GrantsQuery({
		q: debouncedSearch || undefined,
		bf_mode: urlState.bf_mode.length ? urlState.bf_mode : undefined,
		limit: PAGE_SIZE,
		offset: urlState.offset,
	});
	const [revokeGrant, { isLoading: revoking }] = useRevokeOAuth2GrantMutation();

	const [pendingDelete, setPendingDelete] = useState<OAuth2GrantRow | null>(null);
	const [pendingActionRowId, setPendingActionRowId] = useState<string | null>(null);

	const page = data?.sessions ?? [];
	const totalCount = data?.total_count ?? 0;
	const hasActiveFilters = !!urlState.q || urlState.bf_mode.length > 0;

	// Snap the offset back into range when the total shrinks past the current
	// page (e.g. a revoke removes the last row on the last page). Without this
	// the page goes blank with the paginator and clear-filters affordances both
	// hidden. Mirrors the MCP sessions page.
	useEffect(() => {
		if (!data || urlState.offset < totalCount) return;
		setUrlState({
			offset: totalCount === 0 ? 0 : Math.floor((totalCount - 1) / PAGE_SIZE) * PAGE_SIZE,
		});
	}, [totalCount, urlState.offset, data, setUrlState]);

	const handleSearchChange = (value: string) => setUrlState({ q: value || null, offset: 0 });
	const handleModeChange = (value: string[]) => setUrlState({ bf_mode: value.length ? value : null, offset: 0 });
	const handleOffsetChange = (offset: number) => setUrlState({ offset });
	const clearFilters = () => setUrlState({ q: null, bf_mode: null, offset: 0 });

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
		<div className="mx-auto flex h-[calc(100dvh-50px)] w-full max-w-7xl flex-col">
			<RevokeGrantDialog
				open={pendingDelete !== null}
				onOpenChange={(open) => !open && setPendingDelete(null)}
				onConfirm={confirmRevoke}
			/>

			<div className="mb-4 flex items-center justify-between gap-4">
				<div>
					<h2 className="text-lg font-semibold tracking-tight">OAuth Grants</h2>
					<p className="text-muted-foreground text-sm">
						Active downstream OAuth grants issued to MCP clients that connected
						via the OAuth consent flow.
					</p>
				</div>
			</div>

			<div className="mb-4">
				<GrantsFilterBar
					search={urlState.q}
					onSearchChange={handleSearchChange}
					modeFilter={urlState.bf_mode}
					onModeChange={handleModeChange}
					hasActiveFilters={hasActiveFilters}
					onClearFilters={clearFilters}
				/>
			</div>

			{isLoading ? (
				<div className="flex grow items-center justify-center">
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
					offset={urlState.offset}
					pageSize={PAGE_SIZE}
					onOffsetChange={handleOffsetChange}
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
