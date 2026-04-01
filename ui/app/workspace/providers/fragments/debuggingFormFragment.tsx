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
import { buildProviderUpdatePayload } from "../views/utils";

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
			store_raw_request_response: provider.store_raw_request_response ?? false,
		},
	});
	const storeRawEnabled = form.watch("store_raw_request_response");

	useEffect(() => {
		dispatch(setProviderFormDirtyState(form.formState.isDirty));
	}, [form.formState.isDirty, dispatch]);

	useEffect(() => {
		form.reset({
			send_back_raw_request: provider.send_back_raw_request ?? false,
			send_back_raw_response: provider.send_back_raw_response ?? false,
			store_raw_request_response: provider.store_raw_request_response ?? false,
		});
	// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [provider.name, provider.send_back_raw_request, provider.send_back_raw_response, provider.store_raw_request_response]);

	const onSubmit = (data: DebuggingFormSchema) => {
		const updatedProvider = buildProviderUpdatePayload(provider, {
			send_back_raw_request: data.send_back_raw_request,
			send_back_raw_response: data.send_back_raw_response,
			store_raw_request_response: data.store_raw_request_response,
		});
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
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6 px-6" data-testid="provider-config-debugging-content">
				<div className="space-y-0">

					{/* Parent: Store Raw Request/Response */}
					<FormField
						control={form.control}
						name="store_raw_request_response"
						render={({ field }) => (
							<FormItem>
								<div className="flex items-center justify-between space-x-2">
									<div className="space-y-0.5">
										<FormLabel>Enable Raw Request/Response</FormLabel>
										<p className="text-muted-foreground text-xs">
											Capture the raw provider request and response for internal logging. Raw payloads are not returned to clients unless send_back_raw_request and send_back_raw_response are also enabled.
										</p>
									</div>
									<FormControl>
										<Switch
											data-testid="provider-debugging-store-raw-request-response-switch"
											size="md"
											checked={field.value}
											disabled={!hasUpdateProviderAccess}
											onCheckedChange={(checked) => {
												field.onChange(checked);
												if (!checked) {
													form.setValue("send_back_raw_request", false, { shouldDirty: true });
													form.setValue("send_back_raw_response", false, { shouldDirty: true });
												}
												form.trigger("store_raw_request_response");
											}}
										/>
									</FormControl>
								</div>
								<FormMessage />
							</FormItem>
						)}
					/>

					{/* Children with tree connectors */}
					<div className="relative mt-3 space-y-3 pl-6">

						{/* Child 1: Send Back Raw Request — trunk continues past this to next sibling */}
						<div className="relative">
							{/* Vertical trunk — extends past bottom by the gap (space-y-3 = 0.75rem) to connect with child 2 */}
							<div className="absolute -left-6 top-0 -bottom-3 w-px bg-border" />
							{/* Horizontal branch at label center (~9px from top) */}
							<div className="absolute -left-6 top-[0.5625rem] w-5 h-px bg-border" />
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
													disabled={!hasUpdateProviderAccess || !storeRawEnabled}
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
						</div>

						{/* Child 2: Send Back Raw Response — last child, trunk stops at label */}
						<div className="relative">
							{/* L-connector: vertical from top down to label center, then turns right */}
							<div className="absolute -left-6 top-0 w-5 h-[0.5625rem] border-l border-b border-border rounded-bl" />
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
													disabled={!hasUpdateProviderAccess || !storeRawEnabled}
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

					</div>
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
