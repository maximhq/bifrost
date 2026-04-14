"use client";

import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { getErrorMessage, setProviderFormDirtyState, useAppDispatch } from "@/lib/store";
import { useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import { ModelProvider } from "@/lib/types/config";
import { codexConfigFormSchema, type CodexConfigFormSchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { useForm, type Resolver } from "react-hook-form";
import { toast } from "sonner";

interface CodexConfigFormFragmentProps {
	provider: ModelProvider;
}

export function CodexConfigFormFragment({ provider }: CodexConfigFormFragmentProps) {
	const dispatch = useAppDispatch();
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();
	const form = useForm<CodexConfigFormSchema, any, CodexConfigFormSchema>({
		resolver: zodResolver(codexConfigFormSchema) as Resolver<CodexConfigFormSchema, any, CodexConfigFormSchema>,
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: {
			pricing_mode: provider.codex_config?.pricing_mode ?? "included_zero",
		},
	});

	useEffect(() => {
		dispatch(setProviderFormDirtyState(form.formState.isDirty));
	}, [dispatch, form.formState.isDirty]);

	useEffect(() => {
		form.reset({
			pricing_mode: provider.codex_config?.pricing_mode ?? "included_zero",
		});
	}, [form, provider.codex_config?.pricing_mode, provider.name]);

	const onSubmit = (data: CodexConfigFormSchema) => {
		updateProvider({
			...provider,
			codex_config: data,
		})
			.unwrap()
			.then(() => {
				toast.success("Codex configuration updated successfully");
				form.reset(data);
			})
			.catch((err) => {
				toast.error("Failed to update Codex configuration", {
					description: getErrorMessage(err),
				});
			});
	};

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6 px-6 pb-6" data-testid="provider-config-codex-content">
				<FormField
					control={form.control}
					name="pricing_mode"
					render={({ field }) => (
						<FormItem className="space-y-3">
							<div className="space-y-1">
								<FormLabel>Pricing Mode</FormLabel>
								<p className="text-muted-foreground text-xs">
									Use zero cost when Codex usage is included in the subscription, or estimate spend using equivalent OpenAI API pricing for
									governance and analytics.
								</p>
							</div>
							<FormControl>
								<div className="space-y-3">
									<Select value={field.value} onValueChange={field.onChange} disabled={!hasUpdateProviderAccess}>
										<SelectTrigger className="w-full" data-testid="provider-codex-pricing-mode-select">
											<SelectValue placeholder="Select pricing mode" />
										</SelectTrigger>
										<SelectContent>
											<SelectItem value="included_zero">Included in subscription</SelectItem>
											<SelectItem value="openai_equivalent">Equivalent OpenAI pricing</SelectItem>
										</SelectContent>
									</Select>
									<div className="text-muted-foreground rounded-sm border p-3 text-xs">
										{field.value === "openai_equivalent"
											? "Estimate Codex request cost using OpenAI-equivalent model pricing for governance and analytics."
											: "Treat Codex usage as zero-cost for budgets, logs, and analytics when it is included in the subscription."}
									</div>
								</div>
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>

				<div className="flex justify-end">
					<Button
						type="submit"
						disabled={!form.formState.isDirty || !form.formState.isValid || !hasUpdateProviderAccess || isUpdatingProvider}
						isLoading={isUpdatingProvider}
					>
						Save Codex Configuration
					</Button>
				</div>
			</form>
		</Form>
	);
}
