import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { cn } from "@/lib/utils";
import { ChevronDown } from "lucide-react";

const AVAILABLE_ROLES = [
	{ value: "system", label: "System" },
	{ value: "user", label: "User" },
	{ value: "assistant", label: "Assistant" },
	{ value: "tool", label: "Tool" },
] as const;

export default function MessageRoleSwitcher({
	role,
	disabled,
	onRoleChange,
	restrictedRoles,
}: {
	role: string;
	disabled?: boolean;
	onRoleChange: (role: string) => void;
	restrictedRoles?: (typeof AVAILABLE_ROLES)[number]["value"][];
}) {
	return (
		<DropdownMenu>
			<DropdownMenuTrigger asChild disabled={disabled}>
				<button
					className={cn(
						"-ml-1.5 flex items-center gap-1 rounded-sm px-1.5 py-0.5 text-xs font-medium uppercase",
						!disabled && "hover:bg-muted cursor-pointer",
					)}
				>
					{role}
					<ChevronDown className="size-3 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100" />
				</button>
			</DropdownMenuTrigger>
			<DropdownMenuContent align="start">
				{AVAILABLE_ROLES.filter((r) => r.value !== role && (!restrictedRoles || !restrictedRoles.includes(r.value))).map((option) => (
					<DropdownMenuItem key={option.value} onSelect={() => onRoleChange(option.value)}>
						{option.label.toUpperCase()}
					</DropdownMenuItem>
				))}
			</DropdownMenuContent>
		</DropdownMenu>
	);
}
