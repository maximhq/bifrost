import { DecisionTier } from "@/lib/types/complexityRouter";
import { DEFAULT_ROUTING_TARGET, RoutingFallbackFormData, RoutingTargetFormData } from "@/lib/types/routingRules";

// A target in the form: one model for one tier, with the fallbacks that go
// with it.
export interface TierTargetFormData extends RoutingTargetFormData {
	tier: DecisionTier;
	fallbacks: RoutingFallbackFormData[];
}

// An edit to the targets, applied to the latest list. One user action can
// make several edits in a row, as picking a provider sets it and clears the
// model and key, so each edit must build on the one before it rather than on
// the list the form last rendered.
export type TierTargetsUpdate = (targets: TierTargetFormData[]) => TierTargetFormData[];

// Changes the fields of the target at index, its place in the whole list.
export const updateTargetAt =
	(index: number, changes: Partial<TierTargetFormData>): TierTargetsUpdate =>
	(targets) =>
		targets.map((target, i) => (i === index ? { ...target, ...changes } : target));

// Adds a target to a tier, weighted with whatever the tier's other targets
// leave of 1.
export const addTargetTo =
	(tier: DecisionTier): TierTargetsUpdate =>
	(targets) => {
		const used = targets.filter((target) => target.tier === tier).reduce((sum, target) => sum + (target.weight || 0), 0);
		return [...targets, { ...DEFAULT_ROUTING_TARGET, tier, weight: Math.max(0, parseFloat((1 - used).toFixed(4))), fallbacks: [] }];
	};

// Removes the target at index.
export const removeTargetAt =
	(index: number): TierTargetsUpdate =>
	(targets) =>
		targets.filter((_, i) => i !== index);