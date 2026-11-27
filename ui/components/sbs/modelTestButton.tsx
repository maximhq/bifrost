// SBS fork: per-model "Test" button for the Model Catalog. Sends one bare user message straight
// to the provider (no MCP tools, no injected tools, no plugins) and shows the result together
// with the exact request body that went out.
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { SBSModelTestResult, useTestSBSModelMutation } from "@/lib/store";
import { CheckCircle2, FlaskConical, Loader2, RotateCw, XCircle } from "lucide-react";
import { useState } from "react";

interface ModelTestButtonProps {
	provider: string;
	model: string;
	testId?: string;
}

function summary(r: SBSModelTestResult) {
	if (!r.ok) return `${r.status_code ? `HTTP ${r.status_code}: ` : ""}${r.error ?? "failed"}`;
	if (r.mode === "embedding") return `${r.latency_ms} ms · ${r.dimensions ?? "?"} dimensions`;
	return `${r.latency_ms} ms${r.tokens ? ` · ${r.tokens} tokens` : ""}`;
}

function pretty(v: unknown) {
	if (v == null) return "";
	if (typeof v === "string") {
		try {
			return JSON.stringify(JSON.parse(v), null, 2);
		} catch {
			return v;
		}
	}
	return JSON.stringify(v, null, 2);
}

export default function ModelTestButton({ provider, model, testId }: ModelTestButtonProps) {
	const [testModel, { isLoading }] = useTestSBSModelMutation();
	const [result, setResult] = useState<SBSModelTestResult | null>(null);
	const [open, setOpen] = useState(false);

	const run = async () => {
		setResult(null);
		setOpen(true);
		try {
			setResult(await testModel({ provider, model }).unwrap());
		} catch (e: unknown) {
			const msg =
				(e as { data?: { error?: { message?: string } } })?.data?.error?.message ?? (e as Error)?.message ?? "request failed";
			setResult({ ok: false, provider, model, mode: "chat", latency_ms: 0, error: msg });
		}
	};

	const Icon = isLoading ? Loader2 : result ? (result.ok ? CheckCircle2 : XCircle) : FlaskConical;
	const color = result && !isLoading ? (result.ok ? "text-green-600" : "text-red-600") : "";
	const raw = pretty(result?.raw_request);

	return (
		<>
			<TooltipProvider>
				<Tooltip>
					<TooltipTrigger asChild>
						<Button
							variant="ghost"
							size="icon"
							className={`h-7 w-7 ${color}`}
							disabled={isLoading}
							onClick={() => (result && !open ? setOpen(true) : run())}
							aria-label={`Test ${model}`}
							data-testid={testId}
						>
							<Icon className={`h-4 w-4 ${isLoading ? "animate-spin" : ""}`} />
						</Button>
					</TooltipTrigger>
					<TooltipContent side="left" className="max-w-sm break-words">
						{isLoading ? "Testing…" : result ? `${summary(result)} (click for details)` : `Test ${model}`}
					</TooltipContent>
				</Tooltip>
			</TooltipProvider>

			<Dialog open={open} onOpenChange={setOpen}>
				<DialogContent className="max-w-2xl">
					<DialogHeader>
						<DialogTitle className="flex items-center gap-2 font-mono text-sm break-all">
							{isLoading ? (
								<Loader2 className="h-4 w-4 shrink-0 animate-spin" />
							) : result?.ok ? (
								<CheckCircle2 className="h-4 w-4 shrink-0 text-green-600" />
							) : (
								<XCircle className="h-4 w-4 shrink-0 text-red-600" />
							)}
							{provider} / {model}
						</DialogTitle>
						<DialogDescription>
							One user message sent straight to the provider: no system prompt, no MCP tools, no injected tools, no plugins.
						</DialogDescription>
					</DialogHeader>

					{isLoading && <p className="text-muted-foreground text-sm">Testing…</p>}

					{result && !isLoading && (
						<div className="min-w-0 space-y-3 text-sm">
							<p className={result.ok ? "text-green-700 dark:text-green-500" : "text-red-700 dark:text-red-500"}>
								{result.ok ? "Working" : "Failed"} · {summary(result)}
							</p>
							{result.reply && (
								<div>
									<p className="text-muted-foreground mb-1 text-xs font-medium">Reply</p>
									<pre className="bg-muted max-h-40 overflow-auto rounded-sm p-2 text-xs whitespace-pre-wrap">{result.reply}</pre>
								</div>
							)}
							<div>
								<p className="text-muted-foreground mb-1 text-xs font-medium">Request sent to the provider</p>
								<pre className="bg-muted max-h-72 overflow-auto rounded-sm p-2 text-xs">
									{raw || "(the provider did not return the raw request)"}
								</pre>
							</div>
						</div>
					)}

					<div className="flex justify-end">
						<Button size="sm" variant="outline" onClick={run} disabled={isLoading}>
							<RotateCw className="mr-1 h-3.5 w-3.5" /> Run again
						</Button>
					</div>
				</DialogContent>
			</Dialog>
		</>
	);
}
