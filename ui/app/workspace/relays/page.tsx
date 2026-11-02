import FullPageLoader from "@/components/fullPageLoader";
import { LoadErrorView } from "@/components/loadErrorView";
import { Button } from "@/components/ui/button";
import { TruncatedLabel } from "@/components/ui/truncatedLabel";
import { useIsMobile } from "@/hooks/use-mobile";
import { useGetRelaysQuery } from "@/lib/store";
import { cn } from "@/lib/utils";
import { showLoadError } from "@/lib/utils/loadError";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { ArrowLeft, PlusIcon } from "lucide-react";
import { useQueryState } from "nuqs";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { DeleteEndpointDialog } from "./dialogs/deleteEndpointDialog";
import { AddEndpointSheet } from "./sheets/addEndpointSheet";
import { EndpointsEmptyState } from "./views/endpointsEmptyState";
import { EndpointView } from "./views/endpointView";
import { useActionParam } from "@/hooks/useActionParam";

/** Relays, laid out like the providers page: the endpoints on the left, the selected one on the right. */
export default function RelaysPage() {
	const { t } = useTranslation("config");
	const isMobile = useIsMobile();
	const canCreate = useRbac(RbacResource.Relays, RbacOperation.Create);
	const { data, isLoading, isFetching, error, refetch } = useGetRelaysQuery();
	const endpoints = data?.relays;
	const [selected, setSelected] = useQueryState("relay");
	const [showAddSheet, setShowAddSheet] = useState(false);
	useActionParam("create", () => setShowAddSheet(true), { isOpen: showAddSheet, enabled: canCreate });
	const [showDeleteDialog, setShowDeleteDialog] = useState(false);
	const [mobileDetailOpen, setMobileDetailOpen] = useState(false);

	// Skipped while the list refetches: a just-created endpoint is not in the old list yet, and
	// falling back then would move the selection off it.
	useEffect(() => {
		if (!endpoints?.length || isFetching) return;
		if (!selected || !endpoints.some((e) => e.name === selected)) void setSelected(endpoints[0].name);
	}, [endpoints, isFetching, selected, setSelected]);

	if (showLoadError(error, data)) {
		return <LoadErrorView entity="relays" error={error} onRetry={() => void refetch()} testId="relays-load-error" />;
	}
	if (isLoading) return <FullPageLoader />;

	const addSheet = (
		<AddEndpointSheet show={showAddSheet} onCancel={() => setShowAddSheet(false)} onCreated={(name) => void setSelected(name)} />
	);
	if (endpoints?.length === 0) {
		return (
			<div className="mx-auto w-full max-w-7xl">
				<EndpointsEmptyState onCreate={() => setShowAddSheet(true)} canCreate={canCreate} />
				{addSheet}
			</div>
		);
	}

	const current = endpoints?.find((e) => e.name === selected);
	return (
		<div className="flex w-full flex-col gap-4 md:h-[calc(var(--app-content-viewport)_-_55px)] md:flex-row">
			{addSheet}
			{current && (
				<DeleteEndpointDialog
					name={current.name}
					show={showDeleteDialog}
					onCancel={() => setShowDeleteDialog(false)}
					onDeleted={() => {
						setShowDeleteDialog(false);
						void setSelected(null);
						setMobileDetailOpen(false);
					}}
				/>
			)}
			<div
				className={cn(
					"w-full flex-col md:flex md:h-[calc(var(--app-content-viewport)_-_55px)] md:w-[300px]",
					mobileDetailOpen ? "hidden" : "flex",
				)}
			>
				<div className="flex min-h-0 flex-1 flex-col rounded-md bg-zinc-50/50 md:p-4 md:pb-0 dark:bg-zinc-800/20">
					<div className="text-muted-foreground mb-2 shrink-0 text-xs font-medium">{t("relays.page.title")}</div>
					<div className="custom-scrollbar min-h-0 flex-1 overflow-y-auto">
						{endpoints?.map((endpoint) => (
							<div
								key={endpoint.name}
								role="button"
								tabIndex={0}
								data-testid="relay-list-item"
								className={cn(
									"mb-1 flex h-8 w-full min-w-0 cursor-pointer items-center gap-2 rounded-sm border px-3 text-sm",
									selected === endpoint.name
										? "bg-secondary opacity-100 hover:opacity-100"
										: "hover:bg-secondary cursor-pointer border-transparent opacity-100 hover:border",
								)}
								onClick={() => {
									void setSelected(endpoint.name);
									if (isMobile) setMobileDetailOpen(true);
								}}
								onKeyDown={(e) => {
									if (e.key === "Enter" || e.key === " ") {
										e.preventDefault();
										void setSelected(endpoint.name);
									}
								}}
							>
								<TruncatedLabel className="flex-1 text-sm">{endpoint.name}</TruncatedLabel>
							</div>
						))}
						{canCreate && (
							<div className="sticky bottom-0 bg-zinc-50/50 py-2 backdrop-blur-sm dark:bg-zinc-800/20">
								<Button
									size="sm"
									variant="outline"
									className="w-full justify-start"
									onClick={() => setShowAddSheet(true)}
									data-testid="relays-create-button"
								>
									<PlusIcon className="h-4 w-4" />
									{t("relays.page.addRelay")}
								</Button>
							</div>
						)}
					</div>
				</div>
			</div>
			{/* Scrolls on its own so the relay list stays in view beside a long route table. */}
			<div className={cn("custom-scrollbar min-w-0 w-full md:h-full", mobileDetailOpen ? "block" : "hidden md:block")}>
				<Button variant="ghost" size="sm" className="mb-3 -ml-2 md:hidden" onClick={() => setMobileDetailOpen(false)}>
					<ArrowLeft className="size-4" />
					{t("relays.page.back")}
				</Button>
				{current && <EndpointView key={current.name} endpoint={current} onRequestDelete={() => setShowDeleteDialog(true)} />}
			</div>
		</div>
	);
}