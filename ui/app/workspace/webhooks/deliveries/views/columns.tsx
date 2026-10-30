import { attemptSequence, DeliveryGroup, outcomeBadge } from "@/app/workspace/webhooks/views/deliveries.utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { WEBHOOK_EVENTS } from "@/lib/types/webhooks";
import { ColumnDef } from "@tanstack/react-table";
import type { TFunction } from "i18next";
import { format, formatDistanceToNow } from "date-fns";
import { ArrowUpRight, ChevronDown, ChevronRight, Loader2, RefreshCcw } from "lucide-react";

const EVENT_LABELS = new Map(WEBHOOK_EVENTS.map((event) => [event.value, event.label]));

/** A short, click-to-copy id cell with the full value on hover. */
const idCell = (value: string | undefined, onCopy: (value: string) => void, testId: string) => {
	if (!value) return <span className="text-muted-foreground">-</span>;
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<button
					type="button"
					className="hover:text-foreground text-muted-foreground cursor-pointer font-mono text-xs"
					onClick={(e) => {
						e.stopPropagation();
						onCopy(value);
					}}
					data-testid={testId}
				>
					{value.slice(0, 8)}
				</button>
			</TooltipTrigger>
			<TooltipContent className="font-mono">{value}</TooltipContent>
		</Tooltip>
	);
};

interface CreateColumnsOptions {
	t: TFunction<"governance">;
	/** Endpoint id → display name, for the cross-endpoint view. */
	endpointNames: Map<string, string>;
	expandedIds: Set<string>;
	onToggleExpanded: (webhookId: string) => void;
	onCopy: (value: string) => void;
	onRedeliver: (group: DeliveryGroup) => void;
	redeliveringIds: Set<string>;
	canManage: boolean;
	/** Opens the matching inference request in the logs page. */
	onOpenRequest: (requestId: string) => void;
}

export function createColumns({
	t,
	endpointNames,
	expandedIds,
	onToggleExpanded,
	onCopy,
	onRedeliver,
	redeliveringIds,
	canManage,
	onOpenRequest,
}: CreateColumnsOptions): ColumnDef<DeliveryGroup>[] {
	return [
		{
			id: "expand",
			header: () => null,
			size: 50,
			cell: ({ row }) => {
				// Only a delivery with more than one send has anything to expand.
				if (row.original.sends.length <= 1) return null;
				const expanded = expandedIds.has(row.original.webhookId);
				return (
					<Button
						variant="ghost"
						size="icon"
						className="size-6"
						onClick={(e) => {
							e.stopPropagation();
							onToggleExpanded(row.original.webhookId);
						}}
						aria-label={expanded ? t("webhooks.deliveries.columns.collapseSends") : t("webhooks.deliveries.columns.expandSends")}
						data-testid={`webhook-delivery-expand-${row.original.webhookId}`}
					>
						{expanded ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
					</Button>
				);
			},
		},
		{
			id: "time",
			header: t("webhooks.details.time"),
			size: 140,
			cell: ({ row }) => {
				const timestamp = row.original.latest?.created_at;
				const date = timestamp ? new Date(timestamp) : null;
				// Same absolute-over-relative stack as the logs table, so scanning
				// across the two pages reads the same way.
				if (!date || date.toString() === "Invalid Date") return <span className="text-muted-foreground">-</span>;
				return (
					<div className="flex flex-col leading-tight">
						<span className="font-mono text-xs tabular-nums">{format(date, "MMM dd  HH:mm:ss")}</span>
						<span className="text-muted-foreground text-[10.5px] tabular-nums">{formatDistanceToNow(date, { addSuffix: true })}</span>
					</div>
				);
			},
		},
		{
			id: "webhook",
			header: t("webhooks.deliveries.columns.webhook"),
			size: 180,
			cell: ({ row }) => {
				const endpointId = row.original.latest?.endpoint_id;
				if (!endpointId) return <span className="text-muted-foreground">-</span>;
				// History outlives its endpoint, so fall back to the raw id rather
				// than rendering a blank cell for a deleted webhook.
				const name = endpointNames.get(endpointId);
				return name ? (
					<span className="truncate text-sm">{name}</span>
				) : (
					<span className="text-muted-foreground font-mono text-xs">{endpointId.slice(0, 8)}</span>
				);
			},
		},
		{
			id: "delivery_id",
			header: t("webhooks.deliveries.columns.deliveryId"),
			size: 110,
			cell: ({ row }) => idCell(row.original.webhookId, onCopy, `webhook-delivery-id-${row.original.webhookId}`),
		},
		{
			id: "request_id",
			header: t("webhooks.details.requestId"),
			size: 110,
			cell: ({ row }) => {
				const requestId = row.original.latest?.request_id;
				if (!requestId) return <span className="text-muted-foreground">-</span>;
				return (
					<Tooltip>
						<TooltipTrigger asChild>
							<button
								type="button"
								className="text-muted-foreground hover:text-foreground group/request inline-flex cursor-pointer items-center gap-0.5 font-mono text-xs underline-offset-2 hover:underline"
								onClick={(e) => {
									e.stopPropagation();
									onOpenRequest(requestId);
								}}
								data-testid={`webhook-delivery-request-${requestId}`}
							>
								{requestId.slice(0, 8)}
								{/* Marks the cell as a link out to the logs page rather than a
								    copy-to-clipboard id like the neighbouring Delivery ID. */}
								<ArrowUpRight className="size-3 opacity-60 transition-opacity group-hover/request:opacity-100" />
							</button>
						</TooltipTrigger>
						<TooltipContent className="font-mono">{t("webhooks.deliveries.columns.openInLogs", { id: requestId })}</TooltipContent>
					</Tooltip>
				);
			},
		},
		{
			id: "event",
			header: t("webhooks.details.event"),
			size: 170,
			cell: ({ row }) => {
				const event = row.original.latest?.event;
				if (!event) return <span className="text-muted-foreground">-</span>;
				return (
					<Badge variant="outline" className="font-normal">
						{EVENT_LABELS.get(event) ?? event}
					</Badge>
				);
			},
		},
		{
			id: "status",
			header: t("webhooks.details.status"),
			size: 190,
			cell: ({ row }) => {
				const latest = row.original.latest;
				if (!latest) return <span className="text-muted-foreground">-</span>;
				return (
					<div className="flex items-center gap-1.5">
						{outcomeBadge(latest)}
						{row.original.sends.length > 1 && (
							<Badge variant="outline" className="text-muted-foreground font-normal">
								{t("webhooks.details.sends", { count: row.original.sends.length })}
							</Badge>
						)}
					</div>
				);
			},
		},
		{
			id: "responses",
			header: t("webhooks.details.responses"),
			size: 200,
			cell: ({ row }) => {
				const attempts = row.original.latestSend?.attempts;
				if (!attempts?.length) return <span className="text-muted-foreground">-</span>;
				return attemptSequence(attempts);
			},
		},
		{
			id: "actions",
			header: "",
			size: 60,
			cell: ({ row }) => {
				const latest = row.original.latest;
				if (!latest) return null;
				const redelivering = redeliveringIds.has(latest.id);
				// A delivery still working through its own retries would collide
				// with a manual replay, so it stays disabled until it settles.
				const reason = !canManage
					? t("webhooks.deliveries.columns.noPermission")
					: redelivering
						? t("webhooks.details.redelivering")
						: latest.outcome === "retryable_failure"
							? t("webhooks.details.redeliverRetrying")
							: t("webhooks.details.redeliver");
				return (
					<Tooltip>
						{/* A disabled button emits no pointer events, so the trigger wraps it -
						    otherwise the tooltip disappears exactly when it explains the most. */}
						<TooltipTrigger asChild>
							<span className="inline-flex">
								<Button
									variant="ghost"
									size="icon"
									className="size-7"
									disabled={!canManage || redelivering || latest.outcome === "retryable_failure"}
									onClick={(e) => {
										e.stopPropagation();
										onRedeliver(row.original);
									}}
									aria-label={t("webhooks.details.redeliver")}
									data-testid={`webhook-delivery-redeliver-${row.original.webhookId}`}
								>
									{redelivering ? <Loader2 className="size-3.5 animate-spin" /> : <RefreshCcw className="size-3.5" />}
								</Button>
							</span>
						</TooltipTrigger>
						<TooltipContent>{reason}</TooltipContent>
					</Tooltip>
				);
			},
		},
	];
}

export const getWebhookDeliveryColumnLabels = (t: TFunction<"governance">): Record<string, string> => ({
	expand: "",
	time: t("webhooks.details.time"),
	webhook: t("webhooks.deliveries.columns.webhook"),
	delivery_id: t("webhooks.deliveries.columns.deliveryId"),
	request_id: t("webhooks.details.requestId"),
	event: t("webhooks.details.event"),
	status: t("webhooks.details.status"),
	responses: t("webhooks.details.responses"),
	actions: t("webhooks.deliveries.columns.actions"),
});