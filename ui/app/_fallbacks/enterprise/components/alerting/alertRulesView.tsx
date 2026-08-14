import AlertingPlaceholderView from "./alertingPlaceholderView";

export default function AlertRulesView() {
	return (
		<AlertingPlaceholderView
			title="解锁告警规则以实现主动监控"
			description="此功能属于 Bifrost 企业版许可的一部分。定义告警规则，在预算、延迟和性能劣化问题演变成事故之前及时发现。"
			readmeLink="https://docs.getbifrost.ai/enterprise/alerting/alert-rules"
			testIdPrefix="alert-rules"
		/>
	);
}