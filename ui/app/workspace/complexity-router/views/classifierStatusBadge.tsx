import { badgeVariants } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { SEMANTIC_STATUS_LABELS, SemanticStatusInfo } from "@/lib/types/complexityRouter";
import { cn } from "@/lib/utils";
import type { VariantProps } from "class-variance-authority";
import { Link } from "@tanstack/react-router";
import { ArrowRight, CircleAlert, CircleCheck, CircleDashed, LoaderCircle, RefreshCw } from "lucide-react";
import type { ReactNode } from "react";
import { semanticWarmupFailureMessage, semanticWarmupImpactMessage } from "./classifierStatusBadge.utils";

// The two states below are local to the form rather than reported by the
// gateway: nothing is embedded yet, so /semantic-status has nothing to say.
type ClassifierState = SemanticStatusInfo["state"] | "not-configured" | "not-saved" | "loading" | "unavailable";

// The badge names the subject as well as the state: on its own in the header,
// a bare "Ready" would not say what is ready.
const LABELS: Record<ClassifierState, string> = {
	ready: `Classifier ${SEMANTIC_STATUS_LABELS.ready.toLowerCase()}`,
	warming: `Classifier ${SEMANTIC_STATUS_LABELS.warming.toLowerCase()}`,
	failed: `Classifier ${SEMANTIC_STATUS_LABELS.failed.toLowerCase()}`,
	disabled: "Classifier off",
	"not-configured": "Classifier not configured",
	"not-saved": "Classifier not saved",
	loading: "Checking classifier…",
	unavailable: "Classifier status unavailable",
};

// Every tone comes from the shared Badge variants rather than a hand-picked
// palette, so this reads as the same kind of status chip the rest of the app
// uses. "Not configured" and "not saved" take the primary-tinted default: both
// are states the operator still has to act on.
const TONES: Record<ClassifierState, VariantProps<typeof badgeVariants>["variant"]> = {
	ready: "success",
	warming: "warning",
	failed: "destructive",
	disabled: "secondary",
	"not-configured": "default",
	"not-saved": "default",
	loading: "secondary",
	unavailable: "warning",
};

function StateIcon({ state }: { state: ClassifierState }) {
	switch (state) {
		case "ready":
			return <CircleCheck />;
		case "failed":
		case "unavailable":
			return <CircleAlert />;
		case "warming":
		case "loading":
			return <LoaderCircle className="animate-spin" />;
		default:
			return <CircleDashed />;
	}
}

// ClassifierStatusBadge surfaces warmup readiness, which is otherwise only
// visible in server logs. Without it a failed warmup looks identical to a
// working deployment, because complexity routing simply stops matching. It is
// deliberately a header badge rather than a panel: the detail only matters when
// something is off, so it lives one click away in the popover.
export function ClassifierStatusBadge({
	status,
	isLoading,
	isNotConfigured,
	isNotSaved,
	hasUnsavedChanges,
	hasEmbeddingProviders,
	statusUnavailable,
	statusRefreshFailed,
	isRetryingStatus,
	onConfigure,
	onRetryStatus,
}: {
	status: SemanticStatusInfo | undefined;
	isLoading: boolean;
	isNotConfigured: boolean;
	isNotSaved: boolean;
	hasUnsavedChanges: boolean;
	hasEmbeddingProviders: boolean;
	statusUnavailable: boolean;
	statusRefreshFailed: boolean;
	isRetryingStatus: boolean;
	onConfigure: () => void;
	onRetryStatus: () => void;
}) {
	const state: ClassifierState = isNotConfigured
		? "not-configured"
		: isNotSaved
			? "not-saved"
			: statusUnavailable
				? "unavailable"
				: status
					? status.state
					: isLoading
						? "loading"
						: "disabled";

	const summary: ReactNode = {
		"not-configured": hasEmbeddingProviders
			? "No embedding provider is selected, so requests carry no complexity tier and rules targeting one never match."
			: "No embedding-capable provider is configured, so requests carry no complexity tier and rules targeting one never match.",
		"not-saved": "Save this configuration to embed the reference phrases and activate classification.",
		loading: "Reading the classifier's warmup state…",
		warming: "Embedding the reference phrases. Classification starts once every phrase is loaded.",
		ready: status ? `${status.total} reference phrase${status.total === 1 ? "" : "s"} embedded and serving.` : "Serving.",
		failed: "Warmup failed.",
		disabled: "Classification is off.",
		unavailable: "Bifrost could not report whether the saved classifier is ready. Routing may still be working.",
	}[state];

	return (
		<Popover>
			<PopoverTrigger asChild>
				<button
					type="button"
					data-testid="complexity-router-semantic-status-badge"
					data-state-value={state}
					// h-8/gap-1.5/px-2.5 are the Button `sm` metrics, so the chip lines up
					// with the outline buttons it shares the header row with.
					className={cn(badgeVariants({ variant: TONES[state] }), "h-8 cursor-pointer gap-1.5 px-2.5 transition-opacity hover:opacity-80")}
				>
					<StateIcon state={state} />
					<span>{LABELS[state]}</span>
				</button>
			</PopoverTrigger>

			<PopoverContent align="end" className="w-80 space-y-2.5 p-3 text-xs leading-relaxed" data-testid="complexity-router-semantic-status">
				<p className="text-muted-foreground">{summary}</p>

				{status?.serving_previous && state === "warming" && (
					<p className="text-amber-700 dark:text-amber-400">
						The previous reference phrases are still serving requests while this generation prepares. Routing is unaffected.
					</p>
				)}

				{hasUnsavedChanges && state !== "not-configured" && state !== "not-saved" && (
					<p className="text-amber-700 dark:text-amber-400">
						The saved classifier is still serving. Save to prepare and activate these phrase or model changes.
					</p>
				)}

				{statusRefreshFailed && status && (
					<p className="text-amber-700 dark:text-amber-400">The latest status check failed. Showing the last known classifier state.</p>
				)}

				{state === "failed" && status && (
					<p className="text-destructive" data-testid="complexity-router-semantic-status-error">
						{semanticWarmupFailureMessage(status)}
					</p>
				)}

				{state === "failed" && status && (
					<p className={status.serving_previous ? "text-amber-700 dark:text-amber-400" : "text-destructive"}>
						{semanticWarmupImpactMessage(status)}
					</p>
				)}

				{state === "unavailable" && (
					<Button type="button" variant="outline" size="sm" className="w-full" onClick={onRetryStatus} disabled={isRetryingStatus}>
						<RefreshCw className={cn("size-3.5", isRetryingStatus && "animate-spin")} />
						Retry status
					</Button>
				)}

				{/* Offering "Configure embedding" with no embedding-capable provider
				    installed opens a sheet whose only control is empty, so the CTA
				    points at the step that actually unblocks them instead. */}
				{state === "not-configured" &&
					(hasEmbeddingProviders ? (
						<Button
							type="button"
							variant="outline"
							size="sm"
							className="w-full"
							onClick={onConfigure}
							data-testid="complexity-router-status-configure-button"
						>
							Configure embedding
						</Button>
					) : (
						<Button asChild variant="outline" size="sm" className="w-full" data-testid="complexity-router-status-add-provider-link">
							<Link to="/workspace/providers">
								Add an embedding provider
								<ArrowRight className="size-3.5" />
							</Link>
						</Button>
					))}
			</PopoverContent>
		</Popover>
	);
}
