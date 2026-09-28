import type { Badge } from "@/components/ui/badge";
import i18n from "@/lib/i18n";
import type { GuardrailMatch } from "@/lib/types/logs";
import type { ComponentProps } from "react";

/** What a guardrail did to a request, as shown to users. */
export type GuardrailMatchAction = "blocked" | "redacted" | "transformed" | "monitored" | "detected";

// English fallback only; the UI renders logs.detail.guardrailActions.<action> through t().
export const GUARDRAIL_MATCH_ACTION_LABEL: Record<GuardrailMatchAction, string> = {
	blocked: "Blocked",
	redacted: "Redacted",
	transformed: "Transformed",
	monitored: "Monitored",
	detected: "Detected",
};

export const GUARDRAIL_MATCH_ACTION_BADGE: Record<GuardrailMatchAction, ComponentProps<typeof Badge>["variant"]> = {
	blocked: "destructive",
	redacted: "secondary",
	transformed: "secondary",
	monitored: "outline",
	detected: "warning",
};

const ENFORCED_ACTIONS: Record<string, GuardrailMatchAction> = { block: "blocked", redact: "redacted", transform: "transformed" };

/**
 * guardrailMatchAction names what a guardrail did for one recorded match. An
 * intervention always blocked the request. A detection is labelled by its
 * action, except that a match an output-monitor rule recorded without acting
 * on it (enforced=false) is "monitored" whatever it would have done.
 */
export function guardrailMatchAction(match: GuardrailMatch, intervention: boolean): GuardrailMatchAction {
	if (intervention) return "blocked";
	if (match.enforced === false) return "monitored";
	switch (match.action) {
		case "block":
			return "blocked";
		case "redact":
			return "redacted";
		case "transform":
			return "transformed";
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
		case "transformed":
			return i18n.t("logs.detail.guardrailHints.transformed", { ns: "observability" });
		case "monitored": {
			switch (match.action ? ENFORCED_ACTIONS[match.action] : undefined) {
				case "blocked":
					return i18n.t("logs.detail.guardrailHints.monitoredWouldBlock", { ns: "observability" });
				case "redacted":
					return i18n.t("logs.detail.guardrailHints.monitoredWouldRedact", { ns: "observability" });
				case "transformed":
					return i18n.t("logs.detail.guardrailHints.monitoredWouldTransform", { ns: "observability" });
				default:
					return i18n.t("logs.detail.guardrailHints.monitored", { ns: "observability" });
			}
		}
		default:
			return i18n.t("logs.detail.guardrailHints.detected", { ns: "observability" });
	}
}