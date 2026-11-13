import type { ModelRouter } from "@/lib/types/complexityRouter";

// matchesRouterSearch reports whether a router matches the routers list's
// search: its name, description or id contains the text, ignoring case and
// the spaces around the text. An empty search matches every router. The list
// endpoint returns every router, so filtering the loaded list is complete.
export function matchesRouterSearch(router: Pick<ModelRouter, "id" | "name" | "description">, search: string): boolean {
	const needle = search.trim().toLowerCase();
	if (!needle) return true;
	return [router.name, router.description ?? "", router.id].some((field) => field.toLowerCase().includes(needle));
}