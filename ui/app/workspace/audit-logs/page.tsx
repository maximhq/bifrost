import AuditLogsView from "@enterprise/components/audit-logs/auditLogsView";

export default function AuditLogsPage() {
	return (
		<div className="no-padding-parent no-border-parent bg-background flex h-[calc(100vh-16px)] w-full">
			<AuditLogsView />
		</div>
	);
}