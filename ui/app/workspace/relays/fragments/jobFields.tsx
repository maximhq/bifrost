import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { FormControl, FormField, FormItem, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { RelayRouteMethods, type RelayJobConfig, type RelayUsageConfig } from "@/lib/types/relays";
import { cn } from "@/lib/utils";
import { ChevronDown, Plus, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import type { Control, FieldValues, UseFormReturn } from "react-hook-form";
import type { RouteForm } from "../routes.utils";
import { InfoLabel } from "./infoLabel";
import { UsageFields } from "./usageFields";
import { useTranslation } from "react-i18next";

/** The job a route starts when the async job switch is turned on. */
export const NEW_RELAY_JOB: RelayJobConfig = {
	id_path: "",
	poll: { method: "GET", path: "" },
	status_path: "",
	succeeded: [],
	interval_seconds: 30,
	timeout_seconds: 3600,
};

const toList = (raw: string) =>
	raw
		.split(",")
		.map((s) => s.trim())
		.filter(Boolean);

function numberValue(v: unknown): string | number {
	return v === undefined || v === null || (typeof v === "number" && Number.isNaN(v)) ? "" : (v as number);
}

/**
 * A comma-separated list input that keeps the text as typed (so "SUCCEEDED, " can be finished) and
 * hands the form the parsed list.
 */
function ListInput({
	value,
	onChange,
	placeholder,
	disabled,
	testId,
}: {
	value: string[] | undefined;
	onChange: (v: string[]) => void;
	placeholder: string;
	disabled?: boolean;
	testId: string;
}) {
	const [text, setText] = useState((value ?? []).join(", "));
	useEffect(() => {
		if (toList(text).join(",") !== (value ?? []).join(",")) setText((value ?? []).join(", "));
		// Only an outside change (a reset) rewrites the text.
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [value]);
	return (
		<Input
			placeholder={placeholder}
			disabled={disabled}
			data-testid={testId}
			value={text}
			onChange={(e) => {
				setText(e.target.value);
				onChange(toList(e.target.value));
			}}
		/>
	);
}

/** Poll request headers, edited as name/value rows and stored as a map. */
function PollHeaders({
	value,
	onChange,
	disabled,
}: {
	value: Record<string, string> | undefined;
	onChange: (v: Record<string, string> | undefined) => void;
	disabled?: boolean;
}) {
	const { t } = useTranslation("config");

	const [rows, setRows] = useState(() => Object.entries(value ?? {}).map(([name, v]) => ({ name, value: v })));
	const commit = (next: { name: string; value: string }[]) => {
		setRows(next);
		const map = Object.fromEntries(next.filter((r) => r.name.trim()).map((r) => [r.name.trim(), r.value]));
		onChange(Object.keys(map).length ? map : undefined);
	};
	return (
		<div className="space-y-2">
			{rows.map((row, i) => (
				<div key={i} className="flex items-start gap-2">
					<Input
						className="flex-1"
						placeholder="X-Amz-Target"
						disabled={disabled}
						value={row.name}
						data-testid={`relay-route-job-poll-header-name-${i}`}
						onChange={(e) => commit(rows.map((r, j) => (j === i ? { ...r, name: e.target.value } : r)))}
					/>
					<Input
						className="flex-1"
						placeholder="Textract.GetDocumentAnalysis"
						disabled={disabled}
						value={row.value}
						data-testid={`relay-route-job-poll-header-value-${i}`}
						onChange={(e) => commit(rows.map((r, j) => (j === i ? { ...r, value: e.target.value } : r)))}
					/>
					<Button
						type="button"
						variant="ghost"
						size="icon"
						aria-label={t("relays.job.removePollHeader")}
						disabled={disabled}
						onClick={() => commit(rows.filter((_, j) => j !== i))}
					>
						<Trash2 className="h-4 w-4" />
					</Button>
				</div>
			))}
			<Button
				type="button"
				variant="outline"
				size="sm"
				disabled={disabled}
				onClick={() => setRows([...rows, { name: "", value: "" }])}
				data-testid="relay-route-job-poll-add-header"
			>
				<Plus className="h-4 w-4" /> {t("relays.job.addPollHeader")}
			</Button>
		</div>
	);
}

interface JobFieldsProps {
	form: UseFormReturn<RouteForm>;
	/** What a job's pricing inherits: the relay pricing with the route override applied. */
	inheritFrom: RelayUsageConfig | undefined;
	disabled?: boolean;
}

/**
 * The async job a route can start, for upstreams that answer a request with a job id and bill for
 * the work once it finishes (Textract StartDocumentAnalysis, batch transcription). The start call is
 * billed as usual; Bifrost then polls the upstream with the job id until it reports a terminal status
 * and charges the finished job to the caller's budgets once.
 */
export function JobFields({ form, inheritFrom, disabled }: JobFieldsProps) {
	const { t } = useTranslation("config");

	const job = form.watch("job");
	const [open, setOpen] = useState(false);
	// A refused save opens the section so its field errors are visible.
	const hasErrors = !!form.formState.errors.job;
	useEffect(() => {
		if (hasErrors) setOpen(true);
	}, [hasErrors]);
	const control = form.control;
	const summary = job
		? t("relays.job.summary", {
				method: job.poll.method || "GET",
				path: job.poll.path || "…",
				statusPath: job.status_path || "…",
				statuses: job.succeeded.join(` ${t("or", { ns: "common" })} `) || "…",
			})
		: t("relays.job.summaryNone");
	// The settlement charges only a fee the job sets itself: the start call already paid the route fee.
	const jobInherit = inheritFrom ? { ...inheritFrom, cost_per_request: undefined } : undefined;

	return (
		<Collapsible open={open} onOpenChange={setOpen} className="rounded-sm border" data-testid="relay-route-job">
			<CollapsibleTrigger asChild>
				<button type="button" className="flex w-full items-center justify-between px-4 py-3 text-left" data-testid="relay-route-job-toggle">
					<div className="min-w-0 space-y-0.5">
						<div className="text-sm font-medium">{t("relays.job.title")}</div>
						<p className="text-muted-foreground truncate text-sm">{summary}</p>
					</div>
					<ChevronDown className={cn("h-4 w-4 shrink-0 transition-transform", open && "rotate-180")} />
				</button>
			</CollapsibleTrigger>
			<CollapsibleContent className="space-y-4 border-t px-4 py-4">
				<div className="flex flex-row items-center justify-between">
					<InfoLabel
						inForm={false}
						label={t("relays.job.enableLabel")}
						info={t("relays.job.enableInfo")}
					/>
					<Switch
						checked={!!job}
						disabled={disabled}
						data-testid="relay-route-job-enabled"
						onCheckedChange={(on) => form.setValue("job", on ? structuredClone(NEW_RELAY_JOB) : undefined, { shouldDirty: true })}
					/>
				</div>
				{job && (
					<>
						<ol className="text-muted-foreground list-decimal space-y-1 pl-5 text-sm">
							<li>{t("relays.job.step1")}</li>
							<li>{t("relays.job.step2")}</li>
							<li>{t("relays.job.step3", { token: "{{job.id}}" })}</li>
							<li>{t("relays.job.step4")}</li>
						</ol>
						<FormField
							control={control}
							name="job.id_path"
							render={({ field }) => (
								<FormItem>
									<InfoLabel label={t("relays.job.idPathLabel")} info={t("relays.job.idPathInfo")} />
									<FormControl>
										{/* i18n-ignore: sample path */}
										<Input placeholder="JobId" disabled={disabled} data-testid="relay-route-job-id-path" {...field} />
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
						<div className="space-y-2">
							<InfoLabel
								inForm={false}
								label={t("relays.job.pollLabel")}
								info={t("relays.job.pollInfo", { token: "{{job.id}}" })}
							/>
							<div className="flex w-full flex-row items-start gap-4">
								<FormField
									control={control}
									name="job.poll.method"
									render={({ field }) => (
										<FormItem className="w-32">
											<Select onValueChange={field.onChange} value={field.value || "GET"} disabled={disabled}>
												<FormControl>
													<SelectTrigger className="w-full" data-testid="relay-route-job-poll-method">
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
									control={control}
									name="job.poll.path"
									render={({ field }) => (
										<FormItem className="flex-1">
											<FormControl>
												<Input placeholder="/jobs/{{job.id}}" disabled={disabled} data-testid="relay-route-job-poll-path" {...field} />
											</FormControl>
											<FormMessage />
										</FormItem>
									)}
								/>
							</div>
							<FormField
								control={control}
								name="job.poll.headers"
								render={({ field }) => <PollHeaders value={field.value} onChange={field.onChange} disabled={disabled} />}
							/>
							<FormField
								control={control}
								name="job.poll.body"
								render={({ field }) => (
									<FormItem>
										<FormControl>
											<Textarea
												rows={2}
												className="font-mono text-xs"
												placeholder={t("relays.job.pollBodyPlaceholder", { example: '{"JobId":"{{job.id}}"}' })}
												disabled={disabled}
												data-testid="relay-route-job-poll-body"
												value={field.value ?? ""}
												onChange={(e) => field.onChange(e.target.value || undefined)}
											/>
										</FormControl>
									</FormItem>
								)}
							/>
						</div>
						<FormField
							control={control}
							name="job.status_path"
							render={({ field }) => (
								<FormItem>
									<InfoLabel label={t("relays.job.statusPathLabel")} info={t("relays.job.statusPathInfo")} />
									<FormControl>
										{/* i18n-ignore: sample path */}
										<Input placeholder="JobStatus" disabled={disabled} data-testid="relay-route-job-status-path" {...field} />
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
						<div className="flex w-full flex-row items-start gap-4">
							<FormField
								control={control}
								name="job.succeeded"
								render={({ field }) => (
									<FormItem className="flex-1">
										<InfoLabel label={t("relays.job.succeededLabel")} info={t("relays.job.succeededInfo")} />
										<FormControl>
											<ListInput
												value={field.value}
												onChange={field.onChange}
												placeholder="SUCCEEDED" // i18n-ignore: sample status
												disabled={disabled}
												testId="relay-route-job-succeeded"
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name="job.failed"
								render={({ field }) => (
									<FormItem className="flex-1">
										<InfoLabel label={t("relays.job.failedLabel")} info={t("relays.job.failedInfo")} />
										<FormControl>
											<ListInput
												value={field.value}
												onChange={(v) => field.onChange(v.length ? v : undefined)}
												placeholder="FAILED" // i18n-ignore: sample status
												disabled={disabled}
												testId="relay-route-job-failed"
											/>
										</FormControl>
									</FormItem>
								)}
							/>
						</div>
						<div className="flex w-full flex-row items-start gap-4">
							{(
								[
									["interval_seconds", t("relays.job.intervalLabel"), "30", t("relays.job.intervalInfo")],
									[
										"timeout_seconds",
										t("relays.job.timeoutLabel"),
											"3600",
											t("relays.job.timeoutInfo"),
									],
								] as const
							).map(([key, label, placeholder, info]) => (
								<FormField
									key={key}
									control={control}
									name={`job.${key}`}
									render={({ field }) => (
										<FormItem className="flex-1">
											<InfoLabel label={label} info={info} />
											<FormControl>
												<Input
													type="number"
													min={1}
													placeholder={placeholder}
													disabled={disabled}
													data-testid={`relay-route-job-${key.replaceAll("_", "-")}`}
													value={numberValue(field.value)}
													onChange={(e) => field.onChange(e.target.value === "" ? undefined : Number(e.target.value))}
												/>
											</FormControl>
											<FormMessage />
										</FormItem>
									)}
								/>
							))}
						</div>
						<div className="space-y-1">
							<div className="text-sm font-medium">{t("relays.job.pricingTitle")}</div>
							<p className="text-muted-foreground text-sm">
								{t("relays.job.pricingInfo")}
							</p>
						</div>
						<UsageFields
							control={control as unknown as Control<FieldValues>}
							name="job.usage"
							value={job.usage}
							inheritFrom={jobInherit ?? {}}
							disabled={disabled}
							testIdPrefix="relay-route-job-usage"
						/>
					</>
				)}
			</CollapsibleContent>
		</Collapsible>
	);
}