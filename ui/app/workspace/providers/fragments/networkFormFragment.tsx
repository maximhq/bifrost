"use client";

import { Button } from "@/components/ui/button";
import { Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { HeadersTable } from "@/components/ui/headersTable";
import { Input } from "@/components/ui/input";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { DefaultNetworkConfig } from "@/lib/constants/config";
import { getErrorMessage, setProviderFormDirtyState, useAppDispatch } from "@/lib/store";
import { useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import { ModelProvider, isKnownProvider } from "@/lib/types/config";
import { networkOnlyFormSchema, type NetworkOnlyFormSchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { useForm, type Resolver } from "react-hook-form";
import { toast } from "sonner";

interface NetworkFormFragmentProps {
	provider: ModelProvider;
}

// seconds to human readable time
const secondsToHumanReadable = (seconds: number) => {
	// Handle edge cases
	if (!seconds || seconds < 0 || isNaN(seconds)) {
		return "0 seconds";
	}
	seconds = Math.floor(seconds);
	if (seconds < 60) {
		return `${seconds} ${seconds === 1 ? "second" : "seconds"}`;
	}
	if (seconds < 3600) {
		const minutes = Math.floor(seconds / 60);
		return `${minutes} ${minutes === 1 ? "minute" : "minutes"}`;
	}
	if (seconds < 86400) {
		const hours = Math.floor(seconds / 3600);
		return `${hours} ${hours === 1 ? "hour" : "hours"}`;
	}
	// For >= 1 day, only show non-zero components
	const days = Math.floor(seconds / 86400);
	const hours = Math.floor((seconds % 86400) / 3600);
	const minutes = Math.floor((seconds % 3600) / 60);
	const remainingSeconds = seconds % 60;
	const parts: string[] = [];
	parts.push(`${days} ${days === 1 ? "day" : "days"}`);
	if (hours > 0) parts.push(`${hours} ${hours === 1 ? "hour" : "hours"}`);
	if (minutes > 0) parts.push(`${minutes} ${minutes === 1 ? "minute" : "minutes"}`);
	if (remainingSeconds > 0) parts.push(`${remainingSeconds} ${remainingSeconds === 1 ? "second" : "seconds"}`);
	return parts.join(" ");
};

export function NetworkFormFragment({ provider }: NetworkFormFragmentProps) {
	const dispatch = useAppDispatch();
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();
	const isCustomProvider = !isKnownProvider(provider.name as string);

	const form = useForm<NetworkOnlyFormSchema, any, NetworkOnlyFormSchema>({
		resolver: zodResolver(networkOnlyFormSchema) as Resolver<NetworkOnlyFormSchema, any, NetworkOnlyFormSchema>,
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: {
			network_config: {
				base_url: provider.network_config?.base_url || undefined,
				extra_headers: provider.network_config?.extra_headers,
				default_request_timeout_in_seconds:
					provider.network_config?.default_request_timeout_in_seconds ?? DefaultNetworkConfig.default_request_timeout_in_seconds,
				max_retries: provider.network_config?.max_retries ?? DefaultNetworkConfig.max_retries,
				retry_backoff_initial: provider.network_config?.retry_backoff_initial ?? DefaultNetworkConfig.retry_backoff_initial,
				retry_backoff_max: provider.network_config?.retry_backoff_max ?? DefaultNetworkConfig.retry_backoff_max,
			},
		},
	});

	useEffect(() => {
		dispatch(setProviderFormDirtyState(form.formState.isDirty));
	}, [form.formState.isDirty, dispatch]);

	const onSubmit = (data: NetworkOnlyFormSchema) => {
		const requiresBaseUrl = isCustomProvider || provider.name === "ollama" || provider.name === "sgl";
		if (requiresBaseUrl && (data.network_config?.base_url ?? "").trim() === "") {
			if ((provider.network_config?.base_url ?? "").trim() !== "") {
				toast.error("You can't remove network configuration for this provider.");
			} else {
				toast.error("Base URL is required for this provider.");
			}
			return;
		}
		// Create updated provider configuration
		const updatedProvider: ModelProvider = {
			...provider,
			network_config: {
				...provider.network_config,
				base_url: data.network_config?.base_url || undefined,
				extra_headers: data.network_config?.extra_headers || undefined,
				default_request_timeout_in_seconds: data.network_config?.default_request_timeout_in_seconds ?? 30,
				max_retries: data.network_config?.max_retries ?? 0,
				retry_backoff_initial: data.network_config?.retry_backoff_initial ?? 500,
				retry_backoff_max: data.network_config?.retry_backoff_max ?? 10000,
			},
		};
		updateProvider(updatedProvider)
			.unwrap()
			.then(() => {
				toast.success("Provider configuration updated successfully");
				form.reset(data);
			})
			.catch((err) => {
				toast.error("Failed to update provider configuration", {
					description: getErrorMessage(err),
				});
			});
	};

	useEffect(() => {
		// Reset form with new provider's network_config when provider.name changes
		form.reset({
			network_config: {
				base_url: provider.network_config?.base_url || undefined,
				extra_headers: provider.network_config?.extra_headers,
				default_request_timeout_in_seconds:
					provider.network_config?.default_request_timeout_in_seconds ?? DefaultNetworkConfig.default_request_timeout_in_seconds,
				max_retries: provider.network_config?.max_retries ?? DefaultNetworkConfig.max_retries,
				retry_backoff_initial: provider.network_config?.retry_backoff_initial ?? DefaultNetworkConfig.retry_backoff_initial,
				retry_backoff_max: provider.network_config?.retry_backoff_max ?? DefaultNetworkConfig.retry_backoff_max,
			},
		});
	}, [form, provider.name, provider.network_config]);

	const baseURLRequired = provider.name === "ollama" || provider.name === "sgl" || provider.name === "vllm" || isCustomProvider;

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6 px-6">
				{/* Network Configuration */}
				<div className="space-y-4">
					<div className="grid grid-cols-1 gap-4">
						<FormField
							control={form.control}
							name="network_config.base_url"
							render={({ field }) => (
								<FormItem>
									<FormLabel>Base URL {baseURLRequired ? "(Required)" : "(Optional)"}</FormLabel>
									<FormControl>
										<Input
											placeholder={isCustomProvider ? "https://api.your-provider.com" : "https://api.example.com"}
											{...field}
											value={field.value || ""}
											disabled={!hasUpdateProviderAccess}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
						<div className="flex w-full flex-row items-start gap-4">
							<FormField
								control={form.control}
								name="network_config.default_request_timeout_in_seconds"
								render={({ field }) => (
									<FormItem className="flex-1">
										<FormLabel>Timeout (seconds)</FormLabel>
										<FormControl>
											<Input
												placeholder="30"
												{...field}
												value={field.value === undefined || Number.isNaN(field.value) ? '' : field.value}
												disabled={!hasUpdateProviderAccess}
												onChange={(e) => {
													const value = e.target.value
													if (value === '') {
														field.onChange(undefined)
														return
													}
													const parsed = Number(value)
													if (!Number.isNaN(parsed)) {
														field.onChange(parsed)
													}
													form.trigger("network_config");
												}}
											/>
										</FormControl>
										<FormDescription>{secondsToHumanReadable(field.value)}</FormDescription>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={form.control}
								name="network_config.max_retries"
								render={({ field }) => (
									<FormItem className="flex-1">
										<FormLabel>Max Retries</FormLabel>
										<FormControl>
											<Input
												placeholder="0"
												{...field}
												value={field.value === undefined || Number.isNaN(field.value) ? '' : field.value}
												disabled={!hasUpdateProviderAccess}
												onChange={(e) => {
													const value = e.target.value
													if (value === '') {
														field.onChange(undefined)
														return
													}
													const parsed = Number(value)
													if (!Number.isNaN(parsed)) {
														field.onChange(parsed)
													}
													form.trigger("network_config");
												}}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</div>
						<div className="flex w-full flex-row items-start gap-4">
							<FormField
								control={form.control}
								name="network_config.retry_backoff_initial"
								render={({ field }) => (
									<FormItem className="flex-1">
										<FormLabel>Initial Backoff (ms)</FormLabel>
										<FormControl>
											<Input
												placeholder="e.g 500"
												{...field}
												value={field.value === undefined || Number.isNaN(field.value) ? '' : field.value}
												disabled={!hasUpdateProviderAccess}
												onChange={(e) => {
													const value = e.target.value
													if (value === '') {
														field.onChange(undefined)
														return
													}
													const parsed = Number(value)
													if (!Number.isNaN(parsed)) {
														field.onChange(parsed)
													}
													form.trigger("network_config");
												}}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={form.control}
								name="network_config.retry_backoff_max"
								render={({ field }) => (
									<FormItem className="flex-1">
										<FormLabel>Max Backoff (ms)</FormLabel>
										<FormControl>
											<Input
												placeholder="e.g 10000"
												{...field}
												value={field.value === undefined || Number.isNaN(field.value) ? '' : field.value}
												disabled={!hasUpdateProviderAccess}
												onChange={(e) => {
													const value = e.target.value
													if (value === '') {
														field.onChange(undefined)
														return
													}
													const parsed = Number(value)
													if (!Number.isNaN(parsed)) {
														field.onChange(parsed)
													}
													form.trigger("network_config");
												}}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</div>
						<FormField
							control={form.control}
							name="network_config.extra_headers"
							render={({ field }) => (
								<FormItem>
									<FormControl>
										<HeadersTable
											value={field.value || {}}
											onChange={field.onChange}
											keyPlaceholder="Header name"
											valuePlaceholder="Header value"
											label="Extra Headers"
											disabled={!hasUpdateProviderAccess}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					</div>
				</div>

				{/* Form Actions */}
				<div className="flex justify-end space-x-2 py-2">
					<Button
						type="button"
						variant="outline"
						onClick={() => {
							form.reset({
								network_config: undefined,
							});
							onSubmit(form.getValues());
						}}
						disabled={
							!hasUpdateProviderAccess ||
							isUpdatingProvider ||
							!provider.network_config ||
							!provider.network_config.base_url ||
							provider.network_config.base_url.trim() === ""
						}
					>
						Remove configuration
					</Button>
					<TooltipProvider>
						<Tooltip>
							<TooltipTrigger asChild>
								<Button
									type="submit"
									disabled={!form.formState.isDirty || !form.formState.isValid || !hasUpdateProviderAccess}
									isLoading={isUpdatingProvider}
								>
									Save Network Configuration
								</Button>
							</TooltipTrigger>
							{(!form.formState.isDirty || !form.formState.isValid) && (
								<TooltipContent>
									<p>
										{!form.formState.isDirty && !form.formState.isValid
											? "No changes made and validation errors present"
											: !form.formState.isDirty
												? "No changes made"
												: "Please fix validation errors"}
									</p>
								</TooltipContent>
							)}
						</Tooltip>
					</TooltipProvider>
				</div>
			</form>
		</Form>
	);
}
