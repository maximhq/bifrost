import { Button } from "@/components/ui/button";
import { EnvVarInput } from "@/components/ui/envVarInput";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { maximFormSchema, normalizeEnvVar, type MaximFormSchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm, type Resolver } from "react-hook-form";

interface MaximFormFragmentProps {
	initialConfig?: {
		enabled?: boolean;
		api_key?: string | { value?: string; env_var?: string; from_env?: boolean };
		log_repo_id?: string | { value?: string; env_var?: string; from_env?: boolean };
	};
	onSave: (config: MaximFormSchema) => Promise<void>;
	onDelete?: () => void;
	isDeleting?: boolean;
	isLoading?: boolean;
}

export function MaximFormFragment({ initialConfig, onSave, onDelete, isDeleting = false, isLoading = false }: MaximFormFragmentProps) {
	const hasMaximAccess = useRbac(RbacResource.Observability, RbacOperation.Update);
	const [isSaving, setIsSaving] = useState(false);

	const form = useForm<MaximFormSchema, any, MaximFormSchema>({
		resolver: zodResolver(maximFormSchema) as Resolver<MaximFormSchema, any, MaximFormSchema>,
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: {
			enabled: initialConfig?.enabled ?? true,
			maxim_config: {
				api_key: normalizeEnvVar(initialConfig?.api_key),
				log_repo_id: normalizeEnvVar(initialConfig?.log_repo_id),
			},
		},
	});

	const onSubmit = (data: MaximFormSchema) => {
		setIsSaving(true);
		onSave(data).finally(() => setIsSaving(false));
	};

	useEffect(() => {
		// Reset form with new initial config when it changes
		form.reset({
			enabled: initialConfig?.enabled ?? true,
			maxim_config: {
				api_key: normalizeEnvVar(initialConfig?.api_key),
				log_repo_id: normalizeEnvVar(initialConfig?.log_repo_id),
			},
		});
	}, [form, initialConfig]);

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6">
				<div className="space-y-4">
					<div className="grid grid-cols-1 gap-4">
						<FormField
							control={form.control}
							name="maxim_config.api_key"
							render={({ field }) => (
								<FormItem>
									<FormLabel>API Key</FormLabel>
									<FormControl>
										<EnvVarInput
											placeholder="Enter your Maxim API key or env.MAXIM_API_KEY"
											disabled={!hasMaximAccess}
											maskNonEnvValue
											data-testid="maxim-api-key-input"
											value={field.value}
											onChange={field.onChange}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>

						<FormField
							control={form.control}
							name="maxim_config.log_repo_id"
							render={({ field }) => (
								<FormItem>
									<FormLabel>Log Repository ID (Optional)</FormLabel>
									<FormControl>
										<EnvVarInput
											placeholder="Enter log repository ID or env.MAXIM_LOG_REPO_ID"
											disabled={!hasMaximAccess}
											data-testid="maxim-log-repo-id-input"
											value={field.value}
											onChange={field.onChange}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					</div>
				</div>

				{/* Form Actions */}
				<div className="flex w-full flex-row items-center">
					<FormField
						control={form.control}
						name="enabled"
						render={({ field }) => (
							<FormItem className="flex items-center gap-2 py-2">
								<FormLabel className="text-muted-foreground text-sm font-medium">Enabled</FormLabel>
								<FormControl>
									<Switch
										checked={field.value}
										onCheckedChange={field.onChange}
										disabled={!hasMaximAccess}
										data-testid="maxim-connector-enable-toggle"
									/>
								</FormControl>
							</FormItem>
						)}
					/>
					<div className="ml-auto flex justify-end space-x-2 py-2">
						{onDelete && (
							<Button
								type="button"
								variant="outline"
								onClick={onDelete}
								disabled={isDeleting}
								title="Delete connector"
								aria-label="Delete connector"
							>
								<Trash2 className="size-4" />
							</Button>
						)}
						<Button
							type="button"
							variant="outline"
							onClick={() => {
								form.reset({
									enabled: initialConfig?.enabled ?? true,
									maxim_config: {
										api_key: normalizeEnvVar(initialConfig?.api_key),
										log_repo_id: normalizeEnvVar(initialConfig?.log_repo_id),
									},
								});
							}}
							disabled={!hasMaximAccess || isLoading || !form.formState.isDirty}
						>
							Reset
						</Button>
						<TooltipProvider>
							<Tooltip>
								<TooltipTrigger asChild>
									<Button type="submit" disabled={!hasMaximAccess || !form.formState.isDirty} isLoading={isSaving}>
										Save Maxim Configuration
									</Button>
								</TooltipTrigger>
								{!form.formState.isDirty && (
									<TooltipContent>
										<p>
											{!form.formState.isDirty
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
				</div>
			</form>
		</Form>
	);
}