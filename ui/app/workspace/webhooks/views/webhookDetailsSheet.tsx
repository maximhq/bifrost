import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { getErrorMessage, useGetWebhookDeliveriesQuery, useRedeliverWebhookDeliveryMutation } from "@/lib/store";
import { WEBHOOK_TUNING_DEFAULTS, WebhookEndpoint, WebhookEvent } from "@/lib/types/webhooks";
import { useNavigate } from "@tanstack/react-router";
import { format, formatDistanceToNow } from "date-fns";
import { ArrowRight, ChevronDown, ChevronRight, Info, Loader2, RefreshCcw, Send } from "lucide-react";
import { Fragment, useMemo, useState } from "react";
import { toast } from "sonner";
import { attemptSequence, groupDeliveries, outcomeBadge } from "./deliveries.utils";

// The sheet shows only a preview; the dedicated deliveries page owns the
// full, filterable, paginated history.
const PREVIEW_SIZE = 5;

const DetailEntry = ({ label, value }: { label: string; value: React.ReactNode }) => (
	<div>
		<div className="text-muted-foreground text-xs">{label}</div>
		<div className="text-sm font-medium break-all">{value}</div>
	</div>
);

const relativeTime = (timestamp?: string) => (timestamp ? formatDistanceToNow(new Date(timestamp), { addSuffix: true }) : "never");

// Why the redeliver control is (or is not) available. Ordered by precedence:
// an in-flight replay first, then the reasons the button is disabled.
const redeliverHint = (outcome: string, endpointDisabled?: boolean, redelivering?: boolean) => {
	if (redelivering) return "Redelivering...";
	if (endpointDisabled) return "Enable this webhook to redeliver";
	if (outcome === "retryable_failure") return "Still retrying automatically - redelivery is available once it settles";
	return "Redeliver";
};

interface WebhookDetailsSheetProps {
	endpoint: WebhookEndpoint | null;
	// Test fires are owned by the parent so the in-flight state stays shared
	// with the table's actions menu.
	isTesting: boolean;
	// Gates the mutating controls (test fire, redeliver) so a view-only user
	// sees the history without the ability to trigger deliveries.
	canManage: boolean;
	onTest: (endpoint: WebhookEndpoint, event: WebhookEvent) => void;
	onClose: () => void;
}

export function WebhookDetailsSheet({ endpoint, isTesting, canManage, onTest, onClose }: WebhookDetailsSheetProps) {
	const open = !!endpoint;
	const [redeliverWebhookDelivery] = useRedeliverWebhookDeliveryMutation();
	const [redeliveringIds, setRedeliveringIds] = useState<Set<string>>(new Set());
	const { copy } = useCopyToClipboard();
	const navigate = useNavigate();

	const { data, isLoading, isError } = useGetWebhookDeliveriesQuery(
		{ endpointId: endpoint?.id ?? "", limit: PREVIEW_SIZE, offset: 0 },
		{ skip: !open, pollingInterval: 5000 },
	);
	const totalCount = data?.pagination.total_count ?? 0;

	const deliveries = useMemo(() => groupDeliveries(data?.deliveries ?? []), [data]);

	const [expandedIds, setExpandedIds] = useState<Set<string>>(new Set());
	const toggleExpanded = (webhookId: string) => {
		setExpandedIds((prev) => {
			const next = new Set(prev);
			if (next.has(webhookId)) {
				next.delete(webhookId);
			} else {
				next.add(webhookId);
			}
			return next;
		});
	};

	const handleRedeliver = async (deliveryId: string) => {
		setRedeliveringIds((prev) => new Set(prev).add(deliveryId));
		try {
			await redeliverWebhookDelivery(deliveryId).unwrap();
			toast.success("Redelivery queued under the original webhook id");
		} catch (err) {
			toast.error(getErrorMessage(err));
		} finally {
			setRedeliveringIds((prev) => {
				const next = new Set(prev);
				next.delete(deliveryId);
				return next;
			});
		}
	};

	// Effective knob value with its unit; unset knobs show the worker default.
	const tuning = (key: keyof typeof WEBHOOK_TUNING_DEFAULTS, unit = "") => {
		const value = endpoint?.[key] || WEBHOOK_TUNING_DEFAULTS[key];
		return `${value}${unit}`;
	};

	return (
		<Sheet open={open} onOpenChange={(sheetOpen) => !sheetOpen && onClose()}>
			<SheetContent className="flex w-full flex-col gap-0 overflow-x-hidden p-4 sm:max-w-[60%] md:p-8">
				<SheetHeader className="flex flex-col items-start px-0">
					<SheetTitle className="flex w-fit items-center gap-2 font-medium">
						<p className="text-md max-w-full truncate">{endpoint?.name}</p>
						{endpoint?.disabled ? (
							<Badge variant="outline" className="bg-gray-100 text-gray-800">
								disabled
							</Badge>
						) : (
							<Badge variant="outline" className="bg-green-100 text-green-800">
								enabled
							</Badge>
						)}
					</SheetTitle>
					<SheetDescription className="break-all">{endpoint?.url}</SheetDescription>
				</SheetHeader>

				<div className="space-y-4 rounded-sm border p-4">
					<div className="grid grid-cols-1 gap-4 md:grid-cols-3">
						<DetailEntry
							label="Events"
							value={
								<div className="flex flex-wrap gap-1">
									{endpoint?.events.map((event) => (
										<Badge key={event} variant="outline" className="font-mono text-xs">
											{event}
										</Badge>
									))}
								</div>
							}
						/>
						<DetailEntry label="Include response" value={endpoint?.include_response ? "yes" : "no"} />
						<DetailEntry label="Private network" value={endpoint?.allow_private_network ? "allowed" : "blocked"} />
						<DetailEntry label="Last success" value={relativeTime(endpoint?.last_success_at)} />
						<DetailEntry label="Last failure" value={relativeTime(endpoint?.last_failure_at)} />
						<DetailEntry label="Consecutive failures" value={endpoint?.consecutive_failures ?? 0} />
						<DetailEntry label="Max retries" value={tuning("max_retries")} />
						<DetailEntry
							label="Retry backoff"
							value={`${tuning("retry_backoff_initial_seconds", "s")} → ${tuning("retry_backoff_max_seconds", "s")}`}
						/>
						<DetailEntry label="Attempt timeout" value={tuning("attempt_timeout_seconds", "s")} />
					</div>
				</div>

				<div className="mt-4 flex items-center justify-between">
					<div className="flex items-center gap-2">
						<h3 className="font-semibold">Recent Deliveries</h3>
						<Button
							variant="link"
							size="sm"
							className="h-auto p-0 text-xs"
							onClick={() => {
								onClose();
								navigate({ to: "/workspace/webhooks/deliveries", search: { webhook_id: [endpoint?.id ?? ""] } as never });
							}}
							data-testid="webhook-view-delivery-history-btn"
						>
							{totalCount > PREVIEW_SIZE ? `View all ${totalCount.toLocaleString()}` : "View delivery history"}
							<ArrowRight className="size-3" />
						</Button>
					</div>
					{canManage && (
						<DropdownMenu>
							<DropdownMenuTrigger asChild>
								<Button
									variant="outline"
									size="sm"
									className="min-w-[10rem] justify-between"
									disabled={isTesting || endpoint?.disabled}
									data-testid="webhook-test-fire-btn"
								>
									{isTesting ? <Loader2 className="h-4 w-4 animate-spin" /> : <Send className="h-4 w-4" />}
									<span className="flex-1 text-center">Send Test Event</span>
									<ChevronDown className="h-3 w-3" />
								</Button>
							</DropdownMenuTrigger>
							<DropdownMenuContent align="end">
								{endpoint?.events.map((event) => (
									<DropdownMenuItem
										key={event}
										className="cursor-pointer"
										data-testid={`webhook-test-fire-${event}`}
										onSelect={() => onTest(endpoint, event)}
									>
										{event}
									</DropdownMenuItem>
								))}
							</DropdownMenuContent>
						</DropdownMenu>
					)}
				</div>

				<div className="mt-4 min-h-0 flex-1 overflow-auto rounded-sm border [scrollbar-gutter:stable]">
					<Table>
						<TableHeader className="bg-muted sticky top-0 z-10">
							<TableRow>
								<TableHead className="w-8 px-2"></TableHead>
								<TableHead>Time</TableHead>
								<TableHead>Request ID</TableHead>
								<TableHead>Event</TableHead>
								<TableHead>
									<Tooltip>
										<TooltipTrigger asChild>
											<span className="inline-flex cursor-help items-center gap-1.5">
												Status
												<Info className="text-muted-foreground size-3" />
											</span>
										</TooltipTrigger>
										<TooltipContent className="max-w-xs">
											<div className="space-y-1">
												<p>
													<span className="font-medium">delivered</span>: receiver returned a 2xx.
												</p>
												<p>
													<span className="font-medium">retrying</span>: transient failure (network error, timeout, 429, or 5xx); another
													attempt is scheduled.
												</p>
												<p>
													<span className="font-medium">failed</span>: permanent error (a non-retryable 4xx such as 401/404); not retried
													automatically.
												</p>
												<p>
													<span className="font-medium">retries exhausted</span>: kept failing until the retry budget ran out.
												</p>
											</div>
										</TooltipContent>
									</Tooltip>
								</TableHead>
								<TableHead>
									<Tooltip>
										<TooltipTrigger asChild>
											<span className="inline-flex cursor-help items-center gap-1.5">
												Responses
												<Info className="text-muted-foreground size-3" />
											</span>
										</TooltipTrigger>
										<TooltipContent className="max-w-xs">
											One chip per delivery attempt, oldest to newest: the receiver's response code, or a dash when no response arrived.
											Hover a failed code for its error.
										</TooltipContent>
									</Tooltip>
								</TableHead>
								<TableHead className="text-right">Actions</TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{isLoading ? (
								<TableRow>
									<TableCell colSpan={7} className="h-24 text-center">
										<Loader2 className="mx-auto h-4 w-4 animate-spin" />
									</TableCell>
								</TableRow>
							) : isError ? (
								<TableRow>
									<TableCell colSpan={7} className="text-destructive h-24 text-center" data-testid="webhook-delivery-history-error">
										Failed to load delivery history. Retrying…
									</TableCell>
								</TableRow>
							) : deliveries.length === 0 ? (
								<TableRow>
									<TableCell colSpan={7} className="text-muted-foreground h-24 text-center">
										No deliveries yet.
									</TableCell>
								</TableRow>
							) : (
								deliveries.map(({ webhookId, latest, latestSend, sends }) => {
									const hasResends = sends.length > 1;
									const expanded = expandedIds.has(webhookId);
									// The delivery counts as delivered if any send reached the receiver,
									// even when a later manual redelivery failed. Headline the newest
									// successful send; otherwise fall back to the latest send's state.
									const deliveredSend = [...sends].reverse().find((send) => send.attempts[0].outcome === "delivered");
									const headlineSend = deliveredSend ?? latestSend;
									const headline = headlineSend.attempts[0];
									return (
										<Fragment key={webhookId}>
											<TableRow data-testid={`webhook-delivery-row-${webhookId}`}>
												<TableCell className="px-2">
													{hasResends && (
														<Button
															variant="ghost"
															size="icon"
															className="size-8"
															onClick={() => toggleExpanded(webhookId)}
															aria-expanded={expanded}
															aria-label={expanded ? "Collapse redeliveries" : "Expand redeliveries"}
															data-testid={`webhook-delivery-expand-${webhookId}`}
														>
															{expanded ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
														</Button>
													)}
												</TableCell>
												<TableCell className="whitespace-nowrap">{relativeTime(latest.created_at)}</TableCell>
												<TableCell>
													{latest.request_id ? (
														<Tooltip>
															<TooltipTrigger asChild>
																<code
																	className="cursor-pointer font-mono text-xs"
																	onClick={() => copy(latest.request_id ?? "")}
																	data-testid={`webhook-delivery-request-id-${webhookId}`}
																>
																	{latest.request_id.slice(0, 8)}…
																</code>
															</TooltipTrigger>
															<TooltipContent className="font-mono">{latest.request_id}</TooltipContent>
														</Tooltip>
													) : (
														"-"
													)}
												</TableCell>
												<TableCell className="whitespace-nowrap">
													<Badge variant="outline" className="font-mono text-xs">
														{latest.event}
													</Badge>
												</TableCell>
												<TableCell>
													<div className="flex items-center gap-2">
														{outcomeBadge(headline)}
														{hasResends && (
															<Badge variant="outline" className="text-muted-foreground text-xs">
																{sends.length} sends
															</Badge>
														)}
													</div>
												</TableCell>
												<TableCell>{attemptSequence(headlineSend.attempts)}</TableCell>
												<TableCell className="text-right">
													{canManage && (
														<Tooltip>
															{/* A disabled button emits no pointer events, so the trigger wraps it —
																	    otherwise the tooltip vanishes exactly when it explains the most. */}
															<TooltipTrigger asChild>
																<span className="inline-flex">
																	<Button
																		variant="ghost"
																		size="sm"
																		onClick={() => handleRedeliver(latest.id)}
																		disabled={
																			redeliveringIds.has(latest.id) || latest.outcome === "retryable_failure" || endpoint?.disabled
																		}
																		data-testid={`webhook-redeliver-btn-${webhookId}`}
																		aria-label="Redeliver"
																	>
																		{redeliveringIds.has(latest.id) ? (
																			<Loader2 className="h-4 w-4 animate-spin" />
																		) : (
																			<RefreshCcw className="h-4 w-4" />
																		)}
																	</Button>
																</span>
															</TooltipTrigger>
															<TooltipContent>
																{redeliverHint(latest.outcome, endpoint?.disabled, redeliveringIds.has(latest.id))}
															</TooltipContent>
														</Tooltip>
													)}
												</TableCell>
											</TableRow>
											{hasResends &&
												expanded &&
												sends.map((send) => {
													const sendLatest = send.attempts[0];
													return (
														<TableRow key={send.key} className="bg-muted/30" data-testid={`webhook-delivery-send-${send.key}`}>
															<TableCell className="px-2"></TableCell>
															<TableCell colSpan={3}>
																<div className="flex items-center gap-2">
																	<span
																		className="border-border ml-1 inline-block size-2.5 rounded-bl-[3px] border-b border-l"
																		aria-hidden="true"
																	/>
																	<span className="text-sm font-medium">{send.label}</span>
																	<span className="text-muted-foreground text-xs tabular-nums">
																		{format(new Date(sendLatest.created_at), "MMM d, yyyy hh:mm:ss aa")}
																	</span>
																</div>
															</TableCell>
															<TableCell>{outcomeBadge(sendLatest)}</TableCell>
															<TableCell>{attemptSequence(send.attempts)}</TableCell>
															<TableCell></TableCell>
														</TableRow>
													);
												})}
										</Fragment>
									);
								})
							)}
						</TableBody>
					</Table>
				</div>
			</SheetContent>
		</Sheet>
	);
}