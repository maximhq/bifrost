import { groupDeliveries } from "@/app/workspace/webhooks/views/deliveries.utils";
import { WebhookDeliveriesFilterSidebar } from "@/components/filters/webhookDeliveriesFilterSidebar";
import PageTitle from "@/components/pageTitle";
import { useColumnConfig } from "@/components/table";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import {
	getErrorMessage,
	useGetWebhookEndpointsQuery,
	useRedeliverWebhookDeliveryMutation,
	useSearchWebhookDeliveriesQuery,
} from "@/lib/store";
import { dateUtils } from "@/lib/types/logs";
import { parseAsSafeArrayOf, parseAsSafeString } from "@/lib/queryParamsParser";
import type { WebhookDeliveryFilters, WebhookDeliveryOutcome, WebhookDeliveryStatusClass, WebhookEvent } from "@/lib/types/webhooks";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useNavigate } from "@tanstack/react-router";
import { AlertCircle } from "lucide-react";
import { parseAsBoolean, parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useCallback, useMemo, useState } from "react";
import { toast } from "sonner";
import { normalizeDeliveriesPagination } from "./deliveries.page.utils";
import { createColumns, WEBHOOK_DELIVERY_COLUMN_LABELS } from "./views/columns";
import { DeliveriesTable, type DeliveriesPagination } from "./views/deliveriesTable";
import { DeliveriesHeaderView } from "./views/deliveriesHeaderView";

const COLUMN_IDS = ["expand", "time", "webhook", "delivery_id", "request_id", "event", "status", "responses", "actions"];

export default function WebhookDeliveriesPage() {
	const navigate = useNavigate();
	const canManage = useRbac(RbacResource.Governance, RbacOperation.Update);
	const { copy } = useCopyToClipboard();

	const defaultTimeRange = useMemo(() => dateUtils.getDefaultTimeRange(), []);

	const [urlState, setUrlState] = useQueryStates(
		{
			// `webhook_id` is the endpoint id — what the UI calls a webhook. The
			// delivery-run id is `delivery_id`. See WebhookDeliveryFilters.
			webhook_id: parseAsSafeArrayOf.withDefault([]),
			event: parseAsSafeArrayOf.withDefault([]),
			outcome: parseAsSafeArrayOf.withDefault([]),
			status_class: parseAsSafeArrayOf.withDefault([]),
			request_id: parseAsSafeString.withDefault(""),
			delivery_id: parseAsSafeString.withDefault(""),
			start_time: parseAsInteger.withDefault(defaultTimeRange.startTime),
			end_time: parseAsInteger.withDefault(defaultTimeRange.endTime),
			limit: parseAsInteger.withDefault(25),
			offset: parseAsInteger.withDefault(0),
			polling: parseAsBoolean.withDefault(false).withOptions({ clearOnDefault: false }),
			period: parseAsString.withDefault("24h").withOptions({ clearOnDefault: false }),
		},
		{ history: "push", shallow: false },
	);

	const polling = urlState.polling;

	// Both URL values are user-editable; clamp to a usable page size and a
	// non-negative, page-aligned offset so a hand-edited "?offset=-25" or
	// "?limit=0" never 400s the query and blocks recovery.
	const { limit, offset } = useMemo(
		() => normalizeDeliveriesPagination(urlState.limit, urlState.offset),
		[urlState.limit, urlState.offset],
	);

	const filters: WebhookDeliveryFilters = useMemo(
		() => ({
			endpoint_ids: urlState.webhook_id.length ? urlState.webhook_id : undefined,
			events: urlState.event.length ? (urlState.event as WebhookEvent[]) : undefined,
			outcomes: urlState.outcome.length ? (urlState.outcome as WebhookDeliveryOutcome[]) : undefined,
			status_class: urlState.status_class.length ? (urlState.status_class as WebhookDeliveryStatusClass[]) : undefined,
			request_id: urlState.request_id || undefined,
			delivery_id: urlState.delivery_id || undefined,
			// A period is resolved server-side on every request so a live view keeps
			// sliding; an absolute range is sent as stored timestamps instead.
			...(urlState.period
				? { period: urlState.period }
				: {
						start_time: dateUtils.toISOString(urlState.start_time),
						end_time: dateUtils.toISOString(urlState.end_time),
					}),
		}),
		[
			urlState.webhook_id,
			urlState.event,
			urlState.outcome,
			urlState.status_class,
			urlState.request_id,
			urlState.delivery_id,
			urlState.period,
			urlState.start_time,
			urlState.end_time,
		],
	);

	const pagination: DeliveriesPagination = useMemo(() => ({ limit, offset }), [limit, offset]);

	const { data, isLoading, isFetching, error, refetch } = useSearchWebhookDeliveriesQuery(
		{ ...filters, limit: pagination.limit, offset: pagination.offset },
		{ pollingInterval: polling ? 10000 : 0, skipPollingIfUnfocused: true },
	);

	// Endpoint names for the Webhook column. History outlives its endpoint, so
	// the column falls back to the raw id when a lookup misses.
	const { data: endpointsData } = useGetWebhookEndpointsQuery({ limit: 100, offset: 0 });
	const endpointNames = useMemo(
		() => new Map((endpointsData?.endpoints ?? []).map((endpoint) => [endpoint.id, endpoint.name || endpoint.url])),
		[endpointsData],
	);

	const deliveries = useMemo(() => groupDeliveries(data?.deliveries ?? []), [data]);
	const totalItems = data?.pagination.total_count ?? 0;

	const [expandedIds, setExpandedIds] = useState<Set<string>>(new Set());
	const toggleExpanded = useCallback((webhookId: string) => {
		setExpandedIds((prev) => {
			const next = new Set(prev);
			if (next.has(webhookId)) {
				next.delete(webhookId);
			} else {
				next.add(webhookId);
			}
			return next;
		});
	}, []);

	const [redeliverWebhookDelivery] = useRedeliverWebhookDeliveryMutation();
	const [redeliveringIds, setRedeliveringIds] = useState<Set<string>>(new Set());

	const handleRedeliver = useCallback(
		async (group: { latest: { id: string } }) => {
			const deliveryId = group.latest.id;
			setRedeliveringIds((prev) => new Set(prev).add(deliveryId));
			try {
				await redeliverWebhookDelivery(deliveryId).unwrap();
				toast.success("Delivery re-queued");
			} catch (err) {
				toast.error(getErrorMessage(err));
			} finally {
				setRedeliveringIds((prev) => {
					const next = new Set(prev);
					next.delete(deliveryId);
					return next;
				});
			}
		},
		[redeliverWebhookDelivery],
	);

	const handleCopy = useCallback(
		(value: string) => {
			copy(value);
			toast.success("Copied to clipboard");
		},
		[copy],
	);

	const handleOpenRequest = useCallback(
		(requestId: string) => {
			navigate({ to: "/workspace/logs", search: { request_id: requestId } as never });
		},
		[navigate],
	);

	const setFilters = useCallback(
		(newFilters: WebhookDeliveryFilters) => {
			const timeChanged = newFilters.start_time !== undefined || newFilters.end_time !== undefined;
			setUrlState({
				...(timeChanged && { period: "" }),
				webhook_id: newFilters.endpoint_ids ?? [],
				event: newFilters.events ?? [],
				outcome: newFilters.outcomes ?? [],
				status_class: newFilters.status_class ?? [],
				request_id: newFilters.request_id ?? "",
				delivery_id: newFilters.delivery_id ?? "",
				start_time: newFilters.start_time ? dateUtils.toUnixTimestamp(new Date(newFilters.start_time)) : undefined,
				end_time: newFilters.end_time ? dateUtils.toUnixTimestamp(new Date(newFilters.end_time)) : undefined,
				// Any filter edit invalidates the current page.
				offset: 0,
			});
		},
		[setUrlState],
	);

	const setPagination = useCallback(
		(newPagination: DeliveriesPagination) => {
			setUrlState({ limit: newPagination.limit, offset: newPagination.offset });
		},
		[setUrlState],
	);

	const handlePeriodChange = useCallback(
		(period?: string, from?: Date, to?: Date) => {
			setUrlState({
				period: period ?? "",
				start_time: from ? dateUtils.toUnixTimestamp(from) : undefined,
				end_time: to ? dateUtils.toUnixTimestamp(to) : undefined,
				offset: 0,
			});
		},
		[setUrlState],
	);

	const {
		entries: columnEntries,
		columnOrder,
		columnVisibility,
		columnPinning,
		toggleVisibility,
		togglePin,
		reorder,
		reset: resetColumns,
	} = useColumnConfig({
		columnIds: COLUMN_IDS,
		paramName: "wd_cols",
		storageKey: "bifrost.webhookDeliveries.cols",
		fixedColumns: { left: ["expand"], right: ["actions"] },
	});

	const columns = useMemo(
		() =>
			createColumns({
				endpointNames,
				expandedIds,
				onToggleExpanded: toggleExpanded,
				onCopy: handleCopy,
				onRedeliver: handleRedeliver,
				redeliveringIds,
				canManage,
				onOpenRequest: handleOpenRequest,
			}),
		[endpointNames, expandedIds, toggleExpanded, handleCopy, handleRedeliver, redeliveringIds, canManage, handleOpenRequest],
	);

	const displayError = error ? getErrorMessage(error) : undefined;

	return (
		<div className="no-padding-parent no-border-parent bg-background flex h-[calc(var(--app-content-viewport)_-_var(--app-bottom-padding))] w-full gap-3">
			{/* The trail replaces the old in-page back button: navigating up is the
			    topbar's job, and it frees the header row for the filters. */}
			<PageTitle breadcrumbs={[{ label: "Webhooks", to: "/workspace/webhooks" }, { label: "Deliveries" }]} />
			<WebhookDeliveriesFilterSidebar filters={filters} onFiltersChange={setFilters} />

			<div className="bg-card flex min-w-0 flex-1 flex-col gap-2 overflow-hidden rounded-md border">
				<div className="flex items-center gap-2 p-4 pb-0">
					<DeliveriesHeaderView
						filters={filters}
						onFiltersChange={setFilters}
						period={urlState.period}
						onPeriodChange={handlePeriodChange}
						polling={polling}
						onPollToggle={(enabled) => setUrlState({ polling: enabled })}
						onRefresh={refetch}
						loading={isFetching}
						columnEntries={columnEntries}
						columnLabels={WEBHOOK_DELIVERY_COLUMN_LABELS}
						onToggleColumnVisibility={toggleVisibility}
						onResetColumns={resetColumns}
					/>
				</div>

				{displayError && (
					<div className="px-4">
						<Alert variant="destructive" className="shrink-0">
							<AlertCircle className="h-4 w-4" />
							<AlertDescription>{displayError}</AlertDescription>
						</Alert>
					</div>
				)}

				<DeliveriesTable
					columns={columns}
					data={deliveries}
					totalItems={totalItems}
					loading={isLoading || isFetching}
					error={error}
					pagination={pagination}
					onPaginationChange={setPagination}
					onRefresh={refetch}
					polling={polling}
					expandedIds={expandedIds}
					columnEntries={columnEntries}
					columnOrder={columnOrder}
					columnVisibility={columnVisibility}
					columnPinning={columnPinning}
					onToggleColumnVisibility={toggleVisibility}
					onTogglePin={togglePin}
					onReorderColumns={reorder}
				/>
			</div>
		</div>
	);
}