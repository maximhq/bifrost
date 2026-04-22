import { RouterProvider, createRouter } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

// Tailwind + global styles (also declares @font-face for local Geist fonts).
import "@/app/globals.css";

import { routeTree } from "./routeTree.gen";
import { ErrorComponent } from "./__error";
import { NotFoundComponent } from "./__notFound";

const router = createRouter({
	routeTree,
	defaultPreload: "intent",
	scrollRestoration: true,
	notFoundMode: "root",
	defaultNotFoundComponent: NotFoundComponent,
	defaultErrorComponent: ErrorComponent,
});

declare module "@tanstack/react-router" {
	interface Register {
		router: typeof router;
	}
}

const rootEl = document.getElementById("root");
if (!rootEl) throw new Error("Root element #root not found");

createRoot(rootEl).render(
	<StrictMode>
		<RouterProvider router={router} />
	</StrictMode>,
);