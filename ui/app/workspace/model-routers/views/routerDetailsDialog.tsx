import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Form } from "@/components/ui/form";
import { getErrorMessage } from "@/lib/store";
import { useUpdateModelRouterMutation } from "@/lib/store/apis/governanceApi";
import { ModelRouter } from "@/lib/types/complexityRouter";
import { zodResolver } from "@hookform/resolvers/zod";
import { LoaderCircle } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { RouterIdentityFields, routerIdentitySchema, type RouterIdentityValues } from "./routerIdentityFields";

interface RouterDetailsDialogProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	router: ModelRouter;
}

// RouterDetailsDialog renames a router or changes its description. Its
// classifier and models are edited on their own tabs, so it sends only the
// name and description, and a classifier changed elsewhere since it opened
// is left as it is.
export default function RouterDetailsDialog({ open, onOpenChange, router }: RouterDetailsDialogProps) {
	const { t } = useTranslation("models", { keyPrefix: "routing.modelRouters" });
	const { t: tc } = useTranslation("common");
	const [submitError, setSubmitError] = useState<string | null>(null);
	const [updateRouter, { isLoading: isSaving }] = useUpdateModelRouterMutation();

	const form = useForm<RouterIdentityValues>({
		resolver: zodResolver(routerIdentitySchema),
		defaultValues: { name: "", description: "" },
	});
	useEffect(() => {
		if (!open) return;
		setSubmitError(null);
		form.reset({ name: router.name, description: router.description ?? "" });
	}, [open, router, form]);

	const onSubmit = form.handleSubmit((values) => {
		setSubmitError(null);
		updateRouter({ id: router.id, name: values.name, description: values.description })
			.unwrap()
			.then(() => {
				toast.success(t("detailsSaved"), { position: "top-right" });
				onOpenChange(false);
			})
			.catch((err) => setSubmitError(t("detailsSaveFailed", { error: getErrorMessage(err) })));
	});

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent className="max-w-lg" data-testid="model-router-details-dialog">
				<DialogHeader>
					<DialogTitle>{t("editDetailsTitle")}</DialogTitle>
					<DialogDescription>{t("editDetailsDescription")}</DialogDescription>
				</DialogHeader>
				<Form {...form}>
					<form onSubmit={onSubmit} className="space-y-4" noValidate>
						<RouterIdentityFields control={form.control} />
						{submitError && (
							<p className="text-destructive text-sm" role="alert" data-testid="model-router-details-error">
								{submitError}
							</p>
						)}
						<DialogFooter className="gap-2">
							<Button
								type="button"
								variant="outline"
								size="sm"
								onClick={() => onOpenChange(false)}
								data-testid="model-router-details-cancel-button"
							>
								{tc("cancel")}
							</Button>
							<Button type="submit" size="sm" disabled={isSaving} data-testid="model-router-details-submit-button">
								{isSaving && <LoaderCircle className="size-3.5 animate-spin" />}
								{tc("save")}
							</Button>
						</DialogFooter>
					</form>
				</Form>
			</DialogContent>
		</Dialog>
	);
}