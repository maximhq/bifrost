import { createFileRoute } from "@tanstack/react-router";
import LoggingPage from "./page";

export const Route = createFileRoute("/workspace/config/logging")({
	component: LoggingPage,
});