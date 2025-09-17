import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { getErrorMessage, useCreateProviderMutation } from "@/lib/store";
import { toast } from "sonner";
import { KnownProvider, ModelProviderName } from "@/lib/types/config";
import { AlertTriangle } from "lucide-react";
import { AllowedRequestsFields } from "../fragments/allowedRequestsFields";

const allowedRequestsSchema = z.object({
	text_completion: z.boolean(),
	chat_completion: z.boolean(),
	chat_completion_stream: z.boolean(),
	embedding: z.boolean(),
	speech: z.boolean(),
	speech_stream: z.boolean(),
	transcription: z.boolean(),
	transcription_stream: z.boolean(),
});

const formSchema = z.object({
	name: z.string().min(1),
	baseFormat: z.string().min(1),
	allowed_requests: allowedRequestsSchema,
});

type FormData = z.infer<typeof formSchema>;

interface Props {
	show: boolean;
	onSave: () => void;
	onClose: () => void;
}

export default function AddCustomProviderDialog({ show, onClose, onSave }: Props) {
	const [addProvider, { isLoading: isAddingProvider }] = useCreateProviderMutation();
	const form = useForm<FormData>({
		resolver: zodResolver(formSchema),
		defaultValues: {
			name: "",
			baseFormat: "",
			allowed_requests: {
				text_completion: true,
				chat_completion: true,
				chat_completion_stream: true,
				embedding: true,
				speech: true,
				speech_stream: true,
				transcription: true,
				transcription_stream: true,
			},
		},
	});

	const onSubmit = (data: FormData) => {
		addProvider({
			provider: data.name as ModelProviderName,
			custom_provider_config: {
				base_provider_type: data.baseFormat as KnownProvider,
				allowed_requests: data.allowed_requests,
			},
			keys: [],
		})
			.unwrap()
			.then(() => {
				onSave();
				form.reset();
			})
			.catch((err) => {
				toast.error("Failed to add provider", {
					description: getErrorMessage(err),
				});
			});
	};

	return (
		<Dialog open={show} onOpenChange={(open) => !open && onClose()}>
			<DialogContent className="custom-scrollbar max-h-[80vh] max-w-[600px] overflow-y-scroll">
				<DialogHeader>
					<DialogTitle>Add Custom Provider</DialogTitle>
					<DialogDescription>Enter the details of your custom provider.</DialogDescription>
				</DialogHeader>
				<Form {...form}>
					<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
						{form.formState.isDirty && (
							<Alert>
								<AlertTriangle className="h-4 w-4" />
								<AlertDescription>Creating a custom provider requires a Bifrost service restart to take effect.</AlertDescription>
							</Alert>
						)}

						<FormField
							control={form.control}
							name="name"
							render={({ field }) => (
								<FormItem className="flex flex-col gap-3">
									<FormLabel className="text-right">Name</FormLabel>
									<div className="col-span-3">
										<FormControl>
											<Input placeholder="Name" {...field} />
										</FormControl>
										<FormMessage />
									</div>
								</FormItem>
							)}
						/>

						<FormField
							control={form.control}
							name="baseFormat"
							render={({ field }) => (
								<FormItem className="flex flex-col gap-3">
									<FormLabel>Base Format</FormLabel>
									<div>
										<FormControl>
											<Select onValueChange={field.onChange} defaultValue={field.value}>
												<SelectTrigger className="w-full">
													<SelectValue placeholder="Select base format" />
												</SelectTrigger>
												<SelectContent>
													<SelectItem value="openai">OpenAI</SelectItem>
													<SelectItem value="anthropic">Anthropic</SelectItem>
													<SelectItem value="gemini">Gemini</SelectItem>
													<SelectItem value="cohere">Cohere</SelectItem>
													<SelectItem value="bedrock">AWS Bedrock</SelectItem>
												</SelectContent>
											</Select>
										</FormControl>
										<FormMessage />
									</div>
								</FormItem>
							)}
						/>

						{/* Allowed Requests Configuration */}
						<AllowedRequestsFields control={form.control} />

						<DialogFooter className="flex flex-row gap-2">
							<Button type="button" variant="outline" onClick={onClose}>
								Cancel
							</Button>
							<Button type="submit">Add</Button>
						</DialogFooter>
					</form>
				</Form>
			</DialogContent>
		</Dialog>
	);
}
