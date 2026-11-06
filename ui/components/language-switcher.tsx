import { Button } from "@/components/ui/button";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuRadioGroup,
	DropdownMenuRadioItem,
	DropdownMenuSub,
	DropdownMenuSubContent,
	DropdownMenuSubTrigger,
	DropdownMenuTrigger,
} from "@/components/ui/dropdownMenu";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { changeLocale, getLocale, SUPPORTED_LOCALES, type SupportedLocale } from "@/lib/i18n";
import { Check, Languages } from "lucide-react";
import { useTranslation } from "react-i18next";

/**
 * Compact icon-button language picker for pre-auth screens (login), where
 * Client Settings is not reachable. Mirrors ThemeToggle layout.
 */
export function LanguageSwitcher() {
	const { t, i18n } = useTranslation("common");
	const current = (i18n.resolvedLanguage ?? getLocale()) as SupportedLocale;

	const handleSelect = (locale: SupportedLocale) => {
		if (locale === current) return;
		void changeLocale(locale);
	};

	return (
		<DropdownMenu>
			<DropdownMenuTrigger asChild>
				<Button
					variant="ghost"
					size="icon"
					aria-label={t("language")}
					data-testid="language-switcher"
					className="text-muted-foreground hover:bg-accent hover:text-accent-foreground data-[state=open]:bg-card data-[state=open]:text-accent-foreground size-8 border-0 ring-offset-0 outline-none select-none focus-visible:ring-0 data-[state=open]:border"
				>
					<Languages className="size-4" strokeWidth={2} />
					<span className="sr-only">{t("language")}</span>
				</Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent align="end" sideOffset={2}>
				{SUPPORTED_LOCALES.map(({ code, label }) => (
					<DropdownMenuItem
						key={code}
						onClick={() => handleSelect(code)}
						className="cursor-pointer"
						data-testid={`language-option-${code}`}
					>
						<span className="flex-1">{label}</span>
						{current === code && <Check className="text-muted-foreground size-3.5" strokeWidth={2.5} />}
					</DropdownMenuItem>
				))}
			</DropdownMenuContent>
		</DropdownMenu>
	);
}

/**
 * Language submenu for the topbar account menu. Client Settings sits behind
 * Settings View, so this keeps the preference reachable for roles without it.
 */
export function LanguageMenuSub() {
	const { t, i18n } = useTranslation("common");
	const current = (i18n.resolvedLanguage ?? getLocale()) as SupportedLocale;

	const handleChange = (value: string) => {
		if (value === current) return;
		void changeLocale(value as SupportedLocale);
	};

	return (
		<DropdownMenuSub>
			<DropdownMenuSubTrigger data-testid="topbar-language-submenu" className="cursor-pointer gap-2">
				<Languages className="size-4" strokeWidth={2} />
				<span>{t("language")}</span>
			</DropdownMenuSubTrigger>
			<DropdownMenuSubContent>
				<DropdownMenuRadioGroup value={current} onValueChange={handleChange}>
					{SUPPORTED_LOCALES.map(({ code, label }) => (
						<DropdownMenuRadioItem key={code} value={code} data-testid={`topbar-language-option-${code}`} className="cursor-pointer">
							{label}
						</DropdownMenuRadioItem>
					))}
				</DropdownMenuRadioGroup>
			</DropdownMenuSubContent>
		</DropdownMenuSub>
	);
}

/**
 * Form-style language picker for Client Settings. The choice is a per-browser
 * preference (persisted to localStorage by the i18n detector), so it applies
 * immediately and is independent of the page's Save button and RBAC.
 */
export function LanguageSelect({ id }: { id?: string }) {
	const { i18n } = useTranslation("common");
	const current = (i18n.resolvedLanguage ?? getLocale()) as SupportedLocale;

	const handleChange = (value: string) => {
		if (value === current) return;
		void changeLocale(value as SupportedLocale);
	};

	return (
		<Select value={current} onValueChange={handleChange}>
			<SelectTrigger id={id} data-testid="client-settings-language-select" className="w-full shrink-0 sm:w-[180px]">
				<SelectValue />
			</SelectTrigger>
			<SelectContent>
				{SUPPORTED_LOCALES.map(({ code, label }) => (
					<SelectItem key={code} value={code} data-testid={`client-settings-language-option-${code}`}>
						{label}
					</SelectItem>
				))}
			</SelectContent>
		</Select>
	);
}