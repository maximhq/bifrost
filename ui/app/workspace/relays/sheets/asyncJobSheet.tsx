import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { RelayRouteMethods, type RelayUsageConfig } from "@/lib/types/relays";
import { relayAsyncJobFormSchema, relayUsageUnitsPathError, type RelayAsyncJobForm } from "@/lib/types/schemas";
import { applyAsyncJob, JOB_ID_PLACEHOLDER, jobBillsRequestUnits } from "@/lib/utils/relayAsyncJob";
import { mergeUsage, routeBillableByDefault } from "@/lib/utils/relayForm";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect, useState, type ReactNode } from "react";
import { useForm, type Control, type FieldValues } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { DecimalInput, decimalText } from "../fragments/decimalInput";
import { HeaderPairsTable } from "../fragments/headerPairsTable";
import { InfoLabel } from "../fragments/infoLabel";
import { JobStatusCheckFields } from "../fragments/jobFields";
import { UsageFields } from "../fragments/usageFields";
import type { RouteForm } from "../routes.utils";

const NEW_ASYNC_JOB: RelayAsyncJobForm = {
	method: "POST",
	path: "/",
	headers: [],
	enabled: true,
	// Most status checks are a GET of the job; a Textract-style API picks POST.
	job: { id_path: "", poll: { method: "GET", path: "/" }, status_path: "", succeeded: [], interval_seconds: 30, timeout_seconds: 3600 },
};

// A start route is buffered and charged: the gateway refuses a job on a streamed or free route.
const asStartRoute = (v: RelayAsyncJobForm): RelayAsyncJobForm => ({
	...v,
	stream: undefined,
	billable: routeBillableByDefault(v.method) ? undefined : true,
});

/** A route as the table shows it: method, path and header matchers. */
function describeRoute(r: Pick<RouteForm, "method" | "path" | "headers">): string {
	const headers = r.headers.filter((h) => h.name.trim()).map((h) => `${h.name}: ${h.value}`);
	return [`${r.method} ${r.path}`, ...headers].join(" · ");
}

function Section({ title, hint, testId, children }: { title: string; hint: string; testId: string; children: ReactNode }) {
	return (
		<section className="space-y-4 rounded-sm border p-4" data-testid={testId}>
			<div className="space-y-0.5">
				<h3 className="text-sm font-semibold">{title}</h3>
				<p className="text-muted-foreground text-sm">{hint}</p>
			</div>
			{children}
		</section>
	);
}

interface AsyncJobSheetProps {
	show: boolean;
	routes: RouteForm[];
	/** The job's start route, or -1 for a new job. */
	editIndex: number;
	/** Identifies the opened route, so a reorder while the sheet is open keeps the typing. */
	openKey: string;
	/** The opened route is no longer in the list, so saving could only overwrite another one. */
	routeMissing?: boolean;
	endpointUsage: RelayUsageConfig | undefined;
	ignoreUpstreamCost?: boolean;
	saving: boolean;
	disabled?: boolean;
	onCancel: () => void;
	onSave: (next: RouteForm[]) => void;
}

/**
 * Sets up an async job in one place: the start route, which is charged and carries the job; how
 * Bifrost checks the job until it finishes; and the price of a finished job. The caller's own status
 * checks are relayed free by the gateway, so they need no route here.
 */
export function AsyncJobSheet({
	show,
	routes,
	editIndex,
	openKey,
	routeMissing,
	endpointUsage,
	ignoreUpstreamCost,
	saving,
	disabled,
	onCancel,
	onSave,
}: AsyncJobSheetProps) {
	const { t } = useTranslation("config");
	// Fields stay locked while saving, so a late edit cannot be dropped when the sheet closes.
	const locked = disabled || saving;
	const editing = editIndex >= 0 ? routes[editIndex] : undefined;
	const form = useForm<RelayAsyncJobForm>({ resolver: zodResolver(relayAsyncJobFormSchema), defaultValues: NEW_ASYNC_JOB });
	// Remounts the status check fields after a reset, so their rows show the job being opened.
	const [openedJob, setOpenedJob] = useState(0);
	// Reset on open only: the table rebuilds its routes on every render, which must not wipe the typing.
	useEffect(() => {
		if (!show) return;
		form.reset(editing?.job ? (structuredClone(editing) as RelayAsyncJobForm) : structuredClone(NEW_ASYNC_JOB));
		setOpenedJob((n) => n + 1);
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [show, openKey]);

	const values = form.watch();
	// A start route identical to another is refused, judged on the routes as they would be saved.
	const duplicateOf = applyAsyncJob(routes, editIndex, asStartRoute({ ...values, path: values.path?.trim() ?? "" })).duplicateOf;
	const startUsage = mergeUsage(endpointUsage, values.usage);
	// The settlement charges only a fee the job sets itself: the start call already paid the route fee.
	const jobInherit = startUsage ? { ...startUsage, cost_per_request: undefined } : {};
	const statuses = (values.job?.succeeded ?? []).join(` ${t("or", { ns: "common" })} `);

	const save = (v: RelayAsyncJobForm) => {
		if (routeMissing) return;
		// The gateway judges the job's pricing merged over the route's and the relay's.
		const message = relayUsageUnitsPathError(v.job.usage ?? undefined, mergeUsage(endpointUsage, v.usage) ?? {});
		if (message) {
			form.setError("job.usage.units_path", { message });
			return;
		}
		// The job is priced from its final reply, so request-side units it would inherit cannot be billed.
		if (jobBillsRequestUnits(endpointUsage, v.usage, v.job.usage)) {
			form.setError("job.usage.units_source", { message: t("validation.relays.jobRequestUnits", { ns: "common" }) });
			return;
		}
		const result = applyAsyncJob(routes, editIndex, asStartRoute(v));
		if (result.duplicateOf !== undefined) return;
		onSave(result.routes);
	};

	return (
		<Sheet open={show} onOpenChange={(open) => !open && onCancel()}>
			<SheetContent className="p-0 pt-4 sm:max-w-2xl" data-testid="relay-async-job-sheet" onInteractOutside={(e) => e.preventDefault()}>
				<SheetHeader className="flex flex-col items-start py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8">
					<SheetTitle>
						<div className="font-lg flex items-center gap-2">
							{editing ? t("relays.asyncJob.titleEdit") : t("relays.asyncJob.titleAdd")}
						</div>
					</SheetTitle>
					<p className="text-muted-foreground text-sm">{t("relays.asyncJob.description")}</p>
				</SheetHeader>
				<Form {...form}>
					<form onSubmit={form.handleSubmit(save)} className="flex grow flex-col gap-6 pt-4">
						<div className="grow space-y-6 px-4 md:px-8">
							{routeMissing && (
								<Alert variant="destructive" data-testid="relay-async-job-route-missing">
									<AlertDescription>{t("relays.routes.routeChanged")}</AlertDescription>
								</Alert>
							)}
							<Section title={t("relays.asyncJob.startTitle")} hint={t("relays.asyncJob.startHint")} testId="relay-async-job-start">
								<div className="flex w-full flex-row items-start gap-4">
									<FormField
										control={form.control}
										name="method"
										render={({ field }) => (
											<FormItem className="w-32">
												<FormLabel>{t("relays.fields.method")}</FormLabel>
												<Select onValueChange={field.onChange} value={field.value} disabled={locked}>
													<FormControl>
														<SelectTrigger className="w-full" data-testid="relay-async-job-method">
															<SelectValue />
														</SelectTrigger>
													</FormControl>
													<SelectContent>
														{RelayRouteMethods.map((m) => (
															<SelectItem key={m} value={m}>
																{m}
															</SelectItem>
														))}
													</SelectContent>
												</Select>
											</FormItem>
										)}
									/>
									<FormField
										control={form.control}
										name="path"
										render={({ field }) => (
											<FormItem className="flex-1">
												<InfoLabel label={t("relays.routeSheet.pathLabel")} info={t("relays.routeSheet.pathInfo")} />
												<FormControl>
													<Input placeholder="/jobs" disabled={locked} data-testid="relay-async-job-path" {...field} />
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
								</div>
								<FormField
									control={form.control}
									name="headers"
									render={({ field }) => (
										<FormItem>
											<InfoLabel label={t("relays.routeSheet.headersLabel")} info={t("relays.routeSheet.headersInfo")} />
											<HeaderPairsTable
												value={field.value}
												onChange={field.onChange}
												errors={form.formState.errors.headers}
												keyPlaceholder="X-Amz-Target"
												valuePlaceholder="Textract.StartDocumentAnalysis"
												disabled={locked}
												testIdPrefix="relay-async-job-header"
											/>
										</FormItem>
									)}
								/>
								<div className="flex w-full flex-row items-start gap-4">
									<FormField
										control={form.control}
										name="usage.cost_per_request"
										render={({ field }) => (
											<FormItem className="flex-1">
												<InfoLabel label={t("relays.asyncJob.feeLabel")} info={t("relays.asyncJob.feeInfo")} />
												<FormControl>
													<DecimalInput
														placeholder={decimalText(endpointUsage?.cost_per_request) || "0.01"}
														disabled={locked}
														data-testid="relay-async-job-fee"
														value={field.value}
														onChange={field.onChange}
													/>
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
									<FormField
										control={form.control}
										name="job.id_path"
										render={({ field }) => (
											<FormItem className="flex-1">
												<InfoLabel label={t("relays.job.idPathLabel")} info={t("relays.job.idPathInfo")} />
												<FormControl>
													{/* i18n-ignore: sample path */}
													<Input placeholder="JobId" disabled={locked} data-testid="relay-job-id-path" {...field} />
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
								</div>
								{duplicateOf !== undefined && (
									<Alert variant="destructive" data-testid="relay-async-job-start-duplicate">
										<AlertDescription>
											{t("relays.asyncJob.startRouteDuplicate", { route: describeRoute(routes[duplicateOf]) })}
										</AlertDescription>
									</Alert>
								)}
							</Section>

							<Section
								title={t("relays.asyncJob.statusTitle")}
								hint={t("relays.asyncJob.statusHint", { token: JOB_ID_PLACEHOLDER })}
								testId="relay-async-job-status"
							>
								<JobStatusCheckFields key={openedJob} control={form.control} disabled={locked} />
							</Section>

							<Section title={t("relays.job.pricingTitle")} hint={t("relays.job.pricingInfo")} testId="relay-async-job-pricing">
								<UsageFields
									control={form.control as unknown as Control<FieldValues>}
									name="job.usage"
									value={values.job?.usage}
									inheritFrom={jobInherit}
									ignoreUpstreamCost={ignoreUpstreamCost}
									disabled={locked}
									testIdPrefix="relay-job-usage"
								/>
								<p className="bg-muted rounded-sm p-3 text-sm" data-testid="relay-async-job-summary">
									{t("relays.asyncJob.summary", { statuses: statuses || "…" })}
								</p>
							</Section>
						</div>

						<div className="bg-card sticky bottom-0 z-10 flex justify-end gap-2 border-t px-4 py-4 md:px-8">
							<Button type="button" variant="outline" onClick={onCancel} data-testid="relay-async-job-cancel">
								{t("cancel", { ns: "common" })}
							</Button>
							<Button
								type="submit"
								// A save in flight sends the whole route list; a second one could land first and be overwritten.
								disabled={disabled || duplicateOf !== undefined || routeMissing || saving}
								isLoading={saving}
								data-testid="relay-async-job-save"
							>
								{editing ? t("relays.asyncJob.saveEdit") : t("relays.asyncJob.saveAdd")}
							</Button>
						</div>
					</form>
				</Form>
			</SheetContent>
		</Sheet>
	);
}