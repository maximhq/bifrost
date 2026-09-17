import { Button } from "@/components/ui/button";
import { Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { getErrorMessage, setProviderFormDirtyState, useAppDispatch } from "@/lib/store";
import { useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import type { ModelProvider } from "@/lib/types/config";
import { miniMaxConfigFormSchema, type MiniMaxConfigFormSchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { useForm, type Resolver } from "react-hook-form";
import { toast } from "sonner";
import { buildProviderUpdatePayload } from "../views/utils";

export function MiniMaxConfigFormFragment({ provider }: { provider: ModelProvider }) {
	const dispatch = useAppDispatch();
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const [updateProvider, { isLoading }] = useUpdateProviderMutation();
	const form = useForm<MiniMaxConfigFormSchema, unknown, MiniMaxConfigFormSchema>({
		resolver: zodResolver(miniMaxConfigFormSchema) as Resolver<MiniMaxConfigFormSchema, unknown, MiniMaxConfigFormSchema>,
		mode: "onChange",
		defaultValues: { auth_type: provider.minimax_config?.auth_type ?? "bearer" },
	});

	useEffect(() => {
		dispatch(setProviderFormDirtyState(form.formState.isDirty));
	}, [form.formState.isDirty, dispatch]);

	useEffect(() => {
		form.reset({ auth_type: provider.minimax_config?.auth_type ?? "bearer" });
	}, [form, provider.name, provider.minimax_config?.auth_type]);

	const onSubmit = (data: MiniMaxConfigFormSchema) => {
		updateProvider(buildProviderUpdatePayload(provider, { minimax_config: data }))
			.unwrap()
			.then(() => {
				toast.success("MiniMax configuration updated successfully");
				form.reset(data);
			})
			.catch((err) => toast.error("Failed to update MiniMax configuration", { description: getErrorMessage(err) }));
	};

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6 px-4 md:px-6" data-testid="provider-config-minimax-content">
				<FormField
					control={form.control}
					name="auth_type"
					render={({ field }) => (
						<FormItem>
							<FormLabel>Authentication Type</FormLabel>
							<Select value={field.value} onValueChange={field.onChange} disabled={!hasUpdateProviderAccess}>
								<FormControl>
									<SelectTrigger data-testid="provider-minimax-auth-type">
										<SelectValue />
									</SelectTrigger>
								</FormControl>
								<SelectContent>
									<SelectItem value="bearer">MiniMax Official (Bearer)</SelectItem>
									<SelectItem value="x-key">X-Key Compatible (ExchangeToken)</SelectItem>
								</SelectContent>
							</Select>
							<FormDescription>Controls how the configured provider key is sent upstream.</FormDescription>
							<FormMessage />
						</FormItem>
					)}
				/>
				<div className="flex justify-end pb-6">
					<Button
						type="submit"
						data-testid="provider-minimax-save"
						disabled={!form.formState.isDirty || !hasUpdateProviderAccess}
						isLoading={isLoading}
					>
						Save MiniMax Configuration
					</Button>
				</div>
			</form>
		</Form>
	);
}