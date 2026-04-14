import { createFileRoute } from "@tanstack/react-router";
import ModelCatalogPage from "./page";

export const Route = createFileRoute("/workspace/model-catalog")({
	component: ModelCatalogPage,
});