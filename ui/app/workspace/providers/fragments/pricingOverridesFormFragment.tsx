"use client";

import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormMessage } from "@/components/ui/form";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage, setProviderFormDirtyState, useAppDispatch } from "@/lib/store";
import { useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import { ModelProvider } from "@/lib/types/config";
import { providerPricingOverrideSchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { useForm, type Resolver } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";

interface PricingOverridesFormFragmentProps {
	provider: ModelProvider;
}

const pricingOverridesArraySchema = z.array(providerPricingOverrideSchema);

const toPrettyJSON = (value: unknown) => JSON.stringify(value, null, 2);

// Form schema that takes a JSON string and validates it as pricing overrides array
const pricingOverridesFormSchema = z.object({
	pricing_overrides_json: z.string().refine(
		(value) => {
			try {
				const parsed = JSON.parse(value);
				const result = pricingOverridesArraySchema.safeParse(parsed);
				return result.success;
			} catch {
				return false;
			}
		},
		{
			message: "Invalid JSON format or pricing overrides structure",
		}
	),
});

type PricingOverridesFormSchema = z.infer<typeof pricingOverridesFormSchema>;

export function PricingOverridesFormFragment({ provider }: PricingOverridesFormFragmentProps) {
	const dispatch = useAppDispatch();
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();

	const form = useForm<PricingOverridesFormSchema, any, PricingOverridesFormSchema>({
		resolver: zodResolver(pricingOverridesFormSchema) as Resolver<PricingOverridesFormSchema, any, PricingOverridesFormSchema>,
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: {
			pricing_overrides_json: toPrettyJSON(provider.pricing_overrides ?? []),
		},
	});

	useEffect(() => {
		dispatch(setProviderFormDirtyState(form.formState.isDirty));
	}, [form.formState.isDirty, dispatch]);

	useEffect(() => {
		form.reset({
			pricing_overrides_json: toPrettyJSON(provider.pricing_overrides ?? []),
		});
	}, [form, provider.name, provider.pricing_overrides]);

	const onSubmit = (data: PricingOverridesFormSchema) => {
		const parsed = JSON.parse(data.pricing_overrides_json);
		const validated = pricingOverridesArraySchema.parse(parsed);

		const updatedProvider: ModelProvider = {
			...provider,
			pricing_overrides: validated,
		};

		updateProvider(updatedProvider)
			.unwrap()
			.then(() => {
				toast.success("Pricing overrides updated successfully");
				form.reset({ pricing_overrides_json: toPrettyJSON(validated) });
			})
			.catch((err) => {
				toast.error("Failed to update pricing overrides", {
					description: getErrorMessage(err),
				});
			});
	};

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4 px-6 pb-6">
				<div className="space-y-1">
					<p className="text-sm font-medium">Provider Pricing Overrides</p>
					<p className="text-muted-foreground text-xs">
						Enter a JSON array of override objects. Match precedence is exact &gt; wildcard &gt; regex. Unspecified fields fall back to
						datasheet pricing.
					</p>
				</div>

				<FormField
					control={form.control}
					name="pricing_overrides_json"
					render={({ field }) => (
						<FormItem>
							<FormControl>
								<Textarea
									data-testid="provider-pricing-overrides-json-input"
									value={field.value}
									onChange={(event) => {
										field.onChange(event.target.value);
										form.trigger("pricing_overrides_json");
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
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>

				<div className="flex justify-end gap-2">
					<Button
						type="button"
						variant="outline"
						data-testid="provider-pricing-overrides-reset-button"
						onClick={() => form.reset()}
						disabled={!hasUpdateProviderAccess || !form.formState.isDirty}
					>
						Reset
					</Button>
					<Button
						type="submit"
						data-testid="provider-pricing-overrides-save-button"
						isLoading={isUpdatingProvider}
						disabled={!hasUpdateProviderAccess || !form.formState.isDirty || !form.formState.isValid}
					>
						Save Pricing Overrides
					</Button>
				</div>
			</form>
		</Form>
	);
}
