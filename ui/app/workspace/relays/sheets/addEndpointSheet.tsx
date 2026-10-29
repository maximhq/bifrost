import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { getErrorMessage, useCreateRelayMutation } from "@/lib/store";
import { relayFormSchema, type RelayFormValues } from "@/lib/types/schemas";
import { emptyRelayForm, toRelayConfig } from "@/lib/utils/relayForm";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { Waypoints } from "lucide-react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { CredentialFields } from "../fragments/credentialFields";
import { InfoLabel } from "../fragments/infoLabel";

/** Creates a relay from its name, upstream and credential; routes and pricing are added afterwards. */
export function AddEndpointSheet({
	show,
	onCancel,
	onCreated,
}: {
	show: boolean;
	onCancel: () => void;
	onCreated: (name: string) => void;
}) {
	const canCreate = useRbac(RbacResource.Relays, RbacOperation.Create);
	const [createEndpoint, { isLoading }] = useCreateRelayMutation();
	const form = useForm<RelayFormValues>({
		resolver: zodResolver(relayFormSchema),
		defaultValues: emptyRelayForm(),
		mode: "onChange",
	});

	const close = () => {
		form.reset(emptyRelayForm());
		onCancel();
	};

	const onSubmit = async (values: RelayFormValues) => {
		try {
			const created = await createEndpoint(toRelayConfig(values)).unwrap();
			toast.success("Relay added successfully");
			onCreated(created.name);
			close();
		} catch (e) {
			toast.error("Failed to add relay", { description: getErrorMessage(e) });
		}
	};

	return (
		<Sheet open={show} onOpenChange={(open) => !open && close()}>
			<SheetContent className="p-0 pt-4" data-testid="relay-add-sheet" onInteractOutside={(e) => e.preventDefault()}>
				<SheetHeader className="flex flex-col items-start py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8">
					<SheetTitle>
						<div className="font-lg flex items-center gap-2">
							<Waypoints className="h-5 w-5" />
							Add relay
						</div>
					</SheetTitle>
				</SheetHeader>
				<Form {...form}>
					<form onSubmit={form.handleSubmit(onSubmit)} className="flex grow flex-col gap-6 pt-4">
						<div className="grow space-y-4 px-4 md:px-8">
							<FormField
								control={form.control}
								name="name"
								render={({ field }) => (
									<FormItem>
										<InfoLabel label="Name (Required)" info={<>Callers use /relay/&lt;name&gt;. It cannot be changed later.</>} />
										<FormControl>
											<Input placeholder="tavily" data-testid="relay-name" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={form.control}
								name="base_url"
								render={({ field }) => (
									<FormItem>
										<InfoLabel
											label="Base URL (Required)"
											info="Every request is pinned under this origin and path; callers cannot reach another host."
										/>
										<FormControl>
											<Input placeholder="https://api.tavily.com" data-testid="relay-new-base-url" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<CredentialFields form={form} />
						</div>
						<div className="bg-card sticky bottom-0 z-10 flex justify-end gap-2 border-t px-4 py-4 md:px-8">
							<Button type="button" variant="outline" onClick={close} disabled={isLoading}>
								Cancel
							</Button>
							<Button type="submit" disabled={!canCreate || isLoading} isLoading={isLoading} data-testid="relay-create">
								Add Relay
							</Button>
						</div>
					</form>
				</Form>
			</SheetContent>
		</Sheet>
	);
}