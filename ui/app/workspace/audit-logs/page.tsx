import AuditLogsView from "@enterprise/components/audit-logs/auditLogsView";

export default function AuditLogsPage() {
	return (
		<div className="no-padding-parent no-border-parent bg-background flex h-[calc(var(--app-content-viewport)_-_16px)] w-full">
			<AuditLogsView />
		</div>
	);
}