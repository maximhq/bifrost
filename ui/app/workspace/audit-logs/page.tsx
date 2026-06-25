import AuditLogsView from "@enterprise/components/audit-logs/auditLogsView";

export default function AuditLogsPage() {
	return (
		<div className="no-padding-parent mx-auto flex h-[calc(100dvh-1rem)] w-full flex-col p-4">
			<AuditLogsView />
		</div>
	);
}