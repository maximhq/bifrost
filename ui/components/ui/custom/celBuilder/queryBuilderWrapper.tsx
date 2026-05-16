/**
 * Query Builder Wrapper Component
 * Provides styled wrapper with custom CSS for react-querybuilder
 */

import { ReactNode } from "react";
import "./queryBuilderWrapper.css";

interface QueryBuilderWrapperProps {
	children: ReactNode;
}

export function QueryBuilderWrapper({ children }: QueryBuilderWrapperProps) {
	return <div className="query-builder-wrapper">{children}</div>;
}