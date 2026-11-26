import type { Badge } from "@/components/ui/badge";
import i18n from "@/lib/i18n";
import type { GuardrailMatch, GuardrailMatchAction } from "@/lib/types/logs";
import type { ComponentProps } from "react";

export type { GuardrailMatchAction } from "@/lib/types/logs";

// English fallback only; the UI renders logs.detail.guardrailActions.<action> through t().
export const GUARDRAIL_MATCH_ACTION_LABEL: Record<GuardrailMatchAction, string> = {
	blocked: "Blocked",
	redacted: "Redacted",
	monitored: "Monitored",
	detected: "Detected",
};

export const GUARDRAIL_MATCH_ACTION_BADGE: Record<GuardrailMatchAction, ComponentProps<typeof Badge>["variant"]> = {
	blocked: "destructive",
	redacted: "secondary",
	monitored: "outline",
	detected: "warning",
};

// A provider rewriting the matched content is a redaction to users, whichever way the provider reports it.
const ENFORCED_ACTIONS: Record<string, GuardrailMatchAction> = { block: "blocked", redact: "redacted", transform: "redacted" };

/**
 * guardrailMatchAction names what a guardrail did for one recorded match. An
 * intervention always blocked the request. A detection is labelled by its
 * action, a provider rewrite counting as a redaction, except that a match an
 * output-monitor rule recorded without acting on it (enforced=false) is
 * "monitored" whatever it would have done. The logs API's guardrail_actions
 * filter labels matches the same way.
 */
export function guardrailMatchAction(match: GuardrailMatch, intervention: boolean): GuardrailMatchAction {
	if (intervention) return "blocked";
	if (match.enforced === false) return "monitored";
	switch (match.action) {
		case "block":
			return "blocked";
		case "redact":
		case "transform":
			return "redacted";
		default:
			return "detected";
	}
}

/** guardrailMatchActionHint explains an action in one sentence, for tooltips. */
export function guardrailMatchActionHint(match: GuardrailMatch, action: GuardrailMatchAction): string {
	switch (action) {
		case "blocked":
			return i18n.t("logs.detail.guardrailHints.blocked", { ns: "observability" });
		case "redacted":
			return i18n.t("logs.detail.guardrailHints.redacted", { ns: "observability" });
		case "monitored": {
			switch (match.action ? ENFORCED_ACTIONS[match.action] : undefined) {
				case "blocked":
					return i18n.t("logs.detail.guardrailHints.monitoredWouldBlock", { ns: "observability" });
				case "redacted":
					return i18n.t("logs.detail.guardrailHints.monitoredWouldRedact", { ns: "observability" });
				default:
					return i18n.t("logs.detail.guardrailHints.monitored", { ns: "observability" });
			}
		}
		default:
			return i18n.t("logs.detail.guardrailHints.detected", { ns: "observability" });
	}
}