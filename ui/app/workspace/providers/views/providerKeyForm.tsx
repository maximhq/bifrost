import { Button } from "@/components/ui/button";
import { ConfigSyncAlert } from "@/components/ui/configSyncAlert";
import { Form } from "@/components/ui/form";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { getErrorMessage } from "@/lib/store";
import { useCreateProviderKeyMutation, useGetProviderKeysQuery, useUpdateProviderKeyMutation } from "@/lib/store/apis/providersApi";
import { ModelProvider } from "@/lib/types/config";
import { modelProviderKeySchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { Save } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { v4 as uuid } from "uuid";
import { z } from "zod";
import { ApiKeyFormFragment } from "../fragments";
interface Props {
	provider: ModelProvider;
	keyId: string | null;
	onCancel: () => void;
	onSave: () => void;
}

// Create a simple form schema using only ModelProviderKeySchema
const providerKeyFormSchema = z.object({
	key: modelProviderKeySchema,
});

type ProviderKeyFormValues = z.infer<typeof modelProviderKeySchema>;

export default function ProviderKeyForm({ provider, keyId, onCancel, onSave }: Props) {
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const [createProviderKey, { isLoading: isCreatingProviderKey }] = useCreateProviderKeyMutation();
	const [updateProviderKey, { isLoading: isUpdatingProviderKey }] = useUpdateProviderKeyMutation();
	const { data: keys = [] } = useGetProviderKeysQuery(provider.name);
	// When a new copilot key is auto-created mid-flow (after device-auth completes so the
	// backend can populate the model catalog), the parent's `keyId` prop stays null but we
	// track the freshly created id locally so subsequent saves go through the update path
	// and the model picker can resolve the live catalog.
	const [autoCreatedKeyId, setAutoCreatedKeyId] = useState<string | null>(null);
	const effectiveKeyId = keyId ?? autoCreatedKeyId;
	const isEditing = effectiveKeyId !== null;
	const currentKey = effectiveKeyId ? keys.find((k) => k.id === effectiveKeyId) : undefined;

	const form = useForm({
		resolver: zodResolver(providerKeyFormSchema),
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: {
			key: (currentKey as ProviderKeyFormValues) ?? {
				id: uuid(),
				name: "",
				models: ["*"],
				blacklisted_models: [],
				weight: 1.0,
				enabled: true,
			},
		},
	});

	// Reset form when currentKey arrives (handles late async resolution)
	// Skip reset if user has unsaved edits to avoid discarding changes during background refetches
	useEffect(() => {
		if (!isEditing || !currentKey || form.formState.isDirty) return;
		form.reset({ key: currentKey as ProviderKeyFormValues });
	}, [isEditing, currentKey, form]);

	// Trigger validation on mount when editing existing data
	useEffect(() => {
		if (isEditing) {
			form.trigger();
		}
	}, [isEditing, form]);

	const getTooltipContent = useCallback(() => {
		if (!hasUpdateProviderAccess) {
			return "You do not have permission to modify provider keys";
		}
		if (!form.formState.isValid && form.formState.errors.root?.message) {
			return form.formState.errors.root?.message;
		}
		if (!form.formState.isDirty) {
			return "No changes made";
		}
		return null;
	}, [form?.formState.errors, form?.formState.isValid, form?.formState.isDirty, hasUpdateProviderAccess]);

	const onSubmit = (value: any) => {
		if (isEditing && !currentKey) return;
		// Strip internal _auth_type fields before sending to API
		const key = { ...value.key };
		if (key.azure_key_config) {
			const { _auth_type, ...rest } = key.azure_key_config;
			key.azure_key_config = rest;
		}
		if (key.vertex_key_config) {
			const { _auth_type, ...rest } = key.vertex_key_config;
			key.vertex_key_config = rest;
		}
		if (key.bedrock_key_config) {
			const { _auth_type, ...rest } = key.bedrock_key_config;
			key.bedrock_key_config = rest;
		}
		if (key.bedrock_mantle_key_config) {
			const { _auth_type, ...rest } = key.bedrock_mantle_key_config;
			key.bedrock_mantle_key_config = rest;
		}
		const mutation = isEditing
			? updateProviderKey({
					provider: provider.name,
					keyId: currentKey!.id,
					key,
				})
			: createProviderKey({
					provider: provider.name,
					key,
				});
		// Used by submit error toast below — captured before async resolves.
		const submittingAsEdit = isEditing;

		mutation
			.unwrap()
			.then(() => {
				onSave();
			})
			.catch((err) => {
				if (err?.status === 409) {
					form.setError("key.name", { message: getErrorMessage(err) });
					return;
				}
				toast.error(isEditing ? "Error updating key" : "Error creating key", {
					description: getErrorMessage(err),
				});
			});
	};

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="flex grow flex-col gap-6 pt-4">
				<div className="grow px-8">
					<ApiKeyFormFragment
						control={form.control}
						providerName={provider.name}
						baseProviderType={provider.custom_provider_config?.base_provider_type}
						form={form}
						keyId={effectiveKeyId}
						createProviderKey={createProviderKey}
						onAutoCreated={setAutoCreatedKeyId}
					/>
					{isEditing && currentKey?.config_hash && <ConfigSyncAlert className="mt-4" />}
				</div>
				<div className="bg-card sticky bottom-0 border-t px-8 py-4">
					<div className="flex justify-end space-x-3">
						<Button type="button" variant="outline" onClick={onCancel} data-testid="key-cancel-btn">
							Cancel
						</Button>
						<TooltipProvider>
							<Tooltip>
								<TooltipTrigger asChild>
									<span>
										<Button
											type="submit"
											disabled={!form.formState.isDirty || !hasUpdateProviderAccess}
											isLoading={form.formState.isSubmitting || isCreatingProviderKey || isUpdatingProviderKey}
											data-testid="key-save-btn"
										>
											<Save className="h-4 w-4 shrink-0" />
											Save
										</Button>
									</span>
								</TooltipTrigger>
								{getTooltipContent() && <TooltipContent>{getTooltipContent()}</TooltipContent>}
							</Tooltip>
						</TooltipProvider>
					</div>
				</div>
			</form>
		</Form>
	);
}