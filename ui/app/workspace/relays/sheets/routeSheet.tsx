import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { RelayRouteMethods, type RelayUsageConfig } from "@/lib/types/relays";
import { relayRouteFormSchema, relayUsageUnitsPathError } from "@/lib/types/schemas";
import { cn } from "@/lib/utils";
import { cleanUsage, routeBillableByDefault, withoutPricing } from "@/lib/utils/relayForm";
import { zodResolver } from "@hookform/resolvers/zod";
import { ChevronDown, Waypoints } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm, type Control, type FieldValues } from "react-hook-form";
import { HeaderPairsTable } from "../fragments/headerPairsTable";
import { UsageFields } from "../fragments/usageFields";
import type { RouteForm } from "../routes.utils";
import { InfoLabel } from "../fragments/infoLabel";

const STREAM_AUTO = "auto";

interface RouteSheetProps {
	show: boolean;
	route: RouteForm | null;
	isNew: boolean;
	endpointUsage: RelayUsageConfig | undefined;
	ignoreUpstreamCost?: boolean;
	saving: boolean;
	disabled?: boolean;
	onCancel: () => void;
	onSave: (route: RouteForm) => void;
}

/** Adds or edits one route, in the same sheet shape as adding a provider key. */
export function RouteSheet({ show, route, isNew, endpointUsage, ignoreUpstreamCost, saving, disabled, onCancel, onSave }: RouteSheetProps) {
	const form = useForm<RouteForm>({
		resolver: zodResolver(relayRouteFormSchema),
		defaultValues: route ?? { method: "POST", path: "/", headers: [] },
	});
	useEffect(() => {
		form.reset(route ?? { method: "POST", path: "/", headers: [] });
	}, [route, form]);
	const method = form.watch("method");
	const job = form.watch("job");
	const usage = form.watch("usage");
	// A route that is not billed has nothing to price, so its override is hidden.
	const billableNow = form.watch("billable") ?? routeBillableByDefault(method);
	const hasOverride = !!cleanUsage(usage, true);
	// Collapsed on every open; the summary line says whether the route overrides anything.
	const [pricingOpen, setPricingOpen] = useState(false);
	useEffect(() => {
		if (show) setPricingOpen(false);
	}, [show, route]);

	// The backend judges a route's pricing merged with the relay's, which the form schema cannot see.
	// A route that is not billed is never priced and hides its pricing, so the pricing is dropped rather
	// than left to fail a check the person cannot see; its model and token mapping stays.
	const save = (values: RouteForm) => {
		if (!(values.billable ?? routeBillableByDefault(values.method))) {
			onSave({ ...values, usage: withoutPricing(values.usage ?? undefined) });
			return;
		}
		const message = relayUsageUnitsPathError(values.usage ?? undefined, endpointUsage);
		if (message) {
			form.setError("usage.units_path", { message });
			setPricingOpen(true);
			return;
		}
		onSave(values);
	};

	return (
		<Sheet open={show} onOpenChange={(open) => !open && onCancel()}>
			<SheetContent className="p-0 pt-4" data-testid="relay-route-sheet" onInteractOutside={(e) => e.preventDefault()}>
				<SheetHeader className="flex flex-col items-start py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8">
					<SheetTitle>
						<div className="font-lg flex items-center gap-2">
							<Waypoints className="h-5 w-5" />
							{isNew ? "Add route" : "Edit route"}
						</div>
					</SheetTitle>
				</SheetHeader>
				<Form {...form}>
					<form
						onSubmit={form.handleSubmit(save, (errors) => errors.usage && setPricingOpen(true))}
						className="flex grow flex-col gap-6 pt-4"
					>
						<div className="grow space-y-4 px-4 md:px-8">
							<div className="flex w-full flex-row items-start gap-4">
								<FormField
									control={form.control}
									name="method"
									render={({ field }) => (
										<FormItem className="w-32">
											<FormLabel>Method</FormLabel>
											<Select onValueChange={field.onChange} value={field.value} disabled={disabled}>
												<FormControl>
													<SelectTrigger className="w-full" data-testid="relay-route-method">
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
											<FormMessage />
										</FormItem>
									)}
								/>
								<FormField
									control={form.control}
									name="path"
									render={({ field }) => (
										<FormItem className="flex-1">
											<InfoLabel label="Path (Required)" info="* matches within one segment; a trailing /** matches everything below." />
											<FormControl>
												<Input placeholder="/v1/search" disabled={disabled} data-testid="relay-route-path" {...field} />
											</FormControl>
											<FormMessage />
										</FormItem>
									)}
								/>
							</div>

							<FormField
								control={form.control}
								name="headers"
								render={({ field }) => (
									<FormItem>
										<InfoLabel
											label="Header Matchers (Optional)"
											info="Request headers that must also match, such as X-Amz-Target for AWS JSON APIs. Values are globs."
										/>
										<HeaderPairsTable
											value={field.value}
											onChange={field.onChange}
											errors={form.formState.errors.headers}
											keyPlaceholder="X-Amz-Target"
											valuePlaceholder="Textract.Analyze*"
											disabled={disabled}
											testIdPrefix="relay-route-header"
										/>
									</FormItem>
								)}
							/>

							<FormField
								control={form.control}
								name="stream"
								render={({ field }) => (
									<FormItem>
										<InfoLabel label="Streaming" info={<>Auto follows the relay&apos;s streaming settings.</>} />
										<Select
											value={field.value === undefined ? STREAM_AUTO : field.value ? "on" : "off"}
											onValueChange={(v) => field.onChange(v === STREAM_AUTO ? undefined : v === "on")}
											disabled={disabled || !!job}
										>
											<FormControl>
												<SelectTrigger className="w-full" data-testid="relay-route-stream">
													<SelectValue />
												</SelectTrigger>
											</FormControl>
											<SelectContent>
												<SelectItem value={STREAM_AUTO}>Auto</SelectItem>
												<SelectItem value="on">Always stream</SelectItem>
												<SelectItem value="off">Never stream</SelectItem>
											</SelectContent>
										</Select>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={form.control}
								name="billable"
								render={({ field }) => (
									<FormItem className="flex flex-row items-center justify-between">
										<div className="space-y-0.5">
											<InfoLabel
												label="Billable"
												info={
													<>
														Counted against rate limits and charged to budgets. Default for {method}:{" "}
														{routeBillableByDefault(method) ? "yes" : "no"}.
													</>
												}
											/>
										</div>
										<FormControl>
											<Switch
												checked={field.value ?? routeBillableByDefault(method)}
												onCheckedChange={field.onChange}
												disabled={disabled || !!job}
												data-testid="relay-route-billable"
											/>
										</FormControl>
									</FormItem>
								)}
							/>
							{billableNow && (
								<Collapsible open={pricingOpen} onOpenChange={setPricingOpen} className="rounded-sm border">
									<CollapsibleTrigger asChild>
										<button
											type="button"
											className="flex w-full items-center justify-between px-4 py-3 text-left"
											data-testid="relay-route-pricing-toggle"
										>
											<div className="space-y-0.5">
												<div className="text-sm font-medium">Pricing Override</div>
												<p className="text-muted-foreground text-sm">
													{hasOverride ? "This route overrides the relay pricing." : "Uses the relay pricing."}
												</p>
											</div>
											<ChevronDown className={cn("h-4 w-4 transition-transform", pricingOpen && "rotate-180")} />
										</button>
									</CollapsibleTrigger>
									<CollapsibleContent className="space-y-4 border-t px-4 py-4">
										<p className="text-muted-foreground text-sm">
											Empty fields inherit the relay&apos;s pricing; set a field to 0 to waive it on this route.
										</p>
										<UsageFields
											control={form.control as unknown as Control<FieldValues>}
											name="usage"
											value={usage}
											inheritFrom={endpointUsage ?? {}}
											disabled={disabled}
											ignoreUpstreamCost={ignoreUpstreamCost}
											testIdPrefix="relay-route-usage"
										/>
									</CollapsibleContent>
								</Collapsible>
							)}
						</div>

						<div className="bg-card sticky bottom-0 z-10 flex justify-end gap-2 border-t px-4 py-4 md:px-8">
							<Button type="button" variant="outline" onClick={onCancel}>
								Cancel
							</Button>
							<Button type="submit" disabled={disabled} isLoading={saving} data-testid="relay-route-save">
								{isNew ? "Add Route" : "Save Route"}
							</Button>
						</div>
					</form>
				</Form>
			</SheetContent>
		</Sheet>
	);
}