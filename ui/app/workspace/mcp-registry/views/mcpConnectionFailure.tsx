// The two surfaces that explain a server's connection state: the badge
// popover in the servers table (a quick view) and the reason block in the
// server sheet. Both read the same MCPConnectionFailure records, folded the
// same way, so what the table hints at is exactly what the sheet spells out.

import { Badge } from "@/components/ui/badge";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { MCP_STATUS_COLORS } from "@/lib/constants/config";
import type { MCPClient, MCPConnectionFailure, MCPConnectionState } from "@/lib/types/mcp";
import {
	failureStageLabel,
	foldInstanceStates,
	formatFailureTiming,
	hasStateReason,
	stateReasonTitle,
} from "@/lib/utils/mcpConnectionFailure";
import { titleCaseFromSnakeCase } from "@/lib/utils/strings";
import { ChevronDown } from "lucide-react";

type StatefulClient = Pick<MCPClient, "state" | "last_failure" | "node_states">;

function FailureDetail({ failure, state }: { failure: MCPConnectionFailure; state: MCPConnectionState }) {
	return (
		<div className="space-y-1">
			<div className="font-medium">{failureStageLabel(failure.stage)}</div>
			<div className="bg-muted rounded px-1.5 py-1 font-mono text-[11px] break-words whitespace-normal">{failure.message}</div>
			<div className="text-muted-foreground">{formatFailureTiming(failure, state)}</div>
		</div>
	);
}

/**
 * StateBadge is the servers table's state cell. A healthy server is a plain
 * badge. Any other state that carries a reason (its own failure record, or a
 * per-instance breakdown in a distributed deployment) opens a popover that
 * names the failed stage, the error, and when it last happened. Instances
 * are grouped by state and reason with a count; only Degraded shows a badge
 * per group, because that is the only case where states differ.
 */
export function StateBadge({ client }: { client: StatefulClient }) {
	const { state, last_failure, node_states } = client;
	const badge = <Badge className={MCP_STATUS_COLORS[state]}>{titleCaseFromSnakeCase(state)}</Badge>;
	if (!hasStateReason(client)) {
		return badge;
	}
	const groups = node_states ? foldInstanceStates(node_states) : [];
	const mixed = state === "degraded";
	return (
		<Popover>
			<PopoverTrigger asChild>
				<button type="button" data-testid="mcp-client-state-reason-trigger" className="inline-flex cursor-help items-center gap-1">
					{badge}
					<ChevronDown className="text-muted-foreground size-3" />
				</button>
			</PopoverTrigger>
			<PopoverContent className="w-xs text-xs" align="start">
				<p className="text-muted-foreground mb-1.5">{stateReasonTitle(state, groups.length > 0)}</p>
				{groups.length > 0 ? (
					<ul className="space-y-2.5">
						{groups.map((g, i) => (
							<li key={i} className="space-y-1">
								<div className="flex items-center gap-2">
									{mixed && <Badge className={MCP_STATUS_COLORS[g.state]}>{titleCaseFromSnakeCase(g.state)}</Badge>}
									<span className={mixed ? "text-muted-foreground" : "font-medium"}>
										{g.count} {g.count === 1 ? "instance" : "instances"}
									</span>
								</div>
								{g.last_failure && <FailureDetail failure={g.last_failure} state={g.state} />}
							</li>
						))}
					</ul>
				) : last_failure ? (
					<FailureDetail failure={last_failure} state={state} />
				) : null}
				<p className="text-muted-foreground mt-2 border-t pt-2">
					<a href="https://docs.getbifrost.ai/mcp/connections" target="_blank" rel="noreferrer" className="underline underline-offset-2">
						About connection states
					</a>
				</p>
			</PopoverContent>
		</Popover>
	);
}

/**
 * ConnectionFailureBlock is the server sheet's reason block: the same fold
 * as the popover, laid out as rows. It explains and never acts: every fix
 * (reauthorize, refresh the admin credential, edit the configuration) lives
 * in the servers table's actions menu and the form, not here.
 */
export function ConnectionFailureBlock({ client }: { client: MCPClient }) {
	const { state, last_failure, node_states } = client;
	if (!hasStateReason(client)) {
		return null;
	}
	const groups = node_states ? foldInstanceStates(node_states) : [];
	const mixed = state === "degraded";
	const tone =
		state === "needs_reauth"
			? "border-red-200 dark:border-red-900"
			: state === "degraded"
				? "border-blue-200 dark:border-blue-900"
				: "border-yellow-200 dark:border-yellow-900";

	if (groups.length > 0) {
		return (
			<div className={`overflow-hidden rounded-md border ${tone}`} data-testid="mcpclient-connection-failure-block">
				<div className="bg-muted text-muted-foreground hidden gap-3 px-3 py-2 text-[11px] font-medium tracking-wide uppercase sm:grid sm:grid-cols-[6rem_1fr_auto]">
					<span>Instances</span>
					<span>Reason</span>
					<span>Last failed</span>
				</div>
				{groups.map((g, i) => (
					<div key={i} className="grid grid-cols-1 gap-1 border-t px-3 py-2 text-xs sm:grid-cols-[6rem_1fr_auto] sm:items-center sm:gap-3">
						<span className="flex flex-col gap-1">
							<span>
								{g.count} {g.count === 1 ? "instance" : "instances"}
							</span>
							{mixed && <Badge className={`w-fit ${MCP_STATUS_COLORS[g.state]}`}>{titleCaseFromSnakeCase(g.state)}</Badge>}
						</span>
						<span className="font-mono text-[11px] break-words whitespace-normal">
							{g.last_failure ? (
								`${failureStageLabel(g.last_failure.stage)}: ${g.last_failure.message}`
							) : (
								<span className="text-muted-foreground">Check passed</span>
							)}
						</span>
						<span className="text-muted-foreground sm:whitespace-nowrap">
							{g.last_failure ? formatFailureTiming(g.last_failure, g.state) : ""}
						</span>
					</div>
				))}
			</div>
		);
	}

	if (!last_failure) {
		return null;
	}
	return (
		<div className={`space-y-2 rounded-md border px-3 py-2 text-xs ${tone}`} data-testid="mcpclient-connection-failure-block">
			<div className="font-medium">{failureStageLabel(last_failure.stage)}</div>
			<div className="font-mono text-[11px] break-words whitespace-normal">{last_failure.message}</div>
			<div className="text-muted-foreground">{formatFailureTiming(last_failure, state)}</div>
		</div>
	);
}