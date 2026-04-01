"use client";

import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Switch } from "@/components/ui/switch";
import { getErrorMessage, setProviderFormDirtyState, useAppDispatch } from "@/lib/store";
import { useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import { ModelProvider } from "@/lib/types/config";
import { buildProviderUpdatePayload } from "@/app/workspace/providers/views/utils";
import { openaiConfigFormSchema, type OpenAIConfigFormSchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { useForm, type Resolver } from "react-hook-form";
import { toast } from "sonner";

interface OpenAIConfigFormFragmentProps {
	provider: ModelProvider;
}

export function OpenAIConfigFormFragment({ provider }: OpenAIConfigFormFragmentProps) {
	const dispatch = useAppDispatch();
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();
	const form = useForm<OpenAIConfigFormSchema, any, OpenAIConfigFormSchema>({
		resolver: zodResolver(openaiConfigFormSchema) as Resolver<OpenAIConfigFormSchema, any, OpenAIConfigFormSchema>,
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: {
			disable_store: provider.openai_config?.disable_store ?? false,
		},
	});

	useEffect(() => {
		dispatch(setProviderFormDirtyState(form.formState.isDirty));
	}, [form.formState.isDirty, dispatch]);

	useEffect(() => {
		form.reset({
			disable_store: provider.openai_config?.disable_store ?? false,
		});
	}, [form, provider.name, provider.openai_config?.disable_store]);

	const onSubmit = (data: OpenAIConfigFormSchema) => {
		updateProvider(buildProviderUpdatePayload(provider, {
			openai_config: {
				disable_store: data.disable_store,
			},
		}))
			.unwrap()
			.then(() => {
				toast.success("OpenAI configuration updated successfully");
				form.reset(data);
			})
			.catch((err) => {
				toast.error("Failed to update OpenAI configuration", {
					description: getErrorMessage(err),
				});
			});
	};

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6 px-6" data-testid="provider-config-openai-content">
				<div className="space-y-4">
					<FormField
						control={form.control}
						name="disable_store"
						render={({ field }) => (
							<FormItem>
								<div className="flex items-center justify-between space-x-2">
									<div className="space-y-0.5">
										<FormLabel>Disable Store</FormLabel>
										<p className="text-muted-foreground text-xs">
											With the Responses API, store defaults to true, and when it is on, the generated response is stored for later retrieval via API. OpenAI
											exposes endpoints to retrieve and delete stored responses, so your response IDs become durable server-side objects instead of one-shot
											IDs.
										</p>
									</div>
									<FormControl>
										<Switch
											data-testid="provider-openai-disable-store-switch"
											size="md"
											checked={field.value}
											disabled={!hasUpdateProviderAccess}
											onCheckedChange={(checked) => {
												field.onChange(checked);
												form.trigger("disable_store");
											}}
										/>
									</FormControl>
								</div>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>

				<div className="flex justify-end space-x-2 pb-6">
					<Button
						type="submit"
						disabled={!form.formState.isDirty || !form.formState.isValid || !hasUpdateProviderAccess || isUpdatingProvider}
						isLoading={isUpdatingProvider}
					>
						Save OpenAI Configuration
					</Button>
				</div>
			</form>
		</Form>
	);
}
