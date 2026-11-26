import { createFileRoute } from "@tanstack/react-router";
import VectorStoresPage from "./page";

export const Route = createFileRoute("/workspace/config/vector-stores")({
	component: VectorStoresPage,
});