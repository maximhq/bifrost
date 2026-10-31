import type { Badge } from "@/components/ui/badge";
import type { GuardrailMatch } from "@/lib/types/logs";
import type { ComponentProps } from "react";

/** What a guardrail did to a request, as shown to users. */
export type GuardrailMatchAction = "blocked" | "redacted" | "transformed" | "monitored" | "detected";

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

const ENFORCED_VERB: Record<string, string> = { block: "blocked", redact: "redacted", transform: "transformed" };

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
			return "The rule stopped the request.";
		case "redacted":
			return "The rule removed the matched content and the request continued.";
		case "transformed":
			return "The rule rewrote the matched content and the request continued.";
		case "monitored": {
			const verb = match.action ? ENFORCED_VERB[match.action] : undefined;
			return verb
				? `The rule only monitors: it would have ${verb} this, but the request continued unchanged.`
				: "The rule only monitors: the request continued unchanged.";
		}
		default:
			return "The rule flagged the request and let it continue unchanged.";
	}
}