import { Card, CardContent, CardFooter, CardHeader } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { IS_ENTERPRISE } from "@/lib/constants/config";
import { getErrorMessage, useGetCoreConfigQuery, useLazyGetCoreConfigQuery } from "@/lib/store";
import { useUpdateClientMetadataMutation } from "@/lib/store/apis/configApi";
import { useGetModelConfigsQuery, useGetVirtualKeysQuery } from "@/lib/store/apis/governanceApi";
import { useGetAllKeysQuery } from "@/lib/store/apis/providersApi";
import { useGetSCIMProvidersQuery } from "@enterprise/lib/store/apis/scimApi";
import { cn } from "@/lib/utils";
import { useNavigate } from "@tanstack/react-router";
import type confetti from "canvas-confetti";
import { ChevronRight, X } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { useCookies } from "react-cookie";
import { toast } from "sonner";

const ONBOARDING_DISMISSED_COOKIE = "bifrost_onboarding_dismissed";
const METADATA_DISMISSED_KEY = "onboarding_dismissed";
const METADATA_SKIPPED_KEY = "onboarding_skipped";

type Section = "Security" | "Provider Setup" | "Everything Else";

interface Step {
	id: string;
	title: string;
	route: string;
	complete: boolean;
	section: Section;
}

let confettiFn: typeof confetti | null = null;

async function fireConfettiFrom(el: HTMLElement) {
	const rect = el.getBoundingClientRect();
	const originX = (rect.left + rect.width / 2) / window.innerWidth;
	const originY = (rect.top + rect.height / 2) / window.innerHeight;
	if (!confettiFn) {
		confettiFn = (await import("canvas-confetti")).default;
	}
	confettiFn?.({
		particleCount: 36,
		spread: 60,
		startVelocity: 28,
		ticks: 80,
		origin: { x: originX, y: originY },
		colors: ["#a855f7", "#ec4899", "#22c55e", "#f59e0b", "#3b82f6"],
		disableForReducedMotion: true,
	});
}

const parseSkippedIds = (raw: unknown) => (Array.isArray(raw) ? raw.filter((id): id is string => typeof id === "string") : []);

export default function OnboardingWidget() {
	const navigate = useNavigate();
	const [closedForSession, setClosedForSession] = useState(false);
	// When non-null, the user picked this step and is now configuring it on
	// another page. Backdrop undims, widget dims, and pointer-events still go
	// to the widget so X / Hide buttons remain reachable.
	const [activeStepId, setActiveStepId] = useState<string | null>(null);
	const [cookies, setCookie] = useCookies([ONBOARDING_DISMISSED_COOKIE]);
	const isDismissedForMe = !!cookies[ONBOARDING_DISMISSED_COOKIE];
	const shouldSkipCoreConfigQuery = closedForSession || isDismissedForMe;
	const [updateMetadata, { isLoading: writingMetadata }] = useUpdateClientMetadataMutation();
	const [fetchCoreConfig] = useLazyGetCoreConfigQuery();
	const pendingSkippedIdsRef = useRef<Set<string>>(new Set());
	const skipWriteChainRef = useRef<Promise<void>>(Promise.resolve());
	const [pendingSkippedIds, setPendingSkippedIds] = useState<string[]>([]);

	const { data: bifrostConfig } = useGetCoreConfigQuery({}, { skip: shouldSkipCoreConfigQuery });
	// A widget dismissed for everyone only needs the core-config query (to learn
	// the dismiss flag). Once isDismissedForAll is known, skip the provider and
	// governance queries so they don't hit the network on every page load.
	const isDismissedForAll = bifrostConfig?.metadata?.[METADATA_DISMISSED_KEY] === true;
	const shouldSkipChecklistQueries = shouldSkipCoreConfigQuery || isDismissedForAll;
	const { data: allKeys } = useGetAllKeysQuery(undefined, { skip: shouldSkipChecklistQueries });
	const { data: vksResponse } = useGetVirtualKeysQuery(undefined, {
		skip: shouldSkipChecklistQueries || !IS_ENTERPRISE,
	});
	const { data: modelConfigsResponse } = useGetModelConfigsQuery(undefined, {
		skip: shouldSkipChecklistQueries || !IS_ENTERPRISE,
	});
	const { data: scimProviders } = useGetSCIMProvidersQuery(undefined, {
		skip: shouldSkipChecklistQueries || !IS_ENTERPRISE,
	});
	const checklistReady =
		bifrostConfig !== undefined &&
		allKeys !== undefined &&
		(!IS_ENTERPRISE || (vksResponse !== undefined && modelConfigsResponse !== undefined && scimProviders !== undefined));

	const skippedIds = useMemo<string[]>(() => {
		return parseSkippedIds(bifrostConfig?.metadata?.[METADATA_SKIPPED_KEY]);
	}, [bifrostConfig?.metadata]);

	const authConfig = bifrostConfig?.auth_config;
	const clientConfig = bifrostConfig?.client_config;
	const authValueSet = (envVar: { value?: string; env_var?: string; from_env?: boolean } | undefined) => {
		if (!envVar) return false;
		return !!envVar.value || !!envVar.env_var;
	};

	const steps: Step[] = useMemo(() => {
		// Order: 1) Security, 2) Provider Setup, 3) Everything Else.
		// Security comes first so admins lock down access before exposing keys.
		const common: Step[] = [
			{
				id: "cors",
				title: "Restrict CORS origins",
				route: "/workspace/config/security",
				section: "Security",
				complete: (clientConfig?.allowed_origins?.length ?? 0) > 0,
			},
			{
				id: "dashboard-auth",
				title: "Set up dashboard auth",
				route: "/workspace/config/security",
				section: "Security",
				complete: !!authConfig?.is_enabled && authValueSet(authConfig?.admin_username) && authValueSet(authConfig?.admin_password),
			},
			{
				id: "enforce-inference-auth",
				title: "Enforce auth on inference",
				route: "/workspace/config/security",
				section: "Security",
				complete: !!clientConfig?.enforce_auth_on_inference,
			},
			{
				id: "provider-key",
				title: "Add a provider key",
				route: "/workspace/providers",
				section: "Provider Setup",
				complete: (allKeys?.length ?? 0) > 0,
			},
		];
		const enterprise: Step[] = IS_ENTERPRISE
			? [
					{
						id: "scim",
						title: "Configure SCIM provisioning",
						route: "/workspace/scim",
						section: "Everything Else",
						complete: (scimProviders?.length ?? 0) > 0,
					},
					{
						id: "models",
						title: "Configure governance model catalog",
						route: "/workspace/model-catalog",
						section: "Everything Else",
						complete: (modelConfigsResponse?.total_count ?? 0) > 0,
					},
					{
						id: "virtual-keys",
						title: "Set up virtual keys / access profiles",
						route: "/workspace/virtual-keys",
						section: "Everything Else",
						complete: (vksResponse?.total_count ?? 0) > 0,
					},
				]
			: [];
		return [...common, ...enterprise];
	}, [allKeys, clientConfig, authConfig, scimProviders, modelConfigsResponse, vksResponse]);

	// Map step id → checkbox element so we can launch confetti from the
	// exact tick position when a step transitions to complete.
	const checkboxRefs = useRef<Record<string, HTMLElement | null>>({});
	// Tracks previously-observed completion state per step so we only fire
	// confetti on the false → true edge (not on initial mount or skip).
	const prevCompleteRef = useRef<Record<string, boolean> | null>(null);

	const completeSignature = steps.map((s) => `${s.id}:${s.complete ? "1" : "0"}`).join("|");

	useEffect(() => {
		if (!checklistReady) return;
		if (prevCompleteRef.current === null) {
			// First observation — prime the snapshot without firing confetti
			// so an already-completed step on page load doesn't celebrate.
			const snapshot: Record<string, boolean> = {};
			steps.forEach((s) => {
				snapshot[s.id] = s.complete;
			});
			prevCompleteRef.current = snapshot;
			return;
		}
		const prev = prevCompleteRef.current;
		const next: Record<string, boolean> = {};
		steps.forEach((s) => {
			next[s.id] = s.complete;
			if (s.complete && !prev[s.id]) {
				const el = checkboxRefs.current[s.id];
				if (el) void fireConfettiFrom(el);
			}
		});
		prevCompleteRef.current = next;
		// completeSignature captures the actual data dependency; steps reference is recreated each render.
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [completeSignature, checklistReady]);

	// Auto-restore widget focus when the picked step is finished (either real
	// completion or explicit skip), so the user is pulled back to the checklist
	// without needing to interact with the dimmed widget.
	useEffect(() => {
		if (!checklistReady) return;
		if (!activeStepId) return;
		const active = steps.find((s) => s.id === activeStepId);
		if (!active) return;
		if (active.complete || skippedIds.includes(active.id)) {
			setActiveStepId(null);
		}
	}, [activeStepId, steps, skippedIds, checklistReady]);

	if (closedForSession || isDismissedForMe) {
		return null;
	}

	if (!checklistReady) {
		return null;
	}

	if (isDismissedForAll) {
		return null;
	}

	const isStepDone = (step: Step) => step.complete || skippedIds.includes(step.id) || pendingSkippedIds.includes(step.id);
	const doneCount = steps.filter(isStepDone).length;

	if (doneCount === steps.length) {
		return null;
	}

	const handleStepClick = (step: Step) => {
		if (isStepDone(step)) return;
		// Mark this step as the one in progress so the widget dims and the
		// backdrop lifts; user is now focused on the destination page.
		setActiveStepId(step.id);
		navigate({ to: step.route as string });
	};

	const handleSkip = async (stepId: string) => {
		if (skippedIds.includes(stepId) || pendingSkippedIdsRef.current.has(stepId)) return;
		const nextPending = new Set(pendingSkippedIdsRef.current).add(stepId);
		pendingSkippedIdsRef.current = nextPending;
		setPendingSkippedIds(Array.from(nextPending));

		const runSkipWrite = async () => {
			const latestConfig = await fetchCoreConfig({}, false).unwrap();
			const latestSkippedIds = parseSkippedIds(latestConfig?.metadata?.[METADATA_SKIPPED_KEY]);
			const mergedSkippedIds = Array.from(new Set([...latestSkippedIds, ...skippedIds, ...pendingSkippedIdsRef.current, stepId]));
			await updateMetadata({
				[METADATA_SKIPPED_KEY]: mergedSkippedIds,
			}).unwrap();
		};

		const queuedWrite = skipWriteChainRef.current.catch(() => undefined).then(runSkipWrite);
		skipWriteChainRef.current = queuedWrite;
		try {
			await queuedWrite;
		} catch (err) {
			toast.error(getErrorMessage(err));
		} finally {
			const remainingPending = new Set(pendingSkippedIdsRef.current);
			remainingPending.delete(stepId);
			pendingSkippedIdsRef.current = remainingPending;
			setPendingSkippedIds(Array.from(remainingPending));
		}
	};

	const handleHideForMe = () => {
		const expires = new Date();
		expires.setFullYear(expires.getFullYear() + 1);
		setCookie(ONBOARDING_DISMISSED_COOKIE, "true", { path: "/", expires });
	};

	const handleHideForAll = async () => {
		try {
			await updateMetadata({ [METADATA_DISMISSED_KEY]: true }).unwrap();
		} catch (err) {
			toast.error(getErrorMessage(err));
		}
	};

	const isWorking = activeStepId !== null;

	return (
		<>
			{!isWorking && (
				<div
					aria-hidden
					className="pointer-events-none fixed inset-0 z-30 bg-black/40 backdrop-blur-[1px] transition-opacity duration-200"
				/>
			)}
			<Card
				// While a step is active, keep the card visible and reachable so the
				// user can still close, hide, or skip without completing the step.
				className={cn(
					"fixed right-6 bottom-4 z-40 w-[360px] gap-0 py-0 shadow-lg origin-bottom-right",
					"transition-all duration-300 ease-out",
					isWorking ? "scale-95 opacity-70" : "scale-100 opacity-100",
				)}
			>
				<CardHeader className="flex flex-row items-start justify-between gap-2 px-4 py-3">
					<div className="flex min-w-0 flex-1 items-start gap-2">
						<span
							aria-hidden
							className="flex-shrink-0 pt-1 text-base leading-none select-none"
							style={{
								// Prefer Apple Color Emoji on Apple platforms; fall back to the
								// OS-native emoji font elsewhere. Apple does not license its
								// emoji set for redistribution, so non-Apple users see their
								// platform's design.
								fontFamily: '"Apple Color Emoji", "Segoe UI Emoji", "Noto Color Emoji", "EmojiOne Color", sans-serif',
							}}
						>
							👋
						</span>
						<div className="min-w-0 flex-1">
							<div className="text-sm font-semibold">Setup checklist</div>
							<div className="text-muted-foreground text-xs">
								{doneCount} of {steps.length} steps complete
							</div>
						</div>
					</div>
					<button
						aria-label="Close for now"
						type="button"
						data-testid="onboarding-close"
						onClick={() => setClosedForSession(true)}
						className="text-muted-foreground hover:text-foreground -m-1 flex-shrink-0 rounded p-1"
					>
						<X className="size-4" />
					</button>
				</CardHeader>
				<CardContent className="flex flex-col gap-1 border-t px-2 py-2">
					{steps.map((step, idx) => {
						// Mirror isStepDone: a step in flight to be skipped (pendingSkippedIds)
						// must read as skipped here too, or the row stays actionable while
						// doneCount already counts it done during a slow metadata write.
						const skipped = skippedIds.includes(step.id) || pendingSkippedIds.includes(step.id);
						const done = step.complete || skipped;
						// Emit a section label whenever this row's section differs from the
						// previous one (or it's the first row). Cheap inline grouping that
						// keeps the flat steps array intact for other lookups.
						const prevSection = idx === 0 ? null : steps[idx - 1].section;
						const showSectionHeader = step.section !== prevSection;
						return (
							<div key={step.id}>
								{showSectionHeader && (
									<div
										className={cn("text-muted-foreground px-2 pb-1 text-[10px] font-semibold tracking-wider uppercase", idx > 0 && "pt-3")}
									>
										{step.section}
									</div>
								)}
								<div
									className={cn(
										"group relative flex items-center gap-3 rounded-md px-2 py-2 transition-colors",
										!done && "hover:bg-accent",
									)}
								>
									<button
										type="button"
										data-testid={`onboarding-step-${step.id}`}
										disabled={done}
										onClick={() => handleStepClick(step)}
										className={cn("flex flex-1 items-center gap-3 text-left", done ? "cursor-default" : "cursor-pointer")}
									>
										<span
											ref={(el) => {
												checkboxRefs.current[step.id] = el;
											}}
											className="inline-flex"
										>
											<Checkbox checked={step.complete} disabled className="pointer-events-none" />
										</span>
										<span
											className={cn(
												"flex-1 text-sm",
												step.complete && "text-muted-foreground line-through",
												skipped && !step.complete && "text-muted-foreground italic",
											)}
										>
											{step.title}
										</span>
									</button>
									{!done && (
										<>
											<button
												type="button"
												data-testid={`onboarding-skip-${step.id}`}
												onClick={() => handleSkip(step.id)}
												disabled={writingMetadata}
												className="text-muted-foreground hover:text-foreground text-xs opacity-0 transition-opacity group-hover:opacity-100 focus:opacity-100 focus-visible:opacity-100 disabled:opacity-50"
											>
												Skip
											</button>
											<ChevronRight className="text-muted-foreground size-4 transition-transform group-hover:translate-x-0.5" />
										</>
									)}
									{skipped && !step.complete && <span className="text-muted-foreground text-xs">Skipped</span>}
								</div>
							</div>
						);
					})}
				</CardContent>
				<CardFooter className="grid grid-cols-2 divide-x border-t p-0 !pt-0 text-xs">
					<button
						type="button"
						data-testid="onboarding-later"
						onClick={handleHideForMe}
						className="text-muted-foreground hover:text-foreground py-2 text-center"
					>
						I'll do it later
					</button>
					<button
						type="button"
						data-testid="onboarding-skip-all"
						onClick={handleHideForAll}
						disabled={writingMetadata}
						className="text-muted-foreground hover:text-foreground py-2 text-center disabled:opacity-50"
					>
						Hide for everyone
					</button>
				</CardFooter>
			</Card>
		</>
	);
}