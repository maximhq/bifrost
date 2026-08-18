import { Check, Laptop, Moon, Sun } from "lucide-react";
import { useTheme } from "next-themes";

import { TOPBAR_MENU_SIDE_OFFSET } from "@/components/topbar.utils";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";

const THEMES = [
	{ value: "light", label: "Light", icon: Sun },
	{ value: "dark", label: "Dark", icon: Moon },
	{ value: "system", label: "System", icon: Laptop },
] as const;

/**
 * The theme choices as bare dropdown items, so they can be embedded in a larger
 * menu (e.g. the topbar account menu) rather than only in their own popover.
 */
export function ThemeToggleItems() {
	const { theme, setTheme } = useTheme();

	return (
		<>
			{THEMES.map(({ value, label, icon: Icon }) => (
				<DropdownMenuItem key={value} onClick={() => setTheme(value)} className="cursor-pointer">
					<Icon className="size-4" strokeWidth={2} />
					<span className="flex-1">{label}</span>
					{theme === value && <Check className="text-muted-foreground size-3.5" strokeWidth={2.5} />}
				</DropdownMenuItem>
			))}
		</>
	);
}

export function ThemeToggle() {
	return (
		<DropdownMenu>
			<DropdownMenuTrigger asChild>
				<Button
					variant="ghost"
					size="icon"
					// size-8 box around a size-4 glyph, matching the notification and menu triggers. Equal boxes are
					// what keep the topbar icons evenly spaced and open their menus on the same line.
					className="text-muted-foreground hover:bg-accent hover:text-accent-foreground size-8 border-0 ring-offset-0 outline-none select-none focus-visible:ring-0"
				>
					<Sun className="size-4 scale-100 rotate-0 transition-all dark:scale-0 dark:-rotate-90" strokeWidth={2} />
					<Moon className="absolute size-4 scale-0 rotate-90 transition-all dark:scale-100 dark:rotate-0" strokeWidth={2} />
					<span className="sr-only">Toggle theme</span>
				</Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent align="end" sideOffset={TOPBAR_MENU_SIDE_OFFSET}>
				<ThemeToggleItems />
			</DropdownMenuContent>
		</DropdownMenu>
	);
}