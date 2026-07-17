import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { getErrorMessage, useGetWebhookDeliveriesQuery, useRedeliverWebhookDeliveryMutation } from "@/lib/store";
import {
	WEBHOOK_EVENT_COLORS,
	WEBHOOK_TUNING_DEFAULTS,
	WebhookDelivery,
	WebhookDeliveryOutcome,
	WebhookEndpoint,
	WebhookEvent,
} from "@/lib/types/webhooks";
import { format, formatDistanceToNow } from "date-fns";
import { ChevronDown, ChevronLeft, ChevronRight, Info, Loader2, RefreshCcw, Send } from "lucide-react";
import { Fragment, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

const PAGE_SIZE = 25;

const OUTCOME_COLORS: Record<WebhookDeliveryOutcome, string> = {
	delivered: "bg-green-100 text-green-800",
	retryable_failure: "bg-yellow-100 text-yellow-800",
	permanent_failure: "bg-red-100 text-red-800",
	exhausted: "bg-red-100 text-red-800",
};

const OUTCOME_LABELS: Record<WebhookDeliveryOutcome, string> = {
	delivered: "delivered",
	retryable_failure: "retrying",
	permanent_failure: "failed",
	exhausted: "retries exhausted",
};

const DetailEntry = ({ label, value }: { label: string; value: React.ReactNode }) => (
	<div>
		<div className="text-muted-foreground text-xs">{label}</div>
		<div className="text-sm font-medium break-all">{value}</div>
	</div>
);

const relativeTime = (timestamp?: string) => (timestamp ? formatDistanceToNow(new Date(timestamp), { addSuffix: true }) : "never");

// Wraps a badge with the attempt's error text as a tooltip when present.
const withErrorTooltip = (badge: React.ReactNode, error?: string) => {
	if (!error) {
		return badge;
	}
	return (
		<Tooltip>
			<TooltipTrigger>{badge}</TooltipTrigger>
			{/* text-wrap overrides the component's text-balance, which leaves a
			    right-side gap by shortening lines inside a full-width box. */}
			<TooltipContent className="max-w-[400px] text-wrap break-words">{error}</TooltipContent>
		</Tooltip>
	);
};

// Run-level outcome: where the delivery as a whole stands.
const outcomeBadge = (attempt: WebhookDelivery) =>
	withErrorTooltip(
		<Badge variant="outline" className={OUTCOME_COLORS[attempt.outcome]}>
			{OUTCOME_LABELS[attempt.outcome]}
		</Badge>,
		attempt.error,
	);

// Colour a response status code by band: 2xx delivered, 429/5xx retryable,
// other 4xx permanent.
const statusCodeClass = (code: number | undefined): string => {
	if (code && code >= 200 && code < 300) return "bg-green-100 text-green-800";
	if (code === 429 || (code && code >= 500)) return "bg-yellow-100 text-yellow-800";
	return "bg-red-100 text-red-800";
};

// A send's attempts, oldest first, as a "503 → 503 → 200" sequence of
// status-code chips. Failed attempts surface their error on hover. A response
// that never arrived (network error) has a zero status code, shown as a dash.
const attemptSequence = (attemptsNewestFirst: WebhookDelivery[]) => (
	<div className="flex items-center gap-1">
		{[...attemptsNewestFirst].reverse().map((attempt, index) => (
			<Fragment key={attempt.id}>
				{index > 0 && <span className="text-muted-foreground text-xs">→</span>}
				{withErrorTooltip(
					<Badge variant="outline" className={`font-mono text-xs ${statusCodeClass(attempt.status_code)}`}>
						{attempt.status_code || "-"}
					</Badge>,
					attempt.error,
				)}
			</Fragment>
		))}
	</div>
);

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
	const [offset, setOffset] = useState(0);
	const [redeliverWebhookDelivery] = useRedeliverWebhookDeliveryMutation();
	const [redeliveringIds, setRedeliveringIds] = useState<Set<string>>(new Set());
	const { copy } = useCopyToClipboard();

	useEffect(() => {
		setOffset(0);
	}, [endpoint?.id]);

	const { data, isLoading, isError } = useGetWebhookDeliveriesQuery(
		{ endpointId: endpoint?.id ?? "", limit: PAGE_SIZE, offset },
		{ skip: !open, pollingInterval: 5000 },
	);
	const totalCount = data?.pagination.total_count ?? 0;

	// The history groups into two levels. Level 1 is one row per delivery
	// (webhook_id): the notification owed to the endpoint for a job event.
	// Level 2 is its sends: the original plus each manual redelivery, which
	// reuse the webhook_id and restart attempt numbering, so a send boundary is
	// where attempt_no stops decreasing. Attempts within a send stay inline as a
	// status-code sequence. Rows arrive newest-first, so the delivery order and
	// the newest attempt of each send both come for free.
	const deliveries = useMemo(() => {
		const rows = data?.deliveries ?? [];
		const order: string[] = [];
		const byWebhookId = new Map<string, WebhookDelivery[]>();
		for (const row of rows) {
			const existing = byWebhookId.get(row.webhook_id);
			if (existing) {
				existing.push(row);
			} else {
				byWebhookId.set(row.webhook_id, [row]);
				order.push(row.webhook_id);
			}
		}
		return order.map((webhookId) => {
			const attempts = byWebhookId.get(webhookId) ?? [];
			// Split the newest-first attempts into sends at each attempt-number restart.
			const sendsNewestFirst: WebhookDelivery[][] = [];
			for (const attempt of attempts) {
				const current = sendsNewestFirst[sendsNewestFirst.length - 1];
				if (current && attempt.attempt_no < current[current.length - 1].attempt_no) {
					current.push(attempt);
				} else {
					sendsNewestFirst.push([attempt]);
				}
			}
			// The oldest send is the original; label the rest as redeliveries in order.
			const sends = [...sendsNewestFirst].reverse().map((sendAttempts, index) => ({
				key: `${webhookId}:${index}`,
				label: index === 0 ? "Original" : `Redelivery ${index}`,
				attempts: sendAttempts,
			}));
			return { webhookId, latest: attempts[0], latestSend: sends[sends.length - 1], sends };
		});
	}, [data]);

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
			<SheetContent className="flex w-full flex-col gap-0 overflow-x-hidden p-8 sm:max-w-[60%]">
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
					<div className="grid grid-cols-3 gap-4">
						<DetailEntry
							label="Events"
							value={
								<div className="flex flex-wrap gap-1">
									{endpoint?.events.map((event) => (
										<Badge key={event} variant="outline" className={`font-mono text-xs ${WEBHOOK_EVENT_COLORS[event]}`}>
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
					<h3 className="font-semibold">Delivery History</h3>
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
													<Badge variant="outline" className={`font-mono text-xs ${WEBHOOK_EVENT_COLORS[latest.event]}`}>
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
														<Button
															variant="ghost"
															size="sm"
															onClick={() => handleRedeliver(latest.id)}
															disabled={redeliveringIds.has(latest.id) || latest.outcome === "retryable_failure" || endpoint?.disabled}
															data-testid={`webhook-redeliver-btn-${webhookId}`}
															aria-label="Redeliver"
														>
															{redeliveringIds.has(latest.id) ? (
																<Loader2 className="h-4 w-4 animate-spin" />
															) : (
																<RefreshCcw className="h-4 w-4" />
															)}
														</Button>
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

				{totalCount > 0 && (
					<div className="flex shrink-0 items-center justify-between text-xs" data-testid="webhook-delivery-pagination">
						<div className="text-muted-foreground flex items-center gap-2">
							{(offset + 1).toLocaleString()}-{Math.min(offset + PAGE_SIZE, totalCount).toLocaleString()} of {totalCount.toLocaleString()}{" "}
							deliveries
						</div>
						<div className="flex items-center gap-2">
							<Button
								variant="ghost"
								size="sm"
								onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
								disabled={offset === 0}
								aria-label="Previous page"
							>
								<ChevronLeft className="size-3" />
							</Button>
							<div className="flex items-center gap-1">
								<span>Page</span>
								<span>{Math.floor(offset / PAGE_SIZE) + 1}</span>
								<span>of {Math.ceil(totalCount / PAGE_SIZE)}</span>
							</div>
							<Button
								variant="ghost"
								size="sm"
								onClick={() => setOffset(offset + PAGE_SIZE)}
								disabled={offset + PAGE_SIZE >= totalCount}
								aria-label="Next page"
							>
								<ChevronRight className="size-3" />
							</Button>
						</div>
					</div>
				)}
			</SheetContent>
		</Sheet>
	);
}