import { badgeVariants } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { SemanticStatusInfo } from "@/lib/types/complexityRouter";
import { cn } from "@/lib/utils";
import { Link } from "@tanstack/react-router";
import type { VariantProps } from "class-variance-authority";
import { ArrowRight, CircleAlert, CircleCheck, CircleDashed, LoaderCircle, RefreshCw } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { semanticWarmupFailureMessage, semanticWarmupImpactMessage } from "./classifierStatusBadge.utils";

// The two states below are local to the form rather than reported by the
// gateway: nothing is embedded yet, so /semantic-status has nothing to say.
type ClassifierState = SemanticStatusInfo["state"] | "not-configured" | "not-saved" | "loading" | "unavailable";

const LABEL_KEYS: Record<ClassifierState, string> = {
	ready: "classifierReady",
	warming: "classifierWarming",
	failed: "classifierFailed",
	disabled: "classifierOff",
	"not-configured": "classifierNotConfigured",
	"not-saved": "classifierNotSaved",
	loading: "classifierChecking",
	unavailable: "classifierUnavailable",
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
	canRetryWarmup,
	isRetryingWarmup,
	onConfigure,
	onRetryStatus,
	onRetryWarmup,
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
	canRetryWarmup: boolean;
	isRetryingWarmup: boolean;
	onConfigure: () => void;
	onRetryStatus: () => void;
	onRetryWarmup: () => void;
}) {
	const { t } = useTranslation("models");
	const tcpx = (key: string, opts?: Record<string, unknown>) => t(`routing.complexityUi.${key}`, opts);

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
		"not-configured": hasEmbeddingProviders ? tcpx("summaryNotConfigured") : tcpx("summaryNoEmbeddingProvider"),
		"not-saved": tcpx("summaryNotSaved"),
		loading: tcpx("summaryLoading"),
		warming: tcpx("summaryWarming"),
		ready: status ? tcpx("summaryReady", { count: status.total }) : tcpx("summaryServing"),
		failed: tcpx("summaryFailed"),
		disabled: tcpx("summaryDisabled"),
		unavailable: tcpx("summaryUnavailable"),
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
					<span>{tcpx(LABEL_KEYS[state])}</span>
				</button>
			</PopoverTrigger>

			<PopoverContent
				align="end"
				className="z-0 w-80 space-y-2.5 p-3 text-xs leading-relaxed"
				data-testid="complexity-router-semantic-status"
			>
				<p className="text-muted-foreground">{summary}</p>

				{status?.serving_previous && state === "warming" && (
					<p className="text-amber-700 dark:text-amber-400">{tcpx("servingPrevious")}</p>
				)}

				{hasUnsavedChanges && state !== "not-configured" && state !== "not-saved" && (
					<p className="text-amber-700 dark:text-amber-400">{tcpx("unsavedServing")}</p>
				)}

				{statusRefreshFailed && status && (
					<p className="text-amber-700 dark:text-amber-400">{tcpx("statusRefreshFailed")}</p>
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

				{state === "failed" && canRetryWarmup && !hasUnsavedChanges && (
					<Button type="button" variant="outline" size="sm" className="w-full" onClick={onRetryWarmup} disabled={isRetryingWarmup}>
						<RefreshCw className={cn("size-3.5", isRetryingWarmup && "animate-spin")} />
						{tcpx("retryWarmup")}
					</Button>
				)}

				{state === "unavailable" && (
					<Button type="button" variant="outline" size="sm" className="w-full" onClick={onRetryStatus} disabled={isRetryingStatus}>
						<RefreshCw className={cn("size-3.5", isRetryingStatus && "animate-spin")} />
						{tcpx("retryStatus")}
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
							{tcpx("configureEmbedding")}
						</Button>
					) : (
						<Button asChild variant="outline" size="sm" className="w-full" data-testid="complexity-router-status-add-provider-link">
							<Link to="/workspace/providers">
								{tcpx("addEmbeddingProvider")}
								<ArrowRight className="size-3.5" />
							</Link>
						</Button>
					))}
			</PopoverContent>
		</Popover>
	);
}