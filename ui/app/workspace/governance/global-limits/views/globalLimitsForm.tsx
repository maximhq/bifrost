"use client";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import MultiBudgetLines, { BudgetLineEntry } from "@/components/ui/multibudgets";
import NumberAndSelect from "@/components/ui/numberAndSelect";
import { DottedSeparator } from "@/components/ui/separator";
import { resetDurationOptions } from "@/lib/constants/governance";
import {
	getErrorMessage,
	useDeleteGlobalGovernanceMutation,
	useGetGlobalGovernanceQuery,
	useUpsertGlobalGovernanceMutation,
} from "@/lib/store";
import { formatCurrency } from "@/lib/utils/governance";
import { formatDistanceToNow } from "date-fns";
import isEqual from "lodash.isequal";
import { Globe } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import { z } from "zod";

const POLLING_INTERVAL = 5000;

const budgetLineSchema = z.object({
	max_limit: z.number().positive("Budget max limit must be greater than 0").optional(),
	reset_duration: z.string().min(1, "Reset duration is required"),
});

const formSchema = z.object({
	budgets: z.array(budgetLineSchema).superRefine((budgets, ctx) => {
		const seen = new Set<string>();
		budgets.forEach((b, i) => {
			if (seen.has(b.reset_duration)) {
				ctx.addIssue({
					code: "custom",
					message: `Duplicate budget window "${b.reset_duration}" — each reset duration can only appear once`,
					path: [i, "reset_duration"],
				});
			}
			seen.add(b.reset_duration);
		});
	}),
	tokenMaxLimit: z.number().int().positive("Token max limit must be greater than 0").optional(),
	tokenResetDuration: z.string(),
	requestMaxLimit: z.number().int().positive("Request max limit must be greater than 0").optional(),
	requestResetDuration: z.string(),
});

interface FormState {
	budgets: BudgetLineEntry[];
	tokenMaxLimit: number | undefined;
	tokenResetDuration: string;
	requestMaxLimit: number | undefined;
	requestResetDuration: string;
}

function emptyForm(): FormState {
	return {
		budgets: [],
		tokenMaxLimit: undefined,
		tokenResetDuration: "1h",
		requestMaxLimit: undefined,
		requestResetDuration: "1h",
	};
}

function formFromData(data: { budgets?: any[]; rate_limit?: any } | undefined): FormState {
	if (!data) return emptyForm();
	return {
		budgets: (data.budgets ?? []).map((b: any) => ({
			max_limit: b.max_limit,
			reset_duration: b.reset_duration,
		})),
		tokenMaxLimit: data.rate_limit?.token_max_limit ?? undefined,
		tokenResetDuration: data.rate_limit?.token_reset_duration ?? "1h",
		requestMaxLimit: data.rate_limit?.request_max_limit ?? undefined,
		requestResetDuration: data.rate_limit?.request_reset_duration ?? "1h",
	};
}

export default function GlobalLimitsForm() {
	const { data, isLoading, error } = useGetGlobalGovernanceQuery(undefined, {
		pollingInterval: POLLING_INTERVAL,
	});

	const [upsert, { isLoading: isSaving }] = useUpsertGlobalGovernanceMutation();
	const [deleteAll, { isLoading: isDeleting }] = useDeleteGlobalGovernanceMutation();

	const [savedForm, setSavedForm] = useState<FormState>(emptyForm());
	const [form, setForm] = useState<FormState>(emptyForm());

	const formRef = useRef(form);
	const savedFormRef = useRef(savedForm);
	useEffect(() => {
		formRef.current = form;
		savedFormRef.current = savedForm;
	}, [form, savedForm]);

	useEffect(() => {
		const next = formFromData(data);
		setSavedForm(next);
		if (isEqual(formRef.current, savedFormRef.current)) {
			setForm(next);
		}
	}, [data]);

	const isDirty = useMemo(() => !isEqual(form, savedForm), [form, savedForm]);

	const validation = useMemo(() => formSchema.safeParse(form), [form]);
	const hasValidationErrors = !validation.success;

	function updateField<K extends keyof FormState>(key: K, value: FormState[K]) {
		setForm((prev) => ({ ...prev, [key]: value }));
	}

	async function handleSave() {
		const parsed = formSchema.safeParse(form);
		if (!parsed.success) {
			toast.error(parsed.error.issues[0]?.message ?? "Invalid global limits configuration");
			return;
		}
		const { budgets, tokenMaxLimit, tokenResetDuration, requestMaxLimit, requestResetDuration } = parsed.data;
		try {
			const validBudgets = budgets.filter((b): b is { max_limit: number; reset_duration: string } => b.max_limit !== undefined);
			const hasRateLimit = tokenMaxLimit !== undefined || requestMaxLimit !== undefined;

			await upsert({
				budgets: validBudgets.map((b) => ({
					max_limit: b.max_limit,
					reset_duration: b.reset_duration,
				})),
				rate_limit: hasRateLimit
					? {
							token_max_limit: tokenMaxLimit,
							token_reset_duration: tokenMaxLimit !== undefined ? tokenResetDuration : undefined,
							request_max_limit: requestMaxLimit,
							request_reset_duration: requestMaxLimit !== undefined ? requestResetDuration : undefined,
						}
					: null,
			}).unwrap();

			toast.success("Global limits saved.");
		} catch (err) {
			toast.error(getErrorMessage(err));
		}
	}

	async function handleClear() {
		try {
			await deleteAll().unwrap();
			toast.success("Global limits cleared.");
		} catch (err) {
			toast.error(getErrorMessage(err));
		}
	}

	if (isLoading) {
		return (
			<div className="flex h-64 items-center justify-center">
				<p className="text-muted-foreground text-sm">Loading global limits…</p>
			</div>
		);
	}

	if (error) {
		return (
			<div className="flex h-64 items-center justify-center">
				<p className="text-destructive text-sm">{getErrorMessage(error)}</p>
			</div>
		);
	}

	const hasExisting = (data?.budgets?.length ?? 0) > 0 || data?.rate_limit != null;

	return (
		<div className="space-y-6">
			{/* Header */}
			<div className="flex items-start justify-between">
				<div className="flex items-center gap-3">
					<Globe className="text-muted-foreground h-6 w-6" />
					<div>
						<h1 className="text-xl font-semibold">Global Limits</h1>
						<p className="text-muted-foreground text-sm">
							Instance-wide budget and rate limits enforced before provider, model, VK, team, and customer checks.
						</p>
					</div>
				</div>
			</div>

			<Card>
				<CardHeader className="pb-3">
					<CardTitle className="text-sm font-medium">Configuration</CardTitle>
					<CardDescription>Set multi-window budget limits and token/request rate limits. Changes take effect immediately.</CardDescription>
				</CardHeader>
				<CardContent className="space-y-6">
					{/* Budget limits */}
					<div className="space-y-4">
						<Label className="text-sm font-medium">Budget Limits</Label>
						<MultiBudgetLines
							id="global-budgets"
							data-testid="global-budgets"
							lines={form.budgets}
							onChange={(lines) => updateField("budgets", lines)}
						/>
					</div>

					<DottedSeparator />

					{/* Rate limits */}
					<div className="space-y-4">
						<Label className="text-sm font-medium">Rate Limiting</Label>
						<NumberAndSelect
							id="global-token-max-limit"
							dataTestId="global-token-max-limit-input"
							label="Maximum Tokens"
							value={form.tokenMaxLimit}
							selectValue={form.tokenResetDuration}
							onChangeNumber={(v) => updateField("tokenMaxLimit", v)}
							onChangeSelect={(v) => updateField("tokenResetDuration", v)}
							options={resetDurationOptions}
						/>
						<NumberAndSelect
							id="global-request-max-limit"
							dataTestId="global-request-max-limit-input"
							label="Maximum Requests"
							value={form.requestMaxLimit}
							selectValue={form.requestResetDuration}
							onChangeNumber={(v) => updateField("requestMaxLimit", v)}
							onChangeSelect={(v) => updateField("requestResetDuration", v)}
							options={resetDurationOptions}
						/>
					</div>

					{/* Current usage */}
					{hasExisting && (
						<>
							<DottedSeparator />
							<div className="space-y-4">
								<Label className="text-sm font-medium">Current Usage</Label>
								<div className="bg-muted/50 grid grid-cols-2 gap-4 rounded-lg p-4">
									{(data?.budgets ?? []).map((b) => (
										<div key={b.id} className="space-y-1">
											<p className="text-muted-foreground text-xs">Budget ({b.reset_duration})</p>
											<p className="text-sm font-medium">
												{formatCurrency(b.current_usage)} / {formatCurrency(b.max_limit)}
											</p>
											<p className="text-muted-foreground text-xs">
												Last Reset: {formatDistanceToNow(new Date(b.last_reset), { addSuffix: true })}
											</p>
										</div>
									))}
									{data?.rate_limit?.token_max_limit != null && (
										<div className="space-y-1">
											<p className="text-muted-foreground text-xs">Token Usage</p>
											<p className="text-sm font-medium">
												{(data.rate_limit.token_current_usage ?? 0).toLocaleString()} / {data.rate_limit.token_max_limit.toLocaleString()}
											</p>
											<p className="text-muted-foreground text-xs">
												Last Reset: {formatDistanceToNow(new Date(data.rate_limit.token_last_reset), { addSuffix: true })}
											</p>
										</div>
									)}
									{data?.rate_limit?.request_max_limit != null && (
										<div className="space-y-1">
											<p className="text-muted-foreground text-xs">Request Usage</p>
											<p className="text-sm font-medium">
												{(data.rate_limit.request_current_usage ?? 0).toLocaleString()} /{" "}
												{data.rate_limit.request_max_limit.toLocaleString()}
											</p>
											<p className="text-muted-foreground text-xs">
												Last Reset: {formatDistanceToNow(new Date(data.rate_limit.request_last_reset), { addSuffix: true })}
											</p>
										</div>
									)}
								</div>
							</div>
						</>
					)}

					{/* Actions */}
					<div className="flex justify-end gap-2">
						<Button
							data-testid="global-limits-remove-btn"
							variant="outline"
							onClick={handleClear}
							disabled={isDeleting || !hasExisting}
							className="text-destructive"
						>
							Remove configuration
						</Button>
						<Button data-testid="global-limits-save-btn" onClick={handleSave} disabled={isSaving || !isDirty || hasValidationErrors}>
							{isSaving ? "Saving…" : "Save Global Limits"}
						</Button>
					</div>
				</CardContent>
			</Card>
		</div>
	);
}