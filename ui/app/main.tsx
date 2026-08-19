import { RouterProvider, createRouter, parseSearchWith, stringifySearchWith } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

// Tailwind + global styles (also declares @font-face for local Geist fonts).
import "@/app/globals.css";

import { ErrorComponent } from "./__error";
import { NotFoundComponent } from "./__notFound";
import { routeTree } from "./routeTree.gen";

// Only JSON.parse structured values (objects/arrays). Plain strings and numbers
// stay as-is so large numeric IDs don't lose precision through Number coercion.
function safeJsonParse(value: string): unknown {
	const c = value[0];
	if (c === "{" || c === "[") {
		try {
			return JSON.parse(value);
		} catch {
			return value;
		}
	}
	return value;
}

// Keep query params compatible with nuqs parsers: primitive strings stay raw
// (so numeric-looking IDs are not JSON-quoted), while arrays use comma lists.
function stringifySearchValue(value: unknown): string {
	if (Array.isArray(value)) return value.map(String).join(",");
	if (value === null || value === undefined) return "";
	if (typeof value === "string" || typeof value === "number" || typeof value === "boolean") {
		return String(value);
	}
	return JSON.stringify(value);
}

const router = createRouter({
	routeTree,
	defaultPreload: "intent",
	scrollRestoration: true,
	notFoundMode: "root",
	defaultNotFoundComponent: NotFoundComponent,
	defaultErrorComponent: ErrorComponent,
	parseSearch: parseSearchWith(safeJsonParse),
	stringifySearch: stringifySearchWith(stringifySearchValue),
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