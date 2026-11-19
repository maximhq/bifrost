import { Button } from "@/components/ui/button";
import { FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { RelayRouteMethods } from "@/lib/types/relays";
import type { RelayAsyncJobForm } from "@/lib/types/schemas";
import { JOB_ID_PLACEHOLDER } from "@/lib/utils/relayAsyncJob";
import { Plus, Trash2 } from "lucide-react";
import { useEffect, useState, type ComponentProps } from "react";
import type { Control } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { InfoLabel } from "./infoLabel";

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
	...inputProps
}: Omit<ComponentProps<typeof Input>, "value" | "onChange"> & {
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
			{...inputProps}
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

/** Status check headers, edited as name/value rows and stored as a map. */
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
						data-testid={`relay-job-poll-header-name-${i}`}
						onChange={(e) => commit(rows.map((r, j) => (j === i ? { ...r, name: e.target.value } : r)))}
					/>
					<Input
						className="flex-1"
						placeholder="Textract.GetDocumentAnalysis"
						disabled={disabled}
						value={row.value}
						data-testid={`relay-job-poll-header-value-${i}`}
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
				data-testid="relay-job-poll-add-header"
			>
				<Plus className="h-4 w-4" /> {t("relays.job.addPollHeader")}
			</Button>
		</div>
	);
}

/**
 * How Bifrost checks an async job until it finishes: the status check request, where its reply
 * carries the status, which statuses end the job, and how often and how long it checks.
 */
// Cleared fields stay explicit ("", [], false, null) so the control shows them cleared; react-hook-form
// would show the opened value again for undefined. cleanJob drops them on save.
export function JobStatusCheckFields({ control, disabled }: { control: Control<RelayAsyncJobForm>; disabled?: boolean }) {
	const { t } = useTranslation("config");

	return (
		<div className="space-y-4">
			<div className="space-y-2">
				<InfoLabel inForm={false} label={t("relays.job.pollLabel")} info={t("relays.job.pollInfo", { token: JOB_ID_PLACEHOLDER })} />
				<div className="flex w-full flex-row items-start gap-4">
					<FormField
						control={control}
						name="job.poll.method"
						render={({ field }) => (
							<FormItem className="w-32">
								<FormLabel>{t("relays.fields.method")}</FormLabel>
								<Select onValueChange={field.onChange} value={field.value || "POST"} disabled={disabled}>
									<FormControl>
										<SelectTrigger className="w-full" data-testid="relay-job-poll-method">
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
								<FormLabel>{t("relays.routeSheet.pathLabel")}</FormLabel>
								<FormControl>
									<Input placeholder="/jobs/{{job.id}}" disabled={disabled} data-testid="relay-job-poll-path" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>
				<FormField
					control={control}
					name="job.poll.headers"
					render={({ field }) => (
						<FormItem>
							<PollHeaders value={field.value} onChange={field.onChange} disabled={disabled} />
							<FormMessage data-testid="relay-job-poll-headers-error" />
						</FormItem>
					)}
				/>
				<FormField
					control={control}
					name="job.poll.body"
					render={({ field }) => (
						<FormItem>
							<FormLabel>{t("relays.job.pollBodyLabel")}</FormLabel>
							<FormControl>
								<Textarea
									rows={2}
									className="font-mono text-xs"
									placeholder={t("relays.job.pollBodyPlaceholder", { example: '{"JobId":"{{job.id}}"}' })}
									disabled={disabled}
									data-testid="relay-job-poll-body"
									value={field.value ?? ""}
									onChange={(e) => field.onChange(e.target.value)}
								/>
							</FormControl>
						</FormItem>
					)}
				/>
				<FormField
					control={control}
					name="job.poll.content_type"
					render={({ field }) => (
						<FormItem>
							<InfoLabel label={t("relays.job.contentTypeLabel")} info={t("relays.job.contentTypeInfo")} />
							<FormControl>
								<Input
									placeholder="application/x-amz-json-1.1"
									disabled={disabled}
									data-testid="relay-job-poll-content-type"
									value={field.value ?? ""}
									onChange={(e) => field.onChange(e.target.value)}
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
							<Input placeholder="JobStatus" disabled={disabled} data-testid="relay-job-status-path" {...field} />
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
									testId="relay-job-succeeded"
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
									onChange={field.onChange}
									placeholder="FAILED" // i18n-ignore: sample status
									disabled={disabled}
									testId="relay-job-failed"
								/>
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
			</div>
			<div className="flex w-full flex-row items-start gap-4">
				{(
					[
						["interval_seconds", t("relays.job.intervalLabel"), "30", t("relays.job.intervalInfo")],
						["timeout_seconds", t("relays.job.timeoutLabel"), "3600", t("relays.job.timeoutInfo")],
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
										data-testid={`relay-job-${key.replaceAll("_", "-")}`}
										value={numberValue(field.value)}
										onChange={(e) => field.onChange(e.target.value === "" ? null : Number(e.target.value))}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				))}
			</div>
			<FormField
				control={control}
				name="job.reused_id_is_new_job"
				render={({ field }) => (
					<FormItem className="flex flex-row items-center justify-between">
						<InfoLabel label={t("relays.job.reusedIdLabel")} info={t("relays.job.reusedIdInfo")} />
						<FormControl>
							<Switch
								checked={field.value ?? false}
								onCheckedChange={field.onChange}
								disabled={disabled}
								data-testid="relay-job-reused-id"
							/>
						</FormControl>
					</FormItem>
				)}
			/>
		</div>
	);
}