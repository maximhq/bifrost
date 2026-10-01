import FullPageLoader from "@/components/fullPageLoader";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { getErrorMessage, useGetVirtualMCPsQuery } from "@/lib/store";
import { parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useEffect, useState } from "react";
import VirtualMCPSheet, { VirtualMCPSheetTarget } from "./views/virtualMcpSheet";
import VirtualMCPWizard from "./views/virtualMcpWizard";
import VirtualMCPsTable from "./views/virtualMcpsTable";

const PAGE_SIZE = 25;

export default function VirtualMCPsPage() {
	const [urlState, setUrlState] = useQueryStates(
		{
			q: parseAsString.withDefault(""),
			offset: parseAsInteger.withDefault(0),
		},
		{ history: "push" },
	);

	const [sheetTarget, setSheetTarget] = useState<VirtualMCPSheetTarget | null>(null);
	const [creating, setCreating] = useState(false);

	const debouncedSearch = useDebouncedValue(urlState.q, 300);

	const { data, isLoading, isFetching, isError, error } = useGetVirtualMCPsQuery({
		search: debouncedSearch || undefined,
		limit: PAGE_SIZE,
		offset: urlState.offset,
	});

	const totalCount = data?.total_count ?? 0;

	// Snap offset back if the total shrinks past the current page (e.g. a delete
	// removed the last row on the last page). Same logic as the sessions/VK lists.
	useEffect(() => {
		if (!data || urlState.offset < totalCount) return;
		setUrlState({ offset: totalCount === 0 ? 0 : Math.floor((totalCount - 1) / PAGE_SIZE) * PAGE_SIZE });
	}, [totalCount, urlState.offset, data, setUrlState]);

	// Full-page creation wizard, swapped in over the list. Fills the content height so the
	// footer stays pinned to the bottom.
	if (creating) {
		return (
			<div className="mx-auto flex h-full min-h-0 w-full max-w-7xl flex-col px-4 md:px-0">
				<VirtualMCPWizard onCancel={() => setCreating(false)} onDone={() => setCreating(false)} />
			</div>
		);
	}

	if (isLoading) return <FullPageLoader />;

	if (isError) {
		return (
			<div className="mx-auto w-full max-w-7xl px-4 md:px-0">
				<div className="border-destructive bg-destructive/10 text-destructive rounded-lg border p-6 text-sm">
					Failed to load Virtual MCPs: {getErrorMessage(error)}
				</div>
			</div>
		);
	}

	// Row-to-row navigation within the current page, mirroring the MCP catalog sheet.
	const editList = data?.virtual_mcps ?? [];
	const editIndex = sheetTarget?.mode === "edit" ? editList.findIndex((v) => v.id === sheetTarget.id) : -1;
	const navigateEdit = (direction: "prev" | "next") => {
		if (editIndex < 0) return;
		const next = editList[direction === "prev" ? editIndex - 1 : editIndex + 1];
		if (next) setSheetTarget({ mode: "edit", id: next.id });
	};

	return (
		<div className="mx-auto w-full max-w-7xl px-4 md:px-0">
			<VirtualMCPsTable
				virtualMcps={data?.virtual_mcps ?? []}
				totalCount={totalCount}
				isFetching={isFetching}
				search={urlState.q}
				onSearchChange={(value) => setUrlState({ q: value || null, offset: 0 })}
				hasActiveSearch={!!urlState.q}
				offset={urlState.offset}
				limit={PAGE_SIZE}
				onOffsetChange={(offset) => setUrlState({ offset })}
				onCreate={() => setCreating(true)}
				onEdit={(id) => setSheetTarget({ mode: "edit", id })}
			/>
			{sheetTarget && (
				<VirtualMCPSheet
					target={sheetTarget}
					onClose={() => setSheetTarget(null)}
					hasPrev={editIndex > 0}
					hasNext={editIndex >= 0 && editIndex < editList.length - 1}
					onNavigate={navigateEdit}
				/>
			)}
		</div>
	);
}