import { Button } from "@/components/ui/button";
import { getCachedBrandingAssets } from "@/lib/hooks/useBranding";
import i18n from "@/lib/i18n";
import { getEndpointUrl } from "@/lib/utils/port";
import { consumeAutoReload, startVersionPoll } from "@/lib/utils/versionSkew";
import { Loader2, RefreshCw, Sparkles } from "lucide-react";
import { useEffect, useState } from "react";
import { I18nextProvider, useTranslation } from "react-i18next";

function UpdatingBannerBody() {
	const { t } = useTranslation("shell");
	return (
		<div
			className="pointer-events-none fixed inset-x-0 bottom-4 z-[100] flex justify-center px-4 sm:bottom-6"
			data-testid="updating-banner"
			role="status"
			aria-live="polite"
		>
			<div className="bg-card/95 pointer-events-auto flex w-full max-w-xl items-center gap-3 rounded-sm border p-2.5 pr-3 backdrop-blur sm:gap-4 sm:p-3">
				<div className="bg-primary/10 text-primary relative flex size-9 shrink-0 items-center justify-center rounded-sm">
					<span className="bg-primary/20 absolute size-2.5 rounded-full motion-safe:animate-ping" aria-hidden="true" />
					<span className="bg-primary relative size-2 rounded-full" aria-hidden="true" />
				</div>
				<div className="min-w-0 flex-1">
					<p className="text-foreground text-sm font-medium">{t("updating.bannerTitle")}</p>
					<p className="text-muted-foreground truncate text-xs">{t("updating.bannerDescription")}</p>
				</div>
				<Button
					variant="outline"
					size="sm"
					className="shrink-0"
					data-testid="updating-banner-reload-btn"
					onClick={() => window.location.reload()}
				>
					<RefreshCw aria-hidden="true" />
					<span className="hidden sm:inline">{t("updating.reload")}</span>
				</Button>
			</div>
		</div>
	);
}

export function UpdatingBanner() {
	return (
		<I18nextProvider i18n={i18n}>
			<UpdatingBannerBody />
		</I18nextProvider>
	);
}

function UpdatingScreenBody() {
	const { t } = useTranslation("shell");
	// Rendered above <ReduxProvider> (main.tsx) and as the router error component,
	// so branding has to come from the cache rather than a query.
	const [{ logoSrc, logoAlt }] = useState(() => getCachedBrandingAssets(false));
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
		<main
			className="bg-background relative isolate flex min-h-dvh items-center justify-center overflow-hidden p-4 sm:p-6"
			data-testid="updating-overlay"
		>
			<div
				className="bg-primary/6 pointer-events-none absolute inset-0 -z-10 [mask-image:radial-gradient(circle_at_center,black,transparent_68%)]"
				aria-hidden="true"
			/>
			<section
				className="bg-card w-full max-w-lg overflow-hidden rounded-sm border"
				aria-labelledby="updating-title"
				aria-describedby="updating-description"
				aria-live="polite"
			>
				<div className="flex items-center border-b px-5 py-4 sm:px-6">
					<img src={logoSrc} alt={logoAlt} className="h-5 w-auto max-w-[180px] object-contain" />
				</div>

				<div className="px-5 py-8 sm:px-8 sm:py-10">
					<div className="bg-primary/10 text-primary flex size-12 items-center justify-center rounded-sm">
						<Sparkles className="size-5" aria-hidden="true" />
					</div>
					<h1 id="updating-title" className="text-foreground mt-5 text-2xl font-semibold tracking-tight">
						{t("updating.screenTitle")}
					</h1>
					<p id="updating-description" className="text-muted-foreground mt-2 max-w-md text-sm leading-6">
						{autoReloadExhausted ? t("updating.descriptionStuck") : t("updating.descriptionWaiting")}
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
									<span>{t("updating.waiting")}</span>
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
				</div>
			</section>
		</main>
	);
}

export function UpdatingScreen() {
	return (
		<I18nextProvider i18n={i18n}>
			<UpdatingScreenBody />
		</I18nextProvider>
	);
}
