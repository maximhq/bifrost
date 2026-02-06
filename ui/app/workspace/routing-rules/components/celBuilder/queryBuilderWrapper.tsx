/**
 * Query Builder Wrapper Component
 * Provides styled wrapper with custom CSS for react-querybuilder
 */

"use client";

import { ReactNode } from "react";

interface QueryBuilderWrapperProps {
	children: ReactNode;
}

export function QueryBuilderWrapper({ children }: QueryBuilderWrapperProps) {
	return (
		<div className="query-builder-wrapper">
			<style jsx global>{`
				.query-builder-wrapper .queryBuilder {
					font-family: inherit;
				}

				.query-builder-wrapper .ruleGroup {
					background-color: hsl(var(--muted) / 0.3);
					border: 1px solid hsl(var(--border));
					border-radius: 0.5rem;
					margin-left: 0.07rem;
					margin-bottom: 0.5rem;
				}

				.query-builder-wrapper .ruleGroup .ruleGroup {
					background-color: hsl(var(--background));
				}

				.query-builder-wrapper .ruleGroup-header {
					display: flex;
					gap: 0.5rem;
					padding-bottom: 0.5rem;
					align-items: center;
					flex-wrap: wrap;
				}

				.query-builder-wrapper .ruleGroup-body {
					display: flex;
					flex-direction: column;
					padding-left: 1rem;
				}

				.query-builder-wrapper .rule {
					display: flex;
					gap: 0.5rem;
					align-items: center;
					padding: 0.5rem;
					background-color: hsl(var(--background));
					border: 1px solid hsl(var(--border));
					border-radius: 0.375rem;
				}

				.query-builder-wrapper .rule > * {
					flex-shrink: 0;
				}

				.query-builder-wrapper .ruleGroup-addRule,
				.query-builder-wrapper .ruleGroup-addGroup {
					margin-top: 0.5rem;
				}

				.query-builder-wrapper .ruleGroup-header .ruleGroup-addRule,
				.query-builder-wrapper .ruleGroup-header .ruleGroup-addGroup {
					margin-top: 0;
				}

				.query-builder-wrapper > .queryBuilder > .ruleGroup > .ruleGroup-header .ruleGroup-remove {
					margin-left: auto;
				}

				.query-builder-wrapper .ruleGroup .ruleGroup .ruleGroup-header .ruleGroup-remove {
					margin-left: 0.5rem;
				}

				/* Branch line styles */
				.query-builder-wrapper .queryBuilder-branches .ruleGroup-body {
					padding-left: 1rem;
				}

				.query-builder-wrapper .queryBuilder-branches .rule,
				.query-builder-wrapper .queryBuilder-branches .ruleGroup {
					position: relative;
				}

				.query-builder-wrapper .queryBuilder-branches .rule::before,
				.query-builder-wrapper .queryBuilder-branches .rule::after,
				.query-builder-wrapper .queryBuilder-branches .ruleGroup::before,
				.query-builder-wrapper .queryBuilder-branches .ruleGroup::after {
					content: "";
					position: absolute;
					background-color: hsl(var(--border));
				}

				/* Validation styles */
				.query-builder-wrapper .validateQuery .queryBuilder .ruleGroup.queryBuilder-invalid {
					background-color: color-mix(in srgb, rebeccapurple, transparent 60%);
				}

				.query-builder-wrapper .validateQuery .queryBuilder .ruleGroup.queryBuilder-invalid .ruleGroup-addRule {
					font-weight: bold !important;
				}

				.query-builder-wrapper .validateQuery .queryBuilder .rule.queryBuilder-invalid .rule-value {
					background-color: color-mix(in srgb, rebeccapurple, transparent 60%);
				}

				.query-builder-wrapper .validateQuery .queryBuilder .rule.queryBuilder-invalid .rule-value::placeholder {
					color: color-mix(in srgb, rebeccapurple, black 30%);
				}
			`}</style>
			{children}
		</div>
	);
}
