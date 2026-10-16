// RefreshTokenStatus is the shared one-line reading of an OAuth credential's
// refresh token, used by the MCP sessions table and the MCP server sheet so
// both describe the same row with the same words. Present: Bifrost renews
// the access token at use time. Not issued: the provider returned none, so
// the credential must be re-authenticated once the access token expires.
// Rejected upstream: the provider refused the last refresh (the row is in
// needs_reauth), whether or not a refresh token string is still stored.

import { cn } from "@/lib/utils";
import { Check, X } from "lucide-react";

interface RefreshTokenStatusProps {
	hasRefreshToken: boolean;
	status: string;
	className?: string;
}

export function RefreshTokenStatus({ hasRefreshToken, status, className }: RefreshTokenStatusProps) {
	if (status === "needs_reauth") {
		return (
			<span className={cn("inline-flex items-center gap-1.5 text-red-700 dark:text-red-300", className)}>
				<X className="size-3.5 shrink-0" />
				Rejected upstream
			</span>
		);
	}
	if (hasRefreshToken) {
		return (
			<span className={cn("inline-flex items-center gap-1.5 text-green-700 dark:text-green-300", className)}>
				<Check className="size-3.5 shrink-0" />
				Present
			</span>
		);
	}
	return (
		<span className={cn("inline-flex items-center gap-1.5", className)}>
			<X className="text-muted-foreground size-3.5 shrink-0" />
			Not issued
		</span>
	);
}