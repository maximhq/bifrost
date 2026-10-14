import VirtualKeysTable from "@/app/workspace/virtual-keys/views/virtualKeysTable";
import FullPageLoader from "@/components/fullPageLoader";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { parseAsSafeString } from "@/lib/queryParamsParser";
import { getErrorMessage, useGetVirtualKeysQuery } from "@/lib/store";
import { isValidVirtualKeyMetadataFilterKey } from "@/lib/utils/virtualKeyMetadata";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useEffect, useRef } from "react";
import { toast } from "sonner";

const POLLING_INTERVAL = 5000;
const PAGE_SIZE = 25;

export default function GovernanceVirtualKeysPage() {
	const hasVirtualKeysAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.View);
	const shownErrorsRef = useRef(new Set<string>());

	const [urlState, setUrlState] = useQueryStates(
		{
			search: parseAsSafeString.withDefault(""),
			customer_id: parseAsString.withDefault(""),
			team_id: parseAsString.withDefault(""),
			user_id: parseAsString.withDefault(""),
			metadata_key: parseAsString.withDefault(""),
			metadata_value: parseAsString.withDefault(""),
			offset: parseAsInteger.withDefault(0),
			sort_by: parseAsString.withDefault(""),
			order: parseAsString.withDefault(""),
			selected_vk: parseAsString.withDefault(""),
		},
		{ history: "push" },
	);

	const debouncedSearch = useDebouncedValue(urlState.search, 300);

	// A metadata key from the URL (a shared or hand-edited link) is checked with the server's rule
	// before it is sent: an invalid one would make every list and export request fail with 400.
	// It is dropped instead, and a banner offers to clear it.
	const metadataKeyInvalid = urlState.metadata_key !== "" && !isValidVirtualKeyMetadataFilterKey(urlState.metadata_key);
	const activeMetadataKey = metadataKeyInvalid ? "" : urlState.metadata_key;

	const {
		data: virtualKeysData,
		error: vkError,
		isLoading: vkLoading,
	} = useGetVirtualKeysQuery(
		{
			limit: PAGE_SIZE,
			offset: urlState.offset,
			search: debouncedSearch || undefined,
			customer_id: urlState.customer_id || undefined,
			team_id: urlState.team_id || undefined,
			user_id: urlState.user_id || undefined,
			metadata: activeMetadataKey ? { [activeMetadataKey]: urlState.metadata_value } : undefined,
			sort_by: (urlState.sort_by as "name" | "budget_spent" | "created_at" | "status") || undefined,
			order: (urlState.order as "asc" | "desc") || undefined,
		},
		{
			skip: !hasVirtualKeysAccess,
			pollingInterval: POLLING_INTERVAL,
		},
	);

	const vkTotal = virtualKeysData?.total_count ?? 0;

	// Snap offset back when total shrinks past current page (e.g. delete last item on last page)
	useEffect(() => {
		if (!virtualKeysData || urlState.offset < vkTotal) return;
		setUrlState({
			offset: vkTotal === 0 ? 0 : Math.floor((vkTotal - 1) / PAGE_SIZE) * PAGE_SIZE,
		});
	}, [vkTotal, urlState.offset]);

	const isLoading = vkLoading;

	useEffect(() => {
		if (!vkError) {
			shownErrorsRef.current.clear();
			return;
		}
		const errorKey = `${!!vkError}`;
		if (shownErrorsRef.current.has(errorKey)) return;
		shownErrorsRef.current.add(errorKey);
		toast.error(`Failed to load virtual keys: ${getErrorMessage(vkError)}`);
	}, [vkError]);

	if (isLoading) {
		return <FullPageLoader />;
	}

	const handleSearchChange = (value: string) => {
		setUrlState({ search: value || null, offset: 0 });
	};

	const handleCustomerFilterChange = (value: string) => {
		setUrlState({ customer_id: value || null, offset: 0 });
	};

	const handleTeamFilterChange = (value: string) => {
		setUrlState({ team_id: value || null, offset: 0 });
	};

	const handleUserFilterChange = (value: string) => {
		setUrlState({ user_id: value || null, offset: 0 });
	};

	const handleMetadataFilterChange = (key: string, value: string) => {
		setUrlState({ metadata_key: key || null, metadata_value: key ? value : null, offset: 0 });
	};

	const handleOffsetChange = (newOffset: number) => {
		setUrlState({ offset: newOffset });
	};

	const handleSortChange = (newSortBy: string, newOrder: string) => {
		setUrlState({
			sort_by: newSortBy || null,
			order: newOrder || null,
			offset: 0,
		});
	};

	const handleSelectedVkChange = (id: string, options?: { offset?: number }) => {
		const update: Record<string, string | number | null> = {
			selected_vk: id || null,
		};
		if (options?.offset !== undefined) {
			update.offset = options.offset;
		}
		setUrlState(update);
	};

	return (
		<div className="no-padding-parent mx-auto flex h-[calc(var(--app-content-viewport)_-_var(--app-bottom-padding))] min-h-0 w-full flex-col overflow-hidden p-4">
			{metadataKeyInvalid && (
				<Alert variant="warning" className="mb-3" data-testid="vk-metadata-filter-invalid-alert">
					<AlertDescription className="flex items-center justify-between gap-3">
						<span>
							The metadata filter key in this link is not valid, so it is not applied. Keys use 1-256 letters, digits, &quot;.&quot;,
							&quot;_&quot; or &quot;-&quot;.
						</span>
						<Button
							size="sm"
							variant="outline"
							data-testid="vk-metadata-filter-invalid-clear-btn"
							onClick={() => handleMetadataFilterChange("", "")}
						>
							Clear filter
						</Button>
					</AlertDescription>
				</Alert>
			)}
			<VirtualKeysTable
				virtualKeys={virtualKeysData?.virtual_keys || []}
				totalCount={virtualKeysData?.total_count || 0}
				search={urlState.search}
				debouncedSearch={debouncedSearch}
				onSearchChange={handleSearchChange}
				customerFilter={urlState.customer_id}
				onCustomerFilterChange={handleCustomerFilterChange}
				teamFilter={urlState.team_id}
				onTeamFilterChange={handleTeamFilterChange}
				userFilter={urlState.user_id}
				onUserFilterChange={handleUserFilterChange}
				metadataFilterKey={activeMetadataKey}
				metadataFilterValue={urlState.metadata_value}
				onMetadataFilterChange={handleMetadataFilterChange}
				offset={urlState.offset}
				limit={PAGE_SIZE}
				onOffsetChange={handleOffsetChange}
				sortBy={urlState.sort_by}
				order={urlState.order}
				onSortChange={handleSortChange}
				selectedVkId={urlState.selected_vk}
				onSelectedVkChange={handleSelectedVkChange}
			/>
		</div>
	);
}