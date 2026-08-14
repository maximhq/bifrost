import AlertingPlaceholderView from "./alertingPlaceholderView";

export default function AlertHistoryView() {
	return (
		<AlertingPlaceholderView
			title="解锁告警历史以实现主动监控"
			description="此功能属于 Bifrost 企业版许可的一部分。在一个地方查看告警投递结果、失败和解决事件。"
			readmeLink="https://docs.getbifrost.ai/enterprise/alerting/alert-history"
			testIdPrefix="alert-history"
		/>
	);
}