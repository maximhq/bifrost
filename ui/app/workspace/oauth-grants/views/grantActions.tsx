// Per-row actions menu for an OAuth grant: a dropdown exposing "View auth
// sessions" (deep-link into MCP sessions pre-filtered to this grant's exact
// identity) and a destructive "Revoke" action. The revoke confirmation itself
// is owned by the page via onRevoke.

import { Button } from "@/components/ui/button";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuTrigger,
} from "@/components/ui/dropdownMenu";
import type { OAuth2GrantRow } from "@/lib/store/apis/oauth2SessionsApi";
import { Link } from "@tanstack/react-router";
import { ExternalLink, Loader2, MoreHorizontal, Trash2 } from "lucide-react";

interface GrantActionsProps {
	row: OAuth2GrantRow;
	revoking: boolean;
	isPendingRow: boolean;
	onRevoke: () => void;
}

export default function GrantActions({ row, revoking, isPendingRow, onRevoke }: GrantActionsProps) {
	const busy = revoking;
	// Link to Auth Sessions pre-filtered to this grant's exact identity: the
	// mode plus the identity filter, which exact-matches bf_sub against the
	// session's user_id / virtual key id / session id, so the user lands on
	// just this identity's sessions.
	const authSessionsUrl = `/workspace/mcp-sessions?auth_mode=${row.bf_mode}&identity=${encodeURIComponent(row.bf_sub)}`;

	return (
		<DropdownMenu>
			<DropdownMenuTrigger asChild>
				<Button data-testid="oauth-grants-actions-trigger" variant="ghost" size="icon" className="h-8 w-8" aria-label="Grant actions" disabled={busy}>
					{busy && isPendingRow ? <Loader2 className="h-4 w-4 animate-spin" /> : <MoreHorizontal className="h-4 w-4" />}
				</Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent align="end">
				{(row.bf_mode === "user" || row.bf_mode === "vk" || row.bf_mode === "session") && (
					<DropdownMenuItem asChild className="cursor-pointer">
						<Link to={authSessionsUrl} data-testid="oauth-grants-view-sessions-link">
							<ExternalLink className="h-4 w-4" />
							View auth sessions
						</Link>
					</DropdownMenuItem>
				)}
				<DropdownMenuItem
					data-testid="oauth-grants-revoke-action"
					variant="destructive"
					className="cursor-pointer"
					disabled={busy}
					onSelect={(e) => { e.preventDefault(); onRevoke(); }}
				>
					<Trash2 className="h-4 w-4" />
					Revoke
				</DropdownMenuItem>
			</DropdownMenuContent>
		</DropdownMenu>
	);
}
