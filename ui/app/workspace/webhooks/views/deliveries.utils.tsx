import { Badge } from "@/components/ui/badge";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { WebhookDelivery, WebhookDeliveryOutcome } from "@/lib/types/webhooks";
import { Fragment } from "react";

export const OUTCOME_COLORS: Record<WebhookDeliveryOutcome, string> = {
	delivered: "bg-green-100 text-green-800",
	retryable_failure: "bg-yellow-100 text-yellow-800",
	permanent_failure: "bg-red-100 text-red-800",
	exhausted: "bg-red-100 text-red-800",
};

export const OUTCOME_LABELS: Record<WebhookDeliveryOutcome, string> = {
	delivered: "delivered",
	retryable_failure: "retrying",
	permanent_failure: "failed",
	exhausted: "retries exhausted",
};

// Wraps a badge with the attempt's error text as a tooltip when present.
export const withErrorTooltip = (badge: React.ReactNode, error?: string) => {
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
export const outcomeBadge = (attempt: WebhookDelivery) =>
	withErrorTooltip(
		<Badge variant="outline" className={OUTCOME_COLORS[attempt.outcome]}>
			{OUTCOME_LABELS[attempt.outcome]}
		</Badge>,
		attempt.error,
	);

// Colour a response status code by band: 2xx delivered, 429/5xx retryable,
// other 4xx permanent.
export const statusCodeClass = (code: number | undefined): string => {
	if (code && code >= 200 && code < 300) return "bg-green-100 text-green-800";
	if (code === 429 || (code && code >= 500)) return "bg-yellow-100 text-yellow-800";
	return "bg-red-100 text-red-800";
};

// A send's attempts, oldest first, as a "503 → 503 → 200" sequence of
// status-code chips. Failed attempts surface their error on hover. A response
// that never arrived (network error) has a zero status code, shown as a dash.
export const attemptSequence = (attemptsNewestFirst: WebhookDelivery[]) => (
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

/** One send of a delivery: the original, or a manual redelivery. */
export interface DeliverySend {
	key: string;
	label: string;
	attempts: WebhookDelivery[];
}

/** One logical delivery (`webhook_id`), with its sends newest-send-last. */
export interface DeliveryGroup {
	webhookId: string;
	latest: WebhookDelivery;
	latestSend: DeliverySend;
	sends: DeliverySend[];
}

// The history groups into two levels. Level 1 is one row per delivery
// (webhook_id): the notification owed to the endpoint for a job event.
// Level 2 is its sends: the original plus each manual redelivery, which
// reuse the webhook_id and restart attempt numbering, so a send boundary is
// where attempt_no stops decreasing. Attempts within a send stay inline as a
// status-code sequence. Rows arrive newest-first, so the delivery order and
// the newest attempt of each send both come for free.
export const groupDeliveries = (rows: WebhookDelivery[]): DeliveryGroup[] => {
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
};