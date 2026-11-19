import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { RelayRouteMethods, type RelayUsageConfig } from "@/lib/types/relays";
import { relayAsyncJobFormSchema, type RelayAsyncJobForm } from "@/lib/types/schemas";
import { applyAsyncJob, JOB_ID_PLACEHOLDER, type StatusRoutePlan } from "@/lib/utils/relayAsyncJob";
import { mergeUsage, routeBillableByDefault } from "@/lib/utils/relayForm";
import { zodResolver } from "@hookform/resolvers/zod";
import { Hourglass } from "lucide-react";
import { useEffect, type ReactNode } from "react";
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
	job: { id_path: "", poll: { method: "POST", path: "/" }, status_path: "", succeeded: [], interval_seconds: 30, timeout_seconds: 3600 },
};

// A start route is buffered and charged: the gateway refuses a job on a streamed or free route.
const asStartRoute = (v: RelayAsyncJobForm): RelayAsyncJobForm => ({
	...v,
	stream: undefined,
	billable: routeBillableByDefault(v.method) ? undefined : true,
});

/** A route as the table shows it: method, path and header matchers. */
export function describeRoute(r: Pick<RouteForm, "method" | "path" | "headers">): string {
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
	endpointUsage: RelayUsageConfig | undefined;
	ignoreUpstreamCost?: boolean;
	saving: boolean;
	disabled?: boolean;
	onCancel: () => void;
	onSave: (next: RouteForm[]) => void;
}

/**
 * Sets up an async job in one place: the start route, which is charged and carries the job; how
 * Bifrost checks the job until it finishes; and the price of a finished job. Saving also makes sure a
 * free route takes the caller's own status checks, since the job is charged once.
 */
export function AsyncJobSheet({
	show,
	routes,
	editIndex,
	endpointUsage,
	ignoreUpstreamCost,
	saving,
	disabled,
	onCancel,
	onSave,
}: AsyncJobSheetProps) {
	const { t } = useTranslation("config");
	const editing = editIndex >= 0 ? routes[editIndex] : undefined;
	const form = useForm<RelayAsyncJobForm>({ resolver: zodResolver(relayAsyncJobFormSchema), defaultValues: NEW_ASYNC_JOB });
	// Reset on open only: the table rebuilds its routes on every render, which must not wipe the typing.
	useEffect(() => {
		if (show) form.reset(editing?.job ? (structuredClone(editing) as RelayAsyncJobForm) : structuredClone(NEW_ASYNC_JOB));
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [show, editIndex]);

	const values = form.watch();
	// The status route is judged on the routes as they would be saved, so the person sees it before saving.
	const plan: StatusRoutePlan | undefined = values.job?.poll?.path?.startsWith("/")
		? applyAsyncJob(routes, editIndex, asStartRoute(values)).plan
		: undefined;
	const startUsage = mergeUsage(endpointUsage, values.usage);
	// The settlement charges only a fee the job sets itself: the start call already paid the route fee.
	const jobInherit = startUsage ? { ...startUsage, cost_per_request: undefined } : {};
	const statuses = (values.job?.succeeded ?? []).join(` ${t("or", { ns: "common" })} `);

	const save = (v: RelayAsyncJobForm) => {
		const result = applyAsyncJob(routes, editIndex, asStartRoute(v));
		if (result.plan.kind === "blocked") return;
		onSave(result.routes);
	};

	return (
		<Sheet open={show} onOpenChange={(open) => !open && onCancel()}>
			<SheetContent className="p-0 pt-4 sm:max-w-2xl" data-testid="relay-async-job-sheet" onInteractOutside={(e) => e.preventDefault()}>
				<SheetHeader className="flex flex-col items-start py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8">
					<SheetTitle>
						<div className="font-lg flex items-center gap-2">
							<Hourglass className="h-5 w-5" />
							{editing ? t("relays.asyncJob.titleEdit") : t("relays.asyncJob.titleAdd")}
						</div>
					</SheetTitle>
					<p className="text-muted-foreground text-sm">{t("relays.asyncJob.description")}</p>
				</SheetHeader>
				<Form {...form}>
					<form onSubmit={form.handleSubmit(save)} className="flex grow flex-col gap-6 pt-4">
						<div className="grow space-y-6 px-4 md:px-8">
							<Section title={t("relays.asyncJob.startTitle")} hint={t("relays.asyncJob.startHint")} testId="relay-async-job-start">
								<div className="flex w-full flex-row items-start gap-4">
									<FormField
										control={form.control}
										name="method"
										render={({ field }) => (
											<FormItem className="w-32">
												<FormLabel>{t("relays.fields.method")}</FormLabel>
												<Select onValueChange={field.onChange} value={field.value} disabled={disabled}>
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
													<Input placeholder="/jobs" disabled={disabled} data-testid="relay-async-job-path" {...field} />
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
												disabled={disabled}
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
														disabled={disabled}
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
													<Input placeholder="JobId" disabled={disabled} data-testid="relay-job-id-path" {...field} />
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
								</div>
							</Section>

							<Section
								title={t("relays.asyncJob.statusTitle")}
								hint={t("relays.asyncJob.statusHint", { token: JOB_ID_PLACEHOLDER })}
								testId="relay-async-job-status"
							>
								<JobStatusCheckFields control={form.control} disabled={disabled} />
								{plan && (
									<Alert
										variant={plan.kind === "blocked" ? "destructive" : "info"}
										data-testid={`relay-async-job-status-route-${plan.kind}`}
									>
										<AlertDescription>
											{plan.kind === "reuse" && t("relays.asyncJob.statusRouteReuse", { route: describeRoute(routes[plan.index]) })}
											{plan.kind === "add" && t("relays.asyncJob.statusRouteAdd", { route: describeRoute(plan.route) })}
											{plan.kind === "blocked" && t("relays.asyncJob.statusRouteBlocked", { route: describeRoute(plan.route) })}
										</AlertDescription>
									</Alert>
								)}
							</Section>

							<Section title={t("relays.job.pricingTitle")} hint={t("relays.job.pricingInfo")} testId="relay-async-job-pricing">
								<UsageFields
									control={form.control as unknown as Control<FieldValues>}
									name="job.usage"
									value={values.job?.usage}
									inheritFrom={jobInherit}
									ignoreUpstreamCost={ignoreUpstreamCost}
									disabled={disabled}
									testIdPrefix="relay-job-usage"
								/>
								<p className="bg-muted rounded-sm p-3 text-sm" data-testid="relay-async-job-summary">
									{t("relays.asyncJob.summary", { statuses: statuses || "…" })}
								</p>
							</Section>
						</div>

						<div className="bg-card sticky bottom-0 z-10 flex justify-end gap-2 border-t px-4 py-4 md:px-8">
							<Button type="button" variant="outline" onClick={onCancel}>
								{t("cancel", { ns: "common" })}
							</Button>
							<Button type="submit" disabled={disabled || plan?.kind === "blocked"} isLoading={saving} data-testid="relay-async-job-save">
								{editing ? t("relays.asyncJob.saveEdit") : t("relays.asyncJob.saveAdd")}
							</Button>
						</div>
					</form>
				</Form>
			</SheetContent>
		</Sheet>
	);
}