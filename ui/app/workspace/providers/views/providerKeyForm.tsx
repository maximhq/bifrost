import { Button } from "@/components/ui/button";
import { ConfigSyncAlert } from "@/components/ui/configSyncAlert";
import { Form } from "@/components/ui/form";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { getErrorMessage, useUpdateProviderMutation } from "@/lib/store";
import { ModelProvider } from "@/lib/types/config";
import { modelProviderKeySchema } from "@/lib/types/schemas";
import { zodResolver } from "@hookform/resolvers/zod";
import { Save } from "lucide-react";
import { useCallback, useEffect } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { v4 as uuid } from "uuid";
import { z } from "zod";
import { ApiKeyFormFragment } from "../fragments";
interface Props {
	provider: ModelProvider;
	keyIndex: number;
	onCancel: () => void;
	onSave: () => void;
}

// Create a simple form schema using only ModelProviderKeySchema
const providerKeyFormSchema = z.object({
	key: modelProviderKeySchema,
});

type ProviderKeyFormValues = z.infer<typeof modelProviderKeySchema>;

export default function ProviderKeyForm({ provider, keyIndex, onCancel, onSave }: Props) {
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();
	const isEditing = provider?.keys?.[keyIndex] !== undefined;
	const currentKey = provider?.keys?.[keyIndex];

	const form = useForm({
		resolver: zodResolver(providerKeyFormSchema),
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: {
			key: (provider?.keys?.[keyIndex] as ProviderKeyFormValues) ?? {
				id: uuid(),
				name: "",
				models: [],
				weight: 1.0,
				enabled: true,
			},
		},
	});

	// Trigger validation on mount when editing existing data
	useEffect(() => {
		if (isEditing) {
			form.trigger();
		}
	}, [isEditing, form]);

	const getTooltipContent = useCallback(() => {
		if (!form.formState.isValid && form.formState.errors.root?.message) {
			return form.formState.errors.root?.message;
		}
		if (!form.formState.isDirty) {
			return "No changes made";
		}
		return null;
	}, [form?.formState.errors, form?.formState.isValid, form?.formState.isDirty]);

	const onSubmit = (value: any) => {
		const keys = provider.keys ?? [];
		const updatedKeys = [...keys.slice(0, keyIndex), value.key, ...keys.slice(keyIndex + 1)];
		updateProvider({
			...provider,
			keys: updatedKeys,
		})
			.unwrap()
			.then(() => {
				onSave();
			})
			.catch((err) => {
				toast.error("Error while updating key", {
					description: getErrorMessage(err),
				});
			});
	};

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6">
				<ApiKeyFormFragment control={form.control} providerName={provider.name} form={form} />
				{isEditing && currentKey?.config_hash && <ConfigSyncAlert className="mt-4" />}
				<div className="dark:bg-card bg-white pt-6">
					<div className="flex justify-end space-x-3">
						<Button type="button" variant="outline" onClick={onCancel}>
							Cancel
						</Button>
						<TooltipProvider>
							<Tooltip>
								<TooltipTrigger asChild>
									<span>
										<Button
											type="submit"
											disabled={!form.formState.isDirty || !form.formState.isValid}
											isLoading={form.formState.isSubmitting || isUpdatingProvider}
										>
											<Save className="h-4 w-4" />
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
