import { Button } from "@/components/ui/button";
import { IS_ENTERPRISE } from "@/lib/constants/config";
import { getCachedBrandingAssets } from "@/lib/hooks/useBranding";
import i18n from "@/lib/i18n";
import { getEndpointUrl } from "@/lib/utils/port";
import { consumeAutoReload, getSkewState, startVersionPoll, subscribeSkew } from "@/lib/utils/versionSkew";
import { Loader2, RefreshCw, Sparkles, WifiOff } from "lucide-react";
import { type ReactNode, useEffect, useState, useSyncExternalStore } from "react";
import { I18nextProvider, useTranslation } from "react-i18next";

function useSkewCause() {
	return useSyncExternalStore(subscribeSkew, getSkewState, getSkewState).cause;
}

interface BannerShellProps {
	title: string;
	description: string;
	testId: string;
	reloadTestId: string;
}

function BannerShell({ title, description, testId, reloadTestId }: BannerShellProps) {
	const { t } = useTranslation("shell");
	return (
		<div
			className="pointer-events-none fixed inset-x-0 bottom-4 z-[100] flex justify-center px-4 sm:bottom-6"
			data-testid={testId}
			role="status"
			aria-live="polite"
		>
			<div className="bg-card/95 pointer-events-auto flex w-full max-w-xl items-center gap-3 rounded-sm border p-2.5 pr-3 backdrop-blur sm:gap-4 sm:p-3">
				<div className="bg-primary/10 text-primary relative flex size-9 shrink-0 items-center justify-center rounded-sm">
					<span className="bg-primary/20 absolute size-2.5 rounded-full motion-safe:animate-ping" aria-hidden="true" />
					<span className="bg-primary relative size-2 rounded-full" aria-hidden="true" />
				</div>
				<div className="min-w-0 flex-1">
					<p className="text-foreground text-sm font-medium">{title}</p>
					<p className="text-muted-foreground truncate text-xs">{description}</p>
				</div>
				<Button variant="outline" size="sm" className="shrink-0" data-testid={reloadTestId} onClick={() => window.location.reload()}>
					<RefreshCw aria-hidden="true" />
					<span className="hidden sm:inline">{t("updating.reload")}</span>
				</Button>
			</div>
		</div>
	);
}

function SkewBannerBody() {
	const { t } = useTranslation("shell");
	const cause = useSkewCause();

	if (cause === "upgrade") {
		return (
			<BannerShell
				title={t("updating.bannerTitle")}
				description={t("updating.bannerDescription")}
				testId="updating-banner"
				reloadTestId="updating-banner-reload-btn"
			/>
		);
	}
	if (cause === "blocked") {
		return (
			<BannerShell
				title={t("assetLoadFailed.bannerTitle")}
				description={t("assetLoadFailed.bannerDescription")}
				testId="load-failed-banner"
				reloadTestId="load-failed-banner-reload-btn"
			/>
		);
	}
	// The probe has not settled yet; say nothing rather than guess.
	return null;
}

/** Bottom banner for a chunk that failed while the current page kept working. */
export function SkewBanner() {
	return (
		<I18nextProvider i18n={i18n}>
			<SkewBannerBody />
		</I18nextProvider>
	);
}

function ScreenShell({
	testId,
	labelledBy,
	describedBy,
	children,
}: {
	testId: string;
	labelledBy: string;
	describedBy: string;
	children: ReactNode;
}) {
	// Rendered above <ReduxProvider> (main.tsx) and as the router error component,
	// so branding has to come from the cache rather than a query.
	const [{ logoSrc, logoAlt }] = useState(() => getCachedBrandingAssets(false));

	return (
		<main
			className="bg-background relative isolate flex min-h-dvh items-center justify-center overflow-hidden p-4 sm:p-6"
			data-testid={testId}
		>
			<div
				className="bg-primary/6 pointer-events-none absolute inset-0 -z-10 [mask-image:radial-gradient(circle_at_center,black,transparent_68%)]"
				aria-hidden="true"
			/>
			<section
				className="bg-card w-full max-w-lg overflow-hidden rounded-sm border"
				aria-labelledby={labelledBy}
				aria-describedby={describedBy}
				aria-live="polite"
			>
				<div className="flex items-center border-b px-5 py-4 sm:px-6">
					<img src={logoSrc} alt={logoAlt} className="h-5 w-auto max-w-[180px] object-contain" />
				</div>
				<div className="px-5 py-8 sm:px-8 sm:py-10">{children}</div>
			</section>
		</main>
	);
}

function UpdatingScreenBody() {
	const { t } = useTranslation("shell");
	const [autoReloadExhausted, setAutoReloadExhausted] = useState(false);

	useEffect(
		() =>
			startVersionPoll({
				fetchVersion: (signal) =>
					fetch(getEndpointUrl("/api/version"), {
						cache: "no-store",
						credentials: "include",
						signal,
					}),
				onUpdateReady: () => {
					if (consumeAutoReload()) {
						window.location.reload();
						return;
					}
					setAutoReloadExhausted(true);
				},
				onTimeout: () => setAutoReloadExhausted(true),
			}),
		[],
	);

	return (
		<ScreenShell testId="updating-overlay" labelledBy="updating-title" describedBy="updating-description">
			<div className="bg-primary/10 text-primary flex size-12 items-center justify-center rounded-sm">
				<Sparkles className="size-5" aria-hidden="true" />
			</div>
			<h1 id="updating-title" className="text-foreground mt-5 text-2xl font-semibold tracking-tight">
				{t(IS_ENTERPRISE ? "updating.screenTitleEnterprise" : "updating.screenTitle")}
			</h1>
			<p id="updating-description" className="text-muted-foreground mt-2 max-w-md text-sm leading-6">
				{autoReloadExhausted
					? t(IS_ENTERPRISE ? "updating.descriptionStuckEnterprise" : "updating.descriptionStuck")
					: t(IS_ENTERPRISE ? "updating.descriptionWaitingEnterprise" : "updating.descriptionWaiting")}
			</p>

			<div className="bg-muted mt-7 h-1.5 overflow-hidden rounded-full" aria-hidden="true">
				{autoReloadExhausted ? (
					<div className="bg-muted-foreground/30 h-full w-full" />
				) : (
					<div className="bg-primary h-full w-1/3 rounded-full motion-safe:animate-[update-progress_1.8s_ease-in-out_infinite] motion-reduce:w-full" />
				)}
			</div>

			<div className="mt-4 flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
				<div className="text-muted-foreground flex items-center gap-2 text-xs">
					{autoReloadExhausted ? (
						<span>{t("updating.autoReloadPaused")}</span>
					) : (
						<>
							<Loader2 className="size-3.5 motion-safe:animate-spin" aria-hidden="true" />
							<span>{t(IS_ENTERPRISE ? "updating.waitingEnterprise" : "updating.waiting")}</span>
						</>
					)}
				</div>
				<Button
					variant={autoReloadExhausted ? "default" : "outline"}
					size="sm"
					className="w-full sm:w-auto"
					data-testid="updating-reload-btn"
					onClick={() => window.location.reload()}
				>
					<RefreshCw aria-hidden="true" />
					{t("updating.reloadNow")}
				</Button>
			</div>
		</ScreenShell>
	);
}

function LoadFailedScreenBody() {
	const { t } = useTranslation("shell");
	return (
		<ScreenShell testId="load-failed-overlay" labelledBy="load-failed-title" describedBy="load-failed-description">
			<div className="bg-muted text-muted-foreground flex size-12 items-center justify-center rounded-sm">
				<WifiOff className="size-5" aria-hidden="true" />
			</div>
			<h1 id="load-failed-title" className="text-foreground mt-5 text-2xl font-semibold tracking-tight">
				{t("assetLoadFailed.screenTitle")}
			</h1>
			<p id="load-failed-description" className="text-muted-foreground mt-2 max-w-md text-sm leading-6">
				{t("assetLoadFailed.screenDescription")}
			</p>
			<div className="mt-7 flex justify-end">
				<Button size="sm" className="w-full sm:w-auto" data-testid="load-failed-reload-btn" onClick={() => window.location.reload()}>
					<RefreshCw aria-hidden="true" />
					{t("updating.reloadNow")}
				</Button>
			</div>
		</ScreenShell>
	);
}

function SkewScreenBody() {
	const cause = useSkewCause();
	if (cause === "upgrade") return <UpdatingScreenBody />;
	if (cause === "blocked") return <LoadFailedScreenBody />;
	return (
		<main className="bg-background flex min-h-dvh items-center justify-center" data-testid="load-failure-probing">
			<Loader2 className="text-muted-foreground size-5 motion-safe:animate-spin" aria-hidden="true" />
		</main>
	);
}

/** Full-page replacement for a route whose chunk failed: the upgrade screen only when the server confirms the chunk is gone. */
export function SkewScreen() {
	return (
		<I18nextProvider i18n={i18n}>
			<SkewScreenBody />
		</I18nextProvider>
	);
}