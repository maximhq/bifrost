import { ThemeToggle } from "./themeToggle";
import { Separator } from "./ui/separator";

export default function Header({ title }: { title: string }) {
	return (
		<div className="bg-background fixed top-0 right-0 left-(--sidebar-width) z-10">
			<div className="flex items-center justify-between px-3">
				<div className="p-3 font-semibold">{title}</div>
				<ThemeToggle />
			</div>
			<Separator className="w-full" />
		</div>
	);
}
