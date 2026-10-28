import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { RelayRouteMethods, type RelayUsageConfig } from "@/lib/types/relays";
import { relayRouteFormSchema } from "@/lib/types/schemas";
import { cn } from "@/lib/utils";
import { cleanUsage, routeBillableByDefault } from "@/lib/utils/relayForm";
import { zodResolver } from "@hookform/resolvers/zod";
import { ChevronDown, Plus, Trash2, Waypoints } from "lucide-react";
import { useEffect, useState } from "react";
import { useFieldArray, useForm, type Control, type FieldValues } from "react-hook-form";
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
	const headers = useFieldArray({ control: form.control, name: "headers" });
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
						onSubmit={form.handleSubmit(onSave, (errors) => errors.usage && setPricingOpen(true))}
						className="space-y-4 px-4 pb-6 md:px-8"
					>
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

						<div className="space-y-2">
							<InfoLabel
								label="Header Matchers (Optional)"
								info="Request headers that must also match, such as X-Amz-Target for AWS JSON APIs. Values are globs."
								inForm={false}
							/>
							{headers.fields.map((field, i) => (
								<div key={field.id} className="flex items-start gap-2">
									<FormField
										control={form.control}
										name={`headers.${i}.name`}
										render={({ field: f }) => (
											<FormItem className="flex-1">
												<FormControl>
													<Input placeholder="X-Amz-Target" disabled={disabled} data-testid={`relay-route-header-name-${i}`} {...f} />
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
									<FormField
										control={form.control}
										name={`headers.${i}.value`}
										render={({ field: f }) => (
											<FormItem className="flex-1">
												<FormControl>
													<Input placeholder="Textract.Analyze*" disabled={disabled} data-testid={`relay-route-header-value-${i}`} {...f} />
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
									<Button
										type="button"
										variant="ghost"
										size="icon"
										aria-label="Remove header matcher"
										disabled={disabled}
										onClick={() => headers.remove(i)}
									>
										<Trash2 className="h-4 w-4" />
									</Button>
								</div>
							))}
							<Button
								type="button"
								variant="outline"
								size="sm"
								disabled={disabled}
								onClick={() => headers.append({ name: "", value: "" })}
								data-testid="relay-route-add-header"
							>
								<Plus className="h-4 w-4" /> Add header matcher
							</Button>
						</div>

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

						<div className="flex justify-end space-x-2 pt-4">
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