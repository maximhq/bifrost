import AuditLogsView from "@enterprise/components/audit-logs/auditLogsView";

export default function AuditLogsPage() {
	return (
		<div className="no-padding-parent mx-auto h-[calc(100dvh-1rem)] w-full p-4 flex flex-col">
			<AuditLogsView />
		</div>
	);
}