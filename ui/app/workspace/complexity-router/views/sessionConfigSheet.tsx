import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { SESSION_IDENTITY_SOURCE_OPTIONS, SESSION_MODE_OPTIONS, SessionIdentitySource } from "@/lib/types/complexityRouter";
import { cn } from "@/lib/utils";
import { Info, LoaderCircle, Save, TriangleAlert } from "lucide-react";
import { Controller, type Control, type FieldErrors, type UseFormRegister } from "react-hook-form";
import type { AnalyzerFormValues, SessionFormValues } from "../formSchema";
import { sessionTtlFieldValue } from "../formSchema";
import { FieldLabel } from "./formPrimitives";

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
	canSave,
	isSaving,
	onSave,
}: Props) {
	const mode = session?.mode ?? "off";
	const isEnabled = mode === "pinned" || mode === "cache_aware";
	const isCacheAware = mode === "cache_aware";
	const fieldsDisabled = !canUpdate || !isEnabled;

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="flex flex-col p-0" data-testid="complexity-router-session-sheet">
				<SheetHeader className="flex flex-col items-start gap-1 px-6 py-4" headerClassName="bg-card z-10 mb-0 border-b">
					<SheetTitle>Session behavior</SheetTitle>
					<SheetDescription className="text-xs">
						Whether a conversation is classified once and held at that tier, or reclassified on every turn.
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

					{/* Session state is held per node. Track A's readiness probe will
					    replace this with the live backend's guarantees; until then the
					    caveat is stated unconditionally rather than implied, because a
					    silent split across replicas looks like the feature working. */}
					{isEnabled && (
						<Alert variant="warning" data-testid="complexity-router-session-replica-warning">
							<TriangleAlert className="h-4 w-4" />
							<AlertDescription>
								Session state is held in each gateway&apos;s own memory. If you run more than one replica, turns handled by different
								replicas resolve their tiers independently, so a conversation can be pinned to two different tiers at once.
							</AlertDescription>
						</Alert>
					)}

					<div className="grid gap-4 sm:grid-cols-2">
						<div className="space-y-2">
							<FieldLabel
								htmlFor="session-ttl"
								tooltip="Measured from the last turn, not from the start of the conversation: the window slides on every request, so an active session never expires."
							>
								Session timeout (minutes)
							</FieldLabel>
							<Controller
								control={control}
								name="session.ttl"
								render={({ field }) => (
									<Input
										id="session-ttl"
										data-testid="complexity-router-session-ttl-input"
										type="number"
										min={1}
										step={5}
										disabled={fieldsDisabled}
										value={sessionTtlFieldValue(field.value)}
										onChange={(event) => {
											const raw = event.target.value;
											field.onChange(raw === "" ? "" : `${raw}m`);
										}}
										aria-invalid={errors?.ttl ? true : undefined}
										className={cn("font-mono", errors?.ttl && "border-destructive focus-visible:ring-destructive")}
									/>
								)}
							/>
							{errors?.ttl ? (
								<p className="text-destructive text-xs">{errors.ttl.message}</p>
							) : (
								<p className="text-muted-foreground text-xs leading-relaxed">How long an idle conversation keeps its tier.</p>
							)}
						</div>

						<div className="space-y-2">
							<FieldLabel
								htmlFor="session-release-after-failures"
								tooltip="After this many consecutive upstream failures the pin is dropped and the next turn is classified fresh, on the assumption that the pinned tier is the thing failing."
							>
								Release after failures
							</FieldLabel>
							<Input
								id="session-release-after-failures"
								data-testid="complexity-router-session-release-input"
								type="number"
								min={1}
								step={1}
								disabled={fieldsDisabled}
								aria-invalid={errors?.release_after_failures ? true : undefined}
								className={cn("font-mono", errors?.release_after_failures && "border-destructive focus-visible:ring-destructive")}
								{...register("session.release_after_failures", {
									valueAsNumber: true,
								})}
							/>
							{errors?.release_after_failures && <p className="text-destructive text-xs">{errors.release_after_failures.message}</p>}
						</div>
					</div>

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
					    reads as a setting that applies, and there are six of them. */}
					{isCacheAware && (
						<div className="space-y-5 border-t pt-5" data-testid="complexity-router-session-cache-aware-fields">
							<div className="space-y-1">
								<h3 className="text-sm font-semibold">When a session may change tier</h3>
								<p className="text-muted-foreground text-xs leading-relaxed">
									A pinned session gives up its prompt cache when it moves, so these decide when a move is worth that cost.
								</p>
							</div>

							<div className="grid gap-4 sm:grid-cols-2">
								<div className="space-y-2">
									<FieldLabel
										htmlFor="session-switch-min-similarity"
										tooltip="Deliberately higher than the classifier's own minimum similarity: a low bar to classify a single turn, a higher bar to move a whole conversation. 0 turns the gate off entirely."
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
											Between 0 and 1. How confident a classification must be before it may move the session.
										</p>
									)}
								</div>

								<div className="space-y-2">
									<FieldLabel
										htmlFor="session-downgrade-after-n-turns"
										tooltip="Long sessions are full of turns like 'yes' or 'run the tests' that classify as confidently simple. Those are exactly the turns not to downgrade on: the classifier is right about the turn and wrong about the conversation."
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
									{errors?.downgrade_after_n_turns && <p className="text-destructive text-xs">{errors.downgrade_after_n_turns.message}</p>}
								</div>
							</div>

							<div className="grid gap-4 sm:grid-cols-2">
								<div className="space-y-2">
									<FieldLabel
										htmlFor="session-min-cached-tokens"
										tooltip="Below this many cached tokens the cache is too small to be worth protecting, so a switch is allowed even when it would discard it."
									>
										Minimum cached tokens to hold
									</FieldLabel>
									<Input
										id="session-min-cached-tokens"
										data-testid="complexity-router-session-min-cached-tokens-input"
										type="number"
										min={0}
										step={128}
										disabled={fieldsDisabled}
										aria-invalid={errors?.min_cached_tokens_to_hold ? true : undefined}
										className={cn("font-mono", errors?.min_cached_tokens_to_hold && "border-destructive focus-visible:ring-destructive")}
										{...register("session.min_cached_tokens_to_hold", {
											valueAsNumber: true,
										})}
									/>
									{errors?.min_cached_tokens_to_hold && (
										<p className="text-destructive text-xs">{errors.min_cached_tokens_to_hold.message}</p>
									)}
								</div>

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
									tooltip="Escalations already ignore cache cost: holding a conversation on an undersized model to protect cache spend produces bad answers, which is a worse outcome than overpaying."
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