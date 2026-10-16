// Reusable form for the MCP per-user-headers flow.
//
// Two callers exercise this:
//   - Admin-test panel (MCP client form): admin enters sample values for the
//     PerUserHeaderKeys schema they just declared, hits "Test", and the
//     backend verifies + discovers tools. Values are NOT persisted from
//     this path.
//   - User submission page (auth landing): authenticated caller submits
//     their own values for an existing MCP client; backend verifies +
//     upserts the credential row.
//
// Both share the same input shape: one labelled secret input per
// requiredKey, an optional read-only display of admin-static header names
// (so the user knows what static context will accompany their request),
// optional prefill of previously-submitted key NAMES (never values), and
// Test/Submit handlers.

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { AUTH_SCHEMES, detectAuthScheme, stripAuthScheme } from "@/lib/utils/authScheme";
import { Eye, EyeOff, Info, Loader2 } from "lucide-react";
import React, { useEffect, useMemo, useState } from "react";

export interface HeadersFormProps {
	// Required keys — admin-declared, immutable in this form. Labels and
	// input order follow array order.
	requiredKeys: string[];
	// Optional read-only context: admin-static header *names* (no values)
	// that always accompany the caller's request. Surfaced so the user
	// knows what shape the upstream will see; not editable here.
	adminHeaderKeys?: string[];
	// Names (not values) of keys the user has already submitted before.
	// Used to render a "previously submitted" badge so the user knows which
	// fields they're editing vs filling in fresh.
	previouslySubmittedKeys?: string[];
	// Optional "Test" handler. When provided, a Test button is rendered
	// alongside Submit. Used by the admin-test panel.
	onTest?: (values: Record<string, string>) => Promise<void> | void;
	// Submit handler. Required.
	onSubmit: (values: Record<string, string>) => Promise<void> | void;
	// Disable all inputs + buttons (e.g. during network calls).
	busy?: boolean;
	// Label override for the Submit button (e.g. "Save" vs "Submit").
	submitLabel?: string;
	// Label override for the Test button.
	testLabel?: string;
	// Optional starting values for each key. Used by neither caller right
	// now (we never round-trip secrets to the client), but kept for future
	// reveal flows.
	initialValues?: Record<string, string>;
	// Optional "data-testid" prefix so tests can target individual inputs.
	testIdPrefix?: string;
	// When true, the Submit button is not rendered. Used by the admin-test
	// panel inside the MCP client form — commit happens via the parent
	// form's main button, so a per-form submit here would be misleading.
	hideSubmit?: boolean;
	// Optional Cancel handler. When provided, a Cancel button is rendered
	// in the same row as Test/Submit so the dialog's two buttons share one
	// flex row instead of stacking.
	onCancel?: () => void;
	cancelLabel?: string;
}

export default function HeadersForm({
	requiredKeys,
	adminHeaderKeys,
	previouslySubmittedKeys,
	onTest,
	onSubmit,
	busy = false,
	submitLabel = "Submit",
	testLabel = "Test",
	initialValues,
	testIdPrefix = "headers-form",
	hideSubmit = false,
	onCancel,
	cancelLabel = "Cancel",
}: HeadersFormProps) {
	const [values, setValues] = useState<Record<string, string>>(() => buildInitialValues(requiredKeys, initialValues));
	const [reveal, setReveal] = useState<Record<string, boolean>>({});

	// If the schema changes mid-form (e.g. admin adds a key), reset state
	// so we don't leak stale entries for removed keys.
	useEffect(() => {
		setValues((prev) => syncValuesToSchema(prev, requiredKeys, initialValues));
	}, [requiredKeys, initialValues]);

	const previouslySubmittedSet = useMemo(() => new Set(previouslySubmittedKeys ?? []), [previouslySubmittedKeys]);

	const canSubmit = useMemo(
		() => requiredKeys.every((k) => previouslySubmittedSet.has(k) || !isEffectivelyBlank(k, values[k] ?? "")),
		[previouslySubmittedSet, requiredKeys, values],
	);

	const handleChange = (key: string, value: string) => {
		setValues((prev) => ({ ...prev, [key]: value }));
	};

	// canSubmit is computed via Array.every() which returns true for an
	// empty array — so an empty schema (requiredKeys.length === 0) would
	// satisfy the "all filled" check. Both handlers must explicitly guard
	// against the empty-schema case; the Submit button already does via
	// disabled state but Test does not, and an Enter-key submit would
	// otherwise fire onSubmit with no values.
	const handleSubmit = async (e: React.FormEvent) => {
		e.preventDefault();
		if (requiredKeys.length === 0 || !canSubmit || busy) return;
		await onSubmit(buildSubmissionValues(requiredKeys, values, previouslySubmittedSet));
	};

	const handleTest = async () => {
		if (!onTest || requiredKeys.length === 0 || !canSubmit || busy) return;
		await onTest(buildSubmissionValues(requiredKeys, values, previouslySubmittedSet));
	};

	return (
		<form onSubmit={handleSubmit} className="space-y-4">
			{adminHeaderKeys && adminHeaderKeys.length > 0 && (
				<div className="border-muted-foreground/20 bg-muted/40 rounded-md border p-3">
					<div className="flex items-center gap-1.5">
						<p className="text-muted-foreground text-xs font-medium">Static admin headers</p>
						<TooltipProvider>
							<Tooltip>
								<TooltipTrigger asChild>
									<Info className="text-muted-foreground size-3" />
								</TooltipTrigger>
								<TooltipContent className="max-w-xs">
									<p>
										These headers are set by the admin on the MCP client and accompany every request alongside your submitted values. You
										can&apos;t edit them here.
									</p>
								</TooltipContent>
							</Tooltip>
						</TooltipProvider>
					</div>
					<div className="mt-2 flex flex-wrap gap-1.5">
						{adminHeaderKeys.map((name) => (
							<code key={name} className="bg-background rounded px-1.5 py-0.5 font-mono text-xs">
								{name}
							</code>
						))}
					</div>
				</div>
			)}

			<div className="space-y-3">
				{requiredKeys.length === 0 ? (
					<p className="text-muted-foreground text-sm">No header keys have been declared on this MCP client.</p>
				) : (
					requiredKeys.map((key) => {
						const isRevealed = reveal[key] === true;
						const wasSubmitted = previouslySubmittedSet.has(key);
						// "Authorization" gets a Bearer/Basic prefix helper so callers
						// can paste a bare token instead of typing the scheme by hand.
						const showAuthScheme = key.trim().toLowerCase() === "authorization";
						const authScheme = showAuthScheme ? detectAuthScheme(values[key] ?? "") : "none";
						const displayValue = showAuthScheme && authScheme !== "none" ? stripAuthScheme(values[key] ?? "") : (values[key] ?? "");
						const handleTokenChange = (newToken: string) => {
							handleChange(key, showAuthScheme && authScheme !== "none" ? `${authScheme} ${newToken}` : newToken);
						};
						const handleSchemeChange = (newScheme: string) => {
							const stripped = stripAuthScheme(values[key] ?? "");
							handleChange(key, newScheme === "none" ? stripped : `${newScheme} ${stripped}`);
						};
						return (
							<div key={key} className="space-y-1.5">
								<div className="flex items-center justify-between gap-2">
									<Label htmlFor={`${testIdPrefix}-${key}`} className="font-mono text-xs">
										{key}
									</Label>
									{wasSubmitted && <span className="text-muted-foreground text-xs">Previously submitted</span>}
								</div>
								<div className="flex items-center gap-1.5">
									{showAuthScheme && (
										<Select value={authScheme} onValueChange={handleSchemeChange} disabled={busy}>
											<SelectTrigger size="sm" className="w-[92px] shrink-0 text-xs" data-testid={`${testIdPrefix}-scheme-${key}`}>
												<SelectValue />
											</SelectTrigger>
											<SelectContent>
												<SelectItem value="none">Raw</SelectItem>
												{AUTH_SCHEMES.map((scheme) => (
													<SelectItem key={scheme} value={scheme}>
														{scheme}
													</SelectItem>
												))}
											</SelectContent>
										</Select>
									)}
									<div className="relative flex-1">
										<Input
											id={`${testIdPrefix}-${key}`}
											type={isRevealed ? "text" : "password"}
											autoComplete="off"
											value={displayValue}
											onChange={(e) => handleTokenChange(e.target.value)}
											placeholder={wasSubmitted ? "•••••• (enter new value to overwrite)" : `Value for ${key}`}
											disabled={busy}
											data-testid={`${testIdPrefix}-input-${key}`}
										/>
										<button
											type="button"
											onClick={() => setReveal((r) => ({ ...r, [key]: !r[key] }))}
											className="text-muted-foreground hover:text-foreground absolute top-1/2 right-2 -translate-y-1/2"
											aria-label={isRevealed ? "Hide value" : "Show value"}
											data-testid={`${testIdPrefix}-reveal-${key}`}
										>
											{isRevealed ? <EyeOff className="size-3.5" /> : <Eye className="size-3.5" />}
										</button>
									</div>
								</div>
							</div>
						);
					})
				)}
			</div>

			<div className="flex items-center justify-end gap-2 pt-2">
				{onCancel && (
					<Button type="button" variant="outline" onClick={onCancel} disabled={busy} data-testid={`${testIdPrefix}-cancel-btn`}>
						{cancelLabel}
					</Button>
				)}
				{onTest && (
					<Button
						type="button"
						variant="outline"
						onClick={handleTest}
						disabled={!canSubmit || busy || requiredKeys.length === 0}
						data-testid={`${testIdPrefix}-test-btn`}
					>
						{busy ? <Loader2 className="size-3.5 animate-spin" /> : null}
						{testLabel}
					</Button>
				)}
				{!hideSubmit && (
					<Button type="submit" disabled={!canSubmit || busy || requiredKeys.length === 0} data-testid={`${testIdPrefix}-submit-btn`}>
						{busy ? <Loader2 className="size-3.5 animate-spin" /> : null}
						{submitLabel}
					</Button>
				)}
			</div>
		</form>
	);
}

// Treats a scheme-only Authorization value (e.g. "Bearer " with no token) as
// blank, so the scheme can be picked before typing a token without letting it
// count as "filled" and overwrite a stored credential.
function isEffectivelyBlank(key: string, value: string): boolean {
	const relevant = key.trim().toLowerCase() === "authorization" ? stripAuthScheme(value) : value;
	return relevant.trim() === "";
}

function buildSubmissionValues(
	keys: string[],
	values: Record<string, string>,
	previouslySubmittedSet: Set<string>,
): Record<string, string> {
	const out: Record<string, string> = {};
	for (const k of keys) {
		const value = values[k] ?? "";
		if (!isEffectivelyBlank(k, value) || !previouslySubmittedSet.has(k)) {
			out[k] = value;
		}
	}
	return out;
}

function buildInitialValues(keys: string[], initial?: Record<string, string>): Record<string, string> {
	const out: Record<string, string> = {};
	for (const k of keys) {
		out[k] = initial?.[k] ?? "";
	}
	return out;
}

// syncValuesToSchema preserves user-entered values for keys that still
// exist in the new schema, drops values for removed keys, and seeds blank
// entries for newly added keys.
function syncValuesToSchema(prev: Record<string, string>, keys: string[], initial?: Record<string, string>): Record<string, string> {
	const out: Record<string, string> = {};
	for (const k of keys) {
		if (k in prev) {
			out[k] = prev[k];
		} else {
			out[k] = initial?.[k] ?? "";
		}
	}
	return out;
}