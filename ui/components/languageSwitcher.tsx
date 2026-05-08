import { useTranslation } from "react-i18next";
import { Languages } from "lucide-react";

const languages = [
	{ code: "en", label: "English" },
	{ code: "zh", label: "中文" },
] as const;

export function LanguageSwitcher({ className }: { className?: string }) {
	const { i18n } = useTranslation();

	const toggleLanguage = () => {
		const next = i18n.language === "zh" ? "en" : "zh";
		i18n.changeLanguage(next);
	};

	return (
		<button
			onClick={toggleLanguage}
			className={`hover:text-primary text-muted-foreground flex cursor-pointer items-center space-x-3 p-0.5 ${className ?? ""}`}
			type="button"
			aria-label={`Switch language (${languages.find((l) => l.code !== i18n.language)?.label})`}
			title={languages.find((l) => l.code !== i18n.language)?.label}
		>
			<Languages className="h-4 w-4" size={20} strokeWidth={2} />
		</button>
	);
}