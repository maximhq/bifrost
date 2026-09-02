import { useTranslation } from "react-i18next";

export function NoChartData() {
	const { t } = useTranslation("observability");
	return <div className="text-muted-foreground flex h-full items-center justify-center text-sm">{t("labels.noData")}</div>;
}
