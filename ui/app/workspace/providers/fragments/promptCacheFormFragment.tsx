import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { getErrorMessage, setProviderFormDirtyState, useAppDispatch } from "@/lib/store";
import { useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import type { CacheControlInjectionPoint, ModelProvider } from "@/lib/types/config";
import { promptCacheFormSchema, type PromptCacheFormSchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { Plus, Trash2 } from "lucide-react";
import { useEffect } from "react";
import { useFieldArray, useForm, type Resolver } from "react-hook-form";
import { toast } from "sonner";
import { buildProviderUpdatePayload } from "../views/utils";

interface PromptCacheFormFragmentProps {
	provider: ModelProvider;
}

// Sentinel for "no TTL set", since a Select cannot hold undefined as a value.
const TTL_DEFAULT = "default";

const ROLES = ["system", "developer", "user", "assistant"] as const;

const toFormValues = (provider: ModelProvider): PromptCacheFormSchema => ({
	auto_inject: provider.prompt_cache?.auto_inject ?? false,
	ttl: provider.prompt_cache?.ttl ?? TTL_DEFAULT,
	cache_control_injection_points: provider.prompt_cache?.cache_control_injection_points ?? [],
});

export function PromptCacheFormFragment({ provider }: PromptCacheFormFragmentProps) {
	const dispatch = useAppDispatch();
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();

	const form = useForm<PromptCacheFormSchema, any, PromptCacheFormSchema>({
		resolver: zodResolver(promptCacheFormSchema) as Resolver<PromptCacheFormSchema, any, PromptCacheFormSchema>,
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: toFormValues(provider),
	});

	const { fields, append, remove } = useFieldArray({
		control: form.control,
		name: "cache_control_injection_points",
	});

	useEffect(() => {
		dispatch(setProviderFormDirtyState(form.formState.isDirty));
	}, [form.formState.isDirty, dispatch]);

	useEffect(() => {
		form.reset(toFormValues(provider));
	}, [form, provider.name, provider.prompt_cache]);

	const onSubmit = (data: PromptCacheFormSchema) => {
		const points = (data.cache_control_injection_points ?? []) as CacheControlInjectionPoint[];
		updateProvider(
			buildProviderUpdatePayload(provider, {
				prompt_cache: {
					auto_inject: data.auto_inject,
					// Send nothing rather than the sentinel, so the provider default applies.
					ttl: data.ttl === TTL_DEFAULT ? undefined : data.ttl,
					cache_control_injection_points: points.length > 0 ? points : undefined,
				},
			}),
		)
			.unwrap()
			.then(() => {
				toast.success("Prompt caching updated successfully");
				form.reset(data);
			})
			.catch((err) => {
				toast.error("Failed to update prompt caching", {
					description: getErrorMessage(err),
				});
			});
	};

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6 px-4 md:px-6" data-testid="provider-config-prompt-cache-content">
				<div className="space-y-4">
					<FormField
						control={form.control}
						name="auto_inject"
						render={({ field }) => (
							<FormItem>
								<div className="flex items-center justify-between space-x-2">
									<div className="space-y-0.5">
										<FormLabel>Auto-inject cache breakpoints</FormLabel>
										<p className="text-muted-foreground text-xs">
											Agentic clients such as Codex send no cache markers, so the cached prefix follows the newest message and every turn is
											billed as a cache write. Turning this on marks the first cacheable block instead, which keeps the cached region stable
											so turn 2 onward is a cache read. Requests that already carry their own cache markers are never modified, and models
											without explicit caching are left alone.
										</p>
									</div>
									<FormControl>
										<Switch
											data-testid="provider-prompt-cache-auto-inject-switch"
											size="md"
											checked={field.value}
											disabled={!hasUpdateProviderAccess}
											onCheckedChange={(checked) => {
												field.onChange(checked);
												form.trigger("auto_inject");
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
						name="ttl"
						render={({ field }) => (
							<FormItem>
								<FormLabel>Cache TTL</FormLabel>
								<Select value={field.value ?? TTL_DEFAULT} onValueChange={field.onChange} disabled={!hasUpdateProviderAccess}>
									<FormControl>
										<SelectTrigger data-testid="provider-prompt-cache-ttl-select" className="w-56">
											<SelectValue />
										</SelectTrigger>
									</FormControl>
									<SelectContent>
										<SelectItem value={TTL_DEFAULT}>Provider default (5 minutes)</SelectItem>
										<SelectItem value="1h">1 hour</SelectItem>
									</SelectContent>
								</Select>
								<p className="text-muted-foreground text-xs">
									A longer TTL costs more per cache write but survives gaps between turns. Providers that cannot carry a TTL ignore this.
								</p>
								<FormMessage />
							</FormItem>
						)}
					/>

					<div className="space-y-3">
						<div className="space-y-0.5">
							<FormLabel>Injection points</FormLabel>
							<p className="text-muted-foreground text-xs">
								Optional. Target specific messages instead of the first cacheable block. Adding any point{" "}
								<span className="font-medium">replaces</span> the default strategy rather than adding to it. Each point needs a role, an
								index, or both. At most four markers are injected, matching the provider ceiling.
							</p>
						</div>

						{fields.map((row, index) => (
							<div key={row.id} className="flex items-end gap-2" data-testid={`provider-prompt-cache-point-${index}`}>
								<FormField
									control={form.control}
									name={`cache_control_injection_points.${index}.role`}
									render={({ field }) => (
										<FormItem className="flex-1">
											<FormLabel className="text-xs">Role</FormLabel>
											<Select
												value={field.value ?? ""}
												onValueChange={(v) => field.onChange(v === "" ? undefined : v)}
												disabled={!hasUpdateProviderAccess}
											>
												<FormControl>
													<SelectTrigger>
														<SelectValue placeholder="Any role" />
													</SelectTrigger>
												</FormControl>
												<SelectContent>
													{ROLES.map((role) => (
														<SelectItem key={role} value={role}>
															{role}
														</SelectItem>
													))}
												</SelectContent>
											</Select>
										</FormItem>
									)}
								/>
								<FormField
									control={form.control}
									name={`cache_control_injection_points.${index}.index`}
									render={({ field }) => (
										<FormItem className="flex-1">
											<FormLabel className="text-xs">Index</FormLabel>
											<FormControl>
												<Input
													type="number"
													placeholder="e.g. -1 for last"
													value={field.value ?? ""}
													disabled={!hasUpdateProviderAccess}
													onChange={(e) => field.onChange(e.target.value === "" ? undefined : Number(e.target.value))}
												/>
											</FormControl>
										</FormItem>
									)}
								/>
								<Button
									type="button"
									variant="outline"
									size="icon"
									disabled={!hasUpdateProviderAccess}
									onClick={() => remove(index)}
									data-testid={`provider-prompt-cache-point-remove-${index}`}
								>
									<Trash2 className="h-4 w-4" />
								</Button>
							</div>
						))}

						{form.formState.errors.cache_control_injection_points && (
							<p className="text-destructive text-xs">Each point needs a role, an index, or both.</p>
						)}

						<Button
							type="button"
							variant="outline"
							size="sm"
							disabled={!hasUpdateProviderAccess}
							onClick={() => append({ location: "message", role: undefined, index: undefined })}
							data-testid="provider-prompt-cache-point-add"
						>
							<Plus className="mr-1 h-4 w-4" />
							Add injection point
						</Button>
					</div>
				</div>

				<div className="flex justify-end space-x-2 pb-6">
					<Button
						type="submit"
						disabled={!form.formState.isDirty || !form.formState.isValid || !hasUpdateProviderAccess || isUpdatingProvider}
						isLoading={isUpdatingProvider}
					>
						Save Prompt Caching
					</Button>
				</div>
			</form>
		</Form>
	);
}