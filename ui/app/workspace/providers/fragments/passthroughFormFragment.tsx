import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { MultiSelect } from "@/components/ui/multiSelect";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { getErrorMessage, setProviderFormDirtyState, useAppDispatch } from "@/lib/store";
import { useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import type { ModelProvider } from "@/lib/types/config";
import { PASSTHROUGH_METHODS, passthroughFormSchema, type PassthroughFormSchema, type PassthroughMethod } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { AlertTriangle, Plus, Trash2 } from "lucide-react";
import { useEffect } from "react";
import { useFieldArray, useForm, type Resolver, type UseFormReturn } from "react-hook-form";
import { toast } from "sonner";
import { buildProviderUpdatePayload } from "../views/utils";
import {
	emptyRuleRow,
	GENAI_PASSTHROUGH_PREFIX,
	passthroughChanged,
	passthroughModeOptions,
	passthroughRoutePrefix,
	passthroughUpdates,
	rulesForCustomMode,
	savedPassthroughFormValues,
	withPassthroughRequestsAllowed,
	type PassthroughMode,
} from "./passthroughForm.utils";

const METHOD_OPTIONS = PASSTHROUGH_METHODS.map((method) => ({ label: method, value: method }));

const PATH_PLACEHOLDERS: Record<string, string> = {
	"/azure_passthrough": "/openai/**",
	"/bedrock_passthrough": "/knowledgebases/*/retrieve",
	[GENAI_PASSTHROUGH_PREFIX]: "/v1beta/**",
};

function modeDescription(mode: PassthroughMode, isCustom: boolean): string {
	switch (mode) {
		case "all":
			return "Every route is forwarded.";
		case "custom":
			return "Only routes that match a rule are forwarded.";
		case "off":
			return isCustom ? "Passthrough requests that name this provider are refused." : "Passthrough requests to this provider are refused.";
	}
}

interface PassthroughFormFragmentProps {
	provider: ModelProvider;
}

interface PassthroughRuleRowProps {
	form: UseFormReturn<PassthroughFormSchema, any, PassthroughFormSchema>;
	index: number;
	locked: boolean;
	pathPlaceholder: string;
	onRemove: () => void;
}

function PassthroughRuleRow({ form, index, locked, pathPlaceholder, onRemove }: PassthroughRuleRowProps) {
	const testId = `provider-passthrough-row-${index}`;

	return (
		<tr className="border-b align-top last:border-b-0" data-testid={testId}>
			<td className="w-44 px-3 py-2">
				<FormField
					control={form.control}
					name={`rules.${index}.methods`}
					render={({ field }) => (
						<FormItem>
							<FormControl>
								<MultiSelect
									data-testid={`${testId}-methods-select`}
									options={METHOD_OPTIONS}
									defaultValue={field.value}
									onValueChange={(methods) => field.onChange(methods as PassthroughMethod[])}
									placeholder="Any method"
									hideSelectAll
									searchable={false}
									maxCount={PASSTHROUGH_METHODS.length}
									disabled={locked}
									className="border-input text-foreground hover:bg-accent hover:text-accent-foreground min-h-8 rounded-sm bg-transparent text-xs font-normal"
								/>
							</FormControl>
						</FormItem>
					)}
				/>
			</td>
			<td className="px-3 py-2">
				<FormField
					control={form.control}
					name={`rules.${index}.path`}
					render={({ field }) => (
						<FormItem>
							<FormControl>
								<Input
									{...field}
									placeholder={pathPlaceholder}
									className="h-8 font-mono text-xs"
									disabled={locked}
									aria-label="Route path"
									data-testid={`${testId}-path-input`}
								/>
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
			</td>
			<td className="w-10 px-1 py-2">
				<Button
					type="button"
					variant="ghost"
					size="icon"
					className="h-8 w-8"
					disabled={locked}
					onClick={onRemove}
					aria-label="Remove rule"
					data-testid={`${testId}-remove-btn`}
				>
					<Trash2 className="h-3.5 w-3.5" />
				</Button>
			</td>
		</tr>
	);
}

export function PassthroughFormFragment({ provider }: PassthroughFormFragmentProps) {
	const dispatch = useAppDispatch();
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();
	const locked = !hasUpdateProviderAccess || isUpdatingProvider;
	const isCustom = !!provider.custom_provider_config;
	const prefix = passthroughRoutePrefix(provider);
	const pathPlaceholder = (prefix && PATH_PLACEHOLDERS[prefix]) ?? "/v1/**";
	// A stable key for the saved policy, so a refetch returning the same policy does not reset edits.
	const savedKey = JSON.stringify(provider.passthrough ?? null);
	const saved = savedPassthroughFormValues(provider.passthrough, isCustom);

	const form = useForm<PassthroughFormSchema, any, PassthroughFormSchema>({
		resolver: zodResolver(passthroughFormSchema) as Resolver<PassthroughFormSchema, any, PassthroughFormSchema>,
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: saved,
	});
	const { fields, append, remove, replace } = useFieldArray({ control: form.control, name: "rules" });

	const values = form.watch();
	const changed = passthroughChanged(values, saved, isCustom);
	// A custom provider with passthrough turned off in allowed_requests refuses every route.
	const enablesRequests =
		isCustom && values.mode !== "off" && withPassthroughRequestsAllowed(provider.custom_provider_config) !== undefined;
	const rulesError = form.formState.errors.rules?.root?.message ?? form.formState.errors.rules?.message;

	useEffect(() => {
		dispatch(setProviderFormDirtyState(changed));
	}, [changed, dispatch]);

	// A different provider, or a saved policy that changed, replaces the form.
	useEffect(() => {
		form.reset(savedPassthroughFormValues(provider.passthrough, isCustom));
		// eslint-disable-next-line react-hooks/exhaustive-deps -- savedKey stands in for the policy's contents
	}, [form, provider.name, savedKey, isCustom]);

	const save = (data: PassthroughFormSchema) => {
		updateProvider(buildProviderUpdatePayload(provider, passthroughUpdates(data, isCustom, provider.custom_provider_config)))
			.unwrap()
			.then(() => {
				toast.success("Passthrough routes updated");
				form.reset(data);
			})
			.catch((err) => {
				toast.error("Failed to update passthrough routes", { description: getErrorMessage(err) });
			});
	};

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(save)} className="space-y-4 px-4 md:px-6" data-testid="provider-config-passthrough-content">
				<p className="text-muted-foreground text-xs">
					Routes the raw passthrough endpoint forwards to this provider, using a key held by Bifrost.
				</p>
				<div className="bg-muted/50 rounded-sm border p-3 text-xs" data-testid="provider-passthrough-endpoint-hint">
					Clients call <code className="font-mono">{`${prefix}/<path>`}</code>
					{isCustom && (
						<>
							{" "}
							with the header <code className="font-mono">{`x-model-provider: ${provider.name}`}</code>
						</>
					)}
					.
				</div>

				<FormField
					control={form.control}
					name="mode"
					render={({ field }) => (
						<FormItem>
							<FormLabel>Allowed routes</FormLabel>
							<Select
								value={field.value}
								onValueChange={(value) => {
									if (value === "custom") {
										const rules = form.getValues("rules");
										const next = rulesForCustomMode(rules);
										if (next !== rules) replace(next);
									}
									field.onChange(value);
								}}
								disabled={locked}
							>
								<FormControl>
									<SelectTrigger className="w-full" data-testid="provider-passthrough-mode-select">
										<SelectValue />
									</SelectTrigger>
								</FormControl>
								<SelectContent>
									{passthroughModeOptions(isCustom).map((option) => (
										<SelectItem key={option.value} value={option.value} data-testid={`provider-passthrough-mode-option-${option.value}`}>
											{option.label}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
							<FormDescription data-testid="provider-passthrough-mode-description">
								{modeDescription(field.value, isCustom)}
							</FormDescription>
						</FormItem>
					)}
				/>

				{values.mode === "all" && !isCustom && (
					<Alert variant="warning" data-testid="provider-passthrough-all-routes-warning">
						<AlertTriangle />
						<AlertDescription>
							Every provider endpoint, including management APIs such as files, batches and fine-tuning, is reachable with any virtual key.
						</AlertDescription>
					</Alert>
				)}

				{values.mode === "custom" && (
					<div className="space-y-3">
						<p className="text-muted-foreground text-xs">
							Paths match what follows the route prefix{prefix === GENAI_PASSTHROUGH_PREFIX ? ", starting with the API version" : ""}.{" "}
							<code className="font-mono">*</code> matches within one segment, and a final <code className="font-mono">/**</code> matches
							that path and everything below it. No methods means any method.
						</p>
						{fields.length > 0 ? (
							<div className="overflow-x-auto rounded-md border">
								<table className="w-full text-sm" data-testid="provider-passthrough-table">
									<thead>
										<tr className="border-b">
											<th className="px-3 py-2 text-left font-medium">Methods</th>
											<th className="px-3 py-2 text-left font-medium">Path</th>
											<th className="w-10 px-1 py-2">
												<span className="sr-only">Remove</span>
											</th>
										</tr>
									</thead>
									<tbody>
										{fields.map((field, index) => (
											<PassthroughRuleRow
												key={field.id}
												form={form}
												index={index}
												locked={locked}
												pathPlaceholder={pathPlaceholder}
												onRemove={() => remove(index)}
											/>
										))}
									</tbody>
								</table>
							</div>
						) : (
							<p
								className="text-muted-foreground rounded-md border border-dashed px-3 py-6 text-center text-xs"
								data-testid="provider-passthrough-empty"
							>
								No rules.
							</p>
						)}
						{rulesError && (
							<p className="text-destructive text-xs" data-testid="provider-passthrough-rules-error">
								{rulesError}
							</p>
						)}
						<Button
							type="button"
							variant="outline"
							size="sm"
							disabled={locked}
							onClick={() => append(emptyRuleRow())}
							data-testid="provider-passthrough-add-btn"
						>
							<Plus className="mr-1 h-3.5 w-3.5" />
							Add rule
						</Button>
					</div>
				)}

				{enablesRequests && (
					<p className="text-muted-foreground text-xs" data-testid="provider-passthrough-allowed-requests-note">
						Passthrough and Passthrough Stream are off in this provider&apos;s allowed request types. Saving turns them on.
					</p>
				)}

				<div className="flex justify-end pb-6">
					<Button
						type="submit"
						data-testid="provider-passthrough-save-btn"
						disabled={!(changed || enablesRequests) || !form.formState.isValid || locked}
						isLoading={isUpdatingProvider}
					>
						Save Passthrough
					</Button>
				</div>
			</form>
		</Form>
	);
}