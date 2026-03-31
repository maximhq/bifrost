"use client";

import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage, setProviderFormDirtyState, useAppDispatch } from "@/lib/store";
import { useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import { ModelProvider } from "@/lib/types/config";
import { providerPricingOverrideSchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { z } from "zod";

interface PricingOverridesFormFragmentProps {
	provider: ModelProvider;
}

const pricingOverridesArraySchema = z.array(providerPricingOverrideSchema);

const toPrettyJSON = (value: unknown) => JSON.stringify(value, null, 2);

export function PricingOverridesFormFragment({ provider }: PricingOverridesFormFragmentProps) {
	const dispatch = useAppDispatch();
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();
	const initialValue = useMemo(() => toPrettyJSON(provider.pricing_overrides ?? []), [provider.pricing_overrides]);
	const [overridesJSON, setOverridesJSON] = useState(initialValue);
	const [validationError, setValidationError] = useState<string>("");
	const [hasUserEdits, setHasUserEdits] = useState(false);
	const isDirty = hasUserEdits && overridesJSON !== initialValue;

	useEffect(() => {
		if (isDirty) {
			return;
		}
		setOverridesJSON(initialValue);
		setValidationError("");
	}, [initialValue, isDirty, provider.name]);

	useEffect(() => {
		dispatch(setProviderFormDirtyState(isDirty));
	}, [dispatch, isDirty]);

	const onReset = () => {
		setOverridesJSON(initialValue);
		setValidationError("");
		setHasUserEdits(false);
	};

	const onSave = async () => {
		let parsed: unknown;
		try {
			parsed = JSON.parse(overridesJSON);
		} catch {
			setValidationError("Invalid JSON format.");
			return;
		}

		const validated = pricingOverridesArraySchema.safeParse(parsed);
		if (!validated.success) {
			setValidationError(validated.error.issues[0]?.message || "Invalid pricing overrides configuration.");
			return;
		}

		setValidationError("");

		try {
			await updateProvider({
				...provider,
			}).unwrap();
			toast.success("Pricing overrides updated successfully");
			setOverridesJSON(toPrettyJSON(validated.data));
			setHasUserEdits(false);
		} catch (err) {
			toast.error("Failed to update pricing overrides", {
				description: getErrorMessage(err),
			});
		}
	};

	return (
		<div className="space-y-4 px-6 pb-6">
			<div className="space-y-1">
				<p className="text-sm font-medium">Provider Pricing Overrides</p>
				<p className="text-muted-foreground text-xs">
					Enter a JSON array of override objects. Match precedence is exact &gt; wildcard &gt; regex. Unspecified fields fall back to
					datasheet pricing.
				</p>
			</div>

			<Textarea
				data-testid="provider-pricing-overrides-json-input"
				value={overridesJSON}
				onChange={(event) => {
					setOverridesJSON(event.target.value);
					setHasUserEdits(true);
				}}
				rows={18}
				className="font-mono text-xs"
				disabled={!hasUpdateProviderAccess}
				placeholder={`[
  {
    "model_pattern": "gpt-4o*",
    "match_type": "wildcard",
    "request_types": ["chat_completion"],
    "input_cost_per_token": 0.000005,
    "output_cost_per_token": 0.000015
  }
]`}
			/>

			{validationError ? <p className="text-destructive text-xs">{validationError}</p> : null}

			<div className="flex justify-end gap-2">
				<Button
					type="button"
					variant="outline"
					data-testid="provider-pricing-overrides-reset-button"
					onClick={onReset}
					disabled={!hasUpdateProviderAccess || !isDirty}
				>
					Reset
				</Button>
				<Button
					type="button"
					data-testid="provider-pricing-overrides-save-button"
					onClick={onSave}
					isLoading={isUpdatingProvider}
					disabled={!hasUpdateProviderAccess || !isDirty}
				>
					Save Pricing Overrides
				</Button>
			</div>
		</div>
	);
}
