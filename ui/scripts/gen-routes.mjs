/**
 * One-shot codegen for Phase 2 of the Next.js -> TanStack Router migration.
 *
 * Walks app/** for page.tsx / layout.tsx and emits thin wrapper routes
 * under src/routes/** that import the original components from @/app.
 *
 * Special cases:
 *   - app/layout.tsx              -> handled by src/routes/__root.tsx (manual)
 *   - app/not-found.tsx           -> wired into __root.tsx notFoundComponent (manual)
 *   - app/page.tsx (redirect)     -> emitted as beforeLoad redirect
 *   - app/workspace/page.tsx      -> emitted as beforeLoad redirect
 *   - app/workspace/config/large-payload/page.tsx -> emitted as beforeLoad redirect
 *   - app/_fallbacks/**           -> skipped (not routes; alias targets)
 *   - app/enterprise/**           -> skipped (alias target only when enterprise build)
 *
 * Idempotent: deletes generated files (anything not in KEEP) before writing.
 */
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const ROOT = path.resolve(__dirname, "..");
const APP = path.join(ROOT, "app");
const ROUTES = path.join(ROOT, "src", "routes");

// Files in src/routes that the script must NOT delete (manually authored).
const KEEP = new Set([
	path.join(ROUTES, "__root.tsx"),
	path.join(ROUTES, "routeTree.gen.ts"),
]);

// Pages that are pure redirects — emit a beforeLoad wrapper instead of
// importing the original component (which still has the now-broken
// next/navigation `redirect` call).
const REDIRECT_PAGES = {
	"app/page.tsx": "/login",
	"app/workspace/page.tsx": "/workspace/dashboard",
	"app/workspace/config/large-payload/page.tsx": "/workspace/config/client-settings",
};

const SKIP_PREFIXES = ["app/_fallbacks/", "app/enterprise/"];
const SKIP_FILES = new Set(["app/layout.tsx", "app/not-found.tsx"]);

function walk(dir, out = []) {
	for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
		const full = path.join(dir, entry.name);
		if (entry.isDirectory()) walk(full, out);
		else out.push(full);
	}
	return out;
}

function rmGenerated(dir) {
	if (!fs.existsSync(dir)) return;
	for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
		const full = path.join(dir, entry.name);
		if (entry.isDirectory()) {
			rmGenerated(full);
			// remove empty dirs
			if (fs.readdirSync(full).length === 0) fs.rmdirSync(full);
		} else if (!KEEP.has(full)) {
			fs.unlinkSync(full);
		}
	}
}

/**
 * Compute the TanStack Router route id for a wrapper file.
 *
 * Conventions used here (directory-based file routing):
 *   src/routes/index.tsx                     -> "/"
 *   src/routes/login/index.tsx               -> "/login/"
 *   src/routes/login/route.tsx               -> "/login"
 *   src/routes/workspace/dashboard/index.tsx -> "/workspace/dashboard/"
 *
 * The router-plugin will validate / rewrite these on first build.
 */
function routeId(relWrapperPath) {
	// relWrapperPath like "workspace/dashboard/index.tsx" or "index.tsx"
	const parsed = path.parse(relWrapperPath);
	const isIndex = parsed.name === "index";
	const dir = parsed.dir; // "" for root index
	if (isIndex) {
		if (dir === "") return "/";
		return "/" + dir.replace(/\\/g, "/") + "/";
	}
	// route.tsx (layout)
	return "/" + dir.replace(/\\/g, "/");
}

function pageWrapper(routeIdStr, importPath) {
	return `import { createFileRoute } from "@tanstack/react-router";
import Page from "${importPath}";

export const Route = createFileRoute("${routeIdStr}")({
\tcomponent: Page,
});
`;
}

function layoutWrapper(routeIdStr, importPath) {
	return `import { Outlet, createFileRoute } from "@tanstack/react-router";
import Layout from "${importPath}";

export const Route = createFileRoute("${routeIdStr}")({
\tcomponent: LayoutRoute,
});

function LayoutRoute() {
\treturn (
\t\t<Layout>
\t\t\t<Outlet />
\t\t</Layout>
\t);
}
`;
}

function redirectWrapper(routeIdStr, target) {
	return `import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("${routeIdStr}")({
\tbeforeLoad: () => {
\t\tthrow redirect({ to: "${target}" });
\t},
});
`;
}

function appRelative(absFile) {
	return path.relative(ROOT, absFile).replace(/\\/g, "/");
}

function isSkipped(rel) {
	if (SKIP_FILES.has(rel)) return true;
	return SKIP_PREFIXES.some((p) => rel.startsWith(p));
}

function importPathFor(absAppFile) {
	// "/Users/.../ui/app/workspace/dashboard/page.tsx" -> "@/app/workspace/dashboard/page"
	const rel = path.relative(ROOT, absAppFile).replace(/\\/g, "/");
	return "@/" + rel.replace(/\.tsx$/, "");
}

function wrapperFileFor(absAppFile) {
	// app/workspace/dashboard/page.tsx -> src/routes/workspace/dashboard/index.tsx
	// app/workspace/dashboard/layout.tsx -> src/routes/workspace/dashboard/route.tsx
	// app/page.tsx -> src/routes/index.tsx
	const rel = path.relative(APP, absAppFile).replace(/\\/g, "/");
	const parsed = path.parse(rel);
	const targetName = parsed.name === "page" ? "index.tsx" : "route.tsx";
	return path.join(ROUTES, parsed.dir, targetName);
}

function ensureDir(dir) {
	fs.mkdirSync(dir, { recursive: true });
}

function main() {
	console.log("Cleaning previously generated routes…");
	rmGenerated(ROUTES);

	const all = walk(APP).filter((f) => /\/(page|layout)\.tsx$/.test(f.replace(/\\/g, "/")));

	let count = 0;
	for (const abs of all) {
		const rel = appRelative(abs);
		if (isSkipped(rel)) continue;

		const wrapperAbs = wrapperFileFor(abs);
		const wrapperRel = path.relative(ROUTES, wrapperAbs).replace(/\\/g, "/");
		const id = routeId(wrapperRel);

		let body;
		if (REDIRECT_PAGES[rel]) {
			body = redirectWrapper(id, REDIRECT_PAGES[rel]);
		} else if (rel.endsWith("/page.tsx") || rel === "app/page.tsx") {
			body = pageWrapper(id, importPathFor(abs));
		} else {
			body = layoutWrapper(id, importPathFor(abs));
		}

		ensureDir(path.dirname(wrapperAbs));
		fs.writeFileSync(wrapperAbs, body);
		count++;
	}

	console.log(`Generated ${count} route wrappers under src/routes/.`);
}

main();
