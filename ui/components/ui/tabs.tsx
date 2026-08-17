import * as TabsPrimitive from "@radix-ui/react-tabs";
import { ChevronDown } from "lucide-react";
import * as React from "react";

import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { partitionTabs } from "@/components/ui/tabs.utils";
import { cn } from "@/lib/utils";

/**
 * The selected value plus a setter, so <TabsList> can decide which trigger must
 * stay on screen and can still switch tabs from the overflow dropdown. Radix
 * keeps this in a private context, so we mirror it on the way through.
 */
const TabsValueContext = React.createContext<{ value?: string; setValue: (next: string) => void }>({ setValue: () => {} });

/** Room reserved for the overflow dropdown trigger once it is shown. */
const MORE_BUTTON_WIDTH = 40;

function Tabs({ className, value, defaultValue, onValueChange, ...props }: React.ComponentProps<typeof TabsPrimitive.Root>) {
	// Track the uncontrolled case ourselves; the controlled case just reads `value`.
	const [uncontrolled, setUncontrolled] = React.useState(defaultValue);
	const current = value ?? uncontrolled;

	const setValue = React.useCallback(
		(next: string) => {
			if (value === undefined) setUncontrolled(next);
			onValueChange?.(next);
		},
		[value, onValueChange],
	);

	const context = React.useMemo(() => ({ value: current, setValue }), [current, setValue]);

	return (
		<TabsValueContext.Provider value={context}>
			<TabsPrimitive.Root
				data-slot="tabs"
				className={cn("flex flex-col gap-2", className)}
				value={value}
				defaultValue={defaultValue}
				onValueChange={setValue}
				{...props}
			/>
		</TabsValueContext.Provider>
	);
}

type TabsTriggerElement = React.ReactElement<{ value?: string; children?: React.ReactNode }>;

/**
 * Renders the triggers that fit on one row and collapses the rest into a
 * trailing dropdown.
 *
 * The alternative - a horizontally scrolling strip - hides tabs behind an
 * affordance most people never find, and inside a sheet a trackpad's horizontal
 * gesture fights the sheet's own scrolling. Strips that already fit are
 * untouched: partitionTabs returns everything inline and no dropdown renders.
 */
function TabsList({ className, children, ...props }: React.ComponentProps<typeof TabsPrimitive.List>) {
	const items = React.useMemo(
		() => React.Children.toArray(children).filter((child) => React.isValidElement(child)) as TabsTriggerElement[],
		[children],
	);
	const { value: activeValue, setValue } = React.useContext(TabsValueContext);
	const activeIndex = items.findIndex((item) => item.props.value === activeValue);

	const listRef = React.useRef<HTMLDivElement>(null);
	const [containerWidth, setContainerWidth] = React.useState(0);
	// Widths are captured on the first pass, while every trigger is still inline;
	// a trigger moved into the dropdown reports 0 and could not be re-measured.
	// `count` invalidates the cache when the set of tabs changes.
	const [metrics, setMetrics] = React.useState<{ count: number; widths: number[] }>({ count: -1, widths: [] });
	const measured = metrics.count === items.length;

	React.useLayoutEffect(() => {
		const list = listRef.current;
		if (measured || !list) return;
		const widths = Array.from(list.children).map((child) => (child as HTMLElement).offsetWidth);
		if (widths.length === items.length && widths.every((width) => width > 0)) {
			setMetrics({ count: items.length, widths });
		}
	}, [measured, items.length]);

	// Measure the PARENT, never the list. TabsList is `w-fit inline-flex` by
	// default and consumers often set `w-max`, so the list's own width is just the
	// sum of its children - comparing against it would always report "everything
	// fits" and the dropdown would never appear. The parent is what actually
	// constrains the row. Measuring the parent also avoids a feedback loop:
	// hiding a tab shrinks the list, which would re-trigger a list-based observer.
	React.useEffect(() => {
		const list = listRef.current;
		if (!list || typeof ResizeObserver === "undefined") return;
		const parent = list.parentElement;
		const target = parent ?? list;

		const measure = () => {
			const horizontalPadding = (element: HTMLElement) => {
				const style = window.getComputedStyle(element);
				return (Number.parseFloat(style.paddingLeft) || 0) + (Number.parseFloat(style.paddingRight) || 0);
			};
			// clientWidth excludes border and scrollbar but includes padding, and the
			// list's own padding is not available to its children either.
			const outer = parent ? parent.clientWidth - horizontalPadding(parent) : list.clientWidth;
			setContainerWidth(Math.max(0, outer - horizontalPadding(list)));
		};

		measure();
		const observer = new ResizeObserver(measure);
		observer.observe(target);
		return () => observer.disconnect();
	}, []);

	const { visible, overflow } = measured
		? partitionTabs({ itemWidths: metrics.widths, containerWidth, moreButtonWidth: MORE_BUTTON_WIDTH, activeIndex })
		: { visible: items.map((_, index) => index), overflow: [] as number[] };

	return (
		<TabsPrimitive.List
			ref={listRef}
			data-slot="tabs-list"
			className={cn("bg-muted text-muted-foreground inline-flex h-9 w-fit items-center justify-center rounded-lg p-[3px]", className)}
			{...props}
		>
			{visible.map((index) => items[index])}
			{overflow.length > 0 && (
				<DropdownMenu>
					<DropdownMenuTrigger asChild>
						<button
							type="button"
							data-testid="tabs-overflow-trigger"
							aria-label={`Show ${overflow.length} more ${overflow.length === 1 ? "tab" : "tabs"}`}
							className="text-foreground hover:bg-background/60 focus-visible:ring-ring/50 flex h-[calc(100%-1px)] shrink-0 cursor-pointer items-center gap-0.5 rounded-sm border border-transparent px-2 text-sm font-medium focus-visible:ring-[3px] focus-visible:outline-1"
						>
							{overflow.length}
							<ChevronDown className="size-3.5" />
						</button>
					</DropdownMenuTrigger>
					<DropdownMenuContent align="end">
						{overflow.map((index) => {
							const item = items[index];
							const itemValue = item.props.value;
							return (
								<DropdownMenuItem
									key={itemValue ?? index}
									className="cursor-pointer"
									data-state={itemValue === activeValue ? "active" : "inactive"}
									onSelect={() => itemValue !== undefined && setValue(itemValue)}
								>
									{item.props.children}
								</DropdownMenuItem>
							);
						})}
					</DropdownMenuContent>
				</DropdownMenu>
			)}
		</TabsPrimitive.List>
	);
}

function TabsTrigger({ className, ...props }: React.ComponentProps<typeof TabsPrimitive.Trigger>) {
	return (
		<TabsPrimitive.Trigger
			data-slot="tabs-trigger"
			className={cn(
				"dark:data-[state=active]:text-foreground focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:outline-ring dark:data-[state=active]:border-input dark:data-[state=active]:bg-input/30 text-foreground dark:text-muted-foreground inline-flex h-[calc(100%-1px)] flex-1 cursor-pointer items-center justify-center gap-1.5 rounded-sm border border-transparent px-2 py-1 text-sm font-medium whitespace-nowrap transition-[color,box-shadow] focus-visible:ring-[3px] focus-visible:outline-1 disabled:pointer-events-none disabled:opacity-50 data-[state=active]:bg-white data-[state=active]:shadow-sm [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
				className,
			)}
			{...props}
		/>
	);
}

function TabsContent({ className, ...props }: React.ComponentProps<typeof TabsPrimitive.Content>) {
	return <TabsPrimitive.Content data-slot="tabs-content" className={cn("flex-1 outline-none", className)} {...props} />;
}

export { Tabs, TabsContent, TabsList, TabsTrigger };