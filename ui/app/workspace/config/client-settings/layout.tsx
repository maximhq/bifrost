import { createFileRoute } from "@tanstack/react-router";
import ClientSettingsPage from "./page";

export const Route = createFileRoute("/workspace/config/client-settings")({
	component: ClientSettingsPage,
});