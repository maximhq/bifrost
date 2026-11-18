import { createFileRoute } from "@tanstack/react-router";
import DatasheetPage from "./page";

export const Route = createFileRoute("/workspace/datasheet-editor")({
	component: DatasheetPage,
});