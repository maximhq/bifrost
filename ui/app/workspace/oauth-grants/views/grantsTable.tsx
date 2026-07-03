// Results table for OAuth grants: one row per active downstream grant, with the
// bound identity, an approximate access-token expiry, created/last-used relative
// times, and a per-row actions menu. Owns the empty state and pagination; the
// page passes in the current page slice plus filter/revoke state.

import { Button } from "@/components/ui/button";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { PIN_SHADOW_RIGHT } from "@/components/table/columnPinning";
import type { OAuth2GrantRow } from "@/lib/store/apis/oauth2SessionsApi";
import {
	ChevronLeft,
	ChevronRight,
	Fingerprint,
	Info,
	KeyRound,
	UserRound,
} from "lucide-react";
import GrantActions from "./grantActions";

interface GrantsTableProps {
	rows: OAuth2GrantRow[];
	totalCount: number;
	offset: number;
	pageSize: number;
	onOffsetChange: (offset: number) => void;
	isFetching: boolean;
	hasActiveFilters: boolean;
	revoking: boolean;
	pendingActionRowId: string | null;
	onRevoke: (row: OAuth2GrantRow) => void;
}

export default function GrantsTable({
	rows,
	totalCount,
	offset,
	pageSize,
	onOffsetChange,
	isFetching,
	hasActiveFilters,
	revoking,
	pendingActionRowId,
	onRevoke,
}: GrantsTableProps) {
	return (
		<div className="flex grow flex-col overflow-hidden">
			<div className={`mb-2 grow overflow-hidden rounded-sm border ${isFetching ? "opacity-70 transition-opacity" : ""}`}>
				<Table containerClassName="h-full overflow-auto">
					<TableHeader className="bg-muted sticky top-0 z-20">
						<TableRow>
							<TableHead>Client</TableHead>
							<TableHead>
								<HeaderWithTooltip
									label="Bound to"
									tooltip="The identity this grant is tied to: an end user (via SSO), a virtual key (shared by anyone using that VK), or an anonymous session. This determines which upstream per-user OAuth sessions are reachable under this grant."
								/>
							</TableHead>
							<TableHead>
								<HeaderWithTooltip
									label="Access token expiry"
									tooltip="When the current JWT access token expires. MCP clients silently refresh using the refresh token, so an active grant past its expiry will mint a new token automatically on the next request."
								/>
							</TableHead>
							<TableHead>Created</TableHead>
							<TableHead>
								<HeaderWithTooltip
									label="Last used"
									tooltip="When this grant last refreshed its access token. MCP clients refresh as their token nears expiry, so this tracks the grant's most recent activity. Grants that have not refreshed since they were authorized fall back to when they were created."
								/>
							</TableHead>
							<TableHead className={`bg-muted relative sticky right-0 z-10 w-[56px] text-right ${PIN_SHADOW_RIGHT}`} />
						</TableRow>
					</TableHeader>
					<TableBody>
						{rows.length === 0 ? (
							<TableRow>
								<TableCell colSpan={6} className="h-24 text-center">
									{hasActiveFilters ? (
										<div className="text-muted-foreground text-sm">
											No grants match these filters.
										</div>
									) : (
										<EmptyGrantsState />
									)}
								</TableCell>
							</TableRow>
						) : (
							rows.map((row) => (
								<TableRow key={row.id} className="group">
									<TableCell className="font-medium">
										{row.client_name || row.client_id}
									</TableCell>
									<TableCell>
										<BindingCell row={row} />
									</TableCell>
									<TableCell className="text-muted-foreground text-sm">
										<AccessTokenExpiry row={row} />
									</TableCell>
									<TableCell className="text-muted-foreground text-sm">
										{formatRelativePast(row.created_at)}
									</TableCell>
									<TableCell className="text-muted-foreground text-sm">
										{formatRelativePast(row.last_used_at || row.created_at)}
									</TableCell>
									<TableCell
										className={`relative group-hover:bg-muted dark:bg-card dark:group-hover:bg-muted sticky right-0 z-10 bg-white text-right ${PIN_SHADOW_RIGHT}`}
									>
										<GrantActions
											row={row}
											revoking={revoking}
											isPendingRow={pendingActionRowId === row.id}
											onRevoke={() => onRevoke(row)}
										/>
									</TableCell>
								</TableRow>
							))
						)}
					</TableBody>
				</Table>
			</div>

			{totalCount > 0 && (
				<div className="flex shrink-0 items-center justify-between text-xs" data-testid="pagination">
					<div className="text-muted-foreground flex items-center gap-2">
						{(offset + 1).toLocaleString()}-{Math.min(offset + pageSize, totalCount).toLocaleString()} of {totalCount.toLocaleString()} entries
					</div>

					<div className="flex items-center gap-2">
						<Button
							variant="ghost"
							size="sm"
							onClick={() => onOffsetChange(Math.max(0, offset - pageSize))}
							disabled={offset === 0}
							data-testid="oauth-grants-prev-page-btn"
							aria-label="Previous page"
						>
							<ChevronLeft className="size-3" />
						</Button>

						<div className="flex items-center gap-1">
							<span>Page</span>
							<span>{Math.floor(offset / pageSize) + 1}</span>
							<span>of {Math.ceil(totalCount / pageSize)}</span>
						</div>

						<Button
							variant="ghost"
							size="sm"
							onClick={() => onOffsetChange(offset + pageSize)}
							disabled={offset + pageSize >= totalCount}
							data-testid="oauth-grants-next-page-btn"
							aria-label="Next page"
						>
							<ChevronRight className="size-3" />
						</Button>
					</div>
				</div>
			)}
		</div>
	);
}

function BindingCell({ row }: { row: OAuth2GrantRow }) {
	const display = row.bf_sub_display || row.bf_sub;
	if (row.bf_mode === "user") {
		return (
			<span className="inline-flex items-center gap-2">
				<UserRound className="text-muted-foreground size-3.5 shrink-0" />
				<span className="text-sm">{display}</span>
			</span>
		);
	}
	if (row.bf_mode === "vk") {
		return (
			<span className="inline-flex items-center gap-2">
				<KeyRound className="text-muted-foreground size-3.5 shrink-0" />
				<span className="text-sm">{display}</span>
			</span>
		);
	}
	return (
		<span className="inline-flex items-center gap-2">
			<Fingerprint className="text-muted-foreground size-3.5 shrink-0" />
			<span className="font-mono text-sm">{display}</span>
		</span>
	);
}

function AccessTokenExpiry({ row }: { row: OAuth2GrantRow }) {
	// Access token TTL is 10 min (600s default). Access tokens are stateless JWTs
	// not stored server-side, so we approximate expiry from the grant's last
	// activity (last_used_at, falling back to created_at). Anchoring to created_at
	// alone would read as expired for any grant that has silently refreshed.
	const baseMs = new Date(row.last_used_at ?? row.created_at).getTime();
	if (!Number.isFinite(baseMs)) {
		return <span className="text-muted-foreground">Unknown</span>;
	}
	const expiryMs = baseMs + 600_000; // 10 min default
	const diffMs = expiryMs - Date.now();
	if (diffMs < 0) {
		return <span className="text-muted-foreground">Refreshes on next use</span>;
	}
	const mins = Math.ceil(diffMs / 60_000);
	return <span>in {mins} min</span>;
}

function HeaderWithTooltip({ label, tooltip }: { label: string; tooltip: string }) {
	return (
		<TooltipProvider delayDuration={150}>
			<Tooltip>
				<TooltipTrigger asChild>
					<span className="inline-flex cursor-help items-center gap-2">
						{label}
						<Info className="text-muted-foreground size-3" />
					</span>
				</TooltipTrigger>
				<TooltipContent className="max-w-xs">{tooltip}</TooltipContent>
			</Tooltip>
		</TooltipProvider>
	);
}

function EmptyGrantsState() {
	return (
		<div className="flex flex-col items-center gap-3 py-4">
			<p className="text-muted-foreground text-sm">
				No grants yet. Grants appear here when an MCP client connects via the OAuth
				consent flow. (Authentication Mode needs to be set to "oauth" or "both" for grants to be issued.)
			</p>
		</div>
	);
}

function formatRelativePast(iso: string): string {
	try {
		const ts = new Date(iso).getTime();
		if (!Number.isFinite(ts)) return iso;
		const diffMs = Date.now() - ts;
		if (diffMs < 0) return "just now";
		const mins = Math.floor(diffMs / 60_000);
		if (mins < 1) return "just now";
		if (mins < 60) return `${mins}m ago`;
		const hrs = Math.floor(mins / 60);
		if (hrs < 24) return `${hrs}h ago`;
		const days = Math.floor(hrs / 24);
		return `${days}d ago`;
	} catch {
		return iso;
	}
}
