// Fallback stub for non-enterprise builds. The OAuth discover callback is an
// enterprise-only flow, so this placeholder simply lets the import path resolve
// when `@enterprise` points at `app/_fallbacks/enterprise`.
export default function DiscoverCallbackView() {
	return null;
}