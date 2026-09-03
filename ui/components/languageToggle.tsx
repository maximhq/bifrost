import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { LOCALES, LOCALE_META, useI18n, type Locale } from "@/lib/i18n";
import { Check, Languages } from "lucide-react";

/**
 * Language choices as bare dropdown items so they can be embedded in a larger
 * menu (e.g. the topbar account menu) rather than only in their own popover.
 */
export function LanguageToggleItems() {
	const { locale, setLocale } = useI18n();

	return (
		<>
			{LOCALES.map((value: Locale) => (
				<DropdownMenuItem key={value} onClick={() => setLocale(value)} className="cursor-pointer" data-testid={`language-option-${value}`}>
					<span className="flex-1">{LOCALE_META[value].nativeLabel}</span>
					{locale === value && <Check className="text-muted-foreground size-3.5" strokeWidth={2.5} />}
				</DropdownMenuItem>
			))}
		</>
	);
}

export function LanguageToggle() {
	const { t } = useI18n();

	return (
		<DropdownMenu>
			<DropdownMenuTrigger asChild>
				<Button
					variant="ghost"
					size="icon"
					data-testid="language-toggle-btn"
					className="text-muted-foreground hover:bg-accent hover:text-accent-foreground data-[state=open]:bg-card data-[state=open]:text-accent-foreground size-8 border-0 ring-offset-0 outline-none select-none focus-visible:ring-0 data-[state=open]:border"
				>
					<Languages className="size-4" strokeWidth={2} />
					<span className="sr-only">{t("language.toggle")}</span>
				</Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent align="end" sideOffset={2}>
				<LanguageToggleItems />
			</DropdownMenuContent>
		</DropdownMenu>
	);
}