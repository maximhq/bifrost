import * as React from "react";
import { Streamdown, type StreamdownProps } from "streamdown";
import { code } from "@streamdown/code";
import "streamdown/styles.css";

import { cn } from "@/components/ui/utils";

interface MarkdownProps {
	content: string;
	className?: string;
	/** Set to true when content is actively streaming */
	isStreaming?: boolean;
	/** Enable per-word fade-in animation during streaming */
	animated?: boolean | StreamdownProps["animated"];
	/** Show a caret indicator while streaming ("block" | "circle") */
	caret?: StreamdownProps["caret"];
	/** Show copy/download controls on code blocks, tables, etc. */
	controls?: StreamdownProps["controls"];
	/** Shiki themes for code highlighting [light, dark] */
	shikiTheme?: StreamdownProps["shikiTheme"];
	/** Custom component overrides */
	components?: StreamdownProps["components"];
}

function Markdown({
	content,
	className,
	isStreaming = false,
	animated = false,
	caret,
	controls = true,
	shikiTheme = ["github-light", "github-dark"],
	components,
}: MarkdownProps) {
	return (
		<div className={cn("text-sm text-foreground", className)}>
			<Streamdown
				mode={isStreaming ? "streaming" : "static"}
				isAnimating={isStreaming}
				animated={animated}
				caret={caret}
				controls={controls}
				shikiTheme={shikiTheme}
				plugins={{ code }}
				components={components}
			>
				{content}
			</Streamdown>
		</div>
	);
}

export { Markdown, type MarkdownProps };
