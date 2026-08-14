import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import {
	SESSION_IDENTITY_SOURCE_OPTIONS,
	SESSION_MODE_OPTIONS,
	SessionIdentitySource,
	sessionStoreReadiness,
	SessionStoreReadiness,
	SessionStoreStatus,
} from "@/lib/types/complexityRouter";
import { cn } from "@/lib/utils";
import { Info, LoaderCircle, Save, TriangleAlert } from "lucide-react";
import { Controller, type Control, type FieldErrors, type UseFormRegister } from "react-hook-form";
import type { AnalyzerFormValues, SessionFormValues } from "../formSchema";
import { FieldLabel } from "./formPrimitives";

// "replicated-not-atomic" deliberately has no entry: it's covered in the docs
// rather than the UI, since a banner explaining replica-consistency mechanics
// is more than most operators configuring this form need to see.
const READINESS_COPY: Partial<Record<SessionStoreReadiness, { title: string; body: string }>> = {
	"node-local": {
		title: "Session state is node-local.",
		body: "Each gateway holds its own copy. That is fine on a single replica; if you run more than one, a conversation is tracked separately on each and can be pinned to a different tier on each.",
	},
	"shared-atomic": {
		title: "Shared and atomic.",
		body: "Every replica sees the same session records and concurrent updates to one conversation are serialized across them.",
	},
};

interface Props {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	control: Control<AnalyzerFormValues>;
	register: UseFormRegister<AnalyzerFormValues>;
	errors: FieldErrors<AnalyzerFormValues>["session"];
	session: SessionFormValues | undefined;
	canUpdate: boolean;
	// True once an embedding provider and model are saved. Session behavior acts
	// on the tier the classifier produces, so with no classifier there is nothing
	// to pin and these settings sit inert.
	isClassifierConfigured: boolean;
	// What the session backend reports about itself. Undefined means no store is
	// attached, which is not the same as an unsafe one.
	storeStatus: SessionStoreStatus | undefined;
	storeStatusLoading: boolean;
	canSave: boolean;
	isSaving: boolean;
	onSave: () => void;
}

// SessionConfigSheet holds the whole session block. Like the embedding sheet it
// is a sheet rather than a page section: mode is chosen once and then left
// alone, while the phrase lists behind it are what operators actually tune.
//
// Its fields are bound to the page's form, so closing the sheet keeps edits
// pending; the page footer can still save or discard them.
export default function SessionConfigSheet({
	open,
	onOpenChange,
	control,
	register,
	errors,
	session,
	canUpdate,
	isClassifierConfigured,
	storeStatus,
	storeStatusLoading,
	canSave,
	isSaving,
	onSave,
}: Props) {
	const mode = session?.mode ?? "off";
	const readiness = sessionStoreReadiness(storeStatus);
	const isEnabled = mode === "pinned" || mode === "cache_aware";
	const isCacheAware = mode === "cache_aware";
	const fieldsDisabled = !canUpdate || !isEnabled;

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="flex flex-col p-0" data-testid="complexity-router-session-sheet">
				<SheetHeader className="flex flex-col items-start gap-1 px-6 py-4" headerClassName="bg-card z-10 mb-0 border-b">
					<SheetTitle>Session behavior</SheetTitle>
					<SheetDescription className="text-xs">
						Whether a conversation keeps its first tier for the whole session, or reclassifies every turn and only changes tier when
						confidence or sustained signal says so.
					</SheetDescription>
				</SheetHeader>

				<div className="custom-scrollbar min-h-0 flex-1 space-y-5 overflow-y-auto px-6 py-5">
					{/* Session behavior acts on the classifier's output, so without a
					    classifier these controls save but never do anything. */}
					{!isClassifierConfigured && (
						<Alert variant="info" data-testid="complexity-router-session-needs-classifier">
							<Info className="h-4 w-4" />
							<AlertDescription>
								No embedding classifier is configured, so no tier is produced and there is nothing for a session to hold. These settings
								save, but take effect only once the classifier is running.
							</AlertDescription>
						</Alert>
					)}

					<div className="space-y-2">
						<FieldLabel htmlFor="session-mode">Mode</FieldLabel>
						<Controller
							control={control}
							name="session.mode"
							render={({ field }) => (
								<Select value={field.value} onValueChange={field.onChange} disabled={!canUpdate}>
									<SelectTrigger className="w-full" id="session-mode" data-testid="complexity-router-session-mode-select">
										<SelectValue />
									</SelectTrigger>
									<SelectContent>
										{SESSION_MODE_OPTIONS.map((option) => (
											<SelectItem key={option.value} value={option.value}>
												{option.label}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
							)}
						/>
						<p className="text-muted-foreground text-xs leading-relaxed">
							{SESSION_MODE_OPTIONS.find((option) => option.value === mode)?.description}
						</p>
					</div>

					{/* The backend can only describe itself, it cannot see how many
					    replicas are running, so neither of these two say "safe". They
					    say what the storage guarantees and leave the topology to the
					    operator, who is the only one who knows it. */}
					{isEnabled && !storeStatusLoading && readiness && READINESS_COPY[readiness] && (
						<Alert variant={readiness === "shared-atomic" ? "info" : "warning"} data-testid="complexity-router-session-store-readiness">
							{readiness === "shared-atomic" ? <Info className="h-4 w-4" /> : <TriangleAlert className="h-4 w-4" />}
							<AlertDescription>
								<span>
									<span className="font-medium">{READINESS_COPY[readiness].title}</span> {READINESS_COPY[readiness].body}
								</span>
							</AlertDescription>
						</Alert>
					)}

					<div className="space-y-2">
						<FieldLabel tooltip="Tried in order from the top. The first one that yields an ID wins, so a caller-sent header always beats a harness-provided one.">
							How a conversation is identified
						</FieldLabel>
						<Controller
							control={control}
							name="session.identity_sources"
							render={({ field }) => (
								<div className="divide-y rounded-sm border">
									{SESSION_IDENTITY_SOURCE_OPTIONS.map((option) => {
										const checked = field.value?.includes(option.value) ?? false;
										return (
											<div key={option.value} className="flex items-start gap-3 p-3">
												<Checkbox
													id={`session-identity-${option.value}`}
													data-testid={`complexity-router-session-identity-${option.value}`}
													className="mt-0.5"
													checked={checked}
													disabled={fieldsDisabled}
													onCheckedChange={(next) => {
														const current = field.value ?? [];
														// Rebuilt from the option order rather than by
														// appending, so the saved list reads in the same
														// order the gateway tries them.
														const selected = new Set<SessionIdentitySource>(current);
														if (next === true) selected.add(option.value);
														else selected.delete(option.value);
														field.onChange(
															SESSION_IDENTITY_SOURCE_OPTIONS.map((entry) => entry.value).filter((value) => selected.has(value)),
														);
													}}
												/>
												<div className="space-y-1">
													<Label htmlFor={`session-identity-${option.value}`} className="text-xs font-medium">
														{option.label}
													</Label>
													<p className="text-muted-foreground text-xs leading-relaxed">{option.description}</p>
												</div>
											</div>
										);
									})}
								</div>
							)}
						/>
						{errors?.identity_sources && <p className="text-destructive text-xs">{errors.identity_sources.message}</p>}
					</div>

					{/* Every control below only reads in cache_aware mode. They are hidden
					    rather than disabled in the other modes: a disabled field still
					    reads as a setting that applies, and there are five of them. */}
					{isCacheAware && (
						<div className="space-y-5 border-t pt-5" data-testid="complexity-router-session-cache-aware-fields">
							<div className="space-y-1">
								<h3 className="text-sm font-semibold">When a session may change tier</h3>
								<p className="text-muted-foreground text-xs leading-relaxed">
									A turn switches the session immediately if it's confident enough on its own, or once enough consecutive turns favor
									the move. These decide how sure and how sustained that needs to be.
								</p>
							</div>

							<div className="grid gap-4 sm:grid-cols-2">
								<div className="space-y-2">
									<FieldLabel
										htmlFor="session-switch-min-similarity"
										tooltip="Deliberately higher than the classifier's own minimum similarity: a low bar to classify a single turn, a higher bar to move a whole conversation. For a downgrade, clearing this on one turn switches immediately without waiting for the turn count below. 0 disables that fast path, so every downgrade waits for sustained turns instead."
									>
										Minimum similarity to switch
									</FieldLabel>
									<Input
										id="session-switch-min-similarity"
										data-testid="complexity-router-session-switch-similarity-input"
										type="number"
										min={0}
										max={0.99}
										step={0.05}
										disabled={fieldsDisabled}
										aria-invalid={errors?.switch_min_similarity ? true : undefined}
										className={cn("font-mono", errors?.switch_min_similarity && "border-destructive focus-visible:ring-destructive")}
										{...register("session.switch_min_similarity", {
											valueAsNumber: true,
										})}
									/>
									{errors?.switch_min_similarity ? (
										<p className="text-destructive text-xs">{errors.switch_min_similarity.message}</p>
									) : (
										<p className="text-muted-foreground text-xs leading-relaxed">
											Between 0 and 1. A single turn this confident switches right away.
										</p>
									)}
								</div>

								<div className="space-y-2">
									<FieldLabel
										htmlFor="session-downgrade-after-n-turns"
										tooltip="Long sessions are full of turns like 'yes' or 'run the tests' that classify as confidently simple. Those are exactly the turns not to downgrade on: the classifier is right about the turn and wrong about the conversation. Any lower tier counts toward this, not just the same one each time."
									>
										Turns before downgrading
									</FieldLabel>
									<Input
										id="session-downgrade-after-n-turns"
										data-testid="complexity-router-session-downgrade-input"
										type="number"
										min={1}
										step={1}
										disabled={fieldsDisabled}
										aria-invalid={errors?.downgrade_after_n_turns ? true : undefined}
										className={cn("font-mono", errors?.downgrade_after_n_turns && "border-destructive focus-visible:ring-destructive")}
										{...register("session.downgrade_after_n_turns", {
											valueAsNumber: true,
										})}
									/>
									{errors?.downgrade_after_n_turns ? (
										<p className="text-destructive text-xs">{errors.downgrade_after_n_turns.message}</p>
									) : (
										<p className="text-muted-foreground text-xs leading-relaxed">
											Used only when no single turn was confident enough to switch immediately.
										</p>
									)}
								</div>
							</div>

							<div className="grid gap-4 sm:grid-cols-2">
								<div className="space-y-2">
									<FieldLabel
										htmlFor="session-max-switches"
										tooltip="A backstop against a session oscillating between tiers. 0 means no limit."
									>
										Maximum switches per session
									</FieldLabel>
									<Input
										id="session-max-switches"
										data-testid="complexity-router-session-max-switches-input"
										type="number"
										min={0}
										step={1}
										disabled={fieldsDisabled}
										aria-invalid={errors?.max_switches_per_session ? true : undefined}
										className={cn("font-mono", errors?.max_switches_per_session && "border-destructive focus-visible:ring-destructive")}
										{...register("session.max_switches_per_session", {
											valueAsNumber: true,
										})}
									/>
									{errors?.max_switches_per_session ? (
										<p className="text-destructive text-xs">{errors.max_switches_per_session.message}</p>
									) : (
										<p className="text-muted-foreground text-xs leading-relaxed">0 means no limit.</p>
									)}
								</div>
							</div>

							<div className="flex items-center justify-between gap-6 border-t pt-4">
								<FieldLabel
									htmlFor="session-always-allow-escalation"
									tooltip="An escalation already accepts the cost of switching: holding a conversation on an undersized model to avoid that cost produces worse answers, which is a worse outcome than switching."
								>
									Always allow escalation to a higher tier
								</FieldLabel>
								<Controller
									control={control}
									name="session.always_allow_escalation"
									render={({ field }) => (
										<Switch
											id="session-always-allow-escalation"
											data-testid="complexity-router-session-escalation-switch"
											checked={field.value ?? false}
											onCheckedChange={field.onChange}
											disabled={fieldsDisabled}
										/>
									)}
								/>
							</div>
						</div>
					)}
				</div>

				<SheetFooter className="bg-card flex-row items-center justify-end gap-2 border-t px-6 py-4">
					<Button
						type="button"
						variant="outline"
						size="sm"
						onClick={() => onOpenChange(false)}
						data-testid="complexity-router-session-sheet-close-button"
					>
						Close
					</Button>
					<Button
						type="button"
						size="sm"
						onClick={onSave}
						disabled={!canSave || isSaving}
						data-testid="complexity-router-session-sheet-save-button"
					>
						{isSaving ? <LoaderCircle className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
						{isSaving ? "Saving…" : "Save changes"}
					</Button>
				</SheetFooter>
			</SheetContent>
		</Sheet>
	);
}