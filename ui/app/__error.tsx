import { Button } from "@/components/ui/button";
import i18n from "@/lib/i18n";
import { isSkewError, reportSkew } from "@/lib/utils/versionSkew";
import type { ErrorComponentProps } from "@tanstack/react-router";
import { useEffect } from "react";
import { I18nextProvider, useTranslation } from "react-i18next";
import { UpdatingScreen } from "./__updating";

function ErrorBody({ error }: Partial<ErrorComponentProps>) {
	const { t } = useTranslation("shell");
	const skew = isSkewError(error);

	// Notifying skew subscribers is a store write, so keep it out of render.
	useEffect(() => {
		if (skew) reportSkew("hard");
	}, [skew]);

	if (skew) {
		return <UpdatingScreen />;
	}

	return (
		<main className="h-base flex items-center justify-center p-6">
			<div className="mx-auto w-full max-w-md text-center">
				<p className="text-foreground text-7xl font-bold tracking-tight">500</p>
				<h1 className="text-foreground mt-4 text-2xl font-semibold">{t("error.title")}</h1>
				<p className="text-muted-foreground mt-2 text-sm">{t("error.description")}</p>
				<div className="mt-6 flex items-center justify-center gap-3">
					<Button size={"sm"} data-testid="error-reload-btn" onClick={() => window.location.reload()}>
						{t("error.reload")}
					</Button>
				</div>
			</div>
		</main>
	);
}

export function ErrorComponent({ error }: Partial<ErrorComponentProps>) {
	return (
		<I18nextProvider i18n={i18n}>
			<ErrorBody error={error} />
		</I18nextProvider>
	);
}
