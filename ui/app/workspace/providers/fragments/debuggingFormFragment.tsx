"use client";

import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Switch } from "@/components/ui/switch";
import { getErrorMessage, setProviderFormDirtyState, useAppDispatch } from "@/lib/store";
import { useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import { ModelProvider } from "@/lib/types/config";
import { debuggingFormSchema, type DebuggingFormSchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { useForm, type Resolver } from "react-hook-form";
import { toast } from "sonner";

interface DebuggingFormFragmentProps {
	provider: ModelProvider;
}

export function DebuggingFormFragment({ provider }: DebuggingFormFragmentProps) {
	const dispatch = useAppDispatch();
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();
	const form = useForm<DebuggingFormSchema, any, DebuggingFormSchema>({
		resolver: zodResolver(debuggingFormSchema) as Resolver<DebuggingFormSchema, any, DebuggingFormSchema>,
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: {
			send_back_raw_request: provider.send_back_raw_request ?? false,
			send_back_raw_response: provider.send_back_raw_response ?? false,
		},
	});

	useEffect(() => {
		dispatch(setProviderFormDirtyState(form.formState.isDirty));
	}, [form.formState.isDirty, dispatch]);

	useEffect(() => {
		form.reset({
			send_back_raw_request: provider.send_back_raw_request ?? false,
			send_back_raw_response: provider.send_back_raw_response ?? false,
		});
	}, [form, provider.name, provider.send_back_raw_request, provider.send_back_raw_response]);

	const onSubmit = (data: DebuggingFormSchema) => {
		const updatedProvider: ModelProvider = {
			...provider,
			send_back_raw_request: data.send_back_raw_request,
			send_back_raw_response: data.send_back_raw_response,
		};
		updateProvider(updatedProvider)
			.unwrap()
			.then(() => {
				toast.success("Debugging configuration updated successfully");
				form.reset(data);
			})
			.catch((err) => {
				toast.error("Failed to update debugging configuration", {
					description: getErrorMessage(err),
				});
			});
	};

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6 px-6">
				<div className="space-y-4">
					<FormField
						control={form.control}
						name="send_back_raw_request"
						render={({ field }) => (
							<FormItem>
								<div className="flex items-center justify-between space-x-2">
									<div className="space-y-0.5">
										<FormLabel>Send Back Raw Request</FormLabel>
										<p className="text-muted-foreground text-xs">
											Include the raw provider request alongside the parsed request for debugging and advanced use cases
										</p>
									</div>
									<FormControl>
										<Switch
											size="md"
											checked={field.value}
											disabled={!hasUpdateProviderAccess}
											onCheckedChange={(checked) => {
												field.onChange(checked);
												form.trigger("send_back_raw_request");
											}}
										/>
									</FormControl>
								</div>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={form.control}
						name="send_back_raw_response"
						render={({ field }) => (
							<FormItem>
								<div className="flex items-center justify-between space-x-2">
									<div className="space-y-0.5">
										<FormLabel>Send Back Raw Response</FormLabel>
										<p className="text-muted-foreground text-xs">
											Include the raw provider response alongside the parsed response for debugging and advanced use cases
										</p>
									</div>
									<FormControl>
										<Switch
											size="md"
											checked={field.value}
											disabled={!hasUpdateProviderAccess}
											onCheckedChange={(checked) => {
												field.onChange(checked);
												form.trigger("send_back_raw_response");
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
						Save Debugging Configuration
					</Button>
				</div>
			</form>
		</Form>
	);
}
