import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { X } from "lucide-react";
import React from "react";

type OmittedInputProps = Omit<React.InputHTMLAttributes<HTMLInputElement>, "value" | "onChange">;

interface TagInputProps extends OmittedInputProps {
	value: string[];
	onValueChange: (value: string[]) => void;
	// Height in px the tag area is clamped to before a "show all" toggle appears.
	// Clamping by height rather than by tag count is what keeps several of these
	// side by side the same size: tags wrap to different numbers of lines, so a
	// fixed count of tags is not a fixed amount of space.
	collapsedMaxHeight?: number;
	expandButtonTestId?: string;
}

// Badge renders a single clipped line by default, which drops the tail of a
// sentence-length tag. TAG_CLASSES overrides that so a tag wraps inside the
// container rather than running past its edge.
const TAG_CLASSES = "bg-accent dark:bg-card flex max-w-full shrink items-center gap-1 text-left break-words whitespace-normal";

export const TagInput = React.forwardRef<HTMLInputElement, TagInputProps>(
	({ className, value, onValueChange, collapsedMaxHeight, expandButtonTestId, ...props }, ref) => {
		const [inputValue, setInputValue] = React.useState("");
		const [tagsExpanded, setTagsExpanded] = React.useState(false);
		const [isOverflowing, setIsOverflowing] = React.useState(false);
		const tagsRef = React.useRef<HTMLDivElement>(null);

		const isCollapsed = isOverflowing && !tagsExpanded;

		// The toggle has to appear from the rendered height, not from the tag count:
		// how many tags fit depends on how long each one is and how wide the column
		// is, neither of which is known here.
		React.useLayoutEffect(() => {
			if (collapsedMaxHeight === undefined) {
				setIsOverflowing(false);
				return;
			}
			const element = tagsRef.current;
			if (!element) return;

			const measure = () => setIsOverflowing(element.scrollHeight > collapsedMaxHeight + 1);
			measure();

			const observer = new ResizeObserver(measure);
			observer.observe(element);
			return () => observer.disconnect();
		}, [collapsedMaxHeight, value]);

		React.useEffect(() => {
			if (!isOverflowing) setTagsExpanded(false);
		}, [isOverflowing]);

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

		if (collapsedMaxHeight === undefined) {
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
			<div className={cn("group border-input dark:bg-accent rounded-sm border", className)}>
				<div className="relative">
					<div
						ref={tagsRef}
						className="flex flex-wrap content-start items-start gap-2 overflow-hidden p-2"
						style={{ maxHeight: isCollapsed ? collapsedMaxHeight : undefined }}
						// Every tag carries a remove button, so tabbing through a collapsed list
						// moves focus onto buttons clipped by the height clamp — off-screen, with
						// no visible focus ring. Expanding on focus keeps the keyboard path in
						// view; onFocus bubbles from the descendant, which is what makes this work
						// without a listener per tag.
						onFocus={() => {
							if (isCollapsed) setTagsExpanded(true);
						}}
					>
						{tags}
					</div>
					{/* Fades the tag clipped by the height clamp, so the cut reads as
					    "there is more below" rather than as a rendering fault. */}
					{isCollapsed && (
						<div
							aria-hidden
							className="to-card dark:to-accent pointer-events-none absolute inset-x-0 bottom-0 h-10 bg-gradient-to-b from-transparent"
						/>
					)}
				</div>

				{isOverflowing && (
					<button
						type="button"
						data-testid={expandButtonTestId}
						aria-expanded={!isCollapsed}
						onClick={() => setTagsExpanded((expanded) => !expanded)}
						className="text-muted-foreground/70 hover:text-foreground/90 hover:bg-muted/15 border-border/30 w-full cursor-pointer border-t py-2 text-xs font-medium transition-colors"
					>
						{isCollapsed ? `Show all ${value.length}` : "Show less"}
					</button>
				)}

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