/**
 * Routing Rules Layout
 * Provides layout structure only and renders children. RBAC gating is not
 * applied here; child components (e.g. routingRulesView.tsx) perform RBAC
 * checks via useRbac.
 */

import { Metadata } from "next";

export const metadata: Metadata = {
	title: "Routing Rules | Bifrost",
	description: "Manage CEL-based routing rules for intelligent request routing",
};

interface RoutingRulesLayoutProps {
	children: React.ReactNode;
}

export default function RoutingRulesLayout({ children }: RoutingRulesLayoutProps) {
	// Note: useRbac is a hook, so we use it at the top level
	// For server components, RBAC is checked client-side in the child components
	// This layout just provides the structure
	return <>{children}</>;
}
