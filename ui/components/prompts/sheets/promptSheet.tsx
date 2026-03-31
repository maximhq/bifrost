import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { getErrorMessage } from "@/lib/store";
import { useCreatePromptMutation, useUpdatePromptMutation } from "@/lib/store/apis/promptsApi";
import { Prompt } from "@/lib/types/prompts";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";

interface PromptFormData {
	name: string;
}

interface PromptSheetProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	prompt?: Prompt;
	folderId?: string;
	onSaved: (promptId?: string) => void;
}

export function PromptSheet({ open, onOpenChange, prompt, folderId, onSaved }: PromptSheetProps) {
	const [createPrompt, { isLoading: isCreating }] = useCreatePromptMutation();
	const [updatePrompt, { isLoading: isUpdating }] = useUpdatePromptMutation();

	const isLoading = isCreating || isUpdating;
	const isEditing = !!prompt;

	const {
		register,
		handleSubmit,
		reset,
		formState: { errors },
	} = useForm<PromptFormData>({
		defaultValues: { name: "" },
	});

	useEffect(() => {
		if (open) {
			reset({ name: prompt?.name ?? "" });
		}
	}, [open, prompt, reset]);

	async function onSubmit(data: PromptFormData) {
		try {
			if (isEditing) {
				await updatePrompt({
					id: prompt.id,
					data: { name: data.name.trim() },
				}).unwrap();
				toast.success("Prompt updated");
				onSaved();
			} else {
				const result = await createPrompt({
					name: data.name.trim(),
					...(folderId ? { folder_id: folderId } : {}),
				}).unwrap();
				toast.success("Prompt created");
				onSaved(result.prompt.id);
			}
			onOpenChange(false);
		} catch (err) {
			toast.error(`Failed to ${isEditing ? "update" : "create"} prompt`, {
				description: getErrorMessage(err),
			});
		}
	}

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="p-8" onOpenAutoFocus={(e) => { e.preventDefault(); document.getElementById("name")?.focus(); }}>
				<form onSubmit={handleSubmit(onSubmit)}>
					<SheetHeader className="flex flex-col items-start">
						<SheetTitle>{isEditing ? "Rename Prompt" : "Create Prompt"}</SheetTitle>
						<SheetDescription>{isEditing ? "Update the prompt name." : folderId ? "Create a new prompt in this folder." : "Create a new prompt."}</SheetDescription>
					</SheetHeader>

					<div className="mt-6 space-y-4">
						<div className="space-y-2">
							<Label htmlFor="name">Name</Label>
							<Input
								id="name"
								data-testid="prompt-name-input"
								placeholder="Customer Support Assistant"
								{...register("name", {
									required: "Prompt name is required",
									validate: (v) => v.trim().length > 0 || "Prompt name cannot be blank",
								})}
								autoFocus
							/>
							{errors.name && <p className="text-destructive text-xs">{errors.name.message}</p>}
						</div>
					</div>

					<SheetFooter className="mt-6 flex flex-row items-center justify-end gap-2 p-0">
						<Button type="button" variant="outline" data-testid="prompt-cancel" onClick={() => onOpenChange(false)}>
							Cancel
						</Button>
						<Button type="submit" data-testid="prompt-submit" disabled={isLoading}>
							{isLoading ? "Saving..." : isEditing ? "Update" : "Create"}
						</Button>
					</SheetFooter>
				</form>
			</SheetContent>
		</Sheet>
	);
}
