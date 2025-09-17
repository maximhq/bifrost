"use client";

import { Badge } from "@/components/ui/badge";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Separator } from "@/components/ui/separator";
import { VirtualKey } from "@/lib/types/governance";
import { formatDistanceToNow } from "date-fns";
import { parseResetPeriod, calculateUsagePercentage, getUsageVariant } from "@/lib/utils/governance";
import { formatCurrency } from "@/lib/utils/governance";

interface VirtualKeyDetailDialogProps {
	virtualKey: VirtualKey;
	onClose: () => void;
}

export default function VirtualKeyDetailDialog({ virtualKey, onClose }: VirtualKeyDetailDialogProps) {
	const getEntityInfo = () => {
		if (virtualKey.team) {
			return { type: "Team", name: virtualKey.team.name };
		}
		if (virtualKey.customer) {
			return { type: "Customer", name: virtualKey.customer.name };
		}
		return { type: "None", name: "" };
	};

	const entityInfo = getEntityInfo();

	const isExhausted =
		(virtualKey.budget?.current_usage && virtualKey.budget?.max_limit && virtualKey.budget.current_usage >= virtualKey.budget.max_limit) ||
		(virtualKey.rate_limit?.token_current_usage &&
			virtualKey.rate_limit?.token_max_limit &&
			virtualKey.rate_limit.token_current_usage >= virtualKey.rate_limit.token_max_limit) ||
		(virtualKey.rate_limit?.request_current_usage &&
			virtualKey.rate_limit?.request_max_limit &&
			virtualKey.rate_limit.request_current_usage >= virtualKey.rate_limit.request_max_limit);

	return (
		<Dialog open onOpenChange={onClose}>
			<DialogContent className="max-h-[80vh] w-full max-w-2xl overflow-y-auto p-0">
				<DialogHeader className="z-10 border-b px-6 pt-6">
					<DialogTitle>{virtualKey.name}</DialogTitle>
					<DialogDescription>{virtualKey.description || "Virtual key details and usage information"}</DialogDescription>
				</DialogHeader>

				<div className="space-y-6 px-6 pb-6">
					{/* Basic Information */}
					<div className="space-y-4">
						<h3 className="font-semibold">Basic Information</h3>

						<div className="grid gap-4">
							<div className="grid grid-cols-3 items-center gap-4">
								<span className="text-muted-foreground text-sm">Status</span>
								<div className="col-span-2">
									<Badge variant={virtualKey.is_active ? (isExhausted ? "destructive" : "default") : "secondary"}>
										{virtualKey.is_active ? (isExhausted ? "Exhausted" : "Active") : "Inactive"}
									</Badge>
								</div>
							</div>

							<div className="grid grid-cols-3 items-center gap-4">
								<span className="text-muted-foreground text-sm">Created</span>
								<div className="col-span-2 text-sm">{formatDistanceToNow(new Date(virtualKey.created_at), { addSuffix: true })}</div>
							</div>

							<div className="grid grid-cols-3 items-center gap-4">
								<span className="text-muted-foreground text-sm">Last Updated</span>
								<div className="col-span-2 text-sm">{formatDistanceToNow(new Date(virtualKey.updated_at), { addSuffix: true })}</div>
							</div>
						</div>
					</div>

					<Separator />

					{/* Entity Assignment */}
					<div className="space-y-4">
						<h3 className="font-semibold">Entity Assignment</h3>

						<div className="grid grid-cols-3 items-center gap-4">
							<span className="text-muted-foreground text-sm">Assigned To</span>
							<div className="col-span-2 flex items-center gap-2">
								<Badge variant={entityInfo.type === "None" ? "outline" : "secondary"}>{entityInfo.type}</Badge>
								<span className="text-sm">{entityInfo.name}</span>
							</div>
						</div>
					</div>

					<Separator />

					{/* Model & Provider Restrictions */}
					<div className="space-y-4">
						<h3 className="font-semibold">Allowed Models & Providers</h3>

						<div className="space-y-3">
							{!virtualKey.allowed_models && !virtualKey.allowed_providers ? (
								<span className="text-muted-foreground text-sm">All models and providers allowed</span>
							) : (
								<>
									{virtualKey.allowed_models && virtualKey.allowed_models.length > 0 ? (
										<div>
											<span className="text-muted-foreground text-sm font-medium">Models</span>
											<div className="mt-2">
												{virtualKey.allowed_models && virtualKey.allowed_models.length > 0 ? (
													<div className="flex flex-wrap gap-2">
														{virtualKey.allowed_models.map((model) => (
															<Badge key={model} variant="secondary" className="text-xs">
																{model}
															</Badge>
														))}
													</div>
												) : (
													<span className="text-muted-foreground text-sm">All models allowed</span>
												)}
											</div>
										</div>
									) : (
										<span className="text-muted-foreground text-sm">All models allowed</span>
									)}
									{virtualKey.allowed_providers && virtualKey.allowed_providers.length > 0 ? (
										<div>
											<span className="text-muted-foreground text-sm font-medium">Providers</span>
											<div className="mt-2">
												{virtualKey.allowed_providers && virtualKey.allowed_providers.length > 0 ? (
													<div className="flex flex-wrap gap-2">
														{virtualKey.allowed_providers.map((provider) => (
															<Badge key={provider} variant="secondary" className="text-xs">
																{provider}
															</Badge>
														))}
													</div>
												) : (
													<span className="text-muted-foreground text-sm">All providers allowed</span>
												)}
											</div>
										</div>
									) : (
										<span className="text-muted-foreground text-sm">All providers allowed</span>
									)}
								</>
							)}
						</div>
					</div>

					<Separator />

					{/* Budget Information */}
					<div className="space-y-4">
						<h3 className="font-semibold">Budget Information</h3>

						{virtualKey.budget ? (
							<div className="space-y-3">
								<div className="grid grid-cols-3 items-center gap-4">
									<span className="text-muted-foreground text-sm">Usage</span>
									<div className="col-span-2">
										<div className="flex items-center gap-2">
											<span className="font-mono text-sm">
												{formatCurrency(virtualKey.budget.current_usage)} / {formatCurrency(virtualKey.budget.max_limit)}
											</span>
											<Badge
												variant={virtualKey.budget.current_usage >= virtualKey.budget.max_limit ? "destructive" : "default"}
												className="text-xs"
											>
												{Math.round((virtualKey.budget.current_usage / virtualKey.budget.max_limit) * 100)}%
											</Badge>
										</div>
									</div>
								</div>

								<div className="grid grid-cols-3 items-center gap-4">
									<span className="text-muted-foreground text-sm">Reset Period</span>
									<div className="col-span-2 text-sm">{parseResetPeriod(virtualKey.budget.reset_duration)}</div>
								</div>

								<div className="grid grid-cols-3 items-center gap-4">
									<span className="text-muted-foreground text-sm">Last Reset</span>
									<div className="col-span-2 text-sm">
										{formatDistanceToNow(new Date(virtualKey.budget.last_reset), { addSuffix: true })}
									</div>
								</div>
							</div>
						) : (
							<p className="text-muted-foreground text-sm">No budget limits configured</p>
						)}
					</div>

					<Separator />

					{/* Rate Limits */}
					<div className="space-y-4">
						<h3 className="font-semibold">Rate Limits</h3>

						{virtualKey.rate_limit ? (
							<div className="space-y-4">
								{/* Token Limits */}
								{virtualKey.rate_limit.token_max_limit && (
									<div className="rounded-lg border p-4">
										<div className="mb-3">
											<span className="font-medium">Token Limits</span>
										</div>

										<div className="space-y-2">
											<div className="grid grid-cols-3 items-center gap-4">
												<span className="text-muted-foreground text-sm">Usage</span>
												<div className="col-span-2">
													<div className="flex items-center gap-2">
														<span className="font-mono text-sm">
															{virtualKey.rate_limit.token_current_usage} / {virtualKey.rate_limit.token_max_limit}
														</span>
														<Badge
															variant={getUsageVariant(
																calculateUsagePercentage(virtualKey.rate_limit.token_current_usage, virtualKey.rate_limit.token_max_limit),
															)}
															className="text-xs"
														>
															{calculateUsagePercentage(virtualKey.rate_limit.token_current_usage, virtualKey.rate_limit.token_max_limit)}%
														</Badge>
													</div>
												</div>
											</div>

											<div className="grid grid-cols-3 items-center gap-4">
												<span className="text-muted-foreground text-sm">Reset Period</span>
												<div className="col-span-2 text-sm">{parseResetPeriod(virtualKey.rate_limit.token_reset_duration || "")}</div>
											</div>

											<div className="grid grid-cols-3 items-center gap-4">
												<span className="text-muted-foreground text-sm">Last Reset</span>
												<div className="col-span-2 text-sm">
													{formatDistanceToNow(new Date(virtualKey.rate_limit.token_last_reset), { addSuffix: true })}
												</div>
											</div>
										</div>
									</div>
								)}

								{/* Request Limits */}
								{virtualKey.rate_limit.request_max_limit && (
									<div className="rounded-lg border p-4">
										<div className="mb-3">
											<span className="font-medium">Request Limits</span>
										</div>

										<div className="space-y-2">
											<div className="grid grid-cols-3 items-center gap-4">
												<span className="text-muted-foreground text-sm">Usage</span>
												<div className="col-span-2">
													<div className="flex items-center gap-2">
														<span className="font-mono text-sm">
															{virtualKey.rate_limit.request_current_usage} / {virtualKey.rate_limit.request_max_limit}
														</span>
														<Badge
															variant={getUsageVariant(
																calculateUsagePercentage(
																	virtualKey.rate_limit.request_current_usage,
																	virtualKey.rate_limit.request_max_limit,
																),
															)}
															className="text-xs"
														>
															{calculateUsagePercentage(
																virtualKey.rate_limit.request_current_usage,
																virtualKey.rate_limit.request_max_limit,
															)}
															%
														</Badge>
													</div>
												</div>
											</div>

											<div className="grid grid-cols-3 items-center gap-4">
												<span className="text-muted-foreground text-sm">Reset Period</span>
												<div className="col-span-2 text-sm">{parseResetPeriod(virtualKey.rate_limit.request_reset_duration || "")}</div>
											</div>

											<div className="grid grid-cols-3 items-center gap-4">
												<span className="text-muted-foreground text-sm">Last Reset</span>
												<div className="col-span-2 text-sm">
													{formatDistanceToNow(new Date(virtualKey.rate_limit.request_last_reset), { addSuffix: true })}
												</div>
											</div>
										</div>
									</div>
								)}

								{!virtualKey.rate_limit.token_max_limit && !virtualKey.rate_limit.request_max_limit && (
									<p className="text-muted-foreground text-sm">No rate limits configured</p>
								)}
							</div>
						) : (
							<p className="text-muted-foreground text-sm">No rate limits configured</p>
						)}
					</div>
				</div>
			</DialogContent>
		</Dialog>
	);
}
