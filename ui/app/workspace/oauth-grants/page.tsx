import { Input } from "@/components/ui/input";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { getErrorMessage, useGetOAuth2GrantsQuery, useRevokeOAuth2GrantMutation } from "@/lib/store";
import type { OAuth2GrantRow } from "@/lib/store/apis/oauth2SessionsApi";
import { Loader2, Search } from "lucide-react";
import { parseAsArrayOf, parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import GrantsTable from "./views/grantsTable";
import { OAuthGrantFilters, OAuthGrantsFilterSidebar } from "./views/oauthGrantsFilterSidebar";
import RevokeGrantDialog from "./views/revokeGrantDialog";

const PAGE_SIZE = 50;

export default function OAuthGrantsPage() {
	const [urlState, setUrlState] = useQueryStates(
		{
			q: parseAsString.withDefault(""),
			bf_mode: parseAsArrayOf(parseAsString).withDefault([]),
			virtual_key_id: parseAsArrayOf(parseAsString).withDefault([]),
			user_id: parseAsArrayOf(parseAsString).withDefault([]),
			offset: parseAsInteger.withDefault(0),
		},
		{ history: "push" },
	);

	const debouncedSearch = useDebouncedValue(urlState.q, 300);

	const filters: OAuthGrantFilters = useMemo(
		() => ({ mode: urlState.bf_mode, virtual_key_id: urlState.virtual_key_id, user_id: urlState.user_id }),
		[urlState.bf_mode, urlState.virtual_key_id, urlState.user_id],
	);

	const setFilters = useCallback(
		(newFilters: OAuthGrantFilters) => {
			void setUrlState({
				bf_mode: newFilters.mode.length ? newFilters.mode : null,
				virtual_key_id: newFilters.virtual_key_id.length ? newFilters.virtual_key_id : null,
				user_id: newFilters.user_id.length ? newFilters.user_id : null,
				offset: 0,
			});
		},
		[setUrlState],
	);

	const { data, isLoading, isFetching, isError, error } = useGetOAuth2GrantsQuery({
		q: debouncedSearch || undefined,
		bf_mode: filters.mode.length ? filters.mode : undefined,
		virtual_key_id: filters.virtual_key_id.length ? filters.virtual_key_id : undefined,
		user_id: filters.user_id.length ? filters.user_id : undefined,
		limit: PAGE_SIZE,
		offset: urlState.offset,
	});
	const [revokeGrant, { isLoading: revoking }] = useRevokeOAuth2GrantMutation();

	const [pendingDelete, setPendingDelete] = useState<OAuth2GrantRow | null>(null);
	const [pendingActionRowId, setPendingActionRowId] = useState<string | null>(null);

	const page = data?.sessions ?? [];
	const totalCount = data?.total_count ?? 0;
	const hasActiveFilters = !!urlState.q || filters.mode.length > 0 || filters.virtual_key_id.length > 0 || filters.user_id.length > 0;

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
	const handleOffsetChange = (offset: number) => setUrlState({ offset });

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

	const content = (
		<div className="flex h-full flex-col">
			<RevokeGrantDialog open={pendingDelete !== null} onOpenChange={(open) => !open && setPendingDelete(null)} onConfirm={confirmRevoke} />

			<div className="mb-4 flex items-center justify-between gap-4">
				<div>
					<h2 className="text-lg font-semibold tracking-tight">OAuth Grants</h2>
					<p className="text-muted-foreground text-sm">
						Active downstream OAuth grants issued to MCP clients that connected via the OAuth consent flow.
					</p>
				</div>
			</div>

			<div className="mb-4 flex items-center gap-3">
				<div className="relative max-w-sm min-w-[200px] flex-1">
					<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
					<Input
						data-testid="oauth-grants-search-input"
						aria-label="Search grants"
						placeholder="Search client, identity..."
						value={urlState.q}
						onChange={(e) => handleSearchChange(e.target.value)}
						className="pl-9"
					/>
				</div>
			</div>

			{isLoading ? (
				<div className="flex grow items-center justify-center">
					<Loader2 className="text-muted-foreground h-6 w-6 animate-spin" />
				</div>
			) : isError ? (
				<div className="border-destructive bg-destructive/10 text-destructive rounded-lg border p-6 text-sm">
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

	// No grants at all and no active filters/search: render full-width without
	// the filter sidebar, mirroring the MCP clients/sessions onboarding state.
	if (!isLoading && totalCount === 0 && !hasActiveFilters) {
		return <div className="mx-auto flex h-[calc(100dvh-50px)] w-full max-w-7xl flex-col">{content}</div>;
	}

	return (
		<div className="dark:bg-card no-padding-parent no-border-parent h-[calc(100dvh_-_16px)]">
			<div className="bg-background flex h-full w-full grow gap-3">
				<OAuthGrantsFilterSidebar filters={filters} onFiltersChange={setFilters} />
				<div className="bg-card h-full w-full overflow-hidden rounded-l-md">
					<div className="flex h-full flex-col p-4">{content}</div>
				</div>
			</div>
		</div>
	);
}
