"use client";

import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { getErrorMessage, setProviderFormDirtyState, useAppDispatch } from "@/lib/store";
import { useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import { ModelProvider } from "@/lib/types/config";
import { DefaultPerformanceConfig } from "@/lib/constants/config";
import { performanceFormSchema, type PerformanceFormSchema } from "@/lib/types/schemas";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { useForm, type Resolver } from "react-hook-form";
import { toast } from "sonner";

interface PerformanceFormFragmentProps {
	provider: ModelProvider;
}

export function PerformanceFormFragment({ provider }: PerformanceFormFragmentProps) {
	const dispatch = useAppDispatch();
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();
	const form = useForm<PerformanceFormSchema, any, PerformanceFormSchema>({
		resolver: zodResolver(performanceFormSchema) as Resolver<PerformanceFormSchema, any, PerformanceFormSchema>,
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: {
			concurrency_and_buffer_size: {
				concurrency: provider.concurrency_and_buffer_size?.concurrency ?? DefaultPerformanceConfig.concurrency,
				buffer_size: provider.concurrency_and_buffer_size?.buffer_size ?? DefaultPerformanceConfig.buffer_size,
			},
			send_back_raw_response: provider.send_back_raw_response ?? false,
		},
	});

	useEffect(() => {
		dispatch(setProviderFormDirtyState(form.formState.isDirty));
	}, [form.formState.isDirty]);

	useEffect(() => {
		// Reset form with new provider's concurrency_and_buffer_size when provider changes
		form.reset({
			concurrency_and_buffer_size: {
				concurrency: provider.concurrency_and_buffer_size?.concurrency ?? DefaultPerformanceConfig.concurrency,
				buffer_size: provider.concurrency_and_buffer_size?.buffer_size ?? DefaultPerformanceConfig.buffer_size,
			},
			send_back_raw_response: provider.send_back_raw_response ?? false,
		});
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [form, provider.name]);

	const onSubmit = (data: PerformanceFormSchema) => {
		// Create updated provider configuration
		const updatedProvider: ModelProvider = {
			...provider,
			concurrency_and_buffer_size: {
				concurrency: data.concurrency_and_buffer_size.concurrency,
				buffer_size: data.concurrency_and_buffer_size.buffer_size,
			},
			send_back_raw_response: data.send_back_raw_response,
		};
		updateProvider(updatedProvider)
			.unwrap()
			.then(() => {
				toast.success("Provider configuration updated successfully");
			})
			.catch((err) => {
				toast.error("Failed to update provider configuration", {
					description: getErrorMessage(err),
				});
			});
	};

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6 px-6">
				{/* Performance Configuration */}
				<div className="space-y-4">
					<div className="flex flex-row gap-4">
						<div className="flex-1">
							<FormField
								control={form.control}
								name="concurrency_and_buffer_size.concurrency"
								render={({ field }) => (
									<FormItem>
										<FormLabel>Concurrency</FormLabel>
										<FormControl>
											<Input
												type="number"
												placeholder="10"
												{...field}
												onChange={(e) => field.onChange(Number.parseInt(e.target.value) || 0)}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</div>
						<div className="flex-1">
							<FormField
								control={form.control}
								name="concurrency_and_buffer_size.buffer_size"
								render={({ field }) => (
									<FormItem>
										<FormLabel>Buffer Size</FormLabel>
										<FormControl>
											<Input
												type="number"
												placeholder="10"
												{...field}
												onChange={(e) => field.onChange(Number.parseInt(e.target.value) || 0)}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</div>
					</div>

					<div className="mt-6 space-y-4">
						<FormField
							control={form.control}
							name="send_back_raw_response"
							render={({ field }) => (
								<FormItem>
									<div className="flex items-center justify-between space-x-2">
										<div className="space-y-0.5">
											<FormLabel>Include Raw Response</FormLabel>
											<p className="text-muted-foreground text-xs">
												Include the raw provider response alongside the parsed response for debugging and advanced use cases
											</p>
										</div>
										<FormControl>
											<Switch size="md" checked={field.value} onCheckedChange={field.onChange} />
										</FormControl>
									</div>
									<FormMessage />
								</FormItem>
							)}
						/>
					</div>
				</div>

				{/* Form Actions */}
				<div className="flex justify-end space-x-2 pb-6">
					<Button
						type="submit"
						disabled={!form.formState.isDirty || !form.formState.isValid || isUpdatingProvider}
						isLoading={isUpdatingProvider}
					>
						Save Performance Configuration
					</Button>
				</div>
			</form>
		</Form>
	);
}
