import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { HeadersTable } from "@/components/ui/headersTable";
import { TagInput } from "@/components/ui/tagInput";
import { getErrorMessage, setProviderFormDirtyState, useAppDispatch } from "@/lib/store";
import { useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import { ModelProvider } from "@/lib/types/config";
import { providerLabelsFormSchema, type ProviderLabelsFormSchema } from "@/lib/types/schemas";
import { normalizeTags } from "@/lib/utils/metadataTags";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { buildProviderUpdatePayload } from "../views/utils";

interface LabelsFormFragmentProps {
	provider: ModelProvider;
}

const labelsOf = (provider: ModelProvider): ProviderLabelsFormSchema => ({
	metadata: provider.metadata ?? {},
	tags: provider.tags ?? [],
});

export function LabelsFormFragment({ provider }: LabelsFormFragmentProps) {
	const dispatch = useAppDispatch();
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();
	const form = useForm<ProviderLabelsFormSchema>({
		resolver: zodResolver(providerLabelsFormSchema),
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: labelsOf(provider),
	});

	useEffect(() => {
		dispatch(setProviderFormDirtyState(form.formState.isDirty));
	}, [form.formState.isDirty, dispatch]);

	useEffect(() => {
		form.reset(labelsOf(provider));
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [provider.name, provider.metadata, provider.tags]);

	const onSubmit = (data: ProviderLabelsFormSchema) => {
		// Sent as a whole: {} and [] clear the stored labels.
		const labels = { metadata: data.metadata, tags: normalizeTags(data.tags).tags };
		updateProvider(buildProviderUpdatePayload(provider, labels))
			.unwrap()
			.then(() => {
				toast.success("Metadata and tags updated successfully");
				form.reset(labels);
			})
			.catch((err) => {
				toast.error("Failed to update metadata and tags", {
					description: getErrorMessage(err),
				});
			});
	};

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6 p-6" data-testid="provider-config-labels-content">
				<FormField
					control={form.control}
					name="tags"
					render={({ field }) => (
						<FormItem>
							<FormLabel>Tags</FormLabel>
							<FormControl>
								<TagInput
									data-testid="provider-labels-tags-input"
									placeholder="e.g., prod, eu, approved-for-pii"
									value={field.value ?? []}
									onValueChange={field.onChange}
									readOnly={!hasUpdateProviderAccess}
								/>
							</FormControl>
							<p className="text-muted-foreground text-xs">
								Labels for routing, ownership and compliance. Press Enter or comma to add one. Tags use letters, digits, &quot;.&quot;,
								&quot;_&quot; and &quot;-&quot; (up to 64 characters), and the providers list can be filtered by them.
							</p>
							<FormMessage />
						</FormItem>
					)}
				/>
				<FormField
					control={form.control}
					name="metadata"
					render={({ field }) => (
						<FormItem data-testid="provider-labels-metadata-section">
							<HeadersTable
								label="Metadata"
								value={field.value ?? {}}
								onChange={field.onChange}
								keyPlaceholder="e.g., owner"
								valuePlaceholder="e.g., platform-team"
								useSecretVarInput={false}
								disabled={!hasUpdateProviderAccess}
							/>
							<p className="text-muted-foreground text-xs">
								Key/value details such as owner, region or cost center. Keys use letters, digits, &quot;.&quot;, &quot;_&quot; and
								&quot;-&quot;. Metadata is not secret: do not store credentials here.
							</p>
							<FormMessage />
						</FormItem>
					)}
				/>
				<div className="flex justify-end space-x-2">
					<Button
						type="submit"
						data-testid="provider-labels-save-button"
						disabled={!form.formState.isDirty || !form.formState.isValid || !hasUpdateProviderAccess || isUpdatingProvider}
						isLoading={isUpdatingProvider}
					>
						Save Metadata &amp; Tags
					</Button>
				</div>
			</form>
		</Form>
	);
}