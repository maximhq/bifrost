import { Button } from "@/components/ui/button";
import { Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { getErrorMessage, setProviderFormDirtyState, useAppDispatch } from "@/lib/store";
import { useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import { DefaultKeySelectionCooldownSeconds, type KeySelectionStrategy, type ModelProvider } from "@/lib/types/config";
import { keySelectionFormSchema, type KeySelectionFormSchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { useForm, type Resolver } from "react-hook-form";
import { toast } from "sonner";
import { buildProviderUpdatePayload } from "../views/utils";

interface KeySelectionFormFragmentProps {
	provider: ModelProvider;
}

const DEFAULT_STICKY_LIMIT = 1;

const STRATEGIES: { value: KeySelectionStrategy; label: string; description: string }[] = [
	{
		value: "weighted_random",
		label: "Weighted random (default)",
		description: "Picks a key at random on every request, in proportion to each key's weight.",
	},
	{
		value: "round_robin",
		label: "Round robin",
		description: "Cycles through keys in order, moving to the next key after the sticky limit is reached.",
	},
	{
		value: "least_used",
		label: "Least used",
		description: "Sends each request to the key that has served the fewest requests so far.",
	},
	{
		value: "fill_first",
		label: "Fill first",
		description: "Uses the first healthy key until it is rate limited or fails, then moves on to the next.",
	},
];

const toFormValues = (provider: ModelProvider): KeySelectionFormSchema => ({
	strategy: provider.key_selection?.strategy ?? "weighted_random",
	sticky_limit: provider.key_selection?.sticky_limit || DEFAULT_STICKY_LIMIT,
	cooldown_seconds: provider.key_selection?.cooldown_seconds ?? DefaultKeySelectionCooldownSeconds,
});

// Parses a number input's raw text into the form value: empty clears the field so the
// schema (or the server default) decides, and non-integers are ignored while typing.
const parseIntegerInput = (raw: string): number | undefined | null => {
	if (raw === "") return undefined;
	const parsed = Number.parseInt(raw, 10);
	return Number.isNaN(parsed) ? null : parsed;
};

export function KeySelectionFormFragment({ provider }: KeySelectionFormFragmentProps) {
	const dispatch = useAppDispatch();
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();

	const form = useForm<KeySelectionFormSchema, any, KeySelectionFormSchema>({
		resolver: zodResolver(keySelectionFormSchema) as Resolver<KeySelectionFormSchema, any, KeySelectionFormSchema>,
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: toFormValues(provider),
	});

	useEffect(() => {
		dispatch(setProviderFormDirtyState(form.formState.isDirty));
	}, [form.formState.isDirty, dispatch]);

	useEffect(() => {
		form.reset(toFormValues(provider));
	}, [form, provider.name, provider.key_selection]);

	const strategy = form.watch("strategy");
	const isRoundRobin = strategy === "round_robin";
	const selectedStrategy = STRATEGIES.find((s) => s.value === strategy);

	const onSubmit = (data: KeySelectionFormSchema) => {
		updateProvider(
			buildProviderUpdatePayload(provider, {
				key_selection: {
					strategy: data.strategy,
					// sticky_limit only means something for round_robin; the server rejects it elsewhere.
					sticky_limit: data.strategy === "round_robin" ? (data.sticky_limit ?? DEFAULT_STICKY_LIMIT) : undefined,
					// Omitted means the server default (60s); 0 is sent explicitly to disable cooldown.
					cooldown_seconds: data.cooldown_seconds,
				},
			}),
		)
			.unwrap()
			.then(() => {
				toast.success("Key rotation updated successfully");
				form.reset(data);
			})
			.catch((err) => {
				toast.error("Failed to update key rotation", {
					description: getErrorMessage(err),
				});
			});
	};

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6 px-4 md:px-6" data-testid="provider-config-key-selection-content">
				<p className="text-muted-foreground text-xs">
					Controls how requests are spread across this provider&apos;s keys (accounts). Keys that hit a rate limit or an authentication
					error are skipped for the cooldown period, across requests, before they are tried again.
				</p>
				<div className="space-y-4">
					<FormField
						control={form.control}
						name="strategy"
						render={({ field }) => (
							<FormItem>
								<FormLabel>Strategy</FormLabel>
								<Select
									value={field.value}
									onValueChange={(value) => {
										field.onChange(value);
										form.trigger();
									}}
									disabled={!hasUpdateProviderAccess}
								>
									<FormControl>
										<SelectTrigger data-testid="provider-key-selection-strategy-select" className="w-72">
											<SelectValue />
										</SelectTrigger>
									</FormControl>
									<SelectContent>
										{STRATEGIES.map((option) => (
											<SelectItem
												key={option.value}
												value={option.value}
												data-testid={`provider-key-selection-strategy-option-${option.value}`}
											>
												{option.label}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
								{selectedStrategy && (
									<FormDescription data-testid="provider-key-selection-strategy-description">{selectedStrategy.description}</FormDescription>
								)}
								<FormMessage />
							</FormItem>
						)}
					/>

					{isRoundRobin && (
						<FormField
							control={form.control}
							name="sticky_limit"
							render={({ field }) => (
								<FormItem>
									<FormLabel>Sticky limit</FormLabel>
									<FormControl>
										<Input
											type="number"
											min={1}
											max={1000}
											step={1}
											placeholder={String(DEFAULT_STICKY_LIMIT)}
											className="w-56"
											data-testid="provider-key-selection-sticky-limit-input"
											name={field.name}
											ref={field.ref}
											onBlur={field.onBlur}
											value={field.value === undefined || Number.isNaN(field.value) ? "" : field.value}
											disabled={!hasUpdateProviderAccess}
											onChange={(e) => {
												const parsed = parseIntegerInput(e.target.value);
												if (parsed === null) return;
												field.onChange(parsed);
												form.trigger("sticky_limit");
											}}
										/>
									</FormControl>
									<FormDescription>Consecutive requests a key serves before rotating to the next one (1-1000).</FormDescription>
									<FormMessage />
								</FormItem>
							)}
						/>
					)}

					<FormField
						control={form.control}
						name="cooldown_seconds"
						render={({ field }) => (
							<FormItem>
								<FormLabel>Cooldown (seconds)</FormLabel>
								<FormControl>
									<Input
										type="number"
										min={0}
										max={86400}
										step={1}
										placeholder={String(DefaultKeySelectionCooldownSeconds)}
										className="w-56"
										data-testid="provider-key-selection-cooldown-input"
										name={field.name}
										ref={field.ref}
										onBlur={field.onBlur}
										value={field.value === undefined || Number.isNaN(field.value) ? "" : field.value}
										disabled={!hasUpdateProviderAccess}
										onChange={(e) => {
											const parsed = parseIntegerInput(e.target.value);
											if (parsed === null) return;
											field.onChange(parsed);
											form.trigger("cooldown_seconds");
										}}
									/>
								</FormControl>
								<FormDescription>
									How long a rate-limited or failing key is skipped. 0 disables cooldown; leave empty for the default (
									{DefaultKeySelectionCooldownSeconds}s). Max 86400.
								</FormDescription>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>

				<div className="flex justify-end space-x-2 pb-6">
					<Button
						type="submit"
						data-testid="provider-key-selection-save-btn"
						disabled={!form.formState.isDirty || !form.formState.isValid || !hasUpdateProviderAccess || isUpdatingProvider}
						isLoading={isUpdatingProvider}
					>
						Save Key Rotation
					</Button>
				</div>
			</form>
		</Form>
	);
}
