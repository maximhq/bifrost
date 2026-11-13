import { Button } from "@/components/ui/button";
import { type ProviderSelectorOption } from "@/components/ui/providerSelector";
import { resolveProviderIconKey } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";
import { COMPLEXITY_TIER_VALUES, DecisionTier, ModelRouterTarget, TIER_PHRASE_LIST_DEFINITIONS } from "@/lib/types/complexityRouter";
import { DBKey } from "@/lib/types/governance";
import { DEFAULT_ROUTING_FALLBACK } from "@/lib/types/routingRules";
import { denormalizeFallback, normalizeFallback } from "@/lib/utils/routingRules";
import type { TFunction } from "i18next";
import { Plus } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { FallbackRow, TargetRow } from "./formParts";
import { addTargetTo, removeTargetAt, TierTargetFormData, TierTargetsUpdate, updateTargetAt } from "./tierTargets";

export type { TierTargetFormData, TierTargetsUpdate } from "./tierTargets";

export type TierTargetErrors = Partial<Record<DecisionTier, string>>;

export function toTierTargetForms(targets: ModelRouterTarget[] | undefined): TierTargetFormData[] {
	return (targets ?? []).map((target) => ({
		tier: target.tier,
		provider: target.provider ?? "",
		model: target.model ?? "",
		key_id: target.key_id ?? "",
		weight: target.weight,
		fallbacks: (target.fallbacks ?? []).map(normalizeFallback),
	}));
}

export function toTierTargetPayload(targets: TierTargetFormData[]): ModelRouterTarget[] {
	return targets.map((target) => ({
		tier: target.tier,
		provider: target.provider || undefined,
		model: target.model || undefined,
		key_id: target.key_id || undefined,
		weight: target.weight,
		fallbacks: target.fallbacks.filter((fallback) => fallback.provider.trim()).map(denormalizeFallback),
	}));
}

// Checks the targets the way the gateway does, so each error shows next to
// the tier it is about instead of after a round trip.
export function validateTierTargets(targets: TierTargetFormData[], t: TFunction<"models">): TierTargetErrors {
	const tiers: TierTargetErrors = {};
	for (const tier of COMPLEXITY_TIER_VALUES) {
		const inTier = targets.filter((target) => target.tier === tier);
		if (inTier.length === 0) continue;
		if (inTier.some((target) => !target.provider.trim() && !target.model.trim())) {
			tiers[tier] = t("routing.modelRouters.models.providerOrModel");
		} else if (inTier.some((target) => !(target.weight > 0))) {
			tiers[tier] = t("routing.modelRouters.models.weightPositive");
		} else {
			const total = inTier.reduce((sum, target) => sum + target.weight, 0);
			if (Math.abs(total - 1) > 0.001) tiers[tier] = t("routing.modelRouters.models.weightsMustSum", { total: total.toFixed(4) });
		}
	}
	return tiers;
}

const TIER_DEFINITIONS: Record<DecisionTier, (typeof TIER_PHRASE_LIST_DEFINITIONS)[number]> = {
	SIMPLE: TIER_PHRASE_LIST_DEFINITIONS[0],
	MEDIUM: TIER_PHRASE_LIST_DEFINITIONS[1],
	COMPLEX: TIER_PHRASE_LIST_DEFINITIONS[2],
};

interface TierTargetsEditorProps {
	targets: TierTargetFormData[];
	// Receives each edit as an update of the latest list; see TierTargetsUpdate.
	onChange: (update: TierTargetsUpdate) => void;
	errors: TierTargetErrors;
	// Names of the configured providers; providers a target names that are no
	// longer configured stay offerable, so opening a router never blanks a target.
	configuredProviders: string[];
	keys: DBKey[];
	disabled?: boolean;
}

// TierTargetsEditor picks the model each tier gets: one row per tier, each
// with its models, the weights that split the tier between them, and each
// model's fallbacks.
export function TierTargetsEditor({ targets, onChange, errors, configuredProviders, keys, disabled }: TierTargetsEditorProps) {
	const { t } = useTranslation("models");
	const { t: tmr } = useTranslation("models", { keyPrefix: "routing.modelRouters.models" });

	const referencedProviderOptions = useMemo<ProviderSelectorOption[]>(() => {
		const configured = new Set(configuredProviders);
		const referenced = new Set<string>();
		for (const target of targets) {
			if (target.provider) referenced.add(target.provider);
			for (const fallback of target.fallbacks) if (fallback.provider) referenced.add(fallback.provider);
		}
		return Array.from(referenced)
			.filter((name) => !configured.has(name))
			.map((name) => ({ value: name, label: getProviderLabel(name), iconKey: resolveProviderIconKey(name) }));
	}, [targets, configuredProviders]);

	// Targets are addressed by their place in the whole list; each tier renders
	// its own slice of it.
	const updateTarget = (index: number, changes: Partial<TierTargetFormData>) => onChange(updateTargetAt(index, changes));
	const addTarget = (tier: DecisionTier) => onChange(addTargetTo(tier));
	const removeTarget = (index: number) => onChange(removeTargetAt(index));

	return (
		<div className="overflow-hidden rounded-sm border" data-testid="model-router-targets">
			{COMPLEXITY_TIER_VALUES.map((tier) => {
				const tierKey = tier.toLowerCase();
				const definition = TIER_DEFINITIONS[tier];
				const inTier = targets.map((target, index) => ({ target, index })).filter(({ target }) => target.tier === tier);
				return (
					<div
						key={tier}
						className="grid gap-4 border-b p-4 last:border-b-0 md:grid-cols-[minmax(0,14rem)_1fr]"
						data-testid={`model-router-tier-${tierKey}`}
					>
						<div className="space-y-1">
							<p className="text-sm font-medium">{definition.label}</p>
							<p className="text-muted-foreground text-xs leading-relaxed">{definition.description}</p>
						</div>
						<div className="min-w-0 space-y-3">
							{inTier.length === 0 && <p className="text-muted-foreground text-sm">{tmr("tierKeepsModel")}</p>}
							{inTier.map(({ target, index }, position) => {
								const prefix = `model-routing-target-${tierKey}-${position}`;
								return (
									<div key={`${tier}-${index}`} className="space-y-2">
										<TargetRow
											target={target}
											index={position}
											idPrefix={prefix}
											referencedProviderOptions={referencedProviderOptions}
											allKeys={keys}
											showRemove={!disabled}
											onUpdate={(_, field, value) => updateTarget(index, { [field]: value } as Partial<TierTargetFormData>)}
											onRemove={() => removeTarget(index)}
										/>
										<div className="ml-3 space-y-2 border-l pl-3">
											{target.fallbacks.map((fallback, fallbackIndex) => (
												<FallbackRow
													key={`${prefix}-fallback-${fallbackIndex}`}
													fallback={fallback}
													index={fallbackIndex}
													idPrefix={`${prefix}-fallback-${fallbackIndex}`}
													referencedProviderOptions={referencedProviderOptions}
													allKeys={keys}
													onUpdate={(_, changes) =>
														onChange((current) =>
															updateTargetAt(index, {
																fallbacks: current[index].fallbacks.map((item, i) =>
																	i === fallbackIndex ? { ...item, ...changes } : item,
																),
															})(current),
														)
													}
													onRemove={() =>
														onChange((current) =>
															updateTargetAt(index, { fallbacks: current[index].fallbacks.filter((_, i) => i !== fallbackIndex) })(current),
														)
													}
												/>
											))}
											<Button
												type="button"
												variant="ghost"
												size="sm"
												disabled={disabled}
												onClick={() =>
													onChange((current) =>
														updateTargetAt(index, { fallbacks: [...current[index].fallbacks, { ...DEFAULT_ROUTING_FALLBACK }] })(current),
													)
												}
												data-testid={`${prefix}-add-fallback-button`}
											>
												<Plus className="size-3.5" />
												{t("routing.addFallback")}
											</Button>
										</div>
									</div>
								);
							})}
							<Button
								type="button"
								variant="outline"
								size="sm"
								disabled={disabled}
								onClick={() => addTarget(tier)}
								data-testid={`model-router-tier-${tierKey}-add-button`}
							>
								<Plus className="size-3.5" />
								{tmr("addModel")}
							</Button>
							{errors[tier] && (
								<p className="text-destructive text-sm" data-testid={`model-router-tier-${tierKey}-error`}>
									{errors[tier]}
								</p>
							)}
						</div>
					</div>
				);
			})}
		</div>
	);
}