import { MCPClientSelector } from "@/components/entitySelectors/mcpClientSelector";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { getErrorMessage, setProviderFormDirtyState, useAppDispatch, useGetMCPClientsQuery, useLazyGetMCPClientsQuery } from "@/lib/store";
import { useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import type { ModelProvider } from "@/lib/types/config";
import { injectedToolsFormSchema, type InjectedToolsFormSchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { Plus, Trash2 } from "lucide-react";
import type { ParseKeys } from "i18next";
import { useEffect, useState } from "react";
import { useFieldArray, useForm, type Resolver, type UseFormReturn } from "react-hook-form";
import { Trans, useTranslation } from "react-i18next";
import { toast } from "sonner";
import { buildProviderUpdatePayload } from "../views/utils";
import {
	emptyToolRow,
	hydratedValues,
	lookupSavedClient,
	nameAfterServerChange,
	nextSavedClientState,
	pickersDisabled,
	prefilledName,
	replacesNativeWebSearch,
	savedFormValues,
	startedSavedClientState,
	toInjectedTools,
	toolMenuState,
	withResolvedClientId,
	type SavedClientState,
} from "./injectedToolsForm.utils";

const SAVED_CLIENT_PAGE_SIZE = 50;

const TOOL_PLACEHOLDER_KEYS: Record<ReturnType<typeof toolMenuState>, ParseKeys<"models">> = {
	"no-server": "providers.injectedTools.placeholders.noServer",
	loading: "providers.injectedTools.placeholders.loading",
	error: "providers.injectedTools.placeholders.error",
	empty: "providers.injectedTools.placeholders.empty",
	ready: "providers.injectedTools.placeholders.ready",
};

interface InjectedToolsFormFragmentProps {
	provider: ModelProvider;
}

// The provider stores the MCP client by name, while the client picker works by id. The
// list search matches names by substring, so it pages on until the exact name turns up.
// It reports the name the id was resolved for, so callers never pair an id with another name,
// and whether the lookup failed or found the server gone; retry runs it again. It runs only
// while enabled: a row whose server was picked in this form already has its id.
function useSavedClientId(clientName: string, enabled: boolean): SavedClientState & { retry: () => void } {
	const [fetchClients] = useLazyGetMCPClientsQuery();
	const [resolved, setResolved] = useState<SavedClientState>({ name: "", id: "", status: "idle" });
	const [attempt, setAttempt] = useState(0);
	useEffect(() => {
		if (!enabled || clientName === "") return;
		let cancelled = false;
		setResolved((previous) => startedSavedClientState(clientName, previous));
		lookupSavedClient(
			(offset) => fetchClients({ search: clientName, limit: SAVED_CLIENT_PAGE_SIZE, offset }, true).unwrap(),
			clientName,
			SAVED_CLIENT_PAGE_SIZE,
			() => cancelled,
		).then((result) => {
			const next = nextSavedClientState(clientName, result);
			if (!cancelled && next) setResolved(next);
		});
		return () => {
			cancelled = true;
		};
	}, [clientName, enabled, fetchClients, attempt]);
	return { ...resolved, retry: () => setAttempt((n) => n + 1) };
}

interface InjectedToolRowProps {
	form: UseFormReturn<InjectedToolsFormSchema, any, InjectedToolsFormSchema>;
	index: number;
	lockPickers: boolean;
	onRemove: () => void;
}

function InjectedToolRow({ form, index, lockPickers, onRemove }: InjectedToolRowProps) {
	const { t } = useTranslation("models");
	const { t: tc } = useTranslation("common");
	const testId = `provider-tools-row-${index}`;
	const name = form.watch(`tools.${index}.name`);
	const clientId = form.watch(`tools.${index}.mcp_client_id`);
	const clientName = form.watch(`tools.${index}.mcp_client_name`);

	// A saved row carries the client name only; resolve its id for the picker.
	const savedClient = useSavedClientId(clientName, clientId === "");
	const savedClientStatus = clientId === "" && savedClient.name === clientName ? savedClient.status : "idle";
	useEffect(() => {
		if (savedClient.status !== "found" || savedClient.name !== clientName) return;
		const resolved = withResolvedClientId(form.getValues(), index, savedClient.name, savedClient.id);
		if (!resolved) return;
		// An untouched form takes the id as its saved state; an edited one keeps its edits.
		const values = hydratedValues(form.formState.isDirty, resolved);
		if (values) form.reset(values);
		else form.setValue(`tools.${index}.mcp_client_id`, savedClient.id, { shouldValidate: true });
		// eslint-disable-next-line react-hooks/exhaustive-deps -- runs when the id resolves, not on every edit
	}, [savedClient.status, savedClient.id, savedClient.name]);

	const toolsQuery = useGetMCPClientsQuery({ server: clientId, limit: 1 }, { skip: clientId === "" });
	const selectedClient = toolsQuery.data?.clients?.[0];
	const tools = selectedClient?.tools ?? [];
	const menuState = toolMenuState(clientId, toolsQuery, tools.length);

	// The picker reports only the id; the provider config stores the name.
	useEffect(() => {
		if (
			selectedClient &&
			selectedClient.config.client_id === clientId &&
			form.getValues(`tools.${index}.mcp_client_name`) !== selectedClient.config.name
		) {
			form.setValue(`tools.${index}.mcp_client_name`, selectedClient.config.name, { shouldDirty: true, shouldValidate: true });
		}
	}, [selectedClient, clientId, form, index]);

	return (
		<tr className="border-b align-top last:border-b-0" data-testid={testId}>
			<td className="px-3 py-2">
				<FormField
					control={form.control}
					name={`tools.${index}.name`}
					render={({ field }) => (
						<FormItem>
							<FormControl>
								<Input
									{...field}
									placeholder="web_search"
									className="h-8 font-mono text-xs"
									disabled={lockPickers}
									aria-label={t("providers.injectedTools.toolNameAria")}
									data-testid={`${testId}-name`}
								/>
							</FormControl>
							{replacesNativeWebSearch(name) && (
								<Badge variant="secondary" className="text-xs" data-testid={`${testId}-replaces-web-search`}>
									{t("providers.injectedTools.replacesWebSearch")}
								</Badge>
							)}
							<FormMessage />
						</FormItem>
					)}
				/>
			</td>
			<td className="px-3 py-2">
				<FormField
					control={form.control}
					name={`tools.${index}.mcp_client_id`}
					render={({ field }) => (
						<FormItem>
							<FormControl>
								<div data-testid={`${testId}-client-select`}>
									<MCPClientSelector
										value={field.value}
										onChange={(value) => {
											field.onChange(value);
											const currentName = form.getValues(`tools.${index}.name`);
											const nextName = nameAfterServerChange(currentName, form.getValues(`tools.${index}.tool_name`));
											if (nextName !== currentName) form.setValue(`tools.${index}.name`, nextName, { shouldDirty: true });
											form.setValue(`tools.${index}.mcp_client_name`, "", { shouldDirty: true });
											form.setValue(`tools.${index}.tool_name`, "", { shouldDirty: true, shouldValidate: true });
										}}
										disabled={lockPickers}
										fallbackOption={field.value && clientName ? { value: field.value, label: clientName } : null}
									/>
								</div>
							</FormControl>
							{savedClientStatus === "loading" && (
								<p className="text-muted-foreground text-xs" data-testid={`${testId}-client-loading`}>
									{t("providers.injectedTools.loadingSavedServer", { name: clientName })}
								</p>
							)}
							{savedClientStatus === "missing" && (
								<p className="text-destructive text-xs" data-testid={`${testId}-client-missing`}>
									{t("providers.injectedTools.savedServerMissing", { name: clientName })}
								</p>
							)}
							{savedClientStatus === "error" && (
								<div className="text-destructive flex items-center gap-2 text-xs" data-testid={`${testId}-client-error`}>
									{t("providers.injectedTools.savedServerError", { name: clientName })}
									<Button type="button" variant="outline" size="sm" onClick={savedClient.retry} data-testid={`${testId}-client-retry`}>
										{tc("retry")}
									</Button>
								</div>
							)}
							{savedClientStatus === "idle" && <FormMessage />}
						</FormItem>
					)}
				/>
			</td>
			<td className="px-3 py-2">
				<FormField
					control={form.control}
					name={`tools.${index}.tool_name`}
					render={({ field }) => (
						<FormItem>
							<Select
								value={field.value}
								onValueChange={(value) => {
									const current = form.getValues(`tools.${index}.name`);
									const next = prefilledName(current, field.value, value);
									if (next !== current) form.setValue(`tools.${index}.name`, next, { shouldDirty: true, shouldValidate: true });
									field.onChange(value);
								}}
								disabled={lockPickers || menuState !== "ready"}
							>
								<FormControl>
									<SelectTrigger className="h-8 w-full text-xs" data-testid={`${testId}-tool-select`}>
										<SelectValue placeholder={t(TOOL_PLACEHOLDER_KEYS[menuState])} />
									</SelectTrigger>
								</FormControl>
								<SelectContent>
									{tools.map((tool) => (
										<SelectItem key={tool.name} value={tool.name} data-testid={`${testId}-tool-option-${tool.name}`}>
											{tool.name}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
							{menuState === "error" && (
								<div className="text-destructive flex items-center gap-2 text-xs" data-testid={`${testId}-tool-error`}>
									{t("providers.injectedTools.toolsLoadError")}
									<Button
										type="button"
										variant="outline"
										size="sm"
										onClick={() => toolsQuery.refetch()}
										data-testid={`${testId}-tool-retry`}
									>
										{tc("retry")}
									</Button>
								</div>
							)}
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
					disabled={lockPickers}
					onClick={onRemove}
					aria-label={name ? t("providers.injectedTools.removeToolNamed", { name }) : t("providers.injectedTools.removeTool")}
					data-testid={`${testId}-remove-btn`}
				>
					<Trash2 className="h-3.5 w-3.5" />
				</Button>
			</td>
		</tr>
	);
}

export function InjectedToolsFormFragment({ provider }: InjectedToolsFormFragmentProps) {
	const { t } = useTranslation("models");
	const { t: tc } = useTranslation("common");
	const dispatch = useAppDispatch();
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();
	const lockPickers = pickersDisabled(hasUpdateProviderAccess, isUpdatingProvider);
	// A stable key for the saved list, so a refetch returning the same list does not reset edits.
	const savedKey = JSON.stringify(provider.injected_tools ?? []);

	const form = useForm<InjectedToolsFormSchema, any, InjectedToolsFormSchema>({
		resolver: zodResolver(injectedToolsFormSchema) as Resolver<InjectedToolsFormSchema, any, InjectedToolsFormSchema>,
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: savedFormValues(provider.injected_tools),
	});
	const { fields, append, remove } = useFieldArray({ control: form.control, name: "tools" });

	useEffect(() => {
		dispatch(setProviderFormDirtyState(form.formState.isDirty));
	}, [form.formState.isDirty, dispatch]);

	// A different provider, or a saved list that changed, replaces the form.
	useEffect(() => {
		form.reset(savedFormValues(provider.injected_tools));
		// eslint-disable-next-line react-hooks/exhaustive-deps -- savedKey stands in for the list's contents
	}, [form, provider.name, savedKey]);

	const save = (data: InjectedToolsFormSchema) => {
		const injectedTools = toInjectedTools(data);
		updateProvider(buildProviderUpdatePayload(provider, { injected_tools: injectedTools }))
			.unwrap()
			.then(() => {
				toast.success(injectedTools ? t("providers.injectedTools.toolsUpdated") : t("providers.injectedTools.toolsRemoved"));
				form.reset(data);
			})
			.catch((err) => {
				toast.error(t("providers.injectedTools.updateFailed"), { description: getErrorMessage(err) });
			});
	};

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(save)} className="space-y-4 px-4 md:px-6" data-testid="provider-config-tools-content">
				<p className="text-muted-foreground text-xs">
					<Trans t={t} i18nKey="providers.injectedTools.intro" components={{ code: <code className="font-mono" /> }} />
				</p>
				{fields.length > 0 ? (
					<div className="overflow-x-auto rounded-md border">
						<table className="w-full text-sm" data-testid="provider-tools-table">
							<thead>
								<tr className="border-b">
									<th className="px-3 py-2 text-left font-medium">{t("providers.name")}</th>
									<th className="px-3 py-2 text-left font-medium">{t("providers.injectedTools.colServer")}</th>
									<th className="px-3 py-2 text-left font-medium">{t("providers.injectedTools.colTool")}</th>
									<th className="w-10 px-1 py-2">
										<span className="sr-only">{t("providers.injectedTools.remove")}</span>
									</th>
								</tr>
							</thead>
							<tbody>
								{fields.map((field, index) => (
									<InjectedToolRow key={field.id} form={form} index={index} lockPickers={lockPickers} onRemove={() => remove(index)} />
								))}
							</tbody>
						</table>
					</div>
				) : (
					<p
						className="text-muted-foreground rounded-md border border-dashed px-3 py-6 text-center text-xs"
						data-testid="provider-tools-empty"
					>
						{t("providers.injectedTools.empty")}
					</p>
				)}

				<div className="flex justify-between space-x-2 pb-6">
					<Button
						type="button"
						variant="outline"
						size="sm"
						disabled={lockPickers}
						onClick={() => append(emptyToolRow())}
						data-testid="provider-tools-add-btn"
					>
						<Plus className="mr-1 h-3.5 w-3.5" />
						{t("providers.injectedTools.addTool")}
					</Button>
					<Button
						type="submit"
						data-testid="provider-tools-save-btn"
						disabled={!form.formState.isDirty || !form.formState.isValid || !hasUpdateProviderAccess || isUpdatingProvider}
						isLoading={isUpdatingProvider}
					>
						{t("providers.injectedTools.save")}
					</Button>
				</div>
			</form>
		</Form>
	);
}