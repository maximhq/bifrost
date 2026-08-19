import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { X } from "lucide-react";
import React from "react";

type OmittedInputProps = Omit<React.InputHTMLAttributes<HTMLInputElement>, "value" | "onChange">;

interface TagInputProps extends OmittedInputProps {
	value: string[];
	onValueChange: (value: string[]) => void;
	collapsedTagLimit?: number;
	expandButtonTestId?: string;
}

export const TagInput = React.forwardRef<HTMLInputElement, TagInputProps>(
	({ className, value, onValueChange, collapsedTagLimit, expandButtonTestId, ...props }, ref) => {
		const [inputValue, setInputValue] = React.useState("");
		const [tagsExpanded, setTagsExpanded] = React.useState(false);

		const canCollapse = collapsedTagLimit !== undefined && value.length > collapsedTagLimit;
		const isCollapsed = canCollapse && !tagsExpanded;
		const visibleTags = isCollapsed ? value.slice(0, collapsedTagLimit) : value;
		const hiddenTagCount = canCollapse ? value.length - collapsedTagLimit : 0;

		React.useEffect(() => {
			if (collapsedTagLimit !== undefined && value.length <= collapsedTagLimit) {
				setTagsExpanded(false);
			}
		}, [collapsedTagLimit, value.length]);

		const handleInputChange = (e: React.ChangeEvent<HTMLInputElement>) => {
			setInputValue(e.target.value);
		};

		const addCurrentTag = () => {
			const newTag = inputValue.trim();
			if (newTag && !value.includes(newTag)) {
				onValueChange([...value, newTag]);
			}
			setInputValue("");
		};

		const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
			if (e.key === "Enter" || e.key === ",") {
				e.preventDefault();
				addCurrentTag();
			} else if (e.key === "Backspace" && inputValue === "" && value.length > 0) {
				onValueChange(value.slice(0, -1));
			}
		};

		const handleBlur = () => {
			addCurrentTag();
		};

		const removeTag = (tagToRemove: string) => {
			onValueChange(value.filter((tag) => tag !== tagToRemove));
		};

		if (collapsedTagLimit === undefined) {
			return (
				<div className={cn("border-input dark:bg-accent flex flex-wrap items-center gap-2 rounded-sm border p-1", className)}>
					{value.map((tag) => (
						<Badge key={tag} variant="secondary" className="bg-accent dark:bg-card flex items-center gap-1">
							{tag}
							<button
								aria-label={`Remove ${tag}`}
								type="button"
								className="ring-offset-background focus:ring-ring cursor-pointer rounded-sm outline-none focus:ring-2 focus:ring-offset-2"
								onClick={() => removeTag(tag)}
							>
								<X className="h-3 w-3" />
							</button>
						</Badge>
					))}
					<Input
						ref={ref}
						type="text"
						value={inputValue}
						onChange={handleInputChange}
						onKeyDown={handleKeyDown}
						onBlur={handleBlur}
						className={cn("dark:bg-accent h-7 min-w-32 flex-1 border-0 py-0 px-2 text-xs shadow-none focus-visible:ring-0")}
						{...props}
					/>
				</div>
			);
		}

		return (
			<div className={cn("group border-input dark:bg-accent relative overflow-hidden rounded-sm border", className)}>
				{isCollapsed && (
					<div
						aria-hidden
						className="from-accent/35 via-accent/75 to-muted/50 dark:from-accent/45 dark:via-accent/85 dark:to-muted/45 pointer-events-none absolute inset-x-0 top-8 bottom-0 z-[1] bg-gradient-to-b"
					/>
				)}

				<div className="relative z-[2] flex flex-wrap items-center gap-2 p-2 pb-1">
					{visibleTags.map((tag) => (
						<Badge key={tag} variant="secondary" className="bg-accent dark:bg-card flex items-center gap-1">
							{tag}
							<button
								aria-label={`Remove ${tag}`}
								type="button"
								className="ring-offset-background focus:ring-ring cursor-pointer rounded-sm outline-none focus:ring-2 focus:ring-offset-2"
								onClick={() => removeTag(tag)}
							>
								<X className="h-3 w-3" />
							</button>
						</Badge>
					))}
				</div>

				<div
					className={cn(
						"relative z-[2] transition-[background-color,opacity] duration-200 h-[75px] flex items-end",
						isCollapsed &&
							"bg-muted/25 opacity-80 group-hover:opacity-100 group-hover:bg-muted/20 group-focus-within:opacity-100 group-focus-within:bg-muted/20",
					)}
				>
					{isCollapsed && (
						<button
							type="button"
							data-testid={expandButtonTestId}
							onClick={() => setTagsExpanded(true)}
							className="text-muted-foreground/70 hover:text-foreground/90 group-hover:text-muted-foreground/85 flex w-full cursor-pointer items-center justify-center py-2.5 text-xs font-medium transition-colors absolute top-4"
						>
							Show {hiddenTagCount} more
						</button>
					)}

					<div
						className={cn(
							"p-1 transition-colors duration-200 w-full",
							(isCollapsed || (canCollapse && tagsExpanded)) && "border-border/30 border-t",
							isCollapsed && "border-transparent group-hover:border-border/30 group-focus-within:border-border/30",
						)}
					>
						<Input
							ref={ref}
							type="text"
							value={inputValue}
							onChange={handleInputChange}
							onKeyDown={handleKeyDown}
							onBlur={handleBlur}
							className={cn(
								"dark:bg-accent h-7 w-full min-w-0 rounded-sm border-0 py-0 px-2 text-xs shadow-none transition-[background-color,color,opacity] duration-200 focus-visible:ring-0",
								isCollapsed
									? "text-muted-foreground/25 placeholder:text-muted-foreground/15 bg-transparent opacity-70 group-hover:bg-background/70 group-hover:text-foreground/80 group-hover:placeholder:text-muted-foreground/60 group-hover:opacity-100 group-focus-within:bg-background group-focus-within:text-foreground group-focus-within:placeholder:text-muted-foreground/70 group-focus-within:opacity-100 focus-visible:bg-background focus-visible:text-foreground focus-visible:opacity-100"
									: undefined,
							)}
							{...props}
						/>
					</div>
				</div>

				{canCollapse && tagsExpanded && (
					<button
						type="button"
						onClick={() => setTagsExpanded(false)}
						className="text-muted-foreground/50 hover:text-muted-foreground/80 hover:bg-muted/15 border-border/40 bg-muted/10 relative z-[2] w-full cursor-pointer border-t py-2 text-xs transition-[color,background-color]"
					>
						Show less
					</button>
				)}
			</div>
		);
	},
);

TagInput.displayName = "TagInput";