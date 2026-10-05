import { Button } from "@/components/ui/button";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuRadioGroup,
	DropdownMenuRadioItem,
	DropdownMenuTrigger,
} from "@/components/ui/dropdownMenu";
import { matchWarpCommands, resolveWarpCommand, type WarpCommand } from "@/components/warp/warpCommands";
import { cn } from "@/lib/utils";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import type { WarpModel } from "@/lib/types/warp";
import { hasProviderIcon, warpModelKey, warpModelLabel } from "./warpComposer.utils";
import { Link } from "@tanstack/react-router";
import { ArrowUp, ChevronDown, Settings2, Square } from "lucide-react";
import { useState } from "react";
import TextareaAutosize from "react-textarea-autosize";

interface WarpComposerProps {
	onCommand?: (command: WarpCommand) => void;
	isStreaming: boolean;
	disabled?: boolean;
	/** A question card is showing above and already supplies the top gap. */
	attached?: boolean;
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
	attached,
	models,
	selectedModel,
	onSelectModel,
	canConfigure,
	onCommand,
	onSend,
	onQueue,
	onStop,
}: WarpComposerProps) {
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
		<div className={cn("shrink-0 px-3 pb-3", attached ? "pt-0" : "pt-3")}>
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
							<span className="text-muted-foreground truncate text-xs font-normal">{command.description}</span>
						</button>
					))}
				</div>
			)}
			<div className="focus-within:border-ring bg-background dark:bg-card flex flex-col gap-2 rounded-lg border p-2 transition-colors">
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
					placeholder={
						isStreaming && onQueue ? "Ask a follow-up, it is sent when this answer finishes..." : "Ask about your Bifrost data..."
					}
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
								aria-label="Warp settings"
								title="Warp settings"
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
							aria-label="Stop"
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
							aria-label="Send"
							data-testid="warp-send-btn"
							className="size-7 shrink-0 rounded-full"
						>
							<ArrowUp className="size-3.5" />
						</Button>
					)}
				</div>
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
					aria-label="Switch model"
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
							{index === 0 && <span className="text-muted-foreground ml-auto pl-2 text-[10px]">Default</span>}
						</DropdownMenuRadioItem>
					))}
				</DropdownMenuRadioGroup>
			</DropdownMenuContent>
		</DropdownMenu>
	);
}
