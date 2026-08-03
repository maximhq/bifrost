import { createFileRoute } from "@tanstack/react-router";
import BrandingPage from "./page";

export const Route = createFileRoute("/workspace/config/branding")({
	component: BrandingPage,
});
