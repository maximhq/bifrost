"use client";

import { Button } from "@/components/ui/button";
import { Form } from "@/components/ui/form";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { getErrorMessage } from "@/lib/store";
import { otelFormSchema, type OtelFormSchema, type OtelProfileConfigSchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { useState } from "react";
import { useForm, type Resolver } from "react-hook-form";
import { toast } from "sonner";
import { OtelFormFragment } from "../fragments/otelFormFragment";

interface OtelProfileSheetProps {
	profile?: OtelProfileConfigSchema | null;
	onSave: (profile: OtelProfileConfigSchema) => Promise<void>;
	onCancel: () => void;
}

export default function OtelProfileSheet({ profile, onSave, onCancel }: OtelProfileSheetProps) {
	const [isOpen, setIsOpen] = useState(true);
	const isEditing = !!profile;
	const hasAccess = useRbac(RbacResource.Observability, RbacOperation.Update);
	const [isSaving, setIsSaving] = useState(false);

	const form = useForm<OtelFormSchema, any, OtelFormSchema>({
		resolver: zodResolver(otelFormSchema) as Resolver<OtelFormSchema, any, OtelFormSchema>,
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: {
			otel_profile: profile
				? { ...profile }
				: {
						name: "",
						enabled: true,
						service_name: "bifrost",
						collector_url: "",
						headers: {},
						trace_type: "otel",
						protocol: "http",
						tls_ca_cert: "",
						insecure: true,
						metrics_enabled: false,
						metrics_endpoint: "",
						metrics_push_interval: 15,
					},
		},
	});

	const handleClose = () => {
		setIsOpen(false);
		setTimeout(() => {
			onCancel();
		}, 150);
	};

	const onSubmit = async (data: OtelFormSchema) => {
		if (!hasAccess) {
			toast.error("You don't have permission to perform this action");
			return;
		}
		setIsSaving(true);
		try {
			await onSave(data.otel_profile);
			toast.success(isEditing ? "Profile updated successfully" : "Profile created successfully");
		} catch (error) {
			toast.error(isEditing ? "Failed to update profile" : "Failed to create profile", {
				description: getErrorMessage(error),
			});
		} finally {
			setIsSaving(false);
		}
	};

	return (
		<Sheet open={isOpen} onOpenChange={(open) => !open && handleClose()}>
			<SheetContent
				className="dark:bg-card flex w-full flex-col overflow-x-hidden overflow-y-auto bg-white p-8"
				onInteractOutside={(e) => e.preventDefault()}
				onEscapeKeyDown={(e) => e.preventDefault()}
			>
				<SheetHeader className="flex flex-col items-start">
					<SheetTitle>{isEditing ? "Edit Profile" : "Add Profile"}</SheetTitle>
					<SheetDescription>Configure an OpenTelemetry collector profile for sending traces and metrics.</SheetDescription>
				</SheetHeader>

				<Form {...form}>
					<OtelFormFragment form={form} hasAccess={hasAccess} />

					<div className="mt-auto flex justify-end gap-2 pt-6">
						<Button type="button" variant="outline" onClick={handleClose} disabled={isSaving}>
							Cancel
						</Button>
						<TooltipProvider>
							<Tooltip>
								<TooltipTrigger asChild>
									<span className="inline-block">
										<Button
											type="button"
											disabled={!hasAccess || !form.formState.isDirty || !form.formState.isValid || isSaving}
											isLoading={isSaving}
											onClick={form.handleSubmit(onSubmit)}
										>
											{isEditing ? "Update" : "Create"}
										</Button>
									</span>
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
				</Form>
			</SheetContent>
		</Sheet>
	);
}
