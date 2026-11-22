import { Button } from "@/components/ui/button";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuRadioGroup,
	DropdownMenuRadioItem,
	DropdownMenuTrigger,
} from "@/components/ui/dropdownMenu";
import { matchWarpCommands, resolveWarpCommand, type WarpCommand } from "@/components/warp/warpCommands";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import type { WarpModel } from "@/lib/types/warp";
import { cn } from "@/lib/utils";
import { Link } from "@tanstack/react-router";
import { ArrowUp, ChevronDown, Settings2, Square } from "lucide-react";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import TextareaAutosize from "react-textarea-autosize";
import { hasProviderIcon, warpModelKey, warpModelLabel } from "./warpComposer.utils";
import { WarpTrayActiveContext } from "./warpTrayContext";

interface WarpComposerProps {
	onCommand?: (command: WarpCommand) => void;
	isStreaming: boolean;
	disabled?: boolean;
	/** A card that grows out of the top of the input box, such as a question awaiting an answer. */
	tray?: ReactNode;
	/** Every model the operator exposed, the default first. */
	models: WarpModel[];
	/** The model the next question runs on. */
	selectedModel?: WarpModel;
	onSelectModel: (model: WarpModel) => void;
	/** Offers the link to Warp's settings. Only for callers who may change them. */
	canConfigure?: boolean;
	onSend: (question: string) => void;
	/** Holds a message submitted mid-answer until it finishes; without it the submit is dropped. */
	onQueue?: (question: string) => void;
	onStop: () => void;
}

export default function WarpComposer({
	isStreaming,
	disabled,
	tray,
	models,
	selectedModel,
	onSelectModel,
	canConfigure,
	onCommand,
	onSend,
	onQueue,
	onStop,
}: WarpComposerProps) {
	const { t } = useTranslation("shell");
	const [value, setValue] = useState("");
	const [highlighted, setHighlighted] = useState(0);

	const commands = onCommand ? matchWarpCommands(value) : [];
	const menuOpen = commands.length > 0;

	const runCommand = (command: WarpCommand) => {
		onCommand?.(command);
		setValue("");
		setHighlighted(0);
	};

	const submit = () => {
		const text = value.trim();
		if (!text || disabled) return;

		// Only with a handler: otherwise the input would clear and nothing would run.
		const command = onCommand ? resolveWarpCommand(text) : undefined;
		if (command) {
			runCommand(command);
			return;
		}
		if (isStreaming) {
			if (!onQueue) return;
			onQueue(text);
			setValue("");
			return;
		}
		onSend(text);
		setValue("");
	};

	return (
		<div className="shrink-0 px-3 pt-3 pb-3">
			{menuOpen && (
				<div className="bg-popover mb-2 overflow-hidden rounded-md border shadow-sm" data-testid="warp-command-menu">
					{commands.map((command, index) => (
						<button
							key={command.id}
							type="button"
							onMouseEnter={() => setHighlighted(index)}
							onClick={() => runCommand(command)}
							data-testid={`warp-command-${command.id}`}
							className={cn(
								"flex w-full cursor-pointer items-baseline gap-2 px-3 py-2 text-left text-sm transition-colors",
								index === highlighted ? "bg-accent" : "hover:bg-accent/50",
							)}
						>
							<span className="font-mono text-xs">/{command.name}</span>
							<span className="text-muted-foreground truncate text-xs font-normal">{t(command.descriptionKey)}</span>
						</button>
					))}
				</div>
			)}
			{/* One shell for the tray and the input, so a card opens out of the input box and folds back into it. */}
			<div className="has-[textarea:focus]:border-ring bg-background dark:bg-card flex flex-col overflow-hidden rounded-lg border transition-colors">
				<WarpComposerTray tray={tray} />
				<div className="flex flex-col gap-2 p-2">
					<TextareaAutosize
						value={value}
						onChange={(event) => setValue(event.target.value)}
						onKeyDown={(event) => {
							// IME commits with Enter; keyCode 229 covers browsers without isComposing.
							if (event.nativeEvent.isComposing || event.keyCode === 229) return;
							if (menuOpen) {
								if (event.key === "ArrowDown") {
									event.preventDefault();
									setHighlighted((current) => (current + 1) % commands.length);
									return;
								}
								if (event.key === "ArrowUp") {
									event.preventDefault();
									setHighlighted((current) => (current - 1 + commands.length) % commands.length);
									return;
								}
								if (event.key === "Escape") {
									event.preventDefault();
									setValue("");
									return;
								}
								if (event.key === "Enter" && !event.shiftKey) {
									event.preventDefault();
									runCommand(commands[Math.min(highlighted, commands.length - 1)]);
									return;
								}
							}
							if (event.key === "Enter" && !event.shiftKey) {
								event.preventDefault();
								submit();
							}
						}}
						placeholder={isStreaming && onQueue ? t("warp.composer.followUp") : t("warp.composer.placeholder")}
						disabled={disabled}
						minRows={1}
						maxRows={8}
						data-testid="warp-composer-input"
						className="placeholder:text-muted-foreground max-h-48 w-full resize-none bg-transparent px-1 text-sm outline-none disabled:opacity-50"
					/>
					<div className="flex items-center justify-between gap-2">
						<div className="flex min-w-0 items-center gap-0.5">
							<WarpModelSwitcher models={models} selectedModel={selectedModel} onSelectModel={onSelectModel} />
							{canConfigure && (
								<Link
									to="/workspace/config/warp"
									aria-label={t("warp.composer.settings")}
									title={t("warp.composer.settings")}
									className="text-muted-foreground hover:text-foreground hover:bg-accent flex size-5 shrink-0 items-center justify-center rounded transition-colors"
									data-testid="warp-composer-settings"
								>
									<Settings2 className="size-3" />
								</Link>
							)}
						</div>
						{isStreaming ? (
							<Button
								type="button"
								size="icon"
								variant="secondary"
								onClick={onStop}
								aria-label={t("warp.composer.stop")}
								data-testid="warp-stop-btn"
								className="size-7 shrink-0 rounded-full"
							>
								<Square className="size-3" />
							</Button>
						) : (
							<Button
								type="button"
								size="icon"
								onClick={submit}
								disabled={!value.trim() || disabled}
								aria-label={t("warp.composer.send")}
								data-testid="warp-send-btn"
								className="size-7 shrink-0 rounded-full"
							>
								<ArrowUp className="size-3.5" />
							</Button>
						)}
					</div>
				</div>
			</div>
		</div>
	);
}

// Matches the tray's close transition, after which the closed card unmounts.
const TRAY_CLOSE_MS = 180;
const SPRING = "var(--warp-spring, cubic-bezier(0.165, 0.84, 0.44, 1))";
// Folding back in accelerates out of rest and eases into the input, so it reads as drawn back rather than dropped.
const FOLD = "cubic-bezier(0.645, 0.045, 0.355, 1)";

/**
 * The top of the input box. It opens by springing its measured height up from zero, so the card rises out of
 * the input with the same overshoot as a sent message, and on close keeps the last card rendered while it folds
 * back down. A pixel height, not grid rows: an fr track caps at its content, which flattens the spring's overshoot.
 */
function WarpComposerTray({ tray }: { tray?: ReactNode }) {
	const hasTray = !!tray;
	const kept = useRef<ReactNode>(tray);
	if (tray) kept.current = tray;
	const [mounted, setMounted] = useState(hasTray);
	const [open, setOpen] = useState(hasTray);
	const [height, setHeight] = useState(0);
	const [content, setContent] = useState<HTMLDivElement | null>(null);
	// Read here because the inline transitions below outrank a motion-reduce class.
	const still = typeof window !== "undefined" && !!window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;

	// Follows the card as it changes size, such as a confirm card's rows landing, so the frame never clips it.
	useEffect(() => {
		if (!content) return;
		setHeight(content.offsetHeight);
		const observer = new ResizeObserver(() => setHeight(content.offsetHeight));
		observer.observe(content);
		return () => observer.disconnect();
	}, [content]);

	useEffect(() => {
		if (hasTray) {
			setMounted(true);
			// Two frames: the first paints the closed tray, so the second has something to transition from.
			let inner = 0;
			const outer = requestAnimationFrame(() => {
				inner = requestAnimationFrame(() => setOpen(true));
			});
			return () => {
				cancelAnimationFrame(outer);
				cancelAnimationFrame(inner);
			};
		}
		setOpen(false);
		const timer = setTimeout(() => setMounted(false), TRAY_CLOSE_MS);
		return () => clearTimeout(timer);
	}, [hasTray]);

	if (!mounted) return null;
	return (
		<div
			className="overflow-hidden"
			style={{
				height: open ? height : 0,
				transition: still ? "none" : open ? `height 380ms ${SPRING}` : `height ${TRAY_CLOSE_MS}ms ${FOLD}`,
			}}
			inert={!open}
			data-testid="warp-composer-tray"
		>
			<div
				ref={setContent}
				className={cn(
					// A lighter step of the input's grey: set apart from the white panel, still one unit with the input.
					"bg-background/50 dark:bg-muted/20 border-b",
					open ? "translate-y-0 opacity-100" : "translate-y-2 opacity-0",
				)}
				style={{
					// In: rises on the spring a beat behind the frame. Out: fades first, so the fold never crushes visible text.
					transition: still
						? "none"
						: open
							? `opacity 180ms ease-out 30ms, translate 380ms ${SPRING} 20ms`
							: "opacity 90ms ease-in, translate 150ms ease-in",
				}}
			>
				<WarpTrayActiveContext.Provider value={hasTray}>{tray ?? kept.current}</WarpTrayActiveContext.Provider>
			</div>
		</div>
	);
}

function WarpModelName({ model }: { model: WarpModel }) {
	return (
		<>
			{hasProviderIcon(model.provider) && (
				<RenderProviderIcon provider={model.provider as ProviderIconType} size="xs" className="size-3.5 shrink-0" />
			)}
			<span className="truncate">{warpModelLabel(model.provider, model.model)}</span>
		</>
	);
}

interface WarpModelSwitcherProps {
	models: WarpModel[];
	selectedModel?: WarpModel;
	onSelectModel: (model: WarpModel) => void;
}

/**
 * Names the model the next question runs on, and switches between the models
 * the operator exposed when there is more than one.
 *
 * A menu over that fixed list rather than ModelSelector: this picks one of a
 * handful of configured provider and model pairs, not a model from the catalog.
 */
function WarpModelSwitcher({ models, selectedModel, onSelectModel }: WarpModelSwitcherProps) {
	const { t } = useTranslation("shell");
	if (!selectedModel) return null;
	const chipClassName = "text-muted-foreground flex min-w-0 items-center gap-1.5 rounded px-1 py-0.5 text-xs";
	if (models.length < 2) {
		return (
			<span className={chipClassName} data-testid="warp-composer-model">
				<WarpModelName model={selectedModel} />
			</span>
		);
	}
	return (
		<DropdownMenu>
			<DropdownMenuTrigger asChild>
				<button
					type="button"
					aria-label={t("warp.composer.switchModel")}
					className={cn(chipClassName, "hover:text-foreground hover:bg-accent cursor-pointer transition-colors")}
					data-testid="warp-composer-model"
				>
					<WarpModelName model={selectedModel} />
					<ChevronDown className="size-3 shrink-0 opacity-60" />
				</button>
			</DropdownMenuTrigger>
			<DropdownMenuContent side="top" align="start" className="max-w-72" data-testid="warp-model-menu">
				<DropdownMenuRadioGroup
					value={warpModelKey(selectedModel)}
					onValueChange={(key) => {
						const next = models.find((model) => warpModelKey(model) === key);
						if (next) onSelectModel(next);
					}}
				>
					{models.map((model, index) => (
						<DropdownMenuRadioItem
							key={warpModelKey(model)}
							value={warpModelKey(model)}
							className="text-xs"
							data-testid={`warp-model-option-${model.provider}-${model.model}`}
						>
							<WarpModelName model={model} />
							{index === 0 && <span className="text-muted-foreground ml-auto pl-2 text-[10px]">{t("warp.composer.defaultModel")}</span>}
						</DropdownMenuRadioItem>
					))}
				</DropdownMenuRadioGroup>
			</DropdownMenuContent>
		</DropdownMenu>
	);
}