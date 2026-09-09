import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { X } from "lucide-react";
import React from "react";

type OmittedInputProps = Omit<React.InputHTMLAttributes<HTMLInputElement>, "value" | "onChange">;

interface TagInputProps extends OmittedInputProps {
	value: string[];
	onValueChange: (value: string[]) => void;
	// Height in px the tag area is fixed to; tags past it scroll inside it.
	// Fixing the height rather than a tag count is what keeps several of these
	// side by side the same size: tags wrap to different numbers of lines, so a
	// fixed count of tags is not a fixed amount of space.
	listHeight?: number;
	// Whether a comma commits the current entry alongside Enter. True for
	// values that cannot contain a comma (scopes, keys), where typing one
	// almost always means "next item". Set false where the tags are prose —
	// reference phrases read like sentences, and eating their commas turns
	// one phrase into two.
	submitOnComma?: boolean;
}

// Badge renders a single clipped line by default, which drops the tail of a
// sentence-length tag. TAG_CLASSES overrides that so a tag wraps inside the
// container rather than running past its edge.
const TAG_CLASSES = "bg-accent dark:bg-card flex max-w-full shrink items-center gap-1 text-left break-words whitespace-normal";

export const TagInput = React.forwardRef<HTMLInputElement, TagInputProps>(
	({ className, value, onValueChange, listHeight, submitOnComma = true, ...props }, ref) => {
		const [inputValue, setInputValue] = React.useState("");

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
			if (e.key === "Enter" || (submitOnComma && e.key === ",")) {
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

		const tags = value.map((tag) => (
			<Badge key={tag} variant="secondary" className={TAG_CLASSES}>
				{tag}
				<button
					aria-label={`Remove ${tag}`}
					type="button"
					className="ring-offset-background focus:ring-ring shrink-0 cursor-pointer rounded-sm outline-none focus:ring-2 focus:ring-offset-2"
					onClick={() => removeTag(tag)}
				>
					<X className="h-3 w-3" />
				</button>
			</Badge>
		));

		if (listHeight === undefined) {
			return (
				<div className={cn("border-input dark:bg-accent flex flex-wrap items-center gap-2 rounded-sm border p-1", className)}>
					{tags}
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
			<div className={cn("border-input dark:bg-accent rounded-sm border", className)}>
				{/* The tag area is its own scroll container, so tabbing onto a remove
				    button below the fold scrolls it into view natively -- no expand
				    state to keep the keyboard path visible. */}
				<div className="custom-scrollbar flex flex-wrap content-start items-start gap-2 overflow-y-auto p-2" style={{ height: listHeight }}>
					{tags}
				</div>

				{/* On a light card the default placeholder tint reads as disabled text,
				    so the entry row gets its own surface and a full-strength
				    placeholder to stay recognisable as somewhere you can type. */}
				<div className="bg-muted/40 dark:bg-accent border-border/30 border-t p-1">
					<Input
						ref={ref}
						type="text"
						value={inputValue}
						onChange={handleInputChange}
						onKeyDown={handleKeyDown}
						onBlur={handleBlur}
						className={cn(
							"placeholder:text-muted-foreground focus-visible:bg-background h-7 w-full min-w-0 rounded-sm border-0 bg-transparent py-0 px-2 text-xs shadow-none focus-visible:ring-0",
						)}
						{...props}
					/>
				</div>
			</div>
		);
	},
);

TagInput.displayName = "TagInput";