import { createFileRoute } from "@tanstack/react-router";
import ClaudeCodePage from "./page";

export const Route = createFileRoute("/workspace/config/claude-code")({
	component: ClaudeCodePage,
});