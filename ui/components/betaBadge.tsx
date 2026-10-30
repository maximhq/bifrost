import { useTranslation } from "react-i18next";
import { Badge } from "./ui/badge";

export default function BetaBadge() {
	const { t } = useTranslation("common");
	return <Badge variant="secondary">{t("badges.beta")}</Badge>;
}