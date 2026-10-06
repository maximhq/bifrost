import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Switch } from "@/components/ui/switch";
import { getErrorMessage, setProviderFormDirtyState, useAppDispatch } from "@/lib/store";
import { useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import { ModelProvider } from "@/lib/types/config";
import { pricingFormSchema, type PricingFormSchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { useForm, type Resolver } from "react-hook-form";
import { toast } from "sonner";
import { buildProviderUpdatePayload } from "../views/utils";

interface PricingFormFragmentProps {
	provider: ModelProvider;
}

export function PricingFormFragment({ provider }: PricingFormFragmentProps) {
	const dispatch = useAppDispatch();
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();
	const form = useForm<PricingFormSchema, any, PricingFormSchema>({
		resolver: zodResolver(pricingFormSchema) as Resolver<PricingFormSchema, any, PricingFormSchema>,
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: {
			ignore_provider_cost: provider.ignore_provider_cost ?? false,
		},
	});

	useEffect(() => {
		dispatch(setProviderFormDirtyState(form.formState.isDirty));
	}, [form.formState.isDirty, dispatch]);

	useEffect(() => {
		form.reset({
			ignore_provider_cost: provider.ignore_provider_cost ?? false,
		});
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [provider.name, provider.ignore_provider_cost]);

	const onSubmit = (data: PricingFormSchema) => {
		const updatedProvider = buildProviderUpdatePayload(provider, {
			ignore_provider_cost: data.ignore_provider_cost,
		});
		updateProvider(updatedProvider)
			.unwrap()
			.then(() => {
				toast.success("Pricing configuration updated successfully");
				form.reset(data);
			})
			.catch((err) => {
				toast.error("Failed to update pricing configuration", {
					description: getErrorMessage(err),
				});
			});
	};

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6 p-6" data-testid="provider-config-pricing-content">
				<div className="space-y-4">
					<FormField
						control={form.control}
						name="ignore_provider_cost"
						render={({ field }) => (
							<FormItem>
								<div className="flex items-center justify-between space-x-2">
									<div className="space-y-0.5">
										<FormLabel>Ignore Provider-Reported Cost</FormLabel>
										<p className="text-muted-foreground text-xs">
											Discard the cost the provider returns in its usage object and calculate it in Bifrost from model pricing and pricing
											overrides. Use this when a provider reports cost in its own units, such as credits or a non-USD currency. Models
											without pricing or an override are logged at $0.
										</p>
									</div>
									<FormControl>
										<Switch
											data-testid="provider-pricing-ignore-provider-cost-switch"
											size="md"
											checked={field.value}
											disabled={!hasUpdateProviderAccess}
											onCheckedChange={(checked) => {
												field.onChange(checked);
												form.trigger("ignore_provider_cost");
											}}
										/>
									</FormControl>
								</div>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>

				<div className="flex justify-end space-x-2">
					<Button
						type="submit"
						disabled={!form.formState.isDirty || !hasUpdateProviderAccess || isUpdatingProvider}
						isLoading={isUpdatingProvider}
					>
						Save Pricing Configuration
					</Button>
				</div>
			</form>
		</Form>
	);
}