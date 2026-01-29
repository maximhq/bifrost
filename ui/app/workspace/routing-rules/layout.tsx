/**
 * Routing Rules Layout
 * Provides RBAC gating and layout structure for routing rules pages
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
